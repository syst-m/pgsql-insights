/* The R10.12 named fixtures — now generated from the contract, not retyped.
 *
 * This file used to be 386 lines of hand-transcribed cluster data. That was
 * R16.8's failure waiting to happen: a second copy of the contract, on this
 * side, that CI validated the TS types against — and the TS types were written
 * by the same person who wrote them. When A1 froze the real contract at
 * `9214534`, the two copies disagreed in every nested type and nothing
 * complained.
 *
 * So the duplicate is gone. `FIXTURE_FLEET` is now `testdata/api/clusters.json`
 * — the file R16.8 names as the byte-level contract, the same one the Go
 * `TestGoldenFixtureIsCanonical` re-marshals — run through the same
 * `parseEnvelope`/`toFleetResponse` path the live client uses. Two things
 * follow:
 *
 *   - the annotation on `FIXTURE_ENVELOPE` below makes a json tag renamed on the
 *     Go side a *compile* error here, because `resolveJsonModule` gives the
 *     import a structural type and `Envelope<ClusterView>` has to accept it;
 *   - the fixture and the live payload now go through identical code, so the
 *     fixture cannot pass by being shaped differently from the API.
 *
 * The four reference clusters themselves (ai-api-gateway/rev-proxy-pg with the
 * only Pooler, cookbook/cookbook-pg single-instance, freeidp/freeidp-pg with
 * two instances and zero serving, open-webui/open-webui-pg on PG16) are
 * unchanged in content — they came from docs/ENVIRONMENT.md and
 * docs/research/live-probe.md and are now maintained on the Go side.
 */

import apiClustersJson from "../../../testdata/api/clusters.json";

import { emptyEnvelope, toFleetResponse, type RowExtras } from "./envelope";
import type { ClusterMetrics, ClusterView, Envelope, Finding, FleetResponse, FleetRow } from "./types";

/** The drift check, in one line.
 *
 *  `apiClustersJson`'s type is inferred from the JSON by `resolveJsonModule`;
 *  assigning it to `Envelope<ClusterView>` only compiles while the two agree.
 *  Rename a json tag in internal/api/dto/clusters.go without regenerating the
 *  fixture and `make typecheck-web` fails — which is the R16.8 guarantee,
 *  enforced by the compiler instead of by review. */
export const FIXTURE_ENVELOPE: Envelope<ClusterView> = apiClustersJson;

/** Now read from the contract instead of being a second copy of it. The old
 *  value ("2026-11-20T11:04:12Z") was invented here; the contract's is
 *  "2026-11-20T09:12:00Z". Still fixed, so the visual snapshots and the
 *  R10.21 timezone tests stay deterministic. */
export const FIXTURE_GENERATED_AT: string = FIXTURE_ENVELOPE.meta.generatedAt;

/* The presentation extras the wire does not carry (see types.ts): `metrics` is
 * Phase 5's R9 contract and `topFinding` Phase 6's insight engine. They are
 * declared here as presentation data — which is what they are — rather than
 * being added to the wire type, where they would be a lie about the endpoint.
 *
 * `unconfigured` on three clusters is live-probe Finding 1 (pg_stat_statements
 * is absent on every reachable cluster); `failed` on freeidp-pg is Finding 3
 * (the endpoint is unreachable at all, so the scrape broke rather than never
 * being set up). R10.12's "unavailable (source not configured)" state needs the
 * distinction and the fixture is where it is asserted. */
const UNCONFIGURED: ClusterMetrics = {
  tps: null,
  p95LatencyMs: null,
  connectionsPct: null,
  diskPct: null,
  source: "unconfigured",
  collectedAt: null,
};

const FAILED: ClusterMetrics = {
  tps: null,
  p95LatencyMs: null,
  connectionsPct: null,
  diskPct: null,
  source: "failed",
  collectedAt: null,
};

const DISK_PRESSURE: Finding = {
  code: "disk-pressure",
  severity: "critical",
  summary: "No space left on device; postgres container in CrashLoopBackOff",
};

const PRESENTATION: Record<string, RowExtras> = {
  "ai-api-gateway/rev-proxy-pg": { metrics: UNCONFIGURED },
  "cookbook/cookbook-pg": { metrics: UNCONFIGURED },
  "freeidp/freeidp-pg": { metrics: FAILED, topFinding: DISK_PRESSURE },
  "open-webui/open-webui-pg": { metrics: UNCONFIGURED },
};

/** The four clusters as the contract serialises them, plus the derived row
 *  fields. Exported so tests can address a single row without rebuilding the
 *  envelope. */
export const FIXTURE_CLUSTERS: readonly FleetRow[] = toFleetResponse(FIXTURE_ENVELOPE, PRESENTATION).clusters;

/** The plain fleet: all four reference clusters, no partial render.
 *
 *  With Phase 1a shipping discovery only, this is also R10.12's *unavailable*
 *  state — no cluster has a metrics source that can answer — which is why
 *  `?fixture=fleet` and `?fixture=unavailable` render the same banner. That is
 *  not a fixture bug: it is the reference environment's real condition (R10.12
 *  names it, live-probe Finding 1 causes it), and it stops being true the
 *  moment Phase 5 populates `metrics`. */
export const FIXTURE_FLEET: FleetResponse = toFleetResponse(FIXTURE_ENVELOPE, PRESENTATION);

/** R10.12's empty state: a valid envelope that served zero clusters. */
export const FIXTURE_EMPTY: FleetResponse = toFleetResponse(emptyEnvelope(FIXTURE_GENERATED_AT));

/** R10.22's partial render: three of the four clusters, with the fourth's
 *  namespace named in `meta.loading` rather than in a `partial` key.
 *
 *  `meta.total` is 3, not 4 — the contract counts what the response serves and
 *  names the missing namespace instead, so "attempted" never has to be encoded
 *  in a field that already means "served". */
const PARTIAL_ENV: Envelope<ClusterView> = {
  data: FIXTURE_ENVELOPE.data.filter((c) => c.namespace !== "freeidp"),
  meta: {
    total: FIXTURE_ENVELOPE.data.filter((c) => c.namespace !== "freeidp").length,
    truncated: false,
    generatedAt: FIXTURE_GENERATED_AT,
    loading: ["freeidp"],
  },
};

export const FIXTURE_PARTIAL: FleetResponse = toFleetResponse(PARTIAL_ENV, PRESENTATION);

/** R10.22's second partial case: truncated *and* partial, which is the pair the
 *  spec says the fixture set was missing.
 *
 *  `FIXTURE_PARTIAL` sets `meta.total` to the filtered length with
 *  `truncated: false`, so `total` and `data.length` are equal there and a
 *  banner built from either one reads the same. R8.4 says paging and a partial
 *  render co-occur in contract — with 120 matching and `limit=50`, `total` is
 *  120 and `data.length` is 50 — and only that case separates the two.
 *
 *  Here four clusters match, this page serves two of them, and the other two
 *  (freeidp, open-webui) are named in `loading` because their namespaces are
 *  still in the first discovery pass — so `total: 4` and `data.length: 2` are
 *  both correct and the banner has to pick the second one. */
const TRUNCATED_PARTIAL_ENV: Envelope<ClusterView> = {
  data: FIXTURE_ENVELOPE.data.slice(0, 2),
  meta: {
    total: FIXTURE_ENVELOPE.data.length,
    truncated: true,
    generatedAt: FIXTURE_GENERATED_AT,
    nextCursor: "eyJvZmZzZXQiOjJ9",
    loading: ["freeidp", "open-webui"],
  },
};

export const FIXTURE_TRUNCATED_PARTIAL: FleetResponse =
  toFleetResponse(TRUNCATED_PARTIAL_ENV, PRESENTATION);

/** R10.12's "unavailable (source not configured)". A separate object from
 *  FIXTURE_FLEET so the two stay addressable individually even while their
 *  content is the same — see the note on FIXTURE_FLEET. */
export const FIXTURE_UNAVAILABLE: FleetResponse = toFleetResponse(FIXTURE_ENVELOPE, PRESENTATION);
