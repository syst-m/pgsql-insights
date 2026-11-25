# syntax=docker/dockerfile:1.7
#
# Multi-stage build for the single pgsi binary (R11.1).
#
# R11.2 multi-arch: the build stage is pinned to $BUILDPLATFORM and Go itself
# cross-compiles to $TARGETARCH, so neither arm64 nor amd64 needs QEMU emulation.
# Both base images are pinned by digest (R11.5) and bumped by Renovate, never by hand.
#
# The digests below are the OCI index digests, which cover linux/amd64 and
# linux/arm64 — a per-platform digest would break one of the two targets.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

# Copy the module files first so the dependency layer is cached independently
# of the source (R11.11 keeps CI fast enough to run everything on every push).
COPY go.mod go.sum* ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
# `api/` is a Go package, not only a spec file: api/embed.go embeds openapi.yaml
# and x-planned-paths.yaml, and internal/api/openapi_gen.go:62 has imported it as
# `openapipkg "github.com/frankellis/pgsql-insights/api"` since A2's fc8dd45. A
# host `go build ./...` cannot catch its absence — the host tree always has api/ —
# so this COPY line is the only place the dependency can be broken, and building an
# image is the only check that finds it (ci.yml's image job runs on pushes to main
# and on PRs, so a branch with no PR has no runner).
COPY api/ ./api/
# R10.1: the SPA is embedded via go:embed (web/embed.go), so `web/` is part of
# the Go build's inputs — omitting it breaks `go build ./...` with
# "pattern all:dist: no matching files found". web/dist is committed, so this
# stage needs no node toolchain; the node build is `make web` / the CI web job.
COPY web/ ./web/

# R11.3: the ldflags-injected version/commit are the same values the Makefile
# injects locally, and they surface in pgsi_build_info, /api/v1/version and the
# OCI labels below. cgo off (§3.4) — the binary must stay static.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w -X github.com/frankellis/pgsql-insights/internal/version.Version=${VERSION} -X github.com/frankellis/pgsql-insights/internal/version.Commit=${COMMIT}" \
      -o /out/pgsi ./cmd/pgsi

FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
# R11.5 / R12.7: distroless static, nonroot uid 65532, no shell, no apt layer.
ARG TARGETARCH
LABEL org.opencontainers.image.title="pgsi" \
      org.opencontainers.image.description="Postgres insights for Kubernetes clusters (see requirements.md)" \
      org.opencontainers.image.source="https://github.com/frankellis/pgsql-insights" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/pgsi /usr/local/bin/pgsi

USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/pgsi"]
CMD ["serve"]
