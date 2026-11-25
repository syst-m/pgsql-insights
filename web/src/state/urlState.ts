/* URL is the single source of truth for what's on screen (R10.16).
 *
 * R10.16 lists the state that must be encoded in the URL: page, cluster,
 * selected row, tab, sort, direction, filters and time range. Encoding them
 * rather than keeping them in component state buys three things at once:
 * a link you can paste into Slack, a working back button, and a state the
 * R16.7 visual suite can address by URL instead of by driving the UI.
 *
 * The URL shape:
 *
 *   /                                  fleet, default sort
 *   /?sort=health&dir=desc             fleet, sorted
 *   /?ns=freeidp&sort=name             fleet, filtered to one namespace
 *   /?q=proxy                        fleet, text filter
 *   /?range=7d                       fleet, time range (R10.4's metric window)
 *   /c/freeidp/freeidp-pg              cluster detail
 *   /c/freeidp/freeidp-pg?tab=replication   cluster detail, one tab
 *   /c/.../...?row=freeidp-pg-1        cluster detail, one instance row selected
 *
 * Route lives in the path, everything else in the query string. That split is
 * deliberate: the Go server serves `/` and `/c/**` off one handler (R10.1),
 * and a path-based route survives a server that can't rewrite arbitrary
 * query strings.
 */

import type { ClusterHealth } from "../api/types";
import { HEALTH_ORDER } from "../api/health";
import { buildPath, matchPath, requiresCluster, type Page } from "../routes";

/** R10.3's page ids. The table in `../routes.ts` is the single declaration;
 *  this re-export keeps every existing `import { Page } from "./state/urlState"`
 *  working while the URL module stops keeping its own two-member copy of a
 *  fourteen-member list. */
export type { Page };

/** Columns the Fleet table sorts on. Mirrors ClusterTable's column list. */
export type SortKey =
  | "name"
  | "namespace"
  | "health"
  | "instances"
  | "pgMajor"
  | "tps"
  | "p95LatencyMs"
  | "connectionsPct"
  | "diskPct"
  | "observedAt";

export type SortDir = "asc" | "desc";

/** R10.15's time-range vocabulary: the set and the order are on `TIME_RANGES`
 *  below, and this type is derived from that array. See the note there. */
export type TimeRange = (typeof TIME_RANGES)[number];

/** R10.12's cluster-detail tabs. */
export type ClusterTab = "overview" | "instances" | "replication" | "storage" | "config" | "findings";

export interface UrlState {
  page: Page;
  /** "namespace/name" — set iff `requiresCluster(page)`, i.e. every page but `fleet`. */
  cluster: string | null;
  /** Selected table row (R10.16). On fleet this is the focused cluster id. */
  row: string | null;
  tab: ClusterTab;
  sort: SortKey;
  dir: SortDir;
  /** Namespace filter; empty means all namespaces. */
  ns: string[];
  /** Free-text filter over name/namespace. */
  q: string;
  /** Health filter; empty means all. */
  health: ClusterHealth[];
  range: TimeRange;
  /** The raw `?fixture=` value ("" when absent). Carried through navigation so
   *  clicking a sort header cannot silently switch the app back to the default
   *  data source; `api/client.ts` interprets it. */
  fixtureFlag: string;
  /** The raw `?delay=` value ("" when absent), carried for the same reason. */
  delayFlag: string;
}

export const DEFAULT_STATE: UrlState = {
  page: "fleet",
  cluster: null,
  row: null,
  tab: "overview",
  sort: "health",
  dir: "desc",
  ns: [],
  q: "",
  health: [],
  /** `24h`'s successor — the same one-day window, spelled the way the API spells
   *  it (A3's ruling on R10.15; `24h` was a value `series.go` rejects). */
  range: "1d",
  fixtureFlag: "",
  delayFlag: "",
};

const SORT_KEYS: readonly SortKey[] = [
  "name",
  "namespace",
  "health",
  "instances",
  "pgMajor",
  "tps",
  "p95LatencyMs",
  "connectionsPct",
  "diskPct",
  "observedAt",
];

const TABS: readonly ClusterTab[] = [
  "overview",
  "instances",
  "replication",
  "storage",
  "config",
  "findings",
];

/** The ranges the picker offers, in order — the single declaration that the
 *  `TimeRange` type above is derived from, and that `Fleet.tsx` and
 *  `Queries.tsx` now import instead of each restating.
 *
 *  A3's ruling on R10.15 (requirements.md, asked by A4): the cell names six
 *  values, this file offered four, and one of the four — `24h` — is a minted
 *  token the API has no entry for, so `parseSeriesQuery` fails on it and
 *  `internal/api/series.go:289` answers 400 `InvalidRequest`. In the ruling's
 *  words, "a value the UI can put in the URL that the UI's own request builder
 *  cannot send".
 *
 *  The ruling's phasing is what this array implements: `24h` → `1d` and the `6h`
 *  addition ship now; `custom`, the sixth, is welded to the cell's
 *  auto-refresh-disable rule and ships with it, not before it. So this is five
 *  of the six, and landing the sixth later is one string here and no type change.
 *
 *  It used to be a `type TimeRange` at :53 *and* a `const RANGES` here — two
 *  copies of one list, which is how a list drifts from its spec while both copies
 *  still compile. `../routes.ts`'s page table gets the same treatment for `Page`:
 *  one declaration, everyone else imports it. */
export const TIME_RANGES = ["1h", "6h", "1d", "7d", "30d"] as const;

/** Unknown values from a hand-edited URL fall back to the default rather than
 *  throwing — a bad link should open the app, not blank it. */
function pick<T extends string>(raw: string | null, allowed: readonly T[], fallback: T): T {
  if (raw === null) return fallback;
  return (allowed as readonly string[]).includes(raw) ? (raw as T) : fallback;
}

/** Decode a location string ("path" + "?query") into state.
 *
 *  The path half comes from `matchPath` (routes.ts), the query half from here.
 *  Previously the path half was a regex here that matched `/c/{ns}/{name}` and
 *  ignored the rest, so `/c/ns/name/queries` parsed as the Overview; now every
 *  route in the table parses, and the two branches this function used to carry
 *  (cluster vs everything else) collapse into one. */
export function parseUrl(loc: string): UrlState {
  const [rawPath = "/", rawQuery = ""] = splitLocation(loc);
  const params = new URLSearchParams(rawQuery);
  const match = matchPath(rawPath);
  const page: Page = match?.page ?? "fleet";

  return {
    ...DEFAULT_STATE,
    page,
    cluster: match?.cluster ?? null,
    // Query Detail carries its row in the path as the fingerprint; every other
    // page takes it from `?row=` (R10.16).
    row: match?.row ?? params.get("row"),
    // `tab` is Cluster Overview's section list (R10.5) — on any other page it
    // stays at the default so a stale `?tab=storage` cannot follow the user into
    // a page that has no tabs.
    tab: page === "cluster" ? pick(params.get("tab"), TABS, "overview") : "overview",
    sort: pick(params.get("sort"), SORT_KEYS, "health"),
    dir: pick(params.get("dir"), ["asc", "desc"] as const, "desc"),
    ns: splitList(params.get("ns")),
    q: params.get("q") ?? "",
    health: parseHealthList(params.get("health")),
    range: pick(params.get("range"), TIME_RANGES, "1d"),
    fixtureFlag: params.get("fixture") ?? "",
    delayFlag: params.get("delay") ?? "",
  };
}

function splitLocation(loc: string): [string, string] {
  const i = loc.indexOf("?");
  if (i === -1) return [loc, ""];
  return [loc.slice(0, i), loc.slice(i)];
}

function splitList(raw: string | null): string[] {
  if (raw === null || raw === "") return [];
  return raw.split(",").map((s) => s.trim()).filter((s) => s !== "");
}

function parseHealthList(raw: string | null): ClusterHealth[] {
  const wanted = splitList(raw).filter((s): s is ClusterHealth =>
    (HEALTH_ORDER as readonly string[]).includes(s),
  );
  // Keep the canonical order so two URLs that set the same set in different
  // orders produce the same string after a round-trip.
  return HEALTH_ORDER.filter((h) => wanted.includes(h));
}

/** Encode state to a query string, leading `?` included ("" when empty).
 *
 *  Exported separately from `formatUrl` so a caller can rebuild a `Location`'s
 *  search without the path — `App` uses it to rebuild the API client config
 *  after every navigation. */
export function formatQuery(s: UrlState): string {
  const params = new URLSearchParams();

  // `tab` is Cluster Overview's section list (R10.5), so it belongs in the
  // query only on that page — every other page keeps it at the default.
  if (s.page === "cluster" && s.tab !== "overview") params.set("tab", s.tab);
  // `row` is the selected table row (R10.16). Query Detail is the one page that
  // carries it in the *path* instead, as the fingerprint; emitting it in both
  // places would put one value in two spots that could then disagree.
  if (s.row !== null && s.row !== "" && s.page !== "query-detail") params.set("row", s.row);

  if (s.sort !== DEFAULT_STATE.sort) params.set("sort", s.sort);
  if (s.dir !== DEFAULT_STATE.dir || s.sort !== DEFAULT_STATE.sort) params.set("dir", s.dir);
  if (s.ns.length > 0) params.set("ns", s.ns.join(","));
  if (s.q !== "") params.set("q", s.q);
  if (s.health.length > 0) params.set("health", s.health.join(","));
  if (s.range !== DEFAULT_STATE.range) params.set("range", s.range);
  if (s.fixtureFlag !== "") params.set("fixture", s.fixtureFlag);
  if (s.delayFlag !== "") params.set("delay", s.delayFlag);

  const qs = params.toString();
  return qs === "" ? "" : `?${qs}`;
}

/** Encode state back to a location string. Defaults are omitted so the common
 *  case stays short (`/`) and every state has exactly one canonical string. */
export function formatUrl(s: UrlState): string {
  // `buildPath` is the counterpart of the `matchPath` call in `parseUrl` — one
  // table, one pattern per page, so the two directions cannot drift.
  const path = buildPath(s.page, s.cluster, s.row);
  const qs = formatQuery(s);
  return qs === "" ? path : `${path}${qs}`;
}

/** Round-trip guarantee the tests assert: formatting then parsing is identity. */
export function withState(current: UrlState, patch: Partial<UrlState>): UrlState {
  const next: UrlState = { ...current, ...patch };
  // Invariants the type allows but the UI never wants:
  // 1. `tab` is Cluster Overview's own section list; on any other page it has
  //    to sit at the default so navigating away from an Overview opened on
  //    `?tab=storage` doesn't carry a tab the new page cannot show.
  if (next.page !== "cluster") {
    next.tab = "overview";
  }
  // 2. A cluster-scoped page with no cluster has nothing to render — fall back
  //    to the fleet rather than emit a URL the parser would reject. Generalised
  //    from `page === "cluster"` to `requiresCluster(page)` — every page but
  //    `fleet` (R5.9's per-cluster timeline is what makes `events` one).
  if (requiresCluster(next.page) && next.cluster === null) {
    next.page = "fleet";
  }
  // 3. Query Detail *is* a selected row. Without a fingerprint there is no such
  //    page to show, so it degrades to the Queries list it was opened from —
  //    which is what `buildPath` would have linked to anyway.
  if (next.page === "query-detail" && (next.row === null || next.row === "")) {
    next.page = "queries";
  }
  return next;
}

/** The history API, narrowed so tests can pass a fake.
 *
 * This mirrors `window.history` (TS's lib.dom declares pushState/replaceState
 * on `History`, not on `Window`), so callers pass `window.history` — passing
 * `window` itself does not typecheck. */
export interface HistoryLike {
  pushState(data: unknown, unused: string, url?: string | URL | null): void;
  replaceState(data: unknown, unused: string, url?: string | URL | null): void;
}

/** The location API, narrowed the same way; matches `window.location`. */
export interface LocationLike {
  readonly pathname: string;
  readonly search: string;
}

/** Write state to the URL. `replace` is for the first paint and for changes
 *  that should not add a back step (e.g. a hover-driven row focus). */
export function pushState(history: HistoryLike, s: UrlState, replace = false): void {
  const url = formatUrl(s);
  if (replace) history.replaceState(s, "", url);
  else history.pushState(s, "", url);
}

/** Read a location into state. */
export function readState(location: LocationLike): UrlState {
  return parseUrl(`${location.pathname}${location.search}`);
}

/** Subscribe to back/forward. Returns the unsubscribe function. */
export function subscribeToHistory(
  target: { addEventListener(type: "popstate", fn: () => void): void; removeEventListener(type: "popstate", fn: () => void): void },
  read: () => UrlState,
  set: (s: UrlState) => void,
): () => void {
  const onPop = (): void => set(read());
  target.addEventListener("popstate", onPop);
  return () => target.removeEventListener("popstate", onPop);
}
