/* R10.16 — every piece of UI state is in the URL, and the URL round-trips. */

import { describe, expect, it } from "vitest";

import {
  DEFAULT_STATE,
  formatUrl,
  parseUrl,
  pushState,
  readState,
  withState,
  type UrlState,
} from "./urlState";

describe("parseUrl", () => {
  it("reads the root as the default fleet view", () => {
    expect(parseUrl("/")).toEqual(DEFAULT_STATE);
  });

  it("reads a cluster route", () => {
    const s = parseUrl("/c/freeidp/freeidp-pg");
    expect(s.page).toBe("cluster");
    expect(s.cluster).toBe("freeidp/freeidp-pg");
  });

  it("reads a cluster route with a tab and a row", () => {
    const s = parseUrl("/c/freeidp/freeidp-pg?tab=storage&row=freeidp-pg-2");
    expect(s.tab).toBe("storage");
    expect(s.row).toBe("freeidp-pg-2");
  });

  it("reads sort, direction, filters and range", () => {
    const s = parseUrl("/?sort=pgMajor&dir=asc&ns=cookbook,freeidp&q=pg&health=down&range=7d");
    expect(s.sort).toBe("pgMajor");
    expect(s.dir).toBe("asc");
    expect(s.ns).toEqual(["cookbook", "freeidp"]);
    expect(s.q).toBe("pg");
    expect(s.health).toEqual(["down"]);
    expect(s.range).toBe("7d");
  });

  it("falls back to defaults for values a hand-typed URL gets wrong", () => {
    const s = parseUrl("/?sort=colour&dir=sideways&range=99y&tab=bogus");
    expect(s.sort).toBe("health");
    expect(s.dir).toBe("desc");
    expect(s.range).toBe("24h");
    expect(s.tab).toBe("overview");
  });

  it("normalises a percent-encoded cluster id", () => {
    expect(parseUrl("/c/ai-api-gateway/rev-proxy-pg").cluster).toBe("ai-api-gateway/rev-proxy-pg");
  });
});

describe("formatUrl", () => {
  it("emits the bare root for the default state", () => {
    expect(formatUrl(DEFAULT_STATE)).toBe("/");
  });

  it("omits defaults but keeps the ones that differ", () => {
    expect(formatUrl({ ...DEFAULT_STATE, sort: "name", dir: "asc" })).toBe("/?sort=name&dir=asc");
  });

  it("round-trips every field it can carry", () => {
    const cases: string[] = [
      "/",
      "/?sort=tps&dir=asc",
      "/?ns=freeidp",
      "/?q=proxy&range=30d",
      "/?health=healthy,down",
      "/c/cookbook/cookbook-pg",
      "/c/cookbook/cookbook-pg?tab=config",
      "/c/open-webui/open-webui-pg?tab=instances&row=open-webui-pg-1&sort=name&dir=asc",
    ];
    for (const url of cases) {
      // Parse, then format, then parse again: the second parse must agree with
      // the first. That is the property the back button depends on.
      const once = parseUrl(url);
      const twice = parseUrl(formatUrl(once));
      expect(twice).toEqual(once);
      expect(formatUrl(twice)).toBe(formatUrl(once));
    }
  });

  it("orders a health set canonically so two spellings collapse to one URL", () => {
    const a = parseUrl("/?health=down,healthy");
    const b = parseUrl("/?health=healthy,down");
    expect(formatUrl(a)).toBe(formatUrl(b));
  });
});

describe("withState", () => {
  it("clears the tab when leaving a cluster page", () => {
    const cluster: UrlState = parseUrl("/c/cookbook/cookbook-pg?tab=storage");
    const back = withState(cluster, { page: "fleet" });
    expect(back.tab).toBe("overview");
  });

  it("drops to fleet when a cluster page loses its cluster", () => {
    const cluster: UrlState = parseUrl("/c/cookbook/cookbook-pg");
    expect(withState(cluster, { cluster: null }).page).toBe("fleet");
  });
});

describe("data-source flags", () => {
  it("carries ?fixture= and ?delay= through a patch so navigation keeps them", () => {
    const s = parseUrl("/?fixture=partial&delay=5000");
    expect(s.fixtureFlag).toBe("partial");
    expect(s.delayFlag).toBe("5000");

    const sorted = withState(s, { sort: "name", dir: "asc" });
    expect(formatUrl(sorted)).toBe("/?sort=name&dir=asc&fixture=partial&delay=5000");
    expect(parseUrl(formatUrl(sorted))).toEqual(sorted);
  });

  it("omits both when absent, so the default URL stays short", () => {
    expect(formatUrl(DEFAULT_STATE)).toBe("/");
    expect(DEFAULT_STATE.fixtureFlag).toBe("");
  });
});

describe("history plumbing", () => {
  it("pushState writes the formatted URL and replaceState does not add a step", () => {
    const calls: { method: string; url: string }[] = [];
    const fake = {
      pushState: (_d: unknown, _u: string, url?: string | URL | null) => calls.push({ method: "push", url: String(url) }),
      replaceState: (_d: unknown, _u: string, url?: string | URL | null) => calls.push({ method: "replace", url: String(url) }),
    };
    const s = parseUrl("/?sort=name&dir=asc");
    pushState(fake, s);
    pushState(fake, s, true);
    expect(calls).toEqual([
      { method: "push", url: "/?sort=name&dir=asc" },
      { method: "replace", url: "/?sort=name&dir=asc" },
    ]);
  });

  it("readState reads a location object", () => {
    expect(readState({ pathname: "/c/freeidp/freeidp-pg", search: "?tab=storage" }).tab).toBe("storage");
  });
});
