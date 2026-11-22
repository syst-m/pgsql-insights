# A collection round exceeds R13.1's budget

**Alert:** `PGSICollectionRoundSlow` · **severity:** `warning` · **for:** `15m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-collection-round-slow.md`

```promql
max by (namespace, cluster) (pgsi_collector_duration_seconds) > 3
```

## What it means

R13.1 budgets a collection round at under 3 seconds per instance, and
`pgsi_collector_duration_seconds` (R9.8) is how we know. The rule takes `max by
(namespace, cluster)`, so it fires on the slowest collector in the round rather than
on the average — averaging hides a single 30-second collector behind nine fast ones,
and the round cannot finish before its slowest part does.

The budget is not cosmetic. R13.1's 3 seconds is what makes a 15-second refresh
interval feasible without rounds overlapping; a round that takes longer than the
interval means the scheduler queues work faster than it drains it, which is
`PGSISchedulerQueueSaturated`, and eventually `PGSISchedulerShedding`. The three
rules are one causal chain observed at three points.

## First three commands

```sh
# Which collector in the round is slow?
curl -s localhost:8080/api/v1/clusters | jq '.data[] | select(.name=="<cluster>") | .collectors'
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=topk(5, max by (namespace,cluster,collector) (pgsi_collector_duration_seconds))' | jq .
# Is the scheduler backing up behind it?
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=max(pgsi_scheduler_queue_depth)' | jq .
```

## Common causes

| Cause | How you tell |
|---|---|
| `pg_stat_statements` with a huge number of distinct queries | the statements collector dominates; R9.5's top-N (default 50 per cluster) bounds what we *export*, not what the view contains to sort |
| A cluster with thousands of backends | `pg_stat_activity` is slow to scan; correlates with a high active-query count |
| Lock-graph collection on a contended database | `pg_blocking_pids()` per row is the expensive part (R4.4); shows up as one collector, not the round |
| The instance itself is slow to answer any query | `PGSIAPILatencyHigh` fires too, and `time curl …/api/v1/version` is slow |
| Collection running against a replica under read load | the round is slow only on one role |

## Remediation

Identify the collector first — a round that is slow because one collector is slow is
fixed by narrowing that query (R9.5's top-N, a smaller `retention.statementSnapshots`
window per R3.14), not by raising the budget.

If the whole round is uniformly slow, the instance is slow, and this alert is a
symptom of something the database-side rules will name.

## Escalation

Not paged. Watch for the follow-on: a round slower than the refresh interval produces
queue growth within minutes and shedding shortly after, and those two are the alerts
that mean data is being dropped.
