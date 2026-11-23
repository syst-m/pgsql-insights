/* R10.6 — the load bar, and the two states that must not look like it.
 *
 * The bar itself is one number and one width, so the tests that matter are the
 * edges (clamping, NaN) and the *adjacency*: `—` (R10.4 "not yet") and the
 * `n/a (no per-query source, §5)` literal (R3.2 "not ever") are two different
 * claims that a careless implementation renders identically. Both are asserted
 * here against the exact strings, and against each other.
 */

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { NULL_VALUE } from "../lib/format";
import {
  ANALYZE_LATENCY_GAP,
  LOCK_WAIT_NA,
  LoadBar,
  LoadCell,
  LoadNotApplicable,
  LoadSourcePending,
} from "./LoadBars";

describe("LoadBar", () => {
  it("prints the value as text and draws the bar as a separate element", () => {
    // marks-and-anatomy: text wears text tokens. A number drawn inside the
    // filled part is two ink layers carrying one fact.
    const { container } = render(<LoadBar fraction={0.25} text="842 ms" />);
    expect(container.querySelector(".load-value")?.textContent).toBe("842 ms");
    expect(container.querySelector(".load-fill")?.getAttribute("style")).toContain("width: 25%");
  });

  it("clamps out-of-range fractions instead of overflowing the track", () => {
    const width = (fraction: number) => {
      const { container } = render(<LoadBar fraction={fraction} text="x" />);
      return container.querySelector(".load-fill")?.getAttribute("style");
    };
    expect(width(1.5)).toBe("width: 100%;");
    expect(width(-0.2)).toBe("width: 0%;");
    expect(width(Number.NaN)).toBe("width: 0%;");
  });

  it("defaults the tooltip to the value, and lets a derived column name its source instead", () => {
    const plain = render(<LoadBar fraction={0.5} text="842 ms" />);
    expect(plain.container.querySelector(".load-cell")?.getAttribute("title")).toBe("842 ms");

    // R10.19: a derived column's tooltip names the derivation, because
    // "derived" is the information a reader needs in order to trust it.
    const derived = render(
      <LoadBar fraction={0.5} text="842 ms" title="Load by IO wait — 842 ms (derived: blk_read_time + blk_write_time)" />,
    );
    expect(derived.container.querySelector(".load-cell")?.getAttribute("title")).toContain("derived");
  });

  it("is not itself a link — the row is the click target (R10.19)", () => {
    const { container } = render(<LoadBar fraction={0.5} text="842 ms" />);
    expect(container.querySelector("a")).toBeNull();
  });

  it("marks the track aria-hidden so the screen reader reads the text, not a percentage twice", () => {
    const { container } = render(<LoadBar fraction={0.5} text="842 ms" />);
    expect(container.querySelector(".load-track")?.getAttribute("aria-hidden")).toBe("true");
  });
});

describe("the two non-bar states", () => {
  it("renders R3.2's lock-wait literal exactly as requirements.md:306 spells it", () => {
    expect(LOCK_WAIT_NA).toBe("n/a (no per-query source, §5)");
    const { container } = render(<LoadNotApplicable text={LOCK_WAIT_NA} />);
    expect(container.textContent).toBe(LOCK_WAIT_NA);
  });

  it("renders the Gap marker exactly as requirements.md:306 spells it", () => {
    expect(ANALYZE_LATENCY_GAP).toBe("deferred (Gap — auto_explain, §5)");
  });

  it("draws no track for an n/a cell", () => {
    // A track with no fill means "measured, and it was zero". This cell was
    // never measured, so it gets no track at all.
    const { container } = render(<LoadNotApplicable text={LOCK_WAIT_NA} />);
    expect(container.querySelector(".load-track")).toBeNull();
  });

  it("renders R10.4's `—` for a source that has not run yet, and names the source", () => {
    const { container } = render(<LoadSourcePending source="R7.x recommendations" />);
    expect(container.querySelector(".load-pending")?.textContent).toContain(NULL_VALUE);
    expect(container.querySelector(".pending-marker")?.textContent).toBe("R7.x recommendations");
  });

  it("keeps `—` and the n/a literal distinguishable", () => {
    // The whole point of the split: `—` = not yet, `n/a` = not ever. If they
    // ever collapse to one glyph, this fails.
    const na = render(<LoadNotApplicable text={LOCK_WAIT_NA} />);
    const pending = render(<LoadSourcePending source="R7.x recommendations" />);
    expect(na.container.textContent).not.toBe(pending.container.textContent);
    expect(na.container.textContent).not.toBe(NULL_VALUE);
    expect(pending.container.textContent).toContain(NULL_VALUE);
  });
});

describe("LoadCell", () => {
  const cases = [
    { props: { kind: "bar", fraction: 0.5, text: "842 ms" } as const, testid: undefined, text: "842 ms" },
    { props: { kind: "not-applicable", text: LOCK_WAIT_NA } as const, testid: "load-na", text: LOCK_WAIT_NA },
    {
      props: { kind: "source-pending", source: "R7.x recommendations" } as const,
      testid: "load-pending",
      text: NULL_VALUE,
    },
    // No `kind: "none"` case: it was a fourth member whose render was a bare
    // `—`, and `—` belongs to `source-pending` alone (see LoadBars.tsx's note on
    // the glyph). A column with no load cell is now `loadCellFor` returning
    // `null`, which is `QueryTable.test.tsx`'s assertion, not a `LoadCell` case.
  ];

  for (const c of cases) {
    it(`dispatches kind "${c.props.kind}" to the right rendering`, () => {
      const { container } = render(<LoadCell {...c.props} />);
      expect(container.textContent).toContain(c.text);
      if (c.testid) {
        expect(container.querySelector(`[data-testid="${c.testid}"]`)).not.toBeNull();
      }
    });
  }

  it("exhausts the union so a new kind is a compile error, not an unhandled render", () => {
    // The switch in LoadBars.tsx has no default branch: adding a fourth kind
    // without handling it fails typecheck. This test only pins the three that
    // exist, so the compiler stays the enforcement.
    expect(cases.map((c) => c.props.kind).sort()).toEqual(
      ["bar", "not-applicable", "source-pending"],
    );
  });
});
