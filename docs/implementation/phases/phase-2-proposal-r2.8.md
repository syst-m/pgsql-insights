# Proposal: R2.8 — exclude our own sessions from every count we report

Measured `2026-11-28T11:14:37Z` · gate `rc=1` · by A5 · classification: `existing-spec` (confidence `0.9`)

<!-- Every number in this file is copied from a command that ran in the same action that wrote
     it: the header's timestamp, head and gate rc come from the
     `python3 .claude/skills/spec-implement/scripts/ledger.py R2.8 --json` run that produced §1
     (measured_at 2026-11-28T11:14:37Z, head 36d6687db16f8cebfd17122c950712b1ddc9cca4,
     gate_rc 1). The gate's failing findings, quoted in §6, come from
     `bash scripts/check-parity-matrix.sh` run in a detached worktree at that same head
     (`git worktree add --detach /tmp/a5w0 HEAD`, `git -C /tmp/a5w0 status --porcelain | wc -l`
     → 0), so the rc is the head's own and not the dirty tree's; the five files this proposal
     covers were then copied into that worktree and measured there (`gofmt -l` → no files,
     `go build ./...` → rc 0, `go vet ./internal/collector/` → rc 0,
     `go test ./internal/collector/` → rc 0) before the commit, because the real working tree
     carries A2's uncommitted `internal/metrics/metrics.go` and that file's build failure is
     not mine to grade.

     NPU: the `halogen-npu` MCP server is not configured in this project —
     `.claude/settings.local.json` is absent, so `mcp__halogen-npu__decide` is not in this
     slot's tool list. Steps 2-5's `decide`, `decision-score` and `decision-verify` calls were
     therefore answered by A5 directly rather than by `d1-2B`/`noul`. Every confidence below is
     A5's own estimate and is marked as such; the global rule is that an unreachable endpoint is
     an optimisation lost, not a dependency, so the loop continued instead of blocking. -->

## 1. Current state (measured)

```
R2.8   in-progress  36d6687:docs/implementation/phases/phase-2.md:178  code 15  first e4b9abb660d4@2026-11-25T11:48:25Z  latest 4360e217798f@2026-11-22T16:15:34Z  doc 27  first 189093a6582e@2026-11-28T01:18:38Z  cites 0  consistent
```

The row is `in-progress`, not `implemented`, so there is no "the row already says done, say so
before writing code" case. It is not a finding (`finding: false`), the ledger's self-check
reports `123/123` rows agreeing with the checker, and the row carries `cites 0` — no
`path:N` citation depends on the two files this change edits, so nothing outside the row can be
stranded by editing it.

The row's own File(s) cell names `internal/collector/sql.go` (`ApplicationName`) and
`internal/collector/database.go`, both A1's, and its status cell says why the rest is open: the
*setting* half is in `SQLConfig` on every `Begin`, and the *filtering* half is "qualified by the
requirement's own words — 'filtering it out of `pg_stat_activity`-derived metrics' — and
`pg_stat_activity` is not in this collector's view group, so that half lands with the collector
that queries it; the same for the `pg_stat_statements` `exclude_users` pattern the requirement
cites as the model." That is this change: the two collectors that query those views, plus the
SQL-level quoting both of them need.

## 2. What is being asked

Spec text, verbatim from the shell's §4 (`requirements.md:85`; the shell is the right cite for
§4 rows — `docs/requirements/**` holds the topic prose, not the id table):

> **R2.8** | Exclude our own sessions from every count we report, by setting `application_name=pgsi-collector` and filtering it out of `pg_stat_activity`-derived metrics (the postgres_exporter `--collector.stat_statements.exclude_users` pattern, applied by us at query level). Confirmed in the wild: CNPG's own exporter does exactly this with `application_name="cnpg_metrics_exporter"` (`live-probe.md` §8). **Ruling (A3, on A5's ask from `internal/collector/statements.go:104` ("// statementsWhere is R2.8's exclusion, applied at query level."): the cell names two identifiers and which one is correct depends on the role the collector connected as — the requirement's operative clause is "exclude *our own* sessions", and `application_name` and `exclude_users` are both implementations of it, not the requirement.** For `pg_stat_activity`-derived metrics the identifier is `application_name`, as the cell says. For `pg_stat_statements` there is no application_name column, so the identifier is the role or the statement text — and `internal/collector/conn.go:137-139` resolves R1.8's credential precedence to `<cluster>-app` on a zero-config cluster (`internal/collector/conn.go:42` says so in its own words), so a role filter there excludes the application's entire workload, which is the population R3.2's top-N exists to rank. **Ruled:** while the collector shares the app's role, statement-text matching is the form (`statementsWhere`); when a dedicated credential is configured at R1.8's `--credential-secret` level, the role filter is the form and the text filter is a belt. **The text form's completeness rests on R2.5:** `internal/collector/sql.go:413`'s `knownSchemas` is the closed set `{pg_catalog, pg_stat_statements}` that every Project-built query's FROM is checked against, so the two LIKE patterns cover every query this codebase issues *from a view*. The one shape R2.5 does not constrain is a function call with no FROM clause — R1.3's `SELECT current_setting('server_version_num')` fallback (`internal/discovery/build.go:51`, documented and not yet a query string in the tree) matches neither pattern, so its rows will rank in the top-N when it lands; recorded now so it is decided then rather than discovered then.

The requirement is two clauses and the row has one of them. The clause that ships is *setting*
`application_name=pgsi-collector` (`SQLConfig.ApplicationName`, `fea3571b`, sent on every
`Begin`). The clause that is missing is *filtering it out*: `ActivityCollector` queried
`pg_stat_activity` and counted its own polling session among the backends, and
`StatementsCollector` had the text form of the exclusion (`statementsWhere`) but no way to
carry the role form A3's ruling makes primary whenever a dedicated credential is configured.

## 3. Assumptions adopted without asking

| Q | Answer | Confidence (A5's own) | Why this is not a question |
|---|---|---|---|
| Which half of R2.8 is left | the filtering half, on both views | 0.92 | The row's own status cell says the setting half shipped in `fea3571b` and names the filtering half as pending on the collectors that query the two views; nothing in the tree has added a `WHERE application_name` since, which is what `ActivityCollector.specFor` now adds. |
| Which identifier per view | `application_name` for `pg_stat_activity`; the role for `pg_stat_statements` only when a dedicated credential is configured, statement text otherwise | 0.90 | This is A3's ruling quoted in §2, not a judgement of mine: "while the collector shares the app's role, statement-text matching is the form (`statementsWhere`); when a dedicated credential is configured … the role filter is the form and the text filter is a belt." The implementation gates the role clause on `excludeRole != ""` so a zero-config collector keeps exactly the text form the ruling names, and a configured one gets role-primary + text-as-belt. |
| `<>` or `IS DISTINCT FROM` for the application_name filter | `IS DISTINCT FROM` | 0.86 | `application_name` is nullable, and a comparison against `NULL` evaluates to `NULL`, so `application_name <> 'pgsi-collector'` drops every session that never set a name — background workers and any session the operator did not name — which is a wider exclusion than "exclude *our own* sessions". The test asserts the operator's own name is excluded and that no plain `<>`/`!=` form appears, so the choice is pinned rather than implied. |
| Where SQL-string quoting lives | `internal/collector/sql.go`'s new `quoteSQLLiteral` | 0.84 | Both callers build their `WHERE` through this file's `QuerySpec`, and `ValidateProjection` validates a spec's `Where` but never quotes it, so a role named `o'brien` would otherwise reach the query as `<> 'o'brien'` and fail at parse time. Putting it in each collector instead would be two copies of the same escape rule. |
| Does this change R13.1's round-trip count | no | 0.88 | §4-D20 counts queries issued, and this change alters a `WHERE` string on queries the collectors already issue — no new `Begin`, no extra query. `cmd/pgsi/budget_test.go`'s measured 23 (A1, `194aaa9`) is unchanged because `TestSQLConfigThreadsToTheOpener` counts `Begin`s per `Desc.ViewGroups`, and neither edit adds a view group or a transaction. |
| Which of the five files are mine | `statements.go` and `statements_test.go` are A5's; `sql.go`, `activity.go`, `activity_test.go` are A1's | 0.95 | `docs/agents.md:61` gives A5 `internal/query/`, `internal/store/` and the `pg_stat_statements` collector; the other three belong to `docs/agents.md:57`. They are staged by pathspec and named in the commit message, per the shared-tree rule. |

## 4. Questions for you

None. Every answer came back at or above the 0.7 bar, so there is no residue to ask. The one
answer with room to be argued — `IS DISTINCT FROM` over `<>` — is stated in §3 where a confident
answer belongs, and it can be overridden there; the test pins it either way.

## 5. Recommendation

Give each of the two collectors a `specFor(view)` method, which is the placement
`internal/collector/pgversion/matrix.go:159-163` already rules for this exact requirement ("R2.7's
BEGIN READ ONLY / statement_timeout wrapper and R2.8's `application_name = 'pgsi_confirmed'`
exclusion are per-view-group and per-collector concerns, not per-view ones, so the collector adds
them" — the matrix never emits a `WHERE`, so the collector has to). `ActivityCollector.specFor`
takes the static `activitySpecs[v]` and adds
`application_name IS DISTINCT FROM <quoted c.cfg.ApplicationName>` on the `pg_stat_activity`
spec only; `StatementsCollector.specFor` takes `statementSpecs[v]` and, when a role is
configured through the new `WithExcludeRole`, puts
`pg_catalog.pg_get_userbyid(userid) <> <quoted role>` in front of the existing parenthesised
text filter — the belt shape A3's ruling prescribes — and leaves the `…_info` view (whose spec
has no `Where`) alone. `quoteSQLLiteral` lands in `sql.go` next to the `QuerySpec` both callers
build.

Scored `5/6` on `spec-implement ship rubric` (`noul ship` not run — no NPU; A5's own P(yes) `0.88`).
Verified: grounded `0.93` · on_brief `0.89` · no_leaks `0.90` (A5's own, not `decision-verify`'s).

The one point off the score is coverage of the word *every* in "every count we report": the
exclusion can only be applied where the view exposes an identifier to filter on.
`pg_stat_wait_events` — the conditional second view in the activity group — has no
`application_name` column, so our own session's waits stay in that view's counts; that is a
column-availability limit, not a missed filter, and it is stated in the code comment and the test
rather than left to be discovered.

## 6. Plan

- **Owner:** `A5` (`docs/agents.md:61`); `internal/collector/sql.go`, `activity.go` and
  `activity_test.go` are A1-owned (`docs/agents.md:57`) and are staged by pathspec with that
  disclosure in the commit message
- **Files:** `internal/collector/sql.go` (`quoteSQLLiteral`), `internal/collector/activity.go`
  (`specFor` + the `projections` call site), `internal/collector/activity_test.go`
  (`TestActivityExcludesItsOwnApplicationName`), `internal/collector/statements.go`
  (`excludeRole` field, `WithExcludeRole`, `specFor` + the `Collect` call site),
  `internal/collector/statements_test.go` (`TestStatementsExcludeRoleIsTheBelt`),
  `docs/implementation/phases/phase-2.md`
- **Gate:** `bash scripts/check-parity-matrix.sh` — `rc=1` measured at `36d6687` in a clean
  detached worktree, and every failing finding there is in a phase-4 file this commit does not
  touch: two wrong path citations and three ambiguous basenames in
  `docs/implementation/phases/phase-4-proposal-r9.6-r9.8-r13.3.md` (a cmd/pgsi/targets.go line
  past that file's end, a blank internal/metrics/metrics_test.go line, and two basenames that
  match two files each), plus two d-id exemptions — that file and
  `docs/implementation/phases/phase-4.md` each mention the id that §4 and §5 both define without
  the `§5-D13` namespace the rule asks for. Re-run after the commit and report the post-commit
  rc.
- **Build:** `gofmt -l internal/collector/` → no files, `go build ./...` → rc 0,
  `go vet ./internal/collector/` → rc 0, `go test ./internal/collector/` → rc 0, all measured in
  the detached worktree at `36d6687` with these five files copied in (the real tree's uncommitted
  `internal/metrics/metrics.go` has a build failure that is A2's in-progress work)
- **Line neutrality:** every cited line in the three pre-existing files is preserved — `sql.go`'s
  twelve cited lines are all ≤ `:623` and the new function appends after it; `activity.go`'s only
  in-place change is the one-for-one call-site swap at `:393` (the row at `:397` is unmoved) with
  everything else appended after `:900`; `statements.go`'s eight cited lines are preserved by a
  one-for-one swap at `:439` and a docblock merge (6 lines → 5) that pays for the new field, so
  the file is line-neutral above `:694` and `:850`
- **Commit:** `A5: R2.8 — filter our own sessions out of the activity and statements queries`
  with pathspec (`git commit -- <paths>`, never a bare commit on this shared tree),
  `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`
- **Also updates:** `docs/implementation/phases/phase-2.md` row `:178` in the same commit, and
  the record at `docs/implementation/phases/phase-2-record-r2.8.md`

## 7. Awaiting

The assignment said "propose, then implement, then write the record", so step 6 proceeds in this
same turn rather than waiting on an answer: §4 is empty, so there is nothing to answer first.
Nothing is pushed — the coordinator owns the push.
