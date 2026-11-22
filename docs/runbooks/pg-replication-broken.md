# A replica claims to be in recovery but has no WAL receiver running

**Alert:** `PGReplicationBroken` · **severity:** `critical` · **for:** `5m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pg-replication-broken.md`

```promql
max by (namespace, cluster) (cnpg_pg_replication_in_recovery) > max by (namespace, cluster) (cnpg_pg_replication_is_wal_receiver_up)
```

## What it means

CNPG's `ReplicaFailingReplication`, promoted to critical. `in_recovery` counts pods
that are standbys; `is_wal_receiver_up` counts the ones actually streaming. A standby
with no receiver is serving reads from a snapshot that is not advancing — it looks
healthy to a readiness probe and is silently stale to a client.

## First three commands

```sh
kubectl get cnpg -n <ns> <cluster> -o jsonpath='{.status.instancesReportedState}' | jq
kubectl get pods -n <ns> -l cnpg.io/cluster=<cluster>,cnpg.io/instanceRole=replica
kubectl logs -n <ns> <cluster>-<n> --tail=200 | grep -i "wal receiver\|streaming\|connection to"
```

## Common causes

| Primary changed and the replica has not reconnected | `currentPrimary` differs from the pod the replica is connected to |
| `primary_conninfo` auth rotated | logs show `FATAL: password authentication failed` from the replicator role |
| Replica promoted elsewhere (double primary) | two pods report `in_recovery=0` in one cluster |

## Remediation

A double primary is the emergency case: stop the old primary (`kubectl scale` the
cluster down to the healthy instance, or `kubectl cnpg promote`) before accepting
writes on both. A plain dead receiver is normally fixed by deleting the replica pod
and letting the operator rebuild it.

## Escalation

Double-primary suspicion on a cluster with clients attached: escalate immediately, do not wait out the `for` window.
