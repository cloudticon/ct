package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cloudticon/ct/pkg/cache"
	"github.com/cloudticon/ct/pkg/packages"
	"github.com/evanw/esbuild/pkg/api"
)

type Transpiler struct {
	projectDir string
}

func NewTranspiler(projectDir string) *Transpiler {
	if projectDir != "" {
		if abs, err := filepath.Abs(projectDir); err == nil {
			projectDir = abs
		}
	}
	return &Transpiler{projectDir: projectDir}
}

func (t *Transpiler) Bundle(entryPoint string) (string, error) {
	result := api.Build(api.BuildOptions{
		EntryPoints: []string{entryPoint},
		Bundle:      true,
		Format:      api.FormatIIFE,
		Platform:    api.PlatformNeutral,
		Write:       false,
		Loader: map[string]api.Loader{
			".ts": api.LoaderTS,
			".ct": api.LoaderTS,
		},
		Plugins: []api.Plugin{t.asyncDetectPlugin(), t.urlResolverPlugin()},
	})

	if len(result.Errors) > 0 {
		msgs := make([]string, len(result.Errors))
		for i, e := range result.Errors {
			msgs[i] = e.Text
		}
		return "", fmt.Errorf("esbuild errors: %s", strings.Join(msgs, "; "))
	}

	if len(result.OutputFiles) == 0 {
		return "", fmt.Errorf("esbuild produced no output")
	}

	return string(result.OutputFiles[0].Contents), nil
}

var asyncAwaitRe = regexp.MustCompile(`\b(async|await)\b`)

func (t *Transpiler) asyncDetectPlugin() api.Plugin {
	return api.Plugin{
		Name: "async-detect",
		Setup: func(build api.PluginBuild) {
			build.OnLoad(api.OnLoadOptions{Filter: `\.(ct|ts)$`},
				func(args api.OnLoadArgs) (api.OnLoadResult, error) {
					data, err := os.ReadFile(args.Path)
					if err != nil {
						return api.OnLoadResult{}, nil
					}

					loc := asyncAwaitRe.FindIndex(data)
					if loc == nil {
						return api.OnLoadResult{}, nil
					}

					line := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
					keyword := string(data[loc[0]:loc[1]])
					return api.OnLoadResult{}, fmt.Errorf(
						"async/await is not supported ('%s' at line %d in %s); Goja runtime is synchronous, use sync alternatives",
						keyword, line, filepath.Base(args.Path),
					)
				})
		},
	}
}

func (t *Transpiler) urlResolverPlugin() api.Plugin {
	return api.Plugin{
		Name: "url-resolver",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `^https://`},
				func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					pkgDir, err := cache.Resolve(args.Path)
					if err != nil {
						return api.OnResolveResult{}, err
					}
					return api.OnResolveResult{
						Path: resolveFilePath(pkgDir, ""),
					}, nil
				})

			build.OnResolve(api.OnResolveOptions{Filter: `^[a-zA-Z0-9]`},
				func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					if !packages.IsGitPackage(args.Path) {
						return api.OnResolveResult{}, nil
					}

					pkgWithVersion, subPath := packages.SplitPackagePath(args.Path)
					pkg, version := packages.SplitPackageVersion(pkgWithVersion)

					url := "https://" + pkg
					if version != "" {
						url += "@" + version
					}

					pkgDir, err := cache.Resolve(url)
					if err != nil {
						return api.OnResolveResult{}, err
					}

					return api.OnResolveResult{
						Path: resolveFilePath(pkgDir, subPath),
					}, nil
				})
		},
	}
}

var entryExtensions = [...]string{".ts", ".ct"}

func resolveFilePath(pkgDir, subPath string) string {
	if subPath == "" {
		return resolveIndex(pkgDir)
	}
	for _, ext := range entryExtensions {
		f := filepath.Join(pkgDir, subPath+ext)
		if _, err := os.Stat(f); err == nil {
			return f
		}
	}
	return resolveIndex(filepath.Join(pkgDir, subPath))
}

func resolveIndex(dir string) string {
	for _, ext := range entryExtensions {
		f := filepath.Join(dir, "index"+ext)
		if _, err := os.Stat(f); err == nil {
			return f
		}
	}
	return filepath.Join(dir, "index.ts")
}
