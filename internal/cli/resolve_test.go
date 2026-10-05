package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSourceDir_LocalPathPassThrough(t *testing.T) {
	origResolve := cacheResolveFn
	origInvalidate := cacheInvalidateFn
	t.Cleanup(func() {
		cacheResolveFn = origResolve
		cacheInvalidateFn = origInvalidate
	})

	cacheResolveFn = func(string) (string, error) {
		t.Fatal("cache resolve should not be called for local paths")
		return "", nil
	}
	cacheInvalidateFn = func(string) error {
		t.Fatal("cache invalidate should not be called for local paths")
		return nil
	}

	got, err := resolveSourceDir("./my-app", false)
	require.NoError(t, err)
	assert.Equal(t, "./my-app", got)
}

func TestResolveSourceDir_GitPackageResolvesFromCache(t *testing.T) {
	origResolve := cacheResolveFn
	origInvalidate := cacheInvalidateFn
	t.Cleanup(func() {
		cacheResolveFn = origResolve
		cacheInvalidateFn = origInvalidate
	})

	var gotURL string
	cacheResolveFn = func(rawURL string) (string, error) {
		gotURL = rawURL
		return "/tmp/cache/github.com/cloudticon/my-app@v1.0", nil
	}
	cacheInvalidateFn = func(string) error {
		t.Fatal("cache invalidate should not be called when no-cache=false")
		return nil
	}

	got, err := resolveSourceDir("github.com/cloudticon/my-app@v1.0/charts/api", false)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/cloudticon/my-app@v1.0", gotURL)
	assert.Equal(t, filepath.Join("/tmp/cache/github.com/cloudticon/my-app@v1.0", "charts/api"), got)
}

func TestResolveSourceDir_NoCacheInvalidatesBeforeResolve(t *testing.T) {
	origResolve := cacheResolveFn
	origInvalidate := cacheInvalidateFn
	t.Cleanup(func() {
		cacheResolveFn = origResolve
		cacheInvalidateFn = origInvalidate
	})

	var calls []string
	cacheInvalidateFn = func(rawURL string) error {
		calls = append(calls, "invalidate:"+rawURL)
		return nil
	}
	cacheResolveFn = func(rawURL string) (string, error) {
		calls = append(calls, "resolve:"+rawURL)
		return "/tmp/cache/pkg", nil
	}

	got, err := resolveSourceDir("github.com/cloudticon/my-app", true)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/cache/pkg", got)
	assert.Equal(t, []string{
		"invalidate:https://github.com/cloudticon/my-app",
		"resolve:https://github.com/cloudticon/my-app",
	}, calls)
}

func TestResolveSourceDir_NoCacheInvalidateError(t *testing.T) {
	origResolve := cacheResolveFn
	origInvalidate := cacheInvalidateFn
	t.Cleanup(func() {
		cacheResolveFn = origResolve
		cacheInvalidateFn = origInvalidate
	})

	cacheInvalidateFn = func(string) error {
		return errors.New("boom")
	}
	cacheResolveFn = func(string) (string, error) {
		t.Fatal("cache resolve should not be called when invalidate fails")
		return "", nil
	}

	_, err := resolveSourceDir("github.com/cloudticon/my-app", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalidating cache")
}

func TestRemoteSourceURL(t *testing.T) {
	local := filepath.Join(t.TempDir(), "my.app", "deploy")
	require.NoError(t, os.MkdirAll(local, 0o755))

	assert.Equal(t, "", remoteSourceURL(local), "an existing directory is local even if it looks like a host")
	assert.Equal(t, "", remoteSourceURL("."))
	assert.Equal(t, "https://github.com/acme/infra@v1", remoteSourceURL("github.com/acme/infra@v1/deploy"))
}
