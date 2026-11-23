package collector

// wal_test.go covers the third of the four Phase 2 SQL collectors, and it inherits
// the fixture pattern from database_test.go and settings_test.go: fakeOpener/fakeTx
// are database_test.go's (one fake per interface), fullRow is settings_test.go's
// (every matrix column filled, so a column a test forgets is unparseable rather than
// accidentally right), and the golden sample count is derived from the fixture and
// spelled out in the failure message rather than remembered.
//
// The two tests with no precedent in the other files are TestWALQueriesAreSchemaQualified's
// exact column-list comparison (a wal-specific strengthening: the PG18 column set is
// four columns narrower than the PG17 one, and an absence check is the half a presence
// check cannot do) and TestWALKindOfCoversEveryMatrixColumn, which is what makes the
// datid/datname flag on the type doc enforced rather than merely written down.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract, matching database_test.go's,
// settings_test.go's and replication_test.go's: the collector is a Collector.
var _ Collector = (*WALCollector)(nil)

// newTestWAL builds the collector plus the fake opener and tx it runs on, serving
// the given rows for each view's query.
func newTestWAL(t *testing.T, tgt Target, rows map[pgversion.View][]Row) (*WALCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	projections, err := NewWALCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range projections {
		tx.rows[pv.Query] = rows[pv.view]
	}
	opener := &fakeOpener{tx: tx}
	return NewWALCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener, tx
}

// walProjectionFor pairs one view with one major's projection, for the builder tests
// that need a viewProjection and nothing else. It calls Project directly rather than
// asking a collector for its projections, for the reason replication_test.go's
// viewProjectionFor gives: the builder tests then check the builder against the
// spec's own answer instead of against the collector that is also under test.
func walProjectionFor(t *testing.T, view pgversion.View, major int) viewProjection {
	t.Helper()
	p, ok := Project(view, major, QuerySpec{})
	if !ok {
		t.Fatalf("Project(%s, %d): not ok", view, major)
	}
	return viewProjection{view: view, Projection: p}
}

// walRow builds a pg_stat_wal row with every PG17 matrix column filled (via fullRow)
// and the columns this collector reads set. The eight metric columns have to be
// overridden because fullRow's default value is "x", which parses as nothing; the
// stats_reset override is the per-database epoch the builder takes a max over.
func walRow(t *testing.T, over map[string]string) Row {
	t.Helper()
	return fullRow(t, pgversion.ViewStatWAL, over)
}

// archRow builds a pg_stat_archiver row with every matrix column filled and the two
// counters set; the four text columns get explicit values so the test can assert they
// produce no families.
func archRow(t *testing.T, archived, failed string) Row {
	t.Helper()
	return fullRow(t, pgversion.ViewStatArchiver, map[string]string{
		"archived_count":     archived,
		"failed_count":       failed,
		"last_archived_wal":  "000000010000000000000001",
		"last_archived_time": "2026-11-22 09:55:00+00",
		"last_failed_wal":    "",
		"last_failed_time":   "",
		"stats_reset":        "2026-01-01 00:00:00+00",
	})
}

// walEpoch is the newest stats_reset in the fixture: two of the three database rows
// reset earlier (2026-01-01) or never (NULL), and the builder's rule is that the
// newest among the contributing rows is the summed family's epoch.
var walEpoch = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// walTestRows is the one fixture the collect-path tests share: three pg_stat_wal rows
// (one per database, which is what the view returns) and one pg_stat_archiver row
// (cluster-wide, which is all the view ever returns).
//
// The three WAL rows are the sum the golden test asserts: records 1000+2000+0,
// fpi 50+10+0, bytes 1234+2048+0, buffers_full 3+1+0, write 10+20+0, sync 5+10+0,
// write_time 25+50+0, sync_time 12+20+0.
func walTestRows(t *testing.T) map[pgversion.View][]Row {
	t.Helper()
	return map[pgversion.View][]Row{
		pgversion.ViewStatWAL: {
			walRow(t, map[string]string{
				"wal_records": "1000", "wal_fpi": "50", "wal_bytes": "1234", "wal_buffers_full": "3",
				"wal_write": "10", "wal_sync": "5", "wal_write_time": "25", "wal_sync_time": "12",
				"stats_reset": "2026-01-01 00:00:00+00",
			}),
			walRow(t, map[string]string{
				"wal_records": "2000", "wal_fpi": "10", "wal_bytes": "2048", "wal_buffers_full": "1",
				"wal_write": "20", "wal_sync": "10", "wal_write_time": "50", "wal_sync_time": "20",
				"stats_reset": "2026-06-01 00:00:00+00",
			}),
			walRow(t, map[string]string{
				"wal_records": "0", "wal_fpi": "0", "wal_bytes": "0", "wal_buffers_full": "0",
				"wal_write": "0", "wal_sync": "0", "wal_write_time": "0", "wal_sync_time": "0",
				// A NULL stats_reset: the value parses to the zero time and must not
				// win the epoch max.
				"stats_reset": "",
			}),
		},
		pgversion.ViewStatArchiver: {archRow(t, "42", "3")},
	}
}

func TestWALDescriptor(t *testing.T) {
	c, _, _ := newTestWAL(t, tgtPG("pg", "pg-1", "primary"), walTestRows(t))
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorWAL {
		t.Errorf("Name = %q, want %q (§3.3's row name, and the R2.2 `collector` label)", d.Name, CollectorWAL)
	}
	if d.Interval != WALInterval {
		t.Errorf("Interval = %s, want %s (§3.3's 60 s)", d.Interval, WALInterval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %s, want %s (§3.3's row lists two S2 views and one S3 source; Descriptor.Source "+
			"declares one, and the S3 half is the instance-metrics collector's — see the comment at the field)",
			d.Source, SourceSQL)
	}
	if d.Cost != CostStandard {
		t.Errorf("Cost = %s, want %s (collector.go's CostStandard doc names this collector)", d.Cost, CostStandard)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != walViewGroup {
		t.Errorf("ViewGroups = %v, want [%s] (R2.7: one entry per view group, and this collector has one)",
			d.ViewGroups, walViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; the tuning knob is R2.7's statement_timeout)", d.Timeout)
	}

	// The applicability half is §3.3's "(14+)" derived from the matrix rather than
	// typed in: pg_stat_wal exists on 14-18 and pg_stat_archiver on 13-18, so
	// AppliesToViews' intersection is 14-18. PG13 is the case that a hand-written
	// "13-18" range would get wrong, and the one R13.9 makes free (PG13 and PG14 are
	// fixture-tested SQL-layer rows only on CNPG 1.30).
	for _, major := range pgversion.Majors() {
		want := major >= 14
		if got := d.Applies(major); got != want {
			t.Errorf("Applies(%d) = %v, want %v (pg_stat_wal is 14+, pg_stat_archiver is 13-18, "+
				"so the intersection of the two views this collector reads is 14-18)", major, got, want)
		}
	}
	for _, major := range []int{12, 19} {
		if d.Applies(major) {
			t.Errorf("Applies(%d) = true, want false", major)
		}
	}
}

// selectList returns the exact column tokens a projection's SELECT list names, so a
// test can compare a query's column set instead of substring-guessing at it.
func selectList(t *testing.T, query string) []string {
	t.Helper()
	head, _, ok := strings.Cut(query, " FROM ")
	if !ok {
		t.Fatalf("query %q has no FROM clause", query)
	}
	return strings.Split(strings.TrimPrefix(head, "SELECT "), ", ")
}

// TestWALQueriesAreSchemaQualified is R2.5 and R2.4 checked on every major, with the
// comparison exact rather than per-column: the SELECT list must equal the matrix's
// column set for that view and major plus the now() alias, in order.
//
// The exactness is the point. replication_test.go's equivalent checks that every
// declared column appears, which cannot see a column that appears on a major where it
// should not — and pg_stat_wal is the one view in Phase 2 where the set *shrinks*
// inside the supported range (PG18 removes wal_write, wal_sync, wal_write_time and
// wal_sync_time, which is R2.4's 9 → 5 case). A substring check is worse than useless
// there: "wal_write" is inside "wal_write_time", so a PG18 query that kept the retired
// columns would satisfy a contains-check for every column the matrix does have.
func TestWALQueriesAreSchemaQualified(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		projections, err := NewWALCollector(nil, SQLConfig{}).projections(tgt)
		if major == 13 {
			// pg_stat_wal has no column set on 13, so the collector refuses the
			// target instead of querying one view and skipping the other.
			if err == nil {
				t.Errorf("PG13: projections = nil error, want a refusal (§3.3's row says 14+)")
			}
			continue
		}
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}
		if len(projections) != len(walViews) {
			t.Fatalf("PG%d: %d projections, want %d", major, len(projections), len(walViews))
		}
		for _, pv := range projections {
			want := " FROM pg_catalog." + string(pv.view)
			if !strings.HasSuffix(pv.Query, want) {
				t.Errorf("PG%d %s: query %q does not end in %q (R2.5)", major, pv.view, pv.Query, want)
			}
			cols, ok := pgversion.Columns(pv.view, major)
			if !ok {
				t.Fatalf("PG%d: Columns(%s, %d) not ok", major, pv.view, major)
			}
			// The want-list appends the *Query* spelling of the source-now
			// column, not the bare SourceNowColumn constant, because Project
			// writes the two halves of a Projection in two different forms
			// (sql.go:261-262): Query gets `now() AS pgsi_source_now`, Columns
			// gets the bare alias `pgsi_source_now`. selectList parses Query, so
			// a want-list built from the constant compares one representation's
			// output against the other's name and fails on every major.
			wantList := append(append([]string{}, cols...), "now() AS "+SourceNowColumn)
			if got := selectList(t, pv.Query); !reflect.DeepEqual(got, wantList) {
				t.Errorf("PG%d %s: SELECT list = %v, want %v (R2.4 says the matrix's column set is "+
					"the projection, and this collector declares no Extras to add to it)",
					major, pv.view, got, wantList)
			}
			if len(pv.Extras) != 0 {
				t.Errorf("PG%d %s: %d Extras, want 0 (every wal family comes from a matrix column; "+
					"an Extra here needs a reason, and the reason is in the type doc)", major, pv.view, len(pv.Extras))
			}
		}
	}
}

// TestWALCollectSamples is the golden sample set for one round on PG17.
//
// Ten, derived from the fixture in walTestRows: pg_stat_wal's nine PG17 columns are
// eight metrics plus stats_reset (an epoch, so not a metric), and the three rows sum
// into the eight families the table names; pg_stat_archiver's seven columns are two
// metrics, two WAL file names and two non-epoch timestamps (all text, so not metrics)
// plus stats_reset, so its one row contributes the two counts.
func TestWALCollectSamples(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, _, _ := newTestWAL(t, tgt, walTestRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(res.Samples) != 10 {
		t.Fatalf("len(Samples) = %d, want 10 (8 summed pg_stat_wal families + 2 pg_stat_archiver counts)\n%s",
			len(res.Samples), dumpSamples(res.Samples))
	}

	byMetric := map[string]Sample{}
	for _, s := range res.Samples {
		byMetric[s.Metric] = s
	}
	for _, tc := range []struct {
		metric string
		value  float64
	}{
		// The sums are 1000+2000+0, 50+10+0, 1234+2048+0, 3+1+0, 10+20+0, 5+10+0,
		// 25+50+0, 12+20+0 — the three fixture rows, added.
		{"wal_records_total", 3000},
		{"wal_fpi_total", 60},
		{"wal_generated_bytes_total", 3282},
		{"wal_buffers_full_total", 4},
		{"wal_write_total", 30},
		{"wal_sync_total", 15},
		{"wal_write_time_seconds_total", 75},
		{"wal_sync_time_seconds_total", 32},
		// The archiver's one row, unsummed because it is the only row the view has.
		{"archiver_archived_count_total", 42},
		{"archiver_failed_count_total", 3},
	} {
		s, ok := byMetric[tc.metric]
		if !ok {
			t.Errorf("no sample for %s\n%s", tc.metric, dumpSamples(res.Samples))
			continue
		}
		if s.Value != tc.value {
			t.Errorf("%s value = %v, want %v", tc.metric, s.Value, tc.value)
		}
		if !s.SourceTime.Equal(sourceNow) {
			t.Errorf("%s SourceTime = %s, want %s (R13.11's instance clock, from the now() column)",
				tc.metric, s.SourceTime, sourceNow)
		}
		if !s.IngestTime.Equal(fixedClock) {
			t.Errorf("%s IngestTime = %s, want %s (R13.11's our-clock, from the injected clock)",
				tc.metric, s.IngestTime, fixedClock)
		}
		// R9.3's contract has no slot for a database name on a cluster aggregate,
		// and the identity half is the scheduler's, so a wal sample carries no labels.
		if len(s.Labels) != 0 {
			t.Errorf("%s labels = %v, want none (R9.3's datname belongs to database series; this is a "+
				"cluster aggregate — see the type doc)", tc.metric, s.Labels)
		}
	}

	// The epoch columns are R3.3's field, not families of their own.
	for _, metric := range []string{"wal_stats_reset_timestamp_seconds", "archiver_stats_reset_timestamp_seconds"} {
		if _, ok := byMetric[metric]; ok {
			t.Errorf("%s exists, want stats_reset in Sample.StatsReset and not as a family", metric)
		}
	}
	// The text columns are R9.1's API half: a WAL file name and two timestamps that
	// are not the view's epoch produce no series.
	for _, metric := range []string{
		"pg_stat_archiver_last_archived_wal",
		"pg_stat_archiver_last_failed_wal",
		"archiver_last_archived_timestamp_seconds",
		"archiver_last_failed_timestamp_seconds",
	} {
		if _, ok := byMetric[metric]; ok {
			t.Errorf("%s exists, want a text column unexported (R9.1: the API carries the text)", metric)
		}
	}
}

// TestWALCollectOneTransactionPerRound is R2.7 as a count: one Begin, one query per
// view, one Commit, and the config that reached the opener carrying R2.7's
// statement_timeout and R2.8's application_name.
func TestWALCollectOneTransactionPerRound(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, opener, tx := newTestWAL(t, tgt, walTestRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if opener.calls != 1 {
		t.Errorf("Begin calls = %d, want 1 (R2.7: one read-only transaction per view group, and this collector has one group)", opener.calls)
	}
	projections, _ := c.projections(tgt)
	if len(tx.queries) != len(walViews) {
		t.Fatalf("queries = %d, want %d (one per view)", len(tx.queries), len(walViews))
	}
	for i, q := range tx.queries {
		if q != projections[i].Query {
			t.Errorf("query %d =\n%s\nwant\n%s", i, q, projections[i].Query)
		}
	}
	if tx.commits != 1 || tx.rollbacks != 0 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/0 on the success path", tx.commits, tx.rollbacks)
	}
	if res.RoundTrips != len(walViews) {
		t.Errorf("RoundTrips = %d, want %d (R13.1 measures a round from this count)", res.RoundTrips, len(walViews))
	}

	cfg := opener.cfgs[0]
	if cfg.StatementTimeout != DefaultStatementTimeout {
		t.Errorf("Begin cfg.StatementTimeout = %s, want %s (R2.7's 5 s reaches the connection, not just the collector)",
			cfg.StatementTimeout, DefaultStatementTimeout)
	}
	if cfg.ApplicationName != ApplicationName {
		t.Errorf("Begin cfg.ApplicationName = %q, want %q (R2.8's self-exclusion name)", cfg.ApplicationName, ApplicationName)
	}
	if opener.targets[0] != tgt {
		t.Errorf("Begin target = %+v, want %+v", opener.targets[0], tgt)
	}
}

// TestWALCollectQueryErrorKeepsRoundTrips is the Collector contract's "on error, the
// returned Result must still carry the RoundTrips it actually issued", on the second
// view, with the first view's samples still returned (D20's S9).
func TestWALCollectQueryErrorKeepsRoundTrips(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	projections, _ := NewWALCollector(nil, SQLConfig{}).projections(tgt)

	tx := &fakeTx{
		rows: map[string][]Row{
			projections[0].Query: {walRow(t, map[string]string{
				"wal_records": "1000", "wal_fpi": "50", "wal_bytes": "1234", "wal_buffers_full": "3",
				"wal_write": "10", "wal_sync": "5", "wal_write_time": "25", "wal_sync_time": "12",
				"stats_reset": "2026-01-01 00:00:00+00",
			})},
		},
		failOn: projections[1].Query,
	}
	opener := &fakeOpener{tx: tx}
	c := NewWALCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the query failure surfaced")
	}
	if res.RoundTrips != 2 {
		t.Errorf("RoundTrips = %d, want 2 (two queries issued before the failure)", res.RoundTrips)
	}
	if len(res.Samples) != 8 {
		t.Errorf("Samples = %d, want 8 (the pg_stat_wal row set survived; the archiver's did not run)\n%s",
			len(res.Samples), dumpSamples(res.Samples))
	}
	if tx.rollbacks != 1 || tx.commits != 0 {
		t.Errorf("rollbacks/commits = %d/%d, want 1/0 (a failed round must not leave a transaction open)",
			tx.rollbacks, tx.commits)
	}
	if !strings.Contains(err.Error(), string(pgversion.ViewStatArchiver)) {
		t.Errorf("error %q does not name the failing view", err)
	}
}

func TestWALCollectCommitError(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, _, tx := newTestWAL(t, tgt, walTestRows(t))
	tx.commitErr = errors.New("fake: connection lost")

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the commit failure surfaced")
	}
	if res.RoundTrips != len(walViews) {
		t.Errorf("RoundTrips = %d, want %d (the queries did run)", res.RoundTrips, len(walViews))
	}
	if len(res.Samples) == 0 {
		t.Error("Samples empty, want the read samples returned alongside the error")
	}
	if tx.commits != 1 || tx.rollbacks != 1 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/1 (a failed COMMIT may leave the transaction open on a pooled connection; the best-effort Rollback is what closes it)",
			tx.commits, tx.rollbacks)
	}
}

func TestWALCollectBeginError(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	opener := &fakeOpener{beginErr: errors.New("fake: connect refused")}
	c := NewWALCollector(opener, SQLConfig{})

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the begin failure surfaced")
	}
	if res.RoundTrips != 0 || len(res.Samples) != 0 {
		t.Errorf("res = %+v, want zero round-trips and no samples (nothing was issued)", res)
	}
	if !strings.Contains(err.Error(), "begin read-only transaction") {
		t.Errorf("error %q does not name the begin step", err)
	}
}

// TestWALCollectRefusesUnknownMajors includes PG13, which is not an unknown major but
// an unsupported one: the matrix has pg_stat_archiver on 13 and not pg_stat_wal, so
// the collector refuses the target rather than running half its view set (R2.7's
// one-transaction rule is what makes "half a result set" the wrong answer).
func TestWALCollectRefusesUnknownMajors(t *testing.T) {
	for _, major := range []int{0, 12, 13, 19} {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		opener := &fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}
		c := NewWALCollector(opener, SQLConfig{})

		res, err := c.Collect(context.Background(), tgt)
		if err == nil {
			t.Errorf("PG%d: Collect = nil error, want a refusal", major)
		}
		if opener.calls != 0 {
			t.Errorf("PG%d: Begin called %d times, want 0 (no SQL to run, so no transaction to open)", major, opener.calls)
		}
		if res.RoundTrips != 0 {
			t.Errorf("PG%d: RoundTrips = %d, want 0", major, res.RoundTrips)
		}
	}
}

// TestWALKindOfCoversEveryMatrixColumn is the test that makes the datid/datname flag
// on the type doc enforced rather than documented.
//
// It walks every column the matrix has for either view on every major and fails on
// kindUnknown. The matrix's pg_stat_wal set is missing datid and datname (real
// PostgreSQL has both), so the day matrix_data.go gains them, this test fails on both
// new columns and the collector has to be updated in the same commit — which is the
// outcome the flag wants. database.go's pg_stat_database arm ends its switch with
// `return kindMetric`, which would instead classify a new datname as a number and
// leave this test green with a pgsi_stat_wal_datname family on the wire.
func TestWALKindOfCoversEveryMatrixColumn(t *testing.T) {
	checked := 0
	for _, view := range walViews {
		for _, major := range pgversion.Majors() {
			cols, ok := pgversion.Columns(view, major)
			if !ok {
				continue // pg_stat_wal has no column set on 13; the collector refuses it.
			}
			for _, col := range cols {
				checked++
				if got := walKindOf(view, col); got == kindUnknown {
					t.Errorf("walKindOf(%s, %s) = unknown on PG%d: the matrix gained a column and "+
						"this file did not notice (see the flag on WALCollector's doc)", view, col, major)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no matrix columns checked, so nothing was checked")
	}
	// The now() alias is every query's own column and not a matrix one, so the loop
	// above cannot reach it; it is classified here.
	if got := walKindOf(pgversion.ViewStatWAL, SourceNowColumn); got != kindSourceNow {
		t.Errorf("walKindOf(pg_stat_wal, %s) = %s, want source_now (R13.11's first timestamp)", SourceNowColumn, got)
	}
	// And a column no view has must stay unknown, or the classifier's default is a lie.
	if got := walKindOf(pgversion.ViewStatWAL, "not_a_column"); got != kindUnknown {
		t.Errorf("walKindOf(pg_stat_wal, not_a_column) = %s, want unknown", got)
	}
}

// TestWALSamples pins the builder on the three rules the end-to-end test can only
// sample: a metric column's value is the sum over the rows that parsed, a column with
// no parseable value in any row produces no family rather than a 0, and the epoch is
// the newest stats_reset among the contributing rows.
func TestWALSamples(t *testing.T) {
	pv17 := walProjectionFor(t, pgversion.ViewStatWAL, 17)

	cases := []struct {
		name   string
		pv     viewProjection
		rows   []Row
		want   map[string]float64
		epoch  time.Time
		source time.Time
	}{
		{
			// Three databases, eight metric columns, one family each.
			name: "three rows summed on PG17",
			pv:   pv17,
			rows: walTestRows(t)[pgversion.ViewStatWAL],
			want: map[string]float64{"wal_records_total": 3000, "wal_fpi_total": 60,
				"wal_generated_bytes_total": 3282, "wal_buffers_full_total": 4,
				"wal_write_total": 30, "wal_sync_total": 15,
				"wal_write_time_seconds_total": 75, "wal_sync_time_seconds_total": 32},
			epoch:  walEpoch,
			source: sourceNow,
		},
		{
			// The PG18 column set is four columns narrower (R2.4's 9 → 5 shrink), so
			// the same builder over the same shape yields four families and the four
			// retired columns produce nothing — not even a 0.
			name: "PG18 set",
			pv:   walProjectionFor(t, pgversion.ViewStatWAL, 18),
			rows: []Row{{
				"wal_records": "7", "wal_fpi": "1", "wal_bytes": "1024", "wal_buffers_full": "0",
				"stats_reset": "2026-06-01 00:00:00+00", SourceNowColumn: fixedSourceNow,
			}},
			want: map[string]float64{"wal_records_total": 7, "wal_fpi_total": 1,
				"wal_generated_bytes_total": 1024, "wal_buffers_full_total": 0},
			epoch:  walEpoch,
			source: sourceNow,
		},
		{
			// The archiver's builder is the same function: one row, so the sum is the
			// value, and the two counters are the only families.
			name:   "archiver, one row",
			pv:     walProjectionFor(t, pgversion.ViewStatArchiver, 17),
			rows:   []Row{archRow(t, "42", "3")},
			want:   map[string]float64{"archiver_archived_count_total": 42, "archiver_failed_count_total": 3},
			epoch:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			source: sourceNow,
		},
		{
			// A column no row answers for has no family. The other columns still do,
			// so this is the case where a 0 would be a measurement of nothing: the
			// fixture's first row has an unparseable wal_fpi and a parseable
			// wal_records, and only the second family exists.
			name: "one column unparseable in every row",
			pv:   pv17,
			rows: []Row{{
				"wal_records": "5", "wal_fpi": "x", "wal_bytes": "9", "wal_buffers_full": "0",
				"wal_write": "1", "wal_sync": "0", "wal_write_time": "0", "wal_sync_time": "0",
				"stats_reset": "2026-01-01 00:00:00+00", SourceNowColumn: fixedSourceNow,
			}},
			want: map[string]float64{"wal_records_total": 5, "wal_generated_bytes_total": 9,
				"wal_buffers_full_total": 0, "wal_write_total": 1,
				"wal_sync_total": 0, "wal_write_time_seconds_total": 0, "wal_sync_time_seconds_total": 0},
			epoch:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			source: sourceNow,
		},
		{name: "no rows", pv: pv17, rows: nil, want: nil, source: time.Time{}},
	}

	for _, tc := range cases {
		got := walSamples(tc.pv, fixedClock, tc.rows)
		if len(got) != len(tc.want) {
			t.Errorf("%s: %d samples, want %d\n%s", tc.name, len(got), len(tc.want), dumpSamples(got))
			continue
		}
		for _, s := range got {
			want, ok := tc.want[s.Metric]
			if !ok {
				t.Errorf("%s: unexpected family %s", tc.name, s.Metric)
				continue
			}
			if s.Value != want {
				t.Errorf("%s: %s = %v, want %v", tc.name, s.Metric, s.Value, want)
			}
			if len(s.Labels) != 0 {
				t.Errorf("%s: %s labels = %v, want none (R9.3)", tc.name, s.Metric, s.Labels)
			}
			if !s.IngestTime.Equal(fixedClock) {
				t.Errorf("%s: %s IngestTime = %s, want %s", tc.name, s.Metric, s.IngestTime, fixedClock)
			}
			if !s.SourceTime.Equal(tc.source) {
				t.Errorf("%s: %s SourceTime = %s, want %s", tc.name, s.Metric, s.SourceTime, tc.source)
			}
			if !s.StatsReset.Equal(tc.epoch) {
				t.Errorf("%s: %s StatsReset = %s, want %s (the newest stats_reset among the rows that "+
					"contributed, not the first row's and not a zero)", tc.name, s.Metric, s.StatsReset, tc.epoch)
			}
		}
	}
}
