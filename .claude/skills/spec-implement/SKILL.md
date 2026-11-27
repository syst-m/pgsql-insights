---
name: spec-implement
description: Use to implement a requirement from the project's specs (requirements.md / docs/requirements) or a new feature the user asks for, in a controlled way — "implement R8.1", "add X as a feature", "build the next thing", "what's left, and implement it". Tracks implementation state (status, dates, evidence) by measuring the repo, and uses the decision skills to decide *which* questions to ask the user, then validates its own proposal before presenting it. Not for a one-line fix (just fix it), not for running an already-partitioned plan (parallel-plan-execution), not for pure status questions with no intent to act (ledger.py alone).
---

# Spec-driven implementation

Implements a spec — or a new feature — in a way that keeps the spec, the code, and the record
agreeing with each other, and that never asks the user a question a 2B model can answer.

The one rule that explains everything else:

> **Every tracked fact is a measurement, not a memory.**

A status cell, a date, a count, a file path: each must come from a command run in the same
action that writes it. This project has paid for every violation of that rule — a status table
that said a phase was undone nine merges after it shipped, a count reported as "118 ids / 53
files" where the command gives 120, a line number read *before* an edit and then cited *after*
it, a commit message naming four edits when the diff was three lines. So this skill does not
remember the project's state; it runs `scripts/ledger.py` and reads the output.

The second rule, and the reason the decision skills are wired in:

> **Ask the user only what the small model cannot answer.**

Claude's default is to ask the user everything (slow, and it spends the user's attention on
trivia) or nothing (fast, and it silently invents requirements). The cascade in step 3 splits
the difference: enumerate the open questions, get `d1-2B`'s answer to each with a fixed option
set, adopt the confident ones as *stated assumptions*, and put only the low-confidence residue
in front of the user.

## When to use

| Request | Use |
|---|---|
| "Implement R8.1" / "finish phase 4" | this skill |
| "Add per-connection rate limiting as a feature" | this skill (new-feature path, step 2) |
| "What's the status of X?" with intent to act | this skill |
| "What's the status of X?" alone | `python3 .claude/skills/spec-implement/scripts/ledger.py R8.1` |
| "Here is a plan with 6 work packages, run it" | `parallel-plan-execution` |
| A one-line fix | make the change |
| Review a spec change | the `a3-review` slot |

## Files

| File | Role |
|---|---|
| `scripts/ledger.py` | **the instrument.** Runs the parity gate, then prints one measured row per requirement id: declared status, owner, the first and latest *code* commit citing it, its resolved path cites, and a verdict. |
| `templates/proposal.md` | the artifact that goes to the user (step 5). |
| `templates/record.md` | the per-implementation record written after step 6. |
| `requirements.md` + `docs/requirements/**` | the spec (what should exist). |
| `IMPLEMENTATION.md` + `docs/implementation/**` | the record (what does exist, per phase). |
| `docs/agents.md:57-62` | the roster — who owns which path. Step 6 routes through it. |
| `scripts/check-parity-matrix.sh` | the gate. `ledger.py` runs it; step 6 must have it green. |

`ledger.py` imports the checker rather than shelling out to it, and the checker's module *is*
its `main()` — so using the ledger's parser necessarily runs the gate. There is no version of
this skill that reports a status with the gate unrun.

## The loop

```
1  Orient        run ledger.py; never answer from recall
2  Classify     one decide call: existing-spec | new-feature | spec-change | defect-fix | docs-only
3  Clarify      enumerate the open questions → decide each → ask the user only the residue
4  Recommend    decision-score to rank the options
5  Validate      decision-verify my own draft → present → WAIT for the user
6  Implement     route to the owning slot, gate each action, run the real gate, commit
7  Record        re-run ledger.py; the record updates by derivation, not by typing
```

Steps 1–5 are cheap and mostly free (the NPU endpoint costs ~24 input tokens per question).
Step 6 is the expensive one. The point of the ordering is that step 6 should never be started
on a question step 3 could have resolved for free.

---

## Step 1 — Orient

Run the instrument. Do not read `IMPLEMENTATION.md` and summarise it.

```bash
python3 .claude/skills/spec-implement/scripts/ledger.py R8.1        # one id
python3 .claude/skills/spec-implement/scripts/ledger.py             # all of them (~2 min)
```

It prints four sections. Read them in order:

- **`## gate rc=N`** — the parity checker's own stdout, byte-for-byte. `rc` is read from the
  module's `SystemExit`, not from a pipe.
- **`## self-check`** — the row count this script parsed vs the count `check_status_enum`
  reports. They must be equal; `DIFFER` means the two parse rules drifted and every number
  below it is suspect.
- **`## ledger`** — one row per id.
- **`## findings`** — rows where the doc and the code contradict each other.

**Why the row is split into `declared` and `code`.** Every requirement row contains its own
id, so a single `git log -S R8.1` finds the commit that *wrote the row* and reports it as
proof the feature exists — an assertion that cannot fail. The first version of `ledger.py`
did exactly that and produced 156 "disagreements", most of them artifacts. The fix is to
search `:(exclude)*.md` for the code-side answer: an id cited in a `.go` or `.tsx` file is
cited by code; an id that appears only in markdown is claimed by docs alone. That is the
difference between `implemented` and `claimed`, and it is the only question `git` can answer
about a requirement's status.

| `status` | code commits | verdict |
|---|---|---|
| implemented / validated | ≥1 | consistent |
| implemented / validated | 0 | **no-code-trace** — the doc claims it, nothing cites it |
| not-started | ≥1 | **contradicted** — code exists for an unstarted requirement |
| tabled | ≥1 | **contradicted** — tabled requirements should not have shipped code |
| in-progress / blocked | any | consistent (partial code proves nothing either way) |

If the request is about a phase rather than an id, read that phase's file under
`docs/implementation/phases/` and its §3 row — but get the *status* from the ledger, not from
the prose. A row's file list can name the wrong file; `git log -S` is the instrument.

**Report what you found before proposing anything.** A change of plan driven by a discovered
fact is the thing the user needs mid-run: "R8.1 is rowed twice, at `:650` and `:683`, and
both say implemented" is worth more than a paragraph of plan.

## Step 2 — Classify the request

One `decide` call, before any other work. The answer picks the path, and getting it wrong is
how a defect fix turns into an unrequested spec rewrite.

```json
{"state": "A request about pgsql-insights: <the request, verbatim>\nThe project's spec is requirements.md; 177 requirement ids exist.",
 "questions": [{"id": "kind", "type": "choice", "instructions": "What kind of work is this?",
   "options": ["existing-spec", "new-feature", "spec-change", "defect-fix", "docs-only"]}]}
```

| `kind` | What follows |
|---|---|
| `existing-spec` | the id is in the spec. Steps 3–7 with the spec as the contract. |
| `new-feature` | **not** in the spec. The spec change comes first: `docs/requirements/**` is the `a3-review` slot's file, so the requirement text is authored there and reviewed there *before* code. Then steps 3–7. |
| `spec-change` | an existing requirement's text changes. Same rule: `a3-review` owns that edit. |
| `defect-fix` | skip steps 3–5 unless the fix changes a documented behaviour. Fix, gate, commit, record. |
| `docs-only` | steps 6–7 only. |

Read `confidence` with the answer, always. A `choice` at ~0.5 is the model saying it does not
know — that is not a licence to guess, it is the first question for the user: *"is this a new
feature or a change to R<n>?"*

## Step 3 — Clarify (the cascade, applied to questions)

This is the step that makes the skill "use the decision skills to formulate clarifications".
`decision-route`'s pattern is normally run over inbox items; here it runs over the **questions
you would otherwise ask**.

**3a. Enumerate the questions.** Write them down before asking anyone. A question is only
routeable if it has a fixed, small answer set — a question whose answers you cannot list is a
question you have not thought about yet. Typical set for a spec implementation:

```
scope        : minimal-spec-text | spec-text-plus-ui | full-phase-scope
placement    : existing-file | new-file-in-owned-dir | new-package
rollout      : code-only | code-plus-helm | code-plus-helm-plus-runbook
compat       : current-versions-only | full-matrix | defer-compat
verify       : unit-only | unit-plus-integration | unit-plus-live-probe
```

**3b. Ask the model, in one call.** Up to 16 questions per `decide` call — batch them, never
one call per question.

```json
{"state": "Implementing R8.1 (Cluster discovery) in pgsql-insights. The spec text says: <quote it>.",
 "questions": [
   {"id": "scope",     "type": "choice", "instructions": "How much of the spec does this change implement?",
    "options": ["minimal-spec-text", "spec-text-plus-ui", "full-phase-scope"]},
   {"id": "placement", "type": "choice", "instructions": "Where does the code go?",
    "options": ["existing-file", "new-file-in-owned-dir", "new-package"]},
   {"id": "rollout",   "type": "choice", "instructions": "What ships alongside the code?",
    "options": ["code-only", "code-plus-helm", "code-plus-helm-plus-runbook"]},
   {"id": "verify",    "type": "choice", "instructions": "What proves it works?",
    "options": ["unit-only", "unit-plus-integration", "unit-plus-live-probe"]}]}
```

**3c. Route on confidence.** Default bar 0.7.

| Result | Action |
|---|---|
| `≥ bar` | **Adopt it, and say so.** It goes into the proposal as a listed assumption with the model's answer and confidence attached — it does *not* become a question. |
| `< bar` | **Ask the user.** Only these questions go up, with the model's answer and its probabilities shown so the user is choosing *against a proposal*, not from a blank page. |

Two rules from `decision-route` that carry over verbatim:

- **The bar is a policy, not a constant.** If the user keeps overriding the model in one
  category (`placement` is the usual one), the wording of that question's options is the bug —
  fix the option set, don't lower the bar.
- **Anything that writes to a system stays draft-first.** A confident answer licenses *asking*
  about it cheaply; it never licenses doing it.

**3d. If the endpoint is down, do not block.** Ask the questions yourself, in one message, and
say the NPU was unreachable. The NPU is an optimisation here, not a dependency — the skill's
job (ask fewer, better questions) is still doable without it, just more expensively.

## Step 4 — Recommend

Now that the constraints are fixed, rank the candidate approaches with `decision-score`. The
load-bearing trick from that skill: **the scale *is* the rubric.** `["bad","ok","good"]` gives
a mood; a scale whose levels name the evidence gives a measurement.

```json
{"state": "Proposed approach for R8.1: <the approach in 2-3 sentences, with the step-3 answers folded in>",
 "questions": [
   {"id": "quality", "type": "score", "instructions": "Implementation quality of this approach",
    "criteria": ["contradicts the spec text",
                 "implements less than the spec text",
                 "implements the spec text, no tests",
                 "implements the spec text, tests present",
                 "implements the spec text, tests pass, no new lint findings",
                 "implements the spec text, tests pass, no new lint findings, docs updated"]},
   {"id": "ship", "type": "noul", "instructions": "Is this ready to implement without asking the user more?"}]}
```

Score and gate in one call — that is the point of pairing them. Threshold on `quality` (the
default bar for "propose this" is the 5th level, not the 4th) and let `ship`'s `P(yes)` decide
whether step 5 is a proposal or another round of step 3.

## Step 5 — Validate the recommendation, then propose

Before the draft reaches the user, `decision-verify` judges it. This is the "validate the
recommendations you will propose to me" half of the mandate, and it has one caveat that changes
how the call is built:

> The model that wrote the draft is a weak judge of its own draft.

So the `state` you send is not your prose — it is the **spec text**, and your proposal goes in
as the thing being checked. That makes `grounded` a real test (does the proposal follow from
the spec) rather than a restatement of your own paragraph.

```json
{"state": "requirements.md R8.1 says: <verbatim spec text>\n---\nDraft proposal:\n<the proposal>",
 "questions": [
   {"id": "grounded", "type": "noul", "instructions": "Does every claim in the draft follow from the spec text quoted above?"},
   {"id": "on_brief", "type": "noul", "instructions": "Does the draft do what was asked, and nothing extra?"},
   {"id": "no_leaks", "type": "noul", "instructions": "Does the draft avoid secrets, internal urls, and unreviewed claims about other people's files?"}]}
```

| Check | Bar | On failure |
|---|---|---|
| `grounded` | 0.8 | block — fix the proposal, do not present it |
| `on_brief` | 0.7 | revise — the draft drifted from the request |
| `no_leaks` | 0.9 | block |

Report **probabilities, not just pass/fail** — a `grounded` of 0.79 and one of 0.12 are both
"no", and only the number tells the user which kind of no it is.

Then write `templates/proposal.md`, send it, and **stop**. The proposal is the control: nothing
in step 6 happens on a model's answer alone, and a background notification is never the user's
approval. Wait for the user's own reply.

## Step 6 — Implement

**Route, don't free-write.** `docs/agents.md:57-62` names the owner of every path in the
repo. The rule is the roster's, not this skill's invention:

| Slot | Owns |
|---|---|
| A1 | discovery, collector framework, PG version matrix, `cmd/pgsi`, `api/dto`, `internal/api/server.go` |
| A2 | `internal/api` handlers, auth/CORS, OpenAPI, `/metrics`, the PromQL pair |
| A3 | architecture, spec maintenance, per-phase validation, review of every spec change, `requirements.md`, `docs/reviews/finding-index.md`, `scripts/check-parity-matrix.py` |
| A4 | `web/` shell + build, Helm chart, packaging, docs (`README.md`, `docs/architecture.md`, `docs/runbooks/`) |
| A5 | `internal/query/`, `internal/store/`, `pg_stat_statements` collector |
| A6 | `web/src/pages/**`, `web/src/components/**`, the Query Insights UI |

Spawning the owning slot's agent type is the default. Writing the code yourself is correct only
when the change is smaller than the round trip — and then it is *your* change, so it carries
your attribution and the owner is told. Committing a file is not the same as owning it: an
edit to someone else's file that extends its behaviour is an ownership claim their row does not
grant you.

**Gating is already installed.** `decision-gate` runs as a `PreToolUse`/`PostToolUse`/
`UserPromptSubmit` hook (`~/.claude/hooks/claude-gate.py`), currently `GATE_MODE=passive` —
logging every call, computing nothing. Check it before relying on it:

```bash
grep -m1 '^GATE_MODE=' ~/.claude/gate.conf
```

Do not reimplement a gate inside this skill. If the mode is `passive` and the user wants the
step-6 actions actually screened, that is a one-line change to `~/.claude/gate.conf` (no
restart) and it is the user's call, not this skill's.

**Run the real gate, and read the log.**

```bash
bash scripts/check-parity-matrix.sh > /tmp/gate.txt 2>&1; echo "rc=$?"
```

`rc` from a pipe is the last command's, not the tool's — hence the redirect. Running the `.py`
bare prints USAGE and exits 2, which looks exactly like a failed gate.

**Commit with a pathspec.** `git add x && git commit` commits the whole index, and on a shared
tree that index has other people's staged work in it:

```bash
git commit -- <paths> -m "$(cat <<'EOF'
A1: <subject>

<body: what changed, why, and the measured evidence>

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

Backticks inside a double-quoted message run as command substitution — hence the quoted heredoc.
If the commit moves lines in a file others cite, name the stranded cites in the body (the
project's blast-radius rule), and re-run the gate *after* the commit: a pre-commit run grades
the copy the index holds, not the one you are about to write.

## Step 7 — Record

Re-run `ledger.py`. The record updates **by derivation** — the new row's status, date, and
code trace all come from the commit that just landed — and the report to the user shows the
measured before and after from the same command, not two numbers from two memories.

Write `templates/record.md` next to the phase file it belongs to, and update the phase's
`docs/implementation/phases/` file in the same commit as its code (the roster's
one-commit-per-phase rule). A status table that is not updated in the commit that changes the
truth will be re-read next time and will mislead whoever reads it — including you, in three
days.

## Failure modes

| Symptom | Cause | Fix |
|---|---|---|
| Status reported, no command run with it | Answered from recall | Step 1 is a script, not a memory |
| 156 "findings" from the ledger, most of them fake | `git log -S` unscoped found the doc row itself | Scope the code search to `:(exclude)*.md` |
| The user is asked five questions about trivia | Questions not enumerated, so all of them went up | Step 3b: batch them through `decide`, ask only the residue |
| The user is asked nothing, and the wrong thing got built | Claude answered everything itself | The bar exists to catch this; ~0.5 means ask |
| A confident model answer became an action | Route used as approval | Confident ⇒ stated assumption, never ⇒ done (step 3c) |
| Proposal validates itself | `decision-verify` given the draft as its own state | Put the **spec text** in `state`, the draft as the artifact |
| Pass/fail reported, no numbers | Verification collapsed to a boolean | Report per-check probabilities; 0.79 and 0.12 are different no's |
| Gate green, but the cited line is blank | rc proves resolvability, not correspondence | Read the cited lines; the gate cannot see a wrong-but-non-blank line |
| `git add x && git commit` commits someone else's work | The index is shared | `git commit -- <paths>` |
| A commit message names four edits, the diff is three lines | Message written from recall | Generate the list from `git show --numstat HEAD` in the same call |
| The gate goes red the moment a file is committed | The commit moved lines that other files cite | Keep insertions == deletions, or retarget the citing rows in the same commit |
| A new feature implemented with no spec entry | Skipped step 2's `new-feature` path | Author it in `docs/requirements/**` via A3 first |
| NPU unreachable, work stops | Treated the optimisation as a dependency | Ask the questions yourself, say the endpoint was down |
| A background notification read as the user's approval | Task notification treated as input | Step 5 waits for the user's own reply |

## Anti-patterns

- **Summarising the spec instead of quoting it.** The proposal's `state` is the spec text
  verbatim; a paraphrase is a second copy of the contract, and yours is the one that drifts.
- **One `decide` call per question.** Batch to 16. The cost is per call, not per question.
- **Concatenating items into one `state`.** One call per item; merged items average away the
  disagreement you are trying to find.
- **Lowering the bar to stop being overridden.** If the user keeps saying no to a category, the
  option set is wrong. Fix the wording.
- **Asking the model to judge its own prose.** Give `decision-verify` the spec as `state`.
- **Implementing before the proposal is answered.** The proposal is the control, not a courtesy.
- **Editing a file you don't own and then extending it.** Committing is not owning.
- **Trusting an agent's report over the merged result.** If step 6 spawned an agent, verify on
  the merge: run the gate, read the cited lines, mutate the load-bearing test.
- **Writing the record by hand.** Re-run the instrument; a typed row is a claim, not a record.
