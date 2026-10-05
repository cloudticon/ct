package engine_test

import (
	"testing"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecute_EmptyScript(t *testing.T) {
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode: "",
	})
	require.NoError(t, err)
	assert.Empty(t, resources)
}

func TestExecute_SingleResource(t *testing.T) {
	js := `
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "apps/v1",
			kind: "Deployment",
			metadata: { name: "test-app" },
			spec: { replicas: 3 },
		});
	`
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    js,
		Namespace: "production",
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	res := resources[0]
	assert.Equal(t, "apps/v1", res["apiVersion"])
	assert.Equal(t, "Deployment", res["kind"])
	assert.Nil(t, res["__ctts_scope"])

	meta := res["metadata"].(map[string]interface{})
	assert.Equal(t, "test-app", meta["name"])
	assert.Equal(t, "production", meta["namespace"])
}

func TestExecute_ClusterScopedNoNamespace(t *testing.T) {
	js := `
		globalThis.__ct_resources.push({
			__ctts_scope: "cluster",
			apiVersion: "v1",
			kind: "Namespace",
			metadata: { name: "production" },
		});
	`
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    js,
		Namespace: "default",
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	meta := resources[0]["metadata"].(map[string]interface{})
	_, hasNamespace := meta["namespace"]
	assert.False(t, hasNamespace, "cluster-scoped resource should not get namespace")
}

func TestExecute_ExplicitNamespaceNotOverridden(t *testing.T) {
	js := `
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "v1",
			kind: "Service",
			metadata: { name: "svc", namespace: "custom" },
		});
	`
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    js,
		Namespace: "production",
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	meta := resources[0]["metadata"].(map[string]interface{})
	assert.Equal(t, "custom", meta["namespace"])
}

func TestExecute_NilFieldsCleaned(t *testing.T) {
	js := `
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "v1",
			kind: "Service",
			metadata: { name: "svc", labels: null, annotations: null },
			spec: { clusterIP: null },
		});
	`
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    js,
		Namespace: "default",
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	meta := resources[0]["metadata"].(map[string]interface{})
	_, hasLabels := meta["labels"]
	assert.False(t, hasLabels, "nil labels should be removed")

	_, hasAnnotations := meta["annotations"]
	assert.False(t, hasAnnotations, "nil annotations should be removed")
}

func TestExecute_ValuesInjected(t *testing.T) {
	js := `
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "v1",
			kind: "ConfigMap",
			metadata: { name: "cfg" },
			data: { image: globalThis.Values.image },
		});
	`
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode: js,
		Values: map[string]interface{}{
			"image": "nginx:1.25",
		},
		Namespace: "test",
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	data := resources[0]["data"].(map[string]interface{})
	assert.Equal(t, "nginx:1.25", data["image"])
}

func TestExecute_ReleaseInjected(t *testing.T) {
	js := `
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "v1",
			kind: "ConfigMap",
			metadata: { name: globalThis.Release.name },
			data: { namespace: globalThis.Release.namespace },
		});
	`
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:      js,
		Namespace:   "production",
		ReleaseName: "my-release",
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	meta := resources[0]["metadata"].(map[string]interface{})
	assert.Equal(t, "my-release", meta["name"])

	data := resources[0]["data"].(map[string]interface{})
	assert.Equal(t, "production", data["namespace"])
}

func TestExecute_MultipleResources_OrderPreserved(t *testing.T) {
	js := `
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "apps/v1", kind: "Deployment",
			metadata: { name: "first" },
		});
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "v1", kind: "Service",
			metadata: { name: "second" },
		});
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "networking.k8s.io/v1", kind: "Ingress",
			metadata: { name: "third" },
		});
	`
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    js,
		Namespace: "ns",
	})
	require.NoError(t, err)
	require.Len(t, resources, 3)

	meta0 := resources[0]["metadata"].(map[string]interface{})
	meta1 := resources[1]["metadata"].(map[string]interface{})
	meta2 := resources[2]["metadata"].(map[string]interface{})
	assert.Equal(t, "first", meta0["name"])
	assert.Equal(t, "second", meta1["name"])
	assert.Equal(t, "third", meta2["name"])
}

func TestExecute_JSError(t *testing.T) {
	_, err := engine.Execute(engine.ExecuteOpts{
		JSCode: `throw new Error("boom");`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestExecute_NoNamespaceFlag(t *testing.T) {
	js := `
		globalThis.__ct_resources.push({
			__ctts_scope: "namespaced",
			apiVersion: "v1",
			kind: "Service",
			metadata: { name: "svc" },
		});
	`
	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode: js,
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	meta := resources[0]["metadata"].(map[string]interface{})
	_, hasNamespace := meta["namespace"]
	assert.False(t, hasNamespace, "no namespace flag means no namespace added")
}
