package engine_test

import (
	"fmt"
	"testing"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/dop251/goja"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefineFunc_BasicCall(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	var called bool
	h.DefineFunc("myFunc", func(args *engine.Args) (interface{}, error) {
		called = true
		assert.Equal(t, "hello", args.String(0))
		return "world", nil
	})

	val, err := vm.RunString(`myFunc("hello")`)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "world", val.Export())
}

func TestDefineFunc_ErrorBecomesJSException(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	h.DefineFunc("failing", func(args *engine.Args) (interface{}, error) {
		return nil, fmt.Errorf("something went wrong")
	})

	_, err := vm.RunString(`failing()`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failing(): something went wrong")
}

func TestDefineFunc_PanicRecovery(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	h.DefineFunc("crasher", func(args *engine.Args) (interface{}, error) {
		var s *string
		_ = *s // nil pointer panic
		return nil, nil
	})

	_, err := vm.RunString(`crasher()`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "crasher()")
}

func TestDefineFunc_NilReturnsUndefined(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	h.DefineFunc("voidFn", func(args *engine.Args) (interface{}, error) {
		return nil, nil
	})

	val, err := vm.RunString(`typeof voidFn()`)
	require.NoError(t, err)
	assert.Equal(t, "undefined", val.Export())
}

func TestDefineFunc_GlobalThisAlias(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	h.DefineFunc("test", func(args *engine.Args) (interface{}, error) {
		return 42, nil
	})

	val, err := vm.RunString(`globalThis.test()`)
	require.NoError(t, err)
	assert.Equal(t, int64(42), val.Export())
}

func TestDefineFunc_MultipleArgs(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	h.DefineFunc("add", func(args *engine.Args) (interface{}, error) {
		return args.Int(0) + args.Int(1), nil
	})

	val, err := vm.RunString(`add(3, 7)`)
	require.NoError(t, err)
	assert.Equal(t, int64(10), val.Export())
}

func TestDefineFunc_GojaExceptionReThrown(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	h.DefineFunc("thrower", func(args *engine.Args) (interface{}, error) {
		_ = args.Object(0) // panics with TypeError for missing arg
		return nil, nil
	})

	_, err := vm.RunString(`thrower()`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "argument 0 is required")
}

func TestDefineValue_String(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)
	h.DefineValue("greeting", "hello")

	val, err := vm.RunString(`greeting`)
	require.NoError(t, err)
	assert.Equal(t, "hello", val.Export())
}

func TestDefineValue_Map(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)
	h.DefineValue("config", map[string]interface{}{"port": int64(8080)})

	val, err := vm.RunString(`config.port`)
	require.NoError(t, err)
	assert.Equal(t, int64(8080), val.Export())
}

func TestDefineValue_GlobalThisAlias(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)
	h.DefineValue("VERSION", "1.0.0")

	val, err := vm.RunString(`globalThis.VERSION`)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", val.Export())
}

func TestDefineArray_HasPush(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)
	h.DefineArray("items")

	_, err := vm.RunString(`items.push({name: "a"}); items.push({name: "b"});`)
	require.NoError(t, err)

	val, err := vm.RunString(`items.length`)
	require.NoError(t, err)
	assert.Equal(t, int64(2), val.Export())
}

func TestDefineArray_IsRealJSArray(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)
	h.DefineArray("arr")

	val, err := vm.RunString(`Array.isArray(arr)`)
	require.NoError(t, err)
	assert.Equal(t, true, val.Export())
}

func TestDefineArray_GlobalThisAlias(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)
	h.DefineArray("list")

	_, err := vm.RunString(`globalThis.list.push(42)`)
	require.NoError(t, err)

	val, err := vm.RunString(`list[0]`)
	require.NoError(t, err)
	assert.Equal(t, int64(42), val.Export())
}

func TestDefineFunc_InvalidIdentifier_Panics(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	assert.Panics(t, func() {
		h.DefineFunc("foo-bar", func(args *engine.Args) (interface{}, error) {
			return nil, nil
		})
	})
}

func TestDefineValue_InvalidIdentifier_Panics(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	assert.Panics(t, func() {
		h.DefineValue("123abc", "value")
	})
}

func TestDefineArray_InvalidIdentifier_Panics(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	assert.Panics(t, func() {
		h.DefineArray("has space")
	})
}

func TestArgs_Object_MissingPanics(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	h.DefineFunc("needsObj", func(args *engine.Args) (interface{}, error) {
		_ = args.Object(0)
		return nil, nil
	})

	_, err := vm.RunString(`needsObj()`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "argument 0 is required")
}

func TestArgs_Object_WrongTypePanics(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	h.DefineFunc("needsObj", func(args *engine.Args) (interface{}, error) {
		_ = args.Object(0)
		return nil, nil
	})

	_, err := vm.RunString(`needsObj("not an object")`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected object")
}

func TestArgs_OptionalObject_MissingReturnsFalse(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	var gotObj bool
	h.DefineFunc("optObj", func(args *engine.Args) (interface{}, error) {
		_, gotObj = args.OptionalObject(0)
		return nil, nil
	})

	_, err := vm.RunString(`optObj()`)
	require.NoError(t, err)
	assert.False(t, gotObj)

	_, err = vm.RunString(`optObj({key: "val"})`)
	require.NoError(t, err)
	assert.True(t, gotObj)
}

func TestArgs_StringMap(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	var result map[string]string
	h.DefineFunc("parseMap", func(args *engine.Args) (interface{}, error) {
		result = args.StringMap(0)
		return nil, nil
	})

	_, err := vm.RunString(`parseMap({a: "1", b: 2, c: true})`)
	require.NoError(t, err)
	assert.Equal(t, "1", result["a"])
	assert.Equal(t, "2", result["b"])
	assert.Equal(t, "true", result["c"])
}

func TestArgs_HasArg(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	var has0, has1, has2 bool
	h.DefineFunc("checkArgs", func(args *engine.Args) (interface{}, error) {
		has0 = args.HasArg(0)
		has1 = args.HasArg(1)
		has2 = args.HasArg(2)
		return nil, nil
	})

	_, err := vm.RunString(`checkArgs("a", 42)`)
	require.NoError(t, err)
	assert.True(t, has0)
	assert.True(t, has1)
	assert.False(t, has2)
}

func TestArgs_HasArg_NullIsNotPresent(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	var has0 bool
	h.DefineFunc("check", func(args *engine.Args) (interface{}, error) {
		has0 = args.HasArg(0)
		return nil, nil
	})

	_, err := vm.RunString(`check(null)`)
	require.NoError(t, err)
	assert.False(t, has0)
}

func TestArgs_Len(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	var argLen int
	h.DefineFunc("countArgs", func(args *engine.Args) (interface{}, error) {
		argLen = args.Len()
		return nil, nil
	})

	_, err := vm.RunString(`countArgs(1, 2, 3)`)
	require.NoError(t, err)
	assert.Equal(t, 3, argLen)
}

func TestArgs_String_ReturnsEmptyForMissing(t *testing.T) {
	vm := goja.New()
	h := engine.NewJSHelper(vm)

	var result string
	h.DefineFunc("getStr", func(args *engine.Args) (interface{}, error) {
		result = args.String(5) // way beyond args
		return nil, nil
	})

	_, err := vm.RunString(`getStr()`)
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

func TestIsValidJSIdentifier(t *testing.T) {
	assert.True(t, engine.IsValidJSIdentifier("config"))
	assert.True(t, engine.IsValidJSIdentifier("_private"))
	assert.True(t, engine.IsValidJSIdentifier("$var"))
	assert.True(t, engine.IsValidJSIdentifier("camelCase123"))
	assert.True(t, engine.IsValidJSIdentifier("__ct_resources"))
	assert.False(t, engine.IsValidJSIdentifier(""))
	assert.False(t, engine.IsValidJSIdentifier("foo-bar"))
	assert.False(t, engine.IsValidJSIdentifier("123abc"))
	assert.False(t, engine.IsValidJSIdentifier("has space"))
}
