package output_test

import (
	"testing"

	"github.com/cloudticon/ct/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSerialize_YAMLSingleResource(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "web-app",
				"namespace": "production",
			},
			"spec": map[string]interface{}{
				"replicas": 3,
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.Contains(t, result, "apiVersion: apps/v1")
	assert.Contains(t, result, "kind: Deployment")
	assert.Contains(t, result, "name: web-app")
	assert.Contains(t, result, "namespace: production")
	assert.Contains(t, result, "replicas: 3")
	assert.NotContains(t, result, "---")
}

func TestSerialize_YAMLMultiDocument(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name": "app",
			},
		},
		{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name": "app-svc",
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.Contains(t, result, "---")
	assert.Contains(t, result, "kind: Deployment")
	assert.Contains(t, result, "kind: Service")
}

func TestSerialize_YAMLDefaultFormat(t *testing.T) {
	resources := []output.Resource{
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "cfg"}},
	}

	result, err := output.Serialize(resources, "")

	require.NoError(t, err)
	assert.Contains(t, result, "kind: ConfigMap")
}

func TestSerialize_JSONSingleResource(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name": "svc",
			},
		},
	}

	result, err := output.Serialize(resources, "json")

	require.NoError(t, err)
	assert.Contains(t, result, `"apiVersion": "v1"`)
	assert.Contains(t, result, `"kind": "Service"`)
	assert.Contains(t, result, `"name": "svc"`)
}

func TestSerialize_JSONMultipleResources(t *testing.T) {
	resources := []output.Resource{
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "a"}},
		{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]interface{}{"name": "b"}},
	}

	result, err := output.Serialize(resources, "json")

	require.NoError(t, err)
	assert.Contains(t, result, `"kind": "ConfigMap"`)
	assert.Contains(t, result, `"kind": "Secret"`)
}

func TestSerialize_UnsupportedFormat(t *testing.T) {
	_, err := output.Serialize([]output.Resource{}, "xml")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported output format")
}

func TestSerialize_CleansNilFields(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":        "app",
				"annotations": nil,
			},
			"spec": map[string]interface{}{
				"replicas": 1,
				"strategy": nil,
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.NotContains(t, result, "annotations")
	assert.NotContains(t, result, "strategy")
	assert.Contains(t, result, "name: app")
	assert.Contains(t, result, "replicas: 1")
}

func TestSerialize_CleansNilInNestedMaps(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name": "cfg",
				"labels": map[string]interface{}{
					"app":     "test",
					"version": nil,
				},
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.Contains(t, result, "app: test")
	assert.NotContains(t, result, "version")
}

func TestSerialize_CleansNilInArrays(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata":   map[string]interface{}{"name": "p"},
			"spec": map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{
						"name":    "app",
						"image":   "nginx",
						"command": nil,
					},
				},
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.Contains(t, result, "name: app")
	assert.Contains(t, result, "image: nginx")
	assert.NotContains(t, result, "command")
}

func TestSerialize_RemovesEmptyMapsAfterCleaning(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name": "svc",
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"removed": nil,
				},
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.NotContains(t, result, "selector")
	assert.NotContains(t, result, "spec")
}

func TestSerialize_EmptyResourceList(t *testing.T) {
	result, err := output.Serialize([]output.Resource{}, "yaml")

	require.NoError(t, err)
	assert.Equal(t, "\n", result)
}

func TestSerialize_PreservesZeroValues(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]interface{}{"name": "cfg"},
			"data": map[string]interface{}{
				"count":   0,
				"enabled": false,
				"label":   "",
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.Contains(t, result, "count: 0")
	assert.Contains(t, result, "enabled: false")
	assert.Contains(t, result, `label: ""`)
}

func TestSerialize_KeepsAuthoredEmptyObjects(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata":   map[string]interface{}{"name": "p"},
			"spec": map[string]interface{}{
				"volumes": []interface{}{
					map[string]interface{}{"name": "tmp", "emptyDir": map[string]interface{}{}},
				},
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.Contains(t, result, "emptyDir: {}")
}

func TestSerialize_EmptyResourceDoesNotPanic(t *testing.T) {
	result, err := output.Serialize([]output.Resource{{"metadata": map[string]interface{}{"labels": nil}}}, "yaml")

	require.NoError(t, err)
	assert.Equal(t, "{}\n", result)
}

func TestSerialize_YAMLTwoSpaceIndent(t *testing.T) {
	resources := []output.Resource{
		{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata":   map[string]interface{}{"name": "svc"},
			"spec": map[string]interface{}{
				"ports": []interface{}{map[string]interface{}{"port": 80}},
			},
		},
	}

	result, err := output.Serialize(resources, "yaml")

	require.NoError(t, err)
	assert.Equal(t, "apiVersion: v1\nkind: Service\nmetadata:\n  name: svc\nspec:\n  ports:\n    - port: 80\n", result)
}

func TestSerialize_JSONEmptyListIsArray(t *testing.T) {
	result, err := output.Serialize(nil, "json")

	require.NoError(t, err)
	assert.Equal(t, "[]\n", result)
}
