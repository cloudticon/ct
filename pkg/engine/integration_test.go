package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_SimpleInlineCode(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entry, []byte(`
(globalThis as any).__ct_resources.push({
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { name: "web-app" },
  spec: {
    replicas: Values.replicas,
    template: {
      spec: {
        containers: [{ name: "web", image: Values.image }],
      },
    },
  },
});
`), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)

	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode: js,
		Values: map[string]interface{}{
			"image":    "nginx:1.25",
			"replicas": 3,
		},
		Namespace: "production",
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	res := resources[0]
	assert.Equal(t, "apps/v1", res["apiVersion"])
	assert.Equal(t, "Deployment", res["kind"])

	meta := res["metadata"].(map[string]interface{})
	assert.Equal(t, "web-app", meta["name"])
	assert.Equal(t, "production", meta["namespace"])
}

func TestIntegration_ConditionalWithValues(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entry, []byte(`
(globalThis as any).__ct_resources.push({
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { name: "app" },
  spec: { replicas: 1 },
});

if (Values.enableExtra) {
  (globalThis as any).__ct_resources.push({
    apiVersion: "v1",
    kind: "ConfigMap",
    metadata: { name: "extra" },
    data: { key: "value" },
  });
}
`), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)

	enabled, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    js,
		Values:    map[string]interface{}{"enableExtra": true},
		Namespace: "test",
	})
	require.NoError(t, err)
	assert.Len(t, enabled, 2)

	disabled, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    js,
		Values:    map[string]interface{}{"enableExtra": false},
		Namespace: "test",
	})
	require.NoError(t, err)
	assert.Len(t, disabled, 1)
}

func TestIntegration_LoopFromValues(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entry, []byte(`
for (const w of Values.workers) {
  (globalThis as any).__ct_resources.push({
    apiVersion: "apps/v1",
    kind: "Deployment",
    metadata: { name: "worker-" + w.name },
    spec: {
      replicas: w.replicas,
      template: { spec: { containers: [{ name: w.name, image: w.image }] } },
    },
  });
}
`), 0644)
	require.NoError(t, err)

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entry)
	require.NoError(t, err)

	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode: js,
		Values: map[string]interface{}{
			"workers": []interface{}{
				map[string]interface{}{"name": "email", "image": "worker:1.0", "replicas": 2},
				map[string]interface{}{"name": "pdf", "image": "worker:1.0", "replicas": 1},
			},
		},
		Namespace: "default",
	})
	require.NoError(t, err)
	require.Len(t, resources, 2)

	meta0 := resources[0]["metadata"].(map[string]interface{})
	meta1 := resources[1]["metadata"].(map[string]interface{})
	assert.Equal(t, "worker-email", meta0["name"])
	assert.Equal(t, "worker-pdf", meta1["name"])
}

func TestIntegration_FullPipeline_WithValuesFile(t *testing.T) {
	dir := t.TempDir()
	valuesFile := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(valuesFile, []byte(`{"image": "myapp:2.0", "replicas": 5}`), 0644))

	entryFile := filepath.Join(dir, "main.ct")
	err := os.WriteFile(entryFile, []byte(`
(globalThis as any).__ct_resources.push({
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { name: "myapp" },
  spec: {
    replicas: Values.replicas,
    template: { spec: { containers: [{ name: "myapp", image: Values.image }] } },
  },
});
`), 0644)
	require.NoError(t, err)

	values, err := engine.LoadValuesFile(valuesFile, []string{"replicas=10"})
	require.NoError(t, err)
	assert.Equal(t, int64(10), values["replicas"])

	tr := engine.NewTranspiler("")
	js, err := tr.Bundle(entryFile)
	require.NoError(t, err)

	resources, err := engine.Execute(engine.ExecuteOpts{
		JSCode:    js,
		Values:    values,
		Namespace: "staging",
	})
	require.NoError(t, err)
	require.Len(t, resources, 1)

	spec := resources[0]["spec"].(map[string]interface{})
	assert.Equal(t, int64(10), spec["replicas"])

	meta := resources[0]["metadata"].(map[string]interface{})
	assert.Equal(t, "staging", meta["namespace"])
}
