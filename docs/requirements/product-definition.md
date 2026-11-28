# Product definition

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

