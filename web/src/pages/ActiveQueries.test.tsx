/* R10.8's page shell — the four clauses it has to render, and the one piece of
 * local state that ties them together.
 *
 * The suite is organised by R10.8's own sentence (`19eb117:requirements.md:421`),
 * because that is the contract a later reader checks the file against:
 *
 * 1. **the live table (R4.1)** — the shell, the row set, and the column set the
 *    menu hands the table. The column assertion is the interesting one: the
 *    default set is nine of nineteen, and `useColumnVisibility` *silently drops*
 *    a `DEFAULTS` key that no `ColumnDef` declares, so a typo in the default
 *    list is a column that quietly stops rendering rather than a failing test —
 *    unless the test asserts the two lists agree.
 * 2. **the 15 s auto-refresh indicator** — and the pairing the header documents:
 *    the number rendered and the number passed to the hook are one constant, so
 *    the test reads `data-poll-ms` rather than a second literal.
 * 3. **row expansion to the blocking chain** — R4.2's chain as a row opened
 *    (`ChainRows` lives in the page file, so its rendering is tested here), plus
 *    the two cases the payload distinguishes: a row with a chain, and a row
 *    naming blockers with no chain entry.
 * 4. **the Terminate action behind the R4.5 gate** — the two Go action values
 *    (`actions.go:47`/`:54`) rendered as an enabled and a disabled
 *    control, and R4.5's *confirmed* step. The gate is R4.5, not R4.6; see the
 *    page's header for why. This clause has two halves now: the arm (fixture
 *    arm, no network) and the POST it fires (`client.ts:667`, A4's `efdabb3`),
 *    and only the second one needs a `fetch` stub.
 *
 * A sixth block covers R10.18's two halves on this page — the density toggle and
 * the `data-density` the table carries from the same value, and the spacer rows
 * that keep a 500-row payload's scrollbar the length of the payload rather than
 * of the 39 rows the window mounts.
 *
 * The URL half (`activeParamsFrom`) is A4's function with its own suite in
 * `state/useActiveQueries.test.ts`; what this file owns is its render half — the
 * no-cluster frame — because a page that renders the fixture for an
 * address-less URL passes every other test here.
 *
 * Rendered directly, not through `<App>` even though the shell mounts this page
 * now (`routes.ts:221`'s `component: ActiveQueriesPage`, A4's `a5e5be0`): the
 * mount is `routes.test.tsx`'s to assert, and `PageProps<T>` exists precisely so
 * a page test can pass the props the shell will — `fixtureOverride` among them,
 * which is how most of the cases here pin a payload instead of racing a fetch.
 * The one block that does race is the terminate POST, and it stubs `fetch`
 * because a mutation has no fixture-override equivalent.
 */

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ClientConfig } from "../api/client";
import { FIXTURE_ACTIVE_QUERIES, FIXTURE_TERMINATE_SESSION } from "../api/fixtures";
import type { ActiveQueriesResponse, SessionView } from "../api/types";
import { DEFAULTS } from "../components/ColumnMenu";
import { buildPath } from "../routes";
import { DEFAULT_STATE, type UrlState } from "../state/urlState";
import { formatDurationMs, formatTimestamp } from "../lib/format";
import {
  ACTIVE_COLUMNS,
  ActiveQueriesPage,
  type ActiveQueriesPageProps,
} from "./ActiveQueries";

const CFG: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };
const CLUSTER_ID = "open-webui/open-webui-pg";

/** The URL this page is mounted from: `routes.ts:215-225`'s
 *  `/c/:ns/:name/active`, which is `scope: "cluster"` with no other params. */
const URL_STATE: UrlState = {
  ...DEFAULT_STATE,
  page: "active",
  cluster: CLUSTER_ID,
};

/** The fixture's first two rows, named. Spreading a fixture row rather than
 *  writing one out keeps these cases honest about the *other* fields: a row
 *  built by hand would pass while the real type gained a field. The `!` is the
 *  fixture's own contract (three rows, asserted by the table test below), not a
 *  way to silence a real `undefined`. */
const ROW_41288 = FIXTURE_ACTIVE_QUERIES.data[0]!;
const ROW_41102 = FIXTURE_ACTIVE_QUERIES.data[1]!;

/** Every render starts from the documented default column set.
 *
 *  `useColumnVisibility` (`web/src/components/ColumnMenu.tsx:206`) reads a persisted
 *  selection from `browserStorage()` (`web/src/components/ThemeToggle.tsx:61`) before
 *  it falls back to `DEFAULTS["active-queries"]` (`ColumnMenu.tsx:95`, the nine
 *  this file's header names), and any test that drives the ColumnMenu writes one.
 *  Without this clear, a test renders whatever the previous test left in storage —
 *  which is why the R10.18 windowing case below saw eleven visible columns, a
 *  colspan of twelve with the expander column, while the documented default is
 *  nine and ten. Storage is only live where `globalThis.localStorage` resolves to
 *  a working store, so the suite's result was a property of the runner's node:
 *  `.github/workflows/ci.yml:164`'s node 22 has jsdom's per-file storage and
 *  fails, a node whose global throws (the state `ThemeToggle.tsx:61` documents)
 *  has none and passes.
 */
beforeEach(() => {
  try {
    globalThis.localStorage?.clear();
  } catch {
    // Accessing the property itself threw: storage is absent, so there is no
    // persisted selection to leak. Same fallback as `browserStorage()` (:61).
  }
});

function renderPage(overrides: Partial<ActiveQueriesPageProps> = {}) {
  const onPatch = vi.fn();
  const props: ActiveQueriesPageProps = {
    state: URL_STATE,
    onPatch,
    cfg: CFG,
    fixtureOverride: FIXTURE_ACTIVE_QUERIES,
    ...overrides,
  };
  return { ...render(<ActiveQueriesPage {...props} />), onPatch };
}

/** The fixture with its payload replaced, for the cases the shipped fixture
 *  cannot show (an enabled Terminate, an empty session set). `meta` and
 *  `scorecard` carry over so a test about one cell does not also change the
 *  header stamp. */
function respWith(data: SessionView[]): ActiveQueriesResponse {
  return { ...FIXTURE_ACTIVE_QUERIES, data };
}

/** The fixture's first row, re-labelled. */
function rowWith(row: SessionView, overrides: Partial<SessionView>): SessionView {
  return { ...row, ...overrides };
}

/* ── 1. the live table (R10.8's first clause, R4.1's read) ────────────────── */

describe("ActiveQueriesPage — the table (R10.8's first clause)", () => {
  it("renders the shell for a cluster URL", () => {
    const { container } = renderPage();
    expect(container.querySelector('[data-page="active"]')).not.toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Active Queries" })).toBeInTheDocument();
    expect(screen.getByTestId("active-table")).toBeInTheDocument();
  });

  it("renders one row per session in the payload", () => {
    const { container } = renderPage();
    // The fixture's three rows, keyed by pid. `data-pid` is the row's identity
    // for the expansion and the confirm step, so it is also what the test reads.
    const pids = [...container.querySelectorAll("tbody tr[data-pid]")].map((tr) =>
      tr.getAttribute("data-pid"),
    );
    expect(pids).toEqual(FIXTURE_ACTIVE_QUERIES.data.map((r) => String(r.pid)));
    expect(pids).toEqual(["41288", "41102", "41304"]);
  });

  it("renders the nine documented default columns, not all nineteen", () => {
    const { container } = renderPage();
    const heads = [...container.querySelectorAll("thead th")].map((th) => th.textContent?.trim());
    // One `expand-col` header plus one per visible key. The expand column is
    // not in DEFAULTS — it is not a data column, it is R10.8's third clause.
    expect(heads[0]).toBe("Blocking chain");
    expect(heads.slice(1)).toEqual([
      "PID",
      "Query",
      "Type",
      "Status",
      "Query duration",
      "Wait event",
      "Database",
      "User name",
      "Action",
    ]);
    // The hidden-by-default ones are still reachable through the menu, which is
    // R10.20's contract; the count on the button is the visible proof.
    expect(screen.getByText("9/19")).toBeInTheDocument();
  });

  it("keeps every DEFAULTS key backed by a ColumnDef", () => {
    // `normalizeSelection` filters unknown keys out rather than erroring, so a
    // `DEFAULTS` entry that no `ACTIVE_COLUMNS` entry declares renders as a
    // missing column and nothing else. The type can't catch it — DEFAULTS is
    // `Record<string, readonly string[]>`, not a keyed map over the columns.
    const declared = new Set(ACTIVE_COLUMNS.map((c) => c.key));
    const defaults = DEFAULTS["active-queries"] ?? [];
    expect(defaults.length).toBeGreaterThan(0);
    for (const key of defaults) {
      expect(
        declared.has(key),
        `DEFAULTS["active-queries"] names "${key}", which ACTIVE_COLUMNS does not declare`,
      ).toBe(true);
    }
  });

  it("renders the R4.6 columns when the menu turns them on", () => {
    // R4.6 (`requirements.md:333`) is satisfied by two columns — the separate
    // transaction age and `backend_type` — not by the Terminate button. They are
    // off by default, so the only way to prove the page can answer R4.6 is to
    // ask the menu for them and read the cells.
    const { container } = renderPage();
    fireEvent.click(screen.getByRole("button", { name: /Columns/ }));
    fireEvent.click(screen.getByLabelText("Transaction duration"));
    fireEvent.click(screen.getByLabelText("Backend type"));

    const heads = [...container.querySelectorAll("thead th")].map((th) => th.textContent?.trim());
    expect(heads).toEqual(expect.arrayContaining(["Transaction duration", "Backend type"]));

    // Row 41102: a 4.2 s query holding a transaction open for 964.6 s — the row
    // that is the whole argument for keeping the two durations apart.
    const row = container.querySelector('tr[data-pid="41102"]');
    expect(row?.textContent).toContain(formatDurationMs(964.6 * 1000));
    expect(row?.textContent).toContain("client backend");
  });
});

/* ── 2. the 15 s auto-refresh indicator ───────────────────────────────────── */

describe("ActiveQueriesPage — the refresh indicator (R10.8's second clause)", () => {
  it("prints the interval the hook polls at", () => {
    const { container } = renderPage();
    const indicator = container.querySelector(".refresh-indicator");
    expect(indicator).not.toBeNull();
    // `data-poll-ms` is the constant itself, so this asserts the value handed to
    // `useActiveQueries` and not a second copy of the number in the markup.
    expect(indicator?.getAttribute("data-poll-ms")).toBe("15000");
    expect(indicator?.textContent).toContain("refreshes every 15 s");
  });

  it("stamps the payload's own generatedAt, local-zone with UTC on hover (R10.21)", () => {
    const { container } = renderPage();
    const meta = container.querySelector(".page-meta");
    const generatedAt = FIXTURE_ACTIVE_QUERIES.meta.generatedAt;
    expect(meta?.textContent).toContain(`Data as of ${formatTimestamp(generatedAt)}`);
    expect(meta?.getAttribute("title")).toBe(`${generatedAt} UTC`);
  });

  it("offers no range control", () => {
    // `client.ts:614`'s params are namespace+cluster only: pg_stat_activity
    // answers "now", so a time window would be R10.15's question about the query
    // store rendered on a page that cannot ask it.
    const { container } = renderPage();
    expect(container.querySelector(".range-control")).toBeNull();
  });
});

/* ── the scorecard (R4.1's summary header) ────────────────────────────────── */

describe("ActiveQueriesPage — the scorecard", () => {
  it("counts every session by state and totals them", () => {
    renderPage();
    const card = screen.getByTestId("scorecard");
    expect(card.textContent).toContain(`${FIXTURE_ACTIVE_QUERIES.scorecard.total} total`);
    // The fixture's read: two active, one idle in transaction, listed
    // highest-count first (the sort is the page's, not the payload's order).
    const states = [...card.querySelectorAll(".scorecard-states li")].map((li) =>
      li.textContent?.trim(),
    );
    expect(states).toEqual(["active2", "idle in transaction1"]);
  });

  it("renders the two longest cells separately", () => {
    // R4.1 counts them as two findings, and the fixture's row 41102 is the case
    // where collapsing them loses the finding: a 4.2 s query holding a
    // transaction open for 964 s.
    renderPage();
    const card = screen.getByTestId("scorecard");
    const cells = [...card.querySelectorAll(".scorecard-cell")];
    const labels = cells.map((c) => c.querySelector(".scorecard-label")?.textContent);
    expect(labels).toEqual(["By state", "Longest query", "Longest idle in transaction"]);

    const longestQuery = FIXTURE_ACTIVE_QUERIES.scorecard.longestQuery;
    const longestIdle = FIXTURE_ACTIVE_QUERIES.scorecard.longestIdleInTransaction;
    expect(longestQuery).toBeDefined();
    expect(longestIdle).toBeDefined();
    expect(cells[1]?.textContent).toContain(formatDurationMs((longestQuery?.queryDurationSeconds ?? 0) * 1000));
    expect(cells[1]?.textContent).toContain(`pid ${longestQuery?.pid}`);
    expect(cells[2]?.textContent).toContain(formatDurationMs((longestIdle?.queryDurationSeconds ?? 0) * 1000));
    expect(cells[2]?.textContent).toContain(`pid ${longestIdle?.pid}`);
  });

  it("renders an absent longest cell as the null marker", () => {
    // Both `longest*` fields are `omitempty` pointers on the Go side, so an
    // absent one is a normal response, not a missing field to guess at.
    const resp: ActiveQueriesResponse = {
      ...FIXTURE_ACTIVE_QUERIES,
      scorecard: { byState: { active: 1 }, total: 1 },
      data: [ROW_41288],
    };
    renderPage({ fixtureOverride: resp });
    const card = screen.getByTestId("scorecard");
    expect(card.textContent).toContain("Longest query");
    expect(card.textContent).toContain("Longest idle in transaction");
    expect(card.querySelectorAll(".scorecard-longest")).toHaveLength(0);
  });
});

/* ── 4. the Terminate action behind the R4.5 gate ─────────────────────────── */

describe("ActiveQueriesPage — the Action cell (R10.8's fourth clause, R4.5's gate)", () => {
  it("renders the fixture's disabled actions as disabled controls, not absent ones", () => {
    // R4.1's rule for the column is that it is "never silently blank": an absent
    // button and a disabled one are two different answers to "can I kill this
    // backend", and `action` exists so the server, not the page, gives it.
    const { container } = renderPage();
    const buttons = [...container.querySelectorAll('tbody button[data-action="disabled"]')];
    expect(buttons).toHaveLength(FIXTURE_ACTIVE_QUERIES.data.length);
    for (const button of buttons) {
      expect(button.hasAttribute("disabled")).toBe(true);
      expect(button.textContent).toBe("Terminate");
    }
    // The reason rides beside the control, titled with the gate it is under.
    const reasons = [...container.querySelectorAll(".action-reason")];
    expect(reasons).toHaveLength(FIXTURE_ACTIVE_QUERIES.data.length);
    expect(reasons[0]?.getAttribute("title")).toBe("R12.8's --allow-actions gate");
    expect(reasons[0]?.textContent).toBe("disabled");
  });

  it("renders terminate as an enabled control", () => {
    const resp = respWith([rowWith(ROW_41288, { action: "terminate" })]);
    const { container } = renderPage({ fixtureOverride: resp });
    const button = container.querySelector('tbody button[data-action="terminate"]');
    expect(button).not.toBeNull();
    expect(button?.hasAttribute("disabled")).toBe(false);
    expect(button?.textContent).toBe("Terminate");
    // An enabled action has no reason string beside it — the reason is what the
    // disabled variant carries.
    expect(container.querySelector(".action-reason")).toBeNull();
  });

  it("renders an unrecognised action string as a disabled control carrying the string", () => {
    // The Go side has exactly two values today, but the rule is about the third
    // one arriving: the cell shows what the server said instead of the page
    // guessing that an unknown value means "no action".
    const resp = respWith([rowWith(ROW_41102, { action: "pause" })]);
    const { container } = renderPage({ fixtureOverride: resp });
    const button = container.querySelector("tbody .action-cell button");
    expect(button?.hasAttribute("disabled")).toBe(true);
    expect(button?.getAttribute("data-action")).toBe("pause");
    expect(container.querySelector(".action-reason")?.textContent).toBe("pause");
  });

  it("arms a confirm step instead of firing on click (R4.5's confirmed action)", () => {
    const resp = respWith([rowWith(ROW_41288, { action: "terminate" })]);
    const { container } = renderPage({ fixtureOverride: resp });
    expect(container.querySelector('[data-testid="confirm-row"]')).toBeNull();

    fireEvent.click(container.querySelector('tbody button[data-action="terminate"]')!);
    const confirm = container.querySelector('[data-testid="confirm-row"]');
    expect(confirm).not.toBeNull();
    expect(confirm?.textContent).toContain(`Terminate pid ${ROW_41288.pid}`);

    // Cancel disarms without acting.
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(container.querySelector('[data-testid="confirm-row"]')).toBeNull();
  });

  it("arms one row at a time", () => {
    // Two rows armed by one click would render two confirmations for one
    // action, so `confirmPid` is a single value rather than a set.
    const resp = respWith([
      rowWith(ROW_41288, { action: "terminate" }),
      rowWith(ROW_41102, { action: "terminate" }),
    ]);
    const { container } = renderPage({ fixtureOverride: resp });
    const buttons = container.querySelectorAll('tbody button[data-action="terminate"]');
    expect(buttons).toHaveLength(2);
    fireEvent.click(buttons[0]!);
    expect(container.querySelectorAll('[data-testid="confirm-row"]')).toHaveLength(1);
    expect(container.querySelector('tr[data-pid="41288"] + tr[data-testid="confirm-row"]')).not.toBeNull();
  });

  it("disarms on confirm", () => {
    // Confirming clears the arm on *both* outcomes of the mutation — the
    // `setConfirmPid(null)` in `terminate()` runs before the POST settles — so
    // a row never offers the same action twice while one is in flight. The POST
    // itself is the next block's subject.
    const resp = respWith([rowWith(ROW_41288, { action: "terminate" })]);
    const { container } = renderPage({ fixtureOverride: resp });
    fireEvent.click(container.querySelector('tbody button[data-action="terminate"]')!);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    expect(container.querySelector('[data-testid="confirm-row"]')).toBeNull();
  });
});

/* ── 2b. the terminate POST (R4.5's mutation half, `client.ts:667`) ───────── */

describe("ActiveQueriesPage — the terminate POST (R4.5's mutation half)", () => {
  // The http arm, because a mutation has no `fixtureOverride` equivalent: the
  // prop pins what the *page* renders, and the only way to observe what the page
  // *sends* is to answer the request. `delayMs: 0` keeps the fixture arm's
  // timer out of the picture too.
  const HTTP: ClientConfig = { source: "http", apiBase: "", fixture: "fleet", delayMs: 0 };
  const TERM = respWith([rowWith(ROW_41288, { action: "terminate" })]);
  const realFetch = globalThis.fetch;

  /** Answer the list GET with `list` and the terminate POST with the terminate
   *  fixture, recording every call as `"<METHOD> <url>"` — the URL is the part
   *  of R4.5 the page owns (`{pid}` in the path, `confirm=true` in the query
   *  string), and it is the part a mocked fetcher would let slide. `defer` holds
   *  the POST open so the in-flight frame is observable before it resolves. */
  function stubHttp(
    list: ActiveQueriesResponse,
    post: { ok?: boolean; status?: number; statusText?: string; defer?: boolean } = {},
  ): { calls: string[]; settle: () => void } {
    const calls: string[] = [];
    let settlePost: (() => void) | null = null;
    globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      const isPost = method === "POST";
      calls.push(`${method} ${url}`);
      const text = JSON.stringify(isPost ? FIXTURE_TERMINATE_SESSION : list);
      const ok = isPost ? post.ok ?? true : true;
      const res = {
        ok,
        status: isPost ? post.status ?? 200 : 200,
        statusText: isPost ? post.statusText ?? (ok ? "OK" : "Forbidden") : "OK",
        text: async () => text,
        json: async () => JSON.parse(text) as unknown,
      } as unknown as Response;
      if (isPost && post.defer === true) {
        return new Promise<Response>((resolve) => {
          settlePost = () => resolve(res);
        });
      }
      return Promise.resolve(res);
    }) as typeof globalThis.fetch;
    return { calls, settle: () => settlePost?.() };
  }

  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  it("posts the confirmed pid with confirm=true, then re-reads the list", async () => {
    const stub = stubHttp(TERM);
    const { container } = renderPage({ cfg: HTTP, fixtureOverride: TERM });

    fireEvent.click(container.querySelector('tbody button[data-action="terminate"]')!);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

    // One string asserts the whole request the page builds: the pid in the path
    // and not the query, the pair the route needs for scoping, and `confirm=true`
    // present at all — the literal that is R4.5's confirmed action spelled in a
    // type, so a page that fired without arming the dialog cannot produce it. The
    // order is `search()`'s (`client.ts:197` preserves the object's own key order
    // rather than sorting), so it is part of the claim. The `/terminate` suffix is
    // `client.ts:667`'s and the contract's (`requirements.md:332`,
    // `internal/api/server.go:230` (`Terminate`)): this assertion was first written against the
    // suffix-less URL the fetcher had and tightened to the exact string when A4's
    // `9790fda` added it, which is the pair the api suite asserts too at
    // `client.test.ts:1567-1570` (the URL string at :1568, the method at :1570).
    await waitFor(() =>
      expect(stub.calls).toContain(
        `POST /api/v1/sessions/${ROW_41288.pid}/terminate?namespace=open-webui&cluster=open-webui-pg&confirm=true`,
      ),
    );
    // And the re-read the flow ends in: the initial GET, then a second one after
    // the POST resolved. Asserting the count is what proves the success path
    // refetched — the render itself looks the same either way.
    await waitFor(() => expect(stub.calls.filter((c) => c.startsWith("GET"))).toHaveLength(2));
  });

  it("disables the row's button while the POST is in flight", async () => {
    const stub = stubHttp(TERM, { defer: true });
    const { container } = renderPage({ cfg: HTTP, fixtureOverride: TERM });
    fireEvent.click(container.querySelector('tbody button[data-action="terminate"]')!);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

    // `pendingPid` is what disables it, not the read's `phase`: a terminate in
    // flight and a list being refetched are two different facts, and the row's
    // button needs the first one or a second click sends a second POST at a
    // backend the server is already killing.
    const button = container.querySelector<HTMLButtonElement>(
      'tr[data-pid="41288"] button[data-action="terminate"]',
    )!;
    expect(button.disabled).toBe(true);
    expect(button.getAttribute("data-terminating")).toBe("true");
    expect(button.textContent).toBe("Terminating…");

    act(() => stub.settle());
    await waitFor(() => expect(button.disabled).toBe(false));
  });

  it("surfaces a refused terminate as an alert, not a silent row", async () => {
    stubHttp(TERM, { ok: false, status: 403, statusText: "Forbidden" });
    renderPage({ cfg: HTTP, fixtureOverride: TERM });
    fireEvent.click(screen.getByRole("button", { name: "Terminate" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    // `describeFailure`'s line, so the verb is the POST's and not the list GET's
    // — the reason `client.ts:706` takes a method instead of hardcoding "GET",
    // and the reason the mutation gets its own `role="alert"` rather than
    // borrowing the read's `role="status"` banner.
    expect(screen.getByRole("alert").textContent).toContain("POST → 403 Forbidden");
  });
});

/* ── 3. row expansion to the blocking chain (R4.2's render) ───────────────── */

describe("ActiveQueriesPage — the row expansion (R10.8's third clause)", () => {
  it("expands a row that has a chain and renders its blockers", () => {
    const { container } = renderPage();
    // Closed by default: the chain is a row opened, not a section of the page.
    expect(container.querySelectorAll('[data-testid="chain"]')).toHaveLength(0);

    fireEvent.click(screen.getByLabelText(`Blocking chain for pid ${ROW_41288.pid}`));
    const chain = screen.getByTestId("chain");
    expect(chain.textContent).toContain(`Blocking chain for pid ${ROW_41288.pid}`);
    // The fixture's chain: 41288 blocked by 41102.
    expect(container.querySelector('[data-pid="41288"] + tr[data-testid="chain-row"]')).not.toBeNull();
    expect(chain.textContent).toContain(String(ROW_41102.pid));
    expect(chain.textContent).toContain(ROW_41102.query);
    // The blocker's own duration renders in the chain, which is why the chain is
    // rows and not a pid list.
    expect(chain.textContent).toContain(formatDurationMs(ROW_41102.transactionDurationSeconds * 1000));
  });

  it("collapses again on a second click", () => {
    const { container } = renderPage();
    const button = screen.getByLabelText(`Blocking chain for pid ${ROW_41288.pid}`);
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(button);
    expect(container.querySelectorAll('[data-testid="chain-row"]')).toHaveLength(0);
  });

  it("says so when a row names blockers the response has no chain for", () => {
    // The cross-check the header documents: `blockingPids` (R4.4's edge list)
    // and `blockingChains` (R4.2's chains) are separate fields, and "blocked by
    // 99" beside "the API told me nothing about 99" are different answers.
    // `blockingChains: []` is the point of the case: the row still names a
    // blocker and the response has no chain for it. Reusing the fixture's chains
    // here would find the real 41288→41102 entry and render that instead.
    const resp: ActiveQueriesResponse = {
      ...FIXTURE_ACTIVE_QUERIES,
      data: [rowWith(ROW_41288, { blockingPids: [99] })],
      blockingChains: [],
    };
    const { container } = renderPage({ fixtureOverride: resp });
    fireEvent.click(screen.getByLabelText(`Blocking chain for pid ${ROW_41288.pid}`));
    const row = container.querySelector('tr[data-pid="41288"] + tr[data-testid="chain-row"]');
    expect(row?.textContent).toContain("names 99 as blocking it");
    expect(row?.textContent).toContain(`no chain for pid ${ROW_41288.pid}`);
    expect(container.querySelectorAll('[data-testid="chain"]')).toHaveLength(0);
  });

  it("renders a chain with an empty blocker set as a sentence, not silence", () => {
    // `blockers` has no `omitempty`, so an empty array is a real answer — the
    // chain exists and the blocker set is empty. Silence is what a *missing*
    // chain looks like, and the two have to be distinguishable.
    const resp: ActiveQueriesResponse = {
      ...FIXTURE_ACTIVE_QUERIES,
      data: [rowWith(ROW_41288, { blockingPids: [] })],
      blockingChains: [{ blocked: ROW_41288, blockers: [] }],
    };
    const { container } = renderPage({ fixtureOverride: resp });
    // The chain renders only when the row is open — the expansion is the render.
    fireEvent.click(screen.getByLabelText(`Blocking chain for pid ${ROW_41288.pid}`));
    expect(screen.getByTestId("chain").textContent).toContain("The chain reports no blockers");
    expect(container.querySelector('[data-pid="41288"] + tr[data-testid="chain-row"]')).not.toBeNull();
  });

  it("renders no expand control for a row with neither a chain nor blockers", () => {
    const { container } = renderPage({
      fixtureOverride: respWith([rowWith(ROW_41102, { blockingPids: [] })]),
    });
    // 41102 is a blocker, not blocked: no chain entry names it as `blocked` and
    // its own `blockingPids` is empty, so there is nothing to expand.
    expect(screen.queryByLabelText(`Blocking chain for pid ${ROW_41102.pid}`)).toBeNull();
    expect(container.querySelector('tr[data-pid="41102"] .expand-col')?.textContent).toBe("—");
  });
});

/* ── the URL half ─────────────────────────────────────────────────────────── */

describe("ActiveQueriesPage — a URL that names no cluster", () => {
  it("renders the no-cluster frame instead of the page", () => {
    // `routes.ts:215-225` is `scope: "cluster"`; `pg_stat_activity` is
    // per-instance, so there is no fleet-wide form of this page to fall back to.
    const { container, onPatch } = renderPage({
      state: { ...DEFAULT_STATE, page: "active" },
      fixtureOverride: undefined,
    });
    expect(container.querySelector('[data-page="active"]')).toBeNull();
    expect(screen.getByText(/This URL does not name a cluster\./)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back to Fleet" }));
    expect(onPatch).toHaveBeenCalledWith({ page: "fleet", cluster: null });
  });

  it("names a malformed cluster instead of hiding it", () => {
    // `activeParamsFrom` rejects a cluster with no `namespace/name` pair, and
    // the frame quotes the string it rejected — a page that says "no cluster"
    // about a URL that clearly names something sends the reader to the wrong bug.
    const { container } = renderPage({
      state: { ...DEFAULT_STATE, page: "active", cluster: "open-webui-pg" },
      fixtureOverride: undefined,
    });
    expect(container.querySelector('[data-page="active"]')).toBeNull();
    expect(screen.getByText(/“open-webui-pg” is not a namespace\/name pair\./)).toBeInTheDocument();
  });

  it("links the breadcrumb back to the cluster the URL names", () => {
    const { container } = renderPage();
    const link = container.querySelector<HTMLAnchorElement>(
      `a.cluster-link[href="${buildPath("cluster", CLUSTER_ID, null)}"]`,
    );
    expect(link).not.toBeNull();
    expect(link?.textContent).toBe(CLUSTER_ID);
  });
});

/* ── the states R10.12 names ──────────────────────────────────────────────── */

describe("ActiveQueriesPage — R10.12's named states", () => {
  it("renders the named empty state for a cluster with nothing running", () => {
    // An empty session set is this page's good news and still a named screen:
    // the query store's emptiness is about a window, this one is about a moment.
    const resp: ActiveQueriesResponse = {
      ...FIXTURE_ACTIVE_QUERIES,
      data: [],
      scorecard: { byState: {}, total: 0 },
      blockingChains: [],
    };
    const { container } = renderPage({ fixtureOverride: resp });
    const panel = container.querySelector('.state-panel[data-state="empty"]');
    expect(panel).not.toBeNull();
    expect(panel?.textContent).toContain("No active sessions");
    // The table is not rendered alongside it — the state panel replaces it.
    expect(container.querySelector('[data-testid="active-table"]')).toBeNull();
  });

  it("renders the partial banner composed from the payload (R10.22)", () => {
    // The partial needs a `meta.loading` (or `meta.failed`) entry, not just a
    // truncated flag — see the next case. `copyFor` composes the sentence from
    // the response rather than using ACTIVE_COPY's fixed body, so the served
    // count in the banner is `data.length` (R8.4's rule, enforced by
    // `partialBanner` reading the source, not `meta.total`). The noun is
    // `SESSION_NOUN`, passed at `web/src/pages/ActiveQueries.tsx:216`, so the sentence counts
    // sessions — the page's own subject — not the clusters the fleet counts.
    const resp: ActiveQueriesResponse = {
      ...FIXTURE_ACTIVE_QUERIES,
      meta: {
        ...FIXTURE_ACTIVE_QUERIES.meta,
        truncated: true,
        nextCursor: "eyJvZmZzZXQiOjN9",
        loading: ["open-webui"],
      },
    };
    const { container } = renderPage({ fixtureOverride: resp });
    expect(container.querySelector('.state-panel[data-state="empty"]')).toBeNull();
    const banner = container.querySelector(".banner");
    expect(banner).not.toBeNull();
    expect(banner?.textContent).toContain("Partial results");
    expect(banner?.textContent).toContain("3 sessions served, more pages available");
    expect(banner?.textContent).toContain("1 namespace still in the first discovery pass (open-webui)");
    expect(container.querySelector('[data-testid="active-table"]')).not.toBeNull();
  });

  it("does not read meta.truncated alone as a partial render", () => {
    // R8.10: `truncated` means there is another page — a paging affordance, not a
    // warning. `partialBanner` returns null for it, so the page is `ready` and
    // renders no banner. Asserting this is what keeps the pair of tests above
    // from passing on a page that banners on every truncated response.
    const resp: ActiveQueriesResponse = {
      ...FIXTURE_ACTIVE_QUERIES,
      meta: { ...FIXTURE_ACTIVE_QUERIES.meta, truncated: true },
    };
    const { container } = renderPage({ fixtureOverride: resp });
    expect(container.querySelector(".banner")).toBeNull();
    expect(container.querySelector('[data-testid="active-table"]')).not.toBeNull();
  });
});

/* ── 6. R10.18's windowing and density ─────────────────────────────────────── */

describe("ActiveQueriesPage — R10.18's windowing and density", () => {
  /** A payload of `n` rows built by re-labelling the fixture's first row, for
   *  the windowing cases the three-row fixture cannot show. Re-labelled rather
   *  than written out, for the reason `rowWith` exists: a row built by hand
   *  passes while the real type gains a field. */
  function rowsFor(n: number): SessionView[] {
    return Array.from({ length: n }, (_, i) => rowWith(ROW_41288, { pid: 50000 + i }));
  }

  it("offers the toggle and marks the table with the density it holds", () => {
    const { container } = renderPage();
    const toggle = container.querySelector('.segmented[aria-label="Row density"]');
    expect(toggle).not.toBeNull();
    expect(toggle?.querySelectorAll("button[aria-pressed]")).toHaveLength(2);
    // The two halves of R10.18 are one value here: the primitive owns row height
    // and the table renders the density the control reports, so a toggle that
    // paints compact over a comfortable table cannot pass this test.
    expect(toggle?.querySelector('button[aria-pressed="true"]')?.textContent).toBe("Compact");
    expect(container.querySelector('[data-testid="active-table"]')?.getAttribute("data-density")).toBe(
      "compact",
    );
  });

  it("moves the table's density when the toggle changes", () => {
    const { container } = renderPage();
    const comfortable = screen.getByRole("button", { name: "Comfortable" });
    fireEvent.click(comfortable);
    expect(comfortable.getAttribute("aria-pressed")).toBe("true");
    expect(container.querySelector('[data-testid="active-table"]')?.getAttribute("data-density")).toBe(
      "comfortable",
    );
  });

  it("windows an R8.10-sized payload and keeps the scrollbar the right length", () => {
    // 500 rows is R8.10's max page. At compact (24px) in the 640px viewport the
    // window is 27 visible rows plus 6 of overscan, so 39 mount and the
    // padBottom spacer carries the other 461 * 24 = 11064px. The spacer is the
    // half of R10.18 a plain `<table>` loses: without it the scrollbar measures
    // the mounted rows, and the page reads as 39 sessions.
    const rows = rowsFor(500);
    const { container } = renderPage({ fixtureOverride: respWith(rows) });
    const dataRows = container.querySelectorAll("tbody tr[data-pid]");
    expect(dataRows).toHaveLength(39);
    expect(dataRows.length).toBeLessThan(rows.length);
    // padTop is 0 at the top of the list, so only the bottom spacer renders, and
    // it spans every column — 9 visible plus the expand column, the same count
    // the header row renders.
    const spacers = container.querySelectorAll("tbody tr.virtual-spacer");
    expect(spacers).toHaveLength(1);
    const cell = spacers[0]?.querySelector("td");
    expect(cell?.getAttribute("colspan")).toBe("10");
    expect(cell?.getAttribute("style")).toContain("height: 11064px");
  });

  it("re-windows when the scroll container moves", () => {
    // The window's other input is `scrollTop`, and it is state for the reason
    // the page's header gives: a scroll that never reaches React leaves the
    // slice where it was. 240px is 10 rows, minus the 6-row overscan the window
    // starts at row 4 — which is what the first mounted row has to be.
    const rows = rowsFor(500);
    const { container } = renderPage({ fixtureOverride: respWith(rows) });
    const scroller = container.querySelector('[data-testid="active-table-scroll"]') as HTMLElement;
    expect(scroller).not.toBeNull();
    act(() => {
      fireEvent.scroll(scroller, { target: { scrollTop: 240 } });
    });
    expect(container.querySelector("tbody tr[data-pid]")?.getAttribute("data-pid")).toBe("50004");
    expect(container.querySelector("tbody tr.virtual-spacer td")?.getAttribute("style")).toContain(
      "height: 96px",
    );
  });
});

describe("R4.1's Status cell", () => {
  // R4.1 (`requirements.md:112`) names the hourglass and the block; the page
  // draws them through `SessionStatusChip` (`../components/StatusGlyph`), so
  // this block owns the wiring — that the status cell renders a chip and picks
  // its state from the row's own two fields — and not the geometry, which the
  // component suite pins by golden.
  //
  // The shipped fixture covers all three states on its own: 41288 is `active`
  // with `blockingPids: [41102]` (blocked), 41102 is `idle in transaction`
  // (idle), 41304 is `active` with no blockers (active). The expectation is
  // typed here rather than imported from `sessionStateFor`, so a change to the
  // mapping fails this test too instead of moving with it.
  const STATES_BY_PID: Record<string, string> = {
    "41288": "blocked",
    "41102": "idle",
    "41304": "active",
  };

  it("renders a status chip in every row, with the state that row's fields imply", () => {
    const { container } = renderPage();
    const rows = [...container.querySelectorAll("tbody tr[data-pid]")];
    expect(rows).toHaveLength(FIXTURE_ACTIVE_QUERIES.data.length);
    for (const tr of rows) {
      const pid = tr.getAttribute("data-pid") ?? "";
      const chip = tr.querySelector(".status-chip");
      expect(chip, `row ${pid}`).not.toBeNull();
      const row = FIXTURE_ACTIVE_QUERIES.data.find((s) => String(s.pid) === pid);
      // The cell's text is the API's string, unchanged.
      expect(chip?.textContent, `row ${pid}`).toBe(row?.status);
      expect(chip?.getAttribute("data-session-state"), `row ${pid}`).toBe(STATES_BY_PID[pid]);
      expect(chip?.querySelector("svg.status-glyph path"), `row ${pid}`).not.toBeNull();
    }
  });
});
