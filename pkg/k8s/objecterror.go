package k8s

import "fmt"

// ObjectError is an apply failure caused by one object: the API server
// rejected it, its kind isn't served, or the client may not write it.
// Callers find it with errors.As and use Object to point at the code that
// registered it.
type ObjectError struct {
	// Object is the object as ct sent it.
	Object Resource
	Err    error
}

func (e *ObjectError) Error() string {
	obj := toUnstructured(e.Object)
	return fmt.Sprintf("applying %s %q: %v", obj.GetKind(), obj.GetName(), e.Err)
}

func (e *ObjectError) Unwrap() error { return e.Err }

// UnknownKindError means the cluster doesn't serve a kind: its CRD isn't
// installed, or apiVersion or kind has a typo.
type UnknownKindError struct {
	APIVersion string
	Kind       string
	// GroupVersionMissing is set when the cluster serves nothing at all in
	// APIVersion, as opposed to serving the group without this kind.
	GroupVersionMissing bool
}

func (e *UnknownKindError) Error() string {
	if e.GroupVersionMissing {
		return fmt.Sprintf("the cluster does not serve apiVersion %q (kind %q)", e.APIVersion, e.Kind)
	}
	return fmt.Sprintf("the cluster does not serve kind %q in apiVersion %q", e.Kind, e.APIVersion)
}
