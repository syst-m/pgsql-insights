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

import apiActiveQueriesJson from "../../../testdata/api/active-queries.json";
import apiClustersJson from "../../../testdata/api/clusters.json";
import apiQueryDetailJson from "../../../testdata/api/query-detail.json";
import apiQueryPlansJson from "../../../testdata/api/query-plans.json";
import apiQuerySeriesJson from "../../../testdata/api/query-series.json";
import apiQueriesJson from "../../../testdata/api/queries.json";
import apiSessionsTerminateJson from "../../../testdata/api/sessions-terminate.json";
import apiTagsJson from "../../../testdata/api/tags.json";

import { emptyEnvelope, toFleetResponse, type RowExtras } from "./envelope";
import type {
  ActiveQueriesResponse,
  ClusterMetrics,
  ClusterView,
  Envelope,
  Finding,
  FleetResponse,
  FleetRow,
  PlanSampleView,
  QueryDetailView,
  QuerySeriesEnvelope,
  QueryView,
  TagView,
  TerminateResponse,
} from "./types";

/** The drift check, in one line.
 *
 *  `apiClustersJson`'s type is inferred from the JSON by `resolveJsonModule`;
 *  assigning it to `Envelope<ClusterView>` only compiles while the two agree.
 *  Rename a json tag in internal/api/dto/clusters.go without regenerating the
 *  fixture and `make typecheck-web` fails — which is the R16.8 guarantee,
 *  enforced by the compiler instead of by review. */
export const FIXTURE_ENVELOPE: Envelope<ClusterView> = apiClustersJson;

/* The Phase 3 query fixtures, same rule as the cluster ones: the annotation is
 * the drift check, so a json tag renamed in internal/api/dto/queries.go without
 * regenerating the fixture is a compile error here. R16.8 wants a fixture per
 * endpoint and Phase 3 serves four from that file, so there are four files and
 * four exports — `queries.json` is the name clusters_test.go pre-announces, the
 * other three are named after the routes that serve them. */

/** GET /api/v1/insights/queries — the R3.2 table, R9.5's top-N page. */
export const FIXTURE_QUERIES: Envelope<QueryView> = apiQueriesJson;

/** GET /api/v1/insights/queries/{fingerprint} — R10.7's detail payload. Not an
 *  envelope: the detail endpoint returns one object, so this mirrors the dto's
 *  `QueryDetailView` directly rather than `Envelope<…>`. */
export const FIXTURE_QUERY_DETAIL: QueryDetailView = apiQueryDetailJson;

/** GET /api/v1/insights/queries/{fingerprint}/plans — R3.10's two states: one
 *  `available` sample with its plan JSON, one
 *  `unavailable (insufficient table privileges)` with a message and no plan. */
export const FIXTURE_QUERY_PLANS: Envelope<PlanSampleView> = apiQueryPlansJson;

/* R8.11's series fixture, from dto/series.go rather than dto/queries.go, so it
 * sits outside the four-file group above — and it is the one `4cef345` shipped
 * the route without. That commit landed `series.go`, its Go test, the openapi
 * entry and the handler, and left `testdata/api/query-series.json` out, which
 * left `client.test.ts` holding an inlined copy of the openapi example instead
 * of importing the fixture (its own comment said so, and named this file as the
 * fix). A1's `8b046ef` added the JSON and the Go test that guards it; this is
 * the TS half of that.
 *
 * Three entries, and the three shapes R8.11 allows are all here: one with every
 * `omitempty` label and two minutes of points, one with no `pod`/`instanceRole`
 * (the cluster-aggregated answer, one point), and one whose `points` is `[]` —
 * the empty-not-null case that `parseQuerySeries` exists to police.
 *
 * The one fixture here written with `as` instead of the annotation the other five
 * use, and the reason is `instanceRole`. `resolveJsonModule` types every string
 * in a JSON file as `string`, so a bare annotation can only carry the drift check
 * while the payload has no union-typed field — which is why `types.ts` types
 * `PlanSampleView.availability` as `string` and keeps `PlanAvailability` as a
 * separate type. `QuerySeries.instanceRole` is the exception, and its own doc
 * explains why the union is the narrow two rather than `InstanceRole`; widening
 * it to `string` to satisfy the annotation would delete that decision to fix a
 * typing artefact. The cast keeps the check that matters: it is still an
 * assignability test, so a json tag renamed in `series.go` without regenerating
 * the fixture fails with TS2352 (verified — `fingerprint`→`fp` and
 * `executionTimeSeconds: number`→`string` both error), and what it stops
 * re-checking is only that `"primary"` is a member of a union the JSON import
 * already erased. The runtime half is `parseQuerySeries`, which does police the
 * enum, and `client.test.ts` runs this object through it. */
export const FIXTURE_QUERY_SERIES = apiQuerySeriesJson as QuerySeriesEnvelope;

/** GET /api/v1/insights/tags — R3.12's dimensions with R3.7's rollup. Three
 *  tags: a full comment, no comment at all, and a partial one. */
export const FIXTURE_TAGS: Envelope<TagView> = apiTagsJson;

/** GET /api/v1/active-queries — R4.1's sessions and scorecard plus R4.4's
 *  chains. Three rows, one chain, both `longest*` pointers populated, and
 *  `meta.total: 3` equal to `data.length` — the pairing `client.test.ts`
 *  asserts on instead of trusting. Assigned, not cast: the same drift check
 *  `FIXTURE_ENVELOPE` is, so a renamed tag in sessions.go fails the build here
 *  rather than rendering a table with a blank column. The third row carries no
 *  `queryid`, which is the fixture's own proof that the field is omitempty. */
export const FIXTURE_ACTIVE_QUERIES: ActiveQueriesResponse = apiActiveQueriesJson;

/** POST /api/v1/sessions/{pid}/terminate — R4.5's audit answer, and the second
 *  fixture here for a route whose body is not an envelope. Assigned, not cast:
 *  the same one-line drift check `FIXTURE_ENVELOPE` is, so a json tag renamed on
 *  `dto.TerminateResponse` without regenerating the fixture fails the typecheck
 *  here. One object, four fields, no `meta` — the non-envelope case the
 *  interface above explains, and the reason there is no `data` array to pin. */
export const FIXTURE_TERMINATE_SESSION: TerminateResponse = apiSessionsTerminateJson;

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
