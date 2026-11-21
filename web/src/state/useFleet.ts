/* The one data-fetching hook (R10.22's refetch behaviour lives here).
 *
 * R10.22: a background refresh must not blank the page. The previous payload
 * stays in state while a new one is in flight, so the table keeps its rows at
 * reduced opacity (`.hold-previous`) instead of flashing a skeleton — and a
 * failed refresh keeps the last good frame rather than replacing it with an
 * error screen.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { fetchFleet, type ClientConfig } from "../api/client";
import type { FleetResponse } from "../api/types";

export type Phase = "idle" | "loading" | "ready" | "error";

export interface FleetState {
  phase: Phase;
  fleet: FleetResponse | null;
  error: string | null;
  /** True when a refresh is running over an existing payload. */
  refreshing: boolean;
}

export interface UseFleet {
  state: FleetState;
  /** Force a refresh (the R16.7 suite and the error panel's Retry both call it). */
  refresh: () => void;
}

export function useFleet(cfg: ClientConfig, pollMs = 30_000): UseFleet {
  const [state, setState] = useState<FleetState>({ phase: "idle", fleet: null, error: null, refreshing: false });
  const abort = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;

    setState((prev) => ({
      // Keep the old rows on screen while the new ones arrive (R10.22).
      phase: prev.fleet === null ? "loading" : "ready",
      fleet: prev.fleet,
      error: null,
      refreshing: prev.fleet !== null,
    }));

    try {
      const next = await fetchFleet(cfg, { signal: ctrl.signal });
      if (ctrl.signal.aborted) return;
      setState({ phase: "ready", fleet: next, error: null, refreshing: false });
    } catch (err) {
      if (ctrl.signal.aborted) return;
      const message = err instanceof Error ? err.message : String(err);
      setState((prev) => ({
        phase: prev.fleet === null ? "error" : "ready",
        fleet: prev.fleet,
        error: message,
        refreshing: false,
      }));
    }
  }, [cfg]);

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
