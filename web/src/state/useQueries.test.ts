/* R10.22's refetch contract, asserted on the Queries list, plus the params rule.
 *
 * R10.22 names four states a fetching component has to get right — first load
 * (`loading`, not an empty `ready`), refresh in flight (previous payload held +
 * `refreshing`), refresh failed (previous payload kept *and* the error surfaced),
 * and first load failed (`error`, no data). The charter's bar is that the named
 * case is the one tested, not the easy one, so all four are here rather than the
 * two a fixture makes trivial: the fixture arm resolves immediately, so a suite
 * that only ever renders against it can never observe `loading` or `refreshing`
 * at all. Hence the `pending()` helper — the fetch's answer is handed over by
 * hand, which is the only way to assert the state *between* request and response.
 *
 * The fifth case is the R16.8 one the `envelope.ts` parser exists for: a 200
 * whose body is not an envelope must surface as a `ContractError` message, not
 * as an empty list. `useFleet.ts:54`'s `err instanceof Error ? err.message :
 * String(err)` is what preserves it, and the test pins the exact string so a
 * future `catch { /* ignore *\/ }` cannot swallow it silently.
 */

import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

import { FIXTURE_GENERATED_AT, FIXTURE_QUERIES } from "../api/fixtures";
import type { ClientConfig, QueryListParams } from "../api/client";
import { listParamsFrom, useQueries } from "./useQueries";
import { DEFAULT_STATE, type TimeRange, type UrlState } from "./urlState";

const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
});

/** The fake `client.ts` needs: `getJSON` awaits `res.text()`, so the double has
 *  to carry the bytes, not a pre-parsed object (the shape `client.test.ts:618`
 *  settled on after an earlier stub "parsed" by never running the parse). */
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

/** A fetch whose answer the test hands over by hand. */
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

/** A valid queries envelope whose `meta.total` identifies it, so "which payload
 *  is on screen?" is answerable from the assertion. */
const envelope = (total: number) => ({
  data: [],
  meta: { total, truncated: false, generatedAt: FIXTURE_GENERATED_AT },
  // Required since `ae9cc86` — `parseQueriesResponse` reads the whole named body,
  // so a stub with only `data` and `meta` now rejects and `ready` never happens.
  pgStatStatements: [],
});

const http: ClientConfig = { source: "http", apiBase: "", fixture: "fleet", delayMs: 0 };
const fixture: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };

/* The params object is hoisted, not inlined, and this is load-bearing rather
 * than tidy: `useQueries`'s `load` lists `params` in its deps (useQueries.ts:147
 * — copied from `useFleet.ts:62`'s `cfg`), so a fresh object per render is a
 * fresh `load` per render, and the effect at :149-152 refires on every one.
 * Since `load` itself calls `setState`, that is a render loop, not just an extra
 * fetch: the first version of this file inlined `{}` at six call sites and the
 * worker died at a 4 GB heap. The hook's caller contract says to memoise (its
 * docblock points at `App.tsx:85-88` for the same rule on `cfg`); a module const
 * is the test's version of that memoise. */
const NO_PARAMS: QueryListParams = {};

/** `pollMs: 0` everywhere — `useFleet.ts:70`'s `pollMs <= 0` off-switch is what
 *  lets a test hold the polling clock still while it inspects one request. */
describe("useQueries refetch behaviour (R10.22)", () => {
  it("is `loading` with no data while the first request is in flight", async () => {
    const box = pending();
    const { result } = renderHook(() => useQueries(http, NO_PARAMS, 0));

    // The state the old cast used to fake: an empty list is not a loading state.
    expect(result.current.state).toEqual({ phase: "loading", queries: null, error: null, refreshing: false });

    box.resolve(envelope(3));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.queries?.meta.total).toBe(3);
    expect(result.current.state.refreshing).toBe(false);
  });

  it("ignores a response whose request was superseded", async () => {
    // `useFleet.ts:36-38`'s abort-previous, copied. The first version of this
    // file had a single-slot `pending()` (one `let resolve`), so the second
    // fetch overwrote the first's resolver and the superseded request could not
    // be answered at all — the rule had no test in this file, only in
    // `useQueryDetail.test.ts`, which had already grown the multi-slot version.
    const box = pending();
    const { result } = renderHook(() => useQueries(http, NO_PARAMS, 0));
    expect(box.urls).toHaveLength(1);

    act(() => result.current.refresh());
    expect(box.urls).toHaveLength(2);

    // Answer the superseded request and flush its continuation: with the
    // `ctrl.signal.aborted` check deleted, envelope(9) would land here and the
    // assertion below would see it before the real answer ever arrives.
    box.resolveAt(0, envelope(9));
    await act(async () => {
      await Promise.resolve();
    });
    expect(result.current.state.queries).toBeNull();
    expect(result.current.state.phase).toBe("loading");

    box.resolve(envelope(3));
    await waitFor(() => expect(result.current.state.queries?.meta.total).toBe(3));
  });

  it("holds the previous payload and sets `refreshing` while a refresh is in flight", async () => {
    stub(envelope(3));
    const { result } = renderHook(() => useQueries(http, NO_PARAMS, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    const first = result.current.state.queries;

    const box = pending();
    act(() => result.current.refresh());

    // R10.22's `.hold-previous`: the rows stay (same object reference) at
    // reduced opacity rather than flashing a skeleton.
    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.queries).toBe(first);
    expect(result.current.state.refreshing).toBe(true);
    expect(result.current.state.error).toBeNull();

    box.resolve(envelope(4));
    await waitFor(() => expect(result.current.state.queries?.meta.total).toBe(4));
    expect(result.current.state.refreshing).toBe(false);
  });

  it("keeps the last good payload and surfaces the error when a refresh fails", async () => {
    stub(envelope(3));
    const { result } = renderHook(() => useQueries(http, NO_PARAMS, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    const first = result.current.state.queries;

    stub({ error: { code: "Internal", message: "store unavailable", details: ["collector"] } }, false, 500, "Internal Server Error");
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.state.error).not.toBeNull());

    // The half that a `phase: "error"` screen gets wrong: a failed refresh is
    // not a page with no data, so the payload survives *and* the error shows.
    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.queries).toBe(first);
    expect(result.current.state.error).toBe("GET → 500 Internal: store unavailable — collector");
    expect(result.current.state.refreshing).toBe(false);
  });

  it("is `error` with no data when the first request fails", async () => {
    stub({ error: { code: "Internal", message: "store unavailable", details: ["collector"] } }, false, 500, "Internal Server Error");
    const { result } = renderHook(() => useQueries(http, NO_PARAMS, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("error"));
    expect(result.current.state.queries).toBeNull();
    expect(result.current.state.error).toBe("GET → 500 Internal: store unavailable — collector");
  });

  it("surfaces a ContractError's own message rather than an empty list", async () => {
    // 200 OK, parseable JSON, and the payload under `queries` instead of `data`
    // — the exact R16.8 bug class the parser names in its own message
    // (envelope.ts:809-814, since the http arm moved from `parseEnvelope` to
    // `parseQueriesResponse`). The hook's `err instanceof Error ? err.message :
    // String(err)` is what turns that string into the error the panel shows
    // rather than a blank table that looks like an empty result set.
    stub({ generatedAt: FIXTURE_GENERATED_AT, queries: [] });
    const { result } = renderHook(() => useQueries(http, NO_PARAMS, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("error"));
    expect(result.current.state.queries).toBeNull();
    expect(result.current.state.error).toBe(
      "QueriesResponse.data is undefined, expected array (keys present: generatedAt, queries)",
    );
  });

  it("serves the fixture arm without touching fetch", async () => {
    let calls = 0;
    globalThis.fetch = () => {
      calls += 1;
      return Promise.resolve(responseOf({}));
    };
    const { result } = renderHook(() => useQueries(fixture, NO_PARAMS, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.queries?.queries.length).toBe(FIXTURE_QUERIES.queries.length);
    expect(calls).toBe(0);
  });
});

describe("listParamsFrom", () => {
  const now = new Date("2026-11-23T12:00:00Z");

  it("maps each of the five R10.15 presets to its window", () => {
    const windows: Record<TimeRange, string> = {
      "1h": "2026-11-23T11:00:00Z",
      "6h": "2026-11-23T06:00:00Z",
      "1d": "2026-11-22T12:00:00Z",
      "7d": "2026-11-16T12:00:00Z",
      "30d": "2026-10-24T12:00:00Z",
    };
    for (const range of Object.keys(windows) as TimeRange[]) {
      expect(listParamsFrom({ ...DEFAULT_STATE, range }, now).from).toBe(windows[range]);
    }
  });

  it("sends `to` as the clock it was given, never a fresh one", () => {
    // The clock is a parameter so `load`'s identity doesn't change every render;
    // a `new Date()` inside the function would make every render a new request.
    expect(listParamsFrom(DEFAULT_STATE, now).to).toBe("2026-11-23T12:00:00Z");
  });

  it("takes the namespace only when exactly one is selected", () => {
    expect(listParamsFrom({ ...DEFAULT_STATE, ns: [] }, now).namespace).toBeUndefined();
    expect(listParamsFrom({ ...DEFAULT_STATE, ns: ["freeidp"] }, now).namespace).toBe("freeidp");
    // Two namespaces have no encoding in a single-string param, and sending the
    // first would silently narrow the set to a namespace the user did not pick
    // alone — the no-clamp argument `client.ts:425-430` makes for the batch.
    expect(listParamsFrom({ ...DEFAULT_STATE, ns: ["a", "b"] }, now).namespace).toBeUndefined();
  });

  it("sets exactly the four fields that have a URL counterpart", () => {
    const state: UrlState = { ...DEFAULT_STATE, cluster: "freeidp/freeidp-pg", sort: "tps" };
    // `sort` is a fleet-table SortKey and the queries list has no such column,
    // `q`/`health` aren't on the param type, `limit`/`cursor` are paging the
    // hook doesn't own, `db` has no UrlState field — so all five stay unset.
    expect(Object.keys(listParamsFrom(state, now)).sort()).toEqual(["cluster", "from", "to"]);
    expect(Object.keys(listParamsFrom(DEFAULT_STATE, now)).sort()).toEqual(["from", "to"]);
  });
});
