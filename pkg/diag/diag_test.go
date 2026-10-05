package diag_test

import (
	"encoding/json"
	"testing"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiagnostic_String(t *testing.T) {
	d := diag.Diagnostic{
		Code:     diag.CodeUnknownField,
		Message:  "unknown field",
		File:     "main.ct",
		Line:     12,
		Column:   3,
		LineText: `deployment({ name: "web", image: "nginx" });`,
		Resource: `Deployment "web"`,
		Path:     "spec.image",
		Hint:     "container fields go under spec.template.spec.containers[]",
		Stack:    []diag.Frame{{File: "lib/app.ct", Line: 4, Column: 1, Function: "app"}},
	}

	assert.Equal(t, `main.ct:12:3: Deployment "web": spec.image: unknown field [unknown-field]
    12 | deployment({ name: "web", image: "nginx" });
    at app (lib/app.ct:4:1)
  hint: container fields go under spec.template.spec.containers[]`, d.String())
}

func TestDiagnostic_LocationVariants(t *testing.T) {
	assert.Equal(t, "", diag.Diagnostic{}.Location())
	assert.Equal(t, "a.ct", diag.Diagnostic{File: "a.ct"}.Location())
	assert.Equal(t, "a.ct:3", diag.Diagnostic{File: "a.ct", Line: 3}.Location())
	assert.Equal(t, "a.ct:3:7", diag.Diagnostic{File: "a.ct", Line: 3, Column: 7}.Location())
}

func TestList_ErrorJoinsDiagnostics(t *testing.T) {
	l := diag.List{{Code: "a", Message: "first"}, {Code: "b", Message: "second"}}
	assert.Equal(t, "first [a]\nsecond [b]", l.Error())
}

func TestJSON(t *testing.T) {
	out := diag.JSON(diag.List{{Code: diag.CodeRuntime, Message: "x < y", File: "main.ct", Line: 1}})

	var parsed struct {
		Errors []map[string]interface{} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Len(t, parsed.Errors, 1)
	assert.Equal(t, "runtime", parsed.Errors[0]["code"])
	assert.Equal(t, "main.ct", parsed.Errors[0]["file"])
	assert.NotContains(t, parsed.Errors[0], "path", "empty fields are omitted")
	assert.Contains(t, string(out), "x < y", "no HTML escaping")
}
