# The scheduler is shedding work

**Alert:** `PGSISchedulerShedding` · **severity:** `warning` · **for:** `15m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-scheduler-shedding.md`

```promql
sum(rate(pgsi_scheduler_shed_total[15m])) > 0
```

## What it means

`pgsi_scheduler_shed_total` (R9.8) counts work items the scheduler **dropped** rather
than ran. Shedding is the deliberate consequence of a bounded queue: past the limit,
the scheduler discards new work instead of growing memory without bound, so the
failure mode is bounded and visible rather than an OOM kill.

This is the alert that means **data is being lost right now.** Every other
self-observability rule describes something slow or failing; this one describes
work we chose not to do. A round that is shed leaves the last successful result in
place, so the UI keeps showing a number that is one round old — the same staleness
`PGSICollectorErrors` produces, reached by a different path.

The causal chain is fixed: a round slower than the refresh interval
(`PGSICollectionRoundSlow`) grows the queue (`PGSISchedulerQueueSaturated`) until the
queue is full and work is dropped (this alert). All three firing is one incident with
three symptoms, and the fix belongs at the slow end, not here.

## First three commands

```sh
# Is the queue full, or is the drain rate the problem?
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=max(pgsi_scheduler_queue_depth)' | jq .
# Which work items are being shed — all clusters, or one?
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=sum by (namespace,cluster) (increase(pgsi_scheduler_shed_total[30m]))' | jq .
# Are rounds overrunning their interval?
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=max(pgsi_collector_duration_seconds)' | jq .
```

## Common causes

| Cause | How you tell |
|---|---|
| Collection rounds overrunning the refresh interval | `pgsi_collector_duration_seconds` above the R13.1 3-second budget; the usual cause, and `PGSICollectionRoundSlow` fires with it |
| Fleet grew without the worker count growing | discovery found more instances (R1.1–R1.9) while the scheduler's concurrency stayed fixed; shedding spread across all clusters, not one |
| One cluster monopolising the queue | the second command concentrates on one cluster |
| A retry storm | auth or connectivity failures cause every round to retry, multiplying queued work |
| Queue depth configured too small | `pgsi_scheduler_queue_depth` sits at a hard ceiling that matches the configured limit exactly |

## Remediation

Fix the slow end. Raising queue depth or worker count buys time and costs memory;
making the round faster is the fix. If the fleet grew, the worker count needs to grow
with it — that is a capacity decision, and it is the correct one to make now rather
than after an OOM.

## Escalation

Not paged by default, and worth paging deliberately: it is the only rule that says the
data on screen is incomplete, and the only one whose silence is indistinguishable from
"nothing to do".
