package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudticon/ct/internal/dev"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDevCmd_RejectsArgs(t *testing.T) {
	cmd := newDevCmd()
	cmd.SetArgs([]string{"unexpected"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown command")
}

func TestDevCmd_DefaultFlags(t *testing.T) {
	cmd := newDevCmd()

	envFile, _ := cmd.Flags().GetString("env-file")
	assert.Equal(t, ".env", envFile)

	ctx, _ := cmd.Flags().GetString("context")
	assert.Equal(t, "", ctx)

	name, _ := cmd.Flags().GetString("name")
	assert.Equal(t, "dev", name)

	del, _ := cmd.Flags().GetBool("delete")
	assert.False(t, del)

	createNamespace, _ := cmd.Flags().GetBool("create-namespace")
	assert.True(t, createNamespace)
}

func TestRunDev_PassesOptionsToRunner(t *testing.T) {
	origRunner := runDevMode
	t.Cleanup(func() { runDevMode = origRunner })

	var captured dev.RunOpts
	runDevMode = func(ctx context.Context, opts dev.RunOpts) error {
		captured = opts
		return nil
	}

	wdBefore, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Chdir(wdBefore)
	})

	workDir := t.TempDir()
	require.NoError(t, os.Chdir(workDir))

	stdin := strings.NewReader("input")
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd := &cobra.Command{}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	err = runDev(cmd, devOpts{
		envFile:         ".env.dev",
		context:         "staging",
		releaseName:     "my-dev",
		delete:          true,
		createNamespace: true,
	})
	require.NoError(t, err)

	absDir, err := filepath.Abs(workDir)
	require.NoError(t, err)
	assert.Equal(t, absDir, captured.Dir)
	assert.Equal(t, ".env.dev", captured.EnvFile)
	assert.Equal(t, "staging", captured.KubeCtx)
	assert.Equal(t, "my-dev", captured.ReleaseName)
	assert.True(t, captured.Delete)
	assert.True(t, captured.CreateNamespace)
	assert.False(t, captured.EnvFileRequired, "flag not set on the command line: the default .env is optional")
	assert.Same(t, stdin, captured.Stdin)
	assert.Same(t, stdout, captured.Stdout)
	assert.Same(t, stderr, captured.Stderr)
}

// An --env-file given on the command line must exist; only the default
// .env is optional.
func TestDevCmd_ExplicitEnvFileIsRequired(t *testing.T) {
	origRunner := runDevMode
	t.Cleanup(func() { runDevMode = origRunner })
	var captured dev.RunOpts
	runDevMode = func(_ context.Context, opts dev.RunOpts) error {
		captured = opts
		return nil
	}

	cmd := newDevCmd()
	cmd.SetArgs([]string{"--env-file", ".env.staging"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	require.NoError(t, cmd.Execute())
	assert.Equal(t, ".env.staging", captured.EnvFile)
	assert.True(t, captured.EnvFileRequired)
}
