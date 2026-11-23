/* R10.20 — the column-visibility menu.
 *
 * requirements.md:430 (R10.20) requires "**Column-visibility menu** on every
 * wide table, with a documented default column set and a persisted per-view
 * selection." Three nouns, three pieces of this file:
 *
 *   - **menu**            → `ColumnMenu`, the popover of checkboxes.
 *   - **documented default column set** → `DEFAULTS` here, one named set per
 *         view. "Documented" is why the defaults are a declared table and not a
 *         filter inside the component: a reader has to be able to find the
 *         default set for a given table by reading one place.
 *   - **persisted per-view selection** → `useColumnVisibility(viewId, …)`.
 *         *Per-view* is load-bearing: a global column preference would mean
 *         hiding `Disk %` on Fleet also hides it on Queries, where it is one of
 *         the R3.2 columns. So the storage key carries the view id.
 *
 * Why this is a component and not a detail of each table: R10.20 says *every
 * wide table*. `ClusterTable.tsx` is wide (10 columns) and has no menu today,
 * and the Queries table is wider (the R3.2 set is 9 columns plus the load
 * family). Building it once here is what makes "every" achievable.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { browserStorage } from "./ThemeToggle";

export interface ColumnDef {
  key: string;
  label: string;
  /** A column the view cannot render without. It still appears in the menu —
   *  checked and disabled — because hiding a required column makes the menu
   *  lie about what the table has. */
  required?: boolean;
}

/** The documented default column sets (R10.20's "documented default column
 *  set"), keyed by view id.
 *
 *  Fleet's set is `ClusterTable.tsx`'s current `COLUMNS` order, so adopting the
 *  menu changes nothing for a table that accepts the default. Queries' set is
 *  the R3.2 column set (requirements.md:306) in R3.2's order and with R3.2's
 *  names — all thirteen, including the two R3.2 itself marks as having no
 *  per-query value.
 *
 *  Those two are on by default *because* §9's 6b acceptance names them: "Queries
 *  page renders the R3.2 column set exactly (incl. `Load by lock wait` =
 *  `n/a`)". A default that hid them would render a nine-column table and pass
 *  the eye while failing the sentence. `Load by lock wait` renders the
 *  `n/a (no per-query source, §5)` literal and `Analyze latency` the deferred
 *  **Gap** marker (both in LoadBars.tsx); they are columns with no sort, not
 *  columns with no cell.
 */
export const DEFAULTS: Record<string, readonly string[]> = {
  fleet: [
    "health",
    "name",
    "namespace",
    "instances",
    "pgMajor",
    "tps",
    "p95LatencyMs",
    "connectionsPct",
    "diskPct",
    "observedAt",
  ],
  queries: [
    "query",
    "database",
    "recommendations",
    "loadByTotalTime",
    "pctLoadByQueries",
    "avgExecTimeMs",
    "totalExecTimeMs",
    "avgRowsReturned",
    "timesCalled",
    "loadByCPUMs",
    "loadByIOWaitMs",
    "loadByLockWait",
    "analyzeLatency",
  ],
};

/** Storage key for one view's selection. Exported so a test can write a
 *  pre-existing selection without guessing the format. */
export function storageKey(viewId: string): string {
  return `pgsi:columns:${viewId}`;
}

/** Read + validate a persisted selection against the columns that exist now.
 *
 *  Two failure modes this closes, both of which otherwise produce a silently
 *  wrong table rather than an error:
 *
 *  - **A stored key for a column that no longer exists.** A renamed column
 *    would otherwise keep the old key in the selection and render nothing for
 *    it, or drop the new one. Unknown keys are filtered out.
 *  - **A stored selection that omits a required column.** Re-added, because a
 *    selection that cannot render the table is not a valid selection.
 *
 *  A throw from `localStorage` (Safari private mode, storage disabled) is
 *  caught: the default set is the correct answer when storage is unavailable,
 *  and a preferences layer must not be able to blank a page. The accessor is
 *  `browserStorage()` from ThemeToggle.tsx:53 — the repo's one place that
 *  knows a `localStorage` global can exist *without* being usable (Node ships
 *  a method-less one unless `--localstorage-file` is set, which is what a
 *  Vitest jsdom run inherits). Re-implementing the check per component is how
 *  one of them eventually gets it wrong.
 */
export function normalizeSelection(
  stored: readonly string[] | null,
  columns: readonly ColumnDef[],
  fallback: readonly string[],
): string[] {
  const known = new Set(columns.map((c) => c.key));
  const source = stored ?? fallback;
  const picked = source.filter((k) => known.has(k));
  for (const col of columns) {
    if (col.required && !picked.includes(col.key)) picked.push(col.key);
  }
  // Preserve the column order rather than the stored order, so the table's
  // column order stays a property of the table and not of click history.
  return columns.filter((c) => picked.includes(c.key)).map((c) => c.key);
}

export interface UseColumnVisibilityArgs {
  viewId: string;
  columns: readonly ColumnDef[];
  /** Defaults to `DEFAULTS[viewId]`; empty when the view has no documented set. */
  defaultKeys?: readonly string[];
}

export interface ColumnVisibility {
  visible: string[];
  setVisible: (keys: readonly string[]) => void;
  reset: () => void;
  /** The documented default, for the menu's "Reset" affordance and for tests
   *  that assert the default set is the documented one. */
  defaultKeys: readonly string[];
}

export function useColumnVisibility({
  viewId,
  columns,
  defaultKeys,
}: UseColumnVisibilityArgs): ColumnVisibility {
  const declared = useMemo(
    () => defaultKeys ?? DEFAULTS[viewId] ?? columns.map((c) => c.key),
    [defaultKeys, viewId, columns],
  );

  const [visible, setVisible] = useState<string[]>(() => {
    let stored: string[] | null = null;
    try {
      const raw = browserStorage()?.getItem(storageKey(viewId)) ?? null;
      const parsed: unknown = raw ? JSON.parse(raw) : null;
      if (Array.isArray(parsed)) {
        stored = parsed.filter((k): k is string => typeof k === "string");
      }
    } catch {
      stored = null;
    }
    return normalizeSelection(stored, columns, declared);
  });

  const persist = useCallback(
    (keys: readonly string[]) => {
      const next = normalizeSelection([...keys], columns, declared);
      setVisible(next);
      try {
        browserStorage()?.setItem(storageKey(viewId), JSON.stringify(next));
      } catch {
        // Storage unavailable: the selection still applies for this render, it
        // just won't survive a reload. That is R10.20 degraded, not broken.
      }
    },
    [columns, declared, viewId],
  );

  return {
    visible,
    setVisible: persist,
    reset: () => persist(declared),
    defaultKeys: declared,
  };
}

export interface ColumnMenuProps {
  columns: readonly ColumnDef[];
  visible: readonly string[];
  onChange: (keys: readonly string[]) => void;
  /** Accessible name; defaults to "Column visibility". */
  label?: string;
}

/** The menu itself: a button plus a popover of checkboxes.
 *
 *  Keyboard contract (R10.13's "keyboard navigation", and the reason the
 *  popover is a `dialog` and not a hover menu): the button is a real
 *  `<button>`, `Enter`/`Space` open it, `Escape` closes it and returns focus to
 *  the button, and while open the checkboxes are ordinary tabbable inputs. A
 *  hover-only menu would fail that, and a menu you cannot reach with the keyboard
 *  is how R10.20 becomes a requirement the UI doesn't actually meet.
 */
export function ColumnMenu({ columns, visible, onChange, label = "Column visibility" }: ColumnMenuProps) {
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);

  const close = useCallback(() => {
    setOpen(false);
    buttonRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        close();
      }
    };
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("mousedown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("mousedown", onDown);
    };
  }, [open, close]);

  const set = (key: string, on: boolean) => {
    const next = on
      ? [...visible, key]
      : visible.filter((k) => k !== key);
    onChange(columns.filter((c) => c.required || next.includes(c.key)).map((c) => c.key));
  };

  return (
    <div className="column-menu" ref={rootRef} data-open={open || undefined}>
      <button
        type="button"
        className="btn"
        ref={buttonRef}
        aria-haspopup="true"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        Columns
        <span className="column-menu-count secondary">{visible.length}/{columns.length}</span>
      </button>
      {open && (
        <div className="column-menu-pop" role="dialog" aria-label={label}>
          <ul>
            {columns.map((col) => (
              <li key={col.key}>
                <label>
                  <input
                    type="checkbox"
                    checked={visible.includes(col.key) || col.required === true}
                    disabled={col.required === true}
                    onChange={(e) => set(col.key, e.target.checked)}
                  />
                  <span>{col.label}</span>
                </label>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
