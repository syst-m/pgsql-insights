/* R3.9's latency panel — the panel whose data shape is easiest to draw wrong.
 *
 * `HistogramBucket.count` is cumulative, so the chart's one real risk is
 * plotting the wire values and calling the resulting CDF a distribution. The
 * invariant that catches it — the derived per-bucket counts sum to
 * `latency.count` — is asserted against the fixture, where it is exact (the
 * nine derived counts sum to 3 007, the population the DTO reports), and the
 * fixture's own `+Inf` row is what makes the sum checkable at all.
 */

import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { FIXTURE_QUERY_DETAIL } from "../api/fixtures";
import type { HistogramBucket, LatencyView } from "../api/types";
import { latencyBins, latencyTiles, LatencyHistogram, SAMPLED_LABEL } from "./LatencyHistogram";

const { latency } = FIXTURE_QUERY_DETAIL;

describe("latencyBins", () => {
  it("derives per-bucket counts that sum to the population the DTO reports", () => {
    // The R9.13 audit, from the UI's side: the bars must account for every call.
    const bins = latencyBins(latency.buckets);
    const sum = bins.reduce((acc, b) => acc + b.count, 0);
    expect(sum).toBe(latency.count);
  });

  it("subtracts the previous bucket instead of plotting the cumulative values", () => {
    // Plotting the wire values gives 214, 1102, 1988, … (a staircase); the
    // distribution is 214, 888, 886, … — the calls *in* each bucket.
    expect(latencyBins(latency.buckets).map((b) => b.count)).toEqual([
      214, 888, 886, 756, 237, 17, 9, 0, 0,
    ]);
  });

  it("keeps the cumulative value alongside the per-bucket one", () => {
    // The tooltip and the table twin both need it, and re-deriving it at render
    // time is how the two halves of the audit start disagreeing.
    const bins = latencyBins(latency.buckets);
    expect(bins.map((b) => b.cumulative)).toEqual(latency.buckets.map((b) => b.count));
    expect(bins[0]).toEqual({ le: "1", cumulative: 214, count: 214 });
  });

  it("clamps a decreasing count at 0 rather than drawing a negative bar", () => {
    // A store that emits a decreasing cumulative has broken the invariant the
    // type documents. A zero bar is a visible oddity; a negative height is a
    // path with no defined geometry.
    const buckets: HistogramBucket[] = [
      { le: "1", count: 10 },
      { le: "5", count: 4 },
    ];
    expect(latencyBins(buckets).map((b) => b.count)).toEqual([10, 0]);
  });

  it("returns nothing for no buckets", () => {
    expect(latencyBins([])).toEqual([]);
  });
});

describe("latencyTiles", () => {
  it("renders the four R3.9 stats and no percentiles when the DTO has none", () => {
    // The fixture is R10.7's "or the R3.9 fallback" branch: p50/p95/p99 are
    // omitempty, so their absence renders as three fewer tiles, not as `—`.
    const tiles = latencyTiles(latency);
    expect(tiles.map((t) => t.label)).toEqual(["Min", "Mean", "Max", "Std dev"]);
    expect(tiles.map((t) => t.value)).toEqual(["1.8 ms", "43 ms", "1.84 s", "118 ms"]);
  });

  it("adds the percentiles when the DTO has them", () => {
    const withPct: LatencyView = { ...latency, p50Ms: 30, p95Ms: 300, p99Ms: 900 };
    expect(latencyTiles(withPct).map((t) => t.label)).toEqual([
      "Min",
      "Mean",
      "Max",
      "Std dev",
      "P50",
      "P95",
      "P99",
    ]);
  });

  it("renders a percentile of 0 as a tile, not as the not-yet dash", () => {
    // `omitempty` means an absent pointer and a pointer to 0 are different facts:
    // one is "no distribution to take it from", the other is a real measurement.
    const zero: LatencyView = { ...latency, p50Ms: 0 };
    const tile = latencyTiles(zero).find((t) => t.label === "P50");
    // `formatDurationMs(0)` is "0.0 ms" (the sub-10 branch keeps one decimal);
    // asserting the rendered string rather than the presence of a key is what
    // proves the branch taken was "format the value", not "render the dash".
    expect(tile?.value).toBe("0.0 ms");
  });
});

describe("LatencyHistogram", () => {
  it("shows R3.9's mandated sampled label verbatim", () => {
    const { container } = render(<LatencyHistogram latency={latency} />);
    expect(container.querySelector("[data-testid='sampled-note']")?.textContent).toBe(SAMPLED_LABEL);
  });

  it("omits the label when the numbers are not sampled", () => {
    // The label is a claim about the data, not a permanent caption: an
    // unsampled histogram must not carry it.
    const { container } = render(<LatencyHistogram latency={{ ...latency, sampled: false }} />);
    expect(container.querySelector("[data-testid='sampled-note']")).toBeNull();
  });

  it("states the population and the total in the card head", () => {
    // `count` with `sampled: true` is the sampled count, not `timesCalled`, and
    // `types.ts:529` says the UI labels them with different words. The panel
    // says "calls" against the latency block and the header says "Times called"
    // against the row — this asserts the panel's number is the latency one.
    const { container } = render(<LatencyHistogram latency={latency} />);
    expect(container.querySelector(".card-actions .card-meta")?.textContent).toBe(
      "3,007 calls · 2m 08s total",
    );
  });

  it("draws one bar per bucket, including the zero-height +Inf bar", () => {
    const { container } = render(<LatencyHistogram latency={latency} />);
    expect(container.querySelectorAll(".hist-bar")).toHaveLength(latency.buckets.length);
    // The x labels are the bucket bounds verbatim, `+Inf` included — the string
    // form is what lets the last row share one shape with the other eight.
    const labels = Array.from(container.querySelectorAll(".spark-x-labels text")).map((t) => t.textContent);
    expect(labels).toEqual(["1", "5", "25", "100", "500", "1000", "5000", "10000", "+Inf"]);
  });

  it("labels only the modal bucket on the chart", () => {
    // marks-and-anatomy: selective direct labels, never a number on every point.
    // The table twin below carries all nine; the chart carries the mode.
    const { container } = render(<LatencyHistogram latency={latency} />);
    expect(container.querySelectorAll(".mark-label")).toHaveLength(1);
    expect(container.querySelector(".mark-label")?.textContent).toBe("888");
  });

  it("ships the table twin R10.23 requires", () => {
    // The chart is never the only way to read it, and the two columns are the two
    // numbers R9.13's accounting is made of.
    const { container } = render(<LatencyHistogram latency={latency} />);
    const rows = container.querySelectorAll(".latency-table tbody tr");
    expect(rows).toHaveLength(latency.buckets.length);
    expect(rows[1]?.textContent).toBe("58881,102");
  });

  it("shows both counts in the tooltip for a hovered bucket", () => {
    const { container } = render(<LatencyHistogram latency={latency} />);
    const hits = container.querySelectorAll(".spark-hit");
    expect(hits).toHaveLength(latency.buckets.length);
    fireEvent.mouseEnter(hits[1] as Element);
    const tip = container.querySelector(".viz-tooltip");
    expect(tip?.querySelector(".tt-value")?.textContent).toBe("888 calls");
    expect(tip?.querySelector(".tt-label")?.textContent).toBe("at or under 5 ms · 1,102 cumulative");
  });

  it("explains itself instead of drawing an empty chart when there are no buckets", () => {
    // R10.13's rule generalised: the panel has a reason ("no buckets") and must
    // say it rather than rendering a frame with nothing in it.
    const empty: LatencyView = {
      minMs: 0,
      meanMs: 0,
      maxMs: 0,
      stddevMs: 0,
      sampled: false,
      buckets: [],
      sumMs: 0,
      count: 0,
    };
    const { container } = render(<LatencyHistogram latency={empty} />);
    expect(container.querySelector(".histogram")).toBeNull();
    expect(container.querySelector(".card")).not.toBeNull();
  });
});
