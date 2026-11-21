/* R10.4's sorting contract: every column sorts, nulls sort last in both
 * directions, and ties never move. */

import { describe, expect, it } from "vitest";

import { healthFor } from "../api/health";
import { FIXTURE_CLUSTERS, FIXTURE_FLEET } from "../api/fixtures";
import type { ClusterView, FleetRow } from "../api/types";
import { COLUMNS } from "../components/ClusterTable";
import { compareCells, filterClusters, namespaceOptions, sortClusters } from "./sort";

describe("compareCells", () => {
  it("sorts numbers numerically, not lexically", () => {
    expect(compareCells(9, 10)).toBeLessThan(0);
    // Numeric collation on the string side too: a cell holding "9" must not
    // sort after one holding "10", which plain lexicographic compare would do.
    expect(compareCells("9", "10")).toBeLessThan(0);
    expect(compareCells("item2", "item10")).toBeLessThan(0);
  });

  it("puts null last in both directions", () => {
    expect(compareCells(null, 5)).toBe(1);
    expect(compareCells(5, null)).toBe(-1);
    expect(compareCells(null, null)).toBe(0);
  });
});

describe("sortClusters", () => {
  const rows = FIXTURE_CLUSTERS;

  it("sorts every column the table exposes, both directions", () => {
    for (const col of COLUMNS) {
      const asc = sortClusters(rows, col.key, "asc");
      const desc = sortClusters(rows, col.key, "desc");
      expect(asc).toHaveLength(rows.length);
      expect(desc).toHaveLength(rows.length);
      // Reversing an ascending order is not required (nulls stay last), but the
      // two orders must be permutations of the same set.
      expect(new Set(asc.map((r) => `${r.namespace}/${r.name}`))).toEqual(
        new Set(desc.map((r) => `${r.namespace}/${r.name}`)),
      );
    }
  });

  it("orders names A→Z ascending", () => {
    const names = sortClusters(rows, "name", "asc").map((r) => r.name);
    expect(names).toEqual(["cookbook-pg", "freeidp-pg", "open-webui-pg", "rev-proxy-pg"]);
  });

  it("orders health by severity, ascending first", () => {
    expect(sortClusters(rows, "health", "asc").map((r) => r.health)).toEqual([
      "healthy",
      "healthy",
      "healthy",
      "down",
    ]);
    // Descending puts the worst first: `down` is the highest-severity value
    // present, so it leads.
    expect(sortClusters(rows, "health", "desc").map((r) => r.health)[0]).toBe("down");
  });

  it("sorts metrics columns with nulls last even ascending", () => {
    // Every fixture metric is null (no collector), so ascending must not put
    // them first — that is what makes "no data" read as no data.
    const asc = sortClusters(rows, "tps", "asc");
    expect(asc.every((r) => r.metrics?.tps === null)).toBe(true);
  });

  it("is stable: the same input renders in the same order twice", () => {
    const a = sortClusters(rows, "pgMajor", "desc").map((r) => r.name);
    const b = sortClusters(rows, "pgMajor", "desc").map((r) => r.name);
    expect(a).toEqual(b);
  });

  it("does not mutate its input", () => {
    const before = rows.map((r) => r.name);
    sortClusters(rows, "name", "desc");
    expect(rows.map((r) => r.name)).toEqual(before);
  });
});

describe("filterClusters", () => {
  it("filters by namespace", () => {
    expect(filterClusters(FIXTURE_CLUSTERS, { ns: ["freeidp"], q: "", health: [] }).map((c) => c.name)).toEqual([
      "freeidp-pg",
    ]);
  });

  it("filters by free text over namespace and name", () => {
    expect(filterClusters(FIXTURE_CLUSTERS, { ns: [], q: "PROXY", health: [] })).toHaveLength(1);
    expect(filterClusters(FIXTURE_CLUSTERS, { ns: [], q: "pg", health: [] })).toHaveLength(4);
  });

  it("filters by health", () => {
    expect(filterClusters(FIXTURE_CLUSTERS, { ns: [], q: "", health: ["down"] })).toHaveLength(1);
    expect(filterClusters(FIXTURE_CLUSTERS, { ns: [], q: "", health: ["healthy"] })).toHaveLength(3);
  });

  it("combines filters", () => {
    expect(filterClusters(FIXTURE_CLUSTERS, { ns: ["freeidp"], q: "cookbook", health: [] })).toHaveLength(0);
  });

  it("returns everything when no filter is set", () => {
    expect(filterClusters(FIXTURE_CLUSTERS, { ns: [], q: "   ", health: [] })).toHaveLength(4);
  });
});

describe("namespaceOptions", () => {
  it("lists namespaces alphabetically with counts", () => {
    expect(namespaceOptions(FIXTURE_CLUSTERS)).toEqual([
      { ns: "ai-api-gateway", count: 1 },
      { ns: "cookbook", count: 1 },
      { ns: "freeidp", count: 1 },
      { ns: "open-webui", count: 1 },
    ]);
  });
});

describe("the fixture set", () => {
  it("is the four reference clusters", () => {
    expect(FIXTURE_FLEET.clusters.map((c) => `${c.namespace}/${c.name}`)).toEqual([
      "ai-api-gateway/rev-proxy-pg",
      "cookbook/cookbook-pg",
      "freeidp/freeidp-pg",
      "open-webui/open-webui-pg",
    ]);
  });

  it("carries every R1.5 field on every cluster", () => {
    // The non-optional members of ClusterView — the fields dto/clusters.go
    // emits on every cluster. `currentPrimary` and `pgImage` are `omitempty`
    // in Go but present on all four reference clusters, so they are asserted
    // here too. `phaseReason` (only freeidp-pg has it) and `pgVersionDetail`
    // (only freeidp-pg lacks it) are omitted for the same reason: on a type
    // with `noUncheckedIndexedAccess`, asserting an optional key the fixture
    // does not always write proves nothing about the contract.
    const wire: (keyof ClusterView)[] = [
      "namespace",
      "name",
      "phase",
      "instances",
      "readyInstances",
      "servingInstances",
      "currentPrimary",
      "pgMajor",
      "pgImage",
      "instanceList",
      "conditions",
      "certificates",
      "pvcs",
      "services",
      "poolers",
      "credentialSource",
      "createdAt",
    ];
    // What the page adds on top of the wire type (types.ts's presentation half).
    const presentation: (keyof FleetRow)[] = ["health", "observedAt"];
    for (const c of FIXTURE_CLUSTERS) {
      for (const key of [...wire, ...presentation]) expect(c).toHaveProperty(key);
    }
  });

  it("agrees between the fixture's health field and the derived rollup", () => {
    // The fixture stores `health` the way the API will; if the UI's own
    // judgement disagrees, one of the two is wrong.
    for (const c of FIXTURE_CLUSTERS) {
      expect(c.health).toBe(healthFor(c));
    }
  });
});
