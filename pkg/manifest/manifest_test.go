package manifest_test

import (
	"testing"

	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func obj(apiVersion, kind, name, namespace string) manifest.Resource {
	meta := map[string]interface{}{"name": name}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	return manifest.Resource{"apiVersion": apiVersion, "kind": kind, "metadata": meta}
}

func kinds(resources []manifest.Resource) []string {
	out := make([]string, len(resources))
	for i, r := range resources {
		out[i] = r["kind"].(string)
	}
	return out
}

func TestClean_KeepsAuthoredEmptyObjects(t *testing.T) {
	res := manifest.Resource{
		"spec": map[string]interface{}{
			"volumes": []interface{}{
				map[string]interface{}{"name": "tmp", "emptyDir": map[string]interface{}{}},
			},
			"podSelector": map[string]interface{}{},
		},
	}

	manifest.Clean(res)

	spec := res["spec"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{}, spec["podSelector"])
	vol := spec["volumes"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{}, vol["emptyDir"])
}

func TestClean_KeepsEmptyObjectsInsideArrays(t *testing.T) {
	// NetworkPolicy `ingress: [{}]` allows all traffic; dropping the {} would
	// flip it to deny-all.
	res := manifest.Resource{
		"spec": map[string]interface{}{
			"ingress": []interface{}{map[string]interface{}{}},
		},
	}

	manifest.Clean(res)

	assert.Equal(t, []interface{}{map[string]interface{}{}}, res["spec"].(map[string]interface{})["ingress"])
}

func TestClean_DropsNullsAndWhatTheyEmptied(t *testing.T) {
	res := manifest.Resource{
		"metadata": map[string]interface{}{"name": "x", "labels": nil},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{"app": nil},
			"args":     []interface{}{nil},
			"env":      []interface{}{map[string]interface{}{"value": nil}},
			"ports":    []interface{}{},
			"replicas": int64(0),
			"paused":   false,
			"name":     "",
		},
	}

	manifest.Clean(res)

	assert.Equal(t, map[string]interface{}{"name": "x"}, res["metadata"])
	assert.Equal(t, map[string]interface{}{
		"replicas": int64(0),
		"paused":   false,
		"name":     "",
	}, res["spec"])
}

func TestNormalize_DefaultsNamespaceOnlyOnNamespacedObjects(t *testing.T) {
	marked := obj("example.com/v1", "Thing", "marked", "")
	marked[manifest.ScopeMarker] = "cluster"
	resources := []manifest.Resource{
		obj("v1", "ConfigMap", "cfg", ""),
		obj("v1", "Namespace", "team", ""),
		obj("rbac.authorization.k8s.io/v1", "ClusterRole", "reader", ""),
		obj("core/v1", "Namespace", "legacy", ""),
		obj("apps/v1", "Deployment", "pinned", "other"),
		marked,
	}

	manifest.Normalize(resources, "prod")

	ns := func(i int) interface{} { return resources[i]["metadata"].(map[string]interface{})["namespace"] }
	assert.Equal(t, "prod", ns(0))
	assert.Nil(t, ns(1), "Namespace is cluster-scoped")
	assert.Nil(t, ns(2), "ClusterRole is cluster-scoped")
	assert.Nil(t, ns(3), "core/v1 is still the core group")
	assert.Equal(t, "other", ns(4), "explicit namespace wins")
	assert.Nil(t, ns(5), "scope marker wins")
	assert.NotContains(t, resources[5], manifest.ScopeMarker)
}

func TestNormalize_NoNamespaceLeavesObjectsAlone(t *testing.T) {
	resources := []manifest.Resource{obj("v1", "ConfigMap", "cfg", "")}

	manifest.Normalize(resources, "")

	assert.NotContains(t, resources[0]["metadata"], "namespace")
}

func TestGroup(t *testing.T) {
	assert.Equal(t, "", manifest.Group("v1"))
	assert.Equal(t, "", manifest.Group("core/v1"))
	assert.Equal(t, "apps", manifest.Group("apps/v1"))
	assert.Equal(t, "cert-manager.io", manifest.Group("cert-manager.io/v1"))
}

func TestFindDuplicates(t *testing.T) {
	resources := []manifest.Resource{
		obj("apps/v1", "Deployment", "web", "prod"),
		obj("v1", "Service", "web", "prod"),
		obj("apps/v1beta1", "Deployment", "web", "prod"),
		obj("apps/v1", "Deployment", "web", "staging"),
		obj("apps/v1", "Deployment", "web", "prod"),
		obj("v1", "Namespace", "prod", ""),
		obj("v1", "Namespace", "prod", "ignored-for-cluster-scope"),
	}

	dups := manifest.FindDuplicates(resources)

	require.Len(t, dups, 2)
	assert.Equal(t, `Deployment "web" (namespace "prod")`, dups[0].Ref)
	assert.Equal(t, []int{0, 2, 4}, dups[0].Indexes)
	assert.Equal(t, `Namespace "prod"`, dups[1].Ref)
	assert.Equal(t, []int{5, 6}, dups[1].Indexes)
}

func TestFindDuplicates_None(t *testing.T) {
	assert.Empty(t, manifest.FindDuplicates([]manifest.Resource{
		obj("v1", "ConfigMap", "a", ""),
		obj("v1", "ConfigMap", "b", ""),
	}))
}

func TestSortForApply_FollowsHelmInstallOrder(t *testing.T) {
	resources := []manifest.Resource{
		obj("networking.k8s.io/v1", "Ingress", "web", ""),
		obj("apps/v1", "Deployment", "web", ""),
		obj("example.com/v1", "Widget", "w", ""),
		obj("v1", "Service", "web", ""),
		obj("example.com/v1", "Gadget", "g", ""),
		obj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "widgets.example.com", ""),
		obj("v1", "ConfigMap", "second", ""),
		obj("v1", "Namespace", "team", ""),
		obj("v1", "ConfigMap", "first", ""),
	}
	resources[8]["metadata"].(map[string]interface{})["name"] = "third"

	manifest.SortForApply(resources)

	assert.Equal(t, []string{
		"Namespace", "ConfigMap", "ConfigMap", "CustomResourceDefinition",
		"Service", "Deployment", "Ingress", "Gadget", "Widget",
	}, kinds(resources))
	assert.Equal(t, "second", resources[1]["metadata"].(map[string]interface{})["name"], "same kind keeps registration order")
	assert.Equal(t, "third", resources[2]["metadata"].(map[string]interface{})["name"])
}
