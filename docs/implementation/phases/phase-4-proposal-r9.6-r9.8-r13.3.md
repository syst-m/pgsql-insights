# Proposal: R9.6 + R9.8 + R13.3 — the per-tag family's hub slot, the self-observability row's stale count, and who owns the latency script

Measured `2026-11-28T10:07:58Z` · gate `rc=0` · by A2 · classification: `existing-spec` (confidence `0.86`)

<!-- NPU note: `mcp__halogen-npu__decide` is not in this session's tool set (the project has no
.claude/settings.local.json enabling it), so the classification, the score and the three verify
probabilities below are A2's own, not `decide`'s — which is what SKILL.md:195 prescribes when the
endpoint is unreachable ("say the NPU was unreachable … the NPU is an optimisation here, not a
dependency") rather than blocking the task on it. -->

## 1. Current state (measured)

```
R9.6   blocked      ef9aa93:docs/implementation/phases/phase-4.md:92  code 5  first 62740ed42594@2026-11-24T12:30:44Z  cites 0  consistent
R9.8   in-progress  ef9aa93:docs/implementation/phases/phase-4.md:93  code 20  first e6614d7634b3@2026-11-24T10:37:20Z  cites 10  consistent
R13.3  blocked      ef9aa93:docs/implementation/phases/phase-4.md:97  code 3  first 7a8e15821ef9@2026-11-24T07:37:52Z  cites 0  consistent
```

No row is a finding (`finding: false` on all three) and the ledger's self-check reports
`122/122` rows agreeing with the checker. Two of the three rows are stale, in opposite
directions, and both are stated here rather than quietly fixed:

- **R9.6's gate has opened.** The row blocks the family on R3.12 ("R3.12 (tags) is Phase 6b's, so
  there is no `tag_hash` to key on until tags exist"). Tags now exist:
  `ef9aa93:internal/api/tags.go:244` (`type TagsSource interface`),
  `ef9aa93:internal/api/dto/queries.go:624` (`type TagView struct`),
  `ef9aa93:internal/store/rollups.go:133` (`func (d Dimensions) tagView() dto.TagView`). The
  row's own escape clause already applies — "Nothing in the chart blocks it: the family rides the
  same `/metrics` endpoint the ServiceMonitor already scrapes, so it needs an emitter and no
  plumbing."
- **R9.8's count is wrong.** The row reads "6 of the 8 render at `aee9046`" and then: "The two
  that remain — `pgsi_scheduler_queue_depth`, `pgsi_scheduler_shed_total` — are both R2.9's and
  both blocked on the subsystem rather than on a metric." `internal/collector/scheduler_metrics.go`
  now exists, `ef9aa93:internal/metrics/metrics.go:186` (`WithScheduler`) renders it, and
  `internal/metrics/scheduler_test.go` covers it — so the count is 8 of 8 and the row's status
  (`in-progress`) plus that sentence are both stale. Verified: `go test ./internal/metrics/
  ./internal/collector/` rc=0 at `ef9aa93`.

## 2. What is being asked

Implement Phase 4's three remaining A2-owned rows. The spec texts, verbatim:

- `requirements.md:185` (R9.6) — "Export the **per-tag** family (Cloud SQL's `insights/pertag`)
  when R3.12 tags are enabled, keyed by `tag_hash`."
- `requirements.md:187` (R9.8) — "Export **self-observability**: `pgsi_build_info{version,commit}`,
  `pgsi_collector_duration_seconds`, `pgsi_collector_errors_total`, `pgsi_scrape_target_up{cluster,pod}`,
  `pgsi_store_size_bytes`, `pgsi_http_request_duration_seconds`, `pgsi_scheduler_queue_depth`,
  `pgsi_scheduler_shed_total` (R2.9)".
- `requirements.md:268` (R13.3) — "API p50 **< 100 ms** for cached endpoints, p95 **< 400 ms**;
  first UI paint **< 1.5 s** (R10.14)."

This is code + record only: no requirement text changes, so nothing lands in
`docs/requirements/**` and the A3 spec-review gate isn't in the path — only
`scripts/check-parity-matrix.sh` is.

## 3. Assumptions adopted without asking

| Q | Answer | Confidence | Why this is not a question |
|---|---|---|---|
| Where R9.6's family body lives | `internal/metrics` declares the consumer-side interface only; the render body is the store's | 0.86 | `dto.TagView` (`internal/api/dto/queries.go:624`) carries `tagHash, application, database, user, action, route, controller, framework, dbDriver` — R3.12's set (`requirements.md:103`) — but **no** `namespace/cluster/pod/instance_role`, which `docs/requirements/cardinality.md:18`'s `…,tag_hash` label cell requires of every per-tag series. Only the store's `Dimensions` has them, and `rollups.go:133` drops them when it builds the DTO. This is R9.5's shape verbatim: the body is `internal/store/perquery.go:278` and `internal/metrics/metrics.go:139` declares `PerQueryFamily` because importing store from metrics closes the cycle `api→metrics→store→api`. |
| Metric names | `pgsi_tag_*`, marked provisional in the file | 0.74 | R9.6 names a *family*, not a metric — R9.11's provisional-naming rule's case, the one `chart/templates/prometheusrule.yaml`'s header applies (name it, mark it provisional in the file, bound the blast radius). The charter's §4 D13 / §5 D13 pairing is not invoked here: `IMPLEMENTATION.md` defines the id twice — §4 D13 is the Phase 1a/1b split, §5 D13 is the R8.1 openapi ruling — and neither is the metric-naming ruling, so the bare form is ambiguous to the checker and wrong in content. The set is derived from Cloud SQL's own member list (`docs/research/cloud-sql-insights.md:334-339`) and from the R9.4/R9.5 spelling already in `internal/api/promql_catalogue.go:129-135`, with `query` → `tag`. Naming beats stubbing or deferring, and the header bounds the blast radius the way `prometheusrule.yaml`'s header does. |
| `RenderTags` signature | `(ctx context.Context, prefix string) (string, error)` — no `topN` | 0.72 | R9.6's sentence has no N and `cardinality.md:18`'s Control cell for per-tag is the R3.12 gate, not a cap (unlike R9.5, whose N comes from R9.5's own text). R9.5's method takes `topN` because R9.5 says to; nothing here does. |
| `internal/api/server.go` wiring | flag it, don't edit it | 0.90 | `server.go` is excluded from my row and A1 is in it right now — shared-contract rule 6 ("Stay out of files another slot owns while they're mid-flight. Write the change and flag it, or ask for it"). The precedent is in my own charter: the last `server.go` change "was written inert and flagged rather than committed over their tree". The change is one line beside `ef9aa93:internal/api/server.go:773` (`metrics.WithPerQuery(s.cfg.PerQueryMetrics),`) inside `metricsBody` at `:771`. |
| R13.3 scope | stays `blocked`; the artifact is `scripts/api-latency.py`, which is not my file | 0.80 | The row already says why: "no measurement artifact exists. `scripts/` holds seven scripts and none measures latency … and `web/e2e` asserts rendered state, not timing." The p50/p95 instrument already exists as `pgsi_http_request_duration_seconds` (`internal/metrics/http.go`) and the budgets are already encoded in `chart/templates/prometheusrule.yaml` and `docs/runbooks/pgsi-api-latency.md`; what's missing is a script that scrapes and computes. R11.11 then needs a Makefile target (mine), and a Makefile target for an uncommitted script is `45a69a1`'s exact defect — so the target and the script must be one commit, which they can't be while the script belongs to someone else. |
| R9.8's fix shape | the row's status cell + the stale "6 of the 8" / "two that remain" prose, nothing else | 0.88 | The code is done; the only work is the record. The cell's own last sentence is the precedent for how A2 touches it: "**Cell wording:** A2 proposed `**partial**` for this cell; the R14.8 enum has no `partial` … so the cell reads `in-progress`" — A2 proposed, A3 wrote. Hence Q2. |

## 4. Questions for you

**Q1 — Who writes the two files this needs that are outside my row?** My answer: A5 writes
`internal/store/pertag.go` (it is the same pattern as the `perquery.go` A5 already owns, and the
`TagFamily` interface I ship is the contract it has to match — the same way
`internal/metrics/metrics.go:139` is the contract `perquery.go:278` matches); and
`scripts/api-latency.py` is A4's or mine-by-exception, since my `scripts/` ownership is
`check-parity-matrix.sh` alone. Options: (a) as proposed — A5 writes pertag, I ship the metrics
half now and the family renders absent until theirs lands; (b) you grant me `internal/store/pertag.go`
and `scripts/api-latency.py` for this phase and I write all of it; (c) hold R9.6 until A5's
Phase 3 pass includes it. The instruction "if it wants a non-`internal/api` file, say so in the
proposal rather than reaching outside your row" is what makes this a question instead of an edit.

**Q2 — Do I edit the three `phase-4.md` rows in the same commit as the code, or does A3 land
them?** The skill says the implementing slot updates the phase file in the same commit as its code
(and `ledger.py`'s `declared_in` is those rows, so they *are* the record); the R9.8 cell's own
text records that A2 proposed wording and A3 wrote it. My default is to write them (line-neutral,
status cells only) unless you'd rather A3 does.

## 5. Recommendation

Ship R9.6's metrics-side half in `internal/metrics`: a `TagFamily` interface beside
`PerQueryFamily`, a `tags` Hub field, a `WithTags` Option, one line in `RenderContext` after the
per-query block, and the provisional `pgsi_tag_*` name set marked provisional in the docblock with
its blast radius bounded ("if A5's provider chooses different names, only this header and the
provider change"). R9.8's row moves to `implemented` with the eight-family count corrected per
family. R13.3 stays `blocked` with the artifact named and its owner asked.

Scored `4/6` on `spec-implement ship rubric` (`noul ship` not run — no NPU; A2's own P(yes) `0.72`).
Verified: grounded `0.84` · on_brief `0.78` · no_leaks `0.91` (A2's own, not `decision-verify`'s).

Not `6/6`, and the two reasons are the assignment's own shape rather than this draft's: R9.6's
family emits no series until the provider lands (the rubric's 5th/6th levels want the spec text
*implemented*, and my half is the contract plus the slot), and R13.3 lands no artifact at all.

## 6. Plan

- **Owner:** `A2` (`docs/agents.md:58`)
- **Files:**
  - `internal/metrics/metrics.go` — as committed: `TagFamily` interface at `:187` (below
    `PerQueryFamily` at `:147`), `WithTags` Option at `:246`, the `tags` Hub field at `:287`, the
    `RenderContext` render at `:409`, and the provisional-name paragraph in the
    `# Catalogue families` section. Every insertion is below line 24, so
    `docs/architecture.md:126`'s `internal/metrics/metrics.go:18-24` cite stays byte-correct.
  - `internal/metrics/metrics_test.go` — the tag-family tests **appended at the end** (after
    `internal/metrics/metrics_test.go:339`), so `cmd/pgsi/targets.go:6`'s `internal/metrics/metrics_test.go:127` cite doesn't move.
  - `docs/implementation/phases/phase-4.md` — rows `:92` and `:93` (pending Q2). `:97` (R13.3)
    stays `blocked` and is not edited, per §3.
- **Also in this commit (in-row, line-neutral):** three stale cites inside my own files —
  `internal/metrics/metrics.go:135` cited `internal/store/perquery.go:271`, and
  `internal/metrics/metrics_test.go:30` and `internal/metrics/metrics_test.go:231` cited `internal/store/perquery.go:271`
  and `:295`; the method is at `perquery.go:278` and the name derivation is at `:305`. All three
  resolved in the checker (the numbers land on non-blank lines inside the right function) and all
  three were wrong — rc proves resolvability, not correspondence. The third (`internal/metrics/metrics_test.go:30`)
  was not in the proposal's first draft; it was found by grepping every `perquery.go:` cite in the
  two files rather than the two the draft named.
- **Gate:** `bash scripts/check-parity-matrix.sh` — rc=0 measured before (`ef9aa93`), reported
  after the commit with the rc from the script itself, not from a pipe.
- **Commit:** `A2: add the R9.6 per-tag family slot to the metrics hub and correct the R9.8 row`
  with pathspec (`git commit -- <paths>`), `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
- **Record:** the phase-4.md rows are the record (`ledger.py`'s `declared_in` points at them); the
  before→after pair is this file's §1 versus the post-commit `ledger.py` run, plus the mutation row.
- **Not pushed** — the coordinator owns the push.

## 7. Awaiting

The instruction was "propose, implement, then record with the before→after pair plus the mutation
row", so step 6 ran on that sequence rather than on an answer to Q1/Q2. The two questions stay
open and the §3/§4 defaults were taken: A5 owns `internal/store/pertag.go`, so the family renders
absent until it lands, and the `phase-4.md` rows were written here, line-neutral, status cells
only. An answer that contradicts either is a follow-up edit, not a revert. A background
notification is not an answer, and a confident model answer (§3) is not an approval — the commit is
what the coordinator asked for, and it is offered as the artifact an answer can act on.
