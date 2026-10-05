package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newReleaseTestClient(t *testing.T) (*client, *dynamicfake.FakeDynamicClient) {
	t.Helper()

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClient(scheme)

	dynClient.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction := action.(k8stesting.PatchAction)
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(patchAction.GetPatch(), &obj.Object); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})

	dynClient.PrependReactor("delete", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})

	fakeClientset := fake.NewSimpleClientset()
	fakeClientset.Fake.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
				{Name: "services", Kind: "Service", Namespaced: true},
			},
		},
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", Kind: "Deployment", Namespaced: true},
			},
		},
	}

	fakeClientset.PrependReactor("patch", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction, ok := action.(k8stesting.PatchAction)
		if !ok {
			return false, nil, nil
		}
		var cm corev1.ConfigMap
		if err := json.Unmarshal(patchAction.GetPatch(), &cm); err != nil {
			return true, nil, err
		}
		if cm.Name == "" {
			cm.Name = patchAction.GetName()
		}
		if cm.Namespace == "" {
			cm.Namespace = patchAction.GetNamespace()
		}

		gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
		_, err := fakeClientset.Tracker().Get(gvr, cm.Namespace, cm.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				_ = fakeClientset.Tracker().Create(gvr, &cm, cm.Namespace)
				return true, &cm, nil
			}
			return true, nil, err
		}
		_ = fakeClientset.Tracker().Update(gvr, &cm, cm.Namespace)
		return true, &cm, nil
	})

	c := &client{
		CoreV1:    fakeClientset.CoreV1(),
		Discovery: fakeClientset.Discovery(),
		Dynamic:   dynClient,
		Namespace: "default",
		gvrCache:  make(map[string]*resourceInfo),
	}

	return c, dynClient
}

func seedInventory(t *testing.T, c *client, namespace, releaseName string, refs []ResourceRef) {
	t.Helper()
	refsJSON, err := json.Marshal(refs)
	require.NoError(t, err)
	_, err = c.CoreV1.ConfigMaps(namespace).Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      inventoryConfigMapName(releaseName),
			Namespace: namespace,
		},
		Data: map[string]string{"resources": string(refsJSON)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

func TestApplyRelease_HappyPath(t *testing.T) {
	c, dynClient := newReleaseTestClient(t)
	c.Namespace = "test-ns"

	resources := []Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "web", "namespace": "test-ns"},
		},
	}

	err := c.applyRelease(context.Background(), "test-ns", "my-release", resources)
	require.NoError(t, err)

	var patchCount, deleteCount int
	for _, action := range dynClient.Actions() {
		switch action.GetVerb() {
		case "patch":
			patchCount++
		case "delete":
			deleteCount++
		}
	}
	assert.Equal(t, 1, patchCount, "should apply 1 resource")
	assert.Equal(t, 0, deleteCount, "should not delete any resources when no orphans")

	cm, err := c.CoreV1.ConfigMaps("test-ns").Get(context.Background(), "ct-inventory-my-release", metav1.GetOptions{})
	require.NoError(t, err)
	require.Contains(t, cm.Data, "resources")

	var refs []ResourceRef
	require.NoError(t, json.Unmarshal([]byte(cm.Data["resources"]), &refs))
	assert.Equal(t, []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "test-ns"},
	}, refs)
}

func TestApplyRelease_PrunesOrphans(t *testing.T) {
	c, dynClient := newReleaseTestClient(t)
	c.Namespace = "test-ns"

	seedInventory(t, c, "test-ns", "my-release", []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "test-ns"},
		{APIVersion: "v1", Kind: "Service", Name: "old-svc", Namespace: "test-ns"},
	})

	resources := []Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "web", "namespace": "test-ns"},
		},
	}

	err := c.applyRelease(context.Background(), "test-ns", "my-release", resources)
	require.NoError(t, err)

	var deletedNames []string
	for _, action := range dynClient.Actions() {
		if action.GetVerb() == "delete" {
			deletedNames = append(deletedNames, action.(k8stesting.DeleteAction).GetName())
		}
	}
	assert.Equal(t, []string{"old-svc"}, deletedNames)
}

func TestApplyRelease_NoOrphans_SameResources(t *testing.T) {
	c, dynClient := newReleaseTestClient(t)
	c.Namespace = "test-ns"

	seedInventory(t, c, "test-ns", "my-release", []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "test-ns"},
	})

	resources := []Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "web", "namespace": "test-ns"},
		},
	}

	err := c.applyRelease(context.Background(), "test-ns", "my-release", resources)
	require.NoError(t, err)

	var deleteCount int
	for _, action := range dynClient.Actions() {
		if action.GetVerb() == "delete" {
			deleteCount++
		}
	}
	assert.Equal(t, 0, deleteCount, "should not delete when resources match")
}

func TestApplyRelease_ApplyError(t *testing.T) {
	c, dynClient := newReleaseTestClient(t)
	c.Namespace = "test-ns"

	dynClient.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apply boom")
	})

	resources := []Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "web", "namespace": "test-ns"},
		},
	}

	err := c.applyRelease(context.Background(), "test-ns", "my-release", resources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applying resources")
}

func TestApplyRelease_LoadInventoryError(t *testing.T) {
	c, _ := newReleaseTestClient(t)

	err := c.applyRelease(context.Background(), "test-ns", "", []Resource{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading inventory")
}

func TestApplyRelease_InvalidResourceRef(t *testing.T) {
	c, _ := newReleaseTestClient(t)
	c.Namespace = "test-ns"

	resources := []Resource{
		{"kind": "ConfigMap"},
	}

	err := c.applyRelease(context.Background(), "test-ns", "my-release", resources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "building resource refs")
}

func TestApplyRelease_EmptyResources(t *testing.T) {
	c, _ := newReleaseTestClient(t)
	c.Namespace = "test-ns"

	err := c.applyRelease(context.Background(), "test-ns", "my-release", []Resource{})
	require.NoError(t, err)
}

func TestApplyRelease_FailedApplyKeepsNewObjectsTracked(t *testing.T) {
	c, dynClient := newReleaseTestClient(t)
	c.Namespace = "test-ns"
	seedInventory(t, c, "test-ns", "my-release", []ResourceRef{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "cfg", Namespace: "test-ns"},
	})
	// The ConfigMap applies, the Deployment (applied after it) fails.
	dynClient.PrependReactor("patch", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apply boom")
	})

	resources := []Resource{
		{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]interface{}{"name": "web", "namespace": "test-ns"}},
		{"apiVersion": "v1", "kind": "Service", "metadata": map[string]interface{}{"name": "web", "namespace": "test-ns"}},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "cfg", "namespace": "test-ns"}},
	}

	err := c.applyRelease(context.Background(), "test-ns", "my-release", resources)
	require.Error(t, err)

	refs, err := loadInventory(context.Background(), c, "test-ns", "my-release")
	require.NoError(t, err)
	assert.ElementsMatch(t, []ResourceRef{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "cfg", Namespace: "test-ns"},
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "test-ns"},
		{APIVersion: "v1", Kind: "Service", Name: "web", Namespace: "test-ns"},
	}, refs, "the Service was created before the failure and must stay prunable")
}

func TestApplyRelease_PrunesObjectsFromAFailedRun(t *testing.T) {
	c, dynClient := newReleaseTestClient(t)
	c.Namespace = "test-ns"
	seedInventory(t, c, "test-ns", "my-release", []ResourceRef{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "cfg", Namespace: "test-ns"},
		{APIVersion: "v1", Kind: "Service", Name: "leftover", Namespace: "test-ns"},
	})

	resources := []Resource{
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "cfg", "namespace": "test-ns"}},
	}
	require.NoError(t, c.applyRelease(context.Background(), "test-ns", "my-release", resources))

	var deleted []string
	for _, a := range dynClient.Actions() {
		if d, ok := a.(k8stesting.DeleteAction); ok {
			deleted = append(deleted, d.GetName())
		}
	}
	assert.Equal(t, []string{"leftover"}, deleted)
}
