/* R10.6 — the Queries table, tested as the two things it is.
 *
 * 1. **The R3.2 column set.** §9's 6b acceptance is a sentence about a list:
 *    "Queries page renders the R3.2 column set exactly (incl. `Load by lock
 *    wait` = `n/a`)". "Exactly" is testable — the header's cells, in order,
 *    against `requirements.md:309` — and it is the assertion that catches the
 *    failure mode this table actually had: a nine-column default that looked
 *    right and omitted the two columns R3.2 withholds a value for.
 * 2. **The `useVirtualWindow` adopter.** VirtualList's header says a `<table>`
 *    can't be windowed the way a `<div>` list can, so the caller has to build
 *    spacer *rows*. That's a behaviour only this file can prove, and it's
 *    proven by counting: N rows in a 640px viewport renders the window plus a
 *    bottom spacer of exactly the right height.
 *
 * The pure functions (`loadCellFor`, `textCell`, `queryDetailHref`) are tested
 * directly rather than through the DOM wherever they can be, because the DOM
 * renders one of them per cell and a wrong denominator in a bar is invisible to
 * a query selector — a meter at 0.6 and a meter at 0.06 are both a div with a
 * width. The fixture is `FIXTURE_QUERIES`, i.e. `testdata/api/queries.json`
 * pinned through `resolveJsonModule`, so every assertion here is against the
 * bytes the Go endpoint emits, not a hand-written object that could drift from it.
 */

import { fireEvent, render, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { FIXTURE_QUERIES } from "../api/fixtures";
import type { QuerySeries, QueryView } from "../api/types";
import {
  QUERY_COLUMNS,
  QueryCell,
  QueryTable,
  isQuerySortKey,
  loadCellFor,
  queryDetailHref,
  textCell,
  truncationNote,
  type QueryColumnKey,
} from "./QueryTable";
import { ANALYZE_LATENCY_GAP, LOCK_WAIT_NA } from "./LoadBars";
import { ROW_HEIGHT } from "./VirtualList";
import { NULL_VALUE, formatDurationMs, formatNumber, formatPercent } from "../lib/format";

const rows = FIXTURE_QUERIES.queries;
const pg16 = rows.find((r) => r.pgMajor === 16 && !r.truncated) as QueryView;
const pg17 = rows.find((r) => r.pgMajor === 17) as QueryView;
const truncatedRow = rows.find((r) => r.truncated) as QueryView;

/** R8.11's series for `pg16`, for the `Load sparkline` column.
 *
 *  Built inline because there is nothing to import: `testdata/api/` has no
 *  query-series fixture (the R8.11 fixture is A4's to generate), and
 *  `QueryDetailView` carries no `points` field — `types.ts:575` documents the
 *  import cycle that keeps the series off the row — so the detail fixture cannot
 *  supply it either. Same three points the Detail page's suite uses, so the two
 *  suites plot the same series from the same numbers. */
const SERIES: QuerySeries = {
  fingerprint: pg16.fingerprint,
  namespace: pg16.namespace,
  cluster: pg16.cluster,
  points: [
    {
      timestamp: "2026-11-20T09:11:00Z",
      executionTimeSeconds: 1.8,
      rows: 12,
      sharedBlocksHit: 40,
      sharedBlocksRead: 7,
      lockTimeSeconds: 0.2,
      ioTimeSeconds: 0.024,
    },
    {
      timestamp: "2026-11-20T09:12:00Z",
      executionTimeSeconds: 42.7,
      rows: 981,
      sharedBlocksHit: 2044,
      sharedBlocksRead: 61,
      lockTimeSeconds: 1.4,
      ioTimeSeconds: 0.2092,
    },
    {
      timestamp: "2026-11-20T09:13:00Z",
      executionTimeSeconds: 7.4,
      rows: 88,
      sharedBlocksHit: 310,
      sharedBlocksRead: 12,
      lockTimeSeconds: 0.1,
      ioTimeSeconds: 0.0412,
    },
  ],
};

/** `rows` with unique fingerprints, for the windowing test — the fixture's seven
 *  all fit in one viewport, so they can't show a window at all. */
function manyRows(n: number): QueryView[] {
  // `as QueryView` on the element: tsconfig has noUncheckedIndexedAccess, so
  // `rows[i]` is `QueryView | undefined`, and spreading a possibly-undefined
  // object makes every property in the result optional — which then fails the
  // `QueryView[]` return type on the one field that is required there.
  return Array.from({ length: n }, (_, i): QueryView => {
    const src = rows[i % rows.length] as QueryView;
    return { ...src, fingerprint: `${src.fingerprint.slice(0, 60)}${i.toString(16).padStart(4, "0")}` };
  });
}

describe("the R3.2 column set", () => {
  it("is R3.2's thirteen columns in R3.2's order with R3.2's names, plus R10.6's sparkline last", () => {
    // `requirements.md:309` for the thirteen — the §9 6b "renders the R3.2 column
    // set exactly" assertion, the list and not a rendering of it — and
    // `requirements.md:419` for the fourteenth. The order is the assertion: the
    // thirteen must keep R3.2's order, so R10.6's column is appended after them
    // rather than inserted among them, which is what lets this one test assert
    // both requirements' wordings at once.
    expect(QUERY_COLUMNS.map((c) => c.key)).toEqual([
      "query",
      "database",
      "recommendations",
      "loadByTotalTime",
      "pctLoadByQueries",
      "avgExecTimeMs",
      "totalExecTimeMs",
      "avgRowsReturned",
      "timesCalled",
      "loadByCPUMs",
      "loadByIOWaitMs",
      "loadByLockWait",
      "analyzeLatency",
      "sparkline",
    ]);
    expect(QUERY_COLUMNS.map((c) => c.label)).toEqual([
      "Query",
      "Database",
      "Recommendations",
      "Load by total time",
      "% load by Queries",
      "Avg execution time (ms)",
      "Total execution time (ms)",
      "Avg rows returned",
      "Times called",
      "Load by CPU",
      "Load by IO wait",
      "Load by lock wait",
      "Analyze latency",
      "Load sparkline",
    ]);
  });

  it("has unique keys", () => {
    const keys = QUERY_COLUMNS.map((c) => c.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("makes exactly three columns non-sortable, for two different reasons", () => {
    // R10.6's "sorting must be by any column" means any column with something to
    // sort on. A sort control on a cell that always renders the same literal is
    // a control that cannot do anything — that's `loadByLockWait` and
    // `analyzeLatency`. The sparkline is the third for the other reason: it has
    // data, but no scalar to sort on, and R8.10's `?sort=` names the DTO's
    // columns, not a chart. Both reasons end in the same flag, and this asserts
    // that neither of them produced a fourth.
    expect(QUERY_COLUMNS.filter((c) => !c.sortable).map((c) => c.key)).toEqual([
      "loadByLockWait",
      "analyzeLatency",
      "sparkline",
    ]);
  });

  it("marks every numeric column numeric, and only the four non-figure ones not", () => {
    // Right-align + tabular figures is a per-column fact the table must not
    // re-derive from the key's shape. The sparkline is not numeric even though
    // it is drawn from a number: the cell has no figure to align, and marking it
    // numeric would right-align an empty `td` around a fixed-width svg.
    expect(QUERY_COLUMNS.filter((c) => !c.numeric).map((c) => c.key)).toEqual([
      "query",
      "database",
      "recommendations",
      "sparkline",
    ]);
  });
});

describe("isQuerySortKey", () => {
  it("accepts the eleven sortable keys", () => {
    for (const col of QUERY_COLUMNS.filter((c) => c.sortable)) {
      expect(isQuerySortKey(col.key)).toBe(true);
    }
  });

  it("rejects the three non-sortable columns", () => {
    expect(isQuerySortKey("loadByLockWait")).toBe(false);
    expect(isQuerySortKey("analyzeLatency")).toBe(false);
    // R10.6's sparkline is in `QueryColumnKey` (the menu must be able to name it)
    // and not in `QuerySortKey` (a chart is not a sort value), so a URL carrying
    // `?sort=sparkline` is rejected here instead of reaching R8.10's `sort=`.
    expect(isQuerySortKey("sparkline")).toBe(false);
  });

  it("rejects a fleet sort key, which is a different page's vocabulary", () => {
    // urlState.SortKey is the fleet's set; a Queries URL carrying `tps` is a
    // bug, and the guard is where that gets caught.
    expect(isQuerySortKey("tps")).toBe(false);
    expect(isQuerySortKey("")).toBe(false);
    expect(isQuerySortKey("Query")).toBe(false); // a label, not a key
  });
});

describe("loadCellFor — the denominators", () => {
  // A meter needs a known whole, and the answer differs per column. Getting it
  // from the DTO's field docs rather than from the page max is the whole design
  // question here, so each one is asserted as a number.
  it("Load by total time: a percentage, so the whole is 100 — not the page max", () => {
    const cell = loadCellFor(pg16, "loadByTotalTime");
    expect(cell).toMatchObject({
      kind: "bar",
      fraction: pg16.loadByTotalTime / 100,
      text: formatPercent(pg16.loadByTotalTime),
    });
    // 60% of total load renders 60% full. Against a page max it would render
    // full and the ranking the column exists to show would disappear.
    expect((cell as { fraction: number }).fraction).toBeCloseTo(0.6, 10);
  });

  it("% load by Queries: a percentage too, same numerator restated", () => {
    const cell = loadCellFor(pg16, "pctLoadByQueries");
    expect(cell).toMatchObject({
      kind: "bar",
      fraction: pg16.pctLoadByQueries / 100,
      text: formatPercent(pg16.pctLoadByQueries),
    });
  });

  it("Load by CPU: a part of totalExecTimeMs, so that is the whole", () => {
    // R3.2 derives it as `total_exec_time − Load by IO wait`; the value is a
    // fraction of the row's own total, not of a column max.
    const cell = loadCellFor(pg16, "loadByCPUMs");
    expect(cell).toMatchObject({
      kind: "bar",
      fraction: pg16.loadByCPUMs / pg16.totalExecTimeMs,
      text: formatDurationMs(pg16.loadByCPUMs),
    });
  });

  it("Load by IO wait: same whole, and the tooltip names which PG's derivation", () => {
    // R10.28's named "PG16 vs PG17 column differences" state: the two
    // derivations are not comparable across the boundary, so the cell says which
    // one this row's number came from. pgMajor is the only field that knows.
    const on16 = loadCellFor(pg16, "loadByIOWaitMs");
    expect(on16).toMatchObject({
      kind: "bar",
      fraction: pg16.loadByIOWaitMs / pg16.totalExecTimeMs,
    });
    expect((on16 as { title: string }).title).toContain("blk_read_time + blk_write_time");
    expect((on16 as { title: string }).title).toContain("PG16");

    const on17 = loadCellFor(pg17, "loadByIOWaitMs");
    expect((on17 as { title: string }).title).toContain(
      "shared_blk_read_time + shared_blk_write_time",
    );
    expect((on17 as { title: string }).title).toContain("PG17");
    // The PG16 spelling must not leak into a PG17 row's tooltip. `\b` is doing
    // the work here, not a character class: `blk_read_time` *is* a substring of
    // `shared_blk_read_time`, and in that word the `b` is preceded by `_` — also
    // a word character — so there is no boundary there and the pattern misses.
    expect((on17 as { title: string }).title).not.toMatch(/\bblk_read_time\b/);
  });

  it("the two valueless columns render their literals, not bars and not em-dashes", () => {
    expect(loadCellFor(pg16, "loadByLockWait")).toEqual({ kind: "not-applicable", text: LOCK_WAIT_NA });
    expect(loadCellFor(pg16, "analyzeLatency")).toEqual({
      kind: "not-applicable",
      text: ANALYZE_LATENCY_GAP,
    });
  });

  it("every other column is not a load cell at all", () => {
    // The contract is the six load columns; the other seven return `null` so
    // QueryCell falls through to textCell. `null` rather than a `{kind:"none"}`
    // member of the union — see `loadCellFor`'s doc for why a fourth member
    // would have put an unmarked `—` on screen.
    for (const key of ["query", "database", "recommendations", "avgExecTimeMs", "totalExecTimeMs", "avgRowsReturned", "timesCalled"] as QueryColumnKey[]) {
      expect(loadCellFor(pg16, key)).toBeNull();
    }
  });
});

describe("textCell", () => {
  it("Avg rows returned keeps the decimal — it is a mean, not a count", () => {
    // internal/api/dto/queries.go:115-118: a float64 mean over an R3.3 delta, so 12.5 is a real
    // answer and rendering it as `12` is a wrong number, not a rounding choice.
    expect(textCell(pg16, "avgRowsReturned")).toBe(formatNumber(pg16.avgRowsReturned, 1));
    expect(textCell(pg16, "avgRowsReturned")).toBe("1.0");
  });

  it("Times called is an integer with digit grouping", () => {
    expect(textCell(pg16, "timesCalled")).toBe("3,007");
  });

  it("the two duration columns go through formatDurationMs", () => {
    expect(textCell(pg16, "avgExecTimeMs")).toBe(formatDurationMs(pg16.avgExecTimeMs));
    expect(textCell(pg16, "avgExecTimeMs")).toBe("43 ms");
    expect(textCell(pg16, "totalExecTimeMs")).toBe(formatDurationMs(pg16.totalExecTimeMs));
    expect(textCell(pg16, "totalExecTimeMs")).toBe("2m 08s");
  });

  it("Query and Database render the raw strings", () => {
    expect(textCell(pg16, "query")).toBe(pg16.query);
    expect(textCell(pg16, "database")).toBe(pg16.database);
  });

  it("a load column has no text — LoadBars owns it", () => {
    expect(textCell(pg16, "loadByCPUMs")).toBe("");
  });
});

describe("queryDetailHref / truncationNote", () => {
  it("builds the query-detail route from the row's own namespace, cluster and fingerprint", () => {
    // routes.ts's buildPath is the one path table that also parses these, so a
    // row can never link to a URL the back button cannot come back through.
    expect(queryDetailHref(pg16)).toBe(
      `/c/${pg16.namespace}/${pg16.cluster}/queries/${pg16.fingerprint}`,
    );
  });

  it("the R3.6 note names the cap and the fingerprint it applies to", () => {
    const note = truncationNote(truncatedRow);
    expect(note).toContain("1 024-byte");
    expect(note).toContain(truncatedRow.fingerprint);
  });
});

describe("QueryCell", () => {
  const onOpen = vi.fn();
  const col = (key: QueryColumnKey) => QUERY_COLUMNS.find((c) => c.key === key)!;

  it("the query cell is the row's only link, and plain clicks go through onOpen", () => {
    const { container } = render(<QueryCell row={pg16} col={col("query")} onOpen={onOpen} />);
    const link = within(container).getByRole("link");
    expect(link.getAttribute("href")).toBe(queryDetailHref(pg16));

    onOpen.mockClear();
    fireEvent.click(link);
    expect(onOpen).toHaveBeenCalledWith(pg16.fingerprint);
  });

  it("a modifier-click is left to the browser (open in new tab)", () => {
    const { container } = render(<QueryCell row={pg16} col={col("query")} onOpen={onOpen} />);
    const link = within(container).getByRole("link");
    onOpen.mockClear();
    // fireEvent returns false when the handler called preventDefault.
    expect(fireEvent.click(link, { metaKey: true })).toBe(true);
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("a truncated row carries the ⋯ flag with the R3.6 note as its title", () => {
    const { container } = render(<QueryCell row={truncatedRow} col={col("query")} onOpen={onOpen} />);
    const flag = container.querySelector(".trunc-flag");
    expect(flag).not.toBeNull();
    expect(flag?.getAttribute("title")).toBe(truncationNote(truncatedRow));
    // The link text is the truncated template itself; the flag is what says so.
    expect(within(container).getByRole("link").textContent).toBe(truncatedRow.query);
  });

  it("empty recommendations render the R7.1 pending marker, not a zero", () => {
    // R7.1 is marked post-0.1 and the array is empty for every Phase 3 response
    // (internal/api/dto/queries.go:206-212). `0` would claim the recommender ran and found nothing.
    const { container } = render(
      <QueryCell row={pg16} col={col("recommendations")} onOpen={onOpen} />,
    );
    expect(container.querySelector('[data-testid="load-pending"]')?.textContent).toContain("R7.1");
  });

  it("a populated recommendations cell renders the count", () => {
    const withRec = {
      ...pg16,
      recommendations: [
        {
          finding: "missing-index",
          evidence: ["Seq Scan on users"],
          action: "CREATE INDEX",
          confidence: "high",
        },
      ],
    };
    const { container } = render(<QueryCell row={withRec} col={col("recommendations")} onOpen={onOpen} />);
    expect(container.textContent).toBe("1");
  });

  it("a load cell renders no inner link — the row is the hit target (R10.19)", () => {
    const { container } = render(
      <QueryCell row={pg16} col={col("loadByTotalTime")} onOpen={onOpen} />,
    );
    expect(container.querySelector("a")).toBeNull();
    expect(container.querySelector(".load-fill")).not.toBeNull();
  });

  it("the sparkline cell plots the row's series and carries no figure", () => {
    // R10.6's fourteenth column. The assertion worth making is the one a
    // screenshot can't settle: the cell's numbers come from the *series* prop,
    // not the row, so a cell wired to the wrong source still renders a line. The
    // accessible name names the fingerprint, which is what ties this svg to this
    // row, and the cell itself stays figure-free — the number is in the tooltip.
    const { container } = render(
      <QueryCell row={pg16} col={col("sparkline")} onOpen={onOpen} series={SERIES} />,
    );
    const svg = container.querySelector("td .sparkline-cell svg.sparkline");
    expect(svg?.getAttribute("role")).toBe("img");
    expect(svg?.getAttribute("aria-label")).toBe(`Execution time per minute for ${pg16.fingerprint}`);
    // The framed variant's axes and y labels would crowd a 20px cell, so the
    // cell variant is the bare line — and if it were framed, the cell would have
    // text in it.
    expect(container.querySelector(".spark-grid-group")).toBeNull();
    expect(container.querySelector("td")?.textContent).toBe("");
  });

  it("the sparkline cell renders — when the row has no series, and for an empty one", () => {
    // R8.11's batch endpoint answers with one entry per query that has data, so
    // a row missing from the map is normal, not an error. R10.13 forbids a blank
    // box with no explanation and a flat line at zero would be a lie about the
    // data, so both the missing and the empty case render the house's
    // not-available marker.
    const missing = render(<QueryCell row={pg16} col={col("sparkline")} onOpen={onOpen} />);
    expect(missing.container.querySelector("svg")).toBeNull();
    expect(missing.container.querySelector("td .load-na")?.textContent).toBe(NULL_VALUE);

    const empty = render(
      <QueryCell
        row={pg16}
        col={col("sparkline")}
        onOpen={onOpen}
        series={{ ...SERIES, points: [] }}
      />,
    );
    expect(empty.container.querySelector("svg")).toBeNull();
    expect(empty.container.querySelector("td .load-na")?.textContent).toBe(NULL_VALUE);
  });
});

describe("QueryTable rendering", () => {
  const base = {
    sort: "loadByTotalTime" as const,
    dir: "desc" as const,
    onSort: vi.fn(),
    onOpen: vi.fn(),
    density: "compact" as const,
  };

  it("renders one th per column and one td per column per row", () => {
    const { container } = render(<QueryTable {...base} rows={[pg16]} />);
    const table = container.querySelector("table.data") as HTMLElement;
    const head = within(table).getAllByRole("row")[0]!;
    // `QUERY_COLUMNS.length`, not a literal: the literal is the column-set test's
    // job (it asserts the exact thirteen-plus-one list). Here the assertion is
    // one-th-per-column and one-td-per-column-per-row, which is a statement about
    // the count, so it reads off the same array the table renders from.
    expect(within(head).getAllByRole("columnheader")).toHaveLength(QUERY_COLUMNS.length);

    const body = container.querySelector("tbody")!;
    const dataRow = body.querySelector("tr:not(.virtual-spacer)") as HTMLElement;
    expect(within(dataRow).getAllByRole("cell")).toHaveLength(QUERY_COLUMNS.length);
  });

  it("renders the three non-sortable headers as text and the eleven sortable ones as buttons", () => {
    // The e6ef285 class of bug (a header styled by a class rule that doesn't
    // exist) is avoided by construction here, and this asserts the other half:
    // a non-sortable column has no button at all — including the sparkline, whose
    // header is a plain span for the reason in the column-set test.
    const { container } = render(<QueryTable {...base} rows={[pg16]} />);
    const head = within(container.querySelector("thead")!).getByRole("row");
    expect(within(head).getAllByRole("button")).toHaveLength(11);
    expect(within(head).getByText("Load by lock wait").tagName).toBe("SPAN");
    expect(within(head).getByText("Analyze latency").tagName).toBe("SPAN");
    expect(within(head).getByText("Load sparkline").tagName).toBe("SPAN");
  });

  it("puts aria-sort on the active header only, and it matches dir", () => {
    const { container, rerender } = render(<QueryTable {...base} rows={[pg16]} />);
    const head = within(container.querySelector("thead")!).getByRole("row");
    const active = within(head).getByRole("button", { name: /Load by total time/ }).closest("th")!;
    expect(active.getAttribute("aria-sort")).toBe("descending");
    expect(head.querySelectorAll("[aria-sort]")).toHaveLength(1);

    rerender(<QueryTable {...base} rows={[pg16]} dir="asc" />);
    const head2 = within(container.querySelector("thead")!).getByRole("row");
    expect(
      within(head2).getByRole("button", { name: /Load by total time/ }).closest("th")!.getAttribute("aria-sort"),
    ).toBe("ascending");
  });

  it("reports a header click as the column key", () => {
    const onSort = vi.fn();
    const { container } = render(<QueryTable {...base} rows={[pg16]} onSort={onSort} />);
    const head = within(container.querySelector("thead")!).getByRole("row");
    fireEvent.click(within(head).getByRole("button", { name: "Times called" }));
    expect(onSort).toHaveBeenCalledWith("timesCalled");
  });

  it("windows the rows and pads the rest with spacer rows", () => {
    // The table-shaped half of R10.18: the spacers are <tr>s with colSpan, not
    // <div>s, because the rows they stand in for are rows. Numbers come from
    // windowFor(200, 24, 640, 0, 6): start 0, end 39, padBottom (200-39)*24.
    const many = manyRows(200);
    const { container } = render(<QueryTable {...base} rows={many} />);
    const body = container.querySelector("tbody")!;
    const dataRows = body.querySelectorAll("tr:not(.virtual-spacer)");
    expect(dataRows).toHaveLength(39);
    const spacer = body.querySelector("tr.virtual-spacer") as HTMLElement;
    expect(spacer.querySelector("td")?.getAttribute("colspan")).toBe(String(QUERY_COLUMNS.length));
    expect(spacer.querySelector("td")?.style.height).toBe(`${(200 - 39) * ROW_HEIGHT.compact}px`);
    // The rendered rows are the first 39, in order — the window, not a sample.
    expect(dataRows[0]!.textContent).toContain(many[0]!.query);
    expect(dataRows[38]!.textContent).toContain(many[38]!.query);
  });

  it("renders no spacer when everything fits", () => {
    const { container } = render(<QueryTable {...base} rows={[pg16]} />);
    expect(container.querySelector(".virtual-spacer")).toBeNull();
  });

  it("honours a column subset and drops keys that are not columns", () => {
    // R10.20's persisted selection arrives as string[] and can contain a key
    // from an older column set; rendering it would produce an empty column.
    const { container } = render(
      <QueryTable {...base} rows={[pg16]} columns={["database", "not-a-column", "timesCalled"]} />,
    );
    const head = within(container.querySelector("thead")!).getByRole("row");
    expect(within(head).getAllByRole("columnheader").map((th) => th.textContent)).toEqual([
      "Database",
      "Times called",
    ]);
    const body = container.querySelector("tbody")!;
    expect(within(body.querySelector("tr:not(.virtual-spacer)") as HTMLElement).getAllByRole("cell").map((td) => td.textContent)).toEqual([
      pg16.database,
      "3,007",
    ]);
  });

  it("carries the density onto the table so the row heights are the ones asked for", () => {
    const { container, rerender } = render(<QueryTable {...base} rows={[pg16]} />);
    expect(container.querySelector("table.data")?.getAttribute("data-density")).toBe("compact");
    expect(container.querySelector("tbody tr:not(.virtual-spacer)")?.getAttribute("style")).toContain(
      `height: ${ROW_HEIGHT.compact}px`,
    );
    rerender(<QueryTable {...base} rows={[pg16]} density="comfortable" />);
    expect(container.querySelector("tbody tr:not(.virtual-spacer)")?.getAttribute("style")).toContain(
      `height: ${ROW_HEIGHT.comfortable}px`,
    );
  });
});
