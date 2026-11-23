# docs/

Index of the documentation tree.

R14.4 names seven artifacts that `docs/` must hold, and A3's ruling on it is that the
requirement's normative content is the *inventory* — that these artifacts exist and are
discoverable — not the directory layout. The residual gap it left was not where the spec
lives but that `docs/` had no index to point at it from; this file is that pointer, and
it is the Phase 9 artifact the ruling names.

**The spec itself is not in this directory.** R14.4 lists it first among the seven, and
the ruling is that a root `requirements.md` satisfies it: moving it here is a 22-file
cross-reference edit across three other slots' trees, which costs more than
discoverability buys. The contract is
[`../requirements.md`](../requirements.md) — v0.3.0 — and this index is what makes it
reachable from inside `docs/`.

[`../README.md`](../README.md) is the product front door; this file is the docs-directory
index. Where the two overlap — the quickstart, the image list, the capability matrix, the
repository layout — this file links and does not restate.

## The seven artifacts R14.4 names

| R14.4 names | Where it is |
|---|---|
| this spec | [`../requirements.md`](../requirements.md) — root, per the ruling above |
| the two research documents | [`research/cloud-sql-insights.md`](research/cloud-sql-insights.md), [`research/cloudnativepg.md`](research/cloudnativepg.md) |
| the live probe | [`research/live-probe.md`](research/live-probe.md) |
| `ENVIRONMENT.md` | [`ENVIRONMENT.md`](ENVIRONMENT.md) |
| `reviews/` | [`reviews/`](reviews/) — 3 files |
| per-feature runbooks | [`runbooks/`](runbooks/) — 28 files, indexed by [`runbooks/README.md`](runbooks/README.md) |
| an architecture doc with the diagram from §3.1 | [`architecture.md`](architecture.md) |

Two of those seven account for all three files in `research/`: the requirement counts the
Cloud SQL and CloudNativePG documents as "the two research documents" and names the probe
separately, so the directory holds exactly what the row asks for and nothing extra.

## In `docs/` but not named by R14.4

| File | What it is |
|---|---|
| [`agents.md`](agents.md) | The agent specification — the six-slot roster (the cap the brief set at four, amended by the user on 2026-11-22), the saturation rules, each slot's charter, and the §5.1 commit-and-citation rules that are shared contract |
| [`compatibility-matrix.md`](compatibility-matrix.md) | R13.9's artifact: the compatibility claims as per-cell states (`tested \| fixture \| impossible \| untested`) on two separate axes, PostgreSQL and Kubernetes, instead of one prose sentence |
| [`licenses/`](licenses/) | R15.3's third-party license audit — 4 files, listed below; regenerated in CI |
| [`runbooks/README.md`](runbooks/README.md) | The runbook directory's own index, which explains why there are two sets of runbooks and how the naming scheme works |
| [`README.md`](README.md) | This file |

## Every file in `docs/`

```
docs/
  README.md                     this index
  agents.md                     agent specification (R14.x shared contract, §5.1 rules)
  architecture.md               the §3.1 diagram + as-built vs target
  compatibility-matrix.md       R13.9 per-cell compatibility states
  ENVIRONMENT.md                the reference environment, observed 2026-11-20
  licenses/                     R15.3
    README.md                   how the audit is generated and how CI regenerates it
    go-licenses-report.csv      go-licenses output
    go-dependencies.txt         Go dependency manifest
    web-dependencies.txt        SPA dependency manifest
  research/
    cloud-sql-insights.md       the Cloud SQL Query/System Insights study
    cloudnativepg.md            the CloudNativePG study
    live-probe.md               the live probe record
  reviews/
    round-1-architecture.md     round-1 architecture review
    round-1-product.md          round-1 product review
    v0.3.0-architecture-review.md
  runbooks/                     28 files — see runbooks/README.md for the index
    README.md                   the two-set naming scheme
    pgsi-*.md                   15 files — runbooks for the rules chart/templates/prometheusrule.yaml ships
    pg-*.md                     12 files — runbooks for the Postgres/CNPG alerts an operator already has
```

## Root-level documents this index also points at

These live at the repository root, not here, and are listed so that `docs/` is the place
you can find every document in the project from, not just the ones under `docs/`.

| File | Requirement |
|---|---|
| [`../requirements.md`](../requirements.md) | R14.4 — the spec, v0.3.0 |
| [`../README.md`](../README.md) | the product front door |
| [`../IMPLEMENTATION.md`](../IMPLEMENTATION.md) | R14.8 — the requirement→file→status table |
| [`../CHANGELOG.md`](../CHANGELOG.md) | R14.6 — maintained from Phase 1 onward, not retro-fitted |
| [`../CONTRIBUTING.md`](../CONTRIBUTING.md) | R14.5 — dev loop, code layout map, release process |
| [`../chart/README.md`](../chart/README.md) | R11.6 / R11.12 — the chart's own reference: profiles, the two-plane rule, cardinality budget, golden renders |
| [`../LICENSE`](../LICENSE) | R15.1 — Apache-2.0 |

## Two version spaces, and why they differ

`../requirements.md` is at **0.3.0** and the product is at **0.1.0-dev**. That is not
drift, and the two numbers must not be synced: R15.2's ruling establishes two independent
version spaces — the **spec document** (`0.1.0-draft` → `0.2.0` → `0.3.0`) and the
**product** (`0.1.0-dev` → `v0.1.0`, which is Phase 9's first tag). Citing one against the
other is a category error. It is also why `reviews/v0.3.0-architecture-review.md` carries
a spec-document version while `chart/Chart.yaml` carries `0.1.0`.

## How this file is checked

Every relative link in this file resolves, and
[`../scripts/check-parity-matrix.py`](../scripts/check-parity-matrix.py) enforces it: the
checker validates requirement ids, `path:N` citations, and now relative markdown links
across every tracked `.md` file, so an index whose links rot fails CI the same way a wrong
requirement id does. Run it through the CI entry point:

```sh
scripts/check-parity-matrix.sh
```
