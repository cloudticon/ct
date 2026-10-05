package cli

import (
	"bytes"
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
	require.NoError(t, scaffold.Init(dir))
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

	path := resolveValuesPath(dir, "")
	assert.Equal(t, filepath.Join(dir, "values.json"), path)
}

func TestTemplateCmd_ExplicitValuesOverride(t *testing.T) {
	dir := setupProject(t)

	custom := filepath.Join(t.TempDir(), "custom.json")
	require.NoError(t, os.WriteFile(custom, []byte(`{"image":"custom:1.0","replicas":1}`), 0644))

	path := resolveValuesPath(dir, custom)
	assert.Equal(t, custom, path)
}

func TestTemplateCmd_NoValuesFile(t *testing.T) {
	dir := t.TempDir()
	path := resolveValuesPath(dir, "")
	assert.Equal(t, "", path)
}

func TestTemplateCmd_NoCacheFlagDefault(t *testing.T) {
	cmd := newTemplateCmd()
	noCache, err := cmd.Flags().GetBool("no-cache")
	require.NoError(t, err)
	assert.False(t, noCache)
}
