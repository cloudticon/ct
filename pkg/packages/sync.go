package packages

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudticon/ct/pkg/cache"
)

func SyncPackages(projectDir string) error {
	entryPoint := filepath.Join(projectDir, "main.ct")
	visited := make(map[string]bool)
	return syncImports(entryPoint, visited)
}

func syncImports(filePath string, visited map[string]bool) error {
	if visited[filePath] {
		return nil
	}
	visited[filePath] = true

	imports, err := ParseImports(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("parsing imports from %s: %w", filePath, err)
	}

	for _, imp := range imports {
		url, ok := ImportURL(imp.Path)
		if !ok {
			continue
		}

		pkgDir, err := cache.Resolve(url)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", imp.Path, err)
		}

		sourceFiles, _ := collectSourceFiles(pkgDir)
		for _, f := range sourceFiles {
			if err := syncImports(f, visited); err != nil {
				return err
			}
		}
	}

	return nil
}

// collectSourceFiles lists the .ts and .ct files of a package.
func collectSourceFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".ct")) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}
