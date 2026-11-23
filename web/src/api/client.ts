/* Where the pages get their data from.
 *
 * Four routes, two sources, one interface (R1.5's contract):
 *
 *   http     — GET /api/v1/… on the same origin as the page. Because the Go
 *              binary serves the SPA itself (R10.1), same-origin is the normal
 *              case and no CORS or base-URL config is needed. The four routes
 *              are the four the contract marks `x-status: implemented`:
 *              `/clusters`, `/insights/queries`, `/insights/queries/{fp}` and
 *              `/insights/queries/series` — one fetcher each, and every one of
 *              them takes its params from the route's own `parameters:` list
 *              rather than from a shape invented here.
 *   fixture  — web/src/api/fixtures.ts, which is itself generated from
 *              testdata/api/*.json (R16.8), so the two sources serve the
 *              same bytes through the same parser.
 *
 * The switch is a URL flag (`?fixture=0`) or a build-time env
 * (`VITE_PGSI_API_BASE`), never a code change — so flipping to the live client
 * is a rebuild, not a rewrite.
 *
 * The http path does not cast the response. It parses it (see envelope.ts):
 * `await res.json() as FleetResponse` was how this file shipped an empty Fleet
 * page against a working API, because the cast accepted a body with no
 * `clusters` key and `?? []` turned that into a plausible-looking empty state.
 *
 * It also reads the body as text before parsing it, so a non-JSON error page is
 * an `ApiError` with a status rather than a `SyntaxError` from the parse — see
 * `getJSON`.
 */

import {
  FIXTURE_EMPTY,
  FIXTURE_FLEET,
  FIXTURE_QUERY_DETAIL,
  FIXTURE_QUERY_SERIES,
  FIXTURE_QUERIES,
  FIXTURE_PARTIAL,
  FIXTURE_TRUNCATED_PARTIAL,
  FIXTURE_UNAVAILABLE,
} from "./fixtures";
import {
  ContractError,
  parseEnvelope,
  parseErrorBody,
  parseQueryDetailView,
  parseQuerySeries,
  parseQueryView,
  toFleetResponse,
  toQueriesResponse,
} from "./envelope";
import type {
  ErrorCode,
  FleetResponse,
  QueryDetailView,
  QueryResponse,
  QuerySeriesEnvelope,
} from "./types";

export type Source = "http" | "fixture";

/** R10.12's named fixtures, addressable from the URL.
 *
 * `?fixture=partial` renders the partial state without a live API, which is
 * what makes every named state reachable by a URL — the requirement's own
 * wording ("reachable by a stable URL") and the handle R16.7's visual suite
 * uses to screenshot each one. `1`/`true` is the plain fleet fixture. */
export type FixtureName = "fleet" | "empty" | "partial" | "truncated-partial" | "unavailable";

export const FIXTURES: Record<FixtureName, FleetResponse> = {
  fleet: FIXTURE_FLEET,
  empty: FIXTURE_EMPTY,
  partial: FIXTURE_PARTIAL,
  "truncated-partial": FIXTURE_TRUNCATED_PARTIAL,
  unavailable: FIXTURE_UNAVAILABLE,
};

const FIXTURE_NAMES: readonly string[] = [
  "fleet",
  "empty",
  "partial",
  "truncated-partial",
  "unavailable",
];

export interface ClientConfig {
  source: Source;
  /** Base URL for the http source; "" means same-origin. */
  apiBase: string;
  /** Which fixture the fixture source serves; unused by the http source. */
  fixture: FixtureName;
  /** Artificial latency on the fixture source, ms. `?delay=5000` holds the
   *  loading state open long enough to screenshot it (R10.12 names loading as
   *  a state a page must render, and R16.7 needs to reach it). */
  delayMs: number;
}

const DEFAULT_DELAY_MS = 60;
const MAX_DELAY_MS = 60_000;

function parseDelay(raw: string | undefined): number {
  const n = Number(raw);
  if (raw === undefined || raw === "" || !Number.isFinite(n) || n < 0) return DEFAULT_DELAY_MS;
  return Math.min(Math.floor(n), MAX_DELAY_MS);
}

/** Resolve the source from the URL, then the build env, then the default.
 *
 * `?fixture=1` forces the fixture; `?fixture=0` forces the HTTP client;
 * `?fixture=empty|partial|unavailable` picks a named fixture; `?delay=5000`
 * adds latency. The query params win over the env vars because they are the
 * per-load override the e2e suite and a reviewer use without rebuilding.
 */
export function resolveConfig(search: string, env: Record<string, string | undefined>): ClientConfig {
  const params = new URLSearchParams(search);
  const flag = params.get("fixture");
  const base = env.VITE_PGSI_API_BASE ?? "";
  const delayMs = parseDelay(params.get("delay") ?? env.VITE_PGSI_DELAY);

  if (flag === "0" || flag === "false") return { source: "http", apiBase: base, fixture: "fleet", delayMs };
  if (flag !== null && FIXTURE_NAMES.includes(flag)) {
    return { source: "fixture", apiBase: base, fixture: flag as FixtureName, delayMs };
  }
  if (flag === "1" || flag === "true") return { source: "fixture", apiBase: base, fixture: "fleet", delayMs };
  // Default: the fixture. The Phase 1a endpoint is merged now, so this is a
  // choice rather than a block — the fixture is the default because it is the
  // source the R16.7 visual suite pins, and `?fixture=0` on any deployment
  // reaches the live API without a rebuild (IMPLEMENTATION.md §3.3).
  const fromEnv = env.VITE_PGSI_FIXTURE;
  if (fromEnv === "0") return { source: "http", apiBase: base, fixture: "fleet", delayMs };
  if (fromEnv !== undefined && FIXTURE_NAMES.includes(fromEnv)) {
    return { source: "fixture", apiBase: base, fixture: fromEnv as FixtureName, delayMs };
  }
  return { source: "fixture", apiBase: base, fixture: "fleet", delayMs };
}

export interface FetchOptions {
  /** Aborts an in-flight request when the page unmounts or a new one starts. */
  signal?: AbortSignal;
}

/** A tick of latency on the fixture source, so a fixture-backed page resolves on
 *  roughly the schedule the live API sets and the loading state is exercised the
 *  same way in dev as it is against the cluster. `?delay=5000` holds it open long
 *  enough to screenshot (R10.12 names loading as a state a page must render, and
 *  R16.7 waits on it). The http paths skip this — the network is their latency.
 *
 *  Extracted from `fetchFleet` when the other three fetchers landed, so the four
 *  of them share one implementation of "the fixture arm" instead of each
 *  re-deriving it. */
async function delay(cfg: ClientConfig): Promise<void> {
  await new Promise((r) => setTimeout(r, cfg.delayMs));
}

/** `?a=b&c=d` from a param record, dropping the keys the caller left undefined.
 *
 *  A `Record<string, …>` argument rather than a per-route union: one helper
 *  serves four routes, and a union would need a new member every time a route
 *  gets a fetcher — the growing-union pattern `envelope.ts` rejects for the same
 *  reason. The param types below are `type` aliases, not interfaces, because TS
 *  gives a type alias an implicit index signature and an interface not, and an
 *  interface here would not be assignable to the Record.
 *
 *  Dropping undefined matters: `?db=` asks the server about the empty-string
 *  database, which is a different question from asking about no database. Every
 *  R8.4 param is optional, so an unset one is absent from the URL, not empty.
 *
 *  The record's key order *is* the query string's order — `URLSearchParams`
 *  preserves insertion order — so each caller controls the spelling by how it
 *  builds the record, and the fetchers below put a route's required param first
 *  rather than letting a spread decide it. */
function search(params: Record<string, string | number | undefined>): string {
  const usp = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) usp.set(key, String(value));
  }
  const q = usp.toString();
  return q === "" ? "" : `?${q}`;
}

/** `JSON.parse` reporting failure as `undefined` instead of throwing.
 *
 *  `res.json()` — the call this replaced — threw, and the throw was the bug: a
 *  caller that never receives a value cannot tell "the proxy answered HTML" from
 *  "the request never completed", and the `ApiError` shape every page's error
 *  panel is built on depended on the *body being parseable*, which is a property
 *  of the body and not of the contract. An empty body is `undefined` too:
 *  `JSON.parse("")` throws, and every route here answers 200 with a body, so an
 *  empty one is not a value either. */
function parseJson(text: string): unknown {
  if (text === "") return undefined;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return undefined;
  }
}

/** The first line of a body, cut to 80 characters. An error message that quotes
 *  a body has to quote it without pasting a whole HTML page into the error panel. */
function preview(text: string): string {
  const first = text.split("\n", 1)[0] ?? "";
  return first.length > 80 ? `${first.slice(0, 80)}…` : first;
}

/** One GET and one failure path, shared by all four routes.
 *
 *  The http side used to be inline in `fetchFleet`; four fetchers each repeating
 *  the trailing-slash strip, the `accept` header, the body read and the
 *  two-step `ok`/`ApiError` dance is four places to get R8.5's error handling
 *  subtly wrong. The order is the original one — read the body, *then* branch on
 *  `ok` — because the message needs the body to recover R8.5's `code`, and a
 *  404's body is the only place that says whether it was `NotFound` or
 *  `NotReady`.
 *
 *  The body is read as text and then parsed (see `parseJson`), so an HTML 502
 *  from a proxy is an `ApiError` carrying 502 rather than a `SyntaxError`. Both
 *  arms then have to say what an unparseable body means for *them*: a failed
 *  request keeps its status line, a successful one is a contract break. */
async function getJSON(cfg: ClientConfig, pathWithQuery: string, opts: FetchOptions = {}): Promise<unknown> {
  const url = `${cfg.apiBase.replace(/\/$/, "")}${pathWithQuery}`;
  const res = await fetch(url, { signal: opts.signal, headers: { accept: "application/json" } });
  const text = await res.text();
  const json = parseJson(text);

  if (!res.ok) {
    throw new ApiError(res.status, describeFailure(res.status, res.statusText, json), parseErrorBody(json)?.code);
  }
  // A 200 whose body is not JSON is the same family of break as a 200 that is
  // JSON but not an envelope. `parseEnvelope` throws ContractError on that one,
  // so this throws it on this one too rather than handing the caller an
  // `undefined` to cast — which is the original bug wearing a different hat.
  if (json === undefined) {
    throw new ContractError(`GET ${pathWithQuery} → 200 body is not JSON (${preview(text)})`);
  }
  return json;
}

/** Fetch the fleet snapshot from the configured source. */
export async function fetchFleet(cfg: ClientConfig, opts: FetchOptions = {}): Promise<FleetResponse> {
  if (cfg.source === "fixture") {
    await delay(cfg);
    return FIXTURES[cfg.fixture];
  }

  // Throws ContractError if the body is not an Envelope — the check that the
  // old cast skipped. No `?? []` anywhere on this path: an empty fleet has to
  // be an empty `data` array, never a missing key.
  return toFleetResponse(parseEnvelope(await getJSON(cfg, "/api/v1/clusters", opts)));
}

/* ── the query routes ───────────────────────────────────────────────────────
 *
 * Three fetchers for the three routes `api/openapi.yaml` marks implemented
 * besides `/clusters`. Each one's parameters come from that route's own
 * `parameters:` list — not from a shape invented here — for the same reason
 * `fixtures.ts` imports the JSON instead of retyping it: a second copy of a
 * contract is the bug, and a fetcher that accepts a parameter the route does not
 * take is a second copy of the request half of it. */

/** `GET /api/v1/insights/queries` — R3.2's table, R9.5's top-N page.
 *
 *  R8.4's `Namespace/Cluster/DB/From/To/Limit/Sort/Cursor` set, every one of
 *  them optional. `limit` is passed through, never clamped: R8.10 fixes the
 *  default at 50 and the maximum at 500 and rules that above the maximum the
 *  answer is `InvalidRequest`, not a silent clamp — "a client that asks for 5000
 *  learns about it instead of getting 500". Clamping here would replace the
 *  server's answer with the client's opinion, which is the thing R8.10 forbids.
 *
 *  `sort` is a plain string here, not an enum: the route's `Sort` component is
 *  `type: string` with no `enum:` (the sort keys are R8.10's per-endpoint list,
 *  and the stable secondary key — `queryid` — is implicit, not a parameter), so
 *  an enum in the client would be a stricter contract than the published one. */
export type QueryListParams = {
  namespace?: string;
  cluster?: string;
  db?: string;
  from?: string;
  to?: string;
  limit?: number;
  sort?: string;
  cursor?: string;
};

export async function fetchQueries(
  cfg: ClientConfig,
  params: QueryListParams = {},
  opts: FetchOptions = {},
): Promise<QueryResponse> {
  if (cfg.source === "fixture") {
    // The one query fixture the contract has, and the params are ignored on this
    // arm exactly as `fetchFleet` ignores them when serving a fleet fixture:
    // R10.12's named states are fleet-side, so there is no named set to choose
    // between here. `toQueriesResponse` is the same builder the route test uses,
    // so the fixture arm and the http arm return the identical shape.
    await delay(cfg);
    return toQueriesResponse(FIXTURE_QUERIES);
  }
  const json = await getJSON(cfg, `/api/v1/insights/queries${search(params)}`, opts);
  return toQueriesResponse(parseEnvelope(json, parseQueryView));
}

/** `GET /api/v1/insights/queries/{fingerprint}` — R10.7's payload.
 *
 *  All three fields are required, and the route's own description is the reason:
 *  `namespace` and `cluster` are required here and optional on the list, because
 *  R3.4 makes a fingerprint canonical *within* a cluster, so the pair is what
 *  turns `{fingerprint}` into a row. The list uses them as filters; this route
 *  uses them as half of the key. That is also why the fingerprint is a path
 *  segment and gets `encodeURIComponent` — the same encoding `routes.ts`'s
 *  `buildPath` applies to the URL side, so a fingerprint with a `%` in it
 *  addresses the same row from both.
 *
 *  The fixture arm answers every fingerprint with `FIXTURE_QUERY_DETAIL`, the
 *  only detail fixture there is; the http arm is the one that can answer 404,
 *  and per the route's description it answers 404 `NotFound` for every
 *  fingerprint until Phase 3's store is assigned to `Config.QueryDetail`. */
export type QueryDetailParams = {
  fingerprint: string;
  namespace: string;
  cluster: string;
};

export async function fetchQueryDetail(
  cfg: ClientConfig,
  params: QueryDetailParams,
  opts: FetchOptions = {},
): Promise<QueryDetailView> {
  if (cfg.source === "fixture") {
    await delay(cfg);
    return FIXTURE_QUERY_DETAIL;
  }
  // The fingerprint is the path segment; the other two are the query string.
  const { fingerprint, ...scope } = params;
  const json = await getJSON(
    cfg,
    `/api/v1/insights/queries/${encodeURIComponent(fingerprint)}${search(scope)}`,
    opts,
  );
  // Not `parseEnvelope`: this route's 200 is a bare object, so the check is
  // `parseQueryDetailView`'s own — which is why that function exists rather
  // than this call being the one place in the file that casts a body.
  return parseQueryDetailView(json);
}

/** R10.15's range vocabulary as the API spells it.
 *
 *  Mirrors `internal/api/series.go:63-68`'s constants and the openapi `Range`
 *  enum (`[1h, 6h, 1d, 7d, 30d, custom]`, default `1h`).
 *
 *  Deliberately not `TimeRange` from `state/urlState.ts`, which lists
 *  `1h | 24h | 7d | 30d` — a fourth spelling that matches neither R10.15's five
 *  presets nor this enum (it has `24h` where both say `6h` and `1d`, and no
 *  `custom`). The fetcher takes the API's word list because the request is the
 *  API's; the URL's vocabulary is a UI question, and `urlState.ts` owns
 *  settling it. */
export type SeriesRange = "1h" | "6h" | "1d" | "7d" | "30d" | "custom";

/** `GET /api/v1/insights/queries/series` — R8.11's batch.
 *
 *  `fingerprints` is required — an empty list is `InvalidRequest`, not an empty
 *  page — and capped at `series.go:56`'s `MaxBatchFingerprints` (50). Above the
 *  cap the server answers `InvalidRequest` rather than truncating, so this joins
 *  the array and does not slice it to 50: a client that sends 80 and gets 50
 *  cannot tell "30 of them have no data" from "you were cut off", which is
 *  R8.10's reason for forbidding the clamp on the batch parameter too.
 *
 *  `range` is omitted when unset so the server's `1h` default applies rather
 *  than an empty string. On a preset the `from`/`to` pair is ignored rather than
 *  rejected; with `range=custom` both bounds become required, which the server
 *  enforces and this type cannot say — encoding "required when custom" would
 *  mean a discriminated union over one field's two states, and the parser in
 *  `series.go` is where that rule is actually checked. */
export type QuerySeriesParams = {
  fingerprints: readonly string[];
  range?: SeriesRange;
  namespace?: string;
  cluster?: string;
  db?: string;
  from?: string;
  to?: string;
};

/** R8.11's batched series fetch — the one call that fills R10.6's sparkline
 *  column and the one R10.7's detail chart reuses with a batch of one.
 *
 *  Returns `QuerySeriesEnvelope` — `dto.QuerySeriesEnvelope` verbatim, the
 *  alias over `Envelope<QuerySeries>` (types.ts:707), not a re-typed copy of
 *  it — because the route serves the generic envelope and the alias is its
 *  published name on both sides. `points` is `[]` and never null (R8.3):
 *  `parseEnvelope(json, parseQuerySeries)` is what enforces it, since
 *  `parseQuerySeries` requires the key and requires it to be an array, so a
 *  `"points": null` from the server throws here instead of plotting a hole the
 *  UI cannot tell from a real zero. */
export async function fetchQuerySeries(
  cfg: ClientConfig,
  params: QuerySeriesParams,
  opts: FetchOptions = {},
): Promise<QuerySeriesEnvelope> {
  if (cfg.source === "fixture") {
    await delay(cfg);
    return FIXTURE_QUERY_SERIES;
  }
  const { fingerprints, ...rest } = params;
  // `fingerprints` first, then the optionals in the order the type declares them:
  // `search` emits the record's own key order, and `{ ...rest, fingerprints }`
  // would put `range` ahead of the one param the route requires.
  const json = await getJSON(
    cfg,
    `/api/v1/insights/queries/series${search({ fingerprints: fingerprints.join(","), ...rest })}`,
    opts,
  );
  return parseEnvelope(json, parseQuerySeries);
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** R8.5's machine-readable code, when the body carried one. Present for
     *  every well-formed error from the Go side; absent when a proxy or a
     *  non-pgsi server answered. */
    readonly code?: ErrorCode,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** One line for the error panel. The R8.5 `code` is what the panel needs to
 *  distinguish `NotReady` ("discovery has not synced yet", retry) from
 *  `Forbidden` ("the ServiceAccount cannot read these namespaces", fix RBAC) —
 *  both are a 4xx with a message, and only one of them is fixed by waiting. */
function describeFailure(status: number, statusText: string, json: unknown): string {
  const detail = parseErrorBody(json);
  if (detail === null) return `GET → ${status} ${statusText}`;
  const extra = detail.details.length > 0 ? ` — ${detail.details.join("; ")}` : "";
  return `GET → ${status} ${detail.code}: ${detail.message}${extra}`;
}
