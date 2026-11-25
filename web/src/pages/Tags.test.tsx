/* R10.3's ninth page, and the two things a tag table can get wrong.
 *
 * The suite follows the page's own structure rather than a generic page-test
 * template, because the page's structure is what R3.12 dictates:
 *
 * 1. **the table** — the shell, the row set, and the column set the menu hands
 *    it. The column assertion is the load-bearing one: `TagView` has eighteen
 *    json tags, the default set shows eight, and `useColumnVisibility`
 *    *silently drops* a `DEFAULTS` key that no `ColumnDef` declares — so a typo
 *    in the default list is a column that quietly stops rendering rather than a
 *    failing test, unless the test asserts the two lists agree.
 * 2. **which dimensions a row has** — R3.12's two families, and the fixture's
 *    three rows are its three cases (a full comment, no comment, a partial one).
 *    These are the tests that keep an absent dimension rendering as `—` instead
 *    of as a missing cell, which is the one failure mode a tag table has that a
 *    query table does not.
 * 3. **R10.12's named states** — via `StatePanel`/`TAGS_COPY`, including the
 *    R10.27 branch and the `truncated`-is-not-a-warning case R8.10 defines.
 *
 * `tagsParamsFrom` is tested here because it lives here: the other pages'
 * params functions are A4's in `state/` with their own suites
 * (`state/useActiveQueries.test.ts`), and this one has no state-layer file to
 * belong to until `useTags` exists.
 *
 * Rendered directly, not through `<App>` even though the shell mounts this page
 * now (`routes.ts:263`'s `component: TagsPage`, A4's `843986b`): the mount is
 * `routes.test.tsx`'s to assert, and `PageProps<T>` exists precisely so a page
 * test can pass the props the shell will without routing to it.
 */

import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { isValidElement, type ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ClientConfig, TagsParams } from "../api/client";
import { FIXTURE_TAGS } from "../api/fixtures";
import type { Envelope, TagView } from "../api/types";
import { DEFAULTS } from "../components/ColumnMenu";
import { buildPath } from "../routes";
import { DEFAULT_STATE, TIME_RANGES, type TimeRange, type UrlState } from "../state/urlState";
import { formatDurationMs, formatNumber, formatPercent, formatTimestamp, NULL_VALUE } from "../lib/format";
import {
  TAG_COLUMNS,
  TagsPage,
  cell,
  tagsKey,
  tagsParamsFrom,
  useTagsFetch,
  type TagsPageProps,
} from "./Tags";

const CFG: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };
const CLUSTER_ID = "open-webui/open-webui-pg";

/** The unstubbed fetch, so the http-arm suite below can put it back. */
const realFetch = globalThis.fetch;

/** The URL this page is mounted from: `routes.ts:257-267`'s
 *  `/c/:ns/:name/tags`, which is `scope: "cluster"` with no other params. */
const URL_STATE: UrlState = { ...DEFAULT_STATE, page: "tags", cluster: CLUSTER_ID };

/** The fixture's three rows, named by the case each one is. Spreading a fixture
 *  row rather than writing one out keeps these cases honest about the *other*
 *  fields; the `!` is the fixture's own contract (three rows, asserted by the
 *  table test below), not a way to silence a real `undefined`. */
const TAG_FULL_COMMENT = FIXTURE_TAGS.data[0]!; // openwebui — every (b) dimension
const TAG_NO_COMMENT = FIXTURE_TAGS.data[1]!; // gateway — the (a) dimensions only
const TAG_PARTIAL = FIXTURE_TAGS.data[2]!; // cookbook — action + route, no controller

function renderPage(overrides: Partial<TagsPageProps> = {}) {
  const onPatch = vi.fn();
  const props: TagsPageProps = {
    state: URL_STATE,
    onPatch,
    cfg: CFG,
    fixtureOverride: FIXTURE_TAGS,
    ...overrides,
  };
  return { ...render(<TagsPage {...props} />), onPatch };
}

/** The fixture with its payload replaced, for the states the shipped fixture
 *  cannot show (an empty tag set, a partial one). `meta` carries over so a test
 *  about one cell does not also change the header stamp. */
function envelopeWith(data: TagView[]): Envelope<TagView> {
  return { ...FIXTURE_TAGS, data };
}

/* ── 1. the table ──────────────────────────────────────────────────────────── */

describe("TagsPage — the table (R10.3's ninth page)", () => {
  it("renders the shell for a cluster URL", () => {
    const { container } = renderPage();
    expect(container.querySelector('[data-page="tags"]')).not.toBeNull();
    // The exact `routes.ts:259` title, which `routes.test.tsx:53-68`'s
    // `R10_3_TITLES` asserts for the nav and this asserts for the page.
    expect(screen.getByRole("heading", { level: 1, name: "Tags" })).toBeInTheDocument();
    expect(screen.getByTestId("tags-table")).toBeInTheDocument();
  });

  it("renders one row per tag in the payload, keyed by tagHash", () => {
    const { container } = renderPage();
    // `data-tag-hash` is the row's identity — R8.2's path key — so it is also
    // what the test reads, exactly as the Active Queries rows carry `data-pid`.
    const hashes = [...container.querySelectorAll("tbody tr[data-tag-hash]")].map((tr) =>
      tr.getAttribute("data-tag-hash"),
    );
    expect(hashes).toEqual(FIXTURE_TAGS.data.map((r) => r.tagHash));
    expect(hashes).toHaveLength(3);
  });

  it("renders the eight documented default columns, not all eighteen", () => {
    const { container } = renderPage();
    const heads = [...container.querySelectorAll("thead th")].map((th) => th.textContent?.trim());
    expect(heads).toEqual([
      "Tag hash",
      "Application",
      "Database",
      "User",
      "Action",
      "Load by total time",
      "Avg exec time",
      "Times called",
    ]);
    // The hidden-by-default ones are still reachable through the menu, which is
    // R10.20's contract; the count on the button is the visible proof.
    expect(screen.getByText("8/18")).toBeInTheDocument();
  });

  it("keeps every DEFAULTS key backed by a ColumnDef", () => {
    // `normalizeSelection` filters unknown keys out rather than erroring, so a
    // `DEFAULTS` entry that no `TAG_COLUMNS` entry declares renders as a
    // missing column and nothing else. The type can't catch it — DEFAULTS is
    // `Record<string, readonly string[]>`, not a keyed map over the columns.
    const declared = new Set(TAG_COLUMNS.map((c) => c.key));
    const defaults = DEFAULTS["tags"] ?? [];
    expect(defaults.length).toBeGreaterThan(0);
    for (const key of defaults) {
      expect(
        declared.has(key),
        `DEFAULTS["tags"] names "${key}", which TAG_COLUMNS does not declare`,
      ).toBe(true);
    }
    // And the column list is the type's field list, which is the one count
    // `client.ts:467-476`'s docblock states as a number ("all eighteen of
    // TagView's json tags") and so the one a rename can silently break.
    expect(TAG_COLUMNS).toHaveLength(18);
    expect(new Set(TAG_COLUMNS.map((c) => c.key)).size).toBe(18);
  });

  it("renders the hidden dimensions when the menu turns them on", () => {
    // The four remaining (b) dimensions are off by default, so the only way to
    // prove the page can render them is to ask the menu for them and read the
    // cells — which is also how the R3.12 (b) family gets exercised end-to-end.
    const { container } = renderPage();
    fireEvent.click(screen.getByRole("button", { name: /Columns/ }));
    fireEvent.click(screen.getByLabelText("Route"));
    fireEvent.click(screen.getByLabelText("Framework"));

    const heads = [...container.querySelectorAll("thead th")].map((th) => th.textContent?.trim());
    expect(heads).toEqual(expect.arrayContaining(["Route", "Framework"]));

    // The openwebui row carries both; the gateway row carries neither, and the
    // two answers stay distinguishable once the columns are on.
    const rows = [...container.querySelectorAll("tbody tr[data-tag-hash]")];
    const openwebui = rows.find((r) => r.getAttribute("data-tag-hash") === TAG_FULL_COMMENT.tagHash);
    const gateway = rows.find((r) => r.getAttribute("data-tag-hash") === TAG_NO_COMMENT.tagHash);
    expect(openwebui?.textContent).toContain("/api/v1/users/{user_id}");
    expect(openwebui?.textContent).toContain("fastapi");
    expect(gateway?.textContent).toContain(NULL_VALUE);
  });
});

/* ── 2. R3.12's two dimension families, one per fixture row ───────────────── */

describe("TagsPage — R3.12's dimensions (the fixture's three cases)", () => {
  /** The (b) dimensions, in the order the type declares them. */
  const COMMENT_DIMS = ["action", "route", "controller", "framework", "dbDriver"] as const;

  it("a full comment renders all five (b) dimensions", () => {
    // The openwebui row is the fixture's complete answer: every key the
    // `pgsi:` grammar defines is present, so no cell in this group is a marker.
    expect(cell(TAG_FULL_COMMENT, "action")).toBe("session.load");
    expect(cell(TAG_FULL_COMMENT, "route")).toBe("/api/v1/users/{user_id}");
    expect(cell(TAG_FULL_COMMENT, "controller")).toBe("UsersController");
    expect(cell(TAG_FULL_COMMENT, "framework")).toBe("fastapi");
    expect(cell(TAG_FULL_COMMENT, "dbDriver")).toBe("pgx");
  });

  it("no comment at all renders the (b) cells as the marker, not as absent cells", () => {
    // The gateway row has only R3.12's (a) dimensions. "The statement carried no
    // comment" and "this page has no such column" are different answers, and
    // omitting the cell is the one thing that conflates them — so the cell is
    // there and it says `—`.
    for (const key of COMMENT_DIMS) {
      expect(cell(TAG_NO_COMMENT, key)).toBe(NULL_VALUE);
    }
    // …while the (a) family is fully populated, which is what makes the row a
    // *tag* rather than a row with nothing in it.
    expect(cell(TAG_NO_COMMENT, "application")).toBe("gateway");
    expect(cell(TAG_NO_COMMENT, "database")).toBe("gateway");
    expect(cell(TAG_NO_COMMENT, "user")).toBe("gateway");
  });

  it("a partial comment renders the two it has and the three it has not", () => {
    // The cookbook row is the case a table that rendered only the dimensions a
    // row *has* would hide: `action` and `route` present, the other three
    // absent. Both halves are asserted from the same row.
    expect(cell(TAG_PARTIAL, "action")).toBe("recipe.list");
    expect(cell(TAG_PARTIAL, "route")).toBe("/api/recipes");
    expect(cell(TAG_PARTIAL, "controller")).toBe(NULL_VALUE);
    expect(cell(TAG_PARTIAL, "framework")).toBe(NULL_VALUE);
    expect(cell(TAG_PARTIAL, "dbDriver")).toBe(NULL_VALUE);
  });

  it("renders the tag hash in full", () => {
    // 64 lowercase hex (`types.ts:641-644`), and R8.2's path key. This cell is
    // the one the page returns as an element rather than a string —
    // `<span className="num trunc" title={…}>` (`Tags.tsx:375-382`) — so the
    // assertion reads the element's props instead of a formatted string, which
    // is also the only way to prove the `title` and the visible text are the same
    // value and that no page-invented shortening sits between them.
    const hashCell = cell(TAG_FULL_COMMENT, "tagHash");
    expect(isValidElement(hashCell)).toBe(true);
    const props = (hashCell as ReactElement<{ className: string; title: string; children: string }>).props;
    expect(props.className).toBe("num trunc");
    expect(props.title).toBe(TAG_FULL_COMMENT.tagHash);
    expect(props.children).toBe(TAG_FULL_COMMENT.tagHash);
    expect(TAG_FULL_COMMENT.tagHash).toHaveLength(64);
  });
});

/* ── the load columns' formatting (the page's own rules, exported for this) ── */

describe("TagsPage — R3.7's rollup cells", () => {
  it("formats every load column the way the query table does", () => {
    // The gateway row, so the numbers are the fixture's and not invented here.
    // The two percentage columns are percentages (`LoadColumns`, types.ts:430-433);
    // the four ms columns are durations; `avgRowsReturned` is a mean over an
    // R3.3 delta and so keeps one decimal — the fixture's `14` renders "14.0",
    // which is `formatNumber(value, 1)`'s rule and `QueryTable.textCell`'s.
    // Each cell is asserted against the formatter applied to the same field, so
    // the test pins the *routing* (which formatter a column uses), not the
    // formatter's own output — that has its own suite in `lib/format.test.ts`.
    const row = TAG_NO_COMMENT;
    expect(cell(row, "loadByTotalTime")).toBe(formatPercent(row.loadByTotalTime));
    expect(cell(row, "pctLoadByQueries")).toBe(formatPercent(row.pctLoadByQueries));
    expect(cell(row, "avgExecTimeMs")).toBe(formatDurationMs(row.avgExecTimeMs));
    expect(cell(row, "totalExecTimeMs")).toBe(formatDurationMs(row.totalExecTimeMs));
    expect(cell(row, "loadByCPUMs")).toBe(formatDurationMs(row.loadByCPUMs));
    expect(cell(row, "loadByIOWaitMs")).toBe(formatDurationMs(row.loadByIOWaitMs));
    expect(cell(row, "lockWaitMs")).toBe(formatDurationMs(row.lockWaitMs));
    expect(cell(row, "avgRowsReturned")).toBe(formatNumber(row.avgRowsReturned, 1));
    expect(cell(row, "timesCalled")).toBe(formatNumber(row.timesCalled));
  });

  it("renders an unrecognised column key as the marker", () => {
    // `useColumnVisibility` can only hand back declared keys, so this branch is
    // the one a caller outside the table can reach — and the answer it gives is
    // the marker, not an empty string that looks like a blank cell.
    expect(cell(TAG_NO_COMMENT, "notAColumn")).toBe(NULL_VALUE);
  });
});

/* ── the header (R10.21's stamp, R10.15's range) ───────────────────────────── */

describe("TagsPage — the header", () => {
  it("stamps the payload's own generatedAt, local-zone with UTC on hover (R10.21)", () => {
    const { container } = renderPage();
    const meta = container.querySelector(".page-meta");
    const generatedAt = FIXTURE_TAGS.meta.generatedAt;
    expect(meta?.textContent).toContain(`Data as of ${formatTimestamp(generatedAt)}`);
    expect(meta?.getAttribute("title")).toBe(`${generatedAt} UTC`);
  });

  it("offers the five R10.15 presets and patches the URL on change", () => {
    // The route takes `from`/`to` (`client.ts:467-476`), so the window is part
    // of what this page asks for; changing it patches the URL, which re-derives
    // the params, which refetches — the chain `Queries.tsx`'s header drives.
    const { onPatch } = renderPage();
    // `getBy*` throws when the element is absent, so the query is the assertion
    // and needs no follow-up `not.toBeNull()`.
    const select = screen.getByLabelText<HTMLSelectElement>("Range");
    expect([...select.querySelectorAll("option")].map((o) => o.value)).toEqual([...TIME_RANGES]);
    expect(select.value).toBe(URL_STATE.range);

    const next: TimeRange = "7d";
    fireEvent.change(select, { target: { value: next } });
    expect(onPatch).toHaveBeenCalledWith({ range: next });
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

/* ── 3. the states R10.12 names ────────────────────────────────────────────── */

describe("TagsPage — R10.12's named states", () => {
  it("renders the named empty state for a cluster with no tags", () => {
    const { container } = renderPage({ fixtureOverride: envelopeWith([]) });
    const panel = container.querySelector('.state-panel[data-state="empty"]');
    expect(panel).not.toBeNull();
    expect(panel?.textContent).toContain("No tags in this window");
    // The table is not rendered alongside it — the state panel replaces it.
    expect(container.querySelector('[data-testid="tags-table"]')).toBeNull();
  });

  it("renders the contract fixture while the first fetch is in flight", () => {
    // `?delay=` exists (`client.ts:112-115`) so R16.7 can hold a page in its
    // pre-response frame. With no `fixtureOverride` and a slow client, the
    // payload on screen is `props.fixtureOverride ?? data.tags ?? FIXTURE_TAGS`
    // (`Tags.tsx:190`), so the frame is the contract fixture and
    // `deriveTagsPanelState`'s `tags === null → "loading"` arm is unreachable
    // from the page — the same shape `ActiveQueries.tsx:144` has, and why
    // `ActiveQueries.test.tsx` has no loading case either. Asserting the
    // fallback is the honest version of "the loading state": a page never blanks
    // while a fetch is in flight, and the arm that would blank it is the one the
    // `?? FIXTURE_TAGS` fallback keeps shut.
    const { container } = renderPage({
      fixtureOverride: undefined,
      cfg: { ...CFG, delayMs: 60_000 },
    });
    expect(container.querySelector('[data-testid="tags-table"]')).not.toBeNull();
    expect(container.querySelectorAll("tbody tr[data-tag-hash]")).toHaveLength(3);
  });

  it("renders the R10.27 screen when the extension is absent", () => {
    // The other reading of an empty payload. `extensionInstalled` is a prop
    // because the tags envelope has no presence field — see the page header —
    // and an explicit `false` is the only thing that reaches this branch.
    const { container } = renderPage({
      fixtureOverride: envelopeWith([]),
      extensionInstalled: false,
    });
    expect(container.querySelector('[data-testid="tags-table"]')).toBeNull();
    const panel = container.querySelector('.state-panel[data-state="unavailable"]');
    expect(panel).not.toBeNull();
    expect(panel?.textContent).toContain("pg_stat_statements is not installed");
    // The prop the page passes is `params.cluster` — the name half of the pair,
    // not the `namespace/name` string the URL carries (`Tags.tsx:214`) — so that
    // is what the screen names, and asserting the full pair would fail.
    expect(panel?.textContent).toContain("open-webui-pg");
    // R3.15's four steps, from the shared component rather than a copy.
    const steps = Array.from(panel?.querySelectorAll(".ext-step") ?? []).map((n) =>
      n.getAttribute("data-step"),
    );
    expect(steps).toEqual(["detect", "offer", "patch", "verify"]);
  });

  it("renders the plain empty, not the R10.27 screen, when presence is unknown", () => {
    // Every response today is *unknown*: the tags route carries no presence
    // field, so the default must not claim the extension is missing. This is the
    // other half of the pair the test above is — without it, a page that
    // rendered the not-installed screen for every empty payload would pass.
    const { container } = renderPage({
      fixtureOverride: envelopeWith([]),
      extensionInstalled: true,
    });
    expect(container.querySelector('.state-panel[data-state="unavailable"]')).toBeNull();
    expect(container.querySelector('.state-panel[data-state="empty"]')).not.toBeNull();
  });

  it("renders the partial banner in tag nouns (R10.22)", () => {
    // `copyFor` composes the sentence from the response rather than using
    // TAGS_COPY's fixed body, and the noun is the page's own — the sentence
    // counts tags, not the clusters the fleet's copy would count.
    const { container } = renderPage({
      fixtureOverride: {
        ...FIXTURE_TAGS,
        meta: {
          ...FIXTURE_TAGS.meta,
          truncated: true,
          nextCursor: "eyJvZmZzZXQiOjN9",
          loading: ["open-webui"],
        },
      },
    });
    const banner = container.querySelector(".banner");
    expect(banner).not.toBeNull();
    expect(banner?.textContent).toContain("Partial results");
    expect(banner?.textContent).toContain("3 tags served, more pages available");
    expect(banner?.textContent).toContain("1 namespace still in the first discovery pass (open-webui)");
    expect(container.querySelector('[data-testid="tags-table"]')).not.toBeNull();
  });

  it("does not read meta.truncated alone as a partial render", () => {
    // R8.10: `truncated` means there is another page — a paging affordance, not
    // a warning. Without this case, a page that banners on every truncated
    // response passes both banner tests above.
    const { container } = renderPage({
      fixtureOverride: { ...FIXTURE_TAGS, meta: { ...FIXTURE_TAGS.meta, truncated: true } },
    });
    expect(container.querySelector(".banner")).toBeNull();
    expect(container.querySelector('[data-testid="tags-table"]')).not.toBeNull();
  });
});

/* ── the URL half ──────────────────────────────────────────────────────────── */

describe("TagsPage — a URL that names no cluster", () => {
  it("renders the no-cluster frame instead of the page", () => {
    // `routes.ts:257-267` is `scope: "cluster"`; the route takes a
    // namespace/cluster pair, so there is no fleet-wide form to fall back to.
    const { container, onPatch } = renderPage({
      state: { ...DEFAULT_STATE, page: "tags" },
      fixtureOverride: undefined,
    });
    expect(container.querySelector('[data-page="tags"]')).toBeNull();
    expect(screen.getByText(/This URL does not name a cluster\./)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back to Fleet" }));
    expect(onPatch).toHaveBeenCalledWith({ page: "fleet", cluster: null });
  });

  it("names a malformed cluster instead of hiding it", () => {
    // A page that says "no cluster" about a URL that clearly names something
    // sends the reader to the wrong bug, so the frame quotes the string.
    const { container } = renderPage({
      state: { ...DEFAULT_STATE, page: "tags", cluster: "open-webui-pg" },
      fixtureOverride: undefined,
    });
    expect(container.querySelector('[data-page="tags"]')).toBeNull();
    expect(screen.getByText(/“open-webui-pg” is not a namespace\/name pair\./)).toBeInTheDocument();
  });
});

describe("tagsParamsFrom", () => {
  const NOW = new Date("2026-11-23T12:00:00Z");

  it("splits the cluster pair into namespace and cluster", () => {
    const params = tagsParamsFrom({ ...DEFAULT_STATE, page: "tags", cluster: CLUSTER_ID }, NOW);
    expect(params).not.toBeNull();
    expect(params?.namespace).toBe("open-webui");
    expect(params?.cluster).toBe("open-webui-pg");
  });

  it("turns each preset into the window it names", () => {
    // The five `TIME_RANGES` and the seconds each is worth, from one fixed
    // `now` — the clock is a parameter precisely so this is assertable.
    const expected: Record<TimeRange, string> = {
      "1h": "2026-11-23T11:00:00Z",
      "6h": "2026-11-23T06:00:00Z",
      "1d": "2026-11-22T12:00:00Z",
      "7d": "2026-11-16T12:00:00Z",
      "30d": "2026-10-24T12:00:00Z",
    };
    for (const range of TIME_RANGES) {
      const params = tagsParamsFrom({ ...DEFAULT_STATE, page: "tags", cluster: CLUSTER_ID, range }, NOW);
      expect(params?.from).toBe(expected[range]);
      expect(params?.to).toBe("2026-11-23T12:00:00Z");
    }
  });

  it("leaves sort unmapped", () => {
    // `UrlState.SortKey` names the *fleet table's* columns and none of them is
    // a tag column, so forwarding it would ask this route to sort by a field it
    // does not have. The page's own filter is the `tag` chip on Queries.
    const params = tagsParamsFrom(
      { ...DEFAULT_STATE, page: "tags", cluster: CLUSTER_ID, sort: "tps" },
      NOW,
    );
    expect(params?.sort).toBeUndefined();
  });

  it("rejects every cluster string that is not a namespace/name pair", () => {
    for (const cluster of ["noslash", "ns/", "/name", ""]) {
      expect(
        tagsParamsFrom({ ...DEFAULT_STATE, page: "tags", cluster }, NOW),
        `"${cluster}" is not a pair`,
      ).toBeNull();
    }
    expect(tagsParamsFrom({ ...DEFAULT_STATE, page: "tags", cluster: null }, NOW)).toBeNull();
  });
});

/* ── 5. useTagsFetch — the R10.22 envelope (S96/S97) ───────────────────────── */

describe("useTagsFetch — the R10.22 envelope (S96/S97)", () => {
  const NOW = new Date("2026-11-23T12:00:00Z");
  /** The http arm, because the fixture arm returns a const without fetching —
   *  every rule in this block is invisible to a `CFG`-based render. */
  const HTTP: ClientConfig = { source: "http", apiBase: "", fixture: "fleet", delayMs: 0 };
  // Annotated `TagsParams`, not the narrower `TagsParams & {namespace; cluster}`
  // the factory returns: `renderHook` infers its Props from `initialProps`, and
  // the narrower type makes every later `rerender` with a plain `TagsParams` an
  // assignment error for a reason that has nothing to do with the test.
  const PARAMS_A: TagsParams = tagsParamsFrom(URL_STATE, NOW)!; // open-webui/open-webui-pg
  const PARAMS_B: TagsParams = { namespace: "postgres-operator", cluster: "postgres-operator-pg" };

  /** A fetch that answers with `body` and counts its calls.
   *
   *  The count is the point, and it is the only instrument for S96: with
   *  `state.tags` in `load`'s dep list the hook refetched in a loop — 21 calls
   *  30 ms after mount, 46 by 60 ms — and the render is byte-identical at one
   *  fetch and at forty-six, so every test above passes either way.
   *  `responseOf`/`stub` from `state/useActiveQueries.test.ts:30-46`, in the
   *  shape this hook needs. */
  function countingFetch(body: unknown): () => number {
    const text = JSON.stringify(body);
    let calls = 0;
    globalThis.fetch = () => {
      calls += 1;
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "OK",
        text: async () => text,
        json: async () => JSON.parse(text) as unknown,
      } as unknown as Response);
    };
    return () => calls;
  }

  /** A fetch whose answers the test hands over by hand, so the in-flight frame
   *  between a request and its response is observable. `pending()` from the same
   *  template, narrowed to the one thing these tests need: answer the n-th
   *  request, optionally with a failure. */
  function holdFetch(): {
    resolve: (n: number, body: unknown, ok?: boolean, status?: number) => void;
  } {
    const open: ((res: Response) => void)[] = [];
    globalThis.fetch = () =>
      new Promise<Response>((resolve) => {
        open.push(resolve);
      });
    return {
      resolve: (n, body, ok = true, status = 200) => {
        const text = JSON.stringify(body);
        open[n]!(
          {
            ok,
            status,
            statusText: ok ? "OK" : "Internal Server Error",
            text: async () => text,
            json: async () => JSON.parse(text) as unknown,
          } as unknown as Response,
        );
      },
    };
  }

  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  it("keys on the cluster pair and nothing else", () => {
    // `activeKey` (state/useActiveQueries.ts:73-75) in this page's vocabulary:
    // the subject is the cluster, so the window, the limit and the sort must not
    // change the key — they change the query, not whose rows these are.
    expect(tagsKey(PARAMS_A)).toBe("open-webui/open-webui-pg");
    expect(tagsKey({ ...PARAMS_A, limit: 50, sort: "cpu" })).toBe(tagsKey(PARAMS_A));
    expect(tagsKey(PARAMS_B)).not.toBe(tagsKey(PARAMS_A));
  });

  it("fetches once per params, not once per render (S96)", async () => {
    const calls = countingFetch(FIXTURE_TAGS);
    const { result } = renderHook(() => useTagsFetch(HTTP, PARAMS_A));

    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    // One fetch — and the payload it returned is the one on screen, so a hook
    // that fetched once and dropped the body would not pass on the count alone.
    expect(calls()).toBe(1);
    expect(result.current.state.tags?.data).toEqual(FIXTURE_TAGS.data);
  });

  it("is idle and never fetches when the URL names no cluster", () => {
    const calls = countingFetch(FIXTURE_TAGS);
    const { result } = renderHook(() => useTagsFetch(HTTP, null));

    expect(result.current.state).toEqual({ phase: "idle", tags: null, error: null, refreshing: false });
    expect(calls()).toBe(0);
  });

  it("drops the other cluster's rows while the new subject is in flight (S97)", async () => {
    const held = holdFetch();
    const { result, rerender } = renderHook(
      ({ params }: { params: TagsParams }) => useTagsFetch(HTTP, params),
      { initialProps: { params: PARAMS_A } },
    );

    await act(async () => {
      held.resolve(0, envelopeWith([TAG_NO_COMMENT]));
    });
    expect(result.current.state.tags?.data[0]?.tagHash).toBe(TAG_NO_COMMENT.tagHash);

    // Navigate to cluster b with b's answer still open. Before the key existed
    // `held` was `state.tags`, so cluster a's rows stayed on screen for the whole
    // in-flight window under cluster b's title. The abort pair stops a's response
    // from *overwriting* b's; only the key stops a's from *displaying* under it.
    rerender({ params: PARAMS_B });
    expect(result.current.state.phase).toBe("loading");
    expect(result.current.state.tags).toBeNull();

    await act(async () => {
      held.resolve(1, envelopeWith([TAG_PARTIAL]));
    });
    expect(result.current.state.tags?.data[0]?.tagHash).toBe(TAG_PARTIAL.tagHash);
  });

  it("holds the rows and flags refreshing when the same subject refetches", async () => {
    // The other half of the key. The caller memoises `params` with
    // `useMemo(..., [state])` (`Tags.tsx:178`), so patching any *other* URL
    // param hands over a new object for the same cluster; that must refetch
    // without blanking the table.
    const held = holdFetch();
    const { result, rerender } = renderHook(
      ({ params }: { params: TagsParams }) => useTagsFetch(HTTP, params),
      { initialProps: { params: PARAMS_A } },
    );
    await act(async () => {
      held.resolve(0, envelopeWith([TAG_NO_COMMENT]));
    });

    rerender({ params: { ...PARAMS_A, limit: 50 } });
    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.refreshing).toBe(true);
    expect(result.current.state.tags?.data[0]?.tagHash).toBe(TAG_NO_COMMENT.tagHash);
  });

  it("keeps the last good frame and the banner when a refresh fails (R10.22)", async () => {
    const held = holdFetch();
    const { result } = renderHook(() => useTagsFetch(HTTP, PARAMS_A));
    await act(async () => {
      held.resolve(0, envelopeWith([TAG_NO_COMMENT]));
    });

    // The error panel's Retry, on a subject whose rows are already on screen.
    // `phase: held === null ? "error" : "ready"` is the line this asserts: a
    // failed refresh over a good frame is ready-with-a-banner, not an error
    // screen replacing a table that still has data in it.
    act(() => result.current.refresh());
    await act(async () => {
      held.resolve(1, {}, false, 500);
    });
    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.tags?.data[0]?.tagHash).toBe(TAG_NO_COMMENT.tagHash);
    expect(result.current.state.error).not.toBeNull();
    expect(result.current.state.refreshing).toBe(false);
  });
});
