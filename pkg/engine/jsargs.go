package engine

import (
	"fmt"

	"github.com/dop251/goja"
)

// Args provides typed extraction from Goja function call arguments.
// NOT goroutine-safe -- mirrors the single-threaded Goja VM model.
type Args struct {
	vm     *goja.Runtime
	call   goja.FunctionCall
	fnName string
}

func (a *Args) Len() int               { return len(a.call.Arguments) }
func (a *Args) Raw(idx int) goja.Value { return a.call.Argument(idx) }

func (a *Args) HasArg(idx int) bool {
	if idx >= a.Len() {
		return false
	}
	v := a.call.Argument(idx)
	return !goja.IsUndefined(v) && !goja.IsNull(v)
}

func (a *Args) String(idx int) string {
	v := a.call.Argument(idx)
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

func (a *Args) Int(idx int) int64 {
	return a.call.Argument(idx).ToInteger()
}

// Object returns argument as map. Panics with JS error if not an object.
func (a *Args) Object(idx int) map[string]interface{} {
	v := a.call.Argument(idx)
	if goja.IsUndefined(v) || goja.IsNull(v) {
		panic(a.vm.NewGoError(fmt.Errorf(
			"%s(): argument %d is required", a.fnName, idx,
		)))
	}
	exported := v.Export()
	obj, ok := exported.(map[string]interface{})
	if !ok {
		panic(a.vm.NewGoError(fmt.Errorf(
			"%s(): argument %d: expected object, got %T", a.fnName, idx, exported,
		)))
	}
	return obj
}

// OptionalObject returns (nil, false) if argument is missing/null/undefined.
func (a *Args) OptionalObject(idx int) (map[string]interface{}, bool) {
	if !a.HasArg(idx) {
		return nil, false
	}
	exported := a.call.Argument(idx).Export()
	obj, ok := exported.(map[string]interface{})
	return obj, ok
}

// StringMap converts an object argument to map[string]string.
func (a *Args) StringMap(idx int) map[string]string {
	obj := a.Object(idx)
	result := make(map[string]string, len(obj))
	for k, v := range obj {
		result[k] = fmt.Sprint(v)
	}
	return result
}
