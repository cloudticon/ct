package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/internal/scaffold"
	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestApplyCmd_MissingMainCt(t *testing.T) {
	dir := t.TempDir()

	cmd := newApplyCmd()
	cmd.SetArgs([]string{"my-release", dir})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entry point not found")
}

func TestApplyCmd_RequiresExactlyTwoArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"zero args", []string{}},
		{"one arg", []string{"my-release"}},
		{"three args", []string{"my-release", ".", "extra"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newApplyCmd()
			cmd.SetArgs(tt.args)
			cmd.SetOut(new(bytes.Buffer))
			cmd.SetErr(new(bytes.Buffer))

			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "accepts 2 arg(s)")
		})
	}
}

func TestApplyCmd_DefaultFlags(t *testing.T) {
	cmd := newApplyCmd()

	ns, _ := cmd.Flags().GetString("namespace")
	assert.Equal(t, "", ns)

	ctx, _ := cmd.Flags().GetString("context")
	assert.Equal(t, "", ctx)

	outputFmt, _ := cmd.Flags().GetString("output")
	assert.Equal(t, "", outputFmt)

	noCache, _ := cmd.Flags().GetBool("no-cache")
	assert.False(t, noCache)

	createNamespace, _ := cmd.Flags().GetBool("create-namespace")
	assert.False(t, createNamespace)
}

func TestApplyCmd_UsageShowsTwoArgs(t *testing.T) {
	cmd := newApplyCmd()
	assert.Contains(t, cmd.Use, "<name>")
	assert.Contains(t, cmd.Use, "<dir|repo>")
}

func TestApplyCmd_MissingMainCt_WithReleaseName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)

	cmd := newApplyCmd()
	cmd.SetArgs([]string{"prod-release", t.TempDir()})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entry point not found")
}

func TestEnsureApplyNamespace_SkipsWhenDisabledOrNamespaceEmpty(t *testing.T) {
	fake := withFakeCluster(t)

	require.NoError(t, ensureApplyNamespace(context.Background(), fake, "dev", false))
	require.NoError(t, ensureApplyNamespace(context.Background(), fake, "", true))

	assert.Empty(t, fake.Namespaces, "EnsureNamespace must not be called when disabled or empty")
}

func TestEnsureApplyNamespace_CreatesNamespace(t *testing.T) {
	fake := withFakeCluster(t)

	require.NoError(t, ensureApplyNamespace(context.Background(), fake, "dev", true))
	assert.True(t, fake.Namespaces["dev"], "namespace should be ensured")
}

func TestEnsureApplyNamespace_WrapsError(t *testing.T) {
	fake := withFakeCluster(t)
	wrapped := &errCluster{Cluster: fake, ensureErr: errors.New("boom")}
	orig := newClusterFn
	newClusterFn = func(kubeContext, namespace string) (k8s.Cluster, error) { return wrapped, nil }
	t.Cleanup(func() { newClusterFn = orig })

	err := ensureApplyNamespace(context.Background(), wrapped, "dev", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ensuring namespace "dev"`)
}

func TestRunApply_CreatesNamespaceAndAppliesRelease(t *testing.T) {
	fake := withFakeCluster(t)

	origResolveSourceDir := resolveSourceDirForApply
	origRenderResources := renderResourcesForApply
	t.Cleanup(func() {
		resolveSourceDirForApply = origResolveSourceDir
		renderResourcesForApply = origRenderResources
	})

	resolveSourceDirForApply = func(source string, noCache bool) (string, error) {
		return "/tmp/fake-source", nil
	}
	renderResourcesForApply = func(dir string, opts templateOpts) ([]k8s.Resource, origins, error) {
		return []k8s.Resource{
			{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]interface{}{
					"name":      "cfg",
					"namespace": "dev",
				},
			},
		}, nil, nil
	}

	err := runApply(&cobra.Command{}, "my-release", ".", applyOpts{
		templateOpts:    templateOpts{namespace: "dev"},
		createNamespace: true,
	})
	require.NoError(t, err)

	assert.True(t, fake.Namespaces["dev"], "namespace should be ensured")
	require.Len(t, fake.ApplyCalls, 1)
	assert.Equal(t, "dev", fake.ApplyCalls[0].Namespace)
	assert.Equal(t, "my-release", fake.ApplyCalls[0].Release)

	// Inject release labels must have run before apply.
	require.Len(t, fake.ApplyCalls[0].Resources, 1)
	meta := fake.ApplyCalls[0].Resources[0]["metadata"].(map[string]interface{})
	labels, ok := meta["labels"].(map[string]interface{})
	require.True(t, ok, "release labels should be injected onto resource metadata")
	assert.Equal(t, "my-release", labels["ct.cloudticon.com/instance"])
}

func TestApplyError_MissingNamespaceGetsHint(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "ghost")

	err := applyError(fmt.Errorf("applying resources: %w", notFound), nil)

	var list diag.List
	require.ErrorAs(t, err, &list)
	assert.Equal(t, "namespace-not-found", list[0].Code)
	assert.Contains(t, list[0].Hint, "--create-namespace")
}

func TestApplyError_OtherErrorsPassThrough(t *testing.T) {
	err := applyError(errors.New("boom"), nil)
	assert.EqualError(t, err, "apply failed: boom")
}

// applyProject is a project whose Deployment is registered at main.ct:3:20.
const applyProject = `const app = "web";

__ct_resources.push({
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { name: app },
  spec: {
    selector: { matchLabels: { app } },
    template: {
      metadata: { labels: { app } },
      spec: { containers: [{ name: app, image: "nginx" }] },
    },
  },
});
`

// runRejectedApply applies applyProject to a fake cluster that answers the
// Deployment with err, and returns what ct prints with --error-format json.
func runRejectedApply(t *testing.T, err error) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(applyProject), 0o644))
	fake := withFakeCluster(t)
	fake.Reject("Deployment", "web", err)
	withErrorFormat(t, "json")

	cmd := newApplyCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"demo", dir})
	runErr := cmd.Execute()
	require.Error(t, runErr)

	var out bytes.Buffer
	PrintError(&out, runErr)
	return out.String()
}

func TestRunApply_FieldRejectedByClusterPointsAtRegistration(t *testing.T) {
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
		field.Invalid(field.NewPath("spec", "selector"), "{app: web2}", "field is immutable"),
		field.Required(field.NewPath("spec", "template", "spec", "containers").Index(0).Child("image"), ""),
	})

	got := runRejectedApply(t, invalid)

	assert.Equal(t, `{
  "errors": [
    {
      "code": "invalid-value",
      "message": "Invalid value: \"{app: web2}\": field is immutable",
      "file": "main.ct",
      "line": 3,
      "column": 20,
      "resource": "Deployment \"web\"",
      "path": "spec.selector",
      "hint": "this field can't change on an existing object: keep the old value, or delete the object (or the release) and apply again to recreate it"
    },
    {
      "code": "missing-field",
      "message": "Required value",
      "file": "main.ct",
      "line": 3,
      "column": 20,
      "resource": "Deployment \"web\"",
      "path": "spec.template.spec.containers[0].image"
    }
  ]
}
`, got)
}

func TestRunApply_ClusterRejections(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		code     string
		path     string
		message  string
		hintPart string
	}{
		{
			name:     "unknown kind",
			err:      &k8s.UnknownKindError{APIVersion: "apps/v1", Kind: "Deployment"},
			code:     "unknown-kind",
			message:  `the cluster does not serve kind "Deployment" in apiVersion "apps/v1"`,
			hintPart: "install the CRD",
		},
		{
			name:     "forbidden",
			err:      apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("RBAC: access denied")),
			code:     "forbidden",
			message:  "the cluster refused access: ",
			hintPart: "--context",
		},
		{
			name: "undeclared field from server-side apply",
			err: apierrors.NewBadRequest("failed to create typed patch object (default/web; apps/v1, Kind=Deployment): " +
				".spec.replcas: field not declared in schema"),
			code:     "unknown-field",
			path:     "spec.replcas",
			message:  "field not declared in schema",
			hintPart: "kubectl explain",
		},
		{
			name:    "namespace missing",
			err:     apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "ghost"),
			code:    "namespace-not-found",
			message: "apply failed: ",
		},
		{
			name:    "anything else",
			err:     apierrors.NewInternalError(errors.New("etcd timeout")),
			code:    "error",
			message: "apply failed: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parsed struct {
				Errors []diag.Diagnostic `json:"errors"`
			}
			require.NoError(t, json.Unmarshal([]byte(runRejectedApply(t, tt.err)), &parsed))
			require.Len(t, parsed.Errors, 1)
			d := parsed.Errors[0]
			assert.Equal(t, tt.code, d.Code)
			assert.Equal(t, `Deployment "web"`, d.Resource)
			assert.Equal(t, "main.ct:3:20", d.Location(), "points at the registration in main.ct")
			assert.Equal(t, tt.path, d.Path)
			assert.Contains(t, d.Message, tt.message)
			assert.Contains(t, d.Hint, tt.hintPart)
		})
	}
}
