package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/collector"
)

// The fixture's source: one pod of one cluster, one database. Every snapshot in
// this file is from the same source so the (fingerprint, usename) keys line up
// across rounds — which is the whole subject, since R3.3's rules are about what
// happens when a second round arrives for a source the store already has.
func testSource() QuerySource {
	return QuerySource{
		Target: collector.Target{
			Namespace: "prod",
			Cluster:   "pg",
			Pod:       "pg-1",
			Role:      "primary",
			Address:   "10.0.0.1:5432",
			Source:    collector.SourceSQL,
			PGMajor:   16,
		},
		Datname: "appdb",
	}
}

// sourceIn is the fixture source in another database. One Snapshot carries one
// Source, so a store that has seen a fingerprint in two databases is two snapshots
// — which is the shape the plan tables' PK is written for.
func sourceIn(db string) QuerySource {
	s := testSource()
	s.Datname = db
	return s
}

// snap builds a snapshot from the fixture source (prod/pg/pg-1, appdb).
func snap(at, reset time.Time, rows ...StatementRow) Snapshot {
	return snapFrom(testSource(), at, reset, rows...)
}

// snapFrom is snap with the source named, for the tests that need more than one
// source in one store.
func snapFrom(src QuerySource, at, reset time.Time, rows ...StatementRow) Snapshot {
	return Snapshot{
		Source:     src,
		CapturedAt: at,
		IngestedAt: at.Add(2 * time.Second),
		StatsReset: reset,
		Rows:       rows,
	}
}

// row builds a StatementRow as appuser, with the fields every test needs and none
// it doesn't. rows_returned is calls×10 so a calls delta and a rows delta can be
// checked against the same fixture number.
func row(queryid, sql string, calls int64, totalMs float64) StatementRow {
	return rowFor(queryid, sql, "appuser", calls, totalMs)
}

// rowFor is row with the usename named. It is the third member of the row
// identity and the only one a single-source snapshot can vary per row, so a test
// that wants two rows for one fingerprint varies it.
func rowFor(queryid, sql, usename string, calls int64, totalMs float64) StatementRow {
	return StatementRow{
		QueryID:         queryid,
		SQL:             sql,
		Usename:         usename,
		Calls:           calls,
		TotalExecTimeMs: totalMs,
		RowsReturned:    float64(calls) * 10,
	}
}

// countRows is SELECT COUNT(*) for one table. Every assertion in this file is
// ultimately a row count or a column read, so it is written once.
func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// rollupTuple is the part of a query_rollup row a test asserts on: the identity
// it was written under and the two numbers R3.3 is about.
type rollupTuple struct {
	fingerprint string
	bucket      int64
	calls       int64
	totalMs     float64
}

// rollupRows reads the whole rollup table. The fingerprints come from the store
// rather than from the test's own computation so the assertion is about what was
// stored, not about whether two pieces of code agree on a hash.
func rollupRows(t *testing.T, s *Store) []rollupTuple {
	t.Helper()
	rows, err := s.DB().Query(`SELECT fingerprint, bucket, calls, total_exec_ms FROM query_rollup`)
	if err != nil {
		t.Fatalf("query rollups: %v", err)
	}
	defer rows.Close()

	var out []rollupTuple
	for rows.Next() {
		var r rollupTuple
		if err := rows.Scan(&r.fingerprint, &r.bucket, &r.calls, &r.totalMs); err != nil {
			t.Fatalf("scan rollup: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rollup rows: %v", err)
	}
	return out
}

// TestPutSnapshotFirstRound pins R3.3's first rule: a source the store has never
// seen produces registry rows and no rollup rows.
//
// The rollup is a *delta* table, so the first round has nothing to subtract and
// writing a row anyway would assert "this minute ran 100 ms" from a counter that
// has been accumulating since the last reset — the number pg_stat_statements
// reports is cumulative, and R3.3's whole framing is that only the difference
// between two snapshots is a per-minute measurement.
func TestPutSnapshotFirstRound(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	err := s.PutSnapshot(ctx, snap(now.Add(-time.Hour), now.Add(-720*time.Hour),
		row("4571984512", "SELECT * FROM orders WHERE id = 42", 10, 100),
		row("8123004477", "UPDATE accounts SET balance = 10 WHERE id = 7", 5, 50),
	))
	if err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}

	if got := countRows(t, s, "query"); got != 2 {
		t.Errorf("query rows = %d, want 2 (two distinct statements)", got)
	}
	if got := countRows(t, s, "query_source"); got != 2 {
		t.Errorf("query_source rows = %d, want 2", got)
	}
	if got := countRows(t, s, "query_rollup"); got != 0 {
		t.Errorf("query_rollup rows = %d, want 0 on a first round (no baseline to diff against)", got)
	}
}

// TestPutSnapshotCounterReset is the test requirements.md:669 names as a Phase 3
// exit criterion ("counter-reset test passes"), and it pins R3.3:307's second
// rule: "a reset must reset the baseline rather than produce a negative or
// wrapped rate".
//
// The fixture is the shape a reset actually produces: the epoch moves from R1 to
// R2 and every counter drops to a smaller value, because pg_stat_statements
// started over. Subtracting the new total from the old one gives -70 ms, and a
// rate built from that is negative in a way nothing downstream notices — the
// rollup row looks like a slow minute, not like a missing measurement.
//
// The third round is the PG13 case in the same test rather than a separate one,
// because it is the same rule with a different trigger: with no epoch available
// (StatsReset zero, which is what a PG13 collector reports — the info view's
// stats_reset is PG14+), the decrease itself is the only signal, and it has to
// produce the same zero.
func TestPutSnapshotCounterReset(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	const sql1 = "SELECT * FROM orders WHERE id = 42"
	r1 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC) // the epoch three days ago
	t0 := now.Add(-2 * time.Hour)
	t1 := now.Add(-1 * time.Hour)
	t2 := now

	// Round 1: the baseline.
	if err := s.PutSnapshot(ctx, snap(t0, r1, row("4571984512", sql1, 10, 100))); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	// Round 2: same epoch, counters up. This is the round that proves the test
	// measures deltas at all — if the store wrote cumulative numbers, every
	// later assertion would pass for the wrong reason.
	if err := s.PutSnapshot(ctx, snap(t1, r1, row("4571984512", sql1, 15, 150))); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	// Round 3: the reset. New epoch, smaller counters.
	r2 := now.Add(-30 * time.Minute)
	if err := s.PutSnapshot(ctx, snap(t2, r2, row("4571984512", sql1, 3, 30))); err != nil {
		t.Fatalf("round 3: %v", err)
	}

	want := []rollupTuple{
		// bucket t1: 15-10 calls, 150-100 ms.
		{bucket: unix(t1), calls: 5, totalMs: 50},
		// bucket t2: the reset. 3-10 would be -7 calls and -120 ms; the epoch
		// change zeroes both instead.
		{bucket: unix(t2), calls: 0, totalMs: 0},
	}
	got := rollupRows(t, s)
	if len(got) != len(want) {
		t.Fatalf("rollup rows = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		// The fingerprint is the store's own; only the bucket and the two
		// numbers are compared, so a normalizer change cannot fail this test by
		// hashing differently and a storage change cannot pass it by accident.
		if g.bucket != w.bucket || g.calls != w.calls || g.totalMs != w.totalMs {
			t.Errorf("rollup %d = %+v, want bucket %d calls %d total %.1f", i, g, w.bucket, w.calls, w.totalMs)
		}
	}

	// The invariant the two rules exist to protect, stated once over the whole
	// table rather than per row: no stored rollup is negative. A single negative
	// here means a reset was turned into a measurement somewhere.
	var neg int
	if err := s.DB().QueryRow(`
		SELECT COUNT(*) FROM query_rollup
		WHERE calls < 0 OR total_exec_ms < 0 OR cpu_ms < 0 OR io_wait_ms < 0 OR rows_returned < 0
	`).Scan(&neg); err != nil {
		t.Fatalf("count negatives: %v", err)
	}
	if neg != 0 {
		t.Errorf("%d rollup rows hold a negative counter; R3.3 forbids a negative rate", neg)
	}
}

// TestPutSnapshotNoEpochDecrease covers R3.3's PG13 fallback on its own: with
// StatsReset zero on both rounds there is no epoch to compare, so a counter that
// went backwards has to be caught per row.
//
// It is a separate test from the reset case because the two are different code
// paths in deltaFloat (resetChanged versus cur < prev) and a test that only
// exercises the first would pass on an implementation that never looked at the
// second.
func TestPutSnapshotNoEpochDecrease(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	const sql1 = "SELECT id FROM users ORDER BY id LIMIT 20"
	t0, t1 := now.Add(-2*time.Hour), now.Add(-time.Hour)

	if err := s.PutSnapshot(ctx, snap(t0, time.Time{}, row("7712003344", sql1, 40, 400))); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	// No epoch on either side, and the counters went down: the only signal left
	// is the decrease.
	if err := s.PutSnapshot(ctx, snap(t1, time.Time{}, row("7712003344", sql1, 25, 250))); err != nil {
		t.Fatalf("round 2: %v", err)
	}

	got := rollupRows(t, s)
	if len(got) != 1 {
		t.Fatalf("rollup rows = %d, want 1: %+v", len(got), got)
	}
	if got[0].calls != 0 || got[0].totalMs != 0 {
		t.Errorf("decrease with no epoch gave calls=%d total=%.1f, want 0/0", got[0].calls, got[0].totalMs)
	}
}

// TestPutSnapshotMergesDuplicateFingerprints pins R3.4's keying: two
// pg_stat_statements rows that normalize to one fingerprint sum their counters,
// and the stored text comes from the higher-total row.
//
// The duplicate is real, not hypothetical: pg_stat_statements' own key is
// (userid, dbid, queryid), so two queryids whose statements normalize to one
// template produce two rows with one fingerprint — which is the case R3.4's
// "additionally compute our own normalized fingerprint" exists to collapse. The
// fixture makes it explicit: two queryids, two literals, one template.
//
// The cite here is the R-id rather than the review-finding id the first draft
// used: R-ids are defined in requirements.md and the checker gates them, so a
// reader can open the line the test claims to implement.
func TestPutSnapshotMergesDuplicateFingerprints(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	// Both normalize to `SELECT * FROM items WHERE id = $1`.
	big := row("1111111111", "SELECT * FROM items WHERE id = 900", 100, 900)
	small := row("2222222222", "SELECT * FROM items WHERE id = 3", 1, 4)

	if err := s.PutSnapshot(ctx, snap(now, now.Add(-720*time.Hour), big, small)); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	if got := countRows(t, s, "query"); got != 1 {
		t.Fatalf("query rows = %d, want 1 (both statements share a fingerprint)", got)
	}
	if got := countRows(t, s, "query_source"); got != 1 {
		t.Fatalf("query_source rows = %d, want 1", got)
	}

	// The merged row: 101 calls and 904 ms, and the 900 ms statement's raw text.
	var calls int64
	var total float64
	var display, firstText string
	if err := s.DB().QueryRow(`
		SELECT qs.calls, qs.total_exec_ms, q.display_text, q.first_seen_text
		FROM query_source qs JOIN query q ON q.fingerprint = qs.fingerprint
	`).Scan(&calls, &total, &display, &firstText); err != nil {
		t.Fatalf("read merged row: %v", err)
	}
	if calls != 101 || total != 904 {
		t.Errorf("merged counters = %d/%.1f, want 101/904 (the sum, not the larger)", calls, total)
	}

	// Two text columns, two assertions, because they hold two different strings —
	// the distinction schema.go's query DDL spends six lines drawing: display_text
	// is the *template* cut to R3.6's cap, first_seen_text is the *raw* text cut to
	// the cap, and neither is re-derivable from the other.
	//
	// The template is identical for both rows — that is what makes them one
	// fingerprint — so it cannot show which row the merge took. The raw text can,
	// and "SELECT * FROM items WHERE id = 3" is exactly what a merge that took the
	// lower-total row would have stored. The first draft of this test asserted
	// "900" on display_text, which is the column that cannot contain it.
	if display != "SELECT * FROM items WHERE id = $1" {
		t.Errorf("display_text = %q, want the template", display)
	}
	if firstText != "SELECT * FROM items WHERE id = 900" {
		t.Errorf("first_seen_text = %q, want the higher-total row's raw text (the 900 one, not the 3 one)", firstText)
	}
}

// TestPutSnapshotPlanSampleIdentity is the proof table A3's R10.7 extension ships
// with: three plan samples for one fingerprint, from two users in two databases,
// and all three have to survive the write.
//
// The two-snapshot shape is forced by the input type — a Snapshot carries one
// Source, so one round sees one datname — and it is also what lets one assertion
// discriminate all three candidate keys at once. Both snapshots are captured at the
// same instant, which is what one collector round on a pod with two databases
// actually looks like:
//
//	(f0, prod/pg/pg-1, db1, appuser)   plan hash hash_appuser_db1
//	(f0, prod/pg/pg-1, db1, postgres)  plan hash hash_postgres_db1
//	(f0, prod/pg/pg-1, db2, postgres)  plan hash hash_postgres_db2
//
//	old    (fingerprint, captured_at)                          → 1 row (the defect)
//	narrow (fingerprint, usename, captured_at)                 → 2 rows (db1's and db2's postgres collide)
//	ruled  (fingerprint, ns, cluster, pod, datname, usename, captured_at) → 3 rows
//
// Asserting three is therefore the assertion that cannot pass on either rejected
// key — which is why the schema change and this test land in one commit. Written
// against the old table the test has to assert one and be edited in the next
// commit, and the commit in between carries an assertion known to be wrong.
func TestPutSnapshotPlanSampleIdentity(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	const sql1 = "SELECT id FROM invoices WHERE order_id = 17"
	epoch := now.Add(-720 * time.Hour)
	withPlan := func(queryid, usename, hash string) StatementRow {
		r := rowFor(queryid, sql1, usename, 10, 100)
		r.Plan = &dto.PlanSampleView{
			CapturedAt:   now,
			Method:       "auto-explain",
			Availability: "explained",
			SQL:          sql1,
			PlanJSON:     json.RawMessage(`{"Node Type":"Index Scan"}`),
			PlanHash:     hash,
		}
		return r
	}

	// One round, two databases, three (datname, usename) pairs on one fingerprint.
	err := s.PutSnapshot(ctx, snapFrom(sourceIn("db1"), now, epoch,
		withPlan("1000000001", "appuser", "hash_appuser_db1"),
		withPlan("1000000002", "postgres", "hash_postgres_db1")))
	if err != nil {
		t.Fatalf("snapshot in db1: %v", err)
	}
	if err := s.PutSnapshot(ctx, snapFrom(sourceIn("db2"), now, epoch,
		withPlan("1000000003", "postgres", "hash_postgres_db2"))); err != nil {
		t.Fatalf("snapshot in db2: %v", err)
	}

	// The registry side of the same fact: one fingerprint, three source rows. If
	// these are right and the plan count is not, the bug is in planRow's key
	// rather than in the dedupe that feeds it.
	if got := countRows(t, s, "query"); got != 1 {
		t.Errorf("query rows = %d, want 1 (three queryids, one template, one fingerprint)", got)
	}
	if got := countRows(t, s, "query_source"); got != 3 {
		t.Errorf("query_source rows = %d, want 3", got)
	}

	// The three plan rows, read back as their own identities so a failure names
	// which one is missing instead of only that one is.
	rows, err := s.DB().Query(`
		SELECT namespace, cluster, pod, datname, usename, plan_hash
		FROM plan_sample
		ORDER BY datname, usename`)
	if err != nil {
		t.Fatalf("read plan_sample: %v", err)
	}
	defer rows.Close()

	var got [][6]string
	for rows.Next() {
		var r [6]string
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3], &r[4], &r[5]); err != nil {
			t.Fatalf("scan plan_sample: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan_sample rows: %v", err)
	}

	want := [][6]string{
		{"prod", "pg", "pg-1", "db1", "appuser", "hash_appuser_db1"},
		{"prod", "pg", "pg-1", "db1", "postgres", "hash_postgres_db1"},
		{"prod", "pg", "pg-1", "db2", "postgres", "hash_postgres_db2"},
	}
	if len(got) != len(want) {
		t.Fatalf("plan_sample rows = %d, want %d (the old two-column PK leaves 1, the rejected narrow one leaves 2): %v",
			len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("plan_sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestPutSnapshotKeepsFirstSeenText pins R3.4's first-seen rule across two
// rounds: the query table's first_seen and first_seen_text are about the *first*
// sighting, so a later round updates last_seen and leaves the rest alone.
//
// The upsert's DO UPDATE branch touches last_seen only, which is the cheapest
// way to get this right — but "cheapest" is not the same as "checked", and a
// future change that moved the whole row into the update clause would silently
// rewrite the text a fingerprint was first seen with.
func TestPutSnapshotKeepsFirstSeenText(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	const sql1 = "DELETE FROM sessions WHERE token = 'abc'"
	t0 := now.Add(-time.Hour)
	if err := s.PutSnapshot(ctx, snap(t0, now.Add(-720*time.Hour), row("5555000001", sql1, 2, 20))); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	var firstSeen int64
	var firstText string
	if err := s.DB().QueryRow(`SELECT first_seen, first_seen_text FROM query`).Scan(&firstSeen, &firstText); err != nil {
		t.Fatalf("read query: %v", err)
	}
	if firstSeen != unix(t0) {
		t.Fatalf("first_seen = %d, want %d", firstSeen, unix(t0))
	}

	// Round 2, a later capture. first_seen must not move.
	t1 := now
	if err := s.PutSnapshot(ctx, snap(t1, now.Add(-720*time.Hour), row("5555000001", sql1, 3, 30))); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	var again struct {
		first int64
		text  string
		last  int64
	}
	if err := s.DB().QueryRow(`SELECT first_seen, first_seen_text, last_seen FROM query`).
		Scan(&again.first, &again.text, &again.last); err != nil {
		t.Fatalf("read query again: %v", err)
	}
	if again.first != firstSeen {
		t.Errorf("first_seen moved from %d to %d on a repeat sighting", firstSeen, again.first)
	}
	if again.text != firstText {
		t.Errorf("first_seen_text changed from %q to %q", firstText, again.text)
	}
	if again.last != unix(t1) {
		t.Errorf("last_seen = %d, want %d", again.last, unix(t1))
	}
}

// TestPutSnapshotValidation covers the inputs that would write a row nothing can
// read back. Each case names the field it is about, so a new required field
// without a case here is visible as a gap.
func TestPutSnapshotValidation(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()
	rows := []StatementRow{row("1", "SELECT 1", 1, 1)}

	cases := []struct {
		name string
		mut  func(*Snapshot)
	}{
		{"no namespace", func(x *Snapshot) { x.Source.Namespace = "" }},
		{"no cluster", func(x *Snapshot) { x.Source.Cluster = "" }},
		{"no pod", func(x *Snapshot) { x.Source.Pod = "" }},
		{"no datname", func(x *Snapshot) { x.Source.Datname = "" }},
		{"no captured_at", func(x *Snapshot) { x.CapturedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := snap(now, now.Add(-720*time.Hour), rows...)
			tc.mut(&snap)
			if err := s.PutSnapshot(ctx, snap); err == nil {
				t.Errorf("PutSnapshot accepted a snapshot with %s", tc.name)
			}
			if got := countRows(t, s, "query"); got != 0 {
				t.Errorf("a rejected snapshot still wrote %d query rows", got)
			}
		})
	}
}

// TestStddevValueEncoding pins the nullable-stddev rule: the view's NULL is the
// store's -1, and a real 0 stays 0.
//
// The two are one byte apart in the schema and mean opposite things — "every
// execution took the same time" versus "the view had nothing to say" — which is
// the same not-available-vs-zero distinction as the rollup's lock_wait sentinel.
func TestStddevValueEncoding(t *testing.T) {
	zero := 0.0
	for _, tc := range []struct {
		name string
		in   *float64
		want float64
	}{
		{"nil is not-available", nil, -1},
		{"zero is a measurement", &zero, 0},
		{"value passes through", ptr(2.5), 2.5},
	} {
		if got := stddevValue(tc.in); got != tc.want {
			t.Errorf("%s: stddevValue = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func ptr(f float64) *float64 { return &f }

// TestPutSnapshotPlanChange pins R3.11:315's event row and, with it, the S26
// fix: the previous hash is read for samples *older than the one being written*.
//
// Without S26's second argument the read-back returns the row the insert loop
// just wrote, prev equals the current hash, and no change is ever recorded —
// which compiles, and passes every test that does not look. So this test looks:
// two rounds with different hashes must produce exactly one plan_change row
// naming both, and a third round repeating the second hash must not add another.
func TestPutSnapshotPlanChange(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	const sql1 = "SELECT * FROM orders WHERE customer_id = 7 ORDER BY created_at DESC LIMIT 25"
	plan := func(at time.Time, hash string) *dto.PlanSampleView {
		return &dto.PlanSampleView{
			CapturedAt:   at,
			Method:       "auto-explain",
			Availability: "explained",
			SQL:          sql1,
			PlanJSON:     json.RawMessage(`{"Node Type":"Seq Scan","Relation Name":"orders"}`),
			PlanHash:     hash,
		}
	}
	withPlan := func(at time.Time, hash string) StatementRow {
		r := row("6600112233", sql1, 10, 100)
		r.Plan = plan(at, hash)
		return r
	}

	t0, t1, t2 := now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-1*time.Hour)
	hashA, hashB := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"

	// Round 1: the first sample. A plan exists, but there is no previous one, so
	// nothing changed and no plan_change row is written.
	if err := s.PutSnapshot(ctx, snap(t0, now.Add(-720*time.Hour), withPlan(t0, hashA))); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if got := countRows(t, s, "plan_change"); got != 0 {
		t.Fatalf("plan_change rows after the first plan = %d, want 0", got)
	}

	// Round 2: a different hash. This is the event R3.11 asks for.
	if err := s.PutSnapshot(ctx, snap(t1, now.Add(-720*time.Hour), withPlan(t1, hashB))); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	var prev, cur string
	var at int64
	if err := s.DB().QueryRow(`
		SELECT previous_plan_hash, current_plan_hash, detected_at FROM plan_change
	`).Scan(&prev, &cur, &at); err != nil {
		t.Fatalf("read plan_change: %v", err)
	}
	if prev != hashA || cur != hashB {
		t.Errorf("plan_change = %s -> %s, want %s -> %s", prev, cur, hashA, hashB)
	}
	if at != unix(t1) {
		t.Errorf("detected_at = %d, want %d", at, unix(t1))
	}

	// Round 3: the same hash as round 2. No change, so the count stays at one.
	if err := s.PutSnapshot(ctx, snap(t2, now.Add(-720*time.Hour), withPlan(t2, hashB))); err != nil {
		t.Fatalf("round 3: %v", err)
	}
	if got := countRows(t, s, "plan_change"); got != 1 {
		t.Errorf("plan_change rows = %d, want 1 (an unchanged plan adds no row)", got)
	}
}
