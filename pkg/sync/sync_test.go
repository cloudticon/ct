package sync

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePodExecutor implements k8s.PodExecutor for tests. WaitFn / ExecFn are
// optional hooks; zero-value is "always return pod-1 from WaitPod and succeed
// silently on ExecPod".
type fakePodExecutor struct {
	mu        sync.Mutex
	WaitFn    func(ctx context.Context, ns string, sel k8s.Selector) (string, error)
	ExecFn    func(ctx context.Context, ns, pod string, opts k8s.ExecOpts) error
	ExecCalls []k8s.ExecOpts // recorded ExecPod opts (Command + body via tar reader)
}

func (f *fakePodExecutor) WaitPod(ctx context.Context, ns string, sel k8s.Selector) (string, error) {
	if f.WaitFn != nil {
		return f.WaitFn(ctx, ns, sel)
	}
	return "pod-1", nil
}

func (f *fakePodExecutor) ExecPod(ctx context.Context, ns, pod string, opts k8s.ExecOpts) error {
	f.mu.Lock()
	f.ExecCalls = append(f.ExecCalls, opts)
	f.mu.Unlock()
	if f.ExecFn != nil {
		return f.ExecFn(ctx, ns, pod, opts)
	}
	return nil
}

func TestCollectFiles_RespectsExclude(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.ts"), []byte("ok"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "debug.log"), []byte("skip"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "node_modules", "pkg", "x.js"), []byte("skip"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "important.log"), []byte("keep"), 0o644))

	files, err := collectFiles(root, []string{"/node_modules", "*.log", "!important.log"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"important.log", "src/main.ts"}, files)
}

func TestWriteTarFromFiles_WritesExpectedEntries(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "a.txt"), []byte("A"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "b.txt"), []byte("BB"), 0o644))

	var buf bytes.Buffer
	size, err := writeTarFromFiles(&buf, root, []string{"src/a.txt", "src/b.txt"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), size)

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	names := make([]string, 0, 2)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, h.Name)
	}
	assert.ElementsMatch(t, []string{"src/a.txt", "src/b.txt"}, names)
}

func TestSyncerIncrementalSync_TarsChangedAndDeletesRemoved(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "app.js"), []byte("console.log(1)"), 0o644))

	fake := &fakePodExecutor{}

	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app"})
	s.podName = "pod-1"

	require.NoError(t, s.incrementalSync(context.Background(), []FileChange{
		{Path: "src/app.js", Type: ChangeModify},
		{Path: "src/old.js", Type: ChangeDelete},
	}))

	require.Len(t, fake.ExecCalls, 2, "one tar stream + one rm")
	tarCall := fake.ExecCalls[0]
	rmCall := fake.ExecCalls[1]
	assert.Equal(t, []string{"tar", "xf", "-", "-C", "/app"}, tarCall.Command)
	require.NotNil(t, tarCall.Stdin)
	body, err := io.ReadAll(tarCall.Stdin)
	require.NoError(t, err)
	assert.NotEmpty(t, body)
	assert.Equal(t, []string{"rm", "-rf", "/app/src/old.js"}, rmCall.Command)
}

func TestSyncerRun_RequiresExec(t *testing.T) {
	s := NewSyncer(nil, "demo", nil, SyncRule{From: ".", To: "/app"})
	err := s.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "k8s client is required")
}

func TestSyncerRunWithReady_StillSignalsReadyOnValidationError(t *testing.T) {
	s := NewSyncer(nil, "demo", nil, SyncRule{From: ".", To: "/app"})
	var readyErr error
	readyCalled := false
	err := s.RunWithReady(context.Background(), func(err error) { readyCalled, readyErr = true, err })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "k8s client is required")
	assert.True(t, readyCalled, "ready must be called even on validation error")
	assert.Equal(t, err, readyErr, "ready must report the failure")
}

func TestSyncerRunWithReady_PropagatesWaitError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	fake := &fakePodExecutor{
		WaitFn: func(ctx context.Context, ns string, sel k8s.Selector) (string, error) {
			return "", errors.New("pod missing")
		},
	}
	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app", Polling: true})
	err := s.RunWithReady(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pod missing")
}

func TestSyncerRun_CallsInitialAndExitsOnCancel(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	fake := &fakePodExecutor{}
	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{
		From:    root,
		To:      "/app",
		Polling: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("syncer run did not exit after context cancel")
	}
}

func TestSyncerRunWithReady_SignalsAfterInitialSync(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	fake := &fakePodExecutor{}
	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{
		From:    root,
		To:      "/app",
		Polling: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	readyCh := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.RunWithReady(ctx, func(err error) { readyCh <- err })
	}()

	select {
	case err := <-readyCh:
		require.NoError(t, err, "a successful initial sync reports nil")
	case <-time.After(2 * time.Second):
		t.Fatal("ready callback was not called after initial sync")
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("syncer run did not exit after context cancel")
	}
}

func TestSyncerRunWithReady_NilReadyDoesNotPanic(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	fake := &fakePodExecutor{}
	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{
		From:    root,
		To:      "/app",
		Polling: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunWithReady(ctx, nil) }()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("syncer run did not exit after context cancel")
	}
}

// Compile-time check fakePodExecutor satisfies the port.
var _ k8s.PodExecutor = (*fakePodExecutor)(nil)

func TestSyncer_ExecsTargetContainer(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	fake := &fakePodExecutor{}
	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app", Container: "app"})
	s.podName = "pod-1"

	require.NoError(t, s.initialSync(context.Background()))
	require.NoError(t, s.incrementalSync(context.Background(), []FileChange{
		{Path: "a.txt", Type: ChangeModify},
		{Path: "gone.txt", Type: ChangeDelete},
	}))

	require.NotEmpty(t, fake.ExecCalls)
	for _, call := range fake.ExecCalls {
		assert.Equal(t, "app", call.Container, "exec %v must target the configured container", call.Command)
	}
}

func tarEntryNames(t *testing.T, r io.Reader) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		require.NoError(t, err)
		names = append(names, h.Name)
	}
}

// A file saved while the initial sync is uploading used to be lost: the tar
// snapshot did not contain it yet and the watcher only started afterwards.
func TestSyncerRun_ChangeDuringInitialSyncIsSynced(t *testing.T) {
	for _, polling := range []bool{false, true} {
		t.Run(map[bool]string{false: "fsnotify", true: "polling"}[polling], func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644))

			initialInFlight := make(chan struct{})
			releaseInitial := make(chan struct{})
			var mu sync.Mutex
			var tars [][]string
			fake := &fakePodExecutor{ExecFn: func(_ context.Context, _, _ string, opts k8s.ExecOpts) error {
				if opts.Command[0] != "tar" {
					return nil
				}
				names := tarEntryNames(t, opts.Stdin)
				mu.Lock()
				tars = append(tars, names)
				first := len(tars) == 1
				mu.Unlock()
				if first {
					close(initialInFlight)
					<-releaseInitial
				}
				return nil
			}}
			s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app", Polling: polling})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.Run(ctx) }()

			<-initialInFlight
			require.NoError(t, os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0o644))
			close(releaseInitial)

			require.Eventually(t, func() bool {
				mu.Lock()
				defer mu.Unlock()
				for _, names := range tars[1:] {
					for _, n := range names {
						if n == "b.txt" {
							return true
						}
					}
				}
				return false
			}, 3*time.Second, 20*time.Millisecond, "b.txt changed during the initial sync was never synced")

			cancel()
			require.NoError(t, <-done)
		})
	}
}

// Deleting a directory with many files produces one event per file; each
// used to cost its own exec round trip, stalling sync for minutes.
func TestSyncerIncrementalSync_BatchesDeletes(t *testing.T) {
	fake := &fakePodExecutor{}
	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{From: t.TempDir(), To: "/app"})
	s.podName = "pod-1"

	var changes []FileChange
	want := map[string]bool{}
	for i := 0; i < 1000; i++ {
		rel := fmt.Sprintf("dist/chunk-%04d.js", i)
		changes = append(changes, FileChange{Path: rel, Type: ChangeDelete})
		want["/app/"+rel] = true
	}
	require.NoError(t, s.incrementalSync(context.Background(), changes))

	assert.LessOrEqual(t, len(fake.ExecCalls), 3, "deletes must be batched into few rm calls")
	got := map[string]bool{}
	for _, call := range fake.ExecCalls {
		require.Equal(t, []string{"rm", "-rf"}, call.Command[:2])
		for _, p := range call.Command[2:] {
			got[p] = true
		}
	}
	assert.Equal(t, want, got)
}

// Sync commands discarded the container's stderr, so a failing tar only
// reported "command terminated with exit code 2" without the reason.
func TestSyncerInitialSync_ErrorIncludesRemoteStderr(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	fake := &fakePodExecutor{ExecFn: func(_ context.Context, _, _ string, opts k8s.ExecOpts) error {
		if opts.Command[0] != "tar" {
			return nil
		}
		_, _ = io.WriteString(opts.Stderr, "tar: a.txt: Cannot open: Permission denied\n")
		return errors.New("command terminated with exit code 2")
	}}
	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app"})
	s.podName = "pod-1"

	err := s.initialSync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit code 2")
	assert.Contains(t, err.Error(), "Permission denied")
}

func TestSyncerInitialSync_MissingToolHint(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	fake := &fakePodExecutor{ExecFn: func(_ context.Context, _, _ string, opts k8s.ExecOpts) error {
		return errors.New(`OCI runtime exec failed: exec failed: unable to start container process: exec: "mkdir": executable file not found in $PATH: unknown`)
	}}
	s := NewSyncer(fake, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app", Container: "app"})
	s.podName = "pod-1"

	err := s.initialSync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `runs tar, mkdir and rm in container "app"`)
}

// podSwitchingExecutor serves pod-1 until switchPod() is called and pod-2
// afterwards, recording which pod every tar went to.
type podSwitchingExecutor struct {
	mu       sync.Mutex
	current  string
	tarsTo   []string
	failPods map[string]bool
}

func (p *podSwitchingExecutor) WaitPod(ctx context.Context, _ string, _ k8s.Selector) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current, nil
}

func (p *podSwitchingExecutor) ExecPod(_ context.Context, _, pod string, opts k8s.ExecOpts) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failPods[pod] {
		return fmt.Errorf("pods %q not found", pod)
	}
	if opts.Command[0] == "tar" {
		p.tarsTo = append(p.tarsTo, pod)
	}
	return nil
}

func (p *podSwitchingExecutor) switchPod(failOld bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if failOld {
		p.failPods = map[string]bool{p.current: true}
	}
	p.current = "pod-2"
}

func (p *podSwitchingExecutor) tarredTo(pod string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.tarsTo {
		if t == pod {
			return true
		}
	}
	return false
}

func setPodCheckInterval(t *testing.T, d time.Duration) {
	t.Helper()
	orig := podCheckInterval
	podCheckInterval = d
	t.Cleanup(func() { podCheckInterval = orig })
}

// When the pod is replaced (rollout, eviction, the rollout triggered by
// ct dev's own patch), the new pod starts from the image: it must get a full
// sync even if nothing changes locally.
func TestSyncerRun_ResyncsWhenPodIsReplaced(t *testing.T) {
	setPodCheckInterval(t, 20*time.Millisecond)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	exec := &podSwitchingExecutor{current: "pod-1"}
	s := NewSyncer(exec, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app", Polling: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	require.Eventually(t, func() bool { return exec.tarredTo("pod-1") }, 2*time.Second, 10*time.Millisecond)
	exec.switchPod(true)
	require.Eventually(t, func() bool { return exec.tarredTo("pod-2") }, 2*time.Second, 10*time.Millisecond,
		"the replacement pod never received the synced files")

	cancel()
	require.NoError(t, <-done)
}

// An incremental sync that fails because the pod is gone must move to the
// new pod right away instead of failing against the old one forever.
func TestSyncerRun_FailedIncrementalSyncMovesToNewPod(t *testing.T) {
	setPodCheckInterval(t, time.Hour)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))

	exec := &podSwitchingExecutor{current: "pod-1"}
	s := NewSyncer(exec, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app", Polling: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	require.Eventually(t, func() bool { return exec.tarredTo("pod-1") }, 2*time.Second, 10*time.Millisecond)
	exec.switchPod(true)
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.txt"), []byte("y"), 0o644))
	require.Eventually(t, func() bool { return exec.tarredTo("pod-2") }, 3*time.Second, 10*time.Millisecond,
		"sync kept targeting the deleted pod")

	cancel()
	require.NoError(t, <-done)
}
