package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

// ExecuteDevOpts configures dev.ct execution.
type ExecuteDevOpts struct {
	JSCode   string
	EnvVars  map[string]string
	PromptFn func(question string) (string, error)
}

// DevResult holds the parsed output of a dev.ct execution.
type DevResult struct {
	Namespace string
	Values    map[string]interface{}
	Targets   []RawDevTarget
}

// RawDevTarget stores unparsed dev target data straight from the JS runtime.
// Converted to dev.Target later by the runner.
type RawDevTarget struct {
	Name       string
	Selector   map[string]string
	Container  string
	Sync       []map[string]interface{}
	Ports      []interface{} // number or [number, number]
	Terminal   string
	Probes     *bool
	Replicas   *int64
	Env        []map[string]interface{}
	WorkingDir string
	Image      string
	Command    []string
}

// ExecuteDev runs dev.ct JS code with injected globals: config, dev, env, prompt.
func ExecuteDev(opts ExecuteDevOpts) (*DevResult, error) {
	vm := goja.New()
	h := NewJSHelper(vm)
	result := &DevResult{}

	registerConfigGlobal(h, result)
	registerDevGlobal(h, result)
	registerEnvGlobal(h, opts.EnvVars)
	registerPromptGlobal(h, opts.PromptFn)

	if _, err := vm.RunString(opts.JSCode); err != nil {
		return nil, fmt.Errorf("dev.ct execution error: %w", err)
	}
	return result, nil
}

func registerConfigGlobal(h *JSHelper, result *DevResult) {
	h.DefineFunc("config", func(args *Args) (interface{}, error) {
		obj := args.Object(0)
		if ns, ok := obj["namespace"].(string); ok {
			result.Namespace = ns
		}
		if vals, ok := obj["values"].(map[string]interface{}); ok {
			result.Values = vals
		}
		return nil, nil
	})
}

func registerDevGlobal(h *JSHelper, result *DevResult) {
	h.DefineFunc("dev", func(args *Args) (interface{}, error) {
		name := args.String(0)
		obj := args.Object(1)
		target := RawDevTarget{Name: name}

		if sel, ok := obj["selector"]; ok && sel != nil {
			target.Selector = rawToStringMap(sel)
		}
		if container, ok := obj["container"].(string); ok {
			target.Container = container
		}
		if term, ok := obj["terminal"].(string); ok {
			target.Terminal = term
		}

		target.Sync = extractSyncRules(obj)
		target.Ports = extractPorts(obj)
		target.Probes = extractOptionalBool(obj, "probes")
		target.Replicas = extractOptionalInt64(obj, "replicas")
		target.Env = extractObjectSlice(obj, "env")
		if wd, ok := obj["workingDir"].(string); ok {
			target.WorkingDir = wd
		}
		if img, ok := obj["image"].(string); ok {
			target.Image = img
		}
		target.Command = extractStringSlice(obj, "command")

		result.Targets = append(result.Targets, target)
		return nil, nil
	})
}

func registerEnvGlobal(h *JSHelper, envVars map[string]string) {
	h.DefineFunc("env", func(args *Args) (interface{}, error) {
		name := args.String(0)
		val, exists := envVars[name]
		if !exists {
			if args.HasArg(1) {
				return args.Raw(1).Export(), nil
			}
			return "", nil
		}
		if args.HasArg(1) {
			v, err := coerceToType(val, args.Raw(1).Export())
			if err != nil {
				return nil, fmt.Errorf("%s=%q: %w", name, val, err)
			}
			return v, nil
		}
		return val, nil
	})
}

func registerPromptGlobal(h *JSHelper, promptFn func(string) (string, error)) {
	h.DefineFunc("prompt", func(args *Args) (interface{}, error) {
		return promptFn(args.String(0))
	})
}

// coerceToType parses val to the type of defaultVal (number or boolean; any
// other default returns val unchanged). An empty value means "not set" for
// typed defaults (`PORT=` placeholders in .env files). A value that cannot be
// parsed is an error: returning the raw string would only fail later and far
// away, and the string "false" is truthy in JS.
func coerceToType(val string, defaultVal interface{}) (interface{}, error) {
	switch defaultVal.(type) {
	case int64, float64:
		s := strings.TrimSpace(val)
		if s == "" {
			return defaultVal, nil
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return f, nil
		}
		return nil, fmt.Errorf("value is not a number (the default %v is a number)", defaultVal)
	case bool:
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "":
			return defaultVal, nil
		case "true", "1", "yes", "on":
			return true, nil
		case "false", "0", "no", "off":
			return false, nil
		}
		return nil, fmt.Errorf("value is not a boolean (use true/false, 1/0, yes/no or on/off; the default %v is a boolean)", defaultVal)
	}
	return val, nil
}

func rawToStringMap(v interface{}) map[string]string {
	raw, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	result := make(map[string]string, len(raw))
	for k, val := range raw {
		result[k] = fmt.Sprint(val)
	}
	return result
}

func extractSyncRules(obj map[string]interface{}) []map[string]interface{} {
	raw, ok := obj["sync"].([]interface{})
	if !ok {
		return nil
	}
	rules := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			rules = append(rules, m)
		}
	}
	return rules
}

func extractPorts(obj map[string]interface{}) []interface{} {
	raw, ok := obj["ports"].([]interface{})
	if !ok {
		return nil
	}
	return raw
}

func extractOptionalBool(obj map[string]interface{}, key string) *bool {
	v, ok := obj[key]
	if !ok || v == nil {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

func extractOptionalInt64(obj map[string]interface{}, key string) *int64 {
	v, ok := obj[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case int64:
		return &n
	case float64:
		i := int64(n)
		return &i
	}
	return nil
}

func extractObjectSlice(obj map[string]interface{}, key string) []map[string]interface{} {
	raw, ok := obj[key].([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			result = append(result, m)
		}
	}
	return result
}

func extractStringSlice(obj map[string]interface{}, key string) []string {
	raw, ok := obj[key].([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		result = append(result, fmt.Sprint(item))
	}
	return result
}
