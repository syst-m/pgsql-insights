# pgsql-insights — build entrypoints (R11.11)
#
# Every target here is the local mirror of a step in .github/workflows/ci.yml.
# If it works here it must work there; CI never runs a command this file lacks.

SHELL := /bin/bash
MODULE := github.com/frankellis/pgsql-insights

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# D6: full SHA, not --short. The first published binary carried a commit that was
# not its own commit; a 7-character string is trivially copied from an older
# build and hard to trace back, a 40-character one is not. CI pins the
# relationship (see .github/workflows/ci.yml "version output carries ldflags").
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
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

.PHONY: all build test cover vet lint lint-go openapi openapi-check tidy-check web test-web typecheck-web \
       helm-lint render render-check kustomize rules-check image image-size push-local licenses \
       licenses-check licenses-selftest deploy verify clean help

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

## web: build the SPA bundle into web/dist for go:embed (R10.1, R11.4).
# `npm run build` is `tsc --noEmit && vite build`, so this target typechecks too.
web:
	@cd web && npm ci --no-audit --no-fund && npm run build

## typecheck-web: tsc --noEmit over web/src + vite.config.ts (R16.7)
typecheck-web:
	@cd web && npm run typecheck

## test-web: the R16.7 web harness — Vitest unit tests, then the Playwright
# visual suite against the built bundle. `web` runs first so Playwright
# screenshots the exact files go:embed ships, not a dev-server transform.
test-web: web typecheck-web
	@cd web && npm run test
	@cd web && npx playwright install --with-deps chromium
	@cd web && npm run test:e2e

## cover: coverage report against the 70% gate on internal/... (R11.11)
cover:
	@mkdir -p dist
	@go test ./... -count=1 -coverprofile=dist/coverage.out >/dev/null
	@total=$$(go tool cover -func=dist/coverage.out | awk '/^total:/ {gsub("%","",$$3); print $$3}'); \
	echo "total coverage: $$total% (gate 70%, R11.11)"; \
	awk -v t="$$total" 'BEGIN { exit (t+0 >= 70 ? 0 : 1) }' || \
		{ echo "FAIL: coverage $$total% is below the 70% gate (R11.11)"; exit 1; }

## lint: go vet + golangci-lint when installed (R11.11)
lint: vet lint-go openapi-check cardinality-check sha-check as-built-check

vet:
	go vet ./...

lint-go:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed locally; go vet ran instead (CI runs golangci-lint with .golangci.yml)"; \
	fi

## openapi: regenerate api/openapi.yaml from its inputs (R8.1b).
# The document is generated output now: internal/api/openapi_gen.go's
# GenerateOpenAPI() assembles it from the route table (a go/ast walk over
# internal/api), the annotation table (internal/api/openapi_annotations.go) and the
# planned-route manifest (api/x-planned-paths.yaml). So editing api/openapi.yaml by
# hand is not how the document changes — the edit survives until the next run of this
# target, which is the argument for the annotation table being the editable file.
# Deliberately NOT a dependency of openapi-check below: the byte-compare's value is
# that a committed file nobody regenerated fails, and chaining the rebuild into the
# check would regenerate the evidence before grading it. CI gets the comparison from
# `go test ./internal/api` and the structure from openapi-check; this target is the
# developer's half.
openapi:
	go test ./internal/api -run TestGeneratedOpenAPI -update

## openapi-check: validate api/openapi.yaml (R8.1's "checked in CI" half).
# The document is the one api/embed.go ships and GET /api/v1/openapi serves, so a
# broken document is a broken endpoint — R8.1 asks for it validated in CI, and
# api/embed_test.go's header names this target as the gate. scripts/check-openapi.py
# parses it (PyYAML when importable, a stdlib line scan otherwise, so it runs on a
# runner with no pyyaml) and checks what a hand-written 3.0 document actually
# breaks on: duplicate keys, dangling $refs, missing operationIds, non-/api/v1
# paths, and — since R8.3 — that both metrics routes enumerate their catalogues
# and that the two lists are disjoint.
openapi-check:
	python3 scripts/check-openapi.py

## as-built-check: verify docs/architecture.md's as-built table against the tree (R14.3).
# The table's "Where it lives" column is a hand-maintained copy of `git ls-tree -r
# --name-only HEAD` -- every file count, test count, component count and file name in it
# is derivable from git, and every one rots the moment anyone commits a file without
# editing the row. The script regenerates the derivable half and diffs it, leaving Box
# names, Requirement ids and Status prose byte-for-byte alone: the same fix
# api/openapi.yaml got from openapi_gen.go -- a derived artifact gets a generator or it
# rots.
as-built-check:
	python3 scripts/gen-as-built-table.py --check

## cardinality-check: R13.4's assertion — the exported series count for the fixture
## cluster set, computed from requirements.md §5's family table.
## R13.4 asks for the budget "per §5 … with a CI-checkable assertion that the exported
## series count for a fixture cluster set stays under the documented bound". The budget
## is documented (chart/README.md, R9.10); this is the assertion half, which chart/README.md:229
## names as "the artifact that settles it". It parses the table and the fixture rather than
## carrying a literal, because a literal would be a second hand-computed number able to drift
## from the table it supersedes — the defect class that left three different totals in three
## files. Counting the live /metrics is not an option yet: the binary exports one metric
## (pgsi_build_info), so that check would be `1 < 24 480`.
cardinality-check:
	python3 scripts/cardinality-check.py --quiet

## sha-check: D6 — no script in scripts/ stamps anything with a short SHA.
# D6's rule: a short SHA is not a valid version stamp. ci.yml:43-51 is the record — the
# old CI check was `grep -q "$(git rev-parse --short HEAD)" version.txt`, which a binary
# stamped with some *other* commit could pass, and the first published binary did exactly
# that (stamped a47e149, built from db4f237). The fix compared the binary's stamp to
# $GITHUB_SHA, and in doing so removed CI's only use of the flag; nothing was left behind
# to catch a *script* using it, which is how scripts/build-push-dev.sh:20 shipped
# `COMMIT=${COMMIT:-$(git rev-parse --short HEAD)}` and stayed green until A4's 6a9ba00.
# The build job's step gates the binary; this gates the scripts.
#
# Breadth, decided rather than inherited: D6 forbids the short SHA as a *stamp*, and this
# greps every .sh under scripts/ instead of only stamping assignments. Scoping it to
# assignments would mean re-authoring D6's rule as a regex — which variable names count as
# a stamp, in which of the ways a script can assign one — and an invented pattern is a
# second copy of the contract it is checking. The wide form's worst case is failing on a
# comment that mentions the flag, and the fix there is to reword the comment; the narrow
# form's worst case is a silent miss.
#
# Grep dialect, probed 2026-11-24 because a recipe's portability claim has no checker: make
# runs this under /bin/sh, which resolves `grep` to BSD grep 2.6.0-FreeBSD here (and GNU
# grep on the ubuntu-latest runner); both honour --include. An interactive zsh on this box
# resolves `grep` to a shell function from ~/.claude/shell-snapshots wrapping ugrep 7.8.4,
# which honours it too — probed by grepping a directory holding both a matching .sh and a
# matching .py, which returned only the .sh under each binary.
# `/bin/grep` itself is absent here (A4's finding, confirmed: `test -x /bin/grep` → no, and
# /bin is a 39-entry directory rather than a symlink to /usr/bin), so the recipe must resolve
# `grep` through PATH — a target that hardcoded /bin/grep would fail on this machine even
# though /usr/bin/grep is present.
sha-check:
	@if grep -rn --include='*.sh' 'rev-parse --short' scripts/; then \
		echo 'FAIL: a script uses git rev-parse --short — D6 forbids a short SHA as a version stamp'; exit 1; fi

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

## kustomize: R11.13 — build the deploy/kustomize overlay over the generated plain-yaml.
# The flag is the whole point of this target, not decoration. kustomize v5.8.1 (the
# version kubectl v1.36.1 bundles) defaults to --load-restrictor=LoadRestrictionsRootOnly,
# and this repo's only kustomization has exactly one resource, `../plain-yaml/all.yaml` —
# one directory ABOVE its own root, which that restrictor rejects outright:
#   error: accumulating resources: accumulation err='accumulating resources from
#   '../plain-yaml/all.yaml': security; file '.../deploy/plain-yaml/all.yaml' is not in
#   or below '.../deploy/kustomize'': must build at directory: '...all.yaml': file is
#   not directory
# The restrictor cannot be set in the file at this version — `loadRestrictions: none`
# parses as `invalid Kustomization: json: unknown field "loadRestrictions"` — so the
# command-line flag is the only form that builds (probed 2026-11-24: bare command rc=1,
# with the flag rc=0 and 9 kinds in the output). ci.yml ran the bare command, so the
# helm job failed there while no Makefile target existed to mirror it (R11.11); the flag
# now lives here and ci.yml calls `make kustomize`.
kustomize:
	@kubectl kustomize deploy/kustomize --load-restrictor=LoadRestrictionsNone > /dev/null

## rules-check: promtool check rules on the rendered PrometheusRule (R9.11).
## The rule set is disabled in all three profiles, so `make render` never renders
## it — this renders it with the flag that turns it on. promtool cannot parse the
## monitoring.coreos.com/v1 envelope itself (it wants a bare {groups: [...] }
## document), so spec.groups is extracted first: same rules, the shape promtool
## accepts. Uses $(PROMTOOL) when set, otherwise $(PROMTOOL_IMAGE) via docker.
## Note this checks syntax and PromQL parseability only — it says nothing about
## whether the rules fire correctly (see the header of the template).
PROMTOOL       ?=
PROMTOOL_IMAGE ?= docker.io/prom/prometheus:v2.55.1
rules-check:
	@set -e; out=/tmp/pgsi-rules-$$(date +%s).yaml; \
	helm template pgsi $(CHART_DIR) -n $(NS) -f $(CHART_DIR)/values-production.yaml \
		--set metrics.prometheusRule.enabled=true \
		-s templates/prometheusrule.yaml 2>/dev/null > $$out.crd; \
	python3 -c "import yaml,sys; d=yaml.safe_load(open(sys.argv[1])); \
		open(sys.argv[2],'w').write(yaml.safe_dump({'groups': d['spec']['groups']}, sort_keys=False))" \
		$$out.crd $$out; \
	if [ -n "$(PROMTOOL)" ]; then $(PROMTOOL) check rules $$out; \
	else docker run --rm --entrypoint=/bin/promtool -v $$out:/rules.yaml:ro $(PROMTOOL_IMAGE) check rules /rules.yaml; fi; \
	rm -f $$out $$out.crd

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
	@# R11.4's second number, from the same tool: the embedded bundle's bytes.

## push-local: publish both platforms without docker login (scripts/push-oci.py, IMPL §5)
push-local:
	@sh scripts/build-push-dev.sh $(REGISTRY) $(REPO) $(TAG)

## licenses: regenerate the dependency manifest (R15.3) — Go modules + SPA licenses
# scripts/gen-licenses.py reads go.mod and web/package-lock.json, both committed, so
# it needs no Go SDK, no npm and no network. The shell one-liner this replaces shelled
# out to `go list -m all` and stamped the output with `# Generated: <timestamp>` — the
# timestamp meant the file could never be diffed, which is why the target stayed a
# report you ran by hand. Git records the date.
#
# GOOS/GOARCH are pinned because go-licenses-report.csv is not host-independent, which
# is the half of "byte-stable" the sentence above this one used to claim without
# qualifying it: `go-licenses report ./...` resolves the import graph for the host
# platform, so one commit yields 67 rows on darwin (golang.org/x/sys,
# github.com/mattn/go-isatty, github.com/ncruces/go-strftime) and 65 on linux
# (golang.org/x/sys/unix, neither of the other two). The committed file was the darwin
# form and CI runs on ubuntu, which is the whole of the `license manifest (R15.3)`
# failure at 56fd5b7 — regenerating on a mac cannot fix it, because the mac's answer is
# the one already committed. Pinning the resolution target to linux/amd64 gives the
# artifact one form on every host, and it is the form CI's check compares against; the
# two manifest files were always host-independent, since they come from go.mod and the
# lockfile rather than from a build.
licenses:
	GOOS=linux GOARCH=amd64 python3 scripts/gen-licenses.py

## licenses-check: R15.3's "regenerated in CI" half — the gate, not the report.
# `make render-check` is the house precedent for a generated file that must be
# committed (R11.13); this is the same gate for docs/licenses. It uses the script's
# --check rather than `git diff --exit-code` because the diff of a 176-line lockfile
# buries the answer, while --check names the stale files. Once the files match it
# goes on to assert docs/licenses/README.md's own ten restated counts against them:
# the README is prose the generator does not write, so it is the one copy of those
# numbers nothing regenerated, and it stayed wrong from a8da067 to d2b08f5.
# Until this target existed, docs/licenses/README.md claimed "CI regenerates it" while the target appeared
# nowhere in .github/workflows/ci.yml — the false-claim class at IMPLEMENTATION.md
# §5, and the reason the fix has to be the wiring rather than another README edit.
# ci.yml calls this step directly instead of folding it into `lint:` — the same shape
# as `scripts/check-parity-matrix.sh`, which the lint job runs without a make target.
# Folding it in would have made `make lint` require go-licenses on every machine.
# The GOOS/GOARCH pin is `licenses:`'s, above, and has to be repeated here: --check
# regenerates and compares, so a gate that resolves a different platform than the
# generator answers a different question than the generator answers, which is R11.11's
# one-contract rule one level down (the same rule that says CI runs no command the
# Makefile lacks — here, the same rule that says the check runs the same command).
licenses-check:
	GOOS=linux GOARCH=amd64 python3 scripts/gen-licenses.py --check

## licenses-selftest: the negative tests for licenses-check's README assertion.
# scripts/gen-licenses-selftest.py mutates each of the ten counts the README
# restates, one at a time, and requires the failure to name the one it broke —
# the evidence that check_readme_counts grades instead of always returning 0.
# It imports the script and reads the committed manifests, so it needs no
# toolchain and runs on a machine where licenses-check cannot go green because
# go-licenses is not on PATH; there, it is the only way to exercise the README
# half at all. docs/agents.md §5 rule 11 is the rule this target exists to allow.
licenses-selftest:
	python3 scripts/gen-licenses-selftest.py

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
