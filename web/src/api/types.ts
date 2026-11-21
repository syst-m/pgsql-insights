/* The GET /api/v1/clusters contract, as the Go side defines it (R8.1, R16.8).
 *
 * Two families of type live here and they are deliberately not the same:
 *
 *   wire          — `ClusterView`, `Meta`, `InstanceView` and the rest mirror
 *                   internal/api/dto/clusters.go field-for-field, tag-for-tag.
 *                   That file is authoritative (A1 owns R1.x); this one is a
 *                   copy that must not drift. R16.8's whole point is that two
 *                   copies of a contract disagree silently, so the copy is
 *                   pinned two ways: fixtures.ts imports
 *                   testdata/api/clusters.json — the same file the Go
 *                   TestGoldenFixtureIsCanonical re-marshals — and assigns it
 *                   to `Envelope<ClusterView>`, so a renamed json tag is a
 *                   compile error here rather than a page that renders empty.
 *
 *   presentation  — `FleetRow`, what the Fleet table actually draws: a
 *                   ClusterView plus the fields the UI computes or owns and
 *                   the API deliberately does not send.
 *
 * The split exists because the first version of this file put `health`,
 * `metrics` and `topFinding` on the wire type. None of the three are on the
 * wire: `health` is R1.2's rollup, computed in health.ts from the counts the
 * API does send, and `metrics`/`topFinding` are Phase 5's R9 contract and
 * Phase 6's insight engine — dto/clusters.go says so in as many words ("the
 * R10.4 metric columns and the 'top finding' column are deliberately absent
 * from ClusterView… guessing their shape now would commit Phase 1a to a
 * contract Phase 5 owns"). Claiming them as wire fields meant the type
 * described an endpoint that did not exist, and nothing caught it until A1's
 * contract landed. They live here as presentation types now, optional on the
 * row, so Phase 5 can populate them without touching this file's wire half.
 */

/* ── wire: internal/api/dto/clusters.go ─────────────────────────────────── */

/** R8.5's error code enum. Mirrors the dto.Err* constants; R8.5 requires the
 *  codes be documented, which is why they are a named union rather than
 *  `string` at each call site. */
export type ErrorCode =
  | "InvalidRequest"
  | "Unauthorized"
  | "Forbidden"
  | "NotFound"
  | "MethodNotAllowed"
  | "NotReady"
  | "Unavailable"
  | "PromQLUnavailable"
  | "Unsupported"
  | "Internal";

/** R8.5's single error shape. `details` is always present (possibly empty) so
 *  clients never branch on its existence — Go's NewError normalises a nil
 *  slice to `[]` precisely because `null` is a different JSON type. Do not
 *  write `if (err.details)` in the client. */
export interface ErrorDetail {
  code: ErrorCode;
  message: string;
  details: string[];
}

export interface ErrorBody {
  error: ErrorDetail;
}

/** R8.4's pagination metadata, carried by every list endpoint. */
export interface Meta {
  /** Matching objects before the page was cut. Counts clusters the response
   *  can *serve*: a 3-of-4 page says `total: 3` and names the fourth in
   *  `failed`, because "attempted" would make `total` mean two things. */
  total: number;
  /** True when `data` holds fewer objects than `total` — i.e. there is another
   *  page (R8.10). Purely pagination: a page can be complete and still have a
   *  failed namespace, and a truncated page can have zero failures. */
  truncated: boolean;
  /** RFC 3339 UTC this inventory snapshot was produced (R8.4, R10.21). The
   *  Fleet page's "Data as of" stamp reads this, not a per-cluster field. */
  generatedAt: string;
  /** Opaque base64 cursor for the next page (R8.10); `omitempty`, so absent
   *  on the last page. Callers treat it as opaque. */
  nextCursor?: string;
  /** R10.22's partial-render banner, per namespace. Absent when every
   *  namespace in scope synced — the common case, hence `omitempty`. */
  failed?: NamespaceFailure[];
  /** Namespaces in their first discovery pass, so their clusters are not in
   *  `data` yet. R10.22's "3 of 4 shown" case. */
  loading?: string[];
}

/** R10.22's per-namespace discovery failure. The unit is the namespace, not
 *  the cluster, because that is the unit discovery fails at (R1.1's allow-list
 *  and R12's RBAC both scope by namespace): a namespace whose watch errored
 *  contributes no clusters, so there is no ClusterView to hang an error on. */
export interface NamespaceFailure {
  namespace: string;
  /** Same R8.5 enum as ErrorDetail.Code — deliberately, so the UI needs one
   *  vocabulary for whole-request and partial failures. */
  code: ErrorCode;
  message: string;
  /** When this namespace last synced, so the banner can say "stale since
   *  09:12" and not only "failed". A pointer in Go because `omitempty` never
   *  fires on a struct value — a bare time.Time would emit
   *  0001-01-01T00:00:00Z for "never synced". */
  lastSuccessTime?: string;
}

/** R8.4's response wrapper for every list endpoint. `data` + `meta`, nothing
 *  else — the earlier `FleetResponse{generatedAt, clusters, partial}` shape
 *  was invented on this side and the endpoint does not emit it. */
export interface Envelope<T> {
  data: T[];
  meta: Meta;
}

/** The R1.10 per-port transport table for one instance, as the API emits it.
 *  Part of the contract because the scheme is per-port data, not a guess: an
 *  HTTP probe of the status port returns "Client sent an HTTP request to an
 *  HTTPS server." (live-probe §4, Finding 6). Every field is `omitempty`, so a
 *  pod with no address serialises as `"endpoints": {}` (freeidp-pg-2). */
export interface InstanceEndpoints {
  /** The 5432 wire address ("10.42.2.39:5432"), not an HTTP URL. */
  postgres?: string;
  /** 9187, the instance-manager Prometheus endpoint. http://, or https://
   *  when spec.monitoring.tls.enabled. */
  metrics?: string;
  /** 8000, the instance-manager API. Always https://. */
  status?: string;
  /** 8010, pod-local; populated only when the probe path is exec/port-forward. */
  local?: string;
}

/** The documented values of `InstanceView.role` (R1.2: `cnpg.io/instanceRole`,
 *  falling back to the deprecated `role` label). The wire field is `string`,
 *  not this union — same reason `ConditionView.status` is: CNPG writes these
 *  three today and a CRD can add more, so a union on the wire type would make
 *  an unknown role a type error instead of a value to render. */
export type InstanceRole = "primary" | "replica" | "unhealthy";

/** One instance pod of a Cluster (R1.2, R1.5). */
export interface InstanceView {
  name: string;
  /** See InstanceRole. Absent on a pod that has not been assigned one yet
   *  (freeidp-pg-2), which is why it is optional rather than `""`. */
  role?: string;
  /** spec.nodeName; absent while the pod is unscheduled. */
  node?: string;
  /** The pod's ContainersReady condition — R1.5's "ready flags". */
  ready: boolean;
  /** R1.2: the pod's `postgres` container is actually running. A pod can be
   *  Running with the container in CrashLoopBackOff, so this is not `ready`. */
  serving: boolean;
  /** Running | Pending | Succeeded | Failed | Unknown. */
  phase: string;
  /** The `postgres` container's state verbatim: "running",
   *  "CrashLoopBackOff", "PodInitializing", … — the field that makes R1.2's
   *  crash-loop case legible instead of just a wrong count. No omitempty. */
  containerStatus: string;
  /** The postgres container's restartCount (1551 on freeidp-pg-1). No
   *  omitempty, so 0 is emitted rather than omitted. */
  restartCount: number;
  /** status.podIP; absent while the pod has no address. */
  podIP?: string;
  /** True when the pod is in the Cluster's fencedInstances annotation — a pod
   *  CNPG is deliberately keeping out of the topology. */
  fenced?: boolean;
  /** The pod's startTime; absent while Pending. RFC 3339 UTC (R8.4). */
  startedAt?: string | null;
  endpoints: InstanceEndpoints;
}

/** One entry of a Cluster's status.conditions (R1.5). `status` is a string,
 *  not a True|False|Unknown union: CNPG writes those three, and a CRD we do
 *  not control can add others. */
export interface ConditionView {
  type: string;
  status: string;
  reason?: string;
  message?: string;
  lastTransitionTime: string;
}

/** R1.5's "cert expirations". Go emits CNPG's secret-name→notAfter map rather
 *  than three flat fields, because a cluster with custom secrets has more than
 *  three and the UI should show all of them. The three named secret fields say
 *  which secret is which. */
export interface CertificateExpirations {
  clientCA?: string;
  serverTLSSecret?: string;
  replicationTLSSecret?: string;
  /** Secret name → notAfter, normalised to RFC 3339 UTC so the UI never parses
   *  two time formats (CNPG writes Go's time.String() otherwise). No
   *  omitempty: always an object, possibly empty.
   *
   *  `string | undefined` is TypeScript's, not the wire's: Go's
   *  `map[string]string` never holds an undefined value. But `resolveJsonModule`
   *  infers the fixture's four clusters as a *union* of object shapes, and
   *  normalises each member to carry the other members' keys as `?: undefined`
   *  — so `cookbook-pg-ca` is `string | undefined` on rev-proxy-pg's shape.
   *  Typing this as `Record<string, string>` makes that normalisation an
   *  assignability error, and the only alternative is a cast on the import —
   *  which is the exact thing that shipped the empty Fleet page. It is also
   *  what `noUncheckedIndexedAccess` already reports for any read of a
   *  `Record<string, string>`, so nothing downstream gets looser. */
  expirations: Record<string, string | undefined>;
}

/** One PVC of a Cluster (R1.5's "PVC list"). `instance` is carried explicitly
 *  rather than derived, so a renamed or orphaned volume still renders against
 *  the right row. */
export interface PVCView {
  name: string;
  instance?: string;
  size?: string;
  storageClass?: string;
  accessModes?: string[];
  /** Bound | Pending | Lost. */
  phase?: string;
}

/** One port of a Service. CNPG names its ports, which is why this is an array
 *  of objects and not a `number[]` — a bare port list loses which one is
 *  postgres and which is pgbouncer. Go normalises Kubernetes' IntOrString
 *  targetPort to a string ("5432", or "pgbouncer" for a named one). */
export interface ServicePort {
  name?: string;
  protocol?: string;
  port: number;
  targetPort?: string;
}

/** One Service of a Cluster (R1.5's "services"). CNPG creates -rw, -ro and -r
 *  per cluster plus one per Pooler. */
export interface ServiceView {
  name: string;
  /** ClusterIP | LoadBalancer | NodePort. */
  type?: string;
  /** rw | ro | r | pooler, read from the name suffix (the CNPG label differs
   *  between 1.29 and 1.30, so the suffix is the stable read). */
  role?: string;
  ports?: ServicePort[];
  /** spec.clusterIP; "None" for headless services. */
  clusterIP?: string;
}

/** One pod of a Pooler Deployment (cnpg.io/podRole=pooler, R1.4). */
export interface PoolerPodView {
  name: string;
  node?: string;
  phase: string;
  ready: boolean;
  ip?: string;
}

/** A pgbouncer Pooler discovered per R1.4 and attached through
 *  spec.cluster.name. The same shape backs GET /api/v1/poolers (R8.2), which
 *  is why `namespace` is carried even on the nested copy.
 *
 *  The old `PoolerRef{mode, defaultPoolSize, maxClientConn, minPoolSize, kind}`
 *  flattened pgbouncer's config into three numbers. The contract keeps
 *  `parameters` as the CR's own map of strings, so a parameter we never
 *  anticipated (pool_mode's siblings, max_db_connections) is still visible. */
export interface PoolerView {
  name: string;
  namespace: string;
  /** spec.cluster.name — R1.4's association rule. */
  cluster?: string;
  /** spec.type: rw | ro. */
  type?: string;
  /** spec.pgbouncer.poolMode: transaction | session | statement. R6's
   *  client-attribution problem keys off this value. */
  poolMode?: string;
  /** spec.instances (the Deployment's replicas). */
  instances: number;
  readyInstances: number;
  /** status.phase ("active", …). */
  phase?: string;
  /** status.image, e.g. "ghcr.io/cloudnative-pg/pgbouncer:1.25.1". */
  image?: string;
  /** The pooler's Service name — R6's connection surface. */
  service?: string;
  /** spec.pgbouncer.parameters (default_pool_size, max_client_conn,
   *  min_pool_size, …), which R6.2's pool sizing reads. Values are strings
   *  because pgbouncer's own config is a map of strings. */
  parameters?: Record<string, string>;
  pods?: PoolerPodView[];
  /** The pgbouncer exporter endpoint on 9127 (R1.10's pooler row). */
  metricsURL?: string;
}

/** R1.8's credential precedence chain, most to least specific. Mirrors the
 *  dto.Credential* constants. R1.8 requires the SQL collectors to report
 *  `unconfigured` rather than fail when it is "none" while non-SQL collectors
 *  keep working, so the resolution has to be observable through the API.
 *
 *  The documented set, not the wire type: `ClusterView.credentialSource` is a
 *  `string`, exactly as `dto.CredentialSource string` is — same reasoning as
 *  `InstanceView.role` vs `InstanceRole` above. */
export type CredentialSource = "secret-flag" | "secret-cluster" | "configured" | "none";

/** One CloudNativePG Cluster as GET /api/v1/clusters renders it. The field set
 *  is R1.5's list, verbatim and in order. */
export interface ClusterView {
  namespace: string;
  name: string;

  /** CNPG's status.phase, passed through as the human string CNPG writes
   *  ("Cluster in healthy state", "Not enough disk space") — R1.5 asks for the
   *  CNPG phase, not an enum we invent, and the UI renders the string verbatim. */
  phase: string;
  /** status.phaseReason. `omitempty`, so it is absent on the healthy clusters
   *  and present on freeidp-pg. A required `string` here would demand a key
   *  the fixture does not have on three of four clusters. */
  phaseReason?: string;

  /** spec.instances — what the CR *claims*. */
  instances: number;
  /** Pods Kubernetes reports ready — CNPG's own number. */
  readyInstances: number;
  /** R1.2's correction: pods whose `postgres` container is actually running.
   *  On freeidp-pg the CR reports 2 and 0 serve. */
  servingInstances: number;

  /** status.currentPrimary; absent while no pod is elected. */
  currentPrimary?: string;

  /** R1.3's resolved major. Null when neither the pod label nor SQL has
   *  answered yet — a 0 would render as a real version, and R1.3's whole point
   *  is that the answer is sometimes not known yet. No omitempty: the null is
   *  always emitted, so `number | null` rather than `number?`. */
  pgMajor: number | null;
  /** The image the CR reports (status.image, else spec.imageName). */
  pgImage?: string;
  /** The full version string when known ("17.11"). Absent on freeidp-pg,
   *  whose version is unresolved — see the fixture. */
  pgVersionDetail?: string;

  /** R1.5's "instance list with roles, node names and ready flags". Named
   *  `instanceList` because `instances` above is the count. */
  instanceList: InstanceView[];

  conditions: ConditionView[];
  certificates: CertificateExpirations;
  pvcs: PVCView[];
  services: ServiceView[];
  poolers: PoolerView[];

  /** Which of R1.8's four precedence levels supplied this cluster's Postgres
   *  credentials — the field R1.6's zero-prerequisite acceptance reads.
   *
   *  `string`, mirroring `dto.CredentialSource string`; the documented values
   *  are the CredentialSource union above. A union here would make the JSON
   *  import infer an error (it infers `string` for every JSON property), and
   *  the Go side deliberately leaves the field open for the same reason it
   *  leaves `role` and `condition.status` open: a constant added server-side
   *  should render as an unfamiliar value, not fail to compile. */
  credentialSource: string;

  /** metadata.creationTimestamp — the cluster's age column. "when discovery
   *  last saw it" is meta.generatedAt, not this. */
  createdAt: string;
}

/* ── presentation ────────────────────────────────────────────────────────── */

/** pgsi's own health rollup — the 4-level judgement the Fleet page colours.
 *
 *  Not a wire field: the API sends the counts, and R1.2's derivation is
 *  `servingInstances`. Computed in health.ts and attached to the row, because
 *  a CR reporting 2 instances with zero serving must not render green. */
export type ClusterHealth = "healthy" | "degraded" | "down" | "unknown";

/** R10.12's named severities. */
export type Severity = "info" | "warning" | "serious" | "critical";

/** R10.4's metric columns (TPS, p95 latency, connections %, disk %).
 *
 *  Presentation, not wire: dto/clusters.go omits them from ClusterView on
 *  purpose because they are Phase 5's R9 contract, and "adding them here later
 *  is additive; guessing their shape now would commit Phase 1a to a contract
 *  Phase 5 owns". The shape below is what the table needs today and what Phase
 *  5 is expected to fill in; when it lands, this type moves to the wire half
 *  and the row stops carrying it as optional. */
export interface ClusterMetrics {
  tps: number | null;
  p95LatencyMs: number | null;
  connectionsPct: number | null;
  diskPct: number | null;
  /** R10.12's distinction between "no source configured" and "the collector
   *  ran and failed" — the former is expected on every reachable cluster
   *  (live-probe Finding 1), the latter is an incident. */
  source: "pg_stat_statements" | "cnpg_metrics" | "unconfigured" | "failed";
  /** RFC 3339 UTC; null when nothing has been collected yet (R8.4). */
  collectedAt: string | null;
}

/** One insight row (R10.4's "top finding" column). Phase 6's insight engine;
 *  presentation-only until then, for the same reason as ClusterMetrics. */
export interface Finding {
  code: string;
  severity: Severity;
  summary: string;
}

/** A Fleet table row: one ClusterView plus what the page adds.
 *
 *  `health` is computed (health.ts), `observedAt` is the envelope's
 *  `meta.generatedAt` copied onto each row — R10.21's "data as of" stamp is
 *  per-response, not per-cluster, but the table sorts and renders it per row,
 *  so it is denormalised here. The cluster's own timestamp is `createdAt`,
 *  which the wire type carries.
 *
 *  `metrics`/`topFinding` are optional because the API does not send them yet
 *  (Phase 5 / Phase 6); the table renders "—" for absent, which is R10.12's
 *  unavailable state. */
export interface FleetRow extends ClusterView {
  health: ClusterHealth;
  observedAt: string;
  metrics?: ClusterMetrics | null;
  topFinding?: Finding | null;
}

/** What the Fleet page renders: the envelope's metadata plus the rows.
 *
 *  Named `FleetResponse` because that is what the page asks for; it is built
 *  from `Envelope<ClusterView>` by `toFleetResponse()` in envelope.ts, which is
 *  the one place the wire shape becomes the presentation shape. Keeping the
 *  name means the 14 existing `.clusters` call sites survive the rename; the
 *  shape no longer lies about the endpoint. */
export interface FleetResponse {
  meta: Meta;
  clusters: FleetRow[];
}

/** `namespace/name` — the cluster id used in URLs (R10.16) and table rows.
 *  Takes the wire type so it works on a ClusterView and on a FleetRow. */
export function clusterId(c: ClusterView): string {
  return `${c.namespace}/${c.name}`;
}
