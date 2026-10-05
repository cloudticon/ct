package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/dop251/goja"
)

type Resource = map[string]interface{}

type ExecuteOpts struct {
	JSCode      string
	Values      map[string]interface{}
	Namespace   string
	ReleaseName string
	// SourceDir is the project directory; source positions in errors are
	// shown relative to it.
	SourceDir string
	// Timeout aborts scripts that run longer, e.g. an endless loop. Zero
	// means no limit.
	Timeout time.Duration
}

// Result is a rendered release.
type Result struct {
	Resources []Resource
	// Origins[i] is the call chain in the user's own files (innermost first,
	// imported packages left out) that registered Resources[i].
	Origins [][]diag.Frame
}

// Origin returns the call chain that registered resource i, or nil.
func (r *Result) Origin(i int) []diag.Frame {
	if i < len(r.Origins) {
		return r.Origins[i]
	}
	return nil
}

// Execute runs a bundle and returns the registered resources.
func Execute(opts ExecuteOpts) ([]Resource, error) {
	result, err := Render(opts)
	if err != nil {
		return nil, err
	}
	return result.Resources, nil
}

// Render runs a bundle and returns the registered resources together with
// their source origins. Errors are diag.List values with .ct positions.
func Render(opts ExecuteOpts) (*Result, error) {
	vm := goja.New()
	origins := injectGlobals(vm, opts)

	prg, err := goja.Compile(bundleName, opts.JSCode, false)
	if err != nil {
		return nil, diag.Errorf(diag.CodeSyntax, "compiling bundle: %v", err)
	}

	if opts.Timeout > 0 {
		timer := time.AfterFunc(opts.Timeout, func() { vm.Interrupt(opts.Timeout) })
		defer timer.Stop()
	}

	if _, err := vm.RunProgram(prg); err != nil {
		return nil, runtimeDiagnostics(err, opts.SourceDir)
	}

	resources, err := extractResources(vm)
	if err != nil {
		return nil, diag.Errorf(diag.CodeInvalidResource, "%v", err)
	}

	result := &Result{Resources: resources}
	if len(*origins) == len(resources) {
		result.Origins = *origins
	}

	manifest.Normalize(resources, opts.Namespace)
	if err := checkDuplicates(result); err != nil {
		return nil, err
	}
	return result, nil
}

const bundleName = "ct-bundle.js"

func checkDuplicates(result *Result) error {
	dups := manifest.FindDuplicates(result.Resources)
	if len(dups) == 0 {
		return nil
	}
	list := make(diag.List, 0, len(dups))
	for _, d := range dups {
		var where []string
		for _, idx := range d.Indexes {
			if o := result.Origin(idx); o != nil {
				where = append(where, diag.Chain(o))
			} else {
				where = append(where, fmt.Sprintf("resource #%d", idx+1))
			}
		}
		list = append(list, diag.Diagnostic{
			Code:     diag.CodeDuplicate,
			Message:  fmt.Sprintf("registered %d times (%s)", len(d.Indexes), strings.Join(where, "; ")),
			Resource: d.Ref,
			Hint:     "each object must be registered once; give the copies different names or namespaces, or register it in one place",
		}.AtChain(result.Origin(d.Indexes[len(d.Indexes)-1])))
	}
	return list
}

func injectGlobals(vm *goja.Runtime, opts ExecuteOpts) *[][]diag.Frame {
	h := NewJSHelper(vm)
	h.DefineArray("__ct_resources")
	values := opts.Values
	if values == nil {
		values = map[string]interface{}{}
	}
	h.DefineValue("Values", values)
	h.DefineValue("Release", map[string]interface{}{
		"name":      opts.ReleaseName,
		"namespace": opts.Namespace,
	})
	return trackRegistrations(vm, opts.SourceDir)
}

// trackRegistrations wraps __ct_resources.push to remember which line of the
// user's code registered each object, so later errors can point there.
func trackRegistrations(vm *goja.Runtime, sourceDir string) *[][]diag.Frame {
	origins := &[][]diag.Frame{}
	arr := vm.Get("__ct_resources").ToObject(vm)
	arrayPush, ok := goja.AssertFunction(vm.Get("Array").ToObject(vm).Get("prototype").ToObject(vm).Get("push"))
	if !ok {
		return origins
	}
	_ = arr.DefineDataProperty("push", vm.ToValue(func(call goja.FunctionCall) goja.Value {
		origin := userChain(sourceFrames(vm.CaptureCallStack(0, nil), sourceDir))
		for range call.Arguments {
			*origins = append(*origins, origin)
		}
		res, err := arrayPush(call.This, call.Arguments...)
		if err != nil {
			panic(err)
		}
		return res
	}), goja.FLAG_TRUE, goja.FLAG_FALSE, goja.FLAG_FALSE)
	return origins
}

// userChain picks the call chain to blame for a registration, innermost
// first: frames in the project directory, else any frames outside imported
// packages, else the innermost frame.
func userChain(frames []diag.Frame) []diag.Frame {
	for _, keep := range []func(string) bool{isProjectPath, func(p string) bool { return !isPackagePath(p) }} {
		var chain []diag.Frame
		for _, f := range frames {
			if keep(f.File) {
				chain = append(chain, f)
			}
		}
		if len(chain) > 0 {
			return chain
		}
	}
	if len(frames) > 0 {
		return frames[:1]
	}
	return nil
}

// sourceFrames converts goja frames (already mapped through the bundle's
// source map) to diag frames, dropping native and bundle-wrapper frames.
func sourceFrames(stack []goja.StackFrame, sourceDir string) []diag.Frame {
	var frames []diag.Frame
	for _, f := range stack {
		pos := f.Position()
		if pos.Filename == "" || pos.Filename == bundleName || pos.Line == 0 {
			continue
		}
		fn := f.FuncName()
		if fn == "<anonymous>" {
			fn = ""
		}
		frames = append(frames, diag.Frame{
			File:     DisplayPath(pos.Filename, sourceDir),
			Line:     pos.Line,
			Column:   pos.Column,
			Function: fn,
		})
	}
	return frames
}

// isPackagePath reports whether a display path points into an imported
// package (DisplayPath renders those as host/owner/repo@version/...).
func isPackagePath(p string) bool {
	host, _, _ := strings.Cut(p, "/")
	return strings.Contains(host, ".") && strings.Contains(p, "@")
}

// isProjectPath reports whether a display path is inside the project
// (DisplayPath renders those relative to it).
func isProjectPath(p string) bool {
	return p != "" && !filepath.IsAbs(p) && !strings.HasPrefix(p, "..") && !isPackagePath(p)
}

func runtimeDiagnostics(err error, sourceDir string) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		d := diag.Diagnostic{
			Code:    diag.CodeTimeout,
			Message: fmt.Sprintf("rendering did not finish within %v", interrupted.Value()),
			Hint:    "look for an endless loop or recursion around the frames below",
			Stack:   sourceFrames(interrupted.Stack(), sourceDir),
		}
		return diag.List{d.At(userFrame(d.Stack))}
	}

	var ex *goja.Exception
	if errors.As(err, &ex) {
		d := diag.Diagnostic{
			Code:    diag.CodeRuntime,
			Message: exceptionMessage(ex),
			Stack:   sourceFrames(ex.Stack(), sourceDir),
		}
		if strings.Contains(d.Message, "of undefined") || strings.Contains(d.Message, "of null") {
			d.Hint = "something on that line is undefined; missing Values keys are a common cause: add them to values.json/values.yaml or pass --set, and use ?. for optional ones"
		}
		return diag.List{d.At(userFrame(d.Stack))}
	}
	return diag.Errorf(diag.CodeRuntime, "%v", err)
}

// userFrame is where an error is best fixed: the innermost frame of the
// user's call chain.
func userFrame(frames []diag.Frame) *diag.Frame {
	if chain := userChain(frames); len(chain) > 0 {
		return &chain[0]
	}
	return nil
}

func exceptionMessage(ex *goja.Exception) string {
	if v := ex.Value(); v != nil {
		return v.String()
	}
	return ex.Error()
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
		// goja exports one JS object as one Go map wherever it appears, and
		// Values maps are shared with the script. Copy, so normalizing one
		// object (e.g. defaulting its namespace) can't leak into another.
		copied, err := copyValue(res, map[uintptr]bool{})
		if err != nil {
			return nil, fmt.Errorf("resource at index %d: %w", i, err)
		}
		resources = append(resources, copied.(map[string]interface{}))
	}

	return resources, nil
}

// copyValue deep-copies maps and slices, rejecting cycles (which can't be
// rendered as YAML/JSON anyway).
func copyValue(v interface{}, onPath map[uintptr]bool) (interface{}, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		id := reflect.ValueOf(val).Pointer()
		if onPath[id] {
			return nil, fmt.Errorf("object contains a reference to itself")
		}
		onPath[id] = true
		defer delete(onPath, id)
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			c, err := copyValue(item, onPath)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			c, err := copyValue(item, onPath)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	default:
		return v, nil
	}
}
