/* The wire → presentation boundary (R8.1, R16.8).
 *
 * This module is the only place a JSON payload becomes a `FleetResponse`. It
 * exists as its own file for one reason: `client.ts` and `fixtures.ts` both
 * need it, and if it lived in `client.ts` then `fixtures.ts` would import
 * `client.ts`, which imports `fixtures.ts` — a cycle.
 *
 * The important property is in `parseEnvelope`. The first version of the client
 * did `await res.json() as FleetResponse`, and `normalizeFleet` then read
 * `raw.clusters ?? []`. Against A1's real envelope — `{data, meta}`, no
 * `clusters` key — that cast succeeded (a cast never checks anything), the
 * `?? []` fired, and the page rendered the empty state against a working API.
 * A blank page and an empty fleet were indistinguishable, which is exactly the
 * R16.8 failure class: two copies of a contract disagreeing silently.
 *
 * So the envelope is *checked*, not cast. A payload that is not an
 * `Envelope` throws, and the error says what arrived.
 */

import { healthFor } from "./health";
import {
  clusterId,
  type ClusterView,
  type Envelope,
  type ErrorDetail,
  type ErrorCode,
  type FleetRow,
  type FleetResponse,
  type Meta,
  type QueryDetailView,
  type QueryResponse,
  type QuerySeries,
  type QueryView,
} from "./types";

/** Thrown when a payload is not shaped like the contract. Distinct from
 *  `ApiError`, which is a non-2xx HTTP response: this is a 200 whose body the
 *  client cannot use. */
export class ContractError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ContractError";
  }
}

function typeName(v: unknown): string {
  if (v === null) return "null";
  if (Array.isArray(v)) return "array";
  return typeof v;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** How a row of type `T` is validated: the parser each endpoint's rows get
 *  checked with. Its own type so `parseEnvelope` can take it as a parameter
 *  with a default, which is what keeps one generic function serving every
 *  list endpoint instead of a union that has to be widened by hand each time a
 *  page lands (`FleetResponse | QueriesResponse | DatabasesResponse …` — a
 *  growing union is a per-phase edit to a shared file, and shared files are
 *  where the `docs/agents.md` §5 rule-1 commits happen). */
export type RowParser<T> = (raw: unknown) => T;

/** Check the R8.4 envelope shell and return it typed.
 *
 * This validates the envelope's own shape (`data` is an array; `meta` has the
 * three required fields) and then each row's required fields — it does not
 * re-implement the Go validator. The division of labour is deliberate: the Go
 * side owns the shape, and this exists to catch the one failure that a cast
 * hides, which is the top-level key being wrong. Deep field validation would
 * turn every future server bug into a blank page; a shell check catches the
 * class that actually bit.
 *
 *  Two overloads rather than one generic signature with a default. The obvious
 *  form — `parseEnvelope<T>(json, parseRow: RowParser<T> = parseClusterView)` —
 *  does not compile: TS will not infer a type parameter from a parameter's
 *  *default value*, so `T` resolves to `unknown` and the default is then
 *  rejected (`ClusterView is not assignable to T`). The first overload is what
 *  keeps every existing `parseEnvelope(json)` call site returning
 *  `Envelope<ClusterView>`; the second is the generic form a new endpoint uses.
 *  The implementation signature is the non-generic `RowParser<unknown>` form
 *  with the default on it — a generic implementation signature is not accepted
 *  either (the same complaint, from the other side), because an overload's
 *  implementation must be assignable to *both* signatures and a
 *  `<T>(…, parseRow: RowParser<T> = parseClusterView)` cannot be: for the
 *  generic one, `T` is every type at once, and `ClusterView` is not. */
export function parseEnvelope(json: unknown): Envelope<ClusterView>;
export function parseEnvelope<T>(json: unknown, parseRow: RowParser<T>): Envelope<T>;
export function parseEnvelope(
  json: unknown,
  parseRow: RowParser<unknown> = parseClusterView,
): Envelope<unknown> {
  if (!isRecord(json)) {
    throw new ContractError(`expected an Envelope object from the API, got ${typeName(json)}`);
  }
  const { data, meta } = json;
  if (!Array.isArray(data)) {
    // The bug this exists for: a body with `clusters` instead of `data`, or a
    // bare array, or an error body served with a 200.
    throw new ContractError(
      `Envelope.data is ${typeName(data)}, expected array (keys present: ${
        Object.keys(json).join(", ") || "none"
      })`,
    );
  }
  return { data: data.map(parseRow), meta: parseMeta(meta) };
}

/** Check `meta`'s required fields. `nextCursor`, `failed` and `loading` are
 *  `omitempty` in Go, so their absence is normal and only their *types* are
 *  checked when present. */
function parseMeta(meta: unknown): Meta {
  if (!isRecord(meta)) {
    throw new ContractError(`Envelope.meta is ${typeName(meta)}, expected object`);
  }
  const { total, truncated, generatedAt } = meta;
  if (typeof total !== "number") {
    throw new ContractError(`meta.total is ${typeName(total)}, expected number`);
  }
  if (typeof truncated !== "boolean") {
    throw new ContractError(`meta.truncated is ${typeName(truncated)}, expected boolean`);
  }
  if (typeof generatedAt !== "string") {
    throw new ContractError(`meta.generatedAt is ${typeName(generatedAt)}, expected string`);
  }
  return meta as unknown as Meta;
}

/** Check one row's required fields and name it in the error.
 *
 * `name`/`namespace` are checked first because `clusterId()` would otherwise
 * render the row as "undefined/undefined" — a table full of blank links is the
 * same silent failure the envelope check exists to prevent. */
export function parseClusterView(raw: unknown): ClusterView {
  if (!isRecord(raw)) {
    throw new ContractError(`cluster entry is ${typeName(raw)}, expected object`);
  }
  // Name the row in every message, so a bad row in a 4-cluster payload says
  // which one instead of failing the whole page with "cluster entry".
  const where = typeof raw.name === "string" ? `${raw.namespace ?? "?"}/${raw.name}` : typeName(raw);
  const require = (field: string, type: "string" | "number" | "boolean" | "object" | "array") => {
    const v = raw[field];
    // `pgMajor` is legitimately null in the contract (R1.3: unresolved), and
    // `null` is not one of the named types, so it gets its own check below.
    if (v === undefined) throw new ContractError(`cluster ${where}: missing required field "${field}"`);
    if (type === "object" ? !isRecord(v) : type === "array" ? !Array.isArray(v) : typeof v !== type) {
      throw new ContractError(`cluster ${where}: field "${field}" is ${typeName(v)}, expected ${type}`);
    }
  };

  require("namespace", "string");
  require("name", "string");
  require("phase", "string");
  require("instances", "number");
  require("readyInstances", "number");
  require("servingInstances", "number");
  // Two-step, because `require` cannot express "null is valid": R1.3's field is
  // `*int` with no omitempty, so the Go side always emits it — `"pgMajor": 17`
  // or `"pgMajor": null` — and an *absent* key is the contract break, while a
  // null is the documented unknown. A single check would have to report the
  // null as `field "pgMajor" is null, expected number`, which is the opposite
  // of the truth.
  if (raw.pgMajor === undefined) {
    throw new ContractError(`cluster ${where}: missing required field "pgMajor"`);
  }
  if (raw.pgMajor !== null && typeof raw.pgMajor !== "number") {
    throw new ContractError(`cluster ${where}: field "pgMajor" is ${typeName(raw.pgMajor)}, expected number or null`);
  }
  require("instanceList", "array");
  require("conditions", "array");
  require("certificates", "object");
  require("pvcs", "array");
  require("services", "array");
  require("poolers", "array");
  require("credentialSource", "string");
  require("createdAt", "string");

  // `certificates.expirations` is not omitempty in Go — a cluster with no
  // known certs still serialises it as `{}` — so a missing one is a contract
  // break, and the detail page's cert table reads it unconditionally.
  if (!isRecord((raw.certificates as Record<string, unknown>).expirations)) {
    throw new ContractError(`cluster ${where}: certificates.expirations is not an object`);
  }

  return raw as unknown as ClusterView;
}

/** Check one query row's required fields and name it in the error.
 *
 *  Same rule as `parseClusterView`: check the fields the Go declares
 *  non-`omitempty` and stop. `pgMajor` has no two-step here — queries.go:203
 *  types it a plain `int`, not the `*int` R1.3's cluster field is, because a
 *  query row only exists once a collector resolved the major — so a plain
 *  `require(…, "number")` is the correct check and a null would be a contract
 *  break rather than the documented unknown.
 *
 *  The eight `LoadColumns` members are listed rather than spread, because
 *  `extends` on the TS side mirrors Go's embedded struct but a runtime object
 *  carries them as flat keys; the list is the mirror of queries.go:90–132. */
export function parseQueryView(raw: unknown): QueryView {
  if (!isRecord(raw)) {
    throw new ContractError(`query entry is ${typeName(raw)}, expected object`);
  }
  // Name the row the same way clusters do: a bad fingerprint in a 7-row payload
  // should say which one.
  const where = typeof raw.fingerprint === "string" ? raw.fingerprint.slice(0, 12) : typeName(raw);
  const require = (field: string, type: "string" | "number" | "boolean" | "object" | "array") => {
    const v = raw[field];
    if (v === undefined) throw new ContractError(`query ${where}: missing required field "${field}"`);
    if (type === "object" ? !isRecord(v) : type === "array" ? !Array.isArray(v) : typeof v !== type) {
      throw new ContractError(`query ${where}: field "${field}" is ${typeName(v)}, expected ${type}`);
    }
  };

  require("fingerprint", "string");
  require("query", "string");
  require("truncated", "boolean");
  require("database", "string");
  require("namespace", "string");
  require("cluster", "string");
  require("pgMajor", "number");
  require("recommendations", "array");
  // LoadColumns, embedded in Go (queries.go:215) and flat on the wire.
  for (const col of [
    "loadByTotalTime",
    "pctLoadByQueries",
    "avgExecTimeMs",
    "totalExecTimeMs",
    "avgRowsReturned",
    "timesCalled",
    "loadByCPUMs",
    "loadByIOWaitMs",
  ] as const) {
    require(col, "number");
  }

  return raw as unknown as QueryView;
}

/** Check the `/insights/queries/{fingerprint}` body and return it typed.
 *
 *  The fourth published payload, and the only one that is not an envelope: the
 *  detail route's 200 is a bare `QueryDetailView` (openapi's 200 schema is the
 *  object itself, not `Envelope[…]`), so there is no `parseEnvelope` call to
 *  hang the check on. Without this function, `fetchQueryDetail`'s http path is
 *  the one place in the client that would have to cast a body — the exact
 *  `res.json() as FleetResponse` this module was extracted to remove, and the
 *  reason `client.ts`'s header says the http path parses rather than casts.
 *
 *  The inherited half is checked by `parseQueryView`, not restated. The dto
 *  embeds `QueryView` in `QueryDetailView` (queries.go:241) and the TS mirror
 *  extends it (types.ts:582), so the six identity fields and the eight
 *  `LoadColumns` get exactly the checks the list route applies to them — one
 *  copy of the field list, which is R16.8's whole point.
 *
 *  `plans[]` entries are not inspected here, for the same reason
 *  `parseClusterView` stops at `instanceList` and `parseQueryView` stops at
 *  `recommendations`: a malformed entry costs one sub-table on a page, not the
 *  page. `parseQuerySeries` is the exception because there `points` *is* the
 *  payload. The plans route's own row parser is what will validate these when
 *  `fetchQueryPlans` lands; writing a `PlanSampleView` check here would be a
 *  second copy of a list that file is about to own. */
export function parseQueryDetailView(raw: unknown): QueryDetailView {
  if (!isRecord(raw)) {
    throw new ContractError(`query-detail payload is ${typeName(raw)}, expected object`);
  }
  // The embedded half first, so its errors name the row the way the list
  // parser's do (`query 84f2a91c…: …`) and so `fingerprint` is known to be a
  // string before the detail-only checks use it as the address.
  const view = parseQueryView(raw);
  const where = view.fingerprint.slice(0, 12);
  const require = (field: string, type: "string" | "object" | "array") => {
    const v = raw[field];
    if (v === undefined) throw new ContractError(`query-detail ${where}: missing required field "${field}"`);
    if (type === "object" ? !isRecord(v) : type === "array" ? !Array.isArray(v) : typeof v !== type) {
      throw new ContractError(`query-detail ${where}: field "${field}" is ${typeName(v)}, expected ${type}`);
    }
  };

  // The four fields queries.go:250-276 adds to the embedded QueryView. All four
  // are tagged without `omitempty`, so all four are always present — and for the
  // two slices that includes the empty case: a nil Go slice with no omitempty
  // marshals as `null`, which this rejects for the same reason `parseEnvelope`
  // rejects `"data": null` (the house rule is `[]` on the wire).
  require("firstSeen", "string");
  require("latency", "object");
  require("plans", "array");
  require("planChanges", "array");
  // `user` is the one field on this shape that is omitempty (queries.go:250):
  // R3.2's table has no user column, and a fingerprint the store knows the
  // cluster for but not the role for has nothing to show.
  if (raw.user !== undefined && typeof raw.user !== "string") {
    throw new ContractError(`query-detail ${where}: field "user" is ${typeName(raw.user)}, expected string`);
  }

  return raw as unknown as QueryDetailView;
}

/** Check one `QuerySeriesPoint`'s six fields, naming the owning fingerprint.
 *
 *  A checker, not a constructor — same shape as the `require` closures in the
 *  other two parsers: it throws or returns, and the caller casts.
 *
 *  All six are required. `series.go:112` gives every field a plain json tag with
 *  no `omitempty`, and the openapi schema lists all six under `required`, so an
 *  absent key is a contract break rather than an omitted optional. The five
 *  metric fields are `number` because Go types them all `float64` — including
 *  `rows`, which is a per-minute delta off a cumulative counter (R3.3) and so is
 *  not narrowable to an int even though rows are countable. */
function checkQuerySeriesPoint(raw: unknown, where: string, index: number): void {
  if (!isRecord(raw)) {
    throw new ContractError(`query ${where}: points[${index}] is ${typeName(raw)}, expected object`);
  }
  const require = (field: string, type: "string" | "number") => {
    const v = raw[field];
    if (v === undefined) {
      throw new ContractError(`query ${where}: points[${index}] missing required field "${field}"`);
    }
    if (typeof v !== type) {
      throw new ContractError(
        `query ${where}: points[${index}].${field} is ${typeName(v)}, expected ${type}`,
      );
    }
  };
  require("timestamp", "string");
  require("executionTimeSeconds", "number");
  require("rows", "number");
  require("sharedBlocksHit", "number");
  require("sharedBlocksRead", "number");
  require("lockTimeSeconds", "number");
}

/** Check one `/insights/queries/series` row and name it in the error.
 *
 *  The third row parser, for the third published payload family (R8.11; the
 *  types live in `internal/api/series.go`, not `dto` — see the header note).
 *  `parseEnvelope(json, parseQuerySeries)` is how the Detail chart's client
 *  call gets a typed envelope, and it is the reason the overload pair exists.
 *
 *  **This one checks its nested array, where the other two do not.**
 *  `parseClusterView` validates `instanceList` as an array and stops; the
 *  `InstanceView` entries inside are never inspected. Same for
 *  `parseQueryView`'s `recommendations`. The reason those two can stop is that
 *  the array is one of seven sibling collections on the row — a malformed entry
 *  there costs one sub-table on a detail page. Here `points` *is* the row's
 *  payload: the endpoint exists to serve it, the chart renders nothing else, and
 *  a point missing `executionTimeSeconds` plots a hole the UI has no way to
 *  distinguish from a real zero. That is the class this module exists to catch,
 *  so the check goes one level deep. */
export function parseQuerySeries(raw: unknown): QuerySeries {
  if (!isRecord(raw)) {
    throw new ContractError(`query-series entry is ${typeName(raw)}, expected object`);
  }
  // Same naming rule as the other two: a bad entry in a multi-entry payload says
  // which one. Truncated to 12 chars because a fingerprint is 64.
  const where = typeof raw.fingerprint === "string" ? raw.fingerprint.slice(0, 12) : typeName(raw);
  const require = (field: string, type: "string" | "array") => {
    const v = raw[field];
    if (v === undefined) throw new ContractError(`query-series ${where}: missing required field "${field}"`);
    if (type === "array" ? !Array.isArray(v) : typeof v !== type) {
      throw new ContractError(`query-series ${where}: field "${field}" is ${typeName(v)}, expected ${type}`);
    }
  };

  // The four fields series.go:142 declares without `omitempty`.
  require("fingerprint", "string");
  require("namespace", "string");
  require("cluster", "string");
  require("points", "array");

  // The five it declares *with* `omitempty` — `queryid`, `pod`, `instanceRole`,
  // `datname`, `usename`. Their absence is legal and means something specific:
  // a cluster-aggregated answer (what R10.6's table row asks for) has no pod and
  // no role, and `QueryDetailView`'s own chart asks for exactly that shape. So
  // these are type-checked only when present; a check that demanded them would
  // throw on the most common legal response.
  const optional = (field: string, type: "string") => {
    const v = raw[field];
    if (v !== undefined && typeof v !== type) {
      throw new ContractError(`query-series ${where}: field "${field}" is ${typeName(v)}, expected ${type}`);
    }
  };
  optional("queryid", "string");
  optional("pod", "string");
  optional("datname", "string");
  optional("usename", "string");
  // The enum, not the wider `InstanceRole` union: the published schema admits
  // exactly two values (openapi QuerySeries.instanceRole `enum: [primary,
  // replica]`), so a third — including clusters.go's `unhealthy`, which is not a
  // role a sample can belong to — is a contract break, not a type to accept.
  if (raw.instanceRole !== undefined && raw.instanceRole !== "primary" && raw.instanceRole !== "replica") {
    throw new ContractError(
      `query-series ${where}: field "instanceRole" is ${JSON.stringify(raw.instanceRole)}, expected "primary" or "replica"`,
    );
  }

  (raw.points as unknown[]).forEach((point, i) => checkQuerySeriesPoint(point, where, i));

  return raw as unknown as QuerySeries;
}

/** Parse an R8.5 error body. Returns null when the body is not one, so the
 *  caller can fall back to the HTTP status line rather than throwing a second
 *  error on top of the first. */
export function parseErrorBody(json: unknown): ErrorDetail | null {
  if (!isRecord(json) || !isRecord(json.error)) return null;
  const { code, message, details } = json.error as Record<string, unknown>;
  if (typeof code !== "string" || typeof message !== "string") return null;
  return {
    code: code as ErrorCode,
    message,
    // NewError normalises a nil slice to `[]`, so a missing `details` means the
    // server did not go through NewError. Defaulting here keeps the client from
    // branching on the array's existence, which is the whole point of R8.5.
    details: Array.isArray(details) ? details.filter((d): d is string => typeof d === "string") : [],
  };
}

/** The presentation extras a row can carry that the wire does not send yet.
 *
 * `metrics` and `topFinding` are Phase 5's R9 contract and Phase 6's insight
 * engine; dto/clusters.go leaves them out of ClusterView on purpose. The
 * fixture supplies them so the R10.12 states stay assertable, and the live
 * client supplies nothing until Phase 5 lands. */
export type RowExtras = Pick<FleetRow, "metrics" | "topFinding">;

/** Build the page's payload from a validated envelope.
 *
 * `observedAt` is `meta.generatedAt` denormalised onto each row: R10.21's
 * "data as of" stamp is per-response, but the table sorts and renders it per
 * row. `health` is R1.2's rollup, computed from the counts (health.ts). */
export function toFleetResponse(env: Envelope<ClusterView>, extras: Record<string, RowExtras> = {}): FleetResponse {
  return {
    meta: env.meta,
    clusters: env.data.map((c) => ({
      ...c,
      health: healthFor(c),
      observedAt: env.meta.generatedAt,
      ...extras[clusterId(c)],
    })),
  };
}

/** The Queries page's payload from a validated query envelope.
 *
 *  The mirror of `toFleetResponse` with the presentation half subtracted: no
 *  `health` to compute (health.ts is R1.2's rule about clusters) and no
 *  `observedAt` to denormalise (R10.21's stamp stays on `meta`, because the
 *  Queries table sorts by `totalExecTimeMs`, not by freshness). What is left is
 *  the pair A6 asked for — the same 3 lines with `queries` in place of
 *  `clusters` — and that is the point: the envelope is generic, so a new
 *  endpoint costs a row parser and a builder, not a new shape family. */
export function toQueriesResponse(env: Envelope<QueryView>): QueryResponse {
  return { meta: env.meta, queries: env.data };
}

/** An envelope with no rows, for the R10.12 empty fixture. */
export function emptyEnvelope<T = ClusterView>(generatedAt: string): Envelope<T> {
  return { data: [], meta: { total: 0, truncated: false, generatedAt } };
}

/** Everything the banner needs: the rows this response actually served, plus
 *  their `meta`. Both the wire type and the presentation type qualify, which is
 *  the point — the banner is derived from a *response*.
 *
 *  Taking `Meta` alone was the first signature, and it is how the bug got in:
 *  with only `meta` in reach, the served count available to interpolate is
 *  `meta.total`, and R8.4 (corrected at A1's `f08b6ac`) says `meta.total` is
 *  R8.10's **pre-cut** count — matching rows across *all* pages. On a truncated
 *  page that overstates the banner by the rest of the result set: "120 clusters
 *  served" above a table rendering 50. R10.22 now names the rule directly:
 *  "The banner's served-cluster count comes from `data.length`, never
 *  `meta.total`." */
export type BannerSource<T = unknown> = { meta: Meta } & ({ data: readonly T[] } | { clusters: readonly T[] });

/** `data.length` on the wire type, `clusters.length` on the presentation type —
 *  the same number, because `toFleetResponse` maps `data` 1:1. */
function rowsServed<T>(source: BannerSource<T>): number {
  return "data" in source ? source.data.length : source.clusters.length;
}

/** R10.22's banner sentence, composed from the response rather than read from a
 *  `partial` key the API does not emit.
 *
 *  `truncated` alone is not a partial render — it means there is another page
 *  (R8.10), which is a paging affordance, not a warning. The banner fires only
 *  when a namespace failed or is still loading, which is what R10.22's
 *  "3 of 4 clusters shown, and it says so" is about. `meta.generatedAt` is
 *  R10.21's page-header stamp and is deliberately not a banner input either. */
export function partialBanner<T>(source: BannerSource<T>): string | null {
  const meta = source.meta;
  const failed = meta.failed ?? [];
  const loading = meta.loading ?? [];
  if (failed.length === 0 && loading.length === 0) return null;

  const parts: string[] = [];
  if (loading.length > 0) {
    parts.push(
      `${loading.length} namespace${loading.length === 1 ? "" : "s"} still in the first discovery pass (${joinNames(loading)})`,
    );
  }
  if (failed.length > 0) {
    const detail = failed
      .map((f) => {
        const stale = f.lastSuccessTime === undefined ? "" : `, stale since ${f.lastSuccessTime}`;
        return `${f.namespace} (${f.code}${stale})`;
      })
      .join(", ");
    parts.push(`${failed.length} namespace${failed.length === 1 ? "" : "s"} failed to sync: ${detail}`);
  }

  const served = `${rowsServed(source)} cluster${rowsServed(source) === 1 ? "" : "s"} served`;
  const paging = meta.truncated && meta.nextCursor !== undefined ? ", more pages available" : "";
  return `${served}${paging}. ${parts.join("; ")}.`;
}

/** First three names, then "+N more" — a banner is one line, and a failed
 *  RBAC scope can list dozens of namespaces. */
function joinNames(names: readonly string[]): string {
  if (names.length <= 3) return names.join(", ");
  return `${names.slice(0, 3).join(", ")} +${names.length - 3} more`;
}
