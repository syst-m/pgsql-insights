/* The Queries list hook — `fetchQueries` (`client.ts:342`) behind R10.22's
 * refetch envelope.
 *
 * Copied from `useFleet.ts` (read 2026-11-23, the 76-line file as of `dd0b1ed`),
 * which the brief names as the template rather than a second pattern to invent.
 * What came across verbatim, and where it lives there:
 *   - the four-field state object and its `phase` union — `Phase` is *imported*
 *     from `./useFleet` (:15) rather than restated, so the state layer has one
 *     declaration of it the way R16.8 wants the contract to have one;
 *   - abort-previous-on-new-request (:36-38) and abort-on-unmount (:66);
 *   - R10.22's hold-previous rule, which is the
 *     `phase: prev.fleet === null ? "loading" : "ready"` +
 *     `refreshing: prev.fleet !== null` pair at :40-46, and the same pair in
 *     the catch at :55-60 — a failed refresh keeps the last good frame rather
 *     than replacing it with an error screen;
 *   - `err instanceof Error ? err.message : String(err)` (:54), which is what
 *     makes a `ContractError` surface as its own message instead of as
 *     `[object Object]` swallowed into a blank table (the R16.8 bug class);
 *   - the separate 30 s poll effect with its own `pollMs <= 0` off-switch
 *     (:69-73).
 *
 * What the brief adds on top of the copy: `QueryListParams` (`client.ts:331`) is
 * *fed* from urlState.ts rather than re-derived here, which is what
 * `listParamsFrom` below is for — exported and pure so a page can memoise the
 * result. `useFleet.ts:62` puts `cfg` in `load`'s dep list, so params identity
 * is load identity here exactly as cfg identity is there: a caller that builds a
 * fresh object every render refetches every render. App.tsx:85-88 memoises `cfg`
 * for the same reason; a page passing params has to do the same to them.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { fetchQueries, type ClientConfig, type QueryListParams } from "../api/client";
import type { QueryResponse } from "../api/types";
import type { TimeRange, UrlState } from "./urlState";
import type { Phase } from "./useFleet";

export interface QueriesState {
  phase: Phase;
  queries: QueryResponse | null;
  error: string | null;
  /** True when a refresh is running over an existing payload (R10.22). */
  refreshing: boolean;
}

export interface UseQueries {
  state: QueriesState;
  /** Force a refresh (the R16.7 suite and the error panel's Retry both call it). */
  refresh: () => void;
}

/** Each preset's window in seconds — the five `TIME_RANGES` (urlState.ts:141)
 *  enumerates, and the reason `listParamsFrom` needs a clock at all: the list
 *  route has no `range` parameter (`QueryListParams`' fields are `from`/`to`,
 *  `client.ts:331-340`), so a preset has to become a window before it can be sent.
 *
 *  No `custom` entry, because `TIME_RANGES` has none yet — urlState.ts:132-135
 *  phases it with the cell's auto-refresh-disable rule. When it lands, the entry
 *  appears here and `TimeRange` gains its sixth member in the same commit; the
 *  `Record<TimeRange, number>` type is what makes forgetting this map the
 *  compile error, which is the point of typing it as a Record rather than
 *  reading it with a lookup that could miss. */
const RANGE_SECONDS: Record<TimeRange, number> = {
  "1h": 3_600,
  "6h": 21_600,
  "1d": 86_400,
  "7d": 604_800,
  "30d": 2_592_000,
};

/** URL state → the params `fetchQueries` takes, as a pure function so the clock
 *  is an argument and not a hidden dependency (a bare `new Date()` inside the
 *  hook would put a value that changes every render in `load`'s deps and defeat
 *  the memoise the docblock above is about).
 *
 *  Four of the eight fields map. `namespace` only when exactly one namespace is
 *  selected: the param is a single string and `UrlState.ns` is a list, so a
 *  two-namespace filter has no faithful encoding, and sending the first would
 *  silently narrow the result set to a namespace the user did not pick alone —
 *  the same "no silent clamp" argument `client.ts:425-430` makes for the batch
 *  parameter. `cluster` maps when set. `from`/`to` come from `range` + `now`,
 *  formatted without milliseconds to match `time.RFC3339` as
 *  `internal/api/series.go:178` formats it (R10.21: every timestamp the API
 *  emits is RFC 3339 UTC).
 *
 *  `sort` is deliberately not mapped: `SortKey` (urlState.ts:37-47) names the
 *  *fleet table's* columns — `health`, `tps`, `diskPct` — and none of them is a
 *  query-list column, so forwarding `health` to `/insights/queries` would ask
 *  that route to sort by a field it does not have. `q` and `health` are not on
 *  the param type at all, so the text and health filters stay where the page
 *  applies them today. `limit`/`cursor` are paging, which this hook does not
 *  own yet; `db` has no `UrlState` field to come from. */
export function listParamsFrom(state: UrlState, now: Date = new Date()): QueryListParams {
  const params: QueryListParams = {};

  const [onlyNamespace] = state.ns;
  if (state.ns.length === 1 && onlyNamespace !== undefined) params.namespace = onlyNamespace;
  if (state.cluster !== null) params.cluster = state.cluster;

  const seconds = RANGE_SECONDS[state.range];
  params.from = trimMs(new Date(now.getTime() - seconds * 1_000));
  params.to = trimMs(now);
  return params;
}

/** `2026-11-23T12:00:00.000Z` → `2026-11-23T12:00:00Z`. */
function trimMs(d: Date): string {
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

export function useQueries(cfg: ClientConfig, params: QueryListParams, pollMs = 30_000): UseQueries {
  const [state, setState] = useState<QueriesState>({
    phase: "idle",
    queries: null,
    error: null,
    refreshing: false,
  });
  const abort = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;

    setState((prev) => ({
      // Keep the old rows on screen while the new ones arrive (R10.22).
      phase: prev.queries === null ? "loading" : "ready",
      queries: prev.queries,
      error: null,
      refreshing: prev.queries !== null,
    }));

    try {
      const next = await fetchQueries(cfg, params, { signal: ctrl.signal });
      if (ctrl.signal.aborted) return;
      setState({ phase: "ready", queries: next, error: null, refreshing: false });
    } catch (err) {
      if (ctrl.signal.aborted) return;
      const message = err instanceof Error ? err.message : String(err);
      setState((prev) => ({
        phase: prev.queries === null ? "error" : "ready",
        queries: prev.queries,
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
