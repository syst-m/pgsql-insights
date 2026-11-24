/* The detail hook's two rules that `useFleet`'s tests can't teach: the subject
 * key, and the stale-response rule that comes with it.
 *
 * R10.22's hold-previous governs a refresh of the *same* payload, so the case
 * this file exists to cover is the one a fleet-shaped test never sees: the
 * subject changes underneath a live payload. Holding query A's SQL under query
 * B's heading is a wrong frame, not a stale one, so `useQueryDetail` must drop
 * it and go back to `loading` — the opposite of what the other two hooks do in
 * the same situation, and the reason the pair lives in a ref rather than being
 * read out of the `setState` updater.
 *
 * The second is `abort.current?.abort()` (`useFleet.ts:36-38`): a response that
 * arrives after its request was superseded must not land, or a slow answer for
 * the old fingerprint overwrites the new one. The test resolves the superseded
 * request *after* the new one to make that ordering the thing under test rather
 * than an accident of timing.
 */

import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

import { FIXTURE_QUERY_DETAIL } from "../api/fixtures";
import type { ClientConfig, QueryDetailParams } from "../api/client";
import { detailKey, detailParamsFrom, useQueryDetail } from "./useQueryDetail";
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

/** A fetch whose answer the test hands over by hand, plus the URLs it was called
 *  with — the detail route puts the fingerprint in the path, so the URL is the
 *  only way to assert *which* subject a render asked for.
 *
 *  `resolve` answers the oldest unanswered request; `resolveAt(n)` answers the
 *  n-th one, which is what lets a test resolve an *aborted* request while a
 *  newer one is still in flight — the ordering the superseded-response test
 *  needs, and the reason a plain FIFO queue isn't enough. */
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

const FP_A = "843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3";
const FP_B = "f".repeat(64);
const A: QueryDetailParams = { fingerprint: FP_A, namespace: "freeidp", cluster: "freeidp-pg" };
const B: QueryDetailParams = { fingerprint: FP_B, namespace: "freeidp", cluster: "freeidp-pg" };

/** The fixture payload re-labelled as a different fingerprint, so the two
 *  subjects' bodies are distinguishable by the field the assertions read. */
const detailFor = (fingerprint: string) => ({ ...FIXTURE_QUERY_DETAIL, fingerprint });

describe("detailKey", () => {
  it("changes when any of the three params changes", () => {
    const base = detailKey(A);
    // A key that ignored `namespace` would make a cluster move look like a
    // refresh of the same subject and hold the wrong detail.
    expect(detailKey({ ...A, namespace: "other" })).not.toBe(base);
    expect(detailKey({ ...A, cluster: "other" })).not.toBe(base);
    expect(detailKey({ ...A, fingerprint: FP_B })).not.toBe(base);
    expect(detailKey({ ...A, namespace: "freeidp", cluster: "freeidp-pg", fingerprint: FP_A })).toBe(base);
  });
});

describe("detailParamsFrom", () => {
  it("splits the cluster pair and takes the fingerprint from `row`", () => {
    const state: UrlState = { ...DEFAULT_STATE, page: "query-detail", cluster: "freeidp/freeidp-pg", row: FP_A };
    expect(detailParamsFrom(state)).toEqual({ fingerprint: FP_A, namespace: "freeidp", cluster: "freeidp-pg" });
  });

  it("is null for every URL that cannot name a subject", () => {
    // `withState` degrades a fingerprint-less query-detail to the Queries list
    // (urlState.ts:261-266), so these are the hand-edited-URL cases; the page's
    // job is its own "no such query" frame, not a fetch that answers 404.
    expect(detailParamsFrom({ ...DEFAULT_STATE, cluster: "freeidp/freeidp-pg", row: null })).toBeNull();
    expect(detailParamsFrom({ ...DEFAULT_STATE, cluster: null, row: FP_A })).toBeNull();
    expect(detailParamsFrom({ ...DEFAULT_STATE, cluster: "freeidp/freeidp-pg", row: "" })).toBeNull();
    // Malformed pairs: a pair needs a non-empty name on both sides of the slash.
    expect(detailParamsFrom({ ...DEFAULT_STATE, cluster: "noslash", row: FP_A })).toBeNull();
    expect(detailParamsFrom({ ...DEFAULT_STATE, cluster: "ns/", row: FP_A })).toBeNull();
    expect(detailParamsFrom({ ...DEFAULT_STATE, cluster: "/name", row: FP_A })).toBeNull();
  });
});

describe("useQueryDetail refetch behaviour (R10.22)", () => {
  it("is `loading` with no detail while the first request is in flight", async () => {
    const box = pending();
    const { result } = renderHook(() => useQueryDetail(http, A, 0));

    expect(result.current.state).toEqual({ phase: "loading", detail: null, error: null, refreshing: false });
    expect(box.urls).toEqual([`/api/v1/insights/queries/${FP_A}?namespace=freeidp&cluster=freeidp-pg`]);

    box.resolve(FIXTURE_QUERY_DETAIL);
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.detail?.fingerprint).toBe(FP_A);
  });

  it("holds the payload and sets `refreshing` when the subject is unchanged", async () => {
    stub(FIXTURE_QUERY_DETAIL);
    const { result } = renderHook(() => useQueryDetail(http, A, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    const first = result.current.state.detail;

    const box = pending();
    act(() => result.current.refresh());

    // Same subject → this is a refresh, so R10.22's hold-previous applies and the
    // payload is held by reference while the new one is in flight.
    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.detail).toBe(first);
    expect(result.current.state.refreshing).toBe(true);

    box.resolve(detailFor(FP_A));
    await waitFor(() => expect(result.current.state.refreshing).toBe(false));
    expect(result.current.state.detail?.fingerprint).toBe(FP_A);
  });

  it("drops the payload when the subject changes, rather than holding the old one", async () => {
    stub(detailFor(FP_A));
    const { result, rerender } = renderHook(
      ({ params }: { params: QueryDetailParams | null }) => useQueryDetail(http, params, 0),
      { initialProps: { params: A } },
    );
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.detail?.fingerprint).toBe(FP_A);

    const box = pending();
    rerender({ params: B });

    // The named case: A's SQL under B's heading is a wrong frame, so the held
    // payload is dropped and the phase goes back to `loading` — `refreshing`
    // stays false because nothing is refreshing, this is a first load of B.
    expect(result.current.state.phase).toBe("loading");
    expect(result.current.state.detail).toBeNull();
    expect(result.current.state.refreshing).toBe(false);
    expect(box.urls).toEqual([`/api/v1/insights/queries/${FP_B}?namespace=freeidp&cluster=freeidp-pg`]);

    box.resolve(detailFor(FP_B));
    await waitFor(() => expect(result.current.state.detail?.fingerprint).toBe(FP_B));
  });

  it("ignores a response whose request was superseded", async () => {
    // `useFleet.ts:36-38`'s abort-previous, copied. Resolving the *older*
    // request last is what makes this a test of the rule and not of timing.
    const box = pending();
    const { result, rerender } = renderHook(
      ({ params }: { params: QueryDetailParams | null }) => useQueryDetail(http, params, 0),
      { initialProps: { params: A } },
    );
    expect(box.urls).toEqual([`/api/v1/insights/queries/${FP_A}?namespace=freeidp&cluster=freeidp-pg`]);

    rerender({ params: B });
    expect(box.urls).toHaveLength(2);

    // Answer the *superseded* request first. Its controller was aborted by B's
    // load, so `load` returns at the `ctrl.signal.aborted` check and the body
    // never reaches setState — answering it before B's is what makes this a test
    // of the rule rather than of which promise settles first.
    box.resolveAt(0, detailFor(FP_A));
    expect(result.current.state.detail).toBeNull();
    expect(result.current.state.phase).toBe("loading");

    box.resolve(detailFor(FP_B));
    await waitFor(() => expect(result.current.state.detail?.fingerprint).toBe(FP_B));
    expect(result.current.state.phase).toBe("ready");
  });

  it("keeps the last good payload and surfaces the error when a refresh fails", async () => {
    stub(FIXTURE_QUERY_DETAIL);
    const { result } = renderHook(() => useQueryDetail(http, A, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    const first = result.current.state.detail;

    stub({ error: { code: "Internal", message: "store unavailable", details: ["collector"] } }, false, 500, "Internal Server Error");
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.state.error).not.toBeNull());

    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.detail).toBe(first);
    expect(result.current.state.error).toBe("GET → 500 Internal: store unavailable — collector");
  });

  it("is `error` with no detail when the first request fails", async () => {
    stub({ error: { code: "NotFound", message: "no such fingerprint", details: ["ns/c"] } }, false, 404, "Not Found");
    const { result } = renderHook(() => useQueryDetail(http, A, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("error"));
    expect(result.current.state.detail).toBeNull();
    expect(result.current.state.error).toBe("GET → 404 NotFound: no such fingerprint — ns/c");
  });

  it("surfaces a ContractError's own message rather than an empty detail", async () => {
    // 200, valid JSON, and the detail-only `latency` field gone. All four fields
    // queries.go:250-276 add to the embedded QueryView are non-omitempty, so
    // their absence is a contract break, and `parseQueryDetailView:273-275`
    // names the fingerprint the payload claims to be about rather than returning
    // a half-populated object. (`{ ...detail, latency: undefined }` is how you
    // delete a key through a typed spread: `JSON.stringify` drops undefined
    // values, so the key is absent from the bytes the parser sees.)
    stub({ ...FIXTURE_QUERY_DETAIL, latency: undefined });
    const { result } = renderHook(() => useQueryDetail(http, A, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("error"));
    expect(result.current.state.error).toBe(
      `query-detail ${FP_A.slice(0, 12)}: missing required field "latency"`,
    );
  });

  it("is `idle` and never fetches when the URL names no subject", async () => {
    let calls = 0;
    globalThis.fetch = () => {
      calls += 1;
      return Promise.resolve(responseOf({}));
    };
    const { result } = renderHook(() => useQueryDetail(http, null, 0));

    expect(result.current.state).toEqual({ phase: "idle", detail: null, error: null, refreshing: false });
    expect(calls).toBe(0);
  });

  it("serves the fixture arm without touching fetch", async () => {
    let calls = 0;
    globalThis.fetch = () => {
      calls += 1;
      return Promise.resolve(responseOf({}));
    };
    const { result } = renderHook(() => useQueryDetail(fixture, A, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.detail).toBe(FIXTURE_QUERY_DETAIL);
    expect(calls).toBe(0);
  });
});
