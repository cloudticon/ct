package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteCmd_RequiresReleaseName(t *testing.T) {
	cmd := newDeleteCmd()
	cmd.SetArgs([]string{})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestDeleteCmd_DefaultFlags(t *testing.T) {
	cmd := newDeleteCmd()

	ns, err := cmd.Flags().GetString("namespace")
	require.NoError(t, err)
	assert.Equal(t, "", ns)

	kubeContext, err := cmd.Flags().GetString("context")
	require.NoError(t, err)
	assert.Equal(t, "", kubeContext)
}

func TestRunDelete_DeletesReleaseAndReportsCount(t *testing.T) {
	fake := withFakeCluster(t)
	seedRelease(t, fake, "prod", "my-release", 2)

	stderr := new(bytes.Buffer)
	cmd := &cobra.Command{}
	cmd.SetErr(stderr)

	require.NoError(t, runDelete(cmd, "my-release", deleteOpts{
		namespace: "prod",
		context:   "staging",
	}))

	assert.Contains(t, stderr.String(), "deleted release my-release (2 resources)")
	require.Len(t, fake.DeleteCalls, 1)
	assert.Equal(t, "prod", fake.DeleteCalls[0].Namespace)
	assert.Equal(t, "my-release", fake.DeleteCalls[0].Release)

	// Inventory must be gone afterwards.
	releases, err := fake.ListReleases(context.Background(), "prod", false)
	require.NoError(t, err)
	assert.Empty(t, releases)
}

func TestRunDelete_ReturnsZeroWhenInventoryEmpty(t *testing.T) {
	withFakeCluster(t)
	stderr := new(bytes.Buffer)
	cmd := &cobra.Command{}
	cmd.SetErr(stderr)

	require.NoError(t, runDelete(cmd, "missing", deleteOpts{namespace: "prod"}))
	assert.Contains(t, stderr.String(), "(0 resources)")
}

func TestRunDelete_ReturnsErrorWhenClientCreationFails(t *testing.T) {
	orig := newClusterFn
	newClusterFn = func(kubeContext, namespace string) (k8s.Cluster, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { newClusterFn = orig })

	err := runDelete(&cobra.Command{}, "my-release", deleteOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating k8s client")
}

func TestRunDelete_ReturnsErrorWhenDeleteFails(t *testing.T) {
	fake := withFakeCluster(t)
	wrapped := &errCluster{Cluster: fake, deleteErr: errors.New("delete failure")}
	orig := newClusterFn
	newClusterFn = func(kubeContext, namespace string) (k8s.Cluster, error) {
		return wrapped, nil
	}
	t.Cleanup(func() { newClusterFn = orig })

	err := runDelete(&cobra.Command{}, "my-release", deleteOpts{namespace: "prod"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete failure")
}
