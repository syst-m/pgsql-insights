# Phase 0 — Repo skeleton, packaging, chart skeleton, CI

## Status

⚠️ Done with deviations. Six of them, all in
[deviations.md](../deviations.md) (rows §5-D1–§5-D6, phase `0`). The image is published at
`10.10.111.134:5000/frankellis/pgsql-insights:dev` for both platforms and is running in the
reference cluster (R11.14).

## Owners

A4 (sonnet). The Phase 0 commit is the one R14.1 scopes to code plus its `IMPLEMENTATION.md`
update; see [phase-1.md](phase-1.md) for the commit-index incident that moved part of it.

## Task breakdown

| # | Task | Req | Status |
|---|---|---|---|
| 0.1 | `go.mod` (Go 1.26, no cgo, no `replace`) + `cmd/pgsi` with `serve\|collect\|api\|version` stubs | R3.1, R13.10 | ✅ |
| 0.2 | Makefile: `build`, `test`, `lint`, `helm-lint`, `render`, `image`, `licenses` | R11.11 | ✅ |
| 0.3 | Multi-stage Dockerfile → `gcr.io/distroless/static:nonroot`, uid 65532, pinned base digest | R11.2, R11.3, R11.5 | ✅ |
| 0.4 | `buildx --platform linux/amd64,linux/arm64` verified (Phase 0 **blocker** — all 5 nodes are amd64) | R11.2 | ✅ |
| 0.5 | Chart skeleton: chart.yaml, values.yaml (**`values-default.yaml` as the shipped default**), `_helpers.tpl`, Deployment, Service, ServiceAccount, Role/RoleBinding (+ ClusterRole variant), ConfigMap, ingress, networkpolicy, `.helmignore` | R11.6, R11.7, R11.12, R12.1–R12.3 | ✅ |
| 0.6 | `httpGet` probes on `/healthz` + `/readyz`; `/tmp` as `emptyDir`; securityContext per R12.7 | R11.2, R12.7 | ✅ |
| 0.7 | `deploy/plain-yaml/all.yaml` **generated** by `helm template` in CI; `deploy/kustomize/` points at it | R11.13 | ✅ |
| 0.8 | CI: build, `golangci-lint`, `helm lint`, `helm template` snapshot tests (3 profiles), unit tests, multi-arch build, **image publication to the named registry** | R11.11, R11.16 | ✅ ⚠️ §5-D3 |
| 0.9 | LICENSE (Apache-2.0) + `docs/licenses/` scaffold | R15.1 | ✅ |
| 0.10 | **Deploy-and-observe acceptance**: image built **and deployed** to the reference cluster, pod `Running`, `/healthz` answers through the Service | R11.14 | ✅ |
| 0.11 | Compatibility matrix artifact with `tested\|fixture\|impossible\|untested` cells | R13.9 | ✅ |
| 0.12 | `IMPLEMENTATION.md` requirement→file→status table populated for Phase 0's IDs | R14.8 | ✅ |

## Validation entry (R14.2)

Run 2026-11-20 on the reference environment (k3s v1.37.2+k3s1, 5 amd64 nodes, CNPG 1.30.0) from
the arm64 dev machine. Full text at `1b11199:IMPLEMENTATION.md` §3.2; the short
version:

- **Build + unit tests (R11.11, R13.10, R11.3)** — `go build ./...` clean; `go test ./... -count=1`
  passes (`internal/api`, `internal/version`); `make build` → `bin/pgsi` → `0.1.0-dev` + commit
  `a47e149` (the ldflags injection R11.3 asks for, and the pair CI's build job greps for);
  `make tidy-check` clean, no `replace`, no cgo requires; `make cover` passes the 70% gate.
  `golangci-lint` was not installed on the machine, so `make lint` ran `gofmt` + `go vet` and
  printed a skip line — §5-D4, since closed by the pinned v1.64.8 install.
- **Multi-arch image (R11.2, R11.4, R11.5)** — buildx `linux/amd64,linux/arm64` through the
  `multi-arch` docker-container builder with `FROM --platform=$BUILDPLATFORM` cross-compile, so no
  QEMU; index `sha256:0bd8fe6a9e86ea50e068353aa09fae2ce787a5211078ffd71e2f72a0f8a34df4`;
  3.279 MiB amd64 / 3.052 MiB arm64 from the registry's own manifests against a <60 MB target
  (CI fails >100 MB).
- **Chart (R11.6, R11.7, R11.12, R11.13)** — `helm lint` (helm 4.3.1) clean on all three profiles
  via `make helm-lint`; `make render-check` clean against the three `deploy/plain-yaml/` goldens
  (R11.13); `make kustomize` builds — the bare `kubectl kustomize` needs
  `--load-restrictor=LoadRestrictionsNone`, which the target carries (A2's `7a6a13b`);
  `values-default.yaml` is a symlink to `values.yaml`, follows, and renders identically (R11.12).
- **Deploy-and-observe (R11.14)** — 8 objects applied to namespace `pgsi` (§5-D2 records why
  `kubectl create -f` rather than `helm upgrade --install`); PVC `pgsi-data` Bound, 1Gi RWO
  `iscsi-ext4` (R11.15); pod `pgsi-5599b95b8-rp6vk` `Running` 1/1 on `k8s-wrk-1` with the amd64
  image pulled on an amd64 node (the §5-D5/R11.2 guarantee); `/healthz` `ok` 200, `/readyz` `ready`
  200, `/api/v1/version` `{"version":"0.1.0-dev","commit":"a47e149"}`, `/metrics`
  `pgsi_build_info{…} 1` (R11.3), all through the Service; the R16.4 helm test pod `Completed`
  with `ok`.
- **Not yet observed** — CI has never run green: the branch is not pushed and `ghcr.io` has no
  usable credentials on this machine (§5-D1, §5-D3). Every step has a local `make` equivalent that
  does pass.

## Requirement → file → status (R14.8)

| Req | Requirement (abridged) | File(s) | Status |
|---|---|---|---|
| R11.1 | One image contains API + collector + UI | `Dockerfile`, `cmd/pgsi/main.go` | **implemented** — the single-binary shape is fixed; the collector (P2) and UI (P1b) fill the same binary |
| R11.2 | Multi-arch distroless build via buildx | `Dockerfile`, `.github/workflows/ci.yml` (job `image`), `Makefile` (`image`, `push-local`) | **validated** — both platforms published; the amd64 image runs on the amd64 reference nodes |
| R11.3 | OCI labels + ldflags version at `/api/v1/version` + `pgsi_build_info` | `Dockerfile` (LABEL, `ARG VERSION/COMMIT`), `internal/version/version.go`, `internal/api/server.go`, `chart/values.yaml` (`appVersion`) | **validated** — curl returned the injected pair; `make build` and CI grep the SHA |
| R11.4 | < 60 MB compressed (registry-reported, amd64) + SPA JS as a separate number | `scripts/image-size.py`, `Makefile` (`image-size`), CI (`--max-mb 100`, `web` job) | **validated** — 3.279 MiB amd64; the JS number is now real (85 448 B gzipped, from `web/dist` — see §3.5), reported by `scripts/image-size.py --spa-bundle` alongside the image size |
| R11.5 | Pin base digests; Dependabot/Renovate present | `Dockerfile` (both `@sha256:`), `.github/dependabot.yml`, `.github/renovate.json` | **validated** |
| R11.6 | One chart, documented profiles | `chart/values.yaml`, `chart/values-production.yaml`, `chart/values-lab.yaml`; `cmd/pgsi/main.go:350` + `cmd/pgsi/main.go:732` + `cmd/pgsi/config.go:202` (A1, `c5c3665`; `5652612` moved the three to `cmd/pgsi/main.go:423`, `cmd/pgsi/main.go:816` and `cmd/pgsi/config.go:209`) | **validated** — `make helm-lint` lints all three. The binary half landed at `c5c3665`: `--store=` is the flag `19eb117:requirements.md:221` spells (`--store=memory`, the lab profile this requirement names), so `chart/values.yaml:64`'s `sqlite \| memory` value set — a Phase 0 comment that promised a "Phase 2+ key" — now has an implementation behind it, with `openSink` (`cmd/pgsi/main.go:732`) resolving the value to the sink the scheduler writes to. The comment that commit replaced attributed the flag to R11.8, which is the precedence requirement, and to R13.5, whose flag is `--max-store-bytes`; `cmd/pgsi/config.go:202`'s Req column now says R11.6 and so does this row |
| R11.7 | Chart ships Deployment, Service, SA, Role/RoleBinding (+ClusterRole variant), ConfigMap, ServiceMonitor, PodMonitor variant, PrometheusRule, Ingress, NetworkPolicy, PDB, tests, NOTES | `chart/templates/*.yaml`, `chart/templates/tests/test-connection.yaml` | **validated** — rendered in all three profiles; the test pod ran in the reference cluster |
| R11.9 | <5-min two-command quickstart (blocked on Q6); packaged tarball is the interim form | `README.md`, CI (`helm package` via `make helm-lint`/`image`) | **in-progress** — §5-D1; README:120 documents the tarball + reference-registry paths, and the tarball itself now measures clean (`pgsi-0.1.0.tgz`, 36138 B, `helm lint` rc 0, render byte-identical to `./chart` in all three profiles, 2026-11-28); `Makefile` `helm-package:` and the ci.yml step that calls it landed 2026-11-28, so the artifact the column names now exists in CI (`fdc721f`'s record is the finding that it did not); the quickstart itself stays blocked on Q6 |
| R11.10 | Uninstall leaves no cluster-scoped residue | `chart/templates/clusterrole.yaml`, `clusterrolebinding.yaml` (both gated on `rbac.allNamespaces`) | **implemented** — the default render contains no cluster-scoped object; `helm uninstall` itself runs in P9's lab pass |
| R11.11 | CI mirrors the Makefile; ≥70 % coverage; render test | `.github/workflows/ci.yml`, `Makefile`, `.golangci.yml` | **implemented** — all steps pass locally; first green run pending (§5-D3) |
| R11.12 | `values-default.yaml` is a real, complete profile | `chart/values.yaml`, `chart/values-default.yaml` (symlink) | **validated** — helm follows the symlink and renders the shipped default |
| R11.13 | `deploy/plain-yaml/all.yaml` generated, not maintained; kustomize points at it | `deploy/plain-yaml/*.yaml`, `deploy/kustomize/kustomization.yaml`, `Makefile` (`render`, `render-check`) | **validated** — `make render-check` clean; the rendered file is what was applied to the cluster |
| R11.14 | Phase 0 acceptance is deploy-and-observe | `README.md`, §3.2 of this file | **validated** — pod `Running`, `/healthz` 200 through the Service |
| R11.15 | Storage access-mode contract (RWO PVC ⇒ `Recreate`) | `chart/templates/deployment.yaml`, `chart/templates/pvc.yaml`, `chart/values.yaml` (`persistence`) | **validated** — RWO iSCSI PVC bound and mounted by the Deployment |
| R11.16 | CI publishes `:dev` + git-SHA tag on merge to main | `.github/workflows/ci.yml` (job `image`, main path), `scripts/push-oci.py` | **in-progress** — §5-D1/§5-D3: published to the reference registry today, ghcr path pending Q6; README:120 now lists the four hand-written org sites (§5-D10 names three, `release.yml:36` was unlisted) |
| R12.1 | Least-privilege namespaced Role verb set | `chart/templates/_helpers.tpl` (`pgsi.rules`), `chart/templates/role.yaml` | **validated** — the shared helper feeds Role and ClusterRole so they cannot drift |
| R12.2 | `pods/exec` opt-in via `rbac.allowPodsExec` | `chart/templates/_helpers.tpl`, `chart/values.yaml` (`rbac.allowPodsExec: false`) | **validated** |
| R12.3 | Cluster-scoped RBAC only when `rbac.allNamespaces=true` | `chart/templates/clusterrole.yaml`, `clusterrolebinding.yaml` | **validated** — default render has none; production profile renders the pair |
| R13.9 | Compatibility matrix artifact with per-cell states | `docs/compatibility-matrix.md` | **validated** — two axes + operator/monitoring rows, each cell carrying its evidence |
| R13.10 | Go 1.26+, no cgo, no `replace`, tidy in CI | `go.mod`, `Makefile` (`tidy-check`), CI (`build` job) | **validated** |
| R14.1 | `IMPLEMENTATION.md` is the handoff file, updated in the same commit | `IMPLEMENTATION.md` | **validated** |
| R14.8 | Requirement→file→status table | §3.3 of this file | **validated** — for Phase 0's IDs; grows one section per phase |
| R15.1 | License chosen and committed (Apache-2.0) | `LICENSE`, `docs/licenses/` | **validated** |
| R15.2 | Distribution artifacts named/versioned from day one (tag scheme, chart vs appVersion) | `chart/Chart.yaml`, CI tag pair, `README.md` | **implemented** — `:dev` + SHA scheme in place; the GitHub Release policy lands with P9 |
