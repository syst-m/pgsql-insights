/* R10.9's page shell (`requirements.md:422`, `web/src/routes.ts:224-234`).
 *
 * The chart's own maths is tested in `WaitEventsChart.test.tsx`; what this file
 * owns is the page's four clauses, in the order the page renders them:
 *
 * 1. **the shell** — `data-page`, the `<h1>` the route's `title:` promises
 *    (`web/src/routes.ts:226`), and the R10.21 stamp. The `data-page`/`h1` pair
 *    is also what `routes.test.tsx` will assert when the mount flips
 *    (`component: null` at `web/src/routes.ts:230`), so it is asserted here first.
 * 2. **the selection contract** — the chart and the drill read one `buckets`
 *    array, and the drill shows the bucket the chart marks
 *    `aria-current="true"`. Clicking a bar moves both together; a page where the
 *    outline and the table disagree is the bug this block closes.
 * 3. **the column set** — `DEFAULTS["wait-events"]` vs `WAIT_EVENTS_COLUMNS`.
 *    `normalizeSelection` silently drops a key with no `ColumnDef` behind it, so
 *    the two lists have to be asserted equal rather than assumed.
 * 4. **the state table** — empty and partial render the copy from
 *    `WAIT_EVENTS_COPY` instead of a chart, and the no-cluster URL renders the
 *    `NoCluster` card.
 *
 * Rendered directly, not through `<App>`: the mount is `routes.test.tsx`'s to
 * assert, and `PageProps<T>` exists so a page test can pass the props the shell
 * will — `fixtureOverride` among them, which is how every case here pins a
 * payload instead of racing a fetch. The `beforeEach` storage clear is the same
 * one `ActiveQueries.test.tsx:96-104` added after a persisted column set from
 * one test leaked into the next.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ClientConfig } from "../api/client";
import { FIXTURE_ACTIVE_QUERIES } from "../api/fixtures";
import type { ActiveQueriesResponse, SessionView } from "../api/types";
import { DEFAULTS } from "../components/ColumnMenu";
import { formatTimestamp } from "../lib/format";
import { DEFAULT_STATE, type UrlState } from "../state/urlState";
import {
  WAIT_EVENTS_COLUMNS,
  WAIT_EVENTS_DEFAULTS,
  WaitEventsPage,
  type WaitEventsPageProps,
} from "./WaitEvents";

const CFG: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };
const CLUSTER_ID = "open-webui/open-webui-pg";

/** `/c/:ns/:name/wait-events` (`web/src/routes.ts:232`) — `scope: "cluster"`,
 *  and the route takes no other params, so this is the whole URL. */
const URL_STATE: UrlState = {
  ...DEFAULT_STATE,
  page: "wait-events",
  cluster: CLUSTER_ID,
};

const LOCK = FIXTURE_ACTIVE_QUERIES.data[0]!;
const CLIENT = FIXTURE_ACTIVE_QUERIES.data[1]!;

function rowWith(row: SessionView, overrides: Partial<SessionView>): SessionView {
  return { ...row, ...overrides };
}

function renderPage(overrides: Partial<WaitEventsPageProps> = {}) {
  const onPatch = vi.fn();
  const props: WaitEventsPageProps = {
    state: URL_STATE,
    onPatch,
    cfg: CFG,
    fixtureOverride: FIXTURE_ACTIVE_QUERIES,
    ...overrides,
  };
  return { ...render(<WaitEventsPage {...props} />), onPatch };
}

/** The fixture with its payload replaced, for the states the shipped fixture
 *  cannot show (an empty set, a partial one). `meta` carries over so a test about
 *  one cell does not also move the header stamp. */
function respWith(data: SessionView[], meta?: Partial<ActiveQueriesResponse["meta"]>) {
  return {
    ...FIXTURE_ACTIVE_QUERIES,
    data,
    meta: { ...FIXTURE_ACTIVE_QUERIES.meta, ...meta },
  };
}

beforeEach(() => {
  try {
    globalThis.localStorage?.clear();
  } catch {
    // Accessing the property itself threw: storage is absent, so there is no
    // persisted selection to leak. Same fallback as `browserStorage()`.
  }
});

/* ── 1. the shell ──────────────────────────────────────────────────────────── */

describe("WaitEventsPage — the shell (R10.9, R10.21)", () => {
  it("renders the shell a cluster URL names", () => {
    const { container } = renderPage();
    expect(container.querySelector('[data-page="wait-events"]')).not.toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Wait Events" })).toBeInTheDocument();
  });

  it("renders the chart and the drill over the fixture's payload", () => {
    const { container } = renderPage();
    expect(container.querySelector('[data-panel="wait-events"]')).not.toBeNull();
    expect(container.querySelector('[data-panel="wait-events-drill"]')).not.toBeNull();
    // Three sessions, three distinct types, so three bars — the number on the
    // card's meta line and the number of bars are the same count.
    expect(container.querySelectorAll("svg path[fill^='var(--blue-']")).toHaveLength(3);
    expect(screen.getByText("3 sessions · 3 buckets")).toBeInTheDocument();
  });

  it("stamps the header with the payload's generatedAt, UTC on hover (R10.21)", () => {
    const { container } = renderPage();
    const meta = container.querySelector(".page-meta");
    expect(meta).not.toBeNull();
    // The rendered text is the local rendering; the hover title is the absolute
    // UTC string from the payload — R10.21's pair, both from one field.
    expect(meta!.getAttribute("title")).toBe(`${FIXTURE_ACTIVE_QUERIES.meta.generatedAt} UTC`);
    expect(meta!.textContent).toContain(formatTimestamp(FIXTURE_ACTIVE_QUERIES.meta.generatedAt));
  });

  it("shows the refresh interval the page actually polls at", () => {
    const { container } = renderPage();
    const indicator = container.querySelector(".refresh-indicator");
    expect(indicator).not.toBeNull();
    // Read from the rendered attribute, not a second `15000` literal: the page
    // passes the same constant to the hook and to the header, and a test that
    // retypes the number asserts its own copy instead of that pairing.
    expect(indicator!.getAttribute("data-poll-ms")).toBe("15000");
    expect(indicator!.textContent).toContain("refreshes every 15 s");
  });

  it("crumbs Fleet / cluster / Wait Events", () => {
    const { container } = renderPage();
    const crumbs = [...container.querySelectorAll(".breadcrumb a")].map((a) => a.textContent);
    expect(crumbs).toEqual(["Fleet", CLUSTER_ID]);
  });
});

/* ── 2. the selection contract ─────────────────────────────────────────────── */

describe("WaitEventsPage — the drill follows the chart", () => {
  it("opens on the first bucket and shows its sessions", () => {
    const { container } = renderPage();
    // Three buckets tied at one session each, so the sort's tie-break (label asc)
    // picks `Client`; the chart marks it and the drill shows it.
    const marked = container.querySelector('rect[role="button"][aria-current="true"]');
    expect(marked!.getAttribute("aria-label")).toBe("Client: 1 of 3 sessions");
    const drill = container.querySelector('[data-panel="wait-events-drill"]');
    expect(drill!.querySelector("h2")!.textContent).toBe("Client");
    const rows = drill!.querySelectorAll('[data-testid="wait-event-sessions"] tbody tr');
    expect(rows).toHaveLength(1);
    expect(rows[0]!.querySelector("th")).toBeNull();
    expect(rows[0]!.querySelector("td")!.textContent).toBe(String(CLIENT.pid));
  });

  it("moves the drill when a bar is clicked", () => {
    const { container } = renderPage();
    const hits = container.querySelectorAll('rect[role="button"]');
    expect(hits).toHaveLength(3);
    fireEvent.click(hits[2]!); // Lock
    const drill = container.querySelector('[data-panel="wait-events-drill"]');
    expect(drill!.querySelector("h2")!.textContent).toBe("Lock");
    expect(
      container.querySelector('rect[role="button"][aria-current="true"]')!.getAttribute("aria-label"),
    ).toBe("Lock: 1 of 3 sessions");
    expect(drill!.querySelector('[data-testid="wait-event-sessions"] tbody td')!.textContent).toBe(
      String(LOCK.pid),
    );
  });

  it("lists the per-event counts inside the bucket (R4.3's second tab)", () => {
    const { container } = renderPage();
    const rows = container.querySelectorAll('[data-testid="wait-event-rows"] tbody tr');
    expect(rows).toHaveLength(1);
    const cells = [...rows[0]!.querySelectorAll("th,td")].map((c) => c.textContent);
    expect(cells).toEqual(["ClientRead", "1", String(CLIENT.pid)]);
  });

  it("marks an unrecognised pair in the cell, not only on the bar", () => {
    // The bucket is chosen *from* the flag, so the row has to repeat the
    // qualification — the `?` + "Not in pg_wait_events" treatment the Active
    // Queries page gives the same cell (`web/src/pages/ActiveQueries.tsx:656-663`).
    const { container } = renderPage({
      fixtureOverride: respWith([rowWith(LOCK, { unknownWaitEvent: true })]),
    });
    const drill = container.querySelector('[data-panel="wait-events-drill"]');
    expect(drill!.querySelector("h2")!.textContent).toBe("Unrecognised — unrecognised");
    const cell = drill!.querySelectorAll('[data-testid="wait-event-sessions"] tbody td')[1]!;
    expect(cell.querySelector(".trunc-flag")?.getAttribute("aria-label")).toBe(
      "unrecognised wait event",
    );
    expect(cell.querySelector("[title='Not in pg_wait_events']")?.textContent).toBe(
      "transactionid?",
    );
    // The per-event row names the whole pair, per `activity.go:636`'s key.
    expect(
      container.querySelector('[data-testid="wait-event-rows"] tbody th')!.textContent,
    ).toBe("Lock/transactionid");
  });
});

/* ── 3. the column set ─────────────────────────────────────────────────────── */

describe("WaitEventsPage — the column set (R10.20)", () => {
  it("renders the six documented default columns", () => {
    const { container } = renderPage();
    const heads = [
      ...container.querySelectorAll('[data-testid="wait-event-sessions"] thead th'),
    ].map((th) => th.textContent);
    expect(heads).toEqual(["PID", "Wait event", "Query", "Status", "Database", "Action"]);
  });

  it("keeps every DEFAULTS key backed by a ColumnDef", () => {
    // `normalizeSelection` filters unknown keys out rather than erroring, so a
    // default key that no column declares renders as a missing column, not a
    // failing test. The two lists are exported precisely so this is assertable.
    const declared = new Set(WAIT_EVENTS_COLUMNS.map((c) => c.key));
    for (const key of WAIT_EVENTS_DEFAULTS) {
      expect(declared.has(key), `WAIT_EVENTS_DEFAULTS names "${key}" with no ColumnDef`).toBe(true);
    }
    expect(DEFAULTS["wait-events"]).toEqual(WAIT_EVENTS_DEFAULTS);
  });

  it("renders the cell values the payload carries", () => {
    const { container } = renderPage();
    const cells = [
      ...container.querySelectorAll('[data-testid="wait-event-sessions"] tbody tr')[0]!
        .children,
    ].map((td) => td.textContent);
    expect(cells).toEqual([
      String(CLIENT.pid),
      "ClientRead",
      CLIENT.query,
      "idle in transaction",
      "orders",
      "disabled",
    ]);
  });
});

/* ── 4. the state table ────────────────────────────────────────────────────── */

describe("WaitEventsPage — the states (R10.12, R10.22)", () => {
  it("renders the empty copy instead of a chart", () => {
    const { container } = renderPage({ fixtureOverride: respWith([]) });
    const panel = container.querySelector('[data-state="empty"]');
    expect(panel).not.toBeNull();
    expect(panel!.querySelector(".state-title")!.textContent).toBe("No sessions to bucket");
    // No chart, no drill: the page is the state panel and nothing else.
    expect(container.querySelector('[data-panel="wait-events"]')).toBeNull();
    expect(container.querySelector('[data-panel="wait-events-drill"]')).toBeNull();
  });

  it("renders the header over the state panel", () => {
    // R10.21's stamp is not conditional on having data: a page in `empty` still
    // says when it last read, or the reader cannot tell "empty" from "stale".
    const { container } = renderPage({ fixtureOverride: respWith([]) });
    expect(container.querySelector(".page-head")).not.toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Wait Events" })).toBeInTheDocument();
  });

  it("banners a partial payload with the composed body (R10.22)", () => {
    const { container } = renderPage({
      fixtureOverride: respWith(FIXTURE_ACTIVE_QUERIES.data, { loading: ["orders"] }),
    });
    const banner = container.querySelector(".banner[role='status']");
    expect(banner).not.toBeNull();
    expect(banner!.querySelector("strong")!.textContent).toBe("Partial results");
    // The body is composed from the payload, not the table's fixed string: it has
    // to name the namespace that is still loading.
    expect(banner!.querySelector("span")!.textContent).toBe(
      "3 sessions served. 1 namespace still in the first discovery pass (orders).",
    );
    // A partial payload still renders the chart — the banner is over real content.
    expect(container.querySelector('[data-panel="wait-events"]')).not.toBeNull();
  });

  it("renders the no-cluster card and offers the way back", () => {
    const { container, onPatch } = renderPage({
      state: { ...DEFAULT_STATE, page: "wait-events" },
    });
    expect(container.querySelector('[data-page="wait-events"]')).not.toBeNull();
    expect(screen.getByText(/This URL does not name a cluster\./)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back to Fleet" }));
    expect(onPatch).toHaveBeenCalledWith({ page: "fleet", cluster: null });
  });
});
