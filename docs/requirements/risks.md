# Risks

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| K1 | **Prometheus cardinality explosion** kills adoption (the classic failure mode of this product class) | High | High | §5 budget with a CI-checkable assertion (R13.4); `topN` default 50 **per cluster**; query text never a label outside top-N; the R5.4 500-database guard |
| K2 | **`pg_stat_statements` is not installed** on most CNPG clusters (measured absent on all three reachable reference clusters), so the headline feature has no data on a stock install | **Confirmed** | High | R3.1 detection + **R3.15 enablement as a product flow** (scheduled Phase 2 so the rolling restart has lead time); R10.27 makes the not-installed screen first-class; Phases 1–2 and 4–5 deliver value without it; documented in the README's first paragraph, not a footnote |
| K3 | **Credential acquisition** — reading `<cluster>-app` needs secret read access; some teams won't grant it | Medium | Medium | R1.8 four-level precedence; R12.4 documents the `pg_monitor` role path (and its R3.10 consequence); S3 (9187) needs no SQL credential at all, so Phase 2 works credential-free |
| K4 | **Scope creep** — the parity list is ~25 capabilities and the brief also demands implementation | High | High | The phase table is the contract; anything not in §7.2 is out; `IMPLEMENTATION.md`'s deviations log makes scope changes visible rather than silent |
| K5 | **SQLite single-writer** limits horizontal scale | Medium | Medium | §3.4 documents the limit, the R11.15 access-mode contract and the v1.x path (Postgres-backed store); R13.2 sets a 50-cluster target SQLite comfortably meets |
| K6 | **PG version drift** — PG18 changes query-id jumbling and removes/renames columns; a 17→18 upgrade silently breaks baselines | Medium | Medium | R2.4 matrix-as-data with per-view column sets; R3.4's fingerprint + R9.5b's fingerprint-labelled family survive id changes; R16.2 tests on two majors live |
| K7 | **The reference environment is one k3s cluster** — we cannot validate multi-cluster or multi-tenant behaviour from it | Medium | Medium | R13.9 marks untested cells honestly; R11.6's lab profile gives a kind/minikube validation path |
| K8 | **CNPG operator instability in the reference env** (99 restarts) may make discovery flaky during development and be mistaken for our bugs | Medium | Low | R1.9 stale-grace behaviour; R8.7 `/readyz` gated on a real sync; validation script distinguishes "operator unreachable" from "our parse failed" |
| K9 | **Frontend quality** — "extremely easy-to-use" is the brief's emphasis and the easiest thing to under-deliver on | Medium | High | R10.2 forces the `dataviz` skill before chart code (with a named fallback); R10.12/R10.28 force designed empty states; R10.14 sets a load-time budget; **R16.7 adds UI tests** where 0.1.0 had none; A4 owns UI as a dedicated role |
| K10 | **Our own `/metrics` duplicates `cnpg_*`**, so users see the same number twice under two prefixes | Medium | Low | R9.7 re-exports deliberately with a documented mapping table; the chart README explains which prefix to alert on |
| **K11** | **The reference environment's k3s v1.37.2 is outside CNPG 1.30's supported Kubernetes window** (CNPG supports 1.34–1.36; 1.37 is "tested but unsupported"). An operator bug we hit may be a k3s-1.37 artifact, and R13.9 cannot claim a supported-cell result from it | Medium | Medium | R13.9 states it explicitly; a kind-hosted CNPG on a supported k8s version is the R16.2 integration path, so "tested" cells are tested somewhere supported |

---

