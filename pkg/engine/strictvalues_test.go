package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderProject bundles main.ct with the given source and renders it with
// values, the way ct template does.
func renderProject(t *testing.T, src string, values map[string]interface{}) (*engine.Result, error) {
	t.Helper()
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	require.NoError(t, os.WriteFile(entry, []byte(src), 0o644))
	js, err := engine.NewTranspiler("").Bundle(entry)
	require.NoError(t, err)
	return engine.Render(engine.ExecuteOpts{JSCode: js, Values: values, SourceDir: dir, Namespace: "default"})
}

func singleDiag(t *testing.T, err error) diag.Diagnostic {
	t.Helper()
	require.Error(t, err)
	var list diag.List
	require.True(t, errors.As(err, &list), "want diag.List, got %T: %v", err, err)
	require.Len(t, list, 1)
	return list[0]
}

func configMap(data string) string {
	return `(globalThis as any).__ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name: "c" }, data: ` + data + ` });
`
}

func TestValues_UnknownTopLevelKeyFailsAtTheRead(t *testing.T) {
	src := "const r = 1;\nconst n = Values.replicsa;\n" + configMap(`{ n: String(n) }`)
	_, err := renderProject(t, src, map[string]interface{}{"replicas": 2})

	d := singleDiag(t, err)
	assert.Equal(t, diag.CodeUnknownValuesKey, d.Code)
	assert.Equal(t, "main.ct", d.File)
	assert.Equal(t, 2, d.Line)
	assert.Equal(t, 17, d.Column)
	assert.Equal(t, "replicsa", d.Path)
	assert.Equal(t, `no key "replicsa" in Values`, d.Message)
	assert.Contains(t, d.Hint, "did you mean Values.replicas?")
}

func TestValues_UnknownNestedKeyNamesFullPathAndClosestKey(t *testing.T) {
	src := configMap("{ tag: Values.image.tagg }")
	_, err := renderProject(t, src, map[string]interface{}{
		"image": map[string]interface{}{"repository": "nginx", "tag": "1.27"},
	})

	d := singleDiag(t, err)
	assert.Equal(t, diag.CodeUnknownValuesKey, d.Code)
	assert.Equal(t, "image.tagg", d.Path)
	assert.Equal(t, `no key "tagg" in Values.image`, d.Message)
	assert.Contains(t, d.Hint, "did you mean Values.image.tag?")
	assert.Equal(t, "main.ct", d.File)
	assert.Equal(t, 1, d.Line)
}

func TestValues_UnknownKeyWithoutCloseNameSuggestsDeclaringIt(t *testing.T) {
	src := configMap("{ x: String(Values.logLevel) }")
	_, err := renderProject(t, src, map[string]interface{}{"image": "nginx"})

	d := singleDiag(t, err)
	assert.Equal(t, "logLevel", d.Path)
	assert.NotContains(t, d.Hint, "did you mean")
	assert.Contains(t, d.Hint, "add logLevel to the values file")
	assert.Contains(t, d.Hint, "null")
}

func TestValues_UnknownKeyInsideArrayElement(t *testing.T) {
	src := "for (const w of Values.workers) { const q = w.queu; }\n" + configMap("{}")
	_, err := renderProject(t, src, map[string]interface{}{
		"workers": []interface{}{map[string]interface{}{"name": "a", "queue": "q"}},
	})

	d := singleDiag(t, err)
	assert.Equal(t, "workers[0].queu", d.Path)
	assert.Contains(t, d.Hint, "did you mean Values.workers[0].queue?")
}

func TestValues_NullKeysAreDeclared(t *testing.T) {
	src := configMap(`{
  a: String(Values.a),
  b: String(Values.a?.b),
  level: Values.logLevel ?? "info",
}`)
	res, err := renderProject(t, src, map[string]interface{}{"a": nil, "logLevel": nil})
	require.NoError(t, err)
	data := res.Resources[0]["data"].(map[string]interface{})
	assert.Equal(t, "null", data["a"])
	assert.Equal(t, "undefined", data["b"])
	assert.Equal(t, "info", data["level"])
}

func TestValues_IterationKeysSpreadInAndJSONDoNotFail(t *testing.T) {
	src := configMap(`{
  keys: Object.keys(Values).sort().join(","),
  forIn: (() => { const k: string[] = []; for (const key in Values) k.push(key); return k.sort().join(","); })(),
  entries: Object.entries(Values.env).map(([k, v]) => k + "=" + v).join(","),
  spread: JSON.stringify({ ...Values.image }),
  has: String("missing" in Values) + String("image" in Values),
  json: JSON.stringify(Values),
  own: String(Values.hasOwnProperty("missing")),
  workers: Values.workers.map((w: any) => w.name).join(","),
  isArray: String(Array.isArray(Values.workers)),
  tmpl: ` + "`${Values.image.repository}:${Values.image.tag}`" + `,
}`)
	values := map[string]interface{}{
		"env":     map[string]interface{}{"A": "1"},
		"image":   map[string]interface{}{"repository": "nginx", "tag": "1.27"},
		"workers": []interface{}{map[string]interface{}{"name": "w1"}},
	}
	res, err := renderProject(t, src, values)
	require.NoError(t, err)
	data := res.Resources[0]["data"].(map[string]interface{})
	assert.Equal(t, "env,image,workers", data["keys"])
	assert.Equal(t, "env,image,workers", data["forIn"])
	assert.Equal(t, "A=1", data["entries"])
	assert.Equal(t, `{"repository":"nginx","tag":"1.27"}`, data["spread"])
	assert.Equal(t, "falsetrue", data["has"])
	assert.JSONEq(t, `{"env":{"A":"1"},"image":{"repository":"nginx","tag":"1.27"},"workers":[{"name":"w1"}]}`, data["json"].(string))
	assert.Equal(t, "false", data["own"])
	assert.Equal(t, "w1", data["workers"])
	assert.Equal(t, "true", data["isArray"])
	assert.Equal(t, "nginx:1.27", data["tmpl"])
}

func TestValues_ObjectsFromValuesRenderInResources(t *testing.T) {
	src := `(globalThis as any).__ct_resources.push({ apiVersion: "v1", kind: "ConfigMap", metadata: { name: "c", labels: Values.labels }, data: Values.data });
(globalThis as any).__ct_resources.push(Values.extra);
`
	values := map[string]interface{}{
		"labels": map[string]interface{}{"app": "web"},
		"data":   map[string]interface{}{"k": "v"},
		"extra":  map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "x"}},
	}
	res, err := renderProject(t, src, values)
	require.NoError(t, err)
	require.Len(t, res.Resources, 2)
	assert.Equal(t, map[string]interface{}{"app": "web"}, res.Resources[0]["metadata"].(map[string]interface{})["labels"])
	assert.Equal(t, map[string]interface{}{"k": "v"}, res.Resources[0]["data"])
	assert.Equal(t, "x", res.Resources[1]["metadata"].(map[string]interface{})["name"])
}

func TestValues_IdentityAndReassignment(t *testing.T) {
	src := `const same = Values.image === Values.image;
Values.image = { tag: "2" };
` + configMap(`{ same: String(same), tag: Values.image.tag }`)
	res, err := renderProject(t, src, map[string]interface{}{"image": map[string]interface{}{"tag": "1"}})
	require.NoError(t, err)
	data := res.Resources[0]["data"].(map[string]interface{})
	assert.Equal(t, "true", data["same"])
	assert.Equal(t, "2", data["tag"])
}
