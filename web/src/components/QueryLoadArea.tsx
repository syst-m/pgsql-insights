/* R10.5(a)'s Query load chart: the stacked area of total execution time and IO
 * time.
 *
 * ── which requirement, and why this file exists at all ────────────────────
 *
 * §4's R10.5 cell is one row with three parts, and A3's ruling (`2a9c429`)
 * splits it between two phases by naming the parts instead of lettering them:
 *
 *   10.5 *(Query load)*                            → **6b**
 *   10.5 *(Resource load, R5.1 cards, topology)*   → **7**
 *
 * This file is the first line of that split — the chart 6b owns. The other
 * half's panel is `ResourceLoadPanel.tsx`. The ruling is also why the split is
 * written by *part* and not by *letter*: "the letters label §4's cell, they do
 * not name the scopes, and they cannot carry the unlettered *Plus* clauses."
 *
 * The name is the one `QueryLoadChart.tsx:7` reserves for this chart: "R10.5(a)'s
 * `QueryLoadArea` (the Cluster Overview's stacked area of total execution time
 * + IO time) is a *different* requirement's chart, and this file is
 * deliberately not named `QueryLoadArea` so the two cannot be confused by
 * whoever comes after and reads a list of components." That file was R10.7's
 * small multiples; this one is the requirement that sentence was pointing at.
 *
 * ── the two series, and the one the contract has ───────────────────────────
 *
 * R10.5(a) names two measures. The contract carries one of them as a series.
 *
 *   total execution time  `QuerySeriesPoint.executionTimeSeconds` — R9.4's
 *                         `execution_time`, per minute, in **seconds**.
 *   IO time               no series field exists. R9.4's family
 *                         (requirements.md:396) is "execution time, rows,
 *                         shared block hits/reads, **lock time**" — five
 *                         members, none of them IO — and the OpenAPI
 *                         `x-promql-catalogue` for `/api/v1/metrics/query`
 *                         enumerates exactly those five plus
 *                         `aggregate.cache_hit_ratio`. The API's only IO-time
 *                         value is `QueryView.loadByIOWaitMs` (types.ts:413,
 *                         `dto/queries.go:77`): R3.2's derived load column,
 *                         `blk_read_time + blk_write_time` on PG 15–16 and
 *                         `shared_blk_read_time + shared_blk_write_time` on
 *                         PG 17+. That is a per-query scalar on a list row, not
 *                         a per-minute series, so it cannot be a band here.
 *
 * So the IO band is `null` and says so in three places that each answer a
 * different question: the **legend** (R10.5(a)'s second measure is accounted
 * for rather than silently dropped), the **tooltip** (only drawn series appear,
 * so a reader is never shown a number that is not data), and **`stackSeries`'s
 * return value** — the null is *produced by the function that reads the
 * contract*, which is what stops a caller from passing a plausible `0` and
 * turning "we do not have this" into "this minute had none".
 *
 * The ask it documents is A1/A2's, not a local interface (R16.8), and A2 has
 * since answered it: the ask is right, and it is A3's ruling to make — growing
 * R9.4's frozen cell has exactly one precedent, R9.11's admission of lock_time,
 * which that cell calls "added by ruling rather than by drift". Two facts came
 * back with the answer and belong here:
 *
 *   - the exporter already mints per-query IO time. `collector/naming.go:135`
 *     and `:136` name R2.4's enumerated pair
 *     (`query_shared_blk_read_time_seconds_total`,
 *     `query_shared_blk_write_time_seconds_total`), with the pre-PG16
 *     `blk_read_time`/`blk_write_time` successors at `:152-153`. So "no IO-time
 *     field exists" is true of the DTO and the catalogue, not of the collector —
 *     which is why the answer is not "the series has no IO dimension";
 *   - a single `io_time` member over two counters has to be *derived*, and
 *     `api/promql_test.go:955` enforces `aggregate.cache_hit_ratio` as the only
 *     entry allowed an empty `Metric` — so the derived entry costs widening that
 *     test's exception from one name to a set. R3.2's own derivation
 *     (`loadByIOWaitMs` = read + write on both PG generations) is this chart's
 *     answer to the read+write-or-read-only half: the band R10.5(a) names is the
 *     sum, so the UI wants read + write.
 *
 * ── what the sum is, and what it is not ────────────────────────────────────
 *
 * R10.5(a) is a cluster-level chart and the route that exists is
 * per-fingerprint (R8.11's `/insights/queries/series`, one entry per
 * fingerprint). Summing a batch gives the *batch's* load — R9.5's top-N, N=50
 * by default — not the cluster's total, which is R9.4's aggregate family on
 * `/api/v1/metrics/query`. The scope line says "top-N" for that reason; when
 * the aggregate route gets a fetcher, the same component takes its points and
 * the qualifier goes away.
 *
 * ── units ──────────────────────────────────────────────────────────────────
 *
 * Series points are **seconds**; `QueryView`/`LoadColumns` are milliseconds
 * (`dto/series.go` and `api/types.ts:639` both say so). `formatDurationMs` is
 * the app's only duration formatter and takes ms, so feeding it a seconds value
 * renders a 42.7-second minute as "43 ms" — a 1000× error that looks like a
 * plausible number and is therefore invisible. `formatSec` below is this file's
 * one multiplier, the same role `QueryLoadChart.tsx`'s private `formatSec`
 * plays there. Two copies of a four-line formatter exist until it moves to
 * `lib/format.ts`, which is A4's file and whose contract is R3.2's column
 * formatters.
 *
 * ── colour (colour-formula, and the validator) ─────────────────────────────
 *
 * Two named series, so the categorical job applies and the order is fixed,
 * never cycled: execution time takes `--series-1` (#2a78d6 light / #3987e5
 * dark) and IO time takes `--series-2` (#eb6834 / #d95926). Both are existing
 * app tokens, validated as a pair in `scripts/validate_palette.js` under
 * `--mode light` and `--mode dark`; nothing new is minted here. Colour follows
 * the measure, not its rank — the IO band keeps `--series-2` whether or not it
 * draws, so the day it gains data nothing repaints.
 *
 * Marks (marks-and-anatomy): area fills are the hue at 10% with a 2px stroke of
 * the full hue; stacked fills are separated by the 2px surface gap, which
 * `areaPath` produces by insetting each *shared* edge by 1px — with one band
 * drawn there is no shared edge, so nothing is inset and the band sits on the
 * baseline. Text wears ink tokens (`--text-muted` on the axes,
 * `--text-secondary` on the direct label), never the series colour.
 *
 * ── R10.19 for this chart type ─────────────────────────────────────────────
 *   - **hover**: a crosshair at the bucket plus one tooltip listing *every
 *     drawn series at that x* (interaction.md's rule for a line or area),
 *     values leading and the bucket timestamp following in R10.21's format;
 *   - **legend**: always present for ≥ 2 series. It lists both measures the
 *     requirement names, so identity is never colour-alone (R10.23) and the
 *     absent one is visible as absent. Click-to-isolate is degenerate here —
 *     one drawable band cannot be hidden and still leave a chart — so the
 *     entries are not buttons, which is stated rather than left as a disabled
 *     control;
 *   - **click-through**: none. R10.19 asks for a target per chart type; a
 *     cluster-level area has no row to open, and the page it mounts on is the
 *     one whose table rows are clickable, so the frame is not a link;
 *   - **table twin** (R10.23): the buckets the axis labels, as a table, so the
 *     chart is readable without colour and without hover.
 *
 * ── R10.13's empty rule ────────────────────────────────────────────────────
 *
 * `points: []` renders the sentence, not an empty frame. One sentence, not two
 * as `QueryLoadChart.tsx` has: `stackSeries([])` and
 * `stackSeries([{ …, points: [] }])` both yield `[]`, so "the page has not
 * fetched" and "the batch had no minutes" are not distinguishable from the
 * input this component is given, and the copy says both rather than guessing.
 */

import { useMemo, useState } from "react";
import type { ReactElement } from "react";
import { scaleLinear } from "d3-scale";

import type { SeriesRange } from "../api/client";
import type { QuerySeries } from "../api/types";
import { formatDurationMs, formatTimestamp } from "../lib/format";

/** x-axis label row, in px. The same value as `Sparkline.tsx`'s private
 *  `X_AXIS_ROW`; the two charts want the same axis height, and the constant is
 *  not exported there because it is a layout detail of one component. */
const X_AXIS_ROW = 14;
/** y-label gutter, in px. Same value as `Sparkline.tsx`'s `Y_GUTTER`, same
 *  reason: a wide y label must never overlap the plot it labels. */
const Y_GUTTER = 46;
/** Max x tick labels — `Sparkline.tsx`'s `MAX_X_TICKS` carries the reasoning
 *  (more collide at any width, fewer stop being an axis). */
const MAX_X_TICKS = 6;
/** Rows in the table twin. More than the axis gets, because a table row costs
 *  nothing that a text measurement costs; far fewer than the series has,
 *  because a 7 d range at one-minute buckets is 10 080 rows and a table that
 *  long is not a reading aid. `ticksFor` picks them — the same function the
 *  axis asks, so the two are one rule over one array rather than two samples of
 *  it; the caps differ because one is limited by text width and the other by a
 *  row budget. */
const MAX_TWIN_ROWS = 12;
/** Half the 2px surface gap marks-and-anatomy requires between touching fills. */
const GAP_HALF = 1;

/** Two decimals, which is the precision a viewBox needs and no more — the same
 *  rule `Sparkline.tsx`'s `n()` states for the same reason. */
function n(v: number): string {
  return String(Math.round(v * 100) / 100);
}

/** Seconds → the house duration string, via the ms formatter. See the header's
 *  "units" section: this is the only place in this file that multiplies. */
export function formatSec(sec: number): string {
  return formatDurationMs(sec * 1000);
}

/** One time bucket of the cluster's query load.
 *
 *  The shape is the contract's own idea, not a new one: `QuerySeriesPoint`
 *  bundles five measures with one timestamp precisely so they cannot disagree
 *  about which minute they describe (R8.11's argument for one route with no
 *  `metric=` parameter). A bucket does the same thing at cluster level — one
 *  timestamp, every measure for it — so `points[3].t` is the same minute for
 *  both series and no caller can pass two arrays of different lengths. */
export interface QueryLoadPoint {
  /** Bucket start, RFC 3339 UTC (R10.21: every timestamp the API emits is
   *  RFC 3339 UTC). A string, as it is on the wire and in `QuerySeriesPoint`. */
  t: string;
  /** Total execution time in the bucket, in **seconds** (types.ts:639). */
  execSec: number;
  /** IO time in the bucket, in seconds, or `null` when the source has no
   *  IO-time member — which is every source today. `null` is not `0`: the
   *  header's "two series" section is the argument, and `stackSeries` is the
   *  function that produces it. */
  ioSec: number | null;
}

/** Fold a batch of per-fingerprint series into one bucket-per-timestamp stack.
 *
 *  Exported and pure for the same reason `linePath` is: the summing is the part
 *  that can be wrong without being visible. Merging on the timestamp rather
 *  than on the array index is the whole job — two fingerprints sampled in
 *  different minutes have different `points` arrays of different lengths, and
 *  zipping them by index would add one query's 14:03 to another's 14:07.
 *
 *  Sorting the RFC 3339 strings lexicographically *is* sorting them
 *  chronologically while the format holds (fixed width, always `Z`, per
 *  R10.21); a `+00:00` offset spelling or a missing seconds field would break
 *  that, which is why the contract pins the format and this file does not
 *  re-parse it into a `Date`.
 *
 *  `ioSec` is `null` on every bucket this returns, and that is the contract
 *  read back rather than a placeholder: `QuerySeriesPoint` has no IO-time
 *  field. When one lands, the line that sets it is the only line that changes. */
export function stackSeries(series: readonly QuerySeries[]): QueryLoadPoint[] {
  const totals = new Map<string, number>();
  for (const s of series) {
    for (const p of s.points) {
      // A non-finite value voids the bucket, not the chart: `linePath` drops
      // these for the same reason (one `NaN` in a path string invalidates the
      // whole path), and summing one would make the bucket's total a lie.
      if (!Number.isFinite(p.executionTimeSeconds)) continue;
      totals.set(p.timestamp, (totals.get(p.timestamp) ?? 0) + p.executionTimeSeconds);
    }
  }
  return [...totals.entries()]
    .sort((a, b) => (a[0] === b[0] ? 0 : a[0] < b[0] ? -1 : 1))
    .map(([t, execSec]) => ({ t, execSec, ioSec: null }));
}

/** The y domain of a stacked chart: `[0, max]`, never the data's own min.
 *
 *  A filled area's height *is* its value, so a floor above zero truncates every
 *  band at once and the chart lies about magnitudes it looks like it is
 *  reporting. `Sparkline` may use the data's min because a line has no
 *  baseline; an area must not. A max of 0 widens to `[0, 1]` so the axis still
 *  has two labels and the flat line sits on the baseline. */
export function stackDomain(max: number): readonly [number, number] {
  return max > 0 ? [0, max] : [0, 1];
}

/** The closed `d` for one stacked band: forward along its top edge, back along
 *  its bottom edge, then `Z`.
 *
 *  `insetTop` / `insetBottom` shrink the band by part of the 2px surface gap
 *  between touching fills (marks-and-anatomy, "stacked segments and adjacent
 *  bars alike"). Two stacked bands each give up 1px at the edge they share, so
 *  the gap between them is 2px of surface; a band with no neighbour on one side
 *  gives up nothing there, which is why a single-band chart sits on the
 *  baseline instead of floating 1px above it.
 *
 *  `top` and `bottom` must be index-aligned — they are one bucket table's two
 *  edges. A length mismatch returns `""` rather than closing a polygon whose
 *  last edge is a guess, and a non-finite value voids the whole band rather
 *  than one vertex of it, because that is what SVG does with a `NaN` in a path. */
export function areaPath(
  top: readonly number[],
  bottom: readonly number[],
  width: number,
  height: number,
  domain: readonly [number, number],
  insetTop = 0,
  insetBottom = 0,
): string {
  if (top.length === 0 || top.length !== bottom.length) return "";
  if (width <= 0 || height <= 0) return "";
  if (!top.every(Number.isFinite) || !bottom.every(Number.isFinite)) return "";

  const [lo, hi] = domain;
  const x = scaleLinear()
    .domain([0, Math.max(top.length - 1, 1)])
    .range([0, width]);
  const y = scaleLinear()
    .domain([lo, hi])
    .range([height, 0]);

  const upper = top.map((v, i) => `${i === 0 ? "M" : "L"}${n(x(i))},${n(y(v) + insetTop)}`);
  // Reversed, so the polygon runs clockwise: along the top, back along the
  // bottom. `slice()` because `reverse()` mutates and `bottom` is a readonly
  // array the caller still owns.
  const lower = bottom
    .slice()
    .reverse()
    .map((v, j) => `L${n(x(bottom.length - 1 - j))},${n(y(v) - insetBottom)}`);

  return `${upper.join(" ")} ${lower.join(" ")} Z`;
}

/** One drawable band of the stack. */
interface Band {
  /** Stable id; the CSS class that carries the hue is `qla-band--${key}`. */
  key: "exec" | "io";
  label: string;
  top: readonly number[];
  bottom: readonly number[];
}

/** The bands to draw, bottom-up, from the bucket table.
 *
 *  The execution band draws whenever there are buckets. The IO band draws only
 *  when **every** bucket carries a value: a stack cannot have a hole in it,
 *  because a missing minute would collapse the band above it to the wrong
 *  height, and `null` here means "this source has no such measure", not "this
 *  minute was zero". */
function bandsOf(points: readonly QueryLoadPoint[]): Band[] {
  if (points.length === 0) return [];
  const execTop = points.map((p) => p.execSec);
  const bands: Band[] = [
    { key: "exec", label: "Total execution time", top: execTop, bottom: points.map(() => 0) },
  ];
  const io = points.map((p) => p.ioSec);
  if (io.every((v): v is number => v !== null)) {
    bands.push({
      key: "io",
      label: "IO time",
      top: points.map((p, i) => p.execSec + (io[i] ?? 0)),
      bottom: execTop,
    });
  }
  return bands;
}

/** `count` indices, evenly spaced, always including the first and the last,
 *  capped at `max`.
 *
 *  Indices rather than measured text, so the result is deterministic in jsdom
 *  (where SVG layout is not implemented) and identical between the two places
 *  that use it: the axis and the table twin. Two different samples of one
 *  series is how a table ends up disagreeing with the axis above it. */
export function ticksFor(count: number, max: number): number[] {
  if (count <= 0 || max <= 0) return [];
  const ticks = Math.min(max, count);
  const step = (count - 1) / Math.max(ticks - 1, 1);
  return Array.from({ length: ticks }, (_, i) => Math.round(i * step));
}

export interface QueryLoadAreaProps {
  /** The buckets to plot.
   *
   *  `stackSeries(fetchQuerySeries(cfg, …).data)` is the production input. It
   *  is a prop and not a fetch inside this component because R8.11's whole
   *  claim is that *one* response serves this chart, R10.6's sparkline column
   *  and R10.7's detail chart; a component that fetches for itself turns one
   *  request into three and spends R10.14's 1.5 s budget on the wrong thing. */
  points: readonly QueryLoadPoint[];
  /** The window the batch was fetched for, in the API's own vocabulary.
   *
   *  Optional, and the optionality *is* the `24h` problem: `urlState.ts:53`'s
   *  `TimeRange` is `1h | 24h | 7d | 30d` while `SeriesRange` is
   *  `1h | 6h | 1d | 7d | 30d | custom`, and `24h` is a value the API rejects.
   *  A3 is ruling R10.15's five presets before anyone renames the URL's
   *  spelling, so hardcoding the URL's word here would bake a rejected value
   *  into a second file and make the ruling cost more than it should. Omitting
   *  it renders the chart without a window label rather than with a wrong one. */
  range?: SeriesRange;
  /** Accessible name for the `<svg role="img">`. */
  label?: string;
  /** viewBox width, in CSS px; the element itself is `width:100%`. */
  width?: number;
  /** Plot height in px, excluding the x-label row. Taller than a sparkline's
   *  48 px because a stacked area's bands need vertical room to separate. */
  height?: number;
}

/** R10.5(a)'s Query load: the stacked area of total execution time and IO time. */
export function QueryLoadArea({
  points,
  range,
  label,
  width = 720,
  height = 160,
}: QueryLoadAreaProps): ReactElement {
  const [hover, setHover] = useState<number | null>(null);

  const bands = useMemo(() => bandsOf(points), [points]);
  const maxTop = useMemo(() => {
    let m = 0;
    for (const b of bands) for (const v of b.top) if (Number.isFinite(v) && v > m) m = v;
    return m;
  }, [bands]);
  const domain = stackDomain(maxTop);
  const plotW = Math.max(0, width - Y_GUTTER);
  const axisTicks = useMemo(() => ticksFor(points.length, MAX_X_TICKS), [points.length]);
  const twinRows = useMemo(() => ticksFor(points.length, MAX_TWIN_ROWS), [points.length]);

  const ioDrawn = bands.some((b) => b.key === "io");
  const totalH = height + X_AXIS_ROW;
  const x = scaleLinear()
    .domain([0, Math.max(points.length - 1, 1)])
    .range([0, plotW]);
  const y = scaleLinear()
    .domain([domain[0], domain[1]])
    .range([height, 0]);

  const name = label ?? "Query load";

  return (
    <section className="card" aria-labelledby="qla-title">
      <div className="card-head">
        <h2 className="card-title" id="qla-title">
          Query load
        </h2>
        <span className="card-meta">R10.5</span>
      </div>

      {/* What the plot is a sum of, and over what window. The top-N qualifier
          is here because the route this reads is per-fingerprint (R8.11) and
          R9.5 caps it at 50; the cluster total is R9.4's aggregate family. */}
      <p className="qla-scope">
        Total execution time across the top-N fingerprints in the batch
        {range === undefined ? "" : `, last ${range}`} — the R9.4 aggregate series is the cluster
        total and has no fetcher yet.
      </p>

      {/* R10.19's legend, rendered even when the chart is empty: the legend is
          the requirement's own inventory, and the day IO time gains a field the
          `n/a` disappears from here as visibly as the band appears on the plot. */}
      <ul className="qla-legend">
        <li className="qla-legend-item">
          <span className="qla-key qla-key--exec" aria-hidden="true" />
          <span>Total execution time</span>
        </li>
        <li className="qla-legend-item">
          <span className={`qla-key qla-key--io${ioDrawn ? "" : " qla-key--absent"}`} aria-hidden="true" />
          <span>IO time{ioDrawn ? "" : " · n/a"}</span>
        </li>
      </ul>

      {points.length === 0 ? (
        <p className="chart-empty">
          No query-load buckets for this window — the page has not fetched a series batch, or the
          batch it fetched had no minutes.
        </p>
      ) : (
        <div className="chart-frame">
          <svg
            className="sparkline"
            viewBox={`0 0 ${width} ${totalH}`}
            width="100%"
            height={totalH}
            role="img"
            aria-label={name}
          >
            <g transform={`translate(${Y_GUTTER} 0)`}>
              <g className="spark-grid-group">
                {[domain[0], (domain[0] + domain[1]) / 2, domain[1]].map((v) => (
                  <line
                    key={`grid-${n(v)}`}
                    className={v === domain[0] ? "spark-baseline" : "spark-grid"}
                    x1={0}
                    x2={n(plotW)}
                    y1={n(y(v))}
                    y2={n(y(v))}
                  />
                ))}
              </g>

              {bands.map((b, i) => {
                const d = areaPath(
                  b.top,
                  b.bottom,
                  plotW,
                  height,
                  domain,
                  // The 2px surface gap, split between the two bands that share
                  // an edge: a band insets an edge only if a band touches it.
                  i < bands.length - 1 ? GAP_HALF : 0,
                  i > 0 ? GAP_HALF : 0,
                );
                if (d === "") return null;
                return <path key={`band-${b.key}`} className={`qla-band qla-band--${b.key}`} d={d} />;
              })}

              {/* Selective direct labels: one per drawn band at the right edge,
                  never a value at every bucket. A band thinner than the text is
                  not labelled at all — the legend already names it, and a label
                  taller than its band is the collision the skill says not to
                  resolve by stacking. */}
              {bands.map((b) => {
                const topY = y(b.top[b.top.length - 1] ?? 0);
                const botY = y(b.bottom[b.bottom.length - 1] ?? 0);
                if (botY - topY < 10) return null;
                return (
                  <text key={`label-${b.key}`} className="qla-label" x={n(plotW - 4)} y={n((topY + botY) / 2 + 3)}>
                    {b.label}
                  </text>
                );
              })}

              {/* Hit targets: full-height rects, one per bucket, wider than any
                  mark — a band's edge is a 2px stroke and not a target a pointer
                  finds (R10.19's ≥ 24px target). */}
              {points.map((p, i) => (
                <rect
                  key={`hit-${p.t}`}
                  className="spark-hit"
                  x={n(Math.max(0, x(i) - plotW / Math.max(points.length - 1, 1) / 2))}
                  y={0}
                  width={n(plotW / Math.max(points.length - 1, 1))}
                  height={height}
                  onMouseEnter={() => setHover(i)}
                  onMouseLeave={() => setHover((h) => (h === i ? null : h))}
                />
              ))}

              {hover !== null && (
                <line
                  className="spark-crosshair"
                  x1={n(x(hover))}
                  x2={n(x(hover))}
                  y1={0}
                  y2={height}
                />
              )}
            </g>

            <g className="spark-y-labels">
              {[domain[0], (domain[0] + domain[1]) / 2, domain[1]].map((v) => (
                <text key={`y-${n(v)}`} className="spark-axis-label" x={Y_GUTTER - 6} y={n(y(v) + 3)} textAnchor="end">
                  {formatSec(v)}
                </text>
              ))}
            </g>

            <g className="spark-x-labels">
              {axisTicks.map((i) => (
                <text
                  key={`x-${i}`}
                  className="spark-axis-label"
                  x={n(Y_GUTTER + x(i))}
                  y={height + X_AXIS_ROW - 3}
                  textAnchor={i === 0 ? "start" : i === points.length - 1 ? "end" : "middle"}
                >
                  {formatTimestamp(points[i]?.t ?? null)}
                </text>
              ))}
            </g>
          </svg>

          {hover !== null && (
            <div className="viz-tooltip" role="tooltip" style={{ left: `${((Y_GUTTER + x(hover)) / width) * 100}%`, top: 8 }}>
              {/* Values lead, labels follow (interaction.md), and *every* drawn
                  series at this x is listed — one tooltip for the crosshair,
                  not one per band. */}
              {bands.map((b) => {
                // The band's own value at this bucket, not the stack height: a
                // stacked area's top edge is the *cumulative* figure, so a
                // tooltip that read the edge would show the IO band's value as
                // exec + io and the exec band's as the total.
                const v = (b.top[hover] ?? 0) - (b.bottom[hover] ?? 0);
                return (
                  <div className="tt-row" key={`tt-${b.key}`}>
                    <span className={`line-key qla-key--${b.key}`} aria-hidden="true" />
                    <span className="tt-value">{formatSec(v)}</span>
                  </div>
                );
              })}
              <div className="tt-label">{formatTimestamp(points[hover]?.t ?? null)}</div>
            </div>
          )}
        </div>
      )}

      {/* R10.23's table twin: the chart is never the only way to read it. The
          rows come from `ticksFor`, the same function the axis above asks, so
          the table is one rule over one array rather than a second sample of it
          — see `MAX_TWIN_ROWS` for why the twin is a sample and not the whole
          series. */}
      {points.length > 0 && (
        <div className="table-wrap">
          <table className="data qla-table">
            <caption className="sr-only">Query load buckets, as a table</caption>
            <thead>
              <tr>
                <th scope="col">Bucket</th>
                <th scope="col">Total execution time</th>
                <th scope="col">IO time</th>
              </tr>
            </thead>
            <tbody>
              {twinRows.map((i) => (
                <tr key={`row-${points[i]?.t ?? i}`}>
                  <td className="mono">{formatTimestamp(points[i]?.t ?? null)}</td>
                  <td className="num">{formatSec(points[i]?.execSec ?? 0)}</td>
                  <td className="num">
                    {points[i]?.ioSec === null || points[i] === undefined
                      ? formatDurationMs(null)
                      : formatSec(points[i]?.ioSec ?? 0)}
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
