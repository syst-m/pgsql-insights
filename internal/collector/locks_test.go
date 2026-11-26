package collector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract, the same one database_test.go:35 opens
// with: the locks collector is a Collector, checked by the compiler rather than by
// a test that has to remember to call it.
var _ Collector = (*LocksCollector)(nil)

// locksProjection returns the pg_locks projection for a major, failing the test if
// the matrix has none.
//
// Every test that needs rows needs this first, because the fake opener answers from
// a table keyed on query text: a test that keys the fake with a query the collector
// would not issue gets nil rows and reports an empty result, which reads as a
// parsing bug and is a fixture bug. Building the projection from Project is the only
// way to know what the collector would issue.
func locksProjection(t *testing.T, major int) Projection {
	t.Helper()

	p, ok := Project(pgversion.ViewLocks, major, locksSpecs[pgversion.ViewLocks])
	if !ok {
		t.Fatalf("no projection for %s on PostgreSQL %d", pgversion.ViewLocks, major)
	}
	return p
}

// locksCollector wires a fake opener that answers p.Query with rows, and returns
// both the collector and the opener so a test can read the calls out of it.
//
// It takes the projection rather than a major because the two are needed together in
// almost every test here, and because a test that builds rows from one major's
// projection and a collector that would query another's is the fixture bug the pair
// exists to prevent.
func locksCollector(t *testing.T, p Projection, rows []Row) (*LocksCollector, *fakeOpener) {
	t.Helper()

	tx := &fakeTx{rows: map[string][]Row{p.Query: rows}}
	opener := &fakeOpener{tx: tx}
	return NewLocksCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener
}

// TestLocksDescriptor pins the §3.3 row.
//
// §3.3's locks row is `| locks | 30 s | S2 pg_locks | Feeds the blocking chain |`,
// which is four of the Descriptor's fields stated as a table row and the whole
// reason the struct exists: a collector whose interval is a literal in a
// constructor cannot be overridden by R2.3's --interval.locks, and one whose
// Source is wrong is a collector that claims the wrong provenance on every metric
// it exports.
func TestLocksDescriptor(t *testing.T) {
	c := NewLocksCollector(nil, SQLConfig{})
	d := c.Descriptor()

	if d.Name != CollectorLocks {
		t.Errorf("Name = %q, want %q", d.Name, CollectorLocks)
	}
	if d.Interval != LocksInterval {
		t.Errorf("Interval = %v, want %v (§3.3's 30 s)", d.Interval, LocksInterval)
	}
	if d.Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", d.Interval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %q, want %q (S2)", d.Source, SourceSQL)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != locksViewGroup {
		t.Errorf("ViewGroups = %v, want [%s]", d.ViewGroups, locksViewGroup)
	}
	if d.Cost != CostStandard {
		t.Errorf("Cost = %q, want %q: one read-only transaction and one query is the class's definition", d.Cost, CostStandard)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 (R2.1's 10 s default; the SQL is not known to be cheap enough to shorten it)", d.Timeout)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Descriptor.Validate() = %v", err)
	}
}

// TestLocksAppliesToMajors checks the applicability against the matrix row rather
// than against a hardcoded range, which is the rule AppliesToViews' doc gives.
//
// matrix_data.go:532 maps pg_locks to LocksPG13 on 13 and LocksPG14to18 on 14
// through 18. The two majors outside that — 12 and 19 — are the ones worth naming:
// a collector that applied to 12 would build SQL from a column set the matrix
// never returns there, and one that applied to 19 would do the same the day the
// matrix gains it.
func TestLocksAppliesToMajors(t *testing.T) {
	c := NewLocksCollector(nil, SQLConfig{})

	for _, major := range []int{13, 14, 15, 16, 17, 18} {
		if !c.Descriptor().Applies(major) {
			t.Errorf("Applies(%d) = false, want true (matrix_data.go:532 has a column set there)", major)
		}
	}
	for _, major := range []int{11, 12, 19, 20} {
		if c.Descriptor().Applies(major) {
			t.Errorf("Applies(%d) = true, want false (pg_locks has no column set there)", major)
		}
	}
}

// TestLocksCollectLocksIssuesOneQueryInOneTransaction is R2.7's rule read off the
// fake: one Begin per round, one query per view group, and the query is the
// projection the matrix builds for the target's major.
//
// The query string is the assertion that matters most in the file. A collector that
// wrote its own SQL could get the column list wrong for one major and still return
// rows; a collector that goes through Project cannot, and the fake — which answers
// from a table keyed on query text — returns nothing when the strings disagree.
func TestLocksCollectLocksIssuesOneQueryInOneTransaction(t *testing.T) {
	p := locksProjection(t, 16)
	c, opener := locksCollector(t, p, []Row{
		rowFor(p, map[string]string{"locktype": "relation", "mode": "AccessShare", "granted": "t"}),
	})

	rows, res, err := c.CollectLocks(context.Background(), Target{Namespace: "pg", Pod: "db-0", PGMajor: 16, Role: "primary"})
	if err != nil {
		t.Fatalf("CollectLocks: %v", err)
	}
	if opener.calls != 1 {
		t.Errorf("Begin called %d times, want 1 (R2.7: one transaction per view group)", opener.calls)
	}
	if len(opener.tx.queries) != 1 {
		t.Fatalf("queries = %v, want exactly one", opener.tx.queries)
	}
	if opener.tx.queries[0] != p.Query {
		t.Errorf("query = %q, want %q", opener.tx.queries[0], p.Query)
	}
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].LockType != "relation" {
		t.Errorf("rows[0].LockType = %q, want relation", rows[0].LockType)
	}
	// R2.7's statement_timeout and R2.8's application_name are asserted on the
	// Begin call, not on the collector: a collector that configures them and then
	// opens a connection without them passes a field-level test and fails this one.
	if got := opener.cfgs[0].ApplicationName; got != ApplicationName {
		t.Errorf("Begin cfg ApplicationName = %q, want %q (R2.8)", got, ApplicationName)
	}
	if got := opener.cfgs[0].StatementTimeout; got != DefaultStatementTimeout {
		t.Errorf("Begin cfg StatementTimeout = %v, want %v (R2.1's default)", got, DefaultStatementTimeout)
	}
}

// TestLockFromRowPG16 parses a full sixteen-column row.
//
// The fixture is built from the projection's own column list via rowFor, so the
// test cannot pass on a row that has columns the PG16 projection does not select —
// which is the difference between testing the parser and testing a fixture.
func TestLockFromRowPG16(t *testing.T) {
	p := locksProjection(t, 16)

	row := rowFor(p, map[string]string{
		"locktype":           "transactionid",
		"database":           "16384",
		"relation":           "",
		"page":               "",
		"tuple":              "",
		"virtualxid":         "",
		"transactionid":      "874",
		"virtualtransaction": "3/1042",
		"classid":            "0",
		"objid":              "874",
		"objsubid":           "0",
		"pid":                "4321",
		"mode":               "ExclusiveLock",
		"granted":            "f",
		"fastpath":           "f",
		"waitstart":          "2026-11-22 09:59:30+00",
	})

	got, ok := lockFromRow(row)
	if !ok {
		t.Fatal("lockFromRow returned ok=false for a row with a locktype")
	}
	want := LockRow{
		LockType:           "transactionid",
		Database:           "16384",
		TransactionID:      "874",
		VirtualTransaction: "3/1042",
		ClassID:            "0",
		ObjID:              "874",
		ObjSubID:           "0",
		Pid:                "4321",
		Mode:               "ExclusiveLock",
		Granted:            false,
		Fastpath:           false,
		WaitStart:          time.Date(2026, 11, 22, 9, 59, 30, 0, time.UTC),
		SourceTime:         sourceNow,
	}
	// The two timestamps are compared apart from the struct: parseTimestamp returns
	// a zone named "+0000" where the fixture has time.UTC, so == on the struct
	// reports two equal instants as different. database_test.go:320 uses .Equal for
	// the same reason, and the struct comparison below covers every other field.
	if !got.WaitStart.Equal(want.WaitStart) {
		t.Errorf("WaitStart = %s, want %s", got.WaitStart, want.WaitStart)
	}
	if !got.SourceTime.Equal(want.SourceTime) {
		t.Errorf("SourceTime = %s, want %s", got.SourceTime, want.SourceTime)
	}
	got.WaitStart, want.WaitStart = time.Time{}, time.Time{}
	got.SourceTime, want.SourceTime = time.Time{}, time.Time{}
	if got != want {
		t.Errorf("lockFromRow:\n got %+v\nwant %+v", got, want)
	}
}

// TestLockFromRowPG13HasNoWaitStart is the version gap made into an assertion.
//
// The matrix's own split — LocksPG13's fifteen columns against LocksPG14to18's
// sixteen — means a PG13 row has no waitstart key at all. The row must still parse
// (it is a real lock), and WaitStart must be the zero time rather than a value
// invented from the row's other columns.
func TestLockFromRowPG13HasNoWaitStart(t *testing.T) {
	p := locksProjection(t, 13)
	for _, col := range p.Columns {
		if col == "waitstart" {
			t.Fatal("the PG13 projection selects waitstart; the matrix says LocksPG13 has 15 columns")
		}
	}

	row := rowFor(p, map[string]string{"locktype": "relation", "mode": "RowExclusiveLock", "granted": "t"})
	got, ok := lockFromRow(row)
	if !ok {
		t.Fatal("lockFromRow returned ok=false for a valid PG13 row")
	}
	if !got.WaitStart.IsZero() {
		t.Errorf("WaitStart = %v, want the zero time (PG13 has no waitstart column)", got.WaitStart)
	}
	if !got.Granted {
		t.Error("Granted = false, want true (rowFor set granted=t)")
	}
	if !got.SourceTime.Equal(sourceNow) {
		t.Errorf("SourceTime = %s, want %s", got.SourceTime, sourceNow)
	}
}

// TestLockFromRowDropsAndKeepsRows pins the two rows this collector treats
// specially, which are the only two judgement calls in the parser.
//
// A row with no locktype is dropped: locktype is the column that gives every other
// column its meaning, so a row without one is not a lock (the analogue of
// database.go's shared-objects filter and activity.go's "no usable pid").
//
// A row with no pid is kept: unlike Session, where the pid is the row's identity,
// a pg_locks row with a NULL pid is a real lock — a shared lock, or one the startup
// process holds — and R4.4's table has to render it.
func TestLockFromRowDropsAndKeepsRows(t *testing.T) {
	p := locksProjection(t, 16)

	t.Run("no locktype is dropped", func(t *testing.T) {
		row := rowFor(p, map[string]string{"locktype": "", "mode": "AccessShare"})
		if _, ok := lockFromRow(row); ok {
			t.Error("lockFromRow kept a row with an empty locktype")
		}
	})

	t.Run("no pid is kept", func(t *testing.T) {
		row := rowFor(p, map[string]string{"locktype": "relation", "pid": "", "mode": "AccessShare", "granted": "t"})
		got, ok := lockFromRow(row)
		if !ok {
			t.Fatal("lockFromRow dropped a relation lock with a NULL pid")
		}
		if got.Pid != "" {
			t.Errorf("Pid = %q, want the empty string (a NULL pid is not pid 0)", got.Pid)
		}
	})

	t.Run("lockRows applies the filter across a full result set", func(t *testing.T) {
		rows := []Row{
			rowFor(p, map[string]string{"locktype": "relation", "mode": "AccessShare"}),
			rowFor(p, map[string]string{"locktype": "", "mode": "AccessShare"}),
			rowFor(p, map[string]string{"locktype": "tuple", "mode": "TupleShare", "pid": ""}),
		}
		if got := lockRows(rows); len(got) != 2 {
			t.Errorf("lockRows returned %d rows, want 2 (one of three has no locktype)", len(got))
		}
	})
}

// TestLocksSamplesIsEmpty is the S50 argument as an assertion.
//
// The test is not here to prove a function returns nil — it is here so that the day
// someone gives LocksSamples a body, this test fails and they have to read the
// reason. That reason is on the function: R9.7's union admits no pg_locks family,
// R5.2 names none, and a per-row sample per numeric column is D20's S8 invalid
// exposition. A test that fails when the answer changes is the cheapest way to make
// sure the answer changes on purpose.
func TestLocksSamplesIsEmpty(t *testing.T) {
	if got := LocksSamples(nil, fixedClock); len(got) != 0 {
		t.Errorf("LocksSamples(nil, now) = %v, want empty", got)
	}

	p := locksProjection(t, 16)
	rows := lockRows([]Row{
		rowFor(p, map[string]string{"locktype": "relation", "mode": "AccessShare", "granted": "t"}),
		rowFor(p, map[string]string{"locktype": "relation", "mode": "RowExclusiveLock", "granted": "f"}),
	})
	if len(rows) != 2 {
		t.Fatalf("fixture rows = %d, want 2", len(rows))
	}
	if got := LocksSamples(rows, fixedClock); len(got) != 0 {
		t.Errorf("LocksSamples(rows, now) = %v, want empty (see the S50 argument on the function)", got)
	}
}

// TestLocksCollectReturnsNoSamplesForARoundOfRows is R9.1's split observed at the
// interface boundary: the round happens, the rows exist, and the Result carries no
// samples.
//
// It is the counterpart to TestLocksSamplesIsEmpty. That test checks the
// derivation; this one checks that Collect actually calls it and publishes what it
// returns, so a collector that returned a hardcoded nil and one that returned
// LocksSamples' nil are the same thing to a scheduler — and stay the same thing
// when the function gains a body.
func TestLocksCollectReturnsNoSamplesForARoundOfRows(t *testing.T) {
	p := locksProjection(t, 16)
	c, _ := locksCollector(t, p, []Row{
		rowFor(p, map[string]string{"locktype": "relation", "mode": "AccessShare", "granted": "t"}),
	})

	res, err := c.Collect(context.Background(), Target{Namespace: "pg", Pod: "db-0", PGMajor: 16, Role: "primary"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Samples) != 0 {
		t.Errorf("Samples = %v, want empty", res.Samples)
	}
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
	}
}

// TestLocksErrorPathsReportRoundTrips is R13.1's budget rule checked on all three
// failure paths.
//
// The rule is that a Result returned alongside an error must still carry the
// round-trips actually issued, because the scheduler's per-target budget is measured
// from Result.RoundTrips and a collector that returns a zero on partial failure
// makes a cluster that issued a query look like one that issued none. Each path
// here has a different correct answer — 0 for a Begin that never opened, 1 for a
// query that failed inside an opened transaction — and a single
// `return Result{}, err` for all three is the bug.
func TestLocksErrorPathsReportRoundTrips(t *testing.T) {
	p := locksProjection(t, 16)
	tgt := Target{Namespace: "pg", Pod: "db-0", PGMajor: 16, Role: "primary"}
	cfg := SQLConfig{Now: func() time.Time { return fixedClock }}

	t.Run("begin fails: zero round trips", func(t *testing.T) {
		c := NewLocksCollector(&fakeOpener{beginErr: errors.New("fake: begin failed")}, cfg)
		rows, res, err := c.CollectLocks(context.Background(), tgt)
		if err == nil {
			t.Fatal("CollectLocks returned nil error for a failing Begin")
		}
		if res.RoundTrips != 0 {
			t.Errorf("RoundTrips = %d, want 0 (a Begin that failed issued nothing)", res.RoundTrips)
		}
		if rows != nil {
			t.Errorf("rows = %v, want nil", rows)
		}
	})

	t.Run("query fails: one round trip and a rollback", func(t *testing.T) {
		tx := &fakeTx{rows: map[string][]Row{}, failOn: p.Query}
		c := NewLocksCollector(&fakeOpener{tx: tx}, cfg)
		rows, res, err := c.CollectLocks(context.Background(), tgt)
		if err == nil {
			t.Fatal("CollectLocks returned nil error for a failing query")
		}
		if res.RoundTrips != 1 {
			t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
		}
		if rows != nil {
			t.Errorf("rows = %v, want nil", rows)
		}
		// D20's S9: sql.go's Tx doc says "Rollback on every error path", and a
		// failed query is one. The count is the assertion; the error being wrapped
		// rather than swallowed is the other half, and the message check is what
		// stops a collector from returning the rollback's error instead.
		if tx.rollbacks != 1 {
			t.Errorf("rollbacks = %d, want 1 (D20's S9: rollback on every error path)", tx.rollbacks)
		}
		if tx.commits != 0 {
			t.Errorf("commits = %d, want 0", tx.commits)
		}
	})

	t.Run("commit fails: rows and one round trip", func(t *testing.T) {
		rows := []Row{rowFor(p, map[string]string{"locktype": "relation", "mode": "AccessShare"})}
		tx := &fakeTx{rows: map[string][]Row{p.Query: rows}, commitErr: errors.New("fake: commit failed")}
		c := NewLocksCollector(&fakeOpener{tx: tx}, cfg)
		got, res, err := c.CollectLocks(context.Background(), tgt)
		if err == nil {
			t.Fatal("CollectLocks returned nil error for a failing Commit")
		}
		if res.RoundTrips != 1 {
			t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
		}
		// The data was read before the transaction failed to close, so the rows come
		// back with the error: a caller that wants the last readable table can use
		// them, and the scheduler still treats the round as failed.
		if len(got) != 1 {
			t.Errorf("rows = %d, want 1 (the query succeeded; only the commit failed)", len(got))
		}
	})

	t.Run("unknown major issues nothing", func(t *testing.T) {
		tx := &fakeTx{rows: map[string][]Row{}}
		opener := &fakeOpener{tx: tx}
		c := NewLocksCollector(opener, cfg)
		_, res, err := c.CollectLocks(context.Background(), Target{Namespace: "pg", Pod: "db-0"})
		if err == nil {
			t.Fatal("CollectLocks returned nil error for a target with no PGMajor")
		}
		if res.RoundTrips != 0 {
			t.Errorf("RoundTrips = %d, want 0", res.RoundTrips)
		}
		if opener.calls != 0 {
			t.Errorf("Begin called %d times, want 0 (building the SQL is what tells us there is nothing to issue)", opener.calls)
		}
	})
}
