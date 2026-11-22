# WAL archiving is failing on the primary

**Alert:** `PGWALArchivingFailing` · **severity:** `critical` · **for:** `5m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/PGWALArchivingFailing.md`

```promql
(cnpg_pg_stat_archiver_last_failed_time - cnpg_pg_stat_archiver_last_archived_time) > 1 and on (namespace, pod) cnpg_pg_replication_in_recovery == 0
```

## What it means

CNPG's `LastFailedArchiveTime`, promoted to critical. The `and on(...)
cnpg_pg_replication_in_recovery == 0` clause is what makes it correct — without it
the rule fires on every replica, which has no archiver role. A primary that cannot
archive cannot recycle WAL, so the failure is simultaneously a PITR gap (you can no
longer restore to an arbitrary point) and a disk-time bomb (WAL accumulates to the
volume ceiling).

## First three commands

```sh
kubectl exec -n <ns> <cluster>-1 -- psql -U postgres -Atc \
  "SELECT archived_count, failed_count, last_archived_time, last_failed_time FROM pg_stat_archiver"
kubectl logs -n <ns> <cluster>-1 --tail=200 | grep -i "archive\|barman\|wal"
kubectl exec -n <ns> <cluster>-1 -- df -h /var/lib/postgresql/data
```

## Common causes

| Archive_command destination unreachable | S3/GCS endpoint error, expired IAM credentials, wrong bucket |
| `archive_mode = off` after a parameter patch | `pg_settings` disagrees with `spec.backup.barmanObjectStore` |
| Disk full, so archiving cannot write its status file | `df` at 100 %; the archive error is a symptom |
| `archive_timeout` far too high | not a failure, but `last_archived_time` looks stale — check `failed_count` before believing this alert |

## Remediation

Fix the destination, then confirm `failed_count` stops incrementing and
`last_archived_time` advances. A backlog of unarchived segments drains slowly; watch
`cnpg_collector_pg_wal{value="count"}` until it returns under `max`.

## Escalation

Any PITR-capable production cluster: page. A cluster with a broken archiver cannot be restored to now, and the RPO you promised is no longer the RPO you have.
