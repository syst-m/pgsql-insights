# Validation plan against the reference environment

The four clusters in `docs/ENVIRONMENT.md` are chosen well enough to be the test fixture; each
exercises a specific code path. **The "Reachable for SQL?" column is new** and fixes the 0.1.0
defect where checks were phrased "all 4 clusters" while one cluster has zero serving instances
(A3-B4): `freeidp-pg-1`'s postgres container is in `CrashLoopBackOff` and `-2` is `Pending`, so
there is no Postgres to query there at all.

| Cluster | What it exercises | Reachable for SQL? | Requirements proven |
|---|---|---|---|
| `ai-api-gateway/rev-proxy-pg` (2 inst, PG **17.11**, **Pooler**) | Normal HA path + pgbouncer attribution + pooler metrics + PG17 column sets (`shared_blk_read_time`, `pg_stat_checkpointer`) + `lastFailedWAL` anomaly on a "healthy" CR | **yes** | R1.2, R1.10, R3.5, R6.1–R6.5, R2.4, R5.5 |
| `cookbook/cookbook-pg` (1 inst, PG **17.11**) | Degenerate single-instance path: empty replication set, no replicas | **yes** | R5.5, R9.3 |
| `freeidp/freeidp-pg` (2 inst, PG 17, **disk pressure + Pending replica + crash-looping primary**) | Degraded rendering, disk-pressure metrics, a non-serving instance, `phaseReason` surfacing, R1.2's container-status rule | **no** (zero serving instances) | R1.1, R1.2, R5.1, R10.12, R10.28 |
| `open-webui/open-webui-pg` (2 inst, **PG 16.11**, 2 Gi) | The PG16 branch (`blk_read_time` present, `pg_stat_checkpointer` absent, `op_bytes` present) and a small-storage cluster | **yes** | R2.4, R13.9 |

**Acceptance checks** (the `[A]` requirements, scripted as `scripts/validate-env.sh`, R16.5). Checks
8 and 9 run against **the 3 clusters with a serving postgres**; `freeidp` is validated through S1
and S4 only:

1. `GET /api/v1/clusters` returns exactly the 4 clusters with correct phase/instances/**servingInstances**/primary/major. *(R1.5, Phase 1a)*
2. The `freeidp-pg` entry reports the disk-pressure `phaseReason`, the Pending instance, **and `servingInstances: 0`**. *(R1.2, Phase 1a)*
3. `pgsi_up{cluster="cookbook-pg"} == 1` and its replication section renders "single instance". *(R9.12, Phase 2 — the API's replication section; the UI that renders it, R5.5, lands at 7)*
4. Version-matrix unit tests cover PG 13–18 (13/14 marked `impossible` per R13.9); live collection succeeds on PG 16 **and** PG 17. *(R2.4, Phase 2)*
5. `promtool check metrics < our /metrics` exits 0; `promtool check rules chart/templates/prometheusrule.yaml` exits 0. *(R9.9, R9.11, Phase 4)*
6. The **existing** `monitoring/prom-operator` Prometheus, with no config change beyond applying our ServiceMonitor, returns series for all 4 clusters. *(R9.9, Phase 4)*
7. `helm template` golden tests pass for all three profiles; `helm install` from a packaged tarball into a fresh namespace serves the UI in < 5 min. *(R11.6, R11.12, R11.13, Phase 5)*
8. Active-queries API output equals a direct `pg_stat_activity` dump (modulo our own session, R2.8) on **the 3 SQL-reachable clusters**, with value equality per R4.8. *(R4.1, Phase 6a)*
9. The five System Insights cards render for **the 3 SQL-reachable clusters**, each value matched to its source query within tolerance and both printed. *(R5.1, Phase 7)*
10. For `rev-proxy-pg-pooler`, the pgbouncer-sourced client address in the **active-session list** differs from the Postgres-side `client_addr`, proving attribution works on the surface where it is derivable (R6.4's scoping). *(R6.4, Phase 8)*

---

