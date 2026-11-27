# Phase 3 — `pg_stat_statements` collector, normalizer, query store, top-N API, rollups

## Status

⚠️ Done with deviations (A5). The store, the normalizer and every read path shipped; the
`pg_stat_statements` collector and the R9.5/R9.5b metrics family did not —
[deviations.md](../deviations.md) §5-D18.

## Owners

A5. `internal/store/**` and `internal/query/**`.

## Task breakdown

§3.11 carries no task table; Phase 3's WBS row in the [README](../README.md) is the task list.

## Validation entry (R14.2)

Full text at `1b11199:IMPLEMENTATION.md` §3.11; the short version:

- **Scope** — every `.go` file in `internal/store` was read in full across four review rounds, and
  each round's output is the ledger rows it filed; the file table below is checkable against
  `docs/reviews/finding-index.md` because every row names a file and a line range.
  `internal/query/normalizer.go` and its test were reviewed in the Phase 1a round (`65ed2ee`) and are
  not re-opened here; R3.5's fixture-corpus clause belongs to R16.1 and is tracked in
  [phase-1.md](phase-1.md).
- **Commands run, in one call, at `ab13ca4`** — `python3 scripts/check-parity-matrix.py
  requirements.md` → `rc=0` (`mismatches: 0`, `citations: 207 distinct ids across 186 files · 0 not
  defined by §4`, `status cells: 77 requirement rows · 0 non-canonical`, `phase cells: 12 §3 phase
  rows · 0 malformed · 5 §1 phase rows · 0 disagreeing with §3`, `markdown links: 73 checked · 0
  broken`). A5's own run of `gofmt -l` (empty), `go build ./...`, `go vet ./internal/store/` and
  `go test -count=1 ./internal/store/` is quoted in their report for the same commit and is the
  source for the Go-side result; this entry does not re-run the Go toolchain over a tree A5 owns.
- **Gap 1 — no `pg_stat_statements` collector.** Phase 3's scope line names it first, and it is the
  one Phase 3 artefact with no file. The store's write path (`internal/store/sink.go`'s `Publish`) is
  complete and tested against fixtures, so the store is correct and unexercised: nothing in a running
  binary calls it with real rows.
- **Gap 2 — R9.5 / R9.5b's per-query metrics family is not emitted.** The catalogue documents it
  (`internal/api/promql_catalogue.go` carries the R9.5b fingerprint label) but the exposition at
  `internal/api/server.go:747` writes `metricsBody()`: a metric in docs with no code, which is
  R14.7's inverse. Closed since for the body and the composer — the body is
  `internal/store/perquery.go` (`3418da3`) and the composer is `internal/metrics/metrics.go`
  (`c8f2024`) — and the inverse still holds for the running binary until `server.go:416` passes the
  family to `metrics.New`, which is the half A1 has in flight.
- **Why ⚠️ and not 🔄** — the store, the normalizer and every read path R3.2–R3.8 name are done and
  reviewed, and the two gaps are the phase's scope line's first and fifth clauses; neither is fixable
  inside Phase 3 without reopening Phase 2's collector set, which is why they are deviations and not
  work-in-progress.

| File | Round that read it | Findings filed | Last commit touching it |
|---|---|---|---|
| `internal/store/schema.go` | the `8a86b8d` round | A3's S26, A3's S33-class | `adea4fd` |
| `internal/store/store.go` | the `8a86b8d` round | A3's S35 | `8a86b8d` |
| `internal/store/statements.go` | the `8a86b8d` round, re-read at the A3's S43 fix | A3's S43 | `6623812` |
| `internal/store/detail.go` | the `8a86b8d` round | A3's S35's read-path half | `adea4fd` |
| `internal/store/statements_test.go` | the `cd861fb` round | — | `adea4fd` |
| `internal/store/topn_test.go` | the `cd861fb` round, re-read at the A3's S49 fix | — | `ab13ca4` |
| `internal/store/detail_test.go` | the `764ce20` round | A3's S42, A3's S43, A3's S44 | `6623812` |
| `internal/store/series.go` | this round | — (clean) | `6e713ef` |
| `internal/store/series_test.go` | this round | — (clean) | `6405120` |
| `internal/store/rollups.go` | this round | A3's S45 | `ab13ca4` |
| `internal/store/rollups_test.go` | this round (the tests A3's S45 asked for, filed as A3's S29) | — | `e0fc76c` |
| `internal/store/retention.go` | this round | A3's S46 | `ab13ca4` |
| `internal/store/sink.go` | this round | A3's S47, A3's S48 | `ab13ca4` |
| `internal/store/topn.go` | this round | A3's S49 | `ab13ca4` |
| `internal/store/store_test.go` | this round | — (clean) | `ed6f96d` |

## Requirement → file → status (R14.8)

R8.2, R8.4 and R8.5 are Phase 3 ids that Phase 5 owns the implementation of; they are
cross-referenced to [phase-5.md](phase-5.md) rather than re-implemented here, because a second row
for one requirement is how a requirement gets two contradictory statuses (R14.9).

| Req | File | Status |
|---|---|---|
| R3.1 | `internal/collector/settings.go` (`extensionSamples`, `:288-298`), `cmd/pgsi/config.go`, `internal/api/queries.go`, `internal/api/dto/queries.go` (A1, `a83612b` — `ExtensionPresenceView` and `QueriesResponse`; the route still serves `Envelope[QueryView]` until A2 swaps the alias, so the type has no fetcher yet) | **in-progress** — the `pg_extension` metric surface R3.1's S2 detection feeds exists. The dependency this cell named — §5-D18's missing collector — closed at `76b0f56` with `internal/collector/statements.go`, so the presence report is now testable against real rows; what remains is the wiring, since no `collector.Registry` is constructed in `cmd/pgsi/main.go` and no collector of any kind runs in the binary; and the per-database fan-out R3.1's second ruling (A3) requires a database target on `SQLConfig` in `internal/collector/sql.go` — three fields today, StatementTimeout, ApplicationName and Now, none of them a database — which is where A1's S119 fix starts |
| R3.2 | `internal/store/topn.go`, `internal/store/topn_test.go`, `internal/api/queries.go` (`51c5bbd`) | **implemented** — the store side and the route; the column set is the R3.2 per-query set, sourced from `query_rollup` |
| R3.3 | `internal/store/statements.go`, `internal/store/statements_test.go` | **implemented** — snapshot deltas and the counter-reset path; `internal/store/statements_test.go:546-549` pins the `plan_change` hash chain |
| R3.4 | `internal/query/normalizer.go`, `internal/store/schema.go` (the `query` table, keyed on `fingerprint` alone) | **implemented** |
| R3.5 | `internal/query/normalizer.go`, `internal/query/normalizer_test.go` | **implemented** — literals → `$1`; R16.1's fixture-corpus clause is Phase 7's and is rowed in §3.8 |
| R3.6 | `internal/store/store.go:520` (`firstSeenText`), `internal/store/schema.go:78` | **implemented** — the cap is enforced on write and the capped column is separate from the raw one |
| R3.7 | `internal/store/rollups.go`, `internal/store/rollups_test.go` | **implemented** — four kinds; S45's kind-scoped `digest()` landed at `ab13ca4` and `validate()` now rejects fields the kind does not own |
| R3.8 | `internal/store/detail.go`, `internal/store/detail_test.go` (`6623812`) | **implemented** |
| R3.13 | `internal/store/store.go:145-149` (`RecordClientAddress`), `internal/store/store.go:151-208` (`ApplicationTagSource`, `ValidApplicationTagSource`), `internal/store/store.go:220` (`filterRows`), `internal/store/schema.go:371` | **implemented** — both halves now gate writes. `Config.filterRows` (store.go:220) drops `kind='client_address'` rows unless `RecordClientAddress`, and the `application` row unless `ApplicationTagSource` is `off`; `ValidApplicationTagSource` (:202) enumerates the three `--application-tag-source` values R3.12's two tag sources make necessary, and `Open` (:289) rejects anything outside them rather than letting `withDefaults` rewrite an unspellable value to the default. The `--record-client-address` half previously stored the field without any code reading it, so the row's "gates `kind='client_address'` rows" described an intent; `filterRows` is the gate. Tests: `TestR313WriteGate` (rollups_test.go:672), `TestApplicationTagSourceGate` (store_test.go:316) |
| R3.14 | `internal/store/retention.go`, `internal/api/dto/queries.go:274` | **implemented** — S46's windowed orphan-query pass landed at `ab13ca4`; the week-over-week answer is the dto's plan-change correlation |
| R8.2 | see §3.7 | **in-progress** — cross-referenced, not re-implemented |
| R8.4 | see §3.7 | **validated** — cross-referenced, not re-implemented |
| R8.5 | see §3.7 | **validated** — cross-referenced, not re-implemented |
| R9.5 | `internal/api/promql_catalogue.go`, `internal/store/perquery.go` (A5, `3418da3`), `internal/metrics/metrics.go` (A2, `c8f2024`) | **implemented** — the blocker chain this cell carried is closed end to end, so the word for the requirement is no longer `blocked`. The collector shipped at `76b0f56` with `internal/collector/statements.go:694` (`samplesFor`) minting the samples; the driver decision this cell named as the remaining gate was ruled into R13.10 (`c8e54e2`) and implemented at `9e6572a`, which registers the seven SQL collectors beside the pooler — the "registers the pooler only" clause was stale from the moment `9e6572a` landed (A3's S65 fixed the code; A3's S72 rows the cell); `internal/store/perquery.go` (`3418da3`) renders the four S25 members as four HELP/TYPE families from `query_source`, the §5-D18 source rule, with `query_rollup` excluded because a sum over a windowed rollup falls as its oldest buckets evict and a `_total` counter that decreases is a reset to every reader; and `internal/metrics/metrics.go` (`c8f2024`) composes it into the hub through the `PerQueryFamily` interface at `internal/metrics/metrics.go:139`. The caller gap the cell carried is closed (the coordinator's ruling, drafted by A5 and qualified here so every number is a resolvable cite rather than a bare one): `cmd/pgsi/main.go:757` assigns `cfg.PerQueryMetrics = bundle.perQuery` (`c5c3665:cmd/pgsi/main.go:570`, the commit that added it), and the two addresses the earlier wording named moved with it -- the `api.Config` literal is `cmd/pgsi/main.go:579` (was `cmd/pgsi/main.go:362`) and the hub call is `internal/api/server.go:772` (was `internal/api/server.go:507-510`). The `--discover=false` branch, which leaves `bundle.perQuery` nil, is a designed configuration mode and not a remaining gap: `internal/api/server.go:333` documents the typed-nil pass-through (`cfg.PerQueryMetrics = (*store.Store)(nil)`) as the shape when there is no store to render from, and `internal/store/perquery.go`'s nil-receiver guard returns `(, nil)` -- a nil source renders the family absent, which is the hub's rule for every optional source. A5's report called three of the four hub values nil at runtime; A2 measured two of them not to be — `cfg.Metrics` is assigned at `cmd/pgsi/main.go:736` and `cfg.HTTPMetrics` is defaulted inside `New` at `internal/api/server.go:462-463`, so the HTTP family renders — and the cell names one nil. The store family's absence is a separate gap, owned by 1b11199:IMPLEMENTATION.md:710. **Ruled on A1's question, revised twice:** the cell had kept `blocked` because `STATUS_ENUM` (`8cc7a8b:scripts/check-parity-matrix.py:842`) has no "partially unblocked" and a registry closes a blocker without emitting a family; it moved to `in-progress` once the family was emitted, and the caller-gap ruling above moves it to `implemented` -- the enum's own vocabulary both times, nothing invented. R9.5b's "same cause" resolves here **A3's S167 (fixed here 2026-11-26):** nine of this cell's eleven anchored cites resolved at `d221e4f` to lines saying something else; seven are retargeted above, and the two `(was …)` cites it carried (`cmd/pgsi/main.go:362`, `internal/api/server.go:507-510`) are quoted rather than moved because they match no rev of their files in `git log` (25 main.go revs and 23 server.go revs scanned, zero matches) -- they were never measured. The bare `:127` is now pathed: `8cc7a8b:scripts/check-parity-matrix.py:481` makes a bare `:N` inherit the line's first anchored path, so `:127` was always checked as `internal/collector/statements.go:127` and the `internal/metrics/metrics.go:139` line it meant was never checked. |
| R9.5b | `internal/api/promql_catalogue.go`, `internal/api/dto/queries.go`, `internal/api/dto/series.go` | **implemented** — A3's S167: the cause this cell named was `internal/api/server.go:416`, which at `d221e4f` is a docblock line about `cfg.Metrics`; the argument the family needs is `internal/api/server.go:340` (`PerQueryMetrics metrics.PerQueryFamily`, added by `61461fa`) and it reaches the hub at `internal/api/server.go:773` (`metrics.WithPerQuery(s.cfg.PerQueryMetrics)`), so the shared cause R9.5's cell closed end to end now has a matching word on this side. The family exists (`3418da3`, `c8f2024`) and carries the fingerprint label, so the sentence "there is no family to carry it" is stale, and the label's own correctness in the catalogue and the dtos is unchanged |
