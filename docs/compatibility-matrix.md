# Compatibility matrix (R13.9)

R13.9 requires the compatibility claims to be **an artifact with per-cell states**, not
prose: `tested | fixture | impossible | untested`. 0.1.0 over-claimed by collapsing two
axes into one sentence; this file keeps them separate — **PostgreSQL** and
**Kubernetes** — and adds the operator/monitoring versions the spec names.

| Cell state | Meaning |
|---|---|
| `tested` | We ran against a real instance of that version and observed the result the claim rests on. |
| `fixture` | We have captured output from that version (or its documented schema) and test against the capture, not against a live instance. |
| `impossible` | The combination cannot exist in our target stack — the claim is closed, not pending. |
| `untested` | The combination is supported in principle and we have not observed it. No claim beyond "supported in principle". |

**Phase 0 status of this file.** The binary does not query Postgres yet (that is
Phase 2), so every `tested` cell below is backed by the **live probe**
(`docs/research/live-probe.md` + `testdata/`), not by `pgsi` itself. The column
`evidence now` names the artifact; `promoted by` names the run that replaces it with
product evidence. A cell is never promoted by editing this file.

---

## Axis 1 — PostgreSQL

CNPG 1.30 supports PG 15–18. PG 13 and 14 are closed rows, not gaps in our testing.

| PostgreSQL | Cell | Evidence now | Promoted by |
|---|---|---|---|
| **13** | `impossible` | CNPG 1.30 cannot run PG 13, so no `pgsi` install can ever target it. R3.3's PG13 fallback (no `pg_stat_statements_info`) is therefore untestable on the target stack and is documented as such. | Nothing — closed. R2.4's SQL-layer rows for 13 exist as fixture-tested rows only. |
| **14** | `impossible` | CNPG 1.30 dropped PG 14 (PG 14 EOL 2026-11-12). Documented as fixture-tested SQL-layer rows only (R2.4: `total_time` → `total_exec_time` landed at PG 13). | Nothing — closed. |
| **15** | `untested` | No PG 15 instance exists in the reference environment; no fixture captured. | Phase 2 run against a PG 15 cluster (kind-hosted CNPG per R16.2, or a new reference cluster), plus the R2.4 matrix unit tests. |
| **16** | `tested` (16.11) | `open-webui/open-webui-pg` runs `postgresql:16` → 16.11. Live probe captured `pg_stat_*` and the instance-manager surface: `testdata/pg-status/open-webui__open-webui-pg-2__8000.json`, `testdata/cnpg-metrics/open-webui__open-webui-pg-2__9187.txt`. The PG16 branch (`blk_read_time` present, `pg_stat_checkpointer` absent, `op_bytes` present) was measured, not inferred. | Phase 2: `pgsi collect` against the same instance, with the captured fixture as the golden file. |
| **17** | `tested` (17.11) | `ai-api-gateway/rev-proxy-pg` and `cookbook/cookbook-pg` run `postgresql:17` → 17.11; `freeidp/freeidp-pg` is PG 17 as well. Captured: `testdata/pg-status/ai-api-gateway__rev-proxy-pg-2__8000.json`, `testdata/pg-status/cookbook__cookbook-pg-1__8000.json`, `testdata/cnpg-metrics/*__9187.txt`. The R2.4 bgwriter/checkpointer split (`pg_stat_bgwriter` 11→4 columns, stats moving to `pg_stat_checkpointer`) is measured on these instances. | Phase 2: same, plus the `pg_stat_statements` `shared_blk_*` rename check. |
| **18** | `untested` | Supported by CNPG 1.30; no PG 18 instance in the reference environment. R2.4's PG18 rows (`op_bytes` removed from `pg_stat_io`, `pg_stat_wal` 9→5 columns) are encoded from the CNPG/PG documentation, not observed. | Phase 2/3 run against a PG 18 cluster; until then the SQL is written from the matrix and not claimed. |

Reference-environment coverage note: the four clusters give us **one PG 16 instance and
three PG 17 instances**, so the PG 16 cell rests on a single instance. That is stated
rather than averaged away.

## Axis 2 — Kubernetes

CNPG 1.30 supports 1.34–1.36 and lists 1.37 as "tested but unsupported". Our reference
environment runs **k3s v1.37.2+k3s1**, i.e. outside that window (spec risk K11) — so the
supported cells cannot be marked `tested` from the reference environment.

| Kubernetes | CNPG 1.30 status | Cell | Evidence now | Promoted by |
|---|---|---|---|---|
| **1.34** | supported | `untested` | Nothing run on 1.34. | R16.2 integration environment (kind-hosted CNPG on a supported version). |
| **1.35** | supported | `untested` | Nothing run on 1.35. | R16.2 integration environment. |
| **1.36** | supported | `untested` | The **kubectl client** is v1.36.1 (docs/ENVIRONMENT.md), which is not a server and does not make the cell tested. | R16.2 integration environment. |
| **1.37** | tested but unsupported | `tested` (k3s v1.37.2+k3s1) | Phase 0 deployed here: the pod runs and `/healthz` answers through the Service (R11.14). The chart's `kubeVersion: ">=1.24.0-0"` accepts it. | Phase 1–9 features validated here, with every finding labelled as a possible k3s-1.37 artifact (K11). |

**Reading this table:** the reference environment proves our chart works on k3s 1.37, and
*nothing* about 1.34–1.36. Claiming "1.29–1.37 supported" from one cluster is exactly the
over-claim R13.9 exists to prevent.

## Operator and monitoring stack

| Component | Cell | Notes |
|---|---|---|
| **CNPG 1.30.0** | `tested` | The reference operator (`ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0`, namespace `cnpg-system`). The CRD set the chart's Role grants on (`clusters`, `clusters/status`, `backups`, `scheduledbackups`, `pooledqueries`, `clusterimages`) is installed there. The operator's 99 restarts are recorded in docs/ENVIRONMENT.md and are an environment observation, not a `pgsi` defect. |
| **CNPG 1.29** | `untested` | In the spec's supported range; not present in the reference environment. |
| **CNPG 1.28** | `impossible` | Predates the CVE-2026-44477 exporter-role model R12.4 depends on; out of scope by ruling, not by lack of testing. |
| **kube-prometheus-stack 0.7x+** | `tested` (reference install `prom-operator` in `monitoring`) | Its `ServiceMonitor` CRD is what `chart/templates/servicemonitor.yaml` renders against, and its selector requires `release: prom-operator` — captured in values.yaml's `additionalLabels` comment. The Phase 0 ServiceMonitor is rendered and applied; scrape confirmation is Phase 9's R9.1 acceptance. |
| **No Prometheus operator** | `tested` (lab profile) | `values-lab.yaml` sets `monitoring.enabled=false`, so the chart renders no `monitoring.coreos.com` objects and installs on a cluster without the CRDs (R13.6). |

## How a cell is promoted

1. Run the thing the `promoted by` column names.
2. Record the artifact (fixture path, command output, or CI run) in the `evidence now` cell.
3. Change the cell state in the same commit as the artifact. A cell state that moves
   without new evidence is a regression in this file, not an improvement.

Cells are never demoted silently either: an environment change that invalidates a
`tested` cell (an operator upgrade, a k3s upgrade) moves it back to `untested` with a note.
