package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/internal/scaffold"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	renderResourcesForApply = func(dir string, opts templateOpts) ([]k8s.Resource, error) {
		return []k8s.Resource{
			{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]interface{}{
					"name":      "cfg",
					"namespace": "dev",
				},
			},
		}, nil
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
