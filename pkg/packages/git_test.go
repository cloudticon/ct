package packages_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudticon/ct/pkg/packages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initBareRepo(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "remote.git")
	run(t, "git", "init", "--bare", bare)

	work := filepath.Join(t.TempDir(), "work")
	run(t, "git", "clone", bare, work)
	run(t, "git", "-C", work, "config", "user.email", "test@test.com")
	run(t, "git", "-C", work, "config", "user.name", "test")

	require.NoError(t, os.WriteFile(filepath.Join(work, "index.ts"), []byte(`export const x = 1;`), 0o644))
	run(t, "git", "-C", work, "add", ".")
	run(t, "git", "-C", work, "commit", "-m", "initial")
	run(t, "git", "-C", work, "push", "origin", "HEAD")
	run(t, "git", "-C", work, "tag", "v1.0.0")
	run(t, "git", "-C", work, "push", "origin", "v1.0.0")

	return bare
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "command %s %v failed: %s", name, args, string(out))
	return strings.TrimSpace(string(out))
}

func TestClone_DefaultBranch(t *testing.T) {
	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "pkg")

	git := packages.NewGitClient()
	sha, err := git.Clone(bare, "", dest)
	require.NoError(t, err)

	assert.Len(t, sha, 40)
	assert.FileExists(t, filepath.Join(dest, "index.ts"))
}

func TestClone_WithTag(t *testing.T) {
	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "pkg")

	git := packages.NewGitClient()
	sha, err := git.Clone(bare, "v1.0.0", dest)
	require.NoError(t, err)

	assert.Len(t, sha, 40)
	assert.FileExists(t, filepath.Join(dest, "index.ts"))
}

func TestClone_InvalidURL(t *testing.T) {
	git := packages.NewGitClient()
	_, err := git.Clone("/nonexistent/repo.git", "", t.TempDir())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "git clone")
}

func TestFetchSHA_DefaultBranch(t *testing.T) {
	bare := initBareRepo(t)

	git := packages.NewGitClient()
	sha, err := git.FetchSHA(bare, "")
	require.NoError(t, err)
	assert.Len(t, sha, 40)
}

func TestFetchSHA_Tag(t *testing.T) {
	bare := initBareRepo(t)

	git := packages.NewGitClient()
	sha, err := git.FetchSHA(bare, "v1.0.0")
	require.NoError(t, err)
	assert.Len(t, sha, 40)
}

func TestFetchSHA_InvalidRef(t *testing.T) {
	bare := initBareRepo(t)

	git := packages.NewGitClient()
	_, err := git.FetchSHA(bare, "nonexistent-ref")
	assert.Error(t, err)
}
