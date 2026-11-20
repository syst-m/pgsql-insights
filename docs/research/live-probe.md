# Live probe of the reference environment (2026-11-20)

Direct measurements taken against the four clusters in `docs/ENVIRONMENT.md`, captured to settle
spec questions that reading the CRDs could not answer. Raw captures are in `testdata/`.

Every row below is a command that was actually run, not an inference. Where a measurement
contradicts an earlier document, the contradiction is called out and the earlier document is fixed.

## 1. Postgres versions and `pg_stat_statements` — the K2 risk, confirmed

`kubectl -n <ns> exec <pod> -c postgres -- psql -Atc "select current_setting('server_version_num'),
coalesce((select extversion from pg_extension where extname='pg_stat_statements'),'ABSENT'),
current_setting('shared_preload_libraries')"`

| Cluster | Pod | `server_version_num` | Exact version | `pg_stat_statements` | `shared_preload_libraries` |
|---|---|---|---|---|---|
| `cookbook` | `cookbook-pg-1` | `170011` | PostgreSQL **17.11** | **ABSENT** | *(empty)* |
| `open-webui` | `open-webui-pg-2` | `160015` | PostgreSQL **16.11** | **ABSENT** | *(empty)* |
| `ai-api-gateway` | `rev-proxy-pg-2` | `170011` | PostgreSQL **17.11** | **ABSENT** | *(empty)* |
| `freeidp` | `freeidp-pg-1` | — | — | not reachable | container not running (see §3) |

**Finding 1 — the headline Cloud SQL feature has no data source on any of these clusters.**
`pg_stat_statements` is absent on all three reachable clusters, and `shared_preload_libraries` is
empty on all three. This empirically confirms risk **K2** in `requirements.md` §9: it was inferred
from `cloudnativepg.md` §4.10 ("`pg_stat_statements` is absent from CNPG's 14 default queries");
it is now measured on the actual target.

**Finding 2 — enabling it is not free, and the spec understates the friction.** `shared_preload_libraries`
is a postmaster-level parameter: `CREATE EXTENSION pg_stat_statements` alone fails with
`required extension "pg_stat_statements" is not loaded`. Enabling it on a CNPG cluster means
patching `spec.parameters.shared_preload_libraries` and **restarting every instance** of that
cluster. So Query Insights — the product's headline — requires a per-cluster `Cluster` patch plus a
rolling restart, which is in tension with the brief's "rapidly rollable out" and with the spec's own
"zero per-cluster prerequisites" rule (R1.6).

This is the one place where the architecture and the brief genuinely conflict, and it needs an
explicit answer in the spec rather than a remediation string. Options, in the order the spec should
adopt them:

1. **Ship Phases 1–2 and 4–5 without it.** Everything in discovery, the aggregate/system metric
   surface, the ServiceMonitor, and the Fleet page works from S1 + S3 alone. This is already the
   phase order the spec chose, which is the strongest argument for that ordering.
2. **Make enablement a first-class product action, not a documentation footnote**: a documented
   `pgsi`-side capability to patch `spec.parameters` for a chosen cluster (the one mutation the
   product needs beyond `pg_terminate_backend`), with the restart cost stated up front and an
   opt-in `--enable-pg-stat-statements` path. This generalizes R12.8's `--allow-actions` gate from
   "terminate session" to "the small set of mutations this product performs".
3. **Never block on it.** A cluster without the extension still renders inventory, system insights,
   active queries, replication, poolers, and recommendations from bloat/settings.

## 2. S3 (`:9187/metrics`) — always up, with `spec.monitoring` unset

`kubectl -n <ns> port-forward pod/<pod> 19999:9187` then `GET http://127.0.0.1:19999/metrics`:

| Cluster | PG | Bytes | `# TYPE` families |
|---|---|---|---|
| `cookbook/cookbook-pg-1` | 17 | 43 476 | **107** |
| `open-webui/open-webui-pg-2` | 16 | 45 873 | **115** |
| `ai-api-gateway/rev-proxy-pg-2` | 17 | 48 802 | **118** |
| `ai-api-gateway/rev-proxy-pg-pooler-86b4bdd84d-n9vlv` :9127 | — | 22 775 | **80** |

**Finding 3 — the spec's central architectural claim is correct.** All four clusters have
`spec.monitoring` unset, yet every pod serves a full `cnpg_*` exposition on 9187 and the pooler
serves 80 families on 9127. This validates `cloudnativepg.md` §4.1 (the exporter is compiled into
the instance manager, not a sidecar) and corrects the earlier `ENVIRONMENT.md` framing that called
the sidecar "a third, opt-in source" — fixed in the same commit as this document.

**Finding 4 — the pgbouncer surface is real and large.** 80 families on 9127 including
`cnpg_pgbouncer_lists_{databases,dns_names,dns_pending,dns_queries,dns_zones,free_clients,free_servers,login_clients,used_clients,used_servers}`
and the pools/stats families. `requirements.md` R6.1 claims this surface has zero existing
dashboards and zero alert rules in the CNPG ecosystem; the measurement shows 80 families of data
nobody is consuming, which is the strongest single "free feature" argument in the product.

## 3. R2.4's PG 13–18 version matrix — proven from live data

Diffing the metric-family sets of the two majors present in the reference environment
(`comm` on the sorted `# TYPE` name lists):

**Families present on PG 16 (`open-webui-pg-2`) and absent on PG 17 (`rev-proxy-pg-2`):**
```
cnpg_pg_stat_bgwriter_buffers_backend
cnpg_pg_stat_bgwriter_buffers_backend_fsync
cnpg_pg_stat_bgwriter_buffers_checkpoint
cnpg_pg_stat_bgwriter_checkpoint_sync_time
cnpg_pg_stat_bgwriter_checkpoint_write_time
cnpg_pg_stat_bgwriter_checkpoints_req
cnpg_pg_stat_bgwriter_checkpoints_timed
```
**Families present on PG 17 and absent on PG 16:**
```
cnpg_pg_stat_bgwriter_stats_reset_time
cnpg_pg_stat_checkpointer_buffers_written
cnpg_pg_stat_checkpointer_checkpoints_req
cnpg_pg_stat_checkpointer_checkpoints_timed
cnpg_pg_stat_checkpointer_restartpoints_done
cnpg_pg_stat_checkpointer_restartpoints_req
cnpg_pg_stat_checkpointer_restartpoints_timed
cnpg_pg_stat_checkpointer_sync_time
cnpg_pg_stat_checkpointer_write_time
```

**Finding 5 — the `pg_stat_bgwriter` → `pg_stat_checkpointer` split claimed in R2.4 and
`cloudnativepg.md` §7 is exactly what the exporter emits**, and CNPG 1.30 ships both the legacy
`pg_stat_bgwriter` query (which on 17 collapses to just `stats_reset_time`, hence
`pg_stat_bgwriter_17` in §4.10) and the new `pg_stat_checkpointer` set. A collector that hardcodes
either family is wrong on one of the two clusters we actually have. This is the concrete
justification for R2.4's "matrix as data, not as scattered `if` statements".

## 4. S4 (`:8000/pg/status`) — HTTPS-only, no client certificate needed

`GET https://<pod>:8000/pg/status` via port-forward returns the `postgres.PodStatus` JSON;
`GET http://<pod>:8000/pg/status` returns `Client sent an HTTP request to an HTTPS server.`
No client certificate is required for this path, confirming `cloudnativepg.md` §3.3.

**Finding 6 — R1.7 must encode the scheme per port, not guess it.** The measured contract:

| Port | Scheme | Auth | Evidence |
|---|---|---|---|
| 5432 | postgres wire | password / TLS | `psql` connects |
| 9187 | **HTTP** (HTTPS only when `spec.monitoring.tls.enabled`) | none | `http://` returns metrics |
| 8000 | **HTTPS, always** | none for `/pg/status` | `http://` errors, `https:// -k` returns JSON |
| 8010 | HTTP, pod-local only | n/a | not reachable externally by design |

A collector that probes `http://pod:8000` will report the status endpoint as down. R1.7's
transport selection needs this table as its input, and the spec should state it explicitly.

**Finding 7 — 27 fields verified on a primary** (24 on the single-instance pod), which resolves
the `(unverified)` marker `cloudnativepg.md` §1.1 carries on the PodStatus field list:

```
currentLsn, currentWAL, executableHash, instanceArch, instanceManagerVersion, isArchivingWAL,
isInstanceManagerUpgrading, isPgRewindRunning, isPodReady, isPrimary, isWalReceiverActive,
lastArchivedWAL, lastArchivedWALTime, lastFailedWAL, lastFailedWALTime, loadedConfigurationHash,
mightBeUnavailable, node, pendingRestart, pendingRestartForDecrease, pod, replayPaused,
replicationInfo, replicationSlotsInfo, sessionID, systemID, timeLineID
```

Two of these are more valuable to us than the spec currently realises:

- **`replicationInfo[]`** (`applicationName`, `state: streaming`, `receivedLsn`, `writeLsn`, …) and
  **`replicationSlotsInfo[]`** (`slotName: _cnpg_rev_proxy_pg_1`, `slotType: physical`,
  `restartLsn`, `walStatus`) give the **entire replication topology and slot set from one
  unauthenticated HTTP GET**, with no SQL and no credentials. R5.5/R10.5's replication topology
  view should prefer S4 over S2 here, and the spec doesn't currently say so.
- **`lastFailedWAL: 00000004.history` + `lastFailedWALTime: 2026-11-17T11:35:42Z`** on
  `rev-proxy-pg-2` is a live archiving anomaly on a cluster whose CR reports
  "Cluster in healthy state" — exactly the class of finding the product exists to surface, obtained
  for free. `isPodReady: false` on a running primary is a second such signal.

## 5. `pods/proxy` — works in-cluster, fails from this laptop

| Attempt | Result |
|---|---|
| `kubectl get --raw /api/v1/namespaces/cookbook/pods/http:cookbook-pg-1:9187/proxy/metrics` | `ServiceUnavailable … proxy error from 127.0.0.1:6443 while dialing 10.42.2.226:9187, code 502` |
| Same with `https:` on 9187, `http:` on 8010, `https:` on 8000, and bare `cookbook-pg-1:9187` | **all** the same 502 |
| `kubectl exec` on the same pod | works |
| `kubectl port-forward` on the same pod, then `curl` | works (see §2) |

**Finding 8 — the 502 is an artifact of the laptop→k3s-API-server path, not of the endpoints.**
`exec` and `port-forward` reach the pod fine; only the API-server *proxy* subresource fails, because
the k3s server we talk to (via `127.0.0.1:6443`, i.e. a tunnelled endpoint) cannot route to the pod
CIDR `10.42.0.0/16`. R1.7's ordering — direct URL first, `pods/proxy` as fallback — is therefore
**correct as written**, and this measurement is the evidence for it. The spec should add one
sentence: `pods/proxy` availability depends on the API server's route to the pod CIDR, so it is
reliable when `pgsi` runs in-cluster and unreliable when the API endpoint is tunnelled.

## 6. Degraded cluster characterisation (`freeidp`)

| Pod | Phase | Ready | Detail |
|---|---|---|---|
| `freeidp-pg-1` | Running | **false** | container `postgres` in **`CrashLoopBackOff`**; `kubectl exec` → `unable to upgrade connection: container not found ("postgres")` |
| `freeidp-pg-2` | **Pending** | false | `PodInitializing`; unschedulable per `ENVIRONMENT.md` |
| `freeidp-59c66c9bc-*` ×2 | Running | false | the app's own pods, also not ready — the outage is visible one level up |

**Finding 9 — the CR reports `2` instances with status "Not enough disk space", and the reality is
worse than the CR says**: the primary's postgres container is crash-looping, so the cluster has
**zero** serving instances while `status.instances` still reports 2. A collector that trusts
`status.instances` over the pod's container statuses will render a green cluster that is fully down.
R1.2 must read `instancesStatus`/container statuses, not just the counts, and R10.12's degraded
state has a second variant to handle beyond the Pending replica.

## 7. Node architecture — R11.2's multi-arch requirement is a hard blocker

| Node | Architecture | OS |
|---|---|---|
| `dmaf5-1`, `dmaf5-2`, `k8s-ctrl`, `k8s-wrk-1`, `um760-1` | **amd64** (all five) | Debian 12 (bookworm), k3s v1.37.2+k3s1 |

Cross-checked against the pod itself: `instanceArch: amd64` in `rev-proxy-pg-2`'s PodStatus.
Local toolchain: `uname -m` → `arm64`, `docker version --format '{{.Server.Arch}}` → `arm64`.

**Finding 10 — a plain `docker build` on the dev machine produces an arm64 image that cannot be
scheduled on any node in the target cluster.** `ENVIRONMENT.md` said "k3s nodes are arm64-capable
here"; all five are amd64. This promotes R11.2's multi-arch build from a quality goal to a Phase 0
blocker: without `buildx --platform linux/amd64,linux/arm64`, the image will not run in the
reference environment at all. It also means Phase 0's acceptance criterion must be *deploy the
multi-arch image to the reference cluster and see it Running*, not merely *build it*.

## 8. Self-observability precedent observed in the wild

`cnpg_backends_max_tx_duration_seconds` carries `application_name="cnpg_metrics_exporter"` on the
exporter's own session, alongside `application_name=""` for real idle sessions. CNPG tags its own
connection and thereby makes itself filterable — the exact mechanism R2.8 specifies for us. The
measured exposition confirms the pattern is load-bearing rather than theoretical: without the
filter, every `backends` count we publish is inflated by one.

## 9. What this changes in `requirements.md`

To be folded in together with the round-1 review findings at v0.2.0 (deliberately not edited now,
so the reviewers are not reviewing a moving target):

| # | Change | Target |
|---|---|---|
| P1 | Add the measured PG versions (17.11, 16.11) to the compatibility matrix and to §8's fixture table | R13.9, §8 |
| P2 | Rewrite R3.1 so extension absence is a *product flow* (detect → offer → patch → verify), not a docs string; generalize R12.8's action gate to cover the `Cluster` patch | R3.1, R12.8 |
| P3 | Add the per-port scheme/auth table (5432 wire, 9187 HTTP, 8000 HTTPS, 8010 pod-local) to R1.7 so transport selection is data, not guessing | R1.7 |
| P4 | Add one sentence to R1.7 on `pods/proxy` depending on the API server's route to the pod CIDR | R1.7 |
| P5 | Prefer S4's `replicationInfo`/`replicationSlotsInfo` over S2 for the replication topology view | R5.5, R10.5 |
| P6 | R1.2 must read container statuses, not `status.instances` counts (Finding 9) | R1.2, R10.12 |
| P7 | R11.2's acceptance becomes "deploy to the reference cluster and see it Running"; record that all nodes are amd64 | R11.2, §8 |
| P8 | Record the 80-family pgbouncer measurement as the evidence line for R6.1 | R6.1 |
| P9 | Add the captured exposition files as the R16.3 fixture set (already committed in `testdata/`) | R16.3 |
