/* R10.3's nav, and the two rules that decide which links exist.
 *
 * The component is 82 lines with no state of its own, and everything interesting
 * in it is a projection of the route table: `navFor(state.page)` for the link
 * set, `statusOf(route)` for the styling hook, `buildPath` for the href, and
 * `targetFor(route)` for what a click patches. So the tests read their
 * expectations out of `ROUTES` rather than typing them — the same argument the
 * component's own header makes against the hard-coded `<a href="/">Fleet</a>`
 * this replaced, applied to the test: a list written twice is a list that drifts.
 *
 * The click tests are the router-contract half (R10.16): a plain click patches
 * the state, and a modifier or middle click does not, because a nav that
 * swallows "open in new tab" is a regression the spec names.
 */

import type { MouseEvent } from "react";
import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Nav, navTo } from "./Nav";
import { ROUTES, ROUTE_BY_PAGE } from "../routes";
import { DEFAULT_STATE, withState, type UrlState } from "../state/urlState";

/** Render the nav for a state and collect every patch it pushes. */
function renderNav(state: UrlState) {
  const patches: Partial<UrlState>[] = [];
  const view = render(<Nav state={state} onPatch={(p) => { patches.push(p); }} />);
  return { ...view, patches };
}

/** The nav's links, in DOM order, as `[text, href, status, aria-current]`. */
function links(container: HTMLElement): [string, string, string | null, string | null][] {
  return Array.from(container.querySelectorAll("nav.nav a")).map((a) => [
    a.textContent ?? "",
    a.getAttribute("href") ?? "",
    a.getAttribute("data-status"),
    a.getAttribute("aria-current"),
  ]);
}

describe("Nav", () => {
  it("is Fleet alone on the fleet page", () => {
    // navFor's rule, stated in the header: every other page needs a cluster, and
    // twelve links that all currently mean "not built yet" is noise in front of
    // the one page that answers.
    const { container } = renderNav(DEFAULT_STATE);
    expect(container.querySelector("nav.nav")).not.toBeNull();
    expect(container.querySelector("nav.nav")?.getAttribute("aria-label")).toBe("Primary");
    expect(links(container)).toEqual([["Fleet", "/", "implemented", "page"]]);
  });

  it("is Fleet plus every cluster-scoped nav page on a cluster route", () => {
    const state = withState(DEFAULT_STATE, { page: "cluster", cluster: "freeidp/freeidp-pg" });
    const { container } = renderNav(state);
    const expected = [
      "Fleet",
      ...ROUTES.filter((r) => r.scope === "cluster" && r.inNav).map((r) => r.title),
    ];
    const found = links(container);
    expect(found.map((l) => l[0])).toEqual(expected);
    // Every cluster-scoped href carries the selected cluster — `buildPath`'s
    // job, and the reason the nav can render a link to a page the URL is not
    // currently on. The cluster root itself has no trailing segment, so the
    // prefix check stops at the name rather than requiring a `/` after it.
    for (const [, href] of found) {
      expect(href === "/" || href.startsWith("/c/freeidp/freeidp-pg")).toBe(true);
    }
  });

  it("omits Query Detail because the table says so, not because the JSX does", () => {
    // The header's claim is that this component has no special case for the one
    // page reachable only from a table row. The assertion is therefore paired:
    // the route exists with `inNav: false`, and its title is not in the nav.
    const detail = ROUTE_BY_PAGE["query-detail"];
    expect(detail.inNav).toBe(false);
    const state = withState(DEFAULT_STATE, { page: "query-detail", cluster: "freeidp/freeidp-pg", row: "abc123" });
    const { container } = renderNav(state);
    expect(links(container).map((l) => l[0])).not.toContain(detail.title);
    // The consequence worth pinning: a page the table keeps out of the nav cannot
    // be marked current either, so the nav renders no `aria-current` at all here
    // rather than highlighting a link the reader is not on.
    expect(links(container).filter((l) => l[3] === "page")).toHaveLength(0);
  });

  it("marks exactly one link as current, with the class and the aria attribute", () => {
    const state = withState(DEFAULT_STATE, { page: "users", cluster: "ai-api-gateway/rev-proxy-pg" });
    const { container } = renderNav(state);
    const found = links(container);
    const current = found.filter((l) => l[3] === "page");
    expect(current).toHaveLength(1);
    expect(current[0]?.[0]).toBe(ROUTE_BY_PAGE.users.title);
    expect(container.querySelectorAll("nav.nav a.active")).toHaveLength(1);
    expect(container.querySelector("nav.nav a.active")?.textContent).toBe(ROUTE_BY_PAGE.users.title);
  });

  it("puts the route's status on every link", () => {
    // `data-status` is the styling hook for "planned" vs "implemented", and it
    // comes from `statusOf(route)` — derived from `Route.component`, so a page
    // that lands cannot be rendered as still-planned by a stale class.
    const state = withState(DEFAULT_STATE, { page: "cluster", cluster: "freeidp/freeidp-pg" });
    const { container } = renderNav(state);
    const byTitle = new Map(links(container).map((l) => [l[0], l[2]]));
    for (const route of ROUTES) {
      if (!route.inNav) continue;
      expect(byTitle.get(route.title)).toBe(route.component === null ? "planned" : "implemented");
    }
  });

  it("patches on a plain click and not on a modifier click", () => {
    const state = withState(DEFAULT_STATE, { page: "users", cluster: "freeidp/freeidp-pg" });
    const { container, patches } = renderNav(state);
    const users = Array.from(container.querySelectorAll("nav.nav a")).find(
      (a) => a.textContent === ROUTE_BY_PAGE.users.title,
    );
    expect(users).toBeDefined();
    if (users === undefined) return;

    fireEvent.click(users, { metaKey: true });
    expect(patches).toEqual([]);
    fireEvent.click(users, { button: 1 });
    expect(patches).toEqual([]);
    fireEvent.click(users);
    // A cluster-scoped link patches the page and lets the existing cluster ride
    // along — `withState` requires one, and dropping it would navigate to a
    // cluster page with no cluster.
    expect(patches).toEqual([{ page: "users" }]);
  });

  it("clears the cluster when the fleet link is clicked", () => {
    const state = withState(DEFAULT_STATE, { page: "users", cluster: "freeidp/freeidp-pg" });
    const { container, patches } = renderNav(state);
    const fleet = Array.from(container.querySelectorAll("nav.nav a")).find((a) => a.textContent === "Fleet");
    expect(fleet).toBeDefined();
    if (fleet === undefined) return;
    fireEvent.click(fleet);
    expect(patches).toEqual([{ page: "fleet", cluster: null }]);
  });
});

describe("navTo", () => {
  it("returns a handler that patches the state it was given", () => {
    // The brand link in App.tsx and every nav link share this one function, so
    // the pair of behaviours — patch on plain click, nothing on modifier — is
    // asserted once here rather than once per call site.
    const patches: Partial<UrlState>[] = [];
    const handler = navTo((p) => { patches.push(p); }, { page: "queries" });
    const plain = { metaKey: false, ctrlKey: false, shiftKey: false, button: 0, preventDefault: () => {} };
    handler(plain as MouseEvent<HTMLAnchorElement>);
    expect(patches).toEqual([{ page: "queries" }]);
    let prevented = 0;
    const cmdClick = {
      metaKey: true,
      ctrlKey: false,
      shiftKey: false,
      button: 0,
      preventDefault: () => { prevented += 1; },
    };
    handler(cmdClick as MouseEvent<HTMLAnchorElement>);
    expect(patches).toEqual([{ page: "queries" }]);
    expect(prevented).toBe(0);
  });
});
