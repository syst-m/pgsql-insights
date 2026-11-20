# Target Environment (observed 2026-11-20)

The reference environment `pgsql-insights` is developed and validated against. Everything in the
spec must work here first; the spec's acceptance criteria are testable against these clusters.

## Kubernetes

| Field | Value |
|---|---|
| Distribution | k3s |
| Server version | v1.37.2+k3s1 |
| kubectl (client) | v1.36.1 |
| Kustomize (bundled) | v5.8.1 |
| Context | `default` (single context, current) |
| Nodes | 5 (names seen on pods: `um760-1`, `dmaf5-1`, `dmaf5-2`, `k8s-wrk-1`, `k8s-ctrl`) |

## CloudNativePG

| Field | Value |
|---|---|
| Operator | `ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0` in namespace `cnpg-system` |
| Operator health | Running, **99 restarts** (last 9m ago) — worth flagging as a real-world signal |
| API group | `postgresql.cnpg.io` (all `v1`, storage version `v1`) |
| CRDs installed | `clusters`, `poolers`, `backups`, `scheduledbackups`, `databases`, `databaseroles`, `imagecatalogs`, `clusterimagecatalogs`, `publications`, `subscriptions`, `failoverquorums` |

`failoverquorums.postgresql.cnpg.io` being present confirms a CNPG 1.30-era operator. The spec
targets the **CNPG 1.30 `postgresql.cnpg.io/v1` API surface**.

### Clusters under observation (the natural test set)

| Namespace | Cluster | Instances | Status | Primary | PG image | Storage | Notes |
|---|---|---|---|---|---|---|---|
| `ai-api-gateway` | `rev-proxy-pg` | 2 | Cluster in healthy state | `rev-proxy-pg-2` | `postgresql:17` | 10Gi | **Has a Pooler** (`rev-proxy-pg-pooler`, pgbouncer, `poolMode: transaction`, 2 instances, `type: rw`, `default_pool_size 20`, `max_client_conn 400`, `min_pool_size 4`) |
| `cookbook` | `cookbook-pg` | 1 | Cluster in healthy state | `cookbook-pg-1` | `postgresql:17` | 10Gi | Single-instance: no replicas, so replication views are empty — tests the degenerate path |
| `freeidp` | `freeidp-pg` | 2 | **Not enough disk space** | `freeidp-pg-1` | `postgresql:17` | 10Gi | `freeidp-pg-2` is **Pending** (unschedulable). Real degraded cluster: exercises disk-pressure + missing-replica + pending-pod surfaces |
| `open-webui` | `open-webui-pg` | 2 | Cluster in healthy state | `open-webui-pg-2` | `postgresql:16` | 2Gi | PG **16** (others are 17) — exercises the version matrix (e.g. `pg_stat_io` differences) |

This set is unusually good for validation because it includes: a healthy 2-instance cluster, a
single-instance cluster (empty replication set), a **degraded** cluster (disk pressure + pending
replica), a **pgbouncer pooler** (breaks client→query attribution, the classic Query Insights
problem), and **two different major PG versions** (16 and 17).

## Existing observability (must integrate, not replace)

| Component | Present | Consequence for us |
|---|---|---|
| kube-prometheus-stack (`prom-operator`) in `monitoring` | Yes — `prometheus-prom-operator-kube-prometh-prometheus-0`, grafana, node-exporter DaemonSet (5 pods) | Our `/metrics` must be scrapeable by *this* Prometheus. `ServiceMonitor`/`PodMonitor`/`PrometheusRule` CRDs exist (`monitoring.coreos.com/v1`) → ship a ServiceMonitor in the Helm chart |
| `prometheus-operator-prometheus-node-exporter` | Yes, 5 pods (one per node) | Real OS metrics available for the System Insights side, beyond what Postgres can see |
| `metrics-server` (kube-system) | Yes | Pod/node CPU+memory available via `metrics.k8s.io` |
| CNPG `spec.metrics` on our 4 clusters | **Not set on any cluster** | **No postgres_exporter sidecars exist today.** Our collector must not *require* enabling CNPG metrics — see below |

### The single most important environment finding

None of the four clusters enable CNPG's `spec.metrics` block, so there is no per-instance
`postgres_exporter` sidecar to scrape. Therefore the collector's primary data path must be
**direct SQL against the Postgres instances** (which always exists), with the CNPG instance-manager
API and an optional postgres_exporter/`spec.metrics` scrape as *additive* sources — never as a
prerequisite. A product that required users to patch every Cluster to turn on metrics would not be
"rapidly rollable out" here.

### Second finding: the instance-manager ports are always there

Inspecting `ai-api-gateway/rev-proxy-pg-2` shows the postgres container declares three ports
**without** `spec.metrics` being set:

| Port | Name | What it is |
|---|---|---|
| 5432 | `postgresql` | Postgres itself |
| **9187** | **`metrics`** | CNPG **instance manager** Prometheus endpoint — always present, no opt-in |
| **8000** | **`status`** | CNPG instance manager HTTP status/probe API — always present |

So there are two always-available, zero-config sources per instance: **direct SQL on 5432** and
**the instance manager on 9187/8000**. The postgres_exporter sidecar (from `spec.metrics`) is a
third, opt-in source. The spec's collector is built on the first two and treats the third as a
bonus, which is what makes zero-config rollout possible.

### Discovery metadata (exact, observed on a real pod)

Services for `rev-proxy-pg`: `rev-proxy-pg-rw`, `rev-proxy-pg-ro`, `rev-proxy-pg-r`,
`rev-proxy-pg-pooler` (all ClusterIP).

Pod labels on `rev-proxy-pg-2`:

```
app.kubernetes.io/component: database
app.kubernetes.io/instance:  rev-proxy-pg
app.kubernetes.io/managed-by: cloudnative-pg
app.kubernetes.io/name:      postgresql
app.kubernetes.io/version:   17
cnpg.io/cluster:             rev-proxy-pg
cnpg.io/instanceName:        rev-proxy-pg-2
cnpg.io/instanceRole:        primary
cnpg.io/podRole:             instance
role:                        primary
```

Pod annotations: `cnpg.io/nodeSerial`, `cnpg.io/operatorVersion` (`1.30.0`), `cnpg.io/podEnvHash`,
`cnpg.io/podSpec` (a JSON snapshot of the rendered pod spec — useful for reading the real resource
requests/limits and the PVC name without parsing the CR).

Secrets per cluster (`ai-api-gateway`): `rev-proxy-pg-app` (Opaque, 3 keys — app role credentials),
`rev-proxy-pg-ca`, `rev-proxy-pg-server` (TLS), `rev-proxy-pg-replication` (TLS),
`rev-proxy-pg-pooler` (TLS). The `-app` secret is the credential path a collector can read per
cluster; the spec also needs a least-privilege alternative (`pg_monitor` grant) for teams that
won't hand out the app password.

`app.kubernetes.io/version: 17` gives the PG major version directly from the pod label — cheaper
than a `SELECT version()` per poll.

## Local developer toolchain

| Tool | Version |
|---|---|
| Go | go1.26.1 darwin/arm64 |
| Docker | 29.9.2 (buildx v0.38.1 → multi-arch builds available) |
| Helm | v4.3.1 |
| kubectl | v1.36.1 |
| Node / npm | v25.8.0 / 11.16.0 |
| `kubectl cnpg` plugin | **not installed locally** (available via `kubectl cnpg` on clusters that ship it) |

Platform is `darwin/arm64`, so container images must be built `linux/arm64` + `linux/amd64`
(k3s nodes are arm64-capable here; the build must be multi-arch regardless for adoption).
