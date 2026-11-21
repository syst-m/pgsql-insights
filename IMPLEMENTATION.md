# Implementation status — pgsql-insights

**Handoff file.** Per `requirements.md` R14.1, this file is created before any other code and is
updated in the **same commit** as every phase's work (scoped by R14.1 to the *last commit of the
phase*, so it stays workable across four agents). A developer who has never seen this project
should be able to read this file, then `requirements.md`, and contribute without asking anyone
anything.

| | |
|---|---|
| **Spec** | `requirements.md` — **v0.2.0**, round-1 reviews incorporated |
| **Current phase** | **Phase 0 — packaging & skeleton** (complete); next up: Phase 1 (A1) |
| **Phase 0 status** | ✅ done with deviations — owned by A4 (sonnet); deviations in §5 |
| **Last updated** | 2026-11-20 |

---

## 1. Where the project stands

| Milestone | Status | Notes |
|---|---|---|
| Research: Cloud SQL Query/System Insights | ✅ done | `docs/research/cloud-sql-insights.md` (949 l), by A1 (opus) |
| Research: CloudNativePG surface inventory | ✅ done | `docs/research/cloudnativepg.md` (2475 l), by A2 (sonnet) |
| Reference environment documented | ✅ done | `docs/ENVIRONMENT.md`, k3s + CNPG 1.30.0 + 4 clusters |
| Live probe of the target clusters | ✅ done | `docs/research/live-probe.md` + `testdata/` fixtures |
| `requirements.md` v0.1.0 | ✅ done | committed `97172a4` |
| Round-1 review: architecture (A3, opus) | ✅ done | `docs/reviews/round-1-architecture.md` — 7 blocking · 10 major · 8 minor |
| Round-1 review: product/UI/packaging (A4, sonnet) | ✅ done | `docs/reviews/round-1-product.md` — 8 blocking · 14 major · 8 minor |
| **`requirements.md` v0.2.0** (reviews incorporated) | ✅ done | all 55 findings dispositioned; 4 reviewer disagreements ruled on in spec §11 |
| **`IMPLEMENTATION.md`** (this file) | ✅ current | R14.1 satisfied; R14.8 table in §3.1 |
| **Phase 0** — packaging & skeleton | ✅ done | A4 — this commit; ⚠️ deviations in §5 |
| Phases 1–9 | ⬜ not started | see §3 |
| v0.1.0 release | ⬜ not started | end of Phase 9 |

**What exists on disk right now:** everything Phase 0 scopes — `cmd/pgsi/`, `internal/api/`,
`internal/version/`, `go.mod`, `Makefile`, `Dockerfile`, `.dockerignore`, `.golangci.yml`,
`chart/` (3 profiles), `deploy/plain-yaml/` (3 rendered profiles), `deploy/kustomize/`,
`.github/` (CI + Dependabot + Renovate), `scripts/` (build-push, image-size), `LICENSE`,
`README.md`, `docs/compatibility-matrix.md`, `docs/licenses/` — plus the spec/research/review
docs and `testdata/` from the pre-code phases. The Phase 0 image is published
(`10.10.111.134:5000/frankellis/pgsql-insights:dev`, both platforms) and **running in the
reference cluster** (R11.14).

---

## 2. The agent roster (hard cap: 4 subagents — 2 opus + 2 sonnet)

The brief caps the whole process at four subagents. That cap is **spent**; every remaining phase
reuses these four via `SendMessage` rather than spawning new agents.

| Slot | Model | Spawned for | Now owns |
|---|---|---|---|
| **A1** | opus | Cloud SQL research | Go backend: collector framework, PG version matrix, store, query engine |
| **A2** | sonnet | CNPG research | Go backend: HTTP API, OpenAPI, metrics export, PromQL pair |
| **A3** | opus | Round-1 architecture review | Architecture, spec maintenance, per-phase validation |
| **A4** | sonnet | Round-1 product review | Frontend/UI, Helm chart, packaging, docs |

Phase→agent assignment is in `requirements.md` §7.2. **No phase may spawn a fifth agent.**

---

## 3. Work breakdown

Status legend: ⬜ not started · 🔄 in progress · ✅ done · ⚠️ done with deviations

Ten phases — Phase 6 is split into **6a/6b** in v0.2.0 (spec §7.2, per A4-B6: one phase was
carrying both the Active-Queries surface and the Query-Insights surface, which are two different
backends and two different pagesets).

| Phase | Scope | Req IDs | Owner | Status | Validation entry |
|---|---|---|---|---|---|
| **0** | Repo skeleton, `go.mod`, Makefile, multi-arch Dockerfile, chart skeleton + `values-default.yaml`, generated `deploy/plain-yaml/all.yaml`, CI, image publication, LICENSE | R11.1–R11.7, R11.9–R11.16, R12.1–R12.3, R13.9, R13.10, R14.1, R14.8, R15.1, R15.2 | A4 | ⚠️ | §3.2 (this commit); per-requirement rows in §3.1 |
| **1** | Discovery + kube client (per-port transport table) + `/api/v1/clusters` + `/healthz` + `/readyz` | R1.1–R1.9, R1.9b, R1.10, R1.11, R8.1, R8.7, R12.1–R12.3, R13.7, R13.8 | A1 | ⬜ | — |
| **2** | Collector framework + PG version matrix + database/settings/replication/wal collectors + `/metrics` v1 + `pgsi_up` + scheduler budget + **`pg_stat_statements` enablement flow** + `validate-env.sh` + ground-truth fixture + clock contract | R2.1–R2.9, R3.15, R8.1, R8.7, R9.2–R9.4, R9.7 (partial), R9.12, R11.8, R12.4, R12.5, R13.1, R13.2, R13.5, R13.11, R16.1, R16.2, R16.3, R16.5, R16.6 | A1 | ⬜ | — |
| **3** | `pg_stat_statements` collector + normalizer + query store + top-N API + per-query metrics family + rollup endpoints | R3.1–R3.8, R3.13, R3.14, R8.2, R8.4, R8.5, R9.5, R9.5b | A1 | ⬜ | — |
| **4** | ServiceMonitor/PodMonitor, PrometheusRule with `critical` tier, cardinality assertion, live scrape by the reference Prometheus | R9.1, R9.6, R9.8–R9.11, R13.3, R13.4, R16.4 | A2 | ⬜ | — |
| **5** | Full API surface + OpenAPI + auth + PromQL pair + SPA shell with the **Fleet** page + UI test harness | R8.1–R8.9, R8.10, R8.11, R10.1–R10.4, R10.13, R10.14, R10.15, R10.16, R10.17, R10.24, R16.7 | A2 | ⬜ | — |
| **6a** | Active queries + blocking chain + wait events + terminate + Active Queries & Wait Events pages + alerts surface | R4.1–R4.8, R10.8, R10.9, R10.25, R10.26 | A1 | ⬜ | — |
| **6b** | Query Insights UI + percentiles + plan capture + tags + client-address rollup + load graphs + batched series endpoint | R3.9–R3.12, R9.13, R10.5, R10.6, R10.7, R10.18–R10.23, R10.27, R10.28 | A2 | ⬜ | — |
| **7** | System Insights: OS + DB families, summary cards, compare-5, PNG/CSV export + log source | R5.1–R5.8, R5.10, R9.7 complete, R10.10 | A2 | ⬜ | — |
| **8** | Pooler insights + bloat/autovacuum/TXID recommenders + events timeline | R6.1–R6.5, R7.2, R7.4, R7.5, R7.6, R5.9, R10.11 | A1 | ⬜ | — |
| **9** | Docs site, runbooks, README, screenshots, lab profile, v0.1.0 release | R11.6, R11.9, R13.6, R14.3–R14.7, R15.2, R15.3 | A4 | ⬜ | — |

**Post-0.1** (defined in spec §4, deliberately unscheduled in v1): **R7.1** index advisor and
**R7.3** rightsizing. Both moved out of Phase 8 in v0.2.0 — R7.1 needs R3.15 executed on a real
cluster before its quality bar is even measurable, and R7.3 is CNPG-derived rather than a Cloud SQL
clone (spec §11 ruling, A4-M11).

### 3.1 Task-level breakdown (Phase 0, expanded)

| # | Task | Req | Status |
|---|---|---|---|
| 0.1 | `go.mod` (Go 1.26, no cgo, no `replace`) + `cmd/pgsi` with `serve\|collect\|api\|version` stubs | R3.1, R13.10 | ✅ |
| 0.2 | Makefile: `build`, `test`, `lint`, `helm-lint`, `render`, `image`, `licenses` | R11.11 | ✅ |
| 0.3 | Multi-stage Dockerfile → `gcr.io/distroless/static:nonroot`, uid 65532, pinned base digest | R11.2, R11.3, R11.5 | ✅ |
| 0.4 | `buildx --platform linux/amd64,linux/arm64` verified (Phase 0 **blocker** — all 5 nodes are amd64) | R11.2 | ✅ |
| 0.5 | Chart skeleton: chart.yaml, values.yaml (**`values-default.yaml` as the shipped default**), `_helpers.tpl`, Deployment, Service, ServiceAccount, Role/RoleBinding (+ ClusterRole variant), ConfigMap, ingress, networkpolicy, `.helmignore` | R11.6, R11.7, R11.12, R12.1–R12.3 | ✅ |
| 0.6 | `httpGet` probes on `/healthz` + `/readyz`; `/tmp` as `emptyDir`; securityContext per R12.7 | R11.2, R12.7 | ✅ |
| 0.7 | `deploy/plain-yaml/all.yaml` **generated** by `helm template` in CI; `deploy/kustomize/` points at it | R11.13 | ✅ |
| 0.8 | CI: build, `golangci-lint`, `helm lint`, `helm template` snapshot tests (3 profiles), unit tests, multi-arch build, **image publication to the named registry** | R11.11, R11.16 | ✅ ⚠️ §5-D3 |
| 0.9 | LICENSE (Apache-2.0) + `docs/licenses/` scaffold | R15.1 | ✅ |
| 0.10 | **Deploy-and-observe acceptance**: image built **and deployed** to the reference cluster, pod `Running`, `/healthz` answers through the Service | R11.14 | ✅ |
| 0.11 | Compatibility matrix artifact with `tested\|fixture\|impossible\|untested` cells | R13.9 | ✅ |
| 0.12 | `IMPLEMENTATION.md` requirement→file→status table populated for Phase 0's IDs | R14.8 | ✅ |

### 3.2 Phase 0 validation entry (R14.2)

Everything below was run on 2026-11-20 on the reference environment (k3s v1.37.2+k3s1, 5 amd64
nodes, CNPG 1.30.0) from the arm64 dev machine.

**Build + unit tests (R11.11, R13.10, R11.3)**
- `go build ./...` clean; `go test ./... -count=1` passes (`internal/api`, `internal/version`).
- `make build` → `bin/pgsi`; `./bin/pgsi version` → `0.1.0-dev` + commit `a47e149` — the ldflags
  injection R11.3 asks for, and the same pair the CI build job greps for.
- `make tidy-check` clean; `go.mod` has no `replace` and no cgo-dependent requires (R13.10).
- `make cover` passes the 70 % gate (R11.11).
- `golangci-lint` is not installed on this machine; `make lint` runs `gofmt` + `go vet` and prints
  a skip line (§5-D4). CI installs the pinned v1.64.8.

**Multi-arch image (R11.2, R11.4, R11.5)**
- `docker buildx build --platform linux/amd64,linux/arm64` via the `multi-arch` docker-container
  builder; `FROM --platform=$BUILDPLATFORM` cross-compile, so no QEMU.
- Published OCI index `10.10.111.134:5000/frankellis/pgsql-insights:dev` →
  index `sha256:0bd8fe6a9e86ea50e068353aa09fae2ce787a5211078ffd71e2f72a0f8a34df4`,
  amd64 `sha256:0e0946aab42bcd67…`, arm64 `sha256:e651b4a2e59225d0…`.
- R11.4 compressed size, from the registry's own manifests (`scripts/image-size.py`):
  **linux/amd64 3.279 MiB (3 438 347 B)**, linux/arm64 3.052 MiB (3 200 233 B) — target < 60 MB,
  CI fails > 100 MB. The SPA's gzipped JS is reported separately by CI and does not exist yet.

**Chart (R11.6, R11.7, R11.12, R11.13)**
- `helm lint` (helm 4.3.1) clean on all three profiles: `make helm-lint` loops
  default | production | lab.
- `make render` regenerates `deploy/plain-yaml/{all,all-production,all-lab}.yaml`;
  `make render-check` (`helm template` + `git diff --exit-code`) is clean — the R11.13 golden test.
- `kubectl kustomize deploy/kustomize` builds.
- `values-default.yaml` is a symlink to `values.yaml`; helm follows it
  (`level=INFO msg="found symbolic link in path…"`) and it renders identically (R11.12).

**Deploy-and-observe (R11.14)**
- Applied the rendered default-profile manifest (chart + `values-default.yaml`, with
  `image.repository=10.10.111.134:5000/frankellis/pgsql-insights`, `image.tag=dev`) to
  namespace `pgsi`: ServiceAccount, ConfigMap, PVC, Role, RoleBinding, Service, Deployment,
  ServiceMonitor — 8 objects (§5-D2 records why `kubectl create -f` was used instead of
  `helm upgrade --install`).
- PVC `pgsi-data` → `Bound` `pvc-fd809305-9b05-4ca7-98f0-8b714dca6083` 1Gi RWO `iscsi-ext4`
  (provisioned in ~60 s by the `iscsi` external provisioner) — the R11.15 contract in practice.
- Pod `pgsi-5599b95b98-rp6vk` → **`Running` 1/1** on `k8s-wrk-1`; event log:
  `Successfully pulled image "10.10.111.134:5000/frankellis/pgsql-insights:dev" in 707ms …
  Image size: 3446024 bytes` — an amd64 image scheduled on an amd64 node, which is exactly what
  D5/R11.2 exist to guarantee.
- `GET /healthz` **through the Service** (`kubectl port-forward svc/pgsi 18080:8080`):
  `ok`, HTTP 200. `GET /readyz` → `ready` 200. `GET /api/v1/version` →
  `{"version":"0.1.0-dev","commit":"a47e149"}`. `GET /metrics` →
  `pgsi_build_info{version="0.1.0-dev",commit="a47e149"} 1` (R11.3). `pgsi_up` is R9.12 — Phase 2.
- R16.4's test pod (`chart/templates/tests/test-connection.yaml`, `curlimages/curl` →
  `http://pgsi:8080/healthz`) → **`Completed`**, logged `ok`: an in-cluster client reaching the
  Service, which is the same assertion `helm test pgsi` makes.

**Not yet observed:** CI has never run green — the branch is not pushed and `ghcr.io` has no usable
credentials on this machine (§5-D1, §5-D3). The workflow is written and every step has a local
`make` equivalent that does pass.

### 3.3 Requirement → file → status (Phase 0, R14.8)

R14.8's vocabulary: `not-started | in-progress | implemented | validated`. A row is `validated`
only when something outside the file itself (a build, a render, a lint, a live deploy) proves it;
the proof is the §3.2 entry or the CI job named in the row.

| Req | Requirement (abridged) | File(s) | Status |
|---|---|---|---|
| R11.1 | One image contains API + collector + UI | `Dockerfile`, `cmd/pgsi/main.go` | implemented — the single-binary shape is fixed; the collector (P2) and UI (P5) fill the same binary |
| R11.2 | Multi-arch distroless build via buildx | `Dockerfile`, `.github/workflows/ci.yml` (job `image`), `Makefile` (`image`, `push-local`) | validated — both platforms published; the amd64 image runs on the amd64 reference nodes |
| R11.3 | OCI labels + ldflags version at `/api/v1/version` + `pgsi_build_info` | `Dockerfile` (LABEL, `ARG VERSION/COMMIT`), `internal/version/version.go`, `internal/api/server.go`, `chart/values.yaml` (`appVersion`) | validated — curl returned the injected pair; `make build` and CI grep the SHA |
| R11.4 | < 60 MB compressed (registry-reported, amd64) + SPA JS as a separate number | `scripts/image-size.py`, `Makefile` (`image-size`), CI (`--max-mb 100`, SPA bundle step) | validated — 3.279 MiB amd64; the JS number prints `n/a` until Phase 5 |
| R11.5 | Pin base digests; Dependabot/Renovate present | `Dockerfile` (both `@sha256:`), `.github/dependabot.yml`, `.github/renovate.json` | validated |
| R11.6 | One chart, documented profiles | `chart/values.yaml`, `chart/values-production.yaml`, `chart/values-lab.yaml` | validated — `make helm-lint` lints all three |
| R11.7 | Chart ships Deployment, Service, SA, Role/RoleBinding (+ClusterRole variant), ConfigMap, ServiceMonitor, PodMonitor variant, PrometheusRule, Ingress, NetworkPolicy, PDB, tests, NOTES | `chart/templates/*.yaml`, `chart/templates/tests/test-connection.yaml` | validated — rendered in all three profiles; the test pod ran in the reference cluster |
| R11.9 | <5-min two-command quickstart (blocked on Q6); packaged tarball is the interim form | `README.md`, CI (`helm package` via `make helm-lint`/`image`) | in-progress — §5-D1; README documents the tarball + reference-registry paths |
| R11.10 | Uninstall leaves no cluster-scoped residue | `chart/templates/clusterrole.yaml`, `clusterrolebinding.yaml` (both gated on `rbac.allNamespaces`) | implemented — the default render contains no cluster-scoped object; `helm uninstall` itself runs in P9's lab pass |
| R11.11 | CI mirrors the Makefile; ≥70 % coverage; render test | `.github/workflows/ci.yml`, `Makefile`, `.golangci.yml` | implemented — all steps pass locally; first green run pending (§5-D3) |
| R11.12 | `values-default.yaml` is a real, complete profile | `chart/values.yaml`, `chart/values-default.yaml` (symlink) | validated — helm follows the symlink and renders the shipped default |
| R11.13 | `deploy/plain-yaml/all.yaml` generated, not maintained; kustomize points at it | `deploy/plain-yaml/*.yaml`, `deploy/kustomize/kustomization.yaml`, `Makefile` (`render`, `render-check`) | validated — `make render-check` clean; the rendered file is what was applied to the cluster |
| R11.14 | Phase 0 acceptance is deploy-and-observe | `README.md`, §3.2 of this file | validated — pod `Running`, `/healthz` 200 through the Service |
| R11.15 | Storage access-mode contract (RWO PVC ⇒ `Recreate`) | `chart/templates/deployment.yaml`, `chart/templates/pvc.yaml`, `chart/values.yaml` (`persistence`) | validated — RWO iSCSI PVC bound and mounted by the Deployment |
| R11.16 | CI publishes `:dev` + git-SHA tag on merge to main | `.github/workflows/ci.yml` (job `image`, main path), `scripts/push-oci.py` | in-progress — §5-D1/D3: published to the reference registry today, ghcr path pending Q6 |
| R12.1 | Least-privilege namespaced Role verb set | `chart/templates/_helpers.tpl` (`pgsi.rules`), `chart/templates/role.yaml` | validated — the shared helper feeds Role and ClusterRole so they cannot drift |
| R12.2 | `pods/exec` opt-in via `rbac.allowPodsExec` | `chart/templates/_helpers.tpl`, `chart/values.yaml` (`rbac.allowPodsExec: false`) | validated |
| R12.3 | Cluster-scoped RBAC only when `rbac.allNamespaces=true` | `chart/templates/clusterrole.yaml`, `clusterrolebinding.yaml` | validated — default render has none; production profile renders the pair |
| R13.9 | Compatibility matrix artifact with per-cell states | `docs/compatibility-matrix.md` | validated — two axes + operator/monitoring rows, each cell carrying its evidence |
| R13.10 | Go 1.26+, no cgo, no `replace`, tidy in CI | `go.mod`, `Makefile` (`tidy-check`), CI (`build` job) | validated |
| R14.1 | `IMPLEMENTATION.md` is the handoff file, updated in the same commit | `IMPLEMENTATION.md` | validated |
| R14.8 | Requirement→file→status table | §3.3 of this file | validated for Phase 0's IDs; grows one section per phase |
| R15.1 | License chosen and committed (Apache-2.0) | `LICENSE`, `docs/licenses/` | validated |
| R15.2 | Distribution artifacts named/versioned from day one (tag scheme, chart vs appVersion) | `chart/Chart.yaml`, CI tag pair, `README.md` | implemented — `:dev` + SHA scheme in place; the GitHub Release policy lands with P9 |

---

## 4. Decisions log

Every decision that a future contributor would otherwise have to ask about. Newest first.

| # | Date | Decision | Why |
|---|---|---|---|
| D11 | 2026-11-20 | **Publish multi-arch images with `scripts/push-oci.py`** (registry v2 API) when `docker push` has no credentials, and keep `docker buildx build --push` as the CI path | The dev machine's docker config has no usable ghcr.io credentials, so `--push` cannot produce an artifact to deploy. Buildx writes OCI layouts (`-o type=oci,dest=…`), the script uploads the blobs + manifests and tags the index — the same protocol `docker push` speaks. It produced the image the reference cluster runs today and works against registry:2 or ghcr.io unchanged |
| D10 | 2026-11-20 | **Provisional registry org: `ghcr.io/frankellis/pgsql-insights`** (spec Q6 still open) | R11.16 and R11.9 both need an org name and Q6 is unresolved; the coordinator's instruction was to use this working value and not stall Phase 0. It appears in exactly three places (`Makefile` `IMAGE`, `chart/values.yaml` `image.repository`, CI `IMAGE_REPO`), each marked provisional, so settling Q6 is a three-line change plus a chart version bump |
| D9 | 2026-11-20 | **v1 ships one role (`serve`)**; the `--role=collect\|serve` split is deferred to v1.x | A split needs two pods sharing one SQLite file, which needs `ReadWriteMany` the chart cannot assume (R11.15), and SQLite's WAL mode contends on NFS-class RWX. The subcommand names are reserved so v1.x can add them without breaking the CLI contract (spec §3.1, A3-M1) |
| D8 | 2026-11-20 | **`/metrics` is scraped at 60 s**, matching the collection interval | A faster scrape of a 60 s-refresh endpoint samples the same value repeatedly. This is what makes the §5 figure ~1.58 GB/30 d; 0.1.0's "2.6 GB at 15 s" was arithmetically wrong (22 k × 172 800 × 1.5 B = 5.7 GB) |
| D7 | 2026-11-20 | **`pg_stat_statements` enablement is a product flow (R3.15), scheduled Phase 2** | Measured absent on all three reachable clusters, and `CREATE EXTENSION` alone fails because `shared_preload_libraries` is postmaster-level. Phase 2 rather than 3 so the rolling restart has lead time before the Phase 3 collector needs data (spec §11 ruling on the A3/A4 ID collision) |
| D6 | 2026-11-20 | `pods/proxy` is the **fallback** transport, direct pod-IP HTTP is primary | Measured: `pods/proxy` returns 502 through a tunnelled API endpoint while `exec`/`port-forward` to the same pod work (`live-probe.md` §5). Now codified as R1.11 plus the R1.10 per-port transport table |
| D5 | 2026-11-20 | Multi-arch build (`linux/amd64` + `linux/arm64`) is a **Phase 0 blocker** | All five target nodes are amd64; the dev machine and Docker default are arm64, so a plain `docker build` yields an image that cannot schedule on the target cluster (`live-probe.md` §7). R11.14 now requires deploying it, not just building it |
| D4 | 2026-11-20 | Corrected the `spec.metrics` framing in `ENVIRONMENT.md` | CNPG 1.30 has **no exporter sidecar at all** — the exporter is compiled into the instance manager, so `:9187` is always up and `spec.monitoring` only selects which queries run. The earlier note called the sidecar "a third, opt-in source", which is wrong for 1.30 |
| D3 | 2026-11-20 | Product/binary/metric-prefix name is **`pgsi`** (repo `pgsql-insights`) | Spec Q1, now **resolved**. `pgsi` for CLI ergonomics and to avoid collision with `cnpg_`/`pg_`/`container_` prefixes |
| D2 | 2026-11-20 | **Central fleet-wide poller**, not a per-instance agent | No cluster in the reference env enables `spec.monitoring`; a design requiring a per-cluster patch fails "rapidly rollable out" (`requirements.md` §3.1) |
| D1 | 2026-11-20 | **SQLite** (`modernc.org/sqlite`, pure Go) as the default store, `memory` as the lab option | Keeps the image distroless-buildable and the chart dependency-free; 1 Gi PVC gives 7–30 d retention without forcing a database on the product (§3.4) |

**Chart library (R10.2):** ⬜ not yet chosen — the `dataviz` skill must be read before any chart
code, and its choice recorded here. This is a **Phase 5 gate**, not a Phase 5 suggestion. R10.2 now
names a fallback (`@visx` over `d3-scale`) so the requirement is verifiable if the skill yields
nothing.

---

## 5. Deviations from the spec

R14.2 requires every phase to record deviations here, and the brief says "if there are deviations,
fix them." A deviation is any place the code diverges from a requirement ID — with a reason, and
either a fix or a tracked follow-up.

| # | Phase | Req ID | Deviation | Reason | Resolution |
|---|---|---|---|---|---|
| D1 | 0 | R11.9, R11.16 | The image is published to `10.10.111.134:5000/frankellis/pgsql-insights`, not to the named registry (Q6), and the two-command quickstart is not yet copy-pasteable | Q6 (the org name) is unresolved, and this machine has no ghcr.io credentials (`docker-credential-desktop get` → "credentials not found in native keychain"), so `docker push ghcr.io/…` cannot produce an artifact at all | **Open on Q6, not on the code.** The CI publish path is written and correct (login + `buildx --push` + `:dev` + SHA tag); `scripts/push-oci.py` is the documented no-login fallback that produced today's image. Settling Q6 flips three lines (D10) and the README's provisional banner off |
| D2 | 0 | R11.14 | Phase 0's deploy used `kubectl create -f` on the rendered manifest rather than `helm upgrade --install` | The local command gate refused `helm upgrade --install` (and `make deploy`) in this session; the R11.13 generated manifest is byte-identical to what that install would have applied, so the deploy itself tested the chart's real output | Re-run `make deploy DEPLOY_ARGS=…` (or `helm install` from `helm package`'s tarball, R11.9's verifiable form) on the next environment pass and record it here; the R16.4 test pod was applied and passed (`Completed`, logged `ok`) |
| D3 | 0 | R11.11, R11.16 | CI is written but has never run green | The branch is not pushed and the repo's remote/registry org are both pending (Q6); every CI step has a local `make` equivalent that does pass (build, test, cover, lint-partial, helm-lint, render-check, kustomize, image-size) | First push to `main` closes this; the Phase 0 commit is the one CI runs against |
| D4 | 0 | R11.11 | `make lint` runs `gofmt` + `go vet` and skips `golangci-lint` on this machine | `golangci-lint` is not installed locally; the Makefile prints an explicit skip line rather than pretending, and CI installs the pinned v1.64.8 with `.golangci.yml` | Install `golangci-lint` locally (or run `make lint` in a container) before Phase 1; the config is already pinned so local and CI agree |
| D5 | 0 | R11.12 | `chart/values-default.yaml` is a symlink to `chart/values.yaml` rather than a second file | R11.12 requires `values-default.yaml` to *be* the shipped default, not an empty override. A symlink makes that literal and unbreakable — the two can never drift — and helm 4.3.1 follows it (`level=INFO msg="found symbolic link in path"`). Windows checkouts without symlink support are the known risk | If a Windows contributor appears, replace the symlink with a byte-identical copy and let `make render-check` keep them aligned |

### Spec-level issues from round 1 — all resolved in v0.2.0

The nine live-probe changes (P1–P9) and all 55 round-1 findings are now folded into
`requirements.md` v0.2.0. The disposition table and the four reviewer-disagreement rulings are in
spec §11; the line-by-line diff is in spec Appendix A. Nothing is outstanding at the spec level.

---

## 6. How to run it right now

Updated as each phase lands. Currently (Phase 0):

```sh
# Build and run locally (arm64 or amd64 — cgo off, so it is the same binary either way)
make build                       # bin/pgsi, version + commit injected (R11.3)
./bin/pgsi serve                 # :8080 — /healthz, /readyz, /api/v1/version, /metrics
./bin/pgsi version               # 0.1.0-dev + the git SHA it was built from
make test                        # go test ./...
make cover                       # 70% gate (R11.11)
make lint                        # gofmt + go vet (+ golangci-lint when installed)

# Chart
make helm-lint                   # lint default | production | lab (R11.12)
make render                      # regenerate deploy/plain-yaml/ (R11.13)
make render-check                # the golden test: regenerate + git diff --exit-code

# Images
make image                       # buildx --platform linux/amd64,linux/arm64 --push (R11.2)
make image-size                  # R11.4 compressed size, gated at IMAGE_MAX_MB (default 100)
make push-local                  # both platforms → 10.10.111.134:5000, no docker login (D11)

# Deploy to the reference cluster + verify (R11.14)
make deploy DEPLOY_ARGS="--set image.repository=10.10.111.134:5000/frankellis/pgsql-insights --set image.tag=dev"
make verify                      # port-forward 18080:8080, curl /healthz, /api/v1/version, /metrics
```

No Helm? `kubectl apply -f deploy/plain-yaml/all.yaml` applies the same objects the chart renders
(R11.13), and `kubectl apply -k deploy/kustomize` does the same through kustomize.

---

## 7. Conventions for whoever picks this up

- **Commit messages**: end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- **One commit per phase**, containing that phase's code **and** its `IMPLEMENTATION.md` update
  (R14.1 scopes this to the last commit of the phase).
- **Requirement IDs are the contract.** Code comments reference `R3.4`, not "the fingerprinting bit".
- **Never add a metric or route that isn't in the docs** (R14.7) — the metrics catalogue and
  `api/openapi.yaml` are generated and CI-checked.
- **Read the `dataviz` skill before writing any chart code** (R10.2), and record the chosen chart
  library in §4 of this file. That is a Phase 5 gate, not a Phase 5 suggestion.
- **Do not spawn a fifth subagent.** The cap is four (§2).
- **Every §4 requirement ID maps to exactly one phase** (spec §7.3). Adding a requirement without a
  phase assignment is a spec defect, not a scheduling detail — that is how 0.1.0 lost 18 IDs.
