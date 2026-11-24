package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The fixture's two statements. Every assertion below needs the fingerprint each
// one produces, and fpOfSQL (retention_test.go:23) is the package's existing way
// to name it: it is the same query.Normalize call PutSnapshot makes, so the test
// does not hardcode a hash the normalizer owns (the reason topn_test.go:90's
// oneFingerprint exists) and does not re-test the normalizer a second time.
const (
	pqSQLA = "SELECT id FROM invoices WHERE order_id = 17"
	pqSQLB = "SELECT count(*) FROM orders"
)

// pqSource is the fixture source with its cluster named.
//
// testSource() and sourceOn(pod, db) fix the cluster at "pg", and R9.5's window
// is per cluster, so a fixture with one cluster cannot tell a per-cluster top-N
// from a global one. This starts from testSource() rather than rebuilding the
// collector.Target, so the address, source and PG major stay the fixture's one
// spelling of them.
func pqSource(namespace, cluster, pod, db string) QuerySource {
	s := testSource()
	s.Namespace = namespace
	s.Cluster = cluster
	s.Pod = pod
	s.Datname = db
	return s
}

// pqRow is a StatementRow with all four exported counters set, because rowFor
// derives rows_returned from calls and this family exports both — in a fixture
// where rows is calls×10, a render that read the calls column for the rows
// member still passes.
func pqRow(queryid, sql string, calls int64, totalMs, rows, hit, read float64) StatementRow {
	return StatementRow{
		QueryID:          queryid,
		SQL:              sql,
		Usename:          "appuser",
		Calls:            calls,
		TotalExecTimeMs:  totalMs,
		RowsReturned:     rows,
		SharedBlocksHit:  hit,
		SharedBlocksRead: read,
	}
}

// pqPut stores one snapshot of rows from src.
func pqPut(t *testing.T, s *Store, src QuerySource, at, reset time.Time, rows ...StatementRow) {
	t.Helper()
	if err := s.PutSnapshot(context.Background(), Snapshot{
		Source:     src,
		CapturedAt: at,
		IngestedAt: at.Add(2 * time.Second),
		StatsReset: reset,
		Rows:       rows,
	}); err != nil {
		t.Fatalf("PutSnapshot(%s/%s/%s): %v", src.Namespace, src.Cluster, src.Pod, err)
	}
}

// pqFixture is the store the golden renders: two clusters, three pods, two
// fingerprints.
//
//	prod/pg  pg-1 appdb  A  10 calls  1500 ms  rows 20  hit 5  read 3   qid 1111111111
//	prod/pg  pg-1 appdb  B   4 calls   400 ms  rows  8  hit 1  read 0   qid 2222222222
//	prod/pg  pg-2 appdb  A   6 calls   600 ms  rows 12  hit 2  read 1   qid 1111111111
//	prod/pg2 pg2-1 appdb A   2 calls   250 ms  rows  4  hit 0  read 7   qid 3333333333
//
// The numbers are chosen so the ranking has one answer per cluster and the two
// clusters are not symmetric: pg's busiest is A at 1500 ms and pg2's only series
// is A at 250 ms, so a render that ranked globally instead of per cluster would
// drop pg2's row at N=1 and every golden here would change.
func pqFixture(t *testing.T) *Store {
	t.Helper()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	epoch := now.Add(-720 * time.Hour)
	at := now.Add(-time.Minute)

	pqPut(t, s, pqSource("prod", "pg", "pg-1", "appdb"), at, epoch,
		pqRow("1111111111", pqSQLA, 10, 1500, 20, 5, 3),
		pqRow("2222222222", pqSQLB, 4, 400, 8, 1, 0),
	)
	pqPut(t, s, pqSource("prod", "pg", "pg-2", "appdb"), at, epoch,
		pqRow("1111111111", pqSQLA, 6, 600, 12, 2, 1),
	)
	pqPut(t, s, pqSource("prod", "pg2", "pg2-1", "appdb"), at, epoch,
		pqRow("3333333333", pqSQLA, 2, 250, 4, 0, 7),
	)
	return s
}

// pqLine is one sample line: the metric name, the six labels in key order, and
// the value.
//
// The label formatting is spelled here a second time on purpose. The render builds
// `{k="v",...}` sorted by key from a fixed array; this builds the same shape by
// writing the six labels in order in a format string. Two independent spellings
// of the format is what makes the comparison able to fail — an expectation
// assembled by calling the code under test is the S59 class this package already
// filed once ("comparing a value to the constant it was assigned from restates
// the assignment").
func pqLine(metric, cluster, fp, role, ns, pod, qid, value string) string {
	return fmt.Sprintf("%s{cluster=%q,fingerprint=%q,instance_role=%q,namespace=%q,pod=%q,queryid=%q} %s",
		metric, cluster, fp, role, ns, pod, qid, value)
}

// pqFamily is one family's three-part block: its HELP line, its TYPE line, and
// the sample lines under it.
func pqFamily(name, help string, lines ...string) string {
	out := "# HELP " + name + " " + help + "\n# TYPE " + name + " counter\n"
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// The four members' names and HELP text, written out here rather than read from
// perQueryMembers. The names are the ones requirements.md's R9.5 row and
// collector/naming.go's table give them; the HELP strings are copied from the
// table so a render that emitted the right name with the wrong help, or the right
// help under a wrong name, fails.
const (
	pqHelpExec = "Cumulative execution time of the statement, from pg_stat_statements (R9.5 per-query family)."
	pqHelpRows = "Rows the statement returned, from pg_stat_statements (R9.5 per-query family)."
	pqHelpHit  = "Shared blocks the statement found in the cache (R9.5 per-query family)."
	pqHelpRead = "Shared blocks the statement read from disk (R9.5 per-query family)."
)

// pqBody is the whole expected exposition for a store whose series are the four
// rows pqFixture writes, at any N >= 3, for any prefix.
//
// The values are the check the names alone cannot be. The execution_time lines
// read 1.5, 0.6, 0.4 and 0.25 where the columns hold 1500, 600, 400 and 250 —
// the millisecond-to-second division collector/statements.go:652 names as the
// failure mode ("a collector that skipped this would export a cluster that spent
// 1.5 s in one query as pgsi_query_execution_time_seconds_total=1500") — and the
// rows/hit/read lines are not divided, so a render that divided every member
// would fail here rather than pass.
func pqBody(prefix, fpA, fpB string) string {
	a1 := func(base, v string) string {
		return pqLine(prefix+base, "pg", fpA, "primary", "prod", "pg-1", "1111111111", v)
	}
	a2 := func(base, v string) string {
		return pqLine(prefix+base, "pg", fpA, "primary", "prod", "pg-2", "1111111111", v)
	}
	b1 := func(base, v string) string {
		return pqLine(prefix+base, "pg", fpB, "primary", "prod", "pg-1", "2222222222", v)
	}
	c1 := func(base, v string) string {
		return pqLine(prefix+base, "pg2", fpA, "primary", "prod", "pg2-1", "3333333333", v)
	}

	return pqFamily(prefix+"query_execution_time_seconds_total", pqHelpExec,
		a1("query_execution_time_seconds_total", "1.5"),
		a2("query_execution_time_seconds_total", "0.6"),
		b1("query_execution_time_seconds_total", "0.4"),
		c1("query_execution_time_seconds_total", "0.25")) +
		pqFamily(prefix+"query_rows_total", pqHelpRows,
			a1("query_rows_total", "20"),
			a2("query_rows_total", "12"),
			b1("query_rows_total", "8"),
			c1("query_rows_total", "4")) +
		pqFamily(prefix+"query_shared_blocks_hit_total", pqHelpHit,
			a1("query_shared_blocks_hit_total", "5"),
			a2("query_shared_blocks_hit_total", "2"),
			b1("query_shared_blocks_hit_total", "1"),
			c1("query_shared_blocks_hit_total", "0")) +
		pqFamily(prefix+"query_shared_blocks_read_total", pqHelpRead,
			a1("query_shared_blocks_read_total", "3"),
			a2("query_shared_blocks_read_total", "1"),
			b1("query_shared_blocks_read_total", "0"),
			c1("query_shared_blocks_read_total", "7"))
}

// TestRenderPerQueryExact pins the whole body for a four-series store: the four
// family names, their order, their types, every label set, and every value.
func TestRenderPerQueryExact(t *testing.T) {
	s := pqFixture(t)
	fpA, fpB := fpOfSQL(pqSQLA), fpOfSQL(pqSQLB)

	got, err := s.RenderPerQuery(context.Background(), 50, "")
	if err != nil {
		t.Fatalf("RenderPerQuery(50): %v", err)
	}
	if want := pqBody("pgsi_", fpA, fpB); got != want {
		t.Errorf("RenderPerQuery(50) body:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderPerQueryPrefix is R9.2's metrics.prefix reaching the family.
//
// The prefix is applied at render time and never baked into a name
// (collector/naming.go's "The prefix stays out" rule), so a non-default prefix
// renames all four families and changes nothing else. The empty string is the
// documented way to ask for collector.DefaultPrefix, which the golden above
// covers; this one passes a prefix that is not the default, so the assertion
// cannot pass by the code ignoring the parameter and using "pgsi_" anyway.
func TestRenderPerQueryPrefix(t *testing.T) {
	s := pqFixture(t)
	fpA, fpB := fpOfSQL(pqSQLA), fpOfSQL(pqSQLB)

	got, err := s.RenderPerQuery(context.Background(), 50, "metrics_")
	if err != nil {
		t.Fatalf("RenderPerQuery(50, metrics_): %v", err)
	}
	if want := pqBody("metrics_", fpA, fpB); got != want {
		t.Errorf("RenderPerQuery(50, \"metrics_\") body:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderPerQueryTopN covers R9.5's range and its disable in one table,
// because the five cases are the same call with five numbers and only the
// boundaries can tell the range from a guess.
//
// 0 is R9.5's "N=0 disables the family" and must be legal, not an error; -1 and
// 1001 are outside the documented 0–1000 and must error rather than clamp (the
// collector clamps in WithTopN because its constructor already accepted the
// value; this call has no constructor to absolve it); 1000 is the top of the
// range and must be accepted. The 1000 case renders the same body as 50, which is
// also the check that a larger N does not silently change the ranking.
func TestRenderPerQueryTopN(t *testing.T) {
	s := pqFixture(t)
	fpA, fpB := fpOfSQL(pqSQLA), fpOfSQL(pqSQLB)
	body := pqBody("pgsi_", fpA, fpB)
	ctx := context.Background()

	for _, tc := range []struct {
		n      int
		want   string
		wantOK bool
	}{
		{n: -1, wantOK: false},
		{n: 0, want: "", wantOK: true},
		{n: 50, want: body, wantOK: true},
		{n: 1000, want: body, wantOK: true},
		{n: 1001, wantOK: false},
	} {
		got, err := s.RenderPerQuery(ctx, tc.n, "")
		if tc.wantOK && err != nil {
			t.Errorf("RenderPerQuery(%d): unexpected error %v", tc.n, err)
			continue
		}
		if !tc.wantOK {
			if err == nil {
				t.Errorf("RenderPerQuery(%d) = %q, want an error (R9.5's range is 0..1000)", tc.n, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("RenderPerQuery(%d) body:\n got:\n%s\nwant:\n%s", tc.n, got, tc.want)
		}
	}
}

// TestRenderPerQueryTopNOnePerCluster is the "per cluster" half of R9.5:397's
// "default N=50 per cluster (see §5 — metrics.topN is per cluster, not per
// database)".
//
// At N=1 the fixture must yield one series per cluster — pg's busiest (A on
// pg-1, 1.5 s) and pg2's (A on pg2-1, 0.25 s) — not one series for the whole
// store and not one per pod. A render that partitioned by pod would emit three
// lines per family at N=1 (pg-1 has two fingerprints), and one that ignored the
// partition entirely would emit one line for the whole store and drop pg2's.
func TestRenderPerQueryTopNOnePerCluster(t *testing.T) {
	s := pqFixture(t)
	fpA := fpOfSQL(pqSQLA)
	ctx := context.Background()

	got, err := s.RenderPerQuery(ctx, 1, "")
	if err != nil {
		t.Fatalf("RenderPerQuery(1): %v", err)
	}

	want := pqFamily("pgsi_query_execution_time_seconds_total", pqHelpExec,
		pqLine("pgsi_query_execution_time_seconds_total", "pg", fpA, "primary", "prod", "pg-1", "1111111111", "1.5"),
		pqLine("pgsi_query_execution_time_seconds_total", "pg2", fpA, "primary", "prod", "pg2-1", "3333333333", "0.25")) +
		pqFamily("pgsi_query_rows_total", pqHelpRows,
			pqLine("pgsi_query_rows_total", "pg", fpA, "primary", "prod", "pg-1", "1111111111", "20"),
			pqLine("pgsi_query_rows_total", "pg2", fpA, "primary", "prod", "pg2-1", "3333333333", "4")) +
		pqFamily("pgsi_query_shared_blocks_hit_total", pqHelpHit,
			pqLine("pgsi_query_shared_blocks_hit_total", "pg", fpA, "primary", "prod", "pg-1", "1111111111", "5"),
			pqLine("pgsi_query_shared_blocks_hit_total", "pg2", fpA, "primary", "prod", "pg2-1", "3333333333", "0")) +
		pqFamily("pgsi_query_shared_blocks_read_total", pqHelpRead,
			pqLine("pgsi_query_shared_blocks_read_total", "pg", fpA, "primary", "prod", "pg-1", "1111111111", "3"),
			pqLine("pgsi_query_shared_blocks_read_total", "pg2", fpA, "primary", "prod", "pg2-1", "3333333333", "7"))

	if got != want {
		t.Errorf("RenderPerQuery(1) body:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderPerQueryGroupsSameLabels is the exposition-validity rule the family's
// label set forces: the store's PK is finer than the family's six labels by
// (datname, usename), so two rows that share the six labels must render as ONE
// series with summed counters.
//
// Two databases, one statement, one queryid, 1500 ms and 500 ms. Emitting the two
// rows as two lines gives two samples with identical label sets — D20's S8 class,
// "an invalid exposition whose surviving value is whichever row the driver
// returned last" — and promtool rejects it. The summed value (2 s, 30 rows, 8
// hits, 4 reads) is the point: a render that grouped but took MIN or MAX instead
// of SUM would render 0.5 or 1.5.
func TestRenderPerQueryGroupsSameLabels(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	epoch := now.Add(-720 * time.Hour)
	at := now.Add(-time.Minute)

	pqPut(t, s, pqSource("prod", "pg", "pg-1", "appdb"), at, epoch,
		pqRow("1111111111", pqSQLA, 10, 1500, 20, 5, 3))
	pqPut(t, s, pqSource("prod", "pg", "pg-1", "reportdb"), at, epoch,
		pqRow("1111111111", pqSQLA, 4, 500, 10, 3, 1))

	got, err := s.RenderPerQuery(context.Background(), 50, "")
	if err != nil {
		t.Fatalf("RenderPerQuery(50): %v", err)
	}

	fp := fpOfSQL(pqSQLA)
	want := pqFamily("pgsi_query_execution_time_seconds_total", pqHelpExec,
		pqLine("pgsi_query_execution_time_seconds_total", "pg", fp, "primary", "prod", "pg-1", "1111111111", "2")) +
		pqFamily("pgsi_query_rows_total", pqHelpRows,
			pqLine("pgsi_query_rows_total", "pg", fp, "primary", "prod", "pg-1", "1111111111", "30")) +
		pqFamily("pgsi_query_shared_blocks_hit_total", pqHelpHit,
			pqLine("pgsi_query_shared_blocks_hit_total", "pg", fp, "primary", "prod", "pg-1", "1111111111", "8")) +
		pqFamily("pgsi_query_shared_blocks_read_total", pqHelpRead,
			pqLine("pgsi_query_shared_blocks_read_total", "pg", fp, "primary", "prod", "pg-1", "1111111111", "4"))

	if got != want {
		t.Errorf("RenderPerQuery(50) body:\n got:\n%s\nwant:\n%s", got, want)
	}
	// The count is the S8 assertion stated separately from the string: eight lines
	// carrying `fingerprint=` is the invalid exposition, four is the grouped one.
	if n := strings.Count(got, "fingerprint="); n != 4 {
		t.Errorf("rendered %d series lines, want 4 (one per family): two rows sharing a label set would make 8", n)
	}
}

// TestRenderPerQueryEmptyStore is the absence rule: a store with no rows exports
// no family, not a HELP/TYPE pair beside a value the process never measured.
func TestRenderPerQueryEmptyStore(t *testing.T) {
	s := openMem(t, time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))

	got, err := s.RenderPerQuery(context.Background(), 50, "")
	if err != nil {
		t.Fatalf("RenderPerQuery(50): %v", err)
	}
	if got != "" {
		t.Errorf("RenderPerQuery(50) on an empty store = %q, want \"\" (a family with no series is absent)", got)
	}
}
