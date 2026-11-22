# Transaction IDs approaching the 2^31 wraparound cliff

**Alert:** `PGXIDWraparoundRisk` · **severity:** `critical` · **for:** `30m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pg-xid-wraparound-risk.md`

```promql
max by (namespace, cluster, database) (cnpg_pg_database_xid_age{cluster!=""}) > 1500000000
```

## What it means

CNPG warns at `> 300000000`. That threshold is ~14 % of the 2 147 483 648 ceiling
and is a reasonable early warning, but it is a *warning*: at 300 M you have weeks of
headroom on a busy cluster and the alert tells you nothing you must act on today. We
ship both tiers — the CNPG-parity warning (see below) and this critical rule at
1.5 B, the point where the remaining headroom is days and vacuum becomes the only
conversation.

## First three commands

```sh
kubectl exec -n <ns> <cluster>-1 -- psql -U postgres -Atc \
  "SELECT datname, age(datfrozenxid) FROM pg_database ORDER BY 2 DESC LIMIT 10"
kubectl exec -n <ns> <cluster>-1 -- psql -U postgres -Atc \
  "SELECT relname, age(relfrozenxid) FROM pg_class WHERE relkind='r' ORDER BY 2 DESC LIMIT 10"
```
Then check whether autovacuum is running at all: `SELECT * FROM pg_stat_activity WHERE query ILIKE '%autovacuum%'`.

## Common causes

| Autovacuum disabled or throttled to nothing | `autovacuum = off` in `pg_settings`, or `autovacuum_vacuum_cost_limit` set very low |
| A long-running prepared transaction pins the xmin | `SELECT * FROM pg_prepared_xacts` |
| A logical slot is inactive and holding xmin | `pg_replication_slots` with `active = false` |
| One cold table never vacuumed | `age(relfrozenxid)` high on a table with no writes |

## Remediation

At 1.5 B the fix is to remove whatever pins xmin (kill the idle-in-transaction
session, unreserve or drop the stale slot) and then `VACUUM (FREEZE)` the oldest
tables. Raising thresholds without removing the pin only buys time.

## Escalation

Any database above 1.9 B is a page-at-any-hour situation: a wraparound failure puts the database in single-user mode and the recovery is a `VACUUM` from single-user, not a restart.
