# Fewer pods are Ready for a cluster than the Cluster CR asks for

**Alert:** `PGNoRunningPods` · **severity:** `critical` · **for:** `10m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/PGNoRunningPods.md`

```promql
count by (namespace, cluster) (kube_pod_status_ready{condition="true",job="kube-state-metrics"} * on (namespace, pod) group_left (cluster) cnpg_pg_postmaster_start_time{cluster!=""}) < count by (namespace, cluster) (cnpg_pg_postmaster_start_time{cluster!=""})
```

## What it means

CNPG's `PGPodsNotRunning`/`PGNoActivePods` pair, expressed against the reference
environment's kube-state-metrics so the rule works on a stock kube-prometheus-stack
install. `spec.instances` is the promise; this rule fires when the fleet stops
keeping it. It is the alert that catches a single replica pod stuck on a drained node
while the primary looks perfectly healthy.

## First three commands

```sh
kubectl get cnpg -n <ns> <cluster> -o jsonpath='{.spec.instances} {.status.instances} {.status.readyInstances}'
kubectl get pods -n <ns> -l cnpg.io/cluster=<cluster> -o wide
kubectl describe pod -n <ns> <cluster>-<n> | tail -30
```

## Common causes

| Pod pending after a node drain | `kubectl get pods -o wide` shows a node that is `SchedulingDisabled` |
| PVC cannot attach (RWO on a different node) | `Multi-Attach error` in the pod events |
| Image pull failure | `ImagePullBackOff` |
| Instance manager not ready while Postgres is | pod `Running` but not `Ready`; the 8000 probe is failing |

## Remediation

Match the fix to the event, not to the alert. A `Multi-Attach` error means the pod
must move, not that the PVC must grow; an instance-manager probe failure is usually
fixed by deleting the pod.

## Escalation

On a single-instance cluster this alert means the cluster is fully down — treat it as the `PGClusterDown` case.
