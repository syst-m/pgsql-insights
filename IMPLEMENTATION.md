# Implementation status — pgsql-insights

**Handoff file.** Per `requirements.md` R14.1, this file is created before any other code and is
updated in the **same commit** as every phase's work. A developer who has never seen this project
should be able to read this file, then `requirements.md`, and contribute without asking anyone
anything.

| | |
|---|---|
| **Spec** | `requirements.md` — v0.1.0-draft, committed at `97172a4` |
| **Current phase** | **Pre-Phase 0** — spec under review, no code yet |
| **Phase 0 status** | not started (blocked on the round-1 review consolidation → spec v0.2.0) |
| **Last updated** | 2026-11-20 |

---

## 1. Where the project stands

| Milestone | Status | Notes |
|---|---|---|
| Research: Cloud SQL Query/System Insights | ✅ done | `docs/research/cloud-sql-insights.md` (949 l), by A1 (opus) |
| Research: CloudNativePG surface inventory | ✅ done | `docs/research/cloudnativepg.md` (2475 l), by A2 (sonnet) |
| Reference environment documented | ✅ done | `docs/ENVIRONMENT.md`, k3s + CNPG 1.30.0 + 4 clusters |
| Live probe of the target clusters | ✅ done | `docs/research/live-probe.md` + `testdata/` fixtures |
| **`requirements.md` v0.1.0** | ✅ done | committed `97172a4` |
| **`IMPLEMENTATION.md`** (this file) | ✅ created | R14.1 satisfied ahead of Phase 0 |
| Round-1 review: architecture (A3, opus) | 🔄 in flight | → `docs/reviews/round-1-architecture.md` |
| Round-1 review: product/UI/packaging (A4, sonnet) | 🔄 in flight | → `docs/reviews/round-1-product.md` |
| **Spec v0.2.0** (reviews incorporated) | ⬜ pending | the gate for Phase 0 |
| Phases 0–9 | ⬜ not started | see §3 |
| v0.1.0 release | ⬜ not started | end of Phase 9 |

**What exists on disk right now:** `requirements.md`, `IMPLEMENTATION.md`, `docs/ENVIRONMENT.md`,
`docs/research/` (3 docs), `testdata/` (4 metric expositions + 3 status JSONs), `.gitignore`.
No Go code, no chart, no Dockerfile yet — deliberately, because the brief gates implementation on
the spec being "documented, reviewed, and validated by subagents".

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

Phase→agent assignment is in `requirements.md` §7's table. No phase may spawn a fifth agent.

---

## 3. Work breakdown

Status legend: ⬜ not started · 🔄 in progress · ✅ done · ⚠️ done with deviations

| Phase | Scope | Req IDs | Owner | Status | Validation entry |
|---|---|---|---|---|---|
| **0** | Repo skeleton, `go.mod`, Makefile, multi-arch Dockerfile, chart skeleton, CI, license | R11.1–R11.5, R11.7, R11.11, R15 | A4 | ⬜ | — |
| **1** | Discovery + kube client + `/api/v1/clusters` + `/healthz` + `/readyz` | R1.1–R1.9, R8.1, R8.7 | A1 | ⬜ | — |
| **2** | Collector framework + PG version matrix + database/settings/replication/wal collectors + `/metrics` v1 | R2.1–R2.8, R9.2–R9.4 | A1 | ⬜ | — |
| **3** | `pg_stat_statements` collector + normalizer + query store + top-N API + per-query metrics | R3.1–R3.8, R3.14, R9.5 | A1 | ⬜ | — |
| **4** | ServiceMonitor/PodMonitor, PrometheusRule, cardinality assertion, live scrape by the reference Prometheus | R9.1, R9.6, R9.8–R9.10 | A2 | ⬜ | — |
| **5** | Full API surface + OpenAPI + auth + PromQL pair + SPA shell with the Fleet page | R8.1–R8.9, R10.1–R10.4, R10.13 | A2 | ⬜ | — |
| **6** | Active queries + blocking chain + wait events + terminate + Queries & Query Detail pages | R4.1–R4.7, R3.9–R3.13, R10.5–R10.9 | A1 | ⬜ | — |
| **7** | System Insights: OS + DB families, summary cards, compare-5, PNG/CSV export | R5.1–R5.8, R9.7, R10.10 | A2 | ⬜ | — |
| **8** | Pooler insights + Recommenders + events timeline + plan capture | R6.1–R6.5, R7.1–R7.6, R5.9, R3.10–R3.11, R10.11 | A1 | ⬜ | — |
| **9** | Docs site, runbooks, README, screenshots, lab profile, v0.1.0 release | R11.6, R11.9, R13.6, R14.3–R14.7, R15.2 | A4 | ⬜ | — |

### Task-level breakdown (Phase 0, expanded when it starts)

Phases 1–9 are expanded into tasks here as each one starts. The rule from R14.1: this table and the
phase's code land in the **same commit** — no catching up on documentation later.

---

## 4. Decisions log

Every decision that a future contributor would otherwise have to ask about. Newest first.

| # | Date | Decision | Why |
|---|---|---|---|
| D6 | 2026-11-20 | `pods/proxy` is the **fallback** transport, direct pod-IP HTTP is primary | Measured: `pods/proxy` returns 502 through a tunnelled API endpoint while `exec`/`port-forward` to the same pod work (`live-probe.md` §5). The 502 is a route artifact, not an endpoint failure |
| D5 | 2026-11-20 | Multi-arch build (`linux/amd64` + `linux/arm64`) is a **Phase 0 blocker** | All five target nodes are amd64; the dev machine and Docker default are arm64, so a plain `docker build` yields an image that cannot schedule on the target cluster (`live-probe.md` §7) |
| D4 | 2026-11-20 | Corrected the `spec.metrics` framing in `ENVIRONMENT.md` | CNPG 1.30 has **no exporter sidecar at all** — the exporter is compiled into the instance manager, so `:9187` is always up and `spec.monitoring` only selects which queries run. The earlier note called the sidecar "a third, opt-in source", which is wrong for 1.30 |
| D3 | 2026-11-20 | Product/binary/metric-prefix name is **`pgsi`** (repo `pgsql-insights`) | Open question Q1 in the spec; `pgsi` for CLI ergonomics and to avoid collision with `cnpg_`/`pg_`/`container_` prefixes. Awaiting the reviews' verdict on it |
| D2 | 2026-11-20 | **Central fleet-wide poller**, not a per-instance agent | No cluster in the reference env enables `spec.monitoring`; a design requiring a per-cluster patch fails "rapidly rollable out" (`requirements.md` §3.1) |
| D1 | 2026-11-20 | **SQLite** (`modernc.org/sqlite`, pure Go) as the default store, `memory` as the lab option | Keeps the image distroless-buildable and the chart dependency-free; 1 Gi PVC gives 7–30 d retention without forcing a database on the product (§3.4) |

---

## 5. Deviations from the spec

R14.2 requires every phase to record deviations here, and the brief says "if there are deviations,
fix them." A deviation is any place the code diverges from a requirement ID — with a reason, and
either a fix or a tracked follow-up.

| # | Phase | Req ID | Deviation | Reason | Resolution |
|---|---|---|---|---|---|
| — | — | — | *(no code yet, so no deviations)* | | |

### Known spec-level issues awaiting v0.2.0

Nine changes from the live probe are queued for the v0.2.0 consolidation (`live-probe.md` §9),
deliberately **not** applied to `requirements.md` while the reviewers are reading it, so they are
not reviewing a moving target. They fold in together with the round-1 findings.

---

## 6. How to run it right now

Updated as each phase lands. Currently:

```sh
# Nothing to run yet — the repo is spec-only.
# After Phase 0 this becomes:
make build          # builds bin/pgsi
make test           # unit tests
make helm-lint      # lint the chart
docker buildx build --platform linux/amd64,linux/arm64 -t pgsi:dev .
```

---

## 7. Conventions for whoever picks this up

- **Commit messages**: end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- **One commit per phase**, containing that phase's code **and** its `IMPLEMENTATION.md` update.
- **Requirement IDs are the contract.** Code comments reference `R3.4`, not "the fingerprinting bit".
- **Never add a metric or route that isn't in the docs** (R14.7) — the metrics catalogue and
  `api/openapi.yaml` are generated and CI-checked.
- **Read the `dataviz` skill before writing any chart code** (R10.2), and record the chosen chart
  library in §4 of this file. That is a Phase 5 gate, not a Phase 5 suggestion.
- **Do not spawn a fifth subagent.** The cap is four (§2).
