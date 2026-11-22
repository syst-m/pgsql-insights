/* R10.3's page set, as one table (and with it, R10.16's URL scheme).
 *
 * R10.3 names fourteen pages. Until now the app had two — Fleet and Cluster
 * Overview — and the other twelve existed only as a row in a requirements
 * table, so there was no way to link to them, no way to tell a user which ones
 * were coming, and nothing for R16.7 to screenshot when they landed. The shell
 * is the Phase 1b half of R10.3: the routes exist and answer, and the twelve
 * pages that Phase 5+ builds render an honest placeholder naming the phase that
 * will build them.
 *
 * Putting the table in one file is what makes it testable as a single artifact.
 * `routes.test.ts` asserts the properties that were previously spread across
 * urlState.ts's regex, App.tsx's JSX and the spec: fourteen entries, unique
 * ids, unique paths, exactly the two implemented pages implemented, every
 * cluster-scoped path under `/c/:ns/:name`, every page mapped to the requirement
 * and the §9 phase that owns it, and every path pattern round-tripping through
 * `matchPath`/`buildPath`. A fifteenth page added without a phase, or an
 * implemented page with no component, fails there rather than in review.
 *
 * The path scheme is the existing one, extended rather than replaced: `/` for
 * the fleet, `/c/:ns/:name` for a cluster, and one segment below that for each
 * cluster-scoped page (`/c/freeidp/freeidp-pg/queries`). `?tab=` is untouched —
 * it selects a *section inside* Cluster Overview (R10.5's instances,
 * replication, storage, config, findings), which is a different axis from the
 * page being routed to. Query Detail is the one pattern with a second argument,
 * the query fingerprint, and it reuses `state.row` for it — the field R10.16
 * already uses for "selected table row", which is what a query detail is when
 * opened from the Queries table.
 */

/** The 14 pages R10.3 names. This is the canonical declaration; `state/urlState.ts`
 *  re-exports it so existing imports keep working. */
export type Page =
  | "fleet"
  | "cluster"
  | "queries"
  | "query-detail"
  | "active"
  | "wait-events"
  | "databases"
  | "users"
  | "tags"
  | "client-addresses"
  | "system"
  | "poolers"
  | "recommendations"
  | "events";

/** `implemented` — a component exists and renders real data.
 *  `planned` — the route resolves and renders `NotImplemented`, naming the
 *  phase that will build it. Not `stub`: a stub implies code that pretends to
 *  work, and a page that says "Phase 6b, and this is the endpoint that will
 *  serve it" is not pretending. */
export type RouteStatus = "implemented" | "planned";

/** Whether a page exists on its own or needs a selected cluster. */
export type Scope = "fleet" | "cluster";

export interface Route {
  page: Page;
  /** Nav label and page title: R10.3's own wording, so the nav can be checked
   *  against the requirement by reading it. */
  title: string;
  /** The requirement(s) this page implements. Several for the four rollup
   *  pages, which R10.3 requires on behalf of R3.7/R3.12 and R8.2 rather than
   *  carrying an R10.n of their own. */
  requirement: string;
  /** The R8.2 endpoint that serves it — so the placeholder can name the API a
   *  developer should poke today, and so the endpoint list and the page list
   *  cannot drift apart silently. */
  endpoint: string;
  /** The §9 phase that ships it, written as §9 writes it ("6b", not 6.5). */
  phase: string;
  status: RouteStatus;
  scope: Scope;
  /** Path pattern: `/`, `/c/:ns/:name`, `/c/:ns/:name/queries`, or
   *  `/c/:ns/:name/queries/:fingerprint`. Parsed once into `PATTERNS` and used
   *  by both `matchPath` and `buildPath`, so the two cannot disagree. */
  path: string;
  /** False for Query Detail: it is reached by selecting a row in Queries, not
   *  from the nav, and listing it would offer a link that cannot be followed
   *  without a fingerprint. */
  inNav: boolean;
}

/** The table, in nav order — R10.3's order, with the two entry points first. */
export const ROUTES: readonly Route[] = [
  {
    page: "fleet",
    title: "Fleet",
    requirement: "R10.4",
    endpoint: "GET /api/v1/clusters",
    phase: "1b",
    status: "implemented",
    scope: "fleet",
    path: "/",
    inNav: true,
  },
  {
    page: "cluster",
    title: "Cluster Overview",
    requirement: "R10.5",
    endpoint: "GET /api/v1/clusters/{ns}/{name}",
    phase: "1b",
    status: "implemented",
    scope: "cluster",
    path: "/c/:ns/:name",
    inNav: true,
  },
  {
    page: "queries",
    title: "Queries",
    requirement: "R10.6",
    endpoint: "GET /api/v1/insights/queries",
    phase: "6b",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/queries",
    inNav: true,
  },
  {
    page: "query-detail",
    title: "Query Detail",
    requirement: "R10.7",
    endpoint: "GET /api/v1/insights/queries/{fingerprint}",
    phase: "6b",
    status: "planned",
    scope: "cluster",
    // The fingerprint is `state.row` — R10.16's "selected row", which is what a
    // query detail is when opened from the Queries table.
    path: "/c/:ns/:name/queries/:fingerprint",
    inNav: false,
  },
  {
    page: "active",
    title: "Active Queries",
    requirement: "R10.8",
    endpoint: "GET /api/v1/active-queries",
    phase: "6a",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/active",
    inNav: true,
  },
  {
    page: "wait-events",
    title: "Wait Events",
    requirement: "R10.9",
    endpoint: "GET /api/v1/insights/wait-events",
    phase: "6a",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/wait-events",
    inNav: true,
  },
  {
    page: "databases",
    title: "Databases",
    requirement: "R10.3 · R3.7 · R8.2",
    endpoint: "GET /api/v1/insights/databases",
    phase: "5",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/databases",
    inNav: true,
  },
  {
    page: "users",
    title: "Users",
    requirement: "R10.3 · R3.7 · R8.2",
    endpoint: "GET /api/v1/insights/users",
    phase: "5",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/users",
    inNav: true,
  },
  {
    page: "tags",
    title: "Tags",
    requirement: "R10.3 · R3.12 · R8.2",
    endpoint: "GET /api/v1/insights/tags",
    phase: "5",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/tags",
    inNav: true,
  },
  {
    page: "client-addresses",
    title: "Client Addresses",
    requirement: "R10.3 · R3.7 · R6.4",
    endpoint: "GET /api/v1/insights/client-addresses",
    phase: "5",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/client-addresses",
    inNav: true,
  },
  {
    page: "system",
    title: "System Insights",
    requirement: "R10.10",
    endpoint: "GET /api/v1/insights/system",
    phase: "7",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/system",
    inNav: true,
  },
  {
    page: "poolers",
    title: "Poolers",
    requirement: "R10.11",
    endpoint: "GET /api/v1/poolers/{ns}/{name}",
    phase: "8",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/poolers",
    inNav: true,
  },
  {
    page: "recommendations",
    title: "Recommendations",
    requirement: "R7.2 · R7.4–R7.6",
    endpoint: "GET /api/v1/insights/recommendations",
    phase: "8",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/recommendations",
    inNav: true,
  },
  {
    page: "events",
    title: "Events Timeline",
    requirement: "R5.9",
    endpoint: "GET /api/v1/events",
    phase: "8",
    status: "planned",
    scope: "cluster",
    path: "/c/:ns/:name/events",
    inNav: true,
  },
];

/** Lookup by page id. Typed as a complete Record, so a `Page` member with no
 *  route entry is a compile error rather than an `undefined` at runtime — the
 *  same trick `FIXTURES` uses in api/client.ts. */
export const ROUTE_BY_PAGE: Record<Page, Route> = ROUTES.reduce(
  (acc, r) => {
    acc[r.page] = r;
    return acc;
  },
  {} as Record<Page, Route>,
);

/** The nav for one route.
 *
 *  On the fleet page the nav is just Fleet. Every other page needs a cluster,
 *  and offering twelve links that all mean nothing until a cluster is selected
 *  is worse than offering none — the table row is how you get to a cluster page
 *  from here. On any cluster-scoped route the nav is Fleet (the way back, which
 *  is R10.24's breadcrumb in nav form) plus every cluster-scoped page. Query
 *  Detail is excluded because `inNav` says so, not because the component
 *  special-cases it. */
export function navFor(page: Page): readonly Route[] {
  if (page === "fleet") return ROUTES.filter((r) => r.inNav && r.scope === "fleet");
  return ROUTES.filter((r) => r.inNav && (r.page === "fleet" || r.scope === "cluster"));
}

/** Pages that cannot render without a cluster. `fleet` is the only one that
 *  can; every other R10.3 page is per-cluster (R5.9's "one timeline per
 *  cluster" is what decides `events`). */
export function requiresCluster(page: Page): boolean {
  return ROUTE_BY_PAGE[page].scope === "cluster";
}

/* ── path patterns ───────────────────────────────────────────────────────── */

/** A parsed `Route.path`. Deriving both directions from this — rather than a
 *  regex for parsing and a template for formatting, which is what urlState.ts
 *  used to do — is what makes them agree by construction.
 *
 *  The old regex matched `/c/{ns}/{name}` and silently dropped everything after
 *  it, so `/c/ns/name/queries` rendered the Overview. A route in the table is
 *  now a route that parses, and `routes.test.ts` asserts it for every entry. */
interface Pattern {
  /** The literal segment after `/c/:ns/:name`; null for the cluster root. */
  segment: string | null;
  /** True when the pattern ends in a parameter segment — the fingerprint. */
  param: boolean;
}

/** `/c/:ns/:name/queries/:fingerprint` → `{segment:"queries", param:true}`. */
function parsePattern(path: string): Pattern | null {
  const parts = path.split("/"); // [ "", "c", ":ns", ":name", ... ]
  if (parts[0] !== "" || parts[1] !== "c") return null; // not a cluster pattern
  if (parts.length < 4) return null;
  const rest = parts.slice(4);
  if (rest.length === 0) return { segment: null, param: false };
  const first = rest[0] ?? "";
  if (first.startsWith(":")) return null; // a parameter where a route name belongs
  if (rest.length === 1) return { segment: first, param: false };
  if (rest.length === 2 && (rest[1] ?? "").startsWith(":")) return { segment: first, param: true };
  return null;
}

const PATTERNS: Record<Page, Pattern | null> = ROUTES.reduce(
  (acc, r) => {
    acc[r.page] = r.scope === "fleet" ? { segment: null, param: false } : parsePattern(r.path);
    return acc;
  },
  {} as Record<Page, Pattern | null>,
);

export interface Match {
  page: Page;
  cluster: string | null;
  /** The selected row — the query fingerprint on a detail route, `?row=`
   *  everywhere else (R10.16). */
  row: string | null;
}

/** `decodeURIComponent` that never throws.
 *
 *  A hand-typed or truncated URL can end in a stray `%` (`/c/ns/pa%`), which
 *  makes `decodeURIComponent` throw a URIError. The old urlState.ts regex path
 *  had the same exposure; here it degrades to the raw segment, so a bad link
 *  opens the app instead of blanking it. */
function decode(segment: string | undefined): string {
  if (segment === undefined) return "";
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/** Match a pathname to a route, or null when nothing matches.
 *
 *  A malformed URL falls back rather than throwing — the same rule `parseUrl`
 *  applies to a bad `?sort=` value: a bad link opens the app, it does not blank
 *  the page. */
export function matchPath(pathname: string): Match | null {
  const path = pathname === "" ? "/" : pathname;
  if (path === "/") return { page: "fleet", cluster: null, row: null };
  if (!path.startsWith("/c/")) return null;

  const segments = path.slice("/c/".length).split("/").filter((s) => s !== "");
  if (segments.length < 2) return null;
  const ns = decode(segments[0]);
  const name = decode(segments[1]);
  if (ns === "" || name === "") return null;
  const cluster = `${ns}/${name}`;

  for (const route of ROUTES) {
    if (route.scope !== "cluster") continue;
    const pattern = PATTERNS[route.page];
    if (pattern === null) continue;
    const rest = segments.slice(2);

    if (pattern.param) {
      // Needs exactly `[segment, value]`: `/c/ns/name/queries/0x84f2…`.
      if (rest.length === 2 && rest[0] === pattern.segment) {
        return { page: route.page, cluster, row: decode(rest[1]) };
      }
      continue;
    }
    const matches =
      pattern.segment === null ? rest.length === 0 : rest.length === 1 && rest[0] === pattern.segment;
    if (matches) return { page: route.page, cluster, row: null };
  }
  return null;
}

/** The path half of `formatUrl`.
 *
 *  A cluster-scoped page with no cluster resolves to `/` — the same
 *  normalisation `withState` applies, repeated here so a caller that builds a
 *  path directly cannot emit `/c//` or a path the parser would reject. */
export function buildPath(page: Page, cluster: string | null, row: string | null): string {
  const route = ROUTE_BY_PAGE[page];
  const pattern = PATTERNS[page];
  if (route.scope === "fleet" || pattern === null) return "/";
  if (cluster === null || cluster === "" || !cluster.includes("/")) return "/";

  const [ns = "", name = ""] = cluster.split("/");
  let path = `/c/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`;
  if (pattern.segment !== null) path += `/${pattern.segment}`;
  if (pattern.param) {
    // No fingerprint means there is no detail page to link to; the honest path
    // is its parent, the list it was opened from. `withState` keeps the pair
    // consistent so this is the fallback rather than the normal case.
    if (row === null || row === "") return `${path}/`;
    path += `/${encodeURIComponent(row)}`;
  }
  return path;
}
