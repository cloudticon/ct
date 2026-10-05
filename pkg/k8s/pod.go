package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/fatih/color"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
)

// waitLog writes directly to stderr so messages are visible even when the
// global log output is redirected (e.g. terminal mode in ct dev).
var waitLog = log.New(os.Stderr, "", 0)

var (
	fetchPreviousLogsFn = fetchPreviousLogs
	retrySleep          = defaultRetrySleep
)

var problemWaitingReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"CreateContainerConfigError": true,
}

func containerProblem(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && problemWaitingReasons[cs.State.Waiting.Reason] {
			return fmt.Sprintf("container %q is in %s", cs.Name, cs.State.Waiting.Reason)
		}
		if cs.State.Terminated != nil {
			return fmt.Sprintf("container %q has terminated (%s, exit code %d)",
				cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
		}
	}
	return ""
}

// rewatchDelay is the pause before listing again after the API server closed
// the pod watch.
var rewatchDelay = time.Second

// pendingProblemReasons are waiting reasons that keep a pod from ever
// becoming ready without the user changing something (image, config, ...).
var pendingProblemReasons = map[string]bool{
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"CrashLoopBackOff":           true,
}

// pendingProblem explains why a pod that is not running cannot start, or
// returns "" when it is just on its way.
func pendingProblem(pod *corev1.Pod) string {
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for _, cs := range statuses {
			waiting := cs.State.Waiting
			if waiting == nil || !pendingProblemReasons[waiting.Reason] {
				continue
			}
			if waiting.Message != "" {
				return fmt.Sprintf("container %q is in %s: %s", cs.Name, waiting.Reason, waiting.Message)
			}
			return fmt.Sprintf("container %q is in %s", cs.Name, waiting.Reason)
		}
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == corev1.PodReasonUnschedulable {
			return "cannot be scheduled: " + cond.Message
		}
	}
	return ""
}

// waitForPod blocks until a healthy running pod matching selector is available.
// While waiting it reports why pending pods cannot start (image pull errors,
// missing config, unschedulable), which would otherwise look like an endless
// "waiting for pod".
func waitForPod(ctx context.Context, c *client, selector map[string]string) (string, error) {
	if c.CoreV1 == nil {
		return "", errors.New("kubernetes core/v1 client is required")
	}

	labelSelector := labels.Set(selector).String()
	podsClient := c.CoreV1.Pods(c.Namespace)

	reported := map[string]string{}
	reportPending := func(pod *corev1.Pod) {
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodRunning {
			return
		}
		problem := pendingProblem(pod)
		if problem == "" || reported[pod.Name] == problem {
			return
		}
		reported[pod.Name] = problem
		waitLog.Printf("%s pod %q: %s (still waiting)", color.YellowString("[wait]"), pod.Name, problem)
	}

	for {
		list, err := podsClient.List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return "", fmt.Errorf("listing pods for selector %q: %w", labelSelector, err)
		}

		name, problem := firstRunningPodName(list.Items)
		switch {
		case name != "" && problem == "":
			return name, nil
		case name != "":
			logContainerProblem(ctx, c, name, problem)
			if err := retrySleep(ctx); err != nil {
				return "", err
			}
			continue
		}
		for i := range list.Items {
			reportPending(&list.Items[i])
		}

		// No running pods — fall back to watch (original behavior).
		watcher, err := podsClient.Watch(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return "", fmt.Errorf("watching pods for selector %q: %w", labelSelector, err)
		}

		if name, err := waitForRunningPod(ctx, watcher, reportPending); err != nil || name != "" {
			return name, err
		}
		// The API server closed the watch (timeouts after 30-60 minutes,
		// restarts): list again instead of failing.
		if !sleepContext(ctx, rewatchDelay) {
			return "", ctx.Err()
		}
	}
}

// waitForRunningPod consumes watch events until a pod is running. It returns
// ("", nil) when the watch was closed by the server.
func waitForRunningPod(ctx context.Context, watcher watch.Interface, reportPending func(*corev1.Pod)) (string, error) {
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return "", nil
			}
			pod, ok := event.Object.(*corev1.Pod)
			if !ok || pod == nil || pod.DeletionTimestamp != nil {
				continue
			}
			if pod.Status.Phase == corev1.PodRunning {
				return pod.Name, nil
			}
			reportPending(pod)
		}
	}
}

// firstRunningPodName returns the first healthy running pod. If all running
// pods have container problems, it returns the first problematic pod's name
// and the problem description. If no running pods exist, both values are empty.
func firstRunningPodName(pods []corev1.Pod) (name, problem string) {
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if p := containerProblem(pod); p != "" {
			if name == "" {
				name = pod.Name
				problem = p
			}
			continue
		}
		return pod.Name, ""
	}
	return name, problem
}

// defaultContainerAnnotation names the container kubectl uses when a command
// does not specify one. ct dev sets it on patched workloads; service meshes
// set it when they inject a sidecar.
const defaultContainerAnnotation = "kubectl.kubernetes.io/default-container"

// resolveContainer returns container when it is set. Otherwise it returns
// the pod's default container the way kubectl resolves it, because the API
// server rejects exec and log requests without a container name for pods
// with more than one container (e.g. an injected sidecar). It returns "" when
// the pod cannot be read, leaving the choice to the API server.
func resolveContainer(ctx context.Context, c *client, podName, container string) string {
	if container != "" || c == nil || c.CoreV1 == nil {
		return container
	}
	pod, err := c.CoreV1.Pods(c.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return defaultContainerName(pod)
}

// defaultContainerName returns the container named by the default-container
// annotation when it exists in the pod, else the first container.
func defaultContainerName(pod *corev1.Pod) string {
	if name := pod.Annotations[defaultContainerAnnotation]; name != "" {
		for _, c := range pod.Spec.Containers {
			if c.Name == name {
				return name
			}
		}
	}
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Name
	}
	return ""
}

// problemReports remembers when a pod problem was last printed. Several
// features wait for the same pod concurrently (session, port-forward, logs,
// sync); without this each printed the same crash logs every retry.
var (
	problemReportsMu    sync.Mutex
	problemReports      = map[string]time.Time{}
	problemReportsEvery = 30 * time.Second
)

func shouldReportProblem(podName, problem string) bool {
	problemReportsMu.Lock()
	defer problemReportsMu.Unlock()
	key := podName + "\x00" + problem
	if last, ok := problemReports[key]; ok && time.Since(last) < problemReportsEvery {
		return false
	}
	problemReports[key] = time.Now()
	return true
}

func logContainerProblem(ctx context.Context, c *client, podName, problem string) {
	if !shouldReportProblem(podName, problem) {
		return
	}
	waitLog.Printf("%s pod %q: %s, fetching crash logs...", color.YellowString("[wait]"), podName, problem)
	if logs := fetchPreviousLogsFn(ctx, c, podName); logs != "" {
		waitLog.Printf("%s previous logs for %q:\n%s", color.YellowString("[wait]"), podName, logs)
	}
	waitLog.Printf("%s retrying in 5s...", color.YellowString("[wait]"))
}

func fetchPreviousLogs(ctx context.Context, c *client, podName string) string {
	tailLines := int64(20)
	req := c.CoreV1.Pods(c.Namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: resolveContainer(ctx, c, podName, ""),
		Previous:  true,
		TailLines: &tailLines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return ""
	}
	return string(data)
}

var podHealthPollInterval = 2 * time.Second

// watchPodHealth polls a specific pod's status at short intervals and returns
// an error as soon as the pod is terminating, no longer running, or gone. This
// allows the caller to react to pod deletion faster than waiting for a broken
// exec/port-forward TCP connection to time out.
func watchPodHealth(ctx context.Context, c *client, podName string) error {
	if c.CoreV1 == nil {
		return errors.New("kubernetes core/v1 client is required")
	}

	podsClient := c.CoreV1.Pods(c.Namespace)

	ticker := time.NewTicker(podHealthPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pod, err := podsClient.Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("pod %q is gone: %w", podName, err)
			}
			if pod.DeletionTimestamp != nil {
				return fmt.Errorf("pod %q is terminating", podName)
			}
			if pod.Status.Phase != corev1.PodRunning {
				return fmt.Errorf("pod %q is no longer running (phase: %s)", podName, pod.Status.Phase)
			}
			if p := containerProblem(pod); p != "" {
				return fmt.Errorf("pod %q: %s", podName, p)
			}
		}
	}
}

func defaultRetrySleep(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return nil
	}
}
