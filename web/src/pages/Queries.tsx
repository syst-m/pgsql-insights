/* The Queries page (R10.6).
 *
 * R10.6's sentence names five things and this file owns three of them: the
 * table (which is `components/QueryTable.tsx`, its own file and its own test),
 * the **filter chips**, and the **search over normalized text**. The fourth,
 * the per-row sparkline, is `Sparkline` — it reads R8.11's
 * `/insights/queries/series`, the same path as R10.7's detail chart, so the two
 * shipped in one change and the column is in `QUERY_COLUMNS` here. The fifth,
 * the saved-view concept, is below.
 *
 * ── the three chips, and why only one is here ──────────────────────────────
 *
 * R10.6's chips are *database, user, tag*. They have three different sources:
 *
 *   database  on the row (`QueryView.database`, R3.2's own column). Built here.
 *   user      on `QueryDetailView` only. `api/types.ts:565` says so outright:
 *               "The list view keeps it off the row (R3.12 folds it into the
 *               tag) and the detail view shows it." So there is no set of users
 *               to build a chip from without fetching a detail per row.
 *   tag       on `TagView`, a different endpoint (`/insights/tags`).
 *
 * So the user and tag chips arrive with the tags payload rather than as an
 * invented local interface — which is the rule for this slot: a shape the DTO
 * doesn't have is an ask, not a local type. `QueryFilters` carries the two
 * fields that exist today and the type is exported so widening it is additive.
 *
 * ── the saved view ─────────────────────────────────────────────────────────
 *
 * R10.6's "saved-view concept" is scoped to *shared/cluster-scoped* saved
 * views; per-user ones are "impossible under R8.8's single shared bearer token
 * and are deferred with OIDC". Server-stored means a DTO, and there is no
 * `SavedView` in `api/types.ts`, so the control is the same kind of ask as the
 * two chips. `QueryUrlPatch.view` is the field it will write to.
 *
 * ── the not-installed screen ───────────────────────────────────────────────
 *
 * R10.27's "first-class screen … not an empty table with a tooltip" is
 * `components/ExtensionNotInstalled.tsx`. The page's part is to pick it for
 * `panel === "unavailable"` and hand it the cluster the URL names; how the page
 * tells *not installed* from *installed and quiet* is the `extensionInstalled`
 * prop's story, below.
 *
 * ── the URL ────────────────────────────────────────────────────────────────
 *
 * `PageProps<QueryResponse>` is A4's generic (commit 033919a) and exists for
 * exactly this page: the payload a page renders is per-page, and a union of
 * every payload in `routes.ts` would have to be widened by hand each phase.
 *
 * Two fields here are *patched but not yet serialised*, both because `urlState`
 * is A4's file and both are the ask on it:
 *
 * 1. `sort`. `UrlState.SortKey` is the fleet's ten keys, so
 *    `parseUrl("?sort=timesCalled")` runs `pick(..., SORT_KEYS, "health")`,
 *    gets `"health"`, `isQuerySortKey` rejects it, and the page falls back to
 *    its own default. The control works in-session (App.tsx:59's
 *    `{...current, ...patch}` keeps the patched value in React state) and is
 *    lost on back/reload. `QueryUrlPatch` widens it locally so the page
 *    compiles against today's type and needs no further change when the
 *    widening lands.
 * 2. `database` / `user` / `tag` / `view` — same story, `formatQuery` emits
 *    only the fields it declares.
 */

import { useCallback, useMemo, useState } from "react";

import { toQueriesResponse } from "../api/envelope";
import { FIXTURE_QUERIES } from "../api/fixtures";
import type { QueryResponse, QuerySeries, QuerySeriesEnvelope, QueryView } from "../api/types";
import { buildPath, type PageProps } from "../routes";
import { TIME_RANGES, type SortDir, type SortKey, type TimeRange, type UrlState } from "../state/urlState";
import { listParamsFrom, useQueries } from "../state/useQueries";
import { seriesParamsFor, useQuerySeries } from "../state/useQuerySeries";
import { formatTimestamp } from "../lib/format";
import { ColumnMenu, useColumnVisibility } from "../components/ColumnMenu";
import { ExtensionNotInstalled } from "../components/ExtensionNotInstalled";
import { QUERY_COLUMNS, QueryTable, isQuerySortKey, type QuerySortKey } from "../components/QueryTable";
import { DensityToggle, type Density } from "../components/VirtualList";
import {
  QUERY_COPY,
  StatePanel,
  copyFor,
  deriveQueryPanelState,
  type FetchPhase,
} from "../components/StatePanel";

/** What the page writes back to the URL.
 *
 *  `Omit`s the `sort` it widens rather than intersecting two declarations of
 *  it: `Partial<UrlState> & { sort?: SortKey | QuerySortKey }` intersects the
 *  two `sort` members into `SortKey & QuerySortKey`, which is `never` (the two
 *  unions do not overlap), and every `onPatch({ sort })` from then on fails to
 *  typecheck for a reason no reader would guess from reading the type.
 *
 *  The extra four fields are optional, which is what makes
 *  `Partial<UrlState>` assignable *to* this type and lets the component assert
 *  a `QueryUrlPatch` back down to `Partial<UrlState>` at the `onPatch` call
 *  site — see the two doc comments above for which of them `formatQuery`
 *  actually serialises today. */
export type QueryUrlPatch = Omit<Partial<UrlState>, "sort"> & {
  sort?: SortKey | QuerySortKey;
  database?: string;
  user?: string;
  tag?: string;
  /** R10.6's saved-view concept; see the header note. */
  view?: string;
};

/** R10.6's filter set, as far as the DTO supports it today (see the header's
 *  three-chip table). Exported so adding `user`/`tag` is a field added here and
 *  nothing else changes shape. */
export interface QueryFilters {
  /** R10.6's "search over normalized text" — the R3.5 template, not the
   *  fingerprint, which is what makes two `SELECT`s with different literals one
   *  row and therefore one searchable string. */
  q: string;
  /** "" means all databases. */
  database: string;
}

/** The search + database filter, as one pure function so the rules are testable
 *  without a DOM: a substring match on the template, and an exact database
 *  match. Case-insensitive on the query because a search box that makes the user
 *  match `SELECT` vs `select` is a search box people stop using. */
export function filterQueries(rows: readonly QueryView[], f: QueryFilters): QueryView[] {
  const q = f.q.trim().toLowerCase();
  return rows.filter((r) => {
    if (q !== "" && !r.query.toLowerCase().includes(q)) return false;
    if (f.database !== "" && r.database !== f.database) return false;
    return true;
  });
}

/** The database chip's option list, most-used first.
 *
 *  Built from the rows rather than from a `?databases=` endpoint, because the
 *  alternative is a request that R8.2 does not have. Sorting by count (ties
 *  alphabetical) makes the list stable across refetches, which an insertion-
 *  ordered `Map` iteration is not once the server paginates. */
export function databaseOptions(rows: readonly QueryView[]): { value: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const r of rows) counts.set(r.database, (counts.get(r.database) ?? 0) + 1);
  return [...counts.entries()]
    .map(([value, count]) => ({ value, count }))
    .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
}

/** The sort the table shows. `isQuerySortKey` is the single place the two
 *  vocabularies meet: a fleet key arriving here (which is every value today —
 *  see the header's note on `SortKey`) resolves to the column R10.6's default
 *  view is sorted by, not to a crash and not to an unsorted table. */
export function resolveSort(raw: string): QuerySortKey {
  return isQuerySortKey(raw) ? raw : "loadByTotalTime";
}

/** Clicking the active column flips it; a new column sorts the way that puts
 *  the interesting end first — names A→Z, every measurement high→low. Mirrors
 *  `Fleet.tsx`'s name/namespace rule exactly. */
export function nextDir(current: QuerySortKey, dir: SortDir, key: QuerySortKey): SortDir {
  if (current === key) return dir === "asc" ? "desc" : "asc";
  return key === "query" || key === "database" ? "asc" : "desc";
}

/** The panel's action button, for the three states that offer one.
 *
 *  Every one of them points at something this page does not have yet: `Retry`
 *  needs `fetchQueries` (`api/client.ts` has `fetchFleet` only — the ask on
 *  A4), `Enable pg_stat_statements` is R3.15's patch-and-verify half, which is
 *  Phase 2's backend work rather than a page action (the screen that offers it
 *  is `components/ExtensionNotInstalled.tsx`, and it takes this same no-op),
 *  and `Widen the range` is a header control whose effect needs the same
 *  missing fetch. The button renders because the copy names an action; wiring
 *  it to a no-op is what keeps it from being a dead control that looks wired,
 *  and the fetch is what replaces it. */
const NO_ACTION = () => {};

/** The database filter as it currently stands.
 *
 *  `UrlState` does not declare `database` yet (the ask on A4), but `App.tsx:59`
 *  merges a patch with `{ ...current, ...patch }`, so a patched value *is* in
 *  the live state object — it just isn't in the type, and `formatQuery` doesn't
 *  serialise it. Reading it through this cast is what makes the select hold the
 *  value that was chosen for the rest of the session instead of snapping back
 *  to "All databases" on the next render, and it needs no change at all when
 *  the field is declared: the expression is then just a plain property read. */
function currentDatabase(state: UrlState): string {
  return (state as UrlState & { database?: string }).database ?? "";
}

export interface QueriesPageProps extends PageProps<QueryResponse> {
  /** R3.1's per-database `pg_stat_statements` presence, for R10.27's
   *  not-installed screen.
   *
   *  Not on `QueryResponse`, and not on `PageProps` — it belongs to the cluster,
   *  and `dto/clusters.go` doesn't expose it (the ask on A1). It is a prop here
   *  rather than a field invented on the payload because R16.8 pins
   *  `api/types.ts` to the Go DTO field-for-field: adding a field to
   *  `QueryResponse` without adding it to `dto/queries.go` is a parity break,
   *  and adding it to `PageProps` is an edit to A4's file. Optional, and
   *  absent-means-unknown, so `routes.ts` mounting this component with plain
   *  `PageProps<any>` needs no change to pass. */
  extensionInstalled?: boolean;
  /** R8.11's batched series for the table's `Load sparkline` column, keyed by
   *  fingerprint.
   *
   *  The page fetches its own batch now — `useQuerySeries` over the fingerprints
   *  in the payload it is rendering, the batch-of-N `client.ts:384-385` names as
   *  this column's source — and this prop is the caller's override, the same role
   *  `fixtureOverride` plays for the rows. It cannot become a field on
   *  `QueryResponse`: R16.8 pins `web/src/api/types.ts` to the Go DTO and the DTO
   *  has no series field to inline (the detail half documents the import-cycle
   *  ruling). Optional and partial by nature — R8.11's batch endpoint returns one
   *  entry per query that has data, so a 50-row page with 37 series is normal
   *  and the 13 rows without one render `—`. */
  series?: Record<string, QuerySeries>;
}

/** Index an R8.11 envelope by fingerprint, which is the shape `QueryTable`'s
 *  `series` prop takes.
 *
 *  The envelope carries one entry per fingerprint that has data
 *  (`client.ts:362-367`), so indexing a 50-row page's batch to 37 entries is the
 *  normal answer. A later entry for the same fingerprint wins over an earlier
 *  one, which is what R5.7's per-pod entries (`QuerySeries.pod`) make possible:
 *  the table row asks for the cluster-aggregated one, and the last one in the
 *  array is as good a guess as any until the table grows a per-pod column. */
export function seriesByFingerprint(
  env: QuerySeriesEnvelope | null,
): Record<string, QuerySeries> {
  const byFingerprint: Record<string, QuerySeries> = {};
  for (const entry of env?.data ?? []) byFingerprint[entry.fingerprint] = entry;
  return byFingerprint;
}

export function QueriesPage(props: QueriesPageProps) {
  const { state, onPatch } = props;

  // The swap the previous comment on these lines documented for three rounds,
  // and it is `Fleet.tsx:45-47` verbatim in shape: the page calls the hook, the
  // prop still wins, and the contract fixture is what's on screen before the
  // first response lands. `listParams` is memoised because
  // `useQueries.ts:22-28` makes params identity load identity — "a caller that
  // builds a fresh object every render refetches every render", and since `load`
  // calls `setState` that is a render loop rather than merely an extra request.
  const listParams = useMemo(() => listParamsFrom(state), [state]);
  const { state: data, refresh } = useQueries(props.cfg, listParams);
  const resp: QueryResponse =
    props.fixtureOverride ?? data.queries ?? toQueriesResponse(FIXTURE_QUERIES);
  // The phase the comment above this one insisted on having as a parameter
  // instead of an implicit: with a prop the page is showing a named fixture, so
  // it is `ready` whatever the fetch is doing; without one it is the hook's.
  const phase: FetchPhase = props.fixtureOverride !== undefined ? "ready" : data.phase;

  const patch = useCallback(
    (p: QueryUrlPatch) => onPatch(p as Partial<UrlState>),
    [onPatch],
  );

  const panel = useMemo(
    () => deriveQueryPanelState({ phase, queries: resp, extensionInstalled: props.extensionInstalled }),
    [phase, resp, props.extensionInstalled],
  );

  const sort = resolveSort(state.sort);
  const filters = useMemo<QueryFilters>(
    () => ({ q: state.q, database: currentDatabase(state) }),
    [state],
  );
  const rows = useMemo(() => filterQueries(resp.queries, filters), [resp.queries, filters]);
  const databases = useMemo(() => databaseOptions(resp.queries), [resp.queries]);

  // R8.11's batch for the sparkline column: every fingerprint in the served
  // payload, in one call (`client.ts:384-385`'s batch-of-N). It is fed from
  // `resp.queries` and not from `rows` for the reason the banner below draws for
  // the same pair — a filter is a view over the served set, so typing in the
  // search box must not change what was asked for. The params are memoised for
  // the load-identity reason above. An empty list is `InvalidRequest`
  // (`client.ts:362-367`), so the call only means something once the payload has
  // rows; its error is not this page's error state (the rows' own is, via
  // `panel`), and the column degrades to `—`.
  const seriesParams = useMemo(
    () => seriesParamsFor(resp.queries.map((row) => row.fingerprint), state),
    [resp.queries, state],
  );
  const { state: seriesData } = useQuerySeries(props.cfg, seriesParams);
  const series = useMemo(
    () => props.series ?? seriesByFingerprint(seriesData.series),
    [props.series, seriesData.series],
  );

  const columns = useColumnVisibility({ viewId: "queries", columns: QUERY_COLUMNS });

  // The one piece of local state the page owns. R10.16's URL contract lists
  // page, cluster, row, tab, sort, dir, filters and range — density is not on
  // it, and `Fleet.tsx`'s "the page owns no state of its own beyond the data"
  // is about *navigable* state. Density is a rendering preference with a
  // persisted-settings story (R10.20's) and no URL story, so it lives here.
  const [density, setDensity] = useState<Density>("compact");

  // R10.12's full-panel states replace the body with header + panel, as Fleet
  // does — with one asymmetry. `unavailable` is on this list and not on
  // Fleet's, and that is R10.27's wording: the not-installed screen is "a
  // first-class screen, designed and tested — not an empty table with a
  // tooltip". A banner over a zero-row table is exactly the tooltip-shaped
  // answer that sentence rules out, and with the extension absent there is no
  // table to put a banner over anyway.
  //
  // `unavailable` renders its own component rather than the generic panel
  // because R10.27 asks for a screen and `StatePanel` is a title, a sentence and
  // a button. The other three states keep the generic renderer, and the
  // replacement keeps `state-panel` + `data-state="unavailable"` so R16.7's
  // suite still keys the screen by state name.
  if (panel === "loading" || panel === "empty" || panel === "error" || panel === "unavailable") {
    return (
      <div className="page" data-page="queries">
        <PageHeader state={state} resp={resp} patch={patch} />
        {panel === "unavailable" ? (
          <ExtensionNotInstalled cluster={state.cluster} onEnable={NO_ACTION} />
        ) : (
          <StatePanel state={panel} copy={QUERY_COPY} onAction={refresh} />
        )}
      </div>
    );
  }

  // The banner half: `partial` only, and from the response rather than from
  // `rows` — a filter is a view over the served set, not a smaller response
  // (the same distinction `Fleet.tsx:66` draws).
  const bannerCopy = copyFor(panel, { meta: resp.meta, data: resp.queries }, QUERY_COPY);

  return (
    <div className="page" data-page="queries">
      <PageHeader state={state} resp={resp} patch={patch} />

      {bannerCopy !== null && (
        <div className="banner" role="status">
          <strong>{bannerCopy.title}</strong>
          <span>{bannerCopy.body}</span>
        </div>
      )}

      <FilterRow filters={filters} databases={databases} patch={patch} />

      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Queries</h2>
          <div className="card-actions">
            <span className="card-meta num">{rows.length} shown</span>
            <ColumnMenu columns={QUERY_COLUMNS} visible={columns.visible} onChange={columns.setVisible} />
            <DensityToggle density={density} onChange={setDensity} />
          </div>
        </div>
        <QueryTable
          rows={rows}
          columns={columns.visible}
          series={series}
          sort={sort}
          dir={state.dir}
          onSort={(key) => patch({ sort: key, dir: nextDir(sort, state.dir, key) })}
          onOpen={(fingerprint) => patch({ page: "query-detail", row: fingerprint })}
          density={density}
        />
      </section>
    </div>
  );
}

/** The page header: breadcrumb, title, the R10.21 stamp, and the range control.
 *
 *  The cluster id comes from the URL. When the page is rendered without one (a
 *  direct render, or the `NotImplemented` → page swap mid-flight) it falls back
 *  to the rows' own cluster, because every row carries `namespace`/`cluster`
 *  and a page that shows cluster A's rows under an empty breadcrumb is lying
 *  about which cluster it is on. */
function PageHeader({
  state,
  resp,
  patch,
}: {
  state: UrlState;
  resp: QueryResponse;
  patch: (p: QueryUrlPatch) => void;
}) {
  const first = resp.queries[0];
  const cluster = state.cluster ?? (first === undefined ? "" : `${first.namespace}/${first.cluster}`);

  return (
    <header className="page-head">
      <div>
        <p className="breadcrumb">
          <a className="cluster-link" href="/">
            Fleet
          </a>
          <span aria-hidden> / </span>
          {cluster !== "" && (
            <>
              <a className="cluster-link" href={buildPath("cluster", cluster, null)}>
                {cluster}
              </a>
              <span aria-hidden> / </span>
            </>
          )}
          <span>Queries</span>
        </p>
        <h1 className="page-title">Queries</h1>
        <p className="page-meta" title={`${resp.meta.generatedAt} UTC`}>
          {/* R10.21: local zone, zone labelled, absolute UTC on hover. The stamp
              is the payload's own `meta.generatedAt`, not one the page made up. */}
          Data as of {formatTimestamp(resp.meta.generatedAt)}
        </p>
      </div>
      <div className="page-actions">
        <label className="field">
          <span className="field-label">Range</span>
          <select value={state.range} onChange={(e) => patch({ range: e.target.value as TimeRange })}>
            {TIME_RANGES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
      </div>
    </header>
  );
}

/** R10.6's search box and database chip, in the one-row filter band
 *  interaction.md prescribes (above everything it scopes). */
function FilterRow({
  filters,
  databases,
  patch,
}: {
  filters: QueryFilters;
  databases: { value: string; count: number }[];
  patch: (p: QueryUrlPatch) => void;
}) {
  return (
    <div className="filter-row">
      <label className="field grow">
        <span className="field-label">Search</span>
        <input
          type="search"
          value={filters.q}
          placeholder="normalized query text"
          onChange={(e) => patch({ q: e.target.value })}
        />
      </label>

      <label className="field">
        <span className="field-label">Database</span>
        <select value={filters.database} onChange={(e) => patch({ database: e.target.value })}>
          <option value="">All databases</option>
          {databases.map((d) => (
            <option key={d.value} value={d.value}>
              {d.value} ({d.count})
            </option>
          ))}
        </select>
      </label>
    </div>
  );
}
