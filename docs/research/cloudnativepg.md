# CloudNativePG (CNPG) — Observability Surface Inventory

Reverse-engineering reference for building an open-source "Query & System Insights" web product for
PostgreSQL clusters running under CloudNativePG on Kubernetes.

Compiled 2026-11-20. Everything below is transcribed from the live `cloudnative-pg/cloudnative-pg`
repository (tag `v1.30.2` and `main`), the live `cloudnative-pg.io/docs` tree, the GitHub release
notes, and the `prometheus-community/postgres_exporter` source (tag `0.21.1` + `master`), not from
memory. Provenance is in §0. Anything not directly evidenced is marked **(unverified)**.

---

## 0. Sources and verification

| Source | URL / path | What it yielded |
|---|---|---|
| Monitoring docs | `https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/main/docs/src/monitoring.md` (1084 lines) | §4.1–§4.9 — exporter port 9187, `cnpg-default-monitoring`, query schema, `usage` enum, output format, PodMonitor, TLS, operator metrics 8080, caching |
| Default metrics ConfigMap | `.../main/config/manager/default-monitoring.yaml` (491 lines) | §4.10 — the complete 14-query default metric set |
| CRD API reference (generated) | `.../main/docs/src/cloudnative-pg.v1.md` (3557 lines) | §4.2 — `MonitoringConfiguration`, `ClusterMonitoringTLSConfiguration`, `PoolerMonitoringConfiguration` |
| Cluster types | `.../main/api/v1/cluster_types.go` (2991 lines) and `.../tags/v1.30.2/api/v1/cluster_types.go` | §1.3 — full `ClusterStatus`, `ClusterConditionType`, `ConditionReason`, phase list |
| api/v1 package | `.../tags/v1.30.2/api/v1/` (`groupversion_info.go`, `addKnownTypes`) | §1.1–§1.2 — CRD set, kinds, plural/singular |
| CRD manifests | `.../tags/v1.30.2/config/crd/bases/*.yaml` (11 files) | §1.1 — scope, served/storage, subresources, printer columns |
| Other status types | `.../tags/v1.30.2/api/v1/{backup,pooler,database,databaserole,publication,subscription,failoverquorum,imagecatalog,clusterimagecatalog}_types.go` | §1.4 — status field names, phase enums |
| Labels/annotations | `.../main/pkg/utils/labels_annotations.go`; `docs/src/labels_annotations.md` | §2.1–§2.2 — every `cnpg.io/*` and `alpha.cnpg.io/*` key |
| Pod/service/spec builders | `.../main/pkg/specs/{pods,services,roles,podmonitor,secrets,jobs}.go`; `pkg/reconciler/instance/metadata.go`; `pkg/reconciler/persistentvolumeclaim/calculator.go`; `pkg/specs/pgbouncer/{deployments,services,rbac,podmonitor}.go` | §2.4–§2.7 — per-object label sets, service names/selectors, container ports, PodMonitor shapes |
| RBAC | `.../main/config/rbac/{role.yaml,role_binding.yaml,kustomization.yaml,*_role.yaml}`; `releases/cnpg-1.30.2.yaml`; `docs/src/rbac.md`; `docs/src/security.md`; `docs/src/kubectl-plugin.md:1480–1620` | §2.8.1–§2.8.6 — operator ClusterRole, per-cluster Role, per-pooler Role, documented plugin RBAC |
| Instance manager routing | `.../main/pkg/management/url/url.go`; `pkg/management/postgres/webserver/{remote,local,webserver}.go`; `pkg/management/postgres/webserver/metricserver/{metrics,multus}.go`; `pkg/management/pgbouncer/metricsserver/metricsserver.go`; `internal/cmd/manager/instance/run/cmd.go` | §3.1–§3.6 — every port, every path, auth model, response envelopes |
| Instance manager docs | `.../main/docs/src/instance_manager.md` | §3.8 — probe strategies, `startDelay`, `livenessProbeTimeout`, isolation check |
| Go collector metrics | `.../main/pkg/management/postgres/webserver/metricserver/pg_collector.go`; `.../metricserver/wal.go` | §5–§5.3 — every `cnpg_collector_*` name + HELP text + labels |
| PgBouncer metrics | `.../main/pkg/management/pgbouncer/metricsserver/{pgbouncer_collector,consts,lists,pools,stats}.go` | §5.4 — `cnpg_pgbouncer_*` names |
| Query parser | `.../main/pkg/management/postgres/metrics/{parser,mappings,collector}.go` | §4.3–§4.5 — CNPG's `UserQuery`/`ColumnMapping` schema, FQName rule, cache fields |
| Certificates | `.../main/pkg/certs/{selfsigned,certs,tls,k8s}.go`; `docs/src/certificates.md`; `api/v1/cluster_types.go` `CertificatesConfiguration` | §2.7, §3.3 — secret names, keys, fingerprint pinning |
| Versions | `.../main/pkg/versions/versions.go` | §1.0 — `Version = "1.30.2"`, default images |
| Release policy | `.../main/docs/src/supported_releases.md` (225 lines) | §1.0 — support table, EOL dates, release cadence |
| Container images | `.../main/docs/src/container_images.md` | §1.0 — supported Postgres versions, image tag rules |
| Release notes | `https://api.github.com/repos/cloudnative-pg/cloudnative-pg/releases` | §1.8 — observability-relevant changes in v1.30.2 / 1.30.1 / 1.29.3 / 1.30.0 / 1.29.1 / 1.28.3 / 1.29.0 |
| postgres_exporter source | `https://github.com/prometheus-community/postgres_exporter/archive/refs/heads/master.tar.gz` (VERSION `0.21.1`) — `collector/collector.go`, `collector/collector_names.go`, `config/config.go`, `exporter/queries.go`, `queries.yaml`, `docs/configuration.md` | §4.11–§4.12 — namespace `pg`, all 36 collector names, full metric+label catalog, flag table, deprecated options |
| postgres_exporter v0.16.0 | `.../archive/refs/tags/v0.16.0.tar.gz` README.md:310–321 | §4.11 — `--extend.query-path` deprecation wording |
| Grafana dashboard | `https://github.com/cloudnative-pg/grafana-dashboards/blob/main/charts/cluster/grafana-dashboard.json` | §6.3 |
| kubectl plugin | `.../main/docs/src/kubectl-plugin.md` | §6.1–§6.2 |
| PostgreSQL stats views | `https://www.postgresql.org/docs/{13..18}/monitoring-stats.html`, `.../using-explain.html`, `pg_stat_statements` docs | §7 |

### Corrections to the brief's assumptions

Six premises in the request do not hold against `v1.30.2` / `main`. Each is verified, with the
corrected fact inline in the relevant section:

| # | Assumption | Reality | Where |
|---|---|---|---|
| 1 | `cnpg-default-monitoring.toml` / `configs/monitoring` | Both paths **404** on `main` and on tag `v1.30.2`. The real artifact is **`config/manager/default-monitoring.yaml`** — YAML, not TOML. Deployed as ConfigMap `cnpg-default-monitoring`. | §4.10 |
| 2 | CRD field `spec.metrics.*` with `enabled` / `image` / `imagePullPolicy` / `containerConfig` / `service` / `serviceMonitor` / `customQueriesMetricPrefix` | **`MetricsConfiguration` does not exist** in v1.30.2 (verified by `awk '/^type MetricsConfiguration struct/,/^}/'` on main, release-1.30, release-1.23, release-1.20, release-1.15 — all empty). The field is **`spec.monitoring`** → `*MonitoringConfiguration` with 8 fields. The metrics exporter is **compiled into the instance manager binary**, not a sidecar, so there is no exporter image / pull policy / resources / probes. That field list describes a Crunchy PGO / Zalando-style `MetricsSpec`. | §4.2 |
| 3 | postgres_exporter `monitoring.conf` TOML format; `usage` values including `MASTER` / `REPLICA` / `BUILTIN` / `DYNAMIC` / `TABLESPACE` / `EXTEND` / `DESCRIBE` | postgres_exporter has **never used TOML** (grep of README at v0.16.0 for `toml`/`.conf` → zero matches). It uses **YAML only**: `postgres_exporter.yml` (`--config.file`) and `queries.yaml` (`--extend.query-path`, deprecated since v0.16). The `usage` enum has exactly **seven** values: `DISCARD`, `LABEL`, `COUNTER`, `GAUGE`, `MAPPEDMETRIC`, `DURATION`, `HISTOGRAM`. `master`/`primary` are **booleans on the query**, not usage values. The `MASTER`/`REPLICA`/`BUILTIN`/`DYNAMIC` set belongs to `burningalchemist/sql_exporter`. | §4.4 |
| 4 | Instance manager on port **9101**, with `/liveness`, `/readiness`, `/promote`, `/demote`, `/reload`, `/restart`, `/fence`, `/unfence`, `/replication-status`, `/pg-basebackup`, `/join-copy`, `/cleanup`, `/wait-primary` | **Port 9101 does not exist.** `pkg/management/url/url.go` (byte-identical from release-1.15 → main) defines exactly four ports: **8010, 9187, 9127, 8000**. None of the listed paths exist in any release from 1.15 → main. Real paths are `/healthz`, `/readyz`, `/startupz`, `/failsafe`, `/pg/status`, `/pg/controldata`, `/pg/mode/backup`, `/pg/archive/partial`, `/pg/backup`, `/cache/`, `/cluster/status/condition/wal/archive`, `/update`, `/metrics`. Promote/demote/fence/restart are **not HTTP calls** in 1.20+ — they are driven through the Cluster CR and reconciler; `instance join` and `instance pgbasebackup` are CLI subcommands. | §5 |
| 5 | `cnpg_wfile_*` / `pg_wal_stat` metrics | Superseded. v1.30 emits **`cnpg_collector_pg_wal{value="count"\|"size"\|"min"\|"max"\|"keep"\|"slots_max"\|"volume_max"\|"volume_size"}`**. | §5.1 |
| 6 | `pg_stat_statements` in CNPG's default monitoring set | **Absent.** CNPG's 14 default queries do not include `pg_stat_statements`. It comes from postgres_exporter's `stat_statements` collector (`pg_stat_statements_*`), which is **disabled by default** there and requires `CREATE EXTENSION pg_stat_statements`. This is the single biggest gap for a Query Insights product. | §4.10, §4.12 |

**Unverified items (explicit):**

1. The exact JSON field set of `postgres.PodStatus` returned by `GET /pg/status` — confirmed it is
   `ws.instance.GetStatus()` marshalled, but the struct was not enumerated on `main`.
2. The truncated tails of `cnpg_pgbouncer_lists_*` and `cnpg_pgbouncer_stats_*` metric lists (both
   source files were cut off mid-list during extraction).
3. Whether `cnpg_last_error` is emitted by Go code or is a docs-only legacy alias — it appears in
   `docs/src/monitoring.md`'s sample output but not in `pg_collector.go`'s `NewDesc` set.
4. The origin of the brief's `MetricsConfiguration` field list (read as Crunchy PGO `MetricsSpec`;
   not verified against that repo).
5. Whether `metrics.k8s.io` / `podmetrics` RBAC is required by any CNPG feature — the string
   `metrics.k8s.io` appears **nowhere** in the CNPG repo.
6. `docs/src/supported_releases.md` is internally inconsistent: the intro says "a new minor release
   every three months", the Support Policy section says "Approximately every two months", and the
   page contains a stray LLM artifact line ("Sure, here's an improved version of the naming scheme
   section:"). Both cadences are quoted in §1.0.
7. `main` is ahead of `v1.30.2` in places (`podSelectorRefs`, `pgDataImageInfo`,
   `targetPgDataImageInfo`, `failoverquorums`, `api/v1/failoverquorum_types.go`). Where a field
   exists only on `main`, it is marked **(main only)**.

---

## 1. Version landscape and CRD set

### 1.0 Versions

| Field | Value | Source |
|---|---|---|
| Latest stable | **v1.30.2** (2026-11-17) | GitHub releases API; `pkg/versions/versions.go` → `Version = "1.30.2"`, `buildVersion = "1.30.2"`, `buildCommit = "none"`, `buildDate = "unknown"` |
| 1.30.x released | 29 Jun 2026 | `docs/src/supported_releases.md` |
| 1.30.x EOL | ~ Mar 2027 | same |
| Currently supported | **Yes** (1.30.x is the only supported minor) | same |
| Supported Kubernetes | 1.34, 1.35, 1.36 (tested but unsupported: 1.37, 1.33, 1.32, 1.31) | same |
| Supported Postgres (1.30.x and `main`) | **15 – 18** | same |
| Default Postgres image | `ghcr.io/cloudnative-pg/postgresql:18.7-system-trixie` | `pkg/versions/versions.go` |
| Default operator image | `ghcr.io/cloudnative-pg/cloudnative-pg:1.30.2` | same |
| Default PgBouncer image | `ghcr.io/cloudnative-pg/pgbouncer:1.26.1` | same |
| Default exporter image | **none exists** — no `DefaultPostgresExporterImage` in any `versions.go` checked (main, release-1.30, release-1.23, release-1.20) | same |
| Prior branch heads | release-1.23 → `1.23.6` (`postgresql:17.2`); release-1.20 → `1.20.6` (`postgresql:16.1`) | `pkg/versions/versions.go` per branch |

Release policy (`docs/src/supported_releases.md`): new builds per commit; a minor release
**approximately every two months** (the intro paragraph says "every three months" — see §0.6);
each minor is supported until 3 months after the N+1 minor ships; **only the last patch release of
each branch is supported**; RC built 1–2 weeks before release. Release types: Development Build /
Release Candidate / Minor Release / Patch / Security Patch.

Upcoming: 1.31.0 ~Dec 2026 (EOL ~Jun 2027), 1.32.0 ~Mar 2027, 1.33.0 ~Jun 2027. **1.31 is the last
minor shipping native Barman Cloud support** — removed from 1.32.0; migrate to the Barman Cloud
Plugin first.

Postgres EOL note: *"Starting from November 12, 2026, Postgres 14 is no longer supported… Postgres 15
will stop receiving fixes on November 11, 2027."*

Old releases (release date / EOL / K8s): 1.29.x 31 Mar 2026 / 29 Sep 2026 / 1.33–1.35 · 1.28.x
9 Dec 2025 / 30 Jun 2026 / 1.32–1.35 · 1.27.x 12 Aug 2025 / 9 Mar 2026 / 1.31–1.33 · 1.26.x
23 May 2025 / 12 Nov 2025 / 1.30–1.33 · 1.25.x 23 Dec 2024 / 22 Aug 2025 · 1.24.x 22 Aug 2024 /
23 May 2025 · 1.23.x 24 Apr 2024 / 24 Nov 2024 · 1.22.x 21 Dec 2023 / 24 Jul 2024 · 1.21.x
12 Oct 2023 / 12 Jun 2024 · 1.20.x 27 Apr 2023 / 21 Jan 2024 · 1.19.x 14 Feb 2023 / 3 Nov 2023 ·
1.18.x 10 Nov 2022 / 12 Jun 2023 · 1.17.x 6 Sep 2022 / 20 Mar 2023 · 1.16.x 7 Jul 2022 /
21 Dec 2022 · 1.15.x 21 Apr 2022 / 6 Oct 2022.

### 1.1 The CRD set — all 11 kinds

API group/version (`api/v1/groupversion_info.go`): `postgresql.cnpg.io/v1`.
Kind constants declared there: `ClusterKind`, `BackupKind`, `PoolerKind`, `ImageCatalogKind`,
`ClusterImageCatalogKind`, `PublicationKind`, `SubscriptionKind`, `DatabaseKind` — **8 consts**.
`addKnownTypes` registers **11** types (adds `FailoverQuorum`, `DatabaseRole`, `ScheduledBackup`,
which have no `…Kind` const).

| File (`config/crd/bases/`) | kind | plural | singular | scope | served | storage | subresources |
|---|---|---|---|---|---|---|---|
| `postgresql.cnpg.io_clusters.yaml` | Cluster | `clusters` | `cluster` | Namespaced | True | True | **scale**, status |
| `postgresql.cnpg.io_backups.yaml` | Backup | `backups` | `backup` | Namespaced | True | True | status |
| `postgresql.cnpg.io_poolers.yaml` | Pooler | `poolers` | `pooler` | Namespaced | True | True | **scale**, status |
| `postgresql.cnpg.io_scheduledbackups.yaml` | ScheduledBackup | `scheduledbackups` | `scheduledbackup` | Namespaced | True | True | status |
| `postgresql.cnpg.io_databases.yaml` | Database | `databases` | `database` | Namespaced | True | True | status |
| `postgresql.cnpg.io_databaseroles.yaml` | DatabaseRole | `databaseroles` | `databaserole` | Namespaced | True | True | status |
| `postgresql.cnpg.io_publications.yaml` | Publication | `publications` | `publication` | Namespaced | True | True | status |
| `postgresql.cnpg.io_subscriptions.yaml` | Subscription | `subscriptions` | `subscription` | Namespaced | True | True | status |
| `postgresql.cnpg.io_failoverquorums.yaml` | FailoverQuorum | `failoverquorums` | `failoverquorum` | Namespaced | True | True | status |
| `postgresql.cnpg.io_imagecatalogs.yaml` | ImageCatalog | `imagecatalogs` | `imagecatalog` | Namespaced | True | True | (none) |
| `postgresql.cnpg.io_clusterimagecatalogs.yaml` | ClusterImageCatalog | `clusterimagecatalogs` | `clusterimagecatalog` | **Cluster** | True | True | (none) |

**No CRD in v1.30.2 defines `shortNames` or `categories`.** All 11 have `annotations: None`. Only
`ClusterImageCatalog` is cluster-scoped (`+genclient:nonNamespaced`). Only `Cluster` and `Pooler`
have a `scale` subresource (relevant to HPA; an insights app reads pods via `status.selector`).

### 1.2 One-line purposes

| Kind | Purpose |
|---|---|
| `Cluster` | A PostgreSQL cluster (HA set of instances) — the primary object |
| `Backup` | One-off physical base backup request (barman-object-store / volume-snapshot / plugin) |
| `ScheduledBackup` | Cron-scheduled series of `Backup` objects |
| `Pooler` | PgBouncer connection-pooler deployment fronting a `Cluster` |
| `Database` | Declarative PostgreSQL database (+ schemas, extensions, FDWs, servers) |
| `DatabaseRole` | Declarative PostgreSQL role (declarative role management) |
| `Publication` | Declarative logical-replication publication |
| `Subscription` | Declarative logical-replication subscription |
| `ImageCatalog` | Namespace-scoped map Postgres major → container image (+ extensions) |
| `ClusterImageCatalog` | Cluster-scoped equivalent |
| `FailoverQuorum` | Current failover-quorum state, written by the primary's instance manager and reset by the operator to trigger an update |

### 1.3 `Cluster.status` — exact JSON field names

`api/v1/cluster_types.go:902–1143` (v1.30.2). This is the richest single source of cluster state
available to a Kubernetes-native insights app — no metrics needed.

| JSON field | Go type | Notes |
|---|---|---|
| `instances` | `int` | Total number of instances |
| `readyInstances` | `int` | Number of ready instances |
| `selector` | `string` | Serialized label selector for the `scale` subresource |
| `instancesStatus` | `map[PodStatus][]string` | Pod names bucketed by `healthy` / `replicating` / `failed` |
| `instancesReportedState` | `map[PodName]InstanceReportedState` | Per-pod `{isPrimary, timeLineID, ip}` |
| `managedRolesStatus` | `ManagedRoles` | `{byStatus map[RoleStatus][]string, cannotReconcile map[string][]string, passwordStatus map[string]PasswordState}` |
| `tablespacesStatus` | `[]TablespaceState` | `{name, owner, state, error}` |
| `podSelectorRefs` | `[]PodSelectorRefStatus` (`listType=map`, `listMapKey=name`) | `{name, ips[]}` — **(main only)** |
| `timelineID` | `int` | Cluster timeline ID |
| `topology` | `Topology` | `{instances map[PodName]PodTopologyLabels, nodesUsed int32, successfullyExtracted bool}` |
| `latestGeneratedNode` | `int` | **Deprecated: this field is not set anymore** |
| `currentPrimary` | `string` | Pod name of the current primary |
| `targetPrimary` | `string` | Pod name of the primary being elected |
| `lastPromotionToken` | `string` | |
| `pvcCount` | `int32` | |
| `jobCount` | `int32` | |
| `danglingPVC` / `resizingPVC` / `initializingPVC` / `healthyPVC` / `unusablePVC` | `[]string` | Five separate PVC lists |
| `writeService` | `string` | Name of the rw service |
| `readService` | `string` | Name of the ro service |
| `phase` | `string` | 22 valid values, table below |
| `phaseReason` | `string` | |
| `secretsResourceVersion` | `SecretsResourceVersion` | see below |
| `configMapResourceVersion` | `ConfigMapResourceVersion` | `{metrics map[string]string}` |
| `certificates` | `CertificatesStatus` | `{CertificatesConfiguration (inline), expirations map[string]string}` |
| `firstRecoverabilityPoint` | `string` | **Deprecated** |
| `firstRecoverabilityPointByMethod` | `map[BackupMethod]metav1.Time` | **Deprecated** |
| `lastSuccessfulBackup` | `string` | **Deprecated** |
| `lastSuccessfulBackupByMethod` | `map[BackupMethod]metav1.Time` | **Deprecated** |
| `lastFailedBackup` | `string` | **Deprecated** |
| `cloudNativePGCommitHash` | `string` | |
| `currentPrimaryTimestamp` | `string` | |
| `currentPrimaryFailingSinceTimestamp` | `string` | |
| `targetPrimaryTimestamp` | `string` | |
| `poolerIntegrations` | `*PoolerIntegrations` | `{pgBouncerIntegration: {secrets: []string}}` |
| `cloudNativePGOperatorHash` | `string` | |
| `operatorCertificateFingerprint` | `string` | SHA-256 of the operator's in-memory client-cert public key, pinned by the instance manager |
| `availableArchitectures` | `[]AvailableArchitecture` | `{goArch, hash}` |
| `conditions` | `[]metav1.Condition` | |
| `instanceNames` | `[]string` | |
| `onlineUpdateEnabled` | `bool` | |
| `image` | `string` | Current container image |
| `pgDataImageInfo` | `*ImageInfo` | `{image, majorVersion, extensions[]}` — **(main only)** |
| `targetPgDataImageInfo` | `*ImageInfo` | In-progress major upgrade target — **(main only)** |
| `pluginStatus` | `[]PluginStatus` | `{name, version, capabilities[], operatorCapabilities[], walCapabilities[], …}` |
| `switchReplicaClusterStatus` | `SwitchReplicaClusterStatus` | `{inProgress bool}` |
| `demotionToken` | `string` | |
| `systemID` | `string` | PostgreSQL system identifier |

**Absent** (do not exist in v1.30.2): `pvcSizes`, `pvcTemplates`, `instancesReportedServices`,
`minSyncNodes`, `latestGeneratedSerial`, `proxy`, `serviceAccount`, `cloudNativePGStatus`,
`members`, `secretMetrics`, `secretServerCA`, `scheduledBackup`.

`SecretsResourceVersion` (`cluster_types.go:2808`) exact keys: `superuserSecretVersion`,
`replicationSecretVersion`, `applicationSecretVersion`, `managedRoleSecretVersion`
(`map[string]string`), `caSecretVersion` (Unused, retained), `clientCaSecretVersion`,
`serverCaSecretVersion`, `serverSecretVersion`, `barmanEndpointCA`,
`externalClusterSecretVersion` (`map[string]string`), `metrics` (`map[string]string` — "A map with
the versions of all the secrets used to pass metrics").

`InstanceReportedState` (`:1164`): `isPrimary bool`, `timeLineID int`, `ip string`.

### 1.4 `status.phase` — all 22 values

`cluster_types.go:704–773` (untyped string consts):

| Const | Value |
|---|---|
| `PhaseSwitchover` | `Switchover in progress` |
| `PhaseFailOver` | `Failing over` |
| `PhaseFirstPrimary` | `Setting up primary` |
| `PhaseCreatingReplica` | `Creating a new replica` |
| `PhaseUpgrade` | `Upgrading cluster` |
| `PhaseMajorUpgrade` | `Upgrading Postgres major version` |
| `PhaseUpgradeDelayed` | `Cluster upgrade delayed` |
| `PhaseWaitingForUser` | `Waiting for user action` |
| `PhaseInplacePrimaryRestart` | `Primary instance is being restarted in-place` |
| `PhaseInplaceDeletePrimaryRestart` | `Primary instance is being restarted without a switchover` |
| `PhaseHealthy` | `Cluster in healthy state` |
| `PhaseUnknownPlugin` | `Cluster cannot proceed to reconciliation due to an unknown plugin being required` |
| `PhaseFailurePlugin` | `Cluster cannot proceed to reconciliation due to an error while interacting with plugins` |
| `PhaseImageCatalogError` | `Cluster has incomplete or invalid image catalog` |
| `PhaseUnrecoverable` | `Cluster is unrecoverable and needs manual intervention` |
| `PhaseArchitectureBinaryMissing` | `Cluster cannot execute instance online upgrade due to missing architecture binary` |
| `PhaseWaitingForInstancesToBeActive` | `Waiting for the instances to become active` |
| `PhaseOnlineUpgrading` | `Online upgrade in progress` |
| `PhaseApplyingConfiguration` | `Applying configuration` |
| `PhaseReplicaClusterPromotion` | `Promoting to primary cluster` |
| `PhaseCannotCreateClusterObjects` | `Unable to create required cluster objects` |
| `PhaseDefinitionInvalid` | `Invalid cluster definition` |

### 1.5 Conditions and reasons

`ClusterConditionType` consts (`cluster_types.go:1174–1193`): `ContinuousArchiving`,
`LastBackupSucceeded` (`ConditionBackup`), `Ready` (`ConditionClusterReady`), `ConsistentSystemID`,
`Initialized`. `SyncReplicationTopologySatisfied` appears on **main only**.
`ConditionStatus`: `True` / `False` / `Unknown`.
`ConditionReason` constants: `BackupStarted`, `LastBackupSucceeded`, `LastFailedBackup`,
`ContinuousArchivingSuccess`, `ContinuousArchivingFailing`, `ClusterIsReady`, `ClusterIsNotReady`,
`DetachedVolume`, `BootstrapCompleted`, `BootstrapPending`, `Satisfied`, `TopologyNotExtracted`,
`InsufficientCrossDomainReplicas`.

### 1.6 Other CRD statuses (exact JSON keys)

**`Backup.status`** (`backup_types.go:198–315`) — inlines `barmanApi.BarmanCredentials`, then:
`majorVersion` (int), `endpointCA` (SecretKeySelector), `endpointURL`, `destinationPath`,
`serverName`, `encryption`, `backupId`, `backupName`, `phase` (`BackupPhase`), `startedAt`,
`stoppedAt`, **`reconciliationStartedAt`**, **`reconciliationTerminatedAt`** (added v1.29.0 for
long-running-backup observability), `beginWal`, `endWal`, `beginLSN`, `endLSN`, `error`,
`commandOutput` (Unused), `commandError`, `backupLabelFile` ([]byte), `tablespaceMapFile` ([]byte),
`instanceID` (`{podName, ContainerID, sessionID}`), `snapshotBackupStatus`
(`{elements: [{name, type, tablespaceName}]}`), `method` (`BackupMethod`), `online` (*bool),
`pluginMetadata` (map[string]string).
`BackupPhase` values: `pending`, `started`, `running`, `finalizing`, `completed`, `failed`,
`walArchivingFailing`, `invalid backup definition`.
`BackupMethod` values: `volumeSnapshot`, `barmanObjectStore`, `plugin`.

**`ScheduledBackup.status`** (`scheduledbackup_types.go:88–104`): `lastCheckTime`,
`lastScheduleTime`, `nextScheduleTime`, `error`.

**`Pooler.status`** (`pooler_types.go`): `secrets` (`PoolerSecrets`), `instances` (int32), `phase`
(`PoolerPhase`), `phaseReason`, `image`, `error`. `PoolerSecrets`: `clientTLS`, `serverTLS`,
`serverCA`, `clientCA` (each `SecretVersion{name, version}`), `pgBouncerSecrets`
(`{authQuery: SecretVersion}`). `PoolerPhase`: `active`, `paused`, `inactive`, `failed`.
`PoolerType`: `rw`, `ro`, `r`.

**`Database.status`** (`database_types.go:328–371`): `observedGeneration`, `applied` (*bool),
`message`, `schemas[]`, `extensions[]`, `fdws[]`, `servers[]` (each
`DatabaseObjectStatus{name, applied, message}`). `DatabaseReclaimPolicy`: `delete`, `retain`.
`UsageSpecType`: `grant`, `revoke`.

**`DatabaseRole.status`** (`databaserole_types.go:112–139`): `observedGeneration`, `applied`,
`message`, `secretResourceVersion`, `clientCertificate` (`{expiration, message}`), `conditions[]`.
`DatabaseRoleReclaimPolicy`: `delete`, `retain`. `DatabaseRoleConditionType`:
`PasswordSecretChange`. Reserved role names (webhook): `postgres`, `streaming_replica`, any
`pg_`/`cnpg_` prefix.

**`Publication.status`** / **`Subscription.status`**: `observedGeneration`, `applied`, `message`.

**`FailoverQuorum.status`** (`failoverquorum_types.go`): `method` (string), `standbyNames`
([]string), `standbyNumber` (int), `primary` (string). **FailoverQuorum has no `spec`** — only
`metadata` + `status`.

**`ImageCatalogSpec` / `ClusterImageCatalogSpec`** (shared): `images` ([]CatalogImage
`{image, major, extensions[]}`, min 1 / max 8, unique majors), `componentImages`
([]CatalogComponentImage `{key, image}`, listType=map). Neither has a status.

### 1.7 `additionalPrinterColumns` (all 11)

| Kind | Columns (name → jsonPath) |
|---|---|
| Cluster | Age→`.metadata.creationTimestamp`; Instances→`.status.instances`; Ready→`.status.readyInstances`; Status→`.status.phase`; Primary→`.status.currentPrimary` |
| Backup | Age; Cluster→`.spec.cluster.name`; Method→`.spec.method`; Phase→`.status.phase`; Error→`.status.error` |
| Pooler | Age; Cluster→`.spec.cluster.name`; Type→`.spec.type`; Phase→`.status.phase` |
| ScheduledBackup | Age; Cluster→`.spec.cluster.name`; Last Backup→`.status.lastScheduleTime` |
| Database / DatabaseRole / Publication / Subscription | Age; Cluster→`.spec.cluster.name`; PG Name→`.spec.name`; Applied→`.status.applied`; Message→`.status.message` |
| ClusterImageCatalog / ImageCatalog / FailoverQuorum | Age only |

### 1.8 Observability-related changes in recent releases

| Release | Date | Observability-relevant change |
|---|---|---|
| **v1.30.2** | 2026-11-17 | Fixed enabling a sidecar plugin on a running cluster — pods created before that had no sidecar, logged `Unknown plugin` errors and **their metrics scrapes failed**. **Fixed the WAL count and size metrics counting `.partial`, `.backup` and `.history` files** (#9967). Fixed the instance manager waiting forever on a frozen postmaster — this blocked the instance status, **the metrics exporter**, and the pre-backup check. |
| **v1.30.1 / v1.29.3** | 2026-09-23 | **PgBouncer metrics** silently reported zero when PgBouncer adds or reorders columns: `SHOW STATS` and `SHOW POOLS` results now matched by **column name** instead of position (@AdzerKI). Handled a **`NULL` `stats_reset` in the `pg_stat_wal` metric**, which could break Prometheus scraping on **PostgreSQL 18** clusters (#11207, @HoniSanders). |
| **v1.30.0 / -rc1** | 2026-06-29 / 06-19 | **Added TLS support for the `Pooler` metrics endpoint via `.spec.monitoring.tls.enabled`** — metrics served over HTTPS reusing `.spec.pgbouncer.clientTLSSecret`. Fixed a bootstrap failure where a **metrics-exporter setup error** (duplicate-key race with the controller) rolled back `streaming_replica` creation and wedged replica joins. |
| **v1.29.1 / v1.28.3** | 2026-05-08 | **`CVE-2026-44477` / `GHSA-423p-g724-fr39`: metrics exporter privilege escalation.** The exporter **no longer authenticates as the `postgres` superuser**; it now uses a dedicated **`cnpg_metrics_exporter`** role with `pg_monitor`. Upgrade impact: custom monitoring queries reading user-owned tables, or using `target_databases: '*'` against databases where `PUBLIC CONNECT` was revoked, need explicit `GRANT`s to `cnpg_metrics_exporter`. For replica clusters, upgrade the source primary first — the role is created there and replicates downstream. **Schema-qualified catalog references in default monitoring queries** (every `pg_catalog` object now explicitly qualified). Switched TLS peer verification from `VerifyPeerCertificate` to `VerifyConnection`. |
| **v1.29.0** | 2026-04-01 | Improved operator observability by **emitting native Kubernetes events** during key reconciliation phases. Added **`reconciliationStartedAt` / `reconciliationTerminatedAt`** to `Backup.status`. |

---

## 2. Discovery metadata

Everything a namespace-scoped app can use to find and classify CNPG objects.

### 2.1 Label keys

Canonical file: **`pkg/utils/labels_annotations.go`**.
Namespaces: `MetadataNamespace = "cnpg.io"`, `AlphaMetadataNamespace = "alpha.cnpg.io"`,
`KubernetesAppNamespaceDomain = "app.kubernetes.io"`.
Name constants: `ManagerName = "cloudnative-pg"`, `AppName = "postgresql"`,
`DatabaseComponentName = "database"`, `PoolerComponentName = "pooler"`.

| Go const | Literal label key |
|---|---|
| `KubernetesAppManagedByLabelName` | `app.kubernetes.io/managed-by` (value `cloudnative-pg`) |
| `KubernetesAppLabelName` | `app.kubernetes.io/name` (value `postgresql`; `cloudnative-pg` for the operator) |
| `KubernetesAppInstanceLabelName` | `app.kubernetes.io/instance` (cluster name) |
| `KubernetesAppVersionLabelName` | `app.kubernetes.io/version` (PG major version) |
| `KubernetesAppComponentLabelName` | `app.kubernetes.io/component` (`database` / `pooler`) |
| `ClusterLabelName` | **`cnpg.io/cluster`** |
| `ClusterInstanceRoleLabelName` | **`cnpg.io/instanceRole`** |
| `ClusterRoleLabelName` | **`role`** — **Deprecated: use `cnpg.io/instanceRole`**; `SetInstanceRole()` sets **both** |
| `PodRoleLabelName` | **`cnpg.io/podRole`** |
| `InstanceNameLabelName` | **`cnpg.io/instanceName`** |
| `PvcRoleLabelName` | **`cnpg.io/pvcRole`** |
| `TablespaceNameLabelName` | `cnpg.io/tablespaceName` |
| `JobRoleLabelName` | `cnpg.io/jobRole` |
| `BackupNameLabelName` | `cnpg.io/backupName` |
| `MajorVersionLabelName` | `cnpg.io/majorVersion` |
| `PgbouncerNameLabel` | `cnpg.io/poolerName` |
| `ImmediateBackupLabelName` | `cnpg.io/immediateBackup` |
| `ParentScheduledBackupLabelName` | `cnpg.io/scheduled-backup` |
| `WatchedLabelName` | **`cnpg.io/reload`** — set on a ConfigMap/Secret to have the operator auto-reload it |
| `UserTypeLabelName` | `cnpg.io/userType` |
| `BackupTimelineLabelName` | `cnpg.io/backupTimeline` |
| `BackupYearLabelName` / `BackupMonthLabelName` / `BackupDateLabelName` | `cnpg.io/backupYear` / `-backupMonth` / `-backupDate` |
| `IsOnlineBackupLabelName` | `cnpg.io/onlineBackup` |
| `IsManagedLabelName` | `cnpg.io/isManaged` |
| `PluginNameLabelName` | `cnpg.io/pluginName` |

**Label value types:**

```go
type PodRole string   // PodRoleInstance = "instance";  PodRolePooler = "pooler"
type PVCRole string   // PVCRolePgData = "PG_DATA";  PVCRolePgWal = "PG_WAL";  PVCRolePgTablespace = "PG_TABLESPACE"
type UserType string  // UserTypeSuperuser = "superuser";  UserTypeApp = "app"
type PodStatus string // PodHealthy = "healthy"; PodReplicating = "replicating"; PodFailed = "failed"
type RoleStatus string // "reconciled" | "not-managed" | "pending-reconciliation"
```
`pkg/specs/pods.go`: `ClusterRoleLabelPrimary = "primary"`, `ClusterRoleLabelReplica = "replica"`,
`ClusterRoleLabelUnhealthy = "unhealthy"` — the values written to `role` / `cnpg.io/instanceRole`.

**Verified absent in v1.30.2** (repo-wide grep): `cnpg.io/pvcName`, `cnpg.io/clusterImageName`,
`cnpg.io/pvcStorageClass`, `cnpg.io/pvcStorage`, `cnpg.io/offline`, `cnpg.io/switchoverTarget`,
`topologies.kubernetes.io/*`, `cnpg.io/metrics`, **`cnpg.io/nodeName`**, `cnpg.io/jobName`,
`cnpg.io/replicaCluster`, and the `postgresql.cnpg.io/*` **label** prefix (that domain is the API
group only; all labels use `cnpg.io/`, `app.kubernetes.io/`, or bare `role`). The string
`cnpg-rbac` appears nowhere.

Note: the brief's `cnpg.io/nodeName` does not exist — node placement is read from
`status.topology.instances` (`map[PodName]PodTopologyLabels`) or from `Pod.spec.nodeName`.

### 2.2 Annotation keys

| Go const | Literal annotation key |
|---|---|
| `OperatorVersionAnnotationName` | `cnpg.io/operatorVersion` |
| `ReconciliationLoopAnnotationName` | `cnpg.io/reconciliationLoop` |
| `ReconcilePodSpecAnnotationName` | `cnpg.io/reconcilePodSpec` |
| `HibernateClusterManifestAnnotationName` | `cnpg.io/hibernateClusterManifest` (Deprecated) |
| `HibernatePgControlDataAnnotationName` | `cnpg.io/hibernatePgControlData` (Deprecated) |
| `PodEnvHashAnnotationName` | `cnpg.io/podEnvHash` (Deprecated) |
| `PodSpecAnnotationName` | `cnpg.io/podSpec` |
| `ClusterManifestAnnotationName` | `cnpg.io/clusterManifest` |
| `CoredumpFilter` | `cnpg.io/coredumpFilter` |
| `PgControldataAnnotationName` | `cnpg.io/pgControldata` |
| `SkipWalArchiving` | `cnpg.io/skipWalArchiving` |
| `skipEmptyWalArchiveCheck` (unexported) | `cnpg.io/skipEmptyWalArchiveCheck` |
| `ClusterSerialAnnotationName` | `cnpg.io/nodeSerial` |
| `ClusterReloadAnnotationName` | `cnpg.io/reloadedAt` |
| `PVCStatusAnnotationName` | `cnpg.io/pvcStatus` (values `initializing` / `ready` / `detached`) |
| `LegacyBackupAnnotationName` | `cnpg.io/forceLegacyBackup` |
| `HibernationAnnotationName` | `cnpg.io/hibernation` (values `on` / `off`) |
| `PoolerSpecHashAnnotationName` | `cnpg.io/poolerSpecHash` |
| `OperatorManagedSecretsAnnotationName` | `cnpg.io/managedSecrets` |
| `FencedInstanceAnnotation` | **`cnpg.io/fencedInstances`** (JSON list of fenced pod names — fencing state is an annotation, not a status field) |
| `CNPGHashAnnotationName` | `cnpg.io/hash` |
| `BackupStartWALAnnotationName` / `BackupEndWALAnnotationName` | `cnpg.io/backupStartWAL` / `cnpg.io/backupEndWAL` |
| `BackupStartTimeAnnotationName` / `BackupEndTimeAnnotationName` | `cnpg.io/backupStartTime` / `cnpg.io/backupEndTime` |
| `BackupLabelFileAnnotationName` / `BackupTablespaceMapFileAnnotationName` | `cnpg.io/backupLabelFile` / `cnpg.io/backupTablespaceMapFile` |
| `BackupVolumeSnapshotDeadlineAnnotationName` | `cnpg.io/volumeSnapshotDeadline` |
| `SnapshotStartTimeAnnotationName` / `SnapshotEndTimeAnnotationName` | `cnpg.io/snapshotStartTime` / `cnpg.io/snapshotEndTime` |
| `ClusterRestartAnnotationName` | `kubectl.kubernetes.io/restartedAt` |
| `UpdateStrategyAnnotation` | `cnpg.io/updateStrategy` (values `patch` / `replace`) |
| `LastAppliedSpecAnnotationName` | `cnpg.io/lastAppliedSpec` |
| `PluginClientSecretAnnotationName` / `PluginServerSecretAnnotationName` / `PluginPortAnnotationName` / `PluginServerNameAnnotationName` | `cnpg.io/pluginClientSecret` / `-pluginServerSecret` / `-pluginPort` / `-pluginServerName` |
| `PodPatchAnnotationName` | `cnpg.io/podPatch` |
| `WebhookValidationAnnotationName` | `cnpg.io/validation` |
| `PasswordPassthroughAnnotationName` | `cnpg.io/passwordPassthrough` |
| `AppArmorAnnotationPrefix` | `container.apparmor.security.beta.kubernetes.io` |
| `LivenessPingerAnnotationName` | `alpha.cnpg.io/livenessPinger` |
| `FailoverQuorumAnnotationName` | `alpha.cnpg.io/failoverQuorum` |
| `EnableInstancePprofAnnotationName` | `alpha.cnpg.io/enableInstancePprof` (value `"true"` → adds `--pprof-server`, port 6060) |
| `UnrecoverableInstanceAnnotationName` | `alpha.cnpg.io/unrecoverable` |

### 2.3 Per-object label sets (exact)

| Object | Builder | Labels |
|---|---|---|
| **Instance Pod** | `pkg/reconciler/instance/metadata.go:221–245` + `pkg/specs/pods.go` | `cnpg.io/cluster`, `cnpg.io/instanceName`, `cnpg.io/podRole=instance`, `role` + `cnpg.io/instanceRole` (`primary`/`replica`/`unhealthy`), `app.kubernetes.io/{name,instance,version,component,managed-by}`, `cnpg.io/majorVersion`. Annotations: `cnpg.io/{podSpec, clusterManifest, operatorVersion, nodeSerial, pvcStatus, reloadedAt}` |
| **PVC** | `pkg/reconciler/persistentvolumeclaim/calculator.go:123–131` | `cnpg.io/instanceName`, `cnpg.io/pvcRole` (`PG_DATA`/`PG_WAL`/`PG_TABLESPACE`), `app.kubernetes.io/{managed-by,name,component}`; tablespace PVCs add `cnpg.io/tablespaceName`; annotation `cnpg.io/nodeSerial`; `role`/`cnpg.io/instanceRole` also set via `utils.SetInstanceRole` |
| **Job + job pod template** | `pkg/specs/jobs.go:359–382` | `cnpg.io/instanceName`, `cnpg.io/cluster`, `cnpg.io/jobRole`, `app.kubernetes.io/{name,instance,version,component,managed-by}` |
| **Pooler Deployment / Service / pods** | `pkg/specs/pgbouncer/{deployments,services}.go` | `cnpg.io/poolerName`, `cnpg.io/cluster`, `cnpg.io/podRole=pooler`, `app.kubernetes.io/name=postgresql`, `app.kubernetes.io/instance=<cluster>`, `app.kubernetes.io/component=pooler`, `app.kubernetes.io/managed-by=cloudnative-pg`; annotation `cnpg.io/poolerSpecHash` |

### 2.4 Services (all `ServiceTypeClusterIP`, port `postgres`/TCP/5432 → targetPort 5432)

| Builder (`pkg/specs/services.go`) | Name | `PublishNotReadyAddresses` | Selector |
|---|---|---|---|
| `CreateClusterAnyService` | **`<cluster>-any`** | **true** | `cnpg.io/cluster`, `cnpg.io/podRole=instance` |
| `CreateClusterReadService` | **`<cluster>-r`** | false | `cnpg.io/cluster`, `cnpg.io/podRole=instance` |
| `CreateClusterReadOnlyService` | **`<cluster>-ro`** | false | `cnpg.io/cluster`, `cnpg.io/instanceRole=replica` |
| `CreateClusterReadWriteService` | **`<cluster>-rw`** | false | `cnpg.io/cluster`, `cnpg.io/instanceRole=primary` |
| `BuildManagedServices` (from `spec.managed.services.additional`) | user-named | — | + `cnpg.io/isManaged="true"`, `cnpg.io/updateStrategy` |

All four carry the identical label set: `cnpg.io/cluster`, `app.kubernetes.io/name=postgresql`,
`app.kubernetes.io/instance=<cluster>`, `app.kubernetes.io/version=<major>`,
`app.kubernetes.io/component=database`, `app.kubernetes.io/managed-by=cloudnative-pg`.

**There is NO `<cluster>-metrics` Service and NO `<cluster>-instances` Service in v1.30.2.** The
"all instances" service is `<cluster>-any`. Metrics are exposed **per-pod** (port 9187) and scraped
via PodMonitor, not via a Service. Pooler Service: name = `pooler.Name`, ClusterIP, port
`{name: "pgbouncer", port: 5432, targetPort: "pgbouncer", protocol: TCP}`.

### 2.5 Container ports (exact)

| Port | Constant | File |
|---|---|---|
| `5432` | `postgres.ServerPort` | `pkg/postgres/configuration.go:245` |
| **`9187`** | `url.PostgresMetricsPort` (int32, HTTP or HTTPS) | `pkg/management/url/url.go:34` |
| **`9127`** | `url.PgBouncerMetricsPort` (int32) | `pkg/management/url/url.go:37` |
| **`8000`** | `url.StatusPort` (int32, HTTPS) | `pkg/management/url/url.go` |
| **`8010`** | `url.LocalPort` (int32, HTTP, localhost-only) | `pkg/management/url/url.go` |
| `6060` | hardcoded literal, name `pprof` | `pkg/specs/pods.go` (opt-in) |
| `5432` | `pgbouncer/config.PgBouncerPort`, name `PgBouncerPortName = "pgbouncer"` | `pkg/management/pgbouncer/config/config.go:78,80` |
| `8080` | operator metrics, name `metrics` | `releases/cnpg-1.30.2.yaml` Deployment |
| `9443` | operator `webhook-server` | same + `cnpg-webhook-service` (443→9443) |

Pod container names (`pkg/specs/pods.go`): `postgres` (5432), `metrics` (9187, `--metrics-port-tls`
when TLS), init container `bootstrap-controller`. `PgDataPath = /var/lib/postgresql/data/pgdata`,
`PgWalPath = <pgdata>/pg_wal`, `PgWalArchiveStatusPath = <pg_wal>/archive_status`. All probes
period 10s. `DefaultPostgresUID = 26`, `DefaultPostgresGID = 26`.

### 2.6 PodMonitor objects CNPG generates

**Cluster** (`pkg/specs/podmonitor.go`): name = `cluster.Name`; labels = the 5
`app.kubernetes.io/*`; endpoint `port: "metrics"`; selector
`{cnpg.io/cluster: <name>, cnpg.io/podRole: instance}`; TLS → `scheme: https`,
`tlsConfig.ca.secret = {name: <cluster>-ca, key: ca.crt}`, `serverName: <cluster>-rw`,
`insecureSkipVerify: true` (in-code comment: *"needs to be set to match the ssl_mode=verify-ca used
by postgres"*). `.spec.monitoring.enablePodMonitor` is **deprecated** — docs recommend creating
your own with `selector.matchLabels: {cnpg.io/cluster: <name>}` + `podMetricsEndpoints: [{port: metrics}]`.

**Pooler** (`pkg/specs/pgbouncer/podmonitor.go`): name = `pooler.Name`; labels
`{cnpg.io/poolerName, cnpg.io/podRole: pooler, app.kubernetes.io/name: postgresql,
app.kubernetes.io/instance: <pooler>, app.kubernetes.io/component: pooler,
app.kubernetes.io/managed-by: cloudnative-pg}`; selector `{cnpg.io/poolerName: <pooler>,
cnpg.io/podRole: pooler}`; endpoint `port: "metrics"`; TLS → https + CA from
`pooler.GetClientCASecretNameOrDefault(cluster)` key `ca.crt`, `serverName: <pooler>`,
`insecureSkipVerify: true`. Enabled only if `spec.pgbouncer.monitoring.enablePodMonitor`.

**Operator** (sample `docs/src/samples/monitoring/podmonitor.yaml`): `name: cnpg-controller-manager`,
`selector.matchLabels: {app.kubernetes.io/name: cloudnative-pg}`, `podMetricsEndpoints: [{port: metrics}]`.

### 2.7 Secrets (exact names + keys)

| Secret | Source | Type | Keys |
|---|---|---|---|
| `<cluster>-ca` | `GetServerCASecretName()` / `GetClientCASecretName()` (both `DefaultServerCaSecretSuffix = ClientCaSecretSuffix = "-ca"`) | `kubernetes.io/tls` | `ca.crt` (`certs.CACertKey`), `ca.key` |
| `<cluster>-server` | `GetServerTLSSecretName()` (`ServerSecretSuffix = "-server"`) | `kubernetes.io/tls` | `tls.crt` (`certs.TLSCertKey`), `tls.key` (`certs.TLSPrivateKeyKey`) — **this is the cert served on :8000 and on :9187 when TLS** |
| `<cluster>-replication` | `GetReplicationSecretName()` (`ReplicationSecretSuffix`, user `streaming_replica`) | `kubernetes.io/tls` | `tls.crt`, `tls.key` |
| `<cluster>-superuser` | `GetSuperuserSecretName()` (`SuperUserSecretSuffix`) | `kubernetes.io/basic-auth` | see below |
| `<cluster>-app` | `GetApplicationSecretName()` (`ApplicationUserSecretSuffix`) | `kubernetes.io/basic-auth` | see below |
| `<cluster>-pooler` | `Pooler.GetAuthQuerySecretName()` = `<cluster>` + `DefaultPgBouncerPoolerSecretSuffix = "-pooler"` | basic-auth or TLS | `username`, `password` (TLS form: `tls.crt`, `tls.key`) |
| `<cluster>-pull-secret` | `ClusterSecretSuffix` | image pull secret | — |
| `<databaserole>-client-cert` | `clientCertSecretSuffix` | `kubernetes.io/tls` | `tls.crt`, `tls.key` |

`pkg/specs/secrets.go` `CreateSecret(...)` — type `kubernetes.io/tls` → `kubernetes.io/basic-auth`
(`SecretTypeBasicAuth`), labels `{cnpg.io/userType: <superuser|app>, cnpg.io/reload: "true",
app.kubernetes.io/managed-by: cloudnative-pg}`, StringData keys: **`username`, `user`, `password`,
`dbname`, `host`, `port`, `pgpass`, `uri`, `jdbc-uri`, `fqdn-uri`, `fqdn-jdbc-uri`**.

**There is no `<cluster>-metrics` secret in v1.30.2.** `status.secretsResourceVersion.metrics` is a
`map[string]string` of the custom-queries secrets' resourceVersions. Metrics auth is **peer auth on
the pod-local Unix socket**, not a client cert (see §4.7).

`CertificatesConfiguration` (`api/v1/cluster_types.go`, main):
```go
type CertificatesConfiguration struct {
    ServerCASecret       string   `json:"serverCASecret,omitempty"`       // default <cluster>-server-ca
    ServerTLSSecret      string   `json:"serverTLSSecret,omitempty"`      // default <cluster>-server  ← served on :8000
    ReplicationTLSSecret string   `json:"replicationTLSSecret,omitempty"` // default <cluster>-replication
    ClientCASecret       string   `json:"clientCASecret,omitempty"`       // default <cluster>-client-ca
    ServerAltDNSNames    []string `json:"serverAltDNSNames,omitempty"`
}
type CertificatesStatus struct {
    CertificatesConfiguration `json:",inline"`
    Expirations map[string]string `json:"expirations,omitempty"`
}
```
`CertificatesStatus.expirations` is a `map[secretName]RFC3339` — a directly usable cert-expiry
surface for an insights product. **No `metricsSecret` field exists** in any release checked.

### 2.8 RBAC

#### 2.8.1 Operator's ClusterRole — `config/rbac/role.yaml` (name `manager`; released as `cnpg-manager`)

| apiGroups | resources | verbs |
|---|---|---|
| `""` | configmaps, secrets, services | create, delete, get, list, patch, update, watch |
| `""` | configmaps/status, secrets/status | get, patch, update |
| `""` | events | create, patch |
| `""` | **nodes** | get, list, watch |
| `""` | persistentvolumeclaims, pods, **pods/exec** | create, delete, get, list, patch, watch |
| `""` | pods/status | get |
| `""` | serviceaccounts | create, get, list, patch, update, watch |
| `admissionregistration.k8s.io` | mutatingwebhookconfigurations, validatingwebhookconfigurations | get, patch |
| `apps` | deployments | create, delete, get, list, patch, update, watch |
| `batch` | jobs | create, delete, get, list, patch, watch |
| `coordination.k8s.io` | leases | create, delete, get, list, update, watch |
| `discovery.k8s.io` | endpointslices | get, list, watch |
| `monitoring.coreos.com` | podmonitors | create, delete, get, list, patch, watch |
| `policy` | poddisruptionbudgets | create, delete, get, list, patch, update, watch |
| `postgresql.cnpg.io` | backups, clusters, databaseroles, databases, poolers, publications, scheduledbackups, subscriptions | create, delete, get, list, patch, update, watch |
| `postgresql.cnpg.io` | backups/status, databases/status, publications/status, scheduledbackups/status, subscriptions/status | get, patch, update |
| `postgresql.cnpg.io` | clusterimagecatalogs, imagecatalogs | get, list, watch |
| `postgresql.cnpg.io` | clusters/finalizers, databaseroles/finalizers, poolers/finalizers | update |
| `postgresql.cnpg.io` | clusters/status, databaseroles/status, failoverquorums/status, poolers/status | get, patch, update, watch |
| `postgresql.cnpg.io` | failoverquorums | create, delete, get, list, watch |
| `rbac.authorization.k8s.io` | rolebindings, roles | create, get, list, patch, update, watch |
| `snapshot.storage.k8s.io` | volumesnapshots | create, get, list, patch, watch |

`config/rbac/role_binding.yaml`: `ClusterRoleBinding/manager-rolebinding` → `ClusterRole/manager` ←
`ServiceAccount/cnpg-manager` in `cnpg-system`.
Released names (`releases/cnpg-1.30.2.yaml`): Namespace `cnpg-system`; SA `cnpg-manager`;
ClusterRoles `cnpg-manager`, `cnpg-database-editor-role`, `cnpg-database-viewer-role`,
`cnpg-publication-editor-role`, `cnpg-publication-viewer-role`, `cnpg-subscription-editor-role`,
`cnpg-subscription-viewer-role`; ClusterRoleBinding `cnpg-manager-rolebinding`;
`cnpg-mutating-webhook-configuration`; `cnpg-validating-webhook-configuration`; Service
`cnpg-webhook-service` (443→9443); Deployment `cnpg-controller-manager`
(`ghcr.io/cloudnative-pg/cloudnative-pg:1.30.2`, args `controller --leader-elect
--max-concurrent-reconciles=10 --config-map-name=cnpg-controller-manager-config
--secret-name=cnpg-controller-manager-config --webhook-port=9443`).

`docs/src/security.md`: *"The operator currently requires `ClusterRole` permissions to read `nodes`
and `ClusterImageCatalog` objects. All other permissions can be namespace-scoped (i.e., `Role`)."*
Write access to `DatabaseRole` == superuser in that namespace; write access to `Pooler` (arbitrary
`authQuery` SQL) needs the same care as `Cluster` write.

**`metrics.k8s.io` and `authentication.k8s.io` appear NOWHERE in the repo** — no `podmetrics` RBAC
exists in CNPG v1.30.2.

#### 2.8.2 Per-cluster namespaced Role for the instance manager (`pkg/specs/roles.go`)

Name = cluster name, labels `app.kubernetes.io/managed-by: cloudnative-pg`:

| apiGroups | resources | verbs | resourceNames |
|---|---|---|---|
| `""` | configmaps | get, watch | `getInvolvedConfigMapNames` |
| `""` | secrets | get, watch | `getInvolvedSecretNames` |
| `postgresql.cnpg.io` | clusters | get, list, watch | `[cluster]` |
| `postgresql.cnpg.io` | clusters/status | get, patch, update, watch | `[cluster]` |
| `postgresql.cnpg.io` | backups | list, get, delete | — |
| `postgresql.cnpg.io` | backups/status | get, patch, update | — |
| `""` | events | create, patch | — |
| `postgresql.cnpg.io` | databases (+/status) | get, update, list, watch (+ get,patch,update) | — |
| `postgresql.cnpg.io` | publications (+/status) | get, update, list, watch (+ get,patch,update) | — |
| `postgresql.cnpg.io` | subscriptions (+/status) | get, update, list, watch (+ get,patch,update) | — |
| `coordination.k8s.io` | leases | get, update | `[cluster]` |
| `postgresql.cnpg.io` | failoverquorums (+/status) | get, list, watch (+ get,patch,update,watch) | `[cluster]` |
| `postgresql.cnpg.io` | databaseroles (+/status) | get, update, list, watch (+ get,patch,update) | — |

`getInvolvedSecretNames(opts RoleOptions)` exact list:
```go
[]string{
    opts.Cluster.GetReplicationSecretName(), opts.Cluster.GetClientCASecretName(),
    opts.Cluster.GetServerCASecretName(),   opts.Cluster.GetServerTLSSecretName(),
    opts.Cluster.GetApplicationSecretName(),opts.Cluster.GetSuperuserSecretName(),
    opts.Cluster.GetLDAPSecretName(),
}
// + spec.monitoring.customQueriesSecret[].name
// + backupSecrets(cluster, backupOrigin) + externalClusterSecrets(cluster)
// + managedRolesSecrets(cluster)       + customResourceRolesSecrets(roles)
```

#### 2.8.3 Per-pooler namespaced Role (`pkg/specs/pgbouncer/rbac.go`)

SA name = `pooler.Name`; Role name = `pooler.Name`:

| apiGroups | resources | verbs | resourceNames |
|---|---|---|---|
| `postgresql.cnpg.io` | poolers | get, watch | `[pooler]` |
| `postgresql.cnpg.io` | poolers/status | get, patch, update, watch | `[pooler]` |
| `""` | secrets | get, watch | `[pooler.GetAuthQuerySecretName(), status.secrets.serverCA.name, status.secrets.serverTLS.name, status.secrets.clientCA.name, status.secrets.clientTLS.name]` |

#### 2.8.4 Scaffolded viewer/editor ClusterRoles (`config/rbac/`)

Shape: `<kind>-viewer-role` = `[get, list, watch]` on the resource + `[get]` on `<resource>/status`;
`<kind>-editor-role` = `[create, delete, get, list, patch, update, watch]` + `[get]` on status.

| ClusterRole | In `config/rbac/kustomization.yaml`? |
|---|---|
| `cluster-viewer-role` / `cluster-editor-role` | **No** (only in `releases/`) |
| `backup-viewer-role` / `backup-editor-role` | **No** |
| `pooler-viewer-role` / `pooler-editor-role` | **No** |
| `scheduledbackup-viewer-role` / `-editor-role` | **No** |
| `database-viewer-role` / `database-editor-role` | **Yes** |
| `publication-viewer-role` / `publication-editor-role` | **Yes** |
| `subscription-viewer-role` / `subscription-editor-role` | **Yes** |

(`config/rbac/kustomization.yaml` also lists `role.yaml`, `role_binding.yaml`, with the comment
*"Those roles are not used by the Project itself."*)

#### 2.8.5 Documented RBAC for kubectl-plugin use-cases (`docs/src/kubectl-plugin.md:1480–1620`)

The two published example Roles are the closest official answer for a namespace-scoped read-only
insights app:

```yaml
# cnpg-log  (kubectl cnpg logs)
- apiGroups: ["postgresql.cnpg.io"]
  resources: ["clusters"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list"]
- apiGroups: [""]
  resources: ["pods/log"]
  verbs: ["get"]

# cnpg-status  (kubectl cnpg status)
- apiGroups: ["postgresql.cnpg.io"]
  resources: ["clusters"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list"]
- apiGroups: [""]
  resources: ["pods/exec"]
  verbs: ["create"]
- apiGroups: [""]
  resources: ["pods/proxy"]
  verbs: ["create"]
- apiGroups: ["policy"]
  resources: ["poddisruptionbudgets"]
  verbs: ["list"]
- apiGroups: ["barmancloud.cnpg.io"]
  resources: ["objectstores"]
  verbs: ["get"]
```
The doc also carries a per-command RBAC table for: `backup`, `certificate`, `destroy`, `fencing`,
`fio`, `hibernate`, `install`, `logs`, `maintenance`, `pgadmin4`, `pgbench`, `promote`, `psql`,
`publication`, `reload`, `report cluster`, `report operator`, `restart`, `status`, `subscription`,
`version`.

#### 2.8.6 Minimal RBAC for a namespace-scoped insights app (derived)

Combining §2.8.4 + §2.8.5 + the operator role:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: <app>-cnpg-reader, namespace: <ns>}
rules:
- apiGroups: ["postgresql.cnpg.io"]
  resources: [clusters, poolers, backups, scheduledbackups, databases, databaseroles,
              publications, subscriptions]
  verbs: [get, list, watch]
- apiGroups: ["postgresql.cnpg.io"]
  resources: [clusters/status, poolers/status, backups/status, scheduledbackups/status,
              databases/status, databaseroles/status, publications/status, subscriptions/status]
  verbs: [get]
- apiGroups: [""]
  resources: [pods, services, configmaps, events]
  verbs: [get, list, watch]
- apiGroups: [""]
  resources: [pods/log]
  verbs: [get]
- apiGroups: [""]
  resources: [pods/exec]        # only if shelling in / using kubectl cnpg psql
  verbs: [create]
- apiGroups: [""]
  resources: [secrets]          # <cluster>-ca/-server/-replication/-app/-superuser/-pooler
  verbs: [get, list, watch]
- apiGroups: ["apps"]
  resources: [deployments]      # pooler deployments
  verbs: [get, list, watch]
- apiGroups: ["batch"]
  resources: [jobs]             # backup/join jobs
  verbs: [get, list, watch]
- apiGroups: ["policy"]
  resources: [poddisruptionbudgets]
  verbs: [get, list, watch]
- apiGroups: ["discovery.k8s.io"]
  resources: [endpointslices]
  verbs: [get, list, watch]
- apiGroups: ["monitoring.coreos.com"]
  resources: [podmonitors]
  verbs: [get, list, watch]
```
Plus, cluster-scoped (only if reading `ClusterImageCatalog`): `ClusterRole` with
`postgresql.cnpg.io/clusterimagecatalogs` `[get, list, watch]`.

---

## 3. Instance manager HTTP API

**Source:** `pkg/management/url/url.go` (byte-identical at main / release-1.30 / release-1.23 /
release-1.20 / release-1.17 / release-1.15), `pkg/management/postgres/webserver/{remote,local,webserver}.go`,
`pkg/management/postgres/webserver/metricserver/metrics.go`,
`pkg/management/pgbouncer/metricsserver/metricsserver.go`.

### 3.1 The authoritative constants

```go
// Package url holds the constants for webserver routing
const (
    // LocalPort is the port for only available from Postgres.
    LocalPort int32 = 8010
    // PostgresMetricsPort is the port for the exporter of PostgreSQL related metrics (HTTP)
    PostgresMetricsPort int32 = 9187
    // PgBouncerMetricsPort is the port for the exporter of PgBouncer related metrics (HTTP)
    PgBouncerMetricsPort int32 = 9127

    PathFailSafe                   string = "/failsafe"
    PathHealth                     string = "/healthz"
    PathReady                      string = "/readyz"
    PathStartup                    string = "/startupz"
    PathPGControlData              string = "/pg/controldata"
    PathPgStatus                   string = "/pg/status"
    PathWALArchiveStatusCondition  string = "/cluster/status/condition/wal/archive"
    PathPgBackup                   string = "/pg/backup"
    PathPgModeBackup               string = "/pg/mode/backup"
    PathPgArchivePartial           string = "/pg/archive/partial"
    PathMetrics                    string = "/metrics"
    PathUpdate                     string = "/update"
    PathCache                      string = "/cache/"

    // StatusPort is the port for status HTTP requests
    StatusPort int32 = 8000
)
func Local(path string, port int32) string { return Build("http", "localhost", path, port) }
func Build(scheme, hostname, path string, port int32) string { /* neturl.URL → string */ }
```
Pre-1.23 variants (release-1.23 / 1.20 / 1.17 / 1.15) use `int` not `int32`, their
`Build(hostname, path, port)` hardcodes `http://`, and those releases lack `/failsafe`,
`/startupz`, `/pg/controldata`, `/cluster/status/condition/wal/archive`, `/pg/archive/partial`.

### 3.2 Server inventory

| Port | Constant | Scheme | Bind | Serves | Source file |
|---|---|---|---|---|---|
| **8000** | `url.StatusPort` | **HTTPS, TLS 1.3 min** | `:8000` | `/healthz`, `/readyz`, `/startupz`, `/failsafe`, `/pg/status`, `/pg/controldata`, `/pg/mode/backup`, `/pg/archive/partial`, `/update` | `webserver/remote.go` |
| **9187** | `url.PostgresMetricsPort` | HTTP; HTTPS when `--metrics-port-tls` | `:9187` | `/metrics` (PostgreSQL exporter) | `webserver/metricserver/metrics.go` |
| **8010** | `url.LocalPort` | plain HTTP | `localhost:8010` only | `/cache/`, `/pg/backup`, `/cluster/status/condition/wal/archive` | `webserver/local.go` |
| **9127** | `url.PgBouncerMetricsPort` | HTTP; HTTPS when `METRICS_PORT_TLS=true` | `:9127` | `/metrics` (PgBouncer exporter) | `pgbouncer/metricsserver/metricsserver.go` |
| **6060** | hardcoded literal, name `pprof` | HTTP | `:6060` | `/debug/pprof/*` (net/http/pprof) | `pkg/specs/pods.go` |
| **5432** | `postgres.ServerPort` | PG wire | — | not HTTP | — |
| **8080** | kubebuilder default | HTTP | operator Deployment pod | operator's controller-runtime metrics | `docs/src/monitoring.md` |

### 3.3 TLS and auth on port 8000

```go
server := &http.Server{
    Addr:              fmt.Sprintf(":%d", url.StatusPort),
    Handler:           serveMux,
    ReadTimeout:       DefaultReadTimeout,        // 20s
    ReadHeaderTimeout: DefaultReadHeaderTimeout,  // 3s
    TLSConfig: &tls.Config{
        MinVersion: tls.VersionTLS13,
        GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
            return instance.GetServerCertificate(), nil
        },
        // RequestClientCert asks the client to send a certificate but does not require it.
        // Authentication is enforced per-endpoint by withOperatorAuth.
        ClientAuth: tls.RequestClientCert,
    },
}
```
`DefaultReadTimeout = 20 * time.Second`, `DefaultReadHeaderTimeout = 3 * time.Second`
(`webserver/webserver.go`). `Webserver.Start()` calls `ListenAndServeTLS("","")` whenever
`TLSConfig != nil`.

**Which cert it serves:** `instance.GetServerCertificate()` — the `kubernetes.io/tls` secret named
by `cluster.Spec.Certificates.ServerTLSSecret`, defaulting to **`<cluster>-server`**. Not a separate
"instance manager" cert, and not a `<cluster>-metrics` cert (no such field exists).

**How clients authenticate** — `withOperatorAuth` (`remote.go`): *"wraps a handler to authenticate
the operator by pinning its client certificate's public key. It reads the expected fingerprint from
the instance's cached cluster … and compares it against the SHA-256 public key fingerprint of the
peer certificate presented by the caller."*

`pkg/certs/selfsigned.go:91-98`:
```go
// PublicKeyFingerprint returns the hex-encoded SHA-256 digest of the certificate's
// SubjectPublicKeyInfo. It depends only on the public key, so two certificates that
// share a key produce the same fingerprint. Note that the operator generates a fresh
// key on every restart, so its fingerprint is not stable across restarts.
func PublicKeyFingerprint(cert *x509.Certificate) string {
    digest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
    return hex.EncodeToString(digest[:])
}
```
Expected value read from `ws.instance.GetClusterOrDefault().Status.OperatorCertificateFingerprint`.

| Condition | Status | Message |
|---|---|---|
| No usable peer cert presented (caller spoke plain HTTP) | **401** | `client certificate required` |
| Cert presented, `Status.OperatorCertificateFingerprint == ""` | **503** | `operator certificate not registered` |
| Cert presented, fingerprint mismatch | **503** | `operator certificate not recognized` (transient while the fingerprint propagates) |

`docs/src/certificates.md:47-48`: *"instance manager's status port. This certificate is not signed
by the client CA: the instance manager trusts it by pinning its public-key fingerprint."*

**Metrics-server TLS** (`metricserver/metrics.go`):
```go
if serverInstance.MetricsPortTLS {
    server.TLSConfig = &tls.Config{
        MinVersion: tls.VersionTLS13,
        GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
            return serverInstance.GetServerCertificate(), nil
        },
    }
}
```
Same `<cluster>-server` cert, **no client auth on 9187**. Driven by `spec.monitoring.tls.enabled` →
`--metrics-port-tls` on the container command (`pkg/specs/pods.go`), read in
`internal/cmd/manager/instance/run/cmd.go` as `--metrics-port-tls` (bool, "Enable TLS for metrics
scraping"). The `--status-port-tls` flag exists but is **hidden** and documented *"Ignored: the
status port always uses TLS"* — kept only because plugins before 1.31 read it.

**Pod container ports** (`pkg/specs/pods.go`):
```go
Ports: []corev1.ContainerPort{
    {Name: "postgresql", ContainerPort: postgres.ServerPort, Protocol: "TCP"},   // 5432
    {Name: "metrics",    ContainerPort: url.PostgresMetricsPort, Protocol: "TCP"}, // 9187
    {Name: "status",     ContainerPort: url.StatusPort, Protocol: "TCP"},           // 8000
}
Command: []string{"/controller/manager", "instance", "run"}
```
pprof opt-in (`alpha.cnpg.io/enableInstancePprof: "true"`) appends
`{Name: "pprof", ContainerPort: 6060}` and `--pprof-server`. So `/debug/pprof/*` is **not** in the
status mux — separate `net/http/pprof` server on 6060, off by default.

### 3.4 Every endpoint — status server `:8000` HTTPS

The mux, with the file's own verbatim comments:

```go
serveMux := http.NewServeMux()
// Unauthenticated: used by the Kubernetes API server ProxyGet (kubectl cnpg status).
serveMux.HandleFunc(url.PathPgStatus, endpoints.pgStatus)
// Unauthenticated: kubelet liveness probe; must be reachable without client cert.
serveMux.HandleFunc(url.PathHealth, endpoints.isServerHealthy)
// Unauthenticated: kubelet readiness probe; must be reachable without client cert.
serveMux.HandleFunc(url.PathReady, endpoints.isServerReady)
// Unauthenticated: kubelet startup probe; must be reachable without client cert.
serveMux.HandleFunc(url.PathStartup, endpoints.isServerStartedUp)
// Unauthenticated: failsafe is a lightweight no-op called only by the operator.
// It does not perform any sensitive operation and needs no authentication.
serveMux.HandleFunc(url.PathFailSafe, endpoints.failSafe)
// Authenticated: backup drives a PostgreSQL pg_start/stop_backup session.
serveMux.HandleFunc(url.PathPgModeBackup, endpoints.withOperatorAuth(endpoints.backup))
// Authenticated: spawns a pg_controldata process per call; only the operator should invoke it.
serveMux.HandleFunc(url.PathPGControlData, endpoints.withOperatorAuth(endpoints.pgControlData))
// Authenticated: pgarchivepartial triggers WAL archival and must not be callable by arbitrary clients.
serveMux.HandleFunc(url.PathPgArchivePartial, endpoints.withOperatorAuth(endpoints.pgArchivePartial))
// Authenticated: update replaces the running instance manager binary.
serveMux.HandleFunc(url.PathUpdate, endpoints.withOperatorAuth(endpoints.updateInstanceManager(cancelFunc, exitedConditions)))
```
Plus `srv.routines = append(srv.routines, endpoints.cleanupStaleCollections)` — a background
goroutine that every **1 minute** closes stale backup connections.

| Method | Path | Auth | Returns | Caller |
|---|---|---|---|---|
| GET | `/healthz` | none | 200 if healthy; 500 otherwise | kubelet **liveness** probe (`HTTPGet{Path:"/healthz", Port:8000, Scheme:HTTPS}`, `PeriodSeconds: LivenessProbePeriod`, `TimeoutSeconds: 5`) |
| GET | `/readyz` | none | 200 if ready; **500 `"instance is not ready yet"`** when `!ws.instance.CanCheckReadiness()` | kubelet **readiness** probe (`Scheme: HTTPS`, `TimeoutSeconds: 5`) |
| GET | `/startupz` | none | 200; if `PgRewindIsRunning \|\| MightBeUnavailable()` writes plain text **`"Skipped"`** | kubelet **startup** probe (`Scheme: HTTPS`, `PeriodSeconds: StartupProbePeriod`, `TimeoutSeconds: 5`) |
| GET | `/failsafe` | none | `application/json` → `url.FailSafeResponse{ TargetPrimary string \`json:"targetPrimary,omitempty"\` }`. `ParseFailSafe` tolerates pre-upgrade peers that returned plain `"OK"` | operator only (primary-isolation / lease watchdog) |
| GET | **`/pg/status`** | none | `application/json` — `ws.instance.GetStatus()` marshalled (`postgres.PodStatus`: pod name/uid/node, phases, pg data). 500 on error | **operator `ProxyGet`** and **`kubectl cnpg status`** |
| GET | `/pg/controldata` | **operator** | `application/json` → `struct { Data string \`json:"data,omitempty"\` }` = raw `pg_controldata` stdout. Spawns a process per call | operator only |
| GET | `/pg/mode/backup` | **operator** | `Response[BackupResultData]`; `{}` if no session; error code `BACKUP_STATUS_CONTAINS_ERROR` | operator (barman-less online backup) |
| POST | `/pg/mode/backup` | **operator** | body `StartBackupRequest{ ImmediateCheckpoint bool \`json:"immediateCheckpoint"\`; WaitForArchive bool \`json:"waitForArchive"\`; BackupName string \`json:"backupName"\` }` → `pg_start_backup`. 400 `FAILED_TO_PARSE_REQUEST`; 422 `CANNOT_INITIALIZE_CONNECTION` | operator |
| PUT | `/pg/mode/backup` | **operator** | body `StopBackupRequest{ BackupName string \`json:"backupName"\` }` → `pg_stop_backup`. 400 `NO_ONGOING_BACKUP`; 422 `NOT_CURRENT_RUNNING_BACKUP`; 422 `BACKUP_FAILED`; 422 `CANNOT_CLOSE_NOT_STARTED` | operator |
| POST | `/pg/archive/partial` | **operator** | 200 + JSON string `walFile`. Errors: 400 `NOT_FENCED`, `NO_CLUSTER_FOUND`, `NOT_EXPECTED_PRIMARY`, `COULD_NOT_PARSE_REDOWAL_FILE`, `ERROR_WHILE_CREATING_SYMLINK`, `ERROR_WHILE_EXECUTING_WAL_ARCHIVE`. Reads the REDO WAL file from `pg_controldata`, hard-links `<wal>.partial`, runs `/controller/manager wal-archive <wal>.partial` | operator (fenced-instance partial WAL archival) |
| PUT | `/update` | **operator** | body = the new instance-manager **binary**. Non-PUT → **405** `"wrong method used"`; already upgrading → **418** `"instance manager is already upgrading"` | operator (rolling IM upgrade) |

Shared JSON envelope (`webserver.go`) — used by every JSON reply above:
```go
type Error struct {
    Code    string   `json:"code"`
    Message string   `json:"message"`
    Details []Error  `json:"details,omitempty"`
}
type Response[T interface{}] struct {
    Data  *T     `json:"data,omitempty"`
    Error *Error `json:"error,omitempty"`
}
func sendJSONResponse[T any](w http.ResponseWriter, statusCode int, Response[T])  // Content-Type: application/json
func sendBadRequestJSONResponse(w, errorCode, message)           // 400
func sendUnprocessableEntityJSONResponse(w, errorCode, message)  // 422
func sendJSONResponseWithData[T](w, statusCode, T)
const errCodeAnotherRequestInProgress = "ANOTHER_REQUEST_IN_PROGRESS"
func IsRetryableError(err *Error) bool
```

### 3.5 Every endpoint — local server `localhost:8010` plain HTTP

```go
serveMux := http.NewServeMux()
serveMux.HandleFunc(url.PathCache, endpoints.serveCache)
serveMux.HandleFunc(url.PathPgBackup, endpoints.requestBackup)
serveMux.HandleFunc(url.PathWALArchiveStatusCondition, endpoints.setWALArchiveStatusCondition)
server := &http.Server{
    Addr:              fmt.Sprintf("localhost:%d", url.LocalPort),
    Handler:           serveMux,
    ReadHeaderTimeout: DefaultReadTimeout,
    ReadTimeout:       DefaultReadTimeout,
}
```
Bound to `localhost` only — reachable from inside the pod (Postgres hooks, pgbouncer/sidecar
containers), never from the network. No TLS, no auth.

| Method | Path | Params / body | Returns | Caller |
|---|---|---|---|---|
| GET | `/cache/<key>` | prefix `/cache/` stripped; key ∈ `cache.ClusterKey` → JSON of the `Cluster` object (404 if absent); `cache.WALRestoreKey`, `cache.WALArchiveKey` (via `cache.LoadEnv`); `cache.WALRestoreConfigKey` (via `cache.Load`); anything else → 404 | JSON / env blob | Postgres hooks + in-pod helpers (`internal/management/cache/client`) |
| GET | `/pg/backup` | **query param `?name=<backupname>`** (required; missing → 400 `"Missing backup name parameter"`). Dispatches on `backup.Spec.Method`: `BackupMethodBarmanObjectStore` → 409 `"Barman backup not configured in the cluster"` if absent, else runs `postgres.NewBarmanBackupCommand(...)` and writes `"OK"`; `BackupMethodPlugin` → 409 `"Plugin backup not configured in the cluster"` if absent, else `NewPluginBackupCommand(...).Start(ctx)` then `"OK"`; default → 400 `Unknown backup method: %v` | plain text `"OK"` | `kubectl cnpg backup` / operator-invoked in-pod backup |
| POST | `/cluster/status/condition/wal/archive` | body `ArchiveStatusRequest{ Error string \`json:"error,omitempty"\` }` → sets `apiv1.ConditionContinuousArchiving` (True / `ContinuousArchivingSucceeding`, or False with reason `ConditionReasonContinuousArchivingFailing`) on the Cluster object | plain text `"OK"` | the WAL archiver (reports archive success/failure through the IM so the IM, not the hook, owns the k8s write) |

### 3.6 Metrics servers

`:9187` (`metricserver/metrics.go`):
```go
registry := prometheus.NewRegistry()
registry.Register(exporter)
registry.Register(collectors.NewGoCollector())
serveMux := http.NewServeMux()
serveMux.Handle(url.PathMetrics, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
server := &http.Server{ Addr: fmt.Sprintf(":%d", url.PostgresMetricsPort), ... }
```
| Method | Path | Returns | Caller |
|---|---|---|---|
| GET | `/metrics` | Prometheus text exposition (`text/plain; version=0.0.4`) via `promhttp.HandlerFor` | Prometheus / PodMonitor; `kubectl cnpg pgmetrics` |

`:9127` (PgBouncer): `serveMux.Handle(url.PathMetrics, promhttp.HandlerFor(registry, ...))` where
registry = `NewExporter(ctx)` + `collectors.NewGoCollector()`. **The pgbouncer IM exposes exactly one
path: `GET /metrics` on 9127.** No `/liveness`, no `/readiness`. Pooler pod readiness probe is
`TCPSocket` on port 5432 — **there is no HTTP liveness/readiness endpoint on a pooler pod.**
`internal/cmd/manager/pgbouncer/run/cmd.go` flags: `--pooler-name` (env `POOLER_NAME`),
`--namespace` (env `NAMESPACE`), `--metrics-port-tls` (env `METRICS_PORT_TLS`).

### 3.7 Endpoints from the brief that do not exist (1.15 → main)

`/liveness`, `/readiness`, `/promote`, `/demote`, `/reload`, `/restart`, `/fence`, `/unfence`,
`/replication-status`, `/pg-basebackup`, `/join-copy`, `/join`, `/cleanup`, `/wait-primary`,
`/status`, `/ping` — none appear in any `pkg/management/url/url.go` from release-1.15 to main, and
**port 9101 does not exist**. Real equivalents: `/healthz` (liveness), `/readyz` (readiness),
`/startupz` (startup), `/pg/status` (status). Promote / demote / restart / fence are **not** HTTP
calls in 1.20+ — driven via the Cluster CR (`spec.instances`, `cnpg.io/fencedInstances` annotation,
`status.targetPrimary`) and the reconciler. `instance join` and `instance pgbasebackup` are **CLI
subcommands** (`internal/cmd/manager/instance/join/cmd.go`, `.../pgbasebackup/cmd.go`).
`internal/manager/routes.go` and `mux.NewRouter` do not exist — routing is stdlib
`http.NewServeMux()` across three separate `http.Server` values.

### 3.8 Probes (`docs/src/instance_manager.md`)

All three probes target port **8000** with `Scheme: HTTPS`, `TimeoutSeconds: 5`.
`startDelay` default **3600**, `livenessProbeTimeout` default **30**. The doc also covers the
**isolation check** (the liveness pinger, `alpha.cnpg.io/livenessPinger`) and the **3-step shutdown**
sequence.

### 3.9 Inspecting the endpoints (from `docs/src/monitoring.md`)

```bash
kubectl port-forward cluster-example-1 9187:9187     # then open http://localhost:9187/metrics
curl -s  <pod_ip>:9187/metrics                       # plain HTTP
curl -sk https://<pod_ip>:9187/metrics               # when spec.monitoring.tls.enabled
kubectl -n cnpg-system port-forward pod/<CONTROLLER-MANAGER-POD> 8080:8080   # operator metrics
```

---

## 4. Metrics / postgres_exporter integration

### 4.1 The architecture, in one paragraph

CNPG does **not** ship a `postgres_exporter` sidecar. The metrics exporter is **compiled into the
instance manager binary** (`pkg/management/postgres/webserver/metricserver/`), served on **port
9187** per pod, and named `metrics` on the container. `docs/src/monitoring.md`: *"For each
PostgreSQL instance, the operator provides an exporter of metrics for Prometheus via HTTP or HTTPS,
on port 9187, named `metrics`."* Its query-config format is **derived from** postgres_exporter's
YAML `queries:` schema (v0.12-era), and its code header says *"This code is inspired on
[postgres_exporter]"*. The upstream project itself has since moved to Go collectors +
`postgres_exporter.yml`; CNPG still uses the YAML query format.

### 4.2 CRD fields — `spec.monitoring` (`MonitoringConfiguration`)

Referenced from `ClusterSpec` as:
```go
// +optional
Monitoring *MonitoringConfiguration `json:"monitoring,omitempty"`
```

| Field | Type | Doc | Default |
|---|---|---|---|
| `disableDefaultQueries` | `boolean` | Whether default queries should be injected | `false` |
| `customQueriesConfigMap` | `ConfigMapKeySelector[]` | The list of config maps containing the custom queries | — |
| `customQueriesSecret` | `SecretKeySelector[]` | The list of secrets containing the custom queries | — |
| `enablePodMonitor` | `boolean` | Enable or disable the `PodMonitor`. **Deprecated**: will be removed in an upcoming release | `false` |
| `tls` | `ClusterMonitoringTLSConfiguration` | Configure TLS for the metrics endpoint. Changing forces a rollout of all instances | — |
| `podMonitorMetricRelabelings` | `monitoringv1.RelabelConfig[]` | Metric relabelings for PodMonitor, applied before ingestion. **Deprecated** | — |
| `podMonitorRelabelings` | `monitoringv1.RelabelConfig[]` | Relabelings for PodMonitor, applied before scraping. **Deprecated** | — |
| `metricsQueriesTTL` | `metav1.Duration` | Interval during which metrics computed from queries are considered current; default **30s**; zero disables caching | 30s |

```go
// ClusterMonitoringTLSConfiguration for configurations of the TLS on cluster monitoring
type ClusterMonitoringTLSConfiguration struct {
    // +kubebuilder:default:=false
    // +optional
    Enabled bool `json:"enabled,omitempty"`
}
```
`release-1.30`'s `MonitoringConfiguration` is **identical** to main's.

**Pooler equivalent**: `PoolerMonitoringConfiguration` + `PoolerMonitoringTLSConfiguration`, set at
`.spec.pgbouncer.monitoring` (fields `enablePodMonitor`, `tls.enabled`). Added in **v1.30.0**.

**No `MetricsConfiguration` type exists** — so the brief's `metrics.enabled` / `metrics.image` /
`imagePullPolicy` / `containerConfig` / `metrics.queries` / `customQueriesMetricPrefix` /
`metrics.service` / `metrics.serviceMonitor` / `metrics.podMonitor` are **not** CNPG v1.30 fields
(§0.2).

### 4.3 Query-file schema (`pkg/management/postgres/metrics/parser.go`)

```go
type UserQueries map[string]UserQuery
type UserQuery struct {
    Query           string    `yaml:"query"`
    PredicateQuery  string    `yaml:"predicate_query"`
    Metrics         []Mapping `yaml:"metrics"`
    Master          bool      `yaml:"master"`    // wokeignore:rule=master
    Primary         bool      `yaml:"primary"`
    CacheSeconds    uint64    `yaml:"cache_seconds"`
    RunOnServer     string    `yaml:"runonserver"`
    TargetDatabases []string  `yaml:"target_databases"`
    Name            string    `yaml:"name"`
}
type Mapping map[string]ColumnMapping
type ColumnMapping struct {
    Usage           ColumnUsage        `yaml:"usage"`
    Description     string             `yaml:"description"`
    Mapping         map[string]float64 `yaml:"metric_mapping"`
    SupportedVersions string           `yaml:"pg_version"`
    Name            string             `yaml:"name"`
}
func ParseQueries(content []byte) (UserQueries, error)
```

Documented field semantics (`docs/src/monitoring.md`, "Structure of a user defined metric"):
```yaml
<MetricName>:
      query: "<SQLQuery>"
      metrics:
        - <ColumnName>:
            usage: "<MetricType>"
            description: "<MetricDescription>"
```
| Field | Meaning |
|---|---|
| `<MetricName>` (map key) | Namespace for the metrics produced by this query; overridable per-column via `name` |
| `query` | The SQL to run |
| `name` | Override the metric name for a column |
| `primary` | Run only on the primary (formerly `master`, now a deprecated alias) |
| `master` | Deprecated alias of `primary` |
| `runonserver` | Semver range, e.g. `">=12.0.0 <=15.0.0"` — unsupported columns are internally converted to `DISCARD` |
| `target_databases` | List of DBs, or shell globs (`*`, `?`, `[]` via `path.Match`). Auto-discovery via `SELECT datname FROM pg_catalog.pg_database WHERE datallowconn AND NOT datistemplate AND pg_catalog.has_database_privilege(datname,'CONNECT')` |
| `predicate_query` | Must return ≤1 row, 1 boolean column. If true, `query` runs; executed in the same transaction, same DBs |
| `metrics` | List; each entry is `{<ColumnName>: {name, usage, description, metric_mapping}}` |
| `cache_seconds` | **Documented as not implemented in CloudNativePG** (CNPG uses cluster-level `metricsQueriesTTL` instead) |

### 4.4 `usage` enum — exactly seven values

`docs/src/monitoring.md` and `pkg/management/postgres/metrics/parser.go`:
```go
type ColumnUsage string
const (
    DISCARD      ColumnUsage = "DISCARD"      // ignore the column
    LABEL        ColumnUsage = "LABEL"        // promote to a Prometheus label
    COUNTER      ColumnUsage = "COUNTER"
    GAUGE        ColumnUsage = "GAUGE"
    MAPPEDMETRIC ColumnUsage = "MAPPEDMETRIC" // use with the supplied `metric_mapping` of text values
    DURATION     ColumnUsage = "DURATION"     // text duration, converted to milliseconds
    HISTOGRAM    ColumnUsage = "HISTOGRAM"
)
```
Upstream postgres_exporter (`exporter/postgres_exporter.go:47-66`, master) has the identical seven
as a Go `int` enum (`type ColumnUsage int`, `iota`-based) — same names, same meanings. The brief's
`MASTER` / `REPLICA` / `BUILTIN` / `DYNAMIC` / `TABLESPACE` / `EXTEND` / `DESCRIBE` set is from
`burningalchemist/sql_exporter`, which postgres_exporter's own README now points at for generic SQL
monitoring.

### 4.5 Metric naming rule

`pkg/management/postgres/metrics/mappings.go` + `docs/src/monitoring.md`:
```
cnpg_<MetricName>_<ColumnName>{<LabelColumn>="<LabelValue>", ...} <ColumnValue>
```
FQName built as `fmt.Sprintf("%s_%s", namespace, columnName)` where `namespace = cnpg_<QueryName>`
(or `columnMapping.Name` when set). Documented examples:
```
cnpg_pg_replication_in_recovery 0
cnpg_pg_replication_lag 0
cnpg_pg_replication_streaming_replicas 2
cnpg_pg_replication_is_wal_receiver_up 0
```
postgres_exporter's namespace is `pg` (`collector/collector.go:58`: `const namespace = "pg"`;
`config/config.go`: `DefaultMetricPrefix string = "pg"`), giving `pg_<subsystem>_<name>`.

### 4.6 Where query files come from

| Mechanism | Detail |
|---|---|
| Default ConfigMap name | **`cnpg-default-monitoring`** (`api/v1/cluster_defaults.go:38-44`: `DefaultMonitoringConfigMapName`; `DefaultMonitoringSecretName = DefaultMonitoringConfigMapName`; **`DefaultMonitoringKey = "queries"`**) |
| Operator-level config | `MONITORING_QUERIES_CONFIGMAP` / `MONITORING_QUERIES_SECRET` (operator config file) — default to `cnpg-default-monitoring` |
| Distribution | The operator's `cnpg-default-monitoring` ConfigMap is **copied into each Cluster's namespace** with the fixed name `cnpg-default-monitoring` |
| Per-cluster custom | `.spec.monitoring.customQueriesConfigMap` / `.spec.monitoring.customQueriesSecret` (lists of `ConfigMapKeySelector` / `SecretKeySelector`); must be in the **same namespace as the Cluster** |
| Auto-reload | Add label **`cnpg.io/reload: ""`** to the ConfigMap/Secret, else run `kubectl cnpg reload` |
| Name collisions | Logged as `Query with the name already found. Overwriting the existing one.` (key `queryName`); documented wording: `Query with the same name already found. Overwriting the existing one.` |
| Disable defaults | `spec.monitoring.disableDefaultQueries: true` |

### 4.7 How the exporter authenticates to PostgreSQL

`docs/src/monitoring.md`: queries run as role **`cnpg_metrics_exporter`** (`MetricsExporterUserName`),
a member of **`pg_monitor`**, with `application_name` set to **`cnpg_metrics_exporter`**, using
**peer authentication on the pod-local Unix socket**. Every query is **atomic** — executed in its own
read-only transaction. Because `session_user` is never a superuser, the session cannot escalate via
`RESET ROLE` / `RESET SESSION_AUTHORIZATION`. (This is the CVE-2026-44477 fix; pre-1.29.1 it
authenticated as the `postgres` superuser.)

Manual creation:
```sql
CREATE ROLE cnpg_metrics_exporter WITH LOGIN NOSUPERUSER NOCREATEDB
    NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT;
GRANT pg_monitor TO cnpg_metrics_exporter;
```
For custom queries touching user objects:
```sql
GRANT USAGE ON SCHEMA myschema TO cnpg_metrics_exporter;
GRANT SELECT ON TABLE myschema.mytable TO cnpg_metrics_exporter;
GRANT CONNECT ON DATABASE <db> TO cnpg_metrics_exporter;
```
Custom queries run in a transaction whose `search_path` is pinned to
**`pg_catalog, public, pg_temp`** — schema-qualify catalog references to avoid `search_path`
shadowing.

Default target DB per bootstrap method: `initdb.database` (or `app`), `recovery.database` (or
`postgres`), `pg_basebackup.database` (or `postgres`) — overridable per-query via `target_databases`.

### 4.8 Caching

Default **30s** (`cluster.spec.monitoring.metricsQueriesTTL`); `0` disables. Exposed as
`cache_hits`, `cache_misses`, `last_update_timestamp` (namespaced under the query set).
`pkg/management/postgres/metrics/collector.go` fields: `cacheHits`, `cacheMiss prometheus.Gauge`,
`lastUpdateTimestamp`; methods `Name()`, `Update()`, `ShouldUpdate(ttl)`, `RecordCacheHit()`.
Upstream postgres_exporter's per-query `cache_seconds` is **not implemented** in CNPG.

### 4.9 TLS on the metrics endpoint

`spec.monitoring.tls.enabled: true` → metrics served over **HTTPS** using the same server cert as
PG port 5432 (`<cluster>-server`). **Changing it triggers a rolling restart of all instances.**
Prometheus scrape config: `scheme: https`, `tlsConfig.ca.secret.name: <cluster>-ca`,
`tlsConfig.ca.secret.key: ca.crt`, `serverName: <cluster>-rw`.
Operator's own metrics TLS: `METRICS_CERT_DIR` env + mounted Secret `cnpg-metrics-cert` (keys
`tls.crt`, `tls.key`).

### 4.10 The complete CNPG default metric set — 14 queries

From `config/manager/default-monitoring.yaml` (491 lines, `main`). This is the **authoritative**
list of what a CNPG pod exposes from PostgreSQL. Every metric name below is
`cnpg_<QueryName>_<ColumnName>`.

| # | Query | Column | usage | Description |
|---|---|---|---|---|
| 1 | `backends` | `datname` | LABEL | Name of the database |
| | | `usename` | LABEL | Name of the user |
| | | `application_name` | LABEL | Name of the application |
| | | `state` | LABEL | State of the backend |
| | | `total` | GAUGE | Number of backends |
| | | `max_tx_duration_seconds` | GAUGE | Maximum duration of a transaction in seconds |
| 2 | `backends_waiting` | `total` | GAUGE | Total number of backends that are currently waiting on other queries |
| 3 | `pg_database` | `datname` | LABEL | (label) |
| | | `size_bytes` | GAUGE | Disk space used by the database |
| | | `xid_age` | GAUGE | Number of transactions from the frozen XID to the current one |
| | | `mxid_age` | GAUGE | Number of multiple transactions (Multixact) from the frozen XID to the current one |
| 4 | `pg_postmaster` | `start_time` | GAUGE | Time at which postgres started (based on epoch) → **`cnpg_pg_postmaster_start_time`** |
| 5 | `pg_replication` | `lag` | GAUGE | Replication lag behind primary in seconds → **`cnpg_pg_replication_lag`** |
| | | `in_recovery` | GAUGE | Whether the instance is in recovery |
| | | `is_wal_receiver_up` | GAUGE | Whether the instance wal_receiver is up |
| | | `streaming_replicas` | GAUGE | Number of streaming replicas connected to the instance |
| 6 | `pg_replication_slots` | `slot_name` | LABEL | (label) |
| | | `slot_type` | LABEL | (label) |
| | | `database` | LABEL | (label) |
| | | `active` | GAUGE | Flag indicating whether the slot is active |
| | | `pg_wal_lsn_diff` | GAUGE | Replication lag in bytes → `cnpg_pg_replication_slots_pg_wal_lsn_diff` |
| 7 | `pg_stat_archiver` | `archived_count` | COUNTER | (archived WAL files) |
| | | `failed_count` | COUNTER | (failed archives) |
| | | `seconds_since_last_archival` | GAUGE | |
| | | `seconds_since_last_failure` | GAUGE | |
| | | `last_archived_time` | GAUGE | Epoch of the last time WAL archiving succeeded |
| | | `last_failed_time` | GAUGE | |
| | | `last_archived_wal_start_lsn` | GAUGE | Archived WAL start LSN |
| | | `last_failed_wal_start_lsn` | GAUGE | Last failed WAL LSN |
| | | `stats_reset_time` | GAUGE | |
| 8 | `pg_stat_bgwriter` | `checkpoints_timed` | COUNTER | |
| | | `checkpoints_req` | COUNTER | |
| | | `checkpoint_write_time` | COUNTER | milliseconds |
| | | `checkpoint_sync_time` | COUNTER | milliseconds |
| | | `buffers_checkpoint` | COUNTER | |
| | | `buffers_clean` | COUNTER | |
| | | `maxwritten_clean` | COUNTER | |
| | | `buffers_backend` | COUNTER | |
| | | `buffers_backend_fsync` | COUNTER | |
| | | `buffers_alloc` | COUNTER | |
| 9 | `pg_stat_bgwriter_17` | `buffers_clean` | COUNTER | PG17-shaped variant (post-17 `pg_stat_bgwriter` slimmed down) |
| | | `maxwritten_clean` | COUNTER | |
| | | `buffers_alloc` | COUNTER | |
| | | `stats_reset_time` | GAUGE | |
| 10 | `pg_stat_checkpointer` | `checkpoints_timed` | COUNTER | |
| | | `checkpoints_req` | COUNTER | |
| | | `restartpoints_timed` | COUNTER | |
| | | `restartpoints_req` | COUNTER | |
| | | `restartpoints_done` | COUNTER | |
| | | `write_time` | COUNTER | |
| | | `sync_time` | COUNTER | |
| | | `buffers_written` | COUNTER | |
| | | `stats_reset_time` | GAUGE | |
| 11 | `pg_stat_database` | `datname` | LABEL | (label) |
| | | `xact_commit` | COUNTER | |
| | | `xact_rollback` | COUNTER | |
| | | `blks_read` | COUNTER | |
| | | `blks_hit` | COUNTER | |
| | | `tup_returned` | COUNTER | |
| | | `tup_fetched` | COUNTER | |
| | | `tup_inserted` | COUNTER | |
| | | `tup_updated` | COUNTER | |
| | | `tup_deleted` | COUNTER | |
| | | `conflicts` | COUNTER | |
| | | `temp_files` | COUNTER | |
| | | `temp_bytes` | COUNTER | |
| | | `deadlocks` | COUNTER | |
| | | `blk_read_time` | COUNTER | |
| | | `blk_write_time` | COUNTER | |
| 12 | `pg_stat_replication` | `usename` | LABEL | |
| | | `application_name` | LABEL | |
| | | `client_addr` | LABEL | |
| | | `client_port` | LABEL | |
| | | `backend_start` | COUNTER | |
| | | `backend_xmin_age` | COUNTER | |
| | | `sent_diff_bytes` | GAUGE | |
| | | `write_diff_bytes` | GAUGE | |
| | | `flush_diff_bytes` | GAUGE | |
| | | `replay_diff_bytes` | GAUGE | |
| | | `write_lag_seconds` | GAUGE | |
| | | `flush_lag_seconds` | GAUGE | |
| | | `replay_lag_seconds` | GAUGE | |
| 13 | `pg_settings` | `name` | LABEL | Name of the setting |
| | | `setting` | GAUGE | Setting value |
| 14 | `pg_extensions` | `datname` | LABEL | |
| | | `extname` | LABEL | |
| | | `default_version` | LABEL | |
| | | `installed_version` | LABEL | |
| | | `update_available` | GAUGE | An update is available |

`pg_extensions` is the **only** default query that sets `target_databases: ['*']` — it enumerates
every connectable database.

**Views the brief asked about that are NOT in CNPG's default set:** `pg_stat_statements`,
`pg_stat_statements_info`, `pg_stat_user_tables`, `pg_stat_user_indexes`, `pg_stat_all_tables`,
`pg_stat_io`, `pg_stat_slru`, `pg_stat_xact_*`, `pg_stat_progress_*`, `pg_stat_gssapi`,
`pg_stat_subscription`, `pg_stat_database_conflicts`, `pg_stat_wal` (as a query — it *is* emitted by
the Go collector, §5), tablespaces. Those must be added as custom queries or sourced from
postgres_exporter (§4.11).

### 4.11 postgres_exporter's own format (for the sidecar path)

| File | Flag | Parser | Purpose |
|---|---|---|---|
| `postgres_exporter.yml` | `--config.file` (default `postgres_exporter.yml`; `=""` to run without) | `gopkg.in/yaml.v3` (`config/config.go`) | auth_modules, metric prefix, collector on/off, timeouts |
| `queries.yaml` | `--extend.query-path` (**deprecated in v0.16+**) | `gopkg.in/yaml.v2` (`exporter/queries.go`) | custom SQL queries → metrics |

v0.16.0 README.md:310-315: *"### Adding new metrics via a config file (DEPRECATED) — This feature is
deprecated in favor of built-in collector functions. For generic SQL database monitoring see
[sql_exporter](https://github.com/burningalchemist/sql_exporter). The -extend.query-path
command-line argument specifies a YAML file containing additional queries to run."*
`queries.yaml` at both v0.16.0 and master is now a **two-line stub**:
```yaml
# Adding queries to this file is deprecated
# Example queries have been transformed into collectors.
```
`--disable-default-metrics` (README.md:317-321): *"To work with non-officially-supported postgres
versions (e.g. 8.2.15), or variants of postgres (e.g. Greenplum), you can disable the default
metrics with the `--disable-default-metrics` flag."*

`exporter/queries.go` (master) schema:
```go
type UserQuery struct {
    Query        string    `yaml:"query"`
    Metrics      []Mapping `yaml:"metrics"`
    Master       bool      `yaml:"master"`        // Querying only for master database
    CacheSeconds uint64    `yaml:"cache_seconds"` // Number of seconds to cache the namespace result metrics for.
    RunOnServer  string    `yaml:"runonserver"`   // Querying to run on which server version
}
type UserQueries map[string]UserQuery
func parseUserQueries(content []byte, logger *slog.Logger) (map[string]intermediateMetricMap, map[string]string, error)
type MappingOptions struct {
    Usage             string             `yaml:"usage"`
    Description       string             `yaml:"description"`
    Mapping           map[string]float64 `yaml:"metric_mapping"` // Optional column mapping for MAPPEDMETRIC
    SupportedVersions semver.Range       `yaml:"pg_version"`     // Unsupported columns are internally converted to DISCARD.
}
type Mapping map[string]MappingOptions
type ColumnMapping struct { /* same fields, unexported */ }
type intermediateMetricMap struct {
    columnMappings map[string]ColumnMapping
    master         bool
    cacheSeconds   uint64
}
```
File's own TODO: *"the YAML this supports is 'non-standard' - we should move away from it."*
`addQueries(content, pgVersion, server, metricPrefix)` merges into `metricMap` + `queryOverrides`;
existing keys log `"Overriding metric from user YAML file"`. **CNPG's parser is a superset** of this
(adds `predicate_query`, `primary`, `target_databases`, per-column `name`).

`config/config.go` (master):
```go
const (
    DefaultMetricPrefix      string = "pg"
    DefaultCollectionTimeout time.Duration = time.Minute
    DefaultLongRunningTransactionsThreshold time.Duration = time.Minute
    DefaultAutoDiscoverDatabasesMaxConcurrency int = 10
    DefaultPGStatStatementsIncludeQuery bool = false
    DefaultPGStatStatementsQueryLength  uint = 120
    DefaultPGStatStatementsLimit        uint = 100
)
type Config struct {
    DataSourceNames                     []string
    MetricPrefix                        string
    CollectionTimeout                   time.Duration
    WrapLargeCounters                   bool
    DisableDefaultMetrics               bool
    AutoDiscoverDatabases               bool
    AutoDiscoverDatabasesMaxConcurrency int
    UserQueriesPath                     string
    ConstantLabels                      string
    ExcludeDatabases                    []string
    IncludeDatabases                    []string
    Collectors                          map[string]bool
    LongRunningTransactions             LongRunningTransactionsConfig
    PGStatStatements                    PGStatStatementsConfig
}
```

### 4.12 postgres_exporter collector catalog and full metric list (v0.21.1 / master)

**36 collector names** (`config/config.go` constants; `collector/collector_names.go`). Enabled-by-default
per `docs/configuration.md`:

| Collector | Default | Metrics (all prefixed `pg_`) | Labels |
|---|---|---|---|
| `buffercache_summary` | disabled | `pg_buffercache_summary_{buffers_used, buffers_unused, buffers_dirty, buffers_pinned, usagecount_avg}` | — |
| `database` | enabled | `pg_database_size_bytes`, `pg_database_connection_limit` | `datname` |
| `database_wraparound` | disabled | `pg_database_wraparound_age_datfrozenxid`, `pg_database_wraparound_age_datminmxid`, `pg_database_wraparound_age_datfrozenxid_seconds` (deprecated alias), `pg_database_wraparound_age_datminmxid_seconds` (deprecated alias) | `datname` |
| `locks` | enabled | `pg_locks_count` | `datname`, `mode` |
| `long_running_transactions` | disabled | `pg_long_running_transactions_oldest_timestamp_seconds` | — |
| `postmaster` | disabled | `pg_postmaster_start_time_seconds` | — |
| `process_idle` | disabled | `pg_process_idle_seconds` | `state`, `application_name` |
| `replication` | enabled | `pg_replication_lag_seconds`, `pg_replication_is_replica`, `pg_replication_last_replay_seconds` | — |
| `replication_slots` | enabled | `pg_replication_slots_{active, pg_wal_lsn_diff, pg_xlog_location_diff, conflicting, inactive_since_timestamp_seconds, failover, synced}`; plus per-slot: `pg_replication_slots_slot_current_wal_lsn`, `pg_replication_slots_slot_confirmed_flush_lsn`, `pg_replication_slots_slot_is_active`, `pg_replication_slots_safe_wal_size_bytes`, `pg_replication_slots_wal_status` | last five: `slot_name`, `slot_type`, `wal_status` |
| `roles` | enabled | `pg_roles_connection_limit` | `rolname` |
| `settings` | enabled | `pg_settings_pending_restart` + dynamic `pg_settings_<settingname>` (optionally `_<unit>` suffix, e.g. `bytes`, `seconds`; `pg_setting.go:142` `BuildFQName(namespace, settingsSubsystem, name)` with `name = fmt.Sprintf("%s_%s", name, unit)`) | `name` |
| `stat_activity` | enabled | `pg_stat_activity_count`, `pg_stat_activity_max_tx_duration` | — |
| `stat_activity_autovacuum` | disabled | `pg_stat_activity_autovacuum_timestamp_seconds` | `datname`, `schemaname`, `relname` |
| `stat_archiver` | enabled | `pg_stat_archiver_{archived_count, failed_count, last_archive_age}` | — |
| `stat_bgwriter` | enabled | `pg_stat_bgwriter_{checkpoints_timed_total, checkpoints_req_total, checkpoint_write_time_total, checkpoint_sync_time_total, buffers_checkpoint_total, buffers_clean_total, maxwritten_clean_total, buffers_backend_total, buffers_backend_fsync_total, buffers_alloc_total, stats_reset_total}` | — |
| `stat_checkpointer` | disabled (PG17+ only) | `pg_stat_checkpointer_{num_timed_total, num_requested_total, restartpoints_timed_total, restartpoints_req_total, restartpoints_done_total, write_time_total, sync_time_total, buffers_written_total, stats_reset_total}` | — |
| `stat_database` | enabled | `pg_stat_database_{numbackends, xact_commit, xact_rollback, blks_read, blks_hit, tup_returned, tup_fetched, tup_inserted, tup_updated, tup_deleted, conflicts, temp_files, temp_bytes, deadlocks, blk_read_time, blk_write_time, stats_reset, active_time_seconds_total}` | `datid`, `datname` |
| `stat_progress_vacuum` | enabled | `pg_stat_progress_vacuum_phase`; `pg_stat_progress_vacuum_{heap_blks, heap_blks_scanned, heap_blks_vacuumed, index_vacuums, max_dead_tuples, num_dead_tuples}` | `datname`, `relname` (+ `phase` on `_phase`) |
| `stat_replication` | enabled | `pg_stat_replication_pg_current_wal_lsn_bytes`, `pg_stat_replication_pg_wal_lsn_diff`, `pg_stat_replication_pg_xlog_location_diff` | — |
| `stat_statements` | **disabled** | `pg_stat_statements_calls_total`, `pg_stat_statements_seconds_total`, `pg_stat_statements_rows_total`, `pg_stat_statements_block_read_seconds_total`, `pg_stat_statements_block_write_seconds_total`, `pg_stat_statements_query_id` | `user`, `datname`, `queryid` (`query_id` metric: `queryid`, `query`) |
| `stat_user_tables` | enabled | `pg_stat_user_tables_{seq_scan, seq_tup_read, idx_scan, idx_tup_fetch, n_tup_ins, n_tup_upd, n_tup_del, n_tup_hot_upd, n_live_tup, n_dead_tup, n_mod_since_analyze, last_vacuum, last_autovacuum, last_analyze, last_autoanalyze, vacuum_count, autovacuum_count, analyze_count, autoanalyze_count, index_size_bytes, table_size_bytes}` | `datname`, `schemaname`, `relname` |
| `stat_wal_receiver` | disabled | `pg_stat_wal_receiver_{receive_start_lsn, receive_start_tli, flushed_lsn, received_tli, last_msg_send_time, last_msg_receipt_time, latest_end_lsn, latest_end_time, upstream_node}` | — |
| `statio_user_indexes` | disabled | `pg_statio_user_indexes_idx_blks_read_total`, `pg_statio_user_indexes_idx_blks_hit_total` | — |
| `statio_user_tables` | enabled | `pg_statio_user_tables_{heap_blocks_read, heap_blocks_hit, idx_blocks_read, idx_blocks_hit, toast_blocks_read, toast_blocks_hit, tidx_blocks_read, tidx_blocks_hit}` | `datname`, `schemaname`, `relname` |
| `wal` | enabled | `pg_wal_segments`, `pg_wal_size_bytes` | — |
| `xlog_location` | disabled | `pg_xlog_location_bytes` | — |
| `aurora_global_db_instance_status` | disabled (Aurora) | `pg_aurora_global_db_instance_status_visibility_lag_seconds` | `server_id`, `aws_region` |
| `aurora_global_db_status` | disabled (Aurora) | `pg_aurora_global_db_status_{durability_lag_seconds, rpo_lag_seconds}` | `aws_region` |
| `aurora_instance` | disabled (Aurora) | `pg_aurora_instance_info` | `server_id`, `version` |
| `aurora_replica_status` | disabled (Aurora) | `pg_aurora_replica_status_{lag_seconds, replay_latency_seconds, pending_read_ios}` | `server_id` |
| `aurora_stat_bgwriter` | disabled (Aurora) | `pg_aurora_stat_bgwriter_{orcache_blocks_written_total, orcache_block_write_time_seconds_total}` | — |
| `aurora_stat_database` | disabled (Aurora) | `pg_aurora_stat_database_{storage_blocks_read_total, orcache_blocks_hit_total, local_blocks_read_total, storage_block_read_time_seconds_total, orcache_block_read_time_seconds_total, local_block_read_time_seconds_total}` | `datid`, `datname` |
| `aurora_stat_dml_activity` | disabled (Aurora) | `pg_aurora_stat_dml_activity_{operations_total, latency_seconds_total}` | `datid`, `datname`, `operation` |
| `aurora_stat_get_db_commit_latency` | disabled (Aurora) | `pg_aurora_stat_get_db_commit_latency_seconds_total` | `datid`, `datname` |
| `aurora_stat_logical_wal_cache` | disabled (Aurora) | `pg_aurora_stat_logical_wal_cache_{cache_hits_total, cache_misses_total, blocks_read_total}` | `slot_name` |
| `aurora_stat_optimized_reads_cache` | disabled (Aurora) | `pg_aurora_stat_optimized_reads_cache_{total_size_bytes, used_size_bytes}` | — |

**Views the brief asked about that postgres_exporter 0.21.1 also does NOT collect:** `pg_stat_io`
(docs state explicitly: *"the checkpoint ones live in `pg_stat_io`, which this exporter does not
currently collect"*), `pg_stat_slru`, `pg_stat_gssapi`, `pg_stat_xact_*`, `pg_stat_database_conflicts`
(as a separate source — `conflicts` is folded into `stat_database`), `pg_stat_user_functions`,
`pg_stat_all_tables`, `pg_stat_all_indexes`, tablespaces, and `pg_stat_progress_*` other than
`vacuum`.

**`stat_statements` sub-flags** (`docs/configuration.md`) — the direct input for a Query Insights
product:

| Flag | Default | Effect |
|---|---|---|
| `--collector.stat_statements.include_query` | off | Emits `pg_stat_statements_query_id` mapping each query ID to its query text, so IDs can be joined back to queries. Off by default because the text may contain literals |
| `--collector.stat_statements.query_length` | 120 | Max length of the query text above, in characters |
| `--collector.stat_statements.limit` | 100 | Max number of statements returned per scrape, ordered by total execution time. **The main lever on cardinality** |
| `--collector.stat_statements.exclude_databases` | none | Comma-separated DB names to skip |
| `--collector.stat_statements.exclude_users` | none | Comma-separated user names to skip — useful for excluding the monitoring role's own queries |

**Common flags:** `--config.file` (`postgres_exporter.yml`), `--collection-timeout` (`1m`),
`--wrap-large-counters` (`true`, wraps 64-bit counters at 2^53 to avoid float rounding).
**Toolkit flags:** `--web.telemetry-path` (`/metrics`), `--web.max-requests` (`40`; excess → 503),
`--web.disable-exporter-metrics` (`false`), `--web.listen-address` (`:9187`, repeatable),
`--web.config.file`, `--web.systemd-socket` (Linux only).
**Logging:** `--log.level` (`debug`\|`info`\|`warn`\|`error`, default `info`), `--log.format`
(`logfmt`\|`json`).
**Config file** (only needed for multi-target `/probe` auth):
```yaml
auth_modules:
  foo: # referenced as ?auth_module=foo on /probe requests
    type: userpass
    userpass: {username: monitoring_user, password: monitoring_password}
    options: {sslmode: require}   # merged into the DSN as key=value query params
```
Reload without restart: `SIGHUP` or `POST /-/reload`.
**Deprecated options:** `--auto-discover-databases`, `--exclude-databases`, `--include-databases`,
`--extend.query-path`, `--disable-default-metrics`, `--constantLabels`. `PG_EXPORTER_*` env vars are
deprecated, **removed in v0.22.0**. CI-tested PG versions: 13, 14, 15, 16, 17, 18.
Collectors fail independently: if one errors (missing extension, unsupported version, insufficient
privileges), its metrics are absent and the error is logged, but the rest of the scrape succeeds.

---

## 5. CNPG-specific instance-manager metrics (Go collector)

From `pkg/management/postgres/webserver/metricserver/pg_collector.go`.
`const PrometheusNamespace = "cnpg"`; `subsystem := "collector"` in `newMetrics()` → every
Go-emitted metric is `cnpg_collector_*`. (`Describe()`/`Collect()` also delegate to
`e.queries.Describe/Collect` for §4.10 and `e.pluginCollector.Describe/Collect` for cnpg-i plugins.)

| Go field | Metric name | HELP (verbatim) | Labels | Type |
|---|---|---|---|---|
| `CollectionsTotal` | `cnpg_collector_collections_total` | "Total number of times PostgreSQL was accessed for metrics." | — | counter |
| `PgCollectionErrors` | `cnpg_collector_collection_errors_total` | "Total errors occurred accessing PostgreSQL for metrics." | `collector` | counter vec |
| `Error` | `cnpg_collector_last_collection_error` | "1 if the last collection ended with error, 0 otherwise." | — | gauge |
| `PostgreSQLUp` | **`cnpg_collector_up`** | "1 if PostgreSQL is up, 0 otherwise." | `cluster` | gauge |
| `CollectionDuration` | `cnpg_collector_collection_duration_seconds` | "Collection time duration in seconds" | `collector` | summary/histogram |
| `SwitchoverRequired` | `cnpg_collector_manual_switchover_required` | "1 if a manual switchover is required, 0 otherwise" | — | gauge |
| `SyncReplicas` | `cnpg_collector_sync_replicas` | "Number of requested synchronous replicas (synchronous_standby_names)" | `value` | gauge |
| `ReplicaCluster` | `cnpg_collector_replica_mode` | "1 if the cluster is in replica mode, 0 otherwise" | — | gauge |
| `PgWALArchiveStatus` | `cnpg_collector_pg_wal_archive_status` | "Number of WAL segments in the '<specs.PgWalArchiveStatusPath>' directory (ready, done)" | `value` ∈ `ready`, `done` | gauge |
| `PgVersion` | `cnpg_collector_postgres_version` | "Postgres version" | `full`, `cluster` | gauge |
| `PgWALDirectory` | **`cnpg_collector_pg_wal`** | "Total size in bytes of WAL segments in the '<specs.PgWalPath>' directory computed as (wal_segment_size * count)" | `value` | gauge |
| `FirstRecoverabilityPoint` | `cnpg_collector_first_recoverability_point` | "…as a unix timestamp (Deprecated)" | — | gauge |
| `LastAvailableBackupTimestamp` | `cnpg_collector_last_available_backup_timestamp` | "(Deprecated)" | — | gauge |
| `LastFailedBackupTimestamp` | `cnpg_collector_last_failed_backup_timestamp` | "(Deprecated)" | — | gauge |
| `FencingOn` | `cnpg_collector_fencing_on` | "1 if the instance is fenced, 0 otherwise" | — | gauge |
| `NodesUsed` | `cnpg_collector_nodes_used` | distinct nodes hosting cluster pods; `-1` = unavailable, `1` = no HA | — | gauge |
| `PgStatWalMetrics` (PG 14+; last four PG 14–17 only) | `cnpg_collector_wal_records` | from `pg_stat_wal` | `stats_reset` | gauge |
| | `cnpg_collector_wal_fpi` | | `stats_reset` | gauge |
| | `cnpg_collector_wal_bytes` | | `stats_reset` | gauge |
| | `cnpg_collector_wal_buffers_full` | | `stats_reset` | gauge |
| | `cnpg_collector_wal_write` | (PG 14–17) | `stats_reset` | gauge |
| | `cnpg_collector_wal_sync` | (PG 14–17) | `stats_reset` | gauge |
| | `cnpg_collector_wal_write_time` | (PG 14–17) | `stats_reset` | gauge |
| | `cnpg_collector_wal_sync_time` | (PG 14–17) | `stats_reset` | gauge |

### 5.1 `cnpg_collector_pg_wal` value set (`metricserver/wal.go`)

`collectPGWalSettings` sets `cnpg_collector_pg_wal` with `value` ∈
**`count`, `size`, `min`, `max`, `keep`, `slots_max`, `volume_max`, `volume_size`**.
Implementation: reads `specs.PgWalPath`, matches segment files with regex `^[0-9A-F]{24}$`, queries
`pg_settings` for `wal_segment_size, min_wal_size, max_wal_size, wal_keep_size, wal_keep_segments,
max_slot_wal_keep_size`; `getWalVolumeSize()` uses `cluster.ShouldCreateWalArchiveVolume()` /
`Spec.WalStorage.GetSizeOrNil()`. **This is the v1.30 replacement for the brief's `cnpg_wfile_*` /
`pg_wal_stat` names.** v1.30.2 fixed this collector to stop counting `.partial`, `.backup` and
`.history` files (#9967).

### 5.2 `collector` label values

Emitted by `Collect()`: `"Collect.up"`, `"Collect.PgWALArchiveStats"`, `"Collect.PGWalSettings"`,
`"Collect.PluginMetrics"`, and `"Collect." + e.queries.Name()`.

### 5.3 Cache + Go runtime

`cache_hits`, `cache_misses`, `last_update_timestamp` (query-set namespaced, §4.8).
`registry.Register(collectors.NewGoCollector())` → the standard `go_*` set (`go_goroutines`,
`go_memstats_*`, `go_gc_*`, …), visible in `docs/src/monitoring.md`'s sample output.
`cnpg_last_error` appears in the docs sample alongside `cnpg_collector_last_collection_error` —
**(unverified)** whether it is emitted by Go code or is a docs-only legacy alias.

### 5.4 PgBouncer metrics (`:9127`)

`pkg/management/pgbouncer/metricsserver/pgbouncer_collector.go`: `const PrometheusNamespace = "cnpg"`,
`subsystem := "pgbouncer"`. `consts.go`: `databaseLabel = "database"`, `userLabel = "user"`.

| Metric | HELP / source |
|---|---|
| `cnpg_pgbouncer_collections_total` | "Total number of times PostgreSQL was accessed for metrics." |
| `cnpg_pgbouncer_collection_errors_total` | |
| `cnpg_pgbouncer_up` | "1 if pgbouncer is up, 0 otherwise." |
| `cnpg_pgbouncer_last_collection_error` | |
| `cnpg_pgbouncer_collection_duration_seconds` | |

`NewShowListsMetrics(subsystem)` → subsystem **`pgbouncer_lists`**:
`cnpg_pgbouncer_lists_{databases, users, pools, free_clients, used_clients, login_clients,
free_servers}` (+ more — list truncated in source extraction, §0 item 2).

`NewShowPoolsMetrics` → **`cnpg_pgbouncer_pools_*`** (GaugeVec, labels `database` / `user`):
`cl_active`, `cl_waiting`, `cl_cancel_req`, `cl_active_cancel_req`, `cl_waiting_cancel_req`,
`sv_active`, `sv_active_cancel`, `sv_wait_cancels`, `sv_idle`, `sv_used`, `sv_tested`, `sv_login`.

`NewShowStatsMetrics` → **`cnpg_pgbouncer_stats_*`**: `total_bind_count`,
`total_client_parse_count`, `total_server_assignment_count`, `total_server_parse_count`,
`total_xact_count`, `total_query_count`, `total_received`, `total_sent`, `total_xact_time`,
`total_query_time`, `total_wait_time`, `avg_bind_count` (+ more — truncated, §0 item 2).

### 5.5 Sample output location

`docs/src/samples/monitoring/`: `kube-stack-config.yaml`, `prometheusrule.yaml`, `podmonitor.yaml`,
`alerts.yaml` (raw alert rules). The **Grafana dashboard JSON lives in a separate repo**:
`https://github.com/cloudnative-pg/grafana-dashboards/blob/main/charts/cluster/grafana-dashboard.json`.

---

## 6. kubectl cnpg plugin and official Grafana dashboards

**Repo-location corrections** (three more false premises in the brief):

| Assumed | Reality |
|---|---|
| `github.com/cloudnative-pg/kubectl-cnpg` | **HTTP 404.** The plugin lives in the main repo: `cloudnative-pg/cloudnative-pg` → `cmd/kubectl-cnpg/main.go` + `internal/cmd/plugin/**` |
| Dashboard JSON at `configs/monitoring/` | **No `configs/` dir.** The repo has `config/` (kustomize); `config/prometheus/` contains only `kustomization.yaml` + `monitor.yaml` (a PodMonitor). The Grafana JSON lives in a **separate repo**: `github.com/cloudnative-pg/grafana-dashboards` → `charts/cluster/grafana-dashboard.json` (253,385 bytes) |
| Alert rules in `config/prometheus/` | They live in `docs/src/samples/monitoring/` — two files: `prometheusrule.yaml` (current) and `alerts.yaml` (legacy "raw" source) |
| `github.com/cloudnative-pg/postgresql-ha` | 404 (that's Bitnami). The CNPG charts repo has exactly 3 charts |
| `cloudnative-pg.io/documentation/current/*` | 301/meta-refresh → `https://cloudnative-pg.io/docs/current/*` ("documentation has moved") |

### 6.1 Command tree

From `cmd/kubectl-cnpg/main.go`:
```go
rootCmd := &cobra.Command{
    Use:   "kubectl-cnpg",
    Short: "A plugin to manage your CloudNativePG clusters",
    Annotations: map[string]string{
        cobra.CommandDisplayNameAnnotation: "kubectl cnpg",
    },
    SilenceUsage: true,
}
```
Cobra **command groups** (`internal/cmd/plugin/plugin.go`):

| GroupID | Title |
|---|---|
| `admin` | Operator-level administration |
| `troubleshooting` | Troubleshooting |
| `cluster` | Cluster administration |
| `db` | Database administration |
| `misc` | Miscellaneous |

Registered subcommands, in source order — **this is the complete tree: 21 commands + cobra's
built-in `completion`**:
```go
subcommands := []*cobra.Command{
    backup.NewCmd(), certificate.NewCmd(), destroy.NewCmd(), fence.NewCmd(),
    fio.NewCmd(), hibernate.NewCmd(), install.NewCmd(), logs.NewCmd(),
    maintenance.NewCmd(), pgadmin.NewCmd(), pgbench.NewCmd(), promote.NewCmd(),
    psql.NewCmd(), publication.NewCmd(), reload.NewCmd(), report.NewCmd(),
    restart.NewCmd(), snapshot.NewCmd(), status.NewCmd(), subscription.NewCmd(),
    versions.NewCmd(),
}
for _, cmd := range subcommands { plugin.AddColorControlFlag(cmd); rootCmd.AddCommand(cmd) }
```

| Command | Group | Description | Key flags |
|---|---|---|---|
| `backup CLUSTER` | db | Request an on-demand backup | `--backup-name` (default `CLUSTER-CURRENT_TIMESTAMP`), `-t/--backup-target` (`primary`\|`prefer-standby`), `-m/--method` (incl. `volumeSnapshot`, `plugin`), `--online` (`true\|false\|""`), `--immediate-checkpoint` (`true\|false\|""`), `--wait-for-archive` (`true\|false\|""`), `--plugin-name`, `--plugin-parameters` (`k=v,k=v`), `--dry-run`. **No `--wait` flag** |
| `certificate [secretName]` | db | Create a client cert for TLS/certificate auth | `--cnpg-user` (req), `--cnpg-cluster` (req), `-o/--output` (`json\|yaml`), `--dry-run`. **No `generate` subcommand** |
| `completion` | — | Shell completion (cobra built-in) | `--bash/--zsh/--fish/--powershell` |
| `destroy CLUSTER INSTANCE` | db | Destroy an instance and its PVC | `-k/--keep-pvc` (keeps PVCs, strips ownerReferences, flips `cnpg.io/pvcStatus` `ready`→`detached`) |
| `fencing on CLUSTER INSTANCE` | cluster | Fence an instance | numeric instance arg expands to `CLUSTER-N` |
| `fencing off CLUSTER INSTANCE` | cluster | Remove fence | **no `fencing status`** |
| `fio [name]` | misc | Creates a fio deployment, PVC and ConfigMap | `--storageClass`, `--pvcSize` (default `2Gi`), `--dry-run` |
| `hibernate on CLUSTER` | cluster | Hibernates a cluster (patches `cnpg.io/hibernation`) | errors `cluster %s is already in the requested state` |
| `hibernate off CLUSTER` | cluster | Resumes a hibernated cluster | **no `hibernate status`** |
| `install generate` | admin | Generates the YAML manifests to install the operator | `--version` (default plugin version), `--watch-namespace` (csv), `--replicas` (0 = no override), `--image`, `--log-field-level` (default `level`), `--log-field-timestamp` (default `ts`), `--control-plane` |
| `logs cluster CLUSTER` | troubleshooting | Logs for cluster's pods | `-o/--output` (file), `-t/--timestamps`, `--tail` (int64, default -1), `-f/--follow` (docs note: unlike other cnpg commands `-f` = follow, not file) |
| `logs pretty` | troubleshooting | Pretty-print a JSON CNPG log stream on stdin, sorted by timestamp | `--pods`, `--loggers`, `--log-level`, `--sorting-group-size` (default 1000) |
| `maintenance set CLUSTER` | cluster | Set `.spec.nodeMaintenanceWindow.inProgress` | `-A/--all-namespaces`, `--reusePVC`, `-y/--yes`; `cobra.MaximumNArgs(1)` |
| `maintenance unset CLUSTER` | cluster | Unset it | same |
| `pgadmin4 [name]` | misc | Creates a pgAdmin deployment | `--dry-run`, `--mode` (`server` default \| `desktop` insecure), `--image` (default `dpage/pgadmin4:latest`). Docs: **"Never deploy pgAdmin in production using the plugin."** |
| `pgbench CLUSTER [-- PGBENCH_ARGS...]` | misc | Creates a pgbench job | `--job-name`, **`--pgbench-job-name` → `MarkDeprecated: "use job-name instead"`**, `--db-name` (default `app`), `--ttl` (int32, default 0), `--dry-run`, `--node-selector` (stringSlice) |
| `promote CLUSTER INSTANCE` | db | Promote an instance to primary | no flags |
| `psql CLUSTER [-- PSQL_ARGS...]` | misc | Start a psql session against a cluster | `--replica` (first replica), `-t/--tty` (default true), `-i/--stdin` (default true) |
| `publication create` | db | Create a logical replication publication | `--publication`, `--external-cluster`, `--all-tables`, `--table`, `--schema`, `--dry-run` |
| `publication drop` | db | Drop a publication | `--publication`, `--external-cluster`, `--dry-run` |
| `reload CLUSTER` | db | Reload a cluster | no flags |
| `report operator` | troubleshooting | Report operator deployment, pod, events, logs (opt-in) | `-f/--file`, `-o/--output` (default `yaml`), `-S/--stopRedaction`, `-l/--logs`, `-t/--timestamps` |
| `report cluster CLUSTER` | troubleshooting | Report cluster resources, pods, events, logs (opt-in) | `-f/--file`, `-o/--output` (yaml), `-l/--logs`, `-t/--timestamps` (`-S` disabled here) |
| `restart CLUSTER [INSTANCE]` | db | Restart a cluster or one instance | no flags |
| `snapshot CLUSTER` | db | **DEPRECATED** (use `backup -m volumeSnapshot`); docs: "The `kubectl cnpg snapshot` command has been removed." | — |
| `status CLUSTER` | db | Get the status of a Cluster | §6.2 |
| `subscription create` | db | Create a logical replication subscription | `--subscription`, `--publication`, `--external-cluster` |
| `subscription drop` | db | Drop a subscription | `--subscription`, `--external-cluster` |
| `subscription sync-sequences` | db | Sync sequences from the publisher | `--subscription`, `--external-cluster` |
| `version` | — | Prints version, commit sha and date of the build | note: **`version`**, not `versions` |

**Global/persistent flags**: `logFlags` (`-v` verbosity, log format / json-field flags from
`github.com/cloudnative-pg/machinery/pkg/log`) + `configFlags` from
`genericclioptions.NewConfigFlags(true)` → `-n/--namespace`, `--context`, `--kubeconfig`,
`-s/--server`, `--token`, `--certificate-authority`, `--client-certificate`, `--client-key`,
`--cluster`, `--request-timeout`, `--as`, `--as-group`, `--as-uid`, `--cache-dir`,
`--allow-missing-template-keys`, `--raw`, `--chunk-size`. Plus `--color` (`AddColorControlFlag` on
every subcommand; docs show `--color=never`).

**Commands the brief listed that DO NOT EXIST**: `promote-pods`, `start`, `stop`, `suspend`,
`unsuspend`, `rescale`, `scale`, `restore`, `restore-backup`, `list-backups`, `backups`,
`generate` / `manifest generate-cluster`, `set`, `exec`, `sql`, `monitor`, `upgrade`, `annotate`,
`rebalance`, `add-replica`, `remove-replica`, `bundle`, `must-gather`, `instance`, `plugin`.
Naming corrections: `fence` → **`fencing`**; `pgadmin` → **`pgadmin4`**; `versions` → **`version`**;
`backup --wait` → **no such flag** (it's `--wait-for-archive`).

Install: `curl -sSfL https://github.com/cloudnative-pg/cloudnative-pg/raw/main/hack/install-cnpg-plugin.sh | sudo sh -s -- -b /usr/local/bin`
(also Debian/RPM/AUR/Krew/Homebrew packages). K9s integration:
`https://github.com/derailed/k9s/blob/master/plugins/cloudnative-pg.yaml`.

### 6.2 `kubectl cnpg status` — exact output structure

The section an insights product duplicates. From `internal/cmd/plugin/status/cmd.go`:
```go
statusCmd.Flags().CountP("verbose", "v", "Increase verbosity to display more information")
statusCmd.Flags().StringP("output", "o", "text", "Output format. One of text|json")
statusCmd.Flags().DurationP("timeout", "t", 10*time.Second, "Timeout for operations that access pod filesystems (e.g., du, cat)")
```
**No `--logs`, no `--raw`, no `--watch`.** Namespace comes from the global `-n`. Docs: for >1TB
clusters the `du`/`cat` exec can exceed the 10s default → use `--timeout 45s`.

Print order, from `Status()` in `status.go` (1634 lines):
```go
status.printBasicInfo(ctx, clientInterface, timeout)
status.printHibernationInfo()
status.printDemotionTokenInfo()
status.printPromotionTokenInfo()
if verbosity > 1 {
    errs = append(errs, status.printPostgresConfiguration(ctx, clientInterface, timeout)...)
    status.printCertificatesStatus()
}
if !hibernated {
    status.printBackupStatus()
    status.printBasebackupStatus(verbosity)
    status.printReplicaStatus(verbosity)
    if verbosity > 0 {
        status.printUnmanagedReplicationSlotStatus()
        status.printRoleManagerStatus()
        status.printTablespacesStatus()
        status.printPodDisruptionBudgetStatus()
    }
    status.printInstancesStatus()
    status.printFailureDomainStatus()
}
status.printPluginStatus(verbosity)
// then a red "Error(s) extracting status" table if errs
```

| Section | Verbosity | Contents |
|---|---|---|
| `Cluster Summary` (green) / `Replica Cluster Summary` (yellow) | 0 | `Name`, `System ID:`, `PostgreSQL Image:`, `Designated primary:` (+ `Source cluster:` for replica clusters) or `Primary instance:`, `Primary promotion time:`, `Status:` (`Hibernated` \| `Primary instance is fenced` \| e.g. `Cluster in healthy state`), `Instances:`, `Ready instances:`, `Fenced instances:`, `Size:` (or `- (hibernated)` / `- (fenced)` / exec-error text), `Current Write LSN:` formatted `"%s (Timeline: %d - WAL File: %s)"`. Extra banners: `Primary server is initializing`, `Switchover in progress` |
| Hibernation / demotion token / promotion token | 0 | `printHibernationInfo`, `printDemotionTokenInfo`, `printPromotionTokenInfo` |
| `PostgreSQL Configuration` + `PostgreSQL HBA Rules` | **-vv** | rendered GUCs + parsed pg_hba |
| `Certificates Status` (red/yellow/green) | **-vv** | header `Certificate Name \| Expiration Date \| Days Left Until Expiration`; appends `- Expires Soon` (<7d) or `Expired` (<0) |
| `Continuous Backup status` / `Continuous Backup not configured` / `Continuous Backup status (Barman Cloud Plugin)` | 0 | |
| `Physical backups` / `No running physical backups found` | 0 | rows: `Restore Job`, `Progress`, `Transferred`, `Temporary`, `Catalog XMin` |
| `Streaming Replication status` (+ `Replication Slots Enabled`) | 0 | header `Name \| Sent LSN \| Write LSN \| Flush LSN \| Replay LSN \| Write Lag \| Flush Lag \| Replay Lag \| State \| Sync State \| Sync Priority \| Replication Slot`; **-v** adds `Slot Restart LSN \| Slot WAL Status \| Slot Safe WAL Size` |
| `Unmanaged Replication Slot Status` / `No unmanaged replication slots found` | -v | |
| `Managed roles status` / `No roles managed` / `Irreconcilable roles` | -v | |
| `Tablespaces status` / `No managed tablespaces` | -v | |
| `Pod Disruption Budgets status` / `No active PodDisruptionBudgets found` | -v | header `Name \| Role \| Expected Pods \| Current Healthy \| Minimum Desired Healthy \| Disruptions Allowed` |
| **`All Instances`** | 0 | header `Name \| Current LSN \| Replication role \| Status \| QoS \| Manager Version \| Node`. Role strings: `Primary`, `Fenced`, `Standby (async)`, `Standby (sync)`, `Standby (potential sync)`, `Standby (file based)`, `Standby (in Replica Cluster)`, `Standby (paused)`, `Standby (starting up)`, `Standby (pg_rewind)`. Status: `OK` or `OK (pending restart)`. Other strings: `Started at`, `Starting Up`, `Not Available`, `Not available yet`, `Message`, `Failing`, `License` |
| `Failure Domain Topology` | 0 | header `Domain \| Instances \| Primary Domain`; also `Topology labels could not be extracted from pods or nodes`, `No topology information available yet` |
| `Plugins status` / `No plugins found` | 0 | header `Name \| Version \| Status \| Reported Operator Capabilities`; capability strings → `Backup Service`, `Restore Job`, `Reconciler Hooks`, `WAL Service`, `Operator Service`, `Lifecycle Service`, `Postgres Service` |

**RBAC for `status`**: `clusters:get`, `pods:list`, `pods/exec:create` (optional),
`pods/proxy:create`, `PDBs:list`, `objectstores.barmancloud.cnpg.io:get`. Without `pods/exec`,
`status` still works but cluster size and (with `-v`) the PG config show as not permitted.

**`-o json` is the cheapest ingestion point for a product** — a typed, versioned snapshot of
everything the text renderer prints (§1.3 `ClusterStatus` + the per-section structures above).

### 6.3 Official Grafana dashboard — full panel inventory

- **Repo:** `github.com/cloudnative-pg/grafana-dashboards` → `charts/cluster/grafana-dashboard.json` (253,385 bytes)
- **title:** `CloudNativePG` · **uid:** `cloudnative-pg` · **id:** `null` · **schemaVersion:** 39 · **version:** 2 · **tags:** `['cloudnativepg']`
- **grafana.com dashboard ID: 20417** → `https://grafana.com/grafana/dashboards/20417-cloudnativepg/`
- Helm chart in that repo: `name: cluster`, `version: 0.0.5`, `appVersion: "1.16.0"`
- Pulled as a dependency of the operator chart (`charts/cloudnative-pg/Chart.yaml`):
  ```yaml
  dependencies:
    - name: cluster
      alias: monitoring
      condition: monitoring.grafanaDashboard.create
      version: "0.0"
      repository: https://cloudnative-pg.github.io/grafana-dashboards
  ```
  (`charts/cloudnative-pg/monitoring/grafana-dashboard.json` in the operator chart is a 198-byte stub.)
  Grafana sidecar label `grafana_dashboard: "1"`.
- `refresh: 30s`, time `now-7d`→`now`, `graphTooltip: 1`, `fiscalYearStartMonth: 0`.
- `__inputs`: `DS_PROMETHEUS` (pluginId `prometheus`), `DS_EXPRESSION` (pluginId `__expr__`).
- **Dashboard links:** one entry — type `dashboards`, title **"Related Dashboards"**, tag `cloudnativepg`.
- **Annotations:** built-in "Annotations & Alerts" (limit 100).

**Templating variables:**

| name | type | query | label |
|---|---|---|---|
| `DS_PROMETHEUS` | datasource | — | Datasource |
| `operatorNamespace` | query | `label_values(controller_runtime_webhook_requests_total{webhook="/mutate-postgresql-cnpg-io-v1-c..."}, namespace)` | Operator Namespace |
| `namespace` | query | `label_values(cnpg_collector_up, namespace)` | Database Namespace |
| `cluster` | query | `cnpg_collector_up{namespace=~"$namespace"}` | Cluster |
| `instances` | query | `cnpg_collector_up{namespace=~"$namespace",pod=~"$cluster-([1-9][0-9]*)$"}` | Instances |

**66 top-level panels.**

*Header block (y=0–5, not inside a row)* — `[alertlist] Alerts` (y0 x0 w3 h7, the only alertlist
panel) + `[text]` headers `Health` (x3 w4), `Overview` (x7 w12), `Storage` (x19 w2), `Backups` (x21 w3).

Health column (`[stat]`):
1. `(max(cnpg_pg_replication_streaming_replicas) - sum(cnpg_pg_replication_is_wal_receiver_up))`
2. `max(cnpg_pg_replication_lag) + max(cnpg_pg_stat_replication_write_lag_seconds)`
3. `max((1 - kubelet_volume_stats_available_bytes{...} / kubelet_volume_stats_capacity_bytes{...}))`
4. `time() - max({__name__=~"cnpg_collector_last_available_backup_timestamp|barman_cloud_cloudnative_pg_io_last_available_backup_timestamp"})`
5. `max((1 - cnpg_pg_replication_in_recovery) * (time() - timestamp(cnpg_pg_stat_archiver_seconds_since_last_archival)))`
6. `sum(kube_pod_status_ready{namespace="$operatorNamespace", pod=~"cloudnative-pg.+|cnpg-controller-manager.+", condition="true"})`
7. `clamp_max(controller_runtime_reconcile_total{result="error",controller="backup"},1) || clamp_max(...controller="cluster") || clamp_max(...controller="pooler") || clamp_max(...controller=~"scheduledbackup|scheduled-backup")`

Overview column:
8. `[stat] Last failover` — `max(cnpg_pg_postmaster_start_time)*1000`
9. `[stat] TPS` — `sum(rate(cnpg_pg_stat_database_xact_commit[$__rate_interval])) + sum(rate(cnpg_pg_stat_database_xact_rollback[...]))`
10. `[gauge] CPU Utilisation` — `sum(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{...}) / sum(kube_pod_container_resource_requests{...})`
11. `[gauge] Memory Utilisation` — `sum(container_memory_working_set_bytes{job="kubelet", metrics_path="/metrics/cadvisor",...}) / sum(...)`
12. `[stat] Replication Lag` — `max(cnpg_pg_replication_lag)`
13. `[stat] Write Lag` — `max(cnpg_pg_stat_replication_write_lag_seconds)`
14. `[stat] Version` — `cnpg_collector_postgres_version`
15. `[stat] Flush Lag` — `max(cnpg_pg_stat_replication_flush_lag_seconds)`
16. `[stat] Replay Lag` — `max(cnpg_pg_stat_replication_replay_lag_seconds)`
17. `[stat]` (untitled) — `(sum(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{...}) / sum(kube_pod_container_resource_requests{...}))`
18. `[stat]` (untitled) — `(max(sum by (pod) (cnpg_backends_total{...}) / sum by (pod) (cnpg_pg_settings_setting{name="max_connections",...})))`

Storage column:
19. `[gauge] Volume Space Usage` — 3 targets: PGDATA pvc, `(${instances})-wal` pvc, `(${instances})-tbs.*` tablespaces

Backups column (`[stat]`):
20. `Last Base Backup` — `-(time() - max({__name__=~"cnpg_collector_last_available_backup_timestamp|barman_cloud_cloudnative_pg_io_last_available_backup_timestamp"}))`
21. `Last archived WAL` — `max((1 - cnpg_pg_replication_in_recovery) * (time() - timestamp(cnpg_pg_stat_archiver_seconds_since_last_archival)))`
22. `Database Size` — `cnpg_pg_database_size_bytes{namespace="$namespace", pod=~"$instances"}`
23. `First Recoverability Point` — `max({__name__=~"cnpg_collector_first_recoverability_point|barman_cloud_cloudnative_pg_io_first_recoverability_point"})`

**Row `Server Health`** (y=7, w24 h1, **expanded**) — label row (y=8, all `[text]`): `Instance`,
`Status`, `Clustering / replicas`, `Zone`, `Connections`, `Max Connections`, `Wraparound`,
`Started`, `Version`, each targeting
`kube_pod_container_status_ready{container="postgres",namespace=~"$namespace",pod=~"$instances"}`.
Value row (y=9): `[text]` raw HTML `<table ...><td ...><p style="font-weight: bold;">$instances</p>`;
`[stat] min(kube_pod_container_status_ready{container="postgres",...})`;
`[stat] 1 - cnpg_pg_replication_in_recovery + cnpg_pg_replication_is_wal_receiver_up`;
`[stat] cnpg_pg_replication_streaming_replicas`;
`[stat] kube_pod_info{...} * on(node,instance) group_left(label_topology_kubernetes_io_zone) kube_node_labels`;
`[timeseries] sum by (pod) (cnpg_backends_total{...})`;
`[gauge] 100 * sum by (pod) (cnpg_backends_total{...}) / sum by (pod) (cnpg_pg_settings_setting{name="max_connections",...})`;
`[bargauge] max by (pod) (cnpg_pg_database_xid_age{...})`;
`[stat] cnpg_pg_postmaster_start_time{...}*1000`; `[stat] cnpg_collector_postgres_version{...}`.

**Row `Configuration`** (y=18, **collapsed, 17 children**) — `[text]` headers `Instance`, `Max
Connections`, `Shared Buffers`, `Effective Cache Size`, `Work Mem`, `Maintenance Work Mem`,
`Random Page Cost`, `Sequential Page Cost`, one blank; then `[stat]` panels on
`cnpg_pg_settings_setting{name="max_connections"}`,
`max by (pod) (cnpg_pg_settings_setting{name="shared_buffers"}) * max by (pod) (...{name="block_size"})`,
`... {name="effective_cache_size"} * ...{name="block_size"}`, `{name="work_mem"} * 1024`,
`{name="maintenance_work_mem"} * 1024`, `{name="random_page_cost"}`, `{name="seq_page_cost"}`;
plus `[table] Configurations` — `cnpg_pg_settings_setting{namespace=~"$namespace",pod=~"$instances"}`.

**Row `Operational Stats`** (y=19, **expanded**):
- `[timeseries] CPU Usage` — `sum(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{pod=~"$instances", namespace=~"$namespace"}) by (pod)`
- `[timeseries] Memory Usage (container memory working set)` — `sum(container_memory_working_set_bytes{pod=~"$instances", namespace="$namespace", container!="", image!=""}) by (pod)`
- `[timeseries] Session States` — `sum(cnpg_backends_total{...}) by (pod)` + `... by (state, pod)`
- `[timeseries] Transactions [5m]` — `sum(rate(cnpg_pg_stat_database_xact_commit{...}[5m])) by (pod)` + rollback
- `[timeseries] Longest Transaction` — `max by (pod) (cnpg_backends_max_tx_duration_seconds{...})`
- `[timeseries] Deadlocks [5m]` — `rate(cnpg_pg_stat_database_deadlocks{datname="",...}[5m])`
- `[timeseries] Blocked Queries` — `cnpg_backends_waiting_total{...}`

**Row `Storage & I/O`** (y=51, **collapsed, 7 children**) — `[gauge] Volume Space Usage: PGDATA and
WAL`, `[gauge] Volume Inode Usage: PGDATA and WAL`, `[gauge] Volume Space Usage: Tablespaces`,
`[timeseries] Tuple I/O [5m]` (`cnpg_pg_stat_database_{tup_deleted,tup_inserted,tup_fetched,tup_returned,tup_updated}`),
`[timeseries] Block I/O [5m]` (`cnpg_pg_stat_database_blks_hit`, `blks_read`),
`[timeseries] Database Size` (`max by (datname) (cnpg_pg_database_size_bytes{datname!~"template.*",datname!="postgres",...})`),
`[timeseries] Temp Bytes [5m]` (`rate(cnpg_pg_stat_database_temp_bytes{...}[5m])`).

**Row `Write Ahead Log`** (y=52, **collapsed, 4 children**) — `[timeseries] WAL Segment Archive
Status` (`cnpg_collector_pg_wal_archive_status{value="ready"}` + `{value="done"}`),
`[timeseries] Archiver Status [5m]` (`rate(cnpg_pg_stat_archiver_archived_count[5m])` + `failed_count`),
`[timeseries] Last Archive Age` (`cnpg_pg_stat_archiver_seconds_since_last_archival`),
`[timeseries] WAL Count` (`cnpg_collector_pg_wal{..., value="count"}`).

**Row `Replication`** (y=53, **collapsed, 4 children**) — `[timeseries] Replication Lag`,
`Write Lag`, `Flush Lag`, `Replay Lag`.

**Row `Collector Stats`** (y=54, **collapsed, 2 children**) — `[heatmap] Collection Duration`
(`cnpg_collector_collection_duration_seconds`), `[timeseries] Errors`
(`cnpg_collector_last_collection_error`).

**Row `Backups`** (y=55, **collapsed, 1 child**) — `[timeseries] First Recoverability Point`.

**Row `Checkpoints`** (y=56, **collapsed, 2 children**) — `[timeseries] Requested/Timed`
(`rate({__name__=~"cnpg_pg_stat_(bgwriter|checkpointer)_checkpoints_req"}[$__rate_interval])` +
`..._checkpoints_timed`), `[timeseries] Write/Sync time`
(`rate({__name__=~"cnpg_pg_stat_(bgwriter_checkpoint|checkpointer)_write_time"})` + `..._sync_time`).

**Row `Extensions`** (y=57, **collapsed, 1 child**) — `[table] Installed extensions`
(`max(cnpg_pg_extensions_update_available{pod=~"$instances", namespace=~"$namespace"}) by (datname, extname, default_version, installed_version)`).

**Row `Operator`** (y=58, **collapsed, 10 children**) — 5 `[stat]` (ready operator pods; reconcile
error counts for `controller=cluster` / `backup` / `scheduledbackup` / `pooler`) + `[timeseries]
Ready Operator Pods`, `Cluster Reconcile Errors`, `Backup Reconcile Errors`, `Scheduled Backup
Reconcile Errors`, `Pooler Reconcile Errors` — all on
`controller_runtime_reconcile_total{namespace=~"$operatorNamespace", result="error", controller="..."}`.

### 6.4 PrometheusRule alert rules — exact names, expressions, severity

**`docs/src/samples/monitoring/prometheusrule.yaml`** (2541 b) — CURRENT.
`apiVersion: monitoring.coreos.com/v1`, `kind: PrometheusRule`, `metadata.name: cnpg-default-alerts`,
**one group: `cnp-default.rules`**, every rule `for: 1m`, every rule `severity: warning`.

| Alert | expr | severity | for |
|---|---|---|---|
| `LongRunningTransaction` | `cnpg_backends_max_tx_duration_seconds > 300` | warning | 1m |
| `BackendsWaiting` | `cnpg_backends_waiting_total > 300` | warning | 1m |
| `PGDatabaseXidAge` | `cnpg_pg_database_xid_age > 300000000` | warning | 1m |
| `PGReplication` | `cnpg_pg_replication_lag > 300` | warning | 1m |
| `LastFailedArchiveTime` | `(cnpg_pg_stat_archiver_last_failed_time - cnpg_pg_stat_archiver_last_archived_time) > 1` `and on(namespace, pod) cnpg_pg_replication_in_recovery == 0` | warning | 1m |
| `DatabaseDeadlockConflicts` | `increase(cnpg_pg_stat_database_deadlocks[1h]) > 10` | warning | 1m |
| `ReplicaFailingReplication` | `cnpg_pg_replication_in_recovery > cnpg_pg_replication_is_wal_receiver_up` | warning | 1m |

**`docs/src/samples/monitoring/alerts.yaml`** (2207 b) — legacy "raw" source. Same group
`cnp-default.rules`, 7 alerts, all `for: 1m`, `severity: warning`. Deltas: alert named **`PGDatabase`**
(not `PGDatabaseXidAge`); `DatabaseDeadlockConflicts` = `cnpg_pg_stat_database_deadlocks > 10` (no
`increase(...[1h])`); `LastFailedArchiveTime` lacks the `and on(namespace, pod) ... == 0` clause.
Docs: *"In addition, we provide the 'raw' sources for the Prometheus alert rules in the
`alerts.yaml` file."*

**Severity ceiling: all 7 are `warning`. There is no `critical` rule anywhere in the repo.**

Also in that dir: `podmonitor.yaml` (219 b), `kube-stack-config.yaml` (2122 b).
`config/prometheus/monitor.yaml` (338 b) is PodMonitor-only:
```yaml
spec:
  selector: {matchLabels: {app.kubernetes.io/name: cloudnative-pg}}
  podMetricsEndpoints:
    - port: metrics
```
Docs heading **"Deprecation of Automatic `PodMonitor` Creation"** — the operator no longer
auto-creates PodMonitors.

### 6.5 `github.com/cloudnative-pg/machinery`

*"Common library extracted from CloudNativePG and containing functions and types shared by multiple
projects"*. Homepage `https://pkg.go.dev/github.com/cloudnative-pg/machinery`, default branch `main`.
Root: `.github/ .gitignore .gitvote.yml .golangci.yml .release-please-manifest.json .spellcheck.yaml
.wordlist.txt CHANGELOG.md CODE_OF_CONDUCT.md CODEOWNERS COMPONENT_OWNERS.md CONTRIBUTING.md
GOVERNANCE.md LICENSE README.md Taskfile.yml commitlint.config.js dagger/ go.mod go.sum hack/ logo/
pkg/ release-please-config.json renovate.json5`.
Relevance here: `kubectl-cnpg` imports `github.com/cloudnative-pg/machinery/pkg/log` for `log.Flags`
(`logFlags.AddFlags(rootCmd.PersistentFlags())`) — which is why `kubectl cnpg` carries a global `-v`
verbosity + structured-log flag set alongside the kubectl `configFlags`. Also the source of the
`api/v1` types the operator imports. Release automation: release-please + Taskfile + dagger.

---

## 7. PostgreSQL version matrix for stats sources (PG 13 – 18)

Which stats view exists in which major version, and when each column first appeared. This is the
matrix an insights product needs in order to build one collector that works across PG 13–18 (the
target environment runs CNPG 1.30.0, which supports **PG 15–18**; PG 13/14 are included because
existing clusters and the postgres_exporter collector set still cover them).

**Sources:** `postgresql.org/docs/{13..18}/monitoring-stats.html`, `progress-reporting.html`,
`monitoring-locks.html`, `view-pg-locks.html`, `view-pg-settings.html`, `view-pg-file-settings.html`,
`functions-admin.html`, `functions-info.html`, `functions-statistics.html`, the
`contrib/pg_stat_statements/` docs plus `pg_stat_statements.control` from
`github.com/postgres/postgres@REL_XX_STABLE`, `src/backend/catalog/system_views.sql` per REL tag,
and `release-18.html`. Everything below is doc-verified unless marked **(unverified)**.

### 7.0 Corrections to the request's premises

1. **pg_stat_statements extension versions**: PG16 = **1.10** (same as PG15), PG17 = **1.11**,
   PG18 = **1.12** — not 1.11 / 1.12 / 1.13.
2. **`pg_stat_wal`** does not exist in PG13 (added PG14), and PG18 shrinks it to 5 columns.
3. **`pg_stat_io`** added PG16 (correct), but PG18 removes `op_bytes` and adds three byte columns.
4. **`pg_stat_bgwriter`** shrinks from 11 → 4 columns in PG17; checkpoint stats move to the new
   `pg_stat_checkpointer`.
5. **`blk_read_time` / `blk_write_time` in `pg_stat_statements`** were **removed in PG17**, not
   merely superseded.
6. **`pg_stat_ssl.compression`** removed in PG14 (PG13 has 9 cols, PG14+ has 8).
7. **`pg_stat_activity.query_id`** is a PG14 addition — PG13 has 21 columns, not 22.
8. **`pg_stat_database` session/active-time columns** are a PG14 addition — PG13 has 21 columns,
   not 28.
9. **`pg_stat_all_tables.last_seq_scan` / `last_idx_scan` / `n_tup_newpage_upd` / `t_ctid`** are
   PG16 additions; the four `total_*_time` columns are PG18 additions.
10. **`pg_stat_progress_copy`** is a PG14 view (not PG13); PG17 adds `tuples_skipped`.
11. **`pg_stat_progress_vacuum`** changed shape in PG17 (`max_dead_tuples` / `num_dead_tuples` →
    `max_dead_tuple_bytes` / `dead_tuple_bytes` / `num_dead_item_ids` + `indexes_total` /
    `indexes_processed`).
12. **`pg_stat_reset_shared` / `pg_stat_reset_slru`** signatures changed in PG17 to bracketed-default form.
13. **`stats_temp_directory`** existed through PG14 and was removed in PG15 (not PG13).
14. **`pg_stat_gssapi.credentials_delegated`** is a PG16 addition.
15. **`pg_locks.waitstart`** is a PG14 addition.

### 7.1 `pg_stat_statements` (contrib module)

**Extension `default_version` per PG release** (from `pg_stat_statements.control`):

| PG | 13 | 14 | 15 | 16 | 17 | 18 |
|---|---|---|---|---|---|---|
| version | **1.8** | **1.9** | **1.10** | **1.10** | **1.11** | **1.12** |

**Columns by version of first appearance** (● = present):

| Column | 13 | 14 | 15 | 16 | 17 | 18 | Notes |
|---|---|---|---|---|---|---|---|
| `userid`, `dbid` | ● | ● | ● | ● | ● | ● | `oid` |
| `queryid` | ● | ● | ● | ● | ● | ● | `bigint` |
| `query` | ● | ● | ● | ● | ● | ● | `text`; populated per `pg_stat_statements.showtext` (1.8) / `track_parameters` |
| `toplevel` | | ● | ● | ● | ● | ● | `boolean`, **added PG14** |
| `plans`, `total_plan_time`, `min_plan_time`, `max_plan_time`, `mean_plan_time`, `stddev_plan_time` | ● | ● | ● | ● | ● | ● | `double precision`, ms |
| `calls` | ● | ● | ● | ● | ● | ● | `bigint` |
| `total_exec_time`, `min_exec_time`, `max_exec_time`, `mean_exec_time`, `stddev_exec_time` | ● | ● | ● | ● | ● | ● | see the rename note below |
| `rows` | ● | ● | ● | ● | ● | ● | |
| `shared_blks_hit`, `shared_blks_read`, `shared_blks_dirtied`, `shared_blks_written` | ● | ● | ● | ● | ● | ● | |
| `local_blks_hit`, `local_blks_read`, `local_blks_dirtied`, `local_blks_written` | ● | ● | ● | ● | ● | ● | |
| `temp_blks_read`, `temp_blks_written` | ● | ● | ● | ● | ● | ● | |
| `blk_read_time`, `blk_write_time` | ● | ● | ● | ● | **removed** | | **removed PG17** |
| `temp_blk_read_time`, `temp_blk_write_time` | | | ● | ● | ● | ● | **added PG15** |
| `shared_blk_read_time`, `shared_blk_write_time` | | | | | ● | ● | **added PG17** (replaces `blk_read_time`/`blk_write_time`) |
| `local_blk_read_time`, `local_blk_write_time` | | | | | ● | ● | **added PG17** |
| `wal_records`, `wal_fpi`, `wal_bytes` | ● | ● | ● | ● | ● | ● | **WAL columns added PG13** (1.8) |
| `wal_buffers_full` | | | | | | ● | **added PG18** |
| `jit_functions`, `jit_generation_time`, `jit_inlining_count`, `jit_inlining_time`, `jit_optimization_count`, `jit_optimization_time`, `jit_emission_count`, `jit_emission_time` | | | ● | ● | ● | ● | **added PG15** |
| `jit_deform_count`, `jit_deform_time` | | | | | ● | ● | **added PG17** |
| `minmax_only` | | | | | ● | ● | `boolean`, **added PG17** |
| `stats_since` | | | | | ● | ● | `timestamptz`, **added PG17** |
| `minmax_stats_since` | | | | | ● | ● | `timestamptz`, **added PG17** |
| `parallel_workers_to_launch`, `parallel_workers_launched` | | | | | | ● | **added PG18** |

Column counts: **PG13 = 32**, **PG14 = 33**, **PG15 = 42**, **PG16 = 42** (no changes vs 15),
**PG17 = 48**, **PG18 = 51**.

**The `total_time` → `total_exec_time` rename.** ≤ PG12 (ext 1.7) had `total_time`, `min_time`,
`max_time`, `mean_time`, `stddev_time` (+ `query_plan`). **PG13** (ext 1.8) renamed them to
`total_exec_time` / `min_exec_time` / `max_exec_time` / `mean_exec_time` / `stddev_exec_time`,
**removed `query_plan`**, and **added `total_plan_time`** + plan min/max/mean/stddev. Any collector
that has to speak to PG12 and PG13 alike must branch on this.

**`pg_stat_statements_info`**: does **not** exist in PG13; exists **PG14+** with `dealloc`
(`bigint`) and `stats_reset` (`timestamptz`) — identical in 14, 15, 16, 17, 18.

**Function signatures:**

| Function | 13–16 | 17 | 18 |
|---|---|---|---|
| `pg_stat_statements()` | `( showtext boolean DEFAULT true ) → setof record` | same | same |
| `pg_stat_statements_reset()` | `( userid oid DEFAULT 0, dbid oid DEFAULT 0, queryid bigint DEFAULT 0 ) → void` | `( userid oid DEFAULT 0, dbid oid DEFAULT 0, queryid bigint DEFAULT 0, minmax_only boolean DEFAULT false ) → timestamp with time zone` | same as 17 |

PG17 changed the reset return type from `void` → `timestamptz` and added `minmax_only`.

### 7.2 `pg_stat_activity`

| Column | 13 | 14–18 |
|---|---|---|
| `datid`, `datname`, `pid`, `leader_pid`, `usesysid`, `usename`, `application_name`, `client_addr`, `client_hostname`, `client_port`, `backend_start`, `xact_start`, `query_start`, `state`, `state_change`, `wait_event_type`, `wait_event`, `backend_xid`, `backend_xmin`, `query`, `backend_type` | ● | ● |
| `query_id` | | ● **added PG14** |

Counts: **PG13 = 21**, **PG14–PG18 = 22**. No other column additions in 15/16/17/18.

**`state` domain**: `active`, `idle`, `idle in transaction`, `idle in transaction (aborted)`,
`fastpath function call`, `disabled` — all six in PG13–17; PG18 adds **`starting`**.

**`wait_event_type` domain** (identical PG13–PG18): `Activity`, `Backend`, `BufferPin`, `Client`,
`Extension`, `IO`, `IPC`, `Lock`, `LWLock`, `Timeout`, `InjectionPoint`. (`Backend` appears in the
PG14+ docs; the PG13 table lists the other ten.)

**`backend_type` domain**, verified per version:
- **PG13 (12)**: `autovacuum analyzer`, `autovacuum worker`, `background writer`, `checkpointer`,
  `client backend`, `io worker`, `logical replication launcher`, `parallel worker`, `startup`,
  `stats collector`, `wal writer`, `walsender`, `walreceiver`
- **PG14**: adds **`archiver`**
- **PG15**: `stats collector` removed (the statistics collector itself is gone)
- **PG16**: adds **`standalone backend`**; renames `autovacuum analyzer` → **`autovacuum analyze worker`**
- **PG17**: adds **`walsummarizer`**
- **PG18**: same set as PG17 (15 entries)

*(The `archiver` / `standalone backend` / `walsummarizer` additions are fully verified; the exact
PG13-vs-PG15 enumeration is the one item here flagged **partially reconstructed**.)*

**Related functions** (all versions unless noted): `pg_backend_pid()`, `pg_stat_get_activity(integer)`,
`pg_stat_get_backend_idset()`, `pg_stat_get_backend_pid(integer)`, `pg_stat_get_backend_activity(integer)`,
`pg_stat_get_backend_activity_start(integer)`, `pg_stat_get_backend_start(integer)`,
`pg_stat_get_backend_xact_start(integer)`, `pg_stat_get_backend_dbid(integer)`,
`pg_stat_get_backend_userid(integer)`, `pg_stat_get_backend_client_addr(integer)`,
`pg_stat_get_backend_client_port(integer)`, `pg_stat_get_backend_wait_event(integer)`,
`pg_stat_get_backend_wait_event_type(integer)`; **PG16** adds `pg_stat_get_backend_subxact(integer)`;
**PG18** adds `pg_stat_get_backend_io(integer)` (fields identical to `pg_stat_io`) and
`pg_stat_get_backend_wal(integer)` (fields identical to `pg_stat_wal`).

### 7.3 `pg_stat_database` / `pg_stat_database_conflicts`

| `pg_stat_database` | 13 | 14–17 | 18 |
|---|---|---|---|
| `datid`, `datname`, `numbackends`, `xact_commit`, `xact_rollback`, `blks_read`, `blks_hit`, `tup_returned`, `tup_fetched`, `tup_inserted`, `tup_updated`, `tup_deleted`, `conflicts`, `temp_files`, `temp_bytes`, `deadlocks`, `checksum_failures`, `checksum_last_failure`, `blk_read_time`, `blk_write_time`, `stats_reset` | ● | ● | ● |
| `session_time`, `active_time`, `idle_in_transaction_time`, `sessions`, `sessions_abandoned`, `sessions_fatal`, `sessions_killed` | | ● **all 7 added PG14** | ● |
| `parallel_workers_to_launch`, `parallel_workers_launched` | | | ● **added PG18** |

Counts: **PG13 = 21**, **PG14–17 = 28**, **PG18 = 30**.

| `pg_stat_database_conflicts` | 13–15 | 16–18 |
|---|---|---|
| `datid`, `datname`, `confl_tablespace`, `confl_lock`, `confl_snapshot`, `confl_bufferpin`, `confl_deadlock` | ● | ● |
| `confl_active_logicalslot` | | ● **added PG16** |

Counts: **PG13–15 = 7**, **PG16–18 = 8**.

**`pg_stat_get_db_*` functions** (from `system_views.sql`, exact): PG13 exposes 23 —
`pg_stat_get_db_blk_read_time`, `_blk_write_time`, `_blocks_fetched`, `_blocks_hit`,
`_checksum_failures`, `_checksum_last_failure`, `_conflict_all`, `_conflict_bufferpin`,
`_conflict_lock`, `_conflict_snapshot`, `_conflict_startup_deadlock`, `_conflict_tablespace`,
`_deadlocks`, `_numbackends`, `_stat_reset_time`, `_temp_bytes`, `_temp_files`, `_tuples_deleted`,
`_tuples_fetched`, `_tuples_inserted`, `_tuples_returned`, `_tuples_updated`, `_xact_commit`,
`_xact_rollback`. PG14/15 add 7 (`_active_time`, `_session_time`, `_idle_in_transaction_time`,
`_sessions`, `_sessions_abandoned`, `_sessions_fatal`, `_sessions_killed`); PG16/17 add 1
(`_conflict_logicalslot`); PG18 adds 2 (`_parallel_workers_to_launch`, `_parallel_workers_launched`).

### 7.4 `pg_stat_wal`, `pg_stat_io`, `pg_stat_slru`, `pg_stat_bgwriter`, `pg_stat_checkpointer`

**`pg_stat_wal`** — **view added PG14**; PG13 has none.

| Column | 14–17 | 18 |
|---|---|---|
| `wal_records`, `wal_fpi`, `wal_bytes`, `wal_buffers_full`, `stats_reset` | ● | ● |
| `wal_write`, `wal_sync`, `wal_write_time`, `wal_sync_time` | ● | **removed** |

Counts: **PG14–17 = 9**, **PG18 = 5** (release-18: *"Remove read/sync columns from pg_stat_wal"*).
PG18 also makes `track_wal_io_timing` control WAL timing in `pg_stat_io` instead of `pg_stat_wal`.

**`pg_stat_io`** — **added PG16.**

| Column | 16 | 17 | 18 |
|---|---|---|---|
| `backend_type`, `object`, `context`, `reads`, `read_time`, `writes`, `write_time`, `writebacks`, `writeback_time`, `extends`, `extend_time`, `hits`, `evictions`, `reuses`, `fsyncs`, `fsync_time`, `stats_reset` | ● | ● | ● |
| `op_bytes` | ● | ● | **removed** |
| `read_bytes`, `write_bytes`, `extend_bytes` | | | ● **added PG18** |

Counts: **PG16 = 18**, **PG17 = 18**, **PG18 = 20**.
Enum domains (PG18 docs): `backend_type` = `autovacuum analyze worker`, `autovacuum worker`,
`background writer`, `checkpointer`, `client backend`, `io worker`, `parallel worker`, `startup`,
`wal writer`, `walsender`, `walreceiver`, `walsummarizer`, `archiver`; `object` = `relation`,
`temprelation`, `index`, `tempindex`, `toast`, `toastindex`, `temptoast`, `temptoastindex`,
**`wal`** (PG18), `bulkwrite`, `initfork`; `context` = `normal`, `vacuum`, `backgroundwrite`,
`checkpointwrite`, `bulkwrite`, **`init`** (PG18), `vacuumsequence`, `walwrite` (PG18).

> **Note for a CNPG-based product:** `pg_stat_io` is **not** collected by CNPG's built-in exporter,
> and postgres_exporter's own docs confirm it does not collect it either — *"the checkpoint ones live
> in `pg_stat_io`, which this exporter does not currently collect."* On PG 17/18 this is where the
> backend-write metrics went when `pg_stat_bgwriter` shrank.

**`pg_stat_slru`** — **added PG13**; columns identical in **all six versions (9)**: `name`,
`blks_zeroed`, `blks_hit`, `blks_read`, `blks_written`, `blks_exists`, `flushes`, `truncates`,
`stats_reset`. The `slru_name` domain was **renamed in PG15**:

| PG13–PG14 | PG15+ |
|---|---|
| `CommitTs` | `commit_timestamp` |
| `MultiXactMember` | `multixact_member` |
| `MultiXactOffset` | `multixact_offset` |
| `Notify` | `notify` |
| `Serial` | `serializable` |
| `Subtrans` | `subtransaction` |
| `Xact` | `transaction` |
| (+ `other`) | (+ `other`) |

**`pg_stat_bgwriter`:**

| Column | 13–16 | 17 | 18 |
|---|---|---|---|
| `checkpoints_timed`, `checkpoints_req`, `checkpoint_write_time`, `checkpoint_sync_time`, `buffers_checkpoint`, `buffers_backend`, `buffers_backend_fsync` | ● | **removed** | **removed** |
| `buffers_clean`, `maxwritten_clean`, `buffers_alloc`, `stats_reset` | ● | ● | ● |

Counts: **PG13–16 = 11**, **PG17–18 = 4**. The checkpoint columns moved out to a new view in PG17.

**`pg_stat_checkpointer`** — **view does not exist before PG17.**

| Column | 17 | 18 |
|---|---|---|
| `num_timed`, `num_requested`, `restartpoints_timed`, `restartpoints_req`, `restartpoints_done`, `write_time`, `sync_time`, `buffers_written`, `stats_reset` | ● | ● |
| `num_done` | | ● **added PG18** (completed checkpoints; `num_timed`/`num_requested` now count completed *and* skipped) |
| `slru_written` | | ● **added PG18** (SLRU buffers written at checkpoint) |

Counts: **PG17 = 9**, **PG18 = 11**. Underlying functions: PG13–16 expose `pg_stat_get_bgwriter_*`
(11) + `pg_stat_get_checkpoint_write_time` / `pg_stat_get_checkpoint_sync_time`; PG17 replaces them
with `pg_stat_get_checkpointer_num_timed`, `_num_requested`, `_restartpoints_timed`,
`_restartpoints_requested`, `_restartpoints_performed`, `_write_time`, `_sync_time`,
`_buffers_written`, `_stat_reset_time`; PG18 adds `pg_stat_get_checkpointer_num_performed` and
`pg_stat_get_checkpointer_slru_written`.

### 7.5 `pg_stat_progress_*` family

| View | 13 | 14 | 15 | 16 | 17 | 18 | Cols |
|---|---|---|---|---|---|---|---|
| `pg_stat_progress_analyze` | ● | ● | ● | ● | ● | ● | 12 → **13 in PG18** (+`delay_time`) |
| `pg_stat_progress_basebackup` | ● | ● | ● | ● | ● | ● | 6 |
| `pg_stat_progress_cluster` | ● | ● | ● | ● | ● | ● | 12 |
| `pg_stat_progress_copy` | | ● | ● | ● | ● | ● | 10 → **11 in PG17** (+`tuples_skipped`) |
| `pg_stat_progress_create_index` | ● | ● | ● | ● | ● | ● | 16 |
| `pg_stat_progress_vacuum` | ● | ● | ● | ● | ● | ● | 11 → **14 in PG17** → **15 in PG18** |
| `pg_stat_progress_checkpoint` | | | | | | — | does not exist in PG18 |

**`pg_stat_progress_vacuum`** — `pid`, `datid`, `datname`, `relid`, `phase`, `heap_blks_total`,
`heap_blks_scanned`, `heap_blks_vacuumed`, `index_vacuum_count` (all versions);
`max_dead_tuples` / `num_dead_tuples` (**PG13–16 only**, removed in 17);
`max_dead_tuple_bytes` / `dead_tuple_bytes` / `num_dead_item_ids` + `indexes_total` /
`indexes_processed` (**PG17+**); `delay_time` (**PG18**, requires `track_cost_delay_timing`).

**`pg_stat_progress_create_index`** (identical 13–18, 16): `pid`, `datid`, `datname`, `relid`,
`index_relid`, `command`, `phase`, `lockers_total`, `lockers_done`, `current_locker_pid`,
`blocks_total`, `blocks_done`, `tuples_total`, `tuples_done`, `partitions_total`, `partitions_done`.

**`pg_stat_progress_analyze`** (12, PG13–17): `pid`, `datid`, `datname`, `relid`, `phase`,
`sample_rows`, `current_columns`, `estimated_total_rows`, `rows_targeted_for_sample`,
`pages_total`, `pages_done`, `rows_sampled` → **PG18 adds `delay_time` (13)**.

**`pg_stat_progress_cluster`** (12): `pid`, `datid`, `datname`, `relid`, `index_relid`, `command`,
`phase`, `cluster_relation_type`, `blocks_total`, `blocks_done`, `tuples_total`, `tuples_done`.

**`pg_stat_progress_basebackup`** (6): `pid`, `phase`, `backup_total`, `backup_streamed`,
`tablespaces_total`, `tablespaces_streamed`.

**`pg_stat_progress_copy`** (10, PG14–16): `pid`, `datid`, `datname`, `relid`, `command`, `type`,
`bytes_done`, `tuples_done`, `tuples_processed`, `bytes_total` → **PG17 adds `tuples_skipped` (11)**.

### 7.6 Table / index / function view family

All of these exist in **all six versions**: `pg_stat_user_tables`, `pg_stat_sys_tables`,
`pg_stat_all_tables`, `pg_stat_xact_user_tables`, `pg_stat_xact_sys_tables`,
`pg_stat_xact_all_tables`, `pg_stat_user_indexes`, `pg_stat_sys_indexes`, `pg_stat_all_indexes`,
`pg_stat_xact_user_functions`, `pg_stat_user_functions`, `pg_statio_user_tables`,
`pg_statio_sys_tables`, `pg_statio_all_tables`, `pg_statio_user_indexes`, `pg_statio_sys_indexes`,
`pg_statio_all_indexes`, `pg_statio_user_sequences`, `pg_statio_sys_sequences`,
`pg_statio_all_sequences`.

`pg_stat_all_tables` **PG13–15 (23)**: `relid`, `schemaname`, `relname`, `seq_scan`,
`seq_tup_read`, `idx_scan`, `idx_tup_fetch`, `n_tup_ins`, `n_tup_upd`, `n_tup_del`,
`n_tup_hot_upd`, `n_live_tup`, `n_dead_tup`, `n_mod_since_analyze`, `n_ins_since_vacuum`,
`last_vacuum`, `last_autovacuum`, `last_analyze`, `last_autoanalyze`, `vacuum_count`,
`autovacuum_count`, `analyze_count`, `autoanalyze_count`.
**PG16 adds 4** → 27: `last_seq_scan`, `last_idx_scan`, `n_tup_newpage_upd`, `t_ctid`.
**PG18 adds 4** → 31: `total_vacuum_time`, `total_autovacuum_time`, `total_analyze_time`,
`total_autoanalyze_time`.

`pg_stat_all_indexes` **PG13–15 (8)**: `relid`, `indexrelid`, `schemaname`, `relname`,
`indexrelname`, `idx_scan`, `idx_tup_read`, `idx_tup_fetch`; **PG16 adds `last_idx_scan` → 9**.

`pg_stat_user_functions` (6, all versions): `funcid`, `schemaname`, `funcname`, `calls`,
`total_time`, `self_time`.
`pg_statio_all_tables` (11, all versions): `relid`, `schemaname`, `relname`, `heap_blks_read`,
`heap_blks_hit`, `idx_blks_read`, `idx_blks_hit`, `toast_blks_read`, `toast_blks_hit`,
`tidx_blks_read`, `tidx_blks_hit`.
`pg_statio_all_indexes` (7, all versions): `relid`, `indexrelid`, `schemaname`, `relname`,
`indexrelname`, `idx_blks_read`, `idx_blks_hit`.
`pg_statio_all_sequences` (5, all versions): `relid`, `schemaname`, `relname`, `blks_read`, `blks_hit`.

Functions added later: `pg_stat_get_lastscan` (**PG16**),
`pg_stat_get_tuples_newpage_updated` / `pg_stat_get_xact_tuples_newpage_updated` (**PG16**),
`pg_stat_get_total_vacuum_time` / `_total_autovacuum_time` / `_total_analyze_time` /
`_total_autoanalyze_time` (**PG18**).

### 7.7 Replication, connection, lock, and settings views

| View | 13 | 14 | 15 | 16 | 17 | 18 | Notes |
|---|---|---|---|---|---|---|---|
| `pg_stat_replication` | ● | ● | ● | ● | ● | ● | **identical 20 cols in all six**: `pid`, `usesysid`, `usename`, `application_name`, `client_addr`, `client_hostname`, `client_port`, `backend_start`, `backend_xmin`, `state`, `sent_lsn`, `write_lsn`, `flush_lsn`, `replay_lsn`, `write_lag`, `flush_lag`, `replay_lag`, `sync_priority`, `sync_state`, `reply_time` |
| `pg_stat_wal_receiver` | ● | ● | ● | ● | ● | ● | **identical 15 cols in all six**: `pid`, `status`, `receive_start_lsn`, `receive_start_tli`, `written_lsn`, `flushed_lsn`, `received_tli`, `last_msg_send_time`, `last_msg_receipt_time`, `latest_end_lsn`, `latest_end_time`, `slot_name`, `sender_host`, `sender_port`, `conninfo` |
| `pg_stat_replication_slots` | | ● | ● | ● | ● | ● | **added PG14**, 10 cols: `slot_name`, `spill_txns`, `spill_count`, `spill_bytes`, `stream_txns`, `stream_count`, `stream_bytes`, `total_txns`, `total_bytes`, `stats_reset` |
| `pg_stat_recovery_prefetch` | | | ● | ● | ● | ● | **added PG15**, 10 cols: `wal_distance`, `block_distance`, `io_depth`, `stats_reset`, `prefetch`, `hit`, `skip_init`, `skip_new`, `skip_fpw`, `skip_rep` |
| `pg_stat_subscription` | ● | ● | ● | ● | ● | ● | 13–15 = 9 (`subid`, `subname`, `pid`, `relid`, `received_lsn`, `last_msg_send_time`, `last_msg_receipt_time`, `latest_end_lsn`, `latest_end_time`); **PG16 +`leader_pid`** (10); **PG17 +`worker_type`** (11) |
| `pg_stat_subscription_stats` | | | ● | ● | ● | ● | **added PG15**, 5 cols (`subid`, `subname`, `apply_error_count`, `sync_error_count`, `stats_reset`); **PG18 = 12** (+`confl_insert_exists`, `confl_update_origin_differs`, `confl_update_exists`, `confl_update_missing`, `confl_delete_origin_differs`, `confl_delete_missing`, `confl_multiple_unique_conflicts`) |
| `pg_stat_ssl` | ● | ● | ● | ● | ● | ● | **PG13 = 9** (`pid`, `ssl`, `version`, `cipher`, `bits`, `compression`, `client_dn`, `client_serial`, `issuer_dn`); **PG14+ = 8** (`compression` removed) |
| `pg_stat_gssapi` | ● | ● | ● | ● | ● | ● | 13–15 = 4 (`pid`, `gss_authenticated`, `principal`, `encrypted`); **PG16 +`credentials_delegated`** (5) |
| `pg_stat_archiver` | ● | ● | ● | ● | ● | ● | identical 7 in all six: `archived_count`, `last_archived_wal`, `last_archived_time`, `failed_count`, `last_failed_wal`, `last_failed_time`, `stats_reset` |
| `pg_locks` | ● | ● | ● | ● | ● | ● | **PG13 = 14**, **PG14–18 = 15** (`waitstart` **added PG14**). `locktype` domain: `relation`, `extend`, `page`, `tuple`, `transactionid`, `virtualtransaction`, `pid`, `multixact`, `object`, `userlock`, `database`, `relation_toast`, `toasted_tuple`, `snapshot`, `extension` (`extension` added PG14) |
| `pg_replication_slots` | ● | ● | ● | ● | ● | ● | **PG13 (12)**: `slot_name`, `plugin`, `slot_type`, `datoid`, `database`, `temporary`, `active`, `active_pid`, `xmin`, `catalog_xmin`, `restart_lsn`, `confirmed_flush_lsn`, `wal_status`, `safe_wal_size` → **PG14 +`two_phase`** (15) → **PG16 +`conflicting`** (16) → **PG17 +`inactive_since`, `synced`, `failover`, `invalidation_reason`** (20) → **PG18 +`two_phase_at`** (21) |
| `pg_replication_origin_status` | ● | ● | ● | ● | ● | ● | `localoid`, `provider`, `rolename`, `remoteoid`, `local_lsn`, `remote_lsn` |
| `pg_hba_file_rules` | ● | ● | ● | ● | ● | ● | **PG15 adds `pg_ident_file_mappings`** |
| `pg_wait_events` | | | | | ● | ● | **added PG17** — enumerates `type`/`name` for every wait event |
| `pg_backend_memory_contexts` | | ● | ● | ● | ● | ● | **added PG14**; **PG18 adds columns `type` and `path`** |
| `pg_shmem_allocations` | ● | ● | ● | ● | ● | ● | **PG18 adds `pg_shmem_allocations_numa`** and **`pg_aios`** |
| `pg_settings` | ● | ● | ● | ● | ● | ● | **identical 17 columns in PG13–PG18**: `name`, `setting`, `unit`, `category`, `short_desc`, `extra_desc`, `context`, `vartype`, `source`, `min_val`, `max_val`, `enumvals`, `boot_val`, `reset_val`, `sourcefile`, `sourceline`, `pending_restart`. PG18 adds the function `pg_settings_get_flags(text) → text[]` |
| `pg_file_settings` | ● | ● | ● | ● | ● | ● | identical 7 in all six: `sourcefile`, `sourceline`, `seqno`, `name`, `setting`, `applied`, `error` |

**Postmaster / server-info functions** (all versions unless noted): `pg_postmaster_start_time()`,
`pg_conf_load_time()`, `pg_control_system()`, `pg_control_checkpoint()`, `pg_control_init()`,
`pg_control_recovery()`, `pg_control_last_modified()`, `pg_controldata()`, `pg_current_logfile([text])`,
`pg_stat_file(text [, missing_ok boolean])`; **PG17** `pg_postmaster_get_postmasterid() → oid`
*(single-source: PG17/18 `functions-info.html`; PG13–16 pages not individually checked — **(unverified)**
for 14–16)*; **PG17** `pg_input_is_valid(text,regtype)` / `pg_input_error_info(text,regtype)`;
**PG18** `pg_numa_available()`, `pg_get_wal_summarizer_state()`, `pg_available_wal_summaries()`,
`pg_wal_summary_contents()`, `pg_ls_summariesdir()`, `pg_get_acl(oid) → aclitem[]`,
`pg_get_loaded_modules() → text[]`.

**Size functions** (identical PG13–PG18): `pg_relation_size(regclass [, fork text]) → bigint`,
`pg_total_relation_size(regclass) → bigint`, `pg_tablespace_size(name|oid) → bigint`,
`pg_size_pretty(bigint|numeric) → text`, `pg_database_size(text|oid) → bigint`.
Planner statistics (not cumulative stats): `pg_statistic`, `pg_statistic_ext` / `pg_stats_ext`
(**PG14+**), `pg_statistic_ext_data` / `pg_stats_ext_exprs` (**PG14+**), plus `pg_class.reltuples`,
`pg_class.relpages`, `pg_class.relallvisible`.

### 7.8 Reset semantics

| Function | 13 | 14 | 15 | 16 | 17 | 18 |
|---|---|---|---|---|---|---|
| `pg_stat_reset() → void` | ● | ● | ● | ● | ● | ● |
| `pg_stat_reset_shared(text) → void` | ● | ● | ● | ● | | |
| `pg_stat_reset_shared([target text DEFAULT NULL]) → void` | | | | | ● | ● | **signature changed PG17** |
| `pg_stat_reset_single_table_counters(oid) → void` | ● | ● | ● | ● | ● | ● |
| `pg_stat_reset_single_function_counters(oid) → void` | ● | ● | ● | ● | ● | ● |
| `pg_stat_reset_slru(text) → void` | ● | ● | ● | ● | | |
| `pg_stat_reset_slru([target text DEFAULT NULL]) → void` | | | | | ● | ● | **signature changed PG17** |
| `pg_stat_reset_replication_slot(text) → void` | | ● | ● | ● | ● | ● | **added PG14** |
| `pg_stat_reset_subscription_stats(oid) → void` | | | ● | ● | ● | ● | **added PG15** |
| `pg_stat_reset_backend_stats(integer) → void` | | | | | | ● | **added PG18** |
| `pg_stat_clear_snapshot() → void` | ● | ● | ● | ● | ● | ● |
| `pg_stat_get_snapshot_timestamp() → timestamptz` | ● | ● | ● | ● | ● | ● |

**Valid `pg_stat_reset_shared` targets per version:**

| PG | Allowed targets |
|---|---|
| 13 | `bgwriter`, `archiver` |
| 14 | `bgwriter`, `archiver`, `wal` |
| 15 | `bgwriter`, `archiver`, `wal`, `recovery_prefetch` |
| 16 | `bgwriter`, `archiver`, `io`, `wal`, `recovery_prefetch` |
| 17 | `archiver`, `bgwriter`, `checkpointer`, `io`, `recovery_prefetch`, `slru`, `wal` (+ `NULL` = all) |
| 18 | `archiver`, `bgwriter`, `checkpointer`, `io`, `recovery_prefetch`, `slru`, `wal` (+ `NULL` = all) |

**Which views expose a `stats_reset` column**: `pg_stat_archiver` (all), `pg_stat_bgwriter` (all),
`pg_stat_database` (all), `pg_stat_wal` (14+), `pg_stat_slru` (13+), `pg_stat_io` (16+),
`pg_stat_checkpointer` (17+), `pg_stat_recovery_prefetch` (15+), `pg_stat_replication_slots` (14+),
`pg_stat_subscription_stats` (15+). **No** `stats_reset` on `pg_stat_activity`, `pg_locks`,
`pg_stat_all_indexes`, `pg_statio_*`.

**Persistence model — the change that breaks naive counter math:**

| PG | Model |
|---|---|
| **≤ PG14** | Statistics collector process; data transmitted through **temporary files** in the dir named by **`stats_temp_directory`** (default `pg_stat_tmp`); on clean shutdown a permanent copy is written to `pg_stat/`; on unclean start (immediate shutdown, crash, PITR) all counters reset. |
| **PG15+** | **Statistics collector removed**; statistics live in **shared memory** (`pg_stat/pgstat.stat` is the on-disk permanent file). **`stats_temp_directory` removed** (present in PG14, absent in PG15). New GUC **`stats_fetch_consistency`** (`none` \| `transaction` \| `snapshot`). |
| **PG16 / PG17** | Same shared-memory model. |
| **PG18** | Same, plus `pg_stat_reset_backend_stats(integer)` and per-backend I/O / WAL views. |

`pg_stat_statements` persistence: PG13–14 the module kept its own file under
`$PGDATA/pg_stat_statements/` (`pg_stat_statements.stat`); from **PG15** it uses the shared-memory
stats subsystem and its file moved to `$PGDATA/pg_stat/` **(unverified** — the PG15 module docs
describe shared-memory integration but do not state the path).

> **Product implication:** a counter that resets on crash/PITR means a Prometheus `rate()` over a
> restart boundary is meaningless without the `stats_reset` timestamp, and CNPG's failover → PG18
> `stats_reset` NULL bug (v1.30.1 fix, §1.8) is exactly this class of problem.

### 7.9 PG 18 new statistics features (from `release-18.html` §E.7.3.1.4 "Monitoring" + §E.7.3.9.1)

| Change | Detail |
|---|---|
| `pg_stat_all_tables` time columns | new `total_vacuum_time`, `total_autovacuum_time`, `total_analyze_time`, `total_autoanalyze_time` |
| Cost-delay timing | new GUC **`track_cost_delay_timing`**; `delay_time` added to `pg_stat_progress_vacuum` and `pg_stat_progress_analyze`; also in server log and `VACUUM/ANALYZE VERBOSE` |
| `ANALYZE VERBOSE` output | adds WAL, CPU and average-read statistics |
| Per-backend I/O stats | new **`pg_stat_get_backend_io(integer)`**; cleared via `pg_stat_reset_backend_stats(integer)` |
| Per-backend WAL stats | new **`pg_stat_get_backend_wal(integer)`**; cleared via `pg_stat_reset_backend_stats()` |
| `pg_stat_io` byte columns | new `read_bytes`, `write_bytes`, `extend_bytes`; **`op_bytes` removed** |
| `pg_stat_io` WAL rows | WAL I/O activity rows added (incl. WAL receiver activity) + a new wait event |
| `track_wal_io_timing` | now controls WAL timing in `pg_stat_io` instead of `pg_stat_wal` |
| `pg_stat_wal` | **removed** `wal_write`, `wal_sync`, `wal_write_time`, `wal_sync_time` |
| `pg_stat_checkpointer` | new `num_done` and `slru_written`; `num_timed`/`num_requested` now count completed *and* skipped |
| `pg_stat_database` | new `parallel_workers_to_launch`, `parallel_workers_launched` |
| `pg_stat_subscription_stats` | 7 new conflict counters (see §7.7) |
| `pg_stat_activity` | new `state` value **`starting`** |
| `pg_stat_statements` (1.12) | new `wal_buffers_full`, `parallel_workers_to_launch`, `parallel_workers_launched`; now tracks `CREATE TABLE AS` and `DECLARE`; parameterizes `SET` values; **query-id jumbling changes** (constant lists → first/last only; the same relation name in different schemas now group together) |
| Extensions API | **extensions can now use the server's cumulative statistics API** (Michael Paquier) |
| New views | `pg_aios`, `pg_shmem_allocations_numa`, `pg_available_wal_summaries`, `pg_wal_summary_contents`, `pg_ls_summariesdir()` |
| `pg_backend_memory_contexts` | new `type` and `path` columns |
| Other new functions | `pg_settings_get_flags()`, `pg_numa_available()`, `pg_get_wal_summarizer_state()`, `pg_get_acl()`, `pg_get_loaded_modules()` |
| Logging | `log_connections` gains granularity options + connection-stage duration; new `log_lock_failures`; new `log_line_prefix` escape `%L` |

> **Query-id jumbling change matters for a Query Insights product**: PG18 changes how `queryid` is
> computed (constant lists collapse to first/last; same-named relations across schemas merge). A
> query store that spans a PG17 → PG18 upgrade will see **queryids change for unchanged queries**,
> breaking baseline comparison unless the store re-jumbles or keys on a normalized fingerprint instead.

### 7.10 Mapping to what CNPG actually exposes

CNPG's built-in exporter (§4.10) queries 14 of these views; postgres_exporter's collector set
(§4.12) covers a further set. Cross-referencing the two against this matrix:

| View | Exists in | CNPG default queries (§4.10) | postgres_exporter collector (§4.12) |
|---|---|---|---|
| `pg_stat_statements` | 13–18 (ext 1.8→1.12) | **no** | `stat_statements` — **disabled by default** |
| `pg_stat_user_tables` | 13–18 | yes | `stat_user_tables` — enabled |
| `pg_stat_database` | 13–18 | yes | `stat_database` — enabled |
| `pg_stat_activity` | 13–18 | yes | `stat_activity` — enabled |
| `pg_locks` | 13–18 | yes | `locks` — enabled |
| `pg_replication_slots` | 13–18 | yes | `replication_slots` — enabled |
| `pg_stat_replication` | 13–18 | yes | `stat_replication` — enabled |
| `pg_stat_archiver` | 13–18 | yes | `stat_archiver` — enabled |
| `pg_stat_bgwriter` | 13–18 (shape changes at 17) | yes | `stat_bgwriter` — enabled |
| `pg_stat_checkpointer` | **17+ only** | yes | `stat_checkpointer` — **disabled by default**, 17+ only |
| `pg_database` | 13–18 | yes | `database` — enabled |
| `pg_postmaster` | 13–18 | yes | `postmaster` — disabled by default |
| `pg_wal` | 13–18 | yes | `wal` — enabled |
| `pg_statio_user_tables` | 13–18 | yes | `statio_user_tables` — enabled |
| `pg_statio_user_indexes` | 13–18 | yes | `statio_user_indexes` — disabled by default |
| `pg_stat_progress_vacuum` | 13–18 (shape changes at 17/18) | yes | `stat_progress_vacuum` — enabled |
| `pg_settings` | 13–18 | yes | `settings` — enabled |
| `pg_roles` | 13–18 | yes | `roles` — enabled |
| `pg_replication` | 13–18 | yes | `replication` — enabled |
| `pg_stat_wal` | **14+ only** | **no** | **no** (CNPG emits `pg_wal` via the Go collector instead, §5.1) |
| `pg_stat_io` | **16+ only** | **no** | **no** — explicitly not collected |
| `pg_stat_slru` | 13–18 | **no** | **no** |
| `pg_stat_wal_receiver` | 13–18 | **no** | `stat_wal_receiver` — disabled by default |
| `pg_stat_subscription` / `_stats` | 13–18 / 15+ | **no** | **no** |
| `pg_stat_recovery_prefetch` | **15+ only** | **no** | **no** |
| `pg_wait_events` | **17+ only** | **no** | **no** |
| `pg_backend_memory_contexts` | **14+ only** | **no** | **no** |
| `pg_stat_ssl` / `pg_stat_gssapi` | 13–18 | **no** | **no** |
| `pg_stat_progress_{analyze,cluster,copy,create_index,basebackup}` | 13–18 | **no** | **no** |

The `pg_stat_checkpointer` row is the one to watch operationally: CNPG's default query set includes
it unconditionally, but the view **only exists on PG 17+** — on PG 15/16 that query errors per
scrape (the exporter logs it and moves on, §4.7).

---

## 8. Overlap analysis — what CNPG already ships vs. the gap

### 8.1 What exists today, by surface

| Surface | What it is | Limits |
|---|---|---|
| **Grafana dashboard 20417** (§6.3) | 66 panels, uid `cloudnativepg`, one official JSON | **Metric-only, PromQL-only, single-cluster via template dropdown, zero query text.** No `pg_stat_statements` panel at all |
| **`kubectl cnpg status`** (§6.2) | Typed terminal render of `ClusterStatus` + live PG queries | Point-in-time, single-cluster, terminal-only, no history, no charts. `-o json` is the reusable artifact |
| **`cnpg-headlamp-plugin`** | `ghcr.io/cloudnative-pg/cnpg-headlamp-plugin:v0.1.6`, requires Headlamp ≥ 0.42; blog 2026-10-15 | **The real UI competitor.** Manages Clusters/Poolers/Backups/ScheduledBackups/Database objects. Traffic-light health (phase, WAL archiving, last backup); instance roles + sync-replication warnings; per-instance Postgres logs (filterable, color-coded, live-following); a **`psql` terminal** against primary or any replica; and a **"Live Metrics"** tab scraped from each instance's exporter on **port 9187 via the k8s API pod proxy subresource — no psql, no exec** — in categories **Replication & Archiving** (connected standbys, lag, inactive slots, archiving failures, backlog/timing, sync replica counts, per-standby + per-slot tables), **General Health** and **Checkpointing** (connections, cache hit ratio, database size, blocked and long-running queries, deadlocks, checkpoint/restartpoint counts), **Database Health** (xid + multixact age, rollback ratio, temp spill, extension updates, per database). Every tile shows an (i) icon with the underlying `cnpg_*` metric names; refresh 15s/30s/60s/off (matched to the exporter's 30s cadence). Also scale, manual switchover, fencing, hibernation, leader-election lease details, tabbed creation form with GUC presets + bootstrap-from-backup, Object Stores, Image Catalogs, and an **Operator status page** (installed CRDs, pod health, detected CNPG-i plugins with log access). **Still metric-only — no query text, no EXPLAIN** |
| **pgAdmin4** | **No pgAdmin Helm chart in the CNPG org.** `github.com/cloudnative-pg/charts` has exactly 3 charts: **cloudnative-pg** (operator, `version 0.29.2` / `appVersion 1.30.2` / `kubeVersion >=1.29.0-0`, maintainers phisco/mnencia/gbartolini), **plugin-barman-cloud** (CNPG-I), **cluster** (sample DB cluster). pgAdmin exists only as `kubectl cnpg pgadmin4` (`--mode server|desktop`, image `dpage/pgadmin4:latest`), and docs say **"Never deploy pgAdmin in production using the plugin."** | Eval/demo object browser — zero overlap with metrics/insights |
| **Built-in alert rules** (§6.4) | 7 rules, one group, all `warning`, all `for: 1m` | No `critical` tier, no per-cluster thresholds, no notification routing, no runbook links, no grouping/inhibition config |
| **`postgres_exporter` dashboards** (community, e.g. 9628, 14149) | Cover `pg_stat_statements`-derived panels | CNPG's exporter is *inspired by* it but distinct (`cache_seconds` unimplemented, `cnpg_` prefix, built into the pod on 9187) — those dashboards target metrics CNPG **does not emit** |
| **"CloudNativePG Console" / an EDB "AI" plugin** | **Do not exist.** Verified seven ways: the full org repo listing (~40 repos across both API pages) has no `ai`/`plugin-ai`/`cnpg-ai`/`mcp`/`console`; `curl -I` on `github.com/cloudnative-pg/{ai, plugin-ai, mcp, cnpg-ai, console}` → all **HTTP 404**; `cloudnative-pg.io/documentation/current/mcp/` → 404; `docs/src/` has no `ai.md`/`mcp.md`/`console.md`; `cmd/kubectl-cnpg/main.go` has no `ai` subcommand; the homepage lists no AI/Console/Insights product; the full blog index (33 posts) has no AI/assistant/MCP/console/insights title. WebSearch hits for `cloudnative-pg/plugin-ai`, `cloudnative-pg/mcp`, and an "EDB Postgres AI Assistant" were **not verifiable** (edb.com itself returned `ECONNREFUSED 195.254.213.7:443`) | The real plugin ecosystem is the **CNPG-I** gRPC spec (`cloudnative-pg/cnpg-i`), whose README lists exactly: **CNPG-I Hello World**, **Barman Cloud CNPG-I Plugin** (`plugin-barman-cloud`), plus third-party **CNPG-I Scale-to-Zero Plugin** (`xataio/cnpg-i-scale-to-zero`). Also `cnpg-i-machinery`, `plugin-pvc-backup`, `klio` ("Multi-Tiered Backup and Recovery Plugin"). Capabilities: Backup Service, Restore Job, Reconciler Hooks, WAL Service, Operator Service, Lifecycle Service, Postgres Service |
| **K9s plugin** | `derailed/k9s/blob/master/plugins/cloudnative-pg.yaml` | Terminal CRUD, no metrics |

### 8.2 Gaps an insights product fills (not covered by any surface above)

1. **Query-level insights UI** — nothing in the CNPG ecosystem renders `pg_stat_statements`. The
   exporter doesn't emit it (§4.10), the dashboard has no panel for it, `status` doesn't show it,
   Headlamp doesn't show it.
2. **EXPLAIN / `EXPLAIN (ANALYZE, BUFFERS)` plans** — no plan viewer, no plan history, no
   plan-change regression detection anywhere.
3. **`pg_stat_statements` top-N UI** — top-N by `total_exec_time` / `mean_exec_time` / `rows` /
   `shared_blks_hit` / `temp_bytes` / `calls`, with normalization and `queryid` tracking.
4. **Wait-event analysis** — `pg_stat_activity.wait_event_type`/`wait_event` rollup
   (Lock/LWLock/IO/Activity/Client/Timeout/IPC). CNPG exposes only `cnpg_backends_waiting_total`
   (a count) — no breakdown by event.
5. **Index / table bloat estimation** — nothing. Only `cnpg_pg_database_size_bytes` (per-DB) and
   `cnpg_pg_database_xid_age` (wraparound). No per-table bloat, no unused-index detection.
6. **Long-running transaction detection as a product** — exists only as one PromQL alert
   (`LongRunningTransaction`, `> 300`) and one `Longest Transaction` timeseries. No per-backend
   list, no `pid`/`usename`/`application_name`/`query` detail, no idle-in-transaction detection.
7. **Historical query store** — Prometheus gives 15s-resolution counters with a retention window;
   no per-query history, no baseline comparison, no week-over-week regression.
8. **Per-cluster drilldown** — the dashboard is `$namespace`/`$cluster`/`$instances` templated but
   single-select; no drilldown tree, no cross-cluster navigation, no saved views.
9. **Multi-cluster fleet view** — the biggest structural gap. No fleet-level rollup exists anywhere:
   no "all clusters in one page", no health distribution, no org-wide top offenders, no
   cost/rightsizing rollup. `operatorNamespace` + `namespace` + `cluster` variables are the only
   fleet surface, and they're a dropdown.
10. **Anomaly detection** — everything is static thresholds (`> 300`, `> 300000000`, `> 10`). No
    baselining, no seasonality, no learned thresholds, no `critical` tier at all.
11. **Lock-graph / blocking-chain visualization** — `Blocked Queries` is a single counter panel; no
    `pg_locks` deadlock graph, no blocker→blockee chain.
12. **Replication topology visualization** — 4 lag timeseries + 2 stats; no primary→standby topology
    diagram, no per-slot lag matrix.
13. **Autovacuum insight** — no `pg_stat_user_tables` autovacuum last/next/analyze panels in the
    dashboard at all; only `xid_age`.
14. **Connection-pool (PgBouncer/Pooler) insights** — CNPG Poolers exist as a CRD and appear in
    Headlamp, but have **zero dashboard panels and zero alert rules**, despite `cnpg_pgbouncer_*`
    being fully emitted (§5.4).
15. **Tablespace-level trend** — `status` prints tablespaces and the dashboard has a
    `Volume Space Usage: Tablespaces` gauge, but no per-tablespace time trend.
16. **Cost / rightsizing** — no CPU/memory-request vs. actual utilization recommendation surface
    (two gauge panels exist, no recommendation).
17. **Runbook / remediation linkage** — alerts have no annotations/runbooks; `kubectl cnpg` actions
    (promote/restart/fence/hibernate/backup) are not linked from any metric.

### 8.3 Cheapest ingestion points, ranked

1. **`Cluster.status`** (§1.3) — one GET per cluster; gives phase, instances, roles, PVCs, services,
   topology, cert expirations, conditions, systemID, image. No metrics needed, no exec needed.
2. **`kubectl cnpg status -o json`** (§6.2) — the same data plus live PG-derived rows (streaming
   replication table, PDB table, instance LSN/QoS/manager-version, plugins) that `ClusterStatus`
   alone does not carry.
3. **`GET https://<pod>:8000/pg/status`** (§3.4) — the unauthenticated HTTP endpoint behind #2;
   reachable without exec, without a kubeconfig, from any pod network.
4. **`GET http://<pod>:9187/metrics`** (§4, §5) — the full `cnpg_*` metric surface, plain HTTP
   unless `spec.monitoring.tls.enabled`.
5. **`GET http://<pooler-pod>:9127/metrics`** (§5.4) — the entirely-unused PgBouncer surface.
6. **`GET http://<operator-pod>:8080/metrics`** — `controller_runtime_reconcile_total` per
   controller, `controller_runtime_webhook_requests_total` per webhook.
