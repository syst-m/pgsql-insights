/* The chart's own contract: R10.9's bar half (`requirements.md:422`) and the
 * dataviz mark specs the component header claims to follow.
 *
 * Organised by the three exports, because each is a separate contract and the
 * component is mostly the second one rendered:
 *
 *   · `waitBuckets` — the grouping rule. This is where the interesting cases
 *     live, because the shipped fixture cannot show two of the three bucket
 *     kinds: `testdata/api/active-queries.json` has three rows, all with
 *     `unknownWaitEvent: false` and a non-empty pair, so the Unrecognised and
 *     Not-waiting buckets are built by spreading a fixture row and flipping the
 *     one field under test (`rowWith`), never by writing a row out by hand.
 *   · `WAIT_RAMP` / `rampTier` / `rampStep` — the colour rule. The test pins the
 *     constant to the validator's own input string — the invocation the component
 *     header records — because `tokens.css` cannot be read from a test:
 *     `web/vite.config.ts` sets `css: false`, so every `.css` import, `?raw` and
 *     `import.meta.glob` included, resolves to an empty module under vitest.
 *   · `WaitEventsChart` — the drawing. Bar geometry, the axis tokens, the
 *     selection marker, the tooltip, and the table twin.
 *
 * Every test renders its own tree: RTL's automatic cleanup (vite.config.ts sets
 * `globals: true`) unmounts after each test, so a describe-level container is
 * empty from the second test onwards — the same reason `web/src/components/HealthBar.test.tsx:8-11`
 * gives for the same shape.
 */

import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { FIXTURE_ACTIVE_QUERIES } from "../api/fixtures";
import type { SessionView } from "../api/types";
import {
  describeWaitBuckets,
  rampStep,
  rampTier,
  WAIT_RAMP,
  WaitEventsChart,
  waitBuckets,
  type WaitBucket,
} from "./WaitEventsChart";

/** The fixture's three rows, named. Spreading one rather than writing a row keeps
 *  these cases honest about the *other* fields. */
const LOCK = FIXTURE_ACTIVE_QUERIES.data[0]!; // Lock/transactionid, active
const CLIENT = FIXTURE_ACTIVE_QUERIES.data[1]!; // Client/ClientRead, idle in transaction
const IO = FIXTURE_ACTIVE_QUERIES.data[2]!; // IO/DataFileRead, active

function rowWith(row: SessionView, overrides: Partial<SessionView>): SessionView {
  return { ...row, ...overrides };
}

/** Render the chart and hand back the container. */
function chart(
  buckets: readonly WaitBucket[],
  props: { width?: number; selectedKey?: string | null; onSelect?: (key: string) => void } = {},
): HTMLElement {
  return render(<WaitEventsChart buckets={buckets} {...props} />).container;
}

/* ── the grouping rule ─────────────────────────────────────────────────────── */

describe("waitBuckets", () => {
  it("buckets the fixture's three sessions by their wait-event type", () => {
    const buckets = waitBuckets(FIXTURE_ACTIVE_QUERIES.data);
    // Three distinct types, one session each, so the sort is count desc → label
    // asc with every count tied: alphabetical.
    expect(buckets.map((b) => [b.key, b.count])).toEqual([
      ["Client", 1],
      ["IO", 1],
      ["Lock", 1],
    ]);
    expect(buckets.every((b) => b.kind === "named")).toBe(true);
  });

  it("groups two sessions on the same pair into one bucket with one event row", () => {
    const buckets = waitBuckets([LOCK, rowWith(LOCK, { pid: 41999 })]);
    expect(buckets).toHaveLength(1);
    const lock = buckets[0]!;
    expect(lock.count).toBe(2);
    expect(lock.events).toEqual([
      { event: "transactionid", count: 2, sessions: [LOCK, rowWith(LOCK, { pid: 41999 })] },
    ]);
  });

  it("gives an unrecognised pair its own bucket instead of folding it into its type", () => {
    // The coordinator's ruling on this page: "`unknownWaitEvent: true` is a third
    // bucket, not a subset of a named type." The fixture's Lock row, flagged.
    const buckets = waitBuckets([LOCK, rowWith(LOCK, { pid: 41999, unknownWaitEvent: true })]);
    expect(buckets.map((b) => [b.key, b.count])).toEqual([
      ["Lock", 1],
      ["unrecognised", 1],
    ]);
    expect(buckets[1]!.label).toBe("Unrecognised");
    // The pair is the row identity inside that bucket, per the Go rollup's
    // `{Type, Event}` key (`internal/collector/activity.go:636`).
    expect(buckets[1]!.events[0]!.event).toBe("Lock/transactionid");
  });

  it("keeps a session with no wait event as its own bucket", () => {
    // The documented divergence: `internal/collector/activity.go:697-699` drops
    // these rows from the rollup. Here they are a bucket, so the bars' total
    // equals the payload's session count.
    const buckets = waitBuckets([rowWith(LOCK, { waitEventType: "", waitEvent: "" })]);
    expect(buckets).toHaveLength(1);
    expect(buckets[0]!.key).toBe("not-waiting");
    expect(buckets[0]!.label).toBe("Not waiting");
    expect(buckets[0]!.events[0]!.event).toBe("");
  });

  it("sorts buckets by count descending, then by label", () => {
    const buckets = waitBuckets([
      LOCK,
      rowWith(LOCK, { pid: 41999 }),
      CLIENT,
      rowWith(LOCK, { pid: 41998, waitEventType: "Activity", waitEvent: "LogicalApplyMain" }),
    ]);
    expect(buckets.map((b) => [b.key, b.count])).toEqual([
      ["Lock", 2],
      ["Activity", 1],
      ["Client", 1],
    ]);
  });

  it("sorts the events inside a bucket by count descending, then by name", () => {
    const buckets = waitBuckets([
      LOCK,
      rowWith(LOCK, { pid: 41999 }),
      rowWith(LOCK, { pid: 41998, waitEvent: "relation" }),
    ]);
    expect(buckets[0]!.events.map((e) => [e.event, e.count])).toEqual([
      ["transactionid", 2],
      ["relation", 1],
    ]);
  });

  it("returns nothing for no sessions", () => {
    expect(waitBuckets([])).toEqual([]);
  });
});

/* ── the colour rule ───────────────────────────────────────────────────────── */

describe("WAIT_RAMP", () => {
  it("is exactly the four steps the header names, in validator order", () => {
    // This is the component header's documented validator invocation —
    // `node validate_palette.js "#86b6ef,#5598e7,#2a78d6,#184f95" --mode light
    // --ordinal` — restated as the constant's own value, so the test pins the
    // string that was validated rather than a second opinion on it. The token
    // names are in the same table because the drawing tests assert the bars paint
    // with `var(--blue-*)`: this is what ties those names to these hexes.
    expect(WAIT_RAMP.map((s) => `${s.token}:${s.hex}`)).toEqual([
      "--blue-250:#86b6ef",
      "--blue-350:#5598e7",
      "--blue-450:#2a78d6",
      "--blue-600:#184f95",
    ]);
  });

  it("runs light to dark, which is what a sequential ramp means", () => {
    // Relative luminance, computed rather than eyeballed, and in the same order
    // the validator's ordinal mode checks (lightness monotonicity).
    const lum = (hex: string): number => {
      const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
      return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!;
    };
    const lightness = WAIT_RAMP.map((s) => lum(s.hex));
    expect([...lightness].sort((a, b) => b - a)).toEqual(lightness);
  });
});

describe("rampTier", () => {
  it("assigns quartiles of the largest bucket, not ranks", () => {
    const cases: [number, number, number][] = [
      [10, 10, 3], // > 75 %
      [8, 10, 3],
      [7, 10, 2], // > 50 %
      [5, 10, 1],
      [4, 10, 1], // > 25 %
      [3, 10, 1],
      [2, 10, 0],
      [1, 10, 0],
      [0, 0, 0], // degenerate: nothing to scale against
      [0, 5, 0],
    ];
    for (const [count, max, tier] of cases) {
      expect(rampTier(count, max), `rampTier(${count}, ${max})`).toBe(tier);
    }
  });

  it("gives two equal counts the same step", () => {
    // The property a rank-based colouring loses: equal values, equal colour.
    const rows = [LOCK, rowWith(LOCK, { pid: 41999 }), CLIENT];
    const buckets = waitBuckets(rows);
    const lock = buckets.find((b) => b.key === "Lock")!;
    const tied = waitBuckets([LOCK, CLIENT]);
    const lockTied = tied.find((b) => b.key === "Lock")!;
    expect(rampStep(lock.count, 2).token).toBe(rampStep(lockTied.count, 1).token);
  });
});

/* ── the drawing ───────────────────────────────────────────────────────────── */

/** Two buckets, one tied pair and one single, so the tiers differ: Lock 2/2 →
 *  `--blue-600` (r 1.0), Client 1/2 → `--blue-350` (r 0.5, the top of tier 1). */
const TWO_BUCKETS = waitBuckets([LOCK, rowWith(LOCK, { pid: 41999 }), CLIENT]);

describe("WaitEventsChart", () => {
  it("renders a card with the chart frame and one bar per bucket", () => {
    const container = chart(TWO_BUCKETS);
    expect(container.querySelector('[data-panel="wait-events"]')).not.toBeNull();
    expect(container.querySelector(".chart-frame")).not.toBeNull();
    const bars = Array.from(container.querySelectorAll("svg path[fill^='var(--blue-']"));
    expect(bars.map((p) => p.getAttribute("fill"))).toEqual(["var(--blue-600)", "var(--blue-350)"]);
  });

  it("draws each bar as a square-based, 4px-rounded path at the plot origin", () => {
    const container = chart(TWO_BUCKETS);
    const bars = Array.from(container.querySelectorAll("svg path[fill^='var(--blue-']"));
    const lock = bars[0]!;
    // width 720 − 168 label column − 48 value column = 504 plot; the largest
    // bucket fills it, so the path's straight run ends at 504 − 4.
    expect(lock.getAttribute("transform")).toBe("translate(168 0)");
    expect(lock.getAttribute("d")).toMatch(/^M 0 0 H 500 A 4 4 0 0 1 504 4 V 12 A 4 4 0 0 1 500 16 H 0 Z$/);
    // The smaller bucket is half as wide: 252.
    expect(bars[1]!.getAttribute("d")).toMatch(/^M 0 0 H 248 A 4 4 0 0 1 252 4/);
  });

  it("draws the axis with a baseline rule and gridlines in the grid token", () => {
    const container = chart(TWO_BUCKETS);
    const lines = Array.from(container.querySelectorAll("svg line"));
    expect(lines).toHaveLength(5); // 0/25/50/75/100 % of max
    expect(lines[0]!.getAttribute("stroke")).toBe("var(--baseline)");
    for (const line of lines.slice(1)) {
      expect(line.getAttribute("stroke")).toBe("var(--gridline)");
    }
  });

  it("labels every bar with its count in the mark-label class, never in the bar's colour", () => {
    const container = chart(TWO_BUCKETS);
    const labels = Array.from(container.querySelectorAll("svg text.mark-label"));
    expect(labels.map((t) => t.textContent)).toEqual(["2", "1"]);
    for (const label of labels) {
      expect(label.getAttribute("fill")).toBe("var(--text-secondary)");
    }
  });

  it("names the buckets in the bucket column in the primary ink", () => {
    const container = chart(TWO_BUCKETS);
    const names = Array.from(
      container.querySelectorAll("svg text:not(.spark-axis-label):not(.mark-label)"),
    );
    expect(names.map((t) => t.textContent)).toEqual(["Lock", "Client"]);
    expect(names.every((t) => t.getAttribute("fill") === "var(--text-primary)")).toBe(true);
  });

  it("marks the selected bucket with an outline and aria-current, not a fill change", () => {
    // The hit target exists only in interactive mode (`onSelect` set) — how the
    // page renders it, and the only element that can carry `aria-current`.
    const container = chart(TWO_BUCKETS, { selectedKey: "Lock", onSelect: () => {} });
    const hit = container.querySelector('rect[role="button"][aria-current="true"]');
    expect(hit).not.toBeNull();
    expect(hit!.getAttribute("aria-label")).toBe("Lock: 2 of 3 sessions");
    const outline = container.querySelector("svg rect[stroke='var(--text-secondary)']");
    expect(outline).not.toBeNull();
    // The bar itself is untouched: selection is not a colour, per R10.23.
    expect(container.querySelectorAll("svg path[fill='var(--blue-600)']")).toHaveLength(1);
  });

  it("calls onSelect when a bar's hit target is clicked", () => {
    const onSelect = vi.fn();
    const container = chart(TWO_BUCKETS, { onSelect });
    const hits = Array.from(container.querySelectorAll('rect[role="button"]'));
    expect(hits).toHaveLength(2);
    fireEvent.click(hits[1]!);
    expect(onSelect).toHaveBeenCalledWith("Client");
  });

  it("shows the tooltip on hover with the value before the label", () => {
    // Hover lives on the hit target too, so this needs the interactive render.
    const container = chart(TWO_BUCKETS, { onSelect: () => {} });
    const hit = container.querySelector('rect[role="button"]')!;
    expect(container.querySelector(".viz-tooltip")).toBeNull();
    fireEvent.mouseEnter(hit);
    const tip = container.querySelector(".viz-tooltip");
    expect(tip).not.toBeNull();
    const [value, label] = Array.from(tip!.children);
    expect(value!.className).toBe("tt-value");
    expect(value!.textContent).toBe("2");
    expect(label!.className).toBe("tt-label");
    expect(label!.textContent).toBe("Lock");
    fireEvent.mouseLeave(hit);
    expect(container.querySelector(".viz-tooltip")).toBeNull();
  });

  it("renders the table twin and no legend box", () => {
    const container = chart(TWO_BUCKETS);
    // One series → no legend (the skill's rule); the twin is the styled data
    // table, and it is the only table the chart renders.
    expect(container.querySelectorAll("table")).toHaveLength(1);
    expect(container.querySelector("table.data")).not.toBeNull();
    const rows = Array.from(container.querySelectorAll("table.data tbody tr"));
    expect(rows).toHaveLength(2);
    const cells = Array.from(rows[0]!.querySelectorAll("th,td")).map((c) => c.textContent);
    expect(cells).toEqual(["Lock", "2", "67%"]);
  });

  it("says so in one line when there is nothing to draw", () => {
    const container = chart([]);
    expect(container.querySelector(".chart-empty")).not.toBeNull();
    expect(container.querySelector("svg")).toBeNull();
  });
});

/* ── the text equivalent ───────────────────────────────────────────────────── */

describe("describeWaitBuckets", () => {
  it("names the total and every bucket", () => {
    expect(describeWaitBuckets(waitBuckets([LOCK, CLIENT, IO]))).toBe(
      "3 sessions across 3 wait-event buckets: Client 1, IO 1, Lock 1.",
    );
  });

  it("answers for an empty set", () => {
    expect(describeWaitBuckets([])).toBe("No wait events.");
  });
});
