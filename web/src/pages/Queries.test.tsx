/* R10.6's page, tested as the three things the requirement makes testable.
 *
 * The table itself is `QueryTable.test.tsx`'s problem (the R3.2 column set, the
 * windowing, the load denominators). What this file owns is the page's own
 * surface:
 *
 * 1. **The filter rules** — `filterQueries` and `databaseOptions` are pure, so
 *    they are tested as functions. R10.6's "search over normalized text" is a
 *    rule about *which string* gets searched (the R3.5 template, not the
 *    fingerprint) and a rule about matching (a search box that requires an exact
 *    case match is one people stop using); the database chip's rule is that it
 *    is *exact*, not a substring, because `openwebui` and `openwebui_replica`
 *    are two databases and a substring filter conflates them.
 * 2. **The URL contract** — `resolveSort` and `nextDir` are where the page's
 *    sort meets `urlState.SortKey`, and the interesting case is the one that
 *    looks like a bug: a fleet sort key arriving here resolves to the queries
 *    default rather than to an unsorted table. That is the visible half of the
 *    `SortKey` widening ask, so it is asserted rather than commented.
 * 3. **The copy table** — the page must render `QUERY_COPY`, not the fleet's.
 *    "No clusters yet" above an empty *query* list is the failure mode the
 *    `copy` prop exists to prevent, and the only test for it is a render.
 *
 * The page is rendered directly rather than through `<App>`: `routes.ts` still
 * has `component: null` for the queries route (A4's flip is the ask), and
 * `App`'s own `fixtureOverride` is typed `FleetResponse`, which is exactly the
 * union problem A4's generic `PageProps<T>` solved. Rendering the page with its
 * own props is what `AnyPageComponent`'s doc says the page's test should do.
 */

import { fireEvent, render, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { ClientConfig } from "../api/client";
import { emptyEnvelope, toQueriesResponse } from "../api/envelope";
import { FIXTURE_GENERATED_AT, FIXTURE_QUERIES } from "../api/fixtures";
import type { QueryResponse, QueryView } from "../api/types";
import { DEFAULT_STATE, parseUrl, type UrlState } from "../state/urlState";
import { formatTimestamp } from "../lib/format";
import {
  QueriesPage,
  databaseOptions,
  filterQueries,
  nextDir,
  resolveSort,
  type QueriesPageProps,
} from "./Queries";
// `QuerySortKey` comes from the table, not the page: the page imports it from
// there too, and re-exporting it from `Queries.tsx` would put a second home on a
// union whose first home is what `QueryTable.test.tsx` asserts key-by-key.
import type { QuerySortKey } from "../components/QueryTable";
import { ROW_HEIGHT } from "../components/VirtualList";

const rows = FIXTURE_QUERIES.data;
const first = rows[0] as QueryView;

const CFG: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };

/** The URL the page would be mounted from: cluster-scoped, nothing else set. */
/** The URL the page would be mounted from: cluster-scoped, nothing else set.
 *
 *  `open-webui/open-webui-pg` is the fixture's own cluster (the namespace has a
 *  hyphen, the database does not — `openwebui`), so the breadcrumb the page
 *  builds from `state.cluster` and the one it falls back to from the rows agree. */
const URL_STATE: UrlState = { ...DEFAULT_STATE, page: "queries", cluster: "open-webui/open-webui-pg" };

function renderPage(overrides: Partial<QueriesPageProps> = {}) {
  const onPatch = vi.fn();
  const props: QueriesPageProps = {
    state: URL_STATE,
    onPatch,
    cfg: CFG,
    fixtureOverride: toQueriesResponse(FIXTURE_QUERIES),
    ...overrides,
  };
  return { ...render(<QueriesPage {...props} />), onPatch };
}

describe("filterQueries — R10.6's search and database chip", () => {
  it("searches the normalized template, not the fingerprint", () => {
    // R3.5's template is the searchable string; the fingerprint is a hash. The
    // fixture has three `SELECT * FROM …` rows across three clusters, so a hit
    // set of three proves the match ran against `query` and not against a field
    // that happens to contain the same characters.
    const hits = filterQueries(rows, { q: "select * from", database: "" });
    expect(hits.map((r) => r.query)).toEqual([
      "SELECT * FROM users WHERE id = $1",
      "SELECT * FROM pg_catalog.pg_stat_activity x",
      "SELECT * FROM sessions WHERE subject = $1 AND state = $2",
    ]);
  });

  it("matches case-insensitively", () => {
    // `SELECT` and `select` find the same rows. A case-sensitive search box is
    // one where the user learns not to type capital letters.
    expect(filterQueries(rows, { q: "SELECT", database: "" })).toHaveLength(
      filterQueries(rows, { q: "select", database: "" }).length,
    );
  });

  it("trims the query, so trailing whitespace while typing is not a filter", () => {
    expect(filterQueries(rows, { q: "users ", database: "" })).toEqual(
      filterQueries(rows, { q: "users", database: "" }),
    );
  });

  it("an empty search keeps every row", () => {
    expect(filterQueries(rows, { q: "", database: "" })).toHaveLength(rows.length);
  });

  it("the database filter is exact, not a substring", () => {
    expect(filterQueries(rows, { q: "", database: "openwebui" })).toHaveLength(3);
    // `gate` is a prefix of `gateway` and matches nothing. A substring match
    // here would make the chip's count and the table's rows disagree.
    expect(filterQueries(rows, { q: "", database: "gate" })).toHaveLength(0);
  });

  it("the two filters compose", () => {
    // Two of openwebui's three rows mention `users` (the plain one and the R3.6
    // truncated one, whose template continues `… FROM users u LEFT JOIN
    // user_groups g O`), and gateway's two do not. So the result is the
    // intersection — the AND, not the OR — and the count is the fixture's, not a
    // number this file gets to choose.
    const hits = filterQueries(rows, { q: "users", database: "openwebui" });
    expect(hits).toHaveLength(2);
    expect(hits.every((r) => r.database === "openwebui")).toBe(true);
    expect(filterQueries(rows, { q: "users", database: "gateway" })).toHaveLength(0);
  });
});

describe("databaseOptions", () => {
  it("counts per database, most-used first, ties alphabetical", () => {
    // Ordering by count then name is what makes the list stable across refetches;
    // a Map's insertion order changes as soon as the server paginates.
    expect(databaseOptions(rows)).toEqual([
      { value: "openwebui", count: 3 },
      { value: "gateway", count: 2 },
      { value: "cookbook", count: 1 },
      { value: "freeidp", count: 1 },
    ]);
  });

  it("is empty for an empty payload", () => {
    expect(databaseOptions([])).toEqual([]);
  });
});

describe("resolveSort — where the page's sort meets urlState.SortKey", () => {
  it("accepts every query sort key", () => {
    for (const key of ["query", "database", "loadByTotalTime", "timesCalled", "loadByIOWaitMs"]) {
      expect(resolveSort(key)).toBe(key);
    }
  });

  it("falls back to the queries default for a fleet sort key", () => {
    // The visible half of the SortKey ask: `parseUrl("?sort=timesCalled")` runs
    // `pick(..., SORT_KEYS, "health")` and gets "health", because SortKey is
    // the fleet's ten keys. The page must not render an unsorted table over that
    // — it resolves to the column R10.6's default view is sorted by.
    expect(resolveSort("health")).toBe("loadByTotalTime");
    expect(resolveSort("tps")).toBe("loadByTotalTime");
  });

  it("falls back for an empty or unknown value", () => {
    expect(resolveSort("")).toBe("loadByTotalTime");
    expect(resolveSort("not-a-column")).toBe("loadByTotalTime");
  });

  it("agrees with parseUrl about a real queries URL", () => {
    // End-to-end on the URL half: a hand-typed queries URL parses, and the page
    // renders a defined sort rather than throwing on an unrecognised key.
    const state = parseUrl("/c/ai-api-gateway/rev-proxy-pg/queries?sort=timesCalled&dir=asc");
    expect(state.page).toBe("queries");
    expect(state.cluster).toBe("ai-api-gateway/rev-proxy-pg");
    expect(state.sort).toBe("health"); // SortKey is the fleet's — the ask
    expect(resolveSort(state.sort)).toBe("loadByTotalTime");
    expect(state.dir).toBe("asc");
  });
});

describe("nextDir", () => {
  const base: QuerySortKey = "loadByTotalTime";

  it("flips the direction when the column is already the active sort", () => {
    expect(nextDir(base, "desc", base)).toBe("asc");
    expect(nextDir(base, "asc", base)).toBe("desc");
  });

  it("starts a new text column at ascending and a new measurement at descending", () => {
    // Names A→Z, every measurement high→low: the interesting end first, which is
    // the same rule Fleet.tsx's nextDir implements for name/namespace.
    expect(nextDir(base, "desc", "query")).toBe("asc");
    expect(nextDir(base, "desc", "database")).toBe("asc");
    expect(nextDir(base, "asc", "timesCalled")).toBe("desc");
    expect(nextDir(base, "asc", "avgExecTimeMs")).toBe("desc");
  });
});

describe("QueriesPage", () => {
  it("renders the table with the fixture's rows", () => {
    const { container } = renderPage();
    expect(container.querySelector('[data-page="queries"]')).not.toBeNull();
    expect(container.querySelector("table.data")).not.toBeNull();
    expect(container.querySelectorAll("table.data tbody tr:not(.virtual-spacer)")).toHaveLength(
      rows.length,
    );
  });

  it("renders the header from the payload's own meta (R10.21)", () => {
    const { container } = renderPage();
    expect(within(container).getByRole("heading", { level: 1, name: "Queries" })).toBeInTheDocument();
    const stamp = container.querySelector("p.page-meta") as HTMLElement;
    expect(stamp.getAttribute("title")).toBe(`${FIXTURE_GENERATED_AT} UTC`);
    expect(stamp.textContent).toBe(`Data as of ${formatTimestamp(FIXTURE_GENERATED_AT)}`);
  });

  it("breadcrumbs Fleet / cluster / Queries", () => {
    const { container } = renderPage();
    const crumb = container.querySelector("p.breadcrumb") as HTMLElement;
    const links = within(crumb).getAllByRole("link");
    expect(links[0]?.textContent).toBe("Fleet");
    expect(links[0]?.getAttribute("href")).toBe("/");
    expect(links[1]?.textContent).toBe("open-webui/open-webui-pg");
    expect(links[1]?.getAttribute("href")).toBe("/c/open-webui/open-webui-pg");
    expect(crumb.textContent).toContain("Queries");
  });

  it("falls back to the rows' own cluster when the URL has none", () => {
    // A direct render has no cluster; the rows carry namespace/cluster, and a
    // page showing openwebui's rows under an empty breadcrumb is lying.
    const { container } = renderPage({ state: { ...DEFAULT_STATE, page: "fleet" } });
    const crumb = container.querySelector("p.breadcrumb") as HTMLElement;
    expect(crumb.textContent).toContain(`${first.namespace}/${first.cluster}`);
  });

  it("shows the sort the URL resolves to, in the table's own caption", () => {
    const { container } = renderPage({ state: { ...URL_STATE, dir: "asc" } });
    const caption = container.querySelector("table.data caption");
    expect(caption?.textContent).toBe("Top queries, sorted by loadByTotalTime ascending");
  });

  it("a header click patches the sort and the direction that follows from it", () => {
    const { container, onPatch } = renderPage();
    const head = within(container.querySelector("thead")!);
    fireEvent.click(head.getByRole("button", { name: "Times called" }));
    expect(onPatch).toHaveBeenCalledWith({ sort: "timesCalled", dir: "desc" });
  });

  it("a row click opens the detail route for that fingerprint", () => {
    const { container, onPatch } = renderPage();
    const link = container.querySelector("a.query-link") as HTMLElement;
    fireEvent.click(link);
    expect(onPatch).toHaveBeenCalledWith({ page: "query-detail", row: first.fingerprint });
  });

  it("the density toggle reaches the table", () => {
    const { container } = renderPage();
    expect(container.querySelector("table.data")?.getAttribute("data-density")).toBe("compact");
    fireEvent.click(within(container).getByRole("button", { name: "Comfortable" }));
    expect(container.querySelector("table.data")?.getAttribute("data-density")).toBe("comfortable");
    expect(container.querySelector("tbody tr:not(.virtual-spacer)")?.getAttribute("style")).toContain(
      `height: ${ROW_HEIGHT.comfortable}px`,
    );
  });

  it("offers the column menu", () => {
    const { container } = renderPage();
    expect(within(container).getByRole("button", { name: /Columns/ })).toBeInTheDocument();
  });

  it("renders the range control the URL contract needs", () => {
    const { container } = renderPage();
    const select = within(container).getByRole("combobox", { name: "Range" }) as HTMLSelectElement;
    // The list is spelled out rather than imported from `TIME_RANGES`: a test
    // that renders the same array the component maps is checking the array
    // against itself. This is the spec's list (R10.15, as A3 ruled — `24h` was
    // minted here and `series.go` rejects it, so it is `1d`, and `6h` was the
    // preset the picker never offered), and the component's is the one under test.
    expect(select.value).toBe("1d");
    expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "1h",
      "6h",
      "1d",
      "7d",
      "30d",
    ]);
  });

  it("falls back to the fixture when no payload is passed", () => {
    // The `fixtureOverride ?? toQueriesResponse(FIXTURE_QUERIES)` line, exercised
    // by omitting the prop — which is the shape `routes.ts` mounts it with.
    const { container } = render(
      <QueriesPage state={URL_STATE} onPatch={vi.fn()} cfg={CFG} />,
    );
    expect(container.querySelectorAll("table.data tbody tr:not(.virtual-spacer)")).toHaveLength(
      rows.length,
    );
  });
});

describe("the page's states (R10.12, in the queries' nouns)", () => {
  const EMPTY: QueryResponse = toQueriesResponse(emptyEnvelope<QueryView>(FIXTURE_GENERATED_AT));

  it("an empty payload says \"No queries in this window\", not the fleet's wording", () => {
    // The whole reason `copyFor` takes a table and `StatePanel` takes a `copy`
    // prop: the strings in FLEET_COPY are about clusters, and a page that
    // renders them for a query payload says the wrong noun.
    const { container } = renderPage({ fixtureOverride: EMPTY });
    const panel = container.querySelector(".state-panel") as HTMLElement;
    expect(panel.getAttribute("data-state")).toBe("empty");
    expect(within(panel).getByRole("heading", { name: "No queries in this window" })).toBeInTheDocument();
    expect(container.textContent).not.toContain("No clusters yet");
    expect(container.querySelector("table.data")).toBeNull();
  });

  it("renders R10.27's not-installed screen when the extension is absent", () => {
    // §9's 6b acceptance names this screen, and R10.27 requires it to be "a
    // first-class screen … not an empty table with a tooltip". The page renders
    // it as a full panel — the one state that is full-panel here and a banner on
    // Fleet — because a banner over a zero-row table is the tooltip-shaped
    // answer that sentence rules out.
    // The page renders `ExtensionNotInstalled` here rather than the generic
    // `StatePanel` it uses for the other three full-panel states, and the test
    // says so by asserting the screen's own class and the cluster it names — the
    // heading and button alone would pass with the old three-string answer.
    const { container } = renderPage({ fixtureOverride: EMPTY, extensionInstalled: false });
    const panel = container.querySelector(".state-panel") as HTMLElement;
    expect(panel.classList.contains("ext-screen")).toBe(true);
    expect(panel.getAttribute("data-state")).toBe("unavailable");
    expect(
      within(panel).getByRole("heading", { name: "pg_stat_statements is not installed" }),
    ).toBeInTheDocument();
    expect(within(panel).getByRole("button", { name: "Enable pg_stat_statements" })).toBeInTheDocument();
    // The cluster comes from the URL, which is the only identity the page has
    // before a fetch: `state.cluster`, the same string the breadcrumb is built
    // from a few lines above in the render.
    expect(within(panel).getByText("open-webui/open-webui-pg", { exact: true })).toBeInTheDocument();
  });

  it("an unknown extension presence is the plain empty, not the R10.27 screen", () => {
    // `undefined` is not `false`. Every response today is this case, so getting
    // it wrong would render the not-installed screen for every cluster.
    const { container } = renderPage({ fixtureOverride: EMPTY, extensionInstalled: undefined });
    expect(container.querySelector(".state-panel")?.getAttribute("data-state")).toBe("empty");
  });

  it("a partial payload renders as a banner over the rows it has", () => {
    const partial: QueryResponse = {
      meta: {
        total: 7,
        truncated: false,
        generatedAt: FIXTURE_GENERATED_AT,
        loading: ["cookbook"],
      },
      queries: [first],
    };
    const { container } = renderPage({ fixtureOverride: partial });
    const banner = container.querySelector(".banner") as HTMLElement;
    expect(banner.getAttribute("role")).toBe("status");
    expect(within(banner).getByText("Partial results")).toBeInTheDocument();
    // The body is composed from the payload (R10.22), not from a `partial` key —
    // and asserting the whole string is deliberate: it is the test that goes red
    // when A4 parameterises `partialBanner`'s noun. Today it says
    // "1 cluster served" above a *queries* payload, which is the ask; the
    // requirement (R10.22's served-count) is satisfied either way, so the page
    // passes the source through rather than falling back to the static copy.
    expect(within(banner).getByText("1 cluster served. 1 namespace still in the first discovery pass (cookbook).")).toBeInTheDocument();
    expect(container.querySelector("table.data")).not.toBeNull();
  });

  it("a populated payload renders no banner and no panel", () => {
    const { container } = renderPage();
    expect(container.querySelector(".banner")).toBeNull();
    expect(container.querySelector(".state-panel")).toBeNull();
  });
});
