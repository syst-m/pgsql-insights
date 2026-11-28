# Phase 6 — Active Queries & Wait Events (6a) and Query Insights UI (6b)

One phase, split in v0.2.0 into two owned halves. Both halves are here; the phase index row in the
[README](../README.md) is the single WBS entry they both belong to.

## Status

- **6a** — 🔄 In progress (A1). Deviation [§5-D20](../deviations.md).
- **6b** — 🔄 In progress (A6). No deviation rows in [deviations.md](../deviations.md) carry phase `6b`.

## Owners

**6a** — A1 (the phase's owner per the WBS row): `internal/collector/activity.go` + test,
`internal/collector/locks.go`, the two registry entries in `cmd/pgsi/main.go`. The phase's twelve
ids also name A2's API files (`internal/api/active_queries.go`, `internal/api/sessions.go`,
`internal/api/dto/sessions.go`, the two `server.go` mounts, the two `api/openapi.yaml` paths) and
A6's pages (`web/src/pages/ActiveQueries.tsx`, `web/src/pages/WaitEvents.tsx`) with A4's routes and
fetchers; those are listed in 6a's table and validated there, not re-implemented here.
**6b** — A6: `web/src/pages/**` (15 files) and `web/src/components/**` (37 files).

## 6a — Active queries, blocking chain, wait events, terminate

### Task breakdown

§3.17 carries no task table; the phase's WBS row in the [README](../README.md) is the task list.

### Validation entry (R14.2)

Full text at `1b11199:IMPLEMENTATION.md` §3.17; the short version:

- **Scope, stated as a file list.** Eleven tracked files across three owners, and the list is the
  union of what the phase's twelve requirement rows name. Mine: `internal/collector/activity.go`
  (900 l, `49b3210`, last touched `e043850`), `internal/collector/activity_test.go` (985 l,
  `49b3210`, last touched `8099036`), `internal/collector/locks.go` (469 l, `b51ea03`, last touched
  `e043850`), and the two registry entries at `cmd/pgsi/main.go:1054` and `:1058`. A2's:
  `internal/api/active_queries.go` (274 l, `e6a4921`, last touched `3377848`),
  `internal/api/sessions.go` (335 l, `db265b6`), `internal/api/dto/sessions.go` (317 l, `e6a4921`
  + `c5d1772`), and the two `internal/api/server.go` mounts (`server.go:497`, `:506`) with the two
  `Config` fields they read (`server.go:228`, `:244`). A6's: `web/src/pages/ActiveQueries.tsx`
  (767 l, `99f37f0`, last touched `6e10be8`) and `web/src/pages/WaitEvents.tsx` (410 l, `be7f042`,
  last touched `8fe4f32`). A4's: `web/src/routes.ts:220` and `:231`, both tagged `phase: "6a"`
  (`af433cc`, last touched `baba6dc`), and the two fetchers at `web/src/api/client.ts:559` and
  `:607`. The two OpenAPI paths are `api/openapi.yaml:137` and `:247` (A2's, last touched `0147824`).
- **Commands run, in one call, at `25f84e4`.** `go build ./...` rc=0 · `go vet ./...` rc=0 ·
  `go test -count=1 ./internal/collector/ ./internal/api/ ./internal/api/dto/` rc=0 ·
  `bash scripts/check-parity-matrix.sh` rc=0. The build and vet results are captured as
  `out=$(go build ./... 2>&1); rc=$?` rather than piped into `tail`, because a pipeline's `$?` is
  the last command's status and this file has previously recorded a false `build exit=0` from
  exactly that form.
- **The composition, from §3's table.** The WBS row at `:120` claims twelve ids and this entry rows
  all twelve: six implemented (four mine — R4.2, R4.4, R4.6, R4.8 — and two A6's, R10.8 and R10.9),
  four in progress (R4.1, R4.3 and R4.7 mine, R4.5 A2's), two not started (R10.25 and R10.26, both
  A6's). The row is 🔄 and not ✅ because two of the twelve ids have no file anywhere in the repo,
  and R14.8's rule is that a phase row cannot read done while a requirement it names has no
  implementation. It is 🔄 and not ⚠️ because nothing here is done-with-deviations in the Phase 0
  sense — it is done-but-not-closed, the shape §3.16's Phase 4 row takes for the same reason.
- **Findings — three, one of which is a correction to the review this entry answers.**
  1. The two ids reported as having no code are built. The review that scoped this phase reported
     R10.8 and R10.9 as having no code, and the scoping was to A1's Go files:
     `web/src/pages/ActiveQueries.tsx` (`99f37f0`) and `web/src/pages/WaitEvents.tsx` (`be7f042`)
     both exist and are A6's, and `19eb117:requirements.md:421` and `:422` are their spec rows. The ids with
     genuinely no code are R10.25 and R10.26 at `19eb117:requirements.md:438` and `:439` — and those two
     rows write their ids bold, `| **R10.25** |`, where R10.1–R10.14 write them plain, so
     `grep '^| R10\.25 '` returns nothing on a file that has the row.
  2. `cmd/pgsi/main.go:579` builds `api.Config` with six fields and assigns neither
     `ActiveQueries` (`server.go:228`) nor `Terminate` (`server.go:244`), so both of the phase's
     routes answer from their empty defaults (`active_queries.go:117`) in a running binary even
     though the activity collector is registered at `main.go:1054`. Rowed as §5-D20 in the same
     commit — the same class as §5-D18's store finding: the collector exists, the DTO exists, the route
     exists, and the one line that would connect them was not written.
  3. `internal/api/sessions.go:3` and `:89` cite `19eb117:requirements.md:330` for R4.5; `19eb117:requirements.md:330`
     is R4.3's row and R4.5 is `19eb117:requirements.md:332`. A2's file, named here rather than edited there.
- **Ruling this entry carries.** The phase is A1's per §3's Owner column, and R14.1 makes a phase's
  code and its IMPLEMENTATION.md update one commit, which puts this entry's authorship with whoever
  wrote the code — six of the twelve ids here are A2's and A6's, and their files are named rather
  than re-described, which is R14.9's rule against a second owner restating a requirement another
  slot implemented. The entry does not close the phase: R10.25 and R10.26 have no code, and 6a
  cannot read ✅ until they do. What it closes is the state where §3's row said ⬜ while
  `internal/collector/activity.go`, `internal/api/active_queries.go`, `ActiveQueries.tsx` and
  `WaitEvents.tsx` all existed.

### Requirement → file → status (R14.8)

| Req | File | Status |
|---|---|---|
| R4.1 | `internal/collector/activity.go` (A1, `49b3210`), `internal/api/active_queries.go` (A2, `e6a4921`) | **in-progress (A1)** — collector, DTO and route all exist; the missing piece is `cmd/pgsi/main.go:579`, which builds `api.Config` without `ActiveQueries` (`server.go:228`), so a running binary answers from `active_queries.go:117`'s empty source — §5-D20 |
| R4.2 | `internal/collector/activity.go:46` (`ActivityInterval`, 15 s), returned by `Descriptor` at `activity.go:284` | **implemented (A1)** — the const is the descriptor's own and `activity_test.go:106` asserts it; the cadence matches the requirement's number exactly, and the UI half of the refresh belongs to R10.8 |
| R4.3 | `internal/collector/activity.go:624` (`WaitEventCount`), `:650` (`waitCatalogue`), `:673` (`ValidateWaitEvents`), `:690` (`WaitEventRollup`) | **in-progress (A1)** — the rollup and its catalogue validation are built and tested (`activity_test.go:655`, `:689`, `:744`), but `/api/v1/insights/wait-events` is still `api/x-planned-paths.yaml:146` rather than an `openapi.yaml` path, so there is no handler and A6's page reads the active-queries fetcher instead |
| R4.4 | `internal/collector/activity.go:733` (`BlockingChain`), `:752` (`BlockingChains`), `internal/collector/locks.go` (A1, `b51ea03`) | **implemented (A1)** — built on `pg_catalog.pg_blocking_pids` with the depth cap the requirement names (`activity.go:122`, `MaxChainDepth` 3) and the cycle-cut `activity_test.go:798` pins |
| R4.5 | `internal/api/sessions.go` (A2, `db265b6`), `internal/api/dto/sessions.go:302` (`TerminateResponse`, mine, `c5d1772`) | **in-progress (A2)** — mounted at `server.go:506` and gated behind the R12.8 vocabulary (`internal/api/actions.go`, `3377848`), but `Config.Terminate` (`server.go:244`) is unset at `main.go:579`, so the endpoint cannot succeed in a running binary — §5-D20 |
| R4.6 | `internal/collector/activity.go:219` (`BackendType`), `:178`–`:180` (the three duration fields) | **implemented (A1)** — idle-in-transaction age is a field separate from active duration and `backend_type` is carried so autovacuum and parallel workers are distinguishable; `activity_test.go:263` and `:392` pin both |
| R4.7 | `internal/collector/activity.go:224` (`QueryID`), `internal/api/dto/sessions.go:126`, `web/src/pages/ActiveQueries.tsx:137` | **in-progress (A1)** — the id is collected, serialized and rendered as a column, but the requirement's second half — link it to the R3 query detail — is not built, so the id is text with no destination |
| R4.8 | `internal/collector/activity_test.go:418-423` (`TestActivityDropsRowsWithoutPid`), `:896` (`TestActivitySamples`), `:957` (`TestCollectReturnsSamplesAndNoRows`) | **implemented (A1)** — `activity.go:305-306` names the entry point the requirement's [A] clause calls, and the three tests assert value equality rather than presence, which is what "a card that renders `NaN` passes a presence check and fails this one" is for |
| R10.8 | `web/src/pages/ActiveQueries.tsx` (A6, `99f37f0`, last touched `6e10be8`) | **implemented (A6)** — built in the commit whose subject names this id; see Finding one for why this row needed the review corrected before it could be written |
| R10.9 | `web/src/pages/WaitEvents.tsx` (A6, `be7f042`, last touched `8fe4f32`) | **implemented (A6)** — the page and its chart; its header records that `GET /api/v1/insights/wait-events` is its `endpoint:` and `phase: "6a"` but that no A1 DTO backs it, which is R4.3's in-progress half and not a reason to mark this row open |
| R10.25 | — | **not-started (A6)** — the only key handlers in `web/src` are `components/ColumnMenu.tsx` and `components/WaitEventsChart.tsx`, both local to their own widgets; none of `j`/`k`, `Enter`, `/`, `t`, `Esc` or `?` from `19eb117:requirements.md:438` exists at page level |
| R10.26 | — | **not-started (A6)** — nothing in `web/src` fetches firing or pending alerts; R9.11's rule set shipped in Phase 4 (`f9aae41`, §3.9) and remains unconsumed |

## 6b — Query Insights UI, percentiles, plan capture, tags, load graphs

### Task breakdown

§3.14 carries no task table of its own — §3.13, below, *is* Phase 6b's row set, and §3.14's R14.8
note says so explicitly. The WBS row in the [README](../README.md) is the task list.

### Validation entry (R14.2)

- **Scope.** The phase's fifty-two tracked files are `web/src/pages/**` (15) and
  `web/src/components/**` (37), listed in §3.13. The `Findings filed` column is copied from
  `docs/reviews/finding-index.md`; the `Round that read it` column is that file's `filed in`
  column, with the two rows whose `filed in` cell reads `this review` rendered as the commit that
  rowed them (`7954e3a`), because a relative phrase copied out of the file that defines it names
  nothing. That leaves the column able to name a round only where something was filed, so the five
  files this round read are named whether or not they filed: `web/src/pages/Tags.tsx`,
  `web/src/pages/Tags.test.tsx`, `web/src/components/StatePanel.tsx`,
  `web/src/components/ColumnMenu.tsx` and `web/src/pages/EmptyStateCatalogue.test.tsx`, all read at
  `0c8f53e` and all byte-identical there to `c5d1772`. Six of the fifty-two rows name a round; the
  other forty-six are listed with their commits and no read claimed, which is this entry's first
  named gap.
- **Also read this round, owned by the 6a/1b half.** Phase 6b's tracked set is pages and components;
  the Tags page mounts through seven files A4 owns, and the review read them because three of the
  findings are about them rather than about A6's page.

| File | Round that read it | Findings filed | Last commit touching it |
|---|---|---|---|
| `web/src/components/ClusterTable.tsx` | — | — | `6d78980` |
| `web/src/components/ColumnMenu.test.tsx` | — | — | `67a299d` |
| `web/src/components/ColumnMenu.tsx` | `7954e3a` | — | `0c8f53e` |
| `web/src/components/ExtensionNotInstalled.test.tsx` | — | — | `1927125` |
| `web/src/components/ExtensionNotInstalled.tsx` | — | — | `1927125` |
| `web/src/components/HealthBar.test.tsx` | — | — | `6d78980` |
| `web/src/components/HealthBar.tsx` | — | — | `f5a7635` |
| `web/src/components/LatencyHistogram.test.tsx` | — | — | `73d5971` |
| `web/src/components/LatencyHistogram.tsx` | — | — | `73d5971` |
| `web/src/components/LoadBars.test.tsx` | — | — | `68f052d` |
| `web/src/components/LoadBars.tsx` | — | — | `68f052d` |
| `web/src/components/Nav.test.tsx` | — | — | `1949801` |
| `web/src/components/Nav.tsx` | — | — | `2cbbf78` |
| `web/src/components/PlanList.test.tsx` | — | — | `73d5971` |
| `web/src/components/PlanList.tsx` | — | — | `73d5971` |
| `web/src/components/PlanViewer.test.tsx` | — | — | `73d5971` |
| `web/src/components/PlanViewer.tsx` | — | — | `73d5971` |
| `web/src/components/QueryLoadArea.test.tsx` | — | — | `34ae29d` |
| `web/src/components/QueryLoadArea.tsx` | — | — | `7f6f89f` |
| `web/src/components/QueryLoadChart.test.tsx` | — | — | `d2ffbfb` |
| `web/src/components/QueryLoadChart.tsx` | — | — | `d29e6dd` |
| `web/src/components/QueryTable.test.tsx` | — | — | `d2ffbfb` |
| `web/src/components/QueryTable.tsx` | — | — | `8e5d3e1` |
| `web/src/components/RecommendationList.test.tsx` | — | — | `73d5971` |
| `web/src/components/RecommendationList.tsx` | — | — | `73d5971` |
| `web/src/components/ResourceLoadPanel.test.tsx` | — | — | `a107147` |
| `web/src/components/ResourceLoadPanel.tsx` | — | — | `a107147` |
| `web/src/components/Sparkline.test.tsx` | — | — | `729f60c` |
| `web/src/components/Sparkline.tsx` | — | — | `962cb89` |
| `web/src/components/StatePanel.test.tsx` | — | — | `215895d` |
| `web/src/components/StatePanel.tsx` | `7954e3a` | S98 | `bee6b5f` |
| `web/src/components/StatusGlyph.test.tsx` | — | — | `6892d9d` |
| `web/src/components/StatusGlyph.tsx` | — | — | `14d4df0` |
| `web/src/components/ThemeToggle.test.tsx` | — | — | `14d4df0` |
| `web/src/components/ThemeToggle.tsx` | — | — | `14d4df0` |
| `web/src/components/VirtualList.test.tsx` | — | — | `0b6bcc9` |
| `web/src/components/VirtualList.tsx` | — | — | `0b6bcc9` |
| `web/src/pages/ActiveQueries.test.tsx` | — | — | `a216448` |
| `web/src/pages/ActiveQueries.tsx` | — | — | `0dd5f23` |
| `web/src/pages/ClusterDetail.test.tsx` | — | — | `b840e06` |
| `web/src/pages/ClusterDetail.tsx` | — | — | `3feae96` |
| `web/src/pages/EmptyStateCatalogue.test.tsx` | `7954e3a` | — | `0c8f53e` |
| `web/src/pages/Fleet.test.tsx` | — | — | `6d78980` |
| `web/src/pages/Fleet.tsx` | `68f052d` | S22 | `6d78980` |
| `web/src/pages/NotImplemented.test.tsx` | — | — | `82c09f1` |
| `web/src/pages/NotImplemented.tsx` | — | — | `af433cc` |
| `web/src/pages/Queries.test.tsx` | — | — | `7855768` |
| `web/src/pages/Queries.tsx` | — | — | `0544374` |
| `web/src/pages/QueryDetail.test.tsx` | — | — | `3feae96` |
| `web/src/pages/QueryDetail.tsx` | — | — | `3feae96` |
| `web/src/pages/Tags.test.tsx` | `7954e3a` | — | `bee6b5f` |
| `web/src/pages/Tags.tsx` | `7954e3a` | S96, S97 | `bee6b5f` |

| File | Round that read it | Findings filed | Last commit touching it |
|---|---|---|---|
| `web/src/api/client.ts` | — | — | `9790fda` |
| `web/src/api/envelope.ts` | `7954e3a` | S95 | `efdabb3` |
| `web/src/api/types.ts` | — | — | `1d4ea46` |
| `web/src/routes.ts` | — | — | `843986b` |
| `web/src/state/useActiveQueries.ts` | — | — | `df3e83b` |
| `web/src/state/useQueries.ts` | — | — | `0effc54` |
| `web/src/state/useQueryDetail.ts` | — | — | `0effc54` |

- **Findings.** Four rows belong to this entry: A3's S96 and A3's S97 against `web/src/pages/Tags.tsx`
  (a `load` dep array that includes the state `load` writes, so the hook refetches in a loop,
  measured at 21 calls in 30 ms with `fetchTags` mocked to return a fresh object; and R10.22's
  subject key dropped, so navigating `/c/ns/a/tags` to `/c/ns/b/tags` renders cluster a's rows under
  cluster b's header for the whole in-flight window), A3's S98 against `web/src/components/StatePanel.tsx`
  (four cites one line off plus a short count of the derivations that can reach `unavailable`), and
  A3's S95 against `web/src/api/envelope.ts`, filed earlier and closed at `843986b`. All three of
  the new rows closed at `bee6b5f` the same day they were rowed: S96 and S97 are one refactor there —
  the payload and its subject key now share a single `last` ref and `load`'s dep list is
  `[cfg, params]`, the `useActiveQueries.ts:105-160` shape — and the commit exports `useTagsFetch`
  and adds the fetch-count test S96 needed, because the render is byte-identical at one fetch and at
  the measured forty-six, so none of the 26 existing cases could have caught it. S98's fifth cite,
  `routes.ts:260` at `Tags.tsx:313`, resolves at HEAD and A6 left it; the two `routes.ts:257-267`
  range cites in `web/src/pages/Tags.test.tsx` moved to `:257-267` for the same `843986b` line
  shift. The table's `Last commit touching it` cells stay dated at `c5d1772` — three of the five
  files this round read have a newer commit, and rewriting the cells to `bee6b5f` would erase the
  rev the entry actually measured.
- **R14.8 rows.** Per R14.9 this entry adds no requirement table of its own: §3.13 (A6, `ee97e7f`)
  is Phase 6b's row set, and this entry's only change to §3's phase table is row 6b's own two cells:
  Status `⬜` → `🔄` and Validation entry `—` → §3.14. The ordering is the one R14.1 keeps filing
  people for: `0c8f53e` landed the Tags page, its suite and the StatePanel and ColumnMenu edits on
  2026-11-24, and the §3.13 row that documents them arrived two commits later in `ee97e7f`, also on
  2026-11-24. The review did precede this entry — `7954e3a` rowed the findings first, which is
  R14.2's order — but the code commit did not carry its IMPLEMENTATION.md update.
- **Ruling this entry carries.** A6 logged the R3.1 presence-ranking question in §3.13's R10.27
  cell instead of answering it, and the answer is now in `19eb117:requirements.md:308` as prose under R3.1:
  the ranking is `true` > `false` > not-known, the Go `Installed *bool` with
  `json:"installed,omitempty"` at `internal/api/dto/queries.go:298` is the contract, and
  `nullable: true` at `internal/api/openapi_annotations.go:3909` is the defect — it adds a second
  not-known encoding the Go type cannot emit, in the same package whose handler rejects
  `pgStatStatements: null` at `internal/api/queries.go:206` for exactly that reason. The ruling
  names no new requirement id, so it touches no R-number: R3.1's own sentence is what was ambiguous,
  and spec §11 rule 4 files it there.

### Requirement → file → status (R14.8)

Phase 6b's id set is sixteen, and this section — §3.13, A6's — is the table that carries them.
§3.14's R14.8 note states the R14.9 rule: the validation entry adds no table of its own.

| Req | Requirement (abridged) | File(s) | Status |
| --- | --- | --- | --- |
| R3.9 | Latency distribution: p50/p95/p99 plus min/mean/max/stddev and a sampled histogram over the named ms buckets, labelled as sampled and biased toward long queries | `web/src/components/LatencyHistogram.tsx` (73d5971) + `LatencyHistogram.test.tsx`; the `LatencyHistogram`/`HistogramBucket` wire types in `web/src/api/types.ts` (ad2cc04); the Go side is `internal/api/dto/queries.go` (A2/A5, Phase 3) | **implemented** — the UI half. `SAMPLED_LABEL` at `web/src/components/LatencyHistogram.tsx:85` is the mandated string, exported so the test asserts it instead of re-typing it; the four stats render as tiles and the buckets as a distribution with a screen-reader table twin. The bucket list itself comes from the payload, not from a second copy in TS, which is what keeps R3.9's percentile arm from becoming a UI decision. |
| R3.10 | Plan samples: bounded `EXPLAIN (FORMAT JSON)` capture for the top-N queries (5 plans/minute/cluster default) | `web/src/components/PlanList.tsx` + `web/src/components/PlanViewer.tsx` (73d5971) and their tests; `web/src/pages/QueryDetail.tsx` (3feae96); `internal/api/query_plans.go` (A2, 41282e1) | **in-progress** — the two components and the route shipped; the capture did not. §3.7's R8.2 row records that the handler answers 404 for every fingerprint until the scheduler lands and assigns that work to this phase, so the UI renders the fixture-backed list (`web/src/api/fixtures.ts`, 2223be9) today. The row lives here rather than in §3.7 because §3.7 says so in as many words. |
| R3.11 | Detect plan changes: keep plan hashes per fingerprint and surface a change event when the hash changes | `web/src/components/PlanList.tsx:58` (`resolveChange`) + the change log; `internal/store/detail.go:126` (A5, `detailPlanChangesSQL`); `internal/api/dto/queries.go`'s `QueryDetailView.PlanChanges` | **implemented** — both halves exist. The UI's `resolveChange` maps a `PlanChangeView` to the sample it names and handles the three reachable states (the change's hash matches a sample, the sample carries no hash, the change predates the retained window and resolves to nothing → `index: -1`), which is the part a list that only prints the log would skip. |
| R3.12 | Tags: (a) application_name/usename/datname and (b) the `/* pgsi:action=… */` comment grammar promoted to tag dimensions with a tag_hash | `web/src/pages/Tags.tsx` + `web/src/pages/Tags.test.tsx` (0c8f53e); `web/src/components/ColumnMenu.tsx:136-145` (0c8f53e, the `tags` default set); `web/src/api/types.ts`'s `TagView` (ad2cc04); `testdata/api/tags.json` (f7b197e); `web/src/routes.ts:264` (843986b, A4's mount); `internal/api/tags.go` + `internal/api/tags_test.go` (A2, 62740ed); `internal/query/normalizer.go` (A5) is the comment parser | **implemented** — both halves. (a) is the three Postgres-supplied dimensions rendered as columns; (b) is the comment grammar, parsed in Go and surfaced as the `action`/`route`/`controller`/`framework`/`dbDriver` columns plus the `tagHash` the Queries tag chip filters by. The default set shows eight of the eighteen columns and the menu reaches all eighteen, so the abridgement is visible rather than lossy. |
| R9.13 | Sampled-histogram series accounting: 50 fingerprints × (8 buckets + `_sum` + `_count`) = 450 series per cluster, accounted in §5's budget | `web/src/components/LatencyHistogram.tsx` (73d5971) renders the two numbers the accounting is made of; `web/src/api/types.ts:575`'s sampled-vs-derived flag; the series family itself is R9.5's (`internal/store/perquery.go`, A5 3418da3) | **in-progress** — rides R9.5's gap, which §3.7 records as cmd/pgsi never building a non-nil per-query source. The budget is accounted in the docs and the UI renders the numbers whenever a payload carries them, but nothing emits them yet, so the row does not claim the accounting is done on the strength of a screen that can display a number the collector does not produce. |
| R10.5 | Cluster Overview: (a) Query load stacked area (total execution + IO) and (b) Resource load (CPU capacity vs usage, memory working set), with (b) rendered only when metrics.k8s.io answers | `web/src/pages/ClusterDetail.tsx` (3feae96) + its test; `web/src/components/QueryLoadArea.tsx` (7f6f89f) + test; `web/src/components/ResourceLoadPanel.tsx` (a107147) + test | **implemented** — (a) and (b) are separate components so the "only when metrics.k8s.io answers" gate lives in the panel rather than in the page: the page renders its header and the load area unconditionally and lets the resource panel render its own no-metrics state. |
| R10.6 | Queries: the R3.2 table with sparklines per row (R8.11's batched endpoint, not fifty requests), inline load bars, filter chips (database, user, tag), search over normalized text, saved views scoped to shared/cluster | `web/src/pages/Queries.tsx` (0544374) + test; `web/src/components/QueryTable.tsx` (8e5d3e1) + test; `web/src/components/Sparkline.tsx` (962cb89); `web/src/components/LoadBars.tsx` (68f052d); `web/src/components/ColumnMenu.tsx:69-84` (the `queries` default set) | **implemented** — including §9's 6b acceptance sentence ("Queries page renders the R3.2 column set exactly (incl. `Load by lock wait` = `n/a`)"): the thirteen R3.2 columns plus R10.6's sparkline are the documented default, and the two columns R3.2 itself marks as having no per-query value render the `n/a (no per-query source, §5)` literal and the deferred **Gap** marker rather than being dropped — a default that hid them would render a nine-column table and pass the eye while failing the sentence. The sparkline comes from the batched endpoint, not from per-row requests. |
| R10.7 | Query Detail: header (fingerprint, database, user, first-seen), per-minute load chart, latency panel (P50/P95/P99 or the R3.9 fallback), plan samples with a JSON/plan viewer, recommendations | `web/src/pages/QueryDetail.tsx` (3feae96) + test; `web/src/components/QueryLoadChart.tsx` (d29e6dd); `web/src/components/PlanList.tsx` + `web/src/components/PlanViewer.tsx` (73d5971); `web/src/components/LatencyHistogram.tsx` (73d5971); `web/src/components/RecommendationList.tsx` (73d5971) | **implemented** — all five named parts render from `QueryDetailView`. The latency panel renders the R3.9 fallback shape (the sampled histogram plus the four stats) when the percentiles are absent instead of an empty panel, which is the R3.9/R10.7 seam and the reason the two requirements share a component. |
| R10.18 | Table virtualization + density toggle (compact/comfortable) on every table that can exceed 100 rows | `web/src/components/VirtualList.tsx:34` (`Density`, `ROW_HEIGHT`) (0b6bcc9) + test; used by `web/src/components/QueryTable.tsx` (8e5d3e1), `web/src/components/ClusterTable.tsx` (6d78980), `web/src/pages/ActiveQueries.tsx:303` and `web/src/pages/Tags.tsx:299` (this commit) | **implemented** — the four tables the app renders now take the primitive. The two page tables were the gap this cell named last ("two do not, and they lose both halves at once because the primitive owns density", the coupling `web/src/components/VirtualList.tsx:16-18` states as the design): each now holds `density` as page state, renders `DensityToggle` in its card head, marks its `<table>` with `data-density`, and builds its `<tbody>` from `useVirtualWindow` with the two `virtual-spacer` rows. The page tests pin the window's arithmetic rather than the toggle's look — a 500-row payload at compact mounts 39 rows and a 11064px bottom spacer, and scrolling to 240px moves the first mounted row to index 4 with a 96px top spacer — so a table that keeps the toggle but drops the spacers (the failure mode, where the scrollbar measures the mounted rows) fails here. |
| R10.19 | Per-chart interaction contract stated per chart type: hover tooltip contents, legend behaviour, click-through target, crosshair on multi-series charts | Six chart files carry the contract header: `web/src/components/QueryLoadArea.tsx:125`, `web/src/components/Sparkline.tsx:55`, `web/src/components/LoadBars.tsx:38`, `web/src/components/LatencyHistogram.tsx:68`, `web/src/components/QueryLoadChart.tsx:69`, and `web/src/components/ResourceLoadPanel.tsx:66` (this commit) | **implemented** — the sixth surface this cell named last ("`web/src/components/ResourceLoadPanel.tsx` (a107147) has zero hits for tooltip, hover, legend or crosshair in its 171 lines … The fix is a header block plus a tooltip in that file, not a new requirement") has both. The header states the shipped half and the half Phase 7's graph inherits, because the panel is not a chart yet and naming only the future one would leave the shipped surface unspecified; the tooltip is the exported `resourceLoadTip`, composed from `COPY` and applied as `title` on the frame and on both source rows, so the test asserts the function the render calls instead of a second copy of the sentence — the duplication class R16.8 mints rows from. |
| R10.20 | Column-visibility menu on every wide table, with a documented default column set and a persisted per-view selection | `web/src/components/ColumnMenu.tsx` (0c8f53e) + test; `DEFAULTS` at `web/src/components/ColumnMenu.tsx:56-146` names five views (fleet, queries, active-queries, wait-events, tags) | **implemented** — every table with a column set has a menu: ClusterTable, QueryTable, ActiveQueries.tsx and Tags.tsx all import it. The persisted half is `storageKey(viewId)` + `normalizeSelection`, and each page's suite cross-checks `DEFAULTS[viewId]` against its own column array, so a default key with no column behind it is a test failure rather than a silently missing column. |
| R10.21 | Timestamp rendering and timezone: the API emits RFC 3339 UTC; the UI renders in the browser's local zone with the zone labelled and the absolute timestamp on hover | `web/src/lib/format.ts:14` (`localZone`), `web/src/lib/format.ts:34` (`formatTimestamp`), `web/src/lib/format.ts:67` (`utcTitle`) (14d4df0) + test | **implemented** — one formatter rather than per-page `toLocaleString` calls, which is the whole reason the rule is a function and not a convention: a convention drifts per page, and the drift is invisible until two pages render the same instant differently. The zone name is in the rendered text, not only in the `title`. |
| R10.22 | Loading skeletons, a global "data as of" stamp, and a partial-data banner (a page that renders 3 of 4 clusters while the fourth loads says so) | `web/src/components/StatePanel.tsx` (0c8f53e) + test; `web/src/api/envelope.ts` (843986b) — `copyFor`, `partialBanner`, the per-view noun tables | **implemented** — the banner composes from R8.4's `meta.failed[]`/`meta.loading[]` per §5's §5-D18 ruling, and the copy tables are `Record<Exclude<PanelState, "ready">, PanelCopy>`, so a missing state's copy is a type error rather than a missing branch. The skeleton and the "data as of" stamp are the `loading` and `ready` arms of the same component. |
| R10.23 | Colour-blind-safe palette plus non-colour encoding: every series distinguishable in greyscale, every state conveyed by shape/icon/label as well as colour (R4.1's icons, R5.1's card states) | `web/src/styles/tokens.css` (§5-D12's validated palette; `scripts/validate_palette.js` vendored at db41cbb); `web/src/components/StatusGlyph.tsx` (14d4df0) + test; `web/src/components/HealthBar.tsx` (f5a7635); every chart's table-view twin | **in-progress** — the palette half and the health-cell half are done and §4's §5-D12 records the validation. The two clauses this cell carried as open are both closed now. The generalisation §5's 1b entry names `StatusGlyph.tsx` as the interim relief for shipped in `b52f83a`: the drawing is split into a `Glyph` primitive taking a `GlyphPresentation`, a `ring` shape is added, and `PANEL_GLYPHS` gives R10.22's six `PanelState`s each a shape, token and label, with `StatusGlyph`/`StatusChip` kept as the health-typed front doors. And `scripts/validate_palette.js` is a gate, not just a command: `make palette-check` (`Makefile:98`, A2's `25f84e4`) runs it over both themes' palettes extracted from `web/src/styles/tokens.css`, and `.github/workflows/ci.yml:180` invokes it in the web job — so the cell's old "0 hits in both, so it exists as a command, not as a gate" is false as of that commit, and the two places that quote that sentence (`Makefile:81-82`, `.github/workflows/ci.yml:173-175`) are A2's to update. What is left is one measured gap and one by construction. The per-chart greyscale claim is prose at the chart level: the validator answers the greyscale question for the token palette (the CVD simulation and the chroma floor, `scripts/validate_palette.js:9`), so a chart drawing one series from `--series-1` inherits a measured answer while a chart drawing several has its own set asserted in its dataviz header and never checked as a set — measured, the word "greyscale" appears in one component (`web/src/components/WaitEventsChart.tsx:51`, `web/src/components/WaitEventsChart.tsx:353`) and a colour section in three of the six chart files R10.19's row names (`web/src/components/QueryLoadArea.tsx`, `web/src/components/Sparkline.tsx`, `web/src/components/LoadBars.tsx`; `web/src/components/LatencyHistogram.tsx`, `web/src/components/QueryLoadChart.tsx`, `web/src/components/ResourceLoadPanel.tsx` have none). R5.1's card states are Phase 7's, so the "every state conveyed by shape as well as colour" set stays incomplete by construction until Phase 7 lands.|
| R10.27 | `pg_stat_statements` not-installed is a first-class screen, designed and tested — the product's most-seen screen in v1 given K2 | `web/src/components/ExtensionNotInstalled.tsx` (1927125) + test; `web/src/components/StatePanel.tsx:111`; `web/src/pages/Queries.tsx:281` (the `extensionInstalled?` prop) and `web/src/pages/Queries.tsx:347` (the read site); `web/src/pages/Tags.tsx`; `web/src/api/types.ts:534-537`; `testdata/api/queries.json` (f7b197e) | **implemented** — A3's rule for this row, flipped on A6's `44fcc32`. The screen exists and is tested, the presence type exists, and the value is now reachable from the fetcher: `web/src/api/types.ts:541-545` reads Reachable from a fetcher as of this commit (A4's `79d4c4f`), and `testdata/api/queries.json:142` carries the key (A1's `ae9cc86`, `TestQueriesFixtureIsCanonical` at `internal/api/dto/queries_test.go:89`), so `web/src/pages/Queries.tsx:347` resolves the presence through `resolveExtensionInstalled` at `web/src/pages/Queries.tsx:215-228`, with the prop override at `web/src/pages/Queries.tsx:281` still taking precedence. Both commits the cell named as gating the flip are in. The third item, the presence-ranking question this cell asked A3, is ruled in `requirements.md`'s R3.1 row (A3's `a93f8d1`, 2026-11-24): the ranking is `true` > `false` > not-known and R10.27's screen renders only on an explicit `false`, which is what `web/src/components/StatePanel.tsx:111` already does — so the page's `unavailable` arm does not change when the fetcher lands. The defect the ruling names was A2's `nullable: true`, closed in `c4e714e` — the line sat at `internal/api/openapi_annotations.go:3909` before that commit deleted it and the cite now resolves to the `type: boolean` line at `internal/api/openapi_annotations.go:3908` that replaced it; the class survives in two siblings of that file (`NamespaceFailure.lastSuccessTime`, `InstanceView.startedAt`), filed as A3's S100, and `web/src/api/types.ts:534-537` is already correct in not widening `installed?: boolean` to `boolean \| null`. |
| R10.28 | Empty-state catalogue: an enumerated, designed state for every combination the reference environment produces, each with a named fixture in R16.7 | `web/src/pages/EmptyStateCatalogue.test.tsx` (0c8f53e); `web/src/components/StatePanel.tsx` (0c8f53e) | **implemented** — eight rows: the seven R10.28 enumerates (each marked `from: "spec"` and naming its R16.7 fixture) plus the Tags page's own empty state (`from: "page"`), which R10.28 does not enumerate because it is a page-level state rather than a cluster-level one. The test renders each entry through its owning component instead of asserting a list of strings, so a row naming a component that no longer renders that state fails. |
