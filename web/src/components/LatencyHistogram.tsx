/* R3.9's latency panel on R10.7's Query Detail — the four stats and the eight
 * named buckets, drawn as a distribution.
 *
 * ── the form, and why it is not one chart ─────────────────────────────────
 *
 * `choosing-a-form` answers the first half before any chart is drawn: "a handful
 * of headline numbers" is a **KPI row of stat tiles**, not a chart. min, mean,
 * max, stddev (and p50/p95/p99 when the backend has them) are four to seven
 * headline numbers, so they render as tiles. Drawing them as a bar chart — four
 * bars of 1.8, 42.7, 1840.5 and 118.3 ms — would be the same mistake in the
 * other direction: stddev is not a magnitude comparable to max, it is a
 * *summary of* the distribution the bars draw.
 *
 * The distribution itself is the second half, and it is a bar chart in one
 * sequential hue: R3.9's named buckets are ordered bins of one measure (call
 * counts), which is a distribution, and the skill's rule for magnitude along an
 * ordered axis is one hue light→dark, never a per-category hue.
 *
 * ── one hue, not a ramp across the bars ───────────────────────────────────
 *
 * Two readings of "sequential" are possible here and only one is right:
 *   (a) each bar is one step of a light→dark ramp, so the 1 ms bucket is pale
 *       and the +Inf bucket is dark;
 *   (b) all bars share `--series-1`, and length alone carries the value.
 * (a) makes colour redundant with length — two encodings of the same number,
 * which the skill forbids — and it implies the *bucket* has a magnitude, which
 * it does not: a bucket is a range, and its width already says which. (b) is
 * what LoadBars established (`--series-1` for every bar, "giving CPU and IO
 * wait different hues would use categorical colour to label columns"), and it
 * is what this file does. The ramp exists in the tokens (`--blue-100…--blue-700`)
 * for ordinal data; a histogram's bins are not an ordinal series.
 *
 * ── the cumulative trap ───────────────────────────────────────────────────
 *
 * `HistogramBucket.count` is **cumulative** (`api/types.ts:502`: "every bucket
 * counts the calls at or below `le`, so the counts are non-decreasing and the
 * `+Inf` count equals `latency.count`"). Plot the wire values and you draw a CDF
 * — a staircase that only ever climbs, ending flat at 3 007 three times — and
 * call it a distribution. `latencyBins` subtracts the previous bucket to get the
 * per-bucket counts the chart draws, and the tooltip shows both numbers so the
 * audit R9.13 promises stays possible from the UI: the fixture's derived counts
 * are 214, 888, 886, 756, 237, 17, 9, 0, 0 and they sum to exactly
 * `latency.count` (3 007), which is the invariant `dto/queries.go` calls the
 * reason the histogram is auditable.
 *
 * ── R10.7's two branches ──────────────────────────────────────────────────
 *
 * "latency panel (P50/P95/P99 **or the R3.9 fallback**)". The mirror declares
 * `p50Ms/p95Ms/p99Ms` optional because R3.9's divergence is that Cloud SQL's
 * distribution metric has no `pg_stat_statements` equivalent: a pointer to 0
 * emits `0` and renders as a tile, an omitted one renders no tile at all. The
 * fixture is the fallback branch (no percentiles, `sampled: true`), and the
 * label R3.9 mandates — "sampled, biased toward long queries" — is exported so
 * the requirement, this file and the test agree on one spelling.
 *
 * ── marks ─────────────────────────────────────────────────────────────────
 *   - bars take the bin width minus the 2px surface gap between adjacent fills.
 *     The ≤24px thickness ceiling governs a categorical bar, where width is a
 *     styling choice; in a histogram the bin width *is* the measurement, so the
 *     bar fills its bin and the ceiling does not apply.
 *   - 4px rounded on the data end (top), square at the baseline (bottom) — the
 *     vertical form of `HealthBar`'s `dataEndPath`.
 *   - gridlines hairline/recessive, baseline stronger, no axis box.
 *   - selective direct labels: the modal bucket is labelled on the chart, every
 *     bucket is in the table twin, the hovered one is in the tooltip.
 *   - text in ink tokens; the fill carries identity.
 *
 * ── R10.19 for this chart type ────────────────────────────────────────────
 *   - tooltip: the bucket, its per-bucket count and its cumulative count (the
 *     two numbers R9.13's accounting needs).
 *   - legend: none — one measure, one hue, and the title names it.
 *   - click-through: none. A bucket has no detail page to open.
 *   - crosshair: n/a, bars not lines.
 */

import { useMemo, useState } from "react";
import type { ReactElement } from "react";
import { scaleLinear } from "d3-scale";

import type { HistogramBucket, LatencyView } from "../api/types";
import { formatDurationMs, formatNumber } from "../lib/format";

/** R3.9's mandated wording, verbatim from `requirements.md:316`. Exported so the
 *  requirement's quoted string has exactly one implementation. */
export const SAMPLED_LABEL = "sampled, biased toward long queries";

const PLOT_HEIGHT = 160;
const Y_GUTTER = 52;
const X_LABEL_ROW = 16;
const GAP = 2; // the 2px surface gap between adjacent fills
const END_RADIUS = 4; // rounded data-end, square baseline

/** A bar with square bottom corners and `r`-rounded top corners: the vertical
 *  form of HealthBar's `dataEndPath`, for the same reason — the rounding
 *  belongs on the data end and the baseline stays square. */
function barPath(x: number, y: number, w: number, h: number, r: number): string {
  const radius = Math.max(0, Math.min(r, w / 2, h / 2));
  if (radius === 0) return `M ${x} ${y + h} L ${x} ${y} L ${x + w} ${y} L ${x + w} ${y + h} Z`;
  return [
    `M ${x} ${y + h}`,
    `L ${x} ${y + radius}`,
    `A ${radius} ${radius} 0 0 1 ${x + radius} ${y}`,
    `L ${x + w - radius} ${y}`,
    `A ${radius} ${radius} 0 0 1 ${x + w} ${y + radius}`,
    `L ${x + w} ${y + h}`,
    "Z",
  ].join(" ");
}

export interface LatencyBin {
  /** The bucket's upper bound as the wire spells it: "1", "5", … "10000", "+Inf". */
  le: string;
  /** The wire value, verbatim: calls at or below `le`. */
  cumulative: number;
  /** Calls in this bucket only — `cumulative[i] − cumulative[i−1]`, first bucket
   *  = `cumulative[0]`. Clamped at 0: a store that emits a decreasing count has
   *  broken the invariant, and a negative bar is a worse answer than a zero one
   *  (the chart would render a bar with no defined geometry). */
  count: number;
}

/** The pure half: cumulative wire counts → the per-bucket counts the chart draws.
 *
 *  Exported because this is where the histogram is most easily wrong (plotting
 *  the wire values draws a CDF and calls it a distribution) and the assertion
 *  that catches it — the derived counts sum to `latency.count` — needs no DOM. */
export function latencyBins(buckets: readonly HistogramBucket[]): LatencyBin[] {
  let prev = 0;
  return buckets.map((b) => {
    const count = Math.max(0, b.count - prev);
    prev = b.count;
    return { le: b.le, cumulative: b.count, count };
  });
}

/** The stat tiles R10.7 names, in the order the requirement lists them.
 *
 *  The percentiles are present only when the DTO has them: `p50Ms` etc. are
 *  `*float64` with `omitempty`, so an absent one is `undefined` and renders no
 *  tile — a `—` tile would be R10.4's "not yet" marker on a panel that is
 *  reporting what it has. A pointer to 0 is present and renders `0 ms`. */
export function latencyTiles(
  latency: LatencyView,
): { label: string; value: string }[] {
  const tiles: { label: string; value: string }[] = [
    { label: "Min", value: formatDurationMs(latency.minMs) },
    { label: "Mean", value: formatDurationMs(latency.meanMs) },
    { label: "Max", value: formatDurationMs(latency.maxMs) },
    { label: "Std dev", value: formatDurationMs(latency.stddevMs) },
  ];
  const pct: [string, number | undefined][] = [
    ["P50", latency.p50Ms],
    ["P95", latency.p95Ms],
    ["P99", latency.p99Ms],
  ];
  for (const [label, v] of pct) {
    if (v !== undefined) tiles.push({ label, value: formatDurationMs(v) });
  }
  return tiles;
}

export interface LatencyHistogramProps {
  latency: LatencyView;
  /** Total SVG width in CSS px. */
  width?: number;
}

/** R10.7's latency panel: the stat-tile row, the sampled label, the histogram,
 *  and the table twin R10.23 requires. */
export function LatencyHistogram({ latency, width = 720 }: LatencyHistogramProps): ReactElement {
  const [hover, setHover] = useState<number | null>(null);
  const bins = useMemo(() => latencyBins(latency.buckets), [latency.buckets]);
  const tiles = useMemo(() => latencyTiles(latency), [latency]);

  const plotW = Math.max(0, width - Y_GUTTER);
  const slot = bins.length > 0 ? plotW / bins.length : 0;
  const maxCount = bins.reduce((m, b) => Math.max(m, b.count), 0);
  const y = scaleLinear().domain([0, maxCount === 0 ? 1 : maxCount]).range([PLOT_HEIGHT, 0]);
  const modeIndex = bins.reduce((best, b, i) => (b.count > (bins[best]?.count ?? -1) ? i : best), 0);

  return (
    <section className="card" data-panel="latency">
      <div className="card-head">
        <h2 className="card-title">Latency</h2>
        <div className="card-actions">
          <span className="card-meta num">
            {formatNumber(latency.count)} calls · {formatDurationMs(latency.sumMs)} total
          </span>
        </div>
      </div>

      {/* R3.9's mandated label. It is not decoration: an unlabelled histogram
          from a 60 s activity sample overstates p95, and the requirement names
          the exact string the UI must show. */}
      {latency.sampled && (
        <p className="latency-note" data-testid="sampled-note">
          {SAMPLED_LABEL}
        </p>
      )}

      <div className="stat-row">
        {tiles.map((t) => (
          <div className="stat-tile" key={t.label}>
            <div className="stat-label">{t.label}</div>
            <div className="stat-value">{t.value}</div>
          </div>
        ))}
      </div>

      {bins.length > 0 && (
        <div className="chart-frame">
          <svg
            className="histogram"
            viewBox={`0 0 ${width} ${PLOT_HEIGHT + X_LABEL_ROW}`}
            width="100%"
            height={PLOT_HEIGHT + X_LABEL_ROW}
            role="img"
            aria-label={`Latency distribution across ${bins.length} buckets, ${formatNumber(latency.count)} calls`}
          >
            <g transform={`translate(${Y_GUTTER} 0)`}>
              {[0, maxCount === 0 ? 1 : maxCount].map((v) => (
                <line
                  key={`grid-${v}`}
                  className={v === 0 ? "spark-baseline" : "spark-grid"}
                  x1={0}
                  x2={plotW}
                  y1={y(v)}
                  y2={y(v)}
                />
              ))}

              {bins.map((b, i) => {
                const h = PLOT_HEIGHT - y(b.count);
                return (
                  <path
                    key={`bar-${b.le}`}
                    className="hist-bar"
                    d={barPath(i * slot, y(b.count), Math.max(0, slot - GAP), h, END_RADIUS)}
                  />
                );
              })}

              {/* Selective direct label: the modal bucket only. Labelling every
                  bar is the "a number on every point" the skill rules out, and
                  the table twin below already lists all nine. */}
              {bins.length > 0 && (
                <text
                  className="mark-label"
                  x={modeIndex * slot + slot / 2}
                  y={y(bins[modeIndex]?.count ?? 0) - 5}
                  textAnchor="middle"
                >
                  {formatNumber(bins[modeIndex]?.count ?? 0)}
                </text>
              )}

              {bins.map((b, i) => (
                <rect
                  key={`hit-${b.le}`}
                  className="spark-hit"
                  x={i * slot}
                  y={0}
                  width={slot}
                  height={PLOT_HEIGHT}
                  onMouseEnter={() => setHover(i)}
                  onMouseLeave={() => setHover((h) => (h === i ? null : h))}
                />
              ))}
            </g>

            <g className="spark-y-labels">
              {[0, maxCount === 0 ? 1 : maxCount].map((v) => (
                <text key={`yl-${v}`} className="spark-axis-label" x={Y_GUTTER - 6} y={y(v) + 3} textAnchor="end">
                  {formatNumber(v)}
                </text>
              ))}
            </g>

            <g className="spark-x-labels">
              {bins.map((b, i) => (
                <text
                  key={`xl-${b.le}`}
                  className="spark-axis-label"
                  x={Y_GUTTER + i * slot + slot / 2}
                  y={PLOT_HEIGHT + X_LABEL_ROW - 4}
                  textAnchor="middle"
                >
                  {b.le}
                </text>
              ))}
            </g>
          </svg>

          {hover !== null && bins[hover] !== undefined && (
            <div
              className="viz-tooltip"
              role="tooltip"
              style={{ left: `${((Y_GUTTER + hover * slot + slot / 2) / width) * 100}%`, top: "8px" }}
            >
              <div className="tt-value">{formatNumber(bins[hover].count)} calls</div>
              <div className="tt-label">
                {bins[hover].le === "+Inf" ? "over 10 000 ms" : `at or under ${bins[hover].le} ms`} ·{" "}
                {formatNumber(bins[hover].cumulative)} cumulative
              </div>
            </div>
          )}
        </div>
      )}

      {/* R10.23's table twin: the chart is never the only way to read it. The
          two columns are the two numbers R9.13's accounting is made of, so the
          audit is possible from the page without the SVG. */}
      {bins.length > 0 && (
        <div className="table-wrap">
          <table className="data latency-table">
            <caption className="sr-only">Latency buckets, as a table</caption>
            <thead>
              <tr>
                <th scope="col">Bucket (ms)</th>
                <th scope="col" className="num">Calls in bucket</th>
                <th scope="col" className="num">Cumulative</th>
              </tr>
            </thead>
            <tbody>
              {bins.map((b) => (
                <tr key={`row-${b.le}`}>
                  <th scope="row">{b.le}</th>
                  <td className="num">{formatNumber(b.count)}</td>
                  <td className="num">{formatNumber(b.cumulative)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
