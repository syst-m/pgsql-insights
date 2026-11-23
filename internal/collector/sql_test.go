package collector

import (
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// TestProjectQueryIsTheMatrixQuery pins the two SQL-side rules that live in
// Project: R2.4 (the column set comes from the matrix, per major) and R2.5 (the
// FROM clause is schema-qualified).
//
// The expected string is rebuilt from the matrix in the test rather than
// hardcoded, because the hardcoded version would be a second copy of the table
// and the failure it catches is "the builder stopped asking the matrix" — which
// only a second, independent construction of the same answer can see. The
// pg_database case carries extras so the alias form is covered too.
func TestProjectQueryIsTheMatrixQuery(t *testing.T) {
	cases := []struct {
		view  pgversion.View
		extra []Extra
	}{
		{view: pgversion.ViewStatDatabase},
		{view: pgversion.ViewStatDatabaseConflicts},
		{view: pgversion.ViewDatabase, extra: databaseExtras[pgversion.ViewDatabase]},
	}

	for _, tc := range cases {
		for _, major := range pgversion.Majors() {
			if !pgversion.Exists(tc.view, major) {
				continue
			}
			p, ok := Project(tc.view, major, tc.extra...)
			if !ok {
				t.Fatalf("Project(%s, %d): not ok", tc.view, major)
			}

			cols, _ := pgversion.Columns(tc.view, major)
			want := "SELECT " + strings.Join(cols, ", ")
			for _, e := range tc.extra {
				want += ", " + e.Expr + " AS " + e.Alias
			}
			want += ", now() AS " + SourceNowColumn
			want += " FROM " + pgversion.Schema(tc.view) + "." + string(tc.view)

			if p.Query != want {
				t.Errorf("Project(%s, %d).Query =\n%s\nwant\n%s", tc.view, major, p.Query, want)
			}
			if err := validateProjection(p); err != nil {
				t.Errorf("validateProjection(%s, %d): %v", tc.view, major, err)
			}
		}
	}
}

// TestProjectColumns pins the column-name list: matrix columns in order, then the
// extra aliases, then the now() alias. A collector iterates this list, so a
// missing entry is a metric that silently stops being reported.
func TestProjectColumns(t *testing.T) {
	p, ok := Project(pgversion.ViewDatabase, pgversion.Major16, databaseExtras[pgversion.ViewDatabase]...)
	if !ok {
		t.Fatal("Project(pg_database, 16): not ok")
	}
	cols, _ := pgversion.Columns(pgversion.ViewDatabase, pgversion.Major16)

	want := append(append([]string{}, cols...), "size", "frozenxid_age", "minmxid_age", SourceNowColumn)
	if len(p.Columns) != len(want) {
		t.Fatalf("Columns len = %d, want %d (%v)", len(p.Columns), len(want), p.Columns)
	}
	for i, w := range want {
		if p.Columns[i] != w {
			t.Errorf("Columns[%d] = %q, want %q", i, p.Columns[i], w)
		}
	}
}

// TestProjectUnknownViewMajor is the R2.4 answer for a view a major does not have:
// not ok, and no query. Building SQL from an empty column list would produce
// "SELECT  FROM pg_catalog.x", which is a syntax error on the instance rather
// than an error in Go.
func TestProjectUnknownViewMajor(t *testing.T) {
	if _, ok := Project(pgversion.ViewStatIO, pgversion.Major13); ok {
		t.Error("Project(pg_stat_io, 13) = ok, want not ok (pg_stat_io exists from 16)")
	}
	if _, ok := Project(pgversion.View("pg_nope"), pgversion.Major16); ok {
		t.Error("Project(pg_nope, 16) = ok, want not ok (unknown view)")
	}
	if _, ok := Project(pgversion.ViewStatDatabase, 12); ok {
		t.Error("Project(pg_stat_database, 12) = ok, want not ok (12 is outside the matrix)")
	}
}

func TestSQLConfigWithDefaults(t *testing.T) {
	got := SQLConfig{}.withDefaults()
	if got.StatementTimeout != DefaultStatementTimeout {
		t.Errorf("StatementTimeout = %s, want %s (R2.7's 5 s)", got.StatementTimeout, DefaultStatementTimeout)
	}
	if got.ApplicationName != ApplicationName {
		t.Errorf("ApplicationName = %q, want %q (R2.8)", got.ApplicationName, ApplicationName)
	}
	if got.Now == nil {
		t.Fatal("Now = nil, want time.Now")
	}

	// Set values are kept, including a timeout below the default: R2.7's flag is
	// a knob, and a config layer that clamped 1 s up to 5 s would make
	// --statement-timeout=1s unconfigurable.
	custom := SQLConfig{StatementTimeout: time.Second, ApplicationName: "pgsi-lab"}.withDefaults()
	if custom.StatementTimeout != time.Second || custom.ApplicationName != "pgsi-lab" {
		t.Errorf("custom config = %+v, want 1s / pgsi-lab", custom)
	}
}

// TestMetricName pins the naming rule clock.go's Sample.Metric documents —
// view + column, no R9.2 prefix — using clock.go's own example as the case, so
// the doc and the function cannot drift.
func TestMetricName(t *testing.T) {
	cases := []struct {
		view   pgversion.View
		column string
		want   string
	}{
		{pgversion.ViewStatDatabase, "xact_commit", "pg_stat_database_xact_commit"},
		{pgversion.ViewStatDatabase, "blks_hit", "pg_stat_database_blks_hit"},
		{pgversion.ViewDatabase, "size", "pg_database_size"},
		{pgversion.ViewDatabase, "frozenxid_age", "pg_database_frozenxid_age"},
		{pgversion.ViewDatabase, "minmxid_age", "pg_database_minmxid_age"},
		{pgversion.ViewStatDatabaseConflicts, "confl_lock", "pg_stat_database_conflicts_confl_lock"},
	}
	for _, tc := range cases {
		if got := MetricName(tc.view, tc.column); got != tc.want {
			t.Errorf("MetricName(%s, %s) = %q, want %q", tc.view, tc.column, got, tc.want)
		}
	}
}

// TestAppliesToViews is R2.1's applicability for a multi-view collector: every
// view must exist, not any.
func TestAppliesToViews(t *testing.T) {
	all := AppliesToViews(databaseViews...)
	for _, major := range pgversion.Majors() {
		if !all(major) {
			t.Errorf("AppliesToViews(database views)(%d) = false, want true (all three exist on every matrix major)", major)
		}
	}
	for _, major := range []int{12, 19, 0} {
		if all(major) {
			t.Errorf("AppliesToViews(database views)(%d) = true, want false", major)
		}
	}

	// The all-vs-any rule, isolated: pg_stat_io exists only on 16-18, so a
	// collector pairing it with pg_database must refuse 13 even though
	// pg_database itself exists there.
	pair := AppliesToViews(pgversion.ViewDatabase, pgversion.ViewStatIO)
	if pair(pgversion.Major13) {
		t.Error("AppliesToViews(pg_database, pg_stat_io)(13) = true, want false (pg_stat_io is 16+; the rule is all views, not any)")
	}
	if !pair(pgversion.Major16) {
		t.Error("AppliesToViews(pg_database, pg_stat_io)(16) = false, want true")
	}

	if AppliesToViews()(pgversion.Major16) {
		t.Error("AppliesToViews()(16) = true, want false (a collector that reads no views has no reason to run)")
	}
}

func TestParseValue(t *testing.T) {
	cases := []struct {
		raw   string
		want  float64
		valid bool
	}{
		{"0", 0, true},
		{"42", 42, true},
		{"-3", -3, true},
		{"1.5", 1.5, true},
		{"1e3", 1000, true},
		{"9223372036854775807", 9223372036854775807, true},
		{"t", 1, true},
		{"f", 0, true},
		{"true", 1, true},
		{"false", 0, true},
		{"", 0, false},
		{"postgres", 0, false},
		{"2026-11-22 10:00:00+00", 0, false},
		{"1,5", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseValue(tc.raw)
		if ok != tc.valid {
			t.Errorf("parseValue(%q) ok = %v, want %v", tc.raw, ok, tc.valid)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("parseValue(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	want := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		raw   string
		want  time.Time
		valid bool
	}{
		{"2026-11-22 10:00:00+00", want, true},
		{"2026-11-22 10:00:00.123456+00", time.Date(2026, 11, 22, 10, 0, 0, 123456000, time.UTC), true},
		{"2026-11-22T10:00:00Z", want, true},
		{"2026-11-22 11:00:00+01:00", want, true},
		{"", time.Time{}, false},
		{"0", time.Time{}, false},
		{"not a time", time.Time{}, false},
	}
	for _, tc := range cases {
		got, ok := parseTimestamp(tc.raw)
		if ok != tc.valid {
			t.Errorf("parseTimestamp(%q) ok = %v, want %v", tc.raw, ok, tc.valid)
			continue
		}
		if ok && !got.Equal(tc.want) {
			t.Errorf("parseTimestamp(%q) = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

// TestValidateProjection checks the two typos that would otherwise surface as a
// Postgres syntax error: a column name that is not an identifier, and a FROM
// clause with no schema (R2.5's whole point).
func TestValidateProjection(t *testing.T) {
	if err := validateProjection(Projection{
		Query:   "SELECT a, b FROM pg_catalog.pg_database",
		Columns: []string{"a", "b"},
	}); err != nil {
		t.Errorf("valid projection rejected: %v", err)
	}

	if err := validateProjection(Projection{
		Query:   "SELECT a FROM pg_database",
		Columns: []string{"a"},
	}); err == nil {
		t.Error("unqualified FROM accepted, want error (R2.5)")
	}
	if err := validateProjection(Projection{
		Query:   "SELECT a FROM pg_catalog.pg_database",
		Columns: []string{"a", "bad-name"},
	}); err == nil {
		t.Error("column name with a hyphen accepted, want error")
	}
	if err := validateProjection(Projection{
		Query:   "SELECT a FROM pg_catalog.pg_database",
		Columns: []string{"a", ""},
	}); err == nil {
		t.Error("empty column name accepted, want error")
	}
	// The extension's own schema is the exception R2.5 names, so it validates too.
	if err := validateProjection(Projection{
		Query:   "SELECT a FROM pg_stat_statements.pg_stat_statements",
		Columns: []string{"a"},
	}); err != nil {
		t.Errorf("pg_stat_statements schema rejected: %v", err)
	}
}
