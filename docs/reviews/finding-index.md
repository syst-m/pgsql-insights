# Finding index — every `A3's S<NN>` id, what it said, and what became of it

The review slot's findings have been numbered five different ways across four rounds, and
only one of them (`S<NN>`) is cited from code. This table is the registry for that one: the
parity checker parses the id column below and scans every tracked `.md`, `.yml`, `.yaml`
and `.go` file plus the Makefile for `A3's S<NN>`; a cite naming an id with no row here
fails the build (`scripts/check-parity-matrix.py`, the finding-id section). Before this
table, a Go comment saying *"the `captured_at < ?` is A3's S26"* pointed at a paragraph in
a commit-message body — a different repository object, unresolvable without a `git log`,
and unresolvable at all for the ids that were never written down anywhere but a subject line.

| id | filed in | against | finding | fixed by | status |
|---|---|---|---|---|---|
| S1 | `f08b6ac`, IMPLEMENTATION.md §5 D18 | requirements.md R8.4 | R8.4 as amended said `meta.total` "counts what the response can serve, not what was attempted", while `dto/clusters.go` documents it as the pre-cut count and defines `truncated ⟺ len(data) < total`: with 120 matching and `limit=50` the two documents assign 50 and 120 to one field, and `web/src/api/envelope.ts`'s `partialBanner()` announces 120 while rendering 50. R8.10's pre-cut count wins — `truncated` is defined against it and the Go side already ships it. The equation `attempted = total + len(failed) + len(loading)` is deleted outright: it adds *rows to namespaces*, and a namespace yields N clusters, so no arithmetic over `total` recovers an attempted-cluster count. The banner's count is `data.length`. Second amendment (same day, my own wording): `truncated ⟺ len(data) < total` is wrong on every page but the first — on page 2 of a 4-row `limit=2` result `len(data)` is 2 and `total` is 4 with `truncated` false. | `f08b6ac` | ruled |
| S2 | `f08b6ac`, IMPLEMENTATION.md §5 D18 | requirements.md R8.9 | R8.9 exempted `/metrics` and `/healthz` from auth but not `/readyz`, so under `--auth=token` the kubelet's readiness `httpGet` gets a 401 — the chart mounts the token as a *file*, not as `httpGet.httpHeaders` — and the pod never goes Ready. Both probes exempt unconditionally rather than via `--auth-metrics`, because a gated probe is a broken pod, not a preference; `/metrics` stays keyed to `--auth-metrics` and `/debug/pprof/*` follows `--auth`. | `f08b6ac` | ruled |
| S3 | `f08b6ac`, IMPLEMENTATION.md §5 D18 | `chart/values-production.yaml`, `cmd/pgsi/main.go` | The chart's `auth.enabled`/`tokenSecret` are **inert**: `main.go`'s flagset had only `-listen` and `-shutdown-timeout`, so the chart had been rendering an auth config the binary ignores — R8.8's real gap. Forwarded to the Phase 5 owner. Re-review the same day: A1's tree gained `--auth`/`--auth-token-file`/`--auth-metrics`/`--cors-*`, so the problem moved rather than closed; the residue is `main.go:81` and `auth.go`'s single `GroupProbe` still governing "/metrics, /healthz and /readyz" as one group, which contradicts S2's ruling that the two probes are exempt unconditionally. | `f08b6ac` (spec); A2's Phase 5 (code) | open — forwarded |
| S21 | `68f052d` | `web/src/components/QueryTable.tsx`, `LoadBars.tsx` | `LoadCellProps` had a fourth member `{kind:"none"}` whose render was a bare `—`; that glyph is R10.4's marker for *source has not run yet* and belongs to `source-pending` alone, so a fourth state invented a meaning R10.4 reserves. `loadCellFor` returns `LoadCellProps \| null`, the union shrinks to the three real states, and the `none` assertion moves from `LoadBars.test.tsx` to `QueryTable.test.tsx`'s `toBeNull()`. | `68f052d` | fixed |
| S22 | `68f052d` | `web/src/pages/Fleet.tsx` | `Fleet.tsx` rendered `<ClusterTable>` with no menu, so `DEFAULTS.fleet` was documented, tested and unreachable — the menu existed and the page that owns it never mounted it. Mounted as `ColumnMenu.test.tsx` already exercises it; `ClusterTable` gained `columns?: readonly string[]` and an exported `renderCell` so the body maps over the visible columns instead of ten hardcoded `<td>`s, without which the menu would have driven the count badge and nothing else. `Fleet.test.tsx` gains the missing assertion (the page renders a menu reading 10/10). | `68f052d` | fixed |
| S23 | `68f052d` | `web/src/components/ClusterTable.tsx`, `QueryTable.tsx` | `ColumnDef.required` existed in `ColumnMenu` and in nothing else, so its three enforcement points (`normalizeSelection`, the disabled checkbox, `set()`) had nothing to enforce. Both real column types now carry it: `query` on `QueryTable.QueryColumn` — the row's only link lives in that cell, so a selection without it renders a table of cells with zero links and R10.7's detail page becomes unreachable while the table looks fine — and `health` + `name` on `ClusterTable.Column`. | `68f052d` | fixed |
| S25 | `00b3f53` | requirements.md R9.5 | R9.5's cell left the per-query family's metric list open and the code answered it by copying R9.4's slice. R9.4's `lock_time` was admitted on a granularity-specific argument (sources report lock wait at database/user level, which is R9.4's granularity, and it renders n/a per query), so it does not belong in R9.5. The family is `execution_time`, `rows`, `shared_blocks_hit`, `shared_blocks_read`; R4-sourced per-query lock time was rejected as a scope change rather than a ruling, since R3.9's sampled source is scoped to the R9.13 histogram. | `00b3f53` | ruled |
| S26 | `8a86b8d` | `internal/store/statements.go` | **Blocking.** `previousPlanHashSQL` had no `captured_at < ?`. Detection runs after the insert inside the same tx, so the read-back returned the row the insert had just written, `prev == p.planHash`, and every plan change was missed forever — not just the first, which would have been correct. The comment described the query with the bound; the SQL did not have it. | `8a86b8d` | fixed |
| S27 | `8a86b8d` | `internal/store/schema.go` (dimension_rollup) | `dimension_rollup` was keyed without the namespace/cluster columns the other rollup tables carry, so two clusters in one namespace collapsed into one row's dimensions; PK widened. Caught mid-write, not in the review pass. | `8a86b8d` | fixed |
| S28 | `8a86b8d` | `internal/store/statements.go` | `normalizeAndDedupe` kept the higher-total row and dropped the other's counters: two queryids normalizing to one template stored 100 ms where 150 happened, and since `query_source` *is* R3.3's baseline the error is permanent. A later round whose slice order flipped the survivor then looked like a counter reset — the thing R3.3 exists to prevent. `mergeRows` now sums the counters and takes the non-additive fields (QueryID, SQL, Usename, Min, Max, Stddev, Plan) from the highest-load row. | `8a86b8d` | fixed |
| S29 | `8a86b8d` | `internal/store` test coverage | No test called `PutSnapshot`, which is the write path every other store test depends on and §3.3's last open gating item. | `cd861fb` (`topn_test.go`), `internal/store/statements_test.go` | fixed |
| S30 | `8a86b8d` | `internal/store/{detail,schema,statements}.go` | `plan_sample` and `plan_change` lacked the `REFERENCES query(fingerprint) ON DELETE CASCADE` that `query_source` already had, so deleting a `query` row orphaned its plans and plan changes instead of cascading. The constraint was written once and not copied, which makes it an omission rather than a design choice. | `8a86b8d` | fixed |
| S31 | `8a86b8d` | `internal/store/schema.go` | No index on `query(last_seen)` — the column `Purge`'s age query reads, on the table §4's byte math sizes as the largest in the store. `idx_query_last_seen` added. | `8a86b8d` | fixed |
| S32 | `8a86b8d` | `internal/store/store.go` | The package doc claimed "the read side of every Phase 3 endpoint" while the only methods on `*Store` were the `Open` plumbing and the `PutSnapshot` write path. Doc scoped to the code the commit contains; the file list is now part of the sentence so the next reader can check it. | `8a86b8d` | fixed |
| S33 | `8a86b8d` | — | **No content anywhere.** `8a86b8d`'s subject reads "A3's Phase 3 review fixes (S26-S35)" and its body enumerates nine of the ten ids in that range — S26, S27, S28, S29, S30, S31, S32, S34, S35 — never S33. The row exists so a cite of `S33` resolves and so the range-form citation that produced it is on record as unverifiable: a subject line naming a range is not an enumeration of its members. | — | unresolvable |
| S34 | `8a86b8d` | `internal/store` | `now()` had zero call sites, and `go vet` cannot see an unused method. `topn.go`'s `window()` is its caller now. | `8a86b8d` | fixed |
| S35 | `8a86b8d` | `internal/store/store.go` | `ErrUnknownFingerprint` did not wrap `api.ErrQueryDetailNotFound`, so the detail handler's `errors.Is` could not answer the 404 its own doc asks for ("over wrapping [so that] a Phase 3 store [can] add context … without changing what the client sees"). It now wraps it, and the write path's malformed-key error is the separate `ErrMalformedFingerprint`: `statements.go` had used the not-found sentinel for a malformed key, collapsing "this collector is hashing wrong" into "no such row". | `8a86b8d` | fixed |
| S36 | `2a9c429` (in review) | `scripts/check-parity-matrix.py` | An invocation error presenting as a result: running the checker without its one required argument raises `IndexError` at line 2 and exits 1, which reads as "the parity gate failed" when nothing was checked. The script takes the spec path as `sys.argv[1]` and has no usage message, so the failure mode of *not running the checker* is identical to the failure mode of *failing it*. | open | open |
| S37 | `dec45d2` | `scripts/check-parity-matrix.py` | The annotated-ID parser applied every parenthetical in a §7.3 cell to every id in it: `10.1, 10.2, 10.4 *(inventory columns)*, 10.12, 10.16 *(page, sort)* → **1b**` recorded `page, sort` on R10.1 and `inventory columns` on R10.16. 46 entries, 23 of them wrong. Attribution is now per token, via the `split_tokens()` the file already had — the only way to keep `*(page, sort)*`, a note containing a comma, from splitting in half. | `dec45d2` | fixed |
| S38 | `dec45d2` | `scripts/check-parity-matrix.py` | The render printed `sorted(v)[0]`, so an id holding two notes printed one: R10.4 holds three across the row and printed one, R8.1 printed `full surface; re-validates the 1a and 3 partials` and dropped `partial`, R9.7 printed `complete` and dropped `partial`. 12 of the 46 entries lost at least one note. Now joins the full sorted set with `/`. | `dec45d2` | fixed |
| S39 | this review | `scripts/check-parity-matrix.py` | The annotated-ID list is **print-only**: `annotated` is assigned, populated, printed, and read by no check and not by the exit line — so it cannot fail a build, which is the exact class this slot keeps filing against other people's tooling. A typo that silently drops an id from §7.3 shrinks the list with no red line. | open | open |
| S40 | this review | `scripts/check-parity-matrix.py` | `:114` assigns `a_line = True` on the `### 7.3` heading and never reads it, so the parser scans every `^R\d+$` row in the document, including Appendix A's change-log table. Verified inert: gating the loop to §7.3 yields the identical 23 entries, because Appendix A's tokens are `R9.5b fingerprint label (A3-M10)`-shaped and `expand()` rejects them. Latent, not live — it becomes live the first time Appendix A gains a well-formed id with a note. | open | open |
| S41 | this review | requirements.md §7.3, R11 row | A part with no `→` is skipped by the parser, so R11's `(11.6 also re-validated at **5** and **9**)` — a third annotation placement, a parenthetical with no arrow — is never attributed to R11.6. Fixing it means first ruling whether a note with no arrow is an assignment at all. | open | open |

## The other four id forms, and why only `S<NN>` is indexed here

`A3`'s findings have been numbered with five different prefixes across four rounds. Only
the S-series is cited from code, so only the S-series is machine-checked; the rest are
indexed by the documents that authored them and are listed here so a reader who meets one
knows where to go.

| form | round | where it lives | count |
|---|---|---|---|
| `A3's S<NN>` | current | this table | 23 ids, S1–S41 |
| `A3-M1`…`M10` | v0.3.0 architecture review | `docs/reviews/v0.3.0-architecture-review.md` | 7 ids, 16 cites |
| `A3-N1`…`N8` | v0.3.0 architecture review (minor) | same document | 6 ids, 12 cites |
| `A3-B3`, `B4`, `B6`, `B7` | round-1 (blocking) | `docs/reviews/round-1-architecture.md` | 4 ids, 6 cites |
| `A3-Q1`, `Q1.2`, `Q1.3`, `Q2` | rulings owed by the review slot | `requirements.md` prose, and the checker's own section comments | 4 ids, 5 cites |

`A3-Q1.2` is the one that overlaps this file's own subject: it is the checker comment that
generated the annotated-ID list S37/S38 are about.

## The collision the charter did not anticipate

The charter's reporting rule is *"Number findings with a prefix that cannot be mistaken for
a slot name. `S1, S2, …` is the default, and `A1`–`A6` are not available as finding IDs —
they are the six slots."* It avoids the collision it names and walks into two others:

- **`S1`–`S10` are data sources.** requirements.md §3.2's source table uses `S1`…`S10`
  (`S1` = `Cluster.status`, `S2` = Direct SQL), and `internal/collector/collector.go:72`
  encodes it as `SourceClusterStatus SourceID = "S1"`. So at HEAD, `S1` names both
  `Cluster.status` and the R8.4 `meta.total` ruling, and `S3` names both a source and the
  inert-chart finding — in the same commit, `f08b6ac`.
- **`D20's S5`–`S12`** are findings scoped to one deviation, cited from `internal/collector/*.go`.

The collision is bounded and the bound is exploitable: source ids stop at S10, so **S1–S10
are ambiguous** and **S11 and above are unambiguous**. Every finding filed since the
Phase 1a round has been at S21+, which is why the qualified form has worked so far. The
rule this table adopts, and the amendment owed to `.claude/agents/a3-review.md`: a finding
id is written `A3's S<NN>` in prose, and a new review round **starts above S10** rather
than at S1. The checker enforces the qualified form's resolution; it does not yet flag a
bare `S1`–`S10` outside §3.2's table as ambiguous, and that gap is stated rather than
hidden.

## What is not a finding

Rulings the spec answers directly (the R10.5 phase split, the R10.7 identity set) go into
`requirements.md` as prose and are logged in IMPLEMENTATION.md §5 as D-rows, not here. A
row appears in this table only if the review slot filed it as a finding against code or
against the spec's own wording.
