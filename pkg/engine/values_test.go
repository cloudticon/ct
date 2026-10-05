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
