/* The Fleet table (R10.4).
 *
 * Every column the page shows is sortable, and the sort lives in the URL
 * (R10.16) — so this component owns no sort state of its own. It renders the
 * rows it is handed and reports clicks upward; the page owns ordering.
 *
 * `aria-sort` on the active header is what a screen reader uses to announce
 * "Health, sorted descending"; the caret glyph is the visual twin of it.
 *
 * ── the body is a map over the columns, not a hardcoded row ───────────────
 *
 * It used to be a hardcoded row: ten `<td>`s written out in order, with the
 * header mapping `COLUMNS` above them. That is fine while the two lists are one
 * list, and it breaks the moment a `columns` prop arrives (R10.20's persisted
 * selection): the header shrinks, the body doesn't, and every cell ends up
 * under the wrong heading — the worst kind of wrong, because nothing looks
 * broken. `renderCell` is the per-column switch now, so header and body are the
 * same map over the same `visible` array. `QueryTable` has had that shape since
 * it was written; this is the two tables converging on it.
 */

import { useMemo } from "react";
import type { ReactElement } from "react";

import type { SortKey, UrlState } from "../state/urlState";
import type { FleetRow } from "../api/types";
import { StatusChip } from "./StatusGlyph";
import { formatNumber, formatPercent, formatSize, formatTimestamp, formatDurationMs, NULL_VALUE } from "../lib/format";

export interface Column {
  key: SortKey;
  label: string;
  /** Right-align and use tabular figures — for every numeric column. */
  numeric?: boolean;
  /** A column the table cannot render without (R10.20's `ColumnDef.required`).
   *
   *  `health` and `name` are the two, for two different reasons. `health` is
   *  what the page is ordered by — `DEFAULT_STATE.sort` is `"health"` and the
   *  health bar above the table is the same fact drawn a second way. `name` is
   *  the only cell carrying the row's link (`a.cluster-link`, R10.19's "the row
   *  is the hit target"): hide it and the table still renders, every row still
   *  looks fine, and the cluster detail page becomes unreachable.
   *
   *  Before this field existed here the requirement's `required` was
   *  unrepresentable on the real column types — `ColumnMenu.ColumnDef` declares
   *  it and three places enforce it (`normalizeSelection` re-adds required keys,
   *  the checkbox renders `disabled`, `set()` keeps them), but a column type
   *  without the field means every real column is non-required and the machinery
   *  only ever fires on a test fixture. A3's S23; the `query` column on
   *  `QueryTable` is the same fix. */
  required?: boolean;
}

/** The column set. Order is the reading order: state first, then identity,
 *  then the shape of the cluster, then the metrics, then freshness. */
export const COLUMNS: readonly Column[] = [
  { key: "health", label: "Health", required: true },
  { key: "name", label: "Cluster", required: true },
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
  /** The visible columns, in order (R10.20's persisted selection). Keys not in
   *  `COLUMNS` are dropped — the same rule `normalizeSelection` applies upstream
   *  and `QueryTable` applies in its own `visible` memo. Omitted means all of
   *  them, which is what a caller that has not adopted the menu passes. */
  columns?: readonly string[];
}

/** The default set, so omitting `columns` renders all ten. */
const ALL_KEYS: readonly string[] = COLUMNS.map((c) => c.key);

export function ClusterTable({ rows, state, onSort, onOpen, columns }: ClusterTableProps) {
  const visible = useMemo(() => {
    const wanted = columns ?? ALL_KEYS;
    return COLUMNS.filter((c) => wanted.includes(c.key));
  }, [columns]);

  return (
    <div className="table-wrap">
      <table className="data" data-testid="cluster-table">
        <caption className="sr-only">
          PostgreSQL clusters in the fleet, sorted by {state.sort} {state.dir === "asc" ? "ascending" : "descending"}
        </caption>
        <thead>
          <tr>
            {visible.map((col) => {
              const active = state.sort === col.key;
              // `sortable` belongs on the th, not on the button: base.css styles
              // the header button through `th.sortable button`, so a th without it
              // renders an unstyled UA button. The class list here used to be `num`
              // alone, and the button carried a `sort-btn` class no rule ever
              // defined — 14d4df0 shipped both spellings in one commit and only the
              // CSS one was real.
              return (
                <th
                  key={col.key}
                  scope="col"
                  className={col.numeric ? "sortable num" : "sortable"}
                  {...(active ? { "aria-sort": state.dir === "asc" ? ("ascending" as const) : ("descending" as const) } : {})}
                >
                  <button type="button" onClick={() => onSort(col.key)}>
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
                {visible.map((col) => renderCell(c, col, onOpen))}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** One cell of one row, keyed by column.
 *
 *  Exported for the reason `QueryTable.QueryCell` is: the per-column rules —
 *  which cell is the status chip, which is the link, which is `—` for a null PG
 *  version, which carries the R1.2 tooltip — are assertable without the table
 *  around them. The `key` is set on the returned `<td>` so the row's `map` needs
 *  no wrapper element. */
export function renderCell(row: FleetRow, col: Column, onOpen: (clusterId: string) => void): ReactElement {
  const id = `${row.namespace}/${row.name}`;
  switch (col.key) {
    case "health":
      return <td key={col.key}><StatusChip health={row.health} /></td>;
    case "name":
      return (
        <td key={col.key}>
          <a
            className="cluster-link"
            href={`/c/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}`}
            onClick={(e) => {
              // Keep the browser's modifier behaviours (open in new tab)
              // and let the router own plain clicks.
              if (e.metaKey || e.ctrlKey || e.button === 1) return;
              e.preventDefault();
              onOpen(id);
            }}
          >
            {row.name}
          </a>
        </td>
      );
    case "namespace":
      return <td key={col.key}>{row.namespace}</td>;
    case "instances":
      // The tooltip comes from `instancesTooltip` rather than a second inline
      // copy of the sentence, so the exported function and the rendered cell
      // cannot disagree — that function exists only to be asserted against.
      return (
        <td key={col.key} className="num" title={instancesTooltip(row)}>
          {row.servingInstances}/{row.instances}
        </td>
      );
    case "pgMajor":
      return <td key={col.key} className="num">{row.pgMajor === null ? NULL_VALUE : row.pgMajor}</td>;
    case "tps":
      return <td key={col.key} className="num">{formatNumber(row.metrics?.tps ?? null)}</td>;
    case "p95LatencyMs":
      return <td key={col.key} className="num">{formatDurationMs(row.metrics?.p95LatencyMs ?? null)}</td>;
    case "connectionsPct":
      return <td key={col.key} className="num">{formatPercent(row.metrics?.connectionsPct ?? null)}</td>;
    case "diskPct":
      return <td key={col.key} className="num">{formatPercent(row.metrics?.diskPct ?? null)}</td>;
    case "observedAt":
      return (
        <td key={col.key} className="num" title={formatTimestamp(row.observedAt, { withZone: true })}>
          {formatTimestamp(row.observedAt, { withZone: false })}
        </td>
      );
  }
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
