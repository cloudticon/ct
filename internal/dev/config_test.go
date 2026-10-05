package dev_test

import (
	"testing"

	"github.com/cloudticon/ct/internal/dev"
	"github.com/stretchr/testify/assert"
)

func TestDeepMergeValues_FlatOverride(t *testing.T) {
	base := map[string]interface{}{"a": "base_a", "b": "base_b"}
	overlay := map[string]interface{}{"b": "overlay_b", "c": "overlay_c"}

	result := dev.DeepMergeValues(base, overlay)

	assert.Equal(t, "base_a", result["a"])
	assert.Equal(t, "overlay_b", result["b"])
	assert.Equal(t, "overlay_c", result["c"])
}

func TestDeepMergeValues_NestedMerge(t *testing.T) {
	base := map[string]interface{}{
		"outer": map[string]interface{}{
			"keep":     "original",
			"override": "old",
		},
	}
	overlay := map[string]interface{}{
		"outer": map[string]interface{}{
			"override": "new",
			"added":    "extra",
		},
	}

	result := dev.DeepMergeValues(base, overlay)
	outer := result["outer"].(map[string]interface{})

	assert.Equal(t, "original", outer["keep"])
	assert.Equal(t, "new", outer["override"])
	assert.Equal(t, "extra", outer["added"])
}

func TestDeepMergeValues_DeeplyNested(t *testing.T) {
	base := map[string]interface{}{
		"l1": map[string]interface{}{
			"l2": map[string]interface{}{
				"keep": "yes",
			},
		},
	}
	overlay := map[string]interface{}{
		"l1": map[string]interface{}{
			"l2": map[string]interface{}{
				"add": "new",
			},
		},
	}

	result := dev.DeepMergeValues(base, overlay)
	l2 := result["l1"].(map[string]interface{})["l2"].(map[string]interface{})

	assert.Equal(t, "yes", l2["keep"])
	assert.Equal(t, "new", l2["add"])
}

func TestDeepMergeValues_OverlayReplacesNonMap(t *testing.T) {
	base := map[string]interface{}{"key": "string_value"}
	overlay := map[string]interface{}{
		"key": map[string]interface{}{"nested": true},
	}

	result := dev.DeepMergeValues(base, overlay)
	assert.Equal(t, map[string]interface{}{"nested": true}, result["key"])
}

func TestDeepMergeValues_MapReplacedByScalar(t *testing.T) {
	base := map[string]interface{}{
		"key": map[string]interface{}{"nested": true},
	}
	overlay := map[string]interface{}{"key": "flat"}

	result := dev.DeepMergeValues(base, overlay)
	assert.Equal(t, "flat", result["key"])
}

func TestDeepMergeValues_DoesNotMutateInputs(t *testing.T) {
	base := map[string]interface{}{"a": "1"}
	overlay := map[string]interface{}{"b": "2"}

	dev.DeepMergeValues(base, overlay)

	_, hasB := base["b"]
	assert.False(t, hasB, "base should not be mutated")
	_, hasA := overlay["a"]
	assert.False(t, hasA, "overlay should not be mutated")
}

func TestDeepMergeValues_NilBase(t *testing.T) {
	result := dev.DeepMergeValues(nil, map[string]interface{}{"a": int64(1)})
	assert.Equal(t, int64(1), result["a"])
}

func TestDeepMergeValues_NilOverlay(t *testing.T) {
	result := dev.DeepMergeValues(map[string]interface{}{"a": int64(1)}, nil)
	assert.Equal(t, int64(1), result["a"])
}

func TestDeepMergeValues_BothNil(t *testing.T) {
	result := dev.DeepMergeValues(nil, nil)
	assert.NotNil(t, result)
	assert.Empty(t, result)
}

func TestDeepMergeValues_EmptyMaps(t *testing.T) {
	result := dev.DeepMergeValues(map[string]interface{}{}, map[string]interface{}{})
	assert.NotNil(t, result)
	assert.Empty(t, result)
}
