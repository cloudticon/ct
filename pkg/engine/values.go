package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cloudticon/ct/pkg/diag"
	"gopkg.in/yaml.v3"
)

// ValuesOpts lists value sources in precedence order: Files are deep-merged
// left to right (maps merge, everything else is replaced), then Set and
// SetString overrides are applied in order.
type ValuesOpts struct {
	Files []string
	// Set holds key=value overrides; values that look like numbers, booleans
	// or null are typed (see parseValue).
	Set []string
	// SetString holds key=value overrides that always stay strings.
	SetString []string
}

// LoadValues builds the Values object for a render. With no sources it
// returns an empty map.
func LoadValues(opts ValuesOpts) (map[string]interface{}, error) {
	values := map[string]interface{}{}
	for _, path := range opts.Files {
		fileValues, err := readValuesFile(path)
		if err != nil {
			return nil, err
		}
		mergeValues(values, fileValues)
	}

	if err := applySetOverrides(values, "--set", opts.Set, parseValue); err != nil {
		return nil, err
	}
	if err := applySetOverrides(values, "--set-string", opts.SetString, func(s string) interface{} { return s }); err != nil {
		return nil, err
	}
	return values, nil
}

// LoadValuesFile loads a single values file and applies --set overrides.
func LoadValuesFile(path string, setOverrides []string) (map[string]interface{}, error) {
	return LoadValues(ValuesOpts{Files: []string{path}, Set: setOverrides})
}

func readValuesFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, diag.List{{
			Code:    diag.CodeValues,
			Message: fmt.Sprintf("reading values file: %v", err),
			File:    path,
		}}
	}

	var values map[string]interface{}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, jsonValuesDiag(path, data, err)
		}
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &values); err != nil {
			return nil, yamlValuesDiag(path, data, err)
		}
	default:
		return nil, diag.List{{
			Code:    diag.CodeValues,
			Message: fmt.Sprintf("unsupported values file format %q", ext),
			File:    path,
			Hint:    "values files must be .json, .yaml or .yml",
		}}
	}

	// An empty YAML file or a JSON `null` decodes to a nil map.
	if values == nil {
		values = map[string]interface{}{}
	}
	normalizeNumbers(values)
	return values, nil
}

// mergeValues deep-merges src into dst. Nested maps merge key by key; any
// other value (including arrays) replaces what dst had, like Helm.
func mergeValues(dst, src map[string]interface{}) {
	for k, v := range src {
		srcMap, srcIsMap := v.(map[string]interface{})
		dstMap, dstIsMap := dst[k].(map[string]interface{})
		if srcIsMap && dstIsMap {
			mergeValues(dstMap, srcMap)
			continue
		}
		dst[k] = v
	}
}

// normalizeNumbers converts float64 whole numbers (from JSON) and int (from YAML)
// to int64 for consistent downstream behavior.
func normalizeNumbers(m map[string]interface{}) {
	for k, v := range m {
		m[k] = normalizeValue(v)
	}
}

func normalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case float64:
		if val == float64(int64(val)) {
			return int64(val)
		}
		return val
	case int:
		return int64(val)
	case map[string]interface{}:
		normalizeNumbers(val)
		return val
	case []interface{}:
		for i, item := range val {
			val[i] = normalizeValue(item)
		}
		return val
	default:
		return val
	}
}

func applySetOverrides(values map[string]interface{}, flag string, overrides []string, parse func(string) interface{}) error {
	for _, override := range overrides {
		key, rawValue, ok := strings.Cut(override, "=")
		if !ok || key == "" {
			return diag.List{{
				Code:    diag.CodeValues,
				Message: fmt.Sprintf("invalid %s format: %q (expected key=value)", flag, override),
				Hint:    fmt.Sprintf("use %s replicas=3 or %s app.image=nginx:1.25", flag, flag),
			}}
		}
		path, err := splitKeyPath(key)
		if err != nil {
			return diag.List{{
				Code:    diag.CodeValues,
				Message: fmt.Sprintf("invalid %s key in %q: %v", flag, override, err),
				Hint:    fmt.Sprintf("use dots for nesting, e.g. %s app.image=nginx:1.25; escape a literal dot as \\.", flag),
			}}
		}
		setNestedValue(values, path, parse(rawValue))
	}
	return nil
}

// splitKeyPath splits a --set key on dots. A backslash escapes a dot, so
// `annotations.nginx\.ingress\.kubernetes\.io/rewrite-target` addresses one
// annotation key.
func splitKeyPath(key string) ([]string, error) {
	var path []string
	var cur strings.Builder
	for i := 0; i < len(key); i++ {
		switch c := key[i]; {
		case c == '\\' && i+1 < len(key) && key[i+1] == '.':
			cur.WriteByte('.')
			i++
		case c == '.':
			path = append(path, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	path = append(path, cur.String())
	for _, segment := range path {
		if segment == "" {
			return nil, fmt.Errorf("empty path segment in %q", key)
		}
	}
	return path, nil
}

func setNestedValue(obj map[string]interface{}, keys []string, value interface{}) {
	for i, k := range keys {
		if i == len(keys)-1 {
			obj[k] = value
			return
		}
		next, ok := obj[k].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			obj[k] = next
		}
		obj = next
	}
}

// parseValue types a --set value. Numbers are only typed when they survive a
// round trip unchanged, so image tags like "1.10" or "1.0" and zero-padded
// strings like "0123" stay strings instead of silently becoming 1.1, 1 or 123.
func parseValue(s string) interface{} {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil && strconv.FormatInt(i, 10) == s {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) &&
		strconv.FormatFloat(f, 'f', -1, 64) == s {
		return f
	}
	return s
}

// jsonValuesDiag turns a JSON decode error into a diagnostic with the line,
// column and text of the offending spot when the decoder reports an offset.
func jsonValuesDiag(path string, data []byte, err error) diag.List {
	d := diag.Diagnostic{Code: diag.CodeValues, Message: "invalid JSON: " + err.Error(), File: path}
	var offset int64 = -1
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		offset = syn.Offset
		d.Message = "invalid JSON: " + syn.Error()
	case errors.As(err, &typ):
		offset = typ.Offset
		d.Message = "invalid values: " + typ.Error()
		d.Hint = "a values file must contain an object at the top level"
	}
	if offset > 0 {
		// Offset counts bytes read, so the offending byte is offset-1.
		d.Line, d.Column, d.LineText = offsetPosition(data, int(offset)-1)
	}
	return diag.List{d}
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// yamlValuesDiag extracts the line from a yaml.v3 error ("yaml: line 3: ...").
func yamlValuesDiag(path string, data []byte, err error) diag.List {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	d := diag.Diagnostic{Code: diag.CodeValues, File: path}
	if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
		d.Line, _ = strconv.Atoi(m[1])
		msg = strings.TrimSpace(strings.Replace(msg, m[0]+":", "", 1))
		lines := strings.Split(string(data), "\n")
		if d.Line >= 1 && d.Line <= len(lines) {
			d.LineText = strings.TrimRight(lines[d.Line-1], "\r")
		}
	}
	d.Message = "invalid YAML: " + msg
	d.Hint = "check indentation: YAML uses spaces (no tabs) and nested keys are indented under their parent"
	return diag.List{d}
}

// offsetPosition maps a byte offset to a 1-based line, column and the line text.
func offsetPosition(data []byte, off int) (line, col int, text string) {
	if off < 0 {
		off = 0
	}
	if off > len(data) {
		off = len(data)
	}
	line = 1 + strings.Count(string(data[:off]), "\n")
	start := strings.LastIndex(string(data[:off]), "\n") + 1
	col = off - start + 1
	end := strings.Index(string(data[start:]), "\n")
	if end < 0 {
		end = len(data) - start
	}
	return line, col, strings.TrimRight(string(data[start:start+end]), "\r")
}
