package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract: the bloat collector is a Collector, checked
// by the compiler rather than by a test that has to remember to call it.
var _ Collector = (*BloatCollector)(nil)

// bloatTarget is a PG17 SQL target.
//
// It names the major rather than borrowing tgtPG's 16 for the reason io_test.go gives
// for ioTarget: the fixture is built from one major's column set, and the two views
// here differ by major (pg_stat_user_tables is 23 columns on 13-15, 26 on 16-17, 30 on
// 18; pg_stat_user_indexes is 8 on 13-15 and 9 on 16+). PG17 is the major whose sets
// match the naming table's 45 rows minus the four PG18-only total_*_time columns, so a
// fixture built here cannot be a row with columns the query does not select.
func bloatTarget() Target {
	return Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: 17}
}

// bloatRowFor builds one row for one view on one major with every projection column
// filled and the columns in over set on top.
//
// It builds from Project rather than from pgversion.Columns so the Extra aliases
// (total_bytes, dead_ratio, next_autovacuum, over_autovacuum_threshold, never_analyzed,
// size) are in the row: they are part of the SELECT list and
// the builder reads them like any other column, so a fixture that omitted them would
// silently drop the derived half of both requirements this collector now serves.
func bloatRowFor(t *testing.T, view pgversion.View, major int, over map[string]string) Row {
	t.Helper()
	p, ok := Project(view, major, bloatSpecs[view])
	if !ok {
		t.Fatalf("Project(%s, %d): not ok", view, major)
	}
	row := Row{}
	for _, c := range p.Columns {
		switch c {
		case SourceNowColumn:
			row[c] = fixedSourceNow
		case "schemaname":
			row[c] = "public"
		case "relname":
			row[c] = "orders"
		case "indexrelname":
			row[c] = "orders_pkey"
		default:
			row[c] = "7"
		}
	}
	for k, v := range over {
		row[k] = v
	}
	return row
}

// bloatTestRows is the fixture the collect-path tests share: two rows per view.
//
// Two rows is database_test.go's D20's-S12 argument applied here. With one row, "one
// sample per metric column" is the whole hypothesis, so every bug that only shows up on
// a second row — a label read off the wrong row, a per-row timestamp that leaks between
// rows, a builder that aggregates where it should label — has no test that can see it.
// The second index row is the one R7.2's unused-index detection is about: idx_scan = 0
// with a non-zero size, which is the shape the requirement names and which a fixture
// where every index is scanned would never produce.
func bloatTestRows(t *testing.T, major int) map[pgversion.View][]Row {
	t.Helper()
	return map[pgversion.View][]Row{
		pgversion.ViewStatUserTables: {
			bloatRowFor(t, pgversion.ViewStatUserTables, major, map[string]string{
				"relname": "orders", "n_live_tup": "1000", "n_dead_tup": "250",
				"total_bytes": "81920", "dead_ratio": "0.2",
				// R7.4's three for a table that has been analyzed and is over its threshold.
				"next_autovacuum": "1731000000", "over_autovacuum_threshold": "1", "never_analyzed": "0",
			}),
			bloatRowFor(t, pgversion.ViewStatUserTables, major, map[string]string{
				"relname": "audit_log", "n_live_tup": "9000", "n_dead_tup": "1000",
				"total_bytes": "16384", "dead_ratio": "0.1",
				// The never-analyzed half of R7.4, on the row that has neither last_analyze
				// nor last_autoanalyze in the real view.
				"next_autovacuum": "1731000600", "over_autovacuum_threshold": "1", "never_analyzed": "1",
			}),
		},
		pgversion.ViewStatUserIndexes: {
			bloatRowFor(t, pgversion.ViewStatUserIndexes, major, map[string]string{
				"indexrelname": "orders_pkey", "idx_scan": "4200", "size": "8192",
			}),
			bloatRowFor(t, pgversion.ViewStatUserIndexes, major, map[string]string{
				"indexrelname": "orders_idx_created", "idx_scan": "0", "size": "49152",
			}),
		},
	}
}

// newTestBloat builds the collector plus the fake opener and tx it runs on, serving
// rows for every projection the collector would issue against tgt.
func newTestBloat(t *testing.T, tgt Target, rows map[pgversion.View][]Row) (*BloatCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	projections, err := NewBloatCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range projections {
		tx.rows[pv.Query] = rows[pv.view]
	}
	opener := &fakeOpener{tx: tx}
	return NewBloatCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener, tx
}

// bloatFind returns the one sample for a metric with one label value, so a spot check
// names the series it means instead of asserting on whichever row the loop saw last.
func bloatFind(t *testing.T, samples []Sample, metric, labelKey, labelValue string) (Sample, bool) {
	t.Helper()
	var (
		found Sample
		n     int
	)
	for _, s := range samples {
		if s.Metric == metric && s.Labels[labelKey] == labelValue {
			found = s
			n++
		}
	}
	if n > 1 {
		t.Fatalf("%s{%s=%q}: %d samples, want exactly 1", metric, labelKey, labelValue, n)
	}
	return found, n == 1
}

func TestBloatDescriptor(t *testing.T) {
	c, _, _ := newTestBloat(t, bloatTarget(), bloatTestRows(t, 17))
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorBloat {
		t.Errorf("Name = %q, want %q (R7.2's second ruling names it, requirements.md:366)", d.Name, CollectorBloat)
	}
	if d.Interval != BloatInterval {
		t.Errorf("Interval = %s, want %s (§3.3's fourteenth row, requirements.md:208)", d.Interval, BloatInterval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %s, want %s (§3.3's S2)", d.Source, SourceSQL)
	}
	if d.Cost != CostStandard {
		t.Errorf("Cost = %s, want %s (R7.2's ruling: relcache reads, not the `expensive` statement and activity earn)",
			d.Cost, CostStandard)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != bloatViewGroup {
		t.Errorf("ViewGroups = %v, want [%s] (R2.7: one entry per view group, and this collector has one)",
			d.ViewGroups, bloatViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; the tuning knob is R2.7's statement_timeout)", d.Timeout)
	}
	// Both views have column sets on every major the matrix knows, so the collector
	// applies on all of them; 12 and 19 are outside the matrix and must be false.
	for _, major := range pgversion.Majors() {
		if !d.Applies(major) {
			t.Errorf("Applies(%d) = false, want true (both views have column sets on every matrix major)", major)
		}
	}
	for _, major := range []int{12, 19} {
		if d.Applies(major) {
			t.Errorf("Applies(%d) = true, want false", major)
		}
	}
}

// TestBloatQueriesAreSchemaQualified is R2.5 on every major: each query names its
// schema, selects the matrix's column set for that major, and carries the Extras that
// are R7.2's actual subject.
//
// The Extras are the half this test exists for. The FROM clause and the column list come
// from Project, which every collector shares and which its own tests already pin; what
// nothing else checks is that bloat.go asked for the three derived expressions at all,
// because an Extra the collector forgot to declare simply does not appear in the query
// and the rest of the projection still validates.
func TestBloatQueriesAreSchemaQualified(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		projections, err := NewBloatCollector(nil, SQLConfig{}).projections(tgt)
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}
		if len(projections) != len(bloatViews) {
			t.Fatalf("PG%d: %d projections, want %d", major, len(projections), len(bloatViews))
		}
		for _, pv := range projections {
			want := " FROM " + pgversion.Schema(pv.view) + "." + string(pv.view)
			if !strings.Contains(pv.Query, want) {
				t.Errorf("PG%d %s: query %q does not contain %q (R2.5)", major, pv.view, pv.Query, want)
			}
			// Neither view has a WHERE clause: both are scoped to user relations
			// already, and the role can only see rows in a database it has CONNECT on.
			if pv.Where != "" {
				t.Errorf("PG%d %s: unexpected WHERE %q", major, pv.view, pv.Where)
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
					t.Errorf("PG%d %s: query does not carry the Extra %q (R7.2's two pairs are these expressions)",
						major, pv.view, e.Expr+" AS "+e.Alias)
				}
			}
		}
	}
}

// TestBloatDeadRatioGuard pins the denominator guard, which is the difference between a
// round that works and a round that fails outright.
//
// A table that has never been written to has n_live_tup and n_dead_tup both at zero, and
// a float8 division by zero in Postgres is `ERROR: division by zero` — not an empty cell
// but a failed SELECT, which takes the whole query and, in the same transaction, the
// index view's query with it. That is D20's S7's class (database.go's WHERE clause is
// that ruling's fix for the size half: "not just the size column, the whole row set").
// The guard is greatest(..., 1); asserting the exact expression is what stops a future
// edit from "simplifying" it back to the bare sum.
func TestBloatDeadRatioGuard(t *testing.T) {
	e, ok := bloatSpecs[pgversion.ViewStatUserTables].Extras[1], true
	if !ok {
		t.Fatal("no pg_stat_user_indexes spec")
	}
	if e.Alias != "dead_ratio" {
		t.Fatalf("second pg_stat_user_tables Extra alias = %q, want dead_ratio", e.Alias)
	}
	if !strings.Contains(e.Expr, "pg_catalog.greatest(n_live_tup + n_dead_tup, 1)") {
		t.Errorf("dead_ratio expression %q has no greatest(..., 1) denominator guard", e.Expr)
	}
	if !strings.Contains(e.Expr, "n_dead_tup::float8") {
		t.Errorf("dead_ratio expression %q does not cast the numerator to float8; integer division would truncate every ratio to 0", e.Expr)
	}
}

// TestBloatR74Expressions pins the three R7.4 Extras the way TestBloatDeadRatioGuard
// pins R7.2's: by quoting the expression, because an Extra that "simplifies" into the
// wrong SQL still compiles, still returns a number, and still fills a metric. Each of
// the three carries a hazard the compiler cannot see, so the assertions are the hazards
// rather than a comparison to the constant each Extra is assigned from — which cannot
// fail and so documents nothing.
//
//   - next_autovacuum must resolve naptime inside the expression. The ruled formula
//     (`requirements.md:369`) writes the bare word autovacuum_naptime, which is not a
//     column of pg_stat_user_tables; replacing the subquery with a literal interval, or
//     reading the value from the settings collector (option (b), rejected on schedule),
//     both still produce a number and both are wrong.
//   - over_autovacuum_threshold must be a CASE and must not contain coalesce. S143
//     records why the second half matters. The test asserts the prohibition instead of
//     trusting the comment, and asserts the narrower facts the comment rests on by
//     calling unqualifiedCall directly on the two coalesce forms and on the
//     paren-after-keyword shape this Extra works around.
//   - never_analyzed must contain no parenthesis at all, which is the property the
//     ruling names as the reason a CASE is safe to write.
func TestBloatR74Expressions(t *testing.T) {
	spec := bloatSpecs[pgversion.ViewStatUserTables]
	byAlias := make(map[string]string, len(spec.Extras))
	for _, e := range spec.Extras {
		if _, dup := byAlias[e.Alias]; dup {
			t.Errorf("duplicate Extra alias %q on pg_stat_user_tables", e.Alias)
		}
		byAlias[e.Alias] = e.Expr
	}
	for _, alias := range []string{"total_bytes", "dead_ratio", "next_autovacuum", "over_autovacuum_threshold", "never_analyzed"} {
		if _, ok := byAlias[alias]; !ok {
			t.Errorf("no pg_stat_user_tables Extra with alias %s (R7.2's two pairs plus R7.4's three)", alias)
		}
	}

	// next_autovacuum: the ruled formula, with naptime resolved in-expression.
	next := byAlias["next_autovacuum"]
	for _, want := range []string{
		"pg_catalog.date_part('epoch',",
		"last_autovacuum +",
		"(SELECT setting::interval FROM pg_catalog.pg_settings WHERE name = 'autovacuum_naptime')",
	} {
		if !strings.Contains(next, want) {
			t.Errorf("next_autovacuum expression %q is missing %s", next, want)
		}
	}
	if bad := unqualifiedCall(next); bad != "" {
		t.Errorf("unqualifiedCall(next_autovacuum) = %q, want empty", bad)
	}

	// over_autovacuum_threshold: a CASE, no coalesce, and every "(" in a position the
	// byte scan cannot read as a call.
	over := byAlias["over_autovacuum_threshold"]
	if strings.Contains(strings.ToLower(over), "coalesce(") {
		t.Errorf("over_autovacuum_threshold expression %q uses coalesce: unqualifiedCall rejects the bare form and Postgres rejects pg_catalog.coalesce (S143), so this Extra has no way to spell override-or-default except CASE", over)
	}
	for _, want := range []string{
		"CASE WHEN", "THEN", "ELSE", "END",
		"pg_catalog.substring(",
		"FROM pg_catalog.pg_class WHERE oid = relid",
		"FROM pg_catalog.pg_settings WHERE name = 'autovacuum_vacuum_threshold'",
	} {
		if !strings.Contains(over, want) {
			t.Errorf("over_autovacuum_threshold expression %q is missing %s", over, want)
		}
	}
	if bad := unqualifiedCall(over); bad != "" {
		t.Errorf("unqualifiedCall(over_autovacuum_threshold) = %q, want empty: a non-empty answer fails ValidateProjection on every round", bad)
	}

	// S143's fix, asserted rather than narrated. Before sqlKeywords the scan read
	// SQL's own syntax as a callee and these two returned "THEN" and "coalesce" —
	// the false positives this file's Extra dodges by writing the default branch as
	// 0 + (…) instead of a bare subselect. Both now return empty. The third case is
	// unchanged on purpose: the checker accepts pg_catalog.coalesce, so the checker
	// was never what forbade it, and pinning that keeps the fix from being mistaken
	// for a rule that coalesce must be qualified.
	if got := unqualifiedCall("CASE WHEN x IS NULL THEN (SELECT y FROM pg_catalog.pg_class) END"); got != "" {
		t.Errorf(`unqualifiedCall("CASE WHEN x IS NULL THEN (SELECT y FROM pg_catalog.pg_class) END") = %q, want empty (S143: "THEN" is a keyword, not a callee)`, got)
	}
	if got := unqualifiedCall("coalesce(a, b)"); got != "" {
		t.Errorf(`unqualifiedCall("coalesce(a, b)") = %q, want empty (S143: COALESCE is SQL-standard syntax, and there is no pg_catalog.coalesce to qualify it with)`, got)
	}
	if got := unqualifiedCall("pg_catalog.coalesce(a, b)"); got != "" {
		t.Errorf(`unqualifiedCall("pg_catalog.coalesce(a, b)") = %q, want empty: the checker accepts the qualified form, so the checker is not what forbids it — Postgres does (S143)`, got)
	}

	// never_analyzed: no parenthesis anywhere.
	never := byAlias["never_analyzed"]
	if strings.ContainsAny(never, "()") {
		t.Errorf("never_analyzed expression %q contains a parenthesis; the ruling's safety argument for CASE is that it has none", never)
	}
	for _, want := range []string{"CASE WHEN", "last_analyze IS NULL", "AND", "last_autoanalyze IS NULL", "THEN 1", "ELSE 0", "END"} {
		if !strings.Contains(never, want) {
			t.Errorf("never_analyzed expression %q is missing %s", never, want)
		}
	}
}

// TestBloatCollectSamples is the golden round: the expected count is derived — metric
// columns times rows, summed over the two projections — not remembered, and then a set
// of spot checks names the values the fixture set so a builder that emitted the right
// number of samples with the wrong values cannot pass.
func TestBloatCollectSamples(t *testing.T) {
	tgt := bloatTarget()
	rows := bloatTestRows(t, 17)
	c, _, _ := newTestBloat(t, tgt, rows)

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	projections, _ := c.projections(tgt)
	want := 0
	for _, pv := range projections {
		metrics := 0
		for _, col := range pv.Columns {
			if bloatKindOf(pv.view, col) == kindMetric {
				metrics++
			}
		}
		want += metrics * len(rows[pv.view])
	}
	if want == 0 {
		t.Fatal("derived 0 expected samples, so nothing was checked")
	}
	if len(res.Samples) != want {
		t.Fatalf("len(Samples) = %d, want %d (metric columns x rows across the two projections)\n%s",
			len(res.Samples), want, dumpSamples(res.Samples))
	}

	// The three values R7.2 names, plus one plain counter, checked by value and label,
	// followed by the three R7.4 ones on both of the table rows so each of the six
	// R7.2+R7.4 values is checked on at least one row by value.
	for _, want := range []struct {
		metric, key, value string
		valueOf            float64
	}{
		{"table_dead_tuples", "relname", "orders", 250},
		{"table_total_bytes", "relname", "orders", 81920},
		{"table_dead_ratio", "relname", "orders", 0.2},
		{"table_next_autovacuum_timestamp_seconds", "relname", "orders", 1731000000},
		{"table_over_autovacuum_threshold", "relname", "orders", 1},
		{"table_never_analyzed", "relname", "orders", 0},
		{"table_never_analyzed", "relname", "audit_log", 1},
		{"table_seq_scan_total", "relname", "orders", 7},
		{"index_idx_scan_total", "indexrelname", "orders_idx_created", 0},
		{"index_size_bytes", "indexrelname", "orders_idx_created", 49152},
	} {
		s, ok := bloatFind(t, res.Samples, want.metric, want.key, want.value)
		if !ok {
			t.Errorf("no sample for %s{%s=%q}", want.metric, want.key, want.value)
			continue
		}
		if s.Value != want.valueOf {
			t.Errorf("%s{%s=%q} = %v, want %v", want.metric, want.key, want.value, s.Value, want.valueOf)
		}
		if s.IngestTime != fixedClock {
			t.Errorf("%s IngestTime = %s, want the injected clock %s (R13.11)", want.metric, s.IngestTime, fixedClock)
		}
		if !s.SourceTime.Equal(sourceNow) {
			t.Errorf("%s SourceTime = %s, want %s (R13.11's query-side timestamp)", want.metric, s.SourceTime, sourceNow)
		}
		// Neither view has a stats_reset column, so the R3.3 epoch is unavailable here
		// by construction and the field must stay zero rather than borrow one.
		if !s.StatsReset.IsZero() {
			t.Errorf("%s StatsReset = %s, want the zero value (neither view has a stats_reset column)",
				want.metric, s.StatsReset)
		}
	}
}

// TestBloatCollectOneTransactionPerRound is R2.7's shape: one Begin, one query per
// projection, one Commit, and the SQLConfig that reaches the connection.
func TestBloatCollectOneTransactionPerRound(t *testing.T) {
	tgt := bloatTarget()
	c, opener, tx := newTestBloat(t, tgt, bloatTestRows(t, 17))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if opener.calls != 1 {
		t.Errorf("Begin calls = %d, want 1 (R2.7: one read-only transaction per view group, and this collector has one)",
			opener.calls)
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
		t.Errorf("Begin cfg.StatementTimeout = %s, want %s (R2.7's statement_timeout reaches the connection, not just the collector)",
			cfg.StatementTimeout, DefaultStatementTimeout)
	}
	if cfg.ApplicationName != ApplicationName {
		t.Errorf("Begin cfg.ApplicationName = %q, want %q (R2.8's self-exclusion name)", cfg.ApplicationName, ApplicationName)
	}
	if opener.targets[0] != tgt {
		t.Errorf("Begin target = %+v, want %+v", opener.targets[0], tgt)
	}
}

// TestBloatCollectQueryErrorKeepsRoundTrips is the Collector contract's "on error the
// Result still carries the RoundTrips actually issued", on the second of two queries —
// the only one of the two that proves the first view's samples survive a later view's
// failure rather than merely proving the loop counts.
func TestBloatCollectQueryErrorKeepsRoundTrips(t *testing.T) {
	tgt := bloatTarget()
	rows := bloatTestRows(t, 17)
	projections, err := NewBloatCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{
		rows:   map[string][]Row{projections[0].Query: rows[pgversion.ViewStatUserTables]},
		failOn: projections[1].Query,
	}
	opener := &fakeOpener{tx: tx}
	c := NewBloatCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the query failure surfaced")
	}
	if res.RoundTrips != 2 {
		t.Errorf("RoundTrips = %d, want 2 (two queries issued before the failure)", res.RoundTrips)
	}
	tableMetrics := 0
	for _, col := range projections[0].Columns {
		if bloatKindOf(pgversion.ViewStatUserTables, col) == kindMetric {
			tableMetrics++
		}
	}
	if len(res.Samples) != tableMetrics*len(rows[pgversion.ViewStatUserTables]) {
		t.Errorf("Samples = %d, want %d (the pg_stat_user_tables rows survived; the index query did not)\n%s",
			len(res.Samples), tableMetrics*len(rows[pgversion.ViewStatUserTables]), dumpSamples(res.Samples))
	}
	if tx.rollbacks != 1 || tx.commits != 0 {
		t.Errorf("rollbacks/commits = %d/%d, want 1/0 (a failed round must not leave a transaction open)",
			tx.rollbacks, tx.commits)
	}
	if !strings.Contains(err.Error(), string(pgversion.ViewStatUserIndexes)) {
		t.Errorf("error %q does not name the failing view", err)
	}
}

// TestBloatCollectNamelessRowProducesNoSamples is the invalid-exposition rule on the
// labelled builder: a row whose identity columns are incomplete yields nothing at all,
// rather than a sample with a partial label set that would collide with the next row's.
func TestBloatCollectNamelessRowProducesNoSamples(t *testing.T) {
	tgt := bloatTarget()
	projections, err := NewBloatCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	var (
		tablesQuery string
		indexQuery  string
	)
	for _, pv := range projections {
		if pv.view == pgversion.ViewStatUserTables {
			tablesQuery = pv.Query
		} else {
			indexQuery = pv.Query
		}
	}
	// One complete row and one with a blank relname.
	good := bloatRowFor(t, pgversion.ViewStatUserTables, 17, map[string]string{"relname": "orders"})
	bad := bloatRowFor(t, pgversion.ViewStatUserTables, 17, map[string]string{"relname": "  "})

	tx := &fakeTx{rows: map[string][]Row{
		tablesQuery: {good, bad},
		indexQuery:  {bloatRowFor(t, pgversion.ViewStatUserIndexes, 17, nil)},
	}}
	c := NewBloatCollector(&fakeOpener{tx: tx}, SQLConfig{Now: func() time.Time { return fixedClock }})

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	tableMetrics := 0
	for _, col := range projections[0].Columns {
		if bloatKindOf(pgversion.ViewStatUserTables, col) == kindMetric {
			tableMetrics++
		}
	}
	indexMetrics := 0
	for _, col := range projections[1].Columns {
		if bloatKindOf(pgversion.ViewStatUserIndexes, col) == kindMetric {
			indexMetrics++
		}
	}
	if got, want := len(res.Samples), tableMetrics+indexMetrics; got != want {
		t.Errorf("Samples = %d, want %d (the blank-relname row yields nothing: %d table metrics from one row, %d index metrics from one row)\n%s",
			got, want, tableMetrics, indexMetrics, dumpSamples(res.Samples))
	}
}

func TestBloatCollectCommitError(t *testing.T) {
	tgt := bloatTarget()
	c, _, tx := newTestBloat(t, tgt, bloatTestRows(t, 17))
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
		t.Errorf("commits/rollbacks = %d/%d, want 1/1 (D20's S9: a failed COMMIT may leave the transaction open on a pooled connection; the best-effort Rollback is what closes it)",
			tx.commits, tx.rollbacks)
	}
}

func TestBloatCollectBeginError(t *testing.T) {
	tgt := bloatTarget()
	opener := &fakeOpener{beginErr: errors.New("fake: connect refused")}
	c := NewBloatCollector(opener, SQLConfig{})

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

// TestBloatCollectRefusesUnknownMajors covers the majors the matrix has no column set
// for. Unlike io_test.go's version there is no second group of "matrix knows it, this
// collector does not apply" majors: both of this collector's views exist on every major
// from 13 to 18, so the applicability check has nothing to refuse inside the matrix.
func TestBloatCollectRefusesUnknownMajors(t *testing.T) {
	for _, major := range []int{0, 12, 19} {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		opener := &fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}
		c := NewBloatCollector(opener, SQLConfig{})

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

// TestBloatKindOfCoversEveryMatrixColumn is the test that makes the classifier on the
// type doc enforced rather than documented: it walks every column the matrix has for
// both views on every major and fails on kindUnknown.
//
// The columns that make it worth writing are the ones that exist on one major and not
// the others — n_tup_newpage_upd and last_seq_scan/last_idx_scan on 16+, the four
// total_*_time columns on 18 — because a switch written against one column set and
// reused for the next leaves a hole this test lands in. The three Extra aliases are not
// matrix columns, so the loop cannot reach them and they are checked alongside.
func TestBloatKindOfCoversEveryMatrixColumn(t *testing.T) {
	checked := 0
	for _, view := range bloatViews {
		for _, major := range pgversion.Majors() {
			cols, ok := pgversion.Columns(view, major)
			if !ok {
				continue
			}
			for _, col := range cols {
				checked++
				if got := bloatKindOf(view, col); got == kindUnknown {
					t.Errorf("bloatKindOf(%s, %s) = unknown on PG%d: the matrix gained a column and "+
						"this file did not notice (see the classifier's doc)", view, col, major)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no matrix columns checked, so nothing was checked")
	}

	for _, view := range bloatViews {
		if got := bloatKindOf(view, SourceNowColumn); got != kindSourceNow {
			t.Errorf("bloatKindOf(%s, %s) = %s, want source_now (R13.11's query-side timestamp)", view, SourceNowColumn, got)
		}
		for _, e := range bloatSpecs[view].Extras {
			if got := bloatKindOf(view, e.Alias); got != kindMetric {
				t.Errorf("bloatKindOf(%s, %s) = %s, want metric (an Extra R7.2 names must be a value)", view, e.Alias, got)
			}
		}
	}
	if got := bloatKindOf(pgversion.ViewStatUserTables, "not_a_column"); got != kindUnknown {
		t.Errorf("bloatKindOf(pg_stat_user_tables, not_a_column) = %s, want unknown", got)
	}
}

// TestBloatKindOfRoles pins the classification itself, not just its coverage: the two
// families' identifier columns are ids, their name columns are names, the six last_*
// columns are text (named in the table, emitted by nobody), and the four gauge counters
// are metrics despite not being counters.
func TestBloatKindOfRoles(t *testing.T) {
	cases := []struct {
		view   pgversion.View
		column string
		want   columnKind
	}{
		// Row identity: join keys and function arguments, not metrics.
		{pgversion.ViewStatUserTables, "relid", kindID},
		{pgversion.ViewStatUserIndexes, "relid", kindID},
		{pgversion.ViewStatUserIndexes, "indexrelid", kindID},
		// R9.3's labels, not values.
		{pgversion.ViewStatUserTables, "schemaname", kindName},
		{pgversion.ViewStatUserTables, "relname", kindName},
		{pgversion.ViewStatUserIndexes, "schemaname", kindName},
		{pgversion.ViewStatUserIndexes, "relname", kindName},
		{pgversion.ViewStatUserIndexes, "indexrelname", kindName},
		// The now() alias fills Sample.SourceTime instead of becoming a sample.
		{pgversion.ViewStatUserTables, SourceNowColumn, kindSourceNow},
		{pgversion.ViewStatUserIndexes, SourceNowColumn, kindSourceNow},
		// The six timestamptz columns: named in the table, emitted by nobody.
		{pgversion.ViewStatUserTables, "last_seq_scan", kindText},
		{pgversion.ViewStatUserTables, "last_idx_scan", kindText},
		{pgversion.ViewStatUserTables, "last_vacuum", kindText},
		{pgversion.ViewStatUserTables, "last_autovacuum", kindText},
		{pgversion.ViewStatUserTables, "last_analyze", kindText},
		{pgversion.ViewStatUserTables, "last_autoanalyze", kindText},
		{pgversion.ViewStatUserIndexes, "last_idx_scan", kindText},
		// The three Extras R7.2 names are metrics despite not being columns.
		{pgversion.ViewStatUserTables, "total_bytes", kindMetric},
		{pgversion.ViewStatUserTables, "dead_ratio", kindMetric},
		{pgversion.ViewStatUserIndexes, "size", kindMetric},
		// R7.4's three, including next_autovacuum: its source column last_autovacuum is
		// kindText two rows above and its own alias is kindMetric, which is the
		// named-but-silent / derived-and-emitting asymmetry the ruling calls intended.
		{pgversion.ViewStatUserTables, "next_autovacuum", kindMetric},
		{pgversion.ViewStatUserTables, "over_autovacuum_threshold", kindMetric},
		{pgversion.ViewStatUserTables, "never_analyzed", kindMetric},
		// Gauges, not counters, and still values.
		{pgversion.ViewStatUserTables, "n_live_tup", kindMetric},
		{pgversion.ViewStatUserTables, "n_dead_tup", kindMetric},
		{pgversion.ViewStatUserTables, "n_mod_since_analyze", kindMetric},
		{pgversion.ViewStatUserTables, "n_ins_since_vacuum", kindMetric},
		// A view this collector does not read has no classification.
		{pgversion.ViewStatDatabase, "datname", kindUnknown},
	}
	for _, tc := range cases {
		if got := bloatKindOf(tc.view, tc.column); got != tc.want {
			t.Errorf("bloatKindOf(%s, %s) = %s, want %s", tc.view, tc.column, got, tc.want)
		}
	}
}
