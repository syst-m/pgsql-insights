/* R10.12's named states, rendered.
 *
 * Each state is reached through the same path the app uses — a URL plus a
 * payload — so the test proves the routing and the state choice together, and
 * R16.7's visual suite screenshots the same six screens. */

import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { App } from "../App";
import { COLUMNS } from "../components/ClusterTable";
import { ROW_HEIGHT } from "../components/VirtualList";
import {
  FIXTURE_EMPTY,
  FIXTURE_FLEET,
  FIXTURE_GENERATED_AT,
  FIXTURE_PARTIAL,
  FIXTURE_TRUNCATED_PARTIAL,
  FIXTURE_UNAVAILABLE,
} from "../api/fixtures";
import type { FleetResponse } from "../api/types";
import { derivePanelState } from "../components/StatePanel";

function renderPage(location: string, fixture: FleetResponse) {
  return render(<App initialLocation={location} fixtureOverride={fixture} />);
}

/** The fleet table's rows. `tbody tr` on its own also matches the health
 *  chart's legend table, which would inflate every count by four. */
function rows(container: HTMLElement): Element[] {
  return Array.from(container.querySelectorAll("table.data tbody tr"));
}

describe("Fleet page", () => {
  it("renders the fleet with its four clusters", () => {
    const { container } = renderPage("/", FIXTURE_FLEET);
    expect(screen.getByRole("heading", { level: 1, name: "Fleet" })).toBeInTheDocument();
    expect(rows(container)).toHaveLength(4);
    expect(container.querySelector("svg.healthbar")).not.toBeNull();
  });

  it("shows the data-as-of stamp in the local zone (R10.21)", () => {
    renderPage("/", FIXTURE_FLEET);
    const stamp = screen.getByText(/^Data as of /);
    // The title carries the UTC instant (R10.21's hover form) while the visible
    // text is the local rendering. Read from meta.generatedAt, so it is the
    // contract's timestamp and not one the page made up.
    expect(stamp.getAttribute("title")).toBe(`${FIXTURE_GENERATED_AT} UTC`);
  });

  it("renders the empty state by name", () => {
    renderPage("/", FIXTURE_EMPTY);
    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(screen.getByText("No clusters yet")).toBeInTheDocument();
  });

  it("renders the partial state as a banner over the rows it has (R10.22)", () => {
    const { container } = renderPage("/", FIXTURE_PARTIAL);
    expect(screen.getByText("Partial results")).toBeInTheDocument();
    expect(rows(container)).toHaveLength(3);
  });

  it("counts the rows it rendered in the banner, not meta.total (R10.22)", () => {
    // Truncated *and* partial: four clusters match, this page serves two, and
    // the banner has to say "2 clusters served" over a two-row table. The
    // pre-fix code interpolated meta.total and rendered "4 clusters served"
    // above two rows — the pair this fixture exists to catch.
    const { container } = renderPage("/", FIXTURE_TRUNCATED_PARTIAL);
    expect(screen.getByText("Partial results")).toBeInTheDocument();
    expect(screen.getByText(/^2 clusters served/)).toBeInTheDocument();
    expect(screen.queryByText(/4 clusters served/)).toBeNull();
    expect(rows(container)).toHaveLength(2);
  });

  it("renders the unavailable state when no metrics source exists", () => {
    const { container } = renderPage("/", FIXTURE_UNAVAILABLE);
    expect(screen.getByText("Metrics unavailable")).toBeInTheDocument();
    expect(rows(container)).toHaveLength(4);
  });

  it("applies a URL search filter to the table", () => {
    const { container } = renderPage("/?q=freeidp", FIXTURE_FLEET);
    expect(rows(container)).toHaveLength(1);
    expect(screen.getByText("1 shown")).toBeInTheDocument();
  });

  it("applies a URL health filter to the table", () => {
    const { container } = renderPage("/?health=healthy&sort=name&dir=asc", FIXTURE_FLEET);
    const found = rows(container);
    expect(found).toHaveLength(3);
    expect(found.map((r) => r.textContent).join(" ")).not.toContain("freeidp-pg");
  });

  it("mounts the column menu on the table it controls (R10.20)", () => {
    // ColumnMenu.test.tsx tests the menu; this is the assertion that the Fleet
    // page *renders* one. A3's S22 was exactly that gap — the menu existed and
    // was green while Fleet rendered no instance of it, so DEFAULTS.fleet was
    // documented and unreachable. The count is the menu's own readout of the
    // selection it hands ClusterTable, so it pins the wiring, not just the tag.
    const { container } = renderPage("/", FIXTURE_FLEET);
    const menu = container.querySelector(".card-actions .column-menu");
    expect(menu).not.toBeNull();
    expect(within(menu as HTMLElement).getByRole("button", { name: /Columns/ }).textContent).toBe(
      `Columns${COLUMNS.length}/${COLUMNS.length}`,
    );
  });

  it("renders the density toggle and puts the choice on the table (R10.18)", () => {
    // The same shape as the column-menu assertion above: the toggle existed in
    // `VirtualList` and was mounted on Queries only, while R10.18 names *every*
    // table over 100 rows. The wiring is the thing being tested — the control,
    // the attribute the CSS reads, and the inline height the window math uses.
    const { container } = renderPage("/", FIXTURE_FLEET);
    const toggle = container.querySelector(".card-actions .segmented");
    expect(toggle).not.toBeNull();
    expect(container.querySelector("table.data")?.getAttribute("data-density")).toBe("compact");
    expect(container.querySelector("table.data tbody tr")?.getAttribute("style")).toContain(
      `height: ${ROW_HEIGHT.compact}px`,
    );
    fireEvent.click(within(toggle as HTMLElement).getByRole("button", { name: "Comfortable" }));
    expect(container.querySelector("table.data")?.getAttribute("data-density")).toBe("comfortable");
    expect(container.querySelector("table.data tbody tr")?.getAttribute("style")).toContain(
      `height: ${ROW_HEIGHT.comfortable}px`,
    );
  });

  it("windows the table with no spacers when every row fits (R10.18)", () => {
    // Four rows in a 640px viewport is a window that covers the whole set, so the
    // correct render is four rows and zero spacers. The spacer arithmetic itself
    // is `VirtualList.test.tsx`'s; this is the assertion that the Fleet table went
    // through the primitive instead of mapping `rows` directly.
    const { container } = renderPage("/", FIXTURE_FLEET);
    expect(container.querySelectorAll("table.data tbody tr.virtual-spacer")).toHaveLength(0);
    expect(rows(container)).toHaveLength(4);
  });
});

describe("Cluster detail page", () => {
  it("renders a cluster from the URL", () => {
    const { container } = renderPage("/c/freeidp/freeidp-pg", FIXTURE_FLEET);
    expect(screen.getByRole("heading", { level: 1, name: /freeidp-pg/ })).toBeInTheDocument();
    // R1.5 renders CNPG's phase verbatim, and the header appends status.phaseReason
    // after it — so the two share one element and the phase has to be matched as a
    // prefix, not as the element's whole text.
    expect(container.querySelector(".page-meta")?.textContent).toContain("Not enough disk space");
    expect(container.querySelectorAll("nav.tabs button")).toHaveLength(6);
  });

  it("switches tabs through the URL", () => {
    const { container } = renderPage("/c/ai-api-gateway/rev-proxy-pg?tab=storage", FIXTURE_FLEET);
    expect(screen.getByRole("heading", { level: 2, name: "Storage" })).toBeInTheDocument();
    // The PVC names come from the contract (`rev-proxy-pg-1`, `-2`), not from a
    // name the page builds off the cluster.
    expect(container.textContent).toContain("rev-proxy-pg-1");
    expect(container.textContent).toContain("10Gi");
  });

  it("shows the pooler config for the one cluster that has one", () => {
    renderPage("/c/ai-api-gateway/rev-proxy-pg?tab=config", FIXTURE_FLEET);
    // The Pooler's name is also its Service's name (CNPG names them the same),
    // so the string appears on two rows; assert the sections instead.
    expect(screen.getByRole("heading", { level: 2, name: "Poolers" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Services" })).toBeInTheDocument();
    // R6's client attribution keys off pool_mode, and R6.2's pool sizing off
    // spec.pgbouncer.parameters — both now render as the contract's own values
    // rather than the three numbers the old type flattened them into.
    expect(screen.getByText("transaction")).toBeInTheDocument();
    expect(screen.getByText("default_pool_size=20, max_client_conn=400, min_pool_size=4")).toBeInTheDocument();
  });

  it("degrades to a card for a cluster id that is not in the fleet", () => {
    renderPage("/c/nope/nope-pg", FIXTURE_FLEET);
    expect(screen.getByText("Unknown cluster")).toBeInTheDocument();
  });
});

describe("derivePanelState", () => {
  it("matches what the rendered pages show", () => {
    expect(derivePanelState({ phase: "loading", fleet: null })).toBe("loading");
    expect(derivePanelState({ phase: "ready", fleet: FIXTURE_FLEET })).toBe("unavailable");
    expect(derivePanelState({ phase: "ready", fleet: FIXTURE_EMPTY })).toBe("empty");
    expect(derivePanelState({ phase: "ready", fleet: FIXTURE_PARTIAL })).toBe("partial");
    // Truncation alone is paging; truncation *with* a loading namespace is the
    // second partial case, and it still keys off partialBanner, not off
    // `truncated`.
    expect(derivePanelState({ phase: "ready", fleet: FIXTURE_TRUNCATED_PARTIAL })).toBe("partial");
    expect(derivePanelState({ phase: "error", fleet: null, error: "boom" })).toBe("error");
    // R10.22: a failed refresh over a good frame keeps the frame.
    expect(derivePanelState({ phase: "error", fleet: FIXTURE_FLEET, error: "boom" })).toBe("partial");
  });
});
