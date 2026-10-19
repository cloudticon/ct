package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Action is what `ct apply` would do with one object.
type Action string

const (
	ActionCreate Action = "create"
	ActionChange Action = "change"
	ActionNone   Action = "none"
	ActionPrune  Action = "prune"
)

// PlanOpts configures PlanRelease.
type PlanOpts struct {
	// CreateNamespace says apply would create the release namespace first
	// (--create-namespace), so objects in it are planned even when it doesn't
	// exist yet.
	CreateNamespace bool
}

// PlannedObject is one object of a plan. Namespace is resolved: empty only
// for cluster-scoped kinds.
type PlannedObject struct {
	Action     Action `json:"action"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	// Diff is a unified diff of the object as YAML, live ("---") against what
	// apply would leave ("+++"). Empty for "none" and "prune". Secret values
	// are masked.
	Diff string `json:"diff,omitempty"`
}

// Plan lists what applying a release would do, in apply order, followed by
// the objects prune would delete in delete order.
type Plan struct {
	Objects []PlannedObject
}

// Count returns how many objects get action a.
func (p *Plan) Count(a Action) int {
	n := 0
	for _, o := range p.Objects {
		if o.Action == a {
			n++
		}
	}
	return n
}

// planRelease computes what applyRelease would do without writing anything:
// each object goes through server-side apply with dryRun=All and is compared
// with its live state, and the inventory is only read. Rejections of all
// objects are collected (errors.Join of *ObjectError) so one run reports them
// all.
func (c *client) planRelease(ctx context.Context, namespace, releaseName string, resources []Resource, opts PlanOpts) (*Plan, error) {
	oldRefs, err := loadInventory(ctx, c, namespace, releaseName)
	if err != nil {
		return nil, fmt.Errorf("loading inventory: %w", err)
	}
	newRefs, err := resourcesToRefs(resources)
	if err != nil {
		return nil, fmt.Errorf("building resource refs: %w", err)
	}
	defaultNs := namespace
	if defaultNs == "" {
		defaultNs = c.Namespace
	}

	ordered := slices.Clone(resources)
	manifest.SortForApply(ordered)

	// Namespaces and kinds this run would create: a dry run can't place an
	// object in a namespace, or of a kind, that doesn't exist yet.
	newNamespaces := map[string]bool{}
	if opts.CreateNamespace && namespace != "" {
		newNamespaces[namespace] = true
	}
	newKinds := map[string]bool{}

	plan := &Plan{}
	var errs []error
	for _, res := range ordered {
		obj, err := c.planOne(ctx, res, newKinds, newNamespaces)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		plan.Objects = append(plan.Objects, obj)
		if groupKind, ok := crdGroupKind(res); ok {
			newKinds[groupKind] = true
		}
		if obj.Action == ActionCreate && obj.APIVersion == "v1" && obj.Kind == "Namespace" {
			newNamespaces[obj.Name] = true
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	for _, ref := range orderForDelete(computeOrphaned(oldRefs, newRefs, defaultNs)) {
		ns := ref.Namespace
		switch {
		case manifest.IsClusterScoped(ref.APIVersion, ref.Kind):
			ns = ""
		case ns == "":
			ns = defaultNs
		}
		plan.Objects = append(plan.Objects, PlannedObject{
			Action:     ActionPrune,
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Namespace:  ns,
			Name:       ref.Name,
		})
	}
	return plan, nil
}

func (c *client) planOne(ctx context.Context, res Resource, newKinds, newNamespaces map[string]bool) (PlannedObject, error) {
	obj := toUnstructured(res)
	planned := PlannedObject{APIVersion: obj.GetAPIVersion(), Kind: obj.GetKind(), Name: obj.GetName()}

	info, err := c.resolveResourceInfo(obj.GetAPIVersion(), obj.GetKind())
	if err != nil {
		var unknown *UnknownKindError
		if errors.As(err, &unknown) && newKinds[manifest.Group(obj.GetAPIVersion())+"/"+obj.GetKind()] {
			// The CRD is created by this release; the kind isn't served yet.
			planned.Namespace = obj.GetNamespace()
			if planned.Namespace == "" && !manifest.IsClusterScoped(planned.APIVersion, planned.Kind) {
				planned.Namespace = c.Namespace
			}
			return plannedCreate(planned, res)
		}
		if errors.As(err, &unknown) {
			return planned, &ObjectError{Object: res, Err: err}
		}
		return planned, fmt.Errorf("resolving resource info for %s %s: %w", obj.GetAPIVersion(), obj.GetKind(), err)
	}

	var dynClient dynamic.ResourceInterface
	if info.Namespaced {
		planned.Namespace = obj.GetNamespace()
		if planned.Namespace == "" {
			planned.Namespace = c.Namespace
		}
		dynClient = c.Dynamic.Resource(info.GVR).Namespace(planned.Namespace)
	} else {
		dynClient = c.Dynamic.Resource(info.GVR)
	}

	live, err := dynClient.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		live, err = nil, nil
	}
	if err != nil {
		return planned, &ObjectError{Object: res, Err: err}
	}

	data, err := json.Marshal(obj.Object)
	if err != nil {
		return planned, fmt.Errorf("marshaling %s %q: %w", obj.GetKind(), obj.GetName(), err)
	}
	force := true
	after, err := dynClient.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: "ct",
		Force:        &force,
		DryRun:       []string{metav1.DryRunAll},
	})
	if err != nil {
		if live == nil && info.Namespaced && newNamespaces[planned.Namespace] && isNamespaceNotFound(err) {
			// The namespace comes with this release or --create-namespace.
			return plannedCreate(planned, res)
		}
		return planned, &ObjectError{Object: res, Err: err}
	}

	if live == nil {
		return plannedCreate(planned, after.Object)
	}
	before, afterObj := forDiff(live.Object), forDiff(after.Object)
	if reflect.DeepEqual(before, afterObj) {
		planned.Action = ActionNone
		return planned, nil
	}
	maskSecret(before, afterObj)
	planned.Action = ActionChange
	planned.Diff, err = unifiedDiff(before, afterObj)
	return planned, err
}

func plannedCreate(planned PlannedObject, obj map[string]interface{}) (PlannedObject, error) {
	after := forDiff(obj)
	maskSecret(nil, after)
	planned.Action = ActionCreate
	var err error
	planned.Diff, err = unifiedDiff(nil, after)
	return planned, err
}

func isNamespaceNotFound(err error) bool {
	var status *apierrors.StatusError
	return errors.As(err, &status) && apierrors.IsNotFound(err) &&
		status.ErrStatus.Details != nil && status.ErrStatus.Details.Kind == "namespaces"
}

// serverOnlyMetadata is metadata the API server owns; it changes on every
// write and says nothing about what ct would change.
var serverOnlyMetadata = []string{"managedFields", "resourceVersion", "generation", "uid", "creationTimestamp", "selfLink"}

// forDiff returns a deep copy of obj without status and server-owned
// metadata.
func forDiff(obj map[string]interface{}) map[string]interface{} {
	out := jsonCopy(obj)
	delete(out, "status")
	if meta, ok := out["metadata"].(map[string]interface{}); ok {
		for _, k := range serverOnlyMetadata {
			delete(meta, k)
		}
	}
	return out
}

func jsonCopy(obj map[string]interface{}) map[string]interface{} {
	// JSON round trip normalizes number types between the live object (int64)
	// and what was sent (float64 or int64), so equal values compare equal.
	data, err := json.Marshal(obj)
	if err != nil {
		return (&unstructured.Unstructured{Object: obj}).DeepCopy().Object
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return (&unstructured.Unstructured{Object: obj}).DeepCopy().Object
	}
	return out
}

const (
	maskedValue   = "***"
	maskedChanged = "*** (changed)"
)

// lastAppliedAnnotation holds a full copy of the object written by
// `kubectl apply`, Secret values included.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// maskSecret replaces Secret values in before and after (either may be nil)
// so a diff shows which keys are added, removed or changed but never a value.
func maskSecret(before, after map[string]interface{}) {
	if !isSecret(before) && !isSecret(after) {
		return
	}
	for _, field := range []string{"data", "stringData"} {
		b, _ := mapField(before, field)
		a, _ := mapField(after, field)
		for k, v := range a {
			if old, ok := b[k]; ok && !reflect.DeepEqual(old, v) {
				a[k] = maskedChanged
				b[k] = maskedValue
				continue
			}
			a[k] = maskedValue
		}
		for k := range b {
			b[k] = maskedValue
		}
	}
	for _, obj := range []map[string]interface{}{before, after} {
		meta, _ := mapField(obj, "metadata")
		if ann, ok := mapField(meta, "annotations"); ok {
			if _, has := ann[lastAppliedAnnotation]; has {
				ann[lastAppliedAnnotation] = maskedValue
			}
		}
	}
}

func isSecret(obj map[string]interface{}) bool {
	return obj != nil && obj["apiVersion"] == "v1" && obj["kind"] == "Secret"
}

func mapField(obj map[string]interface{}, field string) (map[string]interface{}, bool) {
	if obj == nil {
		return nil, false
	}
	m, ok := obj[field].(map[string]interface{})
	return m, ok
}

// unifiedDiff renders before and after (nil = absent) as YAML and diffs them.
func unifiedDiff(before, after map[string]interface{}) (string, error) {
	a, err := toYAML(before)
	if err != nil {
		return "", err
	}
	b, err := toYAML(after)
	if err != nil {
		return "", err
	}
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        diffLines(a),
		B:        diffLines(b),
		FromFile: "live",
		ToFile:   "ct",
		Context:  3,
	})
}

// diffLines splits text into lines that keep their "\n"; unlike
// difflib.SplitLines it adds no empty line at the end.
func diffLines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if last := len(lines) - 1; lines[last] == "" {
		lines = lines[:last]
	} else {
		lines[last] += "\n"
	}
	return lines
}

func toYAML(obj map[string]interface{}) (string, error) {
	if obj == nil {
		return "", nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(obj); err != nil {
		return "", fmt.Errorf("rendering diff: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("rendering diff: %w", err)
	}
	return strings.TrimRight(buf.String(), "\n") + "\n", nil
}
