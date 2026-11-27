package collector

// io_test.go covers the fifth of the Phase 2 SQL collectors (A3's ruling at
// 1b11199:IMPLEMENTATION.md:766), and it inherits the fixture pattern the other four set:
// fakeOpener/fakeTx are database_test.go's, the fake clock is database_test.go's
// fixedClock/fixedSourceNow, and the golden sample count is derived from the fixture
// and spelled out in the failure message rather than remembered — the way
// wal_test.go does it rather than the way database_test.go does, because this is the
// same four-views-in-one-transaction shape wal.go has.
//
// The one helper with no precedent is ioRowFor. fullRow is settings_test.go's and it
// hardcodes pgversion.Columns(view, 17), which is fine for a collector that only ever
// runs on 17 and not for one whose whole PG18 difference is three extra columns and
// five missing ones. So the row builder takes the major, and ioRow is the 17-shaped
// call site the tests use most.
//
// The two tests with no precedent at all are TestIOQueriesAreSchemaQualified's
// per-major projection *count* (3 on PG16, 4 on PG17 and PG18 — the succession pair
// contributes one row below 17 and two at 17+, which is the shape R2.4's fourth
// enumerated case forces and the half of the file's applicability argument that is
// testable) and TestIOCollectSamples' label-set check, which is what proves the
// labelled half of the collector does not put two rows' values in one series.

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract, matching the other four collectors'.
var _ Collector = (*IOCollector)(nil)

// newTestIO builds the collector plus the fake opener and tx it runs on, serving the
// given rows for each view's query. It is newTestWAL with the io collector's
// projections: the map is keyed by the exact query string so a query the collector
// did not build is a miss rather than a silent empty result set.
func newTestIO(t *testing.T, tgt Target, rows map[pgversion.View][]Row) (*IOCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	projections, err := NewIOCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range projections {
		tx.rows[pv.Query] = rows[pv.view]
	}
	opener := &fakeOpener{tx: tx}
	return NewIOCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener, tx
}

// ioProjectionFor pairs one view with one major's projection, for the builder tests
// that need a viewProjection and nothing else. It calls Project directly rather than
// asking a collector for its projections, for the reason wal_test.go's version gives:
// the builder tests then check the builder against the spec's own answer instead of
// against the collector that is also under test.
func ioProjectionFor(t *testing.T, view pgversion.View, major int) viewProjection {
	t.Helper()
	p, ok := Project(view, major, QuerySpec{})
	if !ok {
		t.Fatalf("Project(%s, %d): not ok", view, major)
	}
	return viewProjection{view: view, Projection: p}
}

// ioRowFor builds a row for one view on one major with every matrix column filled and
// the columns in over set on top.
//
// It exists because fullRow — settings_test.go's, and the one the other collector
// tests use — hardcodes major 17. That is not a detail this file can borrow: the
// pg_stat_io PG18 set has read_bytes/write_bytes/extend_bytes and no op_bytes, and
// the pg_stat_checkpointer PG18 set has num_done/slru_written and the PG17 set does
// not, so a PG18 fixture built from the PG17 column list would be a row with columns
// the query does not select and without the ones it does.
func ioRowFor(t *testing.T, view pgversion.View, major int, over map[string]string) Row {
	t.Helper()
	cols, ok := pgversion.Columns(view, major)
	if !ok {
		t.Fatalf("Columns(%s, %d): not ok", view, major)
	}
	row := Row{SourceNowColumn: fixedSourceNow}
	for _, c := range cols {
		row[c] = "x"
	}
	for k, v := range over {
		row[k] = v
	}
	return row
}

// ioRow is ioRowFor on PG17, the major the shared fixture is built on.
func ioRow(t *testing.T, view pgversion.View, over map[string]string) Row {
	t.Helper()
	return ioRowFor(t, view, 17, over)
}

// ioEpoch is the newest stats_reset in the fixture. The two pg_stat_io rows reset at
// different times — which the labelled shape keeps per row rather than maxing — and
// the checkpointer's 2026-06-01 is the value the summed-shape tests assert, while
// the bgwriter's 2026-01-01 is the other one.
var ioEpochNew = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
var ioEpochOld = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// ioTestRows is the one fixture the collect-path tests share: two pg_stat_io rows,
// one pg_stat_slru row, and one row each for the succession pair's PG17 spellings —
// 46 samples, derived in TestIOCollectSamples rather than remembered.
//
// Two io rows is the minimum that tests the labelled shape at all: with one row, a
// builder that ignored the identity columns and emitted one unlabelled sample per
// metric would produce the same 14 names, and the only thing distinguishing the two
// shapes is whether the second row's values are reachable.
func ioTestRows(t *testing.T) map[pgversion.View][]Row {
	t.Helper()
	return map[pgversion.View][]Row{
		pgversion.ViewStatIO: {
			ioRow(t, pgversion.ViewStatIO, map[string]string{
				"backend_type": "client backend", "object": "relation", "context": "normal",
				"reads": "1000", "read_time": "500", "writes": "200", "write_time": "100",
				"writebacks": "5", "writeback_time": "10", "extends": "50", "extend_time": "25",
				"op_bytes": "8192000", "hits": "900000", "evictions": "12", "reuses": "34",
				"fsyncs": "7", "fsync_time": "3",
				"stats_reset": "2026-01-01 00:00:00+00",
			}),
			ioRow(t, pgversion.ViewStatIO, map[string]string{
				"backend_type": "background writer", "object": "relation", "context": "normal",
				"reads": "10", "read_time": "5", "writes": "40", "write_time": "20",
				"writebacks": "1", "writeback_time": "1", "extends": "2", "extend_time": "1",
				"op_bytes": "16384", "hits": "99", "evictions": "0", "reuses": "1",
				"fsyncs": "2", "fsync_time": "0",
				"stats_reset": "2026-06-01 00:00:00+00",
			}),
		},
		pgversion.ViewStatSLRU: {
			ioRow(t, pgversion.ViewStatSLRU, map[string]string{
				"name":         "xact",
				"blks_zeroed":  "1",
				"blks_hit":     "1000",
				"blks_read":    "20",
				"blks_written": "30",
				"blks_exists":  "40",
				"flushes":      "5",
				"truncates":    "6",
				"stats_reset":  "2026-01-01 00:00:00+00",
			}),
		},
		pgversion.ViewStatBgwriter: {
			ioRow(t, pgversion.ViewStatBgwriter, map[string]string{
				"buffers_clean": "100", "maxwritten_clean": "2", "buffers_alloc": "300",
				"stats_reset": "2026-01-01 00:00:00+00",
			}),
		},
		pgversion.ViewStatCheckpointer: {
			ioRow(t, pgversion.ViewStatCheckpointer, map[string]string{
				"num_timed": "7", "num_requested": "1", "restartpoints_timed": "3",
				"restartpoints_req": "0", "restartpoints_done": "3", "write_time": "40",
				"sync_time": "8", "buffers_written": "900",
				"stats_reset": "2026-06-01 00:00:00+00",
			}),
		},
	}
}

// ioTarget is a PG17 SQL target.
//
// tgtPG — scheduler_test.go's, and the one the other collector tests use — returns
// PGMajor 16. That is the right default for the collectors that apply on 16 and the
// wrong one for a fixture built on PG17's column sets: on PG16 the succession pair
// contributes pg_stat_bgwriter alone, so the checkpointer's eight families exist only
// on 17 and 18 and a PG16 round leaves the fixture's checkpointer row unqueried. The
// collect-path tests that assert a sample count name the major instead of inheriting
// one, and TestIOQueriesAreSchemaQualified already loops over every major.
func ioTarget() Target {
	return Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: 17}
}

func TestIODescriptor(t *testing.T) {
	c, _, _ := newTestIO(t, ioTarget(), ioTestRows(t))
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorIO {
		t.Errorf("Name = %q, want %q (§3.3's row name, and the R2.2 `collector` label)", d.Name, CollectorIO)
	}
	if d.Interval != IOInterval {
		t.Errorf("Interval = %s, want %s (§3.3's 60 s)", d.Interval, IOInterval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %s, want %s (§3.3's row lists four S2 views; the S3 caveat in the row's last "+
			"column is about what the CNPG exporter omits, not a second source here)", d.Source, SourceSQL)
	}
	if d.Cost != CostStandard {
		t.Errorf("Cost = %s, want %s (four projections, one read-only transaction, no cross-join)", d.Cost, CostStandard)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != ioViewGroup {
		t.Errorf("ViewGroups = %v, want [%s] (R2.7: one entry per view group; four S2 views read in one "+
			"round are one group, the way wal.go's two views are one)", d.ViewGroups, ioViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; the tuning knob is R2.7's statement_timeout)", d.Timeout)
	}

	// The applicability half is §3.3's "(16+)" derived from the matrix rather than
	// typed in — and the derivation is the one the descriptor comment argues for:
	// pg_stat_io is 16+ and pg_stat_slru is 13-18, so the intersection of the two
	// *independent* views is 16-18. The succession pair is not an input, and PG16 is
	// the major that shows why: intersecting all four views would also refuse PG16
	// (pg_stat_checkpointer starts at 17), which contradicts the row's own "(16+)"
	// on the one major where the row says pg_stat_io exists.
	for _, major := range pgversion.Majors() {
		want := major >= 16
		if got := d.Applies(major); got != want {
			t.Errorf("Applies(%d) = %v, want %v (pg_stat_io is 16+, pg_stat_slru is 13-18; the "+
				"bgwriter/checkpointer pair is excluded from the intersection — see the AppliesTo comment)",
				major, got, want)
		}
	}
	for _, major := range []int{12, 19} {
		if d.Applies(major) {
			t.Errorf("Applies(%d) = true, want false", major)
		}
	}
}

// TestIOQueriesAreSchemaQualified is R2.5 and R2.4 on every major, with the
// comparison exact rather than per-column, and with a second assertion wal.go's
// version does not have: the number of projections per major.
//
// The count is the testable half of the succession-pair argument. wal.go's two views
// are both applicability inputs, so it always has two projections on every applicable
// major; this collector has three on PG16 and four on PG17+, because
// pg_stat_bgwriter and pg_stat_checkpointer are one source with two spellings and
// exactly one of them exists below PG17. A collector that queried both on PG17 and
// also minted the checkpointer's PG16 counters from the bgwriter's row would still
// produce the right names — the naming table dedupes them — but would issue a query
// against a view PostgreSQL does not have, and that is only visible here.
func TestIOQueriesAreSchemaQualified(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		projections, err := NewIOCollector(nil, SQLConfig{}).projections(tgt)

		// 13, 14 and 15 are not applicable: pg_stat_io has no column set there, so
		// the collector refuses the target rather than running the three views that
		// do exist (R2.7's one-transaction rule is what makes "three quarters of a
		// result set" the wrong answer).
		if major < 16 {
			if err == nil {
				t.Errorf("PG%d: projections = nil error, want a refusal (§3.3's row says 16+)", major)
			}
			continue
		}
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}

		wantCount := len(ioViews)
		if major < 17 {
			// PG16: the checkpointer has no column set, so the pair contributes its
			// one PG16 spelling and the round runs three queries, not four.
			wantCount = 3
		}
		if len(projections) != wantCount {
			t.Fatalf("PG%d: %d projections, want %d (the succession pair contributes one view below "+
				"PG17 and two at 17+)", major, len(projections), wantCount)
		}

		for _, pv := range projections {
			want := " FROM " + pgversion.Schema(pv.view) + "." + string(pv.view)
			if !strings.HasSuffix(pv.Query, want) {
				t.Errorf("PG%d %s: query %q does not end in %q (R2.5)", major, pv.view, pv.Query, want)
			}
			cols, ok := pgversion.Columns(pv.view, major)
			if !ok {
				t.Fatalf("PG%d: Columns(%s, %d) not ok", major, pv.view, major)
			}
			// The want-list appends the *Query* spelling of the source-now column,
			// not the bare constant, for the reason wal_test.go spells out: Project
			// writes `now() AS pgsi_source_now` in Query and the bare alias in
			// Columns, and selectList parses the first.
			wantList := append(append([]string{}, cols...), "now() AS "+SourceNowColumn)
			if got := selectList(t, pv.Query); !reflect.DeepEqual(got, wantList) {
				t.Errorf("PG%d %s: SELECT list = %v, want %v (R2.4 says the matrix's column set is "+
					"the projection, and this collector declares no Extras to add to it)",
					major, pv.view, got, wantList)
			}
			if len(pv.Extras) != 0 {
				t.Errorf("PG%d %s: %d Extras, want 0 (every io family comes from a matrix column; the "+
					"views have no interval columns needing date_part)", major, pv.view, len(pv.Extras))
			}
		}
	}
}

// TestIOCollectSamples is the golden sample set for one round on PG17: 46.
//
//   - pg_stat_io: 14 metric columns × 2 rows = 28 (three of the view's 18 columns are
//     the row's identity and one is its epoch, so 14 are metrics);
//   - pg_stat_slru: 7 metric columns × 1 row = 7 (one identity column, one epoch);
//   - pg_stat_bgwriter's PG17 set: 3 metrics, summed to 3 samples (the other seven of
//     its 13-to-16 columns moved to the checkpointer at PG17 or were retired, and
//     stats_reset is an epoch);
//   - pg_stat_checkpointer's PG17 set: 8 metrics, summed to 8 samples.
//
// The count is the weak half of this test; the strong half is the key set. 46 samples
// must have 46 distinct (metric, labels) pairs and 32 distinct metric names — the
// only shape in which 28 samples can come from 14 names is two label sets per name,
// so a builder that dropped the labels would produce 14 colliding io samples and fail
// here even with the right total.
func TestIOCollectSamples(t *testing.T) {
	tgt := ioTarget()
	c, _, _ := newTestIO(t, tgt, ioTestRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(res.Samples) != 46 {
		t.Fatalf("len(Samples) = %d, want 46 (28 labelled pg_stat_io + 7 labelled pg_stat_slru + "+
			"3 summed pg_stat_bgwriter + 8 summed pg_stat_checkpointer)\n%s", len(res.Samples), dumpSamples(res.Samples))
	}

	keys := map[string]bool{}
	byMetric := map[string][]Sample{}
	for _, s := range res.Samples {
		keys[s.Metric+"|"+labelsKey(s.Labels)] = true
		byMetric[s.Metric] = append(byMetric[s.Metric], s)
	}
	if len(keys) != len(res.Samples) {
		t.Errorf("%d distinct (metric, labels) pairs for %d samples: two samples share one series, so one "+
			"row's value overwrites the other's in the exposition", len(keys), len(res.Samples))
	}
	if len(byMetric) != 32 {
		t.Errorf("%d distinct metric names, want 32 (14 io + 7 slru + 3 bgwriter + 8 checkpointer)\n%s",
			len(byMetric), dumpSamples(res.Samples))
	}

	// The clocks are every sample's, from the fixture rather than from the metric.
	for _, s := range res.Samples {
		if !s.SourceTime.Equal(sourceNow) {
			t.Errorf("%s SourceTime = %s, want %s (R13.11's instance clock, from the now() column)",
				s.Metric, s.SourceTime, sourceNow)
			break
		}
		if !s.IngestTime.Equal(fixedClock) {
			t.Errorf("%s IngestTime = %s, want %s (R13.11's our-clock, from the injected clock)",
				s.Metric, s.IngestTime, fixedClock)
			break
		}
	}

	// Spot checks, one per view plus the two the succession pair shares a name for.
	// The values come from the fixture rows named in ioTestRows, and the labels half
	// is what makes an io spot check mean anything: a value with the wrong labels is a
	// value attached to the wrong series.
	for _, tc := range []struct {
		metric string
		labels string
		value  float64
	}{
		{"io_reads_total", "backend_type=client backend,context=normal,object=relation", 1000},
		{"io_reads_total", "backend_type=background writer,context=normal,object=relation", 10},
		{"io_op_bytes_total", "backend_type=client backend,context=normal,object=relation", 8192000},
		{"io_fsync_time_seconds_total", "backend_type=background writer,context=normal,object=relation", 0},
		{"slru_blocks_hit_total", "name=xact", 1000},
		{"slru_flushes_total", "name=xact", 5},
		{"bgwriter_buffers_clean_total", "", 100},
		{"bgwriter_buffers_alloc_total", "", 300},
		{"checkpoints_timed_total", "", 7},
		{"checkpoints_requested_total", "", 1},
		{"checkpoint_write_time_seconds_total", "", 40},
		{"buffers_checkpoint_total", "", 900},
		{"restartpoints_done_total", "", 3},
	} {
		var found *Sample
		for i, s := range res.Samples {
			if s.Metric == tc.metric && labelsKey(s.Labels) == tc.labels {
				found = &res.Samples[i]
				break
			}
		}
		if found == nil {
			t.Errorf("no sample for %s{%s}\n%s", tc.metric, tc.labels, dumpSamples(res.Samples))
			continue
		}
		if found.Value != tc.value {
			t.Errorf("%s{%s} = %v, want %v", tc.metric, tc.labels, found.Value, tc.value)
		}
	}

	// The epoch columns are R3.3's field, not families of their own — all four views
	// have one and none of the four may export it as a metric.
	for _, metric := range []string{
		"io_stats_reset_timestamp_seconds",
		"slru_stats_reset_timestamp_seconds",
		"bgwriter_stats_reset_timestamp_seconds",
		"checkpointer_stats_reset_timestamp_seconds",
	} {
		if _, ok := byMetric[metric]; ok {
			t.Errorf("%s exists, want stats_reset in Sample.StatsReset and not as a family", metric)
		}
	}
	// And the identity columns are labels, not families: pg_stat_database's datname
	// precedent says a name column is exported as a label and never as a metric.
	for _, metric := range []string{
		"pg_stat_io_backend_type", "pg_stat_io_object", "pg_stat_io_context",
		"pg_stat_slru_name",
	} {
		if _, ok := byMetric[metric]; ok {
			t.Errorf("%s exists, want the identity column as a label (R9.3), not a family", metric)
		}
	}
}

// labelsKey renders a label set as a stable string so two samples can be compared for
// series identity without comparing map pointers.
func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	kv := make([]string, 0, len(labels))
	for k, v := range labels {
		kv = append(kv, k+"="+v)
	}
	sort.Strings(kv)
	return strings.Join(kv, ",")
}

// TestIOCollectOneTransactionPerRound is R2.7 as a count: one Begin, one query per
// projection, one Commit, and the config that reached the opener carrying R2.7's
// statement_timeout and R2.8's application_name.
func TestIOCollectOneTransactionPerRound(t *testing.T) {
	tgt := ioTarget()
	c, opener, tx := newTestIO(t, tgt, ioTestRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if opener.calls != 1 {
		t.Errorf("Begin calls = %d, want 1 (R2.7: one read-only transaction per view group, and this collector has one group)", opener.calls)
	}
	projections, _ := c.projections(tgt)
	if len(tx.queries) != len(projections) {
		t.Fatalf("queries = %d, want %d (one per projection)", len(tx.queries), len(projections))
	}
	for i, q := range tx.queries {
		if q != projections[i].Query {
			t.Errorf("query %d =\n%s\nwant\n%s", i, q, projections[i].Query)
		}
	}
	if tx.commits != 1 || tx.rollbacks != 0 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/0 on the success path", tx.commits, tx.rollbacks)
	}
	if res.RoundTrips != len(projections) {
		t.Errorf("RoundTrips = %d, want %d (R13.1 measures a round from this count)", res.RoundTrips, len(projections))
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

// TestIOCollectQueryErrorKeepsRoundTrips is the Collector contract's "on error, the
// returned Result must still carry the RoundTrips it actually issued", on the second
// of four queries, with the first view's samples still returned (D20's S9).
//
// The second query is the interesting one rather than the fourth: failing the last
// query only proves the loop counts, while failing the second proves the first view's
// samples survive a later view's failure, which is the half a collector with four
// views can get wrong four ways.
func TestIOCollectQueryErrorKeepsRoundTrips(t *testing.T) {
	tgt := ioTarget()
	projections, err := NewIOCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}

	tx := &fakeTx{
		rows:   map[string][]Row{projections[0].Query: ioTestRows(t)[pgversion.ViewStatIO]},
		failOn: projections[1].Query,
	}
	opener := &fakeOpener{tx: tx}
	c := NewIOCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the query failure surfaced")
	}
	if res.RoundTrips != 2 {
		t.Errorf("RoundTrips = %d, want 2 (two queries issued before the failure)", res.RoundTrips)
	}
	if len(res.Samples) != 28 {
		t.Errorf("Samples = %d, want 28 (the pg_stat_io rows survived; the other three views did not run)\n%s",
			len(res.Samples), dumpSamples(res.Samples))
	}
	if tx.rollbacks != 1 || tx.commits != 0 {
		t.Errorf("rollbacks/commits = %d/%d, want 1/0 (a failed round must not leave a transaction open)",
			tx.rollbacks, tx.commits)
	}
	if !strings.Contains(err.Error(), string(pgversion.ViewStatSLRU)) {
		t.Errorf("error %q does not name the failing view", err)
	}
}

func TestIOCollectCommitError(t *testing.T) {
	tgt := ioTarget()
	c, _, tx := newTestIO(t, tgt, ioTestRows(t))
	tx.commitErr = errors.New("fake: connection lost")

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the commit failure surfaced")
	}
	projections, _ := c.projections(tgt)
	if res.RoundTrips != len(projections) {
		t.Errorf("RoundTrips = %d, want %d (the queries did run)", res.RoundTrips, len(projections))
	}
	if len(res.Samples) == 0 {
		t.Error("Samples empty, want the read samples returned alongside the error")
	}
	if tx.commits != 1 || tx.rollbacks != 1 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/1 (a failed COMMIT may leave the transaction open on a pooled connection; the best-effort Rollback is what closes it)",
			tx.commits, tx.rollbacks)
	}
}

func TestIOCollectBeginError(t *testing.T) {
	tgt := ioTarget()
	opener := &fakeOpener{beginErr: errors.New("fake: connect refused")}
	c := NewIOCollector(opener, SQLConfig{})

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

// TestIOCollectRefusesUnknownMajors covers both refusals in one table: majors the
// matrix does not know at all (0, 12, 19) and majors it knows but this collector does
// not apply to (13, 14, 15). The second group is the one that costs something — on
// PG15 three of the four views have column sets, so a collector without the
// descriptor check would open a transaction and run three queries to produce a result
// §3.3 says does not exist.
func TestIOCollectRefusesUnknownMajors(t *testing.T) {
	for _, major := range []int{0, 12, 13, 14, 15, 19} {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		opener := &fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}
		c := NewIOCollector(opener, SQLConfig{})

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

// TestIOKindOfCoversEveryMatrixColumn is the test that makes the classifier on the
// type doc enforced rather than documented.
//
// It walks every column the matrix has for all four views on every major and fails on
// kindUnknown. The point is the PG18-only columns: op_bytes's three successors, and
// the checkpointer's num_done and slru_written, are each a column that exists on one
// major and not the others, so a switch written against one column set and reused for
// the other leaves a hole this test lands in. database.go's permissive
// `default: return kindMetric` would instead classify a new column as a number and
// leave the test green with a family R9.4 never named.
func TestIOKindOfCoversEveryMatrixColumn(t *testing.T) {
	checked := 0
	for _, view := range ioViews {
		for _, major := range pgversion.Majors() {
			cols, ok := pgversion.Columns(view, major)
			if !ok {
				continue // pg_stat_checkpointer has no column set below 17.
			}
			for _, col := range cols {
				checked++
				if got := ioKindOf(view, col); got == kindUnknown {
					t.Errorf("ioKindOf(%s, %s) = unknown on PG%d: the matrix gained a column and "+
						"this file did not notice (see the classifier's doc)", view, col, major)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no matrix columns checked, so nothing was checked")
	}
	// The now() alias is every query's own column and not a matrix one, so the loop
	// above cannot reach it; it is classified here.
	if got := ioKindOf(pgversion.ViewStatIO, SourceNowColumn); got != kindSourceNow {
		t.Errorf("ioKindOf(pg_stat_io, %s) = %s, want source_now (R13.11's first timestamp)", SourceNowColumn, got)
	}
	// And a column no view has must stay unknown, or the classifier's default is a lie.
	if got := ioKindOf(pgversion.ViewStatIO, "not_a_column"); got != kindUnknown {
		t.Errorf("ioKindOf(pg_stat_io, not_a_column) = %s, want unknown", got)
	}
}

// TestIOSamplesLabelled pins the per-row builder on the three rules the end-to-end
// test can only sample: a metric column's value carries the row's own identity labels,
// a row with a missing identity column produces nothing at all rather than a partial
// label set, and the epoch is that row's own stats_reset rather than a max over the
// rows (which is the summed shape's rule and the two are not interchangeable).
func TestIOSamplesLabelled(t *testing.T) {
	cases := []struct {
		name   string
		pv     viewProjection
		rows   []Row
		want   int
		metric string
		labels string
		value  float64
		epoch  time.Time
	}{
		{
			// Two pg_stat_io rows, 14 metric columns each.
			name:   "pg_stat_io, two rows",
			pv:     ioProjectionFor(t, pgversion.ViewStatIO, 17),
			rows:   ioTestRows(t)[pgversion.ViewStatIO],
			want:   28,
			metric: "io_reads_total",
			labels: "backend_type=background writer,context=normal,object=relation",
			value:  10,
			epoch:  ioEpochNew,
		},
		{
			// The second row's own epoch is 2026-06-01 and the first row's is
			// 2026-01-01: the labelled shape keeps them apart, which is the half of
			// the difference between the two builders that a summed test cannot show.
			name:   "pg_stat_slru, one row",
			pv:     ioProjectionFor(t, pgversion.ViewStatSLRU, 17),
			rows:   ioTestRows(t)[pgversion.ViewStatSLRU],
			want:   7,
			metric: "slru_blocks_hit_total",
			labels: "name=xact",
			value:  1000,
			epoch:  ioEpochOld,
		},
		{
			// The PG18 pg_stat_io set: 20 columns, of which 3 are identity, one is the
			// epoch and 16 are metrics — op_bytes gone, its three successors present.
			// One row, so 16 samples, and the successor names are the ones the naming
			// table gives them.
			name: "pg_stat_io, PG18 set",
			pv:   ioProjectionFor(t, pgversion.ViewStatIO, 18),
			rows: []Row{{
				"backend_type": "client backend", "object": "relation", "context": "normal",
				"reads": "3", "read_bytes": "4", "read_time": "5", "writes": "6",
				"write_bytes": "7", "write_time": "8", "writebacks": "9",
				"writeback_time": "10", "extends": "11", "extend_bytes": "12",
				"extend_time": "13", "hits": "14", "evictions": "15", "reuses": "16",
				"fsyncs": "17", "fsync_time": "18",
				"stats_reset": "2026-06-01 00:00:00+00", SourceNowColumn: fixedSourceNow,
			}},
			want:   16,
			metric: "io_read_bytes_total",
			labels: "backend_type=client backend,context=normal,object=relation",
			value:  4,
			epoch:  ioEpochNew,
		},
		{
			// A row whose `context` is blank. D20's S8 generalised to N columns: one
			// missing identity column means the row cannot be labelled at all, so all
			// 14 of its metrics disappear rather than reappearing in a series that
			// differs from its siblings by an absent label.
			name: "pg_stat_io, one blank identity column",
			pv:   ioProjectionFor(t, pgversion.ViewStatIO, 17),
			rows: []Row{{
				"backend_type": "client backend", "object": "relation", "context": "  ",
				"reads": "1", "read_time": "1", "writes": "1", "write_time": "1",
				"writebacks": "1", "writeback_time": "1", "extends": "1", "extend_time": "1",
				"op_bytes": "1", "hits": "1", "evictions": "1", "reuses": "1",
				"fsyncs": "1", "fsync_time": "1",
				"stats_reset": "2026-01-01 00:00:00+00", SourceNowColumn: fixedSourceNow,
			}},
			want: 0,
		},
		{
			// One unparseable metric in an otherwise complete row: 13 samples, and
			// io_reads_total specifically absent, so a 0 was not invented for it.
			name: "pg_stat_io, one unparseable metric",
			pv:   ioProjectionFor(t, pgversion.ViewStatIO, 17),
			rows: []Row{{
				"backend_type": "client backend", "object": "relation", "context": "normal",
				"reads": "x", "read_time": "1", "writes": "1", "write_time": "1",
				"writebacks": "1", "writeback_time": "1", "extends": "1", "extend_time": "1",
				"op_bytes": "1", "hits": "1", "evictions": "1", "reuses": "1",
				"fsyncs": "1", "fsync_time": "1",
				"stats_reset": "2026-01-01 00:00:00+00", SourceNowColumn: fixedSourceNow,
			}},
			want:   13,
			metric: "io_writes_total",
			labels: "backend_type=client backend,context=normal,object=relation",
			value:  1,
			epoch:  ioEpochOld,
		},
		{name: "pg_stat_io, no rows", pv: ioProjectionFor(t, pgversion.ViewStatIO, 17), rows: nil, want: 0},
	}

	for _, tc := range cases {
		got := ioSamples(tc.pv, fixedClock, tc.rows)
		if len(got) != tc.want {
			t.Errorf("%s: %d samples, want %d\n%s", tc.name, len(got), tc.want, dumpSamples(got))
			continue
		}
		if tc.want == 0 {
			continue
		}
		var found *Sample
		for i, s := range got {
			if s.Metric == tc.metric && labelsKey(s.Labels) == tc.labels {
				found = &got[i]
				break
			}
		}
		if found == nil {
			t.Errorf("%s: no sample for %s{%s}\n%s", tc.name, tc.metric, tc.labels, dumpSamples(got))
			continue
		}
		if found.Value != tc.value {
			t.Errorf("%s: %s{%s} = %v, want %v", tc.name, tc.metric, tc.labels, found.Value, tc.value)
		}
		if !found.StatsReset.Equal(tc.epoch) {
			t.Errorf("%s: %s{%s} StatsReset = %s, want %s (the row's own stats_reset)",
				tc.name, tc.metric, tc.labels, found.StatsReset, tc.epoch)
		}
		if !found.IngestTime.Equal(fixedClock) {
			t.Errorf("%s: %s IngestTime = %s, want %s", tc.name, tc.metric, found.IngestTime, fixedClock)
		}
		if !found.SourceTime.Equal(sourceNow) {
			t.Errorf("%s: %s SourceTime = %s, want %s", tc.name, tc.metric, found.SourceTime, sourceNow)
		}
	}

	// The unparseable case's negative half, which the table shape cannot express: the
	// table asserts one metric exists, and the reason it exists is that a sibling does
	// not. Asserting both here keeps "13 samples" from being satisfiable by the wrong
	// 13.
	got := ioSamples(ioProjectionFor(t, pgversion.ViewStatIO, 17), fixedClock, []Row{{
		"backend_type": "client backend", "object": "relation", "context": "normal",
		"reads": "x", "read_time": "1", "writes": "1", "write_time": "1",
		"writebacks": "1", "writeback_time": "1", "extends": "1", "extend_time": "1",
		"op_bytes": "1", "hits": "1", "evictions": "1", "reuses": "1",
		"fsyncs": "1", "fsync_time": "1",
		"stats_reset": "2026-01-01 00:00:00+00", SourceNowColumn: fixedSourceNow,
	}})
	for _, s := range got {
		if s.Metric == "io_reads_total" {
			t.Errorf("io_reads_total exists with value %v, want no sample (a 0 measures nothing and "+
				"reads as a backend that performed no reads)", s.Value)
		}
	}
}

// TestIOSamplesSummed pins the aggregate builder on the succession pair: the sum over
// the rows is the value, a column no row answers for produces no family rather than a
// 0, the epoch is the newest stats_reset among the contributing rows, and the PG18
// checkpointer set yields ten families where the PG17 set yields eight.
func TestIOSamplesSummed(t *testing.T) {
	cases := []struct {
		name  string
		pv    viewProjection
		rows  []Row
		want  map[string]float64
		epoch time.Time
	}{
		{
			// The PG17 bgwriter set is four columns — buffers_clean, maxwritten_clean,
			// buffers_alloc and stats_reset — because the other seven moved to the
			// checkpointer at PG17 or were retired. Three families, unlabelled.
			name:  "pg_stat_bgwriter, PG17 set",
			pv:    ioProjectionFor(t, pgversion.ViewStatBgwriter, 17),
			rows:  ioTestRows(t)[pgversion.ViewStatBgwriter],
			want:  map[string]float64{"bgwriter_buffers_clean_total": 100, "bgwriter_maxwritten_clean_total": 2, "bgwriter_buffers_alloc_total": 300},
			epoch: ioEpochOld,
		},
		{
			// The PG17 checkpointer set: eight metrics plus the epoch. The five names it
			// shares with the bgwriter's moved columns (checkpoints_timed_total,
			// checkpoints_requested_total, checkpoint_write_time_seconds_total,
			// checkpoint_sync_time_seconds_total, buffers_checkpoint_total) come from
			// the succession walk, which is why the two views produce no duplicate
			// families on PG17.
			name: "pg_stat_checkpointer, PG17 set",
			pv:   ioProjectionFor(t, pgversion.ViewStatCheckpointer, 17),
			rows: ioTestRows(t)[pgversion.ViewStatCheckpointer],
			want: map[string]float64{
				"checkpoints_timed_total":             7,
				"checkpoints_requested_total":         1,
				"checkpoint_write_time_seconds_total": 40,
				"checkpoint_sync_time_seconds_total":  8,
				"buffers_checkpoint_total":            900,
				"restartpoints_timed_total":           3,
				"restartpoints_requested_total":       0,
				"restartpoints_done_total":            3,
			},
			epoch: ioEpochNew,
		},
		{
			// The PG18 set is eleven columns, two more than PG17's nine: num_done and
			// slru_written. Both are their own canonicals (succession.go has no row
			// mentioning either), so both name from their own rows and the set yields
			// ten families where the PG17 set yields eight.
			name: "pg_stat_checkpointer, PG18 set",
			pv:   ioProjectionFor(t, pgversion.ViewStatCheckpointer, 18),
			rows: []Row{{
				"num_timed": "1", "num_requested": "2", "num_done": "3",
				"restartpoints_timed": "4", "restartpoints_req": "5", "restartpoints_done": "6",
				"write_time": "7", "sync_time": "8", "buffers_written": "9",
				"slru_written": "10", "stats_reset": "2026-06-01 00:00:00+00",
				SourceNowColumn: fixedSourceNow,
			}},
			want: map[string]float64{
				"checkpoints_timed_total":             1,
				"checkpoints_requested_total":         2,
				"checkpoints_done_total":              3,
				"restartpoints_timed_total":           4,
				"restartpoints_requested_total":       5,
				"restartpoints_done_total":            6,
				"checkpoint_write_time_seconds_total": 7,
				"checkpoint_sync_time_seconds_total":  8,
				"buffers_checkpoint_total":            9,
				"checkpointer_slru_written_total":     10,
			},
			epoch: ioEpochNew,
		},
		{
			// A column no row answers for has no family. The other columns still do, so
			// this is the case where a 0 would be a measurement of nothing: buffers_clean
			// is unparseable and maxwritten_clean is not, and only the second exists.
			name: "pg_stat_bgwriter, one column unparseable",
			pv:   ioProjectionFor(t, pgversion.ViewStatBgwriter, 17),
			rows: []Row{{
				"buffers_clean": "x", "maxwritten_clean": "4", "buffers_alloc": "6",
				"stats_reset": "2026-01-01 00:00:00+00", SourceNowColumn: fixedSourceNow,
			}},
			want:  map[string]float64{"bgwriter_maxwritten_clean_total": 4, "bgwriter_buffers_alloc_total": 6},
			epoch: ioEpochOld,
		},
		{name: "pg_stat_bgwriter, no rows", pv: ioProjectionFor(t, pgversion.ViewStatBgwriter, 17), rows: nil, want: nil},
	}

	for _, tc := range cases {
		got := ioSamples(tc.pv, fixedClock, tc.rows)
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
				t.Errorf("%s: %s labels = %v, want none (a cluster aggregate has no series-specific "+
					"labels; the identity half is the scheduler's)", tc.name, s.Metric, s.Labels)
			}
			if !s.IngestTime.Equal(fixedClock) {
				t.Errorf("%s: %s IngestTime = %s, want %s", tc.name, s.Metric, s.IngestTime, fixedClock)
			}
			if !s.SourceTime.Equal(sourceNow) {
				t.Errorf("%s: %s SourceTime = %s, want %s", tc.name, s.Metric, s.SourceTime, sourceNow)
			}
			if !s.StatsReset.Equal(tc.epoch) {
				t.Errorf("%s: %s StatsReset = %s, want %s (the newest stats_reset among the rows that "+
					"contributed, not the first row's and not a zero)", tc.name, s.Metric, s.StatsReset, tc.epoch)
			}
		}
	}
}
