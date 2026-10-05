package k8s

import (
	"context"
	"errors"
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
