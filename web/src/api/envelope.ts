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

/** Check the R8.4 envelope shell and return it typed.
 *
 * This validates the envelope's own shape (`data` is an array; `meta` has the
 * three required fields) and then each row's required fields — it does not
 * re-implement the Go validator. The division of labour is deliberate: the Go
 * side owns the shape, and this exists to catch the one failure that a cast
 * hides, which is the top-level key being wrong. Deep field validation would
 * turn every future server bug into a blank page; a shell check catches the
 * class that actually bit. */
export function parseEnvelope(json: unknown): Envelope<ClusterView> {
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
  return { data: data.map(parseClusterView), meta: parseMeta(meta) };
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
function parseClusterView(raw: unknown): ClusterView {
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

/** An envelope with no rows, for the R10.12 empty fixture. */
export function emptyEnvelope(generatedAt: string): Envelope<ClusterView> {
  return { data: [], meta: { total: 0, truncated: false, generatedAt } };
}

/** R10.22's banner sentence, composed from `meta` rather than read from a
 *  `partial` key the API does not emit.
 *
 *  `truncated` alone is not a partial render — it means there is another page
 *  (R8.10), which is a paging affordance, not a warning. The banner fires only
 *  when a namespace failed or is still loading, which is what R10.22's
 *  "3 of 4 clusters shown, and it says so" is about. */
export function partialBanner(meta: Meta): string | null {
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

  const served = `${meta.total} cluster${meta.total === 1 ? "" : "s"} served`;
  const paging = meta.truncated && meta.nextCursor !== undefined ? ", more pages available" : "";
  return `${served}${paging}. ${parts.join("; ")}.`;
}

/** First three names, then "+N more" — a banner is one line, and a failed
 *  RBAC scope can list dozens of namespaces. */
function joinNames(names: readonly string[]): string {
  if (names.length <= 3) return names.join(", ");
  return `${names.slice(0, 3).join(", ")} +${names.length - 3} more`;
}
