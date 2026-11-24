// series.go is R8.11's batched series read: the points behind
// GET /api/v1/insights/queries/series?fingerprints=a,b,c&namespace=&cluster=&range=.
//
// The route takes a *batch* — up to api.MaxBatchFingerprints fingerprints in one
// request — because R10.6's Queries table renders a sparkline per row and one
// request per row is a page of requests. The store's side of that is one query: the
// WHERE takes the whole list through whereBuilder.in, so a 50-fingerprint page is
// fifty placeholders and one round trip rather than fifty prepared statements.
//
// # One entry per fingerprint, and what its labels can honestly say
//
// api.QuerySeriesSource's contract is "one entry per fingerprint in q.Fingerprints,
// in any order, with empty (not missing) Points for a fingerprint it has never
// seen", and the reason fewer-entries-is-legal-but-empty-is-required is that "one
// stale row in a 50-row page load must not turn into a 404 for the other 49". So
// this file answers len(q.Fingerprints) entries always — the same choice
// emptyQuerySeries makes and for the reason it states: `data: []` beside
// `total: 50` reads as "you were truncated" when the truth is "all 50 exist, none
// has points yet".
//
// The entries come back in request order. The contract allows any order; request
// order costs nothing (q.Fingerprints is already deduplicated in request order by
// parseFingerprints, which preserves it "because R10.6 renders rows in the order it
// asked") and it means the handler's meta and the UI's rows line up without either
// one sorting.
//
// The labels are the request's own scoping — Fingerprint, Namespace, Cluster,
// Datname — which is exactly the set emptyQuerySeries copies, and the reason to copy
// it from there is that it is the set an implementation with no data can fill, so
// nothing here is a label the store only sometimes has. Pod and InstanceRole stay
// empty because the points aggregate across the cluster's instances (dto: they are
// "present only when the answer is a single instance's", which is R5.7's
// per-instance series and not this route's); Usename for the same reason on the
// other half of R9.4's identity. QueryID stays empty too, and that one is a
// judgement call worth stating: the batch entry's identity is (fingerprint,
// namespace, cluster) plus optionally datname, while the queryid that belongs to a
// row is computed by topn.go's correlated subquery over a *finer* identity — the
// busiest source within one (fingerprint, namespace, cluster, datname). Copying
// that subquery here with a different scope would be a second copy of a contract
// with a different answer, and R9.5b makes queryid "informational" and R3.4 makes
// it "never the key", so the batch route leaves it to the two routes that have a
// well-defined one.
package store

import (
	"context"
	"fmt"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// QuerySeries's signature is asserted against the contract it implements, for the
// reason detail.go gives: a Go method no interface names is never checked, and the
// first draft of this package had two such methods.
var _ api.QuerySeriesSource = (*Store)(nil)

// seriesSQL is the batch read: one row per (fingerprint, minute) for every
// requested fingerprint, with the per-pod and per-user rows folded into the one
// series the entry reports.
//
// The GROUP BY is (fingerprint, bucket) and not the rollup PK's eight columns,
// which is the difference between the two read shapes in this package: the list
// route's row identity is (fingerprint, namespace, cluster, datname) because
// dto.QueryView has a Database field and R10.6's table shows one row per database,
// while this route's is one entry per *fingerprint* because that is what the request
// names. The datname filter in the WHERE is what reconciles them — with ?db= set the
// group is exactly the list row's aggregate, and without it every database's rows
// fold into the one entry the contract asks for.
//
// lock_wait_ms is the one column that cannot be summed. The schema's -1 is a code
// meaning "this source has no lock_time" (A3's S25 interim encoding, statements.go's
// noLockWait), and a code is not a duration: SUM of -1 and 5.0 is 4.0, a number
// that looks like a measurement and isn't. The CASE poisons the group the way an
// unknown should — one source that cannot answer makes the minute unable to answer —
// while two real per-pod deltas still sum, which is what a per-minute aggregate of
// two pods means.
//
// io_wait_ms gets no CASE because it has no code to protect. schema.go:236 declares it
// REAL NOT NULL with no default — the -1 at :240 belongs to lock_wait_ms alone — and
// the collector fills it on every row from the major's IO pair, so a 0 there is a real
// 0 and not "no source" wearing a 0. That is the answer to the question A2 asked twice
// about how the -1 encoding reaches dto.QuerySeriesPoint.IOTimeSeconds: it does not,
// and the conversion is a plain division. A6's stackSeries guard for a -1 in
// ioTimeSeconds (34ae29d) is correct for lockTimeSeconds and unreachable for this
// field; the two columns share a sentinel in that commit's prose and not in the schema.
const seriesSQL = `
SELECT r.fingerprint, r.bucket,
       SUM(r.calls)            AS calls,
       SUM(r.total_exec_ms)    AS total_exec_ms,
       SUM(r.rows_returned)    AS rows_returned,
       SUM(r.shared_blks_hit)  AS shared_blks_hit,
       SUM(r.shared_blks_read) AS shared_blks_read,
       SUM(r.io_wait_ms)       AS io_wait_ms,
       CASE WHEN MIN(r.lock_wait_ms) < 0 THEN -1 ELSE SUM(r.lock_wait_ms) END AS lock_wait_ms
FROM query_rollup r
`

// seriesGroupBy closes the aggregate and orders the result so each fingerprint's
// points arrive oldest-first, which is the order the sparkline draws and the order
// the entry's Points slice keeps.
const seriesGroupBy = `
GROUP BY r.fingerprint, r.bucket
ORDER BY r.fingerprint, r.bucket
`

// msPerSecond converts the columns' milliseconds to the DTO's seconds.
//
// The reason it is a named const and not a literal: dto.QuerySeriesPoint's two
// duration fields are ExecutionTimeSeconds and LockTimeSeconds (R9.4's Prometheus
// family is in seconds — execution_time_seconds, lock_time_seconds) while the
// columns are total_exec_ms and lock_wait_ms (pg_stat_statements' unit), so a
// struct literal in this file divides some fields and not others. An unlabelled
// 1000 next to a Rows that is correctly not divided is exactly how a unit bug
// survives review; a named one makes the two units visible in the same expression.
const msPerSecond = 1000.0

// seriesRow is one (fingerprint, minute) aggregate, before it becomes a DTO point.
type seriesRow struct {
	fingerprint  string
	bucket       int64
	calls        int64
	totalExecMs  float64
	rowsReturned float64
	sharedHit    float64
	sharedRead   float64
	// ioWaitMs is the rollup's IO column in milliseconds. No sentinel: see the
	// seriesSQL doc for why this column, unlike lockWaitMs, has no code to carry.
	ioWaitMs float64
	// lockWaitMs carries the schema's -1 through unchanged; point() decides what
	// the DTO's seconds field says about it.
	lockWaitMs float64
}

// point converts the row to the DTO's point.
//
// The -1 sentinel becomes nil, not -1 and not -0.001. R8.3's ruling at 7cecc49 —
// implemented on the DTO by A1 — spells "no value" with `null`, so the code's whole
// job here is to be recognised and dropped. The two prose encodings it replaces were
// both wrong in the way the ruling names: -1 was a milliseconds value assigned
// undivided to a seconds-typed field, and -0.001 was that code divided by a unit
// conversion, which reads as a duration and is not one. A nil pointer is the answer
// that does not have to be a number.
//
// IOTimeSeconds always gets a value: its column is NOT NULL with no code (see
// seriesSQL's doc), so its pointer is the contract's nullable pair, not the data's.
func (r seriesRow) point() dto.QuerySeriesPoint {
	p := dto.QuerySeriesPoint{
		Timestamp:            timeFromUnix(r.bucket),
		ExecutionTimeSeconds: r.totalExecMs / msPerSecond,
		Rows:                 r.rowsReturned,
		SharedBlocksHit:      r.sharedHit,
		SharedBlocksRead:     r.sharedRead,
		IOTimeSeconds:        fptr(r.ioWaitMs / msPerSecond),
	}
	// Recognised before it is converted: a code is not a duration, so there is
	// nothing to divide and the field stays nil.
	if r.lockWaitMs != noLockWait {
		p.LockTimeSeconds = fptr(r.lockWaitMs / msPerSecond)
	}
	return p
}

// fptr returns the address of f, for the DTO's two *float64 fields (R8.3's nullable
// pair): Go will not take the address of a literal, so filling one from an
// expression needs a value with an address.
//
// The store's test files already have this as `ptr` (statements_test.go). This one is
// named `fptr` because a non-test declaration of `ptr` would collide with that test
// file's — same package — and the non-test one is the half every caller can reach,
// including the test files.
func fptr(f float64) *float64 { return &f }

// QuerySeries implements api.QuerySeriesSource: the points for every fingerprint in
// q, one entry each.
func (s *Store) QuerySeries(ctx context.Context, q api.SeriesQuery) ([]dto.QuerySeries, error) {
	rows, err := s.seriesRows(ctx, q)
	if err != nil {
		return nil, err
	}

	// Group the rows under their fingerprints before building the entries, so the
	// entries can be emitted in request order from q.Fingerprints rather than in
	// the order the rows came back. A row whose fingerprint is not in the request
	// (possible only if the store has rows for a fingerprint the caller did not
	// ask for, which the IN clause rules out) is dropped by this shape rather than
	// producing an entry nobody asked for.
	byFP := make(map[string][]dto.QuerySeriesPoint, len(q.Fingerprints))
	for _, r := range rows {
		byFP[r.fingerprint] = append(byFP[r.fingerprint], r.point())
	}

	out := make([]dto.QuerySeries, 0, len(q.Fingerprints))
	for _, fp := range q.Fingerprints {
		pts := byFP[fp]
		if pts == nil {
			// The contract's "empty (not missing) Points for a fingerprint it has
			// never seen", and the difference between this route and the detail
			// route: an unknown fingerprint here is an entry with no points, and
			// an unknown triple there is a 404. One stale row must not fail the
			// other 49.
			pts = []dto.QuerySeriesPoint{}
		}
		out = append(out, dto.QuerySeries{
			Fingerprint: fp,
			Namespace:   q.Namespace,
			Cluster:     q.Cluster,
			Datname:     q.Datname,
			Points:      pts,
		})
	}
	return out, nil
}

// seriesRows runs seriesSQL for one parsed request.
//
// The window is q.Start/q.End and not s.window: parseSeriesQuery has already
// resolved the range preset to a concrete pair ("the window the range resolved to:
// now-duration for a preset, the request's own from/to for range=custom") and its
// doc says a source never re-derives it from a preset name. Re-deriving it here
// would also lose the range=custom case entirely, which is the only way R10.15's
// custom window reaches the store at all.
//
// A window wider than the store's retention is not clamped here. The list route
// clamps because its window is the store's own; this one is the caller's, and the
// honest answer to "give me 30 days" from a 7-day store is the seven days of rows
// that exist — R3.14's answer-not-available-rather-than-zero, satisfied by fewer
// points rather than by an error or by padding.
func (s *Store) seriesRows(ctx context.Context, q api.SeriesQuery) ([]seriesRow, error) {
	if len(q.Fingerprints) == 0 {
		// parseFingerprints rejects an empty list, so this is a caller that built
		// SeriesQuery by hand. Answering empty beats running `IN ()`, which SQLite
		// rejects outright.
		return nil, nil
	}

	w := newWhere()
	w.raw("r.bucket_kind = 'minute'")
	w.raw("r.bucket >= ? AND r.bucket <= ?", unix(q.Start), unix(q.End))
	w.in("r.fingerprint", q.Fingerprints)
	if q.Namespace != "" {
		w.eq("r.namespace", q.Namespace)
	}
	if q.Cluster != "" {
		w.eq("r.cluster", q.Cluster)
	}
	if q.Datname != "" {
		w.eq("r.datname", q.Datname)
	}

	rows, err := s.db.QueryContext(ctx, seriesSQL+w.String()+seriesGroupBy, w.Args()...)
	if err != nil {
		return nil, fmt.Errorf("store: query series: %w", err)
	}
	defer rows.Close()

	var out []seriesRow
	for rows.Next() {
		var r seriesRow
		if err := rows.Scan(
			&r.fingerprint, &r.bucket,
			&r.calls, &r.totalExecMs, &r.rowsReturned, &r.sharedHit, &r.sharedRead,
			&r.ioWaitMs,
			&r.lockWaitMs,
		); err != nil {
			return nil, fmt.Errorf("store: query series scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: query series rows: %w", err)
	}
	return out, nil
}
