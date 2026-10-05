package k8s

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newInventoryTestClient(t *testing.T) (*client, *fake.Clientset) {
	t.Helper()

	fakeClient := fake.NewSimpleClientset()
	fakeClient.PrependReactor("patch", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction, ok := action.(k8stesting.PatchAction)
		require.True(t, ok)
		require.Equal(t, types.ApplyPatchType, patchAction.GetPatchType())

		var cm corev1.ConfigMap
		require.NoError(t, json.Unmarshal(patchAction.GetPatch(), &cm))
		if cm.Name == "" {
			cm.Name = patchAction.GetName()
		}
		if cm.Namespace == "" {
			cm.Namespace = patchAction.GetNamespace()
		}

		gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
		_, err := fakeClient.Tracker().Get(gvr, cm.Namespace, cm.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				require.NoError(t, fakeClient.Tracker().Create(gvr, &cm, cm.Namespace))
				return true, &cm, nil
			}
			return true, nil, err
		}

		require.NoError(t, fakeClient.Tracker().Update(gvr, &cm, cm.Namespace))
		return true, &cm, nil
	})

	return &client{
		CoreV1:    fakeClient.CoreV1(),
		Namespace: "default",
	}, fakeClient
}

func TestSaveInventory(t *testing.T) {
	client, fakeClient := newInventoryTestClient(t)
	ctx := context.Background()

	resources := []Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "web",
				"namespace": "prod",
			},
		},
		{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name":      "web-svc",
				"namespace": "prod",
			},
		},
	}

	err := saveInventory(ctx, client, "prod", "my-release", resources)
	require.NoError(t, err)

	actions := fakeClient.Actions()
	require.Len(t, actions, 1)
	assert.Equal(t, "patch", actions[0].GetVerb())
	assert.Equal(t, "configmaps", actions[0].GetResource().Resource)
	assert.Equal(t, "prod", actions[0].GetNamespace())

	patchAction := actions[0].(k8stesting.PatchAction)
	assert.Equal(t, types.ApplyPatchType, patchAction.GetPatchType())

	var patchObj map[string]interface{}
	require.NoError(t, json.Unmarshal(patchAction.GetPatch(), &patchObj))

	metadata := patchObj["metadata"].(map[string]interface{})
	assert.Equal(t, "ct-inventory-my-release", metadata["name"])
	labels := metadata["labels"].(map[string]interface{})
	assert.Equal(t, "ct", labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "my-release", labels["ct.cloudticon.com/instance"])

	cm, err := client.CoreV1.ConfigMaps("prod").Get(ctx, "ct-inventory-my-release", metav1.GetOptions{})
	require.NoError(t, err)
	require.Contains(t, cm.Data, "resources")

	var refs []ResourceRef
	require.NoError(t, json.Unmarshal([]byte(cm.Data["resources"]), &refs))
	assert.Equal(t, []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "prod"},
		{APIVersion: "v1", Kind: "Service", Name: "web-svc", Namespace: "prod"},
	}, refs)
}

func TestSaveInventory_UsesClientNamespaceWhenEmptyArg(t *testing.T) {
	client, _ := newInventoryTestClient(t)
	client.Namespace = "fallback-ns"

	err := saveInventory(context.Background(), client, "", "dev", []Resource{
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name": "cfg",
			},
		},
	})
	require.NoError(t, err)

	_, err = client.CoreV1.ConfigMaps("fallback-ns").Get(context.Background(), "ct-inventory-dev", metav1.GetOptions{})
	require.NoError(t, err)
}

func TestLoadInventory(t *testing.T) {
	client, _ := newInventoryTestClient(t)
	ctx := context.Background()

	payload := `[{"apiVersion":"apps/v1","kind":"Deployment","name":"web","namespace":"prod"}]`
	_, err := client.CoreV1.ConfigMaps("prod").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ct-inventory-my-release",
			Namespace: "prod",
		},
		Data: map[string]string{
			"resources": payload,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	refs, err := loadInventory(ctx, client, "prod", "my-release")
	require.NoError(t, err)
	assert.Equal(t, []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "prod"},
	}, refs)
}

func TestLoadInventory_NotFoundReturnsEmpty(t *testing.T) {
	client, _ := newInventoryTestClient(t)

	refs, err := loadInventory(context.Background(), client, "prod", "missing")
	require.NoError(t, err)
	assert.Empty(t, refs)
}

func TestDeleteInventory(t *testing.T) {
	client, _ := newInventoryTestClient(t)
	ctx := context.Background()

	_, err := client.CoreV1.ConfigMaps("prod").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ct-inventory-my-release",
			Namespace: "prod",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.NoError(t, deleteInventory(ctx, client, "prod", "my-release"))

	_, err = client.CoreV1.ConfigMaps("prod").Get(ctx, "ct-inventory-my-release", metav1.GetOptions{})
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestDeleteInventory_IgnoresNotFound(t *testing.T) {
	client, _ := newInventoryTestClient(t)
	require.NoError(t, deleteInventory(context.Background(), client, "prod", "missing"))
}

func TestListReleases_Namespace(t *testing.T) {
	client, _ := newInventoryTestClient(t)
	ctx := context.Background()

	_, err := client.CoreV1.ConfigMaps("prod").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ct-inventory-my-release",
			Namespace: "prod",
			Labels: map[string]string{
				managedByLabelKey: "ct",
				instanceLabelKey:  "my-release",
			},
		},
		Data: map[string]string{
			inventoryDataKey: `[{"kind":"Deployment"},{"kind":"Service"}]`,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = client.CoreV1.ConfigMaps("prod").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ignored-no-label",
			Namespace: "prod",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = client.CoreV1.ConfigMaps("prod").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ignored-no-instance",
			Namespace: "prod",
			Labels: map[string]string{
				managedByLabelKey: "ct",
			},
		},
		Data: map[string]string{
			inventoryDataKey: `[]`,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	// A ConfigMap the release itself renders carries the same labels as the
	// inventory but is not one.
	_, err = client.CoreV1.ConfigMaps("prod").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "prod",
			Labels: map[string]string{
				managedByLabelKey: "ct",
				instanceLabelKey:  "my-release",
			},
		},
		Data: map[string]string{"LOG_LEVEL": "info"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	releases, err := listReleases(ctx, client, "prod", false)
	require.NoError(t, err)
	assert.Equal(t, []ReleaseInfo{
		{Name: "my-release", Namespace: "prod", Resources: 2},
	}, releases)
}

func TestListReleases_AllNamespaces(t *testing.T) {
	client, _ := newInventoryTestClient(t)
	ctx := context.Background()

	_, err := client.CoreV1.ConfigMaps("staging").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ct-inventory-backend",
			Namespace: "staging",
			Labels: map[string]string{
				managedByLabelKey: "ct",
				instanceLabelKey:  "backend",
			},
		},
		Data: map[string]string{
			inventoryDataKey: `[{"kind":"Deployment"}]`,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = client.CoreV1.ConfigMaps("prod").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ct-inventory-api",
			Namespace: "prod",
			Labels: map[string]string{
				managedByLabelKey: "ct",
				instanceLabelKey:  "api",
			},
		},
		Data: map[string]string{
			inventoryDataKey: `[{"kind":"Deployment"},{"kind":"Service"},{"kind":"ConfigMap"}]`,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	releases, err := listReleases(ctx, client, "", true)
	require.NoError(t, err)
	assert.Equal(t, []ReleaseInfo{
		{Name: "api", Namespace: "prod", Resources: 3},
		{Name: "backend", Namespace: "staging", Resources: 1},
	}, releases)
}

func TestListReleases_ReturnsErrorOnInvalidResourcesJSON(t *testing.T) {
	client, _ := newInventoryTestClient(t)
	ctx := context.Background()

	_, err := client.CoreV1.ConfigMaps("prod").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ct-inventory-broken",
			Namespace: "prod",
			Labels: map[string]string{
				managedByLabelKey: "ct",
				instanceLabelKey:  "broken",
			},
		},
		Data: map[string]string{
			inventoryDataKey: `{`,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = listReleases(ctx, client, "prod", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshaling inventory resources")
}

func TestResourcesToRefs_Validation(t *testing.T) {
	_, err := resourcesToRefs([]Resource{
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]interface{}{},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata.name")
}
