package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func TestPortForward_ReconnectsAfterForwardError(t *testing.T) {
	stubPortForward(t)
	portForwardBackoff = time.Millisecond
	origWaitForPodFn := waitForPodFn
	origForwardPortsFn := forwardPortsFn
	t.Cleanup(func() {
		waitForPodFn = origWaitForPodFn
		forwardPortsFn = origForwardPortsFn
	})

	waitCalls := 0
	waitForPodFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		waitCalls++
		return "pod-1", nil
	}

	forwardCalls := 0
	forwardPortsFn = func(_ context.Context, _ *client, _ string, _ []PortRule) error {
		forwardCalls++
		if forwardCalls == 1 {
			return errors.New("connection dropped")
		}
		return nil
	}

	err := portForward(context.Background(), &client{}, map[string]string{"app": "web"}, []PortRule{{Local: 3000, Remote: 3000}})
	require.NoError(t, err)
	assert.Equal(t, 2, waitCalls)
	assert.Equal(t, 2, forwardCalls)
}

func TestPortForward_GracefulOnContextCancel(t *testing.T) {
	origWaitForPodFn := waitForPodFn
	origForwardPortsFn := forwardPortsFn
	t.Cleanup(func() {
		waitForPodFn = origWaitForPodFn
		forwardPortsFn = origForwardPortsFn
	})

	ctx, cancel := context.WithCancel(context.Background())
	waitForPodFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "pod-1", nil
	}
	forwardPortsFn = func(_ context.Context, _ *client, _ string, _ []PortRule) error {
		cancel()
		return context.Canceled
	}

	err := portForward(ctx, &client{}, map[string]string{"app": "web"}, []PortRule{{Local: 3000, Remote: 3000}})
	require.NoError(t, err)
}

func TestPortForward_ReturnsWaitError(t *testing.T) {
	origWaitForPodFn := waitForPodFn
	origForwardPortsFn := forwardPortsFn
	t.Cleanup(func() {
		waitForPodFn = origWaitForPodFn
		forwardPortsFn = origForwardPortsFn
	})

	waitForPodFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "", errors.New("no pods")
	}
	forwardPortsFn = func(_ context.Context, _ *client, _ string, _ []PortRule) error {
		return nil
	}

	err := portForward(context.Background(), &client{}, map[string]string{"app": "web"}, []PortRule{{Local: 3000, Remote: 3000}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pods")
}

func TestPortForward_ValidatesInput(t *testing.T) {
	err := portForward(context.Background(), nil, map[string]string{"app": "web"}, []PortRule{{Local: 3000, Remote: 3000}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client is required")

	err = portForward(context.Background(), &client{}, map[string]string{"app": "web"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one port rule is required")
}

func TestToPFPorts(t *testing.T) {
	ports := toPFPorts([]PortRule{
		{Local: 3000, Remote: 3000},
		{Local: 15432, Remote: 5432},
	})

	assert.Equal(t, []string{"3000:3000", "15432:5432"}, ports)
}

func stubPortForward(t *testing.T) {
	t.Helper()
	origWait, origForward := waitForPodFn, forwardPortsFn
	origBackoff, origMax := portForwardBackoff, maxPortForwardBackoff
	t.Cleanup(func() {
		waitForPodFn, forwardPortsFn = origWait, origForward
		portForwardBackoff, maxPortForwardBackoff = origBackoff, origMax
	})
	waitForPodFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "pod-1", nil
	}
}

// "local port in use" can never be fixed by reconnecting; it used to be
// retried in a tight loop, each attempt opening a new API server connection.
func TestPortForward_LocalPortInUseIsFatal(t *testing.T) {
	stubPortForward(t)
	calls := 0
	forwardPortsFn = func(_ context.Context, _ *client, pod string, _ []PortRule) error {
		calls++
		return fmt.Errorf("forwarding ports for pod %s: %w", pod,
			errors.New("unable to listen on any of the requested ports: [{8080 80}]"))
	}

	done := make(chan error, 1)
	go func() {
		done <- portForward(context.Background(), &client{}, map[string]string{"app": "web"}, []PortRule{{Local: 8080, Remote: 80}})
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "8080")
		assert.Contains(t, err.Error(), "already in use")
		assert.Equal(t, 1, calls)
	case <-time.After(2 * time.Second):
		t.Fatal("portForward kept retrying an error that a reconnect cannot fix")
	}
}

func TestPortForward_BacksOffBetweenReconnects(t *testing.T) {
	stubPortForward(t)
	portForwardBackoff = 50 * time.Millisecond
	maxPortForwardBackoff = time.Second

	var times []time.Time
	forwardPortsFn = func(_ context.Context, _ *client, _ string, _ []PortRule) error {
		times = append(times, time.Now())
		if len(times) < 3 {
			return errors.New("lost connection to pod")
		}
		return nil
	}

	require.NoError(t, portForward(context.Background(), &client{}, map[string]string{"app": "web"}, []PortRule{{Local: 3000, Remote: 3000}}))
	require.Len(t, times, 3)
	assert.GreaterOrEqual(t, times[1].Sub(times[0]), 50*time.Millisecond)
	assert.GreaterOrEqual(t, times[2].Sub(times[1]), 100*time.Millisecond)
}

func TestPortForward_CancelDuringBackoffReturns(t *testing.T) {
	stubPortForward(t)
	portForwardBackoff = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	forwardPortsFn = func(_ context.Context, _ *client, _ string, _ []PortRule) error {
		time.AfterFunc(20*time.Millisecond, cancel)
		return errors.New("lost connection to pod")
	}

	done := make(chan error, 1)
	go func() {
		done <- portForward(ctx, &client{}, map[string]string{"app": "web"}, []PortRule{{Local: 3000, Remote: 3000}})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("portForward did not return after cancel during backoff")
	}
}

// Every failed attempt leaked two goroutines: one waiting for a ready signal
// that never comes, one waiting for ctx to close the stop channel.
func TestForwardPorts_DoesNotLeakGoroutinesOnFailedDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// client-go does not close the connection of a refused upgrade;
		// close it server-side so only our goroutines are counted.
		w.Header().Set("Connection", "close")
		http.Error(w, "upgrade refused", http.StatusBadRequest)
	}))
	defer srv.Close()
	cfg := &rest.Config{Host: srv.URL}
	core, err := corev1client.NewForConfig(cfg)
	require.NoError(t, err)
	c := &client{Config: cfg, CoreV1: core, Namespace: "ns"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ports := []PortRule{{Local: 18080, Remote: 80}}
	require.Error(t, forwardPorts(ctx, c, "pod-1", ports)) // warm up transport goroutines

	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		require.Error(t, forwardPorts(ctx, c, "pod-1", ports))
	}
	if !assert.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+3 }, 2*time.Second, 20*time.Millisecond,
		"goroutines leaked: before=%d now=%d", before, runtime.NumGoroutine()) {
		buf := make([]byte, 1<<20)
		t.Logf("%s", buf[:runtime.Stack(buf, true)])
	}
}

func TestLineLogger_LogsEachLine(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	input := []byte("Unable to listen on port 8080: in use\nsecond\n")
	n, err := (&lineLogger{prefix: "[pf]"}).Write(input)
	require.NoError(t, err)
	assert.Equal(t, len(input), n)
	assert.Contains(t, buf.String(), "[pf] Unable to listen on port 8080: in use\n")
	assert.Contains(t, buf.String(), "[pf] second\n")
}

// A refused upgrade because of RBAC cannot be fixed by reconnecting (and
// client-go leaks the connection of every refused upgrade).
func TestPortForward_ForbiddenIsFatal(t *testing.T) {
	stubPortForward(t)
	calls := 0
	forwardPortsFn = func(_ context.Context, _ *client, pod string, _ []PortRule) error {
		calls++
		return fmt.Errorf("forwarding ports for pod %s: error upgrading connection: pods %q is forbidden: User "+
			"\"dev\" cannot create resource \"pods/portforward\" in API group \"\" in the namespace \"ns\"", pod, pod)
	}

	err := portForward(context.Background(), &client{}, map[string]string{"app": "web"}, []PortRule{{Local: 8080, Remote: 80}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden")
	assert.Equal(t, 1, calls)
}
