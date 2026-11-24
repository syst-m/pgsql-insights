// R8.11's batched-series response types (GET /api/v1/insights/queries/series).
//
// These two structs live here, not in internal/api, because of who has to
// produce them. `internal/api.QuerySeriesSource` — the read interface the batch
// route depends on — has `QuerySeries(...) ([]QuerySeries, error)` in its
// method set, so the Phase 3 store that implements it would have to import
// internal/api, its own consumer, to name its own return type. The dto package
// is the leaf every consumer can import: internal/api already does, and
// internal/store will. The same argument put QuerySeriesEnvelope here a month
// earlier (it is `dto.Envelope[QuerySeries]`, aliased in series.go), and the
// one A5 raised in dto/queries.go's header — "moving the series types into dto
// is a change to A2's file and is flagged as such rather than made here" — is
// the change this file makes.
//
// internal/api keeps both names as aliases (series.go), so every existing
// `api.QuerySeries` spelling — the interface, the empty source, the tests, the
// OpenAPI annotation table's `Go:` fields — keeps compiling and keeps naming the
// one definition. That is the alias half of the pattern, not a compatibility
// shim: an alias creates no second type, so there is nothing for the two names
// to disagree about.

package dto

import "time"

// QuerySeriesPoint is one minute of one fingerprint's series. The six values
// are R9.4's aggregate family members, named from the member list in
// promql_catalogue.go rather than re-invented here: execution_time, rows,
// shared_blocks_hit, shared_blocks_read, lock_time, io_time.
//
// Carrying all six per point is what lets one response serve both R10.6's row
// sparkline and R10.7's Query Detail chart, and it is why the route takes no
// `metric=` parameter — R8.11's signature does not name one, and a per-metric
// request would need six requests per page to fill one table of sparklines,
// which is the R10.14 budget spent on the wrong thing.
//
// io_time is the member the requirements.md ruling at 5668cb3 admitted to the
// list — the second admission after lock_time — and it is the one value here
// with no metric of its own: its catalogue entry is derived, the sum of the two
// block-time counters, which is the same derivation R3.2's Load by IO wait
// column publishes as `loadByIOWaitMs` on a list row. The store keeps the
// number in milliseconds (`query.io_wait_ms`, `query_rollup.io_wait_ms`), so
// the value here is that column over `msPerSecond`, the way
// ExecutionTimeSeconds is `total_exec_ms` over the same constant. The store's
// batched read now selects the column: internal/store/series.go's `seriesSQL`
// carries all seven value columns as of A5's 9a16c71, so the value here is the
// column's and not the zero the type used to supply. Until that commit this
// paragraph said the select had not landed — it was written against the
// six-column read and outlived it by one commit, which is the whole cost of
// describing a sibling file in prose no tool checks: the file moved, the
// description did not, and nothing went red.
//
// float64 for all six because that is the type the R9.4 metric family carries
// (a Prometheus sample value); a per-minute delta derived from a counter is not
// necessarily integral even when the underlying count is. R8.3's ruling at
// 7cecc49 makes two of the six `*float64` — lockTimeSeconds and ioTimeSeconds,
// the two whose source can be absent — and the struct lines themselves
// (`internal/api/dto/series.go:63`, `internal/api/dto/series.go:64`) are A1's
// to flip; the document already says `nullable: true` on both.
type QuerySeriesPoint struct {
	// Timestamp is the start of the minute, RFC 3339 in UTC (R10.21: every
	// timestamp the API emits is RFC 3339 UTC, and R8.4's meta.generatedAt is
	// the same type for the same reason).
	Timestamp time.Time `json:"timestamp"`

	ExecutionTimeSeconds float64 `json:"executionTimeSeconds"`
	Rows                 float64 `json:"rows"`
	SharedBlocksHit      float64 `json:"sharedBlocksHit"`
	SharedBlocksRead     float64 `json:"sharedBlocksRead"`
	LockTimeSeconds      float64 `json:"lockTimeSeconds"`
	IOTimeSeconds        float64 `json:"ioTimeSeconds"`
}

// QuerySeries is one requested fingerprint's answer.
//
// The label set is R9.3's contract. namespace and cluster are always present
// because the request is scoped to them. pod and instanceRole are present only
// when the answer is a single instance's — R5.7's compare-up-to-5 needs the
// (namespace, cluster, pod) triple to label a per-instance series — and empty
// when the points aggregate across the cluster's instances, which is what
// R10.6's table row asks for and why one entry per fingerprint is possible at
// all. datname and usename are R9.3's database/user additions and the other
// half of R9.4's row identity (per cluster, db, user). queryid is R9.5b's
// secondary, informational identifier: R3.4 is why it is optional and why it is
// never the key — `fingerprint` is.
//
// Points is `[]`, never null, for a fingerprint the store has never seen: an
// absent array and an empty one mean the same thing to the UI, and R8.3's house
// rule (and clusters_test.go's TestEmptyFleetRendersArrayNotNull) is that the
// API says "none" with `[]`.
type QuerySeries struct {
	Fingerprint  string `json:"fingerprint"`
	QueryID      string `json:"queryid,omitempty"`
	Namespace    string `json:"namespace"`
	Cluster      string `json:"cluster"`
	Pod          string `json:"pod,omitempty"`
	InstanceRole string `json:"instanceRole,omitempty"`
	Datname      string `json:"datname,omitempty"`
	Usename      string `json:"usename,omitempty"`

	Points []QuerySeriesPoint `json:"points"`
}
