/* R10.7's page shell — the URL half and the composition half.
 *
 * The four blocks each have their own suite; this file owns what only the page
 * has:
 *
 * 1. **The URL contract** — `detailParamsFrom` is the single function that turns
 *    `UrlState` into the route's params, and it is the file A3's R10.7 ruling
 *    changed. Its cases are tested as a pure function (the triple, the missing
 *    pieces, the two optionals) because that is where a route change lands. The
 *    `null` it returns has a render half — the no-such-query frame — asserted in
 *    the page suite, because a page that renders the fixture for an
 *    address-less URL passes every other test in this file.
 * 2. **The header** — R10.7's four fields, drawn from both halves of the payload
 *    (the embedded `QueryView` and the detail-only `user`/`firstSeen`), which is
 *    the visible proof that the `allOf` composition was rendered rather than
 *    half of it.
 * 3. **The composition** — all four panels present, and the `series` prop
 *    actually reaching the load chart. A page that renders a component but never
 *    passes it the data is the bug a render test catches and a typecheck doesn't.
 *
 * Rendered directly, not through `<App>`: the page's own props are what
 * `PageProps<T>` exists to let its test pass, and `routes.ts:208`'s
 * `component: QueryDetailPage` is what the shell renders for the route.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { ClientConfig } from "../api/client";
import { FIXTURE_QUERY_DETAIL } from "../api/fixtures";
import type { QueryDetailView, QuerySeries } from "../api/types";
import { buildPath } from "../routes";
import { DEFAULT_STATE, type UrlState } from "../state/urlState";
import { formatTimestamp, utcTitle } from "../lib/format";
import { detailParamsFrom, QueryDetailPage, type QueryDetailPageProps } from "./QueryDetail";

const CFG: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };
const FP = FIXTURE_QUERY_DETAIL.fingerprint;
const CLUSTER_ID = `${FIXTURE_QUERY_DETAIL.namespace}/${FIXTURE_QUERY_DETAIL.cluster}`;

/** The URL this page is mounted from: cluster-scoped with a selected row, which
 *  is exactly what R10.16's `/c/:ns/:name/queries/:fingerprint` decodes to. */
const URL_STATE: UrlState = {
  ...DEFAULT_STATE,
  page: "query-detail",
  cluster: CLUSTER_ID,
  row: FP,
};

const SERIES: QuerySeries = {
  fingerprint: FP,
  namespace: FIXTURE_QUERY_DETAIL.namespace,
  cluster: FIXTURE_QUERY_DETAIL.cluster,
  points: [
    {
      timestamp: "2026-11-20T09:11:00Z",
      executionTimeSeconds: 1.8,
      rows: 12,
      sharedBlocksHit: 40,
      sharedBlocksRead: 7,
      lockTimeSeconds: 0.2,
      ioTimeSeconds: 0.024,
    },
    {
      timestamp: "2026-11-20T09:12:00Z",
      executionTimeSeconds: 42.7,
      rows: 981,
      sharedBlocksHit: 2044,
      sharedBlocksRead: 61,
      lockTimeSeconds: 1.4,
      ioTimeSeconds: 0.2092,
    },
  ],
};

function renderPage(overrides: Partial<QueryDetailPageProps> = {}) {
  const onPatch = vi.fn();
  const props: QueryDetailPageProps = {
    state: URL_STATE,
    onPatch,
    cfg: CFG,
    fixtureOverride: FIXTURE_QUERY_DETAIL,
    series: SERIES,
    ...overrides,
  };
  return { ...render(<QueryDetailPage {...props} />), onPatch };
}

describe("detailParamsFrom — A3's R10.7 ruling, in one function", () => {
  it("reads the required triple out of the URL state", () => {
    // R8.2's route takes the fingerprint as a path segment and namespace/cluster
    // as required query parameters, because R3.4 makes a fingerprint canonical
    // *within* a cluster. `UrlState.cluster` is the `namespace/name` form both
    // directions of the route use.
    expect(detailParamsFrom(URL_STATE)).toEqual({
      namespace: "open-webui",
      cluster: "open-webui-pg",
      fingerprint: FP,
      db: undefined,
      user: undefined,
    });
  });

  it("returns null with no fingerprint", () => {
    // A detail page addressed at nothing has nothing to render, and `buildPath`
    // already refuses to build a path for it — the page asks the same question.
    expect(detailParamsFrom({ ...URL_STATE, row: null })).toBeNull();
    expect(detailParamsFrom({ ...URL_STATE, row: "" })).toBeNull();
    expect(detailParamsFrom({ ...URL_STATE, row: "   " })).toBeNull();
  });

  it("returns null with no cluster", () => {
    // A fleet-scoped URL (`state.cluster === null`) has a fingerprint but no
    // cluster to resolve it in, which is not an addressable query.
    expect(detailParamsFrom({ ...DEFAULT_STATE, row: FP })).toBeNull();
    expect(detailParamsFrom({ ...URL_STATE, cluster: "" })).toBeNull();
  });

  it("returns null for a cluster that is not a namespace/name pair", () => {
    // The split rule lives here and in `buildPath`, and both refuse a bare name
    // rather than guessing a namespace for it.
    expect(detailParamsFrom({ ...URL_STATE, cluster: "open-webui-pg" })).toBeNull();
  });

  it("carries the optional db and user when the URL has them", () => {
    // A3's ruling exists so a viewer filtered to one user in R10.6's table gets a
    // detail page whose header agrees with the filter they arrived through. The
    // fields arrive in `UrlState` when A4 widens it; until then they are read
    // through the same widening cast the Queries page uses for `database`.
    const withOptionals = { ...URL_STATE, db: "openwebui", user: "openwebui" } as UrlState & {
      db?: string;
      user?: string;
    };
    const params = detailParamsFrom(withOptionals);
    expect(params?.db).toBe("openwebui");
    expect(params?.user).toBe("openwebui");
  });

  it("omits an empty db or user rather than passing an empty discriminator", () => {
    // `?user=` present-but-blank means "not filtered", and forwarding `user=""`
    // to the API asks a different question from omitting it.
    const blank = { ...URL_STATE, db: "", user: "  " } as UrlState & {
      db?: string;
      user?: string;
    };
    const params = detailParamsFrom(blank);
    expect(params?.db).toBeUndefined();
    expect(params?.user).toBeUndefined();
  });
});

describe("QueryDetailPage", () => {
  it("renders the page shell and the params it extracted", () => {
    // The extraction is on the element, so a page that parses the URL into a
    // local and never uses it fails here instead of shipping.
    const { container } = renderPage();
    const page = container.querySelector(".page");
    expect(page?.getAttribute("data-page")).toBe("query-detail");
    expect(page?.getAttribute("data-fingerprint")).toBe(FP);
  });

  it("renders R10.24's trail plus the list the route passes through", () => {
    // R10.24 names "Fleet → Cluster → Query Detail" in those words. The Queries
    // page sits between the two — it is the page that links here, and R10.16's
    // path spells the level — so the crumb renders four levels.
    const { container } = renderPage();
    const links = container.querySelectorAll(".breadcrumb a");
    expect(Array.from(links).map((a) => a.getAttribute("href"))).toEqual([
      "/",
      buildPath("cluster", CLUSTER_ID, null),
      buildPath("queries", CLUSTER_ID, null),
    ]);
    expect(Array.from(links).map((a) => a.textContent)).toEqual(["Fleet", CLUSTER_ID, "Queries"]);
    expect(container.querySelector(".breadcrumb")?.textContent).toContain("Query Detail");
  });

  it("titles the page with the statement, not the hash", () => {
    // A page titled with a SHA-256 is not scannable, so the SQL text is the title
    // and the fingerprint is a labelled field under it.
    const { container } = renderPage();
    const title = container.querySelector("h1.page-title");
    expect(title?.textContent).toBe(FIXTURE_QUERY_DETAIL.query);
    expect(title?.getAttribute("title")).toBe(FIXTURE_QUERY_DETAIL.query);
  });

  it("renders the header from both halves of the payload", () => {
    // `QueryDetailView` is `QueryView` embedded plus five fields. The header
    // shows two from each half — fingerprint/database from the embedded one,
    // user/firstSeen from the detail-only one — which is the visible proof the
    // composition was rendered rather than half of it.
    const { container } = renderPage();
    const header = container.querySelector("[data-testid='detail-header']");
    const labels = Array.from(header?.querySelectorAll("dt") ?? []).map((n) => n.textContent);
    expect(labels).toEqual(["Fingerprint", "Database", "User", "First seen", "queryid", "Truncated"]);

    const values = Array.from(header?.querySelectorAll("dd") ?? []).map((n) => n.textContent);
    expect(values[0]).toBe(FP);
    expect(values[1]).toBe(FIXTURE_QUERY_DETAIL.database);
    expect(values[2]).toBe(FIXTURE_QUERY_DETAIL.user);
    expect(values[3]).toBe(formatTimestamp(FIXTURE_QUERY_DETAIL.firstSeen));
    expect(values[4]).toBe(FIXTURE_QUERY_DETAIL.queryid);
    expect(values[5]).toBe("no");
    // The UTC hover is R10.21's contract on every timestamp the UI prints.
    expect(header?.querySelectorAll("dd")[3]?.getAttribute("title")).toBe(
      utcTitle(FIXTURE_QUERY_DETAIL.firstSeen),
    );
  });

  it("renders all four R10.7 blocks in the requirement's order", () => {
    // Load chart, latency panel, plan samples list with its viewer, recommendations.
    const { container } = renderPage();
    expect(
      Array.from(container.querySelectorAll("section[data-panel]")).map((s) => s.getAttribute("data-panel")),
    ).toEqual(["load", "latency", "plans", "plan-viewer", "recommendations"]);
  });

  it("passes the series prop through to the load chart", () => {
    // The DTO cannot inline the series (`types.ts:584`: an import cycle), so it
    // arrives as a prop. A page that accepts it and never forwards it typechecks
    // and renders an empty panel — this is the assertion that catches that.
    const withSeries = renderPage();
    expect(withSeries.container.querySelectorAll("[data-panel='load'] .small-multiple")).toHaveLength(4);
    const without = renderPage({ series: undefined });
    expect(without.container.querySelector("[data-panel='load'] .small-multiples")).toBeNull();
    expect(without.container.querySelector("[data-panel='load'] .chart-empty")).not.toBeNull();
  });

  it("renders the payload it is given, not only the fixture", () => {
    // `fixtureOverride` is the visual suite's seam and, now that
    // `client.ts:387`'s `fetchQueryDetail` exists, the shape the fetched payload
    // arrives in too — the page's chain is prop, then the hook's payload, then the
    // fixture. A page that renders the fixture unconditionally passes every test
    // and shows the wrong query.
    const other: QueryDetailView = { ...FIXTURE_QUERY_DETAIL, fingerprint: "b".repeat(64) };
    const { container } = renderPage({
      fixtureOverride: other,
      state: { ...URL_STATE, row: "b".repeat(64) },
    });
    expect(container.querySelector(".page")?.getAttribute("data-fingerprint")).toBe("b".repeat(64));
    expect(container.querySelector(".detail-header .mono")?.textContent).toBe("b".repeat(64));
  });

  it("renders a no-such-query frame when the URL names no fingerprint", () => {
    // The render half of `detailParamsFrom`'s `null`. `withState` degrades a
    // fingerprint-less query-detail URL to the Queries list, so this is the
    // hand-typed case, and the page's answer is a frame that says what is missing
    // rather than the fixture under a heading the URL does not name. The button
    // patches back to Queries at the same cluster, which is where R10.16 puts the
    // fingerprint.
    const { container, onPatch } = renderPage({
      state: { ...URL_STATE, row: null },
      fixtureOverride: undefined,
      series: undefined,
    });
    expect(container.querySelector(".page")).toBeNull();
    expect(container.textContent).toContain("does not name a fingerprint");
    expect(container.textContent).toContain(CLUSTER_ID);
    fireEvent.click(screen.getByRole("button", { name: "Back to Queries" }));
    expect(onPatch).toHaveBeenCalledWith({ page: "queries", cluster: CLUSTER_ID });
  });
});
