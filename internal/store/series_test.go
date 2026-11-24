// series_test.go is A3's S29 test file for series.go — R8.11's batched series read
// behind GET /api/v1/insights/queries/series.
//
// Like detail_test.go, it splits the source into the half that is SQL and the half
// that is Go, because the two fail differently: a wrong GROUP BY changes which
// points exist, a wrong division changes what an existing point says. The SQL half
// is reached through QuerySeries (every one of seriesSQL's columns surfaces on
// dto.QuerySeriesPoint, so the entry shape and the aggregate are the same
// assertion), and seriesRow.point is tested on its own.
//
// The fixture seeds query_rollup directly instead of going through PutSnapshot,
// for the reason detail_test.go's history test gives for plan_sample: the write
// path can only fill two of the columns this file is about one way — bucket_kind
// is always 'minute' and lock_wait_ms is always the -1 sentinel — and the
// bucket_kind filter and the lock_wait CASE are two of the five SQL facts in
// series.go. Seeding lets a test put an 'hour' row and a real lock_wait_ms next
// to the values the writer produces.
//
// Not covered: the three error returns in seriesRows (QueryContext, Scan,
// rows.Err). They wrap the driver's error verbatim and there is no cheap way to
// make a live *sql.DB fail one of them short of closing it under the call.

package store

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// seriesNow is the fixture clock. seriesRows reads q.Start/q.End and never
// s.now(), so this only pins openMem's clock — but pinning it is what lets every
// bucket below be written as an offset from a named instant.
var seriesNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

// fpNamed is a 64-hex fingerprint with the given prefix and zeros after it —
// validFingerprint's shape without the test reading a wall of hex. The prefix is
// the only part that matters: it is what makes lexicographic order (what the SQL's
// ORDER BY produces) differ from request order (what the contract promises).
func fpNamed(prefix string) string {
	return prefix + strings.Repeat("0", 64-len(prefix))
}

// seriesRollupInsert is the fixture's INSERT INTO query_rollup.
//
// It is not statements.go's insertRollupSQL, which hardcodes bucket_kind to the
// 'minute' literal and so cannot seed the 'hour' row TestSeriesBucketKind needs.
// cpu_ms and io_wait_ms are 0 literals: seriesSQL does not read them (they are
// statements.go's derivation, and statements_test.go covers it), and naming them
// in the column list is what keeps this INSERT valid if the table ever grows a
// NOT NULL column between the two.
const seriesRollupInsert = `
INSERT OR REPLACE INTO query_rollup
	(fingerprint, namespace, cluster, pod, datname, usename, bucket_kind, bucket,
	 calls, total_exec_ms, cpu_ms, io_wait_ms, rows_returned,
	 shared_blks_hit, shared_blks_read, lock_wait_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?)`

// seriesSeed is one query_rollup row. The field order mirrors the INSERT's column
// list so a call site can be checked against the table without opening schema.go.
type seriesSeed struct {
	fp        string
	namespace string
	cluster   string
	pod       string
	datname   string
	usename   string
	kind      string // '' means 'minute'
	bucket    int64
	calls     int64
	totalMs   float64
	rows      float64
	hit       float64
	read      float64
	lockMs    float64
}

// seed is a minute row under the fixture's default scope (prod/pg/pg-1, db1,
// appuser) with the numbers the test wants.
func seed(fp string, bucket int64, calls int64, totalMs, rows, hit, read, lockMs float64) seriesSeed {
	return seriesSeed{
		fp: fp, namespace: "prod", cluster: "pg", pod: "pg-1", datname: "db1",
		usename: "appuser", kind: "minute", bucket: bucket,
		calls: calls, totalMs: totalMs, rows: rows, hit: hit, read: read, lockMs: lockMs,
	}
}

// seedOn is seed with the pod, database and user named — the three identity
// columns the request cannot filter on but the GROUP BY folds away.
func seedOn(fp, pod, db, user string, bucket int64, calls int64, totalMs, rows, hit, read, lockMs float64) seriesSeed {
	s := seed(fp, bucket, calls, totalMs, rows, hit, read, lockMs)
	s.pod, s.datname, s.usename = pod, db, user
	return s
}

// seedKind is seed with bucket_kind named, for the hour row the kind filter
// is supposed to ignore.
func seedKind(fp, kind string, bucket int64, calls int64, totalMs, rows, hit, read, lockMs float64) seriesSeed {
	s := seed(fp, bucket, calls, totalMs, rows, hit, read, lockMs)
	s.kind = kind
	return s
}

// newSeriesStore opens an empty in-memory store and seeds it.
//
// query_rollup has no foreign key (schema.go puts the REFERENCES on
// query_source/plan_sample/plan_change, not here), so seeding rollups needs no
// query rows — which is why this fixture can be one INSERT and no registry.
func newSeriesStore(t *testing.T, rows ...seriesSeed) *Store {
	t.Helper()
	s := openMem(t, seriesNow)
	for _, r := range rows {
		_, err := s.DB().Exec(seriesRollupInsert,
			r.fp, r.namespace, r.cluster, r.pod, r.datname, r.usename, r.kind, r.bucket,
			r.calls, r.totalMs, r.rows, r.hit, r.read, r.lockMs,
		)
		if err != nil {
			t.Fatalf("seed rollup %s/%d: %v", r.fp[:2], r.bucket, err)
		}
	}
	return s
}

// ask is the one-line QuerySeries call every test makes: the fingerprints, plus
// whatever scope the case names.
func ask(t *testing.T, s *Store, fps []string, namespace, cluster, datname string) []dto.QuerySeries {
	t.Helper()
	out, err := s.QuerySeries(context.Background(), api.SeriesQuery{
		Fingerprints: fps,
		Namespace:    namespace,
		Cluster:      cluster,
		Datname:      datname,
		Start:        seriesNow.Add(-72 * time.Hour),
		End:          seriesNow.Add(72 * time.Hour),
	})
	if err != nil {
		t.Fatalf("QuerySeries(%s): %v", strings.Join(shortFPs(fps), ","), err)
	}
	return out
}

// askAll is ask with the window left wide and the scope empty, for the tests whose
// only variable is the fingerprint list.
func askAll(t *testing.T, s *Store, fps ...string) []dto.QuerySeries {
	t.Helper()
	return ask(t, s, fps, "", "", "")
}

// askWindow is ask with the window named, for TestSeriesWindowBounds.
func askWindow(t *testing.T, s *Store, fps []string, start, end time.Time) []dto.QuerySeries {
	t.Helper()
	out, err := s.QuerySeries(context.Background(), api.SeriesQuery{
		Fingerprints: fps, Start: start, End: end,
	})
	if err != nil {
		t.Fatalf("QuerySeries(window): %v", err)
	}
	return out
}

// shortFPs is the fingerprint list with each entry cut to its prefix, so a failure
// message names the fingerprints without printing 64 zeros per entry.
func shortFPs(fps []string) []string {
	out := make([]string, 0, len(fps))
	for _, fp := range fps {
		out = append(out, fpNamed(fp[:2]))
	}
	return out
}

// entriesByFingerprint indexes an answer by fingerprint so a test that asked for
// several can name the one it means instead of counting positions.
func entriesByFingerprint(out []dto.QuerySeries) map[string]dto.QuerySeries {
	m := make(map[string]dto.QuerySeries, len(out))
	for _, e := range out {
		m[e.Fingerprint] = e
	}
	return m
}

// pointRows is an entry's points as their Rows values, in order.
//
// Rows is the one field with no unit conversion — dto's `rows` is the column's
// count, not a duration — so it identifies a seeded minute exactly. It is the
// field to assert on when the question is "which minutes did this entry get"
// rather than "what does this minute say".
func pointRows(e dto.QuerySeries) []float64 {
	out := make([]float64, 0, len(e.Points))
	for _, p := range e.Points {
		out = append(out, p.Rows)
	}
	return out
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

// sameFloat is the comparison for a value that went through the ms→s division.
// The fixtures' milliseconds are integers, so the quotient is exactly
// representable and == would do; the tolerance is here so a future fixture with
// 0.1 ms does not fail on the last bit.
func sameFloat(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// sameFloatP is sameFloat for the DTO's two *float64 fields (R8.3's nullable pair).
//
// A nil pointer is never "the same float" as any want, so a field the store left nil
// fails an assertion that expected a number instead of panicking on the deref — which
// is the difference between a test that reports the sentinel and a test that crashes
// on it.
func sameFloatP(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) < 1e-9
}

// jsonHas and jsonLacks assert on an entry's JSON spelling, which is where the
// omitempty rules live: `"points":[]` versus `"points":null`, and the absence of
// pod/usename/queryid on a batch entry, are both invisible to a Go-side check.
func jsonHas(t *testing.T, v any, want string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	if !strings.Contains(string(b), want) {
		t.Errorf("JSON %s does not contain %s", b, want)
	}
}

func jsonLacks(t *testing.T, v any, want string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	if strings.Contains(string(b), want) {
		t.Errorf("JSON %s contains %s, want it absent", b, want)
	}
}

// TestSeriesEmptyFingerprintList covers seriesRows' first branch and the shape of
// its answer when nothing was requested.
//
// The early return exists because SQLite rejects `IN ()` outright, so the
// assertion that matters is err == nil: an implementation that built the query
// anyway answers this with a driver error, and R8.11's route turns that into a 500
// for a request that is merely empty.
func TestSeriesEmptyFingerprintList(t *testing.T) {
	s := newSeriesStore(t, seed(fpNamed("ab"), minuteBucket(seriesNow), 1, 10, 10, 1, 1, 5))
	ctx := context.Background()

	// Two shapes of "nothing requested": the nil slice a caller gets from
	// SeriesQuery{}, and the empty slice parseFingerprints never produces (it
	// rejects an empty `fingerprints=`) but a hand-built query can hold.
	for name, fps := range map[string][]string{"nil": nil, "empty": {}} {
		rows, err := s.seriesRows(ctx, api.SeriesQuery{Fingerprints: fps, Namespace: "prod", Cluster: "pg"})
		if err != nil {
			t.Errorf("seriesRows(%s list): %v, want nil error (the guard exists so an empty list is not `IN ()`)", name, err)
			continue
		}
		if len(rows) != 0 {
			t.Errorf("seriesRows(%s list) = %d rows, want 0", name, len(rows))
		}
	}

	// QuerySeries answers an empty, non-nil slice for the same input — the
	// `data: []` beside `total: 0` that emptyQuerySeries's doc says is the
	// difference between "none" and "you were truncated".
	out, err := s.QuerySeries(ctx, api.SeriesQuery{})
	if err != nil {
		t.Fatalf("QuerySeries(empty): %v", err)
	}
	if out == nil {
		t.Fatal("QuerySeries(empty) returned a nil slice, want an empty one (a nil marshals to null)")
	}
	if len(out) != 0 {
		t.Fatalf("QuerySeries(empty) = %d entries, want 0", len(out))
	}
	if b, err := json.Marshal(out); err != nil || string(b) != "[]" {
		t.Errorf("QuerySeries(empty) marshals to %s (err %v), want []", b, err)
	}
}

// TestSeriesBatchOrderAndLabels is the batch contract: len(q.Fingerprints)
// entries, in request order, with the labels the request itself supplied.
//
// Request order is what makes this test mean something. The SQL ends with
// ORDER BY r.fingerprint, so an implementation that returned rows in query order
// answers [aa, cc, ee, ff] where the contract asks for the caller's order — and
// the caller asked for ff, aa, cc, 00. The seeded-but-unrequested fingerprint is
// the other half: the doc says a row nobody asked for is dropped by the
// map-before-build shape rather than becoming an entry, so the answer must be four
// entries, not five.
func TestSeriesBatchOrderAndLabels(t *testing.T) {
	bm := minuteBucket(seriesNow.Add(-2 * time.Hour))
	b0 := minuteBucket(seriesNow.Add(-time.Hour))
	b1 := minuteBucket(seriesNow)
	fpZulu, fpAlpha, fpMid, fpExtra, fpUnknown :=
		fpNamed("ff"), fpNamed("aa"), fpNamed("cc"), fpNamed("ee"), fpNamed("00")

	s := newSeriesStore(t,
		seed(fpZulu, b0, 11, 110, 11, 1, 0, 10), seed(fpZulu, b1, 12, 120, 12, 1, 0, 10),
		seed(fpAlpha, bm, 21, 210, 21, 2, 1, 10), seed(fpAlpha, b0, 22, 220, 22, 2, 1, 10),
		seed(fpAlpha, b1, 23, 230, 23, 2, 1, 10),
		seed(fpMid, b0, 31, 310, 31, 3, 1, 10),
		seed(fpExtra, b0, 41, 410, 41, 4, 1, 10), // seeded, not requested
	)

	// The request order is deliberately not the SQL's: ff, aa, cc, 00 against an
	// ORDER BY that would produce 00, aa, cc, ee, ff.
	want := []string{fpZulu, fpAlpha, fpMid, fpUnknown}
	out := ask(t, s, want, "prod", "pg", "db1")

	if len(out) != len(want) {
		t.Fatalf("got %d entries, want %d (fpExtra is seeded but not requested, so it must not appear)",
			len(out), len(want))
	}
	for i, fp := range want {
		if out[i].Fingerprint != fp {
			t.Errorf("entry %d is %s, want %s — the contract answers in request order, not the ORDER BY's",
				i, shortFPs([]string{out[i].Fingerprint})[0], shortFPs([]string{fp})[0])
		}
	}

	// Each entry carries its own minutes and nobody else's. The rows values are
	// 11/12, 21/22/23, 31 per fingerprint, so a shared or crossed slice shows up
	// here even though all four fingerprints share one scope.
	wantPoints := map[string][]float64{
		fpZulu:  {11, 12},
		fpAlpha: {21, 22, 23},
		fpMid:   {31},
		// The contract's "empty (not missing) Points for a fingerprint it has
		// never seen": an unknown fingerprint here is an entry with no points,
		// where the same string on the detail route is a 404. One stale row in a
		// 50-row page must not fail the other 49.
		fpUnknown: {},
	}
	for fp, pts := range wantPoints {
		e, ok := entriesByFingerprint(out)[fp]
		if !ok {
			t.Errorf("no entry for %s", shortFPs([]string{fp})[0])
			continue
		}
		if got := pointRows(e); !equalFloats(got, pts) {
			t.Errorf("%s points rows = %v, want %v", shortFPs([]string{fp})[0], got, pts)
		}
	}

	// The labels are the request's own scoping and nothing else. Pod,
	// InstanceRole, QueryID and Usename stay empty even though every seeded row
	// names a pod and a user: the points aggregate across the cluster's instances
	// (dto: those two are "present only when the answer is a single instance's"),
	// and the batch entry's identity has no single queryid to copy — R9.5b makes
	// it informational and R3.4 makes it never the key, so the route leaves it to
	// the two routes that have a well-defined one.
	e := entriesByFingerprint(out)[fpZulu]
	if e.Namespace != "prod" || e.Cluster != "pg" || e.Datname != "db1" {
		t.Errorf("labels = (%s, %s, %s), want (prod, pg, db1) from the request",
			e.Namespace, e.Cluster, e.Datname)
	}
	if e.Pod != "" || e.InstanceRole != "" || e.QueryID != "" || e.Usename != "" {
		t.Errorf("per-instance labels are set: pod=%q role=%q queryid=%q usename=%q, want all empty",
			e.Pod, e.InstanceRole, e.QueryID, e.Usename)
	}

	// The same facts as JSON, where omitempty is what makes them assertable.
	jsonHas(t, e, `"fingerprint":"`+fpZulu+`"`)
	jsonHas(t, e, `"namespace":"prod"`)
	jsonHas(t, e, `"cluster":"pg"`)
	jsonHas(t, e, `"datname":"db1"`)
	jsonLacks(t, e, `"pod"`)
	jsonLacks(t, e, `"usename"`)
	jsonLacks(t, e, `"queryid"`)
	jsonHas(t, e, `"points":[{`)

	// And the unknown fingerprint's entry says "none" with `[]`, not `null` —
	// R8.3's house rule, and the reason the code assigns an empty slice rather
	// than leaving the field nil.
	unknown := entriesByFingerprint(out)[fpUnknown]
	jsonHas(t, unknown, `"points":[]`)
	jsonLacks(t, unknown, "null")
}

// TestSeriesSumsAcrossPodsAndUsers pins the GROUP BY's other half: one entry per
// fingerprint means every pod's and every user's row for one minute folds into one
// point, and the SUMs have to be over all of them.
//
// The four rows' numbers are chosen so every partial sum is a different number —
// db1 alone is 12 calls, appuser alone is 13, pg-1 alone is 14, all four is 18 —
// so a SUM that drops a dimension answers a number the fixture can name. The
// fixture's own topnFixture uses the same trick for the list route's rows.
func TestSeriesSumsAcrossPodsAndUsers(t *testing.T) {
	b0 := minuteBucket(seriesNow.Add(-time.Hour))
	fp := fpNamed("ab")

	s := newSeriesStore(t,
		seedOn(fp, "pg-1", "db1", "appuser", b0, 3, 30, 30, 5, 1, 10),
		seedOn(fp, "pg-2", "db1", "appuser", b0, 4, 40, 40, 6, 2, 20),
		seedOn(fp, "pg-1", "db1", "postgres", b0, 5, 50, 50, 7, 3, 30),
		seedOn(fp, "pg-1", "db2", "appuser", b0, 6, 60, 60, 8, 4, 40),
	)

	t.Run("unfiltered folds every pod, database and user", func(t *testing.T) {
		out := askAll(t, s, fp)
		if len(out) != 1 {
			t.Fatalf("got %d entries, want 1", len(out))
		}
		if len(out[0].Points) != 1 {
			t.Fatalf("got %d points, want 1 — the four rows share one minute, and the GROUP BY is (fingerprint, bucket)",
				len(out[0].Points))
		}
		p := out[0].Points[0]
		// The two duration fields divide; the three counters do not.
		if !sameFloat(p.ExecutionTimeSeconds, 0.18) {
			t.Errorf("executionTimeSeconds = %v, want 0.18 (180 ms / 1000)", p.ExecutionTimeSeconds)
		}
		if !sameFloatP(p.LockTimeSeconds, 0.1) {
			t.Errorf("lockTimeSeconds = %v, want 0.1 (100 ms / 1000)", p.LockTimeSeconds)
		}
		if !sameFloat(p.Rows, 180) {
			t.Errorf("rows = %v, want 180 — rows_returned is a count, not a duration", p.Rows)
		}
		if !sameFloat(p.SharedBlocksHit, 26) || !sameFloat(p.SharedBlocksRead, 10) {
			t.Errorf("sharedBlocksHit/Read = %v/%v, want 26/10", p.SharedBlocksHit, p.SharedBlocksRead)
		}
		if !p.Timestamp.Equal(timeFromUnix(b0)) {
			t.Errorf("timestamp = %s, want %s", p.Timestamp, timeFromUnix(b0))
		}
	})

	// With ?db= set the group is exactly the list route's row aggregate — the one
	// place the two read shapes reconcile, and the reason the entry can label
	// itself db1 while the unfiltered entry cannot label itself anything.
	t.Run("the db filter narrows the sum", func(t *testing.T) {
		out := ask(t, s, []string{fp}, "prod", "pg", "db1")
		if len(out) != 1 || len(out[0].Points) != 1 {
			t.Fatalf("got %+v, want one entry with one point", out)
		}
		p := out[0].Points[0]
		if !sameFloat(p.ExecutionTimeSeconds, 0.12) || !sameFloat(p.Rows, 120) {
			t.Errorf("db1 point = %v s / %v rows, want 0.12 s / 120 rows (db2's row must not be in the sum)",
				p.ExecutionTimeSeconds, p.Rows)
		}
		if !sameFloat(p.SharedBlocksHit, 18) || !sameFloatP(p.LockTimeSeconds, 0.06) {
			t.Errorf("db1 hit/lock = %v/%v, want 18/0.06", p.SharedBlocksHit, p.LockTimeSeconds)
		}
	})
}

// TestSeriesLockWaitPoisoning covers the one column seriesSQL cannot SUM: the
// schema's -1 means "this source has no lock_time" (A3's S25 interim encoding), and
// SUM of -1 and 5.0 is 4.0 — a number that looks like a measurement and isn't.
// The CASE makes one unanswerable source poison the minute, which is R3.14's
// answer-not-available-rather-than-zero, while two real per-pod deltas still sum.
func TestSeriesLockWaitPoisoning(t *testing.T) {
	b0 := minuteBucket(seriesNow.Add(-time.Hour))
	allReal := fpNamed("11")  // 10 + 20 ms, both real
	poisoned := fpNamed("22") // the sentinel and a real 5.0
	allSentinel := fpNamed("33")

	s := newSeriesStore(t,
		seedOn(allReal, "pg-1", "db1", "appuser", b0, 1, 10, 10, 1, 0, 10),
		seedOn(allReal, "pg-2", "db1", "appuser", b0, 2, 20, 20, 2, 0, 20),
		seedOn(poisoned, "pg-1", "db1", "appuser", b0, 1, 10, 10, 1, 0, noLockWait),
		seedOn(poisoned, "pg-2", "db1", "appuser", b0, 2, 20, 20, 2, 0, 5.0),
		seedOn(allSentinel, "pg-1", "db1", "appuser", b0, 1, 10, 10, 1, 0, noLockWait),
		seedOn(allSentinel, "pg-2", "db1", "appuser", b0, 2, 20, 20, 2, 0, noLockWait),
	)

	out := askAll(t, s, allReal, poisoned, allSentinel)
	entries := entriesByFingerprint(out)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	t.Run("two real rows sum", func(t *testing.T) {
		p := entries[allReal].Points[0]
		if !sameFloatP(p.LockTimeSeconds, 0.03) {
			t.Errorf("lockTimeSeconds = %v, want 0.03 (10 + 20 ms / 1000)", p.LockTimeSeconds)
		}
	})

	// The discriminating case: the naive SUM(-1, 5.0) is 4.0, which divided is
	// 0.004 — a plausible-looking duration. The CASE answers -1 instead, and the
	// rest of the point still sums, because the sentinel is a code on one column
	// and not a verdict on the row.
	t.Run("one sentinel poisons the minute, not the point", func(t *testing.T) {
		p := entries[poisoned].Points[0]
		if p.LockTimeSeconds != nil {
			t.Errorf("lockTimeSeconds = %v, want null (R8.3: the -1 code leaves the store as null, not as a number; the naive SUM gives 0.004)", *p.LockTimeSeconds)
		}
		if !sameFloat(p.ExecutionTimeSeconds, 0.03) || !sameFloat(p.Rows, 30) {
			t.Errorf("the other fields stopped summing: %v s / %v rows, want 0.03 s / 30 rows",
				p.ExecutionTimeSeconds, p.Rows)
		}
	})

	// All-sentinel is the case the division would otherwise turn into -0.001: a
	// code divided by a unit conversion is no longer the code.
	t.Run("the sentinel passes through undivided", func(t *testing.T) {
		p := entries[allSentinel].Points[0]
		if p.LockTimeSeconds != nil {
			t.Errorf("lockTimeSeconds = %v, want null, not -1 and not -0.001 (R8.3: the code leaves the store as null)", *p.LockTimeSeconds)
		}
	})
}

// TestSeriesPointConversion tests seriesRow.point on its own — the pure-Go half,
// where msPerSecond exists precisely so this is testable.
//
// The doc's stated fear is "an unlabelled 1000 next to a Rows that is correctly not
// divided": two of the six fields are durations and four are not, so the test names
// each field's expectation rather than round-tripping the struct.
func TestSeriesPointConversion(t *testing.T) {
	b := minuteBucket(seriesNow)

	t.Run("every field", func(t *testing.T) {
		p := seriesRow{
			fingerprint:  fpNamed("ab"),
			bucket:       b,
			calls:        7,
			totalExecMs:  1500,
			rowsReturned: 30,
			sharedHit:    7,
			sharedRead:   2,
			lockWaitMs:   250,
		}.point()

		if !p.Timestamp.Equal(timeFromUnix(b)) {
			t.Errorf("timestamp = %s, want %s", p.Timestamp, timeFromUnix(b))
		}
		if !sameFloat(p.ExecutionTimeSeconds, 1.5) {
			t.Errorf("executionTimeSeconds = %v, want 1.5 (1500 ms / 1000)", p.ExecutionTimeSeconds)
		}
		if !sameFloatP(p.LockTimeSeconds, 0.25) {
			t.Errorf("lockTimeSeconds = %v, want 0.25 (250 ms / 1000)", p.LockTimeSeconds)
		}
		// The three counters pass through: dividing Rows is the bug the named
		// const exists to prevent.
		if !sameFloat(p.Rows, 30) {
			t.Errorf("rows = %v, want 30 (a count, not divided by 1000)", p.Rows)
		}
		if !sameFloat(p.SharedBlocksHit, 7) || !sameFloat(p.SharedBlocksRead, 2) {
			t.Errorf("sharedBlocksHit/Read = %v/%v, want 7/2", p.SharedBlocksHit, p.SharedBlocksRead)
		}
	})

	t.Run("the sentinel is not divided", func(t *testing.T) {
		p := seriesRow{
			fingerprint: fpNamed("ab"), bucket: b,
			totalExecMs: 1500, rowsReturned: 30, lockWaitMs: noLockWait,
		}.point()
		if p.LockTimeSeconds != nil {
			t.Errorf("lockTimeSeconds = %v, want null, not -1 and not -0.001 (R8.3: the code leaves the store as null)", *p.LockTimeSeconds)
		}
		if !sameFloat(p.ExecutionTimeSeconds, 1.5) {
			t.Errorf("executionTimeSeconds = %v, want 1.5 — the sentinel is one field's problem", p.ExecutionTimeSeconds)
		}
	})

	// bucket 0 is timeFromUnix's zero-time branch, reachable here because a
	// zero-valued seriesRow is a legal thing to convert.
	t.Run("the zero row", func(t *testing.T) {
		p := seriesRow{}.point()
		if !p.Timestamp.IsZero() {
			t.Errorf("timestamp = %s, want the zero time (bucket 0 is timeFromUnix's empty case)", p.Timestamp)
		}
		if p.Rows != 0 || p.SharedBlocksHit != 0 || p.SharedBlocksRead != 0 {
			t.Errorf("counters = %v/%v/%v, want 0/0/0", p.Rows, p.SharedBlocksHit, p.SharedBlocksRead)
		}
		if p.ExecutionTimeSeconds != 0 {
			t.Errorf("executionTimeSeconds = %v, want 0", p.ExecutionTimeSeconds)
		}
	})
}

// TestSeriesWindowBounds pins the window's two edges.
//
// The comparison is `r.bucket >= ? AND r.bucket <= ?` on raw seconds, so both ends
// are inclusive and the boundary is the second, not the minute: a start at
// 12:00:30 excludes the 12:00:00 row, which is what a range preset (now-duration,
// so almost always mid-minute) actually asks for. An implementation that truncated
// the window to the minute would return one more point here, and the two readings
// differ by the partial minute the preset's own clock says hasn't finished.
func TestSeriesWindowBounds(t *testing.T) {
	fp := fpNamed("ab")
	// Six minutes, -4m … +1m, with rows 1…6 so the answer names the minutes it
	// kept by their rows value.
	minutes := []time.Duration{-4 * time.Minute, -3 * time.Minute, -2 * time.Minute,
		-1 * time.Minute, 0, 1 * time.Minute}
	var rows []seriesSeed
	for i, d := range minutes {
		rows = append(rows, seed(fp, minuteBucket(seriesNow.Add(d)),
			int64(i+1), float64(i+1)*10, float64(i+1), 1, 0, 5))
	}
	s := newSeriesStore(t, rows...)

	cases := []struct {
		name  string
		start time.Time
		end   time.Time
		want  []float64
	}{
		{
			name:  "both ends inclusive",
			start: seriesNow.Add(-3 * time.Minute),
			end:   seriesNow,
			want:  []float64{2, 3, 4, 5},
		},
		{
			name:  "a mid-minute start drops its own minute",
			start: seriesNow.Add(-3*time.Minute + 30*time.Second),
			end:   seriesNow,
			want:  []float64{3, 4, 5},
		},
		{
			name:  "a mid-minute end keeps its own minute",
			start: seriesNow.Add(-3 * time.Minute),
			end:   seriesNow.Add(30 * time.Second),
			want:  []float64{2, 3, 4, 5},
		},
		{
			name:  "a one-second window is one minute",
			start: seriesNow.Add(-2 * time.Minute),
			end:   seriesNow.Add(-2 * time.Minute),
			want:  []float64{3},
		},
		{
			// The list route clips a reversed pair; this one takes the caller's
			// window as given, and the honest answer to from > to is no rows.
			name:  "a reversed pair answers empty",
			start: seriesNow.Add(time.Minute),
			end:   seriesNow.Add(-4 * time.Minute),
			want:  nil,
		},
		{
			name:  "a window wider than the data returns all of it",
			start: seriesNow.Add(-10 * time.Minute),
			end:   seriesNow.Add(10 * time.Minute),
			want:  []float64{1, 2, 3, 4, 5, 6},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := askWindow(t, s, []string{fp}, tc.start, tc.end)
			if len(out) != 1 {
				t.Fatalf("got %d entries, want 1", len(out))
			}
			if got := pointRows(out[0]); !equalFloats(got, tc.want) {
				t.Errorf("points rows = %v, want %v", got, tc.want)
			}
		})
	}

	// The ORDER BY's half: points arrive oldest-first, which is the order the
	// sparkline draws and the order the Points slice keeps.
	t.Run("points are oldest-first", func(t *testing.T) {
		out := askWindow(t, s, []string{fp}, seriesNow.Add(-10*time.Minute), seriesNow.Add(10*time.Minute))
		if len(out) != 1 || len(out[0].Points) != 6 {
			t.Fatalf("got %+v, want one entry with six points", out)
		}
		for i, p := range out[0].Points {
			want := timeFromUnix(minuteBucket(seriesNow.Add(minutes[i])))
			if !p.Timestamp.Equal(want) {
				t.Errorf("point %d timestamp = %s, want %s", i, p.Timestamp, want)
			}
		}
	})
}

// TestSeriesBucketKind pins `r.bucket_kind = 'minute'`.
//
// query_rollup's PK includes bucket_kind, so an hour row and a minute row can sit
// at the same bucket with different numbers, and the only thing that keeps the
// series route from mixing them is this one WHERE line. topn_test.go has the
// equivalent test for the list route (TestQueryListWindowAndBucketKind); it is
// duplicated here because the two routes have two SQL strings, and a filter that
// exists in one and not the other is exactly what a shared test would hide.
func TestSeriesBucketKind(t *testing.T) {
	b0 := minuteBucket(seriesNow.Add(-time.Hour))
	minuteFP := fpNamed("ab")
	hourOnlyFP := fpNamed("cd")

	s := newSeriesStore(t,
		seed(minuteFP, b0, 3, 30, 30, 1, 0, 5),
		seedKind(minuteFP, "hour", b0, 99, 990, 990, 9, 9, 90),
		// A fingerprint whose only row is an hour one: the filter's answer is no
		// points at all, not the hour row's numbers.
		seedKind(hourOnlyFP, "hour", b0, 77, 770, 770, 7, 7, 70),
	)

	t.Run("the minute row answers, the hour row is invisible", func(t *testing.T) {
		out := askAll(t, s, minuteFP)
		if len(out) != 1 || len(out[0].Points) != 1 {
			t.Fatalf("got %+v, want one entry with one point", out)
		}
		p := out[0].Points[0]
		if !sameFloat(p.Rows, 30) || !sameFloat(p.ExecutionTimeSeconds, 0.03) {
			t.Errorf("point = %v rows / %v s, want 30 rows / 0.03 s (the hour row's 990 would make it 1020)",
				p.Rows, p.ExecutionTimeSeconds)
		}
	})

	t.Run("an hour-only fingerprint has no points", func(t *testing.T) {
		out := askAll(t, s, hourOnlyFP)
		if len(out) != 1 {
			t.Fatalf("got %d entries, want 1", len(out))
		}
		if len(out[0].Points) != 0 {
			t.Errorf("points = %v, want none — the row exists but is not a minute", pointRows(out[0]))
		}
	})
}

// TestSeriesOptionalScopeFilters covers the three optional WHERE columns.
//
// One fingerprint, four rows, one per (namespace, cluster, datname) combination,
// with calls 1/2/4/8. Binary weights mean every subset has a unique sum, so the
// table's want column identifies exactly which filters were applied — a query that
// ignored `cluster=` answers 7 where the answer is 3, and no case in the table
// shares either number.
func TestSeriesOptionalScopeFilters(t *testing.T) {
	fp := fpNamed("ab")
	b0 := minuteBucket(seriesNow.Add(-time.Hour))
	// Four rows, four distinct PKs. seedOn cannot vary the namespace, and two rows
	// sharing (fingerprint, namespace, cluster, pod, datname, usename, kind,
	// bucket) is one row — INSERT OR REPLACE would silently fold staging's row
	// into prod's and every want below would be off by that row's numbers.
	s := newSeriesStore(t,
		seriesSeed{fp: fp, namespace: "prod", cluster: "pg", pod: "pg-1", datname: "db1",
			usename: "appuser", kind: "minute", bucket: b0, calls: 1, totalMs: 10, rows: 10, hit: 1, lockMs: 5},
		seriesSeed{fp: fp, namespace: "prod", cluster: "pg", pod: "pg-1", datname: "db2",
			usename: "appuser", kind: "minute", bucket: b0, calls: 2, totalMs: 20, rows: 20, hit: 1, lockMs: 5},
		seriesSeed{fp: fp, namespace: "prod", cluster: "pg2", pod: "pg-1", datname: "db1",
			usename: "appuser", kind: "minute", bucket: b0, calls: 4, totalMs: 40, rows: 40, hit: 1, lockMs: 5},
		seriesSeed{fp: fp, namespace: "staging", cluster: "pg", pod: "pg-1", datname: "db1",
			usename: "appuser", kind: "minute", bucket: b0, calls: 8, totalMs: 80, rows: 80, hit: 1, lockMs: 5},
	)

	cases := []struct {
		namespace, cluster, datname string
		want                        float64
	}{
		{"", "", "", 15},    // everything
		{"prod", "", "", 7}, // 1 + 2 + 4
		{"", "pg", "", 11},  // 1 + 2 + 8
		{"", "", "db1", 13}, // 1 + 4 + 8
		{"prod", "pg", "", 3},
		{"prod", "", "db1", 5}, // 1 + 4
		{"", "pg", "db1", 9},   // 1 + 8
		{"prod", "pg", "db1", 1},
		{"staging", "pg", "db1", 8},
	}
	for _, tc := range cases {
		t.Run(tc.namespace+"|"+tc.cluster+"|"+tc.datname, func(t *testing.T) {
			out := ask(t, s, []string{fp}, tc.namespace, tc.cluster, tc.datname)
			if len(out) != 1 {
				t.Fatalf("got %d entries, want 1", len(out))
			}
			if len(out[0].Points) != 1 {
				t.Fatalf("got %d points, want 1 (all four rows share one minute)", len(out[0].Points))
			}
			// The fixture sets rows = calls × 10 on all four rows, so the want
			// column can be written once as calls and read back off a field the
			// duration division cannot shift.
			got := out[0].Points[0].Rows
			want := tc.want * 10
			if !sameFloat(got, want) {
				t.Errorf("rows = %v, want %v (calls %v × 10)", got, want, tc.want)
			}
			// The entry's datname label is the request's, so an unfiltered query
			// that folded two databases answers with an empty one — the label
			// set is what the request can state, not what the rows contain.
			if out[0].Datname != tc.datname {
				t.Errorf("datname label = %q, want %q", out[0].Datname, tc.datname)
			}
		})
	}
}
