# The primary of a cluster is not collectable

**Alert:** `PGSIPrimaryUnreachable` · **severity:** `critical` · **for:** `5m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-primary-unreachable.md`

```promql
pgsi_up{instance_role="primary"} == 0
```

## What it means

Our last collection round against the instance CNPG labels `primary` failed. The
`instance_role` label comes from R9.3's label contract, which every series carries,
and its value is read from CNPG's own `cnpg.io/instance-role` label rather than
inferred from a SQL query — so this alert is telling you the *writer* is unreadable,
not that a reader is slow.

It is critical because a cluster cannot accept writes without a primary, and because
the failure is usually one of two things: the primary pod is gone (and promotion has
not happened, or failed), or the primary is up but wedged — too many connections, or
an `idle in transaction` session holding a lock that makes every query time out
(R4.6 is the requirement that separates the two ages, and this alert cannot).

`PGSIClusterUnreachable` fires alongside it whenever no replica is collectable
either. One firing alone means the replicas are still readable — a read-only
degradation that would otherwise be invisible.

## First three commands

```sh
kubectl get cluster -n <namespace> <cluster> -o jsonpath='{.status.currentPrimary}{"\n"}{.status.instancesReported}{"\n"}'
kubectl get pod -n <namespace> -l cnpg.io/cluster=<cluster>,cnpg.io/instance-role=primary -o wide
kubectl logs -n <namespace> -l cnpg.io/cluster=<cluster>,cnpg.io/instance-role=primary --tail=100 --prefix
```

If the first command's `currentPrimary` names a pod the second command does not
list, CNPG has not yet noticed, or is mid-promotion.

## Common causes

| Cause | How you tell |
|---|---|
| Primary pod deleted or evicted | second command returns nothing while the cluster still names a primary |
| Promotion in progress | the cluster's status shows a `currentPrimary` that is not Ready yet; the alert usually clears within the CNPG probe window and the 5m `for:` exists to suppress exactly this |
| Primary up, Postgres not accepting connections | pod is `Running 1/1`; the log shows `too many connections` — check `pgsi_up` is 0 while the pod is Ready, which is the signature of a live pod that cannot answer a query |
| Collector query timing out | also firing `PGSICollectorErrors` for the same cluster; the API's per-instance `error` field carries the driver message |
| Role label stale | the pod's `cnpg.io/instance-role` still says `primary` after a demotion, so the series exists with the wrong role |

## Remediation

Confirm the role from CNPG's status, not from the pod label — the label lags a
promotion by one reconcile, and acting on a stale label restarts the wrong pod.

If the primary is up but wedged, the next question is what it is waiting on, and that
is R4.3's wait-event rollup and R4.4's blocking chain (the API's active-queries
payload, not a metric — R9.1's two-plane rule puts session text on the API and
numbers on `/metrics`). `pg_terminate_backend()` is the only write pgsi performs and
it is behind `--allow-actions`, default false (R4.5).

## Escalation

Paged. A single-instance cluster fires this and `PGSIClusterUnreachable` together;
that is the same fault counted two ways, not two incidents.
