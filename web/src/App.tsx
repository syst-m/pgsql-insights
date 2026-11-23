/* The app shell and the router.
 *
 * Routing is URL-first (R10.16): `App` holds exactly one piece of state — the
 * parsed URL — and every page change is a `pushState` that flows back through
 * the popstate subscription. That is what makes the back button work and what
 * lets the R16.7 suite address any screen by URL alone.
 *
 * The shell is deliberately thin: brand, nav, theme control. Everything with
 * data in it lives in a page. The nav is `routes.ts` rendered (see
 * components/Nav.tsx) and the page switch below is a lookup in that same table,
 * so adding a page is a table edit plus a component and never an edit here.
 * That sentence used to be false — the switch was an if-chain on `route.page`,
 * so each of the other twelve pages needed an edit here when it landed. It now
 * reads `Route.component`, which is what makes it true.
 */

import { useCallback, useEffect, useMemo, useState } from "react";

import { resolveConfig } from "./api/client";
import {
  DEFAULT_STATE,
  formatQuery,
  parseUrl,
  pushState,
  readState,
  subscribeToHistory,
  withState,
  type UrlState,
} from "./state/urlState";
import { Nav, navTo } from "./components/Nav";
import { ThemeToggle } from "./components/ThemeToggle";
import { NotImplemented } from "./pages/NotImplemented";
import { ROUTE_BY_PAGE, type PageProps } from "./routes";
import type { FleetResponse } from "./api/types";

export interface AppProps {
  /** Test-only: render a fixed location instead of the real window's. */
  initialLocation?: string;
  /** Test-only: render a fixed payload, skipping the fetch. */
  fixtureOverride?: FleetResponse;
}

export function App({ initialLocation, fixtureOverride }: AppProps = {}) {
  const [urlState, setUrlState] = useState<UrlState>(() => initialLocation !== undefined
    ? parseUrl(initialLocation)
    : typeof window === "undefined"
      ? DEFAULT_STATE
      : readState(window.location),
  );

  useEffect(() => {
    if (initialLocation !== undefined || typeof window === "undefined") return;
    return subscribeToHistory(window, () => readState(window.location), setUrlState);
  }, [initialLocation]);

  const patch = useCallback(
    (p: Partial<UrlState>) => {
      setUrlState((prev) => {
        const next = withState(prev, p);
        if (initialLocation === undefined && typeof window !== "undefined") pushState(window.history, next);
        return next;
      });
    },
    [initialLocation],
  );

  // The client config comes from the same parsed URL the pages render from.
  // Deriving it here, rather than reading `window.location.search` once at
  // mount, is what keeps `?fixture=partial` alive after a sort click rewrites
  // the URL — otherwise the first navigation silently switches the source.
  const cfg = useMemo(
    () => resolveConfig(formatQuery(urlState), import.meta.env as unknown as Record<string, string | undefined>),
    [urlState],
  );

  return (
    <div className="shell">
      <header className="topbar">
        <a className="brand" href="/" onClick={navTo(patch, { page: "fleet", cluster: null })}>
          pgsi
        </a>
        <Nav state={urlState} onPatch={patch} />
        <div className="topbar-right">
          <ThemeToggle />
        </div>
      </header>

      <main className="main">
        <PageView state={urlState} onPatch={patch} cfg={cfg} fixtureOverride={fixtureOverride} />
      </main>
    </div>
  );
}

/** The page switch: the route table's component for this page, or the
 *  placeholder that names the phase that will build it.
 *
 *  Two of the fourteen R10.3 pages have components at Phase 1b; the other
 *  twelve render `NotImplemented`. Routing them is what makes R10.3's page set
 *  one testable artifact today — every page in the table has a URL that answers,
 *  so a page landing at Phase 6b swaps a component without changing a route, a
 *  nav entry, a path, or this file. That last clause is why `Route` carries the
 *  component: the if-chain this replaced had a branch per page, so twelve more
 *  pages meant twelve more edits to a file A6 doesn't own. */
function PageView(props: PageProps) {
  const route = ROUTE_BY_PAGE[props.state.page];
  const Page = route.component;

  if (Page === null) {
    return <NotImplemented route={route} state={props.state} onPatch={props.onPatch} />;
  }
  return <Page {...props} />;
}
