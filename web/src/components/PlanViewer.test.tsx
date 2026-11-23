/* R10.7's JSON/plan viewer — the two views, the three sample shapes, and the
 * EXPLAIN envelope the tree has to unwrap.
 *
 * The suite is organised by the shapes `PlanSampleView` can take, because the
 * component's whole job is degrading correctly across them: an available sample
 * with a plan (tree + JSON), an unavailable one (a state, no views), and a plan
 * object the tree cannot read (JSON alone). The fixture supplies the first two
 * and the third is built inline, since the DTO's `plan` is
 * `Record<string, unknown>` and any shape the server emits has to render.
 */

import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { FIXTURE_QUERY_DETAIL } from "../api/fixtures";
import type { PlanSampleView } from "../api/types";
import { utcTitle } from "../lib/format";
import { PLAN_UNAVAILABLE, PlanViewer } from "./PlanViewer";

/** The fixture's available sample: an `EXPLAIN (FORMAT JSON)` body wrapped in its
 *  `Plan` key, which is the shape that broke the tree the first time it was
 *  rendered against real data. */
const AVAILABLE = FIXTURE_QUERY_DETAIL.plans[0] as PlanSampleView;
/** The fixture's unavailable sample: no `plan`, a `message`, R10.28's exact string. */
const UNAVAILABLE = FIXTURE_QUERY_DETAIL.plans[1] as PlanSampleView;

/** A three-node tree, to test the recursion and the indentation together. */
const NESTED: PlanSampleView = {
  capturedAt: "2026-11-20T09:11:58Z",
  method: "explain",
  availability: "available",
  plan: {
    Plan: {
      "Node Type": "Nested Loop",
      "Total Cost": 20.5,
      "Plan Rows": 40,
      Plans: [
        { "Node Type": "Index Scan", "Index Name": "orders_pkey", "Relation Name": "orders", "Total Cost": 0.42, "Plan Rows": 1 },
        { "Node Type": "Seq Scan", "Relation Name": "order_items", "Total Cost": 14.6, "Plan Rows": 39 },
      ],
    },
  },
  planHash: "a".repeat(64),
};

/** A `plan` object with no node in it — a future major, a hand-written fixture. */
const UNREADABLE: PlanSampleView = {
  capturedAt: "2026-11-20T09:11:58Z",
  method: "explain",
  availability: "available",
  plan: { "Query Text": "SELECT 1", "Planning Time": 0.081 },
};

describe("PlanViewer with nothing selected", () => {
  it("says what to do instead of drawing an empty pane", () => {
    const { container } = render(<PlanViewer />);
    expect(container.querySelector("[data-panel='plan-viewer']")).not.toBeNull();
    expect(container.querySelector(".chart-empty")?.textContent).toBe("Select a plan sample to view it.");
    expect(container.querySelector(".segmented")).toBeNull();
  });
});

describe("PlanViewer with an available sample", () => {
  it("renders the tree from inside the EXPLAIN envelope", () => {
    // `EXPLAIN … (FORMAT JSON)` wraps the root node in a `Plan` key. A viewer
    // that reads the envelope as the node finds no `Node Type` at the top and
    // shows no tree for every real plan — which is what this assertion pins.
    const { container } = render(<PlanViewer sample={AVAILABLE} />);
    const tree = container.querySelector("[data-testid='plan-tree']");
    expect(tree).not.toBeNull();
    expect(tree?.querySelector(".plan-node-type")?.textContent).toBe("Index Scan");
  });

  it("names the index the scan uses, not just the node type", () => {
    // `Index Scan on users_pkey` is the answer the viewer exists to surface; the
    // index name is in the payload and omitting it is the difference between a
    // plan and a guess.
    const { container } = render(<PlanViewer sample={AVAILABLE} />);
    expect(container.querySelector(".plan-node-target")?.textContent).toBe("users_pkey");
  });

  it("prints the cost and row estimate the formatter's way", () => {
    const { container } = render(<PlanViewer sample={AVAILABLE} />);
    expect(container.querySelector(".plan-node-metrics")?.textContent).toBe("cost 8.44 · 1 rows");
  });

  it("renders the JSON view on demand and the tree by default", () => {
    const { container } = render(<PlanViewer sample={AVAILABLE} />);
    expect(container.querySelector("[data-testid='plan-json']")).toBeNull();
    const jsonButton = container.querySelectorAll(".segmented button")[1] as HTMLElement;
    fireEvent.click(jsonButton);
    const pre = container.querySelector("[data-testid='plan-json']");
    expect(pre?.textContent).toBe(JSON.stringify(AVAILABLE.plan, null, 2));
    // The two views are the requirement's two views: switching back must restore
    // the tree, not leave both rendered.
    fireEvent.click(container.querySelectorAll(".segmented button")[0] as HTMLElement);
    expect(container.querySelector("[data-testid='plan-tree']")).not.toBeNull();
    expect(container.querySelector("[data-testid='plan-json']")).toBeNull();
  });

  it("labels the sample: captured, method, availability, hash, SQL", () => {
    // The method is not decoration — R3.10(b) makes `prepare_execute` and
    // `explain` different answers to the same SQL, so two samples of one query
    // look like a contradiction without it.
    const { container } = render(<PlanViewer sample={AVAILABLE} />);
    const meta = container.querySelector(".plan-meta");
    expect(meta?.querySelector(".plan-hash")?.textContent).toBe(AVAILABLE.planHash);
    expect(meta?.querySelector(".plan-hash")?.getAttribute("title")).toBe(AVAILABLE.planHash);
    expect(meta?.querySelector(".plan-sql")?.textContent).toBe(AVAILABLE.sql);
    expect(meta?.querySelector("[data-testid='plan-availability']")?.textContent).toBe("available");
    expect(meta?.querySelector("dd")?.getAttribute("title")).toBe(utcTitle(AVAILABLE.capturedAt));
  });

  it("draws every node of a nested tree, indented by depth", () => {
    const { container } = render(<PlanViewer sample={NESTED} />);
    const nodes = container.querySelectorAll(".plan-node");
    expect(nodes).toHaveLength(3);
    const [root, left, right] = Array.from(nodes).map((n) => n.getAttribute("style") ?? "");
    expect(root).toContain("padding-left: 0px");
    // Both children sit one level in, and the two of them are distinguishable
    // from the parent only by that indent — which is the tree's whole syntax.
    expect(left).toContain("padding-left: 14px");
    expect(right).toContain("padding-left: 14px");
  });

  it("renders the node types in tree order", () => {
    const { container } = render(<PlanViewer sample={NESTED} />);
    expect(
      Array.from(container.querySelectorAll(".plan-node-type")).map((n) => n.textContent),
    ).toEqual(["Nested Loop", "Index Scan", "Seq Scan"]);
  });

  it("offers JSON alone when the plan object has no readable node", () => {
    // The degrade path: content the tree cannot structure still has an evidence
    // view, and the segmented control disappears because there is one view left.
    const { container } = render(<PlanViewer sample={UNREADABLE} />);
    expect(container.querySelector("[data-testid='plan-tree']")).toBeNull();
    expect(container.querySelector(".segmented")).toBeNull();
    expect(container.querySelector("[data-testid='plan-json']")?.textContent).toBe(
      JSON.stringify(UNREADABLE.plan, null, 2),
    );
  });
});

describe("PlanViewer with an unavailable sample", () => {
  it("renders R10.28's state string verbatim", () => {
    // The exact-string contract: R10.28 names the state and `types.ts` mirrors
    // it, so the constant and the fixture must agree character for character.
    expect(PLAN_UNAVAILABLE).toBe(UNAVAILABLE.availability);
    const { container } = render(<PlanViewer sample={UNAVAILABLE} />);
    expect(container.querySelector("[data-testid='plan-availability']")?.textContent).toBe(
      PLAN_UNAVAILABLE,
    );
  });

  it("shows the message as a state detail, not an error banner", () => {
    const { container } = render(<PlanViewer sample={UNAVAILABLE} />);
    expect(container.querySelector("[data-testid='plan-message']")?.textContent).toBe(
      "permission denied for table users",
    );
    expect(container.querySelector(".state-detail")).not.toBeNull();
    expect(container.querySelector(".banner")).toBeNull();
  });

  it("offers no views at all, because there is no plan to view", () => {
    const { container } = render(<PlanViewer sample={UNAVAILABLE} />);
    expect(container.querySelector(".segmented")).toBeNull();
    expect(container.querySelector("[data-testid='plan-tree']")).toBeNull();
    expect(container.querySelector("[data-testid='plan-json']")).toBeNull();
  });

  it("omits the hash row when the sample has no hash", () => {
    // R3.10's unavailable capture has no plan, so it has no hash; rendering an
    // empty hash cell would be a second claim that something went wrong.
    const { container } = render(<PlanViewer sample={UNAVAILABLE} />);
    expect(container.querySelector(".plan-hash")).toBeNull();
  });
});
