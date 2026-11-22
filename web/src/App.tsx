/* The app shell and the router.
 *
 * Routing is URL-first (R10.16): `App` holds exactly one piece of state — the
 * parsed URL — and every page change is a `pushState` that flows back through
 * the popstate subscription. That is what makes the back button work and what
 * lets the R16.7 suite address any screen by URL alone.
 *
 * The shell is deliberately thin: brand, nav, theme control. Everything with
 * data in it lives in a page. The nav is `routes.ts` rendered (see
 * components/Nav.tsx) and the page switch below is the same table consulted by
 * page id, so adding a page is a table edit plus a component — never an edit
 * here.
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
import { FleetPage } from "./pages/Fleet";
import { ClusterDetailPage } from "./pages/ClusterDetail";
import { NotImplemented } from "./pages/NotImplemented";
import { ROUTE_BY_PAGE } from "./routes";
import { FIXTURE_CLUSTERS } from "./api/fixtures";
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

/** The page switch, keyed off the route table rather than off an if-chain.
 *
 *  Two of the fourteen R10.3 pages have components at Phase 1b; the other
 *  twelve render `NotImplemented`, which names the §9 phase that will build
 *  them. Routing them is what makes R10.3's page set one testable artifact
 *  today — every page in the table has a URL that answers, so a page landing at
 *  Phase 6b swaps a component without changing a route, a nav entry or a path. */
function PageView({
  state,
  onPatch,
  cfg,
  fixtureOverride,
}: {
  state: UrlState;
  onPatch: (p: Partial<UrlState>) => void;
  cfg: ReturnType<typeof resolveConfig>;
  fixtureOverride?: FleetResponse;
}) {
  const route = ROUTE_BY_PAGE[state.page];

  if (route.page === "fleet") {
    return <FleetPage state={state} onPatch={onPatch} cfg={cfg} fixtureOverride={fixtureOverride} />;
  }
  if (route.page === "cluster") {
    return <ClusterOrFleet state={state} onPatch={onPatch} />;
  }
  return <NotImplemented route={route} state={state} onPatch={onPatch} />;
}

/** Resolve the detail route. An unknown cluster id falls back to a card with a
 *  way back — a stale link should degrade, not blank the page. */
function ClusterOrFleet({ state, onPatch }: { state: UrlState; onPatch: (p: Partial<UrlState>) => void }) {
  const cluster = FIXTURE_CLUSTERS.find((c) => `${c.namespace}/${c.name}` === state.cluster);
  if (cluster === undefined) {
    return (
      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Unknown cluster</h2>
        </div>
        <p className="card-note">
          <code>{state.cluster ?? ""}</code> is not in the current fleet snapshot.
        </p>
        <button type="button" className="btn" onClick={() => onPatch({ page: "fleet", cluster: null })}>
          Back to Fleet
        </button>
      </section>
    );
  }
  return <ClusterDetailPage cluster={cluster} state={state} onPatch={onPatch} />;
}
