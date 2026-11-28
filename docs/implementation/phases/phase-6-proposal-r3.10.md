# Proposal: R3.10 — plan samples, captured with `PREPARE` + `EXPLAIN EXECUTE`

Measured `2026-11-28T12:00:32Z` · gate `rc=1` · by A5 · classification: `existing-spec` (confidence `0.9`)

<!-- Every number in this file is copied from a command that ran in the same action that wrote
     it: the header's timestamp, head and gate rc come from the
     `python3 .claude/skills/spec-implement/scripts/ledger.py R3.10 --json` run that produced §1
     (measured_at 2026-11-28T12:00:32Z, head 887040024f992c3ccacff14348c48b3761a58307,
     gate_rc 1). That run was made in a detached worktree added in the same call as its own
     cleanliness check (`git worktree add --detach /tmp/a5w10 8870400…` and
     `git -C /tmp/a5w10 status --porcelain | wc -l` → 0), and the ledger's `root` field reads
     `.` — so the cwd the script is invoked from is part of the measurement, and this file's
     numbers are the worktree's, not the main checkout's.
     The gate's failing findings, quoted in §6, come from `bash scripts/check-parity-matrix.sh`
     in that same worktree. The five files this proposal covers were then copied into a second
     worktree at the same rev and measured there (`gofmt -l ./` → no files, `go build ./...` →
     rc 0, `go vet ./internal/collector/` → rc 0, `go test ./internal/collector/ -count=1` →
     rc 0, 0.852s) before the commit, because the real working tree carries other slots'
     uncommitted files and their build failures are not mine to grade.

     NPU: the `halogen-npu` MCP server is not configured in this project —
     `.claude/settings.local.json` is absent, so `mcp__halogen-npu__decide` is not in this
     slot's tool list. Steps 2-5's `decide`, `decision-score` and `decision-verify` calls were
     therefore answered by A5 directly rather than by `d1-2B`/`noul`. Every confidence below is
     A5's own estimate and is marked as such; the global rule is that an unreachable endpoint is
     an optimisation lost, not a dependency, so the loop continued instead of blocking. -->

## 1. Current state (measured)

```
R3.10  in-progress  docs/implementation/phases/phase-6.md:231  code 13  first 62740ed42594@2026-11-24T12:30:44Z  latest 65ed2eec565a@2026-11-22T23:52:42Z  doc 23  first 189093a6582e@2026-11-28T01:18:38Z  cites 0  consistent
```

The row's own status cell already says which half is missing, in as many words: *"the two
components and the route shipped; the capture did not."* Its File(s) cell names
`web/src/components/PlanList.tsx`, `web/src/components/PlanViewer.tsx`, `web/src/pages/QueryDetail.tsx`
and `internal/api/query_plans.go` — A4's and A6's UI and A2's handler — and no file under
`internal/collector/`, which is where the capture has to land. The row is not a finding
(`finding: false`), the ledger's self-check reports `133/133` rows agreeing with the checker,
and the row carries `cites 0`: no `path:N` citation depends on the two pre-existing files this
change edits, so rewriting the cell cannot strand a cite outside it. That is the check the
proposal makes before writing anything, and it is why the row can be rewritten in the same
commit as the code.

## 2. What is being asked

Spec text, verbatim from the shell's §4 (`requirements.md:101`; the shell is the right cite for
§4 rows — `docs/requirements/**` holds the topic prose, not the id table):

> **R3.10** | **Plan samples.** Capture `EXPLAIN (FORMAT JSON)` for top-N queries on a bounded schedule (default 5 plans/minute/cluster, Cloud SQL's `queryPlansPerMinute` default; Enterprise Plus allows 200 — we expose the knob and default to the cheap value). Three constraints the 0.1.0 text missed: (a) the stored template is normalized (`$1`,`$2`) and truncated, so `EXPLAIN` of it is a syntax error — we EXPLAIN the **first-seen raw text** (R3.4) and **skip truncated rows**; (b) for a parameterized statement we use `PREPARE` + `EXPLAIN EXECUTE` to get the generic plan rather than a pet plan; (c) on a `pg_monitor`-only deployment (R12.4) the role has statistics visibility but **no `SELECT` on application tables**, so EXPLAIN returns `permission denied for table …` — we report `unavailable (insufficient table privileges)` as a state, not as a per-query failure. `EXPLAIN ANALYZE` is **never** run automatically (it executes the statement); it is available on explicit user request, and only for `SELECT`s unless `--allow-analyze-write=true`. |

The row at `docs/implementation/phases/phase-6.md:231` is the assignment of this requirement to
Phase 6, and it explains its own presence there ("The row lives here rather than in §3.7 because
§3.7 says so in as many words"). Everything the requirement names is missing from the tree:
there is no code path that produces a `PlanSampleView` — the dto type exists
(`internal/api/dto/queries.go:442`) and its docblock names R3.10 as its reason for existing, but
`internal/collector/statements.go` built rows with `Plan` left nil, and its docblock said so.

## 3. Assumptions adopted without asking

| Q | Answer | Confidence (A5's own) | Why this is not a question |
|---|---|---|---|
| Where the capture lives | a component of the statements collector (`plans.go` in that package, reached from `Collect`), not a new registry collector | 0.90 | `internal/collector/collector.go:287` says a Descriptor's `Name` is "one of §3.3's collector names; the schedule table is the list", and §3.3's table has no `plans` row — so a registry entry would mint a name the spec's list does not contain, which then becomes the `collector` label value on both R2.2 families and the address of R2.3's `--interval.<collector>=` flag. The alternative that does not invent a name is a component of the collector that already reads `pg_stat_statements`, on the transaction it already opened. |
| How the prepared-statement set is bounded (the assignment names this as the known failure mode: "preparing per-poll grows the prepared-statement set unboundedly") | three mechanisms, named: a **deterministic** name (`pgsi_` + the first 16 hex characters of R3.4's fingerprint, `internal/query/normalizer.go:178`), an **immediate** `DEALLOCATE` after the EXPLAIN, and the round's transaction as Postgres's own backstop | 0.88 | A counter-derived name (`pgsi_1`, `pgsi_2`, …) makes the set grow one entry per poll per statement forever, which is the failure the assignment names. A fingerprint-derived name cannot: re-preparing the same statement replaces the same name, so the set's size is bounded by the number of *distinct statements* captured, not by the number of polls — and the `DEALLOCATE` shrinks it back to empty in the same transaction, so the steady-state set is empty and the peak is the current round's cap. `prepareName` (`internal/collector/plans.go:421`) reuses the exported `Fingerprint` rather than adding a second sha256 call site, so there is one fingerprint implementation. |
| Which text gets EXPLAINed | the row's first-seen raw text, never the template; and truncated rows are skipped entirely | 0.95 | This is the requirement's own (a), quoted in §2. `internal/query/normalizer.go:147`'s `Normalize` returns `Normalized{Template, Fingerprint, DisplayText, Truncated, Params}`, and `Truncated` is the flag (a) needs; the test forces it true with `query.Options{Length: 64}` rather than trusting a fixture that happens to be short. |
| How the generic plan is forced | `SET LOCAL plan_cache_mode = force_generic_plan`, issued **once per round** and only when the round contains at least one parameterized capture | 0.86 | (b) asks for the generic plan. A parameterless prepared statement already gets a generic plan, so the setting only matters for the `PREPARE` path — and `SET LOCAL` is scoped to the transaction, so one statement covers every capture in the round instead of one per capture. Issuing it conditionally keeps a round of plain `EXPLAIN`s from paying for a setting it cannot use. |
| How a failed capture is survived | a `SAVEPOINT`/`RELEASE` pair around each capture, with `ROLLBACK TO SAVEPOINT` on failure | 0.92 | A failed statement aborts a Postgres transaction, so without a savepoint the (c) permission error would poison every later query in the round *and* the `COMMIT`. §4-D20 (`requirements.md:266`) counts "queries issued, not transaction control statements", so the savepoints are free — which is the only reason the cost model fits the budget. The rollback is also what discards a `PREPARE` that succeeded before a later query failed, so a failed capture leaves nothing behind. |
| What this costs against R13.1 | at the default cap of 5, all parameterized: 5×3 + 1 = 16 queries, on top of the 23 A1 measured (`cmd/pgsi/budget_test.go:143`, `194aaa9`) → 39 < 40 | 0.84 | §4-D20's rule is what makes the arithmetic work: the six savepoints and the `SET LOCAL` are transaction control or shared, so the counted number is 1 per plain capture and 3 per `PREPARE` capture. The budget test's fixture answers every query with no rows, so the sampler picks nothing, issues nothing, and the measured 23 is unchanged — `TestStatementsWithoutASamplerIssueNoPlanQueries` pins the 2 for a no-sampler round so the default path is provably untouched. |
| The 200-plans/minute setting vs the 40-round-trip budget | a real tension, recorded here and since priced by the spec | 0.80 | R3.10 lets the knob go to 200 (Enterprise Plus). 200 captures at 3 queries each is ~600 queries in a round, which is 15× R13.1's `< 40 round-trips`. The two requirements were only compatible at the default 5 — which is what the spec itself defaults to ("we expose the knob and default to the cheap value"). This row first recorded that as unresolved; A3 has since priced it in R13.1's row (`requirements.md:266`, commit 303462d, mirrored by a pointer in this requirement's own row at `requirements.md:101`): the budget governs the collector round and does not cap R3.10's capture rate, so the knob is itself the budget for plan-capture queries and 200 takes the round to 23 + 601 = 624 without failing R13.1. The cap is validated (`internal/collector/plans.go:126`) and clamped (`internal/collector/plans.go:157`) so a config of 500 cannot smuggle in an unbounded round. |
| Which files are mine | all five: `plans.go` and `plans_test.go` (new), `statements.go` and `statements_test.go`'s package, and the two phase-6 docs | 0.95 | `docs/agents.md:61` gives A5 `internal/query/` (normalizer, top-N), `internal/store/`, and the `pg_stat_statements` collector. Unlike the R2.8 commit, nothing here is staged from another slot's file list — `statements.go` is the collector this row owns. |

## 4. Questions for you

None. The plan was approved as stated before implementation began, so §3's rows record the
judgements that were already accepted rather than asking them again; the one item with room to be
argued — the 200/minute-vs-budget tension — is stated in §3 where a confident answer belongs and
can be overridden there. It has been: A3 ruled it in R13.1's row (`requirements.md:266`, commit
303462d), and §3's row now points at the ruling instead of at the code comment alone.

## 5. Recommendation

`internal/collector/plans.go` adds a `PlanSampler` that the statements collector reaches on the
transaction it has already opened, before the `COMMIT`. Per round it: picks the eligible rows
(`internal/collector/plans.go:270`) — ranked by `total_exec_time` descending, template ascending
as the tie-break, capped at `PlansPerMinute`, skipping rows whose normalized text is truncated
(R3.10's (a)) and rows whose statement kind `EXPLAIN` cannot take (`internal/collector/plans.go:328`,
which reuses `internal/collector/activity.go:499`'s existing `queryTypeFrom` rather than adding
a second keyword classifier, so `VACUUM`, `SET` and `BEGIN` rows are filtered by the same rule
the activity collector already applies); issues `SET LOCAL plan_cache_mode = force_generic_plan`
once if any pick is parameterized; then captures each plan and returns the samples **keyed by
index into the rows slice** — a caller has two fingerprints for one row and R3.10 names neither as
the plan's key, so the index is the one identity that cannot disagree.

`internal/collector/plans.go:378`'s `planSQLFor` is the two paths (a) and (b) name: a
parameterless statement is `EXPLAIN (FORMAT JSON) <raw>` (one query — `PREPARE` would add two
more for a statement that already has only one plan), and a parameterized one is
`PREPARE pgsi_<fp> AS <template>` + `EXPLAIN (FORMAT JSON) EXECUTE pgsi_<fp>(NULL, …)` +
`DEALLOCATE pgsi_<fp>` (three). The `PREPARE` uses the **template**, not the raw text, because
the template is what carries `$1`,`$2` and `Params` is what says how many arguments `EXECUTE`
needs; `NULL` is the only type-compatible, plan-neutral argument. Nothing in either path spells
`ANALYZE`, which is the requirement's prohibition and is asserted textually in the test.

`internal/collector/statements.go` gains a `plans *PlanSampler` field, a `WithPlanSampler`
setter, and a `rowWithPlan` helper; the capture call is inserted between the last view query and
the `COMMIT`, and the round's `RoundTrips` grows by the sampler's own count. A collector built
without a sampler — every deployment built before this file existed, including
`cmd/pgsi/main.go:1090` — still issues exactly its two view queries and leaves every row's
`Plan` nil, which `TestStatementsWithoutASamplerIssueNoPlanQueries` pins.

Scored `5/6` on `spec-implement ship rubric` (`noul ship` not run — no NPU; A5's own P(yes) `0.90`).
Verified: grounded `0.91` · on_brief `0.90` · no_leaks `0.87` (A5's own, not `decision-verify`'s).

The one point off is the §3 row the spec left unpriced at the time of writing: at the Enterprise
Plus 200 the capture alone would issue ~600 queries against a `< 40` budget, so the requirement's
own upper setting contradicts R13.1 and nothing in the tree then decided which one wins. The
default — the value the spec defaults to — fits with one to spare (39 of 40), and that is what the
code's budget comment claims. The tree has since decided it: A3's ruling at `requirements.md:266`
(commit 303462d) holds that the budget governs the collector round and R3.10's knob budgets plan
capture, so the 200 setting takes the round to 624 queries without failing R13.1.

## 6. Plan

- **Owner:** `A5` (`docs/agents.md:61`). Every file in this commit is on that row's list; no
  cross-slot staging and therefore no staging disclosure beyond this line.
- **Files:** `internal/collector/plans.go` (new, 584 lines — the sampler, the two capture paths,
  the savepoint wrapper, the plan-hash derivation), `internal/collector/plans_test.go` (new,
  657 lines — eleven tests), `internal/collector/statements.go` (the field, the setter, the capture
  call, `rowWithPlan`), `docs/implementation/phases/phase-6-proposal-r3.10.md` (this file),
  `docs/implementation/phases/phase-6.md` (row `:231`).
- **Gate:** `bash scripts/check-parity-matrix.sh` — `rc=1` measured at `8870400` in a clean
  detached worktree (`git -C /tmp/a5w10 status --porcelain | wc -l` → `0` in the same call). The
  one failing finding there is a table-width mismatch in
  `docs/implementation/phases/phase-6-record-r4.1-r4.3-r4.5-r4.7.md` — A6's record, landed in
  the commit this one is built on — and the run reports `0 wrong` path citations, `0 ambiguous`,
  `0 not exempt` d-ids, `0 non-canonical` status cells and `0 broken` links, so nothing in the
  failing set is in a file this commit touches and nothing here can fix it. Re-run after the
  commit and report the post-commit rc: the commit-message tier parses cites out of the message,
  so the artefact being created is one of the gate's inputs and a pre-commit run scores every
  file while reading no messages.
- **Build:** `gofmt -l ./` → no files, `go build ./...` → rc 0, `go vet ./internal/collector/` →
  rc 0, `go test ./internal/collector/ -count=1` → rc 0 (0.852s), all measured in a detached
  worktree at `8870400` with these files copied in. The rc values come from
  `out=$(cmd 2>&1); rc=$?`, not from a pipe's `$?`.
- **Line neutrality:** `statements.go` goes 916 → 945 lines. Every cited line in it — `:104`,
  `:127`, `:415`, `:438`, `:443`, `:458`, `:479`, `:694`, `:850`, all nine found by grepping the
  tree's markdown for `internal/collector/statements.go:[0-9]+` at `8870400` — is byte-identical
  to HEAD's copy, checked by comparing each line's text rather than by insertions/deletions
  arithmetic. The +29 is the appended block after `:850` plus the capture call; the `Plan`
  docblock merge pays for the new struct field.
- **Commit:** `A5: R3.10 — capture plan samples with PREPARE + EXPLAIN EXECUTE` with pathspec
  (`git commit -- <paths>`, never a bare commit on this shared tree),
  `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`
- **Also updates:** `docs/implementation/phases/phase-6.md` row `:231` in the same commit (the
  row's File(s) cell gains the two collector files by path, without a sha — the record names the
  sha), and the record at `docs/implementation/phases/phase-6-record-r3.10.md`.

## 7. Awaiting

The assignment was "propose, implement, record" and the plan was approved as stated, so step 6
proceeds in this same turn: §4 is empty, so there is nothing to answer first. Nothing is pushed —
the coordinator owns the push.
