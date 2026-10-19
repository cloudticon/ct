package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const mainCtTemplate = `import { webApp } from "github.com/cloudticon/k8s-factories@master";

// webApp registers a Deployment and a Service (plus HPA/Ingress when asked).
// Drop down to primitives from github.com/cloudticon/k8s for anything else.
webApp({
  name: Values.name,
  image: Values.image,
  port: Values.port,
  replicas: Values.replicas,
});
`

const valuesJsonTemplate = `{
  "name": "web-app",
  "image": "nginx:1.27",
  "port": 80,
  "replicas": 2
}
`

const agentsMdTemplate = "# Kubernetes manifests (ct)\n" + `
This directory is a [ct](https://github.com/cloudticon/ct) project. ` + "`main.ct`" + ` is
TypeScript that registers Kubernetes objects; ` + "`values.json`" + ` holds settings,
read in code as ` + "`Values.<key>`" + `.

## Work loop

1. Change ` + "`main.ct`" + ` or the values files.
2. Run ` + "`ct check`" + `. It renders and validates the base and every ` + "`values-*`" + `
   file without a cluster and prints one line per variant.
3. Read the diagnostics. Each has a ` + "`file:line:column`" + `, a stable ` + "`code`" + ` and a
   hint. Fix the first one and run ` + "`ct check`" + ` again until it exits 0. Use
   ` + "`--error-format json`" + ` for machine-readable errors.
4. Before touching a cluster, preview with ` + "`ct diff`" + ` or ` + "`ct apply --dry-run`" + `.

## Commands

- Check: ` + "`ct check`" + ` (or ` + "`ct check . -f values.json`" + ` for one combination).
- Render: ` + "`ct template my-app . -n default`" + ` prints the manifests.
- Environments: ` + "`-f values-prod.json`" + ` layers a file over the project values file
  (deep-merge, left to right); one-off overrides with ` + "`--set key=value`" + `.
- Field reference, offline: ` + "`ct explain Deployment.spec.template.spec.containers`" + `.
- Editor types: ` + "`ct types .`" + `.
- Preview: ` + "`ct diff <release> . -n <namespace> --context <context>`" + ` lists what apply
  would create, change and prune; ` + "`ct apply ... --dry-run`" + ` checks every object with
  the API server without writing.
- Deploy: ` + "`ct apply <release> . -n <namespace> --context <context>`" + ` (server-side
  apply, prunes objects removed from the code). Remove: ` + "`ct delete`" + `.

## Where the code of imports lives

Imported packages are cloned to ` + "`~/.ct/cache/<host>/<owner>/<repo>@<version>/`" + `
(for example ` + "`~/.ct/cache/github.com/cloudticon/k8s-factories@master/`" + `). Read
the TypeScript there to see which options a factory accepts and which objects
a primitive builds. Do not edit the cache.

## Exit codes

- 0: success.
- 1: the .ct code, values or manifests are wrong. Fix the project.
- 2: ct was called wrongly (arguments, flags, command). Read the usage hint.
- 3: the cluster is unreachable or refused the request. Not a code problem.

## Diagnostic codes

Each diagnostic carries one of these codes (stable; tools match them):
` + "`error`, `syntax`, `import`, `async-not-supported`, `runtime`, `timeout`," + `
` + "`duplicate-resource`, `invalid-resource`, `unknown-kind`, `unknown-field`," + `
` + "`invalid-value`, `missing-field`, `namespace-not-found`, `forbidden`," + `
` + "`invalid-input`, `values`, `unknown-values-key`, `usage`" + `.

## Rules

- Imports are URLs (` + "`github.com/<owner>/<repo>@<version>`" + `) or relative paths.
  There is no npm.
- No async/await: rendering is synchronous.
- Prefer factories from github.com/cloudticon/k8s-factories (` + "`webApp`" + `, ...);
  use primitives from github.com/cloudticon/k8s for other objects.
- Each object needs a unique kind + name + namespace.
- ` + "`Values`" + ` keys: reading a key that no values file declares is an error
  (` + "`unknown-values-key`" + `). Declare every key in the values file; write ` + "`null`" + `
  for optional ones and read them with ` + "`?.`" + ` or ` + "`??`" + `. The project values file
  (` + "`values.json`" + `/` + "`values.yaml`" + `) is always the base; ` + "`-f`" + ` files layer on top.
- ` + "`Release.name`" + ` is the first argument of ` + "`ct template`" + `/` + "`apply`" + ` (for ` + "`ct check`" + `:
  ` + "`--name`" + `, default the directory name); ` + "`Release.namespace`" + ` is ` + "`-n`" + `
  (default ` + "`default`" + `). Use them instead of hardcoding names and namespaces.
- Without a terminal (CI, agents) ` + "`apply`, `delete`, `diff` and `dev`" + ` require
  ` + "`--context <name>`" + `. Never rely on the default kubeconfig context.
- Validation uses the Kubernetes types built into ct. Pass ` + "`--validate=false`" + `
  only when the cluster is newer than ct and a field you need is reported as
  ` + "`unknown-field`" + `; do not use it to silence real errors.
`

// claudeMdTemplate only imports AGENTS.md, which stays the single source: Claude
// Code reads CLAUDE.md, not AGENTS.md.
const claudeMdTemplate = "@AGENTS.md\n"

// Options configures Init.
type Options struct {
	// Force overwrites an existing main.ct and values.json.
	Force bool
}

// Init writes a starter project into dir: main.ct, values.json, an
// AGENTS.md telling coding agents how to work with it and a CLAUDE.md that
// imports it. It refuses to touch an
// existing main.ct or values.json unless opts.Force is set. Existing
// AGENTS.md and CLAUDE.md are never touched: they may be the repository's own. Init returns
// the files written.
func Init(dir string, opts Options) ([]string, error) {
	files := []struct {
		name     string
		content  string
		optional bool // skipped when present, even with Force
	}{
		{"main.ct", mainCtTemplate, false},
		{"values.json", valuesJsonTemplate, false},
		{"AGENTS.md", agentsMdTemplate, true},
		{"CLAUDE.md", claudeMdTemplate, true},
	}

	if !opts.Force {
		var existing []string
		for _, f := range files {
			if !f.optional && fileExists(filepath.Join(dir, f.name)) {
				existing = append(existing, f.name)
			}
		}
		if len(existing) > 0 {
			return nil, fmt.Errorf("%s already exist(s) in %s; use --force to overwrite", strings.Join(existing, ", "), dir)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating project directory %s: %w", dir, err)
	}
	written := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if f.optional && fileExists(path) {
			continue
		}
		if err := os.WriteFile(path, []byte(f.content), 0o644); err != nil {
			return written, fmt.Errorf("writing %s: %w", path, err)
		}
		written = append(written, f.name)
	}
	return written, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
