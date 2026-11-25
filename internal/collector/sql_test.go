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
// pg_database case carries extras and a WHERE clause, so the alias form and the
// filter form (D20's S7) are both covered.
func TestProjectQueryIsTheMatrixQuery(t *testing.T) {
	cases := []struct {
		view pgversion.View
		spec QuerySpec
	}{
		{view: pgversion.ViewStatDatabase},
		{view: pgversion.ViewStatDatabaseConflicts},
		{view: pgversion.ViewDatabase, spec: databaseSpecs[pgversion.ViewDatabase]},
	}

	for _, tc := range cases {
		for _, major := range pgversion.Majors() {
			if !pgversion.Exists(tc.view, major) {
				continue
			}
			p, ok := Project(tc.view, major, tc.spec)
			if !ok {
				t.Fatalf("Project(%s, %d): not ok", tc.view, major)
			}

			cols, _ := pgversion.Columns(tc.view, major)
			want := "SELECT " + strings.Join(cols, ", ")
			for _, e := range tc.spec.Extras {
				want += ", " + e.Expr + " AS " + e.Alias
			}
			want += ", now() AS " + SourceNowColumn
			want += " FROM " + pgversion.Schema(tc.view) + "." + string(tc.view)
			if tc.spec.Where != "" {
				want += " WHERE " + tc.spec.Where
			}

			if p.Query != want {
				t.Errorf("Project(%s, %d).Query =\n%s\nwant\n%s", tc.view, major, p.Query, want)
			}
			if err := ValidateProjection(p); err != nil {
				t.Errorf("ValidateProjection(%s, %d): %v", tc.view, major, err)
			}
		}
	}
}

// TestProjectColumns pins the column-name list: matrix columns in order, then the
// extra aliases, then the now() alias. A collector iterates this list, so a
// missing entry is a metric that silently stops being reported.
func TestProjectColumns(t *testing.T) {
	p, ok := Project(pgversion.ViewDatabase, pgversion.Major16, databaseSpecs[pgversion.ViewDatabase])
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
	if _, ok := Project(pgversion.ViewStatIO, pgversion.Major13, QuerySpec{}); ok {
		t.Error("Project(pg_stat_io, 13) = ok, want not ok (pg_stat_io exists from 16)")
	}
	if _, ok := Project(pgversion.View("pg_nope"), pgversion.Major16, QuerySpec{}); ok {
		t.Error("Project(pg_nope, 16) = ok, want not ok (unknown view)")
	}
	if _, ok := Project(pgversion.ViewStatDatabase, 12, QuerySpec{}); ok {
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
	// R3.1's database. The field exists because pg_extension is per-database and the
	// settings collector has to name the one its SQL runs in; withDefaults is the
	// single place the default is supplied, which is what lets every builder trust the
	// field is non-empty.
	if got.Database != DefaultDatabase {
		t.Errorf("Database = %q, want %q (R3.1's default database)", got.Database, DefaultDatabase)
	}
	if got.Now == nil {
		t.Fatal("Now = nil, want time.Now")
	}

	// Set values are kept, including a timeout below the default: R2.7's flag is
	// a knob, and a config layer that clamped 1 s up to 5 s would make
	// --statement-timeout=1s unconfigurable.
	custom := SQLConfig{StatementTimeout: time.Second, ApplicationName: "pgsi-lab", Database: "appdb"}.withDefaults()
	if custom.StatementTimeout != time.Second || custom.ApplicationName != "pgsi-lab" || custom.Database != "appdb" {
		t.Errorf("custom config = %+v, want 1s / pgsi-lab / appdb", custom)
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
		{pgversion.ViewStatDatabase, "xact_commit", "db_xact_commit_total"},
		{pgversion.ViewStatDatabase, "blks_hit", "db_blks_hit_total"},
		{pgversion.ViewDatabase, "size", "db_size_bytes"},
		{pgversion.ViewDatabase, "frozenxid_age", "db_txid_age"},
		{pgversion.ViewDatabase, "minmxid_age", "db_mxid_age"},
		{pgversion.ViewStatDatabaseConflicts, "confl_lock", "db_conflicts_lock_total"},
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

// TestValidateProjection checks the three typos that would otherwise surface as a
// Postgres syntax error: a column name that is not an identifier, a FROM clause
// with no schema (R2.5's whole point), and an expression that calls a function
// without one. The third is D20's S10 — the check covered the first two and not the
// one field a collector author types by hand, so `age(datfrozenxid)` passed.
func TestValidateProjection(t *testing.T) {
	if err := ValidateProjection(Projection{
		Query:   "SELECT a, b FROM pg_catalog.pg_database",
		Columns: []string{"a", "b"},
	}); err != nil {
		t.Errorf("valid projection rejected: %v", err)
	}

	if err := ValidateProjection(Projection{
		Query:   "SELECT a FROM pg_database",
		Columns: []string{"a"},
	}); err == nil {
		t.Error("unqualified FROM accepted, want error (R2.5)")
	}
	if err := ValidateProjection(Projection{
		Query:   "SELECT a FROM pg_catalog.pg_database",
		Columns: []string{"a", "bad-name"},
	}); err == nil {
		t.Error("column name with a hyphen accepted, want error")
	}
	if err := ValidateProjection(Projection{
		Query:   "SELECT a FROM pg_catalog.pg_database",
		Columns: []string{"a", ""},
	}); err == nil {
		t.Error("empty column name accepted, want error")
	}
	// The extension's own schema is the exception R2.5 names, so it validates too.
	if err := ValidateProjection(Projection{
		Query:   "SELECT a FROM pg_stat_statements.pg_stat_statements",
		Columns: []string{"a"},
	}); err != nil {
		t.Errorf("pg_stat_statements schema rejected: %v", err)
	}

	// D20's S10, the gap itself: Extra.Expr is the one field in the package a
	// collector author types by hand, and it used to be the one field not checked,
	// so an unqualified call there passed while the same text in a query string
	// would not.
	if err := ValidateProjection(Projection{
		Query:   "SELECT a, age(datfrozenxid) AS x FROM pg_catalog.pg_database",
		Columns: []string{"a", "x"},
		Extras:  []Extra{{Expr: "age(datfrozenxid)", Alias: "x"}},
	}); err == nil {
		t.Error(`Extra{Expr: "age(datfrozenxid)"} accepted, want error (R2.5: schema-qualify every catalog reference)`)
	}
	// The Where clause is checked by the same rule, so a hand-written filter cannot
	// smuggle an unqualified call past R2.5 either.
	if err := ValidateProjection(Projection{
		Query:   "SELECT a FROM pg_catalog.pg_database WHERE has_database_privilege(oid, 'CONNECT')",
		Columns: []string{"a"},
		Where:   "has_database_privilege(oid, 'CONNECT')",
	}); err == nil {
		t.Error("Where clause with an unqualified call accepted, want error (the same rule as an Extra)")
	}
	// A schema outside knownSchemas is not one Postgres has for our purposes. The
	// check is a membership test now rather than two hardcoded strings, so the FROM
	// clause and the expressions consult one answer.
	if err := ValidateProjection(Projection{
		Query:   "SELECT a FROM information_schema.pg_database",
		Columns: []string{"a"},
	}); err == nil {
		t.Error("FROM a schema outside knownSchemas accepted, want error")
	}
}

// TestUnqualifiedCall is the expression checker on its own, because
// ValidateProjection's verdict on an Extra or a Where clause is entirely this
// function's answer and the shapes it has to get right are not obvious: a call with
// spaces before the paren, a parenthesised expression with no callee, a qualified
// call whose schema is not one we know, and a call with a string-literal argument
// (the second argument of has_database_privilege(oid, 'CONNECT') is not a call).
func TestUnqualifiedCall(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"pg_catalog.age(datfrozenxid)", ""},
		{"pg_catalog.db_size_bytes(oid)", ""},
		{"pg_catalog.has_database_privilege(oid, 'CONNECT')", ""},
		{"age(datfrozenxid)", "age"},
		{"db_size_bytes(oid)", "db_size_bytes"},
		{"f (x)", "f"}, // spaces between the callee and its "("
		{"other_schema.f(x)", "other_schema.f"},
		{"1 + 2", ""},
		{"(a)", ""},              // a grouped expression, not a call
		{"()", ""},               // no callee at all
		{"", ""},                 // empty: no call, and ValidateProjection accepts an empty Where
		{"a + b(1) * c(d)", "b"}, // the first bad one wins, left to right
		{"pg_catalog.f(x) + g(y)", "g"},
		{"pg_catalog.f(x) + other.g(y)", "other.g"},
		{"7up(x)", "7up"}, // a name starting with a digit is not a valid identifier
	}
	for _, tc := range cases {
		if got := unqualifiedCall(tc.expr); got != tc.want {
			t.Errorf("unqualifiedCall(%q) = %q, want %q", tc.expr, got, tc.want)
		}
	}
}
