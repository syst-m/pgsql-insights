/* The Queries table (R10.6) — and the reason `useVirtualWindow` exists.
 *
 * `requirements.md:419` (R10.6) is one sentence with five features in it: "the
 * R3.2 table with sparklines per row (served by R8.11's batched endpoint, not 50
 * requests), inline load bars, filter chips (database, user, tag), search over
 * normalized text, and a saved-view concept". This file is the table; the chips
 * and the search are the page's (`pages/Queries.tsx`). The sparkline column is
 * `Sparkline` — the same data path as R10.7's detail chart, both reading R8.11's
 * batched `/insights/queries/series`, which is why R10.6's one extra column
 * landed with the Detail page rather than on its own (see QUERY_COLUMNS).
 *
 * VirtualList's header says a `<table>` cannot be windowed the way a `<div>` list
 * can: the rows have to stay inside `<tbody>`, so the caller needs the indices
 * and the two spacer heights, not a rendered container. This is that caller. It
 * calls `useVirtualWindow` and renders spacer rows with `colSpan`, which is the
 * table-shaped half of R10.18 — and R8.10 caps a page at `limit=` 500, so the
 * 500-row page R10.14's budget is quoted against is this table's normal input.
 *
 * ── columns ────────────────────────────────────────────────────────────────
 *
 * `QUERY_COLUMNS` is R3.2's thirteen (`requirements.md:309`), in R3.2's order and
 * with R3.2's names, plus R10.6's fourteenth (the per-row sparkline) appended
 * last so the thirteen keep the order §9's 6b acceptance asserts. It is the same
 * list `ColumnMenu`'s `DEFAULTS.queries` documents — the two are one fact and
 * `ColumnMenu.test.tsx` asserts it. The
 * keys are the DTO's json tags wherever the column has a field
 * (`loadByTotalTime`, `avgExecTimeMs`, …); the two columns R3.2 withholds a
 * per-query value for have no field to name, so they are named after the column
 * (`loadByLockWait`, `analyzeLatency`).
 *
 * Eleven of the thirteen are sortable. The two without a value are not, and that
 * is the one place this table departs from `ClusterTable`, which makes every
 * header a button: a sort control on a column that always renders
 * `n/a (no per-query source, §5)` is a control that cannot do anything, and R10.6's
 * "sorting must be by any column" means any column with a value to sort on.
 * The sparkline is a third non-sortable column for a different reason: it has no
 * scalar to sort on — its content is a shape — and R8.10's `?sort=` vocabulary
 * names the DTO's columns, not a chart.
 *
 * ── the load bars, and what each bar is a fraction of ──────────────────────
 *
 * LoadBars.tsx fixes the form; the open question is the denominator, since a
 * meter needs a known whole. The answers come from the DTO's own field docs
 * (internal/api/dto/queries.go:81), not from a guess:
 *
 *   Load by total time  a percentage ("this query's share of the cluster's
 *                         total load"), so the whole is 100 — not the page max.
 *   % load by Queries   a percentage too (the same numerator restated against
 *                         the Queries category; A5 flags the wording as the
 *                         spec's to supply and renders both).
 *   Load by CPU         R3.2 derives it as `total_exec_time − Load by IO wait`,
 *                         so it is a *part of* `totalExecTimeMs` and that is the
 *                         whole. Same for Load by IO wait.
 *
 * Using the page max as the denominator would make every bar on a page of
 * similar queries render near-full and hide the ranking the column exists to
 * show; using the value's own whole makes the bar mean what it says.
 *
 * The cell's tooltip also names the derivation, because `loadByIOWaitMs` means
 * `blk_read_time + blk_write_time` on PG 15–16 and
 * `shared_blk_read_time + shared_blk_write_time` on PG 17+ ((internal/api/dto/queries.go:77-78)
 * and the two are not comparable across the boundary — which is R10.28's named
 * "PG16 vs PG17 column differences" state, and `pgMajor` is the only field that
 * knows which side a row is on.
 */

import { useCallback, useMemo, useState } from "react";

import { buildPath } from "../routes";
import type { SortDir } from "../state/urlState";
import type { QuerySeries, QueryView } from "../api/types";
import { ROW_HEIGHT, useVirtualWindow, type Density } from "./VirtualList";
import {
  ANALYZE_LATENCY_GAP,
  LOCK_WAIT_NA,
  LoadCell,
  LoadSourcePending,
  type LoadCellProps,
} from "./LoadBars";
import { NULL_VALUE, formatDurationMs, formatNumber, formatPercent } from "../lib/format";
// `formatSec` is the one place a series second becomes the millisecond the shared
// formatters expect (R9.5's units rule), and it lives with the other consumer of
// series data rather than being re-derived per call site.
import { formatSec } from "./QueryLoadChart";
import { Sparkline } from "./Sparkline";

/** The eleven columns that have a value to sort on. R10.6: "Sorting must be by
 *  any column with server-side pagination (R8.10)" — the ordering itself is the
 *  API's job (R8.10's `?sort=`), so this is the vocabulary the URL carries and
 *  the header offers, not a client-side comparator. */
export type QuerySortKey =
  | "query"
  | "database"
  | "recommendations"
  | "loadByTotalTime"
  | "pctLoadByQueries"
  | "avgExecTimeMs"
  | "totalExecTimeMs"
  | "avgRowsReturned"
  | "timesCalled"
  | "loadByCPUMs"
  | "loadByIOWaitMs";

/** Every column key: the eleven sortable ones, the two R3.2 columns with no
 *  per-query value and therefore no sort, and R10.6's `sparkline` — which is in
 *  the union because `ColumnDef.key` and `DEFAULTS.queries` need to be able to
 *  name it, and not in `QuerySortKey` because a chart has no value to sort on. */
export type QueryColumnKey = QuerySortKey | "loadByLockWait" | "analyzeLatency" | "sparkline";

export interface QueryColumn {
  key: QueryColumnKey;
  /** R3.2's own wording for the column, so the header can be checked against
   *  `requirements.md:309` by reading it. */
  label: string;
  /** Right-align and use tabular figures — every numeric column. */
  numeric?: boolean;
  /** False exactly for the two columns with no per-query value. */
  sortable: boolean;
  /** A column the table cannot render without (R10.20's `ColumnDef.required`).
   *
   *  `query` is the one, and it is the reason the field exists: the row's link
   *  lives only in that cell (`QueryCell` below), so a selection without it
   *  renders a table of twelve cells with zero links — R10.7's detail page
   *  becomes unreachable while the table looks fine. `ColumnMenu` declares and
   *  enforces `required` in three places; without it on the consuming type every
   *  real column is non-required and the machinery only fires on a fixture
   *  (A3's S23; `ClusterTable.Column` is the same fix on the other table). */
  required?: boolean;
}

/** R3.2's thirteen columns, in R3.2's order, plus R10.6's fourteenth.
 *
 *  R10.6 names the sparkline in the same sentence as the table itself ("the R3.2
 *  table with sparklines per row, served by R8.11's batched endpoint, not 50
 *  requests"), but R3.2's own list at `requirements.md:309` does not have such a
 *  column, so it is appended after the thirteen rather than inserted among them —
 *  §9's 6b acceptance checks the thirteen's order, and an insertion in the middle
 *  of that list fails it for a column the requirement never ordered.
 *
 *  It renders `Sparkline`, the same component and the same data path as R10.7's
 *  detail chart: both read R8.11's `/insights/queries/series` (whose batch-of-one
 *  rationale internal/api/dto/queries.go:22-25 spells out), which is why the two shipped in one
 *  change. The data arrives through `QueryTableProps.series`, not the row —
 *  `types.ts:584` documents why `QueryView` cannot inline it.
 */
export const QUERY_COLUMNS: readonly QueryColumn[] = [
  { key: "query", label: "Query", sortable: true, required: true },
  { key: "database", label: "Database", sortable: true },
  { key: "recommendations", label: "Recommendations", sortable: true },
  { key: "loadByTotalTime", label: "Load by total time", numeric: true, sortable: true },
  { key: "pctLoadByQueries", label: "% load by Queries", numeric: true, sortable: true },
  { key: "avgExecTimeMs", label: "Avg execution time (ms)", numeric: true, sortable: true },
  { key: "totalExecTimeMs", label: "Total execution time (ms)", numeric: true, sortable: true },
  { key: "avgRowsReturned", label: "Avg rows returned", numeric: true, sortable: true },
  { key: "timesCalled", label: "Times called", numeric: true, sortable: true },
  { key: "loadByCPUMs", label: "Load by CPU", numeric: true, sortable: true },
  { key: "loadByIOWaitMs", label: "Load by IO wait", numeric: true, sortable: true },
  { key: "loadByLockWait", label: "Load by lock wait", numeric: true, sortable: false },
  { key: "analyzeLatency", label: "Analyze latency", numeric: true, sortable: false },
  // R10.6's addition, after R3.2's thirteen. Not `numeric`: the cell has no
  // figure to right-align — the number is in the tooltip and the detail page.
  { key: "sparkline", label: "Load sparkline", sortable: false },
];

/** Narrow a column key to one the table can sort on. Exported so the page and
 *  the URL layer share one answer instead of each keeping a copy of the list —
 *  which is the duplication `ClusterTable.COLUMNS` vs `urlState.SORT_KEYS` is. */
export function isQuerySortKey(key: string): key is QuerySortKey {
  return QUERY_COLUMNS.some((c) => c.key === key && c.sortable);
}

/** The R3.6 note appended to a truncated template. R3.6's default cap is 1 024
 *  bytes (Enterprise's `queryStringLength`), so a truncated row says so rather
 *  than showing a statement that ends mid-clause. */
export function truncationNote(row: QueryView): string {
  return `R3.6: the stored template is truncated at the 1 024-byte default; this is not the whole statement (${row.fingerprint})`;
}

/** The R10.7 link target for a row: the query-detail route, built by the one
 *  path table that also parses it (routes.ts), so a row can never link to a URL
 *  the back button cannot come back through. */
export function queryDetailHref(row: QueryView): string {
  return buildPath("query-detail", `${row.namespace}/${row.cluster}`, row.fingerprint);
}

/** Which of LoadBars' three cell states one of the **six load columns** renders
 *  for a row: the four bar columns and the two `n/a` ones. Any other key returns
 *  `null` — `renderCell` is the total switch over all thirteen, and this is the
 *  part of it worth testing on its own.
 *
 *  `null`, not a fourth `{kind:"none"}` member (A3's S21). A `none` in the union
 *  gives `LoadCell` a branch that renders a bare `—` for a column with no load
 *  cell at all, and `—` is R10.4's marker for *the source has not run yet* —
 *  which is not a claim `avgExecTimeMs` ever makes, and one LoadBars' own header
 *  says must not be shared with the `n/a` literal. The only thing keeping it
 *  unrendered was a caller-side `if (cell.kind !== "none")`. A `null` return is
 *  the same fact stated where a caller cannot ignore it: you cannot render a
 *  load cell for a column that has none.
 *
 *  Exported and pure so the denominators are testable without a table: the whole
 *  point of LoadBars' discriminated union is that a cell can't be in two states
 *  at once, and this is the function that decides which one. */
export function loadCellFor(row: QueryView, key: QueryColumnKey): LoadCellProps | null {
  // The derivation this row's numbers came from, for the tooltips. internal/api/dto/queries.go:77-78
  // names both sides; pgMajor picks one.
  const ioSource =
    row.pgMajor >= 17 ? "shared_blk_read_time + shared_blk_write_time" : "blk_read_time + blk_write_time";

  switch (key) {
    case "loadByTotalTime":
      // A percentage of total load (internal/api/dto/queries.go:87-91), so the whole is 100.
      return {
        kind: "bar",
        fraction: row.loadByTotalTime / 100,
        text: formatPercent(row.loadByTotalTime),
        title: `Load by total time — ${formatPercent(row.loadByTotalTime)} of the cluster's total load (PG${row.pgMajor})`,
      };
    case "pctLoadByQueries":
      return {
        kind: "bar",
        fraction: row.pctLoadByQueries / 100,
        text: formatPercent(row.pctLoadByQueries),
        title: `% load by Queries — ${formatPercent(row.pctLoadByQueries)} (the same numerator as Load by total time, restated against the Queries category)`,
      };
    case "loadByCPUMs":
      // R3.2: `total_exec_time − Load by IO wait`, so totalExecTimeMs is the
      // whole this bar is a part of.
      return {
        kind: "bar",
        fraction: row.loadByCPUMs / row.totalExecTimeMs,
        text: formatDurationMs(row.loadByCPUMs),
        title: `Load by CPU — ${formatDurationMs(row.loadByCPUMs)}, derived as total_exec_time − (${ioSource}) on PG${row.pgMajor}`,
      };
    case "loadByIOWaitMs":
      return {
        kind: "bar",
        fraction: row.loadByIOWaitMs / row.totalExecTimeMs,
        text: formatDurationMs(row.loadByIOWaitMs),
        title: `Load by IO wait — ${formatDurationMs(row.loadByIOWaitMs)}, derived as ${ioSource} on PG${row.pgMajor}`,
      };
    case "loadByLockWait":
      return { kind: "not-applicable", text: LOCK_WAIT_NA };
    case "analyzeLatency":
      return { kind: "not-applicable", text: ANALYZE_LATENCY_GAP };
    default:
      // query / database / recommendations / avgExecTimeMs / totalExecTimeMs /
      // avgRowsReturned / timesCalled are not load cells; `renderCell` renders
      // them and this function's contract is the six load columns only.
      return null;
  }
}

/** The text a plain-value cell renders, for the columns that are neither a link
 *  nor a load bar. Exported so the formatting rules are assertable without a
 *  table — `avgRowsReturned` in particular, which is a mean over an R3.3 delta
 *  (internal/api/dto/queries.go:115-118) and so renders 12.5, not 12. */
export function textCell(row: QueryView, key: QueryColumnKey): string {
  switch (key) {
    case "query":
      return row.query;
    case "database":
      return row.database;
    case "recommendations":
      // `QueryCell` renders the R7.1 pending marker for the empty case, so this
      // branch only ever sees a populated array — and there the count is the
      // answer R3.2's column asks for. Without it the cell renders empty and
      // the column shows nothing for a query that has recommendations.
      return formatNumber(row.recommendations.length);
    case "avgExecTimeMs":
      return formatDurationMs(row.avgExecTimeMs);
    case "totalExecTimeMs":
      return formatDurationMs(row.totalExecTimeMs);
    case "avgRowsReturned":
      return formatNumber(row.avgRowsReturned, 1);
    case "timesCalled":
      return formatNumber(row.timesCalled);
    default:
      return "";
  }
}

export interface QueryTableProps {
  rows: readonly QueryView[];
  /** The visible columns, in order (R10.20's persisted selection). Keys not in
   *  `QUERY_COLUMNS` are dropped, the same way `normalizeSelection` drops them. */
  columns?: readonly string[];
  /** R8.11's batched series, keyed by fingerprint, for the `sparkline` column.
   *
   *  Keyed rather than a parallel array because the envelope's entries are
   *  labelled, not positional: `internal/api/dto/series.go:79` documents
   *  `QuerySeries` as "one requested fingerprint's answer" and `:90` says the
   *  secondary id is "never the key — `fingerprint` is", which is what
   *  `web/src/pages/Queries.tsx:306`'s `seriesByFingerprint` projects. Array
   *  order carries no meaning, so index arithmetic would pair row 3 with the
   *  wrong series whatever the counts were. Optional because only one column
   *  needs it: a caller that has not fetched the series still renders thirteen
   *  correct columns and one `—`. */
  series?: Record<string, QuerySeries>;
  /** The sort the URL says (R10.6's "sorting must be by any column"). The page
   *  owns it; the table renders the header state and reports clicks. */
  sort: QuerySortKey;
  dir: SortDir;
  onSort: (key: QuerySortKey) => void;
  onOpen: (fingerprint: string) => void;
  density: Density;
  /** Height of the scroll viewport in px. A number, not a measurement — the
   *  same reason `VirtualList` gives: the tables live in fixed-height cards, and
   *  a ResizeObserver's first frame is 0, which is the empty-table flash R10.22's
   *  skeleton exists to prevent. */
  viewportHeight?: number;
}

/** The default set, so a caller that has not adopted the column menu still
 *  renders R3.2's thirteen. */
const ALL_KEYS: readonly string[] = QUERY_COLUMNS.map((c) => c.key);

export function QueryTable({
  rows,
  columns,
  series,
  sort,
  dir,
  onSort,
  onOpen,
  density,
  viewportHeight = 640,
}: QueryTableProps) {
  const rowHeight = ROW_HEIGHT[density];

  // Resolve the visible set once: unknown keys dropped, order preserved. This is
  // `normalizeSelection`'s job upstream; re-doing the filter here means a caller
  // that renders the table directly (a test, a fixture screenshot) gets the same
  // behaviour as one that went through the menu.
  const visible = useMemo(() => {
    const wanted = columns ?? ALL_KEYS;
    return QUERY_COLUMNS.filter((c) => wanted.includes(c.key));
  }, [columns]);

  const [scrollTop, setScrollTop] = useState(0);
  const onScroll = useCallback((e: { currentTarget: EventTarget | null }) => {
    const target = e.currentTarget as { scrollTop?: number } | null;
    if (target && typeof target.scrollTop === "number") setScrollTop(target.scrollTop);
  }, []);

  const win = useVirtualWindow({
    count: rows.length,
    rowHeight,
    viewportHeight,
    scrollTop,
  });
  const slice = rows.slice(win.start, win.end);

  return (
    <div
      className="table-wrap"
      onScroll={onScroll}
      style={{ height: viewportHeight, overflowY: "auto" }}
      data-testid="query-table-scroll"
    >
      <table className="data" data-testid="query-table" data-density={density}>
        <caption className="sr-only">
          Top queries, sorted by {sort} {dir === "asc" ? "ascending" : "descending"}
        </caption>
        <thead>
          <tr>
            {visible.map((col) => {
              const active = col.sortable && sort === col.key;
              // `isQuerySortKey` rather than `col.sortable`: the two are true of
              // exactly the same columns, but only the type guard narrows
              // `col.key` to `QuerySortKey` for the `onSort` call. The narrowing
              // has to be bound here — TS drops it inside the `onClick` closure,
              // because a `.map` parameter is a mutable binding and only `const`
              // narrowing survives into a callback body (TS 4.4's aliased-condition
              // rule). `col.sortable ? <button onClick={() => onSort(col.key)}>`
              // fails with TS2345 for exactly this reason.
              const sortKey = isQuerySortKey(col.key) ? col.key : null;
              return (
                <th
                  key={col.key}
                  scope="col"
                  // `sortable` belongs on the th, not the button: base.css styles
                  // the header button through `th.sortable button`. A th without it
                  // renders an unstyled UA button — the bug e6ef285 fixed in
                  // ClusterTable, avoided here by construction.
                  className={
                    col.numeric ? (col.sortable ? "sortable num" : "num") : col.sortable ? "sortable" : undefined
                  }
                  {...(active
                    ? { "aria-sort": dir === "asc" ? ("ascending" as const) : ("descending" as const) }
                    : {})}
                >
                  {sortKey !== null ? (
                    <button type="button" onClick={() => onSort(sortKey)}>
                      <span>{col.label}</span>
                      {active && <span className="caret" aria-hidden>{dir === "asc" ? "▲" : "▼"}</span>}
                    </button>
                  ) : (
                    // No button: a sort control on a column that always renders
                    // the same literal sorts nothing either way.
                    <span>{col.label}</span>
                  )}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {/* The two spacers are what make the scrollbar the right length. They
              are rows, not divs, because the rows they stand in for are rows. */}
          {win.padTop > 0 && (
            <tr className="virtual-spacer" aria-hidden="true">
              <td colSpan={visible.length} style={{ height: win.padTop, padding: 0 }} />
            </tr>
          )}
          {slice.map((row) => (
            <QueryRow
              key={row.fingerprint}
              row={row}
              columns={visible}
              onOpen={onOpen}
              rowHeight={rowHeight}
              series={series?.[row.fingerprint]}
            />
          ))}
          {win.padBottom > 0 && (
            <tr className="virtual-spacer" aria-hidden="true">
              <td colSpan={visible.length} style={{ height: win.padBottom, padding: 0 }} />
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

/** One cell of one row. Exported so the cell-by-cell rules — which column is a
 *  link, which is a bar, which is a plain number, which is the R7.1 pending
 *  marker, which is the sparkline — are assertable without rendering the table
 *  around them. */
export function QueryCell({
  row,
  col,
  onOpen,
  series,
}: {
  row: QueryView;
  col: QueryColumn;
  onOpen: (fingerprint: string) => void;
  /** R8.11's series for this row's fingerprint, keyed by fingerprint upstream.
   *  Only the `sparkline` column reads it; every other cell is a `QueryView`
   *  field. Optional because the table renders correctly without it — the one
   *  column that needs it degrades to `—`. */
  series?: QuerySeries;
}) {
  // The row's click-through target (R10.19): the row is the link, and LoadBars
  // deliberately renders no inner link so a row has exactly one behaviour.
  if (col.key === "query") {
    return (
      <td className="query-cell">
        <a
          className="query-link"
          href={queryDetailHref(row)}
          title={row.truncated ? truncationNote(row) : row.query}
          onClick={(e) => {
            if (e.metaKey || e.ctrlKey || e.button === 1) return;
            e.preventDefault();
            onOpen(row.fingerprint);
          }}
        >
          {row.query}
        </a>
        {row.truncated && (
          <span className="trunc-flag" title={truncationNote(row)} aria-label="truncated">
            ⋯
          </span>
        )}
      </td>
    );
  }

  // R7.1 (the index recommender) is marked post-0.1 and the array is empty for
  // every Phase 3 response (internal/api/dto/queries.go:206-212). An empty one is R10.4's `—` with a
  // marker naming the pending source, not a count of nothing; a populated one is
  // the count, because the number is the answer R3.2's column asks for.
  if (col.key === "recommendations" && row.recommendations.length === 0) {
    return (
      <td className="num">
        <LoadSourcePending source="R7.1" />
      </td>
    );
  }

  // R10.6's per-row sparkline. The values are not on the row — `types.ts:584`
  // documents why `QueryView` cannot inline the series — so they arrive through
  // the `series` prop, and a row whose series has not arrived renders `—` (the
  // house's not-available marker) rather than an empty plot: R10.13 forbids a
  // blank box with no explanation, and a flat line at zero would be a lie about
  // the data. `executionTimeSeconds` is the plotted metric because that is what
  // R3.2's "Avg execution time" column measures; the other three series metrics
  // are R9.5's S25 set and belong to the detail chart, which shows all four.
  if (col.key === "sparkline") {
    return (
      <td>
        {series === undefined || series.points.length === 0 ? (
          <span className="load-na secondary">{NULL_VALUE}</span>
        ) : (
          <Sparkline
            values={series.points.map((p) => p.executionTimeSeconds)}
            width={96}
            height={20}
            framed={false}
            formatValue={formatSec}
            label={`Execution time per minute for ${row.fingerprint}`}
          />
        )}
      </td>
    );
  }

  const cell = loadCellFor(row, col.key);
  if (cell !== null) {
    return (
      <td className={col.numeric ? "num" : undefined}>
        <LoadCell {...cell} />
      </td>
    );
  }

  return <td className={col.numeric ? "num" : undefined}>{textCell(row, col.key)}</td>;
}

function QueryRow({
  row,
  columns,
  onOpen,
  rowHeight,
  series,
}: {
  row: QueryView;
  columns: readonly QueryColumn[];
  onOpen: (fingerprint: string) => void;
  rowHeight: number;
  /** This row's series, already looked up by fingerprint. The row does the
   *  lookup once so eleven cells don't each index the same record. */
  series?: QuerySeries;
}) {
  return (
    <tr style={{ height: rowHeight }}>
      {columns.map((col) => (
        <QueryCell key={col.key} row={row} col={col} onOpen={onOpen} series={series} />
      ))}
    </tr>
  );
}
