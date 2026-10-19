// Package explain describes the fields of built-in Kubernetes kinds without a
// cluster. The schema comes from the Go types ct is built with (client-go's
// scheme) and the field docs from their generated SwaggerDoc methods, so the
// answer matches what ct's validation accepts, for the Kubernetes version ct
// was built with.
package explain

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes/scheme"
)

// Field is one field of an object.
type Field struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

// Result describes a kind or one of its fields.
type Result struct {
	Kind        string  `json:"kind"`
	APIVersion  string  `json:"apiVersion"`
	Path        string  `json:"path,omitempty"`
	Type        string  `json:"type"`
	Required    bool    `json:"required,omitempty"`
	Description string  `json:"description,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
}

// NotFoundError says which part of the query is unknown and the closest
// known name.
type NotFoundError struct {
	// What is "kind" or "field".
	What string
	// Name is the unknown kind or field.
	Name string
	// Parent is where the field was looked up, e.g. "Deployment.spec".
	Parent string
	// Path is the unknown field's path inside the kind, e.g. "spec.replcas".
	Path string
	// Closest is the nearest known name, "" when there are none.
	Closest string
	// Known lists the names that would have matched (fields of Parent).
	Known []string
}

func (e *NotFoundError) Error() string {
	if e.What == "kind" {
		return fmt.Sprintf("Kubernetes has no built-in kind %q", e.Name)
	}
	return fmt.Sprintf("%s has no field %q", e.Parent, e.Name)
}

// Explain resolves a query "<Kind>[.<field>[.<field>...]]". apiVersion picks
// the version when a kind is served under several; "" chooses the stable,
// newest one (core group first).
func Explain(query, apiVersion string) (*Result, error) {
	kindName, path, _ := strings.Cut(query, ".")
	gvk, err := findKind(kindName, apiVersion)
	if err != nil {
		return nil, err
	}
	t, ok := scheme.Scheme.AllKnownTypes()[gvk]
	if !ok {
		return nil, &NotFoundError{What: "kind", Name: kindName}
	}
	res := &Result{
		Kind:        gvk.Kind,
		APIVersion:  gvk.GroupVersion().String(),
		Type:        typeName(t),
		Description: docs(t)[""],
	}

	parent := gvk.Kind
	var segments []string
	if path != "" {
		segments = strings.Split(path, ".")
	}
	for _, seg := range segments {
		seg = indexRe.ReplaceAllString(seg, "")
		fields := structFields(elem(t))
		f, ok := fields[seg]
		if !ok {
			names := sortedNames(fields)
			return nil, &NotFoundError{What: "field", Name: seg, Parent: parent, Path: strings.TrimPrefix(res.Path+"."+seg, "."),
				Closest: closest(seg, names), Known: names}
		}
		t = f.typ
		res.Required = f.required
		res.Description = f.doc
		res.Type = typeName(t)
		parent += "." + seg
		res.Path = strings.TrimPrefix(res.Path+"."+seg, ".")
	}

	for name, f := range structFields(elem(t)) {
		res.Fields = append(res.Fields, Field{Name: name, Type: typeName(f.typ), Required: f.required, Description: f.doc})
	}
	sort.Slice(res.Fields, func(i, j int) bool { return res.Fields[i].Name < res.Fields[j].Name })
	return res, nil
}

var indexRe = regexp.MustCompile(`\[[^\]]*\]$`)

// findKind picks the group/version for a kind name (case-insensitive).
func findKind(name, apiVersion string) (schema.GroupVersionKind, error) {
	var candidates []schema.GroupVersionKind
	kinds := map[string]bool{}
	for gvk := range scheme.Scheme.AllKnownTypes() {
		if gvk.Version == runtime.APIVersionInternal || isHelperKind(gvk.Kind) {
			continue
		}
		kinds[gvk.Kind] = true
		if strings.EqualFold(gvk.Kind, name) {
			candidates = append(candidates, gvk)
		}
	}
	if len(candidates) == 0 {
		names := make([]string, 0, len(kinds))
		for k := range kinds {
			names = append(names, k)
		}
		sort.Strings(names)
		return schema.GroupVersionKind{}, &NotFoundError{What: "kind", Name: name, Closest: closest(name, names)}
	}
	if apiVersion != "" {
		for _, c := range candidates {
			if c.GroupVersion().String() == apiVersion {
				return c, nil
			}
		}
		var served []string
		for _, c := range candidates {
			served = append(served, c.GroupVersion().String())
		}
		sort.Strings(served)
		return schema.GroupVersionKind{}, fmt.Errorf("%s is not served as %s; known apiVersions: %s",
			candidates[0].Kind, apiVersion, strings.Join(served, ", "))
	}
	sort.Slice(candidates, func(i, j int) bool { return preferred(candidates[i], candidates[j]) })
	return candidates[0], nil
}

// preferred orders stable versions first, then the core group, then groups
// alphabetically, then the newest version.
func preferred(a, b schema.GroupVersionKind) bool {
	if sa, sb := isStable(a.Version), isStable(b.Version); sa != sb {
		return sa
	}
	if (a.Group == "") != (b.Group == "") {
		return a.Group == ""
	}
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	return version.CompareKubeAwareVersionStrings(a.Version, b.Version) > 0
}

func isStable(v string) bool {
	return !strings.Contains(v, "alpha") && !strings.Contains(v, "beta")
}

func isHelperKind(kind string) bool {
	return strings.HasSuffix(kind, "List") || strings.HasSuffix(kind, "Options") ||
		kind == "WatchEvent" || kind == "Status" || kind == "APIGroup" || kind == "APIGroupList" ||
		kind == "APIResourceList" || kind == "APIVersions"
}

type fieldInfo struct {
	typ      reflect.Type
	required bool
	doc      string
}

var (
	quantityType    = reflect.TypeOf(resource.Quantity{})
	intOrStringType = reflect.TypeOf(intstr.IntOrString{})
	timeType        = reflect.TypeOf(metav1.Time{})
	microTimeType   = reflect.TypeOf(metav1.MicroTime{})
	durationType    = reflect.TypeOf(metav1.Duration{})
	rawType         = reflect.TypeOf(runtime.RawExtension{})
)

// scalar reports types that serialize as one JSON value although they are
// structs in Go.
func scalar(t reflect.Type) (string, bool) {
	switch t {
	case quantityType:
		return "Quantity", true
	case intOrStringType:
		return "IntOrString", true
	case timeType, microTimeType:
		return "string (date-time)", true
	case durationType:
		return "string (duration)", true
	case rawType:
		return "Object", true
	}
	return "", false
}

// elem strips pointers, slices and maps down to the type whose fields a
// path continues into.
func elem(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
				return t
			}
			t = t.Elem()
		case reflect.Map:
			t = t.Elem()
		default:
			return t
		}
	}
}

// structFields lists the JSON fields of t, with inline (embedded) structs
// flattened. Non-struct and scalar-like types have none.
func structFields(t reflect.Type) map[string]fieldInfo {
	out := map[string]fieldInfo{}
	if t.Kind() != reflect.Struct {
		return out
	}
	if _, ok := scalar(t); ok {
		return out
	}
	d := docs(t)
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if sf.Anonymous && (name == "" || strings.Contains(opts, "inline")) {
			for n, f := range structFields(elem(sf.Type)) {
				out[n] = f
			}
			continue
		}
		if name == "" {
			name = sf.Name
		}
		out[name] = fieldInfo{
			typ:      sf.Type,
			required: !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero"),
			doc:      d[name],
		}
	}
	return out
}

type swaggerDoc interface{ SwaggerDoc() map[string]string }

// docs returns the generated field documentation of a struct type; "" holds
// the type's own description.
func docs(t reflect.Type) map[string]string {
	t = elem(t)
	if t.Kind() != reflect.Struct {
		return nil
	}
	if s, ok := reflect.New(t).Elem().Interface().(swaggerDoc); ok {
		return s.SwaggerDoc()
	}
	if s, ok := reflect.New(t).Interface().(swaggerDoc); ok {
		return s.SwaggerDoc()
	}
	return nil
}

// typeName renders a Go type the way the OpenAPI schema names it.
func typeName(t reflect.Type) string {
	if name, ok := scalar(t); ok {
		return name
	}
	switch t.Kind() {
	case reflect.Pointer:
		return typeName(t.Elem())
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "string (base64)"
		}
		return "[]" + typeName(t.Elem())
	case reflect.Map:
		return "map[string]" + typeName(t.Elem())
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Struct:
		return t.Name()
	case reflect.Interface:
		return "Object"
	}
	return t.String()
}

func sortedNames(fields map[string]fieldInfo) []string {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// closest returns the name nearest to want (case-insensitive edit distance),
// ties broken alphabetically because names arrive sorted.
func closest(want string, names []string) string {
	best, bestDist := "", -1
	for _, n := range names {
		d := editDistance(strings.ToLower(want), strings.ToLower(n))
		if bestDist < 0 || d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// FieldPath turns a diagnostic path such as "spec.template.spec.containers[0].imag"
// into the explain query for its parent object: "spec.template.spec.containers".
func FieldPath(path string) string {
	parts := strings.Split(path, ".")
	if len(parts) <= 1 {
		return ""
	}
	parts = parts[:len(parts)-1]
	for i, p := range parts {
		parts[i] = indexRe.ReplaceAllString(p, "")
	}
	return strings.Join(parts, ".")
}
