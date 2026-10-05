package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/fatih/color"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// ResourceRef identifies a Kubernetes resource by API version, kind, name and optional namespace.
type ResourceRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

// Delete removes resources and continues on NotFound errors.
// Deletion order is safety-aware: namespaced resources first, then Namespace objects,
// and inventory ConfigMaps at the very end.
func (c *client) del(ctx context.Context, resources []ResourceRef) error {
	ordered := orderForDelete(resources)
	var errs []error

	for _, ref := range ordered {
		if err := c.deleteOne(ctx, ref); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("deleting resources: %w", errors.Join(errs...))
}

func (c *client) deleteOne(ctx context.Context, ref ResourceRef) error {
	info, err := c.resolveResourceInfo(ref.APIVersion, ref.Kind)
	if err != nil {
		return fmt.Errorf("resolving resource info for %s %s: %w", ref.APIVersion, ref.Kind, err)
	}

	var dynClient dynamic.ResourceInterface
	targetNamespace := ref.Namespace

	if info.Namespaced {
		if targetNamespace == "" {
			targetNamespace = c.Namespace
		}
		dynClient = c.Dynamic.Resource(info.GVR).Namespace(targetNamespace)
	} else {
		targetNamespace = ""
		dynClient = c.Dynamic.Resource(info.GVR)
	}

	if err := dynClient.Delete(ctx, ref.Name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			log.Printf("%s %s/%s", color.YellowString("already deleted"), ref.Kind, ref.Name)
			return nil
		}
		return fmt.Errorf("deleting %s %q: %w", ref.Kind, ref.Name, err)
	}

	if targetNamespace != "" {
		log.Printf("%s %s/%s in namespace %s", color.HiRedString("deleted"), ref.Kind, ref.Name, targetNamespace)
	} else {
		log.Printf("%s %s/%s", color.HiRedString("deleted"), ref.Kind, ref.Name)
	}
	return nil
}

func orderForDelete(resources []ResourceRef) []ResourceRef {
	if len(resources) == 0 {
		return []ResourceRef{}
	}

	regular := make([]ResourceRef, 0, len(resources))
	namespaces := make([]ResourceRef, 0)
	inventoryConfigMaps := make([]ResourceRef, 0)

	for _, ref := range resources {
		switch {
		case isInventoryConfigMap(ref):
			inventoryConfigMaps = append(inventoryConfigMaps, ref)
		case isNamespaceResource(ref):
			namespaces = append(namespaces, ref)
		default:
			regular = append(regular, ref)
		}
	}

	// Undo the install order: custom resources first (while their CRD and
	// controller still exist), then known kinds in reverse install order.
	sort.SliceStable(regular, func(i, j int) bool {
		ri, iok := manifest.InstallRank(regular[i].APIVersion, regular[i].Kind)
		rj, jok := manifest.InstallRank(regular[j].APIVersion, regular[j].Kind)
		if iok != jok {
			return !iok
		}
		return iok && ri > rj
	})

	ordered := make([]ResourceRef, 0, len(resources))
	ordered = append(ordered, regular...)
	ordered = append(ordered, namespaces...)
	ordered = append(ordered, inventoryConfigMaps...)
	return ordered
}

func isNamespaceResource(ref ResourceRef) bool {
	return ref.APIVersion == "v1" && ref.Kind == "Namespace"
}

func isInventoryConfigMap(ref ResourceRef) bool {
	return ref.APIVersion == "v1" && ref.Kind == "ConfigMap" && strings.HasPrefix(ref.Name, inventoryCMPrefix)
}
