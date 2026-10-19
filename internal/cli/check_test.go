package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const checkMainCt = `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: Values.name },
  data: { release: Release.name, namespace: Release.namespace },
});
`

func runCheckCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newCheckCmd()
	cmd.SilenceUsage = true // as under rootCmd
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

func checkProject(t *testing.T, prod string) string {
	return writeProject(t, checkMainCt, map[string]string{
		"values.yaml":      "name: web\n",
		"values-dev.yaml":  "name: web-dev\n",
		"values-prod.yaml": prod,
		"notes.yaml":       "not: values\n",
	})
}

func TestCheck_ChecksBaseAndEveryValuesFile(t *testing.T) {
	dir := checkProject(t, "name: web-prod\n")

	stdout, _, err := runCheckCmd(t, dir)
	require.NoError(t, err)
	assert.Equal(t, "values.yaml       ok\nvalues-dev.yaml   ok\nvalues-prod.yaml  ok\n", stdout)
	assert.NotContains(t, stdout, "apiVersion", "check must not print manifests")
}

func TestCheck_FailureOnlyInProdNamesVariantAndPosition(t *testing.T) {
	dir := checkProject(t, "name: Bad_Name\n")

	stdout, _, err := runCheckCmd(t, dir)
	require.Error(t, err)
	assert.Equal(t, ExitCodeError, ExitCode(err))
	assert.Equal(t, "values.yaml       ok\nvalues-dev.yaml   ok\nvalues-prod.yaml  1 problem\n", stdout)

	msg := err.Error()
	assert.True(t, strings.HasPrefix(msg, "1 of 3 variants failed\n"), msg)
	assert.Contains(t, msg, "values-prod.yaml: main.ct:2:")
	assert.Contains(t, msg, "[invalid-value]")
	assert.NotContains(t, msg, "values-dev.yaml:")
}

func TestCheck_JSONReportListsVariants(t *testing.T) {
	withErrorFormat(t, "json")
	dir := checkProject(t, "name: Bad_Name\n")

	stdout, _, err := runCheckCmd(t, dir)
	require.Error(t, err)
	assert.Equal(t, ExitCodeError, ExitCode(err))

	var buf bytes.Buffer
	PrintError(&buf, err)
	assert.Empty(t, buf.String(), "the report on stdout is the only output")

	var report checkReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
	assert.False(t, report.OK)
	require.Len(t, report.Variants, 3)
	assert.Equal(t, "values.yaml", report.Variants[0].Name)
	assert.Equal(t, []string{"values.yaml"}, report.Variants[0].Files)
	assert.True(t, report.Variants[0].OK)
	assert.Empty(t, report.Variants[0].Errors)
	prod := report.Variants[2]
	assert.Equal(t, "values-prod.yaml", prod.Name)
	assert.Equal(t, []string{"values.yaml", "values-prod.yaml"}, prod.Files)
	assert.False(t, prod.OK)
	require.Len(t, prod.Errors, 1)
	assert.Equal(t, "invalid-value", prod.Errors[0].Code)
	assert.Equal(t, "main.ct", prod.Errors[0].File)
	assert.Equal(t, 2, prod.Errors[0].Line)
	assert.Contains(t, stdout, `"errors": []`, "a passing variant has an empty error list, not null")
}

func TestCheck_ReleaseNameAndNamespace(t *testing.T) {
	const mainCt = `
if (Release.name !== Values.wantName || Release.namespace !== Values.wantNamespace) {
  throw new Error("got " + Release.name + "/" + Release.namespace);
}
`
	parent := t.TempDir()
	dir := filepath.Join(parent, "shop")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(mainCt), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("wantName: shop\nwantNamespace: default\n"), 0o644))

	_, _, err := runCheckCmd(t, dir)
	require.NoError(t, err, "defaults: directory name and namespace default")

	_, _, err = runCheckCmd(t, dir, "--name", "api", "-n", "prod", "--set", "wantName=api", "--set", "wantNamespace=prod")
	require.NoError(t, err)

	_, _, err = runCheckCmd(t, dir, "--name", "api")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "got api/default")
}

func TestCheck_ExplicitValuesIsOneVariant(t *testing.T) {
	dir := checkProject(t, "name: Bad_Name\n")

	stdout, _, err := runCheckCmd(t, dir, "-f", "values-dev.yaml")
	require.NoError(t, err)
	assert.Equal(t, "values-dev.yaml  ok\n", stdout)
}

func TestCheck_NoValuesFileChecksBase(t *testing.T) {
	dir := writeProject(t, `__ct_resources.push({apiVersion: "v1", kind: "ConfigMap", metadata: {name: "a"}});`, nil)

	stdout, _, err := runCheckCmd(t, dir)
	require.NoError(t, err)
	assert.Equal(t, "base  ok\n", stdout)
}

func TestCheck_NoCluster(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))
	dir := checkProject(t, "name: web-prod\n")

	stdout, _, err := runCheckCmd(t, dir)
	require.NoError(t, err)
	assert.NotContains(t, stdout, "kind:")
}

func TestCheck_InvalidDefaultReleaseNameHintsFlag(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "My_App")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(""), 0o644))

	_, _, err := runCheckCmd(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--name")
}

func TestCheck_MissingDirIsUsageError(t *testing.T) {
	_, _, err := runCheckCmd(t, filepath.Join(t.TempDir(), "nope"))
	require.Error(t, err)
	assert.Equal(t, ExitCodeUsage, ExitCode(err))
}
