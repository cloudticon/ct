# RFC: ct as an AI-friendly replacement for Helm

- **Status:** draft for discussion
- **Scope:** ct CLI, `cloudticon/k8s`, `cloudticon/k8s-factories`, docs and skills

## Summary

ct should be the tool you reach for instead of Helm, and the one an AI coding agent can drive without a human translating cluster errors back into code. Two goals follow:

1. **Feedback in one loop.** A mistake in a `.ct` file must fail at render time, with the file, line, object and field that caused it, in a form both people and programs can act on.
2. **Helm parity where it matters.** Values layering, install order, diff, wait, rollback, hooks, pinned dependencies, and a way to consume existing charts. Without these, teams can't drop Helm.

ct already has the right base for both: a real language, typed primitives, a single binary, and inventory-based apply with pruning. This RFC makes the two goals explicit, records what landed with it, and orders the rest.

## Why

AI agents now write a large share of deployment code, and they work in tight loops: edit, run, read the error, fix. Helm makes each turn of that loop long and noisy:

- It templates YAML as text, so whitespace (`nindent`) and quoting bugs are common, and errors refer to rendered YAML lines (`error converting YAML to JSON: line 37`) rather than the template.
- Most mistakes surface only at apply time, on the cluster, far from their cause. Some never surface: unknown fields are dropped with a warning nobody reads.
- Values have no types unless someone maintains a `values.schema.json`.
- Errors are prose meant for humans, with no stable codes or positions.

Agents are good at fixing a precise error and bad at guessing where an imprecise one came from. The same is true of people. A tool that gives precise errors helps both.

## Principles

1. **Fail at render time, not on the cluster.** Anything the API server would reject should be caught by `ct template`.
2. **Every error points at code.** File, line and column in the user's `.ct` sources; the object and field path; a stable code; a hint. Available as JSON.
3. **Deterministic, boring output.** Same input, same bytes: Helm's install order, 2-space YAML, no hidden defaults.
4. **One obvious way.** Explicit values layering, imports by URL and version, no npm, no async.
5. **Safe releases by default.** Inventory and prune today; diff, wait and rollback next.
6. **Projects explain themselves.** Agents get the commands and rules from the project (`AGENTS.md`), not from guesswork.

## What landed with this RFC

The branch that introduces this document fixes correctness bugs and lays the base for principles 1–3 and 6:

| Area | Change |
| --- | --- |
| Validation | `pkg/validate`: strict decoding of built-in kinds into client-go types (unknown fields, wrong types, wrong or removed apiVersions) plus the admission rules the API server enforces. On by default; `--validate=false` skips it. |
| Diagnostics | `pkg/diag`: structured errors with code, position, resource, field path and hint; `--error-format json`. Inline source maps make Goja stack traces point at `.ct` lines; each object remembers the call chain that registered it. |
| Rendering | Only `null`/`undefined` fields are dropped, like `JSON.stringify` (before, empty objects and arrays vanished too, turning a NetworkPolicy `ingress: [{}]` from allow-all into deny-all); objects are copied out of the JS runtime so shared objects can't leak namespaces; cluster-scoped kinds get no namespace; duplicate objects are an error; Helm install order (by API group and kind) for `template` and `apply`; apply waits for CRDs created in the same run; bounded call stack and a 1-minute render timeout. |
| Values | Repeatable `-f` with Helm-style deep merge, `--set` without a values file, number typing that keeps `1.10` a string, `--set-string`, escaped dots in keys. |
| Bundling | Parser-based async detection (no false positives on comments, strings or names like `async-worker`), extensionless `.ct` imports, import errors with hints. |
| Releases | Prune compares objects by API group, kind, effective namespace and name (it used to delete the object it had just applied after an `apiVersion` or `-n` change); the inventory keeps objects created by a failed apply; delete runs in reverse install order; `ct list` counts only inventories. |
| Cache | Atomic installs, package URLs and import paths can't escape the cache, `--no-cache` refreshes imported packages, no git password prompts, custom git hosts. |
| Project | Module path `github.com/cloudticon/ct` (so `go install` works); `ct init` never overwrites files, scaffolds a valid app and writes `AGENTS.md`. |

In `cloudticon/k8s` (separate branch): core kinds use `apiVersion: v1` instead of `core/v1`; ConfigMap, Secret, ServiceAccount, RBAC and StorageClass fields render at the top level instead of under `spec`; cluster-scoped kinds carry the scope marker. ct's new validation would have caught all three.

## Gap analysis: what replacing Helm takes

| Helm capability | ct today | Plan |
| --- | --- | --- |
| Templating | TypeScript | done |
| Values files, `--set`, `--set-string` | done | `--set-json`; typed values (below) |
| `values.schema.json` | — | **Typed values**: validate the merged values against a type declared in the project, report the values file line |
| Install order | done | — |
| Lint / kubeconform | built-in kinds | **CRD schemas**: validate custom resources against the schemas the factory library already carries (`openAPISchema`) and, with cluster access, the CRDs on the cluster |
| `helm diff` (plugin) | — | **`ct diff`** via server-side dry-run apply against live objects |
| `--dry-run=server` | — | `ct apply --dry-run=server` |
| `--wait`, `--atomic` | — | **`ct apply --wait`**: rollout status, and on failure the events and last log lines of failing pods as diagnostics |
| History, `rollback` | latest inventory only | Keep the last N rendered releases (compressed) next to the inventory; `ct history`, `ct rollback` |
| Hooks (pre/post install Jobs) | — | `hook()` helper: Jobs run and awaited before or after apply (migrations) |
| `Chart.lock`, dependency pinning | `@branch`/`@tag`, no lock | **`ct.lock`** with commit SHAs, `ct update`; SHA versions in URLs |
| Chart repos / OCI | git repos | git is the registry for now |
| Consuming third-party charts | — | **`helmChart({ repo, chart, version, values })`** renders a chart into ct objects, so one ct release can include upstream software (ingress-nginx, cert-manager, ...) |
| `helm test`, helm-unittest | — | **`ct test`** for `*.test.ct` files with `describe`/`it`/`assertResource` (the ct skills already document this API) |
| `--show-only` | — | `ct template -k Kind/name` filter |

## Roadmap

Each phase is usable on its own. The bold rows in the table above are the priority items.

### Phase 1: trustworthy render (in progress)

- Done: built-in validation, diagnostics, source maps, JSON errors.
- **CRD validation** from library schemas. Acceptance: a typo in a cert-manager `Certificate` spec fails `ct template` with a field path.
- **Typed values**. Acceptance: a wrong type in `values-prod.yaml` fails with the values file and key, before rendering.
- **`ct check`**: render and validate every environment (each `values-*.yaml`) in one command; exit code and JSON summary.
- **`ct explain <kind>[.field]`**: field docs and types offline, for agents that can't reach a cluster.

### Phase 2: safe releases

- `ct diff`, `ct apply --dry-run=server`.
- `ct apply --wait` with failure diagnostics (events, container states, last log lines).
- History and `ct rollback`.
- Hooks for pre/post-apply Jobs.

### Phase 3: reproducibility and ecosystem

- `ct.lock` and `ct update`; SHA-pinned imports.
- `helmChart()` for third-party charts.
- `ct test`.

### Phase 4: agent integration

- `ct mcp`: an MCP server exposing render, validate, explain and diff as tools.
- Ship the ct skills (`cloudticon/skills`) with the repo and keep them tested against the CLI.
- `llms.txt` on cloudticon.com generated from the docs.

## Ecosystem work

- **`cloudticon/k8s`**: int-or-string fields (`targetPort`, `maxSurge`, PDB `minAvailable`) and quantities are typed as `string`, so valid numbers need casts. The built-ins come from Kubernetes 1.26 schemas, so newer fields aren't typed. Generated `.default()`s make required fields optional and add `undefined` to array elements. `z` builders aren't marked side-effect free, so importing one core kind keeps all of them. The generator should emit the committed format (local imports, `/* @__PURE__ */`).
- **Docs** (cloudticon.com): the Helm comparison promises compile-time Zod validation, which ct doesn't do; validation now happens at render time and the page should say so. Examples use shorthands the library doesn't have (`deployment({ image })`, Ingress `service`/`port` on paths). The pattern docs should cover `topLevel`.
- **Skills**: `ct` says `Release.Name` doesn't exist (it's `Release.name`) and that `--values` doesn't merge (repeated `-f` now does); `ct-create-factories` documents `ct test`, which the CLI doesn't have yet.

## Non-goals

- Cloud resources or general infrastructure as code (Pulumi and Terraform already do that).
- A chart repository or OCI registry in the near term: a git repository with tags is the package.
- Compatibility with Go templates.

## Open questions

1. **Kubernetes version skew.** Validation uses the Kubernetes types ct was built with. Should ct also accept `--kube-version`, or read the server version during `ct apply`, to avoid false "unknown field" errors on newer clusters?
2. **Helm charts.** Embed the Helm SDK (bigger binary, no external tool) or call the `helm` binary when present?
3. **Release history storage.** One Secret per revision (Helm's approach, 1 MiB limit each) or a compressed ring in the inventory ConfigMap?
4. **Lockfile format.** JSON `ct.lock` next to `main.ct`, keyed by import URL, holding the commit SHA and a content hash?
