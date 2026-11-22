# pgsi's store is approaching its capacity budget

**Alerts:** `PGSIStoreNearCapacity` (`warning`, `for: 30m`, above 90 %) and
`PGSIStoreAtCapacity` (`critical`, `for: 10m`, above 99 %)
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-store-capacity.md`

```promql
(pgsi_store_size_bytes / 536870912) > 0.9    # warning,  90 %
(pgsi_store_size_bytes / 536870912) > 0.99   # critical, 99 %
```

## What it means

`pgsi_store_size_bytes` is one of R9.8's self-observability metrics: the on-disk size
of our own store. The two rules are the same measurement at two thresholds, which is
why one file covers both — the diagnosis is identical and only the urgency differs.
The warning exists to give an operator a 30-minute-headroom signal before the
critical one, which is the point at which writes start failing.

**The denominator is `config.maxStoreBytes`, and it is hardcoded.** `536870912` is
512 MiB — R13.5's store cap, written literally in `chart/templates/prometheusrule.yaml`
rather than templated from `metrics.maxStoreBytes`. It is not the PVC size: the
volume is 1 GiB on the default and lab profiles and 5 GiB on production. The rule
measures the store against the store's own ceiling, which is the right denominator —
R13.5's contract is size-based eviction, so the first thing to hit 100 % is the store,
not the disk. The defect is the hardcoding: `chart/values.yaml:69` carries
`maxStoreBytes: 536870912` as a *commented* default, so setting it to anything else
moves the real ceiling and leaves the alert's percentage behind.

## First three commands

```sh
kubectl get pvc -n pgsi pgsi-data -o jsonpath='{.status.capacity.storage} {.status.phase}{"\n"}'
kubectl get pod -n pgsi -l app.kubernetes.io/name=pgsi -o jsonpath='{.items[*].spec.volumes[*].persistentVolumeClaim.claimName}{"\n"}'
kubectl exec -n pgsi <pod> -- df -h /var/lib/pgsi 2>/dev/null || \
  kubectl get --raw "/api/v1/namespaces/pgsi/pods/<pod>:8080/proxy/metrics" | grep '^pgsi_store_size_bytes'
```

The first command rules out the disk; the third reads the number the alert actually
uses, straight from the pod's own `/metrics` over the API-server proxy — which works
even though the image is distroless and has no shell, because the proxy needs no
`exec`. Compare it to 536870912 and to whatever `metrics.maxStoreBytes` is set to.

## Common causes

| Cause | How you tell |
|---|---|
| Retention longer than the volume allows | R3.14's `retention.statementSnapshots` (default 7 d) or R5.8's 30-day system retention outgrew the PVC; the store grows linearly with the retention window |
| `metrics.topN` raised | R9.5 caps per-query series at 50 per cluster by default (0–1000); raising it multiplies the query half of the store |
| A cluster with far more queries than the budget assumed | R9.4's ~1 200 series/cluster assumption holds only for a normal workload; a cluster with 50 000 distinct fingerprints breaks the estimate, not the cap |
| Downsampling not running | the store keeps raw rows it should have rolled up |
| `maxStoreBytes` overridden without updating the rule | first command shows a 1 GiB (or 5 GiB) volume while the rule divides by 512 MiB — the store's cap and the alert's denominator have diverged |

## Remediation

Short term: shorten the retention window, or lower `metrics.topN` toward its default
of 50 — R9.5 makes 0 a valid value that disables the per-query family entirely, which
is the cheapest way to reclaim space while keeping R9.4's aggregates.

Long term: raise `metrics.maxStoreBytes`, the rule's denominator, **and** the PVC
together. The store lives on the chart's RWO volume (R11.15) and the Deployment is
`Recreate` because that volume cannot attach to two pods, so a size change is a
recreate, not a rolling one.

## Escalation

`PGSIStoreAtCapacity` is paged; `PGSIStoreNearCapacity` is not. At 100 % the store
rejects writes and the collectors keep running, so the failure mode is silent data
loss with a healthy-looking pod — which is why the warning tier exists.

**Known defect:** the denominator should be templated from `metrics.maxStoreBytes`
rather than written literally in the rule. Until it is, check the configured cap
before believing the percentage.
