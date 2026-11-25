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
//
// It seeds the tx from the *round* rather than from the view list, because since
// S119 the collector's queries are not one flat list: the settings group has two
// and the extensions group has two more (the pg_database enumeration and the
// pg_extension probe). Seeding from the round is what keeps this helper honest
// about which SQL the collector will actually issue — a fixture built from a
// hand-written view list would keep compiling even if the collector changed the
// query it runs.
func newTestSettings(t *testing.T, tgt Target, rows map[pgversion.View][]Row) (*SettingsCollector, *fakeOpener, *fakeTx) {
	t.Helper()
	rnd, err := NewSettingsCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{}}
	for _, pv := range roundProjections(rnd) {
		tx.rows[pv.Query] = rows[pv.view]
	}
	opener := &fakeOpener{tx: tx}
	return NewSettingsCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }}), opener, tx
}

// roundProjections flattens a settingsRound into the order Collect runs its
// queries: the settings group's two, then the extensions group's enumeration and
// probe.
//
// It exists because the round is a struct with three named fields and the tests
// want one ordered list; the ordering rule lives here (one place) rather than in
// each test that needs it, so a test cannot silently assert a different order than
// the collector uses.
func roundProjections(rnd settingsRound) []viewProjection {
	return append(append([]viewProjection{}, rnd.settings...), rnd.enumeration, rnd.probe)
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

// dbRow builds a pg_database row for the extension probe's enumeration.
//
// The two booleans are the only columns S119's fan-out reads (plus datname), so
// they are the two a caller names; everything else the matrix has on the view gets
// fullRow's "x". The settings collector mints no samples from these rows —
// database.go owns the pg_database_* families — so this helper exists to feed the
// enumeration, not to feed a metric.
func dbRow(t *testing.T, name, allowconn, istemplate string) Row {
	t.Helper()
	return fullRow(t, pgversion.ViewDatabase, map[string]string{
		"datname":       name,
		"datallowconn":  allowconn,
		"datistemplate": istemplate,
	})
}

// testRows is the one fixture the collect-path tests share: three GUC rows (two
// numeric, one string), three file-settings rows (two applied, one erroring), two
// extensions, and the two databases every Postgres instance has.
//
// The database rows are what S119 added to the fixture. They are postgres
// (connectable) and template0 (not): the first is the configured database, so the
// fan-out loop skips it and the probe still costs one transaction, which is why
// every pre-S119 count assertion in this file survives unchanged. The second is
// there so the "template0 yields no row" rule has a fixture to be true about.
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
		pgversion.ViewDatabase: {
			dbRow(t, "postgres", "t", "f"),
			dbRow(t, "template0", "f", "t"),
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
	// Two entries, not one. R2.7's rule is one read-only transaction per *view
	// group*, and since S119 the collector has two groups because a Postgres
	// transaction belongs to exactly one database while pg_settings does not: the
	// cluster-wide views and the per-database extension probe cannot share a
	// transaction. The precedent for a second group in one collector is
	// statements.go's `statements` group.
	if len(d.ViewGroups) != 2 || d.ViewGroups[0] != settingsViewGroup || d.ViewGroups[1] != extensionsViewGroup {
		t.Errorf("ViewGroups = %v, want [%s %s] (R2.7: one entry per view group; S119 split the per-database extension probe into its own)",
			d.ViewGroups, settingsViewGroup, extensionsViewGroup)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; the tuning knob is R2.7's statement_timeout)", d.Timeout)
	}
	// All four views exist on every matrix major (SettingsPG13to18,
	// FileSettingsPG13to18, ExtensionPG13to18, and DatabasePG13to14/15/17to18), so
	// the collector applies to all six — and to nothing outside the matrix. Adding
	// pg_database to AppliesTo could only have shrunk that set, and it did not:
	// pg_database has a column set on all six majors too.
	for _, major := range pgversion.Majors() {
		if !d.Applies(major) {
			t.Errorf("Applies(%d) = false, want true (all four views exist on every matrix major)", major)
		}
	}
	for _, major := range []int{12, 19} {
		if d.Applies(major) {
			t.Errorf("Applies(%d) = true, want false", major)
		}
	}
}

// TestSettingsQueriesAreSchemaQualified is R2.5 and R2.4 checked on every major:
// each of the four queries names its schema, and each column list is the matrix's
// for that major rather than a hand-written one.
func TestSettingsQueriesAreSchemaQualified(t *testing.T) {
	for _, major := range pgversion.Majors() {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: major}
		rnd, err := NewSettingsCollector(nil, SQLConfig{}).projections(tgt)
		if err != nil {
			t.Fatalf("PG%d: projections: %v", major, err)
		}
		projections := roundProjections(rnd)
		if len(projections) != len(settingsViews)+len(extensionViews) {
			t.Fatalf("PG%d: %d projections, want %d", major, len(projections), len(settingsViews)+len(extensionViews))
		}
		for _, pv := range projections {
			want := " FROM " + pgversion.Schema(pv.view) + "." + string(pv.view)
			// Containment, not suffix, for the same reason database_test.go's
			// equivalent switched: a view with a WHERE clause (pg_database, D20's S7)
			// no longer ends at the table name. The suffix check still runs where it is
			// the right shape, and the WHERE clause gets its own.
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
			// The one spec this collector owns: pg_database's, and it must be
			// database.go's verbatim. The docblock claims the reuse; this is the check
			// that makes the claim cost something to break.
			if pv.view == pgversion.ViewDatabase && pv.Where != databaseSpecs[pgversion.ViewDatabase].Where {
				t.Errorf("PG%d %s: WHERE = %q, want database.go's %q (a second copy of D20's S7 filter is a second place to fix it)",
					major, pv.view, pv.Where, databaseSpecs[pgversion.ViewDatabase].Where)
			}
			cols, _ := pgversion.Columns(pv.view, major)
			for _, col := range cols {
				if !strings.Contains(pv.Query, col) {
					t.Errorf("PG%d %s: query does not select %s (R2.4 says the matrix's column set is the projection)",
						major, pv.view, col)
				}
			}
			// Every projection carries the database its SQL runs in, and before S119
			// there was nowhere to carry it: the field is the producer R3.1's
			// `database` cell was missing.
			if pv.database != DefaultDatabase {
				t.Errorf("PG%d %s: projection database = %q, want %q (SQLConfig.Database, defaulted by withDefaults)",
					major, pv.view, pv.database, DefaultDatabase)
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
	// file-settings rows, which collapse to 3 samples; 2 extension rows. The
	// pg_database rows contribute nothing (database.go owns those families), and
	// template0 is filtered out of the fan-out, so there is exactly one probe
	// transaction and two extension samples.
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
		labels map[string]string
	}{
		// §3.3's row names these three gauges; two are here, and the naming rule
		// is what makes them queryable by name at all.
		{"pg_settings_max_connections", 100, nil},
		{"pg_settings_shared_buffers", 131072, nil},
		// The file-settings counts: three lines, two applied, one error.
		{"file_settings_count", 3, nil},
		{"file_settings_applied_count", 2, nil},
		{"file_settings_error_count", 1, nil},
		// Extensions are presence, valued 1, and now carry the database they were
		// installed in — the half S119 was filed over.
		{"pg_extension_postgis", 1, map[string]string{"datname": "postgres"}},
		{"pg_extension_pg_stat_statements", 1, map[string]string{"datname": "postgres"}},
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
		// R9.3's contract, per family. The GUC and file-settings families carry no
		// labels at all — no slot for a GUC name or a unit (the shared_buffers row's
		// "8kB" is deliberately not one), and the identity half is the scheduler's.
		// The extension families carry exactly one: datname, which R9.3 gives to
		// every series that belongs to a database.
		want := tc.labels
		if want == nil {
			want = map[string]string{}
		}
		if len(s.Labels) != len(want) {
			t.Errorf("%s labels = %v, want %v (R9.3's closed set: no unit label on a settings family; datname on an extension series; identity labels are the scheduler's)",
				tc.metric, s.Labels, want)
		}
		for k, v := range want {
			if s.Labels[k] != v {
				t.Errorf("%s labels[%s] = %q, want %q", tc.metric, k, s.Labels[k], v)
			}
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
	// The pg_database rows the round read for the enumeration mint no families here:
	// database.go owns pg_database_*, and a second collector exporting them would be
	// two writers on one series.
	for _, s := range res.Samples {
		if strings.HasPrefix(s.Metric, "pg_database_") {
			t.Errorf("sample %s exists, want the pg_database families owned by database.go (this collector reads pg_database for its rows, not for its metrics)", s.Metric)
		}
	}
}

// TestSettingsCollectOneTransactionPerGroup is R2.7 as a count: one Begin per view
// group, one query per view in each, one Commit per group, and the config that
// reached the opener carrying R2.7's statement_timeout and R2.8's application_name.
//
// It used to be "PerRound", because the collector had one group and R2.7's rule
// made the two words mean the same thing. Since S119 they do not: the extensions
// group is one transaction per connectable database, so a round's Begin count is
// 1 + N and the assertion has to name the group it is counting.
func TestSettingsCollectOneTransactionPerGroup(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	c, opener, tx := newTestSettings(t, tgt, testRows(t))

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Two Begins: the settings group's, and the extensions group's for the one
	// connectable database in the fixture (postgres; template0 is filtered).
	if opener.calls != 2 {
		t.Errorf("Begin calls = %d, want 2 (R2.7: one read-only transaction per view group, and this collector has two groups; the fixture has one connectable database, so the extensions group fans out once)", opener.calls)
	}
	projections := roundProjections(mustRound(t, c, tgt))
	if len(tx.queries) != len(projections) {
		t.Fatalf("queries = %d, want %d (one per view)\n%v", len(tx.queries), len(projections), tx.queries)
	}
	for i, q := range tx.queries {
		if q != projections[i].Query {
			t.Errorf("query %d =\n%s\nwant\n%s", i, q, projections[i].Query)
		}
	}
	if tx.commits != 2 || tx.rollbacks != 0 {
		t.Errorf("commits/rollbacks = %d/%d, want 2/0 on the success path (one commit per group)", tx.commits, tx.rollbacks)
	}
	if res.RoundTrips != len(projections) {
		t.Errorf("RoundTrips = %d, want %d (R13.1 measures a round from this count)", res.RoundTrips, len(projections))
	}

	// Both Begins on this round connect to postgres: the fixture's only other
	// database is template0, which cannot be opened. Asserting the config's database
	// — not just the query count — is the only way to see that the second transaction
	// is a *different database's* transaction and not a repeat of the first.
	for i, want := range []string{"postgres", "postgres"} {
		if opener.cfgs[i].Database != want {
			t.Errorf("Begin %d cfg.Database = %q, want %q", i, opener.cfgs[i].Database, want)
		}
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

// mustRound rebuilds a collector's round for a test that already has the collector
// but needs the projection list to assert against.
func mustRound(t *testing.T, c *SettingsCollector, tgt Target) settingsRound {
	t.Helper()
	rnd, err := c.projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	return rnd
}

// TestSettingsCollectQueryErrorKeepsRoundTrips is the Collector contract's
// "on error, the returned Result must still carry the RoundTrips it actually
// issued", for the settings collector's second view.
func TestSettingsCollectQueryErrorKeepsRoundTrips(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	rnd := mustRound(t, NewSettingsCollector(nil, SQLConfig{}), tgt)

	tx := &fakeTx{
		rows:   map[string][]Row{rnd.settings[0].Query: {gucRow(t, "max_connections", "100", "")}},
		failOn: rnd.settings[1].Query,
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
	// The group is in the message too: with two groups there are two Begin calls and
	// two query loops, and "collector settings: query" cannot say which failed.
	if !strings.Contains(err.Error(), string(pgversion.ViewFileSettings)) {
		t.Errorf("error %q does not name the failing view", err)
	}
	if !strings.Contains(err.Error(), "group "+settingsViewGroup) {
		t.Errorf("error %q does not name the failing group", err)
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
	// The settings group's two queries ran; the extensions group never happened,
	// because a round that cannot close its first transaction does not open a second.
	if res.RoundTrips != len(settingsViews) {
		t.Errorf("RoundTrips = %d, want %d (the settings group's queries did run)", res.RoundTrips, len(settingsViews))
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

// TestSettingsExtensionProbeFansOut is S119 on the metric plane: a pod with three
// databases — one connectable and configured, one connectable and not, one not
// connectable at all — reports the extensions of the two it can open, and labels
// each sample with the database it came from.
//
// The fixture's limitation is worth naming: fakeTx answers by *query text*, and the
// fan-out varies the *config*, not the query — so both probes see the same two
// extensions. That is what makes this the right test for the fix rather than a
// weaker one. The same extname appearing twice with two different datname values is
// only possible if the label came from the projection, which is exactly the producer
// R3.1's `database` cell was missing; a test that gave each database its own
// extension list would also pass if the label were read out of the row.
func TestSettingsExtensionProbeFansOut(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	rnd, err := NewSettingsCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	tx := &fakeTx{rows: map[string][]Row{
		rnd.settings[0].Query: {gucRow(t, "max_connections", "100", "")},
		rnd.settings[1].Query: nil,
		rnd.enumeration.Query: {
			dbRow(t, "postgres", "t", "f"),
			dbRow(t, "appdb", "t", "f"),
			dbRow(t, "template0", "f", "t"),
		},
		rnd.probe.Query: {extRow(t, "pg_stat_statements", "1.10"), extRow(t, "postgis", "1.1.1")},
	}}
	opener := &fakeOpener{tx: tx}
	c := NewSettingsCollector(opener, SQLConfig{Now: func() time.Time { return fixedClock }})

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Three transactions: the settings group's, the extensions group's for postgres
	// (which carries the enumeration), and the extensions group's for appdb.
	// template0 is not connected to at all.
	if opener.calls != 3 {
		t.Fatalf("Begin calls = %d, want 3 (settings, extensions/postgres, extensions/appdb)\n%+v", opener.calls, opener.cfgs)
	}
	gotDatabases := make([]string, 0, len(opener.cfgs))
	for _, cfg := range opener.cfgs {
		gotDatabases = append(gotDatabases, cfg.Database)
	}
	wantDatabases := []string{"postgres", "postgres", "appdb"}
	for i, want := range wantDatabases {
		if gotDatabases[i] != want {
			t.Fatalf("Begin %d cfg.Database = %q, want %q (got %v)", i, gotDatabases[i], want, gotDatabases)
		}
	}

	// Two extensions in each of two databases: four presence samples, and the
	// datname label is what tells them apart.
	byMetric := map[string][]Sample{}
	for _, s := range res.Samples {
		byMetric[s.Metric] = append(byMetric[s.Metric], s)
	}
	for _, ext := range []string{"pg_extension_postgis", "pg_extension_pg_stat_statements"} {
		got := byMetric[ext]
		if len(got) != 2 {
			t.Errorf("%s: %d samples, want 2 (one per connectable database)\n%s", ext, len(got), dumpSamples(got))
			continue
		}
		seen := map[string]bool{}
		for _, s := range got {
			seen[s.Labels["datname"]] = true
		}
		for _, want := range []string{"postgres", "appdb"} {
			if !seen[want] {
				t.Errorf("%s: no sample with datname=%q (got %v)", ext, want, seen)
			}
		}
	}

	// template0 is in the enumeration and cannot be opened, so it must not appear as
	// a datname anywhere: R3.1's "datallowconn = false databases and template0 cannot
	// be probed and yield no row".
	for _, s := range res.Samples {
		if s.Labels["datname"] == "template0" {
			t.Errorf("sample %s has datname=template0, want it never probed (datallowconn = false)", s.Metric)
		}
	}
}

// TestRunTxRejectsProjectionFromAnotherDatabase is the pairing invariant runTx
// enforces: a projection whose SQL is built for one database cannot be run in a
// transaction opened on another.
//
// The bug it gates is the one the fan-out would have shipped with. The probe
// projection is built once, from the collector's own SQLConfig.Database, and then run
// in every connectable database; passing it unchanged into a transaction opened on
// appdb labels appdb's extensions as postgres's — a wrong answer with no symptom,
// because the label is the only place the answer lives.
func TestRunTxRejectsProjectionFromAnotherDatabase(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	rnd, err := NewSettingsCollector(nil, SQLConfig{}).projections(tgt)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	opener := &fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}
	c := NewSettingsCollector(opener, SQLConfig{})

	cfg := SQLConfig{}
	cfg.Database = "appdb"

	// The projection still says postgres; the transaction opens on appdb.
	_, err = c.runTx(context.Background(), tgt, cfg, extensionsViewGroup, []viewProjection{rnd.probe}, fixedClock)
	if err == nil {
		t.Fatal("runTx = nil error, want the pairing refusal")
	}
	if !strings.Contains(err.Error(), "appdb") || !strings.Contains(err.Error(), DefaultDatabase) {
		t.Errorf("error %q does not name both databases", err)
	}
	if opener.calls != 0 {
		t.Errorf("Begin calls = %d, want 0 (the check is pre-flight, so a mismatch costs no connection)", opener.calls)
	}

	// The same projection, re-paired, is accepted — which is what Collect does in the
	// fan-out loop and what makes the check a guard rather than a ban.
	if _, err := c.runTx(context.Background(), tgt, cfg, extensionsViewGroup, []viewProjection{rnd.probe.in("appdb")}, fixedClock); err != nil {
		t.Errorf("runTx with the projection re-paired to appdb: %v", err)
	}
}

// TestProbedDatabases checks the enumeration rules directly, including the shapes
// the SQL filter cannot produce but a driver can: a NULL datname, and a
// datallowconn column that is absent rather than false.
func TestProbedDatabases(t *testing.T) {
	cases := []struct {
		name string
		rows []Row
		want []string
	}{
		{"empty", nil, nil},
		{
			"the configured database is in the list",
			[]Row{dbRow(t, "postgres", "t", "f")},
			[]string{"postgres"},
		},
		{
			"template0 is skipped even when connectable",
			[]Row{dbRow(t, "postgres", "t", "f"), dbRow(t, "template0", "t", "t")},
			[]string{"postgres"},
		},
		{
			"datallowconn=false is skipped",
			[]Row{dbRow(t, "appdb", "f", "f"), dbRow(t, "reporting", "t", "f")},
			[]string{"reporting"},
		},
		{
			"an empty datname names no database",
			[]Row{dbRow(t, "", "t", "f"), dbRow(t, "appdb", "t", "f")},
			[]string{"appdb"},
		},
	}
	for _, tc := range cases {
		got := probedDatabases(tc.rows)
		if len(got) != len(tc.want) {
			t.Errorf("%s: probedDatabases = %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: probedDatabases = %v, want %v", tc.name, got, tc.want)
				break
			}
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
