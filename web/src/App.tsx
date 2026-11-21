/* The app shell and the router.
 *
 * Routing is URL-first (R10.16): `App` holds exactly one piece of state — the
 * parsed URL — and every page change is a `pushState` that flows back through
 * the popstate subscription. That is what makes the back button work and what
 * lets the R16.7 suite address any screen by URL alone.
 *
 * The shell is deliberately thin: brand, nav, theme control. Everything with
 * data in it lives in a page.
 */

import { useCallback, useEffect, useMemo, useState, type MouseEvent } from "react";

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
import { ThemeToggle } from "./components/ThemeToggle";
import { FleetPage } from "./pages/Fleet";
import { ClusterDetailPage } from "./pages/ClusterDetail";
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
        <nav className="nav" aria-label="Primary">
          <a
            href="/"
            className={urlState.page === "fleet" ? "active" : undefined}
            aria-current={urlState.page === "fleet" ? "page" : undefined}
            onClick={navTo(patch, { page: "fleet", cluster: null })}
          >
            Fleet
          </a>
        </nav>
        <div className="topbar-right">
          <ThemeToggle />
        </div>
      </header>

      <main className="main">
        {urlState.page === "fleet" ? (
          <FleetPage state={urlState} onPatch={patch} cfg={cfg} fixtureOverride={fixtureOverride} />
        ) : (
          <ClusterOrFleet state={urlState} onPatch={patch} />
        )}
      </main>
    </div>
  );
}

/** A plain-click handler that keeps modifier-click and middle-click semantics
 *  (open in new tab) intact — a router that swallows those is a regression. */
function navTo(patch: (p: Partial<UrlState>) => void, to: Partial<UrlState>) {
  return (e: MouseEvent<HTMLAnchorElement>): void => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    patch(to);
  };
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
