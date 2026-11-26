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
import { Glyph, type GlyphPresentation } from "./StatusGlyph";

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
 *  `QueryPanelInput.extensionInstalled` is the source of that fact: the
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
   *  same shape `QueryPanelInput.extensionInstalled` has, with the same
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

/** The interim row type for R8.2's `/api/v1/insights/system` (R10.10's page).
 *
 *  R16.8 pins `web/src/api/types.ts` to `internal/api/dto/*.go` field-for-field
 *  and the Phase 7 DTO does not exist, so this is the same interim declaration
 *  `web/src/components/ResourceLoadPanel.tsx:59-64` makes for R5.3's source
 *  union: declared from the requirement's own sentence, marked the standing ask
 *  on A2, and moved to `types.ts` when the Go DTO lands rather than kept as a
 *  second copy beside it. Its three fields are the three the requirements name —
 *  the group (R10.10's tabs are the groups), the PromQL (R5.6's external link is
 *  "built from the panel's PromQL"), and the aggregate value (the group aggregate
 *  the route exists to serve).
 *
 *  It lives here rather than in `SystemInsights.tsx` because the other five
 *  derivations take their payload types from this file's import block, and a
 *  second copy in the page would be two places to update when the DTO lands. */
export interface SystemGroupView {
  group: string;
  promql: string;
  value: number | null;
}

export interface SystemPanelInput {
  phase: FetchPhase;
  /** The last good payload, if any. Present during a background refetch.
   *
   *  `Envelope<SystemGroupView>`, the same R8.4 envelope shape
   *  `TagsPanelInput.tags` at `:208` uses for its R8.2 route — the envelope has
   *  exactly the two fields every route returns and the row type is the only
   *  provisional part. */
  system: Envelope<SystemGroupView> | null;
  /** Set when the fetch failed. */
  error?: string;
}

/** The System Insights page's rule (R10.10).
 *
 *  The sixth derivation over the one `PanelState` union, and the first whose
 *  *default* answer is `unavailable` rather than `loading`. Every other rule
 *  reads a phase that a hook is driving: `phase === "idle"` means "a fetch is
 *  about to happen", so `loading` is the honest answer. Here there is no fetcher
 *  for R8.2's route — `web/src/api/client.ts` has none, which is the standing
 *  ask on A4 — so `idle` means "the app will never ask", and R10.12's rule that
 *  a panel defines each of its four states (`requirements.md:425`) picks
 *  `unavailable`: a loading state would promise a frame that cannot arrive.
 *  A3's ruling on this row makes it the deliverable — "It renders R10.12's
 *  unavailable state rather than invented data … that state is the requirement,
 *  not a placeholder."
 *
 *  The rule is payload-keyed before it is phase-keyed, which is the half A3's
 *  S150 review (`docs/reviews/finding-index.md:184`, filed in `06320f8`) asked
 *  to have said plainly: the `system === null` arm comes first, so
 *  `{phase: "idle", system: ONE}` returns `partial`, not `unavailable`.
 *  `unavailable` is what a *payload-less* cluster answers, and `idle` only names
 *  it because a page with no fetcher can never produce a payload; with a payload
 *  and a driving phase the mid-refetch answer is `partial`, as in the other
 *  five derivations.
 *
 *  `error` still wins over it, because a failed ask and a never-asked question
 *  are different sentences; and `partial` needs a payload to be partial about,
 *  so the branch is reachable only once the DTO lands. */
export function deriveSystemPanelState(input: SystemPanelInput): PanelState {
  const { phase, system } = input;

  if (system === null) return phase === "error" ? "error" : "unavailable";
  if (phase === "idle" || phase === "loading") return "partial";
  if (phase === "error") return "partial";
  if (system.data.length === 0) return "empty";
  if (partialBanner({ meta: system.meta, data: system.data }) !== null) return "partial";
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
 *    (`GET /api/v1/insights/wait-events`, `web/src/routes.ts:230`). The copy has
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

/** The System Insights page's copy table (R10.10).
 *
 *  Sixth table, same six states. Two cells are the page's own rather than
 *  borrowed:
 *
 *  · `unavailable` is the state the page renders today, and the copy has to say
 *    why without implying an outage: the route exists in the manifest
 *    (`api/x-planned-paths.yaml:147`) and has no handler, so nothing has been
 *    fetched — which is R5.3's "never render a blank chart without saying why"
 *    applied to a whole page. The page renders this pair as a `.banner` over its
 *    real content (`web/src/pages/SystemInsights.tsx`), not as a full-panel
 *    replacement, because R10.12's two warning states are banners.
 *  · `error` names the route the page would call
 *    (`GET /api/v1/insights/system`, `web/src/routes.ts:285`), not one it calls
 *    today — there is no request to report a failure about until the fetcher
 *    exists, which is the same situation `WAIT_EVENTS_COPY.error` documents at
 *    `web/src/components/StatePanel.tsx:481` for a page that reads a different route.
 *
 *  `partial`'s fixed body is what renders, not R10.22's composed one: `copyFor`
 *  composes only when a `BannerSource` is passed, and the page passes none —
 *  there is no payload to count metrics in yet. When the DTO lands, the noun
 *  question is A4's (`web/src/api/envelope.ts:935`'s `BannerNoun` set has no
 *  metric entry), and it is deferred rather than invented here because
 *  `partial` is unreachable while `system === null`, so a noun added now would
 *  be inert and untestable. */
export const SYSTEM_COPY: Record<Exclude<PanelState, "ready">, PanelCopy> = {
  loading: {
    title: "Loading system metrics",
    body: "A system-metrics fetch is in flight for this cluster.",
  },
  empty: {
    title: "No metric groups returned",
    body: "The route answered but returned no metric groups, so there is nothing to tab between and no card has a value. R5.2 and R5.3's families are listed on the page so the inventory is still visible.",
    action: "Refresh",
  },
  partial: {
    title: "Partial results",
    body: "The response reported fewer metric groups than the cluster has families, so the cards shown are a subset of R5.2 and R5.3's list.",
  },
  unavailable: {
    title: "System metrics are not configured",
    body: "GET /api/v1/insights/system is in the manifest but has no handler, so this page has no metrics to show and renders R5.2 and R5.3's families instead of inventing values for them. The Log errors card is the one value the spec answers today: R5.10's --log-source defaults to off, and R5.1 says the card renders `not configured` when it is.",
    action: "Configure system metrics",
  },
  error: {
    title: "Could not load the system metrics",
    body: "GET /api/v1/insights/system did not answer for this cluster.",
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
/** R10.23's non-colour encoding for the panel states themselves: the
 *  health vocabulary's table is `api/health.ts`'s `PRESENTATION`, and this is
 *  the second one `StatusGlyph.tsx`'s generalised `Glyph` draws -- the half of
 *  R10.23 IMPLEMENTATION.md:1140 still calls open. Six states, six shapes, so
 *  no two share a glyph and the set property the test holds is the same
 *  property the health table has. `partial` is a triangle rather than a yellow
 *  dot because `--status-warning` is below 3:1 on the light surface: the shape
 *  is what carries it. */
export const PANEL_GLYPHS: Record<PanelState, GlyphPresentation> = {
  loading: { label: "Loading", token: "var(--text-muted)", glyph: "dash" },
  empty: { label: "Empty", token: "var(--text-muted)", glyph: "square" },
  partial: { label: "Partial", token: "var(--status-warning)", glyph: "triangle" },
  unavailable: { label: "Unavailable", token: "var(--status-serious)", glyph: "ring" },
  ready: { label: "Ready", token: "var(--status-good)", glyph: "circle" },
  error: { label: "Error", token: "var(--status-critical)", glyph: "cross" },
};

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
      <span className="status-chip" data-state={state}>
        <Glyph p={PANEL_GLYPHS[state]} attrs={{ "data-state": state }} />
        {PANEL_GLYPHS[state].label}
      </span>
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
