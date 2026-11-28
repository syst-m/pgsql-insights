# Record: R2.8 — exclude our own sessions from every count we report

Landed `cfbd5bc` (code + proposal + the phase-2.md row) at `2026-11-28T03:19:26Z` and this
record in its own commit, whose sha it cannot print without changing · by A5 · gate `rc=1` after `cfbd5bc`, and every failing finding in
that run lives in the two phase-4 files (`docs/implementation/phases/phase-4-proposal-r9.6-r9.8-r13.3.md`
and `docs/implementation/phases/phase-4.md`, both writing the shared `§5-D13` bare, plus A2's
wrong and ambiguous path cites there) — the identical failure set is present at `36d6687`, the
commit this one is built on, so nothing here introduces or can fix it.

The code, the proposal and the row are one commit because the row's File(s) cell cannot cite
the commit that lands it: it names the two collectors and their tests by path without a sha,
and this record — which can name `cfbd5bc` — is the file that carries that citation. The
proposal's header follows the same rule A1's does: it quotes the rev it measured (`36d6687`),
not the commit it lands in.

## Measured before → after

```
before  36d6687  R2.8  in-progress  docs/implementation/phases/phase-2.md:178  code 15  first e4b9abb660d4@2026-11-25T11:48:25Z  latest 4360e217798f@2026-11-22T16:15:34Z  doc 27  first 189093a6582e@2026-11-28T01:18:38Z  cites 0  consistent
after   cfbd5bc  R2.8  implemented  docs/implementation/phases/phase-2.md:178  code 16  first cfbd5bc9288d@2026-11-28T03:19:26Z  latest 4360e217798f@2026-11-22T16:15:34Z  doc 28  first cfbd5bc9288d@2026-11-28T03:19:26Z  cites 0  consistent
```

Both lines come from `python3 .claude/skills/spec-implement/scripts/ledger.py R2.8 --json`. The
`before` was run in the working tree at `36d6687` (`measured_at 2026-11-28T11:14:37Z`,
`gate_rc 1`); the `after` was run in a detached worktree at `cfbd5bc`
(`git -C <wt> status --porcelain | wc -l` → `0` in the same call, so the checkout is the rev and
not a dirty tree; `measured_at 2026-11-28T11:21:52Z`, `gate_rc 1`). The ledger's self-check
reports `123/123` rows agreeing with the checker in both runs, and the row is not a finding
(`finding: false`) in either.

`cites` stays `0` on both sides: the row's File(s) cell lists paths without line numbers, and
the numbers the new status cell carries (`fea3571b`, `ca5f5d4`) are commit shas, not `path:N`
cites — so rewriting the cell could not strand a cite, which is the check the proposal's §1 made
before writing anything.

## What changed

| File | cfbd5bc | Change |
|---|---|---|
| `internal/collector/sql.go` | 15 / 0 | `quoteSQLLiteral(s string) string` appended after `:623` — the single-quote escape both collectors need, because `ValidateProjection` checks a spec's `Where` but never quotes it |
| `internal/collector/activity.go` | 28 / 1 | `(*ActivityCollector).specFor(v)` appended after `:900`, and the one-for-one call-site swap at `:393` from `activitySpecs[v]` to `c.specFor(v)`; the method adds `application_name IS DISTINCT FROM <quoted c.cfg.ApplicationName>` to the `pg_stat_activity` spec only |
| `internal/collector/activity_test.go` | 44 / 0 | `TestActivityExcludesItsOwnApplicationName` — the filter is present, it is `IS DISTINCT FROM` and not `<>`/`!=`, the wait-events projection carries no `application_name` clause, and a custom name `pgsi'x` is quoted as `'pgsi''x'` |
| `internal/collector/statements.go` | 56 / 12 | `excludeRole` field (paid for by merging the struct docblock from 6 lines to 5, so the file stays line-neutral above `:415`), `WithExcludeRole(role)`, and `specFor(v)` which puts `pg_catalog.pg_get_userbyid(userid) <> <quoted role>` in front of the parenthesised `statementsWhere` only when a role is configured; `:439` swapped from `statementSpecs[v]` to `c.specFor(v)` |
| `internal/collector/statements_test.go` | 53 / 0 | `TestStatementsExcludeRoleIsTheBelt` — four subtests: no role leaves the const alone (byte-equal to `statementsWhere`, Extras preserved), a role goes in front of the parenthesised text filter, `o'brien` becomes `'o''brien'`, and the `…_info` view never carries it |
| `docs/implementation/phases/phase-2-proposal-r2.8.md` | 144 / 0 | the proposal (amended once: its §6 gate bullet had quoted A2's bad cites in backticks (a cmd/pgsi/targets.go line past that file's end, a blank internal/metrics/metrics_test.go line), which the checker then parsed as *my* cites and reported as 3 wrong — a quoted cite is checkable, so the numbers are now prose) |
| `docs/implementation/phases/phase-2.md` | 1 / 1 | row `:178` rewritten from `**in-progress**` to `**implemented**`; the file is 180 lines before and after, so no row below it moved |

## Verification

| Command | rc | What it proves |
|---|---|---|
| `bash scripts/check-parity-matrix.sh` (detached worktree at `cfbd5bc`, `status --porcelain \| wc -l` → 0) | 1 | the doc/code parity gate; 0 findings name a file this commit touches — the 2 wrong path cites, 3 ambiguous basenames and 2 non-exempt d-ids are all in the two phase-4 files, and `36d6687` reports the same set |
| `gofmt -l internal/collector/` | 0 | no unformatted file among the five |
| `go build ./...` | 0 | the tree compiles at `cfbd5bc` |
| `go vet ./...` | 0 | no vet findings |
| `go test ./internal/collector -count=1` | 0 | the whole collector package, including the two new tests and the pre-existing fixture tests whose query strings `specFor` had to leave unchanged |
| MUT-A: `quoteSQLLiteral` returns `"'" + s + "'"` (no doubling) | 1 | `--- FAIL: TestActivityExcludesItsOwnApplicationName` and `--- FAIL: TestStatementsExcludeRoleIsTheBelt` — both tests really exercise the quoting, not just the string shape |
| MUT-B: `activity.go` uses `application_name <> ` instead of `IS DISTINCT FROM` | 1 | `--- FAIL: TestActivityExcludesItsOwnApplicationName` — the nullable-column rule is asserted, so the `<>` reading cannot be reintroduced silently |
| MUT-C: `statements.go` drops the `c.excludeRole != ""` guard | 1 | `--- FAIL: TestStatementsExcludeRoleIsTheBelt` — the "no role configured leaves the const alone" case is load-bearing, which is what keeps A3's shared-role ruling intact |

All three mutations were hand-run and reverted in a detached worktree at `cfbd5bc` (each
`git checkout -- <file>` back to `0` dirty lines, final `git status --porcelain | wc -l` → `0`);
`scripts/mutation-sweep.py` scopes its `SITES` to `internal/query` and `internal/store` and has
no `internal/collector` entries, so there is no sweep target to add them to yet. The rc values
are from `out=$(cmd 2>&1); rc=$?`, not from a pipe's `$?`. The gate rc is post-commit — a
pre-commit run checks the copy the index holds, and on this shared tree the two differ.

## Deviations from the spec

**The `pg_stat_wait_events` view has no `application_name` column.** The requirement says
"every count we report", and the exclusion can only be applied where the view exposes an
identifier to filter on. The activity group's conditional second view has no such column, so
our own session's waits stay in that view's counts. The code comment and the test both say so
rather than leaving it to be discovered; the filter is on the `pg_stat_activity` spec only.

**The role form is implemented at the collector but not yet selected by config resolution.**
A3's ruling makes the role filter the form "when a dedicated credential is configured at R1.8's
`--credential-secret` level", with the text filter demoted to a belt. `WithExcludeRole`
implements exactly that shape and its test pins it, but `newRegistry` does not yet call it with
the role that `internal/collector/conn.go` resolves — so today every deployment gets the
shared-role case (text form alone), which is what the ruling prescribes for that case. Wiring
the resolved credential's role through to `WithExcludeRole` is the remaining step, and it is
A1's `cmd/pgsi` change, not a collector change.

**`quoteSQLLiteral` lands in a file A5 does not own.** `internal/collector/sql.go` is A1's
(`docs/agents.md:57`); the helper is unexported and appended at that file's line 623
(`internal/collector/sql.go:623`) so no cited line in that file moves. The alternative — one quoting expression per collector — would have put two
copies of the same escape rule in two files that both build their SQL through this one.

## Answers to the proposal's open questions

§4 was `None.` — all six judgements cleared the 0.7 bar and went into §3 as stated assumptions
instead of questions, so nothing was waited on and nothing was overridden. All six stand as
written, including the one that picked `IS DISTINCT FROM` over `<>` for the `application_name`
comparison; MUT-B is the instrument that makes that assumption enforceable rather than a
preference recorded in prose.
