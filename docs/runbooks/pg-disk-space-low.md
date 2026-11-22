# The data volume is above 90 % of its configured limit

**Alert:** `PGDiskSpaceLow` · **severity:** `critical` · **for:** `10m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pg-disk-space-low.md`

```promql
cnpg_collector_pg_wal{cluster!="",value="volume_size"} > 0.9 * on (namespace, pod) cnpg_collector_pg_wal{cluster!="",value="volume_max"}
```

## What it means

`cnpg_collector_pg_wal` carries `value="volume_size"` (bytes used) and
`value="volume_max"` (the `pg_wal` size limit CNPG enforces) on every instance
(research §5.1). Crossing 90 % on a primary is an outage in waiting: Postgres panics
when it cannot extend a relation, and a WAL volume that fills takes the whole
instance with it, not just the database that grew.

## First three commands

```sh
kubectl exec -n <ns> <cluster>-1 -- df -h /var/lib/postgresql/data
kubectl exec -n <ns> <cluster>-1 -- du -sh /var/lib/postgresql/data/base /var/lib/postgresql/data/pg_wal
kubectl get pvc -n <ns> -l cnpg.io/cluster=<cluster>
```
Then find the growth: `SELECT pg_size_pretty(pg_database_size(datname)), datname FROM pg_database ORDER BY pg_database_size(datname) DESC LIMIT 10`.

## Common causes

| WAL backlog from a broken archiver | see `PGWALArchivingFailing`; `value="count"` is at `max` |
| A replication slot holding WAL on a dead replica | `pg_replication_slots` with `active = false` |
| Table growth from a bulk job | one database grew by GB in the last hour |
| PVC resized but the filesystem not expanded | `kubectl get pvc` shows a larger `spec` than `status` |

## Remediation

Order matters: drop stale slots and fix archiving **before** expanding the volume,
otherwise you pay for a larger disk that the same leak fills. If the growth is real,
expand the PVC (the reference environment's `iscsi-ext4`/`iscsi-xfs` classes support
`allowVolumeExpansion`) and confirm the filesystem grows — a PVC resize without a
filesystem resize changes nothing.

## Escalation

Above 95 % on a primary, page immediately; Postgres can fail a commit before the alert's 10 m `for` window closes.
