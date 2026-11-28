# Record: R4.1 / R4.3 / R4.5 / R4.7 — the session glyphs land, the other three are scoped

Landed `6f6f7a3f9d98536e5641dd411bfa61198d6bcd2e` (`6f6f7a3`) at `2026-11-28T03:28:24Z` · by A6 · gate `rc=1` after the commit

<!-- The sha and the date are read from git in the same call that writes this file —
     `git log -1 --format='%h %H %ad' --date=format:%Y-%m-%dT%H:%M:%SZ 6f6f7a3f9d98536e5641dd411bfa61198d6bcd2e`. This record is
     its own commit and cannot print its own sha, so the code commit is named and this file is
     described — the same shape as `phase-2-record-r2.8.md` for A5's `cfbd5bc`. -->

The proposal covered four phase-6 rows; the coordinator's reply narrowed it to one: *"So the only
code available to you now is Proposal 1's G1 + G2 (glyphs + the corrected comment, with the
three-number fix and `:44-61`). Build that one commit."* The diff is therefore R4.1's alone, and
R4.3, R4.5 and R4.7 appear here with the rulings that scope them rather than with code — the
Answers section carries each one.

## Measured before → after

```
before   R4.1  in-progress  docs/implementation/phases/phase-6.md:86  code 21  first 337784888de9@2026-11-26T01:59:00Z  latest d36e05b3e34f@2026-11-23T08:53:10Z  doc 33  first 189093a6582e@2026-11-28T01:18:38Z  cites 3  consistent
after    R4.1  in-progress  docs/implementation/phases/phase-6.md:86  code 22  first 6f6f7a3f9d98@2026-11-28T03:28:24Z  latest d36e05b3e34f@2026-11-23T08:53:10Z  doc 34  first 6f6f7a3f9d98@2026-11-28T03:28:24Z  cites 3  consistent

before   R4.3  in-progress  docs/implementation/phases/phase-6.md:88  code 11  first be7f04238f28@2026-11-25T20:08:47Z  latest f9aae4156327@2026-11-22T01:06:22Z  doc 22  first aa20dae519ea@2026-11-27T00:02:29Z  cites 3  consistent
after    R4.3  in-progress  docs/implementation/phases/phase-6.md:88  code 11  first be7f04238f28@2026-11-25T20:08:47Z  latest f9aae4156327@2026-11-22T01:06:22Z  doc 22  first aa20dae519ea@2026-11-27T00:02:29Z  cites 3  consistent

before   R4.5  in-progress  docs/implementation/phases/phase-6.md:90  code 16  first 337784888de9@2026-11-26T01:59:00Z  latest 49b32106d0a1@2026-11-23T15:35:17Z  doc 26  first aa20dae519ea@2026-11-27T00:02:29Z  cites 4  consistent
after    R4.5  in-progress  docs/implementation/phases/phase-6.md:90  code 16  first 337784888de9@2026-11-26T01:59:00Z  latest 49b32106d0a1@2026-11-23T15:35:17Z  doc 26  first aa20dae519ea@2026-11-27T00:02:29Z  cites 4  consistent

before   R4.7  in-progress  docs/implementation/phases/phase-6.md:92  code 2  first e6a4921fdfc7@2026-11-24T10:20:04Z  latest 49b32106d0a1@2026-11-23T15:35:17Z  doc 9  first aa20dae519ea@2026-11-27T00:02:29Z  cites 3  consistent
after    R4.7  in-progress  docs/implementation/phases/phase-6.md:92  code 2  first e6a4921fdfc7@2026-11-24T10:20:04Z  latest 49b32106d0a1@2026-11-23T15:35:17Z  doc 9  first aa20dae519ea@2026-11-27T00:02:29Z  cites 3  consistent
```

Both sets from `python3 .claude/skills/spec-implement/scripts/ledger.py R4.1 R4.3 R4.5 R4.7 --json`.
The `before` ran in the working tree at `189093a6582e…` (`measured_at 2026-11-28T09:32:40Z`,
`gate_rc 0`, `self_check 122/122 agree`).
The `after` ran with `--root` pointed at a detached worktree checked out at `6f6f7a3`
(`git -C <wt> status --porcelain | wc -l` → `0` in the same call, so the checkout is the rev and
not a dirty tree; `measured_at 2026-11-28T11:50:32Z`, `root /private/tmp/wt-6f6f7a3`,
`gate_rc 1`, `self_check 123/123 agree`).

The `--root` flag is not decoration. Run from the main checkout's cwd, the same script reports
`head 3730993866099ffcbd147f3c53c97a0d0483d232` — the main tree's tip, not the worktree's — so a
measurement of `6f6f7a3` taken from inside the main checkout silently measures somebody else's
commit. The row above is the one taken with the root named.

Two things the pair shows that one row would hide:

- **R4.1's status cell does not move.** `phase-6.md:86` still reads `**in-progress (A1)**`, and
  that is the ruling, not an omission: *"your commit should not touch `phase-6.md` at all"*. The
  row belongs to the 6a half A1 owns; A6's work still registers as a `code_commits` addition
  (21 → 22, `first` now
  `6f6f7a3f9d98`) without being a status change.
- **3 of the 4 pairs are byte-identical** (R4.3, R4.5, R4.7). The commit
  implements R4.1 only, so their rows cannot move; printing them is the point — a record that
  prints only the row that changed cannot show the other three were left alone on purpose.

## What changed

| File | 6f6f7a3 | Change |
|---|---|---|
| `web/src/components/StatusGlyph.test.tsx` | 201 / 7 | `SESSION_PATHS_AT_10` golden, `SAMPLE_ROWS` and the `sessionGlyph()` helper added; the shared-vocabulary test widened from two tables to three; two new describes — `SessionStatusGlyph` (a 12-row status/blocked mapping table, the goldens, distinctness + a11y, the size prop) and `SessionStatusChip` (the API's own status string as the chip's text, and the chip composing the glyph rather than redrawing it) |
| `web/src/components/StatusGlyph.tsx` | 108 / 3 | `GlyphShape` widened with `hourglass` and `block`; `shape()` gains both cases (the hourglass as two triangles sharing their apex at the centre, the block as the ring's annulus plus a clockwise bar so the nonzero fill rule fills the bar back in where it crosses the hole); `SessionState`, `SESSION_GLYPHS`, `sessionStateFor()`, `sessionPresentationFor()`, `SessionStatusGlyph` and `SessionStatusChip` appended below the health and panel blocks; the header's two-vocabulary sentence rewritten to name three |
| `web/src/pages/ActiveQueries.test.tsx` | 35 / 0 | append-only `describe("R4.1's Status cell")`: every fixture row carries a `.status-chip` whose text is that row's own `status` string and whose `data-session-state` matches a typed pid→state table, with a glyph path inside. No new import — the table is a literal so the expectation is not a copy of `sessionStateFor` |
| `web/src/pages/ActiveQueries.tsx` | 11 / 11 | `case "status"` now renders `<SessionStatusChip status={row.status} blocked={row.blockingPids.length > 0} />`; the header's R4.1 comment corrected on all three numbers (`twenty columns` → `nineteen`, `nine of the twenty` → `nine of the nineteen`, `the other eleven reachable` → `ten reachable`) and its cite widened from `dto/sessions_test.go:45-60` to `:44-61`; the header paragraph rewrapped 7 → 6 lines to pay for the new import, so no line below it moved |
| `web/src/pages/WaitEvents.test.tsx` | 24 / 0 | append-only `describe("R4.1's Status cell on the bucket drill")`: the drill's session table rows each carry a chip whose text matches the fixture row it came from |
| `web/src/pages/WaitEvents.tsx` | 7 / 7 | the same `case "status"` swap and `SessionStatusChip` import; header rewrapped 6 → 5 lines for the same line-neutrality reason |

6 files, 386 insertions, 28 deletions — generated from
`git show --numstat 6f6f7a3` in the same call that wrote this table. The two page files are
2 of the
six where insertions equal deletions; those two are the ones with inbound line-cites, and the
header paragraphs were rewrapped rather than grown so the cites below them still resolve.

## Verification

| Command | rc | What it proves |
|---|---|---|
| `bash scripts/check-parity-matrix.sh` (detached worktree at `6f6f7a3`, `status --porcelain \| wc -l` → `0`) | 1 | the doc/code parity gate: `1809 resolved · 2 wrong · 1 unresolvable (reported, not failed) · 3 ambiguous (failed) · 3 exempt`. Both wrong cites belonged to the phase-4 proposal -- a cmd/pgsi/targets.go line past that file's 236-line end, and a blank internal/metrics/metrics_test.go line; the ambiguous one was a references/components.md path in docs/implementation/decisions.md; the 2 non-exempt d-ids were the §5-D13 double definition in that proposal and in phase-4.md. No finding names a file this commit touches, and the report-only non-md tier -- the one that reads `.tsx`, i.e. every cite in the six files -- reports `506 checked · 0 wrong · 0 ambiguous`. The paths are named in prose, not cited: a quoted cite is checkable, and once the phase-4 pair was fixed by its owners the same gate at this record's own commit reports `1844 resolved · 0 wrong · 0 ambiguous` with the two wrong ones being this file's quoted copies. |
| `npm --prefix web run build` (`tsc -p tsconfig.json --noEmit && vite build`) | 0 | the six files typecheck and the bundle builds; run on the working tree that became `6f6f7a3`, before the commit |
| `npm --prefix web run test` (`vitest run`) | 0 | `Test Files  39 passed (39)`, `Tests  795 passed (795)`; re-run after the commit at HEAD `3730993`, where `git diff --numstat 6f6f7a3 HEAD -- web/src` → `0` lines, so the suite is this commit's suite and not a later one's |
| MUT-A `shape()`'s `hourglass` case loses its lower triangle (`M 0 0 H ${size} L ${r} ${r} Z`) | 1 | `FAIL … > draws each session state's documented shape` and `> sizes the box from the prop, defaulting to 10` — `Tests 2 failed \| 16 passed (18)`. The golden pins the whole `d` string, so a half-drawn hourglass is a failure, not a rendering nuance |
| MUT-B `sessionStateFor` drops `if (blocked) return "blocked";` | 1 | five tests fail — the mapping table, the presentation identity, the goldens, the distinctness set and the chip's text. `blocked` is not a decoration on one branch: every session assertion in the file routes through the mapping |
| MUT-C `ActiveQueries.tsx`'s status cell passes `blocked={false}` instead of `blocked={row.blockingPids.length > 0}` | 1 | `FAIL src/pages/ActiveQueries.test.tsx > R4.1's Status cell > renders a status chip in every row, with the state that row's fields imply` — `Tests 1 failed \| 35 passed (36)`. The page test reads the row's `blockingPids`, so a page that renders a chip with the wrong state fails; a page that renders no chip fails the same test |

All three mutations were applied with a python string replace and reverted with
`git checkout -- <file>` in the same call, each verified `git status --porcelain -- <file>` → `0`
lines; the suite's own `web/src` was byte-identical to `6f6f7a3` throughout (the only dirty paths
in the tree were `web/dist/assets/index.js`, A4's build output, and two `internal/` files).
`scripts/mutation-sweep.py` scopes its `SITES` to `internal/query` and `internal/store`, so there
is no sweep entry for a `.tsx` to add these to; they are hand-run, as A5's are.

## Deviations from the spec

**The phase-6 row is not rewritten.** Step 7 normally flips the phase table's status cell; the
coordinator ruled the 6a/6b split means this commit *"should not touch `phase-6.md` at all"*, so
`docs/implementation/phases/phase-6.md:86` still reads `**in-progress (A1)**` and this file is the
only place A6's R4.1 work is dated. The row flips when A1's half does.

**`status` has no type to switch on.** `web/src/api/types.ts:813` types `SessionView.status` as a
plain `string`, so the mapping is a function with a default arm rather than a `Record` the
compiler checks, and the test's 12-row table is the contract's only second copy. Narrowing the DTO
field to a union is an A1/A2 change to request, not a local interface to invent, and this proposal
did not ask for one.

**The third state borrows a shape.** `requirements.md:112` names exactly two icons — hourglass and
block — so `idle` draws `dash`, the shape health's `unknown` also borrows, in `--text-muted` for
the same reason: it is not a state the spec draws, and inventing a third icon would be inventing a
spec. The alternative (a per-status icon set) is the thing the requirement does not ask for.

**R4.3, R4.5 and R4.7 have no code in this commit.** Their rows' work is their owners' — see below.

## Answers to the proposal's open questions

The coordinator's replies, all dated 2026-11-28, quoted as received:

- **Q1 (which icons):** *"(a)"* — the two the requirement names, hourglass and block, with a
  default arm for every other status string. That is what `SESSION_GLYPHS` implements, and MUT-A
  and MUT-B are what make the choice enforceable rather than a preference in a comment.
- **Q2 (G3, the R4.3 wait-events work):** *"not yours to chase"*.
- **Q3 (the phase rows):** *"your commit should not touch `phase-6.md` at all"*, and *"R4.5:
  recorded as built, no UI work."*
- And the instruction that scoped the work: *"So the only code available to you now is Proposal 1's
  G1 + G2 … Build that one commit."* followed by *"**Do not push.** Commit with a pathspec
  (`git commit -- <paths>`), report the sha and post-commit gate rc. I own the push."* — the
  commit is `git commit -F "$msg" -- <the six paths>`, and it is not pushed.

Two deferred items the same reply settled, so they are recorded rather than lost: R4.7's
`query_id` cell change waits on A4 mirroring `fingerprint` in `web/src/api/types.ts`, and R4.3's
two-line migration (rendering the page from a real `WaitEventsResponse` instead of grouping
`ActiveQueriesResponse` client-side) lands in the commit after A1/A2's payload change.
