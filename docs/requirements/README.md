# pgsql-insights — Requirements Specification

The spec's §1, §2, §3, §5, §8, §9, §10, §11 and Appendix A, split out of the root file.
`requirements.md` at the repo root is the shell — the preamble plus §4, §6 and §7 — and this
directory is the detail.

**How to read it.** The sections were moved, not copied: the root file no longer carries them.
A `path:N` citation written before this split resolves against `19eb117:requirements.md` — the
last rev where the whole spec was one file — because the checker grades a revved cite against
that rev's copy, not this one. §4 (the requirement rows), §6 (the parity matrix) and §7 (the
delivery plan) stayed in the root file: they are the three sections every other file in the repo
cites by line number, so keeping them in place is what let the 66 inbound citations be pinned
rather than rewritten to nine different files.

## Sections

| File | Spec section | Lines at `19eb117` |
|---|---|---|
| [product-definition.md](product-definition.md) | §1 Product definition | 34–91 |
| [personas.md](personas.md) | §2 Personas and jobs to be done | 92–114 |
| [architecture.md](architecture.md) | §3 Architecture | 115–265 |
| [cardinality.md](cardinality.md) | §5 Cardinality and retention budget | 532–579 |
| [validation-plan.md](validation-plan.md) | §8 Validation plan against the reference environment | 775–806 |
| [risks.md](risks.md) | §9 Risks | 807–824 |
| [open-questions.md](open-questions.md) | §10 Open questions | 825–837 |
| [review-log.md](review-log.md) | §11 Review log | 838–908 |
| [change-log.md](change-log.md) | Appendix A — Change log from 0.1.0 to 0.2.0 | 909–940 |

## Shell

The root [requirements.md](../../requirements.md) keeps the preamble, §4, §6 and §7 — 494 lines
of content — plus the 16-line index block above, for 510. The 446 lines this directory holds and
those 494 are the 940 lines at `19eb117`.
