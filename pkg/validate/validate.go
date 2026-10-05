// Package validate checks rendered manifests before they reach a cluster.
//
// Every rule mirrors something the Kubernetes API server rejects on admission
// (unknown fields, wrong types, invalid names, a selector that doesn't match
// its template, a Job without restartPolicy, ...), so a clean result means
// `kubectl apply` won't fail on the shape of the objects. Built-in kinds are
// decoded strictly into the client-go types; custom resources, whose schemas
// live in their CRDs, only get metadata checks.
package validate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/cloudticon/ct/pkg/diag"
	"github.com/cloudticon/ct/pkg/manifest"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	pathvalidation "k8s.io/apimachinery/pkg/api/validation/path"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/scheme"
)

// Resources validates rendered objects. origin(i) returns the call chain in
// the user's code that registered object i, so each diagnostic points there.
func Resources(resources []manifest.Resource, origin func(i int) []diag.Frame) diag.List {
	var list diag.List
	for i, res := range resources {
		for _, fe := range checkResource(res) {
			d := diag.Diagnostic{
				Code:     codeFor(fe),
				Message:  messageFor(fe),
				Resource: manifest.Ref(res),
				Path:     fe.Field,
				Hint:     hintFor(fe),
			}
			if origin != nil {
				d = d.AtChain(origin(i))
			}
			list = append(list, d)
		}
	}
	return list
}

func messageFor(fe *field.Error) string {
	switch fe.Type {
	case unknownField:
		return fe.Detail
	case unknownKind:
		if fe.Field == "kind" {
			return fe.Detail
		}
		return fmt.Sprintf("unknown apiVersion %q: %s", fe.BadValue, fe.Detail)
	}
	return fe.ErrorBody()
}

// Error types field.Error doesn't have.
const (
	unknownField field.ErrorType = "FieldValueUnknown"
	unknownKind  field.ErrorType = "KindUnknown"
)

func codeFor(fe *field.Error) string {
	switch fe.Type {
	case unknownField:
		return diag.CodeUnknownField
	case unknownKind:
		return diag.CodeUnknownKind
	case field.ErrorTypeRequired:
		return diag.CodeMissingField
	default:
		return diag.CodeInvalidValue
	}
}

func hintFor(fe *field.Error) string {
	switch {
	case fe.Type == unknownField && strings.HasPrefix(fe.Field, "spec.") && strings.Count(fe.Field, ".") == 1:
		return "check the field name and nesting; for workloads, container fields (image, ports, env) go under spec.template.spec.containers[]"
	case fe.Type == unknownField:
		return "check the field name and nesting against the Kubernetes API reference (`kubectl explain`); if your cluster is newer than this ct build, pass --validate=false"
	case fe.Type == unknownKind && fe.Field == "kind":
		return "if the kind comes from a Kubernetes release newer than this ct build, pass --validate=false"
	case strings.HasPrefix(fe.Detail, "expected a string, got"):
		return "convert it in code with String(...), quote it in the values file, or pass it with --set-string"
	case fe.Field == "metadata.name" && fe.Type == field.ErrorTypeRequired:
		return "every object needs a name; factories usually take it as `name`"
	case (fe.Field == "metadata.name" || fe.Field == "metadata.namespace") && fe.Type == field.ErrorTypeInvalid:
		return "names are lowercase letters, digits and '-' (Services and Namespaces also no '.'), starting and ending with a letter or digit"
	}
	return ""
}

var strictDecoder = kjson.NewSerializerWithOptions(kjson.DefaultMetaFactory, scheme.Scheme, scheme.Scheme,
	kjson.SerializerOptions{Strict: true})

func checkResource(res manifest.Resource) field.ErrorList {
	var errs field.ErrorList
	apiVersion, _ := res["apiVersion"].(string)
	kind, _ := res["kind"].(string)
	if apiVersion == "" {
		errs = append(errs, field.Required(field.NewPath("apiVersion"), ""))
	}
	if kind == "" {
		errs = append(errs, field.Required(field.NewPath("kind"), ""))
	}
	meta, ok := res["metadata"].(map[string]interface{})
	if !ok {
		return append(errs, field.Required(field.NewPath("metadata"), "metadata.name is needed"))
	}
	if apiVersion == "" || kind == "" {
		return append(errs, checkMetadata(meta, kind, false)...)
	}

	gvk := schema.FromAPIVersionAndKind(apiVersion, kind)
	if r, ok := removedAPIs[apiVersion+"/"+kind]; ok {
		return append(errs, field.Invalid(field.NewPath("apiVersion"), apiVersion,
			fmt.Sprintf("%s %s was removed in Kubernetes %s; use %s", apiVersion, kind, r.removedIn, r.replacement)))
	}

	builtin := scheme.Scheme.Recognizes(gvk)
	if !builtin {
		if alternatives := servedAs(gvk); alternatives != nil {
			return append(errs, &field.Error{
				Type:     unknownKind,
				Field:    "apiVersion",
				BadValue: apiVersion,
				Detail:   fmt.Sprintf("%s is served as %s", kind, strings.Join(alternatives, " or ")),
			})
		}
		if gvk.Group == "core" || builtinGroups[gvk.Group] {
			detail := fmt.Sprintf("Kubernetes has no kind %q in %s", kind, apiVersion)
			if similar := similarKinds(kind); len(similar) > 0 {
				detail += fmt.Sprintf("; did you mean %s?", strings.Join(similar, " or "))
			}
			return append(errs, &field.Error{Type: unknownKind, Field: "kind", BadValue: kind, Detail: detail})
		}
	}
	errs = append(errs, checkMetadata(meta, kind, builtin)...)
	if !builtin {
		return append(errs, checkCustom(res, gvk)...)
	}

	obj, decodeErrs := decodeStrict(res)
	errs = append(errs, decodeErrs...)
	if obj != nil {
		errs = append(errs, checkTyped(obj)...)
	}
	return errs
}

func checkMetadata(meta map[string]interface{}, kind string, builtin bool) field.ErrorList {
	var errs field.ErrorList
	metaPath := field.NewPath("metadata")

	name, _ := meta["name"].(string)
	if name == "" {
		detail := ""
		if _, ok := meta["generateName"]; ok {
			detail = "ct tracks objects by name; generateName is not supported"
		}
		errs = append(errs, field.Required(metaPath.Child("name"), detail))
	} else {
		for _, msg := range nameValidator(kind, builtin)(name, false) {
			errs = append(errs, field.Invalid(metaPath.Child("name"), name, msg))
		}
		if kind == "CronJob" && builtin && len(name) > 52 {
			errs = append(errs, field.TooLong(metaPath.Child("name"), name, 52))
		}
	}
	if ns, ok := meta["namespace"].(string); ok && ns != "" {
		for _, msg := range apivalidation.ValidateNamespaceName(ns, false) {
			errs = append(errs, field.Invalid(metaPath.Child("namespace"), ns, msg))
		}
	}

	// Built-in kinds get type errors from strict decoding; report them here
	// only for custom resources.
	labels, labelErrs := stringMap(meta["labels"], metaPath.Child("labels"))
	annotations, annotationErrs := stringMap(meta["annotations"], metaPath.Child("annotations"))
	if !builtin {
		errs = append(errs, labelErrs...)
		errs = append(errs, annotationErrs...)
	}
	errs = append(errs, metav1validation.ValidateLabels(labels, metaPath.Child("labels"))...)
	errs = append(errs, apivalidation.ValidateAnnotations(annotations, metaPath.Child("annotations"))...)
	return append(errs, checkFinalizers(meta["finalizers"], metaPath.Child("finalizers"))...)
}

// standardFinalizers may be used without a domain prefix.
var standardFinalizers = map[string]bool{
	"kubernetes":                     true,
	metav1.FinalizerOrphanDependents: true,
	metav1.FinalizerDeleteDependents: true,
}

func checkFinalizers(v interface{}, path *field.Path) field.ErrorList {
	list, ok := v.([]interface{})
	if !ok {
		return nil // absent, or a type error strict decoding reports
	}
	var names []string
	var errs field.ErrorList
	for i, item := range list {
		name, ok := item.(string)
		if !ok {
			continue
		}
		names = append(names, name)
		if !strings.Contains(name, "/") && !standardFinalizers[name] {
			errs = append(errs, field.Invalid(path.Index(i), name,
				"name is neither a standard finalizer name nor is it fully qualified (use <domain>/<name>, e.g. example.com/cleanup)"))
		}
	}
	return append(apivalidation.ValidateFinalizers(names, path), errs...)
}

// nameValidator returns the API server's name rule for a kind.
func nameValidator(kind string, builtin bool) apivalidation.ValidateNameFunc {
	switch {
	case !builtin:
		return apivalidation.NameIsDNSSubdomain
	case kind == "CertificateSigningRequest":
		return func(string, bool) []string { return nil } // any name is accepted
	case kind == "Namespace":
		return apivalidation.ValidateNamespaceName
	case kind == "Service":
		return apivalidation.NameIsDNS1035Label
	case kind == "Role" || kind == "ClusterRole" || kind == "RoleBinding" || kind == "ClusterRoleBinding":
		return pathvalidation.ValidatePathSegmentName
	default:
		return apivalidation.NameIsDNSSubdomain
	}
}

func stringMap(v interface{}, path *field.Path) (map[string]string, field.ErrorList) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, field.ErrorList{field.Invalid(path, fmt.Sprintf("%T", v), "must be an object of strings")}
	}
	out := make(map[string]string, len(m))
	var errs field.ErrorList
	for _, k := range sortedKeys(m) {
		s, ok := m[k].(string)
		if !ok {
			errs = append(errs, field.Invalid(path.Key(k), m[k], "must be a string; quote numbers and booleans"))
			continue
		}
		out[k] = s
	}
	return out, errs
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// servedAs lists the apiVersions a built-in kind is served under when the
// requested apiVersion is wrong (e.g. ConfigMap as "core/v1"). It returns nil
// for groups Kubernetes doesn't define, which belong to custom resources:
// serving.knative.dev/v1 Service is not a mistake for v1 Service.
func servedAs(gvk schema.GroupVersionKind) []string {
	if gvk.Group != "core" && !builtinGroups[gvk.Group] {
		return nil
	}
	var versions []string
	for known := range scheme.Scheme.AllKnownTypes() {
		if known.Kind != gvk.Kind || known.Version == runtime.APIVersionInternal {
			continue
		}
		apiVersion := known.GroupVersion().String()
		if _, removed := removedAPIs[apiVersion+"/"+known.Kind]; removed || !isStable(known.Version) {
			continue
		}
		versions = append(versions, apiVersion)
	}
	sort.Strings(versions)
	return versions
}

var builtinGroups = func() map[string]bool {
	groups := map[string]bool{}
	for gvk := range scheme.Scheme.AllKnownTypes() {
		groups[gvk.Group] = true
	}
	return groups
}()

// similarKinds suggests built-in kinds for a misspelled one: same name in
// another case, or within two edits.
func similarKinds(kind string) []string {
	seen := map[string]bool{}
	var out []string
	for known := range scheme.Scheme.AllKnownTypes() {
		k := known.Kind
		if seen[k] || known.Version == runtime.APIVersionInternal || strings.HasSuffix(k, "List") || strings.HasSuffix(k, "Options") {
			continue
		}
		seen[k] = true
		if strings.EqualFold(k, kind) || editDistance(strings.ToLower(k), strings.ToLower(kind)) <= 2 {
			out = append(out, fmt.Sprintf("%q", k))
		}
	}
	sort.Strings(out)
	return out
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

func isStable(version string) bool {
	return !strings.Contains(version, "alpha") && !strings.Contains(version, "beta")
}

type removal struct{ removedIn, replacement string }

// removedAPIs are apiVersion/kind pairs no supported Kubernetes release serves
// any more, even though client-go still knows their Go types.
var removedAPIs = map[string]removal{}

func init() {
	add := func(removedIn, replacement string, apiVersion string, kinds ...string) {
		for _, k := range kinds {
			removedAPIs[apiVersion+"/"+k] = removal{removedIn, replacement}
		}
	}
	add("1.16", "apps/v1", "extensions/v1beta1", "Deployment", "DaemonSet", "ReplicaSet")
	add("1.16", "networking.k8s.io/v1", "extensions/v1beta1", "NetworkPolicy")
	add("1.16", "apps/v1", "apps/v1beta1", "Deployment", "StatefulSet", "ReplicaSet", "ControllerRevision")
	add("1.16", "apps/v1", "apps/v1beta2", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "ControllerRevision")
	add("1.22", "networking.k8s.io/v1", "extensions/v1beta1", "Ingress")
	add("1.22", "networking.k8s.io/v1", "networking.k8s.io/v1beta1", "Ingress", "IngressClass")
	add("1.22", "rbac.authorization.k8s.io/v1", "rbac.authorization.k8s.io/v1beta1", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding")
	add("1.22", "apiextensions.k8s.io/v1", "apiextensions.k8s.io/v1beta1", "CustomResourceDefinition")
	add("1.22", "admissionregistration.k8s.io/v1", "admissionregistration.k8s.io/v1beta1", "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration")
	add("1.22", "apiregistration.k8s.io/v1", "apiregistration.k8s.io/v1beta1", "APIService")
	add("1.22", "certificates.k8s.io/v1", "certificates.k8s.io/v1beta1", "CertificateSigningRequest")
	add("1.22", "coordination.k8s.io/v1", "coordination.k8s.io/v1beta1", "Lease")
	add("1.22", "scheduling.k8s.io/v1", "scheduling.k8s.io/v1beta1", "PriorityClass")
	add("1.22", "storage.k8s.io/v1", "storage.k8s.io/v1beta1", "CSIDriver", "CSINode", "StorageClass", "VolumeAttachment")
	add("1.25", "batch/v1", "batch/v1beta1", "CronJob")
	add("1.25", "discovery.k8s.io/v1", "discovery.k8s.io/v1beta1", "EndpointSlice")
	add("1.25", "events.k8s.io/v1", "events.k8s.io/v1beta1", "Event")
	add("1.25", "autoscaling/v2", "autoscaling/v2beta1", "HorizontalPodAutoscaler")
	add("1.25", "policy/v1", "policy/v1beta1", "PodDisruptionBudget")
	add("1.25", "Pod Security Admission", "policy/v1beta1", "PodSecurityPolicy")
	add("1.25", "Pod Security Admission", "extensions/v1beta1", "PodSecurityPolicy")
	add("1.25", "node.k8s.io/v1", "node.k8s.io/v1beta1", "RuntimeClass")
	add("1.26", "autoscaling/v2", "autoscaling/v2beta2", "HorizontalPodAutoscaler")
	add("1.26", "flowcontrol.apiserver.k8s.io/v1", "flowcontrol.apiserver.k8s.io/v1beta1", "FlowSchema", "PriorityLevelConfiguration")
	add("1.27", "storage.k8s.io/v1", "storage.k8s.io/v1beta1", "CSIStorageCapacity")
	add("1.29", "flowcontrol.apiserver.k8s.io/v1", "flowcontrol.apiserver.k8s.io/v1beta2", "FlowSchema", "PriorityLevelConfiguration")
	add("1.32", "flowcontrol.apiserver.k8s.io/v1", "flowcontrol.apiserver.k8s.io/v1beta3", "FlowSchema", "PriorityLevelConfiguration")
}

// decodeStrict decodes a built-in object the way the API server does. Type
// errors stop a decode, so each offending field is reported and removed, and
// decoding is retried to surface every problem in one run.
func decodeStrict(res manifest.Resource) (runtime.Object, field.ErrorList) {
	var errs field.ErrorList
	work := deepCopy(res).(map[string]interface{})
	for attempt := 0; attempt < 50; attempt++ {
		data, err := json.Marshal(work)
		if err != nil {
			return nil, append(errs, field.InternalError(nil, err))
		}
		obj, _, err := strictDecoder.Decode(data, nil, nil)
		if err == nil {
			return obj, errs
		}
		if strictErr, ok := runtime.AsStrictDecodingError(err); ok {
			for _, e := range strictErr.Errors() {
				errs = append(errs, strictFieldError(e.Error()))
			}
			return obj, errs
		}
		if m := typeErrorRe.FindStringSubmatch(err.Error()); m != nil {
			path := m[2]
			errs = append(errs, &field.Error{
				Type:     field.ErrorTypeInvalid,
				Field:    path,
				BadValue: field.OmitValueType{},
				Detail:   fmt.Sprintf("expected %s, got %s", jsonTypeName(m[3]), m[1]),
			})
			if !deletePath(work, strings.Split(path, ".")) {
				return nil, errs
			}
			continue
		}
		detail := err.Error()
		if strings.Contains(detail, "illegal base64") {
			detail = "Secret data values must be base64-encoded; put plain text in stringData instead"
		}
		return nil, append(errs, &field.Error{Type: field.ErrorTypeInvalid, BadValue: field.OmitValueType{}, Detail: detail})
	}
	return nil, errs
}

// typeErrorRe matches sigs.k8s.io/json type errors, e.g. `json: cannot
// unmarshal string into Go struct field DeploymentSpec.spec.replicas of type int32`.
var typeErrorRe = regexp.MustCompile(`cannot unmarshal (.+?) into Go struct field \w+\.(\S+) of type (\S+)`)

var unknownFieldRe = regexp.MustCompile(`^unknown field "(.+)"$`)

func strictFieldError(msg string) *field.Error {
	if m := unknownFieldRe.FindStringSubmatch(msg); m != nil {
		return &field.Error{Type: unknownField, Field: m[1], BadValue: field.OmitValueType{}, Detail: "unknown field"}
	}
	return &field.Error{Type: field.ErrorTypeInvalid, BadValue: field.OmitValueType{}, Detail: msg}
}

func jsonTypeName(goType string) string {
	goType = strings.TrimLeft(goType, "*")
	switch {
	case strings.HasPrefix(goType, "int"), strings.HasPrefix(goType, "uint"):
		return "an integer"
	case strings.HasPrefix(goType, "float"):
		return "a number"
	case goType == "string":
		return "a string"
	case goType == "bool":
		return "a boolean"
	case strings.HasPrefix(goType, "[]"):
		return "an array"
	case strings.HasPrefix(goType, "map["):
		return "an object"
	default:
		return goType
	}
}

// deletePath removes a dotted path, fanning out over arrays (type error paths
// carry no indexes). It reports whether anything was removed.
func deletePath(v interface{}, path []string) bool {
	switch val := v.(type) {
	case []interface{}:
		removed := false
		for _, item := range val {
			removed = deletePath(item, path) || removed
		}
		return removed
	case map[string]interface{}:
		if len(path) == 1 {
			_, ok := val[path[0]]
			delete(val, path[0])
			return ok
		}
		next, ok := val[path[0]]
		return ok && deletePath(next, path[1:])
	}
	return false
}

func deepCopy(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			out[k] = deepCopy(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = deepCopy(item)
		}
		return out
	default:
		return v
	}
}

// checkCustom covers custom resources and kinds outside client-go's scheme.
func checkCustom(res manifest.Resource, gvk schema.GroupVersionKind) field.ErrorList {
	if gvk.Group != "apiextensions.k8s.io" || gvk.Kind != "CustomResourceDefinition" {
		return nil
	}
	spec, _ := res["spec"].(map[string]interface{})
	group, _ := spec["group"].(string)
	names, _ := spec["names"].(map[string]interface{})
	plural, _ := names["plural"].(string)
	meta, _ := res["metadata"].(map[string]interface{})
	name, _ := meta["name"].(string)
	if group == "" || plural == "" || name == "" {
		return nil
	}
	if want := plural + "." + group; name != want {
		return field.ErrorList{field.Invalid(field.NewPath("metadata", "name"), name,
			fmt.Sprintf("must be spec.names.plural + \".\" + spec.group (%q)", want))}
	}
	return nil
}

// labelSelectorMatches reports whether sel selects objects carrying labels.
func labelSelectorMatches(sel *metav1.LabelSelector, labels map[string]string) (bool, error) {
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false, err
	}
	return s.Matches(k8slabels.Set(labels)), nil
}

func sortedStrings(s []string) []string {
	sort.Strings(s)
	return s
}

// countSet counts non-nil pointer fields of a struct, e.g. how many sources a
// corev1.VolumeSource names.
func countSet(v interface{}) int {
	rv := reflect.ValueOf(v)
	n := 0
	for i := 0; i < rv.NumField(); i++ {
		if f := rv.Field(i); f.Kind() == reflect.Pointer && !f.IsNil() {
			n++
		}
	}
	return n
}
