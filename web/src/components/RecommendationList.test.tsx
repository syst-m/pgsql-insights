/* R7.6's four fields, rendered as four labelled parts, and the one inference this
 * component makes about a string the API types as `string`.
 *
 * `isFingerprint` is exported and tested because it is an inference, not a
 * lookup: `RecommendationView.evidence` is a flat `string[]`, so the payload
 * never says which entries are fingerprints and which are metric readings, and
 * the only tool available is the shape. Everything that follows from that —
 * which evidence becomes a link — is asserted against the fixture's own
 * fingerprint so the test and the route agree on one value.
 */

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { FIXTURE_QUERY_DETAIL } from "../api/fixtures";
import type { RecommendationView } from "../api/types";
import { buildPath } from "../routes";
import { isFingerprint, RecommendationList } from "./RecommendationList";

const FP = FIXTURE_QUERY_DETAIL.fingerprint;
const NS = FIXTURE_QUERY_DETAIL.namespace;
const CLUSTER = FIXTURE_QUERY_DETAIL.cluster;

/** A recommendation in R7.6's shape: finding, evidence (one fingerprint, one
 *  metric reading), action, confidence. */
const REC: RecommendationView = {
  finding: "Sequential scan on a table with a usable index",
  evidence: [FP, "total_exec_ms = 128410.5"],
  action: "Add an index on users(id) or rewrite the predicate to use users_pkey",
  confidence: "medium",
};

describe("isFingerprint", () => {
  it("accepts the fixture's fingerprint", () => {
    // R3.4's shape is a SHA-256 hex digest; the fixture is the canonical example
    // and the route the link is built for is the one that proves it.
    expect(isFingerprint(FP)).toBe(true);
  });

  it("accepts any 64-character lowercase hex string", () => {
    expect(isFingerprint("0".repeat(64))).toBe(true);
    expect(isFingerprint("a".repeat(64))).toBe(true);
    expect(isFingerprint("0123456789abcdef".repeat(4))).toBe(true);
  });

  it("rejects a 63- and a 65-character string", () => {
    // Length is the whole contract. A truncated hash is not a fingerprint, and
    // linking it would produce a detail page that 404s.
    expect(isFingerprint("a".repeat(63))).toBe(false);
    expect(isFingerprint("a".repeat(65))).toBe(false);
  });

  it("rejects uppercase hex, which is not what the store emits", () => {
    expect(isFingerprint("A".repeat(64))).toBe(false);
  });

  it("rejects a non-hex 64-character string", () => {
    expect(isFingerprint("z".repeat(64))).toBe(false);
  });

  it("rejects the metric-reading form and the empty string", () => {
    expect(isFingerprint("total_exec_ms = 128410.5")).toBe(false);
    expect(isFingerprint("")).toBe(false);
  });
});

describe("RecommendationList", () => {
  it("says the suppression rule when there is nothing to show", () => {
    // The fixture's `recommendations` is `[]`, and R7.6 makes that a meaningful
    // answer: unsupported recommendations are suppressed, not guessed. The
    // sentence has to distinguish "nothing was supportable" from "not run yet",
    // because a reader acts differently on each.
    const { container } = render(
      <RecommendationList recommendations={[]} namespace={NS} cluster={CLUSTER} />,
    );
    const empty = container.querySelector("[data-testid='recommendations-empty']");
    expect(empty?.textContent).toContain("R7.6 suppresses recommendations the data cannot support");
    expect(container.querySelector(".recommendation-list")).toBeNull();
    // No count badge on an empty list: a "0" next to a sentence saying there are
    // none is the same information twice, in two registers.
    expect(container.querySelector(".card-actions")).toBeNull();
  });

  it("renders all four R7.6 fields", () => {
    const { container } = render(
      <RecommendationList recommendations={[REC]} namespace={NS} cluster={CLUSTER} />,
    );
    expect(container.querySelector(".recommendation-finding")?.textContent).toBe(REC.finding);
    expect(container.querySelector(".recommendation-confidence")?.textContent).toBe(REC.confidence);
    expect(container.querySelector(".recommendation-action")?.textContent).toBe(REC.action);
    expect(container.querySelectorAll(".recommendation-evidence li")).toHaveLength(2);
  });

  it("renders confidence as plain text, with no status colour", () => {
    // `dto/queries.go:636` flags that R7.6 never names the confidence vocabulary.
    // A coloured chip would freeze an undefined scale and spend a reserved status
    // colour (R10.23), so the value renders in text ink.
    const { container } = render(
      <RecommendationList recommendations={[REC]} namespace={NS} cluster={CLUSTER} />,
    );
    const el = container.querySelector(".recommendation-confidence");
    expect(el?.className).toBe("recommendation-confidence");
  });

  it("links an evidence fingerprint to that query's detail page", () => {
    // `dto/queries.go:624` says the fingerprints are in the payload *so that* a
    // client can follow them to R8.2's route. Printing one as text and stopping
    // there declines the reason it was sent.
    const { container } = render(
      <RecommendationList recommendations={[REC]} namespace={NS} cluster={CLUSTER} />,
    );
    const link = container.querySelector(".recommendation-evidence a.query-link");
    expect(link?.getAttribute("href")).toBe(buildPath("query-detail", `${NS}/${CLUSTER}`, FP));
    expect(link?.textContent).toBe(FP);
  });

  it("leaves a metric reading as text", () => {
    // The other half of the classification: `total_exec_ms = 128410.5` is a
    // measurement, not an address, and linking it would be a link to nothing.
    const { container } = render(
      <RecommendationList recommendations={[REC]} namespace={NS} cluster={CLUSTER} />,
    );
    const items = container.querySelectorAll(".recommendation-evidence li");
    expect(items[1]?.textContent).toBe("total_exec_ms = 128410.5");
    expect(items[1]?.querySelector("a")).toBeNull();
  });

  it("renders the fingerprint as text when there is no cluster to link within", () => {
    // R3.4 makes a fingerprint canonical *within* a cluster, so without one the
    // address does not exist. The same answer as "not a fingerprint", and cheaper
    // than inventing a default cluster.
    const { container } = render(<RecommendationList recommendations={[REC]} />);
    expect(container.querySelector(".recommendation-evidence a")).toBeNull();
    expect(container.querySelector(".recommendation-evidence li")?.textContent).toBe(FP);
  });

  it("counts the recommendations in the card head when there are any", () => {
    const { container } = render(
      <RecommendationList recommendations={[REC, REC]} namespace={NS} cluster={CLUSTER} />,
    );
    expect(container.querySelectorAll(".recommendation")).toHaveLength(2);
    expect(container.querySelector(".card-actions .card-meta")?.textContent).toBe("2");
  });

  it("keys each item so two identical recommendations both render", () => {
    // R7.6 does not forbid two recommendations with the same finding, and a key
    // of `finding` alone would drop the second one silently.
    const { container } = render(
      <RecommendationList recommendations={[REC, REC]} namespace={NS} cluster={CLUSTER} />,
    );
    expect(container.querySelectorAll(".recommendation")).toHaveLength(2);
  });
});
