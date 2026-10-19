package cli

import (
	"bytes"
	"github.com/cloudticon/ct/pkg/diag"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/internal/scaffold"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupProject(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)
	return dir
}

func TestTemplateCmd_MissingMainCt(t *testing.T) {
	dir := t.TempDir()

	cmd := newTemplateCmd()
	cmd.SetArgs([]string{"test-release", dir})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entry point not found")
}

func TestTemplateCmd_RequiresReleaseNameAndSource(t *testing.T) {
	cmd := newTemplateCmd()
	cmd.SetArgs([]string{"test-release"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 2 arg(s)")
}

func TestTemplateCmd_AutoDetectsValuesJSON(t *testing.T) {
	dir := setupProject(t)

	files := resolveValuesFiles(dir, nil)
	assert.Equal(t, []string{filepath.Join(dir, "values.json")}, files)
}

func TestTemplateCmd_ExplicitValuesOverride(t *testing.T) {
	dir := setupProject(t)

	custom := filepath.Join(t.TempDir(), "custom.json")
	require.NoError(t, os.WriteFile(custom, []byte(`{"image":"custom:1.0","replicas":1}`), 0644))

	files := resolveValuesFiles(dir, []string{custom})
	assert.Equal(t, []string{filepath.Join(dir, "values.json"), custom}, files)
}

func TestTemplateCmd_BaseFileNotLoadedTwice(t *testing.T) {
	dir := setupProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "values-prod.json"), []byte(`{}`), 0644))

	withBase := resolveValuesFiles(dir, []string{"values.json", filepath.Join(dir, "values-prod.json")})
	without := resolveValuesFiles(dir, []string{filepath.Join(dir, "values-prod.json")})
	assert.Equal(t, without, withBase)
}

func TestTemplateCmd_ExplicitValuesFallBackToProjectDir(t *testing.T) {
	dir := setupProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "values-prod.json"), []byte(`{}`), 0644))
	t.Chdir(t.TempDir())

	files := resolveValuesFiles(dir, []string{"values-prod.json", "missing.json"})
	assert.Equal(t, []string{filepath.Join(dir, "values.json"), filepath.Join(dir, "values-prod.json"), "missing.json"}, files)
}

func TestTemplateCmd_NoValuesFile(t *testing.T) {
	dir := t.TempDir()
	assert.Empty(t, resolveValuesFiles(dir, nil))
}

func TestTemplateCmd_NoCacheFlagDefault(t *testing.T) {
	cmd := newTemplateCmd()
	noCache, err := cmd.Flags().GetBool("no-cache")
	require.NoError(t, err)
	assert.False(t, noCache)
}

func TestCheckValuesFilesExist_HintListsProjectFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(`{}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "values-dev.json"), []byte(`{}`), 0644))

	err := checkValuesFilesExist(dir, []string{filepath.Join(dir, "values-prod.json")})
	var list diag.List
	require.ErrorAs(t, err, &list)
	assert.Equal(t, diag.CodeValues, list[0].Code)
	assert.Equal(t, filepath.Join(dir, "values-prod.json"), list[0].File)
	assert.Equal(t, "values files in the project: values-dev.json, values.yaml", list[0].Hint)
}
