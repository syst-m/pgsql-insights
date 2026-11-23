package store

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// topnSQL is the fixture statement whose template every QueryList test reads
// back. The fixture's three sources share this one string on purpose: sharing it
// is what gives them one fingerprint, and one fingerprint spread over two
// databases and two pods is the whole projection R8.2's row describes.
const topnSQL = "SELECT id FROM invoices WHERE order_id = 17"

// sourceOn is the fixture source on a named pod and database.
//
// sourceIn varies the database; the top-N projection needs the pod varied too,
// because query_rollup's key is finer than the list row's and the GROUP BY that
// closes the gap is the thing under test.
func sourceOn(pod, db string) QuerySource {
	s := sourceIn(db)
	s.Pod = pod
	return s
}

// topnFixture builds the store every QueryList test reads.
//
// One fingerprint, seen by three sources — (pg-1, db1), (pg-2, db1), (pg-1, db2) —
// each with a baseline round and a delta round. Two rounds per source is not
// ceremony: R3.3's first rule means a source with one snapshot has no
// query_rollup row at all, and a store with no rollups makes every list assertion
// pass for the wrong reason (an empty answer satisfies "no rows" forever).
//
// The numbers are chosen so each assertion has one number to point at:
//
//	(pg-1, db1)  10 calls / 100 ms / 20 rows   queryid 1111111111
//	(pg-2, db1)   6 calls /  60 ms / 12 rows   queryid 9999999999
//	(pg-1, db2)   4 calls /  40 ms /  8 rows   queryid 5555555555
//
// Two details are load-bearing. The rows' rows_returned is not calls×10 here (the
// way rowFor builds it) but calls×2, because a fixture where the two averages come
// out equal cannot tell an AvgExecTimeMs from an AvgRowsReturned. And the two db1
// pods' queryids are the wrong way round for MAX(): the busier pod holds the
// lexicographically smaller id, so a query that used MAX(s.queryid) answers
// 9999999999 where the correlated subquery answers 1111111111.
func topnFixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()
	t0, t1 := now.Add(-time.Hour), now.Add(-30*time.Minute)
	epoch := now.Add(-720 * time.Hour)

	for _, r := range []struct {
		src                 QuerySource
		queryid             string
		baseCalls, curCalls int64
		baseTotal, curTotal float64
		baseRows, curRows   float64
	}{
		{sourceOn("pg-1", "db1"), "1111111111", 4, 14, 40, 140, 8, 28},
		{sourceOn("pg-2", "db1"), "9999999999", 2, 8, 20, 80, 4, 16},
		{sourceOn("pg-1", "db2"), "5555555555", 1, 5, 10, 50, 2, 10},
	} {
		base := StatementRow{
			QueryID: r.queryid, SQL: topnSQL, Usename: "appuser",
			Calls: r.baseCalls, TotalExecTimeMs: r.baseTotal, RowsReturned: r.baseRows,
		}
		cur := StatementRow{
			QueryID: r.queryid, SQL: topnSQL, Usename: "appuser",
			Calls: r.curCalls, TotalExecTimeMs: r.curTotal, RowsReturned: r.curRows,
		}
		if err := s.PutSnapshot(ctx, snapFrom(r.src, t0, epoch, base)); err != nil {
			t.Fatalf("baseline for %s/%s: %v", r.src.Pod, r.src.Datname, err)
		}
		if err := s.PutSnapshot(ctx, snapFrom(r.src, t1, epoch, cur)); err != nil {
			t.Fatalf("second round for %s/%s: %v", r.src.Pod, r.src.Datname, err)
		}
	}
	return s, now
}

// oneFingerprint reads back the store's single registered fingerprint. The
// fixture has exactly one, and every list assertion is about a row keyed on it, so
// the tests read it rather than recomputing it — a test that re-derived the
// fingerprint here would be testing the normalizer a second time.
func oneFingerprint(t *testing.T, s *Store) string {
	t.Helper()
	var fp string
	if err := s.DB().QueryRow(`SELECT fingerprint FROM query`).Scan(&fp); err != nil {
		t.Fatalf("read fingerprint: %v", err)
	}
	return fp
}

// TestWindow covers the [from, to] resolver every read path calls first.
//
// The two interesting rows are the ones a caller cannot see the effect of
// otherwise: an unset pair, which is what makes a memory store look back 24 h and
// a file store 7 d from one function (the reason R3.14's week-over-week question
// has an honest answer on one profile and not the other), and the reversed pair,
// which is clipped to from = to rather than swapped — a swap silently rewrites the
// caller's request, and the empty answer the clip produces is the honest one.
func TestWindow(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	day := s.RetentionWindow()

	tokyo := time.Date(2026, 11, 23, 15, 0, 0, 0, time.FixedZone("JST", 9*3600))

	tests := []struct {
		name     string
		from, to time.Time
		wantFrom time.Time
		wantTo   time.Time
	}{
		{
			name:     "both unset is the store's own window",
			wantFrom: now.Add(-day),
			wantTo:   now,
		},
		{
			name:     "to only keeps to and back-dates from",
			to:       now.Add(-2 * time.Hour),
			wantFrom: now.Add(-2*time.Hour - day),
			wantTo:   now.Add(-2 * time.Hour),
		},
		{
			name:     "from only keeps from and uses now as to",
			from:     now.Add(-3 * time.Hour),
			wantFrom: now.Add(-3 * time.Hour),
			wantTo:   now,
		},
		{
			name:     "an explicit pair wider than the retention is not clipped",
			from:     now.Add(-30 * 24 * time.Hour),
			to:       now,
			wantFrom: now.Add(-30 * 24 * time.Hour),
			wantTo:   now,
		},
		{
			// A3's S49: this case asserted the clip (from == to). The pair now
			// comes back as asked, and the query's own
			// `bucket >= from AND bucket <= to` matches nothing — the empty
			// answer, without window() rewriting the request.
			name:     "a reversed pair passes through, neither swapped nor clipped",
			from:     now.Add(time.Hour),
			to:       now.Add(-time.Hour),
			wantFrom: now.Add(time.Hour),
			wantTo:   now.Add(-time.Hour),
		},
		{
			// 15:00 +0900 is 06:00 UTC. The assertion is about the location the
			// answer carries, not the instant: the unix() the queries bind would
			// give the same second either way, and a non-UTC time.Time is what
			// makes a caller's stored bucket and the window disagree.
			name:     "a non-UTC pair comes back in UTC",
			from:     tokyo,
			to:       tokyo.Add(time.Hour),
			wantFrom: time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC),
			wantTo:   time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to := s.window(tt.from, tt.to)
			if !from.Equal(tt.wantFrom) || !to.Equal(tt.wantTo) {
				t.Errorf("window(%v, %v) = (%v, %v), want (%v, %v)",
					tt.from, tt.to, from, to, tt.wantFrom, tt.wantTo)
			}
			if from.Location() != time.UTC || to.Location() != time.UTC {
				t.Errorf("window returned non-UTC locations: %v %v", from.Location(), to.Location())
			}
		})
	}
}

// TestClusterKey pins the one-line map key behind every LoadByTotalTime lookup.
//
// \x00 as the separator is not decoration: with ":" or "/", ("a/b", "c") and
// ("a", "b/c") are the same key, and the failure is silent — one cluster's total
// is used as the denominator for another's rows, so every percentage on the page is
// wrong and no query looks malformed.
func TestClusterKey(t *testing.T) {
	if clusterKey("a", "b") == clusterKey("a/b", "") {
		t.Errorf(`clusterKey("a","b") == clusterKey("a/b","") — the separator must not be a character that can appear in a name`)
	}
	if clusterKey("prod", "pg") != clusterKey("prod", "pg") {
		t.Error("clusterKey is not deterministic")
	}
	// The two halves must both survive: a key that dropped the cluster would
	// collapse every cluster in a namespace onto one total.
	if clusterKey("prod", "pg") == clusterKey("prod", "pg2") {
		t.Error("clusterKey ignores the cluster")
	}
	if clusterKey("prod", "pg") == clusterKey("staging", "pg") {
		t.Error("clusterKey ignores the namespace")
	}
}

// TestLoadColumns covers the three derived columns and, more than the arithmetic,
// the two denominators that are allowed to be zero.
//
// A zero calls with a non-zero total is a row the collector produces (a stats reset
// mid-interval, R3.3), and 0/0 in Go is NaN — which encoding/json refuses to
// marshal, turning a 200 into a 500 at the encoding step, after the headers are
// already sent. An empty cluster has no total at all, and 100*t/0 is +Inf, which
// json refuses for the same reason. Both cases are asserted by marshalling,
// because a float field that is 0 and a float field that is NaN look the same in a
// struct comparison and behave completely differently on the way out.
func TestLoadColumns(t *testing.T) {
	r := queryListRow{
		fingerprint: "f", namespace: "prod", cluster: "pg", datname: "db1",
		calls: 16, totalExecMs: 160, cpuMs: 40, ioWaitMs: 12,
		rowsReturned: 32, sharedHit: 5, sharedRead: 1,
		pgMajor: 16, queryid: "1111111111", display: "SELECT 1", firstSeen: 1,
	}

	t.Run("the arithmetic", func(t *testing.T) {
		c := r.loadColumns(200)
		if c.LoadByTotalTime != 80 {
			t.Errorf("LoadByTotalTime = %v, want 80 (160 of a 200 cluster total)", c.LoadByTotalTime)
		}
		// The DTO documents these two as separate columns with one formula
		// available; until the spec supplies the wording that separates them,
		// both carry the R3.2 value and the UI renders both.
		if c.PctLoadByQueries != c.LoadByTotalTime {
			t.Errorf("PctLoadByQueries = %v, want it to equal LoadByTotalTime %v", c.PctLoadByQueries, c.LoadByTotalTime)
		}
		if c.AvgExecTimeMs != 10 {
			t.Errorf("AvgExecTimeMs = %v, want 10 (160 ms / 16 calls)", c.AvgExecTimeMs)
		}
		if c.AvgRowsReturned != 2 {
			t.Errorf("AvgRowsReturned = %v, want 2 (32 rows / 16 calls)", c.AvgRowsReturned)
		}
		// The pass-through columns are asserted here so a future edit that
		// reorders the struct literals — the failure mode an args()-less insert
		// has — shows up as one named field rather than as a diff.
		if c.TotalExecTimeMs != 160 || c.TimesCalled != 16 || c.LoadByCPUMs != 40 || c.LoadByIOWaitMs != 12 {
			t.Errorf("pass-through columns = %+v, want 160/16/40/12", c)
		}
	})

	t.Run("zero calls does not produce NaN", func(t *testing.T) {
		quiet := r
		quiet.calls = 0
		quiet.rowsReturned = 0
		c := quiet.loadColumns(200)
		if c.AvgExecTimeMs != 0 || c.AvgRowsReturned != 0 {
			t.Errorf("averages with 0 calls = %v/%v, want 0/0", c.AvgExecTimeMs, c.AvgRowsReturned)
		}
		if _, err := json.Marshal(c); err != nil {
			t.Fatalf("a zero-calls row must still marshal: %v", err)
		}
	})

	t.Run("an empty cluster does not produce Inf", func(t *testing.T) {
		c := r.loadColumns(0)
		if c.LoadByTotalTime != 0 || c.PctLoadByQueries != 0 {
			t.Errorf("shares against a 0 total = %v/%v, want 0/0", c.LoadByTotalTime, c.PctLoadByQueries)
		}
		if math.IsInf(c.LoadByTotalTime, 0) || math.IsNaN(c.LoadByTotalTime) {
			t.Fatalf("LoadByTotalTime = %v", c.LoadByTotalTime)
		}
		if _, err := json.Marshal(c); err != nil {
			t.Fatalf("a zero-total cluster must still marshal: %v", err)
		}
	})
}

// TestQueryListRowView pins the one DTO field the store has to manufacture rather
// than read: Recommendations has no omitempty, so a nil slice marshals to null and
// the UI's `.length` reads undefined instead of 0.
func TestQueryListRowView(t *testing.T) {
	r := queryListRow{
		fingerprint: "f", namespace: "prod", cluster: "pg", datname: "db1",
		calls: 1, totalExecMs: 1, display: "SELECT 1", truncated: 1,
	}
	v := r.view(1)

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal QueryView: %v", err)
	}
	if !strings.Contains(string(b), `"recommendations":[]`) {
		t.Errorf("marshalled view has no `\"recommendations\":[]` — the empty slice must be made, not left nil: %s", b)
	}
	// The truncated flag is an int in the table and a bool in the DTO; the
	// conversion is one line and it is the only place the two shapes meet.
	if !v.Truncated {
		t.Error("Truncated = false for a row stored with truncated = 1")
	}
	if v.Query != "SELECT 1" || v.Fingerprint != "f" || v.Database != "db1" {
		t.Errorf("identity columns = %+v", v)
	}
}

// TestQueryListProjection is R8.2's row: what one fingerprint looks like when the
// rollup's finer key has several pods and users under it.
//
// The fixture's three sources produce two list rows, not three and not one. Not
// three, because the DTO has no pod and no user column and the GROUP BY collapses
// (pg-1, db1) and (pg-2, db1) into one row; not one, because the same statement in
// db2 is a different row — R3.4's cross-database grouping reason, and the reason
// the list's key is four columns rather than the fingerprint alone.
func TestQueryListProjection(t *testing.T) {
	s, _ := topnFixture(t)
	fp := oneFingerprint(t, s)

	views, err := s.QueryList(context.Background(), api.ListQuery{})
	if err != nil {
		t.Fatalf("QueryList: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("QueryList returned %d rows, want 2 (three sources, one fingerprint, two databases): %+v",
			len(views), views)
	}

	byDB := map[string]dto.QueryView{}
	for _, v := range views {
		byDB[v.Database] = v
	}
	if len(byDB) != 2 {
		t.Fatalf("rows span %d databases, want 2", len(byDB))
	}

	for _, tt := range []struct {
		db      string
		calls   int64
		totalMs float64
		avgMs   float64
		avgRows float64
		queryid string
	}{
		{"db1", 16, 160, 10, 2, "1111111111"}, // both pods summed
		{"db2", 4, 40, 10, 2, "5555555555"},
	} {
		v, ok := byDB[tt.db]
		if !ok {
			t.Errorf("no row for %s", tt.db)
			continue
		}
		if v.Fingerprint != fp {
			t.Errorf("%s: Fingerprint = %q, want %q", tt.db, v.Fingerprint, fp)
		}
		if v.Namespace != "prod" || v.Cluster != "pg" {
			t.Errorf("%s: scope = %s/%s, want prod/pg", tt.db, v.Namespace, v.Cluster)
		}
		if v.TimesCalled != tt.calls || v.LoadColumns.TotalExecTimeMs != tt.totalMs {
			t.Errorf("%s: calls/total = %d/%v, want %d/%v",
				tt.db, v.TimesCalled, v.LoadColumns.TotalExecTimeMs, tt.calls, tt.totalMs)
		}
		if v.LoadColumns.AvgExecTimeMs != tt.avgMs || v.LoadColumns.AvgRowsReturned != tt.avgRows {
			t.Errorf("%s: avgMs/avgRows = %v/%v, want %v/%v (the two averages must not be interchangeable)",
				tt.db, v.LoadColumns.AvgExecTimeMs, v.LoadColumns.AvgRowsReturned, tt.avgMs, tt.avgRows)
		}
		// The correlated subquery's answer, not MAX()'s: db1's group holds
		// 1111111111 (100 ms) and 9999999999 (60 ms), and the DTO wants the
		// busiest source's id. MAX() returns the lexicographically largest.
		if v.QueryID != tt.queryid {
			t.Errorf("%s: QueryID = %q, want %q (the busiest source's, not MAX())", tt.db, v.QueryID, tt.queryid)
		}
		if v.PGMajor != 16 {
			t.Errorf("%s: PGMajor = %d, want 16 (query_source is the only source for it, hence the join)",
				tt.db, v.PGMajor)
		}
		// The DTO's Query is the template, not the raw text — the distinction
		// schema.go's query DDL draws between display_text and first_seen_text.
		if !strings.Contains(v.Query, "$1") || strings.Contains(v.Query, "17") {
			t.Errorf("%s: Query = %q, want the normalised template (the literal is in first_seen_text)", tt.db, v.Query)
		}
	}
}

// TestQueryListDenominatorIgnoresDBFilter is the half of LoadByTotalTime that a
// single-query implementation gets wrong.
//
// dto calls the column "this query's share of the cluster's total load", so the
// denominator is the cluster's — not the filtered subset's. Filter to db1 and the
// rows shown are db1's; if the denominator were db1's too, every database's
// queries would sum to 100% and the column would stop meaning anything. So
// clusterLoadSQL deliberately ignores the db filter while queryListSQL honours it,
// and the assertion that pins it is a number: db1's row is 80% of the cluster, not
// 100% of db1.
func TestQueryListDenominatorIgnoresDBFilter(t *testing.T) {
	s, _ := topnFixture(t)

	views, err := s.QueryList(context.Background(), api.ListQuery{DB: "db1"})
	if err != nil {
		t.Fatalf("QueryList(?db=db1): %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("rows = %d, want 1 (the db filter applies to the rows)", len(views))
	}
	if views[0].Database != "db1" {
		t.Fatalf("row is for %s, want db1", views[0].Database)
	}
	got := views[0].LoadColumns.LoadByTotalTime
	// 160 of a 200 ms cluster total (160 in db1 + 40 in db2). A denominator
	// taken from the filtered rows would say 100.
	if math.Abs(got-80) > 1e-9 {
		t.Errorf("LoadByTotalTime = %v, want 80 (160 of the whole cluster, not 100 of db1)", got)
	}

	// The scope filters do apply to the denominator — that is what "the cluster"
	// names. Ask for a cluster with no rows and the share is 0, not 100.
	empty, err := s.QueryList(context.Background(), api.ListQuery{Cluster: "other"})
	if err != nil {
		t.Fatalf("QueryList(?cluster=other): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("rows for cluster \"other\" = %d, want 0", len(empty))
	}
}

// TestQueryListWindowAndBucketKind covers the two WHERE clauses the list's SQL
// carries beyond the scope filters.
//
// The bucket range is R8.4's ?from=/?to= made physical: the same store answers for
// any window, and a minute outside the requested one must not contribute. The
// bucket_kind filter is the quieter one — query_rollup's PK includes the kind, so
// an hour row and a minute row for the same instant are two rows, and a list query
// that dropped the filter would add the hour's total to the minute's and report
// every query at roughly 60× its real load. The fixture has only minute rows, so
// the test inserts an hour row and asserts the numbers do not move.
func TestQueryListWindowAndBucketKind(t *testing.T) {
	s, now := topnFixture(t)
	fp := oneFingerprint(t, s)
	ctx := context.Background()

	t.Run("a window that contains the minute", func(t *testing.T) {
		views, err := s.QueryList(ctx, api.ListQuery{
			From: now.Add(-45 * time.Minute), To: now,
		})
		if err != nil {
			t.Fatalf("QueryList: %v", err)
		}
		if len(views) != 2 {
			t.Fatalf("rows = %d, want 2", len(views))
		}
	})

	t.Run("a window that does not", func(t *testing.T) {
		// The fixture's only rollup minute is t1 = now-30m; the baseline round
		// at t0 produced none (R3.3's first rule), so a window ending before
		// t1 has nothing to return and must return none rather than falling
		// back to the store's own window.
		views, err := s.QueryList(ctx, api.ListQuery{
			From: now.Add(-90 * time.Minute), To: now.Add(-60 * time.Minute),
		})
		if err != nil {
			t.Fatalf("QueryList: %v", err)
		}
		if len(views) != 0 {
			t.Fatalf("rows = %d, want 0 (the only minute is at now-30m): %+v", len(views), views)
		}
	})

	t.Run("an hour bucket does not count toward a minute window", func(t *testing.T) {
		before, err := s.QueryList(ctx, api.ListQuery{})
		if err != nil {
			t.Fatalf("QueryList before the insert: %v", err)
		}
		if _, err := s.DB().Exec(`
			INSERT INTO query_rollup
				(fingerprint, namespace, cluster, pod, datname, usename, bucket_kind, bucket,
				 calls, total_exec_ms, cpu_ms, io_wait_ms, rows_returned,
				 shared_blks_hit, shared_blks_read)
			VALUES (?, 'prod', 'pg', 'pg-1', 'db1', 'appuser', 'hour', ?,
			        1000, 9999, 0, 0, 0, 0, 0)`,
			fp, minuteBucket(now.Add(-30*time.Minute))); err != nil {
			t.Fatalf("insert hour row: %v", err)
		}

		after, err := s.QueryList(ctx, api.ListQuery{})
		if err != nil {
			t.Fatalf("QueryList after the insert: %v", err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Errorf("an hour row changed the minute list:\nbefore %+v\nafter  %+v", before, after)
		}
	})
}

// One source, two fingerprints, two rounds. The baseline round carries both rows
// at zero so both fingerprints register without producing rollups (R3.3's first
// rule), and the second round's snapshot carries both rows' new counters — one
// snapshot per round, because one source's round is one snapshot.
//
// The two assertions are the two paging claims topn.go's doc makes. The order is
// the store's own (fingerprint ascending) so its output is deterministic for a
// caller that does not page. The Limit is the interesting one: q.Limit is
// deliberately unused here, because the handler's pageRows cuts the page and a
// store that honoured it too would make meta.total report the page size — the
// failure queries.go's doc names, and the reason the method is called QueryList
// rather than TopN.
func TestQueryListOrderingAndLimit(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()
	epoch := now.Add(-720 * time.Hour)

	base := []StatementRow{
		rowFor("1111111111", "SELECT id FROM invoices WHERE order_id = 17", "appuser", 0, 0),
		rowFor("2222222222", "SELECT count(*) FROM orders", "appuser", 0, 0),
	}
	cur := []StatementRow{
		rowFor("1111111111", "SELECT id FROM invoices WHERE order_id = 17", "appuser", 10, 100),
		rowFor("2222222222", "SELECT count(*) FROM orders", "appuser", 5, 50),
	}
	if err := s.PutSnapshot(ctx, snap(now.Add(-time.Hour), epoch, base...)); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if err := s.PutSnapshot(ctx, snap(now.Add(-30*time.Minute), epoch, cur...)); err != nil {
		t.Fatalf("second round: %v", err)
	}

	all, err := s.QueryList(ctx, api.ListQuery{})
	if err != nil {
		t.Fatalf("QueryList: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(all), all)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Fingerprint > all[i].Fingerprint {
			t.Errorf("rows %d and %d are out of fingerprint order (%q then %q)",
				i-1, i, all[i-1].Fingerprint, all[i].Fingerprint)
		}
	}

	limited, err := s.QueryList(ctx, api.ListQuery{Limit: 1})
	if err != nil {
		t.Fatalf("QueryList(Limit=1): %v", err)
	}
	if len(limited) != len(all) {
		t.Errorf("Limit=1 returned %d rows, want %d — the store returns the full set and pageRows cuts it",
			len(limited), len(all))
	}
}

// TestQueryListAgreesWithQueryDetail is the cross-route contract
// api/query_detail.go states and the one thing a second mapping would break: the
// detail view's embedded QueryView must agree with the row the list route would
// have shown for the same triple.
//
// It holds by construction rather than by test — queryDetail calls the same
// queryRows with one more equality and the same view() — which is exactly why the
// test is worth having: it is the only thing that fails when someone gives the
// detail its own mapping again, and the failure names the field that diverged.
func TestQueryListAgreesWithQueryDetail(t *testing.T) {
	s, _ := topnFixture(t)
	ctx := context.Background()

	views, err := s.QueryList(ctx, api.ListQuery{})
	if err != nil {
		t.Fatalf("QueryList: %v", err)
	}
	for _, list := range views {
		detail, err := s.queryDetail(ctx, list.Namespace, list.Cluster, list.Fingerprint, list.Database, "")
		if err != nil {
			t.Fatalf("queryDetail(%s/%s/%s): %v", list.Namespace, list.Cluster, list.Fingerprint, err)
		}
		if !reflect.DeepEqual(detail.QueryView, list) {
			t.Errorf("detail's QueryView disagrees with the list row for %s:\nlist   %+v\ndetail %+v",
				list.Database, list, detail.QueryView)
		}
	}
}
