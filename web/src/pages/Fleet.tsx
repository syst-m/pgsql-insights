/* The Fleet page (R10.4).
 *
 * Layout order is the reading order a fleet view needs: the health distribution
 * first (how many of each), then the table (which ones), with the filter row
 * above everything it scopes (interaction.md). The page owns no state of its
 * own beyond the data — every control writes to the URL (R10.16), so the
 * rendered page is a pure function of the URL plus the last payload.
 */

import { useMemo } from "react";

import type { ClientConfig } from "../api/client";
import type { ClusterHealth } from "../api/types";
import { HEALTH_ORDER, presentationFor } from "../api/health";
import { formatTimestamp } from "../lib/format";
import { filterClusters, namespaceOptions, sortClusters } from "../lib/sort";
import type { SortDir, SortKey, TimeRange, UrlState } from "../state/urlState";
import { useFleet } from "../state/useFleet";
import { HealthBar } from "../components/HealthBar";
import { ClusterTable } from "../components/ClusterTable";
import { StatePanel, derivePanelState, copyFor } from "../components/StatePanel";
import { FIXTURE_FLEET } from "../api/fixtures";

export interface FleetPageProps {
  state: UrlState;
  onPatch: (patch: Partial<UrlState>) => void;
  cfg: ClientConfig;
  /** The visual suite renders a named fixture directly instead of fetching. */
  fixtureOverride?: typeof FIXTURE_FLEET;
}

const RANGES: readonly TimeRange[] = ["1h", "24h", "7d", "30d"];

export function FleetPage({ state, onPatch, cfg, fixtureOverride }: FleetPageProps) {
  const { state: data, refresh } = useFleet(cfg);
  const fleet = fixtureOverride ?? data.fleet;
  const phase = fixtureOverride !== undefined ? ("ready" as const) : data.phase;

  const panel = useMemo(
    () => derivePanelState({ phase, fleet, error: data.error ?? undefined }),
    [phase, fleet, data.error],
  );

  // Filters and sort are separate steps: the chart shows the filtered set, the
  // table shows the filtered set ordered. Sorting must not change the chart.
  const filtered = useMemo(
    () => filterClusters(fleet?.clusters ?? [], { ns: state.ns, q: state.q, health: state.health }),
    [fleet, state.ns, state.q, state.health],
  );
  const rows = useMemo(
    () => sortClusters(filtered, state.sort, state.dir),
    [filtered, state.sort, state.dir],
  );

  // Full-panel states replace the body entirely (R10.12's named fixtures).
  if (panel === "loading" || panel === "empty" || panel === "error") {
    return (
      <div className="page" data-page="fleet">
        <PageHeader state={state} fleet={fleet} onPatch={onPatch} />
        <StatePanel state={panel} error={data.error ?? undefined} onAction={refresh} />
      </div>
    );
  }

  const bannerCopy = copyFor(panel);

  return (
    <div className="page" data-page="fleet">
      <PageHeader state={state} fleet={fleet} onPatch={onPatch} />

      {bannerCopy !== null && (
        <div className="banner" role="status">
          <strong>{bannerCopy.title}</strong>
          <span>{bannerCopy.body}</span>
        </div>
      )}

      <FilterRow state={state} onPatch={onPatch} namespaces={namespaceOptions(fleet?.clusters ?? [])} />

      {filtered.length > 0 && (
        <section className="card">
          <HealthBar clusters={filtered} />
        </section>
      )}

      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Clusters</h2>
          <span className="card-meta num">{rows.length} shown</span>
        </div>
        <div className={data.refreshing ? "hold-previous" : undefined}>
          <ClusterTable
            rows={rows}
            state={state}
            onSort={(key) => onPatch({ sort: key, dir: nextDir(state, key) })}
            onOpen={(id) => onPatch({ page: "cluster", cluster: id })}
          />
        </div>
      </section>
    </div>
  );
}

/** Clicking the active column flips its direction; a new column sorts the way
 *  that puts the interesting end first (names A→Z, counts high→low). */
function nextDir(state: UrlState, key: SortKey): SortDir {
  if (state.sort === key) return state.dir === "asc" ? "desc" : "asc";
  return key === "name" || key === "namespace" ? "asc" : "desc";
}

function PageHeader({
  state,
  fleet,
  onPatch,
}: {
  state: UrlState;
  fleet: { generatedAt: string } | null;
  onPatch: (patch: Partial<UrlState>) => void;
}) {
  return (
    <header className="page-head">
      <div>
        <h1 className="page-title">Fleet</h1>
        {fleet !== null && (
          <p className="page-meta" title={`${fleet.generatedAt} UTC`}>
            {/* R10.21: local zone, zone labelled, absolute UTC on hover. */}
            Data as of {formatTimestamp(fleet.generatedAt)}
          </p>
        )}
      </div>
      <div className="page-actions">
        <label className="field">
          <span className="field-label">Range</span>
          <select
            value={state.range}
            onChange={(e) => onPatch({ range: e.target.value as TimeRange })}
          >
            {RANGES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
      </div>
    </header>
  );
}

/** The filter row: one row, above everything it scopes (interaction.md). */
function FilterRow({
  state,
  onPatch,
  namespaces,
}: {
  state: UrlState;
  onPatch: (patch: Partial<UrlState>) => void;
  namespaces: { ns: string; count: number }[];
}) {
  const ns = state.ns[0] ?? "";
  const health = state.health[0] ?? "";
  return (
    <div className="filter-row">
      <label className="field">
        <span className="field-label">Namespace</span>
        <select value={ns} onChange={(e) => onPatch({ ns: e.target.value === "" ? [] : [e.target.value] })}>
          <option value="">All namespaces</option>
          {namespaces.map((o) => (
            <option key={o.ns} value={o.ns}>
              {o.ns} ({o.count})
            </option>
          ))}
        </select>
      </label>

      <label className="field grow">
        <span className="field-label">Search</span>
        <input
          type="search"
          value={state.q}
          placeholder="cluster name"
          onChange={(e) => onPatch({ q: e.target.value })}
        />
      </label>

      <label className="field">
        <span className="field-label">Health</span>
        <select
          value={health}
          onChange={(e) =>
            onPatch({ health: e.target.value === "" ? [] : [e.target.value as ClusterHealth] })
          }
        >
          <option value="">All health</option>
          {HEALTH_ORDER.map((h) => (
            <option key={h} value={h}>
              {presentationFor(h).label}
            </option>
          ))}
        </select>
      </label>
    </div>
  );
}
