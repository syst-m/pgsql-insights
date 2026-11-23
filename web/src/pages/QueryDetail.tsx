/* The Query Detail page (R10.7).
 *
 * R10.7's sentence lists five blocks and this file is the shell that composes
 * them: the **header** (fingerprint, database, user, first-seen), the **per-minute
 * load chart** (`components/QueryLoadChart.tsx`), the **latency panel**
 * (`components/LatencyHistogram.tsx`), the **plan samples list with a JSON/plan
 * viewer** (`components/PlanList.tsx` + `PlanViewer.tsx`), and the
 * **recommendations** (`components/RecommendationList.tsx`). Each is its own file
 * with its own test; the page owns the composition and the URL, nothing else —
 * which is the charter's split restated: "you write pages and components; A4 owns
 * the build that bundles them and the routes that mount them."
 *
 * ── the payload is the list row plus five fields ──────────────────────────
 *
 * `dto.QueryDetailView` is `QueryView` embedded **plus** `user`, `firstSeen`,
 * `latency`, `plans`, `planChanges`, and the OpenAPI document composes it with
 * `allOf` over `QueryView` rather than re-listing R3.2's sixteen columns. The
 * consequence for this page is that the embedded half is the same object the
 * Queries list renders per row — so the header renders it with the same
 * formatters the table uses, and there is no second copy of the row shape here to
 * drift (the failure `dto/queries.go` names as its own reason for embedding).
 * Everything new on this page is exactly those five fields.
 *
 * ── A3's ruling, and the one function that absorbs it ─────────────────────
 *
 * The route addresses `(namespace, cluster, fingerprint)` plus optional `?db=`
 * and `?user=`; with both omitted the header's `database` and `user` name the
 * highest-`total_exec_ms` `(datname, usename)` pair that fingerprint has in that
 * cluster, ties broken lexicographically (requirements.md:416, A3's ruling on
 * the question `internal/api/query_detail.go:93` routed). Two things follow, and
 * both are handled by keeping param extraction in one exported function:
 *
 *   - the two optionals arrive in `UrlState` when A4 widens it (the standing ask;
 *     `QueryUrlPatch` on the Queries page already declares `database`/`user`), so
 *     `detailParamsFrom` reads them through the same widening cast that page uses
 *     and needs no other change when they become real fields;
 *   - the store's selection rule is the *server's* answer, not this page's: the
 *     page renders whatever `QueryDetailView` names, which is the point of the
 *     embedding. Nothing here re-ranks rows.
 *
 * ── mounting ──────────────────────────────────────────────────────────────
 *
 * `routes.ts` has the entry (`page: "query-detail"`, `requirement: "R10.7"`,
 * `path: "/c/:ns/:name/queries/:fingerprint"`) with `component: null`. Flipping
 * it to `QueryDetailPage` is A4's one-line edit — adding a page means a file here
 * and an entry there, and this commit is the file.
 */

import type { ReactElement } from "react";

import { FIXTURE_QUERY_DETAIL } from "../api/fixtures";
import type { QueryDetailView, QuerySeries } from "../api/types";
import { buildPath, type PageProps } from "../routes";
import type { UrlState } from "../state/urlState";
import { formatTimestamp, utcTitle } from "../lib/format";

import { QueryLoadChart } from "../components/QueryLoadChart";
import { LatencyHistogram } from "../components/LatencyHistogram";
import { PlanList } from "../components/PlanList";
import { RecommendationList } from "../components/RecommendationList";

/** Everything the R10.7 route is addressed by, in one shape.
 *
 *  The three required parts are R8.2's route (`query_detail.go:69` takes the
 *  fingerprint as a path segment and `namespace`/`cluster` as required query
 *  parameters, because R3.4 makes a fingerprint canonical *within* a cluster);
 *  the two optional ones are A3's ruling, which exists so a viewer filtered to
 *  one user in R10.6's table gets a detail page whose header agrees with the
 *  filter they arrived through. */
export interface QueryDetailUrlParams {
  namespace: string;
  cluster: string;
  fingerprint: string;
  /** A3's `?db=`. Absent until `urlState.ts` grows the field (the ask on A4 that
   *  `QueryUrlPatch.database` on the Queries page already declares). */
  db?: string;
  /** A3's `?user=`. Same status as `db`. */
  user?: string;
}

/** The single place this page turns URL state into route params.
 *
 *  Exported and alone on purpose. The coordinator's instruction on the A3 open
 *  item was to keep param extraction in one function so a ruling that changes the
 *  route's parameter set is a one-line change; the ruling came in (optional
 *  `?db=`/`?user=`) and this is the function that changed. Every future
 *  discriminator — a pod, an instance role, a window — lands here and nowhere
 *  else in the page.
 *
 *  Returns null when the triple is incomplete: a detail page with no fingerprint
 *  has nothing to render, and `buildPath` already refuses to build one (it returns
 *  `/` for a cluster with no `/`), so the page can ask the same question the
 *  router asks rather than keeping a second rule. */
export function detailParamsFrom(state: UrlState): QueryDetailUrlParams | null {
  const fingerprint = (state.row ?? "").trim();
  if (fingerprint === "") return null;
  // `state.cluster` is the `namespace/name` form R10.16's URLs use (`null` on a
  // fleet-scoped URL, which is why the detail page's params can be absent at all);
  // `buildPath` splits it the same way, so the two directions of the route agree by
  // construction rather than by two copies of the split rule.
  const clusterId = state.cluster ?? "";
  if (clusterId === "" || !clusterId.includes("/")) return null;
  const [namespace = "", cluster = ""] = clusterId.split("/");
  if (namespace === "" || cluster === "") return null;

  // `db`/`user` are read through a widening cast because `UrlState` does not
  // declare them yet. The Queries page uses the identical pattern for
  // `database` (`currentDatabase`), so this is the house way of reading a field
  // the URL carries and the type does not — and it deletes itself when A4 widens
  // the type, at which point `state.db` typechecks directly.
  const extra = state as UrlState & { db?: string; user?: string };
  const db = extra.db?.trim() || undefined;
  const user = extra.user?.trim() || undefined;

  return { namespace, cluster, fingerprint, db, user };
}

export interface QueryDetailPageProps extends PageProps<QueryDetailView> {
  /** This fingerprint's per-minute series, from R8.11's batched route requested
   *  with one `fingerprints=` value.
   *
   *  A prop, not a field on `QueryDetailView`, because the DTO cannot inline it:
   *  `types.ts:575` says so outright — "There is no `points`/`series` field
   *  here… `QuerySeries` is declared in `internal/api`, which imports this
   *  package, so referencing it from here is an import cycle. Ask the series
   *  endpoint for the chart instead of expecting it on this object." It is
   *  optional because `client.ts` has no `fetchQuerySeries` yet (the ask on A4);
   *  when absent, the load panel renders R10.13's one-line explanation rather
   *  than a blank frame. */
  series?: QuerySeries;
}

/** R10.7's Query Detail page. */
export function QueryDetailPage(props: QueryDetailPageProps): ReactElement {
  const { state, series } = props;

  // The same fixture pattern as every other page in `pages/`: the payload is the
  // prop when a caller has one, the fixture otherwise, and the swap when A4 adds
  // `fetchQueryDetail` is the three lines the Queries page documents.
  const view: QueryDetailView = props.fixtureOverride ?? FIXTURE_QUERY_DETAIL;
  const params = detailParamsFrom(state);

  // R10.24's trail is literally "Fleet → Cluster → Query Detail". The Queries
  // list sits between the cluster and the detail — it is the page that links here
  // (R10.6's row click patches `page: "query-detail"`), and R10.16's path
  // `/c/:ns/:name/queries/:fingerprint` spells the same level — so the crumb
  // renders four levels: the spec's three plus the list the route passes through.
  const clusterId = `${view.namespace}/${view.cluster}`;

  return (
    // The extracted params are on the element so the extraction is observable
    // without exporting a test-only render: this is the argument the fetcher will
    // be called with once `client.ts` has `fetchQueryDetail`, and a page that
    // parsed the URL into a local and never used it is the bug a test should
    // catch. The value shown in the header is the payload's own fingerprint, so
    // the two agree only if the extraction is right.
    <div className="page" data-page="query-detail" data-fingerprint={params?.fingerprint ?? ""}>
      <header className="page-head">
        <div>
          <p className="breadcrumb">
            <a className="cluster-link" href="/">
              Fleet
            </a>
            <span aria-hidden> / </span>
            <a className="cluster-link" href={buildPath("cluster", clusterId, null)}>
              {clusterId}
            </a>
            <span aria-hidden> / </span>
            <a className="cluster-link" href={buildPath("queries", clusterId, null)}>
              Queries
            </a>
            <span aria-hidden> / </span>
            <span>Query Detail</span>
          </p>

          {/* R10.7's header is four fields. Three come from the embedded
              QueryView (fingerprint, database) and the detail-only half (user,
              firstSeen); the fourth is the page title. The SQL text is the
              natural title — a page titled with a SHA-256 is not scannable — so
              the title is the statement and the fingerprint is a labelled field
              under it, which is also what makes the header's own copy auditable:
              the two halves of the payload are both visible, not one. */}
          <h1 className="page-title" title={view.query}>
            {view.query}
          </h1>

          <dl className="detail-header" data-testid="detail-header">
            <dt>Fingerprint</dt>
            <dd className="mono" title={view.fingerprint}>
              {view.fingerprint}
            </dd>
            <dt>Database</dt>
            <dd>{view.database}</dd>
            {/* R3.12 folds the user into the tag on the list view and only the
                detail view shows it (`types.ts:565`); A3's ruling makes this the
                highest-load user of the fingerprint unless `?user=` named one. */}
            <dt>User</dt>
            <dd>{view.user ?? "—"}</dd>
            <dt>First seen</dt>
            <dd title={utcTitle(view.firstSeen)}>{formatTimestamp(view.firstSeen)}</dd>
            {view.queryid !== undefined && (
              <>
                <dt>queryid</dt>
                <dd className="mono">{view.queryid}</dd>
              </>
            )}
            <dt>Truncated</dt>
            <dd>{view.truncated ? "yes" : "no"}</dd>
          </dl>
        </div>
      </header>

      {/* The four blocks, in R10.7's own order: load chart, latency, plans,
          recommendations. Each is its own card and its own component file, so a
          reviewer can audit one requirement at a time. */}
      <QueryLoadChart series={series} />
      <LatencyHistogram latency={view.latency} />
      <PlanList plans={view.plans} planChanges={view.planChanges} />
      <RecommendationList
        recommendations={view.recommendations}
        namespace={view.namespace}
        cluster={view.cluster}
      />
    </div>
  );
}
