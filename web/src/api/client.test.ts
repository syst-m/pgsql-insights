/* The source switch (fixture vs live) and the trust boundary on a payload. */

import { describe, expect, it } from "vitest";

import { FIXTURE_EMPTY, FIXTURE_FLEET, FIXTURE_PARTIAL, FIXTURE_UNAVAILABLE, FIXTURE_CLUSTERS } from "./fixtures";
import { FIXTURES, normalizeFleet, resolveConfig, type FixtureName } from "./client";
import type { ClusterSummary, FleetResponse } from "./types";

describe("resolveConfig", () => {
  it("defaults to the fixture while the Phase 1a endpoint is unmerged", () => {
    expect(resolveConfig("", {}).source).toBe("fixture");
  });

  it("honours ?fixture=0 to force the live client", () => {
    expect(resolveConfig("?fixture=0", {}).source).toBe("http");
    expect(resolveConfig("?fixture=false", {}).source).toBe("http");
  });

  it("honours ?fixture=1 to force the fixture", () => {
    expect(resolveConfig("?fixture=1", { VITE_PGSI_FIXTURE: "0" }).source).toBe("fixture");
  });

  it("reads VITE_PGSI_FIXTURE=0 as the build-time switch", () => {
    expect(resolveConfig("", { VITE_PGSI_FIXTURE: "0" }).source).toBe("http");
  });

  it("keeps an API base for the http source and defaults it to same-origin", () => {
    expect(resolveConfig("?fixture=0", { VITE_PGSI_API_BASE: "http://localhost:8080" }).apiBase).toBe(
      "http://localhost:8080",
    );
    expect(resolveConfig("", {}).apiBase).toBe("");
  });

  it("names a fixture in the URL so every R10.12 state has a stable URL", () => {
    const expected: Record<FixtureName, FleetResponse> = {
      fleet: FIXTURE_FLEET,
      empty: FIXTURE_EMPTY,
      partial: FIXTURE_PARTIAL,
      unavailable: FIXTURE_UNAVAILABLE,
    };
    for (const name of ["fleet", "empty", "partial", "unavailable"] as const) {
      const cfg = resolveConfig(`?fixture=${name}`, {});
      expect(cfg.source).toBe("fixture");
      expect(cfg.fixture).toBe(name);
      // The named config resolves to the payload the state panel renders for it.
      expect(FIXTURES[name]).toBe(expected[name]);
    }
  });

  it("ignores an unknown fixture name and falls back to the default", () => {
    expect(resolveConfig("?fixture=banana", {}).fixture).toBe("fleet");
  });

  it("reads ?delay as the fixture's latency, clamped to a sane range", () => {
    expect(resolveConfig("", {}).delayMs).toBe(60);
    expect(resolveConfig("?delay=5000", {}).delayMs).toBe(5000);
    expect(resolveConfig("?delay=-3", {}).delayMs).toBe(60);
    expect(resolveConfig("?delay=999999", {}).delayMs).toBe(60_000);
    expect(resolveConfig("?delay=soon", {}).delayMs).toBe(60);
  });
});

describe("normalizeFleet", () => {
  it("passes a full payload through unchanged", () => {
    expect(normalizeFleet(FIXTURE_FLEET)).toEqual(FIXTURE_FLEET);
  });

  it("fills missing arrays rather than blanking the page", () => {
    // An older Phase 1a binary that omits `poolers`/`conditions` must still
    // render, not throw in every row.
    const thin = {
      generatedAt: "2026-11-20T11:04:12Z",
      clusters: [{ namespace: "cookbook", name: "cookbook-pg" } as unknown as ClusterSummary],
    } as unknown as FleetResponse;
    const out = normalizeFleet(thin);
    expect(out.clusters[0]?.poolers).toEqual([]);
    expect(out.clusters[0]?.conditions).toEqual([]);
    expect(out.clusters[0]?.pvcs).toEqual([]);
    expect(out.clusters[0]?.services).toEqual([]);
    expect(out.clusters[0]?.instancesList).toEqual([]);
    expect(out.partial).toBeNull();
  });

  it("keeps a partial payload's partial info", () => {
    const withPartial = { ...FIXTURE_FLEET, partial: { loadingNamespaces: ["x"], failedNamespaces: [] } };
    expect(normalizeFleet(withPartial).partial?.loadingNamespaces).toEqual(["x"]);
  });

  it("does not mutate the input", () => {
    const before = JSON.stringify(FIXTURE_FLEET);
    normalizeFleet(FIXTURE_FLEET);
    expect(JSON.stringify(FIXTURE_FLEET)).toBe(before);
  });
});

describe("the fixture payload", () => {
  it("is served with the four clusters the contract promises", () => {
    expect(FIXTURE_FLEET.clusters).toHaveLength(FIXTURE_CLUSTERS.length);
    expect(FIXTURE_FLEET.generatedAt).toBe("2026-11-20T11:04:12Z");
  });

  it("marks every cluster's metrics as unconfigured or failed, never invented", () => {
    // Phase 1b ships discovery only: no cluster may show a made-up TPS number.
    for (const c of FIXTURE_CLUSTERS) {
      expect(c.metrics).not.toBeNull();
      expect(["unconfigured", "failed"]).toContain(c.metrics?.source);
      expect(c.metrics?.tps).toBeNull();
      expect(c.metrics?.p95LatencyMs).toBeNull();
    }
  });
});
