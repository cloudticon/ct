package engine_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptCache_GetSetRoundtrip(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	cache := engine.NewPromptCacheFromPath(cachePath)

	_, ok := cache.Get("question?")
	assert.False(t, ok)

	require.NoError(t, cache.Set("question?", "answer"))

	val, ok := cache.Get("question?")
	assert.True(t, ok)
	assert.Equal(t, "answer", val)
}

func TestPromptCache_PersistsToDisk(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	cache1 := engine.NewPromptCacheFromPath(cachePath)
	require.NoError(t, cache1.Set("q1", "a1"))

	cache2 := engine.NewPromptCacheFromPath(cachePath)
	val, ok := cache2.Get("q1")
	assert.True(t, ok)
	assert.Equal(t, "a1", val)
}

func TestPromptCache_MultipleEntries(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	cache := engine.NewPromptCacheFromPath(cachePath)

	require.NoError(t, cache.Set("q1", "a1"))
	require.NoError(t, cache.Set("q2", "a2"))

	v1, _ := cache.Get("q1")
	v2, _ := cache.Get("q2")
	assert.Equal(t, "a1", v1)
	assert.Equal(t, "a2", v2)
}

func TestPromptCache_OverwriteExisting(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	cache := engine.NewPromptCacheFromPath(cachePath)

	require.NoError(t, cache.Set("q", "old"))
	require.NoError(t, cache.Set("q", "new"))

	val, _ := cache.Get("q")
	assert.Equal(t, "new", val)
}

func TestPromptCache_EmptyFileHandled(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	os.WriteFile(cachePath, []byte(""), 0o644)

	cache := engine.NewPromptCacheFromPath(cachePath)
	_, ok := cache.Get("anything")
	assert.False(t, ok)
}

func TestNewPromptCache_CreatesDirectory(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	cache, err := engine.NewPromptCache("/some/project/dir")
	require.NoError(t, err)
	require.NotNil(t, cache)

	cacheDir := filepath.Join(tmpHome, ".ct", "prompt_cache")
	info, err := os.Stat(cacheDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestMakePromptFn_UsesCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	cache := engine.NewPromptCacheFromPath(cachePath)
	require.NoError(t, cache.Set("Name?", "cached_name"))

	var output bytes.Buffer
	fn := engine.MakePromptFn(cache, strings.NewReader(""), &output)

	answer, err := fn("Name?")
	require.NoError(t, err)
	assert.Equal(t, "cached_name", answer)
	assert.Contains(t, output.String(), "[cached: cached_name]")
}

func TestMakePromptFn_ReadsStdin(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	cache := engine.NewPromptCacheFromPath(cachePath)

	input := strings.NewReader("my_input\n")
	var output bytes.Buffer
	fn := engine.MakePromptFn(cache, input, &output)

	answer, err := fn("What?")
	require.NoError(t, err)
	assert.Equal(t, "my_input", answer)
	assert.Contains(t, output.String(), "What?: ")

	cached, ok := cache.Get("What?")
	assert.True(t, ok)
	assert.Equal(t, "my_input", cached)
}

func TestMakePromptFn_TrimWhitespace(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	cache := engine.NewPromptCacheFromPath(cachePath)

	input := strings.NewReader("  spaced  \n")
	var output bytes.Buffer
	fn := engine.MakePromptFn(cache, input, &output)

	answer, err := fn("Q?")
	require.NoError(t, err)
	assert.Equal(t, "spaced", answer)
}
