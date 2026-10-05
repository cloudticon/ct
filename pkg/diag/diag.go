// Package diag defines the structured errors ct reports for bad .ct code and
// bad manifests. Every diagnostic carries what a human or an AI agent needs to
// fix the problem in one step: a stable code, the source position, the object
// and field involved, and a hint.
package diag

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Stable diagnostic codes. Tools may match on them; don't rename.
const (
	CodeSyntax           = "syntax"
	CodeImport           = "import"
	CodeAsync            = "async-not-supported"
	CodeRuntime          = "runtime"
	CodeTimeout          = "timeout"
	CodeDuplicate        = "duplicate-resource"
	CodeInvalidResource  = "invalid-resource"
	CodeUnknownKind      = "unknown-kind"
	CodeUnknownField     = "unknown-field"
	CodeInvalidValue     = "invalid-value"
	CodeMissingField     = "missing-field"
	CodeNamespaceOnScope = "namespace-on-cluster-scoped"
)

// Frame is a position in a .ct/.ts source file.
type Frame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Function string `json:"function,omitempty"`
}

func (f Frame) String() string {
	pos := fmt.Sprintf("%s:%d:%d", f.File, f.Line, f.Column)
	if f.Function != "" {
		return fmt.Sprintf("%s (%s)", f.Function, pos)
	}
	return pos
}

// Diagnostic is one problem found while bundling, running or validating.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// File/Line/Column locate the problem in the user's sources: the bad
	// line for bundle errors, the throwing line for runtime errors, or the
	// call that registered the object for manifest errors.
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	// LineText is the source line, when known.
	LineText string `json:"lineText,omitempty"`
	// Resource names the object involved, e.g. `Deployment "web"`.
	Resource string `json:"resource,omitempty"`
	// Path is the field inside Resource, e.g. `spec.template.spec.containers[0].image`.
	Path  string  `json:"path,omitempty"`
	Hint  string  `json:"hint,omitempty"`
	Stack []Frame `json:"stack,omitempty"`
}

// At sets the source position from a frame.
func (d Diagnostic) At(f *Frame) Diagnostic {
	if f != nil {
		d.File, d.Line, d.Column = f.File, f.Line, f.Column
	}
	return d
}

// Location returns "file:line:col", "file:line", "file" or "".
func (d Diagnostic) Location() string {
	switch {
	case d.File == "":
		return ""
	case d.Line == 0:
		return d.File
	case d.Column == 0:
		return fmt.Sprintf("%s:%d", d.File, d.Line)
	default:
		return fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Column)
	}
}

// String renders the diagnostic for a terminal:
//
//	main.ct:12:3: Deployment "web": spec.image: unknown field [unknown-field]
//	    12 |   image: "nginx",
//	  hint: container fields go under spec.template.spec.containers[]
func (d Diagnostic) String() string {
	var b strings.Builder
	if loc := d.Location(); loc != "" {
		b.WriteString(loc)
		b.WriteString(": ")
	}
	if d.Resource != "" {
		b.WriteString(d.Resource)
		b.WriteString(": ")
	}
	if d.Path != "" {
		b.WriteString(d.Path)
		b.WriteString(": ")
	}
	b.WriteString(d.Message)
	if d.Code != "" {
		fmt.Fprintf(&b, " [%s]", d.Code)
	}
	if d.LineText != "" {
		fmt.Fprintf(&b, "\n    %d | %s", d.Line, d.LineText)
	}
	for _, f := range d.Stack {
		fmt.Fprintf(&b, "\n    at %s", f)
	}
	if d.Hint != "" {
		fmt.Fprintf(&b, "\n  hint: %s", d.Hint)
	}
	return b.String()
}

// List is an error made of one or more diagnostics.
type List []Diagnostic

func (l List) Error() string {
	parts := make([]string, len(l))
	for i, d := range l {
		parts[i] = d.String()
	}
	return strings.Join(parts, "\n")
}

// Errorf wraps a single diagnostic as an error.
func Errorf(code, format string, args ...interface{}) List {
	return List{{Code: code, Message: fmt.Sprintf(format, args...)}}
}

// JSON renders diagnostics as {"errors": [...]} for machine consumers.
func JSON(l List) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		Errors List `json:"errors"`
	}{l})
	return buf.Bytes()
}
