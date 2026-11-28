# Cardinality and retention budget

This is the section that decides whether the product is adoptable. Cloud SQL bounds it explicitly
(top 100 queries/day on Enterprise, top 4 000/minute on Enterprise Plus, 4 500-byte query text cap
on Enterprise / 100 KB on Enterprise Plus, ~36 GB at 7 d / ~155 GB at 30 d on instance disk) and so
must we.

**Scrape interval is declared per family.** Our `/metrics` is scraped at **60 s**, matching the
collection interval — a faster scrape of an endpoint that refreshes every 60 s samples the same
value repeatedly and costs storage for nothing. The 15 s `activity` collector (R4.2) feeds the API
and store, not `/metrics` (R9.1), so it does not change this number.

| Family | Labels | Series per cluster | Interval | Control |
|---|---|---|---|---|
| Aggregate (R9.4) | `namespace,cluster,pod,instance_role,datname,usename` | ~`databases × users`, capped by the R5.4 500-database guard; measured **~1.2 k** for the reference env | 60 s | Always on |
| Per-query (R9.5) | `…,fingerprint,queryid` | **N per cluster × 4 metric series** = 50 × 4 = **200** (was 6 × 50 = **300**, a pre-ruling figure: the multiplier came from R9.4's aggregate member list, and A3's S25 closed R9.5's list at four — execution_time, rows, shared_blocks_hit, shared_blocks_read — so the Phase 3 acceptance criterion "series count within the §5 budget" evaluates against 200) | 60 s | `metrics.topN` 0–1000, **per cluster**; N=0 disables |
| Sampled latency histogram (R9.13) | `…,fingerprint,le` | 50 × (8 buckets + `_sum` + `_count`) = **450** | 60 s | Ships with R3.9 |
| Per-tag (R9.6) | `…,tag_hash` | tags × databases | 60 s | Off unless R3.12 enabled |
| System (R9.7) | `namespace,cluster,pod,instance_role` + family-specific | ~**4 k** | 60 s | Always on |
| Pooler (R6.1) | `…,pooler,database,user` | ~**200** where a Pooler exists | 60 s | Only where a Pooler exists |
| Self (R9.8) | — | ~**120** | 60 s | Always on |

**Reference-environment estimate.** Per cluster ≈ 1 200 + 200 + 450 + 4 000 + 120 = **5 970**; four
clusters ≈ **24.08 k series**, of which one cluster contributes ~200 pooler series. At ~1.5
bytes/sample after compression and a **60 s** scrape (43 200 samples per series over 30 d):

> **24 080 × 43 200 × 1.5 B ≈ 1.56 GB of Prometheus storage over 30 days.**

This is the number R9.10 makes the chart README promise. *(0.1.0 stated "~2.6 GB" for "~22 k
series at a 15 s scrape". Both halves were wrong: 22 k × 172 800 × 1.5 B = **5.7 GB**, and 2.6 GB is
the 30 s figure, not the 15 s one. The per-query row also said "N × databases" while R9.5 defines
`topN` per cluster — 14 k per-query series across 4 clusters implies ~70 databases each, which the
~1.2 k aggregate row cannot support. Corrected here and in R13.4.)*

**Deliberate divergences from Cloud SQL's limits, and why:**

| Cloud SQL | Us | Why |
|---|---|---|
| 4 500-byte query text (Enterprise) / 100 KB (Enterprise Plus) | 1 024 default; range 256–100 000; 10 000 documented as the E+ default | Matches Enterprise's default exactly and gives the Enterprise-Plus ceiling as a knob; 4 500 B × 4 000 queries is the cost Google absorbs on instance disk, and we ship it as opt-in rather than as the default |
| 200 plan samples/min (E+) | 5/min default, knob up to 200 | `EXPLAIN` costs planner CPU on the real database; Google's 200/min is for EEP instances that pay for it. Default = Enterprise's 5, ceiling = EEP's 200 |
| 30-day retention (E+) / 7 d (Enterprise) | 7 d default, 30 d available | 7 d is Enterprise's number and fits a 1 Gi PVC; 30 d is one config change |
| Per-query latency DISTRIBUTION | min/mean/max/stddev + sampled histogram | PostgreSQL has no per-query latency distribution; R3.9 |
| Per-query `lock_time` with `lock_type` | `n/a` per query; available at database/user level | `pg_stat_statements` has no lock-time column. We derive lock wait at database/user level from `pg_locks` + `pg_stat_activity` and label it as such rather than faking a per-query number |
| End-to-end application trace (sqlcommenter + Cloud Trace) | Not in v0.1 | Google's trace integration needs Cloud Trace. The R3.12 comment convention is the generic-Postgres seed; a real trace exporter is post-0.1 |
| AI/anomaly **detection** half (24 h baseline + warning icons) | Deferred with the AI pane | Only the Gemini half is deferred by §1.3; the 24 h baseline + anomaly icons are a separate feature and are not in v0.1 |

---

