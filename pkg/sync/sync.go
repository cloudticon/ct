package sync

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"time"

	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/fatih/color"
)

// SyncRule describes one local->container sync mapping.
type SyncRule struct {
	From    string
	To      string
	Exclude []string
	Polling bool
	// Container is the pod container the files are synced into. Empty means
	// the API server default, which only works for single-container pods.
	Container string
}

// podCheckInterval is how often a running syncer checks whether its pod
// was replaced.
var podCheckInterval = 3 * time.Second

// Syncer performs initial and incremental sync.
type Syncer struct {
	exec      k8s.PodExecutor
	namespace string
	selector  map[string]string
	rule      SyncRule
	podName   string
}

// NewSyncer creates a Syncer instance bound to the given pod-executor port
// and namespace. Selector identifies the workload pod the sync targets.
func NewSyncer(exec k8s.PodExecutor, namespace string, selector map[string]string, rule SyncRule) *Syncer {
	return &Syncer{
		exec:      exec,
		namespace: namespace,
		selector:  selector,
		rule:      rule,
	}
}

// Run starts initial sync and then incremental sync on file changes.
func (s *Syncer) Run(ctx context.Context) error {
	return s.RunWithReady(ctx, nil)
}

// RunWithReady is like Run but reports the outcome of the initial sync. ready
// is called exactly once: with nil as soon as the initial sync completed, or
// with the error that prevented it (before RunWithReady returns that error).
// Callers can therefore tell "synced" from "failed" without racing the
// returned error.
func (s *Syncer) RunWithReady(ctx context.Context, ready func(error)) error {
	signalled := false
	err := s.run(ctx, func() {
		signalled = true
		if ready != nil {
			ready(nil)
		}
	})
	if !signalled {
		if err == nil {
			err = errors.New("sync stopped before the initial sync completed")
		}
		if ready != nil {
			ready(err)
		}
	}
	if err != nil && errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func (s *Syncer) run(ctx context.Context, signalReady func()) error {
	if s.exec == nil {
		return errors.New("k8s client is required")
	}
	if strings.TrimSpace(s.rule.From) == "" || strings.TrimSpace(s.rule.To) == "" {
		return errors.New("sync rule requires non-empty from and to")
	}

	watcher, err := NewWatcher(s.rule.From, s.rule.Exclude, s.rule.Polling)
	if err != nil {
		return err
	}
	// Watch before the initial snapshot is taken: a file saved while the
	// initial sync is uploading is then reported afterwards instead of being
	// lost between the snapshot and the start of the watcher.
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	changes, err := watcher.Watch(watchCtx)
	if err != nil {
		return err
	}

	pod, err := s.exec.WaitPod(ctx, s.namespace, s.selector)
	if err != nil {
		return err
	}
	s.podName = pod

	if err := s.initialSync(ctx); err != nil {
		return fmt.Errorf("initial sync failed: %w", err)
	}

	signalReady()

	podCheck := time.NewTicker(podCheckInterval)
	defer podCheck.Stop()
	for {
		select {
		case batch, ok := <-changes:
			if !ok {
				if err := watcher.Err(); err != nil {
					return fmt.Errorf("watching %s stopped: %w", s.rule.From, err)
				}
				if errors.Is(ctx.Err(), context.Canceled) {
					return nil
				}
				return ctx.Err()
			}
			if s.podName == "" {
				// The last re-sync failed; a successful full sync also
				// covers this batch.
				s.followPod(ctx)
				continue
			}
			if err := s.incrementalSync(ctx, batch); err != nil && ctx.Err() == nil {
				log.Printf("%s incremental sync error: %v", color.YellowString("[sync]"), err)
				// The usual cause is that the pod is gone; move on to its
				// replacement (the full sync covers this batch as well).
				s.followPod(ctx)
			}
		case <-podCheck.C:
			s.followPod(ctx)
		}
	}
}

// followPod re-resolves the target pod and, when it has been replaced (a
// rollout, an eviction, or the rollout triggered by ct dev's own workload
// patch while the old pod was still running), runs a full sync into the new
// pod: it starts from the image and has none of the synced files.
func (s *Syncer) followPod(ctx context.Context) {
	pod, err := s.exec.WaitPod(ctx, s.namespace, s.selector)
	if err != nil || pod == s.podName {
		return
	}
	previous := s.podName
	s.podName = pod
	if err := s.initialSync(ctx); err != nil {
		if ctx.Err() == nil {
			log.Printf("%s re-sync into new pod %s failed, will retry: %v", color.YellowString("[sync]"), pod, err)
		}
		s.podName = "" // retry on the next check
		return
	}
	if previous != "" {
		log.Printf("%s pod %s replaced by %s, re-synced %s", color.CyanString("[sync]"), previous, pod, s.rule.From)
	}
}

func (s *Syncer) execStream(ctx context.Context, cmd []string, stdin io.Reader) error {
	return s.runInPod(ctx, cmd, stdin)
}

func (s *Syncer) execSimple(ctx context.Context, cmd []string) error {
	return s.runInPod(ctx, cmd, nil)
}

// runInPod runs cmd in the target container. On failure the error carries
// what the command printed on stderr: "exit code 2" alone does not tell the
// user that tar could not write to the target directory.
func (s *Syncer) runInPod(ctx context.Context, cmd []string, stdin io.Reader) error {
	stderr := &boundedBuffer{max: 4 << 10}
	err := s.exec.ExecPod(ctx, s.namespace, s.podName, k8s.ExecOpts{
		Container: s.rule.Container,
		Command:   cmd,
		Stdin:     stdin,
		Stdout:    io.Discard,
		Stderr:    stderr,
	})
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		err = fmt.Errorf("%w: %s", err, msg)
	}
	if isMissingToolError(err) {
		container := "the target container"
		if s.rule.Container != "" {
			container = fmt.Sprintf("container %q", s.rule.Container)
		}
		err = fmt.Errorf("%w (ct dev sync runs tar, mkdir and rm in %s; the image must provide them)", err, container)
	}
	return err
}

func isMissingToolError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "exit code 126") ||
		strings.Contains(msg, "exit code 127")
}

// boundedBuffer keeps the first max bytes written to it. Exec streams may
// write from another goroutine, hence the lock.
type boundedBuffer struct {
	mu  stdsync.Mutex
	buf bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (s *Syncer) ensureRemoteDir(ctx context.Context) error {
	return s.execSimple(ctx, []string{"mkdir", "-p", s.rule.To})
}

func (s *Syncer) initialSync(ctx context.Context) error {
	files, err := collectFiles(s.rule.From, s.rule.Exclude)
	if err != nil {
		return err
	}

	// Create the target even when there is nothing to copy yet: later
	// incremental syncs extract into it.
	if err := s.ensureRemoteDir(ctx); err != nil {
		return fmt.Errorf("creating remote directory %s: %w", s.rule.To, err)
	}
	if len(files) == 0 {
		return nil
	}

	buf := bytes.NewBuffer(nil)
	if _, err := writeTarFromFiles(buf, s.rule.From, files); err != nil {
		return err
	}

	cmd := []string{"tar", "xf", "-", "-C", s.rule.To}
	if err := s.execStream(ctx, cmd, bytes.NewReader(buf.Bytes())); err != nil {
		return err
	}
	return nil
}

func (s *Syncer) incrementalSync(ctx context.Context, changes []FileChange) error {
	if len(changes) == 0 {
		return nil
	}

	changed := make([]string, 0, len(changes))
	deleted := make([]string, 0, len(changes))
	for _, ch := range changes {
		switch ch.Type {
		case ChangeCreate, ChangeModify:
			changed = append(changed, ch.Path)
		case ChangeDelete:
			deleted = append(deleted, ch.Path)
		}
	}

	syncedCount := 0
	if len(changed) > 0 {
		tarBuf := bytes.NewBuffer(nil)
		written, err := writeTarFromRelativePaths(tarBuf, s.rule.From, changed)
		if err != nil {
			return err
		}
		if written > 0 {
			cmd := []string{"tar", "xf", "-", "-C", s.rule.To}
			if err := s.execStream(ctx, cmd, bytes.NewReader(tarBuf.Bytes())); err != nil {
				return err
			}
			syncedCount = written
		}
	}

	deletedCount := 0
	for _, batch := range rmBatches(s.rule.To, deleted) {
		if err := s.execSimple(ctx, append([]string{"rm", "-rf"}, batch...)); err != nil {
			return err
		}
		deletedCount += len(batch)
	}

	log.Printf("%s %d files synced, %d deleted", color.CyanString("[sync]"), syncedCount, deletedCount)
	return nil
}

// maxRmArgBytes caps the size of one rm command line, far below ARG_MAX.
const maxRmArgBytes = 64 * 1024

// rmBatches maps relative paths to container paths under to and groups them
// into as few rm invocations as the argument size limit allows. Deleting a
// directory produces one event per file; one exec per path would cost one
// API round trip each.
func rmBatches(to string, rels []string) [][]string {
	var batches [][]string
	var current []string
	size := 0
	for _, rel := range rels {
		remote := filepath.ToSlash(filepath.Join(to, rel))
		if len(current) > 0 && size+len(remote)+1 > maxRmArgBytes {
			batches = append(batches, current)
			current, size = nil, 0
		}
		current = append(current, remote)
		size += len(remote) + 1
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func collectFiles(root string, exclude []string) ([]string, error) {
	excluder, err := newPathExcluder(exclude)
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	var files []string
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}

		if excluder.IsExcluded(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

func writeTarFromFiles(w io.Writer, root string, relPaths []string) (int64, error) {
	_, size, err := writeTar(w, root, relPaths)
	return size, err
}

func writeTarFromRelativePaths(w io.Writer, root string, relPaths []string) (int, error) {
	files, _, err := writeTar(w, root, relPaths)
	return files, err
}

// writeTar archives the regular files among relPaths (relative to root) and
// returns how many files and bytes it wrote. Paths that vanished, and
// anything that is not a regular file (directories, FIFOs, sockets,
// devices), are skipped: opening a FIFO blocks forever and sockets cannot be
// archived. Each file is read completely before its header is written, so a
// file that changes while being archived yields a consistent entry instead
// of a corrupt archive ("archive/tar: write too long"). Files that cannot be
// read (symlink loops, other users' 0600 files) are skipped with a warning
// rather than failing the whole sync.
func writeTar(w io.Writer, root string, relPaths []string) (int, int64, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return 0, 0, err
	}

	tw := tar.NewWriter(w)
	files := 0
	var size int64
	var skipped []string
	for _, rel := range relPaths {
		rel = filepath.ToSlash(rel)
		srcPath := filepath.Join(absRoot, filepath.FromSlash(rel))
		info, err := os.Stat(srcPath)
		if err != nil {
			if !os.IsNotExist(err) {
				skipped = append(skipped, err.Error())
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			if !os.IsNotExist(err) {
				skipped = append(skipped, err.Error())
			}
			continue
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return files, size, err
		}
		header.Name = rel
		header.Size = int64(len(data))
		if err := tw.WriteHeader(header); err != nil {
			return files, size, err
		}
		if _, err := tw.Write(data); err != nil {
			return files, size, err
		}
		files++
		size += int64(len(data))
	}
	if len(skipped) > 0 {
		log.Printf("%s skipped %d unreadable file(s), e.g. %s (exclude them to silence this)",
			color.YellowString("[sync]"), len(skipped), skipped[0])
	}
	return files, size, tw.Close()
}
