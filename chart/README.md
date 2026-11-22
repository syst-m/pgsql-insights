# pgsi — Helm chart

The chart in this directory is the primary distribution artifact (R11.6). It renders
one workload — the `pgsi` binary, which contains the API, the collector and the UI in
a single image (R11.1) — plus the RBAC, config, and optional monitoring and network
surfaces around it.

```
chart/
  Chart.yaml                 name pgsi, version 0.1.0, appVersion 0.1.0-dev, kubeVersion >=1.24.0-0
  values.yaml                the shipped default profile (R11.12)
  values-default.yaml        symlink to values.yaml, so -f values-default.yaml works
  values-production.yaml     documented delta (R11.6)
  values-lab.yaml            documented delta (R11.6, R13.6)
  templates/                 18 templates + templates/tests/test-connection.yaml
  .helmignore
```

`helm lint` passes on all three profiles; the rendered output of each is committed
under `deploy/plain-yaml/` as the golden file (R16.4) and CI fails on drift
(`make render-check`).

---

## Reference environment

R9.9 is tagged **[A]** — "validated against the reference environment's
kube-prometheus-stack". That is only falsifiable if the validated version is on
record, so it is on record here. Everything below was read from the live cluster on
**2026-11-21** (the same environment `docs/ENVIRONMENT.md` describes); the
`kubectl`/CRD reads behind the ServiceMonitor validation are reproduced in the next
section.

| Component | Version | How it was read |
|---|---|---|
| Kubernetes | `v1.37.2+k3s1` (k3s, 5 nodes, all amd64) | `kubectl version -o json` / `docs/ENVIRONMENT.md` |
| kube-prometheus-stack | **chart `66.1.1`**, app **`v0.78.1`** | `helm list -n monitoring` → release `prom-operator`, revision 6 |
| prometheus-operator | `0.78.1` | `metadata.annotations["operator.prometheus.io/version"]` on the `servicemonitors` and `podmonitors` CRDs |
| controller-gen (CRD generator) | `v0.16.4` | `metadata.annotations["controller-gen.kubebuilder.io/version"]`, same CRDs |
| CNPG operator | `1.30.0` | `docs/ENVIRONMENT.md` (`ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0`) |
| Prometheus CR | `prom-operator-kube-prometh-prometheus` in `monitoring` | `kubectl get prometheus -n monitoring -o json` |
| Grafana / Alertmanager / node-exporter | deployed by the same release | `kubectl get svc -n monitoring` |

R13.9's matrix additionally requires the chart to state its kube-prometheus-stack
floor: **0.7x+** (the spec's wording). The chart's own `kubeVersion` gate is
`>=1.24.0-0`, and nothing in `templates/` uses an API removed in 1.30+.

---

## Profiles (R11.6, R11.12)

One chart, three named configurations. `values.yaml` **is** the default: `helm
install pgsi ./chart` with no `-f` renders exactly what is in it. The other two are
**deltas** merged on top of it, not replacements — every key they change is commented
with the value it overrides.

| | `values.yaml` (default) | `values-production.yaml` | `values-lab.yaml` |
|---|---|---|---|
| Store | sqlite on a 1 Gi RWO PVC | sqlite on a 5 Gi RWO PVC | in-memory, no PVC (R11.6) |
| Auth (R8.8) | off | **on**, `pgsi-api-token` secret | off |
| `metrics.serviceMonitor` | on | on | **off** |
| `metrics.podMonitor` | off | off | off |
| `metrics.prometheusRule` | off | off | off |
| RBAC (R12.3) | namespaced | **`allNamespaces: true`** → ClusterRole | namespaced |
| Resources | 50m/96Mi → 500m/256Mi | 100m/128Mi → 1 CPU/512Mi | 25m/64Mi → 200m/128Mi |
| Ingress | off | on (`pgsi.example.com`) | off |
| NetworkPolicy | off | on | off |
| PDB | off | off | off |
| `helm test` hook | on | on | on |

The lab profile is the R13.6 path: single namespace, no PVC, no auth, no
ServiceMonitor, under 100 MB RAM, and it must install on a cluster with **no**
Prometheus operator. It gets there by setting `metrics.enabled: false`, which makes
all three `monitoring.coreos.com` templates render nothing — so a kind/minikube
cluster without those CRDs installs cleanly (this cell is `tested` in
`docs/compatibility-matrix.md`).

### Values namespace: `metrics:`, not `monitoring:`

R9.9 names the key `metrics.serviceMonitor.enabled`. Phase 0 shipped the block as
`monitoring:`. The spec's name won and the block is now `metrics:` in all three
values files and all four templates.

The rename is a **breaking values change** with no alias, and it is deliberately
one-sided: the chart is pre-1.0 and not yet published to a Helm repo (R11.9's
`helm repo add` is blocked on Q6, the registry org), so there is no install base to
migrate, and keeping two names for one requirement is the drift class R16.8 exists
to prevent. If you are carrying a Phase 0 values file, rename `monitoring:` →
`metrics:` and keep everything under it unchanged.

---

## The two-plane rule (R9.1)

R9.1 decides what `/metrics` is allowed to contain, and it is the reason this chart ships a
ServiceMonitor at all: **basic metrics → `/metrics` → Prometheus; query text, plan bodies,
active-session rows and blocking chains → API + store only.** Metrics carry the numbers; the
API carries the text. The rule's own reason is cardinality — a label per query is how a
5 000-query cluster buys a 40 GB TSDB — so the test for any capability is not "is this useful
in Prometheus" but "can this be a label at a price": a number that aggregates is a metric, a
string you would read is not.

R9.1 requires the tagging to be *in this file*, so it is here and not only in the root
README's metrics catalogue (`README.md:178`). Every capability the spec names, with the plane
that serves it:

| Capability | Plane | Req | Why that plane |
|---|---|---|---|
| Aggregate load — execution time, rows, shared hits/reads, lock time per (cluster, db, user) | `/metrics` | R9.4 | ~1 200 series/cluster; these four numbers are the export's whole purpose |
| Per-query numbers, **top-N only** | `/metrics` | R9.5, R9.5b | 300 series; `metrics.topN` is what makes `fingerprint` an affordable label — uncapped it is the 40 GB case |
| Sampled latency histogram | `/metrics` | R9.13 | 450 series; 50 × (8 buckets + `_sum` + `_count`) |
| Per-tag numbers | `/metrics` | R9.6 | tags × databases; gated on R3.12 for the same reason — every tag is a label |
| System metrics (the full R5.2 set, plus parsed `cnpg_*`) | `/metrics` | R9.7 | ~4 000 series |
| Pooler utilisation | `/metrics` | R6.1 | ~200 series |
| Self-observability (the eight R9.8 series) | `/metrics` | R9.8 | ~120 series |
| `pgsi_build_info{version,commit}` | `/metrics` | R9.8, R11.16 | the one place a *string* is legitimately a metric: one series, two labels, the Prometheus idiom for "which build answered" |
| Instance liveness | `/metrics` | R9.12 | `pgsi_up`, one series per instance; the API answers the same question as a row in `/api/v1/clusters` and both are correct |
| Instance identity (`namespace`, `cluster`, `pod`, `instance_role`) | **both** | R9.3 | on the metrics plane it is a *label set* carried by every series; on the API plane it is a *record*. One value in two shapes, not a third metric |
| Databases / Users / Client-address rollups | **both** | R3.7 | the numbers ride R9.4's `datname` / `usename` labels; the rows are R8.2's |
| Query text (normalized statement) | API + store | R3.2, R8.2 | the 40 GB case verbatim; R9.5b's `fingerprint` is its metrics-plane proxy |
| Plan bodies (`EXPLAIN (FORMAT JSON)` samples) | API + store | R3.10, R8.2 | a plan is a document; R9 has no plan family and R9.1 forbids one |
| Active-session rows | API + store | R4.1, R8.2 | pid / state / query per session; the metrics plane carries one series per *instance*, not per session |
| Blocking chains | API + store | R4.4, R8.2 | a graph edge from `pg_blocking_pids()`; not a number. R9.4's lock time is the aggregate that pages you to read it |
| Terminate session | API only | R4.5 | the API plane also owns *actions*, which a read-only export cannot express at all |
| Wait-event breakdown | API + store | R4.3, R8.2 | see the note below |
| Recommendations | API only | R7.6 | finding + evidence + action + `confidence` is a document; its *evidence* cites metrics, which is not the same as being one |
| Events timeline | API only | R8.2, R10.3 | |
| Search (`/search?q=`) | API only | R8.2, R10.17 | searching text requires the text, so it is unreachable from the metrics plane by construction |
| Pooler config (`poolMode`, pool sizes) | API only | R6.5 | config is a document; its utilisation is the metric (R6.1) |

**The one capability whose numbers are not on the metrics plane.** Wait events are where the
rule's two halves come apart, so the exception is named rather than left for a reader to
object to. R4.3's `wait_event_type` × `wait_event` rollup is served by
`/insights/wait-events` (R8.2) because R9's family set has no wait-event member — the
breakdown is text-plane even though it is a count. The only wait number that reaches
`/metrics` is `cnpg_backends_waiting_total`, re-exported under `pgsi_` by R9.7's `cnpg_*`
clause, and R4.3 records that CNPG's own metric is a single count with no breakdown. So
R10.9's donut is fed by the API, not by Prometheus — which is the rule working as intended,
not an oversight: one label per wait event is the same cardinality purchase at a smaller scale.

### What the chart ships per plane

The split is also the fastest way to read `templates/`: each template serves one plane or the
other, and the fact that two planes need one pod and one port is R11.1's point.

| Template | Plane |
|---|---|
| `templates/servicemonitor.yaml`, `templates/podmonitor.yaml` (R9.9) | metrics — the scrape surface for the rows above |
| `templates/prometheusrule.yaml` (R9.11) | metrics — an alert is PromQL, so it can only read this plane |
| `templates/service.yaml` | **both** — one `http` port serves `/metrics` and `/api/v1/*`; R8.9 splits their auth (`--auth-metrics` vs `--auth`) on that single port |
| `templates/deployment.yaml`, `templates/configmap.yaml` | **both** — one env block carries both planes' flags |
| `templates/pvc.yaml` | API — the store exists to hold the text the metrics plane refuses |
| `templates/ingress.yaml`, `templates/networkpolicy.yaml`, `templates/pdb.yaml`, `templates/tests/` | API — the client-facing surface |
| `templates/role.yaml`, `rolebinding.yaml`, `clusterrole.yaml`, `clusterrolebinding.yaml`, `serviceaccount.yaml`, `extra-manifests.yaml`, `_helpers.tpl`, `NOTES.txt` | neither — RBAC and rendering plumbing, plane-agnostic |

---

## ServiceMonitor and PodMonitor (R9.9)

`templates/servicemonitor.yaml` and `templates/podmonitor.yaml`. Both are gated on
`metrics.enabled` **and** their own `.enabled`, so the flag's zero value is *off*
(R9.9: "off by default") and a profile opts in. Enabling both at once scrapes the
same `/metrics` twice and doubles the bill below — the chart allows it (R11.7 ships
the variant) but the profiles do not.

### Field-level validation against the installed CRD

R9.9 says "correct for the installed `monitoring.coreos.com/v1` API". Checked
field-by-field against the CRDs as installed on the reference cluster, not against
the upstream docs:

```
kubectl get crd servicemonitors.monitoring.coreos.com -o json
kubectl get crd podmonitors.monitoring.coreos.com    -o json
```

Both CRDs expose exactly one version — `v1`, `served: true`, `storage: true` — so
`apiVersion: monitoring.coreos.com/v1` is the only correct spelling and there is no
`monitoring.coreos.com/v1alpha1` to accidentally target.

| Emitted field | In the CRD schema? | Notes |
|---|---|---|
| `ServiceMonitor.spec.selector` | yes — **required** | `spec.required: [endpoints, selector]` |
| `ServiceMonitor.spec.endpoints` | yes — **required** | present, one element |
| `spec.endpoints[].port` | yes | must be a **string** naming a Service port; `http` from `templates/service.yaml`. A numeric port here is a schema error — that is `targetPort` |
| `spec.endpoints[].path` | yes | from values |
| `spec.endpoints[].interval` | yes | from values |
| `spec.endpoints[].scrapeTimeout` | yes | from values; the CRD does not enforce `<= interval`, so the chart cannot either |
| `spec.endpoints[].tlsConfig` | yes | `ca`, `caFile`, `cert`, `certFile`, `insecureSkipVerify`, `keyFile`, `keySecret`, `maxVersion`, `minVersion`, `serverName` — all accepted |
| `spec.endpoints[].relabelings` | yes | from values |
| `spec.endpoints[].metricRelabelings` | yes | from values |
| `spec.namespaceSelector.matchNames` | yes | `any` and `matchNames` are the only two children |
| `PodMonitor.spec.selector` | yes — **required** | `spec.required: [selector]` |
| `PodMonitor.spec.podMetricsEndpoints` | yes | not required, always emitted |
| `podMetricsEndpoints[].port` | yes | names the **container** port `http` from `templates/deployment.yaml` |
| `podMetricsEndpoints[].path` | yes | **added in Phase 4** — Phase 0 hardcoded `/metrics` here while the ServiceMonitor read it from values |
| `podMetricsEndpoints[].tlsConfig` | yes | **added in Phase 4** |
| `podMetricsEndpoints[].relabelings` | yes | **added in Phase 4** |
| `podMetricsEndpoints[].metricRelabelings` | yes | **added in Phase 4** |

No field the installed CRD rejects. The two gaps this pass closed were both on the
PodMonitor: a hardcoded `path` and a reduced knob set relative to the ServiceMonitor
(the CRD accepts the same set on both, so the "variant" is now a variant in
*mechanism*, not in capability).

Fields the CRD offers that the chart deliberately does **not** expose:
`spec.jobLabel`, `spec.podTargetLabels`, `spec.targetLabels`, `spec.sampleLimit`,
`spec.targetLimit`, `spec.bodySizeLimit`, `spec.labelLimit`, `spec.scrapeClass`,
`spec.attachMetadata`, `spec.scrapeProtocols`, and per-endpoint `honorLabels`,
`honorTimestamps`, `params`, `basicAuth`, `authorization`, `oauth2`, `proxyUrl`,
`enableHttp2`, `followRedirects`, `filterRunning`, `trackTimestampsStaleness`.
R9.9 asks for a correct ServiceMonitor, not for the whole CRD as values surface;
`extraManifests: []` is the escape hatch for anyone who needs them, and adding a
key here later is additive.

### Making it discoverable

A ServiceMonitor that exists is not a ServiceMonitor that is scraped. The local
Prometheus decides via `spec.serviceMonitorSelector`, and the reference cluster's
says:

```yaml
# prometheus/prom-operator-kube-prometh-prometheus
spec:
  serviceMonitorSelector: {matchLabels: {release: prom-operator}}
  podMonitorSelector:     {matchLabels: {release: prom-operator}}
  ruleSelector:           {matchLabels: {release: prom-operator}}
  serviceMonitorNamespaceSelector: {}   # all namespaces
  podMonitorNamespaceSelector:     {}   # all namespaces
```

kube-prometheus-stack's convention is `{release: <release-name>}`; this cluster's
release is named `prom-operator`, so the label is `release: prom-operator`. The
chart's default `additionalLabels: {}` produces a ServiceMonitor carrying only the
`app.kubernetes.io/*` and `helm.sh/chart` labels — which that Prometheus ignores.
On the reference cluster, install with:

```bash
helm install pgsi ./chart -n pgsi \
  --set metrics.serviceMonitor.additionalLabels.release=prom-operator
```

`{}` is the right default and not a bug: it is correct on a cluster with a
permissive selector, and the correct label is a property of *their* Prometheus
release name, which the chart cannot know. It is, however, the difference between
R9.9 passing and failing in the reference environment, which is why it is written
down here rather than only in a values comment.

The same selector applies to `PrometheusRule` (R9.11) — an alert set with no
`release: prom-operator` label loads nothing.

---

## Cardinality budget (R9.10)

R9.10: document §5's budget in the chart README **with a measured number for the
reference environment**, so an adopter can predict their Prometheus bill before
installing. Three numbers, each labelled with how it was obtained — because "the
spec says 23.7 k" and "we measured it" are different claims and the spec's own two
figures disagree.

### 1. The budget (§5), computed for the reference cluster set

Per-cluster series, from the §5 family table:

| Family | Requirement | Series / cluster | Interval | Control |
|---|---|---|---|---|
| Aggregate | R9.4 | ~1 200 | 60 s | always on |
| Per-query (top-N) | R9.5 | 50 × 6 = **300** | 60 s | `metrics.topN` 0–1000, per cluster; N=0 disables |
| Sampled latency histogram | R9.13 | 50 × (8 + `_sum` + `_count`) = **450** | 60 s | ships with R3.9 |
| Per-tag | R9.6 | tags × databases | 60 s | off unless R3.12 enabled |
| System | R9.7 | ~4 000 | 60 s | always on |
| Pooler | R6.1 | ~200 | 60 s | only where a Pooler exists |
| Self | R9.8 | ~120 | 60 s | always on |

Per cluster (excluding the pooler row): `1 200 + 300 + 450 + 4 000 + 120 =` **6 070**.
The fixture cluster set (`testdata/api/clusters.json`) is the four reference
clusters, exactly one of which has a Pooler (`ai-api-gateway/rev-proxy-pg`; the
pgbouncer scrape is checked in at
`testdata/cnpg-metrics/ai-api-gateway__rev-proxy-pg-pooler-86b4bdd84d-n9vlv__9127.txt`):

> **4 × 6 070 + 200 = 24 480 series.**
> At ~1.5 bytes/sample after compression and a 60 s scrape (43 200 samples per
> series over 30 d): **24 480 × 43 200 × 1.5 B ≈ 1.59 GB over 30 days.**

**The spec states two different numbers for this.** §5's table sums to 24 280 and
its prose says "24.3 k series … 1.58 GB"; R13.4's headline says "~23.7 k series /
~1.5 GB". Both pairs are internally consistent (23 700 × 43 200 × 1.5 B = 1.54 GB;
24 300 × 43 200 × 1.5 B = 1.58 GB), so this is not a rounding artifact — it is two
generations of the same estimate, and §5's parenthetical claims to have corrected
R13.4 while R13.4 still carries the older figure. The chart README uses the number
the table actually computes (**24 480**, including the pooler row the prose mentions
but the sum omits) and files the discrepancy rather than silently picking a side.
R13.4's CI script (`scripts/cardinality-check.py`) is the artifact that settles it;
when it lands, its printed number replaces the hand-computed one here.

### 2. Measured on the reference environment today

What the reference Prometheus actually holds for those same four namespaces, read
live on 2026-11-21 via the apiserver proxy
(`…/services/prom-operator-kube-prometh-prometheus:9090/proxy/api/v1/query`):

| Namespace | Series | Distinct metric names | `kube_*` (kube-state-metrics) | `container_*` (cadvisor) |
|---|---|---|---|---|
| `ai-api-gateway` | 4 630 | 261 | 897 | 1 182 |
| `cookbook` | 1 169 | 173 | 511 | 552 |
| `freeidp` | 1 004 | 209 | 402 | 411 |
| `open-webui` | 1 336 | 183 | 501 | 710 |
| **total** | **8 139** | **826** | **2 311** | **2 855** |

Two things an adopter should be able to read off this table:

* **The Postgres exporter contributes 0 series today.** `count({__name__=~"pg_.*",
  namespace="ai-api-gateway"})` is empty on all four namespaces — CNPG's instance
  manager serves `:9187` (R1.10, `docs/ENVIRONMENT.md`), but no ServiceMonitor for it
  matches `release: prom-operator`, so the reference Prometheus never scrapes it.
  Our 24 480 is not additive on top of an existing Postgres metric set there; it is
  the first one.
* **Our export is ~3× the cost of everything kube-state-metrics and cadvisor
  already pay for those namespaces combined** (24 480 vs 5 166). That is the honest
  framing of "predict your Prometheus bill": the §5 budget is real money relative to
  what a namespace normally costs, and `metrics.topN` (R9.5) plus the R9.6 per-tag
  family are the levers, not the scrape interval.

### 3. Measured for our own `/metrics`

Not yet available. The families above are Phase 4–8 collector output; the binary
serves `/metrics` from Phase 5 onward (R9.12's `pgsi_up` is the first series that
exists before then). Until then the chart README's promise is the computed budget
plus the measured baseline, and R13.4's assertion is the artifact that converts the
first into a measured number. This is the same dependency that makes R9.6 and R9.8
deferred to Phase 2's `/metrics` rather than validated now.

---

## Alert rules (R9.11) and runbooks (R14.4)

`templates/prometheusrule.yaml` renders `metrics.prometheusRule.groups` verbatim and
is off in all three profiles. R9.11's rule set (≥10 rules, a `critical` tier CNPG's
own `cnpg-default-alerts` lacks, each rule carrying `summary` / `description` /
`runbook_url`) is authored in `chart/values.yaml` by the Phase 4 Go half; the
`runbook_url` values resolve to `docs/runbooks/` (R14.4), where the runbooks
themselves live. Remember the `release: prom-operator` label above — a rule set
without it loads nothing.

---

## Installing

```bash
# from a source checkout
helm install pgsi ./chart -n pgsi --create-namespace

# from a packaged chart (the verifiable path while Q6 is open, R11.9)
helm package chart -d dist/
helm install pgsi dist/pgsi-0.1.0.tgz -n pgsi --create-namespace

# production profile, on the reference cluster
helm install pgsi ./chart -n pgsi --create-namespace \
  -f chart/values-production.yaml \
  --set metrics.serviceMonitor.additionalLabels.release=prom-operator
```

`Chart.yaml`'s `version` is the chart version and `appVersion` the binary version
(R11.16's policy: the release pipeline bumps `version`, never a developer by hand
during development; CI stamps the image tag from the git SHA).

**Uninstall** (R11.10): `helm uninstall pgsi -n pgsi` removes the cluster-scoped
ClusterRole and ClusterRoleBinding under the same `rbac.allNamespaces` gate that
created them, so a namespaced install leaves no cluster-scoped residue. The PVC is
retained by Helm's own semantics; delete it explicitly to drop the store.

**Verify** (R11.14): `make verify` port-forwards `svc/pgsi` and curls `/healthz`,
`/api/v1/version` and `/metrics`; `helm test pgsi` runs the same check through the
`chart/templates/tests/test-connection.yaml` hook.

---

## Golden renders (R16.4)

`deploy/plain-yaml/all.yaml`, `all-production.yaml`, `all-lab.yaml` are generated
files — the committed `helm template` output for the three profiles, and the golden
test R16.4 asks for. `make render` regenerates them; `make render-check` regenerates
into a temp file, moves it into place, and fails on any `git diff`. A template change
committed without its re-rendered manifests fails CI, so the manifests move in the
same commit as the template. `deploy/kustomize/kustomization.yaml` consumes the same
files for kustomize users (R11.13).
