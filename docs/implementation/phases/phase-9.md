# Phase 9 — Docs site, runbooks, README, screenshots, lab profile, v0.1.0 release

## Status

🔄 In progress (A4). No deviation rows in [deviations.md](../deviations.md) carry phase `9`. No
validation entry exists yet — this is A4's own phase, and R14.2 makes the entry A3's artifact.

## Owners

A4 (sonnet).

## Task breakdown

No task table yet; the WBS row in the [README](../README.md) is the task list. What has landed,
since that row's own cell is the only record of it:

- **Started at `0b440df`** — R14.3's README refreshed (its status line now carries all ten phases
  from the index table, and its API table lists every route registered at HEAD, which is R14.7's
  route half) and R14.5's dev-loop half brought in line with the Makefile's four lint deps plus the
  `make openapi` target that `9794560` added; `b097882` closed the `.gitignore` gap that build exposed.
- **R14.4 docs index** — closed at `2a645e8`, corrected at `76fe953`. It shipped before the WBS row
  was written, and the row still read it as open.
- **R15.3 license audit** — closed at `a8da067` (the audit plus the `make licenses-check` wiring at
  `.github/workflows/ci.yml:116-117`), extended by `9018c41` and `99bf269`, then by `8c07252`, which
  overrides the audit's one `Unknown` row (`modernc.org/mathutil` → BSD-3-Clause, the evidence
  recorded in the generator's `LICENSE_OVERRIDES` at `scripts/gen-licenses.py:77-98` rather than
  hand-typed into the CSV, because `--check` regenerates the CSV and compares it to the committed
  file) and regenerates `go-dependencies.txt` and `web-dependencies.txt`, which had fallen five
  modules behind `go.mod` when `9e6572a` added pgx/v5.
- **R13.6 kind half** — closed 2026-11-25: `deploy/kind.yaml` is now tracked (the artifact the WBS
  cell named missing), and on kind v0.33.0 / kindest/node v1.37.0 / Docker 29.9.2 macOS-arm64 the
  CONTRIBUTING loop ran end to end — create rc=0,
  `make deploy DEPLOY_ARGS="-f chart/values-lab.yaml"` deployed the lab profile, `make verify`
  answered 200 on all five endpoints, and `crictl stats` read 9.683 MB against R13.6's "< 100 MB".
  Two findings from that run are recorded in the files they belong to: `featureGates:
  {MetricsServer: true}` broke cluster creation (the log excerpt is in `deploy/kind.yaml`), and a
  CRD-less cluster answers `/readyz` 503 and makes `make deploy`'s rollout wait exit 2, which
  CONTRIBUTING.md now states along with the `--server-side` CNPG-CRD prerequisite. **minikube, the
  other half of R13.6's sentence, stays open**: no config, no run.
- **Open** — R15.2's release automation. `.github/workflows/release.yml` was added 2026-11-26 and is
  the artifact the WBS cell named missing; its SBOM step is provisional until A3 rules whether the
  release's SBOM is syft-on-the-built-image or R15.3's committed `docs/licenses/` audit, and the
  release itself still needs the `v0.1.0` tag plus the `chart/Chart.yaml` `appVersion` flip from
  `0.1.0-dev` that release.yml's assert step gates on.

## Requirement → file → status (R14.8)

No table yet. The ids the WBS row claims are R11.6, R11.9, R13.6, R14.3–R14.7, R15.2, R15.3.
