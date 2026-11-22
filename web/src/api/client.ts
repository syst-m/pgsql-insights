/* Where the Fleet page gets its data from.
 *
 * Two sources, one interface (R1.5's contract):
 *
 *   http     — GET /api/v1/clusters on the same origin as the page. Because
 *              the Go binary serves the SPA itself (R10.1), same-origin is the
 *              normal case and no CORS or base-URL config is needed.
 *   fixture  — web/src/api/fixtures.ts, which is itself generated from
 *              testdata/api/clusters.json (R16.8), so the two sources serve the
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
 */

import {
  FIXTURE_EMPTY,
  FIXTURE_FLEET,
  FIXTURE_PARTIAL,
  FIXTURE_TRUNCATED_PARTIAL,
  FIXTURE_UNAVAILABLE,
} from "./fixtures";
import { parseEnvelope, parseErrorBody, toFleetResponse } from "./envelope";
import type { ErrorCode, FleetResponse } from "./types";

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

/** Fetch the fleet snapshot from the configured source. */
export async function fetchFleet(cfg: ClientConfig, opts: FetchOptions = {}): Promise<FleetResponse> {
  if (cfg.source === "fixture") {
    // A tick of latency so the loading state is exercised the same way in dev
    // as it is against the live API; the e2e suite waits on it.
    await new Promise((r) => setTimeout(r, cfg.delayMs));
    return FIXTURES[cfg.fixture];
  }

  const url = `${cfg.apiBase.replace(/\/$/, "")}/api/v1/clusters`;
  const res = await fetch(url, { signal: opts.signal, headers: { accept: "application/json" } });
  const json: unknown = await res.json();

  if (!res.ok) {
    throw new ApiError(res.status, describeFailure(res.status, res.statusText, json), parseErrorBody(json)?.code);
  }
  // Throws ContractError if the body is not an Envelope — the check that the
  // old cast skipped. No `?? []` anywhere on this path: an empty fleet has to
  // be an empty `data` array, never a missing key.
  return toFleetResponse(parseEnvelope(json));
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
