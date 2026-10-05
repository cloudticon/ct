package packages_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/pkg/packages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseImportsFromSource_SingleImport(t *testing.T) {
	src := `import { deployment } from "ctts/k8s/apps/v1";`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 1)
	assert.Equal(t, "ctts/k8s/apps/v1", imports[0].Path)
}

func TestParseImportsFromSource_MultipleImports(t *testing.T) {
	src := `
import { deployment } from "ctts/k8s/apps/v1";
import { webApp } from "github.com/someone/ctts-webapp";
import { redis } from "github.com/someone/ctts-redis/presets/ha";
`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 3)
	assert.Equal(t, "ctts/k8s/apps/v1", imports[0].Path)
	assert.Equal(t, "github.com/someone/ctts-webapp", imports[1].Path)
	assert.Equal(t, "github.com/someone/ctts-redis/presets/ha", imports[2].Path)
}

func TestParseImportsFromSource_MultilineImport(t *testing.T) {
	src := `
import {
  deployment,
  statefulSet,
} from "ctts/k8s/apps/v1";
`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 1)
	assert.Equal(t, "ctts/k8s/apps/v1", imports[0].Path)
}

func TestParseImportsFromSource_ReExport(t *testing.T) {
	src := `export { helper } from "github.com/someone/ctts-utils";`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 1)
	assert.Equal(t, "github.com/someone/ctts-utils", imports[0].Path)
}

func TestParseImportsFromSource_DefaultImport(t *testing.T) {
	src := `import defaults from "./lib/defaults";`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 1)
	assert.Equal(t, "./lib/defaults", imports[0].Path)
}

func TestParseImportsFromSource_StarImport(t *testing.T) {
	src := `import * as utils from "github.com/someone/ctts-utils";`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 1)
	assert.Equal(t, "github.com/someone/ctts-utils", imports[0].Path)
}

func TestParseImportsFromSource_NoImports(t *testing.T) {
	src := `const x = 1; console.log(x);`
	imports := packages.ParseImportsFromSource(src)
	assert.Empty(t, imports)
}

func TestParseImports_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.ts")
	require.NoError(t, os.WriteFile(f, []byte(`import { x } from "github.com/a/b";`), 0o644))

	imports, err := packages.ParseImports(f)
	require.NoError(t, err)
	require.Len(t, imports, 1)
	assert.Equal(t, "github.com/a/b", imports[0].Path)
}

func TestParseImports_FileNotFound(t *testing.T) {
	_, err := packages.ParseImports("/nonexistent/file.ts")
	assert.Error(t, err)
}

func TestIsGitPackage(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"github.com/owner/repo", true},
		{"github.com/owner/repo/lib/helpers", true},
		{"gitlab.com/org/pkg", true},
		{"custom.dev/mylib", true},
		{"ctts/k8s/apps/v1", false},
		{"./lib/helpers", false},
		{"../utils", false},
		{"bare-name", false},
		{"no-dot/but-slash", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, packages.IsGitPackage(tt.path))
		})
	}
}

func TestSplitPackagePath(t *testing.T) {
	tests := []struct {
		input   string
		wantPkg string
		wantSub string
	}{
		{"github.com/owner/repo", "github.com/owner/repo", ""},
		{"github.com/owner/repo/lib/helpers", "github.com/owner/repo", "lib/helpers"},
		{"github.com/owner/repo/index", "github.com/owner/repo", "index"},
		{"gitlab.com/org/pkg/sub", "gitlab.com/org/pkg", "sub"},
		{"custom.dev/mylib", "custom.dev/mylib", ""},
		{"custom.dev/mylib/utils", "custom.dev/mylib", "utils"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			pkg, sub := packages.SplitPackagePath(tt.input)
			assert.Equal(t, tt.wantPkg, pkg)
			assert.Equal(t, tt.wantSub, sub)
		})
	}
}

func TestParseImportsFromSource_TypeImport(t *testing.T) {
	src := `import type { Config } from "github.com/someone/ctts-types";`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 1)
	assert.Equal(t, "github.com/someone/ctts-types", imports[0].Path)
}

func TestParseImportsFromSource_SingleQuotes(t *testing.T) {
	src := `import { helper } from 'github.com/someone/ctts-utils';`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 1)
	assert.Equal(t, "github.com/someone/ctts-utils", imports[0].Path)
}

func TestParseImportsFromSource_MixedQuotes(t *testing.T) {
	src := `
import { a } from "github.com/someone/pkg-a";
import { b } from 'github.com/someone/pkg-b';
`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 2)
	assert.Equal(t, "github.com/someone/pkg-a", imports[0].Path)
	assert.Equal(t, "github.com/someone/pkg-b", imports[1].Path)
}

func TestParseImportsFromSource_DynamicImportIgnored(t *testing.T) {
	src := `const mod = await import("github.com/someone/dynamic-pkg");`
	imports := packages.ParseImportsFromSource(src)
	assert.Empty(t, imports, "dynamic imports should not be matched")
}

func TestParseImportsFromSource_RequireIgnored(t *testing.T) {
	src := `const x = require("github.com/someone/cjs-pkg");`
	imports := packages.ParseImportsFromSource(src)
	assert.Empty(t, imports, "require calls should not be matched")
}

func TestParseImportsFromSource_EmptySource(t *testing.T) {
	imports := packages.ParseImportsFromSource("")
	assert.Empty(t, imports)
}

func TestParseImportsFromSource_MultilineMultipleSpecifiers(t *testing.T) {
	src := `
import {
  deployment,
  statefulSet,
  daemonSet,
} from "ctts/k8s/apps/v1";
import {
  service,
  configMap,
  secret,
  namespace,
} from "ctts/k8s/core/v1";
`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 2)
	assert.Equal(t, "ctts/k8s/apps/v1", imports[0].Path)
	assert.Equal(t, "ctts/k8s/core/v1", imports[1].Path)
}

func TestParseImportsFromSource_ExportAllFrom(t *testing.T) {
	src := `export * from "github.com/someone/ctts-utils";`
	imports := packages.ParseImportsFromSource(src)
	require.Len(t, imports, 1)
	assert.Equal(t, "github.com/someone/ctts-utils", imports[0].Path)
}

func TestIsGitPackage_AdditionalCases(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"bitbucket.org/team/repo", true},
		{"bitbucket.org/team/repo/sub/path", true},
		{"my-registry.io/pkg", true},
		{"", false},
		{"ctts/resource", false},
		{"lodash", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, packages.IsGitPackage(tt.path))
		})
	}
}

func TestSplitPackagePath_BitbucketOrg(t *testing.T) {
	pkg, sub := packages.SplitPackagePath("bitbucket.org/team/repo/lib/utils")
	assert.Equal(t, "bitbucket.org/team/repo", pkg)
	assert.Equal(t, "lib/utils", sub)
}

func TestSplitPackageVersion(t *testing.T) {
	tests := []struct {
		input       string
		wantPkg     string
		wantVersion string
	}{
		{"github.com/owner/repo@v1.0.0", "github.com/owner/repo", "v1.0.0"},
		{"github.com/owner/repo@master", "github.com/owner/repo", "master"},
		{"github.com/owner/repo", "github.com/owner/repo", ""},
		{"custom.dev/mylib@0.1.0-beta", "custom.dev/mylib", "0.1.0-beta"},
		{"custom.dev/mylib", "custom.dev/mylib", ""},
		{"repo@tag", "repo", "tag"},
		{"repo", "repo", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			pkg, version := packages.SplitPackageVersion(tt.input)
			assert.Equal(t, tt.wantPkg, pkg)
			assert.Equal(t, tt.wantVersion, version)
		})
	}
}

func TestSplitPackagePath_DeepSubpath(t *testing.T) {
	pkg, sub := packages.SplitPackagePath("github.com/owner/repo/a/b/c/d")
	assert.Equal(t, "github.com/owner/repo", pkg)
	assert.Equal(t, "a/b/c/d", sub)
}

func TestIsURLImport(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"https://github.com/cloudticon/k8s@4.17.21", true},
		{"https://gitlab.com/org/lib@v1.0.0", true},
		{"http://github.com/owner/repo@v1", false},
		{"github.com/owner/repo", false},
		{"ctts/k8s/apps/v1", false},
		{"./lib/helpers", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, packages.IsURLImport(tt.path))
		})
	}
}

func TestPackageToGitURL(t *testing.T) {
	tests := []struct {
		pkg  string
		want string
	}{
		{"github.com/someone/ctts-webapp", "https://github.com/someone/ctts-webapp.git"},
		{"gitlab.com/org/lib", "https://gitlab.com/org/lib.git"},
		{"bitbucket.org/team/repo", "https://bitbucket.org/team/repo.git"},
		{"custom.dev/mylib", "https://custom.dev/mylib.git"},
	}
	for _, tt := range tests {
		t.Run(tt.pkg, func(t *testing.T) {
			assert.Equal(t, tt.want, packages.PackageToGitURL(tt.pkg))
		})
	}
}
