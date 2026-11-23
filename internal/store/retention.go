// retention.go is R3.14's retention half: Purge, and the rules for which rows
// expire and which must not.
//
// R3.14:318 is one sentence and it asks for two things: "retain statement
// snapshots for `retention.statementSnapshots` (default 7 d)" and "answer
// week-over-week 'is this query slower than last week' from it". The second is a
// read (the routes that answer it read query_rollup over a window, which is
// topn.go/series.go/detail.go); the first is this file. §3.4:222 adds the second
// retention rule — `retention.maxBytes: 1Gi` with size-based eviction — and that
// is the eviction pass below.
//
// # What expires, and the one table that never does
//
//	query_rollup        expires at RetentionWindow() — this is the history R3.14
//	                  names, and it is the only table that is history in the
//	                  week-over-week sense
//	plan_sample         expires at Config.PlanRetention (retention.plans, 7 d)
//	plan_change         same, because a change older than its plan's retention
//	                  describes a plan the store no longer has
//	sink_current        expires at RetentionWindow() — the collector.Sink rows
//	                  age with the store's own window, the way MemSink's buckets
//	                  age with its ring
//	query               expires only when orphaned (see below)
//	query_source        never expires
//
// query_source is the exception worth arguing, because "delete old rows from every
// table" is the obvious implementation and it is wrong. The table is not history:
// it is R3.3's baseline — one row per (fingerprint, namespace, cluster, pod,
// datname) holding the cumulative counters the *next* snapshot is diffed against.
// Deleting it does not remove old data, it removes the number the next round
// subtracts, and the first symptom is R3.3's false reset: a fingerprint whose
// baseline vanished looks like a counter reset in a round where nothing was reset,
// which is the failure the merge rule (statements.go, R3.4's keying) exists to
// prevent. Its
// captured_at column records when the row was last refreshed; it is not an age.
//
// The query table's rows are the fingerprint registry (R3.4's identity, plus the
// template and display text R3.6 caps). A fingerprint whose rollups aged out keeps
// its row — the registry is not time-partitioned and R3.4's fingerprint is not
// "this week's fingerprint". A row is deleted only when nothing references it
// any more: no query_source, no query_rollup, no plan_sample, no plan_change.
// That is the case the plan tables' ON DELETE CASCADE cannot reach (a cascade
// deletes children when the parent goes, never the reverse), and it is real: a
// statement that stops running leaves its rollups to age out and its source row
// behind, and without this pass the registry only ever grows.
//
// # Why the eviction pass is the only one that runs VACUUM
//
// SQLite's DELETE returns pages to the database's own freelist, not to the
// filesystem: the file keeps the size its high-water mark gave it. So a byte cap
// enforced by measuring the file cannot be satisfied by deleting rows — the
// measurement does not move — and a naive loop over "delete, measure, delete"
// empties the database and still reports the same number.
//
// The expire pass therefore does not VACUUM (a daily rewrite of a 1 GiB file to
// make room the next insert would reuse anyway is cost without a consumer), and
// the eviction pass does, once, because that pass is the one whose contract is
// measured in bytes. The report's BytesAfter is taken after it, so the two numbers
// are the file's real size before and after rather than the same number twice.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// daySeconds is one day in the epoch-second integers the bucket columns hold.
//
// The eviction pass deletes in day-sized steps rather than row-by-row: a 7 d store
// holding one minute per fingerprint per pod is hundreds of thousands of rows, and
// deleting them one at a time to shave a byte cap down would be hundreds of
// thousands of statements for a number the report shows once. A day is also the
// granularity R3.14's own question is asked at.
const daySeconds = 86400

// PurgeReport is what one Purge call measured.
//
// It is a struct rather than an error-and-count because the caller is a maintenance
// loop (and R16.3's `pgsi` subcommand surface, which renders it): the counts are
// the answer to "did retention run", and a report with zeros is a different answer
// from a failed call. Every count is a RowsAffected, not an estimate.
type PurgeReport struct {
	// RollupsDeleted is query_rollup rows past the retention window, plus the
	// eviction pass's rows (counted separately below so an operator can tell
	// "old" from "over budget").
	RollupsDeleted int
	EvictedRollups int
	PlansDeleted   int
	ChangesDeleted int
	SinkDeleted    int
	QueriesDeleted int

	// BytesBefore and BytesAfter are the SQLite file's size (os.Stat) around the
	// call. Both are zero for the §3.4:219 memory profile, which has no file —
	// the profile's size is bounded by R13.5's --max-store-bytes inside
	// collector.MemSink, not here (store.go's "Two size ceilings" note is why the
	// two numbers are not merged).
	//
	// The measurement is the database file only. A WAL-mode store also has a
	// <path>-wal file that os.Stat on the path does not include, so the number
	// under-counts by whatever has not checkpointed; the PVC's own accounting
	// counts it. Naming the gap is cheaper than inventing a second number.
	BytesBefore int64
	BytesAfter  int64

	// Window is the retention window the expire pass used, so a report from a
	// week ago can be read without knowing what the store was configured with.
	Window time.Duration
}

// Purge deletes what R3.4's retention set says has expired, and enforces
// retention.maxBytes if the file is over it.
//
// It is safe to call on any schedule and twice in a row: every delete is a
// predicate on a stored timestamp, so the second call finds nothing and reports
// zeros. There is no in-memory "last purged" marker to keep in sync, which is the
// same reason the schema has no metadata table for it.
func (s *Store) Purge(ctx context.Context) (PurgeReport, error) {
	var report PurgeReport
	report.Window = s.RetentionWindow()

	// The window's start, in the same integer the bucket columns store. Every
	// expiry predicate below compares a stored timestamp to this one, so the
	// report's Window and the rows it removed cannot disagree about the clock:
	// both come from s.window, which is s.cfg.Now for an unset pair.
	from, _ := s.window(time.Time{}, time.Time{})
	cutoff := unix(from)

	// The file's size before anything is removed. Taken first because the
	// eviction pass needs the pre-change number to decide whether to run at all,
	// and because after a VACUUM the old number no longer exists anywhere.
	before, err := s.fileBytes()
	if err != nil {
		return report, err
	}
	report.BytesBefore = before

	// 1. The history: rollup minutes older than the window. This is the pass
	//    R3.14's sentence is about; everything below it is what hangs off it.
	n, err := s.db.ExecContext(ctx,
		`DELETE FROM query_rollup WHERE bucket_kind = 'minute' AND bucket < ?`, cutoff)
	if err != nil {
		return report, fmt.Errorf("store: purge rollups: %w", err)
	}
	report.RollupsDeleted = rowsAffected(n)

	// 2. The plans, at their own retention (retention.plans is a separate knob
	//    from statementSnapshots in §3.4:222, so it gets its own cutoff rather
	//    than borrowing the window above).
	planCutoff := unix(s.now().Add(-s.cfg.PlanRetention))
	n, err = s.db.ExecContext(ctx,
		`DELETE FROM plan_sample WHERE captured_at < ?`, planCutoff)
	if err != nil {
		return report, fmt.Errorf("store: purge plan samples: %w", err)
	}
	report.PlansDeleted = rowsAffected(n)

	// plan_change has no retention of its own in §3.4:222. It follows
	// retention.plans because a change row names two plan hashes, and a change
	// whose plans have both expired is a fact about data the store no longer has.
	n, err = s.db.ExecContext(ctx,
		`DELETE FROM plan_change WHERE detected_at < ?`, planCutoff)
	if err != nil {
		return report, fmt.Errorf("store: purge plan changes: %w", err)
	}
	report.ChangesDeleted = rowsAffected(n)

	// 3. The collector.Sink rows, at the store's window. MemSink keeps its own
	//    rows until R13.5's ceiling evicts them; the SQLite half ages them here so
	//    a 7 d store does not serve a 30 d-old sample set from a collector that
	//    stopped running.
	n, err = s.db.ExecContext(ctx,
		`DELETE FROM sink_current WHERE ingested_at < ?`, cutoff)
	if err != nil {
		return report, fmt.Errorf("store: purge sink rows: %w", err)
	}
	report.SinkDeleted = rowsAffected(n)

	// 4. Orphaned fingerprints. See the file doc for why this is the reverse of
	//    what ON DELETE CASCADE does, and why query_source is not purged.
	//
	// The last_seen guard is not redundant with the three NOT EXISTS clauses: a
	// fingerprint inserted this round has a query_source row in the same
	// transaction, so it is already excluded, but a store whose write path changes
	// (a fingerprint registered before its first snapshot lands) would otherwise
	// lose the registry row and with it the template R3.6 capped. Requiring the
	// row to be older than the window makes the pass delete only fingerprints the
	// store has stopped seeing.
	n, err = s.db.ExecContext(ctx, `
		DELETE FROM query
		WHERE last_seen < ?
		  AND fingerprint NOT IN (SELECT fingerprint FROM query_source)
		  AND fingerprint NOT IN (SELECT fingerprint FROM query_rollup)
		  AND fingerprint NOT IN (SELECT fingerprint FROM plan_sample)
		  AND fingerprint NOT IN (SELECT fingerprint FROM plan_change)`, cutoff)
	if err != nil {
		return report, fmt.Errorf("store: purge orphan queries: %w", err)
	}
	report.QueriesDeleted = rowsAffected(n)

	// 5. The byte cap. Only a file store has one to enforce (the memory profile's
	//    ceiling is A1's, in collector.MemSink), and only an over-capacity store
	//    pays for the VACUUM that makes the cap enforceable at all.
	if !s.IsMemory() && before > s.cfg.MaxBytes {
		evicted, after, err := s.evict(ctx, s.cfg.MaxBytes)
		if err != nil {
			return report, err
		}
		report.EvictedRollups = evicted
		report.BytesAfter = after
		return report, nil
	}

	// Not over the cap: the file's size is unchanged by the deletes (the freelist
	// argument in the file doc), so BytesAfter is the same measurement taken
	// again. Re-statting rather than copying the field keeps the two numbers
	// honest if something outside the process grew the file mid-call.
	after, err := s.fileBytes()
	if err != nil {
		return report, err
	}
	report.BytesAfter = after
	return report, nil
}

// evict removes the oldest rollup days until the file fits under maxBytes, and
// returns how many rows went and what the file measures afterwards.
//
// The loop deletes a day, VACUUMs, and measures — in that order and together,
// because the measurement only moves when the VACUUM runs (a DELETE alone leaves
// the file at its high-water mark). That makes the eviction path cost O(days over
// budget) file rewrites, which is the honest price of a byte cap on SQLite: the
// alternative is to stop enforcing retention.maxBytes and let the PVC be the only
// cap, which is what §3.4:222's number is describing in the first place.
//
// It stops with the newest day still present. Evicting the current minute to
// satisfy a cap would empty the store to protect a number, and R3.14's
// week-over-week question needs *some* history to have an answer; a store that
// keeps its newest day and reports the shortfall is the lesser failure.
func (s *Store) evict(ctx context.Context, maxBytes int64) (int, int64, error) {
	var evicted int
	for {
		// Both ends in one query. MIN alone is not enough: the loop has to know
		// whether anything newer than the day it is about to delete exists, and
		// a DELETE of `bucket < min + 1 day` on a store whose rows all fall in one
		// day empties it — which is the failure this function is supposed to stop
		// short of, and the rowsAffected==0 guard cannot catch it because that
		// delete does affect rows.
		var minb, maxb sql.NullInt64
		err := s.db.QueryRowContext(ctx,
			`SELECT MIN(bucket), MAX(bucket) FROM query_rollup WHERE bucket_kind = 'minute'`).
			Scan(&minb, &maxb)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return evicted, 0, fmt.Errorf("store: evict bucket range: %w", err)
		}
		// !Valid is the empty-table case (MIN/MAX of no rows is NULL).
		if !minb.Valid || !maxb.Valid {
			break
		}
		// Everything still in the table fits in one day: that day is the store's
		// remaining history and it stays, cap or no cap.
		if maxb.Int64-minb.Int64 < daySeconds {
			break
		}

		n, err := s.db.ExecContext(ctx,
			`DELETE FROM query_rollup WHERE bucket_kind = 'minute' AND bucket < ?`,
			minb.Int64+daySeconds)
		if err != nil {
			return evicted, 0, fmt.Errorf("store: evict day %d: %w", minb.Int64, err)
		}
		evicted += rowsAffected(n)

		if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
			return evicted, 0, fmt.Errorf("store: vacuum after evict: %w", err)
		}
		size, err := s.fileBytes()
		if err != nil {
			return evicted, 0, err
		}
		if size <= maxBytes {
			return evicted, size, nil
		}
	}

	size, err := s.fileBytes()
	if err != nil {
		return evicted, 0, err
	}
	return evicted, size, nil
}

// fileBytes is the SQLite file's size, or zero for the memory profile.
//
// os.Stat rather than `PRAGMA page_count * page_size`: the pragma pair answers the
// same question for the database proper and nothing for the -wal file, and a stat
// is one syscall with no driver round-trip. Both under-count a WAL-mode store by
// the un-checkpointed WAL, which is documented on PurgeReport.
func (s *Store) fileBytes() (int64, error) {
	if s.IsMemory() {
		return 0, nil
	}
	info, err := os.Stat(s.cfg.Path)
	if err != nil {
		return 0, fmt.Errorf("store: stat %s: %w", s.cfg.Path, err)
	}
	return info.Size(), nil
}

// rowsAffected turns a driver count into the report's int, with 0 for a driver
// that declines to report one (modernc always reports it; the guard is so a future
// driver that returns -1 does not put a negative in a count field).
func rowsAffected(res sql.Result) int {
	if res == nil {
		return 0
	}
	n, err := res.RowsAffected()
	if err != nil || n < 0 {
		return 0
	}
	return int(n)
}
