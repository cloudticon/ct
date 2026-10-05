package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectReleaseLabels_AddsRequiredLabels(t *testing.T) {
	input := []Resource{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "web",
				"namespace": "prod",
			},
		},
	}

	got := InjectReleaseLabels(input, "my-release")

	require.Len(t, got, 1)
	metadata, ok := got[0]["metadata"].(map[string]interface{})
	require.True(t, ok)
	labels, ok := metadata["labels"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "ct", labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "my-release", labels["ct.cloudticon.com/instance"])
}

func TestInjectReleaseLabels_DoesNotOverrideExistingLabels(t *testing.T) {
	input := []Resource{
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name": "cfg",
				"labels": map[string]interface{}{
					"custom":                       "value",
					"app.kubernetes.io/managed-by": "helm",
					"ct.cloudticon.com/instance":   "existing-release",
				},
			},
		},
	}

	got := InjectReleaseLabels(input, "new-release")

	require.Len(t, got, 1)
	metadata, ok := got[0]["metadata"].(map[string]interface{})
	require.True(t, ok)
	labels, ok := metadata["labels"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "value", labels["custom"])
	assert.Equal(t, "helm", labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "existing-release", labels["ct.cloudticon.com/instance"])
}

func TestInjectReleaseLabels_DoesNotMutateInput(t *testing.T) {
	labels := map[string]interface{}{
		"custom": "value",
	}
	metadata := map[string]interface{}{
		"name":   "svc",
		"labels": labels,
	}
	input := []Resource{
		{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata":   metadata,
		},
	}

	_ = InjectReleaseLabels(input, "rel")

	_, hasManagedBy := labels["app.kubernetes.io/managed-by"]
	_, hasInstance := labels["ct.cloudticon.com/instance"]
	assert.False(t, hasManagedBy)
	assert.False(t, hasInstance)

	_, metadataHasLabels := metadata["labels"].(map[string]interface{})
	assert.True(t, metadataHasLabels)
}

func TestInjectReleaseLabels_EmptyInput(t *testing.T) {
	got := InjectReleaseLabels(nil, "release")
	require.Empty(t, got)
}
