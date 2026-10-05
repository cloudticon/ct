// Package k8stest provides an in-memory implementation of k8s.Cluster for
// tests. The Fake exposes its state as plain public fields so test setup is
// readable and asserting on recorded calls is trivial.
package k8stest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudticon/ct/pkg/k8s"
)

// FakePod is the in-memory pod fixture. Healthy=true means WaitPod returns
// immediately; Healthy=false makes WaitPod block until either Healthy flips
// or ctx is cancelled.
type FakePod struct {
	Name      string
	Namespace string
	Labels    k8s.Selector
	Healthy   bool

	// LogContent, when non-empty, is written to streamLogs' writer once and
	// the call then blocks on ctx until cancellation.
	LogContent string
}

// ApplyCall records one ApplyRelease invocation.
type ApplyCall struct {
	Namespace string
	Release   string
	Resources []k8s.Resource
}

// DeleteCall records one DeleteRelease invocation.
type DeleteCall struct {
	Namespace string
	Release   string
}

// ExecCall records one Exec / ExecPod invocation. Pod is set only for ExecPod
// or after Exec resolved a pod via WaitPod.
type ExecCall struct {
	Namespace string
	Selector  k8s.Selector
	Pod       string
	Opts      k8s.ExecOpts
}

// Fake is an in-memory Cluster. Construct with NewFake; populate Pods or use
// hooks to drive behavior in tests. Methods are safe for concurrent calls.
type Fake struct {
	DefaultNamespace string

	// Inventory keyed by namespace -> release -> refs. Apply / Delete maintain
	// this; tests may pre-seed it.
	Releases map[string]map[string][]k8s.ResourceRef

	// Pods keyed by namespace -> pod-name -> *FakePod.
	Pods map[string]map[string]*FakePod

	// Namespaces tracked by ensureNamespace.
	Namespaces map[string]bool

	// Hooks override the default (no-op success) behavior. Each hook is
	// invoked before any Fake state change so tests can inject errors or
	// custom IO.
	ApplyHook       func(ns, release string, resources []k8s.Resource) error
	DeleteHook      func(ns, release string) (deleted int, err error)
	ExecHook        func(ns, pod string, opts k8s.ExecOpts) error
	PortForwardHook func(ns string, sel k8s.Selector, ports []k8s.PortRule) error
	StreamLogsHook  func(ns, target string, sel k8s.Selector, w io.Writer) error
	WatchPodHook    func(ns, pod string) error

	// Recorded calls. Append-only; tests assert on length / contents.
	ApplyCalls  []ApplyCall
	DeleteCalls []DeleteCall
	ExecCalls   []ExecCall

	// PollInterval controls how often WaitPod re-checks pod state. Zero means
	// 10ms (fast tests). Override only when a test wants to stretch timing.
	PollInterval time.Duration

	mu        sync.Mutex
	activeOps int32 // goroutines currently inside long-lived ops (port-fwd, logs, exec)
}

// NewFake returns a zero-value-ready Fake with maps allocated.
func NewFake() *Fake {
	return &Fake{
		Releases:   map[string]map[string][]k8s.ResourceRef{},
		Pods:       map[string]map[string]*FakePod{},
		Namespaces: map[string]bool{},
	}
}

// ActiveOps returns the count of goroutines currently blocked inside a
// long-lived Cluster method (Exec, ExecPod, portForward, streamLogs, WatchPod).
// Useful for cancel-cascade assertions: after cancel, this should drop to 0.
func (f *Fake) ActiveOps() int { return int(atomic.LoadInt32(&f.activeOps)) }

// HasActiveExec reports whether at least one Exec/ExecPod call is currently
// blocked in the fake. Pair with require.Eventually to synchronize tests.
func (f *Fake) HasActiveExec() bool { return f.ActiveOps() > 0 }

// AddPod is a convenience for seeding a pod. Returns the same pod for chaining.
//
// FakePod fields are read under the Fake's mutex; mutating them after AddPod
// races with WaitPod/streamLogs/findHealthyPod. Use SetPodHealthy to flip
// readiness from a test goroutine.
func (f *Fake) AddPod(p *FakePod) *FakePod {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.Namespace == "" {
		p.Namespace = f.DefaultNamespace
	}
	pods := f.Pods[p.Namespace]
	if pods == nil {
		pods = map[string]*FakePod{}
		f.Pods[p.Namespace] = pods
	}
	pods[p.Name] = p
	return p
}

// SetPodHealthy flips the Healthy flag on a seeded pod under the fake's mutex.
// Safe to call from any goroutine. Returns false if the pod isn't found.
func (f *Fake) SetPodHealthy(ns, name string, healthy bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	pods := f.Pods[f.resolveNS(ns)]
	if pods == nil {
		return false
	}
	p, ok := pods[name]
	if !ok {
		return false
	}
	p.Healthy = healthy
	return true
}

// --- Cluster implementation ---

func (f *Fake) ApplyRelease(ctx context.Context, ns, release string, resources []k8s.Resource) error {
	ns = f.resolveNS(ns)
	if f.ApplyHook != nil {
		if err := f.ApplyHook(ns, release, resources); err != nil {
			return err
		}
	}
	refs := make([]k8s.ResourceRef, 0, len(resources))
	for i, res := range resources {
		ref, err := refOf(res, i)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Releases[ns] == nil {
		f.Releases[ns] = map[string][]k8s.ResourceRef{}
	}
	f.Releases[ns][release] = refs
	f.ApplyCalls = append(f.ApplyCalls, ApplyCall{Namespace: ns, Release: release, Resources: resources})
	return nil
}

func (f *Fake) DeleteRelease(ctx context.Context, ns, release string) (int, error) {
	ns = f.resolveNS(ns)
	if f.DeleteHook != nil {
		return f.DeleteHook(ns, release)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	refs := f.Releases[ns][release]
	delete(f.Releases[ns], release)
	f.DeleteCalls = append(f.DeleteCalls, DeleteCall{Namespace: ns, Release: release})
	return len(refs), nil
}

func (f *Fake) ListReleases(ctx context.Context, ns string, allNamespaces bool) ([]k8s.ReleaseInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []k8s.ReleaseInfo{}
	for releasesNS, byRelease := range f.Releases {
		if !allNamespaces && releasesNS != f.resolveNS(ns) {
			continue
		}
		for name, refs := range byRelease {
			out = append(out, k8s.ReleaseInfo{Name: name, Namespace: releasesNS, Resources: len(refs)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace == out[j].Namespace {
			return out[i].Name < out[j].Name
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out, nil
}

func (f *Fake) EnsureNamespace(ctx context.Context, ns string) error {
	if ns == "" {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Namespaces[ns] = true
	return nil
}

func (f *Fake) WaitPod(ctx context.Context, ns string, sel k8s.Selector) (string, error) {
	ns = f.resolveNS(ns)
	tick := f.PollInterval
	if tick <= 0 {
		tick = 10 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if name := f.findHealthyPod(ns, sel); name != "" {
			return name, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.C:
		}
	}
}

func (f *Fake) WatchPod(ctx context.Context, ns, pod string) error {
	atomic.AddInt32(&f.activeOps, 1)
	defer atomic.AddInt32(&f.activeOps, -1)
	if f.WatchPodHook != nil {
		return f.WatchPodHook(f.resolveNS(ns), pod)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *Fake) Exec(ctx context.Context, ns string, sel k8s.Selector, opts k8s.ExecOpts) error {
	ns = f.resolveNS(ns)
	pod, err := f.WaitPod(ctx, ns, sel)
	if err != nil {
		return err
	}
	return f.execPod(ctx, ns, pod, sel, opts)
}

func (f *Fake) ExecPod(ctx context.Context, ns, pod string, opts k8s.ExecOpts) error {
	return f.execPod(ctx, f.resolveNS(ns), pod, nil, opts)
}

func (f *Fake) execPod(ctx context.Context, ns, pod string, sel k8s.Selector, opts k8s.ExecOpts) error {
	atomic.AddInt32(&f.activeOps, 1)
	defer atomic.AddInt32(&f.activeOps, -1)

	f.mu.Lock()
	f.ExecCalls = append(f.ExecCalls, ExecCall{Namespace: ns, Selector: sel, Pod: pod, Opts: opts})
	f.mu.Unlock()

	if f.ExecHook != nil {
		return f.ExecHook(ns, pod, opts)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *Fake) PortForward(ctx context.Context, ns string, sel k8s.Selector, ports []k8s.PortRule) error {
	atomic.AddInt32(&f.activeOps, 1)
	defer atomic.AddInt32(&f.activeOps, -1)
	if f.PortForwardHook != nil {
		return f.PortForwardHook(f.resolveNS(ns), sel, ports)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *Fake) StreamLogs(ctx context.Context, ns, target string, sel k8s.Selector, w io.Writer) error {
	atomic.AddInt32(&f.activeOps, 1)
	defer atomic.AddInt32(&f.activeOps, -1)

	if f.StreamLogsHook != nil {
		return f.StreamLogsHook(f.resolveNS(ns), target, sel, w)
	}
	if name := f.findHealthyPod(f.resolveNS(ns), sel); name != "" {
		f.mu.Lock()
		pod := f.Pods[f.resolveNS(ns)][name]
		f.mu.Unlock()
		if pod != nil && pod.LogContent != "" {
			_, _ = io.WriteString(w, pod.LogContent)
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

// --- helpers ---

func (f *Fake) resolveNS(ns string) string {
	if ns != "" {
		return ns
	}
	return f.DefaultNamespace
}

func (f *Fake) findHealthyPod(ns string, sel k8s.Selector) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, p := range f.Pods[ns] {
		if !p.Healthy {
			continue
		}
		if matchLabels(p.Labels, sel) {
			return name
		}
	}
	return ""
}

func matchLabels(labels, sel k8s.Selector) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func refOf(res k8s.Resource, idx int) (k8s.ResourceRef, error) {
	apiVersion, _ := res["apiVersion"].(string)
	kind, _ := res["kind"].(string)
	if apiVersion == "" || kind == "" {
		return k8s.ResourceRef{}, fmt.Errorf("resource %d missing apiVersion or kind", idx)
	}
	meta, _ := res["metadata"].(map[string]interface{})
	if meta == nil {
		return k8s.ResourceRef{}, fmt.Errorf("resource %d missing metadata", idx)
	}
	name, _ := meta["name"].(string)
	if name == "" {
		return k8s.ResourceRef{}, errors.New("resource missing metadata.name")
	}
	ns, _ := meta["namespace"].(string)
	return k8s.ResourceRef{APIVersion: apiVersion, Kind: kind, Name: name, Namespace: ns}, nil
}

// Compile-time guarantee Fake satisfies both ports.
var (
	_ k8s.Cluster     = (*Fake)(nil)
	_ k8s.PodExecutor = (*Fake)(nil)
)
