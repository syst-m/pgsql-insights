# A replica is not collectable

**Alert:** `PGSIInstanceUnreachable` · **severity:** `warning` · **for:** `10m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-instance-unreachable.md`

```promql
pgsi_up{instance_role="replica"} == 0
```

## What it means

Our last collection round against a **replica** failed. The `instance_role` label is
part of R9.3's label contract — every series carries `namespace`, `cluster`, `pod` and
`instance_role` — and its value comes from CNPG's `cnpg.io/instance-role` label, so
the alert names the exact pod in its own labels and never needs a join to answer
"which one".

The tier is warning, not critical, because a replica is not on the write path: the
cluster still accepts writes and reads continue from the primary. What you lose is
read capacity and, if it is the only replica, high availability — a second failure
now takes the cluster down. That is the actual risk this alert prices, and it is why
it should not be silenced as "just a replica".

`PGSIPrimaryUnreachable` and `PGSIClusterUnreachable` are the critical siblings. This
rule fires alone when the primary is fine; it fires alongside them when nothing is
readable.

## First three commands

```sh
kubectl get pod -n <namespace> <pod> -o wide
kubectl get cluster -n <namespace> <cluster> -o jsonpath='{.status.instances}{"\n"}'
kubectl logs -n <namespace> <pod> --tail=100 --prefix
```

The second command is the check that matters: CNPG's `.status.instances` is the
authoritative role map, and a pod whose label disagrees with it is a stale label, not
a dead database.

## Common causes

| Cause | How you tell |
|---|---|
| Replica pod deleted, evicted, or moved by a rollout | first command returns `NotFound`, or the pod age is newer than the alert's `for:` window |
| Pod up but Postgres not ready | `Running 1/1` with the CNPG readiness probe passing on the pod while our query fails — the instance manager is up, Postgres is starting |
| Collector query fails on this pod only | also firing `PGSICollectorErrors` for the same cluster; the API's per-instance `error` field carries the driver message |
| Too many connections on the replica | the log shows `too many connections`; a read-heavy client pinned to one replica is the usual cause |
| Stale role label after a promotion | CNPG's status now calls this pod something else, so the series is labelled with a role it no longer holds |

## Remediation

If the pod is gone and CNPG is recreating it, wait out the reconcile — the 10-minute
`for:` exists to suppress exactly this churn, so an alert that clears on its own was
a rollout, not an incident.

If the pod is up and only the collector fails, the failure is ours. Check whether the
instance manager's `:9187` exposition answers (R2.6 parses `cnpg_*` from it) before
suspecting Postgres itself.

## Escalation

Not paged by default. Page it manually if the cluster has exactly one replica and the
primary is also degraded — that combination is one failure away from an outage, and
`PGSIClusterUnreachable` is the rule that will fire next.
