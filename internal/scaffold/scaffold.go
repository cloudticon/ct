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

## Commands

- Render and validate: ` + "`ct template <release> . -n <namespace>`" + `. On problems
  it exits non-zero with ` + "`file:line:column`" + ` diagnostics pointing at the .ct
  code; add ` + "`--error-format json`" + ` for machine-readable errors.
- Environments: ` + "`ct template <release> . -f values.json -f values-prod.json`" + `
  (files deep-merge left to right); one-off overrides with ` + "`--set key=value`" + `.
- Deploy: ` + "`ct apply <release> . -n <namespace>`" + ` (server-side apply, prunes
  objects removed from the code). Remove: ` + "`ct delete <release> -n <namespace>`" + `.
- Editor types: ` + "`ct types .`" + `.

## Rules

- Imports are URLs (` + "`github.com/<owner>/<repo>@<version>`" + `) or relative paths.
  There is no npm.
- No async/await: rendering is synchronous.
- Prefer factories from github.com/cloudticon/k8s-factories (` + "`webApp`" + `, ...);
  use primitives from github.com/cloudticon/k8s for other objects.
- Each object needs a unique kind + name + namespace.
- Run ` + "`ct template`" + ` after every change; it validates against the
  Kubernetes API schema before anything reaches a cluster.
`

// Options configures Init.
type Options struct {
	// Force overwrites an existing main.ct and values.json.
	Force bool
}

// Init writes a starter project into dir: main.ct, values.json and an
// AGENTS.md telling coding agents how to work with it. It refuses to touch an
// existing main.ct or values.json unless opts.Force is set. An existing
// AGENTS.md is never touched: it may be the repository's own. Init returns
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
