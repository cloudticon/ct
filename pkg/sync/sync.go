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

	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/fatih/color"
)

// SyncRule describes one local->container sync mapping.
type SyncRule struct {
	From    string
	To      string
	Exclude []string
	Polling bool
}

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

// RunWithReady is like Run but calls ready after the initial sync completes.
// The ready callback is guaranteed to be called exactly once before returning,
// even if an error occurs during initial sync.
func (s *Syncer) RunWithReady(ctx context.Context, ready func()) error {
	signalReady := func() {}
	if ready != nil {
		var once stdsync.Once
		signalReady = func() { once.Do(ready) }
		defer signalReady()
	}

	if s.exec == nil {
		return errors.New("k8s client is required")
	}
	if strings.TrimSpace(s.rule.From) == "" || strings.TrimSpace(s.rule.To) == "" {
		return errors.New("sync rule requires non-empty from and to")
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

	watcher, err := NewWatcher(s.rule.From, s.rule.Exclude, s.rule.Polling)
	if err != nil {
		return err
	}

	for changes := range watcher.Watch(ctx) {
		if err := s.incrementalSync(ctx, changes); err != nil {
			log.Printf("%s incremental sync error: %v", color.YellowString("[sync]"), err)
		}
	}

	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return ctx.Err()
}

func (s *Syncer) execStream(ctx context.Context, cmd []string, stdin io.Reader) error {
	return s.exec.ExecPod(ctx, s.namespace, s.podName, k8s.ExecOpts{
		Command: cmd,
		Stdin:   stdin,
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	})
}

func (s *Syncer) execSimple(ctx context.Context, cmd []string) error {
	return s.exec.ExecPod(ctx, s.namespace, s.podName, k8s.ExecOpts{
		Command: cmd,
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	})
}

func (s *Syncer) ensureRemoteDir(ctx context.Context) error {
	return s.execSimple(ctx, []string{"mkdir", "-p", s.rule.To})
}

func (s *Syncer) initialSync(ctx context.Context) error {
	files, err := collectFiles(s.rule.From, s.rule.Exclude)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	if err := s.ensureRemoteDir(ctx); err != nil {
		return fmt.Errorf("creating remote directory %s: %w", s.rule.To, err)
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
	for _, rel := range deleted {
		remote := filepath.ToSlash(filepath.Join(s.rule.To, rel))
		if err := s.execSimple(ctx, []string{"rm", "-rf", remote}); err != nil {
			return err
		}
		deletedCount++
	}

	log.Printf("%s %d files synced, %d deleted", color.CyanString("[sync]"), syncedCount, deletedCount)
	return nil
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
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}

	tw := tar.NewWriter(w)
	defer tw.Close()

	var total int64
	for _, rel := range relPaths {
		rel = filepath.ToSlash(rel)
		srcPath := filepath.Join(absRoot, filepath.FromSlash(rel))
		info, err := os.Stat(srcPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return total, err
		}
		if info.IsDir() {
			continue
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return total, err
		}
		header.Name = rel

		if err := tw.WriteHeader(header); err != nil {
			return total, err
		}
		file, err := os.Open(srcPath)
		if err != nil {
			return total, err
		}
		n, err := io.Copy(tw, file)
		_ = file.Close()
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, tw.Close()
}

func writeTarFromRelativePaths(w io.Writer, root string, relPaths []string) (int, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}

	tw := tar.NewWriter(w)
	defer tw.Close()

	written := 0
	for _, rel := range relPaths {
		rel = filepath.ToSlash(rel)
		srcPath := filepath.Join(absRoot, filepath.FromSlash(rel))
		info, err := os.Stat(srcPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return written, err
		}
		if info.IsDir() {
			continue
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return written, err
		}
		header.Name = rel
		if err := tw.WriteHeader(header); err != nil {
			return written, err
		}

		file, err := os.Open(srcPath)
		if err != nil {
			return written, err
		}
		_, err = io.Copy(tw, file)
		_ = file.Close()
		if err != nil {
			return written, err
		}
		written++
	}
	return written, tw.Close()
}
