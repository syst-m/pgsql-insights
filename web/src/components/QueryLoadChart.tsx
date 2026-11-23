/* R10.7's "the per-minute load chart for that query", drawn as small multiples.
 *
 * ── which chart R10.7 is asking for ───────────────────────────────────────
 *
 * R10.7's sentence — "the per-minute load chart for that query" — names one
 * chart and supplies no metric, and the metric set is what makes it awkward:
 * the data behind it is `QuerySeriesPoint`, which carries five values per
 * minute. R10.5(a)'s `QueryLoadArea` (the Cluster Overview's stacked area of
 * total execution time + IO time) is a *different* requirement's chart, and
 * this file is deliberately not named `QueryLoadArea` so the two cannot be
 * confused by whoever comes after and reads a list of components.
 *
 * ── why five values cannot share a chart ──────────────────────────────────
 *
 * The dataviz skill's #1 chart mistake is the dual-axis chart, and its rule
 * admits one remedy for two measures of different scale: "two charts, small
 * multiples, or indexed to a common base." `QuerySeriesPoint` is the worst case
 * the rule contemplates — two measures in *seconds* (`executionTimeSeconds`,
 * `lockTimeSeconds`) and three in *counts* (`rows`, `sharedBlocksHit`,
 * `sharedBlocksRead`). One axis cannot host 42.7 seconds and 3 007 rows, and
 * indexing them to a common base would answer a question nobody asked ("how did
 * rows move relative to their own mean") instead of the one R10.7 asks ("what
 * did this query's load look like"). So: one `Sparkline` per metric, framed,
 * stacked in a column — the small multiple the skill prescribes, and the reason
 * `Sparkline` has a `framed` mode at all.
 *
 * ── which four, and why not five ──────────────────────────────────────────
 *
 * R9.5's S25 ruling names the per-query export family as `execution_time`,
 * `rows`, `shared_blocks_hit`, `shared_blocks_read` — four — and explicitly
 * keeps `lock_time` out of it (`lock_time` was admitted to the *aggregate*
 * family only). `dto/series.go` still carries all five per point, because
 * R8.11's own comment says the five-value point is what lets one response serve
 * both R10.6's sparkline and R10.7's chart. Carrying a value and rendering it
 * are two different things: this file draws the four the ruling names, and
 * `SERIES_METRICS` is the single place that decision is written down, so if the
 * ruling ever widens, the fix is one entry in one array rather than a hunt
 * through JSX. The `QueryView` side of the page already answers "what about
 * lock wait" for v1 with R10.4's `n/a (no per-query source, §5)` cell.
 *
 * ── the units trap this file exists to close ──────────────────────────────
 *
 * A5's rule, stated in `dto/series.go` and repeated in `api/types.ts:639`:
 * series points are in **seconds**, QueryView/LoadColumns are in
 * **milliseconds**. `formatDurationMs` is the app's only duration formatter and
 * it is fed milliseconds everywhere else, so piping `executionTimeSeconds`
 * into it unmultiplied renders a 42.7-second minute as "43 ms" — a 1000× error
 * that looks like a plausible number and is therefore invisible. `formatSec`
 * below is the one function that multiplies, and every second-valued metric in
 * this file goes through it. It lives here rather than in `lib/format.ts`
 * because that file is A4's and its contract is the R3.2 column formatters.
 *
 * ── R10.13's empty rule ───────────────────────────────────────────────────
 *
 * "no chart renders as a blank box without a one-line explanation." With no
 * series (the route has not been wired — `client.ts` still exports only
 * `fetchFleet`) and with an empty `points` array (a fingerprint the store has
 * never sampled), this renders the sentence in the panel body, not an empty
 * frame. The two cases get different sentences because they mean different
 * things: one is "the app has not asked yet", the other is "the store has no
 * minutes for this query".
 *
 * ── R10.19 for this chart type ────────────────────────────────────────────
 *   - each multiple carries its own title (the metric name) — with one series
 *     per frame there is no legend box to draw, per the skill's single-series
 *     rule;
 *   - the shared x axis is drawn once per frame (R10.21 timestamps), so a viewer
 *     reading down the column is comparing the same minutes;
 *   - hover is per frame, and a frame's tooltip names one value — five series
 *     stacked in one chart would give one tooltip with five rows, which is the
 *     shape the skill wants for a multi-series line and not this shape.
 */

import type { ReactElement } from "react";

import type { QuerySeries, QuerySeriesPoint } from "../api/types";
import { formatDurationMs, formatNumber, formatTimestamp } from "../lib/format";
import { Sparkline } from "./Sparkline";

/** Seconds → the house duration string, via the ms formatter.
 *
 *  The single multiply that closes the units trap documented in the header.
 *  Exported so the Queries table's sparkline column and any future
 *  seconds-valued cell use this instead of re-deriving `* 1000` at each call
 *  site (six unmultiplied call sites is how this class of bug ships). */
export function formatSec(seconds: number): string {
  // Rounding to hundredths of a ms first keeps a float artefact like
  // 42.700000000000003 * 1000 from reaching the formatter as
  // "42700.000000000004 ms"; the formatter's own contract then picks the
  // unit. See lib/format.ts for that contract.
  return formatDurationMs(Math.round(seconds * 1000 * 100) / 100);
}

export interface SeriesMetric {
  /** The field on `QuerySeriesPoint`. */
  key: "executionTimeSeconds" | "rows" | "sharedBlocksHit" | "sharedBlocksRead";
  /** R9.4's metric name, as the spec spells it — the frame title. */
  metric: string;
  /** The R3.2-style display name, for readers who know the table not the spec. */
  label: string;
  /** Formats a point value for the frame's y labels and tooltip. */
  format: (v: number) => string;
}

/** The four members of R9.5's per-query export family, in the order R9.5 lists
 *  them. `lockTimeSeconds` is carried by the wire type and deliberately absent
 *  here — the header documents the S25 ruling that excludes it and the one-line
 *  change that admits it. */
export const SERIES_METRICS: readonly SeriesMetric[] = [
  { key: "executionTimeSeconds", metric: "execution_time", label: "Execution time", format: formatSec },
  { key: "rows", metric: "rows", label: "Rows", format: (v) => formatNumber(v) },
  { key: "sharedBlocksHit", metric: "shared_blocks_hit", label: "Shared blocks hit", format: (v) => formatNumber(v) },
  { key: "sharedBlocksRead", metric: "shared_blocks_read", label: "Shared blocks read", format: (v) => formatNumber(v) },
];

/** The x labels for a series: R10.21's RFC 3339 UTC timestamps, rendered in the
 *  viewer's zone by the shared formatter and index-aligned to the values so
 *  `Sparkline`'s tick selection can label the right minute. */
export function seriesXLabels(points: readonly QuerySeriesPoint[]): string[] {
  return points.map((p) => formatTimestamp(p.timestamp));
}

export interface QueryLoadChartProps {
  /** The fingerprint's series, from `GET /api/v1/insights/queries/series`
   *  requested with one `fingerprints=` value (R8.11's batched route, of which
   *  a batch of one is legal). Undefined while `client.ts` has no
   *  `fetchQuerySeries` — the documented stand-in for the missing fetcher, not
   *  a bug in the page. */
  series?: QuerySeries;
  /** Width passed to each frame's SVG. */
  width?: number;
  /** Plot height per frame. */
  height?: number;
}

/** R10.7's per-minute load chart: four framed sparklines, one per R9.5 metric. */
export function QueryLoadChart({ series, width = 720, height = 64 }: QueryLoadChartProps): ReactElement {
  const points = series?.points ?? [];

  return (
    <section className="card" data-panel="load">
      <div className="card-head">
        <h2 className="card-title">Load</h2>
        {series !== undefined && (
          <div className="card-actions">
            <span className="card-meta num">
              {formatNumber(points.length)} {points.length === 1 ? "minute" : "minutes"}
            </span>
          </div>
        )}
      </div>

      {points.length === 0 ? (
        /* R10.13: an explanation, never a blank box. The two empty cases are
           distinguished because they are answered differently — one needs the
           fetcher, the other needs the collector to have run. */
        <p className="chart-empty" data-testid="load-empty">
          {series === undefined
            ? "No series loaded — this page reads GET /api/v1/insights/queries/series with one fingerprints= value (R8.11); the client has not requested it yet."
            : "The store has no per-minute samples for this fingerprint."}
        </p>
      ) : (
        <div className="small-multiples">
          {SERIES_METRICS.map((m) => (
            <figure className="small-multiple" key={m.key}>
              <figcaption className="small-multiple-head">
                <span className="small-multiple-title">{m.label}</span>
                <span className="small-multiple-metric">{m.metric}</span>
              </figcaption>
              <Sparkline
                values={points.map((p) => p[m.key])}
                width={width}
                height={height}
                framed
                formatValue={m.format}
                xLabels={seriesXLabels(points)}
                label={`${m.label} per minute`}
              />
            </figure>
          ))}
        </div>
      )}
    </section>
  );
}
