/* The Active Queries hook — `fetchActiveQueries` (client.ts:447) behind R10.22's
 * refetch envelope.
 *
 * The template is `useQueryDetail.ts` (read 2026-11-24, the 172-line file as of
 * `2223be9`), not `useQueries.ts`, and the reason is the param type rather than
 * the page: `ActiveQueriesParams` (client.ts:442-445) requires both `namespace`
 * and `cluster`, while `QueryListParams` (client.ts:282) makes all eight of
 * its fields optional. An all-optional bag names a *collection* — the filtered
 * set — so a params change there is a refresh of the same thing and the
 * `setState`-`prev` form is enough. A required pair names one *subject*: one
 * cluster's sessions. Move from `freeidp/freeidp-pg` to
 * `freeidp/replica-pg` and the payload in state is another cluster's sessions,
 * and showing them under the new heading is a wrong frame, not a stale one —
 * which is R10.22's rule for a subject change, and it is the reason the held
 * payload and its key live in one ref here too.
 *
 * What came across verbatim from the template, and where it lives there:
 *   - `Phase` *imported* from `./useFleet` (:15) rather than restated — the
 *     state layer has one declaration of it the way R16.8 wants a contract to;
 *   - abort-previous-on-new-request (:121) and abort-on-unmount (:162);
 *   - the hold-previous pair `phase: held === null ? "loading" : "ready"` +
 *     `refreshing: held !== null` (:135-140) and its twin in the catch
 *     (:151-156) — a failed refresh keeps the last good frame rather than
 *     replacing it with an error screen;
 *   - `err instanceof Error ? err.message : String(err)` (:149), so a
 *     `ContractError` surfaces as its own message instead of as
 *     `[object Object]` swallowed into an empty table;
 *   - the separate poll effect with its own `pollMs <= 0` off-switch (:69-73);
 *   - the caller contract that `params` is memoised (:91-101), because `params`
 *     is in `load`'s dep list (:158) exactly as `cfg` is in `useFleet.ts:62`.
 *
 * `pollMs` defaults to **15_000**, not the 30_000 the other four hooks use
 * (`useFleet.ts:31`, `useQueries.ts:111`, `useQueryDetail.ts:105`,
 * `useQuerySeries.ts:83`). R10.8 (requirements.md:419) names "a 15 s
 * auto-refresh indicator" for this page specifically, and an indicator that
 * counts to fifteen over a poll that runs every thirty seconds is a widget that
 * lies. This is the first hook whose interval comes from a page requirement
 * rather than the house default; the default itself is not asserted in the test
 * file, because none of the four existing hooks tests its own default either —
 * every test passes `pollMs` explicitly, and this one does too.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { fetchActiveQueries, type ActiveQueriesParams, type ClientConfig } from "../api/client";
import type { ActiveQueriesResponse } from "../api/types";
import type { UrlState } from "./urlState";
import type { Phase } from "./useFleet";

export interface ActiveQueriesState {
  phase: Phase;
  active: ActiveQueriesResponse | null;
  error: string | null;
  /** True when a refresh of *this* cluster is running over its own payload. */
  refreshing: boolean;
}

export interface UseActiveQueries {
  state: ActiveQueriesState;
  /** Force a refresh (the R16.7 suite and the error panel's Retry both call it). */
  refresh: () => void;
}

/** The two required params as one string, so "same subject?" is a string
 *  compare — the two-field form of `detailKey` (useQueryDetail.ts:62-64).
 *
 *  It looks redundant next to that one, because `state.cluster` is already
 *  "namespace/name" and this returns the same string. It is still a function and
 *  still exported for the same reason: the test asserts that *each* half
 *  participates, and a key written as `params.cluster` — the likelier typo once
 *  the pair is a single URL field — passes every other test while making a
 *  namespace move look like a refresh of the same subject. */
export function activeKey(params: ActiveQueriesParams): string {
  return `${params.namespace}/${params.cluster}`;
}

/** URL state → the params `fetchActiveQueries` takes.
 *
 *  `ActiveQueriesParams` requires both halves and both come from the one URL
 *  field (`state.cluster`, "namespace/name", urlState.ts:60-61) — the route
 *  cannot answer for the fleet, because `pg_stat_activity` is per-instance, so
 *  there is no cluster-less form of this page to fall back to.
 *
 *  The malformed-pair rules are `detailParamsFrom`'s (useQueryDetail.ts:83-84)
 *  verbatim: a pair needs a non-empty name on both sides of the slash, so
 *  "noslash", "ns/" and "/name" are all `null`. `row` is not consulted — this
 *  page's subject is the cluster, not a row of it. */
export function activeParamsFrom(state: UrlState): ActiveQueriesParams | null {
  if (state.cluster === null) return null;

  const slash = state.cluster.indexOf("/");
  if (slash <= 0 || slash === state.cluster.length - 1) return null;
  return {
    namespace: state.cluster.slice(0, slash),
    cluster: state.cluster.slice(slash + 1),
  };
}

/** Caller contract, in one line: memoise `params` (the `useMemo(() =>
 *  activeParamsFrom(state), [state])` form, per the docblock above). */
export function useActiveQueries(
  cfg: ClientConfig,
  params: ActiveQueriesParams | null,
  pollMs = 15_000,
): UseActiveQueries {
  const [state, setState] = useState<ActiveQueriesState>({
    phase: "idle",
    active: null,
    error: null,
    refreshing: false,
  });
  const abort = useRef<AbortController | null>(null);
  /** The payload and the subject it belongs to, as one value: see the header. */
  const last = useRef<{ key: string | null; active: ActiveQueriesResponse | null }>({
    key: null,
    active: null,
  });

  const load = useCallback(async () => {
    abort.current?.abort();

    if (params === null) {
      last.current = { key: null, active: null };
      setState({ phase: "idle", active: null, error: null, refreshing: false });
      return;
    }

    const ctrl = new AbortController();
    abort.current = ctrl;
    const key = activeKey(params);
    // Same subject → hold the payload (R10.22). Different subject → drop it.
    const held = last.current.key === key ? last.current.active : null;

    setState({
      phase: held === null ? "loading" : "ready",
      active: held,
      error: null,
      refreshing: held !== null,
    });

    try {
      const next = await fetchActiveQueries(cfg, params, { signal: ctrl.signal });
      if (ctrl.signal.aborted) return;
      last.current = { key, active: next };
      setState({ phase: "ready", active: next, error: null, refreshing: false });
    } catch (err) {
      if (ctrl.signal.aborted) return;
      const message = err instanceof Error ? err.message : String(err);
      last.current = { key, active: held };
      setState({
        phase: held === null ? "error" : "ready",
        active: held,
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
