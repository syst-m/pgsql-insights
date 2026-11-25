/* The Tags page — R10.3's ninth page, R3.12's answer, served by R8.2's
 * `GET /api/v1/insights/tags`.
 *
 * R10.3 (`requirements.md:414`) names the page in its list of fourteen; R3.12
 * (`:317`) is what the page is *about*, and this file is that requirement's UI
 * half. The route entry is `routes.ts:256-266` (`page: "tags"`,
 * `requirement: "R10.3 · R3.12 · R8.2"`, `endpoint: "GET /api/v1/insights/tags"`,
 * `path: "/c/:ns/:name/tags"`, `inNav: true`); its `component:` is `null`
 * until A4 flips it, which is the same half-pair as `99f37f0`/`a5e5be0` were
 * for Active Queries — the page file from this slot, the import and the
 * `IMPLEMENTED` entry from theirs. Until that flip this file is unimported, so
 * esbuild drops it and `web/dist` is byte-identical; the page's own suite is
 * what runs it today.
 *
 * ── what a tag is, and which columns the answer needs ──────────────────────
 *
 * R3.12 defines a tag in two families, and `api/types.ts:640-665`'s `TagView`
 * is that definition typed:
 *   (a) Postgres' own, always available — `application_name`, `usename`,
 *       `datname` → `application`, `user`, `database`;
 *   (b) the opt-in `/* pgsi:action=…,route=…,controller=…,framework=…,
 *       dbdriver=… *\/` comment grammar, promoted to tag dimensions → `action`,
 *       `route`, `controller`, `framework`, `dbDriver`.
 * Plus `tagHash` (R3.12's `tag_hash`, the hash of the dimension set and R8.2's
 * path key) and R3.7's load rollup over every query carrying the tag — the
 * eight `LoadColumns` fields plus `lockWaitMs`, which the rollup has and the
 * per-query row does not (`types.ts:661-664`).
 *
 * The fixture's three rows are exactly the three cases R3.12 allows, and the
 * table renders all eighteen columns so the difference between them is visible
 * rather than inferred:
 *   - `openwebui` — a full comment: all five (b) dimensions present;
 *   - `gateway` — no comment at all: only the three (a) dimensions. Its five
 *     comment-derived cells (`action`, `route`, `controller`, `framework`,
 *     `dbDriver`) render `—` rather than being omitted — the other nine columns
 *     are populated, `lockWaitMs` among them, so the row is sparse in the (b)
 *     family only — because "the statement carried no comment" and "this page
 *     has no such column" are different answers and omitting the cell is the
 *     one thing that conflates them;
 *   - `cookbook` — a partial comment: `action` and `route` present,
 *     `controller`/`framework`/`dbDriver` absent. A table that rendered only
 *     the dimensions a row has would show this row as three columns wide.
 *
 * `TAG_COLUMNS` is exhaustive by construction: its eighteen keys are `TagView`'s
 * eighteen json tags, the same set `client.ts:467-476`'s `TagsParams` docblock
 * counts when it says the route "lists all eighteen of TagView's json tags as
 * sort keys". `ColumnMenu.DEFAULTS["tags"]` shows eight of them; the menu is
 * what makes the other ten reachable, which is R10.20's contract, not this
 * file's.
 *
 * ── why the load cells are numbers here and bars on the Queries table ──────
 *
 * `QueryTable.loadCellFor(row, key)` returns a bar partly because a query row
 * carries `pgMajor`, and R3.2's IO-wait derivation differs between PG16 and
 * PG17 — the bar's tooltip has to name which one produced the number. `TagView`
 * has no `pgMajor`: a tag's rollup spans every query carrying it, which can
 * span majors, and the dto does not say which derivation the aggregate used. So
 * the load cells here are the same formatted values (`formatPercent`,
 * `formatDurationMs`, `formatNumber`) without a bar — a bar whose tooltip
 * cannot name its derivation would render a claim the payload does not support.
 *
 * ── rows are not links ─────────────────────────────────────────────────────
 *
 * R10.3's fourteen pages contain no tag detail page, and R8.2's
 * `/insights/tags/{tag_hash}` has no row in `routes.ts`, so a row has nowhere
 * to go. The affordance R3.12 does specify runs the other way: `QueryUrlPatch.tag`
 * is Queries' filter-by-tag chip, so "show me this tag's queries" lives on the
 * Queries page. R10.3's page is the list.
 *
 * ── the fetch, and why it is inline ────────────────────────────────────────
 *
 * `web/src/state/` is A4's slot and their half of this pair is routes.ts +
 * routes.test.tsx + dist, so there is no `useTags` hook to import, and
 * restating one inside `pages/` would be a second copy of a pattern the state
 * layer owns. `useTagsFetch` below is `useActiveQueries.ts`'s body —
 * abort-previous, abort-on-unmount, R10.22's hold-previous-on-error, `Phase`
 * *imported* from `../state/useFleet` rather than restated — written so that
 * extracting it to `state/useTags.ts` later is a cut-paste rather than a
 * rewrite. The standing ask on `web/src/api/` is otherwise unchanged by this
 * file: `fetchTags` exists (`client.ts:477`), the hook does not.
 *
 * No polling. R10.8 names 15 s for Active Queries and `useQueries.ts:111`
 * defaults to 30 s; R3.7 and R8.2 name no interval at all for the tag rollup,
 * and a page that picks one is inventing it. The R10.21 stamp is this page's
 * freshness answer.
 *
 * ── the empty state, and the field it is waiting on ────────────────────────
 *
 * The empty branch is not a bespoke string: it is `StatePanel` with `TAGS_COPY`
 * (the fourth copy table in `components/StatePanel.tsx`) chosen by
 * `deriveTagsPanelState` (the fourth derivation there, for the fourth payload
 * type), and `EmptyStateCatalogue.test.tsx` gains a row for it marked
 * `from: "page"` — R10.28's seven enumerated states at `:439` are cluster-level
 * and this is a page-level one, so the catalogue is the one place both kinds
 * are listed.
 *
 * `extensionInstalled` is the interim `pages/Queries.tsx:45` carries too. The
 * tags envelope has no presence field — R3.1's per-database detection is
 * `dto.ExtensionPresenceView`, which A1 added at `dto/queries.go:268` for the
 * *queries* route — so `unavailable` renders on an explicit `false` and every
 * response today is *unknown*, which renders the plain empty.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement } from "react";

import { fetchTags, type ClientConfig, type TagsParams } from "../api/client";
import { TAG_NOUN } from "../api/envelope";
import { FIXTURE_TAGS } from "../api/fixtures";
import type { Envelope, TagView } from "../api/types";
import { buildPath, type PageProps } from "../routes";
import { TIME_RANGES, type TimeRange, type UrlState } from "../state/urlState";
import type { Phase } from "../state/useFleet";
import { formatDurationMs, formatNumber, formatPercent, formatTimestamp, NULL_VALUE } from "../lib/format";
import { ColumnMenu, type ColumnDef, useColumnVisibility } from "../components/ColumnMenu";
import { ExtensionNotInstalled } from "../components/ExtensionNotInstalled";
import { StatePanel, TAGS_COPY, copyFor, deriveTagsPanelState, type FetchPhase } from "../components/StatePanel";

// The subject's noun for R10.22's banner comes from `api/envelope.ts:800`
// (A4's `cdf81c9`, 2026-11-24) — a value import, not the `import type` this
// file first carried: with `verbatimModuleSyntax: true` a type-only specifier
// emits no runtime import, so a local `const TAG_NOUN: BannerNoun` annotated
// from a type import was a second copy of a value the envelope file exports.
// The `copyFor(…, TAGS_COPY, TAG_NOUN)` call at `:203` is unchanged by the swap
// — the two definitions were byte-identical.

/** The page's column set: `TagView`'s eighteen json tags, in the order the
 *  type declares them — identity first (R3.12's `tagHash`, then the three (a)
 *  dimensions, then the five (b) dimensions), then R3.7's eight rollup fields
 *  plus `lockWaitMs`.
 *
 *  `tagHash` is the one `required` column: it is R8.2's path key, the row's
 *  identity, and the only cell a reader needs in order to use the row at all
 *  (Queries' `tag` chip filters by it). Every other column is switchable,
 *  because R10.20's contract is that the menu lists what the table can render
 *  and the default set is a documented choice, not a limit. */
export const TAG_COLUMNS: readonly ColumnDef[] = [
  // R3.12's identity.
  { key: "tagHash", label: "Tag hash", required: true },
  // R3.12's (a) dimensions — Postgres' own, always available.
  { key: "application", label: "Application" },
  { key: "database", label: "Database" },
  { key: "user", label: "User" },
  // R3.12's (b) dimensions — the `pgsi:` comment grammar, absent without a comment.
  { key: "action", label: "Action" },
  { key: "route", label: "Route" },
  { key: "controller", label: "Controller" },
  { key: "framework", label: "Framework" },
  { key: "dbDriver", label: "DB driver" },
  // R3.7's rollup over every query carrying the tag (the eight LoadColumns…).
  { key: "loadByTotalTime", label: "Load by total time" },
  { key: "pctLoadByQueries", label: "% load by Queries" },
  { key: "avgExecTimeMs", label: "Avg exec time" },
  { key: "totalExecTimeMs", label: "Total exec time" },
  { key: "avgRowsReturned", label: "Avg rows returned" },
  { key: "timesCalled", label: "Times called" },
  { key: "loadByCPUMs", label: "Load by CPU" },
  { key: "loadByIOWaitMs", label: "Load by IO wait" },
  // …plus the one field the rollup has and the per-query row does not.
  { key: "lockWaitMs", label: "Load by lock wait" },
];

export type TagsPageProps = PageProps<Envelope<TagView>> & {
  /** R3.1's per-database presence, for the R10.27 screen. See the header's
   *  last section: the tags envelope does not carry it, so it is a prop here
   *  exactly as it is one on `Queries.tsx:45`, with the same default of
   *  *unknown*. */
  extensionInstalled?: boolean;
};

export function TagsPage(props: TagsPageProps): ReactElement {
  const { state } = props;

  // The params-identity contract every page in this tree observes: the hook
  // puts `params` in `load`'s dep list, so a fresh object per render is a
  // refetch per render, and since `load` calls `setState` that is a render
  // loop, not merely an extra request. `Fleet.tsx:45-47` states it; every page
  // since has memoised for the same reason.
  const params = useMemo(() => tagsParamsFrom(state), [state]);

  // Every hook is above this line, because `tagsParamsFrom` can return `null`
  // and the early return below has to come after them — the rule
  // `Fleet.tsx:64-65` states and `QueryDetail.tsx`'s no-such-query frame follows.
  const { state: data, refresh } = useTagsFetch(props.cfg, params);

  if (params === null) return <NoCluster {...props} />;

  // The prop still wins over the fetched payload, so the visual suite renders a
  // named fixture without a fetch; before the first response lands the contract
  // fixture is what is on screen, as on every other page.
  const resp: Envelope<TagView> = props.fixtureOverride ?? data.tags ?? FIXTURE_TAGS;
  const phase: FetchPhase = props.fixtureOverride !== undefined ? "ready" : data.phase;

  const panel = deriveTagsPanelState({
    phase,
    tags: resp,
    error: data.error ?? undefined,
    extensionInstalled: props.extensionInstalled,
  });
  const columns = useColumnVisibility({ viewId: "tags", columns: TAG_COLUMNS });
  // R10.22's wording is composed from the payload rather than written here: the
  // rollup that served 17 of 23 tags has to name the six, which is what
  // `copyFor`'s `source` argument does and a fixed sentence cannot.
  const bannerCopy = copyFor(panel, { meta: resp.meta, data: resp.data }, TAGS_COPY, TAG_NOUN);

  // R10.27's first-class screen, then R10.12's named states, then the table.
  // `unavailable` renders the component rather than a panel here as on
  // `Queries.tsx`: the not-installed case is a designed screen with the flow
  // and the CLI line on it, and an empty table with a tooltip is what R10.27
  // explicitly rules out.
  if (panel === "unavailable") {
    return (
      <div className="page" data-page="tags">
        <PageHeader state={state} resp={resp} onPatch={props.onPatch} />
        <ExtensionNotInstalled cluster={params.cluster} />
      </div>
    );
  }

  if (panel === "loading" || panel === "empty" || panel === "error") {
    return (
      <div className="page" data-page="tags">
        <PageHeader state={state} resp={resp} onPatch={props.onPatch} />
        <StatePanel
          state={panel}
          error={data.error ?? undefined}
          onAction={refresh}
          copy={TAGS_COPY}
        />
      </div>
    );
  }

  return (
    <div className="page" data-page="tags">
      <PageHeader state={state} resp={resp} onPatch={props.onPatch} />

      {bannerCopy !== null && (
        <div className="banner" role="status">
          <strong>{bannerCopy.title}</strong>
          <span>{bannerCopy.body}</span>
        </div>
      )}

      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Tags</h2>
          <div className="card-actions">
            <span className="card-meta num">{resp.data.length} tags</span>
            <ColumnMenu columns={TAG_COLUMNS} visible={columns.visible} onChange={columns.setVisible} />
          </div>
        </div>

        <table className="data" data-testid="tags-table">
          <caption className="sr-only">
            Tags reported for {params.namespace}/{params.cluster}
          </caption>
          <thead>
            <tr>
              {columns.visible.map((key) => (
                <th key={key} scope="col" className={numericCol(key) ? "num" : undefined}>
                  {labelFor(key)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {resp.data.map((row) => (
              <TagRow key={row.tagHash} row={row} columns={columns.visible} />
            ))}
          </tbody>
        </table>
      </section>
    </div>
  );
}

/* ── header ────────────────────────────────────────────────────────────────── */

/** Breadcrumb, title, the R10.21 stamp, and R10.15's range control.
 *
 *  The range is here because the route takes it: `TagsParams` has `from`/`to`
 *  (`client.ts:467-476`), so the window is part of what the page asks for, and
 *  R10.15's five presets are the only vocabulary for stating one. Changing it
 *  patches the URL, which re-derives `params`, which refetches — the same chain
 *  `Queries.tsx`'s header drives. */
function PageHeader({
  state,
  resp,
  onPatch,
}: {
  state: UrlState;
  resp: Envelope<TagView>;
  onPatch: (p: Partial<UrlState>) => void;
}): ReactElement {
  return (
    <header className="page-head">
      <div>
        <p className="breadcrumb">
          <a className="cluster-link" href="/">
            Fleet
          </a>
          <span aria-hidden> / </span>
          {state.cluster !== null && (
            <>
              <a className="cluster-link" href={buildPath("cluster", state.cluster, null)}>
                {state.cluster}
              </a>
              <span aria-hidden> / </span>
            </>
          )}
          <span>Tags</span>
        </p>
        {/* The exact nav label, which `routes.ts:259`'s `title` is and
            `routes.test.tsx:52-67`'s `R10_3_TITLES` asserts. */}
        <h1 className="page-title">Tags</h1>
        <p className="page-meta" title={`${resp.meta.generatedAt} UTC`}>
          {/* R10.21: the stamp is the payload's own `meta.generatedAt`, rendered
              in the local zone and labelled, with the absolute UTC on hover. */}
          Data as of {formatTimestamp(resp.meta.generatedAt)}
        </p>
      </div>
      <div className="page-actions">
        <label className="field">
          <span className="field-label">Range</span>
          <select
            value={state.range}
            onChange={(e) => onPatch({ range: e.target.value as TimeRange })}
          >
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

/* ── one row ───────────────────────────────────────────────────────────────── */

function TagRow({
  row,
  columns,
}: {
  row: TagView;
  /** The visible column keys, in order — `useColumnVisibility`'s output, the
   *  same shape `QueryTableProps.columns` takes. */
  columns: readonly string[];
}): ReactElement {
  return (
    <tr data-tag-hash={row.tagHash}>
      {columns.map((key) => (
        <td key={key} className={numericCol(key) ? "num" : undefined}>
          {cell(row, key)}
        </td>
      ))}
    </tr>
  );
}

/** The cell a column renders for one tag.
 *
 *  Exported and pure for the same reason `QueryTable` exports `textCell`: the
 *  formatting rules are then assertable without a table. The rule that matters
 *  here is the absent-dimension case — every optional `TagView` field renders
 *  `NULL_VALUE` when missing, which is what makes the fixture's no-comment row
 *  (`gateway`) and its partial-comment row (`cookbook`) readable as facts.
 *
 *  The load columns are plain formatted values, not bars — see the header's
 *  "why the load cells are numbers" section. */
export function cell(row: TagView, key: string): ReactElement | string {
  switch (key) {
    case "tagHash":
      // 64 lowercase hex (`types.ts:641-644`). The full string is the value and
      // the full string is rendered — a page-invented shortening would be a
      // second formatting rule for the field R8.2 uses as a path key, and the
      // `trunc` class is what bounds the column instead.
      return (
        <span className="num trunc" title={row.tagHash}>
          {row.tagHash}
        </span>
      );
    case "application":
      return row.application ?? NULL_VALUE;
    case "database":
      return row.database ?? NULL_VALUE;
    case "user":
      return row.user ?? NULL_VALUE;
    case "action":
      return row.action ?? NULL_VALUE;
    case "route":
      return row.route ?? NULL_VALUE;
    case "controller":
      return row.controller ?? NULL_VALUE;
    case "framework":
      return row.framework ?? NULL_VALUE;
    case "dbDriver":
      return row.dbDriver ?? NULL_VALUE;
    case "loadByTotalTime":
      // A percentage of the cluster's total load (`LoadColumns`, types.ts:430-433).
      return formatPercent(row.loadByTotalTime);
    case "pctLoadByQueries":
      return formatPercent(row.pctLoadByQueries);
    case "avgExecTimeMs":
      return formatDurationMs(row.avgExecTimeMs);
    case "totalExecTimeMs":
      return formatDurationMs(row.totalExecTimeMs);
    case "avgRowsReturned":
      // One decimal, because it is a mean over an R3.3 delta — the same reason
      // `textCell` gives for the query row (`QueryTable.tsx:273-275`): 12.5, not 12.
      return formatNumber(row.avgRowsReturned, 1);
    case "timesCalled":
      return formatNumber(row.timesCalled);
    case "loadByCPUMs":
      return formatDurationMs(row.loadByCPUMs);
    case "loadByIOWaitMs":
      return formatDurationMs(row.loadByIOWaitMs);
    case "lockWaitMs":
      return formatDurationMs(row.lockWaitMs);
    default:
      return NULL_VALUE;
  }
}

/** The columns right-aligned as numbers. `tagHash` is in the set because it is
 *  a hex identifier and every other identifier cell in this tree (the
 *  fingerprint, the plan hash) is right-aligned; the eight rollup columns are
 *  numbers; the eight dimension columns are text. */
const NUMERIC_COLS = new Set([
  "tagHash",
  "loadByTotalTime",
  "pctLoadByQueries",
  "avgExecTimeMs",
  "totalExecTimeMs",
  "avgRowsReturned",
  "timesCalled",
  "loadByCPUMs",
  "loadByIOWaitMs",
  "lockWaitMs",
]);

function numericCol(key: string): boolean {
  return NUMERIC_COLS.has(key);
}

/** A column key's label. `useColumnVisibility` hands back keys and the table
 *  needs the label, so the lookup lives beside the list that defines both. */
function labelFor(key: string): string {
  return TAG_COLUMNS.find((c) => c.key === key)?.label ?? key;
}

/* ── params ────────────────────────────────────────────────────────────────── */

/** Each preset's window in seconds — the five `TIME_RANGES` (urlState.ts:141)
 *  enumerates.
 *
 *  The second copy of this map: `useQueries.ts:63-69` has the first, and it is
 *  module-private there. The ask on `web/src/state/` is to export it (or move
 *  it beside `TIME_RANGES`, which is the file that defines the set and so the
 *  file that should own its durations); until then a page that needs a window
 *  from a preset needs the number, and the alternative to a local const is a
 *  cross-page import from a sibling page's file. The `Record<TimeRange, number>`
 *  type is what makes a sixth preset a compile error here rather than an
 *  `undefined` in a URL. */
const RANGE_SECONDS: Record<TimeRange, number> = {
  "1h": 3_600,
  "6h": 21_600,
  "1d": 86_400,
  "7d": 604_800,
  "30d": 2_592_000,
};

/** URL state → the params `fetchTags` takes, as a pure function so the clock is
 *  an argument and not a hidden dependency (a bare `new Date()` inside the hook
 *  would put a value that changes every render in `load`'s deps and defeat the
 *  memoise every page's docblock is about).
 *
 *  `null` when the URL names no cluster or a malformed pair. That is stricter
 *  than `listParamsFrom` (useQueries.ts:92-106), which returns `{}` for a
 *  cluster-less URL because the queries route has a fleet-wide form; this
 *  route's row is `scope: "cluster"` (`routes.ts:256-266`) and `TagsParams`
 *  takes a `namespace`/`cluster` pair, so there is no cluster-less form of this
 *  page to fall back to and a page that cannot name its cluster should say so
 *  rather than fetch a fleet-wide rollup and label it with a cluster's name.
 *  The malformed-pair rule is `activeParamsFrom`'s (useActiveQueries.ts:88-96)
 *  verbatim: a pair needs a non-empty name on both sides of the slash, so
 *  "noslash", "ns/" and "/name" are all `null`.
 *
 *  `namespace` comes from the pair, not from `state.ns`: the pair is
 *  unambiguous, and `state.ns` is the *fleet* table's multi-namespace filter,
 *  which `listParamsFrom` can only honour when exactly one ns is selected
 *  (useQueries.ts:96-98). With a cluster named there is nothing to choose.
 *
 *  `sort` is deliberately not mapped, for the reason `listParamsFrom` gives at
 *  useQueries.ts:107-113 in a different vocabulary: `UrlState.SortKey` names
 *  the *fleet table's* columns (`health`, `tps`, `diskPct`) and none of them is
 *  a tag column, so forwarding it would ask this route to sort by a field it
 *  does not have. The route's own sort vocabulary is `TagView`'s eighteen json
 *  tags (client.ts:467-476), which `UrlState` has no field for. `db` is the
 *  other unmapped field with no `UrlState` source, and `limit`/`cursor` are
 *  paging, which no page owns yet.
 *
 *  The return type narrows `namespace` and `cluster` from optional to required
 *  (`TagsParams & { namespace: string; cluster: string }`) because the function
 *  always sets both when it returns a value at all. `TagsParams` makes every
 *  field optional because the *route* makes every param optional; a caller that
 *  has to handle "params but no cluster" is handling a case this function cannot
 *  produce, and the page's R10.27 branch needs a `string` for the cluster name.
 *  It is assignable to `TagsParams` everywhere a `TagsParams` is wanted, so
 *  `fetchTags` and `useTagsFetch` see the type they declare. */
export function tagsParamsFrom(
  state: UrlState,
  now: Date = new Date(),
): (TagsParams & { namespace: string; cluster: string }) | null {
  if (state.cluster === null) return null;

  const slash = state.cluster.indexOf("/");
  if (slash <= 0 || slash === state.cluster.length - 1) return null;

  const seconds = RANGE_SECONDS[state.range];
  return {
    namespace: state.cluster.slice(0, slash),
    cluster: state.cluster.slice(slash + 1),
    from: trimMs(new Date(now.getTime() - seconds * 1_000)),
    to: trimMs(now),
  };
}

/** `2026-11-23T12:00:00.000Z` → `2026-11-23T12:00:00Z`, matching `time.RFC3339`
 *  as `internal/api/series.go:178` formats it (R10.21: every timestamp the API
 *  emits is RFC 3339 UTC). The second copy of this one too — useQueries.ts:106-109. */
function trimMs(d: Date): string {
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/* ── the fetch ─────────────────────────────────────────────────────────────── */

interface TagsFetchState {
  phase: Phase;
  tags: Envelope<TagView> | null;
  error: string | null;
  /** True when a refresh is running over an existing payload (R10.22). */
  refreshing: boolean;
}

export interface UseTags {
  state: TagsFetchState;
  /** Force a refresh (the error panel's Retry calls it; R16.7's suite calls it). */
  refresh: () => void;
}

/** The subject a `TagsParams` names: the cluster pair, and nothing else.
 *
 *  `activeKey` (useActiveQueries.ts:73-75) in another vocabulary — the pair,
 *  not the window — because the window is a query parameter and the subject is
 *  the cluster: changing the range refetches the same cluster, and the rows that
 *  belong to that cluster are the ones worth holding while the new frame is in
 *  flight. Exported so the suite can name the key it is testing. */
export function tagsKey(params: TagsParams): string {
  return `${params.namespace ?? ""}/${params.cluster ?? ""}`;
}

/** `fetchTags` (client.ts:477) behind R10.22's refetch envelope.
 *
 *  `useActiveQueries.ts`'s body, inlined for the reason the header gives: the
 *  state layer is A4's and this page's payload is the fourth one no hook there
 *  serves. Four rules carry across verbatim, and they are the four that make a
 *  fetch correct rather than merely working:
 *    - abort the previous request when a new one starts, and abort on unmount,
 *      so a slow response for the cluster you navigated away from cannot
 *      overwrite the one you are on;
 *    - R10.22's hold-previous pair — a failed refresh keeps the last good frame
 *      and shows the banner instead of replacing it with an error screen
 *      (`phase: held === null ? "error" : "ready"`);
 *    - `err instanceof Error ? err.message : String(err)`, so a
 *      `ContractError` surfaces as its own message instead of as
 *      `[object Object]` swallowed into a blank table (the R16.8 bug class);
 *    - `params` in `load`'s dep list, which is why the caller memoises it.
 *
 *  No `pollMs`: see the header. The `refresh` the error panel's Retry calls is
 *  the same `load`, so the button in `TAGS_COPY.error` does what it says. */
export function useTagsFetch(cfg: ClientConfig, params: TagsParams | null): UseTags {
  const [state, setState] = useState<TagsFetchState>({
    phase: "idle",
    tags: null,
    error: null,
    refreshing: false,
  });
  const abort = useRef<AbortController | null>(null);
  /** The payload and the subject it belongs to, as one value (R10.22).
   *
   *  Two jobs, one ref. It lets `load` read the previous frame without putting
   *  `state.tags` in `load`'s own dep list — a value `load` writes must not be
   *  a dep of `load`, or every fetch produces a new `load`, the effect re-runs,
   *  and the page refetches forever. And the `key` half is what separates the
   *  two cases `params` identity conflates: the caller memoises with
   *  `useMemo(..., [state])` (`:178`), which keys on the whole `UrlState`, so
   *  patching *any* other URL param yields a new `params` for the same cluster.
   *  Same subject → hold the rows while the refetch is in flight; different
   *  subject → drop them, because cluster a's rows under cluster b's title is
   *  the failure R10.22's key exists to prevent. The abort pair stops a stale
   *  response from *overwriting* the new one; only the key stops it from
   *  *displaying* under the new title. */
  const last = useRef<{ key: string | null; tags: Envelope<TagView> | null }>({
    key: null,
    tags: null,
  });

  const load = useCallback(async () => {
    abort.current?.abort();

    if (params === null) {
      last.current = { key: null, tags: null };
      setState({ phase: "idle", tags: null, error: null, refreshing: false });
      return;
    }

    const ctrl = new AbortController();
    abort.current = ctrl;
    // R10.22: same subject → hold the previous frame; different subject → drop
    // it. `held` comes from the ref, never from `state`, so `load`'s dep list
    // stays `[cfg, params]`.
    const key = tagsKey(params);
    const held = last.current.key === key ? last.current.tags : null;

    setState({ phase: held === null ? "loading" : "ready", tags: held, error: null, refreshing: held !== null });

    try {
      const next = await fetchTags(cfg, params, { signal: ctrl.signal });
      if (ctrl.signal.aborted) return;
      last.current = { key, tags: next };
      setState({ phase: "ready", tags: next, error: null, refreshing: false });
    } catch (err) {
      if (ctrl.signal.aborted) return;
      const message = err instanceof Error ? err.message : String(err);
      last.current = { key, tags: held };
      setState({ phase: held === null ? "error" : "ready", tags: held, error: message, refreshing: false });
    }
  }, [cfg, params]);

  useEffect(() => {
    void load();
    return () => abort.current?.abort();
    // `load` is the only dep, exactly as in the template: `state.tags` is read
    // inside `load` through the ref-equivalent, and including it in the dep
    // list would re-run the effect on every state change it causes.
  }, [load]);

  return { state, refresh: () => void load() };
}

/* ── a URL that names no cluster ───────────────────────────────────────────── */

function NoCluster({ state, onPatch }: TagsPageProps): ReactElement {
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Tags</h2>
      </div>
      <p className="card-note">
        {state.cluster === null
          ? "This URL does not name a cluster."
          : `“${state.cluster}” is not a namespace/name pair.`}{" "}
        GET /api/v1/insights/tags takes a namespace and a cluster, so there is no
        fleet-wide form of this page to fall back to.
      </p>
      <button
        type="button"
        className="btn"
        onClick={() => onPatch({ page: "fleet", cluster: null })}
      >
        Back to Fleet
      </button>
    </section>
  );
}
