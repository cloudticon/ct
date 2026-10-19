package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// planCluster is a fake API server for planRelease: live objects by
// "<resource>/<namespace>/<name>", namespaces that exist, and rejections.
type planCluster struct {
	live       map[string]map[string]interface{}
	namespaces map[string]bool
	reject     map[string]error
}

func planKey(resource, ns, name string) string { return resource + "/" + ns + "/" + name }

var planAPIResources = []*metav1.APIResourceList{
	{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
			{Name: "secrets", Kind: "Secret", Namespaced: true},
			{Name: "services", Kind: "Service", Namespaced: true},
			{Name: "namespaces", Kind: "Namespace", Namespaced: false},
		},
	},
	{
		GroupVersion: "apps/v1",
		APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true}},
	},
}

// newPlanClient returns a client whose dynamic and core fakes record every
// action, so tests can assert that a plan writes nothing.
func newPlanClient(t *testing.T, pc *planCluster, inventory []ResourceRef) (*client, *dynamicfake.FakeDynamicClient, *fake.Clientset) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("get", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		get := action.(k8stesting.GetAction)
		obj, ok := pc.live[planKey(get.GetResource().Resource, get.GetNamespace(), get.GetName())]
		if !ok {
			return true, nil, apierrors.NewNotFound(get.GetResource().GroupResource(), get.GetName())
		}
		return true, &unstructured.Unstructured{Object: jsonCopy(obj)}, nil
	})
	dyn.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patch := action.(k8stesting.PatchAction)
		ns := patch.GetNamespace()
		if err := pc.reject[patch.GetName()]; err != nil {
			return true, nil, err
		}
		if ns != "" && !pc.namespaces[ns] {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, ns)
		}
		obj := map[string]interface{}{}
		if err := json.Unmarshal(patch.GetPatch(), &obj); err != nil {
			return true, nil, err
		}
		// What the server answers: the applied fields plus server-owned ones.
		meta := obj["metadata"].(map[string]interface{})
		meta["resourceVersion"] = "999"
		meta["uid"] = "u-1"
		meta["managedFields"] = []interface{}{map[string]interface{}{"manager": "ct"}}
		obj["status"] = map[string]interface{}{}
		if live, ok := pc.live[planKey(patch.GetResource().Resource, ns, patch.GetName())]; ok {
			if ann, ok := live["metadata"].(map[string]interface{})["annotations"]; ok {
				meta["annotations"] = ann
			}
		}
		return true, &unstructured.Unstructured{Object: obj}, nil
	})

	core := fake.NewSimpleClientset()
	core.Fake.Resources = planAPIResources
	if inventory != nil {
		data, err := json.Marshal(inventory)
		require.NoError(t, err)
		_, err = core.CoreV1().ConfigMaps("prod").Create(context.Background(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: inventoryConfigMapName("web"), Namespace: "prod"},
			Data:       map[string]string{inventoryDataKey: string(data)},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		core.ClearActions()
	}
	return newClientFromInterfaces(core.CoreV1(), core.Discovery(), dyn, "prod"), dyn, core
}

func assertNoWrites(t *testing.T, dyn *dynamicfake.FakeDynamicClient, core *fake.Clientset) {
	t.Helper()
	for _, a := range dyn.Actions() {
		switch a.GetVerb() {
		case "get", "list":
		case "patch":
			opts := a.(k8stesting.PatchActionImpl).PatchOptions
			assert.Equal(t, []string{metav1.DryRunAll}, opts.DryRun, "every patch must be a dry run")
		default:
			t.Errorf("unexpected %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
	for _, a := range core.Actions() {
		if v := a.GetVerb(); v != "get" && v != "list" {
			t.Errorf("unexpected core %s %s", v, a.GetResource().Resource)
		}
	}
}

func deployment(name string, replicas int64) Resource {
	return Resource{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": name, "namespace": "prod"},
		"spec":       map[string]interface{}{"replicas": replicas},
	}
}

func configMap(name string, data map[string]interface{}) Resource {
	return Resource{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": name, "namespace": "prod"},
		"data":       data,
	}
}

func liveCopy(res Resource) map[string]interface{} {
	obj := jsonCopy(res)
	meta := obj["metadata"].(map[string]interface{})
	meta["resourceVersion"] = "1"
	meta["uid"] = "u-1"
	meta["creationTimestamp"] = "2026-10-01T00:00:00Z"
	meta["managedFields"] = []interface{}{map[string]interface{}{"manager": "ct", "time": "old"}}
	obj["status"] = map[string]interface{}{"replicas": 2}
	return obj
}

func TestPlanRelease_CreateChangeNoneAndPrune(t *testing.T) {
	pc := &planCluster{
		live: map[string]map[string]interface{}{
			planKey("deployments", "prod", "web"): liveCopy(deployment("web", 2)),
			planKey("configmaps", "prod", "same"): liveCopy(configMap("same", map[string]interface{}{"a": "1"})),
			planKey("services", "prod", "old"):    {"apiVersion": "v1", "kind": "Service", "metadata": map[string]interface{}{"name": "old"}},
		},
		namespaces: map[string]bool{"prod": true},
	}
	inventory := []ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "prod", Name: "web"},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "same"},
		{APIVersion: "v1", Kind: "Service", Name: "old"},
	}
	c, dyn, core := newPlanClient(t, pc, inventory)

	plan, err := c.planRelease(context.Background(), "prod", "web", []Resource{
		deployment("web", 3),
		configMap("same", map[string]interface{}{"a": "1"}),
		configMap("new", map[string]interface{}{"b": "2"}),
	}, PlanOpts{})
	require.NoError(t, err)

	actions := map[string]Action{}
	for _, o := range plan.Objects {
		actions[o.Kind+"/"+o.Name] = o.Action
	}
	assert.Equal(t, map[string]Action{
		"Deployment/web": ActionChange,
		"ConfigMap/same": ActionNone,
		"ConfigMap/new":  ActionCreate,
		"Service/old":    ActionPrune,
	}, actions)
	assert.Equal(t, ActionPrune, plan.Objects[len(plan.Objects)-1].Action, "prunes come after the applied objects")
	assert.Equal(t, "prod", plan.Objects[len(plan.Objects)-1].Namespace, "prune refs get the release namespace")

	byName := map[string]PlannedObject{}
	for _, o := range plan.Objects {
		byName[o.Name] = o
	}
	assert.Contains(t, byName["web"].Diff, "-  replicas: 2\n")
	assert.Contains(t, byName["web"].Diff, "+  replicas: 3\n")
	assert.NotContains(t, byName["web"].Diff, "resourceVersion", "server-owned metadata is not a change")
	assert.NotContains(t, byName["web"].Diff, "managedFields")
	assert.NotContains(t, byName["web"].Diff, "status")
	assert.Contains(t, byName["new"].Diff, "+  b: \"2\"\n")
	assert.Empty(t, byName["same"].Diff)
	assert.Empty(t, byName["old"].Diff)

	assert.Equal(t, 1, plan.Count(ActionCreate))
	assert.Equal(t, 1, plan.Count(ActionChange))
	assert.Equal(t, 1, plan.Count(ActionPrune))
	assert.Equal(t, 1, plan.Count(ActionNone))
	assertNoWrites(t, dyn, core)
}

func TestPlanRelease_MasksSecretValues(t *testing.T) {
	secret := func(data map[string]interface{}) Resource {
		return Resource{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata":   map[string]interface{}{"name": "creds", "namespace": "prod"},
			"data":       data,
		}
	}
	live := liveCopy(secret(map[string]interface{}{"password": "b2xkLXNlY3JldA==", "user": "YWRtaW4=", "gone": "Z29uZQ=="}))
	live["metadata"].(map[string]interface{})["annotations"] = map[string]interface{}{
		lastAppliedAnnotation: `{"data":{"password":"b2xkLXNlY3JldA=="}}`,
	}
	pc := &planCluster{
		live:       map[string]map[string]interface{}{planKey("secrets", "prod", "creds"): live},
		namespaces: map[string]bool{"prod": true},
	}
	c, _, _ := newPlanClient(t, pc, nil)

	plan, err := c.planRelease(context.Background(), "prod", "web", []Resource{
		secret(map[string]interface{}{"password": "bmV3LXNlY3JldA==", "user": "YWRtaW4=", "added": "YWRkZWQ="}),
		{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata":   map[string]interface{}{"name": "fresh", "namespace": "prod"},
			"stringData": map[string]interface{}{"token": "plain-token"},
		},
	}, PlanOpts{})
	require.NoError(t, err)
	require.Len(t, plan.Objects, 2)

	all := plan.Objects[0].Diff + plan.Objects[1].Diff
	for _, value := range []string{"b2xkLXNlY3JldA==", "bmV3LXNlY3JldA==", "YWRtaW4=", "YWRkZWQ=", "Z29uZQ==", "plain-token"} {
		assert.NotContains(t, all, value)
	}
	changed := plan.Objects[0].Diff
	assert.Equal(t, ActionChange, plan.Objects[0].Action)
	assert.Contains(t, changed, "+  password: '*** (changed)'\n")
	assert.Contains(t, changed, "+  added: '***'\n")
	assert.Contains(t, changed, "-  gone: '***'\n")
	assert.Contains(t, changed, "\n   user: '***'\n", "an unchanged key is only context")
	assert.NotContains(t, changed, lastAppliedAnnotation+": '{", "kubectl's copy of the object is masked too")
	assert.Contains(t, plan.Objects[1].Diff, "+  token: '***'")
}

func TestPlanRelease_CollectsEveryRejection(t *testing.T) {
	invalid := func(name string) error {
		return apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, name, field.ErrorList{
			field.Invalid(field.NewPath("data"), "x", "bad"),
		})
	}
	pc := &planCluster{
		namespaces: map[string]bool{"prod": true},
		reject:     map[string]error{"a": invalid("a"), "b": invalid("b")},
	}
	c, dyn, core := newPlanClient(t, pc, nil)

	_, err := c.planRelease(context.Background(), "prod", "web", []Resource{
		configMap("a", nil), configMap("ok", nil), configMap("b", nil),
	}, PlanOpts{})
	require.Error(t, err)

	joined, ok := err.(interface{ Unwrap() []error })
	require.True(t, ok, "rejections are joined")
	require.Len(t, joined.Unwrap(), 2)
	for _, e := range joined.Unwrap() {
		var objErr *ObjectError
		require.True(t, errors.As(e, &objErr))
		assert.True(t, apierrors.IsInvalid(e))
	}
	assertNoWrites(t, dyn, core)
}

func TestPlanRelease_NamespaceCreatedByReleaseOrFlag(t *testing.T) {
	pc := &planCluster{namespaces: map[string]bool{}}
	c, dyn, core := newPlanClient(t, pc, nil)

	plan, err := c.planRelease(context.Background(), "prod", "web", []Resource{
		{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]interface{}{"name": "prod"}},
		configMap("cfg", map[string]interface{}{"a": "1"}),
	}, PlanOpts{})
	require.NoError(t, err)
	require.Len(t, plan.Objects, 2)
	assert.Equal(t, ActionCreate, plan.Objects[0].Action)
	assert.Equal(t, ActionCreate, plan.Objects[1].Action)
	assert.Contains(t, plan.Objects[1].Diff, "+  a: \"1\"")

	plan, err = c.planRelease(context.Background(), "prod", "web", []Resource{configMap("cfg", nil)}, PlanOpts{CreateNamespace: true})
	require.NoError(t, err)
	assert.Equal(t, ActionCreate, plan.Objects[0].Action)

	_, err = c.planRelease(context.Background(), "prod", "web", []Resource{configMap("cfg", nil)}, PlanOpts{})
	require.Error(t, err, "a missing namespace nobody creates is an error, as on apply")
	assert.True(t, isNamespaceNotFound(err))
	assertNoWrites(t, dyn, core)
}

func TestPlanRelease_UnknownKindIsObjectError(t *testing.T) {
	c, _, _ := newPlanClient(t, &planCluster{namespaces: map[string]bool{"prod": true}}, nil)

	_, err := c.planRelease(context.Background(), "prod", "web", []Resource{
		{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]interface{}{"name": "w", "namespace": "prod"}},
	}, PlanOpts{})
	var unknown *UnknownKindError
	require.True(t, errors.As(err, &unknown))
}

func TestUnifiedDiff_Deterministic(t *testing.T) {
	before := map[string]interface{}{"b": 1, "a": map[string]interface{}{"z": 1, "y": 2}}
	after := map[string]interface{}{"b": 2, "a": map[string]interface{}{"z": 1, "y": 2}}
	first, err := unifiedDiff(before, after)
	require.NoError(t, err)
	for range 5 {
		again, err := unifiedDiff(before, after)
		require.NoError(t, err)
		assert.Equal(t, first, again)
	}
	assert.True(t, strings.HasPrefix(first, "--- live\n+++ ct\n"), first)
}

func TestMaskSecret_LastAppliedAnnotationAndOtherKinds(t *testing.T) {
	secret := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{"annotations": map[string]interface{}{
			lastAppliedAnnotation: `{"data":{"password":"c2VjcmV0"}}`,
		}},
	}
	maskSecret(secret, nil)
	assert.Equal(t, maskedValue, secret["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})[lastAppliedAnnotation])

	cm := configMap("cfg", map[string]interface{}{"a": "visible"})
	maskSecret(nil, cm)
	assert.Equal(t, "visible", cm["data"].(map[string]interface{})["a"], "only Secrets are masked")
}
