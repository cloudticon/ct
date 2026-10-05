package packages_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/pkg/packages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncPackages_NoURLImports(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	require.NoError(t, os.WriteFile(entry, []byte(`const x = 1;`), 0o644))

	err := packages.SyncPackages(dir)
	assert.NoError(t, err)
}

func TestSyncPackages_OnlyRelativeImports(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { helper } from "./lib/helpers";
const x = 1;
`), 0o644))

	err := packages.SyncPackages(dir)
	assert.NoError(t, err)
}

func TestSyncPackages_MissingEntryPoint(t *testing.T) {
	dir := t.TempDir()

	err := packages.SyncPackages(dir)
	assert.NoError(t, err, "missing entry point should not error")
}

func TestSyncPackages_FollowsGitImportsIntoCtFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := filepath.Join(home, ".ct", "cache", "github.com", "acme", "factories@v1")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	// The package's own .ct file imports something unresolvable; reaching it
	// proves the github.com/... import and the .ct file were both followed.
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "index.ct"),
		[]byte(`import { x } from "https://github.com/acme";`), 0o644))

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"),
		[]byte(`import { webApp } from "github.com/acme/factories@v1";`), 0o644))

	err := packages.SyncPackages(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https://github.com/acme")
}

func TestImportURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/o/r@v1":     "https://github.com/o/r@v1",
		"github.com/o/r":                "https://github.com/o/r",
		"github.com/o/r@v1.2.0/sub/x":   "https://github.com/o/r@v1.2.0",
		"gitlab.example.com/o/r@main/x": "https://gitlab.example.com/o/r@main",
		"custom.dev/mylib/utils":        "https://custom.dev/mylib",
	} {
		got, ok := packages.ImportURL(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"./x", "../x", "lodash", "@scope/pkg"} {
		_, ok := packages.ImportURL(in)
		assert.False(t, ok, in)
	}
}
