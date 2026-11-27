# Record: <id or feature name> — <one-line summary>

Landed `<sha>` at `<UTC>` · by <slot> · gate `rc=<N>` after the commit

<!-- The sha and the date are read from git in the same call that writes this file —
     `git log -1 --format='%h %ad' --date=format:%Y-%m-%dT%H:%M:%SZ -- <path>`. A date typed
     from memory is a second measurement, and between 16:00 and 24:00 local time a local date
     names tomorrow while the frontmatter stamp is UTC. -->

## Measured before → after

```
before  R8.1  in-progress  1b11199:IMPLEMENTATION.md:650  code  3  first 9fa8aaf12c34@2026-11-24T…
after   R8.1  implemented  1b11199:IMPLEMENTATION.md:650  code  7  first 5d67e8357bb9@2026-11-26T05:43:53Z
```

Both lines from `python3 .claude/skills/spec-implement/scripts/ledger.py R8.1 --json` — the
`before` captured before the work started (step 1), the `after` re-run after the commit (step 7).
Two numbers from two different commands is fine; two numbers from one memory is not.

## What changed

| File | Change |
|---|---|
| `<path>` | <one line> |

<!-- Generate this table from `git show --numstat <sha>` in the same call that writes it. A
     commit message naming four edits whose diff is three lines is a real, recurring defect in
     this repo, and the table written from recall has the same failure mode as the message. -->

## Verification

| Command | rc | What it proves |
|---|---|---|
| `bash scripts/check-parity-matrix.sh` | 0 | the doc/code parity gate |
| `<the requirement's own test command>` | 0 | <what the test asserts> |
| `<a mutation of the load-bearing test>` | 1 | the test can actually fail |

<!-- rc from a pipe is the last command's, not the tool's: redirect, then `echo "rc=$?"`. A
     green gate proves resolvability, not correspondence — for a cite, the check is reading the
     cited lines, which no rc can do. The mutation row is what separates a test that asserts
     something from a test that asserts the implementation. -->

## Deviations from the spec

<None, or: what the spec said, what was built, and why. A deviation that is not written down
becomes a mystery in the next review; the project keeps `docs/implementation/deviations.md` for
exactly this, so a real deviation belongs there too, not only here.>

## Answers to the proposal's open questions

<The user's replies to §4 of the proposal, verbatim, with the date they arrived. If the work
proceeded on §3's adopted assumptions without being overridden, say that — silence on a stated
assumption is the record of it being accepted, and it is worth distinguishing from a question
that was never asked.>
