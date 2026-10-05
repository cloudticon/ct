package engine_test

import (
	"testing"

	"github.com/cloudticon/ct/pkg/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noopPrompt(q string) (string, error) { return "", nil }

func TestExecuteDev_ConfigParsing(t *testing.T) {
	js := `config({ namespace: "test-ns", values: { dev: true, replicas: 1 } })`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	assert.Equal(t, "test-ns", result.Namespace)
	assert.Equal(t, true, result.Values["dev"])
	assert.Equal(t, int64(1), result.Values["replicas"])
}

func TestExecuteDev_ConfigNamespaceOnly(t *testing.T) {
	js := `config({ namespace: "my-ns" })`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	assert.Equal(t, "my-ns", result.Namespace)
	assert.Nil(t, result.Values)
}

func TestExecuteDev_DevTargetBasic(t *testing.T) {
	js := `
		config({ namespace: "ns" })
		dev("remix", {
			sync: [{ from: "./", to: "/app", exclude: ["/node_modules"] }],
			ports: [[3000, 8080]],
			terminal: "bash",
		})
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	require.Len(t, result.Targets, 1)

	target := result.Targets[0]
	assert.Equal(t, "remix", target.Name)
	assert.Nil(t, target.Selector)
	assert.Equal(t, "bash", target.Terminal)

	require.Len(t, target.Sync, 1)
	assert.Equal(t, "./", target.Sync[0]["from"])
	assert.Equal(t, "/app", target.Sync[0]["to"])

	require.Len(t, target.Ports, 1)
	portPair, ok := target.Ports[0].([]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(3000), portPair[0])
	assert.Equal(t, int64(8080), portPair[1])
}

func TestExecuteDev_DevTargetWithSelector(t *testing.T) {
	js := `
		config({ namespace: "ns" })
		dev("postgres", {
			selector: { "app": "pg" },
			ports: [5432],
		})
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	require.Len(t, result.Targets, 1)

	target := result.Targets[0]
	assert.Equal(t, "postgres", target.Name)
	assert.Equal(t, map[string]string{"app": "pg"}, target.Selector)
	require.Len(t, target.Ports, 1)
	assert.Equal(t, int64(5432), target.Ports[0])
}

func TestExecuteDev_MultipleTargets(t *testing.T) {
	js := `
		config({ namespace: "ns" })
		dev("remix", { ports: [[3000, 8080]], terminal: "bash" })
		dev("postgres", { selector: { "app": "pg" }, ports: [5432] })
		dev("auth-proxy", { ports: [[11239, 3000]] })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	require.Len(t, result.Targets, 3)
	assert.Equal(t, "remix", result.Targets[0].Name)
	assert.Equal(t, "postgres", result.Targets[1].Name)
	assert.Equal(t, "auth-proxy", result.Targets[2].Name)
}

func TestExecuteDev_DevTargetWorkloadPatches(t *testing.T) {
	js := `
		config({ namespace: "ns" })
		dev("web", {
			replicas: 1,
			env: [{ name: "NODE_OPTIONS", value: "" }],
			workingDir: "/workspace",
			image: "web:dev",
			command: ["npm", "run", "dev"],
			probes: false,
			container: "main",
		})
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	require.Len(t, result.Targets, 1)

	target := result.Targets[0]
	assert.Equal(t, "web", target.Name)
	assert.Equal(t, "main", target.Container)

	require.NotNil(t, target.Probes)
	assert.False(t, *target.Probes)

	require.NotNil(t, target.Replicas)
	assert.Equal(t, int64(1), *target.Replicas)

	require.Len(t, target.Env, 1)
	assert.Equal(t, "NODE_OPTIONS", target.Env[0]["name"])
	assert.Equal(t, "", target.Env[0]["value"])
	assert.Equal(t, "/workspace", target.WorkingDir)
	assert.Equal(t, "web:dev", target.Image)

	assert.Equal(t, []string{"npm", "run", "dev"}, target.Command)
}

func TestExecuteDev_DevTargetProbesTrue(t *testing.T) {
	js := `
		config({ namespace: "ns" })
		dev("web", { probes: true })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Targets[0].Probes)
	assert.True(t, *result.Targets[0].Probes)
}

func TestExecuteDev_DevTargetProbesNilByDefault(t *testing.T) {
	js := `
		config({ namespace: "ns" })
		dev("web", { ports: [8080] })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	assert.Nil(t, result.Targets[0].Probes)
}

func TestExecuteDev_SyncWithPolling(t *testing.T) {
	js := `
		config({ namespace: "ns" })
		dev("hasura", {
			selector: { "hasura.cloudticon.com/name": "hasura-dev" },
			sync: [{ from: "./hasura", to: "/hasura", polling: true }],
		})
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	require.Len(t, result.Targets[0].Sync, 1)

	sync := result.Targets[0].Sync[0]
	assert.Equal(t, "./hasura", sync["from"])
	assert.Equal(t, "/hasura", sync["to"])
	assert.Equal(t, true, sync["polling"])
}

func TestExecuteDev_EnvWithDefault(t *testing.T) {
	js := `
		const port = env("MY_PORT", 3000)
		config({ namespace: "ns", values: { port: port } })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{"MY_PORT": "8080"},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(8080), result.Values["port"])
}

func TestExecuteDev_EnvFallsBackToDefault(t *testing.T) {
	js := `
		const port = env("MISSING_VAR", 3000)
		config({ namespace: "ns", values: { port: port } })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3000), result.Values["port"])
}

func TestExecuteDev_EnvStringDefault(t *testing.T) {
	js := `
		const host = env("MY_HOST", "localhost")
		config({ namespace: "ns", values: { host: host } })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{"MY_HOST": "example.com"},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	assert.Equal(t, "example.com", result.Values["host"])
}

func TestExecuteDev_EnvNoDefaultReturnsString(t *testing.T) {
	js := `
		const val = env("MY_VAR")
		config({ namespace: "ns", values: { val: val } })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{"MY_VAR": "hello"},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", result.Values["val"])
}

func TestExecuteDev_EnvMissingNoDefault(t *testing.T) {
	js := `
		const val = env("NOPE")
		config({ namespace: "ns", values: { val: val } })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.NoError(t, err)
	assert.Equal(t, "", result.Values["val"])
}

func TestExecuteDev_Prompt(t *testing.T) {
	js := `
		const user = prompt("Username?")
		config({ namespace: "ns-" + user })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:  js,
		EnvVars: map[string]string{},
		PromptFn: func(q string) (string, error) {
			assert.Equal(t, "Username?", q)
			return "krs", nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "ns-krs", result.Namespace)
}

func TestExecuteDev_PromptUsedInValues(t *testing.T) {
	js := `
		const name = prompt("Project name?")
		config({ namespace: name, values: { name: name } })
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:  js,
		EnvVars: map[string]string{},
		PromptFn: func(q string) (string, error) {
			return "myproj", nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "myproj", result.Namespace)
	assert.Equal(t, "myproj", result.Values["name"])
}

func TestExecuteDev_JSError(t *testing.T) {
	js := `throw new Error("broken")`
	_, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken")
}

func TestExecuteDev_ConfigMissingArgError(t *testing.T) {
	js := `config()`
	_, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:   js,
		EnvVars:  map[string]string{},
		PromptFn: noopPrompt,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "argument 0 is required")
}

func TestExecuteDev_FullScenario(t *testing.T) {
	js := `
		const USERNAME = prompt("Username?")
		const NAMESPACE = "dev-" + USERNAME
		const REMIX_PORT = env("REMIX_PORT", 13492)
		const PG_PORT = env("PG_PORT", 43291)

		config({
			namespace: NAMESPACE,
			values: {
				dev: true,
				hosts: [
					{ name: "web", host: "web-" + USERNAME + ".dev.example.com" },
				],
			},
		})

		dev("remix", {
			replicas: 1,
			env: [{ name: "NODE_OPTIONS", value: "" }],
			command: ["npm", "run", "dev"],
			sync: [{ from: "./", to: "/app", exclude: ["/node_modules", "/.git"] }],
			ports: [[REMIX_PORT, 3000]],
			terminal: "npm i && bash",
		})

		dev("postgres", {
			selector: { "cnpg.io/cluster": "postgres" },
			ports: [[PG_PORT, 5432]],
		})
	`
	result, err := engine.ExecuteDev(engine.ExecuteDevOpts{
		JSCode:  js,
		EnvVars: map[string]string{"REMIX_PORT": "9999"},
		PromptFn: func(q string) (string, error) {
			return "alice", nil
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "dev-alice", result.Namespace)
	assert.Equal(t, true, result.Values["dev"])

	hosts, ok := result.Values["hosts"].([]interface{})
	require.True(t, ok)
	require.Len(t, hosts, 1)
	hostMap := hosts[0].(map[string]interface{})
	assert.Equal(t, "web-alice.dev.example.com", hostMap["host"])

	require.Len(t, result.Targets, 2)

	remix := result.Targets[0]
	assert.Equal(t, "remix", remix.Name)
	assert.Nil(t, remix.Selector)
	assert.Equal(t, "npm i && bash", remix.Terminal)
	require.NotNil(t, remix.Replicas)
	assert.Equal(t, int64(1), *remix.Replicas)
	assert.Equal(t, []string{"npm", "run", "dev"}, remix.Command)

	remixPort := remix.Ports[0].([]interface{})
	assert.Equal(t, int64(9999), remixPort[0])
	assert.Equal(t, int64(3000), remixPort[1])

	pg := result.Targets[1]
	assert.Equal(t, "postgres", pg.Name)
	assert.Equal(t, map[string]string{"cnpg.io/cluster": "postgres"}, pg.Selector)

	pgPort := pg.Ports[0].([]interface{})
	assert.Equal(t, int64(43291), pgPort[0])
	assert.Equal(t, int64(5432), pgPort[1])
}

func runEnvJS(t *testing.T, js string, env map[string]string) (*engine.DevResult, error) {
	t.Helper()
	return engine.ExecuteDev(engine.ExecuteDevOpts{JSCode: js, EnvVars: env, PromptFn: noopPrompt})
}

// env("DEBUG", false) returned the string "false" when DEBUG=false was set,
// and every non-empty string is truthy in JS.
func TestExecuteDev_EnvBooleanDefault(t *testing.T) {
	js := `
		const debug = env("DEBUG", false)
		config({ namespace: "ns", values: { debug: debug, branch: debug ? "on" : "off" } })
	`
	for value, want := range map[string]bool{"false": false, "0": false, "no": false, "true": true, "1": true, "YES": true, " on ": true} {
		result, err := runEnvJS(t, js, map[string]string{"DEBUG": value})
		require.NoError(t, err, value)
		assert.Equal(t, want, result.Values["debug"], "DEBUG=%q", value)
		assert.Equal(t, map[bool]string{true: "on", false: "off"}[want], result.Values["branch"], "DEBUG=%q", value)
	}

	result, err := runEnvJS(t, js, map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, false, result.Values["debug"])
}

func TestExecuteDev_EnvInvalidBooleanIsAnError(t *testing.T) {
	_, err := runEnvJS(t, `env("DEBUG", false)`, map[string]string{"DEBUG": "maybe"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DEBUG")
	assert.Contains(t, err.Error(), "boolean")
}

// An unparsable number used to come back as a string and only failed later,
// far away ("ports[0][0]: expected number, got string").
func TestExecuteDev_EnvInvalidNumberIsAnError(t *testing.T) {
	_, err := runEnvJS(t, `env("PORT", 3000)`, map[string]string{"PORT": "30o0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PORT")
	assert.Contains(t, err.Error(), `"30o0"`)
	assert.Contains(t, err.Error(), "number")
}

// `PORT=` is a common .env placeholder; with a typed default it means unset.
func TestExecuteDev_EnvEmptyValueUsesTypedDefault(t *testing.T) {
	js := `config({ namespace: "ns", values: { port: env("PORT", 3000), debug: env("DEBUG", true), name: env("NAME", "web") } })`
	result, err := runEnvJS(t, js, map[string]string{"PORT": "", "DEBUG": " ", "NAME": ""})
	require.NoError(t, err)
	assert.Equal(t, int64(3000), result.Values["port"])
	assert.Equal(t, true, result.Values["debug"])
	assert.Equal(t, "", result.Values["name"], "an explicitly empty string stays empty")
}

func TestExecuteDev_EnvNumberDefaultAcceptsDecimals(t *testing.T) {
	js := `config({ namespace: "ns", values: { ratio: env("RATIO", 1), port: env("PORT", 3000) } })`
	result, err := runEnvJS(t, js, map[string]string{"RATIO": "0.25", "PORT": " 8080 "})
	require.NoError(t, err)
	assert.Equal(t, 0.25, result.Values["ratio"])
	assert.Equal(t, int64(8080), result.Values["port"])
}
