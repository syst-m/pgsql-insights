/* R10.23's non-colour half, as the component pair it is.
 *
 * The requirement forbids encoding state in the hue alone, and this file's own
 * header explains why that is not a style preference here: light-mode
 * `--status-warning` (#fab219) and `--status-serious` (#ec835a) both sit below
 * 3:1 on the light surface, so a colour-only dot is genuinely hard to read.
 * The mitigation is a shape *and* a text label per health value, which makes
 * the testable claim a property of the set rather than four separate ones — no
 * two health values may share a shape, and none may lack a label. Asserting it
 * that way is the difference between "the four glyphs look right today" and
 * "a fifth health value cannot be added without giving it a fifth shape".
 *
 * `shape()` is module-private, so every geometry assertion goes through the
 * rendered `<svg>`. That is the right surface anyway: what R10.23 cares about
 * is what a reader perceives, and the `d` string is what carries it. The
 * expected strings are the formulas in `shape()` evaluated at the default
 * size — a golden, the same approach `HealthBar.test.tsx` takes with
 * `expect(d).toMatch(/^M 0 0/)`.
 */

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Glyph, StatusChip, StatusGlyph } from "./StatusGlyph";
import { PANEL_GLYPHS } from "./StatePanel";
import { HEALTH_ORDER, presentationFor } from "../api/health";
import type { ClusterHealth } from "../api/types";

/** Render one health and hand back its `<svg>` and the `<path>` inside it. */
function glyph(health: ClusterHealth, size?: number) {
  const { container } = render(<StatusGlyph health={health} size={size} />);
  // The casts are the point, not ceremony: `querySelector` hands back a bare
  // `Element`, which has no `style`, so an uncast helper would force every
  // colour assertion into a cast of its own.
  const svg = container.querySelector("svg.status-glyph") as SVGSVGElement | null;
  if (svg === null) throw new Error(`no svg.status-glyph for ${health}`);
  const path = svg.querySelector("path") as SVGPathElement | null;
  if (path === null) throw new Error(`no path inside svg.status-glyph for ${health}`);
  return { svg, path };
}

/** `shape(10, p.glyph)` for each health, written out. `shape` is not exported,
 *  so this is the formula duplicated rather than called — which is what makes
 *  the test a check on the geometry instead of a tautology. */
const PATHS_AT_10: Record<ClusterHealth, string> = {
  // circle: r = 5, closed with a 0.001 epsilon so the arc renders.
  healthy: "M 5 0 A 5 5 0 1 1 4.999 0 Z",
  // triangle: apex at (5,0), base along y = 10.
  degraded: "M 5 0 L 10 10 L 0 10 Z",
  // cross: a plus with t = max(1.6, 1.8) = 1.8, so a = 4.1 and b = a + t.
  // b stringifies as 5.8999999999999995, not 5.9 — 4.1 + 1.8 is not exactly
  // 5.9 in binary floating point. Pinned verbatim on purpose: a golden that
  // rounds the number is a golden that quietly accepts a changed thickness.
  down: "M 4.1 0 H 5.8999999999999995 V 4.1 H 10 V 5.8999999999999995 H 5.8999999999999995 V 10 H 4.1 V 5.8999999999999995 H 0 V 4.1 H 4.1 Z",
  // dash: a bar of height max(1, 1.2) = 1.2 centred on r = 5.
  unknown: "M 0 3.8 H 10 V 6.2 H 0 Z",
};

describe("StatusGlyph", () => {
  it("gives every health value a shape no other value shares", () => {
    // The set property, not the four instances: R10.23's whole argument is that
    // a reader tells the states apart without colour, and two health values
    // drawn with one shape breaks that in a way four separate assertions would
    // each miss. `HEALTH_ORDER` is health.ts's own list, so a new health value
    // has to appear here too.
    const drawn = HEALTH_ORDER.map((h) => glyph(h).path.getAttribute("d"));
    expect(new Set(drawn).size).toBe(HEALTH_ORDER.length);
    for (const d of drawn) expect(d).toBeTruthy();
  });

  it("draws each health's documented shape, not merely a different one", () => {
    // Distinctness alone would pass if every glyph became a circle-plus-one-pixel
    // nudge. This pins the shape family the header names per health: circle for
    // healthy, triangle for degraded, cross for down, dash for unknown.
    for (const health of HEALTH_ORDER) {
      expect(glyph(health).path.getAttribute("d")).toBe(PATHS_AT_10[health]);
    }
  });

  it("takes its colour from the status token and fills the path with currentColor", () => {
    // One rule sets colour: the `<svg>` carries the token and the path inherits
    // it. A chart that recolours a glyph, or a theme that changes the token,
    // never has to touch the path — and the `unknown` case is why the token
    // matters: it is `--text-muted`, deliberately not a status token, because
    // no data is not a state and borrowing `--status-serious` would read as an
    // alarm.
    for (const health of HEALTH_ORDER) {
      const { svg } = glyph(health);
      expect(svg.style.color).toBe(presentationFor(health).token);
      expect(glyph(health).path.getAttribute("fill")).toBe("currentColor");
    }
    expect(presentationFor("unknown").token).toBe("var(--text-muted)");
  });

  it("names the health to assistive tech and carries it as a styling hook", () => {
    for (const health of HEALTH_ORDER) {
      const { svg } = glyph(health);
      expect(svg.getAttribute("role")).toBe("img");
      expect(svg.getAttribute("aria-label")).toBe(presentationFor(health).label);
      expect(svg.getAttribute("data-health")).toBe(health);
      // `flex: 0 0 auto` is what stops a glyph next to a wrapping label from
      // being squashed to a sliver by the flex line.
      expect(svg.style.flex).toBe("0 0 auto");
    }
  });

  it("sizes the box from the prop, defaulting to 10", () => {
    // The default is the size the header ties to a 13px label; the prop exists
    // for the chart, where the glyph sits beside 11px text. All three of
    // width/height/viewBox move together, because a 16px box with a 10px
    // viewBox scales the shape instead of resizing it.
    const small = glyph("healthy");
    expect(small.svg.getAttribute("width")).toBe("10");
    expect(small.svg.getAttribute("height")).toBe("10");
    expect(small.svg.getAttribute("viewBox")).toBe("0 0 10 10");

    const big = glyph("healthy", 16);
    expect(big.svg.getAttribute("width")).toBe("16");
    expect(big.svg.getAttribute("height")).toBe("16");
    expect(big.svg.getAttribute("viewBox")).toBe("0 0 16 16");
    // The path is drawn in the new box (r = 8), not scaled by the viewBox.
    expect(big.path.getAttribute("d")).toBe("M 8 0 A 8 8 0 1 1 7.999 0 Z");
  });
});

describe("StatusChip", () => {
  it("pairs the glyph and the label in one element", () => {
    // R10.23's mitigation is the *pairing*, so the chip is the component the
    // requirement actually names: the shape carries it for a sighted reader and
    // the text carries it for everyone else. The chip's whole text is the
    // label — nothing decorative lands in the accessible name.
    for (const health of HEALTH_ORDER) {
      const { container } = render(<StatusChip health={health} />);
      const chip = container.querySelector("span.status-chip");
      expect(chip).not.toBeNull();
      expect(chip?.getAttribute("data-health")).toBe(health);
      expect(chip?.querySelector("svg.status-glyph")).not.toBeNull();
      expect(chip?.textContent).toBe(presentationFor(health).label);
    }
  });

  it("composes the glyph instead of redrawing it", () => {
    // The header's instruction — "use the bare glyph only inside the chart" —
    // is only true if the chip renders the same path the glyph renders. Two
    // copies of the shape in one file is how they start to disagree.
    for (const health of HEALTH_ORDER) {
      const { container } = render(<StatusChip health={health} size={12} />);
      const inChip = container.querySelector("svg.status-glyph path")?.getAttribute("d");
      expect(inChip).toBe(glyph(health, 12).path.getAttribute("d"));
    }
  });
});

describe("Glyph -- the generalised primitive", () => {
  // R10.23's subject is every state, not every health, and `Glyph` is the
  // generalisation IMPLEMENTATION.md:1140 now records as shipped. The health block
  // above covers `StatusGlyph`; this one covers the primitive drawn directly
  // from a presentation, which is how `StatePanel.tsx`'s `PANEL_GLYPHS` now
  // reaches it. The property is the same one the health block asserts -- a
  // distinct shape per state -- asserted one level down so it holds for the
  // second vocabulary too, not just the first.
  const panels = Object.entries(PANEL_GLYPHS);

  it("draws a distinct shape for every panel state", () => {
    const drawn = panels.map(
      ([, p]) => render(<Glyph p={p} />).container.querySelector("svg.status-glyph path")?.getAttribute("d"),
    );
    expect(drawn.every((d) => typeof d === "string" && d.length > 0)).toBe(true);
    expect(new Set(drawn).size).toBe(panels.length);
  });

  it("names and colours every presentation it is handed", () => {
    for (const [state, p] of panels) {
      const { container } = render(<Glyph p={p} attrs={{ "data-state": state }} />);
      const svg = container.querySelector("svg.status-glyph") as SVGSVGElement | null;
      expect(svg).not.toBeNull();
      expect(svg?.getAttribute("role")).toBe("img");
      expect(svg?.getAttribute("aria-label")).toBe(p.label);
      expect(svg?.getAttribute("data-state")).toBe(state);
      expect(svg?.style.color).toBe(p.token);
      expect(p.token).toMatch(/^var\(--/);
      expect(p.label.length).toBeGreaterThan(0);
    }
  });

  it("keeps the two vocabularies' shapes one shared vocabulary", () => {
    // `GlyphShape` is the union `api/health.ts` declares, widened by `ring`.
    // Typing the expectation rather than deriving it is the point: a seventh
    // shape added to the union has to appear in one of the two tables, and the
    // day it does not, this is the test that says so.
    const used = new Set<string>([
      ...HEALTH_ORDER.map((h) => presentationFor(h).glyph),
      ...panels.map(([, p]) => p.glyph),
    ]);
    expect([...used].sort()).toEqual(["circle", "cross", "dash", "ring", "square", "triangle"]);
  });

  it("draws the ring as an annulus, not a filled disc", () => {
    // The one shape with no health counterpart. Two subpaths -- outer circle
    // clockwise, inner counter-clockwise -- are what the nonzero fill rule
    // needs to leave a hole; a single `A` would give a disc indistinguishable
    // from `healthy`'s circle at 10px.
    const d = render(<Glyph p={PANEL_GLYPHS.unavailable} size={10} />)
      .container.querySelector("svg.status-glyph path")
      ?.getAttribute("d") as string;
    const subpaths = d.split("M").filter((s) => s.trim().length > 0);
    expect(subpaths.length).toBe(2);
    expect(d).toContain("A 5 5 0 1 1");
    expect(d).toContain("A 3.2 3.2 0 1 0");
  });
});

