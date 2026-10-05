package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// E2E tests for `ct template`. Each test drives the cobra command via
// cmd.Execute() with a minimal on-disk project, then asserts on the
// rendered manifests captured from stdout. The .ct fixtures use only the
// __ct_resources global so the tests run fully offline (no esbuild URL
// imports, no values.json schema).

func writeProject(t *testing.T, mainCt string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(mainCt), 0o644))
	for name, body := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	return dir
}

func runTemplateE2E(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newTemplateCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

func TestTemplateE2E_RendersYAMLByDefault(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "demo-cm" },
  data: { greeting: "hello" },
});
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir)
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 1)

	cm := docs[0]
	assert.Equal(t, "v1", cm["apiVersion"])
	assert.Equal(t, "ConfigMap", cm["kind"])

	meta := asMap(t, cm["metadata"])
	assert.Equal(t, "demo-cm", meta["name"])
	labels := asMap(t, meta["labels"])
	assert.Equal(t, "ct", labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "demo", labels["ct.cloudticon.com/instance"])

	data := asMap(t, cm["data"])
	assert.Equal(t, "hello", data["greeting"])
}

func TestTemplateE2E_OutputJSON(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "j-cm" },
});
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir, "-o", "json")
	require.NoError(t, err)

	var docs []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &docs), "output must be valid JSON array")
	require.Len(t, docs, 1)
	assert.Equal(t, "ConfigMap", docs[0]["kind"])
}

func TestTemplateE2E_AppliesValuesFromAutoDetectedFile(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "values-cm" },
  data: {
    image: Values.image,
    replicas: String(Values.replicas),
  },
});
`, map[string]string{
		"values.json": `{"image": "nginx:1.25", "replicas": 3}`,
	})

	stdout, _, err := runTemplateE2E(t, "demo", dir)
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 1)
	data := asMap(t, docs[0]["data"])
	assert.Equal(t, "nginx:1.25", data["image"])
	assert.Equal(t, "3", data["replicas"])
}

func TestTemplateE2E_OverridesValuesFromSetFlag(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "set-cm" },
  data: {
    image: Values.image,
    replicas: String(Values.replicas),
  },
});
`, map[string]string{
		"values.json": `{"image": "nginx:1.25", "replicas": 3}`,
	})

	stdout, _, err := runTemplateE2E(t, "demo", dir,
		"--set", "image=custom:dev",
		"--set", "replicas=7",
	)
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 1)
	data := asMap(t, docs[0]["data"])
	assert.Equal(t, "custom:dev", data["image"])
	assert.Equal(t, "7", data["replicas"])
}

func TestTemplateE2E_ExplicitValuesFile(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "explicit-cm" },
  data: { env: Values.env },
});
`, map[string]string{
		"values.json":      `{"env": "default"}`,
		"values-prod.json": `{"env": "production"}`,
	})

	stdout, _, err := runTemplateE2E(t, "demo", dir,
		"-f", filepath.Join(dir, "values-prod.json"),
	)
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 1)
	data := asMap(t, docs[0]["data"])
	assert.Equal(t, "production", data["env"])
}

func TestTemplateE2E_AppliesNamespaceFlagAsDefault(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "ns-cm" },
});
__ct_resources.push({
  apiVersion: "v1",
  kind: "Secret",
  metadata: { name: "ns-secret", namespace: "explicit-ns" },
});
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir, "-n", "default-ns")
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 2)

	cm := asMap(t, docByKind(t, docs, "ConfigMap")["metadata"])
	assert.Equal(t, "default-ns", cm["namespace"], "namespaced resource without explicit ns must inherit default")

	secret := asMap(t, docByKind(t, docs, "Secret")["metadata"])
	assert.Equal(t, "explicit-ns", secret["namespace"], "explicit namespace must not be overridden")
}

func TestTemplateE2E_RendersResourcesInHelmInstallOrder(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { name: "web" },
});
__ct_resources.push({
  apiVersion: "v1",
  kind: "Service",
  metadata: { name: "web-svc" },
});
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "web-cm" },
});
__ct_resources.push({
  apiVersion: "v1",
  kind: "Namespace",
  metadata: { name: "team" },
});
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir, "--validate=false")
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 4)
	assert.Equal(t, "Namespace", docs[0]["kind"])
	assert.Equal(t, "ConfigMap", docs[1]["kind"])
	assert.Equal(t, "Service", docs[2]["kind"])
	assert.Equal(t, "Deployment", docs[3]["kind"])
}

func TestTemplateE2E_KeepsEmptyObjects(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "Pod",
  metadata: { name: "p", labels: undefined },
  spec: {
    containers: [{ name: "app", image: "nginx", args: [] }],
    volumes: [{ name: "tmp", emptyDir: {} }],
  },
});
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir)
	require.NoError(t, err)

	assert.Contains(t, stdout, "emptyDir: {}")
	assert.Contains(t, stdout, "args: []", "empty arrays stay as written")
	assert.NotContains(t, stdout, "labels: null")
}

func TestTemplateE2E_ClusterScopedKindsGetNoNamespace(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({ apiVersion: "rbac.authorization.k8s.io/v1", kind: "ClusterRole", metadata: { name: "reader" } });
__ct_resources.push({ apiVersion: "v1", kind: "Namespace", metadata: { name: "team" } });
__ct_resources.push({ apiVersion: "v1", kind: "ServiceAccount", metadata: { name: "sa" } });
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir, "-n", "prod")
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 3)
	for _, doc := range docs {
		meta := asMap(t, doc["metadata"])
		if doc["kind"] == "ServiceAccount" {
			assert.Equal(t, "prod", meta["namespace"])
		} else {
			assert.NotContains(t, meta, "namespace", "%s is cluster-scoped", doc["kind"])
		}
	}
}

func TestTemplateE2E_RejectsDuplicateResources(t *testing.T) {
	dir := writeProject(t, `
for (const i of [1, 2]) {
  __ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name: "cfg" } });
}
`, nil)

	_, _, err := runTemplateE2E(t, "demo", dir, "-n", "prod")
	require.Error(t, err)
	var list diag.List
	require.ErrorAs(t, err, &list)
	require.Len(t, list, 1)
	assert.Equal(t, diag.CodeDuplicate, list[0].Code)
	assert.Equal(t, `ConfigMap "cfg" (namespace "prod")`, list[0].Resource)
	assert.Equal(t, "main.ct", list[0].File)
	assert.Equal(t, 3, list[0].Line, "points at the push inside the loop")
	assert.Contains(t, list[0].Message, "registered 2 times")
}

func TestTemplateE2E_YAMLUsesTwoSpaceIndent(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "Service",
  metadata: { name: "svc" },
  spec: { ports: [{ port: 80, targetPort: 8080 }] },
});
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir)
	require.NoError(t, err)

	assert.Contains(t, stdout, "\nmetadata:\n  labels:\n")
	assert.Contains(t, stdout, "\nspec:\n  ports:\n    - port: 80\n      targetPort: 8080\n")
}

func TestTemplateE2E_PreservesUserLabelsOverInjected(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: {
    name: "user-cm",
    labels: {
      "app.kubernetes.io/managed-by": "user-managed",
      "team": "platform",
    },
  },
});
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir)
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 1)
	labels := asMap(t, asMap(t, docs[0]["metadata"])["labels"])
	assert.Equal(t, "user-managed", labels["app.kubernetes.io/managed-by"], "user-set managed-by label wins over injected default")
	assert.Equal(t, "platform", labels["team"], "unrelated user label preserved")
	assert.Equal(t, "demo", labels["ct.cloudticon.com/instance"], "instance label still injected")
}

func TestTemplateE2E_ExposesReleaseGlobalToScript(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1",
  kind: "ConfigMap",
  metadata: { name: "rel-" + Release.name },
  data: {
    release: Release.name,
    namespace: Release.namespace || "<unset>",
  },
});
`, nil)

	stdout, _, err := runTemplateE2E(t, "my-release", dir, "-n", "prod")
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 1)
	assert.Equal(t, "rel-my-release", asMap(t, docs[0]["metadata"])["name"])
	data := asMap(t, docs[0]["data"])
	assert.Equal(t, "my-release", data["release"])
	assert.Equal(t, "prod", data["namespace"])
}

func TestTemplateE2E_ReportsBundleErrorWithLocation(t *testing.T) {
	dir := writeProject(t, `
this is not valid javascript {{{ syntax error
`, nil)

	_, _, err := runTemplateE2E(t, "demo", dir)

	var list diag.List
	require.ErrorAs(t, err, &list)
	assert.Equal(t, diag.CodeSyntax, list[0].Code)
	assert.Equal(t, "main.ct", list[0].File)
	assert.Equal(t, 2, list[0].Line)
	assert.Contains(t, list[0].LineText, "this is not valid javascript")
}

func TestTemplateE2E_RuntimeErrorPointsAtCtSource(t *testing.T) {
	dir := writeProject(t, `import { make } from "./lib/factory";

make({ name: "ok" });
make({ name: "" });
`, map[string]string{
		"lib/factory.ct": `export function make(opts: { name: string }) {
  if (!opts.name) {
    throw new Error("name is required");
  }
  __ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name: opts.name } });
}
`,
	})

	_, _, err := runTemplateE2E(t, "demo", dir)

	var list diag.List
	require.ErrorAs(t, err, &list)
	d := list[0]
	assert.Equal(t, diag.CodeRuntime, d.Code)
	assert.Contains(t, d.Message, "name is required")
	assert.Equal(t, "lib/factory.ct", d.File)
	assert.Equal(t, 3, d.Line)
	require.GreaterOrEqual(t, len(d.Stack), 2)
	assert.Equal(t, "main.ct", d.Stack[1].File)
	assert.Equal(t, 4, d.Stack[1].Line, "the failing call site in main.ct")
}

func TestTemplateE2E_TimesOutEndlessLoops(t *testing.T) {
	old := renderTimeout
	renderTimeout = 200 * time.Millisecond
	t.Cleanup(func() { renderTimeout = old })
	dir := writeProject(t, `
let i = 0;
while (true) { i++; }
`, nil)

	_, _, err := runTemplateE2E(t, "demo", dir)

	var list diag.List
	require.ErrorAs(t, err, &list)
	assert.Equal(t, diag.CodeTimeout, list[0].Code)
	assert.Equal(t, "main.ct", list[0].File)
	assert.Equal(t, 3, list[0].Line)
}

func TestTemplateE2E_RejectsUnsupportedOutputFormat(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({apiVersion: "v1", kind: "ConfigMap", metadata: { name: "x" }});
`, nil)

	_, _, err := runTemplateE2E(t, "demo", dir, "-o", "xml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "serialization failed")
}

func TestTemplateE2E_SetWithoutValuesFile(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name: "cfg" }, data: { tag: Values.image.tag } });
`, nil)

	stdout, _, err := runTemplateE2E(t, "demo", dir, "--set", "image.tag=1.10")
	require.NoError(t, err)

	docs := splitYAMLDocs(t, stdout)
	require.Len(t, docs, 1)
	assert.Equal(t, "1.10", asMap(t, docs[0]["data"])["tag"], "tag stays the string 1.10, not the number 1.1")
}

func TestTemplateE2E_MergesRepeatedValuesFiles(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({
  apiVersion: "v1", kind: "ConfigMap", metadata: { name: "cfg" },
  data: { image: Values.image.repository + ":" + Values.image.tag, replicas: String(Values.replicas) },
});
`, map[string]string{
		"values.yaml":      "image:\n  repository: nginx\n  tag: \"1.25\"\nreplicas: 1\n",
		"values-prod.yaml": "image:\n  tag: \"1.27\"\n",
	})

	stdout, _, err := runTemplateE2E(t, "demo", dir,
		"-f", filepath.Join(dir, "values.yaml"), "-f", filepath.Join(dir, "values-prod.yaml"), "--set-string", "replicas=3")
	require.NoError(t, err)

	data := asMap(t, splitYAMLDocs(t, stdout)[0]["data"])
	assert.Equal(t, "nginx:1.27", data["image"])
	assert.Equal(t, "3", data["replicas"])
}

func TestTemplateE2E_ValidationPointsAtTheRegisteringLine(t *testing.T) {
	dir := writeProject(t, `import { configMap } from "./lib/k8s";

configMap({ name: "ok", data: { a: "1" } });
configMap({ name: "Bad_Name", spec: { data: { a: "1" } } });
`, map[string]string{
		"lib/k8s.ct": `export function configMap(args: { name: string; [k: string]: unknown }) {
  const { name, ...rest } = args;
  __ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name }, ...rest });
}
`,
	})

	_, _, err := runTemplateE2E(t, "demo", dir)

	var list diag.List
	require.ErrorAs(t, err, &list)
	require.Len(t, list, 2)
	for _, d := range list {
		assert.Equal(t, "lib/k8s.ct", d.File, "%s", d)
		assert.Equal(t, 3, d.Line, "%s", d)
		require.Len(t, d.Stack, 1, "the call chain reaches the user's call: %s", d)
		assert.Equal(t, "main.ct", d.Stack[0].File)
		assert.Equal(t, 4, d.Stack[0].Line)
		assert.Equal(t, `ConfigMap "Bad_Name"`, d.Resource)
	}
	assert.Equal(t, "metadata.name", list[0].Path)
	assert.Equal(t, diag.CodeUnknownField, list[1].Code)
	assert.Equal(t, "spec", list[1].Path)
}

func TestTemplateE2E_ValidateFalseSkipsValidation(t *testing.T) {
	dir := writeProject(t, `
__ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name: "cfg" }, spec: { x: 1 } });
`, nil)

	_, _, err := runTemplateE2E(t, "demo", dir)
	require.Error(t, err)

	stdout, _, err := runTemplateE2E(t, "demo", dir, "--validate=false")
	require.NoError(t, err)
	assert.Contains(t, stdout, "spec:")
}

func TestTemplateE2E_RejectsInvalidReleaseName(t *testing.T) {
	dir := writeProject(t, `__ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name: "cfg" } });`, nil)

	_, _, err := runTemplateE2E(t, "My_App", dir)

	var list diag.List
	require.ErrorAs(t, err, &list)
	assert.Contains(t, list[0].Message, `invalid release name "My_App"`)
}

// --- helpers ---

func splitYAMLDocs(t *testing.T, raw string) []map[string]interface{} {
	t.Helper()
	docs := []map[string]interface{}{}
	dec := yaml.NewDecoder(strings.NewReader(raw))
	for {
		var doc map[string]interface{}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

func docByKind(t *testing.T, docs []map[string]interface{}, kind string) map[string]interface{} {
	t.Helper()
	for _, doc := range docs {
		if doc["kind"] == kind {
			return doc
		}
	}
	require.Failf(t, "kind not rendered", "no %s in output", kind)
	return nil
}

func asMap(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	m, ok := v.(map[string]interface{})
	require.Truef(t, ok, "expected map, got %T (%v)", v, v)
	return m
}
