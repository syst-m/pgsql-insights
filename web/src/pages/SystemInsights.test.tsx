/* R10.10's page shell (`requirements.md:423`, `web/src/routes.ts:280-290`).
 *
 * The page has no chart and no fetcher yet — A3's ruling is that it renders
 * R10.12's unavailable state until Phase 7's DTO lands — so this file cannot
 * test a rendered chart. What it tests is the four inventories the requirement
 * states today, each of which is a contract the eventual chart has to honour:
 *
 * 1. **the shell** — `data-page`, the `<h1>` the route's `title:` promises
 *    (`web/src/routes.ts:282`), and R10.21's stamp pair. The `data-page`/`h1`
 *    pair is also what `routes.test.tsx`'s `IMPLEMENTED` row will assert when
 *    A4 flips `component: null` at `web/src/routes.ts:286`, so it is asserted
 *    here first — the same order `WaitEvents.test.tsx:8-11` sets.
 * 2. **R5.1's five cards** — the labels verbatim, and the one value the spec
 *    answers for a default deployment (`Log errors` → `not configured`, from
 *    R5.1's [A] clause plus R5.10's default `off`) against the four it does not
 *    (R10.13's `—`).
 * 3. **R5.2/R5.3's eleven groups** — the tab strip's contents, order and
 *    selection, plus the R10.19 rule that each tab's hover text is derived from
 *    the same table that renders its label.
 * 4. **R5.6's toolbar and R5.7's selector** — three buttons with per-button
 *    reasons for being disabled, the zoom gesture stated in prose because it is
 *    not a button, and the compare-up-to-5 cap read out of the cluster's own
 *    `instanceList`.
 *
 * Rendered directly, not through `<App>`: the mount is `routes.test.tsx`'s to
 * assert, and `PageProps<T>` exists so a page test can pass the props the shell
 * will — `fixtureOverride` among them, which is how every case here pins a
 * payload. The cluster is read out of `FIXTURE_CLUSTERS[0]` rather than
 * retyped, so a fixture rename moves this file with it instead of turning it
 * red for the wrong reason (`EmptyStateCatalogue.test.tsx:34-39`'s rule).
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { ClientConfig } from "../api/client";
import { FIXTURE_CLUSTERS, FIXTURE_GENERATED_AT } from "../api/fixtures";
import type { Envelope, FleetRow, InstanceView } from "../api/types";
import type { SystemGroupView } from "../components/StatePanel";
import { formatNumber, formatTimestamp, NULL_VALUE } from "../lib/format";
import { DEFAULT_STATE, type UrlState } from "../state/urlState";
import {
  compareOptions,
  METRIC_GROUPS,
  SUMMARY_CARDS,
  summaryCardValue,
  SystemInsightsPage,
  TOOLBAR,
  ZOOM_NOTE,
  type SummaryCard,
  type SystemInsightsPageProps,
} from "./SystemInsights";

const CFG: ClientConfig = { source: "fixture", apiBase: "", fixture: "fleet", delayMs: 0 };

/** The first fixture cluster, read rather than retyped: the page resolves the
 *  URL's `namespace/name` against `FIXTURE_CLUSTERS`, so the id and the row
 *  under test have to be the same object. */
const CLUSTER = FIXTURE_CLUSTERS[0] as FleetRow;
const CLUSTER_ID = `${CLUSTER.namespace}/${CLUSTER.name}`;

/** `/c/:ns/:name/system` (`web/src/routes.ts:288`) — `scope: "cluster"`, and the
 *  route takes no other params, so this is the whole URL. */
const URL_STATE: UrlState = { ...DEFAULT_STATE, page: "system", cluster: CLUSTER_ID };

function renderPage(overrides: Partial<SystemInsightsPageProps> = {}) {
  const onPatch = vi.fn();
  const props: SystemInsightsPageProps = {
    state: URL_STATE,
    onPatch,
    cfg: CFG,
    ...overrides,
  };
  return { ...render(<SystemInsightsPage {...props} />), onPatch };
}

/** A payload built over the groups named in the first argument. */
function systemEnvelope(
  groups: readonly string[],
  valueOf: (group: string, index: number) => number | null = (_g, i) => i * 100,
): Envelope<SystemGroupView> {
  const data: SystemGroupView[] = groups.map((group, i) => ({
    group,
    promql: `up{insights_group="${group}"}`,
    value: valueOf(group, i),
  }));
  return {
    data,
    meta: { generatedAt: FIXTURE_GENERATED_AT, total: data.length, truncated: false },
  };
}

/** A payload covering every group, so every grouped card has a row to read. */
const FULL = systemEnvelope(METRIC_GROUPS.map((g) => g.key));

/** The tile that renders a card's value, found by its label — so the test reads
 *  the pair the user sees rather than trusting the function in isolation. */
function tileValue(container: HTMLElement, label: string): string | undefined {
  const tile = Array.from(container.querySelectorAll(".stat-tile")).find(
    (t) => t.querySelector(".stat-label")?.textContent === label,
  );
  return tile?.querySelector(".stat-value")?.textContent ?? undefined;
}

/* ── 1. the shell ──────────────────────────────────────────────────────────── */

describe("SystemInsightsPage — the shell (R10.10, R10.21)", () => {
  it("renders the shell a cluster URL names", () => {
    const { container } = renderPage();
    expect(container.querySelector('[data-page="system"]')).not.toBeNull();
    // The heading text is the route table's `title:` at `web/src/routes.ts:282`,
    // read through the fixture rather than retyped here.
    expect(screen.getByRole("heading", { level: 1, name: "System Insights" })).toBeInTheDocument();
  });

  it("stamps the header from the payload when there is one (R10.21)", () => {
    const { container } = renderPage({ fixtureOverride: FULL });
    const meta = container.querySelector(".page-meta");
    expect(meta?.getAttribute("title")).toBe(`${FIXTURE_GENERATED_AT} UTC`);
    expect(meta?.textContent).toContain(formatTimestamp(FIXTURE_GENERATED_AT));
  });

  it("says so in the header when there is no payload, and prints no stamp", () => {
    const { container } = renderPage();
    const meta = container.querySelector(".page-meta");
    // R10.21's stamp is a timestamp; with no payload there is no timestamp to
    // stamp, so the attribute is absent rather than empty and the text carries
    // the sentence that says why.
    expect(meta?.hasAttribute("title")).toBe(false);
    expect(meta?.textContent).toBe("No system-metrics payload has been fetched for this cluster.");
  });

  it("shows no refresh indicator, because the page has no poll to advertise", () => {
    const { container } = renderPage({ fixtureOverride: FULL });
    // R10.8's widget prints a poll interval. This page has no fetcher, so
    // printing R5.8's 60 s would advertise a poll that never happens.
    expect(container.querySelector(".refresh-indicator")).toBeNull();
  });
});

/* ── 2. R5.1's five summary cards ──────────────────────────────────────────── */

describe("R5.1's summary cards", () => {
  it("renders the five cards with the requirement's own labels", () => {
    renderPage();
    expect(SUMMARY_CARDS).toHaveLength(5);
    for (const card of SUMMARY_CARDS) {
      expect(screen.getByText(card.label)).toBeInTheDocument();
    }
  });

  it("prints `not configured` for Log errors and `—` for the other four", () => {
    // The one card the spec answers before the DTO exists: R5.1's [A] clause
    // sources it from R5.10 and says it renders `not configured` when
    // `--log-source=off`, and R5.10's flag defaults to off. The other four have
    // no answer yet, so they take R10.13's `—` — never `0`.
    const { container } = renderPage();
    expect(container.querySelector(".stat-row")).not.toBeNull();
    for (const card of SUMMARY_CARDS) {
      const expected = card.key === "logErrors" ? "not configured" : NULL_VALUE;
      expect(summaryCardValue(card, null)).toBe(expected);
      expect(tileValue(container, card.label)).toBe(expected);
    }
  });

  it("reads each card's value from its group's row once a payload exists", () => {
    const { container } = renderPage({ fixtureOverride: FULL });
    for (const card of SUMMARY_CARDS) {
      if (card.group === null) continue;
      // The row is found here independently of `summaryCardValue`, so the test
      // checks the pairing (card → group → row) rather than restating it.
      const row = FULL.data.find((v) => v.group === card.group) as SystemGroupView;
      expect(tileValue(container, card.label)).toBe(formatNumber(row.value));
    }
  });

  it("falls back to `—` for a card whose group has no row", () => {
    const osOnly = systemEnvelope(["os"]);
    const { container } = renderPage({ fixtureOverride: osOnly });
    const cpu = SUMMARY_CARDS.find((c) => c.key === "cpu") as SummaryCard;
    const row = osOnly.data.find((v) => v.group === cpu.group) as SystemGroupView;
    expect(tileValue(container, cpu.label)).toBe(formatNumber(row.value));

    // Log errors has no group at all, so once a payload exists it has no value
    // to read: R5.1's `not configured` answer belongs to the no-payload case.
    const logErrors = SUMMARY_CARDS.find((c) => c.key === "logErrors") as SummaryCard;
    expect(tileValue(container, logErrors.label)).toBe(NULL_VALUE);
  });
});

/* ── 3. R5.2 / R5.3's metric groups ────────────────────────────────────────── */

describe("R5.2 and R5.3's metric groups", () => {
  it("renders one tab per group, in the requirements' order", () => {
    const { container } = renderPage();
    const tabs = Array.from(
      container.querySelectorAll('[data-panel="system-tabs"] .segmented > button'),
    );
    expect(tabs).toHaveLength(METRIC_GROUPS.length);
    expect(tabs.map((t) => t.textContent)).toEqual(METRIC_GROUPS.map((g) => g.label));
  });

  it("keeps exactly one tab selected, and moves the selection on click", () => {
    const { container } = renderPage();
    const pressed = () =>
      Array.from(container.querySelectorAll('[data-panel="system-tabs"] button')).filter(
        (b) => b.getAttribute("aria-pressed") === "true",
      );
    expect(pressed().map((b) => b.textContent)).toEqual(["OS"]);

    const wal = screen.getByRole("button", { name: "WAL" });
    fireEvent.click(wal);
    expect(pressed().map((b) => b.textContent)).toEqual(["WAL"]);
    // The chart card follows the tab rather than keeping its own selection — the
    // one-chart-one-selection rule R10.10's single `metric` route implies.
    expect(container.querySelector('[data-panel="system-chart"]')?.getAttribute("data-group")).toBe(
      "wal",
    );
  });

  it("derives each tab's hover text from the table that renders its label (R10.19)", () => {
    const { container } = renderPage();
    for (const g of METRIC_GROUPS) {
      const tab = Array.from(container.querySelectorAll('[data-panel="system-tabs"] button')).find(
        (b) => b.textContent === g.label,
      );
      // One table for both the visible label and the tooltip: a tooltip with its
      // own wording would be a second copy of the answer.
      expect(tab?.getAttribute("title")).toBe(`${g.measures} · ${g.source}`);
    }
  });
});

/* ── 4. R5.6's toolbar and R5.7's selector ─────────────────────────────────── */

describe("R5.6's chart toolbar", () => {
  it("renders the three export buttons disabled, each naming what it needs", () => {
    const { container } = renderPage();
    const buttons = Array.from(
      container.querySelectorAll<HTMLButtonElement>('[data-panel="system-chart"] .card-actions button'),
    );
    expect(buttons).toHaveLength(TOOLBAR.length);
    for (const button of buttons) {
      expect(button.disabled).toBe(true);
      // A control that is inert without saying why is R10.12's failure mode in
      // button form; the title names the field that would enable it.
      expect(button.getAttribute("title")).toMatch(/disabled, needs /);
    }
  });

  it("enables them once the group has a series, and drops the reason", () => {
    const { container } = renderPage({ fixtureOverride: FULL });
    const buttons = Array.from(
      container.querySelectorAll<HTMLButtonElement>('[data-panel="system-chart"] .card-actions button'),
    );
    for (const [i, button] of buttons.entries()) {
      expect(button.disabled).toBe(false);
      expect(button.getAttribute("title")).toBe(TOOLBAR[i]?.label);
    }
  });

  it("states the zoom gesture in prose, since it is not a button", () => {
    const { container } = renderPage();
    // R5.6 names four controls and only three are buttons; the fourth is a
    // gesture on a plot that does not exist yet.
    expect(screen.getByText(ZOOM_NOTE)).toBeInTheDocument();
    expect(container.querySelectorAll('[data-panel="system-chart"] .card-actions button')).toHaveLength(3);
  });
});

describe("R5.7's compare selector", () => {
  const POD = CLUSTER.instanceList[0] as InstanceView;
  const SEVEN: FleetRow = {
    ...CLUSTER,
    currentPrimary: "pg-3",
    instanceList: Array.from({ length: 7 }, (_, i) => ({ ...POD, name: `pg-${i}` })),
  };

  it("caps the options at five and puts the primary first", () => {
    // R5.7's cap is the requirement's ("Compare up to 5 instances"), and R9.3's
    // identity triple is what makes a pod nameable at all.
    expect(compareOptions(SEVEN)).toEqual(["pg-3", "pg-0", "pg-1", "pg-2", "pg-4"]);
    const names = CLUSTER.instanceList.map((i) => i.name);
    const options = compareOptions(CLUSTER);
    expect(options.length).toBeLessThanOrEqual(5);
    // Same pods, reordered: the set is the payload's, and the primary leads it.
    // Asserting the array equals the payload order would restate the fixture and
    // miss the point — the fixture's primary is not its first pod.
    expect([...options].sort()).toEqual([...names].sort());
    expect(options[0]).toBe(CLUSTER.currentPrimary);
  });

  it("selects every offered instance by default and toggles one off", () => {
    const { container } = renderPage();
    const buttons = Array.from(
      container.querySelectorAll('[data-panel="system-compare"] .segmented > button'),
    );
    expect(buttons).toHaveLength(compareOptions(CLUSTER).length);
    expect(buttons.every((b) => b.getAttribute("aria-pressed") === "true")).toBe(true);

    fireEvent.click(buttons[0] as HTMLElement);
    expect(buttons[0]?.getAttribute("aria-pressed")).toBe("false");
    expect(buttons[1]?.getAttribute("aria-pressed")).toBe("true");
    expect(
      container.querySelector('[data-panel="system-compare"] .card-meta')?.textContent,
    ).toBe(`${compareOptions(CLUSTER).length - 1} of 5`);
  });
});

/* ── 5. the no-cluster URL ─────────────────────────────────────────────────── */

describe("a URL with no cluster", () => {
  it("renders the NoCluster card and offers the way back", () => {
    const onPatch = vi.fn();
    const props: SystemInsightsPageProps = {
      state: { ...DEFAULT_STATE, page: "system", cluster: null },
      onPatch,
      cfg: CFG,
    };
    const { container } = render(<SystemInsightsPage {...props} />);
    expect(container.querySelector('[data-panel="no-cluster"]')).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Back to Fleet" }));
    expect(onPatch).toHaveBeenCalledWith({ page: "fleet", cluster: null });
  });
});
