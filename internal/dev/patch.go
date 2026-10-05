package dev

import (
	"fmt"
	"strings"

	"github.com/cloudticon/ct/pkg/engine"
)

// defaultContainerAnnotation tells the API clients that honor it (kubectl,
// and ct's own log streaming) which container of a multi-container pod to
// use when none is named explicitly.
const defaultContainerAnnotation = "kubectl.kubernetes.io/default-container"

// ResolveContainers validates the `container` option of every target backed by
// a workload rendered from main.ct and fills in the default (the first
// container) when it is not set. After this, PatchResources, the terminal and
// sync all address the same container. Targets without a rendered workload
// (external selectors) keep whatever was configured.
func ResolveContainers(targets []Target, resources []engine.Resource) error {
	workloads := indexWorkloads(resources)
	for i := range targets {
		entry, ok := workloads[targets[i].Name]
		if !ok || entry.conflict {
			continue
		}
		names := containerNames(getContainers(entry.resource))
		if len(names) == 0 {
			continue
		}
		if targets[i].Container == "" {
			targets[i].Container = names[0]
			continue
		}
		if containsString(names, targets[i].Container) {
			continue
		}
		kind, _ := entry.resource["kind"].(string)
		if containsString(containerNames(getInitContainers(entry.resource)), targets[i].Container) {
			return fmt.Errorf("target %q: container %q is an init container of %s %q; dev mode can only target regular containers (%s)",
				targets[i].Name, targets[i].Container, kind, targets[i].Name, strings.Join(names, ", "))
		}
		return fmt.Errorf("target %q: container %q not found in %s %q (containers: %s)",
			targets[i].Name, targets[i].Container, kind, targets[i].Name, strings.Join(names, ", "))
	}
	return nil
}

// ValidatePatches rejects workload options that PatchResources could not
// apply, instead of silently ignoring them: options on a target without a
// (unique) workload of that name in main.ct, and replicas on workloads that
// have no replica count.
func ValidatePatches(targets []Target, resources []engine.Resource) error {
	workloads := indexWorkloads(resources)
	for _, t := range targets {
		options := patchOptions(t)
		if len(options) == 0 {
			continue
		}
		entry, ok := workloads[t.Name]
		if !ok {
			return fmt.Errorf("target %q: %s change the workload, but main.ct renders no Deployment/StatefulSet/DaemonSet/ReplicaSet/Job named %q; "+
				"remove them for workloads ct does not manage, or name the target after the workload",
				t.Name, strings.Join(options, ", "), t.Name)
		}
		if entry.conflict {
			return fmt.Errorf("target %q: %s cannot be applied: several workloads are named %q", t.Name, strings.Join(options, ", "), t.Name)
		}
		kind, _ := entry.resource["kind"].(string)
		if t.Replicas != nil && (kind == "DaemonSet" || kind == "Job") {
			return fmt.Errorf("target %q: replicas cannot be set on a %s", t.Name, kind)
		}
	}
	return nil
}

func patchOptions(t Target) []string {
	var options []string
	if t.Image != "" {
		options = append(options, "image")
	}
	if len(t.Command) > 0 {
		options = append(options, "command")
	}
	if t.Replicas != nil {
		options = append(options, "replicas")
	}
	if len(t.Env) > 0 {
		options = append(options, "env")
	}
	if t.WorkingDir != "" {
		options = append(options, "workingDir")
	}
	if t.Probes != nil {
		options = append(options, "probes")
	}
	return options
}

// PatchResources modifies workload resources based on dev target config.
// Called between template render and apply.
// Default: probes are REMOVED unless target.Probes == true.
func PatchResources(resources []engine.Resource, targets []Target) {
	workloads := indexWorkloads(resources)
	for _, t := range targets {
		entry, ok := workloads[t.Name]
		if !ok || entry.conflict {
			continue
		}
		res := entry.resource
		if t.Replicas != nil {
			setReplicas(res, *t.Replicas)
		}

		containerIdx := findContainer(res, t.Container)
		if containerIdx < 0 {
			// Unknown container name: never fall back to another container,
			// that would patch e.g. a sidecar. ResolveContainers reports it.
			continue
		}

		if t.Probes == nil || !*t.Probes {
			removeProbes(res, containerIdx)
		}
		if len(t.Env) > 0 {
			mergeEnvVars(res, t.Env, containerIdx)
		}
		if len(t.Command) > 0 {
			setContainerCommand(res, t.Command, containerIdx)
		}
		if t.WorkingDir != "" {
			setContainerWorkingDir(res, t.WorkingDir, containerIdx)
		}
		if t.Image != "" {
			setContainerImage(res, t.Image, containerIdx)
		}

		setContainerTTY(res, containerIdx)
		setDefaultContainer(res, containerIdx)
	}
}

// findContainer returns the index of the named container, the first container
// when name is empty, or -1 when there is no such container.
func findContainer(res engine.Resource, name string) int {
	containers := getContainers(res)
	if len(containers) == 0 {
		return -1
	}
	if name == "" {
		return 0
	}
	for i, c := range containers {
		cMap, _ := c.(map[string]interface{})
		if cMap["name"] == name {
			return i
		}
	}
	return -1
}

func removeProbes(res engine.Resource, containerIdx int) {
	containers := getContainers(res)
	if containerIdx >= len(containers) {
		return
	}
	c, _ := containers[containerIdx].(map[string]interface{})
	if c == nil {
		return
	}
	delete(c, "livenessProbe")
	delete(c, "readinessProbe")
	delete(c, "startupProbe")
}

func setReplicas(res engine.Resource, replicas int) {
	spec, ok := res["spec"].(map[string]interface{})
	if !ok {
		spec = make(map[string]interface{})
		res["spec"] = spec
	}
	spec["replicas"] = replicas
}

func mergeEnvVars(res engine.Resource, envVars []EnvVar, containerIdx int) {
	containers := getContainers(res)
	if containerIdx >= len(containers) {
		return
	}
	c, _ := containers[containerIdx].(map[string]interface{})
	if c == nil {
		return
	}

	existing, _ := c["env"].([]interface{})
	envMap := make(map[string]int, len(existing))
	for i, e := range existing {
		eMap, _ := e.(map[string]interface{})
		if name, ok := eMap["name"].(string); ok {
			envMap[name] = i
		}
	}

	for _, ev := range envVars {
		entry := map[string]interface{}{"name": ev.Name, "value": ev.Value}
		if idx, exists := envMap[ev.Name]; exists {
			existing[idx] = entry
		} else {
			existing = append(existing, entry)
		}
	}
	c["env"] = existing
}

func setContainerCommand(res engine.Resource, command []string, containerIdx int) {
	containers := getContainers(res)
	if containerIdx >= len(containers) {
		return
	}
	c, _ := containers[containerIdx].(map[string]interface{})
	if c == nil {
		return
	}
	cmdIface := make([]interface{}, len(command))
	for i, s := range command {
		cmdIface[i] = s
	}
	c["command"] = cmdIface
	// The dev command is the full command line (dev.ct has no args option).
	// Keeping the workload's args would append them to it, e.g.
	// "sleep infinity --port 8080", which crashes the container.
	delete(c, "args")
}

func setContainerWorkingDir(res engine.Resource, workingDir string, containerIdx int) {
	containers := getContainers(res)
	if containerIdx >= len(containers) {
		return
	}
	c, _ := containers[containerIdx].(map[string]interface{})
	if c == nil {
		return
	}
	c["workingDir"] = workingDir
}

func setContainerTTY(res engine.Resource, containerIdx int) {
	containers := getContainers(res)
	if containerIdx >= len(containers) {
		return
	}
	c, _ := containers[containerIdx].(map[string]interface{})
	if c == nil {
		return
	}
	c["tty"] = true
	c["stdin"] = true
}

func setContainerImage(res engine.Resource, image string, containerIdx int) {
	containers := getContainers(res)
	if containerIdx >= len(containers) {
		return
	}
	c, _ := containers[containerIdx].(map[string]interface{})
	if c == nil {
		return
	}
	c["image"] = image
}

// setDefaultContainer marks the patched container as the pod's default
// container. Exec and logs requests without an explicit container are
// rejected by the API server for multi-container pods (e.g. with an injected
// service-mesh sidecar); clients resolve the default from this annotation.
func setDefaultContainer(res engine.Resource, containerIdx int) {
	containers := getContainers(res)
	if containerIdx >= len(containers) {
		return
	}
	c, _ := containers[containerIdx].(map[string]interface{})
	name, _ := c["name"].(string)
	if name == "" {
		return
	}
	spec, _ := res["spec"].(map[string]interface{})
	tmpl, _ := spec["template"].(map[string]interface{})
	if tmpl == nil {
		return
	}
	meta, _ := tmpl["metadata"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
		tmpl["metadata"] = meta
	}
	annotations, _ := meta["annotations"].(map[string]interface{})
	if annotations == nil {
		annotations = map[string]interface{}{}
		meta["annotations"] = annotations
	}
	annotations[defaultContainerAnnotation] = name
}

func getContainers(res engine.Resource) []interface{} {
	return getPodSpecList(res, "containers")
}

func getInitContainers(res engine.Resource) []interface{} {
	return getPodSpecList(res, "initContainers")
}

func getPodSpecList(res engine.Resource, key string) []interface{} {
	spec, _ := res["spec"].(map[string]interface{})
	tmpl, _ := spec["template"].(map[string]interface{})
	tSpec, _ := tmpl["spec"].(map[string]interface{})
	list, _ := tSpec[key].([]interface{})
	return list
}

func containerNames(containers []interface{}) []string {
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		cMap, _ := c.(map[string]interface{})
		if name, ok := cMap["name"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
