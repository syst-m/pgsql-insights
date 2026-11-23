package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract for the replication collector, matching
// database_test.go's and settings_test.go's: the collector is a Collector, checked
// by the compiler.
var _ Collector = (*ReplicationCollector)(nil)

// newTestReplication builds the collector plus the fake opener and tx it runs on,
// serving the given rows for each view's query. fakeTx/fakeOpener are
// database_test.go's, for the reason settings_test.go gives: one fake per
// interface, so there is one answer in the package to "what did a Begin record".
func newTestReplication(t *testing.T, tgt Target, rows map[pgversion.View][]Row) (*ReplicationCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	projections, err := NewReplicationCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range projections {
		tx.rows[pv.Query] = rows[pv.view]
	}
	opener := &fakeOpener{tx: tx}
	return NewReplicationCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener, tx
}

// repRow builds a pg_stat_replication row with every matrix column filled (via
// fullRow) and the three derived lag aliases set to the given values.
//
// The aliases are not matrix columns — they are this collector's Extras — so
// fullRow cannot fill them and this builder must: passing "" is how a test says
// "the column is NULL", which is what a walsender that has not streamed yet
// returns, and it is the case the lag families' existence rule is built around.
func repRow(t *testing.T, app string, write, flush, replay string) Row {
	t.Helper()
	return fullRow(t, pgversion.ViewStatReplication, map[string]string{
		"application_name":   app,
		"write_lag_seconds":  write,
		"flush_lag_seconds":  flush,
		"replay_lag_seconds": replay,
	})
}

// slotRow builds a pg_replication_slots row with every matrix column filled and
// the three columns the builder reads set.
func slotRow(t *testing.T, name, active, status string) Row {
	t.Helper()
	return fullRow(t, pgversion.ViewReplicationSlots, map[string]string{
		"slot_name":  name,
		"active":     active,
		"wal_status": status,
	})
}

// replicationTestRows is the one fixture the collect-path tests share: two
// walsender rows (one streaming with lags, one connecting with NULLs) and three
// slots (two active, one of them lost; one inactive).
func replicationTestRows(t *testing.T) map[pgversion.View][]Row {
	t.Helper()
	return map[pgversion.View][]Row{
		pgversion.ViewStatReplication: {
			repRow(t, "cnpg-1", "1.5", "1.5", "2.25"),
			repRow(t, "cnpg-2", "", "", ""),
		},
		pgversion.ViewReplicationSlots: {
			slotRow(t, "cnpg_replica_1", "t", "reserved"),
			slotRow(t, "cnpg_replica_2", "t", "lost"),
			slotRow(t, "cnpg_replica_3", "f", "extended"),
		},
	}
}

func TestReplicationDescriptor(t *testing.T) {
	c, _, _ := newTestReplication(t, tgtPG("pg", "pg-1", "primary"), replicationTestRows(t))
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorReplication {
		t.Errorf("Name = %q, want %q (§3.3's row name, and the R2.2 `collector` label)", d.Name, CollectorReplication)
	}
	if d.Interval != ReplicationInterval {
		t.Errorf("Interval = %s, want %s (§3.3's 60 s)", d.Interval, ReplicationInterval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %s, want %s (§3.3 lists S4 as preferred and S2/S3 as fallbacks; Descriptor.Source "+
			"declares one, and the S2 half is the one Phase 2 owns — see the comment at the field)", d.Source, SourceSQL)
	}
	if d.Cost != CostStandard {
		t.Errorf("Cost = %s, want %s (collector.go's CostStandard doc names this collector)", d.Cost, CostStandard)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != replicationViewGroup {
		t.Errorf("ViewGroups = %v, want [%s] (R2.7: one entry per view group, and this collector has one)",
			d.ViewGroups, replicationViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; the tuning knob is R2.7's statement_timeout)", d.Timeout)
	}
	// Both views exist on every matrix major (pg_stat_replication 13-18,
	// pg_replication_slots 13-18), so the collector applies to all six — and to
	// nothing outside the matrix.
	for _, major := range pgversion.Majors() {
		if !d.Applies(major) {
			t.Errorf("Applies(%d) = false, want true (both views exist on every matrix major)", major)
		}
	}
	for _, major := range []int{12, 19} {
		if d.Applies(major) {
			t.Errorf("Applies(%d) = true, want false", major)
		}
	}
}

// TestReplicationQueriesAreSchemaQualified is R2.5 and R2.4 checked on every
// major: each query names its schema, its column list is the matrix's for that
// major, and — the half the column loop cannot see — every declared Extra appears
// in the query that runs, so a collector cannot declare lag extras and query
// without them.
func TestReplicationQueriesAreSchemaQualified(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		projections, err := NewReplicationCollector(nil, SQLConfig{}).projections(tgt)
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}
		if len(projections) != len(replicationViews) {
			t.Fatalf("PG%d: %d projections, want %d", major, len(projections), len(replicationViews))
		}
		for _, pv := range projections {
			want := " FROM pg_catalog." + string(pv.view)
			if !strings.HasSuffix(pv.Query, want) {
				t.Errorf("PG%d %s: query %q does not end in %q (R2.5)", major, pv.view, pv.Query, want)
			}
			cols, _ := pgversion.Columns(pv.view, major)
			for _, col := range cols {
				if !strings.Contains(pv.Query, col) {
					t.Errorf("PG%d %s: query does not select %s (R2.4 says the matrix's column set is the projection)",
						major, pv.view, col)
				}
			}
			for _, e := range pv.Extras {
				if !strings.Contains(pv.Query, e.Expr+" AS "+e.Alias) {
					t.Errorf("PG%d %s: query does not select extra %q (the collector declares it, so the query must run it)",
						major, pv.view, e.Alias)
				}
			}
		}
	}
}

// TestReplicationCollectSamples is the golden sample set for one round: a count
// and three lag maxima for the two walsender rows, three counts for the three
// slots, and nothing for the NULL lag columns beyond the maxima the streaming row
// supplies.
func TestReplicationCollectSamples(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, _, _ := newTestReplication(t, tgt, replicationTestRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// 2 walsender rows → 1 count + 3 lag maxima (the second row's NULLs add
	// nothing); 3 slot rows → 3 counts. Seven, from the fixture above.
	if len(res.Samples) != 7 {
		t.Fatalf("len(Samples) = %d, want 7 (2 rows → count + 3 maxima; 3 slots → 3 counts)\n%s",
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
		{"pg_stat_replication_count", 2},
		{"pg_stat_replication_write_lag_seconds_max", 1.5},
		{"pg_stat_replication_flush_lag_seconds_max", 1.5},
		{"pg_stat_replication_replay_lag_seconds_max", 2.25},
		{"pg_replication_slots_count", 3},
		{"pg_replication_slots_active_count", 2},
		{"pg_replication_slots_lost_count", 1},
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
		// R9.3's contract has no slot for an application_name or a slot_name, and
		// the identity half is the scheduler's, so a replication sample carries no
		// labels.
		if len(s.Labels) != 0 {
			t.Errorf("%s labels = %v, want none (R9.3's closed set has no slot for the row's own key; "+
				"identity labels are the scheduler's)", tc.metric, s.Labels)
		}
	}

	// The raw interval columns produce no family of their own: "00:00:01.5" is not
	// a number (R9.1), and the number that is exported is the date_part alias.
	if _, ok := byMetric["pg_stat_replication_write_lag"]; ok {
		t.Error("pg_stat_replication_write_lag exists, want the interval column unexported and the seconds alias exported")
	}
	// The per-row identity is not a label either.
	if _, ok := byMetric["pg_stat_replication_application_name"]; ok {
		t.Error("pg_stat_replication_application_name exists, want counts rather than per-connection families")
	}
}

// TestReplicationCollectOneTransactionPerRound is R2.7 as a count: one Begin, one
// query per view, one Commit, and the config that reached the opener carrying
// R2.7's statement_timeout and R2.8's application_name.
func TestReplicationCollectOneTransactionPerRound(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, opener, tx := newTestReplication(t, tgt, replicationTestRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if opener.calls != 1 {
		t.Errorf("Begin calls = %d, want 1 (R2.7: one read-only transaction per view group, and this collector has one group)", opener.calls)
	}
	projections, _ := c.projections(tgt)
	if len(tx.queries) != len(replicationViews) {
		t.Fatalf("queries = %d, want %d (one per view)", len(tx.queries), len(replicationViews))
	}
	for i, q := range tx.queries {
		if q != projections[i].Query {
			t.Errorf("query %d =\n%s\nwant\n%s", i, q, projections[i].Query)
		}
	}
	if tx.commits != 1 || tx.rollbacks != 0 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/0 on the success path", tx.commits, tx.rollbacks)
	}
	if res.RoundTrips != len(replicationViews) {
		t.Errorf("RoundTrips = %d, want %d (R13.1 measures a round from this count)", res.RoundTrips, len(replicationViews))
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

// TestReplicationCollectQueryErrorKeepsRoundTrips is the Collector contract's
// "on error, the returned Result must still carry the RoundTrips it actually
// issued", for the replication collector's second view.
func TestReplicationCollectQueryErrorKeepsRoundTrips(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	projections, _ := NewReplicationCollector(nil, SQLConfig{}).projections(tgt)

	tx := &fakeTx{
		rows: map[string][]Row{
			projections[0].Query: {repRow(t, "cnpg-1", "1.5", "1.5", "2.25")},
		},
		failOn: projections[1].Query,
	}
	opener := &fakeOpener{tx: tx}
	c := NewReplicationCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

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
	if !strings.Contains(err.Error(), string(pgversion.ViewReplicationSlots)) {
		t.Errorf("error %q does not name the failing view", err)
	}
}

func TestReplicationCollectCommitError(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, _, tx := newTestReplication(t, tgt, replicationTestRows(t))
	tx.commitErr = errors.New("fake: connection lost")

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the commit failure surfaced")
	}
	if res.RoundTrips != len(replicationViews) {
		t.Errorf("RoundTrips = %d, want %d (the queries did run)", res.RoundTrips, len(replicationViews))
	}
	if len(res.Samples) == 0 {
		t.Error("Samples empty, want the read samples returned alongside the error")
	}
}

func TestReplicationCollectBeginError(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	opener := &fakeOpener{beginErr: errors.New("fake: connect refused")}
	c := NewReplicationCollector(opener, SQLConfig{})

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

func TestReplicationCollectRefusesUnknownMajors(t *testing.T) {
	for _, major := range []int{0, 12, 19} {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		opener := &fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}
		c := NewReplicationCollector(opener, SQLConfig{})

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

// TestReplicationSingleInstanceIsEmpty is R5.5's "empty on single-instance
// clusters" as the Collector contract's legitimate empty answer: no rows in
// either view is not an error, and it is not a zero-valued series either — it is
// no samples, which is what collector.go's Result doc says a single-instance
// cluster's replication round looks like (and what the cookbook fixture in §8
// returns).
func TestReplicationSingleInstanceIsEmpty(t *testing.T) {
	tgt := tgtPG("cookbook", "cookbook-pg-1", "primary")
	c, _, tx := newTestReplication(t, tgt, map[pgversion.View][]Row{
		pgversion.ViewStatReplication:  {},
		pgversion.ViewReplicationSlots: {},
	})

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect on a single-instance cluster: %v (R5.5: empty is the answer, not an error)", err)
	}
	if len(res.Samples) != 0 {
		t.Errorf("Samples = %d, want 0 (no standbys, no slots → no series)\n%s", len(res.Samples), dumpSamples(res.Samples))
	}
	if res.RoundTrips != len(replicationViews) {
		t.Errorf("RoundTrips = %d, want %d (the queries ran; an empty result is not a skipped round)",
			res.RoundTrips, len(replicationViews))
	}
	if tx.commits != 1 {
		t.Errorf("commits = %d, want 1", tx.commits)
	}
}

// viewProjectionFor pairs one view with one major's projection, for the builder
// tests that need a viewProjection and nothing else.
//
// It calls Project directly rather than asking a collector for its projections, so
// the builder tests check the builder against the spec's own answer instead of
// against the collector that is also under test — the same reason
// TestProjectQueryIsTheMatrixQuery rebuilds the expected query instead of asking
// for it.
func viewProjectionFor(t *testing.T, view pgversion.View, major int) viewProjection {
	t.Helper()
	p, ok := Project(view, major, replicationSpecs[view])
	if !ok {
		t.Fatalf("Project(%s, %d): not ok", view, major)
	}
	return viewProjection{view: view, Projection: p}
}

// TestStreamingSamples pins the pg_stat_replication builder's two rules that the
// end-to-end test can only sample: the max is a max, and a plane with no parseable
// value produces no family rather than a 0.
func TestStreamingSamples(t *testing.T) {
	pv := viewProjectionFor(t, pgversion.ViewStatReplication, 17)

	cases := []struct {
		name string
		rows []Row
		want map[string]float64
	}{
		{
			name: "one streaming, one connecting",
			rows: []Row{repRow(t, "cnpg-1", "1.5", "1.5", "2.25"), repRow(t, "cnpg-2", "", "", "")},
			want: map[string]float64{
				"pg_stat_replication_count":                  2,
				"pg_stat_replication_write_lag_seconds_max":  1.5,
				"pg_stat_replication_flush_lag_seconds_max":  1.5,
				"pg_stat_replication_replay_lag_seconds_max": 2.25,
			},
		},
		{
			// The max has to be a max: two streaming standbys with different lags,
			// and the worst on each plane wins. A sum would be a number nobody
			// queries and a mean hides the one replica that is behind.
			name: "two streaming, worst wins per plane",
			rows: []Row{repRow(t, "cnpg-1", "1.5", "9", "2.25"), repRow(t, "cnpg-2", "3", "1", "1")},
			want: map[string]float64{
				"pg_stat_replication_count":                  2,
				"pg_stat_replication_write_lag_seconds_max":  3,
				"pg_stat_replication_flush_lag_seconds_max":  9,
				"pg_stat_replication_replay_lag_seconds_max": 2.25,
			},
		},
		{
			// All three planes NULL: the count exists, the maxima do not. A 0 here
			// would report "fully caught up" for a standby that has not streamed.
			name: "connecting only",
			rows: []Row{repRow(t, "cnpg-2", "", "", "")},
			want: map[string]float64{"pg_stat_replication_count": 1},
		},
		{
			name: "no rows",
			rows: nil,
			want: nil,
		},
	}

	for _, tc := range cases {
		got := streamingSamples(pv, fixedClock, tc.rows)
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
		}
	}
}

// TestSlotSamples pins the pg_replication_slots builder on the column spellings
// Postgres actually produces: a boolean column is t/f (not the on/off a GUC
// uses), and wal_status is text with four values of which only 'lost' is counted.
func TestSlotSamples(t *testing.T) {
	pv := viewProjectionFor(t, pgversion.ViewReplicationSlots, 17)

	cases := []struct {
		name string
		rows []Row
		want map[string]float64
	}{
		{
			name: "two active one lost, one inactive",
			rows: []Row{
				slotRow(t, "cnpg_replica_1", "t", "reserved"),
				slotRow(t, "cnpg_replica_2", "t", "lost"),
				slotRow(t, "cnpg_replica_3", "f", "extended"),
			},
			want: map[string]float64{
				"pg_replication_slots_count":        3,
				"pg_replication_slots_active_count": 2,
				"pg_replication_slots_lost_count":   1,
			},
		},
		{
			// The driver's other boolean spelling, and the NULL wal_status a slot on
			// a standby carries: active counts, and a slot with no status is not a
			// lost one.
			name: "true and a NULL status",
			rows: []Row{slotRow(t, "cnpg_replica_1", "true", "")},
			want: map[string]float64{
				"pg_replication_slots_count":        1,
				"pg_replication_slots_active_count": 1,
				"pg_replication_slots_lost_count":   0,
			},
		},
		{
			name: "nothing active, nothing lost",
			rows: []Row{
				slotRow(t, "cnpg_replica_1", "f", "unreserved"),
				slotRow(t, "cnpg_replica_2", "f", "reserved"),
			},
			want: map[string]float64{
				"pg_replication_slots_count":        2,
				"pg_replication_slots_active_count": 0,
				"pg_replication_slots_lost_count":   0,
			},
		},
		{
			name: "no rows",
			rows: nil,
			want: nil,
		},
	}

	for _, tc := range cases {
		got := slotSamples(pv, fixedClock, tc.rows)
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
		}
	}
}
