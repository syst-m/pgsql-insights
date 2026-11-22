# Lock wait is over 25 % of execution time

**Alert:** `PGSILockWaitDominant` · **severity:** `warning` · **for:** `30m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-lock-wait.md`

> **Provisional metric name.** `pgsi_query_lock_time_seconds_total` is not a name the
> spec fixes. `lock_time` itself is now a named member of R9.4's aggregate family —
> added to the frozen requirement by the R9.11 ruling from A3's `f9aae41` review, which
> resolved the earlier state where the rule asserted a value R9.4 did not export. The
> *member* is specified; the *metric name* is still the chart's own, and changes when
> Phase 2 picks it.

```promql
sum(rate(pgsi_query_lock_time_seconds_total[15m]))
  / clamp_min(sum(rate(pgsi_query_execution_time_seconds_total[15m])), 1) > 0.25
```

## What it means

A quarter of all query time on this cluster is spent waiting for locks rather than
executing. R9.4 exports lock time per (cluster, db, user) alongside execution time, and
R3.7 names lock wait as one of the three load-attribution axes (exec vs plan, IO, lock),
so this is the aggregate view of what R4.4's blocking chain shows per session.

The `clamp_min(…, 1)` on the denominator is the same guard as the hit-ratio rule: on a
cluster doing nothing, execution time is ~0 and the ratio becomes infinite, which would
page for contention on an idle database. Clamping makes the ratio 0 when there is no
work, which is the honest answer.

What this rule cannot tell you is *which* query. R9.1's two-plane rule keeps lock totals
in `/metrics` and every session's identity in the API, so the alert names the cluster and
R4.4's `pg_blocking_pids()` chain names the sessions — the metric is the pager, the API
is the diagnosis.

## First three commands

```sh
# Per-database lock time, to localise it.
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=sum by (datname) (rate(pgsi_query_lock_time_seconds_total[15m]))' | jq .
# The live blocking chain, from the API (R4.4) — not from a pg_locks join.
curl -s 'localhost:8080/api/v1/active-queries?namespace=<ns>&cluster=<cluster>' | jq .
# Direct from Postgres, to rule out a collection artefact.
psql -h <host> -c "SELECT pid, state, wait_event_type, wait_event,
  now()-query_start AS running, left(query,60) AS q FROM pg_stat_activity
  WHERE wait_event_type='Lock' ORDER BY running DESC LIMIT 10;"
```

## Common causes

| Cause | How you tell |
|---|---|
| A long-running transaction holding locks | the API's active-queries payload shows a session with a long `query_start` age; R4.6 separates an *idle in transaction* age from an active one, and an idle holder is the usual culprit |
| DDL blocking readers | `AccessExclusiveLock` in the third command; a migration is the classic cause |
| Two writers on the same rows | the blocking chain shows a cycle or a long chain on one table |
| A deadlock storm | correlates with `pg-deadlock-rate-high`; R4.4's chain shows the cycle before Postgres breaks it |
| Metric-name drift (provisional) | the metric does not exist, so the ratio is `NaN` and the rule goes quiet |

## Remediation

Get the chain, not the ratio. The metric says a quarter of time is lock wait; the API's
active-queries payload says which session blocks which, and that is the actionable half.
`pg_terminate_backend()` (R4.5) is the only write pgsi performs, it is behind
`--allow-actions` default false, and terminating the *holder* — not the victim — is the fix.

## Escalation

Not paged. Paired with `PGSIAPILatencyHigh` it usually means our own API is waiting
behind the same contention it is reporting.
