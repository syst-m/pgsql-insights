# pgsi's API is slower than its own budget

**Alerts:** `PGSIAPILatencyHigh` (`warning`, `for: 15m`, p95 > 0.4 s) and
`PGSIAPILatencyCritical` (`critical`, `for: 15m`, p95 > 1.5 s)
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-api-latency.md`

```promql
histogram_quantile(0.95, sum by (le) (
  rate(pgsi_http_request_duration_seconds_bucket{path!~"/healthz|/readyz|/metrics"}[10m])
)) > 0.4    # warning,  the R13.3 budget
histogram_quantile(0.95, sum by (le) (
  rate(pgsi_http_request_duration_seconds_bucket{path!~"/healthz|/readyz|/metrics"}[10m])
)) > 1.5   # critical
```

## What it means

`pgsi_http_request_duration_seconds` is one of R9.8's self-observability metrics, and
R13.3 sets the budget the warning tier reads: p95 under 400 ms for an API response.
The critical tier is 1.5 s — the point at which the SPA's own 15-second refresh starts
stacking requests on top of each other.

The `path!~"/healthz|/readyz|/metrics"` exclusion is load-bearing and easy to break.
Those three are served on the same mux as the API (R8.6's single-binary rule), and
`/metrics` in particular is scraped every 30 s by Prometheus and is cheap to serve.
Without the exclusion, a slow `/metrics` render — which is exactly what a large
cardinality footprint produces — would page for "the API is slow" while every real
`/api/v1/*` request stayed fast. The alert measures the API, not the process.

## First three commands

```sh
# Per-route p95, so you know which endpoint is slow rather than that one is.
# Query the Prometheus that fired the alert, not pgsi's own /api/v1/metrics/query —
# that proxy is R8.3 and ships in Phase 5, so today it is not a usable tool.
curl -sG <prometheus-url>/api/v1/query --data-urlencode \
  'query=topk(5, histogram_quantile(0.95, sum by (path,le) (rate(pgsi_http_request_duration_seconds_bucket[10m]))))' | jq .
# Is the API answering at all, and how fast?
time curl -s localhost:8080/api/v1/version -o /dev/null
time curl -s localhost:8080/api/v1/clusters -o /dev/null
```

The two `time` calls separate the two failure classes in one step: if `/version` is
fast and `/clusters` is slow, the handler is the problem; if both are slow, it is the
process (GC, CPU limit, or the pod being throttled).

## Common causes

| Cause | How you tell |
|---|---|
| A large fleet served without pagination | R8.10 caps `limit` at 500 and defaults to 50; a caller passing a large limit, or a handler that ignores it, serialises the whole fleet |
| The store being read synchronously | correlates with `PGSIStoreAtCapacity`; eviction under a read load is the classic cause of a p95 that is slow but not failing |
| Auth on, and every request doing a token compare plus a SubjectAccessReview | `--auth=kube-rbac-proxy` adds an API call per request; `--auth=token` is a constant-time compare and is cheap |
| CPU throttled | the pod is at its limit; check `kubectl top pod` against the chart's `resources` |
| The exclusion regex broken | a new route added to the mux but the alert's regex still excludes only the three probes — check the `path` label distribution before believing the number |

## Remediation

Identify the route first. `/api/v1/clusters` latency is A1's handler; `/metrics`
latency is cardinality (R9.4/R9.5's budgets, and R9.5's `metrics.topN` is the lever);
`/api/v1/metrics/query` latency is Prometheus's, not ours, and R8.3's contract says
to answer `PromQLUnavailable` rather than hang when the upstream is the slow part.

## Escalation

The critical tier is paged. The warning tier is not, and should not be paged for a
single slow route — the per-route breakdown above is what makes the warning
actionable, and the alert alone does not name a route because it aggregates `by (le)`
only.
