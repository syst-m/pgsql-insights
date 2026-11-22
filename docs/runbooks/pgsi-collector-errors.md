# Collectors failing on a cluster

**Alert:** `PGSICollectorErrors` · **severity:** `warning` · **for:** `15m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-collector-errors.md`

```promql
sum by (namespace, cluster) (rate(pgsi_collector_errors_total[15m])) > 0
```

## What it means

`pgsi_collector_errors_total` is one of R9.8's self-observability metrics: a counter
incremented once per collector that raised, per instance. The rule fires on *any*
error rate above zero sustained for fifteen minutes — deliberately, because a
collector that fails intermittently is worse than one that fails outright. An outright
failure sets `pgsi_up` to 0 and every series for that instance disappears, which is
loud. An intermittent failure leaves stale series in place, so a dashboard keeps
plotting a number that is thirty seconds old and looks current.

The two alerts partition the failure modes: `pgsi_up` answers "did the round
succeed", this one answers "which collector broke inside a round that mostly
succeeded". The `by (namespace, cluster)` grouping means the alert names the cluster,
and the API's per-instance `error` field (R8.4's partial-render contract) names the
collector.

## First three commands

```sh
# Which collector, and with what message?
curl -s localhost:8080/api/v1/clusters | jq '.data[] | select(.namespace=="<ns>" and .name=="<cluster>") | .instances[] | {pod,role,lastSuccessTime,error}'
# Did the round itself succeed, or only mostly?
curl -s localhost:8080/api/v1/clusters | jq '.meta | {total,truncated,failed,loading}'
kubectl logs -n pgsi -l app.kubernetes.io/name=pgsi --tail=200 | grep -iE 'collector|error' | tail -30
```

The second command is R8.4's contract doing its job: `meta.failed[]` lists namespaces
we attempted and could not read, with a code from R8.5's enum, so a partial render
says so instead of showing a short list.

## Common causes

| Cause | How you tell |
|---|---|
| A collector's query is not valid on this PG major | the error is a syntax error on a specific view; R3.4's version matrix and the compatibility matrix record which `pg_stat_*` columns exist per major |
| `pg_stat_statements` not installed | the statements collector errors while the system collectors succeed; R3.1's prerequisite, checked at startup and reported, not assumed |
| Permissions revoked on a view | `permission denied for view pg_stat_activity`; R12.1's verb set covers Kubernetes objects, not database grants |
| Statement timeout hit on a slow instance | correlates with `PGSICollectionRoundSlow`; a collector that times out counts as an error and as a slow round |
| Connection pool exhausted on our side | errors cluster across all clusters at once, not one |

## Remediation

Fix the collector, not the alert. A single collector failing degrades one page of the
UI while the rest stay correct — that is the point of per-collector error counting,
and it is why this is warning rather than critical.

If the errors are all one collector on one PG major, that is a version-matrix gap and
belongs in `docs/compatibility-matrix.md`, not in a restart.

## Escalation

Not paged. Escalate if the error rate is above zero on every cluster at once — that
pattern is our bug, not theirs, and it usually means a shared dependency (auth, the
connection pool, the API server) rather than a database.
