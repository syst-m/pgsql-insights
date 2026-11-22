# Changelog

All notable changes to `pgsi`, per [R14.6](requirements.md). R14.6 asks for the
file to be maintained from Phase 1 onward rather than retro-fitted at release, and
this file is not that: it is created here, at Phase 9, so every entry below is
transcribed from `IMPLEMENTATION.md`'s §3 validation entries rather than written
when each phase closed. From this commit the rule holds literally — add your entry
to `[Unreleased]` in the same commit as the change.

The project is pre-1.0 and the spec is at v0.3.0 (see
[`requirements.md`](requirements.md)); the chart's `appVersion` is `0.1.0-dev`.
Phases are the spec's own numbering, and a phase being *specified*, *built* and
*validated* are three different states that [`IMPLEMENTATION.md`](IMPLEMENTATION.md)
tracks per requirement — this file records what shipped, not what is planned.

## [Unreleased]

### Phase 1a — backend (in review)

Discovery (R1.1–R1.9): cluster, instance and pooler enumeration through the
Kubernetes API, with the per-port transport table (R1.10). `GET /api/v1/clusters`
serving R1.5's field set verbatim in the `{data, meta}` envelope (R8.2, R8.4),
`pgMajor` nullable per the partial-render contract (R1.3, R10.22). Auth (R8.8)
and the CORS config (R8.9), `/healthz` and `/readyz` never auth-gated.
`/debug/pprof/*` follows `--auth`. `internal/logx/` adds R13.7's structured JSON
logger and the `--log-field-*` flag shape — the requirement §7.2 lists under 1a
and that this phase first shipped without. The Makefile's `cover` and
`openapi-check` targets (R11.11), `scripts/check-openapi.py`, and the
`internal/version` ldflags wiring (R11.3).

### Phase 1b — web UI (validated)

The React + TypeScript + Vite SPA, built and `go:embed`'d into the binary
(R10.1); the Fleet page and Cluster Overview (R10.4, R10.5); the nav shell and
route table for all fourteen R10.3 pages, so the twelve that later phases build
have URLs, nav entries and a test today. The Vitest + Playwright visual suite
(R16.7), whose eight baselines in `web/e2e/__snapshots/` — the seven R10.12
screens plus the dark-mode shot — are the images CI compares against. R10.22's
served-cluster count fix and the shared fixture that proves it (R16.8).

### Phase 4 — chart half (in review)

The ServiceMonitor/PodMonitor contract corrected against the reference
environment's kube-prometheus-stack (R9.9 — the `release: prom-operator`
selector requirement), the cardinality budget documented with a measured number
(R9.10), and `make render-check` as the golden test for the generated manifests
(R11.13, R16.4). The three profiles: `values.yaml` as the shipped default
(R11.12), `values-production.yaml`, `values-lab.yaml` (R11.6, R13.6).

### Phase 5 — API half (in review)

The auth table R8.8 needs, the CORS config R8.9's, the OpenAPI document R8.1's
(`api/openapi.yaml`, with the `x-planned-paths` extension documenting routes
before they exist), and the provisional rule set R9.11 asks for — 16 rules, a
`critical` tier CNPG's own `cnpg-default-alerts` lacks, each carrying
`summary`/`description`/`runbook_url`. R8.1b's operation-count correction, and
six auth/CORS fixes from A3's review of `f9aae41`.

### Phase 9 — docs (in progress)

`README.md` (R14.3): what it is, the screenshot as the Playwright baseline
itself, the two-command quickstart, the capability matrix vs Cloud SQL, the
metrics catalogue (R14.7), and the link to IMPLEMENTATION.md. The architecture
doc (§3.1's diagram). `docs/runbooks/` (R14.4): one kebab-case scheme and 27 files
(14 for the pgsi rules R9.11's chart emits, 11 for the Postgres/CNPG alert
surface it is measured against, plus the two known-duplication files the index
flags), with the index stating which requirement each belongs to — the fix for
the runbook-linkage gap R9.11 names as its own
differentiator. `docs/agents.md`: the four-slot roster, per-slot charters and
the shared operating contract, so a future run reproduces the arrangement
instead of rediscovering it. `CONTRIBUTING.md` (R14.5) and this file (R14.6).

### Spec

v0.3.0 split Phase 1 into 1a (backend) + 1b (web UI) and incorporated the
architecture review (4 blocking, 8 major, 6 minor). The four accepted rulings
from the `f9aae41` review are transcribed into `requirements.md`, including
`lock_time` added to R9.4's aggregate members — a post-0.3.0 change to a frozen
requirement made by ruling rather than by drift.

## [0.1.0] — the prior release this spec rewrites

The 0.1.0 line is what v0.2.0 and v0.3.0 of the spec were written against: it had
`pgsi_scrape_target_up` (CNPG's up) and no metric for pgsi itself, no `runbook_url`
linkage, a letter-suffixed requirement ID scheme (`R9.9b`), and 18 requirement IDs
that were defined and scheduled in no phase. R9.12 (`pgsi_up`), R9.11's
`runbook_url` annotations, the ID-scheme cleanup (A3-N2) and R14.8's
requirement→file→status table are each a response to one of those gaps.
