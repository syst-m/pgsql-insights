// retention_test.go is A3's S29 test file for retention.go: R3.14's retention
// half and §3.4:222's byte cap. The tests follow the file's own order — the
// rollup history at the window, the plans at their own retention, the sink rows,
// the orphan pass (A3's S46), the eviction loop — plus the two helpers at the
// bottom (fileBytes' memory-profile zero and rowsAffected' missing-count guard).
package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/query"
)

// fpOfSQL is the fingerprint the store registers for a statement's text: the same
// call PutSnapshot makes (statements.go's query.Normalize(r.SQL, s.cfg.Normalizer),
// whose default Options openMem applies). The fixture needs it because the
// fingerprint a snapshot produces is not a fake prefix — fpNamed("a1") is a
// fingerprint, but it is not the one "SELECT a FROM keep" hashes to, and the
// orphan pass's rows are keyed on the real ones.
func fpOfSQL(sql string) string {
	return query.Normalize(sql, query.Options{Length: query.DefaultQueryStringLength}).Fingerprint
}

// openFileAt opens a file-profile store with a fixed clock and an explicit byte
// cap, for the tests that need a file: the eviction pass and the byte counts are
// both gated on !IsMemory, so the in-memory openMem cannot reach them.
func openFileAt(t *testing.T, now time.Time, maxBytes int64) *Store {
	t.Helper()
	s, err := Open(Config{
		Path:     filepath.Join(t.TempDir(), "pgsi.db"),
		Now:      func() time.Time { return now },
		MaxBytes: maxBytes,
	})
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedRollups writes the fixture's rollup rows into an existing store — the same
// INSERT newSeriesStore uses, factored out for the tests that need a file store
// rather than the in-memory one newSeriesStore opens.
func seedRollups(t *testing.T, s *Store, rows ...seriesSeed) {
	t.Helper()
	for _, r := range rows {
		_, err := s.DB().Exec(seriesRollupInsert,
			r.fp, r.namespace, r.cluster, r.pod, r.datname, r.usename, r.kind, r.bucket,
			r.calls, r.totalMs, r.rows, r.hit, r.read, r.lockMs,
		)
		if err != nil {
			t.Fatalf("seed rollup %s/%d: %v", r.fp[:2], r.bucket, err)
		}
	}
}

// itoa renders a bucket for an assertion message. The bucket columns are int64 and
// the package has no formatter for them (collector's itoa takes an int and is in
// another package), so the test has one instead of repeating a strconv call at
// every expectation.
func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// survivingRollups is every query_rollup row left in the store, as
// "fingerprint|bucket_kind|bucket" strings in sorted order — the shape that lets
// one assertion name both the rows that expired and the one that must not have.
func survivingRollups(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB().Query(`SELECT fingerprint, bucket_kind, bucket FROM query_rollup
		ORDER BY fingerprint, bucket_kind, bucket`)
	if err != nil {
		t.Fatalf("read surviving rollups: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fp, kind string
		var bucket int64
		if err := rows.Scan(&fp, &kind, &bucket); err != nil {
			t.Fatalf("scan surviving rollups: %v", err)
		}
		out = append(out, fp+"|"+kind+"|"+itoa(bucket))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate surviving rollups: %v", err)
	}
	return out
}

// TestPurgeEmptyStore is the floor: a store with nothing in it purges without
// error and reports zeros, and the report's Window is the store's own so a report
// can be read without knowing what the process was configured with.
func TestPurgeEmptyStore(t *testing.T) {
	s := openMem(t, seriesNow)
	report, err := s.Purge(context.Background())
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if report.Window != MemoryWindow {
		t.Errorf("Window = %v, want %v (the memory profile's §3.4:219 window)", report.Window, MemoryWindow)
	}
	// Every count zero. A report with zeros is a different answer from a failed
	// call, which is why PurgeReport is a struct and not an error-and-count.
	for _, f := range []struct {
		name string
		got  int
	}{
		{"RollupsDeleted", report.RollupsDeleted},
		{"EvictedRollups", report.EvictedRollups},
		{"PlansDeleted", report.PlansDeleted},
		{"ChangesDeleted", report.ChangesDeleted},
		{"SinkDeleted", report.SinkDeleted},
		{"QueriesDeleted", report.QueriesDeleted},
	} {
		if f.got != 0 {
			t.Errorf("%s = %d, want 0 on an empty store", f.name, f.got)
		}
	}
	// The §3.4:219 profile has no file, so both byte fields are zero rather than
	// a size invented for a database that does not have one.
	if report.BytesBefore != 0 || report.BytesAfter != 0 {
		t.Errorf("BytesBefore/After = %d/%d, want 0/0 for the memory profile",
			report.BytesBefore, report.BytesAfter)
	}
}

// TestPurgeRollupExpiry is the pass R3.14's sentence is about: the minute
// rollups older than the window go, and everything else in the table stays.
func TestPurgeRollupExpiry(t *testing.T) {
	cut := minuteBucket(seriesNow.Add(-24 * time.Hour))
	fresh := minuteBucket(seriesNow.Add(-time.Hour))
	far := minuteBucket(seriesNow.Add(-30 * 24 * time.Hour))
	fpA, fpB := fpNamed("aa"), fpNamed("bb")

	s := newSeriesStore(t,
		seed(fpA, fresh, 1, 10, 10, 1, 1, 0),           // inside the window
		seed(fpA, cut, 1, 10, 10, 1, 1, 0),             // exactly the cutoff: `bucket < cutoff` is false
		seed(fpB, far, 1, 10, 10, 1, 1, 0),             // outside the window
		seedKind(fpB, "hour", far, 1, 10, 10, 1, 1, 0), // outside, but not a minute row
	)
	report, err := s.Purge(context.Background())
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if report.RollupsDeleted != 1 {
		t.Errorf("RollupsDeleted = %d, want 1", report.RollupsDeleted)
	}
	got := survivingRollups(t, s)
	// In the order the read returns them: by fingerprint, then bucket_kind, then
	// bucket ascending — so fpA's 24 h-old minute precedes its 1 h-old one.
	wantRows := []string{
		fpA + "|minute|" + itoa(cut),
		fpA + "|minute|" + itoa(fresh),
		fpB + "|hour|" + itoa(far),
	}
	if !equalStrings(got, wantRows) {
		t.Errorf("surviving rollups:\n got %v\nwant %v", got, wantRows)
	}
}

// TestPurgePlanRetentionIsItsOwnCutoff covers §3.4:222's second retention knob.
// retention.plans is a separate value from retention.statementSnapshots, so the
// plans get their own cutoff rather than borrowing the window — and on a memory
// store the two differ by six days (24 h vs 7 d), which is what makes the
// assertion in this test mean "the plan pass used its own number".
func TestPurgePlanRetentionIsItsOwnCutoff(t *testing.T) {
	ctx := context.Background()
	epoch := seriesNow.Add(-720 * time.Hour)
	eight := seriesNow.Add(-8 * 24 * time.Hour)
	three := seriesNow.Add(-3 * 24 * time.Hour)

	// fp1 has two rounds: one 8 days ago (its plan sample is past retention.plans)
	// and one 3 days ago (its plan sample is inside it, while its rollup minute is
	// outside the 24 h window). fp2 is entirely 8 and 10 days old, so everything
	// it ever wrote is past both cutoffs.
	s := openMem(t, seriesNow)
	rows := []struct {
		src      QuerySource
		queryid  string
		sqlText  string
		at       time.Time
		calls    int64
		totalMs  float64
		planHash string
	}{
		{sourceIn("appdb"), "11", topnSQL, eight, 4, 40, "aaa1"},
		{sourceIn("appdb"), "11", topnSQL, three, 14, 140, "bbb1"},
		{sourceIn("ledger"), "22", "SELECT a FROM ledger", seriesNow.Add(-10 * 24 * time.Hour), 4, 40, "aaa2"},
		{sourceIn("ledger"), "22", "SELECT a FROM ledger", eight, 14, 140, "bbb2"},
	}
	for _, r := range rows {
		if err := s.PutSnapshot(ctx, snapFrom(r.src, r.at, epoch,
			rowWithPlan(r.queryid, r.sqlText, "appuser", r.calls, r.totalMs, r.at, r.planHash))); err != nil {
			t.Fatalf("PutSnapshot(%s at %s): %v", r.queryid, r.at.Format("2006-01-02"), err)
		}
	}

	report, err := s.Purge(ctx)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	// The rollups: fp1's 3-day-old minute and fp2's 8-day-old minute are both past
	// the 24 h window. (fp1's and fp2's first rounds wrote no rollup at all —
	// statements_test.go's TestPutSnapshotFirstRound — so there are exactly two.)
	if report.RollupsDeleted != 2 {
		t.Errorf("RollupsDeleted = %d, want 2", report.RollupsDeleted)
	}
	// The plans, at their own cutoff: fp1's 8-day-old sample goes and its
	// 3-day-old one stays, even though the rollup written in the same round as the
	// 3-day-old sample did go. That asymmetry is the whole test — a plan pass that
	// borrowed the window would delete both, and a plan pass that borrowed the
	// plans' retention would keep fp1's rollup too.
	if report.PlansDeleted != 3 {
		t.Errorf("PlansDeleted = %d, want 3 (fp1's 8 d old; fp2's 8 d and 10 d old)", report.PlansDeleted)
	}
	// fp1's change was detected 3 days ago, inside retention.plans, so it stays;
	// fp2's was detected 8 days ago, so it goes.
	if report.ChangesDeleted != 1 {
		t.Errorf("ChangesDeleted = %d, want 1 (fp2's 8 d old change; fp1's 3 d old one stays)",
			report.ChangesDeleted)
	}
	// fp1 survives the orphan pass on the strength of that surviving plan sample.
	if got := countRows(t, s, "query"); got != 1 {
		t.Errorf("query rows = %d, want 1 (fp1 keeps its registry row; fp2's is orphaned)", got)
	}
}

// TestPurgeOrphanQueries is A3's S46. The pass deletes a fingerprint only when
// nothing references it, and the fix was that the query_source reference is
// windowed: unwindowed it was a tautology (PutSnapshot writes a source row in the
// same transaction that registers the fingerprint and nothing ever deletes those
// rows), so every fingerprint had a source row, the NOT IN was false for all of
// them, and the pass never deleted anything.
func TestPurgeOrphanQueries(t *testing.T) {
	ctx := context.Background()
	epoch := seriesNow.Add(-720 * time.Hour)
	fresh := seriesNow.Add(-time.Hour)
	three := seriesNow.Add(-3 * 24 * time.Hour)
	eight := seriesNow.Add(-8 * 24 * time.Hour)

	// The three statements the write path registers. Their fingerprints are the
	// store's own (fpOfSQL): the pass's rows are keyed on the fingerprint the
	// normalizer computed, not on a fake prefix, and a test that guessed the
	// spelling would be asserting against a row the store never wrote.
	keepSQL, goneSQL, planSQL := "SELECT a FROM keep", "SELECT a FROM gone", "SELECT a FROM plan"
	fpA := fpOfSQL(keepSQL) // fresh snapshot: last_seen inside the window
	fpB := fpOfSQL(goneSQL) // one snapshot 8 d ago: every reference is outside the window
	fpC := fpOfSQL(planSQL) // one snapshot 3 d ago with a plan: the plan keeps it alive
	fpD := fpNamed("d4")    // direct row, last_seen outside the window, no references
	fpE := fpNamed("e5")    // direct row, last_seen inside the window, no references

	s := openMem(t, seriesNow)
	// Each is a first round from its own source, so each writes a query row and a
	// query_source row and no rollup (statements_test.go's first-round rule) —
	// which is what isolates the orphan pass's four NOT IN clauses from the rollup
	// table.
	for _, r := range []struct {
		queryid, sqlText string
		at               time.Time
		plan             string
	}{
		{"11", keepSQL, fresh, ""},
		{"22", goneSQL, eight, ""},
		{"33", planSQL, three, "ccc3"},
	} {
		src := testSource()
		src.Datname = "appdb"
		var row StatementRow
		if r.plan == "" {
			row = rowFor(r.queryid, r.sqlText, "appuser", 4, 40)
		} else {
			row = rowWithPlan(r.queryid, r.sqlText, "appuser", 4, 40, r.at, r.plan)
		}
		if err := s.PutSnapshot(ctx, snapFrom(src, r.at, epoch, row)); err != nil {
			t.Fatalf("PutSnapshot(%s): %v", r.queryid, err)
		}
	}
	// The two fingerprints inserted directly, which is the only way to separate the
	// pass's last_seen guard from its four NOT IN clauses: a row can be old with no
	// references (deleted) or fresh with no references (kept).
	for _, r := range []struct {
		fp       string
		lastSeen int64
	}{{fpD, minuteBucket(eight)}, {fpE, minuteBucket(fresh)}} {
		if _, err := s.DB().Exec(upsertQuerySQL,
			r.fp, topnSQL, topnSQL, topnSQL, 0, len(topnSQL), r.lastSeen, r.lastSeen); err != nil {
			t.Fatalf("insert query %s: %v", r.fp[:2], err)
		}
	}

	before := queryFingerprints(t, s)
	if len(before) != 5 {
		t.Fatalf("fixture has %d query rows, want 5: %v", len(before), before)
	}

	report, err := s.Purge(ctx)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if report.QueriesDeleted != 2 {
		t.Errorf("QueriesDeleted = %d, want 2 (fpB: every reference outside the window — "+
			"the case the unwindowed NOT IN never deleted; fpD: old with no references)",
			report.QueriesDeleted)
	}
	// Every row by name, on both sides of the pass. With five rows and two
	// deleted, which two went is the finding, and a survivor list alone cannot say
	// it: it shows three names without saying which of the five they were chosen from.
	for _, r := range []struct {
		fp      string
		present bool
	}{
		{fpA, true},  // last_seen inside the window, so the guard keeps it
		{fpB, false}, // A3's S46: every reference is outside the window
		{fpC, true},  // a surviving plan_sample references it
		{fpD, false}, // old and unreferenced
		{fpE, true},  // fresh and unreferenced, so the guard keeps it
	} {
		if got := hasFingerprint(t, s, r.fp); got != r.present {
			t.Errorf("fingerprint %s present after Purge = %v, want %v", r.fp[:2], got, r.present)
		}
	}
}

// hasFingerprint asks the registry for one fingerprint by name.
func hasFingerprint(t *testing.T, s *Store, fp string) bool {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM query WHERE fingerprint = ?`, fp).Scan(&n); err != nil {
		t.Fatalf("count fingerprint %s: %v", fp[:2], err)
	}
	return n > 0
}

// queryFingerprints is every fingerprint left in the registry, sorted.
func queryFingerprints(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB().Query(`SELECT fingerprint FROM query ORDER BY fingerprint`)
	if err != nil {
		t.Fatalf("read fingerprints: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			t.Fatalf("scan fingerprint: %v", err)
		}
		out = append(out, fp)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fingerprints: %v", err)
	}
	return out
}

// TestPurgeSinkRows ages the collector.Sink rows with the store's own window, the
// way MemSink's buckets age with its ring — so a store does not serve a 30-day-old
// sample set from a collector that stopped running.
func TestPurgeSinkRows(t *testing.T) {
	ctx := context.Background()
	s := openMem(t, seriesNow)
	// A current row, written through the sink itself.
	if err := s.Publish(sinkTarget, "pg_stat_statements", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// A two-day-old row, written directly because Publish stamps the store's now
	// and the store's now is fixed.
	if _, err := s.DB().Exec(upsertSinkSQL, "prod", "pg", "pg-2", "pg_locks",
		[]byte("payload"), unix(seriesNow.Add(-48*time.Hour))); err != nil {
		t.Fatalf("insert the old sink row: %v", err)
	}

	report, err := s.Purge(ctx)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if report.SinkDeleted != 1 {
		t.Errorf("SinkDeleted = %d, want 1 (the 48 h old row; the current one stays)", report.SinkDeleted)
	}
	if n := countRows(t, s, "sink_current"); n != 1 {
		t.Fatalf("%d sink rows left, want 1", n)
	}
	if got := s.Samples(sinkTarget, "pg_stat_statements"); got == nil {
		t.Error("the current sink row was deleted — the purge kept the wrong one")
	}
}

// TestPurgeIsIdempotent is the file's safety claim: "safe to call on any schedule
// and twice in a row: every delete is a predicate on a stored timestamp, so the
// second call finds nothing and reports zeros. There is no in-memory last-purged
// marker to keep in sync."
func TestPurgeIsIdempotent(t *testing.T) {
	s := newSeriesStore(t,
		seed(fpNamed("aa"), minuteBucket(seriesNow.Add(-time.Hour)), 1, 10, 10, 1, 1, 0),
		seed(fpNamed("bb"), minuteBucket(seriesNow.Add(-30*24*time.Hour)), 1, 10, 10, 1, 1, 0),
	)
	first, err := s.Purge(context.Background())
	if err != nil {
		t.Fatalf("first Purge: %v", err)
	}
	if first.RollupsDeleted != 1 {
		t.Fatalf("first Purge deleted %d rollups, want 1 (the fixture needs an expired row)", first.RollupsDeleted)
	}

	second, err := s.Purge(context.Background())
	if err != nil {
		t.Fatalf("second Purge: %v", err)
	}
	if second.RollupsDeleted != 0 || second.QueriesDeleted != 0 || second.PlansDeleted != 0 ||
		second.ChangesDeleted != 0 || second.SinkDeleted != 0 || second.EvictedRollups != 0 {
		t.Errorf("second Purge = %+v, want all zeros (the first pass left nothing expired)", second)
	}
	if second.Window != first.Window {
		t.Errorf("second Purge Window = %v, want %v (the same window, from the same config)",
			second.Window, first.Window)
	}
}

// TestEvictKeepsTheNewestDay covers the eviction loop's stop rule. The loop
// deletes day-sized steps and VACUUMs, and it stops with the newest day still
// present: "evicting the current minute to satisfy a cap would empty the store to
// protect a number".
func TestEvictKeepsTheNewestDay(t *testing.T) {
	ctx := context.Background()
	fp := fpNamed("aa")
	d0 := minuteBucket(seriesNow)
	d1 := minuteBucket(seriesNow.Add(-24 * time.Hour))
	d2 := minuteBucket(seriesNow.Add(-48 * time.Hour))

	s := openFileAt(t, seriesNow, DefaultMaxFileBytes)
	seedRollups(t, s,
		seed(fp, d0, 1, 10, 10, 1, 1, 0),
		seed(fp, d1, 1, 10, 10, 1, 1, 0),
		seed(fp, d2, 1, 10, 10, 1, 1, 0),
	)

	// A cap of 1 byte can never be satisfied, so the loop runs to its stop rule
	// rather than to its success condition — which is the only way to test the stop
	// rule without engineering a file to a size.
	evicted, size, err := s.evict(ctx, 1)
	if err != nil {
		t.Fatalf("evict: %v", err)
	}
	if evicted != 2 {
		t.Errorf("evicted %d rows, want 2 (the two older days)", evicted)
	}
	if want := []string{fp + "|minute|" + itoa(d0)}; !equalStrings(survivingRollups(t, s), want) {
		t.Errorf("surviving rollups:\n got %v\nwant %v (the newest day is the store's remaining history)",
			survivingRollups(t, s), want)
	}
	if size <= 0 {
		t.Errorf("evict reported a file size of %d, want the real size of a file store", size)
	}

	// The other stop rule: everything already fits in one day, so there is nothing
	// to evict and the loop breaks before its first DELETE. The rowsAffected==0
	// guard cannot catch this case — `DELETE ... WHERE bucket < min + 1 day` on a
	// one-day store does affect rows — so the break is on the bucket range.
	one := openFileAt(t, seriesNow, DefaultMaxFileBytes)
	seedRollups(t, one,
		seed(fp, d0, 1, 10, 10, 1, 1, 0),
		seed(fp, minuteBucket(seriesNow.Add(-30*time.Minute)), 1, 10, 10, 1, 1, 0),
	)
	evicted, size, err = one.evict(ctx, 1)
	if err != nil {
		t.Fatalf("evict (one day): %v", err)
	}
	if evicted != 0 {
		t.Errorf("evicted %d rows from a one-day store, want 0 (the newest day stays, cap or no cap)", evicted)
	}
	if n := countRows(t, one, "query_rollup"); n != 2 {
		t.Errorf("%d rollup rows left, want 2 (untouched)", n)
	}
	if size <= 0 {
		t.Errorf("evict reported a file size of %d, want the real size of a file store", size)
	}
}

// TestPurgeFileStoreBytes covers the report's two byte fields on the profile that
// has a file. The memory profile reports 0/0 (TestPurgeEmptyStore); a file store
// reports the real os.Stat size on both sides, and the doc's argument is that the
// expire pass does not VACUUM, so the two numbers are the same measurement rather
// than a number that moved.
func TestPurgeFileStoreBytes(t *testing.T) {
	s := openFileAt(t, seriesNow, DefaultMaxFileBytes)
	// The file profile's window is DefaultStatementRetention (7 d, §3.4:222), not
	// the memory profile's 24 h, so the fixture needs a row past 7 d to delete and
	// one inside it to keep — a row that the memory tests would have deleted.
	fp := fpNamed("aa")
	expired := minuteBucket(seriesNow.Add(-8 * 24 * time.Hour))
	fresh := minuteBucket(seriesNow.Add(-time.Hour))
	seedRollups(t, s,
		seed(fp, expired, 1, 10, 10, 1, 1, 0),
		seed(fp, fresh, 1, 10, 10, 1, 1, 0),
	)

	report, err := s.Purge(context.Background())
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if report.Window != DefaultStatementRetention {
		t.Errorf("Window = %v, want %v (the file profile's §3.4:222 retention)",
			report.Window, DefaultStatementRetention)
	}
	if report.RollupsDeleted != 1 {
		t.Errorf("RollupsDeleted = %d, want 1 (the 8 d old row; the 1 h old one is inside 7 d)",
			report.RollupsDeleted)
	}
	if want := []string{fp + "|minute|" + itoa(fresh)}; !equalStrings(survivingRollups(t, s), want) {
		t.Errorf("surviving rollups:\n got %v\nwant %v", survivingRollups(t, s), want)
	}
	if report.BytesBefore <= 0 || report.BytesAfter <= 0 {
		t.Fatalf("BytesBefore/After = %d/%d, want a real size on both sides for a file store",
			report.BytesBefore, report.BytesAfter)
	}
	// Under the cap, so no eviction ran and the file is unchanged by the deletes:
	// SQLite's DELETE returns pages to the database's freelist, not to the
	// filesystem, so the file keeps the size its high-water mark gave it.
	if report.EvictedRollups != 0 {
		t.Errorf("EvictedRollups = %d, want 0 (the store is under its cap)", report.EvictedRollups)
	}
	if report.BytesAfter != report.BytesBefore {
		t.Errorf("BytesAfter = %d, BytesBefore = %d — the expire pass does not VACUUM, "+
			"so deleting rows must not shrink the file", report.BytesAfter, report.BytesBefore)
	}
}

// TestRowsAffected covers the count helper's guard. modernc always reports a
// count; the guard is so a driver that returns -1 does not put a negative in a
// report field, and a nil Result (a driver that returns no result and no error)
// reports 0 rather than panicking.
func TestRowsAffected(t *testing.T) {
	if got := rowsAffected(nil); got != 0 {
		t.Errorf("rowsAffected(nil) = %d, want 0", got)
	}
	s := openMem(t, seriesNow)
	res, err := s.DB().Exec(`INSERT INTO query (fingerprint, template, display_text,
		first_seen_text, truncated, text_bytes, first_seen, last_seen)
		VALUES (?, ?, ?, ?, 0, 1, ?, ?)`, fpNamed("aa"), topnSQL, topnSQL, topnSQL,
		minuteBucket(seriesNow), minuteBucket(seriesNow))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := rowsAffected(res); got != 1 {
		t.Errorf("rowsAffected = %d, want 1 for a single insert", got)
	}
}
