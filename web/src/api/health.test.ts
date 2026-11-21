/* R1.2's rollup, as the UI computes it.
 *
 * The case that motivated the requirement — a CR reporting 2 instances with
 * zero serving — is a named test here, not just a comment. */

import { describe, expect, it } from "vitest";

import { HEALTH_ORDER, healthFor, presentationFor, type HealthInput } from "./health";
import type { ClusterHealth } from "./types";

const HEALTHY = "Cluster in healthy state";

function input(over: Partial<HealthInput>): HealthInput {
  return { phase: HEALTHY, instances: 2, readyInstances: 2, servingInstances: 2, ...over };
}

describe("healthFor", () => {
  it("calls a full, ready, serving cluster healthy", () => {
    expect(healthFor(input({}))).toBe("healthy");
  });

  it("calls freeidp-pg down: 2 instances reported, 0 serving", () => {
    // The measured case from the reference environment. A collector that
    // trusts `instances` renders this green.
    expect(healthFor(input({ phase: "Not enough disk space", instances: 2, readyInstances: 0, servingInstances: 0 })))
      .toBe("down");
  });

  it("calls a cluster with one serving instance of two degraded", () => {
    expect(healthFor(input({ instances: 3, readyInstances: 2, servingInstances: 2 }))).toBe("degraded");
  });

  it("calls a serving-but-not-ready pod set degraded", () => {
    // serving counts the container; ready counts the pod. A pod can serve
    // while failing its readiness probe.
    expect(healthFor(input({ instances: 2, readyInstances: 1, servingInstances: 2 }))).toBe("degraded");
  });

  it("calls a healthy-count cluster with a problem phase degraded", () => {
    expect(healthFor(input({ phase: "Setting up primary" }))).toBe("degraded");
  });

  it("calls a cluster with no instances unknown rather than healthy or down", () => {
    expect(healthFor(input({ instances: 0, readyInstances: 0, servingInstances: 0 }))).toBe("unknown");
  });

  it("never returns a value outside the enum", () => {
    const cases: HealthInput[] = [
      input({}),
      input({ instances: 1, readyInstances: 1, servingInstances: 1 }),
      input({ instances: 5, readyInstances: 4, servingInstances: 3 }),
      input({ instances: 0, readyInstances: 0, servingInstances: 0 }),
      input({ phase: "Expanding storage" }),
    ];
    for (const c of cases) expect(HEALTH_ORDER).toContain(healthFor(c) satisfies ClusterHealth);
  });
});

describe("presentationFor", () => {
  it("pairs every health with a label, a token and a shape (R10.23)", () => {
    const seen = new Set<string>();
    for (const h of HEALTH_ORDER) {
      const p = presentationFor(h);
      expect(p.label).not.toBe("");
      expect(p.token.startsWith("var(--")).toBe(true);
      seen.add(p.glyph);
    }
    // Four distinct shapes: colour is never the only difference between two
    // states, which is the point of the requirement.
    expect(seen.size).toBe(HEALTH_ORDER.length);
  });

  it("keeps 'unknown' off the status palette", () => {
    // No data is not a state; borrowing a status token would read as an alarm.
    const tokens = ["healthy", "degraded", "down"].map((h) => presentationFor(h as ClusterHealth).token);
    expect(tokens).not.toContain(presentationFor("unknown").token);
  });
});
