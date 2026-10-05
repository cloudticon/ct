package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/fatih/color"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

type Resource = map[string]interface{}

// crdEstablishTimeout bounds how long apply waits for discovery to serve a
// kind whose CRD was applied earlier in the same run.
var (
	crdEstablishTimeout = 30 * time.Second
	crdPollInterval     = 500 * time.Millisecond
)

// apply server-side-applies resources in Helm install order (namespaces and
// CRDs first). Custom resources whose CRD is part of the same release wait
// until the API server starts serving the new kind.
func (c *client) apply(ctx context.Context, resources []Resource) error {
	ordered := slices.Clone(resources)
	manifest.SortForApply(ordered)

	newKinds := map[string]bool{}
	for _, res := range ordered {
		if err := c.applyOne(ctx, res, newKinds); err != nil {
			return err
		}
		if groupKind, ok := crdGroupKind(res); ok {
			newKinds[groupKind] = true
		}
	}
	return nil
}

// crdGroupKind returns "group/Kind" served by a CustomResourceDefinition.
func crdGroupKind(res Resource) (string, bool) {
	if res["kind"] != "CustomResourceDefinition" {
		return "", false
	}
	spec, _ := res["spec"].(map[string]interface{})
	group, _ := spec["group"].(string)
	names, _ := spec["names"].(map[string]interface{})
	kind, _ := names["kind"].(string)
	if group == "" || kind == "" {
		return "", false
	}
	return group + "/" + kind, true
}

func (c *client) applyOne(ctx context.Context, res Resource, newKinds map[string]bool) error {
	obj := toUnstructured(res)

	info, err := c.resolveResourceInfo(obj.GetAPIVersion(), obj.GetKind())
	if err != nil && newKinds[manifest.Group(obj.GetAPIVersion())+"/"+obj.GetKind()] {
		info, err = c.waitForKind(ctx, obj.GetAPIVersion(), obj.GetKind())
	}
	if err != nil {
		return fmt.Errorf("resolving resource info for %s %s: %w", obj.GetAPIVersion(), obj.GetKind(), err)
	}

	var dynClient dynamic.ResourceInterface
	if info.Namespaced {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = c.Namespace
		}
		dynClient = c.Dynamic.Resource(info.GVR).Namespace(ns)
	} else {
		dynClient = c.Dynamic.Resource(info.GVR)
	}

	data, err := json.Marshal(obj.Object)
	if err != nil {
		return fmt.Errorf("marshaling %s %q: %w", obj.GetKind(), obj.GetName(), err)
	}

	force := true
	_, err = dynClient.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: "ct",
		Force:        &force,
	})
	if err != nil {
		return fmt.Errorf("applying %s %q: %w", obj.GetKind(), obj.GetName(), err)
	}

	log.Printf("%s %s/%s", color.GreenString("applied"), obj.GetKind(), obj.GetName())
	return nil
}

// waitForKind polls discovery until a freshly created CRD's kind is served.
func (c *client) waitForKind(ctx context.Context, apiVersion, kind string) (*resourceInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, crdEstablishTimeout)
	defer cancel()
	for {
		info, err := c.resolveResourceInfo(apiVersion, kind)
		if err == nil {
			return info, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for CRD to serve %s %s: %w", apiVersion, kind, err)
		case <-time.After(crdPollInterval):
		}
	}
}

func toUnstructured(res Resource) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: res}
}

func (c *client) resolveResourceInfo(apiVersion, kind string) (*resourceInfo, error) {
	key := apiVersion + "/" + kind
	if info, ok := c.gvrCache[key]; ok {
		return info, nil
	}

	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, fmt.Errorf("parsing apiVersion %q: %w", apiVersion, err)
	}

	resourceList, err := c.Discovery.ServerResourcesForGroupVersion(apiVersion)
	if err != nil {
		return nil, fmt.Errorf("discovering resources for %s: %w", apiVersion, err)
	}

	for _, r := range resourceList.APIResources {
		if r.Kind == kind {
			info := &resourceInfo{
				GVR: schema.GroupVersionResource{
					Group:    gv.Group,
					Version:  gv.Version,
					Resource: r.Name,
				},
				Namespaced: r.Namespaced,
			}
			c.gvrCache[key] = info
			return info, nil
		}
	}

	return nil, fmt.Errorf("kind %q not found in apiVersion %q", kind, apiVersion)
}
