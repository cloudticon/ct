package k8s

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type fakeStreamExecutor struct {
	called bool
	opts   remotecommand.StreamOptions
	err    error
}

func (f *fakeStreamExecutor) Stream(opts remotecommand.StreamOptions) error {
	return f.StreamWithContext(context.Background(), opts)
}

func (f *fakeStreamExecutor) StreamWithContext(_ context.Context, opts remotecommand.StreamOptions) error {
	f.called = true
	f.opts = opts
	return f.err
}

func stubTerminalFns(t *testing.T) {
	t.Helper()
	origMakeRaw := makeRawFn
	origRestore := restoreTermFn
	origGetSize := getTermSizeFn
	t.Cleanup(func() {
		makeRawFn = origMakeRaw
		restoreTermFn = origRestore
		getTermSizeFn = origGetSize
	})
	makeRawFn = func(int) (*term.State, error) { return nil, nil }
	restoreTermFn = func(int, *term.State) error { return nil }
	getTermSizeFn = func(int) (int, int, error) { return 80, 24, nil }
}

func TestExec_ResolvesPodAndRunsShellCommand(t *testing.T) {
	origWait := waitForPodForExecFn
	origRunner := execStreamRunnerFn
	t.Cleanup(func() {
		waitForPodForExecFn = origWait
		execStreamRunnerFn = origRunner
	})
	stubTerminalFns(t)

	waitForPodForExecFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "pod-1", nil
	}

	called := false
	var gotPod string
	var gotCmd []string
	var gotOpts execStreamOpts
	execStreamRunnerFn = func(_ context.Context, _ *client, pod string, cmd []string, opts execStreamOpts) error {
		called = true
		gotPod = pod
		gotCmd = append([]string(nil), cmd...)
		gotOpts = opts
		return nil
	}

	err := execInteractive(context.Background(), &client{}, map[string]string{"app": "web"}, "npm run dev")
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "pod-1", gotPod)
	assert.Equal(t, []string{"/bin/sh", "-c", "npm run dev"}, gotCmd)
	assert.True(t, gotOpts.TTY)
	assert.NotNil(t, gotOpts.TerminalSizeQueue, "should pass terminal size queue")
}

func TestExec_ReturnsWaitForPodError(t *testing.T) {
	origWait := waitForPodForExecFn
	t.Cleanup(func() {
		waitForPodForExecFn = origWait
	})

	waitForPodForExecFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "", errors.New("cannot find pod")
	}

	err := execInteractive(context.Background(), &client{}, map[string]string{"app": "web"}, "bash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot find pod")
}

func TestExecStream_BuildsExecutorAndStreams(t *testing.T) {
	origBuildURL := buildExecURLFn
	origNewExecutor := newExecExecutorForURL
	t.Cleanup(func() {
		buildExecURLFn = origBuildURL
		newExecExecutorForURL = origNewExecutor
	})

	buildExecURLFn = func(_ *client, _ string, _ []string, _ execStreamOpts) (*url.URL, error) {
		return url.Parse("https://example.invalid/api/v1/namespaces/dev/pods/pod-1/exec")
	}

	fakeExec := &fakeStreamExecutor{}
	newExecExecutorForURL = func(_ *rest.Config, method string, reqURL *url.URL) (remotecommand.Executor, error) {
		assert.Equal(t, http.MethodPost, method)
		assert.Equal(t, "https://example.invalid/api/v1/namespaces/dev/pods/pod-1/exec", reqURL.String())
		return fakeExec, nil
	}

	in := bytes.NewBufferString("input")
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}

	err := execStream(context.Background(), &client{Config: &rest.Config{}}, "pod-1", []string{"echo", "ok"}, execStreamOpts{
		Stdin:  in,
		Stdout: out,
		Stderr: errOut,
		TTY:    true,
	})
	require.NoError(t, err)
	assert.True(t, fakeExec.called)
	assert.Equal(t, in, fakeExec.opts.Stdin)
	assert.Equal(t, out, fakeExec.opts.Stdout)
	assert.Equal(t, errOut, fakeExec.opts.Stderr)
	assert.True(t, fakeExec.opts.Tty)
}

func TestExecStream_ValidatesInput(t *testing.T) {
	err := execStream(context.Background(), nil, "pod-1", []string{"echo"}, execStreamOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client is required")

	err = execStream(context.Background(), &client{}, "pod-1", []string{"echo"}, execStreamOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rest config is required")

	err = execStream(context.Background(), &client{Config: &rest.Config{}}, "", []string{"echo"}, execStreamOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pod name is required")

	err = execStream(context.Background(), &client{Config: &rest.Config{}}, "pod-1", nil, execStreamOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}

func TestExecSimple_UsesNonTTYDefaults(t *testing.T) {
	origRunner := execStreamRunnerFn
	t.Cleanup(func() {
		execStreamRunnerFn = origRunner
	})

	got := execStreamOpts{}
	execStreamRunnerFn = func(_ context.Context, _ *client, _ string, _ []string, opts execStreamOpts) error {
		got = opts
		return nil
	}

	err := execSimple(context.Background(), &client{}, "pod-1", []string{"ls"})
	require.NoError(t, err)
	assert.False(t, got.TTY)
	assert.NotNil(t, got.Stdout)
	assert.NotNil(t, got.Stderr)
}

func TestExec_SetsRawModeAndRestores(t *testing.T) {
	origWait := waitForPodForExecFn
	origRunner := execStreamRunnerFn
	origMakeRaw := makeRawFn
	origRestore := restoreTermFn
	origGetSize := getTermSizeFn
	t.Cleanup(func() {
		waitForPodForExecFn = origWait
		execStreamRunnerFn = origRunner
		makeRawFn = origMakeRaw
		restoreTermFn = origRestore
		getTermSizeFn = origGetSize
	})

	waitForPodForExecFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "pod-1", nil
	}
	execStreamRunnerFn = func(_ context.Context, _ *client, _ string, _ []string, _ execStreamOpts) error {
		return nil
	}
	getTermSizeFn = func(int) (int, int, error) { return 120, 40, nil }

	rawCalled := false
	restoreCalled := false
	makeRawFn = func(int) (*term.State, error) {
		rawCalled = true
		return nil, nil
	}
	restoreTermFn = func(int, *term.State) error {
		restoreCalled = true
		return nil
	}

	err := execInteractive(context.Background(), &client{}, map[string]string{"app": "web"}, "bash")
	require.NoError(t, err)
	assert.True(t, rawCalled, "makeRaw should be called")
	assert.True(t, restoreCalled, "restore should be called after exec")
}

func TestExec_ReturnsRawModeError(t *testing.T) {
	origWait := waitForPodForExecFn
	origMakeRaw := makeRawFn
	origGetSize := getTermSizeFn
	t.Cleanup(func() {
		waitForPodForExecFn = origWait
		makeRawFn = origMakeRaw
		getTermSizeFn = origGetSize
	})

	waitForPodForExecFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "pod-1", nil
	}
	getTermSizeFn = func(int) (int, int, error) { return 80, 24, nil }
	makeRawFn = func(int) (*term.State, error) {
		return nil, errors.New("not a terminal")
	}

	err := execInteractive(context.Background(), &client{}, map[string]string{"app": "web"}, "bash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting terminal raw mode")
}

func TestTermSizeQueue_NextReturnsInitialSize(t *testing.T) {
	origGetSize := getTermSizeFn
	t.Cleanup(func() { getTermSizeFn = origGetSize })

	getTermSizeFn = func(int) (int, int, error) { return 132, 43, nil }

	q := newTermSizeQueue(0)
	defer q.stop()

	size := q.Next()
	require.NotNil(t, size)
	assert.Equal(t, uint16(132), size.Width)
	assert.Equal(t, uint16(43), size.Height)
}

func TestTermSizeQueue_StopMakesNextReturnNil(t *testing.T) {
	origGetSize := getTermSizeFn
	t.Cleanup(func() { getTermSizeFn = origGetSize })

	getTermSizeFn = func(int) (int, int, error) { return 80, 24, nil }

	q := newTermSizeQueue(0)
	_ = q.Next() // consume initial size

	q.stop()

	done := make(chan *remotecommand.TerminalSize, 1)
	go func() { done <- q.Next() }()

	select {
	case got := <-done:
		assert.Nil(t, got, "Next() should return nil after stop")
	case <-time.After(time.Second):
		t.Fatal("Next() did not return after stop()")
	}
}

func TestExecStream_PassesTerminalSizeQueue(t *testing.T) {
	origBuildURL := buildExecURLFn
	origNewExecutor := newExecExecutorForURL
	t.Cleanup(func() {
		buildExecURLFn = origBuildURL
		newExecExecutorForURL = origNewExecutor
	})

	buildExecURLFn = func(_ *client, _ string, _ []string, _ execStreamOpts) (*url.URL, error) {
		return url.Parse("https://example.invalid/exec")
	}

	fakeExec := &fakeStreamExecutor{}
	newExecExecutorForURL = func(_ *rest.Config, _ string, _ *url.URL) (remotecommand.Executor, error) {
		return fakeExec, nil
	}

	fakeSizeQueue := &staticSizeQueue{size: remotecommand.TerminalSize{Width: 100, Height: 50}}

	err := execStream(context.Background(), &client{Config: &rest.Config{}}, "pod-1", []string{"bash"}, execStreamOpts{
		Stdout:            &bytes.Buffer{},
		TTY:               true,
		TerminalSizeQueue: fakeSizeQueue,
	})
	require.NoError(t, err)
	assert.Equal(t, fakeSizeQueue, fakeExec.opts.TerminalSizeQueue)
}

type staticSizeQueue struct {
	size remotecommand.TerminalSize
}

func (q *staticSizeQueue) Next() *remotecommand.TerminalSize { return &q.size }

// Resizing a window sends bursts of SIGWINCH. When a size was still pending,
// the newer size was dropped and the stale one kept, so the remote terminal
// could end up with the wrong dimensions until the next resize.
func TestTermSizeQueue_KeepsLatestSize(t *testing.T) {
	origGetSize := getTermSizeFn
	t.Cleanup(func() { getTermSizeFn = origGetSize })

	var mu sync.Mutex
	current := [2]int{80, 24}
	calls := 0
	getTermSizeFn = func(int) (int, int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return current[0], current[1], nil
	}
	resize := func(w, h int) {
		mu.Lock()
		current = [2]int{w, h}
		mu.Unlock()
	}
	sizeCalls := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}

	q := newTermSizeQueue(0) // initial 80x24 is pending
	defer q.stop()

	resize(100, 30)
	q.sigCh <- os.Interrupt // the monitor ignores the value (SIGWINCH is not portable)
	require.Eventually(t, func() bool { return sizeCalls() == 2 }, time.Second, time.Millisecond)
	resize(120, 40)
	q.sigCh <- os.Interrupt
	require.Eventually(t, func() bool { return sizeCalls() == 3 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond) // let the monitor finish queueing

	size := q.Next()
	require.NotNil(t, size)
	assert.Equal(t, remotecommand.TerminalSize{Width: 120, Height: 40}, *size, "the latest size must win over a stale pending one")
}
