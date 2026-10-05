package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/cloudticon/ct/pkg/manifest"
	"github.com/fatih/color"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Release lifecycle: apply orchestrator, inventory storage, prune, label
// injection, and namespace bootstrap. The pieces share a labelling scheme
// ("managed-by=ct" + "ct.cloudticon.com/instance=<release>") so they live
// together — splitting them across files would scatter related constants
// and require cross-file lookups during reads.

const (
	managedByLabelKey = "app.kubernetes.io/managed-by"
	managedByLabelVal = "ct"
	instanceLabelKey  = "ct.cloudticon.com/instance"
	inventoryDataKey  = "resources"
	inventoryCMPrefix = "ct-inventory-"
)

// ReleaseInfo summarizes a release tracked in inventory.
type ReleaseInfo struct {
	Name      string `json:"name" yaml:"name"`
	Namespace string `json:"namespace" yaml:"namespace"`
	Resources int    `json:"resources" yaml:"resources"`
}

// applyRelease performs a full release cycle: load existing inventory,
// apply resources, prune orphans, save updated inventory.
func (c *client) applyRelease(ctx context.Context, namespace, releaseName string, resources []Resource) error {
	oldRefs, err := loadInventory(ctx, c, namespace, releaseName)
	if err != nil {
		return fmt.Errorf("loading inventory: %w", err)
	}

	newRefs, err := resourcesToRefs(resources)
	if err != nil {
		return fmt.Errorf("building resource refs: %w", err)
	}

	defaultNs := namespace
	if defaultNs == "" {
		defaultNs = c.Namespace
	}
	orphaned := computeOrphaned(oldRefs, newRefs, defaultNs)

	// Track old+new before touching the cluster: if apply fails halfway, the
	// objects it already created stay in the inventory and a later run can
	// still prune them. A first install skips this, because the release may
	// create the namespace the inventory lives in.
	if added := computeOrphaned(newRefs, oldRefs, defaultNs); len(added) > 0 && len(oldRefs) > 0 {
		if err := saveInventoryRefs(ctx, c, namespace, releaseName, append(append([]ResourceRef{}, oldRefs...), added...)); err != nil {
			return fmt.Errorf("saving inventory: %w", err)
		}
	}

	if err := c.apply(ctx, resources); err != nil {
		return fmt.Errorf("applying resources: %w", err)
	}

	if len(orphaned) > 0 {
		log.Printf("%s %d orphaned resource(s)", color.HiRedString("pruning"), len(orphaned))
		if err := c.del(ctx, orphaned); err != nil {
			return fmt.Errorf("pruning orphaned resources: %w", err)
		}
	}

	if err := saveInventory(ctx, c, namespace, releaseName, resources); err != nil {
		return fmt.Errorf("saving inventory: %w", err)
	}

	return nil
}

// ensureNamespace creates the namespace if it does not exist, tagging it with
// the ct managed-by label.
func ensureNamespace(ctx context.Context, c *client, namespace string) error {
	if c == nil || c.CoreV1 == nil {
		return errors.New("k8s client is required")
	}
	if namespace == "" {
		return errors.New("namespace is required")
	}

	_, err := c.CoreV1.Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting namespace %q: %w", namespace, err)
	}

	_, err = c.CoreV1.Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				managedByLabelKey: managedByLabelVal,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %q: %w", namespace, err)
	}

	return nil
}

// --- Inventory CRUD ---

func saveInventory(ctx context.Context, c *client, namespace, releaseName string, resources []Resource) error {
	refs, err := resourcesToRefs(resources)
	if err != nil {
		return err
	}
	return saveInventoryRefs(ctx, c, namespace, releaseName, refs)
}

func saveInventoryRefs(ctx context.Context, c *client, namespace, releaseName string, refs []ResourceRef) error {
	if c == nil || c.CoreV1 == nil {
		return errors.New("k8s client is required")
	}
	if releaseName == "" {
		return errors.New("release name is required")
	}

	targetNamespace, err := resolveInventoryNamespace(c, namespace)
	if err != nil {
		return err
	}

	refsJSON, err := json.Marshal(refs)
	if err != nil {
		return fmt.Errorf("marshaling inventory refs: %w", err)
	}

	patchObj := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      inventoryConfigMapName(releaseName),
			"namespace": targetNamespace,
			"labels": map[string]interface{}{
				managedByLabelKey: managedByLabelVal,
				instanceLabelKey:  releaseName,
			},
		},
		"data": map[string]interface{}{
			inventoryDataKey: string(refsJSON),
		},
	}

	patchData, err := json.Marshal(patchObj)
	if err != nil {
		return fmt.Errorf("marshaling inventory configmap patch: %w", err)
	}

	force := true
	if _, err := c.CoreV1.ConfigMaps(targetNamespace).Patch(
		ctx,
		inventoryConfigMapName(releaseName),
		types.ApplyPatchType,
		patchData,
		metav1.PatchOptions{
			FieldManager: "ct",
			Force:        &force,
		},
	); err != nil {
		return fmt.Errorf("saving inventory configmap: %w", err)
	}

	return nil
}

func loadInventory(ctx context.Context, c *client, namespace, releaseName string) ([]ResourceRef, error) {
	if c == nil || c.CoreV1 == nil {
		return nil, errors.New("k8s client is required")
	}
	if releaseName == "" {
		return nil, errors.New("release name is required")
	}

	targetNamespace, err := resolveInventoryNamespace(c, namespace)
	if err != nil {
		return nil, err
	}

	cm, err := c.CoreV1.ConfigMaps(targetNamespace).Get(ctx, inventoryConfigMapName(releaseName), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []ResourceRef{}, nil
		}
		return nil, fmt.Errorf("loading inventory configmap: %w", err)
	}

	raw := cm.Data[inventoryDataKey]
	if raw == "" {
		return []ResourceRef{}, nil
	}

	var refs []ResourceRef
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil, fmt.Errorf("unmarshaling inventory resources: %w", err)
	}

	return refs, nil
}

func deleteInventory(ctx context.Context, c *client, namespace, releaseName string) error {
	if c == nil || c.CoreV1 == nil {
		return errors.New("k8s client is required")
	}
	if releaseName == "" {
		return errors.New("release name is required")
	}

	targetNamespace, err := resolveInventoryNamespace(c, namespace)
	if err != nil {
		return err
	}

	err = c.CoreV1.ConfigMaps(targetNamespace).Delete(ctx, inventoryConfigMapName(releaseName), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting inventory configmap: %w", err)
	}

	return nil
}

func listReleases(ctx context.Context, c *client, namespace string, allNamespaces bool) ([]ReleaseInfo, error) {
	if c == nil || c.CoreV1 == nil {
		return nil, errors.New("k8s client is required")
	}

	targetNamespace := ""
	if !allNamespaces {
		var err error
		targetNamespace, err = resolveInventoryNamespace(c, namespace)
		if err != nil {
			return nil, err
		}
	}

	cmList, err := c.CoreV1.ConfigMaps(targetNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: managedByLabelKey + "=" + managedByLabelVal,
	})
	if err != nil {
		return nil, fmt.Errorf("listing inventory configmaps: %w", err)
	}

	releases := make([]ReleaseInfo, 0, len(cmList.Items))
	for _, cm := range cmList.Items {
		// ConfigMaps a release renders carry the same labels as its
		// inventory; only ct-inventory-* ones are inventories.
		releaseName := cm.Labels[instanceLabelKey]
		if releaseName == "" || !strings.HasPrefix(cm.Name, inventoryCMPrefix) {
			continue
		}

		resourceCount := 0
		rawResources := cm.Data[inventoryDataKey]
		if rawResources != "" {
			var refs []json.RawMessage
			if err := json.Unmarshal([]byte(rawResources), &refs); err != nil {
				return nil, fmt.Errorf("unmarshaling inventory resources for %s/%s: %w", cm.Namespace, cm.Name, err)
			}
			resourceCount = len(refs)
		}

		releases = append(releases, ReleaseInfo{
			Name:      releaseName,
			Namespace: cm.Namespace,
			Resources: resourceCount,
		})
	}

	sort.Slice(releases, func(i, j int) bool {
		if releases[i].Namespace == releases[j].Namespace {
			return releases[i].Name < releases[j].Name
		}
		return releases[i].Namespace < releases[j].Namespace
	})

	return releases, nil
}

func inventoryConfigMapName(releaseName string) string {
	return inventoryCMPrefix + releaseName
}

func resolveInventoryNamespace(c *client, namespace string) (string, error) {
	if namespace != "" {
		return namespace, nil
	}
	if c.Namespace != "" {
		return c.Namespace, nil
	}
	return "", errors.New("namespace is required")
}

// resourcesToRefs builds a ResourceRef slice from raw manifests, validating
// apiVersion/kind/metadata.name on each.
func resourcesToRefs(resources []Resource) ([]ResourceRef, error) {
	refs := make([]ResourceRef, 0, len(resources))
	for i, resource := range resources {
		apiVersion, ok := resource["apiVersion"].(string)
		if !ok || apiVersion == "" {
			return nil, fmt.Errorf("resource %d has invalid apiVersion", i)
		}
		kind, ok := resource["kind"].(string)
		if !ok || kind == "" {
			return nil, fmt.Errorf("resource %d has invalid kind", i)
		}

		metadata, _ := resource["metadata"].(map[string]interface{})
		name, _ := metadata["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("resource %d has invalid metadata.name", i)
		}

		namespace, _ := metadata["namespace"].(string)
		refs = append(refs, ResourceRef{
			APIVersion: apiVersion,
			Kind:       kind,
			Name:       name,
			Namespace:  namespace,
		})
	}

	return refs, nil
}

// --- Prune (orphan detection) ---

// computeOrphaned returns refs that existed before but are no longer present.
// Refs are compared by server-side identity: API group (not version), kind,
// namespace (an empty one means defaultNamespace) and name. Comparing raw
// apiVersions and namespaces pruned the object just applied when an HPA
// moved from autoscaling/v1 to v2 or a release gained an explicit -n.
func computeOrphaned(oldRefs, newRefs []ResourceRef, defaultNamespace string) []ResourceRef {
	if len(oldRefs) == 0 {
		return []ResourceRef{}
	}

	newSet := make(map[string]struct{}, len(newRefs))
	for _, ref := range newRefs {
		newSet[resourceRefKey(ref, defaultNamespace)] = struct{}{}
	}

	orphaned := make([]ResourceRef, 0)
	seen := make(map[string]struct{}, len(oldRefs))
	for _, ref := range oldRefs {
		key := resourceRefKey(ref, defaultNamespace)
		if _, alreadyAdded := seen[key]; alreadyAdded {
			continue
		}
		seen[key] = struct{}{}

		if _, exists := newSet[key]; !exists {
			orphaned = append(orphaned, ref)
		}
	}

	return orphaned
}

func resourceRefKey(ref ResourceRef, defaultNamespace string) string {
	ns := ref.Namespace
	switch {
	case manifest.IsClusterScoped(ref.APIVersion, ref.Kind):
		ns = ""
	case ns == "":
		ns = defaultNamespace
	}
	return fmt.Sprintf("%s|%s|%s|%s", manifest.Group(ref.APIVersion), ref.Kind, ns, ref.Name)
}

// --- Label injection ---

// InjectReleaseLabels adds ct release labels to each resource metadata.labels
// without overwriting any existing label values.
func InjectReleaseLabels(resources []Resource, releaseName string) []Resource {
	if len(resources) == 0 {
		return []Resource{}
	}

	labeled := make([]Resource, 0, len(resources))
	for _, resource := range resources {
		resourceCopy := cloneMap(resource)

		metadata := map[string]interface{}{}
		if existingMetadata, ok := resourceCopy["metadata"].(map[string]interface{}); ok {
			metadata = cloneMap(existingMetadata)
		}

		labels := map[string]interface{}{}
		if existingLabels, ok := metadata["labels"].(map[string]interface{}); ok {
			labels = cloneMap(existingLabels)
		}

		if _, exists := labels[managedByLabelKey]; !exists {
			labels[managedByLabelKey] = managedByLabelVal
		}
		if _, exists := labels[instanceLabelKey]; !exists {
			labels[instanceLabelKey] = releaseName
		}

		metadata["labels"] = labels
		resourceCopy["metadata"] = metadata
		labeled = append(labeled, resourceCopy)
	}

	return labeled
}

func cloneMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return map[string]interface{}{}
	}

	cloned := make(map[string]interface{}, len(src))
	for k, v := range src {
		cloned[k] = v
	}

	return cloned
}
