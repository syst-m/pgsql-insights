/* The cluster detail page (R10.5's surface, scoped to what Phase 1b has data for).
 *
 * Phase 1b's job is the shell and the Fleet page; the detail pages exist here
 * so a click from the table resolves to something real rather than to a 404,
 * and so R10.16's `?tab=` contract has a page to own it. Every field rendered
 * here comes from the R1.5 contract — nothing is invented for the page.
 *
 * The replication tab is deliberately an empty state: replication statistics
 * are Phase 1a's collector work (R1.6), and showing a placeholder chart would
 * be the R10.12 failure mode (a state that looks like data).
 *
 * The cert section renders the whole `certificates.expirations` map rather than
 * three fixed rows, because that is what the contract emits: CNPG reports one
 * entry per secret, and a cluster with a custom server cert or a pooler client
 * cert has more than the three named ones. The three named secret fields say
 * which entry is which; the map is the source of truth for how many exist.
 *
 * The Overview tab also carries R10.5(b)'s Resource load panel — 6b's frame
 * around a chart Phase 7 builds — because the ruling that split R10.5 assigns
 * the panel and the graph to different phases, and this tab is where the frame
 * lives. The same tab carries R10.5(a)'s Query load chart
 * (`components/QueryLoadArea.tsx`), which is the other half of that ruling:
 * "10.5 *(Query load)* → **6b** · 10.5 *(Resource load, R5.1 cards, topology)*
 * → **7**". Both halves are mounted here because this tab is R10.5's surface;
 * the split is by part of the cell, not by which page renders it.
 *
 * ── where the Query load series come from ──────────────────────────────────
 *
 * R8.11's batched route is the only source — `client.ts:395`'s
 * `fetchQuerySeries` — and the chart takes its input as a prop rather than
 * fetching for itself, because R8.11's whole claim is that *one* response
 * serves this chart, R10.6's sparkline column and R10.7's detail chart. So the
 * page is the caller: it reads `querySeries` off its props and, while no caller
 * has one yet, defaults to the contract fixture filtered to the row's cluster —
 * the same way the row itself comes from `FIXTURE_CLUSTERS` rather than from a
 * fetch. The filter is not decoration: `QuerySeries` names its own namespace
 * and cluster (`api/types.ts:687`) and the chart plots whatever array it is
 * handed, so a batch belonging to another cluster is the one way this page can
 * be wrong without anything failing.
 *
 * Two facts about the fetcher come with it, both A2's. `topN` is a
 * `fetchQueries` (`client.ts:284`) question, because R9.13's batch exists to be
 * fed that way — one series call for N fingerprints rather than N calls — and
 * the batch is capped at 50 (`MaxBatchFingerprints`), above which the server
 * answers `InvalidRequest` rather than truncating (R8.10 forbids the clamp), so
 * a page that wants 80 needs a paging story and not a slice. And in fixture
 * mode the fetcher ignores its params entirely (`client.ts:400-403`), so the
 * fixture default and a fixture-mode fetch return the same three series — all
 * three of which belong to `open-webui/open-webui-pg`, which is why the other
 * three reference clusters still render the empty chart.
 */

import type { FleetRow, PoolerView, QuerySeries, ServiceView } from "../api/types";
import { FIXTURE_CLUSTERS, FIXTURE_QUERY_SERIES } from "../api/fixtures";
import type { ClusterTab, UrlState } from "../state/urlState";
import type { PageProps } from "../routes";
import { StatusChip } from "../components/StatusGlyph";
import { daysUntil, formatNumber, formatSize, formatTimestamp, NULL_VALUE } from "../lib/format";
import { StatePanel } from "../components/StatePanel";
import { ResourceLoadPanel } from "../components/ResourceLoadPanel";
import { QueryLoadArea, stackSeries } from "../components/QueryLoadArea";

const TABS: readonly { key: ClusterTab; label: string }[] = [
  { key: "overview", label: "Overview" },
  { key: "instances", label: "Instances" },
  { key: "replication", label: "Replication" },
  { key: "storage", label: "Storage" },
  { key: "config", label: "Config" },
  { key: "findings", label: "Findings" },
];

/** What the R10.5 route accepts, in one shape: the `PageProps` half is A4's
 *  generic, the rest is this page's.
 *
 *  `querySeries` is the page's only real input, and it is optional for the same
 *  reason the row comes from `FIXTURE_CLUSTERS` rather than from a fetch: the
 *  fetcher exists (`client.ts:395`) and no caller has one yet. The header's
 *  "where the Query load series come from" section is the argument and the
 *  fixture is the stand-in; when `useQuerySeries` lands, this prop is what it
 *  fills and the page needs no other change — the same shape
 *  `QueryDetailPageProps` uses for R10.7's chart. */
export interface ClusterDetailPageProps extends PageProps {
  /** R8.11's batched per-minute series, for this cluster and one window. */
  querySeries?: readonly QuerySeries[];
}

/** The route entry for the `cluster` page (R10.5), mounted from `routes.ts`.
 *
 *  Resolving the row here, rather than in an `App.tsx` branch, is what lets the
 *  route table hold a component: App.tsx used to look the cluster up in its
 *  if-chain and render an "Unknown cluster" card from there, which is why every
 *  new page needed an App.tsx edit. A detail page that cannot find its row is a
 *  page state, not a router state.
 *
 *  `cfg` and `fixtureOverride` stay accepted and unused — the page renders the
 *  row the URL names and does not fetch, and they exist because `PageProps` is
 *  one contract for every page, which is what replaced the if-chain.
 */
export function ClusterDetailPage(props: ClusterDetailPageProps) {
  const cluster = FIXTURE_CLUSTERS.find((c) => `${c.namespace}/${c.name}` === props.state.cluster);
  if (cluster === undefined) return <UnknownCluster {...props} />;
  return (
    <ClusterDetailBody
      cluster={cluster}
      state={props.state}
      onPatch={props.onPatch}
      querySeries={props.querySeries ?? FIXTURE_QUERY_SERIES.data}
    />
  );
}

/** A stale or hand-typed cluster id: the row is gone from the snapshot, so the
 *  page says so and offers the way back instead of rendering an empty detail. */
function UnknownCluster({ state, onPatch }: PageProps) {
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Unknown cluster</h2>
      </div>
      <p className="card-note">
        <code>{state.cluster ?? ""}</code> is not in the current fleet snapshot.
      </p>
      <button type="button" className="btn" onClick={() => onPatch({ page: "fleet", cluster: null })}>
        Back to Fleet
      </button>
    </section>
  );
}

/** Narrow a batch of R8.11 series down to the ones belonging to one cluster.
 *
 *  `QuerySeries` names its own namespace and cluster (`api/types.ts:687`) and
 *  `QueryLoadArea` plots whatever array it is handed, so a batch fetched for
 *  another cluster is the one way this page can render a chart that is wrong
 *  without anything failing. The route entry applies this to the fixture, and
 *  a caller with a real batch gets the same narrowing for free. */
export function seriesForCluster(
  cluster: Pick<FleetRow, "namespace" | "name">,
  series: readonly QuerySeries[],
): QuerySeries[] {
  return series.filter(
    (s) => s.namespace === cluster.namespace && s.cluster === cluster.name,
  );
}

export interface ClusterDetailProps {
  cluster: FleetRow;
  state: UrlState;
  onPatch: (patch: Partial<UrlState>) => void;
  /** R8.11's series for this cluster, already narrowed by `seriesForCluster`.
   *  Required here and optional on the route entry: the entry always resolves
   *  one — the prop, or the fixture — so the body never has to ask. */
  querySeries: readonly QuerySeries[];
}

function ClusterDetailBody({ cluster, state, onPatch, querySeries }: ClusterDetailProps) {
  const tab = state.tab;
  return (
    <div className="page" data-page="cluster">
      <header className="page-head">
        <div>
          <p className="breadcrumb">
            <a className="cluster-link" href="/">
              Fleet
            </a>
            <span aria-hidden> / </span>
            <span>{cluster.namespace}</span>
          </p>
          <h1 className="page-title">
            {cluster.name} <StatusChip health={cluster.health} />
          </h1>
          {/* R1.5 keeps the CNPG phase string verbatim; the chip above is
              pgsi's own judgement, derived from the counts. */}
          <p className="page-meta">
            {cluster.phase}
            {cluster.phaseReason !== undefined && cluster.phaseReason !== cluster.phase
              ? ` · ${cluster.phaseReason}`
              : ""}
          </p>
        </div>
        <div className="page-actions">
          <span className="card-meta num" title={cluster.observedAt}>
            Observed {formatTimestamp(cluster.observedAt, { withZone: false })}
          </span>
        </div>
      </header>

      <nav className="tabs" aria-label="Cluster sections">
        {TABS.map((t) => (
          <button
            key={t.key}
            type="button"
            className={t.key === tab ? "tab active" : "tab"}
            aria-current={t.key === tab ? "page" : undefined}
            onClick={() => onPatch({ tab: t.key })}
          >
            {t.label}
          </button>
        ))}
      </nav>

      {tab === "overview" && <Overview cluster={cluster} querySeries={querySeries} />}
      {tab === "instances" && (
        <Instances cluster={cluster} selectedRow={state.row} onSelect={(name) => onPatch({ row: name })} />
      )}
      {tab === "replication" && (
        <StatePanel state="empty" onAction={() => onPatch({ tab: "instances" })} />
      )}
      {tab === "storage" && <Storage cluster={cluster} />}
      {tab === "config" && <Config cluster={cluster} />}
      {tab === "findings" && <Findings cluster={cluster} />}
    </div>
  );
}

/** The `overview` tab (R10.5). `querySeries` is threaded through from the route
 *  entry rather than fetched here, because the batch is the page's, not the
 *  tab's — R10.6's table row and R10.7's detail chart read the same response
 *  (R8.11's whole claim), and a tab that fetches for itself spends the budget
 *  twice. */
function Overview({
  cluster,
  querySeries,
}: {
  cluster: FleetRow;
  querySeries: readonly QuerySeries[];
}) {
  const rows: [string, string][] = [
    [
      "Instances",
      `${cluster.servingInstances} serving · ${cluster.readyInstances} ready · ${cluster.instances} in spec`,
    ],
    ["Current primary", cluster.currentPrimary ?? NULL_VALUE],
    ["PostgreSQL major", cluster.pgMajor === null ? NULL_VALUE : String(cluster.pgMajor)],
    // R1.3 keeps the image alongside the major: the pod label's "17" cannot
    // express 17.11, and R2.4's matrix keys off the exact minor.
    ["Image", cluster.pgImage ?? NULL_VALUE],
    ["Version", cluster.pgVersionDetail ?? NULL_VALUE],
    // R1.6's zero-prerequisite acceptance reads this field: which of R1.8's
    // four precedence levels supplied the credentials.
    ["Credentials", cluster.credentialSource],
    ["Created", formatTimestamp(cluster.createdAt, { withZone: false })],
    ["Metrics", metricsCell(cluster)],
  ];
  return (
    <>
      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Overview</h2>
        </div>
        <dl className="kv">
          {rows.map(([k, v]) => (
            <div className="kv-row" key={k}>
              <dt>{k}</dt>
              <dd className={/cert|Instances|major|Created/.test(k) ? "num" : undefined}>{v}</dd>
            </div>
          ))}
        </dl>
      </section>
      {/* R10.5(a)'s slot: the chart is 6b's (components/QueryLoadArea.tsx) and
          the batch is R8.11's — the header's "where the Query load series come
          from" section is the whole argument (fetcher `client.ts:395`, top-N
          from `fetchQueries`, cap 50, fixture mode ignores params). The slot
          itself only has to say what it plots: this cluster's series, folded
          into buckets per timestamp by `stackSeries`. A cluster the payload has
          nothing for still gets the chart's own R10.13 sentence, which is the
          same honest answer `source={null}` below gives for R10.5(b). */}
      <QueryLoadArea points={stackSeries(seriesForCluster(cluster, querySeries))} />
      {/* R10.5(b)'s slot: the panel is 6b's, the graph is Phase 7's (see
          components/ResourceLoadPanel.tsx). `source={null}` is the honest
          value, not a placeholder — client.ts has no OS-metrics fetcher yet,
          so the page has not asked, which is the panel's "not queried" answer. */}
      <ResourceLoadPanel
        cluster={`${cluster.namespace}/${cluster.name}`}
        source={null}
      />
      <Certificates cluster={cluster} />
    </>
  );
}

/** R10.4's metrics cell, in words. Phase 1a publishes no metrics at all, so
 *  the honest answer is "not yet published" — not a dash, which would read as
 *  "zero" next to a real number. */
function metricsCell(cluster: FleetRow): string {
  const m = cluster.metrics;
  if (m === null || m === undefined) return "not yet published (Phase 5)";
  if (m.source === "unconfigured") return "unconfigured (R10.12 unavailable)";
  if (m.source === "failed") return "collector failed (R10.12)";
  return m.source;
}

/** Every certificate the cluster reports, not a fixed set of three. */
function Certificates({ cluster }: { cluster: FleetRow }) {
  const entries = Object.entries(cluster.certificates.expirations);
  const named: [string, string | undefined][] = [
    ["clientCA", cluster.certificates.clientCA],
    ["serverTLSSecret", cluster.certificates.serverTLSSecret],
    ["replicationTLSSecret", cluster.certificates.replicationTLSSecret],
  ];
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Certificates</h2>
        <span className="card-meta num">{entries.length}</span>
      </div>
      {entries.length === 0 ? (
        <p className="card-note">
          No certificate expirations reported. {named.filter(([, v]) => v === undefined).length === 3
            ? "No secret names are set either, so this cluster uses secrets CNPG did not create."
            : "The secret names are set but CNPG has not reported their notAfter yet."}
        </p>
      ) : (
        <div className="table-wrap">
          <table className="data">
            <caption className="sr-only">Certificates on {cluster.name}</caption>
            <thead>
              <tr>
                <th scope="col">Secret</th>
                <th scope="col">Role</th>
                <th scope="col" className="num">
                  Expires
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map(([secret, notAfter]) => (
                <tr key={secret}>
                  <td>{secret}</td>
                  <td>{roleForSecret(cluster, secret) ?? NULL_VALUE}</td>
                  <td className="num" title={`${notAfter} UTC`}>
                    {certCell(notAfter)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

/** Which of CNPG's three named secrets this entry is, if it is one of them.
 *  Extra secrets (a custom server cert, a pooler client cert) have no role to
 *  name, and rendering "—" there is honest where the old three-field type had
 *  to drop them entirely. */
function roleForSecret(cluster: FleetRow, secret: string): string | undefined {
  const c = cluster.certificates;
  if (secret === c.clientCA) return "client CA";
  if (secret === c.serverTLSSecret) return "server TLS";
  if (secret === c.replicationTLSSecret) return "replication TLS";
  return undefined;
}

/** Cert cell with the days-left count, which is the part a reader acts on. */
function certCell(value?: string): string {
  if (value === undefined) return NULL_VALUE;
  const days = daysUntil(value);
  return days === null ? value : `${formatShort(value)} · ${days}d`;
}

function formatShort(value: string): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return new Intl.DateTimeFormat("en-GB", { timeZone: "UTC", day: "2-digit", month: "short", year: "numeric" })
    .format(d)
    .replace(/ /g, " ");
}

/** The instance list — four of R10.28's seven named states (:439).
 *
 *  R10.28 enumerates the states the reference environment actually produces
 *  rather than letting each one fall out of a default render, and four of the
 *  seven are instance-level, so they are this table's: *healthy 2-instance*,
 *  *single-instance*, *Pending replica*, and *CrashLoopBackOff on a CR that
 *  reports healthy* (R1.2). The other three have other owners — *no Pooler* is
 *  `Poolers` below, *PG16 vs PG17 column differences* is
 *  `../components/QueryTable.tsx`, *extension-absent* is
 *  `../components/ExtensionNotInstalled.tsx` — and naming them here is what
 *  keeps the catalogue one list with one owner per entry.
 *
 *  The first two are the count in `card-meta`: a one-pod cluster and a two-pod
 *  cluster are different states, not merely different row counts. The other two
 *  are the `role`/`serving`/`containerStatus` trio, and the pair worth stating
 *  is `serving` + `containerStatus` — R1.2's point is that a CR can report
 *  healthy while its pod sits in CrashLoopBackOff (`freeidp-pg-2`), so a table
 *  that rendered only `ready` would show R10.28's worst state as its best one.
 *  That is why the cell carries the container's state verbatim instead of
 *  deriving a glyph from the pod phase.
 */
function Instances({
  cluster,
  selectedRow,
  onSelect,
}: {
  cluster: FleetRow;
  selectedRow: string | null;
  onSelect: (name: string) => void;
}) {
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Instances</h2>
        <span className="card-meta num">{cluster.instanceList.length}</span>
      </div>
      <div className="table-wrap">
        <table className="data">
          <caption className="sr-only">Pods belonging to {cluster.name}</caption>
          <thead>
            <tr>
              <th scope="col">Pod</th>
              <th scope="col">Role</th>
              <th scope="col">Node</th>
              <th scope="col">Ready</th>
              <th scope="col">Serving</th>
              <th scope="col">Container</th>
              <th scope="col" className="num">
                Restarts
              </th>
              <th scope="col" className="num">
                Started
              </th>
              <th scope="col">Endpoints</th>
            </tr>
          </thead>
          <tbody>
            {cluster.instanceList.map((i) => (
              <tr key={i.name} data-selected={selectedRow === i.name || undefined} onClick={() => onSelect(i.name)}>
                <td>{i.name}</td>
                {/* R1.2: role is absent until CNPG assigns one — freeidp-pg-2
                    has no role at all, which is a state, not a blank cell. */}
                <td>{i.role === undefined || i.role === "" ? "unassigned" : i.role}</td>
                <td>{i.node ?? NULL_VALUE}</td>
                <td>{yesNo(i.ready)}</td>
                {/* R1.2: `serving` is the container-level truth, not `ready`. */}
                <td>{yesNo(i.serving)}</td>
                {/* The field that makes a crash loop legible rather than just a
                    wrong count (live-probe §6). */}
                <td>{i.containerStatus}</td>
                <td className="num">{formatNumber(i.restartCount)}</td>
                <td className="num" title={i.startedAt === null || i.startedAt === undefined ? "no start time" : `${i.startedAt} UTC`}>
                  {formatTimestamp(i.startedAt ?? null, { withZone: false })}
                </td>
                <td>{endpointsCell(i.endpoints)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/** R1.10's per-port transport table, as the contract emits it. The scheme is
 *  per-port data, not a guess — an HTTP probe of the status port answers
 *  "Client sent an HTTP request to an HTTPS server." (live-probe §4,
 *  Finding 6) — so the URLs are rendered as given rather than rebuilt here.
 *  A pod with no address serialises as `endpoints: {}` (freeidp-pg-2). */
function endpointsCell(e: FleetRow["instanceList"][number]["endpoints"]): string {
  const parts: string[] = [];
  if (e.postgres !== undefined) parts.push(`pg ${e.postgres}`);
  if (e.metrics !== undefined) parts.push(e.metrics);
  if (e.status !== undefined) parts.push(e.status);
  if (e.local !== undefined) parts.push(e.local);
  return parts.length === 0 ? NULL_VALUE : parts.join(" · ");
}

function yesNo(v: boolean): string {
  return v ? "yes" : "no";
}

function Storage({ cluster }: { cluster: FleetRow }) {
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Storage</h2>
        <span className="card-meta num">{cluster.pvcs.length}</span>
      </div>
      <div className="table-wrap">
        <table className="data">
          <caption className="sr-only">PersistentVolumeClaims attached to {cluster.name}</caption>
          <thead>
            <tr>
              <th scope="col">PVC</th>
              <th scope="col">Instance</th>
              <th scope="col" className="num">
                Size
              </th>
              <th scope="col">StorageClass</th>
              <th scope="col">Phase</th>
              <th scope="col">Access</th>
            </tr>
          </thead>
          <tbody>
            {cluster.pvcs.map((p) => (
              <tr key={p.name}>
                <td>{p.name}</td>
                {/* Carried explicitly rather than derived from the name, so a
                    renamed or orphaned volume still renders on the right row. */}
                <td>{p.instance ?? NULL_VALUE}</td>
                <td className="num">{formatSize(p.size ?? null)}</td>
                <td>{p.storageClass ?? NULL_VALUE}</td>
                <td>{p.phase ?? NULL_VALUE}</td>
                <td>{p.accessModes?.join(", ") ?? NULL_VALUE}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Config({ cluster }: { cluster: FleetRow }) {
  return (
    <>
      <Poolers cluster={cluster} />
      <Services cluster={cluster} />
    </>
  );
}

/** R10.28's *no Pooler* entry (:439), and the reason the empty case is a
 *  sentence in a card rather than a table with one `—` row: the requirement's
 *  argument is that a designed absence reads differently from an accidental one,
 *  so the wording is the state. It names the reference environment's
 *  distribution (R1.4 — only `rev-proxy-pg` has a pooler) which is what lets a
 *  reader tell "this cluster has no pooler" from "this app cannot see poolers";
 *  the latter is R10.22's banner, not this card's job.
 */
function Poolers({ cluster }: { cluster: FleetRow }) {
  if (cluster.poolers.length === 0) {
    return (
      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Poolers</h2>
        </div>
        <p className="card-note">
          No Pooler is attached to this cluster. Only <code>rev-proxy-pg</code> has one in the
          reference environment (R1.4).
        </p>
      </section>
    );
  }
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Poolers</h2>
        <span className="card-meta num">{cluster.poolers.length}</span>
      </div>
      <div className="table-wrap">
        <table className="data">
          <caption className="sr-only">Poolers attached to {cluster.name}</caption>
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Type</th>
              <th scope="col">Pool mode</th>
              <th scope="col" className="num">
                Ready
              </th>
              <th scope="col">Service</th>
              <th scope="col">Image</th>
              <th scope="col">Parameters</th>
            </tr>
          </thead>
          <tbody>
            {cluster.poolers.map((p) => (
              <PoolerRow key={`${p.namespace}/${p.name}`} p={p} />
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function PoolerRow({ p }: { p: PoolerView }) {
  return (
    <tr>
      <td>{p.name}</td>
      <td>{p.type ?? NULL_VALUE}</td>
      {/* R6's client-attribution problem keys off this value. */}
      <td>{p.poolMode ?? NULL_VALUE}</td>
      <td className="num">
        {p.readyInstances}/{p.instances}
      </td>
      <td>{p.service ?? NULL_VALUE}</td>
      <td>{p.image ?? NULL_VALUE}</td>
      {/* spec.pgbouncer.parameters verbatim. The old type flattened three of
          them into numbers; the contract keeps the whole map, so a parameter
          we never anticipated is still visible. */}
      <td>{formatParams(p.parameters)}</td>
    </tr>
  );
}

/** `k=v` pairs, sorted so the cell is stable across renders. */
function formatParams(params: Record<string, string> | undefined): string {
  if (params === undefined || Object.keys(params).length === 0) return NULL_VALUE;
  return Object.entries(params)
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([k, v]) => `${k}=${v}`)
    .join(", ");
}

/** R1.5's "services". CNPG creates -rw, -ro and -r per cluster plus one per
 *  Pooler; the port list is objects, not numbers, because CNPG names its ports
 *  and a bare `5432` loses which one is postgres and which is pgbouncer. */
function Services({ cluster }: { cluster: FleetRow }) {
  if (cluster.services.length === 0) {
    return (
      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Services</h2>
        </div>
        <p className="card-note">No Services reported for this cluster.</p>
      </section>
    );
  }
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Services</h2>
        <span className="card-meta num">{cluster.services.length}</span>
      </div>
      <div className="table-wrap">
        <table className="data">
          <caption className="sr-only">Services attached to {cluster.name}</caption>
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Role</th>
              <th scope="col">Type</th>
              <th scope="col">Ports</th>
              <th scope="col">Cluster IP</th>
            </tr>
          </thead>
          <tbody>
            {cluster.services.map((s) => (
              <ServiceRow key={s.name} s={s} />
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function ServiceRow({ s }: { s: ServiceView }) {
  return (
    <tr>
      <td>{s.name}</td>
      {/* rw | ro | r | pooler, read by the API from the name suffix — the CNPG
          label differs between 1.29 and 1.30, so the label is not stable. */}
      <td>{s.role ?? NULL_VALUE}</td>
      <td>{s.type ?? NULL_VALUE}</td>
      <td className="num">
        {s.ports === undefined || s.ports.length === 0
          ? NULL_VALUE
          : s.ports.map((pt) => (pt.name === undefined ? `${pt.port}` : `${pt.name}:${pt.port}`)).join(", ")}
      </td>
      <td>{s.clusterIP ?? NULL_VALUE}</td>
    </tr>
  );
}

function Findings({ cluster }: { cluster: FleetRow }) {
  const f = cluster.topFinding;
  if (f === null || f === undefined) {
    return (
      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Findings</h2>
        </div>
        <p className="card-note">
          No findings. Findings need a metrics source (R10.4); this cluster reports {metricsCell(cluster)}.
        </p>
      </section>
    );
  }
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Findings</h2>
      </div>
      <div className={`finding ${f.severity}`} data-severity={f.severity}>
        <span className="finding-code">{f.code}</span>
        <span className="finding-severity">{f.severity}</span>
        <p className="finding-summary">{f.summary}</p>
      </div>
    </section>
  );
}
