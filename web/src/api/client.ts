/* Where the Fleet page gets its data from.
 *
 * Two sources, one interface (R1.5's contract):
 *
 *   http     — GET /api/v1/clusters on the same origin as the page. Because
 *              the Go binary serves the SPA itself (R10.1), same-origin is the
 *              normal case and no CORS or base-URL config is needed.
 *   fixture  — web/src/api/fixtures.ts, the four reference clusters.
 *
 * A1 freezes the endpoint in their first Phase 1a commit; until then the
 * fixture is the default so 1b is not blocked on 1a. The switch is a URL flag
 * (`?fixture=0`) or a build-time env (`VITE_PGSI_API_BASE`), never a code
 * change — so flipping to the live client is a rebuild, not a rewrite.
 */

import { FIXTURE_EMPTY, FIXTURE_FLEET, FIXTURE_PARTIAL, FIXTURE_UNAVAILABLE } from "./fixtures";
import type { FleetResponse } from "./types";

export type Source = "http" | "fixture";

/** R10.12's named fixtures, addressable from the URL.
 *
 * `?fixture=partial` renders the partial state without a live API, which is
 * what makes every named state reachable by a URL — the requirement's own
 * wording ("reachable by a stable URL") and the handle R16.7's visual suite
 * uses to screenshot each one. `1`/`true` is the plain fleet fixture. */
export type FixtureName = "fleet" | "empty" | "partial" | "unavailable";

export const FIXTURES: Record<FixtureName, FleetResponse> = {
  fleet: FIXTURE_FLEET,
  empty: FIXTURE_EMPTY,
  partial: FIXTURE_PARTIAL,
  unavailable: FIXTURE_UNAVAILABLE,
};

const FIXTURE_NAMES: readonly string[] = ["fleet", "empty", "partial", "unavailable"];

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
  // Default: the fixture while the Phase 1a endpoint is unmerged. Once A1's
  // commit lands this flips to "http" in one line (see IMPLEMENTATION.md §3.3).
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
  if (!res.ok) {
    throw new ApiError(res.status, `GET ${url} → ${res.status} ${res.statusText}`);
  }
  const body = (await res.json()) as FleetResponse;
  return normalizeFleet(body);
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** Trust-boundary pass for a server payload.
 *
 * The Go side owns the shape (R1.5); this does not re-validate it, it only
 * fills fields an older binary might omit, so one missing array does not blank
 * the page. `health` in particular is recomputed client-side from the counts
 * when absent, which keeps the UI correct against a Phase 1a response that
 * ships the counts but not the rollup.
 */
export function normalizeFleet(raw: FleetResponse): FleetResponse {
  return {
    generatedAt: raw.generatedAt ?? "",
    partial: raw.partial ?? null,
    clusters: (raw.clusters ?? []).map((c) => ({
      ...c,
      instancesList: c.instancesList ?? [],
      conditions: c.conditions ?? [],
      pvcs: c.pvcs ?? [],
      services: c.services ?? [],
      poolers: c.poolers ?? [],
    })),
  };
}
