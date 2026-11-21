/* The non-colour half of R10.23.
 *
 * R10.23 requires state to be encoded by more than hue, and the light palette
 * makes that mandatory rather than stylistic: `--status-warning` (#fab219) and
 * `--status-serious` (#ec835a) both sit below 3:1 on the light surface, so a
 * colour-only "degraded" dot is genuinely hard to read. The glyph shape plus
 * the text label are the mitigation; the colour is the reinforcement.
 *
 * Shapes are drawn as inline SVG so they inherit `currentColor` from the status
 * token and scale with the text they sit beside.
 */

import type { ClusterHealth } from "../api/types";
import { presentationFor } from "../api/health";

export interface StatusGlyphProps {
  health: ClusterHealth;
  /** Square size in px. Defaults to 10, which aligns to a 13px label. */
  size?: number;
}

/** The shape path for a glyph, drawn inside a `size × size` box. */
function shape(size: number, glyph: "circle" | "triangle" | "square" | "cross" | "dash"): string {
  const r = size / 2;
  switch (glyph) {
    case "circle":
      return `M ${r} 0 A ${r} ${r} 0 1 1 ${r - 0.001} 0 Z`;
    case "square":
      return `M 0 0 H ${size} V ${size} H 0 Z`;
    case "triangle":
      return `M ${r} 0 L ${size} ${size} L 0 ${size} Z`;
    case "cross": {
      // A plus with a 2px-thick stroke drawn as a filled path, so it reads at
      // 10px the way a hollow ✕ does not.
      const t = Math.max(1.6, size * 0.18);
      const a = (size - t) / 2;
      const b = a + t;
      return `M ${a} 0 H ${b} V ${a} H ${size} V ${b} H ${b} V ${size} H ${a} V ${b} H 0 V ${a} H ${a} Z`;
    }
    case "dash":
      return `M 0 ${r - Math.max(1, size * 0.12)} H ${size} V ${r + Math.max(1, size * 0.12)} H 0 Z`;
  }
}

export function StatusGlyph({ health, size = 10 }: StatusGlyphProps) {
  const p = presentationFor(health);
  return (
    <svg
      className="status-glyph"
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      role="img"
      aria-label={p.label}
      data-health={health}
      style={{ color: p.token, flex: "0 0 auto" }}
    >
      <path d={shape(size, p.glyph)} fill="currentColor" />
    </svg>
  );
}

/** The chip: glyph + label, the pairing R10.23 asks for. Use this wherever a
 *  health value appears in text; use the bare glyph only inside the chart. */
export function StatusChip({ health, size = 10 }: StatusGlyphProps) {
  const p = presentationFor(health);
  return (
    <span className="status-chip" data-health={health}>
      <StatusGlyph health={health} size={size} />
      <span>{p.label}</span>
    </span>
  );
}
