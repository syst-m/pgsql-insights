/* R10.7's per-minute load chart — the small multiples, the four-metric set, and
 * the one multiply that keeps seconds from being read as milliseconds.
 *
 * `formatSec` is tested first and hardest because it is the component's only
 * arithmetic: every other assertion is about which DOM nodes exist, and a wrong
 * multiplier changes no DOM at all. A 42.7-second minute rendered as "43 ms" is
 * a 1000× error that still looks like a plausible duration, which is what makes
 * it invisible without a test.
 */

import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { QuerySeries, QuerySeriesPoint } from "../api/types";
import { formatTimestamp } from "../lib/format";
import { formatSec, QueryLoadChart, SERIES_METRICS, seriesAxisLabels, seriesXLabels } from "./QueryLoadChart";
import { axisTimeLabel } from "./Sparkline";

/** Three minutes of a fingerprint, each field a different number so a metric
 *  reading the wrong field is visible in the assertion rather than hidden by
 *  two fields that happen to agree. */
const POINTS: QuerySeriesPoint[] = [
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
    executionTimeSeconds: 75,
    rows: 4,
    sharedBlocksHit: 18,
    sharedBlocksRead: 3,
    lockTimeSeconds: 0,
    ioTimeSeconds: 0.0103,
  },
];

const SERIES: QuerySeries = {
  fingerprint: "843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3",
  queryid: "4459823716093821447",
  namespace: "open-webui",
  cluster: "open-webui-pg",
  points: POINTS,
};

describe("formatSec", () => {
  it("multiplies by 1000 before handing the value to the ms formatter", () => {
    // The R16.8 units rule in one assertion: series points are seconds, every
    // other duration in the app is milliseconds.
    expect(formatSec(1.8)).toBe("1.80 s");
    expect(formatSec(42.7)).toBe("42.70 s");
  });

  it("keeps a sub-second value in ms, where the formatter's own contract puts it", () => {
    expect(formatSec(0.5)).toBe("500 ms");
    expect(formatSec(0)).toBe("0.0 ms");
  });

  it("crosses into minutes the way the shared formatter does", () => {
    expect(formatSec(75)).toBe("1m 15s");
  });

  it("does not leak float artefacts into the rendered digits", () => {
    // 42.7 is not exactly representable; 42.7 * 1000 is 42700.000000000004. The
    // round-to-hundredths step is what stops the formatter printing that.
    expect(formatSec(42.7)).not.toContain("000000000004");
  });
});

describe("SERIES_METRICS", () => {
  it("is R9.5's per-query family, in R9.5's order", () => {
    expect(SERIES_METRICS.map((m) => m.metric)).toEqual([
      "execution_time",
      "rows",
      "shared_blocks_hit",
      "shared_blocks_read",
    ]);
  });

  it("omits lock_time, which the S25 ruling keeps out of the per-query family", () => {
    // The wire type carries `lockTimeSeconds` and this array deliberately does
    // not. Widening the ruling is one entry here, which is why the decision is
    // written as data and not spread across JSX.
    const keys: string[] = SERIES_METRICS.map((m) => m.key);
    expect(keys).not.toContain("lockTimeSeconds");
  });

  it("names a formatter for every metric, so no frame renders a raw number", () => {
    // The two unit families: seconds-valued metrics go through formatSec, counts
    // through formatNumber. A metric with the wrong one is the units bug again.
    expect(SERIES_METRICS.find((m) => m.key === "executionTimeSeconds")?.format).toBe(formatSec);
    for (const m of SERIES_METRICS) expect(typeof m.format).toBe("function");
  });
});

describe("seriesXLabels", () => {
  it("renders every point's timestamp through the shared formatter", () => {
    // R10.21: every timestamp the API emits is RFC 3339 UTC; the shared formatter
    // is the app's one rendering of that. Index-aligned with the values so the
    // sparkline's tick selection labels the minute it points at.
    expect(seriesXLabels(POINTS)).toEqual(POINTS.map((p) => formatTimestamp(p.timestamp)));
  });

  it("returns nothing for no points", () => {
    expect(seriesXLabels([])).toEqual([]);
  });
});

describe("seriesAxisLabels", () => {
  it("gives the axis the short form of the same points, in the same order", () => {
    // The axis and the tooltip want two different strings for one point (see
    // `Sparkline.tsx`'s `axisTimeLabel`); this pins that the axis array is that
    // short form over the same points in the same order, not a re-derived subset.
    expect(seriesAxisLabels(POINTS)).toEqual(POINTS.map((p) => axisTimeLabel(p.timestamp)));
    for (const label of seriesAxisLabels(POINTS)) expect(label).toMatch(/^\d{2}:\d{2}$/);
  });

  it("returns nothing for no points", () => {
    expect(seriesAxisLabels([])).toEqual([]);
  });
});

describe("QueryLoadChart", () => {
  it("draws one framed sparkline per metric — small multiples, not one chart", () => {
    // The one-axis rule: five measures on three scales cannot share a chart, and
    // the skill's remedy is small multiples. Four svgs is the shape of that.
    const { container } = render(<QueryLoadChart series={SERIES} />);
    expect(container.querySelectorAll("svg.sparkline")).toHaveLength(SERIES_METRICS.length);
    expect(container.querySelectorAll(".small-multiple")).toHaveLength(SERIES_METRICS.length);
  });

  it("titles each frame with the display name and the R9.4 metric name", () => {
    // The metric name is printed alongside the label so a reader can tie the
    // frame to the spec and to the PromQL name without a legend.
    const { container } = render(<QueryLoadChart series={SERIES} />);
    expect(container.querySelectorAll(".small-multiple-title")).toHaveLength(4);
    expect(container.querySelector(".small-multiple-metric")?.textContent).toBe("execution_time");
  });

  it("counts the minutes in the card head, singularly for one", () => {
    const { container } = render(<QueryLoadChart series={SERIES} />);
    expect(container.querySelector(".card-actions .card-meta")?.textContent).toBe("3 minutes");
    const one = render(<QueryLoadChart series={{ ...SERIES, points: POINTS.slice(0, 1) }} />);
    expect(one.container.querySelector(".card-actions .card-meta")?.textContent).toBe("1 minute");
  });

  it("feeds each frame the values of its own field", () => {
    // The field-to-frame wiring: hover the second point of the first frame and
    // the tooltip must show that point's executionTimeSeconds, formatted as a
    // duration — which also re-proves the multiply through the render path.
    const { container } = render(<QueryLoadChart series={SERIES} />);
    const hits = container.querySelectorAll(".small-multiple .spark-hit");
    expect(hits.length).toBeGreaterThan(0);
    fireEvent.mouseEnter(hits[1] as Element);
    expect(container.querySelector(".small-multiple .viz-tooltip .tt-value")?.textContent).toBe(
      "42.70 s",
    );
  });

  it("says the client has not requested the series when the prop is absent", () => {
    // The documented stand-in for A4's missing `fetchQuerySeries`. R10.13 forbids
    // a blank box, and this is the case where the page has data but no chart.
    const { container } = render(<QueryLoadChart />);
    const empty = container.querySelector("[data-testid='load-empty']");
    expect(empty?.textContent).toContain("No series loaded");
    expect(empty?.textContent).toContain("/api/v1/insights/queries/series");
    expect(container.querySelector(".small-multiples")).toBeNull();
  });

  it("says the store has no samples when the series is empty", () => {
    // A different answer to a different question: one needs the fetcher, the
    // other needs the collector. Collapsing them into one sentence loses which.
    const { container } = render(<QueryLoadChart series={{ ...SERIES, points: [] }} />);
    expect(container.querySelector("[data-testid='load-empty']")?.textContent).toBe(
      "The store has no per-minute samples for this fingerprint.",
    );
  });
});
