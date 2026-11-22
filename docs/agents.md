# Agent specification

The rules for how this project uses subagents, written down so a future run reproduces the same
setup instead of rediscovering it. The constraint comes from the original brief; everything after
§2 is practice that the first run earned and that is easy to lose.

**Machine-readable definitions live in [`.claude/agents/`](../.claude/agents)** — one file per
slot, with the frontmatter (`name`, `description`, `model`) the Agent tool needs to spawn them.
This document is the *why* and the *expectations*; those files are the *what gets loaded*. If the
two disagree, the frontmatter wins on spawn mechanics and this file wins on charter.

---

## 1. The cap (from the brief, not negotiable)

> *"you shall spread the work throughout this WHOLE process amongst a maximum of 4 subagents —
> two opus and two sonnet — a total of 4 subagents at maximum."*

Four slots. Two opus, two sonnet. The cap is **for the whole process**, not per phase — so it is
spent once and stays spent. Adding a fifth agent, even briefly, violates the brief; so does letting
an opus slot execute sonnet-class work while the sonnet slots are full.

**Consequence worth stating plainly:** once the four exist, all further work is *reuse*. New work
goes to an existing slot via `SendMessage`, never to a fresh `Agent` spawn. A spawn that appears to
be "just a quick extra agent for this one thing" is the most common way this project breaks its own
constraint.

## 2. Saturation (the cap is a floor, not just a ceiling)

Respecting the cap and using it are different things. A run where one agent works and three wait is
*compliant and wasteful* — the budget is being spent on serialization. This was called out on
2026-11-21 (*"you should try to scale out the work more to max of 4 total subagents"*) after Phase
1a was progressing through one agent while three sat idle.

**The rule:** before starting work, enumerate which slots are active, and dispatch to the idle ones
*in the same turn*. This matters most when the critical path is narrow — Phase 1a → 2 → 3 is a
single chain, so only one agent can be *on* it, and the answer is to pull work off the chain
(the chart half, the docs half, the review queue), not to wait.

**And check the roster before addressing a message.** Sending a phase brief to the wrong slot
produces two agents on one phase and one idle — which happened once (a Phase 4 brief went to A4,
who owns frontend/Helm/docs, when the phase is assigned to A2).

## 3. The four slots

| Slot | Name | Model | Spawned for | Now owns |
|---|---|---|---|---|
| **A1** | `a1-backend` | opus | Cloud SQL Query/System Insights research | Go backend: discovery, collector framework, PG version matrix, store, query engine, `cmd/pgsi` |
| **A2** | `a2-api-metrics` | sonnet | CloudNativePG surface research | Go API surface: HTTP handlers, OpenAPI, auth/CORS middleware, `/metrics` export, PromQL pair |
| **A3** | `a3-review` | opus | Round-1 adversarial architecture review | Architecture, spec maintenance, per-phase validation, review of every spec change |
| **A4** | `a4-frontend` | sonnet | Round-1 adversarial product review | Frontend/UI (`web/`), Helm chart, packaging, docs |

Phase → slot assignment is authoritative in [`requirements.md` §7.2](../requirements.md), not here.
This table is the *who*; §7.2 is the *what*. The roster table in
[`IMPLEMENTATION.md` §2](../IMPLEMENTATION.md) is the same four and must not diverge from this file.

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

1. **Commit by explicit path. Never `git add -A`.** Four agents share one working tree; a
   blanket add sweeps another agent's in-flight files into your commit and attributes their work
   to you.
2. **One commit per phase**, containing that phase's code **and** its `IMPLEMENTATION.md` update
   (R14.1). Small in-phase commits are fine; the phase-closing commit carries both.
3. **Attribution is the committing model's own**, not a fixed string:
   `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` from an opus slot,
   `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` from a sonnet one.
4. **Requirement IDs are the contract.** Code comments reference `R3.4`, not "the fingerprinting
   bit". Every §4 requirement maps to exactly one phase (spec §7.3), and
   `scripts/check-parity-matrix.sh` enforces it — a spec edit that breaks the parity check is a
   defect, and CI runs it.
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

## 7. Reproducing a run

Spawn the four from `.claude/agents/` (or reuse the live ones via `SendMessage` — the cap means
reuse is the default, and a fresh spawn is only correct when the slot genuinely doesn't exist):

```
Agent(subagent_type: "a1-backend")     # opus
Agent(subagent_type: "a2-api-metrics") # sonnet
Agent(subagent_type: "a3-review")      # opus
Agent(subagent_type: "a4-frontend")    # sonnet
```

Then, per phase: assign from `requirements.md` §7.2 → the owning slot builds → A3 reviews → the
validation entry is written → one commit carries both. Repeat. Nothing in the process requires a
fifth agent; if it seems to, the phase is probably being scheduled by data dependency instead of
by what the product is judged on (that mistake is D13).

## 8. Changing the roster

The cap is fixed by the brief, so "changing the roster" means re-scoping a slot, not adding one.
When a slot's ownership changes: update **all three** places in one commit — this file's §3 table,
the slot's `.claude/agents/*.md` frontmatter `description`, and `IMPLEMENTATION.md` §2. They have
already drifted once (the brief's Phase 4 went to A2 while a message routed it to A4), and a
three-way agreement is only enforceable by committing the three together.
