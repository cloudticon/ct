package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
		return nil, fmt.Errorf("reading values file %s: %w", path, err)
	}

	var values map[string]interface{}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, fmt.Errorf("parsing JSON values from %s: %w", path, err)
		}
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &values); err != nil {
			return nil, fmt.Errorf("parsing YAML values from %s: %w", path, err)
		}
	default:
		return nil, fmt.Errorf("unsupported values file format: %s (expected .json, .yaml, or .yml)", ext)
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
			return fmt.Errorf("invalid %s format: %q (expected key=value)", flag, override)
		}
		path, err := splitKeyPath(key)
		if err != nil {
			return fmt.Errorf("invalid %s key in %q: %w", flag, override, err)
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
