package engine

import (
	"errors"
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
		// Without a usable namespace ct dev would silently fall back to the
		// kubeconfig's default namespace and apply (and prune) there.
		ns, err := optionalString(obj, "namespace")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(ns) == "" {
			return nil, errors.New("namespace must be a non-empty string (check the env()/prompt() values it is built from)")
		}
		result.Namespace = ns
		if v, ok := option(obj, "values"); ok {
			vals, isMap := v.(map[string]interface{})
			if !isMap {
				return nil, fmt.Errorf("values must be an object, got %s", jsTypeName(v))
			}
			result.Values = vals
		}
		return nil, nil
	})
}

func registerDevGlobal(h *JSHelper, result *DevResult) {
	h.DefineFunc("dev", func(args *Args) (interface{}, error) {
		name := args.String(0)
		target, err := parseDevTarget(name, args.Object(1))
		if err != nil {
			// Options of the wrong shape used to be dropped silently (a
			// string command, a "src:dst" sync string, ...), which left
			// the user guessing why nothing happened.
			return nil, fmt.Errorf("target %q: %w", name, err)
		}
		result.Targets = append(result.Targets, target)
		return nil, nil
	})
}

func parseDevTarget(name string, obj map[string]interface{}) (RawDevTarget, error) {
	target := RawDevTarget{Name: name}
	var err error
	if target.Selector, err = optionalSelector(obj); err != nil {
		return target, err
	}
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"container", &target.Container},
		{"terminal", &target.Terminal},
		{"workingDir", &target.WorkingDir},
		{"image", &target.Image},
	} {
		if *field.dst, err = optionalString(obj, field.key); err != nil {
			return target, err
		}
	}
	if target.Sync, err = optionalObjectList(obj, "sync"); err != nil {
		return target, err
	}
	if target.Ports, err = optionalList(obj, "ports"); err != nil {
		return target, err
	}
	if target.Env, err = optionalObjectList(obj, "env"); err != nil {
		return target, err
	}
	if target.Command, err = optionalStringList(obj, "command"); err != nil {
		return target, err
	}
	if target.Probes, err = optionalBool(obj, "probes"); err != nil {
		return target, err
	}
	if target.Replicas, err = optionalInt64(obj, "replicas"); err != nil {
		return target, err
	}
	return target, nil
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

// option returns obj[key]; JS undefined and null count as absent.
func option(obj map[string]interface{}, key string) (interface{}, bool) {
	v, ok := obj[key]
	return v, ok && v != nil
}

func optionalSelector(obj map[string]interface{}) (map[string]string, error) {
	v, ok := option(obj, "selector")
	if !ok {
		return nil, nil
	}
	raw, isMap := v.(map[string]interface{})
	if !isMap {
		return nil, fmt.Errorf("selector must be an object of labels, got %s", jsTypeName(v))
	}
	if len(raw) == 0 {
		return nil, errors.New("selector must not be empty (an empty selector matches every pod in the namespace)")
	}
	result := make(map[string]string, len(raw))
	for k, val := range raw {
		s, ok := scalarString(val)
		if !ok {
			return nil, fmt.Errorf("selector %q must be a string, got %s", k, jsTypeName(val))
		}
		result[k] = s
	}
	return result, nil
}

func optionalString(obj map[string]interface{}, key string) (string, error) {
	v, ok := option(obj, key)
	if !ok {
		return "", nil
	}
	s, isString := v.(string)
	if !isString {
		return "", fmt.Errorf("%s must be a string, got %s", key, jsTypeName(v))
	}
	return s, nil
}

func optionalList(obj map[string]interface{}, key string) ([]interface{}, error) {
	v, ok := option(obj, key)
	if !ok {
		return nil, nil
	}
	list, isList := v.([]interface{})
	if !isList {
		return nil, fmt.Errorf("%s must be an array, got %s", key, jsTypeName(v))
	}
	return list, nil
}

func optionalObjectList(obj map[string]interface{}, key string) ([]map[string]interface{}, error) {
	list, err := optionalList(obj, key)
	if err != nil || list == nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(list))
	for i, item := range list {
		m, isMap := item.(map[string]interface{})
		if !isMap {
			return nil, fmt.Errorf("%s[%d] must be an object, got %s", key, i, jsTypeName(item))
		}
		result = append(result, m)
	}
	return result, nil
}

func optionalStringList(obj map[string]interface{}, key string) ([]string, error) {
	list, err := optionalList(obj, key)
	if err != nil || list == nil {
		return nil, err
	}
	result := make([]string, 0, len(list))
	for i, item := range list {
		s, ok := scalarString(item)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string, got %s", key, i, jsTypeName(item))
		}
		result = append(result, s)
	}
	return result, nil
}

func optionalBool(obj map[string]interface{}, key string) (*bool, error) {
	v, ok := option(obj, key)
	if !ok {
		return nil, nil
	}
	b, isBool := v.(bool)
	if !isBool {
		return nil, fmt.Errorf("%s must be a boolean, got %s", key, jsTypeName(v))
	}
	return &b, nil
}

func optionalInt64(obj map[string]interface{}, key string) (*int64, error) {
	v, ok := option(obj, key)
	if !ok {
		return nil, nil
	}
	switch n := v.(type) {
	case int64:
		return &n, nil
	case float64:
		i := int64(n)
		if float64(i) != n {
			return nil, fmt.Errorf("%s must be an integer, got %v", key, n)
		}
		return &i, nil
	}
	return nil, fmt.Errorf("%s must be a number, got %s", key, jsTypeName(v))
}

// scalarString converts strings, numbers and booleans to their string form.
func scalarString(v interface{}) (string, bool) {
	switch v.(type) {
	case string, int64, float64, bool:
		return fmt.Sprint(v), true
	}
	return "", false
}

func jsTypeName(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null/undefined"
	case string:
		return "string"
	case int64, float64:
		return "number"
	case bool:
		return "boolean"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}
