package k8s

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// --- containerProblem ---

func TestContainerProblem_Healthy(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	assert.Empty(t, containerProblem(pod))
}

func TestContainerProblem_CrashLoopBackOff(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "node", State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				}},
			},
		},
	}
	assert.Equal(t, `container "node" is in CrashLoopBackOff`, containerProblem(pod))
}

func TestContainerProblem_ImagePullBackOff(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "api", State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
				}},
			},
		},
	}
	assert.Equal(t, `container "api" is in ImagePullBackOff`, containerProblem(pod))
}

func TestContainerProblem_ErrImagePull(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "worker", State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"},
				}},
			},
		},
	}
	assert.Equal(t, `container "worker" is in ErrImagePull`, containerProblem(pod))
}

func TestContainerProblem_CreateContainerConfigError(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "svc", State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError"},
				}},
			},
		},
	}
	assert.Equal(t, `container "svc" is in CreateContainerConfigError`, containerProblem(pod))
}

func TestContainerProblem_NoContainerStatuses(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{}}
	assert.Empty(t, containerProblem(pod))
}

func TestContainerProblem_WaitingWithUnknownReason(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "init", State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
				}},
			},
		},
	}
	assert.Empty(t, containerProblem(pod))
}

func TestContainerProblem_TerminatedCompleted(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0},
				}},
			},
		},
	}
	result := containerProblem(pod)
	assert.Contains(t, result, `container "app" has terminated`)
	assert.Contains(t, result, "Completed")
	assert.Contains(t, result, "exit code 0")
}

func TestContainerProblem_TerminatedOOMKilled(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "worker", State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
				}},
			},
		},
	}
	result := containerProblem(pod)
	assert.Contains(t, result, `container "worker" has terminated`)
	assert.Contains(t, result, "OOMKilled")
	assert.Contains(t, result, "exit code 137")
}

func TestContainerProblem_TerminatedError(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "api", State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
				}},
			},
		},
	}
	result := containerProblem(pod)
	assert.Contains(t, result, `container "api" has terminated`)
	assert.Contains(t, result, "exit code 1")
}

// --- waitForPod (moved from portforward_test.go) ---

func TestWaitForPod_ReturnsRunningPodFromList(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-pending", Namespace: "dev-ns", Labels: map[string]string{"app": "web"}},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-running", Namespace: "dev-ns", Labels: map[string]string{"app": "web"}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	client := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")

	pod, err := waitForPod(context.Background(), client, map[string]string{"app": "web"})
	require.NoError(t, err)
	assert.Equal(t, "pod-running", pod)
}

func TestWaitForPod_WaitsForRunningPodOnWatch(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	w := watch.NewFake()

	clientset.Fake.PrependWatchReactor("pods", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, w, nil
	})

	client := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resultCh := make(chan struct {
		pod string
		err error
	}, 1)
	go func() {
		pod, err := waitForPod(ctx, client, map[string]string{"app": "web"})
		resultCh <- struct {
			pod string
			err error
		}{pod: pod, err: err}
	}()

	w.Add(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-running", Namespace: "dev-ns", Labels: map[string]string{"app": "web"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})

	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		assert.Equal(t, "pod-running", result.pod)
	case <-time.After(2 * time.Second):
		t.Fatal("waitForPod did not return after running pod event")
	}
}

// --- CrashLoopBackOff retry ---

func TestWaitForPod_CrashLoopBackOffRetryThenHealthy(t *testing.T) {
	origFetchLogs := fetchPreviousLogsFn
	origSleep := retrySleep
	t.Cleanup(func() {
		fetchPreviousLogsFn = origFetchLogs
		retrySleep = origSleep
	})

	fetchPreviousLogsFn = func(_ context.Context, _ *client, _ string) string {
		return "Error: something crashed"
	}
	retrySleep = func(_ context.Context) error { return nil }

	listCalls := 0

	crashPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-app", Namespace: "dev-ns", Labels: map[string]string{"app": "web"}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "node", State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				}},
			},
		},
	}

	healthyPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-app", Namespace: "dev-ns", Labels: map[string]string{"app": "web"}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "node", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}

	clientset := fake.NewSimpleClientset(&crashPod)
	clientset.Fake.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listCalls++
		if listCalls >= 3 {
			return true, &corev1.PodList{Items: []corev1.Pod{healthyPod}}, nil
		}
		return true, &corev1.PodList{Items: []corev1.Pod{crashPod}}, nil
	})

	client := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pod, err := waitForPod(ctx, client, map[string]string{"app": "web"})
	require.NoError(t, err)
	assert.Equal(t, "pod-app", pod)
	assert.GreaterOrEqual(t, listCalls, 3)
}

// --- firstRunningPodName ---

func TestFirstRunningPodName_ReturnsHealthyPod(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "c", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		}},
	}
	name, problem := firstRunningPodName(pods)
	assert.Equal(t, "b", name)
	assert.Empty(t, problem)
}

func TestFirstRunningPodName_PrefersHealthyOverProblematic(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "crash"}, Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "c", State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				}},
			},
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "healthy"}, Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "c", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		}},
	}
	name, problem := firstRunningPodName(pods)
	assert.Equal(t, "healthy", name)
	assert.Empty(t, problem)
}

func TestFirstRunningPodName_ReturnsProblematicWhenNoHealthy(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "crash"}, Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "node", State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				}},
			},
		}},
	}
	name, problem := firstRunningPodName(pods)
	assert.Equal(t, "crash", name)
	assert.Contains(t, problem, "CrashLoopBackOff")
}

func TestFirstRunningPodName_EmptyWhenNoRunningPods(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
	}
	name, problem := firstRunningPodName(pods)
	assert.Empty(t, name)
	assert.Empty(t, problem)
}

func TestFirstRunningPodName_SkipsTerminatingPod(t *testing.T) {
	now := metav1.Now()
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "terminating", DeletionTimestamp: &now},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "c", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "alive"},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "c", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			},
		},
	}
	name, problem := firstRunningPodName(pods)
	assert.Equal(t, "alive", name)
	assert.Empty(t, problem)
}

// --- watchPodHealth ---

func TestWatchPodHealth_ReturnsWhenPodDeleted(t *testing.T) {
	origInterval := podHealthPollInterval
	podHealthPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { podHealthPollInterval = origInterval })

	healthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "dev-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}

	getCalls := 0
	clientset := fake.NewSimpleClientset(healthyPod)
	clientset.Fake.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getCalls++
		if getCalls >= 3 {
			return true, nil, errors.New("not found")
		}
		return true, healthyPod.DeepCopy(), nil
	})

	client := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := watchPodHealth(ctx, client, "web-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is gone")
	assert.GreaterOrEqual(t, getCalls, 3)
}

func TestWatchPodHealth_ReturnsWhenPodTerminating(t *testing.T) {
	origInterval := podHealthPollInterval
	podHealthPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { podHealthPollInterval = origInterval })

	now := metav1.Now()
	getCalls := 0

	healthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "dev-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	terminatingPod := healthyPod.DeepCopy()
	terminatingPod.DeletionTimestamp = &now

	clientset := fake.NewSimpleClientset(healthyPod)
	clientset.Fake.PrependReactor("get", "pods", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		getCalls++
		if getCalls >= 3 {
			return true, terminatingPod.DeepCopy(), nil
		}
		return true, healthyPod.DeepCopy(), nil
	})

	client := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := watchPodHealth(ctx, client, "web-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is terminating")
}

func TestWatchPodHealth_ReturnsOnContextCancel(t *testing.T) {
	origInterval := podHealthPollInterval
	podHealthPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { podHealthPollInterval = origInterval })

	healthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "dev-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}

	clientset := fake.NewSimpleClientset(healthyPod)
	client := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := watchPodHealth(ctx, client, "web-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWatchPodHealth_ReturnsWhenPodNotRunning(t *testing.T) {
	origInterval := podHealthPollInterval
	podHealthPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { podHealthPollInterval = origInterval })

	failedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "dev-ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}

	clientset := fake.NewSimpleClientset(failedPod)
	client := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := watchPodHealth(ctx, client, "web-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is no longer running")
}

func TestFirstRunningPodName_EmptyWhenAllTerminating(t *testing.T) {
	now := metav1.Now()
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "gone", DeletionTimestamp: &now},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "c", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			},
		},
	}
	name, problem := firstRunningPodName(pods)
	assert.Empty(t, name)
	assert.Empty(t, problem)
}

func captureWaitLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	orig := waitLog
	waitLog = log.New(buf, "", 0)
	t.Cleanup(func() { waitLog = orig })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func pendingPod(name string, mutate func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-ns", Labels: map[string]string{"app": "web"}},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	mutate(p)
	return p
}

// Image pull and config errors keep the pod Pending; they were only reported
// for Running pods, so a typo in `image:` left the user staring at
// "waiting for pod web..." forever.
func TestWaitForPod_ReportsWhyPendingPodDoesNotStart(t *testing.T) {
	logs := captureWaitLog(t)
	clientset := fake.NewSimpleClientset(pendingPod("web-1", func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: `Back-off pulling image "web:dve"`},
		}}}
	}))
	w := watch.NewFake()
	clientset.Fake.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) { return true, w, nil })
	c := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := waitForPod(ctx, c, map[string]string{"app": "web"})
		result <- err
	}()

	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "ImagePullBackOff") }, 2*time.Second, 10*time.Millisecond,
		"the reason the pod does not start must be shown while waiting")
	assert.Contains(t, logs.String(), `pod "web-1": container "app" is in ImagePullBackOff: Back-off pulling image "web:dve"`)

	w.Modify(pendingPod("web-1", func(p *corev1.Pod) {
		p.Status.Conditions = []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
			Message: "0/3 nodes are available: 3 Insufficient cpu.",
		}}
	}))
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "Insufficient cpu") }, 2*time.Second, 10*time.Millisecond)

	w.Modify(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "dev-ns", Labels: map[string]string{"app": "web"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})
	require.NoError(t, <-result)
}

// The API server closes watches routinely (timeouts after 30-60 minutes,
// restarts); that used to abort the wait with an error.
func TestWaitForPod_RewatchesWhenWatchCloses(t *testing.T) {
	origDelay := rewatchDelay
	rewatchDelay = time.Millisecond
	t.Cleanup(func() { rewatchDelay = origDelay })

	clientset := fake.NewSimpleClientset()
	watches := 0
	first := watch.NewFake()
	clientset.Fake.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		watches++
		if watches == 1 {
			return true, first, nil
		}
		w := watch.NewFake()
		go w.Add(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-2", Namespace: "dev-ns", Labels: map[string]string{"app": "web"}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		})
		return true, w, nil
	})
	c := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type res struct {
		pod string
		err error
	}
	result := make(chan res, 1)
	go func() {
		pod, err := waitForPod(ctx, c, map[string]string{"app": "web"})
		result <- res{pod, err}
	}()
	time.Sleep(50 * time.Millisecond)
	first.Stop()

	r := <-result
	require.NoError(t, r.err)
	assert.Equal(t, "web-2", r.pod)
}

// Port-forward, log streaming, sync and the session all wait for the same
// pod; during a crash loop each of them printed the same crash logs every
// five seconds.
func TestLogContainerProblem_DedupesAcrossWaiters(t *testing.T) {
	resetProblemReports(t)
	logs := captureWaitLog(t)
	origFetch := fetchPreviousLogsFn
	t.Cleanup(func() { fetchPreviousLogsFn = origFetch })
	fetches := 0
	fetchPreviousLogsFn = func(context.Context, *client, string) string {
		fetches++
		return "panic: boom"
	}

	for i := 0; i < 4; i++ {
		logContainerProblem(context.Background(), &client{}, "web-dedupe-1", `container "app" is in CrashLoopBackOff`)
	}
	assert.Equal(t, 1, strings.Count(logs.String(), "panic: boom"))
	assert.Equal(t, 1, fetches)

	logContainerProblem(context.Background(), &client{}, "web-dedupe-1", `container "app" has terminated (Error, exit code 1)`)
	assert.Equal(t, 2, strings.Count(logs.String(), "panic: boom"), "a different problem is reported")
}

func resetProblemReports(t *testing.T) {
	t.Helper()
	problemReportsMu.Lock()
	problemReports = map[string]time.Time{}
	problemReportsMu.Unlock()
	t.Cleanup(func() {
		problemReportsMu.Lock()
		problemReports = map[string]time.Time{}
		problemReportsMu.Unlock()
	})
}
