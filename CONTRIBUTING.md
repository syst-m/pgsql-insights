# Contributing

Everything a contributor needs that the README does not cover: the loop you run
code through, where the code lives, and how a release happens. The requirements
themselves are [`requirements.md`](requirements.md); the project state and the
decisions behind it are [`IMPLEMENTATION.md`](IMPLEMENTATION.md).

## The dev loop

CI is the source of truth and the Makefile is its local mirror (R11.11) — every
CI job has a `make` equivalent, so a red run is reproducible without guessing.
The three CI steps worth running by hand are commands rather than targets — `bash
scripts/check-parity-matrix.sh` (the requirement-ID gate), `test -z "$(gofmt -l
.)"`, and `kubectl kustomize deploy/kustomize` — and `make lint` covers none of
them.

```sh
make build          # the pgsi binary, ldflags-injected version + commit (R11.3)
make test           # go test ./... -count=1 — the command CI's test job runs verbatim
make cover          # coverage; the awk parse is R11.11's, not a go tool one-liner
make lint           # vet + golangci-lint + openapi-check + cardinality-check; CI's lint
                    # job adds gofmt, the parity matrix and the license manifest (R15.3)
make tidy-check     # go.mod/go.sum are in sync with the sources
make openapi        # regenerate api/openapi.yaml from the handlers (R8.1b). The document
                    # is generated output since `9794560`, so this belongs in the same
                    # commit as the handler or annotation that changed it — the same rule
                    # web/dist follows with the TS that built it, and for the same reason:
                    # a committed artifact built from uncommitted source is a claim nobody
                    # can rebuild

make web            # build web/ -> web/dist, which is go:embed'd (R10.1)
make typecheck-web  # tsc -p tsconfig.json --noEmit (CI runs it via `npm run build` in the web job)
make test-web       # web + typecheck, then Vitest (web/src/**/*.test.tsx) and Playwright (web/e2e)
```

Chart work has its own four:

```sh
make helm-lint      # helm lint ./chart
make render         # regenerate deploy/plain-yaml/ for all three profiles (R11.13)
make render-check   # fails if the committed manifests are stale (R16.4's golden test)
make rules-check    # promtool check rules on the rendered PrometheusRule (R9.11)
```

**`render-check` compares the index against the worktree** (`git diff --exit-code
deploy/plain-yaml` after a render), so it fails while your chart edits are
unstaged even when the rendered output is correct. Stage them first.

### Running it locally

```sh
./bin/pgsi serve --auth=none     # :8080 — API, /metrics and the SPA, one port
```

`--auth=none` is required, not optional: R8.8's default is `token` and the
binary fails closed rather than accepting an empty bearer, so a bare
`./bin/pgsi serve` exits 2 (IMPLEMENTATION.md §5-D14). The chart passes the flag
for you; running the binary by hand does not.

Against a cluster, the loop is `make deploy` then `make verify` — the second
port-forwards `svc/pgsi` and curls `/healthz`, `/api/v1/version` and `/metrics`
through the Service, which is R11.14's acceptance check and the same one
`helm test pgsi` runs from the chart's own test hook.

Against kind — R14.5's named environment, and R13.6's P5 path:

```sh
kind create cluster --config deploy/kind.yaml         # the name (pgsi-lab) is in the file
kubectl apply --server-side -f <cnpg-1.30.0.yaml>     # CRDs — see the note below
make deploy DEPLOY_ARGS="-f chart/values-lab.yaml"    # helm merges default + lab
make verify
```

`make deploy` always passes `-f chart/values-default.yaml`, so the extra `-f`
merges the lab delta on top of it rather than replacing it, which is how R11.12
wants the profiles read.

The CloudNativePG CRDs are a prerequisite, not a nice-to-have, and the failure
mode is louder than "the fleet stays empty". A bare kind cluster has no
CloudNativePG CRDs, and the chart's Role asks for `clusters` and `poolers` in the
`cnpg.io` group; discovery's first pass blocks in `cache.WaitForCacheSync` until
every informer — the CNPG ones included — has synced
([`internal/discovery/discovery.go:528`](internal/discovery/discovery.go)), and
`/readyz` answers 503 until that sync sets `everSynced` (`:636`, R8.7's "true
once discovery has produced at least one inventory sync"). So `helm install` and
`/healthz` succeed on a CRD-less cluster, but the pod never goes Ready, and
`make deploy`'s `kubectl rollout status deploy/pgsi --timeout=180s` exits 2 on
the way. Observed on `pgsi-lab`: helm `STATUS: deployed`, pod `0/1 Running` for
six minutes, `/healthz` 200 + `/readyz` 503; the moment the CRDs were applied the
sync completed and the pod went `1/1`. The manifest for the pinned CNPG version is
`https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v1.30.0/cnpg-1.30.0.yaml`
— the short `…/download/v1.30.0/cnpg.yaml` form 404s for that tag — and it needs
`--server-side`, because the two CRDs' `last-applied-configuration` exceeds the
262144-byte client-side limit that plain `kubectl apply` hits.

That path is validated against the reference environment (k3s v1.37.2+k3s1, CNPG
1.30.0, four clusters — [`docs/research/live-probe.md`](docs/research/live-probe.md)),
and now against kind too: `deploy/kind.yaml` is tracked, and on kind v0.33.0
(kindest/node v1.37.0, Docker 29.9.2, macOS arm64, 2026-11-25) the loop above ran
end to end — create rc=0, `make deploy` deployed the lab profile, `make verify`
200 on `/healthz`, `/readyz`, `/api/v1/version`, `/api/v1/clusters` and
`/metrics`, `crictl stats` 9.683 MB against R13.6's 100 MB cap. minikube is the
other half of R13.6's sentence and is still unvalidated: there is no minikube
config in the repo and no run recorded.

## Code layout

| Path | What it is |
|---|---|
| `Makefile` | every target above, and CI's local mirror (R11.11) |
| `Dockerfile` | multi-stage build → `distroless/static:nonroot`, pinned base digest (R11.2, R11.5) |
| `.github/workflows/ci.yml` | the six CI jobs — build, lint, test, web, helm, image — that the Makefile mirrors (R11.11) |
| `cmd/pgsi/` | single binary, subcommand dispatch (`serve \| collect \| api \| version`, R3.1) |
| `internal/api/` | HTTP handlers: `/healthz`, `/readyz`, `/api/v1/{clusters,version}`, `/api/v1/clusters/{ns}/{name}`, `/api/v1/insights/queries/series`, the PromQL pair, `/metrics`, `/` (SPA), auth (R8.8), CORS (R8.9) — plus `openapi_gen.go`, the go/ast generator that writes `api/openapi.yaml` (R8.1b) |
| `internal/api/dto/` | the response contracts — R8.5's `ErrorBody`/`NewError`, R1.5's `ClusterView`, R3.2's `QueryView` |
| `internal/collector/` | the collector framework and the per-subsystem collectors (R2.1–R2.9), the scheduler budget (R2.8) and `pgsi_up` (R9.12) |
| `internal/kube/` | the per-port transport table (R1.10) |
| `internal/discovery/` | cluster, instance and pooler discovery (R1.1–R1.9) |
| `internal/version/` | ldflags-injected `Version`/`Commit` (R11.3) |
| `internal/logx/` | R13.7's structured JSON logger and the `--log-field-*` flag shape (the CNPG/machinery log-flag compatibility) |
| `web/` | the React SPA; `dist/` is committed and `go:embed`'d (R10.1) |
| `web/src/routes.ts` | the route table the nav renders from, so the endpoint list and the page list cannot drift apart (R10.3) |
| `chart/` | the Helm chart; `values.yaml` **is** the shipped default (R11.12), the other two profiles are documented deltas. `values-default.yaml` — the file R11.12 names and the Makefile renders — is a symlink to it, so `helm install` with no `-f` and `-f values-default.yaml` cannot disagree |
| `deploy/plain-yaml/` | generated manifests, all three profiles (R11.13) |
| `deploy/kustomize/` | kustomization pointing at the generated default profile |
| `testdata/api/` | the API contract fixtures the Go and TS tests share (R16.8) |
| `docs/` | architecture, runbooks, research, reviews, licenses, ENVIRONMENT, agents, compatibility-matrix |
| `scripts/` | `check-openapi.py`, `check-parity-matrix.py` (+ the `.sh` wrapper CI's lint job runs), `cardinality-check.py`, `build-push-dev.sh`, `image-size.py`, `push-oci.py`, `gen-licenses.py`, `gen-pgmatrix.py`, and `validate_palette.js` — the chart palette gate the A4 charter names, vendored from the `dataviz` skill bundle and recorded with its sha256 in IMPLEMENTATION.md §4 |

`testdata/api/` is the one path with a hard rule attached. The Go and TypeScript
sides read the **same** fixture files, which is what makes a contract change
visible on both sides in one commit — a TS type written from spec prose while the
Go handler says something else is the failure class R16.8 exists for, and it has
already happened once (`raw.clusters ?? []` vs a non-nullable Go field).

## Committing

`git commit -m '…' -- <paths>`. Name the paths to `git commit`, not just to
`git add`: a bare `git commit` commits the whole index, and on a branch where
several people have work in flight that is how a one-line fix lands carrying
five thousand lines of someone else's staged code. The full rule, and the
incident that produced it, are in [`docs/agents.md` §5.1](docs/agents.md).

Commit messages say what changed and why, not that something changed — the
subject line is read as a claim about the code, so it has to survive someone
opening the diff.

## Releasing

R15.2 fixes the scheme; nothing about it is improvised at release time.

- **Image tags**: `ghcr.io/<org>/pgsql-insights:vX.Y.Z` plus `:latest`. The org
  is provisional (open question Q6) — see the README. `make image` builds both
  platforms with buildx (`linux/amd64` + `linux/arm64`, no QEMU) and pushes; on
  `main`, CI logs in to ghcr.io with `GITHUB_TOKEN` first. From a machine with no
  ghcr credential, `make push-local` runs `scripts/build-push-dev.sh`, which
  builds each platform to an OCI layout — no credentials needed — and publishes
  with `scripts/push-oci.py` over the registry v2 API.
- **Chart version vs appVersion**: `chart/Chart.yaml`'s `version` is bumped by
  the release pipeline, never by hand during development; `appVersion` tracks the
  binary version that R11.3 also injects via ldflags and stamps as OCI labels.
- **GitHub Release per tag, with the SBOM attached.** Not yet automated — the
  release job is Phase 9's remaining work.
- **Dependency manifest**: `make licenses` regenerates
  `docs/licenses/go-dependencies.txt` (R15.3); the `go-licenses` full-text audit
  and the CI regeneration R15.3 also asks for are Phase 9's remaining work.
- **Changelog**: [`CHANGELOG.md`](CHANGELOG.md) is maintained from Phase 1 onward,
  not retro-fitted at release (R14.6) — the file itself explains where that rule
  started late. Add your entry to `[Unreleased]` in the same commit as the change,
  not in a release-time sweep.
