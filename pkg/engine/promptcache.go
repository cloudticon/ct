package engine

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PromptCache stores prompt answers in a JSON file keyed by project directory hash.
type PromptCache struct {
	path string
	data map[string]string
}

// NewPromptCache creates a cache file at ~/.ct/prompt_cache/<project-hash>.json.
func NewPromptCache(projectDir string) (*PromptCache, error) {
	hash := sha256.Sum256([]byte(projectDir))
	hexHash := hex.EncodeToString(hash[:8])

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home dir: %w", err)
	}

	// Answers may be secrets: keep the cache private to the user.
	cacheDir := filepath.Join(homeDir, ".ct", "prompt_cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	return NewPromptCacheFromPath(filepath.Join(cacheDir, hexHash+".json")), nil
}

// NewPromptCacheFromPath creates a cache using an explicit file path (for testing).
func NewPromptCacheFromPath(cachePath string) *PromptCache {
	c := &PromptCache{path: cachePath}
	if raw, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(raw, &c.data)
	}
	if c.data == nil { // missing, corrupt or "null" file
		c.data = make(map[string]string)
	}
	return c
}

func (c *PromptCache) Get(question string) (string, bool) {
	val, ok := c.data[question]
	return val, ok
}

// Set stores the answer and persists the cache to disk.
func (c *PromptCache) Set(question, answer string) error {
	c.data[question] = answer
	raw, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.path, raw, 0o600); err != nil {
		return err
	}
	// WriteFile keeps the mode of an existing file; tighten older caches.
	return os.Chmod(c.path, 0o600)
}

// MakePromptFn returns a prompt function that reads stdin with cache support.
func MakePromptFn(cache *PromptCache, reader io.Reader, writer io.Writer) func(string) (string, error) {
	// One buffered reader for all prompts: a reader per prompt reads ahead
	// and swallows the answers to the following prompts when stdin is piped.
	in := bufio.NewReader(reader)
	return func(question string) (string, error) {
		if cached, ok := cache.Get(question); ok {
			fmt.Fprintf(writer, "%s [cached: %s] (change it in %s)\n", question, cached, cache.path)
			return cached, nil
		}
		fmt.Fprintf(writer, "%s: ", question)
		line, err := in.ReadString('\n')
		if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
			if errors.Is(err, io.EOF) {
				// Do not cache anything: an empty answer would be reused
				// silently by every later run.
				key, _ := json.Marshal(question)
				return "", fmt.Errorf("no answer for prompt %q: stdin is closed or not a terminal; "+
					"run ct dev interactively, pipe the answers to stdin, or add %s: \"<answer>\" to the JSON object in %s",
					question, key, cache.path)
			}
			return "", fmt.Errorf("reading answer for prompt %q: %w", question, err)
		}
		answer := strings.TrimSpace(line)
		if err := cache.Set(question, answer); err != nil {
			return "", fmt.Errorf("saving prompt cache: %w", err)
		}
		return answer, nil
	}
}
