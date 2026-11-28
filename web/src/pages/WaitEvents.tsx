/* The Wait Events page — R10.9's cluster page (requirements.md:422), the tenth of
 * R10.3's fourteen (web/src/routes.ts:226-236).
 *
 * What it renders, and from what. The route row names
 * `GET /api/v1/insights/wait-events` as its `endpoint:` and `phase: "6a"`, but
 * there is no such DTO yet — the coordinator's ruling on this page is: "render it
 * from `ActiveQueriesResponse` (`web/src/api/client.ts:619`), grouped. No A2 path,
 * no A1 DTO." So the page calls the *active-queries* fetcher through the same
 * `useActiveQueries` hook the Active Queries page uses, and groups the sessions
 * client-side with `waitBuckets` (`web/src/components/WaitEventsChart.tsx:108`), whose
 * rules mirror `internal/collector/activity.go:690` `WaitEventRollup`. When A1 lands a
 * `WaitEventsResponse`, this file's `resp` line and its `PageProps<…>` argument are the
 * two places that change; the chart and the drill take buckets, not a payload, so
 * neither of them moves.
 *
 * The page is three stacked blocks, in the order R10.9's sentence reads them:
 *
 *   1. `PageHeader` — R10.21's `meta.generatedAt` stamp over the cluster crumb.
 *   2. `WaitEventsChart` — the "donut/bar by `wait_event_type`" half.
 *   3. The drill — "a drill to per-event counts and the sessions currently in
 *      each": the selected bucket's per-event table (R4.3's second tab) above the
 *      session table (R10.20's column menu governs that one, since it is the wide
 *      table).
 *
 * Why the drill is a section and not a route. R10.9 puts the drill *inside* the
 * Wait Events page ("with a drill to…"), and R10.3's table has one wait-events
 * route, so the drill is page state (`selectedKey`) rather than a URL slot — the
 * same reason `web/src/pages/ActiveQueries.tsx:204-208` keeps its row
 * confirmation out of the URL: R10.16's URL grammar has no slot for it, and a
 * second route would need a requirement to justify it.
 */

import { useMemo, useState, type ReactElement } from "react";

import { FIXTURE_ACTIVE_QUERIES } from "../api/fixtures";
import { SESSION_NOUN } from "../api/envelope";
import type { ActiveQueriesResponse, SessionView } from "../api/types";
import { buildPath, type PageProps } from "../routes";
import { activeParamsFrom, useActiveQueries } from "../state/useActiveQueries";
import { formatDurationMs, formatTimestamp, utcTitle, NULL_VALUE } from "../lib/format";
import { ColumnMenu, type ColumnDef, useColumnVisibility } from "../components/ColumnMenu";
import {
  copyFor,
  deriveWaitEventsPanelState,
  StatePanel,
  WAIT_EVENTS_COPY,
  type FetchPhase,
} from "../components/StatePanel";
import { SessionStatusChip } from "../components/StatusGlyph";
import { WaitEventsChart, waitBuckets, type WaitBucket } from "../components/WaitEventsChart";

/** The payload is the Active Queries payload, so its freshness is R10.8's: the
 *  same read, polled at the same interval, on a page that does not itself have a
 *  named interval. Passing the number (rather than taking the hook's default) is
 *  what keeps the header's rendered "refreshes every 15 s" and the actual poll one
 *  value with two uses, exactly as `web/src/pages/ActiveQueries.tsx:94` does for
 *  the page R10.8 names. */
const POLL_MS = 15_000;

/** The wide table's columns (R10.20).
 *
 *  Every key/label pair here is copied from `ACTIVE_COLUMNS`
 *  (`web/src/pages/ActiveQueries.tsx:121`) rather than re-invented, because a
 *  column that exists on two pages has one name: `pid` is "PID" on both or the
 *  two menus disagree about the same cell.
 *
 *  `action` renders the R4.5 gate *value* (`row.action`), not the Terminate
 *  button: the affordance is R4.5's and lives on the page R4.5 names, and a
 *  second copy of a mutation UI on a read-only page is a second thing to keep in
 *  sync. The value is still worth a column because it answers "could this session
 *  have been terminated", which the chart's bucket cannot.
 */
export const WAIT_EVENTS_COLUMNS: readonly ColumnDef[] = [
  { key: "pid", label: "PID", required: true },
  { key: "waitEvent", label: "Wait event" },
  { key: "query", label: "Query" },
  { key: "status", label: "Status" },
  { key: "database", label: "Database" },
  { key: "userName", label: "User name" },
  { key: "queryDurationSeconds", label: "Query duration" },
  { key: "applicationName", label: "Application name" },
  { key: "action", label: "Action" },
];

/** The default visible set (R10.20's "documented default column set"), mirrored
 *  in `DEFAULTS["wait-events"]` (`web/src/components/ColumnMenu.tsx:128`). Six of
 *  the nine: the two that identify the row (pid, the pair the chart bucketed it
 *  by), the two that say what it is doing (query, status), and the two that say
 *  where (database, action). `queryDurationSeconds` and `applicationName` are off
 *  by default for the reason `ACTIVE_COLUMNS` gives for its own duration columns —
 *  the chart above already carries the timing, and the drill answers "which
 *  sessions", not "for how long". */
export const WAIT_EVENTS_DEFAULTS: readonly string[] = [
  "pid",
  "waitEvent",
  "query",
  "status",
  "database",
  "action",
];

export type WaitEventsPageProps = PageProps<ActiveQueriesResponse>;

export function WaitEventsPage(props: WaitEventsPageProps): ReactElement {
  const { state } = props;

  // Every hook above every early return — the rule `web/src/pages/ActiveQueries.tsx:204-208`
  // documents after being bitten by it: a render that turns a malformed URL into
  // a valid one renders more hooks than the render before it and throws.
  const params = useMemo(() => activeParamsFrom(props.state), [props.state]);
  const { state: data, refresh } = useActiveQueries(props.cfg, params, POLL_MS);
  const resp: ActiveQueriesResponse = props.fixtureOverride ?? data.active ?? FIXTURE_ACTIVE_QUERIES;
  const phase: FetchPhase = props.fixtureOverride !== undefined ? "ready" : data.phase;
  const columns = useColumnVisibility({
    viewId: "wait-events",
    columns: WAIT_EVENTS_COLUMNS,
  });
  const [selectedKey, setSelectedKey] = useState<string | null>(null);

  // One grouping, computed once, feeding both the bars and the drill — so the
  // number on a bar and the rows under it cannot come from different passes.
  const buckets = useMemo(() => waitBuckets(resp.data), [resp]);

  if (params === null) {
    return (
      <div className="page" data-page="wait-events">
        <PageHeader state={state} resp={resp} />
        <NoCluster {...props} />
      </div>
    );
  }

  const panel = deriveWaitEventsPanelState({
    phase,
    active: resp,
    error: data.error ?? undefined,
  });
  const bannerCopy = copyFor(
    panel,
    { meta: resp.meta, data: resp.data },
    WAIT_EVENTS_COPY,
    SESSION_NOUN,
  );

  if (panel === "loading" || panel === "empty" || panel === "error") {
    return (
      <div className="page" data-page="wait-events">
        <PageHeader state={state} resp={resp} />
        <StatePanel
          state={panel}
          error={data.error ?? undefined}
          onAction={refresh}
          copy={WAIT_EVENTS_COPY}
        />
      </div>
    );
  }

  // Nothing selected yet: the largest bucket is open. A drill that starts closed
  // shows an empty half-page on first paint, and the bucket the chart just drew a
  // full-width bar for is the one a reader arriving on this page is asking about.
  const selected = buckets.find((b) => b.key === selectedKey) ?? buckets[0] ?? null;

  return (
    <div className="page" data-page="wait-events">
      <PageHeader state={state} resp={resp} />

      {bannerCopy !== null && (
        <div className="banner" role="status">
          <strong>{bannerCopy.title}</strong>
          <span>{bannerCopy.body}</span>
        </div>
      )}

      <WaitEventsChart
        buckets={buckets}
        selectedKey={selected?.key ?? null}
        onSelect={setSelectedKey}
      />

      {selected !== null && (
        <Drill bucket={selected} columns={columns} />
      )}
    </div>
  );
}

/* ── header ─────────────────────────────────────────────────────────────────── */

/** The header block, in the shape `web/src/pages/ActiveQueries.tsx:362` gives it:
 *  crumb, title, then the R10.21 stamp with the absolute UTC on hover.
 *
 *  The R10.8 refresh indicator is rendered here too. It is R10.8's widget and
 *  R10.8 names the Active Queries page, but the number it prints is this page's
 *  own poll interval (`POLL_MS`), and a header that shows live data without
 *  saying how often it refreshes is the R10.21 case rather than an R10.8 one. */
function PageHeader({
  state,
  resp,
}: {
  state: PageProps["state"];
  resp: ActiveQueriesResponse;
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
          <span>Wait Events</span>
        </p>
        <h1 className="page-title">Wait Events</h1>
        <p className="page-meta" title={`${resp.meta.generatedAt} UTC`}>
          Data as of {formatTimestamp(resp.meta.generatedAt)}
          <span className="refresh-indicator" data-poll-ms={POLL_MS}>
            {" · refreshes every "}
            {Math.round(POLL_MS / 1000)} s
          </span>
        </p>
      </div>
    </header>
  );
}

/** The URL names no cluster, or names one that is not a namespace/name pair.
 *
 *  Same wording shape as `web/src/pages/ActiveQueries.tsx:739`'s `NoCluster`, with
 *  this route's sentence: the payload is one cluster's `pg_stat_activity`, so
 *  there is no fleet-wide form of this page either. */
function NoCluster({ state, onPatch }: PageProps<ActiveQueriesResponse>): ReactElement {
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Wait Events</h2>
      </div>
      <p className="card-note">
        {state.cluster === null
          ? "This URL does not name a cluster."
          : `“${state.cluster}” is not a namespace/name pair.`}{" "}
        The payload is one cluster's sessions, so there is no fleet-wide form of this
        page to fall back to.
      </p>
      <button type="button" className="btn" onClick={() => onPatch({ page: "fleet", cluster: null })}>
        Back to Fleet
      </button>
    </section>
  );
}

/* ── the drill ──────────────────────────────────────────────────────────────── */

/** R10.9's second half for one bucket: "per-event counts and the sessions
 *  currently in each".
 *
 *  Two tables, because the requirement names two things. The first is R4.3's
 *  per-event rollup inside the bucket (`event` → count → the pids behind it); the
 *  second is the session rows themselves, and it is the wide one, so it is the one
 *  the column menu governs. The per-event table is three columns and needs no
 *  menu — R10.20's "every wide table" is about tables a reader has to scroll.
 *
 *  No virtualisation: `web/src/pages/ActiveQueries.tsx:290-302` windows its table
 *  because that page lists *every* session in the cluster, which is bounded by
 *  `max_connections`. This table lists one bucket's sessions, whose count is
 *  printed on the bar above it, so the whole set is on screen without a window —
 *  and `DensityToggle` is absent for the paired reason: its `ROW_HEIGHT`
 *  (`web/src/components/VirtualList.tsx`) only means something to a window. */
function Drill({
  bucket,
  columns,
}: {
  bucket: WaitBucket;
  columns: ReturnType<typeof useColumnVisibility>;
}): ReactElement {
  return (
    <section className="card" data-panel="wait-events-drill">
      <div className="card-head">
        <h2 className="card-title">
          {bucket.label}
          {bucket.kind === "named" ? "" : ` — ${bucket.kind}`}
        </h2>
        <div className="card-actions">
          <span className="card-meta num">{bucket.sessions.length} sessions</span>
          <ColumnMenu
            columns={WAIT_EVENTS_COLUMNS}
            visible={columns.visible}
            onChange={columns.setVisible}
          />
        </div>
      </div>

      <div className="table-wrap">
        <table className="data" data-testid="wait-event-rows">
          <caption className="sr-only">Events inside {bucket.label}</caption>
          <thead>
            <tr>
              <th scope="col">Event</th>
              <th scope="col" className="num">
                Sessions
              </th>
              <th scope="col">PIDs</th>
            </tr>
          </thead>
          <tbody>
            {bucket.events.map((e) => (
              <tr key={e.event}>
                <th scope="row">{e.event === "" ? NULL_VALUE : e.event}</th>
                <td className="num">{e.count}</td>
                <td>{e.sessions.map((s) => String(s.pid)).join(", ")}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="table-wrap">
        <table className="data" data-testid="wait-event-sessions">
          <caption className="sr-only">Sessions in {bucket.label}</caption>
          <thead>
            <tr>
              {columns.visible.map((key) => (
                <th key={key} scope="col" className={NUMERIC_COLS.has(key) ? "num" : undefined}>
                  {labelFor(key)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {bucket.sessions.map((row) => (
              <tr key={row.pid}>
                {columns.visible.map((key) => (
                  <td key={key} className={NUMERIC_COLS.has(key) ? "num" : undefined}>
                    {cell(row, key)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/** The columns whose cells are numbers, so they get the `.num` tabular treatment
 *  (`web/src/styles/base.css:62`). Same set as `ACTIVE_COLUMNS`'s numeric
 *  columns, minus the ones this table does not offer. */
const NUMERIC_COLS = new Set(["pid", "queryDurationSeconds"]);

/** A column key's label, from the list that defines both. */
function labelFor(key: string): string {
  return WAIT_EVENTS_COLUMNS.find((c) => c.key === key)?.label ?? key;
}

/** One cell, by column key. Every branch reads a field the DTO has; the only
 *  formatting is duration (the API's fields are seconds, `formatDurationMs` takes
 *  ms) and the timestamp (R10.21's local rendering with the absolute UTC on hover).
 *
 *  `waitEvent` carries the `unknownWaitEvent` marker inline — the same
 *  `?` + title treatment `web/src/pages/ActiveQueries.tsx:656-663` gives it — because
 *  the bucket this row is under was chosen *from* that flag, and a reader looking
 *  at the row needs the qualification on the cell, not only on the bar. */
function cell(row: SessionView, key: string): ReactElement | string {
  switch (key) {
    case "pid":
      return String(row.pid);
    case "query":
      return (
        <span className="query-cell trunc" title={row.query}>
          {row.query}
        </span>
      );
    case "status":
      return <SessionStatusChip status={row.status} blocked={row.blockingPids.length > 0} />;
    case "queryDurationSeconds":
      return formatDurationMs(row.queryDurationSeconds * 1000);
    case "waitEvent":
      return row.unknownWaitEvent ? (
        <span title="Not in pg_wait_events">
          {row.waitEvent === "" ? NULL_VALUE : row.waitEvent}
          <span className="trunc-flag" aria-label="unrecognised wait event">
            ?
          </span>
        </span>
      ) : (
        row.waitEvent === "" ? NULL_VALUE : row.waitEvent
      );
    case "database":
      return row.database;
    case "userName":
      return row.userName;
    case "applicationName":
      return row.applicationName === "" ? NULL_VALUE : row.applicationName;
    case "action":
      return row.action === "" ? NULL_VALUE : row.action;
    case "sourceTime":
      return <span title={utcTitle(row.sourceTime)}>{formatTimestamp(row.sourceTime)}</span>;
    default:
      return NULL_VALUE;
  }
}
