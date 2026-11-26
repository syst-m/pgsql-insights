/* The Wait Events chart — R10.9's "donut/bar by `wait_event_type` with a drill to
 * per-event counts and the sessions currently in each" (requirements.md:422),
 * drawn as one horizontal bar per bucket.
 *
 * Which of R10.9's two offered forms, and why. The dataviz form heuristic routes a
 * part-to-whole reading to a stacked bar and never a pie, and routes a magnitude
 * comparison across categories to a bar/column with a sequential hue. This data is
 * both at once — each bucket's share of the session total *and* their relative
 * sizes — which is the horizontal-bar case. Horizontal rather than vertical
 * because the bucket names are long (`Lock`, `Client/ClientRead`, `Idle in
 * transaction`) and a horizontal bar renders them as horizontal text instead of
 * rotated. It is also the anatomy this app already ships in
 * `web/src/components/HealthBar.tsx:79` — a bar with a square baseline end and a
 * 4px-rounded data end — so the page does not invent a second chart grammar for
 * the same job; that helper is imported rather than copied a third time.
 *
 * The grouping rules mirror the Go rollup instead of inventing a client-side one:
 * `internal/collector/activity.go:690` `WaitEventRollup(sessions)` groups by
 * `waitKey{Type, Event}` (`internal/collector/activity.go:636`) and sorts count
 * desc → type → event; `web/src/api/types.ts:796` `SessionView` is the payload
 * shape this chart reads, since the coordinator's ruling on the route is that
 * there is no `/insights/wait-events` DTO and the page renders from
 * `ActiveQueriesResponse`. One documented divergence, marked at its site below:
 * the Go rollup *excludes* sessions with no wait event
 * (`internal/collector/activity.go:697-699`), because a session that is not
 * waiting has nothing to roll up. This chart keeps them as their own bucket so the
 * bars' total equals the payload's session count — the number the page header
 * prints — instead of silently falling short of it.
 *
 * Colour: one blue ramp, four steps, assigned by value. The `pg_wait_events` type
 * set is larger than the skill's eight-slot categorical ceiling, and the ceiling
 * is per-chart rather than per-app, so the types are not given categorical hues at
 * all; the skill's answer for one-measure magnitude is a sequential ramp — one hue,
 * light to dark — which needs no slot. The four steps are the subset of the
 * `--blue-*` ramp (`web/src/styles/tokens.css:54`) that passes the skill's
 * validator in ordinal mode on both surfaces:
 *
 *   node validate_palette.js "#86b6ef,#5598e7,#2a78d6,#184f95" --mode light --ordinal
 *   node validate_palette.js "#86b6ef,#5598e7,#2a78d6,#184f95" --mode dark --ordinal
 *
 * Both pass all four ordinal checks (lightness monotone, adjacent ΔL ≥ 0.06,
 * light-end contrast ≥ 2.0, single hue ≤ 40°). The full eight-step ramp fails
 * adjacent ΔL at 0.047, which is why this is a four-step subset and not the whole
 * ramp; the categorical mode's own footer says so — its chroma and CVD checks are
 * scoped to categorical palettes and it defers a ramp to "lightness monotonicity".
 *
 * The value label at each bar tip is in `--text-secondary`, never the bar's own
 * colour (text never wears the data colour), and it is also the relief for the
 * contrast warning the categorical mode raises on the light steps. R10.23's
 * non-colour encoding of *selection* is a `--text-secondary` outline plus
 * `aria-current="true"` on the row, not a fill change, so it survives greyscale
 * printing and every CVD mode.
 */

import { useState, type ReactElement } from "react";
import { scaleLinear } from "d3-scale";

import type { SessionView } from "../api/types";
import { NULL_VALUE } from "../lib/format";
import { dataEndPath, measureText } from "./HealthBar";

/** The three kinds of bucket the payload's sessions can fall into.
 *
 *  `named` is a real `wait_event_type`; `unrecognised` is the R10.9/`R4.3` case
 *  the server flags with `unknownWaitEvent` (`web/src/api/types.ts:810`, whose
 *  docblock names this page as its consumer: the UI must be able to say which of
 *  the backends waiting on a pair is a name the server should not know);
 *  `not-waiting` is a session with an empty pair. The coordinator's ruling is that
 *  the flag is "a third bucket, not a subset of a named type", which is what the
 *  branch in `waitBuckets` implements. */
export type WaitBucketKind = "named" | "unrecognised" | "not-waiting";

/** One event inside a bucket — the client-side twin of
 *  `internal/collector/activity.go:624` `WaitEventCount{Type, Event, Count,
 *  Unknown}`, minus `Unknown` because here the unknown-ness is the *bucket*, not a
 *  per-row flag. R4.3's second tab ("per-event counts") is `events`. */
export interface WaitEventRow {
  event: string;
  count: number;
  sessions: SessionView[];
}

/** One bar: a bucket, its session count, and the two drill payloads. */
export interface WaitBucket {
  /** Stable React key and the id `onSelect` round-trips. A named bucket's key is
   *  its `wait_event_type`; the other two kinds are singletons keyed by kind. */
  key: string;
  /** Human label. Equals `wait_event_type` for a named bucket. */
  label: string;
  kind: WaitBucketKind;
  count: number;
  /** Per-event counts inside the bucket (R4.3's second tab). */
  events: WaitEventRow[];
  /** The sessions currently in the bucket (R10.9's "the sessions currently in
   *  each"), so the drill table needs no second fetch. */
  sessions: SessionView[];
}

/** Group sessions into buckets, mirroring `WaitEventRollup`
 *  (`internal/collector/activity.go:690`): group by the `{type, event}` pair, sort
 *  count desc → name asc, at two levels.
 *
 *  Divergence from the Go function, and the reason: the Go rollup skips the empty
 *  pair (`internal/collector/activity.go:697-699`) because a session that is not
 *  waiting has nothing to roll up. A chart whose bars sum to fewer sessions than
 *  the page's own header prints is the "chart lies about the total" case, so the
 *  empty pair becomes a `not-waiting` bucket here instead of disappearing. */
export function waitBuckets(sessions: readonly SessionView[]): WaitBucket[] {
  const byKey = new Map<string, WaitBucket>();

  for (const s of sessions) {
    // The three-way branch. The flag wins over the pair: a session whose pair the
    // server disowns belongs in `unrecognised` whatever the two strings are, which
    // is the whole point of the flag.
    const kind: WaitBucketKind = s.unknownWaitEvent
      ? "unrecognised"
      : s.waitEventType === "" && s.waitEvent === ""
        ? "not-waiting"
        : "named";

    const key = kind === "named" ? s.waitEventType : kind;
    const label =
      kind === "named" ? s.waitEventType : kind === "unrecognised" ? "Unrecognised" : "Not waiting";

    let bucket = byKey.get(key);
    if (bucket === undefined) {
      bucket = { key, label, kind, count: 0, events: [], sessions: [] };
      byKey.set(key, bucket);
    }
    bucket.count += 1;
    bucket.sessions.push(s);

    // The event row inside the bucket. For an unrecognised pair the pair *is* the
    // identity — `internal/collector/activity.go:636` keys the count by
    // `{Type, Event}` — so the row label is the joined `Lock/transactionid`; for
    // every other bucket the type is already the bucket's own label, so the row
    // label is the bare event name (empty for `not-waiting`, which renders as
    // `NULL_VALUE`).
    const event =
      kind === "unrecognised" && s.waitEventType !== "" && s.waitEvent !== ""
        ? `${s.waitEventType}/${s.waitEvent}`
        : s.waitEvent;

    let row = bucket.events.find((e) => e.event === event);
    if (row === undefined) {
      row = { event, count: 0, sessions: [] };
      bucket.events.push(row);
    }
    row.count += 1;
    row.sessions.push(s);
  }

  const buckets = [...byKey.values()];
  for (const b of buckets) {
    b.events.sort((a, z) => z.count - a.count || compare(a.event, z.event));
  }
  buckets.sort((a, z) => z.count - a.count || compare(a.label, z.label));
  return buckets;
}

/** Ascending, locale-independent — the Go sort compares the raw strings, and a
 *  `localeCompare` would order `IO` and `Idle` differently in en vs. tr. */
function compare(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** One step of the sequential ramp: the token to paint with, and the hex the token
 *  resolves to in *both* modes (`web/src/styles/tokens.css:54` and its dark twin
 *  at `web/src/styles/tokens.css:125` declare identical values for every blue
 *  step), which is what lets one constant serve both themes. The hex is here so
 *  the test can re-run the validator's input off the shipped list instead of a
 *  second copy of the numbers. */
export interface RampStep {
  token: string;
  hex: string;
}

/** The four blue steps, light → dark. See the file header for the validation run. */
export const WAIT_RAMP: readonly RampStep[] = [
  { token: "--blue-250", hex: "#86b6ef" },
  { token: "--blue-350", hex: "#5598e7" },
  { token: "--blue-450", hex: "#2a78d6" },
  { token: "--blue-600", hex: "#184f95" },
];

/** Which ramp step a count gets, as an index into `WAIT_RAMP`.
 *
 *  Tiers are quartiles of `max`, not ranks: two buckets with equal counts get
 *  equal colour (which is what a sequential scale means), and a refetch that
 *  reorders the buckets cannot repaint a bar whose own value did not change. */
export function rampTier(count: number, max: number): number {
  if (max <= 0) return 0;
  const r = count / max;
  if (r > 0.75) return 3;
  if (r > 0.5) return 2;
  if (r > 0.25) return 1;
  return 0;
}

export function rampStep(count: number, max: number): RampStep {
  const tier = rampTier(count, max);
  const step = WAIT_RAMP[tier];
  // `noUncheckedIndexedAccess` (web/tsconfig.json:9) types an in-range index as
  // `RampStep | undefined`. `rampTier` returns 0..3 and the array has four
  // entries, so this branch is the type system's, not a runtime case — and the
  // message names the invariant so a future edit that adds a tier without adding
  // a step fails loudly instead of painting nothing.
  if (step === undefined) throw new Error(`WaitEventsChart: no ramp step for tier ${tier}`);
  return step;
}

/** The `<svg role="img" aria-label>` text equivalent, and the answer to "is there
 *  a table view" for people who use neither the bars nor the table. */
export function describeWaitBuckets(buckets: readonly WaitBucket[]): string {
  if (buckets.length === 0) return "No wait events.";
  const total = buckets.reduce((n, b) => n + b.count, 0);
  return (
    `${total} sessions across ${buckets.length} wait-event buckets: ` +
    `${buckets.map((b) => `${b.label} ${b.count}`).join(", ")}.`
  );
}

// --- geometry ---------------------------------------------------------------

const WIDTH = 720; // the app-wide chart width every other chart component defaults to
const LABEL_W = 168; // bucket-name column
const VALUE_W = 48; // value column, so a label never has to overlap the plot
const ROW_H = 28; // row pitch
const BAR_H = 16; // bar thickness, under the skill's 24px ceiling
const BAR_R = 4; // rounded data end
const AXIS_H = 16; // tick-label row above the plot
const TICKS = 4; // gridlines at 0/25/50/75/100 % of max
const LABEL_FONT = 11;

// --- component --------------------------------------------------------------

export interface WaitEventsChartProps {
  /** From `waitBuckets(resp.sessions)` — the page computes it once and passes it
   *  to both the chart and the drill, so the bars and the drill table cannot
   *  disagree. */
  buckets: readonly WaitBucket[];
  width?: number;
  /** The bucket the drill below is showing. `null` = nothing selected. */
  selectedKey?: string | null;
  /** Click / Enter on a bar. Omit to render the chart non-interactive. */
  onSelect?: (key: string) => void;
}

export function WaitEventsChart({
  buckets,
  width = WIDTH,
  selectedKey = null,
  onSelect,
}: WaitEventsChartProps): ReactElement {
  const [hover, setHover] = useState<{ key: string; x: number } | null>(null);

  if (buckets.length === 0) {
    // R10.13: a chart with nothing to draw says so in one line rather than
    // rendering an empty frame. The page's own state panel normally catches this;
    // the guard is here so the component is never the thing that goes blank.
    return (
      <section className="card" data-panel="wait-events">
        <div className="card-head">
          <h2 className="card-title">Wait events by type</h2>
        </div>
        <p className="chart-empty">No sessions in this payload, so there are no wait events to chart.</p>
      </section>
    );
  }

  const total = buckets.reduce((n, b) => n + b.count, 0);
  const max = buckets.reduce((m, b) => Math.max(m, b.count), 0);
  const plotX = LABEL_W;
  const plotW = Math.max(1, width - LABEL_W - VALUE_W);
  const plotH = buckets.length * ROW_H;
  const height = AXIS_H + plotH;
  const scale = scaleLinear().domain([0, max]).range([0, plotW]);

  const hovered = hover === null ? null : buckets.find((b) => b.key === hover.key) ?? null;

  return (
    <section className="card" data-panel="wait-events">
      <div className="card-head">
        <h2 className="card-title">Wait events by type</h2>
        <div className="card-meta">
          {total} sessions · {buckets.length} {buckets.length === 1 ? "bucket" : "buckets"}
        </div>
      </div>

      <div className="chart-frame">
        <svg
          width={width}
          height={height}
          viewBox={`0 0 ${width} ${height}`}
          role="img"
          aria-label={describeWaitBuckets(buckets)}
        >
          {/* Recessive grid: hairlines in the grid token, the axis rule in the
              darker baseline token, tick labels in the muted ink. */}
          {Array.from({ length: TICKS + 1 }, (_, i) => {
            const x = plotX + (plotW * i) / TICKS;
            return (
              <line
                key={`grid-${i}`}
                x1={x}
                y1={AXIS_H}
                x2={x}
                y2={AXIS_H + plotH}
                stroke={i === 0 ? "var(--baseline)" : "var(--gridline)"}
                strokeWidth={1}
              />
            );
          })}
          {Array.from({ length: TICKS + 1 }, (_, i) => (
            <text
              key={`tick-${i}`}
              className="spark-axis-label"
              x={plotX + (plotW * i) / TICKS}
              y={AXIS_H - 5}
              textAnchor={i === 0 ? "start" : i === TICKS ? "end" : "middle"}
              fill="var(--text-muted)"
              fontSize={LABEL_FONT}
            >
              {String(Math.round((max * i) / TICKS))}
            </text>
          ))}

          {buckets.map((b, i) => {
            const y = AXIS_H + i * ROW_H;
            const barY = y + (ROW_H - BAR_H) / 2;
            const barW = Math.max(b.count === 0 ? 0 : 2, scale(b.count));
            const step = rampStep(b.count, max);
            const valueText = String(b.count);
            const valueW = measureText(valueText, LABEL_FONT);
            const selected = selectedKey === b.key;
            return (
              <g key={b.key} transform={`translate(0 ${barY})`}>
                <text
                  x={LABEL_W - 8}
                  y={BAR_H / 2 + 4}
                  textAnchor="end"
                  fill="var(--text-primary)"
                  fontSize={LABEL_FONT}
                >
                  {b.label}
                </text>
                <path
                  d={dataEndPath(barW, BAR_H, BAR_R)}
                  transform={`translate(${plotX} 0)`}
                  fill={`var(${step.token})`}
                />
                {/* Selection marker: an outline, not a fill change — R10.23's
                    non-colour encoding, so the selected row reads in greyscale. */}
                {selected && (
                  <rect
                    x={plotX - 3}
                    y={-3}
                    width={barW + 6}
                    height={BAR_H + 6}
                    rx={BAR_R + 2}
                    fill="none"
                    stroke="var(--text-secondary)"
                    strokeWidth={1}
                  />
                )}
                <text
                  className="mark-label"
                  x={plotX + barW + 6 + valueW / 2}
                  y={BAR_H / 2 + 4}
                  fill="var(--text-secondary)"
                  fontSize={LABEL_FONT}
                >
                  {valueText}
                </text>
                {/* Hit target: the whole row, not the 16px bar (interaction.md:
                    hit targets bigger than the mark). */}
                {onSelect !== undefined && (
                  <rect
                    x={0}
                    y={-6}
                    width={width}
                    height={ROW_H}
                    fill="transparent"
                    role="button"
                    tabIndex={0}
                    aria-label={`${b.label}: ${b.count} of ${total} sessions`}
                    aria-current={selected ? "true" : undefined}
                    onClick={() => onSelect(b.key)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        onSelect(b.key);
                      }
                    }}
                    onMouseEnter={() => setHover({ key: b.key, x: plotX + barW })}
                    onMouseLeave={() => setHover(null)}
                    onFocus={() => setHover({ key: b.key, x: plotX + barW })}
                    onBlur={() => setHover(null)}
                  />
                )}
              </g>
            );
          })}
        </svg>

        {hover !== null && hovered !== null && (
          <div
            className="viz-tooltip"
            role="tooltip"
            style={{
              left: `${(hover.x / width) * 100}%`,
              top: `${AXIS_H + buckets.indexOf(hovered) * ROW_H + BAR_H + 6}px`,
            }}
          >
            <div className="tt-value">{String(hovered.count)}</div>
            <div className="tt-label">{hovered.label}</div>
          </div>
        )}
      </div>

      {/* R10.23's table twin, in the styled `table.data` (`web/src/styles/base.css:213`)
          inside `.table-wrap` (`web/src/styles/base.css:209`) — the same pairing
          `web/src/components/LatencyHistogram.tsx:315` uses. The swatch column
          repeats the bar's own colour so the ramp is readable from the table too;
          it is the twin's key, not a legend — a one-series chart gets no legend
          box, because the title already names the series. */}
      <div className="table-wrap">
        <table className="data">
          <caption className="sr-only">Wait event buckets, as a table</caption>
          <thead>
            <tr>
              <th scope="col">Wait event</th>
              <th scope="col">Sessions</th>
              <th scope="col">Share</th>
            </tr>
          </thead>
          <tbody>
            {buckets.map((b) => (
              <tr key={b.key}>
                <th scope="row">
                  <span
                    aria-hidden="true"
                    style={{
                      display: "inline-block",
                      width: 10,
                      height: 10,
                      borderRadius: 2,
                      marginRight: 6,
                      background: `var(${rampStep(b.count, max).token})`,
                    }}
                  />
                  {b.label}
                </th>
                <td>{b.count}</td>
                <td>{total === 0 ? NULL_VALUE : `${Math.round((b.count / total) * 100)}%`}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
