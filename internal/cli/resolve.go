package cli

import (
	"fmt"
	"path/filepath"

	"github.com/cloudticon/ct/pkg/cache"
	"github.com/cloudticon/ct/pkg/packages"
)

var (
	cacheResolveFn    = cache.Resolve
	cacheInvalidateFn = cache.Invalidate
)

func resolveSourceDir(arg string, noCache bool) (string, error) {
	if !packages.IsGitPackage(arg) {
		return arg, nil
	}

	pkgWithVersion, subPath := packages.SplitPackagePath(arg)
	pkg, version := packages.SplitPackageVersion(pkgWithVersion)

	url := "https://" + pkg
	if version != "" {
		url += "@" + version
	}

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
