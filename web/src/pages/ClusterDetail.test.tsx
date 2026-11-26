/* The cluster detail page, one test per thing the URL can ask it to show.
 *
 * The page is 621 lines whose whole job is to render one `FleetRow` six ways, so
 * the tests are organised the way the page is: first the route half (given an
 * id, find the row — and the case where there is no row, which the header calls
 * a page state rather than a router state), then the header, then the six tabs,
 * then the per-tab content.
 *
 * Two rules shape what gets asserted. The first is that a value the component
 * only *echoes* is asserted against the row it came from, not against a string
 * retyped here: `FIXTURE_CLUSTERS` is the contract fixture, so re-typing
 * `rev-proxy-pg-2` in a test would test the test. What gets a literal is a value
 * the component *transforms* — `yesNo`, the `unassigned` role, the joined
 * endpoints cell, the metrics sentence — because those are the page's own
 * decisions and the fixture cannot state them.
 *
 * The second is R1.2. The reference environment's worst cluster is the one a
 * naive render shows as its best: a CR reporting two instances with zero
 * serving, a primary in CrashLoopBackOff with 1,551 restarts, and a replica with
 * no role at all. Those three fields are asserted as one row of cells, because
 * the requirement's argument is about the *combination* — a table that printed
 * only `ready` would show the crash loop as a healthy pod.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ClusterDetailPage, seriesForCluster, type ClusterDetailPageProps } from "./ClusterDetail";
import { FIXTURE_CLUSTERS, FIXTURE_GENERATED_AT, FIXTURE_QUERY_SERIES } from "../api/fixtures";
import type { FleetRow, QuerySeries } from "../api/types";
import { resolveConfig } from "../api/client";
import { formatTimestamp } from "../lib/format";
import { DEFAULT_STATE, withState, type ClusterTab, type UrlState } from "../state/urlState";

/** Render the page for one cluster id and collect every patch it pushes.
 *
 *  `cfg` comes from `resolveConfig` rather than a literal because the component
 *  accepts it and ignores it (its own header says so — the page renders the row
 *  the URL names, it does not fetch), and the resolver is the cheapest way to
 *  hand over a real one.
 *
 *  `extra` carries the page's own optional props — today `querySeries`, the
 *  R8.11 batch. Omitting it is not the same as passing nothing: the page then
 *  falls back to the contract fixture filtered to the row, so a test that wants
 *  to see the fallback leaves `extra` out and a test that wants to see the
 *  caller's answer passes it.
 */
function renderDetail(
  cluster: string | null,
  patch: Partial<UrlState> = {},
  extra: Partial<ClusterDetailPageProps> = {},
) {
  const patches: Partial<UrlState>[] = [];
  const state = withState(DEFAULT_STATE, { page: "cluster", cluster, ...patch });
  const view = render(
    <ClusterDetailPage
      state={state}
      cfg={resolveConfig("", {})}
      onPatch={(p) => {
        patches.push(p);
      }}
      {...extra}
    />,
  );
  return { ...view, patches, state };
}

/** A fixture row by id, so a test addresses a cluster without rebuilding it. */
function rowFor(id: string): FleetRow {
  const row = FIXTURE_CLUSTERS.find((c) => `${c.namespace}/${c.name}` === id);
  if (row === undefined) throw new Error(`no fixture cluster ${id}`);
  return row;
}

const REV_PROXY = rowFor("ai-api-gateway/rev-proxy-pg");
const COOKBOOK = rowFor("cookbook/cookbook-pg");
const FREEIDP = rowFor("freeidp/freeidp-pg");

/** The id the URL uses for a row. Every call site below goes through this, so
 *  the ids in the tests are the fixture's own and not a second copy of them. */
const idOf = (row: FleetRow): string => `${row.namespace}/${row.name}`;

/** The tab keys are urlState's (`ClusterTab`); the labels are the page's. Both
 *  are module-private in their own files, so this is the one place the pairing
 *  is written down — which is also what makes a new tab key fail here. */
const TAB_LABELS: Record<ClusterTab, string> = {
  overview: "Overview",
  instances: "Instances",
  replication: "Replication",
  storage: "Storage",
  config: "Config",
  findings: "Findings",
};

/** The cards each tab renders, in DOM order. `replication` has none because its
 *  body is a `StatePanel` (`section.state-panel`), not a card — the tab is
 *  deliberately empty, since replication statistics are R1.6's collector work
 *  and a placeholder chart would be R10.12's "a state that looks like data". */
const TAB_CARDS: Record<ClusterTab, string[]> = {
  overview: ["Overview", "Query load", "Resource load", "Certificates"],
  instances: ["Instances"],
  replication: [],
  storage: ["Storage"],
  config: ["Poolers", "Services"],
  findings: ["Findings"],
};

/** The card titles currently rendered, in DOM order. */
function cardTitles(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll("section.card > .card-head > .card-title")).map(
    (el) => el.textContent ?? "",
  );
}

/** The Overview `dl.kv` as a map, so a test asks for a row by its term instead
 *  of an index that moves when a row is added. */
function kv(container: HTMLElement): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of Array.from(container.querySelectorAll("dl.kv .kv-row"))) {
    const dt = row.querySelector("dt");
    const dd = row.querySelector("dd");
    if (dt !== null && dd !== null) out[dt.textContent ?? ""] = dd.textContent ?? "";
  }
  return out;
}

/** A table row's cells as text, in column order. */
function cells(row: Element): string[] {
  return Array.from(row.querySelectorAll("td")).map((td) => td.textContent ?? "");
}

/** The `table.data` rows inside the first card that has a table. */
function tableRows(container: HTMLElement): Element[] {
  return Array.from(container.querySelectorAll("section.card table.data tbody tr"));
}

describe("ClusterDetailPage", () => {
  it("resolves every cluster in the snapshot, not just the first", () => {
    // R10.16's route contract: the URL names the row and the page finds it.
    // Resolving here rather than in an App.tsx branch is the point — a detail
    // page that cannot find its row is a page state, so this loop is the proof
    // that all four fixture clusters route, not just the one routes.test.tsx
    // happens to link to.
    for (const row of FIXTURE_CLUSTERS) {
      const { container } = renderDetail(idOf(row));
      const title = container.querySelector("h1.page-title");
      expect(title?.textContent).toContain(row.name);
      // The chip is pgsi's judgement, derived from the counts by healthFor, and
      // the header keeps it next to the CNPG phase string it was derived from.
      expect(container.querySelector("h1.page-title .status-chip")?.getAttribute("data-health")).toBe(
        row.health,
      );
    }
  });

  it("says so when the id is not in the snapshot, and offers the way back", () => {
    // A stale or hand-typed cluster id. The page renders a card that names the
    // id it could not find — the alternative, an empty detail page, reads as an
    // empty cluster.
    const { container, patches } = renderDetail("ghost/ghost-pg");
    expect(container.querySelector(".card-title")?.textContent).toBe("Unknown cluster");
    expect(container.querySelector("code")?.textContent).toBe("ghost/ghost-pg");
    expect(container.querySelector(".card-note")?.textContent).toContain("is not in the current fleet snapshot.");

    fireEvent.click(screen.getByRole("button", { name: "Back to Fleet" }));
    expect(patches).toEqual([{ page: "fleet", cluster: null }]);
  });

  it("prints an empty code, not the string \"null\", when no cluster is selected", () => {
    // `state.cluster` is `string | null`; the `?? ""` is what stops a null
    // rendering as the word "null" inside the card's sentence.
    const { container } = renderDetail(null);
    expect(container.querySelector("code")?.textContent).toBe("");
  });
});

describe("ClusterDetail header", () => {
  it("carries the row's own words: name, derived chip, phase verbatim, observed stamp", () => {
    const { container } = renderDetail(idOf(REV_PROXY));

    expect(container.querySelector("h1.page-title")?.textContent).toContain("rev-proxy-pg");
    // R1.5 keeps the CNPG phase string verbatim — the page does not paraphrase
    // "Cluster in healthy state" into "Healthy", because the chip already is
    // pgsi's own judgement and the two are different claims.
    expect(container.querySelector(".page-meta")?.textContent).toBe(REV_PROXY.phase);
    // The breadcrumb is the only route back that does not depend on the browser.
    const link = container.querySelector("a.cluster-link");
    expect(link?.getAttribute("href")).toBe("/");
    expect(link?.textContent).toBe("Fleet");
    expect(container.querySelector(".breadcrumb")?.textContent).toContain(REV_PROXY.namespace);

    // R10.21's stamp: the title carries the UTC instant, the visible text is the
    // local rendering, and both come from the row (which denormalises
    // meta.generatedAt) rather than from a clock read at render time.
    const stamp = container.querySelector(".page-actions .card-meta.num");
    expect(stamp?.getAttribute("title")).toBe(FIXTURE_GENERATED_AT);
    expect(stamp?.textContent).toBe(`Observed ${formatTimestamp(FIXTURE_GENERATED_AT, { withZone: false })}`);
  });

  it("appends the phaseReason when the row has one", () => {
    // The rule has three legs — no reason, a reason equal to the phase, a reason
    // that differs — and the fixture set has two of them: this row's reason
    // differs, the other three have none. The equal leg renders nothing extra and
    // has no fixture, so it is the one branch this test cannot reach.
    const { container } = renderDetail(idOf(FREEIDP));
    const meta = container.querySelector(".page-meta")?.textContent ?? "";
    expect(meta.startsWith(FREEIDP.phase)).toBe(true);
    expect(meta).toContain(FREEIDP.phaseReason ?? "");
  });
});

describe("ClusterDetail tabs", () => {
  it("renders one button per tab and marks exactly the current one", () => {
    const { container } = renderDetail(idOf(REV_PROXY), { tab: "config" });
    const nav = container.querySelector('nav.tabs[aria-label="Cluster sections"]');
    expect(nav).not.toBeNull();

    const buttons = Array.from(nav?.querySelectorAll("button.tab") ?? []);
    expect(buttons.map((b) => b.textContent)).toEqual(Object.values(TAB_LABELS));

    // Exactly one tab is current, and it is the one the URL names — the
    // `tab active` class and `aria-current` are the same decision, so a tab list
    // that highlights the wrong section cannot pass by styling the right one.
    const current = buttons.filter((b) => b.getAttribute("aria-current") === "page");
    expect(current).toHaveLength(1);
    expect(current[0]?.textContent).toBe(TAB_LABELS.config);
    expect(current[0]?.getAttribute("class")).toBe("tab active");
    expect(buttons.filter((b) => b.getAttribute("class") === "tab active")).toHaveLength(1);
  });

  it("patches the tab alone when a tab is clicked", () => {
    // R10.16 again, at the second level: a tab click patches `{ tab }` and
    // nothing else. A patch that also rewrote `cluster` or `sort` would navigate
    // out of the cluster or reset a sort the reader chose.
    for (const tab of Object.keys(TAB_LABELS) as ClusterTab[]) {
      const { container, patches } = renderDetail(idOf(REV_PROXY), { tab: "overview" });
      const button = Array.from(container.querySelectorAll("button.tab")).find(
        (b) => b.textContent === TAB_LABELS[tab],
      );
      expect(button).toBeDefined();
      if (button === undefined) continue;
      fireEvent.click(button);
      expect(patches).toEqual([{ tab }]);
    }
  });

  it("renders the cards each tab owns", () => {
    // The tab body table, asserted as a list per tab rather than one test per
    // tab: the claim worth testing is that switching tabs swaps the body and
    // every body is accounted for, including the one that is deliberately empty.
    for (const tab of Object.keys(TAB_LABELS) as ClusterTab[]) {
      const { container } = renderDetail(idOf(REV_PROXY), { tab });
      expect(cardTitles(container)).toEqual(TAB_CARDS[tab]);
    }
  });
});

describe("Overview tab", () => {
  it("renders the contract's eight fields for a healthy cluster", () => {
    const { container } = renderDetail(idOf(REV_PROXY));
    const rows = kv(container);
    // The three counts are one cell, not three, because R10.28's states are
    // defined by their relation: 2 serving / 2 ready / 2 in spec is the healthy
    // two-instance state and 0 / 0 / 2 is a different one.
    expect(rows.Instances).toBe("2 serving · 2 ready · 2 in spec");
    expect(rows["Current primary"]).toBe("rev-proxy-pg-2");
    expect(rows["PostgreSQL major"]).toBe("17");
    // R1.3: the image sits alongside the major because the pod label's "17"
    // cannot express 17.11, and R2.4's matrix keys off the exact minor.
    expect(rows.Image).toBe("ghcr.io/cloudnative-pg/postgresql:17");
    expect(rows.Version).toBe("17.11");
    // R1.6's zero-prerequisite acceptance reads this: which of R1.8's four
    // precedence levels supplied the credentials.
    expect(rows.Credentials).toBe("secret-cluster");
    expect(rows.Created).toBe(formatTimestamp("2026-09-23T08:44:48Z", { withZone: false }));
    // R10.4's metrics cell in words. "unconfigured" and "failed" are different
    // answers (live-probe Findings 1 and 3) and the page says them apart.
    expect(rows.Metrics).toBe("unconfigured (R10.12 unavailable)");
  });

  it("renders the zero-serving cluster without hiding the difference between its counts", () => {
    const { container } = renderDetail(idOf(FREEIDP));
    const rows = kv(container);
    // R1.2's headline case: the CR reports two instances and serves none.
    expect(rows.Instances).toBe("0 serving · 0 ready · 2 in spec");
    // The null leg of the version row: this cluster reports no detail at all, and
    // renders the shared placeholder rather than an empty cell that would read
    // as "version 0".
    expect(rows.Version).toBe("—");
    expect(rows.Metrics).toBe("collector failed (R10.12)");
  });

  it("mounts both halves of the R10.5 split and both say they have no data", () => {
    // The 6b pin. The ruling splits R10.5 by part of the cell — Query load here
    // (6b, `components/QueryLoadArea.tsx`), Resource load's graph in Phase 7 —
    // and this page mounts both with the honest inputs. The panel's is
    // `source={null}`, because `client.ts` has no OS-metrics fetcher at all.
    // The chart's is `stackSeries(seriesForCluster(cluster, querySeries))`, and
    // for this cluster that folds to no buckets: the R8.11 fixture carries
    // series for open-webui only, so rev-proxy-pg's answer is still R10.13's
    // sentence — now because the payload has nothing for it, not because the
    // page never asked. The plotted half is the sibling test in the wiring
    // block below, and the two together are what this one assertion used to be.
    const { container } = renderDetail(idOf(REV_PROXY));

    const queryLoad = container.querySelector("section.card[aria-labelledby='qla-title']");
    expect(queryLoad).not.toBeNull();
    expect(queryLoad?.querySelector(".card-title")?.textContent).toBe("Query load");
    expect(queryLoad?.querySelector("p.chart-empty")?.textContent).toMatch(/no query-load buckets/i);
    // No svg: an empty chart and a chart of nothing are different states, and
    // R10.13's sentence is the first one.
    expect(queryLoad?.querySelector("svg")).toBeNull();
    // R10.19's legend renders even with no plot, and the IO entry carries the
    // `n/a` that disappears the day the field lands.
    expect(queryLoad?.querySelector(".qla-legend")?.textContent).toContain("IO time · n/a");

    const resourceLoad = container.querySelector("section.card.rl-panel");
    expect(resourceLoad?.getAttribute("data-state")).toBe("unavailable");
    // The panel is handed the cluster it belongs to, so a page that renders the
    // wrong cluster's panel fails here even while both say "not queried".
    expect(resourceLoad?.querySelector(".rl-cluster .mono")?.textContent).toBe(idOf(REV_PROXY));
  });

  it("renders every certificate the contract reports, not a fixed set of three", () => {
    // The header's argument: CNPG emits one entry per secret and the three named
    // fields only say which entry is which, so the map is the source of truth for
    // how many exist. The count in the card head is the assertion.
    const { container } = renderDetail(idOf(REV_PROXY));
    const certCard = Array.from(container.querySelectorAll("section.card")).find(
      (s) => s.querySelector(".card-title")?.textContent === "Certificates",
    );
    expect(certCard?.querySelector(".card-meta.num")?.textContent).toBe("3");
    const rows = Array.from(certCard?.querySelectorAll("table.data tbody tr") ?? []).map(cells);
    expect(rows).toEqual([
      ["rev-proxy-pg-ca", "client CA", expect.any(String)],
      ["rev-proxy-pg-replication", "replication TLS", expect.any(String)],
      ["rev-proxy-pg-server", "server TLS", expect.any(String)],
    ]);
  });
});

describe("ClusterDetail query-series wiring", () => {
  // R10.5(a)'s data half. The fetcher is `client.ts:342` and the batch it
  // returns is `fetchQueries`' top-N (cap 50, and R8.10 forbids clamping it
  // silently); what the page decides, and so what these tests cover, is only
  // which of the batch's series reach the chart and what the chart does with
  // them.
  //
  // `batch()` builds series here instead of reusing the fixture because the
  // contract fixture's three series all belong to open-webui: a filter tested
  // against a single-cluster array passes for the wrong reason, and a one-point
  // batch is the only way to see the caller's answer rather than the default.
  const batch = (namespace: string, cluster: string, fingerprint: string): QuerySeries => ({
    fingerprint,
    namespace,
    cluster,
    points: [
      {
        timestamp: "2026-11-23T09:00:00Z",
        executionTimeSeconds: 1.5,
        rows: 12,
        sharedBlocksHit: 4,
        sharedBlocksRead: 2,
        lockTimeSeconds: 0.01,
        ioTimeSeconds: 0.25,
      },
    ],
  });

  it("keeps only the named cluster's series out of a mixed batch", () => {
    const mixed = [
      batch("cookbook", "cookbook-pg", "a".repeat(64)),
      batch("open-webui", "open-webui-pg", "b".repeat(64)),
    ];
    const mine = seriesForCluster({ namespace: "cookbook", name: "cookbook-pg" }, mixed);
    expect(mine).toHaveLength(1);
    expect(mine[0]?.cluster).toBe("cookbook-pg");
  });

  it("returns nothing for a cluster the batch has no series for", () => {
    // The fixture's own answer for three of its four clusters, and the case the
    // chart renders as R10.13's sentence rather than an empty frame.
    expect(seriesForCluster(REV_PROXY, FIXTURE_QUERY_SERIES.data)).toEqual([]);
  });

  it("plots the fixture's buckets for the cluster that has them", () => {
    // The plotted half of the R10.5 split; the empty half is the Overview test
    // above. The fixture's two open-webui fingerprints share 09:11 and one of
    // them owns 09:12, so the two buckets come from the fold, not from a
    // literal here.
    const { container } = renderDetail("open-webui/open-webui-pg");
    const card = container.querySelector("section.card[aria-labelledby='qla-title']");
    expect(card?.querySelector("p.chart-empty")).toBeNull();
    expect(card?.querySelector("svg")).not.toBeNull();
    // R10.19's legend drops the `· n/a` the moment the IO band has a value. The
    // fixture's io values are real, so this is the pair's other side — the
    // Overview test asserts the `n/a`, this one asserts its absence.
    const legend = card?.querySelector(".qla-legend")?.textContent ?? "";
    expect(legend).toContain("IO time");
    expect(legend).not.toContain("IO time · n/a");
  });

  it("prefers the caller's batch over the fixture", () => {
    // The prop is the seam A4's `useQuerySeries` fills, so the fixture has to be
    // provably a default and not the answer: rev-proxy-pg has nothing in the
    // fixture, so the only way its chart can render is if the prop won.
    const { container } = renderDetail(idOf(REV_PROXY), {}, {
      querySeries: [batch("ai-api-gateway", "rev-proxy-pg", "c".repeat(64))],
    });
    const card = container.querySelector("section.card[aria-labelledby='qla-title']");
    expect(card?.querySelector("p.chart-empty")).toBeNull();
    expect(card?.querySelector("svg")).not.toBeNull();
  });
});

describe("Instances tab", () => {
  it("renders R1.2's trio per pod so a crash loop cannot read as healthy", () => {
    const { container } = renderDetail(idOf(FREEIDP), { tab: "instances" });
    const rows = tableRows(container);
    expect(rows).toHaveLength(2);

    // The primary: a running pod in CrashLoopBackOff with 1,551 restarts. The
    // Container cell prints the container's state verbatim rather than deriving
    // a glyph from the pod phase, which is the whole requirement — `phase
    // "Running"` and `containerStatus "CrashLoopBackOff"` are both true and the
    // table has to show both.
    expect(cells(rows[0] as Element)).toEqual([
      "freeidp-pg-1",
      "primary",
      "k8s-ctrl",
      "no",
      "no",
      "CrashLoopBackOff",
      "1,551",
      formatTimestamp("2026-10-09T07:56:21Z", { withZone: false }),
      "pg 10.42.0.198:5432 · http://10.42.0.198:9187 · https://10.42.0.198:8000",
    ]);

    // The replica: no role key at all on the wire (R1.2 — a role is assigned
    // later, and "unassigned" is a state where a blank cell is a gap), a pod with
    // no address that serialises as `endpoints: {}` and renders as the shared
    // placeholder rather than an empty cell.
    expect(cells(rows[1] as Element)).toEqual([
      "freeidp-pg-2",
      "unassigned",
      "um760-1",
      "no",
      "no",
      "PodInitializing",
      "0",
      formatTimestamp("2026-11-15T09:28:56Z", { withZone: false }),
      "—",
    ]);
  });

  it("marks the row the URL selects and a click selects another", () => {
    // R10.16's `?row=` on the second level of the URL: the selected pod is
    // marked, and clicking a row patches the row and nothing else.
    const { container, patches } = renderDetail(idOf(FREEIDP), {
      tab: "instances",
      row: "freeidp-pg-1",
    });
    const rows = tableRows(container);
    expect(rows[0]?.getAttribute("data-selected")).toBe("true");
    expect(rows[1]?.hasAttribute("data-selected")).toBe(false);

    if (rows[1] === undefined) throw new Error("no second instance row");
    fireEvent.click(rows[1]);
    expect(patches).toEqual([{ row: "freeidp-pg-2" }]);
  });
});

describe("Config tab", () => {
  it("renders the pooler the cluster has, with its parameters verbatim", () => {
    const { container } = renderDetail(idOf(REV_PROXY), { tab: "config" });
    const poolers = Array.from(container.querySelectorAll("section.card")).find(
      (s) => s.querySelector(".card-title")?.textContent === "Poolers",
    );
    const row = cells(poolers?.querySelector("table.data tbody tr") as Element);
    expect(row[0]).toBe("rev-proxy-pg-pooler");
    expect(row[1]).toBe("rw");
    // R6's client-attribution problem keys off the pool mode, so it is rendered
    // as the contract has it rather than implied by the pooler's name.
    expect(row[2]).toBe("transaction");
    expect(row[3]).toBe("2/2");
    // `spec.pgbouncer.parameters` whole: the old type flattened three of them
    // into numbers, and a parameter nobody anticipated is only visible if the
    // map is printed.
    expect(row[6]).toMatch(/default_pool_size=\d+/);
  });

  it("names the reference environment's distribution when a cluster has no pooler (R1.4)", () => {
    // R10.28's *no Pooler* entry. A designed absence reads differently from an
    // accidental one, so the wording is the state — and it names which cluster
    // *does* have one, which is what lets a reader tell "this cluster has no
    // pooler" from "this app cannot see poolers".
    const { container } = renderDetail(idOf(COOKBOOK), { tab: "config" });
    const poolers = Array.from(container.querySelectorAll("section.card")).find(
      (s) => s.querySelector(".card-title")?.textContent === "Poolers",
    );
    expect(poolers?.querySelector("code")?.textContent).toBe("rev-proxy-pg");
    expect(poolers?.querySelector(".card-note")?.textContent).toContain("No Pooler is attached to this cluster.");
    expect(poolers?.querySelector(".card-note")?.textContent).toContain("(R1.4)");
    // The same tab's Services card is the R1.5 list: CNPG's -rw/-ro/-r trio.
    const services = Array.from(container.querySelectorAll("section.card")).find(
      (s) => s.querySelector(".card-title")?.textContent === "Services",
    );
    expect(services?.querySelectorAll("table.data tbody tr")).toHaveLength(3);
  });
});

describe("Findings tab", () => {
  it("prints the finding the row carries", () => {
    const { container } = renderDetail(idOf(FREEIDP), { tab: "findings" });
    const finding = container.querySelector("div.finding");
    expect(finding?.getAttribute("data-severity")).toBe("critical");
    expect(finding?.getAttribute("class")).toBe("finding critical");
    expect(finding?.querySelector(".finding-code")?.textContent).toBe("disk-pressure");
    expect(finding?.querySelector(".finding-summary")?.textContent).toBe(
      "No space left on device; postgres container in CrashLoopBackOff",
    );
  });

  it("explains why a cluster without findings has none (R10.4)", () => {
    // Not an empty table: the sentence names the dependency, so "no findings"
    // from a cluster with no metrics source reads differently from "no findings"
    // from a healthy one. The metrics word comes from the same cell the Overview
    // tab prints.
    const { container } = renderDetail(idOf(REV_PROXY), { tab: "findings" });
    expect(container.querySelector("div.finding")).toBeNull();
    expect(container.querySelector(".card-note")?.textContent).toBe(
      "No findings. Findings need a metrics source (R10.4); this cluster reports unconfigured (R10.12 unavailable).",
    );
  });
});
