package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/stretchr/testify/assert"
)

func withErrorFormat(t *testing.T, format string) {
	t.Helper()
	old := errorFormat
	errorFormat = format
	t.Cleanup(func() { errorFormat = old })
}

func TestPrintError_TextDiagnostics(t *testing.T) {
	withErrorFormat(t, "text")
	var buf bytes.Buffer

	PrintError(&buf, diag.List{
		{Code: "syntax", Message: "bad", File: "main.ct", Line: 1, Column: 2},
		{Code: "syntax", Message: "worse", File: "main.ct", Line: 3, Column: 4},
	})

	assert.Equal(t, "Error: 2 problems\nmain.ct:1:2: bad [syntax]\nmain.ct:3:4: worse [syntax]\n", buf.String())
}

func TestPrintError_TextPlainError(t *testing.T) {
	withErrorFormat(t, "text")
	var buf bytes.Buffer

	PrintError(&buf, errors.New("boom"))

	assert.Equal(t, "Error: boom\n", buf.String())
}

func TestPrintError_JSONKeepsStructureThroughWrapping(t *testing.T) {
	withErrorFormat(t, "json")
	var buf bytes.Buffer

	PrintError(&buf, fmt.Errorf("render: %w", diag.List{{Code: "runtime", Message: "x", File: "main.ct", Line: 7}}))

	assert.JSONEq(t, `{"errors":[{"code":"runtime","message":"x","file":"main.ct","line":7}]}`, buf.String())
}

func TestPrintError_JSONPlainError(t *testing.T) {
	withErrorFormat(t, "json")
	var buf bytes.Buffer

	PrintError(&buf, errors.New("boom"))

	assert.JSONEq(t, `{"errors":[{"code":"error","message":"boom"}]}`, buf.String())
}
