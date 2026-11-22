# No instance of a cluster is collectable

**Alert:** `PGSIClusterUnreachable` · **severity:** `critical` · **for:** `5m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-cluster-unreachable.md`

```promql
min by (namespace, cluster) (pgsi_up) == 0
```

## What it means

R9.12's `pgsi_up{namespace,cluster,pod,instance_role}` is 1 when our last collection
round for that instance succeeded and 0 when it did not — it is our verdict on the
instance, not Prometheus's verdict on an exporter. `min by (namespace, cluster)`
collapses every instance of one cluster to its worst value, so this fires only when
**no** instance of that cluster could be collected: not one primary, not one replica.

That distinction is the whole rule. A single dead replica is
`PGSIInstanceUnreachable` (warning); a dead primary is `PGSIPrimaryUnreachable`
(critical); both at once is this. A cluster whose instances are all unreachable has
no readable Postgres behind it, which is why the tier is critical and the window is
5 minutes rather than 15.

The rule is `min by (namespace, cluster)`, so the alert's labels name the cluster
directly — no join against an inventory is needed to answer "which one".

## First three commands

```sh
# 1. What does CNPG think the cluster looks like?
kubectl get cluster -n <namespace> <cluster> -o wide
# 2. Are there instances at all, and what are their roles?
kubectl get pod -n <namespace> -l cnpg.io/cluster=<cluster> \
  -o custom-columns=NAME:.metadata.name,ROLE:.metadata.labels.cnpg\.io/instance-role,READY:.status.conditions[?(@.type==\"Ready\")].status
# 3. What did our last round actually return for it?
curl -s localhost:8080/api/v1/clusters | jq '.data[] | select(.name=="<cluster>") | {health,instances:[.instances[]|{pod,role,lastSuccessTime,error}]}'
```

Command 3 is the one that separates the two failure classes. If the API lists the
cluster with an `error` per instance, discovery reached it and the query failed. If
the cluster is missing from the response entirely — or appears in `meta.failed[]` —
discovery never reached it, and the API's partial-render contract (R8.4) is what
tells you so.

## Common causes

| Cause | How you tell |
|---|---|
| The cluster is genuinely down (all pods gone) | command 2 returns nothing; CNPG's status shows `Cluster status: initializing` or an error |
| Primary missing and no promotion happened | command 2 shows only replicas, or no pod with `instance-role: primary` |
| Discovery cannot see the namespace | the cluster is in a namespace excluded by `discovery.namespaces` (R1.4); the API reports it under `meta.failed[]` with a code from R8.5's enum |
| RBAC revoked the list verb | command 3 returns `Forbidden` for that namespace; R12.1's verb set is namespaced and R12.3 keeps a `ClusterRole` off unless `rbac.allNamespaces=true` |
| Every collector for it failed | command 3's per-instance `error` is populated; also firing `PGSICollectorErrors` |

## Remediation

If CNPG has no healthy instance, this is a Postgres incident and pgsi is the
messenger — follow the CNPG runbook for the cluster, not this file. pgsi's job here
is to have told you in 5 minutes and named the cluster.

If CNPG reports the cluster healthy but our API cannot see it, the fault is ours:
check the namespace list and the RBAC before restarting anything, because a restart
of a pgsi pod that cannot authorise will produce the same result.

## Escalation

Paged. For a single-instance cluster this and `PGSIPrimaryUnreachable` fire together —
that is correct, not a duplicate: one cluster with one instance has one instance role.
R5.5 covers how a single-instance cluster renders (an empty replication section with
an explanation, not a broken chart).
