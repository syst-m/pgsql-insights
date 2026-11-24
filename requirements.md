# pgsql-insights — Requirements Specification

| | |
|---|---|
| **Version** | `0.3.0` (Phase 1 split into 1a/1b — the UI is no longer scheduled last) |
| **Date** | 2026-11-22 |
| **Status** | Reviewed and consolidated — **Phase 0 complete, Phases 1a/1b unblocked** |
| **Supersedes** | `0.2.0` (round-1 reviews incorporated) ← `0.1.0-draft` (`97172a4`) ← the pre-review draft |
| **Owners** | Frank Ellis (product), Claude (spec author) |
| **Applies to** | CloudNativePG **1.29–1.30** on Kubernetes **1.34–1.36**, PostgreSQL **15–18** (13–14 documented, unvalidated — see R13.9). Target env: CNPG 1.30.0, PG **16.11 + 17.11**, k3s v1.37.2+k3s1 |

**Provenance.** Every factual claim this spec builds on is drawn from one of four grounded
documents, not from model memory:

| Source | Path | Used for |
|---|---|---|
| Cloud SQL product research | `docs/research/cloud-sql-insights.md` (949 l) | The capability inventory we are cloning (§2, §6 parity matrix) |
| CloudNativePG surface inventory | `docs/research/cloudnativepg.md` (2475 l) | Every CNPG/PG data source, endpoint, metric name, and version matrix |
| Reference environment | `docs/ENVIRONMENT.md` | The four clusters the acceptance criteria are testable against |
| Live probe | `docs/research/live-probe.md` + `testdata/` | Measured values that settled the questions reading the CRDs could not |

Where this spec deliberately departs from Cloud SQL, the departure is stated with a reason
(marked **Divergence**). Where Cloud SQL has a capability PostgreSQL cannot supply, it is marked
**Gap** rather than silently dropped.

**Review provenance.** This revision consolidates three inputs: the architecture review
(`docs/reviews/round-1-architecture.md`, A3/opus — 7 blocking, 10 major, 8 minor), the product
review (`docs/reviews/round-1-product.md`, A4/sonnet — 8 blocking, 14 major, 8 minor), and the
nine live-probe changes P1–P9 (`live-probe.md` §9). Reviewer disagreements are ruled on in §11
per that section's rule 4 — four rulings, recorded with reasoning, not averaged.

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
| Managing/provisioning Postgres (create cluster, scale, restore) | That is the operator's and `kubectl cnpg`'s job. We are read-only plus a small, explicitly-gated set of actions (terminate session, EXPLAIN, `pg_stat_statements` enablement — R12.8). |
| Supporting non-CNPG Postgres (Zalando, Crunchy, RDS, standalone) | The brief scopes us to CNPG. The collector is factored so the SQL layer is reusable, but CNPG discovery is the contract. |
| Being a Grafana replacement | Grafana answers "plot this metric". We answer "why is this database slow". We export *to* Prometheus so Grafana keeps working. |
| **Embedding Grafana panels instead of building our own** | Cheaper, but it forfeits the brief's own emphasis ("extremely easy to use graphs… in *its* UI"), needs Grafana present and reachable, cannot render non-panel surfaces (the top-N table, the blocking tree, the query detail pane), and makes our UI's load time a function of another product's. We build the charts; we deep-link out instead (R5.6). |
| Multi-engine (MySQL, AlloyDB) | PostgreSQL only. |
| AI-assisted troubleshooting in v1 | Cloud SQL's Investigation pane is Enterprise Plus and needs a model in the loop. We design the API surface for it (`/api/v1/insights/diagnosis`) and ship it post-0.1. |
| Real-time streaming of every statement (a "firehose"/pgBadger-alike) | Cloud SQL samples at 60 s, not per-statement. Matching its sampling model keeps cost and cardinality bounded (§5). |
| Multi-kubernetes-cluster federation of our own API | One deployment sees one cluster's namespaces. Fleet-wide centralization is Prometheus's job, which is exactly the architecture the brief asks for. |

### 1.4 Delivery constraints from the brief, and how each is met

| Constraint (verbatim intent) | How this spec satisfies it | Requirement |
|---|---|---|
| "web based" | SPA served by the same Go binary that serves the API; no separate web tier | R10, R11.1 |
| "container packaged" | One distroless-static image, multi-arch `linux/arm64` + `linux/amd64`, no shell, non-root uid | R11.2–R11.5 |
| "k8s native" | Discovers work through the Kubernetes API (watch on CNPG CRDs), ships ServiceMonitor + PrometheusRule, runs as a normal Deployment with a least-privilege Role | R1, R11, R12 |
| "rapidly rollable out" | **No `Cluster` patch required for inventory, metrics, system insights, active queries or poolers.** One Helm release in one namespace covers every cluster the RBAC can read. **One** documented, reversible, gated `spec.parameters` patch (`shared_preload_libraries`) unlocks Query Insights — the product's headline — and that patch is a product flow (R3.15), not a documentation footnote | R1.6, R2.1, R3.15, R11.6, R12.4, R12.8 |
| "others can benefit… in production or in a lab" | Production profile (SQLite store, TLS, RBAC-only auth) and lab profile (in-memory store, `--single-namespace`, no PVC) from the same chart, plus a `values-default.yaml` that is the shipped default | R11.6, R11.12, R13.6 |
| "provides prometheus metrics so that multiple instances can be centralized into a single Prometheus instance for the basic metrics" | `/metrics` exposes one flat, label-normalized series set per instance, safe to scrape N instances into one Prometheus; documented label contract so dashboards survive multi-instance scraping | R9 |
| "Some features won't make sense to fall back that way" | Explicit split (§4 plane rule): **basic metrics → Prometheus**; **query text, plans, active sessions, blocking chains → API + store only**. Each feature is tagged with which plane serves it | R9.1, R8 |
| "extremely easy-to-use graphs and visualizations" | R10 specifies per-page chart types, the interaction contract (R10.19), the time-range model (R10.15), and a chart library chosen by the `dataviz` skill before any chart code is written | R10 |
| "backend APIs" | Versioned REST API, plus the PromQL-in/PromQL-out pair mirroring Google's MCP shape | R8 |
| "keep state of the implementation progress and work breakdown… a file that can be handed off" | `IMPLEMENTATION.md` is created at Phase 0 and updated every phase; §7.2 is the requirement→phase→status index it mirrors | R14 |

---

## 2. Personas and jobs to be done

| # | Persona | Their job | Served by |
|---|---|---|---|
| P1 | **Platform engineer** running CNPG for several teams | "Install once, see every Postgres in the cluster, and know which one is on fire." | R1, R9, R10.1 (Fleet) |
| P2 | **Application developer** with one app on one cluster | "Why is *my* query slow, and what should I change?" | R3, R10.6–R10.7, R7 |
| P3 | **DBA / on-call** at 03:00 | "What is running right now, who is blocking whom, and can I kill it?" | R4, R10.8 |
| P4 | **SRE building a golden path** | "Give me the metrics in our central Prometheus and the alerts, without hand-maintaining a 66-panel dashboard." | R9, R11.7, R7 |
| P5 | **Evaluator / lab user** | "`helm install` in a namespace with one cluster and see something useful in under five minutes." | R11.6, R11.9, R13.6 |

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
             HTTPS :8000/pg/status ─┘                          │
                                                    Prometheus ◀┘ (ServiceMonitor scrapes us;
                                                      our /metrics carries every instance)
```

**One binary, one role in v1.** `pgsi` ships with a single subcommand surface —
`pgsi serve|collect|api|version` (`cmd/pgsi`, R3.5 layout) — and **`serve` is the only role v1
supports**: collector + store + API + UI in one process. The `collect`/`serve` split that earlier
drafts promised as `--role=collect|serve` is **deferred to v1.x**, for two concrete reasons: a
split role needs two pods sharing one SQLite file, which requires `ReadWriteMany` storage that the
chart cannot assume (R11.15); and SQLite's single-writer WAL mode contends on NFS-class RWX
volumes, which is the usual k3s/on-prem answer. Shipping a speculative split we cannot validate on
the reference environment is worse than shipping one role and adding the second when the
Postgres-backed store (R11.15) lands. The subcommand names are reserved so v1.x can add them
without breaking the CLI contract.

### 3.2 Data sources, ranked by cost and by always-available-ness

| # | Source | Endpoint | Auth | Yields | Always there? |
|---|---|---|---|---|---|
| S1 | **`Cluster.status`** | `GET` on the CR | RBAC | phase, phaseReason, instances, readyInstances, **instancesStatus (per-pod container statuses)**, instancesReportedState (per-pod isPrimary/ip), currentPrimary, currentPrimaryTimestamp, topology, pvc lists, services, conditions, cert expirations, image, systemID | **Yes** — free, no metrics needed |
| S2 | **Direct SQL** | `<cluster>-rw.<ns>.svc:5432`, or pod IP from S1 | `<cluster>-app` secret, or a `pg_monitor` role we document | `pg_stat_statements`, `pg_stat_activity`, `pg_stat_database`, `pg_locks`, `pg_stat_replication`, `pg_stat_wal`, `pg_stat_io`, `pg_stat_slru`, `pg_stat_checkpointer`, `pg_stat_progress_*`, `pg_settings`, `pg_database`, table/index stats | **Yes on a serving instance** — Postgres is up or the cluster is down. Not on a crash-looping pod (see §8's reachability column) |
| S3 | **Instance-manager metrics** | `GET http://<pod>:9187/metrics` (HTTPS when `spec.monitoring.tls.enabled`) | none on 9187 | the full `cnpg_*` surface: 14 default queries + the Go collector set (`cnpg_collector_*`) + `go_*`. Measured **107 / 115 / 118** `# TYPE` families on the three reachable reference pods | **Yes** — compiled into the instance manager, not a sidecar, not gated on `spec.monitoring` |
| S4 | **Instance-manager status** | `GET https://<pod>:8000/pg/status` (**HTTPS always**, measured) | unauthenticated (probes and `/pg/status` need no client cert) | `postgres.PodStatus`: 27 verified fields incl. `replicationInfo[]`, `replicationSlotsInfo[]`, `lastFailedWAL`, `isPodReady`, `pendingRestart`, `instanceArch` | **Yes** |
| S5 | **Pooler metrics** | `GET http://<pooler-pod>:9127/metrics` | none | `cnpg_pgbouncer_*` (lists, pools, stats) — measured **80 families / 22 775 B**, a surface with **zero** existing dashboards or alerts | Only where a `Pooler` exists (1 of our 4 clusters) |
| S6 | **Pooler runtime** | pgbouncer `SHOW POOLS` / `SHOW CLIENTS` / `SHOW STATS` over 5432 on the pooler | `<cluster>-pooler` secret | the real client→server mapping that fixes attribution | Same |
| S7 | **cAdvisor / node-exporter / metrics-server** | queried *from* Prometheus, or direct | — | container CPU/memory, volume bytes, node metrics for the System Insights OS side | Present in the reference env; **optional** for us |
| S8 | **`kubectl cnpg status -o json`** | CLI | kubeconfig | the typed render of S1 plus live PG rows | Optional; we implement the queries natively rather than shelling out |
| S9 | **`postgres_exporter` sidecar** | its own `/metrics` | — | 36 collectors incl. `stat_statements` | Optional third path, off by default |
| S10 | **Pod logs** (`pods/log`) | `GET` on the pod log subresource | RBAC `pods/log: [get, list]` | Postgres log lines for the R5.1 **Log errors** card and the R5.9 events feed. Cloud SQL sources this card from `logging.googleapis.com/log_entry_count`, which we do not have | Only if `--log-source` is enabled (R5.10); **off by default** |

**S1 → S2 → S3 is the required spine.** Everything in Phases 1–4 works with only S1 + S2 + S3, all
of which exist on a stock CNPG pod with no `Cluster` patch. S7 is used opportunistically and
degrades gracefully when absent (R5.3). S9 is opt-in and never required. S10 is opt-in and is the
only source for one R5.1 card (R5.10).

**S3 caveat, measured:** neither CNPG's built-in exporter nor `postgres_exporter` collects
`pg_stat_io`, so the `io` collector's S3 path yields nothing on `cnpg_*` — `pg_stat_io` comes from
S2 only (and only on PG 16+). Building the `io` collector on S3 alone silently produces an empty
panel on every cluster.

### 3.3 Collection model

Per cluster, on a configurable interval (default **60 s**, matching Cloud SQL's sampling period):

| Collector | Interval | Source | Notes |
|---|---|---|---|
| `inventory` | watch + 30 s resync | S1 | Keeps the cluster/pod/role map hot; drives every other collector |
| `statement` | 60 s | S2 `pg_stat_statements` | The Query Insights feed; gated on extension presence (R3.1) |
| `activity` | 15 s | S2 `pg_stat_activity` | The Active queries feed; also our wait-event and blocking sample |
| `database` | 60 s | S2 `pg_stat_database`, `pg_database`, `pg_stat_database_conflicts` | |
| `wal` | 60 s | S2 `pg_stat_wal` (14+), `pg_stat_archiver`, `pg_wal` dir via S3 | |
| `io` | 60 s | S2 `pg_stat_io` (16+), `pg_stat_slru` (13+), `pg_stat_bgwriter`/`pg_stat_checkpointer` | Version-gated; S3 carries no `pg_stat_io` (§3.2 caveat) |
| `replication` | 60 s | **S4 `replicationInfo[]`/`replicationSlotsInfo[]`** (preferred, no SQL, no creds); S2 `pg_stat_replication`, `pg_replication_slots`; S3 `cnpg_pg_replication_*` | Empty on single-instance clusters (R5.5) |
| `locks` | 30 s | S2 `pg_locks` | Feeds the blocking chain |
| `settings` | 15 min | S2 `pg_settings`, `pg_file_settings`, `pg_extension` | Slow-changing; also gives `max_connections`, `shared_buffers`, `work_mem` for the gauge set |
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
| What does **not** | Raw metric samples — those live in Prometheus; we serve them from our registry, not from SQLite | Avoids rebuilding a TSDB. This is why R8.3 cannot answer arbitrary PromQL from the store |
| Retention | `retention.statementSnapshots: 7d` (default), `retention.activeSessions: 48h`, `retention.plans: 7d`, `retention.maxBytes: 1Gi` with size-based eviction | Cloud SQL parity: 7 d Enterprise / 30 d Enterprise Plus, exposed as a single knob |
| Concurrency | Single-writer, many-reader; WAL mode | Matches one poller + N API readers |
| Access mode | **`ReadWriteOnce`** for `--role=serve` (one pod). A future collector/server split requires `ReadWriteMany` (R11.15) | RWO cannot attach to two pods on different nodes; assuming otherwise is how a v1.x role split breaks |
| Scale-out | Sharding by namespace across collector instances writing to one store is **not** supported in v1; documented as a v1.x item | Honest about SQLite's single-writer limit |

### 3.5 Source layout

```
pgsi/
├── cmd/pgsi/                 # main; subcommands serve|collect|api|version (R3.1)
├── internal/
│   ├── discovery/            # S1: informers on clusters/poolers/pods/secrets  (R1)
│   ├── kube/                 # rest.Config, clientset, pods/proxy helper        (R1.7, R1.10)
│   ├── collector/            # collector framework + registry + global budget   (R2)
│   │   ├── pgversion/        # PG 13–18 view/column matrix                      (R2.4)
│   │   ├── statements/       # pg_stat_statements + normalization               (R3)
│   │   ├── activity/         # pg_stat_activity, blocking chain                 (R4)
│   │   ├── database.go  wal.go  replication.go  settings.go   # shipped as flat files (R2)
│   │   ├── io.go  locks.go  progress.go                       # specified, not yet built
│   │   ├── cnpgmetrics/      # S3 parser: cnpg_* + cnpg_collector_*             (R2.6)
│   │   ├── pgbouncer/        # S5 + S6                                          (R6)
│   │   └── podlogs/          # S10 parser                                       (R5.10)
│   ├── store/                # sqlite + memory implementations, retention       (§3.4)
│   ├── metrics/              # MetricHub: our registry, label contract, top-N   (R9)
│   ├── api/                  # REST handlers, query catalogue, auth, OpenAPI    (R8)
│   ├── web/                  # go:embed of the built SPA                        (R10.1)
│   └── config/               # flags + env + ConfigMap + defaults precedence    (R11.8)
├── web/                      # React + Vite + TS SPA                            (R10)
├── chart/                    # Helm chart: deployment, rbac, svc, servicemonitor,
│                             # prometheusrule, configmap                          (R11.7)
├── deploy/
│   ├── plain-yaml/all.yaml   # GENERATED by `helm template` in CI               (R11.13)
│   └── kustomize/            # kustomization pointing at plain-yaml/all.yaml    (R11.9)
├── api/openapi.yaml          # generated, CI-checked                            (R8.1, R8.1b, R8.1c)
├── docs/                     # ENVIRONMENT.md, research/, reviews/, runbooks    (R14)
├── testdata/                 # recorded cnpg_* exposition + pg-status fixtures  (R16.3)
├── requirements.md           # this file
└── IMPLEMENTATION.md         # the handoff file (R14)
```

---

## 4. Requirements

Format: `Rn.m — statement`. Requirements marked **[A]** have an acceptance test in §8 that is
executable against the reference environment; unmarked ones are verified by review or unit test.
Every ID in this section appears in exactly one phase in §7.2 — an ID defined in §4 and scheduled in
no phase is a spec defect (round-1 finding A3-M7).

### R1 — Discovery

| ID | Requirement |
|---|---|
| R1.1 | Discover `Cluster` objects in all namespaces visible to the configured RBAC, via a watch with a 30 s resync; support `--namespaces=a,b,c` and `--exclude-namespaces` allow/deny lists. |
| R1.2 | For each cluster, resolve its instances by the label selector `cnpg.io/cluster=<name>,cnpg.io/podRole=instance` and read role from `cnpg.io/instanceRole` (`primary`/`replica`/`unhealthy`), falling back to the deprecated `role` label when only that is present. **Read the per-pod container statuses, not `status.instances`:** a pod can be `Running` with its `postgres` container in `CrashLoopBackOff`, and a CR reporting 2 instances can have **zero** serving (measured on `freeidp-pg`). A collector that trusts the count renders a green cluster that is fully down. **[A]** |
| R1.3 | Derive the PG major version from the `app.kubernetes.io/version` pod label, avoiding a `SELECT version()` per poll; fall back to SQL `current_setting('server_version_num')` when the label is absent, and cache it per cluster. **The field is nullable** (`*int` in the DTO, `number | null` in the SPA — A1 `93fcd44`): R1.3's answer is sometimes *not known yet*, and a `0` renders in the Fleet table as a real version. The UI renders `—` for null. |
| R1.4 | Discover `Pooler` objects and their pods (`cnpg.io/podRole=pooler`), and associate each to its `spec.cluster.name`. |
| R1.5 | Expose the inventory at `GET /api/v1/clusters` with, per cluster: namespace, name, phase, phaseReason, instances, readyInstances, **servingInstances** (pods whose `postgres` container is running — R1.2), currentPrimary, PG major, instance list with roles, node names and ready flags, conditions, cert expirations, PVC list, services, and any Poolers. |
| R1.6 | **Zero-prerequisite rule:** discovery must succeed and produce a renderable inventory on a cluster where `spec.monitoring` is unset, no Pooler exists, and no credentials have been provided. **[A]** |
| R1.7 | Support two HTTP transports to pod endpoints and select automatically: direct pod-IP/Service URL, and the Kubernetes API `pods/proxy` subresource (`/api/v1/namespaces/<ns>/pods/<scheme>:<pod>:<port>/proxy/<path>`), the latter selected automatically when direct addressing fails or `--use-proxy=true`. |
| R1.8 | Read credentials in this precedence: explicit `--credential-secret` reference → cluster-secret convention (`<cluster>-app`, keys `username`/`password`/`dbname`) → a configured single credential → none (SQL collectors report `unconfigured`, non-SQL collectors still work). |
| R1.9 | React to cluster deletion within one resync by dropping its collectors and labelling its series stale rather than deleting them immediately (a 15 min grace prevents churn-induced gaps in Prometheus). |
| R1.9b | *(reserved — no v0.2.0 requirement; kept so the 0.1.0 ID `R1.9`'s neighbours stay stable)* |
| **R1.10** | **Per-port transport table as data, not as `if` statements.** The endpoint scheme is resolved from a table: `5432` = postgres wire (password/TLS); `9187` = **HTTP**, HTTPS only when `spec.monitoring.tls.enabled`; `8000` = **HTTPS always**; `8010` = HTTP, pod-local only, not reachable externally. A collector that probes `http://pod:8000` reports the status endpoint as down — measured (`live-probe.md` §4, F6). Every HTTP collector reads its scheme from this table. |
| **R1.11** | **`pods/proxy` availability caveat, documented.** `pods/proxy` works only when the API server can route to the pod CIDR; it is reliable when `pgsi` runs in-cluster and unreliable when the API endpoint is tunnelled (measured: 502 through a tunnelled endpoint while `exec`/`port-forward` to the same pod both work — `live-probe.md` §5, F8). R1.7's direct-first ordering is the mitigation; the docs state the limitation rather than presenting the proxy as universally available. |

### R2 — Collection framework

| ID | Requirement |
|---|---|
| R2.1 | Provide a collector framework where each collector declares: name, interval, required source, PG-version applicability, and cost class; the scheduler runs them independently with per-collector timeouts (default 10 s) and per-cluster concurrency limits (default 4). |
| R2.2 | Every collector run emits `pgsi_collector_duration_seconds{collector}` and increments `pgsi_collector_errors_total{collector,cluster,namespace}`; a failed collector leaves its last-good data served and marks it with an age. |
| R2.3 | Support `--interval.<collector>=` overrides and `--disable-collector=a,b` so a lab can run with two collectors, and a production user can disable an expensive one. |
| R2.4 | Maintain a **PG 15–18 capability matrix as data**, not as scattered `if` statements: **per-view column sets per major**, not a rename table. The matrix must encode, among others: `total_time`→`total_exec_time` at PG13 (13–14 rows present but unvalidated, R13.9); `pg_stat_statements` `blk_read_time`/`blk_write_time` → `shared_blk_read_time`/`shared_blk_write_time` at PG17; `op_bytes` removed from `pg_stat_io` at PG18; `pg_stat_wal` shrinking 9→5 columns at PG18; and — the case a rename table gets wrong — **`pg_stat_bgwriter` at PG17 shrinks from 11 columns to 4** (`buffers_clean`, `maxwritten_clean`, `buffers_alloc`, `stats_reset`) **while checkpoint stats move to a new, separate view `pg_stat_checkpointer`**. CNPG 1.30 ships three queries here (`pg_stat_bgwriter`, `pg_stat_bgwriter_17`, `pg_stat_checkpointer`); implementing this as a rename loses four still-valid bgwriter families on our PG17 clusters. Every SQL collector resolves its query through this matrix. **[A]** |
| R2.5 | Schema-qualify every catalog reference (`pg_catalog.*`, `pg_stat_statements.…`) in all generated SQL, matching the CNPG 1.29.1 hardening and immune to `search_path` shadowing. |
| R2.6 | Parse the `cnpg_*` / `cnpg_collector_*` exposition from S3 into typed structs (not a generic Prometheus scrape passthrough), so we can serve the same numbers through the API and re-export them under our own prefix. |
| R2.7 | Run every SQL collector inside a single read-only transaction per view group, and honour `statement_timeout` from config (default 5 s) so a sick instance cannot wedge the poller. |
| R2.8 | Exclude our own sessions from every count we report, by setting `application_name=pgsi-collector` and filtering it out of `pg_stat_activity`-derived metrics (the postgres_exporter `--collector.stat_statements.exclude_users` pattern, applied by us at query level). Confirmed in the wild: CNPG's own exporter does exactly this with `application_name="cnpg_metrics_exporter"` (`live-probe.md` §8). |
| **R2.9** | **Global scheduler budget.** R2.1 bounds concurrency per cluster only; 50 clusters × 13 collectors fans out to ~200 concurrent SQL queries with no global cap, against Postgres instances where `max_connections` is commonly 100 (our `open-webui-pg` is a 2 Gi cluster). Add `--max-concurrent-sql` (default 32), `--max-concurrent-http-scrapes` (default 64), `--scheduler-queue-depth` (default 512) and a shed policy that marks the shed cluster stale rather than dropping the sample. Export `pgsi_scheduler_queue_depth` and `pgsi_scheduler_shed_total{cluster}`. |

### R3 — Query Insights

| ID | Requirement |
|---|---|
| R3.1 | Detect `pg_stat_statements` presence per database (from `pg_extension` via S2 and from `cnpg_pg_extensions_*` via S3) and report it per cluster. When absent, the cluster renders the R10.27 first-class "not installed" screen — and the remediation is a **product flow**, not a docs string: detect → offer → patch → verify, implemented as **R3.15**. |
| R3.2 | Compute the **top-N queries** per cluster, ranked by `total_exec_time`, with the Cloud SQL column set as far as PostgreSQL supplies one. **Per-query sourced columns:** `Query`, `Database`, `Recommendations`, `Load by total time`, `% load by Queries`, `Avg execution time (ms)`, `Total execution time (ms)`, `Avg rows returned`, `Times called`. **`Load by CPU`** and **`Load by IO wait`** are derived per query (`total_exec_time` minus block I/O time; `blk_read_time`/`blk_write_time` ≤PG16, `shared_blk_read_time`/`shared_blk_write_time` PG17+). **`Load by lock wait`** has **no per-query source** (`pg_stat_statements` has no lock-time column — §5 divergence table) and renders `n/a (no per-query source, §5)`; lock wait is sourced at database/user level from `pg_locks` + `pg_stat_activity` via R3.7. `Analyze latency` is deferred (Cloud SQL sources it from `auto_explain`, which we sample per R3.10 rather than per statement) and marked **Gap**. **[A]** |
| R3.3 | Compute deltas correctly: `pg_stat_statements` is cumulative, so every rate is `current − previous snapshot`, with the snapshot's `pg_stat_statements_info.stats_reset` (**PG14+**) checked — a reset must reset the baseline rather than produce a negative or wrapped rate. On PG13 (no `pg_stat_statements_info`) fall back to detecting counter decreases per row — **unvalidated on CNPG 1.30.x**, which cannot run PG13 (R13.9). |
| R3.4 | **Fingerprinting.** Key the query store on PostgreSQL's own `queryid`, and additionally compute our own normalized fingerprint (literal-stripped, whitespace-collapsed, keyword-cased template). Reasons, all real: (a) PG18 changes query-id jumbling, so ids change for unchanged queries across a 17→18 upgrade; (b) we need displayable text; (c) we need cross-database grouping. Store the template plus the first-seen raw text truncated per R3.6. The **fingerprint is the canonical key**; `queryid` is informational (R9.5b). |
| R3.5 | Mirror Cloud SQL's normalization rules for the stored template: literals → `$1`,`$2`,… for PG 11+. *(Cloud SQL's `?` form for PG ≤10 is noted in `cloud-sql-insights.md` §3.5 but is dead code inside a PG15–18 spec — documented as a note, not a requirement.)* |
| R3.6 | **Query text length.** Two profiles, matching both Cloud SQL editions rather than picking one: range **256–100 000 bytes**, default **1 024** (Cloud SQL Enterprise's exact `queryStringLength` default), with **10 000** documented as the Enterprise Plus default and available as the recommended production setting. Longer text is truncated and the truncation flagged. *(0.1.0 shipped Enterprise's 256–4 500 range while §1.2 declared Enterprise Plus the baseline, and §5 mislabelled 4 500 B as the Enterprise Plus cap — it is Enterprise's.)* |
| R3.7 | Provide **per-dimension rollups** matching Cloud SQL's tabs: by database, by user, by application name, by client address. Each rollup answers "which X accounts for the most load" with the same load columns as R3.2 — **including** `Load by lock wait`, which is available at this level from `pg_locks` + `pg_stat_activity` even though it is not available per query. |
| R3.8 | Provide **per-query detail**: the per-minute load history for that query (Cloud SQL's "Database load — specific query", minute-boundary aggregation), latency percentiles, plan samples, and the derived recommendations. |
| R3.9 | **Latency percentiles.** Cloud SQL serves P50/P95/P99 from a DISTRIBUTION metric. `pg_stat_statements` gives min/mean/max/stddev, not a distribution. v1 serves min/mean/max/stddev plus a **sampled** histogram built from the `activity` collector's per-session durations (R4), using our own named buckets (`1, 5, 25, 100, 500, 1000, 5000, 10000` ms) and accounted in §5 as R9.13. The UI labels the panel **"sampled, biased toward long queries"** — a 60 s sample of `pg_stat_activity` observes long-running statements far more often than short ones, and an unlabelled histogram overstates p95. Marked **Divergence**: exact P95/P99 per query requires per-execution sampling Cloud SQL itself only gets in Enterprise Plus. |
| R3.10 | **Plan samples.** Capture `EXPLAIN (FORMAT JSON)` for top-N queries on a bounded schedule (default 5 plans/minute/cluster, Cloud SQL's `queryPlansPerMinute` default; Enterprise Plus allows 200 — we expose the knob and default to the cheap value). Three constraints the 0.1.0 text missed: (a) the stored template is normalized (`$1`,`$2`) and truncated, so `EXPLAIN` of it is a syntax error — we EXPLAIN the **first-seen raw text** (R3.4) and **skip truncated rows**; (b) for a parameterized statement we use `PREPARE` + `EXPLAIN EXECUTE` to get the generic plan rather than a pet plan; (c) on a `pg_monitor`-only deployment (R12.4) the role has statistics visibility but **no `SELECT` on application tables**, so EXPLAIN returns `permission denied for table …` — we report `unavailable (insufficient table privileges)` as a state, not as a per-query failure. `EXPLAIN ANALYZE` is **never** run automatically (it executes the statement); it is available on explicit user request, and only for `SELECT`s unless `--allow-analyze-write=true`. |
| R3.11 | Detect **plan changes**: keep plan hashes per fingerprint and surface a plan-change event when the hash changes for a stable fingerprint — the regression detection Cloud SQL gets from `auto_explain` and we get from bounded sampling. **Extension (S43, the Phase 3 review): "per fingerprint" scopes the storage, not the lookup.** The cell says which hashes are kept together; it does not say which row a new sample's *previous* is read from, and the two are separable — `internal/store/statements.go`'s `previousPlanHashSQL` read the newest hash for the fingerprint "whatever database it came from", so a `(pg-1, db2)` sample compared its hash against `(pg-1, db1)`'s and logged `bbb3<-aaa1`, a plan that pair never had. **Ruled:** the previous-hash lookup carries the sample's own identity — R10.7's 6-tuple plus `captured_at < ?` — so a plan change is a hash difference *within one row*. The corollary is what keeps the requirement measurable: a new `(pod, datname, usename)` appearing for an existing fingerprint is a new row, not a change, and without the scoping every new source logs a spurious change on its first round, which is noise in the one series R3.11 exists to produce. |
| R3.12 | **Tags.** Cloud SQL's `pertag` metrics carry `action, application, controller, db_driver, framework, route, tag_hash`, sourced from app-set query tags. Generic PostgreSQL has no tag mechanism; our answer is (a) `application_name`, `usename`, `datname` as the always-available dimensions, and (b) an opt-in documented convention — a structured SQL comment **`/* pgsi:action=…,route=…,controller=…,framework=…,dbdriver=… */`** that we parse out of the statement text and promote to tag dimensions with a `tag_hash`. The comment grammar now names all five app-supplied keys Cloud SQL documents (0.1.0 named three of six). One spelling: `pgsi:` — the spec's earlier `pgsio:` variant is retired. **Divergence**, documented. |
| R3.13 | Respect `recordClientAddress` and `recordApplicationTags` equivalents (`--record-client-address`, `--application-tag-source=application_name|query_comment|off`) so teams with IP-privacy requirements can turn addresses off without losing the rest. |
| R3.14 | **Historical query store**: retain statement snapshots for `retention.statementSnapshots` (default 7 d) and answer week-over-week "is this query slower than last week" from it. |
| **R3.15** | **`pg_stat_statements` enablement as a product flow** *(A3's proposed R17 and A4's proposed R3.15, merged)*. `CREATE EXTENSION` alone fails on a stock CNPG cluster (`required extension "pg_stat_statements" is not loaded`) because `shared_preload_libraries` is postmaster-level and empty by default on all three reachable reference clusters. The requirement is the flow, in this order: **detect** (R3.1) → **offer** (UI action + `pgsi enable pg-stat-statements --cluster X`, gated behind R12.8's `--allow-actions`) → **patch** `spec.parameters.shared_preload_libraries` (documented diff, reversible, never auto-applied to more than one cluster per invocation) → **verify** (poll until the extension is queryable, report the rolling-restart progress per instance). The restart cost is stated in the offer, not discovered afterwards. **Scheduled in Phase 2, not Phase 3**: a rolling restart takes hours, so it cannot be a same-phase prerequisite for the Phase 3 collector, and Phase 3 needs data to be testable. |

### R4 — Active queries and locks

| ID | Requirement |
|---|---|
| R4.1 | Provide a live session list matching Cloud SQL's **top 50**, including both `active` and `idle in transaction` sessions, with the exact Cloud SQL columns: `Process ID`, `Query`, `Query type`, `Status`, `Session duration`, `Transaction duration`, `Query duration`, `Wait event type`, `Wait event`, `Database`, `Application name`, `User name`, `Client address`, `Action`. Plus Cloud SQL's **summary scorecard** above the table (counts by state, longest-running query, longest idle-in-transaction) and its hourglass/block state icons. `Action` renders **disabled with a link to the R12.8 gate** whenever `--allow-actions=false` (the default), so the column is never silently blank. **[A]** |
| R4.2 | Refresh the live list at 15 s (Cloud SQL refreshes at 60 s; we can afford to be fresher because the query is cheap) while keeping the 60 s server-side snapshot for history. |
| R4.3 | **Wait-event analysis**: roll up `wait_event_type` × `wait_event` counts (Cloud SQL's "Wait event types" and "Wait events" tabs). CNPG today exposes only a count (`cnpg_backends_waiting_total`) with no breakdown, so this is net-new. Use `pg_wait_events` (PG17+) to validate and label event names on versions that have it. **Ruling (A3, on `internal/collector/naming.go:187`'s family, asked by A2): R4.3's rollup is not a metric family — it is the Wait Events page's payload — and the catalogue's 25 families are the correct count.** Three texts decide it. (i) R9.1's two-plane rule lists "query text, plan bodies, **active-session rows**, blocking chains → API + store only" and says "Metrics carry the numbers; the API carries the text"; `naming.go:189-192` quotes that rule for this exact view, and a count derived from active-session rows is a number about rows the API already carries. (ii) R5.2 enumerates its backends families as exactly two — "backends by state and by application name" — and R9.7's published set is the union of R5.2's families with R2.4's `pg_stat_database` columns; a wait-event count is neither, so it has no route into the published set, and R9.7's own R14.7 argument is what makes a name in code with no family behind it a defect rather than an addition. (iii) The parity table at :595 maps Cloud SQL's "Wait event types / wait events" surface to R4.3 at **Phase 6a** — a page, not an R9.x export. **Consequence, and it is A1's fix, not A2's:** `naming.go:187` should read `TypeNotExported` the way every other `pg_stat_activity` column in that table does; the row's `src` clause ("R4.3 wait-event rollup; R9.7 names no family for it") is the correct diagnosis with the wrong remedy, because naming a metric in a code table is not documenting it. Filed as A3's S50; `internal/api/promql_catalogue.go:216-227`'s paragraph is the permanent answer for the catalogue side. |
| R4.4 | **Blocking chain**: from `pg_locks` + `pg_stat_activity`, build the blocker→blockee graph using **`pg_catalog.pg_blocking_pids(pid)`** (not a manual lock join) and render the chain up to **3 levels deep**, matching Cloud SQL's blocked-query investigation tree, with the blocking statement text shown for each ancestor. |
| R4.5 | **Terminate session**: `POST /api/v1/sessions/{pid}/terminate` → `pg_terminate_backend(pid)`, gated behind `--allow-actions=true` (default **false**), requiring a confirmed UI action, audited to a log line with actor and target, and refused on a replica. This is Cloud SQL's Enterprise-Plus-only `Action → Terminate connection`. |
| R4.6 | Report idle-in-transaction age separately from active duration, and surface `backend_type` so autovacuum/parallel/walsender sessions are distinguishable from client backends (PG18's new `starting` state must render, not error). |
| R4.7 | Show per-session `query_id` and link it to the R3 query detail when the id is known. |
| R4.8 | **Acceptance is value equality, not presence.** The [A] test asserts the returned pid set equals a direct `SELECT pid FROM pg_catalog.pg_stat_activity` within ±2 s of `capturedAt`, and that each row's durations match the SQL values within tolerance — with both numbers printed. A card that renders `NaN` passes a presence check and fails this one. |

### R5 — System Insights

| ID | Requirement |
|---|---|
| R5.1 | Provide the Cloud SQL summary-card set, computed per cluster: **CPU utilization P99 \| P50**, **Peak connections**, **Transaction ID utilization**, **Disk utilization**, **Log errors**. **[A]** The **Log errors** card is sourced from R5.10 and renders `not configured` when `--log-source=off`; the [A] test asserts each card's value matches the underlying SQL/PromQL within a stated tolerance and prints both. |
| R5.2 | Provide the DB metric families: transactions (commit/rollback/TPS), tuples (returned/fetched/inserted/updated/deleted), blocks (hit/read, cache hit ratio), temp (files/bytes), deadlocks, backends by state and by application name, database sizes, TXID and MXID age, WAL volume and generation rate, checkpoints. |
| R5.3 | Provide the OS metric families (CPU, memory, disk, network) from the container/pod perspective, sourced from S7 when Prometheus is configured, from `metrics.k8s.io` when only that exists, and degraded-with-a-note when neither does. Never render a blank chart without saying why. |
| R5.4 | **Database-count guard**: Cloud SQL collapses to a single aggregated value above 500 databases and only shows `tuple_size` below 50. Adopt both thresholds as configurable limits, because a 1000-database cluster would otherwise explode both our store and our UI. |
| R5.5 | Handle the degenerate paths honestly: single-instance clusters render an empty replication section with an explanation (not a broken chart) — the `cookbook` cluster is exactly this case. **Source the topology from S4's `replicationInfo[]` / `replicationSlotsInfo[]`** (one unauthenticated HTTPS GET, no SQL, no credentials, 27 verified fields) and fall back to S2's `pg_stat_replication` only when S4 is unreachable. |
| R5.6 | **Chart toolbar** parity: drag-to-zoom a time range, **Download PNG**, **Download CSV**, and "View in Prometheus/Grafana" deep link built from the panel's PromQL. Cloud SQL offers exactly these three; we match them per chart, not per page. |
| R5.7 | **Compare up to 5 instances** on one chart (Cloud SQL parity), which requires our series to carry stable instance identity — see the label contract in R9.3. |
| R5.8 | Retain and serve 30 days of system metrics (from Prometheus when configured, from the store otherwise) with a 1-minute auto-refresh, matching Cloud SQL's stated retention and refresh. |
| R5.9 | Surface **events**: Cloud SQL's events timeline plus CNPG's own signal set — phase transitions from S1, Kubernetes events, fencing changes (`cnpg.io/fencedInstances` annotation), promotions (`currentPrimaryTimestamp`), and backup outcomes — as one timeline per cluster. |
| **R5.10** | **Log-source ingestion** *(A3's proposed R18)*. Cloud SQL's "Log errors" card comes from `logging.googleapis.com/log_entry_count`, which has no generic-Postgres equivalent in S1–S9. Add `--log-source=off|pods-log` (default **off**), which with `pods-log` reads the pod log subresource (RBAC `pods/log: [get, list]` added to R12.1), parses Postgres log severity lines, and feeds the R5.1 card and the R5.9 timeline. When off, the card renders `not configured` rather than `0` — an unmeasured card and a zero card are different answers, and conflating them is how a product lies. |

### R6 — Pooler (connection pooling) insights

| ID | Requirement |
|---|---|
| R6.1 | Discover Poolers and render their pool state from S5 (`cnpg_pgbouncer_pools_*`: `cl_active`, `cl_waiting`, `sv_active`, `sv_idle`, `sv_used`, `sv_tested`, `sv_login`, `cl_cancel_req`, …) and S5 lists (`cnpg_pgbouncer_lists_*`). This surface currently has **zero** Grafana panels and **zero** alert rules in the CNPG ecosystem — measured: **80 families / 22 775 B** on `rev-proxy-pg-pooler-…-n9vlv:9127` (`live-probe.md` §2, F4). **Ruling (A3, on the one-source conflict A1 hit from `internal/collector/collector.go:192-195`): two collectors, not an exception to the rule.** `requirements.md:205`'s schedule row gives the pooler two sources — `S5 + S6` — and the tree at :242 repeats it (`pgbouncer/  # S5 + S6`), while `collector.go:192-194` states "one collector, one source" and rules the multi-source reading out for the sibling row (the `replication` row's "S4 as preferred and S2/S3 as fallbacks … is the replication collector's own logic, not a licence to declare two sources"). The pooler row is not a preference order: R6.1 is the S5 HTTP scrape of the pgbouncer exporter and R6.4 is S6 `SHOW CLIENTS` over 5432 — two transports, two ports, two cost classes (an HTTP GET against the exporter's port vs a psql round-trip against 5432), and different applicability (R6.4's attribution applies only under transaction pooling; R6.1's applies to any Pooler). Those are two schedulable units, and `Descriptor.Source` exists to make a collector schedulable, so making it a set — the exception route — changes the R2.1 cost accounting for every collector to fix one row. **Ruled:** `pooler.go` (S5) and `pooler_clients.go` (S6); :205's row stays one *schedule entry* covering two collectors, which is what the replication row already is. A1's citation "§3.3's pooler row" is `requirements.md:205`, R2.1's schedule table — IMPLEMENTATION.md §3.3 is Phase 0's requirement→file table and has no pooler row. |
| R6.2 | Render pool utilisation: clients waiting vs. servers active per (database, user), the client/server ratio, and pool saturation (`cl_waiting` sustained > 0) as the headline number. |
| R6.3 | Render throughput from `cnpg_pgbouncer_stats_*`: `total_query_count`, `total_xact_count`, `total_received`, `total_sent`, `total_wait_time`, `avg_*` variants, with the `stats_reset` counter-reset handling from R3.3. |
| R6.4 | **Attribution fix, scoped.** Cloud SQL's documented answer for pgbouncer is to *disable* client-IP attribution rather than mis-attribute it (IPs render as `local`). We do better, but only where it is derivable: under **transaction pooling a `pg_stat_statements` row has no session at all**, so per-query client attribution is impossible and we do not claim it. Attribution applies to (a) the **R4.1 active-session list**, via `SHOW CLIENTS` (S6) mapping each server backend to its real client address, and (b) the **R3.7 by-client-address rollup**, which Cloud SQL itself models as a separate dimension. Config: `--pooler-attribution=off|cloudsql|pgbouncer` (default `pgbouncer`, with `cloudsql` reproducing Google's conservative `local` behaviour). R3.2 per-query rows carry the **pooler name** as their connection route, not a client address. |
| R6.5 | Surface pooler config (`poolMode`, `default_pool_size`, `max_client_conn`, `min_pool_size`, `type`) alongside the utilisation so a mis-sized pool is visible next to its symptom. |

### R7 — Recommenders

| ID | Requirement |
|---|---|
| R7.1 | **Index recommendations** *(post-0.1)*: derive candidate indexes from `pg_stat_statements` (predicates and join columns parsed out of the stored statement text) cross-checked against `pg_index`/`pg_stat_user_indexes` usage, ranked by estimated load addressed. This is the Enterprise-Plus index advisor, rebuilt from generic sources; it is a *candidate list with reasoning*, and the spec is explicit that it is weaker than Google's, which has the full plan. **Moved post-0.1**: it depends on R3.15 being executed on a real cluster, and its v0.1 quality bar cannot be met by a parser written in the same phase that ships it. |
| R7.2 | **Table and index bloat**: per-table dead-tuple ratio from `pg_stat_user_tables` plus size from `pg_total_relation_size`; unused-index detection from `pg_stat_user_indexes.idx_scan = 0` with size. |
| R7.3 | **Rightsizing** *(post-0.1)*: requested CPU/memory (from `cnpg.io/podSpec` annotation / pod spec) vs. actual usage, with a recommendation direction. **Marked CNPG-derived, not a Cloud SQL clone**: Cloud SQL's rightsizing recommender sizes a *managed instance* against Google's fleet; the CNPG analogue compares a pod's requests against its cgroup usage, which is a different computation with a different answer. Scope it to "are the pod's CPU/memory requests plausible for the observed usage", and say so. |
| R7.4 | **Autovacuum insight**: last/next vacuum and analyze per table, tables over the autovacuum threshold, and tables never analyzed. **Marked CNPG-derived** — this is CNPG dashboard gap #13 (`cloudnativepg.md` §8.2), not a Cloud SQL recommender; the spec's own Divergence/Gap rule requires the label. |
| R7.5 | **TXID wraparound**: per-database age with the 1.5 B warn / 1 B critical bands, and the `LongRunningTransaction`/`PGDatabaseXidAge` thresholds surfaced as findings, not just as Prometheus alerts. |
| R7.6 | Every recommendation carries: the finding, the evidence (metrics + query fingerprints), the suggested action, and a `confidence` field. Recommendations the data cannot support are suppressed, not guessed. |

### R8 — HTTP API

| ID | Requirement |
|---|---|
| **R8.1** | Versioned REST API under `/api/v1/*`, JSON. **The OpenAPI half of this row is now three separately-statusable clauses** (D13, A3's review of `f9aae41`: bundled into one sentence, "implemented" was claimable while two of the three were false — the same report-without-enforcing shape A3 flagged in the parity parser, in a document whose whole purpose is to make claims checkable). **(a) Committed at the path this row names** — `api/openapi.yaml`, repeated verbatim in the §5 repo tree. The path is normative; the Phase 5 file's location was not. It sat at `internal/api/openapi.yaml` "so `go:embed` can serve it", but nothing embedded it and no route served it, and the real constraint is narrower than the reason given: a `//go:embed` pattern cannot contain `..`, so a file outside `internal/api/` is unreachable from `internal/api/spa.go`. The fix is a three-line `api/embed.go` carrying `//go:embed openapi.yaml` plus an accessor — not a spec amendment. **(b)** generated from the handlers → **R8.1b**. **(c)** checked in CI → **R8.1c**. |
| **R8.1b** | **The OpenAPI document is generated from the code, not authored beside it** (split out of R8.1 by D13 so it can be false while (a) and (c) are true). **Implemented, with three named authored exceptions** (the cell read **Open** while the document was hand-written, and "Go's stdlib has no OpenAPI generator" was the reason on record — true of the toolchain, and not a reason). The generator is `internal/api/openapi_gen.go`'s `GenerateOpenAPI()`; `make openapi` (`Makefile:112-113`) runs it with `-update` and `TestGeneratedOpenAPI` fails the build when the committed file differs from its output, which is what turns this clause from a claim into a gate. Its header names four inputs — the route table (a `go/ast` walk over the package's non-test files, resolving package-level string constants so `"GET "+ClusterDetailPath` expands to the string the router and the tests use), the dto structs (checked by `openapi_schemas.go`, rendered by `openapi_emit.go`), the planned manifest (`api/x-planned-paths.yaml`, re-emitted verbatim), and the prose (`openapi_annotations.go`). The spec names the first three; the fourth is the half the D13 split did not anticipate, and it is how the extension became declared input rather than authored content — which is still why R8.2's named endpoints sit there rather than in `paths`: "planned" means named by the spec and not yet in code, the one set code cannot enumerate. **The three exceptions are named, not counted:** `PromQLSeries` and `QueryDetailView`, composed with `allOf` when the emitter generates flat objects only, and `SamplePair`, whose `MarshalJSON` emits an array form its named fields do not describe. 29 of the document's 32 schema entries are marked `Generated`, and `TestEmitMarkedEntries` requires a marked entry's render to equal its committed block byte-for-byte, so the next flip is a per-entry act with a test behind it rather than a percentage that creeps. The generator's input should be the **dto**, not the handlers: R16.8's lesson is that the DTO is the contract the UI transcribes, so a document derived from it stays in step with the types `web/src/api/` imports, whereas a generator over five handlers documents five routes and omits the thirteen the spec names. **The input artifact, named** (so R8.1b's completion is a buildable thing and not a direction): three of them, not one. **(1) The route table** — the `mux.HandleFunc("METHOD /path", h)` registrations themselves (`server.go:137-155`, `PromQL.Mount` at `promql.go:262-265`), because a dto carries no methods and no paths and a generator over struct types alone cannot know that `GET /api/v1/metrics/query` exists. **(2) The dto structs** — `internal/api/dto/*.go`, whose json tags are the property schemas; this is the half that keeps the document in step with the types `web/src/api/` imports (R16.8). **(3) The planned-route manifest**, below. The count, and the rule that pins it: the document now has **17 `paths` / 19 operations** and the code has **19 registrations** — 15 `mux.HandleFunc` calls in `server.go` plus the four in `PromQL.Mount` — so the one-for-one invariant the sentence exists to defend still holds, and it is the number that moved rather than the document. The row's own rule decides who is wrong: "it is the validator's number the row must agree with", and `scripts/check-openapi.py` reports 17 paths, 19 operations. What this replaced read "its 13 `paths` / 15 operations map one-for-one onto the 15 registrations in those two files — 15 operations, not 17", which was true of the document as authored at Phase 5 and survived R8.3's two metrics routes, the R8.11 series route and `cf2f5aa` without an edit — R14.9's per-owner failure, in the row that sets the rule. The two `x-promql-catalogue` keys beside `get`/`post` under the two metrics paths are still extensions and not operations, and counting every key under a path item is still how the number inflates; that distinction is what "not 17" was for, and it survives the count changing underneath it. **`x-planned-paths` survives generation, and must be demoted from authored content to declared input.** "Planned" means *named by the spec and not yet in code* — the one set code cannot enumerate — so a generator that owns the whole document deletes the extension's 19 routes the day it lands and R8.2's endpoint list stops being checkable from the doc. The file already contains the pattern to copy: `x-promql-catalogue` is an extension whose content is checked against a code-side table (`internal/api/promql_catalogue.go`) by `TestCatalogueMatchesOpenAPIDocument` and `scripts/check-openapi.py`. `x-planned-paths` gets the mirror image — a checked-in manifest the generator reads and re-emits verbatim, never regenerates. The invariant that makes it enforceable, and the one R8.1c's validator should own: **`paths` and `x-planned-paths` are disjoint**. A route in both is a defect — the doc calls it planned while code serves it — which is what happened twice by hand (`r8.3-promql-pair` and `r8.7-debug`, each deleted with a note when its routes landed); generation makes the move automatic and disjointness is what tells you to make it. `x-status` likewise becomes derived from the route table, and the one thing it cannot derive is the partial case a boolean can't hold — `/metrics`'s `implemented (pgsi_build_info only; the R9.2 catalogue is Phase 2)` — so the generator must accept it as a per-route annotation rather than compute it. |
| **R8.1c** | **The document is validated in CI** (split out of R8.1 by D13 for the same reason). A CI job runs an OpenAPI validator (`openapi-spec-validator`, `spectral`, or equivalent) over the committed document so a malformed or stale doc fails the build. `.github/workflows/ci.yml`'s jobs are build / lint / test / web / helm / image; before this amendment none of them touched the document, which is why D13 recorded the clause as false while the Phase 5 row still read `implemented`. The step belongs in the `lint` job beside `scripts/check-parity-matrix.sh`, which is the existing precedent for a doc-consistency gate that can fail the build. |
| R8.2 | Core endpoints: `/clusters`, `/clusters/{ns}/{name}`, `/insights/queries`, `/insights/queries/{fingerprint}`, `/insights/queries/{fingerprint}/plans`, **`/insights/queries/series?fingerprints=a,b,c&range=…`** (batched sparkline series — see R8.11), `/insights/wait-events`, `/insights/databases`, `/insights/users`, `/insights/tags`, `/insights/client-addresses`, `/insights/system`, `/insights/system/{metric}`, `/active-queries`, `/insights/recommendations`, `/poolers`, `/poolers/{ns}/{name}`, `/events`, `/explain`, `/search?q=`. |
| R8.3 | **The PromQL pair**, mirroring Cloud SQL's Database Insights MCP API shape (`FetchQueryMetricsRequest{parent, resource, promqlQuery, startTime, endTime}` → `{promqlResult}`): `GET|POST /api/v1/metrics/query` and `/api/v1/metrics/system`, each accepting a PromQL string + time range and returning a Prometheus-shaped result. When a Prometheus URL is configured, the query is **proxied** to it. Otherwise — because §3.4 keeps raw samples out of SQLite and we ship no PromQL evaluator — the pair serves a **fixed catalogue of named queries** (the R9.4 aggregate and R9.5 per-query rollups, serialized in Prometheus result form) and returns `error.code=PromQLUnavailable` for anything else, with the catalogue enumerated in the OpenAPI doc. Saying "we answer from our own store" without an evaluator is a promise the code cannot keep. |
| R8.4 | Every list endpoint supports `?namespace=`, `?cluster=`, `?db=`, `?from=`, `?to=`, `?limit=`, `?sort=`, and returns a stable envelope `{data, meta:{total, truncated, generatedAt, nextCursor}}`. **`meta` also carries the partial-render contract** (A1 `93fcd44`, answering the gap R10.22 opened and R8.4 predated): `meta.failed[] = {namespace, code, message, lastSuccessTime?}`, where `code` reuses the R8.5 enum rather than minting a second vocabulary, and `meta.loading[] = [namespace]`. The unit is the **namespace**, not the cluster, because that is the unit discovery fails at (R1.1's allow-list and R12's RBAC both scope by namespace) — a namespace whose watch errored contributes no `ClusterView` to hang an `error` field on. **`meta.total` is R8.10's pre-cut count** — matching rows across all pages, exactly as `dto/clusters.go` documents it (`// Total is the number of matching objects before the page was cut`) and exactly what `truncated` is defined against (`truncated ⟺ nextCursor != ""` — the shorthand `truncated ⟺ len(data) < total` that D18 originally printed here holds **only on the first page**: `total` is the pre-cut count and carries no offset, so page 2 of a 4-row `limit=2` result has `len(data)` 2 and `total` 4 with `truncated` false, and the last page of every multi-page result contradicts it. The code and `TestClusterPagination` were right; the equation was the defect, corrected in D18's second amendment). The v0.3.0 wording here said total "counts what the response can serve, not what was attempted", which collides with R8.10 on any truncated page (A3 S1): with 120 matching and `limit=50`, "served" is 50 and R8.10's total is 120, so the two documents assign different numbers to the same field. That wording also implied `attempted = total + len(failed) + len(loading)`, which adds **rows to namespaces** — a namespace yields N clusters, so no arithmetic over `total` recovers an attempted-cluster count. Namespace accounting is therefore carried by `meta.failed[]` / `meta.loading[]` themselves and never derived from `total`. `truncated` stays purely R8.10's pagination flag: a page can be complete and have a failed namespace, or truncated and have none. No server-side `message` — the banner's wording is R10.22's, not the API's. |
| R8.5 | Consistent error body `{error:{code, message, details[]}}` with codes in a documented enum, modelled on the CNPG instance-manager envelope so the ecosystem's error-reading code transfers. |
| R8.6 | Serve the SPA from the same binary via `go:embed`, with `/` → `index.html`, correct asset caching, and `--serve-ui=false` for API-only deployments. |
| **R8.7** | `GET /healthz` (the process is up and the mux is serving), `GET /readyz` (the process will serve *correct* data), `GET /debug/pprof/*`. **The readiness corollary** (A3's re-review of the R8.9 ruling — making `/readyz` unconditionally exempt is what makes this row need three clauses instead of one): **(i) one readiness signal, read by both surfaces.** R8.9 now makes `/readyz` load-bearing for the kubelet *and* the Service, and R8.5's `ErrNotReady` is the API's expression of the same fact — so it must be a single flag, set by the discovery loop and read by both `/readyz` and the API's 503 path, or the probe and the API can disagree. That disagreement is Phase 1a's actual bug: `server.go:119` set `ready` immediately after `net.Listen`, so a pod answered `200` with an empty fleet while claiming ready, and R8.9's unconditional exemption promotes it from a client-visible bug to a Service-routing fact. **(ii) readiness covers only the subsystems *this process* runs.** Discovery's first sync is required when the process runs discovery; D9's reserved `api` role (serve without collecting) would be permanently unready under a discovery-only rule, so state the general rule now rather than at the role split. `--serve-ui=false` (R8.6) still discovers in v1 and is unaffected. **(iii) the two probes answer different questions** — healthz = up, readyz = correct — which is the distinction Phase 1a collapsed and R8.9 now makes load-bearing. |
| **R8.8** | Auth: `--auth=none|token|kube-rbac-proxy`. Default `token` with a bearer token from a Secret; `none` for lab. **Two clauses the v0.2.0 wording left implicit, both ruled in D14:** **(i) A token mode with no token is a startup error, not an accept-anything default.** `--auth=token` with neither `--auth-token` nor `--auth-token-file` resolving to a non-empty value exits non-zero at startup rather than accepting `Authorization: Bearer `. The row's own wording makes the token part of the default — "Default `token` **with a bearer token from a Secret**" — and an empty comparison string authenticates any request that presents the scheme, which is the "auth looks on but isn't" state this row exists to prevent. The behaviour change for a hand-run binary is documented in the README quickstart and `IMPLEMENTATION.md` §6's run block, both of which now read `./bin/pgsi serve --auth=none`; the chart's default profile emits `--auth=none` from its `auth.enabled: false` branch, so no installed profile hits the new exit. A **bad** token answers `401 Unauthorized` (R8.5's code for "missing/bad token"), never `403 Forbidden` — `Forbidden` is R12's scope verdict, and one code cannot answer both an authentication and an authorization failure without costing the client the distinction. **(ii) Separate auth means a separate secret, not merely a separate mode.** `/metrics`'s credential is `--auth-metrics-token` / `--auth-metrics-token-file`, **falling back to `--auth-token` / `--auth-token-file` when unset.** The first implementation gave `--auth-metrics` a mode but no secret of its own — one `Token`/`TokenFile` pair in `AuthzConfig`, the identical string handed to both groups — so `--auth=token --auth-metrics=token` forced Prometheus to hold the API token, which is precisely what the `--auth-metrics=none` parenthetical exists to avoid. The fallback keeps the common single-token deployment at one flag and makes the split deployment expressible. |
| **R8.9** | **CORS configurable, and which endpoints the auth gate covers.** CORS configurable via `--cors-allow-origin` (with `--cors-allow-credentials` and `--cors-max-age`). The v0.2.0 text named no flag at all; the first draft of this amendment said `--cors-origins`, which is not what `cmd/pgsi/main.go:94` and `internal/api/cors.go:29` spell — the spec now follows the code, which matches the `Access-Control-Allow-Origin` header it configures and its sibling flags. **`/healthz` and `/readyz` are never auth-gated, under any value of `--auth`** (A3 S2, closing the gap the v0.2.0 wording left): they are kubelet probes, not API surface, and a kubelet `httpGet` probe cannot present a bearer token unless the chart sets `httpGet.httpHeaders` with the token value in the pod spec — which it does not (`chart/templates/deployment.yaml` mounts the token secret as a *file*), so with `--auth=token` the probe gets `401` and the pod never goes Ready. A gated probe is a broken pod rather than a preference, so the exemption is unconditional and not keyed to `--auth-metrics`. `/metrics` is gated by `--auth-metrics` (default `none`, so Prometheus need not hold a token — R8.8). `/debug/pprof/*`, the third surface R8.7 names, follows `--auth`: it is introspection, not a probe, and the default `token` keeps it behind the token. The v0.2.0 text named only `/metrics` and `/healthz`, silently leaving `/readyz` and `/debug/pprof/*` unaddressed. **Resolved in `f9aae41` — the group has since split, as this ruling required.** `internal/api/auth.go` now declares three groups — `GroupAPI` (`/`, `/api/v1/*`, `/debug/pprof/*`), `GroupMetrics` (`/metrics`), and `GroupProbe` (`/healthz`, `/readyz`) — and `GroupProbe` is modelled as the *absence* of an `Authenticator` rather than a field that could be set to `token`, so no future flag can re-gate it; `cmd/pgsi/main.go`'s `--auth-metrics` help text now reads "/metrics alone". The note had been written in the present tense about code that had already changed, and its cited range (`auth.go:275-305`) now lands on `writeAuthError` and the group const block rather than on the single-group implementation it described. That is the failure mode worth naming: **an implementation note written in the present tense keeps reading as an open gap after the gap closes**, so it is now dated and marked resolved rather than maintained in place. The divergence it warned about — the two readings differing only at `--auth-metrics=token`, the one setting the v0.2.0 wording would have gated and this ruling exists to protect — no longer exists. |
| **R8.10** | **Pagination contract.** `?limit=` default **50**, max **500**; `?cursor=` opaque (base64 of the last sort key), not an offset; every sort has a **stable secondary key** (`queryid` for query lists, `pid` for sessions) so two consecutive pages never overlap or drop a row. `meta.truncated` is true whenever more rows exist. |
| **R8.11** | **Batched series endpoint.** `GET /api/v1/insights/queries/series?fingerprints=a,b,c&range=…` returns the per-minute series for up to 50 fingerprints in **one** response. Without it, R10.6's per-row sparklines need one request per row, and R10.14's 1.5 s budget forbids 50 detail calls on page load. |

### R9 — Prometheus metrics export (the multi-instance centralization requirement)

| ID | Requirement |
|---|---|
| R9.1 | **The two-plane rule.** Every capability is tagged with the plane that serves it, and this is documented in the chart README: **basic metrics → `/metrics` → Prometheus**; **query text, plan bodies, active-session rows, blocking chains → API + store only**. Metrics carry the numbers; the API carries the text. This is the direct answer to "some features won't make sense to fall back that way". |
| R9.2 | Prefix all exported metrics `pgsi_` (configurable `metrics.prefix`) to avoid collision with `cnpg_`, `pg_`, and `cAdvisor` families in a shared Prometheus. |
| R9.3 | **Label contract** (normative, so dashboards survive multi-instance scraping): every series carries `namespace`, `cluster`, `pod` (instance), and `instance_role`; pooler series carry `pooler`; database series add `datname`; user series add `usename`. Instance identity is `(namespace, cluster, pod)` and is stable — the same triple R5.7 needs for compare-up-to-5. **Ruling (A3, on `internal/collector/activity.go:842-849`'s flag): the per-family half enumerates; only the base set closes.** The cell is two clauses with different modal force. "every series carries `namespace`, `cluster`, `pod`, and `instance_role`" is closed — it is the required base set and no family may drop it. "pooler series carry `pooler`; database series add `datname`; user series add `usename`" is a mapping from family-kind to the extra label that kind needs, not a ceiling on the label vocabulary, and the proof is inside the spec: **R9.5b mints `fingerprint` and `queryid` as labels of the per-query family**, neither of which is among R9.3's enumerated seven, and no review has ever filed R9.5b as a violation of R9.3. A closed reading makes an accepted, cited cell non-compliant in order to police a question nobody asked. What *does* follow from the purpose clause ("so dashboards survive multi-instance scraping") is a shape rule rather than a vocabulary rule: a per-family label must be a dimension the family's own source view groups by, and the family name must name that dimension — which is what `pgsi_db_backends_by_state` + `state` does, and what R9.4's "per (cluster, db, user)" naming does for `datname`/`usename`. On that rule `state`, `application_name`, and `wait_event_type`+`wait_event` are admitted to the three families `activity.go` puts them on, and A1's rejected alternative (one family per state value, seven families for one grouped count) stays rejected. `activity.go:137`'s "the label contract is a closed set (namespace, cluster, pod, instance_role, pooler, datname, usename)" is the base-set claim and is correct where it sits; where the same sentence is read as covering the per-family additions it should read *enumerated*, not *closed*. |
| **R9.4** | Export the **aggregate** family (Cloud SQL's `insights/aggregate` equivalent): execution time, rows, shared block hits/reads, **lock time**, per (cluster, db, user) — bounded cardinality, always on. **`lock_time` is a post-0.3.0 scope change to a frozen requirement, added by ruling rather than by drift** (the R9.11 ruling from A3's `f9aae41` review, accepted by the spec owner). The v0.2.0 list named exactly four members and omitted lock wait; three documents now ask for it at this exact granularity. **(i)** §5's own divergence table already commits to it — "Per-query `lock_time` with `lock_type` → `n/a` per query; **available at database/user level** … we derive lock wait at database/user level from `pg_locks` + `pg_stat_activity`" — and R9.4's granularity *is* per (cluster, db, user), so the export row omitted a value the parity table promises. **(ii)** The §11 ruling on the R3.2 load columns sources all three of them at database/user level via R3.7. **(iii)** The chart's `PGSILockWaitDominant` alert reads `pgsi_query_lock_time_seconds_total` and cites "R9.4's lock_time member"; R9.11 needs that alert named now, because Phase 4's acceptance is `promtool check rules`, which validates expressions and a stub has none — so the alternative to naming the member was deleting the alert. The chart's speculative-name caveat stands unchanged (its header separates the two invented rules from the fourteen whose metrics the spec names, so if Phase 2 chooses a different name only that rule changes). **Ruling (A3, on the ask A2 routed from `internal/api/promql_catalogue.go` and A6 filed in `web/src/components/QueryLoadArea.tsx`): yes — `io_time` joins the list as a derived member, the sum of the two block-time counters, by the same mechanism that admitted `lock_time`.** R10.5(a) says the Query load chart plots "total execution time and IO time, **from S2/R9.4**", and this cell had no IO member to serve it: the same shape as the lock_time case this cell already documents — another document naming a measure and citing this cell for it — and the cell's own wording for that class is "a post-0.3.0 scope change to a frozen requirement, added by ruling rather than by drift". **Why the sum, not read-only:** R3.2 defines `Load by IO wait = blk_read_time + blk_write_time` (PG 15/16) and `shared_blk_read_time + shared_blk_write_time` (PG 17+) — `internal/api/dto/queries.go:76-79` — and the Query Detail page already renders that scalar for every fingerprint; a read-only member would draw a chart band that is not the number the detail page shows for the same query, so the two views would disagree about one query. The sum is the only value that keeps them consistent, and it is the sum the collector already mints as two counters at `internal/collector/naming.go:135-136`. **Why not two members (`io_read_time` + `io_write_time`):** R10.5(a) names one band, singular; splitting it makes the detail page's single scalar a projection of two metrics, which is a second derivation the spec never asks for, and it repaints `--series-2`, a published colour that currently means IO time. **Why not "stay at five and fix the citation":** the value *is* reachable from R9.7's system family (`pg_stat_database.blk_read_time` is in R9.7's union per the D20 ruling), but R8.3 scopes `/api/v1/metrics/query`'s catalogue to "the R9.4 aggregate and R9.5 per-query rollups" — so an R9.7-sourced IO time is not reachable from the endpoint the Query load chart reads, and R10.5(a) would name a measure the surface never renders. The parity matrix checks requirement↔phase, not requirement↔field, so nothing would ever catch it. **This does not reopen A3's S25:** A3's S25 closed R9.5's *per-query* member list and withheld `lock_time` from it on a granularity-specific argument; the two lists are declared separately — that separation is what A3's S25 created — and `lock_time`'s admission here did not reopen it either. **Cost, stated so it is not discovered later:** one catalogue entry plus one predicate. `internal/api/promql_test.go:955` pins `cache_hit_ratio` as the only entry allowed an empty `Metric`; a derived sum has no single metric to name, so that exception widens from one name to a set — the collector side already carries the plural precedent at `internal/collector/naming_catalogue_test.go:34`. **Ruling (A3, on `internal/collector/naming.go:115`'s key type, asked by A1): the key widens, so a pgbouncer family can be named.** `exportNames` is `map[pgversion.Source]exportEntry`, and `pgversion.Source{View, Column}` can only name a PostgreSQL catalog column — so the 51 pgbouncer families in `internal/collector/cnpgmetrics/registry.go` cannot enter the table, cannot be named by `MetricName` (:52), and are absent from `ExportedMetricNames()` (:523-524). This is not pgbouncer-specific: `internal/collector/cnpgmetrics/registry.go:34-42` already documents 24 of the 59 cnpg families as unmappable for the same reason ("the rest have no PG column at all (`cnpg_backends_total` is a `COUNT(*)` over `pg_stat_activity`, `cnpg_pg_database_size_bytes` is `pg_database_size()`)") and gates the re-export bridge on this table covering the surface — a gate the key type makes unreachable. R9.7 requires cnpg values be "re-exported under `pgsi_`", which requires them to be nameable, which requires the table to hold them. **Ruled:** the key becomes a general source descriptor (surface + member name) with `pgversion.Source` feeding it for the SQL views — A1's option 1 — because `registry.go` forbids a second naming table citing R16.8 and both alternatives (let `cnpgmetrics` mint the names, let `pooler.go` mint its own) mint one. The signature churn across `MetricView`/`MetricName`/`Canonical` and the two naming tests is the cost of the one-table rule and is paid, not avoided; it also unblocks the 24 cnpg families `internal/collector/cnpgmetrics/registry.go:36-38` lists, which is the argument that this is Phase 8 cashing a Phase 2 debt rather than scope creep. |
| R9.5 | Export the **per-query** family (Cloud SQL's `insights/perquery`) for the **top-N only**: default **N=50 per cluster** (see §5 — `metrics.topN` is per cluster, not per database), range 0–1000. N=0 disables the family. This mirrors postgres_exporter's `--collector.stat_statements.limit` (default 100) as the cardinality lever. **Ruling (S25, from A3's review of the Phase 3/6b surface): this cell's open question — which metrics are in the family — is answered `execution_time`, `rows`, `shared_blocks_hit`, `shared_blocks_read`, and not `lock_time`.** R9.4's `lock_time` was admitted to the *aggregate* family on a granularity-specific argument (§5's divergence table renders per-query `lock_time` `n/a` and sources it at database/user level, which is exactly R9.4's granularity), so the value exists at R9.4's level and not at this one; the four named members are the R3.2 per-query sources that exist per query. `internal/api/promql_catalogue.go` had been minting `pgsi_query_lock_time_seconds_total` into the per-query family by iterating the same `aggregates` slice it builds the aggregate family from (`:152`), which settles this cell from a data-structure shape instead of from the spec; the two families now declare their member lists separately. The alternative — sourcing per-query lock time from R4's activity sampling the way R3.9 sources the latency histogram — was rejected: R3.9's sampled source is scoped to that histogram and accounted in §5 as its own entry (R9.13), so extending it would *add* a source the spec never named, which is a scope change rather than a ruling on an open one. A5's store (`internal/store/schema.go`) already defaults `lock_wait_ms` to -1 for exactly this reason, so the field advertised a number R3.2 says cannot exist. |
| **R9.5b** | The per-query family's identifier label is the **`fingerprint`** (canonical, joins to the API), with `queryid` as a secondary informational label. R3.4 exists precisely because `queryid` is unstable across a PG 17→18 upgrade; labelling the family with `queryid` alone forks every user's saved PromQL on upgrade. |
| R9.6 | Export the **per-tag** family (Cloud SQL's `insights/pertag`) when R3.12 tags are enabled, keyed by `tag_hash`. |
| R9.7 | Export the **system** family: the full R5.2 set, plus the parsed `cnpg_*` values re-exported under `pgsi_` so a user without CNPG's PodMonitor still gets one consistent namespace. **"The full R5.2 set" is a floor, not a ceiling (D20).** R5.2 names *families* in prose — transactions, tuples, blocks, temp, deadlocks, backends, database sizes, TXID/MXID age, WAL, checkpoints — which is a capability list and not a closed set of metric names: it does not name `conflicts`, `checksum_failures`, `blk_read_time`, `session_time`, the `sessions_*` counters or PG18's `parallel_workers_*`, all of which `pg_stat_database` has and R2.4's column set selects. The export set is therefore the **union of R5.2's enumerated families and the numeric columns R2.4's matrix returns for the target major**, and R9.4's naming function must enumerate that union: R14.7 admits no metric that exists in code but not in docs, so exporting a column R9.4 never named is a violation, and naming only what R5.2's prose enumerates leaves R9.7 unmet on every major where the view is wider than the prose. R13.4's computed series count is measured against the same union. **Ruling (A3, on `internal/collector/wal.go`'s flag): the union is over *the matrix's* column set for the target major, not over PostgreSQL's real one**, so a column the matrix does not carry cannot enter the union and no collector is non-compliant for lacking it. `pg_stat_wal` is the live case — R2.4's set for it is the nine PG14–17 / five PG18 **statistic** columns (`matrix_data.go`'s `StatWALPG14to17` / `StatWALPG18`) while real `pg_stat_wal` has eleven columns on PG14 and seven on PG18, because `datid` and `datname` are the two R2.4's count excludes and it excludes them for the reason its source gives (`cloudnativepg.md:2099-2107` counts statistic columns there, where its `pg_database` and `pg_stat_database` sets do include `datname`). The matrix therefore matches its own spec, and adding `datname` is an **R2.4 amendment** — which would invalidate `matrix_data.go`'s generated golden and re-open a validated phase — not a matrix edit, and not Phase 2's to make. Until such an amendment lands, `wal.go`'s answer is the cluster-wide sum over the rows: Σ `wal_bytes` is R5.2's "WAL volume" (and `rate()` over it the "generation rate" R5.2 names), it keeps R13.4's count computable from this union — 8 families from `pg_stat_wal` on 14–17, 4 on 18 — instead of scaling with the number of databases a cluster happens to have, and it avoids D20's S8 class, where N rows against one unlabelled family is N samples with identical label sets: an invalid exposition whose surviving value is whichever row the driver returned last. If R2.4 is later amended to carry `datid`/`datname`, per-database series become possible *in addition* and the sum stays as the unlabelled aggregate beside them, so nothing in `wal.go` has to be undone. **Ruling (A3, on the rename rule A1 asked for): drop the source prefix, keep the member name verbatim, add the unit suffix only where the source declares one, then apply R9.2's prefix** — so `cnpg_pgbouncer_pools_cl_active` → `pgsi_pgbouncer_pools_cl_active`. This is the rule `naming.go` already implements and never states: `pg_stat_activity.backends_by_state` → `pgsi_db_backends_by_state` and `pg_stat_statements.shared_blk_read_time` → `pgsi_query_shared_blk_read_time_seconds_total` both discard the source prefix and keep the member, the second adding `_seconds_total` because the source is a counter. `cnpg_` looks like it is doing work in the middle only because it is the *source* prefix — the part the rule discards — and R9.2's configurable `metrics.prefix` is the outer layer, never the inner one. Stated here rather than only in `naming.go`'s doc because R9.7's own sentence ("the parsed `cnpg_*` values re-exported under `pgsi_`") is the text that is underspecified, and a rule that lives only in a code comment cannot be reviewed. |
| R9.8 | Export **self-observability**: `pgsi_build_info{version,commit}`, `pgsi_collector_duration_seconds`, `pgsi_collector_errors_total`, `pgsi_scrape_target_up{cluster,pod}`, `pgsi_store_size_bytes`, `pgsi_http_request_duration_seconds`, `pgsi_scheduler_queue_depth`, `pgsi_scheduler_shed_total` (R2.9). |
| R9.9 | Ship a **ServiceMonitor** (and a **PodMonitor** variant) in the chart, off by default and enabled by `metrics.serviceMonitor.enabled`, correct for the installed `monitoring.coreos.com/v1` API — validated against the reference environment's kube-prometheus-stack. **[A]** |
| R9.10 | Document the cardinality budget (§5) in the chart README with a measured number for the reference environment, so an adopter can predict their Prometheus bill before installing. |
| **R9.11** | Ship a **PrometheusRule** in the chart with a `critical` tier, which CNPG's own `cnpg-default-alerts` lacks (all 7 of its rules are `warning`). Ours ships ≥10 rules across both severities, each with `summary`/`description`/`runbook_url` annotations — the runbook linkage gap from the CNPG alert set. *(This is 0.1.0's `R9.9b`; the letter suffix broke the ID scheme and is retired — A3-N2.)* |
| **R9.12** | **`pgsi_up{namespace,cluster,pod,instance_role}`** — one series per discovered instance, `1` when our last collection round for that instance succeeded, `0` otherwise. §8 check 3 and Phase 5's acceptance both assert `pgsi_up`, and 0.1.0 defined no such metric (R9.8 had only `pgsi_scrape_target_up`). It carries the full R9.3 label set including `pod`, so a per-instance alert and a per-cluster one are both writable. |
| **R9.13** | **Sampled-histogram series accounting** *(A3's proposed R21)*. The R3.9 histogram is accounted in §5's budget: 50 fingerprints × (8 buckets + `_sum` + `_count`) = **450 series per cluster**. It is a Prometheus histogram over *sampled* durations, not a true distribution, and §5 says so rather than leaving the family uncounted. |

### R10 — Web UI and visualizations

| ID | Requirement |
|---|---|
| R10.1 | Single-page app (React + TypeScript + Vite), built and embedded into the Go binary via `go:embed`; no separate web server, no CDN dependency, works fully offline inside the cluster. |
| R10.2 | **Two claims, split per A3-N2 (v0.3.0).** **(a) Design system** — the colour roles, fixed categorical order, mark specs and interaction contract come from the **`dataviz` skill**, which must be read before any chart code is written, and the resulting tokens recorded in `IMPLEMENTATION.md` §4 as a dated entry. **(b) Chart library** — the skill is a *method* skill and names no library by design, so the library is the named default **`@visx` over `d3-scale`** (no Chart.js/Recharts wrapper), with the skill consulted first in case a later version names one. The v0.2.0 wording ("Chart library ... chosen by the `dataviz` skill") made (b) read as skill-chosen when the skill's own `components.md` prescribes plain HTML/SVG, so the fallback branch fired always and the headline could never be literally true. A deferred choice with no default is not a requirement. |
| R10.3 | **Pages**: Fleet, Cluster Overview, Queries, Query Detail, Active Queries, Wait Events, **Databases**, **Users**, **Tags**, **Client Addresses**, System Insights, Poolers, Recommendations, Events Timeline. The four rollup pages are required by R3.7 and R8.2 and were missing from this list in 0.1.0. Each maps to a requirement above. |
| R10.4 | **Fleet**: health distribution (one stacked bar + counts), a sortable table of every cluster with phase, instances, PG major, TPS, p95 latency, connections %, disk %, and the top finding; click-through to any cluster. This is the biggest structural gap in the CNPG ecosystem today (the existing surface is a three-level dropdown). **Column scoping across phases (A3-B4):** the *inventory columns* — health distribution, phase, instances, PG major, click-through — are **1b**, sourced from `/api/v1/clusters`; the *metric columns* — TPS, p95 latency, connections %, disk % — and the *top finding* are **5**, sourced from R9.4 (Phase 4), R2.2/R5.3 (2/7) and R7.2–R7.6 (8). A column whose source phase has not run renders `—` with a source-pending marker, and the requirement is **re-validated**, not first validated, when the source lands — the rule R10.5(b) already states. Without it, 1b's acceptance silently narrows R10.4 from eight columns to three. |
| R10.5 | **Cluster Overview**, split into the two graphs Cloud SQL actually has rather than one impossible four-series chart: **(a) Query load** — stacked area of total execution time and IO time, from S2/R9.4. **(b) Resource load** — CPU capacity vs. usage and memory working set, rendered **only when S7/`metrics.k8s.io` is present** (R5.3's degradation rule applies), with a "not available" note when it is not. Plus the R5.1 summary cards, a replication topology view sourced from **S4** (R5.5), and instance chips with roles. |
| R10.6 | **Queries**: the R3.2 table with sparklines per row (served by R8.11's batched endpoint, not 50 requests), inline load bars, filter chips (database, user, tag), search over normalized text, and a saved-view concept (**scoped to shared/cluster-scoped saved views** — per-user saved views are impossible under R8.8's single shared bearer token and are deferred with OIDC). Sorting must be by any column with server-side pagination (R8.10). |
| R10.7 | **Query Detail**: header (fingerprint, database, user, first-seen), the per-minute load chart for that query, latency panel (P50/P95/P99 or the R3.9 fallback), plan samples list with a JSON/plan viewer, and recommendations. **Ruling (A3, on the row-selection question A2 routed from `internal/api/query_detail.go:93`): the route addresses `(namespace, cluster, fingerprint)` plus optional `?db=` and `?user=`, and when both are omitted the header's `database` and `user` name the highest-`total_exec_ms` `(datname, usename)` pair that fingerprint has in that cluster, ties broken lexicographically on `datname` then `usename`.** R10.7's header names four fields and R8.2's route supplies only the fingerprint as a path segment, so the other three come from the server: `first_seen` is a stored column, and `database`/`user` are the two this ruling fixes. The store keeps one `query_source` row per `(fingerprint, namespace, cluster, pod, datname, usename)` (`internal/store/schema.go:179`), so a fingerprint seen under two users in one database is two rows with two sets of counters — R9.4's "(cluster, db, user)" identity makes both real, and the spec's silence is about which one the page renders, not whether both exist. Choosing by `total_exec_ms` is the rule R9.5 already applies to pick which queries to export, and `internal/store/statements.go:384` applies it inside the store for the same reason; naming it here stops a second implementation choosing alphabetically and rendering a different page for the same URL. The two optional parameters exist because omitting them entirely is not enough: R10.6's Queries table (:416) filters on `user`, so a viewer filtered to `appuser` who opens a row whose highest-load user is `postgres` would get a detail page whose header contradicts the filter they arrived through — the filter names the row, the parameters carry it. **Scope:** this is R10.7's own parameter set, not an R8.4 amendment — R8.4's uniform set governs list endpoints and already has `?db=` with no `?user=`, and adding a parameter to a detail route does not change the list set. The route's shipped signature (`query_detail.go:93`, `(ctx, namespace, cluster, fingerprint)`) implements the no-parameter case and needs the two optionals added to implement the filtered one.  **Extension (S26's second half): the same identity has to reach the two plan tables or this ruling is unimplementable for plans.** `plan_sample`'s PK is `(fingerprint, captured_at)` (`internal/store/schema.go:289`) and `plan_change`'s is `(fingerprint, detected_at)` (`internal/store/schema.go:310`); neither carries `usename`, while `internal/store/statements.go:288` builds the plan rows from the deduped set, which is keyed `(fingerprint, usename)`. Two users with plans captured in one round therefore produce two `planRow`s with one PK, and the `INSERT OR REPLACE` at `internal/store/statements.go:149` keeps the last — proven against this table's own DDL: inserting `('f0',500,'hash_for_appuser')` then `('f0',500,'hash_for_postgres')` leaves one row, `hash_for_postgres`. The detail page this ruling scopes then names `postgres` and cannot show `appuser`'s plan, because the store discarded it at write time: the `?user=appuser` added above has no row to point at. **Ruled:** `plan_sample`'s PK becomes `(fingerprint, namespace, cluster, pod, datname, usename, captured_at)` and `plan_change`'s `(fingerprint, namespace, cluster, pod, datname, usename, detected_at)` — `internal/store/schema.go:179`'s identity plus the time column, so the four tables that need it share one expression instead of three spellings and one absence. The narrower fix `(fingerprint, usename, captured_at)` is rejected: it fixes the two-user case and leaves the two-database one, and the same three rows inserted under it yield two rows, not three — `('f0','postgres',500)` is one PK for a statement whose plan in `db1` and in `db2` differ, which is the cross-database grouping R3.4's reason (c) exists for. R3.11's "per fingerprint" wording describes the grouping, not the row key, the same reading R3.4 applies to `query`.  **The set is closed.** The 6-tuple `(fingerprint, namespace, cluster, pod, datname, usename)` is the identity of exactly four tables — `internal/store/schema.go:179` (`query_source`), `internal/store/schema.go:241` (`query_rollup`), and the two this extension rules (`internal/store/schema.go:289` `plan_sample`, `internal/store/schema.go:310` `plan_change`), each with its own trailing axis (`bucket_kind`+`bucket`, `captured_at`, `detected_at`). The schema's other two tables are outside it by construction, and widening them would be a mistake rather than an inconsistency: `internal/store/schema.go:377` (`dimension_rollup`) answers R3.7's "which database, which user, which application name accounts for the most load", which aggregates across every fingerprint, so a fingerprint column would split the totals the table exists to produce; and `internal/store/schema.go:403` (`sink_current`) is keyed `(namespace, cluster, pod, collector)` because collector.Sink's contract is one replaced sample set per collector — no query axis and no time axis. `query` itself (`internal/store/schema.go:73`) is keyed on `fingerprint` alone, R3.4's canonical key, and is the parent the other four reference rather than a fifth spelling of the tuple. Seven tables in the schema: four with the tuple, one with the bare key, two with neither. |
| R10.8 | **Active Queries**: live table (R4.1) with a 15 s auto-refresh indicator, row expansion to the blocking chain, and a Terminate action behind the R4.5 gate. |
| R10.9 | **Wait Events**: donut/bar by `wait_event_type` with a drill to per-event counts and the sessions currently in each. |
| R10.10 | **System Insights**: metric-group tabs, each chart with the R5.6 toolbar (zoom / PNG / CSV / external link), and the R5.7 5-instance comparison selector. |
| R10.11 | **Poolers**: pool utilisation and stats charts per pooler with the R6.5 config panel beside them. |
| R10.12 | **Empty and error states are designed, not defaulted.** Every panel defines its empty, loading, partial (some collectors failed), and unavailable (source not configured) state. The reference environment guarantees we hit all four: a Pending replica, a single-instance cluster, a cluster with no Pooler, and no `pg_stat_statements` by default. Verified by R16.7's visual-regression suite, not by inspection. |
| R10.13 | Responsive to 1280 px; dark and light themes; keyboard navigation for the tables; no chart renders as a blank box without a one-line explanation. Verified by R16.7. |
| R10.14 | The initial page load must render from cached API data in **< 1.5 s** on-cluster (R13.3), which requires the API to serve pre-aggregated rollups (R8.11) rather than making the browser compute them. |
| **R10.15** | **Time-range picker**: 1 h / 6 h / 1 d / 7 d / 30 d / custom, global to the app and reflected in the URL. Selecting a custom range **disables auto-refresh** (a moving window over a fixed range is not a coherent state), and the UI says so. **Ruling (A3, on `web/src/state/urlState.ts:53`'s vocabulary, asked by A4): the cell names six values, the UI offers four, and one of the four is not one of the six.** The spec's list is `1 h / 6 h / 1 d / 7 d / 30 d / custom`; `internal/api/series.go:63-68` and `api/openapi.yaml`'s `Range` parameter both implement exactly it (`rangeWindows` at `internal/api/series.go:78-83` has five presets and no sixth key). `urlState.ts:53`'s `TimeRange = "1h" | "24h" | "7d" | "30d"` — duplicated verbatim at `web/src/pages/Queries.tsx:145` — mints **`24h`**, which is not a spelling of the spec's `1 d` but a different token the API has no entry for: `parseSeriesQuery` fails on it and `internal/api/series.go:289` answers 400 `InvalidRequest`. It is a defect, not a variant — a value the UI can put in the URL that the UI's own request builder cannot send — and the picker offers no `6h`, no `1d`, and no `custom`. The correction is `"1h" | "6h" | "1d" | "7d" | "30d" | "custom"`, in both files, as one change. **Phasing:** `24h` → `1d` and the `6h` addition ship now; they change the option list and nothing else. `custom` is welded to this cell's second sentence — selecting it disables auto-refresh and the UI says so — so a slot that adds `custom` to the picker before implementing that rule satisfies the enum and breaks the requirement; it ships with the rule. `urlState.ts:50`'s comment ("Phase 1b has no metrics, but the control is part of the URL contract now so the Phase 5 charts do not change the schema") is the reason this is a contract question and not a styling one, and it is why the enum has to match the server's before the charts are written against it. |
| **R10.16** | **URL state and deep linking**: page, cluster, selected row, tab, sort, filters and time range all serialize to the URL, so any view is shareable and the back button works. |
| **R10.17** | **Global search / ⌘K palette** over clusters, databases, users and query fingerprints, backed by `GET /api/v1/search?q=`. |
| **R10.18** | **Table virtualization + density toggle** (compact / comfortable) on every table that can exceed 100 rows, so R10.14's budget survives a 500-row page. |
| **R10.19** | **Per-chart interaction contract**, stated per chart type rather than left to the library default: hover tooltip contents, legend behaviour (click to isolate/hide a series), click-through target, and crosshair behaviour on multi-series charts. |
| **R10.20** | **Column-visibility menu** on every wide table, with a documented default column set and a persisted per-view selection. |
| **R10.21** | **Timestamp rendering and timezone**: the API emits RFC 3339 UTC everywhere (R8.4's `generatedAt` included); the UI renders in the browser's local zone with the zone labelled, and shows the absolute timestamp on hover. |
| **R10.22** | **Loading skeletons, a global "data as of" stamp, and a partial-data banner.** A page that renders 3 of 4 clusters while the fourth is still loading says so; it does not show a plausible-looking partial page. **Contract source (A1 `93fcd44`):** the UI composes the banner from R8.4's `meta.failed[]` / `meta.loading[]` — **and from nothing else**: `meta.generatedAt` is R10.21's page-header stamp, not a banner input, and `partialBanner()` correctly does not read it (the earlier wording listed it as a third input, which the implementation never honoured) — the shape `web/src/api/types.ts` had invented pre-contract is derived from `meta`, not read off the response. **The banner's served-cluster count comes from `data.length`, never `meta.total`** (A3 S1): per R8.4's corrected wording `meta.total` is the pre-cut count, so a banner built from `total` overstates by the page size on any truncated page — it announces "120 clusters served" while rendering 50. The fixture never catches this, because `FIXTURE_PARTIAL` sets `total` to the filtered length with `truncated: false`; the two only diverge when paging *and* partial co-occur, which R8.4 says is in-contract, so the test set needs a truncated-and-partial case before this is enforced rather than merely stated. |
| **R10.23** | **Colour-blind-safe palette plus non-colour encoding**: every series distinguishable in greyscale, and every state conveyed by shape, icon or label as well as colour (R4.1's icons, R5.1's card states). |
| **R10.24** | **Cluster switcher + breadcrumbs**: a global cluster picker on every cluster-scoped page, and a breadcrumb trail Fleet → Cluster → Query Detail. |
| **R10.25** | **Keyboard shortcuts**: `j`/`k` row movement, `Enter` open, `/` focus search, `t` cycle time range, `Esc` close, `?` shortcut help. |
| **R10.26** | **Alerts surface in the UI**: firing and pending alerts for a cluster, fetched from the Prometheus API using R9.11's rules, each with its `runbook_url` linked. Cloud SQL's Alerting pane is a first-class surface; an alert that exists only in Alertmanager is invisible to P2. |
| **R10.27** | **`pg_stat_statements` not-installed is a first-class screen**, designed and tested — not an empty table with a tooltip. Given K2 (absent on all three reachable clusters), this is **the product's most-seen screen in v1** and gets the same design effort as the Queries page. |
| **R10.28** | **Empty-state catalogue**: an enumerated, designed state for every combination the reference environment produces — healthy 2-instance, single-instance, Pending replica, **CrashLoopBackOff on a CR that reports healthy** (R1.2), no Pooler, PG16 vs PG17 column differences, and extension-absent. Each has a named fixture in R16.7. |

### R11 — Packaging and deployment

| ID | Requirement |
|---|---|
| R11.1 | One image contains the API, the collector, and the UI. |
| R11.2 | Dockerfile builds **multi-arch `linux/arm64` + `linux/amd64`** (the dev machine is arm64; **all five reference nodes are amd64**), via `docker buildx`, from a multi-stage build whose final stage is **distroless static** (`gcr.io/distroless/static:nonroot`), non-root uid 65532, no shell, no libc in the app path (pure-Go SQLite, §3.4). Because distroless has no shell, **all probes are `httpGet`** (`/healthz`, `/readyz`) — an `exec` probe cannot run — and `/tmp` is an `emptyDir` (R12.7). Multi-arch is a Phase 0 **blocker**, not a nicety: a plain `docker build` here yields an arm64 image that cannot schedule on any target node (`live-probe.md` §7, F10). |
| R11.3 | Image labelled with `org.opencontainers.image.{source,revision,version}`; version string injected via `-ldflags` and exposed at `/api/v1/version` and `pgsi_build_info`. |
| R11.4 | Image size target **< 60 MB compressed**, where *compressed* is defined as the **registry manifest-reported compressed size of the amd64 image** (measured via `docker buildx imagetools inspect` / the registry API), not `docker save | wc -c`. CI fails the build above 100 MB. The SPA's gzipped JS bundle size is reported as a **separate** CI number, so a bloated frontend is never hidden inside a small image number. |
| R11.5 | Pin base digests, not tags; Dependabot/Renovate config present. |
| R11.6 | **Helm chart** as the primary artifact, with documented profiles from one chart: `values-production.yaml` (PVC-backed store, token auth, ServiceMonitor on, resource requests/limits, 1 replica) and `values-lab.yaml` (in-memory store, auth off, ServiceMonitor off, `--single-namespace` capable). Plus `deploy/plain-yaml/` and `deploy/kustomize/`. **[A]** |
| R11.7 | Chart ships: Deployment, Service, ServiceAccount, Role/RoleBinding (namespaced) with an `--all-namespaces` ClusterRole variant, ConfigMap, ServiceMonitor, PodMonitor variant, PrometheusRule, ingress, networkpolicy, PDB-optional, and `.helmignore` + `helm lint` clean. |
| R11.8 | Config precedence: CLI flags > `PGSI_*` env > mounted ConfigMap > built-in defaults, documented per flag; `pgsi config dump` prints the resolved config for debugging. |
| R11.9 | **Install in < 5 minutes** with a copy-pasteable two-command quickstart (`helm repo add` + `helm install`) that works against a stock CNPG install with no other prerequisites — the P5 lab job, made literal. **Blocked on naming the org** (Q6 → `ghcr.io/<org>/pgsql-insights`); until the OCI registry exists, the verifiable form is `helm install` from a **packaged chart tarball** built in CI, which R16.4/R11.14 test and the README documents as the offline path. |
| R11.10 | Provide an **uninstall** story: `helm uninstall` leaves no cluster-scoped residue (ClusterRole/Binding gated behind the same flag that creates them). |
| R11.11 | CI: build, lint (`golangci-lint`, `helm lint`, `helm template` snapshot test), unit tests with ≥70 % coverage on `internal/…`, multi-arch image build on tag, and a **render test** that `helm template`s the chart with all three profiles and validates the output against the k8s schema. |
| **R11.12** | **`values-default.yaml` is the shipped default** and is a real, complete profile — not an empty file the other two override. 0.1.0 shipped only `values-production.yaml` and `values-lab.yaml`, so `helm install` with no `-f` produced an undocumented fourth configuration. `values-production.yaml` and `values-lab.yaml` are documented as deltas from it. |
| **R11.13** | **`deploy/plain-yaml/all.yaml` is generated, not maintained**: CI produces it with `helm template` from the chart + `values-default.yaml` and commits it; `deploy/kustomize/kustomization.yaml` points at that generated file. Three hand-maintained deployment formats drift; one generated one does not. |
| **R11.14** | **Phase 0's acceptance is deploy-and-observe, not build-only**: the multi-arch image is built **and** deployed to the reference cluster, the pod reaches `Running`, and `GET /healthz` answers through the Service. 0.1.0 accepted Phase 0 on a successful `docker buildx build` — which an image that cannot schedule on any node also satisfies. |
| **R11.15** | **Storage access-mode contract** *(A3's proposed R20)*. `--role=serve` (the only v1 role, §3.1) uses a `ReadWriteOnce` PVC. A collector/server split requires `ReadWriteMany`, which the chart cannot assume exists; R11.15 documents that the split is gated on either an RWX StorageClass or the Postgres-backed store, and the chart's `persistence.accessModes` is configurable with `ReadWriteOnce` as the default. |
| **R11.16** | **Image publication** *(A3's proposed R23)*: Phase 0 CI publishes the multi-arch image to the named registry (Q6) on every merge to `main` with a `:dev` tag plus the git-SHA tag, so R11.14's deploy step and R11.9's quickstart have a real artifact to reference. Resolves Q6's dependency on the spec side. |

### R12 — Security and RBAC

| ID | Requirement |
|---|---|
| R12.1 | Ship a **least-privilege namespaced Role** as the default: read on the CNPG resources + their `/status`, read on pods/services/configmaps/events/secrets, `pods/proxy:create`, read on deployments/jobs/pdb/endpointslices/podmonitors, and **`pods/log: [get, list]`** (needed by R5.10; included in the default Role but inert while `--log-source=off`). Derived from and consistent with CNPG's own documented per-command RBAC. |
| R12.2 | `pods/exec` is **not** requested by default (it is effectively code execution in the pod namespace); it is opt-in via `rbac.allowPodsExec=true`. |
| R12.3 | Cluster-scoped RBAC is created only when `rbac.allNamespaces=true`; the default install is namespaced. |
| R12.4 | **Postgres-side privilege model**: prefer a dedicated `pg_monitor`-granted role (the `cnpg_metrics_exporter` pattern from the CNPG CVE-2026-44477 fix) over consuming `<cluster>-app`; document the exact `CREATE ROLE`/`GRANT` statements; support reading `<cluster>-app` as the zero-config path; and never connect as the superuser. **Consequence, stated:** `pg_monitor` grants statistics visibility but **not `SELECT` on application tables**, which is what makes R3.10's EXPLAIN report `unavailable (insufficient table privileges)` rather than working. |
| R12.5 | TLS to Postgres configurable. **Derive `sslmode` from the right secret**: a client verifies the server with `<cluster>-ca`, so the default is `verify-ca` using `<cluster>-ca`; `<cluster>-server` holds the *server's* cert **and private key**, and reading it hands us a private key we do not need and should not hold. Fall back to `prefer` when no `-ca` secret exists. TLS on our own HTTP endpoint via a cert secret or cert-manager annotation. |
| R12.6 | Never log query parameters by default (only the normalized template, R3.5), so credentials inside SQL literals cannot leak into logs or into the store. |
| R12.7 | Container hardening: `readOnlyRootFilesystem` with writable `/tmp` as an **`emptyDir`** plus the data volume, `allowPrivilegeEscalation: false`, `capabilities: drop [ALL]`, seccomp `RuntimeDefault`, and a `PodSecurityStandard: restricted`-compliant pod spec. |
| R12.8 | The `--allow-actions` gate is off by default so a read-only product cannot become a mutation surface by surprise. **Generalized** from "terminate session only" (0.1.0) to the full set of mutations the product performs: `pg_terminate_backend` (R4.5), the `spec.parameters` patch (R3.15), and any future action — each individually named in the flag's value, not covered by one global boolean. |
| R12.9 | A documented threat model section: what the token grants, what a leaked `<cluster>-app` password grants, what `pods/proxy:create` grants (read pod HTTP endpoints, not pod contents), and what `pods/log` grants (R5.10 — full Postgres logs, which can contain statement text). |

### R13 — Non-functional

| ID | Requirement |
|---|---|
| R13.1 | **Per-cluster poll cost budget**: one cluster's full 60 s collection round completes in **< 3 s** and issues **< 40 SQL round-trips** on a healthy instance; measured and reported via `pgsi_collector_duration_seconds`. **What counts as a round-trip (D20):** the reported field counts **queries issued**, not transaction control statements — a view group's `BEGIN`/`COMMIT` pair is not counted, so a collector that runs one query per view inside one transaction reports the number of views. The budget is a query budget, and the arithmetic is what settles it: thirteen collectors at three queries each is 39, and R2.7's one-transaction-per-view-group adds two control statements per transaction, so a statement-counting reading puts the same round at 39 + 78 = 117 and makes this requirement unsatisfiable by construction at any collector count above four. |
| R13.2 | **Fleet scaling**: 50 clusters on one poller with **< 1 % CPU per cluster** and < 150 MB RSS baseline; per-cluster concurrency cap (R2.1) plus the global budget (R2.9) prevent a slow cluster from delaying others or exhausting `max_connections`. |
| R13.3 | API p50 **< 100 ms** for cached endpoints, p95 **< 400 ms**; first UI paint **< 1.5 s** (R10.14). |
| R13.4 | **Cardinality budget** per §5 (**~23.7 k series / ~1.5 GB over 30 d** for the reference environment at the declared 60 s interval), with a CI-checkable assertion that the exported series count for a fixture cluster set stays under the documented bound. |
| R13.5 | Memory ceiling configurable and enforced (`--max-store-bytes`), with size-based eviction rather than OOM. |
| R13.6 | **Lab mode**: runs against a single namespace with no PVC, no auth, no ServiceMonitor, in < 100 MB RAM — the P5 path must work on a kind/minikube cluster. |
| R13.7 | Structured logging (JSON, level-configurable, `--log-field-*` compatible with the CNPG/machinery log flag shape so log pipelines treat us like CNPG), plus `--log-level=debug` that dumps the SQL executed per collector. |
| R13.8 | Graceful shutdown: drain in-flight scrapes, flush the store, exit within the terminationGracePeriod. |
| R13.9 | **Compatibility matrix as a tested artifact**, with each cell marked `tested | fixture | impossible | untested` — no silent claims. Two axes, because one matrix over both is how 0.1.0 over-claimed:<br>**PostgreSQL** — CNPG 1.30 supports **PG 15–18**. Cells: 15 `untested`, **16 `tested` (16.11, measured)**, **17 `tested` (17.11, measured)**, 18 `untested`; **PG 13 and 14 are `impossible` on CNPG 1.30** (PG 14 support ended 2026-11-12) and are documented as fixture-tested SQL-layer rows only.<br>**Kubernetes** — CNPG 1.30 supports **1.34, 1.35, 1.36**, and lists **1.37 as "tested but unsupported"**. Our reference environment runs **k3s v1.37.2+k3s1**, i.e. *outside* CNPG's supported window — the matrix must say so rather than claiming 1.29–1.37.<br>**CNPG** — 1.29–1.30 (`1.28` out: it predates the CVE-2026-44477 exporter-role model R12.4 depends on). **kube-prometheus-stack** 0.7x+. |
| R13.10 | Go 1.26+, no cgo, no `replace` directives, `go.mod` tidy in CI. |
| **R13.11** | **Clock contract.** Every stored sample carries **two timestamps**: the source's `stats_reset`/`capturedAt` from the instance and our own ingest time. If the two disagree by **> 30 s**, the collector logs a skew warning naming the cluster, and the UI surfaces clock skew as a finding. Counter deltas (R3.3) across two pods whose clocks disagree by minutes produce rates that are wrong in a way no other check catches. |

### R14 — Documentation and handoff

| ID | Requirement |
|---|---|
| R14.1 | **`IMPLEMENTATION.md` is the handoff file**, created in Phase 0 before any other code and updated in the **same commit** as every phase's code. It contains: the work-breakdown table with per-task status, the current phase and its state, deviations from this spec with reasons, decisions log, known gaps, and "how to run this right now". **The "same commit" rule is scoped to be workable across four agents** (A4-M12): the update lands in the **last commit of the phase**, not literally every commit. |
| R14.2 | Every phase ends with a **validation entry** in `IMPLEMENTATION.md`: what was checked against which requirement IDs, what passed, what deviated, and the fix or the follow-up. Plus a **`Last validated` section** listing, per requirement ID, the date and method of the most recent validation — the traceability artifact R14.1 promised but did not specify. **Split phases (see R14.9):** a phase with more than one owner writes **one entry per owner**, and each later phase gates on the entries belonging to its own owner. |
| R14.9 | **Split-phase validation entries** (A3 filed this as a new top-level ID "R25"; filed under R14 per the §11 rule-4 precedent). R14.2 was written when every phase had exactly one owner. Post-split, the shared 1a/1b entry serializes A1's Phase 2 behind A4's SPA — reintroducing on the critical path the very dependency the split exists to remove. Each owner writes their own entry: Phase 2 gates on **1a's** entry only; Phases 5 and 6a gate on **1b's** as well. Generalizes to any future split phase. |
| R14.3 | `README.md` with: what it is, a GIF/screenshot, the two-command quickstart, a capability matrix vs Cloud SQL, the metrics catalogue, and a link to this file. |
| R14.4 | `docs/` holds this spec, the two research documents, the live probe, `ENVIRONMENT.md`, `reviews/`, per-feature runbooks, and an architecture doc with the diagram from §3.1. **A3 ruling (2026-11-22, on A4's ask):** the requirement's normative content is the *inventory* — that these seven artifacts exist and are discoverable — not the directory layout, and a root `requirements.md` satisfies it. The file is referenced by relative path from **23 tracked files, 53 occurrences**; moving it into `docs/` is a 22-file edit across three other slots' trees, which costs more than the requirement's purpose (discoverability) buys — and if a physical move is ever wanted it lands as its own commit carrying the cross-reference rewrite, never folded into a Phase 9 docs commit. The residual gap is not the spec's location but that `docs/` has no index to point at it from (`docs/` holds `agents.md`, `architecture.md`, `compatibility-matrix.md`, `ENVIRONMENT.md`, `licenses/`, `research/`, `reviews/`, `runbooks/` and no README): the pointer is the Phase 9 artifact, and `README.md:13`'s existing `[requirements.md](requirements.md)` link is what satisfies the requirement today. |
| R14.5 | `CONTRIBUTING.md` with the dev loop (build, test, run locally against a kind cluster), code layout map, and the release process. |
| R14.6 | `CHANGELOG.md` maintained from Phase 1 onward (not retro-fitted at release). |
| R14.7 | Every exported metric and every API endpoint is documented in one place (R9.10, R8.1) — no metric or route that exists in code but not in docs. |
| **R14.8** | **Requirement→file→status table.** `IMPLEMENTATION.md` carries a table mapping every requirement ID in §4 to the file(s) that implement it and its status (`not-started \| in-progress \| implemented \| validated \| blocked \| tabled`). This is the artifact that makes R14.2's validation entries checkable and closes the 0.1.0 gap where 18 IDs were defined and scheduled in no phase. **The status cell is the enum, then the prose** (amended 2026-11-23): the cell is `**<enum>**`, optionally `**<enum> (A4)**` where R14.9's per-owner attribution applies, then ` — ` and the prose; nothing else may appear before the em dash, and `scripts/check-parity-matrix.py` asserts the membership — an enum nobody validates is the convention that broke, and the tables had twenty spellings across seventy-seven rows while the spec had four. Two values the old set could not say: **`blocked`** for a requirement this phase cannot close yet (the prose names what it waits on), and **`tabled`** for one handed to another phase under R14.9 — without it a handed-away requirement reads `implemented` in two phases' tables and is counted twice, which is the Phase 4 counting error this closes. The pipes in the enum are escaped (`\|`) because an unescaped pipe inside a table cell ends the cell: the row that defines the vocabulary was itself rendering as five cells. |

### R15 — Licensing and distribution

| ID | Requirement |
|---|---|
| R15.1 | License chosen and committed before the first public release; the spec's working assumption is **Apache-2.0** (matching CNPG and postgres_exporter, and permissive for adopters), recorded as a decision in `IMPLEMENTATION.md` rather than left implicit. |
| R15.2 | Distribution artifacts named and versioned from day one: OCI image tag scheme (`ghcr.io/<org>/pgsql-insights:vX.Y.Z` + `:latest`), Helm chart version vs. appVersion policy, and a GitHub Release per tag with the SBOM attached. **A3 ruling (2026-11-22, on the coordinator's ask — this row owns "Helm chart version vs. appVersion policy", so the version-space question is recorded here rather than settled by whoever edits README first):** there are **two independent version spaces and they must not be synced.** (i) The **spec-document** version is `0.1.0-draft` (`97172a4`) → `0.2.0` → `0.3.0` (this file's header row). (ii) The **product** version is `0.1.0-dev` (`chart/Chart.yaml`'s `appVersion`, current) → `v0.1.0` (Phase 9's tag, this row's `:vX.Y.Z`). `README.md:14`'s "(v0.3.0)" and `chart/Chart.yaml`'s `0.1.0` are therefore not in conflict — citing one against the other is a category error, and the spec being at 0.3.0 while the product is at 0.1.0-dev is correct, not drift. **There is no shipped 0.1.0 product.** `git tag -l` returns **zero tags**; this file's own header names the antecedent `0.1.0-draft`, with *draft* in the name; and every "0.1.0 shipped / had / missed / stated / named" in this spec describes a *document*, not a release — :314 "three constraints the 0.1.0 **text** missed", :697 "the 0.1.0 **defect where 18 IDs were defined and scheduled in no phase**" (a product cannot have requirement IDs scheduled in no phase; a spec can), :524 "**0.1.0's R16** had zero UI tests" (a requirement group), :403 "This is 0.1.0's `R9.9b`" (a requirement ID). The verb *shipped* at :310 and :842 is loose usage for *specified* and is the word that makes a document revision read as a release; the 26 bare `0.1.0` occurrences in this file are governed by this ruling without a sweep, and :310 is the flagged worst instance. **Consequence:** Phase 9's "v0.1.0 release" is the **first** tag, not a re-tag, and this row's `:vX.Y.Z` resolves to `:v0.1.0` + `:latest` — which also settles what `ci.yml`'s image job will tag once `phase-1a-backend` merges (it pushes `:dev` + `:${GITHUB_SHA::7}` today and has no `release` or `sbom` step, the half gated on that merge). |
| R15.3 | Third-party license audit (`go-licenses` + the SPA's license set) committed as `docs/licenses/` and regenerated in CI. |

### R16 — Testing strategy

| ID | Requirement |
|---|---|
| R16.1 | Unit tests for: the PG version matrix (R2.4) across all six majors, the SQL normalizer (R3.5) against a fixture corpus, the `cnpg_*` exposition parser (R2.6), counter-reset handling (R3.3), and the label contract builder (R9.3). |
| R16.2 | Integration tests against a real Postgres (testcontainers or a kind-hosted CNPG) for the SQL collectors on PG 16 and PG 17 — the two majors in the reference environment — asserting the version-branched SQL actually executes. |
| R16.3 | **Recorded fixtures**, named explicitly (A4-m8): `testdata/cnpg-metrics/cookbook__cookbook-pg-1__9187.txt`, `testdata/cnpg-metrics/open-webui__open-webui-pg-2__9187.txt`, `testdata/cnpg-metrics/ai-api-gateway__rev-proxy-pg-2__9187.txt`, `testdata/cnpg-metrics/ai-api-gateway__rev-proxy-pg-pooler-86b4bdd84d-n9vlv__9127.txt`, `testdata/pg-status/ai-api-gateway__rev-proxy-pg-2__8000.json`, `testdata/pg-status/cookbook__cookbook-pg-1__8000.json`, `testdata/pg-status/open-webui__open-webui-pg-2__8000.json`. The parser is tested against real output rather than a hand-written sample, and regressions are detectable offline. |
| R16.4 | Golden tests for the Helm chart (`helm template` output committed for all three profiles). |
| R16.5 | A **live validation script** (`scripts/validate-env.sh`) that runs the §8 acceptance checks against the reference environment and prints a pass/fail table; this is what makes "[A]" requirements actually checkable. **Scheduled in Phase 2** — 0.1.0 defined it and scheduled it nowhere, which is the mechanism §8 depends on. |
| **R16.6** | **Ground-truth correctness fixture** (A4-B7). 8 of §8's 10 checks are presence checks — a deployment where every TPS gauge reads `0` passes them all. Seed a Postgres fixture with a known workload (N inserts, M selects, known rows) and assert our computed values equal the seeded ground truth, not merely that a value exists. Phase 2. |
| **R16.8** | **API response fixtures** (A3 filed this as a new top-level ID "R24"; filed under R16 per the §11 rule-4 precedent that R1–R16 is the taxonomy and new findings extend an existing group rather than mint R17+). A checked-in JSON fixture per endpoint under `testdata/api/`, created by the phase that first serves it — Phase 1a: `testdata/api/clusters.json` with the 4 reference clusters — consumed by the SPA's Vitest suite and R16.7's visual-regression snapshots. Today the only machine-readable copy of the Phase 1a contract is `web/src/api/fixtures.ts`: the *consumer's* language, in A4's tree. R16.3 covers metric expositions and `pg-status` JSON but not API responses. Phase 1a. |
| **R16.7** | **UI tests** (A4-M13). R10 has 28 requirements and 0.1.0's R16 had zero UI tests. Add a **Vitest** component suite, a **Playwright** end-to-end suite, and **visual-regression** snapshots. **Phase 1b onward** (v0.3.0 — the harness is created with the SPA, not at Phase 5, which the v0.2.0 text's "Phase 5 onward" had become false — A3-M1), scoped at each phase to *the page set and the empty-state catalogue that exist at that phase*: 1b tests the Fleet page and its four R10.12 states, and the R10.3 page set (→5) and R10.28 catalogue (→6b) join the suite as those phases land them. |

---

## 5. Cardinality and retention budget

This is the section that decides whether the product is adoptable. Cloud SQL bounds it explicitly
(top 100 queries/day on Enterprise, top 4 000/minute on Enterprise Plus, 4 500-byte query text cap
on Enterprise / 100 KB on Enterprise Plus, ~36 GB at 7 d / ~155 GB at 30 d on instance disk) and so
must we.

**Scrape interval is declared per family.** Our `/metrics` is scraped at **60 s**, matching the
collection interval — a faster scrape of an endpoint that refreshes every 60 s samples the same
value repeatedly and costs storage for nothing. The 15 s `activity` collector (R4.2) feeds the API
and store, not `/metrics` (R9.1), so it does not change this number.

| Family | Labels | Series per cluster | Interval | Control |
|---|---|---|---|---|
| Aggregate (R9.4) | `namespace,cluster,pod,instance_role,datname,usename` | ~`databases × users`, capped by the R5.4 500-database guard; measured **~1.2 k** for the reference env | 60 s | Always on |
| Per-query (R9.5) | `…,fingerprint,queryid` | **N per cluster × 6 metric series** = 50 × 6 = **300** | 60 s | `metrics.topN` 0–1000, **per cluster**; N=0 disables |
| Sampled latency histogram (R9.13) | `…,fingerprint,le` | 50 × (8 buckets + `_sum` + `_count`) = **450** | 60 s | Ships with R3.9 |
| Per-tag (R9.6) | `…,tag_hash` | tags × databases | 60 s | Off unless R3.12 enabled |
| System (R9.7) | `namespace,cluster,pod,instance_role` + family-specific | ~**4 k** | 60 s | Always on |
| Pooler (R6.1) | `…,pooler,database,user` | ~**200** where a Pooler exists | 60 s | Only where a Pooler exists |
| Self (R9.8) | — | ~**120** | 60 s | Always on |

**Reference-environment estimate.** Per cluster ≈ 1 200 + 300 + 450 + 4 000 + 120 = **6 070**; four
clusters ≈ **24.3 k series**, of which one cluster contributes ~200 pooler series. At ~1.5
bytes/sample after compression and a **60 s** scrape (43 200 samples per series over 30 d):

> **24 300 × 43 200 × 1.5 B ≈ 1.58 GB of Prometheus storage over 30 days.**

This is the number R9.10 makes the chart README promise. *(0.1.0 stated "~2.6 GB" for "~22 k
series at a 15 s scrape". Both halves were wrong: 22 k × 172 800 × 1.5 B = **5.7 GB**, and 2.6 GB is
the 30 s figure, not the 15 s one. The per-query row also said "N × databases" while R9.5 defines
`topN` per cluster — 14 k per-query series across 4 clusters implies ~70 databases each, which the
~1.2 k aggregate row cannot support. Corrected here and in R13.4.)*

**Deliberate divergences from Cloud SQL's limits, and why:**

| Cloud SQL | Us | Why |
|---|---|---|
| 4 500-byte query text (Enterprise) / 100 KB (Enterprise Plus) | 1 024 default; range 256–100 000; 10 000 documented as the E+ default | Matches Enterprise's default exactly and gives the Enterprise-Plus ceiling as a knob; 4 500 B × 4 000 queries is the cost Google absorbs on instance disk, and we ship it as opt-in rather than as the default |
| 200 plan samples/min (E+) | 5/min default, knob up to 200 | `EXPLAIN` costs planner CPU on the real database; Google's 200/min is for EEP instances that pay for it. Default = Enterprise's 5, ceiling = EEP's 200 |
| 30-day retention (E+) / 7 d (Enterprise) | 7 d default, 30 d available | 7 d is Enterprise's number and fits a 1 Gi PVC; 30 d is one config change |
| Per-query latency DISTRIBUTION | min/mean/max/stddev + sampled histogram | PostgreSQL has no per-query latency distribution; R3.9 |
| Per-query `lock_time` with `lock_type` | `n/a` per query; available at database/user level | `pg_stat_statements` has no lock-time column. We derive lock wait at database/user level from `pg_locks` + `pg_stat_activity` and label it as such rather than faking a per-query number |
| End-to-end application trace (sqlcommenter + Cloud Trace) | Not in v0.1 | Google's trace integration needs Cloud Trace. The R3.12 comment convention is the generic-Postgres seed; a real trace exporter is post-0.1 |
| AI/anomaly **detection** half (24 h baseline + warning icons) | Deferred with the AI pane | Only the Gemini half is deferred by §1.3; the 24 h baseline + anomaly icons are a separate feature and are not in v0.1 |

---

## 6. Parity matrix

Every Cloud SQL capability, its source in Google's product, our requirement ID, and the phase that
delivers it. **The Phase column is generated from §7.3's requirement→phase index** — §7.2 is the
phase table, §7.3 is the index, and v0.2.0's preamble cited §7.2 while the cells stayed hand-written,
so four of them disagreed with the index (A3-M5 in the v0.3.0 review, after A3-M8 in round 1). The
cells below are regenerated from §7.3, and `scripts/check-parity-matrix.sh` diffs the Phase column
against the index in CI, so the claim is enforced rather than re-broken by the next edit.

| Cloud SQL capability | Google's source | Our source | Req | Phase |
|---|---|---|---|---|
| Query list w/ load columns | `insights/perquery` metrics | `pg_stat_statements` | R3.2 | 3 |
| Query text + fingerprint | `querystring`, `query_hash` | `queryid` + normalizer | R3.4–R3.6 | 3 |
| Query detail: load graph | per-minute aggregation | statement snapshots @60 s | R3.8 | 3 |
| Query detail: plan samples | `auto_explain` + sampled EXPLAIN | bounded `EXPLAIN (FORMAT JSON)` | R3.10–R3.11 | 6b |
| Query detail: P50/P95/P99 | DISTRIBUTION metric | min/mean/max/stddev + sampled | R3.9 | 6b |
| Load attribution (by db/user/app) | `insights` rollups | `pg_stat_statements` rollups | R3.7 | 3 |
| Normalization | literal stripping | R3.5 normalizer | R3.5 | 3 |
| Wait event types / wait events | `insights` wait metrics | `pg_stat_activity` + `pg_wait_events` | R4.3 | 6a |
| Databases tab | `insights` by db | `pg_stat_database`, `pg_database` | R3.7, R5.2 | 3 (R3.7), 7 (R5.2) |
| Users tab | `insights` by user | `pg_stat_activity`, `pg_stat_statements` | R3.7 | 3 |
| Tags tab | app-set query tags | `application_name` + `/* pgsi: */` convention | R3.12 | 6b |
| Client addresses tab | `client_addr` label | `pg_stat_activity.client_addr` + pooler fix | R3.13, R6.4 | 3 (R3.13), 8 (R6.4) |
| Active queries (top 50) | live query | `pg_stat_activity` | R4.1–R4.2 | 6a |
| Blocked-query tree (3 levels) | lock analysis | `pg_locks` graph | R4.4 | 6a |
| Terminate connection | E+ action | `pg_terminate_backend` | R4.5 | 6a |
| System Insights summary cards | OS+DB metrics | S2/S3/S7 (+S10 for Log errors) | R5.1, R5.10 | 7 |
| System Insights metric families | `database/postgresql/*` | S2 + S3 | R5.2 | 7 |
| OS metrics (CPU/mem/disk/net) | `database/{cpu,memory,disk,network}/*` | cAdvisor/node-exporter/metrics-server | R5.3 | 7 |
| Compare up to 5 instances | UI feature | stable instance identity | R5.7, R9.3 | 7 (R5.7), 2 (R9.3) |
| Download PNG / CSV | chart toolbar | per-chart toolbar | R5.6 | 7 |
| HA / replication view | `replica_byte_lag`, `replication/state`, `available_for_failover` | **S4 `replicationInfo[]`/`replicationSlotsInfo[]`**, S2 | R5.5 | 7 |
| Retention (7 d EE / 30 d EEP) | instance disk | store retention config | R3.14, R5.8 | 3 (R3.14), 7 (R5.8) |
| Events timeline | events | k8s events + phase transitions + backups | R5.9 | 8 |
| Index advisor (E+) | query plans | statement text + index usage | R7.1 | **post-0.1** |
| Recommenders (E+): bloat, autovacuum, TXID | multiple | `pg_stat_user_tables` dead-tuple estimates, `pg_stat_*_tables` autovacuum stats, `pg_current_xact_id()` | R7.2, R7.4, R7.5 | 8 |
| Recommenders: out-of-disk, idle-instance | two named recommenders | disk % + connections % (R10.4) | R7.6 | 8 |
| Rightsizing recommender (E+) | managed-instance sizing | pod requests vs. cgroup usage (**CNPG-derived**) | R7.3 | **post-0.1** |
| Flag recommender | Cloud SQL | not in v0.1 | — | post-0.1 |
| OOM session cancel | Cloud SQL | not in v0.1 | — | post-0.1 |
| Performance Capture | Cloud SQL | not in v0.1 | — | post-0.1 |
| Cloud Logging severity / pgaudit breakdown | `logging.googleapis.com` | `pods/log` parser | R5.10 | 7 |
| Cloud Trace / sqlcommenter | Cloud Trace | R3.12 comment convention | R3.12 | 6b |
| Alerting UI (Notification, annotations-on-charts, Slack/PagerDuty/email/SMS) | Cloud SQL monitoring | Prometheus API + R9.11 rules + `runbook_url` | R10.26 | 6a |
| Custom dashboards | Cloud SQL | not in v0.1 | — | post-0.1 |
| AI troubleshooting (E+) | Gemini | deferred; API slot reserved | §1.3 | post-0.1 |
| Admin API | `cloudsqladmin v1beta4` | REST + OpenAPI | R8.1–R8.2 | 1a (R8.1), 3 (R8.2), 5 (full surface) |
| MCP PromQL-in/out API | `databaseinsights.googleapis.com/mcp` | `/api/v1/metrics/{query,system}` | R8.3 | 5 |
| `InsightsConfig` equivalents | `queryInsightsEnabled`, `queryStringLength`, `recordApplicationTags`, `recordClientAddress`, `queryPlansPerMinute`, `enhancedQueryInsightsEnabled` | flags + ConfigMap keys, same names where meaningful | R3.6, R3.10, R3.13, R11.8 | 3 (R3.6), 6b (R3.10), 3 (R3.13), 2 (R11.8) |
| Edition tiers | Enterprise / Enterprise Plus | single tier at the E+ feature set | §1.2 | — |
| Pooler insights | (Cloud SQL: pgbouncer on 6432) | `cnpg_pgbouncer_*` + `SHOW POOLS/CLIENTS` | R6 | 8 |

---

## 7. Delivery plan

Ten phases, **twelve slots** — Phase 6 split into 6a/6b per A4-B6, and Phase 1 split into **1a/1b**
in v0.3.0 so the web UI ships alongside the first real API instead of four phases after it. Each
phase lists deliverables, the requirement IDs it satisfies, its acceptance criteria, and the assigned
agent. **No phase starts before the previous phase's validation entry is written into
`IMPLEMENTATION.md`** (R14.2) — 1a and 1b are one phase with two owners, so they share a single
validation entry covering both halves.

### 7.1 Agent allocation (the brief's hard constraint: 4 subagents max — 2 opus + 2 sonnet, amended to 6 — 3 opus + 3 sonnet on 2026-11-22)

| Slot | Model | Spawned as | Now owns |
|---|---|---|---|
| A1 | opus | `a19261c1d46185a9e` | Cloud SQL research (done) → Go backend (collector framework, PG version matrix, discovery, `cmd/pgsi`, `api/dto`; store and query engine → A5) |
| A2 | sonnet | `aa0fd103621139f92` | CNPG research (done) → Go backend (API, metrics export, PromQL pair; Phase 6b → A6) |
| A3 | opus | `a22575c69dbb55a9c` | Round-1 architecture review (done) → architecture, spec maintenance, phase validation |
| A4 | sonnet | `a25215aa10f422996` | Round-1 product review (done) → frontend/UI **shell and build**, Helm chart, packaging, docs (pages and components → A6) |
| A5 | opus | `a597c9b5937308ed4` | *(added with the 2026-11-22 cap raise)* → `internal/query/`, `internal/store/`, the `pg_stat_statements` collector (Phase 3, from A1) |
| A6 | sonnet | `a74bd5afabb029289` | *(added with the 2026-11-22 cap raise)* → `web/src/pages/**`, `web/src/components/**` — the Query Insights UI (Phase 6b, from A2) |

Six agents now, reused via `SendMessage` for every phase. **The cap is spent; no seventh spawn.** The user's amendment raised the number and did not remove the rule (`docs/agents.md` §1). One operational corollary belongs here because the cap rule assumes it and doesn't state it: `SendMessage` reaches a slot only while an instance of it is still running, so reuse in practice means spawning a fresh instance of the slot's `subagent_type` — which is why the `.claude/agents/*.md` files have to carry the accumulated rules (`docs/agents.md` §7).

**A roster change is one commit, and the commit is the enforcement** (A3 ruling, 2026-11-23; the rule itself lives in `docs/agents.md` §8 and is the same-commit rule R14.1 already imposes on `IMPLEMENTATION.md`, extended to the rest of the roster). Four places state the roster — this section, §7.2, `docs/agents.md` §3 and `IMPLEMENTATION.md` §2 — plus the slot's `.claude/agents/<name>.md` frontmatter. The 2026-11-22 amendment shipped the docs+agents pair in `c653f36` and this file in a follow-up, so for exactly one commit the spec said four and the docs said six. The rule is not narrowed by that: a rule whose whole value is that a reader gets the same answer from any one of the places is broken by shipping two of them, not by shipping them in the wrong order. Where a rule like this can be made a gate rather than a habit it should be, so the three file citations in `docs/agents.md` §8's table are now written in the parity checker's durable form and `scripts/check-parity-matrix.py` fails if any of them stops pointing at the heading it quotes — an n-way agreement asserted by prose alone is the class of convention this slot keeps reporting on elsewhere.

**Ownership contradiction fixed in v0.3.0.** §7.1 has always said A4 owns *frontend/UI*, but §7.2
assigned the SPA (Phase 5) and the Query-Insights UI (6b) to **A2** — so the roster and the plan
disagreed about who builds the UI. Assigning Phase 1b to **A4** resolves it in favour of the roster,
and it is the assignment that costs nothing: A4 finishes Phase 0 and has no further scheduled work
until Phase 9, while A1 owns 1a and A2's Phase 4 backend track is untouched. A2 keeps every backend
phase §7.2 gives it (4, 5, 7 — 6b went to A6 with the 2026-11-22 cap raise).

### 7.2 Phase table

| Phase | Scope | Req IDs | Owner | Acceptance criteria |
|---|---|---|---|---|
| **0** | Repo skeleton, `go.mod`, Makefile, multi-arch Dockerfile, chart skeleton + `values-default.yaml`, generated `deploy/plain-yaml/all.yaml`, CI, image publication, LICENSE, `IMPLEMENTATION.md` | R11.1–R11.7, R11.9–R11.16, R12.1–R12.3, R13.9, R13.10, R14.1, R15.1, R15.2 | A4 | **R11.14**: image built **and deployed to the reference cluster**, pod `Running`, `/healthz` answers via the Service. Plus: `go build ./...` clean; `buildx --platform linux/amd64,linux/arm64` succeeds; `helm lint` clean; all three profiles render; CI green; `IMPLEMENTATION.md` exists with the WBS |
| **1a** | Discovery + kube client (per-port transport table) + `/api/v1/clusters` + `/healthz` + `/readyz` + **`web/embed.go` and the `GET /` + `GET /c/**` routes** that serve the 1b SPA (R10.1's Go-side half, **1b-owned**: A4 wrote `web/embed.go` *and* `internal/api/spa.go` in the 1b commit `14d4df0`, so 1a must not duplicate them — A1's 1a work integrates against those routes rather than supplying them) + **`testdata/api/clusters.json`** | R1.1–R1.9, R1.9b, R1.10, R1.11, R8.1, R8.7, R12.1–R12.3, R13.7, R13.8, R16.8 | A1 | **All 4 reference clusters returned by `/api/v1/clusters`** with correct phase, instance counts, **servingInstances**, primary and PG major (16 vs 17 distinguished); `freeidp-pg` renders its degraded state (R1.2 **[A]**); `rev-proxy-pg` shows its Pooler; R1.6 passes |
| **1b** | **The web UI, pulled forward from Phase 5** (see the note below this table): `web/` React + TypeScript + Vite app built and `go:embed`-ed into the binary (R10.1), the R10.2 design system, and the **Fleet** page rendering `/api/v1/clusters` (R10.4), with the R16.7 visual-regression harness stood up at the same time. Runs **in parallel with 1a** against the same `/api/v1/clusters` contract. | R10.1, R10.2, R10.4 *(inventory columns)*, R10.12, R10.16 *(page, sort)*, R16.7 | A4 | The SPA is served by the Go binary at `/` (no separate web server), and the Fleet page renders all 4 reference clusters from live API data: health distribution, sortable table with phase/instances/PG major, `freeidp-pg` showing its degraded state. R10.2's library decision recorded in `IMPLEMENTATION.md` §4 as a dated entry; R16.7 harness runs in CI and has ≥1 passing visual test per R10.12 state it can reach at this phase. |
| **2** | Collector framework + PG version matrix + `database`/`settings`/`replication`/`wal` collectors + `/metrics` v1 (aggregate) + `pgsi_up` + global scheduler budget + **`pg_stat_statements` enablement flow** + `validate-env.sh` + ground-truth fixture + clock contract | R2.1–R2.9, R3.15, R8.1, R8.7, R9.2–R9.4, R9.7 (partial), R9.12, R11.8, R12.4, R12.5, R13.1, R13.2, R13.5, R13.11, R16.1, R16.2, R16.3, R16.5, R16.6 | A1 | Collection round < 3 s / < 40 round-trips per cluster (R13.1) on all 4 clusters; PG16 and PG17 both collect via the version matrix (R2.4 **[A]**); `/metrics` passes `promtool check metrics`; `scripts/validate-env.sh` prints a pass/fail table; the seeded fixture's values match ground truth (R16.6) |
| **3** | `pg_stat_statements` collector + normalizer + query store + top-N API + per-query metrics family + rollup endpoints | R3.1–R3.8, R3.13, R3.14, R8.2 (insights endpoints), R8.4, R8.5, R9.5, R9.5b, R16.3 | A5 | Top-N table for each cluster matches a manual `SELECT … FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 50` within tolerance; normalizer output matches the fixture corpus; counter-reset test passes; series count within the §5 budget |
| **4** | ServiceMonitor/PodMonitor, PrometheusRule with `critical` tier, cardinality assertion, end-to-end scrape by the reference env's kube-prometheus-stack | R9.1, R9.6, R9.8–R9.11, R13.3 (API p50/p95), R13.4, R16.4 | A2 | **The existing `prom-operator` Prometheus scrapes us and the series are queryable** (R9.9 **[A]**); `promtool check rules` passes; all 4 clusters' series present with correct labels |
| **5** | Full API surface + OpenAPI + auth + PromQL pair + the **Cluster Overview** page and the remaining shell requirements (page set R10.3, themes R10.13, load budget R10.14, time range R10.15, ⌘K search R10.17, cluster switcher R10.24). The SPA shell, Fleet page and test harness moved to **1b** in v0.3.0. | R8.1–R8.9, R8.10, R8.11, R10.3, R10.13, R10.14, R10.15, R10.17, R10.24 | A2 | `GET /api/v1/metrics/query?query=pgsi_up` returns a Prometheus-shaped result; OpenAPI doc committed and CI-validated; Cluster Overview renders R10.5(a) query-load from R9.4 data and degrades R10.5(b) correctly when `metrics.k8s.io` is absent; `helm install` from a tarball works (R11.6 **[A]**); the 1b-owned Vitest + Playwright suites still pass and cover the pages added at this phase |
| **6a** | Active queries + blocking chain + wait events + terminate + Active Queries & Wait Events pages + alerts surface | R4.1–R4.8, R10.8, R10.9, R10.25, R10.26 | A1 | Active list matches `pg_stat_activity` exactly on all 4 clusters (R4.1 **[A]**, R4.8 value equality); a real blocking pair renders as a chain; terminate works behind its gate |
| **6b** | Query Insights UI + percentiles + plan capture + tags + client-address rollup + load graphs + batched series endpoint | R3.9–R3.12, R9.13, R10.5, R10.6, R10.7, R10.18–R10.23, R10.27, R10.28 | A6 | Queries page renders the R3.2 column set exactly (incl. `Load by lock wait` = `n/a`); Query Detail renders load + latency + plans; the R10.27 not-installed screen renders for every cluster lacking the extension |
| **7** | System Insights (OS + DB families, summary cards, compare-5, PNG/CSV export) + log source | R5.1–R5.8, R5.10, R9.7 complete, R10.10 | A2 | The five summary cards render for all 4 clusters (R5.1 **[A]**, values matched per R4.8's rule); CSV export byte-compares against a golden; PNG export produces a valid image; compare-5 works with 4 available clusters |
| **8** | Pooler insights + bloat/autovacuum/TXID recommenders + events timeline | R6.1–R6.5, R7.2, R7.4, R7.5, R7.6, R5.9, R10.11 | A1 | `rev-proxy-pg-pooler` renders full pool state (R6.1); pgbouncer attribution produces a real client IP in the **session list** where the Postgres side shows the pooler (R6.4 **[A]**, scoped per R6.4); recommendations render with evidence |
| **9** | Docs site, runbooks, README polish, screenshots, lab profile, v0.1.0 release | R11.6, R11.9, R13.6, R14.3–R14.8, R15.2, R15.3 | A4 | Two-command quickstart executed from a clean namespace and verified; release tagged with SBOM; every metric and route documented (R14.7) |

**Post-0.1** (defined in §4, not scheduled in v0.1): R7.1 index advisor, R7.3 rightsizing, and the
§6 rows marked post-0.1 (flag recommender, OOM session cancel, performance capture, custom
dashboards, AI troubleshooting, end-to-end trace).

**Phase 0–2 is the "does this work at all" spine** and is backend-first where the brief's
non-negotiable sits ("ensure the solution provides prometheus metrics so that multiple instances can
be centralized"). **It is no longer UI-last.** In v0.2.0 the UI began at Phase 5, which meant four
phases of headless backend before a single graph — a sequencing error, not a spec gap: R10 was always
written, but it was scheduled by *data dependency* (collect, then store, then serve, then draw)
rather than by what the brief leads with ("web based … I would expect the UI … to have extremely easy
to use graphs and visualizations"). **v0.3.0 splits Phase 1 into 1a (backend, A1) and 1b (the embedded
SPA + Fleet page, A4), in parallel.** Two reasons beyond taste: (i) it costs no agent budget — the
Phase 1 backend was always A1's, and the Phase 4/5 backend that A2 owns doesn't start until Phase 4,
so the UI work slots into A4's capacity after Phase 0 without touching anyone else's critical path;
and (ii) it de-risks **R10.14** — Fleet's own <1.5 s load budget is specified *for* that page, and
writing it at Phase 5 with no Fleet page in the room is how it gets built wrong and reworked. (The
first draft of this note also claimed R8.11; A3-N4 is right that the batched series endpoint serves
the Queries page at 6b, which 1b does not render, so the argument does not extend to it.) Phase 5 keeps the Cluster Overview page and the shell requirements that
genuinely need Phase 2–4 data.

### 7.3 Requirement → phase index

Generated from §7.2; every ID in §4 appears exactly once **as a primary assignment**. This table is
the fix for the 0.1.0 defect where 18 IDs were defined and scheduled in no phase (A3-M7), and it is
the source for §6's Phase column and for `IMPLEMENTATION.md`'s R14.8 traceability table. **The
set-equality between §4's ID column and this index is now asserted**, by
`scripts/check-parity-matrix.sh` (A3-Q2/D2): before that check existed, deleting an ID from a row
shrank the index to 169 and the script still reported "agrees", which is precisely the B1/B2 defect
class this table exists to prevent — validated §6 against the index, never the index against §4.

**The annotation convention (A3-B3, v0.3.0; forms named canonically per A3-Q1).** An ID may appear
under more than one phase only through one of four spelled forms. A bare second assignment is a
defect — two phases claiming one requirement with no statement of who is on the hook.

| form | literal spelling | meaning |
|---|---|---|
| **owner** | `X → n` | phase `n` owns `X` |
| **re-validation** | `X → n` *(also re-validated at m)* | `n` owns it; `m` re-tests it without owning it |
| **scope-split** | `X *(scope a)* → n · *(scope b)* → m` | the requirement splits by scope, each half owned separately — the form R10.4 uses (`10.4 *(inventory columns)* → **1b** · *(metric columns + top finding)* → **5**`); R10.5 was named here until A3's ruling below, and R10.5's index row carried no annotation at all — the generated list at the bottom of this section proved it |
| **universal** | `X → every phase` | applies continuously rather than once (R14.2, R14.8) |

The checker parses all four: ownership comes from the `→ n` part in every form, and the
parentheticals are stripped rather than mistaken for assignments. **The annotated-ID list is its
output, not prose.** The v0.3.0 text hand-enumerated "four annotated cases" and was wrong on
arrival — R9.7's `9.7 (partial) → 2 · 9.7 (complete) → 7` was missing from the list, and the
parser silently dropped `1.9b`, `9.5b`, `9.7` and `10.16` (A3-Q1/D3: a greedy `[a-z]?$` swallowed
the `b` suffixes, and a comma inside `*(page, sort)*` cut the token before the asterisks were
stripped). The script now prints the set — 25 IDs as of this commit, index 174 where the old parser
found 170 — so the enumeration cannot rot on the next edit.

**A3's ruling on R10.5's assignment (A2 routed it; the table named R10.5 as the scope-split
exemplar while its index row was bare).** The cell at §4 R10.5 has three parts, not two: **(a)
Query load**, **(b) Resource load**, and an unlettered *"Plus the R5.1 summary cards, a
replication topology view sourced from S4 (R5.5), and instance chips with roles."* The phase
boundary runs along the content, not the letters: (a) is the Query Insights page, whose data
(S2/R9.4) already ships in the store, so **6b** owns it; (b) plus the R5.1 cards plus the S4
topology are System Insights content, and R5.1, R5.3 and R5.5 are assigned to **7** by §7.2's own
phase row. So the annotation is `10.5 *(Query load)* → **6b** · 10.5 *(Resource load, R5.1 cards,
topology)* → **7**` — the id repeated in each part, which is the shape R10.4's row uses and the
shape the parser needs, since it splits cell 2 on `·` and expands an id out of every part.
A6's reading — `(a) → 6b · (b) → 7` — gets the boundary right and the spelling wrong: the letters
label §4's cell, they do not name the scopes, and they cannot carry the unlettered *Plus* clauses.
R5.3's degradation rule is what makes 6b's half renderable today: it ends "Never render a blank
chart without saying why", so the Resource load panel's absence is itself specified work, and the
graph fills in when 7 supplies the S7/`metrics.k8s.io` data rather than being built from scratch
by it.

**Standing re-validation rule (A3-Q1.3).** **Phase 9 re-validates every requirement shipped before
it.** A release pass is therefore one rule, not one §7.3 edit per ID — which is how R11.6 ended up
as a parenthetical in the first place. R11.6 keeps its annotation because Phase 5 is named
specifically; the `at 9` half is now covered by this rule rather than by the row.

| Group | IDs → phase |
|---|---|
| R1 | 1.1–1.9, 1.9b, 1.10, 1.11 → **1a** |
| R2 | 2.1–2.9 → **2** |
| R3 | 3.1–3.8, 3.13, 3.14 → **3** · 3.15 → **2** · 3.9–3.12 → **6b** |
| R4 | 4.1–4.8 → **6a** |
| R5 | 5.1–5.8, 5.10 → **7** · 5.9 → **8** |
| R6 | 6.1–6.5 → **8** |
| R7 | 7.2, 7.4, 7.5, 7.6 → **8** · 7.1, 7.3 → **post-0.1** |
| R8 | 8.1, 8.7 → **1a** *(partial)* · 8.2, 8.4, 8.5 → **3** *(partial)* · 8.1–8.9, 8.10, 8.11 → **5** *(full surface; re-validates the 1a and 3 partials)* · 8.1b, 8.1c → **5** *(the OpenAPI generation + CI clauses split out of 8.1 by D13)* |
| R9 | 9.2–9.4, 9.7 (partial), 9.12 → **2** · 9.1, 9.6, 9.8–9.11 → **4** · 9.5, 9.5b → **3** · 9.7 (complete) → **7** · 9.13 → **6b** |
| R10 | 10.1, 10.2, 10.4 *(inventory columns)*, 10.12, 10.16 *(page, sort)* → **1b** · 10.3, 10.4 *(metric columns + top finding)*, 10.13, 10.14, 10.15, 10.17, 10.24 → **5** · 10.8, 10.9, 10.25, 10.26 → **6a** · 10.5 *(Query load)*, 10.6, 10.7, 10.18–10.23, 10.27, 10.28 → **6b** · 10.5 *(Resource load, R5.1 cards, topology)*, 10.10 → **7** · 10.11 → **8** |
| R11 | 11.1–11.7, 11.9–11.16 → **0** · **11.8 → 2** · (11.6 also re-validated at **5** and **9**) |
| R12 | 12.1–12.3 → **0** *(also re-validated at **1a**)* · 12.4, 12.5 → **2** · 12.6–12.9 → **0** |
| R13 | 13.9, 13.10 → **0** · 13.1, 13.2, 13.5, 13.11 → **2** · 13.7, 13.8 → **1a** · 13.4 → **4** · 13.3 → **4** · 13.6 → **9** |
| R14 | 14.1 → **0** · 14.8 → **0** (updated every phase) · 14.3–14.7 → **9** · 14.2 → **every phase** · 14.9 → **1a** (first applies at the 1a/1b split) |
| R15 | 15.1, 15.2 → **0** · 15.3 → **9** |
| R16 | 16.1, 16.2, 16.3, 16.5, 16.6 → **2** · 16.4 → **4** · 16.7 → **1b** (harness created with the SPA) · 16.8 → **1a** |

---

## 8. Validation plan against the reference environment

The four clusters in `docs/ENVIRONMENT.md` are chosen well enough to be the test fixture; each
exercises a specific code path. **The "Reachable for SQL?" column is new** and fixes the 0.1.0
defect where checks were phrased "all 4 clusters" while one cluster has zero serving instances
(A3-B4): `freeidp-pg-1`'s postgres container is in `CrashLoopBackOff` and `-2` is `Pending`, so
there is no Postgres to query there at all.

| Cluster | What it exercises | Reachable for SQL? | Requirements proven |
|---|---|---|---|
| `ai-api-gateway/rev-proxy-pg` (2 inst, PG **17.11**, **Pooler**) | Normal HA path + pgbouncer attribution + pooler metrics + PG17 column sets (`shared_blk_read_time`, `pg_stat_checkpointer`) + `lastFailedWAL` anomaly on a "healthy" CR | **yes** | R1.2, R1.10, R3.5, R6.1–R6.5, R2.4, R5.5 |
| `cookbook/cookbook-pg` (1 inst, PG **17.11**) | Degenerate single-instance path: empty replication set, no replicas | **yes** | R5.5, R9.3 |
| `freeidp/freeidp-pg` (2 inst, PG 17, **disk pressure + Pending replica + crash-looping primary**) | Degraded rendering, disk-pressure metrics, a non-serving instance, `phaseReason` surfacing, R1.2's container-status rule | **no** (zero serving instances) | R1.1, R1.2, R5.1, R10.12, R10.28 |
| `open-webui/open-webui-pg` (2 inst, **PG 16.11**, 2 Gi) | The PG16 branch (`blk_read_time` present, `pg_stat_checkpointer` absent, `op_bytes` present) and a small-storage cluster | **yes** | R2.4, R13.9 |

**Acceptance checks** (the `[A]` requirements, scripted as `scripts/validate-env.sh`, R16.5). Checks
8 and 9 run against **the 3 clusters with a serving postgres**; `freeidp` is validated through S1
and S4 only:

1. `GET /api/v1/clusters` returns exactly the 4 clusters with correct phase/instances/**servingInstances**/primary/major. *(R1.5, Phase 1a)*
2. The `freeidp-pg` entry reports the disk-pressure `phaseReason`, the Pending instance, **and `servingInstances: 0`**. *(R1.2, Phase 1a)*
3. `pgsi_up{cluster="cookbook-pg"} == 1` and its replication section renders "single instance". *(R9.12, Phase 2 — the API's replication section; the UI that renders it, R5.5, lands at 7)*
4. Version-matrix unit tests cover PG 13–18 (13/14 marked `impossible` per R13.9); live collection succeeds on PG 16 **and** PG 17. *(R2.4, Phase 2)*
5. `promtool check metrics < our /metrics` exits 0; `promtool check rules chart/templates/prometheusrule.yaml` exits 0. *(R9.9, R9.11, Phase 4)*
6. The **existing** `monitoring/prom-operator` Prometheus, with no config change beyond applying our ServiceMonitor, returns series for all 4 clusters. *(R9.9, Phase 4)*
7. `helm template` golden tests pass for all three profiles; `helm install` from a packaged tarball into a fresh namespace serves the UI in < 5 min. *(R11.6, R11.12, R11.13, Phase 5)*
8. Active-queries API output equals a direct `pg_stat_activity` dump (modulo our own session, R2.8) on **the 3 SQL-reachable clusters**, with value equality per R4.8. *(R4.1, Phase 6a)*
9. The five System Insights cards render for **the 3 SQL-reachable clusters**, each value matched to its source query within tolerance and both printed. *(R5.1, Phase 7)*
10. For `rev-proxy-pg-pooler`, the pgbouncer-sourced client address in the **active-session list** differs from the Postgres-side `client_addr`, proving attribution works on the surface where it is derivable (R6.4's scoping). *(R6.4, Phase 8)*

---

## 9. Risks

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| K1 | **Prometheus cardinality explosion** kills adoption (the classic failure mode of this product class) | High | High | §5 budget with a CI-checkable assertion (R13.4); `topN` default 50 **per cluster**; query text never a label outside top-N; the R5.4 500-database guard |
| K2 | **`pg_stat_statements` is not installed** on most CNPG clusters (measured absent on all three reachable reference clusters), so the headline feature has no data on a stock install | **Confirmed** | High | R3.1 detection + **R3.15 enablement as a product flow** (scheduled Phase 2 so the rolling restart has lead time); R10.27 makes the not-installed screen first-class; Phases 1–2 and 4–5 deliver value without it; documented in the README's first paragraph, not a footnote |
| K3 | **Credential acquisition** — reading `<cluster>-app` needs secret read access; some teams won't grant it | Medium | Medium | R1.8 four-level precedence; R12.4 documents the `pg_monitor` role path (and its R3.10 consequence); S3 (9187) needs no SQL credential at all, so Phase 2 works credential-free |
| K4 | **Scope creep** — the parity list is ~25 capabilities and the brief also demands implementation | High | High | The phase table is the contract; anything not in §7.2 is out; `IMPLEMENTATION.md`'s deviations log makes scope changes visible rather than silent |
| K5 | **SQLite single-writer** limits horizontal scale | Medium | Medium | §3.4 documents the limit, the R11.15 access-mode contract and the v1.x path (Postgres-backed store); R13.2 sets a 50-cluster target SQLite comfortably meets |
| K6 | **PG version drift** — PG18 changes query-id jumbling and removes/renames columns; a 17→18 upgrade silently breaks baselines | Medium | Medium | R2.4 matrix-as-data with per-view column sets; R3.4's fingerprint + R9.5b's fingerprint-labelled family survive id changes; R16.2 tests on two majors live |
| K7 | **The reference environment is one k3s cluster** — we cannot validate multi-cluster or multi-tenant behaviour from it | Medium | Medium | R13.9 marks untested cells honestly; R11.6's lab profile gives a kind/minikube validation path |
| K8 | **CNPG operator instability in the reference env** (99 restarts) may make discovery flaky during development and be mistaken for our bugs | Medium | Low | R1.9 stale-grace behaviour; R8.7 `/readyz` gated on a real sync; validation script distinguishes "operator unreachable" from "our parse failed" |
| K9 | **Frontend quality** — "extremely easy-to-use" is the brief's emphasis and the easiest thing to under-deliver on | Medium | High | R10.2 forces the `dataviz` skill before chart code (with a named fallback); R10.12/R10.28 force designed empty states; R10.14 sets a load-time budget; **R16.7 adds UI tests** where 0.1.0 had none; A4 owns UI as a dedicated role |
| K10 | **Our own `/metrics` duplicates `cnpg_*`**, so users see the same number twice under two prefixes | Medium | Low | R9.7 re-exports deliberately with a documented mapping table; the chart README explains which prefix to alert on |
| **K11** | **The reference environment's k3s v1.37.2 is outside CNPG 1.30's supported Kubernetes window** (CNPG supports 1.34–1.36; 1.37 is "tested but unsupported"). An operator bug we hit may be a k3s-1.37 artifact, and R13.9 cannot claim a supported-cell result from it | Medium | Medium | R13.9 states it explicitly; a kind-hosted CNPG on a supported k8s version is the R16.2 integration path, so "tested" cells are tested somewhere supported |

---

## 10. Open questions

| # | Question | Answer |
|---|---|---|
| Q1 | Product name and binary name — `pgsql-insights` / `pgsi`? | **Resolved.** Repo is `pgsql-insights`; binary and metric prefix are `pgsi`. Recorded as D3 in `IMPLEMENTATION.md`. |
| Q2 | Ship the SPA embedded, or also as a standalone static bundle for CDN hosting? | Embedded only in v1 (R8.6); `--serve-ui=false` covers API-only. |
| Q3 | Do we need `kubectl pgsi` as a plugin surface, mirroring `kubectl cnpg`? | Post-0.1. The API is the contract; a plugin is a thin client over it. |
| Q4 | Multi-user auth (per-user) vs. one shared token? | Shared token in v1 (R8.8); per-user via OIDC post-0.1. **Consequence:** R10.6's saved views are shared/cluster-scoped until OIDC lands. |
| Q5 | Should the store be Postgres-backed by default on CNPG clusters (dogfooding)? | No — SQLite default keeps the install dependency-free; Postgres-backed is the documented scale-up path (R11.15). |
| Q6 | Is Apache-2.0 correct, and does the org exist for `ghcr.io/<org>/`? | Apache-2.0 (R15.1). **The org name is still unresolved and is now a Phase 0 blocker** (A4-M9): R11.16 names image publication as the Phase 0 CI step that consumes it, and R11.9's quickstart cannot be copy-pasted until the registry exists. Until then, the packaged-tarball install is the verifiable form. |

---

## 11. Review log

The brief requires **two adversarial reviews** of this document, with feedback incorporated.

| Round | Reviewer | Scope | Findings | Incorporated |
|---|---|---|---|---|
| — | internal consistency pass | R15/R16 authored from the 0.1.0 self-review | 2 groups added | ✅ in `97172a4` *(the 0.1.0 §11 footer said "added from an internal consistency pass" while the table had no row for it — A3-N8; this row is that row)* |
| 1 | **A3** (opus) — architecture, feasibility, spec-vs-research consistency → `docs/reviews/round-1-architecture.md` | 7 blocking · 10 major · 8 minor · 6 proposed IDs | ✅ all 25 dispositioned |
| 1 | **A4** (sonnet) — product scope, UI/UX, packaging, testability → `docs/reviews/round-1-product.md` | 8 blocking · 14 major · 8 minor · 14 proposed UI reqs + 8 other IDs | ✅ all 30 dispositioned |
| 2 | **A3** (opus) — v0.3.0 architecture review → `docs/reviews/v0.3.0-architecture-review.md` | 4 blocking · 8 major · 6 minor | ✅ in `c416390` — 26 spec edits, with the two reviewer proposals filed as **R16.8** and **R14.9** under existing groups per rule 4 (D16), and `scripts/check-parity-matrix.sh` written to make the propagation CI-enforced rather than re-breakable |
| — | **A3** (opus) — Phase 1a follow-up, re-review of `f08b6ac` | the two rulings D18 records, plus 4 defects in the amendment itself — two of them in the new text (`--cors-origins` mis-spelled vs the code's `--cors-allow-origin`; the R8.9 row citing `S1` where the auth finding is `S2`), one stale (D18 describing `main.go`'s flagset as it was at HEAD, not as A1's working tree now has it), one missing (no v0.3.0 row in this table — the same class A3-N8 flagged at 0.1.0) | ✅ folded into this document and D18 amended |
| — | Live probe P1–P9 (`docs/research/live-probe.md` §9) | 9 queued changes | ✅ folded in (P1→R13.9/§8, P2→R3.1/R3.15/R12.8, P3→R1.10, P4→R1.11, P5→R5.5/R10.5, P6→R1.2, P7→R11.2/R11.14, P8→R6.1, P9→R16.3) |
| — | **Consolidation into `0.2.0`** | this document | ✅ |
| — | **Consolidation into `0.3.0`** (Phase 1 split into 1a/1b, D13) | this document | ✅ — the round-2 incorporation `c416390` plus the Phase 1a rulings `f08b6ac`/D18. The 0.2.0 row existed *solely* because A3-N8 flagged that the 0.1.0 footer claimed a pass the table didn't list; the header has read `0.3.0` since `c416390` with no matching row, which is that same defect one version up |
| — | **A3** (opus) — Phase 5 Go-half review of `f9aae41` (auth, CORS, OpenAPI, and the §3.7 table written for it) | 6 code findings (2 major in `auth.go`, 1 major in `cors.go`, 3 minor) · 3 defects in the §3.7 requirement→file→status table · 1 stale implementation note | ✅ this commit — the spec half: R8.1 split into **R8.1 / R8.1b / R8.1c** (D13), R8.8 gains the **fail-closed** and **separate-secret** clauses (D14), R8.9's implementation note marked resolved, R9.4 gains **`lock_time`** (the R9.11 ruling). The code fixes are A2's; the `git mv` to `api/openapi.yaml` and the CI validator step are tracked as R8.1(a) and R8.1c rather than claimed here |

### Reviewer disagreements, ruled on (rule 4)

Where A3 and A4 conflict, the spec owner decides and records the reasoning rather than averaging.

**1. R3.2's per-query load columns — A3's reading adopted.**
A4 (B2) said `Load by CPU`, `Load by IO wait` and `Load by lock wait` are all unavailable per query.
A3 (B2) showed `Load by IO wait` **is** derivable per query (`blk_read_time`/`blk_write_time` ≤PG16,
`shared_blk_read_time`/`shared_blk_write_time` PG17+), so only lock wait is missing. A3 is more
precise and cites the source correctly. R3.2 now sources CPU and IO per query, marks lock wait
`n/a`, and keeps A4's other two fixes (sourcing the three load columns at database/user level via
R3.7, and recording the `Analyze latency` deferral as a Gap).

**2. R3.6's query-length range — A4's two-profile range adopted.**
A4 (B1) said 0.1.0 shipped Enterprise's 256–4 500/default 1 024 while §1.2 declares Enterprise Plus
(1 024–100 000, default 10 000) the baseline, and that §5 mislabelled 4 500 B as "(Enterprise
Plus)". A3 marked R3.6 correct — but checked it against `cloud-sql-insights.md` §3.2's *Enterprise*
default only, so it validated the number without noticing the edition mismatch. A4's fix (range
256–100 000, default 1 024, 10 000 documented as the E+ default) satisfies both readings; §5's
mislabel is corrected in the same edit.

**3. R2.4's `pg_stat_bgwriter` → `pg_stat_checkpointer` — A3's framing adopted.**
A4 listed it under "what I checked and found correct" ("R2.4's five enumerated renames all check
out"). A3 (B7) showed the fifth is not a rename: at PG17 `pg_stat_bgwriter` **shrinks 11 → 4
columns** while checkpoint stats move to a **separate new view**, and CNPG ships three queries
(`pg_stat_bgwriter`, `pg_stat_bgwriter_17`, `pg_stat_checkpointer`). Implementing it as a rename
loses four still-valid bgwriter families on our PG17 clusters. A3's version matches the live-probe
family diff exactly (7 bgwriter families on PG16, 9 checkpointer families + `bgwriter_stats_reset_time`
on PG17), which is the measurement that settles it. R2.4 now says "per-view column sets per major,
not a rename table".

**4. ID collisions — one ID per feature, and the numbering scheme preserved.**
*Extension enablement:* A4 proposed `R3.15` at **Phase 2**; A3 proposed `R17` at **Phase 3**. Adopted
**R3.15, Phase 2** — filed under R3 rather than as a new R-group (R1–R16 is the taxonomy; adding
R17+ as new groups breaks it), and on A4's argument that a rolling restart takes hours and cannot
be a same-phase prerequisite for the Phase 3 collector. A3's `R17` is recorded as an alias.
*`pgsi_up`:* A4 proposed `R9.11`; A3's N2 renumbers the existing `R9.9b` to `R9.11`. Both cannot
have it. Adopted **R9.11 = the renumbered `R9.9b`** (A3's N2, since retiring the letter suffix is
the fix) and **R9.12 = `pgsi_up`**, with A3's fuller label set `{namespace,cluster,pod,instance_role}`
— which also answers A4's B8 question about whether it carries `pod`.
*General rule applied:* all eight of A3's proposed IDs (R17–R23) and all of A4's (R3.15, R9.11,
R11.12–R11.14, R13.11, R16.6–R16.8, R1.10, R1.11) are absorbed into the existing R-groups, with the
mapping recorded here so no finding is lost.

### Disposition summary

| Reviewer | Blocking | Major | Minor | Disposition |
|---|---|---|---|---|
| A3 | 7 | 10 | 8 | 24 accepted, 1 partially (B2 — see ruling 1), 0 rejected |
| A4 | 8 | 14 | 8 | 28 accepted, 2 partially (B2, B1 — see rulings 1–2), 0 rejected |

Both reviews' "what I checked and found correct" sections are recorded in their files and informed
which 0.1.0 requirements were left unchanged.

---

## Appendix A — Change log from 0.1.0 to 0.2.0

| Area | Change |
|---|---|
| Header | PG range 13–18 → **15–18** (13/14 documented, unvalidated); Kubernetes 1.29–1.37 → **1.34–1.36** with the reference env's 1.37 flagged; CNPG 1.30.x → **1.29–1.30**; measured PG versions 16.11/17.11 added |
| §1.3 | Added the Grafana-embedding non-goal with its argument (A4-M6) |
| §1.4 | "Zero per-cluster prerequisites" row corrected — one gated `spec.parameters` patch unlocks Query Insights (A3-B3) |
| §3.1 | `--role=collect\|serve` deferred to v1.x; v1 ships one role (A3-M1); subcommand surface unified with §3.5 |
| §3.2 | S2 reachability qualified; S3 family counts added; **S10 `pods/log`** added (A3-M6); S3 `pg_stat_io` caveat added (A3) |
| §3.4 | Access-mode row added (A3-M1/R20) |
| R1 | +R1.10 per-port transport table, +R1.11 `pods/proxy` caveat; R1.2 now reads container statuses; R1.5 gains `servingInstances` |
| R2 | R2.4 reframed to per-view column sets (A3-B7); +R2.9 global scheduler budget (A3-M2) |
| R3 | R3.1 points at R3.15; R3.2 column set corrected; R3.3 PG13 path marked unvalidated; R3.4 canonical key = fingerprint; R3.5 PG≤10 branch demoted to a note; R3.6 two-profile range; R3.9 buckets named + bias stated; R3.10 three EXPLAIN constraints added; R3.12 all six tag keys + one spelling; **+R3.15 enablement flow** |
| R4 | R4.1 gains `Query type`, the summary scorecard, icons, and the disabled-`Action` rule; R4.4 names `pg_blocking_pids`; **+R4.8 value-equality acceptance** |
| R5 | R5.1's Log-errors card sourced (R5.10); R5.5 prefers S4; **+R5.10 log-source ingestion** |
| R6 | R6.1 gains the 80-family measurement; R6.4 scoped to sessions + rollup (A3-M5) |
| R7 | R7.1 and R7.3 moved post-0.1; R7.3 and R7.4 labelled CNPG-derived (A4-M11) |
| R8 | R8.2 gains the series + search endpoints; R8.3 fixed-catalogue contract (A3-B6); **+R8.10 pagination, +R8.11 batched series** |
| R9 | R9.5b fingerprint label (A3-M10); **R9.9b → R9.11**; **+R9.12 `pgsi_up`, +R9.13 histogram accounting**; R9.8 gains the scheduler metrics |
| R10 | R10.2 named fallback; R10.3 gains four rollup pages; R10.5 split into query-load + resource-load; R10.6 saved-view scoping; **+R10.15–R10.28** (14 UI requirements from A4's missing-UI list) |
| R11 | R11.2 distroless-static decided + `httpGet` probes + `/tmp` emptyDir; R11.4 "compressed" defined; R11.6 three profiles; R11.9 tarball fallback + org dependency; **+R11.12 values-default, +R11.13 generated plain-yaml, +R11.14 deploy-and-observe, +R11.15 access-mode, +R11.16 image publication** |
| R12 | R12.1 gains `pods/log`; R12.4 states the `SELECT` consequence; R12.5 uses `-ca` + `verify-ca` (A3-N5); R12.8 generalized to all mutations; R12.9 gains `pods/log` in the threat model |
| R13 | R13.4 number corrected; R13.9 split into two axes with cell states; **+R13.11 clock contract** |
| R14 | R14.1 "same commit" scoped to the last commit of the phase; **+R14.8 requirement→file→status table** |
| R16 | R16.3 names the seven `testdata/` files; **+R16.6 ground-truth fixture, +R16.7 UI tests** |
| §5 | Recomputed: **~24.3 k series / ~1.58 GB over 30 d at 60 s** (was "~22 k / 2.6 GB at 15 s"); `topN` declared per cluster; histogram family added; two more divergence rows |
| §6 | Phase column generated from §7.2 (four wrong cells fixed); 12 missing Cloud SQL capability rows added |
| §7 | Phase 6 split into **6a/6b**; R3.10–R3.11 resolved to 6b and removed from Phase 8; **all 167 IDs indexed** in §7.3 |
| §8 | "Reachable for SQL?" column added; checks 8/9 re-scoped to the 3 SQL-reachable clusters; check 2 gains `servingInstances: 0` |
| §9 | **+K11** (reference env outside CNPG's supported k8s window); K2 upgraded from "likely" to "confirmed" |
| §10 | Q1 resolved; Q6 escalated to a Phase 0 blocker |
| §11 | Full review log, four rulings, disposition table |
