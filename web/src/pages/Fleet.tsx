/* The Fleet page (R10.4).
 *
 * Layout order is the reading order a fleet view needs: the health distribution
 * first (how many of each), then the table (which ones), with the filter row
 * above everything it scopes (interaction.md). The page owns no state of its
 * own beyond the data — every control writes to the URL (R10.16), so the
 * rendered page is a pure function of the URL plus the last payload.
 *
 * The one exception is R10.20's column selection, and it is an exception by
 * design: R10.20's contract is a *persisted per-view* preference, which is a
 * localStorage fact with a `viewId` key, not a navigable one — `?columns=` in
 * the URL would put a shared preference in the back button. `useColumnVisibility
 * ("fleet", COLUMNS)` is the same hook the Queries page uses, and mounting it
 * here is what makes R10.20's "every wide table" true of both tables rather than
 * one (A3's S22 — the menu existed, was tested, and was unreachable on Fleet).
 */

import { useMemo } from "react";

import type { ClientConfig } from "../api/client";
import type { ClusterHealth, FleetResponse } from "../api/types";
import { HEALTH_ORDER, presentationFor } from "../api/health";
import { formatTimestamp } from "../lib/format";
import { filterClusters, namespaceOptions, sortClusters } from "../lib/sort";
import { TIME_RANGES, type SortDir, type SortKey, type TimeRange, type UrlState } from "../state/urlState";
import { useFleet } from "../state/useFleet";
import { HealthBar } from "../components/HealthBar";
import { ClusterTable, COLUMNS } from "../components/ClusterTable";
import { ColumnMenu, useColumnVisibility } from "../components/ColumnMenu";
import { StatePanel, derivePanelState, copyFor } from "../components/StatePanel";

export interface FleetPageProps {
  state: UrlState;
  onPatch: (patch: Partial<UrlState>) => void;
  cfg: ClientConfig;
  /** The visual suite renders a named fixture directly instead of fetching. */
  fixtureOverride?: FleetResponse;
}

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
  // R10.20's per-view selection, above the early return because it is a hook and
  // because the menu has to exist in the same render as the table it controls.
  const columns = useColumnVisibility({ viewId: "fleet", columns: COLUMNS });

  // Full-panel states replace the body entirely (R10.12's named fixtures).
  if (panel === "loading" || panel === "empty" || panel === "error") {
    return (
      <div className="page" data-page="fleet">
        <PageHeader state={state} fleet={fleet} onPatch={onPatch} />
        <StatePanel state={panel} error={data.error ?? undefined} onAction={refresh} />
      </div>
    );
  }

  // The response, not `rows`: the banner counts what the API served, and a
  // filter is a view over that set rather than a smaller response.
  const bannerCopy = copyFor(panel, fleet ?? undefined);

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
          <div className="card-actions">
            <span className="card-meta num">{rows.length} shown</span>
            <ColumnMenu columns={COLUMNS} visible={columns.visible} onChange={columns.setVisible} />
          </div>
        </div>
        <div className={data.refreshing ? "hold-previous" : undefined}>
          <ClusterTable
            rows={rows}
            state={state}
            columns={columns.visible}
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
  fleet: FleetResponse | null;
  onPatch: (patch: Partial<UrlState>) => void;
}) {
  return (
    <header className="page-head">
      <div>
        <h1 className="page-title">Fleet</h1>
        {fleet !== null && (
          <p className="page-meta" title={`${fleet.meta.generatedAt} UTC`}>
            {/* R10.21: local zone, zone labelled, absolute UTC on hover. The
                stamp is meta.generatedAt — the snapshot's, not the cluster's
                (that is createdAt, which the row also carries). */}
            Data as of {formatTimestamp(fleet.meta.generatedAt)}
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
            {TIME_RANGES.map((r) => (
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
