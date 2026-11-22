# pgsi has not successfully collected from a cluster in three poll intervals

**Alert:** `PGCollectorScrapeFailing` · **severity:** `warning` · **for:** `10m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pg-collector-scrape-failing.md`

```promql
time() - max by (namespace, cluster) (pgsi_collector_last_success_timestamp{cluster!=""}) > 3 * 60
```

## What it means

Our own metric, not CNPG's — the R9.4 `pgsi_collector_*` family. This is the alert
that distinguishes "the cluster is fine and the data is stale" from "the cluster is
fine and the data is fresh", which is the difference between an honest dashboard and
a lying one. R8.x's partial-render contract marks the cluster stale in the UI; this
rule makes the same fact alertable.

## First three commands

```sh
curl -s localhost:8080/metrics | grep pgsi_collector_last_success_timestamp
kubectl logs -n pgsi deploy/pgsi --tail=200 | grep -i "<cluster>"
kubectl get cnpg -n <ns> <cluster> -o jsonpath='{.status.phase}'
```

## Common causes

| The app secret rotated mid-poll | `403`/`28P01` in the collector log for that cluster |
| The `-rw` service moved | endpoint lookup fails while the CR still resolves |
| The collector is rate-limited by connection count | many clusters fail at once |
| The pod is being restarted before a poll completes | `kubectl get pods -n pgsi` shows restarts |

## Remediation

Fix the per-cluster credential or endpoint. If the collector's poll interval was
lowered below what the fleet needs, raise it rather than the alert threshold — the
`3 × interval` formula follows the config automatically.

## Escalation

Not paged. If it affects every cluster at once, the collector itself is the incident.
