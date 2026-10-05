package dev_test

import (
	"testing"

	"github.com/cloudticon/ct/internal/dev"
	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeWorkloadResource(name string) engine.Resource {
	return engine.Resource{
		"kind":     "Deployment",
		"metadata": map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"replicas": 3,
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"app": name},
			},
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "main",
							"image": "app:latest",
							"livenessProbe": map[string]interface{}{
								"httpGet": map[string]interface{}{"port": 8080},
							},
							"readinessProbe": map[string]interface{}{
								"httpGet": map[string]interface{}{"port": 8080},
							},
							"startupProbe": map[string]interface{}{
								"httpGet": map[string]interface{}{"port": 8080},
							},
							"env": []interface{}{
								map[string]interface{}{"name": "NODE_ENV", "value": "production"},
							},
						},
					},
				},
			},
		},
	}
}

func getFirstContainer(res engine.Resource) map[string]interface{} {
	spec := res["spec"].(map[string]interface{})
	tmpl := spec["template"].(map[string]interface{})
	tSpec := tmpl["spec"].(map[string]interface{})
	containers := tSpec["containers"].([]interface{})
	return containers[0].(map[string]interface{})
}

func TestPatchResources_ProbesRemovedByDefault(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	targets := []dev.Target{{Name: "web"}}

	dev.PatchResources(resources, targets)

	c := getFirstContainer(resources[0])
	_, hasLiveness := c["livenessProbe"]
	_, hasReadiness := c["readinessProbe"]
	_, hasStartup := c["startupProbe"]
	assert.False(t, hasLiveness, "livenessProbe should be removed")
	assert.False(t, hasReadiness, "readinessProbe should be removed")
	assert.False(t, hasStartup, "startupProbe should be removed")
}

func TestPatchResources_ProbesKeptWhenTrue(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	keepProbes := true
	targets := []dev.Target{{Name: "web", Probes: &keepProbes}}

	dev.PatchResources(resources, targets)

	c := getFirstContainer(resources[0])
	_, hasLiveness := c["livenessProbe"]
	_, hasReadiness := c["readinessProbe"]
	assert.True(t, hasLiveness, "livenessProbe should be kept")
	assert.True(t, hasReadiness, "readinessProbe should be kept")
}

func TestPatchResources_ProbesExplicitlyFalse(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	noProbes := false
	targets := []dev.Target{{Name: "web", Probes: &noProbes}}

	dev.PatchResources(resources, targets)

	c := getFirstContainer(resources[0])
	_, hasLiveness := c["livenessProbe"]
	assert.False(t, hasLiveness, "livenessProbe should be removed when Probes=false")
}

func TestPatchResources_ReplicasOverride(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	replicas := 1
	targets := []dev.Target{{Name: "web", Replicas: &replicas}}

	dev.PatchResources(resources, targets)

	spec := resources[0]["spec"].(map[string]interface{})
	assert.Equal(t, 1, spec["replicas"])
}

func TestPatchResources_ReplicasNotChangedWhenNil(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	targets := []dev.Target{{Name: "web"}}

	dev.PatchResources(resources, targets)

	spec := resources[0]["spec"].(map[string]interface{})
	assert.Equal(t, 3, spec["replicas"])
}

func TestPatchResources_EnvMerge(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	targets := []dev.Target{{
		Name: "web",
		Env: []dev.EnvVar{
			{Name: "NODE_ENV", Value: "development"},
			{Name: "DEBUG", Value: "*"},
		},
	}}

	dev.PatchResources(resources, targets)

	c := getFirstContainer(resources[0])
	envs := c["env"].([]interface{})
	require.Len(t, envs, 2)

	env0 := envs[0].(map[string]interface{})
	assert.Equal(t, "NODE_ENV", env0["name"])
	assert.Equal(t, "development", env0["value"])

	env1 := envs[1].(map[string]interface{})
	assert.Equal(t, "DEBUG", env1["name"])
	assert.Equal(t, "*", env1["value"])
}

func TestPatchResources_CommandOverride(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	targets := []dev.Target{{
		Name:    "web",
		Command: []string{"npm", "run", "dev"},
	}}

	dev.PatchResources(resources, targets)

	c := getFirstContainer(resources[0])
	cmd := c["command"].([]interface{})
	assert.Equal(t, []interface{}{"npm", "run", "dev"}, cmd)
}

func TestPatchResources_WorkingDir(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	targets := []dev.Target{{
		Name:       "web",
		WorkingDir: "/workspace",
	}}

	dev.PatchResources(resources, targets)

	c := getFirstContainer(resources[0])
	assert.Equal(t, "/workspace", c["workingDir"])
}

func TestPatchResources_Image(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	targets := []dev.Target{{
		Name:  "web",
		Image: "web:dev",
	}}

	dev.PatchResources(resources, targets)

	c := getFirstContainer(resources[0])
	assert.Equal(t, "web:dev", c["image"])
}

func TestPatchResources_TTYAlwaysEnabled(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	targets := []dev.Target{{Name: "web"}}

	dev.PatchResources(resources, targets)

	c := getFirstContainer(resources[0])
	assert.Equal(t, true, c["tty"], "tty should be enabled")
	assert.Equal(t, true, c["stdin"], "stdin should be enabled")
}

func TestPatchResources_TTYOnSpecificContainer(t *testing.T) {
	res := engine.Resource{
		"kind":     "Deployment",
		"metadata": map[string]interface{}{"name": "multi"},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"app": "multi"},
			},
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{"name": "sidecar", "image": "sidecar:latest"},
						map[string]interface{}{"name": "app", "image": "app:latest"},
					},
				},
			},
		},
	}
	resources := []engine.Resource{res}
	targets := []dev.Target{{Name: "multi", Container: "app"}}

	dev.PatchResources(resources, targets)

	spec := resources[0]["spec"].(map[string]interface{})
	tmpl := spec["template"].(map[string]interface{})
	tSpec := tmpl["spec"].(map[string]interface{})
	containers := tSpec["containers"].([]interface{})

	sidecar := containers[0].(map[string]interface{})
	_, sidecarHasTTY := sidecar["tty"]
	assert.False(t, sidecarHasTTY, "sidecar should not have tty set")

	app := containers[1].(map[string]interface{})
	assert.Equal(t, true, app["tty"], "app container should have tty enabled")
	assert.Equal(t, true, app["stdin"], "app container should have stdin enabled")
}

func TestPatchResources_ExternalTargetSkipped(t *testing.T) {
	resources := []engine.Resource{}
	targets := []dev.Target{{
		Name:     "postgres",
		Selector: map[string]string{"app": "pg"},
	}}
	dev.PatchResources(resources, targets)
}

func TestPatchResources_SpecificContainer(t *testing.T) {
	res := engine.Resource{
		"kind":     "Deployment",
		"metadata": map[string]interface{}{"name": "multi"},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"app": "multi"},
			},
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "sidecar",
							"image": "sidecar:latest",
							"livenessProbe": map[string]interface{}{
								"httpGet": map[string]interface{}{"port": 9090},
							},
						},
						map[string]interface{}{
							"name":  "app",
							"image": "app:latest",
							"livenessProbe": map[string]interface{}{
								"httpGet": map[string]interface{}{"port": 8080},
							},
						},
					},
				},
			},
		},
	}
	resources := []engine.Resource{res}
	targets := []dev.Target{{
		Name:      "multi",
		Container: "app",
		Command:   []string{"sh", "-c", "sleep infinity"},
	}}

	dev.PatchResources(resources, targets)

	spec := resources[0]["spec"].(map[string]interface{})
	tmpl := spec["template"].(map[string]interface{})
	tSpec := tmpl["spec"].(map[string]interface{})
	containers := tSpec["containers"].([]interface{})

	sidecar := containers[0].(map[string]interface{})
	_, sidecarHasProbe := sidecar["livenessProbe"]
	assert.True(t, sidecarHasProbe, "sidecar probe should be untouched")
	_, sidecarHasCmd := sidecar["command"]
	assert.False(t, sidecarHasCmd, "sidecar command should be untouched")

	app := containers[1].(map[string]interface{})
	_, appHasProbe := app["livenessProbe"]
	assert.False(t, appHasProbe, "app probe should be removed")
	cmd := app["command"].([]interface{})
	assert.Equal(t, []interface{}{"sh", "-c", "sleep infinity"}, cmd)
}

func TestPatchResources_MultipleTargets(t *testing.T) {
	resources := []engine.Resource{
		makeWorkloadResource("auth-proxy"),
		makeWorkloadResource("remix"),
	}
	replicas := 1
	targets := []dev.Target{
		{Name: "auth-proxy"},
		{Name: "remix", Replicas: &replicas, Command: []string{"npm", "run", "dev"}},
		{Name: "postgres", Selector: map[string]string{"app": "pg"}},
	}

	dev.PatchResources(resources, targets)

	authC := getFirstContainer(resources[0])
	_, hasProbe := authC["livenessProbe"]
	assert.False(t, hasProbe, "auth-proxy probes should be removed")

	remixSpec := resources[1]["spec"].(map[string]interface{})
	assert.Equal(t, 1, remixSpec["replicas"])

	remixC := getFirstContainer(resources[1])
	cmd := remixC["command"].([]interface{})
	assert.Equal(t, []interface{}{"npm", "run", "dev"}, cmd)
}

func TestPatchResources_AllPatchesCombined(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	replicas := 1
	targets := []dev.Target{{
		Name:     "web",
		Replicas: &replicas,
		Env:      []dev.EnvVar{{Name: "NODE_OPTIONS", Value: ""}},
		Command:  []string{"npm", "run", "dev"},
	}}

	dev.PatchResources(resources, targets)

	spec := resources[0]["spec"].(map[string]interface{})
	assert.Equal(t, 1, spec["replicas"])

	c := getFirstContainer(resources[0])
	_, hasLiveness := c["livenessProbe"]
	assert.False(t, hasLiveness)

	cmd := c["command"].([]interface{})
	assert.Equal(t, []interface{}{"npm", "run", "dev"}, cmd)

	envs := c["env"].([]interface{})
	require.Len(t, envs, 2)
}

func makeMultiContainerDeployment() engine.Resource {
	return engine.Resource{
		"kind":     "Deployment",
		"metadata": map[string]interface{}{"name": "multi"},
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"app": "multi"},
			},
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"initContainers": []interface{}{
						map[string]interface{}{"name": "migrate", "image": "migrate:latest"},
					},
					"containers": []interface{}{
						map[string]interface{}{"name": "sidecar", "image": "sidecar:latest"},
						map[string]interface{}{"name": "app", "image": "app:latest"},
					},
				},
			},
		},
	}
}

func containerByName(t *testing.T, res engine.Resource, name string) map[string]interface{} {
	t.Helper()
	spec := res["spec"].(map[string]interface{})
	tmpl := spec["template"].(map[string]interface{})
	tSpec := tmpl["spec"].(map[string]interface{})
	for _, c := range tSpec["containers"].([]interface{}) {
		cMap := c.(map[string]interface{})
		if cMap["name"] == name {
			return cMap
		}
	}
	t.Fatalf("container %q not found", name)
	return nil
}

// A typo in `container` used to silently patch the FIRST container (here the
// sidecar): its image/command were replaced and the pod broke.
func TestPatchResources_UnknownContainerDoesNotPatchFirstContainer(t *testing.T) {
	resources := []engine.Resource{makeMultiContainerDeployment()}
	targets := []dev.Target{{Name: "multi", Container: "ap", Image: "app:dev", Command: []string{"sleep", "infinity"}}}

	dev.PatchResources(resources, targets)

	sidecar := containerByName(t, resources[0], "sidecar")
	assert.Equal(t, "sidecar:latest", sidecar["image"], "sidecar must not be patched for an unknown container name")
	_, hasCmd := sidecar["command"]
	assert.False(t, hasCmd)
}

func TestResolveContainers_UnknownContainerIsAnError(t *testing.T) {
	resources := []engine.Resource{makeMultiContainerDeployment()}
	targets := []dev.Target{{Name: "multi", Container: "ap"}}

	err := dev.ResolveContainers(targets, resources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `container "ap" not found`)
	assert.Contains(t, err.Error(), "sidecar, app")
}

func TestResolveContainers_InitContainerIsRejected(t *testing.T) {
	resources := []engine.Resource{makeMultiContainerDeployment()}
	targets := []dev.Target{{Name: "multi", Container: "migrate"}}

	err := dev.ResolveContainers(targets, resources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init container")
}

func TestResolveContainers_DefaultsToFirstContainer(t *testing.T) {
	resources := []engine.Resource{makeMultiContainerDeployment()}
	targets := []dev.Target{
		{Name: "multi"},
		{Name: "external", Selector: map[string]string{"app": "pg"}},
	}

	require.NoError(t, dev.ResolveContainers(targets, resources))
	assert.Equal(t, "sidecar", targets[0].Container, "default container is the first one, the same one PatchResources patches")
	assert.Equal(t, "", targets[1].Container, "targets without a rendered workload keep the API default")
}

// Logs have no container parameter on the Cluster port; the pod template
// annotation makes the API default (used by logs and by kubectl) point at the
// dev container of a multi-container workload.
func TestPatchResources_MarksDevContainerAsDefault(t *testing.T) {
	resources := []engine.Resource{makeMultiContainerDeployment()}
	targets := []dev.Target{{Name: "multi", Container: "app"}}

	dev.PatchResources(resources, targets)

	spec := resources[0]["spec"].(map[string]interface{})
	tmpl := spec["template"].(map[string]interface{})
	meta, ok := tmpl["metadata"].(map[string]interface{})
	require.True(t, ok, "pod template metadata should be created")
	annotations := meta["annotations"].(map[string]interface{})
	assert.Equal(t, "app", annotations["kubectl.kubernetes.io/default-container"])
}

// Overriding `command` while keeping the workload's `args` produced e.g.
// `sleep infinity --port 8080`, which crashes the dev container.
func TestPatchResources_CommandOverrideDropsArgs(t *testing.T) {
	res := makeWorkloadResource("web")
	c := getFirstContainer(res)
	c["args"] = []interface{}{"--port", "8080"}
	resources := []engine.Resource{res}

	dev.PatchResources(resources, []dev.Target{{Name: "web", Command: []string{"sleep", "infinity"}}})

	c = getFirstContainer(resources[0])
	assert.Equal(t, []interface{}{"sleep", "infinity"}, c["command"])
	_, hasArgs := c["args"]
	assert.False(t, hasArgs, "args of the original command must not be appended to the dev command")
}

// Workload options on a target that has no workload in main.ct (an external
// selector, e.g. an operator-managed database) were silently not applied.
func TestValidatePatches_RejectsPatchesWithoutWorkload(t *testing.T) {
	resources := []engine.Resource{makeWorkloadResource("web")}
	err := dev.ValidatePatches([]dev.Target{
		{Name: "web", Image: "web:dev"},
		{Name: "postgres", Selector: map[string]string{"cnpg.io/cluster": "pg"}, Image: "postgres:17", Ports: []dev.PortRule{{Local: 5432, Remote: 5432}}},
	}, resources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `target "postgres"`)
	assert.Contains(t, err.Error(), "image")
	assert.Contains(t, err.Error(), "main.ct")

	require.NoError(t, dev.ValidatePatches([]dev.Target{
		{Name: "postgres", Selector: map[string]string{"cnpg.io/cluster": "pg"}, Ports: []dev.PortRule{{Local: 5432, Remote: 5432}}},
	}, resources), "port-forward/sync/terminal-only targets need no workload")
}

func TestValidatePatches_RejectsReplicasForDaemonSet(t *testing.T) {
	ds := makeWorkloadResource("agent")
	ds["kind"] = "DaemonSet"
	one := 1
	err := dev.ValidatePatches([]dev.Target{{Name: "agent", Replicas: &one}}, []engine.Resource{ds})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DaemonSet")
}
