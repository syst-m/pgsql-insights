# Proposal: <id or feature name> — <one-line summary>

Measured `<UTC>` · gate `rc=<N>` · by <slot> · classification: `<kind>` (confidence `<x.xx>`)

<!-- Every number in this file is copied from a command that ran in the same action that wrote
     it. The header's timestamp and gate rc come from the `ledger.py` run in step 1; the
     confidences come from the `decide` calls in steps 2–4; the verification probabilities come
     from step 5. Nothing here is written from memory of an earlier turn. -->

## 1. Current state (measured)

```
R8.1  implemented  1b11199:IMPLEMENTATION.md:650  code 64  first 5d67e8357bb9@2026-11-26T05:43:53Z  cites 11  consistent
```

<!-- One ledger row per id in scope, pasted verbatim from `ledger.py`'s output. If the request
     covers a phase, paste the phase's §3 row too. If a row is a finding, say so here — the user
     reading a proposal wants to know the record is wrong before they approve work on it. -->

## 2. What is being asked

<The request, restated in one sentence. If step 2 classified it as `new-feature` or
`spec-change`, name the spec file that will gain the requirement text and who owns it —
`docs/requirements/**` is A3's, so the spec edit lands there before any code.>

## 3. Assumptions adopted without asking

| Q | Answer | Confidence | Why this is not a question |
|---|---|---|---|
| scope | `minimal-spec-text` | 0.86 | d1-2B answered above the 0.7 bar |
| placement | `existing-file` | 0.74 | ditto |

<!-- Step 3c's `>= bar` half. These are stated, not asked: the user can override any of them by
     saying so, and each one carries the model's confidence so the user can see that 0.74 is a
     weaker call than 0.86. Listing them is what makes a confident small-model answer auditable
     instead of invisible. -->

## 4. Questions for you

<The `< bar` residue, and nothing else. For each: the question, the model's answer with its
probabilities, and the two or three options. The user should be choosing *against a proposal*,
not from a blank page. If this section is empty, say so explicitly — "no questions, the model
answered all six" is itself the answer, and it is the most common good outcome.>

## 5. Recommendation

<The approach in 2–3 sentences.>

Scored `<N>/6` on `<rubric name>` (`<noul ship>` P(yes)=`<0.xx>`).
Verified: grounded `<0.xx>` · on_brief `<0.xx>` · no_leaks `<0.xx>`.

<!-- The score comes from decision-score (the scale IS the rubric — name the rubric so the
     number means something). The three probabilities come from decision-verify run against the
     spec text as `state`, never against this document's own prose. Report the numbers, not
     pass/fail: 0.79 and 0.12 are both "no" and only the number says which kind. -->

## 6. Plan

- **Owner:** `<slot>` (`docs/agents.md:<line>`)
- **Files:** `<paths, from the roster's ownership globs, not from guesswork>`
- **Gate:** `bash scripts/check-parity-matrix.sh` — must be green before and after
- **Commit:** `<subject>` with pathspec, `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`
- **Also updates:** `<the phase file under docs/implementation/phases/, same commit>`

## 7. Awaiting

Nothing happens in step 6 until you answer §4 or say go. A background notification is not an
answer, and a confident model answer (§3) is not an approval.
