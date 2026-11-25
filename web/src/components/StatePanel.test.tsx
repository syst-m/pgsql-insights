/* R10.12's state machine, tested as the function and the component it is.
 *
 * Two page tests already reach parts of this file: `Fleet.test.tsx` has a
 * `describe("derivePanelState")` block covering the six fleet cases, and
 * `Queries.test.tsx` asserts the copy tables through its own renders. What
 * neither reaches is the rest of the module — the half that has no page to
 * render it:
 *
 *   - `deriveQueryPanelState` — the Queries page's rule, whose interesting
 *     branch is the ambiguity its own docblock describes (an empty payload is
 *     R10.27's not-installed screen *or* R10.12's plain empty, and `undefined`
 *     is neither). The page test renders the two outcomes but never the
 *     `extensionInstalled: true` case, and never the rule that a *populated*
 *     payload cannot be not-installed.
 *   - `hasLiveMetrics` — the predicate the fleet's rule ends with, exercised by
 *     exactly one fixture and never as a function.
 *   - `copyFor` — the table swap and R10.22's composed body. A page render
 *     proves the copy for the one state that render happens to be in; this
 *     walks every state in both tables.
 *   - `StatePanel` — the markup per state, including the error-string
 *     interpolation and the action button, which no page test clicks.
 *   - `STATE_FIXTURES` — R16.7's screenshot set, whose claim is that the key
 *     and the derived state agree. Asserting that is the cheapest way to keep
 *     the visual suite's six screens from drifting away from the code that
 *     picks them.
 *
 * The expectation style follows the file under test: pure functions are called
 * directly, and only `StatePanel` itself goes through RTL.
 */

import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import {
  FLEET_COPY,
  QUERY_COPY,
  STATE_FIXTURES,
  StatePanel,
  copyFor,
  derivePanelState,
  deriveQueryPanelState,
  hasLiveMetrics,
  type PanelState,
} from "./StatePanel";
import { emptyEnvelope, partialBanner, toQueriesResponse } from "../api/envelope";
import {
  FIXTURE_CLUSTERS,
  FIXTURE_GENERATED_AT,
  FIXTURE_QUERIES,
  FIXTURE_TRUNCATED_PARTIAL,
} from "../api/fixtures";
import type { ClusterMetrics, FleetRow, QueryResponse, QueryView } from "../api/types";

/** Every state the copy tables name — derived from the table so a state added
 *  to `FLEET_COPY` is covered here without editing this list. */
const NAMED_STATES = Object.keys(FLEET_COPY) as Exclude<PanelState, "ready">[];

const EMPTY_QUERIES: QueryResponse = toQueriesResponse(
  emptyEnvelope<QueryView>(FIXTURE_GENERATED_AT),
);
const POPULATED_QUERIES: QueryResponse = toQueriesResponse(FIXTURE_QUERIES);
const FIRST_ROW = FIXTURE_CLUSTERS[0] as FleetRow;

describe("deriveQueryPanelState", () => {
  it("follows the fleet's rule for the phases the payload cannot answer for", () => {
    // The four phase-first cases are the same rule in both functions, and the
    // duplication is deliberate (the docblock says why); asserting them here is
    // what stops the two rules from drifting one edit apart.
    expect(deriveQueryPanelState({ phase: "idle", queries: null })).toBe("loading");
    expect(deriveQueryPanelState({ phase: "loading", queries: null })).toBe("loading");
    expect(deriveQueryPanelState({ phase: "error", queries: null })).toBe("error");
    // R10.22: a failed refresh over a good payload keeps the payload.
    expect(deriveQueryPanelState({ phase: "error", queries: POPULATED_QUERIES })).toBe("partial");
    // A background refetch keeps the previous frame visible, not a skeleton.
    expect(deriveQueryPanelState({ phase: "loading", queries: POPULATED_QUERIES })).toBe("partial");
    // `ready` with no payload yet is the state between the two, and it is
    // `loading` rather than `empty` — a page that has not asked yet has no
    // answer to be empty of.
    expect(deriveQueryPanelState({ phase: "ready", queries: null })).toBe("loading");
  });

  it("resolves the empty payload's ambiguity from extensionInstalled", () => {
    // The three readings of `extensionInstalled` on a zero-row payload, which is
    // the whole reason the prop exists: `undefined` is *unknown* (every response
    // today, because `dto/clusters.go` does not expose R3.1's presence) and
    // renders R10.12's plain empty; `false` is R10.27's screen; `true` is
    // installed-and-quiet, which is also the plain empty.
    expect(deriveQueryPanelState({ phase: "ready", queries: EMPTY_QUERIES })).toBe("empty");
    expect(
      deriveQueryPanelState({ phase: "ready", queries: EMPTY_QUERIES, extensionInstalled: false }),
    ).toBe("unavailable");
    expect(
      deriveQueryPanelState({ phase: "ready", queries: EMPTY_QUERIES, extensionInstalled: true }),
    ).toBe("empty");
  });

  it("never reads a populated payload as the R10.27 screen", () => {
    // The rows are the proof the extension is there, so the check sits *inside*
    // the zero-row branch. A caller who passes `extensionInstalled: false`
    // alongside a populated payload — a stale prop, a race between two fetches —
    // gets `ready`, not the not-installed screen.
    expect(
      deriveQueryPanelState({ phase: "ready", queries: POPULATED_QUERIES, extensionInstalled: false }),
    ).toBe("ready");
  });

  it("takes R10.22's partial from the payload's meta, not from a partial key", () => {
    // The API has no `partial` field; the banner is derived from
    // meta.failed / meta.loading. A query payload with a failed namespace is
    // therefore partial even though its rows are complete for what synced.
    // Spread, not re-typed: the rows and R3.1's presence report are the fixture's
    // and only `meta` differs here — the form the truncation case at `:130` uses.
    // Naming `queries` and `pgStatStatements` by hand would be a second copy of
    // the contract, and the copy that goes stale when the fixture is regenerated.
    const failed: QueryResponse = {
      ...POPULATED_QUERIES,
      meta: {
        total: 40,
        truncated: false,
        generatedAt: FIXTURE_GENERATED_AT,
        failed: [{ namespace: "analytics", code: "Unavailable", message: "watch errored" }],
      },
    };
    expect(deriveQueryPanelState({ phase: "ready", queries: failed })).toBe("partial");
    // Truncation alone is paging (R8.10), so the same rows with a truncated,
    // failure-free meta are `ready`.
    expect(
      deriveQueryPanelState({
        phase: "ready",
        queries: { ...POPULATED_QUERIES, meta: { ...POPULATED_QUERIES.meta, truncated: true } },
      }),
    ).toBe("ready");
  });
});

describe("hasLiveMetrics", () => {
  /** The first fixture row with its `metrics` replaced. */
  function withMetrics(metrics: ClusterMetrics | null | undefined): FleetRow {
    return { ...FIRST_ROW, metrics };
  }

  const source = (s: ClusterMetrics["source"]): ClusterMetrics => ({
    tps: 12.4,
    p95LatencyMs: 41,
    connectionsPct: 22,
    diskPct: 61,
    source: s,
    collectedAt: "2026-11-23T09:00:00Z",
  });

  it("counts only the sources that produce values", () => {
    // The two live sources are the only two that count; `unconfigured` and
    // `failed` both mean "there are no numbers here", which is the R10.12
    // distinction the field exists to make.
    expect(hasLiveMetrics(withMetrics(source("pg_stat_statements")))).toBe(true);
    expect(hasLiveMetrics(withMetrics(source("cnpg_metrics")))).toBe(true);
    expect(hasLiveMetrics(withMetrics(source("unconfigured")))).toBe(false);
    expect(hasLiveMetrics(withMetrics(source("failed")))).toBe(false);
    // Phase 1a's contract has no metrics at all, and an absent key is exactly as
    // answerless as an explicit null.
    expect(hasLiveMetrics(withMetrics(null))).toBe(false);
    expect(hasLiveMetrics({ ...FIRST_ROW, metrics: undefined })).toBe(false);
  });
});

describe("copyFor", () => {
  it("renders nothing for ready", () => {
    // `ready` is not a state with copy, it is the absence of one — which is how
    // `StatePanel` and the pages agree that a populated table gets no banner.
    expect(copyFor("ready")).toBeNull();
    expect(copyFor("ready", undefined, QUERY_COPY)).toBeNull();
  });

  it("returns the named table's entry for every other state", () => {
    // The `table` parameter is the whole mechanism that lets Queries render
    // query nouns and Fleet render cluster nouns from one component. Asserting
    // it per state — rather than the two strings one page test happens to
    // render — is what keeps a new state from defaulting to the fleet's voice.
    for (const state of NAMED_STATES) {
      expect(copyFor(state, undefined, FLEET_COPY)).toEqual(FLEET_COPY[state]);
      expect(copyFor(state, undefined, QUERY_COPY)).toEqual(QUERY_COPY[state]);
    }
    // The two tables are genuinely different tables, with one deliberate
    // exception worth naming: `partial` is the same sentence in both, because
    // R10.22's banner is composed from the payload anyway (next test) and the
    // static string is only the fallback when there is no payload to compose it
    // from. Every other pair differs, so a page that passes the wrong table is
    // visibly wrong in four of five states.
    const differing = NAMED_STATES.filter((s) => FLEET_COPY[s].title !== QUERY_COPY[s].title);
    expect(differing).toEqual(["loading", "empty", "unavailable", "error"]);
  });

  it("swaps R10.22's composed body in for the partial state only", () => {
    // "3 of 4 clusters shown, and it says so" cannot be a constant: the page has
    // to name the fourth namespace. `copyFor` composes it from the response and
    // keeps the table's title.
    const source = FIXTURE_TRUNCATED_PARTIAL;
    const composed = partialBanner(source);
    expect(composed).not.toBeNull();
    const copy = copyFor("partial", source, FLEET_COPY);
    expect(copy?.title).toBe(FLEET_COPY.partial.title);
    expect(copy?.body).toBe(composed);
    expect(copy?.body).toContain("2 clusters served");
    // The source is ignored for every other state — an empty payload has no
    // banner to compose, and interpolating one there is how a page ends up
    // saying "2 clusters served" above a table with no rows.
    expect(copyFor("empty", source, FLEET_COPY)).toEqual(FLEET_COPY.empty);
  });
});

describe("StatePanel", () => {
  it("renders each named state as a live region with the table's title and body", () => {
    for (const state of NAMED_STATES) {
      const { container } = render(<StatePanel state={state} />);
      const section = container.querySelector(`section.state-panel[data-state="${state}"]`);
      expect(section).not.toBeNull();
      expect(section?.getAttribute("role")).toBe("status");
      expect(section?.getAttribute("aria-live")).toBe("polite");
      expect(section?.querySelector(".state-title")?.textContent).toBe(FLEET_COPY[state].title);
      expect(section?.querySelector(".state-body")?.textContent).toBe(FLEET_COPY[state].body);
    }
  });

  it("appends the error detail to the error body", () => {
    // R10.12's named state plus R8.5's detail: the copy explains the class, the
    // `error` prop carries this occurrence. Only the error state interpolates
    // it — every other state renders its body verbatim, so a stray `error` on a
    // partial render cannot leak into the banner.
    const detail = "503 Unavailable from GET /api/v1/clusters";
    const { container } = render(<StatePanel state="error" error={detail} />);
    expect(container.querySelector(".state-body")?.textContent).toBe(`${FLEET_COPY.error.body} ${detail}`);
    // Without one, the sentence stands alone.
    const bare = render(<StatePanel state="error" />);
    expect(bare.container.querySelector(".state-body")?.textContent).toBe(FLEET_COPY.error.body);
  });

  it("renders the action only where the copy has one, and wires it", () => {
    const onAction = vi.fn();
    for (const state of NAMED_STATES) {
      const { container } = render(<StatePanel state={state} onAction={onAction} />);
      const buttons = Array.from(container.querySelectorAll("button.btn"));
      const action = FLEET_COPY[state].action;
      expect(buttons).toHaveLength(action === undefined ? 0 : 1);
      if (action !== undefined) expect(buttons[0]?.textContent).toBe(action);
    }
    // The one the pages actually rely on: the error state's Retry.
    const error = render(<StatePanel state="error" onAction={onAction} />);
    fireEvent.click(error.container.querySelector("button.btn") as HTMLElement);
    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it("renders nothing for ready", () => {
    // The pages render `<StatePanel>` unconditionally and let it decide, which
    // only works if `ready` is a null render rather than an empty section that
    // the CSS would still give a border.
    const { container } = render(<StatePanel state="ready" />);
    expect(container.innerHTML).toBe("");
  });
});

describe("STATE_FIXTURES", () => {
  // The map's keys are the three payload-bearing states R16.7 screenshots;
  // `loading` and `error` have no payload to key a fixture on — they are
  // `PanelInput`'s `phase`, which is why the map's type names exactly these
  // three. The test derives the key list from the map itself, so widening the
  // type adds a case here without an edit to this file.

  it("files each fixture under the state the derivation picks for it", () => {
    // The claim R16.7 depends on: the suite iterates this map instead of
    // inventing data, so a fixture that stops deriving to its own key is a
    // screenshot of the wrong thing. `Fleet.test.tsx` proves the same pairing
    // through the router; this proves it through the map, which is the half that
    // would break silently if someone repointed `partial` at a different
    // response and left the key alone.
    for (const key of ["empty", "partial", "unavailable"] as const) {
      const fixture = STATE_FIXTURES[key];
      expect(derivePanelState({ phase: "ready", fleet: fixture })).toBe(key);
    }
  });
});
