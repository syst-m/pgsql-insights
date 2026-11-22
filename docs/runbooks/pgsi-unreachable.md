# pgsi's own /metrics is not being scraped

**Alert:** `PGSIUnreachable` · **severity:** `warning` · **for:** `10m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-unreachable.md`

```promql
absent(pgsi_up)
```

## What it means

R9.12's `pgsi_up` is the first series our binary emits. `absent()` on it is the
alert that catches the failure mode R9.9 documents: a ServiceMonitor that exists but
that no Prometheus selects on, so the target never appears and every other `pgsi_*`
alert silently stops firing. Without this rule, a mislabelled ServiceMonitor looks
like a healthy fleet with no problems.

## First three commands

```sh
kubectl get servicemonitor -n pgsi -o jsonpath='{.items[*].metadata.labels}'
kubectl get prometheus -n monitoring -o jsonpath='{.items[0].spec.serviceMonitorSelector}'
kubectl get --raw "/api/v1/namespaces/monitoring/services/prom-operator-kube-prometh-prometheus:9090/proxy/api/v1/targets?state=active" | jq '.data[].labels | select(.job | contains("pgsi"))'
```
The second command is the one that matters: the ServiceMonitor's labels must satisfy
the Prometheus `serviceMonitorSelector` (chart README, "Making it discoverable").

## Common causes

| `additionalLabels` missing the `release` label | the selector is `{release: prom-operator}` and ours is `{}` |
| The ServiceMonitor is in a namespace excluded by `serviceMonitorNamespaceSelector` | |
| Auth is on and the scrape has no token | target is `UP` in Prometheus but returns 401 |
| The pod is not Ready | the Service has no endpoints |

## Remediation

Set `metrics.serviceMonitor.additionalLabels` to whatever the local Prometheus
selects on. On the reference environment that is
`--set metrics.serviceMonitor.additionalLabels.release=prom-operator`.

## Escalation

Not paged. This is the alert that tells you the rest of the alert set stopped working.
