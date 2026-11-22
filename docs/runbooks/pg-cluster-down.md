# Postgres is not answering on a cluster that has pods

**Alert:** `PGClusterDown` · **severity:** `critical` · **for:** `5m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pg-cluster-down.md`

```promql
count by (namespace, cluster) (cnpg_pg_postmaster_start_time{cluster!=""}) == 0
```

## What it means

`cnpg_pg_postmaster_start_time` is emitted by the instance manager on every pod
where postmaster is up (research §5, `pg_postmaster.start_time`). Zero series for a
cluster that the Kubernetes API says has instances means the pods exist but no
Postgres is serving on them — the S3 source is silent, so every other `cnpg_*`
series for that cluster also disappears and their alerts go quiet. This rule is what
makes that silence loud.

## First three commands

```sh
kubectl get cnpg -n <ns> <cluster> -o jsonpath='{.status.phase} {.status.instances} {.status.readyInstances}'
kubectl get pods  -n <ns> -l cnpg.io/cluster=<cluster> -o wide
kubectl logs      -n <ns> -l cnpg.io/cluster=<cluster> --all-containers --tail=200
```
Cross-check against our own inventory (`GET /api/v1/clusters`) so you are looking at
the cluster pgsi discovered, not at a typo.

## Common causes

| Instance crash-looping after a config patch | `kubectl logs` shows a FATAL at startup; `status.phase` is `Upgrading` or `Error` |
| PendingRestart after a `spec.parameters` change | `cnpg_collector_pending_restart == 1` on the primary; the pod is Running but on stale settings |
| PVC unbound / node lost | pod is `Pending`; `kubectl get pvc -n <ns>` shows `Pending` or `Lost` |
| Image pull failure after a pin change | `ImagePullBackOff` in the pod status |

## Remediation

Decide **before** acting whether this is a config regression or an infrastructure
loss — the two have opposite first moves. A config regression is reverted by
`kubectl edit cluster` (or by rolling back the Helm revision that changed
`spec.parameters`); an infrastructure loss is a `kubectl delete pod` to let CNPG
reschedule onto a healthy node. Do not force-delete a primary that is merely slow:
that triggers a switchover.

## Escalation

If `status.readyInstances` is 0 for more than 15 m on a production cluster, page the Postgres on-call. pgsi cannot serve Query Insights for the cluster while S2 and S3 are both unreachable (R8.x's partial-render contract renders it as unavailable rather than empty).
