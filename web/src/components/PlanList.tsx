/* R10.7's "plan samples list", plus R3.11's change log, plus the selection that
 * drives `PlanViewer`.
 *
 * ── why the list owns the viewer ──────────────────────────────────────────
 *
 * R10.7 names them as one phrase — "plan samples list with a JSON/plan viewer" —
 * and the reason they ship as one component is mechanical: the selection state
 * has to live with the list that sets it. If the page held it, every caller of
 * `PlanList` would have to remember to hold it, and a caller that forgot got a
 * list where clicking a row did nothing — a silent failure, which is the class
 * this project treats as a bug rather than a TODO. So `PlanList` renders the
 * list card *and* the viewer card as a fragment, and the page composes one
 * element instead of wiring two.
 *
 * ── what the change log adds that the samples do not ──────────────────────
 *
 * `PlanChangeView` is R3.11's event: two hashes and a timestamp. Read against
 * the sample list it answers a question the samples alone cannot — the fixture
 * has a change whose `currentPlanHash` (`33adc172…`) *is* sample 1's hash, and
 * whose `previousPlanHash` (`9f2c4e18…`) matches **no sample on the page**.
 * That asymmetry is the data's, not a bug: the store keeps the current plan and
 * the hash of the one it replaced, not the replaced plan itself. Rendering the
 * log as a flat table of two hashes hides it; rendering the current hash as a
 * link into the sample that has it, and the previous as a bare hash, states it —
 * and it makes the log navigable, which is R10.19's click-through for this
 * surface (a change with no way to see the plan it names is a notification).
 *
 * ── R10.19 / R10.23 for this surface ──────────────────────────────────────
 *   - the list is a table, so it needs no chart twin; the viewer's two modes are
 *     the twin of the object it renders;
 *   - hit targets are buttons in the row's first cell, the same shape as
 *     `QueryTable`'s `query-cell`/`query-link` — a `<tr onClick>` is not
 *     keyboard reachable, and the house table pattern already knows that;
 *   - the empty cases are sentences, not blank panels: no samples at all (R3.10's
 *     `skip truncated rows` and the privilege state both produce this) and no
 *     plan changes (R3.11's ordinary case — most queries never change plan).
 */

import { useState } from "react";
import type { ReactElement } from "react";

import type { PlanChangeView, PlanSampleView } from "../api/types";
import { formatTimestamp, utcTitle } from "../lib/format";
import { PlanViewer } from "./PlanViewer";

/** Which side of a plan change the page can actually show, and which row it is.
 *
 *  `current` and `previous` name the two hashes; `neither` is the honest answer
 *  when the store has moved on from both. Exported because the fixture proves
 *  all three states are reachable (sample 1 is `current` for the logged change,
 *  sample 2 has no hash at all, and a change from an older generation matches
 *  nothing) and a UI that assumes `current` always resolves renders a dead link.
 *
 *  `index` is the row to select, `-1` when the change resolves to nothing — a
 *  number rather than the sample itself so the caller never has to recover an
 *  index from an object (`indexOf` on a structurally-cloned row is a footgun and
 *  the signature would need a cast to prove the row is still in the list). */
export function resolveChange(
  change: PlanChangeView,
  plans: readonly PlanSampleView[],
): { side: "current" | "previous" | "neither"; index: number } {
  const current = plans.findIndex((p) => p.planHash === change.currentPlanHash);
  if (current >= 0) return { side: "current", index: current };
  const previous = plans.findIndex((p) => p.planHash === change.previousPlanHash);
  if (previous >= 0) return { side: "previous", index: previous };
  return { side: "neither", index: -1 };
}

export interface PlanListProps {
  /** R3.10's captured samples for this fingerprint. `[]`, never null, when there
   *  are none. */
  plans: readonly PlanSampleView[];
  /** R3.11's change events, newest first as the DTO emits them. */
  planChanges: readonly PlanChangeView[];
}

/** R10.7's plan samples list and its viewer, as one fragment. */
export function PlanList({ plans, planChanges }: PlanListProps): ReactElement {
  // Default selection: the newest sample. A page that opens with nothing
  // selected shows an empty viewer next to a populated list, which reads as a
  // broken page; the newest sample is the one the header's current plan is.
  const [selected, setSelected] = useState<number>(plans.length > 0 ? 0 : -1);
  const sample = selected >= 0 ? plans[selected] : undefined;

  return (
    <>
      <section className="card" data-panel="plans">
        <div className="card-head">
          <h2 className="card-title">Plan samples</h2>
          <div className="card-actions">
            <span className="card-meta num">{plans.length} captured</span>
          </div>
        </div>

        {plans.length === 0 ? (
          // R10.13's sentence for the two ways this happens: every statement was
          // truncated (R3.10(a) skips them) or every capture hit the privilege
          // state (R3.10(c)). The page cannot tell which from here — the detail
          // view has no field for "why" — so the sentence names both.
          <p className="chart-empty" data-testid="plans-empty">
            No plan samples for this fingerprint — R3.10 skips truncated statements, and a statement
            the role cannot read yields an unavailable capture rather than a sample.
          </p>
        ) : (
          <div className="table-wrap">
            <table className="data plan-table">
              <caption className="sr-only">Captured plans for this query</caption>
              <thead>
                <tr>
                  <th scope="col">Captured</th>
                  <th scope="col">Method</th>
                  <th scope="col">Availability</th>
                  <th scope="col" className="mono">
                    Plan hash
                  </th>
                </tr>
              </thead>
              <tbody>
                {plans.map((p, i) => (
                  <tr key={`${p.capturedAt}-${i}`} className={selected === i ? "selected" : undefined}>
                    <th scope="row" className="query-cell">
                      {/* The row's action is a button in its first cell, matching
                          QueryTable's query-cell/query-link: a row-wide onClick
                          looks like a hit target and isn't a keyboard one. */}
                      <button type="button" className="query-link" onClick={() => setSelected(i)}>
                        <span title={utcTitle(p.capturedAt)}>{formatTimestamp(p.capturedAt)}</span>
                      </button>
                    </th>
                    <td>{p.method}</td>
                    <td>{p.availability}</td>
                    <td className="mono num">{p.planHash ?? "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* R3.11's change log. It lives on the same card as the samples because
            its two columns are hashes *of* those samples — separating them would
            force the reader to hold one page while reading another. */}
        {planChanges.length > 0 && (
          <div className="table-wrap plan-changes">
            <table className="data change-table">
              <caption className="caption">Plan changes</caption>
              <thead>
                <tr>
                  <th scope="col">Detected</th>
                  <th scope="col" className="mono">
                    Previous
                  </th>
                  <th scope="col" className="mono">
                    Current
                  </th>
                </tr>
              </thead>
              <tbody>
                {planChanges.map((c, i) => {
                  const resolved = resolveChange(c, plans);
                  return (
                    <tr key={`${c.detectedAt}-${i}`}>
                      <th scope="row" title={utcTitle(c.detectedAt)}>
                        {formatTimestamp(c.detectedAt)}
                      </th>
                      {/* A hash with no sample on this page is not a dead link, so
                          it is not a link: the store keeps the current plan and the
                          replaced plan's hash, not the replaced plan. */}
                      <td className="mono">{c.previousPlanHash}</td>
                      <td className="mono" data-side={resolved.side}>
                        {resolved.index >= 0 ? (
                          <button type="button" className="query-link" onClick={() => setSelected(resolved.index)}>
                            {c.currentPlanHash}
                          </button>
                        ) : (
                          c.currentPlanHash
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <PlanViewer sample={sample} />
    </>
  );
}
