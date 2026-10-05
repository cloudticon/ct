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

	require.NoError(t, scaffold.Init(dir))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestInit_CreatesStarterFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	require.NoError(t, scaffold.Init(dir))

	for _, name := range []string{"main.ct", "values.json"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		require.NoError(t, err, "file should exist: %s", name)
		assert.False(t, info.IsDir())
	}
}

func TestInit_MainCtContainsImports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	require.NoError(t, scaffold.Init(dir))

	content, err := os.ReadFile(filepath.Join(dir, "main.ct"))
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "https://github.com/cloudticon/k8s")
	assert.Contains(t, s, "deployment(")
	assert.Contains(t, s, "service(")
}

func TestInit_ValuesJsonHasDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	require.NoError(t, scaffold.Init(dir))

	content, err := os.ReadFile(filepath.Join(dir, "values.json"))
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, `"image"`)
	assert.Contains(t, s, `"replicas"`)
}

func TestInit_DoesNotCreateLegacyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")

	require.NoError(t, scaffold.Init(dir))

	for _, name := range []string{"tsconfig.json", ".gitignore", "ct.ts", "values.ts"} {
		path := filepath.Join(dir, name)
		_, err := os.Stat(path)
		assert.True(t, os.IsNotExist(err), "legacy file should not exist: %s", name)
	}

	_, err := os.Stat(filepath.Join(dir, ".ctts"))
	assert.True(t, os.IsNotExist(err), ".ctts directory should not exist")
}
