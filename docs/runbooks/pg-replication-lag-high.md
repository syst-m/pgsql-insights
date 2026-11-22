# A replica is more than 5 minutes behind its primary

**Alert:** `PGReplicationLagHigh` · **severity:** `critical` · **for:** `5m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pg-replication-lag-high.md`

```promql
max by (namespace, cluster) (cnpg_pg_replication_lag{cluster!=""}) > 300
```

## What it means

CNPG's own `PGReplication` rule fires the same condition at **warning**. We promote
it to critical: a replica 5 minutes behind is not a degraded read path, it is the
window in which a failover loses 5 minutes of writes, and it is the number R5.x's
replication panel leads with.

## First three commands

```sh
kubectl get cnpg -n <ns> <cluster> -o jsonpath='{.status.instancesReportedState}'
kubectl exec -n <ns> <cluster>-1 -- pg_controldata /var/lib/postgresql/data | grep -i checkpoint
kubectl logs -n <ns> <cluster>-2 --tail=100 | grep -i "streaming\|wal\|receive"
```
Compare the primary's LSN with the replica's: `cnpg_pg_replication_slots_pg_wal_lsn_diff`
gives the byte gap per slot, `cnpg_pg_stat_replication_write_lag_seconds` the time gap.

## Common causes

| Replica starved of I/O | write lag high on the replica, WAL receiver up, no errors in logs |
| WAL segment pressure on the primary | `cnpg_collector_pg_wal{value="count"}` at the `max` ceiling; segments recycled before the replica reads them |
| A slot is inactive and holding WAL | `cnpg_pg_replication_slots_*` shows `active=0` for a slot that should be live |
| Network partition between nodes | multiple clusters in the same namespace lag simultaneously |

## Remediation

If the gap is slot-related, drop the stale slot (`pg_drop_replication_slot`) — a slot
nothing reads is a disk leak, not a safety margin. If the replica is simply slow, the
fix is the replica's I/O, not the threshold. Never raise `synchronous_standby_names`
to make the metric lie.

## Escalation

Sustained lag > 15 m on a cluster with `minSyncReplicas > 0` also stalls commits on the primary; escalate at that point rather than waiting for the 5 m `for` window to expire repeatedly.
