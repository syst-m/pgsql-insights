# Record: R2.7 + R13.1 — one read-only transaction per view group, and the poll cost budget

Landed `194aaa9` (code + proposal) at `2026-11-28T02:26:31Z`, `ccfd04e` (the two phase-2.md rows)
at `2026-11-28T02:54:37Z`, and this record in `696bb85` · by A1 · gate `rc=0` after `ccfd04e`;
the post-commit runs at `696bb85` and at this amendment are `rc=1`, and every failure in both of
them lives in A2's phase-4 files, which no commit of mine introduces or can fix

The work is in three commits because the rows cannot cite the commit that lands them and this
record cannot either: `194aaa9` carries `cmd/pgsi/main.go`, `cmd/pgsi/config.go`,
`cmd/pgsi/budget_test.go`, `cmd/pgsi/main_test.go` and the proposal; `ccfd04e` carries the two
phase-2.md rows and the proposal's d-id corrections; `696bb85` carries this file alone. The proposal's header follows the S55 rule from phase-2.md's own validation entry — it
quotes the rev it measured (`ef9aa93`), not the commit it lands in.

## Measured before → after

```
before  ef9aa93  R2.7    in-progress  docs/implementation/phases/phase-2.md:177  code 18  first e4b9abb660d4@2026-11-25T11:48:25Z  latest 4360e217798f@2026-11-22T16:15:34Z  doc 30  first 189093a6582e@2026-11-28T01:18:38Z  cites 5   consistent
after   ccfd04e  R2.7    implemented  docs/implementation/phases/phase-2.md:177  code 19  first 194aaa9a4aba@2026-11-28T02:26:31Z  latest 4360e217798f@2026-11-22T16:15:34Z  doc 31  first 194aaa9a4aba@2026-11-28T02:26:31Z  cites 10  consistent

before  ef9aa93  R13.1   in-progress  docs/implementation/phases/phase-2.md:171  code 26  first e4b9abb660d4@2026-11-25T11:48:25Z  latest db4f237b4b9e@2026-11-20T18:55:06Z  doc 42  first 189093a6582e@2026-11-28T01:18:38Z  cites 0   consistent
after   ccfd04e  R13.1   implemented  docs/implementation/phases/phase-2.md:171  code 27  first 194aaa9a4aba@2026-11-28T02:26:31Z  latest db4f237b4b9e@2026-11-20T18:55:06Z  doc 44  first ccfd04eb7c23@2026-11-28T02:54:37Z  cites 0   consistent
```

Both lines per row come from `python3 .claude/skills/spec-implement/scripts/ledger.py R2.7 R13.1
--json [--root <worktree>]`. The `before` was measured in a detached worktree at `ef9aa93`
(`git -C <wt> status --porcelain | wc -l` → `0` in the same call, so the checkout is the rev and
not a dirty tree; `measured_at 2026-11-28T10:56:23Z`, `gate_rc 0`), the `after` at `ccfd04e` in
the working tree (`measured_at 2026-11-28T10:55:06Z`, `gate_rc 0`). The ledger's self-check
reports `122/122` rows agreeing with the checker in both runs, and neither row is a finding
(`finding: false`) in either.

R2.7's `cites` count moves 5 → 10 because the rewritten cell spells out the addresses the old one
named in prose: the flag registration, the two `pgxopener.go` lines, the CONNECT guard and its
test, and the four per-group `Begin` lines. R13.1's stays 0 — the row's File(s) cell lists paths
without line numbers, and the new cell's numbers (`23`, `40`, `10`) are measurements, not cites.

## What changed

| File | 194aaa9 | ccfd04e | Change |
|---|---|---|---|
| `cmd/pgsi/main.go` | 42 / 13 | — | `statementTimeout *time.Duration` on `serveFlags`, the `--statement-timeout` flag declaration, and `newRegistry`'s new `sqlCfg collector.SQLConfig` parameter replacing the inline `collector.SQLConfig{}` the ten constructor calls used to build |
| `cmd/pgsi/config.go` | 9 / 0 | — | the `{Flag: "statement-timeout", Req: "R2.7"}` settings-table row `TestTableCoversEveryFlag` requires |
| `cmd/pgsi/main_test.go` | 6 / 6 | — | the six `newRegistry(...)` call sites gain the trailing `collector.SQLConfig{}` argument |
| `cmd/pgsi/budget_test.go` | 181 / 0 | — | `TestSQLConfigThreadsToTheOpener` (R2.7) and `TestPollCostBudget` (R13.1), with the `r131RoundTripBudget = 40` and `wantWireCollectors = 10` constants |
| `docs/implementation/phases/phase-2-proposal-r2.7-r13.1.md` | 94 / 0 | 8 / 7 | the proposal; then its four ambiguous d-id mentions spelled out as `§4-D20`/`§5-D20` (that number is defined by both the §4 decisions log and the §5 deviations table, so an unqualified mention of it is ambiguous outside the two tables) and two off-by-one `pgxopener.go` cites corrected (:439 → :440, :449 → :450) |
| `docs/implementation/phases/phase-2.md` | — | 3 / 3 | rows :171 (R13.1) and :177 (R2.7) rewritten to `**implemented**`; row :36's `cmd/pgsi/main.go:587` retargeted to `:610` |
| `docs/implementation/phases/phase-3.md` | — | 1 / 1 | A5's R9.5 row (:89): the three `cmd/pgsi/main.go` cites `194aaa9` shifted are retargeted (:757 → :780, :579 → :602, :736 → :759) and the `(was cmd/pgsi/main.go:362)` exhibit is revved to `d221e4f:cmd/pgsi/main.go:362` |
| `docs/implementation/phases/phase-2-record-r2.7-r13.1.md` | — | — | this record, in its own commit `696bb85` (amended once to spell its d-id mentions in the section-qualified form the gate requires outside the two D tables) |

## Verification

| Command | rc | What it proves |
|---|---|---|
| `bash scripts/check-parity-matrix.sh` | 0 | the doc/code parity gate, run after `ccfd04e` (the two `194aaa9` failures — 2 wrong path cites and 1 non-exempt d-id — are the ones this commit closes) |
| `go build ./...` | 0 | the tree compiles at `ccfd04e` |
| `go vet ./...` | 0 | no vet findings |
| `go test ./cmd/pgsi -run 'TestSQLConfigThreadsToTheOpener\|TestPollCostBudget\|TestTableCoversEveryFlag' -count=1` | 0 | the two new tests plus the flag/table weld `TestTableCoversEveryFlag` enforces |
| MUT-A: `const r131RoundTripBudget = 40` → `= 20` | 1 | `--- FAIL: TestPollCostBudget` — the budget assertion is load-bearing, not a tautology over the number it computed |
| MUT-B: `collector.NewActivityCollector(opener, sqlCfg)` → `collector.NewActivityCollector(opener, collector.SQLConfig{})` | 1 | `--- FAIL: TestSQLConfigThreadsToTheOpener` with `opener was begun with SQLConfig{StatementTimeout: 5s} across 11 Begins, want 3s` — the test really reads the value that reaches the opener, so a collector that drops `cfg` fails |

Both mutations were hand-run and reverted; `scripts/mutation-sweep.py` (A5's) scopes its `SITES`
to `internal/query` and `internal/store` and has no `cmd/pgsi` entries, so there is no sweep
target to add them to yet. The rc values are from `out=$(cmd 2>&1); rc=$?`, not from a pipe's
`$?`. The gate rc is post-commit — a pre-commit run checks the index's copy, and the two differ
on a shared tree.

## Deviations from the spec

**R13.1's `< 3 s` clause is not asserted.** The requirement has two clauses — `< 3 s` and
`< 40 SQL round-trips` — and only the second is now testable. `TestPollCostBudget` asserts the
round-trip count; the duration clause needs a live Postgres, and a fake opener's latency measures
the fake. The row says this explicitly rather than claiming the whole requirement is asserted.

**Eleven transactions for ten collectors.** R2.7's unit is the *view group*, not the collector, and
`internal/collector/settings.go:167` declares two view groups for one collector. The test computes
`wantTx := Σ len(e.Desc.ViewGroups)` over the registry rather than hardcoding the collector count,
so it asserts the requirement's actual unit and the 10 → 11 shape falls out of the registry instead
of being typed in.

**The flag's default is `0`, not `5s`.** `--statement-timeout` defaults to 0, and 0 selects
`collector.DefaultStatementTimeout` (5 s) inside the opener. A second `5 * time.Second` literal in
`cmd/pgsi` would be a second copy of a value `internal/collector/sql.go` already owns; the flag
description states the 5 s it implies.

**`SET LOCAL statement_timeout` is not counted as a round-trip.** §4-D20 excludes "transaction
control statements" from `Result.RoundTrips`, and the opener issues `SET LOCAL` alongside `BEGIN`
as transaction-scoped configuration, not as a query against a view. Recorded as assumption 3 in the
proposal (§3, confidence 0.78) rather than as a spec amendment.

## Answers to the proposal's open questions

§4 was `None.` — every judgement call cleared the 0.7 bar and went into §3 as a stated assumption
instead of a question, so there was nothing to wait on and nothing to override. Silence on a stated
assumption is the record of it being accepted; all five §3 assumptions stand as written, including
the one that put the R13.1 test in `cmd/pgsi/budget_test.go` rather than in `internal/collector`,
which is where the reference registry lives.
