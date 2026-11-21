# Implementation status — pgsql-insights

**Handoff file.** Per `requirements.md` R14.1, this file is created before any other code and is
updated in the **same commit** as every phase's work (scoped by R14.1 to the *last commit of the
phase*, so it stays workable across four agents). A developer who has never seen this project
should be able to read this file, then `requirements.md`, and contribute without asking anyone
anything.

| | |
|---|---|
| **Spec** | `requirements.md` — **v0.2.0**, round-1 reviews incorporated |
| **Current phase** | **Phase 0 — packaging & skeleton** (in progress) |
| **Phase 0 status** | 🔄 in progress — owned by A4 (sonnet) |
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
| **`IMPLEMENTATION.md`** (this file) | ✅ current | R14.1 satisfied |
| **Phase 0** — packaging & skeleton | 🔄 in progress | A4 |
| Phases 1–9 | ⬜ not started | see §3 |
| v0.1.0 release | ⬜ not started | end of Phase 9 |

**What exists on disk right now:** `requirements.md` (v0.2.0), `IMPLEMENTATION.md`,
`docs/ENVIRONMENT.md`, `docs/research/` (3 docs), `docs/reviews/` (2 docs), `testdata/`
(4 metric expositions + 3 status JSONs), `.gitignore`. Go code, chart and Dockerfile land in
Phase 0.

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
| **0** | Repo skeleton, `go.mod`, Makefile, multi-arch Dockerfile, chart skeleton + `values-default.yaml`, generated `deploy/plain-yaml/all.yaml`, CI, image publication, LICENSE | R11.1–R11.7, R11.9–R11.16, R12.1–R12.3, R13.9, R13.10, R14.1, R14.8, R15.1, R15.2 | A4 | 🔄 | — |
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

**Post-0.1** (defined in spec §4, deliberately unscheduled in v0.1): **R7.1** index advisor and
**R7.3** rightsizing. Both moved out of Phase 8 in v0.2.0 — R7.1 needs R3.15 executed on a real
cluster before its quality bar is even measurable, and R7.3 is CNPG-derived rather than a Cloud SQL
clone (spec §11 ruling, A4-M11).

### Task-level breakdown (Phase 0, expanded)

| # | Task | Req | Status |
|---|---|---|---|
| 0.1 | `go.mod` (Go 1.26, no cgo, no `replace`) + `cmd/pgsi` with `serve\|collect\|api\|version` stubs | R3.1, R13.10 | ⬜ |
| 0.2 | Makefile: `build`, `test`, `lint`, `helm-lint`, `render`, `image`, `licenses` | R11.11 | ⬜ |
| 0.3 | Multi-stage Dockerfile → `gcr.io/distroless/static:nonroot`, uid 65532, pinned base digest | R11.2, R11.3, R11.5 | ⬜ |
| 0.4 | `buildx --platform linux/amd64,linux/arm64` verified (Phase 0 **blocker** — all 5 nodes are amd64) | R11.2 | ⬜ |
| 0.5 | Chart skeleton: chart.yaml, values.yaml (**`values-default.yaml` as the shipped default**), `_helpers.tpl`, Deployment, Service, ServiceAccount, Role/RoleBinding (+ ClusterRole variant), ConfigMap, ingress, networkpolicy, `.helmignore` | R11.6, R11.7, R11.12, R12.1–R12.3 | ⬜ |
| 0.6 | `httpGet` probes on `/healthz` + `/readyz`; `/tmp` as `emptyDir`; securityContext per R12.7 | R11.2, R12.7 | ⬜ |
| 0.7 | `deploy/plain-yaml/all.yaml` **generated** by `helm template` in CI; `deploy/kustomize/` points at it | R11.13 | ⬜ |
| 0.8 | CI: build, `golangci-lint`, `helm lint`, `helm template` snapshot tests (3 profiles), unit tests, multi-arch build, **image publication to the named registry** | R11.11, R11.16 | ⬜ |
| 0.9 | LICENSE (Apache-2.0) + `docs/licenses/` scaffold | R15.1 | ⬜ |
| 0.10 | **Deploy-and-observe acceptance**: image built **and deployed** to the reference cluster, pod `Running`, `/healthz` answers through the Service | R11.14 | ⬜ |
| 0.11 | Compatibility matrix artifact with `tested\|fixture\|impossible\|untested` cells | R13.9 | ⬜ |
| 0.12 | `IMPLEMENTATION.md` requirement→file→status table populated for Phase 0's IDs | R14.8 | ⬜ |

**Open blocker inherited by Phase 0:** the container registry org name (spec Q6). R11.16 needs it
to publish, and R11.9's two-command quickstart cannot be copy-pasted until the registry exists.
Until then the packaged chart tarball is the verifiable install path.

---

## 4. Decisions log

Every decision that a future contributor would otherwise have to ask about. Newest first.

| # | Date | Decision | Why |
|---|---|---|---|
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
| — | — | — | *(no code yet, so no code deviations)* | | |

### Spec-level issues from round 1 — all resolved in v0.2.0

The nine live-probe changes (P1–P9) and all 55 round-1 findings are now folded into
`requirements.md` v0.2.0. The disposition table and the four reviewer-disagreement rulings are in
spec §11; the line-by-line diff is in spec Appendix A. Nothing is outstanding at the spec level.

---

## 6. How to run it right now

Updated as each phase lands. Currently:

```sh
# Spec-only repo — nothing to build yet. Phase 0 lands:
make build          # builds bin/pgsi
make test           # unit tests
make lint           # golangci-lint
make helm-lint      # lint the chart
make render         # helm template all three profiles -> deploy/plain-yaml/all.yaml
docker buildx build --platform linux/amd64,linux/arm64 -t pgsi:dev .
```

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
