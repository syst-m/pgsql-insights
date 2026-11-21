/* Types mirroring the GET /api/v1/clusters contract (R1.5).
 *
 * R1.5 enumerates the per-cluster fields the endpoint must expose: namespace,
 * name, phase, phaseReason, instances, readyInstances, servingInstances,
 * currentPrimary, PG major, the instance list with roles/node names/ready
 * flags, conditions, cert expirations, PVC list, services and any Poolers.
 * Every one of those has a field here, so the Fleet page cannot accidentally
 * render a subset the backend never promised.
 *
 * A1 owns the Go side of this contract (Phase 1a). Until their commit lands,
 * web/src/api/fixtures.ts serves the same shape for the four reference
 * clusters — see client.ts for how the two sources are selected.
 */

/** pgsi's own health rollup. The CNPG `status.phase` string is kept verbatim
 *  in `phase`; this enum is the 4-level judgement the Fleet page colours, and
 *  it is derived from the instance states rather than from the phase alone —
 *  R1.2's measured case is a CR that reports healthy while serving nothing. */
export type ClusterHealth = "healthy" | "degraded" | "down" | "unknown";

/** R1.2 — role comes from `cnpg.io/instanceRole`, with the deprecated `role`
 *  label as fallback. */
export type InstanceRole = "primary" | "replica" | "unhealthy";

/** R10.4's "top finding" severity, and R10.12's panel severity vocabulary. */
export type Severity = "info" | "warning" | "serious" | "critical";

export interface InstanceRef {
  pod: string;
  role: InstanceRole;
  node: string;
  /** status.conditions[Ready] — the pod-level readiness. */
  ready: boolean;
  /** R1.2: the pod's `postgres` container is actually running. A pod can be
   *  Running with the container in CrashLoopBackOff, so this is not `ready`. */
  serving: boolean;
  restarts: number;
  /** RFC 3339 UTC (R8.4's timestamp contract, R10.21). */
  startedAt: string | null;
}

export interface Condition {
  type: string;
  status: "True" | "False" | "Unknown";
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
}

/** R1.5 "cert expirations" — CNPG rotates these; the UI surfaces the soonest. */
export interface CertExpirations {
  server: string | null;
  client: string | null;
  replication: string | null;
}

export interface PvcRef {
  name: string;
  size: string;
  storageClass: string;
  phase: "Bound" | "Pending" | "Lost" | string;
  accessModes: string[];
}

export interface ServiceRef {
  name: string;
  /** CNPG creates `-rw`, `-ro` and `-any`; Poolers add `-rw-pooler`/`-ro-pooler`. */
  type: "rw" | "ro" | "any" | "rw-pooler" | "ro-pooler" | string;
  clusterIP: string;
  ports: number[];
}

/** R1.4 / R6.1 — a Pooler attached via spec.cluster.name. */
export interface PoolerRef {
  name: string;
  mode: "transaction" | "session";
  instances: number;
  defaultPoolSize: number;
  maxClientConn: number;
  minPoolSize: number;
  /** pgbouncer | md5auth-scram; R6.5's config panel reads it. */
  kind: string;
}

/** The R10.4 metric columns (TPS, p95 latency, connections %, disk %).
 *  Phase 1b ships discovery only, so every field is null on the reference
 *  clusters and `source` says why — which is R10.12's "unavailable (source not
 *  configured)" state, not a missing feature in the table. */
export interface ClusterMetrics {
  tps: number | null;
  p95LatencyMs: number | null;
  connectionsPct: number | null;
  diskPct: number | null;
  /** Why these numbers exist (or don't): the collector that produced them. */
  source: "pg_stat_statements" | "cnpg_metrics" | "unconfigured" | "failed";
  /** RFC 3339 UTC; null when nothing has been collected yet. */
  collectedAt: string | null;
}

export interface Finding {
  code: string;
  severity: Severity;
  summary: string;
}

export interface ClusterSummary {
  namespace: string;
  name: string;
  /** CNPG status.phase, verbatim (e.g. "Cluster in healthy state"). */
  phase: string;
  /** CNPG status.reason / the condition reason (e.g. "Not enough disk space"). */
  phaseReason: string;
  /** Derived, not copied from the CR — see ClusterHealth. */
  health: ClusterHealth;
  /** spec.instances — what the CR *claims*. */
  instances: number;
  readyInstances: number;
  /** R1.2: pods whose postgres container is running. */
  servingInstances: number;
  currentPrimary: string;
  /** R1.3, from the app.kubernetes.io/version pod label; null if unresolvable. */
  pgMajor: number | null;
  instancesList: InstanceRef[];
  conditions: Condition[];
  certs: CertExpirations;
  pvcs: PvcRef[];
  services: ServiceRef[];
  poolers: PoolerRef[];
  /** null when no collector has produced data for this cluster. */
  metrics: ClusterMetrics | null;
  topFinding: Finding | null;
  /** RFC 3339 UTC the discovery pass last saw this cluster (R8.4). */
  observedAt: string;
}

/** R10.22: a page that renders 3 of 4 clusters while the fourth is still
 *  loading says so, rather than showing a plausible-looking partial page. */
export interface PartialInfo {
  loadingNamespaces: string[];
  failedNamespaces: string[];
  message?: string;
}

export interface FleetResponse {
  /** RFC 3339 UTC (R8.4's `generatedAt`, R10.21's "data as of" stamp). */
  generatedAt: string;
  clusters: ClusterSummary[];
  partial?: PartialInfo | null;
}

/** `namespace/name` — the cluster id used in URLs (R10.16) and table rows. */
export function clusterId(c: ClusterSummary): string {
  return `${c.namespace}/${c.name}`;
}
