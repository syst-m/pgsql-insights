/* R10.3's page set, as one table (and with it, R10.16's URL scheme).
 *
 * R10.3 names fourteen pages. The Phase 1b shell shipped two of them — Fleet and
 * Cluster Overview — and the other twelve existed only as a row in a requirements
 * table, so there was no way to link to them, no way to tell a user which ones
 * were coming, and nothing for R16.7 to screenshot when they landed. The shell
 * is the Phase 1b half of R10.3: the routes exist and answer, and every route
 * still without a component renders an honest placeholder naming the phase that
 * will build it.
 *
 * How many that is moves every time a page lands — Phase 6b's work added Queries
 * and Query Detail to the two from 1b — so the count is stated here as a fact
 * about those commits and nowhere as an invariant. `routes.test.tsx` derives the
 * implemented set from `Route.component` rather than restating it, which is the
 * half that stays true; `docs/agents.md` §5 rule 10 is the rule that says a
 * count written in two files needs a third thing asserting they agree.
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
 *
 * **This file is the single registration point for a page.** A page used to
 * need three shell files at once: this table (to flip `status`), `App.tsx`
 * (whose `PageView` was an if-chain on `route.page`, despite its own comment
 * claiming "adding a page is a table edit plus a component — never an edit
 * here"), and `routes.test.tsx` (which hardcoded the implemented set and the
 * planned count). Since Phase 6b moved `pages/**` and `components/**` to A6
 * while the shell stayed here, three shared files per page is three chances for
 * the `docs/agents.md` §5 rule-1 failure — a pathspec commit takes the working
 * tree's content for a path, so two slots in one file ships one of them under
 * the other's message. Now a page is *one* entry here plus *one* file of
 * theirs, and `App.tsx` never changes again.
 */

import type { ComponentType } from "react";

import type { ClientConfig } from "./api/client";
import type { FleetResponse } from "./api/types";
import { ActiveQueriesPage } from "./pages/ActiveQueries";
import { ClusterDetailPage } from "./pages/ClusterDetail";
import { FleetPage } from "./pages/Fleet";
import { QueryDetailPage } from "./pages/QueryDetail";
import { QueriesPage } from "./pages/Queries";
import type { UrlState } from "./state/urlState";

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
 *  serve it" is not pretending.
 *
 *  Derived from `Route.component` by `statusOf`; it is not a field any more. */
export type RouteStatus = "implemented" | "planned";

/** Whether a page exists on its own or needs a selected cluster. */
export type Scope = "fleet" | "cluster";

/** The one props shape every page takes, so the table can mount a page instead
 *  of `App.tsx` switching on it.
 *
 *  Pages that need fewer fields accept and ignore the rest: `ClusterDetailPage`
 *  resolves its own row from `state.cluster` and never fetches, so `cfg` and
 *  `fixtureOverride` are unused there. A per-page props shape is what forced the
 *  if-chain in App.tsx — a lookup needs one type for every entry.
 */
export interface PageProps<T = FleetResponse> {
  state: UrlState;
  onPatch: (patch: Partial<UrlState>) => void;
  cfg: ClientConfig;
  /** The visual suite renders a named fixture directly instead of fetching.
   *
   *  Generic over the page's own payload rather than a union of every payload
   *  (`FleetResponse | QueryResponse | …`): a union has to be widened by hand
   *  every phase, in a file its owner does not own, which is the shared-file
   *  edit `docs/agents.md` §5 rule 1 exists to catch. Defaulted to
   *  `FleetResponse` so every existing `PageProps` annotation — `App.tsx:105`,
   *  `pages/ClusterDetail.tsx:48` and `:56` — means exactly what it means now. */
  fixtureOverride?: T;
}

/** A page that takes `PageProps<T>`; `T` defaults to the fleet payload. */
export type PageComponent<T = FleetResponse> = ComponentType<PageProps<T>>;

/** What the table stores in `Route.component`: *some* page, whatever it
 *  fetches.
 *
 *  `any` for the payload, deliberately. Props are contravariant, so a
 *  `PageComponent<QueryResponse>` is assignable to `PageComponent<FleetResponse>`
 *  only if `FleetResponse` is assignable to `QueryResponse` — it is not, and
 *  neither is any pair of payloads. `PageProps<never>` would accept every page
 *  but would then forbid `App.tsx` from passing the fleet fixture it already
 *  passes. `any` scopes the looseness to the table's view of one field: each
 *  page file still declares its exact props, and the page's own test renders it
 *  with the right payload. */
export type AnyPageComponent = ComponentType<PageProps<any>>;

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
  /** The component that renders this page, or null while it is planned.
   *
   *  This field *is* the status — see `statusOf`. Carrying a separate
   *  `status: "planned"` string next to a null component was two sources of
   *  truth for one fact, and the fact belongs to the page file (A6's), not to
   *  this table (A4's). That duplication is the whole reason A6 had to edit a
   *  file they don't own in order to ship a page they do. */
  component: AnyPageComponent | null;
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
    component: FleetPage,
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
    component: ClusterDetailPage,
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
    component: QueriesPage,
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
    component: QueryDetailPage,
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
    component: ActiveQueriesPage,
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
    component: null,
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
    component: null,
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
    component: null,
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
    component: null,
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
    component: null,
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
    component: null,
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
    component: null,
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
    component: null,
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
    component: null,
    scope: "cluster",
    path: "/c/:ns/:name/events",
    inNav: true,
  },
];

/** Whether a route is implemented, read off its component.
 *
 *  The old table stored this, which meant `routes.test.ts` could only assert it
 *  by hardcoding `["fleet", "cluster"]` and `toHaveLength(12)` — two numbers
 *  that had to be edited every phase, and that a green suite still caught
 *  nothing new, because a hardcoded list only proves the list was edited.
 *  Derived, the test asserts the rule instead of the snapshot, and shipping a
 *  page stops requiring a test edit. */
export function statusOf(route: Route): RouteStatus {
  return route.component === null ? "planned" : "implemented";
}

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
