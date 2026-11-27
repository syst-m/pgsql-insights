# Phase 4 — ServiceMonitor/PodMonitor, PrometheusRule, cardinality assertion, live scrape

## Status

🔄 In progress (A2 + A4). No deviation rows in [deviations.md](../deviations.md) carry phase `4`:
nothing here is done-with-deviations, it is done-but-not-closed. One requirement, R9.11,
transferred to Phase 5 and is tabled there.

## Owners

**A4 (chart half)** — `chart/README.md`, `chart/templates/servicemonitor.yaml`,
`chart/templates/podmonitor.yaml`, `chart/templates/prometheusrule.yaml`, `chart/values.yaml`,
`chart/values-default.yaml`, `chart/values-lab.yaml`, `chart/values-production.yaml`, `README.md`.
**A2 (metrics half)** — `Makefile`, `scripts/cardinality-check.py`, `.github/workflows/ci.yml`.
Plus the three `deploy/plain-yaml/` goldens and `internal/metrics/metrics.go`, which R9.8's row
names but Phase 2 produced.

## Task breakdown

§3.9 carries no task table; Phase 4's WBS row in the [README](../README.md) is the task list. The
section was written as an inventory reconciliation, and that reconciliation is in the R14.8 section
below.

## Validation entry (R14.2)

Full text at [IMPLEMENTATION.md](../../../IMPLEMENTATION.md) §3.16; the short version:

- **Scope, stated as a file list rather than as "Phase 4 reviewed."** The phase's sixteen tracked
  files are the ones its nine requirement rows name (the owner split is in **Owners** above). R9.6
  and R13.3 name no file at all: both are blocked. The three `chart/values-*` profiles are R16.4's:
  `requirements.md:524` names them as one brace token and excludes `values.yaml`, which is the base
  rather than a profile. The first draft of this entry took its list from §3.9's R16.4 cell, which
  named only `values.yaml`; A3's `2eebbb0` added the three paths to that cell, and this entry adds
  the matching rows so the row and the entry stop disagreeing (R14.9).
  `chart/values-default.yaml` is a symlink to `chart/values.yaml`, mode `120000` where lab and
  production are `100644`, and its row says so rather than reading it as a second set of values.
- **The composition, from §3.9's table.** Of the nine ids the WBS row claims, five are implemented
  (four by A4 — R9.1, R9.9, R9.10, R16.4 — and one by A2: R13.4's assertion half, the live-scrape
  half being Phase 2's), one is implemented by another phase and tabled there (R9.11, Phase 5,
  `f9aae41`, §3.7), and three are blocked (R9.6 on R3.12/Phase 6b, R9.8 on Phase 2's two remaining
  self-metrics, R13.3 on a latency artifact that does not exist).
- **The R9.11 transfer, moved out of the WBS row's Validation entry cell.** R9.11 was taken in
  Phase 5, not here: `chart/templates/prometheusrule.yaml` and the `make rules-check` target shipped
  in `f9aae41` and are tabled in [phase-5.md](phase-5.md)'s R9.11 row, because A4 held the
  ServiceMonitor/PodMonitor half of this phase and the rule set rode out with it. R14.9's per-owner
  rule needs this row to acknowledge the transfer rather than keep claiming a file another phase
  committed — an unmarked ⬜ next to a requirement that is implemented is how a requirement gets
  implemented twice.
- **Findings column.** Generated from the finding index's `against` cell, not from a substring match
  on the line. Over these sixteen files the set is two rows: S107 (`against`
  `chart/values-lab.yaml:64`, filed in this review, fixed by `f8cb141`) and S3 (`against`
  `chart/values-production.yaml` and `cmd/pgsi/main.go`, filed in `f08b6ac` and §5-D18, status
  `open — forwarded` with its code half assigned to "A2's Phase 5"). Three findings name a file in
  this table inside their finding *text* without their `against` cell naming it, and they are listed
  so the rule is checkable rather than asserted: S73 mentions `scripts/cardinality-check.py` but its
  `against` cell is `requirements.md:554`; S104's is `docs/agents.md:58` and
  `.claude/agents/a2-api-metrics.md:13`; S108's is ``1b11199:IMPLEMENTATION.md:1259`. The `Round that read
  it` column is the `filed in` value of those same rows and `—` for the fourteen files with no
  findings row; the column was absent from this table's first draft (the 3-column form at
  `63e05bc`).

| File | Round that read it | Findings filed | Last commit touching it |
|---|---|---|---|
| `chart/README.md` | — | — | `6573ed1` |
| `README.md` | — | — | `0b440df` |
| `chart/templates/servicemonitor.yaml` | — | — | `434134d` |
| `chart/templates/podmonitor.yaml` | — | — | `434134d` |
| `chart/values.yaml` | — | — | `5506d78` |
| `chart/values-default.yaml` (symlink to `chart/values.yaml`, mode `120000`) | — | — | `db4f237` |
| `chart/values-lab.yaml` | this review | S107 | `92ebabf` |
| `chart/values-production.yaml` | `f08b6ac`, IMPLEMENTATION.md §5 §5-D18 | S3 | `434134d` |
| `chart/templates/prometheusrule.yaml` | — | — | `ca9ef4e` |
| `Makefile` | — | — | `7a6a13b` |
| `deploy/plain-yaml/all.yaml` | — | — | `92ebabf` |
| `deploy/plain-yaml/all-lab.yaml` | — | — | `92ebabf` |
| `deploy/plain-yaml/all-production.yaml` | — | — | `92ebabf` |
| `scripts/cardinality-check.py` | — | — | `1be97e2` |
| `.github/workflows/ci.yml` | — | — | `0de8112` |
| `internal/metrics/metrics.go` | — | — | `04169bf` |

## Requirement → file → status (R14.8)

Phase 4's WBS row carried a caveat and no table behind it, which is the failure mode R14.8 names: an
unmarked ⬜ next to a requirement that is implemented is how that requirement gets implemented twice.
The inventory was reconciled before anything was written, and it changed what the phase needs — of
the nine ids the row claims, five are implemented by this phase's own work, one is implemented by
another phase and tabled there, and three are blocked. The row is now 🔄. Appended after §3.8 rather
than inserted before it, so no section another owner holds gets renumbered. Where a file is A4's or
A1's, the row validates their file against the requirement instead of re-implementing it.

| Req | Requirement (abridged) | File(s) | Status |
|---|---|---|---|
| R9.1 | The two-plane rule — every capability tagged with the plane that serves it, **documented in the chart README**: numbers → `/metrics` → Prometheus; query text, plan bodies, active-session rows, blocking chains → API + store only | `chart/README.md` § "The two-plane rule (R9.1)" (:93, A4, `ed58452`), `README.md:170-181` (A4) | **implemented (A4)** — the rule is now documented in the file R9.1 names. `chart/README.md` § "The two-plane rule (R9.1)" states it in R9.1's own wording, adds the test the rule implies (*can this be a label at a price*, not *is this useful in Prometheus*), and tags every capability the spec names rather than only the four R9.1 lists — the R9.4–R9.13 families, the R8.2 endpoint set, and the R3/R4/R6/R7 capabilities, with two rows tagged `both` and justified (R9.3's identity is a label set on one plane and a record on the other; R3.7's rollups are numbers on labels and rows in the API). It names the one capability whose numbers are not on the metrics plane — R4.3's wait-event rollup is served by the API because R9's family set has no wait-event member, and the only wait number that reaches `/metrics` is `cnpg_backends_waiting_total`, re-exported under `pgsi_` by R9.7's `cnpg_*` clause — and maps all 18 templates to the plane each serves (`:141`). **History:** the row read "satisfied, one clause short" for one round, because the rule was documented in `README.md:170-181` and cited by two runbooks (`docs/runbooks/pgsi-lock-wait.md:30`, `docs/runbooks/pgsi-primary-unreachable.md:56`) but absent from the named file — the half R9.1's wording actually asks for. Recorded at `dc40d70`, owner-notified at `8d59b7d`, closed by A4 in `ed58452` |
| R9.6 | Export the **per-tag** family (Cloud SQL's `insights/pertag`) when R3.12 tags are enabled, keyed by `tag_hash` | none yet; §5's table row marks it *off unless R3.12 enabled* | **blocked** — (Phase 6b) the family's own Control cell in §5 gates it on R3.12, and R3.12 (tags) is Phase 6b's, so there is no `tag_hash` to key on until tags exist. Nothing in the chart blocks it: the family rides the same `/metrics` endpoint the ServiceMonitor already scrapes, so it needs an emitter and no plumbing. `scripts/cardinality-check.py` reads the cell as `variable` and contributes 0 at default config, which is the honest answer rather than an invented number |
| R9.8 | Export **self-observability**: the eight named self-metrics — `pgsi_build_info{version,commit}`, `pgsi_collector_duration_seconds`, `pgsi_collector_errors_total`, `pgsi_scrape_target_up{cluster,pod}`, `pgsi_store_size_bytes`, `pgsi_http_request_duration_seconds`, `pgsi_scheduler_queue_depth`, `pgsi_scheduler_shed_total` (R2.9) | `internal/metrics/metrics.go:90` (A2, `0f4b154`; the func moved to `c8f2024:internal/metrics/metrics.go:108`, and the cell lists both addresses rather than overwriting the old one — S52's rule, the same one this cell applies to `server.go:313`) for 6 of the 8 — the row's previous cite `internal/api/server.go:313` (A1) survives its own deletion: that line is now a `return` inside `Run`'s Serve goroutine (`internal/api/server.go:309-316`), and `BuildInfoMetric()` moved to the hub in the same commit (S52's rule — list both addresses rather than overwrite the old one); `internal/metrics/http.go` and `internal/api/metrics_mw.go` (A2, `7a8e158`) and `internal/metrics/store.go` (A2, `c9df249`) join it for the two families those commits added | **in-progress** — (Phase 2's half; the row itself is Phase 4's) 6 of the 8 render at `aee9046`, and the count is now stated per family with its commit because the single-number form — `4 of the 8 render as of 0f4b154`, which is what this cell said and was true of `0f4b154` alone — moved twice in two days (S75). `pgsi_build_info`, `pgsi_collector_duration_seconds`, `pgsi_collector_errors_total` and `pgsi_up` from `0f4b154`; `pgsi_http_request_duration_seconds` from `7a8e158` (`internal/metrics/http.go`, with its api-side middleware in `internal/api/metrics_mw.go`, wired into `Server.Handler` at `aee9046`); `pgsi_store_size_bytes` from `c9df249` (`internal/metrics/store.go`). `server.go` exported `BuildInfoMetric` "so the Phase 2 MetricHub renders the same line rather than a second copy of it", and the hub now exists, so the api package keeps the HTTP half (the content type, the status, the one call at `aee9046:internal/api/server.go:480-482`; the cell cited `internal/api/server.go:416`, where the same call sat at `c8f2024` — S52's rule again; A3's S76: the cell had written that second address as a bare line number, so the checker inherited the file from this line's first citation — `internal/metrics/metrics.go`, 307 lines — read it as line 416 of that file, reported it past the end, and that single entry was the build failure) and no longer owns a rendered metric line. `metrics.Hub.Render()` composes `BuildInfo()` (R11.3's pair, read from the ldvars at render time so the metric and `/api/v1/version` cannot disagree), `collector.Stats.Render()` (R9.2/R9.3's pair), `collector.UpTracker.Render()` (R9.12's `pgsi_up` — the family this row's own list spells `pgsi_scrape_target_up{cluster,pod}`, ruled at `requirements.md:403`), R9.5's per-query family passed in as `WithPerQuery` over the `PerQueryFamily` interface at `internal/metrics/metrics.go:127` (`c8f2024`) — declared in the consuming package because importing `internal/store` from here closes the cycle `api→metrics→store→api` — and `WithHTTPRequests` (`7a8e158`). R9.5's family is not one of the eight this row enumerates, so the count above counts the eight only. The two that remain — `pgsi_scheduler_queue_depth`, `pgsi_scheduler_shed_total` — are both R2.9's and both blocked on the subsystem rather than on a metric: they need R2.9's global budget with its shed counter, which does not exist yet, so there is no queue depth to report and nothing to increment. They are documented pending in the package's own here/pending table rather than stubbed, because a family with no series is absent from a valid exposition and `promtool check metrics` reports the shape a stub produces. The store family's second, smaller gap — the `WithStoreSize` half of that same call site, which this cell described as blocked on `api.Config` carrying no sink field, A1's half — closed in `22023a3`, the commit A1 sent for exactly this sentence: the field is `internal/api/server.go:235` (`StoreSize metrics.StoreSizer`) and the call that passes it, listed above as `aee9046:internal/api/server.go:480-482` and `c8f2024:internal/api/server.go:416`, is at `22023a3:internal/api/server.go:544-548` and at HEAD `898c7c0` (S52's rule keeps all three addresses with the rev each one belongs to rather than overwriting the earlier two). `cmd/pgsi/main.go:509` assigns it the sink the scheduler already has — the `collector.NewMemSink(0)` at `cmd/pgsi/main.go:497` — inside the discovery branch, so `--discover=false` leaves it nil and the family absent, which is what that branch already answers for the scheduler's two families. The sink type exists for both store profiles: `internal/metrics/store.go`'s `StoreSizer` is satisfied by `collector.MemSink` (`c9df249`) and, since `f634142` added `Used() int64` at `internal/store/retention.go:346`, by `*store.Store` as well (A5's report, verified) — so the renderer and the Config field both exist now, and what a given server can still lack is a non-nil sink. **Cell wording:** A2 proposed `**partial**` for this cell; the R14.8 enum has no `partial` (the gate fails it — `IMPLEMENTATION.md:710 R9.8: '**partial** …' non-canonical` is the gate's output at the rev where the cell read `**partial**`, an exhibit of that round per S52, not this rev's message), so the cell reads `in-progress` |
| R9.9 | Ship a **ServiceMonitor** (and a **PodMonitor** variant) in the chart, off by default and enabled by `metrics.serviceMonitor.enabled`, correct for the installed `monitoring.coreos.com/v1` API | `chart/templates/servicemonitor.yaml`, `chart/templates/podmonitor.yaml`, `chart/values.yaml:257`/`:259`, `chart/README.md` § "ServiceMonitor and PodMonitor (R9.9)" (:158) (A4) | **implemented (A4)** — both templates exist and R11.7's validation is documented at `chart/README.md` § "Field-level validation against the installed CRD" (:166) (field-level validation against the installed CRD, including the `scrapeTimeout ≤ interval` ratio the CRD accepts any duration string so the chart cannot enforce). Two subtleties the file rules on rather than glosses: **"off by default" vs the profile's `enabled: true`** — the flag's zero value is off (the template gates on a truthy `.enabled`, so a values file that omits the block renders nothing) and the shipped profile opts in explicitly (`chart/values.yaml:257-260`); and the **values namespace rename** — R9.9 names `metrics.serviceMonitor.enabled`, Phase 0 shipped `monitoring:`, the spec's name won, and `chart/README.md:78` records the rename instead of leaving two spellings alive |
| R9.10 | Document the cardinality budget (§5) in the chart README **with a measured number for the reference environment**, so an adopter can predict their Prometheus bill before installing | `chart/README.md` § "Cardinality budget (R9.10)" (:254) (A4) | **implemented (A4)** — three sections: the budget computed for the reference set (`:262`), measured on the reference environment today (`:302`), and measured for our own `/metrics` (`:330`). The third is "Not yet available" and the file says so, naming the same dependency this table records for R9.6 and R9.8 — the binary exports one metric. The computed figure is now `271b7bb`'s script output; `chart/README.md:287-293` predicted that script and its published number (24 480) is unchanged by it, which is the only outcome that would not have made the file wrong |
| R9.11 | PrometheusRule with a `critical` tier — Phase 4 carryover | `chart/templates/prometheusrule.yaml`, `Makefile` (`rules-check`), shipped `f9aae41` | **tabled** — (Phase 5) tabled in §3.7's R9.11 row (16 rules, provisional, `promtool check rules` reports 16 found, no rule has fired on a real sample). Not re-implemented here; the WBS row's caveat and §3.7 now agree on it, which is what R14.9's per-owner rule asks for |
| R13.3 | API p50 **< 100 ms** for cached endpoints, p95 **< 400 ms**; first UI paint **< 1.5 s** (R10.14) | none | **blocked** — no measurement artifact exists. `scripts/` holds seven scripts and none measures latency (`build-push-dev.sh`, `check-openapi.py`, `check-parity-matrix.py`/`.sh`, `cardinality-check.py`, `gen-licenses.py`, `image-size.py`), and `web/e2e` asserts rendered state, not timing. The requirement's precondition is Phase 4's other half — "live scrape by the reference Prometheus" — i.e. a running deployment behind a Service, which needs Phase 2's `pgsi_up` and Phase 6b's cached endpoints. When it lands it also needs a Makefile target, because R11.11 forbids CI running a command the Makefile lacks |
| R13.4 | The cardinality budget per §5 **with a CI-checkable assertion** that the exported series count for a fixture cluster set stays under the documented bound | `scripts/cardinality-check.py` (`271b7bb`), `Makefile` (`cardinality-check`), `.github/workflows/ci.yml` (lint job); the budget itself is documented in `chart/README.md`, one row up | **implemented** — the assertion half, written here. It computes from §5's family table × `testdata/api/clusters.json` rather than carrying a literal, and its computed figure is **24 080 series / 1.56 GB over 30 d** — the cell read **24 480 series / 1.59 GB** until 2026-11-24, which is what the script computed at `271b7bb` from the table's pre-S25 per-query row of 300 (S52's rule: name both, the older figure is an exhibit of the rev that produced it, not a typo). The gate is `max(published)`, so the governing bound stays chart/README.md's 24 480 and R13.4's own 23 700 is the oldest of the four; §5's prose — 24 300 until this round, now amended to the script's 24 080 — is the fourth (`requirements.md:558-562` records that generation's own correction), and requirements.md is A3's file to amend, so the script reports all four figures and gates on the highest. The live-count form of the same assertion — scrape `/metrics`, count series — is Phase 2's, once the MetricHub emits what the table promises |
| R16.4 | Chart goldens for all three profiles | `deploy/plain-yaml/all.yaml`, `all-lab.yaml`, `all-production.yaml`, `Makefile` (`render`, `render-check`), `chart/README.md` § "Golden renders (R16.4)" (:394), `chart/values-default.yaml` (`db4f237`), `chart/values-lab.yaml` (`92ebabf`), `chart/values-production.yaml` (`434134d`) (A4) | **implemented (A4)** — R11.13's generated-file rule and R16.4's golden test are one mechanism: the committed manifests *are* the goldens, `make render-check` re-renders all three profiles and requires an empty diff, and CI's `helm` job runs it. A chart change without a regenerated manifest fails the build |
