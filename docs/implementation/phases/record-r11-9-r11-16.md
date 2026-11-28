# Record: R11.9 + R11.16 — the quickstart's interim form and the publish path's registry question

Landed `8a25380` at `2026-11-28T02:09:59Z` · by A4 · gate `rc=0` after the commit

This record covers the half of both rows that does not depend on the unresolved
registry org (Q6). The rest stays blocked, with the reason in §Deviations below.

## Measured before → after

```
before  189093a  R11.9   in-progress  docs/implementation/phases/phase-0.md:76  code 0  first —  latest —  doc 9  cites 0  consistent
after   8a25380  R11.9   in-progress  docs/implementation/phases/phase-0.md:76  code 0  first —  latest —  doc 9  cites 0  consistent

before  189093a  R11.16  in-progress  docs/implementation/phases/phase-0.md:83  code 3  first f39ddfdad31b@2026-11-24T19:14:10Z  latest db4f237b4b9e@2026-11-20T18:55:06Z  doc 10  cites 0  consistent
after   8a25380  R11.16  in-progress  docs/implementation/phases/phase-0.md:83  code 3  first f39ddfdad31b@2026-11-24T19:14:10Z  latest db4f237b4b9e@2026-11-20T18:55:06Z  doc 10  cites 0  consistent
```

Both lines from `python3 .claude/skills/spec-implement/scripts/ledger.py R11.9 R11.16
--json [--root <worktree>]`: the `before` measured in a detached worktree at `189093a`
(`git -C <wt> status --porcelain | wc -l` → `0` in the same call, so the checkout is the
rev and not a dirty tree), the `after` at `8a25380`. Neither row's status moved — this
commit documents and measures, it does not close either row, which is why both stay
`in-progress` rather than advancing.

One instrument observation, recorded rather than explained: R11.16's `first`
(`f39ddfdad31b`, 2026-11-24) is *later* than its `latest` (`db4f237b4b9e`, 2026-11-20).
The two fields cannot both be a min and a max over one commit set, so the pair is not a
single range. It is identical in both measurements, so it is not caused by this commit.

## What changed

| File | Change |
|---|---|
| `README.md` | +11 / −11 — the provisional-registry section rewritten in place: it now carries the measured hand-edit site list for the org string and the packaged-tarball evidence, replacing the unmeasured "three-line change … nothing else references the org" sentence |
| `docs/implementation/phases/phase-0.md` | +2 / −2 — rows R11.9 (:76) and R11.16 (:83) rewritten in place with the measured evidence; the new `§5-D10` mentions use the namespaced form (§5-D10 is defined in both §4 and §5, so a bare `§5-D10` is not exempt — the gate failed on it before the fix) |

From `git show --numstat 8a25380`: `11 11 README.md`, `2 2 docs/implementation/phases/phase-0.md`,
`2 files changed, 13 insertions(+), 13 deletions(-)`.

## Verification

| Command | rc | What it proves |
|---|---|---|
| `bash scripts/check-parity-matrix.sh` (before the edit) | 0 | baseline at `189093a` |
| `bash scripts/check-parity-matrix.sh` (after the commit) | 0 | the doc/code parity gate; 127 namespaced d-id cites, 0 unresolvable, 0 not-exempt |
| `helm package chart` → `pgsi-0.1.0.tgz` | 0 | 36138 bytes; the artifact R11.9's interim form names |
| `helm lint /tmp/pgsi-evidence/pgsi-0.1.0.tgz` | 0 | the packaged chart lints (and `helm lint chart` → rc 0 the same call, so packaging did not change lint status) |
| `helm template pgsi <tgz> -f values-<p>.yaml` vs `helm template pgsi chart -f values-<p>.yaml`, p ∈ {default, production, lab} | 0 / 0 / 0 | `diff` → 0 lines in all three profiles: the tarball renders byte-identically to the chart directory |
| `grep -rn 'ghcr\.io/frankellis' $(git ls-files)` | 0 | 11 occurrences in 11 files; 3 are prose copies of the claim itself, 3 are `make render` output, 1 is a symlink → 4 hand-written sites |

The gate rc proves resolvability, not correspondence, so the cited lines were read:
`README.md:120-132` (the section replaced), `phase-0.md:76` and `:83` (the rows rewritten),
`Makefile:21`, `chart/values.yaml:30`, `.github/workflows/ci.yml:27`,
`.github/workflows/release.yml:36`, `Makefile:221-227` (the `render:` recipe that writes
`deploy/plain-yaml/`).

## Deviations from the spec

**R11.9 — the row's artifact column names a step that does not exist.** The row reads
`README.md`, CI (`helm package` via `make helm-lint`/`image`). Measured: `Makefile:215-218`
(`helm-lint:`) runs `helm lint` over three profiles and nothing else, and the Makefile's
30 targets include no packaging target; `.github/workflows/ci.yml`'s helm job (:208) has
`helm lint` (:217) and `helm template snapshot test` (:222), and its only `upload-artifact`
step is the Playwright report (:199-201). So CI does not build the tarball the row and §5-D1
both point at — the tarball verified here was produced locally by `helm package chart`.
`Makefile` and `ci.yml` are A2's (roster `docs/agents.md:58`), and ci.yml carries a
cite-inventory block at :268-285 that constrains where a line may be inserted, so the step
is A2's to add, not mine to smuggle in.

**R11.16 — blocked on Q6, not on the code.** The ghcr publish path is written (ci.yml's
main-path login + `buildx --push` + `:dev` + SHA tag) and `scripts/push-oci.py` is the
documented no-login fallback that produced the reference image. What is missing is the
org: `IMPLEMENTATION.md §5-D10` (:191) records `ghcr.io/frankellis/pgsql-insights` as
*provisional* and `docs/requirements/open-questions.md` Q6 as unresolved, and §5-D1 says
settling it "flips three lines (§5-D10) and the README's provisional banner off". This
commit corrects the count in my own copy of that claim (four hand-written sites, not three
— `.github/workflows/release.yml:36` is not named by §5-D10); §5-D10's own text is A3's.

## Answers to the proposal's open questions

The coordinator's assignment said to do the registry-independent half and "mark the rest
blocked with the reason, rather than inventing a registry name" — that is what this record
does. `frankellis` is not an invention: it is the value already shipped in four files and
recorded in §5-D10, so the open question is a **confirmation** of it (or a replacement),
which is the user's call. Until it arrives, R11.9 and R11.16 stay `in-progress`.
