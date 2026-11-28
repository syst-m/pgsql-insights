# ## Appendix A — Change log from 0.1.0 to 0.2.0

| Area | Change |
|---|---|
| Header | PG range 13–18 → **15–18** (13/14 documented, unvalidated); Kubernetes 1.29–1.37 → **1.34–1.36** with the reference env's 1.37 flagged; CNPG 1.30.x → **1.29–1.30**; measured PG versions 16.11/17.11 added |
| §1.3 | Added the Grafana-embedding non-goal with its argument (A4-M6) |
| §1.4 | "Zero per-cluster prerequisites" row corrected — one gated `spec.parameters` patch unlocks Query Insights (A3-B3) |
| §3.1 | `--role=collect\|serve` deferred to v1.x; v1 ships one role (A3-M1); subcommand surface unified with §3.5 |
| §3.2 | S2 reachability qualified; S3 family counts added; **S10 `pods/log`** added (A3-M6); S3 `pg_stat_io` caveat added (A3) |
| §3.4 | Access-mode row added (A3-M1/R20) |
| R1 | +R1.10 per-port transport table, +R1.11 `pods/proxy` caveat; R1.2 now reads container statuses; R1.5 gains `servingInstances` |
| R2 | R2.4 reframed to per-view column sets (A3-B7); +R2.9 global scheduler budget (A3-M2) |
| R3 | R3.1 points at R3.15; R3.2 column set corrected; R3.3 PG13 path marked unvalidated; R3.4 canonical key = fingerprint; R3.5 PG≤10 branch demoted to a note; R3.6 two-profile range; R3.9 buckets named + bias stated; R3.10 three EXPLAIN constraints added; R3.12 all six tag keys + one spelling; **+R3.15 enablement flow** |
| R4 | R4.1 gains `Query type`, the summary scorecard, icons, and the disabled-`Action` rule; R4.4 names `pg_blocking_pids`; **+R4.8 value-equality acceptance** |
| R5 | R5.1's Log-errors card sourced (R5.10); R5.5 prefers S4; **+R5.10 log-source ingestion** |
| R6 | R6.1 gains the 80-family measurement; R6.4 scoped to sessions + rollup (A3-M5) |
| R7 | R7.1 and R7.3 moved post-0.1; R7.3 and R7.4 labelled CNPG-derived (A4-M11) |
| R8 | R8.2 gains the series + search endpoints; R8.3 fixed-catalogue contract (A3-B6); **+R8.10 pagination, +R8.11 batched series** |
| R9 | R9.5b fingerprint label (A3-M10); **R9.9b → R9.11**; **+R9.12 `pgsi_up`, +R9.13 histogram accounting**; R9.8 gains the scheduler metrics |
| R10 | R10.2 named fallback; R10.3 gains four rollup pages; R10.5 split into query-load + resource-load; R10.6 saved-view scoping; **+R10.15–R10.28** (14 UI requirements from A4's missing-UI list) |
| R11 | R11.2 distroless-static decided + `httpGet` probes + `/tmp` emptyDir; R11.4 "compressed" defined; R11.6 three profiles; R11.9 tarball fallback + org dependency; **+R11.12 values-default, +R11.13 generated plain-yaml, +R11.14 deploy-and-observe, +R11.15 access-mode, +R11.16 image publication** |
| R12 | R12.1 gains `pods/log`; R12.4 states the `SELECT` consequence; R12.5 uses `-ca` + `verify-ca` (A3-N5); R12.8 generalized to all mutations; R12.9 gains `pods/log` in the threat model |
| R13 | R13.4 number corrected; R13.9 split into two axes with cell states; **+R13.11 clock contract** |
| R14 | R14.1 "same commit" scoped to the last commit of the phase; **+R14.8 requirement→file→status table** |
| R16 | R16.3 names the seven `testdata/` files; **+R16.6 ground-truth fixture, +R16.7 UI tests** |
| §5 | Recomputed: **~24.3 k series / ~1.58 GB over 30 d at 60 s** (was "~22 k / 2.6 GB at 15 s"); `topN` declared per cluster; histogram family added; two more divergence rows |
| §6 | Phase column generated from §7.2 (four wrong cells fixed); 12 missing Cloud SQL capability rows added |
| §7 | Phase 6 split into **6a/6b**; R3.10–R3.11 resolved to 6b and removed from Phase 8; **all 167 IDs indexed** in §7.3 |
| §8 | "Reachable for SQL?" column added; checks 8/9 re-scoped to the 3 SQL-reachable clusters; check 2 gains `servingInstances: 0` |
| §9 | **+K11** (reference env outside CNPG's supported k8s window); K2 upgraded from "likely" to "confirmed" |
| §10 | Q1 resolved; Q6 escalated to a Phase 0 blocker |
| §11 | Full review log, four rulings, disposition table |
