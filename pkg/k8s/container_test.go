package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func sidecarPod(annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "dev-ns", Annotations: annotations},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "istio-proxy"},
			{Name: "app"},
		}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestDefaultContainerName(t *testing.T) {
	assert.Equal(t, "app", defaultContainerName(sidecarPod(map[string]string{
		"kubectl.kubernetes.io/default-container": "app",
	})))
	assert.Equal(t, "istio-proxy", defaultContainerName(sidecarPod(nil)), "first container without annotation")
	assert.Equal(t, "istio-proxy", defaultContainerName(sidecarPod(map[string]string{
		"kubectl.kubernetes.io/default-container": "missing",
	})), "stale annotation falls back to the first container")
	assert.Equal(t, "", defaultContainerName(&corev1.Pod{}))
}

// The API server rejects exec without a container for multi-container pods;
// the live adapter must pick the default container like kubectl does.
func TestLiveExecPod_DefaultsContainerForMultiContainerPod(t *testing.T) {
	origRunner := execStreamRunnerFn
	t.Cleanup(func() { execStreamRunnerFn = origRunner })

	var got execStreamOpts
	execStreamRunnerFn = func(_ context.Context, _ *client, _ string, _ []string, opts execStreamOpts) error {
		got = opts
		return nil
	}

	clientset := fake.NewSimpleClientset(sidecarPod(map[string]string{
		"kubectl.kubernetes.io/default-container": "app",
	}))
	lc := &liveCluster{client: newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")}

	require.NoError(t, lc.ExecPod(context.Background(), "dev-ns", "web-1", ExecOpts{Command: []string{"ls"}}))
	assert.Equal(t, "app", got.Container)

	require.NoError(t, lc.ExecPod(context.Background(), "dev-ns", "web-1", ExecOpts{Command: []string{"ls"}, Container: "istio-proxy"}))
	assert.Equal(t, "istio-proxy", got.Container, "an explicit container wins")
}

func TestStreamPodLogs_UsesDefaultContainer(t *testing.T) {
	clientset := fake.NewSimpleClientset(sidecarPod(map[string]string{
		"kubectl.kubernetes.io/default-container": "app",
	}))
	c := newClientFromInterfaces(clientset.CoreV1(), clientset.Discovery(), nil, "dev-ns")

	stream, err := streamPodLogs(context.Background(), c, "web-1")
	require.NoError(t, err)
	_ = stream.Close()

	opts := lastLogOptions(t, clientset)
	assert.Equal(t, "app", opts.Container)
	assert.True(t, opts.Follow)
}

func lastLogOptions(t *testing.T, clientset *fake.Clientset) *corev1.PodLogOptions {
	t.Helper()
	var opts *corev1.PodLogOptions
	for _, action := range clientset.Actions() {
		if action.GetSubresource() != "log" {
			continue
		}
		generic, ok := action.(k8stesting.GenericAction)
		require.True(t, ok)
		opts, _ = generic.GetValue().(*corev1.PodLogOptions)
	}
	require.NotNil(t, opts, "no logs request recorded")
	return opts
}
