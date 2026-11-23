/* R10.3's navigation, rendered from the route table.
 *
 * This used to be one hard-coded `<a href="/">Fleet</a>` in App.tsx. Now the
 * link set is `navFor(state.page)` mapped to anchors, so which links appear is
 * the table's answer rather than a second list someone has to keep in sync: a
 * page added to ROUTES appears here without an edit, and a page removed cannot
 * leave a dead link behind. `routes.test.ts` is what makes the table itself
 * trustworthy.
 *
 * Which links show is `navFor`'s rule, not this component's: on `/` the nav is
 * Fleet alone — every other page needs a cluster, and twelve links that all
 * currently mean "not built yet" is noise in front of the one page that answers.
 * On a cluster route it is Fleet (the way back, which is R10.24's breadcrumb in
 * nav form) plus every cluster-scoped page. Query Detail is absent because
 * `inNav` says so, not because this JSX special-cases it.
 */

import type { MouseEvent } from "react";

import { buildPath, navFor, statusOf, type Route } from "../routes";
import type { UrlState } from "../state/urlState";

/** A plain-click handler that keeps modifier-click and middle-click semantics
 *  (open in new tab) intact — a router that swallows those is a regression.
 *
 *  Lives here rather than in App.tsx because both the brand link and every nav
 *  link need it, and importing it from App would make Nav a dependency of the
 *  component that renders it. */
export function navTo(
  patch: (p: Partial<UrlState>) => void,
  to: Partial<UrlState>,
): (e: MouseEvent<HTMLAnchorElement>) => void {
  return (e: MouseEvent<HTMLAnchorElement>): void => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    patch(to);
  };
}

export interface NavProps {
  state: UrlState;
  onPatch: (patch: Partial<UrlState>) => void;
}

export function Nav({ state, onPatch }: NavProps) {
  return (
    <nav className="nav" aria-label="Primary">
      {navFor(state.page).map((route) => (
        <NavLink key={route.page} route={route} state={state} onPatch={onPatch} />
      ))}
    </nav>
  );
}

function NavLink({ route, state, onPatch }: { route: Route; state: UrlState; onPatch: NavProps["onPatch"] }) {
  const isCurrent = route.page === state.page;
  return (
    <a
      href={buildPath(route.page, state.cluster, null)}
      // `data-status` lets the stylesheet mark a planned link differently from
      // a live one (a dotted underline in R10.2's muted token) without the
      // component having to invent a second class per state.
      data-status={statusOf(route)}
      className={isCurrent ? "active" : undefined}
      aria-current={isCurrent ? "page" : undefined}
      onClick={navTo(onPatch, targetFor(route))}
    >
      {route.title}
    </a>
  );
}

/** The patch a click on this link applies.
 *
 *  A cluster-scoped link is only meaningful with the current cluster attached,
 *  and `withState` requires one — so the patch carries the page and lets the
 *  existing cluster ride along. The fleet link clears the selection, the same
 *  way the brand link does: leaving a cluster page for the list means the row
 *  is no longer selected. */
function targetFor(route: Route): Partial<UrlState> {
  return route.scope === "cluster" ? { page: route.page } : { page: route.page, cluster: null };
}
