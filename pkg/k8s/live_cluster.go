package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"golang.org/x/term"
)

// NewLiveCluster builds a Cluster backed by client-go and the running
// kubeconfig. Empty fields in opts fall back to the same defaults newClient
// uses today (current-context, kubeconfig namespace or "default").
func NewLiveCluster(opts LiveOpts) (Cluster, error) {
	c, err := newClient(opts.KubeContext, opts.DefaultNamespace)
	if err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &liveCluster{client: c, logger: logger}, nil
}

// liveCluster is the production adapter. It delegates to the existing *client
// helpers while presenting the Cluster port to consumers.
type liveCluster struct {
	client *client
	logger *log.Logger
}

// scopedClient returns a *client whose Namespace field is ns. If ns is empty
// or matches the default, the receiver client is reused; otherwise a shallow
// copy is taken so concurrent calls with different ns don't race on the field.
// The shared gvrCache map is safe because client-go discovery is read-mostly
// and the live adapter is single-process — see PR3 follow-up if this changes.
func (lc *liveCluster) scopedClient(ns string) *client {
	if ns == "" || ns == lc.client.Namespace {
		return lc.client
	}
	cp := *lc.client
	cp.Namespace = ns
	return &cp
}

// Release lifecycle.

func (lc *liveCluster) ApplyRelease(ctx context.Context, ns, release string, resources []Resource) error {
	c := lc.scopedClient(ns)
	if ns == "" {
		ns = c.Namespace
	}
	return c.applyRelease(ctx, ns, release, resources)
}

func (lc *liveCluster) DeleteRelease(ctx context.Context, ns, release string) (int, error) {
	c := lc.scopedClient(ns)
	refs, err := loadInventory(ctx, c, ns, release)
	if err != nil {
		return 0, fmt.Errorf("loading inventory for release %q: %w", release, err)
	}
	if err := c.del(ctx, refs); err != nil {
		return 0, fmt.Errorf("deleting release resources: %w", err)
	}
	if err := deleteInventory(ctx, c, ns, release); err != nil {
		return 0, fmt.Errorf("deleting release inventory: %w", err)
	}
	return len(refs), nil
}

func (lc *liveCluster) ListReleases(ctx context.Context, ns string, allNs bool) ([]ReleaseInfo, error) {
	c := lc.scopedClient(ns)
	return listReleases(ctx, c, ns, allNs)
}

func (lc *liveCluster) EnsureNamespace(ctx context.Context, ns string) error {
	if ns == "" {
		return nil
	}
	return ensureNamespace(ctx, lc.client, ns)
}

// Live workload ops.

func (lc *liveCluster) WaitPod(ctx context.Context, ns string, sel Selector) (string, error) {
	return waitForPod(ctx, lc.scopedClient(ns), sel)
}

func (lc *liveCluster) WatchPod(ctx context.Context, ns, pod string) error {
	return watchPodHealth(ctx, lc.scopedClient(ns), pod)
}

func (lc *liveCluster) Exec(ctx context.Context, ns string, sel Selector, opts ExecOpts) error {
	c := lc.scopedClient(ns)
	pod, err := waitForPodForExecFn(ctx, c, sel)
	if err != nil {
		return err
	}
	return lc.execPodOn(ctx, c, pod, opts)
}

func (lc *liveCluster) ExecPod(ctx context.Context, ns, pod string, opts ExecOpts) error {
	return lc.execPodOn(ctx, lc.scopedClient(ns), pod, opts)
}

// execPodOn applies TTY raw-mode handling when needed and forwards to execStream.
// In PR4 the raw-mode plumbing moves to internal/tty; the live adapter will then
// only translate ExecOpts -> execStreamOpts and call execStream directly.
func (lc *liveCluster) execPodOn(ctx context.Context, c *client, pod string, opts ExecOpts) error {
	if c == nil {
		return errors.New("client is required")
	}
	if pod == "" {
		return errors.New("pod name is required")
	}
	if len(opts.Command) == 0 {
		return errors.New("command is required")
	}

	streamOpts := execStreamOpts{
		Container:         resolveContainer(ctx, c, pod, opts.Container),
		Stdin:             opts.Stdin,
		Stdout:            opts.Stdout,
		Stderr:            opts.Stderr,
		TTY:               opts.TTY,
		TerminalSizeQueue: opts.TerminalSizeQueue,
	}

	if opts.TTY && opts.Stdin == os.Stdin && term.IsTerminal(int(os.Stdin.Fd())) {
		fd := int(os.Stdin.Fd())
		oldState, err := makeRawFn(fd)
		if err != nil {
			return fmt.Errorf("setting terminal raw mode: %w", err)
		}
		defer restoreTermFn(fd, oldState)

		if streamOpts.TerminalSizeQueue == nil {
			q := newTermSizeQueue(fd)
			defer q.stop()
			streamOpts.TerminalSizeQueue = q
		}
	}

	return execStreamRunnerFn(ctx, c, pod, opts.Command, streamOpts)
}

func (lc *liveCluster) PortForward(ctx context.Context, ns string, sel Selector, ports []PortRule) error {
	return portForward(ctx, lc.scopedClient(ns), sel, ports)
}

func (lc *liveCluster) StreamLogs(ctx context.Context, ns, target string, sel Selector, w io.Writer) error {
	return streamLogs(ctx, lc.scopedClient(ns), target, sel, w)
}

// Compile-time guarantee that *liveCluster satisfies both ports.
var (
	_ Cluster     = (*liveCluster)(nil)
	_ PodExecutor = (*liveCluster)(nil)
)
