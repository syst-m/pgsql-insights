# Architecture

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
| S10 | **Pod logs** (`pods/log`) | `GET` on the pod log subresource | RBAC `pods/log: [get, list]` | Postgres log lines for the R5.1 **Log errors** card. Cloud SQL sources this card from `logging.googleapis.com/log_entry_count`, which we do not have | Only if `--log-source` is enabled (R5.10); **off by default** |
| S11 | **Kubernetes events** (`events`) | `watch` on the `events` API resource in the cluster's namespace | RBAC read on `events` (R12.1's default Role already grants it) (R12.1) | the R5.9 timeline's non-S1 half: pod-event records — fencing (`cnpg.io/fencedInstances`), promotions (`currentPrimaryTimestamp`), image pulls, OOM kills — plus the `kubectl get events` lines R5.9 also reads | Yes wherever R12.1's Role applies: same API server, same RBAC family as S1. The collector is R5.9's, and R5.9 is a Phase 8 row built early (R5.9's marking note) |

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
| `bloat` | 5 min | S2 `pg_stat_user_tables`, `pg_stat_user_indexes` | R7.2's two pairs and R7.4's autovacuum insight; table size via `pg_total_relation_size`, index size via `pg_relation_size` |

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
│   │   ├── locks.go  io.go  progress.go             # locks.go built at b51ea03 (R4.4's pg_locks input, 6a), io.go at 0da370d (R2.5, R2.7); progress.go not yet built
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

