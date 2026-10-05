package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEnvFile_BasicKeyValue(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("FOO=bar\nBAZ=qux\n"), 0o644)

	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	assert.Equal(t, "bar", env["FOO"])
	assert.Equal(t, "qux", env["BAZ"])
}

func TestLoadEnvFile_QuotedValues(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("DOUBLE=\"quoted value\"\nSINGLE='single quoted'\n"), 0o644)

	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	assert.Equal(t, "quoted value", env["DOUBLE"])
	assert.Equal(t, "single quoted", env["SINGLE"])
}

func TestLoadEnvFile_CommentsAndBlankLines(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	content := "# this is a comment\n\nFOO=bar\n# another comment\nBAZ=qux\n\n"
	os.WriteFile(f, []byte(content), 0o644)

	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	assert.Len(t, env, 2)
	assert.Equal(t, "bar", env["FOO"])
	assert.Equal(t, "qux", env["BAZ"])
}

func TestLoadEnvFile_ValueWithEquals(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("URL=postgres://user:pass@host:5432/db?sslmode=disable\n"), 0o644)

	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	assert.Equal(t, "postgres://user:pass@host:5432/db?sslmode=disable", env["URL"])
}

func TestLoadEnvFile_EmptyValue(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("EMPTY=\nALSO_EMPTY=\"\"\n"), 0o644)

	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	assert.Equal(t, "", env["EMPTY"])
	assert.Equal(t, "", env["ALSO_EMPTY"])
}

func TestLoadEnvFile_NumericValues(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("PORT=8080\nTIMEOUT=30\n"), 0o644)

	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	assert.Equal(t, "8080", env["PORT"])
	assert.Equal(t, "30", env["TIMEOUT"])
}

func TestLoadEnvFile_NonExistentFile(t *testing.T) {
	_, err := engine.LoadEnvFile("/nonexistent/.env")
	require.Error(t, err)
}

func TestLoadEnvFile_WhitespaceHandling(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("  KEY  =  value  \n"), 0o644)

	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	assert.Equal(t, "value", env["KEY"])
}

func TestLoadEnvFile_NoEqualsLineSkipped(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("INVALID_LINE\nVALID=yes\n"), 0o644)

	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	assert.Len(t, env, 1)
	assert.Equal(t, "yes", env["VALID"])
}

func TestMergeEnvWithSystem_FileOverridesSystem(t *testing.T) {
	fileEnv := map[string]string{
		"CUSTOM_VAR": "from_file",
	}
	result := engine.MergeEnvWithSystem(fileEnv)
	assert.Equal(t, "from_file", result["CUSTOM_VAR"])
}

func TestMergeEnvWithSystem_SystemVarsPresent(t *testing.T) {
	result := engine.MergeEnvWithSystem(nil)
	_, hasPath := result["PATH"]
	assert.True(t, hasPath, "system PATH should be present")
}

func TestMergeEnvWithSystem_FileWinsOverSystem(t *testing.T) {
	t.Setenv("TEST_MERGE_VAR", "from_system")
	fileEnv := map[string]string{"TEST_MERGE_VAR": "from_file"}

	result := engine.MergeEnvWithSystem(fileEnv)
	assert.Equal(t, "from_file", result["TEST_MERGE_VAR"])
}

func loadEnvString(t *testing.T, content string) map[string]string {
	t.Helper()
	f := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))
	env, err := engine.LoadEnvFile(f)
	require.NoError(t, err)
	return env
}

// .env files are often also `source`d by shells and use `export`.
func TestLoadEnvFile_ExportPrefix(t *testing.T) {
	env := loadEnvString(t, "export FOO=bar\nexport\tTAB=1\nexporter=keep\n")
	assert.Equal(t, "bar", env["FOO"])
	assert.Equal(t, "1", env["TAB"])
	assert.Equal(t, "keep", env["exporter"])
	_, bogus := env["export FOO"]
	assert.False(t, bogus)
}

func TestLoadEnvFile_InlineComments(t *testing.T) {
	env := loadEnvString(t, "PORT=3000 # web port\nCOLOR=#fff\nURL=http://x/#anchor\nTABBED=1\t# c\n")
	assert.Equal(t, "3000", env["PORT"])
	assert.Equal(t, "#fff", env["COLOR"], "# without preceding whitespace is part of the value")
	assert.Equal(t, "http://x/#anchor", env["URL"])
	assert.Equal(t, "1", env["TABBED"])
}

func TestLoadEnvFile_QuotedValuesWithTrailingComment(t *testing.T) {
	env := loadEnvString(t, "A=\"a # b\" # comment\nB='x' # c\n")
	assert.Equal(t, "a # b", env["A"])
	assert.Equal(t, "x", env["B"])
}

func TestLoadEnvFile_DoubleQuoteEscapes(t *testing.T) {
	env := loadEnvString(t, `MULTI="line1\nline2"`+"\n"+
		`QUOTED="say \"hi\""`+"\n"+
		`WIN="C:\Users\me"`+"\n"+
		`RAW='a\nb'`+"\n")
	assert.Equal(t, "line1\nline2", env["MULTI"])
	assert.Equal(t, `say "hi"`, env["QUOTED"])
	assert.Equal(t, `C:\Users\me`, env["WIN"], "unknown escapes are kept")
	assert.Equal(t, `a\nb`, env["RAW"], "single quotes are literal")
}

func TestLoadEnvFile_BOMAndCRLF(t *testing.T) {
	env := loadEnvString(t, "\ufeffFOO=bar\r\nBAZ=\"q\"\r\nEMPTY=\r\n")
	assert.Equal(t, "bar", env["FOO"])
	assert.Equal(t, "q", env["BAZ"])
	assert.Equal(t, "", env["EMPTY"])
}

func TestLoadEnvFile_LongLinesAndEmptyKeys(t *testing.T) {
	long := strings.Repeat("a", 200*1024)
	env := loadEnvString(t, "CERT="+long+"\n=orphan\n")
	assert.Equal(t, long, env["CERT"])
	_, empty := env[""]
	assert.False(t, empty)
}
