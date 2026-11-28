# Record: R3.10 — plan samples, captured with `PREPARE` + `EXPLAIN EXECUTE`

Landed `4f7c9ab` (code + proposal + the phase-6.md row) at `2026-11-28T04:05:04Z` and this
record in its own commit, whose sha it cannot print without changing · by A5 · gate `rc=0`
after `4f7c9ab`.

`4f7c9ab` is an amend of `708cfd1`. The first commit measured `rc=1` and the failing tier was
the id tier — the one summary line whose wording does not contain the word "wrong", so it is
invisible to a grep for `wrong|not exempt|non-canonical|mismatched`:

    citations: 210 distinct ids across 287 files · 4 not defined by §4 · 0 orphan exemptions

All four were mine: `R3.10a` and `R3.10b` in `docs/implementation/phases/phase-6.md:231`, and
`R3.10a` twice in `internal/collector/plans_test.go:170` and `internal/collector/plans_test.go:330`.
§4 defines `R3.10`; the tier requires every R-id string anywhere in the tree — `.go` included —
to resolve to a §4 definition or an exemption, and `R3.10a` is a *different string*, so it is
reported rather than folded. The fix is the three-string edit (`R3.10a` → `R3.10's (a)`), which
is mine to make; defining `R3.10a`/`R3.10b` in §4 would be A3's file and two new §7.3 index rows,
and the a/b/c split is a reading of one requirement's sentence, not two requirements shipped
apart. The amended commit measures `rc=0` with every tier at zero.

## Measured before → after

```
before  5f995c6  R3.10  in-progress  docs/implementation/phases/phase-6.md:231  code 13  first 62740ed42594@2026-11-24T12:30:44Z  latest 65ed2eec565a@2026-11-22T23:52:42Z  doc 23  first 189093a6582e@2026-11-28T01:18:38Z  cites 0  consistent
after   4f7c9ab  R3.10  implemented  docs/implementation/phases/phase-6.md:231  code 14  first 4f7c9ab21e00@2026-11-28T04:05:04Z  latest 65ed2eec565a@2026-11-22T23:52:42Z  doc 24  first 4f7c9ab21e00@2026-11-28T04:05:04Z  cites 0  consistent
```

Both lines come from `python3 .claude/skills/spec-implement/scripts/ledger.py R3.10 --json`. The
`before` was measured at `5f995c6` (`measured_at 2026-11-28T12:00:32Z`, `gate_rc 1`); the `after`
in a detached worktree at `4f7c9ab` (`git worktree add --detach /tmp/a5w13 4f7c9ab…` and
`git -C /tmp/a5w13 status --porcelain | wc -l` → `0` in the same call, so the checkout is the rev
and not a dirty tree; `measured_at 2026-11-28T12:10:03Z`, `gate_rc 0`). The ledger's self-check
reports `133/133` rows agreeing with the checker in both runs, and the row is not a finding
(`finding: false`) in either.

`cites` stays `0` on both sides: the row's File(s) cell lists paths without line numbers, so
rewriting the cell could not strand a cite outside it — which is the check the proposal's §1 made
before writing anything, and the reason the row can be rewritten in the same commit as the code.

## What changed

| File | 4f7c9ab | Change |
|---|---|---|
| `internal/collector/plans.go` | 584 / 0 | `PlanSampler` (`internal/collector/plans.go:177`) and its whole capture path: `pick` (`internal/collector/plans.go:270`) ranks rows by `total_exec_time` desc / template asc, caps at `PlansPerMinute`, skips truncated (`internal/query/normalizer.go:147`'s `Normalized.Truncated`) and non-`EXPLAIN`able kinds (`internal/collector/plans.go:328`, which reuses `internal/collector/activity.go:499`'s `queryTypeFrom`); `planSQLFor` (`internal/collector/plans.go:378`) is the two paths the requirement names; `prepareName` (`internal/collector/plans.go:421`) is the named invalidation strategy; `capture` (`internal/collector/plans.go:433`) wraps each capture in a savepoint; `failed` (`internal/collector/plans.go:497`) turns a permission error into the (c) state |
| `internal/collector/plans_test.go` | 657 / 0 | eleven tests: config validation/defaults, `pick` ranking + cap + tie-break + the two skip rules, the two SQL paths, the batch run, the (c) state, error propagation, four malformed-plan-JSON cases, the round-trip count, the plan hash, the statements-collector attach, and the no-sampler path |
| `internal/collector/statements.go` | 46 / 17 | the `plans *PlanSampler` field, `WithPlanSampler` (`internal/collector/statements.go:927`), `rowWithPlan` (`internal/collector/statements.go:938`), and the capture call between the last view query and the `COMMIT` (`internal/collector/statements.go:511`); the round's `RoundTrips` grows by the sampler's own count |
| `docs/implementation/phases/phase-6-proposal-r3.10.md` | 158 / 0 | the proposal, whose header quotes the rev it measured (`8870400`), not the commit it lands in |
| `docs/implementation/phases/phase-6.md` | 1 / 1 | row `docs/implementation/phases/phase-6.md:231` rewritten from `**in-progress**` to `**implemented**`; the file is 245 lines before and after, so no row below it moved |

Two of the five files were written before the amend and one line changed in each of them; the
`708cfd1` → `4f7c9ab` diff is three strings and nothing else, so the numstat above is the same on
both shas.

Two fixes came out of running the tests against the code rather than from reading it:

* `run` had been replacing the server's error with an `errPermissionDenied` sentinel, so the (c)
  sample's `Message` read `"permission denied"` instead of the server's own
  `"pq: permission denied for table orders"` — the only text in the sample that names *which*
  table was unreadable. The sentinel is gone; `run` returns the raw error and `failed` classifies
  it with `isPermissionDenied(err)` and quotes `err.Error()`. The classification had also been
  done twice (in `run` and in `failed`), and the sentinel was the second copy.
* The plan-hash test's indented fixture spelled `"Startup Cost": 0.0` where the compact one had
  `0.00`. `json.Compact` normalises whitespace, not number spelling, so the two hashes differ —
  which is a property of the implementation the test had been asserting by accident. The fixture
  now matches, and a new assertion pins the limit: a `0.0` spelling must hash apart from `0.00`,
  so the test says what it checks instead of checking something the author did not mean.

## Verification

| Command | rc | What it proves |
|---|---|---|
| `bash scripts/check-parity-matrix.sh` (detached worktree at `4f7c9ab`, `status --porcelain \| wc -l` → 0) | 0 | the doc/code parity gate: 208 ids 0 not defined, 1862 path cites 0 wrong, 39 commit-message cites 0 wrong, 1221 table rows 0 mismatched, 133 status cells 0 non-canonical, 159 links 0 broken, d-ids 0 not exempt |
| `gofmt -l internal/collector/` | 0 | no unformatted file among the five |
| `go build ./...` | 0 | the tree compiles at `4f7c9ab` |
| `go vet ./internal/collector/` | 0 | no vet findings |
| `go test ./internal/collector/ -count=1` | 0 | the whole collector package, 0.814s — the eleven new tests plus the pre-existing fixture tests whose query strings the sampler had to leave unchanged |
| MUT-A: `pick` stops skipping truncated rows (`if n.Truncated {` → `if false && …`) | 1 | `--- FAIL: TestPlanPickRanksSkipsAndCaps` — constraint (a)'s "skip truncated rows" is asserted, not just written down |
| MUT-B: every statement takes the plain-EXPLAIN path (`if n.Params == 0 {` → `if true {`) | 1 | `--- FAIL: TestPlanSQLForIsTheTwoCapturePaths` — constraint (b)'s `PREPARE` + `EXPLAIN EXECUTE` path is exercised; with it removed, a parameterized statement would be EXPLAINed as a template and get a pet plan |
| MUT-C: `failed` stops classifying permission errors (`if isPermissionDenied(err) {` → `if false && …`) | 1 | `--- FAIL: TestPlanCapturePermissionDeniedIsAStateNotAFailure` — constraint (c) is asserted as a *state* (sample set, `err == nil`), so it cannot silently become a per-query failure |
| MUT-D: the capture stops deallocating what it prepared (`Deallocate: "DEALLOCATE " + name` → `""`) | 1 | `--- FAIL: TestPlanSQLForIsTheTwoCapturePaths` — this is the mutation for the failure mode the assignment named (unbounded prepared-statement growth); the `DEALLOCATE` is asserted by the exact query list, so the invalidation strategy is tested, not only documented |

All four mutations were hand-run and reverted in a detached worktree at `4f7c9ab` (each
`git checkout -- internal/collector/plans.go` back to `0` dirty lines, final
`git status --porcelain | wc -l` → `0`); `scripts/mutation-sweep.py` scopes its `SITES` to
`internal/query` and `internal/store` and has no `internal/collector` entries, so there is no
sweep target to add them to yet. The rc values are from `out=$(cmd 2>&1); rc=$?`, not from a
pipe's `$?`. The gate rc is post-commit — a pre-commit run checks the copy the index holds, and
on this shared tree the two differ; the commit-message tier also parses cites out of the message,
so the artefact being created is one of the gate's inputs and no pre-commit run can read it.

## Deviations from the spec

**Nothing calls `WithPlanSampler` yet.** `cmd/pgsi/main.go:1090` is the only non-test
`NewStatementsCollector(opener, sqlCfg)` call site and it does not pass a sampler, so a real
deployment today still captures no plans — the collector's `plans` field is nil and
`TestStatementsWithoutASamplerIssueNoPlanQueries` pins that path at two view queries and every
row's `Plan` nil. Wiring the sampler into `newRegistry` is the remaining step and it is A1's
`cmd/pgsi` change, not a collector change; the same gap is recorded for R2.8's `WithExcludeRole`.

**The 200-plans/minute setting does not fit R13.1's budget.** R3.10 exposes Cloud SQL's
Enterprise Plus value of 200 plans/minute; 200 captures at three queries each is ~600 queries in
a round, 15× the `< 40 round-trips` rule at `requirements.md:266`. The two requirements are only
compatible at the default 5 — which is what the spec itself defaults to — and the arithmetic that
does fit (23 measured by A1 + 16 at the cap = 39 of 40) relies on `requirements.md:266`'s own
rule that transaction control statements are not counted, which is what makes the six savepoints
and the `SET LOCAL` free. Nothing in the tree prices the 200 case, so it is stated in the
proposal's §3 and §5 and in the code's cost comment rather than decided quietly.

**The capture is a component, not a registry collector.** `internal/collector/collector.go:287`
says a Descriptor's `Name` is "one of §3.3's collector names; the schedule table is the list", and
§3.3's table has no `plans` row — so a registry entry would mint a name the spec's list does not
contain. The capture therefore hangs off the collector that already reads `pg_stat_statements`, on
the transaction it already opened.

## Answers to the proposal's open questions

§4 was `None.` — the plan had been approved as stated before implementation began, so §3's rows
record judgements that were already accepted rather than asking them again. All seven stand as
written, including the one the tests disagreed with: the proposal described `failed` as
classifying through an `errPermissionDenied` sentinel, and the sentinel turned out to be the
reason the (c) sample lost the server's error text. The rule it now follows — the server's error
travels unrewritten to the one place that decides whether it is a state or a failure — is stated
above and in the code comment, and MUT-C is the instrument that makes it enforceable rather than a
preference recorded in prose.
