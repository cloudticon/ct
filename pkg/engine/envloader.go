package engine

import (
	"os"
	"strings"
)

// LoadEnvFile parses a .env file into a map of key-value pairs.
// Supports KEY=VALUE, KEY="VALUE", KEY='VALUE' and an optional `export `
// prefix. Lines starting with # and empty lines are skipped; in unquoted
// values " #" starts a comment. Double-quoted values interpret \n, \r, \t,
// \" and \\ (other backslashes are kept); single-quoted values are literal.
func LoadEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	env := make(map[string]string)
	content := strings.TrimPrefix(string(data), "\ufeff") // BOM from Windows editors
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine) // also drops the \r of CRLF files
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		env[key] = parseEnvValue(strings.TrimSpace(value))
	}

	return env, nil
}

func parseEnvValue(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '\'':
		if end := strings.IndexByte(s[1:], '\''); end >= 0 {
			return s[1 : 1+end]
		}
	case '"':
		if v, ok := parseDoubleQuoted(s); ok {
			return v
		}
	}
	// Unquoted (or unterminated quote): whitespace followed by # starts a
	// comment; a # inside the value (COLOR=#fff, URL=...#anchor) does not.
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

// parseDoubleQuoted returns the content of the leading double-quoted string
// of s, ignoring anything after the closing quote (e.g. a comment).
func parseDoubleQuoted(s string) (string, bool) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '"', '\\':
				b.WriteByte(s[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		case c == '"':
			return b.String(), true
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}

// MergeEnvWithSystem merges file env vars on top of system env.
// File env vars take precedence over system env.
func MergeEnvWithSystem(fileEnv map[string]string) map[string]string {
	result := make(map[string]string)
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		result[k] = v
	}
	for k, v := range fileEnv {
		result[k] = v
	}
	return result
}
