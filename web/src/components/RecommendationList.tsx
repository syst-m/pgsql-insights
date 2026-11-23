/* R10.7's "and recommendations" — R7.6's four fields, rendered in R7.6's order.
 *
 * ── why this is a list of structs and not a list of strings ───────────────
 *
 * R7.6 is the requirement that makes a recommendation a contract: "Every
 * recommendation carries: the finding, the evidence (metrics + query
 * fingerprints), the suggested action, and a `confidence` field. Recommendations
 * the data cannot support are suppressed, not guessed." `dto/queries.go:615`
 * draws the consequence out — a recommendation that cannot cite its evidence is
 * not suppressible, because there is nothing to check — and that is why the UI
 * renders the four fields as four labelled parts instead of a sentence a reader
 * cannot audit.
 *
 * ── the evidence half is navigable, and that is the point ─────────────────
 *
 * `dto/queries.go:624` states why the fingerprints are in the payload at all:
 * "A client can follow a fingerprint back to R8.2's
 * `/insights/queries/{fingerprint}`, which is the reason the fingerprints are in
 * the payload rather than in a log line." A UI that prints the fingerprint as
 * text and stops there is a client that declined the reason. So each evidence
 * string is classified: 64 lowercase hex (R3.4's fingerprint shape) becomes a
 * link to that query's detail page, and anything else — `total_exec_ms = 128410.5`,
 * the metric-plus-value form — renders as text. The classification is exported
 * as `isFingerprint` because it is the one inference this component makes about
 * a string the API types as `string`, and an inference worth testing is an
 * inference worth exporting.
 *
 * It needs a cluster to build the link with: a fingerprint is canonical *within*
 * a cluster (R3.4), so `/insights/queries/{fingerprint}` is not addressable
 * without one. With `namespace`/`cluster` given the links resolve; without them
 * the evidence renders as text, which is the same answer as "not a fingerprint"
 * and costs nothing.
 *
 * ── confidence, and why it has no colour ──────────────────────────────────
 *
 * `dto/queries.go:636` flags the gap this component inherits: R7.6 requires the
 * field and never names its values, R7.1 (the recommender) is post-0.1, so there
 * is no vocabulary to derive a scale from and the type is a bare `string`. The
 * tempting render — a coloured chip, high/medium/low — is exactly what the
 * comment warns against freezing, and it would spend one of the reserved status
 * colours (R10.23: status colours are reserved and never reused) to express a
 * value whose domain is undefined. So confidence renders as its raw string in
 * text ink. When A3 names the vocabulary, the fix is a class map here and
 * nowhere else.
 *
 * ── the empty state ───────────────────────────────────────────────────────
 *
 * The fixture's `recommendations` is `[]`, and R7.6's suppression rule makes
 * that a meaningful answer rather than a missing one: recommendations the data
 * cannot support are suppressed, so an empty list means "nothing was supportable
 * about this query", not "the recommender has not run". The sentence says that,
 * because the two readings lead a reader to different actions (one is "this query
 * is fine", the other is "come back later").
 */

import type { ReactElement } from "react";

import type { RecommendationView } from "../api/types";
import { buildPath } from "../routes";

/** R3.4's fingerprint shape: 64 lowercase hex characters (a SHA-256 digest).
 *
 *  Used to decide which evidence strings are navigable. Exported because the
 *  alternative — trusting the API to say which kind a string is — is not
 *  available (`Evidence` is a flat `[]string`), so this component's only tool is
 *  the shape, and a test that pins the shape is the only defence against a
 *  metric string that happens to look like one. */
export function isFingerprint(value: string): boolean {
  return /^[0-9a-f]{64}$/.test(value);
}

export interface RecommendationListProps {
  /** R7.6's recommendations for this query. `[]`, never null. */
  recommendations: readonly RecommendationView[];
  /** The cluster the recommendations belong to, so an evidence fingerprint can be
   *  linked. Omitted, the evidence renders as text (a fingerprint with no
   *  cluster has no addressable detail page — R3.4). */
  namespace?: string;
  cluster?: string;
}

/** One evidence string: a link when it is a fingerprint, text otherwise. */
function EvidenceItem({
  value,
  href,
}: {
  value: string;
  href?: string;
}): ReactElement {
  if (href === undefined) return <li>{value}</li>;
  return (
    <li>
      <a href={href} className="query-link">
        {value}
      </a>
    </li>
  );
}

/** R10.7's recommendations block. */
export function RecommendationList({
  recommendations,
  namespace,
  cluster,
}: RecommendationListProps): ReactElement {
  // Both halves are needed before a fingerprint is addressable: buildPath returns
  // "/" for a cluster with no "/", and linking to "/" is worse than not linking.
  const clusterId = namespace !== undefined && cluster !== undefined ? `${namespace}/${cluster}` : undefined;
  const hrefFor = (value: string): string | undefined => {
    if (clusterId === undefined || !isFingerprint(value)) return undefined;
    const path = buildPath("query-detail", clusterId, value);
    return path === "/" ? undefined : path;
  };

  return (
    <section className="card" data-panel="recommendations">
      <div className="card-head">
        <h2 className="card-title">Recommendations</h2>
        {recommendations.length > 0 && (
          <div className="card-actions">
            <span className="card-meta num">{recommendations.length}</span>
          </div>
        )}
      </div>

      {recommendations.length === 0 ? (
        <p className="chart-empty" data-testid="recommendations-empty">
          No recommendations for this query — R7.6 suppresses recommendations the data cannot
          support rather than guessing them.
        </p>
      ) : (
        <ul className="recommendation-list">
          {recommendations.map((r, i) => (
            <li className="recommendation" key={`${r.finding}-${i}`}>
              {/* The four fields, in R7.6's order: finding, evidence, action,
                  confidence. A `dl` because each is a labelled part of one record,
                  which is what `dl` means. */}
              <div className="recommendation-head">
                <span className="recommendation-finding">{r.finding}</span>
                <span className="recommendation-confidence">{r.confidence}</span>
              </div>

              <div className="recommendation-body">
                <div className="recommendation-field">
                  <span className="recommendation-label">Evidence</span>
                  <ul className="recommendation-evidence">
                    {r.evidence.map((e, j) => (
                      <EvidenceItem key={`${e}-${j}`} value={e} href={hrefFor(e)} />
                    ))}
                  </ul>
                </div>

                <div className="recommendation-field">
                  <span className="recommendation-label">Action</span>
                  <span className="recommendation-action">{r.action}</span>
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
