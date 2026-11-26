/* R10.7's "JSON/plan viewer", one `PlanSampleView` at a time.
 *
 * R10.7 names two views of a plan and the DTO backs both with one field, which
 * is the whole reason this file is separate from `PlanList.tsx`:
 *
 *   - **plan** — `PlanSampleView.plan` is Postgres' `EXPLAIN` output as JSON, so
 *     the structured view is a *tree*: `Node Type` with its `Plans` children,
 *     each node's cost and rows. This is what a DBA reads, and rendering the raw
 *     JSON at them instead is making them parse a nested document to find the
 *     sequential scan.
 *   - **JSON** — the same object verbatim, because the tree only renders the
 *     keys Postgres documents and the object carries whatever this major
 *     actually emitted (`Parallel Aware?`, `Parent Relationship`, `Work Method`,
 *     a node type's private keys). The tree is a projection; the JSON is the
 *     evidence, and R9.13's auditability promise needs the evidence reachable.
 *
 * The two are not redundant: the tree answers "what plan did the planner pick",
 * the JSON answers "what did Postgres say", and only one of them is a claim
 * about the planner's own output shape. `types.ts:615` says the field is
 * `Record<string, unknown>` for exactly that reason — "the keys vary by node
 * type and by major version, and typing them here would freeze a shape Postgres
 * does not guarantee" — so the tree must degrade rather than assume.
 *
 * ── R3.10's three constraints, as they show up here ───────────────────────
 *
 *   (a) *EXPLAIN the first-seen raw text and skip truncated rows* — the sample
 *       carries `sql` and the row it came from carries `truncated`; the viewer
 *       shows the SQL it planned, and a truncated statement is the list's
 *       reason to have no sample at all, not this viewer's problem to render.
 *   (b) *PREPARE + EXPLAIN EXECUTE for parameterized statements* — that is the
 *       `prepare_execute` method, and the fixture's available sample is one.
 *       The method is shown, not hidden, because the two methods produce
 *       different plans for the same SQL and a viewer that omits which one ran
 *       makes two samples of one query look like a contradiction.
 *   (c) *`unavailable (insufficient table privileges)` is a state, not a
 *       per-query failure* — so an unavailable sample renders as a state panel
 *       with its `message`, in the same register as R10.28's other named empty
 *       states, and not as an error banner. `EXPLAIN ANALYZE` is never run
 *       automatically (R3.10), so a sample with `method: "analyze"` is a
 *       deliberate user action; nothing here implies the app ran it on its own.
 *
 * ── text safety ───────────────────────────────────────────────────────────
 *
 * Every string here is untrusted data — SQL text, Postgres' own messages, node
 * names from an arbitrary major. React's `{value}` interpolation is a
 * `textContent` write, which is the R10.23 rule ("labels are untrusted data →
 * `textContent` never `innerHTML`") satisfied by construction: there is no
 * `dangerouslySetInnerHTML` in this file and adding one would need a reason.
 */

import { useState } from "react";
import type { ReactElement } from "react";

import type { PlanSampleView } from "../api/types";
import { formatTimestamp, utcTitle } from "../lib/format";

/** R10.28's exact unavailable string, mirrored from `PlanAvailability`. Exported
 *  so the state's text has one spelling across the app, the same reason
 *  `LatencyHistogram` exports `SAMPLED_LABEL`. */
export const PLAN_UNAVAILABLE = "unavailable (insufficient table privileges)";

/** The two views R10.7 names. */
export type PlanViewMode = "plan" | "json";

/** A node of Postgres' EXPLAIN JSON tree, narrowed from `Record<string, unknown>`
 *  to the keys this viewer renders. Everything else stays in the raw object the
 *  JSON view prints. */
interface PlanNode {
  nodeType?: string;
  relationName?: string;
  indexName?: string;
  totalCost?: number;
  planRows?: number;
  plans?: PlanNode[];
}

/** Unwraps Postgres' `EXPLAIN … (FORMAT JSON)` envelope.
 *
 *  That format emits `{"Plan": {…}}` — the root node sits one level under a
 *  `Plan` key, and that is the shape the fixture's available sample carries
 *  (`testdata/api/query-detail.json`, generated from the Go DTO). Reading the
 *  envelope itself as a node finds no `Node Type` at the top level, so the tree
 *  would be empty for every real plan the app has. The wrapper comes off here,
 *  and a bare node object (no `Plan` key) passes through unchanged so a sample
 *  that already *is* a node still renders. */
function planRootOf(raw: Record<string, unknown>): Record<string, unknown> {
  const inner = raw["Plan"];
  return isRecord(inner) ? inner : raw;
}

/** Reads the keys the viewer renders out of an untyped plan object.
 *
 *  Returns null when the object has no `Node Type`, which is the signal that
 *  this is not a shape the tree can draw — a v12-era plan, a hand-written
 *  fixture, a future Postgres that renames things. The caller then offers the
 *  JSON view alone rather than rendering an empty tree, which is R10.13's rule
 *  for a panel that has content it cannot structure: say so, don't draw a box. */
function readNode(raw: Record<string, unknown>): PlanNode | null {
  const nodeType = raw["Node Type"];
  if (typeof nodeType !== "string") return null;
  const num = (k: string): number | undefined =>
    typeof raw[k] === "number" ? (raw[k] as number) : undefined;
  const children = Array.isArray(raw["Plans"]) ? raw["Plans"] : undefined;
  return {
    nodeType,
    relationName: typeof raw["Relation Name"] === "string" ? (raw["Relation Name"] as string) : undefined,
    indexName: typeof raw["Index Name"] === "string" ? (raw["Index Name"] as string) : undefined,
    totalCost: num("Total Cost"),
    planRows: num("Plan Rows"),
    plans: children === undefined ? undefined : children.filter(isRecord).map(readNode).filter(isNode),
  };
}

function isNode(n: PlanNode | null): n is PlanNode {
  return n !== null;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** One node and its children, indented. */
function NodeRow({ node, depth }: { node: PlanNode; depth: number }): ReactElement {
  // The relation or index the node touches, in the slot after the node type:
  // `Index Scan on users_pkey` reads the way `EXPLAIN` prints it, and omitting
  // it makes a Nested Loop tree unreadable (which table is the inner side?).
  const target = node.indexName ?? node.relationName;
  const metrics: string[] = [];
  if (node.totalCost !== undefined) metrics.push(`cost ${node.totalCost.toFixed(2)}`);
  if (node.planRows !== undefined) metrics.push(`${node.planRows} rows`);
  return (
    <>
      <div className="plan-node" style={{ paddingLeft: `${depth * 14}px` }}>
        <span className="plan-node-type">{node.nodeType}</span>
        {target !== undefined && <span className="plan-node-target">{target}</span>}
        {metrics.length > 0 && <span className="plan-node-metrics num">{metrics.join(" · ")}</span>}
      </div>
      {node.plans?.map((child, i) => (
        <NodeRow key={`${child.nodeType}-${i}`} node={child} depth={depth + 1} />
      ))}
    </>
  );
}

export interface PlanViewerProps {
  /** The sample to render. Undefined renders the "nothing selected" state. */
  sample?: PlanSampleView;
}

/** R10.7's viewer: the plan tree, the raw JSON, and the unavailable state. */
export function PlanViewer({ sample }: PlanViewerProps): ReactElement {
  const [mode, setMode] = useState<PlanViewMode>("plan");

  if (sample === undefined) {
    return (
      <section className="card" data-panel="plan-viewer">
        <div className="card-head">
          <h2 className="card-title">Plan</h2>
        </div>
        {/* R10.13's rule generalised to a selection panel: an empty pane with no
            sentence is the blank-box failure, and this one has a real thing to
            say — pick a sample. */}
        <p className="chart-empty">Select a plan sample to view it.</p>
      </section>
    );
  }

  const available = sample.availability === "available";
  const raw = sample.plan;
  const tree = raw === undefined ? null : readNode(planRootOf(raw));
  // The tree view is only offered when there is a tree to draw. An available
  // sample whose object Postgres shaped unexpectedly, or an unavailable one with
  // no plan at all, gets the JSON tab alone — and an unavailable sample with no
  // JSON gets neither, just the state.
  const canDrawPlan = tree !== null;
  const json = raw === undefined ? undefined : JSON.stringify(raw, null, 2);
  const active: PlanViewMode = canDrawPlan ? mode : "json";

  return (
    <section className="card" data-panel="plan-viewer">
      <div className="card-head">
        <h2 className="card-title">Plan</h2>
        <div className="card-actions">
          {canDrawPlan && (
            <div className="segmented" role="group" aria-label="Plan view mode">
              {(["plan", "json"] as const).map((m) => (
                <button
                  key={m}
                  type="button"
                  className={active === m ? "active" : undefined}
                  aria-pressed={active === m}
                  onClick={() => setMode(m)}
                >
                  {m === "plan" ? "Plan" : "JSON"}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* The sample's identity above its body: which method produced this, when,
          and its hash. The method is not decoration — R3.10(b) makes
          `prepare_execute` and `explain` different answers to the same SQL, so a
          viewer that hides which ran makes two samples look like a bug. */}
      <dl className="plan-meta">
        <dt>Captured</dt>
        <dd title={utcTitle(sample.capturedAt)}>{formatTimestamp(sample.capturedAt)}</dd>
        <dt>Method</dt>
        <dd>{sample.method}</dd>
        <dt>Availability</dt>
        <dd data-testid="plan-availability">{sample.availability}</dd>
        {sample.planHash !== undefined && (
          <>
            <dt>Plan hash</dt>
            <dd className="mono plan-hash" title={sample.planHash}>
              {sample.planHash}
            </dd>
          </>
        )}
        {sample.sql !== undefined && (
          <>
            <dt>SQL</dt>
            <dd className="plan-sql">{sample.sql}</dd>
          </>
        )}
      </dl>

      {!available && sample.message !== undefined && (
        /* R3.10(c): the privilege failure is a state with a message, rendered in
           the same register as R10.28's other named empty states. Not an error
           banner — the app is not broken, it lacks a grant. */
        <p className="state-detail" data-testid="plan-message">
          {sample.message}
        </p>
      )}

      {active === "plan" && canDrawPlan && tree !== null && (
        <div className="plan-tree" data-testid="plan-tree">
          <NodeRow node={tree} depth={0} />
        </div>
      )}

      {active === "json" && json !== undefined && (
        <pre className="plan-json" data-testid="plan-json">
          {json}
        </pre>
      )}
    </section>
  );
}
