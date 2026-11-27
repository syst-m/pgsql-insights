# Phase 1 — Discovery + `/api/v1/clusters` (1a) and the embedded SPA (1b)

## Status

1a — 🔄 in progress (A1). Deviations [§5-D4, §5-D6, §5-D16, §5-D17](../deviations.md).
1b — ✅ done (A4). Deviations [§5-D7–§5-D12](../deviations.md).

## Owners

**1a (A1)** — `internal/discovery/`, `internal/kube/`, `internal/logx/`, `internal/api/dto/`,
`internal/api/clusters.go`, `internal/api/readiness.go`, `cmd/pgsi/main.go`.
**1b (A4)** — `web/` (SPA, `dist/`, `embed.go`), the R10.2 design system, the R16.7 harness.
**1a's packaging half (A4)** — `Makefile`, `internal/version/version.go`,
`chart/templates/_helpers.tpl` (the `poolers` verbs), the three regenerated
`deploy/plain-yaml/` profiles, and the §5-D6 commit-stamping fix in `.github/workflows/ci.yml`.

## Task breakdown

1a and 1b share one phase and one validation entry; R14.9's one-entry-per-owner rule holds because
each owner wrote their own half of the entry and their own R14.8 table below, rather than one agent
transcribing the other's. The 1b rows are A4's (`14d4df0`, `4d5955c`), the 1a rows are A1's.

**Where the 1a commits are.** The contract freeze is `9214534`, the DTO work `93fcd44`, and the
Phase 1a sources — `internal/{discovery,kube,logx}`, `internal/api/{clusters,readiness,server}.go`
and their tests, `go.mod`/`go.sum` — are `362e8d3`. Getting there took two rounds of the same
index-sweep mistake: 21 of A4's files shipped inside A2's one-line `R8.1b: correct the operation
count to 15` (`f016d02` → `5dd855f` → `4e10ed4`), and the coordinator's split of `4e10ed4` into
`362e8d3`/`3c18f45`/`c8dcf43` reset the branch to `a5b851c` and dropped the three re-landed
commits. The net state is the union of both. Two things in `362e8d3` are not A1's and are on record:
16 lines of A2's `internal/api/server.go` and 6 lines of `.github/workflows/ci.yml`; and
`scripts/check-openapi.py` stays untracked there, so `make openapi-check` — the target this
commit's Makefile adds and the ci.yml step calls — cannot pass until A2 lands it. The rule that
came out of it is [`docs/agents.md`](../../agents.md) §5.1 (`ecd89c7`), restated in
[conventions.md](../conventions.md).

| # | Task | Req | Owner | Status |
|---|---|---|---|---|
| 1a.1 | kube client + per-port transport table (R1.10/R1.11), the discovery pass over the reference namespaces (informers + the CRD→DTO builders), and the R13.7 logging surface | R1.1–R1.11, R12.1–R12.3, R13.7, R13.8 | A1 | ✅ |
| 1a.2 | `GET /api/v1/clusters` — the contract `web/src/api/types.ts` mirrors — plus R8.7's single readiness signal behind `/healthz`, `/readyz` and the API's own 503 path | R8.1, R8.7, R1.5 | A1 | ✅ |
| 1b.1 | `web/` project: Vite 8 + React 19 + TS 5.9, `npm run build` → `web/dist` with un-hashed asset names (§5-D14) | R10.1, R10.2 | A4 | ✅ |
| 1b.2 | `web/embed.go` (`//go:embed all:dist`) + `internal/api/spa.go` mounted at `GET /`, with the API-namespace and missing-asset guards | R10.1 | A4 | ✅ |
| 1b.3 | Design system: `styles/tokens.css` (the validated palette as CSS custom properties, declared under both `@media (prefers-color-scheme)` and `:root[data-theme]`) + `styles/base.css` | R10.2 | A4 | ✅ |
| 1b.4 | App shell + URL-first router (`state/urlState.ts`: parse / format / pushState / popstate) | R10.16 | A4 | ✅ |
| 1b.5 | **Fleet page**: header with the R10.22 refetch/partial banner, the health-distribution chart (legend table twin + hover), the sortable cluster table (health, cluster, namespace, instances serving/reported, PG major, age) with R10.21 timestamps | R10.4 | A4 | ✅ |
| 1b.6 | Cluster detail page (instances, PVCs, services, poolers, conditions — every field the R1.5 contract carries) | R10.4 | A4 | ✅ |
| 1b.7 | R10.12's named page states as URL-addressable fixtures (`?fixture=fleet\|empty\|partial\|unavailable`, `?delay=ms` for loading) | R10.12 | A4 | ✅ |
| 1b.8 | R16.7 harness: Vitest (9 files, 149 tests at Phase 1b; **13 files, 227 tests at `2648f99`** — A6's Phase 6b component tests and `routes.test.tsx` landed after this row was written) + Playwright (15 tests, 9 committed baselines), both in CI | R16.7, R16.8 | A4 | ✅ |
| 1b.9 | Build plumbing: Makefile `web` / `typecheck-web` / `test-web`, `Dockerfile` `COPY web/`, `.dockerignore`, CI `web` job, `scripts/image-size.py --spa-bundle` | R11.4, R11.11 | A4 | ✅ |

## Validation entry (R14.2)

### Phase 1a (A1)

Run 2026-11-22 on the arm64 dev machine, go 1.26.0, no cgo. Scope is spec §7.2's Phase 1a row:
R1.1–R1.9, R1.9b, R1.10, R1.11, R8.1, R8.7, R12.1–R12.3, R13.7, R13.8. Full text at
[IMPLEMENTATION.md](../../../IMPLEMENTATION.md) §3.5; the short version:

- **Build, vet, lint (R11.11)** — `go build ./...` and `go vet ./...` clean; `make lint` runs the
  CI-pinned golangci-lint v1.64.8 against `.golangci.yml` (the install §5-D4 asked for, closing it)
  and exits 0 with zero findings repo-wide. The eight findings the first draft listed in A2's and
  A4's files landed in `134206e`; two of A1's nine fixes (`kube/client.go:54` revive,
  `discovery/discovery.go:582` errcheck on `AddEventHandler`) were visible only in the full output,
  not the tail — the second fixed by making `registerHandlers` a `*Discoverer` method that logs at
  Warn, because an informer that never delivers events leaves a fleet that silently stops updating.
- **Tests (R14.5)** — `go test ./... -count=1`, all packages `ok`:

| Package | Test funcs | Run (incl. subtests) | Owner |
|---|---|---|---|
| `internal/discovery` | 15 | 34 | A1 (this commit) |
| `internal/kube` | 11 | 28 | A1 (this commit) |
| `internal/logx` | 9 | 41 | A1 (this commit) |
| `internal/api/dto` | 5 | 5 | A1 (this commit) |
| `internal/version` | 1 | 1 | A1 (this commit) |
| `internal/api` | 74 | 312 | A1 owns `server_test.go` (7) + `clusters_test.go` (10); the rest are A2's auth (16) / cors (7) / spa (5) / promql (17) / series (12) tests — **corrected in this commit (A3 re-review)**: the row said 50 / 126, which matches no tree — `45a69a1`, the commit that authored it, had 45 (auth 16 + clusters 10 + cors 7 + server 7 + spa 5), and HEAD has 74; see VA2 |
| `api` | 1 | 1 | A2 (the OpenAPI embed) |

- **Acceptance (spec §7.2:660)** — the four reference clusters checked field by field against
  `testdata/api/clusters.json` by `TestReferenceEnvironmentAcceptance` and re-derived from the CRD
  shapes by `internal/discovery/build_test.go`.
- **The fixture as contract (R16.8)** — `testdata/api/clusters.json` has exactly three consumers
  reading the same bytes: `internal/api/dto/clusters_test.go` (re-marshals and fails on any drift),
  `internal/api/clusters_test.go` (the handler body), and `web/src/api/fixtures.ts` (imported and
  annotated `Envelope<ClusterView>`, so `tsc` is the drift check — §5-D12). No second copy.
- **R8.7 — one readiness signal, read by both surfaces** — `internal/api/readiness.go`'s `Readiness`
  has one read path and two documented writers; `Server.New` derives it from `Config.Fleet` so a
  handler cannot hold a different readiness from the probe. With the flag off, `/readyz` answers 503
  *and* `GET /api/v1/clusters` answers the R8.5 `ErrNotReady` 503 from the same value — removing the
  old `Run()` setting `ready` at `net.Listen`, which let a pod answer 200 with an empty fleet and
  still pass its probe. Readiness covers only the subsystems the process runs, so an API-only pod
  (§5-D9) is ready once listening.
- **R13.7 / R13.8** — `internal/logx` is the structured-JSON logger with `--log-level` and both
  `--log-field k=v` and CNPG's `--log-field-k=v` spelling (a pre-scan of `os.Args`, since stdlib
  `flag` has no dynamic flag names — §5-D16); 9 test funcs / 41 runs, 96.6%. R13.8's drain is
  `Run`'s `ctx.Done()` branch with a fresh `context.Background()` inside the grace budget.
- **§5-D6 closed (commit stamping)** — `Makefile` stamps `COMMIT ?= $(shell git rev-parse HEAD …)` of
  the *built* tree, both image jobs pass the full 40-char SHA as `--build-arg`, and CI parses the
  commit back out of `./bin/pgsi version` and fails unless it equals `$GITHUB_SHA` and its 7-char
  prefix equals the tag.
- **Coverage (R11.11's 70% gate)** — the recipe's `awk` had `$3` where a Makefile needs `$$3`, so
  the gate read a blank number and failed on every run; fixed. At the 1a commit the repo read
  54.4% (§5-D17), and `make cover` at HEAD prints **70.6% and exits 0** — `internal/api` moved
  53.1% → 89.1%, the empirical answer to §5-D17's own follow-up about the aggregate being hostage to
  whichever package is newest.
- **A3's code review of the 1a file set** (run 2026-11-22) — DI1 (major, `discovery.go:729-740` the
  R1.9 sweep iterates the wrong map so `d.deleted` grows), DI2 (major, `discovery.go:691`/`:713`
  emit `Code:` values the contract forbids), DI3 (minor, `build.go:358` hardcodes R1.10's
  monitoring-TLS flag to `false`), DI4 (minor, `discovery.go:722` re-stamps the version cache from
  the DTO and the re-stamp lies), DI5 (minor, `build.go:400-423` returns the first prefix match so
  the metrics sidecar can win), DI6 (major, two call sites drop the `nestedStringMapE` error),
  KB1 (major, `kube/client.go:183-188` reads the kubeconfig's CA fields and then never uses them),
  and VA1–VA5 against the entry itself (VA1 blocking: the commit carrying the entry did not compile;
  VA2: the test table's `internal/api` row read `50 | 126`, matching no tree; VA3: the coverage
  number; VA4: the lint queue; VA5: `truncated ⟺ len(data) < total` was not R8.4's invariant).
- **Not yet observed** — a run of the binary against the live k3s cluster. The handler is validated
  against the committed fixture and the builder against CRD fixtures; `discovery.go`'s informer
  plumbing is exercised only through `NewWithClients` in tests.

### Phase 1b (A4)

Full text at [IMPLEMENTATION.md](../../../IMPLEMENTATION.md) §3.5; the short version:

- **Toolchain (R10.1)** — react 19.3.1, typescript 5.9.3, vite 8.3.8 (the rolldown-based build:
  `build.rollupOptions` is deprecated there, so the config uses `build.rolldownOptions`),
  vitest 4.1.12 pinned with `~` (vitest 5's `engines` exclude node 25, which is what this machine
  runs, and a floating `^4` would let npm resolve a version whose tests cannot run),
  @playwright/test 1.65.1, @visx 4.0.0 over d3-scale 4.0.3 (§5-D12).
- **Typecheck + unit tests (R16.7)** — `tsc -p tsconfig.json --noEmit` clean, zero errors;
  `npx vitest run` **9 files, 149 tests, all passing**. The count is 105 → 129 → 149: +24 from the
  `api/client.test.ts` `parseEnvelope`/`parseErrorBody`/`toFleetResponse`/`partialBanner` suites
  that test the contract boundary instead of a cast (§5-D12), +18 from `routes.test.tsx`
  (`af433cc`). The first run failed 16 tests, every one a real defect or a wrong expectation
  (§5-D8's list); notably `utcTitle` re-serialised timestamps through `Date`, appending a `.000`
  milliseconds field the API never sent.
- **Build + embed (R10.1, R11.4)** — `npx vite build` → `web/dist/index.html` 538 B,
  `assets/index.css` 8 971 B (2 378 gz), **`assets/index.js` 288 259 B (88 410 gz)** — the number
  R11.4 previously reported as `n/a`. `base` is `/` (§5-D14), so the bundle is addressable from any
  deep route. The +12 854 B over the pre-D12 figure is `testdata/api/clusters.json` imported into
  the bundle instead of retyped as TS; R11.4 sets no ceiling, only that it be reported separately.
  `go build ./...` and `go test ./internal/... -count=1` pass with the embed in place;
  `internal/api/spa_test.go` adds 5 tests (shell at `/`, asset served + cached, deep-route fallback,
  `/api/…` 404s as JSON, a missing asset 404s instead of returning the shell). `make image-size`
  prints the SPA section from the same tool (`--spa-bundle`, default `web/dist`).
- **Visual regression (R16.7)** — `npx playwright test` **14 passed**, against `vite preview` on the
  built bundle, not the dev server, so what is screenshotted is byte-identical to what `pgsi serve`
  embeds. 9 baselines in `web/e2e/__snapshots__/`: eight from the spec's `SCREENS` table (the four
  R10.12 states, the populated `fleet` view, the `detail` and `queries` routes, and since `af433cc`
  the `planned` placeholder) and the ninth the dark-mode capture (R10.13's media-query path, which a
  light-only screenshot cannot prove). The six interaction tests are assertions without screenshots,
  so they raise the test count without raising the baseline count. `playwright.config.ts` pins
  `deviceScaleFactor: 1` and `reducedMotion: "reduce"` — without a fixed scale factor a Retina and a
  CI capture differ in every pixel — and `webServer.command` passes `--host 127.0.0.1` because
  `vite preview` binds `localhost`, which on macOS resolves to `::1` first.
- **Not yet observed** — the SPA against A1's live `/api/v1/clusters` (§5-D7), and a green CI run
  (§5-D3, plus §5-D9: the workflow file was not valid YAML at HEAD and is fixed here).

## Requirement → file → status (R14.8)

### Phase 1a

| Req | Requirement (abridged) | File(s) | Status |
|---|---|---|---|
| R1.1 | The discovery watch set (Cluster, Pooler, pods, Services, PVCs, Secrets) across the reference namespaces, with the namespace deny list | `internal/discovery/discovery.go` (`namespaces`, `startInformers`, `registerHandlers`, `snapshot`) | **implemented** — the watch set is one `nsStore` per namespace holding five informers; `namespaces()` applies the deny list before any informer is built, so an excluded namespace is never listed and never appears in `meta.failed`. `unstructured.GetCreationTimestamp()` returns a **Local**-zone time, which is why every timestamp on the wire goes through `.UTC()` — the fixture's `2026-11-20T09:12:00Z` and the builder's output now agree byte for byte |
| R1.2 | Instance rollup, incl. **[A]** the pod-is-Running-but-postgres-is-not case | `internal/discovery/build.go`, `internal/api/dto/clusters.go` (`InstanceView`: `phase`, `ready`, `serving`, `containerStatus`, `restartCount`, `role`) | **implemented** — `serving` is derived from the container status, not the pod phase, which is the whole `[A]` correction: `freeidp-pg-1` is `Running` + `CrashLoopBackOff` and therefore not serving, while `freeidp-pg-2` is `Pending`/`PodInitializing`. The role fallback chain (`cnpg.io/instanceRole` → `role` → empty) is tested with the empty case, because guessing a role for a pod that has none is how a UI ends up with two primaries |
| R1.3 | PostgreSQL major version per cluster, distinguishing 16 from 17 | `internal/discovery/build.go` (`pgMajorFromVersion`), `dto` (`pgMajor *int`) | **implemented** — parses CNPG's six-digit `server_version_num` (`160008` → 16, `170004` → 17) with the image string as the secondary source, not the image tag as the primary one. The field is a pointer because R1.3's answer is sometimes genuinely not-yet-known, and a `0` rendering as a version is worse than `null`. **`ca9843d` scoped the read**: `internal/discovery/build.go:204-205` (`if in.Pods[i].Labels[labelCluster] != "" && in.Pods[i].Labels[labelCluster] != in.Cluster.GetName() {`) skips any pod labelled to a different cluster, so a co-located second cluster's image string can no longer answer R1.3 for the first; `internal/discovery/build_test.go:318` `TestPGMajorIgnoresOtherClustersPods` pins it |
| R1.4 | Pooler discovery and pod association via `spec.cluster.name` | `internal/discovery/poolers.go` (`buildPoolers`, `poolerPodSet`, `poolerClusterName`, `buildPoolerPod`) | **implemented** — `poolerClusterName` accepts both the CNPG 1.26+ object form and the ≤1.25 bare-string form, so attribution needs no version branch. Pod membership prefers `cnpg.io/poolerName` (the only label that separates two poolers on one cluster) and falls back to `cnpg.io/cluster` + `podRole=pooler` only when **no** pod in the set carries it — a per-pod fallback would double-count the moment one pooler is labelled and the other is not |
| R1.5 | The `ClusterView` field list the API emits | `internal/api/dto/clusters.go` | **implemented** — frozen at `9214534` and accepted as frozen; `TestGoldenFixtureIsCanonical` re-marshals the fixture and fails on any byte of drift, which is the mechanism that makes a later tag change impossible to land silently |
| R1.6 | Zero-prerequisite rendering: a cluster with no Pooler, no Service or no cert still renders | `internal/api/dto/clusters.go` (empty-not-null slices), `internal/discovery/build_test.go`, `testdata/api/clusters.json` | **implemented** — `services`/`poolers` are initialised to empty slices so they serialise as `[]`, never `null`; three of the four reference clusters have no Pooler and all four render |
| R1.7 | The kube client and its transport choice (direct pod-IP vs `pods/proxy`) | `internal/kube/client.go` (`BuildConfig`, `New`, `Get`, `GetDirect`, `Target`, `UsesProxy`, `PodLogs`) | **implemented** — direct-first when a direct URL exists and `--use-proxy` is false, `pods/proxy` otherwise **and on any direct transport failure**; a non-2xx is not a transport failure, so a 404 from the instance manager is returned rather than retried through the proxy. QPS/Burst are widened from client-go's 5/10 because this process watches every namespace and Phase 2 scrapes every instance |
| R1.8 | Credential resolution and its precedence | `internal/discovery/discovery.go` (`ResolveCredential`), `dto` (`credentialSource` + the three named secret fields) | **implemented** — the resolution is *observable*: every cluster row carries which source answered, so a cluster using the wrong secret is visible in the API rather than only in a connection error. The per-cluster CA plumbing is Phase 2's; `kube.New` documents what is verified today and what is not |
| R1.9 / R1.9b | Certificate expirations and the replication-slot surface | `internal/discovery/build.go`, `internal/discovery/fields.go` | implemented — the cert map is read through `nestedStringMapE`, which returns the error rather than dropping it (a malformed map is a fact R1.5's view has to report, not render as "no entries"); that is why there is one accessor and no error-dropping sibling |
| R1.10 | The per-port transport table (8000 HTTPS always, 9187 HTTP unless `spec.monitoring.tls.enabled`, 9127 for pgbouncer, 8010 pod-local) | `internal/kube/transport.go` (`specFor`, `URLFor`, `OwnerCluster`, `OwnerPooler`), `internal/kube/transport_test.go` | **implemented** — endpoints are built from the table, never by hand (`EndpointsFor` in the discovery layer calls `URLFor` for every pod it reports). The table is the single source for scheme *and* port, which is the only way `freeidp-pg-1`'s `https://…:8000` and `http://…:9187` can both be right in one fixture |
| R1.11 | Tunnelled and non-kubeconfig endpoints (`--api-server-url` + bearer/client-cert) | `internal/kube/client.go` (`BuildConfig`'s explicit-URL branch), `internal/kube/client_test.go` | **implemented** — an explicit URL bypasses the kubeconfig (the tunnelled case, where loading the real cluster's kubeconfig is the wrong thing), and `--client-cert`/`--client-key` must be set together or the build fails. The measured caveat is in the file: `pods/proxy` returns 502 through a tunnel while `exec` and `port-forward` to the same pod both work, which is why direct addressing is the first transport |
| R8.1 | Versioned REST API under `/api/v1/*` with a committed OpenAPI document | `internal/api/clusters.go`, `internal/api/server.go` (A1) · `api/openapi.yaml`, `api/embed.go` (A2) | **implemented (A1)** — the `GET /api/v1/clusters` handler and its route registration. The document's own status is §3.7's R8.1 row (implemented; generated by `internal/api/openapi_gen.go`, with three schema entries authored inside the generator — see R8.1b). The contract the document projects is the one frozen at `9214534` |
| R8.7 | `/healthz`, `/readyz` (true after the first inventory sync), and the rule that readiness covers only what this process runs | `internal/api/readiness.go`, `internal/api/server.go`, `internal/api/server_test.go`, `internal/api/clusters_test.go` | **implemented** — one `Readiness` value, one read path, two documented writers (`SetListening` + the per-subsystem `sources`), derived by `Server.New` from `Config.Fleet` so the handler and the probe cannot disagree. Full text of the three clauses and their tests is in §3.5's 1a half |
| R12.1–R12.3 | Least-privilege RBAC, with the verb set shared between the chart and the plain-yaml profiles | `chart/templates/_helpers.tpl`, `deploy/plain-yaml/all.yaml`, `all-lab.yaml`, `all-production.yaml` | implemented — `poolers` and `poolers/status` added to the CNPG verb set in the helper and in all three generated profiles, in one commit, because `make render-check` (R11.13) renders the chart and diffs the committed files. Without the verb, the pooler watch returns Forbidden on every namespace and R10.22 reports a *failed namespace* — which looks like a broken cluster, not a missing permission |
| R13.7 | Structured JSON logging, `--log-level`, and extra labels in CNPG's flag shape | `internal/logx/logx.go`, `internal/logx/logx_test.go`, `cmd/pgsi/main.go` | **implemented** — 9 test funcs / 41 runs, 96.6% coverage. The logger is built before anything that can talk, so the first log line a process emits is already structured. §5-D16 records that `internal/logx` is not a package in requirements.md §3.5's source layout and why the `--log-field-*` spelling needs a pre-scan |
| R13.8 | Drain in-flight requests on shutdown and exit within `terminationGracePeriodSeconds` | `internal/api/server.go` (`Run`'s `ctx.Done()` branch) | **implemented** — `http.Server.Shutdown` with its own deadline, derived from `context.Background()` rather than from the context that just fired (deriving from it aborts the drain at once). The `//nolint:contextcheck` on that line is load-bearing and carries the comment explaining why |
| R16.8 | The API contract has exactly one copy, consumed by the Go tests and the SPA | `testdata/api/clusters.json` | **implemented** — three consumers, one file: `dto/clusters_test.go`, `api/clusters_test.go`, and A4's `web/src/api/fixtures.ts`. §3.6's R16.8 row and §5-D12 cover the TS side |

### Phase 1b

| Req | Requirement (abridged) | File(s) | Status |
|---|---|---|---|
| R10.1 | The SPA is served by the Go binary at `/`; no separate web server | `web/embed.go`, `internal/api/spa.go`, `internal/api/server.go`, `web/vite.config.ts`, `Dockerfile` | **validated** — `spa_test.go` proves `GET /` and `GET /assets/index.js` from the binary; Playwright renders the same bundle over HTTP; the only network call the app makes is to its own origin |
| R10.2 | Chart library chosen per the `dataviz` skill and recorded in §4 (§5-D12) | `web/package.json` (`@visx/*` 4.0.0 + `d3-scale` 4.0.3), `web/src/styles/tokens.css`, `web/src/components/HealthBar.tsx` | **validated** — §5-D12 unchanged and not re-opened; the chart uses one axis, 2 px marks with rounded data-ends, a legend table twin, direct labels in ink (never the series colour), and the palette validated in both modes (§4's palette note) |
| R10.4 | Fleet page: health distribution + sortable table with phase, instances and PG major | `web/src/pages/Fleet.tsx`, `web/src/components/ClusterTable.tsx`, `web/src/components/HealthBar.tsx`, `web/src/lib/sort.ts` | **validated** — renders all four reference clusters from the R1.5 contract, `freeidp-pg` showing its §5-D7 classification; sorting is tested (numeric collation included) and screenshotted |
| R10.16 | The URL is the single source of truth for view state | `web/src/state/urlState.ts`, `web/src/App.tsx` | **validated** — parse/format round-trip tests for every state field; Playwright asserts a sort click rewrites the URL and Back restores the previous view; `App` derives its data source from the same parsed URL (§5-D14) |
| R16.7 | Vitest + Playwright harness, ≥1 passing visual test per reachable R10.12 state, running in CI | `web/vite.config.ts` (`test` block), `web/playwright.config.ts`, `web/e2e/fleet.spec.ts`, `web/e2e/__snapshots__/`, `.github/workflows/ci.yml` (job `web`) | **validated** — 149 Vitest tests at Phase 1b, and 13 files / 227 tests at `2648f99` (the Phase 6b component suites plus `routes.test.tsx` landed after the Phase 1b count was struck), with 14 Playwright tests, all passing locally; the CI job runs install → build → bundle size → vitest → playwright and uploads the report |
| R16.8 | The API contract has exactly one copy, consumed by both the Go golden test and the SPA's test suite | `testdata/api/clusters.json`, `web/src/api/types.ts`, `web/src/api/envelope.ts`, `web/src/api/fixtures.ts` | **validated** — see §5-D12. `fixtures.ts` imports the contract JSON and annotates it `Envelope<ClusterView>`, so the TS type and the Go fixture cannot disagree without `tsc` failing; the 386-line hand-transcribed duplicate is gone |
