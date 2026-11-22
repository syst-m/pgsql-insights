# More than one pgsi version is reporting metrics

**Alert:** `PGSIVersionSkew` · **severity:** `warning` · **for:** `30m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-version-skew.md`

```promql
count(count by (version) (pgsi_build_info)) > 1
```

## What it means

`pgsi_build_info{version,commit}` (R11.3) is the one series every pgsi process emits
before doing anything else, and the only place our own version is a metric. The rule
counts distinct `version` values across all series and fires when there is more than one.

The inner `count by (version)` collapses per-pod series into one per version, so a
three-replica deployment on one version yields `1` and stays quiet. Two versions means a
rollout that has not finished, two installs sharing a Prometheus, or a pod that was never
updated. It matters because a mixed fleet makes every other alert ambiguous: a latency or
cardinality change at 30 % of pods is not attributable until you know which version
produced which series.

Thirty minutes is the window because a normal rolling update produces skew for a few
minutes and should never page. Sustained skew is the finding.

## First three commands

```sh
# Which versions, and on which pods?
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=count by (version,namespace,pod) (pgsi_build_info)' | jq .
# What does Kubernetes say the pods are running?
kubectl get pod -n pgsi -l app.kubernetes.io/name=pgsi \
  -o custom-columns=NAME:.metadata.name,IMAGE:.spec.containers[0].image,AGE:.metadata.creationTimestamp
# What does the chart think it deployed?
helm get values pgsi -n pgsi | grep -A3 'image:'
```

The second command resolves almost every case: pods with different ages and different
image tags are a rollout in progress or one that stalled.

## Common causes

| Cause | How you tell |
|---|---|
| A rollout mid-flight | pods have different ages; clears on its own and the 30m `for:` normally suppresses it |
| A rollout stalled on a pod that will not start | one new pod in `CrashLoopBackOff` while the old one serves; the chart's `Recreate` strategy (R11.15's RWO volume cannot attach to two pods) means a new pod that never starts leaves the old one running |
| Two installs scraped by one Prometheus | two namespaces, or two releases, both exporting `pgsi_build_info` |
| A tag moved under a running pod | `:latest`-style tags make the running image and the tag disagree; the chart pins by digest for this reason (R11.5) |
| A pod that survived a rollback | the pod's `AGE` predates the current ReplicaSet |

## Remediation

If it is a stalled rollout, fix the pod that will not start — the log is the whole
diagnosis, and an exit 2 on startup is R8.8's fail-closed auth path (IMPLEMENTATION.md
§5-D14). If it is two installs sharing a Prometheus, the fix is a scrape configuration,
not a deployment.

## Escalation

Not paged. Worth resolving before investigating any other alert in this set, because
while two versions report, no other metric can be attributed to a build.
