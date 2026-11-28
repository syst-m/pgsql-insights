/* R10.27's `pg_stat_statements` not-installed screen.
 *
 * R10.27's own sentence is the design brief: "first-class screen, designed and
 * tested — not an empty table with a tooltip", and it says why this empty state
 * is not like the others — "Given K2 (absent on all three reachable reference
 * clusters), this is **the product's most-seen screen in v1** and gets the same
 * design effort as the Queries page." §9's 6b acceptance names it a second time
 * (:673): "the R10.27 not-installed screen renders for every cluster lacking
 * the extension".
 *
 * So this file exists instead of `QUERY_COPY.unavailable`'s three strings being
 * the whole answer. `StatePanel` renders a title, a body and a button, which is
 * the right shape for `loading` and `error`; for *this* state it is the
 * tooltip-shaped answer the requirement rules out, because what a viewer needs
 * here is not a sentence — it is R3.15's flow and the cost of it. The copy table
 * keeps its `unavailable` entry (the `Record` type requires all six keys, and
 * `copyFor` is still how the other states get their wording); this component is
 * the screen that entry was a stub for.
 *
 * Everything rendered is from the spec, because an empty screen is the easiest
 * place in the product to invent something:
 *
 *  - the four steps and their order are R3.15's (`requirements.md:322`):
 *    **detect** (R3.1) → **offer** (UI action + `pgsi enable
 *    pg-stat-statements --cluster X`, gated behind R12.8's `--allow-actions`) →
 *    **patch** `spec.parameters.shared_preload_libraries` (documented diff,
 *    reversible, never auto-applied to more than one cluster per invocation) →
 *    **verify** (poll until the extension is queryable, report the
 *    rolling-restart progress per instance);
 *  - the restart cost is on the screen because R3.15 requires it in terms: "The
 *    restart cost is stated in the offer, not discovered afterwards";
 *  - why `CREATE EXTENSION` alone is not enough is R3.15's first sentence
 *    (`CREATE EXTENSION` alone fails on a stock CNPG cluster — `required
 *    extension "pg_stat_statements" is not loaded` — because
 *    `shared_preload_libraries` is postmaster-level and empty by default on all
 *    three reachable clusters);
 *  - the "expected, not an incident" framing is K2's (`19eb117:requirements.md:812`) and
 *    R10.12's own distinction between "no source configured" and "the collector
 *    ran and failed" — the former is expected on every reachable cluster, the
 *    latter is an incident.
 *
 * It draws no chart, so it has no palette to validate: no series, no axis, no
 * mark. And it deliberately wears no status colour — R10.23 reserves the status
 * palette for status, and colouring this screen would say *something is wrong*
 * about a state the spec calls the normal case on a stock install. Ink only.
 */

import { NULL_VALUE } from "../lib/format";

/** R3.15's four steps, in R3.15's order, with the one this screen is standing
 *  for marked. `state` is a string and not a colour for the reason in the header
 *  comment: the marker has to read the same in both themes and to a reader who
 *  cannot see the hue, which is R10.23's "the colour never carries meaning
 *  alone" rule applied to a non-chart surface. */
const STEPS: readonly { key: string; label: string; state: string; detail: string }[] = [
  {
    key: "detect",
    label: "Detect",
    state: "done",
    // R3.1's two sources, named because the screen is the detection's output and
    // a viewer is entitled to know which read produced it.
    detail: "pg_extension via S2, cnpg_pg_extensions_* via S3 (R3.1). This screen is that answer.",
  },
  {
    key: "offer",
    label: "Offer",
    state: "here",
    detail: "The button below, and the CLI's `pgsi enable pg-stat-statements --cluster X` (R3.15).",
  },
  {
    key: "patch",
    label: "Patch",
    state: "pending",
    detail:
      "spec.parameters.shared_preload_libraries — documented diff, reversible, never auto-applied to more than one cluster per invocation (R3.15).",
  },
  {
    key: "verify",
    label: "Verify",
    state: "pending",
    detail: "Poll until the extension is queryable, reporting the rolling-restart progress per instance (R3.15).",
  },
];

export interface ExtensionNotInstalledProps {
  /** The cluster this screen is about, as the `namespace/name` the URL carries.
   *
   *  Typed `string | null` because that is what `UrlState.cluster` is, and the
   *  page passes it straight through: the null-ness belongs to the URL (the fleet
   *  route has no cluster), and laundering it to `""` at the call site would put
   *  the page's opinion about *why* it is absent inside the component that only
   *  renders it. The screen names the thing the route addressed rather than
   *  rendering a floating notice — R3.1's detection is per cluster, so the answer
   *  belongs to one — and when the route named nothing it says so with the shared
   *  null value. Rendered as a child, never as markup: R10.23's "labels are
   *  untrusted data" rule, and this string came from the URL bar. */
  cluster: string | null;
  /** R3.15's offer, wired to whatever the host page can do about it.
   *
   *  Optional because the action is gated behind R12.8's `--allow-actions`,
   *  which is off by default so "a read-only product cannot become a mutation
   *  surface by surprise". A read-only deployment gets this screen with no
   *  button, and that is a state the spec names rather than a bug — which is why
   *  the CLI form is shown as text next to the button instead of only inside the
   *  click target: with no button, the CLI line is the whole offer. */
  onEnable?: () => void;
}

/** The screen itself (R10.27).
 *
 *  `pages/Queries.tsx` renders this for `panel === "unavailable"` instead of the
 *  generic `StatePanel`, and keeps the `state-panel` class and the
 *  `data-state="unavailable"` attribute so R16.7's visual suite still keys the
 *  screen by state name — the requirement made the screen first-class, it did
 *  not remove it from R10.12's named set.
 */
export function ExtensionNotInstalled({ cluster, onEnable }: ExtensionNotInstalledProps) {
  // Both spellings of "the route named nothing" — `null` from `UrlState`, `""`
  // from a URL that has an empty `?c=` — render the shared null value rather than
  // an empty span next to the word "Cluster".
  const clusterLabel = cluster === null || cluster === "" ? NULL_VALUE : cluster;
  return (
    <section
      className="state-panel ext-screen"
      data-state="unavailable"
      role="status"
      aria-live="polite"
      aria-labelledby="ext-not-installed-title"
    >
      <header className="ext-head">
        <h2 className="state-title" id="ext-not-installed-title">
          pg_stat_statements is not installed
        </h2>
        <p className="ext-subtitle">
          <span className="secondary">Cluster</span> <span className="mono">{clusterLabel}</span>
        </p>
      </header>

      <p className="ext-body">
        There are no query rows to show, because the extension that produces them is not loaded in this
        cluster. This is the normal state of a stock CloudNativePG install, not a failure: the collector
        is not broken and discovery and health are unaffected.
      </p>

      <section className="ext-section" aria-labelledby="ext-flow-title">
        <h3 className="ext-section-title" id="ext-flow-title">
          What enabling it takes
        </h3>
        <p className="ext-note">
          `CREATE EXTENSION` alone fails on a stock cluster — `required extension "pg_stat_statements" is
          not loaded` — because `shared_preload_libraries` is postmaster-level and empty by default. So
          it is a flow, not a command (R3.15), and this screen is the second step of it.
        </p>
        <ol className="ext-flow">
          {STEPS.map((step) => (
            <li className="ext-step" key={step.key} data-step={step.key} data-step-state={step.state}>
              <span className="ext-step-label">{step.label}</span>
              <span className="ext-step-state">{step.state}</span>
              <span className="ext-step-detail">{step.detail}</span>
            </li>
          ))}
        </ol>
      </section>

      <section className="ext-section" aria-labelledby="ext-cost-title">
        <h3 className="ext-section-title" id="ext-cost-title">
          The cost, stated here
        </h3>
        {/* dt/dd as direct grid children, the way `.detail-header`
            (`web/src/styles/base.css:885`) does it — not a wrapper div per row. The
            house note at `web/src/styles/base.css:883` says these three grids should become one
            `.kv-grid`; wrapping each row in a div would add a fourth spelling
            instead of waiting for that consolidation. */}
        <dl className="ext-cost">
          <dt>Restart</dt>
          <dd>
            Rolling restart of every instance in the cluster. `shared_preload_libraries` is
            postmaster-level, so it cannot be changed without one; the flow is scheduled ahead of the
            collector for that reason (R3.15, Phase 2).
          </dd>
          <dt>Reversible</dt>
          <dd>Yes — the patch is a documented diff and can be removed (R3.15).</dd>
          <dt>Blast radius</dt>
          <dd>
            One cluster per invocation, never applied to more than one at a time (R3.15), and gated
            behind `--allow-actions` (R12.8).
          </dd>
          <dt>Unlocks</dt>
          <dd>
            The Queries page and Query Detail for this cluster: the R3.2 column set, the R3.9 latency
            histogram, R3.10's plan samples.
          </dd>
        </dl>
      </section>

      <div className="ext-actions">
        {onEnable !== undefined && (
          <button type="button" className="btn" onClick={onEnable}>
            Enable pg_stat_statements
          </button>
        )}
        <p className="ext-cli secondary">
          <span className="mono">pgsi enable pg-stat-statements --cluster {clusterLabel}</span>
          <span className="ext-cli-note"> requires --allow-actions (R12.8)</span>
        </p>
      </div>
    </section>
  );
}
