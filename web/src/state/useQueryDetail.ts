/* The Query Detail hook — `fetchQueryDetail` (client.ts:323) behind R10.22's
 * refetch envelope.
 *
 * Copied from `useFleet.ts` (read 2026-11-23, the 76-line file as of `dd0b1ed`):
 * the `Phase` union imported from it (:15) rather than restated, the
 * abort-previous-on-new-request (:36-38) and abort-on-unmount (:66), the
 * `err instanceof Error ? err.message : String(err)` normalisation (:54) that
 * lets a `ContractError` surface as its own message rather than as
 * `[object Object]`, and the 30 s poll effect with its `pollMs <= 0` off-switch
 * (:69-73).
 *
 * The one place this departs from the template, and why. `useFleet` decides
 * "hold or blank?" from the `setState` callback's `prev` (:40-46), which works
 * because it has exactly one possible payload. A detail page can be looking at
 * any fingerprint, so the question needs a second input — *whose* payload is
 * this — and R10.22's hold-previous governs a refresh of the *same* payload:
 * holding query A's SQL, timings and plans under query B's heading is a wrong
 * frame, not a stale one. So the held payload and the key it belongs to live in
 * one ref (`last`), every write path updates the pair together, and the
 * decision is `last.current.key === key ? hold : drop`.
 *
 * A ref rather than reading `prev` from the updater: React runs a functional
 * updater during the render phase, not synchronously at the `setState` call, so
 * a value read out of it is not available to the code that runs on the line
 * below — and this hook needs it *before* the await, in the same tick that
 * decides `refreshing`. `useFleet` gets away with `prev` because it never needs
 * the old value before the await, only inside the update.
 *
 * `useQueries` and `useQuerySeries` don't need the key at all: their payloads
 * are the filtered set and the batch envelope, so a params change *is* a
 * refresh of the same collection. That asymmetry is why the rule is written
 * per-hook rather than hoisted into a shared `useResource` — three hooks whose
 * envelopes differ by one input are cheaper to read than an abstraction with a
 * flag for it.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { fetchQueryDetail, type ClientConfig, type QueryDetailParams } from "../api/client";
import type { QueryDetailView } from "../api/types";
import type { UrlState } from "./urlState";
import type { Phase } from "./useFleet";

export interface QueryDetailState {
  phase: Phase;
  detail: QueryDetailView | null;
  error: string | null;
  /** True when a refresh of *this* fingerprint is running over its own payload. */
  refreshing: boolean;
}

export interface UseQueryDetail {
  state: QueryDetailState;
  /** Force a refresh (the R16.7 suite and the error panel's Retry both call it). */
  refresh: () => void;
}

/** The three required params as one string, so "same subject?" is a string
 *  compare. Exported because the test asserts the key changes when *any* of the
 *  three changes — a key that ignored `namespace` would make a cluster move look
 *  like a refresh and hold the wrong detail. */
export function detailKey(params: QueryDetailParams): string {
  return `${params.namespace}/${params.cluster}/${params.fingerprint}`;
}

/** URL state → the params `fetchQueryDetail` takes.
 *
 *  `QueryDetailParams` (client.ts:317) requires all three of fingerprint,
 *  namespace and cluster — the route is scoped by the cluster pair and keyed by
 *  the fingerprint, and there is no "the" query to default to. Both halves come
 *  from the URL: the pair from `state.cluster` ("namespace/name",
 *  urlState.ts:60) and the fingerprint from `state.row`, which is what R10.16's
 *  Query Detail carries in the path instead of `?row=` (urlState.ts:167-169 and
 *  the matching comment at :216-219).
 *
 *  Returns `null` when either is missing. `withState` already degrades a
 *  fingerprint-less query-detail to the Queries list (urlState.ts:261-266), so
 *  `null` here is the hand-edited-URL case, and the page's job is to render its
 *  own "no such query" frame rather than fetch a route that answers 404. */
export function detailParamsFrom(state: UrlState): QueryDetailParams | null {
  if (state.cluster === null || state.row === null || state.row === "") return null;

  const slash = state.cluster.indexOf("/");
  if (slash <= 0 || slash === state.cluster.length - 1) return null;
  return {
    fingerprint: state.row,
    namespace: state.cluster.slice(0, slash),
    cluster: state.cluster.slice(slash + 1),
  };
}

/** Caller contract, in one line: memoise `params`.
 *
 *  `params` is in `load`'s dep list (:158 below, copied from `useFleet.ts:62`),
 *  so a fresh object per render is a fresh `load` per render and the effect at
 *  :160-163 refires on every one — and since `load` calls `setState`, that is a
 *  render loop rather than merely an extra fetch. A page passes
 *  `useMemo(() => detailParamsFrom(state), [state])`; the test file's hoisted
 *  `A`/`B` are the test's version of the same memoise. `useQueries.ts:22-28`
 *  states the rule for its own hook, and `App.tsx:85-88` is the shell's precedent
 *  for `cfg`. */
export function useQueryDetail(
  cfg: ClientConfig,
  params: QueryDetailParams | null,
  pollMs = 30_000,
): UseQueryDetail {
  const [state, setState] = useState<QueryDetailState>({
    phase: "idle",
    detail: null,
    error: null,
    refreshing: false,
  });
  const abort = useRef<AbortController | null>(null);
  /** The payload and the subject it belongs to, as one value: see the header. */
  const last = useRef<{ key: string | null; detail: QueryDetailView | null }>({
    key: null,
    detail: null,
  });

  const load = useCallback(async () => {
    abort.current?.abort();

    if (params === null) {
      last.current = { key: null, detail: null };
      setState({ phase: "idle", detail: null, error: null, refreshing: false });
      return;
    }

    const ctrl = new AbortController();
    abort.current = ctrl;
    const key = detailKey(params);
    // Same subject → hold the payload (R10.22). Different subject → drop it.
    const held = last.current.key === key ? last.current.detail : null;

    setState({
      phase: held === null ? "loading" : "ready",
      detail: held,
      error: null,
      refreshing: held !== null,
    });

    try {
      const next = await fetchQueryDetail(cfg, params, { signal: ctrl.signal });
      if (ctrl.signal.aborted) return;
      last.current = { key, detail: next };
      setState({ phase: "ready", detail: next, error: null, refreshing: false });
    } catch (err) {
      if (ctrl.signal.aborted) return;
      const message = err instanceof Error ? err.message : String(err);
      last.current = { key, detail: held };
      setState({
        phase: held === null ? "error" : "ready",
        detail: held,
        error: message,
        refreshing: false,
      });
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
