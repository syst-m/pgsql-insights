/* R10.6's row sparkline / R10.7's small multiple — one primitive, so one suite.
 *
 * The geometry is the risky half (a wrong scale still draws a line), so
 * `linePath` is tested as a pure function: vertex count, the flat-domain
 * widening, and the non-finite rule are all assertable from the returned string.
 * The render tests then cover what only the DOM can: the framed/unframed split,
 * the hit-target count, and the tooltip's contents.
 */

import { render, fireEvent } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { linePath, Sparkline } from "./Sparkline";

describe("linePath", () => {
  it("emits one vertex per value, first as a moveto and the rest as linetos", () => {
    const d = linePath([1, 2, 3], 100, 20);
    expect(d.startsWith("M")).toBe(true);
    expect(d.match(/L/g)).toHaveLength(2);
    expect(d.match(/M/g)).toHaveLength(1);
  });

  it("maps the first value to the left edge and the last to the right edge", () => {
    const d = linePath([0, 10], 200, 50);
    const [first, second] = d.split(" ");
    // (0, 50) is bottom-left for a domain of [0,10] over a 50-tall plot, and
    // (200, 0) is top-right. Pinning both ends pins the whole scale.
    expect(first).toBe("M0,50");
    expect(second).toBe("L200,0");
  });

  it("widens a flat domain so a constant series does not pin to the ceiling", () => {
    // Without the widening, scaleLinear gets a zero-width domain and puts every
    // point at y=0 — a flat series drawn as a line at the top of the plot.
    const d = linePath([5, 5, 5], 100, 40);
    const ys = d.split(" ").map((p) => p.split(",")[1]);
    // Widened to [5,6], the value 5 sits on the domain's floor → y = 40, the
    // plot's baseline. Without the widening the scale's t is 0/0 and every
    // coordinate is NaN, which invalidates the whole path string.
    expect(new Set(ys)).toEqual(new Set(["40"]));
    expect(d).not.toContain("NaN");
  });

  it("honours an explicit domain over the values' own range", () => {
    // Two siblings sharing a domain must agree on where the same value sits —
    // this is what makes small multiples comparable instead of four autoscales.
    const a = linePath([5], 100, 40, [0, 10]);
    const b = linePath([5], 100, 40, [0, 20]);
    expect(a).toBe("M0,20"); // halfway up a [0,10] axis
    expect(b).toBe("M0,30"); // a quarter up a [0,20] axis
  });

  it("drops non-finite values instead of poisoning the whole path string", () => {
    // One NaN in a `d` invalidates every vertex in it, not just its own.
    const d = linePath([1, Number.NaN, 3], 100, 20);
    expect(d.match(/M|L/g)).toHaveLength(2);
    expect(d).not.toContain("NaN");
  });

  it("returns an empty string for no values or a zero-sized plot", () => {
    expect(linePath([], 100, 20)).toBe("");
    expect(linePath([1, 2], 0, 20)).toBe("");
    expect(linePath([1, 2], 100, 0)).toBe("");
  });

  it("renders one point as a lone moveto, which draws no line", () => {
    // An SVG moveto with no segment is the honest picture of one minute of data:
    // a line through one point would be a guess about the minute before it.
    expect(linePath([7], 100, 20)).toBe("M0,20");
  });
});

describe("Sparkline", () => {
  const values = [1, 4, 2, 8, 5];

  it("names itself for assistive tech and draws the line and the end marker", () => {
    const { container } = render(<Sparkline values={values} label="Execution time per minute" />);
    const svg = container.querySelector("svg.sparkline");
    expect(svg?.getAttribute("role")).toBe("img");
    expect(svg?.getAttribute("aria-label")).toBe("Execution time per minute");
    expect(container.querySelector("path.spark-line")).not.toBeNull();
    expect(container.querySelector("circle.spark-marker")).not.toBeNull();
  });

  it("omits the axes, the hit layer and the crosshair when unframed", () => {
    // A table cell has no room for axes and no room for a tooltip; the framed
    // variant is the one that adds them, and the split is the component's whole
    // answer to "one primitive, three sizes".
    const { container } = render(<Sparkline values={values} label="x" />);
    expect(container.querySelector(".spark-y-labels")).toBeNull();
    expect(container.querySelector(".spark-x-labels")).toBeNull();
    expect(container.querySelectorAll(".spark-hit")).toHaveLength(0);
    expect(container.querySelector(".chart-frame")).toBeNull();
  });

  it("adds one full-height hit target per point when framed", () => {
    // interaction.md: hit targets bigger than the mark. A 2px stroke is not a
    // target a pointer finds, so the hit layer is per-point rects, not the path.
    const { container } = render(<Sparkline values={values} label="x" framed />);
    expect(container.querySelectorAll(".spark-hit")).toHaveLength(values.length);
    expect(container.querySelector(".spark-y-labels")).not.toBeNull();
    expect(container.querySelector(".spark-x-labels")).not.toBeNull();
  });

  it("shows the hovered point's value and label in the tooltip", () => {
    const { container } = render(
      <Sparkline
        values={values}
        label="x"
        framed
        formatValue={(v) => `${v} ms`}
        xLabels={["a", "b", "c", "d", "e"]}
      />,
    );
    expect(container.querySelector(".viz-tooltip")).toBeNull();
    const hits = container.querySelectorAll(".spark-hit");
    fireEvent.mouseEnter(hits[3] as Element);
    const tip = container.querySelector(".viz-tooltip");
    expect(tip).not.toBeNull();
    // values lead, labels follow (interaction.md)
    expect(tip?.querySelector(".tt-value")?.textContent).toBe("8 ms");
    expect(tip?.querySelector(".tt-label")?.textContent).toBe("d");
    // and the crosshair appears at the hovered x
    expect(container.querySelector(".spark-crosshair")).not.toBeNull();
  });

  it("clears the tooltip on mouse leave", () => {
    const { container } = render(<Sparkline values={values} label="x" framed />);
    const hits = container.querySelectorAll(".spark-hit");
    fireEvent.mouseEnter(hits[0] as Element);
    expect(container.querySelector(".viz-tooltip")).not.toBeNull();
    fireEvent.mouseLeave(hits[0] as Element);
    expect(container.querySelector(".viz-tooltip")).toBeNull();
  });

  it("renders nothing for an empty series rather than an empty box", () => {
    // R10.13's rule starts here: a component with no data returns null and lets
    // its caller say why, instead of drawing a frame around nothing.
    const { container } = render(<Sparkline values={[]} label="x" />);
    expect(container.innerHTML).toBe("");
  });
});
