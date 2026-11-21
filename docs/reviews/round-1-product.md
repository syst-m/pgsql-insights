# Round 1 review — product scope, UI/UX, packaging, testability (reviewer A4, sonnet)

**Date:** 2026-11-20 · **Spec reviewed:** `requirements.md` v0.1.0-draft (commit `97172a4`, 644 lines)
**Sources read in full:** `requirements.md`; `docs/research/cloud-sql-insights.md` (949 l);
`docs/research/cloudnativepg.md` (§2.1–2.4, §2.8.1–2.8.6, §4.5–4.10, §5–5.5, §6.3–6.5, §7.0–7.1, §8);
`docs/ENVIRONMENT.md`; `docs/research/live-probe.md` (221 l, folded in per the coordinator's note).
**Findings:** 8 blocking, 14 major, 8 minor (30 total).

---

## Verdict

This is a strong draft — the provenance discipline is real, the S1→S2→S3 spine is correct and now
empirically confirmed, and the parity matrix is honest about divergences in a way most specs are not.
It is not, however, buildable as written in nine phases by four agents, and its UI section is the
weakest part relative to the brief's own emphasis ("extremely easy-to-use graphs" is the constraint
with the thinnest spec behind it). The core problem is a class of requirement that states a Cloud SQL
column, chart or page and marks it **[A]** without checking that the sources in §3.2 can actually
produce it — R3.2's lock/CPU columns are the clearest instance, and the same pattern recurs in
R10.5's four-series graph and R10.6's sparklines. The second problem is phase shape: Phases 6 and 8
each bundle two to three phases of work into one agent slot, and Phase 3's acceptance criterion is
unexecutable on the reference environment as it currently stands.

**The three biggest problems.**
1. **Requirements that their own §5 contradicts** (B2/B3): R3.2 and R10.5 promise per-query lock
   wait and CPU load, which §5 explicitly says `pg_stat_statements` cannot supply. An [A] requirement
   that cannot be satisfied will fail its own acceptance test in Phase 6.
2. **Phase 3's acceptance is unexecutable and Phase 0's is insufficient** (B4/B5): `pg_stat_statements`
   is absent on all three reachable clusters, so Phase 3's "top-N matches a manual SELECT" check
   compares an empty table to an empty table; and Phase 0 accepts on *building* an image that, on an
   arm64 dev machine with amd64-only nodes, cannot schedule.
3. **The UI has no interaction contract** (M2/M3/M4/M6): no time-range picker, no URL state, no
   per-chart interaction spec, no endpoint behind R10.6's sparklines, and the chart library deferred
   to a skill while three other requirements silently depend on which library gets chosen.

---

## Findings

### BLOCKING

**B1 — R3.6 ships Enterprise's query-length range while §1.2 declares Enterprise Plus the baseline** (§R3.6, §1.2, §5)

*What's wrong.* R3.6 reads "configurable 256–4500 bytes, default **1024** — Cloud SQL's exact
`queryStringLength` range and default", and §5's divergence table labels the row
"4 500-byte query text (**Enterprise Plus**)".

*Why it's wrong.* `cloud-sql-insights.md` §3.1's console table gives two ranges: Enterprise
"256–4500 bytes, default 1024" and Enterprise Plus "1024–100000, default 10000"; §3.2's
`InsightsConfig` schema and §3.9's limits table and §2's editions matrix repeat it. So 4 500 B is
**Enterprise's** ceiling, not Enterprise Plus's. §1.2 says "we implement the Enterprise Plus feature
set as the baseline and match Enterprise's cheaper limits where the Enterprise Plus ones are
impractical" — but §5's justification for choosing 4 500 B is "4 500 B × 4 000 queries is the cost
Google absorbs on instance disk", which is the *Enterprise Plus* query count paired with the
*Enterprise* byte cap. The row is mislabelled and the range is the wrong edition's.

*Fix.* R3.6 should read: "**Query text length** configurable 256–100 000 bytes; default **1 024**
(the Enterprise ceiling, our default) with **10 000** available as the documented Enterprise-Plus
default; longer text truncated and the truncation flagged." §5's row should read
"256–4 500 B (Enterprise) / 1 024–100 000 B, default 10 000 (Enterprise Plus) | 1 024 default,
256–100 000 range".

**B2 — R3.2 requires per-query `Load by lock wait` and `Load by CPU`, which §5 says don't exist; and omits `Analyze latency` without recording the deferral** (§R3.2, §5, §1.3)

*What's wrong.* R3.2 is marked **[A]** and demands "the Cloud SQL column set **reproduced exactly**",
listing `Load by CPU`, `Load by IO wait`, `Load by lock wait`. §5's own divergence table says:
"Per-query `lock_time` with `lock_type` | **Not available per query** | `pg_stat_statements` has no
lock-time column." A requirement cannot both reproduce a column exactly and declare it unavailable.
`Load by CPU` has the same problem: `cloud-sql-insights.md` §3.7's per-query family is
`{execution_time, io_time, latencies, lock_time, row_count, shared_blk_access_count}` — there is no
per-query CPU metric either; Google derives it. And R3.2's list omits `Analyze latency`, which
`cloud-sql-insights.md` §3.3 lists as a Queries-tab action, without noting that it is deferred with
AI troubleshooting per §1.3.

*Fix.* R3.2 should scope the per-query table to what `pg_stat_statements` supplies — `Query`,
`Database`, `Recommendations`, `Load by total time`, `% load by Queries`, `Avg execution time (ms)`,
`Total execution time (ms)`, `Avg rows returned`, `Times called` — and add one sentence: "`Load by
CPU`, `Load by IO wait` and `Load by lock wait` are supplied at database/user level by R3.7 and are
not per-query on PostgreSQL (see §5); Cloud SQL's `Analyze latency` action is deferred with AI
troubleshooting (§1.3)."

**B3 — R10.5's four-series "Database load" graph can't render two of its four series, and its replication view has no named data source** (§R10.5, §R3.8, §R5.5)

*What's wrong.* R10.5 specifies "the Cloud SQL 'Database load' graph — stacked area of `CPU capacity`,
`CPU and CPU Wait`, `IO Wait`, `Lock Wait`". From §3.2's sources we can source total execution time
(S2 `pg_stat_statements`) and IO time (`pg_stat_database.blk_read_time`/`blk_write_time`, or
`shared_blk_read_time`/`shared_blk_write_time` on PG 17 per `cloudnativepg.md` §7.1) — but not
per-cluster lock *duration* (`pg_locks` gives lock counts, not seconds; `pg_stat_database` has no
lock-time column), and `CPU capacity` is a container limit from S7, which R5.3 marks **optional**.
So on a lab install with no cAdvisor, three of four series are blank. Separately, R10.5 promises "a
replication topology view (primary→standby with per-standby lag)" but names no source; R5.5 only
covers the empty-state case.

*Fix.* R10.5 should specify two graphs instead of one: (a) **query load** — stacked area of total
execution time + IO time, with a note that lock wait is not available at this level; (b) **resource
load** — CPU capacity vs CPU usage and memory working set, rendered only when S7 or `metrics.k8s.io`
is present (R5.3's degradation rule applies), with a one-line explanation when not. And R10.5's
replication view should name its source: prefer S4's `replicationInfo[]` / `replicationSlotsInfo[]`
(`live-probe.md` §7, 27 verified fields on a primary, no SQL and no credentials) over S2's
`pg_stat_replication`.

**B4 — Phase 3's acceptance criterion is unexecutable on the reference environment as written** (§7 Phase 3, §R3.1, §8, `live-probe.md` §1)

*What's wrong.* Phase 3's acceptance is "Top-N table for each cluster matches a manual
`SELECT … FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 50` within tolerance".
`live-probe.md` §1 measured `pg_stat_statements` **ABSENT on all three reachable clusters**, with
`shared_preload_libraries` empty on all three. The manual SELECT returns zero rows, so the check
compares an empty table to an empty table and passes vacuously. Worse, enabling it is not a
`CREATE EXTENSION` — `live-probe.md` §1 Finding 2 shows the extension requires a postmaster-level
`shared_preload_libraries` patch plus a **rolling restart of every instance**, which directly
contradicts R1.6's "zero-prerequisite rule" for the Query Insights path and the brief's "rapidly
rollable out".

*Fix.* Three edits, together. (i) R3.1 should become a product flow, not a docs string: "detect →
offer → patch `spec.parameters` → verify", generalising R12.8's `--allow-actions` gate to cover the
`Cluster` patch (this is `live-probe.md` §9 P2). (ii) Phase 3's acceptance should read: "on a cluster
where the extension is enabled, the top-N table matches the manual SELECT within tolerance; on a
cluster where it is not, R10.27's not-installed screen renders and the top-N correctness is verified
against the `testdata/` fixture." (iii) The enablement action moves to **Phase 2**, not Phase 3, so
Phase 3 has data to render — the rolling restart takes hours on a real cluster and cannot be a
Phase 3 task.

**B5 — Phase 0 accepts on *building* the image, not on it running; all five nodes are amd64 and the dev machine is arm64** (§7 Phase 0, §R11.2, `live-probe.md` §7)

*What's wrong.* Phase 0's acceptance is "`docker buildx build --platform linux/arm64,linux/amd64`
succeeds". A successful build proves the manifest exists, not that the amd64 layer is a valid
executable — a `FROM alpine/arm64`-style base mistake, or a buildx cache that produced a
single-arch manifest list, both pass this check and then ImagePullBackOff on deploy. `live-probe.md`
§7 measured all five nodes as **amd64** (`instanceArch: amd64` cross-checked from the pod), while
`uname -m` on the dev machine is arm64 — so the amd64 variant is the one that matters here and the
one least exercised by a laptop build. `requirements.md` R11.2's rationale ("the dev machine is
arm64; the brief's adopters are mixed") rests on `ENVIRONMENT.md`'s line 125 claim that "k3s nodes
are arm64-capable here", which `live-probe.md` §7 corrects.

*Fix.* Phase 0's acceptance should read: "`docker buildx build --platform linux/amd64,linux/arm64`
succeeds **and the resulting image is deployed to the reference cluster and reaches `Running`**
(verified via `kubectl get pod -o jsonpath` on the amd64 node), and `/healthz` answers." R11.2's
rationale sentence should be corrected to "all five reference nodes are amd64 (`live-probe.md` §7);
arm64 support serves other adopters and the dev machine."

**B6 — Phases 6 and 8 each bundle two to three phases of work into one agent slot** (§7)

*What's wrong.* Compare the phases by requirement count and by page count. Phase 5 = 9 API
requirements + 4 UI requirements + 1 page (Fleet). Phase 6 = R4.1–R4.7 (7) + R3.9–R3.13 (5) +
R10.5–R10.9 (5 pages: Cluster Overview, Queries, Query Detail, Active Queries, Wait Events) — and
R3.9's sampled histogram and R3.11's plan-change detection are each multi-day algorithms. Phase 8 =
R6.1–R6.5 (5) + R7.1–R7.6 (6, including the index advisor, which is a project on its own) + R5.9 +
R3.10–R3.11 + R10.11. Phases 6 and 8 are each 2–3× Phase 5's scope with the same single agent.

*Fix.* Split Phase 6 into **6a** (R4.1–R4.7 + Active Queries + Wait Events pages, A1) and **6b**
(R3.9–R3.13 + R10.5 Cluster Overview + R10.6 Queries + R10.7 Query Detail, A2). Move **R7.1** (index
advisor) and **R7.3** (rightsizing) to post-0.1 and keep R7.2/R7.4/R7.5 (bloat, autovacuum, wraparound
— all single SQL queries) in Phase 8. That makes Phase 8 = R6 + three SQL recommenders + R5.9 +
R10.11, which is one phase.

**B7 — R16 has no ground-truth correctness test; eight of §8's ten [A] checks are presence checks** (§R16, §8)

*What's wrong.* R16.1–R16.5 cover unit tests, integration execution, fixtures, golden Helm output and
a validation script. None of them asserts that a **number is correct**. Of §8's ten checks: #1 counts
clusters, #2 reads a phaseReason string, #3 checks `pgsi_up == 1`, #4 checks tests exist and
collection runs, #5 checks `promtool` exits 0, #6 checks series exist in Prometheus, #7 checks Helm
templates match, #9 checks five cards *render*. Only #8 (active list vs a `pg_stat_activity` dump) and
#10 (pgbouncer client address differs) compare our computed output to an independent source. A
collector that reports TPS = 0, cache hit ratio = 3 %, or a bloat ratio off by 10× passes every check
in §8. R16.2's integration test asserts the SQL "actually executes" — not that it returns the right
answer.

*Fix.* Add **R16.6 — Ground-truth fixture**: "a seeded Postgres with known values — N inserts in a
known transaction count (assert TPS == N), one query of a known duration (assert `mean_exec_time`
within tolerance), a known dead-tuple count (assert the R7.2 bloat ratio), a known two-session lock
pair (assert R4.4's chain) — asserted by R16.2's integration test. This is the only requirement that
catches a *wrong number* rather than a crash." Phase 2. Additionally, `live-probe.md`'s captured
expositions (`testdata/cnpg-metrics/`, four files) make R16.3's fixture set real rather than
hand-written — R16.3 should cite that path directly.

**B8 — `pgsi_up` is depended on by §8 check 3 and Phase 5's acceptance but defined by no requirement** (§R9.8, §8, §7 Phase 5)

*What's wrong.* §8 check 3 asserts `pgsi_up{cluster="cookbook-pg"} == 1`, and Phase 5's acceptance is
"`GET /api/v1/query?query=pgsi_up` returns a Prometheus-shaped result". R9.8's enumerated
self-observability set is `pgsi_build_info`, `pgsi_collector_duration_seconds`,
`pgsi_collector_errors_total`, `pgsi_scrape_target_up{cluster,pod}`, `pgsi_store_size_bytes`,
`pgsi_http_request_duration_seconds`. There is no `pgsi_up`. `pgsi_scrape_target_up` is per scrape
target (a pod), not per cluster, and it is not what an API-level query for `pgsi_up` would return.

*Fix.* Add `pgsi_up{namespace,cluster}` to R9.8's list — 1 when that cluster's collector round
completed without error in the last interval, 0 otherwise — and state in R9.3 whether it also carries
`pod`. This is a one-line spec change now and a metric migration if it's discovered in Phase 5.

### MAJOR

**M1 — R10.3's page list omits four pages that R3.7 and R8.2 require** (§R10.3, §R3.7, §R8.2)

R3.7 requires rollups by database, user, application name and client address; R8.2 exposes
`/insights/databases`, `/insights/users`, `/insights/tags`; `cloud-sql-insights.md` §3.3 has
Databases / Users / Tags / Client addresses tabs. R10.3 lists ten pages and none of them is any of
those four. **Fix:** R10.3 adds "Databases, Users, Tags, Client Addresses" to the page list — or
states explicitly that R3.7's rollups render as tabs *inside* the Queries page, and names which.

**M2 — No time-range picker is specified anywhere in R10** (§R10, §R5.8, §R8.4)

Cloud SQL's Queries header offers 1 h / 6 h / 1 d / 7 d / 30 d / custom (`cloud-sql-insights.md` §3.3)
and System Insights offers the same plus the documented rule that "Auto Refresh every minute" is
incompatible with custom periods (§4.1). R10.1–R10.14 never mention a time-range control; R8.4 has
`?from=`/`?to=` on the API but no UI spec; R5.8 mandates a 1-minute auto-refresh without saying what
happens to it when a custom range is chosen. **Fix:** add R10.15 (text in *Missing UI specifications*).

**M3 — No URL state / deep-linking spec** (§R10, §R5.6, §R4.7)

Cloud SQL has "Copy Link" on System Insights (§4.1) and every Queries-tab row links to Query Detail.
R10.4 says "click-through to any cluster" and R4.7 says "link it to the R3 query detail", but nothing
says UI state is encoded in the URL — which is the only way a shared link works, and a prerequisite
for R5.6's "View in Prometheus/Grafana" deep link being testable at all. **Fix:** add R10.16.

**M4 — R10.6's per-row sparklines have no supporting endpoint in R8.2** (§R10.6, §R8.2, §R10.14)

R10.6 promises "the R3.2 table with sparklines per row". A sparkline needs a time series per row.
R8.2's list has `/insights/queries` (a list), `/insights/queries/{fingerprint}` (detail) and
`.../plans` — no batch series endpoint. R10.14's <1.5 s budget rules out 50 detail requests. **Fix:**
R8.2 adds `GET /api/v1/insights/queries/series?fingerprints=a,b,c&range=…` returning a compact
per-fingerprint series array, and R10.6 names it as the sparkline source.

**M5 — "Saved views" is impossible under the single shared token** (§R10.6, §R8.8, Q4)

R10.6 promises "a saved-view concept". R8.8 and Q4 fix auth at one shared bearer token in v1 — the
API has no notion of *who* is calling, so a saved view is either global (one shared set, not what
"saved view" means) or localStorage-only (doesn't survive a different browser). **Fix:** scope it —
"saved views are shared, cluster-scoped, editable by any holder of the token" — or move them to
post-0.1 with per-user OIDC (Q4's own answer).

**M6 — R10.2's chart-library deferral hides three implemented-by dependencies, and §1.3 rejects Grafana embedding without argument** (§R10.2, §R5.6, §R11.4, §R10.14, §1.3)

R10.2 defers the library to the `dataviz` skill, which is right in principle — but three requirements
are *delivered by* that library and are already written as if settled: R5.6's **Download PNG** (needs
a library that can rasterise its own output client-side; not all can), R11.4's **< 60 MB** image (the
built SPA's JS is embedded in the image, so bundle size moves this number directly), and R10.14's
**< 1.5 s** first paint (bundle size again). Separately, §1.3 rejects "Being a Grafana replacement"
in one line and never weighs the middle option — embedding Grafana panels for the System Insights
page — which would deliver R5.6's whole toolbar (PNG, CSV, View-in-Grafana) for free at the cost of
R10.1's single-binary embedding. **Fix:** R10.2 adds "the chosen library must (a) export a chart to
PNG client-side (R5.6), (b) keep the built SPA's gzipped JS under the budget implied by R11.4 and
R10.14, and (c) be recorded in `IMPLEMENTATION.md` **with its bundle size**"; and §1.3 gains one row
weighing Grafana-panel embedding for R10.10, with a reason.

**M7 — R4.1's and R5.1's [A] checks assert presence and rendering, not values** (§R4.1, §R5.1, §8 checks 8 and 9)

§8 check 8: "Active-queries API output equals a direct `pg_stat_activity` dump … on all 4 clusters."
`pg_stat_activity` is a live view; between our 15 s poll (R4.2) and the validator's `psql` call, rows
appear and vanish, so the check isn't repeatable as written. Check 9: "The five System Insights cards
render for all 4 clusters" — a card rendering `NaN`, `-1`, or a wrong number passes. **Fix:** check 8
should read "the API response carries `capturedAt`; the validator queries `pg_stat_activity` within
±2 s of it and asserts **set equality on pids**, not row-by-row field equality"; check 9 should read
"each card's value matches the same quantity computed directly from S2/S3 (connection count from
`pg_stat_activity`, disk % from the PVC stats, TXID age from `cnpg_pg_database_xid_age`) within a
stated tolerance, and the check prints both numbers". This is the R4.1/R5.1 half of B7.

**M8 — Three deployment formats with no generator between them** (§R11.6, §R11.9, §3.5, §R11.11)

R11.6 ships the Helm chart **plus** `deploy/plain-yaml/` **plus** `deploy/kustomize/`; R11.11's golden
test covers the chart only. Nothing keeps the other two in sync — they drift the first time a chart
template changes, and R11.9's quickstart then lies to non-Helm users. **Fix:** R11.6 should read
"`deploy/plain-yaml/` is **generated** by `helm template chart/pgsi -f chart/values-lab.yaml >
deploy/plain-yaml/all.yaml` in CI and committed (the golden test *is* the file); `deploy/kustomize/`
is a two-file kustomization pointing at `../plain-yaml`. Only the chart is hand-maintained."

**M9 — R11.9's two-command quickstart is blocked on an unresolved open question** (§R11.9, §R15.2, Q6)

R11.9 promises `helm repo add` + `helm install`. R15.2 names the repo `ghcr.io/<org>/pgsql-insights`
and Q6 says "org name needed before Phase 0 CI publishes images" — so the quickstart cannot be
executed for the Phase 9 acceptance ("Two-command quickstart executed from a clean namespace and
verified") until the org exists and a chart is published. **Fix:** close Q6's second half by naming
the org in the spec, and give R11.9 a second, Phase-5-verifiable form: "the tarball form
`helm install pgsi ./chart` (already Phase 5's acceptance) is the documented fallback when the
published repo is unavailable."

**M10 — values-production / values-lab miss the middle profile the reference environment needs** (§R11.6)

The two profiles are production (PVC + token auth + ServiceMonitor on) and lab (in-memory + auth off +
ServiceMonitor off). The reference environment is a real cluster where you want a PVC-backed store and
a ServiceMonitor but are iterating and don't want to wire a token — and ENVIRONMENT.md's whole thesis
is that install must work with no credentials handed out. There is no profile for "PVC + ServiceMonitor
on + auth off", so `helm install pgsi ./chart` with no `-f` gives an unspecified configuration. **Fix:**
R11.6 adds a third documented profile `values-default.yaml` (PVC store, `--auth=none`, ServiceMonitor
on) as the chart's **shipped default**, with production and lab as the two documented overrides.

**M11 — R7.3 and R7.4 are CNPG-dashboard gaps presented as Cloud SQL clones, and R7.3 is mis-phased** (§R7.3, §R7.4, §1.2, §6, §7 Phase 8)

`cloud-sql-insights.md` §7's Recommenders list is: out-of-disk, idle instance, overprovisioned,
underprovisioned, high-XID; plus Index advisor, Flag recommender, Google vacuum management, OOM
session cancel, Performance Capture. **R7.4 (autovacuum insight) is not in that list** — it comes from
`cloudnativepg.md` §8.2 gap #13 ("no `pg_stat_user_tables` autovacuum last/next/analyze panels in the
dashboard at all"). The spec's own rule (lines 20–22) is that departures are marked **Divergence** and
gaps **Gap**; R7.4 is neither — it's an unlabelled addition. R7.3 (rightsizing) has split provenance
(Cloud SQL's over/underprovisioned recommenders *and* `cloudnativepg.md` §8.2 gap #16 "two gauge
panels exist, no recommendation") and is a sizing *engine* bundled into an already-oversized Phase 8.
**Fix:** mark R7.4 "**CNPG-derived** (`cloudnativepg.md` §8.2 gap #13), not a Cloud SQL capability" and
add a §6 row with the Cloud SQL column reading "(none — CNPG gap)"; scope R7.3 in v1 to "report
requested vs measured CPU/memory per instance with a direction using a documented threshold" and move
the sizing formula itself to post-0.1 with R7.1 (see B6).

**M12 — R14.1's "same commit" rule is unworkable and R14 has no traceability artifact** (§R14.1, §R14.2)

R14.1 requires `IMPLEMENTATION.md` be "updated in the **same commit** as every phase's code". Phases
1–9 each produce many commits (Phase 6 alone is 12 requirement IDs after B6's split); either every
commit touches the file — noisy and conflict-prone across four agents — or the rule is silently
violated. R14.1 lists six contents but not the one a reviewer needs: a **requirement → file/test →
status** table. Without it, "per-task status" is prose and R14.2's validation entries can't be checked
against §4's ~120 requirement IDs. **Fix:** R14.1 reads "updated in the **last commit of each phase**
(and in any commit that changes a documented decision)"; its content list gains "a **traceability
table** mapping every §4 requirement ID to the file(s) and test(s) implementing it and its status
(`todo|wip|done|deviated`)" and "a `Last validated` section recording the date and result of the most
recent `scripts/validate-env.sh` run (R16.5)".

**M13 — R10 has 14 requirements and R16 contains zero UI tests** (§R10, §R16)

R16.1–R16.5 test Go code and the Helm chart. Nothing tests the SPA: no component tests, no smoke test
that the pages render, no visual regression on the charts R10.5–R10.11 specify. R10.12 (empty/error
states "are designed, not defaulted") and R10.13 (keyboard nav, dark/light) are unverifiable as
written — and K9 names frontend quality as the brief's emphasis and the easiest thing to under-deliver
on, with mitigation that is entirely spec-side. **Fix:** add **R16.7 — UI tests**: "component tests
(Vitest + Testing Library) for every page in R10.3 covering its normal, empty, loading, partial and
unavailable states (R10.12); a Playwright smoke test that loads `/`, `/fleet` and one cluster page
against a running container; a visual-regression snapshot of the R10.5 load graph and the R10.6 table."
Phase 5 onward.

**M14 — R13.9's compatibility matrix includes cells CNPG 1.30 cannot produce** (§R13.9, `cloudnativepg.md` §7 preamble)

R13.9 promises a tested matrix of "CNPG 1.28–1.30, PG 13–18". `cloudnativepg.md` §7's preamble states
CNPG 1.30.0 "supports **PG 15–18**; PG 13/14 are included because existing clusters and the
postgres_exporter collector set still cover them". So the CNPG 1.30 × PG 13 and CNPG 1.30 × PG 14
cells are not merely untested — they are impossible. And `live-probe.md` §1 measured the actual
versions as 17.11, 17.11, 16.11, so PG 15 and PG 18 have no fixture at all. **Fix:** R13.9 splits the
matrix into "PG majors CNPG 1.30 can run (15–18)" and "PG majors the SQL layer supports (13–18,
fixture-tested only)", with each cell marked `tested | fixture | impossible | untested`; the four
reference clusters fill exactly the CNPG 1.30 × {16.11, 17.11} cells (which is `live-probe.md` §9 P1).

### MINOR

**m1 — R4.1 omits Cloud SQL's Active-queries summary scorecard, the hourglass/block icons and `Query type`** (§R4.1, `cloud-sql-insights.md` §3.10)
R4.1's 13 columns match §3.10's table exactly, but §3.10 also has a summary scorecard of three
distributions above the table, per-row hourglass/block icons, a `Query type` column, and "Content
display options" for column filtering — none in R4.1 or R10.8. **Fix:** R4.1 adds the scorecard
(counts by state, by backend type, longest-running transaction); R10.8 adds a column-visibility
control.

**m2 — R4.4 doesn't name the function that builds the chain** (§R4.4, `cloud-sql-insights.md` §8)
R4.4 says "from `pg_locks` + `pg_stat_activity`, build the blocker→blockee graph". The correct
primitive is `pg_blocking_pids(pid)` (PG 9.6+), which their §8 row names explicitly; a pure
`pg_locks` join is the pre-9.6 method and produces false edges for table-level locks. **Fix:** "using
`pg_blocking_pids(pid)` for direct blockers and `pg_locks` only to enrich each edge with lock
type/mode."

**m3 — R3.12's SQL-comment prefix `/* pgsio:… */` is a third spelling of the product name** (§R3.12, Q1, §R9.2)
Q1 says the binary/prefix is `pgsi`; R9.2 and R2.8 use `pgsi` and `pgsi-collector`; R3.12 uses
`pgsio:`. **Fix:** one spelling — `/* pgsi:action=…,route=… */`.

**m4 — R11.4's "compressed" is undefined and the 60 MB / 100 MB pair is unexplained** (§R11.4)
Compressed by which tool — the registry's layer digest, `docker save | gzip`, or buildx's zstd? A Go
binary plus a Vite SPA is typically 20–35 MB compressed, so the 60/100 pair reads as uncertainty.
**Fix:** "the `linux/amd64` image's compressed layer size as reported by the registry manifest is
< 60 MB; CI fails above 100 MB. The built SPA's gzipped JS is reported separately in the same CI log."

**m5 — R11.2 leaves distroless-static vs `FROM scratch` undecided, and neither reconciles with R12.7** (§R11.2, §R12.7, §R8.7)
"distroless static (or `FROM scratch` with CA certs)" are two different images. `FROM scratch` means we
own the CA bundle's freshness (R11.5 pins digests, so every CA update needs a rebuild) and there is no
`/tmp` — which R12.7 requires as writable. **Fix:** pick **distroless static** and delete the
parenthetical; state in R12.7 that the writable `/tmp` is an `emptyDir`; note that distroless has no
shell, so R8.7's probes must be `httpGet`, not `exec`.

**m6 — R2.4's matrix omits that `pg_stat_statements_info` doesn't exist in PG 13** (§R2.4, §R3.3, `cloudnativepg.md` §7.1)
R3.3 handles the PG 13 case correctly ("On PG13 (no `pg_stat_statements_info`) fall back to detecting
counter decreases per row"), and `cloudnativepg.md` §7.1 confirms the table "does **not** exist in
PG13; exists PG14+" — but R2.4's enumerated rename/gap list omits it while listing five other
per-major column changes. **Fix:** add it to R2.4's list so the matrix-as-data is complete and R3.3's
branch has a data source.

**m7 — R10.6's 11-column table at 1280 px has no column-priority spec** (§R10.6, §R10.13)
R3.2's table is 11 columns; R10.13's floor is 1280 px. Eleven text columns plus three inline load bars
plus a sparkline plus a row expander cannot fit without a priority order. **Fix:** R10.6 adds "column
priority at 1280 px: Query (truncated, **sticky first column**), Load by total time (bar), Avg
execution time, Times called, Database; the rest behind a column-visibility menu and in the CSV
export."

**m8 — R8.4's pagination contract is unspecified** (§R8.4, §R10.6)
R8.4 gives `?limit=` and `?sort=` but not default page size, max page size, cursor vs offset, or the
tie-break when two rows share a sort key — which for `pg_stat_statements` top-N is common (many
queries with `calls = 1`). Without a stable tie-break, R10.6's server-side pagination returns duplicate
and missing rows across pages. **Fix:** "`?limit=` default 50, max 500; `?cursor=` opaque, returned in
`meta.nextCursor`; no offset pagination. Sorting requires a stable secondary key — `queryid` for query
lists, `pid` for sessions — so pages are disjoint."

---

## Missing UI specifications

Each line below is requirement text to add verbatim to §4 R10.

- **R10.15 — Time-range picker.** "A global control in the app header offering 1 h / 6 h / 1 d / 7 d /
  30 d / custom, driving every chart and table on the page; selecting a custom range disables
  auto-refresh (Cloud SQL's documented behaviour, `cloud-sql-insights.md` §4.1); the selection persists
  in the URL (R10.16)." *(Phase 5)*
- **R10.16 — URL state and deep linking.** "Route + query params encode cluster, database, selected
  fingerprint, time range, filter chips, sort column/direction and page; pasting a URL reproduces the
  exact view. Every table row and every filter chip exposes a copyable link (Cloud SQL's Copy Link,
  §4.1)." *(Phase 5)*
- **R10.17 — Global search.** "A ⌘K palette searching clusters, query fingerprints by normalized text,
  database names and user names, returning typed results that navigate; backed by
  `GET /api/v1/search?q=`." *(Phase 6)*
- **R10.18 — Table virtualization and density.** "Tables above 100 rows virtualize (no full DOM render);
  a compact/comfortable density toggle persists per user session; row height and font size are defined
  per density." *(Phase 6)*
- **R10.19 — Per-chart interaction contract.** "Every chart defines: tooltip contents (series name,
  timestamp, value with unit), legend behaviour (click to isolate, click-again to restore), click-on-series
  navigation target, and crosshair behaviour across stacked panels; the contract is identical across all
  pages and is written down before chart code is written." *(Phase 5)*
- **R10.20 — Column visibility.** "Every table exposes a column-visibility menu (Cloud SQL's 'Content
  display options', §3.10); hidden columns remain in the CSV export." *(Phase 6)*
- **R10.21 — Timestamp rendering and timezone.** "All timestamps render in the viewer's local timezone
  with an absolute tooltip, and every API timestamp is RFC 3339 UTC; relative form ('4 m ago') is used
  only where Cloud SQL uses it (Active queries, Events)." *(Phase 5)*
- **R10.22 — Loading and partial-data indication.** "Every panel shows a skeleton while loading, a
  'data as of HH:MM:SS' stamp once loaded, and a visible partial-data banner when any collector for
  that cluster errored (R2.2), naming which collector and since when." *(Phase 5)*
- **R10.23 — Colour-blind-safe palette and non-colour encoding.** "The chart palette is the `dataviz`
  skill's validated palette; every series is additionally encoded by shape, dash pattern or direct
  labelling so no chart depends on hue alone; dark and light variants are both validated." *(Phase 5)*
- **R10.24 — Cluster switcher and breadcrumbs.** "A cluster switcher in the header (search + recent)
  present on every page below Fleet, plus breadcrumbs Fleet / <cluster> / <page> that are all clickable."
  *(Phase 5)*
- **R10.25 — Keyboard shortcuts.** "`j/k` row movement in tables, `Enter` to open a row, `/` to focus
  search, `t` to cycle the time range, `Esc` to close any panel; a `?` overlay lists them." *(Phase 6)*
- **R10.26 — Alerts surface in the UI.** "An Alerts panel per cluster and a fleet-level alert list,
  sourced from firing Prometheus rules (R9.9b) via the Prometheus API, with `runbook_url` links — the
  Cloud SQL `Notification` button and `Annotations`-on-charts capability (`cloud-sql-insights.md` §7)
  and the `[alertlist]` panel CNPG's own dashboard ships (`cloudnativepg.md` §6.3)." *(Phase 7)*
- **R10.27 — `pg_stat_statements` not-installed as a first-class screen.** "A designed screen (not an
  inline message) shown on Queries and Query Detail when R3.1 detects the extension absent: which
  databases lack it, the exact `spec.parameters` patch, the stated cost (rolling restart of every
  instance), the opt-in enable action behind R12.8's gate, and live progress while the restart rolls.
  This is the default state on 100 % of the reference clusters (`live-probe.md` §1), so it is the
  product's most-seen screen in v1." *(Phase 3)*
- **R10.28 — Empty-state catalogue.** "A table in the spec listing, for each of the ten R10.3 pages, its
  empty / loading / partial / unavailable rendering. R10.12 currently names four reference-environment
  cases; `live-probe.md` §6 Finding 9 adds a fifth — a CR reporting two healthy instances whose
  postgres container is in `CrashLoopBackOff` — which must render as degraded, not green." *(Phase 5)*

---

## Missing requirements

| Proposed ID | Requirement text | Phase |
|---|---|---|
| **R3.15** | "**Extension enablement as a product action.** `POST /api/v1/clusters/{ns}/{name}/enable-pg-stat-statements` patches `spec.parameters.shared_preload_libraries` and `spec.parameters['pg_stat_statements.…']` for one cluster, states the rolling-restart cost before applying, and reports progress until every instance is `Ready`. Gated behind R12.8's `--allow-actions` and generalises that gate from 'terminate session' to 'the small set of mutations this product performs'." (`live-probe.md` §1 Finding 2, §9 P2) | 2 |
| **R9.11** | "`pgsi_up{namespace,cluster}` — 1 when that cluster's collector round completed without error in the current interval, 0 otherwise. Required by §8 check 3 and Phase 5's acceptance, which currently reference a metric no requirement defines." | 2 |
| **R11.12** | "`values-default.yaml` (PVC-backed store, `--auth=none`, ServiceMonitor on) is the chart's **shipped default**, so `helm install pgsi ./chart` with no `-f` yields a working, scrapeable install; `values-production.yaml` and `values-lab.yaml` are the two documented overrides." | 0 |
| **R11.13** | "`deploy/plain-yaml/all.yaml` is generated in CI by `helm template chart/pgsi -f chart/values-lab.yaml` and committed; `deploy/kustomize/` references it. Only `chart/` is hand-maintained." | 0 |
| **R11.14** | "Phase 0's image acceptance is **deploy-and-observe**, not build-only: the multi-arch image is deployed to the reference cluster and reaches `Running` on an amd64 node with `/healthz` answering." (`live-probe.md` §7, §9 P7) | 0 |
| **R13.11** | "**Clock contract.** Every stored sample carries both the collector's wall-clock timestamp and the source's own timestamp where one exists (`pg_stat_database.stats_reset`, `pg_stat_statements_info.stats_reset`, `cnpg_collector_last_collection_timestamp`); a source-vs-collector skew > 30 s is surfaced as a collector warning rather than silently averaged." | 2 |
| **R16.6** | "**Ground-truth fixture.** A seeded Postgres with known values — N inserts in a known transaction count (assert TPS == N), one query of a known duration (assert `mean_exec_time` within tolerance), a known dead-tuple count (assert the R7.2 bloat ratio), a known two-session lock pair (assert R4.4's chain) — asserted by R16.2's integration test. The only requirement that catches a wrong number rather than a crash." | 2 |
| **R16.7** | "**UI tests.** Component tests (Vitest + Testing Library) for every R10.3 page across its normal/empty/loading/partial/unavailable states; a Playwright smoke test loading `/`, `/fleet` and one cluster page against a running container; a visual-regression snapshot of the R10.5 load graph and R10.6 table." | 5 |
| **R16.8** | "R16.3's fixture set is the committed `testdata/cnpg-metrics/` (4 captured expositions: three instance-manager endpoints plus the pooler's 80-family capture) and `testdata/pg-status/` (PodStatus JSON per pod), named by path in the requirement so the fixture is addressable rather than described." | 2 |
| **R1.10** | "**Per-port transport table as data.** R1.7's scheme/auth selection reads a table, not a guess: 5432 postgres wire (password/TLS), 9187 HTTP (HTTPS only when `spec.monitoring.tls.enabled`), 8000 **always HTTPS** (no client cert for `/pg/status`), 8010 pod-local. A collector that probes `http://pod:8000` reports a live endpoint as down." (`live-probe.md` §4 Finding 6, §9 P3) | 1 |
| **R1.11** | "`pods/proxy` availability depends on the API server's route to the pod CIDR, so it is reliable when `pgsi` runs in-cluster and unreliable when the API endpoint is tunnelled (measured: 502 from a tunnelled laptop, working via `exec` and `port-forward`). R1.7's direct-first, proxy-fallback ordering is correct as written and this is its evidence." (`live-probe.md` §5 Finding 8, §9 P4) | 1 |

---

## Cloud SQL capabilities on their checklist but absent from our parity matrix

Line-by-line diff of `cloud-sql-insights.md` §8 (19 rows) against `requirements.md` §6 (27 rows).
Fourteen of their rows map cleanly to ours (mostly two-to-one, since our §6 splits several of their
rows). These do not:

| Their §8 row | Status in our §6 | What to add |
|---|---|---|
| **End-to-end trace** — sqlcommenter `traceparent` + Cloud Trace; their §7 adds "enabling is a hard prerequisite for the plans/trace views" | **Absent entirely.** No §6 row, no requirement, not in §1.3's non-goals either. | Add a §6 row and either scope it (`sqlcommenter` comment parsing, linking to Tempo/Jaeger) or add it to §1.3 with a reason. |
| **HA / replication** — `postgresql/replication/replica_byte_lag` with a `replica_lag_type` enum, `external_sync/*`, `replication/state`, `available_for_failover` | **Absent as a §6 row**, despite R5.2 listing replication metrics and R10.5 promising a topology view. | Add a §6 row: "HA / replication | `postgresql/replication/*` | S4 `replicationInfo[]`/`replicationSlotsInfo[]` + S2 `pg_stat_replication` + CNPG roles | R5.2, R10.5 | 2". |
| **Retention** — 7 d (EE) / 30 d (EEP) query metrics; 30 d system metrics | **Absent as a §6 row.** §5 covers it and §6's "Edition tiers" row mentions tiers, but the capability itself has no row. | Add a §6 row: "Retention | 7 d EE / 30 d EEP | `retention.*` knobs, 7 d default | §3.4, R3.14 | 2". |
| **AI / anomaly** — "24 h detection window, warning icons, Gemini investigations" | **Half covered.** §1.3 defers the Gemini investigation pane; the *anomaly-detection* half (24 h baseline + warning icons) is not deferred anywhere and has no requirement. | Either fold it into §1.3's deferral row, or add a requirement for a per-`queryid` baseline + anomaly flag (their §8 last row: "baseline + anomaly detection per `queryid`"). |
| **Load attribution (CPU/IO/lock)** — `insights/aggregate/{execution_time,io_time,lock_time}` | **Folded into the query-list row**, which is exactly where B2's contradiction hides. | Give it its own §6 row so the per-query vs aggregate split is visible in the matrix rather than only in §5. |
| **Normalization** — literals → `?` (PG ≤10) / `$1,$2` (PG 11+) | **Folded into "Query text + fingerprint"** (R3.4–R3.6), though R3.5 is a distinct requirement. | Give it its own row pointing at R3.5, matching how their §8 lists it separately. |

From their §7 (Related products), which §6 draws on but does not enumerate exhaustively:

| Their §7 item | Status in our §6 |
|---|---|
| **Flag recommender** (`flagRecommenderEnabled`) | Absent. Our §6 Recommenders row lists "bloat, rightsizing, autovacuum, wraparound" — no GUC-recommendation surface. |
| **OOM session cancel** (`oomSessionCancelEnabled`) | Absent. |
| **Performance Capture** (`performanceCaptureConfig` — trigger-based diagnostic capture on high load + transaction kill policy) | Absent. Closest is R3.10's bounded `EXPLAIN`, which is not load-triggered. |
| **Cloud Logging** — `postgres.log` / `pgaudit.log` → `log_entry_count` by severity | Partially. R5.1 has a "Log errors" card; there is no severity breakdown and no pgaudit story. |
| **Cloud Trace** | Absent (same as the §8 end-to-end-trace row above). |
| **Alerting** — per-event `Notification` button, `Annotations` to display alerts on charts, Slack/PagerDuty/email/SMS channels | Absent from the UI. R9.9b ships PrometheusRules; nothing displays firing alerts in the product (see R10.26). |
| **Custom dashboards** — `Customize dashboard`, `Predefined` menu, `Save to dashboard` | Absent. |
| **Recommenders: out-of-disk** and **idle instance** | Both absent. Our §6 Recommenders row omits them while adding bloat and autovacuum, which are *not* Cloud SQL recommenders (see M11). |
| Multi-instance compare, Export, Index advisor, Google vacuum management | Covered (R5.7, R5.6, R7.1, R7.4-adjacent). |

---

## What I checked and found correct

- **R4.1's 13 columns** match `cloud-sql-insights.md` §3.10's Active-queries table exactly, column for column.
- **R5.1's five summary cards** match §4.1's set exactly (CPU P99|P50, Peak connections, TXID utilization, Disk utilization, Log errors).
- **R3.5's normalization rules** match §3.5 (`?` for PG ≤10, `$1,$2` for PG 11+), including the version boundary.
- **R3.6's default of 1024** and **R3.10's default of 5 plans/min** match §3.2's `InsightsConfig` (`queryStringLength` default 1024, `queryPlansPerMinute` "Default is 5") — the *defaults* are right even though B1 shows the *range* is the wrong edition's.
- **§5's "top 100 queries/day on Enterprise, top 4 000/minute on Enterprise Plus"** matches §3.9's limits table verbatim.
- **R6.4's pgbouncer attribution** matches §6.3's `local` client-IP rule for ports 3307/6432, and our `SHOW CLIENTS` improvement is a genuine improvement rather than a misreading.
- **R1.2's label selectors** match `cloudnativepg.md` §2.1 exactly, including the deprecated bare `role` fallback that ENVIRONMENT.md's observed pod labels confirm is still emitted.
- **R12.1's Role** is consistent with `cloudnativepg.md` §2.8.6's derived minimal namespaced Role, and R12.2's refusal to request `pods/exec` by default matches §2.8's per-command RBAC.
- **R6.1/R6.3's pgbouncer metric names** match `cloudnativepg.md` §5.4's `cnpg_pgbouncer_{lists,pools,stats}_*` set — and `live-probe.md` §2 measured **80 families** on `rev-proxy-pg-pooler:9127` (22 775 bytes), which both confirms R6.1's "zero existing dashboards and zero alert rules" claim and makes it the strongest unconsumed-data argument in the product.
- **R9.9b's claim that CNPG's own `cnpg-default-alerts` has seven rules, all `warning`, no `critical`** is confirmed verbatim by `cloudnativepg.md` §6.4's table ("Severity ceiling: all 7 are `warning`. There is no `critical` rule anywhere in the repo").
- **R2.4's five enumerated renames** all check out against `cloudnativepg.md` §7.0–7.1: `total_time`→`total_exec_time` at PG 13 (ext 1.8), `pg_stat_bgwriter`→`pg_stat_checkpointer` at PG 17, `blk_read_time`/`blk_write_time` removed from `pg_stat_statements` at PG 17 in favour of `shared_blk_*_time`, `op_bytes` removed from `pg_stat_io` at PG 18, `pg_stat_wal` shrinking 9→5 at PG 18. `live-probe.md` §3 proves the PG 16/17 split from the live exposition diff.
- **The S1 → S2 → S3 spine** matches ENVIRONMENT.md's "single most important environment finding", and `live-probe.md` §2 Finding 3 confirms all four endpoints answer with `spec.monitoring` unset on every cluster.
- **R2.8's `application_name` self-exclusion** is confirmed as load-bearing by `live-probe.md` §8 (the exporter's own session is visible in the exposition; without the filter every `backends` count is inflated by one).
- **§1.4's constraint→requirement mapping** is complete — every verbatim brief constraint has a named requirement, and the "some features won't make sense to fall back that way" split is answered concretely by R9.1's two-plane rule rather than by prose.
- **The phase order is right for the wrong-stated reason.** Phases 1–2 and 4–5 (discovery, aggregate metrics, ServiceMonitor, Fleet) work from S1 + S3 alone, which `live-probe.md` Finding 3 confirms — so the backend-first ordering survives the `pg_stat_statements` absence. The spec's stated reason ("that is the part the brief calls out as non-negotiable") is weaker than the real one (it's the part that works on a stock CNPG install), and B4's fix makes the real reason explicit.
