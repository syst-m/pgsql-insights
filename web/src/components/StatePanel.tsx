/* R10.12's named page states, and the rule that picks one.
 *
 * R10.12 requires each page to render its empty, loading, partial and
 * unavailable states as named fixtures — not as whatever the component
 * happens to look like when data is missing. Making the choice a pure function
 * (`derivePanelState`) is what turns that into something the test suite can
 * prove: every state has a case, and R16.7's visual suite can screenshot each
 * one by name.
 *
 * R10.22's rule shapes two of them: a refetch keeps the previous frame at
 * reduced opacity instead of flashing a skeleton, and a page that has 3 of 4
 * clusters says so rather than looking complete.
 */

import type { ClusterMetrics, FleetResponse, FleetRow } from "../api/types";
import { partialBanner, type BannerSource } from "../api/envelope";
import { FIXTURE_EMPTY, FIXTURE_PARTIAL, FIXTURE_UNAVAILABLE } from "../api/fixtures";

export type PanelState = "loading" | "empty" | "partial" | "unavailable" | "ready" | "error";

export type FetchPhase = "idle" | "loading" | "ready" | "error";

export interface PanelInput {
  phase: FetchPhase;
  /** The last good payload, if any. Present during a background refetch. */
  fleet: FleetResponse | null;
  /** Set when the fetch failed. */
  error?: string;
}

/** Pick the state to render. Order matters: an in-flight request with no
 *  previous frame is `loading` before anything else is considered. */
export function derivePanelState(input: PanelInput): PanelState {
  const { phase, fleet } = input;

  if (phase === "idle" || phase === "loading") {
    return fleet === null ? "loading" : "partial";
  }
  if (phase === "error") {
    // A failed refresh over a good frame keeps the frame and shows the banner;
    // a first-load failure has nothing to show.
    return fleet === null ? "error" : "partial";
  }
  if (fleet === null) return "loading";
  if (fleet.clusters.length === 0) return "empty";
  // R10.22's banner is derived from the response — meta.failed / meta.loading
  // for the reason, and data.length for the count. The API has no `partial`
  // key, and `truncated` alone is pagination, not a warning.
  if (partialBanner(fleet) !== null) return "partial";
  // Clusters exist but not one of them has a metrics source that can answer:
  // the R10.12 "unavailable (source not configured)" state, which on the
  // reference environment is the normal case (live-probe Finding 1).
  if (!fleet.clusters.some(hasLiveMetrics)) return "unavailable";
  return "ready";
}

/** True when a cluster's metrics came from a source that produces values.
 *
 * `unconfigured` and `failed` both mean "there are no numbers here" — one
 * because nothing was set up, one because the scrape broke — so neither counts
 * as a live source. Only `pg_stat_statements` / `cnpg_metrics` do. */
export function hasLiveMetrics(c: FleetRow): boolean {
  // `metrics` is optional on the row: Phase 1a's contract has no metrics at
  // all, and an absent one is exactly as answerless as a null one.
  const m = c.metrics;
  if (m === null || m === undefined) return false;
  return LIVE_SOURCES.includes(m.source);
}

const LIVE_SOURCES: readonly ClusterMetrics["source"][] = ["pg_stat_statements", "cnpg_metrics"];

export interface PanelCopy {
  title: string;
  body: string;
  /** The action offered, when the state has one. */
  action?: string;
}

const COPY: Record<Exclude<PanelState, "ready">, PanelCopy> = {
  loading: {
    title: "Discovering clusters",
    body: "Reading the ClusterCRDs in every namespace the RBAC scope allows.",
  },
  empty: {
    title: "No clusters yet",
    body: "No PostgreSQL ClusterCRDs were found in the namespaces this deployment can read.",
    action: "Check the RBAC scope",
  },
  partial: {
    title: "Partial results",
    body: "Some namespaces have not reported yet. The rows shown are complete for the namespaces that have.",
  },
  unavailable: {
    title: "Metrics unavailable",
    body: "Clusters were discovered, but no metrics source is configured, so TPS, latency, connections and disk are empty. Discovery and health are unaffected.",
    action: "Configure a metrics source",
  },
  error: {
    title: "Could not load the fleet",
    body: "The API did not answer. The binary serves the API and the UI from one port, so a failure here usually means `pgsi serve` is not running.",
    action: "Retry",
  },
};

export function copyFor(state: PanelState, source?: BannerSource): PanelCopy | null {
  if (state === "ready") return null;
  const copy = COPY[state];
  // R10.22's wording is composed from the payload, not fixed: the page that
  // served 3 of 4 clusters has to name the fourth, which generic copy cannot.
  if (state === "partial" && source !== undefined) {
    const derived = partialBanner(source);
    if (derived !== null) return { ...copy, body: derived };
  }
  return copy;
}

export interface StatePanelProps {
  state: PanelState;
  error?: string;
  onAction?: () => void;
}

/** The full-panel variant (empty / loading / error). Partial and unavailable
 *  render as a banner over real content instead — see FleetPage. */
export function StatePanel({ state, error, onAction }: StatePanelProps) {
  const copy = copyFor(state);
  if (copy === null) return null;
  return (
    <section className="state-panel" data-state={state} role="status" aria-live="polite">
      <h2 className="state-title">{copy.title}</h2>
      <p className="state-body">{state === "error" && error ? `${copy.body} ${error}` : copy.body}</p>
      {copy.action !== undefined && (
        <button type="button" className="btn" onClick={onAction}>
          {copy.action}
        </button>
      )}
    </section>
  );
}

/** The named fixtures R16.7 screenshots, keyed by the state they depict. The
 *  suite iterates this map instead of inventing its own data. */
export const STATE_FIXTURES: Record<"empty" | "partial" | "unavailable", FleetResponse> = {
  empty: FIXTURE_EMPTY,
  partial: FIXTURE_PARTIAL,
  unavailable: FIXTURE_UNAVAILABLE,
};
