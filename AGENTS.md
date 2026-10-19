# AGENTS.md — working on cloudticon/ct

`ct` is a Go CLI that renders Kubernetes packages written in TypeScript (`main.ct`), validates them and applies them. This file is for coding agents contributing to this repository. (Instructions for agents writing `main.ct` live in the `AGENTS.md` that `ct init` generates.)

## Verify every change

Run all of these from the repository root before you commit. Each must exit 0.

```bash
test -z "$(gofmt -l .)" || (gofmt -l . && exit 1)   # formatting; fix with: gofmt -w .
go vet ./...
go test -race ./...
go build -ldflags="-s -w" -o ct ./cmd/ct             # then delete ./ct, it is not tracked
```

These are the same checks as `.github/workflows/build.yml`. Unit tests use a fake cluster (`pkg/k8s/k8stest`) and need no Kubernetes access.

## Package map

```
cmd/ct/              CLI entry point
internal/cli/        cobra commands: init, template, check, apply, diff, delete, explain, list, dev, types
internal/dev/        `ct dev` runner (dev.ct config, patching, port-forward/log/sync wiring)
internal/output/     YAML/JSON serializer
internal/scaffold/   `ct init` (main.ct, values.json, AGENTS.md templates)
pkg/engine/          esbuild transpiler + Goja runtime + values loading
pkg/diag/            structured diagnostics (stable codes, positions, hints)
pkg/manifest/        pure object transforms: cleaning, namespaces, ordering, duplicates
pkg/validate/        Kubernetes schema and admission checks
pkg/explain/         offline field reference for built-in kinds (`ct explain`)
pkg/k8s/             cluster port: apply, dry-run plan, inventory, prune, exec, port-forward, logs; k8stest/ holds the fake
pkg/sync/            `ct dev` file sync
pkg/cache/           URL import cache
pkg/packages/        import parsing and dependency sync
docs/rfc/            direction documents
```

## Rules

- **Diagnostic codes are a contract.** The constants in `pkg/diag/diag.go` ("Stable diagnostic codes") are matched by tools and agents. Never rename or repurpose one by accident; change a code only on purpose, update the tests that pin it, and say so in the commit message and the release notes.
- **Output is deterministic.** Rendering the same input must give byte-identical output: no map-iteration order, timestamps or random values in rendered objects, diagnostics or `--error-format json`. Sort before you print.
- **Never test against the default kubeconfig context.** On the owner's machine the default context is a real cluster. Do not run `ct apply`, `ct delete`, `ct dev` or any code that builds a live client without an explicit, disposable target. Cluster tests run only on a local kind cluster with its own kubeconfig file:

  ```bash
  export KUBECONFIG="$(mktemp -d)/kind-ct.yaml"   # a separate file, never ~/.kube/config
  kind create cluster --name ct-test --kubeconfig "$KUBECONFIG"
  ct apply my-app . --context kind-ct-test        # only with that KUBECONFIG exported
  kind delete cluster --name ct-test --kubeconfig "$KUBECONFIG"
  ```

  Keep `KUBECONFIG` pointing at that file for the whole session and always pass `--context kind-ct-test`. If `KUBECONFIG` is unset or `kubectl config current-context` is not `kind-ct-test`, stop.
- **Tags follow `v0.1.0-beta.N`.** Release tags are public: create or push one only when the repository owner asks. Do not invent other tag shapes.
- **Every CLI change updates `README.md`** (CLI reference, examples, Source layout when packages move). The README is the user-facing contract.
- Keep the binary static and Node-free: no new runtime dependency on external tools such as `node`, `tsc` or `kubectl`.
- Write tests with the change; prefer the fake cluster and table tests as the neighbouring `_test.go` files do.
- Commit messages are short imperative sentences; do not commit build outputs (`ct`, `ct-*`).
