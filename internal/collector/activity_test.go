package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract, matching database_test.go: the activity
// collector is a Collector, checked by the compiler rather than by a test that has
// to remember to call it.
var _ Collector = (*ActivityCollector)(nil)

// actTarget is a PG-target at an explicit major.
//
// tgtPG hardwires 16, and half of what this collector does is conditional on the
// major — pg_wait_events exists on 17 and 18 and not below, query_id exists from
// 14 — so the activity tests need to name the major rather than inherit one.
func actTarget(major int) Target {
	return Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
}

// actRow builds one pg_stat_activity Row for a target's major.
//
// It is actRow rather than a reuse of database_test.go's rowFor because rowFor's
// default value is "7" for every non-name column, and for pg_stat_activity that
// default is wrong in ways the test would have to undo one column at a time: state
// "7" is not a state, query "7" is not a statement, and the whole point of
// TestActivitySessionFields is to check that the fields are read from the right
// columns. So the defaults here are the values a real active session row has, and
// `over` is how a test changes the one field it is testing.
func actRow(t *testing.T, tgt Target, over map[string]string) Row {
	t.Helper()
	p, ok := Project(pgversion.ViewStatActivity, tgt.PGMajor, activitySpecs[pgversion.ViewStatActivity])
	if !ok {
		t.Fatalf("PG%d: no projection for pg_stat_activity", tgt.PGMajor)
	}
	row := Row{}
	for _, col := range p.Columns {
		switch col {
		case SourceNowColumn:
			row[col] = fixedSourceNow
		case "datname":
			row[col] = "app"
		case "state":
			row[col] = "active"
		case "backend_type":
			row[col] = "client backend"
		case "query":
			row[col] = "SELECT 1"
		case "application_name":
			row[col] = "psql"
		case "usename":
			row[col] = "appuser"
		case "client_addr":
			row[col] = "10.0.0.5"
		default:
			row[col] = "7"
		}
	}
	for k, v := range over {
		row[k] = v
	}
	return row
}

// actFixture builds the collector plus the opener and tx it runs on, serving the
// given session rows for pg_stat_activity and the given (type, event) pairs for
// pg_wait_events on the majors that have it.
//
// The wait-events argument is a pair list rather than a Row list because
// pg_wait_events's projection is three columns and a test that wants to say "the
// catalogue has Lock/transactionid" should not have to restate `description`.
func actFixture(t *testing.T, tgt Target, sessions []Row, waitEvents [][2]string) (*ActivityCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	projections, err := NewActivityCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range projections {
		switch pv.view {
		case pgversion.ViewStatActivity:
			tx.rows[pv.Query] = sessions
		case pgversion.ViewWaitEvents:
			rows := make([]Row, 0, len(waitEvents))
			for _, pair := range waitEvents {
				rows = append(rows, Row{
					"type":          pair[0],
					"name":          pair[1],
					"description":   "fake",
					SourceNowColumn: fixedSourceNow,
				})
			}
			tx.rows[pv.Query] = rows
		}
	}
	opener := &fakeOpener{tx: tx}
	return NewActivityCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener, tx
}

func TestActivityDescriptor(t *testing.T) {
	c, _, _ := actFixture(t, actTarget(16), nil, nil)
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorActivity {
		t.Errorf("Name = %q, want %q (§3.3's row name, and the R2.2 `collector` label)", d.Name, CollectorActivity)
	}
	if d.Interval != ActivityInterval {
		t.Errorf("Interval = %s, want %s (R4.2's 15 s)", d.Interval, ActivityInterval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %s, want %s (§3.3's S2)", d.Source, SourceSQL)
	}
	if d.Cost != CostCheap {
		t.Errorf("Cost = %s, want %s (R4.2 calls the query cheap, which is what justifies the 15 s interval)",
			d.Cost, CostCheap)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != activityViewGroup {
		t.Errorf("ViewGroups = %v, want [%s] (R2.7: one entry per view group, and this collector has one)",
			d.ViewGroups, activityViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default)", d.Timeout)
	}
}

// TestActivityAppliesToEveryMajor pins the applicability rule, and the reason
// pg_wait_events is not in the declared view set.
//
// pg_stat_activity exists on all six matrix majors, so the collector applies to all
// six. If pg_wait_events were declared alongside it, AppliesToViews' all-views rule
// would make the collector inapplicable on PG13–PG16 — R4.1's table would stop
// working on four majors because R4.3's optional catalogue does not exist there.
// This test is the one that fails if someone "fixes" that by adding the view.
func TestActivityAppliesToEveryMajor(t *testing.T) {
	c := NewActivityCollector(nil, SQLConfig{})
	d := c.Descriptor()

	for _, major := range pgversion.Majors() {
		if !d.Applies(major) {
			t.Errorf("Applies(%d) = false, want true (pg_stat_activity has a column set on every matrix major)", major)
		}
	}
	for _, major := range []int{12, 19} {
		if d.Applies(major) {
			t.Errorf("Applies(%d) = true, want false", major)
		}
	}
	// The declared set being exactly pg_stat_activity is what the loop above cannot
	// prove: a set that also contained pg_wait_events still Applies() true on the
	// majors where both exist. TestActivityWaitEventsReadConditionally is the
	// assertion that enumerates what the collector actually reads, so it is the
	// check and this is the pointer to it — AppliesTo is an Applicability func
	// (collector.go:166), not a value that can be compared to a name.
}

// TestActivityWaitEventsReadConditionally is R4.3's "(PG17+)" parenthetical turned
// into an assertion: the pg_wait_events query is issued on 17 and 18 and nowhere
// else, and pg_stat_activity is issued on all six.
func TestActivityWaitEventsReadConditionally(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := actTarget(major)
		projections, err := NewActivityCollector(nil, SQLConfig{}).projections(tgt)
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}

		var activity, waitEvents int
		for _, pv := range projections {
			switch pv.view {
			case pgversion.ViewStatActivity:
				activity++
			case pgversion.ViewWaitEvents:
				waitEvents++
			}
		}
		if activity != 1 {
			t.Errorf("PG%d: %d pg_stat_activity projections, want exactly 1", major, activity)
		}

		want := 0
		if pgversion.Exists(pgversion.ViewWaitEvents, major) {
			want = 1
		}
		if waitEvents != want {
			t.Errorf("PG%d: %d pg_wait_events projections, want %d (R4.3 validates on the versions that have the catalogue)",
				major, waitEvents, want)
		}
	}
}

// TestActivityQueriesAreSchemaQualified checks R2.5 on every major: the FROM
// clause names pg_catalog, and the projection carries R4.4's pg_blocking_pids
// Extra with its schema qualification intact.
func TestActivityQueriesAreSchemaQualified(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := actTarget(major)
		projections, err := NewActivityCollector(nil, SQLConfig{}).projections(tgt)
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}
		for _, pv := range projections {
			if !strings.Contains(pv.Query, "FROM pg_catalog."+string(pv.view)) {
				t.Errorf("PG%d %s: query lacks the schema-qualified FROM:\n%s", major, pv.view, pv.Query)
			}
			if pv.view == pgversion.ViewStatActivity {
				if !strings.Contains(pv.Query, "pg_catalog.pg_blocking_pids(pid)") {
					t.Errorf("PG%d: query lacks R4.4's schema-qualified pg_blocking_pids(pid):\n%s", major, pv.Query)
				}
				if !strings.Contains(pv.Query, "AS "+ColumnBlockingPids) {
					t.Errorf("PG%d: query lacks the %s alias:\n%s", major, ColumnBlockingPids, pv.Query)
				}
			}
		}
	}
}

// TestActivityOneTransactionPerRound is R2.7's rule read off the fake: one Begin,
// and one query per view the collector reads.
func TestActivityOneTransactionPerRound(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := actTarget(major)
		wantQueries := 1
		if pgversion.Exists(pgversion.ViewWaitEvents, major) {
			wantQueries = 2
		}

		c, opener, tx := actFixture(t, tgt, []Row{actRow(t, tgt, nil)}, nil)
		_, res, err := c.CollectSessions(context.Background(), tgt)
		if err != nil {
			t.Fatalf("PG%d: CollectSessions: %v", major, err)
		}
		if opener.calls != 1 {
			t.Errorf("PG%d: %d Begin calls, want 1 (R2.7: one read-only transaction per view group)", major, opener.calls)
		}
		if len(tx.queries) != wantQueries {
			t.Errorf("PG%d: %d queries issued, want %d (one per view)", major, len(tx.queries), wantQueries)
		}
		if res.RoundTrips != wantQueries {
			t.Errorf("PG%d: RoundTrips = %d, want %d (R13.1 counts queries issued)", major, res.RoundTrips, wantQueries)
		}
		if tx.commits != 1 {
			t.Errorf("PG%d: %d commits, want 1", major, tx.commits)
		}
	}
}

// TestActivitySessionFields is R4.1's column list checked field by field against
// one row, which is the only way to catch a field wired to the wrong column.
//
// A field read from the wrong column is invisible to a test that asserts on one
// field at a time, because every value in the fixture is distinct and a
// field-to-column swap produces a session that is wrong in exactly one place and
// plausible in the rest. So this test asserts all of them from one row.
func TestActivitySessionFields(t *testing.T) {
	tgt := actTarget(16)
	row := actRow(t, tgt, map[string]string{
		"pid":              "4321",
		"query":            "UPDATE accounts SET bal = bal - 10 WHERE id = 7",
		"state":            "active",
		"wait_event_type":  "Lock",
		"wait_event":       "transactionid",
		"datname":          "billing",
		"application_name": "worker",
		"usename":          "svc",
		"client_addr":      "10.0.0.9",
		"client_port":      "51622",
		"backend_type":     "client backend",
		"query_id":         "4459823716093821447",
		"blocking_pids":    "{9001,9002}",
	})

	c, _, _ := actFixture(t, tgt, []Row{row}, nil)
	sessions := collectSessions(t, c, tgt)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]

	cases := []struct {
		field, got, want string
	}{
		{"Query", s.Query, "UPDATE accounts SET bal = bal - 10 WHERE id = 7"},
		{"Status", s.Status, "active"},
		{"WaitEventType", s.WaitEventType, "Lock"},
		{"WaitEvent", s.WaitEvent, "transactionid"},
		{"Database", s.Database, "billing"},
		{"ApplicationName", s.ApplicationName, "worker"},
		{"UserName", s.UserName, "svc"},
		{"ClientAddress", s.ClientAddress, "10.0.0.9"},
		{"ClientPort", s.ClientPort, "51622"},
		{"BackendType", s.BackendType, "client backend"},
		{"QueryID", s.QueryID, "4459823716093821447"},
		{"QueryType", s.QueryType, "UPDATE"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("Session.%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if s.Pid != 4321 {
		t.Errorf("Session.Pid = %d, want 4321", s.Pid)
	}
	if len(s.BlockingPids) != 2 || s.BlockingPids[0] != 9001 || s.BlockingPids[1] != 9002 {
		t.Errorf("Session.BlockingPids = %v, want [9001 9002]", s.BlockingPids)
	}
	// R4.5's Action is the API's to fill; the collector leaves it empty and the
	// test pins that, because a collector that guessed here would be answering a
	// question about a runtime flag it never read.
	if s.Action != "" {
		t.Errorf("Session.Action = %q, want empty (R4.5's --allow-actions gate is the API's, not the collector's)", s.Action)
	}
}

// collectSessions runs one round and returns the rows, failing the test on error.
//
// It is a function, not a method: a helper declared in a _test.go file still joins
// the production type's method set for anyone reading the package's docs, and this
// one exists only to collapse the fixture-plus-call-plus-error-check triple that
// every round-based test otherwise spells out.
func collectSessions(t *testing.T, c *ActivityCollector, tgt Target) []Session {
	t.Helper()
	sessions, _, err := c.CollectSessions(context.Background(), tgt)
	if err != nil {
		t.Fatalf("CollectSessions: %v", err)
	}
	return sessions
}

// TestActivityDurationsUseTheRowClock is R4.8's requirement in the one form a unit
// test can check offline.
//
// The requirement's [A] clause asserts "each row's durations match the SQL values
// within tolerance — with both numbers printed". Two numbers get printed because
// there are two ways to compute a duration and they differ by clock skew: our-now
// minus their-timestamp, or their-now minus their-timestamp. Only the second
// reproduces what a direct SELECT computes, because both of its operands come from
// the same row on the same server.
//
// The fixture makes the two answers differ by 2.5 hours — fixedClock is 12:30 and
// the row's pgsi_source_now is 10:00 — so a duration of 3600 can only have come
// from the row. A collector that used cfg.Now() would report 12600 and fail here,
// and would also be the collector R13.11's skew check exists to catch.
func TestActivityDurationsUseTheRowClock(t *testing.T) {
	tgt := actTarget(16)
	row := actRow(t, tgt, map[string]string{
		"pid":           "1",
		"backend_start": "2026-11-22 09:00:00+00", // 1 h before the row's now()
		"xact_start":    "2026-11-22 09:30:00+00", // 30 min before
		"query_start":   "2026-11-22 09:59:30+00", // 30 s before
	})

	c, _, _ := actFixture(t, tgt, []Row{row}, nil)
	sessions := collectSessions(t, c, tgt)
	s := sessions[0]

	const eps = 1e-9
	for _, tc := range []struct {
		field string
		got   float64
		want  float64
	}{
		{"SessionDurationSeconds", s.SessionDurationSeconds, 3600},
		{"TransactionDurationSeconds", s.TransactionDurationSeconds, 1800},
		{"QueryDurationSeconds", s.QueryDurationSeconds, 30},
	} {
		if diff := tc.got - tc.want; diff > eps || diff < -eps {
			t.Errorf("Session.%s = %v, want %v (source_now 10:00:00 minus the row's own start; "+
				"a value computed from cfg.Now()=12:30:00 would be 2.5 h larger)", tc.field, tc.got, tc.want)
		}
	}
	if !s.SourceTime.Equal(sourceNow) {
		t.Errorf("Session.SourceTime = %s, want %s", s.SourceTime.Format(time.RFC3339), sourceNow.Format(time.RFC3339))
	}
}

// TestActivityNullDurations checks the two NULL timestamps the view produces in
// normal operation, and the third the fixture can produce.
//
// xact_start is NULL for every session not in a transaction and query_start is NULL
// for one that has not run a statement. A collector that parsed NULL as the epoch
// would report a duration of ~56 years for an idle session, which is the kind of
// number that looks like a real finding.
func TestActivityNullDurations(t *testing.T) {
	tgt := actTarget(16)
	row := actRow(t, tgt, map[string]string{
		"pid":           "1",
		"state":         "idle",
		"backend_start": "2026-11-22 09:00:00+00",
		"xact_start":    "",
		"query_start":   "",
	})
	c, _, _ := actFixture(t, tgt, []Row{row}, nil)
	sessions := collectSessions(t, c, tgt)
	s := sessions[0]
	if s.SessionDurationSeconds != 3600 {
		t.Errorf("SessionDurationSeconds = %v, want 3600", s.SessionDurationSeconds)
	}
	if s.TransactionDurationSeconds != 0 {
		t.Errorf("TransactionDurationSeconds = %v, want 0 (xact_start is NULL outside a transaction)", s.TransactionDurationSeconds)
	}
	if s.QueryDurationSeconds != 0 {
		t.Errorf("QueryDurationSeconds = %v, want 0 (query_start is NULL)", s.QueryDurationSeconds)
	}
}

// TestActivityDropsRowsWithoutPid is the pg_stat_activity analogue of database.go's
// shared-objects filter (D20's S8).
//
// R4.8's acceptance compares a *set of pids* against a direct SELECT, so a row
// whose pid is unreadable cannot be kept: it would enter the set as pid 0, a
// process the SELECT does not return. The database collector's version of this rule
// drops the row for the same reason — an identity the contract cannot express is
// not a row the contract should report.
func TestActivityDropsRowsWithoutPid(t *testing.T) {
	tgt := actTarget(16)
	rows := []Row{
		actRow(t, tgt, map[string]string{"pid": "100"}),
		actRow(t, tgt, map[string]string{"pid": ""}),
		actRow(t, tgt, map[string]string{"pid": "0"}),
		actRow(t, tgt, map[string]string{"pid": "-3"}),
		actRow(t, tgt, map[string]string{"pid": "not-a-number"}),
		actRow(t, tgt, map[string]string{"pid": "101"}),
	}
	c, _, _ := actFixture(t, tgt, rows, nil)
	sessions := collectSessions(t, c, tgt)
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2 (only pids 100 and 101 are usable)", len(sessions))
	}
	if sessions[0].Pid != 100 || sessions[1].Pid != 101 {
		t.Errorf("sessions are pids %d and %d, want 100 and 101", sessions[0].Pid, sessions[1].Pid)
	}
}

// TestActivityQueryErrorRollsBackAndReportsRoundTrips is the R13.1 partial-accounting
// rule on this collector: a round that issued one query before failing reports one
// round-trip, not zero, and rolls the transaction back.
func TestActivityQueryErrorRollsBackAndReportsRoundTrips(t *testing.T) {
	tgt := actTarget(17) // two queries: activity, then wait_events
	c, _, tx := actFixture(t, tgt, nil, nil)

	// Fail the *second* query. Failing the first would make the expected count
	// trivially 1 and hide the question worth asking: does a query that came back
	// with an error count as issued? It does — it was sent to the server and the
	// server answered, which is what a round-trip is — so with two queries issued
	// and the second one failing, R13.1's accounting says 2. Failing the first
	// query would let a collector that counted only successes pass this file.
	projections, _ := NewActivityCollector(nil, SQLConfig{}).projections(tgt)
	tx.failOn = projections[1].Query
	wantTrips := 2

	sessions, res, err := c.CollectSessions(context.Background(), tgt)
	if err == nil {
		t.Fatal("CollectSessions succeeded, want the query error")
	}
	if res.RoundTrips != wantTrips {
		t.Errorf("RoundTrips = %d, want %d (one query issued before the failure)", res.RoundTrips, wantTrips)
	}
	if tx.rollbacks != 1 {
		t.Errorf("%d rollbacks, want 1 (sql.go's Tx doc: rollback on every error path)", tx.rollbacks)
	}
	if tx.commits != 0 {
		t.Errorf("%d commits, want 0", tx.commits)
	}
	if sessions != nil {
		t.Errorf("sessions = %v on a failed round, want nil", sessions)
	}
}

// TestActivityCommitErrorRollsBack is D20's S9 on this collector: a failed Commit is
// an error path, and the rollback that closes the transaction is best-effort.
func TestActivityCommitErrorRollsBack(t *testing.T) {
	tgt := actTarget(16)
	c, _, tx := actFixture(t, tgt, nil, nil)
	tx.commitErr = errors.New("fake: commit failed")

	_, res, err := c.CollectSessions(context.Background(), tgt)
	if err == nil {
		t.Fatal("CollectSessions succeeded, want the commit error")
	}
	if tx.rollbacks != 1 {
		t.Errorf("%d rollbacks, want 1", tx.rollbacks)
	}
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
	}
}

// TestActivityBeginErrorIssuesNothing.
func TestActivityBeginErrorIssuesNothing(t *testing.T) {
	tgt := actTarget(16)
	opener := &fakeOpener{beginErr: errors.New("fake: begin failed")}
	c := NewActivityCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	_, res, err := c.CollectSessions(context.Background(), tgt)
	if err == nil {
		t.Fatal("CollectSessions succeeded, want the begin error")
	}
	if res.RoundTrips != 0 {
		t.Errorf("RoundTrips = %d, want 0 (a Begin that failed issued nothing)", res.RoundTrips)
	}
}

// TestActivityUnknownMajorIsRefused: the collector refuses to guess a column set.
func TestActivityUnknownMajorIsRefused(t *testing.T) {
	c := NewActivityCollector(&fakeOpener{}, SQLConfig{})
	_, _, err := c.CollectSessions(context.Background(), Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL})
	if err == nil {
		t.Fatal("CollectSessions succeeded on a target with no PGMajor, want a refusal")
	}
	if !strings.Contains(err.Error(), "no PostgreSQL major") {
		t.Errorf("error = %v, want it to name the missing major", err)
	}
}

// TestQueryTypeFrom pins the closed set and the comment-stripping.
//
// The closed set is the test's real subject: an open derivation (upper-case the
// first word) returns "9F86D081884C" for a statement that begins with a hex
// literal and "SELECT)" for one that begins with a paren, and once such a value is
// in the payload it is indistinguishable from a real query type. Everything the
// derivation cannot name becomes UNKNOWN, which is a value the UI can render as
// "—" and a test can assert on.
func TestQueryTypeFrom(t *testing.T) {
	cases := []struct{ query, want string }{
		{"SELECT 1", "SELECT"},
		{"select * from t", "SELECT"},
		{"  UPDATE t SET x = 1", "UPDATE"},
		{"DELETE FROM t;", "DELETE"},
		{"INSERT INTO t VALUES (1)", "INSERT"},
		{"WITH cte AS (SELECT 1) SELECT * FROM cte", "WITH"},
		{"BEGIN", "BEGIN"},
		{"COMMIT;", "COMMIT"},
		{"EXPLAIN ANALYZE SELECT 1", "EXPLAIN"},
		{"VACUUM ANALYZE", "VACUUM"},
		{"CREATE INDEX ON t (x)", "CREATE"},
		{"SET application_name = 'x'", "SET"},
		{"-- a comment\nSELECT 1", "SELECT"},
		{"/* block */\n  DELETE FROM t", "DELETE"},
		{"-- only a comment", "UNKNOWN"},
		{"/* unterminated", "UNKNOWN"},
		{"", "UNKNOWN"},
		{"   \t\n  ", "UNKNOWN"},
		{"9f86d081884c", "UNKNOWN"},
		{"(SELECT 1)", "UNKNOWN"},
	}
	for _, tc := range cases {
		if got := queryTypeFrom(tc.query); got != tc.want {
			t.Errorf("queryTypeFrom(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// TestParseBlockingPids covers the text forms of an integer[] plus the failure rule.
func TestParseBlockingPids(t *testing.T) {
	cases := []struct {
		raw  string
		want []int
	}{
		{"{}", nil},
		{"", nil},
		{"NULL", nil},
		{"{1}", []int{1}},
		{"{9001,9002}", []int{9001, 9002}},
		{"{ 9001 , 9002 , 9003 }", []int{9001, 9002, 9003}},
		{"4321", []int{4321}},
		// One unparseable element voids the whole array rather than yielding a
		// partial list: a chain built from two blockers where the array named five
		// is a chain that silently lost three, and nil is the honest answer.
		{"{1,x,3}", nil},
	}
	for _, tc := range cases {
		got := parseBlockingPids(tc.raw)
		if len(got) != len(tc.want) {
			t.Errorf("parseBlockingPids(%q) = %v, want %v", tc.raw, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseBlockingPids(%q) = %v, want %v", tc.raw, got, tc.want)
				break
			}
		}
	}
}

// sessionOf is a one-line Session builder for the pure-function tests, which need
// rows with specific fields and nothing else.
func sessionOf(pid int, status, query string, querySecs float64) Session {
	return Session{Pid: pid, Status: status, Query: query, QueryType: queryTypeFrom(query), QueryDurationSeconds: querySecs}
}

// TestSummarize is R4.1's scorecard: counts by state, longest-running query,
// longest idle-in-transaction.
func TestSummarize(t *testing.T) {
	sessions := []Session{
		sessionOf(1, "active", "SELECT 1", 5),
		sessionOf(2, "active", "SELECT 2", 90),
		{Pid: 3, Status: "idle in transaction", Query: "UPDATE t SET x=1", QueryType: "UPDATE",
			QueryDurationSeconds: 4, TransactionDurationSeconds: 700},
		{Pid: 4, Status: "idle in transaction", Query: "UPDATE u SET x=1", QueryType: "UPDATE",
			QueryDurationSeconds: 2, TransactionDurationSeconds: 1200},
		sessionOf(5, "idle", "SELECT 3", 1),
	}
	sc := Summarize(sessions)

	if sc.Total != 5 {
		t.Errorf("Total = %d, want 5", sc.Total)
	}
	want := map[string]int{"active": 2, "idle in transaction": 2, "idle": 1}
	for state, n := range want {
		if sc.ByState[state] != n {
			t.Errorf("ByState[%s] = %d, want %d", state, sc.ByState[state], n)
		}
	}
	if len(sc.ByState) != len(want) {
		t.Errorf("ByState has %d entries, want %d", len(sc.ByState), len(want))
	}
	// The two "longest" rows are different sessions on purpose: pid 2 has the
	// longest running query, pid 4 the longest transaction. A single-field
	// implementation would report one of them and satisfy half the requirement.
	if sc.LongestQuery == nil || sc.LongestQuery.Pid != 2 {
		t.Errorf("LongestQuery = %v, want pid 2 (90 s)", sc.LongestQuery)
	}
	if sc.LongestIdleInTransaction == nil || sc.LongestIdleInTransaction.Pid != 4 {
		t.Errorf("LongestIdleInTransaction = %v, want pid 4 (1200 s)", sc.LongestIdleInTransaction)
	}
}

// TestSummarizeEmpty pins the empty round. R4.1's scorecard over zero sessions has
// an empty (not nil) ByState and two nil pointers, which is what lets the API emit
// `{"byState":{}}` rather than `"byState":null` under R8.3's never-null rule.
func TestSummarizeEmpty(t *testing.T) {
	sc := Summarize(nil)
	if sc.ByState == nil {
		t.Error("ByState is nil; R8.3's rule is to say \"none\" with an empty map")
	}
	if len(sc.ByState) != 0 || sc.Total != 0 {
		t.Errorf("ByState = %v, Total = %d, want empty and 0", sc.ByState, sc.Total)
	}
	if sc.LongestQuery != nil || sc.LongestIdleInTransaction != nil {
		t.Error("a zero-session scorecard has a longest query")
	}
}

// TestWaitEventRollup is R4.3's "roll up wait_event_type × wait_event counts".
func TestWaitEventRollup(t *testing.T) {
	sessions := []Session{
		sessionOf(1, "active", "SELECT 1", 1), // no wait event
		{Pid: 2, Status: "active", WaitEventType: "Lock", WaitEvent: "transactionid"},
		{Pid: 3, Status: "active", WaitEventType: "Lock", WaitEvent: "transactionid"},
		{Pid: 4, Status: "active", WaitEventType: "Lock", WaitEvent: "tuple"},
		{Pid: 5, Status: "active", WaitEventType: "IO", WaitEvent: "DataFileRead", UnknownWaitEvent: true},
	}
	got := WaitEventRollup(sessions)

	want := []WaitEventCount{
		{Type: "Lock", Event: "transactionid", Count: 2},
		{Type: "IO", Event: "DataFileRead", Count: 1, Unknown: true},
		{Type: "Lock", Event: "tuple", Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("rollup has %d entries %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rollup[%d] = %+v, want %+v (count desc, then type, then event)", i, got[i], want[i])
		}
	}
	// The no-wait session contributes no row: R4.3's tabs are event types and
	// events, and "not waiting" is neither.
	for _, c := range got {
		if c.Type == "" && c.Event == "" {
			t.Error("rollup has an empty-string row; a session that is not waiting has no event type")
		}
	}
}

// TestValidateWaitEvents is R4.3's validation clause, including the two cases that
// are legal by construction.
func TestValidateWaitEvents(t *testing.T) {
	cat := map[waitKey]bool{
		{Type: "Lock", Event: "transactionid"}: true,
		{Type: "Lock", Event: "tuple"}:         true,
	}

	cases := []struct {
		name string
		s    Session
		cat  map[waitKey]bool
		want bool
	}{
		{"known pair", Session{WaitEventType: "Lock", WaitEvent: "transactionid"}, cat, true},
		{"unknown pair", Session{WaitEventType: "Lock", WaitEvent: "bogus"}, cat, false},
		// A session that is not waiting has NULL in both columns. R4.3's rollup is
		// over the sessions that are waiting, so an empty pair is not a finding.
		{"no wait event", Session{}, cat, true},
		{"empty type, named event", Session{WaitEvent: "transactionid"}, cat, false},
		// PG13–PG16 have no pg_wait_events. A validator that treated a nil catalogue
		// as "nothing is valid" would mark every PG16 event unknown, which is the
		// failure the requirement's "(PG17+)" exists to prevent.
		{"no catalogue (PG16)", Session{WaitEventType: "Lock", WaitEvent: "anything"}, nil, true},
	}
	for _, tc := range cases {
		if got := ValidateWaitEvents(tc.s, tc.cat); got != tc.want {
			t.Errorf("%s: ValidateWaitEvents = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestValidateWaitEventsMarksTheRow checks the field the rollup reads: the verdict
// is written onto the session during the round, not recomputed later.
func TestValidateWaitEventsMarksTheRow(t *testing.T) {
	tgt := actTarget(17)
	rows := []Row{
		actRow(t, tgt, map[string]string{"pid": "1", "wait_event_type": "Lock", "wait_event": "transactionid"}),
		actRow(t, tgt, map[string]string{"pid": "2", "wait_event_type": "Lock", "wait_event": "not-a-real-event"}),
	}
	c, _, _ := actFixture(t, tgt, rows, [][2]string{{"Lock", "transactionid"}, {"Lock", "tuple"}})
	sessions, _, err := c.CollectSessions(context.Background(), tgt)
	if err != nil {
		t.Fatalf("CollectSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].UnknownWaitEvent {
		t.Error("pid 1 marked unknown; Lock/transactionid is in the catalogue the same transaction read")
	}
	if !sessions[1].UnknownWaitEvent {
		t.Error("pid 2 not marked unknown; Lock/not-a-real-event is not in the catalogue")
	}
}

// TestValidateWaitEventsOnPG16: no catalogue exists, so nothing is flagged.
func TestValidateWaitEventsOnPG16(t *testing.T) {
	tgt := actTarget(16)
	c, _, _ := actFixture(t, tgt, []Row{actRow(t, tgt, map[string]string{"pid": "1", "wait_event_type": "Lock", "wait_event": "anything"})}, nil)
	sessions, _, err := c.CollectSessions(context.Background(), tgt)
	if err != nil {
		t.Fatalf("CollectSessions: %v", err)
	}
	if sessions[0].UnknownWaitEvent {
		t.Error("pid 1 marked unknown on PG16; there is no catalogue there to be absent from")
	}
}

// TestBlockingChains is R4.4's tree: the 3-level depth cap, the cycle cut, and a
// blocker whose row the query did not return.
func TestBlockingChains(t *testing.T) {
	// 1 is blocked by 2, 2 by 3, 3 by 4, 4 by 5. Depth 3 from pid 1 reaches
	// 2, 3, 4 and stops before 5 — R4.4's "up to 3 levels deep".
	sessions := []Session{
		{Pid: 1, Status: "active", Query: "SELECT 1", BlockingPids: []int{2}},
		{Pid: 2, Status: "active", Query: "SELECT 2", BlockingPids: []int{3}},
		{Pid: 3, Status: "active", Query: "SELECT 3", BlockingPids: []int{4}},
		{Pid: 4, Status: "active", Query: "SELECT 4", BlockingPids: []int{5}},
		{Pid: 5, Status: "active", Query: "SELECT 5"},
	}
	chains := BlockingChains(sessions)
	if len(chains) != 4 {
		t.Fatalf("got %d chains, want 4 (pids 1-4 have blockers; pid 5 blocks without being blocked)", len(chains))
	}
	var first BlockingChain
	for _, ch := range chains {
		if ch.Blocked.Pid == 1 {
			first = ch
		}
	}
	if len(first.Blockers) != 3 {
		t.Fatalf("chain from pid 1 has %d blockers %v, want 3 (R4.4's MaxChainDepth)", len(first.Blockers), first.Blockers)
	}
	for i, want := range []int{2, 3, 4} {
		if first.Blockers[i].Pid != want {
			t.Errorf("blocker %d = %d, want %d (nearest first)", i, first.Blockers[i].Pid, want)
		}
	}
	// R4.4's "with the blocking statement text shown for each ancestor": the
	// ancestors are carried as full sessions, so their text is in the chain.
	for i, b := range first.Blockers {
		if b.Query == "" {
			t.Errorf("blocker %d has no query text; R4.4 requires it for each ancestor", i)
		}
	}
}

// TestBlockingChainsCutsCycles: A blocks B blocks A. Without a visited set the
// traversal visits A twice inside the depth budget and renders the same statement
// twice in one column of the tree.
func TestBlockingChainsCutsCycles(t *testing.T) {
	sessions := []Session{
		{Pid: 1, Status: "active", Query: "SELECT 1", BlockingPids: []int{2}},
		{Pid: 2, Status: "active", Query: "SELECT 2", BlockingPids: []int{1}},
	}
	chains := BlockingChains(sessions)
	if len(chains) != 2 {
		t.Fatalf("got %d chains, want 2", len(chains))
	}
	for _, ch := range chains {
		if n := distinctBlockers(ch); n != len(ch.Blockers) {
			t.Errorf("chain from pid %d has %d blockers but %d distinct ones: %v",
				ch.Blocked.Pid, len(ch.Blockers), n, ch.Blockers)
		}
	}
}

// distinctBlockers counts the unique pids among a chain's blockers. A chain whose
// blocker list is longer than its distinct set has the same session in it twice,
// which is what the visited set in BlockingChains exists to prevent.
func distinctBlockers(ch BlockingChain) int {
	seen := map[int]bool{}
	for _, b := range ch.Blockers {
		seen[b.Pid] = true
	}
	return len(seen)
}

// TestBlockingChainsMissingBlocker: a blocker in another database is a real blocker
// whose row this query did not return. The branch ends there rather than failing —
// the tree is shorter, not wrong.
func TestBlockingChainsMissingBlocker(t *testing.T) {
	chains := BlockingChains([]Session{{Pid: 1, Status: "active", Query: "SELECT 1", BlockingPids: []int{999}}})
	if len(chains) != 0 {
		t.Errorf("got %d chains for a blocked session whose blocker has no row, want 0 (an unresolvable edge is not a chain)", len(chains))
	}
}

// TestTopSessions pins R4.1's "top 50" cut and the ordering the requirement leaves
// unstated: active first, then idle-in-transaction, then the rest, longest first
// inside each group.
func TestTopSessions(t *testing.T) {
	sessions := []Session{
		sessionOf(1, "idle", "SELECT 1", 999),
		sessionOf(2, "active", "SELECT 2", 10),
		sessionOf(3, "idle in transaction", "SELECT 3", 500),
		sessionOf(4, "active", "SELECT 4", 20),
		sessionOf(5, "starting", "SELECT 5", 1),
	}
	got := TopSessions(sessions, 0)
	wantOrder := []int{4, 2, 3, 1, 5}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d sessions, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].Pid != want {
			t.Errorf("TopSessions[0:] pids = %v, want %v (active by duration, then idle-in-transaction, then the rest)",
				pidsOf(got), wantOrder)
			break
		}
	}
}

// TestTopSessionsCap checks the 50 and the copy.
func TestTopSessionsCap(t *testing.T) {
	sessions := make([]Session, 0, 60)
	for i := 0; i < 60; i++ {
		sessions = append(sessions, sessionOf(i+1, "active", "SELECT 1", float64(i)))
	}
	got := TopSessions(sessions, 0)
	if len(got) != MaxSessions {
		t.Errorf("TopSessions over 60 sessions returned %d, want %d (R4.1's top 50)", len(got), MaxSessions)
	}
	if got[0].Pid != 60 {
		t.Errorf("first session is pid %d, want 60 (the longest)", got[0].Pid)
	}

	// The cut must not mutate the input: the API returns the full set in the
	// scorecard's payload and the truncated one in the table, from the same slice.
	if len(sessions) != 60 {
		t.Errorf("input slice is %d long after the call, want 60 (TopSessions copies)", len(sessions))
	}
	n := TopSessions(sessions, 7)
	if len(n) != 7 {
		t.Errorf("TopSessions(_, 7) returned %d, want 7", len(n))
	}
}

func pidsOf(sessions []Session) []int {
	out := make([]int, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.Pid)
	}
	return out
}

// TestActivitySamples is the metrics half of R9.1's split: three families, named by
// naming.go, with the labels R9.7's catalogue implies.
func TestActivitySamples(t *testing.T) {
	sessions := []Session{
		sessionOf(1, "active", "SELECT 1", 1),
		sessionOf(2, "active", "SELECT 2", 1),
		{Pid: 3, Status: "idle in transaction", ApplicationName: "worker"},
		{Pid: 4, Status: "active", ApplicationName: "worker", WaitEventType: "Lock", WaitEvent: "transactionid"},
	}
	got := ActivitySamples(sessions, fixedClock)

	// Two families: 2 states (active, idle in transaction) + 2 application names
	// ("" and "worker"). The wait pair this test used to assert went with the
	// family (A3's S50 ruling on R4.3), and it is still covered where it lives
	// now: the Session rows themselves, which TestCollectSessions checks.
	byMetric := map[string][]Sample{}
	for _, s := range got {
		byMetric[s.Metric] = append(byMetric[s.Metric], s)
	}
	wantMetrics := []string{"db_backends_by_state", "db_backends_by_application_name"}
	if len(byMetric) != len(wantMetrics) {
		t.Errorf("got families %v, want %v", keysOf(byMetric), wantMetrics)
	}
	for _, m := range wantMetrics {
		if len(byMetric[m]) == 0 {
			t.Errorf("no samples for %s", m)
		}
	}

	// The names come from MetricName, so the family names here are the ones
	// naming.go's table and promql_catalogue.go agree on. Asserted per family
	// because a Sample.Metric that baked in the pgsi_ prefix would make
	// metrics.prefix a lie (clock.go's Sample.Metric doc is normative on this).
	for _, s := range got {
		if strings.HasPrefix(s.Metric, "pgsi_") {
			t.Errorf("Sample.Metric %q carries the prefix; R9.2 makes it configurable and metrics.go applies it at render time", s.Metric)
		}
		if s.IngestTime != fixedClock {
			t.Errorf("sample %s IngestTime = %s, want %s", s.Metric, s.IngestTime, fixedClock)
		}
	}

	// The values: active=2, idle in transaction=1.
	states := map[string]float64{}
	for _, s := range byMetric["db_backends_by_state"] {
		states[s.Labels["state"]] = s.Value
	}
	// Three active (pids 1, 2 and 4) and one idle-in-transaction (pid 3).
	if states["active"] != 3 || states["idle in transaction"] != 1 {
		t.Errorf("db_backends_by_state = %v, want active=3 idle in transaction=1", states)
	}
	apps := map[string]float64{}
	for _, s := range byMetric["db_backends_by_application_name"] {
		apps[s.Labels["application_name"]] = s.Value
	}
	// pids 1 and 2 have no application_name, pids 3 and 4 are "worker".
	if apps["worker"] != 2 || apps[""] != 2 {
		t.Errorf("db_backends_by_application_name = %v, want worker=2 and the empty name=2", apps)
	}
}

// TestCollectReturnsSamplesAndNoRows checks the Collector interface method's half of
// the split: it publishes the metrics and drops the rows.
func TestCollectReturnsSamplesAndNoRows(t *testing.T) {
	tgt := actTarget(16)
	rows := []Row{actRow(t, tgt, map[string]string{"pid": "1"}), actRow(t, tgt, map[string]string{"pid": "2"})}
	c, _, _ := actFixture(t, tgt, rows, nil)

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Samples) == 0 {
		t.Fatal("Collect returned no samples; the metrics half of R9.1's split is empty")
	}
	for _, s := range res.Samples {
		if s.Metric == "pg_stat_activity_pid" || s.Metric == "pg_stat_activity_query" {
			t.Errorf("Collect exported %s; naming.go names this as the anti-pattern and R9.1 puts it on the API plane", s.Metric)
		}
	}
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
	}
}

func keysOf(m map[string][]Sample) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestActivityExcludesItsOwnApplicationName is R2.8's filtering half on the view
// the requirement names. The setting half — application_name=pgsi-collector on
// every Begin, from SQLConfig — shipped in fea3571b; the filtering half was not
// there, so the collector queried pg_stat_activity and counted itself among the
// backends it was reporting. matrix.go:159-163 is the placement ruling this
// asserts: the exclusion is a per-collector concern, so the collector adds it and
// a matrix that emitted it for every view would be wrong for the views with no
// application_name column.
func TestActivityExcludesItsOwnApplicationName(t *testing.T) {
	tgt := actTarget(16)
	c := NewActivityCollector(nil, SQLConfig{})
	pps, err := c.projections(tgt)
	if err != nil {
		t.Fatalf("projections(PG%d): %v", tgt.PGMajor, err)
	}
	byView := map[pgversion.View]viewProjection{}
	for _, pv := range pps {
		byView[pv.view] = pv
	}
	act, ok := byView[pgversion.ViewStatActivity]
	if !ok {
		t.Fatalf("no projection for %s", pgversion.ViewStatActivity)
	}
	if !strings.Contains(act.Query, "WHERE application_name IS DISTINCT FROM 'pgsi-collector'") {
		t.Errorf("pg_stat_activity query does not filter the collector's own application_name:\n%s", act.Query)
	}
	// IS DISTINCT FROM, not <>: application_name is nullable and a NULL comparison
	// drops the row, so a plain inequality would exclude every unnamed session —
	// wider than "our own sessions", which is what the cell says to exclude.
	if strings.Contains(act.Query, "application_name <>") || strings.Contains(act.Query, "application_name !=") {
		t.Errorf("plain inequality on a nullable column drops unnamed sessions:\n%s", act.Query)
	}
	// The conditional second view has no application_name column to filter on.
	if we, ok := byView[pgversion.ViewWaitEvents]; ok && strings.Contains(we.Query, "application_name") {
		t.Errorf("pg_stat_wait_events query carries an application_name filter the view has no column for:\n%s", we.Query)
	}
	// The value comes from the config and is quoted, so a name with a quote in it
	// cannot break the query Project just assembled.
	c2 := NewActivityCollector(nil, SQLConfig{ApplicationName: "pgsi'x"})
	if got := c2.specFor(pgversion.ViewStatActivity).Where; !strings.Contains(got, "'pgsi''x'") {
		t.Errorf("application_name not SQL-quoted: %q", got)
	}
}
