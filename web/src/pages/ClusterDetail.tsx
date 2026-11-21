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
 */

import type { ClusterSummary } from "../api/types";
import type { ClusterTab, UrlState } from "../state/urlState";
import { StatusChip } from "../components/StatusGlyph";
import { daysUntil, formatNumber, formatSize, formatTimestamp, NULL_VALUE } from "../lib/format";
import { StatePanel } from "../components/StatePanel";

const TABS: readonly { key: ClusterTab; label: string }[] = [
  { key: "overview", label: "Overview" },
  { key: "instances", label: "Instances" },
  { key: "replication", label: "Replication" },
  { key: "storage", label: "Storage" },
  { key: "config", label: "Config" },
  { key: "findings", label: "Findings" },
];

export interface ClusterDetailProps {
  cluster: ClusterSummary;
  state: UrlState;
  onPatch: (patch: Partial<UrlState>) => void;
}

export function ClusterDetailPage({ cluster, state, onPatch }: ClusterDetailProps) {
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
            {cluster.phaseReason !== "" && cluster.phaseReason !== cluster.phase
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

      {tab === "overview" && <Overview cluster={cluster} />}
      {tab === "instances" && (
        <Instances cluster={cluster} selectedRow={state.row} onSelect={(pod) => onPatch({ row: pod })} />
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

function Overview({ cluster }: { cluster: ClusterSummary }) {
  const rows: [string, string][] = [
    ["Instances", `${cluster.servingInstances} serving · ${cluster.readyInstances} ready · ${cluster.instances} in spec`],
    ["Current primary", cluster.currentPrimary === "" ? NULL_VALUE : cluster.currentPrimary],
    ["PostgreSQL major", cluster.pgMajor === null ? NULL_VALUE : String(cluster.pgMajor)],
    ["Server cert", certCell(cluster.certs.server)],
    ["Client cert", certCell(cluster.certs.client)],
    ["Replication cert", certCell(cluster.certs.replication)],
    [
      "Metrics",
      cluster.metrics === null
        ? "no collector"
        : cluster.metrics.source === "unconfigured"
          ? "unconfigured (R10.12 unavailable)"
          : cluster.metrics.source,
    ],
  ];
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Overview</h2>
      </div>
      <dl className="kv">
        {rows.map(([k, v]) => (
          <div className="kv-row" key={k}>
            <dt>{k}</dt>
            <dd className={/cert|Instances|major/.test(k) ? "num" : undefined}>{v}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

/** Cert cell with the days-left count, which is the part a reader acts on. */
function certCell(value: string | null): string {
  if (value === null) return NULL_VALUE;
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

function Instances({
  cluster,
  selectedRow,
  onSelect,
}: {
  cluster: ClusterSummary;
  selectedRow: string | null;
  onSelect: (pod: string) => void;
}) {
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Instances</h2>
        <span className="card-meta num">{cluster.instancesList.length}</span>
      </div>
      <div className="table-scroll">
        <table className="data">
          <caption className="sr-only">Pods belonging to {cluster.name}</caption>
          <thead>
            <tr>
              <th scope="col">Pod</th>
              <th scope="col">Role</th>
              <th scope="col">Node</th>
              <th scope="col">Ready</th>
              <th scope="col">Serving</th>
              <th scope="col" className="num">
                Restarts
              </th>
              <th scope="col" className="num">
                Started
              </th>
            </tr>
          </thead>
          <tbody>
            {cluster.instancesList.map((i) => (
              <tr
                key={i.pod}
                data-selected={selectedRow === i.pod || undefined}
                onClick={() => onSelect(i.pod)}
              >
                <td>{i.pod}</td>
                <td>{i.role}</td>
                <td>{i.node === "" ? NULL_VALUE : i.node}</td>
                <td>{yesNo(i.ready)}</td>
                {/* R1.2: `serving` is the container-level truth, not `ready`. */}
                <td>{yesNo(i.serving)}</td>
                <td className="num">{formatNumber(i.restarts)}</td>
                <td className="num" title={i.startedAt === null ? "no start time" : `${i.startedAt} UTC`}>
                  {formatTimestamp(i.startedAt, { withZone: false })}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function yesNo(v: boolean): string {
  return v ? "yes" : "no";
}

function Storage({ cluster }: { cluster: ClusterSummary }) {
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Storage</h2>
        <span className="card-meta num">{cluster.pvcs.length}</span>
      </div>
      <div className="table-scroll">
        <table className="data">
          <caption className="sr-only">PersistentVolumeClaims attached to {cluster.name}</caption>
          <thead>
            <tr>
              <th scope="col">PVC</th>
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
                <td className="num">{formatSize(p.size)}</td>
                <td>{p.storageClass}</td>
                <td>{p.phase}</td>
                <td>{p.accessModes.join(", ")}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Config({ cluster }: { cluster: ClusterSummary }) {
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
      <div className="table-scroll">
        <table className="data">
          <caption className="sr-only">Poolers attached to {cluster.name}</caption>
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Kind</th>
              <th scope="col">Mode</th>
              <th scope="col" className="num">
                Instances
              </th>
              <th scope="col" className="num">
                Default pool
              </th>
              <th scope="col" className="num">
                Min pool
              </th>
              <th scope="col" className="num">
                Max clients
              </th>
            </tr>
          </thead>
          <tbody>
            {cluster.poolers.map((p) => (
              <tr key={p.name}>
                <td>{p.name}</td>
                <td>{p.kind}</td>
                <td>{p.mode}</td>
                <td className="num">{formatNumber(p.instances)}</td>
                <td className="num">{formatNumber(p.defaultPoolSize)}</td>
                <td className="num">{formatNumber(p.minPoolSize)}</td>
                <td className="num">{formatNumber(p.maxClientConn)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Findings({ cluster }: { cluster: ClusterSummary }) {
  if (cluster.topFinding === null) {
    return (
      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Findings</h2>
        </div>
        <p className="card-note">
          No findings. Findings need a metrics source (R10.4); this cluster reports{" "}
          {cluster.metrics === null ? "no collector" : cluster.metrics.source}.
        </p>
      </section>
    );
  }
  const f = cluster.topFinding;
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
