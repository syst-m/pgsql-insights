/* R10.18 — virtualization and density, as one primitive.
 *
 * `requirements.md:430` (R10.18) requires "**Table virtualization + density
 * toggle** (compact / comfortable) on every table that can exceed 100 rows, so
 * R10.14's budget survives a 500-row page."
 *
 * Two things follow from the wording that decide the shape of this file:
 *
 * 1. **"every table"**, not "the big tables". R8.10 (`requirements.md:387`) caps
 *    a page at `?limit=` 500, so every list endpoint can return more than 100
 *    rows and therefore every table in the app needs this. That is why it is a
 *    primitive rather than a detail of the Queries table — and it is the reason
 *    `ClusterTable.tsx` is the first adopter after the Queries page: it renders
 *    all N rows today and has no virtualization at all.
 * 2. The requirement names **two** features and they are not the same feature.
 *    Virtualization is a rendering strategy; density is a row-height setting.
 *    Coupling them (a `VirtualList` that owns density) means a non-virtualized
 *    surface can't have a density toggle. So `Density` and `ROW_HEIGHT` are
 *    exported separately from the windowing math, and `DensityToggle` is a
 *    control, not a wrapper.
 *
 * The windowing math is exported as a hook (`useVirtualWindow`) as well as a
 * component, because a `<table>` cannot be windowed the way a `<div>` list can:
 * the rows have to stay inside `<tbody>`, so the caller needs the indices and
 * the two spacer heights rather than a rendered container. `QueryTable` uses
 * the hook and renders its own `<table>`; `VirtualList` is the div renderer for
 * lists that aren't tables. One implementation of the math, two render paths.
 */

import { useCallback, useMemo, useState, type ReactNode } from "react";

/* ── density (the second half of R10.18) ─────────────────────────────────── */

export type Density = "compact" | "comfortable";

/** Row height per density, in px.
 *
 *  Comfortable 36 is `table.data`'s current rendered height (13px text at
 *  line-height 1.45 plus 8px vertical padding, twice) — so adopting this
 *  changes nothing visually for a table that opts in at the default.
 *  Compact 24 is the same content at 4px padding. Both are above the 24px
 *  minimum hover-target floor in the dataviz `interaction.md` reference, which
 *  matters because a table row is a hover target (R10.19's tooltip rides it).
 */
export const ROW_HEIGHT: Record<Density, number> = {
  compact: 24,
  comfortable: 36,
};

export const DENSITY_ORDER: readonly Density[] = ["compact", "comfortable"];

/** The segmented control R10.18 requires. Reuses the shell's `.segmented`
 *  styling (`web/src/styles/base.css:310`) rather than defining a second one. */
export function DensityToggle({
  density,
  onChange,
}: {
  density: Density;
  onChange: (d: Density) => void;
}) {
  return (
    <div className="segmented" role="group" aria-label="Row density">
      {DENSITY_ORDER.map((d) => (
        <button
          key={d}
          type="button"
          aria-pressed={d === density}
          onClick={() => onChange(d)}
        >
          {d === "compact" ? "Compact" : "Comfortable"}
        </button>
      ))}
    </div>
  );
}

/* ── windowing math ──────────────────────────────────────────────────────── */

export interface Window {
  /** First rendered index. */
  start: number;
  /** One past the last rendered index. */
  end: number;
  /** Height in px to leave above the rendered slice. */
  padTop: number;
  /** Height in px to leave below it. */
  padBottom: number;
  /** Total content height, `count * rowHeight`. */
  totalHeight: number;
}

/** The window of rows to render for a scroll position, and nothing else.
 *
 *  Pure and exported on purpose: the whole of R10.18's correctness is "the
 *  rendered slice is a window, and the spacers keep the scrollbar the right
 *  length", and both are testable as arithmetic without a DOM.
 *
 *  `overscan` renders rows past both edges so a wheel-scroll doesn't show a
 *  blank band between frames. It is not free — it is the only thing that makes
 *  the rendered count bigger than the visible count — so it stays small.
 */
export function windowFor(
  count: number,
  rowHeight: number,
  viewportHeight: number,
  scrollTop: number,
  overscan: number,
): Window {
  const safeRow = rowHeight > 0 ? rowHeight : 1;
  const totalHeight = Math.max(0, count * safeRow);

  if (count === 0) {
    return { start: 0, end: 0, padTop: 0, padBottom: 0, totalHeight: 0 };
  }

  // A viewport of 0 (jsdom reports offsetHeight 0 for an unstyled div, and a
  // closed popover is 0 too) has zero visible rows. Rendering zero rows there
  // would read as an empty table on the first frame before layout settles, so
  // the window becomes exactly the overscan band — 2 * overscan rows — which is
  // also what it is at any other scroll position.
  const visible = viewportHeight > 0 ? Math.ceil(viewportHeight / safeRow) : 0;
  const maxStart = Math.max(0, count - 1);
  const start = Math.min(maxStart, Math.max(0, Math.floor(Math.max(0, scrollTop) / safeRow) - overscan));
  const end = Math.min(count, start + visible + overscan * 2);

  return {
    start,
    end,
    padTop: start * safeRow,
    padBottom: Math.max(0, (count - end) * safeRow),
    totalHeight,
  };
}

export interface UseVirtualWindowArgs {
  count: number;
  rowHeight: number;
  /** The scroll container's client height. 0 before layout; handled. */
  viewportHeight: number;
  scrollTop: number;
  overscan?: number;
}

export function useVirtualWindow({
  count,
  rowHeight,
  viewportHeight,
  scrollTop,
  overscan = 6,
}: UseVirtualWindowArgs): Window {
  return useMemo(
    () => windowFor(count, rowHeight, viewportHeight, scrollTop, overscan),
    [count, rowHeight, viewportHeight, scrollTop, overscan],
  );
}

/* ── the div renderer ────────────────────────────────────────────────────── */

export interface VirtualListProps<T> {
  items: readonly T[];
  rowHeight: number;
  /** Height of the scroll viewport in px. A number rather than a measured
   *  value: the app's tables live in fixed-height cards, and measuring adds a
   *  ResizeObserver whose first frame is 0 — which is the empty-table flash
   *  R10.22's skeleton exists to prevent. */
  height: number;
  renderItem: (item: T, index: number) => ReactNode;
  keyFor: (item: T, index: number) => string;
  overscan?: number;
  className?: string;
}

/** Windowed list for non-table content.
 *
 *  For a table, use `useVirtualWindow` inside the caller's own `<table>` —
 *  windowing a table means spacer rows in `<tbody>`, not a div wrapper, and a
 *  generic component cannot know the caller's column count to build them.
 */
export function VirtualList<T>({
  items,
  rowHeight,
  height,
  renderItem,
  keyFor,
  overscan = 6,
  className,
}: VirtualListProps<T>) {
  const [scrollTop, setScrollTop] = useState(0);
  const onScroll = useCallback((e: { currentTarget: EventTarget | null }) => {
    const target = e.currentTarget as { scrollTop?: number } | null;
    if (target && typeof target.scrollTop === "number") setScrollTop(target.scrollTop);
  }, []);

  const win = useVirtualWindow({ count: items.length, rowHeight, viewportHeight: height, scrollTop, overscan });
  const slice = items.slice(win.start, win.end);

  return (
    <div
      className={["virtual-list", className].filter(Boolean).join(" ")}
      style={{ height }}
      onScroll={onScroll}
      data-testid="virtual-list"
      data-count={items.length}
    >
      <div className="virtual-inner" style={{ height: win.totalHeight }}>
        <div className="virtual-window" style={{ transform: `translateY(${win.padTop}px)` }}>
          {slice.map((item, i) => (
            <div className="virtual-row" key={keyFor(item, win.start + i)} style={{ height: rowHeight }}>
              {renderItem(item, win.start + i)}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
