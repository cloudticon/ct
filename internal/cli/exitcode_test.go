package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func execExit(t *testing.T, args ...string) (int, string) {
	t.Helper()
	t.Cleanup(func() {
		errorFormat = "text"
		rootCmd.SetArgs(nil)
	})
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	require.Error(t, err)
	var buf bytes.Buffer
	PrintError(&buf, err)
	return ExitCode(err), buf.String()
}

func TestExitCode_MissingArgsIsUsageWithExample(t *testing.T) {
	code, out := execExit(t, "template")
	assert.Equal(t, 2, code)
	assert.Contains(t, out, "usage: ct template <name> <dir|repo>")
	assert.Contains(t, out, "example: ct template my-app")
}

func TestExitCode_UnknownFlagAndCommandAreUsageInJSON(t *testing.T) {
	for _, args := range [][]string{
		{"--error-format", "json", "template", "a", "b", "--bogus"},
		{"--error-format", "json", "bogus"},
	} {
		code, out := execExit(t, args...)
		assert.Equal(t, 2, code, args)
		assert.Contains(t, out, `"code": "usage"`, args)
	}
}

func TestExitCode_UnreachableClusterIs3(t *testing.T) {
	old := newClusterFn
	t.Cleanup(func() { newClusterFn = old })
	newClusterFn = func(string, string) (k8s.Cluster, error) { return nil, errors.New("no kubeconfig") }
	code, _ := execExit(t, "list")
	assert.Equal(t, 3, code)
}

func TestExitCode_PlainAndDiagnosticsAre1(t *testing.T) {
	assert.Equal(t, 1, ExitCode(errors.New("x")))
	assert.Equal(t, 1, ExitCode(diag.Errorf(diag.CodeInvalidResource, "bad")))
	assert.Equal(t, 3, ExitCode(clusterError(errors.New("x"))))
	assert.Equal(t, 1, ExitCode(clusterError(diag.Errorf(diag.CodeInvalidResource, "bad"))))
}
