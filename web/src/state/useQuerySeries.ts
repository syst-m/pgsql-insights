/* The batched-series hook — `fetchQuerySeries` (`client.ts:459`) behind R10.22's
 * refetch envelope.
 *
 * Copied from `useFleet.ts` (read 2026-11-23, the 76-line file as of `dd0b1ed`):
 * the `Phase` union imported from it (:15), the abort-previous-on-new-request
 * (:36-38) and abort-on-unmount (:66), the R10.22 hold-previous pair at
 * :40-46 and its twin in the catch at :55-60, the
 * `err instanceof Error ? err.message : String(err)` normalisation (:54), and
 * the 30 s poll effect (:69-73). No subject-key rule (that is `useQueryDetail`'s
 * problem): the payload here is R8.11's batch envelope, one entry per
 * fingerprint, so a params change is a refresh of the same collection and the
 * previous envelope is a legitimate thing to hold.
 *
 * The rule the brief singles out, and the one this file can get wrong quietly:
 * **the hook never slices `fingerprints` to 50.** `MaxBatchFingerprints` is 50
 * (`internal/api/series.go:56`, quoted at `client.ts:430`) and the server
 * answers `InvalidRequest` above it rather than truncating, so a `slice(0, 50)`
 * here would turn a loud server error into a chart that silently plots 50 of 80
 * — the exact "cannot tell '30 of them have no data' from 'you were cut off'"
 * failure R8.10 forbids the clamp for. `params` goes to the fetcher verbatim and
 * the error surfaces through the same `error` field any other failure uses.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { fetchQuerySeries, type ClientConfig, type QuerySeriesParams } from "../api/client";
import type { QuerySeriesEnvelope } from "../api/types";
import type { UrlState } from "./urlState";
import type { Phase } from "./useFleet";

export interface QuerySeriesState {
  phase: Phase;
  series: QuerySeriesEnvelope | null;
  error: string | null;
  /** True when a refresh is running over an existing payload (R10.22). */
  refreshing: boolean;
}

export interface UseQuerySeries {
  state: QuerySeriesState;
  /** Force a refresh (the R16.7 suite and the error panel's Retry both call it). */
  refresh: () => void;
}

/** The rows a page is showing + the URL's scope → the params `fetchQuerySeries`
 *  takes.
 *
 *  The fingerprints come from the caller (a table's rows, or one fingerprint for
 *  R10.7's detail chart — R8.11's batch-of-one) because they are not URL state;
 *  everything else is, and comes from `urlState.ts` rather than being re-derived:
 *  `range` maps straight through (`TimeRange`'s five presets are a subset of
 *  `SeriesRange`'s six — `client.ts:409-421` is the phasing note that keeps them
 *  apart, and the reason this assignment needs no cast), and `namespace`/
 *  `cluster` follow the same single-selection rule as `listParamsFrom`
 *  (useQueries.ts): the params are single strings, `UrlState.ns` is a list, so
 *  a multi-namespace filter sends neither rather than silently picking one.
 *
 *  `fingerprints` is passed through whole. No slice, no sort, no dedupe — see
 *  the header for why, and `client.ts:425-430` for the fetcher's own version of
 *  the same argument. */
export function seriesParamsFor(fingerprints: readonly string[], state: UrlState): QuerySeriesParams {
  const params: QuerySeriesParams = { fingerprints, range: state.range };

  const [onlyNamespace] = state.ns;
  if (state.ns.length === 1 && onlyNamespace !== undefined) params.namespace = onlyNamespace;
  if (state.cluster !== null) params.cluster = state.cluster;
  return params;
}

/** Caller contract, in one line: memoise `params`.
 *
 *  `params` is in `load`'s dep list (:120 below, copied from `useFleet.ts:62`),
 *  so a fresh object per render is a fresh `load` per render and the effect at
 *  :122-125 refires on every one — and since `load` calls `setState`, that is a
 *  render loop rather than merely an extra fetch. A page passes
 *  `useMemo(() => seriesParamsFor(fps, state), [fps, state])`; the test file's
 *  hoisted `ONE`/`THREE`/`SIXTY` are the test's version of the same memoise.
 *  `useQueries.ts:22-28` states the rule for its own hook, and `App.tsx:85-88`
 *  is the shell's precedent for `cfg`. */
export function useQuerySeries(
  cfg: ClientConfig,
  params: QuerySeriesParams,
  pollMs = 30_000,
): UseQuerySeries {
  const [state, setState] = useState<QuerySeriesState>({
    phase: "idle",
    series: null,
    error: null,
    refreshing: false,
  });
  const abort = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;

    setState((prev) => ({
      // Keep the old curves on screen while the new ones arrive (R10.22).
      phase: prev.series === null ? "loading" : "ready",
      series: prev.series,
      error: null,
      refreshing: prev.series !== null,
    }));

    try {
      const next = await fetchQuerySeries(cfg, params, { signal: ctrl.signal });
      if (ctrl.signal.aborted) return;
      setState({ phase: "ready", series: next, error: null, refreshing: false });
    } catch (err) {
      if (ctrl.signal.aborted) return;
      const message = err instanceof Error ? err.message : String(err);
      setState((prev) => ({
        phase: prev.series === null ? "error" : "ready",
        series: prev.series,
        error: message,
        refreshing: false,
      }));
    }
  }, [cfg, params]);

  useEffect(() => {
    void load();
    return () => abort.current?.abort();
  }, [load]);

  useEffect(() => {
    if (pollMs <= 0) return;
    const id = setInterval(() => void load(), pollMs);
    return () => clearInterval(id);
  }, [load, pollMs]);

  return { state, refresh: () => void load() };
}
