/* R10.5(b)'s panel, tested for the one thing 6b owns: the reason there is no chart.
 *
 * A3's ruling gives the graph to Phase 7 and the panel to 6b, so the testable
 * surface here is not a rendering — it is the copy matrix. R5.3's rule ("Never
 * render a blank chart without saying why") is only satisfiable if the sentence
 * is correct per source, so every branch of `COPY` is asserted, and the two
 * branches that mean different things — `null` (not asked) and `"none"` (asked,
 * nothing configured) — are asserted against each other so a future edit cannot
 * collapse them into one string.
 *
 * Plus the two rules that apply to any component here: R10.12's state naming
 * (`data-state="unavailable"`, which is how R16.7's suite keys a screen) and
 * R10.23's untrusted-label rule (the cluster id comes from the URL bar and must
 * be text, never markup).
 */

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ResourceLoadPanel, type OsMetricsSource } from "./ResourceLoadPanel";

const CLUSTER = "open-webui/open-webui-pg";

function panelFor(source: OsMetricsSource | null) {
  const { container } = render(<ResourceLoadPanel cluster={CLUSTER} source={source} />);
  return { container, panel: container.querySelector(".rl-panel") as HTMLElement };
}

describe("ResourceLoadPanel (R10.5b)", () => {
  it("names the slot, the requirement, and the cluster it belongs to", () => {
    const { panel } = panelFor(null);
    expect(panel.querySelector(".card-title")?.textContent).toBe("Resource load");
    expect(panel.querySelector(".card-meta")?.textContent).toBe("R10.5");
    expect(panel.querySelector(".mono")?.textContent).toBe(CLUSTER);
  });

  it("says what the chart will show, so the slot is not anonymous", () => {
    // R10.5(b)'s own words, stated once for every source so the reader knows
    // what is missing rather than only that something is.
    const { panel } = panelFor("none");
    expect(panel.querySelector(".rl-scope")?.textContent).toBe(
      "CPU capacity vs. usage, and memory working set.",
    );
  });

  it("renders a sentence instead of a plot, per R5.3's blank-chart rule", () => {
    // "Never render a blank chart without saying why" — the why is the content,
    // and there is no svg here to be blank.
    const { panel } = panelFor("none");
    expect(panel.querySelector("svg")).toBeNull();
    expect(panel.querySelector(".chart-empty")?.textContent).not.toBe("");
  });

  it("carries R10.12's state name so R16.7's suite can key the panel", () => {
    const { panel } = panelFor(null);
    expect(panel.getAttribute("data-state")).toBe("unavailable");
  });

  it("keeps 'not asked' and 'nothing configured' as two different answers", () => {
    // The R10.13 distinction QueryLoadChart draws for series: the app not
    // asking and the store having nothing are different facts.
    const asked = panelFor(null).panel;
    const configured = panelFor("none").panel;
    const askedHead = asked.querySelector(".chart-empty")?.textContent ?? "";
    const configuredHead = configured.querySelector(".chart-empty")?.textContent ?? "";
    expect(askedHead).toContain("No OS-metrics series has been fetched");
    // The "why" for this state names the missing fetcher, and it lives in the
    // detail paragraph, not the headline: the headline is the fact, the detail
    // is the cause. Asserting each against its own node is what stops a future
    // edit moving the cause into the headline and losing the distinction.
    expect(asked.querySelector(".rl-detail")?.textContent).toContain("client.ts");
    expect(configuredHead).toContain("No OS-metrics source is configured");
    expect(configured.querySelector(".rl-detail")?.textContent).toContain("degraded-with-a-note");
    expect(askedHead).not.toBe(configuredHead);
  });

  it("names R5.3's degraded-with-a-note case in its own words", () => {
    const { panel } = panelFor("none");
    expect(panel.querySelector(".rl-detail")?.textContent).toContain("degraded-with-a-note");
  });

  it("gives the two configured sources two different headlines, both Phase 7's", () => {
    const prom = panelFor("prometheus");
    const ms = panelFor("metrics-server");
    const promHead = prom.panel.querySelector(".chart-empty")?.textContent ?? "";
    const msHead = ms.panel.querySelector(".chart-empty")?.textContent ?? "";
    expect(promHead).toContain("S7 (Prometheus) is configured");
    expect(msHead).toContain("metrics.k8s.io is the only source");
    expect(promHead).not.toBe(msHead);
    expect(prom.panel.textContent).toContain("Phase 7");
    expect(ms.panel.textContent).toContain("Phase 7");
  });

  it("reports both sources in R5.3's order, per source", () => {
    const rowsFor = (source: OsMetricsSource | null) => {
      const { panel } = panelFor(source);
      const dts = Array.from(panel.querySelectorAll(".rl-grid dt")).map((n) => n.textContent);
      const dds = Array.from(panel.querySelectorAll(".rl-grid dd")).map((n) => n.textContent);
      return [dts, dds] as const;
    };
    const [keys, values] = rowsFor("prometheus");
    // S7 first, metrics.k8s.io second: R5.3's stated precedence, not alphabetical.
    expect(keys).toEqual(["S7 · Prometheus", "metrics.k8s.io"]);
    expect(values).toEqual(["configured", "not consulted — S7 wins"]);
    expect(rowsFor("metrics-server")[1]).toEqual(["not configured", "configured (the fallback)"]);
    expect(rowsFor("none")[1]).toEqual(["not configured", "not configured"]);
    expect(rowsFor(null)[1]).toEqual(["not queried", "not queried"]);
  });

  it("renders the cluster id as text, never markup (R10.23)", () => {
    const { container } = render(<ResourceLoadPanel cluster={'<img src="x">'} source={null} />);
    const cluster = container.querySelector(".rl-cluster") as HTMLElement;
    expect(cluster.querySelector("img")).toBeNull();
    expect(cluster.textContent).toBe('Cluster <img src="x">');
  });

  it("renders the null and empty cluster ids as the house dash", () => {
    for (const cluster of [null, ""]) {
      const { container } = render(<ResourceLoadPanel cluster={cluster} source={null} />);
      expect(container.querySelector(".mono")?.textContent).toBe("—");
    }
  });
});
