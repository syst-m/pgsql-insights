/* The Active Queries page (R10.8).
 *
 * R10.8's sentence (requirements.md:419) names four things and this file owns
 * all four: the **live table** (R4.1), the **15 s auto-refresh indicator**,
 * **row expansion to the blocking chain**, and the **Terminate action behind
 * the R4.5 gate**. The route entry is `routes.ts:211-220` (`page: "active"`,
 * `requirement: "R10.8"`, `endpoint: "GET /api/v1/active-queries"`,
 * `path: "/c/:ns/:name/active"`, `inNav: true`); its `component:` was flipped
 * from `null` to this file by A4 in `a5e5be0`, the other half of the pair.
 *
 * ── why the blocking chain is in this file and not in a `BlockingChains.tsx` ─
 *
 * R4.2's chain is R10.8's third clause, and `api/types.ts:806-808` says where
 * it renders: "R10.8 renders it as the row expansion of the Active Queries
 * table". A chain is not a section of the page, it is a row opened — so it is
 * `ChainRows` below, inside this file, and there is no sibling component file
 * to go looking for. The reason it is not folded into the row cell either is
 * the payload: `blockingChains[]` is `{blocked, blockers}` of *whole*
 * `SessionView`s, so the blockers' durations and users are only renderable as
 * rows, and a row is what an expansion is.
 *
 * The pairing rule follows from the same field. `blockingChains` is keyed by
 * the blocked session, so a row's chain is looked up by `pid`, and the row's
 * own `blockingPids` (R4.4's edge list) is the cross-check: a row naming pids
 * with no chain entry says so rather than rendering an empty expansion, because
 * "blocked by 41102" and "the API told me nothing about 41102" are different
 * answers and only one of them is true.
 *
 * ── the Action cell, and which requirement gates it ────────────────────────
 *
 * The gate is **R4.5** (`requirements.md:330`, `--allow-actions`, generalized
 * by R12.8 at `:473`) — not R4.6, which is the idle-in-transaction-age and
 * `backend_type` row at `:331` and which this page satisfies with two columns
 * (`transactionDurationSeconds`, `backendType`) rather than with a button.
 *
 * The legal values are the Go side's two constants, not a local union:
 * `active_queries.go:89` `ActionNameTerminate = "terminate"` and `:97`
 * `ActionDisabled = "disabled"`. R4.1's rule for the column is that it
 * "renders disabled with a link to the R12.8 gate … so the column is never
 * silently blank", which is the whole argument for rendering a *disabled*
 * control rather than no control: an absent button and a disabled one are two
 * answers to "can I kill this backend", and the field exists so the server,
 * not the page, gets to give it. So an unrecognised string still renders a
 * disabled control carrying the string, and the only value that renders an
 * enabled one is `"terminate"`.
 *
 * R4.5 also requires the action be "confirmed", so an enabled button does not
 * fire on click — it arms a Confirm/Cancel pair (`confirmPid` below). What it
 * cannot yet do is fire at all: `client.ts` has six fetchers and none of them
 * is `POST /api/v1/sessions/{pid}/terminate`, and a mutation is not a shape
 * this slot may invent. Confirming therefore re-reads the list (the honest
 * half of the flow — the row you acted on is refetched) and the POST is the
 * standing ask on `web/src/api/`.
 *
 * ── the 15 s indicator ─────────────────────────────────────────────────────
 *
 * R10.8 names the interval, so it is a constant here (`POLL_MS`) that is passed
 * to the hook *and* rendered by the header. An indicator that counts to fifteen
 * over a poll that runs every thirty seconds is a widget that lies, and passing
 * the number rather than relying on `useActiveQueries`'s default is what makes
 * the rendered number and the actual interval one value with two uses.
 */

import { useMemo, useState, type ReactElement } from "react";

import { SESSION_NOUN } from "../api/envelope";
import { FIXTURE_ACTIVE_QUERIES } from "../api/fixtures";
import type {
  ActiveQueriesResponse,
  BlockingChainView,
  SessionView,
} from "../api/types";
import { buildPath, type PageProps } from "../routes";
import { activeParamsFrom, useActiveQueries } from "../state/useActiveQueries";
import { formatDurationMs, formatTimestamp, utcTitle, NULL_VALUE } from "../lib/format";
import { ColumnMenu, type ColumnDef, useColumnVisibility } from "../components/ColumnMenu";
import {
  ACTIVE_COPY,
  StatePanel,
  copyFor,
  deriveActivePanelState,
  type FetchPhase,
} from "../components/StatePanel";

/** R10.8's interval, in ms — the number the header prints and the number the
 *  hook polls at, from one declaration. */
const POLL_MS = 15_000;

/** `active_queries.go:89` — the only value that renders an enabled control. */
const ACTION_TERMINATE = "terminate";

/** The page's column set: R4.1's twenty columns as `dto/sessions_test.go:52-54`
 *  enumerates them (`pid`, `query`, `queryType`, `status`, the three durations,
 *  `waitEventType`, `waitEvent`, `database`, `applicationName`, `userName`,
 *  `clientAddress`, `action`, plus `clientPort`, `backendType`, `queryid`,
 *  `blockingPids`, `sourceTime`).
 *
 *  `unknownWaitEvent` is not a column of its own. It is a qualifier on the
 *  wait-event cell — the Go side's `unknownWaitEvent: true` means the
 *  `(type, name)` pair is not in PG's own `pg_wait_events` list, which changes
 *  how the value in that cell should be read, not what sits next to it.
 *
 *  The default *visible* set is nine of the twenty (see
 *  `ColumnMenu.DEFAULTS["active-queries"]`); the menu is what makes the other
 *  eleven reachable, which is R10.20's contract rather than this file's. */
export const ACTIVE_COLUMNS: readonly ColumnDef[] = [
  { key: "pid", label: "PID" },
  { key: "query", label: "Query", required: true },
  { key: "queryType", label: "Type" },
  { key: "status", label: "Status" },
  { key: "sessionDurationSeconds", label: "Session duration" },
  { key: "transactionDurationSeconds", label: "Transaction duration" },
  { key: "queryDurationSeconds", label: "Query duration" },
  { key: "waitEventType", label: "Wait event type" },
  { key: "waitEvent", label: "Wait event" },
  { key: "database", label: "Database" },
  { key: "applicationName", label: "Application name" },
  { key: "userName", label: "User name" },
  { key: "clientAddress", label: "Client address" },
  { key: "clientPort", label: "Client port" },
  { key: "backendType", label: "Backend type" },
  { key: "queryid", label: "queryid" },
  { key: "blockingPids", label: "Blocking pids" },
  { key: "action", label: "Action" },
  { key: "sourceTime", label: "Source time" },
];

/** The page's props are `PageProps` over its own payload and nothing more —
 *  `QueryDetailPageProps` adds a `series` field because that page renders a
 *  second endpoint; this one renders one, so there is nothing to add. */
export type ActiveQueriesPageProps = PageProps<ActiveQueriesResponse>;

export function ActiveQueriesPage(props: ActiveQueriesPageProps): ReactElement {
  const { state } = props;

  // `Fleet.tsx:45-47` verbatim in shape, for the reason every page repeats it:
  // the page calls the hook, the prop still wins, and the contract fixture is
  // what is on screen before the first response lands. `params` is memoised
  // because `useActiveQueries.ts` puts it in `load`'s dep list — a fresh object
  // every render is a refetch every render, and since `load` calls `setState`
  // that is a render loop, not merely an extra request.
  const params = useMemo(() => activeParamsFrom(state), [state]);
  const { state: data, refresh } = useActiveQueries(props.cfg, params, POLL_MS);
  const resp: ActiveQueriesResponse = props.fixtureOverride ?? data.active ?? FIXTURE_ACTIVE_QUERIES;
  const phase: FetchPhase = props.fixtureOverride !== undefined ? "ready" : data.phase;

  // Every hook is above this line. `activeParamsFrom` returns `null` for a URL
  // with no cluster (or a malformed pair), and the route is `scope: "cluster"`
  // — `pg_stat_activity` is per-instance, so there is no cluster-less form of
  // this page to render. The early return has to come after the hooks, which is
  // the rule `Fleet.tsx:64-65` states and `QueryDetail.tsx`'s no-such-query
  // frame follows.
  if (params === null) return <NoCluster {...props} />;

  const panel = deriveActivePanelState({ phase, active: resp, error: data.error ?? undefined });
  const columns = useColumnVisibility({ viewId: "active-queries", columns: ACTIVE_COLUMNS });
  // R10.22's wording is composed from the payload rather than written here: the
  // read that served 47 of 51 sessions has to name the four, which is what
  // `copyFor`'s `source` argument does and a fixed sentence cannot.
  const bannerCopy = copyFor(panel, { meta: resp.meta, data: resp.data }, ACTIVE_COPY, SESSION_NOUN);

  // The one piece of local state the page owns besides the expansion: which row
  // is showing its Confirm/Cancel. R4.5 requires a confirmed action, and a
  // confirmation is per-row, so it cannot live in the URL (R10.16's list has no
  // slot for it) and it must not be global (two rows armed at once would render
  // two confirmations for one click).
  const [confirmPid, setConfirmPid] = useState<number | null>(null);

  if (panel === "loading" || panel === "empty" || panel === "error") {
    return (
      <div className="page" data-page="active">
        <PageHeader state={state} resp={resp} />
        <StatePanel
          state={panel}
          error={data.error ?? undefined}
          onAction={refresh}
          copy={ACTIVE_COPY}
        />
      </div>
    );
  }

  return (
    <div className="page" data-page="active">
      <PageHeader state={state} resp={resp} />

      <Scorecard resp={resp} />

      {bannerCopy !== null && (
        <div className="banner" role="status">
          <strong>{bannerCopy.title}</strong>
          <span>{bannerCopy.body}</span>
        </div>
      )}

      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Active queries</h2>
          <div className="card-actions">
            <span className="card-meta num">{resp.data.length} sessions</span>
            <ColumnMenu columns={ACTIVE_COLUMNS} visible={columns.visible} onChange={columns.setVisible} />
          </div>
        </div>

        <table className="data" data-testid="active-table">
          <caption className="sr-only">
            Sessions reported by pg_stat_activity for {params.namespace}/{params.cluster}
          </caption>
          <thead>
            <tr>
              <th scope="col" className="expand-col">
                <span className="sr-only">Blocking chain</span>
              </th>
              {columns.visible.map((key) => (
                <th key={key} scope="col" className={numericCol(key) ? "num" : undefined}>
                  {labelFor(key)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {resp.data.map((row) => (
              <SessionRow
                key={row.pid}
                row={row}
                columns={columns.visible}
                chain={chainFor(resp.blockingChains, row.pid)}
                confirming={confirmPid === row.pid}
                onConfirm={() => setConfirmPid(row.pid)}
                onCancel={() => setConfirmPid(null)}
                onConfirmed={() => {
                  // The POST is the ask on `web/src/api/` (see the header); the
                  // part that exists today is re-reading the list, which is what
                  // a confirmed action has to end in anyway.
                  setConfirmPid(null);
                  refresh();
                }}
              />
            ))}
          </tbody>
        </table>
      </section>
    </div>
  );
}

/* ── header ────────────────────────────────────────────────────────────────── */

/** Breadcrumb, title, the R10.21 stamp, and R10.8's auto-refresh indicator.
 *
 *  No range control: the route takes `namespace`/`cluster` and no time window
 *  (`client.ts:442-445`'s two required params), because `pg_stat_activity`
 *  answers "what is running now" — a window is R10.15's question about the
 *  query store, not this route's. */
function PageHeader({ state, resp }: { state: PageProps["state"]; resp: ActiveQueriesResponse }) {
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
          <span>Active Queries</span>
        </p>
        <h1 className="page-title">Active Queries</h1>
        <p className="page-meta" title={`${resp.meta.generatedAt} UTC`}>
          {/* R10.21: the stamp is the payload's own `meta.generatedAt`, rendered
              in the local zone and labelled, with the absolute UTC on hover. */}
          Data as of {formatTimestamp(resp.meta.generatedAt)}
          {/* R10.8's indicator, from the same constant the hook polls at. */}
          <span className="refresh-indicator" data-poll-ms={POLL_MS}>
            {" · refreshes every "}
            {Math.round(POLL_MS / 1000)} s
          </span>
        </p>
      </div>
    </header>
  );
}

/* ── R4.1's scorecard ──────────────────────────────────────────────────────── */

/** The summary header R4.1 puts above the table, from `ScorecardView`.
 *
 *  Rendered as its own card rather than merged into the table header because
 *  the two halves of the payload answer different questions: the scorecard
 *  counts every session in the read, the table lists the ones the route
 *  returned, and `meta.truncated` is what tells them apart. Merging them would
 *  make a truncated page look like a fleet with three sessions.
 *
 *  The two `longest*` fields are optional on the type (`omitempty` pointers)
 *  and render as `—` when absent. They exist separately because R4.1 counts
 *  them as two findings: the longest *running query* and the longest *idle in
 *  transaction* are different problems, and the fixture's third row — idle in
 *  transaction, holding a transaction open for 964 s while its last query took
 *  4.2 s — is the case where collapsing them loses the finding entirely. */
function Scorecard({ resp }: { resp: ActiveQueriesResponse }) {
  const { scorecard } = resp;
  const states = Object.entries(scorecard.byState).sort((a, b) => b[1] - a[1]);

  return (
    <section className="card" data-testid="scorecard">
      <div className="card-head">
        <h2 className="card-title">Session summary</h2>
        <div className="card-actions">
          <span className="card-meta num">{scorecard.total} total</span>
        </div>
      </div>
      <div className="scorecard-grid">
        <div className="scorecard-cell">
          <span className="scorecard-label">By state</span>
          <ul className="scorecard-states">
            {states.length === 0 && <li>{NULL_VALUE}</li>}
            {states.map(([status, count]) => (
              <li key={status}>
                <span className="scorecard-state">{status}</span>
                <span className="num">{count}</span>
              </li>
            ))}
          </ul>
        </div>
        <LongestCell label="Longest query" row={scorecard.longestQuery} />
        <LongestCell label="Longest idle in transaction" row={scorecard.longestIdleInTransaction} />
      </div>
    </section>
  );
}

function LongestCell({ label, row }: { label: string; row?: SessionView }) {
  return (
    <div className="scorecard-cell">
      <span className="scorecard-label">{label}</span>
      {row === undefined ? (
        <span className="scorecard-value">{NULL_VALUE}</span>
      ) : (
        <div className="scorecard-longest">
          <span className="scorecard-value num" title={`pid ${row.pid}`}>
            {formatDurationMs(row.queryDurationSeconds * 1000)}
          </span>
          <span className="scorecard-query trunc" title={row.query}>
            {row.query}
          </span>
          <span className="scorecard-meta">
            pid {row.pid} · {row.database} · {row.userName}
          </span>
        </div>
      )}
    </div>
  );
}

/* ── one row, and its expansion ────────────────────────────────────────────── */

function SessionRow({
  row,
  columns,
  chain,
  confirming,
  onConfirm,
  onCancel,
  onConfirmed,
}: {
  row: SessionView;
  /** The visible column keys, in order — `useColumnVisibility`'s output, the
   *  same shape `QueryTableProps.columns` takes. */
  columns: readonly string[];
  chain: BlockingChainView | null;
  confirming: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  onConfirmed: () => void;
}): ReactElement {
  const [open, setOpen] = useState(false);
  const expandable = chain !== null || row.blockingPids.length > 0;

  return (
    <>
      <tr data-pid={row.pid}>
        <td className="expand-col">
          {expandable ? (
            <button
              type="button"
              className="btn btn-ghost btn-xs"
              aria-expanded={open}
              aria-label={`Blocking chain for pid ${row.pid}`}
              onClick={() => setOpen((v) => !v)}
            >
              {open ? "▾" : "▸"}
            </button>
          ) : (
            <span className="secondary" aria-label="No blockers">
              {NULL_VALUE}
            </span>
          )}
        </td>
        {columns.map((key) => (
          <td key={key} className={numericCol(key) ? "num" : undefined}>
            {cell(row, key, onConfirm)}
          </td>
        ))}
      </tr>
      {open && chain !== null && (
        <tr className="row-expansion" data-testid="chain-row">
          <td colSpan={columns.length + 1}>
            <ChainRows blocked={chain.blocked} blockers={chain.blockers} />
          </td>
        </tr>
      )}
      {open && chain === null && (
        <tr className="row-expansion" data-testid="chain-row">
          <td colSpan={columns.length + 1}>
            <p className="card-note">
              This row names {row.blockingPids.map((p) => p).join(", ")} as blocking it, and the
              response carried no chain for pid {row.pid}.
            </p>
          </td>
        </tr>
      )}
      {confirming && (
        <tr className="row-expansion" data-testid="confirm-row">
          <td colSpan={columns.length + 1}>
            <p className="card-note">
              Terminate pid {row.pid}? R4.5 requires a confirmed action and audits it to a log line
              with actor and target.
            </p>
            <button type="button" className="btn btn-danger" onClick={onConfirmed}>
              Confirm
            </button>
            <button type="button" className="btn" onClick={onCancel}>
              Cancel
            </button>
          </td>
        </tr>
      )}
    </>
  );
}

/** R4.4's chain, rendered as the row's expansion.
 *
 *  The blocked session is the row it is attached to, so it is named once and
 *  not repeated; the blockers are the payload. `blockers` is never null (no
 *  `omitempty` on the Go field), and an empty array is a real answer — the
 *  chain exists, the blocker set is empty — which renders as its own sentence
 *  rather than as silence, because silence is what an empty `blockers` and a
 *  missing chain both look like if nothing distinguishes them. */
function ChainRows({ blocked, blockers }: { blocked: SessionView; blockers: readonly SessionView[] }) {
  return (
    <div className="chain" data-testid="chain">
      <p className="chain-head">
        Blocking chain for pid <span className="num">{blocked.pid}</span>
      </p>
      {blockers.length === 0 ? (
        <p className="card-note">The chain reports no blockers for this session.</p>
      ) : (
        <ul className="chain-list">
          {blockers.map((b) => (
            <li key={b.pid} className="chain-item">
              <span className="num">{b.pid}</span>
              <span className="chain-query trunc" title={b.query}>
                {b.query}
              </span>
              <span className="chain-meta">
                {b.status} · {b.database} · {b.userName} ·{" "}
                {formatDurationMs(b.transactionDurationSeconds * 1000)} open
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/* ── cells ─────────────────────────────────────────────────────────────────── */

/** The columns whose cell is a number, so they get `class="num"` and the
 *  right-alignment every other numeric cell in the app has. Exported through
 *  `numericCol` rather than a per-column flag because `ColumnDef` has no
 *  `numeric` field — it is `QueryTable`'s `QueryColumn` that carries one, and
 *  the menu's type is the narrower one. */
function numericCol(key: string): boolean {
  return NUMERIC_COLS.has(key);
}

/** A column key's label. `useColumnVisibility` hands back keys and the table
 *  needs the label, so the lookup lives beside the list that defines both —
 *  the same reason `QueryTable` exports `QUERY_COLUMNS` rather than letting
 *  callers keep their own key→label map. */
function labelFor(key: string): string {
  return ACTIVE_COLUMNS.find((c) => c.key === key)?.label ?? key;
}

const NUMERIC_COLS = new Set([
  "pid",
  "sessionDurationSeconds",
  "transactionDurationSeconds",
  "queryDurationSeconds",
  "clientPort",
  "blockingPids",
]);

/** One cell, by column key. Every branch reads a field the DTO has; the only
 *  formatting is duration (the API's fields are seconds, `formatDurationMs`
 *  takes ms) and the timestamp (R10.21's local-zone rendering with the absolute
 *  UTC on hover). */
function cell(
  row: SessionView,
  key: string,
  /** R4.5's arm step, threaded to the one column that has an action. */
  onTerminate?: () => void,
): ReactElement | string {
  switch (key) {
    case "pid":
      return String(row.pid);
    case "query":
      return (
        <span className="query-cell trunc" title={row.query}>
          {row.query}
        </span>
      );
    case "queryType":
      return row.queryType;
    case "status":
      return row.status;
    case "sessionDurationSeconds":
      return formatDurationMs(row.sessionDurationSeconds * 1000);
    case "transactionDurationSeconds":
      return formatDurationMs(row.transactionDurationSeconds * 1000);
    case "queryDurationSeconds":
      return formatDurationMs(row.queryDurationSeconds * 1000);
    case "waitEventType":
      return row.waitEventType === "" ? NULL_VALUE : row.waitEventType;
    case "waitEvent":
      // `unknownWaitEvent` qualifies this cell: the pair is not in PG's own
      // pg_wait_events list, so the value is the collector's, not the server's.
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
    case "applicationName":
      return row.applicationName === "" ? NULL_VALUE : row.applicationName;
    case "userName":
      return row.userName;
    case "clientAddress":
      return row.clientAddress;
    case "clientPort":
      return row.clientPort;
    case "backendType":
      return row.backendType;
    case "queryid":
      return row.queryid ?? NULL_VALUE;
    case "blockingPids":
      return row.blockingPids.length === 0 ? NULL_VALUE : row.blockingPids.join(", ");
    case "action":
      return <ActionCell row={row} onTerminate={onTerminate} />;
    case "sourceTime":
      return <span title={utcTitle(row.sourceTime)}>{formatTimestamp(row.sourceTime)}</span>;
    default:
      return NULL_VALUE;
  }
}

/** The Action cell — R4.5's gate, rendered per the header's rule that the
 *  column is never silently blank. */
function ActionCell({
  row,
  onTerminate,
}: {
  row: SessionView;
  onTerminate?: () => void;
}): ReactElement {
  if (row.action === ACTION_TERMINATE) {
    return (
      <button type="button" className="btn btn-xs" onClick={onTerminate} data-action={row.action}>
        Terminate
      </button>
    );
  }
  // `disabled` and every future value: a disabled control carrying the string,
  // so the cell says what the server said instead of the page guessing.
  return (
    <span className="action-cell">
      <button type="button" className="btn btn-xs" disabled data-action={row.action}>
        Terminate
      </button>
      <span className="action-reason secondary" title="R12.8's --allow-actions gate">
        {row.action}
      </span>
    </span>
  );
}

/** A URL that names no cluster. The route is `scope: "cluster"` and the route
 *  itself cannot answer for the fleet, so this is the malformed-URL frame, not
 *  a loading state. */
function NoCluster({ state, onPatch }: PageProps<ActiveQueriesResponse>): ReactElement {
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="card-title">Active Queries</h2>
      </div>
      <p className="card-note">
        {state.cluster === null
          ? "This URL does not name a cluster."
          : `“${state.cluster}” is not a namespace/name pair.`}{" "}
        GET /api/v1/active-queries reads one cluster's pg_stat_activity, so there is no
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

/** The chain lookup: a chain is keyed by the blocked session, so a row's chain
 *  is the entry whose `blocked.pid` is this row's pid. */
function chainFor(chains: readonly BlockingChainView[], pid: number): BlockingChainView | null {
  return chains.find((c) => c.blocked.pid === pid) ?? null;
}
