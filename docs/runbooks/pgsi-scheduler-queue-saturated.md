# The scheduler queue is over half full

**Alert:** `PGSISchedulerQueueSaturated` · **severity:** `warning` · **for:** `10m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-scheduler-queue-saturated.md`

```promql
max(pgsi_scheduler_queue_depth) > 256
```

## What it means

`pgsi_scheduler_queue_depth` is one of R9.8's self-observability metrics: the number
of work items waiting to run. The rule's threshold is 256 and its summary says "over
half full", which pins the configured queue capacity at 512 — the alert fires at 50 %,
deliberately early, because the state after a full queue is shedding, and shedding is
lossy.

That ordering is why this rule exists separately from `PGSISchedulerShedding`. Shedding
tells you data was dropped; a half-full queue tells you it *will* be, while there is
still time to act. A 10-minute `for:` plus a 50 % threshold is the difference between a
capacity change you choose and one you make during an incident.

`max()` rather than `sum()` matters: several queues each half full is a different
problem from one queue at capacity, and summing them hides the first behind the second.

## First three commands

```sh
# Which queue, if there is more than one?
curl -s localhost:8080/metrics | grep '^pgsi_scheduler_queue_depth'
# Is it draining at all, or only growing?
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=deriv(pgsi_scheduler_queue_depth[15m])' | jq .
# Are collection rounds overrunning the interval and piling work up?
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=max(pgsi_collector_duration_seconds)' | jq .
```

The second command is most of the diagnosis in one number. A positive derivative means
work arrives faster than it drains — a throughput problem. A flat high value means the
queue sits at a steady state near capacity — a sizing problem.

## Common causes

| Cause | How you tell |
|---|---|
| Rounds slower than the refresh interval | third command above R13.1's 3-second budget; the most common cause, and `PGSICollectionRoundSlow` fires with it |
| The fleet grew and the worker count did not | discovery (R1.1–R1.9) found more instances; depth rose over days, not minutes |
| One slow cluster stalling a shared worker | the first command's label set shows whether queues are per-cluster; one cluster's rounds dominate |
| Retries from auth or connectivity failures | every failed round re-queues; correlates with `PGSICollectorErrors` |
| Queue capacity genuinely too small for the fleet | depth sits at exactly the configured ceiling |

## Remediation

Two levers, and they fix different problems. Make the round faster (R9.5's `metrics.topN`,
a narrower retention window per R3.14) when the derivative is positive. Raise worker
count or queue capacity when the queue is simply sized for a smaller fleet — the latter
costs memory, which against R13.5's 512 MiB store budget is not free.

## Escalation

Not paged. It is the earliest warning in the self-observability set that the pipeline is
undersized, and the one worth acting on before `PGSISchedulerShedding` makes the damage
visible.
