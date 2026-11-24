/* The placeholder every route without a page renders (R10.3's twelve).
 *
 * The component's job is to be honest about an absence, so the tests are the
 * three things it promises and nothing more: it prints the route table's own
 * words — title, requirement ids, §9 phase, R8.2 endpoint — rather than a
 * sentence of its own that could disagree with the table; it names a cluster
 * only when the route needs one *and* one is selected; and its single control
 * goes back to the Fleet page.
 *
 * Reading the values off `ROUTE_BY_PAGE` instead of typing them is deliberate.
 * `routes.ts` is not A6's file and this one is, so the assertion that matters is
 * "the page renders what the table says" — the claim the header makes — which
 * stays true when the table changes and fails when the placeholder starts
 * inventing. The same reason `App.tsx` reads `route.component` now.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { NotImplemented } from "./NotImplemented";
import { ROUTES, ROUTE_BY_PAGE, type Route } from "../routes";
import { DEFAULT_STATE, withState, type UrlState } from "../state/urlState";

/** Render one route's placeholder and collect every patch it pushes. */
function renderFor(route: Route, state: UrlState = DEFAULT_STATE) {
  const patches: Partial<UrlState>[] = [];
  const view = render(
    <NotImplemented
      route={route}
      state={state}
      onPatch={(p) => {
        patches.push(p);
      }}
    />,
  );
  return { ...view, patches };
}

/** The `<dt>`/`<dd>` pairs of the meta list as a map, so a test asks for a row
 *  by its term instead of by an index that moves when a row is added. */
function meta(container: HTMLElement): Record<string, string> {
  const out: Record<string, string> = {};
  const dds = container.querySelectorAll("dl.not-implemented-meta dd");
  container.querySelectorAll("dl.not-implemented-meta dt").forEach((dt, i) => {
    const dd = dds[i];
    if (dd !== undefined) out[dt.textContent ?? ""] = dd.textContent ?? "";
  });
  return out;
}

describe("NotImplemented page", () => {
  const users = ROUTE_BY_PAGE.users;

  it("renders the route table's own words, not a sentence of its own", () => {
    const { container } = renderFor(users);
    const section = container.querySelector("section.not-implemented");
    expect(section).not.toBeNull();
    expect(section?.getAttribute("data-page")).toBe(users.page);
    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: users.title })).toBeInTheDocument();

    const m = meta(container);
    expect(m.Requirement).toBe(users.requirement);
    expect(m["Ships in"]).toBe(`§9 Phase ${users.phase}`);
    expect(m.Endpoint).toBe(users.endpoint);
    // The prose carries the same phase the table does, inside the sentence that
    // promises it — "assigns this page to Phase N of the plan in §9" — so the
    // two places the phase appears cannot disagree.
    expect(container.querySelector(".state-detail")?.textContent).toContain(`Phase ${users.phase}`);
  });

  it("names the cluster only when the route needs one and one is selected", () => {
    // The one conditional in the file. A cluster-scoped route with no selected
    // cluster would otherwise print a `Cluster` row with an empty value, and an
    // empty value next to a label is how a page starts lying about its data.
    const withCluster = renderFor(
      users,
      withState(DEFAULT_STATE, { page: "users", cluster: "freeidp/freeidp-pg" }),
    );
    expect(meta(withCluster.container).Cluster).toBe("freeidp/freeidp-pg");

    const withoutCluster = renderFor(users, DEFAULT_STATE);
    expect(meta(withoutCluster.container).Cluster).toBeUndefined();

    // A fleet-scoped route has no cluster to name even when one is selected: the
    // guard is `scope === "cluster"` *and* `state.cluster !== null`, not the
    // second condition alone.
    const fleet = renderFor(
      ROUTE_BY_PAGE.fleet,
      withState(DEFAULT_STATE, { cluster: "freeidp/freeidp-pg" }),
    );
    expect(meta(fleet.container).Cluster).toBeUndefined();
  });

  it("sends its one control back to the fleet", () => {
    const { container, patches } = renderFor(
      users,
      withState(DEFAULT_STATE, { page: "users", cluster: "freeidp/freeidp-pg" }),
    );
    const back = container.querySelector("a.btn");
    expect(back).not.toBeNull();
    expect(back?.getAttribute("href")).toBe("/");
    fireEvent.click(back as HTMLElement);
    // `navTo` is the shell's own click handler, so the placeholder navigates the
    // way the nav bar does: a plain click patches the state, and the patch clears
    // the cluster as well as the page, because R10.16's URL has no cluster
    // without a cluster page.
    expect(patches).toEqual([{ page: "fleet", cluster: null }]);
  });

  it("answers for every route that has no component", () => {
    // The header's "twelve" is a moving number — Phase 6b added two pages — so
    // this derives the set from the table instead of restating it, the way
    // `routes.test.tsx` does. Every route whose `component` is null is a route
    // `App.tsx` sends here, and each one has to arrive with a title, a phase and
    // an endpoint already filled in.
    const planned = ROUTES.filter((r) => r.component === null);
    expect(planned.length).toBeGreaterThan(0);
    for (const route of planned) {
      const { container } = renderFor(route);
      expect(container.querySelector(`section.not-implemented[data-page="${route.page}"]`)).not.toBeNull();
      expect(container.querySelector(".state-title")?.textContent).toBe(route.title);
      expect(container.querySelector(".state-detail")?.textContent).toContain(`Phase ${route.phase}`);
      expect(meta(container).Requirement).toBe(route.requirement);
      expect(meta(container).Endpoint).toBe(route.endpoint);
    }
  });
});
