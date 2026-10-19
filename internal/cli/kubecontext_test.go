package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/cloudticon/ct/internal/dev"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setContextEnv(t *testing.T, terminal, inCluster bool) {
	t.Helper()
	origT, origI, origL := stdinIsTerminalFn, inClusterFn, loadKubeContextFn
	t.Cleanup(func() { stdinIsTerminalFn, inClusterFn, loadKubeContextFn = origT, origI, origL })
	stdinIsTerminalFn = func() bool { return terminal }
	inClusterFn = func() bool { return inCluster }
	loadKubeContextFn = func() (kubeContextInfo, error) {
		return kubeContextInfo{
			Current:  "prod",
			Contexts: []string{"kind-ct-test", "prod"},
			Servers:  map[string]string{"prod": "https://prod.example:6443"},
		}, nil
	}
}

func stubApplyRender(t *testing.T) {
	t.Helper()
	origR, origS := renderResourcesForApply, resolveSourceDirForApply
	t.Cleanup(func() { renderResourcesForApply, resolveSourceDirForApply = origR, origS })
	resolveSourceDirForApply = func(string, bool) (string, error) { return "/tmp/x", nil }
	renderResourcesForApply = func(string, templateOpts) ([]k8s.Resource, origins, error) {
		return []k8s.Resource{{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]interface{}{"name": "cfg", "namespace": "dev"},
		}}, origins{}, nil
	}
}

func TestApply_NoTerminalWithoutContextRefusesBeforeAnyChange(t *testing.T) {
	setContextEnv(t, false, false)
	stubApplyRender(t)
	fake := withFakeCluster(t)

	cmd := newApplyCmd()
	cmd.SetArgs([]string{"app", ".", "-n", "dev"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	err := cmd.Execute()

	require.Error(t, err)
	assert.Equal(t, ExitCodeUsage, ExitCode(err))
	assert.Contains(t, err.Error(), "--context is required")
	assert.Contains(t, err.Error(), "kind-ct-test, prod")
	assert.Contains(t, err.Error(), "current: prod")
	assert.Empty(t, fake.Namespaces)
}

func TestApply_NoTerminalWithContextWorks(t *testing.T) {
	setContextEnv(t, false, false)
	stubApplyRender(t)
	withFakeCluster(t)

	cmd := newApplyCmd()
	cmd.SetArgs([]string{"app", ".", "-n", "dev", "--context", "kind-ct-test"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	require.NoError(t, cmd.Execute())
}

func TestApply_TerminalWithoutContextPrintsContextAndServer(t *testing.T) {
	setContextEnv(t, true, false)
	stubApplyRender(t)
	withFakeCluster(t)

	stderr := new(bytes.Buffer)
	cmd := newApplyCmd()
	cmd.SetArgs([]string{"app", ".", "-n", "dev"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(stderr)
	require.NoError(t, cmd.Execute())
	assert.Contains(t, stderr.String(), `"prod"`)
	assert.Contains(t, stderr.String(), "https://prod.example:6443")
}

func TestDelete_NoTerminalWithoutContextRefuses(t *testing.T) {
	setContextEnv(t, false, false)
	fake := withFakeCluster(t)
	_ = fake

	cmd := newDeleteCmd()
	cmd.SetArgs([]string{"app", "-n", "dev"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	err := cmd.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitCodeUsage, ExitCode(err))
	assert.Contains(t, err.Error(), "--context is required")
}

func TestDev_NoTerminalWithoutContextRefuses(t *testing.T) {
	setContextEnv(t, false, false)
	orig := runDevMode
	t.Cleanup(func() { runDevMode = orig })
	called := false
	runDevMode = func(context.Context, dev.RunOpts) error { called = true; return nil }

	cmd := newDevCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	err := cmd.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitCodeUsage, ExitCode(err))
	assert.False(t, called)
}

func TestListDoesNotRequireContext(t *testing.T) {
	setContextEnv(t, false, false)
	withFakeCluster(t)

	cmd := newListCmd()
	cmd.SetArgs([]string{"-n", "dev"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	require.NoError(t, cmd.Execute())
}

func TestInClusterDoesNotRequireContext(t *testing.T) {
	setContextEnv(t, false, true)
	require.NoError(t, requireKubeContext(&cobra.Command{}, ""))
}
