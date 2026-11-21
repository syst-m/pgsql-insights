/* The source switch (fixture vs live) and the trust boundary on a payload.
 *
 * The `parseEnvelope` block here is the regression test for the bug the
 * wire/presentation split exists to fix: `await res.json() as FleetResponse`
 * plus `raw.clusters ?? []` rendered an empty Fleet page against a working API,
 * because a cast checks nothing and `?? []` turned a missing key into a
 * plausible empty state. Both halves are asserted below — the throw, and the
 * fact that the http path now goes through it.
 */

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  FIXTURE_EMPTY,
  FIXTURE_ENVELOPE,
  FIXTURE_FLEET,
  FIXTURE_PARTIAL,
  FIXTURE_UNAVAILABLE,
  FIXTURE_CLUSTERS,
  FIXTURE_GENERATED_AT,
} from "./fixtures";
import { FIXTURES, fetchFleet, resolveConfig, type FixtureName } from "./client";
import { ContractError, emptyEnvelope, parseEnvelope, parseErrorBody, partialBanner, toFleetResponse } from "./envelope";
import type { ClusterView, Envelope, ErrorDetail, FleetResponse } from "./types";

describe("resolveConfig", () => {
  it("defaults to the fixture", () => {
    expect(resolveConfig("", {}).source).toBe("fixture");
  });

  it("honours ?fixture=0 to force the live client", () => {
    expect(resolveConfig("?fixture=0", {}).source).toBe("http");
    expect(resolveConfig("?fixture=false", {}).source).toBe("http");
  });

  it("honours ?fixture=1 to force the fixture", () => {
    expect(resolveConfig("?fixture=1", { VITE_PGSI_FIXTURE: "0" }).source).toBe("fixture");
  });

  it("reads VITE_PGSI_FIXTURE=0 as the build-time switch", () => {
    expect(resolveConfig("", { VITE_PGSI_FIXTURE: "0" }).source).toBe("http");
  });

  it("keeps an API base for the http source and defaults it to same-origin", () => {
    expect(resolveConfig("?fixture=0", { VITE_PGSI_API_BASE: "http://localhost:8080" }).apiBase).toBe(
      "http://localhost:8080",
    );
    expect(resolveConfig("", {}).apiBase).toBe("");
  });

  it("names a fixture in the URL so every R10.12 state has a stable URL", () => {
    const expected: Record<FixtureName, FleetResponse> = {
      fleet: FIXTURE_FLEET,
      empty: FIXTURE_EMPTY,
      partial: FIXTURE_PARTIAL,
      unavailable: FIXTURE_UNAVAILABLE,
    };
    for (const name of ["fleet", "empty", "partial", "unavailable"] as const) {
      const cfg = resolveConfig(`?fixture=${name}`, {});
      expect(cfg.source).toBe("fixture");
      expect(cfg.fixture).toBe(name);
      // The named config resolves to the payload the state panel renders for it.
      expect(FIXTURES[name]).toBe(expected[name]);
    }
  });

  it("ignores an unknown fixture name and falls back to the default", () => {
    expect(resolveConfig("?fixture=banana", {}).fixture).toBe("fleet");
  });

  it("reads ?delay as the fixture's latency, clamped to a sane range", () => {
    expect(resolveConfig("", {}).delayMs).toBe(60);
    expect(resolveConfig("?delay=5000", {}).delayMs).toBe(5000);
    expect(resolveConfig("?delay=-3", {}).delayMs).toBe(60);
    expect(resolveConfig("?delay=999999", {}).delayMs).toBe(60_000);
    expect(resolveConfig("?delay=soon", {}).delayMs).toBe(60);
  });
});

describe("parseEnvelope", () => {
  /** The exact body the old client accepted and rendered as an empty fleet.
   *  It is a valid-looking Phase 1b shape with the Phase 1a payload inside it
   *  mislabelled: `clusters` where the contract says `data`, and `generatedAt`
   *  hoisted to the top level where the contract puts it under `meta`. */
  const preSplit = {
    generatedAt: FIXTURE_GENERATED_AT,
    clusters: FIXTURE_EMPTY.clusters,
  };

  it("throws on the shape the cast used to accept", () => {
    expect(() => parseEnvelope(preSplit)).toThrow(ContractError);
    // The message names the keys that did arrive, which is what turns a blank
    // page into a diagnosable one.
    expect(() => parseEnvelope(preSplit)).toThrow(/keys present: generatedAt, clusters/);
  });

  it("throws rather than defaulting a missing data array to empty", () => {
    // `?? []` was the masking operator. An empty fleet must be an empty
    // `data`, never an absent one.
    expect(() => parseEnvelope({ meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } })).toThrow(
      /Envelope.data is undefined, expected array/,
    );
  });

  it("accepts a bare object with a valid data array and meta", () => {
    const env = parseEnvelope({
      data: [],
      meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT },
    });
    expect(env.data).toEqual([]);
    expect(env.meta.generatedAt).toBe(FIXTURE_GENERATED_AT);
  });

  it("rejects non-objects and arrays at the top level", () => {
    expect(() => parseEnvelope(null)).toThrow(/expected an Envelope object from the API, got null/);
    expect(() => parseEnvelope("[]")).toThrow(/got string/);
    expect(() => parseEnvelope([])).toThrow(/got array/);
  });

  it("checks meta's three required fields and names the one that is wrong", () => {
    const data = { data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } };
    expect(() => parseEnvelope({ ...data, meta: { ...data.meta, total: "0" } })).toThrow(/meta.total is string/);
    expect(() => parseEnvelope({ ...data, meta: { ...data.meta, truncated: 0 } })).toThrow(
      /meta.truncated is number/,
    );
    expect(() => parseEnvelope({ ...data, meta: { ...data.meta, generatedAt: null } })).toThrow(
      /meta.generatedAt is null/,
    );
    expect(() => parseEnvelope({ data: [] })).toThrow(/Envelope.meta is undefined/);
  });

  it("tolerates the omitempty meta fields being absent", () => {
    // nextCursor/failed/loading are `omitempty` in Go; their absence is the
    // normal case and must not be a contract break.
    const env = parseEnvelope({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } });
    expect(env.meta.nextCursor).toBeUndefined();
    expect(env.meta.failed).toBeUndefined();
    expect(env.meta.loading).toBeUndefined();
  });

  it("names the offending row, not just the envelope", () => {
    const rows = [
      {
        namespace: "cookbook",
        name: "cookbook-pg",
        phase: "Cluster in healthy state",
        instances: 1,
        readyInstances: 1,
        servingInstances: 1,
        pgMajor: 17,
        instanceList: [],
        conditions: [],
        certificates: { expirations: {} },
        pvcs: [],
        services: [],
        poolers: [],
        credentialSource: "secret-cluster",
        createdAt: "2026-09-01T00:00:00Z",
      },
      { namespace: "freeidp", name: "freeidp-pg" },
    ];
    expect(() => parseEnvelope({ data: rows, meta: { total: 2, truncated: false, generatedAt: FIXTURE_GENERATED_AT } })).toThrow(
      /cluster freeidp\/freeidp-pg: missing required field "phase"/,
    );
  });

  it("accepts pgMajor null (R1.3) and rejects pgMajor of any other type", () => {
    const base = {
      namespace: "x",
      name: "y",
      phase: "p",
      instances: 0,
      readyInstances: 0,
      servingInstances: 0,
      instanceList: [],
      conditions: [],
      certificates: { expirations: {} },
      pvcs: [],
      services: [],
      poolers: [],
      credentialSource: "secret-cluster",
      createdAt: "2026-09-01T00:00:00Z",
    };
    const meta = { total: 1, truncated: false, generatedAt: FIXTURE_GENERATED_AT };
    expect(() => parseEnvelope({ data: [{ ...base, pgMajor: null }], meta })).not.toThrow();
    expect(() => parseEnvelope({ data: [{ ...base, pgMajor: "17" }], meta })).toThrow(
      /field "pgMajor" is string, expected number or null/,
    );
    expect(() => parseEnvelope({ data: [base], meta })).toThrow(/missing required field "pgMajor"/);
  });

  it("requires certificates.expirations to be an object, since Go never omits it", () => {
    const meta = { total: 1, truncated: false, generatedAt: FIXTURE_GENERATED_AT };
    expect(() =>
      parseEnvelope({
        data: [
          {
            namespace: "x",
            name: "y",
            phase: "p",
            instances: 0,
            readyInstances: 0,
            servingInstances: 0,
            pgMajor: null,
            instanceList: [],
            conditions: [],
            certificates: {},
            pvcs: [],
            services: [],
            poolers: [],
            credentialSource: "secret-cluster",
            createdAt: "2026-09-01T00:00:00Z",
          },
        ],
        meta,
      }),
    ).toThrow(/certificates.expirations is not an object/);
  });
});

describe("parseErrorBody", () => {
  it("reads an R8.5 body", () => {
    const detail = parseErrorBody({ error: { code: "NotReady", message: "discovery has not synced", details: [] } });
    expect(detail).toEqual({ code: "NotReady", message: "discovery has not synced", details: [] } as ErrorDetail);
  });

  it("returns null for a body that is not an error, so the caller falls back to the status line", () => {
    expect(parseErrorBody({ data: [], meta: {} })).toBeNull();
    expect(parseErrorBody(null)).toBeNull();
    expect(parseErrorBody({ error: "NotReady" })).toBeNull();
  });

  it("defaults a missing details to [] rather than branching on it", () => {
    // NewError normalises a nil slice to `[]`; a body without it did not come
    // from NewError, and the client must not have to test for the key.
    const detail = parseErrorBody({ error: { code: "Internal", message: "boom" } });
    expect(detail?.details).toEqual([]);
  });
});

describe("toFleetResponse", () => {
  it("derives health and observedAt onto every row", () => {
    const env: Envelope<ClusterView> = {
      data: [
        {
          namespace: "x",
          name: "y",
          phase: "Cluster in healthy state",
          instances: 2,
          readyInstances: 2,
          servingInstances: 2,
          pgMajor: 17,
          instanceList: [],
          conditions: [],
          certificates: { expirations: {} },
          pvcs: [],
          services: [],
          poolers: [],
          credentialSource: "secret-cluster",
          createdAt: "2026-09-01T00:00:00Z",
        },
      ],
      meta: { total: 1, truncated: false, generatedAt: FIXTURE_GENERATED_AT },
    };
    const out = toFleetResponse(env);
    expect(out.meta).toBe(env.meta);
    expect(out.clusters[0]?.health).toBe("healthy");
    // R10.21's per-response stamp, denormalised onto the row the table sorts on.
    expect(out.clusters[0]?.observedAt).toBe(FIXTURE_GENERATED_AT);
    // Phase 1a sends no metrics, so the live path leaves the extras absent
    // rather than inventing them.
    expect(out.clusters[0]?.metrics).toBeUndefined();
    expect(out.clusters[0]?.topFinding).toBeUndefined();
  });

  it("spreads the presentation extras over the wire fields without clobbering them", () => {
    const out = toFleetResponse(parseEnvelope({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } }));
    expect(out.clusters).toEqual([]);
    // The fixture is the case with extras; every one of its rows keeps its own
    // wire fields after the spread.
    for (const row of FIXTURE_FLEET.clusters) {
      expect(row.namespace).toBeDefined();
      expect(row.name).toBeDefined();
      expect(row.metrics).toBeDefined();
    }
  });
});

describe("partialBanner", () => {
  it("is null for a full render, even when truncated", () => {
    // R8.10's `truncated` is a paging affordance, not a warning.
    expect(partialBanner({ total: 4, truncated: false, generatedAt: FIXTURE_GENERATED_AT })).toBeNull();
    expect(partialBanner({ total: 4, truncated: true, generatedAt: FIXTURE_GENERATED_AT, nextCursor: "abc" })).toBeNull();
  });

  it("names a loading namespace (R10.22)", () => {
    expect(
      partialBanner({ total: 3, truncated: false, generatedAt: FIXTURE_GENERATED_AT, loading: ["freeidp"] }),
    ).toBe("3 clusters served. 1 namespace still in the first discovery pass (freeidp).");
  });

  it("names a failed namespace with its code and staleness", () => {
    const banner = partialBanner({
      total: 3,
      truncated: true,
      generatedAt: FIXTURE_GENERATED_AT,
      nextCursor: "eyJv",
      failed: [
        {
          namespace: "cookbook",
          code: "Forbidden",
          message: "cannot list pods",
          lastSuccessTime: "2026-11-19T09:00:00Z",
        },
      ],
    });
    expect(banner).toContain("more pages available");
    expect(banner).toContain("cookbook (Forbidden, stale since 2026-11-19T09:00:00Z)");
  });

  it("truncates a long namespace list instead of wrapping the banner", () => {
    const banner = partialBanner({
      total: 0,
      truncated: false,
      generatedAt: FIXTURE_GENERATED_AT,
      loading: ["a", "b", "c", "d", "e"],
    });
    expect(banner).toContain("(a, b, c +2 more)");
  });

  it("is what StatePanel keys the partial state off", () => {
    expect(partialBanner(FIXTURE_PARTIAL.meta)).not.toBeNull();
    expect(partialBanner(FIXTURE_FLEET.meta)).toBeNull();
  });
});

describe("fetchFleet over http", () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
    vi.restoreAllMocks();
  });

  /** Stand in for the network with a body the client will see. The cast is on
   *  the Response, not on the payload: `fetch`'s return type demands the whole
   *  `Response` interface (headers, url, clone, …) and the client reads only
   *  `ok`, `status`, `statusText` and `json()`. The thing the last commit got
   *  wrong was casting *data* — see the ContractError case below. */
  function stub(json: unknown, ok = true, status = 200, statusText = "OK") {
    const res = { ok, status, statusText, json: async () => json } as unknown as Response;
    globalThis.fetch = vi.fn(async () => res);
  }

  const http = { source: "http", apiBase: "", fixture: "fleet", delayMs: 0 } as const;

  it("GETs the contract path on the same origin", async () => {
    stub({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } });
    await fetchFleet(http);
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe("/api/v1/clusters");
  });

  it("strips a trailing slash from a configured base", async () => {
    stub({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } });
    await fetchFleet({ ...http, apiBase: "http://localhost:8080/" });
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe("http://localhost:8080/api/v1/clusters");
  });

  it("throws ContractError on a 200 whose body is not an envelope", async () => {
    // The end-to-end form of the bug: 200 OK, parseable JSON, no `data`.
    stub({ generatedAt: FIXTURE_GENERATED_AT, clusters: [] });
    await expect(fetchFleet(http)).rejects.toThrow(ContractError);
  });

  it("returns the parsed envelope for a valid 200", async () => {
    stub({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } });
    const out = await fetchFleet(http);
    expect(out.meta.generatedAt).toBe(FIXTURE_GENERATED_AT);
    expect(out.clusters).toEqual([]);
  });

  it("raises an ApiError carrying the R8.5 code on a non-2xx", async () => {
    stub({ error: { code: "Forbidden", message: "cannot list namespaces", details: ["ns/x"] } }, false, 403, "Forbidden");
    const err = await fetchFleet(http).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(Error);
    expect((err as Error).message).toBe("GET → 403 Forbidden: cannot list namespaces — ns/x");
    expect((err as { code?: string }).code).toBe("Forbidden");
  });

  it("falls back to the status line when an error body is not an R8.5 body", async () => {
    stub("<html>502 Bad Gateway</html>", false, 502, "Bad Gateway");
    const err = await fetchFleet(http).catch((e: unknown) => e);
    expect((err as Error).message).toBe("GET → 502 Bad Gateway");
    expect((err as { code?: string }).code).toBeUndefined();
  });
});

describe("the fixture payload", () => {
  it("is served with the four clusters the contract promises", () => {
    expect(FIXTURE_FLEET.clusters).toHaveLength(FIXTURE_CLUSTERS.length);
    // Read from testdata/api/clusters.json, not invented here.
    expect(FIXTURE_FLEET.meta.generatedAt).toBe("2026-11-20T09:12:00Z");
    expect(FIXTURE_GENERATED_AT).toBe("2026-11-20T09:12:00Z");
  });

  it("parses the contract fixture through the same path the live client uses", () => {
    // R16.8: one copy of the contract. fixtures.ts imports the JSON and hands
    // it to parseEnvelope, so the fixture cannot be shaped differently from
    // the API and still pass. This is the assertion that would have failed
    // before the split: FIXTURE_FLEET's own shape ({meta, clusters}) is what
    // the client used to believe the endpoint sent, and parseEnvelope rejects it.
    const reparsed = toFleetResponse(parseEnvelope(FIXTURE_ENVELOPE), {});
    expect(reparsed.clusters.map((r) => `${r.namespace}/${r.name}`)).toEqual(
      FIXTURE_CLUSTERS.map((r) => `${r.namespace}/${r.name}`),
    );
    expect(() => parseEnvelope(FIXTURE_FLEET)).toThrow(/keys present: meta, clusters/);
  });

  it("marks every cluster's metrics as unconfigured or failed, never invented", () => {
    // Phase 1b ships discovery only: no cluster may show a made-up TPS number.
    for (const c of FIXTURE_CLUSTERS) {
      expect(c.metrics).not.toBeNull();
      expect(["unconfigured", "failed"]).toContain(c.metrics?.source);
      expect(c.metrics?.tps).toBeNull();
      expect(c.metrics?.p95LatencyMs).toBeNull();
    }
  });

  it("does not mutate the parsed envelope", () => {
    // The rows the page renders are spreads, not the parsed objects, so the
    // shared fixture object stays byte-identical no matter how many pages read
    // it in one test run.
    const before = JSON.stringify(FIXTURE_ENVELOPE);
    toFleetResponse(parseEnvelope(FIXTURE_ENVELOPE));
    expect(JSON.stringify(FIXTURE_ENVELOPE)).toBe(before);
  });

  it("emptyEnvelope is the empty fixture's envelope", () => {
    expect(emptyEnvelope(FIXTURE_GENERATED_AT).data).toEqual([]);
    expect(FIXTURE_EMPTY.clusters).toEqual([]);
  });
});
