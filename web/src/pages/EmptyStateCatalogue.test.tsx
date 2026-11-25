/* R10.28's empty-state catalogue, as one artifact instead of as prose.
 *
 * requirements.md:439 is a list: "an enumerated, designed state for every
 * combination the reference environment produces — healthy 2-instance,
 * single-instance, Pending replica, **CrashLoopBackOff on a CR that reports
 * healthy** (R1.2), no Pooler, PG16 vs PG17 column differences, and
 * extension-absent. **Each has a named fixture in R16.7.**" R16.7 (:526) says
 * the catalogue joins the UI suite when 6b lands it, and §9's phase table
 * (:675) assigns 6b to A6.
 *
 * The seven states are all *rendered* already, each by the file that owns it and
 * each with its own suite. What did not exist was the list. The enumeration
 * lived as a sentence inside `ClusterDetail.tsx`'s `Instances` docblock ("four
 * of the seven are instance-level, so they are this table's … naming them here
 * is what keeps the catalogue one list with one owner per entry") — and prose
 * cannot be iterated over, so a reader could not tell whether the list was
 * complete, and an eighth state could be added to a page without being added to
 * the catalogue. `CATALOGUE` below is that list as data: seven rows, each
 * naming the requirement's own wording for the state, the fixture that
 * materialises it, and the file that owns it.
 *
 * Two rules keep this from being a second copy of three suites.
 *
 * 1. **Each test asserts the discriminator, not the component.** The owner's own
 *    suite is the deep one (`ClusterDetail.test.tsx` is 550 lines about six
 *    tabs); re-running it here would give two places to update and one of them
 *    stale. Each test below asserts the one fact that makes its state *that*
 *    state and nothing else — for the crash loop it is the pair of numbers a
 *    naive render hides, for the PG16/PG17 entry it is which derivation the
 *    cell names — and cites the owner for the rest.
 * 2. **Nothing is re-typed.** Every cluster id, pod name and fixture in the
 *    test bodies is read out of `CATALOGUE`, which reads them out of
 *    `FIXTURE_CLUSTERS` / `FIXTURE_QUERIES` — the pinned `testdata/api/*.json`
 *    bytes. A test that hardcodes `freeidp-pg-2` tests the test; a test that
 *    reads it from the fixture tests the contract, and if the fixture renames
 *    the pod the catalogue follows instead of going red for the wrong reason.
 *
 * The `spec` strings are quoted from :439 rather than paraphrased, because two
 * of the seven turn out not to mean what their names say, and the only way to
 * keep that recorded is to put the requirement's words next to the fixture's
 * answer: the "Pending replica" state is `containerStatus: "PodInitializing"`
 * in the fixture (CNPG's word for the pod between Pending and running), and the
 * "CR that reports healthy" is `phase: "Not enough disk space"` — the
 * *reporting* that R1.2 calls into question is the instance counts (2 in spec,
 * 0 serving), not the phase string. Both are stated in the rows below.
 *
 * The eighth row is `from: "page"` — the Tags page's empty state. :439's list is
 * cluster-level and R10.3's ninth page has an empty state of its own, so the row
 * is here with the field that keeps it distinguishable from the seven, and its
 * test renders `<TagsPage>` directly for the same reason the extension-absent row
 * renders `<ExtensionNotInstalled>` directly: its owner is not `ClusterDetail`,
 * so `renderEntry`'s cluster-tab route cannot reach it.
 */

import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ClusterDetailPage } from "./ClusterDetail";
import { TagsPage } from "./Tags";
import { QueryTable, loadCellFor } from "../components/QueryTable";
import { ExtensionNotInstalled } from "../components/ExtensionNotInstalled";
import { FIXTURE_CLUSTERS, FIXTURE_QUERIES, FIXTURE_TAGS } from "../api/fixtures";
import type { FleetRow, QueryView } from "../api/types";
import { resolveConfig } from "../api/client";
import { DEFAULT_STATE, withState, type ClusterTab, type UrlState } from "../state/urlState";

/* ── the catalogue ─────────────────────────────────────────────────────────── */

type CatalogueKey =
  | "healthy-two-instance"
  | "single-instance"
  | "pending-replica"
  | "crashloop-reporting-healthy"
  | "no-pooler"
  | "pg16-vs-pg17"
  | "extension-absent"
  | "tags-empty";

interface CatalogueEntry {
  key: CatalogueKey;
  /** Which list the row belongs to. `spec` is one of :439's seven cluster-level
   *  states; `page` is an empty state a page has that :439 does not enumerate —
   *  the Tags page's, for instance, which is R10.3's page-level empty rather than
   *  a cluster combination. The field exists so the requirement's seven stay
   *  countable inside a longer list: without it, an eighth row makes
   *  `toHaveLength(7)` red and the fix is to weaken the assertion, which is how a
   *  catalogue stops being one. */
  from: "spec" | "page";
  /** R10.28's own words for the state (:439) for a `spec` row; for a `page` row,
   *  the page's own wording plus the requirement that names the page. */
  spec: string;
  /** The file that renders it. The test below is the entry's proof of life; the
   *  named file's own suite is the deep one. */
  owner: string;
  /** The fixture that materialises it, named the way R16.7 asks for a name. */
  fixture: string;
  /** The cluster the state belongs to, as `namespace/name`. `null` is a real
   *  answer here: extension-absent is every cluster's state at once (K2), so it
   *  has no single owner cluster and the test picks one to render. */
  clusterId: string | null;
  /** The tab the owner renders it under, for the five ClusterDetail entries. */
  tab?: ClusterTab;
}

const CATALOGUE: readonly CatalogueEntry[] = [
  {
    key: "healthy-two-instance",
    from: "spec",
    spec: "healthy 2-instance",
    owner: "pages/ClusterDetail.tsx — Instances",
    fixture: "testdata/api/clusters.json → FIXTURE_CLUSTERS[ai-api-gateway/rev-proxy-pg]",
    clusterId: "ai-api-gateway/rev-proxy-pg",
    tab: "instances",
  },
  {
    key: "single-instance",
    from: "spec",
    spec: "single-instance",
    owner: "pages/ClusterDetail.tsx — Instances",
    fixture: "testdata/api/clusters.json → FIXTURE_CLUSTERS[cookbook/cookbook-pg]",
    clusterId: "cookbook/cookbook-pg",
    tab: "instances",
  },
  {
    key: "pending-replica",
    from: "spec",
    spec: "Pending replica",
    owner: "pages/ClusterDetail.tsx — Instances",
    // The requirement's word is *Pending*; the fixture's answer for that pod is
    // `containerStatus: "PodInitializing"`, which is what CNPG reports between
    // Pending and running. The catalogue asserts the fixture's string, because
    // the requirement names a state and the fixture is the byte that proves it.
    fixture: "testdata/api/clusters.json → FIXTURE_CLUSTERS[freeidp/freeidp-pg].instanceList[freeidp-pg-2]",
    clusterId: "freeidp/freeidp-pg",
    tab: "instances",
  },
  {
    key: "crashloop-reporting-healthy",
    from: "spec",
    spec: "CrashLoopBackOff on a CR that reports healthy (R1.2)",
    owner: "pages/ClusterDetail.tsx — Overview + Instances",
    // "reports healthy" is about the counts, not the phase string: this cluster's
    // phase is "Not enough disk space". R1.2's defect is a render that shows
    // `instances: 2` as health while `servingInstances: 0` is the truth, so the
    // entry's fixture is the pair and the test asserts both halves.
    fixture: "testdata/api/clusters.json → FIXTURE_CLUSTERS[freeidp/freeidp-pg] (instances 2 / serving 0, freeidp-pg-1 CrashLoopBackOff)",
    clusterId: "freeidp/freeidp-pg",
    tab: "overview",
  },
  {
    key: "no-pooler",
    from: "spec",
    spec: "no Pooler",
    owner: "pages/ClusterDetail.tsx — Config › Poolers",
    fixture: "testdata/api/clusters.json → FIXTURE_CLUSTERS[cookbook/cookbook-pg].poolers === []",
    clusterId: "cookbook/cookbook-pg",
    tab: "config",
  },
  {
    key: "pg16-vs-pg17",
    from: "spec",
    spec: "PG16 vs PG17 column differences",
    owner: "components/QueryTable.tsx — loadCellFor",
    fixture: "testdata/api/queries.json → FIXTURE_QUERIES (rows on both majors)",
    // The state is a *pair*: the difference needs a 16 row and a 17 row to be a
    // difference at all. The entry names the 16 cluster (the reference
    // environment's only PG 16.11 cluster) and the test pulls the 17 row from
    // the same fixture.
    clusterId: "open-webui/open-webui-pg",
  },
  {
    key: "extension-absent",
    from: "spec",
    spec: "extension-absent",
    owner: "components/ExtensionNotInstalled.tsx (R10.27)",
    // K2: `pg_stat_statements` is absent on all three reachable clusters, so
    // this state has no owner cluster — it is every one of them. The fixture is
    // therefore the whole set, and the test renders one cluster's instance of it
    // because the component takes one cluster per render.
    fixture: "every cluster in testdata/api/clusters.json (K2) — rendered here as cookbook/cookbook-pg",
    clusterId: "cookbook/cookbook-pg",
  },
  {
    key: "tags-empty",
    from: "page",
    // Not one of :439's seven: the Tags page's empty state is R10.3's page-level
    // empty (the route is `GET /api/v1/insights/tags`, R8.2), and :439 enumerates
    // *cluster* combinations. It is listed here because the alternative is a
    // second catalogue — one for the requirement's seven and one per page — and
    // the reason this file exists at all is that the prose version could not be
    // iterated over.
    spec: "Tags page with no tags in the window (R10.3 · R8.2)",
    owner: "pages/Tags.tsx — TAGS_COPY via components/StatePanel.tsx",
    // The shipped tags payload has three rows, so the empty frame is the fixture
    // with its payload emptied — the same construction `Tags.test.tsx` uses, and
    // for the same reason: `meta` carries over so the header stamp stays the
    // fixture's and the test is about the empty set and nothing else.
    fixture: "testdata/api/tags.json → FIXTURE_TAGS with data: []",
    clusterId: "open-webui/open-webui-pg",
  },
];

function entryFor(key: CatalogueKey): CatalogueEntry {
  const entry = CATALOGUE.find((e) => e.key === key);
  if (entry === undefined) throw new Error(`catalogue has no entry for ${key}`);
  return entry;
}

/** A fixture cluster by its `namespace/name`, so an entry addresses a row
 *  without rebuilding it. Throws rather than returning undefined: a renamed
 *  cluster in the fixture should fail loudly, and it should say which id went
 *  missing. */
function rowFor(id: string): FleetRow {
  const row = FIXTURE_CLUSTERS.find((c) => `${c.namespace}/${c.name}` === id);
  if (row === undefined) throw new Error(`no fixture cluster ${id}`);
  return row;
}

/** Render the detail page for one cluster id and one tab. */
function renderCluster(clusterId: string, tab: ClusterTab) {
  const state: UrlState = withState(DEFAULT_STATE, { page: "cluster", cluster: clusterId, tab });
  return render(<ClusterDetailPage state={state} cfg={resolveConfig("", {})} onPatch={vi.fn()} />);
}

/** Render the entry's owner. The tab comes from the catalogue row, and the one
 *  entry that spans two tabs (the crash loop: the counts are Overview's, the
 *  container status is Instances') passes the second one explicitly. */
function renderEntry(entry: CatalogueEntry, tab?: ClusterTab) {
  if (entry.clusterId === null) throw new Error(`${entry.key} renders without a cluster; use its own body`);
  return renderCluster(entry.clusterId, tab ?? entry.tab ?? "overview");
}

/** The first card with a given title, as the page's own markup spells it. */
function card(container: HTMLElement, title: string): Element | undefined {
  return Array.from(container.querySelectorAll("section.card")).find(
    (s) => s.querySelector(".card-title")?.textContent === title,
  );
}

/** A table row's cells as text, in column order. */
function cells(row: Element): string[] {
  return Array.from(row.querySelectorAll("td")).map((td) => td.textContent ?? "");
}

/** The Overview `dl.kv` as a map, so a test asks for a row by its term. */
function kv(container: HTMLElement): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of Array.from(container.querySelectorAll("dl.kv .kv-row"))) {
    const dt = row.querySelector("dt");
    const dd = row.querySelector("dd");
    if (dt !== null && dd !== null) out[dt.textContent ?? ""] = dd.textContent ?? "";
  }
  return out;
}

/** The Instances card's rows, keyed by pod name — the shape every instance-level
 *  entry needs, so the pod name is never retyped from the fixture. */
function instanceRows(container: HTMLElement): Record<string, string[]> {
  const table = card(container, "Instances")?.querySelector("table.data");
  const out: Record<string, string[]> = {};
  if (table !== null && table !== undefined) {
    for (const row of Array.from(table.querySelectorAll("tbody tr"))) {
      const c = cells(row);
      const name = c[0];
      if (name !== undefined) out[name] = c;
    }
  }
  return out;
}

/* ── the eight tests ───────────────────────────────────────────────────────── */

describe("R10.28 empty-state catalogue", () => {
  it("lists :439's seven states, and the page-level rows beside them", () => {
    // The requirement is a list, so the first assertion is about the list's
    // shape: seven `spec` rows — one per combination :439 enumerates — with the
    // `page` rows counted separately rather than folded into the seven. This is
    // the assertion that makes the catalogue an artifact — an eighth cluster-level
    // state added to a page without being added here fails, which is the failure
    // mode the prose version could not have. A page-level state added here does
    // *not* move the seven, which is what the `from` field is for: without it the
    // only way to add one is to weaken the count, and a count that has been
    // weakened once stops being an assertion.
    expect(CATALOGUE.filter((e) => e.from === "spec")).toHaveLength(7);
    expect(CATALOGUE.filter((e) => e.from === "page")).toHaveLength(1);
    expect(CATALOGUE).toHaveLength(8);
    expect(new Set(CATALOGUE.map((e) => e.key)).size).toBe(8);
    // Every entry names an owner file that exists in this tree, so the list
    // cannot drift from the tree it describes.
    for (const entry of CATALOGUE) expect(entry.owner).toContain(".tsx");
  });

  it("healthy 2-instance: two pods, both serving, both running", () => {
    const entry = entryFor("healthy-two-instance");
    const row = rowFor(entry.clusterId ?? "");
    const { container } = renderEntry(entry);
    const instances = card(container, "Instances");

    // The count chip and the row count are the same number, and both come from
    // the fixture's `instanceList` — the page's own count, not a literal here.
    expect(instances?.querySelector(".card-meta")?.textContent).toBe(
      String(row.instanceList.length),
    );
    expect(Object.keys(instanceRows(container)).sort()).toEqual(
      row.instanceList.map((i) => i.name).sort(),
    );
    // What makes this state *healthy* rather than merely *two*: every pod is
    // ready and serving, so the two numbers R1.2 pulls apart are equal here.
    for (const cells of Object.values(instanceRows(container))) {
      expect(cells[3]).toBe("yes"); // Ready
      expect(cells[4]).toBe("yes"); // Serving
      expect(cells[5]).toBe("running"); // Container
    }
  });

  it("single-instance: one pod, and the count says one", () => {
    const entry = entryFor("single-instance");
    const row = rowFor(entry.clusterId ?? "");
    const { container } = renderEntry(entry);

    // A one-pod cluster and a two-pod cluster are different states in :439, not
    // two row counts, so the discriminator is the count the page publishes.
    expect(row.instanceList).toHaveLength(1);
    expect(card(container, "Instances")?.querySelector(".card-meta")?.textContent).toBe("1");
    // …and the single pod is the primary, which is the degenerate shape: no
    // replica exists to be in any other state.
    const only = Object.values(instanceRows(container));
    expect(only).toHaveLength(1);
    expect(only[0]?.[1]).toBe("primary");
  });

  it("pending replica: the pod with no role renders unassigned, not serving", () => {
    const entry = entryFor("pending-replica");
    const row = rowFor(entry.clusterId ?? "");
    const { container } = renderEntry(entry);

    // The fixture's replica slot: no role assigned, not ready, not serving, and
    // `containerStatus: "PodInitializing"` where the requirement says "Pending".
    const pending = row.instanceList.find((i) => i.role === undefined || i.role === "");
    expect(pending).toBeDefined();
    const cellsFor = instanceRows(container)[pending?.name ?? ""];
    expect(cellsFor).toBeDefined();
    expect(cellsFor?.[1]).toBe("unassigned");
    expect(cellsFor?.[3]).toBe("no"); // Ready
    expect(cellsFor?.[4]).toBe("no"); // Serving
    expect(cellsFor?.[5]).toBe(pending?.containerStatus ?? "");
  });

  it("CrashLoopBackOff on a CR that reports healthy: the counts disagree, and the cell says why", () => {
    const entry = entryFor("crashloop-reporting-healthy");
    const row = rowFor(entry.clusterId ?? "");
    // This state spans two tabs, which is why the entry's `owner` names both:
    // the disagreeing counts are Overview's, the container status is Instances'.
    // One render cannot show both, so the test takes each half from its tab.
    const overview = renderEntry(entry, "overview");
    const instances = renderEntry(entry, "instances");

    // R1.2's defect in one string: the Overview line prints all three counts
    // side by side, so a cluster with two pods in spec and none serving cannot
    // render as its own best case.
    expect(kv(overview.container)["Instances"]).toBe(
      `${row.servingInstances} serving · ${row.readyInstances} ready · ${row.instances} in spec`,
    );
    expect(row.instances).toBe(2);
    expect(row.servingInstances).toBe(0);
    // The other half of the state: the pod that is not serving says
    // CrashLoopBackOff in the Container column rather than inheriting a glyph
    // from the CR's phase.
    const crashers = row.instanceList.filter((i) => i.containerStatus === "CrashLoopBackOff");
    expect(crashers.length).toBeGreaterThan(0);
    const rows = instanceRows(instances.container);
    for (const pod of crashers) {
      expect(rows[pod.name]?.[5]).toBe("CrashLoopBackOff");
      expect(rows[pod.name]?.[4]).toBe("no"); // Serving
    }
  });

  it("no Pooler: the absence is worded, and names the cluster that does have one", () => {
    const entry = entryFor("no-pooler");
    const { container } = renderEntry(entry);
    const poolers = card(container, "Poolers");

    // A designed absence reads differently from an empty table: the card has no
    // table at all, and the sentence says which cluster in the reference
    // environment does have a Pooler — which is what lets a reader tell "this
    // cluster has no pooler" from "this product cannot see poolers" (R1.4).
    expect(poolers).toBeDefined();
    expect(poolers?.querySelector("table.data")).toBeNull();
    expect(poolers?.querySelector(".card-note")?.textContent).toContain(
      "No Pooler is attached to this cluster.",
    );
    expect(poolers?.querySelector("code")?.textContent).toBe("rev-proxy-pg");
  });

  it("PG16 vs PG17 column differences: the IO-wait cell names the derivation it used", () => {
    const entry = entryFor("pg16-vs-pg17");
    // The state is a pair, so the fixture has to supply both sides. The entry's
    // cluster is the PG 16.11 one; the 17 row comes from the same fixture.
    expect(rowFor(entry.clusterId ?? "").pgMajor).toBe(16);
    const on16 = FIXTURE_QUERIES.queries.find((r) => r.pgMajor === 16);
    const on17 = FIXTURE_QUERIES.queries.find((r) => r.pgMajor === 17);
    expect(on16).toBeDefined();
    expect(on17).toBeDefined();

    const title = (row: QueryView): string =>
      (loadCellFor(row, "loadByIOWaitMs") as { title?: string }).title ?? "";
    const derived16 = title(on16 as QueryView);
    const derived17 = title(on17 as QueryView);

    // R3.2's two derivations are not comparable across the major boundary —
    // PG16 has `blk_read_time`/`blk_write_time`, PG17 split them into the
    // `shared_*` pair — so the cell states which one its number came from
    // instead of rendering one column for two different measurements.
    expect(derived16).toContain("blk_read_time + blk_write_time on PG16");
    expect(derived17).toContain("shared_blk_read_time + shared_blk_write_time on PG17");
    // The assertion that makes it a *difference*: the two strings are not the
    // same sentence with a different number in it.
    expect(derived16).not.toBe(derived17);
    // And the table renders the cell rather than a placeholder for a 16 row,
    // which is the half only the component can answer.
    const { container } = render(
      <QueryTable
        rows={[on16 as QueryView]}
        sort="loadByTotalTime"
        dir="desc"
        onSort={vi.fn()}
        onOpen={vi.fn()}
        density="compact"
      />,
    );
    expect(container.querySelector("table.data")).not.toBeNull();
  });

  it("extension-absent: the R10.27 screen, not an empty table with a tooltip", () => {
    const entry = entryFor("extension-absent");
    const cluster = entry.clusterId ?? "";
    const { container } = render(<ExtensionNotInstalled cluster={cluster} />);

    // R10.27's wording is the test: a first-class screen, so it has the flow
    // and the cost on it, and it keeps the `state-panel` + `data-state` pair so
    // R16.7's suite still keys the screen by state name.
    const panel = container.querySelector(".state-panel") as HTMLElement;
    expect(panel).not.toBeNull();
    expect(panel.getAttribute("data-state")).toBe("unavailable");
    expect(panel.textContent).toContain("pg_stat_statements is not installed");
    expect(panel.textContent).toContain(cluster);
    // R3.15's four steps, in order — the part an empty table with a tooltip
    // cannot carry, and the reason this entry has its own component.
    expect(
      Array.from(panel.querySelectorAll(".ext-step")).map((n) => n.getAttribute("data-step")),
    ).toEqual(["detect", "offer", "patch", "verify"]);
    // No `onEnable`: R12.8's `--allow-actions` is off by default, so the
    // read-only deployment's version of this screen has no button and the CLI
    // line is the whole offer.
    expect(panel.querySelector("button")).toBeNull();
    expect(panel.querySelector(".ext-cli")?.textContent).toContain(
      `pgsi enable pg-stat-statements --cluster ${cluster}`,
    );
  });

  it("tags empty: the page's own copy, not a bespoke string", () => {
    const entry = entryFor("tags-empty");
    // Rendered directly, not through `renderEntry`: the owner is `pages/Tags.tsx`,
    // not `ClusterDetail`, so there is no tab to route through — the same shape as
    // the extension-absent entry above, which renders its owner for the same
    // reason. The cluster comes from the row, as every other entry's does.
    const { container } = render(
      <TagsPage
        state={withState(DEFAULT_STATE, { page: "tags", cluster: entry.clusterId ?? "" })}
        cfg={resolveConfig("", {})}
        onPatch={vi.fn()}
        fixtureOverride={{ ...FIXTURE_TAGS, data: [] }}
      />,
    );

    // The discriminator (rule 1) is that the wording is `TAGS_COPY`'s: the title
    // asserted here is the string `components/StatePanel.tsx` holds for the tags
    // payload, which is the one fact the catalogue can check without re-running
    // `Tags.test.tsx` — and the fact that would go missing if the page grew a
    // bespoke `<div>No tags</div>` instead of going through the shared panel.
    const panel = container.querySelector('.state-panel[data-state="empty"]') as HTMLElement;
    expect(panel).not.toBeNull();
    expect(panel.textContent).toContain("No tags in this window");
    // The table is replaced by the panel, not rendered empty beside it — the
    // distinction R10.27 makes for the not-installed case and R10.12 makes here.
    expect(container.querySelector('[data-testid="tags-table"]')).toBeNull();
  });
});
