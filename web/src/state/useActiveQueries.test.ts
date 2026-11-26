/* The Active Queries hook's tests — the R10.22 envelope, tested against a
 * two-field subject.
 *
 * The shape is `useQueryDetail.test.ts` (read 2026-11-24, the 311-line file as
 * of `2223be9`) with the same three helpers, because the two files exist to
 * cover the same two rules and the helpers are what makes them testable:
 * `responseOf`/`stub` for a fetch that answers, and `pending` for a fetch whose
 * answer the test hands over by hand — which is the only way to resolve an
 * *aborted* request while a newer one is in flight, and so the only way to test
 * the superseded-response rule as a rule rather than as a race.
 *
 * What changes with the subject being a cluster pair instead of a fingerprint:
 * the URL assertion (the route takes the pair in the query string, not the
 * path), the params consts (two, not three), and the field the assertions read
 * to tell one subject's body from another's — this body has no subject field of
 * its own, so `withTotal` relabels `meta.total` instead.
 */

import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

import { FIXTURE_ACTIVE_QUERIES } from "../api/fixtures";
import type { ActiveQueriesParams, ClientConfig } from "../api/client";
import { activeKey, activeParamsFrom, useActiveQueries } from "./useActiveQueries";
import { DEFAULT_STATE, type UrlState } from "./urlState";

const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
});

function responseOf(body: unknown, ok = true, status = 200, statusText = "OK"): Response {
  const text = typeof body === "string" ? body : JSON.stringify(body);
  return {
    ok,
    status,
    statusText,
    text: async () => text,
    json: async () => JSON.parse(text) as unknown,
  } as unknown as Response;
}

function stub(body: unknown, ok = true, status = 200, statusText = "OK"): void {
  globalThis.fetch = () => Promise.resolve(responseOf(body, ok, status, statusText));
}

/** A fetch whose answers the test hands over by hand, plus the URLs it was
 *  called with. `resolve` answers the oldest unanswered request; `resolveAt(n)`
 *  answers the n-th one — the instrument the superseded-response test needs. */
function pending(): {
  urls: string[];
  resolve: (body: unknown, ok?: boolean, status?: number, statusText?: string) => void;
  resolveAt: (n: number, body: unknown, ok?: boolean, status?: number, statusText?: string) => void;
} {
  const open: { done: boolean; resolve: (res: Response) => void }[] = [];
  const urls: string[] = [];
  globalThis.fetch = (input: RequestInfo | URL) => {
    urls.push(String(input));
    return new Promise<Response>((r) => {
      open.push({ done: false, resolve: r });
    });
  };
  const settle = (
    slot: { done: boolean; resolve: (res: Response) => void } | undefined,
    body: unknown,
    ok = true,
    status = 200,
    statusText = "OK",
  ) => {
    if (slot === undefined || slot.done) return;
    slot.done = true;
    slot.resolve(responseOf(body, ok, status, statusText));
  };
  return {
    urls,
    resolve: (body, ok, status, statusText) => {
      settle(open.find((s) => !s.done), body, ok, status, statusText);
    },
    resolveAt: (n, body, ok, status, statusText) => {
      settle(open.filter((s) => !s.done)[n], body, ok, status, statusText);
    },
  };
}

const http: ClientConfig = { source: "http", apiBase: "", fixture: "fleet", delayMs: 0 };
const fixture: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };

const P: ActiveQueriesParams = { namespace: "freeidp", cluster: "freeidp-pg" };
const Q: ActiveQueriesParams = { namespace: "postgres-operator", cluster: "ha-cluster" };
const URL_P = "/api/v1/active-queries?namespace=freeidp&cluster=freeidp-pg";
const URL_Q = "/api/v1/active-queries?namespace=postgres-operator&cluster=ha-cluster";

/** The fixture body re-labelled so two subjects' answers are distinguishable by
 *  a field the assertions read.
 *
 *  The detail route re-labels `fingerprint`, because its body carries one. This
 *  body has no subject field of its own — it is one cluster's sessions, and
 *  nothing inside it names the cluster — so the field used here is
 *  `meta.total`, which `parseMeta` type-checks and the fetcher's response
 *  carries through. Any integer is legal (the parser deliberately does not
 *  re-check `total === data.length`), which is what makes it usable as a label. */
const withTotal = (total: number) => ({
  ...FIXTURE_ACTIVE_QUERIES,
  meta: { ...FIXTURE_ACTIVE_QUERIES.meta, total },
});

describe("activeKey", () => {
  it("changes when either half of the pair changes", () => {
    const base = activeKey(P);
    // The two-field form of `detailKey`'s three-field test: a key written as
    // `params.cluster` — the likelier typo once the pair is one URL field —
    // passes every other test here while making a namespace move look like a
    // refresh of the same subject.
    expect(activeKey({ ...P, namespace: "other" })).not.toBe(base);
    expect(activeKey({ ...P, cluster: "other" })).not.toBe(base);
    expect(activeKey({ namespace: "freeidp", cluster: "freeidp-pg" })).toBe(base);
  });
});

describe("activeParamsFrom", () => {
  it("splits the cluster pair into the two required params", () => {
    const state: UrlState = { ...DEFAULT_STATE, page: "active", cluster: "freeidp/freeidp-pg" };
    expect(activeParamsFrom(state)).toEqual({ namespace: "freeidp", cluster: "freeidp-pg" });
  });

  it("is null for every URL that cannot name a cluster", () => {
    // `routes.ts:219`'s path is `/c/:ns/:name/active`, so a cluster-less URL is
    // the hand-edited case; the route has no fleet-wide form to fall back to
    // because `pg_stat_activity` is per-instance.
    expect(activeParamsFrom({ ...DEFAULT_STATE, cluster: null })).toBeNull();
    expect(activeParamsFrom({ ...DEFAULT_STATE, page: "active", cluster: null })).toBeNull();
    // Malformed pairs: a pair needs a non-empty name on both sides of the slash.
    expect(activeParamsFrom({ ...DEFAULT_STATE, page: "active", cluster: "noslash" })).toBeNull();
    expect(activeParamsFrom({ ...DEFAULT_STATE, page: "active", cluster: "ns/" })).toBeNull();
    expect(activeParamsFrom({ ...DEFAULT_STATE, page: "active", cluster: "/name" })).toBeNull();
  });

  it("ignores `row`: the subject is the cluster, not a row of it", () => {
    // `detailParamsFrom` reads `row` for the fingerprint; this one must not, so
    // a URL that also selects an instance row still fetches the cluster's full
    // session list rather than nothing.
    const withRow: UrlState = {
      ...DEFAULT_STATE,
      page: "active",
      cluster: "freeidp/freeidp-pg",
      row: "freeidp-pg-1",
    };
    expect(activeParamsFrom(withRow)).toEqual(activeParamsFrom({ ...DEFAULT_STATE, page: "active", cluster: "freeidp/freeidp-pg" }));
  });
});

describe("useActiveQueries refetch behaviour (R10.22)", () => {
  it("is `loading` with no payload while the first request is in flight", async () => {
    const box = pending();
    const { result } = renderHook(() => useActiveQueries(http, P, 0));

    expect(result.current.state).toEqual({ phase: "loading", active: null, error: null, refreshing: false });
    expect(box.urls).toEqual([URL_P]);

    box.resolve(withTotal(3));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.active?.meta.total).toBe(3);
  });

  it("holds the payload and sets `refreshing` when the subject is unchanged", async () => {
    stub(FIXTURE_ACTIVE_QUERIES);
    const { result } = renderHook(() => useActiveQueries(http, P, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    const first = result.current.state.active;

    const box = pending();
    act(() => result.current.refresh());

    // Same subject → this is a refresh, so R10.22's hold-previous applies and the
    // payload is held by reference while the new one is in flight.
    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.active).toBe(first);
    expect(result.current.state.refreshing).toBe(true);

    box.resolve(withTotal(3));
    await waitFor(() => expect(result.current.state.refreshing).toBe(false));
    expect(result.current.state.active?.meta.total).toBe(3);
  });

  it("drops the payload when the subject changes, rather than holding the old one", async () => {
    stub(withTotal(3));
    const { result, rerender } = renderHook(
      ({ params }: { params: ActiveQueriesParams | null }) => useActiveQueries(http, params, 0),
      { initialProps: { params: P } },
    );
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.active?.meta.total).toBe(3);

    const box = pending();
    rerender({ params: Q });

    // The named case, and the reason this hook copies `useQueryDetail` rather
    // than `useQueries`: freeidp-pg's sessions under ha-cluster's heading are a
    // wrong frame, so the held payload is dropped and the phase goes back to
    // `loading` — `refreshing` stays false because nothing is refreshing, this
    // is a first load of Q.
    expect(result.current.state.phase).toBe("loading");
    expect(result.current.state.active).toBeNull();
    expect(result.current.state.refreshing).toBe(false);
    expect(box.urls).toEqual([URL_Q]);

    box.resolve(withTotal(9));
    await waitFor(() => expect(result.current.state.active?.meta.total).toBe(9));
  });

  it("ignores a response whose request was superseded", async () => {
    // `useQueryDetail.ts:121`'s abort-previous, copied. Resolving the *older*
    // request first is what makes this a test of the rule and not of timing:
    // its controller was aborted by Q's load, so `load` returns at the
    // `ctrl.signal.aborted` check and P's body never reaches setState.
    const box = pending();
    const { result, rerender } = renderHook(
      ({ params }: { params: ActiveQueriesParams | null }) => useActiveQueries(http, params, 0),
      { initialProps: { params: P } },
    );
    expect(box.urls).toEqual([URL_P]);

    rerender({ params: Q });
    expect(box.urls).toHaveLength(2);

    box.resolveAt(0, withTotal(3));
    expect(result.current.state.active).toBeNull();
    expect(result.current.state.phase).toBe("loading");

    box.resolve(withTotal(9));
    await waitFor(() => expect(result.current.state.active?.meta.total).toBe(9));
    expect(result.current.state.phase).toBe("ready");
  });

  it("keeps the last good payload and surfaces the error when a refresh fails", async () => {
    stub(FIXTURE_ACTIVE_QUERIES);
    const { result } = renderHook(() => useActiveQueries(http, P, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    const first = result.current.state.active;

    stub(
      { error: { code: "Internal", message: "store unavailable", details: ["collector"] } },
      false,
      500,
      "Internal Server Error",
    );
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.state.error).not.toBeNull());

    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.active).toBe(first);
    expect(result.current.state.error).toBe("GET → 500 Internal: store unavailable — collector");
  });

  it("keeps the payload holdable after a failed refresh, so the next refresh still shows it", async () => {
    // The catch writes `last.current = { key, active: held }` (:149) — the
    // *held* payload, not `null`. Writing `active: null` there is invisible to
    // the test above, which reads `state.active` and still sees the right value;
    // the difference surfaces on the *next* refresh, where `held` is read back
    // out of the ref at :132 and decides between `ready` and `loading`.
    stub(FIXTURE_ACTIVE_QUERIES);
    const { result } = renderHook(() => useActiveQueries(http, P, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));

    stub(
      { error: { code: "Internal", message: "store unavailable", details: ["collector"] } },
      false,
      500,
      "Internal Server Error",
    );
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.state.error).not.toBeNull());

    const box = pending();
    act(() => result.current.refresh());
    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.active).not.toBeNull();
    expect(result.current.state.refreshing).toBe(true);

    box.resolve(withTotal(3));
    await waitFor(() => expect(result.current.state.refreshing).toBe(false));
  });

  it("is `error` with no payload when the first request fails", async () => {
    // Two details, so the assertion also pins `describeFailure`'s "; " join
    // (`client.ts:659`) — the detail test's single-detail body could not.
    stub(
      { error: { code: "NotFound", message: "no such cluster", details: ["freeidp/freeidp-pg", "pg_stat_activity"] } },
      false,
      404,
      "Not Found",
    );
    const { result } = renderHook(() => useActiveQueries(http, P, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("error"));
    expect(result.current.state.active).toBeNull();
    expect(result.current.state.error).toBe(
      "GET → 404 NotFound: no such cluster — freeidp/freeidp-pg; pg_stat_activity",
    );
  });

  it("surfaces a ContractError's own message rather than an empty table", async () => {
    // 200, valid JSON, and the route's `scorecard` gone. R4.1 puts a scorecard
    // above this table and the generic envelope has no `scorecard` field to
    // protect, so a missing one used to be one cast away from `undefined`;
    // `parseActiveQueriesResponse` builds the body field-by-field instead and
    // throws. (`{ ...body, scorecard: undefined }` deletes the key through a
    // typed spread: `JSON.stringify` drops undefined values.)
    stub({ ...FIXTURE_ACTIVE_QUERIES, scorecard: undefined });
    const { result } = renderHook(() => useActiveQueries(http, P, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("error"));
    expect(result.current.state.error).toBe("scorecard is undefined, expected object");
  });

  it("is `idle` and never fetches when the URL names no cluster", async () => {
    let calls = 0;
    globalThis.fetch = () => {
      calls += 1;
      return Promise.resolve(responseOf({}));
    };
    const { result } = renderHook(() => useActiveQueries(http, null, 0));

    expect(result.current.state).toEqual({ phase: "idle", active: null, error: null, refreshing: false });
    expect(calls).toBe(0);
  });

  it("serves the fixture arm without touching fetch", async () => {
    let calls = 0;
    globalThis.fetch = () => {
      calls += 1;
      return Promise.resolve(responseOf({}));
    };
    const { result } = renderHook(() => useActiveQueries(fixture, P, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    // The fixture arm returns the const itself (no parse), so reference identity
    // holds here and nowhere in the http arm above.
    expect(result.current.state.active).toBe(FIXTURE_ACTIVE_QUERIES);
    expect(calls).toBe(0);
  });
});
