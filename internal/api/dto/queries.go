// R3.2's Queries row, R10.7's Query Detail page, R3.10's plan samples and
// R3.12's tags.
//
// Four of R8.2's twenty endpoints answer from this file:
//
//	GET /api/v1/insights/queries                     → QueriesResponse           (R3.2)
//	GET /api/v1/insights/queries/{fingerprint}       → QueryDetailView           (R10.7)
//	GET /api/v1/insights/queries/{fingerprint}/plans → Envelope[PlanSampleView]  (R3.10)
//	GET /api/v1/insights/tags                        → Envelope[TagView]         (R3.12)
//
// The fifth query route's shapes are in this package, but in series.go rather
// than here. R8.2's `/insights/queries/series` is R8.11's batched endpoint; its
// `QuerySeries` and `QuerySeriesPoint` were written in internal/api/series.go,
// published in api/openapi.yaml with `x-status: implemented`, and moved to
// dto/series.go once the Phase 3 store needed to name them to implement
// `api.QuerySeriesSource` — a store cannot import internal/api, its own
// consumer, and internal/api keeps both names as aliases, so nothing that
// spelled `api.QuerySeries` had to change.
//
// QueryDetailView still has no `points` field, and the reason is no longer the
// import direction. It is that the detail chart is served by the route that
// already exists: series.go's own doc states that carrying all five values per
// point "is what lets one response serve both R10.6's row sparkline and R10.7's
// Query Detail chart", and a batch of one fingerprint is legal
// (`MaxBatchFingerprints` is 50). Inlining the array into the detail body would
// duplicate an answer the client can ask for — the second-copy failure this
// project keeps hitting, in a file whose header exists to prevent exactly that.
//
// The same rule clusters.go sets for ClusterView applies throughout: the
// requirement that defines a column set names its fields. So QueryView's fields
// are R3.2's words (`database`, not R9.3's `datname`) because R3.2 defines that
// table, while api.QuerySeries' are R9.3's label words because R9.3 defines
// that family. Two vocabularies for two contracts, each traceable to the
// requirement that chose it; the mapping between them is stated on the fields
// rather than left to be discovered.
package dto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// LoadColumns is R3.2's load column set — the eight columns the requirement
// sources per query, in R3.2's order and with R3.2's names.
//
// It is its own type because R3.7 requires it twice over: "Each rollup answers
// 'which X accounts for the most load' with the same load columns as R3.2".
// Embedding the struct in QueryView (R3.2's table) and in TagView (R3.7's
// rollup shape) makes "the same load columns" a fact the compiler checks rather
// than a sentence two structs each paraphrase. web/src/api/types.ts uses the
// same trick on the other side of the mirror (`FleetRow extends ClusterView`).
//
// Two of R3.2's named columns are deliberately absent, and both absences are
// R3.2's own rulings rather than omissions:
//
//   - `Load by lock wait` — "has **no per-query source** (`pg_stat_statements`
//     has no lock-time column — §5 divergence table) and renders
//     `n/a (no per-query source, §5)`". A field that can only ever be null is a
//     field the UI has to special-case anyway, so the literal is the UI's and
//     the contract has nothing to say. R3.7 *does* source lock wait at rollup
//     level from `pg_locks` + `pg_stat_activity`, which is why TagView carries
//     LockWaitMs and QueryView does not.
//   - `Analyze latency` — "deferred (Cloud SQL sources it from `auto_explain`,
//     which we sample per R3.10 rather than per statement) and marked **Gap**".
//     A Gap is not a field; adding one now would put a number on the wire that
//     R3.2 says does not exist yet.
//
// The two derived columns are the reason this struct has a per-major-version
// difference baked in, and it is worth stating because R10.28 lists "PG16 vs
// PG17 column differences" as an empty state the UI must render and this struct
// is where that difference originates. R3.2 derives them as:
//
//	Load by IO wait = blk_read_time + blk_write_time                (PG 15, 16)
//	                = shared_blk_read_time + shared_blk_write_time  (PG 17, 18)
//	Load by CPU     = total_exec_time − Load by IO wait
//
// PG17 split the block-time counters into shared/local/temp, so the ≤PG16 pair
// and the PG17+ pair are different columns answering the same question, and one
// cluster's number is not comparable to another's across the boundary. PGMajor
// below is the field that tells the UI which derivation produced the numbers it
// is looking at.
type LoadColumns struct {
	// LoadByTotalTime is R3.2's `Load by total time`: this query's share of the
	// cluster's total load, as a percentage. It is the column the Queries table
	// sorts by (R3.2: "ranked by `total_exec_time`") and the one Cloud SQL
	// renders as the bar behind each row.
	LoadByTotalTime float64 `json:"loadByTotalTime"`

	// PctLoadByQueries is R3.2's `% load by Queries`: the same numerator as
	// LoadByTotalTime expressed against the Queries category rather than
	// against total load.
	//
	// Flagged for A3: R3.2 names both columns and gives a formula for neither.
	// Cloud SQL's load model attributes load to categories (queries, locks, …)
	// and this is the percentage attributed to the queries category, which for a
	// per-query row is the same measurement as LoadByTotalTime restated. The two
	// are separate fields because R3.2 lists them as separate columns; the
	// wording that distinguishes them is the spec's to supply, not ours to
	// invent. Until it does, both carry the R3.2 value and the UI renders both.
	PctLoadByQueries float64 `json:"pctLoadByQueries"`

	// AvgExecTimeMs is R3.2's `Avg execution time (ms)`: the window's mean,
	// which is pg_stat_statements' `mean_exec_time` for the window — an R3.3
	// delta, not the view's cumulative mean (see the window note on QueryView).
	AvgExecTimeMs float64 `json:"avgExecTimeMs"`

	// TotalExecTimeMs is R3.2's `Total execution time (ms)`: the window's
	// `total_exec_time` delta. It is the numerator of every load column here.
	TotalExecTimeMs float64 `json:"totalExecTimeMs"`

	// AvgRowsReturned is R3.2's `Avg rows returned`. A float64 because it is a
	// mean over a delta (R3.3), not a count: 12.5 rows per call is a real answer
	// and an int would round it to 12.
	AvgRowsReturned float64 `json:"avgRowsReturned"`

	// TimesCalled is R3.2's `Times called`: executions in the window, from the
	// `calls` delta. Integral, because a call count is integral even when the
	// rates derived from it are not.
	TimesCalled int64 `json:"timesCalled"`

	// LoadByCPUMs is R3.2's `Load by CPU`, derived per R3.2 as
	// `total_exec_time` minus block I/O time. See the struct doc for the
	// per-major derivation and PGMajor for which one applies to this row.
	LoadByCPUMs float64 `json:"loadByCPUMs"`

	// LoadByIOWaitMs is R3.2's `Load by IO wait`, derived per R3.2 from
	// `blk_read_time`/`blk_write_time` on PG 15–16 and
	// `shared_blk_read_time`/`shared_blk_write_time` on PG 17+.
	LoadByIOWaitMs float64 `json:"loadByIOWaitMs"`
}

// QueryView is one row of GET /api/v1/insights/queries — R3.2's top-N table,
// and the shape R10.6's Queries page renders.
//
// The numbers are R3.3 deltas over the window the request asked for (R8.4's
// `from`/`to`, R10.15's presets), not pg_stat_statements' cumulative counters.
// That is the single most load-bearing fact about this struct and the reason it
// is stated rather than implied: the view's `total_exec_time` since the last
// reset and Cloud SQL's "last hour" are different numbers for the same query,
// and a DTO that does not say which it serves is a DTO the UI cannot label. The
// window itself is not echoed here — R8.4's envelope carries no window field and
// the client named the window it asked for.
//
// N is R9.5's top-N (default 50 per cluster, range 0–1000, N=0 disabling the
// family), and R8.4's `limit`/`sort`/`nextCursor` page through it (R8.10).
type QueryView struct {
	// Fingerprint is R3.4's normalized fingerprint: the canonical key of the
	// query store, of R8.2's `/insights/queries/{fingerprint}` path segment, and
	// of R9.5b's `fingerprint` label. 64 lowercase hex characters — the shape
	// api/openapi.yaml already publishes in its QuerySeries example — because a
	// fixed-length digest is what keeps a label value URL- and path-safe.
	Fingerprint string `json:"fingerprint"`

	// QueryID is PostgreSQL's `queryid`, informational (R9.5b: "The
	// **fingerprint is the canonical key**; `queryid` is informational"). R3.4
	// is why: PG18 changes query-id jumbling, so the id moves under an unchanged
	// statement.
	//
	// A string, not a number, and that is not a style choice. `queryid` is a
	// bigint, so real values routinely exceed 2^53, and encoding/json marshals
	// an int64 to a JSON number that JavaScript then rounds — the UI would
	// display an id the API never sent. api.QuerySeries makes the same call for
	// the same reason (`QueryID string \`json:"queryid,omitempty"\``).
	QueryID string `json:"queryid,omitempty"`

	// Query is R3.2's `Query` column: the normalized template, cut to R3.6's
	// length cap. It is `query.Normalized.DisplayText` — the truncated form —
	// which is why Truncated sits beside it rather than being implied.
	Query string `json:"query"`

	// Truncated is R3.6's flag: "Longer text is truncated and the truncation
	// flagged." The UI needs it to know that the text in Query is not the whole
	// statement, and R3.10 needs it for a harder reason — a truncated template
	// cannot be EXPLAINed, which is one of the two reasons a plan sample is
	// unavailable.
	Truncated bool `json:"truncated"`

	// Database is R3.2's `Database` column (R9.3's `datname` label). R3.2's
	// table has no user column — users are R3.7's rollup and R10.7's detail
	// header — so the per-query row names the database and nothing else.
	Database string `json:"database"`

	// Namespace and Cluster identify the cluster the row came from. R3.2's
	// column set does not name them, but R8.2's route takes `?namespace=` and
	// `?cluster=` as optional filters (R8.4), so an unfiltered request returns
	// rows from every cluster in the allow-list and a row that does not say
	// which cluster it belongs to cannot be rendered, linked, or paged stably.
	// R8.10's cursor is documented as encoding "the sort key plus a secondary
	// (namespace, name) key" for exactly this reason.
	Namespace string `json:"namespace"`
	Cluster   string `json:"cluster"`

	// PGMajor is the target's PostgreSQL major version, and it is the field that
	// resolves R3.2's per-major derivation for this row: the number in
	// LoadByIOWaitMs came from `blk_read_time`/`blk_write_time` if this is 15 or
	// 16 and from `shared_blk_read_time`/`shared_blk_write_time` if it is 17 or
	// 18. R10.28 makes "PG16 vs PG17 column differences" a named empty state
	// the UI must handle; a mixed-cluster page needs to say which derivation each
	// row's numbers came from, and this is the only place that knows.
	PGMajor int `json:"pgMajor"`

	// Recommendations is R3.2's `Recommendations` column, shaped by R7.6.
	//
	// It is `[]` for every Phase 3 response and says so: R7.1 (the index
	// recommender) is marked post-0.1 and the insight engine that fills it is
	// Phase 6's. The field exists now because R3.2 names the column, R10.6's
	// table has to render it, and a column added later is a breaking change to a
	// contract that has a consumer.
	Recommendations []RecommendationView `json:"recommendations"`

	// LoadColumns carries the eight load columns inline (R3.2's set).
	LoadColumns
}

// QueriesResponse is the body of GET /api/v1/insights/queries: R8.4's envelope
// plus R3.1's extension-presence report.
//
// R8.4 states the envelope once as `Envelope[T]`, and a route that needs a key
// beside `data` gets a named struct rather than a field in `meta` — the rule
// ActiveQueriesResponse states for the active-queries route, and the reason this
// route gets a name too. `meta` is reserved for paging and R10.22's
// partial-render contract, so a per-route key parked there is a shared type
// carrying a private field. The key this route needs is R3.1's, which asks for
// presence "per cluster" on the same page whose rows are R3.2's queries: a caller
// reading the queries of a cluster without pg_stat_statements has to learn that
// from the response it already has, because R10.27's "not installed" screen is
// this route's own fallback and a body that only says "zero rows" cannot tell an
// empty result from an uninstrumented one.
//
// Its producer is internal/api/queries.go (A2's file), which served
// `QueriesEnvelope = Envelope[QueryView]` up to the commit that adopted this
// struct and deleted the alias; the pair is dated in that commit rather than
// here. active_queries.go is the precedent it follows: a handler that serves a
// named response names the dto type in its signature and keeps no local alias,
// because the alias would be a second name for one type.
//
// testdata/api/queries.json now carries this shape's `pgStatStatements` rows. It
// could not before: TestQueriesFixtureIsCanonical pinned the fixture to
// `Envelope[QueryView]`, and a field that type does not have cannot appear in a
// file whose bytes are that type's canonical encoding. a83612b recorded the
// deferral ("the -update run belongs to that commit, not to this one"); the
// commit that switched the test's type to this one ended it, and the fixture's
// four rows are the -update run that belongs to it.
type QueriesResponse struct {
	// Data is R3.2's rows — R8.4's `data`, identical in shape to the other four
	// list routes'. Never null: an empty list marshals to [].
	Data []QueryView `json:"data"`

	// Meta is R8.4's envelope metadata.
	Meta Meta `json:"meta"`

	// PGStatStatements is R3.1's report: one row per database the process knows
	// about. It is a list rather than a single boolean because R3.1's unit of
	// detection is a database and its unit of reporting is a cluster, and one
	// boolean per response describes neither of them across a fleet.
	PGStatStatements []ExtensionPresenceView `json:"pgStatStatements"`
}

// ExtensionPresenceView is one row of R3.1's per-database presence report.
//
// R3.1's sentence names two detection sources and one reporting scope: "Detect
// `pg_stat_statements` presence per database (from `pg_extension` via S2 and from
// `cnpg_pg_extensions_*` via S3) and report it per cluster." This struct is the
// per-database half; the two identity fields are what make it reportable per
// cluster.
type ExtensionPresenceView struct {
	// Namespace and Cluster are R10.16's cluster identity, the pair every list
	// row in this package carries (QueryView, FleetRow, TagView). They are the
	// join key: a per-database row without them cannot be attributed to the
	// cluster whose page renders it, which is the half of R3.1's sentence that
	// says "report it per cluster".
	Namespace string `json:"namespace"`
	Cluster   string `json:"cluster"`

	// Database is R3.1's detection unit — "presence per database". The word is
	// R3.2's `database`, not R9.3's `datname`, for the reason this file's header
	// gives: the requirement that defines a column set names its fields, and R3.2
	// defines this table's vocabulary while R9.3 defines a metric family's.
	Database string `json:"database"`

	// Installed is the tri-state R3.1's two sources force: nil means not known,
	// &false means absent, &true means present.
	//
	// A plain bool cannot hold the first state, and the first state is not an edge
	// case — it is the normal result of asking two sources that do not cover the
	// same ground. S2 answers for every database in a cluster the driver can
	// reach; S3 answers only for the databases a CNPG pod exports metrics for, so
	// a database S2 reported and S3 has no series for is not a database without
	// the extension. R10.27's screen renders the words "not installed", and
	// rendering them for a database nobody measured is the difference between a
	// gap and a wrong answer.
	//
	// omitempty on a *bool omits the key when the pointer is nil and emits `false`
	// or `true` when it is not, which is what makes "absent from the body" a
	// representable third state rather than a field that has to guess.
	Installed *bool `json:"installed,omitempty"`
}

// QueryDetailView is GET /api/v1/insights/queries/{fingerprint} — R10.7's
// Query Detail page, field for field.
//
// R10.7's sentence is the whole spec: "header (fingerprint, database, user,
// first-seen), the per-minute load chart for that query, latency panel
// (P50/P95/P99 or the R3.9 fallback), plan samples list with a JSON/plan viewer,
// and recommendations."
//
// It embeds QueryView rather than re-declaring fingerprint and database, so the
// header's identity half and the R3.2 columns come from one definition (the same
// `extends` pattern types.ts uses for FleetRow). The practical reason is R8.11:
// the page is reached from a table row the client already has, and a detail
// response that omitted the row's numbers would force the client to keep the
// list response alive to render a detail page.
//
// The per-minute load chart is the one R10.7 element with no field here, and
// that is a routing fact rather than an oversight: its data is R8.11's
// `/insights/queries/series`, which A2 shipped and whose point type lives in
// package `api` (see this file's header for the import-cycle reason and the
// "one response serves both R10.6's row sparkline and R10.7's Query Detail
// chart" rationale). The client asks it for this one fingerprint.
type QueryDetailView struct {
	QueryView

	// User is R10.7's header `user` (R9.3's `usename` label): the role that
	// executed the query. R3.2's table has no user column, so this field exists
	// only on the detail shape — which is where R10.7 asks for it.
	//
	// It is a single value because the fingerprint is the key and R9.4's row
	// identity is (cluster, db, user): a statement run by two roles is two rows
	// in the store and two rows in the table, each naming its own user.
	User string `json:"user,omitempty"`

	// FirstSeen is R10.7's header `first-seen`: when the store first observed
	// this fingerprint. RFC 3339 UTC like every timestamp the API emits (R10.21).
	FirstSeen time.Time `json:"firstSeen"`

	// Latency is R10.7's "latency panel (P50/P95/P99 or the R3.9 fallback)".
	Latency LatencyView `json:"latency"`

	// Plans is R10.7's "plan samples list with a JSON/plan viewer" (R3.10). The
	// same list R8.2's `/insights/queries/{fingerprint}/plans` route serves on
	// its own; it is inline here because R10.7 puts it on this page, and a
	// detail page that needed a second request to render its own list is the
	// R10.14 budget spent on the wrong thing.
	Plans []PlanSampleView `json:"plans"`

	// PlanChanges is R3.11's plan-change events for this fingerprint: "keep plan
	// hashes per fingerprint and surface a plan-change event when the hash
	// changes for a stable fingerprint — the regression detection Cloud SQL gets
	// from `auto_explain` and we get from bounded sampling."
	//
	// It is a list rather than a boolean because R3.11's value is the *series* of
	// changes over the retention window: one event says the plan moved, two say
	// it moved twice, and the regression question ("is this query slower than
	// last week", R3.14) is answered by lining the change up against the latency
	// curve above it.
	PlanChanges []PlanChangeView `json:"planChanges"`
}

// LatencyView is R10.7's latency panel, and R3.9 is the requirement that decides
// its shape.
//
// R3.9's problem in one line: "Cloud SQL serves P50/P95/P99 from a DISTRIBUTION
// metric. `pg_stat_statements` gives min/mean/max/stddev, not a distribution."
// So the panel has two halves and the UI renders whichever the data supports —
// which is exactly what R10.7's parenthetical "(P50/P95/P99 **or the R3.9
// fallback**)" means.
//
// The four stats are pg_stat_statements' own (R3.9: "v1 serves
// min/mean/max/stddev"). The three percentiles are Cloud SQL's columns and are
// pointers, because in v1 there is usually no distribution to take them from: an
// absent percentile and a zero one are different answers, and 0 ms is a perfectly
// plausible execution time, so `omitempty` on a float64 would render "we have no
// p95" and "the p95 is zero" identically. A nil pointer omits the field and a
// pointer to 0 emits `"p50Ms": 0`.
//
// The histogram is R3.9's second half: "plus a **sampled** histogram built from
// the `activity` collector's per-session durations (R4), using our own named
// buckets (`1, 5, 25, 100, 500, 1000, 5000, 10000` ms) and accounted in §5 as
// R9.13."
type LatencyView struct {
	// The four stats pg_stat_statements actually has (R3.9), over the R3.3
	// window. Milliseconds, matching R3.2's `(ms)` column spelling.
	MinMs    float64 `json:"minMs"`
	MeanMs   float64 `json:"meanMs"`
	MaxMs    float64 `json:"maxMs"`
	StddevMs float64 `json:"stddevMs"`

	// The three percentiles Cloud SQL serves. Present only when a real
	// distribution exists to produce them; nil (absent) is the R3.9 fallback's
	// answer and means "render the four stats and the histogram instead".
	P50Ms *float64 `json:"p50Ms,omitempty"`
	P95Ms *float64 `json:"p95Ms,omitempty"`
	P99Ms *float64 `json:"p99Ms,omitempty"`

	// Sampled marks R3.9's Divergence: the histogram below is built from the
	// activity collector's 60 s samples, not from every execution. R3.9 requires
	// the UI label the panel "sampled, biased toward long queries" — "a 60 s
	// sample of `pg_stat_activity` observes long-running statements far more
	// often than short ones, and an unlabelled histogram overstates p95" — and
	// this field is what tells the UI to render that label.
	//
	// It is a field rather than a constant in the UI because it is not constant:
	// when the percentiles above are populated from a real distribution, the
	// histogram is not sampled and the label must not appear.
	Sampled bool `json:"sampled"`

	// Buckets is R3.9's named bucket set, and R9.13's accounted histogram.
	Buckets []HistogramBucket `json:"buckets"`

	// SumMs and Count are R9.13's `_sum` and `_count` — the two series R9.13
	// counts alongside the eight buckets ("50 fingerprints × (8 buckets + `_sum`
	// + `_count`) = **450 series per cluster**").
	//
	// Count duplicates the +Inf bucket's count rather than replacing it: R9.13
	// accounts `_count` as its own series, and the invariant between the two is
	// what makes the histogram auditable — every bucketed duration is counted
	// exactly once, so the +Inf bucket and `_count` agree.
	SumMs float64 `json:"sumMs"`
	Count int64   `json:"count"`
}

// HistogramBucket is one of R3.9's named latency buckets.
type HistogramBucket struct {
	// Le is the bucket's upper bound in milliseconds, spelled the way Prometheus
	// spells it: "1", "5", "25", "100", "500", "1000", "5000", "10000" —
	// R3.9's eight named buckets — plus "+Inf" for the overflow bucket.
	//
	// A string rather than a float64 because encoding/json cannot marshal +Inf
	// at all (`json: unsupported value: +Inf`), and the overflow bucket is
	// exactly the half R9.13 counts as `_count`. A float field would make the
	// one bucket the accounting requires unrepresentable.
	Le string `json:"le"`

	// Count is the number of sampled executions at or below Le.
	Count int64 `json:"count"`
}

// PlanSampleView is one captured plan — R3.10's plan sample, and the element of
// R8.2's `/insights/queries/{fingerprint}/plans` response.
//
// R3.10 constrains capture in three ways and all three show up in this shape:
//
//   - (a) "the stored template is normalized (`$1`,`$2`) and truncated, so
//     `EXPLAIN` of it is a syntax error — we EXPLAIN the **first-seen raw text**
//     (R3.4) and **skip truncated rows**". Hence SQL below: the plan's subject is
//     the raw statement, not the template the table row displays, and the two
//     differ by R3.5's construction.
//   - (b) "for a parameterized statement we use `PREPARE` + `EXPLAIN EXECUTE` to
//     get the generic plan rather than a pet plan". Hence Method.
//   - (c) "on a `pg_monitor`-only deployment (R12.4) the role has statistics
//     visibility but **no `SELECT` on application tables**, so EXPLAIN returns
//     `permission denied for table …` — we report `unavailable (insufficient
//     table privileges)` as a state, not as a per-query failure". Hence
//     Availability, whose value is that exact string.
//
// R3.10 also rules that "`EXPLAIN ANALYZE` is **never** run automatically (it
// executes the statement); it is available on explicit user request, and only for
// `SELECT`s unless `--allow-analyze-write=true`." A sample captured that way is
// MethodAnalyze, so the automatic and the requested cases are distinguishable in
// the list rather than being conflated into one "here is the plan" field.
type PlanSampleView struct {
	// CapturedAt is when the plan was captured (A6 asked for `captured_at`; the
	// dto convention is camelCase — `generatedAt`, `lastSuccessTime`,
	// `phaseReason` — so the tag is `capturedAt`). RFC 3339 UTC (R10.21).
	CapturedAt time.Time `json:"capturedAt"`

	// Method is how the plan was obtained, from the Method* constants below
	// (R3.10's (b) and the EXPLAIN ANALYZE rule).
	Method string `json:"method"`

	// Availability is the R3.10 state — the Available/Unavailable pair below.
	//
	// The value is the exact string R3.10 puts in the spec
	// (`unavailable (insufficient table privileges)`) rather than a second
	// snake_case spelling of it, so the UI renders the field instead of
	// maintaining a wording table the spec already owns.
	//
	// It is a state on the sample and not an ErrorBody because R3.10 says so:
	// the query is fine and the plan is simply not obtainable. R12.4's
	// pg_monitor-only deployment is a supported configuration, and reporting it
	// as a per-query failure would turn a supported deployment into an error page
	// for every query on the cluster.
	Availability string `json:"availability"`

	// SQL is the raw first-seen statement text that was EXPLAINed (R3.10's (a)).
	// It is present because the plan belongs to that text and not to the
	// normalized template in QueryView.Query, and the viewer has to be able to
	// show which one it is rendering.
	//
	// An unavailable sample still names its text: R3.10's (c) is about a specific
	// table the role cannot read, so the statement was submitted and failed, and
	// the text is the only evidence of which one. Omitted only when there was no
	// text to report.
	SQL string `json:"sql,omitempty"`

	// PlanJSON is the `EXPLAIN (FORMAT JSON)` output, verbatim.
	//
	// json.RawMessage rather than a string or a parsed structure: a string would
	// make the UI parse JSON out of a JSON string (the viewer's first job would
	// be unescaping), and a parsed structure would commit the API to a shape
	// Postgres owns and changes across majors (R2.4's matrix governs the *views*,
	// not EXPLAIN's output). RawMessage passes Postgres's bytes through
	// untouched. Omitted when there is no plan, which is R3.10's (c).
	PlanJSON json.RawMessage `json:"plan,omitempty"`

	// PlanHash is R3.11's plan hash — the value a plan-change event compares
	// against. Present whenever there is a plan; its absence on an available plan
	// is a bug, not a state.
	//
	// R3.11 says "keep plan hashes" and leaves the derivation open. Ours is the
	// SHA-256 of the plan JSON's canonical compact encoding, hex-encoded:
	// compact, so re-indenting a stored plan is not a plan change, and over the
	// JSON rather than over a hand-picked subset, because "the subset that
	// matters" is the question R3.11 does not answer and an unanswerable field is
	// worse than an over-sensitive one.
	//
	// Flagged for the implementation that ships it: hashing the whole JSON makes
	// an `EXPLAIN ANALYZE` capture look like a change whenever the planner's cost
	// estimates move, and they move on every ANALYZE. R3.11 wants plan changes,
	// not cost jitter, so a shape-only digest (the node-type tree, estimates
	// excluded) is the better answer — but the contract carries a hex digest
	// either way, so the choice can be made without a wire change.
	PlanHash string `json:"planHash,omitempty"`

	// Message carries the underlying text for a non-available state, e.g.
	// `permission denied for table orders` (R3.10's (c) quotes the shape). The
	// state says *why* in category; this says which table.
	Message string `json:"message,omitempty"`
}

// R3.10's capture methods, named as constants because R3.10 names three
// behaviours (plain EXPLAIN, PREPARE + EXPLAIN EXECUTE, and EXPLAIN ANALYZE on
// explicit request) and a client switching on a string literal copied from a
// comment is how an enum rots.
const (
	// MethodExplain is a plain `EXPLAIN (FORMAT JSON)` of the raw first-seen text
	// — R3.10's default path for a statement with no parameters.
	MethodExplain = "explain"

	// MethodPrepareExecute is `PREPARE` + `EXPLAIN EXECUTE`, which R3.10's (b)
	// requires for a parameterized statement so the plan is the generic one and
	// not a pet plan tuned to one parameter set.
	MethodPrepareExecute = "prepare_execute"

	// MethodAnalyze is `EXPLAIN ANALYZE`, which R3.10 says is "never run
	// automatically (it executes the statement)" and is "available on explicit
	// user request, and only for `SELECT`s unless --allow-analyze-write=true".
	// A sample with this method was asked for; nothing in Phase 3 produces it
	// without a user asking.
	MethodAnalyze = "analyze"
)

// R3.10's plan-availability states. The values are the spec's own wording so
// there is exactly one spelling of each in the codebase.
const (
	// PlanAvailable means the sample carries a plan in PlanJSON.
	PlanAvailable = "available"

	// PlanUnavailableTablePrivileges is R3.10's (c), quoted from the spec
	// verbatim: `unavailable (insufficient table privileges)`. R12.4's
	// pg_monitor-only role can read statistics but not the application tables, so
	// EXPLAIN fails and R3.10 requires this be a state rather than a failure.
	PlanUnavailableTablePrivileges = "unavailable (insufficient table privileges)"
)

// PlanChangeView is one R3.11 plan-change event.
//
// R3.11: "keep plan hashes per fingerprint and surface a plan-change event when
// the hash changes for a stable fingerprint — the regression detection Cloud SQL
// gets from `auto_explain` and we get from bounded sampling." The event is the
// hash moving from one value to the other, and nothing else: R3.11's detection
// is the change itself, and attributing a latency movement to it is R3.14's
// week-over-week question, not this struct's answer.
type PlanChangeView struct {
	// DetectedAt is when the new hash was first observed. RFC 3339 UTC (R10.21).
	DetectedAt time.Time `json:"detectedAt"`

	// PreviousPlanHash and CurrentPlanHash are the two hashes. Both are always
	// present: an event with one hash is not a change, and R3.11's whole claim is
	// that there are two.
	PreviousPlanHash string `json:"previousPlanHash"`
	CurrentPlanHash  string `json:"currentPlanHash"`
}

// TagView is one tag — R3.12's dimensions plus the R3.7 load columns, and the
// element of R8.2's `/insights/tags` response.
//
// R3.12 defines what a tag is here, and it is explicit that this is a
// **Divergence**: "Cloud SQL's `pertag` metrics carry `action, application,
// controller, db_driver, framework, route, tag_hash`, sourced from app-set query
// tags. Generic PostgreSQL has no tag mechanism; our answer is (a)
// `application_name`, `usename`, `datname` as the always-available dimensions,
// and (b) an opt-in documented convention — a structured SQL comment
// **`/* pgsi:action=…,route=…,controller=…,framework=…,dbdriver=… */`** that we
// parse out of the statement text and promote to tag dimensions with a
// `tag_hash`."
//
// The two halves are separate field groups below, because they have different
// guarantees: (a) is always populated and (b) only exists when the app opts in.
// R3.12 also retires a spelling — "One spelling: `pgsi:` — the spec's earlier
// `pgsio:` variant is retired" — and fixes the key set: "The comment grammar now
// names all five app-supplied keys Cloud SQL documents (0.1.0 named three of
// six)."
//
// One spelling detail the two halves disagree on, resolved in favour of each
// source: Cloud SQL's metric label is `db_driver` and the comment grammar's key
// is `dbdriver` (no underscore). The field is `dbDriver` — the metric spelling,
// camelCased to this package's convention — because R9.6's exported per-tag
// family is keyed on the metric vocabulary and this struct has to agree with the
// series it describes. The comment key is parsed as `dbdriver`; that mapping is
// the parser's, and it is stated here so the two spellings are a documented
// correspondence rather than a typo one of the two files will eventually "fix".
//
// Note that R3.12's set has no `usename`/`datname` in Cloud SQL's list but ours
// needs them (they are R3.12's (a)); and that R3.7's rollup list — database,
// user, application name, client address — does not include tags at all. So this
// struct answers R8.2's `/insights/tags` route, which exists because R3.12 and
// R9.6 define tags, and borrows its load columns from R3.2 per R3.7's rule that
// every rollup carries them.
type TagView struct {
	// TagHash is R3.12's `tag_hash` and R9.6's family key: the hash of the tag
	// dimension set, so one tag is one series and two apps that agree on the
	// dimensions agree on the hash.
	TagHash string `json:"tagHash"`

	// R3.12's (a): the always-available dimensions.
	Application string `json:"application,omitempty"` // application_name
	Database    string `json:"database,omitempty"`    // datname
	User        string `json:"user,omitempty"`        // usename

	// R3.12's (b): the five app-supplied keys the comment grammar names. All are
	// optional — a statement with no `/* pgsi:… */` comment has none of them and
	// still has the (a) dimensions above.
	Action     string `json:"action,omitempty"`
	Route      string `json:"route,omitempty"`
	Controller string `json:"controller,omitempty"`
	Framework  string `json:"framework,omitempty"`
	DBDriver   string `json:"dbDriver,omitempty"` // comment key `dbdriver`, metric `db_driver`

	// LoadColumns is R3.2's set, which R3.7 requires of every rollup: "Each
	// rollup answers 'which X accounts for the most load' with the same load
	// columns as R3.2".
	LoadColumns

	// LockWaitMs is the column R3.2 denies to a per-query row but grants at this
	// level: R3.7 sources `Load by lock wait` "at database/user level from
	// `pg_locks` + `pg_stat_activity`" and says so in the same sentence that
	// withholds it per query. A rollup that omitted it would be missing the one
	// column the spec says is available here and not there.
	LockWaitMs float64 `json:"lockWaitMs"`
}

// TagDigest is R3.12's `tag_hash` for a set of dimensions: the SHA-256 of the
// populated dimensions as sorted `key=value` pairs joined with "|", hex-encoded.
//
// R3.12 names `tag_hash` and never defines it, and the definition is load-bearing:
// the hash is what makes two apps that agree on the dimensions agree on the tag,
// which is the only reason a tag can be R9.6's metric key and R8.2's URL segment at
// the same time. Sorting the keys and dropping the empty ones is what makes the
// digest independent of the order the comment grammar happened to list them in —
// `/* pgsi:route=/x,action=y */` and `/* pgsi:action=y,route=/x */` are one tag,
// and a hash computed by concatenating fields in struct order would say otherwise.
//
// The key spellings are R3.12's comment grammar's (`dbdriver`, not Cloud SQL's
// `db_driver`), so the digest of a parsed comment and the digest of the comment
// text agree on one spelling. The field above is `dbDriver` for the metric-side
// reason given on TagView; this function is the comment-side one.
func TagDigest(t TagView) string {
	pairs := make([]string, 0, 8)
	for _, kv := range [][2]string{
		{"action", t.Action},
		{"application", t.Application},
		{"controller", t.Controller},
		{"database", t.Database},
		{"dbdriver", t.DBDriver},
		{"framework", t.Framework},
		{"route", t.Route},
		{"user", t.User},
	} {
		if kv[1] != "" {
			pairs = append(pairs, kv[0]+"="+kv[1])
		}
	}
	slices.Sort(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "|")))
	return hex.EncodeToString(sum[:])
}

// RecommendationView is one recommendation, shaped by R7.6 — which is the
// requirement that makes recommendations a contract rather than a string:
// "Every recommendation carries: the finding, the evidence (metrics + query
// fingerprints), the suggested action, and a `confidence` field. Recommendations
// the data cannot support are suppressed, not guessed."
//
// The suppression rule is why this is a struct and not a `[]string`: a
// recommendation that cannot cite its evidence is not suppressible, because
// there is nothing to check. The four fields are R7.6's four, in R7.6's order.
type RecommendationView struct {
	// Finding is what was observed, e.g. `sequential scan on orders for a
	// predicate with no matching index`.
	Finding string `json:"finding"`

	// Evidence is R7.6's "the evidence (metrics + query fingerprints)": the
	// numbers the finding rests on, each one a metric name with its value or a
	// query fingerprint. A client can follow a fingerprint back to
	// R8.2's `/insights/queries/{fingerprint}`, which is the reason the
	// fingerprints are in the payload rather than in a log line.
	Evidence []string `json:"evidence"`

	// Action is R7.6's "the suggested action", e.g. `CREATE INDEX ON orders
	// (customer_id)`.
	Action string `json:"action"`

	// Confidence is R7.6's `confidence` field.
	//
	// A string with no enum, and that is a flagged gap rather than a decision:
	// R7.6 requires the field and never names its values, and R7.1 — the
	// recommender that would populate it — is marked post-0.1, so there is no
	// implementation to derive a vocabulary from yet. Enumerating it now would
	// freeze a scale the recommender has not been written against. A3 owns the
	// wording; the field is here because R7.6 requires it and R3.2's column needs
	// it.
	Confidence string `json:"confidence"`
}
