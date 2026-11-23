/* R10.18 — the virtualization math and the density control.
 *
 * R10.18's whole correctness is two claims: the rendered slice is a window, and
 * the spacers keep the scrollbar the length of the full list. Both are
 * arithmetic, so most of this file tests `windowFor` directly rather than
 * through a DOM — a render-based test can only ever show one (start, end) pair,
 * while the function has to be right at the top, the bottom, and past the end.
 *
 * The component tests then assert the thing the math can't: that the DOM
 * actually contains the window and not the list.
 */

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import {
  DensityToggle,
  ROW_HEIGHT,
  VirtualList,
  windowFor,
  type Window,
} from "./VirtualList";

describe("windowFor", () => {
  it("renders nothing for an empty list, and no spacers", () => {
    expect(windowFor(0, 36, 200, 0, 6)).toEqual({
      start: 0,
      end: 0,
      padTop: 0,
      padBottom: 0,
      totalHeight: 0,
    });
  });

  it("windows a 500-row list at the top: 18 rows, not 500", () => {
    // ceil(200/36) = 6 visible, + 6 overscan each side.
    const w = windowFor(500, 36, 200, 0, 6);
    expect(w).toEqual({
      start: 0,
      end: 18,
      padTop: 0,
      padBottom: (500 - 18) * 36,
      totalHeight: 500 * 36,
    });
  });

  it("slides the window when scrolled, and grows padTop to match", () => {
    // scrollTop 360 is row 10; minus the overscan band the window starts at 4.
    const w = windowFor(500, 36, 200, 360, 6);
    expect(w.start).toBe(4);
    expect(w.end).toBe(22);
    expect(w.padTop).toBe(4 * 36);
    expect(w.padBottom).toBe((500 - 22) * 36);
  });

  it("keeps padTop + rendered + padBottom equal to the total height", () => {
    // The invariant the scrollbar length depends on, at four scroll positions.
    for (const scrollTop of [0, 36, 1_800, 17_999]) {
      const w = windowFor(500, 36, 200, scrollTop, 6);
      expect(w.padTop + (w.end - w.start) * 36 + w.padBottom).toBe(w.totalHeight);
    }
  });

  it("clamps the window to the list at the bottom", () => {
    // 10 rows in a 1000px viewport: the window is the whole list, and the
    // bottom pad is 0 rather than negative.
    const w = windowFor(10, 36, 1000, 0, 6);
    expect(w.start).toBe(0);
    expect(w.end).toBe(10);
    expect(w.padBottom).toBe(0);
  });

  it("clamps start when scrolled past the end", () => {
    const w = windowFor(10, 36, 200, 999_999, 6);
    expect(w.start).toBe(9);
    expect(w.end).toBe(10);
  });

  it("treats a zero row height as 1px rather than dividing by zero", () => {
    const w: Window = windowFor(4, 0, 100, 0, 2);
    expect(Number.isFinite(w.totalHeight)).toBe(true);
    expect(w.totalHeight).toBe(4);
  });

  it("renders the overscan band, not zero rows, in a zero-height viewport", () => {
    // jsdom reports offsetHeight 0 for an unstyled div. A viewport of 0 must
    // not read as an empty table.
    const w = windowFor(500, 36, 0, 0, 6);
    expect(w.end - w.start).toBe(12);
  });
});

describe("VirtualList", () => {
  const items = Array.from({ length: 500 }, (_, i) => `row-${i}`);

  it("renders the window, not the list", () => {
    const { container } = render(
      <VirtualList
        items={items}
        rowHeight={36}
        height={200}
        keyFor={(x) => x}
        renderItem={(x) => <span>{x}</span>}
      />,
    );
    const rows = container.querySelectorAll(".virtual-row");
    expect(rows.length).toBe(18);
    expect(rows[0]?.textContent).toBe("row-0");
    expect(container.querySelector('[data-testid="virtual-list"]')?.getAttribute("data-count")).toBe("500");
  });

  it("renders exactly the window's items, in order", () => {
    const { container } = render(
      <VirtualList items={items} rowHeight={36} height={200} keyFor={(x) => x} renderItem={(x) => <span>{x}</span>} />,
    );
    const rendered = Array.from(container.querySelectorAll(".virtual-row")).map((n) => n.textContent);
    expect(rendered).toEqual(Array.from({ length: 18 }, (_, i) => `row-${i}`));
  });

  it("renders an empty list as an empty window, not a missing container", () => {
    const { container } = render(
      <VirtualList items={[]} rowHeight={36} height={200} keyFor={(x) => x} renderItem={(x) => <span>{x}</span>} />,
    );
    expect(container.querySelector('[data-testid="virtual-list"]')).not.toBeNull();
    expect(container.querySelector(".virtual-inner")?.getAttribute("style")).toContain("height: 0px");
  });
});

describe("density (the second half of R10.18)", () => {
  it("names both densities the requirement names, at 24 and 36 px", () => {
    expect(ROW_HEIGHT).toEqual({ compact: 24, comfortable: 36 });
  });

  it("renders a pressed state for the active density only", () => {
    const { container } = render(<DensityToggle density="compact" onChange={() => {}} />);
    const buttons = Array.from(container.querySelectorAll("button"));
    expect(buttons.map((b) => b.textContent)).toEqual(["Compact", "Comfortable"]);
    expect(buttons.map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "false"]);
  });
});
