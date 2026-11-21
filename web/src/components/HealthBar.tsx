/* The fleet health distribution — one horizontal stacked bar (R10.4).
 *
 * Form: the data's job is magnitude across four fixed categories, so it is a
 * single stacked bar, not a pie and not four bars. One axis (count), one
 * measure — never a second axis for a percentage (the percentage is derived
 * and lives in the table twin).
 *
 * Colour: the status palette, not the categorical one. The hue here *means*
 * good/warning/critical, and the skill reserves status colours for state and
 * forbids a series colour impersonating it — the reverse also holds, a
 * state-coded series wears the status token.
 *
 * Marks (marks-and-anatomy):
 *   - 20px bar thickness (the ≤24px ceiling for a single stacked bar)
 *   - 4px rounded data-end on the right, square where it meets the baseline
 *   - 2px surface gap between every pair of adjacent fills
 *   - selective direct labels, measure-first: a count is drawn above its
 *     segment only when it actually fits inside that segment's width
 *   - text in ink tokens only; the fill carries identity, never the text
 *   - legend always (4 series), and the legend *is* the chart's table twin
 *   - hover + keyboard-focus tooltip per segment
 */

import { useMemo, useState } from "react";
import { scaleLinear } from "d3-scale";

import { HEALTH_ORDER, presentationFor } from "../api/health";
import type { ClusterHealth, FleetRow } from "../api/types";

export interface HealthBarProps {
  clusters: readonly FleetRow[];
  /** CSS pixels. The card is fluid; the SVG scales with a viewBox. */
  width?: number;
}

interface Segment {
  health: ClusterHealth;
  count: number;
  /** Left and right edges in bar-local px, after the surface gaps. */
  x0: number;
  x1: number;
  token: string;
  label: string;
}

const BAR_HEIGHT = 20;
const LABEL_ROW = 16;
const GAP = 2; // the 2px surface gap between adjacent fills
const END_RADIUS = 4; // rounded data-end
const LABEL_FONT = 11;

/** Measure a label's rendered width.
 *
 * The skill's rule is measure-first, never assume: in a browser this asks the
 * renderer for the real advance width. Outside one (jsdom, where SVG layout is
 * not implemented) it falls back to a per-glyph estimate, which is a
 * measurement of the same string, not a guess at the layout.
 */
export function measureText(text: string, fontSize = LABEL_FONT): number {
  if (typeof document !== "undefined") {
    const probe = document.querySelector<SVGTextElement>("svg[data-measure-probe] text");
    if (probe !== null && typeof probe.getComputedTextLength === "function") {
      const len = probe.getComputedTextLength();
      if (len > 0) return len;
    }
    const canvas = document.createElement("canvas").getContext("2d");
    if (canvas !== null) {
      canvas.font = `${fontSize}px system-ui, -apple-system, sans-serif`;
      const w = canvas.measureText(text).width;
      if (typeof w === "number" && w > 0) return w;
    }
  }
  return text.length * fontSize * 0.55;
}

/** A rect with square left corners and `r`-rounded right corners: the bar's
 *  overall shape, so the rounded end lands on the data end and the baseline
 *  stays square (marks-and-anatomy). */
function dataEndPath(w: number, h: number, r: number): string {
  const radius = Math.max(0, Math.min(r, w / 2, h / 2));
  if (radius === 0) return `M 0 0 H ${w} V ${h} H 0 Z`;
  return [
    `M 0 0`,
    `H ${w - radius}`,
    `A ${radius} ${radius} 0 0 1 ${w} ${radius}`,
    `V ${h - radius}`,
    `A ${radius} ${radius} 0 0 1 ${w - radius} ${h}`,
    `H 0`,
    "Z",
  ].join(" ");
}

export function countsByHealth(clusters: readonly FleetRow[]): Record<ClusterHealth, number> {
  const counts: Record<ClusterHealth, number> = { healthy: 0, degraded: 0, down: 0, unknown: 0 };
  for (const c of clusters) counts[c.health] += 1;
  return counts;
}

interface Hover {
  health: ClusterHealth;
  /** x in bar-local px, for tooltip placement. */
  x: number;
  /** The segment's value, so the tooltip renders from the hovered mark. */
  count: number;
}

export function HealthBar({ clusters, width = 720 }: HealthBarProps) {
  const [hover, setHover] = useState<Hover | null>(null);

  const { segments, total } = useMemo(() => {
    const counts = countsByHealth(clusters);
    const sum = HEALTH_ORDER.reduce((n, h) => n + counts[h], 0);
    const scale = scaleLinear().domain([0, Math.max(sum, 1)]).range([0, width]);
    const present = HEALTH_ORDER.filter((h) => counts[h] > 0);
    const segs: Segment[] = [];
    let cum = 0;
    present.forEach((h, i) => {
      const count = counts[h];
      const rawStart = scale(cum);
      const rawEnd = scale(cum + count);
      cum += count;
      // Shrink each side by half the gap so adjacent fills keep 2px of surface
      // between them; the outer edges (baseline, data end) keep their shape
      // because the clip path draws them.
      const first = i === 0;
      const last = i === present.length - 1;
      segs.push({
        health: h,
        count,
        x0: first ? rawStart : rawStart + GAP / 2,
        x1: last ? rawEnd : rawEnd - GAP / 2,
        token: presentationFor(h).token,
        label: presentationFor(h).label,
      });
    });
    return { segments: segs, total: sum };
  }, [clusters, width]);

  if (total === 0) return null;

  const height = BAR_HEIGHT + LABEL_ROW;
  const clipId = "healthbar-data-end";

  return (
    <figure className="chart" data-chart="fleet-health">
      <div className="chart-head">
        <h2 className="chart-title">Health distribution</h2>
        <p className="chart-sub">
          {total} {total === 1 ? "cluster" : "clusters"} · judged by R1.2's rollup
        </p>
      </div>

      <svg
        className="healthbar"
        viewBox={`0 0 ${width} ${height}`}
        width="100%"
        height={height}
        role="img"
        aria-label={describeDistribution(clusters)}
        style={{ overflow: "visible", display: "block" }}
      >
        <defs>
          <clipPath id={clipId}>
            <path d={dataEndPath(width, BAR_HEIGHT, END_RADIUS)} />
          </clipPath>
        </defs>

        {/* The baseline the bar is anchored to (marks-and-anatomy: a bar needs
            a visible baseline, drawn in the baseline token, not a gridline). */}
        <line
          x1={0}
          x2={0}
          y1={LABEL_ROW}
          y2={LABEL_ROW + BAR_HEIGHT}
          stroke="var(--baseline)"
          strokeWidth={1}
        />

        <g transform={`translate(0 ${LABEL_ROW})`} clipPath={`url(#${clipId})`}>
          {segments.map((s) => (
            <rect
              key={s.health}
              x={s.x0}
              y={0}
              width={Math.max(0, s.x1 - s.x0)}
              height={BAR_HEIGHT}
              fill={s.token}
              opacity={hover === null || hover.health === s.health ? 1 : 0.55}
            />
          ))}
        </g>

        {/* Hit targets: full-height rects wider than the visible marks, so a
            pointer near a segment still catches it (interaction.md). */}
        <g transform={`translate(0 ${LABEL_ROW})`}>
          {segments.map((s) => (
            <rect
              key={`hit-${s.health}`}
              className="hit-target"
              x={s.x0}
              y={-LABEL_ROW}
              width={Math.max(0, s.x1 - s.x0)}
              height={height}
              fill="transparent"
              tabIndex={0}
              role="button"
              aria-label={`${s.label}: ${s.count} of ${total}`}
              onMouseEnter={() => setHover({ health: s.health, x: (s.x0 + s.x1) / 2, count: s.count })}
              onMouseLeave={() => setHover(null)}
              onFocus={() => setHover({ health: s.health, x: (s.x0 + s.x1) / 2, count: s.count })}
              onBlur={() => setHover(null)}
            />
          ))}
        </g>

        {/* Selective direct labels: ink, above the bar, and only when the text
            fits inside the segment's own width — so labels cannot collide. */}
        <g transform={`translate(0 ${LABEL_ROW})`}>
          {segments.map((s) => {
            const text = String(s.count);
            const w = measureText(text);
            if (s.x1 - s.x0 < w + 6) return null;
            return (
              <text
                key={`label-${s.health}`}
                x={(s.x0 + s.x1) / 2}
                y={-4}
                textAnchor="middle"
                className="mark-label"
                fill="var(--text-secondary)"
                fontSize={LABEL_FONT}
              >
                {text}
              </text>
            );
          })}
        </g>
      </svg>

      {hover !== null && (
        <div
          className="viz-tooltip"
          role="tooltip"
          style={{ left: `${(hover.x / width) * 100}%`, top: `${LABEL_ROW + BAR_HEIGHT + 6}px` }}
        >
          <div className="tt-value">{String(hover.count)}</div>
          <div className="tt-label">{presentationFor(hover.health).label}</div>
        </div>
      )}

      <HealthLegend clusters={clusters} total={total} />
    </figure>
  );
}

/** The chart's table twin (R10.23): the same four numbers the bar draws, in
 *  tabular form, so the chart is never the only way to read it. */
function HealthLegend({ clusters, total }: { clusters: readonly FleetRow[]; total: number }) {
  const counts = countsByHealth(clusters);
  return (
    <table className="legend-table">
      <caption className="sr-only">Health distribution, as a table</caption>
      <thead>
        <tr>
          <th scope="col">Health</th>
          <th scope="col" className="num">
            Clusters
          </th>
          <th scope="col" className="num">
            Share
          </th>
        </tr>
      </thead>
      <tbody>
        {HEALTH_ORDER.map((h) => (
          <tr key={h}>
            <th scope="row">
              <span className="legend-key">
                <span className="swatch" style={{ background: presentationFor(h).token }} aria-hidden />
                {presentationFor(h).label}
              </span>
            </th>
            <td className="num">{counts[h]}</td>
            <td className="num">{total === 0 ? "—" : `${Math.round((counts[h] / total) * 100)}%`}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** Screen-reader summary of the bar, so the chart has a text equivalent. */
export function describeDistribution(clusters: readonly FleetRow[]): string {
  const counts = countsByHealth(clusters);
  const parts = HEALTH_ORDER.filter((h) => counts[h] > 0).map((h) => `${counts[h]} ${presentationFor(h).label.toLowerCase()}`);
  const total = HEALTH_ORDER.reduce((n, h) => n + counts[h], 0);
  return `Health distribution across ${total} clusters: ${parts.join(", ")}.`;
}
