/* One line primitive, three sizes (R10.6 sparkline · R10.7 detail chart).
 *
 * R8.11 is the reason this file exists as a *shared* primitive rather than two
 * charts. Its whole claim is that one response answers both chart requirements:
 * "Carrying all five per point is what lets one response serve both R10.6's row
 * sparkline and R10.7's Query Detail chart, and it is why the route takes no
 * `metric=` parameter" (`internal/api/dto/series.go:31`). A per-metric request
 * would need five calls per page to fill one table of sparklines, which is
 * R10.14's 1.5 s budget spent on the wrong thing. So the data path is one
 * route, and the renderer for it should be one file too — which is the
 * coordinator's instruction on my stage list verbatim: "your stage-4 `Sparkline`
 * work and the Detail chart are the same data path — build them together, not
 * two stages apart."
 *
 * Three contexts, one component:
 *   1. the Queries table cell (R10.6) — `framed: false`, 96×20, no axes;
 *   2. a small multiple inside R10.7's load chart (`QueryLoadChart.tsx`) —
 *      `framed: true`, one per R9.5 metric;
 *   3. any single-series line chart elsewhere that wants the same mark.
 *
 * ── why small multiples, and not one five-series chart ────────────────────
 *
 * The dataviz skill's #1 chart mistake is the dual-axis chart, and its rule is
 * absolute: "One axis. Never a dual-axis chart (two y-scales). Two measures of
 * different scale -> two charts, small multiples, or indexed to a common base."
 * `QuerySeriesPoint` carries seconds (`executionTimeSeconds`, `lockTimeSeconds`)
 * next to counts (`rows`, `sharedBlocksHit`, `sharedBlocksRead`) — five
 * measures on three different scales, so even a *single*-axis five-series line
 * chart would plot 3 007 rows against 42.7 seconds. The answer the skill gives
 * is small multiples, and a small multiple of one series is exactly this
 * component with its frame on.
 *
 * ── colour ────────────────────────────────────────────────────────────────
 *
 * One series, so it takes `--series-1` — the first slot of the fixed
 * categorical order, never a cycled hue, and colour follows the entity (this
 * line *is* the metric) rather than its rank. A single series needs no legend
 * box (the title names it), which is why `framed` draws a title in the caller's
 * markup and no swatch here. `--series-1` is `#2a78d6` light / `#3987e5` dark,
 * both validated in `scripts/validate_palette.js`; the same token LoadBars and
 * HealthBar use, so one blue exists in the app, not one per chart.
 *
 * ── marks (marks-and-anatomy) ─────────────────────────────────────────────
 *
 *   - 2px stroke, round linejoin and linecap (the line spec)
 *   - no area fill: this is a line, not an area, and a 10% fill would make the
 *     stat-tile sparkline and the framed chart disagree about their form
 *   - the current value gets a marker — 8px framed (the ≥8px floor), 5px in a
 *     table cell, with the 2px surface ring so it separates from the line
 *   - gridlines hairline and recessive (`--gridline`), the baseline stronger
 *     (`--baseline`); no axis box, no tick marks
 *   - labels are selective: two y labels and up to six x labels, never a value
 *     at every point. Text is ink (`--text-secondary`), never the series colour
 *
 * ── library (R10.2b) ──────────────────────────────────────────────────────
 *
 * R10.2(b) names the default as "`@visx` over `d3-scale` (no Chart.js/Recharts
 * wrapper)" and notes that the skill's own `components.md` prescribes plain
 * HTML/SVG. `HealthBar.tsx` set the house form for that: `scaleLinear` from
 * `d3-scale` for the scales, hand-written `<path>` for the marks, because
 * visx's shape wrappers are `<path>`/`<rect>` pass-throughs that cannot express
 * the mark spec's asymmetric radius (HealthBar's `dataEndPath` exists for
 * exactly that reason). This file follows the same form — `d3-scale` for both
 * scales, the path built here — so the app has one way to draw a chart, not two.
 */

import { useMemo, useState } from "react";
import type { ReactElement } from "react";
import { scaleLinear } from "d3-scale";

/** x-axis label row, framed only. */
const X_AXIS_ROW = 14;
/** y-axis label gutter, framed only. The plot starts after it so a wide y
 *  label never overlaps the line. */
const Y_GUTTER = 46;
/** Max x tick labels on a framed sparkline. More than this and the labels
 *  collide at any reasonable width; fewer and the axis stops being readable. */
const MAX_X_TICKS = 6;

/** Two decimals, which is the precision a viewBox needs and no more — a path
 *  string with 17-digit floats is unreadable and costs bytes per point. */
function n(v: number): string {
  return String(Math.round(v * 100) / 100);
}

/** The y domain: the caller's if given, else the values' own min/max.
 *
 *  A flat series (`min === max`) widens to `[v, v + 1]`. Without that the
 *  domain has zero width, `scaleLinear` puts every point at the top of the
 *  plot, and a query that took 42.7 ms every minute renders as a line pinned to
 *  the ceiling with a y label of `42.70 s` under it — a chart that lies about
 *  its own axis. */
function domainOf(pts: readonly number[], override?: readonly [number, number]): [number, number] {
  if (override !== undefined) {
    const [lo, hi] = override;
    return lo === hi ? [lo, lo + 1] : [lo, hi];
  }
  let lo = Infinity;
  let hi = -Infinity;
  for (const v of pts) {
    if (v < lo) lo = v;
    if (v > hi) hi = v;
  }
  return lo === hi ? [lo, lo + 1] : [lo, hi];
}

/** The `d` for a single-series polyline over `values`, oldest first.
 *
 *  Exported and pure so the geometry is assertable without a DOM: the number of
 *  vertices, the domain widening, and the coordinate mapping are all this
 *  function returns, and a wrong scale shows up here rather than in a
 *  screenshot. Non-finite values are dropped (a `NaN` in a path string
 *  invalidates the whole path, not just one vertex).
 *
 *  One point yields `M x,y` with no segment — an SVG moveto draws nothing, so a
 *  one-point series renders as its end marker alone. That is the honest picture
 *  of one minute of data; a line through one point would be a guess. */
export function linePath(
  values: readonly number[],
  width: number,
  height: number,
  domain?: readonly [number, number],
): string {
  const pts = values.filter((v) => Number.isFinite(v));
  if (pts.length === 0 || width <= 0 || height <= 0) return "";
  const [lo, hi] = domainOf(pts, domain);
  const x = scaleLinear()
    .domain([0, Math.max(pts.length - 1, 1)])
    .range([0, width]);
  const y = scaleLinear()
    .domain([lo, hi])
    .range([height, 0]);
  return pts.map((v, i) => `${i === 0 ? "M" : "L"}${n(x(i))},${n(y(v))}`).join(" ");
}

export interface SparklineProps {
  /** The series values, oldest first. */
  values: readonly number[];
  /** Total SVG width in CSS px (the viewBox width; the element is `width:100%`). */
  width?: number;
  /** Plot height in CSS px, excluding the framed variant's x-label row. */
  height?: number;
  /** Force the y domain. Small multiples pass the *shared* domain across their
   *  siblings so a viewer can compare shapes; without it every tile autoscales
   *  and four identical series render as four different shapes. */
  domain?: readonly [number, number];
  /** Draw the axes (gridlines, two y labels, up to six x labels) and turn on
   *  the hover layer. Off for a table cell, on for a small multiple. */
  framed?: boolean;
  /** Formats a value for the y labels and the tooltip. Required when `framed`;
   *  a bare sparkline has no labels, so it may be omitted. */
  formatValue?: (v: number) => string;
  /** Index-aligned x labels (R10.21-formatted timestamps). Framed only. */
  xLabels?: readonly string[];
  /** Accessible name for the `<svg role="img">`. */
  label: string;
}

/** A single-series line: the table-cell sparkline and the framed small multiple. */
export function Sparkline({
  values,
  width = 720,
  height = 48,
  domain,
  framed = false,
  formatValue,
  xLabels = [],
  label,
}: SparklineProps): ReactElement | null {
  // Index-aligned pairs, so a hover state can name both the point and its value
  // without a `pts[i]` that TypeScript widens to `number | undefined` (the
  // array is non-empty by the guard below, but the index signature cannot know
  // that, and `as number` in three places reads worse than the pair does).
  const pts = useMemo(() => values.filter((v) => Number.isFinite(v)), [values]);
  const points = useMemo(() => pts.map((v, i) => ({ i, v })), [pts]);
  const [hover, setHover] = useState<number | null>(null);

  const last = pts[pts.length - 1];
  if (last === undefined) return null;

  const [lo, hi] = domainOf(pts, domain);
  const plotW = Math.max(0, width - (framed ? Y_GUTTER : 0));
  const totalH = height + (framed ? X_AXIS_ROW : 0);
  const d = linePath(pts, plotW, height, [lo, hi]);
  const x = scaleLinear()
    .domain([0, Math.max(pts.length - 1, 1)])
    .range([0, plotW]);
  const y = scaleLinear()
    .domain([lo, hi])
    .range([height, 0]);

  // Which indices get an x label: up to MAX_X_TICKS, evenly spaced, always
  // including the first and last. Choosing indices rather than measuring text
  // keeps this deterministic in jsdom, where SVG layout is not implemented.
  const xTickIdx = useMemo(() => {
    if (!framed || pts.length === 0) return [];
    const count = Math.min(MAX_X_TICKS, pts.length);
    const step = (pts.length - 1) / Math.max(count - 1, 1);
    return Array.from({ length: count }, (_, i) => Math.round(i * step));
  }, [framed, pts]);

  const hovered = hover === null ? undefined : points[hover];

  return (
    <div className={framed ? "chart-frame" : "sparkline-cell"}>
      <svg
        className="sparkline"
        viewBox={`0 0 ${width} ${totalH}`}
        width="100%"
        height={totalH}
        role="img"
        aria-label={label}
      >
        <g transform={framed ? `translate(${Y_GUTTER} 0)` : undefined}>
          {framed && (
            <g className="spark-grid-group">
              {[lo, (lo + hi) / 2, hi].map((v) => (
                <line
                  key={`grid-${n(v)}`}
                  className={v === lo ? "spark-baseline" : "spark-grid"}
                  x1={0}
                  x2={plotW}
                  y1={n(y(v))}
                  y2={n(y(v))}
                />
              ))}
            </g>
          )}

          {d !== "" && <path className="spark-line" d={d} />}

          {/* The current value, ringed so it separates from the line it sits on
              (marks-and-anatomy: 2px surface ring on overlapping marks). */}
          <circle
            className="spark-marker"
            cx={n(x(pts.length - 1))}
            cy={n(y(last))}
            r={framed ? 4 : 2.5}
          />

          {/* Hover layer (interaction.md: a line gets a crosshair + one tooltip
              listing every series at that x — here, one series). The hit targets
              are full-height rects, wider than the 2px line, because a 2px
              stroke is not a hit target a pointer can find. */}
          {framed &&
            pts.map((_, i) => (
              <rect
                key={`hit-${i}`}
                className="spark-hit"
                x={n(Math.max(0, x(i) - plotW / Math.max(pts.length - 1, 1) / 2))}
                y={0}
                width={n(plotW / Math.max(pts.length - 1, 1))}
                height={height}
                onMouseEnter={() => setHover(i)}
                onMouseLeave={() => setHover((h) => (h === i ? null : h))}
              />
            ))}

          {framed && hovered !== undefined && (
            <line
              className="spark-crosshair"
              x1={n(x(hovered.i))}
              x2={n(x(hovered.i))}
              y1={0}
              y2={height}
            />
          )}
        </g>

        {framed && (
          <g className="spark-y-labels">
            {[lo, (lo + hi) / 2, hi].map((v) => (
              <text key={`y-${n(v)}`} className="spark-axis-label" x={Y_GUTTER - 6} y={n(y(v) + 3)} textAnchor="end">
                {formatValue === undefined ? n(v) : formatValue(v)}
              </text>
            ))}
          </g>
        )}

        {framed && (
          <g className="spark-x-labels">
            {xTickIdx.map((i) => (
              <text
                key={`x-${i}`}
                className="spark-axis-label"
                x={n(Y_GUTTER + x(i))}
                y={height + X_AXIS_ROW - 3}
                textAnchor={i === 0 ? "start" : i === pts.length - 1 ? "end" : "middle"}
              >
                {xLabels[i] ?? ""}
              </text>
            ))}
          </g>
        )}
      </svg>

      {framed && hovered !== undefined && (
        <div
          className="viz-tooltip"
          role="tooltip"
          style={{
            left: `${((Y_GUTTER + x(hovered.i)) / width) * 100}%`,
            top: `${y(hovered.v) + 10}px`,
          }}
        >
          {/* values lead, labels follow (interaction.md) */}
          <div className="tt-value">{formatValue === undefined ? n(hovered.v) : formatValue(hovered.v)}</div>
          <div className="tt-label">{xLabels[hovered.i] ?? ""}</div>
        </div>
      )}
    </div>
  );
}
