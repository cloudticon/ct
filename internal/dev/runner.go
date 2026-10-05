package dev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/cloudticon/ct/pkg/k8s"
	ctsync "github.com/cloudticon/ct/pkg/sync"
	"github.com/fatih/color"
	"github.com/go-logr/logr"
	"github.com/pterm/pterm"
	"k8s.io/klog/v2"
)

var devLog = log.New(os.Stderr, "", 0)

const maxSessionRetries = 5

var sessionEstablishedThreshold = 10 * time.Second

// sessionRetryBackoff is the wait before the first reconnect; it doubles for
// every further attempt, up to maxSessionRetryBackoff.
var (
	sessionRetryBackoff    = time.Second
	maxSessionRetryBackoff = 8 * time.Second
)

// podLostError marks a session that ended because the pod went away or
// became unhealthy (reported by the health watcher). It is always worth a
// reconnect, even if the message mentions an exit code of a crashed
// container.
type podLostError struct{ err error }

func (e *podLostError) Error() string { return e.err.Error() }
func (e *podLostError) Unwrap() error { return e.err }

type RunOpts struct {
	Dir             string
	EnvFile         string
	KubeCtx         string
	ReleaseName     string
	Delete          bool
	CreateNamespace bool
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
}

// newClusterFn is the single seam for constructing a k8s.Cluster from CLI
// flags. Tests override this with k8stest.NewFake() to exercise the dev
// runner without a real cluster.
var newClusterFn = func(kubeCtx, namespace string) (k8s.Cluster, error) {
	return k8s.NewLiveCluster(k8s.LiveOpts{
		KubeContext:      kubeCtx,
		DefaultNamespace: namespace,
	})
}

// progressSpinner is the minimal contract runDevSession needs from a spinner.
// pterm.SpinnerPrinter satisfies it; tests substitute a no-op to dodge a
// known data race in pterm v0.12.83 (IsActive read/write between the spinner
// goroutine and Stop) which surfaces under -race in fast-path tests.
type progressSpinner interface {
	Success(args ...any)
	Fail(args ...any)
}

var startSpinner = func(text string) progressSpinner {
	sp, _ := pterm.DefaultSpinner.Start(text)
	return ptermSpinnerAdapter{sp}
}

type ptermSpinnerAdapter struct{ *pterm.SpinnerPrinter }

func (p ptermSpinnerAdapter) Success(args ...any) { p.SpinnerPrinter.Success(args...) }
func (p ptermSpinnerAdapter) Fail(args ...any)    { p.SpinnerPrinter.Fail(args...) }

// startDevFeatures runs the dev session in a retry loop. It returns when the
// terminal exits cleanly, when ctx is cancelled, or when retries are exhausted.
//
// The retry policy:
//   - ctx cancellation (Ctrl+C) ends the session successfully
//   - no retries when no target has a terminal (background features fail-fast)
//   - the pod going away or becoming unhealthy (health watcher) always retries
//   - exit code 130 (Ctrl+C) returns success
//   - non-pod-kill exit codes return the error verbatim (caller-driven exit)
//   - exit codes 137/143 (SIGKILL/SIGTERM, i.e. pod restart) and connection
//     errors retry up to maxSessionRetries with exponential backoff; the
//     counter resets after a session stays up for sessionEstablishedThreshold.
func startDevFeatures(ctx context.Context, cluster k8s.Cluster, namespace string, targets []Target, stdout io.Writer) error {
	if len(targets) == 0 {
		return nil
	}

	hasTerminal := hasTerminalTarget(targets)
	// Log streams of all targets write to stdout concurrently.
	stdout = &lockedWriter{w: stdout}

	for attempt := 0; ; attempt++ {
		sessionStart := time.Now()
		err := runDevSession(ctx, cluster, namespace, targets, stdout, hasTerminal, attempt > 0)

		if ctx.Err() != nil {
			// Interrupted by the user: that is how a dev session ends.
			return nil
		}
		if err == nil {
			return nil
		}
		if !hasTerminal {
			return err
		}
		var lost *podLostError
		if !errors.As(err, &lost) {
			if isTerminalExitCode130(err) {
				return nil
			}
			if isCommandExit(err) && !isPodKilledExit(err) {
				return err
			}
		}
		if time.Since(sessionStart) >= sessionEstablishedThreshold {
			attempt = 0
		}
		if attempt >= maxSessionRetries-1 {
			return fmt.Errorf("session failed after %d attempts: %w", attempt+1, err)
		}

		devLog.Printf("[terminal] disconnected: %v", err)
		devLog.Printf("[terminal] waiting for pod to restart (attempt %d/%d)...", attempt+2, maxSessionRetries)
		if !sleepCtx(ctx, retryBackoff(attempt)) {
			return nil
		}
	}
}

// lockedWriter serializes writes to a writer shared by several goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		return len(p), nil
	}
	return l.w.Write(p)
}

// retryBackoff returns the wait before reconnect attempt+1.
func retryBackoff(attempt int) time.Duration {
	d := sessionRetryBackoff
	for i := 0; i < attempt && d < maxSessionRetryBackoff; i++ {
		d *= 2
	}
	if d > maxSessionRetryBackoff {
		d = maxSessionRetryBackoff
	}
	return d
}

// sleepCtx waits for d and reports whether it did so without ctx ending.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func runDevSession(ctx context.Context, cluster k8s.Cluster, namespace string, targets []Target, stdout io.Writer, hasTerminal bool, reconnect bool) error {
	podNames := make(map[string]string, len(targets))
	for _, t := range targets {
		sp := startSpinner("waiting for pod " + t.Name + "...")
		podName, err := cluster.WaitPod(ctx, namespace, t.Selector)
		if err != nil {
			sp.Fail("pod " + t.Name + " failed: " + err.Error())
			return err
		}
		sp.Success("pod " + t.Name + " ready")
		podNames[t.Name] = podName
	}

	featuresCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	// fail records the first feature failure and stops the session. Errors
	// returned after the session was asked to stop are consequences of the
	// cancellation, not failures of their own.
	fail := func(err error) {
		if err == nil || errors.Is(err, context.Canceled) || featuresCtx.Err() != nil {
			return
		}
		select {
		case errCh <- err:
		default:
		}
		cancel()
	}

	startFeature := func(fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fail(fn(featuresCtx))
		}()
	}

	var syncWg sync.WaitGroup
	var syncCount int

	for _, target := range targets {
		target := target

		if len(target.Ports) > 0 {
			k8sPorts := toK8sPortRules(target.Ports)
			startFeature(func(gctx context.Context) error {
				return cluster.PortForward(gctx, namespace, target.Selector, k8sPorts)
			})
		}

		for _, rule := range target.Sync {
			rule := rule
			syncCount++
			syncWg.Add(1)
			startFeature(func(gctx context.Context) error {
				syncer := ctsync.NewSyncer(cluster, namespace, target.Selector, ctsync.SyncRule{
					From:      rule.From,
					To:        rule.To,
					Exclude:   append([]string(nil), rule.Exclude...),
					Polling:   rule.Polling,
					Container: target.Container,
				})
				return syncer.RunWithReady(gctx, func(err error) {
					// Record a failure before releasing the waiter so it can
					// never mistake a failed initial sync for a completed one.
					fail(err)
					syncWg.Done()
				})
			})
		}

		if !hasTerminal {
			startFeature(func(gctx context.Context) error {
				return cluster.StreamLogs(gctx, namespace, target.Name, target.Selector, stdout)
			})
		}

		if hasTerminal && strings.TrimSpace(target.Terminal) != "" {
			podName := podNames[target.Name]
			startFeature(func(gctx context.Context) error {
				if err := cluster.WatchPod(gctx, namespace, podName); err != nil {
					return &podLostError{err: err}
				}
				return nil
			})
		}
	}

	// waitFeatures blocks until every feature returned and reports the first
	// failure. (Selecting on errCh and a "done" channel instead would pick
	// at random when both are ready and could drop the error.)
	waitFeatures := func() error {
		wg.Wait()
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}

	if hasTerminal && syncCount > 0 {
		syncReady := make(chan struct{})
		go func() {
			syncWg.Wait()
			close(syncReady)
		}()

		sp := startSpinner("waiting for initial sync...")
		select {
		case <-syncReady:
		case <-featuresCtx.Done():
		}
		// A failed sync cancels featuresCtx before it marks itself ready.
		if featuresCtx.Err() != nil {
			sp.Fail("sync interrupted")
			cancel()
			return waitFeatures()
		}
		sp.Success("initial sync complete")
	}

	if hasTerminal {
		defer silenceLibraryLogs()()
	}

	for _, target := range targets {
		if strings.TrimSpace(target.Terminal) == "" {
			continue
		}

		if stdout != nil {
			if reconnect {
				fmt.Fprintf(stdout, "%s for target %s\n", color.CyanString("reconnecting terminal"), target.Name)
			} else {
				fmt.Fprintf(stdout, "%s for target %s\n", color.CyanString("starting terminal"), target.Name)
			}
		}

		terminalErr := cluster.Exec(featuresCtx, namespace, target.Selector, k8s.ExecOpts{
			Container: target.Container,
			Command:   []string{"/bin/sh", "-c", terminalCommand(target)},
			Stdin:     os.Stdin,
			Stdout:    os.Stdout,
			Stderr:    os.Stderr,
			TTY:       true,
		})
		cancel()

		if waitErr := waitFeatures(); waitErr != nil {
			return waitErr
		}
		return terminalErr
	}

	select {
	case <-ctx.Done():
		cancel()
		_ = waitFeatures()
		return nil
	default:
		return waitFeatures()
	}
}

// silenceLibraryLogs mutes the standard logger and klog (client-go) so they
// do not draw over the interactive terminal, and returns a function that
// restores them. It used to swap the global os.Stderr for /dev/null, which
// raced with feature goroutines already logging through klog and closed the
// file under late writers; both loggers serialize these changes internally.
func silenceLibraryLogs() (restore func()) {
	origLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	klog.SetLogger(logr.Discard())
	return func() {
		klog.ClearLogger()
		log.SetOutput(origLogWriter)
	}
}

func toK8sPortRules(ports []PortRule) []k8s.PortRule {
	out := make([]k8s.PortRule, 0, len(ports))
	for _, p := range ports {
		out = append(out, k8s.PortRule{Local: p.Local, Remote: p.Remote})
	}
	return out
}

func terminalCommand(target Target) string {
	cmd := target.Terminal
	if strings.TrimSpace(target.WorkingDir) == "" {
		return cmd
	}
	return fmt.Sprintf("cd %s && %s", strconv.Quote(target.WorkingDir), cmd)
}

func isTerminalExitCode130(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "exit code 130")
}

func isCommandExit(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "exit code")
}

func isPodKilledExit(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "exit code 137") || strings.Contains(msg, "exit code 143")
}

func hasTerminalTarget(targets []Target) bool {
	for _, t := range targets {
		if strings.TrimSpace(t.Terminal) != "" {
			return true
		}
	}
	return false
}

func Run(ctx context.Context, opts RunOpts) error {
	normalizedOpts, err := normalizeRunOpts(opts)
	if err != nil {
		return err
	}

	envVars, err := loadEnvVars(normalizedOpts.Dir, normalizedOpts.EnvFile)
	if err != nil {
		return err
	}

	devCode, err := bundleEntry(normalizedOpts.Dir, "dev.ct")
	if err != nil {
		return fmt.Errorf("bundling dev.ct: %w", err)
	}

	promptCache, err := engine.NewPromptCache(normalizedOpts.Dir)
	if err != nil {
		return fmt.Errorf("creating prompt cache: %w", err)
	}

	devResult, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   devCode,
		EnvVars:  envVars,
		PromptFn: cancellablePrompt(ctx, engine.MakePromptFn(promptCache, normalizedOpts.Stdin, normalizedOpts.Stdout)),
	})
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted: %w", ctx.Err())
		}
		return fmt.Errorf("executing dev.ct: %w", err)
	}

	cluster, err := newClusterFn(normalizedOpts.KubeCtx, devResult.Namespace)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	if normalizedOpts.Delete {
		return runDevDelete(ctx, cluster, devResult.Namespace, normalizedOpts)
	}

	targets, err := convertTargets(devResult.Targets)
	if err != nil {
		return err
	}
	if err := resolveSyncSources(normalizedOpts.Dir, targets); err != nil {
		return err
	}

	resources, err := renderMainResources(normalizedOpts.Dir, devResult.Namespace, devResult.Values)
	if err != nil {
		return err
	}

	if err := ResolveSelectors(targets, resources); err != nil {
		return err
	}
	if err := ResolveContainers(targets, resources); err != nil {
		return err
	}
	PatchResources(resources, targets)

	resources = k8s.InjectReleaseLabels(resources, normalizedOpts.ReleaseName)

	if err := ensureRunNamespace(ctx, cluster, devResult.Namespace, normalizedOpts.CreateNamespace); err != nil {
		return err
	}

	if err := cluster.ApplyRelease(ctx, devResult.Namespace, normalizedOpts.ReleaseName, resources); err != nil {
		return fmt.Errorf("applying resources: %w", err)
	}

	if err := startDevFeatures(ctx, cluster, devResult.Namespace, targets, normalizedOpts.Stdout); err != nil {
		return fmt.Errorf("starting dev features: %w", err)
	}

	return nil
}

// cancellablePrompt makes a blocking prompt give up when ctx is cancelled
// (Ctrl+C). The read itself cannot be interrupted; it is abandoned, which is
// fine because the process is about to exit.
func cancellablePrompt(ctx context.Context, prompt func(string) (string, error)) func(string) (string, error) {
	type answer struct {
		value string
		err   error
	}
	return func(question string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		ch := make(chan answer, 1)
		go func() {
			value, err := prompt(question)
			ch <- answer{value, err}
		}()
		select {
		case a := <-ch:
			return a.value, a.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func ensureRunNamespace(ctx context.Context, cluster k8s.Cluster, namespace string, createNamespace bool) error {
	if !createNamespace || namespace == "" {
		return nil
	}
	if err := cluster.EnsureNamespace(ctx, namespace); err != nil {
		return fmt.Errorf("ensuring namespace %q: %w", namespace, err)
	}
	return nil
}

func runDevDelete(ctx context.Context, cluster k8s.Cluster, namespace string, opts RunOpts) error {
	deleted, err := cluster.DeleteRelease(ctx, namespace, opts.ReleaseName)
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "%s dev environment %s (%d resources)\n", color.HiRedString("deleted"), opts.ReleaseName, deleted)
	return nil
}

func normalizeRunOpts(opts RunOpts) (RunOpts, error) {
	result := opts
	if result.Dir == "" {
		result.Dir = "."
	}
	if result.ReleaseName == "" {
		result.ReleaseName = "dev"
	}

	absDir, err := filepath.Abs(result.Dir)
	if err != nil {
		return RunOpts{}, fmt.Errorf("resolving directory: %w", err)
	}
	result.Dir = absDir

	if result.Stdin == nil {
		result.Stdin = os.Stdin
	}
	if result.Stdout == nil {
		result.Stdout = os.Stdout
	}
	if result.Stderr == nil {
		result.Stderr = os.Stderr
	}

	return result, nil
}

func loadEnvVars(dir, envFile string) (map[string]string, error) {
	if envFile == "" {
		return engine.MergeEnvWithSystem(nil), nil
	}

	envPath := envFile
	if !filepath.IsAbs(envPath) {
		envPath = filepath.Join(dir, envPath)
	}

	fileEnv, err := engine.LoadEnvFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return engine.MergeEnvWithSystem(nil), nil
		}
		return nil, fmt.Errorf("loading env file %s: %w", envPath, err)
	}

	return engine.MergeEnvWithSystem(fileEnv), nil
}

func bundleEntry(dir, fileName string) (string, error) {
	entryPath := filepath.Join(dir, fileName)
	if _, err := os.Stat(entryPath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("entry point not found: %s", entryPath)
		}
		return "", fmt.Errorf("checking %s: %w", fileName, err)
	}

	tr := engine.NewTranspiler(dir)
	jsCode, err := tr.Bundle(entryPath)
	if err != nil {
		return "", fmt.Errorf("bundle failed: %w", err)
	}
	return jsCode, nil
}

func renderMainResources(dir, namespace string, overlayValues map[string]interface{}) ([]engine.Resource, error) {
	mainCode, err := bundleEntry(dir, "main.ct")
	if err != nil {
		return nil, fmt.Errorf("bundling main.ct: %w", err)
	}

	baseValues, err := loadBaseValues(dir)
	if err != nil {
		return nil, err
	}

	mergedValues := DeepMergeValues(baseValues, overlayValues)
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    mainCode,
		Values:    mergedValues,
		Namespace: namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("executing main.ct: %w", err)
	}

	return resources, nil
}

func loadBaseValues(dir string) (map[string]interface{}, error) {
	valuesPath := resolveValuesPath(dir)
	if valuesPath == "" {
		return nil, nil
	}

	values, err := engine.LoadValuesFile(valuesPath, nil)
	if err != nil {
		return nil, fmt.Errorf("loading values from %s: %w", valuesPath, err)
	}
	return values, nil
}

func resolveValuesPath(dir string) string {
	for _, name := range []string{"values.json", "values.yaml", "values.yml"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func convertTargets(rawTargets []engine.RawDevTarget) ([]Target, error) {
	targets := make([]Target, 0, len(rawTargets))
	localPortOwner := map[int]string{}
	for _, raw := range rawTargets {
		syncRules, err := parseSyncRules(raw.Name, raw.Sync)
		if err != nil {
			return nil, err
		}

		portRules, err := parsePortRules(raw.Name, raw.Ports)
		if err != nil {
			return nil, err
		}
		for _, p := range portRules {
			if owner, taken := localPortOwner[p.Local]; taken {
				return nil, fmt.Errorf("local port %d is forwarded twice (targets %q and %q); each local port can only be forwarded once",
					p.Local, owner, raw.Name)
			}
			localPortOwner[p.Local] = raw.Name
		}

		envVars, err := parseEnvVars(raw.Name, raw.Env)
		if err != nil {
			return nil, err
		}

		target := Target{
			Name:       raw.Name,
			Selector:   raw.Selector,
			Container:  raw.Container,
			Sync:       syncRules,
			Ports:      portRules,
			Terminal:   raw.Terminal,
			Probes:     raw.Probes,
			Env:        envVars,
			WorkingDir: raw.WorkingDir,
			Image:      raw.Image,
			Command:    raw.Command,
		}

		if raw.Replicas != nil {
			replicas := int(*raw.Replicas)
			target.Replicas = &replicas
		}

		targets = append(targets, target)
	}
	return targets, nil
}

func parseSyncRules(targetName string, rawSync []map[string]interface{}) ([]SyncRule, error) {
	rules := make([]SyncRule, 0, len(rawSync))
	for i, rawRule := range rawSync {
		from, ok := rawRule["from"].(string)
		if !ok || strings.TrimSpace(from) == "" {
			return nil, fmt.Errorf("target %q: sync[%d].from must be a non-empty string", targetName, i)
		}
		to, ok := rawRule["to"].(string)
		if !ok || strings.TrimSpace(to) == "" {
			return nil, fmt.Errorf("target %q: sync[%d].to must be a non-empty string", targetName, i)
		}

		rule := SyncRule{
			From: from,
			To:   to,
		}

		if rawExclude, present := rawRule["exclude"]; present && rawExclude != nil {
			list, ok := rawExclude.([]interface{})
			if !ok {
				return nil, fmt.Errorf("target %q: sync[%d].exclude must be an array of patterns, got %T", targetName, i, rawExclude)
			}
			rule.Exclude = make([]string, 0, len(list))
			for j, item := range list {
				pattern, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("target %q: sync[%d].exclude[%d] must be a string, got %T", targetName, i, j, item)
				}
				rule.Exclude = append(rule.Exclude, pattern)
			}
		}

		if rawPolling, present := rawRule["polling"]; present && rawPolling != nil {
			polling, ok := rawPolling.(bool)
			if !ok {
				return nil, fmt.Errorf("target %q: sync[%d].polling must be a boolean, got %T", targetName, i, rawPolling)
			}
			rule.Polling = polling
		}

		rules = append(rules, rule)
	}
	return rules, nil
}

// resolveSyncSources makes relative sync sources relative to the project
// directory (not the process working directory) and checks that they are
// directories, before anything is applied to the cluster.
func resolveSyncSources(dir string, targets []Target) error {
	for i := range targets {
		for j := range targets[i].Sync {
			rule := &targets[i].Sync[j]
			from := rule.From
			if !filepath.IsAbs(from) {
				from = filepath.Join(dir, from)
			}
			info, err := os.Stat(from)
			if err != nil {
				return fmt.Errorf("target %q: sync[%d].from %q: %w", targets[i].Name, j, rule.From, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("target %q: sync[%d].from %q is not a directory; sync copies a directory (use exclude to leave files out)",
					targets[i].Name, j, rule.From)
			}
			rule.From = from
		}
	}
	return nil
}

func parsePortRules(targetName string, rawPorts []interface{}) ([]PortRule, error) {
	rules := make([]PortRule, 0, len(rawPorts))
	for i, rawPort := range rawPorts {
		switch v := rawPort.(type) {
		case int:
			rules = append(rules, PortRule{Local: v, Remote: v})
		case int64:
			n := int(v)
			rules = append(rules, PortRule{Local: n, Remote: n})
		case float64:
			n, err := floatToInt(v)
			if err != nil {
				return nil, fmt.Errorf("target %q: ports[%d]: %w", targetName, i, err)
			}
			rules = append(rules, PortRule{Local: n, Remote: n})
		case []interface{}:
			if len(v) != 2 {
				return nil, fmt.Errorf("target %q: ports[%d] tuple must have 2 items", targetName, i)
			}
			local, err := numericToInt(v[0])
			if err != nil {
				return nil, fmt.Errorf("target %q: ports[%d][0]: %w", targetName, i, err)
			}
			remote, err := numericToInt(v[1])
			if err != nil {
				return nil, fmt.Errorf("target %q: ports[%d][1]: %w", targetName, i, err)
			}
			rules = append(rules, PortRule{Local: local, Remote: remote})
		default:
			return nil, fmt.Errorf("target %q: ports[%d] must be number or [local,remote]", targetName, i)
		}
		last := rules[len(rules)-1]
		for _, port := range []int{last.Local, last.Remote} {
			if port < 1 || port > 65535 {
				return nil, fmt.Errorf("target %q: ports[%d]: port %d is out of range 1-65535", targetName, i, port)
			}
		}
	}
	return rules, nil
}

func parseEnvVars(targetName string, rawEnv []map[string]interface{}) ([]EnvVar, error) {
	result := make([]EnvVar, 0, len(rawEnv))
	for i, raw := range rawEnv {
		name, ok := raw["name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("target %q: env[%d].name must be a non-empty string", targetName, i)
		}
		value := ""
		switch v := raw["value"].(type) {
		case nil:
			// Missing/undefined/null: an empty value, as in Kubernetes
			// (it used to become the string "<nil>").
		case string, int64, float64, bool:
			value = fmt.Sprint(v)
		default:
			return nil, fmt.Errorf("target %q: env[%d].value must be a string, number or boolean, got %T", targetName, i, v)
		}
		result = append(result, EnvVar{Name: name, Value: value})
	}
	return result, nil
}

func numericToInt(v interface{}) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return floatToInt(n)
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}

func floatToInt(v float64) (int, error) {
	n := int(v)
	if float64(n) != v {
		return 0, fmt.Errorf("value %v is not an integer", v)
	}
	return n, nil
}
