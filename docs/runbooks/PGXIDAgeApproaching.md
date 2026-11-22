# CNPG-parity early warning on transaction ID age

**Alert:** `PGXIDAgeApproaching` · **severity:** `warning` · **for:** `1h`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/PGXIDAgeApproaching.md`

```promql
max by (namespace, cluster, database) (cnpg_pg_database_xid_age{cluster!=""}) > 300000000
```

## What it means

The CNPG `PGDatabaseXidAge` rule, kept verbatim so adopting pgsi does not silently
drop an alert the adopter already has. Paired with `PGXIDWraparoundRisk` above: this
one is the "plan something" tier, that one is the "do something now" tier.

## First three commands

```sh
kubectl exec -n <ns> <cluster>-1 -- psql -U postgres -Atc \
  "SELECT datname, age(datfrozenxid) FROM pg_database ORDER BY 2 DESC LIMIT 10"
```

## Common causes

See `PGXIDWraparoundRisk` — same causes, caught ~1.8 B earlier.

## Remediation

Schedule the work now; see the critical runbook for the procedure.

## Escalation

Not paged at this tier. It becomes `PGXIDWraparoundRisk` if left alone.
