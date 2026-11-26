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

import type {
  ActiveQueriesResponse,
  ClusterMetrics,
  Envelope,
  FleetResponse,
  FleetRow,
  QueryResponse,
  TagView,
} from "../api/types";
import { CLUSTER_NOUN, partialBanner, type BannerNoun, type BannerSource } from "../api/envelope";
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

export interface ActivePanelInput {
  phase: FetchPhase;
  /** The last good payload, if any. Present during a background refetch. */
  active: ActiveQueriesResponse | null;
  /** Set when the fetch failed. */
  error?: string;
}

/** The Active Queries page's rule (R10.8).
 *
 *  Same shape again, and the third one that cannot share the first's body for
 *  the same reason `deriveQueryPanelState` can't: the payload type. Three
 *  payloads, three derivations, one `PanelState` union — which is the
 *  alternative to a generic over `data`, and the generic is the one that would
 *  have to invent a `partial` rule for a payload whose `meta` says something
 *  different from what `data` lists.
 *
 *  `unavailable` is unreachable here, and that is the requirement's own doing:
 *  R4.1's read needs `CONNECT` and nothing else — no extension, no metrics
 *  source, no Enterprise tier — so the Queries page's "the source is not
 *  configured" case has no analogue. It stays in the copy table because the
 *  table is `Record<Exclude<PanelState, "ready">, …>` and a missing key is a
 *  type error, not a documented choice. */
export function deriveActivePanelState(input: ActivePanelInput): PanelState {
  const { phase, active } = input;

  if (phase === "idle" || phase === "loading") {
    return active === null ? "loading" : "partial";
  }
  if (phase === "error") {
    return active === null ? "error" : "partial";
  }
  if (active === null) return "loading";
  // An empty session set is this page's good news, and it is still R10.12's
  // named `empty`: a cluster with nothing running renders a named screen, not
  // a table with zero rows and no explanation.
  if (active.data.length === 0) return "empty";
  if (partialBanner({ meta: active.meta, data: active.data }) !== null) return "partial";
  return "ready";
}

export interface WaitEventsPanelInput {
  phase: FetchPhase;
  /** The last good payload, if any. Present during a background refetch.
   *
   *  The same `ActiveQueriesResponse` the Active Queries page renders. The
   *  ruling on this route is that there is no wait-events DTO — the page groups
   *  this payload rather than fetching its own — so this field is the payload the
   *  chart buckets, and when a `WaitEventsResponse` lands it becomes a sixth
   *  input beside this one rather than a rename of it. */
  active: ActiveQueriesResponse | null;
  /** Set when the fetch failed. */
  error?: string;
}

/** The Wait Events page's rule (R10.9).
 *
 *  The fifth derivation over the one `PanelState` union — after `:41`, `:92`,
 *  `:139` and the tags one below — and it is the same six lines as
 *  `deriveActivePanelState` apart from the field name, for the reason those two
 *  are the same as `deriveQueryPanelState`: the rule is over a payload *shape*
 *  (a phase, a nullable payload, an optional error) and there are now five
 *  shapes. A generic over `data` is the alternative the file already rejected.
 *
 *  `unavailable` is unreachable here for the same reason it is on the Active
 *  Queries page: the read needs only `CONNECT`, so there is no
 *  extension-not-installed case to render, and the key stays because the copy
 *  table is a `Record` over every non-ready state. */
export function deriveWaitEventsPanelState(input: WaitEventsPanelInput): PanelState {
  const { phase, active } = input;

  if (phase === "idle" || phase === "loading") {
    return active === null ? "loading" : "partial";
  }
  if (phase === "error") {
    return active === null ? "error" : "partial";
  }
  if (active === null) return "loading";
  if (active.data.length === 0) return "empty";
  if (partialBanner({ meta: active.meta, data: active.data }) !== null) return "partial";
  return "ready";
}

export interface TagsPanelInput {
  phase: FetchPhase;
  /** The last good payload, if any. Present during a background refetch.
   *
   *  `Envelope<TagView>` rather than a named response struct, because R8.4's
   *  envelope has exactly the two fields this route serves (`data`, `meta`) and
   *  a third key is what forced `dto.QueriesResponse` into existence for the
   *  queries route. `client.ts:522`'s `fetchTags` returns the same type, so the
   *  page's prop, the fetcher's return and this input are one type. */
  tags: Envelope<TagView> | null;
  /** Set when the fetch failed. */
  error?: string;
  /** R3.1's per-database presence. The tags envelope does not carry it —
   *  `dto.ExtensionPresenceView` is on the *queries* route — so this is the
   *  same interim `QueryPanelInput.extensionInstalled` is, with the same
   *  default of *unknown*. */
  extensionInstalled?: boolean;
}

/** The Tags page's rule (R10.3's ninth page, R3.12's UI, R8.2's route).
 *
 *  The fourth derivation over the one `PanelState` union, and the third one
 *  that can reach `unavailable` — `derivePanelState` at `:61` and
 *  `deriveQueryPanelState` at `:110` come before it. R3.7's rollup is computed from the query store,
 *  which is what `pg_stat_statements` feeds, so an empty tag set is ambiguous in
 *  exactly the way an empty query set is: not installed (R10.27's first-class
 *  screen) or installed with nothing in the window (R10.12's plain empty). The
 *  resolution is the same and lives in the same place — the explicit `false`
 *  renders `unavailable`, `undefined` renders `empty` — and for the same reason:
 *  a populated payload is proof the extension is there, so the check belongs
 *  inside the zero-row branch and not above it.
 *
 *  `partial` is reachable here in a way it is not on the Active Queries page:
 *  `Envelope.meta` carries `failed`/`loading`/`truncated` for every route, and
 *  `partialBanner` is what reads them, so a tag rollup that served 17 of the 23
 *  tags it knows about says so with the tag noun rather than looking complete. */
export function deriveTagsPanelState(input: TagsPanelInput): PanelState {
  const { phase, tags } = input;

  if (phase === "idle" || phase === "loading") {
    return tags === null ? "loading" : "partial";
  }
  if (phase === "error") {
    return tags === null ? "error" : "partial";
  }
  if (tags === null) return "loading";
  if (tags.data.length === 0) {
    return input.extensionInstalled === false ? "unavailable" : "empty";
  }
  if (partialBanner({ meta: tags.meta, data: tags.data }) !== null) return "partial";
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

/** The Active Queries page's copy table (R10.8).
 *
 *  `empty` is the one cell worth reading, because on this page it is the
 *  *normal* answer rather than the exceptional one: a cluster with nothing
 *  running has no active sessions, and R4.1's whole premise is that the answer
 *  changes every second. A page that renders "No queries in this window" for it
 *  borrows the query store's noun for a pg_stat_activity fact — the store's
 *  emptiness is about a window, this one is about a moment. */
export const ACTIVE_COPY: Record<Exclude<PanelState, "ready">, PanelCopy> = {
  loading: {
    title: "Reading pg_stat_activity",
    body: "The first read for this cluster has not answered yet.",
  },
  empty: {
    title: "No active sessions",
    body: "The cluster reported no sessions in pg_stat_activity. Nothing is running and nothing is idle in transaction — the query store's own state is a different question, on the Queries page.",
    action: "Refresh",
  },
  partial: {
    title: "Partial results",
    body: "The read returned fewer sessions than the scorecard counts, so the table is a page of the answer rather than all of it.",
  },
  unavailable: {
    title: "pg_stat_activity is not readable",
    body: "The cluster answered but returned no session set. R4.1's read needs only CONNECT, so this is RBAC or a paused instance rather than a missing extension.",
    action: "Retry",
  },
  error: {
    title: "Could not load the active sessions",
    body: "GET /api/v1/active-queries did not answer for this cluster.",
    action: "Retry",
  },
};

/** The Wait Events page's copy table (R10.9).
 *
 *  Fifth table, same six states. Two cells are worth reading rather than
 *  copying:
 *
 *  · `empty` is not the Active Queries page's good news. "No active sessions"
 *    there means nothing is running; here the chart also has nothing to bucket,
 *    and the two readings differ because a session can be idle *and* waiting.
 *    The body says which one the reader is looking at.
 *  · `error` names the route the page actually calls
 *    (`GET /api/v1/active-queries`), not the one its route row advertises
 *    (`GET /api/v1/insights/wait-events`, `web/src/routes.ts:228`). The copy has
 *    to describe the request that failed, and the advertised one does not exist
 *    yet — see the payload note on `WaitEventsPanelInput`. */
export const WAIT_EVENTS_COPY: Record<Exclude<PanelState, "ready">, PanelCopy> = {
  loading: {
    title: "Reading pg_stat_activity",
    body: "The first read for this cluster has not answered yet.",
  },
  empty: {
    title: "No sessions to bucket",
    body: "The cluster reported no sessions, so there is nothing to group by wait event. A session that is not waiting is still its own bucket when other sessions exist; with no sessions at all the chart has no bar to draw.",
    action: "Refresh",
  },
  partial: {
    title: "Partial results",
    body: "The read returned fewer sessions than the scorecard counts, so the buckets are built from a page of the answer rather than all of it.",
  },
  unavailable: {
    title: "pg_stat_activity is not readable",
    body: "The cluster answered but returned no session set. The read needs only CONNECT, so this is RBAC or a paused instance rather than a missing extension.",
    action: "Retry",
  },
  error: {
    title: "Could not load the sessions",
    body: "GET /api/v1/active-queries did not answer for this cluster. The Wait Events route has no endpoint of its own yet, so this page reads the Active Queries payload and groups it.",
    action: "Retry",
  },
};

/** The Tags page's copy table (R10.3's ninth page).
 *
 *  Fourth table, same six states, the tag's nouns. `unavailable` is R10.27's
 *  wording again rather than the fleet's, for the same reason `QUERY_COPY`'s is:
 *  R3.7's rollup is computed from the query store, so "no tags" and "no
 *  extension" are two answers and only one of them is the plain empty. The page
 *  renders `ExtensionNotInstalled` for that state rather than this string — the
 *  screen is a component with the flow and the CLI line on it — which is the
 *  same division of labour `QUERY_COPY.unavailable` has, and is why the
 *  catalogue test can assert the copy without the page rendering it. */
export const TAGS_COPY: Record<Exclude<PanelState, "ready">, PanelCopy> = {
  loading: {
    title: "Loading tags",
    body: "Reading the tag rollup for this cluster.",
  },
  empty: {
    title: "No tags in this window",
    body: "The tag rollup returned no rows for this cluster and range. A tag exists per distinct dimension set among the queries the store has, so an empty set means no query was attributed in the window; the R10.27 not-installed screen names the other reading once the cluster reports the extension absent.",
    action: "Widen the range",
  },
  partial: {
    title: "Partial results",
    body: "Some namespaces have not reported yet. The rows shown are complete for the namespaces that have.",
  },
  unavailable: {
    title: "pg_stat_statements is not installed",
    body: "This cluster has no pg_stat_statements extension, so the query store has nothing to roll up and there are no tags to show. Enabling it is a product flow (R3.15): detect, offer, patch the ClusterCRD, verify.",
    action: "Enable pg_stat_statements",
  },
  error: {
    title: "Could not load the tags",
    body: "GET /api/v1/insights/tags did not answer for this cluster.",
    action: "Retry",
  },
};

/** The copy for one state, with R10.22's composed body swapped in for `partial`.
 *
 *  `noun` is the subject the banner counts (`QUERY_NOUN`, `SESSION_NOUN`, …) and
 *  defaults to `CLUSTER_NOUN`, which is what every call site before the Active
 *  Queries page wanted and what `Fleet.tsx` still passes by omission. It is
 *  inert for every state but `partial`, because only that state composes a body
 *  from a payload — which is why `StatePanel` can forward it while passing
 *  `undefined` for `source`. */
export function copyFor(
  state: PanelState,
  source?: BannerSource,
  table: Record<Exclude<PanelState, "ready">, PanelCopy> = FLEET_COPY,
  noun: BannerNoun = CLUSTER_NOUN,
): PanelCopy | null {
  if (state === "ready") return null;
  const copy = table[state];
  // R10.22's wording is composed from the payload, not fixed: the page that
  // served 3 of 4 clusters has to name the fourth, which generic copy cannot.
  if (state === "partial" && source !== undefined) {
    const derived = partialBanner(source, noun);
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
