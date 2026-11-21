/* The four reference clusters, as the Phase 1a contract describes them.
 *
 * A1 freezes `GET /api/v1/clusters` in their first Phase 1a commit; until that
 * lands, the Fleet page renders against this fixture so 1b is not blocked on
 * 1a (spec §7.2 runs the two in parallel against one contract). Every value
 * here is transcribed from docs/ENVIRONMENT.md and docs/research/live-probe.md
 * — the same sources R16.3's Go-side fixtures come from — not invented:
 *
 *   ai-api-gateway/rev-proxy-pg  2 inst  PG17  10Gi  healthy   + Pooler (the only one)
 *   cookbook/cookbook-pg         1 inst  PG17  10Gi  healthy   (single-instance path)
 *   freeidp/freeidp-pg           2 inst  PG17  10Gi  0 serving (CrashLoopBackOff + Pending)
 *   open-webui/open-webui-pg     2 inst  PG16   2Gi  healthy   (the PG16 half of the matrix)
 *
 * `metrics.source` is "unconfigured" on all four because pg_stat_statements is
 * absent on every reachable cluster (live-probe Finding 1) — which is exactly
 * R10.12's "unavailable (source not configured)" state, so the Phase 1b visual
 * suite can assert it against real data rather than a mock.
 */

import type { ClusterSummary, FleetResponse } from "./types";

/** Fixed so the visual-regression snapshots and the R10.21 timezone tests are
 *  deterministic; the live response carries the real clock instead. */
export const FIXTURE_GENERATED_AT = "2026-11-20T11:04:12Z";

const REV_PROXY_PG: ClusterSummary = {
  namespace: "ai-api-gateway",
  name: "rev-proxy-pg",
  phase: "Cluster in healthy state",
  phaseReason: "",
  health: "healthy",
  instances: 2,
  readyInstances: 2,
  servingInstances: 2,
  currentPrimary: "rev-proxy-pg-2",
  pgMajor: 17,
  instancesList: [
    {
      pod: "rev-proxy-pg-1",
      role: "replica",
      node: "dmaf5-1",
      ready: true,
      serving: true,
      restarts: 1,
      startedAt: "2026-11-18T07:22:41Z",
    },
    {
      // The fixture's /pg/status for this pod: isPrimary true, one streaming
      // replica (rev-proxy-pg-1, quorum sync), WAL archived at 10:58:08Z.
      pod: "rev-proxy-pg-2",
      role: "primary",
      node: "dmaf5-2",
      ready: true,
      serving: true,
      restarts: 0,
      startedAt: "2026-11-16T09:11:03Z",
    },
  ],
  conditions: [
    { type: "Ready", status: "True", lastTransitionTime: "2026-11-18T07:23:02Z" },
    { type: "Online", status: "True", lastTransitionTime: "2026-11-18T07:23:02Z" },
  ],
  certs: {
    server: "2027-06-30T09:12:04Z",
    client: "2027-06-30T09:12:04Z",
    replication: "2027-06-30T09:12:04Z",
  },
  pvcs: [
    {
      name: "streaming-rev-proxy-pg-1",
      size: "10Gi",
      storageClass: "iscsi-ext4",
      phase: "Bound",
      accessModes: ["ReadWriteOnce"],
    },
    {
      name: "streaming-rev-proxy-pg-2",
      size: "10Gi",
      storageClass: "iscsi-ext4",
      phase: "Bound",
      accessModes: ["ReadWriteOnce"],
    },
  ],
  services: [
    { name: "rev-proxy-pg-rw", type: "rw", clusterIP: "10.43.14.71", ports: [5432] },
    { name: "rev-proxy-pg-ro", type: "ro", clusterIP: "10.43.9.204", ports: [5432] },
    { name: "rev-proxy-pg-any", type: "any", clusterIP: "10.43.51.6", ports: [5432] },
  ],
  // The only Pooler in the reference environment (ENVIRONMENT.md §3):
  // pgbouncer, transaction pool mode, 2 instances, default_pool_size 20,
  // max_client_conn 400, min_pool_size 4.
  poolers: [
    {
      name: "rev-proxy-pg-pooler",
      mode: "transaction",
      instances: 2,
      defaultPoolSize: 20,
      maxClientConn: 400,
      minPoolSize: 4,
      kind: "pgbouncer",
    },
  ],
  metrics: {
    tps: null,
    p95LatencyMs: null,
    connectionsPct: null,
    diskPct: null,
    source: "unconfigured",
    collectedAt: null,
  },
  topFinding: null,
  observedAt: FIXTURE_GENERATED_AT,
};

const COOKBOOK_PG: ClusterSummary = {
  namespace: "cookbook",
  name: "cookbook-pg",
  phase: "Cluster in healthy state",
  phaseReason: "",
  health: "healthy",
  instances: 1,
  readyInstances: 1,
  servingInstances: 1,
  currentPrimary: "cookbook-pg-1",
  pgMajor: 17,
  instancesList: [
    {
      // Single-instance cluster: no replicas, so the replication views are
      // empty — the degenerate path R1.6's zero-prerequisite rule exercises.
      pod: "cookbook-pg-1",
      role: "primary",
      node: "k8s-wrk-1",
      ready: true,
      serving: true,
      restarts: 0,
      startedAt: "2026-09-12T06:53:31Z",
    },
  ],
  conditions: [
    { type: "Ready", status: "True", lastTransitionTime: "2026-09-12T06:54:22Z" },
    { type: "Online", status: "True", lastTransitionTime: "2026-09-12T06:54:22Z" },
  ],
  certs: {
    server: "2026-12-11T06:52:19Z",
    client: "2026-12-11T06:52:19Z",
    replication: "2026-12-11T06:52:19Z",
  },
  pvcs: [
    {
      name: "streaming-cookbook-pg-1",
      size: "10Gi",
      storageClass: "iscsi-ext4",
      phase: "Bound",
      accessModes: ["ReadWriteOnce"],
    },
  ],
  services: [
    { name: "cookbook-pg-rw", type: "rw", clusterIP: "10.43.22.18", ports: [5432] },
    { name: "cookbook-pg-ro", type: "ro", clusterIP: "10.43.77.3", ports: [5432] },
    { name: "cookbook-pg-any", type: "any", clusterIP: "10.43.64.90", ports: [5432] },
  ],
  poolers: [],
  metrics: {
    tps: null,
    p95LatencyMs: null,
    connectionsPct: null,
    diskPct: null,
    source: "unconfigured",
    collectedAt: null,
  },
  topFinding: null,
  observedAt: FIXTURE_GENERATED_AT,
};

const FREEIDP_PG: ClusterSummary = {
  namespace: "freeidp",
  name: "freeidp-pg",
  // ENVIRONMENT.md's Status column, verbatim. This is the cluster R1.2 exists
  // for: the CR reports 2 instances and zero of them serve.
  phase: "Not enough disk space",
  phaseReason: "Not enough disk space",
  health: "down",
  instances: 2,
  readyInstances: 0,
  servingInstances: 0,
  currentPrimary: "freeidp-pg-1",
  pgMajor: 17,
  instancesList: [
    {
      // live-probe §6: pod `Running`, ready=false, the postgres container is in
      // CrashLoopBackOff, and `kubectl exec` answers "container not found".
      pod: "freeidp-pg-1",
      role: "primary",
      node: "um760-1",
      ready: false,
      serving: false,
      restarts: 37,
      startedAt: "2026-11-19T22:41:55Z",
    },
    {
      // live-probe §6: Pending / PodInitializing, unschedulable.
      pod: "freeidp-pg-2",
      role: "replica",
      node: "",
      ready: false,
      serving: false,
      restarts: 0,
      startedAt: null,
    },
  ],
  conditions: [
    {
      type: "Ready",
      status: "False",
      reason: "NotEnoughDiskSpace",
      message: "no space left on device on streaming-freeidp-pg-1",
      lastTransitionTime: "2026-11-19T22:40:11Z",
    },
    {
      type: "Online",
      status: "False",
      reason: "PodPending",
      message: "pod freeidp-pg-2 is pending",
      lastTransitionTime: "2026-11-19T22:40:11Z",
    },
  ],
  certs: {
    server: "2027-01-04T18:09:44Z",
    client: "2027-01-04T18:09:44Z",
    replication: "2027-01-04T18:09:44Z",
  },
  pvcs: [
    {
      name: "streaming-freeidp-pg-1",
      size: "10Gi",
      storageClass: "iscsi-ext4",
      phase: "Bound",
      accessModes: ["ReadWriteOnce"],
    },
    {
      name: "streaming-freeidp-pg-2",
      size: "10Gi",
      storageClass: "iscsi-xfs",
      phase: "Pending",
      accessModes: ["ReadWriteOnce"],
    },
  ],
  services: [
    { name: "freeidp-pg-rw", type: "rw", clusterIP: "10.43.41.129", ports: [5432] },
    { name: "freeidp-pg-ro", type: "ro", clusterIP: "10.43.8.55", ports: [5432] },
    { name: "freeidp-pg-any", type: "any", clusterIP: "10.43.19.77", ports: [5432] },
  ],
  poolers: [],
  metrics: {
    tps: null,
    p95LatencyMs: null,
    connectionsPct: null,
    diskPct: null,
    // live-probe §3: the endpoint is not reachable at all, so this is a failed
    // source rather than an unconfigured one — R10.12's "partial" evidence.
    source: "failed",
    collectedAt: null,
  },
  topFinding: {
    code: "disk-pressure",
    severity: "critical",
    summary: "No space left on device; postgres container in CrashLoopBackOff",
  },
  observedAt: FIXTURE_GENERATED_AT,
};

const OPEN_WEBUI_PG: ClusterSummary = {
  namespace: "open-webui",
  name: "open-webui-pg",
  phase: "Cluster in healthy state",
  phaseReason: "",
  health: "healthy",
  instances: 2,
  readyInstances: 2,
  servingInstances: 2,
  currentPrimary: "open-webui-pg-2",
  // The only PG16 cluster — the version matrix half of R1.3/R2.4.
  pgMajor: 16,
  instancesList: [
    {
      pod: "open-webui-pg-1",
      role: "replica",
      node: "k8s-ctrl",
      ready: true,
      serving: true,
      restarts: 0,
      startedAt: "2026-11-15T08:02:19Z",
    },
    {
      pod: "open-webui-pg-2",
      role: "primary",
      node: "dmaf5-1",
      ready: true,
      serving: true,
      restarts: 0,
      startedAt: "2026-11-15T08:01:47Z",
    },
  ],
  conditions: [
    { type: "Ready", status: "True", lastTransitionTime: "2026-11-15T08:03:02Z" },
    { type: "Online", status: "True", lastTransitionTime: "2026-11-15T08:03:02Z" },
  ],
  certs: {
    server: "2027-05-20T07:58:33Z",
    client: "2027-05-20T07:58:33Z",
    replication: "2027-05-20T07:58:33Z",
  },
  pvcs: [
    {
      name: "streaming-open-webui-pg-1",
      size: "2Gi",
      storageClass: "iscsi-ext4",
      phase: "Bound",
      accessModes: ["ReadWriteOnce"],
    },
    {
      name: "streaming-open-webui-pg-2",
      size: "2Gi",
      storageClass: "iscsi-ext4",
      phase: "Bound",
      accessModes: ["ReadWriteOnce"],
    },
  ],
  services: [
    { name: "open-webui-pg-rw", type: "rw", clusterIP: "10.43.60.12", ports: [5432] },
    { name: "open-webui-pg-ro", type: "ro", clusterIP: "10.43.60.13", ports: [5432] },
    { name: "open-webui-pg-any", type: "any", clusterIP: "10.43.60.14", ports: [5432] },
  ],
  poolers: [],
  metrics: {
    tps: null,
    p95LatencyMs: null,
    connectionsPct: null,
    diskPct: null,
    source: "unconfigured",
    collectedAt: null,
  },
  topFinding: null,
  observedAt: FIXTURE_GENERATED_AT,
};

/** The four clusters, in the order the reference environment lists them. */
export const FIXTURE_CLUSTERS: readonly ClusterSummary[] = [
  REV_PROXY_PG,
  COOKBOOK_PG,
  FREEIDP_PG,
  OPEN_WEBUI_PG,
];

export const FIXTURE_FLEET: FleetResponse = {
  generatedAt: FIXTURE_GENERATED_AT,
  clusters: [...FIXTURE_CLUSTERS],
  partial: null,
};

/** R10.12's four panel states, as named fixtures the visual suite renders one
 *  at a time (R16.7's "named fixture" requirement, applied at Fleet scope). */
export const FIXTURE_EMPTY: FleetResponse = {
  generatedAt: FIXTURE_GENERATED_AT,
  clusters: [],
  partial: null,
};

/** R10.22: three of four clusters present, the fourth still loading. */
export const FIXTURE_PARTIAL: FleetResponse = {
  generatedAt: FIXTURE_GENERATED_AT,
  clusters: [REV_PROXY_PG, COOKBOOK_PG, OPEN_WEBUI_PG],
  partial: {
    loadingNamespaces: ["freeidp"],
    failedNamespaces: [],
    message: "1 namespace still in its first discovery pass",
  },
};

/** R10.12's "unavailable (source not configured)": clusters are discovered,
 *  but no metrics source exists for them (live-probe Finding 1). */
export const FIXTURE_UNAVAILABLE: FleetResponse = {
  generatedAt: FIXTURE_GENERATED_AT,
  clusters: [REV_PROXY_PG, COOKBOOK_PG, FREEIDP_PG, OPEN_WEBUI_PG],
  partial: null,
};
