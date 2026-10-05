package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBundle_SimpleTS(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	err := os.WriteFile(entry, []byte(`console.log(42 as number);`), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "42")
	assert.NotContains(t, js, "as number")
}

func TestBundle_CtFileAsTS(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entry, []byte(`const x: number = 42; console.log(x);`), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "42")
	assert.NotContains(t, js, ": number")
}

func TestBundle_InvalidTS(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	err := os.WriteFile(entry, []byte(`this is not valid { typescript`), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	_, err = tr.Bundle(entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "main.ts:1:6")
	assert.Contains(t, err.Error(), "[syntax]")
}

func TestBundle_IIFEFormat(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	err := os.WriteFile(entry, []byte(`const x = 1;`), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)

	assert.Contains(t, js, "()")
}

func setupFakeCache(t *testing.T, host, owner, repo, version string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	cacheVersion := version
	if cacheVersion == "" {
		cacheVersion = "_default"
	}
	pkgDir := filepath.Join(home, ".ct", "cache", host, owner, repo+"@"+cacheVersion)
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	return pkgDir
}

func writeTS(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestBundle_BareGitImport(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "my-pkg", "")
	writeTS(t, pkgDir, "index.ts", `export const hello = "world";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { hello } from "github.com/someone/my-pkg";
console.log(hello);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "world")
}

func TestBundle_BareGitImportWithVersion(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "my-pkg", "v1.0.0")
	writeTS(t, pkgDir, "index.ts", `export const hello = "versioned";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { hello } from "github.com/someone/my-pkg@v1.0.0";
console.log(hello);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "versioned")
}

func TestBundle_BareGitImportSubpathFile(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "my-pkg", "")
	writeTS(t, pkgDir, "index.ts", `export const root = true;`)
	writeTS(t, pkgDir, "lib/helpers.ts", `export const helper = "from-file";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { helper } from "github.com/someone/my-pkg/lib/helpers";
console.log(helper);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "from-file")
}

func TestBundle_BareGitImportSubpathIndex(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "my-pkg", "")
	writeTS(t, pkgDir, "index.ts", `export const root = true;`)
	writeTS(t, pkgDir, "lib/utils/index.ts", `export const util = "from-index";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { util } from "github.com/someone/my-pkg/lib/utils";
console.log(util);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "from-index")
}

func TestBundle_BareGitImportVersionedSubpath(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "my-pkg", "master")
	writeTS(t, pkgDir, "index.ts", `export const root = true;`)
	writeTS(t, pkgDir, "apps/v1.ts", `export const app = "v1-app";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { app } from "github.com/someone/my-pkg@master/apps/v1";
console.log(app);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "v1-app")
}

func TestBundle_URLImportWithoutVersion(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "cloudticon", "k8s", "")
	writeTS(t, pkgDir, "index.ts", `export const k8s = "no-version";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { k8s } from "https://github.com/cloudticon/k8s";
console.log(k8s);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "no-version")
}

func TestBundle_CtEntryPoint(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "ct-pkg", "")
	writeTS(t, pkgDir, "index.ct", `export const greeting = "from-ct";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { greeting } from "github.com/someone/ct-pkg";
console.log(greeting);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "from-ct")
}

func TestBundle_CtEntryPointURL(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "ct-pkg", "v2")
	writeTS(t, pkgDir, "index.ct", `export const greeting = "ct-url";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { greeting } from "https://github.com/someone/ct-pkg@v2";
console.log(greeting);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "ct-url")
}

func TestBundle_CtSubpathFile(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "ct-pkg", "")
	writeTS(t, pkgDir, "index.ts", `export const root = true;`)
	writeTS(t, pkgDir, "lib/helpers.ct", `export const helper = "ct-helper";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { helper } from "github.com/someone/ct-pkg/lib/helpers";
console.log(helper);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "ct-helper")
}

func TestBundle_CtSubpathIndex(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "ct-pkg", "")
	writeTS(t, pkgDir, "index.ts", `export const root = true;`)
	writeTS(t, pkgDir, "lib/utils/index.ct", `export const util = "ct-index";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { util } from "github.com/someone/ct-pkg/lib/utils";
console.log(util);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "ct-index")
}

func TestBundle_RejectsAsyncFunction(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entry, []byte("async function fetchData() {\n  return \"data\";\n}\n"), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	_, err = tr.Bundle(entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "async")
	assert.Contains(t, err.Error(), "main.ct:1:1")
}

func TestBundle_RejectsAwait(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entry, []byte("const x = 1;\nconst data = await fetch(\"http://example.com\");\n"), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	_, err = tr.Bundle(entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "await")
	assert.Contains(t, err.Error(), "main.ct:2:14")
}

func TestBundle_RejectsAsyncArrow(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entry, []byte("const fn = async () => {};\n"), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	_, err = tr.Bundle(entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "async")
}

func TestBundle_AllowsSyncCode(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entry, []byte("const data = \"synchronous code\";\nconsole.log(data);\n"), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	assert.NoError(t, err)
	assert.Contains(t, js, "synchronous")
}

func TestBundle_RejectsAsyncInImportedFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "helper.ts"),
		[]byte("export async function load() { return 1; }\n"),
		0644,
	))
	entry := filepath.Join(dir, "main.ct")
	require.NoError(t, os.WriteFile(entry, []byte("import { load } from \"./helper\";\nload();\n"), 0644))

	tr := engine.NewTranspiler("")
	_, err := tr.Bundle(entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "async")
	assert.Contains(t, err.Error(), "helper.ts")
}

func TestBundle_TsPreferedOverCt(t *testing.T) {
	pkgDir := setupFakeCache(t, "github.com", "someone", "both-pkg", "")
	writeTS(t, pkgDir, "index.ts", `export const entry = "ts-wins";`)
	writeTS(t, pkgDir, "index.ct", `export const entry = "ct-loses";`)

	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ts")
	require.NoError(t, os.WriteFile(entry, []byte(`
import { entry } from "github.com/someone/both-pkg";
console.log(entry);
`), 0o644))

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "ts-wins")
}

func TestBundle_AsyncWordsInCommentsStringsAndNamesAreFine(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	require.NoError(t, os.WriteFile(entry, []byte(`// we never await anything here
/* async is mentioned in a comment */
const name = "async-worker";
const awaitingApproval = true;
const tpl = `+"`await ${name}`"+`;
function* gen() { yield 1; }
console.log(name, awaitingApproval, tpl, [...gen()]);
`), 0o644))

	_, err := engine.NewTranspiler(dir).Bundle(entry)
	require.NoError(t, err)
}

func TestBundle_ResolvesRelativeImportsWithoutExtension(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "lib/helper.ct", `export const helper = () => "from-ct";`)
	writeTS(t, dir, "lib/util/index.ts", `export const util = () => "from-index";`)
	entry := filepath.Join(dir, "main.ct")
	require.NoError(t, os.WriteFile(entry, []byte(`import { helper } from "./lib/helper";
import { util } from "./lib/util";
console.log(helper(), util());
`), 0o644))

	js, err := engine.NewTranspiler(dir).Bundle(entry)
	require.NoError(t, err)
	assert.Contains(t, js, "from-ct")
	assert.Contains(t, js, "from-index")
}

func TestBundle_ReportsUnresolvedImportsWithHints(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	require.NoError(t, os.WriteFile(entry, []byte(`import _ from "lodash";
import { x } from "./missing";
console.log(_, x);
`), 0o644))

	_, err := engine.NewTranspiler(dir).Bundle(entry)

	var list diag.List
	require.ErrorAs(t, err, &list)
	require.Len(t, list, 2)
	assert.Equal(t, diag.CodeImport, list[0].Code)
	assert.Equal(t, "main.ct", list[0].File)
	assert.Equal(t, 1, list[0].Line)
	assert.Contains(t, list[0].Hint, "doesn't use npm")
	assert.Equal(t, 2, list[1].Line)
	assert.Contains(t, list[1].Hint, "relative to the importing file")
}

func TestBundle_RelativeEntryPointAndProjectDir(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "app/main.ct", `import { v } from "./v"; console.log(v);`)
	writeTS(t, dir, "app/v.ct", `export const v = "relative-ok";`)
	t.Chdir(dir)

	js, err := engine.NewTranspiler("app").Bundle(filepath.Join("app", "main.ct"))
	require.NoError(t, err)
	assert.Contains(t, js, "relative-ok")
}
