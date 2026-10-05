package cache_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/pkg/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePackageURL_Valid(t *testing.T) {
	tests := []struct {
		url     string
		host    string
		owner   string
		repo    string
		version string
	}{
		{
			url:     "https://github.com/cloudticon/k8s@4.17.21",
			host:    "github.com",
			owner:   "cloudticon",
			repo:    "k8s",
			version: "4.17.21",
		},
		{
			url:     "https://gitlab.com/org/lib@v1.0.0",
			host:    "gitlab.com",
			owner:   "org",
			repo:    "lib",
			version: "v1.0.0",
		},
		{
			url:     "https://github.com/someone/my-pkg@0.1.0-beta",
			host:    "github.com",
			owner:   "someone",
			repo:    "my-pkg",
			version: "0.1.0-beta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			ref, err := cache.ParsePackageURL(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.host, ref.Host)
			assert.Equal(t, tt.owner, ref.Owner)
			assert.Equal(t, tt.repo, ref.Repo)
			assert.Equal(t, tt.version, ref.Version)
		})
	}
}

func TestParsePackageURL_NoVersion(t *testing.T) {
	tests := []struct {
		url   string
		host  string
		owner string
		repo  string
	}{
		{
			url:   "https://github.com/cloudticon/k8s",
			host:  "github.com",
			owner: "cloudticon",
			repo:  "k8s",
		},
		{
			url:   "https://gitlab.com/org/lib",
			host:  "gitlab.com",
			owner: "org",
			repo:  "lib",
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			ref, err := cache.ParsePackageURL(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.host, ref.Host)
			assert.Equal(t, tt.owner, ref.Owner)
			assert.Equal(t, tt.repo, ref.Repo)
			assert.Equal(t, "", ref.Version)
		})
	}
}

func TestParsePackageURL_CustomHosts(t *testing.T) {
	vanity, err := cache.ParsePackageURL("https://custom.dev/mylib@v1")
	require.NoError(t, err)
	assert.Equal(t, "https://custom.dev/mylib.git", vanity.GitURL())
	assert.Equal(t, filepath.Join("custom.dev", "mylib@v1"), vanity.CacheKey())

	nested, err := cache.ParsePackageURL("https://git.example.com/group/team/repo@main")
	require.NoError(t, err)
	assert.Equal(t, "group/team", nested.Owner)
	assert.Equal(t, "repo", nested.Repo)
	assert.Equal(t, "https://git.example.com/group/team/repo.git", nested.GitURL())
}

func TestParsePackageURL_Invalid(t *testing.T) {
	tests := []string{
		"github.com/owner/repo",
		"https://github.com/owner",
		"https://github.com/owner/repo/sub@v1",
		"https://custom.dev//x",
		"http://github.com/owner/repo@v1",
		"",
		"not-a-url",
	}

	for _, url := range tests {
		t.Run(url, func(t *testing.T) {
			_, err := cache.ParsePackageURL(url)
			assert.Error(t, err)
		})
	}
}

func TestPackageRef_CacheKey(t *testing.T) {
	ref := &cache.PackageRef{
		Host: "github.com", Owner: "cloudticon", Repo: "k8s", Version: "4.17.21",
	}
	assert.Equal(t, "github.com/cloudticon/k8s@4.17.21", ref.CacheKey())
}

func TestPackageRef_CacheKey_DefaultVersion(t *testing.T) {
	ref := &cache.PackageRef{
		Host: "github.com", Owner: "cloudticon", Repo: "k8s", Version: "",
	}
	assert.Equal(t, "github.com/cloudticon/k8s@_default", ref.CacheKey())
}

func TestPackageRef_GitURL(t *testing.T) {
	ref := &cache.PackageRef{
		Host: "github.com", Owner: "cloudticon", Repo: "k8s", Version: "4.17.21",
	}
	assert.Equal(t, "https://github.com/cloudticon/k8s.git", ref.GitURL())
}

func TestCacheDir_ReturnsPath(t *testing.T) {
	dir, err := cache.CacheDir()
	require.NoError(t, err)
	assert.Contains(t, dir, ".ct")
	assert.Contains(t, dir, "cache")
}

func stubClone(t *testing.T, fn func(ref *cache.PackageRef, dir string) error) *int {
	t.Helper()
	calls := 0
	restore := cache.SetCloneFunc(func(ref *cache.PackageRef, dir string) error {
		calls++
		return fn(ref, dir)
	})
	t.Cleanup(restore)
	return &calls
}

func TestResolve_DownloadsOnceAndStripsGitDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubClone(t, func(ref *cache.PackageRef, dir string) error {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o755))
		return os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export {}"), 0o644)
	})

	dir, err := cache.Resolve("https://github.com/acme/lib@v1")
	require.NoError(t, err)
	again, err := cache.Resolve("https://github.com/acme/lib@v1")
	require.NoError(t, err)

	assert.Equal(t, dir, again)
	assert.Equal(t, 1, *calls, "second resolve is served from cache")
	assert.FileExists(t, filepath.Join(dir, "index.ts"))
	assert.NoDirExists(t, filepath.Join(dir, ".git"))
	assert.DirExists(t, filepath.Join(dir, ".github"))
}

func TestResolve_FailedCloneLeavesNoCacheEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubClone(t, func(ref *cache.PackageRef, dir string) error {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "partial.ts"), []byte("x"), 0o644))
		return errors.New("network down")
	})

	_, err := cache.Resolve("https://github.com/acme/lib@v1")
	require.Error(t, err)

	cacheDir, _ := cache.CacheDir()
	entries, _ := os.ReadDir(filepath.Join(cacheDir, "github.com", "acme"))
	assert.Empty(t, entries, "no half-downloaded package and no temp dir left behind")
}

func TestResolve_ReplacesEmptyLeftoverDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cacheDir, _ := cache.CacheDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "github.com", "acme", "lib@v1"), 0o755))
	stubClone(t, func(ref *cache.PackageRef, dir string) error {
		return os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export {}"), 0o644)
	})

	dir, err := cache.Resolve("https://github.com/acme/lib@v1")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "index.ts"))
}

func TestInvalidate_ForcesRedownload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubClone(t, func(ref *cache.PackageRef, dir string) error {
		return os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export {}"), 0o644)
	})

	_, err := cache.Resolve("https://github.com/acme/lib@main")
	require.NoError(t, err)
	require.NoError(t, cache.Invalidate("https://github.com/acme/lib@main"))
	_, err = cache.Resolve("https://github.com/acme/lib@main")
	require.NoError(t, err)

	assert.Equal(t, 2, *calls)
}

func TestParsePackageURL_RejectsPathTraversal(t *testing.T) {
	for _, url := range []string{
		"https://github.com/o/r@x/../../../../tmp/victim",
		"https://github.com/o/r@..",
		"https://github.com/../r@v1",
		"https://custom.dev/../../etc@v1",
		"https://custom.dev/./x@v1",
		"https://../x/y@v1",
		"https://github.com/o/r@-upload-pack=evil",
		"https://github.com/o/r@v1/",
	} {
		_, err := cache.ParsePackageURL(url)
		assert.Error(t, err, url)
	}
}

func TestInvalidate_NeverLeavesTheCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	victim := filepath.Join(home, "victim")
	require.NoError(t, os.MkdirAll(victim, 0o755))

	err := cache.Invalidate("https://github.com/o/r@x/../../../../../victim")

	require.Error(t, err)
	assert.DirExists(t, victim)
}

func TestCacheKey_BranchWithSlashIsOneDirectory(t *testing.T) {
	ref, err := cache.ParsePackageURL("https://github.com/o/r@feature/new-ui")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("github.com", "o", "r@feature%2Fnew-ui"), ref.CacheKey())
}
