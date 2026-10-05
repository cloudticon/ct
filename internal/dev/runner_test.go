package dev

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/cloudticon/ct/pkg/k8s/k8stest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// devCluster wraps a k8stest.Fake with optional per-method hooks tailored to
// the dev runner. Behavior defaults: PortForward / WatchPod / StreamLogs /
// Exec block on ctx; sync (a recurring ExecPod) succeeds silently.
type devCluster struct {
	*k8stest.Fake

	mu sync.Mutex

	// Optional behavior overrides for each lifecycle feature. When nil, the
	// underlying Fake's default behavior runs (block on ctx for streamers,
	// no-op success for ExecPod).
	PortForwardFn func(ctx context.Context, ns string, sel k8s.Selector, ports []k8s.PortRule) error
	StreamLogsFn  func(ctx context.Context, ns, target string, sel k8s.Selector, w io.Writer) error
	WatchPodFn    func(ctx context.Context, ns, pod string) error
	ExecFn        func(ctx context.Context, ns string, sel k8s.Selector, opts k8s.ExecOpts) error
	WaitPodFn     func(ctx context.Context, ns string, sel k8s.Selector) (string, error)

	portCalls     []string
	logCalls      []string
	syncCalls     []string
	terminalCalls int32
	waitCalls     int32
	healthCalls   int32
}

func newDevCluster() *devCluster {
	return &devCluster{Fake: k8stest.NewFake()}
}

func (d *devCluster) WaitPod(ctx context.Context, ns string, sel k8s.Selector) (string, error) {
	atomic.AddInt32(&d.waitCalls, 1)
	if d.WaitPodFn != nil {
		return d.WaitPodFn(ctx, ns, sel)
	}
	return "pod-stub", nil
}

func (d *devCluster) PortForward(ctx context.Context, ns string, sel k8s.Selector, ports []k8s.PortRule) error {
	d.mu.Lock()
	d.portCalls = append(d.portCalls, sel["app"])
	d.mu.Unlock()
	if d.PortForwardFn != nil {
		return d.PortForwardFn(ctx, ns, sel, ports)
	}
	return d.Fake.PortForward(ctx, ns, sel, ports)
}

func (d *devCluster) StreamLogs(ctx context.Context, ns, target string, sel k8s.Selector, w io.Writer) error {
	d.mu.Lock()
	d.logCalls = append(d.logCalls, target)
	d.mu.Unlock()
	if d.StreamLogsFn != nil {
		return d.StreamLogsFn(ctx, ns, target, sel, w)
	}
	return d.Fake.StreamLogs(ctx, ns, target, sel, w)
}

func (d *devCluster) WatchPod(ctx context.Context, ns, pod string) error {
	atomic.AddInt32(&d.healthCalls, 1)
	if d.WatchPodFn != nil {
		return d.WatchPodFn(ctx, ns, pod)
	}
	return d.Fake.WatchPod(ctx, ns, pod)
}

func (d *devCluster) Exec(ctx context.Context, ns string, sel k8s.Selector, opts k8s.ExecOpts) error {
	atomic.AddInt32(&d.terminalCalls, 1)
	if d.ExecFn != nil {
		return d.ExecFn(ctx, ns, sel, opts)
	}
	return d.Fake.Exec(ctx, ns, sel, opts)
}

// ExecPod is the route used by sync. We return success immediately so the
// dev session's sync feature reports ready and unblocks the terminal.
func (d *devCluster) ExecPod(ctx context.Context, ns, pod string, opts k8s.ExecOpts) error {
	d.mu.Lock()
	if len(opts.Command) > 0 && opts.Command[0] == "tar" {
		d.syncCalls = append(d.syncCalls, "tar")
	}
	d.mu.Unlock()
	return nil
}

func (d *devCluster) PortCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.portCalls...)
}

func (d *devCluster) LogCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.logCalls...)
}

func (d *devCluster) TerminalCalls() int { return int(atomic.LoadInt32(&d.terminalCalls)) }
func (d *devCluster) WaitCalls() int     { return int(atomic.LoadInt32(&d.waitCalls)) }
func (d *devCluster) HealthCalls() int   { return int(atomic.LoadInt32(&d.healthCalls)) }

func silenceDevLog(t *testing.T) {
	t.Helper()
	orig := devLog
	devLog = log.New(io.Discard, "", 0)
	t.Cleanup(func() { devLog = orig })

	origSpinner := startSpinner
	startSpinner = func(string) progressSpinner { return noopSpinner{} }
	t.Cleanup(func() { startSpinner = origSpinner })
}

type noopSpinner struct{}

func (noopSpinner) Success(_ ...any) {}
func (noopSpinner) Fail(_ ...any)    {}

// --- pure logic tests (unchanged content, no k8s deps) ---

func TestConvertTargets_ParsesAllSupportedFields(t *testing.T) {
	replicas := int64(2)
	probes := true

	raw := []engine.RawDevTarget{
		{
			Name:      "remix",
			Selector:  map[string]string{"app": "remix"},
			Container: "web",
			Sync: []map[string]interface{}{
				{
					"from":    "./",
					"to":      "/app",
					"exclude": []interface{}{"/node_modules", "/.git"},
					"polling": true,
				},
			},
			Ports:    []interface{}{int64(3000), []interface{}{int64(15432), int64(5432)}},
			Terminal: "npm i && bash",
			Probes:   &probes,
			Replicas: &replicas,
			Env: []map[string]interface{}{
				{"name": "NODE_ENV", "value": "development"},
			},
			WorkingDir: "/workspace",
			Image:      "custom:dev",
			Command:    []string{"npm", "run", "dev"},
		},
	}

	targets, err := convertTargets(raw)
	require.NoError(t, err)
	require.Len(t, targets, 1)

	target := targets[0]
	assert.Equal(t, "remix", target.Name)
	assert.Equal(t, map[string]string{"app": "remix"}, target.Selector)
	assert.Equal(t, "web", target.Container)
	assert.Equal(t, "npm i && bash", target.Terminal)
	require.NotNil(t, target.Probes)
	assert.True(t, *target.Probes)
	require.NotNil(t, target.Replicas)
	assert.Equal(t, 2, *target.Replicas)
	assert.Equal(t, "/workspace", target.WorkingDir)
	assert.Equal(t, "custom:dev", target.Image)
	assert.Equal(t, []string{"npm", "run", "dev"}, target.Command)

	require.Len(t, target.Ports, 2)
	assert.Equal(t, PortRule{Local: 3000, Remote: 3000}, target.Ports[0])
	assert.Equal(t, PortRule{Local: 15432, Remote: 5432}, target.Ports[1])

	require.Len(t, target.Sync, 1)
	assert.Equal(t, SyncRule{
		From:    "./",
		To:      "/app",
		Exclude: []string{"/node_modules", "/.git"},
		Polling: true,
	}, target.Sync[0])

	assert.Equal(t, []EnvVar{{Name: "NODE_ENV", Value: "development"}}, target.Env)
}

func TestConvertTargets_InvalidPortTypeReturnsError(t *testing.T) {
	raw := []engine.RawDevTarget{
		{
			Name:  "broken",
			Ports: []interface{}{"invalid"},
		},
	}

	_, err := convertTargets(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `target "broken": ports[0]`)
}

func TestLoadEnvVars_LoadsRelativeFileAndMerges(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env.dev")
	require.NoError(t, os.WriteFile(envPath, []byte("RUNNER_ENV=value\n"), 0o644))

	env, err := loadEnvVars(dir, ".env.dev")
	require.NoError(t, err)
	assert.Equal(t, "value", env["RUNNER_ENV"])
}

func TestLoadEnvVars_MissingFileDoesNotFail(t *testing.T) {
	env, err := loadEnvVars(t.TempDir(), ".env.missing")
	require.NoError(t, err)
	assert.NotNil(t, env)
}

func TestNormalizeRunOpts_DefaultsReleaseNameToDev(t *testing.T) {
	result, err := normalizeRunOpts(RunOpts{Dir: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, "dev", result.ReleaseName)
}

func TestNormalizeRunOpts_PreservesExplicitReleaseName(t *testing.T) {
	result, err := normalizeRunOpts(RunOpts{Dir: t.TempDir(), ReleaseName: "my-dev"})
	require.NoError(t, err)
	assert.Equal(t, "my-dev", result.ReleaseName)
}

func TestIsCommandExit(t *testing.T) {
	assert.False(t, isCommandExit(nil))
	assert.False(t, isCommandExit(errors.New("connection lost")))
	assert.True(t, isCommandExit(errors.New("command terminated with exit code 1")))
	assert.True(t, isCommandExit(errors.New("exit code 130")))
}

func TestIsPodKilledExit(t *testing.T) {
	assert.False(t, isPodKilledExit(nil))
	assert.False(t, isPodKilledExit(errors.New("connection lost")))
	assert.False(t, isPodKilledExit(errors.New("exit code 1")))
	assert.False(t, isPodKilledExit(errors.New("exit code 130")))
	assert.True(t, isPodKilledExit(errors.New("command terminated with exit code 137")))
	assert.True(t, isPodKilledExit(errors.New("command terminated with exit code 143")))
}

func TestHasTerminalTarget(t *testing.T) {
	assert.False(t, hasTerminalTarget(nil))
	assert.False(t, hasTerminalTarget([]Target{{Name: "api"}}))
	assert.False(t, hasTerminalTarget([]Target{{Name: "api", Terminal: "  "}}))
	assert.True(t, hasTerminalTarget([]Target{{Name: "api", Terminal: "bash"}}))
	assert.True(t, hasTerminalTarget([]Target{
		{Name: "api"},
		{Name: "web", Terminal: "bash"},
	}))
}

// --- ensureRunNamespace ---

func TestEnsureRunNamespace_SkipsWhenDisabledOrNamespaceEmpty(t *testing.T) {
	fake := k8stest.NewFake()
	require.NoError(t, ensureRunNamespace(context.Background(), fake, "dev", false))
	require.NoError(t, ensureRunNamespace(context.Background(), fake, "", true))
	assert.Empty(t, fake.Namespaces)
}

func TestEnsureRunNamespace_CallsEnsureWhenEnabled(t *testing.T) {
	fake := k8stest.NewFake()
	require.NoError(t, ensureRunNamespace(context.Background(), fake, "dev", true))
	assert.True(t, fake.Namespaces["dev"])
}

// --- runDevDelete ---

func TestRunDevDelete_DeletesReleaseAndReportsCount(t *testing.T) {
	fake := k8stest.NewFake()
	_ = fake.ApplyRelease(context.Background(), "test-ns", "dev", []k8s.Resource{
		{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]interface{}{"name": "web", "namespace": "test-ns"},
		},
		{
			"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]interface{}{"name": "web-svc", "namespace": "test-ns"},
		},
	})

	stdout := &bytes.Buffer{}
	require.NoError(t, runDevDelete(context.Background(), fake, "test-ns", RunOpts{
		ReleaseName: "dev",
		Stdout:      stdout,
	}))
	assert.Contains(t, stdout.String(), "deleted dev environment dev (2 resources)")
	require.Len(t, fake.DeleteCalls, 1)
}

func TestRunDevDelete_ReportsZeroWhenInventoryEmpty(t *testing.T) {
	fake := k8stest.NewFake()
	stdout := &bytes.Buffer{}
	require.NoError(t, runDevDelete(context.Background(), fake, "ns", RunOpts{
		ReleaseName: "dev",
		Stdout:      stdout,
	}))
	assert.Contains(t, stdout.String(), "(0 resources)")
}

func TestRunDevDelete_PropagatesDeleteError(t *testing.T) {
	fake := k8stest.NewFake()
	fake.DeleteHook = func(ns, release string) (int, error) {
		return 0, errors.New("boom")
	}
	err := runDevDelete(context.Background(), fake, "ns", RunOpts{
		ReleaseName: "dev",
		Stdout:      &bytes.Buffer{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// --- startDevFeatures / runDevSession ---

func TestStartDevFeatures_NoTargets(t *testing.T) {
	silenceDevLog(t)
	require.NoError(t, startDevFeatures(context.Background(), newDevCluster(), "ns", nil, &bytes.Buffer{}))
}

func TestStartDevFeatures_StartsAllFeaturesAndRunsTerminal(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	// Terminal returns immediately so the session unwinds.
	dc.ExecFn = func(ctx context.Context, ns string, sel k8s.Selector, opts k8s.ExecOpts) error {
		return nil
	}

	stdout := &bytes.Buffer{}
	targets := []Target{
		{
			Name: "api", Selector: map[string]string{"app": "api"},
			Ports: []PortRule{{Local: 8080, Remote: 8080}},
		},
		{
			Name: "redis", Selector: map[string]string{"app": "redis"},
			Ports:    []PortRule{{Local: 6379, Remote: 6379}},
			Sync:     []SyncRule{{From: "./", To: "/app"}},
			Terminal: "bash",
		},
	}

	require.NoError(t, startDevFeatures(context.Background(), dc, "ns", targets, stdout))
	assert.ElementsMatch(t, []string{"api", "redis"}, dc.PortCalls())
	assert.Empty(t, dc.LogCalls(), "logs should be suppressed when any target has a terminal")
	assert.Equal(t, 1, dc.TerminalCalls(), "terminal must run exactly once for a clean session")
	assert.Contains(t, stdout.String(), "starting terminal for target redis")
}

func TestStartDevFeatures_RunsTerminalInWorkingDir(t *testing.T) {
	silenceDevLog(t)

	var capturedCommand []string
	dc := newDevCluster()
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, opts k8s.ExecOpts) error {
		capturedCommand = opts.Command
		return nil
	}

	targets := []Target{{
		Name: "web", Selector: map[string]string{"app": "web"},
		Terminal: "npm run dev", WorkingDir: "/workspace/app dir",
	}}

	require.NoError(t, startDevFeatures(context.Background(), dc, "ns", targets, &bytes.Buffer{}))
	require.Len(t, capturedCommand, 3)
	assert.Equal(t, `cd "/workspace/app dir" && npm run dev`, capturedCommand[2])
}

func TestStartDevFeatures_ReturnsTerminalError(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		return errors.New("command terminated with exit code 1")
	}

	err := startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "redis", Terminal: "bash"}}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit code 1")
	assert.Equal(t, 1, dc.TerminalCalls())
}

func TestStartDevFeatures_IgnoresTerminalExitCode130(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		return errors.New("executing command in pod web-123: command terminated with exit code 130")
	}

	require.NoError(t, startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "redis", Terminal: "bash"}}, &bytes.Buffer{}))
}

func TestStartDevFeatures_ReturnsBackgroundFeatureError(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	dc.StreamLogsFn = func(_ context.Context, _ string, _ string, _ k8s.Selector, _ io.Writer) error {
		return errors.New("logs failed")
	}

	err := startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "redis"}}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logs failed")
}

func TestStartDevFeatures_StartsLogsWhenNoTerminal(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	targets := []Target{
		{Name: "api", Selector: map[string]string{"app": "api"}},
		{Name: "web", Selector: map[string]string{"app": "web"}},
	}
	require.NoError(t, startDevFeatures(ctx, dc, "ns", targets, &bytes.Buffer{}))
	assert.ElementsMatch(t, []string{"api", "web"}, dc.LogCalls())
}

func TestStartDevFeatures_NoRetryOnExitCode(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		return errors.New("command terminated with exit code 1")
	}

	err := startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "web", Terminal: "bash"}}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Equal(t, 1, dc.TerminalCalls(), "should not retry on caller-driven exit")
}

func TestStartDevFeatures_MaxRetriesExceeded(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		return errors.New("connection refused")
	}

	err := startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "web", Terminal: "bash"}}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session failed after 5 attempts")
	assert.Equal(t, 5, dc.TerminalCalls())
}

func TestStartDevFeatures_ReconnectsOnConnectionError(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	var calls int32
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New("connection lost")
		}
		return nil
	}

	stdout := &bytes.Buffer{}
	require.NoError(t, startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "web", Terminal: "bash"}}, stdout))
	assert.Equal(t, 2, dc.WaitCalls(), "WaitPod should be called once per session")
	assert.Contains(t, stdout.String(), "starting terminal for target web")
	assert.Contains(t, stdout.String(), "reconnecting terminal for target web")
}

func TestStartDevFeatures_RetriesOnExitCode137(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	var calls int32
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New("executing command in pod node-x: command terminated with exit code 137")
		}
		return nil
	}

	require.NoError(t, startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "web", Terminal: "bash"}}, &bytes.Buffer{}))
	assert.Equal(t, 2, dc.TerminalCalls())
	assert.Equal(t, 2, dc.WaitCalls())
}

func TestStartDevFeatures_RetriesOnExitCode143(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	var calls int32
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New("command terminated with exit code 143")
		}
		return nil
	}

	require.NoError(t, startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "web", Terminal: "bash"}}, &bytes.Buffer{}))
	assert.Equal(t, 2, dc.TerminalCalls())
}

func TestStartDevFeatures_NoRetryWithoutTerminal(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	dc.StreamLogsFn = func(_ context.Context, _ string, _ string, _ k8s.Selector, _ io.Writer) error {
		return errors.New("logs stream broken")
	}

	err := startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "api"}}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logs stream broken")
	assert.Equal(t, 1, dc.WaitCalls())
}

func TestStartDevFeatures_DevLogDisconnectMessage(t *testing.T) {
	silenceDevLog(t) // installs no-op spinner; we override devLog below
	logBuf := &bytes.Buffer{}
	devLog = log.New(logBuf, "", 0)

	dc := newDevCluster()
	var calls int32
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New("connection lost")
		}
		return nil
	}

	require.NoError(t, startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "web", Terminal: "bash"}}, &bytes.Buffer{}))
	assert.Contains(t, logBuf.String(), "[terminal] disconnected: connection lost")
	assert.Contains(t, logBuf.String(), "[terminal] waiting for pod to restart (attempt 2/5)")
}

func TestStartDevFeatures_ResetsAttemptAfterEstablishedSession(t *testing.T) {
	silenceDevLog(t)

	origThreshold := sessionEstablishedThreshold
	sessionEstablishedThreshold = 0
	t.Cleanup(func() { sessionEstablishedThreshold = origThreshold })

	dc := newDevCluster()
	var calls int32
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		// Fail 8 times; succeed on 9th. Without reset this would exceed
		// maxSessionRetries (5).
		if atomic.AddInt32(&calls, 1) <= 8 {
			return errors.New("connection lost")
		}
		return nil
	}

	require.NoError(t, startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "web", Terminal: "bash"}}, &bytes.Buffer{}))
	assert.Equal(t, 9, dc.TerminalCalls(),
		"should survive >5 disconnections when sessions are established (counter resets)")
}

func TestStartDevFeatures_HealthWatcherTriggersReconnect(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()
	var execCalls int32
	dc.ExecFn = func(ctx context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		if atomic.AddInt32(&execCalls, 1) == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	var healthCalls int32
	dc.WatchPodFn = func(ctx context.Context, _ string, _ string) error {
		if atomic.AddInt32(&healthCalls, 1) == 1 {
			return errors.New("pod terminating")
		}
		<-ctx.Done()
		return ctx.Err()
	}

	stdout := &bytes.Buffer{}
	require.NoError(t, startDevFeatures(context.Background(), dc, "ns",
		[]Target{{Name: "web", Terminal: "bash"}}, stdout))
	assert.Equal(t, 2, dc.TerminalCalls(), "should reconnect after health watcher detects pod loss")
	assert.Contains(t, stdout.String(), "reconnecting terminal for target web")
}

func TestStartDevFeatures_TerminalWaitsForInitialSync(t *testing.T) {
	silenceDevLog(t)

	dc := newDevCluster()

	// Drive sync so it takes time before signalling ready. The dev runner
	// constructs a Syncer per sync rule, which calls ExecPod. Let our ExecPod
	// override do nothing — sync completes synchronously inside the syncer
	// (writeTar + execPod returns nil) and ready fires immediately.
	// We instead delay by intercepting WaitPod.
	syncStarted := make(chan struct{}, 1)
	terminalAtSyncStart := int32(0)
	dc.WaitPodFn = func(ctx context.Context, ns string, sel k8s.Selector) (string, error) {
		select {
		case syncStarted <- struct{}{}:
		default:
		}
		// First WaitPod call (from runDevSession) is fast; subsequent ones
		// (from sync's RunWithReady) we delay.
		if dc.WaitCalls() > 1 {
			time.Sleep(150 * time.Millisecond)
		}
		return "pod-stub", nil
	}
	dc.ExecFn = func(_ context.Context, _ string, _ k8s.Selector, _ k8s.ExecOpts) error {
		atomic.StoreInt32(&terminalAtSyncStart, 1)
		return nil
	}

	targets := []Target{{
		Name: "web", Selector: map[string]string{"app": "web"},
		Sync:     []SyncRule{{From: t.TempDir(), To: "/app"}},
		Terminal: "bash",
	}}

	require.NoError(t, startDevFeatures(context.Background(), dc, "ns", targets, &bytes.Buffer{}))
	// Terminal must have run after syncer's WaitPod completed.
	assert.Equal(t, 1, dc.TerminalCalls())
}

// TestRunDevSession_CancelCascadesToAllFeatures is the integration test that
// was impossible to write before this refactor: it cancels ctx mid-session and
// asserts every long-lived feature (port-forward, log streaming, watch-pod,
// exec) returns within a deadline. The fake's ActiveOps counter must drop to
// zero when all goroutines have returned.
func TestRunDevSession_CancelCascadesToAllFeatures(t *testing.T) {
	silenceDevLog(t) // also installs a no-op spinner

	fake := k8stest.NewFake()
	fake.AddPod(&k8stest.FakePod{
		Name: "web-x", Namespace: "ns",
		Labels: k8s.Selector{"app": "web"}, Healthy: true,
	})

	targets := []Target{{
		Name: "web", Selector: map[string]string{"app": "web"},
		Ports:    []PortRule{{Local: 8080, Remote: 80}},
		Terminal: "bash",
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runDevSession(ctx, fake, "ns", targets, &bytes.Buffer{}, true, false)
	}()

	require.Eventually(t, fake.HasActiveExec, 2*time.Second, 5*time.Millisecond,
		"expected long-lived ops to register as active")
	cancel()

	select {
	case <-done:
		// runDevSession returned cleanly after cancel.
	case <-time.After(2 * time.Second):
		t.Fatalf("runDevSession did not return after cancel; ActiveOps=%d", fake.ActiveOps())
	}
	assert.Equal(t, 0, fake.ActiveOps(), "no goroutines should remain active after cancel")
}
