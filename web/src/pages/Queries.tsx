/* The Queries page (R10.6).
 *
 * R10.6's sentence names five things and this file owns three of them: the
 * table (which is `components/QueryTable.tsx`, its own file and its own test),
 * the **filter chips**, and the **search over normalized text**. The fourth,
 * the per-row sparkline, arrives with `Sparkline` — it reads R8.11's
 * `/insights/queries/series`, the same path as R10.7's detail chart, so the two
 * ship in one change and the column joins `QUERY_COLUMNS` there rather than
 * being advertised here with no renderer. The fifth, the saved-view concept, is
 * below.
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
import type { QueryResponse, QueryView } from "../api/types";
import { buildPath, type PageProps } from "../routes";
import type { SortDir, SortKey, TimeRange, UrlState } from "../state/urlState";
import { formatTimestamp } from "../lib/format";
import { ColumnMenu, useColumnVisibility } from "../components/ColumnMenu";
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

const RANGES: readonly TimeRange[] = ["1h", "24h", "7d", "30d"];

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
 *  A4), `Enable pg_stat_statements` is R3.15's product flow with no UI owner,
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
}

export function QueriesPage(props: QueriesPageProps) {
  const { state, onPatch } = props;

  // `props.cfg` is accepted and unused, exactly as `ClusterDetail.tsx` accepts
  // it: `client.ts` has no `fetchQueries` yet, so there is nothing to pass it
  // to. The swap, when A4 adds it, is
  //   const { state: data, refresh } = useQueries(props.cfg);
  //   const resp = props.fixtureOverride ?? data.queries;
  // — the same three lines `Fleet.tsx:35-37` are, and the reason the fixture
  // path is written as `?? toQueriesResponse(FIXTURE_QUERIES)` rather than as
  // the only source.
  const resp: QueryResponse = props.fixtureOverride ?? toQueriesResponse(FIXTURE_QUERIES);
  // There is no fetch to have a phase for yet, so the payload is always here.
  // `deriveQueryPanelState` still takes the phase rather than an implicit one,
  // because the moment `useQueries` lands the `loading` branch is live and the
  // page must not have to be re-derived to notice.
  const phase: FetchPhase = "ready";

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
  if (panel === "loading" || panel === "empty" || panel === "error" || panel === "unavailable") {
    return (
      <div className="page" data-page="queries">
        <PageHeader state={state} resp={resp} patch={patch} />
        <StatePanel state={panel} copy={QUERY_COPY} onAction={NO_ACTION} />
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
            {RANGES.map((r) => (
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
