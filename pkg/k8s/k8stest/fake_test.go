package k8stest_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/cloudticon/ct/pkg/k8s/k8stest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFake_ApplyRelease_RecordsAndStoresInventory(t *testing.T) {
	f := k8stest.NewFake()
	res := []k8s.Resource{{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "cm-1", "namespace": "demo"},
	}}

	require.NoError(t, f.ApplyRelease(context.Background(), "demo", "rel", res))

	require.Len(t, f.ApplyCalls, 1)
	assert.Equal(t, "demo", f.ApplyCalls[0].Namespace)
	assert.Equal(t, "rel", f.ApplyCalls[0].Release)

	releases, err := f.ListReleases(context.Background(), "demo", false)
	require.NoError(t, err)
	require.Len(t, releases, 1)
	assert.Equal(t, "rel", releases[0].Name)
	assert.Equal(t, 1, releases[0].Resources)
}

func TestFake_DeleteRelease_RemovesInventoryAndReturnsCount(t *testing.T) {
	f := k8stest.NewFake()
	res := []k8s.Resource{
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "a"}},
		{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]interface{}{"name": "b"}},
	}
	require.NoError(t, f.ApplyRelease(context.Background(), "demo", "rel", res))

	deleted, err := f.DeleteRelease(context.Background(), "demo", "rel")
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)

	releases, err := f.ListReleases(context.Background(), "demo", false)
	require.NoError(t, err)
	assert.Empty(t, releases)
}

func TestFake_WaitPod_BlocksUntilHealthy(t *testing.T) {
	f := k8stest.NewFake()
	f.AddPod(&k8stest.FakePod{
		Name:      "app-x",
		Namespace: "demo",
		Labels:    k8s.Selector{"app": "demo"},
		Healthy:   false,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan struct {
		name string
		err  error
	}, 1)
	go func() {
		name, err := f.WaitPod(ctx, "demo", k8s.Selector{"app": "demo"})
		done <- struct {
			name string
			err  error
		}{name, err}
	}()

	time.Sleep(30 * time.Millisecond)
	require.True(t, f.SetPodHealthy("demo", "app-x", true))

	select {
	case got := <-done:
		require.NoError(t, got.err)
		assert.Equal(t, "app-x", got.name)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitPod did not return after pod became healthy")
	}
}

func TestFake_LongLivedOpsCancelCleanly(t *testing.T) {
	// This is the test pattern the runDevSession refactor (PR4) will rely on:
	// after ctx cancel, every long-lived op (portForward, streamLogs, WatchPod,
	// Exec) returns and ActiveOps drops to zero.
	f := k8stest.NewFake()
	f.AddPod(&k8stest.FakePod{
		Name: "app-x", Namespace: "demo",
		Labels: k8s.Selector{"app": "demo"}, Healthy: true,
	})

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	spawn := func(fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- fn(ctx)
		}()
	}

	spawn(func(c context.Context) error {
		return f.PortForward(c, "demo", k8s.Selector{"app": "demo"}, []k8s.PortRule{{Local: 8080, Remote: 80}})
	})
	spawn(func(c context.Context) error {
		return f.StreamLogs(c, "demo", "demo", k8s.Selector{"app": "demo"}, &bytes.Buffer{})
	})
	spawn(func(c context.Context) error { return f.WatchPod(c, "demo", "app-x") })
	spawn(func(c context.Context) error {
		return f.Exec(c, "demo", k8s.Selector{"app": "demo"}, k8s.ExecOpts{Command: []string{"sh"}})
	})

	require.Eventually(t, f.HasActiveExec, time.Second, 5*time.Millisecond,
		"expected at least one long-lived op to register as active")
	cancel()

	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("long-lived ops did not return after cancel; ActiveOps=%d", f.ActiveOps())
	}
	close(errs)
	for err := range errs {
		require.True(t, errors.Is(err, context.Canceled),
			"every op must return context.Canceled after cancel; got %v", err)
	}
	assert.Equal(t, 0, f.ActiveOps(), "no goroutines should remain active")
}

func TestFake_StreamLogs_WritesPodContentBeforeBlocking(t *testing.T) {
	f := k8stest.NewFake()
	f.AddPod(&k8stest.FakePod{
		Name: "app-x", Namespace: "demo",
		Labels:     k8s.Selector{"app": "demo"},
		Healthy:    true,
		LogContent: "hello\nworld\n",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var buf bytes.Buffer
	_ = f.StreamLogs(ctx, "demo", "demo", k8s.Selector{"app": "demo"}, &buf)

	assert.Equal(t, "hello\nworld\n", buf.String())
}

func TestFake_ExecHook_DrivesBehavior(t *testing.T) {
	f := k8stest.NewFake()
	f.AddPod(&k8stest.FakePod{
		Name: "app-x", Namespace: "demo",
		Labels: k8s.Selector{"app": "demo"}, Healthy: true,
	})
	f.ExecHook = func(ns, pod string, opts k8s.ExecOpts) error {
		_, _ = opts.Stdout.Write([]byte("ran\n"))
		return nil
	}

	var out bytes.Buffer
	require.NoError(t, f.Exec(context.Background(), "demo", k8s.Selector{"app": "demo"}, k8s.ExecOpts{
		Command: []string{"echo", "hi"}, Stdout: &out,
	}))
	assert.Equal(t, "ran\n", out.String())
	require.Len(t, f.ExecCalls, 1)
	assert.Equal(t, "app-x", f.ExecCalls[0].Pod)
}

// Compile-time check that the Fake is usable through the narrow PodExecutor port.
func TestFake_SatisfiesPodExecutor(t *testing.T) {
	var _ k8s.PodExecutor = k8stest.NewFake()
}
