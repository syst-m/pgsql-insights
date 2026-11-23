package collector

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract: the database collector is a Collector,
// checked by the compiler rather than by a test that has to remember to call it.
var _ Collector = (*DatabaseCollector)(nil)

// fakeTx is a Tx that answers from a table of canned rows keyed by query text.
//
// Keying on the query string is what makes the fake assert the SQL: a collector
// that queried the wrong view, or the right view with the wrong column list, gets
// nil rows back and the test says so. Recording the queries it was asked for is
// the other half — that is how the R2.7 "one transaction, one query per view"
// assertions are read rather than inferred.
type fakeTx struct {
	rows      map[string][]Row
	failOn    string
	queries   []string
	commits   int
	rollbacks int
	commitErr error
}

func (f *fakeTx) Query(_ context.Context, query string) ([]Row, error) {
	f.queries = append(f.queries, query)
	if f.failOn == query {
		return nil, errors.New("fake: query failed")
	}
	return f.rows[query], nil
}

func (f *fakeTx) Commit() error {
	f.commits++
	return f.commitErr
}

func (f *fakeTx) Rollback() error {
	f.rollbacks++
	return nil
}

// fakeOpener is the SQLOpener half: it counts Begin calls and records the
// SQLConfig each one carried, which is the only way to assert R2.7's
// statement_timeout and R2.8's application_name are actually passed to the
// connection rather than merely configured on the collector.
type fakeOpener struct {
	tx       *fakeTx
	beginErr error
	calls    int
	cfgs     []SQLConfig
	targets  []Target
}

func (o *fakeOpener) Begin(_ context.Context, tgt Target, cfg SQLConfig) (Tx, error) {
	o.calls++
	o.cfgs = append(o.cfgs, cfg)
	o.targets = append(o.targets, tgt)
	if o.beginErr != nil {
		return nil, o.beginErr
	}
	return o.tx, nil
}

// fixedClock is the injectable clock the tests hand to SQLConfig, so
// Sample.IngestTime is a constant the golden assertions can name instead of a
// time.Now the test has to bound.
var fixedClock = time.Date(2026, 11, 22, 12, 30, 0, 0, time.UTC)

// fixedSourceNow is the value every fake row carries in the now() column.
const fixedSourceNow = "2026-11-22 10:00:00+00"

var sourceNow = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

// rowFor builds one Row for a projection.
//
// Every column gets a value, because a row missing a column is a different
// situation from a row with a valueless one and the tests need the first case to
// be the normal one. Metrics default to "7" so a golden assertion can check a
// value it did not have to enumerate per column; datname gets a name; the now()
// column gets the fixed timestamp. Anything in over wins, which is how a test
// sets stats_reset or a specific counter without restating the other 29 columns.
func rowFor(p Projection, over map[string]string) Row {
	row := Row{}
	for _, col := range p.Columns {
		switch col {
		case SourceNowColumn:
			row[col] = fixedSourceNow
		case "datname":
			row[col] = "app"
		default:
			row[col] = "7"
		}
	}
	for k, v := range over {
		row[k] = v
	}
	return row
}

// databaseNames is the set of databases a fixture round reports for one view: the
// app database plus a second one, and — for pg_stat_database only — the
// shared-objects row every real result set has and whose datname is NULL.
//
// This is D20's S12. The fixture used to be exactly one row per view, which made a
// multi-database round untested rather than thinly tested: with one row, "one
// sample per metric column" is the whole hypothesis, so every bug that only shows
// up on a second row — S8's empty datname, a label read off the wrong row, a
// per-row timestamp that leaks between rows — had no test that could see it. Two
// named rows plus one nameless row is what a real cluster returns.
func databaseNames(view pgversion.View) []string {
	names := []string{"app", "analytics"}
	if view == pgversion.ViewStatDatabase {
		// pg_database and db_conflicts_total have no shared-objects row:
		// that row is a pg_stat_database artefact (datid 0), and the S8 filter is
		// written per view, not per collector.
		names = append(names, "")
	}
	return names
}

// newTestDatabase builds the collector plus the fake opener and tx it runs on,
// serving databaseNames' rows for every projection the collector would issue
// against tgt.
func newTestDatabase(t *testing.T, tgt Target, over map[string]string) (*DatabaseCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	projections, err := NewDatabaseCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range projections {
		for _, name := range databaseNames(pv.view) {
			// The name goes through `over` rather than through a rowFor parameter so
			// there is one way to set a column and it is always the same one.
			row := rowFor(pv.Projection, map[string]string{"datname": name})
			for k, v := range over {
				row[k] = v
			}
			tx.rows[pv.Query] = append(tx.rows[pv.Query], row)
		}
	}
	opener := &fakeOpener{tx: tx}
	return NewDatabaseCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener, tx
}

// datnamesOf returns the datname of every sample in a list, sorted, so an
// assertion can name the whole label set in one string instead of asserting on one
// sample and hoping the others agree.
func datnamesOf(samples []Sample) string {
	names := make([]string, 0, len(samples))
	for _, s := range samples {
		names = append(names, s.Labels["datname"])
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestDatabaseDescriptor(t *testing.T) {
	c, _, _ := newTestDatabase(t, tgtPG("pg", "pg-1", "primary"), nil)
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorDatabase {
		t.Errorf("Name = %q, want %q (§3.3's row name, and the R2.2 `collector` label)", d.Name, CollectorDatabase)
	}
	if d.Interval != DatabaseInterval {
		t.Errorf("Interval = %s, want %s (§3.3's 60 s)", d.Interval, DatabaseInterval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %s, want %s (§3.3's S2)", d.Source, SourceSQL)
	}
	if d.Cost != CostStandard {
		t.Errorf("Cost = %s, want %s (one read-only transaction, single-digit queries)", d.Cost, CostStandard)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != databaseViewGroup {
		t.Errorf("ViewGroups = %v, want [%s] (R2.7: one entry per view group, and this collector has one)",
			d.ViewGroups, databaseViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; the tuning knob is R2.7's statement_timeout)", d.Timeout)
	}
	for _, major := range pgversion.Majors() {
		if !d.Applies(major) {
			t.Errorf("Applies(%d) = false, want true (all three views exist on every matrix major)", major)
		}
	}
	for _, major := range []int{12, 19} {
		if d.Applies(major) {
			t.Errorf("Applies(%d) = true, want false", major)
		}
	}
}

// TestDatabaseQueriesAreSchemaQualified is R2.5 checked on every major the matrix
// has: each of the three queries names its schema, and the column list is the
// matrix's for that major rather than a hand-written one.
func TestDatabaseQueriesAreSchemaQualified(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		projections, err := NewDatabaseCollector(nil, SQLConfig{}).projections(tgt)
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}
		if len(projections) != len(databaseViews) {
			t.Fatalf("PG%d: %d projections, want %d", major, len(projections), len(databaseViews))
		}
		for _, pv := range projections {
			want := " FROM pg_catalog." + string(pv.view)
			// Containment, not suffix: a view with a WHERE clause (pg_database,
			// D20's S7) no longer ends at the table name, and a suffix check would
			// have failed on the very query the fix added. The suffix check survives
			// where it is still the right shape — and the WHERE clause gets its own.
			if !strings.Contains(pv.Query, want) {
				t.Errorf("PG%d %s: query %q does not contain %q (R2.5)", major, pv.view, pv.Query, want)
			}
			if pv.Where == "" {
				if !strings.HasSuffix(pv.Query, want) {
					t.Errorf("PG%d %s: query %q does not end in %q", major, pv.view, pv.Query, want)
				}
			} else if !strings.HasSuffix(pv.Query, " WHERE "+pv.Where) {
				t.Errorf("PG%d %s: query %q does not end in the spec's own WHERE clause %q",
					major, pv.view, pv.Query, pv.Where)
			}
			if pv.view == pgversion.ViewDatabase &&
				!strings.Contains(pv.Where, "pg_catalog.has_database_privilege") {
				t.Errorf("PG%d pg_database: WHERE %q has no has_database_privilege guard (D20's S7: db_size_bytes(oid) needs CONNECT per database, and without the filter the whole SELECT throws on template0)",
					major, pv.Where)
			}
			cols, _ := pgversion.Columns(pv.view, major)
			for _, col := range cols {
				if !strings.Contains(pv.Query, col) {
					t.Errorf("PG%d %s: query does not select %s (R2.4 says the matrix's column set is the projection)",
						major, pv.view, col)
				}
			}
		}
	}
}

// TestDatabaseCollectSamples is the golden sample set for one round: every
// database the fixture reports, and every metric column of each of their rows
// turned into a sample carrying the R9.3 datname label, R13.11's two timestamps
// and R3.3's epoch.
//
// The expected count is derived — metric columns times named rows, summed over the
// three projections — not typed. A number a test asserts on is a claim about the
// collector, and a recalled number is a claim nobody checked; the product form is
// also what makes this test cover S8's rule on every metric of pg_stat_database,
// because the fixture's nameless row is excluded from the product.
func TestDatabaseCollectSamples(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, _, _ := newTestDatabase(t, tgt, map[string]string{"stats_reset": "2026-01-01 00:00:00+00"})

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	projections, _ := c.projections(tgt)
	wantMetric := 0
	for _, pv := range projections {
		metrics, named := 0, 0
		for _, col := range pv.Columns {
			if kindOf(pv.view, col) == kindMetric {
				metrics++
			}
		}
		for _, name := range databaseNames(pv.view) {
			if name != "" {
				named++
			}
		}
		wantMetric += metrics * named
	}
	if len(res.Samples) != wantMetric {
		t.Fatalf("len(Samples) = %d, want %d (metric columns x named rows across the three projections)",
			len(res.Samples), wantMetric)
	}

	// Spot checks that the value, the labels and the three timestamps all came
	// from the row rather than from a default. byMetric holds a slice per family:
	// a map keyed by metric alone keeps whichever database the loop saw last and
	// silently hides the other, which is the half of S12 the count alone cannot see.
	byMetric := map[string][]Sample{}
	for _, s := range res.Samples {
		byMetric[s.Metric] = append(byMetric[s.Metric], s)
	}
	for _, name := range []string{
		"db_xact_commit_total",
		"db_blks_hit_total",
		"db_conflicts_lock_total",
		"db_size_bytes",
		"db_txid_age",
		"db_mxid_age",
	} {
		got := byMetric[name]
		if len(got) != 2 {
			t.Errorf("%s: %d samples, want 2 (one per named database)", name, len(got))
			continue
		}
		if names := datnamesOf(got); names != "analytics,app" {
			t.Errorf("%s datnames = %q, want \"analytics,app\" (R9.3: database series add datname)", name, names)
		}
		for _, s := range got {
			if s.Value != 7 {
				t.Errorf("%s{%s} value = %v, want 7", name, s.Labels["datname"], s.Value)
			}
			if !s.SourceTime.Equal(sourceNow) {
				t.Errorf("%s{%s} SourceTime = %s, want %s (R13.11's instance clock, from the now() column)",
					name, s.Labels["datname"], s.SourceTime, sourceNow)
			}
			if !s.IngestTime.Equal(fixedClock) {
				t.Errorf("%s{%s} IngestTime = %s, want %s (R13.11's our-clock, from the injected clock)",
					name, s.Labels["datname"], s.IngestTime, fixedClock)
			}
		}
	}

	// The epoch lands on the samples that have one and not on the ones that do
	// not: pg_stat_database has stats_reset, pg_database and
	// db_conflicts_total do not, and a zero there is "no epoch" rather
	// than a missing value. Every row of each family is checked, not one of them.
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, s := range byMetric["db_xact_commit_total"] {
		if !s.StatsReset.Equal(epoch) {
			t.Errorf("db_xact_commit_total{%s} StatsReset = %s, want %s (R3.3's counter epoch)",
				s.Labels["datname"], s.StatsReset, epoch)
		}
	}
	for _, s := range byMetric["db_size_bytes"] {
		if !s.StatsReset.IsZero() {
			t.Errorf("db_size_bytes{%s} StatsReset = %s, want zero (pg_database has no stats_reset)",
				s.Labels["datname"], s.StatsReset)
		}
	}

	// The identity labels are the scheduler's to add (clock.go's Sample doc), so a
	// collector that stamped them here would be a collector that duplicates the
	// contract it is documented to leave alone.
	for _, s := range res.Samples {
		if len(s.Labels) != 1 {
			t.Errorf("%s labels = %v, want exactly {datname} (identity labels are the scheduler's)", s.Metric, s.Labels)
			break
		}
	}
}

// TestDatabaseCollectOneTransactionPerRound is R2.7 as a count: one Begin, one
// query per view, one Commit, and the config that reached the opener carrying
// R2.7's statement_timeout and R2.8's application_name.
func TestDatabaseCollectOneTransactionPerRound(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, opener, tx := newTestDatabase(t, tgt, nil)

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if opener.calls != 1 {
		t.Errorf("Begin calls = %d, want 1 (R2.7: one read-only transaction per view group, and this collector has one group)", opener.calls)
	}
	if len(tx.queries) != len(databaseViews) {
		t.Fatalf("queries = %d, want %d (one per view)", len(tx.queries), len(databaseViews))
	}
	projections, _ := c.projections(tgt)
	for i, q := range tx.queries {
		if q != projections[i].Query {
			t.Errorf("query %d =\n%s\nwant\n%s", i, q, projections[i].Query)
		}
	}
	if tx.commits != 1 || tx.rollbacks != 0 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/0 on the success path", tx.commits, tx.rollbacks)
	}
	if res.RoundTrips != len(databaseViews) {
		t.Errorf("RoundTrips = %d, want %d (R13.1 measures a round from this count)", res.RoundTrips, len(databaseViews))
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

// TestDatabaseCollectQueryErrorKeepsRoundTrips is the Collector contract's
// "on error, the returned Result must still carry the RoundTrips it actually
// issued": a round that issued two queries before failing reports 2, not 0,
// because R13.1's budget is measured from this number and a zero makes a cluster
// that did work look like one that did none.
func TestDatabaseCollectQueryErrorKeepsRoundTrips(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	projections, _ := NewDatabaseCollector(nil, SQLConfig{}).projections(tgt)

	tx := &fakeTx{
		rows:   map[string][]Row{projections[0].Query: {rowFor(projections[0].Projection, nil)}},
		failOn: projections[1].Query,
	}
	opener := &fakeOpener{tx: tx}
	c := NewDatabaseCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the query failure surfaced")
	}
	if res.RoundTrips != 2 {
		t.Errorf("RoundTrips = %d, want 2 (two queries issued before the failure)", res.RoundTrips)
	}
	if len(res.Samples) == 0 {
		t.Error("Samples empty, want the rows parsed from the view that did answer")
	}
	if tx.rollbacks != 1 || tx.commits != 0 {
		t.Errorf("rollbacks/commits = %d/%d, want 1/0 (a failed round must not leave a transaction open)",
			tx.rollbacks, tx.commits)
	}
	if !strings.Contains(err.Error(), string(pgversion.ViewDatabase)) {
		t.Errorf("error %q does not name the failing view", err)
	}
}

// TestDatabaseCollectNamelessRowProducesNoSamples is D20's S8 on its own: the
// shared-objects row every real pg_stat_database result set has, whose datname is
// NULL. The row is real data (shared buffers, shared block hits) and R9.3's closed
// label set has no slot for datname="", so it yields no samples rather than a
// series named pgsi_stat_database_xact_commit{datname=""}.
//
// The fixture here is deliberately the nameless row *alone*, where the shared
// fixture mixes it with two named ones: the bug S8 describes was the nameless row
// exporting a series beside the named ones, and a test that only ever had named
// rows could not have seen it. A projection with no name column at all is the other
// half of the rule (hasName false, not filtered) and belongs to the settings
// collector's builders, which have no name column and correctly export everything.
func TestDatabaseCollectNamelessRowProducesNoSamples(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	projections, err := NewDatabaseCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range projections {
		tx.rows[pv.Query] = []Row{rowFor(pv.Projection, map[string]string{"datname": ""})}
	}
	opener := &fakeOpener{tx: tx}
	c := NewDatabaseCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Samples) != 0 {
		t.Errorf("len(Samples) = %d, want 0 (a row with no datname has no R9.3 series to join: skipped, not labelled \"\")\n%v",
			len(res.Samples), res.Samples)
	}
	if res.RoundTrips != len(databaseViews) {
		t.Errorf("RoundTrips = %d, want %d (the round still ran — skipping a row is not skipping a query)",
			res.RoundTrips, len(databaseViews))
	}
}

// TestDatabaseCollectCommitError is D20's S9 as well as the round-trip rule: the
// fake's rollbacks counter is read here, because the version of this test that
// checked commits and RoundTrips but never rollbacks is what let a commit-error path
// return without the Rollback that sql.go's Tx doc requires on every error path.
func TestDatabaseCollectCommitError(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, _, tx := newTestDatabase(t, tgt, nil)
	tx.commitErr = errors.New("fake: connection lost")

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the commit failure surfaced")
	}
	if res.RoundTrips != len(databaseViews) {
		t.Errorf("RoundTrips = %d, want %d (the queries did run)", res.RoundTrips, len(databaseViews))
	}
	if len(res.Samples) == 0 {
		t.Error("Samples empty, want the read samples returned alongside the error")
	}
	if tx.commits != 1 || tx.rollbacks != 1 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/1 (a failed COMMIT may leave the transaction open on a pooled connection; the best-effort Rollback is what closes it)",
			tx.commits, tx.rollbacks)
	}
}

func TestDatabaseCollectBeginError(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	opener := &fakeOpener{beginErr: errors.New("fake: connect refused")}
	c := NewDatabaseCollector(opener, SQLConfig{})

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

// TestDatabaseCollectRefusesUnknownMajors covers the two ways a target can have
// no usable SQL: no major at all, and a major outside the matrix. Both must fail
// before opening a transaction — a Begin against a target whose SQL cannot be
// built is a connection spent on nothing.
func TestDatabaseCollectRefusesUnknownMajors(t *testing.T) {
	for _, major := range []int{0, 12, 19} {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		opener := &fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}
		c := NewDatabaseCollector(opener, SQLConfig{})

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

// TestKindOfCoversEveryMatrixColumn is the test that keeps the classifier and the
// matrix from drifting apart: every column the matrix returns for this
// collector's views on every major must have a kind.
//
// Without it, a matrix update (a new PG18 column, a corrected PG15 set) would
// leave the new column classified as kindUnknown and silently unreported — the
// failure mode R16.1's "table and tests together" rule exists to prevent, on the
// collector side rather than the matrix side.
func TestKindOfCoversEveryMatrixColumn(t *testing.T) {
	for _, major := range pgversion.Majors() {
		for _, view := range databaseViews {
			cols, ok := pgversion.Columns(view, major)
			if !ok {
				t.Fatalf("Columns(%s, %d): not ok", view, major)
			}
			for _, col := range cols {
				if kindOf(view, col) == kindUnknown {
					t.Errorf("kindOf(%s, %s) = unknown on PG%d (a column the matrix has and the collector has no opinion about)",
						view, col, major)
				}
			}
		}
	}
}

// TestKindOfRoles pins the classifications that are not the default, because each
// one is a decision about what is not a value and each is therefore worth naming
// in a test rather than only in a switch.
func TestKindOfRoles(t *testing.T) {
	cases := []struct {
		view   pgversion.View
		column string
		want   columnKind
	}{
		// Row identity: join keys and function arguments, not metrics.
		{pgversion.ViewDatabase, "oid", kindID},
		{pgversion.ViewDatabase, "datdba", kindID},
		{pgversion.ViewDatabase, "dattablespace", kindID},
		{pgversion.ViewStatDatabase, "datid", kindID},
		{pgversion.ViewStatDatabaseConflicts, "datid", kindID},
		// R9.3's label, not a value.
		{pgversion.ViewDatabase, "datname", kindName},
		{pgversion.ViewStatDatabase, "datname", kindName},
		// The two timestamps that fill sample fields instead of becoming samples.
		{pgversion.ViewStatDatabase, "stats_reset", kindEpoch},
		{pgversion.ViewStatDatabase, "checksum_last_failure", kindText},
		{pgversion.ViewDatabase, SourceNowColumn, kindSourceNow},
		{pgversion.ViewStatDatabaseConflicts, SourceNowColumn, kindSourceNow},
		// Text settings with no metric meaning.
		{pgversion.ViewDatabase, "datcollate", kindText},
		{pgversion.ViewDatabase, "datacl", kindText},
		{pgversion.ViewDatabase, "daticurules", kindText},
		// The derived expressions are metrics, and their names are R5.2's three.
		{pgversion.ViewDatabase, "size", kindMetric},
		{pgversion.ViewDatabase, "frozenxid_age", kindMetric},
		{pgversion.ViewDatabase, "minmxid_age", kindMetric},
		// Booleans are metrics (1/0) rather than text.
		{pgversion.ViewDatabase, "datistemplate", kindMetric},
		{pgversion.ViewDatabase, "dathasloginevt", kindMetric},
		// A view this collector does not read has no classification.
		{pgversion.ViewLocks, "locktype", kindUnknown},
	}
	for _, tc := range cases {
		if got := kindOf(tc.view, tc.column); got != tc.want {
			t.Errorf("kindOf(%s, %s) = %s, want %s", tc.view, tc.column, got, tc.want)
		}
	}
}
