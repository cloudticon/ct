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
		if !IsURLImport(imp.Path) {
			continue
		}

		pkgDir, err := cache.Resolve(imp.Path)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", imp.Path, err)
		}

		tsFiles, _ := collectTSFiles(pkgDir)
		for _, f := range tsFiles {
			if err := syncImports(f, visited); err != nil {
				return err
			}
		}
	}

	return nil
}

func collectTSFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".ts") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}
