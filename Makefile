# pgsql-insights — build entrypoints (R11.11)
#
# Every target here is the local mirror of a step in .github/workflows/ci.yml.
# If it works here it must work there; CI never runs a command this file lacks.

SHELL := /bin/bash
MODULE := github.com/frankellis/pgsql-insights

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# R11.3: the same values feed the binary, the OCI labels, /api/v1/version and pgsi_build_info.
LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)

# R11.16 / Q6: ghcr.io/<org>/pgsql-insights, org provisional (see README).
# Override per environment, e.g. the reference cluster's own registry:
#   make image IMAGE=10.10.111.134:5000/frankellis/pgsql-insights:dev
IMAGE     ?= ghcr.io/frankellis/pgsql-insights:dev
# R11.2: multi-arch is a Phase 0 blocker — all five reference nodes are amd64
# while the dev machine is arm64 (live-probe.md §7).
PLATFORMS ?= linux/amd64,linux/arm64
BUILDER   ?=
BUILDKIT  := $(if $(BUILDER),--builder $(BUILDER),)

CHART_DIR := chart
# R11.12: three named profiles, `default` being the shipped one (values.yaml).
PROFILES  := default production lab
# Extra args for the deploy target, e.g. --set image.repository=...
DEPLOY_ARGS ?=

NS := pgsi

# R11.4: CI fails the build above this; the spec's target is < 60 MB compressed.
IMAGE_MAX_MB ?= 100
# push-local target (overridable): make push-local REGISTRY=ghcr.io REPO=org/pgsql-insights
REGISTRY ?= 10.10.111.134:5000
REPO     ?= frankellis/pgsql-insights
TAG      ?= dev

.PHONY: all build test cover vet lint lint-go tidy-check helm-lint render render-check \
       image image-size push-local licenses deploy verify clean help

all: build

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## build: compile bin/pgsi with the version ldflags (R11.3); cgo off (§3.4)
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/pgsi ./cmd/pgsi

## test: unit tests (R11.11)
test:
	go test ./... -count=1

## cover: coverage report against the 70% gate on internal/... (R11.11)
cover:
	@mkdir -p dist
	@go test ./... -count=1 -coverprofile=dist/coverage.out >/dev/null
	@total=$$(go tool cover -func=dist/coverage.out | awk '/^total:/ {gsub("%","",$3); print $$3}'); \
	echo "total coverage: $$total% (gate 70%, R11.11)"; \
	awk -v t="$$total" 'BEGIN { exit (t+0 >= 70 ? 0 : 1) }' || \
		{ echo "FAIL: coverage $$total% is below the 70% gate (R11.11)"; exit 1; }

## lint: go vet + golangci-lint when installed (R11.11)
lint: vet lint-go

vet:
	go vet ./...

lint-go:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed locally; go vet ran instead (CI runs golangci-lint with .golangci.yml)"; \
	fi

## tidy-check: fail if go.mod is not tidy (R13.10: no replace, tidy in CI)
tidy-check:
	@cp go.mod dist/go.mod.before 2>/dev/null || cp go.mod /tmp/go.mod.before
	@before=$$(cat dist/go.mod.before 2>/dev/null || cat /tmp/go.mod.before); \
	go mod tidy; \
	if [ "$$(cat go.mod)" != "$$before" ]; then echo "FAIL: go.mod changed after go mod tidy (R13.10)"; exit 1; fi

## helm-lint: lint the chart against all three profiles (R11.6, R11.7)
helm-lint:
	@set -e; for p in $(PROFILES); do \
		helm lint $(CHART_DIR) -f $(CHART_DIR)/values-$$p.yaml; \
	done

## render: regenerate deploy/plain-yaml from the chart (R11.13 — the files are generated)
render:
	@set -e; mkdir -p deploy/plain-yaml; for p in $(PROFILES); do \
		out=deploy/plain-yaml/all.yaml; \
		[ "$$p" = "default" ] || out=deploy/plain-yaml/all-$$p.yaml; \
		helm template pgsi $(CHART_DIR) -n $(NS) -f $(CHART_DIR)/values-$$p.yaml > $$out.tmp; \
		mv $$out.tmp $$out; \
		echo "generated $$out (from values-$$p.yaml)"; \
	done

## render-check: the golden test — committed generated files must match the chart (R11.13, R16.4)
render-check: render
	@git diff --exit-code deploy/plain-yaml || \
		{ echo "FAIL: deploy/plain-yaml is stale — run 'make render' and commit (R11.13)"; exit 1; }

## image: multi-arch build + push (R11.2, R11.16) — needs credentials for $(IMAGE)'s registry
image:
	docker buildx build $(BUILDKIT) --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE) --push .

## image-size: R11.4 compressed size, gated at IMAGE_MAX_MB (builds amd64 to an OCI tarball)
image-size:
	@mkdir -p bin
	@docker buildx build $(BUILDKIT) --platform linux/amd64 \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE) -o type=oci,dest=bin/pgsi-amd64.oci.tar .
	@python3 scripts/image-size.py --max-mb $(IMAGE_MAX_MB) bin/pgsi-amd64.oci.tar
	@echo "SPA bundle: n/a in Phase 0 (Phase 5) — R11.4 reports it separately from the image size"

## push-local: publish both platforms without docker login (scripts/push-oci.py, IMPL §5)
push-local:
	@sh scripts/build-push-dev.sh $(REGISTRY) $(REPO) $(TAG)

## licenses: regenerate the dependency manifest (R15.3 scaffold)
licenses:
	@mkdir -p docs/licenses
	@{ echo "# Dependency manifest — generated by 'make licenses', do not edit."; \
	   echo "# Source: go list -m all (module $(MODULE))"; \
	   echo "# Generated: $$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
	   echo "# Full per-dependency license text is produced by go-licenses once deps exist (R15.3, Phase 9)."; \
	   go list -m all; } > docs/licenses/go-dependencies.txt.tmp
	@mv docs/licenses/go-dependencies.txt.tmp docs/licenses/go-dependencies.txt

## deploy: install/upgrade the chart into the reference cluster (R11.14)
deploy:
	helm upgrade --install pgsi $(CHART_DIR) -n $(NS) --create-namespace \
		-f $(CHART_DIR)/values-default.yaml $(DEPLOY_ARGS)
	@kubectl -n $(NS) rollout status deploy/pgsi --timeout=180s

## verify: R11.14 evidence — pod Running + /healthz answering through the Service
verify:
	@kubectl -n $(NS) get pods -l app.kubernetes.io/name=pgsi -o wide
	@pid=$$(kubectl -n $(NS) port-forward svc/pgsi 18080:8080 >/dev/null 2>&1 & echo $$!); \
	sleep 3; \
	echo "--- GET /healthz (via Service)"; curl -fsS http://127.0.0.1:18080/healthz; \
	echo "--- GET /api/v1/version (via Service)"; curl -fsS http://127.0.0.1:18080/api/v1/version; \
	echo "--- GET /metrics (via Service)"; curl -fsS http://127.0.0.1:18080/metrics; \
	kill $$pid 2>/dev/null

## clean: remove build output
clean:
	rm -rf bin dist
