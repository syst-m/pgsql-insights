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

| Req | Requirement (abridged) | File(s) | Status |
|---|---|---|---|
| R11.6 | One Helm chart as the primary artifact, with documented profiles plus `deploy/plain-yaml/` and `deploy/kustomize/` | `chart/values.yaml`, `chart/values-lab.yaml`, `deploy/plain-yaml/`, `deploy/kustomize/`, `docs/implementation/phases/phase-0.md:74` | **implemented** — Phase 0's requirement, validated there (`make helm-lint` lints all three named profiles). What Phase 9 claims on it is the lab profile, and that is the profile the kind run below deployed with `make deploy DEPLOY_ARGS="-f chart/values-lab.yaml"`; the chart itself is unchanged by this phase. |
| R11.9 | Two-command quickstart under five minutes, blocked on Q6; the packaged tarball is the interim form | `README.md` § "Provisional image registry (open question Q6)" (:120), `docs/implementation/phases/phase-0.md:76`, `docs/implementation/phases/record-r11-9-r11-16.md:1` | **in-progress** — waits on the user's Q6 answer for the `helm repo add` form. The interim artifact measures clean (`pgsi-0.1.0.tgz`, 36138 B, `helm lint` rc 0, renders byte-identically to `./chart` in all three profiles) and the `helm-package:` Makefile target that puts it in CI is A2's, in flight as of today. |
| R13.6 | Lab mode under 100 MB of memory, on kind and on minikube | `deploy/kind.yaml:1`, `chart/values-lab.yaml` | **in-progress** — the kind half closed 2026-11-25 (kind v0.33.0 / kindest/node v1.37.0, the lab profile deployed, `crictl stats` read 9.683 MB against the "< 100 MB" ceiling). The minikube half has no config and no run and waits on `brew install minikube`, which the user has not granted. |
| R14.3 | README: what it is, a screenshot, a quickstart, the capability matrix, the metrics catalogue, and a link to IMPLEMENTATION.md | `README.md` § "Quickstart — two commands" (:39), § "Capability matrix vs Cloud SQL" (:136), `README.md:29` | **implemented** — refreshed at `0b440df`. The image is the R16.7 Playwright baseline itself (`web/e2e/__snapshots__/fleet-fleet.png`, embedded at `README.md:29`) rather than a separate GIF; the quickstart section is the two-command form R11.9 specifies. |
| R14.4 | The `docs/` inventory (A3's ruling: the normative content is the inventory of artifacts, not the directory layout) | `docs/README.md` § "The seven artifacts R14.4 names" (:22) | **implemented** — closed at `2a645e8`, corrected at `76fe953`. It shipped before the WBS row was written and the row still read it as open, which is the finding recorded in the Task breakdown above. |
| R14.5 | CONTRIBUTING.md: the dev loop, a code-layout map, and the release process | `CONTRIBUTING.md` § "The dev loop" (:8), § "Code layout" (:106), § "Releasing" (:148) | **implemented** — all three sections exist. The dev-loop half was brought in line with the Makefile's four lint deps and the `make openapi` target at `0b440df`; the release section restates R15.2's scheme and names the one thing still missing, which is the automated GitHub Release. |
| R14.6 | CHANGELOG.md maintained from Phase 1 onward, not retro-fitted at release | `CHANGELOG.md` § "Changelog" (:1) | **implemented** — with the tension stated in the file's own header: it was created at Phase 9, so every entry above the current one is transcribed from IMPLEMENTATION.md's §3 validation entries, and Phases 2 and 3 (no entry yet, both mid-phase) are derived from the commit chain instead. From its own commit onward the rule holds literally — each change adds its `[Unreleased]` entry in the same commit. |
| R14.7 | Every exported metric and every API endpoint documented in one place — nothing in code that is not in docs | `README.md` § "Metrics catalogue (R14.7)" (:180), § "API surface (R8.2)" (:215), `internal/collector/sql.go:96` | **implemented** — measured 2026-11-28: `grep -rhoE 'mux\.HandleFunc\("[A-Z]+ [^"]+"' internal/api/*.go \| sort -u` yields 12 registrations (a 13th match is a doc comment, not a route) and all 12 appear in the API table, which folds healthz into the readyz row, the five pprof patterns into one row and the PromQL pair's four patterns into one row. The catalogue lists every `pgsi_*` family; the two collector string literals that are not families are `pgsi_source_now` (`internal/collector/sql.go:96`, a SQL column alias) and `pgsi_db_xact_commit_total` (R9.7's member, inside the System row). |
| R14.8 | Requirement→file→status table | `docs/implementation/phases/phase-0.md:90`, `requirements.md:416`, `IMPLEMENTATION.md:74` | **implemented** — this table, covering the eleven ids the §7.2 row at `requirements.md:416` claims (R11.6, R11.9, R13.6, R14.3–R14.8, R15.2, R15.3). `IMPLEMENTATION.md:74` and this file's own WBS sentence both abridge that range to R14.3–R14.7, which is why R14.8's own row was missing here until now; a phase's table rowing its own R14.8 is the shape `docs/implementation/phases/phase-0.md:90` already uses. |
| R15.2 | Distribution artifacts, the tag scheme, the chart-version-vs-appVersion policy, and a GitHub Release per tag with its SBOM | `.github/workflows/release.yml:1`, `CONTRIBUTING.md` § "Releasing" (:148) | **in-progress** — the workflow exists (added 2026-11-26) and its SBOM step is provisional until A3 rules whether the release's SBOM is syft on the built image or R15.3's committed audit. The release itself waits on the user's `v0.1.0` tag and the `chart/Chart.yaml` `appVersion` flip off `0.1.0-dev`, which the workflow's assert step gates on. |
| R15.3 | The license audit committed as `docs/licenses/` and regenerated in CI | `docs/licenses/README.md:1`, `.github/workflows/ci.yml:123`, `scripts/gen-licenses.py:77-98` | **implemented** — closed at `a8da067`, extended by `9018c41`, `99bf269` and `8c07252` (the last overrides the audit's one `Unknown` row in the generator's `LICENSE_OVERRIDES` rather than in the CSV, because `--check` regenerates and compares). CI runs `make licenses-check` (`.github/workflows/ci.yml:123`), so a stale manifest fails the build rather than drifting. |
