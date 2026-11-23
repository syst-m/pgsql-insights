/* R10.7's plan samples list, R3.11's change log, and the selection that joins
 * them to the viewer.
 *
 * `resolveChange` is tested as a pure function because the fixture proves all
 * three of its answers are reachable — the logged change's current hash *is*
 * sample 0, its previous hash matches nothing on the page — and a UI that
 * assumes `current` always resolves renders a dead link. The render tests then
 * cover the wiring only the DOM can show: which row is marked selected, and
 * which sample the viewer is therefore showing.
 */

import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { FIXTURE_QUERY_DETAIL } from "../api/fixtures";
import type { PlanChangeView, PlanSampleView } from "../api/types";
import { formatTimestamp } from "../lib/format";
import { PlanList, resolveChange } from "./PlanList";

const PLANS = FIXTURE_QUERY_DETAIL.plans;
const CHANGES = FIXTURE_QUERY_DETAIL.planChanges;
const FIRST = PLANS[0] as PlanSampleView;
const SECOND = PLANS[1] as PlanSampleView;
const CHANGE = CHANGES[0] as PlanChangeView;

describe("resolveChange", () => {
  it("resolves a change to the sample that holds its current hash", () => {
    // The fixture's real case: the change's `currentPlanHash` is sample 0's hash.
    expect(resolveChange(CHANGE, PLANS)).toEqual({ side: "current", index: 0 });
  });

  it("falls back to the previous hash when no sample has the current one", () => {
    // A store that has since re-captured: the current hash moved on, but the
    // replaced plan is still on the page as a sample.
    const change: PlanChangeView = {
      detectedAt: "2026-11-06T09:11:55Z",
      previousPlanHash: FIRST.planHash as string,
      currentPlanHash: "f".repeat(64),
    };
    expect(resolveChange(change, PLANS)).toEqual({ side: "previous", index: 0 });
  });

  it("answers `neither` with index -1 when no sample matches either hash", () => {
    // The honest third answer. A UI that returns index 0 here would select an
    // unrelated sample and the viewer would contradict the log beside it.
    const change: PlanChangeView = {
      detectedAt: "2026-11-06T09:11:55Z",
      previousPlanHash: "a".repeat(64),
      currentPlanHash: "b".repeat(64),
    };
    expect(resolveChange(change, PLANS)).toEqual({ side: "neither", index: -1 });
  });

  it("answers `neither` when there are no samples at all", () => {
    const change: PlanChangeView = {
      detectedAt: "2026-11-06T09:11:55Z",
      previousPlanHash: "a".repeat(64),
      currentPlanHash: "b".repeat(64),
    };
    expect(resolveChange(change, []).index).toBe(-1);
  });
});

describe("PlanList", () => {
  it("lists every captured sample with its method, availability and hash", () => {
    const { container } = render(<PlanList plans={PLANS} planChanges={CHANGES} />);
    const rows = container.querySelectorAll(".plan-table tbody tr");
    expect(rows).toHaveLength(PLANS.length);
    expect(container.querySelector(".card-actions .card-meta")?.textContent).toBe(
      `${PLANS.length} captured`,
    );
    expect(rows[1]?.querySelector(".mono")?.textContent).toBe(SECOND.planHash ?? "—");
  });

  it("makes each row's action a button, not a row-wide click", () => {
    // R10.19's hit target, and the house table pattern from `QueryTable`: a
    // `<tr onClick>` looks like a hit target and is not a keyboard one.
    const { container } = render(<PlanList plans={PLANS} planChanges={CHANGES} />);
    const buttons = container.querySelectorAll(".plan-table tbody .query-cell button.query-link");
    expect(buttons).toHaveLength(PLANS.length);
    expect(buttons[0]?.textContent).toBe(formatTimestamp(FIRST.capturedAt));
  });

  it("selects the newest sample so the viewer is never empty beside a full list", () => {
    const { container } = render(<PlanList plans={PLANS} planChanges={CHANGES} />);
    expect(container.querySelector(".plan-table tbody tr")?.className).toContain("selected");
    expect(container.querySelector("[data-panel='plan-viewer'] .plan-meta .plan-sql")?.textContent).toBe(
      FIRST.sql,
    );
  });

  it("switches the viewer when a different row is clicked", () => {
    // The wiring R10.7's single phrase "list with a viewer" is: the list owns the
    // selection, so a caller cannot get a list whose rows do nothing.
    const { container } = render(<PlanList plans={PLANS} planChanges={CHANGES} />);
    const second = container.querySelectorAll(".plan-table tbody .query-cell button")[1] as HTMLElement;
    fireEvent.click(second);
    expect(container.querySelectorAll(".plan-table tbody tr")[1]?.className).toContain("selected");
    expect(container.querySelector("[data-panel='plan-viewer'] .plan-meta")?.textContent).toContain(
      SECOND.method,
    );
  });

  it("links a change's current hash to the sample that has it", () => {
    const { container } = render(<PlanList plans={PLANS} planChanges={CHANGES} />);
    const cell = container.querySelector(".change-table td[data-side='current']");
    expect(cell?.textContent).toBe(CHANGE.currentPlanHash);
    expect(cell?.querySelector("button.query-link")).not.toBeNull();
  });

  it("leaves a change's previous hash as text when no sample has it", () => {
    // The store keeps the current plan and the replaced plan's *hash*, not the
    // replaced plan. Linking to nothing would be a dead link; the asymmetry is
    // the data's and the render says so.
    const { container } = render(<PlanList plans={PLANS} planChanges={CHANGES} />);
    const previous = container.querySelector(".change-table tbody tr td");
    expect(previous?.textContent).toBe(CHANGE.previousPlanHash);
    expect(previous?.querySelector("button")).toBeNull();
  });

  it("selecting a change's current hash selects that sample", () => {
    // The change log is navigable, not just a notification: clicking the hash
    // moves the viewer, which is R10.19's click-through for this surface.
    const { container } = render(<PlanList plans={PLANS} planChanges={CHANGES} />);
    const second = container.querySelectorAll(".plan-table tbody .query-cell button")[1] as HTMLElement;
    fireEvent.click(second);
    const link = container.querySelector(".change-table td[data-side='current'] button") as HTMLElement;
    fireEvent.click(link);
    expect(container.querySelectorAll(".plan-table tbody tr")[0]?.className).toContain("selected");
  });

  it("omits the change table when there are no changes", () => {
    // R3.11's ordinary case — most queries never change plan — is the absence of
    // a table, not a table with a row saying "none".
    const { container } = render(<PlanList plans={PLANS} planChanges={[]} />);
    expect(container.querySelector(".change-table")).toBeNull();
    expect(container.querySelector(".plan-table")).not.toBeNull();
  });

  it("explains the empty list instead of drawing an empty table", () => {
    // R3.10's two ways this happens (every statement truncated, or every capture
    // hit the privilege state) are both named, because the payload has no field
    // that says which one it was.
    const { container } = render(<PlanList plans={[]} planChanges={[]} />);
    expect(container.querySelector("[data-testid='plans-empty']")).not.toBeNull();
    expect(container.querySelector(".plan-table")).toBeNull();
    expect(container.querySelector("[data-panel='plan-viewer'] .chart-empty")?.textContent).toBe(
      "Select a plan sample to view it.",
    );
  });

  it("names the change table so the two tables on one card are distinguishable", () => {
    const { container } = render(<PlanList plans={PLANS} planChanges={CHANGES} />);
    expect(container.querySelector(".change-table caption")?.textContent).toBe("Plan changes");
  });
});
