/* R10.5's Resource load slot, shipped by 6b before the chart that fills it exists.
 *
 * ── why a 6b component renders a Phase 7 chart's absence ───────────────────
 *
 * A3's ruling on R10.5's assignment (requirements.md, "A3's ruling on R10.5's
 * assignment"; A2 routed it, and the parity-matrix row had been bare while §4's
 * cell was the scope-split exemplar) splits the cell three ways, not two:
 * **(a) Query load**, **(b) Resource load**, and an unlettered *"Plus the R5.1
 * summary cards, a replication topology view sourced from S4 (R5.5), and
 * instance chips with roles."* The boundary runs along the content, not the
 * letters: (a) is 6b's, and (b) plus the R5.1 cards plus the S4 topology are
 * System Insights content assigned to **7**.
 *
 * The sentence that makes any of (b) mine today is the ruling's last one:
 * "R5.3's degradation rule is what makes 6b's half renderable today: it ends
 * 'Never render a blank chart without saying why', so the Resource load panel's
 * absence is itself specified work, and the graph fills in when 7 supplies the
 * S7/`metrics.k8s.io` data rather than being built from scratch by it."
 *
 * So the split is by artifact, not by requirement. Phase 7 owns the graph; 6b
 * owns the panel, and the panel's whole v1 content is the reason the graph is
 * not there. The seam is this file: when R5.3's series land, the chart renders
 * inside this `<section>` and the `.chart-empty` sentence goes away — the
 * commit that draws the chart deletes a paragraph rather than inventing a
 * layout, which is what "fills in … rather than being built from scratch" buys.
 *
 * ── the four answers, and why they are not one string ──────────────────────
 *
 * R5.3 names its sources in order — S7 when Prometheus is configured,
 * `metrics.k8s.io` when only that exists, degraded-with-a-note when neither
 * does — and each answer means something different to a reader, so each gets
 * its own sentence:
 *
 *   - `null`             the app has not asked. `client.ts` has no fetcher for
 *                        R5.3's series yet (the standing ask on A4), so the
 *                        page has nothing to draw and says so. This is the
 *                        distinction R10.13's chart rule draws in
 *                        `QueryLoadChart.tsx:127` — "the app has not asked yet"
 *                        and "the store has no minutes" are different answers
 *                        and printing one for the other is the bug.
 *   - `"none"`           asked, and no source is configured. This is R5.3's
 *                        `degraded-with-a-note` case, and it is the expected
 *                        state, not an incident.
 *   - `"prometheus"`     S7 exists. The series is Phase 7's to render; the
 *                        frame is here and named before it lands.
 *   - `"metrics-server"` the fallback source exists and S7 does not, which
 *                        R5.3's precedence order resolves to metrics-server.
 *
 * ── why the panel wears no colour ──────────────────────────────────────────
 *
 * R10.23 reserves the status palette (`--status-good/warning/serious/critical`)
 * for status, and an unconfigured OS-metrics source is not an incident — it is
 * R5.3's expected degradation. Same reasoning as `ExtensionNotInstalled.tsx`
 * for the missing-extension screen: the state is named in type and prose, not
 * painted.
 *
 * ── why the source union lives here and not in api/types.ts ────────────────
 *
 * R16.8 pins `web/src/api/types.ts` to `internal/api/dto/*.go` field-for-field,
 * and there is no OS-metrics DTO yet — R5.3 is Phase 7's contract, and
 * inventing its shape here is exactly the class of bug R16.8 exists to catch.
 * `OsMetricsSource` is therefore declared in this component, from R5.3's own
 * sentence, and when the DTO lands the union moves to `types.ts` and this file
 * imports it rather than keeping its own copy.
 *
 * ── R10.23's table twin ─────────────────────────────────────────────────────
 *
 * "charts need a table twin" applies to charts. This renders no chart, so there
 * is nothing to twin; the requirement's table arrives with Phase 7's graph and
 * belongs to it, not to a panel that has no rows to print.
 */

import { NULL_VALUE } from "../lib/format";

/** R5.3's source enumeration, in its stated precedence order. Declared here —
 *  see the header's R16.8 section — and not in `api/types.ts`. */
export type OsMetricsSource = "prometheus" | "metrics-server" | "none";

export interface ResourceLoadPanelProps {
  /** The cluster the page is about. `null` is the URL's own "no cluster
   *  selected" and renders as `—`, same contract as `ExtensionNotInstalled`. */
  cluster: string | null;
  /** Which OS-metrics source the cluster has. `null` means the app has not
   *  asked yet — no fetcher exists — which is a different answer from `"none"`,
   *  which means it asked and found nothing. */
  source: OsMetricsSource | null;
}

/** Per-source copy: the sentence that replaces the chart, plus the two source
 *  rows it is derived from. One table so the four answers cannot drift apart
 *  the way four string literals in four JSX branches would.
 *
 *  `null` is not a valid `Record` key (TS2344: only string|number|symbol are),
 *  so the not-asked case is keyed by the string `"not-asked"` and mapped at the
 *  use site. The key is the word the copy itself uses, so the table and the
 *  sentence on screen cannot disagree about what the fourth state is called. */
type SourceKey = OsMetricsSource | "not-asked";

const COPY: Record<
  SourceKey,
  { headline: string; detail: string; s7: string; metricsServer: string }
> = {
  "not-asked": {
    headline: "No OS-metrics series has been fetched for this cluster.",
    detail:
      "web/src/api/client.ts has no fetcher for R5.3's CPU and memory series yet, so the page has not asked the API. The frame carries a sentence instead of a plot because a blank frame reads as data (R10.13).",
    s7: "not queried",
    metricsServer: "not queried",
  },
  none: {
    headline: "No OS-metrics source is configured for this cluster.",
    detail:
      "R5.3 sources CPU, memory, disk and network from S7 when Prometheus is configured and from metrics.k8s.io when only that exists; with neither, the requirement's answer is degraded-with-a-note, and this panel is that note.",
    s7: "not configured",
    metricsServer: "not configured",
  },
  prometheus: {
    headline: "S7 (Prometheus) is configured; the graph is Phase 7's.",
    detail:
      "A3's ruling gives the graph to 7 and the panel to 6b, so the slot is named here before the series exists and the commit that draws the chart fills this frame rather than adding a layout.",
    s7: "configured",
    metricsServer: "not consulted — S7 wins",
  },
  "metrics-server": {
    headline: "metrics.k8s.io is the only source; the graph is Phase 7's.",
    detail:
      "R5.3's precedence puts S7 first and metrics.k8s.io second, so this cluster's series comes from the fallback. The panel is 6b's and the graph is Phase 7's, so the fallback fills an existing frame.",
    s7: "not configured",
    metricsServer: "configured (the fallback)",
  },
};

export function ResourceLoadPanel({ cluster, source }: ResourceLoadPanelProps) {
  const clusterLabel = cluster === null || cluster === "" ? NULL_VALUE : cluster;
  const copy = COPY[source === null ? "not-asked" : source];

  return (
    <section className="card rl-panel" data-state="unavailable" aria-labelledby="rl-title">
      <div className="card-head">
        <h2 className="card-title" id="rl-title">
          Resource load
        </h2>
        <span className="card-meta">R10.5</span>
      </div>

      <p className="rl-cluster">
        <span className="secondary">Cluster</span> <span className="mono">{clusterLabel}</span>
      </p>
      {/* What the chart will show, stated once for all four answers so the slot
          is never anonymous: R10.5(b)'s own words, not a paraphrase. */}
      <p className="rl-scope">CPU capacity vs. usage, and memory working set.</p>

      <p className="chart-empty">{copy.headline}</p>
      <p className="rl-detail">{copy.detail}</p>

      <section className="rl-sources" aria-labelledby="rl-sources-title">
        <h3 className="rl-sources-title" id="rl-sources-title">
          Sources, in R5.3&apos;s order
        </h3>
        {/* dt/dd as direct grid children, the way `.detail-header`
            (web/src/styles/base.css:885) does it — one grid, no wrapper per row. */}
        <dl className="rl-grid">
          <dt>S7 · Prometheus</dt>
          <dd>{copy.s7}</dd>
          <dt>metrics.k8s.io</dt>
          <dd>{copy.metricsServer}</dd>
        </dl>
      </section>
    </section>
  );
}
