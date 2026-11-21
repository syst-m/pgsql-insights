# Round 1 review — architecture & feasibility (reviewer A3, opus)

2026-11-20 · reviewed: `requirements.md` `0.1.0-draft` (644 l) · ground truth: `docs/research/cloudnativepg.md` (2475 l, read in full), `docs/research/cloud-sql-insights.md` (949 l, read in full), `docs/research/live-probe.md` (221 l), `docs/ENVIRONMENT.md` (126 l)

## Verdict

Buildable, but not as written — and the parts that need changing are concentrated, fixable in one
revision pass, and mostly arithmetic and scope rather than architecture. The central decision (one
central poller reading S1 `Cluster.status` + S2 direct SQL + S3 `:9187`, no per-instance agent) is
sound and is independently confirmed by the live probe; the nine-phase plan is correctly ordered
behind that spine. The three biggest problems are: **(1)** the spec's supported-version envelope is
wrong on both axes — it claims PG 13–18 and Kubernetes 1.29–1.37 where the research says CNPG 1.30.x
supports PG 15–18 and Kubernetes 1.34–1.36, so R2.4's matrix, R3.3's PG13 branch, R3.5's PG≤10 rule
and R13.9's matrix are all partly unvalidatable, and the reference environment's own k3s 1.37 is a
version CNPG lists as *tested but unsupported*; **(2)** the numbers in §5 don't compute — 22 k series
at a 15 s scrape over 30 d is ~5.7 GB, not the 2.6 GB that R9.10 turns into a chart-README promise and
R13.4 turns into a CI assertion, and the per-query family's series formula contradicts the aggregate
row in the same table; **(3)** the spec's headline capability has no data source on any of the four
reference clusters (`pg_stat_statements` measured absent, `shared_preload_libraries` empty on all
three reachable ones), so R3.1's "here's the SQL string" remediation understates a `spec.parameters`
patch plus a rolling restart — the one real conflict with the brief's "rapidly rollable out", which
§1.4 currently papers over. Below: 7 blocking, 10 major, 8 minor, and 6 proposed new requirement IDs.

## Findings

### BLOCKING

**B1 — The supported-version envelope contradicts the research on both axes** (header table, §4 R2.4, R3.3, R3.5, R13.9)

The header says "Applies to CloudNativePG 1.30.x on Kubernetes, **PostgreSQL 13–18**" and R2.4
promises a "PG **13–18** capability matrix" with a `total_time`→`total_exec_time` rename "**at
PG13**". `cloudnativepg.md` §1.0 states **Supported Postgres (1.30.x and main) = 15 – 18**, with
PG 14 unsupported from 2026-11-12. §7's own preamble concedes the reason PG 13/14 are in the matrix
("existing clusters and the postgres_exporter collector set still cover them") — that's a reason to
*document* them, not a reason to claim the product supports them on the target operator. The
consequences are concrete: R3.3's "On PG13 (no `pg_stat_statements_info`) fall back to detecting
counter decreases per row" is a code path that cannot be validated on CNPG 1.30.x at all, and R3.5's
"`?` for PG ≤10" normalization branch is dead code inside a spec whose floor is 13 (see N3).
**Change:** header → "PostgreSQL 15–18 (matrix documents 13–14 as *documented, unvalidated*)"; R2.4 →
"PG 15–18 capability matrix, with 13–14 rows present but marked untested"; R3.3 → scope the
`pg_stat_statements_info` fallback to "PG13/14 only, unvalidated on CNPG 1.30.x"; R13.9 → see M9.

**B2 — R3.2 promises a Cloud SQL column set that §5's own divergence table says PostgreSQL cannot produce** (§4 R3.2, §5 divergence table, §7 Phase 6 acceptance)

R3.2 is marked **[A]** and says the Cloud SQL column set is "reproduced **exactly**", listing
`Load by IO wait` **and `Load by lock wait`**. §5's divergence table then says, two pages later:
"Per-query `lock_time` with `lock_type` | **Not available per query** | `pg_stat_statements` has no
lock-time column." `cloud-sql-insights.md` §3.7 confirms the Google metric is
`insights/perquery/lock_time` with a `lock_type` label of `lw`/`hw`/`buffer_pin` — a Cloud SQL
construction, not a PostgreSQL column. So R3.2 and §5 contradict each other, and Phase 6's acceptance
criterion ("Queries page table matches the **R3.2 column set exactly**") is unpassable as written.
Note the asymmetry the spec misses: `Load by IO wait` *is* derivable per query (`blk_read_time`/
`blk_write_time` ≤PG16, `shared_blk_read_time`/`shared_blk_write_time` PG17+ — exactly R2.4's third
rename), so only lock wait is missing. **Change:** R3.2 → "the Cloud SQL column set, with `Load by
lock wait` rendered as `n/a (no per-query source, §5)` and sourced at database/user level from
`pg_locks` + `pg_stat_activity`"; and split the [A] test into "columns present as specified" vs
"values sourced per query where §5 says they are".

**B3 — R3.1's remediation is a documentation string; the live probe proves it is a product flow, and §1.4's "no extension install" claim is false** (§1.4, §4 R1.6, R3.1, §9 K2)

§1.4's "rapidly rollable out" row promises "**No `Cluster` patch, no extension install**, no sidecar,
no password hand-out required to get a first useful screen", and K2's mitigation is "R3.1 makes
absence a first-class rendered state with **exact remediation**… document it in the README's first
paragraph". `live-probe.md` §1 Findings 1–2 measured the reality: `pg_stat_statements` is **ABSENT on
all three reachable clusters**, `shared_preload_libraries` is **empty on all three**, and
`CREATE EXTENSION` alone fails with `required extension "pg_stat_statements" is not loaded` — enabling
it means patching `spec.parameters.shared_preload_libraries` and **restarting every instance** of that
cluster. That is a per-cluster `Cluster` patch plus a rolling restart, which is precisely what §1.4
says isn't needed and what §3.1's opening paragraph rejects as failing the rollout bar ("adding
instrumentation means patching every `Cluster` CR, which fails the 'rapidly rollable out' bar"). The
spec asserts the bar is met for J2/J3 when its own research says it isn't. **Change:** adopt
`live-probe.md` §9 P2 — rewrite R3.1 as detect → offer → patch → verify (see proposed **R17**),
generalize R12.8's `--allow-actions` gate from "terminate session" to "the small set of mutations this
product performs", and amend §1.4's row to "no `Cluster` patch required for inventory, metrics,
system insights, active queries or poolers; **one** documented `spec.parameters` patch (reversible,
gated) unlocks Query Insights".

**B4 — §8's fixture set includes a cluster with no reachable Postgres, so three of the ten acceptance checks are unexecutable as phrased** (§8 checks 1/8/9, §7 Phase 3 + Phase 7 acceptance)

§8 asserts all four clusters are testable and writes checks as "on **all 4 clusters**". `live-probe.md`
§6 measured `freeidp`: `freeidp-pg-1`'s postgres container is in **`CrashLoopBackOff`** (`kubectl exec`
→ `unable to upgrade connection: container not found ("postgres")`) and `freeidp-pg-2` is **Pending** —
the CR reports 2 instances, reality is **zero serving**. Consequences: check 8 ("Active-queries API
output equals a direct `pg_stat_activity` dump … on all 4 clusters") has no `pg_stat_activity` to dump
from; check 9 ("The five System Insights cards render for all 4 clusters") has no DB-side source for
that cluster; and Phase 3's acceptance ("Top-N table for **each cluster** matches a manual `SELECT …
FROM pg_stat_statements …`") cannot run there even after B3's patch, because there is no live backend.
§8's own freeidp row already names the right requirements (R1.1, R5.1, R10.12) but never says the
cluster is SQL-unreachable. **Change:** add a "Reachable for SQL?" column to §8's table (yes/yes/**no**/
yes), and re-scope checks 8 and 9 to "all clusters with a running postgres container (3 of 4)" with
freeidp validated through S1/S4 only — which is also the honest test of R10.12's degraded state.

**B5 — §5's storage arithmetic is wrong by ~2.2×, and the per-query series formula contradicts the aggregate row in the same table** (§5, R9.10, R13.4)

Two separate errors in one paragraph.
(a) *The GB number.* 22 k series × (30 d ÷ 15 s = 172 800 samples) × 1.5 B = **5.7 GB**, not the
"~2.6 GB" the spec states. 2.6 GB is what you get at a **30 s** scrape (86 400 samples → 2.85 GB),
which is CNPG's own exporter cadence — not the 15 s the same sentence specifies, and not the 60 s
§3.3's collection model mostly uses. R9.10 makes this number a *chart README promise* and R13.4 makes
it a *CI-checkable assertion*, so the wrong figure is load-bearing in two places. **Change:** state the
interval each family is actually scraped at, recompute, and publish the corrected number (or publish
per-family GB so a reader can scale it).
(b) *The series formula.* The per-query row says "Series per cluster = **N × databases**, N default 50",
i.e. top-N is **per database**; R9.5 says "Export the per-query family for the **top-N only**: default
**N=50**, range 0–1000" with no per-database qualifier, and R3.2 computes "top-N queries **per
cluster/database**". Those read differently, and the env total exposes it: 14 k per-query series across
4 clusters implies ~70 databases per cluster, while the aggregate row for the *whole environment* is
"databases × users … measured **~1.2 k**" — the two rows can only be reconciled by assuming ~5 users per
database, which is stated nowhere. **Change:** state in §5 whether `metrics.topN` is per cluster or per
database (recommend per cluster, matching R9.5's plain reading), give the measured database and user
counts for the reference env, and make the per-query row's arithmetic follow from them.

**B6 — R8.3's PromQL pair cannot be "answered from our own store": §3.4 explicitly keeps raw samples out of the store** (§3.4, §4 R8.3)

R8.3: "When a Prometheus URL is configured, the query is proxied to it; **otherwise it is answered from
our own store**." §3.4's storage table says the opposite: "What does **not** [live in the store]: **Raw
metric samples** — those live in Prometheus; we serve them from our registry, not from SQLite." An
arbitrary PromQL string cannot be evaluated against a SQLite table of statement snapshots and a
MetricHub registry — there is no expression evaluator in scope anywhere in §4, and R8.3's own contract
("returning a **Prometheus-shaped result**", with the `namespace,cluster,pod,instance_role` label set
from R9.3) presumes a TSDB. As written, the endpoint is unimplementable in the lab profile (R11.6's
`values-lab.yaml`, which turns ServiceMonitor off and may have no Prometheus at all) — which is exactly
the profile P5 uses. **Change:** R8.3 → "when a Prometheus URL is configured the query is proxied to
it; otherwise the pair serves a **fixed catalogue of named queries** (the R9.4 aggregate and R9.5
per-query rollups, serialized in Prometheus result form) and returns `error.code=PromQLUnavailable` for
anything else", and say so in the R11.6 lab profile.

**B7 — R2.4's fifth "rename" is not a rename; implementing it literally emits the wrong metric set on one of our two reference clusters** (§4 R2.4, §8 rev-proxy-pg row)

R2.4 lists "`pg_stat_bgwriter`→`pg_stat_checkpointer` at PG17" as a rename. `cloudnativepg.md` §7.4
says something different: `pg_stat_bgwriter` **shrinks 11 → 4 columns** at PG17 (`buffers_clean`,
`maxwritten_clean`, `buffers_alloc`, `stats_reset` remain) and checkpoint stats move to a **new,
separate view** — the old view does not disappear. §4.10 confirms CNPG ships **three** default queries
here (`pg_stat_bgwriter`, `pg_stat_bgwriter_17` "PG17-shaped variant", `pg_stat_checkpointer`), and
`live-probe.md` §5 measured the actual expositions: **7** `cnpg_pg_stat_bgwriter_*` families only-on-16,
**9** `cnpg_pg_stat_checkpointer_*` families only-on-17, **plus** `cnpg_pg_stat_bgwriter_stats_reset_time`
which exists on 17 but not on 16. A collector that implements "rename" — drop bgwriter, add checkpointer
on PG17 — loses four still-valid bgwriter families on our PG17 clusters; one that keeps both emits
families that don't exist. The other four claimed renames check out (see *What I checked*). **Change:**
R2.4 → "`pg_stat_bgwriter` **shrinks 11→4 at PG17 while `pg_stat_checkpointer` (9 cols, 11 at PG18)
appears as a separate view — CNPG emits both**; the matrix must carry per-view column sets per major,
not a rename table".

### MAJOR

**M1 — The `--role=all|collect|serve` split has no storage contract, and is speculative generality in v1** (§3.1 "Roles, one binary", §3.4, §3.5)

Three problems in one paragraph. (i) *Contract:* if `--role=collect` and `--role=serve` are separate
pods, both must reach the same SQLite file — which needs a **ReadWriteMany** StorageClass (RWO cannot
attach to two pods on different nodes), and §3.4 never states an access mode. SQLite's single-writer
WAL model also means the `serve` pod's readers contend with the `collect` pod's writer over NFS-class
storage, where `flock` semantics are the classic failure. (ii) *Naming:* §3.1 defines flags
(`--role=all|collect|serve`) while §3.5 defines subcommands (`cmd/pgsi/ # main; subcommands
serve|collect|api|version`) — an `api` role exists in one and not the other, and `version` exists in
neither flag set. Pick one surface (recommend subcommands, matching `pgsi config dump` in R11.8) and
make the other an alias. (iii) *Scope:* §3.4 already says sharding "is **not supported in v1**", so the
split's only v1 use case — "poll many namespaces from a dedicated poller and serve the API separately" —
is a deployment shape with no supporting scale story. **Change:** v1 ships `--role=all` only; keep
`collect`/`serve` documented as v1.x aliases gated on the Postgres-backed store already named in §3.4,
and add the PVC access-mode contract (proposed **R20**).

**M2 — R2.1 bounds concurrency per cluster but not globally, so R13.2's 50-cluster target fans out unbounded** (§4 R2.1, R13.2, §3.3)

R2.1 gives "per-collector timeouts (default 10 s) and **per-cluster** concurrency limits (default 4)".
R13.2 targets 50 clusters on one poller. Nothing bounds the *global* fan-out: 50 clusters × 13 collectors
from §3.3, all on 15 s/30 s/60 s intervals, means a burst of concurrent SQL connections (up to 200 by
R2.1's own number) plus concurrent HTTP scrapes, with no queue depth, no admission control, and no
load-shedding rule when a round overruns. On a small cluster this is also a Postgres-side problem:
`open-webui-pg` is a 2 Gi cluster whose `max_connections` is typically 100, and our collector is a
multi-connection client on every one of 50 clusters. **Change:** add to R2.1 a global budget — max
concurrent SQL queries, max concurrent HTTP scrapes, queue depth, and a shed policy that marks the
cluster stale rather than queueing indefinitely (proposed **R19**) — and state the per-cluster
connection count that R13.2 assumes.

**M3 — R3.9's "sampled histogram" is not a Prometheus histogram, is systematically biased, and is not counted in §5** (§4 R3.9, §5, §6 parity row)

R3.9 proposes "a **sampled** histogram built from the `activity` collector's per-session durations (R4),
**exposed as a native Prometheus histogram** for the top-N". Three issues. (i) A Prometheus histogram
requires us to maintain bucket counters and expose `_bucket`/`_sum`/`_count`; a set of instantaneous
durations sampled every 15 s is not that unless we bucket them ourselves — say so, and name the buckets.
(ii) Sampling `pg_stat_activity` every 15 s observes only queries *in flight at sample time*, so the
distribution is biased toward long queries and away from the short-but-frequent ones that dominate
`pg_stat_statements` totals — the bias must be stated in the UI, not just in the spec, or the panel
contradicts the R3.2 table next to it. (iii) The histogram family appears nowhere in §5's budget table
(Aggregate/Per-query/Per-tag/System/Pooler/Self), yet 50 top-N queries × ~8 buckets × 7 pods ≈ 2 800
extra series — ~13 % of the claimed 22 k. **Change:** R3.9 → "we maintain our own buckets from the
activity samples, expose them as `pgsi_query_duration_seconds_bucket` for the top-N only, and label the
panel 'sampled, biased toward long queries'"; add the family to §5 (proposed **R21**).

**M4 — R3.10's automatic EXPLAIN fails on two counts: the stored text isn't EXPLAIN-able, and R12.4's role can't EXPLAIN other schemas' tables** (§4 R3.10, R3.5, R3.6, R12.4)

R3.10 says "Capture `EXPLAIN (FORMAT JSON)` for **top-N queries**". But R3.5 stores the *normalized*
template (literals → `$1`,`$2`) and R3.6 truncates to 1 024 bytes by default. `EXPLAIN` on a truncated
string is a syntax error, and `EXPLAIN` on a template with `$1` placeholders only works via
`PREPARE`/`EXPLAIN EXECUTE`, which yields a *generic* plan — not the plan the real execution used, which
undercuts R3.11's plan-change detection (the whole point). The spec must say: EXPLAIN the **first-seen
raw text** (which R3.4 says we store), skip rows whose text is flagged truncated, and restrict to
statements `EXPLAIN` is safe on. Second, harder problem: `EXPLAIN` requires privileges on the referenced
tables. R12.4's preferred identity is a `pg_monitor`-granted role, which grants statistics visibility
but **not** `SELECT` on application tables — so for most top-N queries the EXPLAIN returns
`permission denied for table …`. The `pg_monitor` path (R12.4's preferred) and the `<cluster>-app` path
(R12.4's fallback, which does have table access) give opposite answers. **Change:** R3.10 → add
"requires `SELECT` on the referenced relations; on a `pg_monitor`-only deployment the plan-sample
feature reports `unavailable (insufficient table privileges)` rather than failing per query", and note
that `--allow-analyze-write` and R3.10's SELECT-only restriction are enforced by parsing the stored text.

**M5 — R6.4's pgbouncer attribution is impossible per-query under transaction pooling; scope it to sessions** (§4 R6.4, §6 parity row, §8 check 10)

R6.4 promises to "label **queries** that arrived through a pooler with the pooler name plus the
pgbouncer-side client address". Under `poolMode: transaction` (which is what `rev-proxy-pg-pooler`
uses) a pgbouncer client is not pinned to a server backend, and `pg_stat_statements` rows have **no
session at all** — they are per-(user, database, queryid) aggregates, so there is nothing to attach a
client address to. `cloud-sql-insights.md` §6.3 says exactly this: "with transaction pooling, one server
backend serves many logical sessions, so per-backend attribution of *sessions* breaks even though
per-query attribution via `pg_stat_statements` does not", and recommends the pgbouncer-side address be
attached "as a **separate dimension**" — not joined to queries. R6.4's `cloudsql` mode is Google's
behaviour (render `local`), its `pgbouncer` mode is the improvement, and the spec should say which is
default and why rather than offering three. **Change:** R6.4 → attribution applies to (a) the R4.1
active-session list, where `SHOW CLIENTS` genuinely maps a backend pid to a client address, and (b) the
R3.7 by-client-address rollup, populated from `SHOW CLIENTS` for pooler-served work; **not** to the
R3.2 per-query rows. Rewrite §8 check 10 accordingly (it currently tests the impossible reading).

**M6 — R5.1's "Log errors" card has no source anywhere in S1–S9** (§4 R5.1, §3.2, §8 check 9)

R5.1 is **[A]** and lists five cards, one of which is **Log errors**. Cloud SQL sources that card from
`logging.googleapis.com/log_entry_count` over `postgres.log`/`pgaudit.log` (`cloud-sql-insights.md`
§4.1/§4.2). Our source table (S1–S9) contains no log source: S1 is the CR, S2 SQL views, S3/S4 the
instance manager, S5/S6 pgbouncer, S7 cAdvisor/metrics-server, S8 the plugin, S9 an optional exporter.
So one of five [A]-gated cards has no data path, and §8 check 9 ("the five cards render for all 4
clusters") can't be satisfied. The Kubernetes-native equivalent is reading `pods/log` (which also needs
an RBAC verb R12.1 doesn't request) plus a parser for PostgreSQL's log format — and R12.7's
`readOnlyRootFilesystem` means we can't read `pg_log` off the filesystem. **Change:** either add the
source (proposed **R18**, `--log-source=off|pods-log`, with `pods/log:get,list` added to R12.1) or drop
the card from R5.1 and from §8 check 9 and record the divergence in §5's table like the other four.

**M7 — Fourteen requirement IDs are defined in §4 but scheduled in no phase** (§7 phase table)

Cross-referencing every ID in §4 against the §7 Req IDs columns, these are defined but never assigned to
a phase: **R10.14** (first-paint budget), **R11.8** (config precedence — yet §3.5 maps `internal/config/`
to it and R11.8 is cited in §6's parity row as Phase 1), **R11.10** (uninstall story), **R12.4** (the
Postgres-side privilege model — needed in Phase 2, where SQL collectors first connect, and again in
Phase 3), **R12.5–R12.9** (TLS, log hygiene, container hardening, action gate, threat model),
**R13.2, R13.3, R13.5, R13.7, R13.8, R13.9, R13.10** (the entire non-functional set except R13.1 and
R13.4), **R15.3** (license audit), **R16.2** (integration tests on PG16/17 — the phase that needs them,
Phase 2, lists only R16.1), **R16.5** (`scripts/validate-env.sh`, which §8 says *is* the mechanism that
makes every [A] checkable — and it's in no phase). The last one is the most serious: the validation
script that makes §8 real is unscheduled. **Change:** add an explicit "Req IDs" line per phase covering
these, with R12.4/R16.2/R16.5 in Phase 2 and R11.8/R12.5–R12.9 in Phase 0–1 (they are packaging, not
features).

**M8 — Four §6 parity cells name phases that do not deliver the requirement IDs in the same row** (§6, §7)

| §6 row | Says | §7 actually schedules it in |
|---|---|---|
| Query detail: plan samples (R3.10–R3.11) | `3→6` | Phase 3 lists R3.1–R3.8 + R3.14; Phase 6 lists R3.9–R3.13; **Phase 8** lists R3.10–R3.11. Three cells, three answers. |
| Client addresses tab (R3.13, R6.4) | `3, 8` | R3.13 is Phase **6**, not 3. |
| Databases tab (R3.7, R5.2) | `2` | R3.7 is Phase **3**, R5.2 is Phase **7**. Phase 2 schedules neither. |
| `InsightsConfig` equivalents (R3.6, R3.10, R3.13, R11.8) | `1` | R3.6 → 3, R3.10 → 8, R3.13 → 6, R11.8 → unscheduled (M7). |

The parity matrix is the document's own contract for "did we build what we said", so a row whose phase
column contradicts §7 makes the contract unfalsifiable. **Change:** regenerate §6's Phase column
mechanically from §7's Req IDs columns, and resolve R3.10–R3.11 to exactly one phase (recommend Phase 6,
where R3.9–R3.13 already live, and remove them from Phase 8's list).

**M9 — R13.9's compatibility matrix claims ranges the research contradicts at both ends** (§4 R13.9, §8 open-webui row)

R13.9: "CNPG **1.28–1.30**, PG **13–18**, Kubernetes **1.29–1.37**, kube-prometheus-stack 0.7x+".
`cloudnativepg.md` §1.0: **Supported Kubernetes = 1.34, 1.35, 1.36**, with **1.37 explicitly "tested but
unsupported"** (as are 1.33, 1.32, 1.31), and Supported Postgres 15–18 (B1). So the claimed range
includes four Kubernetes versions CNPG 1.30.x does not support, and the reference environment's own
**k3s v1.37.2+k3s1** is on the unsupported list. That matters for R13.9's promise that "each cell is
either tested, fixture-tested, or explicitly marked untested — no silent claims": a matrix that claims
1.29 and 1.37 as supported *is* a silent claim. **Change:** R13.9 → "Kubernetes 1.34–1.36 supported;
1.37 tested-but-unsupported upstream (the reference environment runs k3s v1.37.2+k3s1 — our behaviour
there is validated empirically, not by CNPG's support matrix)"; CNPG range → 1.29–1.30 (1.28 predates
the CVE-2026-44477 exporter-role model R12.4 depends on, so say whether 1.28 is in or out).

**M10 — R9.5 labels the per-query family with `queryid`, the exact identifier R3.4 says is unstable** (§4 R9.5, R3.4, §9 K6)

R3.4's stated reason for computing our own fingerprint is "(a) **PG18 changes query-id jumbling**, so ids
change for unchanged queries across a 17→18 upgrade" (confirmed by `cloudnativepg.md` §7.9: constant
lists collapse to first/last, same-named relations across schemas merge). R8.2's endpoint is
`/insights/queries/{fingerprint}`. But R9.5 says the metric family carries "`queryid` as the label and
the text served from the API" — so the Prometheus side keys on the unstable id while the API side keys
on the stable fingerprint, and a 17→18 upgrade silently forks every user's saved PromQL and Grafana
alerts. K6 lists this risk and its mitigation says "R3.4's own fingerprint survives id changes" — which
is only true if the metric label *is* the fingerprint. **Change:** R9.5 → labels
`fingerprint` (canonical, joins to the API) + `queryid` (informational, may change across majors), and
document the join in R9.3's contract.

### MINOR

**N1 — §8 check 3 uses `pgsi_up`, which R9.8 does not define** (§8 check 3, §4 R9.8). R9.8's set is
`pgsi_build_info`, `pgsi_collector_duration_seconds`, `pgsi_collector_errors_total`,
`pgsi_scrape_target_up{cluster,pod}`, `pgsi_store_size_bytes`, `pgsi_http_request_duration_seconds`.
Either add `pgsi_up{namespace,cluster,pod,instance_role}` to R9.8 (proposed **R22**) or change check 3 to
`pgsi_scrape_target_up{cluster="cookbook-pg"} == 1`.

**N2 — `R9.9b` is the only letter-suffixed ID in the document** (§4 R9). R9 has 9.1–9.10 plus 9.9b; the
b-suffix pattern is used nowhere else (R15/R16 were given fresh numbers). Renumber to **R9.11** and shift
nothing else, so §7's "R9.8–R9.10" reference stays valid.

**N3 — R3.5's PG≤10 normalization branch is dead code inside a PG13–18 spec** (§4 R3.5). Cloud SQL's rule
(`?` for ≤PG10, `$1`,`$2` for 11+) is copied wholesale; with B1's envelope the ≤10 branch can never
execute. Keep it as a documented note, not a requirement, so R16.1's fixture corpus doesn't need PG10
samples.

**N4 — R3.12's comment convention covers 3 of the 6 tag keys it claims to support** (§4 R3.12). The metric
list is `action, application, controller, db_driver, framework, route, tag_hash`, but the documented
convention is `/* pgsio:action=…,route=…,controller=… */` — no `framework`, no `db_driver`. Cloud SQL's
accepted key set (`cloud-sql-insights.md` §3.8) includes `framework` and `db driver` (spaced key,
`db_driver` label). Add `framework=` and `dbdriver=` to the comment grammar or drop the two columns from
the R3.7/§6 Tags tab.

**N5 — R12.5 derives `sslmode` from the wrong secret** (§4 R12.5). "default `require` when the cluster has
a `-server` secret we can read" — `-server` holds the server cert *and key*; a client verifies with
`<cluster>-ca` (which ENVIRONMENT.md lists and R12.1 grants). Use `verify-ca` with `<cluster>-ca`, and
note in R12.9's threat model that reading `-server` hands us the server private key.

**N6 — R4.1's `Action` column is empty by default** (§4 R4.1, R4.5, R12.8). The column exists to host
`pg_terminate_backend`, which is gated behind `--allow-actions=false`. The [A] test ("matches the exact
Cloud SQL columns") will pass with a dead column in the default config; say the column renders disabled
and links to the gate.

**N7 — R10.2 is not verifiable as written** (§4 R10.2). "Chart library and design system are chosen by the
`dataviz` skill" has no acceptance condition and no fallback if the skill is unavailable (R11.11's CI
can't assert it). Add: "if the skill is unavailable, the fallback is <named library>, recorded in
`IMPLEMENTATION.md`" — which also answers R14.1's decisions-log requirement for free.

**N8 — §11's log is already inconsistent with its own footer** (§11). The footer says R15/R16 "were
already added from an internal consistency pass", but the log table has rows only for A3 and A4 and no
row for that pass; §7's table still lists A3 and A4 as "*to spawn for review*" though both are spawned.
Add the internal pass as a row and update the Spawned column — §11 is the document that proves the review
process happened.

## Findings that require a new requirement ID

1. **R17 — Extension enablement as a product action** (from B3; **Phase 3**). "Given a cluster where
   `pg_stat_statements` is absent, `pgsi` can, behind the generalized action gate, patch
   `spec.parameters.shared_preload_libraries` and `CREATE EXTENSION pg_stat_statements`, then verify
   presence within one collection round. The UI states the rolling-restart cost before offering it, the
   patch is recorded in `IMPLEMENTATION.md`'s decisions log, and reverting it is documented. Default
   off; `--enable-pg-stat-statements=<cluster|list|off>`." Generalizes R12.8's gate from
   `pg_terminate_backend` to "the mutations this product performs", per `live-probe.md` §9 P2.
2. **R18 — Log-source ingestion for the Log errors card** (from M6; **Phase 7**). "`--log-source=off|
   pods-log`. When `pods-log`, read `pods/log` for instance pods (add `pods/log: [get, list]` to R12.1),
   parse PostgreSQL log severity, and serve the count feeding R5.1's Log errors card. `off` renders the
   card as `n/a (no log source)` rather than 0." Default `off` in the lab profile.
3. **R19 — Global scheduler budget** (from M2; **Phase 2**). "Max concurrent SQL queries, max concurrent
   HTTP scrapes, and scheduler queue depth are configurable and enforced globally, not only per cluster;
   when the queue exceeds depth, the oldest pending collection is shed and the cluster is marked stale
   (R1.9) rather than queued. Expose `pgsi_scheduler_queue_depth` and `pgsi_scheduler_shed_total`."
4. **R20 — Storage access-mode contract** (from M1; **Phase 0**). "The chart declares the PVC access mode
   it requires (`ReadWriteOnce` for `--role=all`), documents that any role split needs `ReadWriteMany`,
   and `helm template` fails when `role != all` with an accessMode of `ReadWriteOnce`."
5. **R21 — Histogram series accounting** (from M3; **Phase 6**). "Every exported metric family, including
   the R3.9 sampled-duration histogram, is enumerated in the §5 budget table with its per-cluster series
   count, and R13.4's CI assertion covers all families rather than the aggregate + per-query pair."
6. **R22 — `pgsi_up`** (from N1; **Phase 2**). "`pgsi_up{namespace,cluster,pod,instance_role}` = 1 when
   the most recent collection round for that target succeeded, 0 otherwise; distinct from
   `pgsi_scrape_target_up`, which reports the HTTP scrape. §8 check 3 tests this metric."

## What I checked and found correct

- **All five R2.4 column renames vs `cloudnativepg.md` §7.1/§7.4: 4 correct, 1 mis-framed** (B7).
  `total_time`→`total_exec_time` at PG13 ✓ (§7.1, ext 1.8, also removes `query_plan`, adds
  `total_plan_time`); `blk_read_time`/`blk_write_time` removed from `pg_stat_statements` at PG17 in
  favour of `shared_blk_read_time`/`shared_blk_write_time` ✓ (exact match to §7.1's row); `op_bytes`
  removed from `pg_stat_io` at PG18 ✓ (§7.4, PG18 adds `read_bytes`/`write_bytes`/`extend_bytes`);
  `pg_stat_wal` 9→5 at PG18 ✓ (§7.4 counts PG14–17 = 9, PG18 = 5). The bgwriter→checkpointer entry is
  the wrong one.
- **§8's PG16-vs-PG17 claim is empirically confirmed**: `live-probe.md` §5's diff (7 bgwriter families
  only-on-16, 9 checkpointer families only-on-17) matches §7.4's column sets and validates the *reason*
  R2.4 exists — my objection is to the word "rename", not to the matrix's value.
- **S3's always-up claim** (S3 row, §3.1, K3's "Phase 2 works credential-free") ✓ — `cloudnativepg.md`
  §4.1 (exporter compiled in, not a sidecar) plus `live-probe.md` §2's 107/115/118 families with
  `spec.monitoring` unset. The spec correctly keys HTTPS on `spec.monitoring.tls.enabled` (§4.2/§4.9),
  not on ENVIRONMENT.md's non-existent `spec.metrics` — that wording error is ENVIRONMENT.md's, already
  corrected in the probe commit.
- **S4's auth model** ✓ — `/pg/status` unauthenticated on HTTPS-only 8000 (§3.3/§3.4, confirmed by
  `live-probe.md` §4). R1.7's direct-first/proxy-fallback ordering is confirmed correct by §5's 502
  finding; the spec should add the pod-CIDR caveat (`live-probe.md` §8).
- **R9.9b's claim about CNPG's alerts** ✓ — §6.4: one group `cnp-default.rules`, exactly 7 rules, all
  `severity: warning`, all `for: 1m`, "no `critical` rule anywhere in the repo".
- **R2.5's "matching the CNPG 1.29.1 hardening"** ✓ — §1.8's v1.29.1 entry (CVE-2026-44477) explicitly
  includes "Schema-qualified catalog references in default monitoring queries"; R12.4's
  `cnpg_metrics_exporter`/`pg_monitor` attribution is correct (§4.7).
- **R1.2's label contract** ✓ — `cnpg.io/cluster`, `cnpg.io/podRole`, `cnpg.io/instanceRole` with the
  deprecated `role` fallback matches ENVIRONMENT.md's observed labels exactly; R1.3's
  `app.kubernetes.io/version` shortcut matches the observed `17` label.
- **R4.3's `pg_wait_events` PG17+ gating** ✓ (§7.7), **R4.6's PG18 `starting` state** ✓ (§7.2),
  **R3.3's `pg_stat_statements_info` PG14+** ✓ (§7.1), **R5.4's 500-database / 50-tuple_size
  thresholds** ✓ (`cloud-sql-insights.md` §3.9), **R3.6's 256–4500 / default 1024 range** ✓ (§3.2),
  **R3.10's 5 plans/min default** ✓ (§2 editions matrix: Enterprise default 5, max 20; E+ 200),
  **R4.1's top-50 + 12 columns** ✓ (§3.10), **R4.4's 3-level blocking tree** ✓ (§3.10), **R6.1's
  `cnpg_pgbouncer_pools_*` field list** ✓ (§5.4), **R11.2's pure-Go SQLite → distroless-static** ✓
  (`modernc.org/sqlite`, no cgo, consistent with R13.10).
- **§3.3's collector list vs §7's view inventory**: every view named in §3.3 exists in the target range
  except `pg_stat_io` (PG16+ only — correctly version-gated in the `io` row) and `pg_stat_wal`
  (PG14+ only — correctly gated in the `wal` row). One gap worth noting: neither CNPG's exporter nor
  postgres_exporter collects `pg_stat_io` (§7.4 note, §4.12), so the `io` collector's S3 path yields
  nothing there and the S2 path is the only one — worth one sentence in S3's row.
- **§1.4's constraint→requirement mapping**: 9 of 10 rows hold as written; the "rapidly rollable out"
  row is the one falsified case (B3).
