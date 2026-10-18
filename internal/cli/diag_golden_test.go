package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Golden tests for the diagnostics ct prints with `--error-format json`.
// Codes and messages are a contract for AI agents, so every change to them has
// to show up in a diff.
//
// Each directory under testdata/diag is one case:
//
//	main.ct, values.yaml, lib/...  the project to render
//	release                        optional release name (default "demo")
//	expected.json                  the JSON ct prints for it
//
// Refresh the expected files after an intended change with:
//
//	go test ./internal/cli -run TestDiagnosticsGolden -update
var updateGolden = flag.Bool("update", false, "rewrite the expected.json files in testdata/diag")

func TestDiagnosticsGolden(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("testdata", "diag", "*"))
	require.NoError(t, err)
	require.NotEmpty(t, cases, "no cases in testdata/diag")

	for _, dir := range cases {
		dir := dir
		t.Run(filepath.Base(dir), func(t *testing.T) {
			release := "demo"
			if b, err := os.ReadFile(filepath.Join(dir, "release")); err == nil {
				release = strings.TrimSpace(string(b))
			}

			withErrorFormat(t, "json")
			cmd := newTemplateCmd()
			var out, errBuf bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errBuf)
			cmd.SetArgs([]string{release, dir})
			runErr := cmd.Execute()
			require.Error(t, runErr, "the case must fail to render")

			var got bytes.Buffer
			PrintError(&got, runErr)

			// Paths in the expected files are relative to the project (or a
			// placeholder), never to this machine.
			abs, err := filepath.Abs(dir)
			require.NoError(t, err)
			actual := strings.ReplaceAll(got.String(), abs, "<project>")

			expectedPath := filepath.Join(dir, "expected.json")
			if *updateGolden {
				require.NoError(t, os.WriteFile(expectedPath, []byte(actual), 0o644))
				return
			}
			want, err := os.ReadFile(expectedPath)
			require.NoError(t, err, "missing %s; create it with: go test ./internal/cli -run TestDiagnosticsGolden -update", expectedPath)
			assert.Equal(t, string(want), actual)
		})
	}
}
