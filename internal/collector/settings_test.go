package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract for the settings collector, matching
// database_test.go's: the collector is a Collector, checked by the compiler.
var _ Collector = (*SettingsCollector)(nil)

// newTestSettings builds the collector plus the fake opener and tx it runs on,
// serving the given rows for each view's query.
//
// The fakeTx and fakeOpener are database_test.go's, reused rather than
// re-implemented: one fake per interface is the point of putting them in the
// package's test files in the first place, and a second fakeOpener per collector
// is how a package ends up with three subtly different fakes and no shared
// guarantee about what a Begin call records.
func newTestSettings(t *testing.T, tgt Target, rows map[pgversion.View][]Row) (*SettingsCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	projections, err := NewSettingsCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range projections {
		tx.rows[pv.Query] = rows[pv.view]
	}
	opener := &fakeOpener{tx: tx}
	return NewSettingsCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener, tx
}

// fullRow builds a Row for one view with every column the matrix has on PG17
// filled, then applies over.
//
// The default value is "x" — deliberately not a number, so a column a test
// forgets to override is unparseable and produces no sample rather than a sample
// that happens to look right — with the two exceptions the builders need: the
// now() alias gets the fixed timestamp, and callers override the columns they
// care about. database_test.go's rowFor is the same idea for the database
// collector's views.
func fullRow(t *testing.T, view pgversion.View, over map[string]string) Row {
	t.Helper()
	cols, ok := pgversion.Columns(view, 17)
	if !ok {
		t.Fatalf("Columns(%s, 17): not ok", view)
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

func gucRow(t *testing.T, name, setting, unit string) Row {
	t.Helper()
	return fullRow(t, pgversion.ViewSettings, map[string]string{"name": name, "setting": setting, "unit": unit})
}

func fileRow(t *testing.T, name, applied, errmsg string) Row {
	t.Helper()
	return fullRow(t, pgversion.ViewFileSettings, map[string]string{"name": name, "applied": applied, "error": errmsg})
}

func extRow(t *testing.T, name, version string) Row {
	t.Helper()
	return fullRow(t, pgversion.ViewExtension, map[string]string{"extname": name, "extversion": version})
}

// testRows is the one fixture the collect-path tests share: three GUC rows (two
// numeric, one string), three file-settings rows (two applied, one erroring), and
// two extensions.
func testRows(t *testing.T) map[pgversion.View][]Row {
	t.Helper()
	return map[pgversion.View][]Row{
		pgversion.ViewSettings: {
			gucRow(t, "max_connections", "100", ""),
			gucRow(t, "shared_buffers", "131072", "8kB"),
			gucRow(t, "DateStyle", "ISO, MDY", ""),
		},
		pgversion.ViewFileSettings: {
			fileRow(t, "max_connections", "t", ""),
			fileRow(t, "shared_buffers", "t", ""),
			fileRow(t, "work_mem", "f", `invalid value for parameter "work_mem": "9MB"`),
		},
		pgversion.ViewExtension: {
			extRow(t, "pg_stat_statements", "1.10"),
			extRow(t, "postgis", "1.1.1"),
		},
	}
}

func TestSettingsDescriptor(t *testing.T) {
	c, _, _ := newTestSettings(t, tgtPG("pg", "pg-1", "primary"), testRows(t))
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorSettings {
		t.Errorf("Name = %q, want %q (§3.3's row name, and the R2.2 `collector` label)", d.Name, CollectorSettings)
	}
	if d.Interval != SettingsInterval {
		t.Errorf("Interval = %s, want %s (§3.3's 15 min)", d.Interval, SettingsInterval)
	}
	if d.Source != SourceSQL {
		t.Errorf("Source = %s, want %s (§3.3's S2)", d.Source, SourceSQL)
	}
	if len(d.ViewGroups) != 1 || d.ViewGroups[0] != settingsViewGroup {
		t.Errorf("ViewGroups = %v, want [%s] (R2.7: one entry per view group, and this collector has one)",
			d.ViewGroups, settingsViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; the tuning knob is R2.7's statement_timeout)", d.Timeout)
	}
	// All three views exist on every matrix major (SettingsPG13to18,
	// FileSettingsPG13to18, ExtensionPG13to18), so the collector applies to all
	// six — and to nothing outside the matrix.
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

// TestSettingsQueriesAreSchemaQualified is R2.5 and R2.4 checked on every major:
// each of the three queries names its schema, and each column list is the
// matrix's for that major rather than a hand-written one.
func TestSettingsQueriesAreSchemaQualified(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		projections, err := NewSettingsCollector(nil, SQLConfig{}).projections(tgt)
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}
		if len(projections) != len(settingsViews) {
			t.Fatalf("PG%d: %d projections, want %d", major, len(projections), len(settingsViews))
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
		}
	}
}

// TestSettingsCollectSamples is the golden sample set for one round: one family
// per numeric GUC, three counts for the file settings, one family per extension,
// and nothing at all for the string GUC.
func TestSettingsCollectSamples(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, _, _ := newTestSettings(t, tgt, testRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// 3 GUC rows, one of which (DateStyle) has no number and so no sample; 3
	// file-settings rows, which collapse to 3 samples; 2 extension rows.
	if len(res.Samples) != 7 {
		t.Fatalf("len(Samples) = %d, want 7 (2 GUC + 3 file-settings counts + 2 extensions)\n%s",
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
		// §3.3's row names these three gauges; two are here, and the naming rule
		// is what makes them queryable by name at all.
		{"pg_settings_max_connections", 100},
		{"pg_settings_shared_buffers", 131072},
		// The file-settings counts: three lines, two applied, one error.
		{"file_settings_count", 3},
		{"file_settings_applied_count", 2},
		{"file_settings_error_count", 1},
		// Extensions are presence, valued 1.
		{"pg_extension_postgis", 1},
		{"pg_extension_pg_stat_statements", 1},
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
		// R9.3's contract has no slot for a GUC name or a unit, and the identity
		// half is the scheduler's, so a settings sample carries no labels — even
		// the shared_buffers row, whose unit ("8kB") is deliberately not one.
		if len(s.Labels) != 0 {
			t.Errorf("%s labels = %v, want none (R9.3's closed set has no unit label; identity labels are the scheduler's)",
				tc.metric, s.Labels)
		}
	}

	// The string GUC produces no sample: R9.1 puts text on the API plane, and a
	// sample whose value is a string is a sample whose number is a lie.
	if _, ok := byMetric["pg_settings_datestyle"]; ok {
		t.Error("pg_settings_datestyle exists, want no sample for a string GUC (R9.1)")
	}
	if _, ok := byMetric["pg_settings_setting"]; ok {
		t.Error("pg_settings_setting exists, want one family per GUC rather than one per column")
	}
}

// TestSettingsCollectOneTransactionPerRound is R2.7 as a count: one Begin, one
// query per view, one Commit, and the config that reached the opener carrying
// R2.7's statement_timeout and R2.8's application_name.
func TestSettingsCollectOneTransactionPerRound(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, opener, tx := newTestSettings(t, tgt, testRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if opener.calls != 1 {
		t.Errorf("Begin calls = %d, want 1 (R2.7: one read-only transaction per view group, and this collector has one group)", opener.calls)
	}
	projections, _ := c.projections(tgt)
	if len(tx.queries) != len(settingsViews) {
		t.Fatalf("queries = %d, want %d (one per view)", len(tx.queries), len(settingsViews))
	}
	for i, q := range tx.queries {
		if q != projections[i].Query {
			t.Errorf("query %d =\n%s\nwant\n%s", i, q, projections[i].Query)
		}
	}
	if tx.commits != 1 || tx.rollbacks != 0 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/0 on the success path", tx.commits, tx.rollbacks)
	}
	if res.RoundTrips != len(settingsViews) {
		t.Errorf("RoundTrips = %d, want %d (R13.1 measures a round from this count)", res.RoundTrips, len(settingsViews))
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

// TestSettingsCollectQueryErrorKeepsRoundTrips is the Collector contract's
// "on error, the returned Result must still carry the RoundTrips it actually
// issued", for the settings collector's second view.
func TestSettingsCollectQueryErrorKeepsRoundTrips(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	projections, _ := NewSettingsCollector(nil, SQLConfig{}).projections(tgt)

	tx := &fakeTx{
		rows:   map[string][]Row{projections[0].Query: {gucRow(t, "max_connections", "100", "")}},
		failOn: projections[1].Query,
	}
	opener := &fakeOpener{tx: tx}
	c := NewSettingsCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

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
	if !strings.Contains(err.Error(), string(pgversion.ViewFileSettings)) {
		t.Errorf("error %q does not name the failing view", err)
	}
}

func TestSettingsCollectCommitError(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, _, tx := newTestSettings(t, tgt, testRows(t))
	tx.commitErr = errors.New("fake: connection lost")

	res, err := c.Collect(context.Background(), tgt)
	if err == nil {
		t.Fatal("Collect = nil error, want the commit failure surfaced")
	}
	if res.RoundTrips != len(settingsViews) {
		t.Errorf("RoundTrips = %d, want %d (the queries did run)", res.RoundTrips, len(settingsViews))
	}
	if len(res.Samples) == 0 {
		t.Error("Samples empty, want the read samples returned alongside the error")
	}
	if tx.commits != 1 || tx.rollbacks != 1 {
		t.Errorf("commits/rollbacks = %d/%d, want 1/1 (a failed COMMIT may leave the transaction open on a pooled connection; the best-effort Rollback is what closes it)",
			tx.commits, tx.rollbacks)
	}
}

func TestSettingsCollectBeginError(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	opener := &fakeOpener{beginErr: errors.New("fake: connect refused")}
	c := NewSettingsCollector(opener, SQLConfig{})

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

func TestSettingsCollectRefusesUnknownMajors(t *testing.T) {
	for _, major := range []int{0, 12, 19} {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		opener := &fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}
		c := NewSettingsCollector(opener, SQLConfig{})

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

// TestFamilyFor is the naming rule for the two views whose families come from row
// data rather than from column names, checked on the spellings Postgres actually
// produces.
func TestFamilyFor(t *testing.T) {
	cases := []struct {
		view    pgversion.View
		subject string
		want    string
	}{
		{pgversion.ViewSettings, "max_connections", "pg_settings_max_connections"},
		{pgversion.ViewSettings, "shared_buffers", "pg_settings_shared_buffers"},
		// The punctuation cases: a Prometheus family name is [a-zA-Z_:][a-zA-Z0-9_:]*,
		// and extension-prefixed GUC names have a dot.
		{pgversion.ViewSettings, "pg_stat_statements.max", "pg_settings_pg_stat_statements_max"},
		{pgversion.ViewSettings, "autovacuum.vacuum_cost_delay", "pg_settings_autovacuum_vacuum_cost_delay"},
		// Case is preserved, not folded: Postgres has mixed-case GUC names
		// (DateStyle, IntervalStyle, TimeZone) and folding them would collide two
		// distinct settings into one family.
		{pgversion.ViewSettings, "DateStyle", "pg_settings_DateStyle"},
		{pgversion.ViewSettings, "TimeZone", "pg_settings_TimeZone"},
		{pgversion.ViewExtension, "postgis", "pg_extension_postgis"},
		{pgversion.ViewExtension, "pg_stat_statements", "pg_extension_pg_stat_statements"},
	}
	for _, tc := range cases {
		if got := familyFor(tc.view, tc.subject); got != tc.want {
			t.Errorf("familyFor(%s, %q) = %q, want %q", tc.view, tc.subject, got, tc.want)
		}
	}
}

// TestSanitizeName checks the character rule directly, including the shapes that
// are not valid in a Prometheus name and would otherwise reach the renderer: a
// space, a dash, a dot, a colon in a position the grammar allows but the rule
// does not need, and an empty subject.
func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"max_connections", "max_connections"},
		{"pg_stat_statements.max", "pg_stat_statements_max"},
		{"ssl_ecdh-curve", "ssl_ecdh_curve"},
		{"search path", "search_path"},
		{"DateStyle", "DateStyle"},
		{"a.b.c-d e", "a_b_c_d_e"},
		{"", ""},
		{"7", "7"},
	}
	for _, tc := range cases {
		if got := sanitizeName(tc.in); got != tc.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestGUCValue covers the three shapes a GUC's `setting` column takes: a number, a
// boolean (which Postgres spells on/off here rather than the t/f it uses for a
// boolean column), and text (which has no number).
func TestGUCValue(t *testing.T) {
	cases := []struct {
		setting string
		want    float64
		ok      bool
	}{
		{"100", 100, true},
		{"131072", 131072, true},
		{"-1", -1, true},
		{"64.0", 64, true},
		// Boolean settings: on/off is the GUC spelling, and R9.3 has no boolean
		// type, so they map to 1/0 the way every other exporter does.
		{"on", 1, true},
		{"off", 0, true},
		// Boolean *column* spellings, accepted by the parseValue delegation so a
		// driver that returns true/false behaves the same way.
		{"t", 1, true},
		{"f", 0, true},
		{"true", 1, true},
		{"false", 0, true},
		// Text: no number, no sample.
		{"ISO, MDY", 0, false},
		{"md5", 0, false},
		{"", 0, false},
		{"8kB", 0, false},
	}
	for _, tc := range cases {
		got, ok := gucValue(tc.setting)
		if ok != tc.ok {
			t.Errorf("gucValue(%q) ok = %v, want %v", tc.setting, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("gucValue(%q) = %v, want %v", tc.setting, got, tc.want)
		}
	}
}

// dumpSamples renders a sample list for a failure message, so a count mismatch
// says what was there rather than only how many.
func dumpSamples(samples []Sample) string {
	var b strings.Builder
	for _, s := range samples {
		b.WriteString("  ")
		b.WriteString(s.Metric)
		b.WriteString(" = ")
		b.WriteString(formatFloat(s.Value))
		b.WriteString("\n")
	}
	return b.String()
}
