package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/dop251/goja"
)

// valuesGuard exposes the merged values to the script as `Values`, wrapped in
// proxies so that reading a key no values layer declares fails with a
// diagnostic instead of silently giving undefined (a typo like
// Values.replicsa would otherwise fall back to a factory default and render
// with exit code 0). A key declared as null counts as declared.
//
// Only reads count: iteration, Object.keys, spread, `in` and serialization
// work as on a plain object. Reads from imported packages are not checked:
// a package that receives a values object probes its optional keys, and
// that is not the project's typo.
type valuesGuard struct {
	vm        *goja.Runtime
	sourceDir string
	hasOwn    goja.Callable
	// proxies caches one proxy per values path, so Values.image ===
	// Values.image holds.
	proxies map[string]*goja.Object
}

// lenientKeys are read by the language and common helpers on any object
// (JSON.stringify, await-like checks); a missing one is not a typo.
var lenientKeys = map[string]bool{"toJSON": true, "then": true}

func newValuesGuard(vm *goja.Runtime, values map[string]interface{}, sourceDir string) goja.Value {
	g := &valuesGuard{vm: vm, sourceDir: sourceDir, proxies: map[string]*goja.Object{}}
	proto := vm.Get("Object").ToObject(vm).Get("prototype").ToObject(vm)
	if fn, ok := goja.AssertFunction(proto.Get("hasOwnProperty")); ok {
		g.hasOwn = fn
	}
	return g.wrap(vm.ToValue(values).ToObject(vm), "")
}

func (g *valuesGuard) wrap(target *goja.Object, path string) *goja.Object {
	if p, ok := g.proxies[path]; ok {
		return p
	}
	isArray := target.ClassName() == "Array"
	traps := &goja.ProxyTrapConfig{
		Get: func(t *goja.Object, prop string, _ goja.Value) goja.Value {
			return g.get(t, prop, path, isArray)
		},
		Set: func(t *goja.Object, prop string, value goja.Value, _ goja.Value) bool {
			g.forget(childPath(path, prop, isArray))
			return t.Set(prop, value) == nil
		},
		DeleteProperty: func(t *goja.Object, prop string) bool {
			g.forget(childPath(path, prop, isArray))
			return t.Delete(prop) == nil
		},
	}
	if !isArray {
		// Values objects are Go maps underneath, whose key order is random;
		// sort, so Object.keys, for..in and spread give deterministic output.
		traps.OwnKeys = func(t *goja.Object) *goja.Object {
			keys := t.GetOwnPropertyNames()
			sort.Strings(keys)
			items := make([]interface{}, len(keys))
			for i, k := range keys {
				items[i] = k
			}
			return g.vm.NewArray(items...)
		}
	}
	obj := g.vm.ToValue(g.vm.NewProxy(target, traps)).ToObject(g.vm)
	g.proxies[path] = obj
	return obj
}

func (g *valuesGuard) get(t *goja.Object, prop, path string, isArray bool) goja.Value {
	cp := childPath(path, prop, isArray)
	v := t.Get(prop)
	if v == nil {
		if isArray || lenientKeys[prop] || !g.readByProject() {
			return goja.Undefined()
		}
		panic(g.missingKey(t, prop, path))
	}
	if obj, ok := v.(*goja.Object); ok && g.own(t, prop) {
		switch obj.Export().(type) {
		case map[string]interface{}, []interface{}:
			return g.wrap(obj, cp)
		}
	}
	return v
}

func (g *valuesGuard) own(t *goja.Object, prop string) bool {
	if g.hasOwn == nil {
		return false
	}
	res, err := g.hasOwn(t, g.vm.ToValue(prop))
	return err == nil && res.ToBoolean()
}

// forget drops cached proxies at path and below after the script replaced or
// deleted that value.
func (g *valuesGuard) forget(path string) {
	for p := range g.proxies {
		if p == path || strings.HasPrefix(p, path+".") || strings.HasPrefix(p, path+"[") {
			delete(g.proxies, p)
		}
	}
}

// readByProject reports whether the property read happening now comes from
// the project's own code rather than from an imported package.
func (g *valuesGuard) readByProject() bool {
	frames := sourceFrames(g.vm.CaptureCallStack(maxStackFrames, nil), g.sourceDir)
	if len(frames) == 0 {
		return true
	}
	return !isPackagePath(frames[0].File)
}

// missingKey builds the error thrown for an undeclared key. It follows the
// package error convention (code, path, hint), so runtimeDiagnostics turns it
// into a diagnostic at the line that read the key.
func (g *valuesGuard) missingKey(t *goja.Object, prop, parent string) *goja.Object {
	var keys []string
	if m, ok := t.Export().(map[string]interface{}); ok {
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	path := childPath(parent, prop, false)
	msg := fmt.Sprintf("no key %q in %s", prop, valuesExpr(parent))
	hint := fmt.Sprintf("add %s to the values file or pass --set %s=<value>; for an optional key write null in the values file and read it with ?. or ??", path, path)
	if closest := closestName(prop, keys); closest != "" {
		hint = fmt.Sprintf("did you mean %s? If %s is a new key, %s", valuesExpr(childPath(parent, closest, false)), path, hint)
	}

	e := g.vm.NewTypeError(msg)
	_ = e.Set("code", diag.CodeUnknownValuesKey)
	_ = e.Set("path", path)
	_ = e.Set("hint", hint)
	return e
}

// childPath appends prop to a values path: image.tag, workers[0],
// annotations["a.b/c"].
func childPath(path, prop string, inArray bool) string {
	switch {
	case inArray:
		return path + "[" + prop + "]"
	case !IsValidJSIdentifier(prop):
		return path + "[" + strconv.Quote(prop) + "]"
	case path == "":
		return prop
	default:
		return path + "." + prop
	}
}

// valuesExpr renders a values path as the expression that reads it.
func valuesExpr(path string) string {
	if path == "" || strings.HasPrefix(path, "[") {
		return "Values" + path
	}
	return "Values." + path
}
