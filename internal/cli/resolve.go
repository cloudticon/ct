package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudticon/ct/pkg/cache"
	"github.com/cloudticon/ct/pkg/packages"
)

var (
	cacheResolveFn    = cache.Resolve
	cacheInvalidateFn = cache.Invalidate
)

func resolveSourceDir(arg string, noCache bool) (string, error) {
	if remoteSourceURL(arg) == "" {
		return arg, nil
	}

	url, _ := packages.ImportURL(arg)
	_, subPath := packages.SplitPackagePath(arg)

	if noCache {
		if err := cacheInvalidateFn(url); err != nil {
			return "", fmt.Errorf("invalidating cache for %s: %w", url, err)
		}
	}

	localDir, err := cacheResolveFn(url)
	if err != nil {
		return "", fmt.Errorf("resolving source %s: %w", arg, err)
	}

	return filepath.Join(localDir, subPath), nil
}

// remoteSourceURL returns the package URL for a remote source argument, or
// "" when arg is a local directory. An existing directory always wins, so a
// local path like "my.app/deploy" isn't mistaken for a package.
func remoteSourceURL(arg string) string {
	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		return ""
	}
	url, _ := packages.ImportURL(arg)
	return url
}
