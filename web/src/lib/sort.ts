/* Sorting and filtering for the Fleet table (R10.4).
 *
 * R10.4 asks for a sortable table over every column it shows. Keeping the
 * comparators here, rather than inline in the component, is what lets the
 * Vitest suite prove the ordering without rendering anything — and it keeps
 * null handling in one place: a null always sorts last, in both directions,
 * because "no value" is not a small value and sorting it to the top of an
 * ascending list reads as if it were.
 */

import type { ClusterSummary } from "../api/types";
import type { SortDir, SortKey } from "../state/urlState";

/** The value a column sorts on. Numbers compare numerically, strings by
 *  locale-aware collation, nulls sort last. */
function sortValue(c: ClusterSummary, key: SortKey): number | string | null {
  switch (key) {
    case "name":
      return c.name;
    case "namespace":
      return c.namespace;
    case "health": {
      // Health is a string in the type but an ordinal in the UI: the order is
      // the one the health bar draws, so sorting by health and reading the bar
      // agree. `unknown` is last because it is the absence of a judgement.
      const order = { healthy: 0, degraded: 1, down: 2, unknown: 3 } as const;
      return order[c.health];
    }
    case "instances":
      return c.instances;
    case "pgMajor":
      return c.pgMajor;
    case "tps":
      return c.metrics?.tps ?? null;
    case "p95LatencyMs":
      return c.metrics?.p95LatencyMs ?? null;
    case "connectionsPct":
      return c.metrics?.connectionsPct ?? null;
    case "diskPct":
      return c.metrics?.diskPct ?? null;
    case "observedAt": {
      const ms = Date.parse(c.observedAt);
      return Number.isNaN(ms) ? null : ms;
    }
  }
}

/** Compare two cells. Returns 0 for a tie, including two nulls. */
export function compareCells(a: number | string | null, b: number | string | null): number {
  if (a === null && b === null) return 0;
  if (a === null) return 1; // nulls last, in both directions
  if (b === null) return -1;
  if (typeof a === "number" && typeof b === "number") return a - b;
  return String(a).localeCompare(String(b), "en", { numeric: true });
}

/** Sort a copy of `clusters` by `key`/`dir`.
 *
 * Ties break on `namespace/name` so the order is total and stable: two renders
 * of the same data never swap rows, which is what makes the R16.7 visual
 * snapshots diffable.
 */
export function sortClusters(
  clusters: readonly ClusterSummary[],
  key: SortKey,
  dir: SortDir,
): ClusterSummary[] {
  const sign = dir === "asc" ? 1 : -1;
  return [...clusters].sort((a, b) => {
    const primary = compareCells(sortValue(a, key), sortValue(b, key));
    if (primary !== 0) return primary * sign;
    // The tie-break always ascends: flipping it would make the direction
    // ambiguous for equal keys.
    return compareCells(`${a.namespace}/${a.name}`, `${b.namespace}/${b.name}`);
  });
}

export interface FilterCriteria {
  /** Namespaces to include; empty means all. */
  ns: readonly string[];
  /** Free text over name and namespace; empty means no text filter. */
  q: string;
  /** Health values to include; empty means all. */
  health: readonly string[];
}

/** Apply the URL's filters to a cluster list, preserving order. */
export function filterClusters(
  clusters: readonly ClusterSummary[],
  f: FilterCriteria,
): ClusterSummary[] {
  const needle = f.q.trim().toLowerCase();
  return clusters.filter((c) => {
    if (f.ns.length > 0 && !f.ns.includes(c.namespace)) return false;
    if (f.health.length > 0 && !f.health.includes(c.health)) return false;
    if (needle !== "") {
      const hay = `${c.namespace}/${c.name}`.toLowerCase();
      if (!hay.includes(needle)) return false;
    }
    return true;
  });
}

/** The namespace list for the filter control, alphabetical with counts. */
export function namespaceOptions(clusters: readonly ClusterSummary[]): { ns: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const c of clusters) counts.set(c.namespace, (counts.get(c.namespace) ?? 0) + 1);
  return [...counts.entries()]
    .map(([ns, count]) => ({ ns, count }))
    .sort((a, b) => a.ns.localeCompare(b.ns));
}
