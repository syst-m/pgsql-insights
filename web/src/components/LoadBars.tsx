/* R10.6 — the inline load bars, and the three states that are not bars.
 *
 * requirements.md:416 (R10.6) names them outright: the Queries page is "the R3.2
 * table with sparklines per row …, **inline load bars**, filter chips …". So the
 * form is named by the requirement and not chosen here — which settles the
 * dataviz `choosing-a-form` question ("a single value wants a stat tile, not a
 * one-bar chart"): inside a table cell the requirement's answer is a meter, and
 * a meter is the form for a value with a known whole.
 *
 * ── Which columns are bars, from requirements.md:306 (R3.2) ──────────────
 *
 *   Load by total time   bar    per-query sourced
 *   % load by Queries    bar    per-query sourced
 *   Load by CPU          bar    derived (total_exec_time minus block I/O time)
 *   Load by IO wait      bar    derived (blk_read_time/blk_write_time ≤PG16,
 *                                shared_blk_read_time/shared_blk_write_time PG17+)
 *   Load by lock wait    —      **no per-query source**; renders the literal
 *                                `n/a (no per-query source, §5)`
 *   Analyze latency      —      deferred, marked **Gap** (Cloud SQL sources it
 *                                from auto_explain)
 *
 * The lock-wait cell is the part worth a comment. requirements.md:306 says the
 * column "has **no per-query source** (`pg_stat_statements` has no lock-time
 * column — §5 divergence table) and renders `n/a (no per-query source, §5)`",
 * and requirements.md:669 repeats it as 6b's acceptance test ("Queries page
 * renders the R3.2 column set exactly (incl. `Load by lock wait` = `n/a`)").
 * requirements.md:834-837 records the ruling: A4 argued all three load columns
 * were unavailable per query, A3 showed IO wait *is* derivable, so only lock
 * wait is missing. The coordinator's ruling on my proposal is that the field is
 * absent from the DTO and the UI supplies the literal — which is the same shape
 * as `RowExtras` in api/envelope.ts, a presentation value the API doesn't carry.
 * `LOCK_WAIT_NA` below is that single source so the string can't drift.
 *
 * It must not be rendered as `—`. `—` is R10.4's marker for "source has not run
 * yet" (requirements.md:414); this source will never run. The two states mean
 * opposite things and share a glyph only in a careless implementation.
 *
 * ── R10.19 interaction contract, for this chart type ──────────────────────
 *
 * R10.20's sibling R10.19 (requirements.md:429) requires the contract "stated
 * per chart type rather than left to the library default":
 *   - tooltip: the column name and the exact value, e.g. `Load by IO wait —
 *     842 ms`; a derived column also names its source, because "derived" is the
 *     information a reader needs to trust the number.
 *   - legend: none. One measure per cell is one series, and marks-and-anatomy
 *     is explicit that a single series needs no legend box.
 *   - click-through: the bar is not a target — the *row* is, and it opens Query
 *     Detail (R10.7). A bar that is its own hit target inside a row that is also
 *     one gives the row two behaviours where it needs one.
 *   - crosshair: n/a, single-value meter.
 *
 * ── colour, and why it is not validated separately ────────────────────────
 *
 * One measure per cell is one series, so it takes `--series-1` by the fixed
 * categorical order (the skill's rule: assign in fixed order, never cycle;
 * colour follows the entity, not its rank). Giving CPU and IO wait different
 * hues would use categorical colour to label *columns*, which implies a legend
 * the table doesn't have. `--series-1` is `#2a78d6` (light) / `#3987e5` (dark);
 * both were run through `scripts/validate_palette.js` — the light 8-slot run
 * passes all five checks with the contrast WARN on slots 3/4/5 (`#1baf7a`,
 * `#eda100`, `#e87ba4`), which does not include slot 1, and the 2-series pair
 * `#2a78d6,#eb6834` passes with every contrast ≥3:1. The track is
 * `color-mix(… var(--series-1) 12% …)` — the meter rule that the unfilled track
 * is a lighter step of the same ramp — and the value text is `--text-secondary`,
 * never the series colour, per marks-and-anatomy.
 */

import { NULL_VALUE } from "../lib/format";

/** R3.2's literal, verbatim from requirements.md:306. Exported so the table,
 *  the tests and the acceptance cell all agree on one spelling. */
export const LOCK_WAIT_NA = "n/a (no per-query source, §5)";

/** R3.2's deferred `Analyze latency`, marked **Gap** in the same row. */
export const ANALYZE_LATENCY_GAP = "deferred (Gap — auto_explain, §5)";

export interface LoadBarProps {
  /** The value as a share of the whole it is measured against, 0…1. Clamped. */
  fraction: number;
  /** The already-formatted value to print beside the bar (`formatDurationMs`,
   *  `formatPercent`, …). The bar never carries the number: text wears text
   *  tokens, and a bar with a number drawn on it is two ink layers for one fact. */
  text: string;
  /** Tooltip text; defaults to `text`. Derived columns should pass the source. */
  title?: string;
}

/** A single-series meter in a table cell.
 *
 *  Mark spec (dataviz `marks-and-anatomy`): 8px thick, well under the 24px
 *  ceiling; 4px rounded on the data end and square at the baseline — for a
 *  right-growing bar that is `border-radius: 0 var(--radius-mark) …`, which is
 *  the whole reason `--radius-mark` exists as a token. No stroke around the
 *  fill: the gap and the ring are the separation mechanism, and a border "adds
 *  data-weight ink that isn't data".
 */
export function LoadBar({ fraction, text, title }: LoadBarProps) {
  const pct = Math.max(0, Math.min(1, Number.isFinite(fraction) ? fraction : 0)) * 100;
  return (
    <div className="load-cell" title={title ?? text}>
      <span className="load-value">{text}</span>
      <span className="load-track" aria-hidden="true">
        <span className="load-fill" style={{ width: `${pct}%` }} />
      </span>
    </div>
  );
}

/** The R3.2 cell that has no bar and no number: `n/a (no per-query source, §5)`
 *  or the deferred-Gap marker. Muted text, no track — a track with no fill says
 *  "measured, and it was zero", which is a different claim. */
export function LoadNotApplicable({ text }: { text: string }) {
  return (
    <span className="load-na secondary" data-testid="load-na">
      {text}
    </span>
  );
}

/** R10.4's source-pending cell: a column whose source phase has not run yet
 *  renders `—` with a marker naming the pending source, and the requirement is
 *  *re-validated*, not first validated, when the source lands.
 *
 *  Distinct from `LoadNotApplicable` on purpose: `—` means "not yet", the `n/a`
 *  literal means "not ever". R10.4 (requirements.md:414) defines the marker for
 *  the Fleet columns; this is the same marker, not a second one. */
export function LoadSourcePending({ source }: { source: string }) {
  return (
    <span className="load-pending" data-testid="load-pending" title={`Source pending: ${source}`}>
      <span className="muted">{NULL_VALUE}</span>
      <span className="pending-marker">{source}</span>
    </span>
  );
}

/** One call per R3.2 load cell, so the table never re-derives which of the
 *  three states a column is in. */
export type LoadCellProps =
  | { kind: "bar"; fraction: number; text: string; title?: string }
  | { kind: "not-applicable"; text: string }
  | { kind: "source-pending"; source: string }
  | { kind: "none" };

export function LoadCell(props: LoadCellProps) {
  switch (props.kind) {
    case "bar":
      return <LoadBar fraction={props.fraction} text={props.text} title={props.title} />;
    case "not-applicable":
      return <LoadNotApplicable text={props.text} />;
    case "source-pending":
      return <LoadSourcePending source={props.source} />;
    case "none":
      return <span className="muted">{NULL_VALUE}</span>;
  }
}
