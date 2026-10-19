package scaffold_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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

	for _, name := range []string{"main.ct", "values.json", "AGENTS.md", "CLAUDE.md"} {
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
	assert.Equal(t, []string{"main.ct", "values.json", "AGENTS.md", "CLAUDE.md"}, written)
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
	assert.Equal(t, []string{"main.ct", "values.json", "CLAUDE.md"}, written)

	_, err = scaffold.Init(dir, scaffold.Options{Force: true})
	require.NoError(t, err)
	content, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	assert.Equal(t, "# repo rules", string(content), "--force never overwrites AGENTS.md")
}

// Reading a Values key that no values file declares fails the render, so the
// starter main.ct may only read keys its values.json declares.
func TestInit_MainCtReadsOnlyDeclaredValues(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)

	mainCt, err := os.ReadFile(filepath.Join(dir, "main.ct"))
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(dir, "values.json"))
	require.NoError(t, err)
	var values map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &values))

	reads := regexp.MustCompile(`Values\.(\w+)`).FindAllStringSubmatch(string(mainCt), -1)
	require.NotEmpty(t, reads)
	for _, m := range reads {
		assert.Contains(t, values, m[1], "main.ct reads Values.%s, which values.json does not declare", m[1])
	}
}

func TestInit_ClaudeMdOnlyImportsAgentsMd(t *testing.T) {
	dir := t.TempDir()
	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, "@AGENTS.md\n", string(content))
}

func TestInit_KeepsAnExistingClaudeMd(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# my claude rules"), 0o644))

	for _, force := range []bool{false, true} {
		written, err := scaffold.Init(dir, scaffold.Options{Force: force})
		require.NoError(t, err)
		assert.NotContains(t, written, "CLAUDE.md")
		content, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
		assert.Equal(t, "# my claude rules", string(content), "force=%v", force)
	}
}

func TestInit_AgentsMdCoversTheWorkLoop(t *testing.T) {
	dir := t.TempDir()
	_, err := scaffold.Init(dir, scaffold.Options{})
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err)
	agents := string(raw)

	for _, want := range []string{
		"## Work loop", "ct check", "~/.ct/cache/", "ct explain",
		"## Exit codes", "## Diagnostic codes",
		"unknown-values-key", "null", "Release.name", "Release.namespace",
		"--context", "--validate=false",
	} {
		assert.Contains(t, agents, want)
	}
}
