# More than 10 deadlocks per hour on a database

**Alert:** `PGDeadlockRateHigh` · **severity:** `warning` · **for:** `1h`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pg-deadlock-rate-high.md`

```promql
increase(cnpg_pg_stat_database_deadlocks{cluster!=""}[1h]) > 10
```

## What it means

CNPG parity (`DatabaseDeadlockConflicts` in the current sample, which fixed the
legacy `alerts.yaml` version that read the raw counter instead of `increase(...)`).
Deadlocks are normal in small numbers; a rate means two code paths take locks in
inconsistent order.

## First three commands

```sh
kubectl exec -n <ns> <cluster>-1 -- psql -U postgres -Atc \
  "SELECT datname, deadlocks FROM pg_stat_database WHERE datname NOT LIKE 'template%' ORDER BY 2 DESC"
```
Set `log_lock_waits` and `deadlock_timeout` on the cluster to capture the pair of
statements in the Postgres log.

## Common causes

| Two code paths update the same rows in opposite order | the classic cause |
| A missing index turning an UPDATE into a table scan | lock scope far larger than intended |
| Serialised transactions on a hot row | one row updated by every writer |

## Remediation

The fix is in the application's lock ordering or the missing index, not in
`deadlock_timeout`. Use the R10.3 Queries page to find the statements involved.

## Escalation

Not paged.
