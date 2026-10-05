package k8s

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/term"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/remotecommand"
	"net/http"
	"net/url"
	"os"
)

var (
	waitForPodForExecFn                          = waitForPod
	execStreamRunnerFn                           = execStream
	buildExecURLFn                               = buildExecURL
	newExecExecutorForURL                        = remotecommand.NewSPDYExecutor
	parameterCodec        runtime.ParameterCodec = func() runtime.ParameterCodec {
		s := runtime.NewScheme()
		_ = corev1.AddToScheme(s)
		return runtime.NewParameterCodec(s)
	}()

	makeRawFn     = func(fd int) (*term.State, error) { return term.MakeRaw(fd) }
	restoreTermFn = func(fd int, oldState *term.State) error { return term.Restore(fd, oldState) }
	getTermSizeFn = func(fd int) (int, int, error) { return term.GetSize(fd) }
)

// execStreamOpts configures Kubernetes exec stream behavior.
type execStreamOpts struct {
	Container string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	TTY               bool
	TerminalSizeQueue remotecommand.TerminalSizeQueue
}

// termSizeQueue delivers terminal resize events to the remote shell.
type termSizeQueue struct {
	resizeCh chan remotecommand.TerminalSize
	sigCh    chan os.Signal
	done     chan struct{}
}

func newTermSizeQueue(fd int) *termSizeQueue {
	q := &termSizeQueue{
		resizeCh: make(chan remotecommand.TerminalSize, 1),
		sigCh:    make(chan os.Signal, 1),
		done:     make(chan struct{}),
	}

	if w, h, err := getTermSizeFn(fd); err == nil {
		q.resizeCh <- remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}
	}

	notifyResize(q.sigCh)
	go q.monitor(fd)
	return q
}

func (q *termSizeQueue) monitor(fd int) {
	defer stopResize(q.sigCh)
	for {
		select {
		case <-q.sigCh:
			if w, h, err := getTermSizeFn(fd); err == nil {
				q.offer(remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)})
			}
		case <-q.done:
			return
		}
	}
}

// offer queues size, replacing a size that has not been consumed yet: only
// the latest dimensions matter. (Dropping the new size instead lost the
// final size of a quick series of resizes.) Only the monitor goroutine sends.
func (q *termSizeQueue) offer(size remotecommand.TerminalSize) {
	for {
		select {
		case q.resizeCh <- size:
			return
		default:
		}
		select {
		case <-q.resizeCh: // discard the stale pending size
		default:
		}
	}
}

func (q *termSizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case size, ok := <-q.resizeCh:
		if !ok {
			return nil
		}
		return &size
	case <-q.done:
		return nil
	}
}

func (q *termSizeQueue) stop() {
	select {
	case <-q.done:
	default:
		close(q.done)
	}
}

// execInteractive runs a shell command in the first running pod matching
// selector and attaches local stdio in raw TTY mode. Used internally by
// liveCluster.Exec when callers want full terminal semantics.
func execInteractive(ctx context.Context, c *client, selector map[string]string, command string) error {
	if c == nil {
		return errors.New("client is required")
	}

	pod, err := waitForPodForExecFn(ctx, c, selector)
	if err != nil {
		return err
	}

	fd := int(os.Stdin.Fd())
	oldState, err := makeRawFn(fd)
	if err != nil {
		return fmt.Errorf("setting terminal raw mode: %w", err)
	}
	defer restoreTermFn(fd, oldState)

	sizeQueue := newTermSizeQueue(fd)
	defer sizeQueue.stop()

	return execStreamRunnerFn(ctx, c, pod, []string{"/bin/sh", "-c", command}, execStreamOpts{
		Stdin:             os.Stdin,
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
		TTY:               true,
		TerminalSizeQueue: sizeQueue,
	})
}

// execSimple runs a command without TTY and discards output.
func execSimple(ctx context.Context, c *client, pod string, cmd []string) error {
	return execStreamRunnerFn(ctx, c, pod, cmd, execStreamOpts{
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
}

// execStream executes a command in a pod and streams I/O according to options.
func execStream(ctx context.Context, c *client, pod string, cmd []string, opts execStreamOpts) error {
	if c == nil {
		return errors.New("client is required")
	}
	if c.Config == nil {
		return errors.New("rest config is required")
	}
	if pod == "" {
		return errors.New("pod name is required")
	}
	if len(cmd) == 0 {
		return errors.New("command is required")
	}

	reqURL, err := buildExecURLFn(c, pod, cmd, opts)
	if err != nil {
		return err
	}

	executor, err := newExecExecutorForURL(c.Config, http.MethodPost, reqURL)
	if err != nil {
		return fmt.Errorf("creating exec executor: %w", err)
	}

	streamOpts := remotecommand.StreamOptions{
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
		Tty:    opts.TTY,
	}
	if opts.TerminalSizeQueue != nil {
		streamOpts.TerminalSizeQueue = opts.TerminalSizeQueue
	}

	if err := executor.StreamWithContext(ctx, streamOpts); err != nil {
		return fmt.Errorf("executing command in pod %s: %w", pod, err)
	}
	return nil
}

func buildExecURL(c *client, pod string, cmd []string, opts execStreamOpts) (*url.URL, error) {
	if c.CoreV1 == nil {
		return nil, errors.New("kubernetes core/v1 client is required")
	}

	req := c.CoreV1.RESTClient().
		Post().
		Resource("pods").
		Namespace(c.Namespace).
		Name(pod).
		SubResource("exec")

	req.VersionedParams(&corev1.PodExecOptions{
		Container: opts.Container,
		Command:   cmd,
		Stdin:     opts.Stdin != nil,
		Stdout:    opts.Stdout != nil,
		Stderr:    opts.Stderr != nil,
		TTY:       opts.TTY,
	}, parameterCodec)

	return req.URL(), nil
}
