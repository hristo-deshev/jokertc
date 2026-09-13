# Linting

This project runs a strict `golangci-lint` configuration (`.golangci.yml`,
v2 format) adapted from Kubernetes' linting setup. Local gate:

```sh
make lint        # gofmt, gofumpt, go vet, staticcheck, golangci-lint
```

CI runs the same gate in `.github/workflows/checks.yml` (job `lint`) on every
push to `main` and every pull request. Merges to `main` must require that job
to pass (configure branch protection in the repository settings).

## Source of the configuration

Adapted from `kubernetes/kubernetes` `hack/golangci-hints.yaml` (the strictest
tier in the Kubernetes repository, generated from `golangci.yaml.in`).

## Deviations from the Kubernetes config

The Kubernetes config targets the Kubernetes monorepo and a custom-built
`golangci-lint` binary. The following were changed, with rationale:

| Change | Rationale |
| :--- | :--- |
| Removed the custom `logcheck` linter | It is a private module compiled into Kubernetes' golangci-lint binary; not available in a stock installation. The template already uses `log/slog` directly. |
| Removed the custom `sorted` linter | Kubernetes-only feature-gate sorting; no feature gates in this service. |
| Removed the custom `kubeapilinter` linter | Lints Kubernetes API type definitions; this service defines none. |
| Removed `ginkgolinter` | No Ginkgo/Gomega tests in this project. |
| Removed Kubernetes-specific `forbidigo` patterns | Patterns banned Kubernetes-internal APIs (`managedfields.ExtractInto`, `featuregate.Add`, `AnnotatedEventf`, ginkgo helpers). Kept the generic `md5` ban. |
| Removed `depguard` rule banning `k8s.io/utils/pointer` | Package does not exist in this module. Kept the `go-cmp`/`html/template` test-only rule as a generic hygiene check. |
| Removed all Kubernetes-specific `exclusions.rules` entries (conversion files, `Convert_*`/`SetDefaults_*` naming, `cmd/kubeadm`, `staging/src/k8s.io/...` paths, kube-api-linter exceptions) | Paths and patterns do not exist here. |
| Added `bodyclose`, `contextcheck`, `noctx`, `sqlclosecheck` | The service serves HTTP; these catch leaked response bodies and lost contexts. `sqlclosecheck` guards against future SQL resource leaks and stays enabled even though the service currently has no database layer. |
| Added a scoped exclusion for exported-comment findings | The template code predates the strict revive `exported` rule. Existing findings are grandfathered by text match; new code must document exported symbols. Remove this exclusion as files get documented. |

## Fixing findings

Do not disable linters wholesale. Prefer fixing the code; use scoped
`exclusions.rules` (path or text) only with a documented reason appended to the
table above. `run.modules-download-mode: readonly` is enabled, matching the
upstream strict setup.