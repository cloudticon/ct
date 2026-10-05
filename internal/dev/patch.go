package dev

import "github.com/cloudticon/ct/pkg/engine"

// PatchResources modifies workload resources based on dev target config.
// Called between template render and apply.
// Default: probes are REMOVED unless target.Probes == true.
func PatchResources(resources []engine.Resource, targets []Target) {
	workloads := indexWorkloads(resources)
	for _, t := range targets {
		if t.Selector != nil {
			if _, ok := workloads[t.Name]; !ok {
				continue
			}
		}
		entry, ok := workloads[t.Name]
		if !ok || entry.conflict {
			continue
		}
		res := entry.resource
		containerIdx := findContainer(res, t.Container)

		if t.Probes == nil || !*t.Probes {
			removeProbes(res, containerIdx)
		}
		if t.Replicas != nil {
			setReplicas(res, *t.Replicas)
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
	}
}

func findContainer(res engine.Resource, name string) int {
	containers := getContainers(res)
	if name == "" || len(containers) == 0 {
		return 0
	}
	for i, c := range containers {
		cMap, _ := c.(map[string]interface{})
		if cMap["name"] == name {
			return i
		}
	}
	return 0
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

func getContainers(res engine.Resource) []interface{} {
	spec, _ := res["spec"].(map[string]interface{})
	tmpl, _ := spec["template"].(map[string]interface{})
	tSpec, _ := tmpl["spec"].(map[string]interface{})
	containers, _ := tSpec["containers"].([]interface{})
	return containers
}
