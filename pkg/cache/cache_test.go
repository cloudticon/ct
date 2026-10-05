package cache_test

import (
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

func TestParsePackageURL_Invalid(t *testing.T) {
	tests := []string{
		"github.com/owner/repo",
		"https://github.com/owner",
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
