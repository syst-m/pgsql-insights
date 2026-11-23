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

import type { ClusterMetrics, FleetResponse, FleetRow, QueryResponse } from "../api/types";
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

export interface QueryPanelInput {
  phase: FetchPhase;
  /** The last good payload, if any. Present during a background refetch. */
  queries: QueryResponse | null;
  /** Set when the fetch failed. */
  error?: string;
  /** R3.1's per-database presence, once `dto/clusters.go` exposes it. Absent
   *  means *unknown*, which is every response today — the field is the ask. */
  extensionInstalled?: boolean;
}

/** The Queries page's rule. Same shape as `derivePanelState`, one payload type
 *  apart — and one rule it cannot implement.
 *
 *  The fleet's rule ends with `hasLiveMetrics`, which is how it tells "clusters
 *  exist but nothing can answer" apart from "clusters exist and they answered".
 *  A query row is *itself* the answer, so a non-empty payload is by definition
 *  `ready`; and an empty one is ambiguous in a way the fleet's is not. An empty
 *  `QueryResponse` means either "pg_stat_statements is not installed" (R10.27,
 *  which §9's 6b acceptance requires as a first-class screen) or "installed, and
 *  this window had no queries" (R10.12's plain empty), and nothing in
 *  `QueryResponse` or `Meta` says which — R3.1's per-database presence is a
 *  `pg_extension` read that `dto/clusters.go` does not expose.
 *  `QueryPanelInput.extensionInstalled` is the interim source of that fact: the
 *  page takes it as a prop, defaults it to *unknown*, and unknown renders the
 *  plain empty. The R10.27 screen renders on an explicit `false`, which is what
 *  `pages/Queries.test.tsx` passes to render it. */
export function deriveQueryPanelState(input: QueryPanelInput): PanelState {
  const { phase, queries } = input;

  if (phase === "idle" || phase === "loading") {
    return queries === null ? "loading" : "partial";
  }
  if (phase === "error") {
    return queries === null ? "error" : "partial";
  }
  if (queries === null) return "loading";
  if (queries.queries.length === 0) {
    // The only two readings of an empty payload, and the only place the
    // ambiguity exists. R10.27's not-installed screen needs the extension
    // absent; `undefined` — which is every response today, and the page's
    // default — is *unknown*, not *absent*, so it falls through to R10.12's
    // plain empty. A populated payload can't be "not installed" at all: the
    // rows are the proof, which is why this is inside the zero-row branch
    // rather than above it.
    return input.extensionInstalled === false ? "unavailable" : "empty";
  }
  if (partialBanner({ meta: queries.meta, data: queries.queries }) !== null) return "partial";
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

/** The Fleet page's copy table (R10.12's named states, in the fleet's nouns).
 *
 *  Exported rather than left module-private because the strings are about
 *  *clusters* — "No clusters yet", "Could not load the fleet" — and a page that
 *  renders them for a different payload says the wrong noun. `copyFor`'s
 *  `table` parameter is what lets `QUERY_COPY` below replace them without the
 *  two pages each carrying their own `StatePanel` markup. */
export const FLEET_COPY: Record<Exclude<PanelState, "ready">, PanelCopy> = {
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

/** The Queries page's copy table.
 *
 *  Same six states, different nouns — and one state that is not a copy of the
 *  fleet's at all. R10.27 requires the `pg_stat_statements` not-installed case
 *  to be "a first-class screen, designed and tested — not an empty table with a
 *  tooltip", and §9's 6b acceptance names it: "the R10.27 not-installed screen
 *  renders for every cluster lacking the extension". So `unavailable` here is
 *  that screen's wording, not the fleet's "no metrics source" wording.
 *
 *  The screen itself is a component, not a string, and it is waiting on a field:
 *  an empty `QueryResponse` is ambiguous between *not installed* (R10.27) and
 *  *installed, nothing ran in the window* (R10.12's plain empty), and the only
 *  thing that tells them apart is R3.1's per-database presence detection — which
 *  `dto/clusters.go` does not expose yet. `deriveQueryPanelState` returns
 *  `empty` unless a caller supplies the fact, and
 *  `QueryPanelInput.extensionInstalled` is how it does. */
export const QUERY_COPY: Record<Exclude<PanelState, "ready">, PanelCopy> = {
  loading: {
    title: "Loading queries",
    body: "Reading the query store for this cluster.",
  },
  empty: {
    title: "No queries in this window",
    body: "The query store returned no rows for this cluster and range. A cluster without pg_stat_statements has no query rows to show; the R10.27 not-installed screen names that case once the cluster reports the extension absent.",
    action: "Widen the range",
  },
  partial: {
    title: "Partial results",
    body: "Some namespaces have not reported yet. The rows shown are complete for the namespaces that have.",
  },
  unavailable: {
    title: "pg_stat_statements is not installed",
    body: "This cluster has no pg_stat_statements extension, so there is no query data to show. Enabling it is a product flow (R3.15): detect, offer, patch the ClusterCRD, verify.",
    action: "Enable pg_stat_statements",
  },
  error: {
    title: "Could not load the queries",
    body: "GET /api/v1/insights/queries did not answer for this cluster.",
    action: "Retry",
  },
};

export function copyFor(
  state: PanelState,
  source?: BannerSource,
  table: Record<Exclude<PanelState, "ready">, PanelCopy> = FLEET_COPY,
): PanelCopy | null {
  if (state === "ready") return null;
  const copy = table[state];
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
  /** The page's copy table. Defaults to `FLEET_COPY`, so `Fleet.tsx` is
   *  unchanged by this parameter; `Queries.tsx` passes `QUERY_COPY`. */
  copy?: Record<Exclude<PanelState, "ready">, PanelCopy>;
}

/** The full-panel variant (empty / loading / error). Partial and unavailable
 *  render as a banner over real content instead — see FleetPage. */
export function StatePanel({ state, error, onAction, copy: copyTable }: StatePanelProps) {
  const copy = copyFor(state, undefined, copyTable ?? FLEET_COPY);
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
