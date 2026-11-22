# Shared-buffer hit ratio below 90 %

**Alert:** `PGSICacheHitRatioLow` · **severity:** `warning` · **for:** `30m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-cache-hit-ratio.md`

> **Provisional metric names.** `pgsi_query_shared_blocks_hit_total` and
> `pgsi_query_shared_blocks_read_total` are not names the spec fixes. R9.4 names the
> aggregate *members* (execution time, rows, shared block hits/reads, lock time) without
> naming the metrics, and these two were authored in the rule set ahead of the phase that
> chooses them — the chart's own header separates this pair from the spec-named rules for
> exactly that reason. They change when Phase 2 picks the real names, and this runbook
> changes with them.

```promql
sum(rate(pgsi_query_shared_blocks_hit_total[15m]))
  / clamp_min(sum(rate(pgsi_query_shared_blocks_hit_total[15m]))
            + rate(pgsi_query_shared_blocks_read_total[15m]), 1) < 0.9
```

## What it means

Cloud SQL's `insights/aggregate` surface reports a shared-buffer hit ratio and R9.4
requires the equivalent in our aggregate family. The ratio is hits over hits plus reads:
how often Postgres found a block already in `shared_buffers` instead of reading it from
disk. Below 90 % means a growing share of query time is spent on physical IO, which
shows up as execution time rising while query counts stay flat.

The `clamp_min(…, 1)` on the denominator is what makes the rule safe on a quiet
database. Without it a cluster with almost no activity produces `0/0` → `NaN`, and
`NaN < 0.9` is false — so an idle cluster silently stops being monitored. Clamping the
denominator to at least 1 makes the ratio evaluate to 0 on a cluster doing nothing,
which is the honest answer and does not fire.

Note the chart's rule text ends in `> 0.25` while its summary says "below 90 %"; the
expression above is the one that matches the summary. The threshold discrepancy is
recorded as a chart defect, not resolved here.

## First three commands

```sh
# The ratio per database, from the metric.
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=sum by (datname) (rate(pgsi_query_shared_blocks_hit_total[15m]))' | jq .
# The same number straight from Postgres, to rule out a collection artefact.
psql -h <host> -c 'SELECT datname, blks_hit, blks_read,
  round(100.0*blks_hit/nullif(blks_hit+blks_read,0),2) AS pct
  FROM pg_stat_database WHERE datname IS NOT NULL ORDER BY pct LIMIT 10;'
# How big is shared_buffers on this cluster?
kubectl get cluster -n <ns> <cluster> -o jsonpath='{.spec.shared_buffers}{"\n"}'
```

The second command is the one that settles it. If Postgres reports a healthy ratio and
the metric does not, the bug is in our collection (R9.4's per-(cluster, db, user)
rollup), not in the database.

## Common causes

| Cause | How you tell |
|---|---|
| A working set larger than `shared_buffers` | the ratio is low cluster-wide and stable; the classic cause, and the answer is sizing, not tuning |
| One database with a scan-heavy workload | the first command localises it to one `datname` (R9.3's database label) |
| Sequential scans on a large table | correlates with a rising execution time on one fingerprint (R3.7's attribution) |
| A recent `shared_buffers` change | the ratio drops at a timestamp that matches a config change |
| Metric-name drift (provisional) | the metric does not exist at all, so the ratio is `NaN` and the rule goes quiet rather than firing |

## Remediation

Confirm against `pg_stat_database` before tuning anything. A low hit ratio is a sizing
signal — `shared_buffers` or the working set, not a query change — so getting the number
right first is the whole job.

## Escalation

Not paged. It is a 30-minute trend alert on a capacity property, and one of the two
rules in the set whose metric names are not yet authoritative.
