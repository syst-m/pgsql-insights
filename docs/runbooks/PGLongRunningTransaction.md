# A transaction has been open for more than 5 minutes

**Alert:** `PGLongRunningTransaction` · **severity:** `warning` · **for:** `5m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/PGLongRunningTransaction.md`

```promql
max by (namespace, cluster, pod) (cnpg_backends_max_tx_duration_seconds{cluster!=""}) > 300
```

## What it means

CNPG parity (`LongRunningTransaction`, same metric and threshold). Kept at warning.
An idle-in-transaction session is the cheap version of this: it holds a snapshot, so
it blocks vacuum and inflates `pg_database_xid_age` long before it blocks a writer.

## First three commands

```sh
kubectl exec -n <ns> <cluster>-1 -- psql -U postgres -Atc \
  "SELECT pid, state, xact_start, now()-xact_start AS xact_age, application_name, client_addr \
     FROM pg_stat_activity WHERE state <> 'idle' ORDER BY xact_start LIMIT 10"
```
`client_addr` is what turns this into a name: it maps to the R10.3 Client Addresses
page, which attributes the session to a workload.

## Common causes

| An application that forgot to commit | `application_name` is an app pod; `state = idle in transaction` |
| A long batch job or migration | `state = active`, query is a `COPY`/`ALTER` |
| An interactive session left open | `application_name = psql`, from a human's IP |
| A prepared transaction never resolved | `pg_prepared_xacts` |

## Remediation

`SELECT pg_terminate_backend(pid)` for the forgotten session, after confirming the
owner. For a genuine long write, the fix is the job, not the alert threshold.

## Escalation

Not paged. It is the alert that usually explains an `PGXIDAgeApproaching` firing elsewhere.
