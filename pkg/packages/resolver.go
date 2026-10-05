package packages

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/cloudticon/ct/pkg/cache"
)

func IsURLImport(importPath string) bool {
	return strings.HasPrefix(importPath, "https://")
}

type Import struct {
	Path string
}

var importRegex = regexp.MustCompile(`(?s)(?:import|export)\s.*?from\s*["']([^"']+)["']`)

func ParseImports(filePath string) ([]Import, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filePath, err)
	}
	return ParseImportsFromSource(string(data)), nil
}

func ParseImportsFromSource(source string) []Import {
	matches := importRegex.FindAllStringSubmatch(source, -1)
	imports := make([]Import, 0, len(matches))
	for _, m := range matches {
		imports = append(imports, Import{Path: m[1]})
	}
	return imports
}

func IsGitPackage(importPath string) bool {
	if strings.HasPrefix(importPath, ".") || strings.HasPrefix(importPath, "ctts/") {
		return false
	}
	parts := strings.SplitN(importPath, "/", 2)
	if len(parts) < 2 {
		return false
	}
	return strings.Contains(parts[0], ".")
}

func SplitPackagePath(importPath string) (pkgName, subPath string) {
	parts := strings.Split(importPath, "/")
	n := packageSegmentCount(parts[0])
	// A version marks where the package ends, which also covers hosts with
	// nested groups: "git.example.com/group/team/repo@v1/lib".
	for i := 1; i < len(parts); i++ {
		if strings.Contains(parts[i], "@") {
			n = i + 1
			break
		}
	}
	if len(parts) <= n {
		return importPath, ""
	}
	return strings.Join(parts[:n], "/"), strings.Join(parts[n:], "/")
}

func SplitPackageVersion(pkgName string) (pkg, version string) {
	pkg, version, _ = strings.Cut(pkgName, "@")
	return
}

// ImportURL maps an import path to the package URL the cache understands:
// "github.com/o/r@v1/sub" and "https://github.com/o/r@v1" both become
// "https://github.com/o/r@v1". Relative and bare imports return false.
func ImportURL(importPath string) (string, bool) {
	if IsURLImport(importPath) {
		return importPath, true
	}
	if !IsGitPackage(importPath) {
		return "", false
	}
	pkgWithVersion, _ := SplitPackagePath(importPath)
	pkg, version := SplitPackageVersion(pkgWithVersion)
	url := "https://" + pkg
	if version != "" {
		url += "@" + version
	}
	return url, true
}

func PackageToGitURL(pkgName string) string {
	return "https://" + pkgName + ".git"
}

func packageSegmentCount(domain string) int {
	if cache.IsWellKnownHost(domain) {
		return 3
	}
	return 2
}
