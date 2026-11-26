# Agent specification

The rules for how this project uses subagents, written down so a future run reproduces the same
setup instead of rediscovering it. The constraint comes from the original brief; everything after
§2 is practice that the first run earned and that is easy to lose.

**Machine-readable definitions live in [`.claude/agents/`](../.claude/agents)** — one file per
slot, with the frontmatter (`name`, `description`, `model`) the Agent tool needs to spawn them.
This document is the *why* and the *expectations*; those files are the *what gets loaded*. If the
two disagree, the frontmatter wins on spawn mechanics and this file wins on charter.

---

## 1. The cap (from the brief, amended by the user on 2026-11-22)

The brief's original wording:

> *"you shall spread the work throughout this WHOLE process amongst a maximum of 4 subagents —
> two opus and two sonnet — a total of 4 subagents at maximum."*

The user's amendment, verbatim:

> *"let's bump up to 6 total subagents max- 3 sonnet, 3 opus - going forward"*

**Six slots now. Three opus, three sonnet.** The cap is still **for the whole process**, not per
phase — it is spent once and stays spent. Adding a seventh agent, even briefly, violates it; so
does letting an opus slot execute sonnet-class work while the sonnet slots are full.

**Consequence worth stating plainly:** once the six exist, all further work is *reuse*. New work
goes to an existing slot via `SendMessage`, never to a fresh `Agent` spawn. A spawn that appears to
be "just a quick extra agent for this one thing" is the most common way this project breaks its own
constraint — the amendment raised the number, it did not remove the rule.

## 2. Saturation (the cap is a floor, not just a ceiling)

Respecting the cap and using it are different things. A run where one agent works and the rest wait
is *compliant and wasteful* — the budget is being spent on serialization. This was called out on
2026-11-21 (*"you should try to scale out the work more to max of 4 total subagents"*) after Phase
1a was progressing through one agent while three sat idle, and again on 2026-11-22
(*"be sure to keep the 4 slots active"*) — which is what led to the cap being raised to six rather
than the idleness being fixed. Six idle-capable slots serialising through one is worse, not better,
than four doing it.

**The rule:** before starting work, enumerate which slots are active, and dispatch to the idle ones
*in the same turn*. This matters most when the critical path is narrow — Phase 1a → 2 → 3 is a
single chain, so only one agent can be *on* it, and the answer is to pull work off the chain
(the chart half, the docs half, the review queue), not to wait.

**And check the roster before addressing a message.** Sending a phase brief to the wrong slot
produces two agents on one phase and one idle — which happened once (a Phase 4 brief went to A4,
who owns frontend/Helm/docs, when the phase is assigned to A2).

## 3. The six slots

| Slot | Name | Model | Spawned for | Now owns |
|---|---|---|---|---|
| **A1** | `a1-backend` | opus | Cloud SQL Query/System Insights research | Go backend: discovery, collector framework, PG version matrix, `cmd/pgsi`, `api/dto`, `internal/api/server.go` |
| **A2** | `a2-api-metrics` | sonnet | CloudNativePG surface research | Go API surface: HTTP handlers, OpenAPI, auth/CORS middleware, `/metrics` export, PromQL pair; **`ci.yml`, the root `Makefile` and `.golangci.yml`** (R11.11 makes the first two one contract -- D6 and the `4e10ed4` writeup are the cost of splitting them; A4's charter excludes the Makefile, so their Makefile commits are edits outside their row; `.golangci.yml` is the config `ci.yml:134` runs, reached through that contract, and `748e7ae` is the precedent for an A2 commit there) |
| **A3** | `a3-review` | opus | Round-1 adversarial architecture review | Architecture, spec maintenance, per-phase validation, review of every spec change |
| **A4** | `a4-frontend` | sonnet | Round-1 adversarial product review | `web/` shell + build (`App.tsx`, `routes.ts`, `web/src/api/`, `web/src/lib/`, `web/src/state/`, `styles/`, `dist`, `e2e`, `web/src/test/`, and the `web/*.config.*` + `embed.go` at the `web/` root), Helm chart, packaging, docs |
| **A5** | `a5-query-store` | opus | *(added 2026-11-22 with the cap raise)* | `internal/query/` (normalizer, top-N), `internal/store/`, the `pg_stat_statements` collector — **Phase 3** |
| **A6** | `a6-insights-ui` | sonnet | *(added 2026-11-22 with the cap raise)* | `web/src/pages/**`, `web/src/components/**` — the Query Insights UI, **Phase 6b** |

Phase → slot assignment is authoritative in [`requirements.md` §7.2](../requirements.md), not here.
This table is the *who*; §7.2 is the *what*. The roster table in
[`IMPLEMENTATION.md` §2](../IMPLEMENTATION.md) is the same six and must not diverge from this file.

**What moved when the cap went to six.** Two phases were reassigned rather than two slots given
invented work, and both moves fix a pre-existing awkwardness rather than creating a new one:

- **Phase 3: A1 → A5.** Phase 3 (`pg_stat_statements` collector + normalizer + query store + top-N
  API) was the largest untouched chunk on A1's queue and the only one with no in-flight code to
  collide with — it was ⬜, so nothing had to be handed over mid-flight. A1 keeps Phases 1a, 2, 6a
  and 8, and the collector *framework* that A5's statements collector plugs into.
- **Phase 6b: A2 → A6.** 6b is `web/` UI work that §7.2 had assigned to a slot owning no `web/`
  files — so A2 was speccing pages it couldn't build, and A4 owned the files it needed. A6 now
  builds 6b's pages, A4 owns the shell and route table that mounts them, and A2 stays in its own
  domain (Phases 4 and 5) and supplies 6b's endpoints.

The general rule this encodes: **when the cap rises, take a phase off a slot that was never the
right owner for it before taking work off one that was.** A new slot adjacent to an overloaded one
creates a two-owners-one-file problem; a new slot holding a phase the previous owner couldn't
build creates a clean handoff.

## 4. Per-slot charter

Each slot's full instructions are in its own agent file. What follows is the part that doesn't fit
in frontmatter: the quality bar and the **observed failure modes** — real defects this run produced,
recorded because a charter that doesn't name its own failure modes doesn't prevent them.

### A1 — Go backend (opus)

**Deliverable standard:** code that compiles *in the working tree*, not code that compiles in the
agent's head. `go build ./...` green and `go vet ./...` clean before reporting.

**Observed failure modes**
- Shipped `internal/discovery/build.go` with 11 compile errors sitting uncommitted in the shared
  tree. Go stops reporting after 10 errors, so fixing only the compiler-named ones surfaced four
  more — the fix list has to come from a compile run, not from the error output.
- Set `ready` immediately after `net.Listen` in `server.go`, so `/readyz` answered `200` with an
  empty fleet. R8.7 now has a readiness corollary specifically because of this: one signal, set by
  the discovery loop, read by both `/readyz` and the API's `ErrNotReady` path.
- Froze an API contract (`internal/api/dto/clusters.go`) that the UI half had already transcribed
  differently — which is the R16.8 problem, but on the author's side of it.

### A2 — API surface, metrics (sonnet)

**Deliverable standard:** every route in code is documented (R14.7), and a documented route must
not 404. Routes that are planned but unwritten go in an `x-planned-paths` extension, never in
`paths`.

**Observed failure modes**
- Authored `web/src/api/types.ts` as a **second copy of the API contract**, written from spec prose
  rather than from the Go DTO, and it disagreed in every nested type. The class of bug: `as T`
  checks nothing, and `raw.clusters ?? []` turned a missing key into a plausible empty page — so
  the failure was silent, not red. Fixed by making `tsc` the drift check (`resolveJsonModule` +
  importing the golden JSON), which is the pattern R16.8 exists to enforce.
- Committed two invented metric names (`PGSICacheHitRatioLow`, `PGSILockWaitDominant`) in a
  PrometheusRule for a metrics family the spec names as a *family* only — freezing names ahead of
  the phase that chooses them, and citing R9.4 as the source for a member it does not list. The
  ruling was **name + mark provisional + bound the blast radius**, not defer: `promtool check
  rules` validates expressions and a stub has none. The containment (a header note separating the
  two speculative rules from the fourteen the spec names) was done right; the missing spec member
  went to A3 and is now in R9.4.
- Wrote a phase's validation entry before the code review ran on that phase's code, inverting the
  standing policy.

### A3 — Review, spec, validation (opus)

**Deliverable standard:** findings are *adversarial and specific* — file, line, and the concrete
input that produces the wrong output. A finding without a failure scenario is an opinion.

**Observed failure modes**
- The reference parser A3 wrote for the parity checker printed `mismatches: 3` and **still exited
  0** — report-without-enforcing, the same class A3 was raising in others' code. A checker that
  cannot fail the build is a comment.
- Reviewed a spec amendment and found four defects, two of them typos introduced by that
  amendment — which is the right output from a reviewer, and the reason the review policy is
  "A3 reviews every spec change," including changes A3 did not author.
- Read `cmd/pgsi/main.go` end to end and never noticed that §7.2 lists **R13.7** under Phase 1a,
  so the phase shipped with no logging requirement satisfied. Reading a file is not checking it
  against the requirement set; the two checks are different and A3 did only the first.
- Reported "7 confirmed" and then listed eight findings, having dropped one while summarising. The
  count and the list are both claims, and summarising one from memory instead of from the list is
  how a review under-reports its own output.

**Standing policy:** A3 reviews every spec change, and a code review runs on every phase's code
*before* that phase's validation entry is written — not after.

### A4 — Frontend, chart, packaging, docs (sonnet)

**Deliverable standard:** `tsc --noEmit` clean, Vitest green, Playwright green, `helm lint` clean
on every named profile, and `make render-check` producing an empty diff against the committed
manifests in `deploy/plain-yaml/`. Chart library choice is recorded in `IMPLEMENTATION.md` §4
before any chart code is written (R10.2) — the `dataviz` skill names no library, so the choice is
ours to record, not to infer.

**Observed failure modes**
- Built the UI's API types from the spec's field list while the real contract was still in flight,
  then shipped a client that cast the response and defaulted a missing key to `[]` — a blank page
  indistinguishable from an empty fleet.
- Vite's `base` left at the relative default, which 404s the bundle on any nested route
  (`/c/<ns>/<cluster>` asks for `/c/<ns>/assets/index.js`).
- Received a Phase 4 brief that belonged to A2; the fix was to narrow A4 to the chart half they do
  own rather than run two agents on one phase.

## 5. Shared operating contract

These apply to every slot.

1. **Commit by explicit path — and name the path to `git commit`, not just to `git add`.** Four
   agents share one working tree; a blanket add sweeps another agent's in-flight files into your
   commit and attributes their work to you. The subtler form of the same bug is `git add <path>`
   followed by a bare `git commit`: `git commit` commits the whole *index*, not merely what you
   just added, so a one-line spec fix once shipped 21 of another slot's staged files (6,020 lines —
   `4e10ed4`, "R8.1b: correct the operation count to 15": 22 files, reset away and re-committed as
   `3c18f45` plus `362e8d3`) under its message. `git commit -- <path> [<path>…]` commits only the
   named paths and leaves the rest of the index staged for its owner.

   The same rule has a second half, about **under**-inclusion, and it is now shared contract rather
   than A4's own note: *a commit that takes part of a change set must take all of it,
   transitively.* After any commit that absorbs files from another slot's tree — or splits your own
   change set in two — grep the new commit for every command its own files invoke (`make <target>`,
   `scripts/<x>.sh`, `bin/<tool>`, a CI `run:` line, a Go `//go:generate`) and confirm each one
   resolves in that same commit. **Read the check transitively:** a `Makefile` is itself a file that
   invokes commands, so its recipe lines are in scope too, not only the `run:` lines of the workflow
   that calls the Makefile. Two instances, both A4's: `4e10ed4` shipped A2's `ci.yml` without the
   `Makefile` that file runs, so `make: *** No rule to make target 'openapi-check'` at the tip from
   `362e8d3` onward; and `45a69a1` passed the one-level reading while shipping the `Makefile` whose
   `openapi-check:` target runs `python3 scripts/check-openapi.py` with that script still
   uncommitted in A2's tree (closed at `02381ba`). The rule was satisfiable both times — the two
   files had to ship together, or neither. It binds every slot and not only the one that has broken
   it, because any commit can contain a file that invokes something: A1's Go files have
   `//go:generate` and `testdata/` reads, A2's have `scripts/check-openapi.py`, and A3's own parity
   checker is a script a Makefile target runs.

   The rule has a third half, about **wrong content** rather than over- or under-inclusion: *a
   pathspec commit takes the working tree's content for that path, so the commit is only yours if
   the working tree is.* `git commit -- <path>` does not read the index you built — it snapshots the
   file as it sits on disk. Two consequences. A pathspec commit made while another slot's edit is in
   that same file commits their edit too, under your message, so the index isolation the command
   appears to offer is not real for a shared file. And a writer that fails before it writes
   produces exactly that shape: the file is byte-identical to HEAD, and the commit succeeds with
   your message and an empty diff for the change you meant to make. A2 hit both at once at
   `3b5c774` (first committed as `1dd6286`, then amended): the script that was to rewrite a table
   row died with a `SyntaxError`, so the file it "wrote" was HEAD's own bytes, and the commit
   shipped A3's §3.5 hunk under A2's message with none of A2's intended text. **The guard is one
   command, run before the SHA is announced:** `git show --stat <sha>`, plus a grep for a string
   only your edit contains. If the diff isn't what you wrote, amend rather than stacking a fix-up
   commit on top — safe while the commit is HEAD and nobody has built on it, and worth saying in the
   message when the commit also carries someone else's hunk.

   The rule has a fourth mode, about **paths git has never seen**, and it is the only one that
   **fails loudly**: the pathspec must name a path git already knows. `git commit -- new.go` on a
   file that is not in the index exits 1 with `error: pathspec 'new.go' did not match any file(s)
   known to git` and commits nothing — reproduced in a scratch repo: exit 1, HEAD unchanged, the
   other slot's staged file left staged. So a new file needs `git add <path> && git commit --
   <path>`, and that is why the first two halves compose instead of conflicting: the `git add` is
   mandatory for new files, and the pathspec on the commit is what keeps that add from sweeping the
   rest of the index. The other three modes all ship a wrong commit; this one refuses to ship
   anything, so it needs no verify-before-announcing guard, only the add.

   Four modes of rule 1 now, three of them instances in this repo: index sweep (`4e10ed4`),
   recipe-line reference (`45a69a1`), working-tree content from a silently failed writer
   (`3b5c774`, wording from A4's postmortem), and the untracked pathspec (no repo instance;
   reproduced in a scratch repo, wording from A3).
2. **One commit per phase**, containing that phase's code **and** its `IMPLEMENTATION.md` update
   (R14.1). Small in-phase commits are fine; the phase-closing commit carries both.
3. **Attribution is the committing model's own**, not a fixed string:
   `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` from an opus slot,
   `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` from a sonnet one.
4. **Requirement IDs are the contract.** Code comments reference `R3.4`, not "the fingerprinting
   bit". Every §4 requirement maps to exactly one phase (spec §7.3), and
   `scripts/check-parity-matrix.sh` enforces it — a spec edit that breaks the parity check is a
   defect, and CI runs it.

   **The id column is not uniformly formatted, and that is a trap for anyone scripting off line
   prefixes.** 42 of §4's 176 ids are written bold — `| **R10.20** |`, not `| R10.20 |` (the set
   includes R1.10, R1.11, R2.9, R3.15, R5.10, R8.1, R8.1b, R8.1c …). A `grep '^| R10.20 '` on such
   a row returns nothing, so a script that derives the id set from a `^| R` prefix silently drops
   nearly a quarter of the taxonomy and still reports success — the failure mode is a green run over a short
   list, not an error. `check-parity-matrix.py` strips the emphasis markers when it parses the
   cells (`clean()` at `:10`, and `:48`), so the checker is not affected; the fix for a *new* script
   is to call the checker's parser rather than re-derive the set with grep.

   The rule has a citation half, enforced by the same script as of `88929b3`. **An id cited
   anywhere in the repo must be an id §4 defines, and a `file:line` citation must point at what
   it claims.** Both classes shipped defects this week: phantom ids (`R15.4`/`R15.5` in the
   licenses README, and `coverage gate (R14.5)` in `ci.yml:100` — the gate is R11.11) and stale
   numbers (`ed58452` inserted 65 lines at `chart/README.md:93` and invalidated every number
   below it, three of which survived two rounds of reading, because a wrong number is still a
   plausible number). The script now checks every `R…` mention in tracked markdown, yaml, Go and
   the Makefile against §4's set — twelve ids are exempted with a named reason, each one a
   documented alias rather than an invention — and checks path citations in three forms:
   `path:N`, `path:N-M`, the durable `file § "Heading" (:N)`, and the bare `` `:N` `` that
   inherits its file from the line's first citation. **Prefer the durable form.** A range cite
   rots twice (an insertion inside moves the end, an insertion above moves the start); a
   heading-start cite rots once, and because the number is compared against the heading text, a
   rotted one fails the build instead of reading plausibly. The half CI cannot replicate is
   catching it *in the diff*: reviewing a commit that inserts lines into a file means grepping
   for citations pointing past the insertion. The checker catches the result one commit later;
   the reviewer can catch it before the merge, and that is the only version that costs nothing.
5. **Never add a metric or route that isn't in the docs** (R14.7). If a phase needs a name the
   spec doesn't have, mark it provisional and say so — don't invent it silently.
6. **Stay out of files another slot owns while they're mid-flight.** Write the change and flag it,
   or ask for it — don't edit over an in-flight file. (A2 needed a change in A1's `server.go`; it
   was written inert and flagged rather than committed over their tree.)
7. **R1–R16 is a closed taxonomy.** New requirements are filed *under* an existing group; minting
   R17+ breaks it (spec §11 rule 4). A3's "R24"/"R25" became R16.8 and R14.9 for exactly this
   reason.
8. **Read the `dataviz` skill before any chart code** (R10.2) and record the library choice in
   `IMPLEMENTATION.md` §4. A gate, not a suggestion.
9. **Run a mutation probe against an extract of the commit, not the live tree.**
   `git archive <rev> | tar -x -C /tmp/x`, mutate the copy, run the checker there. The extract
   pins the revision under test, cannot dirty the shared tree, and cannot collide with a peer
   committing mid-round — which is the failure three slots hit in one day. A3 `cp`'d a probe built
   on `936b190` onto `scripts/check-parity-matrix.py` after HEAD had moved to `ab30743`, silently
   reverting A4's hunk; it survived only because `git diff --stat` reported 20 insertions / 34
   deletions where the probe's own diff was 19/8, so the fix was `git checkout --` plus re-applying
   the change as five asserted string replacements (`f14d974`). A4 built `patched.py` on `936b190`
   and re-derived it against the new base instead of copying it — the rule followed rather than
   discovered. And A4 ran a link-stripping probe from the repo root instead of inside the clone
   and stripped 11 real files; the wrong "70 checked" in their own output is what caught it — the
   instance this rule first credited to A2, who ran the D12 hash brute-force and the two archive
   extracts and not the nolinks probe, and filed the correction themselves. Two rules
   fall out of the three instances: **an extract, always, for any probe that writes**; and **read
   the diffstat before you commit** — a diffstat that disagrees with the edit you made is the
   finding, whether the edit was correct or not.
   Scope note on the extract, from A2 and verified: a `git archive` export has no `.git`, so
   `tracked_files()` takes its non-git fallback (`rglob` minus `.git/node_modules/dist/build`)
   rather than `git ls-files`. The two agree only when the tree holds nothing untracked or ignored
   beyond those four names — the live tree lists 210 tracked against 219 on disk — so an extract
   tests the tracked set exactly, and a probe run in a dirty working tree through the fallback
   checks files the repo does not have yet.
10. **A fact written in two files needs a third thing asserting they agree.** The phase-status
   claim lives in three places — `README.md`'s status line, `IMPLEMENTATION.md` §1's milestone
   table, and §3's per-phase table — and all three are hand-maintained, so each one moves only
   when someone remembers. `0b440df` moved README's line and not §3's Phase 9 row (`fe61929`
   closed that); §1 still reads `Phases 2–9 · ⬜ not started` while §3 carries four 🔄 rows, a
   contradiction that predates both commits and sits with no single owner because the row names
   eight phases at once. Where a copy cannot be generated from its source, the checker that
   validates the source has to assert the copy: `check-parity-matrix.py` already asserts that a
   `file:line` citation points at what it claims, and a status line duplicated across files is
   the same class of claim one step short of a stale citation.
11. **A test that exists only in a transcript is not a test.** Where the gating code is a script
   in `scripts/`, its test is a sibling file plus the make target that runs it — `gen-licenses.py`
   with `make licenses-check`, `gen-licenses-selftest.py` with `make licenses-selftest`. The
   precedent is A2's `6d6438d`, which kept a deleted scratch emit harness as a `-emitdump` flag
   for the same reason: the throwaway that produced the evidence is committed as a callable thing,
   not left as a transcript. Two corollaries. **Write the negative cases**: mutate each value the
   check claims to read, one at a time, and require the failure to name that one — a check that
   fails for the wrong reason gets ignored, which is the class R10.22 names (a banner test with
   one fixture of each kind passed while `meta.total` was read wrong). And **the test has to run
   where the check can't**: `licenses-check` needs `go-licenses` on PATH, so on a machine without
   it the generator short-circuits on the first half and the README assertion never executes — the
   selftest imports the script directly and is the only way to run that assertion there.
12. **A rule that has a command form should be written as that command.** Where the rule's action
   fits on one shell line, the entry's text *is* that line with its slots marked — `git commit --
   <path> [<path>…]`, `git show --numstat <sha> -- <path>`, `vitest run --root <abs>/web`,
   `helm lint -f chart/values-<profile>.yaml` — and the prose is reduced to the one-line reason.
   The test for whether an entry is finished: **could a future reader execute it by copying one
   string out of the file?** If not, add the string.
   The failure mode is not forgetting. A1 agreed with item 1 in prose, then measured their own
   `b9cc0fa` with `git diff --numstat HEAD -- internal/collector/metrics.go -> 5 5` — the
   working-tree form, which on that tree happens to print the same `5 5` as the committed form —
   when the rule was `git show --numstat <sha> -- <path>`. Their diagnosis: *"If the rule had been
   the literal string `git show --numstat <sha> -- <path>`, I'd have pasted it."* A sentence
   describes a rule and can be assented to; a template is what gets pasted at the moment of
   writing, when nobody re-reads the sentence. So the fix is not re-reading the prose, it is making
   the rule's text and the command's text one string. The two entries in this section that already
   do that (items 1 and 9) are the two that stuck after a single correction; A4's "verify in the
   same call as the send" needed three re-corrections before it had command forms
   (`git log -1 --format=%B | grep -c "Co-Authored-By"`, `out=$(cmd 2>&1); rc=$?`), and those
   forms are what stopped the recurrence. Where a rule is genuinely not command-shaped — item 11's
   "write the negative cases" — prose is the whole rule, and this clause asks nothing of it.

## 6. Coordinator duty: verify, don't accept

A slot's report is a *claim*. The coordinator's job is to check it before it becomes project
history, because three of the four slots have shipped a claim that didn't survive a command:

- A4 reported the contract types fixed → re-ran the diff of all 14 Go struct tags against the TS
  interfaces (0 mismatches), ran the suite (129 tests), then **mutation-tested the claim**: renamed
  one field in the golden fixture and confirmed `tsc` actually failed. Restoring gave a clean
  compile. The mutation test is the part that matters — a passing suite proves less than a suite
  that fails when it should.
- A3 reported four defects in my commit → each was verified against the code before applying. One
  was correct about HEAD but already fixed in a third agent's uncommitted file; verifying it
  surfaced a fifth defect A3 hadn't reported (the `--auth-metrics` scope drift in `auth.go`).
- A1 reported a clean build → `go build ./...` was red with 11 errors.

**So: re-run the command, don't re-read the prose.** Where a claim is checkable in one command,
run it. Where it's a test-coverage claim, break the thing and confirm the test notices.

The rule has an author's half, and it is the half that gets skipped. **A finding and the sentence
about the finding are two claims.** The headline gets verified because it is the thing being acted
on; the quantified sentence attached to it rides out on the headline's credibility, unchecked. Four
instances in one session, across three slots: A3's `R8.12 → R8.6` fix in `docs/architecture.md`
was correct, and the enumeration justifying it — "R8 has exactly R8.1, R8.1b, R8.1c, R8.2–R8.9" —
was short by two, since R8.10 and R8.11 exist (`requirements.md:388-389`). A2 twice corrected a
number correctly while overstating what that number implied ("the form that survives the next
insertion", then "stops owing you a fix"); A4 doubted an arithmetic claim of their own that was
correct once it was checked against the diffs. The coordinator catches the headline and inherits
the generalisation, so the fix has to be on the author's side: every quantified word in a report —
*exactly, all, every, the only, none, zero* — is a separate claim and needs its own command, run
before the report goes out rather than after someone questions it.

## 7. Reproducing a run

Spawn the six from `.claude/agents/`:

```
Agent(subagent_type: "a1-backend")      # opus
Agent(subagent_type: "a2-api-metrics")  # sonnet
Agent(subagent_type: "a3-review")       # opus
Agent(subagent_type: "a4-frontend")     # sonnet
Agent(subagent_type: "a5-query-store")  # opus
Agent(subagent_type: "a6-insights-ui")  # sonnet
```

Then, per phase: assign from `requirements.md` §7.2 → the owning slot builds → A3 reviews → the
validation entry is written → one commit carries both. Repeat. Nothing in the process requires a
seventh agent; if it seems to, the phase is probably being scheduled by data dependency instead of
by what the product is judged on (that mistake is D13).

**A completed subagent is not addressable, so "reuse via `SendMessage`" only works while it is
still running.** This is the operational fact the cap rule assumes and doesn't state. `SendMessage`
to a finished slot fails with `No agent named 'a3-review' is reachable`, and `ListAgents` then
lists only the ones still running — so a four-slot roster where three slots have finished their
work is, in practice, a one-slot session wearing a four-slot roster. The fix is not to send; it is
to spawn a fresh instance of the same `subagent_type`, which starts with the charter file's full
text and therefore inherits everything the previous instance learned (the charter *is* the
memory — which is why the `.claude/agents/*.md` files have to carry the hard-won rules, not just
the job description). Two corollaries: a slot's knowledge decays the moment it finishes unless it
is written down, and the agent registry is snapshotted at session start, so a `.claude/agents/`
file created mid-session is not spawnable by name until the next one.

## 8. Changing the roster

The cap is the brief's, and only the user can amend it — which happened on 2026-11-22, when the
brief's four became six. Absent such an amendment, "changing the roster" means re-scoping a slot,
not adding one; with one, both the new slots *and* the phases they take have to be written down in
the same breath, or the roster says one thing and §7.2 another.

When a slot's ownership changes, update **all four** places in one commit:

| Place | Why it's in the set |
|---|---|
| `requirements.md` § "7.1 Agent allocation (the brief's hard constraint: 4 subagents max — 2 opus + 2 sonnet, amended to 6 — 3 opus + 3 sonnet on 2026-11-22)" (:643) | The heading itself states the cap and the table names every slot's agent id. It is spec text, so it is A3's to edit. Written in the checker's durable form on purpose: `check-parity-matrix.py` asserts the quoted heading *is* the text at that line, so this row cannot go stale the way the ellipsis quote it replaced did (`(the brief's hard constraint: …)`) — that form matches none of the three citation regexes, so it was an unvalidated copy of another owner's heading. |
| `requirements.md` § "7.2 Phase table" (:665) | The per-phase **Owner** column — a reassigned phase changes a cell here, and this is the authoritative phase→slot map. |
| `agents.md` § "3. The six slots" (:53) | The *who*, with each slot's current ownership. |
| `IMPLEMENTATION.md` § "2. The agent roster (cap: 6 subagents — 3 opus + 3 sonnet, amended from the brief's 4 on 2026-11-22)" (:70) | The handoff file's roster table, which the brief requires to exist and stay current (R14.1). |

Plus the slot's own `.claude/agents/<name>.md` frontmatter `description`, which is what a fresh
instance of that slot actually reads at spawn time.

These have already drifted once (the brief's Phase 4 went to A2 while a message routed it to A4),
and an n-way agreement is only enforceable by committing all of them together — the number of
places grew from three to four when §7.1 was counted, and every added place is another way for the
roster to be internally inconsistent. The rule generalises rather than shrinking: **count the
places, then commit them as one.**
