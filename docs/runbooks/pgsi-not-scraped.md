# pgsi is not exporting metrics

**Alert:** `PGSINotScraped` · **severity:** `critical` · **for:** `10m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-not-scraped.md`

```promql
absent(pgsi_build_info)
```

## What it means

`pgsi_build_info` is the first series our binary emits — it exists before the first
collection round, before any cluster is discovered, and even when every collector is
failing. So `absent()` on it is not a Postgres signal and not a collector signal: it
means **the process is not answering `/metrics` at all**, or Prometheus is not
scraping it.

This is the alert that catches R9.9's failure mode. A ServiceMonitor that exists but
that no Prometheus selects on produces no target, and every other `pgsi_*` alert
silently stops firing — a mislabelled ServiceMonitor otherwise looks like a healthy
fleet with no problems. It also precedes the `pgsi_up` family: `PGSIClusterUnreachable`
and `PGSIInstanceUnreachable` read `pgsi_up`, which we only export when we are
running, so if we are not running they go quiet rather than firing. Only this rule
can tell "everything is fine" from "nothing is reporting".

## First three commands

```sh
kubectl get pod -n pgsi -l app.kubernetes.io/name=pgsi \
  -o custom-columns=NAME:.metadata.name,STATUS:.status.phase,READY:.status.conditions[*].status
kubectl get endpoints -n pgsi pgsi
kubectl get servicemonitor -n pgsi -o jsonpath='{.items[*].metadata.labels}'
```

In order: is the pod up and Ready; does the Service have a ready address for it; do
our ServiceMonitor labels satisfy the local Prometheus `serviceMonitorSelector`. On
the reference environment that selector is `{release: prom-operator}` and the chart's
`metrics.serviceMonitor.additionalLabels` default is `{}`.

## Common causes

| Cause | How you tell |
|---|---|
| Pod crashed or pending (R11.15's RWO PVC cannot attach) | `kubectl describe pod` shows `FailedAttachVolume` or an ImagePull error |
| Readiness probe failing | pod is `Running` but `READY 0/1`, so the Service has no endpoints — both probes are `httpGet` because the distroless image has no shell (R11.2), so a probe failure means `/healthz` or `/readyz` genuinely did not answer 200 |
| ServiceMonitor not selected | the third command's labels do not satisfy the Prometheus selector; the rule set goes quiet with it |
| Auth on, scrape has no token | the target exists in Prometheus but returns 401 — R8.8's `--auth-metrics=none` is what lets Prometheus scrape without a bearer while `/` and `/api/v1/*` still require one |
| Wrong port name on the Service | the ServiceMonitor's `port: http` must match the Service's port name exactly |

## Remediation

If the pod is down, `kubectl logs` it before deleting it — a process that exits 2 on
startup is R8.8's fail-closed path (token mode with an empty token is a startup
error, not an accept-anything default; see IMPLEMENTATION.md §5-D14), and the log is
the only record of which flag was wrong.

If the pod is up and the target is missing, set
`metrics.serviceMonitor.additionalLabels` to whatever the local Prometheus selects on
and re-apply. Then confirm in the Prometheus targets page, not from the alert going
quiet — a rule that stops firing is not the same as a rule that stops being true.

## Escalation

Paged. Nothing else in the set can be trusted while this is firing, including the
rules that appear to be healthy.
