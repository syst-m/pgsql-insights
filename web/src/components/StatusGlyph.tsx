/* The non-colour half of R10.23, in the general form the requirement asks for.
 *
 * R10.23 requires state to be encoded by more than hue, and the light palette
 * makes that mandatory rather than stylistic: `--status-warning` (#fab219) and
 * `--status-serious` (#ec835a) both sit below 3:1 on the light surface, so a
 * colour-only "degraded" dot is genuinely hard to read. The glyph shape plus
 * the text label are the mitigation; the colour is the reinforcement.
 *
 * Shapes are drawn as inline SVG so they inherit `currentColor` from the status
 * token and scale with the text they sit beside.
 *
 * R10.23\'s subject is "every state", not "every health", and IMPLEMENTATION.md
 * :1140 names this file as 1b\'s interim relief whose generalisation is the
 * open half of the requirement. So the drawing lives in `Glyph`, which takes a
 * presentation: `StatusGlyph` and `StatusChip` stay the health-typed front doors
 * `ClusterTable.tsx:40` and `ClusterDetail.tsx:66` already render through, and
 * `StatePanel.tsx`\'s `PANEL_GLYPHS` is the second vocabulary drawn through the
 * same primitive, and `SESSION_GLYPHS` at the bottom of this file is the third:
 * the hourglass and block icons R4.1 (`requirements.md:112`) names for the
 * session list's `Status` column.
 */

import { presentationFor, type HealthPresentation } from "../api/health";
import type { ClusterHealth } from "../api/types";

/** The shape vocabulary, imported from `api/health.ts:54` rather than restated
 *  here so the two cannot drift, widened by `ring`: no health draws a ring, and
 *  six panel states need six distinct shapes to keep the set property the test
 *  asserts. A health-typed call can never ask for one. Widened again by
 *  `hourglass` and `block` — R4.1's two session icons — which are `ring`\'s
 *  case exactly: no health draws them, so a health-typed call still cannot ask
 *  for one, and the session block below is the only caller that can. */
export type GlyphShape = HealthPresentation["glyph"] | "ring" | "hourglass" | "block";

/** The three channels R10.23 names for one state: the word, the colour, the
 *  shape. `api/health.ts`\'s `HealthPresentation` is this same triple narrowed
 *  to health, which is why `StatusGlyph` can hand one to `Glyph` unchanged. */
export interface GlyphPresentation {
  label: string;
  /** The colour token, as a `var(--…)` reference. Never a hex literal, and
   *  never a series token: status colour is reserved for state. */
  token: string;
  glyph: GlyphShape;
}

/** The shape path for a glyph, drawn inside a `size × size` box. */
function shape(size: number, glyph: GlyphShape): string {
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
    case "ring": {
      // A hollow circle drawn as a filled annulus: the outer circle wound
      // clockwise, the inner one counter-clockwise, so the default nonzero fill
      // rule leaves the hole. Same thickness rule as the cross, so a ring and a
      // cross at one size carry the same ink weight.
      const t = Math.max(1.6, size * 0.18);
      const ri = Math.max(0.01, r - t);
      return (
        `M ${r} 0 A ${r} ${r} 0 1 1 ${r} ${size} A ${r} ${r} 0 1 1 ${r} 0 Z ` +
        `M ${r} ${r - ri} A ${ri} ${ri} 0 1 0 ${r} ${r + ri} A ${ri} ${ri} 0 1 0 ${r} ${r - ri} Z`
      );
    }
    case "hourglass":
      // R4.1's running-query icon. Two triangles sharing their apex at the
      // centre rather than one waisted outline: at 10px a real hourglass waist
      // is a pixel wide and antialiasing smears it, while two solid triangles
      // keep the ⧖ silhouette at any size. Same ink weight as `triangle` —
      // both are solid fills of the box, so a row of them sets evenly.
      return `M 0 0 H ${size} L ${r} ${r} Z M ${r} ${r} L ${size} ${size} H 0 Z`;
    case "block": {
      // R4.1's blocked-session icon: the 🚫 mark. The annulus is the ring\'s,
      // built from the same thickness rule so a block and a ring at one size
      // carry the same ink, and the bar is wound clockwise like the outer
      // circle — the nonzero fill rule then fills the bar back in where it
      // crosses the counter-clockwise hole, which is the whole mark.
      const t = Math.max(1.6, size * 0.18);
      const ri = Math.max(0.01, r - t);
      return (
        `M ${r} 0 A ${r} ${r} 0 1 1 ${r} ${size} A ${r} ${r} 0 1 1 ${r} 0 Z ` +
        `M ${r} ${r - ri} A ${ri} ${ri} 0 1 0 ${r} ${r + ri} A ${ri} ${ri} 0 1 0 ${r} ${r - ri} Z ` +
        `M 0 0 L ${t} 0 L ${size} ${size} L ${size} ${size - t} Z`
      );
    }
  }
}

export interface GlyphProps {
  /** The presentation to draw: label, token and shape together. */
  p: GlyphPresentation;
  /** Square size in px. Defaults to 10, which aligns to a 13px label. */
  size?: number;
  /** The svg\'s class. Every call site uses the default; it is a prop so a
   *  caller with a more specific selector can pass one without restyling. */
  className?: string;
  /** Extra attributes spread onto the `<svg>`, keyed by the attribute name as
   *  it should appear: `{ "data-health": "down" }`, `{ "data-state": "error" }`.
   *  One key per glyph, because a glyph belongs to exactly one vocabulary. */
  attrs?: Record<string, string>;
}

/** Draw a presentation. This is the primitive R10.23 generalises to: anything
 *  with a label, a token and a shape renders through it, not only a health. */
export function Glyph({ p, size = 10, className = "status-glyph", attrs }: GlyphProps) {
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      role="img"
      aria-label={p.label}
      {...attrs}
      style={{ color: p.token, flex: "0 0 auto" }}
    >
      <path d={shape(size, p.glyph)} fill="currentColor" />
    </svg>
  );
}

export interface StatusGlyphProps {
  health: ClusterHealth;
  /** Square size in px. Defaults to 10, which aligns to a 13px label. */
  size?: number;
}

export function StatusGlyph({ health, size = 10 }: StatusGlyphProps) {
  return <Glyph p={presentationFor(health)} size={size} attrs={{ "data-health": health }} />;
}

/** The chip: glyph + label, the pairing R10.23 asks for. Use this wherever a
 *  health value appears in text; use the bare glyph only inside the chart. */
export function StatusChip({ health, size = 10 }: StatusGlyphProps) {
  const p = presentationFor(health);
  return (
    <span className="status-chip" data-health={health}>
      <Glyph p={p} size={size} attrs={{ "data-health": health }} />
      <span>{p.label}</span>
    </span>
  );
}

/* --------------------------------------------------------------------------
 * The third vocabulary: R4.1's session states.
 *
 * `requirements.md:112` names exactly two icons for the session list — the
 * hourglass for a running query and the block for a blocked one — so the set
 * here is those two plus a default arm, not a per-status design. Everything
 * else the API\'s `status` string can carry (`idle`, `idle in transaction`,
 * `idle in session`, `fast function call`, R4.6\'s `starting`, or a value from
 * a future PG) falls to that arm, which borrows `dash` for the same reason
 * `unknown` borrows it: it is not a state the spec draws, and inventing a third
 * icon would be inventing a spec. `web/src/api/types.ts:813` types `status` as
 * a plain `string`, not a union, which is why the mapping is a function with a
 * default rather than a `Record` the compiler checks.
 * -------------------------------------------------------------------------- */

/** The three states the two named icons plus the default arm can draw. */
export type SessionState = "active" | "blocked" | "idle";

export const SESSION_GLYPHS: Record<SessionState, GlyphPresentation> = {
  active: { label: "Active", token: "var(--status-good)", glyph: "hourglass" },
  blocked: { label: "Blocked", token: "var(--status-warning)", glyph: "block" },
  // The default arm. `--text-muted` for the reason health\'s `unknown` uses it:
  // not a status colour, because the spec draws no icon here and a coloured one
  // would claim a state the requirement does not have.
  idle: { label: "Idle", token: "var(--text-muted)", glyph: "dash" },
};

/** Map the API\'s free-text status to one of the three.
 *
 *  `blocked` wins over `active`: a session the payload reports as blocked by
 *  another pid is blocked *while* running, and R4.1 gives the row one icon, so
 *  the block — the informative half, and the reason R4.4\'s chain exists — is
 *  the one drawn. */
export function sessionStateFor(status: string, blocked = false): SessionState {
  if (blocked) return "blocked";
  if (status.trim().toLowerCase() === "active") return "active";
  return "idle";
}

/** The presentation for a session row, by status string. */
export function sessionPresentationFor(status: string, blocked = false): GlyphPresentation {
  return SESSION_GLYPHS[sessionStateFor(status, blocked)];
}

export interface SessionStatusProps {
  /** The `SessionView.status` string as the API sends it. */
  status: string;
  /** R4.4\'s edge list for this row (`SessionView.blockingPids`): a non-empty
   *  list is what makes a session blocked rather than merely running. */
  blocked?: boolean;
  /** Square size in px. Defaults to 10, which aligns to a 13px label. */
  size?: number;
}

/** The bare glyph for a session row — R4.1\'s hourglass or block. */
export function SessionStatusGlyph({ status, blocked = false, size = 10 }: SessionStatusProps) {
  return (
    <Glyph
      p={sessionPresentationFor(status, blocked)}
      size={size}
      attrs={{ "data-session-state": sessionStateFor(status, blocked) }}
    />
  );
}

/** The paired form for a cell that shows the icon beside the status text. The
 *  visible text is the API\'s own string, not the table\'s label: R4.1\'s
 *  `Status` column is the server\'s status verbatim, and "Idle" is not what a
 *  row reading `idle in transaction` should say. */
export function SessionStatusChip({ status, blocked = false, size = 10 }: SessionStatusProps) {
  const state = sessionStateFor(status, blocked);
  return (
    <span className="status-chip" data-session-state={state}>
      <Glyph p={SESSION_GLYPHS[state]} size={size} attrs={{ "data-session-state": state }} />
      <span>{status}</span>
    </span>
  );
}
