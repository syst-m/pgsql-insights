/* The Fleet table (R10.4).
 *
 * Every column the page shows is sortable, and the sort lives in the URL
 * (R10.16) — so this component owns no sort state of its own. It renders the
 * rows it is handed and reports clicks upward; the page owns ordering.
 *
 * `aria-sort` on the active header is what a screen reader uses to announce
 * "Health, sorted descending"; the caret glyph is the visual twin of it.
 */

import type { SortKey, UrlState } from "../state/urlState";
import type { FleetRow } from "../api/types";
import { StatusChip } from "./StatusGlyph";
import { formatNumber, formatPercent, formatSize, formatTimestamp, formatDurationMs, NULL_VALUE } from "../lib/format";

export interface Column {
  key: SortKey;
  label: string;
  /** Right-align and use tabular figures — for every numeric column. */
  numeric?: boolean;
}

/** The column set. Order is the reading order: state first, then identity,
 *  then the shape of the cluster, then the metrics, then freshness. */
export const COLUMNS: readonly Column[] = [
  { key: "health", label: "Health" },
  { key: "name", label: "Cluster" },
  { key: "namespace", label: "Namespace" },
  { key: "instances", label: "Instances", numeric: true },
  { key: "pgMajor", label: "PG", numeric: true },
  { key: "tps", label: "TPS", numeric: true },
  { key: "p95LatencyMs", label: "p95", numeric: true },
  { key: "connectionsPct", label: "Conn %", numeric: true },
  { key: "diskPct", label: "Disk %", numeric: true },
  { key: "observedAt", label: "Observed", numeric: true },
];

export interface ClusterTableProps {
  rows: readonly FleetRow[];
  state: UrlState;
  onSort: (key: SortKey) => void;
  onOpen: (clusterId: string) => void;
}

export function ClusterTable({ rows, state, onSort, onOpen }: ClusterTableProps) {
  return (
    <div className="table-scroll">
      <table className="data" data-testid="cluster-table">
        <caption className="sr-only">
          PostgreSQL clusters in the fleet, sorted by {state.sort} {state.dir === "asc" ? "ascending" : "descending"}
        </caption>
        <thead>
          <tr>
            {COLUMNS.map((col) => {
              const active = state.sort === col.key;
              return (
                <th
                  key={col.key}
                  scope="col"
                  className={col.numeric ? "num" : undefined}
                  {...(active ? { "aria-sort": state.dir === "asc" ? ("ascending" as const) : ("descending" as const) } : {})}
                >
                  <button type="button" className="sort-btn" onClick={() => onSort(col.key)}>
                    <span>{col.label}</span>
                    {active && (
                      <span className="caret" aria-hidden>
                        {state.dir === "asc" ? "▲" : "▼"}
                      </span>
                    )}
                  </button>
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => {
            const id = `${c.namespace}/${c.name}`;
            const selected = state.row === id;
            return (
              <tr key={id} data-selected={selected || undefined} data-health={c.health}>
                <td>
                  <StatusChip health={c.health} />
                </td>
                <td>
                  <a
                    className="cluster-link"
                    href={`/c/${encodeURIComponent(c.namespace)}/${encodeURIComponent(c.name)}`}
                    onClick={(e) => {
                      // Keep the browser's modifier behaviours (open in new tab)
                      // and let the router own plain clicks.
                      if (e.metaKey || e.ctrlKey || e.button === 1) return;
                      e.preventDefault();
                      onOpen(id);
                    }}
                  >
                    {c.name}
                  </a>
                </td>
                <td>{c.namespace}</td>
                <td className="num" title={`${c.readyInstances} ready, ${c.servingInstances} serving of ${c.instances} (R1.2)`}>
                  {c.servingInstances}/{c.instances}
                </td>
                <td className="num">{c.pgMajor === null ? NULL_VALUE : c.pgMajor}</td>
                <td className="num">{formatNumber(c.metrics?.tps ?? null)}</td>
                <td className="num">{formatDurationMs(c.metrics?.p95LatencyMs ?? null)}</td>
                <td className="num">{formatPercent(c.metrics?.connectionsPct ?? null)}</td>
                <td className="num">{formatPercent(c.metrics?.diskPct ?? null)}</td>
                <td className="num" title={formatTimestamp(c.observedAt, { withZone: true })}>
                  {formatTimestamp(c.observedAt, { withZone: false })}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** The Instances cell's tooltip text, exported so the test can assert R1.2's
 *  three counts are all visible rather than only the spec count. */
export function instancesTooltip(c: FleetRow): string {
  return `${c.readyInstances} ready, ${c.servingInstances} serving of ${c.instances} (R1.2)`;
}

/** The Storage cell for the detail page's PVC table. */
export function pvcCell(size?: string): string {
  return formatSize(size ?? null);
}
