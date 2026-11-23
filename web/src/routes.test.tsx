/* R10.3's page set and R10.16's URL scheme, asserted as one artifact.
 *
 * The properties here used to be spread across three places — urlState.ts's
 * regex, App.tsx's JSX, and a paragraph in requirements.md — which is exactly
 * how they drifted: the regex matched `/c/{ns}/{name}` and ignored the rest, so
 * `/c/ns/name/queries` rendered the Overview. With the table in one file, the
 * invariants are testable in one file too, and a page added without a phase or
 * a path fails here instead of in review. */

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { App } from "./App";
import { toQueriesResponse } from "./api/envelope";
import { FIXTURE_FLEET, FIXTURE_QUERY_DETAIL, FIXTURE_QUERIES } from "./api/fixtures";
import {
  ROUTES,
  ROUTE_BY_PAGE,
  buildPath,
  matchPath,
  navFor,
  requiresCluster,
  statusOf,
  type Page,
  type Route,
} from "./routes";

/** R10.3's own wording, in order, copied verbatim from the requirement. If the
 *  table renames a page, this is the line that catches it. */
const R10_3_PAGES: readonly Page[] = [
  "fleet",
  "cluster",
  "queries",
  "query-detail",
  "active",
  "wait-events",
  "databases",
  "users",
  "tags",
  "client-addresses",
  "system",
  "poolers",
  "recommendations",
  "events",
];

const R10_3_TITLES = [
  "Fleet",
  "Cluster Overview",
  "Queries",
  "Query Detail",
  "Active Queries",
  "Wait Events",
  "Databases",
  "Users",
  "Tags",
  "Client Addresses",
  "System Insights",
  "Poolers",
  "Recommendations",
  "Events Timeline",
];

const CLUSTER = "freeidp/freeidp-pg";

/** Every implemented route: the URL it mounts at, the payload it takes, and the
 *  h1 it renders.
 *
 *  `App`'s fixture is generic over the routed page's payload (`AppProps<T>`), so
 *  mounting a page through the shell means handing it *its* type. The queries
 *  page reads a `QueryResponse` and the detail page a `QueryDetailView`; hand
 *  either one the fleet response and the first field it touches is undefined —
 *  which is what broke in this file when the two Phase 6b routes flipped from
 *  `null` to a component. The shell had one fixture, typed for the fleet, only
 *  because until then the two pages that could be mounted were the two that read
 *  it (`ClusterDetail.tsx` takes the prop and never uses it).
 *
 *  The list is asserted against the table below, so it cannot drift the way the
 *  hardcoded pair did: the test was named "for each implemented route" while
 *  rendering Fleet and Cluster Overview by hand, which was true only for as long
 *  as the two lists happened to agree. */
const IMPLEMENTED: ReadonlyArray<{ page: Page; location: string; fixture: unknown; heading: string | RegExp }> = [
  { page: "fleet", location: "/", fixture: FIXTURE_FLEET, heading: "Fleet" },
  // ClusterDetailPage resolves its row from the URL and reads neither `cfg` nor
  // the fixture, so the fleet payload is here to answer the type, not because
  // the page looks at it.
  { page: "cluster", location: `/c/${CLUSTER}`, fixture: FIXTURE_FLEET, heading: /freeidp-pg/ },
  {
    page: "queries",
    location: `/c/${CLUSTER}/queries`,
    fixture: toQueriesResponse(FIXTURE_QUERIES),
    heading: "Queries",
  },
  {
    page: "query-detail",
    location: `/c/${FIXTURE_QUERY_DETAIL.namespace}/${FIXTURE_QUERY_DETAIL.cluster}/queries/${FIXTURE_QUERY_DETAIL.fingerprint}`,
    fixture: FIXTURE_QUERY_DETAIL,
    heading: FIXTURE_QUERY_DETAIL.query,
  },
];

/** Derived from the list above so the nav test and the render test read one
 *  table instead of keeping two copies of the same answer. Routes with no entry
 *  render `NotImplemented`, which takes no payload. */
const FIXTURE_BY_PAGE: Partial<Record<Page, unknown>> = Object.fromEntries(
  IMPLEMENTED.map((entry) => [entry.page, entry.fixture]),
);

describe("the route table", () => {
  it("has exactly the fourteen pages R10.3 names, with their titles", () => {
    expect(ROUTES.map((r) => r.page)).toEqual([...R10_3_PAGES]);
    expect(ROUTES.map((r) => r.title)).toEqual(R10_3_TITLES);
  });

  it("gives every page a unique id and a unique path", () => {
    const ids = ROUTES.map((r) => r.page);
    const paths = ROUTES.map((r) => r.path);
    expect(new Set(ids).size).toBe(ids.length);
    expect(new Set(paths).size).toBe(paths.length);
  });

  it("maps every Page id to a route, and every route to itself", () => {
    // ROUTE_BY_PAGE is a Record<Page, Route>, so a missing id is a compile
    // error; this asserts the runtime shape matches the type's promise.
    for (const id of R10_3_PAGES) {
      expect(ROUTE_BY_PAGE[id].page).toBe(id);
    }
  });

  it("implements exactly the two pages whose phase is 1b", () => {
    // Phrased by phase rather than by "what is implemented today". The old
    // assertion was `implemented === ["fleet","cluster"]`, which was a
    // Phase-1b snapshot wearing an invariant: it had to be edited at every
    // phase, and a green run proved only that someone had edited it. The set of
    // pages *1b owns* is permanent, so this half never needs an edit.
    const fromPhase1b = ROUTES.filter((r) => r.phase === "1b").map((r) => r.page);
    expect(fromPhase1b).toEqual(["fleet", "cluster"]);
    for (const page of fromPhase1b) {
      expect(statusOf(ROUTE_BY_PAGE[page] as Route)).toBe("implemented");
    }
  });

  it("derives status from the component, so nothing is implemented without a component", () => {
    // The rule that replaced the hardcoded list. A route cannot advertise
    // "implemented" with nothing behind it, and a page its owner ships becomes
    // implemented by existing — not by an edit to a file they don't own, which
    // is the shared-tree hazard this table's shape used to create.
    for (const route of ROUTES) {
      expect(statusOf(route), `${route.page}`).toBe(route.component === null ? "planned" : "implemented");
    }
    // And every implemented route's component is a function, not a truthy
    // placeholder: the field's type says so, and this says it at runtime.
    for (const route of ROUTES.filter((r) => r.component !== null)) {
      expect(typeof route.component).toBe("function");
    }
  });

  it("names a requirement and a §9 phase for every page", () => {
    for (const route of ROUTES) {
      expect(route.requirement, `${route.page} has no requirement`).toMatch(/^R\d/);
      expect(route.phase, `${route.page} has no phase`).toMatch(/^\d[abc]?$/);
      // `{ns}`-style path params are part of R8.2's endpoint spellings.
      expect(route.endpoint, `${route.page} has no endpoint`).toMatch(/^GET \/api\/v1\/[a-z{}\/-]+$/);
    }
    // The endpoints are one per page: two pages served by the same URL would
    // mean the table and R8.2's endpoint list have drifted apart.
    const endpoints = ROUTES.map((r) => r.endpoint);
    expect(new Set(endpoints).size).toBe(endpoints.length);
  });

  it("puts every cluster-scoped page under /c/:ns/:name", () => {
    for (const route of ROUTES) {
      if (route.scope === "fleet") {
        expect(route.path).toBe("/");
        continue;
      }
      expect(route.path.startsWith("/c/:ns/:name"), `${route.page} is off the cluster path`).toBe(true);
      expect(requiresCluster(route.page)).toBe(true);
    }
    expect(requiresCluster("fleet")).toBe(false);
  });

  it("keeps Query Detail out of the nav, and everything else in it", () => {
    const hidden = ROUTES.filter((r) => !r.inNav).map((r) => r.page);
    expect(hidden).toEqual(["query-detail"]);
  });
});

describe("path matching", () => {
  /** Every route's own pattern must build a path that parses back to the same
   *  page — the property the old regex-only-in-one-direction setup lacked. */
  it("round-trips every route through buildPath and matchPath", () => {
    for (const route of ROUTES) {
      const row = route.page === "query-detail" ? "0x84f2a91c" : null;
      const path = buildPath(route.page, route.scope === "cluster" ? CLUSTER : null, row);
      const match = matchPath(path);
      expect(match, `${route.path} does not parse`).not.toBeNull();
      expect(match?.page).toBe(route.page);
      expect(match?.cluster).toBe(route.scope === "cluster" ? CLUSTER : null);
      expect(match?.row).toBe(row);
    }
  });

  it("resolves the URLs R10.16 names", () => {
    expect(matchPath("/")).toEqual({ page: "fleet", cluster: null, row: null });
    expect(matchPath("/c/freeidp/freeidp-pg")).toEqual({
      page: "cluster",
      cluster: "freeidp/freeidp-pg",
      row: null,
    });
    expect(matchPath("/c/freeidp/freeidp-pg/queries")).toEqual({
      page: "queries",
      cluster: "freeidp/freeidp-pg",
      row: null,
    });
    // The fingerprint lives in the path here, and `?row=` everywhere else.
    expect(matchPath("/c/freeidp/freeidp-pg/queries/0x84f2")).toEqual({
      page: "query-detail",
      cluster: "freeidp/freeidp-pg",
      row: "0x84f2",
    });
  });

  it("rejects the paths that are not routes instead of guessing", () => {
    // The old regex accepted `/c/ns/name/anything` and rendered the Overview;
    // an unknown segment is now no match at all, so parseUrl falls back to the
    // fleet rather than showing the wrong page.
    expect(matchPath("/c/freeidp/freeidp-pg/nonsense")).toBeNull();
    expect(matchPath("/c/freeidp")).toBeNull();
    expect(matchPath("/settings")).toBeNull();
    expect(matchPath("/c/freeidp/freeidp-pg/queries")).not.toBeNull();
  });

  it("decodes a percent-encoded cluster id and survives a broken escape", () => {
    expect(matchPath("/c/ai-api-gateway/rev-proxy-pg")?.cluster).toBe("ai-api-gateway/rev-proxy-pg");
    // A truncated `%` would make decodeURIComponent throw and blank the page.
    expect(matchPath("/c/freeidp/freeidp-pg/queries/0x%")?.row).toBe("0x%");
  });

  it("builds nothing for a cluster page with no cluster", () => {
    expect(buildPath("queries", null, null)).toBe("/");
    expect(buildPath("queries", "no-slash", null)).toBe("/");
    // Query Detail without a fingerprint falls back to the list it came from.
    expect(buildPath("query-detail", CLUSTER, null)).toBe(`/c/${CLUSTER}/queries/`);
  });
});

describe("navFor", () => {
  it("offers only Fleet on the fleet page", () => {
    expect(navFor("fleet").map((r) => r.page)).toEqual(["fleet"]);
  });

  it("offers Fleet plus every cluster page from a cluster route", () => {
    const nav = navFor("queries");
    expect(nav.map((r) => r.page)).toEqual([
      "fleet",
      "cluster",
      "queries",
      "active",
      "wait-events",
      "databases",
      "users",
      "tags",
      "client-addresses",
      "system",
      "poolers",
      "recommendations",
      "events",
    ]);
    // Query Detail is reachable by selecting a row, never from the nav.
    expect(nav.some((r) => r.page === "query-detail")).toBe(false);
    // Every link it renders points at a path that parses back to that page.
    for (const route of nav) {
      const path = buildPath(route.page, CLUSTER, null);
      expect(matchPath(path)?.page).toBe(route.page);
    }
  });

  it("marks the current page on every route", () => {
    for (const route of ROUTES.filter((r) => r.inNav)) {
      // The payload comes from the same table the render tests use: a route with
      // a component needs its own payload type, and the ten without one ignore
      // the prop entirely, so `undefined` is the right answer for them.
      const { container } = render(
        <App initialLocation={buildPath(route.page, CLUSTER, null)} fixtureOverride={FIXTURE_BY_PAGE[route.page]} />,
      );
      const current = container.querySelector('nav[aria-label="Primary"] a[aria-current="page"]');
      expect(current?.textContent).toBe(route.title);
    }
  });
});

describe("App routing", () => {
  const renderAt = (location: string, fixture?: unknown) =>
    render(<App initialLocation={location} fixtureOverride={fixture} />);

  it("renders the real page for each implemented route", () => {
    // The list above and the table have to agree, or this test is a snapshot of
    // someone's memory. `statusOf` is the derivation of "implemented", so it is
    // what says so here: the old version of this test rendered two pages by hand
    // and was named "for each implemented route" on the strength of the two
    // lists happening to match.
    expect(IMPLEMENTED.map((entry) => entry.page).sort()).toEqual(
      ROUTES.filter((r) => r.component !== null).map((r) => r.page).sort(),
    );
    for (const entry of IMPLEMENTED) {
      const { container, unmount } = renderAt(entry.location, entry.fixture);
      // The h1 alone would also pass on `NotImplemented`, which renders the
      // route's title as its heading; `data-page` is what proves the page's own
      // component mounted rather than the placeholder standing in for it.
      expect(container.querySelector("[data-page]")?.getAttribute("data-page")).toBe(entry.page);
      expect(container.querySelector("h1")?.textContent ?? "").toMatch(entry.heading);
      unmount();
    }
  });

  it("renders the placeholder for each planned route, naming its phase", () => {
    // No count here. The old `toHaveLength(12)` was the same snapshot as the
    // implemented list in the other test — two numbers for one fact, and the
    // set of planned pages is whatever `statusOf` says it is, which the
    // derivation test above already pins. What matters is that every route
    // without a component renders the placeholder, so this iterates that set
    // instead of asserting its size.
    const planned = ROUTES.filter((r) => statusOf(r) === "planned") as Route[];
    for (const route of planned) {
      // Query Detail's path needs a fingerprint — without one `buildPath`
      // returns its parent list, which is the correct fallback but renders the
      // Queries page, not this one. It is implemented now, so the branch is
      // dormant here and live in the table above.
      const row = route.page === "query-detail" ? "0x84f2a91c" : null;
      const { unmount } = renderAt(buildPath(route.page, CLUSTER, row));
      expect(screen.getByRole("heading", { level: 1, name: route.title })).toBeInTheDocument();
      expect(screen.getByText(/Not built yet/)).toBeInTheDocument();
      expect(screen.getByText(`§9 Phase ${route.phase}`, { exact: false })).toBeInTheDocument();
      unmount();
    }
  });
});
