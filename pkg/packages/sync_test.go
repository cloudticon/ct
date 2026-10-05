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
