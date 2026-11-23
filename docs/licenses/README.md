# License manifest (R15.1, R15.3)

The project license is **Apache-2.0** — the full text is [`LICENSE`](../../LICENSE) at
the repo root, and `chart/Chart.yaml` advertises it to Artifact Hub via
`artifacthub.io/license: Apache-2.0`. That is R15.1's half, and it was already true.

This directory is R15.3: *"Third-party license audit (`go-licenses` + the SPA's
license set) committed as `docs/licenses/` and regenerated in CI."* The row has three
clauses — the audit exists, it is committed here, and CI regenerates it — and the
third one used to be only asserted. See [How CI regenerates it](#how-ci-regenerates-it).

| File | Content |
|---|---|
| `go-dependencies.txt` | Every module in the module graph — 56 of them, 5 direct and 51 indirect — parsed from `go.mod`, plus this module itself with its Apache-2.0 license. |
| `web-dependencies.txt` | Every package in `web/package-lock.json` with its SPDX license — 176 packages, 0 without a license field. |
| `go-licenses-report.csv` | `go-licenses report ./...` — 61 packages with their license URL and SPDX id, the columns the tool emits (`package`, `license_url`, `license`). |

R15.3 names both halves explicitly, and the numbers are the reason: the SPA is 176
packages against the Go side's 56 modules, so a Go-only audit covers the smaller
dependency surface while the UI carries the larger one.

## How CI regenerates it

`make licenses` runs `scripts/gen-licenses.py` and writes the three files.
`make licenses-check` runs the same script with `--check`, which generates in memory
and fails naming the stale files. `.github/workflows/ci.yml` runs
`make licenses-check` in the `lint` job, after `openapi-check` and
`cardinality-check` — the placement R8.1c's row establishes for a doc-consistency
gate that can fail the build ("the `lint` job beside `scripts/check-parity-matrix.sh`").

Before this wiring existed, the claim was in this README and the step was not in the
workflow: `make licenses` appeared nowhere in `ci.yml`, so the manifest could drift by
any amount and CI stayed green. That is the *report-without-enforcing* shape A3 keeps
finding in this repo — a document asserting a gate that does not run — and it had a
second cause here. The old target stamped its output with `# Generated: <timestamp>`,
so the file differed on every run and could never be the subject of a diff test. A
generated file with an embedded timestamp is uncheckable by construction; git already
records when the file changed, which is where the date belongs.

## Why `go.mod` and not `go list -m all`

The old target shelled out to `go list -m all`, which needs a Go SDK and a network
fetch of the module graph, and whose output on one machine need not match another's.
`go.mod` carries the same information as a checked-in file: since Go 1.17 it lists
every module a build needs, direct and indirect, and `make tidy-check` (R13.10) is the
gate that keeps the file equal to what the build actually imports. Reading the file is
therefore not a stale copy of `go list -m all` — it is the same set, without the
toolchain dependency, and byte-identical on every machine. The target now runs in the
`lint` job with no extra setup.

`go.mod` has no `replace` directives (R13.10 forbids them), so no entry here points
somewhere the build does not actually build from.

## The `go-licenses` half

R15.3 names `go-licenses` as the audit's tool, so the CSV is its real output rather
than a hand-maintained stand-in. CI installs it pinned — `GO_LICENSES_VERSION: v1.6.0`
in the workflow's `env` block, the same treatment `GOLANGCI_LINT_VERSION` and
`HELM_VERSION` get — and `make licenses` writes the CSV whenever the binary is on
PATH. Where it is not, `go-dependencies.txt`'s header says so
(`# License names: go-licenses not on PATH — …`) and `--check` names that as the cause
of the failure instead of looking like an unrelated edit to a generated file. That is
the optional-tool shape `lint-go` already uses for golangci-lint: a machine without the
tool reports the gap rather than pretending the audit is complete.

The tool reports *packages*, not modules, which is why the CSV has 61 rows against
`go.mod`'s 56 modules — a module with several imported packages contributes one row
per package. Its own header line for this module reads `blob/HEAD/LICENSE` because the
main module has no version in `go.mod`; the tool warns about that and the warning is
correct and harmless.

One row resolves to `Unknown,Unknown`: `modernc.org/mathutil`, pulled in by
`modernc.org/sqlite` (Phase 2's store). It is the first unresolved license in this
audit — the other 60 rows carry an SPDX id — and the file records it as `Unknown`
rather than a hand-typed guess, which is what "generated, not curated" is for. The
row is a fact about the tool's resolution, not a license change, and it is the one
cell in `docs/licenses/` worth a human reading before the v0.1.0 license review.

## Images

The runtime image is `gcr.io/distroless/static:nonroot`, pinned by digest in the
`Dockerfile` and bumped by Renovate (`.github/renovate.json`) rather than by hand
(R11.5). The base image carries its own license text inside the image; no R15 row
covers it, so the earlier text here citing "R15.5" for it was citing a requirement
that does not exist — the same wrong-ID class as the `coverage gate (R14.5)` label
that `0d707da` corrected in `ci.yml`. R15 has exactly three rows, R15.1 to R15.3; the
"R15.4" and "R15.5" this file used to cite are not among them, and the facts those two
citations were attached to belong to R15.3 (the SPA license set) and R11.5 (the pinned
digests).
