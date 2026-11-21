/* The chart's own contract (R10.4 + the dataviz mark specs).
 *
 * These assert the parts a screenshot cannot check cheaply: that the fills are
 * the status tokens, that adjacent segments keep a 2px surface gap, that the
 * legend carries the same numbers the bar draws, and that a direct label is
 * only drawn when it fits.
 *
 * Every test renders its own tree through `bar()` rather than sharing one
 * render at describe scope: RTL's automatic cleanup (vite.config.ts sets
 * `globals: true`) unmounts after each test, so a describe-level container is
 * empty from the second test onwards. */

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { FIXTURE_CLUSTERS, FIXTURE_EMPTY } from "../api/fixtures";
import type { ClusterSummary } from "../api/types";
import { ClusterTable } from "./ClusterTable";
import { HealthBar, countsByHealth, describeDistribution, measureText } from "./HealthBar";
import { DEFAULT_STATE } from "../state/urlState";

/** Render a bar over `clusters` and hand back its container. */
function bar(clusters: readonly ClusterSummary[] = FIXTURE_CLUSTERS, width = 720): HTMLElement {
  return render(<HealthBar clusters={clusters} width={width} />).container;
}

/** The data table's rows, never the legend table's. */
function tableRows(container: HTMLElement): Element[] {
  return Array.from(container.querySelectorAll("table.data tbody tr"));
}

describe("countsByHealth", () => {
  it("counts the fixture set correctly", () => {
    expect(countsByHealth(FIXTURE_CLUSTERS)).toEqual({ healthy: 3, degraded: 0, down: 1, unknown: 0 });
  });

  it("counts an empty fleet as all zeros", () => {
    expect(countsByHealth([])).toEqual({ healthy: 0, degraded: 0, down: 0, unknown: 0 });
  });
});

describe("HealthBar", () => {
  it("draws one fill per non-empty health category", () => {
    const container = bar();
    const fills = Array.from(container.querySelectorAll("rect[fill^='var(--status-']"));
    expect(fills).toHaveLength(2); // healthy + down; the two zero counts draw nothing
    expect(fills.map((f) => f.getAttribute("fill"))).toEqual(["var(--status-good)", "var(--status-critical)"]);
  });

  it("sizes the fills proportionally to their counts", () => {
    const container = bar();
    const fills = Array.from(container.querySelectorAll("rect[fill^='var(--status-']")) as SVGRectElement[];
    const widths = fills.map((f) => Number(f.getAttribute("width")));
    // 720px across 4 clusters → 180px per cluster, minus the half-gaps.
    expect(widths[0]).toBeCloseTo(539, 0); // 3 clusters
    expect(widths[1]).toBeCloseTo(179, 0); // 1 cluster
  });

  it("keeps a 2px surface gap between adjacent fills", () => {
    const container = bar();
    const fills = Array.from(container.querySelectorAll("rect[fill^='var(--status-']")) as SVGRectElement[];
    const first = fills[0];
    const second = fills[1];
    if (first === undefined || second === undefined) throw new Error("expected two fills");
    const firstEnd = Number(first.getAttribute("x")) + Number(first.getAttribute("width"));
    const secondStart = Number(second.getAttribute("x"));
    expect(secondStart - firstEnd).toBeCloseTo(2, 6);
  });

  it("rounds only the data end: the bar shape is a 4px-rounded, square-based path", () => {
    const container = bar();
    const path = container.querySelector("clipPath path");
    if (path === null) throw new Error("expected a clipPath path");
    const d = path.getAttribute("d") ?? "";
    expect(d).toMatch(/^M 0 0/); // square top-left, on the baseline
    expect(d).toContain("A 4 4"); // rounded right corners
  });

  it("gives every segment a hover and focus target with a text label", () => {
    const container = bar();
    const hits = Array.from(container.querySelectorAll("rect.hit-target"));
    expect(hits).toHaveLength(2);
    expect(hits.map((h) => h.getAttribute("aria-label"))).toEqual(["Healthy: 3 of 4", "Down: 1 of 4"]);
    expect(hits.every((h) => h.getAttribute("tabindex") === "0")).toBe(true);
  });

  it("labels segments directly, in ink, only where the label fits", () => {
    const container = bar();
    const labels = Array.from(container.querySelectorAll("text.mark-label"));
    // "3" fits in a 539px segment and "1" in a 179px one.
    expect(labels.map((l) => l.textContent)).toEqual(["3", "1"]);
    expect(labels.every((l) => l.getAttribute("fill") === "var(--text-secondary)")).toBe(true);
  });

  it("ships a legend table carrying the same numbers as the bar (the chart's table twin)", () => {
    const container = bar();
    const rows = Array.from(container.querySelectorAll("table.legend-table tbody tr"));
    expect(rows).toHaveLength(4);
    const cells = rows.map((r) =>
      Array.from(r.querySelectorAll("th,td")).map((c) => c.textContent?.trim() ?? ""),
    );
    expect(cells).toEqual([
      ["Healthy", "3", "75%"],
      ["Degraded", "0", "0%"],
      ["Down", "1", "25%"],
      ["Unknown", "0", "0%"],
    ]);
  });

  it("gives the svg a text equivalent of the whole chart", () => {
    const container = bar();
    const svg = container.querySelector("svg.healthbar");
    expect(svg?.getAttribute("role")).toBe("img");
    expect(svg?.getAttribute("aria-label")).toBe(
      "Health distribution across 4 clusters: 3 healthy, 1 down.",
    );
  });

  it("renders nothing for an empty fleet", () => {
    const container = bar(FIXTURE_EMPTY.clusters);
    expect(container.querySelector("svg.healthbar")).toBeNull();
  });
});

describe("measureText", () => {
  it("returns a positive width for a non-empty string", () => {
    expect(measureText("123")).toBeGreaterThan(0);
  });

  it("grows with the string length", () => {
    expect(measureText("12345")).toBeGreaterThan(measureText("1"));
  });
});

describe("describeDistribution", () => {
  it("names only the categories that exist", () => {
    expect(describeDistribution(FIXTURE_CLUSTERS)).toBe("Health distribution across 4 clusters: 3 healthy, 1 down.");
  });
});

describe("ClusterTable", () => {
  const noop = (): void => {};

  /** Render the table on its own so the assertions cannot be satisfied by
   *  markup from another test's render. */
  function table(rows: readonly ClusterSummary[] = FIXTURE_CLUSTERS): HTMLElement {
    return render(<ClusterTable rows={rows} state={DEFAULT_STATE} onSort={noop} onOpen={noop} />).container;
  }

  it("renders one row per cluster", () => {
    expect(tableRows(table())).toHaveLength(4);
  });

  it("marks exactly one header as sorted, with the direction", () => {
    const container = table();
    const sorted = Array.from(container.querySelectorAll("th[aria-sort]"));
    expect(sorted).toHaveLength(1);
    expect(sorted[0]?.getAttribute("aria-sort")).toBe("descending");
    expect(sorted[0]?.textContent).toContain("Health");
  });

  it("links each row to its detail route", () => {
    const container = table();
    const hrefs = Array.from(container.querySelectorAll("a.cluster-link")).map((a) => a.getAttribute("href"));
    expect(hrefs).toContain("/c/ai-api-gateway/rev-proxy-pg");
    expect(hrefs).toContain("/c/freeidp/freeidp-pg");
  });

  it("shows all three instance counts, not just the spec count (R1.2)", () => {
    const container = table();
    const freeidp = tableRows(container).find((r) => r.textContent?.includes("freeidp-pg"));
    const cell = freeidp?.querySelector("td[title]");
    expect(cell?.getAttribute("title")).toBe("0 ready, 0 serving of 2 (R1.2)");
    expect(cell?.textContent).toBe("0/2");
  });
});
