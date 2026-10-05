package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudticon/ct/pkg/packages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTypesCmd_GeneratesFiles(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "values.json"), []byte(`{
  "image": "nginx:1.25",
  "replicas": 3,
  "debug": false
}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(`const x = 1;`), 0644))

	cmd := newTypesCmd()
	cmd.SetArgs([]string{dir, "--output", outDir})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(outDir, "values.d.ts"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "interface CtValues")
	assert.Contains(t, content, "image: string")
	assert.Contains(t, content, "replicas: number")
	assert.Contains(t, content, "debug: boolean")

	data, err = os.ReadFile(filepath.Join(outDir, "globals.d.ts"))
	require.NoError(t, err)
	content = string(data)
	assert.Contains(t, content, "declare const Values: CtValues")
	assert.Contains(t, content, "declare const Release")
	assert.Contains(t, content, "readonly name: string")
	assert.Contains(t, content, "readonly namespace: string")
	assert.NotContains(t, content, "getStatus")
}

func TestTypesCmd_WithOperatorFlag(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "values.json"), []byte(`{}`), 0644))

	cmd := newTypesCmd()
	cmd.SetArgs([]string{dir, "--output", outDir, "--operator"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(outDir, "globals.d.ts"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "declare const Values: CtValues")
	assert.Contains(t, content, "declare const Release")
	assert.Contains(t, content, "declare function getStatus")
	assert.Contains(t, content, "declare function setStatus")
	assert.Contains(t, content, "declare function fetch")
	assert.Contains(t, content, "declare const log")
	assert.Contains(t, content, "declare const Env")
}

func TestTypesCmd_EmptyValues(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	cmd := newTypesCmd()
	cmd.SetArgs([]string{dir, "--output", outDir})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(outDir, "values.d.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "interface CtValues {}")
}

func TestTypesCmd_NestedValues(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "values.json"), []byte(`{
  "app": {
    "name": "web",
    "port": 8080
  }
}`), 0644))

	cmd := newTypesCmd()
	cmd.SetArgs([]string{dir, "--output", outDir})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(outDir, "values.d.ts"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "app: {")
	assert.Contains(t, content, "name: string")
	assert.Contains(t, content, "port: number")
}

func TestTypesCmd_YAMLValues(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("image: nginx\nreplicas: 3\n"), 0644))

	cmd := newTypesCmd()
	cmd.SetArgs([]string{dir, "--output", outDir})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(outDir, "values.d.ts"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "image: string")
	assert.Contains(t, content, "replicas: number")
}

func TestTypesCmd_DefaultOutputDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()

	var buf bytes.Buffer
	cmd := newTypesCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	outputPath := strings.TrimSpace(buf.String())
	assert.Contains(t, outputPath, filepath.Join(home, ".ct", "types"))

	_, err := os.Stat(filepath.Join(outputPath, "values.d.ts"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(outputPath, "globals.d.ts"))
	assert.NoError(t, err)
}

func TestTypesCmd_ArrayValues(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "values.json"), []byte(`{
  "tags": ["web", "prod"],
  "ports": [80, 443],
  "empty": []
}`), 0644))

	cmd := newTypesCmd()
	cmd.SetArgs([]string{dir, "--output", outDir})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(outDir, "values.d.ts"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "tags: string[]")
	assert.Contains(t, content, "ports: number[]")
	assert.Contains(t, content, "empty: any[]")
}

func TestTypesCmd_FindsOperatorCt(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "operator.ct"), []byte(`const x = 1;`), 0644))

	cmd := newTypesCmd()
	cmd.SetArgs([]string{dir, "--output", outDir})
	require.NoError(t, cmd.Execute())

	_, err := os.Stat(filepath.Join(outDir, "globals.d.ts"))
	assert.NoError(t, err)
}

func TestTypesCmd_WithDevFlag_GeneratesDevDts(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	mainCt := `
__ct_resources.push({
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { name: "remix" },
  spec: {
    selector: {
      matchLabels: { app: "remix" }
    }
  }
});
__ct_resources.push({
  apiVersion: "v1",
  kind: "Service",
  metadata: { name: "remix-svc" },
  spec: { selector: { app: "remix" } }
});
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(mainCt), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("ZETA=1\nALPHA=2\n"), 0o644))

	cmd := newTypesCmd()
	cmd.SetArgs([]string{dir, "--output", outDir, "--dev"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(outDir, "dev.d.ts"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, `/// <reference path="./values.d.ts" />`)
	assert.Contains(t, content, `type CtResource = "remix";`)
	assert.Contains(t, content, `type CtEnvKey = "ALPHA" | "ZETA";`)
	assert.Contains(t, content, "interface DevConfig")
	assert.Contains(t, content, "  workingDir?: string;")
	assert.Contains(t, content, "  image?: string;")
	assert.Contains(t, content, "declare function dev(name: CtResource, config: DevConfig): void;")
	assert.Contains(t, content, "declare function env(name: CtEnvKey, defaultValue: number): number;")
}

func TestInferTSType(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		expected string
	}{
		{"string", "hello", "string"},
		{"bool_true", true, "boolean"},
		{"bool_false", false, "boolean"},
		{"int64", int64(42), "number"},
		{"float64", 3.14, "number"},
		{"int", 7, "number"},
		{"nil", nil, "any"},
		{"empty_array", []interface{}{}, "any[]"},
		{"string_array", []interface{}{"a", "b"}, "string[]"},
		{"number_array", []interface{}{int64(1), int64(2)}, "number[]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, inferTSType(tt.value, ""))
		})
	}
}

func TestProjectHash(t *testing.T) {
	h1 := projectHash("/home/user/project1")
	h2 := projectHash("/home/user/project2")
	assert.Len(t, h1, 12)
	assert.Len(t, h2, 12)
	assert.NotEqual(t, h1, h2)

	h1Again := projectHash("/home/user/project1")
	assert.Equal(t, h1, h1Again)
}

func TestGenerateValuesDts_Empty(t *testing.T) {
	result := generateValuesDts(nil)
	assert.Equal(t, "interface CtValues {}\n", result)
}

func TestGenerateValuesDts_SortedKeys(t *testing.T) {
	values := map[string]interface{}{
		"zebra":  "z",
		"alpha":  "a",
		"middle": "m",
	}
	result := generateValuesDts(values)
	alphaIdx := strings.Index(result, "alpha")
	middleIdx := strings.Index(result, "middle")
	zebraIdx := strings.Index(result, "zebra")
	assert.Less(t, alphaIdx, middleIdx)
	assert.Less(t, middleIdx, zebraIdx)
}

func TestGenerateGlobalsDts_WithoutOperator(t *testing.T) {
	result := generateGlobalsDts(false)
	assert.Contains(t, result, "declare const Values: CtValues")
	assert.Contains(t, result, "declare const Release")
	assert.Contains(t, result, "readonly name: string")
	assert.Contains(t, result, "readonly namespace: string")
	assert.Contains(t, result, `/// <reference path="./values.d.ts" />`)
	assert.NotContains(t, result, "getStatus")
	assert.NotContains(t, result, "setStatus")
	assert.NotContains(t, result, "Env")
}

func TestGenerateGlobalsDts_WithOperator(t *testing.T) {
	result := generateGlobalsDts(true)
	assert.Contains(t, result, "declare const Values: CtValues")
	assert.Contains(t, result, "declare const Release")
	assert.Contains(t, result, "declare function getStatus<T>(resource: T): any")
	assert.Contains(t, result, "declare function setStatus(cr: any, status: any): void")
	assert.Contains(t, result, "declare function fetch(url: string")
	assert.Contains(t, result, "method?: string")
	assert.Contains(t, result, "declare const log")
	assert.Contains(t, result, "info(...args: any[]): void")
	assert.Contains(t, result, "warn(...args: any[]): void")
	assert.Contains(t, result, "error(...args: any[]): void")
	assert.Contains(t, result, "declare const Env: Record<string, string>")
}

func TestGenerateDevDts_EmptyUnionsFallbackToNever(t *testing.T) {
	result := generateDevDts(nil, nil)
	assert.Contains(t, result, "type CtResource = never;")
	assert.Contains(t, result, "type CtEnvKey = never;")
}

func TestFindEntryPoint(t *testing.T) {
	t.Run("main.ct", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(""), 0644))
		assert.Equal(t, filepath.Join(dir, "main.ct"), findEntryPoint(dir))
	})

	t.Run("operator.ct", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "operator.ct"), []byte(""), 0644))
		assert.Equal(t, filepath.Join(dir, "operator.ct"), findEntryPoint(dir))
	})

	t.Run("main.ct_preferred_over_operator.ct", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(""), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "operator.ct"), []byte(""), 0644))
		assert.Equal(t, filepath.Join(dir, "main.ct"), findEntryPoint(dir))
	})

	t.Run("no_entry_point", func(t *testing.T) {
		dir := t.TempDir()
		assert.Empty(t, findEntryPoint(dir))
	})
}

func TestGenerateValuesDts_QuotesNonIdentifierKeys(t *testing.T) {
	dts := generateValuesDts(map[string]interface{}{
		"annotations": map[string]interface{}{"nginx.ingress.kubernetes.io/rewrite-target": "/"},
		"my-key":      "x",
		"2fa":         true,
		"ok_key":      int64(1),
	})

	assert.Contains(t, dts, `  "2fa": boolean;`)
	assert.Contains(t, dts, `    "nginx.ingress.kubernetes.io/rewrite-target": string;`)
	assert.Contains(t, dts, `  "my-key": string;`)
	assert.Contains(t, dts, `  ok_key: number;`)
}

func TestImportToURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantURL string
		wantOK  bool
	}{
		{"https_url", "https://github.com/cloudticon/k8s@master", "https://github.com/cloudticon/k8s@master", true},
		{"git_package", "github.com/cloudticon/k8s", "https://github.com/cloudticon/k8s", true},
		{"git_package_with_version", "github.com/cloudticon/k8s@v1.0.0", "https://github.com/cloudticon/k8s@v1.0.0", true},
		{"relative_import", "./helper", "", false},
		{"bare_import", "lodash", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, ok := packages.ImportURL(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantURL, url)
			}
		})
	}
}

func TestGenerateDevDts_EnvAcceptsBooleanDefaults(t *testing.T) {
	dts := generateDevDts(nil, []string{"DEBUG"})

	assert.Contains(t, dts, "declare function env(name: CtEnvKey, defaultValue: boolean): boolean;")
	assert.Contains(t, dts, "declare function env(name: string, defaultValue: boolean): boolean;")
}

func TestCollectUniqueWorkloadNames_UsesDevReleaseName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.ct"), []byte(`
__ct_resources.push({
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { name: Release.name + "-web" },
  spec: { selector: { matchLabels: { app: "web" } } },
});
`), 0o644))

	assert.Equal(t, []string{"dev-web"}, collectUniqueWorkloadNames(dir))
}
