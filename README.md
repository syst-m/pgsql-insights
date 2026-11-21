# pgsql-insights

`pgsi` — a read-only observability surface for CloudNativePG clusters on Kubernetes.
One Go binary, one HTTP port, one Helm chart.

**Status: Phase 0 (packaging).** The binary answers `/healthz`, `/readyz`,
`/api/v1/version` and `/metrics`; it does not discover or query Postgres yet. The
product requirements live in [`requirements.md`](requirements.md) (v0.2.0) and the
phase-by-phase plan with the requirement→file→status table lives in
[`IMPLEMENTATION.md`](IMPLEMENTATION.md).

---

## Provisional image registry (open question Q6)

The spec has not settled the registry organisation (Q6), so the working value
throughout this repo is:

```
ghcr.io/frankellis/pgsql-insights
```

It appears in `Makefile` (`IMAGE`), `chart/values.yaml` (`image.repository`) and
`.github/workflows/ci.yml` (`IMAGE_REPO`), each marked provisional. Changing it later
is a three-line change plus a chart version bump — nothing else references the org.

Until the org exists, the verifiable install paths are:

1. **The packaged chart tarball** — `helm package chart && helm install pgsi pgsi-0.1.0.tgz`.
2. **The reference environment's own registry** — `10.10.111.134:5000/frankellis/pgsql-insights`,
   which is where Phase 0's image is actually published and deployed from:
   ```sh
   make push-local                     # build both platforms, publish without docker login
   make deploy DEPLOY_ARGS="--set image.repository=10.10.111.134:5000/frankellis/pgsql-insights --set image.tag=dev"
   ```
   `docker push` needs credentials that this dev machine does not have for ghcr.io, so
   `scripts/push-oci.py` publishes through the registry v2 API instead
   ([IMPLEMENTATION.md §5](IMPLEMENTATION.md)).

---

## Quick start

```sh
make build            # bin/pgsi, cgo off, version + commit injected (R11.3)
./bin/pgsi serve      # :8080 — /healthz, /readyz, /api/v1/version, /metrics
./bin/pgsi version    # v0.1.0-dev + the git SHA it was built from
make test             # go test ./...
make lint             # go vet (+ golangci-lint when installed)
make cover            # coverage against the 70% gate (R11.11)
```

Subcommands are `serve | collect | api | version` (R3.1). **v1 ships exactly one
working role, `serve`** ([IMPLEMENTATION.md D9](IMPLEMENTATION.md)); `collect` and
`api` are reserved stubs that exit cleanly so the CLI surface is stable from Phase 0.

## Install the chart

```sh
# Default profile (values.yaml IS the shipped default — R11.12)
helm install pgsi ./chart -n pgsi --create-namespace

# The other two named profiles are documented deltas, not replacements
helm install pgsi ./chart -n pgsi --create-namespace -f chart/values-production.yaml
helm install pgsi ./chart -n pgsi --create-namespace -f chart/values-lab.yaml

# No Helm? The plain manifests are generated and committed (R11.13)
kubectl apply -f deploy/plain-yaml/all.yaml
kubectl apply -k deploy/kustomize
```

`make render` regenerates `deploy/plain-yaml/` from the chart for all three profiles;
CI runs `make render-check` and fails if the committed files are stale, which makes the
generated manifests the golden test for R11.13 and R16.4.

### What the chart renders (default profile)

`ServiceAccount`, namespaced `Role` + `RoleBinding` (R12.1's verb set, R12.3 — a
`ClusterRole`/`ClusterRoleBinding` appear **only** with `rbac.allNamespaces=true`),
`ConfigMap`, `PersistentVolumeClaim` (RWO, R11.15), `Deployment` (`strategy: Recreate`
because an RWO volume cannot attach to two pods), `Service` (`ClusterIP :8080`, port
named `http`), `ServiceMonitor`, and a `helm test` pod that curls `/healthz` through the
Service (R16.4). Ingress, NetworkPolicy and PodDisruptionBudget templates exist and are
off by default; `values-production.yaml` turns on ingress and the NetworkPolicy.

Both probes are `httpGet` — the image is distroless, which has no shell, so an exec
probe cannot run (R11.2). The pod runs as uid/gid 65532 with a read-only root
filesystem, `/tmp` as an `emptyDir`, all capabilities dropped, `seccompProfile:
RuntimeDefault` (R12.7).

## Images

```sh
make image            # buildx --platform linux/amd64,linux/arm64 --push (R11.2)
make image-size       # R11.4 compressed size report, gated at 100 MB
make push-local       # both platforms → 10.10.111.134:5000, no docker login needed
```

The `Dockerfile` is multi-stage: `FROM --platform=$BUILDPLATFORM golang:1.26-alpine`
cross-compiles with `GOOS/GOARCH` (no QEMU), and the runtime stage is
`gcr.io/distroless/static:nonroot` pinned by digest with `USER 65532:65532` (R11.3,
R11.5). Both base digests are OCI **index** digests covering both required platforms.

Measured R11.4 size: **3.28 MiB compressed for `linux/amd64`**, 3.05 MiB for
`linux/arm64` — the registry's own manifest numbers, not `docker save | wc -c`. The SPA's gzipped JS is a
separate CI number and does not exist before Phase 5.

## Reference environment

k3s v1.37.2+k3s1, 5 nodes (all `linux/amd64`), CNPG 1.30.0, four PostgreSQL clusters.
Full probe record: [`docs/research/live-probe.md`](docs/research/live-probe.md).
Compatibility claims and their evidence: [`docs/compatibility-matrix.md`](docs/compatibility-matrix.md).

The reference cluster's Prometheus (kube-prometheus-stack in `monitoring`) only selects
ServiceMonitors labelled `release: prom-operator`; set
`monitoring.serviceMonitor.additionalLabels` accordingly (the default `{}` works on
clusters with a permissive selector).

## Repository layout

```
cmd/pgsi/             single binary, subcommand dispatch (R3.1, R11.1)
internal/api/         HTTP handlers: /healthz, /readyz, /api/v1/version, /metrics
internal/version/     ldflags-injected Version/Commit (R11.3)
chart/                the Helm chart (R11.6, R11.7, R11.12)
deploy/plain-yaml/    generated manifests, all three profiles (R11.13)
deploy/kustomize/     kustomization pointing at the generated default profile
Dockerfile            multi-stage, distroless, pinned digests (R11.2, R11.3, R11.5)
Makefile              local mirror of CI (R11.11)
.github/workflows/    CI (R11.11, R11.16)
docs/                 reviews, research, license manifest, compatibility matrix
```

## License

[Apache-2.0](LICENSE). Dependency manifest: [`docs/licenses/`](docs/licenses/).
