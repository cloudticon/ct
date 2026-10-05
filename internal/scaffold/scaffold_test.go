package scaffold_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/internal/scaffold"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInit_CreatesDirectoryStructure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestInit_CreatesStarterFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)

	for _, name := range []string{"main.ct", "values.json", "AGENTS.md"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		require.NoError(t, err, "file should exist: %s", name)
		assert.False(t, info.IsDir())
	}
}

func TestInit_MainCtContainsImports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "main.ct"))
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, `from "github.com/cloudticon/k8s-factories@master"`)
	assert.Contains(t, s, "webApp(")
}

func TestInit_ValuesJsonHasDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "values.json"))
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, `"image"`)
	assert.Contains(t, s, `"replicas"`)
}

func TestInit_RefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte("// my work"), 0o644))

	_, err := scaffold.Init(dir, scaffold.Options{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "main.ct already exist(s)")
	content, _ := os.ReadFile(filepath.Join(dir, "main.ct"))
	assert.Equal(t, "// my work", string(content))
	assert.NoFileExists(t, filepath.Join(dir, "values.json"), "nothing is written when refusing")
}

func TestInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte("// old"), 0o644))

	written, err := scaffold.Init(dir, scaffold.Options{Force: true})

	require.NoError(t, err)
	assert.Equal(t, []string{"main.ct", "values.json", "AGENTS.md"}, written)
	content, _ := os.ReadFile(filepath.Join(dir, "main.ct"))
	assert.Contains(t, string(content), "webApp(")
}

func TestInit_DoesNotCreateLegacyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)

	for _, name := range []string{"tsconfig.json", ".gitignore", "ct.ts", "values.ts"} {
		path := filepath.Join(dir, name)
		_, err := os.Stat(path)
		assert.True(t, os.IsNotExist(err), "legacy file should not exist: %s", name)
	}

	_, err = os.Stat(filepath.Join(dir, ".ctts"))
	assert.True(t, os.IsNotExist(err), ".ctts directory should not exist")
}

func TestInit_KeepsAnExistingAgentsMd(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# repo rules"), 0o644))

	written, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err, "an existing AGENTS.md doesn't block init")
	assert.Equal(t, []string{"main.ct", "values.json"}, written)

	_, err = scaffold.Init(dir, scaffold.Options{Force: true})
	require.NoError(t, err)
	content, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	assert.Equal(t, "# repo rules", string(content), "--force never overwrites AGENTS.md")
}
