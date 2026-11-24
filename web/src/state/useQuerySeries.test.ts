/* The batch hook's own rule — send the array whole — plus R10.22's four states
 * on the third and last envelope.
 *
 * The case this file exists for is the one a `slice(0, 50)` would pass every
 * other test with: the brief's no-clamp rule. `MaxBatchFingerprints` is 50 and
 * the server answers `InvalidRequest` above it rather than truncating, so the
 * hook's job is to *let that error reach the UI* — and the only way to assert
 * that is to count what went on the wire. Hence the 60-fingerprint test: it
 * asserts 59 separators in the `fingerprints=` value, which a `slice(0, 50)`
 * would fail with 49, and it is paired with the 400 that the same call gets back
 * so the "loud" half is covered too, not just the "sent" half.
 *
 * The rest is the R10.22 set the charter names (loading / hold-previous /
 * failed-refresh / failed-first-load), copied from the same `useFleet.ts` body
 * the other two hooks copy — see their files for the line-by-line provenance.
 */

import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

import { FIXTURE_QUERY_SERIES } from "../api/fixtures";
import type { ClientConfig, QuerySeriesParams } from "../api/client";
import { seriesParamsFor, useQuerySeries } from "./useQuerySeries";
import { DEFAULT_STATE, type TimeRange, type UrlState } from "./urlState";

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

/** `stub` + the URLs it was called with, so a test can read the query string. */
function stub(body: unknown, ok = true, status = 200, statusText = "OK"): { urls: string[] } {
  const urls: string[] = [];
  globalThis.fetch = (input: RequestInfo | URL) => {
    urls.push(String(input));
    return Promise.resolve(responseOf(body, ok, status, statusText));
  };
  return { urls };
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

const http: ClientConfig = { source: "http", apiBase: "", fixture: "fleet", delayMs: 0 };
const fixture: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };

/** `n` distinct fingerprints, `fp0…fp{n-1}`. */
const fps = (n: number) => Array.from({ length: n }, (_, i) => `fp${i}`);

/* The params objects are hoisted, not inlined at the call sites, for the reason
 * `useQueries.test.ts` spells out at its own `NO_PARAMS`: `useQuerySeries`'s
 * `load` lists `params` in its deps (:110, copied from `useFleet.ts:62`), so a
 * fresh object per render is a fresh `load` per render and the effect at
 * :112-115 refires on every one. `load` calls `setState`, so that is a render
 * loop and not merely an extra fetch — the first version of this file inlined
 * its literals and the worker died at a 4 GB heap. */
const ONE: QuerySeriesParams = { fingerprints: fps(1), range: "1h" };
const THREE: QuerySeriesParams = { fingerprints: fps(3), range: "1h" };
const SIXTY: QuerySeriesParams = { fingerprints: fps(60), range: "1d" };

describe("seriesParamsFor", () => {
  const state: UrlState = { ...DEFAULT_STATE, ns: ["open-webui"], cluster: "open-webui/open-webui-pg", range: "6h" };

  it("passes the fingerprints through and takes the rest from URL state", () => {
    const list = fps(3);
    const params: QuerySeriesParams = seriesParamsFor(list, state);
    // The array is the caller's, in the caller's order: no slice, no sort, no dedupe.
    expect(params.fingerprints).toEqual(list);
    expect(params.range).toBe("6h");
    expect(params.namespace).toBe("open-webui");
    expect(params.cluster).toBe("open-webui/open-webui-pg");
  });

  it("keeps all 60 fingerprints, so the cap stays the server's to enforce", () => {
    // The intersection the first version missed. The batch-cap block below
    // renders the hook with a hoisted SIXTY and reads the URL, so it never
    // calls seriesParamsFor; this describe's pass-through test calls it with 3,
    // and 3 sliced to 50 is still 3. So a `slice(0, 50)` inside seriesParamsFor
    // was invisible to both — the named case (R8.10's over-the-cap length) and
    // the function that owns the rule had never been in the same test.
    const list = fps(60);
    expect(seriesParamsFor(list, state).fingerprints).toEqual(list);
  });

  it("omits the namespace unless exactly one is selected", () => {
    expect(seriesParamsFor(fps(1), { ...DEFAULT_STATE, ns: [] }).namespace).toBeUndefined();
    expect(seriesParamsFor(fps(1), { ...DEFAULT_STATE, ns: ["a", "b"] }).namespace).toBeUndefined();
    expect(seriesParamsFor(fps(1), { ...DEFAULT_STATE, ns: ["a"] }).namespace).toBe("a");
  });

  it("carries every preset through the SeriesRange subset", () => {
    // `TimeRange`'s five are a subset of `SeriesRange`'s six (client.ts:355-357),
    // so each one assigns without a cast; the loop is the cheap half of that
    // claim, and the type system is the half `tsc` runs on every commit.
    for (const range of Object.keys({ "1h": 1, "6h": 1, "1d": 1, "7d": 1, "30d": 1 }) as TimeRange[]) {
      expect(seriesParamsFor(fps(1), { ...DEFAULT_STATE, range }).range).toBe(range);
    }
  });
});

describe("useQuerySeries refetch behaviour (R10.22)", () => {
  it("is `loading` with no series while the first request is in flight", async () => {
    const box = pending();
    const { result } = renderHook(() => useQuerySeries(http, ONE, 0));

    expect(result.current.state).toEqual({ phase: "loading", series: null, error: null, refreshing: false });

    box.resolve(FIXTURE_QUERY_SERIES);
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.series?.meta.total).toBe(FIXTURE_QUERY_SERIES.meta.total);
  });

  it("ignores a response whose request was superseded", async () => {
    // `useFleet.ts:36-38`'s abort-previous, copied — and the reason `pending()`
    // here went multi-slot: with one `resolve` slot, the second fetch overwrote
    // the first's resolver, so the superseded request could not be answered at
    // all and the `ctrl.signal.aborted` check was untestable.
    const box = pending();
    const { result, rerender } = renderHook(
      ({ params }: { params: QuerySeriesParams }) => useQuerySeries(http, params, 0),
      { initialProps: { params: ONE } },
    );
    expect(box.urls).toHaveLength(1);

    rerender({ params: THREE });
    expect(box.urls).toHaveLength(2);

    // Answer the *superseded* request and flush its continuation. Deleting the
    // aborted-check would land ONE's envelope here, so `series` would be
    // non-null while THREE is still in flight.
    box.resolveAt(0, FIXTURE_QUERY_SERIES);
    await act(async () => {
      await Promise.resolve();
    });
    expect(result.current.state.series).toBeNull();
    expect(result.current.state.phase).toBe("loading");

    box.resolve(FIXTURE_QUERY_SERIES);
    await waitFor(() => expect(result.current.state.series).not.toBeNull());
    expect(result.current.state.phase).toBe("ready");
  });

  it("holds the previous envelope and sets `refreshing` while a refresh is in flight", async () => {
    stub(FIXTURE_QUERY_SERIES);
    const { result } = renderHook(() => useQuerySeries(http, THREE, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    const first = result.current.state.series;

    const box = pending();
    act(() => result.current.refresh());

    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.series).toBe(first);
    expect(result.current.state.refreshing).toBe(true);

    // A batch envelope with one entry, so the new payload is distinguishable.
    box.resolve({ data: [], meta: { total: 0, truncated: false, generatedAt: FIXTURE_QUERY_SERIES.meta.generatedAt } });
    await waitFor(() => expect(result.current.state.series?.meta.total).toBe(0));
    expect(result.current.state.refreshing).toBe(false);
  });

  it("keeps the last good envelope and surfaces the error when a refresh fails", async () => {
    stub(FIXTURE_QUERY_SERIES);
    const { result } = renderHook(() => useQuerySeries(http, THREE, 0));
    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    const first = result.current.state.series;

    stub({ error: { code: "Internal", message: "store unavailable", details: ["collector"] } }, false, 500, "Internal Server Error");
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.state.error).not.toBeNull());

    expect(result.current.state.phase).toBe("ready");
    expect(result.current.state.series).toBe(first);
    expect(result.current.state.error).toBe("GET → 500 Internal: store unavailable — collector");
  });

  it("is `error` with no series when the first request fails", async () => {
    stub({ error: { code: "Internal", message: "store unavailable", details: ["collector"] } }, false, 500, "Internal Server Error");
    const { result } = renderHook(() => useQuerySeries(http, THREE, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("error"));
    expect(result.current.state.series).toBeNull();
  });

  it("serves the fixture arm, whose third entry is the empty-not-null one", async () => {
    let calls = 0;
    globalThis.fetch = () => {
      calls += 1;
      return Promise.resolve(responseOf({}));
    };
    // `THREE`, not a fresh literal: the fixture arm resolves in a microtask, so
    // an inlined params here loops faster than the http arms do — see the
    // hoisting note above the consts.
    const { result } = renderHook(() => useQuerySeries(fixture, THREE, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("ready"));
    expect(result.current.state.series).toBe(FIXTURE_QUERY_SERIES);
    expect(calls).toBe(0);
    // R8.3's `points: []` entry survives the parse rather than becoming a hole
    // the chart can't tell apart from a real zero — the thing `parseQuerySeries`
    // (and this week's `envelope.ts` fix) exists to police.
    expect(result.current.state.series?.data.some((e) => e.points.length === 0)).toBe(true);
  });
});

describe("the batch cap (no client-side clamp)", () => {
  it("sends 60 fingerprints as 60, not the server's cap of 50", () => {
    // The no-slice rule, measured on the wire: 60 joined values carry 59
    // separators. A `slice(0, 50)` here would send 49 and the chart would plot
    // 50 of 60 while looking like a complete answer.
    const { urls } = stub(FIXTURE_QUERY_SERIES);
    renderHook(() => useQuerySeries(http, SIXTY, 0));

    const url = urls[0] ?? "";
    const value = decodeURIComponent(url.split("fingerprints=")[1]?.split("&")[0] ?? "");
    expect(value.split(",").length).toBe(60);
    expect((url.match(/%2C/g) ?? []).length).toBe(59);
  });

  it("lets the server's InvalidRequest above the cap surface as the error", async () => {
    // The other half of the same argument: the hook doesn't clamp, so the 400 is
    // the UI's to show. Truncating would have made this call succeed with a
    // partial answer, which is exactly what R8.10 forbids.
    const { urls } = stub(
      { error: { code: "InvalidRequest", message: "at most 50 fingerprints per request", details: ["got 60"] } },
      false,
      400,
      "Bad Request",
    );
    const { result } = renderHook(() => useQuerySeries(http, SIXTY, 0));

    await waitFor(() => expect(result.current.state.phase).toBe("error"));
    expect(result.current.state.error).toBe(
      "GET → 400 InvalidRequest: at most 50 fingerprints per request — got 60",
    );
    expect(result.current.state.series).toBeNull();
    expect(urls[0]).toContain("fingerprints=");
  });
});
