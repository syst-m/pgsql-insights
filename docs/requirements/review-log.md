# Review log

The brief requires **two adversarial reviews** of this document, with feedback incorporated.

| Round | Reviewer | Scope | Findings | Incorporated |
|---|---|---|---|---|
| — | internal consistency pass | R15/R16 authored from the 0.1.0 self-review | 2 groups added | ✅ in `97172a4` *(the 0.1.0 §11 footer said "added from an internal consistency pass" while the table had no row for it — A3-N8; this row is that row)* |
| 1 | **A3** (opus) — architecture, feasibility, spec-vs-research consistency → `docs/reviews/round-1-architecture.md` | 7 blocking · 10 major · 8 minor · 6 proposed IDs | ✅ all 25 dispositioned |
| 1 | **A4** (sonnet) — product scope, UI/UX, packaging, testability → `docs/reviews/round-1-product.md` | 8 blocking · 14 major · 8 minor · 14 proposed UI reqs + 8 other IDs | ✅ all 30 dispositioned |
| 2 | **A3** (opus) — v0.3.0 architecture review → `docs/reviews/v0.3.0-architecture-review.md` | 4 blocking · 8 major · 6 minor | ✅ in `c416390` — 26 spec edits, with the two reviewer proposals filed as **R16.8** and **R14.9** under existing groups per rule 4 (D16), and `scripts/check-parity-matrix.sh` written to make the propagation CI-enforced rather than re-breakable |
| — | **A3** (opus) — Phase 1a follow-up, re-review of `f08b6ac` | the two rulings D18 records, plus 4 defects in the amendment itself — two of them in the new text (`--cors-origins` mis-spelled vs the code's `--cors-allow-origin`; the R8.9 row citing `S1` where the auth finding is `S2`), one stale (D18 describing `main.go`'s flagset as it was at HEAD, not as A1's working tree now has it), one missing (no v0.3.0 row in this table — the same class A3-N8 flagged at 0.1.0) | ✅ folded into this document and D18 amended |
| — | Live probe P1–P9 (`docs/research/live-probe.md` §9) | 9 queued changes | ✅ folded in (P1→R13.9/§8, P2→R3.1/R3.15/R12.8, P3→R1.10, P4→R1.11, P5→R5.5/R10.5, P6→R1.2, P7→R11.2/R11.14, P8→R6.1, P9→R16.3) |
| — | **Consolidation into `0.2.0`** | this document | ✅ |
| — | **Consolidation into `0.3.0`** (Phase 1 split into 1a/1b, D13) | this document | ✅ — the round-2 incorporation `c416390` plus the Phase 1a rulings `f08b6ac`/D18. The 0.2.0 row existed *solely* because A3-N8 flagged that the 0.1.0 footer claimed a pass the table didn't list; the header has read `0.3.0` since `c416390` with no matching row, which is that same defect one version up |
| — | **A3** (opus) — Phase 5 Go-half review of `f9aae41` (auth, CORS, OpenAPI, and the §3.7 table written for it) | 6 code findings (2 major in `auth.go`, 1 major in `cors.go`, 3 minor) · 3 defects in the §3.7 requirement→file→status table · 1 stale implementation note | ✅ this commit — the spec half: R8.1 split into **R8.1 / R8.1b / R8.1c** (D13), R8.8 gains the **fail-closed** and **separate-secret** clauses (D14), R8.9's implementation note marked resolved, R9.4 gains **`lock_time`** (the R9.11 ruling). The code fixes are A2's; the `git mv` to `api/openapi.yaml` and the CI validator step are tracked as R8.1(a) and R8.1c rather than claimed here |

### Reviewer disagreements, ruled on (rule 4)

Where A3 and A4 conflict, the spec owner decides and records the reasoning rather than averaging.

**1. R3.2's per-query load columns — A3's reading adopted.**
A4 (B2) said `Load by CPU`, `Load by IO wait` and `Load by lock wait` are all unavailable per query.
A3 (B2) showed `Load by IO wait` **is** derivable per query (`blk_read_time`/`blk_write_time` ≤PG16,
`shared_blk_read_time`/`shared_blk_write_time` PG17+), so only lock wait is missing. A3 is more
precise and cites the source correctly. R3.2 now sources CPU and IO per query, marks lock wait
`n/a`, and keeps A4's other two fixes (sourcing the three load columns at database/user level via
R3.7, and recording the `Analyze latency` deferral as a Gap).

**2. R3.6's query-length range — A4's two-profile range adopted.**
A4 (B1) said 0.1.0 shipped Enterprise's 256–4 500/default 1 024 while §1.2 declares Enterprise Plus
(1 024–100 000, default 10 000) the baseline, and that §5 mislabelled 4 500 B as "(Enterprise
Plus)". A3 marked R3.6 correct — but checked it against `cloud-sql-insights.md` §3.2's *Enterprise*
default only, so it validated the number without noticing the edition mismatch. A4's fix (range
256–100 000, default 1 024, 10 000 documented as the E+ default) satisfies both readings; §5's
mislabel is corrected in the same edit.

**3. R2.4's `pg_stat_bgwriter` → `pg_stat_checkpointer` — A3's framing adopted.**
A4 listed it under "what I checked and found correct" ("R2.4's five enumerated renames all check
out"). A3 (B7) showed the fifth is not a rename: at PG17 `pg_stat_bgwriter` **shrinks 11 → 4
columns** while checkpoint stats move to a **separate new view**, and CNPG ships three queries
(`pg_stat_bgwriter`, `pg_stat_bgwriter_17`, `pg_stat_checkpointer`). Implementing it as a rename
loses four still-valid bgwriter families on our PG17 clusters. A3's version matches the live-probe
family diff exactly (7 bgwriter families on PG16, 9 checkpointer families + `bgwriter_stats_reset_time`
on PG17), which is the measurement that settles it. R2.4 now says "per-view column sets per major,
not a rename table".

**4. ID collisions — one ID per feature, and the numbering scheme preserved.**
*Extension enablement:* A4 proposed `R3.15` at **Phase 2**; A3 proposed `R17` at **Phase 3**. Adopted
**R3.15, Phase 2** — filed under R3 rather than as a new R-group (R1–R16 is the taxonomy; adding
R17+ as new groups breaks it), and on A4's argument that a rolling restart takes hours and cannot
be a same-phase prerequisite for the Phase 3 collector. A3's `R17` is recorded as an alias.
*`pgsi_up`:* A4 proposed `R9.11`; A3's N2 renumbers the existing `R9.9b` to `R9.11`. Both cannot
have it. Adopted **R9.11 = the renumbered `R9.9b`** (A3's N2, since retiring the letter suffix is
the fix) and **R9.12 = `pgsi_up`**, with A3's fuller label set `{namespace,cluster,pod,instance_role}`
— which also answers A4's B8 question about whether it carries `pod`.
*General rule applied:* all eight of A3's proposed IDs (R17–R23) and all of A4's (R3.15, R9.11,
R11.12–R11.14, R13.11, R16.6–R16.8, R1.10, R1.11) are absorbed into the existing R-groups, with the
mapping recorded here so no finding is lost.

### Disposition summary

| Reviewer | Blocking | Major | Minor | Disposition |
|---|---|---|---|---|
| A3 | 7 | 10 | 8 | 24 accepted, 1 partially (B2 — see ruling 1), 0 rejected |
| A4 | 8 | 14 | 8 | 28 accepted, 2 partially (B2, B1 — see rulings 1–2), 0 rejected |

Both reviews' "what I checked and found correct" sections are recorded in their files and informed
which 0.1.0 requirements were left unchanged.

---

