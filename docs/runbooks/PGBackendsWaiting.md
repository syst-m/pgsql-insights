# More backends are waiting on locks than the cluster's normal contention

**Alert:** `PGBackendsWaiting` · **severity:** `warning` · **for:** `10m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/PGBackendsWaiting.md`

```promql
max by (namespace, cluster, pod) (cnpg_backends_waiting_total{cluster!=""}) > 300
```

## What it means

CNPG parity (`BackendsWaiting`). The count of backends blocked on a lock. The
threshold is a blunt instrument on a busy cluster — the useful signal is *who is
blocking whom*, which is why R10.3's Active Queries page renders the blocking chain
rather than only the count.

## First three commands

```sh
kubectl exec -n <ns> <cluster>-1 -- psql -U postgres -Atc \
  "SELECT pid, pg_blocking_pids(pid) AS blocked_by, wait_event_type, wait_event, state, query \
     FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid)) > 0"
```

## Common causes

| A single holder blocking a chain | one pid appears in many `pg_blocking_pids` arrays |
| Lock contention on a hot table | many short waits spread across tables |
| A DDL waiting for ACCESS EXCLUSIVE behind readers | `ALTER TABLE` in the activity list |

## Remediation

Terminate or wait out the root holder, not the victims — killing the leaves of a
blocking chain regenerates them. For recurring contention the fix is the query
pattern.

## Escalation

Not paged. Sustained for > 1 h alongside a latency regression on the Queries page is the escalation path.
