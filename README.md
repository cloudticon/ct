# ct — Kubernetes packages in TypeScript, checked before they ship

[![Build](https://github.com/cloudticon/ct/actions/workflows/build.yml/badge.svg)](https://github.com/cloudticon/ct/actions/workflows/build.yml)
[![Release](https://github.com/cloudticon/ct/actions/workflows/release.yml/badge.svg)](https://github.com/cloudticon/ct/actions/workflows/release.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/cloudticon/ct)](https://github.com/cloudticon/ct)
[![License](https://img.shields.io/github/license/cloudticon/ct)](https://github.com/cloudticon/ct/blob/master/LICENSE)

> **⚠️ Beta.** `ct` is under active development. APIs, CLI flags and file formats may change between releases. Feedback and bug reports are welcome.

`ct` replaces Helm with real code and fast, precise feedback. You describe a release in TypeScript (`main.ct`), and `ct` renders it, **validates every object against the Kubernetes API before anything reaches a cluster**, and applies it with inventory-based pruning. Every error points at the line of your code that caused it, in plain text or JSON. That tight loop is what humans and AI coding agents both need.

```bash
ct init                         # main.ct, values.json, AGENTS.md
ct template my-app . -n prod    # render + validate -> YAML
ct apply my-app . -n prod       # render + validate + server-side apply + prune
```

**Documentation:** [cloudticon.com](https://cloudticon.com/) · **Direction:** [RFC: ct as an AI-friendly Helm replacement](docs/rfc/ai-friendly-helm-replacement.md)

## Why ct

- **Code, not text templates.** Loops, conditionals, functions and imports instead of `{{ range }}`, `nindent` and `_helpers.tpl`. Objects are values, so cross-references (`web.spec.selector.matchLabels`) can't drift.
- **Checked before it ships.** Built-in kinds are decoded strictly against the Kubernetes API types, plus the admission rules the API server enforces (selector vs. template labels, Job `restartPolicy`, port names, volume sources, ...). Unknown fields, wrong types, `core/v1`-style apiVersions and removed APIs fail at render time, not in Argo CD.
- **Errors point at your code.** Bundles carry source maps, and `ct` records where each object was registered, so diagnostics read `main.ct:12:3: Deployment "web": spec.replicas: ...`. Add `--error-format json` for tools and agents.
- **Releases without ceremony.** `ct apply` does server-side apply in Helm's install order (namespaces and CRDs first), tracks an inventory per release, and prunes objects you removed from the code.
- **Agent-ready.** `ct init` writes an `AGENTS.md`, output is deterministic, every diagnostic has a stable code, and git never blocks on a password prompt.
- **One static binary.** No Node.js, no Tiller, no plugins.

## Helm vs ct, side by side

The same Deployment, with labels and an optional `env` map from values.

**Helm** (`templates/deployment.yaml` + `_helpers.tpl`):

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "app.fullname" . }}
  labels:
    {{- include "app.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      {{- include "app.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "app.selectorLabels" . | nindent 8 }}
    spec:
      containers:
        - name: web
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          {{- with .Values.env }}
          env:
            {{- range $k, $v := . }}
            - name: {{ $k }}
              value: {{ $v | quote }}
            {{- end }}
          {{- end }}
```

**ct** (`main.ct`):

```typescript
import { deployment } from "github.com/cloudticon/k8s@master";

const labels = { app: Release.name };

deployment({
  name: Release.name,
  replicas: Values.replicas,
  selector: { matchLabels: labels },
  template: {
    metadata: { labels },
    spec: {
      containers: [{
        name: "web",
        image: `${Values.image.repository}:${Values.image.tag}`,
        env: Object.entries(Values.env ?? {}).map(([name, value]) => ({ name, value: String(value) })),
      }],
    },
  },
});
```

Helm renders whatever text the template produces, so a mismatched selector, a misindented block or a 16-character port name surfaces when the cluster (or Argo CD) rejects the object. `ct` reports it at render time, with the line in `main.ct` that caused it — see [Validation and errors](#validation-and-errors).

### And the others?

- **Kustomize** patches YAML; it has no loops, functions or values, and it doesn't check objects against the API.
- **cdk8s** is code too, but it needs Node.js, synthesizes YAML and leaves applying and pruning to other tools. `ct` is one binary that renders, validates and applies.
- **Pulumi** is general-purpose infrastructure as code with a state backend. `ct` keeps state in the cluster (an inventory ConfigMap per release) and stays a Kubernetes package tool.
- **Timoni** validates too, but you write CUE. `ct` uses TypeScript, which your team and your coding agent already know.

## Install

One-line install (Linux/macOS):

```bash
curl -fsSL https://cloudticon.com/install.sh | sudo sh
```

Via `go install`:

```bash
go install github.com/cloudticon/ct/cmd/ct@latest
```

Or build from source:

```bash
git clone https://github.com/cloudticon/ct.git
cd ct
go build -ldflags="-s -w" -o ct ./cmd/ct
```

Error codes and messages are a contract for AI agents, so they have golden tests: each directory in `internal/cli/testdata/diag/` is a broken project plus the `expected.json` that `ct --error-format json` must print for it. After an intended change to a diagnostic, refresh the expected files and review the diff:

```bash
go test ./internal/cli -run TestDiagnosticsGolden -update
```

## Quick start

```bash
ct init                                    # scaffold in the current directory (or: ct init myproject)
ct template my-app . -n production         # render to YAML (validated)
ct template my-app . -n production -o json
ct template my-app . -f values.yaml -f values-prod.yaml --set replicas=5
ct template my-app github.com/acme/my-app@v1.0 -n staging   # render a remote source
```

After `ct init`:

```
myproject/
  main.ct        # the release, in TypeScript
  values.json    # settings, read as Values.<key>
  AGENTS.md      # how coding agents should work on this project
```

No `tsconfig.json`, no generated directories, no `node_modules`.

## Writing main.ct

### High level: factories

[`k8s-factories`](https://github.com/cloudticon/k8s-factories) builds common shapes for you. `webApp` registers a Deployment and a Service (plus HPA, PVCs and Ingress/VirtualService when asked):

```typescript
import { webApp } from "github.com/cloudticon/k8s-factories@master";

webApp({
  name: "api",
  image: Values.image,
  port: 8080,
  replicas: Values.replicas,
  probes: "/healthz",
});
```

### Primitives: the Kubernetes API, typed

[`cloudticon/k8s`](https://github.com/cloudticon/k8s) has one typed factory per kind. Each call registers the object and returns it:

```typescript
import { deployment, service, configMap } from "github.com/cloudticon/k8s@master";

const labels = { app: "web" };

const web = deployment({
  name: "web",
  selector: { matchLabels: labels },
  template: {
    metadata: { labels },
    spec: {
      containers: [{ name: "web", image: Values.image, ports: [{ name: "http", containerPort: 8080 }] }],
    },
  },
});

service({
  name: "web",
  selector: web.spec.selector.matchLabels,   // cross-reference, not a copy
  ports: [{ port: 80, targetPort: "http" }],
});

configMap({ name: "web-config", data: { LOG_LEVEL: Values.logLevel ?? "info" } });
```

### Loops, conditionals, local helpers

It's TypeScript, so reuse is a function and an import:

```typescript
// lib/worker.ct
import { deployment } from "github.com/cloudticon/k8s@master";

export function worker(w: { name: string; queue: string; replicas?: number }) {
  const labels = { app: `worker-${w.name}` };
  return deployment({
    name: `worker-${w.name}`,
    replicas: w.replicas ?? 1,
    selector: { matchLabels: labels },
    template: {
      metadata: { labels },
      spec: { containers: [{ name: "worker", image: Values.image, args: ["--queue", w.queue] }] },
    },
  });
}
```

```typescript
// main.ct
import { worker } from "./lib/worker";   // .ct/.ts extensions are optional

for (const w of Values.workers) worker(w);
```

### Custom resources

`resource()` defines a typed factory for any CRD. Cluster-scoped kinds get no namespace:

```typescript
import { resource, z } from "github.com/cloudticon/k8s@master";

const redis = resource("redis.redis.opstreelabs.in/v1beta2", "Redis", {
  spec: { kubernetesConfig: z.object({ image: z.string() }) },
});

if (Values.redis?.enabled) {
  redis({ name: "cache", kubernetesConfig: { image: "redis:7.2" } });
}
```

### Imports

- Packages are git repositories, imported by URL: `github.com/{owner}/{repo}@{tag-or-branch}`, optionally with a sub-path (`github.com/acme/lib@v2/presets/ha`). Other hosts work too (`git.example.com/group/team/repo@v1`).
- Packages are cloned on first use into `~/.ct/cache/` and reused offline. `--no-cache` re-downloads the source and every imported package (useful for branch versions like `@master`).
- Private repositories need git credentials for https (e.g. `gh auth setup-git`). `ct` never waits on a password prompt.
- There is no npm, and no `async`/`await`: rendering is synchronous.

### Globals

| Global | Value |
| --- | --- |
| `Values` | merged values (files + `--set`) |
| `Release.name` | the release name given to `ct template`/`ct apply` |
| `Release.namespace` | the `-n` namespace (empty if not set) |

## Values

- **Layers, lowest first:** the project's values file (the first of `values.json`, `values.yaml`, `values.yml`) is always the base; each `-f` file is merged on top in the order given; `--set` and `--set-string` come last. A base file that is also passed with `-f` is not loaded twice.
- **Breaking change (beta.2):** a single `-f` used to replace the project's values file; now it only overrides it. Put just the overrides in `-f` files. There is no flag to drop the base.
- `-f` is repeatable. Files deep-merge left to right: objects merge key by key, everything else (including arrays) is replaced, like Helm. A relative `-f` that isn't in the working directory is looked up in the project, so remote sources can use their own `values-prod.yaml`.
- `--set key.nested=value` overrides one value. `true`/`false`/`null` and numbers are typed, but only when the number round-trips: `1.10`, `1.0` and `0123` stay strings. `--set-string` always sets a string. Escape dots in keys with a backslash: `--set 'annotations.nginx\.ingress\.kubernetes\.io/rewrite-target=/'`.

```bash
ct template shop . -f values.yaml -f values-prod.yaml --set-string image.tag=1.10
```

## Validation and errors

`ct template` and `ct apply` validate every rendered object before printing or applying anything:

- **built-in kinds** are decoded strictly into the Kubernetes API types: unknown fields, wrong types and wrong apiVersions are reported with their field path, all in one run;
- **removed APIs** (e.g. `extensions/v1beta1` Ingress, `batch/v1beta1` CronJob) name their replacement;
- **admission rules** the API server enforces: valid names and labels, selector matching the template labels, containers with name and image, at most one source per volume, `volumeMounts` referencing a volume (or a StatefulSet volume claim), Jobs with `restartPolicy: OnFailure|Never`, Services with ports, Ingress paths with `pathType`, base64 `Secret.data`, TLS secrets with `tls.crt`/`tls.key`, CRD names equal to `<plural>.<group>`, and more;
- **duplicates**: registering the same object twice is an error, not a silent last-write-wins.

Custom resources get metadata checks; their schemas live in their CRDs.

Each diagnostic names the code location that registered the object:

```
$ ct template web . -n prod
Error: 3 problems
main.ct:3:11: Deployment "web" (namespace "prod"): spec.replicas: Invalid value: expected an integer, got string [invalid-value]
main.ct:3:11: Deployment "web" (namespace "prod"): spec.template.metadata.labels: Invalid value: {"app":"website"}: `selector` does not match template `labels` [invalid-value]
main.ct:3:11: Deployment "web" (namespace "prod"): spec.template.spec.containers[0].ports[0].name: Invalid value: "http-metrics-port": must be no more than 15 characters [invalid-value]
```

Syntax and runtime errors carry positions in your `.ct` files too (bundles have source maps), with the offending line and a stack:

```
Error: lib/factory.ct:3:10: Error: name is required [runtime]
    at make (lib/factory.ct:3:10)
    at main.ct:4:5
```

With `--error-format json`, errors go to stderr as JSON with stable codes (`syntax`, `import`, `async-not-supported`, `runtime`, `timeout`, `duplicate-resource`, `invalid-resource`, `unknown-kind`, `unknown-field`, `invalid-value`, `missing-field`, `namespace-not-found`, `forbidden`, `invalid-input`, `values`, or `error` for anything else):

```json
{
  "errors": [
    {
      "code": "invalid-value",
      "message": "Invalid value: expected an integer, got string",
      "file": "main.ct",
      "line": 3,
      "column": 11,
      "resource": "Deployment \"web\" (namespace \"prod\")",
      "path": "spec.replicas"
    }
  ]
}
```

Validation uses the Kubernetes types `ct` was built with. If your cluster is newer and you use a field `ct` doesn't know yet, pass `--validate=false`.

### Field reference offline — `ct explain`

An unknown-field diagnostic points at `ct explain` for the object around the field (e.g. ``hint: check the field name and nesting with `ct explain Deployment.spec.template.spec.containers` ``). It shows the fields of a built-in kind, or of a field path inside it, with types, `-required-` markers and descriptions — from the same Kubernetes types validation uses, so it needs no cluster and no network:

```
$ ct explain Deployment.spec.strategy
KIND:       Deployment
VERSION:    apps/v1

FIELD: spec.strategy <DeploymentStrategy>

DESCRIPTION:
    The deployment strategy to use to replace existing pods with new ones.

FIELDS:
  rollingUpdate	<RollingUpdateDeployment>
    Rolling update config params. Present only if DeploymentStrategyType = RollingUpdate.

  type	<string>
    Type of deployment. Can be "Recreate" or "RollingUpdate". Default is RollingUpdate.
```

Kinds are case-insensitive and list indexes are ignored (`ct explain pod.spec.containers[0].resources`). A kind served under several apiVersions resolves to the stable, newest one (core group first); pick another with `--api-version autoscaling/v1`. `-o json` prints one JSON document (`kind`, `apiVersion`, `path`, `type`, `required`, `description`, `fields[]`). An unknown kind or field exits with code 2 and names the closest match (`Deployment.spec has no field "replcas"` → `did you mean "replicas"`). Custom resources are not covered — their schemas live in their CRDs.

### Reporting bad input from a package

Factories in packages should check their input and fail with an error that names the field. Throw an `Error` with a string `code`, and optionally a `path` (the field) and a `hint`; `ct` turns it into a diagnostic with exactly that code, path and hint. Use `invalid-input` for a bad argument to a factory:

```ts
// lib/web-app.ts, inside a package
export function webApp(opts: { replicas?: number }) {
  if (opts.replicas !== undefined && typeof opts.replicas !== "number") {
    throw Object.assign(new Error("expected a number, got " + typeof opts.replicas), {
      code: "invalid-input",
      path: "replicas",
      hint: "pass an integer, e.g. replicas: 3",
    });
  }
  // ...
}
```

The diagnostic points at the first stack frame outside imported packages, that is the call in your own code (`main.ct:4:5`), not the `throw` inside the package. The package frames stay in the stack. A plain `throw new Error("...")` (no `code`) is still reported as `runtime`.

```json
{
  "errors": [
    {
      "code": "invalid-input",
      "message": "expected a number, got string",
      "file": "main.ct",
      "line": 4,
      "column": 5,
      "path": "replicas",
      "hint": "pass an integer, e.g. replicas: 3",
      "stack": [{ "file": "github.com/acme/infra@v1/lib/web-app.ts", "line": 3, "column": 11, "function": "webApp" }]
    }
  ]
}
```

## Exit codes

The exit code tells you what to fix:

| Code | Meaning | What to do |
|------|---------|------------|
| 0 | Success | - |
| 1 | Error in `.ct` code, values or manifests (render and validation) | Fix the code; the diagnostic has `file:line` |
| 2 | Wrong use of `ct` (arguments, flags, unknown command) | Fix the call; the error shows the usage line and an example |
| 3 | Cluster error (connection, authorization, request rejected by apply, diff, delete, list, dev) | Check the cluster, kubeconfig and `--context` |

`--error-format json` keeps the same codes. Releases before `v0.1.0-beta.2` exited with 1 for every error; scripts that test for exactly 1 must accept 2 and 3 too.

## Releases: apply, diff, list, delete

- **Breaking change (beta.2):** `ct apply`, `ct delete` and `ct dev` need `--context` when stdin is not a terminal (agents, CI). Without it they exit with code 2 before any change, listing the kubeconfig contexts and the current one. In a terminal the default context still works, and the command prints its name and API server before it changes anything. Inside a cluster (in-cluster configuration) `--context` is not needed, and `ct list` never needs it. CI scripts that call `ct apply` without `--context` must add it.

```bash
ct apply my-app . -n production                     # render + validate + apply + prune
ct apply my-app . -n development --create-namespace
ct apply my-app . -n production --context prod-cluster -o yaml
ct apply my-app github.com/acme/my-app@v1.0 -n staging

ct diff my-app . -n production                      # what apply would create, change and prune
ct diff my-app . -n production -o json              # the same as a list of objects with an action
ct apply my-app . -n production --dry-run           # server-side checks and the summary, nothing written

ct list -n production       # releases in a namespace
ct list -A -o json          # all namespaces, structured

ct delete my-app -n production   # deletes what the inventory tracks; no source needed
ct delete my-app -n production --dry-run   # list what would be deleted, change nothing
```

`ct apply` uses server-side apply (field manager `ct`) in Helm's install order. Custom resources wait until the API server serves a CRD applied in the same run. Each release keeps an inventory in the ConfigMap `ct-inventory-<release>` (labels `app.kubernetes.io/managed-by=ct`, `ct.cloudticon.com/instance=<release>`); objects that disappear from the code are pruned on the next apply, including objects created by an apply that failed halfway.

When the cluster rejects an object, the error names the object and points at the line in your code that registered it, like a validation error:

- a field the API server refuses (an immutable field changed, a value its admission rejects, a field the CRD schema doesn't declare) gives one diagnostic per field with its path (`invalid-value`, `missing-field`, `unknown-field`);
- a kind the cluster doesn't serve gives `unknown-kind` with a hint about the missing CRD;
- a kubeconfig user without access gives `forbidden` with a hint to check `--context` and RBAC.

```
$ ct apply web . -n prod --context kind-ct-test --set app=web2
Error: main.ct:3:20: Deployment "web" (namespace "prod"): spec.selector: Invalid value: {"matchLabels":{"app":"web2"}}: field is immutable [invalid-value]
  hint: this field can't change on an existing object: keep the old value, or delete the object (or the release) and apply again to recreate it
```

### Preview: `ct diff` and `ct apply --dry-run`

Both take the same arguments and flags as `ct apply`, send every object to the API server as a server-side apply dry run (`dryRun=All`, so admission and schema checks run) and compare the result with the live object. Nothing is written to the cluster, the inventory included; `--create-namespace` only tells the plan that the namespace would exist.

- `ct diff` prints, on stdout, one line per object with its action — `create`, `change` or `none` — and a unified diff of the YAML (`--- live`, `+++ ct`) for created and changed objects, then the objects prune would delete (`prune`) and a one-line summary. Status and server-owned metadata (`managedFields`, `resourceVersion`, `uid`, ...) are not compared. It exits 0 whether or not something would change.
- `ct diff -o json` prints a JSON list of `{action, apiVersion, kind, namespace, name, diff}`, with `action` one of `create`, `change`, `none`, `prune`.
- `ct apply --dry-run` prints the same lines and summary without diffs, on stderr like the rest of apply's report; `-o yaml|json` still prints the rendered objects on stdout.
- Secret values never reach the output: every value under `data` and `stringData` (and kubectl's `last-applied-configuration` copy) shows as `***`, and a changed value as `*** (changed)`, so the diff still tells which keys are added, removed or changed.
- Objects the API server would reject fail the command with the same diagnostics as `ct apply`, one per rejected object in a single run.

```
$ ct diff web . -n demo --context kind-ct-test
change v1 Secret demo/creds
--- live
+++ ct
@@ -1,6 +1,6 @@
 apiVersion: v1
 data:
-  password: '***'
+  password: '*** (changed)'
 kind: Secret
 metadata:
   labels:
create v1 ConfigMap demo/cfg
--- live
+++ ct
@@ -0,0 +1,10 @@
+apiVersion: v1
+data:
+  mode: prod
+kind: ConfigMap
+metadata:
+  labels:
+    app.kubernetes.io/managed-by: ct
+    ct.cloudticon.com/instance: web
+  name: cfg
+  namespace: demo
change apps/v1 Deployment demo/web
--- live
+++ ct
@@ -10,7 +10,7 @@
   namespace: demo
 spec:
   progressDeadlineSeconds: 600
-  replicas: 1
+  replicas: 3
   revisionHistoryLimit: 10
   selector:
     matchLabels:
prune  v1 Service demo/old
Plan: 1 to create, 2 to change, 1 to prune, 0 unchanged.
```

## Development mode — `ct dev`

Run live development workflows directly on cluster workloads from `dev.ct` (DevSpace-inspired):

```bash
ct dev                        # from the current directory
ct dev --env-file .env.dev    # custom env file ("" to skip)
ct dev --context staging
ct dev --delete               # remove the dev release and exit
```

`ct dev` executes `dev.ct`, applies the resources, then starts port forwarding, log streaming, file sync and a terminal for each dev target.

## IDE support — `ct types`

```bash
ct types .                 # values.d.ts + globals.d.ts, path printed to stdout
ct types . --output ./types
ct types . --dev           # dev.d.ts for dev.ct
```

`values.d.ts` holds a `CtValues` interface inferred from your values file; `globals.d.ts` declares `Values` and `Release`. The command also caches URL imports so the editor resolves them offline.

## CLI reference

```
Global flags:
      --error-format string   how to print errors: text, or json for tools and AI agents (default "text")

ct init [dir] [flags]
  -d, --dir string            project directory (default ".")
      --force                 overwrite an existing main.ct and values.json (AGENTS.md is never overwritten)

ct template <name> <dir|repo> [flags]
  -n, --namespace string      default namespace for resources
  -o, --output string         output format: yaml or json (default "yaml")
  -f, --values stringArray    overrides layered on the project values file; repeat to deep-merge left to right
      --set stringArray       override a value (typed)
      --set-string stringArray  override a value as a string
      --validate              check objects against the Kubernetes API (default true)
      --no-cache              re-download the source and imported packages

ct apply <name> <dir|repo> [flags]
  (template flags, with -o defaulting to no output, plus)
      --context string        kubeconfig context to use (required when stdin is not a terminal)
      --create-namespace      create the namespace if it does not exist
      --dry-run               check every object with the API server and print what would change; write nothing

ct diff <name> <dir|repo> [flags]
  (apply flags, except --dry-run, with)
  -o, --output string         text or json (default text)

ct delete <name> [flags]
  -n, --namespace string      namespace holding the release inventory
      --context string        kubeconfig context to use (required when stdin is not a terminal)

ct explain <Kind>[.<field>...] [flags]
      --api-version string    apiVersion of the kind when it is served under several
  -o, --output string         text or json (default text)

ct list [flags]
  -n, --namespace string      namespace to search
  -A, --all-namespaces        list releases across all namespaces
      --context string        kubeconfig context to use
  -o, --output string         table, json or yaml (default table)

ct dev [flags]
      --env-file string       .env file to load ("" to skip) (default ".env")
      --context string        kubeconfig context (required when stdin is not a terminal)
      --name string           release name for labels/inventory (default "dev")
      --create-namespace      create the namespace before apply (default true)
      --delete                delete the dev release and exit

ct types [dir] [flags]
      --output string         output directory (default ~/.ct/types/<project-hash>)
      --operator              include operator globals (getStatus, setStatus, fetch, log, Env)
      --dev                   generate dev.d.ts for dev.ct
```

## How it works

1. **esbuild** bundles `main.ct` and its imports into one script with an inline source map. URL imports resolve from `~/.ct/cache/`; `async`/`await` is rejected by the parser with a precise location.
2. **Goja**, a JavaScript engine written in Go, runs the bundle with `Values` and `Release` as globals. Each factory call pushes an object to `__ct_resources`; `ct` records the call site of each push. Runaway scripts stop after a minute with a stack trace.
3. **Normalize**: objects are copied out of the JS runtime, `null`/`undefined` fields are dropped the way `JSON.stringify` drops `undefined` (empty objects and arrays like `emptyDir: {}` or `ingress: [{}]` are kept), and namespaced objects without a namespace get `-n`.
4. **Validate** against the Kubernetes API types and admission rules; reject duplicates.
5. **Order** objects like Helm does, add the release labels, and print YAML/JSON or server-side-apply them.

No Node.js runtime is needed. Everything runs inside the Go binary.

## Using ct as a Go library

```go
import (
    "github.com/cloudticon/ct/pkg/engine"
    "github.com/cloudticon/ct/pkg/validate"
)

tr := engine.NewTranspiler("/path/to/project")
js, err := tr.Bundle("/path/to/project/main.ct")

values, err := engine.LoadValues(engine.ValuesOpts{
    Files: []string{"values.yaml", "values-prod.yaml"},
    Set:   []string{"replicas=5"},
})

result, err := engine.Render(engine.ExecuteOpts{
    JSCode:    js,
    Values:    values,
    Namespace: "production",
    SourceDir: "/path/to/project",
})

problems := validate.Resources(result.Resources, result.Origin) // diag.List
```

Errors from `Bundle`, `Render` and `validate.Resources` are `diag.List` values: structured diagnostics with code, position, resource, field path and hint.

## Source layout

```
cmd/ct/              CLI entry point
internal/
  cli/               commands
  dev/               ct dev runner
  output/            YAML/JSON serializer
  scaffold/          ct init
pkg/
  engine/            transpiler (esbuild) + runtime (Goja) + values
  diag/              structured diagnostics
  manifest/          pure object transforms: cleaning, namespaces, ordering, duplicates
  validate/          Kubernetes schema and admission checks
  explain/           offline field reference for built-in kinds (ct explain)
  k8s/               cluster port: apply, dry-run plan, inventory, prune, exec, port-forward, logs
  sync/              ct dev file sync
  cache/             URL import cache
  packages/          import parsing and dependency sync
```

## Development

```bash
go test -race ./...
go vet ./...
go build -ldflags="-s -w" -o ct ./cmd/ct
```

## Feedback and contributing

`ct` is young, and real-world charts are the best test it can get. If something doesn't render, validate or apply the way Helm would, [open an issue](https://github.com/cloudticon/ct/issues) with the smallest `main.ct` that shows it. Ideas and questions go to [Discussions](https://github.com/cloudticon/ct/discussions).

If `ct` saved you from one more `nindent`, a ⭐ helps other people find it.

## License

[Apache 2.0](LICENSE)
