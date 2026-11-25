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
 *                   `QueryView`, `QueryDetailView`, `TagView` and the rest of
 *                   the second block mirror `internal/api/dto/queries.go`
 *                   (A5's file, R3.x) the same way; its pin — importing
 *                   `testdata/api/queries.json` the way clusters.json is
 *                   imported — lands with that fixture's commit, because a type
 *                   importing an uncommitted file compiles in a working tree and
 *                   fails at the tip (the `45a69a1` class, one level down).
 *                   The third block — `QuerySeriesPoint`, `QuerySeries`,
 *                   `QuerySeriesEnvelope` — mirrors `internal/api/series.go`,
 *                   which is package `api` and not `dto`, so it is the one block
 *                   here not sourced from the dto package. It is here because the
 *                   contract is published and has no other TS home:
 *                   `api/openapi.yaml` ships all three as schemas with
 *                   `x-status: implemented` (`4cef345`), and `QueryDetailView`
 *                   below points at that route for its own chart — "Ask the
 *                   series endpoint for the chart instead of expecting it on this
 *                   object" — so the pointer currently names a payload with no
 *                   type. Its pin is the published example, inlined in
 *                   client.test.ts, not a fixture: `4cef345` shipped the route
 *                   without its R16.8 `testdata/api/query-series.json`, and when
 *                   A2 add one the test should read the file instead of the
 *                   literal. The PromQL response types (`internal/api/promql.go`)
 *                   are the other package-`api` payloads with no TS side; they
 *                   are not added here because nothing in `web/` consumes the
 *                   PromQL routes yet, whereas the Detail chart does need this one.
 *                   The fourth block — `SessionView`, `ScorecardView`,
 *                   `BlockingChainView`, `ActiveQueriesResponse`,
 *                   `TerminateResponse` — mirrors `internal/api/dto/sessions.go`
 *                   (R4.1/R4.4, served by `e6a4921`; R4.5's body added with
 *                   `c5d1772`). It is the one block here whose response type is
 *                   not `Envelope<T>`: R4.1 puts a scorecard above the table and
 *                   R8.4's generic carries exactly `data` + `meta`, so the route
 *                   publishes a named body and this file copies that name rather
 *                   than widening the shared envelope. R4.5's single-object
 *                   answer is that block's second named body, for the reason the
 *                   dto's own "WHY NOT AN ENVELOPE" paragraph gives it — a page
 *                   and its paging describe a list, and `meta.truncated` has no
 *                   meaning for one object. Its pins are
 *                   `testdata/api/active-queries.json` and
 *                   `testdata/api/sessions-terminate.json`, imported by
 *                   fixtures.ts.
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
  /** R8.10's **pre-cut** count: matching objects across *all* pages, not the
   *  rows in this response. With 120 matching and `limit=50` this is 120 while
   *  `data.length` is 50 — which is exactly what `truncated` is defined against
   *  (`truncated ⟺ len(data) < total`).
   *
   *  v0.3.0 worded this as "counts what the response can serve, not what was
   *  attempted", and R8.4 retired that at A1's `f08b6ac`: on a truncated page
   *  the two documents then assign different numbers to the same field. The UI
   *  consequence is R10.22's — the partial banner's served count is
   *  `data.length`, never this field (`partialBanner()` in envelope.ts). */
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

/* ── wire: internal/api/dto/queries.go ──────────────────────────────────── */

/** R3.2's eight load columns, in R3.2's order.
 *
 *  Declared once and embedded by every rollup the API serves — `QueryView`,
 *  `TagView`, and through it `QueryDetailView` — because R3.2's column set is
 *  one contract that the per-query, per-tag and per-table views all render, and
 *  three copies of eight numbers is three ways for `avgExecTimeMs` to mean
 *  something different on one page than on another.
 *
 *  Two of R3.2's thirteen columns are deliberately absent from this interface
 *  and from every view that embeds it (queries.go spells out the rulings):
 *  `Load by lock wait` has no per-query source and R10.6 renders the literal
 *  `n/a (no per-query source, §5)`, and `Analyze latency` is marked **Gap** and
 *  deferred. A nullable field that is always null is a field the UI has to
 *  special-case anyway, so the literal is the UI's and the contract is silent.
 *
 *  The two derived columns carry a per-major-version difference the UI has to
 *  know about (R10.28's "PG16 vs PG17 column differences"):
 *
 *    loadByIOWaitMs = blk_read_time + blk_write_time               (PG 15, 16)
 *                   = shared_blk_read_time + shared_blk_write_time (PG 17, 18)
 *    loadByCPUMs    = totalExecTimeMs − loadByIOWaitMs
 *
 *  PG17 split the block-time counters into shared/local/temp, so the ≤PG16 pair
 *  and the PG17+ pair answer the same question from different columns and are
 *  not comparable across the boundary. `pgMajor` on the row is what tells the
 *  UI which derivation produced the numbers it is looking at. */
export interface LoadColumns {
  /** "this query's share of the cluster's total load", a percentage. */
  loadByTotalTime: number;
  /** The same numerator restated against the Queries category (R3.2). */
  pctLoadByQueries: number;
  avgExecTimeMs: number;
  totalExecTimeMs: number;
  avgRowsReturned: number;
  /** R3.3's calls delta over the requested window, not the cumulative counter. */
  timesCalled: number;
  loadByCPUMs: number;
  loadByIOWaitMs: number;
}

/** R7.1's index recommendation. R7.1 is marked post-0.1, so `recommendations`
 *  is empty in every Phase 3 response; the shape is here because R3.2's
 *  "Recommendations" column needs one and because an empty array and a missing
 *  key render differently in every table on this app. */
export interface RecommendationView {
  finding: string;
  evidence: string[];
  action: string;
  /** `string`, not a union: the enumerations R7.1 will define are post-0.1, and
   *  a value added server-side should render as unfamiliar, not fail to
   *  compile — the same reason `ClusterView.credentialSource` is a `string`. */
  confidence: string;
}

/** One row of the R3.2 table — the payload of GET /api/v1/insights/queries
 *  (R8.2) and the shape R10.6's Queries page draws.
 *
 *  The numbers are R3.3 deltas over the window the request asked for
 *  (R8.4's `from`/`to`, R10.15's presets), not pg_stat_statements' cumulative
 *  counters — the single most load-bearing fact about the row, since the view's
 *  since-reset value and Cloud SQL's "last hour" are different numbers for the
 *  same query. The window itself is not echoed here: R8.4's envelope has no
 *  window field and the client named the window it asked for.
 *
 *  N is R9.5's top-N (default 50 per cluster, range 0–1000, N=0 disabling the
 *  family), paged by R8.4's `limit`/`sort`/`nextCursor` (R8.10). */
export interface QueryView extends LoadColumns {
  /** R3.4's normalized fingerprint: the canonical key of the query store, of
   *  R8.2's `/insights/queries/{fingerprint}` path segment and of R9.5b's
   *  `fingerprint` label. 64 lowercase hex characters, because a fixed-length
   *  digest is what keeps a label value URL- and path-safe. */
  fingerprint: string;
  /** PostgreSQL's `queryid`, informational (R9.5b: "the **fingerprint is the
   *  canonical key**; `queryid` is informational") — R3.4 changes it in PG18
   *  under an unchanged statement.
   *
   *  A **string**, not a number: a Postgres bigint exceeds 2^53 and JSON
   *  numbers are IEEE-754 doubles, so a numeric `queryid` silently rounds in
   *  every browser. The dto says so; this is the copy that has to keep saying it. */
  queryid?: string;
  /** The R3.5 normalized template, truncated to the R3.6 length profile. */
  query: string;
  /** R3.6's flag: `query` is a prefix of the full template, so the fingerprint
   *  is over text longer than what is shown here. */
  truncated: boolean;
  /** `datname`. R3.2's column name is "Database"; the spec's word wins over
   *  Postgres' where the two differ (dto/clusters.go's naming rule). */
  database: string;
  namespace: string;
  cluster: string;
  /** R1.3's resolved major, and the field that resolves which side of the
   *  PG16/PG17 derivation `loadByCPUMs`/`loadByIOWaitMs` came from.
   *
   *  `number`, not `number | null` as in `ClusterView.pgMajor`: a query row
   *  only exists if the collector connected to the instance, and connecting is
   *  how the major gets resolved. The null case is R1.3's discovery state, not
   *  this contract's. */
  pgMajor: number;
  recommendations: RecommendationView[];
}

/** One row of R3.1's per-database extension-presence report.
 *
 *  Mirrors `dto.ExtensionPresenceView` — `internal/api/dto/queries.go:267` as of
 *  `8d8ddeb`, read 2026-11-24 — field-for-field. The Go side pins its own tag set
 *  with `TestQueriesResponseWireKeys` (`dto/queries_test.go`), which asks
 *  `encoding/json` what the struct emits rather than reading the field list; this
 *  is the copy that has to keep agreeing with the answer. The sha is in the cite
 *  because the line moves under an amend: re-measured at `e4ea88c` it sits at
 *  `:270`, and A2's alias swap — the other half of that sentence — has since landed.
 *
 *  `installed` is the tri-state R3.1's two detection sources force, and the `?`
 *  is the third state. The Go field is `*bool` with `omitempty` — asserted
 *  through `reflect`, not argued in prose, in the test above — so a nil pointer is
 *  *absent from the body*, `false` is *not installed* and `true` is *installed*.
 *  S2 answers for every database a driver can reach and S3 only for the ones a
 *  CNPG pod exports metrics for, so a database S2 reported and S3 has no series
 *  for is not a database without the extension; rendering R10.27's "not installed"
 *  for a database nobody measured is the difference between a gap and a wrong
 *  answer. A plain `boolean` here would collapse the two.
 *
 *  Reachable from a fetcher as of this commit: `QueryResponse` at `:1002` carries
 *  it as `pgStatStatements` — the ruling being to extend the presentation type and
 *  keep the name, the `FleetResponse` precedent at `:977-980` and the published
 *  `api/openapi.yaml:3596` both saying so. That adoption is what the Go struct's
 *  docblock dated, and the reason this type came one step ahead of it. */
export interface ExtensionPresenceView {
  namespace: string;
  cluster: string;
  /** R3.1's detection unit — "presence per database". R3.2's word `database`,
   *  not R9.3's `datname`, for the reason `dto/clusters.go` gives and `QueryView`
   *  repeats: the requirement that defines a column set names its fields. */
  database: string;
  /** `*bool` + `omitempty`: absent = not known, `false` = absent, `true` = present. */
  installed?: boolean;
}

/** R3.9's named bucket set, one row each: the eight named bounds plus the
 *  implicit `+Inf`. `le` is the upper bound in ms as a string, Prometheus-style,
 *  because `+Inf` has no JSON number and a sentinel string keeps the bucket list
 *  one uniform shape instead of special-casing its last row. */
export interface HistogramBucket {
  le: string;
  /** Cumulative: every bucket counts the calls at or below `le`, so the counts
   *  are non-decreasing and the `+Inf` count equals `latency.count`. */
  count: number;
}

/** R3.9's four stats plus the eight named buckets — R10.7's "or the R3.9
 *  fallback" branch of the latency block. */
export interface LatencyView {
  minMs: number;
  meanMs: number;
  maxMs: number;
  stddevMs: number;
  /** R3.9's percentiles are `omitempty` because v1 has no distribution to take
   *  them from: when the buckets are the source, p50/p95/p99 are absent and the
   *  UI renders the buckets instead of inventing a number. A `0` here would
   *  render as a real p50. */
  p50Ms?: number;
  p95Ms?: number;
  p99Ms?: number;
  /** True when the numbers come from R9.13's sampled histogram rather than from
   *  every execution. */
  sampled: boolean;
  buckets: HistogramBucket[];
  sumMs: number;
  /** The population these stats describe. With `sampled` true this is the
   *  sampled count, not `timesCalled` — the two differ and the UI labels them
   *  with different words. */
  count: number;
}

/** R3.10's capture methods. Documented as a union, used as a `string` on the
 *  field, for the same reason as `CredentialSource`. */
export type PlanMethod = "explain" | "prepare_execute" | "analyze";

/** R3.10's two availability states. The unavailable one is the spec's exact
 *  string, parentheses and all, because R10.28 lists it as a named empty state
 *  and an empty state is only testable if its text is fixed. */
export type PlanAvailability = "available" | "unavailable (insufficient table privileges)";

/** One captured plan (R3.10). */
export interface PlanSampleView {
  capturedAt: string;
  /** One of `PlanMethod`. */
  method: string;
  /** One of `PlanAvailability`. */
  availability: string;
  /** The statement text the plan was captured for. Present whenever there is
   *  text to report — an *unavailable* sample still names its SQL, since
   *  "which statement could you not plan" is the question the user is asking. */
  sql?: string;
  /** The `EXPLAIN` output as JSON. Absent when the capture failed.
   *
   *  `Record<string, unknown>` because the plan node shape is Postgres': the
   *  keys vary by node type and by major version, and typing them here would
   *  freeze a shape Postgres does not guarantee. */
  plan?: Record<string, unknown>;
  /** SHA-256 of the plan JSON's canonical compact encoding, hex-encoded. The
   *  identity R3.11's plan-change detection compares. Absent with the plan. */
  planHash?: string;
  /** Why the capture failed, for an unavailable sample. Absent otherwise. */
  message?: string;
}

/** R3.11's plan-change event: the two hashes, and when the change was seen. */
export interface PlanChangeView {
  detectedAt: string;
  previousPlanHash: string;
  currentPlanHash: string;
}

/** The R10.7 Query Detail page's payload: the row the user clicked, plus the
 *  detail-only blocks.
 *
 *  There is no `points`/`series` field here. The detail chart's five series are
 *  served by GET /api/v1/insights/queries/series — the same five points the
 *  sparkline column uses (R8.11's batched endpoint), requested with one
 *  `fingerprints=` value — and the dto cannot inline them: `QuerySeries` is
 *  declared in `internal/api`, which imports this package, so referencing it
 *  from here is an import cycle. Ask the series endpoint for the chart instead
 *  of expecting it on this object. */
export interface QueryDetailView extends QueryView {
  /** `usename`, R3.2's "User" column. The list view keeps it off the row
   *  (R3.12 folds it into the tag) and the detail view shows it. */
  user?: string;
  /** Earliest window in which the fingerprint was seen. */
  firstSeen: string;
  latency: LatencyView;
  plans: PlanSampleView[];
  planChanges: PlanChangeView[];
}

/** One R3.12 tag: the tag's dimensions plus R3.7's load rollup over every query
 *  carrying it. */
export interface TagView extends LoadColumns {
  /** The hash of the tag's dimension set (R3.12's `tag_hash`), and R8.2's path
   *  key for `/insights/tags/{tag_hash}`. 64 lowercase hex, so it is URL-safe
   *  like the fingerprint. */
  tagHash: string;
  // R3.12's (a) dimensions — Postgres' own, always available.
  /** `application_name`. */
  application?: string;
  /** `datname`. */
  database?: string;
  /** `usename`. */
  user?: string;
  // R3.12's (b) dimensions — from the `pgsi:` SQL-comment grammar (R3.12's key set). Absent
  // when the statement carries no comment, which is the common case and not an
  // error: a statement with no comment still has the (a) dimensions.
  action?: string;
  route?: string;
  controller?: string;
  framework?: string;
  /** Comment key `dbdriver`, metric `db_driver`. */
  dbDriver?: string;
  /** R3.7's lock wait, sourced at rollup level from `pg_locks` +
   *  `pg_stat_activity`. The per-query view has no source for this (see
   *  `LoadColumns`), the rollup does, and that asymmetry is the spec's. */
  lockWaitMs: number;
}

/* ── wire: internal/api/series.go (package api, not dto) ────────────────── */

/** One minute of one fingerprint's series (R8.11).
 *
 *  Mirrors `internal/api/dto/series.go:65` tag-for-tag. All five metric fields are
 *  `number` because Go types them all `float64`, and that is the contract's
 *  reason rather than a default: a per-minute delta derived from a cumulative
 *  counter (R3.3) is not necessarily integral even when the underlying count is,
 *  and a Prometheus sample value is a float. So `rows` here is a `number` and
 *  must not be narrowed to an int on the theory that rows are countable — the
 *  same reason `LoadColumns.avgRowsReturned` is a number and not an int. */
export interface QuerySeriesPoint {
  /** Start of the minute, RFC 3339 UTC (R10.21: every timestamp the API emits is
   *  RFC 3339 UTC). Go types this `time.Time`; the wire type is the string, as
   *  it is for `meta.generatedAt`. */
  timestamp: string;
  /** R9.4 `execution_time` for the minute, in seconds. */
  executionTimeSeconds: number;
  /** R9.4 `rows`. */
  rows: number;
  /** R9.4 `shared_blocks_hit`. */
  sharedBlocksHit: number;
  /** R9.4 `shared_blocks_read`. */
  sharedBlocksRead: number;
  /** R9.4 `lock_time`, in seconds. `number | null` per R8.3 as ruled at 7cecc49:
   *  the wire spells "no value" with `null`, not with the store's `-1` code. That
   *  code (`query_rollup.lock_wait_ms`'s no-source default, `noLockWait`) is a
   *  *milliseconds* value that `point()` assigns undivided to this *seconds*
   *  field, so its meaning depends on which side of the store boundary it is read
   *  from — `null` has no unit. The Go field is `*float64`; this is what a nil
   *  pointer serialises to. */
  lockTimeSeconds: number | null;
  /** R9.4 `io_time` for the minute, in seconds — R9.11's derived member (the sum
   *  of the two block counters above, which is what R3.2's Load by IO column
   *  publishes on a list row as `loadByIOWaitMs`). `seriesSQL` now selects the
   *  rollup's `io_wait_ms` (A5, 9a16c71), so the value is read from the column
   *  rather than being the zero its type used to give it. `number | null` from the
   *  same ruling — both fields were ruled at once so the second one inherits no
   *  unit ambiguity from the first's sentinel. */
  ioTimeSeconds: number | null;
}

/** `primary` or `replica` — the role half of R5.7's (namespace, cluster, pod)
 *  triple, on the answers that are a single instance's.
 *
 *  **Not** `InstanceRole`. That union carries a third member, `unhealthy`,
 *  because R1.2 needs a word for a pod that is up with postgres not running; the
 *  series contract's enum is the narrower two (openapi `QuerySeries.instanceRole`
 *  `enum: [primary, replica]`), since a sample is attributed to an instance that
 *  was primary or replica at sample time and "unhealthy" is not a role a series
 *  can belong to. Reusing the wider union would let a TS-only bug through: a
 *  value the document forbids but the type accepts. */
export type QuerySeriesInstanceRole = "primary" | "replica";

/** One requested fingerprint's answer from `GET /api/v1/insights/queries/series`
 *  (R8.11). Mirrors `internal/api/dto/series.go:96` tag-for-tag.
 *
 *  The vocabulary is R9.3's, not R3.2's: `datname`/`usename` here where
 *  `QueryView` above says `database`/`user`. That is not drift to be fixed.
 *  dto/queries.go:28-34 rules that "the requirement that defines a column set
 *  names its fields", so QueryView's fields are R3.2's words because R3.2 defines
 *  the Queries table and `api.QuerySeries`' are R9.3's because R9.3 defines the
 *  label family — "two vocabularies for two contracts, each traceable to the
 *  requirement that chose it". Renaming one to match the other breaks a published
 *  contract in whichever direction it goes. */
export interface QuerySeries {
  /** R3.4's normalized fingerprint — the canonical key of the query store and of
   *  this route's request (R9.5b). */
  fingerprint: string;
  /** PostgreSQL's `queryid`, informational (R9.5b). A string, not a number —
   *  see `QueryView.queryid` for the 2^53 rounding argument. */
  queryid?: string;
  namespace: string;
  cluster: string;
  /** Present only when the answer is a single instance's (R5.7's
   *  compare-up-to-5); absent when the points aggregate across the cluster's
   *  instances, which is what R10.6's table row asks for and what makes one
   *  entry per fingerprint possible at all. */
  pod?: string;
  instanceRole?: QuerySeriesInstanceRole;
  /** R9.3's database label, the other half of R9.4's row identity. */
  datname?: string;
  /** R9.3's role-that-executed label. */
  usename?: string;
  /** The per-minute series, in time order. `[]`, never null, for a fingerprint
   *  the store has never seen — an absent array and an empty one mean the same
   *  thing to the UI, and the house rule `Envelope<ClusterView>` applies by is
   *  `[]` on the wire rather than omitting the key. */
  points: QuerySeriesPoint[];
}

/** `dto.Envelope[QuerySeries]`, named the way Go names it.
 *
 *  A type **alias**, not an interface restating the fields — mirroring Go's
 *  `type QuerySeriesEnvelope = dto.Envelope[QuerySeries]` (series.go:96). The
 *  reason carries across with the shape: R8.4's envelope has exactly one
 *  definition in this codebase and `Envelope<T>` above is it, so a name that
 *  exists so the OpenAPI document has something to `$ref` must not become a
 *  second hand-written copy of it in TS any more than it did in Go. */
export type QuerySeriesEnvelope = Envelope<QuerySeries>;

/* ── wire: internal/api/dto/sessions.go ─────────────────────────────────── */

/** R4.1's one live session — a row of R4.1's list, which is R10.8's Active
 *  Queries table. Mirrors `dto.SessionView` (sessions.go:43) field-for-field,
 *  tag-for-tag.
 *
 *  The field list is not derived from the requirement text here, because
 *  sessions.go refuses that derivation in its own header ("WHY THE FIELD LIST
 *  IS NOT INVENTED HERE. Every field is the projection of a field on
 *  collector.Session…") — restating it is what broke the UI's types once
 *  already, and this copy inherits the collector's list rather than
 *  re-reading R4.1's prose.
 *
 *  Two spellings are the ones a hand-written type gets wrong. `clientPort` is a
 *  **string**, not a number: the collector keeps pg_stat_activity's text form,
 *  and typing it `number` here would make every real row a type error the day
 *  someone "fixed" it. And the fingerprint field is `queryid`, lowercase-d, the
 *  same spelling `QueryView` and `QuerySeries` both use — not `queryId` — and
 *  it is the struct's only `omitempty` field, so its absence is legal and its
 *  type is checked only when present. The contract fixture proves that half:
 *  of its three rows, the idle-in-transaction one has no `queryid`. */
export interface SessionView {
  pid: number;
  query: string;
  queryType: string;
  /** Not a closed enum. The Go side types it `string` (pg_stat_activity's
   *  `state`, plus the collector's own "idle in transaction" spelling), so an
   *  enum here would be a stricter contract than the published one — the same
   *  rule `QueryListParams.sort` follows above. */
  status: string;
  sessionDurationSeconds: number;
  transactionDurationSeconds: number;
  queryDurationSeconds: number;
  waitEventType: string;
  waitEvent: string;
  unknownWaitEvent: boolean;
  database: string;
  applicationName: string;
  userName: string;
  clientAddress: string;
  clientPort: string;
  backendType: string;
  /** `queryid`, not `queryId`; omitempty, hence optional here. */
  queryid?: string;
  /** Never null on the wire (no omitempty): a Go nil slice marshals `null`,
   *  and `parseSessionView` rejects that exactly as `parseEnvelope` rejects
   *  `"data": null`. */
  blockingPids: number[];
  /** The collector leaves this empty and the handler fills it (sessions.go:
   *  "R4.1's rule for that column depends on `--allow-actions` (R12.8), which
   *  is a runtime flag on the API server and not a fact about the session"),
   *  but it is on the wire either way, so it is required here.
   *
   *  The requirement behind that flag is R4.5 (requirements.md:330) — `POST
   *  /api/v1/sessions/{pid}/terminate`, gated behind `--allow-actions=true`,
   *  default false — which R12.8 (:473) generalises to every mutation the
   *  product performs; sessions.go cites the generalised form. R4.6 (:331) is
   *  the idle-in-transaction-age and `backend_type` row, not this gate. */
  action: string;
  /** Go `time.Time` → RFC3339 string, as every other `sourceTime` in this file. */
  sourceTime: string;
}

/** R4.1's summary header above the session table. Mirrors `dto.ScorecardView`.
 *
 *  `byState` is a map keyed by whatever `status` strings the collector
 *  produced, so the type admits any key; the parser checks that every value is
 *  a number and never enumerates the states, which would be a second copy of an
 *  enum the server owns. The two `longest*` fields are `omitempty` pointers, so
 *  both are optional — a fleet with no active query has no longest one to
 *  name, and the fixture's third row (idle in transaction, no query running) is
 *  why the second field exists separately at all. */
export interface ScorecardView {
  byState: Record<string, number>;
  longestQuery?: SessionView;
  longestIdleInTransaction?: SessionView;
  total: number;
}

/** R4.4's blocked-query investigation: one blocked session and the sessions
 *  blocking it. Mirrors `dto.BlockingChainView`; R10.8 renders it as the row
 *  expansion of the Active Queries table. */
export interface BlockingChainView {
  blocked: SessionView;
  /** No omitempty, so an array always — empty is legal, null is not. */
  blockers: SessionView[];
}

/** The body of `GET /api/v1/active-queries`. Mirrors `dto.ActiveQueriesResponse`.
 *
 *  A named struct and not `Envelope<SessionView>`, and the reason is the
 *  route's own rather than this file's (openapi.yaml says it in as many words):
 *  R4.1 asks for a scorecard *above the table*, and R8.4's generic envelope has
 *  exactly two fields, so putting the scorecard in `meta` would move a
 *  per-route field into the shared envelope — R16.8's second-copy mistake
 *  pointed the other way. This is the one list route with a named body type
 *  that is not the generic, and `data`/`meta` inside it are typed exactly as
 *  the generic's are. */
export interface ActiveQueriesResponse {
  data: SessionView[];
  scorecard: ScorecardView;
  blockingChains: BlockingChainView[];
  meta: Meta;
}

/** The body of `POST /api/v1/sessions/{pid}/terminate`. Mirrors
 *  `dto.TerminateResponse` (sessions.go:302), which is the third of the three
 *  named non-generic bodies the dto's "WHY NOT AN ENVELOPE" paragraph counts —
 *  `api.VersionResponse` (server.go:627), `ActiveQueriesResponse` (:236) and
 *  this one, with a different reason on each. This file mirrors two of the
 *  three: `ActiveQueriesResponse` above and this. `VersionResponse` has no TS
 *  type here because nothing in `web/` calls `/version` yet — the same reason
 *  `promql.go`'s payloads are absent, and the reason the header gives for
 *  keeping the wire half to types something consumes.
 *
 *  The name is the Go name, not a TS-side invention of it: A3's ruling on the
 *  A1/A2 naming question (requirements.md:525) is that a Go-side named body
 *  does not rename its TS counterpart, so the pair is `dto.TerminateResponse`
 *  ↔ `TerminateResponse` here, exactly as `dto.ActiveQueriesResponse` pairs
 *  with the interface above.
 *
 *  No field is `omitempty`, so all four are required. `terminated` is
 *  `pg_terminate_backend`'s own return value and `false` on a 200 is a real
 *  answer — the session was already gone — which is the dto's stated reason for
 *  making it a field instead of folding it into the status code. `actor` and
 *  `target` are R4.5's audit pair, carried in the body so the response cannot
 *  disagree with the log line the same request wrote by re-deriving either half
 *  from a second read; `target` is the `<namespace>/<cluster>` pair the path
 *  cannot carry, because a pid is unique only per instance. */
export interface TerminateResponse {
  pid: number;
  terminated: boolean;
  actor: string;
  target: string;
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

/** What the Queries page renders: the same `Meta` (R8.4's envelope shell is
 *  shared by every list endpoint, R8.2) with `QueryView` rows, plus R3.1's
 *  presence report. Named to match `FleetResponse` so `client.ts` has one naming
 *  pattern per endpoint — `<Noun>Response` for the presentation payload, and for
 *  this route the wire body is `dto.QueriesResponse` (`api/openapi.yaml:3596`,
 *  `required: [data, meta, pgStatStatements]` at `:3618`), not `Envelope<QueryView>`: the
 *  document retired that alias because "a route with a named struct has a name
 *  already, so keeping both would be two names for one body".
 *
 *  No presentation layer of its own (no `QueryRow`): the fleet needed one
 *  because `health` is computed and `observedAt` is denormalised, and neither
 *  exists for a query row — R3.2's columns are all served, and R7.6's
 *  recommendations ride on the row. So the presentation type is the wire type
 *  plus `meta`, which is what `toQueriesResponse()` in envelope.ts builds.
 *
 *  `pgStatStatements` is required here because the schema requires it and the Go
 *  field carries no `omitempty`. The live route answers `[]` on every request
 *  today (`internal/api/queries.go:218`), which is requirements.md:306's stated
 *  reason the R10.27 "not installed" screen stays unreachable from this route
 *  until real rows arrive — the spec's own consequence, restated, not a new claim. */
export interface QueryResponse {
  meta: Meta;
  queries: QueryView[];
  /** R3.1's per-database presence report, one row per database the process knows
   *  about; `[]` when nothing has been measured. See `ExtensionPresenceView` at
   *  `:537` for why one row per database and not one boolean per cluster. */
  pgStatStatements: ExtensionPresenceView[];
}

/** `namespace/name` — the cluster id used in URLs (R10.16) and table rows.
 *  Takes the wire type so it works on a ClusterView and on a FleetRow. */
export function clusterId(c: ClusterView): string {
  return `${c.namespace}/${c.name}`;
}
