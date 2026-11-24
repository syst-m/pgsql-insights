/* R10.5(a)'s Query load chart — the bucket merge, the closed band geometry, and
 * the one multiply that keeps seconds from being read as milliseconds.
 *
 * The pure functions get the majority of the tests because they are the parts
 * that can be wrong without changing the DOM in a way a screenshot catches:
 * `stackSeries` summing two fingerprints by array index instead of by
 * timestamp, `areaPath` closing a polygon whose bottom edge is a guess,
 * `stackDomain` floating an area off its baseline. The render tests then check
 * which nodes exist for each state of the data, because the requirement's
 * content is a claim about the contract (two measures, one of them absent) and
 * that claim is what the legend and the tooltip have to keep true.
 */

import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { QuerySeries, QuerySeriesPoint } from "../api/types";
import { formatDurationMs, formatTimestamp } from "../lib/format";
import {
  areaPath,
  formatSec,
  QueryLoadArea,
  stackDomain,
  stackSeries,
  ticksFor,
} from "./QueryLoadArea";
import type { QueryLoadPoint } from "./QueryLoadArea";
import { axisTimeLabel } from "./Sparkline";

/** Three minutes of one fingerprint, every field a different number so a band
 *  reading the wrong field shows up in the assertion. */
const SERIES: QuerySeries = {
  fingerprint: "sha256:aaaa",
  namespace: "postgres",
  cluster: "demo",
  points: [
    {
      timestamp: "2026-11-20T09:11:00Z",
      executionTimeSeconds: 1.8,
      rows: 12,
      sharedBlocksHit: 40,
      sharedBlocksRead: 7,
      lockTimeSeconds: 0.2,
    },
    {
      timestamp: "2026-11-20T09:12:00Z",
      executionTimeSeconds: 42.7,
      rows: 981,
      sharedBlocksHit: 2044,
      sharedBlocksRead: 61,
      lockTimeSeconds: 1.4,
    },
    {
      timestamp: "2026-11-20T09:13:00Z",
      executionTimeSeconds: 75,
      rows: 4,
      sharedBlocksHit: 8,
      sharedBlocksRead: 0,
      lockTimeSeconds: 0,
    },
  ],
};

/** The same three buckets as the component's own shape. */
const POINTS: QueryLoadPoint[] = [
  { t: "2026-11-20T09:11:00Z", execSec: 1.8, ioSec: null },
  { t: "2026-11-20T09:12:00Z", execSec: 42.7, ioSec: null },
  { t: "2026-11-20T09:13:00Z", execSec: 75, ioSec: null },
];

/** A second fingerprint sharing two of the three timestamps — the shape R8.11's
 *  batch returns, where two series' `points` arrays line up only partially. */
const SERIES_B: QuerySeries = {
  fingerprint: "sha256:bbbb",
  namespace: "postgres",
  cluster: "demo",
  points: [
    {
      timestamp: "2026-11-20T09:12:00Z",
      executionTimeSeconds: 0.5,
      rows: 1,
      sharedBlocksHit: 3,
      sharedBlocksRead: 0,
      lockTimeSeconds: 0,
    },
    {
      timestamp: "2026-11-20T09:14:00Z",
      executionTimeSeconds: 2,
      rows: 1,
      sharedBlocksHit: 3,
      sharedBlocksRead: 0,
      lockTimeSeconds: 0,
    },
  ],
};

describe("formatSec", () => {
  // The units trap: the series are seconds, `formatDurationMs` takes ms, and a
  // 42.7-second minute rendered as "43 ms" is a 1000× error that still looks
  // like a plausible duration. This assertion is the whole reason the local
  // formatter exists instead of a bare call.
  it("renders seconds through the ms formatter, not as milliseconds", () => {
    expect(formatSec(42.7)).toBe("42.70 s");
    expect(formatSec(42.7)).not.toMatch(/ms$/);
    expect(formatSec(0.4)).toBe("400 ms");
    expect(formatSec(90)).toBe("1m 30s");
  });

  it("keeps the formatter's null rendering for a non-finite value", () => {
    expect(formatSec(Number.NaN)).toBe(formatDurationMs(null));
  });
});

describe("stackSeries", () => {
  it("sums every fingerprint that shares a timestamp into one bucket", () => {
    const [b12] = stackSeries([SERIES, SERIES_B]).filter((p) => p.t === "2026-11-20T09:12:00Z");
    // 42.7 + 0.5. Zipping by index instead of merging on the timestamp would
    // add 09:12's 42.7 to 09:14's 2 and give 44.7 on a bucket that never had it.
    expect(b12?.execSec).toBeCloseTo(43.2, 10);
  });

  it("keeps distinct timestamps as distinct buckets and sorts them", () => {
    const unsorted = stackSeries([SERIES_B, SERIES]);
    expect(unsorted.map((p) => p.t)).toEqual([
      "2026-11-20T09:11:00Z",
      "2026-11-20T09:12:00Z",
      "2026-11-20T09:13:00Z",
      "2026-11-20T09:14:00Z",
    ]);
  });

  it("yields `null` IO time on every bucket it returns", () => {
    // `null` is the contract read back — `QuerySeriesPoint` has no IO-time
    // field — and it is produced here so a caller cannot pass a plausible `0`.
    const points = stackSeries([SERIES, SERIES_B]);
    expect(points).toHaveLength(4);
    expect(points.every((p) => p.ioSec === null)).toBe(true);
  });

  it("drops a non-finite execution time instead of poisoning the bucket", () => {
    const nan: QuerySeriesPoint = {
      timestamp: "2026-11-20T09:11:00Z",
      executionTimeSeconds: Number.NaN,
      rows: 0,
      sharedBlocksHit: 0,
      sharedBlocksRead: 0,
      lockTimeSeconds: 0,
    };
    // The NaN bucket still exists (the good series has a value for it) and its
    // total is the good value alone, not NaN.
    const points = stackSeries([SERIES, { ...SERIES, points: [nan] }]);
    expect(points[0]?.execSec).toBeCloseTo(1.8, 10);
    // A series whose only point is non-finite contributes no bucket at all:
    // the point is skipped, and a bucket with no surviving value is not a
    // bucket — emitting one would render a minute that reads as zero.
    expect(stackSeries([{ ...SERIES, points: [nan] }])).toHaveLength(0);
  });

  it("returns [] for an empty batch", () => {
    expect(stackSeries([])).toEqual([]);
  });
});

describe("stackDomain", () => {
  // An area's height *is* its value, so a floor above zero truncates every band
  // at once. `Sparkline` may use the data's min because a line has no baseline.
  it("floors at zero whatever the data's own min is", () => {
    expect(stackDomain(42.7)).toEqual([0, 42.7]);
  });

  it("widens a zero max so the axis still has two labels", () => {
    expect(stackDomain(0)).toEqual([0, 1]);
    expect(stackDomain(-5)).toEqual([0, 1]);
  });
});

describe("areaPath", () => {
  // The exact string, because a closed polygon is exactly a string: forward
  // along the top, back along the bottom, `Z`. Six vertices for a two-bucket
  // band, and one missing vertex is a shape that looks like the chart until it
  // is compared to the data.
  it("closes the polygon: forward along the top, back along the bottom", () => {
    expect(areaPath([10, 20], [0, 0], 100, 100, [0, 20])).toBe(
      "M0,50 L100,0 L100,100 L0,100 Z",
    );
  });

  // marks-and-anatomy's 2px surface gap between touching fills, split 1px per
  // band at the edge they share. A single-band chart has no shared edge, so the
  // defaults inset nothing and the band sits on the baseline.
  it("insets a shared edge and leaves an unshared one alone", () => {
    expect(areaPath([10, 20], [0, 0], 100, 100, [0, 20], 1, 1)).toBe(
      "M0,51 L100,1 L100,99 L0,99 Z",
    );
    expect(areaPath([10, 20], [0, 0], 100, 100, [0, 20], 0, 1)).toBe(
      "M0,50 L100,0 L100,99 L0,99 Z",
    );
    expect(areaPath([10, 20], [0, 0], 100, 100, [0, 20], 1, 0)).toBe(
      "M0,51 L100,1 L100,100 L0,100 Z",
    );
  });

  it("returns \"\" rather than a path with a guessed edge", () => {
    expect(areaPath([], [], 100, 100, [0, 1])).toBe("");
    expect(areaPath([1, 2], [0, 0, 0], 100, 100, [0, 2])).toBe("");
    expect(areaPath([1, Number.NaN], [0, 0], 100, 100, [0, 2])).toBe("");
    expect(areaPath([1], [0], 0, 100, [0, 2])).toBe("");
    expect(areaPath([1], [0], 100, 0, [0, 2])).toBe("");
  });
});

describe("ticksFor", () => {
  it("always includes the first and the last index", () => {
    expect(ticksFor(3, 6)).toEqual([0, 1, 2]);
    expect(ticksFor(100, 6)).toEqual([0, 20, 40, 59, 79, 99]);
  });

  it("caps at max and returns nothing for an empty count", () => {
    expect(ticksFor(4, 4)).toEqual([0, 1, 2, 3]);
    expect(ticksFor(0, 6)).toEqual([]);
    expect(ticksFor(5, 0)).toEqual([]);
  });
});

describe("QueryLoadArea", () => {
  it("renders the card the requirement is named on", () => {
    const { container } = render(<QueryLoadArea points={POINTS} />);
    expect(container.querySelector(".card-title")?.textContent).toBe("Query load");
    expect(container.querySelector(".card-meta")?.textContent).toBe("R10.5");
    expect(container.querySelector("svg[aria-label='Query load']")).not.toBeNull();
  });

  // R10.13: `points: []` renders the sentence, not an empty frame. One sentence
  // because `stackSeries([])` and `stackSeries([{…, points: []}])` are
  // indistinguishable from the input this component is given.
  it("renders one sentence and no svg when there are no buckets", () => {
    const { container } = render(<QueryLoadArea points={[]} />);
    expect(container.querySelector("svg")).toBeNull();
    expect(container.querySelectorAll(".chart-empty")).toHaveLength(1);
    expect(container.querySelector(".chart-empty")?.textContent).toMatch(/no query-load buckets/i);
    expect(container.querySelector(".table-wrap")).toBeNull();
    // The legend is the requirement's inventory, so it renders even with no plot.
    expect(container.querySelector(".qla-legend")?.textContent).toContain("IO time · n/a");
  });

  it("names both measures and marks IO time n/a while it has no source", () => {
    const { container } = render(<QueryLoadArea points={POINTS} />);
    const legend = container.querySelector(".qla-legend");
    expect(legend?.textContent).toContain("Total execution time");
    expect(legend?.textContent).toContain("IO time · n/a");
    // R10.23: the absent measure is still visible as absent, and the key keeps
    // its `--series-2` shape so the day it gains data nothing repaints.
    expect(container.querySelector(".qla-key--io.qla-key--absent")).not.toBeNull();
    // One band drawn, and the legend's two entries are not buttons: with one
    // drawable series, click-to-isolate is degenerate and is stated, not disabled.
    expect(container.querySelectorAll(".qla-band")).toHaveLength(1);
    expect(container.querySelectorAll(".qla-legend button")).toHaveLength(0);
  });

  it("draws the IO band and drops the n/a once every bucket has a value", () => {
    const withIo: QueryLoadPoint[] = [
      { t: "2026-11-20T09:11:00Z", execSec: 40, ioSec: 20 },
      { t: "2026-11-20T09:12:00Z", execSec: 20, ioSec: 10 },
    ];
    const { container } = render(<QueryLoadArea points={withIo} />);
    expect(container.querySelectorAll(".qla-band")).toHaveLength(2);
    expect(container.querySelector(".qla-key--io.qla-key--absent")).toBeNull();
    expect(container.querySelector(".qla-legend")?.textContent).not.toContain("n/a");
    // Selective direct labels: one per drawn band at the right edge, never a
    // value at every bucket.
    expect(container.querySelectorAll(".qla-label")).toHaveLength(2);
  });

  it("omits a direct label for a band too thin to hold it", () => {
    // The exec band fills the plot; the io band is 1/1000th of the max, so its
    // band is a sub-pixel sliver and the label would be taller than the mark.
    const thin: QueryLoadPoint[] = [
      { t: "2026-11-20T09:11:00Z", execSec: 1000, ioSec: 1 },
      { t: "2026-11-20T09:12:00Z", execSec: 1000, ioSec: 1 },
    ];
    const { container } = render(<QueryLoadArea points={thin} />);
    expect(container.querySelectorAll(".qla-band")).toHaveLength(2);
    expect(container.querySelectorAll(".qla-label")).toHaveLength(1);
  });

  it("shows one tooltip listing every drawn series at the hovered bucket", () => {
    const withIo: QueryLoadPoint[] = [
      { t: "2026-11-20T09:11:00Z", execSec: 40, ioSec: 20 },
      { t: "2026-11-20T09:12:00Z", execSec: 20, ioSec: 10 },
    ];
    const { container } = render(<QueryLoadArea points={withIo} />);
    expect(container.querySelector(".viz-tooltip")).toBeNull();

    const hits = container.querySelectorAll(".spark-hit");
    expect(hits).toHaveLength(2);
    fireEvent.mouseEnter(hits[0] as Element);

    const tooltip = container.querySelector(".viz-tooltip");
    expect(tooltip).not.toBeNull();
    // One tooltip for the crosshair, not one per band, and it carries a row for
    // each drawn series with the value, not the colour, carrying the number.
    const rows = tooltip?.querySelectorAll(".tt-row") ?? [];
    expect(rows).toHaveLength(2);
    expect(rows[0]?.querySelector(".tt-value")?.textContent).toBe("40.00 s");
    expect(rows[1]?.querySelector(".tt-value")?.textContent).toBe("20.00 s");
    expect(tooltip?.querySelector(".tt-label")?.textContent).toBe(
      formatTimestamp("2026-11-20T09:11:00Z"),
    );
    expect(container.querySelector(".spark-crosshair")).not.toBeNull();
  });

  it("labels the window in the API's vocabulary and renders none when omitted", () => {
    // The prop is optional and this test pins what that means: no window known,
    // no window label. It used to be optional because `urlState.ts:53`'s
    // `TimeRange` and `SeriesRange` disagreed (`24h` versus `1d`) with no ruling
    // yet — A3 has since ruled R10.15 and the rename shipped, so the two lists
    // are now the same minus `custom`, and the optionality is just the label.
    const withRange = render(<QueryLoadArea points={POINTS} range="7d" />);
    expect(withRange.container.querySelector(".qla-scope")?.textContent).toContain("last 7d");

    const without = render(<QueryLoadArea points={POINTS} />);
    expect(without.container.querySelector(".qla-scope")?.textContent).not.toMatch(/last \d/);
  });

  it("names the top-N scope of the sum it plots", () => {
    // R10.5(a) is a cluster chart, R8.11's route is per-fingerprint and R9.5
    // caps the batch at 50, so the caption says top-N rather than claiming the
    // cluster total that lives on the aggregate route.
    const { container } = render(<QueryLoadArea points={POINTS} />);
    expect(container.querySelector(".qla-scope")?.textContent).toMatch(/top-N/);
  });

  it("renders a table twin on the axis's own buckets", () => {
    // R10.23's non-colour encoding. The axis and the twin both ask `ticksFor`
    // for their rows, so they are one rule over one array; the caps differ (the
    // axis is limited by what fits side by side, the twin by `MAX_TWIN_ROWS`),
    // so what is provable is weaker and better than equality: every cell is a
    // bucket the series actually has, and the two ends are shared.
    const many = Array.from({ length: 20 }, (_, i) => ({
      t: `2026-11-20T09:${String(i).padStart(2, "0")}:00Z`,
      execSec: i + 1,
      ioSec: null,
    }));
    const { container } = render(<QueryLoadArea points={many} />);

    const rows = container.querySelectorAll(".qla-table tbody tr");
    expect(rows).toHaveLength(ticksFor(20, 12).length);

    const formatted = many.map((p) => formatTimestamp(p.t));
    const twin = Array.from(rows).map((r) => r.querySelector("td")?.textContent ?? null);
    // No invented rows: every cell is one of the 20 buckets.
    for (const cell of twin) expect(formatted).toContain(cell);
    // The axis's ends are the same two buckets as the twin's ends — the shared
    // part of the two selections — but in the axis's own short form. Asserting
    // both strings for one bucket is what pins them to a single bucket list.
    const axis = Array.from(container.querySelectorAll(".spark-x-labels text")).map(
      (t) => t.textContent,
    );
    expect(axis[0]).toBe(axisTimeLabel(many[0]?.t ?? null));
    expect(axis[axis.length - 1]).toBe(axisTimeLabel(many[many.length - 1]?.t ?? null));
    // The axis form is a clock and nothing else: `HH:mm`, no date, no zone.
    // ~30 px per label is what stops six of them smearing into one run, which is
    // the collision the full R10.21 string produces at this width.
    expect(axis[0]).toMatch(/^\d{2}:\d{2}$/);
    // The IO column renders the null, not a zero: R10.23's state conveyed by a
    // value that means "no source" rather than one that means "none".
    expect(rows[0]?.querySelectorAll("td")[2]?.textContent).toBe(formatDurationMs(null));
  });

  it("accepts a custom accessible name", () => {
    const { container } = render(
      <QueryLoadArea points={POINTS} label="Query load for demo" width={400} height={96} />,
    );
    expect(container.querySelector("svg")?.getAttribute("aria-label")).toBe("Query load for demo");
  });
});
