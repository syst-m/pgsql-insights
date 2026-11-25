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
  FIXTURE_ACTIVE_QUERIES,
  FIXTURE_EMPTY,
  FIXTURE_ENVELOPE,
  FIXTURE_FLEET,
  FIXTURE_QUERY_DETAIL,
  FIXTURE_QUERY_SERIES,
  FIXTURE_QUERIES,
  FIXTURE_PARTIAL,
  FIXTURE_TRUNCATED_PARTIAL,
  FIXTURE_UNAVAILABLE,
  FIXTURE_CLUSTERS,
  FIXTURE_GENERATED_AT,
  FIXTURE_TAGS,
  FIXTURE_TERMINATE_SESSION,
} from "./fixtures";
import {
  ApiError,
  FIXTURES,
  fetchActiveQueries,
  fetchFleet,
  fetchQueries,
  fetchQueryDetail,
  fetchQuerySeries,
  fetchTags,
  resolveConfig,
  terminateSession,
  type FixtureName,
} from "./client";
import {
  CLUSTER_NOUN,
  ContractError,
  emptyEnvelope,
  parseActiveQueriesResponse,
  parseEnvelope,
  parseErrorBody,
  parseQuerySeries,
  parseSessionView,
  parseTagView,
  parseTerminateResponse,
  partialBanner,
  QUERY_NOUN,
  SESSION_NOUN,
  TAG_NOUN,
  toFleetResponse,
  toQueriesResponse,
} from "./envelope";
import type { ClusterView, Envelope, ErrorDetail, FleetResponse, Meta, QuerySeries } from "./types";
import OPENAPI_YAML from "../../../api/openapi.yaml?raw";
import * as api from "./client";

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
      "truncated-partial": FIXTURE_TRUNCATED_PARTIAL,
      unavailable: FIXTURE_UNAVAILABLE,
    };
    for (const name of ["fleet", "empty", "partial", "truncated-partial", "unavailable"] as const) {
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

describe("parseQuerySeries", () => {
  /** `testdata/api/query-series.json`'s first entry, via `FIXTURE_QUERY_SERIES`.
   *
   *  This block used to carry its own copy of the object. The inlining was a known
   *  gap rather than a choice — `4cef345` shipped the route with `series.go`, its
   *  Go test, the openapi entry and the handler, and left `query-series.json` out,
   *  so the test read the document's example because there was no fixture to read
   *  — and the literal's own comment named this file as the fix and promised that
   *  when the fixture landed the assertions below would not change, only where the
   *  object came from. They did not change, with one exception worth naming: two of
   *  them hardcoded the old fingerprint (see `PREFIX`). A1's `8b046ef` added the
   *  JSON and the Go test that guards it, `fixtures.ts:109` is the TS half, and
   *  `18ae97d` made the openapi example that file's bytes — so the example, the
   *  fixture and this test are one object now, which is the whole point of R16.8.
   *
   *  Entry 0 is the entry with every `omitempty` label present and two points,
   *  which is the shape the variant cases below mutate. The fixture's other two
   *  entries are the no-labels and `points: []` shapes, so importing the file
   *  covers all three where the inlined example covered one. */
  const EXAMPLE = FIXTURE_QUERY_SERIES.data[0]!;

  /** The row-identifying prefix, derived rather than spelled out.
   *
   *  `envelope.ts:357` builds it as `raw.fingerprint.slice(0, 12)`. Two of the
   *  cases below hardcoded `9f86d081884c` — the openapi example's fingerprint —
   *  so the moment the object came from the fixture instead, those two went red
   *  while every other assertion in the block stayed green. Deriving it from
   *  `EXAMPLE` deletes the second copy instead of re-syncing it (docs/agents.md §5
   *  rule 10), so the pair cannot drift again; a fingerprint is hex, so
   *  interpolating one into a `RegExp` needs no escaping. */
  const PREFIX = EXAMPLE.fingerprint.slice(0, 12);

  it("parses the published example", () => {
    const parsed = parseQuerySeries(EXAMPLE);
    // The parser validates and returns; it does not normalise, so the row that
    // comes back is the row that went in (the same property
    // parseClusterView/parseQueryView have).
    expect(parsed).toEqual(EXAMPLE);
    expect(parsed.points).toHaveLength(2);
  });

  it("parses through parseEnvelope's generic overload", () => {
    // The call form the overload pair exists to allow: `parseEnvelope(json)`
    // alone would type this payload's rows as ClusterView and the Detail chart
    // would not compile against its own data. The annotation is the assertion —
    // a regression in the pair shows up as TS2322 here, not as a runtime pass.
    const env: Envelope<QuerySeries> = parseEnvelope(
      { data: [EXAMPLE], meta: { total: 1, truncated: false, generatedAt: "2026-11-21T09:12:03Z" } },
      parseQuerySeries,
    );
    expect(env.data[0]?.fingerprint).toBe(EXAMPLE.fingerprint);
    expect(env.meta.total).toBe(1);
  });

  it("rejects a missing fingerprint, naming the row by its type", () => {
    const { fingerprint, ...rest } = EXAMPLE;
    expect(fingerprint).toBeDefined();
    // `where` is built *from* the fingerprint, so a row missing that field is
    // named by the typeName fallback — the same behaviour parseClusterView has
    // for a cluster with no name. Asserted rather than assumed, because the
    // alternative reading of this case is "the parser can't identify the row".
    expect(() => parseQuerySeries(rest)).toThrow(
      /query-series object: missing required field "fingerprint"/,
    );
  });

  it("names the fingerprint in the message for every other missing field", () => {
    // The row-identification rule that matters in a multi-entry payload: with
    // seven entries in `data`, "missing required field" alone does not say which.
    const { namespace, ...rest } = EXAMPLE;
    expect(namespace).toBeDefined();
    expect(() => parseQuerySeries(rest)).toThrow(
      new RegExp(`query-series ${PREFIX}: missing required field "namespace"`),
    );
  });

  it("rejects points:null, which Go never emits", () => {
    // `points` has no `omitempty` (series.go:152), so a fingerprint with no
    // samples serialises as `"points": []`. A null is not that: it is the
    // `?? []`-style lie this module exists to refuse.
    expect(() => parseQuerySeries({ ...EXAMPLE, points: null })).toThrow(
      /field "points" is null, expected array/,
    );
  });

  it("rejects a missing points array", () => {
    const { points, ...rest } = EXAMPLE;
    expect(points).toBeDefined();
    expect(() => parseQuerySeries(rest)).toThrow(/missing required field "points"/);
  });

  it("checks inside the array, and names the owning fingerprint in the message", () => {
    // The case the sibling parsers skip on purpose and this one does not: the
    // array *is* the payload here, so a short point is a hole in the chart.
    const { lockTimeSeconds, ...short } = EXAMPLE.points[0]!;
    expect(lockTimeSeconds).toBeDefined();
    expect(() => parseQuerySeries({ ...EXAMPLE, points: [short] })).toThrow(
      new RegExp(`query ${PREFIX}: points\\[0\\] missing required field "lockTimeSeconds"`),
    );
  });

  it("rejects a numeric field served as a string", () => {
    // The openapi example writes `rows: 1200`; a server that stringifies it —
    // as JSON-from-Prometheus does for sample values — is a contract break, not
    // something to `Number()` into plausibility.
    expect(() =>
      parseQuerySeries({ ...EXAMPLE, points: [{ ...EXAMPLE.points[0]!, rows: "1200" }] }),
    ).toThrow(/points\[0\]\.rows is string, expected number/);
  });

  it("accepts null in the two nullable slots, which is what R8.3's absence looks like", () => {
    // The case the fixture cannot express, and the reason this test exists apart
    // from the ones above: `testdata/api/query-series.json` has zero nulls
    // (`grep -c null` → `0`), so every other assertion in this block runs on a
    // payload where both nullable fields carry values. R8.3's ruling at
    // `7cecc49` (A1's `d0ea2c4`) made `lockTimeSeconds` and `ioTimeSeconds`
    // `*float64` — `dto/series.go:75-76`, `nullable: true` at
    // `api/openapi.yaml:2958` and `:2968` — so a fingerprint with no lock waits
    // answers with a null, and until this commit that response failed the parse:
    // `typeof null === "object"`, which is not `"number"`.
    const point = { ...EXAMPLE.points[0]!, lockTimeSeconds: null, ioTimeSeconds: null };
    expect(() => parseQuerySeries({ ...EXAMPLE, points: [point] })).not.toThrow();
  });

  it("checks ioTimeSeconds, the field this parser never checked", () => {
    // `dto/series.go:69-76` declares seven fields and `api/openapi.yaml:2930-2937`
    // requires seven; the parser checked six, and `ioTimeSeconds` was the
    // missing one (A2 filed it). Before this commit a point with no
    // `ioTimeSeconds` key parsed, and the returned row asserted a field its own
    // type says is there — the sibling of the `lockTimeSeconds` case above,
    // which is the only one of the two that used to be checked.
    const { ioTimeSeconds, ...rest } = EXAMPLE.points[0]!;
    expect(ioTimeSeconds).toBeDefined();
    expect(() => parseQuerySeries({ ...EXAMPLE, points: [rest] })).toThrow(
      new RegExp(`query ${PREFIX}: points\\[0\\] missing required field "ioTimeSeconds"`),
    );
  });

  it("still rejects null in a non-nullable slot", () => {
    // The half that keeps the parser a parser: teaching two slots to accept null
    // must not teach the other four. `executionTimeSeconds` is a plain `float64`
    // (`dto/series.go:71`), so a null there is a contract break, not an absence —
    // and the message says `expected number`, with no `or null`.
    expect(() =>
      parseQuerySeries({
        ...EXAMPLE,
        points: [{ ...EXAMPLE.points[0]!, executionTimeSeconds: null }],
      }),
    ).toThrow(/points\[0\]\.executionTimeSeconds is null, expected number/);
  });

  it("names the union in the message when a nullable field has the wrong type", () => {
    // `expected number or null`, not `expected number`: the message is the only
    // place a caller learns which kind of slot rejected the value, and the
    // nullable entry point has to say so or the two entry points are
    // indistinguishable in a stack trace.
    expect(() =>
      parseQuerySeries({
        ...EXAMPLE,
        points: [{ ...EXAMPLE.points[0]!, lockTimeSeconds: "0.0031" }],
      }),
    ).toThrow(/points\[0\]\.lockTimeSeconds is string, expected number or null/);
  });

  it("accepts the cluster-aggregated answer, with all five omitempty labels absent", () => {
    // The non-obvious legal shape, and the one the Detail chart actually asks
    // for: QueryDetailView's chart has no pod (dto/queries.go:11-34 points it at
    // this route for the fingerprint's series across the cluster), so the answer
    // carries no `pod`, no `instanceRole`, and — for a multi-database cluster —
    // no `datname`/`usename` either. A parser that demanded the label fields
    // would throw on the response the chart is documented to receive.
    expect(() =>
      parseQuerySeries({
        fingerprint: EXAMPLE.fingerprint,
        namespace: EXAMPLE.namespace,
        cluster: EXAMPLE.cluster,
        points: [],
      }),
    ).not.toThrow();
  });

  it("rejects an instanceRole outside the published enum", () => {
    // `unhealthy` is a member of the *clusters* union (InstanceRole, R1.2's word
    // for a pod up with postgres not running) and not of this one
    // (`enum: [primary, replica]`): a sample belongs to an instance that was
    // primary or replica at sample time. Accepting it in TS would let a value
    // the document forbids through a green build.
    expect(() => parseQuerySeries({ ...EXAMPLE, instanceRole: "unhealthy" })).toThrow(
      /field "instanceRole" is "unhealthy", expected "primary" or "replica"/,
    );
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
  /** Build an envelope serving `rows` of the four reference clusters with
   *  `meta` around them.
   *
   *  The signature takes a response, not a `Meta`, and this helper is why the
   *  test had to change with it: passing bare `{ total: 3, ... }` objects is
   *  what let the bug hide, because `meta.total` was the only count in reach.
   *  Constructing a real `Envelope<ClusterView>` forces every case to say how
   *  many rows it served *and* what `total` claims, so the two can disagree. */
  const env = (rows: number, meta: Omit<Meta, "generatedAt">): Envelope<ClusterView> => ({
    data: FIXTURE_ENVELOPE.data.slice(0, rows),
    meta: { ...meta, generatedAt: FIXTURE_GENERATED_AT },
  });

  it("is null for a full render, even when truncated", () => {
    // R8.10's `truncated` is a paging affordance, not a warning.
    expect(partialBanner(FIXTURE_ENVELOPE)).toBeNull();
    expect(partialBanner(env(4, { total: 4, truncated: true, nextCursor: "abc" }))).toBeNull();
  });

  it("names a loading namespace (R10.22)", () => {
    expect(partialBanner(env(3, { total: 3, truncated: false, loading: ["freeidp"] }))).toBe(
      "3 clusters served. 1 namespace still in the first discovery pass (freeidp).",
    );
  });

  it("names a failed namespace with its code and staleness", () => {
    const banner = partialBanner(
      env(3, {
        total: 3,
        truncated: true,
        nextCursor: "eyJv",
        failed: [
          {
            namespace: "cookbook",
            code: "Forbidden",
            message: "cannot list pods",
            lastSuccessTime: "2026-11-19T09:00:00Z",
          },
        ],
      }),
    );
    expect(banner).toContain("3 clusters served, more pages available");
    expect(banner).toContain("cookbook (Forbidden, stale since 2026-11-19T09:00:00Z)");
  });

  it("counts served rows, not meta.total, on a truncated page (R10.22)", () => {
    // The regression A3 found. R8.4's corrected wording makes `meta.total` the
    // pre-cut count — matching rows across *all* pages — so with 4 matching and
    // a 2-row page, a banner built from `total` announces "4 clusters served"
    // above a table rendering 2. R10.22 names the fix: `data.length`, never
    // `meta.total`.
    const truncatedPartial = env(2, {
      total: 4,
      truncated: true,
      nextCursor: "eyJvZmZzZXQiOjJ9",
      loading: ["freeidp", "open-webui"],
    });
    const banner = partialBanner(truncatedPartial);
    expect(banner).toBe(
      "2 clusters served, more pages available. 2 namespaces still in the first discovery pass (freeidp, open-webui).",
    );
    expect(banner).not.toContain("4 clusters served");

    // The same number through the other arm of BannerSource: the banner reads
    // `clusters.length` off the presentation type, which toFleetResponse maps
    // 1:1 from `data`, so the two arms cannot drift apart.
    expect(partialBanner(toFleetResponse(truncatedPartial))).toBe(banner);
    expect(partialBanner(FIXTURE_TRUNCATED_PARTIAL)).toBe(banner);
  });

  it("counts the subject the caller names, in both numbers", () => {
    // The sentence hardcoded "cluster(s)", so the Queries and Active Queries
    // pages both announced clusters above a table of something else (A6's
    // finding on `99f37f0`). The *pair* form is what this case exists to pin:
    // `QUERY_NOUN` has to yield "1 query" and "9 queries", and a one-string
    // parameter with an `s` appended renders "1 querys" — which a test that only
    // checked the plural would pass by accident.
    expect(partialBanner(env(1, { total: 1, truncated: false, loading: ["cookbook"] }), QUERY_NOUN)).toBe(
      "1 query served. 1 namespace still in the first discovery pass (cookbook).",
    );
    expect(partialBanner(env(3, { total: 3, truncated: false, loading: ["cookbook"] }), QUERY_NOUN)).toBe(
      "3 queries served. 1 namespace still in the first discovery pass (cookbook).",
    );
    expect(
      partialBanner(
        env(3, { total: 3, truncated: true, nextCursor: "eyJv", loading: ["cookbook"] }),
        SESSION_NOUN,
      ),
    ).toBe(
      "3 sessions served, more pages available. 1 namespace still in the first discovery pass (cookbook).",
    );
    // `TAG_NOUN` is the fourth entry and the case R10.22 names for a new noun:
    // both numbers, because a one-string parameter that appends `s` renders
    // "1 tags" and a plural-only check passes that by accident.
    expect(partialBanner(env(1, { total: 1, truncated: false, loading: ["cookbook"] }), TAG_NOUN)).toBe(
      "1 tag served. 1 namespace still in the first discovery pass (cookbook).",
    );
    // 4, not 6: `env` slices `FIXTURE_ENVELOPE.data`, which has four rows, so
    // `env(6, …)` serves four and the banner says four — the assertion would
    // have caught a plural-only check either way, but a row count the fixture
    // cannot supply tests the builder, not the noun.
    expect(partialBanner(env(4, { total: 4, truncated: false, loading: ["cookbook"] }), TAG_NOUN)).toBe(
      "4 tags served. 1 namespace still in the first discovery pass (cookbook).",
    );
    // Omitting the argument is byte-identical to passing `CLUSTER_NOUN`, which is
    // the guarantee `Fleet.tsx` and every pre-existing case above rely on.
    const m: Omit<Meta, "generatedAt"> = { total: 3, truncated: false, loading: ["cookbook"] };
    expect(partialBanner(env(3, m))).toBe(partialBanner(env(3, m), CLUSTER_NOUN));
  });

  it("truncates a long namespace list instead of wrapping the banner", () => {
    const banner = partialBanner(env(0, { total: 5, truncated: false, loading: ["a", "b", "c", "d", "e"] }));
    expect(banner).toContain("0 clusters served");
    expect(banner).toContain("(a, b, c +2 more)");
  });

  it("is what StatePanel keys the partial state off", () => {
    expect(partialBanner(FIXTURE_PARTIAL)).not.toBeNull();
    expect(partialBanner(FIXTURE_TRUNCATED_PARTIAL)).not.toBeNull();
    expect(partialBanner(FIXTURE_FLEET)).toBeNull();
  });
});

/* The network stand-in and the two ClientConfigs, shared by every fetcher block.
 *
 * These lived inside `describe("fetchFleet over http")` while that was the only
 * block with an http arm. `fetchQueries`/`fetchQueryDetail`/`fetchQuerySeries`
 * need the same two things, and a `stub` per describe is four copies of the one
 * line every http assertion in this file depends on — the same
 * two-copies-of-a-fact problem the fixture import exists to remove on the data
 * side. The cast is on the Response, not on the payload: `fetch`'s return type
 * demands the whole `Response` interface (headers, url, clone, …) and the client
 * reads only `ok`, `status`, `statusText` and the body. The bug this file exists
 * for was casting *data* — see the ContractError cases.
 *
 * It holds the bytes and derives both accessors from them, which is what a
 * Response is. Stubbing `json()` with the object itself — the form this had —
 * let a test pass on a body the client could never have produced by parsing: the
 * 502 test passed `json: async () => "<html>…"` to a client that called
 * `res.json()`, so the parse succeeded by never running, and the SyntaxError a
 * real proxy would have caused stayed invisible behind a green suite.
 *
 * `delayMs: 0` on both configs: the fixture arm's latency is `resolveConfig`'s
 * business (it parses `?delay=`), and a suite that awaited 60ms per fetch would
 * be 60ms of waiting for nothing. */
const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

function stub(body: unknown, ok = true, status = 200, statusText = "OK") {
  const text = typeof body === "string" ? body : JSON.stringify(body);
  const res = {
    ok,
    status,
    statusText,
    text: async () => text,
    json: async () => JSON.parse(text) as unknown,
  } as unknown as Response;
  globalThis.fetch = vi.fn(async () => res);
}

const http = { source: "http", apiBase: "", fixture: "fleet", delayMs: 0 } as const;
const fixture = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 } as const;

describe("fetchFleet over http", () => {
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

  it("is an ApiError when the error body is not JSON at all", async () => {
    // A proxy in front of the binary answers a 502 with an HTML page. That used
    // to reject inside `res.json()`, before the `ok` branch could run, so the
    // caller got `SyntaxError: Unexpected token '<'` and lost the ApiError shape
    // the error panel is built on — the error surface depended on the body being
    // parseable. The stub used to hand back `json: async () => "<html>…"`, which
    // parses because it never runs; this one holds the bytes and fails the way a
    // real Response fails.
    stub("<html>502 Bad Gateway</html>", false, 502, "Bad Gateway");
    const err = await fetchFleet(http).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as Error).message).toBe("GET → 502 Bad Gateway");
    expect((err as { code?: string }).code).toBeUndefined();
  });

  it("falls back to the status line when a parseable error body is not an R8.5 body", async () => {
    // The other half of `describeFailure`: JSON that parses but carries no
    // `error` key — a gateway with its own JSON error shape. The parse succeeds,
    // `parseErrorBody` returns null, and the message is the status line alone.
    stub({ status: "degraded" }, false, 503, "Service Unavailable");
    const err = await fetchFleet(http).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as Error).message).toBe("GET → 503 Service Unavailable");
    expect((err as { code?: string }).code).toBeUndefined();
  });

  it("is a ContractError when a 200 body is not JSON", async () => {
    // The success arm's half of the same rule. `parseJson` reports the
    // unparseable body as `undefined`, and the `!res.ok` branch cannot claim it —
    // the request succeeded — so the 200 arm has to turn it into an error rather
    // than return it as a value, which is what a cast used to do with it.
    stub("<html>ok</html>");
    const err = await fetchFleet(http).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ContractError);
    expect((err as Error).message).toBe("GET /api/v1/clusters → 200 body is not JSON (<html>ok</html>)");
  });

  it("truncates a long non-JSON 200 body to one line in the message", async () => {
    // `preview` exists so the message can quote a body without pasting a whole
    // HTML page into the error panel. The test above covers the short body; this
    // covers the long one, because a suite with only the short case passes with a
    // `preview` that never truncated anything.
    const body = `<html><head><title>502 Bad Gateway</title></head><body>${"x".repeat(200)}\nmore`;
    stub(body);
    const err = await fetchFleet(http).catch((e: unknown) => e);
    expect((err as Error).message).toBe(
      `GET /api/v1/clusters → 200 body is not JSON (${body.slice(0, 80)}…)`,
    );
  });
});

describe("fetchQueries", () => {
  const empty = { data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } };

  it("GETs the contract path with the params it was given", async () => {
    stub(empty);
    await fetchQueries(http, { namespace: "open-webui", cluster: "open-webui-pg", limit: 50 });
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe(
      "/api/v1/insights/queries?namespace=open-webui&cluster=open-webui-pg&limit=50",
    );
  });

  it("leaves an unset param out of the query string instead of sending it empty", async () => {
    // `?db=` asks the server about the empty-string database; a URL with no `db`
    // asks about no database at all. Every R8.4 param is optional, so an unset
    // one has to be absent — the case `fetchFleet` never had to get right because
    // it takes no params, and the reason `search()` filters undefined rather
    // than handing it to URLSearchParams.
    stub(empty);
    await fetchQueries(http, { namespace: "open-webui" });
    const url = String(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]);
    expect(url).toBe("/api/v1/insights/queries?namespace=open-webui");
    expect(url).not.toContain("db=");
    expect(url).not.toContain("cursor=");
  });

  it("sends no query string at all when every param is unset", async () => {
    stub(empty);
    await fetchQueries(http);
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe("/api/v1/insights/queries");
  });

  it("passes a limit above the maximum through instead of clamping it (R8.10)", async () => {
    // R8.10 fixes the max at 500 and rules that above it the answer is
    // InvalidRequest, not a silent clamp — "a client that asks for 5000 learns
    // about it instead of getting 500". A client that clamped would never see
    // that error and would silently disagree with the server about the page it
    // asked for, which is the same lie as defaulting a missing key to `[]`.
    stub(empty);
    await fetchQueries(http, { limit: 5000 });
    expect(String(vi.mocked(globalThis.fetch).mock.calls[0]?.[0])).toContain("limit=5000");
  });

  it("returns the parsed envelope for a valid 200", async () => {
    stub(FIXTURE_QUERIES);
    const out = await fetchQueries(http);
    // The same builder the fixture arm returns, so the two arms cannot disagree
    // about the shape the page receives.
    expect(out).toEqual(toQueriesResponse(FIXTURE_QUERIES));
  });

  it("throws ContractError on a 200 whose body is not an envelope", async () => {
    stub({ generatedAt: FIXTURE_GENERATED_AT, queries: [] });
    await expect(fetchQueries(http)).rejects.toThrow(ContractError);
  });

  it("serves the queries fixture on the fixture arm", async () => {
    await expect(fetchQueries(fixture)).resolves.toEqual(toQueriesResponse(FIXTURE_QUERIES));
  });
});

describe("fetchQueryDetail", () => {
  const params = {
    fingerprint: FIXTURE_QUERY_DETAIL.fingerprint,
    namespace: FIXTURE_QUERY_DETAIL.namespace,
    cluster: FIXTURE_QUERY_DETAIL.cluster,
  };

  it("puts the fingerprint in the path and the scope in the query string", async () => {
    stub(FIXTURE_QUERY_DETAIL);
    await fetchQueryDetail(http, params);
    // All three fields are required here and only two of them are query params:
    // R3.4 makes a fingerprint canonical *within* a cluster, so namespace+cluster
    // are half of the key and the route makes them required, while the list route
    // — where they filter rather than identify — leaves them optional.
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe(
      `/api/v1/insights/queries/${FIXTURE_QUERY_DETAIL.fingerprint}` +
        `?namespace=${FIXTURE_QUERY_DETAIL.namespace}&cluster=${FIXTURE_QUERY_DETAIL.cluster}`,
    );
  });

  it("percent-encodes a fingerprint that needs it", async () => {
    // `{fingerprint}` is a path segment, so a value with a space or a `%` has to
    // be encoded — the same encoding routes.ts applies when it builds the URL
    // the page is at, so both sides address the same row.
    stub(FIXTURE_QUERY_DETAIL);
    await fetchQueryDetail(http, { fingerprint: "0x84f2 a", namespace: "n", cluster: "c" });
    expect(String(vi.mocked(globalThis.fetch).mock.calls[0]?.[0])).toBe(
      "/api/v1/insights/queries/0x84f2%20a?namespace=n&cluster=c",
    );
  });

  it("returns the bare object the route serves, not an envelope", async () => {
    // The fourth payload's 200 is `QueryDetailView` itself — openapi's 200
    // schema is the object, not `Envelope[…]` — so this is the one fetcher with
    // no parseEnvelope call, and the reason `parseQueryDetailView` exists is
    // that the alternative here was `json as QueryDetailView`: the cast this
    // module was extracted to remove.
    stub(FIXTURE_QUERY_DETAIL);
    await expect(fetchQueryDetail(http, params)).resolves.toEqual(FIXTURE_QUERY_DETAIL);
  });

  it("rejects an envelope body, which is the shape the other three routes serve", async () => {
    // The assertion that pins the shape rather than the parse: hand this fetcher a
    // valid envelope — what `/clusters` and `/insights/queries` return — and it
    // has to fail. A fetcher that reached for `parseEnvelope` by mistake passes
    // the test above and fails this one.
    stub({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } });
    await expect(fetchQueryDetail(http, params)).rejects.toThrow(ContractError);
  });

  it("raises ApiError carrying the R8.5 code on a 404", async () => {
    // Until Phase 3's store is assigned to Config.QueryDetail this route answers
    // 404 NotFound for every fingerprint, so it is the response the page actually
    // gets today — and the code is what separates "no such query" from the
    // NotReady a page recovers from by waiting.
    stub({ error: { code: "NotFound", message: "no such query", details: [] } }, false, 404, "Not Found");
    const err = await fetchQueryDetail(http, params).catch((e: unknown) => e);
    expect((err as Error).message).toBe("GET → 404 NotFound: no such query");
    expect((err as { code?: string }).code).toBe("NotFound");
  });

  it("serves the detail fixture on the fixture arm", async () => {
    await expect(fetchQueryDetail(fixture, params)).resolves.toEqual(FIXTURE_QUERY_DETAIL);
  });
});

describe("fetchQuerySeries", () => {
  const all = FIXTURE_QUERY_SERIES.data.map((s) => s.fingerprint);
  const empty = { data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } };

  it("joins the batch into one fingerprints param", async () => {
    stub(FIXTURE_QUERY_SERIES);
    await fetchQuerySeries(http, { fingerprints: all, range: "1h" });
    // R8.11's wire form is one comma-separated `fingerprints=`, not repeated
    // keys — the form `series.go`'s parser documents. URLSearchParams percent-
    // encodes the commas to %2C, which the server decodes back; asserting the
    // encoded form is what pins the joining rather than re-deriving it.
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe(
      `/api/v1/insights/queries/series?fingerprints=${all.map((f) => encodeURIComponent(f)).join("%2C")}&range=1h`,
    );
  });

  it("omits range when the caller leaves it unset, so the server's 1h default applies", async () => {
    // An empty `range=` is not the same as no `range=`: the server's default is
    // `1h` (series.go's DefaultRange) and its parser rejects a value it cannot
    // name, which an empty string is.
    stub(FIXTURE_QUERY_SERIES);
    await fetchQuerySeries(http, { fingerprints: all });
    const url = String(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]);
    expect(url.startsWith("/api/v1/insights/queries/series?fingerprints=")).toBe(true);
    expect(url).not.toContain("range=");
  });

  it("sends a batch above the cap without truncating it", async () => {
    // `series.go` caps the batch at MaxBatchFingerprints (50) and answers
    // InvalidRequest above it rather than serving a shorter list. Slicing here
    // would turn that error into a silent partial answer — the caller could not
    // tell "one of these has no data" from "you were cut off", which is R8.10's
    // argument against the clamp applied to a second parameter.
    const many = Array.from({ length: 51 }, (_, i) => `fp-${i}`);
    stub(empty);
    await fetchQuerySeries(http, { fingerprints: many });
    const url = String(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]);
    expect(url.split("fingerprints=")[1]!.split("%2C")).toHaveLength(51);
  });

  it("returns the parsed envelope for a valid 200", async () => {
    stub(FIXTURE_QUERY_SERIES);
    await expect(fetchQuerySeries(http, { fingerprints: all })).resolves.toEqual(FIXTURE_QUERY_SERIES);
  });

  it("throws ContractError on a 200 whose body is not an envelope", async () => {
    stub({ series: [] });
    await expect(fetchQuerySeries(http, { fingerprints: all })).rejects.toThrow(ContractError);
  });

  it("serves the series fixture on the fixture arm", async () => {
    await expect(fetchQuerySeries(fixture, { fingerprints: all })).resolves.toEqual(FIXTURE_QUERY_SERIES);
  });
});


describe("parseTagView", () => {
  const rows = FIXTURE_TAGS.data;
  const row = rows[0]!;

  it("parses every row the contract fixture ships, and returns them unchanged", () => {
    // All three, not one: they differ in the way that matters. Row one carries
    // every dimension, row two has the (a) trio only (a statement with no `pgsi:`
    // comment), row three has two of the (b) five. A parser that required the
    // comment dimensions would pass on row one and fail on the other two, which is
    // the case the omitempty rule exists for.
    expect(rows).toHaveLength(3);
    for (const r of rows) expect(parseTagView(r)).toEqual(r);
  });

  it("throws on a missing load column and names the row by its tagHash", () => {
    // The row-identity rule the other parsers follow: a bad row in a 3-row payload
    // says which one, and the hash is this row's identity.
    const { avgExecTimeMs: _dropped, ...rest } = row;
    expect(() => parseTagView(rest)).toThrow(ContractError);
    expect(() => parseTagView(rest)).toThrow(
      /tag 76201a9fd0e4: missing required field "avgExecTimeMs"/,
    );
  });

  it("requires lockWaitMs, the column this route has and QueryView does not", () => {
    // R3.7 grants the lock column at rollup level and withholds it per query
    // (queries.go:565), so a tags parser that copied parseQueryView's list would
    // be missing exactly this field — and this is the test that says so.
    const { lockWaitMs: _dropped, ...rest } = row;
    expect(() => parseTagView(rest)).toThrow(
      /tag 76201a9fd0e4: missing required field "lockWaitMs"/,
    );
  });

  it("rejects a load column typed as a string, the shape a quoted number makes", () => {
    expect(() => parseTagView({ ...row, timesCalled: "4311" })).toThrow(
      /tag 76201a9fd0e4: field "timesCalled" is string, expected number/,
    );
  });

  it("requires tagHash, and rejects it typed as a number", () => {
    // No omitempty on it (queries.go:544), so absence is a break — and it is
    // checked before the require helper runs, because every other message on the
    // row is labelled with it.
    const { tagHash: _dropped, ...rest } = row;
    expect(() => parseTagView(rest)).toThrow(/tag row: field "tagHash" is undefined, expected string/);
    expect(() => parseTagView({ ...row, tagHash: 76201 })).toThrow(
      /tag row: field "tagHash" is number, expected string/,
    );
  });

  it("accepts the omitempty dimensions absent and rejects one typed as a number", () => {
    // The eight dimension strings all carry omitempty, so only their types are
    // policed — the `queryid` rule from parseSessionView:495, applied to eight
    // fields. Row two is the all-absent case (the loop above); this pins the type
    // when present, on one (a) field and one (b) field.
    expect(() => parseTagView({ ...row, dbDriver: 9 })).toThrow(
      /tag 76201a9fd0e4: field "dbDriver" is number, expected string/,
    );
    expect(() => parseTagView({ ...row, user: ["openwebui"] })).toThrow(
      /tag 76201a9fd0e4: field "user" is array, expected string/,
    );
  });
});

describe("fetchTags", () => {
  const empty = { data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } };

  it("GETs the contract path with the params it was given", async () => {
    stub(empty);
    await fetchTags(http, { namespace: "open-webui", limit: 50 });
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe(
      "/api/v1/insights/tags?namespace=open-webui&limit=50",
    );
  });

  it("sends no query string at all when every param is unset", async () => {
    // Every param on this route is optional, so an unset one is absent from the URL
    // rather than empty — the rule `search()` exists for, asserted per fetcher
    // because each route's param list is its own contract.
    stub(empty);
    await fetchTags(http);
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe("/api/v1/insights/tags");
  });

  it("passes a limit above the maximum through instead of clamping it (R8.10)", async () => {
    stub(empty);
    await fetchTags(http, { limit: 5000 });
    expect(String(vi.mocked(globalThis.fetch).mock.calls[0]?.[0])).toContain("limit=5000");
  });

  it("returns the parsed envelope for a valid 200", async () => {
    stub(FIXTURE_TAGS);
    // The fixture object itself, so the http arm and the fixture arm are shown to
    // agree on the type by construction rather than by assertion.
    await expect(fetchTags(http)).resolves.toEqual(FIXTURE_TAGS);
  });

  it("throws ContractError on a 200 whose body is not an envelope", async () => {
    stub({ generatedAt: FIXTURE_GENERATED_AT, tags: [] });
    await expect(fetchTags(http)).rejects.toThrow(ContractError);
  });

  it("throws on a row the parser rejects, which is what the missing parser was", async () => {
    // The class this module exists for, on the route that had no parser: a row
    // typed as TagView by the fixture's own annotation, and by a cast, without
    // being checked. Now it has to be.
    stub({
      data: [{ tagHash: "0".repeat(64), loadByTotalTime: 1 }],
      meta: { total: 1, truncated: false, generatedAt: FIXTURE_GENERATED_AT },
    });
    await expect(fetchTags(http)).rejects.toThrow(/tag 0{12}: missing required field "pctLoadByQueries"/);
  });

  it("serves the tags fixture on the fixture arm", async () => {
    await expect(fetchTags(fixture)).resolves.toEqual(FIXTURE_TAGS);
  });
});

describe("parseSessionView", () => {
  const rows = FIXTURE_ACTIVE_QUERIES.data;
  const row = rows[0]!;

  it("parses every row the contract fixture ships, and returns them unchanged", () => {
    // All three rows, not one: they differ in the way that matters. The third
    // (pid 41304) has no `queryid` — the struct's one omitempty field — and its
    // `transactionDurationSeconds` is an int where the other two are floats.
    expect(rows).toHaveLength(3);
    for (const r of rows) expect(parseSessionView(r)).toEqual(r);
  });

  it("throws on a missing required field and names both the pid and the field", () => {
    const { waitEvent: _dropped, ...rest } = row;
    expect(() => parseSessionView(rest)).toThrow(ContractError);
    expect(() => parseSessionView(rest)).toThrow(/pid 41288: missing required field "waitEvent"/);
  });

  it("rejects blockingPids:null, which Go never emits without omitempty", () => {
    // The house rule is `[]` on the wire for a slice with no omitempty. A
    // `null` means someone added omitempty or hand-wrote the body, and the
    // page's `blockingPids.length` would have to guess — the `?? []` class, on
    // a nested field.
    expect(() => parseSessionView({ ...row, blockingPids: null })).toThrow(
      /pid 41288: field "blockingPids" is null, expected array/,
    );
  });

  it("rejects a non-numeric member of blockingPids", () => {
    expect(() => parseSessionView({ ...row, blockingPids: [41102, "41103"] })).toThrow(
      /pid 41288: blockingPids\[1\] is string, expected number/,
    );
  });

  it("accepts queryid absent and rejects it typed as a number", () => {
    // Absence is the fixture's own third row (covered by the loop above); this
    // pins the type when present. The field is `queryid`, lowercase-d, on both
    // sides — a `queryId` in the body is a missing `queryid` plus an unexamined
    // extra key, which is why the fixture's spelling is the one asserted.
    expect(() => parseSessionView({ ...row, queryid: 1458305871 })).toThrow(
      /pid 41288: field "queryid" is number, expected string/,
    );
  });

  it("rejects clientPort as a number, the type a hand-written copy would have used", () => {
    // The collector keeps pg_stat_activity's text form (sessions.go), so a
    // `number` here is a type that describes an endpoint that does not answer —
    // the exact defect the header of types.ts records for `health`/`metrics`.
    expect(() => parseSessionView({ ...row, clientPort: 5432 })).toThrow(
      /pid 41288: field "clientPort" is number, expected string/,
    );
  });
});

describe("parseActiveQueriesResponse", () => {
  it("parses the contract fixture into the named body type", () => {
    expect(parseActiveQueriesResponse(FIXTURE_ACTIVE_QUERIES)).toEqual(FIXTURE_ACTIVE_QUERIES);
  });

  it("throws when scorecard is missing instead of handing the page an empty header", () => {
    // The key `parseEnvelope` cannot protect, because the generic has no
    // `scorecard` field to protect: this route's body is a named struct and a
    // missing scorecard used to be one cast away from `undefined`.
    const { scorecard: _dropped, ...rest } = FIXTURE_ACTIVE_QUERIES;
    expect(() => parseActiveQueriesResponse(rest)).toThrow(/scorecard is undefined, expected object/);
  });

  it("throws on a body with no data rather than defaulting it to []", () => {
    const { data: _dropped, ...rest } = FIXTURE_ACTIVE_QUERIES;
    expect(() => parseActiveQueriesResponse(rest)).toThrow(
      // The key list is the body's own order (the fixture declares scorecard
      // before blockingChains), not a sorted one — same as the `keys present:
      // meta, clusters` assertion in the parseEnvelope block.
      /ActiveQueriesResponse\.data is undefined, expected array \(keys present: scorecard, blockingChains, meta\)/,
    );
  });

  it("rejects blockingChains:null, and a meta whose total is a string", () => {
    expect(() =>
      parseActiveQueriesResponse({ ...FIXTURE_ACTIVE_QUERIES, blockingChains: null }),
    ).toThrow(/blockingChains is null, expected array/);
    expect(() =>
      parseActiveQueriesResponse({
        ...FIXTURE_ACTIVE_QUERIES,
        meta: { total: "3", truncated: false, generatedAt: FIXTURE_GENERATED_AT },
      }),
    ).toThrow(/meta.total is string, expected number/);
  });

  it("rejects a chain whose blocker is not a row", () => {
    const chain = FIXTURE_ACTIVE_QUERIES.blockingChains[0]!;
    expect(() =>
      parseActiveQueriesResponse({ ...FIXTURE_ACTIVE_QUERIES, blockingChains: [{ ...chain, blockers: [null] }] }),
    ).toThrow(/blocking chain: blockers\[0\] is null, expected object/);
  });

  it("accepts the all-empty body: no rows, empty byState, both longest pointers absent", () => {
    // A cluster with no sessions answers an empty list — the route has no 404
    // for that reason, per its own OpenAPI note — and a scorecard whose two
    // omitempty pointers have nothing to point at.
    expect(() =>
      parseActiveQueriesResponse({
        data: [],
        scorecard: { byState: {}, total: 0 },
        blockingChains: [],
        meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT },
      }),
    ).not.toThrow();
  });
});

describe("parseTerminateResponse", () => {
  /* The second of the three named, non-envelope bodies (types.ts names all
   * three). The fixture is the contract's own bytes —
   * `testdata/api/sessions-terminate.json`, imported by fixtures.ts — so the
   * happy path here is the same object the Go test marshals, not a second copy
   * of it written in this file. */
  const fixtureBody = FIXTURE_TERMINATE_SESSION;

  it("accepts the contract fixture and returns its four fields", () => {
    expect(parseTerminateResponse(fixtureBody)).toEqual(fixtureBody);
  });

  it("rejects a body that is not an object", () => {
    expect(() => parseTerminateResponse([])).toThrow(/TerminateResponse object from the API, got array/);
    expect(() => parseTerminateResponse(null)).toThrow(/got null/);
    expect(() => parseTerminateResponse("48213")).toThrow(/got string/);
  });

  it("names the field whose type broke, for each of the four", () => {
    // The R16.8 case, at runtime: a json tag renamed in `dto/sessions.go`
    // without regenerating the fixture is a *missing* field here, and all four
    // have a check (the dto puts no `omitempty` on any of them), so none of
    // them can arrive as `undefined` and render as a blank.
    expect(() =>
      parseTerminateResponse({ terminated: fixtureBody.terminated, actor: fixtureBody.actor, target: fixtureBody.target }),
    ).toThrow(/TerminateResponse\.pid is undefined, expected number/);
    expect(() =>
      parseTerminateResponse({ pid: fixtureBody.pid, actor: fixtureBody.actor, target: fixtureBody.target }),
    ).toThrow(/TerminateResponse\.terminated is undefined, expected boolean/);
    expect(() =>
      parseTerminateResponse({ pid: fixtureBody.pid, terminated: fixtureBody.terminated, target: fixtureBody.target }),
    ).toThrow(/TerminateResponse\.actor is undefined, expected string/);
    expect(() =>
      parseTerminateResponse({ pid: fixtureBody.pid, terminated: fixtureBody.terminated, actor: fixtureBody.actor }),
    ).toThrow(/TerminateResponse\.target is undefined, expected string/);
  });

  it("rejects a string pid, the shape a hand-written body drifts to", () => {
    // `pid: "48213"` parses as JSON and would render as a number in the UI, so
    // the only place that can tell the two apart is here.
    expect(() => parseTerminateResponse({ ...fixtureBody, pid: "48213" })).toThrow(
      /TerminateResponse\.pid is string, expected number/,
    );
  });

  it("keeps `terminated: false` — a 200 that terminated nothing is a value", () => {
    // The doc's own case: the signal reached no backend. A falsy check would
    // read it as an absence, which is the `?? []` failure class one type down.
    expect(parseTerminateResponse({ ...fixtureBody, terminated: false }).terminated).toBe(false);
  });

  it("drops a key the contract does not have", () => {
    // The body is built from the four checked fields rather than cast, so an
    // extra key cannot reach a page as an untyped extra the type never named.
    expect(Object.keys(parseTerminateResponse({ ...fixtureBody, extra: 1 }))).toEqual([
      "pid",
      "terminated",
      "actor",
      "target",
    ]);
  });
});

describe("fetchActiveQueries", () => {
  const params = { namespace: "postgres-operator", cluster: "ha-cluster" };

  it("GETs the contract path with both required params", async () => {
    stub(FIXTURE_ACTIVE_QUERIES);
    await fetchActiveQueries(http, params);
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe(
      "/api/v1/active-queries?namespace=postgres-operator&cluster=ha-cluster",
    );
  });

  it("returns the parsed response for a valid 200", async () => {
    stub(FIXTURE_ACTIVE_QUERIES);
    await expect(fetchActiveQueries(http, params)).resolves.toEqual(FIXTURE_ACTIVE_QUERIES);
  });

  it("throws ContractError on a 200 whose body is the generic envelope", async () => {
    // `{data, meta}` is exactly what `Envelope<SessionView>` would have been,
    // and it is not this route's body: R4.1's scorecard and R4.4's chains are
    // required siblings of it. A server that shipped the generic here gets a
    // throw, not a table under a header that renders as nothing.
    stub({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } });
    await expect(fetchActiveQueries(http, params)).rejects.toThrow(/blockingChains is undefined/);
  });

  it("serves the active-queries fixture on the fixture arm", async () => {
    await expect(fetchActiveQueries(fixture, params)).resolves.toEqual(FIXTURE_ACTIVE_QUERIES);
  });
});

describe("terminateSession", () => {
  /* The file's only non-GET fetcher, so the block asserts the verb as well as
   * the URL: a terminate that silently issued a GET would still return the
   * fixture on the fixture arm and pass every other test in this file. */
  const params = { pid: 48213, namespace: "freeidp", cluster: "freeidp-pg", confirm: "true" } as const;

  it("POSTs the contract path with the three required query params", async () => {
    stub(FIXTURE_TERMINATE_SESSION);
    await terminateSession(http, params);
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe(
      "/api/v1/sessions/48213/terminate?namespace=freeidp&cluster=freeidp-pg&confirm=true",
    );
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[1]).toMatchObject({ method: "POST" });
  });

  it("returns the parsed response for a valid 200", async () => {
    stub(FIXTURE_TERMINATE_SESSION);
    await expect(terminateSession(http, params)).resolves.toEqual(FIXTURE_TERMINATE_SESSION);
  });

  it("throws ContractError on a 200 whose body is the generic envelope", async () => {
    // `{data, meta}` is not this route's body either — R4.5's answer is a named
    // struct with four scalars and no meta, which is the case types.ts exists to
    // keep from being papered over by an `Envelope<T>`.
    stub({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } });
    await expect(terminateSession(http, params)).rejects.toThrow(
      /TerminateResponse\.pid is undefined, expected number/,
    );
  });

  it("reports R4.5's 403 as a POST, with R8.5's code attached", async () => {
    // The gate-off refusal (and the replica one) both answer 403 `Unsupported`.
    // The verb is in the message because a panel that says `GET → 403` under a
    // button the user just pressed reports the wrong request as refused, and the
    // code is in the error because `Unsupported` ("feature gated off by config")
    // is a different fix from `Forbidden` ("your RBAC cannot do this").
    stub({ error: { code: "Unsupported", message: "--allow-actions is off", details: [] } }, false, 403, "Forbidden");
    await expect(terminateSession(http, params)).rejects.toThrow(
      /^POST → 403 Unsupported: --allow-actions is off$/,
    );
    await expect(terminateSession(http, params)).rejects.toMatchObject({ status: 403, code: "Unsupported" });
  });

  it("serves the terminate fixture on the fixture arm", async () => {
    await expect(terminateSession(fixture, params)).resolves.toEqual(FIXTURE_TERMINATE_SESSION);
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

  it("ships an active-queries fixture whose scorecard counts its own rows", () => {
    // Nothing in the parser enforces that `scorecard.total` equals
    // `data.length` — an arithmetic invariant in the parser would turn a server
    // bug into a blank page (see parseScorecardView) — so it is asserted here
    // instead. If the fixture is ever regenerated with a scorecard that
    // disagrees with its own rows, this fails rather than the page showing a
    // header that contradicts its table.
    const f = FIXTURE_ACTIVE_QUERIES;
    expect(f.meta.total).toBe(f.data.length);
    expect(f.scorecard.total).toBe(f.data.length);
    expect(Object.values(f.scorecard.byState).reduce((sum, n) => sum + n, 0)).toBe(f.data.length);
  });
});

/* R16.8's URL half.
 *
 * R16.8 pairs a checked-in JSON fixture per endpoint with the SPA's suite, and
 * the A3 ruling on it extends the pairing to type *names* (`<Noun>Response` vs
 * `Envelope<Row>`). Neither half reaches the request line: `efdabb3` shipped
 * `terminateSession` building `/api/v1/sessions/48213?...` for a route the
 * contract publishes as `/api/v1/sessions/{pid}/terminate`, and the suite was
 * green because `client.test.ts:1286` asserted the same suffix-less string the
 * fetcher built — a fixture pins the body, and the bug was in the URL. A6 found
 * it from the page three commits later; `9790fda` fixed it.
 *
 * So this block compares code against the contract instead of code against
 * code: it reads the `paths:` keys out of api/openapi.yaml and requires every
 * path the seven fetchers build to be one of them. The yaml comes in through
 * vite's `?raw` suffix rather than a parser, so there is no new dependency and
 * no second copy of the list to drift — the file the Go emitter writes is the
 * file this test reads.
 *
 * The roster is asserted as well as the rows: `cases` is hand-written because
 * each row needs a body and params, so without the set comparison an eighth
 * fetcher landing without a row would pass silently — the same vacuity one
 * level up, with the list moved from the extractor to the case table.
 */
describe("every fetcher path is a contract path", () => {
  // Keys are the two-space-indented lines under `paths:`, up to the next
  // top-level key. Anchoring on the indent — not on a leading `/` alone — is
  // what keeps `/healthz` and `/debug/pprof/trace` in and a schema property out.
  const contractPaths: string[] = (() => {
    const keys: string[] = [];
    let inPaths = false;
    for (const line of OPENAPI_YAML.split("\n")) {
      if (/^paths:\s*$/.test(line)) {
        inPaths = true;
        continue;
      }
      if (!inPaths) continue;
      if (/^[^\s]/.test(line)) break;
      const m = /^ {2}(\/[^:]*):/.exec(line);
      if (m?.[1]) keys.push(m[1]);
    }
    return keys;
  })();

  // The anti-vacuity guard. An extractor that silently returns [] makes every
  // later assertion pass, so the set is asserted before it is used — this is
  // the failure A6 named, a first run "quietly allowed into a list".
  it("extracts the contract's path list", () => {
    expect(contractPaths.length).toBeGreaterThan(20);
    expect(contractPaths).toContain("/api/v1/sessions/{pid}/terminate");
    expect(contractPaths).toContain("/api/v1/clusters");
  });

  // The http arm parses through `parseEnvelope`, so the stub has to serve the
  // envelope shape; `FIXTURE_FLEET`/`FIXTURE_QUERIES`/`FIXTURE_TAGS` are the
  // pre-envelope `{meta, clusters|queries|tags}` form the generator writes for
  // testdata, and parsing one over http is the bug the split exists for.
  const emptyBody = { data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_GENERATED_AT } };

  // `{param}` segments become `[^/]+`. The placeholder is NUL because the
  // escape step below must not touch it and a space could appear in a path.
  const published = (path: string): boolean =>
    contractPaths.some((key) => {
      const pattern = key
        .replace(/\{[^}]+\}/g, "\u0000")
        .replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
        .replaceAll("\u0000", "[^/]+");
      return new RegExp(`^${pattern}$`).test(path);
    });

  const cases: ReadonlyArray<{
    readonly name: string;
    readonly body: unknown;
    readonly call: () => Promise<unknown>;
  }> = [
    { name: "fetchFleet", body: emptyBody, call: () => fetchFleet(http) },
    {
      name: "fetchQueries",
      body: emptyBody,
      call: () => fetchQueries(http, { namespace: "open-webui", cluster: "open-webui-pg", limit: 50 }),
    },
    {
      name: "fetchQueryDetail",
      body: FIXTURE_QUERY_DETAIL,
      call: () =>
        fetchQueryDetail(http, {
          fingerprint: FIXTURE_QUERY_DETAIL.fingerprint,
          namespace: FIXTURE_QUERY_DETAIL.namespace,
          cluster: FIXTURE_QUERY_DETAIL.cluster,
        }),
    },
    {
      name: "fetchQuerySeries",
      body: FIXTURE_QUERY_SERIES,
      call: () =>
        fetchQuerySeries(http, {
          fingerprints: FIXTURE_QUERY_SERIES.data.map((s) => s.fingerprint),
          range: "1h",
        }),
    },
    {
      name: "fetchTags",
      body: emptyBody,
      call: () => fetchTags(http, { namespace: "open-webui", limit: 50 }),
    },
    {
      name: "fetchActiveQueries",
      body: FIXTURE_ACTIVE_QUERIES,
      call: () => fetchActiveQueries(http, { namespace: "postgres-operator", cluster: "ha-cluster" }),
    },
    {
      name: "terminateSession",
      body: FIXTURE_TERMINATE_SESSION,
      call: () => terminateSession(http, { pid: 48213, namespace: "freeidp", cluster: "freeidp-pg", confirm: "true" }),
    },
  ];

  // Everything the module exports that is a function but not a fetcher, named
  // once. `FIXTURES` is excluded by the typeof check (it is an object); a new
  // non-fetcher function export has to be added here or this test fails by name,
  // which is the loud version of a decision that would otherwise be silent.
  const NON_FETCHER_EXPORTS: string[] = ["ApiError", "resolveConfig"];

  it("cases covers every fetcher the module exports", () => {
    const exported = Object.entries(api)
      .filter(([name, value]) => typeof value === "function" && !NON_FETCHER_EXPORTS.includes(name))
      .map(([name]) => name);
    expect(new Set(cases.map((c) => c.name))).toEqual(new Set(exported));
  });

  for (const c of cases) {
    it(`${c.name} builds a path the contract publishes`, async () => {
      stub(c.body);
      await c.call();
      const url = String(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]);
      const path = url.split("?")[0] ?? "";
      expect(published(path), `${c.name} built ${path}, which is not a path in api/openapi.yaml`).toBe(true);
    });
  }
});
