# An exporter scrape is failing

**Alert:** `PGSIScrapeTargetUp` → `PGSIScrapeTargetDown` · **severity:** `warning` · **for:** `15m`
**URL:** `https://github.com/frankellis/pgsql-insights/blob/main/docs/runbooks/pgsi-scrape-target-down.md`

```promql
pgsi_scrape_target_up == 0
```

## What it means

`pgsi_scrape_target_up` is one of R9.8's self-observability metrics and it answers a
narrow question: **can we reach an exporter's HTTP endpoint and parse it.** It is not
`pgsi_up` (R9.12), which answers whether our collection round for an instance
succeeded, and it is not Prometheus's `up`, which answers whether Prometheus reached
us. Three different liveness questions, three different metrics, and confusing them is
the most common way this alert gets misdiagnosed.

The distinction that matters operationally: `pgsi_scrape_target_up == 0` with
`pgsi_up == 1` means the round succeeded but one exporter inside it did not — a
partial failure that degrades one metric family while the rest stay fresh. That is
exactly the case a per-instance `pgsi_up` cannot express, which is why R9.8 carries
the metric separately.

## First three commands

```sh
# Which target, and on which port? The label set names it.
curl -s localhost:8080/metrics | grep '^pgsi_scrape_target_up'
# Does that endpoint answer when you ask it directly?
kubectl get --raw "/api/v1/namespaces/<ns>/pods/<pod>:9187/proxy/metrics" | head -5
# Is the port even the one the chart's transport table expects? (R1.10)
kubectl get pod -n <ns> <pod> -o jsonpath='{.spec.containers[*].ports}{"\n"}'
```

## Common causes

| Cause | How you tell |
|---|---|
| The CNPG instance manager's `:9187` exposition is down | the direct proxy call fails while the pod is `Running 1/1` |
| A port the transport table does not know | R1.10's per-port table is the single source for which port serves what; a pod exposing a port that is not in the table is scraped at the wrong URL |
| The exposition does not parse | the endpoint answers 200 with a body our parser rejects — R2.6's `cnpg_*` parsing contract; a malformed line fails the whole scrape, not one metric |
| Auth on the exporter | 401 from the proxy call; `--auth-metrics` governs our own `/metrics`, not the exporter's |
| Pod terminating | the target is briefly unreachable during every rollout; the 15m `for:` suppresses it |

## Remediation

Confirm the endpoint by proxying to it directly — that is the only command that
distinguishes "we cannot reach it" from "it is answering something we cannot parse".
The second command's output is the answer: a 200 with valid exposition means the
fault is in us, and a parse failure there is a bug worth filing rather than an
incident to work around.

## Escalation

Not paged. It becomes an incident when it pairs with `PGSICollectorErrors` on the same
instance, which means the round is now failing, not just degrading.
