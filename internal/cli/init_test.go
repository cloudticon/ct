package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitCmd_CreatesProjectStructure(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "myproject")

	cmd := newInitCmd()
	cmd.SetArgs([]string{"--dir", projectDir})
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	err := cmd.Execute()
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "Initialized ct project")
	assert.FileExists(t, filepath.Join(projectDir, "main.ct"))
	assert.FileExists(t, filepath.Join(projectDir, "values.json"))
}

func TestInitCmd_DefaultDir(t *testing.T) {
	origDir, _ := os.Getwd()
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { os.Chdir(origDir) })

	cmd := newInitCmd()
	cmd.SetArgs([]string{})
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	err := cmd.Execute()
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "main.ct"))
	assert.FileExists(t, filepath.Join(tmpDir, "values.json"))
}

func TestInitCmd_DoesNotOverwriteExistingProject(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "values.json"), []byte(`{"mine":true}`), 0o644))

	cmd := newInitCmd()
	cmd.SetArgs([]string{"--dir", dir})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	require.Error(t, err)
	content, _ := os.ReadFile(filepath.Join(dir, "values.json"))
	assert.Equal(t, `{"mine":true}`, string(content))
}

func TestInitCmd_DirectoryArgument(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "myproject")

	cmd := newInitCmd()
	cmd.SetArgs([]string{projectDir})
	cmd.SetOut(new(bytes.Buffer))

	require.NoError(t, cmd.Execute())
	assert.FileExists(t, filepath.Join(projectDir, "main.ct"))
}
