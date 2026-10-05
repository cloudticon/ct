package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComputeOrphaned_EmptyOldRefs(t *testing.T) {
	orphaned := computeOrphaned(nil, []ResourceRef{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
	}, "prod")

	assert.Empty(t, orphaned)
}

func TestComputeOrphaned_NoOrphans(t *testing.T) {
	oldRefs := []ResourceRef{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "prod", Name: "web"},
	}
	newRefs := []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "prod", Name: "web"},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
	}

	orphaned := computeOrphaned(oldRefs, newRefs, "prod")
	assert.Empty(t, orphaned)
}

func TestComputeOrphaned_ReturnsRemovedResources(t *testing.T) {
	oldRefs := []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "prod", Name: "web"},
		{APIVersion: "v1", Kind: "Service", Namespace: "prod", Name: "web-svc"},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
	}
	newRefs := []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "prod", Name: "web"},
	}

	orphaned := computeOrphaned(oldRefs, newRefs, "prod")
	assert.Equal(t, []ResourceRef{
		{APIVersion: "v1", Kind: "Service", Namespace: "prod", Name: "web-svc"},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
	}, orphaned)
}

func TestComputeOrphaned_SupportsClusterScopedResources(t *testing.T) {
	oldRefs := []ResourceRef{
		{APIVersion: "v1", Kind: "Namespace", Name: "prod"},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
	}
	newRefs := []ResourceRef{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
	}

	orphaned := computeOrphaned(oldRefs, newRefs, "prod")
	assert.Equal(t, []ResourceRef{
		{APIVersion: "v1", Kind: "Namespace", Name: "prod"},
	}, orphaned)
}

func TestComputeOrphaned_DeduplicatesOldRefs(t *testing.T) {
	oldRefs := []ResourceRef{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
	}

	orphaned := computeOrphaned(oldRefs, nil, "prod")
	assert.Equal(t, []ResourceRef{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
	}, orphaned)
}

func TestComputeOrphaned_SameObjectUnderAnotherVersionOrDefaultedNamespace(t *testing.T) {
	oldRefs := []ResourceRef{
		{APIVersion: "autoscaling/v1", Kind: "HorizontalPodAutoscaler", Namespace: "prod", Name: "web"},
		{APIVersion: "v1", Kind: "ConfigMap", Name: "cfg"},
		{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Namespace: "prod", Name: "reader"},
		{APIVersion: "v1", Kind: "ConfigMap", Name: "gone"},
	}
	newRefs := []ResourceRef{
		{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler", Namespace: "prod", Name: "web"},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"},
		{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Name: "reader"},
	}

	orphaned := computeOrphaned(oldRefs, newRefs, "prod")

	assert.Equal(t, []ResourceRef{{APIVersion: "v1", Kind: "ConfigMap", Name: "gone"}}, orphaned)
}

func TestComputeOrphaned_DifferentNamespacesStayDistinct(t *testing.T) {
	oldRefs := []ResourceRef{{APIVersion: "v1", Kind: "ConfigMap", Namespace: "staging", Name: "cfg"}}
	newRefs := []ResourceRef{{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg"}}

	assert.Equal(t, oldRefs, computeOrphaned(oldRefs, newRefs, "prod"))
}
