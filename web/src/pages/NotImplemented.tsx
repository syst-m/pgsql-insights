/* The honest placeholder for the twelve R10.3 pages that don't exist yet.
 *
 * R10.3 names fourteen pages; Phase 1b ships two. The other twelve still have to
 * answer their URL, because the alternative is worse: an SPA route with no
 * component renders a blank page, and a blank page at a URL that the spec says
 * exists is how a requirement ends up looking implemented before it is.
 *
 * So the page says who is building it and from which endpoint. `Route` carries
 * the requirement id, the §9 phase, and the R8.2 path — all three are already
 * asserted in routes.test.ts, so the placeholder cannot advertise a phase that
 * the table disagrees with, and when Phase 6b replaces this component with the
 * real Queries page, the route entry it replaces it with is the one that was
 * already there.
 */

import { navTo } from "../components/Nav";
import type { Route } from "../routes";
import type { UrlState } from "../state/urlState";

export interface NotImplementedProps {
  route: Route;
  state: UrlState;
  onPatch: (patch: Partial<UrlState>) => void;
}

export function NotImplemented({ route, state, onPatch }: NotImplementedProps) {
  return (
    <section className="state-panel not-implemented" data-page={route.page} role="status">
      <h1 className="state-title">{route.title}</h1>
      <p className="state-detail">
        Not built yet. <code>{route.requirement}</code> assigns this page to{" "}
        <strong>Phase {route.phase}</strong> of the plan in §9; the route exists now so the URL,
        the nav and the visual suite all point at one thing from here on.
      </p>
      <dl className="not-implemented-meta">
        <dt>Requirement</dt>
        <dd>{route.requirement}</dd>
        <dt>Ships in</dt>
        <dd>§9 Phase {route.phase}</dd>
        <dt>Endpoint</dt>
        <dd>
          <code>{route.endpoint}</code>
        </dd>
        {route.scope === "cluster" && state.cluster !== null && (
          <>
            <dt>Cluster</dt>
            <dd>
              <code>{state.cluster}</code>
            </dd>
          </>
        )}
      </dl>
      <a className="btn" href="/" onClick={navTo(onPatch, { page: "fleet", cluster: null })}>
        Back to Fleet
      </a>
    </section>
  );
}
