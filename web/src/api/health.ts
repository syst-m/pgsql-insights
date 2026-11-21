/* The health rollup, and the non-colour encoding that goes with it (R10.23).
 *
 * R1.2 is the reason this is a function and not a field copy: "a pod can be
 * Running with its `postgres` container in CrashLoopBackOff, and a CR reporting
 * 2 instances can have **zero** serving (measured on freeidp-pg). A collector
 * that trusts the count renders a green cluster that is fully down." So the
 * judgement is computed from the instance counts, and `status.phase` is only
 * consulted once the counts agree.
 */

import type { ClusterHealth } from "./types";

export interface HealthInput {
  phase: string;
  instances: number;
  readyInstances: number;
  servingInstances: number;
}

/** CNPG's healthy phase string, verbatim from the reference clusters. */
const HEALTHY_PHASE = "Cluster in healthy state";

export function healthFor(c: HealthInput): ClusterHealth {
  if (c.instances <= 0) {
    // Nothing to judge: a CR with no instances is not a cluster we can call
    // healthy or down, and rendering it as either would be a guess.
    return "unknown";
  }
  if (c.servingInstances === 0) {
    // The freeidp-pg case: the CR reports instances, zero pods serve.
    return "down";
  }
  if (c.servingInstances < c.instances || c.readyInstances < c.servingInstances) {
    return "degraded";
  }
  if (c.phase !== HEALTHY_PHASE) {
    // Every pod serves and the CR still reports a problem (e.g. a pending
    // job or a failed switchover): not down, not healthy.
    return "degraded";
  }
  return "healthy";
}

export interface HealthPresentation {
  label: string;
  /** The status token this health colours itself with. Never a series token:
   *  status colour is reserved for state, and a series colour never
   *  impersonates it (anti-patterns: "Status color used for a non-status
   *  series"). */
  token: string;
  /** R10.23's non-colour channel: the shape drawn beside the label. Light-mode
   *  warning and serious sit below 3:1 on the light surface by design, so the
   *  icon + label pairing is the mitigation, not the hue. */
  glyph: "circle" | "triangle" | "square" | "cross" | "dash";
}

const PRESENTATION: Record<ClusterHealth, HealthPresentation> = {
  healthy: { label: "Healthy", token: "var(--status-good)", glyph: "circle" },
  degraded: { label: "Degraded", token: "var(--status-warning)", glyph: "triangle" },
  down: { label: "Down", token: "var(--status-critical)", glyph: "cross" },
  // "Unknown" is deliberately not a status token: no data is not a state, and
  // borrowing `--status-serious` for it would read as an alarm.
  unknown: { label: "Unknown", token: "var(--text-muted)", glyph: "dash" },
};

export function presentationFor(h: ClusterHealth): HealthPresentation {
  return PRESENTATION[h];
}

/** The order the health distribution draws its segments in — worst first, so
 *  the bar reads the same on every page regardless of which states exist. */
export const HEALTH_ORDER: readonly ClusterHealth[] = [
  "healthy",
  "degraded",
  "down",
  "unknown",
] as const;
