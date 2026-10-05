// Package manifest holds pure transformations on rendered Kubernetes objects:
// cleaning, namespace defaulting, scope detection, ordering and duplicate
// detection. Nothing here talks to a cluster, so everything is unit-testable
// and shared by `ct template`, `ct apply` and `ct dev`.
package manifest

import (
	"fmt"
	"sort"
	"strings"
)

// Resource is a rendered Kubernetes object.
type Resource = map[string]interface{}

// ScopeMarker is the property factory libraries set on cluster-scoped objects
// (`__ctts_scope: "cluster"`). It is stripped before output.
const ScopeMarker = "__ctts_scope"

// Clean removes null/undefined values in place. Objects and arrays that end
// up empty only because their null members were removed are dropped too, as
// are empty arrays (the Kubernetes API treats an empty list like a missing
// one). Objects written as {} are kept: `emptyDir: {}` or `podSelector: {}`
// mean something different from a missing field.
func Clean(obj map[string]interface{}) {
	for k, v := range obj {
		cleaned, keep := cleanValue(v)
		if !keep {
			delete(obj, k)
			continue
		}
		obj[k] = cleaned
	}
}

func cleanValue(v interface{}) (interface{}, bool) {
	switch val := v.(type) {
	case nil:
		return nil, false
	case map[string]interface{}:
		if len(val) == 0 {
			return val, true
		}
		Clean(val)
		return val, len(val) > 0
	case []interface{}:
		cleaned := make([]interface{}, 0, len(val))
		for _, item := range val {
			if cv, keep := cleanValue(item); keep {
				cleaned = append(cleaned, cv)
			}
		}
		return cleaned, len(cleaned) > 0
	default:
		return v, true
	}
}

// Group returns the API group of an apiVersion ("apps/v1" -> "apps",
// "v1" -> ""). The non-canonical "core/v1" is treated as the core group.
func Group(apiVersion string) string {
	group, _, found := strings.Cut(apiVersion, "/")
	if !found || group == "core" {
		return ""
	}
	return group
}

// IsClusterScoped reports whether apiVersion/kind is a well-known
// cluster-scoped type. Factory libraries should mark their own cluster-scoped
// kinds with ScopeMarker; this list is the safety net for hand-built objects.
func IsClusterScoped(apiVersion, kind string) bool {
	_, ok := clusterScopedKinds[Group(apiVersion)+"/"+kind]
	return ok
}

var clusterScopedKinds = setOf(
	"/Namespace", "/Node", "/PersistentVolume", "/ComponentStatus",
	"rbac.authorization.k8s.io/ClusterRole",
	"rbac.authorization.k8s.io/ClusterRoleBinding",
	"apiextensions.k8s.io/CustomResourceDefinition",
	"apiregistration.k8s.io/APIService",
	"admissionregistration.k8s.io/MutatingWebhookConfiguration",
	"admissionregistration.k8s.io/ValidatingWebhookConfiguration",
	"admissionregistration.k8s.io/ValidatingAdmissionPolicy",
	"admissionregistration.k8s.io/ValidatingAdmissionPolicyBinding",
	"admissionregistration.k8s.io/MutatingAdmissionPolicy",
	"admissionregistration.k8s.io/MutatingAdmissionPolicyBinding",
	"storage.k8s.io/StorageClass",
	"storage.k8s.io/CSIDriver",
	"storage.k8s.io/CSINode",
	"storage.k8s.io/VolumeAttachment",
	"storage.k8s.io/VolumeAttributesClass",
	"scheduling.k8s.io/PriorityClass",
	"node.k8s.io/RuntimeClass",
	"networking.k8s.io/IngressClass",
	"networking.k8s.io/IPAddress",
	"networking.k8s.io/ServiceCIDR",
	"certificates.k8s.io/CertificateSigningRequest",
	"certificates.k8s.io/ClusterTrustBundle",
	"flowcontrol.apiserver.k8s.io/FlowSchema",
	"flowcontrol.apiserver.k8s.io/PriorityLevelConfiguration",
	"resource.k8s.io/DeviceClass",
	"resource.k8s.io/ResourceSlice",
	"policy/PodSecurityPolicy",
	// Widely used CRDs.
	"cert-manager.io/ClusterIssuer",
	"external-secrets.io/ClusterSecretStore",
	"external-secrets.io/ClusterExternalSecret",
	"gateway.networking.k8s.io/GatewayClass",
	"snapshot.storage.k8s.io/VolumeSnapshotClass",
	"snapshot.storage.k8s.io/VolumeSnapshotContent",
	"kyverno.io/ClusterPolicy",
	"kyverno.io/ClusterCleanupPolicy",
	"argoproj.io/ClusterWorkflowTemplate",
	"karpenter.sh/NodePool",
	"karpenter.k8s.aws/EC2NodeClass",
	"cilium.io/CiliumClusterwideNetworkPolicy",
)

func setOf(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	return set
}

// Normalize strips the scope marker, cleans null values and sets
// metadata.namespace to namespace on namespaced objects that don't have one.
// An empty namespace leaves namespaces untouched.
func Normalize(resources []Resource, namespace string) {
	for _, res := range resources {
		scope, _ := res[ScopeMarker].(string)
		delete(res, ScopeMarker)
		Clean(res)

		if namespace == "" || scope == "cluster" {
			continue
		}
		apiVersion, _ := res["apiVersion"].(string)
		kind, _ := res["kind"].(string)
		if IsClusterScoped(apiVersion, kind) {
			continue
		}
		meta, ok := res["metadata"].(map[string]interface{})
		if !ok {
			continue
		}
		if ns, _ := meta["namespace"].(string); ns == "" {
			meta["namespace"] = namespace
		}
	}
}

// Ref names an object for messages: `Deployment "web"` or
// `Deployment "web" (namespace "prod")`.
func Ref(res Resource) string {
	kind, _ := res["kind"].(string)
	meta, _ := res["metadata"].(map[string]interface{})
	name, _ := meta["name"].(string)
	ns, _ := meta["namespace"].(string)
	if kind == "" {
		kind = "<no kind>"
	}
	if ns == "" {
		return fmt.Sprintf("%s %q", kind, name)
	}
	return fmt.Sprintf("%s %q (namespace %q)", kind, name, ns)
}

// identity is the server-side identity of an object. The version is left
// out on purpose: apps/v1 and apps/v1beta1 Deployments with the same name are
// the same object.
func identity(res Resource) string {
	apiVersion, _ := res["apiVersion"].(string)
	kind, _ := res["kind"].(string)
	meta, _ := res["metadata"].(map[string]interface{})
	name, _ := meta["name"].(string)
	ns, _ := meta["namespace"].(string)
	if IsClusterScoped(apiVersion, kind) {
		ns = ""
	}
	return Group(apiVersion) + "|" + kind + "|" + ns + "|" + name
}

// Duplicate describes an object registered more than once. Indexes point
// into the rendered resource list (registration order).
type Duplicate struct {
	Ref     string
	Indexes []int
}

// FindDuplicates returns objects registered more than once, in order of first
// registration. Applying such a list silently keeps only the last copy.
func FindDuplicates(resources []Resource) []Duplicate {
	first := make(map[string]int, len(resources))
	dupAt := map[string]int{}
	var dups []Duplicate
	for i, res := range resources {
		key := identity(res)
		f, ok := first[key]
		if !ok {
			first[key] = i
			continue
		}
		if d, ok := dupAt[key]; ok {
			dups[d].Indexes = append(dups[d].Indexes, i)
			continue
		}
		dupAt[key] = len(dups)
		dups = append(dups, Duplicate{Ref: Ref(resources[f]), Indexes: []int{f, i}})
	}
	return dups
}

// installOrder mirrors Helm's install order so ct and Helm apply a release in
// the same sequence: namespaces and CRDs before the objects that need them.
var installOrder = []string{
	"PriorityClass",
	"Namespace",
	"NetworkPolicy",
	"ResourceQuota",
	"LimitRange",
	"PodSecurityPolicy",
	"PodDisruptionBudget",
	"ServiceAccount",
	"Secret",
	"SecretList",
	"ConfigMap",
	"StorageClass",
	"PersistentVolume",
	"PersistentVolumeClaim",
	"CustomResourceDefinition",
	"ClusterRole",
	"ClusterRoleList",
	"ClusterRoleBinding",
	"ClusterRoleBindingList",
	"Role",
	"RoleList",
	"RoleBinding",
	"RoleBindingList",
	"Service",
	"DaemonSet",
	"Pod",
	"ReplicationController",
	"ReplicaSet",
	"Deployment",
	"HorizontalPodAutoscaler",
	"StatefulSet",
	"Job",
	"CronJob",
	"IngressClass",
	"Ingress",
	"APIService",
	"MutatingWebhookConfiguration",
	"ValidatingWebhookConfiguration",
}

var installRank = func() map[string]int {
	rank := make(map[string]int, len(installOrder))
	for i, kind := range installOrder {
		rank[kind] = i
	}
	return rank
}()

// SortForApply orders resources the way Helm installs them. Kinds Helm
// doesn't know (custom resources) go last, alphabetically by kind. Objects of
// the same kind keep their registration order.
func SortForApply(resources []Resource) {
	sort.SliceStable(resources, func(i, j int) bool {
		ki, _ := resources[i]["kind"].(string)
		kj, _ := resources[j]["kind"].(string)
		ri, iok := installRank[ki]
		rj, jok := installRank[kj]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		default:
			return ki < kj
		}
	})
}
