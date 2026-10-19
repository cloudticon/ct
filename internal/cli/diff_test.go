package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/cloudticon/ct/pkg/k8s/k8stest"
	"github.com/fatih/color"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// diffProject registers a Deployment (main.ct:3:20) and a ConfigMap (main.ct:16:20).
const diffProject = applyProject + `
__ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name: "cfg" }, data: { a: "1" } });
`

// diffPlan is what a cluster answers for diffProject when the Deployment
// changed, the ConfigMap is new and a Service left the code.
var diffPlan = &k8s.Plan{Objects: []k8s.PlannedObject{
	{Action: k8s.ActionCreate, APIVersion: "v1", Kind: "ConfigMap", Namespace: "prod", Name: "cfg", Diff: "--- live\n+++ ct\n@@ -0,0 +1,2 @@\n+data:\n+  a: \"1\"\n"},
	{Action: k8s.ActionChange, APIVersion: "apps/v1", Kind: "Deployment", Namespace: "prod", Name: "web", Diff: "--- live\n+++ ct\n@@ -1 +1 @@\n-  replicas: 2\n+  replicas: 3\n"},
	{Action: k8s.ActionNone, APIVersion: "v1", Kind: "ServiceAccount", Namespace: "prod", Name: "web"},
	{Action: k8s.ActionPrune, APIVersion: "v1", Kind: "Service", Namespace: "prod", Name: "old"},
}}

func runDiffProject(t *testing.T, fake *k8stest.Fake, args ...string) (stdout string, err error) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(diffProject), 0o644))
	noColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = noColor })

	cmd := newDiffCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(append([]string{"web", dir, "-n", "prod"}, args...))
	err = cmd.Execute()
	return out.String(), err
}

func TestDiffCmd_PrintsActionsDiffsAndSummary(t *testing.T) {
	fake := withFakeCluster(t)
	fake.PlanHook = func(ns, release string, resources []k8s.Resource, opts k8s.PlanOpts) (*k8s.Plan, error) {
		return diffPlan, nil
	}

	out, err := runDiffProject(t, fake)
	require.NoError(t, err)

	assert.Equal(t, `create v1 ConfigMap prod/cfg
--- live
+++ ct
@@ -0,0 +1,2 @@
+data:
+  a: "1"
change apps/v1 Deployment prod/web
--- live
+++ ct
@@ -1 +1 @@
-  replicas: 2
+  replicas: 3
none   v1 ServiceAccount prod/web
prune  v1 Service prod/old
Plan: 1 to create, 1 to change, 1 to prune, 1 unchanged.
`, out)

	require.Len(t, fake.PlanCalls, 1)
	assert.Equal(t, "prod", fake.PlanCalls[0].Namespace)
	assert.Equal(t, "web", fake.PlanCalls[0].Release)
	labels := fake.PlanCalls[0].Resources[0]["metadata"].(map[string]interface{})["labels"].(map[string]interface{})
	assert.Equal(t, "web", labels["ct.cloudticon.com/instance"], "the plan sees objects as apply sends them")
	assert.Empty(t, fake.ApplyCalls)
}

func TestDiffCmd_JSONListsObjectsWithAction(t *testing.T) {
	fake := withFakeCluster(t)
	fake.PlanHook = func(ns, release string, resources []k8s.Resource, opts k8s.PlanOpts) (*k8s.Plan, error) {
		return diffPlan, nil
	}

	out, err := runDiffProject(t, fake, "-o", "json")
	require.NoError(t, err)

	var got []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 4)
	var actions []string
	for _, o := range got {
		actions = append(actions, o["action"].(string))
	}
	assert.Equal(t, []string{"create", "change", "none", "prune"}, actions)
	assert.Equal(t, map[string]interface{}{
		"action": "prune", "apiVersion": "v1", "kind": "Service", "namespace": "prod", "name": "old",
	}, got[3])
	assert.Contains(t, got[1]["diff"], "+  replicas: 3")
}

func TestDiffCmd_JSONEmptyPlanIsEmptyList(t *testing.T) {
	fake := withFakeCluster(t)
	fake.PlanHook = func(ns, release string, resources []k8s.Resource, opts k8s.PlanOpts) (*k8s.Plan, error) {
		return &k8s.Plan{}, nil
	}

	out, err := runDiffProject(t, fake, "-o", "json")
	require.NoError(t, err)
	assert.Equal(t, "[]\n", out)
}

func TestDiffCmd_PlansFromInventoryWithoutWriting(t *testing.T) {
	fake := withFakeCluster(t)
	inventory := []k8s.ResourceRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "prod", Name: "web"},
		{APIVersion: "v1", Kind: "Service", Namespace: "prod", Name: "old"},
	}
	fake.Releases["prod"] = map[string][]k8s.ResourceRef{"web": inventory}

	out, err := runDiffProject(t, fake)
	require.NoError(t, err)

	assert.Contains(t, out, "none   apps/v1 Deployment prod/web\n")
	assert.Contains(t, out, "create v1 ConfigMap prod/cfg\n")
	assert.Contains(t, out, "prune  v1 Service prod/old\n")
	assert.Contains(t, out, "Plan: 1 to create, 0 to change, 1 to prune, 1 unchanged.\n")
	assert.Equal(t, inventory, fake.Releases["prod"]["web"], "inventory is untouched")
	assert.Empty(t, fake.ApplyCalls)
}

func TestDiffCmd_UnsupportedOutputIsUsageError(t *testing.T) {
	withFakeCluster(t)

	_, err := runDiffProject(t, nil, "-o", "yaml")
	require.Error(t, err)
	assert.Equal(t, ExitCodeUsage, ExitCode(err))
	assert.Contains(t, err.Error(), "unsupported output format: yaml")
}

func TestDiffCmd_EveryRejectionPointsAtRegistration(t *testing.T) {
	fake := withFakeCluster(t)
	fake.Reject("Deployment", "web", apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
		field.Invalid(field.NewPath("spec", "selector"), "x", "field is immutable"),
	}))
	fake.Reject("ConfigMap", "cfg", apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "cfg", nil))

	_, err := runDiffProject(t, fake)
	require.Error(t, err)

	var list diag.List
	require.ErrorAs(t, err, &list)
	require.Len(t, list, 2, "one run reports every rejected object, in apply order")
	assert.Equal(t, diag.CodeForbidden, list[0].Code)
	assert.Equal(t, 16, list[0].Line)
	assert.Equal(t, diag.CodeInvalidValue, list[1].Code)
	assert.Equal(t, "spec.selector", list[1].Path)
	assert.Equal(t, 3, list[1].Line)
	assert.Empty(t, fake.ApplyCalls)
}

func TestApplyDryRun_PrintsPlanAndWritesNothing(t *testing.T) {
	fake := withFakeCluster(t)
	fake.PlanHook = func(ns, release string, resources []k8s.Resource, opts k8s.PlanOpts) (*k8s.Plan, error) {
		assert.True(t, opts.CreateNamespace, "the plan knows apply would create the namespace")
		return diffPlan, nil
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(diffProject), 0o644))
	noColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = noColor })

	cmd := newApplyCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"web", dir, "-n", "prod", "--dry-run", "--create-namespace"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, `create v1 ConfigMap prod/cfg
change apps/v1 Deployment prod/web
none   v1 ServiceAccount prod/web
prune  v1 Service prod/old
Plan: 1 to create, 1 to change, 1 to prune, 1 unchanged.
dry run: nothing was written to the cluster
`, stderr.String(), "apply --dry-run prints the summary without diffs")
	assert.Empty(t, stdout.String())
	assert.Empty(t, fake.ApplyCalls)
	assert.Empty(t, fake.Namespaces, "--create-namespace creates nothing in a dry run")
	assert.Empty(t, fake.Releases)
}

func TestApplyDryRun_RejectionIsDiagnostic(t *testing.T) {
	fake := withFakeCluster(t)
	fake.Reject("Deployment", "web", apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
		field.Required(field.NewPath("spec", "template", "spec", "containers").Index(0).Child("image"), ""),
	}))
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(diffProject), 0o644))

	cmd := newApplyCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"web", dir, "-n", "prod", "--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)

	var list diag.List
	require.ErrorAs(t, err, &list)
	require.Len(t, list, 1)
	assert.Equal(t, diag.CodeMissingField, list[0].Code)
	assert.Equal(t, "main.ct", list[0].File)
	assert.Equal(t, 3, list[0].Line)
	assert.Empty(t, fake.ApplyCalls)
	assert.Empty(t, fake.Releases)
}

func TestApplyCmd_DryRunDefaultsOff(t *testing.T) {
	dryRun, err := newApplyCmd().Flags().GetBool("dry-run")
	require.NoError(t, err)
	assert.False(t, dryRun)
}
