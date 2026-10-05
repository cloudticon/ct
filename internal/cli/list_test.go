package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/cloudticon/ct/pkg/k8s/k8stest"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListCmd_RequiresNoArgs(t *testing.T) {
	cmd := newListCmd()
	cmd.SetArgs([]string{"unexpected"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "unexpected" for "list"`)
}

func TestListCmd_DefaultFlags(t *testing.T) {
	cmd := newListCmd()

	ns, err := cmd.Flags().GetString("namespace")
	require.NoError(t, err)
	assert.Equal(t, "", ns)

	allNamespaces, err := cmd.Flags().GetBool("all-namespaces")
	require.NoError(t, err)
	assert.False(t, allNamespaces)

	kubeContext, err := cmd.Flags().GetString("context")
	require.NoError(t, err)
	assert.Equal(t, "", kubeContext)

	outputFmt, err := cmd.Flags().GetString("output")
	require.NoError(t, err)
	assert.Equal(t, "", outputFmt)
}

// withFakeCluster swaps newClusterFn for the duration of the test, returning
// the fake so the test can seed state and assert calls. Cleanup is automatic.
func withFakeCluster(t *testing.T) *k8stest.Fake {
	t.Helper()
	fake := k8stest.NewFake()
	orig := newClusterFn
	newClusterFn = func(kubeContext, namespace string) (k8s.Cluster, error) {
		fake.DefaultNamespace = namespace
		return fake, nil
	}
	t.Cleanup(func() { newClusterFn = orig })
	return fake
}

func seedRelease(t *testing.T, fake *k8stest.Fake, ns, name string, count int) {
	t.Helper()
	res := make([]k8s.Resource, 0, count)
	for i := 0; i < count; i++ {
		res = append(res, k8s.Resource{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]interface{}{"name": fmtName(name, i), "namespace": ns},
		})
	}
	require.NoError(t, fake.ApplyRelease(context.Background(), ns, name, res))
}

func fmtName(prefix string, i int) string {
	return prefix + "-" + string(rune('a'+i))
}

func TestRunList_TableOutput(t *testing.T) {
	fake := withFakeCluster(t)
	seedRelease(t, fake, "prod", "api", 3)
	seedRelease(t, fake, "staging", "backend", 1)

	stdout := new(bytes.Buffer)
	cmd := &cobra.Command{}
	cmd.SetOut(stdout)

	err := runList(cmd, listOpts{
		namespace:     "prod",
		context:       "dev-cluster",
		allNamespaces: false,
	})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "NAME")
	assert.Contains(t, stdout.String(), "api")
	// Listing scoped to "prod" namespace excludes "staging".
	assert.NotContains(t, stdout.String(), "backend")
}

func TestRunList_JSONOutput(t *testing.T) {
	fake := withFakeCluster(t)
	seedRelease(t, fake, "prod", "api", 2)

	stdout := new(bytes.Buffer)
	cmd := &cobra.Command{}
	cmd.SetOut(stdout)

	err := runList(cmd, listOpts{namespace: "prod", outputFmt: "json"})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), `"name": "api"`)
	assert.Contains(t, stdout.String(), `"resources": 2`)
}

func TestRunList_YAMLOutput_AllNamespaces(t *testing.T) {
	fake := withFakeCluster(t)
	seedRelease(t, fake, "staging", "backend", 1)

	stdout := new(bytes.Buffer)
	cmd := &cobra.Command{}
	cmd.SetOut(stdout)

	err := runList(cmd, listOpts{outputFmt: "yaml", allNamespaces: true})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "name: backend")
	assert.Contains(t, stdout.String(), "namespace: staging")
}

func TestRunList_ReturnsErrorWhenClientCreationFails(t *testing.T) {
	orig := newClusterFn
	newClusterFn = func(kubeContext, namespace string) (k8s.Cluster, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { newClusterFn = orig })

	err := runList(&cobra.Command{}, listOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating k8s client")
}

func TestRunList_ReturnsErrorWhenListFails(t *testing.T) {
	fake := withFakeCluster(t)
	// Override ListReleases via a wrapper that returns an error.
	listErr := &errCluster{Cluster: fake, listErr: errors.New("list failure")}
	orig := newClusterFn
	newClusterFn = func(kubeContext, namespace string) (k8s.Cluster, error) {
		return listErr, nil
	}
	t.Cleanup(func() { newClusterFn = orig })

	err := runList(&cobra.Command{}, listOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing releases")
}

func TestRunList_ReturnsErrorOnUnsupportedOutputFormat(t *testing.T) {
	withFakeCluster(t)

	err := runList(&cobra.Command{}, listOpts{outputFmt: "xml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported output format")
}

// errCluster wraps a Cluster and overrides selected methods to return errors.
// Used to drive specific error paths without recreating the whole adapter.
type errCluster struct {
	k8s.Cluster
	listErr   error
	applyErr  error
	deleteErr error
	ensureErr error
}

func (e *errCluster) ListReleases(ctx context.Context, ns string, allNs bool) ([]k8s.ReleaseInfo, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	return e.Cluster.ListReleases(ctx, ns, allNs)
}

func (e *errCluster) ApplyRelease(ctx context.Context, ns, release string, resources []k8s.Resource) error {
	if e.applyErr != nil {
		return e.applyErr
	}
	return e.Cluster.ApplyRelease(ctx, ns, release, resources)
}

func (e *errCluster) DeleteRelease(ctx context.Context, ns, release string) (int, error) {
	if e.deleteErr != nil {
		return 0, e.deleteErr
	}
	return e.Cluster.DeleteRelease(ctx, ns, release)
}

func (e *errCluster) EnsureNamespace(ctx context.Context, ns string) error {
	if e.ensureErr != nil {
		return e.ensureErr
	}
	return e.Cluster.EnsureNamespace(ctx, ns)
}
