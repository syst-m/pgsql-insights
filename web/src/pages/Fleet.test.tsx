/* R10.12's named states, rendered.
 *
 * Each state is reached through the same path the app uses — a URL plus a
 * payload — so the test proves the routing and the state choice together, and
 * R16.7's visual suite screenshots the same six screens. */

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { App } from "../App";
import {
  FIXTURE_EMPTY,
  FIXTURE_FLEET,
  FIXTURE_PARTIAL,
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
    expect(stamp.getAttribute("title")).toBe("2026-11-20T11:04:12Z UTC");
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
});

describe("Cluster detail page", () => {
  it("renders a cluster from the URL", () => {
    const { container } = renderPage("/c/freeidp/freeidp-pg", FIXTURE_FLEET);
    expect(screen.getByRole("heading", { level: 1, name: /freeidp-pg/ })).toBeInTheDocument();
    expect(screen.getByText("Not enough disk space")).toBeInTheDocument();
    expect(container.querySelectorAll("nav.tabs button")).toHaveLength(6);
  });

  it("switches tabs through the URL", () => {
    const { container } = renderPage("/c/ai-api-gateway/rev-proxy-pg?tab=storage", FIXTURE_FLEET);
    expect(screen.getByRole("heading", { level: 2, name: "Storage" })).toBeInTheDocument();
    expect(container.textContent).toContain("streaming-rev-proxy-pg-1");
  });

  it("shows the pooler config for the one cluster that has one", () => {
    renderPage("/c/ai-api-gateway/rev-proxy-pg?tab=config", FIXTURE_FLEET);
    expect(screen.getByText("rev-proxy-pg-pooler")).toBeInTheDocument();
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
    expect(derivePanelState({ phase: "error", fleet: null, error: "boom" })).toBe("error");
    // R10.22: a failed refresh over a good frame keeps the frame.
    expect(derivePanelState({ phase: "error", fleet: FIXTURE_FLEET, error: "boom" })).toBe("partial");
  });
});
