/* The System Insights page — R10.10's cluster page (requirements.md:423), the
 * eleventh of R10.3's fourteen (web/src/routes.ts:280-290), and the only one of
 * the fourteen whose Phase has not started.
 *
 * ── what it renders, and from what ──────────────────────────────────────────
 *
 * A3's endpoint ruling on this row (requirements.md:423) names two routes and
 * neither has a handler: `/api/v1/insights/system` for the group aggregate and
 * `/api/v1/insights/system/{metric}` for each tab's series — R8.2's pair, filed
 * at api/x-planned-paths.yaml:150-151. `web/src/api/client.ts` has no fetcher
 * for either (the standing ask on A4), so the page cannot ask. That is R10.12's
 * *unavailable* state, and the ruling is explicit that the state is the
 * deliverable: "It renders R10.12's unavailable state rather than invented data
 * while `/api/v1/insights/system` has no handler … that state is the
 * requirement, not a placeholder."
 *
 * So nothing here plots a number the API has not returned. What the page renders
 * instead is the requirement's own inventory — the metric groups R5.2 and R5.3
 * enumerate, the five cards R5.1 names, the toolbar controls R5.6 names, the
 * instances R9.3's identity triple makes selectable — each of which is a fact the
 * spec states today, plus one value the spec states outright (the Log errors
 * card, below).
 *
 * The second condition on the ruling — "The file lands as its own commit, not
 * inside Phase 6b's phase-closing commit" — is why this file and its test are one
 * commit while the rest of 6b's pages sit in theirs. The `SystemPanelInput` /
 * `SYSTEM_COPY` rows in `web/src/components/StatePanel.tsx` ride along in it
 * because R10.12 requires the page to name its states and that file is where this
 * charter keeps them; both insertions are below every existing line cite in that
 * file, so no cite in `requirements.md`, `IMPLEMENTATION.md`,
 * `docs/reviews/finding-index.md` or `web/src/pages/Queries.tsx` moves. The
 * precedent for building a later phase's page early is this charter's own
 * `be7f042` (R10.9's Wait Events, Phase 6a's, mounted by A4 at `7872d7e`).
 *
 * ── the payload type, and why it is here ────────────────────────────────────
 *
 * R16.8 pins `web/src/api/types.ts` to `internal/api/dto/*.go` field-for-field,
 * and the Phase 7 DTO does not exist. The ruling names the shipped precedent —
 * `web/src/components/ResourceLoadPanel.tsx:59-64` — and its rule: declare the
 * type in the component from the requirement's own sentence, mark it the standing
 * ask on A2, and move it to `types.ts` when the Go DTO does. `SystemGroupView`
 * below is that declaration, and its three fields are the three the requirements
 * name: the group (R10.10's tabs are the groups), the PromQL (R5.6's external
 * link is "built from the panel's PromQL"), and the aggregate value (A3's "the
 * group aggregate"). A2's ruling on the sibling type — `OsMetricsSource` lands in
 * `dto` as `OsSourcePrometheus` / `OsSourceMetricsServer` / `OsSourceNone` and
 * `SourceKey`'s `"not-asked"` stays component-local — is the same rule applied to
 * the field level, and it is why nothing here widens a Go union preemptively.
 *
 * The envelope is not interim. `Envelope<T>` (`web/src/api/types.ts:156`) is
 * R8.4's contract shape, every route returns it, and `web/src/pages/Tags.tsx`
 * already types an R8.2 route's payload as `Envelope<TagView>` for exactly that
 * reason — so the page's payload is `Envelope<SystemGroupView>` and only the row
 * type is provisional.
 *
 * ── R10.19 for this surface ────────────────────────────────────────────────
 *
 * R10.19 states the contract per chart type rather than once for the app, and
 * this page has eleven chart slots with none of the charts drawn, so the contract
 * is stated per slot and shipped in the parts that exist today:
 *
 *   - **hover**: every card here derives its `title` from the same table that
 *     renders its visible text (`SUMMARY_CARDS`, `METRIC_GROUPS`, `TOOLBAR`) —
 *     never a second string, the rule
 *     `web/src/components/ResourceLoadPanel.tsx:157-161` states for its own
 *     tooltip. The plots' crosshair-plus-all-series tooltip arrives with the
 *     charts and inherits `web/src/components/QueryLoadArea.tsx:125`'s shape.
 *   - **legend**: the tab strip is the legend for the group set and names every
 *     group whether or not its chart exists (R10.23's non-colour rule: the
 *     inventory is in text, not in a swatch). Each chart's own series legend
 *     arrives with the chart.
 *   - **click-through**: the tab strip (selects a group) and the compare selector
 *     (selects instances). Neither is a route. R10.16's URL grammar has `tab`,
 *     typed `ClusterTab` (`web/src/state/urlState.ts:56`), which enumerates the
 *     *cluster detail* tabs and has no value for a metric group — the same gap
 *     `web/src/pages/WaitEvents.tsx:26-31` documents for its drill, and the same
 *     answer: page state, not a URL slot.
 *   - **table twin** (R10.23): the summary-card row is the twin of the numbers
 *     the charts will plot; each chart's own twin arrives with the chart.
 */

import { useMemo, useState, type ReactElement } from "react";

import { FIXTURE_CLUSTERS } from "../api/fixtures";
import type { Envelope, FleetRow } from "../api/types";
import { buildPath, type PageProps } from "../routes";
import { formatNumber, formatTimestamp, NULL_VALUE } from "../lib/format";
import {
  copyFor,
  deriveSystemPanelState,
  StatePanel,
  SYSTEM_COPY,
  type SystemGroupView,
} from "../components/StatePanel";

/* ── the requirement's inventories ──────────────────────────────────────────── */

/** R10.10's metric groups, in the order the two source requirements list them:
 *  R5.3's OS families first, then R5.2's DB families in R5.2's own order. */
export type MetricGroupKey =
  | "os"
  | "transactions"
  | "tuples"
  | "blocks"
  | "temp"
  | "deadlocks"
  | "backends"
  | "sizes"
  | "age"
  | "wal"
  | "checkpoints";

export interface MetricGroup {
  key: MetricGroupKey;
  /** The tab label: the requirement's own word for the family, so the tab strip
   *  can be checked against R5.2/R5.3 by reading it (the same rule
   *  `web/src/routes.ts:134-135` applies to nav labels). */
  label: string;
  /** The measures the group's chart will plot, copied from the requirement's
   *  parenthetical where it has one. Stated now so the commit that draws a chart
   *  fills a named slot instead of inventing one. */
  measures: string;
  /** Which requirement the family comes from. */
  source: "R5.3" | "R5.2";
}

/** The eleven groups. R5.3's sentence is "Provide the OS metric families (CPU,
 *  memory, disk, network)"; R5.2's is "Provide the DB metric families:
 *  transactions (commit/rollback/TPS), tuples (returned/fetched/inserted/
 *  updated/deleted), blocks (hit/read, cache hit ratio), temp (files/bytes),
 *  deadlocks, backends by state and by application name, database sizes, TXID
 *  and MXID age, WAL volume and generation rate, checkpoints." Every entry below
 *  is copied from one of those two sentences; the list is not a design choice,
 *  which is what lets the test assert it against the requirement. */
export const METRIC_GROUPS: readonly MetricGroup[] = [
  { key: "os", label: "OS", measures: "CPU, memory, disk, network", source: "R5.3" },
  {
    key: "transactions",
    label: "Transactions",
    measures: "commit / rollback / TPS",
    source: "R5.2",
  },
  {
    key: "tuples",
    label: "Tuples",
    measures: "returned / fetched / inserted / updated / deleted",
    source: "R5.2",
  },
  {
    key: "blocks",
    label: "Blocks",
    measures: "hit / read, cache hit ratio",
    source: "R5.2",
  },
  { key: "temp", label: "Temp", measures: "files / bytes", source: "R5.2" },
  { key: "deadlocks", label: "Deadlocks", measures: "deadlocks", source: "R5.2" },
  {
    key: "backends",
    label: "Backends",
    measures: "by state and by application name",
    source: "R5.2",
  },
  {
    key: "sizes",
    label: "Database sizes",
    measures: "database sizes",
    source: "R5.2",
  },
  { key: "age", label: "TXID / MXID age", measures: "TXID and MXID age", source: "R5.2" },
  {
    key: "wal",
    label: "WAL",
    measures: "volume and generation rate",
    source: "R5.2",
  },
  {
    key: "checkpoints",
    label: "Checkpoints",
    measures: "checkpoints",
    source: "R5.2",
  },
];

/** R5.1's five summary cards, in the requirement's order and wording. */
export type SummaryCardKey = "cpu" | "connections" | "txid" | "disk" | "logErrors";

export interface SummaryCard {
  key: SummaryCardKey;
  /** R5.1's label, verbatim — including the `P99 | P50` form it prints, which is
   *  the requirement's own spelling and not a chart option. */
  label: string;
  /** The family the card's value is computed from, and so the group whose
   *  payload answers it. `null` for Log errors, which R5.1 sources from R5.10 —
   *  a log ingestion, not one of R5.2/R5.3's metric families — so no metric
   *  group's aggregate can answer it. */
  group: MetricGroupKey | null;
}

export const SUMMARY_CARDS: readonly SummaryCard[] = [
  { key: "cpu", label: "CPU utilization P99 | P50", group: "os" },
  { key: "connections", label: "Peak connections", group: "backends" },
  { key: "txid", label: "Transaction ID utilization", group: "age" },
  { key: "disk", label: "Disk utilization", group: "os" },
  { key: "logErrors", label: "Log errors", group: null },
];

/** The value a card prints.
 *
 *  Four of the five print `NULL_VALUE` while the payload is absent: R10.13's rule
 *  that a number the app does not have is printed as `—`, never as `0`.
 *
 *  The fifth is the one card the spec answers for a default deployment before any
 *  DTO exists. R5.1's [A] clause says the Log errors card "is sourced from R5.10
 *  and renders `not configured` when `--log-source=off`", and R5.10's flag
 *  defaults to `off` — so `not configured` is the requirement's stated answer
 *  today, and printing `—` there would contradict R5.1 while claiming to obey
 *  R10.13. */
export function summaryCardValue(
  card: SummaryCard,
  system: Envelope<SystemGroupView> | null,
): string {
  if (system === null) {
    return card.key === "logErrors" ? "not configured" : NULL_VALUE;
  }
  if (card.group === null) return NULL_VALUE;
  const row = system.data.find((v) => v.group === card.group);
  return formatNumber(row === undefined ? null : row.value);
}

/** R5.6's per-chart toolbar, in the requirement's order.
 *
 *  R5.6's sentence names four things and only three of them are buttons:
 *  "drag-to-zoom a time range, **Download PNG**, **Download CSV**, and 'View in
 *  Prometheus/Grafana' deep link built from the panel's PromQL. Cloud SQL offers
 *  exactly these three; we match them per chart, not per page." Drag-to-zoom is a
 *  gesture on the plot, so it has no button and the chart card states it in prose
 *  (`ZOOM_NOTE`); the three exports are buttons, and R10.10's parenthetical —
 *  "(zoom / PNG / CSV / external link)" — is the same four.
 *
 *  All three are `disabled` while the group has no series, and each one's `title`
 *  names the field that would enable it. A control that is inert without saying
 *  why is R10.12's failure mode in button form; a control that names its own
 *  missing input is the same argument the panel one up the page makes in prose. */
export const TOOLBAR: readonly { label: string; needs: string }[] = [
  {
    label: "Download PNG",
    needs: "a rendered plot — the export is an image of the chart",
  },
  {
    label: "Download CSV",
    needs: "the series rows — R5.6's CSV is the plotted data",
  },
  {
    label: "View in Prometheus/Grafana",
    needs: "the panel's PromQL, which `SystemGroupView.promql` will carry",
  },
];

/** R5.6's first control, which is a gesture and so cannot be a button. */
export const ZOOM_NOTE = "Drag-to-zoom is a gesture on the plot; there is no plot yet.";

/** R5.7's cap, from its sentence: "Compare up to 5 instances on one chart
 *  (Cloud SQL parity)". */
const MAX_COMPARE = 5;

/** The instances the R5.7 selector offers.
 *
 *  R9.3 makes instance identity the triple `(namespace, cluster, pod)`, and the
 *  cluster row carries all three: `namespace` and `name` are the first two
 *  (`web/src/api/types.ts:349-350`) and `instanceList[].name` is the third
 *  (`web/src/api/types.ts:187`). So this control is populated from data the app
 *  already has — the only R10.10 control that works today, and it works because
 *  R9.3's contract landed in Phase 1a, not because anything Phase 7 shipped.
 *
 *  The primary sorts first: R5.7's parity target compares instances of one
 *  cluster, and the instance a reader asks about first is the one accepting
 *  writes. The rest follow in payload order, and the list is capped at
 *  `MAX_COMPARE` because the requirement caps it, not because the UI does. */
export function compareOptions(cluster: FleetRow): string[] {
  const primary = cluster.currentPrimary;
  const names = cluster.instanceList.map((i) => i.name);
  const ordered =
    primary !== undefined && names.includes(primary)
      ? [primary, ...names.filter((n) => n !== primary)]
      : names;
  return ordered.slice(0, MAX_COMPARE);
}

/* ── page ───────────────────────────────────────────────────────────────────── */

export type SystemInsightsPageProps = PageProps<Envelope<SystemGroupView>>;

export function SystemInsightsPage(props: SystemInsightsPageProps): ReactElement {
  const { state } = props;

  // Every hook above every early return — the rule
  // `web/src/pages/WaitEvents.tsx:107-109` states after Active Queries was
  // bitten by it: a render that turns a malformed URL into a valid one renders
  // more hooks than the render before it and throws.
  const cluster = useMemo(
    () => FIXTURE_CLUSTERS.find((c) => `${c.namespace}/${c.name}` === state.cluster),
    [state.cluster],
  );
  const [groupKey, setGroupKey] = useState<MetricGroupKey>("os");
  const [picked, setPicked] = useState<string[]>([]);

  // The payload is the caller's fixture or nothing. There is no fetcher for
  // R8.2's route, so `null` is not a loading state — see the header. `phase`
  // follows the payload rather than a hook's poll because there is no poll to
  // report: with a fixture the page is `ready`, without one it is `idle`, and
  // `deriveSystemPanelState` is what turns that pair into R10.12's answer.
  const system: Envelope<SystemGroupView> | null = props.fixtureOverride ?? null;
  const phase = system === null ? "idle" : "ready";

  if (cluster === undefined) {
    return (
      <div className="page" data-page="system">
        <PageHeader state={state} system={system} />
        <NoCluster {...props} />
      </div>
    );
  }

  const panel = deriveSystemPanelState({ phase, system });
  // R10.22's composed body for `partial` comes from `copyFor` with the payload as
  // its source — the same call `web/src/pages/Tags.tsx:235` makes for its own
  // table — so the page never re-types a sentence `StatePanel` already owns.
  const bannerCopy = copyFor(
    panel,
    system === null ? undefined : { meta: system.meta, data: system.data },
    SYSTEM_COPY,
  );

  if (panel === "loading" || panel === "empty" || panel === "error") {
    return (
      <div className="page" data-page="system">
        <PageHeader state={state} system={system} />
        <StatePanel state={panel} copy={SYSTEM_COPY} />
      </div>
    );
  }

  // `unavailable` and `partial` render as a banner over the body and `ready`
  // renders the body alone: R10.12's rule that the two warning states are a
  // banner over real content rather than a replacement for it
  // (`web/src/components/StatePanel.tsx:606-607` says so for the pair). Here the
  // content is the inventory the requirement states, and the banner is the
  // sentence that says why none of it has numbers.
  const options = compareOptions(cluster);
  const selected = picked.filter((p) => options.includes(p));
  const active = selected.length > 0 ? selected : options.slice(0, MAX_COMPARE);
  const group = METRIC_GROUPS.find((g) => g.key === groupKey) ?? METRIC_GROUPS[0]!;

  return (
    <div className="page" data-page="system">
      <PageHeader state={state} system={system} />

      {bannerCopy !== null && (
        <div className="banner" role="status">
          <strong>{bannerCopy.title}</strong>
          <span>{bannerCopy.body}</span>
        </div>
      )}

      <SummaryCards system={system} />

      <MetricTabs groupKey={group.key} onSelect={setGroupKey} />

      <GroupChart group={group} system={system} />

      <CompareSelector
        options={options}
        active={active}
        onToggle={(pod) =>
          setPicked((prev) => {
            const current = prev.filter((p) => options.includes(p));
            const base = current.length > 0 ? current : options.slice(0, MAX_COMPARE);
            if (base.includes(pod)) return base.filter((p) => p !== pod);
            if (base.length >= MAX_COMPARE) return base;
            return [...base, pod];
          })
        }
      />
    </div>
  );
}

/* ── header ─────────────────────────────────────────────────────────────────── */

/** The header block, in the shape `web/src/pages/WaitEvents.tsx:204-232` gives
 *  it: crumb, title, then the R10.21 stamp with the absolute UTC on hover.
 *
 *  No refresh indicator. R10.8's widget prints a poll interval, and this page has
 *  none to print — R5.8's 1-minute refresh is Phase 7's rule for a route with no
 *  fetcher, and a header that advertises "refreshes every 60 s" for a page that
 *  will never ask again is a claim the page cannot keep. */
function PageHeader({
  state,
  system,
}: {
  state: PageProps["state"];
  system: Envelope<SystemGroupView> | null;
}): ReactElement {
  return (
    <header className="page-head">
      <div>
        <p className="breadcrumb">
          <a className="cluster-link" href="/">
            Fleet
          </a>
          <span aria-hidden> / </span>
          {state.cluster !== null && (
            <>
              <a className="cluster-link" href={buildPath("cluster", state.cluster, null)}>
                {state.cluster}
              </a>
              <span aria-hidden> / </span>
            </>
          )}
          <span>System Insights</span>
        </p>
        <h1 className="page-title">System Insights</h1>
        {/* R10.21's pair: the local rendering in the text, the absolute UTC in
            the hover title, both from the one `meta.generatedAt` field — the
            shape `web/src/pages/WaitEvents.tsx:223` gives it. With no payload
            there is no timestamp to stamp, so the title is absent rather than
            empty, and the text carries the sentence that says why. */}
        <p
          className="page-meta"
          {...(system === null ? {} : { title: `${system.meta.generatedAt} UTC` })}
        >
          {system === null
            ? "No system-metrics payload has been fetched for this cluster."
            : `Data as of ${formatTimestamp(system.meta.generatedAt)}`}
        </p>
      </div>
    </header>
  );
}

/** The URL names no cluster, or names one that is not a namespace/name pair.
 *
 *  Same shape as `web/src/pages/WaitEvents.tsx:235-240`'s `NoCluster`, with this
 *  route's sentence: R10.10's page is per-cluster (`web/src/routes.ts:287`'s
 *  `scope: "cluster"`), because R5.1's cards and R5.7's instances are both
 *  computed per cluster and neither has a fleet-wide form. */
function NoCluster({ state, onPatch }: SystemInsightsPageProps): ReactElement {
  return (
    <section className="card" data-panel="no-cluster">
      <div className="card-head">
        <h2 className="card-title">No cluster selected</h2>
        <span className="card-meta">R10.10</span>
      </div>
      <p className="card-note">
        <code>{state.cluster ?? ""}</code> is not in the current fleet snapshot. System
        Insights renders one cluster&apos;s metric groups, so there is no fleet-wide form
        of this page to fall back to.
      </p>
      <button
        type="button"
        className="btn"
        onClick={() => onPatch({ page: "fleet", cluster: null })}
      >
        Back to Fleet
      </button>
    </section>
  );
}

/* ── R5.1's summary cards ───────────────────────────────────────────────────── */

function SummaryCards({
  system,
}: {
  system: Envelope<SystemGroupView> | null;
}): ReactElement {
  return (
    <section className="card" data-panel="system-summary" aria-labelledby="si-summary-title">
      <div className="card-head">
        <h2 className="card-title" id="si-summary-title">
          Summary
        </h2>
        <span className="card-meta">R5.1</span>
      </div>
      <div className="stat-row">
        {SUMMARY_CARDS.map((card) => (
          <div
            className="stat-tile"
            key={card.key}
            title={
              card.group === null
                ? `${card.label} — sourced from R5.10 (log ingestion), not from a metric family.`
                : `${card.label} — computed from the ${card.group} family.`
            }
          >
            <div className="stat-label">{card.label}</div>
            <div className="stat-value">{summaryCardValue(card, system)}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

/* ── R10.10's metric-group tabs ─────────────────────────────────────────────── */

/** The tab strip. `.segmented` (`web/src/styles/base.css:310`) is the shell's
 *  one mutually-exclusive button group — the control `PlanViewer.tsx:185`,
 *  `VirtualList.tsx:62` and `ThemeToggle.tsx:96` all reuse rather than
 *  restyle — and R10.10's tabs are exactly that shape: one group selected at a
 *  time, every group present whether or not its chart exists. */
function MetricTabs({
  groupKey,
  onSelect,
}: {
  groupKey: MetricGroupKey;
  onSelect: (key: MetricGroupKey) => void;
}): ReactElement {
  return (
    <section className="card" data-panel="system-tabs" aria-labelledby="si-tabs-title">
      <div className="card-head">
        <h2 className="card-title" id="si-tabs-title">
          Metric groups
        </h2>
        <span className="card-meta num">{METRIC_GROUPS.length}</span>
      </div>
      <div className="segmented" role="group" aria-label="Metric groups">
        {METRIC_GROUPS.map((g) => (
          <button
            key={g.key}
            type="button"
            aria-pressed={g.key === groupKey}
            onClick={() => onSelect(g.key)}
            title={`${g.measures} · ${g.source}`}
          >
            {g.label}
          </button>
        ))}
      </div>
    </section>
  );
}

/* ── the selected group's chart slot, with its R5.6 toolbar ─────────────────── */

function GroupChart({
  group,
  system,
}: {
  group: MetricGroup;
  system: Envelope<SystemGroupView> | null;
}): ReactElement {
  const row = system === null ? undefined : system.data.find((v) => v.group === group.key);
  const hasSeries = row !== undefined;

  return (
    <section
      className="card"
      data-panel="system-chart"
      data-group={group.key}
      data-state={hasSeries ? "ready" : "unavailable"}
      aria-labelledby="si-chart-title"
    >
      <div className="card-head">
        <h2 className="card-title" id="si-chart-title">
          {group.label}
        </h2>
        <div className="card-actions">
          {TOOLBAR.map((control) => (
            <button
              key={control.label}
              type="button"
              className="btn btn-xs"
              disabled={!hasSeries}
              title={hasSeries ? control.label : `${control.label}: disabled, needs ${control.needs}.`}
            >
              {control.label}
            </button>
          ))}
        </div>
      </div>

      <p className="card-note">
        {group.measures} · {group.source}
      </p>

      <div className="chart-frame">
        <p className="chart-empty">
          {hasSeries
            ? `The ${group.label.toLowerCase()} chart is Phase 7's to draw; the slot is named here.`
            : `No ${group.label.toLowerCase()} series has been fetched — web/src/api/client.ts has no fetcher for ${group.source}'s family yet, so the frame carries a sentence instead of a plot (R10.13).`}
        </p>
      </div>

      <p className="card-note">{ZOOM_NOTE}</p>
    </section>
  );
}

/* ── R5.7's compare-up-to-5 selector ───────────────────────────────────────── */

function CompareSelector({
  options,
  active,
  onToggle,
}: {
  options: readonly string[];
  active: readonly string[];
  onToggle: (pod: string) => void;
}): ReactElement {
  return (
    <section
      className="card"
      data-panel="system-compare"
      aria-labelledby="si-compare-title"
    >
      <div className="card-head">
        <h2 className="card-title" id="si-compare-title">
          Compare instances
        </h2>
        <span className="card-meta num">
          {active.length} of {MAX_COMPARE}
        </span>
      </div>

      {options.length === 0 ? (
        <p className="chart-empty">
          This cluster reports no instances, so R5.7&apos;s comparison has nothing to
          select.
        </p>
      ) : (
        <div className="segmented" role="group" aria-label="Instances to compare">
          {options.map((pod) => {
            const on = active.includes(pod);
            return (
              <button
                key={pod}
                type="button"
                aria-pressed={on}
                // R5.7's cap is the requirement's, so at five selected the sixth
                // instance is not selectable — and `disabled` says so rather than
                // swallowing the click.
                disabled={!on && active.length >= MAX_COMPARE}
                onClick={() => onToggle(pod)}
              >
                {pod}
              </button>
            );
          })}
        </div>
      )}

      <p className="card-note">
        R5.7 compares up to {MAX_COMPARE} instances on one chart, and R9.3&apos;s
        identity triple (namespace, cluster, pod) is what makes an instance nameable
        across scrapes. The selection is page state: R10.16&apos;s URL grammar has no
        slot for it.
      </p>
    </section>
  );
}
