# Architecture

R14.4 requires an architecture doc in `docs/` carrying the diagram from
[`requirements.md` §3.1](../requirements.md). This file is that doc: the §3.1 diagram,
the reasoning it encodes, and — because the spec's architecture and the repository's
architecture are not yet the same set of files — a per-package table of what exists
today, what is in flight, and what is planned.

The short version of the design, from §3.1: Cloud SQL can instrument each instance
because Google owns the instance. On CloudNativePG we do not own the pod, so
instrumenting per instance means patching every `Cluster` CR, which fails the
"rapidly rollable out" bar. **pgsi is therefore a fleet-wide poller, not a
per-instance agent** — one Deployment that discovers every CNPG cluster its RBAC can
see and pulls from each.

---

## The §3.1 diagram

```
                         ┌──────────────────────────────────────────────┐
   Kubernetes API ───────▶  pgsi (one Deployment, one container)       │
   watch: clusters,       │                                            │
   poolers, pods,         │  ┌────────────┐  ┌───────────┐  ┌────────┐ │
   secrets, pods/proxy    │  │ COLLECTOR  │─▶│   STORE   │◀─│  API   │ │──▶ /api/v1/*
                         │  │ (pollers)  │  │ sqlite /  │  │ (REST) │ │
                         │  └────────────┘  │  memory   │  └───┬────┘ │
                         │        ▲          └───────────┘      │      │
                         │        │                             │      │
                         │  ┌─────┴──────────────────┐          ▼      │
                         │  │ MetricHub: our own     │      ┌──────┐   │──▶ /metrics
                         │  │ registry, per-instance │      │ SPA  │   │
                         │  │ + fleet aggregation    │      │(embed)│  │──▶ /
                         │  └────────────────────────┘      └──────┘   │
                         └──────────────────────────────────────────────┘
                                    ▲                          ▲
              direct SQL :5432 ─────┤                          │
              HTTP :9187/metrics ───┤   (or via pods/proxy)    │
             HTTPS :8000/pg/status ─┘                          │
                                                    Prometheus ◀┘ (ServiceMonitor scrapes us;
                                                      our /metrics carries every instance)
```

Four boxes — collector, store, API, embedded SPA — inside **one binary and one
container** (R11.1). The diagram is the target; the "as-built" column below says
which boxes are drawn and which are still outlines.

## Why one binary, one role

`pgsi` ships `serve | collect | api | version` (R3.1), and **`serve` is the only role
v1 supports**: collector + store + API + UI in one process. The `collect`/`serve`
split earlier drafts promised as `--role=collect|serve` is deferred to v1.x for two
concrete reasons (§3.1):

* a split role needs two pods sharing one SQLite file, which requires `ReadWriteMany`
  storage the chart cannot assume (R11.15) — the reference environment's only
  StorageClasses are `iscsi-ext4` (default) and `iscsi-xfs`, both `ReadWriteOnce`;
* SQLite's single-writer WAL mode contends on NFS-class RWX volumes, the usual
  k3s/on-prem answer to RWX.

The subcommand names are **reserved** so v1.x can add them without breaking the CLI
contract; today `collect` and `api` are stubs that exit cleanly.

## Data sources (§3.2), ranked by cost and by always-available-ness

| # | Source | Endpoint | Yields | Always there? |
|---|---|---|---|---|
| S1 | `Cluster.status` | `GET` on the CR | phase, instances, per-pod `instancesReportedState`, `currentPrimary`, topology, PVC list, cert expirations, image, systemID | **Yes** — free, no metrics needed |
| S2 | Direct SQL | `<cluster>-rw.<ns>.svc:5432` or the pod IP from S1 | `pg_stat_statements`, `pg_stat_activity`, `pg_stat_database`, `pg_stat_replication`, `pg_stat_wal`, `pg_stat_io`, `pg_settings`, table/index stats | **Yes** on a serving instance; not on a crash-looping pod |
| S3 | Instance-manager metrics | `http://<pod>:9187/metrics` | the `cnpg_*` surface; measured **107 / 115 / 118** `# TYPE` families on the three reachable reference pods | **Yes** — compiled into the instance manager, not gated on `spec.monitoring` |
| S4 | Instance-manager status | `https://<pod>:8000/pg/status` (HTTPS always) | 27 verified fields incl. `replicationInfo[]`, `pendingRestart` | **Yes** |
| S5 | Pooler metrics | `http://<pooler-pod>:9127/metrics` | `cnpg_pgbouncer_*` — measured 80 families / 22 775 B, a surface with zero existing dashboards | Only where a `Pooler` exists (1 of our 4) |
| S6 | Pooler runtime | pgbouncer `SHOW POOLS/CLIENTS/STATS` | the client→server mapping that fixes attribution | Same |
| S7 | cAdvisor / node-exporter / metrics-server | from Prometheus, or direct | container CPU/mem, volume bytes | Present in the reference env; **optional** for us |
| S8 | `kubectl cnpg status -o json` | CLI | S1 plus live PG rows | Optional; we implement natively rather than shell out |
| S9 | `postgres_exporter` sidecar | its own `/metrics` | 36 collectors incl. `stat_statements` | Optional third path, off by default |
| S10 | Pod logs | `pods/log` subresource | the R5.1 log-errors card, the R5.9 events feed | Only with `--log-source` (R5.10); **off by default** |

**S1 → S2 → S3 is the required spine**: everything in Phases 1–4 works with only
those three, all of which exist on a stock CNPG pod with no `Cluster` patch.

One measured caveat that changes a design decision (§3.2): neither CNPG's built-in
exporter nor `postgres_exporter` collects `pg_stat_io`, so the `io` collector's S3
path yields nothing — `pg_stat_io` comes from S2 only, and only on PG 16+. Building
it on S3 alone silently produces an empty panel on every cluster.

## Collection model (§3.3)

Per cluster, on a configurable interval (default **60 s**, matching Cloud SQL's
sampling period): `inventory` (watch + 30 s resync, S1, drives every other
collector), `statement` (60 s, S2, gated on `pg_stat_statements` presence per R3.1),
`activity` (15 s, S2), `database` (60 s, S2), `wal` (60 s, S2 + S3), `io` (60 s,
S2-only per the caveat above). The full table is §3.3.

---

## As-built vs target

R14.3's honesty rule applies here: a reader should be able to tell what runs today
from what is planned. This table is the requirement→file→status view for the
architecture boxes; the authoritative version lives in
[`IMPLEMENTATION.md`](../IMPLEMENTATION.md) (R14.8).

| Box | Requirement | Where it lives | Status |
|---|---|---|---|
| CLI dispatch | R3.1, R11.1 | `cmd/pgsi/main.go` | **implemented** — `serve` real, `collect`/`api` reserved stubs |
| Version injection | R11.3 | `internal/version/` | **implemented** — ldflags, `pgsi version` |
| HTTP server, `/healthz`, `/readyz`, `/api/v1/version` | R8.7 | `internal/api/server.go` | **implemented** |
| `/api/v1/clusters` contract | R8.1, R16.8 | `internal/api/dto/clusters.go`, `testdata/api/clusters.json` | **implemented** — the response shape is frozen (R16.8's single-copy rule); the fixture is consumed by both the Go golden test and the SPA's Vitest suite |
| SPA served from the binary at `/` | R8.6, R10.x | `internal/api/spa.go`, `web/embed.go` (`go:embed all:dist`) | **implemented** — Phase 1b |
| Kubernetes client | R1.7, R1.10, R1.11 | `internal/kube/` (`client.go`, `transport.go` + their tests, 4 files) | **in flight** (Phase 1a, A1) — the ids this row named, R12.1–R12.3, are the chart's RBAC rows, rowed below |
| CNPG discovery | R1.1–R1.9b, R8.1 | `internal/discovery/` (`build.go`, `discovery.go`, `fields.go`, `poolers.go` + their tests, 6 files) | **in flight** (Phase 1a, A1) — R2.1 and R2.4, the ids it named, are Phase 2's |
| API token auth | R8.8 | `internal/api/auth.go` (+ `cors.go`) | **implemented** — Phase 5 (A2), with A3's review fixes in `f9aae41`; the row read "in flight (Phase 1a)" while R8.8 is Phase 5's |
| OpenAPI document | R8.1, R8.1b | `api/openapi.yaml`, generated by `internal/api/openapi_gen.go` and embedded by `api/embed.go` | **implemented** — generated since `9794560`, byte-compared by `openapi_gen_test.go` and validated by `scripts/check-openapi.py`; the row read "R13.4 / `internal/api/openapi.yaml` / not started", and R13.4 is the cardinality budget |
| Store (sqlite / memory) | R13.5, R3.13, R3.14 | `internal/store/` (19 files: `store.go`, `sink.go`, `schema.go`, `series.go`, `detail.go`, `perquery.go`, `statements.go`, `retention.go`, `rollups.go`, `topn.go` + 9 tests) | **⚠️ done with deviations** — Phase 3 (A5), §5-D18; the row read "R11.6 / not started (Phase 2)", and R11.6 is the chart |
| Collectors | R3.x, R9.4–R9.7 | `internal/collector/` (61 files: 31 + 30 tests, incl. the `cnpgmetrics/`, `metrictype/`, `pgxopener/` subpackages) | **in flight** — Phase 2 (A1); `statements.go` (`76b0f56`, Phase 3's) and `bloat.go` (`d9f2901`, Phase 8's R7.4 half) are already in |
| MetricHub (our own registry) | R9.2, R9.3 | `internal/metrics/` (`metrics.go`, `http.go`, `store.go` + 4 tests, 7 files) | **in flight** — A2 owns the hub (``1b11199:IMPLEMENTATION.md:743`); `/metrics` serves the eight R9.8 families (`internal/metrics/metrics.go:18-24`), not just the Go runtime collector, but R9.4/R9.5's product families still have no non-nil source |
| SPA pages | R10.3 | `web/src/pages/` (9 components, 19 files) | **8 of 14** of R10.3's pages exist — the eight routed pages; the six `component: null` routes render `NotImplemented`. Route table: `web/src/routes.ts:167` |
| Helm chart | R11.6, R11.7, R11.12 | `chart/` | **implemented** — see [`chart/README.md`](../chart/README.md) |
| Generated manifests | R11.13, R16.4 | `deploy/plain-yaml/`, `deploy/kustomize/` | **implemented** — golden files for all three profiles |
| CI | R11.11, R11.16 | `.github/workflows/ci.yml` | **implemented** — build, lint, test, web, helm, image |

The **collector → store → MetricHub** chain is built but not yet joined end to end:
`pgsi serve` answers `/api/v1/clusters` from the `*discovery.Discoverer`
(`internal/api/server.go:367-372`), and `/metrics` serves the eight R9.8
self-observability families (`internal/metrics/metrics.go:18-24`) — build info, collector
duration and errors, `pgsi_up`, store size, HTTP duration, scheduler queue depth and shed
— while the R9.4/R9.5 product families still have no non-nil source (§3:117). Everything
to the right of the diagram — the REST surface and the embedded SPA — is real and tested.

---

## Cross-references

* [requirements.md](../requirements.md) — the spec; §3 is the source of this doc's diagram and §3.2–3.3 of the source/collection tables.
* [IMPLEMENTATION.md](../IMPLEMENTATION.md) — the handoff file: work-breakdown, decisions log, deviations, requirement→file→status table (R14.1, R14.8).
* [chart/README.md](../chart/README.md) — the chart contract, the three profiles, the ServiceMonitor validation record and the cardinality budget (R9.9, R9.10, R11.6, R16.4).
* [docs/ENVIRONMENT.md](ENVIRONMENT.md) — the reference cluster as measured.
* [docs/research/live-probe.md](research/live-probe.md) — the probe record behind the S1–S9 measurements.
* [docs/compatibility-matrix.md](compatibility-matrix.md) — R13.9's support matrix and the evidence class behind each cell.
* [docs/runbooks/](runbooks/) — per-feature runbooks (R13.6, R14.4), the targets of R9.11's `runbook_url` annotations.
