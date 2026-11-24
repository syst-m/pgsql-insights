// Statements collector tests: the R3.1 feed, R9.5's family, and R9.5b's labels.
//
// The fixtures here follow database_test.go and activity_test.go rather than
// inventing a third harness: fakeTx/fakeOpener come from database_test.go (they
// are package-level and their keying-on-query-text design is what makes a wrong
// SQL string fail loudly), and the per-view Row builders follow activity_test.go's
// stated reason for not reusing rowFor — a blanket "7" is the wrong default for
// every column a test names in its own assertions.

package collector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
	"github.com/frankellis/pgsql-insights/internal/query"
)

// The compile-time half of the contract: the statement collector is a Collector,
// checked by the compiler rather than by a test that has to remember to call it.
var _ Collector = (*StatementsCollector)(nil)

// stmtTarget is a SQL target at an explicit major.
//
// The major matters here more than anywhere else in the package: the two views this
// collector reads exist on 14–18 and not on 13, and pg_stat_statements' column set
// differs between 15–16 and 17–18 (the blk_read_time pair is renamed), so a test
// has to say which major it is pretending to be.
func stmtTarget(major int) Target {
	return Target{
		Namespace: "db",
		Cluster:   "pg",
		Pod:       "pg-1",
		Role:      "primary",
		Address:   "pg-1.db.svc:5432",
		Source:    SourceSQL,
		PGMajor:   major,
	}
}

// stmtProjections is the collector's view set for a target, built through the same
// projections() the round itself runs. The fixture needs it to key the fake
// transaction's rows by query text, which is the only way the fake can answer the
// right query with the right rows.
func stmtProjections(t *testing.T, tgt Target) []viewProjection {
	t.Helper()
	pps, err := NewStatementsCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections(PG%d): %v", tgt.PGMajor, err)
	}
	return pps
}

// stmtRow builds one pg_stat_statements Row for a target's major.
//
// The defaults are the values a real row has, named here rather than defaulted to
// "7", for the reason activity_test.go gives for the same choice: the fields
// statementRowFrom reads are the fields a test asserts on, so naming them in the
// builder means a test overrides the one it is about instead of restating twelve.
// Every other column in the projection still gets "7" — present, so the row is a
// full projection result, and uninteresting, so no test accidentally depends on it.
func stmtRow(t *testing.T, tgt Target, over map[string]string) Row {
	t.Helper()
	p, ok := Project(pgversion.ViewStatStatements, tgt.PGMajor, statementSpecs[pgversion.ViewStatStatements])
	if !ok {
		t.Fatalf("PG%d: no projection for pg_stat_statements", tgt.PGMajor)
	}
	row := Row{
		SourceNowColumn:    fixedSourceNow,
		"datname":          "app",
		"queryid":          "4520629923479686359",
		"query":            "SELECT * FROM orders WHERE id = 42",
		"usename":          "appuser",
		"calls":            "100",
		"total_exec_time":  "1500",
		"rows":             "12",
		"shared_blks_hit":  "34",
		"shared_blks_read": "56",
		"min_exec_time":    "0.5",
		"max_exec_time":    "90.25",
		"stddev_exec_time": "3.75",
	}
	for _, col := range p.Columns {
		if _, has := row[col]; !has {
			row[col] = "7"
		}
	}
	for k, v := range over {
		row[k] = v
	}
	return row
}

// stmtInfoRow builds one pg_stat_statements_info Row for one database.
//
// The view has exactly two columns (dealloc, stats_reset), so unlike stmtRow there
// is nothing to leave out: the builder takes the two values plus the datname Extra.
func stmtInfoRow(t *testing.T, tgt Target, datname, statsReset string) Row {
	t.Helper()
	p, ok := Project(pgversion.ViewStatStatementsInfo, tgt.PGMajor, statementSpecs[pgversion.ViewStatStatementsInfo])
	if !ok {
		t.Fatalf("PG%d: no projection for pg_stat_statements_info", tgt.PGMajor)
	}
	row := Row{
		SourceNowColumn: fixedSourceNow,
		"datname":       datname,
		"dealloc":       "0",
		"stats_reset":   statsReset,
	}
	for _, col := range p.Columns {
		if _, has := row[col]; !has {
			row[col] = "7"
		}
	}
	return row
}

// stmtFixtureWith builds the collector, its opener and its transaction, serving
// stmts for pg_stat_statements and info for pg_stat_statements_info.
//
// The two row lists are separate arguments because the two views answer different
// questions and every interesting case is a disagreement between them: a database
// with an epoch and no statements, a statement row whose database has no info row,
// two databases in one round.
func stmtFixtureWith(t *testing.T, tgt Target, cfg SQLConfig, stmts, info []Row) (*StatementsCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range stmtProjections(t, tgt) {
		switch pv.view {
		case pgversion.ViewStatStatements:
			tx.rows[pv.Query] = stmts
		case pgversion.ViewStatStatementsInfo:
			tx.rows[pv.Query] = info
		}
	}
	opener := &fakeOpener{tx: tx}
	return NewStatementsCollector(opener, cfg), opener, tx
}

// stmtFixture is the common case: the fixed clock and one database's worth of rows.
func stmtFixture(t *testing.T, tgt Target, stmts, info []Row) (*StatementsCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	return stmtFixtureWith(t, tgt, SQLConfig{Now: func() time.Time { return fixedClock }}, stmts, info)
}

// collectStatements is the typed entry point with the error already checked.
func collectStatements(t *testing.T, c *StatementsCollector, tgt Target) []Snapshot {
	t.Helper()
	snaps, _, err := c.CollectStatements(context.Background(), tgt)
	if err != nil {
		t.Fatalf("CollectStatements: %v", err)
	}
	return snaps
}

// TestStatementsDescriptor checks the R2.1 declaration against §3.3:196's row and
// against the two fields whose values are arguments rather than constants: Cost,
// which collector.go's class doc assigns to this collector by name, and AppliesTo,
// which is the all-views rule over statementViews.
func TestStatementsDescriptor(t *testing.T) {
	c := NewStatementsCollector(nil, SQLConfig{})
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorStatement {
		t.Errorf("Name = %q, want %q (§3.3:196 spells it `statement`, singular)", d.Name, CollectorStatement)
	}
	if d.Interval != StatementInterval {
		t.Errorf("Interval = %s, want %s (§3.3:196's 60 s)", d.Interval, StatementInterval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %s, want %s (§3.3:196's S2)", d.Source, SourceSQL)
	}
	if d.Cost != CostExpensive {
		t.Errorf("Cost = %s, want %s (collector.go's CostExpensive doc names \"the statements and activity collectors\")",
			d.Cost, CostExpensive)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != statementsViewGroup {
		t.Errorf("ViewGroups = %v, want [%s] (R2.7: one entry per view group, and this collector has one)",
			d.ViewGroups, statementsViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; the tuning knob is R2.7's statement_timeout)", d.Timeout)
	}
}

// TestStatementsAppliesToPG14Through18 is the all-views rule's answer for this
// collector's view set, and the reason it is not "every major".
//
// pg_stat_statements exists on every matrix major, but pg_stat_statements_info —
// which the collector must read to fill Snapshot.StatsReset, R3.3's epoch — has one
// column set only: StatStatementsInfoPG14to18 (matrix.go:551). R2.7's applicability
// is conjunctive over the views a collector reads, so the collector applies to
// 14–18 and not to 13. statementViews' doc covers what that means for R3.3's PG 13
// fallback; this test is the assertion.
func TestStatementsAppliesToPG14Through18(t *testing.T) {
	d := NewStatementsCollector(nil, SQLConfig{}).Descriptor()

	cases := []struct {
		major int
		want  bool
		why   string
	}{
		{12, false, "outside the matrix"},
		{13, false, "pg_stat_statements_info has no PG13 column set"},
		{14, true, "both views exist"},
		{15, true, "both views exist"},
		{16, true, "both views exist"},
		{17, true, "both views exist"},
		{18, true, "both views exist (R13.9's top end)"},
		{19, false, "outside the matrix"},
	}
	for _, tc := range cases {
		if got := d.Applies(tc.major); got != tc.want {
			t.Errorf("Applies(%d) = %v, want %v (%s)", tc.major, got, tc.want, tc.why)
		}
	}
}

// TestStatementsQueriesAreSchemaQualified is R2.5 and R2.4 on this collector's two
// queries, plus the one thing the framework cannot check for itself: which query
// the R2.8 filter is on.
//
// projections() already ran validateProjection on both (sql.go:478 is unexported,
// which is why this file is in this package), so schema qualification and the
// column list are checked by the constructor path. What it cannot check is
// placement: the WHERE names a `query` column, and pg_stat_statements_info has
// none, so a spec that put the filter on both views produces SQL that errors at
// runtime and validates fine.
func TestStatementsQueriesAreSchemaQualified(t *testing.T) {
	tgt := stmtTarget(16)
	pps := stmtProjections(t, tgt)
	if len(pps) != 2 {
		t.Fatalf("projections returned %d views, want 2", len(pps))
	}

	byView := map[pgversion.View]viewProjection{}
	for _, pv := range pps {
		byView[pv.view] = pv
	}
	ss, ok := byView[pgversion.ViewStatStatements]
	if !ok {
		t.Fatalf("no projection for %s", pgversion.ViewStatStatements)
	}
	info, ok := byView[pgversion.ViewStatStatementsInfo]
	if !ok {
		t.Fatalf("no projection for %s", pgversion.ViewStatStatementsInfo)
	}

	for _, pv := range []viewProjection{ss, info} {
		want := "FROM pg_stat_statements." + string(pv.view)
		if pv.view == pgversion.ViewStatStatementsInfo {
			want = "FROM pg_stat_statements.pg_stat_statements_info"
		}
		if !strings.Contains(pv.Query, want) {
			t.Errorf("%s query does not name its schema (%s):\n%s", pv.view, want, pv.Query)
		}
		if !strings.Contains(pv.Query, "AS datname") {
			t.Errorf("%s query lost the datname Extra — the database is a column, so a round's rows must name their own:\n%s",
				pv.view, pv.Query)
		}
	}

	// The R2.8 filter: on the view with a `query` column, and only there.
	if !strings.Contains(ss.Query, statementsWhere) {
		t.Errorf("pg_stat_statements query is missing the R2.8 exclusion clause:\n%s", ss.Query)
	}
	if strings.Contains(info.Query, "query NOT LIKE") {
		t.Errorf("pg_stat_statements_info query carries a `query` filter, but the view has no query column:\n%s", info.Query)
	}
	// The usename Extra is how store.StatementRow.Usename gets its value: the view
	// has userid (an OID) on every major and never the name.
	if !strings.Contains(ss.Query, "pg_catalog.pg_get_userbyid(userid) AS usename") {
		t.Errorf("pg_stat_statements query has no schema-qualified pg_get_userbyid(userid) AS usename:\n%s", ss.Query)
	}
}

// TestStatementsOneTransactionPerRound is R2.7 on this collector: one read-only
// transaction, one query per view in its group, and the round-trip count the
// scheduler's R13.1 budget is measured from.
//
// It also checks the SQLConfig that reached Begin. The fake records it, and this is
// the only place the collector's cfg.withDefaults() result is observable: a
// collector that passed its raw SQLConfig instead of the defaulted one would
// connect with no statement_timeout and no application_name, silently dropping
// both R2.7's timeout and R2.8's identity.
func TestStatementsOneTransactionPerRound(t *testing.T) {
	tgt := stmtTarget(16)
	row := stmtRow(t, tgt, nil)
	c, opener, tx := stmtFixture(t, tgt, []Row{row}, []Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")})

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if opener.calls != 1 {
		t.Errorf("Begin called %d times, want 1 (R2.7: one transaction per view group, and this collector has one group)", opener.calls)
	}
	if len(tx.queries) != 2 {
		t.Errorf("%d queries issued, want 2 (one per view in the group)", len(tx.queries))
	}
	if res.RoundTrips != 2 {
		t.Errorf("Result.RoundTrips = %d, want 2", res.RoundTrips)
	}
	if tx.commits != 1 || tx.rollbacks != 0 {
		t.Errorf("commits=%d rollbacks=%d, want 1/0", tx.commits, tx.rollbacks)
	}
	if len(opener.cfgs) == 0 {
		t.Fatal("opener recorded no SQLConfig")
	}
	got := opener.cfgs[0]
	if got.ApplicationName != ApplicationName {
		t.Errorf("Begin got ApplicationName %q, want %q (R2.8: our own sessions must be identifiable)",
			got.ApplicationName, ApplicationName)
	}
	if got.StatementTimeout != DefaultStatementTimeout {
		t.Errorf("Begin got StatementTimeout %s, want %s (R2.7's default; a raw cfg instead of cfg.withDefaults() would be 0)",
			got.StatementTimeout, DefaultStatementTimeout)
	}
	if got.Now == nil {
		t.Error("Begin got a nil Now, so SQLConfig.withDefaults did not run")
	}
}

// TestStatementsQueryErrorRollsBackAndReportsRoundTrips covers both query slots.
//
// The Collector contract requires the round-trips issued before the failure, and
// R13.1's budget is computed from that number, so a collector that returned
// Result{} on error under-reports every partial round it ever had. The two cases
// differ in the number: failing on the first query issued is 1, on the second is 2.
func TestStatementsQueryErrorRollsBackAndReportsRoundTrips(t *testing.T) {
	tgt := stmtTarget(16)
	pps := stmtProjections(t, tgt)
	queries := map[pgversion.View]string{}
	for _, pv := range pps {
		queries[pv.view] = pv.Query
	}

	for _, tc := range []struct {
		name  string
		view  pgversion.View
		trips int
	}{
		{"first query fails", pgversion.ViewStatStatements, 1},
		{"second query fails", pgversion.ViewStatStatementsInfo, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{rows: map[string][]Row{}, failOn: queries[tc.view]}
			opener := &fakeOpener{tx: tx}
			c := NewStatementsCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

			snaps, res, err := c.CollectStatements(context.Background(), tgt)
			if err == nil {
				t.Fatalf("CollectStatements: want error, got nil")
			}
			if res.RoundTrips != tc.trips {
				t.Errorf("RoundTrips = %d, want %d", res.RoundTrips, tc.trips)
			}
			if snaps != nil {
				t.Errorf("snapshots = %v, want nil on error", snaps)
			}
			if tx.rollbacks != 1 {
				t.Errorf("rollbacks = %d, want 1 (the transaction must not be left open)", tx.rollbacks)
			}
			if tx.commits != 0 {
				t.Errorf("commits = %d, want 0", tx.commits)
			}
		})
	}
}

// TestStatementsCommitErrorRollsBack checks the third failure slot: a transaction
// that answered both queries and then failed to commit.
func TestStatementsCommitErrorRollsBack(t *testing.T) {
	tgt := stmtTarget(16)
	tx := &fakeTx{rows: map[string][]Row{}, commitErr: errFakeCommit}
	for _, pv := range stmtProjections(t, tgt) {
		tx.rows[pv.Query] = nil
	}
	opener := &fakeOpener{tx: tx}
	c := NewStatementsCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	_, res, err := c.CollectStatements(context.Background(), tgt)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if res.RoundTrips != 2 {
		t.Errorf("RoundTrips = %d, want 2 (both queries were issued before the commit failed)", res.RoundTrips)
	}
	if tx.rollbacks != 1 {
		t.Errorf("rollbacks = %d, want 1 (Rollback after a failed Commit)", tx.rollbacks)
	}
}

var errFakeCommit = errString("fake: commit failed")

type errString string

func (e errString) Error() string { return string(e) }

// TestStatementsBeginErrorIssuesNothing: a round that never opened a transaction
// issued nothing, so the honest RoundTrips is 0 and not a leftover from a previous
// round.
func TestStatementsBeginErrorIssuesNothing(t *testing.T) {
	tgt := stmtTarget(16)
	opener := &fakeOpener{beginErr: errString("fake: begin failed")}
	c := NewStatementsCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	snaps, res, err := c.CollectStatements(context.Background(), tgt)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if res.RoundTrips != 0 || len(res.Samples) != 0 {
		t.Errorf("Result = {RoundTrips:%d Samples:%d}, want 0/0 (nothing was issued)", res.RoundTrips, len(res.Samples))
	}
	if snaps != nil {
		t.Errorf("snapshots = %v, want nil", snaps)
	}
}

// TestStatementsRefusesUnknownAndInapplicableMajors checks the two projections()
// guards that stop a round before it opens a transaction. Both must issue zero
// round-trips: a collector that began a transaction it then could not fill would
// charge the round against R13.1's budget for a query it never ran.
func TestStatementsRefusesUnknownAndInapplicableMajors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		major   int
		wantSub string
	}{
		{"unknown major", 0, "no PostgreSQL major"},
		{"PG13, where the info view has no columns", 13, "not applicable on PostgreSQL 13"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tgt := stmtTarget(tc.major)
			opener := &fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}
			c := NewStatementsCollector(opener, SQLConfig{})

			_, res, err := c.CollectStatements(context.Background(), tgt)
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.wantSub)
			}
			if opener.calls != 0 {
				t.Errorf("Begin called %d times, want 0 (the guard fires before the transaction)", opener.calls)
			}
			if res.RoundTrips != 0 || len(res.Samples) != 0 {
				t.Errorf("Result = {RoundTrips:%d Samples:%d}, want 0/0", res.RoundTrips, len(res.Samples))
			}
		})
	}
}

// TestStatementsRowFields is the field-by-field half of the collector: the twelve
// fields StatementRow has, read from the twelve columns the type's doc names.
//
// It runs statementRowFrom directly rather than going through a round, because the
// round's own assertions (which query, which rows) belong to the round tests above
// and a field test that goes through them can only ever see the fixture's defaults.
func TestStatementsRowFields(t *testing.T) {
	row := Row{
		"datname":               "app",
		"queryid":               "4520629923479686359",
		"query":                 "SELECT * FROM orders WHERE id = 42",
		"usename":               "appuser",
		"calls":                 "100",
		"total_exec_time":       "1500.5",
		"rows":                  "12",
		"shared_blks_hit":       "34",
		"shared_blks_read":      "56",
		"shared_blk_read_time":  "1.5",
		"shared_blk_write_time": "2.5",
		"min_exec_time":         "0.5",
		"max_exec_time":         "90.25",
		"stddev_exec_time":      "3.75",
		SourceNowColumn:         fixedSourceNow,
	}
	r := statementRowFrom(row)

	switch {
	case r.QueryID != "4520629923479686359":
		t.Errorf("QueryID = %q", r.QueryID)
	case r.SQL != "SELECT * FROM orders WHERE id = 42":
		t.Errorf("SQL = %q", r.SQL)
	case r.Usename != "appuser":
		t.Errorf("Usename = %q", r.Usename)
	case r.Calls != 100:
		t.Errorf("Calls = %d, want 100", r.Calls)
	case r.TotalExecTimeMs != 1500.5:
		t.Errorf("TotalExecTimeMs = %v, want 1500.5", r.TotalExecTimeMs)
	case r.RowsReturned != 12:
		t.Errorf("RowsReturned = %v, want 12", r.RowsReturned)
	case r.SharedBlocksHit != 34:
		t.Errorf("SharedBlocksHit = %v, want 34", r.SharedBlocksHit)
	case r.SharedBlocksRead != 56:
		t.Errorf("SharedBlocksRead = %v, want 56", r.SharedBlocksRead)
	case r.BlockReadMs != 1.5:
		t.Errorf("BlockReadMs = %v, want 1.5", r.BlockReadMs)
	case r.BlockWriteMs != 2.5:
		t.Errorf("BlockWriteMs = %v, want 2.5", r.BlockWriteMs)
	case r.MinExecTimeMs != 0.5:
		t.Errorf("MinExecTimeMs = %v, want 0.5", r.MinExecTimeMs)
	case r.MaxExecTimeMs != 90.25:
		t.Errorf("MaxExecTimeMs = %v, want 90.25", r.MaxExecTimeMs)
	}
	if r.StddevExecTimeMs == nil {
		t.Fatal("StddevExecTimeMs = nil, want &3.75")
	}
	if *r.StddevExecTimeMs != 3.75 {
		t.Errorf("StddevExecTimeMs = %v, want 3.75", *r.StddevExecTimeMs)
	}
	// The collector never captures plans: R3.10's sampling is the API plane's work
	// and a plan is not a column of this view. The field is on the shared row type
	// and this collector's answer for it is nil, always.
	if r.Plan != nil {
		t.Errorf("Plan = %+v, want nil (R3.10's capture is not this collector's)", r.Plan)
	}
}

// TestStatementsStddevNilVersusZero is the not-available-vs-zero rule (R3.14) on
// the one field that can express it.
//
// The view reports NULL stddev for a row with no sample, the driver gives an empty
// string for NULL, and the store has to tell that apart from a real 0 — the same
// distinction rollupTable makes with its lock_wait_ms sentinel. The type's answer is
// a pointer, so the test has to check both sides of it: an empty cell is nil, and a
// "0" cell is a pointer to 0, not nil.
func TestStatementsStddevNilVersusZero(t *testing.T) {
	tgt := stmtTarget(16)

	empty := stmtRow(t, tgt, map[string]string{"stddev_exec_time": ""})
	if r := statementRowFrom(empty); r.StddevExecTimeMs != nil {
		t.Errorf("empty stddev cell → %v, want nil (NULL means no sample, not a 0 ms spread)", *r.StddevExecTimeMs)
	}
	zero := stmtRow(t, tgt, map[string]string{"stddev_exec_time": "0"})
	r := statementRowFrom(zero)
	if r.StddevExecTimeMs == nil {
		t.Fatal("stddev \"0\" → nil, want &0 (a measured 0 is a real value)")
	}
	if *r.StddevExecTimeMs != 0 {
		t.Errorf("stddev \"0\" → %v, want 0", *r.StddevExecTimeMs)
	}
	absent := Row{"queryid": "1", "query": "SELECT 1"}
	if r := statementRowFrom(absent); r.StddevExecTimeMs != nil {
		t.Error("absent stddev column → non-nil, want nil")
	}
}

// TestStatementsPerMajorSpellings covers the two column pairs the row type collapses
// to one field each. succession.go declares the pairs one value under two names and
// MetricName gives each pair one family name, so the collector has to pick one — and
// which one it picks decides whether a PG 16 row and a PG 17 row end up with
// comparable numbers.
func TestStatementsPerMajorSpellings(t *testing.T) {
	cases := []struct {
		name string
		row  Row
		want func(StatementRow) (string, bool)
	}{
		{
			name: "PG17+ total_exec_time wins over total_time",
			row:  Row{"queryid": "1", "query": "SELECT 1", "total_exec_time": "10", "total_time": "99"},
			want: func(r StatementRow) (string, bool) {
				return fmt.Sprintf("%v", r.TotalExecTimeMs), r.TotalExecTimeMs == 10
			},
		},
		{
			name: "total_time alone still parses",
			row:  Row{"queryid": "1", "query": "SELECT 1", "total_time": "99"},
			want: func(r StatementRow) (string, bool) {
				return fmt.Sprintf("%v", r.TotalExecTimeMs), r.TotalExecTimeMs == 99
			},
		},
		{
			name: "PG17+ shared_blk_read_time wins over blk_read_time",
			row:  Row{"queryid": "1", "query": "SELECT 1", "shared_blk_read_time": "3", "blk_read_time": "4"},
			want: func(r StatementRow) (string, bool) { return fmt.Sprintf("%v", r.BlockReadMs), r.BlockReadMs == 3 },
		},
		{
			name: "blk_read_time alone still parses",
			row:  Row{"queryid": "1", "query": "SELECT 1", "blk_read_time": "4"},
			want: func(r StatementRow) (string, bool) { return fmt.Sprintf("%v", r.BlockReadMs), r.BlockReadMs == 4 },
		},
		{
			name: "PG17+ shared_blk_write_time wins over blk_write_time",
			row:  Row{"queryid": "1", "query": "SELECT 1", "shared_blk_write_time": "5", "blk_write_time": "6"},
			want: func(r StatementRow) (string, bool) { return fmt.Sprintf("%v", r.BlockWriteMs), r.BlockWriteMs == 5 },
		},
		{
			name: "blk_write_time alone still parses",
			row:  Row{"queryid": "1", "query": "SELECT 1", "blk_write_time": "6"},
			want: func(r StatementRow) (string, bool) { return fmt.Sprintf("%v", r.BlockWriteMs), r.BlockWriteMs == 6 },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.want(statementRowFrom(tc.row))
			if !ok {
				t.Errorf("field = %s", got)
			}
		})
	}
}

// TestFirstFloatStopsOnUnparseable pins the parse rule the two per-major lists
// depend on.
//
// A cell that is present but unparseable is a parse failure, not a hint that the
// other spelling is the live one, so firstFloat returns false instead of trying the
// next name. Without that rule, a row whose total_exec_time the driver mangled would
// silently report its total_time — two numbers for one field, which is the second
// copy of a contract this repo refuses.
func TestFirstFloatStopsOnUnparseable(t *testing.T) {
	if v, ok := firstFloat(Row{"total_exec_time": "abc", "total_time": "99"}, "total_exec_time", "total_time"); ok {
		t.Errorf("firstFloat = %v, want not-ok (a present-but-bad cell must not fall through to the alternate)", v)
	}
	if v, ok := firstFloat(Row{"total_time": "99"}, "total_exec_time", "total_time"); !ok || v != 99 {
		t.Errorf("firstFloat = %v, %v; want 99, true", v, ok)
	}
	if _, ok := firstFloat(Row{}, "total_exec_time"); ok {
		t.Error("firstFloat on an empty row = ok, want not-ok")
	}
	if v, ok := firstInt(Row{"calls": "100"}, "calls"); !ok || v != 100 {
		t.Errorf("firstInt = %d, %v; want 100, true", v, ok)
	}
	// The documented conversion: parseValue is the package's one numeric parser and
	// firstInt truncates through it rather than adding a ParseInt call site.
	if v, ok := firstInt(Row{"calls": "1.9"}, "calls"); !ok || v != 1 {
		t.Errorf("firstInt(\"1.9\") = %d, %v; want 1, true", v, ok)
	}
	if _, ok := firstInt(Row{"calls": ""}, "calls"); ok {
		t.Error(`firstInt("") = ok, want not-ok`)
	}
}

// TestStatementsEmptyDatabaseStillMakesASnapshot is R3.14's answer-not-available-
// versus-zero rule applied to a database with no statements yet.
//
// The pg_stat_statements view has zero rows there and the info view has one. If the
// collector built snapshots from the statement rows, the round would report nothing
// for that database and the store could not tell "empty" from "not collected" —
// which is the distinction R3.14 exists to keep. Building the database list from the
// info rows is what makes the empty round visible.
//
// It also settles the info view's own two columns against the family: `dealloc` and
// `stats_reset` are not members of R9.5's family (S25's list is four, and neither is
// one), so a round whose only content is an info row exports zero samples.
func TestStatementsEmptyDatabaseStillMakesASnapshot(t *testing.T) {
	tgt := stmtTarget(16)
	c, _, _ := stmtFixture(t, tgt, nil, []Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")})

	snaps := collectStatements(t, c, tgt)
	if len(snaps) != 1 {
		t.Fatalf("got %d snapshots, want 1 (an empty database is still a database)", len(snaps))
	}
	if snaps[0].Source.Datname != "app" {
		t.Errorf("Datname = %q, want %q", snaps[0].Source.Datname, "app")
	}
	if len(snaps[0].Rows) != 0 {
		t.Errorf("Rows = %d, want 0", len(snaps[0].Rows))
	}
	if snaps[0].StatsReset.IsZero() {
		t.Error("StatsReset is zero, want the info view's 2026-11-01 epoch (R3.3's counter epoch)")
	}

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Samples) != 0 {
		t.Errorf("got %d samples from an empty database, want 0 (the info view's own columns are not R9.5 members)", len(res.Samples))
	}
	if res.RoundTrips != 2 {
		t.Errorf("RoundTrips = %d, want 2 (the round ran; only its export is empty)", res.RoundTrips)
	}
}

// TestStatementsGroupsOneSnapshotPerDatabase is the reason CollectStatements returns
// a slice where every other collector returns one value.
//
// A Target is a pod and pg_stat_statements is per-database, so one round covers N
// databases and produces N snapshots, each with its own epoch. The pairing is the
// part under test: a statement row's StatsReset must come from its own database's
// info row, not from whichever info row the collector read first — a snapshot that
// borrowed another database's epoch would make R3.3's reset check fire on a database
// that never reset.
func TestStatementsGroupsOneSnapshotPerDatabase(t *testing.T) {
	tgt := stmtTarget(16)
	appRows := []Row{
		stmtRow(t, tgt, map[string]string{"datname": "app", "queryid": "1", "query": "SELECT 1", "total_exec_time": "10"}),
		stmtRow(t, tgt, map[string]string{"datname": "app", "queryid": "2", "query": "SELECT 2", "total_exec_time": "20"}),
		stmtRow(t, tgt, map[string]string{"datname": "analytics", "queryid": "3", "query": "SELECT 3", "total_exec_time": "30"}),
	}
	info := []Row{
		stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00"),
		stmtInfoRow(t, tgt, "analytics", "2026-11-15 00:00:00+00"),
	}
	c, _, _ := stmtFixture(t, tgt, appRows, info)

	snaps := collectStatements(t, c, tgt)
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2 (one per database)", len(snaps))
	}

	byName := map[string]Snapshot{}
	for _, s := range snaps {
		byName[s.Source.Datname] = s
	}
	if len(byName) != 2 {
		t.Fatalf("snapshot datnames collide: %v", snaps)
	}
	app := byName["app"]
	if len(app.Rows) != 2 {
		t.Errorf("app has %d rows, want 2", len(app.Rows))
	}
	if want := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC); !app.StatsReset.Equal(want) {
		t.Errorf("app StatsReset = %s, want %s", app.StatsReset, want)
	}
	an := byName["analytics"]
	if len(an.Rows) != 1 {
		t.Errorf("analytics has %d rows, want 1", len(an.Rows))
	}
	if want := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC); !an.StatsReset.Equal(want) {
		t.Errorf("analytics StatsReset = %s, want %s (each database's epoch comes from its own info row)", an.StatsReset, want)
	}
	// Every row keeps its own target: the snapshot embeds the Target the round ran
	// against, which is the store's R9.3 instance identity.
	for _, s := range snaps {
		if s.Source.Target != tgt {
			t.Errorf("snapshot source target = %+v, want %+v", s.Source.Target, tgt)
		}
	}
}

// TestStatementsRowWithoutDatnameBecomesItsOwnGroup covers the lost-Extra case.
//
// A statement row with no datname is a projection that dropped the Extra. Dropping
// the row would hide it; folding it into a neighbouring database's snapshot would
// attribute a statement to a database it did not run in. It becomes its own group
// keyed "" so store.validateSnapshot rejects it with its own message ("snapshot
// source has no datname") — the collector's job is to report what it read.
func TestStatementsRowWithoutDatnameBecomesItsOwnGroup(t *testing.T) {
	tgt := stmtTarget(16)
	rows := []Row{
		stmtRow(t, tgt, map[string]string{"datname": "app", "queryid": "1", "query": "SELECT 1"}),
		stmtRow(t, tgt, map[string]string{"datname": "", "queryid": "2", "query": "SELECT 2"}),
	}
	c, _, _ := stmtFixture(t, tgt, rows, []Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")})

	snaps := collectStatements(t, c, tgt)
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2 (the nameless row is its own group, not a dropped row)", len(snaps))
	}
	var nameless *Snapshot
	for i := range snaps {
		if snaps[i].Source.Datname == "" {
			nameless = &snaps[i]
		}
	}
	if nameless == nil {
		t.Fatal("no snapshot with an empty datname")
	}
	if len(nameless.Rows) != 1 || nameless.Rows[0].QueryID != "2" {
		t.Errorf("nameless snapshot rows = %+v, want the one row that had no datname", nameless.Rows)
	}
	if !nameless.StatsReset.IsZero() {
		t.Errorf("nameless snapshot StatsReset = %s, want zero (no info row, so no epoch — R3.3's fallback, not a guess)", nameless.StatsReset)
	}
}

// TestStatementsCapturedAtIsTheRowClock is R13.11 on this collector.
//
// Project appends `now() AS pgsi_source_now` to every query precisely so a collector
// on a pod with clock drift can still report when the instance measured, and
// store.Snapshot.CapturedAt is the field that carries it. The fixture makes the two
// clocks differ by 2.5 hours (fixedClock 12:30, the row's 10:00), so a CapturedAt of
// 10:00 can only have come from the row and a CapturedAt of 12:30 can only have come
// from cfg.Now().
func TestStatementsCapturedAtIsTheRowClock(t *testing.T) {
	tgt := stmtTarget(16)
	c, _, _ := stmtFixture(t, tgt,
		[]Row{stmtRow(t, tgt, map[string]string{"queryid": "1", "query": "SELECT 1"})},
		[]Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")})

	snap := collectStatements(t, c, tgt)[0]
	if !snap.CapturedAt.Equal(sourceNow) {
		t.Errorf("CapturedAt = %s, want %s (the row's pgsi_source_now, not the local clock at %s)",
			snap.CapturedAt, sourceNow, fixedClock)
	}
	if !snap.IngestedAt.Equal(fixedClock) {
		t.Errorf("IngestedAt = %s, want %s (SQLConfig.Now is the field R13.11 wants the local clock in)",
			snap.IngestedAt, fixedClock)
	}

	// An unparseable source timestamp falls back to the local clock rather than to
	// the zero value: clock.go treats a zero SourceTime as "no source clock", and a
	// snapshot that lost the column should not claim an unknown clock when the
	// collector's own clock is right there.
	c2, _, _ := stmtFixture(t, tgt,
		[]Row{stmtRow(t, tgt, map[string]string{"queryid": "1", "query": "SELECT 1", SourceNowColumn: "not a timestamp"})},
		[]Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")})
	snap2 := collectStatements(t, c2, tgt)[0]
	if !snap2.CapturedAt.Equal(fixedClock) {
		t.Errorf("CapturedAt with an unparseable source clock = %s, want %s (the cfg clock, not the zero value)",
			snap2.CapturedAt, fixedClock)
	}
}

// TestStatementsMetricFamily is S25's member list, the names naming.go gives them,
// the ms→s scale R9.4's family name asks for, and R9.5b's label pair — checked on one
// row so a wrong member, a wrong name, or a missing division is attributable.
func TestStatementsMetricFamily(t *testing.T) {
	tgt := stmtTarget(16)
	sql := "SELECT * FROM orders WHERE id = 42"
	c, _, _ := stmtFixture(t, tgt,
		[]Row{stmtRow(t, tgt, map[string]string{"queryid": "4520629923479686359", "query": sql})},
		[]Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")})

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Samples) != len(statementMembers) {
		t.Fatalf("got %d samples, want %d (one per S25 member)", len(res.Samples), len(statementMembers))
	}

	fp := query.Normalize(sql, query.Options{}).Fingerprint
	want := map[string]float64{
		"query_execution_time_seconds_total": 1.5, // 1500 ms / 1000
		"query_rows_total":                   12,
		"query_shared_blocks_hit_total":      34,
		"query_shared_blocks_read_total":     56,
	}
	got := map[string]float64{}
	for _, s := range res.Samples {
		got[s.Metric] = s.Value
		if len(s.Labels) != 2 {
			t.Errorf("%s labels = %v, want exactly {fingerprint, queryid} (the four identity labels are the scheduler's, and R9.5b names no others)", s.Metric, s.Labels)
			continue
		}
		if s.Labels["fingerprint"] != fp {
			t.Errorf("%s fingerprint label = %q, want %q (query.Normalize's output — the same function the store uses, which is what makes R9.5b's join work)",
				s.Metric, s.Labels["fingerprint"], fp)
		}
		if s.Labels["queryid"] != "4520629923479686359" {
			t.Errorf("%s queryid label = %q, want %q", s.Metric, s.Labels["queryid"], "4520629923479686359")
		}
		if !s.SourceTime.Equal(sourceNow) || !s.IngestTime.Equal(fixedClock) {
			t.Errorf("%s SourceTime/IngestTime = %s/%s, want %s/%s", s.Metric, s.SourceTime, s.IngestTime, sourceNow, fixedClock)
		}
		if s.StatsReset.IsZero() {
			t.Errorf("%s StatsReset is zero, want the snapshot's epoch (R3.3 needs it on every sample of this family)", s.Metric)
		}
	}
	for name, wantV := range want {
		if got[name] != wantV {
			t.Errorf("%s = %v, want %v", name, got[name], wantV)
		}
	}
	// The names come from naming.go, not from this file's own list of strings.
	for _, m := range statementMembers {
		if n := MetricName(pgversion.ViewStatStatements, m.column); want[n] == 0 {
			t.Errorf("MetricName(pg_stat_statements, %q) = %q, which is not one of the four this family exports", m.column, n)
		}
	}
}

// TestStatementsTopNIsPerClusterNotPerDatabase is R9.5:397's parenthetical.
//
// "default **N=50 per cluster** (see §5 — `metrics.topN` is per cluster, not per
// database)". The obvious implementation — apply N inside the per-snapshot loop, the
// shape every other collector has — gives a pod hosting three databases 3 × N rows,
// which is the misreading the parenthetical exists to prevent and which becomes live
// the moment the round fans out to more than one database. So the fixture gives one
// round two databases with three rows each and asks for N=4: the answer is four rows
// total, two from each database, not four from each.
func TestStatementsTopNIsPerClusterNotPerDatabase(t *testing.T) {
	tgt := stmtTarget(16)
	rows := []Row{
		stmtRow(t, tgt, map[string]string{"datname": "app", "queryid": "a1", "query": "SELECT a1", "total_exec_time": "100"}),
		stmtRow(t, tgt, map[string]string{"datname": "app", "queryid": "a2", "query": "SELECT a2", "total_exec_time": "90"}),
		stmtRow(t, tgt, map[string]string{"datname": "app", "queryid": "a3", "query": "SELECT a3", "total_exec_time": "80"}),
		stmtRow(t, tgt, map[string]string{"datname": "ops", "queryid": "b1", "query": "SELECT b1", "total_exec_time": "70"}),
		stmtRow(t, tgt, map[string]string{"datname": "ops", "queryid": "b2", "query": "SELECT b2", "total_exec_time": "60"}),
		stmtRow(t, tgt, map[string]string{"datname": "ops", "queryid": "b3", "query": "SELECT b3", "total_exec_time": "50"}),
	}
	info := []Row{
		stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00"),
		stmtInfoRow(t, tgt, "ops", "2026-11-01 00:00:00+00"),
	}
	col, _, _ := stmtFixture(t, tgt, rows, info)
	col.WithTopN(4)
	res, err := col.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// 4 rows × 4 members = 16 samples. Per-snapshot windowing would give 8 rows
	// (4 from each database, and there are only 3 in each) — 4 × 4 = 16 from app
	// alone plus 4 from ops, i.e. 20 samples.
	if len(res.Samples) != 16 {
		t.Errorf("got %d samples, want 16 (4 rows × 4 members; per-snapshot windowing would emit more)", len(res.Samples))
	}
	seen := map[string]bool{}
	for _, s := range res.Samples {
		if s.Metric != "query_execution_time_seconds_total" {
			continue
		}
		seen[s.Labels["queryid"]] = true
	}
	want := map[string]bool{"a1": true, "a2": true, "a3": true, "b1": true}
	for q := range want {
		if !seen[q] {
			t.Errorf("queryid %s not exported; the top 4 by total_exec_time are a1,a2,a3,b1 (got %v)", q, keysOfBool(seen))
		}
	}
	if len(seen) != 4 {
		t.Errorf("exported %d distinct queryids, want 4 (got %v)", len(seen), keysOfBool(seen))
	}
}

func keysOfBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestStatementsRankingIsByTotalExecTime is R10.7's ranking rule, which names itself
// as R9.5's: "Choosing by `total_exec_ms` is the rule R9.5 already applies to pick
// which queries to export ... naming it here stops a second implementation choosing
// alphabetically".
//
// The fixture feeds the rows in ascending order so an implementation that exported
// them in row order fails, and the tie-break case — two rows with one value and one
// template — pins the secondary key so the exported set is stable across rounds.
func TestStatementsRankingIsByTotalExecTime(t *testing.T) {
	tgt := stmtTarget(16)
	rows := []Row{
		stmtRow(t, tgt, map[string]string{"queryid": "d", "query": "SELECT d", "total_exec_time": "70"}),
		stmtRow(t, tgt, map[string]string{"queryid": "c", "query": "SELECT c", "total_exec_time": "80"}),
		stmtRow(t, tgt, map[string]string{"queryid": "b", "query": "SELECT b", "total_exec_time": "90"}),
		stmtRow(t, tgt, map[string]string{"queryid": "a", "query": "SELECT a", "total_exec_time": "100"}),
	}
	info := []Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")}

	col, _, _ := stmtFixture(t, tgt, rows, info)
	col.WithTopN(2)
	res, err := col.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	seen := map[string]float64{}
	for _, s := range res.Samples {
		if s.Metric == "query_execution_time_seconds_total" {
			seen[s.Labels["queryid"]] = s.Value
		}
	}
	if len(seen) != 2 || seen["a"] != 0.1 || seen["b"] != 0.09 {
		t.Errorf("top 2 by total_exec_time = %v, want {a:0.1, b:0.09}", seen)
	}

	// Tie-break: same value, same template (so the same fingerprint), different
	// queryids. The lower queryid wins, so the set a round exports does not depend on
	// the order the driver returned the rows in.
	tie := []Row{
		stmtRow(t, tgt, map[string]string{"queryid": "zz", "query": "SELECT same", "total_exec_time": "50"}),
		stmtRow(t, tgt, map[string]string{"queryid": "aa", "query": "SELECT same", "total_exec_time": "50"}),
	}
	col2, _, _ := stmtFixture(t, tgt, tie, info)
	col2.WithTopN(1)
	res2, err := col2.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res2.Samples) != len(statementMembers) {
		t.Fatalf("got %d samples, want %d", len(res2.Samples), len(statementMembers))
	}
	for _, s := range res2.Samples {
		if s.Labels["queryid"] != "aa" {
			t.Errorf("tie-break exported queryid %q, want %q (equal values, equal fingerprints → the lower id)",
				s.Labels["queryid"], "aa")
		}
	}
}

// TestStatementsTopNZeroDisablesTheFamilyNotTheRound is R9.5's "N=0 disables the
// family" — and the half that sentence does not say, which is that it disables only
// the family. The store still needs the rows (R3.3's deltas need baselines for
// queries that fell out of the export window), so the round still runs and still
// counts its round-trips.
func TestStatementsTopNZeroDisablesTheFamilyNotTheRound(t *testing.T) {
	tgt := stmtTarget(16)
	rows := []Row{stmtRow(t, tgt, map[string]string{"queryid": "1", "query": "SELECT 1"})}
	info := []Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")}

	col, _, _ := stmtFixture(t, tgt, rows, info)
	col.WithTopN(0)

	res, err := col.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Samples) != 0 {
		t.Errorf("got %d samples with N=0, want 0", len(res.Samples))
	}
	if res.RoundTrips != 2 {
		t.Errorf("RoundTrips = %d, want 2 (the round ran; only its export is disabled)", res.RoundTrips)
	}
	snaps, _, err := col.CollectStatements(context.Background(), tgt)
	if err != nil {
		t.Fatalf("CollectStatements: %v", err)
	}
	if len(snaps) != 1 || len(snaps[0].Rows) != 1 {
		t.Errorf("N=0 cost the store its rows: %+v", snaps)
	}
}

// TestStatementsWithTopNAndValidTopN checks the range rule and the two places it is
// applied.
//
// ValidTopN is what a config layer uses to reject 1001 (R9.5's range is 0–1000);
// WithTopN clamps instead of erroring, for the reason query.ValidLength's doc gives:
// a range is a configuration rule, and a library that refuses to normalise is a
// library that made a config question its own. The two must agree at the boundaries,
// which is what this checks — plus the two clamps, which are only observable on the
// field.
func TestStatementsWithTopNAndValidTopN(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want bool
	}{
		{-1, false}, {0, true}, {1, true}, {50, true}, {999, true}, {1000, true}, {1001, false}, {5000, false},
	} {
		if got := ValidTopN(tc.n); got != tc.want {
			t.Errorf("ValidTopN(%d) = %v, want %v", tc.n, got, tc.want)
		}
	}

	c := NewStatementsCollector(nil, SQLConfig{})
	if c.topN != DefaultTopN {
		t.Errorf("default topN = %d, want %d (R9.5's default)", c.topN, DefaultTopN)
	}
	if got := c.WithTopN(-1).topN; got != 0 {
		t.Errorf("WithTopN(-1) → %d, want 0 (R9.5's floor)", got)
	}
	if got := c.WithTopN(2000).topN; got != MaxTopN {
		t.Errorf("WithTopN(2000) → %d, want %d (R9.5's ceiling)", got, MaxTopN)
	}
	if got := c.WithTopN(7).topN; got != 7 {
		t.Errorf("WithTopN(7) → %d, want 7", got)
	}
}

// TestStatementsFingerprintIsTheNormalizerOutput pins R9.5b's identifier label to
// the one function that mints it, and records the one thing WithNormalizer cannot do.
//
// The store's sentence is the requirement: "R3.5's rules and R3.6's length cap
// applied in one place is what keeps the fingerprint the API serves identical to the
// one the collector stored." internal/store/statements.go:370 is that place for the
// store — query.Normalize(r.SQL, s.cfg.Normalizer) — and this collector's samplesFor
// calls the same function on the same string, which is what makes R9.5b's
// fingerprint-as-join-key work at all. So the test's real content is the equality:
// the label on a sample is byte-identical to what the store would compute for the row
// the sample came from.
//
// What it cannot do is vary. Options has one field, Length, and Normalize applies it
// to DisplayText only — Template, and therefore Fingerprint, are computed from the
// whole template before the cut (normalizer.go:147-158). So the label is invariant to
// the collector's Options, and a test that fed Length:10000 and expected a different
// fingerprint would be testing a thing that is not true. The setting still matters —
// it is the collector's copy of the deployment's R3.6 cap, and R9.5b's fixed-length
// digest is what keeps the family at §5's 300 series per cluster instead of one
// variable-length string per query — but it does not reach this label. Both halves
// are asserted here: the equality, and the invariance.
func TestStatementsFingerprintIsTheNormalizerOutput(t *testing.T) {
	tgt := stmtTarget(16)
	// A statement longer than R3.6's default 1 024-byte cap, so the two Options
	// genuinely disagree about DisplayText even though they agree on the fingerprint.
	long := "SELECT " + strings.Repeat("col_a, col_b, ", 200) + "1"
	if len(long) <= 1024 {
		t.Fatalf("fixture is %d bytes, want longer than the 1024 default cap", len(long))
	}
	rows := []Row{stmtRow(t, tgt, map[string]string{"queryid": "1", "query": long})}
	info := []Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")}

	col, _, _ := stmtFixture(t, tgt, rows, info)
	res, err := col.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := query.Normalize(long, query.Options{})
	if len(res.Samples) != len(statementMembers) {
		t.Fatalf("got %d samples, want %d", len(res.Samples), len(statementMembers))
	}
	for _, s := range res.Samples {
		got := s.Labels["fingerprint"]
		if got != want.Fingerprint {
			t.Errorf("%s fingerprint = %q, want %q (query.Normalize's output, which is what store/statements.go:370 computes for the same row)",
				s.Metric, got, want.Fingerprint)
		}
		// R9.5b's shape: 64 lowercase hex, because it is a SHA-256 digest. The
		// fixed length is the cardinality answer, and R8.2 puts the same string in
		// a URL path segment, which is why it has to be URL-safe.
		if len(got) != 64 {
			t.Errorf("%s fingerprint is %d chars, want 64 (a sha256 hex digest)", s.Metric, len(got))
		}
		if got != strings.ToLower(got) {
			t.Errorf("%s fingerprint %q is not lowercase hex", s.Metric, got)
		}
	}

	// The invariance half: change the collector's Options and the label does not
	// move, because Length cuts DisplayText and not Template.
	col2, _, _ := stmtFixture(t, tgt, rows, info)
	col2.WithNormalizer(query.Options{Length: 10000})
	res2, err := col2.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range res2.Samples {
		if s.Labels["fingerprint"] != want.Fingerprint {
			t.Errorf("WithNormalizer(Length:10000) moved the %s fingerprint to %q, want it unchanged at %q "+
				"(Options.Length truncates DisplayText, not Template — normalizer.go:147-158)",
				s.Metric, s.Labels["fingerprint"], want.Fingerprint)
		}
	}
}

// TestStatementsQueryIDLabelIsOmittedWhenUnset keeps the label set honest.
//
// StatementRow.QueryID is a string because dto.QueryView.QueryID is one (a JSON
// number past 2^53 is an id JavaScript rounds), and an empty one means the view gave
// us nothing — a NULL queryid, which is what a statement the planner did not assign an
// id to looks like. R9.5b makes queryid informational, so the right answer is to omit
// it, not to label a series with the empty string.
func TestStatementsQueryIDLabelIsOmittedWhenUnset(t *testing.T) {
	tgt := stmtTarget(16)
	rows := []Row{stmtRow(t, tgt, map[string]string{"queryid": "", "query": "SELECT 1"})}
	col, _, _ := stmtFixture(t, tgt, rows, []Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")})

	res, err := col.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range res.Samples {
		v, ok := s.Labels["queryid"]
		if ok {
			t.Errorf("%s carries queryid %q, want the label absent", s.Metric, v)
		}
		if s.Labels["fingerprint"] == "" {
			t.Errorf("%s has an empty fingerprint label", s.Metric)
		}
	}
}
