package k8s

import (
	"context"
	"io"
	"log"

	"k8s.io/client-go/tools/remotecommand"
)

// Selector matches workload pods by label equality (k=v AND k2=v2).
type Selector = map[string]string

// ExecOpts configures a single Cluster.Exec / Cluster.ExecPod invocation.
//
// When TTY is true and Stdin is os.Stdin connected to a terminal, the live
// adapter switches the local terminal to raw mode and forwards SIGWINCH-driven
// resize events. Pass TerminalSize to override the resize behavior; leave nil
// to let the adapter detect from Stdin.
type ExecOpts struct {
	Container string
	Command   []string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	TTY               bool
	TerminalSizeQueue remotecommand.TerminalSizeQueue
}

// LiveOpts configures NewLiveCluster. Zero-value fields fall back to sensible
// defaults: empty KubeContext = current-context, empty DefaultNamespace = the
// kubeconfig context's namespace (or "default"), nil Logger = log.Default().
type LiveOpts struct {
	KubeContext      string
	DefaultNamespace string
	Logger           *log.Logger
}

// Cluster is the single port between ct and Kubernetes.
//
// Live impl talks to the apiserver via client-go; the in-memory Fake adapter
// in pkg/k8s/k8stest serves tests. All blocking methods honor ctx and return
// cleanly on cancel.
//
// The ns parameter on every method takes precedence over the adapter's default
// namespace. Pass "" to use the adapter default (set in LiveOpts.DefaultNamespace
// for live, or Fake.DefaultNamespace for fake).
type Cluster interface {
	// Release lifecycle.
	ApplyRelease(ctx context.Context, ns, release string, resources []Resource) error
	// PlanRelease reports what ApplyRelease would create, change and prune,
	// using the API server's dry run; it writes nothing, inventory included.
	PlanRelease(ctx context.Context, ns, release string, resources []Resource, opts PlanOpts) (*Plan, error)
	DeleteRelease(ctx context.Context, ns, release string) (deleted int, err error)
	// PlanDelete returns the resources DeleteRelease would remove, in deletion
	// order, and the namespace the inventory was looked up in. It changes
	// nothing; an unknown release yields no resources.
	PlanDelete(ctx context.Context, ns, release string) (resources []ResourceRef, namespace string, err error)
	ListReleases(ctx context.Context, ns string, allNamespaces bool) ([]ReleaseInfo, error)
	EnsureNamespace(ctx context.Context, ns string) error

	// Live workload ops (used by ct dev).
	WaitPod(ctx context.Context, ns string, sel Selector) (string, error)
	WatchPod(ctx context.Context, ns, pod string) error
	Exec(ctx context.Context, ns string, sel Selector, opts ExecOpts) error
	ExecPod(ctx context.Context, ns, pod string, opts ExecOpts) error
	PortForward(ctx context.Context, ns string, sel Selector, ports []PortRule) error
	StreamLogs(ctx context.Context, ns, target string, sel Selector, w io.Writer) error
}

// PodExecutor is the narrow port pkg/sync depends on. Cluster satisfies it
// structurally — pkg/sync need not import full Cluster surface to be tested.
type PodExecutor interface {
	WaitPod(ctx context.Context, ns string, sel Selector) (string, error)
	ExecPod(ctx context.Context, ns, pod string, opts ExecOpts) error
}
