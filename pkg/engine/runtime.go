package engine

import (
	"fmt"
	"strings"

	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/dop251/goja"
)

type Resource = map[string]interface{}

type ExecuteOpts struct {
	JSCode      string
	Values      map[string]interface{}
	Namespace   string
	ReleaseName string
}

func Execute(opts ExecuteOpts) ([]Resource, error) {
	vm := goja.New()

	injectGlobals(vm, opts.Values, opts.ReleaseName, opts.Namespace)

	if _, err := vm.RunString(opts.JSCode); err != nil {
		return nil, fmt.Errorf("JS execution error: %w", err)
	}

	resources, err := extractResources(vm)
	if err != nil {
		return nil, fmt.Errorf("failed to extract resources: %w", err)
	}

	manifest.Normalize(resources, opts.Namespace)
	if err := checkDuplicates(resources); err != nil {
		return nil, err
	}
	return resources, nil
}

func checkDuplicates(resources []Resource) error {
	dups := manifest.FindDuplicates(resources)
	if len(dups) == 0 {
		return nil
	}
	msgs := make([]string, len(dups))
	for i, d := range dups {
		positions := make([]string, len(d.Indexes))
		for j, idx := range d.Indexes {
			positions[j] = fmt.Sprintf("#%d", idx+1)
		}
		msgs[i] = fmt.Sprintf("%s is registered %d times (resources %s)", d.Ref, len(d.Indexes), strings.Join(positions, ", "))
	}
	return fmt.Errorf("duplicate resources: %s", strings.Join(msgs, "; "))
}

func injectGlobals(vm *goja.Runtime, values map[string]interface{}, releaseName, namespace string) {
	h := NewJSHelper(vm)
	h.DefineArray("__ct_resources")
	if values == nil {
		values = map[string]interface{}{}
	}
	h.DefineValue("Values", values)
	h.DefineValue("Release", map[string]interface{}{
		"name":      releaseName,
		"namespace": namespace,
	})
}

func extractResources(vm *goja.Runtime) ([]Resource, error) {
	val := vm.GlobalObject().Get("__ct_resources")
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil, fmt.Errorf("__ct_resources is not defined")
	}

	exported := val.Export()
	arr, ok := exported.([]interface{})
	if !ok {
		return nil, fmt.Errorf("__ct_resources is not an array, got %T", exported)
	}

	resources := make([]Resource, 0, len(arr))
	for i, item := range arr {
		res, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("resource at index %d is not an object", i)
		}
		resources = append(resources, res)
	}

	return resources, nil
}
