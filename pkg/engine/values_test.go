package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadValuesFile_JSON(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{
  "image": "nginx:1.25",
  "replicas": 3,
  "debug": false
}`), 0644))

	values, err := engine.LoadValuesFile(f, nil)
	require.NoError(t, err)
	assert.Equal(t, "nginx:1.25", values["image"])
	assert.Equal(t, int64(3), values["replicas"])
	assert.Equal(t, false, values["debug"])
}

func TestLoadValuesFile_YAML(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.yaml")
	require.NoError(t, os.WriteFile(f, []byte("image: nginx:1.25\nreplicas: 3\ndebug: false\n"), 0644))

	values, err := engine.LoadValuesFile(f, nil)
	require.NoError(t, err)
	assert.Equal(t, "nginx:1.25", values["image"])
	assert.Equal(t, int64(3), values["replicas"])
	assert.Equal(t, false, values["debug"])
}

func TestLoadValuesFile_YML(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.yml")
	require.NoError(t, os.WriteFile(f, []byte("key: value\n"), 0644))

	values, err := engine.LoadValuesFile(f, nil)
	require.NoError(t, err)
	assert.Equal(t, "value", values["key"])
}

func TestLoadValuesFile_Nested(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{"app": {"name": "web", "port": 8080}}`), 0644))

	values, err := engine.LoadValuesFile(f, nil)
	require.NoError(t, err)

	app := values["app"].(map[string]interface{})
	assert.Equal(t, "web", app["name"])
	assert.Equal(t, int64(8080), app["port"])
}

func TestLoadValuesFile_WithSetOverrides(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{"image": "nginx:1.25", "replicas": 3}`), 0644))

	values, err := engine.LoadValuesFile(f, []string{"replicas=5", "image=nginx:1.26"})
	require.NoError(t, err)
	assert.Equal(t, int64(5), values["replicas"])
	assert.Equal(t, "nginx:1.26", values["image"])
}

func TestLoadValuesFile_SetOverrideNested(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{"app": {"name": "web", "port": 8080}}`), 0644))

	values, err := engine.LoadValuesFile(f, []string{"app.port=9090"})
	require.NoError(t, err)

	app := values["app"].(map[string]interface{})
	assert.Equal(t, int64(9090), app["port"])
	assert.Equal(t, "web", app["name"])
}

func TestLoadValuesFile_SetOverrideBooleans(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{"debug": false, "verbose": true}`), 0644))

	values, err := engine.LoadValuesFile(f, []string{"debug=true", "verbose=false"})
	require.NoError(t, err)
	assert.Equal(t, true, values["debug"])
	assert.Equal(t, false, values["verbose"])
}

func TestLoadValuesFile_SetCreatesNestedPath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{}`), 0644))

	values, err := engine.LoadValuesFile(f, []string{"a.b.c=hello"})
	require.NoError(t, err)

	a := values["a"].(map[string]interface{})
	b := a["b"].(map[string]interface{})
	assert.Equal(t, "hello", b["c"])
}

func TestLoadValuesFile_InvalidSetFormat(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{}`), 0644))

	_, err := engine.LoadValuesFile(f, []string{"invalid"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --set format")
}

func TestLoadValuesFile_WithArray(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{
  "workers": [
    {"name": "email", "replicas": 2},
    {"name": "pdf", "replicas": 1}
  ]
}`), 0644))

	values, err := engine.LoadValuesFile(f, nil)
	require.NoError(t, err)

	workers := values["workers"].([]interface{})
	require.Len(t, workers, 2)

	w0 := workers[0].(map[string]interface{})
	assert.Equal(t, "email", w0["name"])
	assert.Equal(t, int64(2), w0["replicas"])
}

func TestLoadValuesFile_FileNotFound(t *testing.T) {
	_, err := engine.LoadValuesFile("/nonexistent/values.json", nil)
	assert.Error(t, err)
}

func TestLoadValuesFile_UnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.txt")
	require.NoError(t, os.WriteFile(f, []byte(`key=value`), 0644))

	_, err := engine.LoadValuesFile(f, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestLoadValuesFile_FloatPreserved(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(f, []byte(`{"ratio": 0.75}`), 0644))

	values, err := engine.LoadValuesFile(f, nil)
	require.NoError(t, err)
	assert.Equal(t, 0.75, values["ratio"])
}

func TestLoadValues_NoSources(t *testing.T) {
	values, err := engine.LoadValues(engine.ValuesOpts{})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{}, values)
}

func TestLoadValues_SetWithoutValuesFile(t *testing.T) {
	values, err := engine.LoadValues(engine.ValuesOpts{Set: []string{"image.tag=v2", "replicas=3"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{
		"image":    map[string]interface{}{"tag": "v2"},
		"replicas": int64(3),
	}, values)
}

func TestLoadValues_EmptyYAMLFileWithSet(t *testing.T) {
	// A values.yaml with only comments decodes to a nil map; --set used to
	// panic writing into it.
	path := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(path, []byte("# all defaults commented out\n"), 0o644))

	values, err := engine.LoadValues(engine.ValuesOpts{Files: []string{path}, Set: []string{"a=1"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"a": int64(1)}, values)
}

func TestLoadValues_JSONNullFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.json")
	require.NoError(t, os.WriteFile(path, []byte("null"), 0o644))

	values, err := engine.LoadValues(engine.ValuesOpts{Files: []string{path}, Set: []string{"a=b"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"a": "b"}, values)
}

func TestLoadValues_MultipleFilesDeepMerge(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "values.yaml")
	prod := filepath.Join(dir, "values-prod.json")
	require.NoError(t, os.WriteFile(base, []byte(`
image: { repository: nginx, tag: "1.25" }
replicas: 1
hosts: [a.example.com, b.example.com]
debug: true
`), 0o644))
	require.NoError(t, os.WriteFile(prod, []byte(`{"image":{"tag":"1.27"},"replicas":3,"hosts":["prod.example.com"]}`), 0o644))

	values, err := engine.LoadValues(engine.ValuesOpts{Files: []string{base, prod}})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{
		"image":    map[string]interface{}{"repository": "nginx", "tag": "1.27"},
		"replicas": int64(3),
		"hosts":    []interface{}{"prod.example.com"},
		"debug":    true,
	}, values)
}

func TestLoadValues_SetKeepsNonCanonicalNumbersAsStrings(t *testing.T) {
	values, err := engine.LoadValues(engine.ValuesOpts{Set: []string{
		"tag=1.10", "minor=1.0", "zip=0123", "exp=1e3", "plus=+1",
		"int=42", "neg=-7", "float=0.5", "on=true", "off=false", "nothing=null",
		"nan=NaN", "inf=+Inf", "ninf=-Inf",
	}})
	require.NoError(t, err)
	assert.Equal(t, "1.10", values["tag"])
	assert.Equal(t, "1.0", values["minor"])
	assert.Equal(t, "0123", values["zip"])
	assert.Equal(t, "1e3", values["exp"])
	assert.Equal(t, "+1", values["plus"])
	assert.Equal(t, "NaN", values["nan"], "NaN and infinities can't be rendered as JSON numbers")
	assert.Equal(t, "+Inf", values["inf"])
	assert.Equal(t, "-Inf", values["ninf"])
	assert.Equal(t, int64(42), values["int"])
	assert.Equal(t, int64(-7), values["neg"])
	assert.Equal(t, 0.5, values["float"])
	assert.Equal(t, true, values["on"])
	assert.Equal(t, false, values["off"])
	assert.Contains(t, values, "nothing")
	assert.Nil(t, values["nothing"])
}

func TestLoadValues_SetString(t *testing.T) {
	values, err := engine.LoadValues(engine.ValuesOpts{
		Set:       []string{"replicas=3", "flag=true"},
		SetString: []string{"flag=true", "port=8080"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), values["replicas"])
	assert.Equal(t, "true", values["flag"], "--set-string wins and stays a string")
	assert.Equal(t, "8080", values["port"])
}

func TestLoadValues_SetEscapedDots(t *testing.T) {
	values, err := engine.LoadValues(engine.ValuesOpts{Set: []string{
		`annotations.nginx\.ingress\.kubernetes\.io/rewrite-target=/`,
		"url=https://example.com/a=b",
	}})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{
		"nginx.ingress.kubernetes.io/rewrite-target": "/",
	}, values["annotations"])
	assert.Equal(t, "https://example.com/a=b", values["url"], "only the first = separates key and value")
}

func TestLoadValues_SetRejectsEmptyKeySegments(t *testing.T) {
	for _, bad := range []string{"=x", "a..b=x", ".a=x", "a.=x"} {
		_, err := engine.LoadValues(engine.ValuesOpts{Set: []string{bad}})
		assert.Error(t, err, bad)
	}
}
