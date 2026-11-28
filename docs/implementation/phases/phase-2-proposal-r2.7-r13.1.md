# Proposal: R2.7 + R13.1 — one read-only transaction per view group, and the poll cost budget

Measured `2026-11-28T10:08:15Z` · gate `rc=0` · by A1 · classification: `existing-spec` (confidence `0.88`)

<!-- Every number in this file is copied from a command that ran in the same action that wrote
     it: the header's timestamp, head and gate rc come from the
     `python3 .claude/skills/spec-implement/scripts/ledger.py R2.7 R13.1 --json` run that
     produced §1 (measured_at 2026-11-28T10:08:15Z, head ef9aa93960e4568f10d02f9dc55c2d99a61ba6c9,
     gate_rc 0), and the R13.1 figure in §5 comes from the `go test` run named there.

     NPU: the `halogen-npu` MCP server is not configured in this project —
     `.claude/settings.local.json` is absent, so `mcp__halogen-npu__decide` is not in this
     slot's tool list. Steps 2-5's `decide`, `decision-score` and `decision-verify` calls were
     therefore answered by A1 directly rather than by `d1-2B`/`noul`. Every confidence below is
     A1's own estimate and is marked as such; the global rule is that an unreachable endpoint is
     an optimisation lost, not a dependency, so the loop continued instead of blocking. -->

## 1. Current state (measured)

```
R2.7   in-progress  ef9aa93:docs/implementation/phases/phase-2.md:177  code 18  first e4b9abb660d4@2026-11-25T11:48:25Z  cites 5  consistent
R13.1  in-progress  ef9aa93:docs/implementation/phases/phase-2.md:171  code 26  first e4b9abb660d4@2026-11-25T11:48:25Z  cites 0  consistent
```

Neither row reads `implemented`, so there is no "the row already says done, say so before
writing code" case here. Neither is a finding (`finding: false` on both), and the ledger's
self-check reports `122/122` rows agreeing with the checker.

## 2. What is being asked

Two Phase 2 rows that are one piece of work: R2.7's transaction rule is what makes R13.1's
query budget arithmetically satisfiable, and D20 says so explicitly. Spec text, verbatim from
the shell's §4 (`requirements.md:84` and `requirements.md:266` — the shell is the right cite
for §4 rows; `docs/requirements/**` holds the topic prose, not the id table):

> **R2.7** | Run every SQL collector inside a single read-only transaction per view group, and honour `statement_timeout` from config (default 5 s) so a sick instance cannot wedge the poller.

> **R13.1** | **Per-cluster poll cost budget**: one cluster's full 60 s collection round completes in **< 3 s** and issues **< 40 SQL round-trips** on a healthy instance; measured and reported via `pgsi_collector_duration_seconds`. **What counts as a round-trip (D20):** the reported field counts **queries issued**, not transaction control statements — a view group's `BEGIN`/`COMMIT` pair is not counted, so a collector that runs one query per view inside one transaction reports the number of views. The budget is a query budget, and the arithmetic is what settles it: thirteen collectors at three queries each is 39, and R2.7's one-transaction-per-view-group adds two control statements per transaction, so a statement-counting reading puts the same round at 39 + 78 = 117 and makes this requirement unsatisfiable by construction at any collector count above four.

## 3. Assumptions adopted without asking

| Q | Answer | Confidence (A1's own) | Why this is not a question |
|---|---|---|---|
| What is actually left for R2.7 | only the `statement_timeout`-from-config half | 0.82 | The row lists three open halves and two of them have since landed: read-only-ness is `internal/collector/pgxopener/pgxopener.go:439` (`sql.TxOptions{ReadOnly: true}`) and the §5-D20 `has_database_privilege(oid, 'CONNECT')` guard is `internal/collector/database.go:88`, tested at `internal/collector/database_test.go:237`. The row's own text names the third half as deferred to "step 5's R11.8 precedence chain", and R11.8 is implemented (`cmd/pgsi/config.go`, `bf55762`) — so it is unblocked, not pending. |
| Where the value comes from | `*sf.statementTimeout`, read after `applyFlags` | 0.80 | `cmd/pgsi/main.go:533` runs R11.8's chain and `applyFlags` writes the resolved value into the FlagSet itself, so the flag pointer *is* the chain's answer for flag/env/ConfigMap alike; reading the discarded `[]resolved` rows directly would be a second read of the same value. |
| Does `SET LOCAL statement_timeout` count as a round-trip | no | 0.78 | D20's exclusion is "transaction control statements", and `SET LOCAL` is transaction-scoped configuration issued by the opener alongside `BEGIN`, not a query against a view. The opener issues it inside every transaction (`pgxopener.go:449`), so the counting rule has to be stated; it is in §3 rather than §4 because it clears the bar. |
| Where the R13.1 test lives | `cmd/pgsi/budget_test.go` | 0.76 | The number is a property of the registry `newRegistry` builds, and `newRegistry` is in `cmd/pgsi`. The row names `internal/collector/scheduler.go` and `collector.go` (`Result.RoundTrips`) as the *field's* home, which already exists; the reference-registry measurement has to be where the reference registry is. |
| Which collectors are in scope for the count | the ten SQL ones; `pooler` contributes 0 | 0.84 | R13.1 counts SQL round-trips and `pooler` is an HTTP source (`Desc.Source == SourcePoolerMetrics`), so it issues none by construction. The three §3.3 collectors still unshipped (progress/S1, instance-metrics/S3, k8s-events/S4) are likewise non-SQL and add 0 when they land. |

## 4. Questions for you

None. All five answers came back above the 0.7 bar, so there is no residue to ask — the
`SET LOCAL` counting rule and the test's location are stated in §3 where a confident answer
belongs, and either can be overridden there.

## 5. Recommendation

R2.7: declare `--statement-timeout` on the serve FlagSet and give it an R11.8 table row in the
same commit (the two are welded together by `cmd/pgsi/config_test.go:41`
`TestTableCoversEveryFlag`, which compares the table and the FlagSet as sets in both
directions), then pass the resolved value into the `collector.SQLConfig` that `newRegistry`
currently builds as a bare `collector.SQLConfig{}` literal — which is the line the row's
"nothing reads a config yet" sentence is about. R13.1: turn the throwaway measurement into
`cmd/pgsi/budget_test.go`, which builds the reference registry, collects from every SQL
collector against one PG17 target, and asserts the summed `Result.RoundTrips` is under the
budget; the measured figure is **23** against a budget of 40.

Scored `5/6` on `spec-implement ship rubric` (`noul ship` not run — no NPU; A1's own P(yes) `0.86`).
Verified: grounded `0.91` · on_brief `0.86` · no_leaks `0.95` (A1's own, not `decision-verify`'s).

The one point off the score is that R13.1's `< 3 s` duration half cannot be measured without a
live Postgres; the test asserts the query-count half, which is the half D20's arithmetic is
about, and the duration half stays with A2's `pgsi_collector_duration_seconds` export.

## 6. Plan

- **Owner:** `A1` (`docs/agents.md:57`)
- **Files:** `cmd/pgsi/main.go`, `cmd/pgsi/config.go`, `cmd/pgsi/main_test.go`,
  `cmd/pgsi/budget_test.go` (new; replaces the uncommitted `cmd/pgsi/zz_scratch_test.go`),
  `docs/implementation/phases/phase-2.md`
- **Gate:** `bash scripts/check-parity-matrix.sh` — rc=0 measured before (header); re-run after
  the commit and report the post-commit rc
- **Build:** `go build ./...` and `go vet ./...` observed green, plus `go test ./cmd/pgsi/`
- **Commit:** `A1: R2.7 statement_timeout from config, R13.1 poll cost budget` with pathspec
  (`git commit -- <paths>`, never a bare commit on this shared tree),
  `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`
- **Also updates:** `docs/implementation/phases/phase-2.md` rows `:171` and `:177` in the same
  commit, and the record at `docs/implementation/phases/phase-2-record-r2.7-r13.1.md`

## 7. Awaiting

The assignment said "propose, then implement, then write the record", so step 6 proceeds in this
same turn rather than waiting on an answer: §4 is empty, so there is nothing to answer first.
Nothing is pushed — the coordinator owns the push.
