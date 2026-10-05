package dev_test

import (
	"testing"

	"github.com/cloudticon/ct/internal/dev"
	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeDeployment(name string, labels map[string]interface{}) engine.Resource {
	return engine.Resource{
		"kind":       "Deployment",
		"apiVersion": "apps/v1",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": labels,
			},
		},
	}
}

func makeStatefulSet(name string, labels map[string]interface{}) engine.Resource {
	return engine.Resource{
		"kind":       "StatefulSet",
		"apiVersion": "apps/v1",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": labels,
			},
		},
	}
}

func makeService(name string, selector map[string]interface{}) engine.Resource {
	return engine.Resource{
		"kind":       "Service",
		"apiVersion": "v1",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"selector": selector,
		},
	}
}

func TestResolveSelectors_Deployment(t *testing.T) {
	resources := []engine.Resource{
		makeDeployment("remix", map[string]interface{}{"app": "remix"}),
	}
	targets := []dev.Target{{Name: "remix"}}

	err := dev.ResolveSelectors(targets, resources)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"app": "remix"}, targets[0].Selector)
}

func TestResolveSelectors_StatefulSet(t *testing.T) {
	resources := []engine.Resource{
		makeStatefulSet("postgres", map[string]interface{}{"app": "postgres"}),
	}
	targets := []dev.Target{{Name: "postgres"}}

	err := dev.ResolveSelectors(targets, resources)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"app": "postgres"}, targets[0].Selector)
}

func TestResolveSelectors_ServiceIgnored(t *testing.T) {
	resources := []engine.Resource{
		makeDeployment("remix", map[string]interface{}{"app": "remix"}),
		makeService("remix", map[string]interface{}{"app": "remix"}),
	}
	targets := []dev.Target{{Name: "remix"}}

	err := dev.ResolveSelectors(targets, resources)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"app": "remix"}, targets[0].Selector)
}

func TestResolveSelectors_ConflictingWorkloads(t *testing.T) {
	resources := []engine.Resource{
		makeDeployment("web", map[string]interface{}{"app": "web-deploy"}),
		makeStatefulSet("web", map[string]interface{}{"app": "web-sts"}),
	}
	targets := []dev.Target{{Name: "web"}}

	err := dev.ResolveSelectors(targets, resources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
}

func TestResolveSelectors_ManualSelectorSkipped(t *testing.T) {
	targets := []dev.Target{{
		Name:     "postgres",
		Selector: map[string]string{"cnpg.io/cluster": "postgres"},
	}}
	err := dev.ResolveSelectors(targets, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"cnpg.io/cluster": "postgres"}, targets[0].Selector)
}

func TestResolveSelectors_NotFound(t *testing.T) {
	targets := []dev.Target{{Name: "nonexistent"}}
	err := dev.ResolveSelectors(targets, []engine.Resource{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveSelectors_OnlyServiceNotEnough(t *testing.T) {
	resources := []engine.Resource{
		makeService("web-svc", map[string]interface{}{"app": "web"}),
	}
	targets := []dev.Target{{Name: "web-svc"}}

	err := dev.ResolveSelectors(targets, resources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveSelectors_MultipleTargets(t *testing.T) {
	resources := []engine.Resource{
		makeDeployment("auth-proxy", map[string]interface{}{"app": "auth-proxy"}),
		makeDeployment("remix", map[string]interface{}{"app": "remix"}),
	}
	targets := []dev.Target{
		{Name: "auth-proxy"},
		{Name: "remix"},
		{Name: "postgres", Selector: map[string]string{"cnpg.io/cluster": "postgres"}},
	}

	err := dev.ResolveSelectors(targets, resources)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"app": "auth-proxy"}, targets[0].Selector)
	assert.Equal(t, map[string]string{"app": "remix"}, targets[1].Selector)
	assert.Equal(t, map[string]string{"cnpg.io/cluster": "postgres"}, targets[2].Selector)
}

func TestResolveSelectors_MultiLabelSelector(t *testing.T) {
	resources := []engine.Resource{
		makeDeployment("web", map[string]interface{}{
			"app":     "web",
			"version": "v2",
		}),
	}
	targets := []dev.Target{{Name: "web"}}

	err := dev.ResolveSelectors(targets, resources)
	require.NoError(t, err)
	assert.Equal(t, "web", targets[0].Selector["app"])
	assert.Equal(t, "v2", targets[0].Selector["version"])
}

func TestResolveSelectors_MissingSelectorPath(t *testing.T) {
	resources := []engine.Resource{{
		"kind":     "Deployment",
		"metadata": map[string]interface{}{"name": "broken"},
		"spec":     map[string]interface{}{},
	}}
	targets := []dev.Target{{Name: "broken"}}

	err := dev.ResolveSelectors(targets, resources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing path")
}

func TestResolveSelectors_JobKind(t *testing.T) {
	resources := []engine.Resource{{
		"kind":       "Job",
		"apiVersion": "batch/v1",
		"metadata":   map[string]interface{}{"name": "migrate"},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"job-name": "migrate"},
			},
		},
	}}
	targets := []dev.Target{{Name: "migrate"}}

	err := dev.ResolveSelectors(targets, resources)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"job-name": "migrate"}, targets[0].Selector)
}

func TestUniqueWorkloadNames_Basic(t *testing.T) {
	resources := []engine.Resource{
		makeDeployment("auth-proxy", map[string]interface{}{"app": "auth-proxy"}),
		makeDeployment("remix", map[string]interface{}{"app": "remix"}),
		makeService("remix-svc", map[string]interface{}{"app": "remix"}),
	}

	names := dev.UniqueWorkloadNames(resources)
	assert.Equal(t, []string{"auth-proxy", "remix"}, names)
}

func TestUniqueWorkloadNames_ExcludesConflicts(t *testing.T) {
	resources := []engine.Resource{
		makeDeployment("web", map[string]interface{}{"app": "web-deploy"}),
		makeStatefulSet("web", map[string]interface{}{"app": "web-sts"}),
		makeDeployment("api", map[string]interface{}{"app": "api"}),
	}

	names := dev.UniqueWorkloadNames(resources)
	assert.Equal(t, []string{"api"}, names)
}

func TestUniqueWorkloadNames_Empty(t *testing.T) {
	names := dev.UniqueWorkloadNames(nil)
	assert.Empty(t, names)
}
