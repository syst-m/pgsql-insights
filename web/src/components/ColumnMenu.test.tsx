/* R10.20 — the column-visibility menu.
 *
 * R10.20 makes three demands and the file has to meet all three, not just the
 * visible one: a menu, **a documented default column set**, and **a persisted
 * per-view selection**. The default set is testable as a fact (the table's
 * declared columns and the documented default are the same list), and the
 * persistence is testable as a round-trip. Both are here, because a menu that
 * forgets the default set still looks finished.
 *
 * Two environment notes, both learned the hard way:
 *
 * - `fireEvent`, not `user-event`: the repo has no @testing-library/user-event
 *   dependency (package.json is A4's and adding one is an ask), and
 *   ThemeToggle.test.tsx sets the house pattern.
 * - Storage is installed per test rather than cleared. `globalThis.localStorage`
 *   in a Vitest run is Node's experimental global: present, but with no
 *   `getItem`/`setItem`/`clear` on it unless `--localstorage-file` is set
 *   (ThemeToggle.tsx:53 documents exactly this, and `browserStorage()` is the
 *   accessor the component now uses). Calling `.clear()` on it throws, and
 *   asserting a write through it proves nothing — the component's try/catch
 *   swallows the error and the test passes without a write having happened. So
 *   the tests mount a real in-memory stub, which is what lets the write be
 *   *asserted*.
 */

import { fireEvent, render, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { COLUMNS } from "./ClusterTable";
import {
  ColumnMenu,
  DEFAULTS,
  normalizeSelection,
  storageKey,
  useColumnVisibility,
  type ColumnDef,
} from "./ColumnMenu";

const COLUMNS_3: readonly ColumnDef[] = [
  { key: "a", label: "A" },
  { key: "b", label: "B", required: true },
  { key: "c", label: "C" },
];

/** The same three columns with no required flag, for the tests where the
 *  required-column rule would mask the thing being asserted. */
const COLUMNS_PLAIN: readonly ColumnDef[] = [
  { key: "a", label: "A" },
  { key: "b", label: "B" },
  { key: "c", label: "C" },
];

/** The Queries column set as ColumnDefs, built from the documented default so
 *  the per-view test exercises the real DEFAULTS entry rather than a fixture. */
const QUERY_COLUMNS: readonly ColumnDef[] = (DEFAULTS.queries ?? []).map((k) => ({ key: k, label: k }));

interface StorageStub {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
}

function memoryStorage(initial: Record<string, string> = {}) {
  const map = new Map(Object.entries(initial));
  return {
    map,
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => {
      map.set(k, v);
    },
  } satisfies StorageStub & { map: Map<string, string> };
}

const globalRef = globalThis as unknown as { localStorage: unknown };
const ambient = globalRef.localStorage;
afterEach(() => {
  globalRef.localStorage = ambient;
});

function Harness({
  viewId,
  columns,
  defaultKeys,
}: {
  viewId: string;
  columns: readonly ColumnDef[];
  defaultKeys?: readonly string[];
}) {
  const v = useColumnVisibility({ viewId, columns, defaultKeys });
  return <ColumnMenu columns={columns} visible={v.visible} onChange={v.setVisible} />;
}

/** Mount the menu wired to the hook, with `initial` as the pre-existing storage
 *  contents. Returns the render result *plus the storage it read from*, so a
 *  test can assert the write instead of trusting the component. */
function probe(
  viewId: string,
  columns: readonly ColumnDef[] = COLUMNS_3,
  initial: Record<string, string> = {},
  defaultKeys: readonly string[] = ["a", "b"],
) {
  const storage = memoryStorage(initial);
  globalRef.localStorage = storage;
  const rendered = render(<Harness viewId={viewId} columns={columns} defaultKeys={defaultKeys} />);
  return { ...rendered, storage };
}

describe("the documented default set (R10.20)", () => {
  it("Fleet's default is exactly ClusterTable's column set, in its order", () => {
    // The two lists are one fact. If a column is added to the table and not to
    // the documented default, the menu's count goes wrong and this fails.
    expect(DEFAULTS.fleet).toEqual(COLUMNS.map((c) => c.key));
  });

  it("Queries' default is the R3.2 set minus the two columns R3.2 marks as having no per-query source", () => {
    // requirements.md:306 — `Load by lock wait` renders the n/a literal and
    // `Analyze latency` is a deferred Gap, so neither is on by default.
    expect(DEFAULTS.queries).toEqual([
      "query",
      "database",
      "loadByTotalTime",
      "loadByQueriesPct",
      "avgExecTimeMs",
      "totalExecTimeMs",
      "avgRowsReturned",
      "timesCalled",
      "recommendations",
    ]);
  });
});

describe("normalizeSelection", () => {
  it("drops a stored key for a column that no longer exists", () => {
    // A renamed column would otherwise keep the old key selected and render
    // nothing for it — a silently wrong table, not an error.
    expect(normalizeSelection(["a", "b", "gone"], COLUMNS_3, ["a", "b"])).toEqual(["a", "b"]);
  });

  it("re-adds a required column a stored selection omitted", () => {
    expect(normalizeSelection(["a"], COLUMNS_3, ["a", "b", "c"])).toEqual(["a", "b"]);
  });

  it("returns columns in the table's order, not the click history's", () => {
    expect(normalizeSelection(["c", "a"], COLUMNS_3, ["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  it("falls back to the default when there is nothing stored", () => {
    expect(normalizeSelection(null, COLUMNS_3, ["a", "b"])).toEqual(["a", "b"]);
  });
});

describe("persistence (R10.20's per-view selection)", () => {
  it("writes the per-view key on change and reads it back on next mount", () => {
    const { getByRole, getByLabelText, storage } = probe("view-x");
    fireEvent.click(getByRole("button", { name: /Columns/ }));
    fireEvent.click(getByLabelText("C"));

    expect(storage.map.get(storageKey("view-x"))).toBe(JSON.stringify(["a", "b", "c"]));
  });

  it("reads a pre-existing selection back on mount", () => {
    // COLUMNS_PLAIN, not COLUMNS_3: with `b` required, a stored ["a"] normalizes
    // back to ["a","b"] and the count would read 2/3 for the wrong reason.
    const { getByRole } = probe("view-pre", COLUMNS_PLAIN, {
      [storageKey("view-pre")]: JSON.stringify(["a"]),
    });
    expect(getByRole("button", { name: /Columns/ }).textContent).toBe("Columns1/3");
  });

  it("keys the selection by view, so Queries' choice never overwrites Fleet's", () => {
    // Both views render from one storage object; each count has to come from its
    // own key. This is also the test that the hook *reads* DEFAULTS rather than
    // merely exporting it.
    globalRef.localStorage = memoryStorage({
      [storageKey("fleet")]: JSON.stringify(["health", "name"]),
      [storageKey("queries")]: JSON.stringify(["query", "database"]),
    });
    // `within(...)`: render's queries bind to document.body, so with two
    // renders in one test the bare `getByRole` matches both menus.
    const fleet = render(<Harness viewId="fleet" columns={COLUMNS} />);
    const queries = render(<Harness viewId="queries" columns={QUERY_COLUMNS} />);
    expect(within(fleet.container).getByRole("button", { name: /Columns/ }).textContent).toBe(
      `Columns2/${COLUMNS.length}`,
    );
    expect(within(queries.container).getByRole("button", { name: /Columns/ }).textContent).toBe(
      `Columns2/${QUERY_COLUMNS.length}`,
    );
  });

  it("uses the default set when the stored value is corrupt", () => {
    const { getByRole } = probe("view-y", COLUMNS_3, { [storageKey("view-y")]: "{not json" });
    expect(getByRole("button", { name: /Columns/ }).textContent).toContain("2/3");
  });

  it("keeps working when localStorage throws on write", () => {
    // Safari private mode throws on setItem. A preferences layer must not be
    // able to blank a page.
    globalRef.localStorage = {
      getItem: () => null,
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
    };
    const { getByRole } = render(<Harness viewId="view-z" columns={COLUMNS_3} defaultKeys={["a", "b"]} />);
    const button = getByRole("button", { name: /Columns/ });
    expect(button.textContent).toBe("Columns2/3");
    // The throw happens on the write, i.e. on click — not just at mount.
    expect(() => {
      fireEvent.click(button);
      fireEvent.click(document.querySelector('input[type="checkbox"]') as Element);
    }).not.toThrow();
  });

  it("renders the defaults when there is no storage at all", () => {
    // The Node/vitest state: a `localStorage` global with no methods on it.
    globalRef.localStorage = {};
    const { getByRole } = render(<Harness viewId="view-none" columns={COLUMNS_3} defaultKeys={["a", "b"]} />);
    expect(getByRole("button", { name: /Columns/ }).textContent).toBe("Columns2/3");
  });
});

describe("the menu", () => {
  it("is a button that opens a dialog and closes on Escape", () => {
    const { getByRole } = probe("view-m");
    const button = getByRole("button", { name: /Columns/ });
    expect(button.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(getByRole("dialog", { name: "Column visibility" })).toBeTruthy();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(button.getAttribute("aria-expanded")).toBe("false");
  });

  it("shows a required column checked and disabled, not hidden", () => {
    const { getByRole, getByLabelText } = probe("view-r");
    fireEvent.click(getByRole("button", { name: /Columns/ }));
    const required = getByLabelText("B") as HTMLInputElement;
    expect(required.checked).toBe(true);
    expect(required.disabled).toBe(true);
  });

  it("lists every column, including the ones currently hidden", () => {
    // A menu that only lists visible columns can hide a column but never bring
    // it back.
    const { getByRole, getAllByRole } = probe("view-list", COLUMNS_3, {
      [storageKey("view-list")]: JSON.stringify(["a"]),
    });
    fireEvent.click(getByRole("button", { name: /Columns/ }));
    expect(getAllByRole("checkbox")).toHaveLength(3);
  });
});
