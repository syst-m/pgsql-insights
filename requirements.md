# pgsql-insights — Requirements Specification

| | |
|---|---|
| **Version** | `0.1.0-draft` (pre-review) |
| **Date** | 2026-11-20 |
| **Status** | Awaiting two adversarial reviews (§11) |
| **Owners** | Frank Ellis (product), Claude (spec author) |
| **Applies to** | CloudNativePG 1.30.x on Kubernetes, PostgreSQL 13–18 (target env: CNPG 1.30.0, PG 16 + 17) |

**Provenance.** Every factual claim this spec builds on is drawn from one of three grounded
documents, not from model memory:

| Source | Path | Used for |
|---|---|---|
| Cloud SQL product research | `docs/research/cloud-sql-insights.md` (949 l) | The capability inventory we are cloning (§2, §6 parity matrix) |
| CloudNativePG surface inventory | `docs/research/cloudnativepg.md` (2475 l) | Every CNPG/PG data source, endpoint, metric name, and version matrix |
| Reference environment | `docs/ENVIRONMENT.md` | The four clusters the acceptance criteria are testable against |

Where this spec deliberately departs from Cloud SQL, the departure is stated with a reason
(marked **Divergence**). Where Cloud SQL has a capability PostgreSQL cannot supply, it is marked
**Gap** rather than silently dropped.

---

## 1. Product definition

### 1.1 One line

An open-source, container-packaged, Kubernetes-native web product that gives a CloudNativePG
cluster the same three capabilities Google sells inside Cloud SQL — **Query Insights**, **Active
queries**, and **System Insights** — using only sources that already exist on a CNPG pod, so that
installing it is a single `helm install` and requires no change to any existing `Cluster` resource.

### 1.2 What we are cloning

| Cloud SQL capability | Scope we adopt | Reference |
|---|---|---|
| **Query Insights** | Top-N query list with the Cloud SQL column set, per-query detail, plan samples, latency percentiles, fingerprinting, per-database/user/tag rollups | `cloud-sql-insights.md` §3 |
| **Active queries** | Live session list (active + idle-in-transaction), blocking chain, session/transaction/query durations, terminate | §3.10 |
| **System Insights** | Summary cards + OS/DB metric families + compare-up-to-5 + PNG/CSV download | §4 |
| **Connection-pooling insights** | Client-address attribution behaviour under pgbouncer, pool statistics | §6 |
| **Recommenders / index advisor** | Index suggestions, bloat, rightsizing | §7 |
| **AI-assisted troubleshooting** | Deferred to a later major (see §1.3) | §3.11 |
| **Admin API shape** | PromQL-in / PromQL-result-out pair, mirroring the Database Insights MCP API | §5.5 |

We clone the **information architecture and the answers each screen gives**, not the Google UI.
Several Google features exist only in **Enterprise Plus** (30-day retention, 100 KB query text,
200 plans/min, wait-event analysis, index advisor, terminate-session, AI troubleshooting) at no
extra cost; since we have no edition tiers, **we implement the Enterprise Plus feature set as the
baseline** and match Enterprise's cheaper limits where the Enterprise Plus ones are impractical
(§5 explains which and why).

### 1.3 Non-goals

| Not in scope | Why |
|---|---|
| Managing/provisioning Postgres (create cluster, scale, restore) | That is the operator's and `kubectl cnpg`'s job. We are read-only plus a small set of explicitly-allowed actions (terminate session, EXPLAIN). |
| Supporting non-CNPG Postgres (Zalando, Crunchy, RDS, standalone) | The brief scopes us to CNPG. The collector is factored so the SQL layer is reusable, but CNPG discovery is the contract. |
| Being a Grafana replacement | Grafana answers "plot this metric". We answer "why is this database slow". We export *to* Prometheus so Grafana keeps working. |
| Multi-engine (MySQL, AlloyDB) | PostgreSQL only. |
| AI-assisted troubleshooting in v1 | Cloud SQL's Investigation pane is Enterprise Plus and needs a model in the loop. We design the API surface for it (`/api/v1/insights/diagnosis`) and ship it post-0.1. |
| Real-time streaming of every statement (a "firehose"/pgBadger-alike) | Cloud SQL samples at 60 s, not per-statement. Matching its sampling model keeps cost and cardinality bounded (§5). |
| Multi-kubernetes-cluster federation of our own API | One deployment sees one cluster's namespaces. Fleet-wide centralization is Prometheus's job, which is exactly the architecture the brief asks for. |

### 1.4 Delivery constraints from the brief, and how each is met

| Constraint (verbatim intent) | How this spec satisfies it | Requirement |
|---|---|---|
| "web based" | SPA served by the same Go binary that serves the API; no separate web tier | R10, R11.1 |
| "container packaged" | One distroless image, multi-arch `linux/arm64` + `linux/amd64`, no shell, non-root uid | R11.2–R11.5 |
| "k8s native" | Discovers work through the Kubernetes API (watch on CNPG CRDs), ships ServiceMonitor + PrometheusRule, runs as a normal Deployment with a least-privilege Role | R1, R11, R12 |
| "rapidly rollable out" | **Zero per-cluster prerequisites.** No `Cluster` patch, no extension install, no sidecar, no password hand-out required to get a first useful screen. One Helm release in one namespace covers every cluster the RBAC can read | R1.6, R2.1, R11.6, R12.4 |
| "others can benefit… in production or in a lab" | Production profile (SQLite store, TLS, RBAC-only auth) and lab profile (in-memory store, `--single-namespace`, no PVC) from the same chart | R11.6, R13.6 |
| "provides prometheus metrics so that multiple instances can be centralized into a single Prometheus instance for the basic metrics" | `/metrics` exposes one flat, label-normalized series set per instance, safe to scrape N instances into one Prometheus; documented label contract so dashboards survive multi-instance scraping | R9 |
| "Some features won't make sense to fall back that way" | Explicit split (§4.9): **basic metrics → Prometheus**; **query text, plans, active sessions, blocking chains → API + store only**. Each feature is tagged with which plane serves it | R9.1, R8 |
| "extremely easy-to-use graphs and visualizations" | R10 specifies per-page chart types, the design system, and a chart library chosen by the `dataviz` skill before any chart code is written | R10 |
| "backend APIs" | Versioned REST API, plus the PromQL-in/PromQL-out pair mirroring Google's MCP shape | R8 |
| "keep state of the implementation progress and work breakdown… a file that can be handed off" | `IMPLEMENTATION.md` is created at Phase 0 and updated every phase (§7.1 lists it as a phase deliverable) | R14 |

---

## 2. Personas and jobs to be done

| # | Persona | Their job | Served by |
|---|---|---|---|
| P1 | **Platform engineer** running CNPG for several teams | "Install once, see every Postgres in the cluster, and know which one is on fire." | R1, R9, R10.1 (Fleet) |
| P2 | **Application developer** with one app on one cluster | "Why is *my* query slow, and what should I change?" | R3, R10.3–R10.4, R7 |
| P3 | **DBA / on-call** at 03:00 | "What is running right now, who is blocking whom, and can I kill it?" | R4, R10.5 |
| P4 | **SRE building a golden path** | "Give me the metrics in our central Prometheus and the alerts, without hand-maintaining a 66-panel dashboard." | R9, R11.7, R7 |
| P5 | **Evaluator / lab user** | "`helm install` in a namespace with one cluster and see something useful in under five minutes." | R11.6, R13.6 |

**Jobs, in priority order** (this ordering is the phase order in §7):

1. J1 — See every CNPG cluster and its health at a glance. *(P1, P4)*
2. J2 — Get the top-N most expensive queries for a cluster, with text. *(P2, P3)*
3. J3 — Drill into one query: its history, plans, and per-minute load. *(P2)*
4. J4 — Export basic metrics to a central Prometheus and have them work across instances. *(P4)*
5. J5 — See what is running right now and what is blocking it. *(P3)*
6. J6 — See system-level health (CPU, memory, connections, TXID wraparound, disk, temp). *(P1, P4)*
7. J7 — Get actionable recommendations (index, bloat, rightsizing). *(P2)*
8. J8 — See pooler behaviour and correctly attribute work through pgbouncer. *(P2, P1)*

---

## 3. Architecture

### 3.1 The decision: a fleet-wide poller, not a per-instance agent

Cloud SQL can instrument each instance because Google owns the instance and installs
`pg_stat_statements` plus the `cloudsql*` flags as part of provisioning. On CNPG we do not own the
pod: adding instrumentation means patching every `Cluster` CR, which fails the "rapidly rollable
out" bar — the reference environment has **four clusters, none of which enable `spec.monitoring`**.

So we adopt a **central poller**: one Deployment that discovers every CNPG cluster its RBAC can
see, and pulls from each. This is the shape the CNPG ecosystem already validates — the official
Headlamp plugin reads instance metrics through the Kubernetes API pod-proxy subresource, "no psql,
no exec" — and it is what makes one install cover a whole fleet.

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
              HTTP :8000/pg/status ─┘                          │
                                                    Prometheus ◀┘ (ServiceMonitor scrapes us;
                                                      our /metrics carries every instance)
```

**Roles, one binary.** A single `pgsi` binary with `serve` as the default mode running collector +
store + API + UI in one process. `--role=collect` and `--role=serve` split the same binary for
teams that want to poll many namespaces from a dedicated poller and serve the API separately;
`--role=all` (default) is what the chart ships. This keeps the common case to one image and one
Deployment while leaving the split available without a second codebase.

### 3.2 Data sources, ranked by cost and by always-available-ness

| # | Source | Endpoint | Auth | Yields | Always there? |
|---|---|---|---|---|---|
| S1 | **`Cluster.status`** | `GET` on the CR | RBAC | phase, instances, readyInstances, instancesStatus, instancesReportedState (per-pod isPrimary/ip), currentPrimary, topology, pvc lists, services, conditions, cert expirations, image, systemID | **Yes** — free, no metrics needed |
| S2 | **Direct SQL** | `<cluster>-rw.<ns>.svc:5432`, or pod IP from S1 | `<cluster>-app` secret, or a `pg_monitor` role we document | `pg_stat_statements`, `pg_stat_activity`, `pg_stat_database`, `pg_locks`, `pg_stat_replication`, `pg_stat_wal`, `pg_stat_io`, `pg_stat_slru`, `pg_stat_checkpointer`, `pg_stat_progress_*`, `pg_settings`, `pg_database`, table/index stats | **Yes** — Postgres is up or the cluster is down |
| S3 | **Instance-manager metrics** | `GET http://<pod>:9187/metrics` (HTTPS when `spec.monitoring.tls.enabled`) | none on 9187 | the full `cnpg_*` surface: 14 default queries + the Go collector set (`cnpg_collector_*`) + `go_*` | **Yes** — compiled into the instance manager, not a sidecar, not gated on `spec.monitoring` |
| S4 | **Instance-manager status** | `GET https://<pod>:8000/pg/status` | unauthenticated (probes and `/pg/status` need no client cert) | `postgres.PodStatus`: pod name/uid/node, phases, pg data | **Yes** |
| S5 | **Pooler metrics** | `GET http://<pooler-pod>:9127/metrics` | none | `cnpg_pgbouncer_*` (lists, pools, stats) — a surface with **zero** existing dashboards or alerts | Only where a `Pooler` exists (1 of our 4 clusters) |
| S6 | **Pooler runtime** | pgbouncer `SHOW POOLS` / `SHOW CLIENTS` / `SHOW STATS` over 5432 on the pooler | `<cluster>-pooler` secret | the real client→server mapping that fixes attribution | Same |
| S7 | **cAdvisor / node-exporter / metrics-server** | queried *from* Prometheus, or direct | — | container CPU/memory, volume bytes, node metrics for the System Insights OS side | Present in the reference env; **optional** for us |
| S8 | **`kubectl cnpg status -o json`** | CLI | kubeconfig | the typed render of S1 plus live PG rows | Optional; we implement the queries natively rather than shelling out |
| S9 | **`postgres_exporter` sidecar** | its own `/metrics` | — | 36 collectors incl. `stat_statements` | Optional third path, off by default |

**S1 → S2 → S3 is the required spine.** Everything in Phase 1–4 works with only S1 + S2 + S3, all
of which exist on a stock CNPG pod with no `Cluster` patch. S7 is used opportunistically and
degrades gracefully when absent (R5.7). S9 is opt-in and never required.

### 3.3 Collection model

Per cluster, on a configurable interval (default **60 s**, matching Cloud SQL's sampling period):

| Collector | Interval | Source | Notes |
|---|---|---|---|
| `inventory` | watch + 30 s resync | S1 | Keeps the cluster/pod/role map hot; drives every other collector |
| `statement` | 60 s | S2 `pg_stat_statements` | The Query Insights feed; gated on extension presence (R3.1) |
| `activity` | 15 s | S2 `pg_stat_activity` | The Active queries feed; also our wait-event and blocking sample |
| `database` | 60 s | S2 `pg_stat_database`, `pg_database`, `pg_stat_database_conflicts` | |
| `wal` | 60 s | S2 `pg_stat_wal` (14+), `pg_stat_archiver`, `pg_wal` dir via S3 | |
| `io` | 60 s | S2 `pg_stat_io` (16+), `pg_stat_slru` (13+), `pg_stat_bgwriter`/`pg_stat_checkpointer` | Version-gated |
| `replication` | 60 s | S2 `pg_stat_replication`, `pg_replication_slots`; S3 `cnpg_pg_replication_*` | Empty on single-instance clusters (R5.5) |
| `locks` | 30 s | S2 `pg_locks` | Feeds the blocking chain |
| `settings` | 15 min | S2 `pg_settings`, `pg_file_settings`, `pg_extensions` | Slow-changing; also gives `max_connections`, `shared_buffers`, `work_mem` for the gauge set |
| `progress` | 30 s | S2 `pg_stat_progress_*` | Vacuum/analyze/cluster/copy/create-index/basebackup progress |
| `pooler` | 60 s | S5 + S6 | Only for clusters with a Pooler |
| `instance-metrics` | 60 s | S3 (direct or `pods/proxy`) | Parses the `cnpg_*` exposition; the path that needs no SQL credentials |
| `k8s-events` | watch | `events` in the cluster's namespace | Feeds the events timeline |

Every collector is **independently failure-isolated**: one collector erroring (missing extension,
view absent on this major, insufficient grant) degrades that collector only, records the error in
`pgsi_collector_collection_errors_total`, and never blocks the others — the pattern CNPG's own
exporter and postgres_exporter both use.

### 3.4 Storage

| Aspect | Decision | Rationale |
|---|---|---|
| Engine | **SQLite** via `modernc.org/sqlite` (pure Go, no CGO) | Keeps the image distroless-buildable and the chart dependency-free; a single 1 Gi PVC gives 7–30 days of history, which is the Cloud SQL Enterprise/Enterprise-Plus retention story without forcing a Postgres on the product |
| Alternative | `--store=memory` (ring buffers, 24 h window, no PVC) | The lab profile (R11.6) and the fallback when a PVC cannot bind |
| What lives in the store | Statement snapshots (per interval), normalized query text + fingerprints, plan samples, active-session snapshots, collector errors, discovery history | |
| What does **not** | Raw metric samples — those live in Prometheus; we serve them from our registry, not from SQLite | Avoids rebuilding a TSDB |
| Retention | `retention.statementSnapshots: 7d` (default), `retention.activeSessions: 48h`, `retention.plans: 7d`, `retention.maxBytes: 1Gi` with size-based eviction | Cloud SQL parity: 7 d Enterprise / 30 d Enterprise Plus, exposed as a single knob |
| Concurrency | Single-writer, many-reader; WAL mode | Matches one poller + N API readers |
| Scale-out | Sharding by namespace across `--role=collect` instances writing to one store is **not** supported in v1; documented as a v1.x item | Honest about SQLite's single-writer limit |

### 3.5 Source layout

```
pgsi/
├── cmd/pgsi/                 # main; subcommands serve|collect|api|version
├── internal/
│   ├── discovery/            # S1: informers on clusters/poolers/pods/secrets  (R1)
│   ├── kube/                 # rest.Config, clientset, pods/proxy helper        (R1.7)
│   ├── collector/            # collector framework + registry                   (R2)
│   │   ├── pgversion/        # PG 13–18 view/column matrix                      (R2.4)
│   │   ├── statements/       # pg_stat_statements + normalization               (R3)
│   │   ├── activity/         # pg_stat_activity, blocking chain                 (R4)
│   │   ├── database/  wal/  io/  replication/  locks/  settings/  progress/
│   │   ├── cnpgmetrics/      # S3 parser: cnpg_* + cnpg_collector_*             (R2.6)
│   │   └── pgbouncer/        # S5 + S6                                          (R6)
│   ├── store/                # sqlite + memory implementations, retention       (§3.4)
│   ├── metrics/              # MetricHub: our registry, label contract, top-N   (R9)
│   ├── api/                  # REST handlers, PromQL pair, auth, OpenAPI        (R8)
│   ├── web/                  # go:embed of the built SPA                        (R10.1)
│   └── config/               # flags + ConfigMap + values precedence            (R11.8)
├── web/                      # React + Vite + TS SPA                            (R10)
├── chart/                    # Helm chart: deployment, rbac, svc, servicemonitor,
│                             # prometheusrule, configmap, hpa-less                (R11)
├── deploy/                   # kustomize + plain-YAML alternatives               (R11.9)
├── docs/                     # ENVIRONMENT.md, research/, runbooks, site         (R15)
├── testdata/                 # recorded cnpg_* exposition samples, fixture SQL   (R16.3)
├── requirements.md           # this file
└── IMPLEMENTATION.md         # the handoff file (R14)
```

---

## 4. Requirements

Format: `Rn.m — statement`. Requirements marked **[A]** have an acceptance test in §8 that is
executable against the reference environment; unmarked ones are verified by review or unit test.

### R1 — Discovery

| ID | Requirement |
|---|---|
| R1.1 | Discover `Cluster` objects in all namespaces visible to the configured RBAC, via a watch with a 30 s resync; support `--namespaces=a,b,c` and `--exclude-namespaces` allow/deny lists. |
| R1.2 | For each cluster, resolve its instances by the label selector `cnpg.io/cluster=<name>,cnpg.io/podRole=instance` and read role from `cnpg.io/instanceRole` (`primary`/`replica`/`unhealthy`), falling back to the deprecated `role` label when only that is present. |
| R1.3 | Derive the PG major version from the `app.kubernetes.io/version` pod label, avoiding a `SELECT version()` per poll; fall back to SQL `current_setting('server_version_num')` when the label is absent, and cache it per cluster. |
| R1.4 | Discover `Pooler` objects and their pods (`cnpg.io/podRole=pooler`), and associate each to its `spec.cluster.name`. |
| R1.5 | Expose the inventory at `GET /api/v1/clusters` with, per cluster: namespace, name, phase, phaseReason, instances, readyInstances, currentPrimary, PG major, instance list with roles and node names, conditions, cert expirations, PVC list, services, and any Poolers. |
| R1.6 | **Zero-prerequisite rule:** discovery must succeed and produce a renderable inventory on a cluster where `spec.monitoring` is unset, no Pooler exists, and no credentials have been provided. **[A]** |
| R1.7 | Support two HTTP transports to pod endpoints and select automatically: direct pod-IP/Service URL, and the Kubernetes API `pods/proxy` subresource (`/api/v1/namespaces/<ns>/pods/<scheme>:<pod>:<port>/proxy/<path>`), the latter selected automatically when direct addressing fails or `--use-proxy=true`. |
| R1.8 | Read credentials in this precedence: explicit `--credential-secret` reference → cluster-secret convention (`<cluster>-app`, keys `username`/`password`/`dbname`) → a configured single credential → none (SQL collectors report `unconfigured`, non-SQL collectors still work). |
| R1.9 | React to cluster deletion within one resync by dropping its collectors and labelling its series stale rather than deleting them immediately (a 15 min grace prevents churn-induced gaps in Prometheus). |

### R2 — Collection framework

| ID | Requirement |
|---|---|
| R2.1 | Provide a collector framework where each collector declares: name, interval, required source, PG-version applicability, and cost class; the scheduler runs them independently with per-collector timeouts (default 10 s) and per-cluster concurrency limits (default 4). |
| R2.2 | Every collector run emits `pgsi_collector_duration_seconds{collector}` and increments `pgsi_collector_errors_total{collector,cluster,namespace}`; a failed collector leaves its last-good data served and marks it with an age. |
| R2.3 | Support `--interval.<collector>=` overrides and `--disable-collector=a,b` so a lab can run with two collectors, and a production user can disable an expensive one. |
| R2.4 | Maintain a **PG 13–18 capability matrix** as data, not as scattered `if` statements: which views exist per major, which columns exist per major, and the renames (`total_time`→`total_exec_time` at PG13; `pg_stat_bgwriter`→`pg_stat_checkpointer` at PG17; `blk_read_time`/`blk_write_time` removed from `pg_stat_statements` at PG17 in favour of `shared_blk_read_time`/`shared_blk_write_time`; `op_bytes` removed from `pg_stat_io` at PG18; `pg_stat_wal` shrinks 9→5 columns at PG18). Every SQL collector resolves its query through this matrix. **[A]** |
| R2.5 | Schema-qualify every catalog reference (`pg_catalog.*`, `pg_stat_statements.…`) in all generated SQL, matching the CNPG 1.29.1 hardening and immune to `search_path` shadowing. |
| R2.6 | Parse the `cnpg_*` / `cnpg_collector_*` exposition from S3 into typed structs (not a generic Prometheus scrape passthrough), so we can serve the same numbers through the API and re-export them under our own prefix. |
| R2.7 | Run every SQL collector inside a single read-only transaction per view group, and honour `statement_timeout` from config (default 5 s) so a sick instance cannot wedge the poller. |
| R2.8 | Exclude our own sessions from every count we report, by setting `application_name=pgsi-collector` and filtering it out of `pg_stat_activity`-derived metrics (the postgres_exporter `--collector.stat_statements.exclude_users` pattern, applied by us at query level). |

### R3 — Query Insights

| ID | Requirement |
|---|---|
| R3.1 | Detect `pg_stat_statements` presence per database (from `pg_extension` via S2 and from `cnpg_pg_extensions_*` via S3) and report it per cluster; when absent, the cluster renders a clear "not installed" state with the exact remediation (`CREATE EXTENSION pg_stat_statements;` plus the `shared_preload_libraries` requirement and the CNPG `spec.parameters` snippet) instead of an empty screen. |
| R3.2 | Compute the **top-N queries** per cluster/database, ranked by `total_exec_time`, with the Cloud SQL column set reproduced exactly: `Query`, `Database`, `Recommendations`, `Load by total time`, `Load by CPU`, `Load by IO wait`, `Load by lock wait`, `% load by Queries`, `Avg execution time (ms)`, `Total execution time (ms)`, `Avg rows returned`, `Times called`. **[A]** |
| R3.3 | Compute deltas correctly: `pg_stat_statements` is cumulative, so every rate is `current − previous snapshot`, with the snapshot's `pg_stat_statements_info.stats_reset` (PG14+) checked — a reset must reset the baseline rather than produce a negative or wrapped rate. On PG13 (no `pg_stat_statements_info`) fall back to detecting counter decreases per row. |
| R3.4 | **Fingerprinting.** Key the query store on PostgreSQL's own `queryid`, and additionally compute our own normalized fingerprint (literal-stripped, whitespace-collapsed, keyword-cased template). Reasons, all real: (a) PG18 changes query-id jumbling, so ids change for unchanged queries across a 17→18 upgrade; (b) we need displayable text; (c) we need cross-database grouping. Store the template plus the first-seen raw text truncated per R3.6. |
| R3.5 | Mirror Cloud SQL's normalization rules for the stored template: literals → `$1`,`$2`,… for PG 11+ and `?` for PG ≤10, matching what Cloud SQL writes into `querystring`. |
| R3.6 | **Query text length** configurable 256–4500 bytes, default **1024** — Cloud SQL's exact `queryStringLength` range and default — with longer text truncated and the truncation flagged. |
| R3.7 | Provide **per-dimension rollups** matching Cloud SQL's tabs: by database, by user, by application name, by client address. Each rollup answers "which X accounts for the most load" with the same load columns as R3.2. |
| R3.8 | Provide **per-query detail**: the per-minute load history for that query (Cloud SQL's "Database load — specific query", minute-boundary aggregation), latency percentiles, plan samples, and the derived recommendations. |
| R3.9 | **Latency percentiles.** Cloud SQL serves P50/P95/P99 from a DISTRIBUTION metric. `pg_stat_statements` gives min/mean/max/stddev, not a distribution. v1 serves min/mean/max/stddev plus a **sampled** histogram built from the `activity` collector's per-session durations (R4), exposed as a native Prometheus histogram for the top-N. Marked **Divergence**: exact P95/P99 per query requires per-execution sampling Cloud SQL itself only gets in Enterprise Plus. |
| R3.10 | **Plan samples.** Capture `EXPLAIN (FORMAT JSON)` for top-N queries on a bounded schedule (default 5 plans/minute/cluster, Cloud SQL's `queryPlansPerMinute` default; Enterprise Plus allows 200 — we expose the knob and default to the cheap value). `EXPLAIN ANALYZE` is **never** run automatically (it executes the statement); it is available on explicit user request, and only for `SELECT`s unless `--allow-analyze-write=true`. |
| R3.11 | Detect **plan changes**: keep plan hashes per fingerprint and surface a plan-change event when the hash changes for a stable fingerprint — the regression detection Cloud SQL gets from `auto_explain` and we get from bounded sampling. |
| R3.12 | **Tags.** Cloud SQL's `pertag` metrics carry `action, application, controller, db_driver, framework, route, tag_hash`, sourced from app-set query tags. Generic PostgreSQL has no tag mechanism; our answer is (a) `application_name`, `usename`, `datname` as the always-available dimensions, and (b) an opt-in documented convention — a structured SQL comment `/* pgsio:action=…,route=…,controller=… */` that we parse out of the statement text and promote to tag dimensions with a `tag_hash`. **Divergence**, documented. |
| R3.13 | Respect `recordClientAddress` and `recordApplicationTags` equivalents (`--record-client-address`, `--application-tag-source=application_name|query_comment|off`) so teams with IP-privacy requirements can turn addresses off without losing the rest. |
| R3.14 | **Historical query store**: retain statement snapshots for `retention.statementSnapshots` (default 7 d) and answer week-over-week "is this query slower than last week" from it. |

### R4 — Active queries and locks

| ID | Requirement |
|---|---|
| R4.1 | Provide a live session list matching Cloud SQL's **top 50**, including both `active` and `idle in transaction` sessions, with the exact Cloud SQL columns: `Process ID`, `Query`, `Status`, `Session duration`, `Transaction duration`, `Query duration`, `Wait event type`, `Wait event`, `Database`, `Application name`, `User name`, `Client address`, `Action`. **[A]** |
| R4.2 | Refresh the live list at 15 s (Cloud SQL refreshes at 60 s; we can afford to be fresher because the query is cheap) while keeping the 60 s server-side snapshot for history. |
| R4.3 | **Wait-event analysis**: roll up `wait_event_type` × `wait_event` counts (Cloud SQL's "Wait event types" and "Wait events" tabs). CNPG today exposes only a count (`cnpg_backends_waiting_total`) with no breakdown, so this is net-new. Use `pg_wait_events` (PG17+) to validate and label event names on versions that have it. |
| R4.4 | **Blocking chain**: from `pg_locks` + `pg_stat_activity`, build the blocker→blockee graph and render the chain up to **3 levels deep**, matching Cloud SQL's blocked-query investigation tree, with the blocking statement text shown for each ancestor. |
| R4.5 | **Terminate session**: `POST /api/v1/sessions/{pid}/terminate` → `pg_terminate_backend(pid)`, gated behind `--allow-actions=true` (default **false**), requiring a confirmed UI action, audited to a log line with actor and target, and refused on a replica. This is Cloud SQL's Enterprise-Plus-only `Action → Terminate connection`. |
| R4.6 | Report idle-in-transaction age separately from active duration, and surface `backend_type` so autovacuum/parallel/walsender sessions are distinguishable from client backends (PG18's new `starting` state must render, not error). |
| R4.7 | Show per-session `query_id` and link it to the R3 query detail when the id is known. |

### R5 — System Insights

| ID | Requirement |
|---|---|
| R5.1 | Provide the Cloud SQL summary-card set, computed per cluster: **CPU utilization P99 \| P50**, **Peak connections**, **Transaction ID utilization**, **Disk utilization**, **Log errors**. **[A]** |
| R5.2 | Provide the DB metric families: transactions (commit/rollback/TPS), tuples (returned/fetched/inserted/updated/deleted), blocks (hit/read, cache hit ratio), temp (files/bytes), deadlocks, backends by state and by application name, database sizes, TXID and MXID age, WAL volume and generation rate, checkpoints. |
| R5.3 | Provide the OS metric families (CPU, memory, disk, network) from the container/pod perspective, sourced from S7 when Prometheus is configured, from `metrics.k8s.io` when only that exists, and degraded-with-a-note when neither does. Never render a blank chart without saying why. |
| R5.4 | **Database-count guard**: Cloud SQL collapses to a single aggregated value above 500 databases and only shows `tuple_size` below 50. Adopt both thresholds as configurable limits, because a 1000-database cluster would otherwise explode both our store and our UI. |
| R5.5 | Handle the degenerate paths honestly: single-instance clusters render an empty replication section with an explanation (not a broken chart) — the `cookbook` cluster is exactly this case. |
| R5.6 | **Chart toolbar** parity: drag-to-zoom a time range, **Download PNG**, **Download CSV**, and "View in Prometheus/Grafana" deep link built from the panel's PromQL. Cloud SQL offers exactly these three; we match them per chart, not per page. |
| R5.7 | **Compare up to 5 instances** on one chart (Cloud SQL parity), which requires our series to carry stable instance identity — see the label contract in R9.3. |
| R5.8 | Retain and serve 30 days of system metrics (from Prometheus when configured, from the store otherwise) with a 1-minute auto-refresh, matching Cloud SQL's stated retention and refresh. |
| R5.9 | Surface **events**: Cloud SQL's events timeline plus CNPG's own signal set — phase transitions from S1, Kubernetes events, fencing changes (`cnpg.io/fencedInstances` annotation), promotions (`currentPrimaryTimestamp`), and backup outcomes — as one timeline per cluster. |

### R6 — Pooler (connection pooling) insights

| ID | Requirement |
|---|---|
| R6.1 | Discover Poolers and render their pool state from S5 (`cnpg_pgbouncer_pools_*`: `cl_active`, `cl_waiting`, `sv_active`, `sv_idle`, `sv_used`, `sv_tested`, `sv_login`, `cl_cancel_req`, …) and S5 lists (`cnpg_pgbouncer_lists_*`). This surface currently has **zero** Grafana panels and **zero** alert rules in the CNPG ecosystem. |
| R6.2 | Render pool utilisation: clients waiting vs. servers active per (database, user), the client/server ratio, and pool saturation (`cl_waiting` sustained > 0) as the headline number. |
| R6.3 | Render throughput from `cnpg_pgbouncer_stats_*`: `total_query_count`, `total_xact_count`, `total_received`, `total_sent`, `total_wait_time`, `avg_*` variants, with the `stats_reset` counter-reset handling from R3.3. |
| R6.4 | **Attribution fix.** Cloud SQL's documented answer for pgbouncer is to *disable* client-IP attribution rather than mis-attribute it (IPs render as `local` on ports 3307/6432). We do better: read `SHOW CLIENTS` from the pooler (S6) to obtain the real client address→server-backend mapping, and label queries that arrived through a pooler with the pooler name plus the pgbouncer-side client address, while marking the Postgres-side `client_addr` as pooler-derived. Config: `--pooler-attribution=off|cloudsql|pgbouncer` (default `pgbouncer`, with `cloudsql` reproducing Google's conservative behaviour). |
| R6.5 | Surface pooler config (`poolMode`, `default_pool_size`, `max_client_conn`, `min_pool_size`, `type`) alongside the utilisation so a mis-sized pool is visible next to its symptom. |

### R7 — Recommenders

| ID | Requirement |
|---|---|
| R7.1 | **Index recommendations**: derive candidate indexes from `pg_stat_statements` (predicates and join columns parsed out of the stored statement text) cross-checked against `pg_index`/`pg_stat_user_indexes` usage, ranked by estimated load addressed. This is the Enterprise-Plus index advisor, rebuilt from generic sources; it is a *candidate list with reasoning*, and the spec is explicit that it is weaker than Google's, which has the full plan. |
| R7.2 | **Table and index bloat**: per-table dead-tuple ratio from `pg_stat_user_tables` plus size from `pg_total_relation_size`; unused-index detection from `pg_stat_user_indexes.idx_scan = 0` with size. |
| R7.3 | **Rightsizing**: requested CPU/memory (from `cnpg.io/podSpec` annotation / pod spec) vs. actual usage, with a recommendation direction. Cloud SQL has a Recommender product here; the CNPG dashboard has two gauges and no recommendation. |
| R7.4 | **Autovacuum insight**: last/next vacuum and analyze per table, tables over the autovacuum threshold, and tables never analyzed — a gap in the existing dashboard. |
| R7.5 | **TXID wraparound**: per-database age with the 1.5 B warn / 1 B critical bands, and the `LongRunningTransaction`/`PGDatabaseXidAge` thresholds surfaced as findings, not just as Prometheus alerts. |
| R7.6 | Every recommendation carries: the finding, the evidence (metrics + query fingerprints), the suggested action, and a `confidence` field. Recommendations the data cannot support are suppressed, not guessed. |

### R8 — HTTP API

| ID | Requirement |
|---|---|
| R8.1 | Versioned REST API under `/api/v1/*`, JSON, with a committed OpenAPI document (`api/openapi.yaml`) generated from the handlers and checked in CI. |
| R8.2 | Core endpoints: `/clusters`, `/clusters/{ns}/{name}`, `/insights/queries`, `/insights/queries/{fingerprint}`, `/insights/queries/{fingerprint}/plans`, `/insights/wait-events`, `/insights/databases`, `/insights/users`, `/insights/tags`, `/insights/system`, `/insights/system/{metric}`, `/active-queries`, `/insights/recommendations`, `/poolers`, `/poolers/{ns}/{name}`, `/events`, `/explain`. |
| R8.3 | **The PromQL pair**, mirroring Cloud SQL's Database Insights MCP API shape (`FetchQueryMetricsRequest{parent, resource, promqlQuery, startTime, endTime}` → `{promqlResult}`): `GET|POST /api/v1/metrics/query` and `/api/v1/metrics/system`, each accepting a PromQL string + time range and returning a Prometheus-shaped result. When a Prometheus URL is configured, the query is proxied to it; otherwise it is answered from our own store. This is what lets an agent or a script ask the same question the UI asks. |
| R8.4 | Every list endpoint supports `?namespace=`, `?cluster=`, `?db=`, `?from=`, `?to=`, `?limit=`, `?sort=`, and returns a stable envelope `{data, meta:{total, truncated, generatedAt}}`. |
| R8.5 | Consistent error body `{error:{code, message, details[]}}` with codes in a documented enum, modelled on the CNPG instance-manager envelope so the ecosystem's error-reading code transfers. |
| R8.6 | Serve the SPA from the same binary via `go:embed`, with `/` → `index.html`, correct asset caching, and `--serve-ui=false` for API-only deployments. |
| R8.7 | `GET /healthz` (process), `GET /readyz` (true once discovery has produced at least one inventory sync), `GET /debug/pprof/*`. |
| R8.8 | Auth: `--auth=none|token|kube-rbac-proxy`. Default `token` with a bearer token from a Secret; `none` for lab. The UI and `/metrics` may have separate auth (`--auth-metrics=none` so Prometheus need not hold a token). |
| R8.9 | CORS configurable; `/metrics` and `/healthz` never require auth when `--auth-metrics=none`. |

### R9 — Prometheus metrics export (the multi-instance centralization requirement)

| ID | Requirement |
|---|---|
| R9.1 | **The two-plane rule.** Every capability is tagged with the plane that serves it, and this is documented in the chart README: **basic metrics → `/metrics` → Prometheus**; **query text, plan bodies, active-session rows, blocking chains → API + store only**. Metrics carry the numbers; the API carries the text. This is the direct answer to "some features won't make sense to fall back that way". |
| R9.2 | Prefix all exported metrics `pgsi_` (configurable `metrics.prefix`) to avoid collision with `cnpg_`, `pg_`, and `cAdvisor` families in a shared Prometheus. |
| R9.3 | **Label contract** (normative, so dashboards survive multi-instance scraping): every series carries `namespace`, `cluster`, `pod` (instance), and `instance_role`; pooler series carry `pooler`; database series add `datname`; user series add `usename`. Instance identity is `(namespace, cluster, pod)` and is stable — the same triple R5.7 needs for compare-up-to-5. |
| R9.4 | Export the **aggregate** family (Cloud SQL's `insights/aggregate` equivalent): execution time, rows, shared block hits/reads, per (cluster, db, user) — bounded cardinality, always on. |
| R9.5 | Export the **per-query** family (Cloud SQL's `insights/perquery`) for the **top-N only**: default **N=50**, range 0–1000, `queryid` as the label and the text served from the API. N=0 disables the family. This mirrors postgres_exporter's `--collector.stat_statements.limit` (default 100) as the cardinality lever. |
| R9.6 | Export the **per-tag** family (Cloud SQL's `insights/pertag`) when R3.12 tags are enabled, keyed by `tag_hash`. |
| R9.7 | Export the **system** family: the full R5.2 set, plus the parsed `cnpg_*` values re-exported under `pgsi_` so a user without CNPG's PodMonitor still gets one consistent namespace. |
| R9.8 | Export **self-observability**: `pgsi_build_info{version,commit}`, `pgsi_collector_duration_seconds`, `pgsi_collector_errors_total`, `pgsi_scrape_target_up{cluster,pod}`, `pgsi_store_size_bytes`, `pgsi_http_request_duration_seconds`. |
| R9.9 | Ship a **ServiceMonitor** (and a **PodMonitor** variant) in the chart, off by default and enabled by `metrics.serviceMonitor.enabled`, correct for the installed `monitoring.coreos.com/v1` API — validated against the reference environment's kube-prometheus-stack. **[A]** |
| R9.9b | Ship a **PrometheusRule** in the chart with a `critical` tier, which CNPG's own `cnpg-default-alerts` lacks (all 7 of its rules are `warning`). Ours ships ≥10 rules across both severities, each with `summary`/`description`/`runbook_url` annotations — the runbook linkage gap from the CNPG alert set. |
| R9.10 | Document the cardinality budget (§5) in the chart README with a measured number for the reference environment, so an adopter can predict their Prometheus bill before installing. |

### R10 — Web UI and visualizations

| ID | Requirement |
|---|---|
| R10.1 | Single-page app (React + TypeScript + Vite), built and embedded into the Go binary via `go:embed`; no separate web server, no CDN dependency, works fully offline inside the cluster. |
| R10.2 | **Chart library and design system are chosen by the `dataviz` skill**, which must be read before any chart code is written, and its choice recorded in `IMPLEMENTATION.md`. This spec deliberately does not name a library so the choice is made against current guidance rather than habit. |
| R10.3 | **Pages**: Fleet, Cluster Overview, Queries, Query Detail, Active Queries, Wait Events, System Insights, Poolers, Recommendations, Events Timeline. Each maps to a requirement above (R10.4–R10.11). |
| R10.4 | **Fleet**: health distribution (one stacked bar + counts), a sortable table of every cluster with phase, instances, PG major, TPS, p95 latency, connections %, disk %, and the top finding; click-through to any cluster. This is the biggest structural gap in the CNPG ecosystem today (the existing surface is a three-level dropdown). |
| R10.5 | **Cluster Overview**: the Cloud SQL "Database load" graph — stacked area of `CPU capacity`, `CPU and CPU Wait`, `IO Wait`, `Lock Wait` — plus the R5.1 summary cards, a replication topology view (primary→standby with per-standby lag, not four separate lag lines), and instance chips with roles. |
| R10.6 | **Queries**: the R3.2 table with sparklines per row, inline load bars, filter chips (database, user, tag), search over normalized text, and a saved-view concept. Sorting must be by any column with server-side pagination. |
| R10.7 | **Query Detail**: header (fingerprint, database, user, first-seen), the per-minute load chart for that query, latency panel (P50/P95/P99 or the R3.9 fallback), plan samples list with a JSON/plan viewer, and recommendations. |
| R10.8 | **Active Queries**: live table (R4.1) with a 15 s auto-refresh indicator, row expansion to the blocking chain, and a Terminate action behind the R4.5 gate. |
| R10.9 | **Wait Events**: donut/bar by `wait_event_type` with a drill to per-event counts and the sessions currently in each. |
| R10.10 | **System Insights**: metric-group tabs, each chart with the R5.6 toolbar (zoom / PNG / CSV / external link), and the R5.7 5-instance comparison selector. |
| R10.11 | **Poolers**: pool utilisation and stats charts per pooler with the R6.5 config panel beside them. |
| R10.12 | **Empty and error states are designed, not defaulted.** Every panel defines its empty, loading, partial (some collectors failed), and unavailable (source not configured) state. The reference environment guarantees we hit all four: a Pending replica, a single-instance cluster, a cluster with no Pooler, and no `pg_stat_statements` by default. |
| R10.13 | Responsive to 1280 px; dark and light themes; keyboard navigation for the tables; no chart renders as a blank box without a one-line explanation. |
| R10.14 | The initial page load must render from cached API data in **< 1.5 s** on-cluster (R13.3), which requires the API to serve pre-aggregated rollups rather than making the browser compute them. |

### R11 — Packaging and deployment

| ID | Requirement |
|---|---|
| R11.1 | One image contains the API, the collector, and the UI. |
| R11.2 | Dockerfile builds **multi-arch `linux/arm64` + `linux/amd64`** (the dev machine is arm64; the brief's adopters are mixed), via `docker buildx`, from a multi-stage build whose final stage is **distroless static** (or `FROM scratch` with CA certs), non-root uid 65532, no shell, no libc in the app path (pure-Go SQLite, R3/§3.4). |
| R11.3 | Image labelled with `org.opencontainers.image.{source,revision,version}`; version string injected via `-ldflags` and exposed at `/api/v1/version` and `pgsi_build_info`. |
| R11.4 | Image size target **< 60 MB** compressed (a Go binary + embedded SPA); CI fails the build above 100 MB. |
| R11.5 | Pin base digests, not tags; Dependabot/Renovate config present. |
| R11.6 | **Helm chart** as the primary artifact, with two documented profiles from one chart: `values-production.yaml` (PVC-backed store, token auth, ServiceMonitor on, resource requests/limits, 1 replica) and `values-lab.yaml` (in-memory store, auth off, ServiceMonitor off, `--single-namespace` capable). Plus a `deploy/plain-yaml/` set for non-Helm users and a `deploy/kustomize/` set. **[A]** |
| R11.7 | Chart ships: Deployment, Service, ServiceAccount, Role/RoleBinding (namespaced) with an `--all-namespaces` ClusterRole variant, ConfigMap, ServiceMonitor, PodMonitor variant, PrometheusRule, ingress, networkpolicy, PDB-optional, and `.helmignore` + `helm lint` clean. |
| R11.8 | Config precedence: CLI flags > `PGSI_*` env > mounted ConfigMap > built-in defaults, documented per flag; `pgsi config dump` prints the resolved config for debugging. |
| R11.9 | **Install in < 5 minutes** with a copy-pasteable two-command quickstart (`helm repo add` + `helm install`) that works against a stock CNPG install with no other prerequisites — the P5 lab job, made literal. |
| R11.10 | Provide an **uninstall** story: `helm uninstall` leaves no cluster-scoped residue (ClusterRole/Binding gated behind the same flag that creates them). |
| R11.11 | CI: build, lint (`golangci-lint`, `helm lint`, `helm template` snapshot test), unit tests with ≥70 % coverage on `internal/…`, multi-arch image build on tag, and a **render test** that `helm template`s the chart with both profiles and validates the output against the k8s schema. |

### R12 — Security and RBAC

| ID | Requirement |
|---|---|
| R12.1 | Ship a **least-privilege namespaced Role** as the default: read on the CNPG resources + their `/status`, read on pods/services/configmaps/events/secrets, `pods/proxy:create`, read on deployments/jobs/pdb/endpointslices/podmonitors. Derived from and consistent with CNPG's own documented per-command RBAC. |
| R12.2 | `pods/exec` is **not** requested by default (it is effectively code execution in the pod namespace); it is opt-in via `rbac.allowPodsExec=true`. |
| R12.3 | Cluster-scoped RBAC is created only when `rbac.allNamespaces=true`; the default install is namespaced. |
| R12.4 | **Postgres-side privilege model**: prefer a dedicated `pg_monitor`-granted role (the `cnpg_metrics_exporter` pattern from the CNPG CVE-2026-44477 fix) over consuming `<cluster>-app`; document the exact `CREATE ROLE`/`GRANT` statements; support reading `<cluster>-app` as the zero-config path; and never connect as the superuser. |
| R12.5 | TLS to Postgres configurable (`sslmode` default `require` when the cluster has a `-server` secret we can read, else `prefer`); TLS on our own HTTP endpoint via a cert secret or cert-manager annotation. |
| R12.6 | Never log query parameters by default (only the normalized template, R3.5), so credentials inside SQL literals cannot leak into logs or into the store. |
| R12.7 | Container hardening: `readOnlyRootFilesystem` (with writable `/tmp` + data volume), `allowPrivilegeEscalation: false`, `capabilities: drop [ALL]`, seccomp `RuntimeDefault`, and a `PodSecurityStandard: restricted`-compliant pod spec. |
| R12.8 | The `--allow-actions` gate (R4.5) is off by default so a read-only product cannot become a mutation surface by surprise. |
| R12.9 | A documented threat model section: what the token grants, what a leaked `<cluster>-app` password grants, and what `pods/proxy:create` grants (read pod HTTP endpoints, not pod contents). |

### R13 — Non-functional

| ID | Requirement |
|---|---|
| R13.1 | **Per-cluster poll cost budget**: one cluster's full 60 s collection round completes in **< 3 s** and issues **< 40 SQL round-trips** on a healthy instance; measured and reported via `pgsi_collector_duration_seconds`. |
| R13.2 | **Fleet scaling**: 50 clusters on one poller with **< 1 % CPU per cluster** and < 150 MB RSS baseline; per-cluster concurrency cap (R2.1) prevents a slow cluster from delaying others. |
| R13.3 | API p50 **< 100 ms** for cached endpoints, p95 **< 400 ms**; first UI paint **< 1.5 s** (R10.14). |
| R13.4 | **Cardinality budget** per §5, with a CI-checkable assertion that the exported series count for a fixture cluster set stays under the documented bound. |
| R13.5 | Memory ceiling configurable and enforced (`--max-store-bytes`), with size-based eviction rather than OOM. |
| R13.6 | **Lab mode**: runs against a single namespace with no PVC, no auth, no ServiceMonitor, in < 100 MB RAM — the P5 path must work on a kind/minikube cluster. |
| R13.7 | Structured logging (JSON, level-configurable, `--log-field-*` compatible with the CNPG/machinery log flag shape so log pipelines treat us like CNPG), plus `--log-level=debug` that dumps the SQL executed per collector. |
| R13.8 | Graceful shutdown: drain in-flight scrapes, flush the store, exit within the terminationGracePeriod. |
| R13.9 | **Compatibility matrix** as a tested artifact: CNPG 1.28–1.30, PG 13–18, Kubernetes 1.29–1.37, kube-prometheus-stack 0.7x+. Each cell is either tested, fixture-tested, or explicitly marked untested in the README — no silent claims. |
| R13.10 | Go 1.26+, no cgo, no `replace` directives, `go.mod` tidy in CI. |

### R14 — Documentation and handoff

| ID | Requirement |
|---|---|
| R14.1 | **`IMPLEMENTATION.md` is the handoff file** and is created in Phase 0, before any other code, and updated in the **same commit** as every phase's code. It contains: the work-breakdown table with per-task status, the current phase and its state, deviations from this spec with reasons, decisions log, known gaps, and "how to run this right now". |
| R14.2 | Every phase ends with a **validation entry** in `IMPLEMENTATION.md`: what was checked against which requirement IDs, what passed, what deviated, and the fix or the follow-up. This is the mechanism the brief asks for with "validate each phase against the plan and specification… if there are deviations, fix them." |
| R14.3 | `README.md` with: what it is, a GIF/screenshot, the two-command quickstart, a capability matrix vs Cloud SQL, the metrics catalogue, and a link to this file. |
| R14.4 | `docs/` holds this spec, the two research documents, `ENVIRONMENT.md`, per-feature runbooks, and an architecture doc with the diagram from §3.1. |
| R14.5 | `CONTRIBUTING.md` with the dev loop (build, test, run locally against a kind cluster), code layout map, and the release process. |
| R14.6 | `CHANGELOG.md` maintained from Phase 1 onward (not retro-fitted at release). |
| R14.7 | Every exported metric and every API endpoint is documented in one place (R9.10, R8.1) — no metric or route that exists in code but not in docs. |

### R15 — Licensing and distribution *(added in review round 1)*

| ID | Requirement |
|---|---|
| R15.1 | License chosen and committed before the first public release; the spec's working assumption is **Apache-2.0** (matching CNPG and postgres_exporter, and permissive for adopters), recorded as a decision in `IMPLEMENTATION.md` rather than left implicit. |
| R15.2 | Distribution artifacts named and versioned from day one: OCI image tag scheme (`ghcr.io/<org>/pgsql-insights:vX.Y.Z` + `:latest`), Helm chart version vs. appVersion policy, and a GitHub Release per tag with the SBOM attached. |
| R15.3 | Third-party license audit (`go-licenses` + the SPA's license set) committed as `docs/licenses/` and regenerated in CI. |

### R16 — Testing strategy *(added in review round 1)*

| ID | Requirement |
|---|---|
| R16.1 | Unit tests for: the PG version matrix (R2.4) across all six majors, the SQL normalizer (R3.5) against a fixture corpus, the `cnpg_*` exposition parser (R2.6), counter-reset handling (R3.3), and the label contract builder (R9.3). |
| R16.2 | Integration tests against a real Postgres (testcontainers or a kind-hosted CNPG) for the SQL collectors on PG 16 and PG 17 — the two majors in the reference environment — asserting the version-branched SQL actually executes. |
| R16.3 | **Recorded fixtures**: the `cnpg_*` and `cnpg_pgbouncer_*` exposition text captured from the reference environment into `testdata/`, so the parser is tested against real output rather than a hand-written sample, and so regressions are detectable offline. |
| R16.4 | Golden tests for the Helm chart (`helm template` output committed for both profiles). |
| R16.5 | A **live validation script** (`scripts/validate-env.sh`) that runs the §8 acceptance checks against the reference environment and prints a pass/fail table; this is what makes "[A]" requirements actually checkable. |

---

## 5. Cardinality and retention budget

This is the section that decides whether the product is adoptable. Cloud SQL bounds it explicitly
(top 100 queries/day on Enterprise, top 4 000/minute on Enterprise Plus, 4 500-byte query text cap,
~36 GB at 7 d / ~155 GB at 30 d on instance disk) and so must we.

| Family | Labels | Series per cluster | Control |
|---|---|---|---|
| Aggregate (R9.4) | `namespace,cluster,pod,instance_role,datname,usename` | ~`databases × users`, capped by the R5.4 500-database guard; measured **~1.2 k** for the reference env | Always on |
| Per-query (R9.5) | `…,queryid` | **N × databases**, N default 50 | `metrics.topN` 0–1000; N=0 disables |
| Per-tag (R9.6) | `…,tag_hash` | tags × databases | Off unless R3.12 enabled |
| System (R9.7) | `namespace,cluster,pod,instance_role` + family-specific | ~**4 k** | Always on |
| Pooler (R6.1) | `…,pooler,database,user` | ~`pools × 12` | Only where a Pooler exists |
| Self (R9.8) | — | ~120 | Always on |

**Reference-environment estimate:** 4 clusters ≈ **~22 k series** with `topN=50`, of which ~14 k
are the per-query family. At ~1.5 bytes/sample after compression and a 15 s scrape, that is
~2.6 GB of Prometheus storage over 30 days — which is the number R9.10 makes the chart README
promise, and the reason `topN` defaults to 50 rather than to "everything".

**Deliberate divergences from Cloud SQL's limits, and why:**

| Cloud SQL | Us | Why |
|---|---|---|
| 4 500-byte query text (Enterprise Plus) | 1 024 default, 256–4 500 range | Matches Enterprise's default exactly and gives the Enterprise-Plus ceiling as a knob; 4 500 B × 4 000 queries is the cost Google absorbs on instance disk, and we ship it as opt-in rather than as the default |
| 200 plan samples/min (E+) | 5/min default, knob up to 200 | `EXPLAIN` costs planner CPU on the real database; Google's 200/min is for EEP instances that pay for it. Default = Enterprise's 5, ceiling = EEP's 200 |
| 30-day retention (E+) | 7 d default, 30 d available | 7 d is Enterprise's number and fits a 1 Gi PVC; 30 d is one config change |
| Per-query latency DISTRIBUTION | min/mean/max/stddev + sampled histogram | PostgreSQL has no per-query latency distribution; R3.9 |
| Per-query `lock_time` with `lock_type` | Not available per query | `pg_stat_statements` has no lock-time column. We derive lock wait at database/user level from `pg_locks` + `pg_stat_activity` and label it as such rather than faking a per-query number |

---

## 6. Parity matrix

Every Cloud SQL capability, its source in Google's product, our requirement ID, and the phase that
delivers it. (Derived from `cloud-sql-insights.md` §8.)

| Cloud SQL capability | Google's source | Our source | Req | Phase |
|---|---|---|---|---|
| Query list w/ load columns | `insights/perquery` metrics | `pg_stat_statements` | R3.2 | 3 |
| Query text + fingerprint | `querystring`, `query_hash` | `queryid` + normalizer | R3.4–R3.6 | 3 |
| Query detail: load graph | per-minute aggregation | statement snapshots @60 s | R3.8 | 3 |
| Query detail: plan samples | `auto_explain` + sampled EXPLAIN | bounded `EXPLAIN (FORMAT JSON)` | R3.10–R3.11 | 3→6 |
| Query detail: P50/P95/P99 | DISTRIBUTION metric | min/mean/max/stddev + sampled | R3.9 | 6 |
| Wait event types / wait events | `insights` wait metrics | `pg_stat_activity` + `pg_wait_events` | R4.3 | 6 |
| Databases tab | `insights` by db | `pg_stat_database`, `pg_database` | R3.7, R5.2 | 2 |
| Users tab | `insights` by user | `pg_stat_activity`, `pg_stat_statements` | R3.7 | 3 |
| Tags tab | app-set query tags | `application_name` + `/* pgsio: */` convention | R3.12 | 6 |
| Client addresses tab | `client_addr` label | `pg_stat_activity.client_addr` + pooler fix | R3.13, R6.4 | 3, 8 |
| Active queries (top 50) | live query | `pg_stat_activity` | R4.1–R4.2 | 6 |
| Blocked-query tree (3 levels) | lock analysis | `pg_locks` graph | R4.4 | 6 |
| Terminate connection | E+ action | `pg_terminate_backend` | R4.5 | 6 |
| System Insights summary cards | OS+DB metrics | S2/S3/S7 | R5.1 | 7 |
| System Insights metric families | `database/postgresql/*` | S2 + S3 | R5.2 | 7 |
| OS metrics (CPU/mem/disk/net) | `database/{cpu,memory,disk,network}/*` | cAdvisor/node-exporter/metrics-server | R5.3 | 7 |
| Compare up to 5 instances | UI feature | stable instance identity | R5.7, R9.3 | 7 |
| Download PNG / CSV | chart toolbar | per-chart toolbar | R5.6 | 7 |
| Events timeline | events | k8s events + phase transitions + backups | R5.9 | 8 |
| Index advisor (E+) | query plans | statement text + index usage | R7.1 | 8 |
| Recommenders (E+) | multiple | bloat, rightsizing, autovacuum, wraparound | R7.2–R7.5 | 8 |
| AI troubleshooting (E+) | Gemini | deferred; API slot reserved | §1.3 | post-0.1 |
| Admin API | `cloudsqladmin v1beta4` | REST + OpenAPI | R8.1–R8.2 | 1→5 |
| MCP PromQL-in/out API | `databaseinsights.googleapis.com/mcp` | `/api/v1/metrics/{query,system}` | R8.3 | 5 |
| `InsightsConfig` equivalents | `queryInsightsEnabled`, `queryStringLength`, `recordApplicationTags`, `recordClientAddress`, `queryPlansPerMinute`, `enhancedQueryInsightsEnabled` | flags + ConfigMap keys, same names where meaningful | R3.6, R3.10, R3.13, R11.8 | 1 |
| Edition tiers | Enterprise / Enterprise Plus | single tier at the E+ feature set | §1.2 | — |
| Pooler insights | (Cloud SQL: pgbouncer on 6432) | `cnpg_pgbouncer_*` + `SHOW POOLS/CLIENTS` | R6 | 8 |

---

## 7. Delivery plan

Nine phases. Each phase lists deliverables, the requirement IDs it satisfies, its acceptance
criteria, and the assigned agent. **No phase starts before the previous phase's validation entry is
written into `IMPLEMENTATION.md`** (R14.2).

### Agent allocation (the brief's hard constraint: 4 subagents max — 2 opus + 2 sonnet)

| Slot | Model | Spawned | Roles |
|---|---|---|---|
| A1 | opus | `a19261c1d46185a9e` | Cloud SQL research (done) → adversarial reviewer → Go backend (collector, store) |
| A2 | sonnet | `aa0fd103621139f92` | CNPG research (done) → adversarial reviewer → Go backend (API, metrics export) |
| A3 | opus | *to spawn for review* | Adversarial reviewer → architecture, spec maintenance, phase validation |
| A4 | sonnet | *to spawn for review* | Adversarial reviewer → frontend/UI, Helm chart, packaging, docs |

Four agents total, reused via `SendMessage` for every phase. No further spawns for the rest of the
project.

| Phase | Scope | Req IDs | Acceptance criteria | Agent |
|---|---|---|---|---|
| **0** | Repo skeleton, `go.mod`, Makefile, Dockerfile (multi-arch), Helm chart skeleton, CI, `IMPLEMENTATION.md`, license | R11.1–R11.5, R11.7, R11.11, R14.1, R15 | `go build ./...` clean; `docker buildx build --platform linux/arm64,linux/amd64` succeeds; `helm lint` clean; CI green; `IMPLEMENTATION.md` exists with the WBS | A4 |
| **1** | Discovery + kube client + `/clusters` API + `/healthz` + `/readyz` | R1.1–R1.9, R8.1, R8.2 (partial), R8.7, R12.1–R12.3 | **All 4 reference clusters returned by `/api/v1/clusters`** with correct phase, instance counts, primary, and PG major (16 vs 17 distinguished); `freeidp-pg` renders its degraded state; `rev-proxy-pg` shows its Pooler; R1.6 passes | A1 |
| **2** | Collector framework + PG version matrix + `database`/`settings`/`replication`/`wal` collectors + `/metrics` v1 (aggregate only) | R2.1–R2.8, R9.2–R9.4, R9.7 (partial), R16.1 | Collection round < 3 s / < 40 round-trips per cluster (R13.1) on all 4 clusters; PG16 and PG17 both collect successfully via the version matrix (R2.4 **[A]**); `/metrics` validates against `promtool check metrics` | A1 |
| **3** | `pg_stat_statements` collector + normalizer + query store + top-N API + per-query metrics family | R3.1–R3.8, R3.14, R9.5, R16.3 | Top-N table for each cluster matches a manual `SELECT … FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 50` within tolerance; normalizer output matches the fixture corpus; counter-reset test passes; series count within the §5 budget | A1 |
| **4** | Metrics export polish: ServiceMonitor/PodMonitor, PrometheusRule with `critical` tier, cardinality assertion, end-to-end scrape by the reference env's kube-prometheus-stack | R9.1, R9.6, R9.8–R9.10, R16.4 | **The existing `prom-operator` Prometheus scrapes us and the series are queryable** (R9.9 **[A]**); `promtool check rules` passes; all 4 clusters' series present with correct labels | A2 |
| **5** | Full API surface + OpenAPI + auth + PromQL pair + SPA shell with the **Fleet** page | R8.1–R8.9, R10.1–R10.4, R10.13 | `GET /api/v1/query?query=pgsi_up` returns a Prometheus-shaped result; OpenAPI doc committed and CI-validated; Fleet page renders all 4 clusters with health; `helm install` from a tarball works (R11.6 **[A]**) | A2 |
| **6** | Active queries + blocking chain + wait events + terminate + Query Detail page + Queries page | R4.1–R4.7, R3.9–R3.13, R10.5–R10.9 | Active list matches `pg_stat_activity` exactly on all 4 clusters (R4.1 **[A]**); a real blocking pair is rendered as a chain; terminate works behind its gate; Queries page table matches the R3.2 column set exactly | A1 |
| **7** | System Insights (OS + DB families, summary cards, compare-5, PNG/CSV export) | R5.1–R5.8, R9.7 complete, R10.10 | The five summary cards render for all 4 clusters (R5.1 **[A]**); CSV export byte-compares against a golden; PNG export produces a valid image; compare-5 works with 4 available clusters | A2 |
| **8** | Pooler insights + Recommenders + events timeline + plan capture | R6.1–R6.5, R7.1–R7.6, R5.9, R3.10–R3.11, R10.11 | `rev-proxy-pg-pooler` renders full pool state (R6.1); pgbouncer attribution produces a real client IP where the Postgres side shows the pooler (R6.4 **[A]**); recommendations render with evidence | A1 |
| **9** | Docs site, runbooks, README polish, screenshots, lab profile, v0.1.0 release | R11.6, R11.9, R13.6, R14.3–R14.7, R15.2 | Two-command quickstart executed from a clean namespace and verified; release tagged with SBOM; every metric and route documented (R14.7) | A4 |

**Phase 0–2 is the "does this work at all" spine** and is deliberately backend-first: it produces a
scrapeable, multi-cluster metrics surface and a JSON inventory before a single pixel is drawn,
because that is the part the brief calls out as non-negotiable ("ensure the solution provides
prometheus metrics so that multiple instances can be centralized"). The UI starts at Phase 5.

---

## 8. Validation plan against the reference environment

The four clusters in `docs/ENVIRONMENT.md` are chosen well enough to be the test fixture; each
exercises a specific code path:

| Cluster | What it exercises | Requirements proven |
|---|---|---|
| `ai-api-gateway/rev-proxy-pg` (2 inst, PG 17, **Pooler**) | Normal HA path + pgbouncer attribution + pooler metrics + PG17 column renames (`shared_blk_read_time`, `pg_stat_checkpointer`) | R1.2, R3.5, R6.1–R6.5, R2.4 |
| `cookbook/cookbook-pg` (1 inst, PG 17) | Degenerate single-instance path: empty replication set, no replicas | R5.5, R9.3 |
| `freeidp/freeidp-pg` (2 inst, PG 17, **disk pressure + Pending replica**) | Degraded rendering, disk-pressure metrics, a non-running instance, `phaseReason` surfacing | R1.1, R5.1, R10.12 |
| `open-webui/open-webui-pg` (2 inst, **PG 16**, 2 Gi) | The PG16 branch (`blk_read_time` still present, `pg_stat_checkpointer` absent, `op_bytes` present) and a small-storage cluster | R2.4, R13.9 |

**Acceptance checks** (the `[A]` requirements, scripted as `scripts/validate-env.sh`, R16.5):

1. `GET /api/v1/clusters` returns exactly the 4 clusters with correct phase/instances/primary/major. *(R1.5, Phase 1)*
2. The `freeidp-pg` entry reports the disk-pressure `phaseReason` and the Pending instance. *(Phase 1)*
3. `pgsi_up{cluster="cookbook-pg"} == 1` and its replication section renders "single instance". *(R5.5, Phase 2)*
4. Version-matrix unit tests cover PG 13–18; live collection succeeds on PG 16 **and** PG 17. *(R2.4, Phase 2)*
5. `promtool check metrics < our /metrics` exits 0; `promtool check rules chart/templates/prometheusrule.yaml` exits 0. *(R9.9, Phase 4)*
6. The **existing** `monitoring/prom-operator` Prometheus, with no config change beyond applying our ServiceMonitor, returns series for all 4 clusters. *(R9.9, Phase 4)*
7. `helm template` golden tests pass for both profiles; `helm install` from a packaged tarball into a fresh namespace serves the UI in < 5 min. *(R11.6, Phase 5)*
8. Active-queries API output equals a direct `pg_stat_activity` dump (modulo our own session, R2.8) on all 4 clusters. *(R4.1, Phase 6)*
9. The five System Insights cards render for all 4 clusters. *(R5.1, Phase 7)*
10. For `rev-proxy-pg-pooler`, the pgbouncer-sourced client address differs from the Postgres-side `client_addr`, proving attribution works. *(R6.4, Phase 8)*

---

## 9. Risks

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| K1 | **Prometheus cardinality explosion** kills adoption (the classic failure mode of this product class) | High | High | §5 budget with a CI-checkable assertion (R13.4); `topN` default 50; query text never a label outside top-N; the R5.4 500-database guard |
| K2 | **`pg_stat_statements` is not installed** on most CNPG clusters (it is absent from CNPG's default set), so the headline feature has no data on a stock install | High | High | R3.1 makes absence a first-class rendered state with exact remediation; Phase 1–2 and 4–5 deliver value without it; document it in the README's first paragraph, not in a footnote |
| K3 | **Credential acquisition** — reading `<cluster>-app` needs secret read access; some teams won't grant it | Medium | Medium | R1.8 four-level precedence; R12.4 documents the `pg_monitor` role path; S3 (9187) needs no SQL credential at all, so Phase 2 works credential-free |
| K4 | **Scope creep** — the parity list is ~25 capabilities and the brief also demands implementation | High | High | The phase table is the contract; anything not in §7 is out; `IMPLEMENTATION.md` deviations log makes scope changes visible rather than silent |
| K5 | **SQLite single-writer** limits horizontal scale | Medium | Medium | §3.4 documents the limit and the v1.x path (Postgres-backed store); R13.2 sets a 50-cluster target that SQLite comfortably meets |
| K6 | **PG version drift** — PG18 changes query-id jumbling and removes/renames columns; a 17→18 upgrade silently breaks baselines | Medium | Medium | R2.4 matrix-as-data; R3.4's own fingerprint survives id changes; R16.2 tests on two majors live |
| K7 | **The reference environment is one k3s cluster** — we cannot validate multi-cluster or multi-tenant behaviour from it | Medium | Medium | R13.9 compatibility matrix marks untested cells honestly; R11.6's lab profile gives a kind/minikube validation path |
| K8 | **CNPG operator instability in the reference env** (99 restarts) may make discovery flaky during development and be mistaken for our bugs | Medium | Low | R1.9 stale-grace behaviour; R8.7 `/readyz` gated on a real sync; validation script distinguishes "operator unreachable" from "our parse failed" |
| K9 | **Frontend quality** — "extremely easy-to-use" is the brief's emphasis and the easiest thing to under-deliver on | Medium | High | R10.2 forces the `dataviz` skill before chart code; R10.12 forces designed empty states; R10.14 sets a load-time budget; A4 owns UI as a dedicated role |
| K10 | **Our own `/metrics` duplicates `cnpg_*`**, so users see the same number twice under two prefixes | Medium | Low | R9.7 re-exports deliberately with a documented mapping table; the chart README explains which prefix to alert on |

---

## 10. Open questions

| # | Question | Working answer (until the reviews say otherwise) |
|---|---|---|
| Q1 | Product name and binary name — `pgsql-insights` / `pgsi`? | Repo is `pgsql-insights`; binary and metric prefix are `pgsi` for CLI ergonomics. Confirm. |
| Q2 | Ship the SPA embedded, or also as a standalone static bundle for CDN hosting? | Embedded only in v1 (R8.6); `--serve-ui=false` covers API-only. |
| Q3 | Do we need `kubectl pgsi` as a plugin surface, mirroring `kubectl cnpg`? | Post-0.1. The API is the contract; a plugin is a thin client over it. |
| Q4 | Multi-user auth (per-user) vs. one shared token? | Shared token in v1 (R8.8); per-user via OIDC post-0.1. |
| Q5 | Should the store be Postgres-backed by default on CNPG clusters (dogfooding)? | No — SQLite default keeps the install dependency-free; Postgres-backed is the documented scale-up path. |
| Q6 | Is Apache-2.0 correct, and does the org exist for `ghcr.io/<org>/`? | Apache-2.0 (R15.1); org name needed before Phase 0 CI publishes images. |

---

## 11. Review log

The brief requires **two adversarial reviews** of this document, with feedback incorporated.

| Round | Reviewer | Scope | Status | Findings | Incorporated |
|---|---|---|---|---|---|
| 1 | A3 (opus) — *architecture, feasibility, spec-vs-research consistency* | pending | ☐ | — | ☐ |
| 1 | A4 (sonnet) — *product scope, UI/UX, packaging, testability* | pending | ☐ | — | ☐ |
| — | Consolidation into `0.2.0` | | ☐ | | ☐ |

Rules for the reviews, so they are adversarial rather than agreeable:
1. Every finding must cite a section ID and state what breaks, not just what is unclear.
2. Findings that contradict `docs/research/*` are graded **blocking**.
3. Findings that identify a missing requirement are graded **major**; a missing requirement is added to §4 with a new ID and a phase assignment, not folded into prose.
4. Disagreements between the two reviewers are resolved by the spec owner and recorded here, with the reasoning, rather than silently averaged.

*(R15 and R16 in this draft were already added from an internal consistency pass; the two external
reviews in this round are expected to produce further changes, which will be logged here and
released as `0.2.0`.)*
