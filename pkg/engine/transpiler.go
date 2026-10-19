package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/cloudticon/ct/pkg/cache"
	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/packages"
	"github.com/evanw/esbuild/pkg/api"
)

type Transpiler struct {
	projectDir string
	// RefreshPackages re-downloads every imported package once instead of
	// trusting the cache; branch versions like @master otherwise stay at
	// whatever was fetched first.
	RefreshPackages bool

	mu        sync.Mutex
	refreshed map[string]bool
}

// MarkFresh records that a package URL was already re-downloaded in this run
// (e.g. the remote source being rendered), so RefreshPackages doesn't delete
// it again while it is being bundled.
func (t *Transpiler) MarkFresh(url string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refreshed == nil {
		t.refreshed = map[string]bool{}
	}
	t.refreshed[url] = true
}

func NewTranspiler(projectDir string) *Transpiler {
	if projectDir != "" {
		if abs, err := filepath.Abs(projectDir); err == nil {
			projectDir = abs
		}
	}
	return &Transpiler{projectDir: projectDir}
}

// Bundle bundles entryPoint and its imports into one IIFE script with an
// inline source map, so runtime errors point at .ct lines. Failures are
// returned as diag.List with file:line:column positions.
func (t *Transpiler) Bundle(entryPoint string) (string, error) {
	absEntry, err := filepath.Abs(entryPoint)
	if err != nil {
		return "", diag.Errorf(diag.CodeImport, "resolving entry point %s: %v", entryPoint, err)
	}
	baseDir := t.projectDir
	if baseDir == "" {
		baseDir = filepath.Dir(absEntry)
	}

	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{absEntry},
		AbsWorkingDir:     baseDir,
		Bundle:            true,
		Format:            api.FormatIIFE,
		Platform:          api.PlatformNeutral,
		Write:             false,
		Sourcemap:         api.SourceMapInline,
		SourcesContent:    api.SourcesContentExclude,
		ResolveExtensions: []string{".ts", ".ct", ".tsx", ".js", ".json"},
		Loader: map[string]api.Loader{
			".ts": api.LoaderTS,
			".ct": api.LoaderTS,
		},
		Plugins: []api.Plugin{asyncDetectPlugin(), t.urlResolverPlugin()},
	})

	if len(result.Errors) > 0 {
		return "", t.bundleDiagnostics(result.Errors, baseDir)
	}
	if len(result.OutputFiles) == 0 {
		return "", diag.Errorf(diag.CodeSyntax, "esbuild produced no output for %s", entryPoint)
	}
	return string(result.OutputFiles[0].Contents), nil
}

// asyncFeaturesOff makes esbuild's parser reject every async construct with
// a precise location. Generators are turned off too, otherwise esbuild would
// quietly lower async functions to generators instead of failing.
var asyncFeaturesOff = map[string]bool{
	"async-await":     false,
	"async-generator": false,
	"for-await":       false,
	"top-level-await": false,
	"generator":       false,
}

const asyncMessage = "async/await is not supported: .ct code runs synchronously"

// asyncDetectPlugin rejects async/await in .ct/.ts files. It parses each file
// instead of grepping, so comments, strings and names like "async-worker"
// don't trip it.
func asyncDetectPlugin() api.Plugin {
	return api.Plugin{
		Name: "async-detect",
		Setup: func(build api.PluginBuild) {
			build.OnLoad(api.OnLoadOptions{Filter: `\.(ct|ts)$`},
				func(args api.OnLoadArgs) (api.OnLoadResult, error) {
					data, err := os.ReadFile(args.Path)
					if err != nil {
						return api.OnLoadResult{}, nil
					}
					contents := string(data)

					check := api.Transform(contents, api.TransformOptions{
						Loader:     api.LoaderTS,
						Sourcefile: args.Path,
						Supported:  asyncFeaturesOff,
					})
					var errs []api.Message
					for _, m := range check.Errors {
						if isAsyncError(m.Text) {
							errs = append(errs, api.Message{Text: asyncMessage, Location: m.Location})
						}
					}
					if len(errs) > 0 {
						return api.OnLoadResult{Errors: errs}, nil
					}
					return api.OnLoadResult{Contents: &contents, Loader: api.LoaderTS}, nil
				})
		},
	}
}

// isAsyncError matches esbuild's messages for async constructs it was told
// not to support. Other errors (plain syntax errors) are left to the main
// build, which reports them as they are.
func isAsyncError(text string) bool {
	return strings.HasPrefix(text, "Transforming async") ||
		strings.HasPrefix(text, "Transforming for-await") ||
		strings.HasPrefix(text, "Top-level await")
}

func (t *Transpiler) urlResolverPlugin() api.Plugin {
	// esbuild resolves imports in parallel. When refreshing, invalidate and
	// re-download under one lock so no resolver hands out a directory that
	// another one is deleting.
	resolve := func(url string) (string, error) {
		if !t.RefreshPackages {
			return cache.Resolve(url)
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.refreshed == nil {
			t.refreshed = map[string]bool{}
		}
		if !t.refreshed[url] {
			t.refreshed[url] = true
			if err := cache.Invalidate(url); err != nil {
				return "", err
			}
		}
		return cache.Resolve(url)
	}

	return api.Plugin{
		Name: "url-resolver",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `^https://`},
				func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					pkgDir, err := resolve(args.Path)
					if err != nil {
						return api.OnResolveResult{}, err
					}
					return api.OnResolveResult{
						Path: resolveFilePath(pkgDir, ""),
					}, nil
				})

			build.OnResolve(api.OnResolveOptions{Filter: `^[a-zA-Z0-9]`},
				func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					url, ok := packages.ImportURL(args.Path)
					if !ok {
						return api.OnResolveResult{}, nil
					}
					_, subPath := packages.SplitPackagePath(args.Path)
					if slices.Contains(strings.Split(subPath, "/"), "..") {
						return api.OnResolveResult{}, fmt.Errorf("import %q leaves its package", args.Path)
					}

					pkgDir, err := resolve(url)
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

func (t *Transpiler) bundleDiagnostics(msgs []api.Message, baseDir string) diag.List {
	list := make(diag.List, 0, len(msgs))
	for _, m := range msgs {
		d := diag.Diagnostic{Code: diag.CodeSyntax, Message: m.Text}
		switch {
		case m.Text == asyncMessage:
			d.Code = diag.CodeAsync
			d.Hint = "rendering is synchronous; call functions directly and drop async/await"
		case m.PluginName == "url-resolver":
			d.Code = diag.CodeImport
			d.Hint = "package addresses look like github.com/<owner>/<repo>@<tag-or-branch>"
		case noExportRe.MatchString(m.Text):
			d = t.missingExportDiagnostic(m.Text, baseDir)
		case strings.HasPrefix(m.Text, "Could not resolve"):
			d.Code = diag.CodeImport
			d.Hint = importHint(m.Text)
		}
		if loc := m.Location; loc != nil {
			d.File = DisplayPath(loc.File, baseDir)
			d.Line = loc.Line
			d.Column = loc.Column + 1
			d.LineText = loc.LineText
		}
		list = append(list, d)
	}
	return list
}

func importHint(text string) string {
	spec := text
	if i := strings.IndexByte(text, '"'); i >= 0 {
		spec = strings.Trim(text[i:], `"`)
		if j := strings.IndexByte(spec, '"'); j >= 0 {
			spec = spec[:j]
		}
	}
	if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
		return "paths are relative to the importing file; .ts and .ct extensions are added automatically"
	}
	return "ct doesn't use npm; import packages by URL, e.g. github.com/cloudticon/k8s@master"
}

// DisplayPath shortens a source path for messages: files in the project
// become relative ("lib/app.ct"), files from the package cache become their
// import path ("github.com/cloudticon/k8s@master/resource.ts").
func DisplayPath(path, baseDir string) string {
	if path == "" {
		return path
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(baseDir, path)
	}
	// The project first: a remote source is itself inside the cache.
	if baseDir != "" {
		if rel, err := filepath.Rel(baseDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if cacheDir, err := cache.CacheDir(); err == nil {
		if rel, err := filepath.Rel(cacheDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return abs
}

var noExportRe = regexp.MustCompile(`^No matching export in "(.*)" for import "(.*)"$`)

// missingExportDiagnostic rewrites esbuild's "No matching export" (which
// names the file in the cache and carries the code "syntax") into an import
// diagnostic that names the package, suggests the closest export and lists
// what the module exports.
func (t *Transpiler) missingExportDiagnostic(text, baseDir string) diag.Diagnostic {
	m := noExportRe.FindStringSubmatch(text)
	file, name := m[1], m[2]
	if !filepath.IsAbs(file) {
		file = filepath.Join(baseDir, file)
	}
	where := DisplayPath(file, baseDir)
	inCache := false
	if cacheDir, err := cache.CacheDir(); err == nil {
		if rel, err := filepath.Rel(cacheDir, file); err == nil && !strings.HasPrefix(rel, "..") {
			inCache = true
		}
	}
	if inCache {
		where = packageAddress(where)
	}

	d := diag.Diagnostic{Code: diag.CodeImport}
	if inCache {
		d.Message = fmt.Sprintf("package %s has no export %q", where, name)
	} else {
		d.Message = fmt.Sprintf("%s has no export %q", where, name)
	}
	exports := t.moduleExports(file)
	if len(exports) == 0 {
		d.Hint = "the module exports nothing by that name; check the import against the package's index file"
		return d
	}
	if best := closestName(name, exports); best != "" {
		d.Hint = fmt.Sprintf("did you mean %q? ", best)
	}
	d.Hint += "exports: " + strings.Join(exports, ", ")
	return d
}

// packageAddress cuts a cache-relative path down to the package:
// "github.com/o/r@v1/lib/x.ts" becomes "github.com/o/r@v1".
func packageAddress(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if strings.Contains(p, "@") {
			return strings.Join(parts[:i+1], "/")
		}
	}
	return rel
}

// moduleExports lists the names a module exports (following re-exports) by
// bundling it once more as an ES module and reading esbuild's metafile.
func (t *Transpiler) moduleExports(file string) []string {
	res := api.Build(api.BuildOptions{
		EntryPoints:       []string{file},
		AbsWorkingDir:     filepath.Dir(file),
		Bundle:            true,
		Format:            api.FormatESModule,
		Platform:          api.PlatformNeutral,
		Write:             false,
		Metafile:          true,
		LogLevel:          api.LogLevelSilent,
		ResolveExtensions: []string{".ts", ".ct", ".tsx", ".js", ".json"},
		Loader:            map[string]api.Loader{".ts": api.LoaderTS, ".ct": api.LoaderTS},
		Plugins:           []api.Plugin{t.urlResolverPlugin()},
	})
	if res.Metafile == "" {
		return nil
	}
	var meta struct {
		Outputs map[string]struct {
			Exports []string `json:"exports"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(res.Metafile), &meta); err != nil {
		return nil
	}
	var names []string
	for _, out := range meta.Outputs {
		names = append(names, out.Exports...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// closestName returns the candidate nearest to name, or "" when nothing is
// close enough to be a likely typo.
func closestName(name string, candidates []string) string {
	best, bestDist := "", 1<<30
	lower := strings.ToLower(name)
	for _, c := range candidates {
		dist := levenshtein(lower, strings.ToLower(c))
		if dist < bestDist {
			best, bestDist = c, dist
		}
	}
	limit := max(len(name)/3, 2)
	if bestDist > limit {
		return ""
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
