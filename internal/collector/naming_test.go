package collector

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// familyNameRE is Prometheus's family-name grammar: [a-zA-Z_:][a-zA-Z0-9_:]*.
// The collector-side names are all lowercase (the catalogue's are too, and R9.2's
// prefix is lowercase), so this is the stricter lowercase form — a name with a
// capital in it is a name two collectors will eventually spell two ways.
var familyNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// TestMetricNameTable pins the names the four existing collectors export.
//
// The table is a table and not a formula — the doc comment on exportNames gives
// the four counter-examples that rule a formula out — so the only way to hold it
// is to read rows back out of it. The cases below are chosen for the shapes, not
// for coverage: the four aggregate members R9.4 names in prose, the succession
// pairs whose whole purpose is to stop a PG16 and a PG17 cluster exporting two
// families for one counter, and one row per view so a view with no rows at all
// fails here rather than silently.
func TestMetricNameTable(t *testing.T) {
	cases := []struct {
		view   pgversion.View
		column string
		want   string
	}{
		// R9.4's aggregate family, quoted verbatim from the requirement:
		// "execution time, rows, shared block hits/reads, lock time".
		{pgversion.ViewStatStatements, "total_exec_time", "query_execution_time_seconds_total"},
		{pgversion.ViewStatStatements, "rows", "query_rows_total"},
		{pgversion.ViewStatStatements, "shared_blks_hit", "query_shared_blocks_hit_total"},
		{pgversion.ViewStatStatements, "shared_blks_read", "query_shared_blocks_read_total"},
		{pgversion.ViewStatStatements, "lock_time", "query_lock_time_seconds_total"},

		// The succession pairs R2.4 enumerates. Naming the pair once each is the
		// entire point of Canonical: without it, a PG16 cluster exports
		// query_shared_blk_read_time from blk_read_time and a PG17 cluster
		// exports a second family from shared_blk_read_time.
		{pgversion.ViewStatStatements, "total_time", "query_execution_time_seconds_total"},
		{pgversion.ViewStatStatements, "blk_read_time", "query_shared_blk_read_time_seconds_total"},
		{pgversion.ViewStatStatements, "blk_write_time", "query_shared_blk_write_time_seconds_total"},

		// R9.7's system family, one row per view the four collectors read.
		{pgversion.ViewStatDatabase, "xact_commit", "db_xact_commit_total"},
		{pgversion.ViewStatDatabase, "tup_returned", "db_tuples_returned_total"},
		// The two pg_stat_activity families the catalogue publishes. These are in
		// the list because the name is *not* derivable — the fallback formula gives
		// pg_stat_activity_backends_by_state, and promql_catalogue.go:229 publishes
		// pgsi_db_backends_by_state — and because pg_stat_database's numbackends row
		// promises they "live with that view". A case here is the only thing that
		// checks the promise against the function.
		{pgversion.ViewStatActivity, "backends_by_state", "db_backends_by_state"},
		{pgversion.ViewStatActivity, "backends_by_application_name", "db_backends_by_application_name"},
		// There is no third case. backends_by_wait_event was here as
		// db_backends_by_wait_event until A3's S50 ruled R4.3's rollup is the
		// Wait Events page's payload and not a family; the row is now
		// TypeNotExported with an empty name, so MetricName has nothing to
		// return for it and a case here would assert the empty string.
		{pgversion.ViewDatabase, "size", "db_size_bytes"},
		{pgversion.ViewStatWAL, "wal_bytes", "wal_generated_bytes_total"},
		{pgversion.ViewStatCheckpointer, "num_timed", "checkpoints_timed_total"},
		{pgversion.ViewFileSettings, "count", "file_settings_count"},
	}
	for _, tc := range cases {
		if got := MetricName(tc.view, tc.column); got != tc.want {
			t.Errorf("MetricName(%s, %q) = %q, want %q", tc.view, tc.column, got, tc.want)
		}
	}
}

// TestMetricNameFallsBackToThePreStep4Formula covers the branch that keeps the
// table from being a hard dependency.
//
// A (view, column) with no row returns `view + "_" + column` — the exact string
// sql.go's one-line placeholder produced through Phase 2 steps 1-3. That is what
// lets the table cover eight views while nine collectors exist: an unlisted pair
// still gets a legal name, and the gap is a documentation gap (R14.7) rather than
// a collector that stops exporting.
func TestMetricNameFallsBackToThePreStep4Formula(t *testing.T) {
	// A column on a view the table does cover, but a name the table does not list:
	// pg_database's `acl`, which is text and has no row.
	if got, want := MetricName(pgversion.ViewDatabase, "acl"), "pg_database_acl"; got != want {
		t.Errorf("MetricName(pg_database, acl) = %q, want %q", got, want)
	}
	// A whole view the table does not cover yet (locks.go's pg_locks is not one of
	// the eight).
	if got, want := MetricName(pgversion.ViewLocks, "count"), "pg_locks_count"; got != want {
		t.Errorf("MetricName(pg_locks, count) = %q, want %q", got, want)
	}
}

// TestExportedMetricNamesAreWellFormed checks every name in the table against
// Prometheus's family grammar and R9.2's prefix rule.
//
// The prefix half is the one worth a test: R9.2 makes `metrics.prefix`
// configurable, so a collector that baked `pgsi_` into Sample.Metric would make
// that config key a lie. clock.go's Sample.Metric doc is the normative statement
// ("pg_stat_database_xact_commit", not "pgsi_pg_stat_database_xact_commit") and
// this is the assertion that enforces it across all 114 rows at once.
func TestExportedMetricNamesAreWellFormed(t *testing.T) {
	names := ExportedMetricNames()
	if len(names) == 0 {
		t.Fatal("ExportedMetricNames() is empty")
	}
	for _, n := range names {
		if !familyNameRE.MatchString(n) {
			t.Errorf("name %q does not match %s", n, familyNameRE)
		}
		if strings.HasPrefix(n, DefaultPrefix) {
			t.Errorf("name %q bakes in the R9.2 prefix %q (the renderer applies it; the base must not)", n, DefaultPrefix)
		}
	}
}

// TestCounterNamesEndInTotal is the promtool rule, which Phase 2's acceptance
// runs.
//
// `promtool check metrics` rejects a counter whose name does not end in `_total`.
// The rule is checkable from the table alone — MetricTypeOf says counter, the
// name says whether it complies — and a single non-compliant family fails the
// check for the whole exposition, so one bad row is a failed acceptance for every
// good one.
func TestCounterNamesEndInTotal(t *testing.T) {
	for key, e := range exportNames {
		if e.typ != TypeCounter || e.name == "" {
			continue
		}
		if !strings.HasSuffix(e.name, "_total") {
			t.Errorf("%s -> %q is a counter without the _total suffix (promtool rejects it)",
				key, e.name)
		}
	}
}

// TestEveryNameHasAProvenance answers R14.7 for the whole table at once.
//
// R14.7 forbids a metric that exists in code but not in docs, and exportEntry.src
// is the field that carries the answer. Asserting it is non-empty on every row is
// the cheap version of the check; the row-level strings are what a reviewer
// actually reads.
func TestEveryNameHasAProvenance(t *testing.T) {
	for key, e := range exportNames {
		if strings.TrimSpace(e.src) == "" {
			t.Errorf("%s -> %q has no src (R14.7 needs a document to point at)", key, e.name)
		}
	}
}

// TestSharedNamesAreSuccessionOnly is the cardinality guard.
//
// Two different (view, column) pairs sharing one family name is legitimate in
// exactly one situation: they are the same logical metric under two spellings,
// which is what the succession table exists to enumerate. Any other collision —
// pg_stat_database.blks_hit and pg_stat_wal.wal_bytes both called `db_total`, say
// — silently merges two series in Prometheus and loses one value, which is the
// failure R9.2's prefix requirement exists to prevent, one level down.
//
// The check is that every key carrying a shared name CanonicalKey()s to the same
// place. That is the definition of "the same logical metric" this package uses, so
// the test and the naming function cannot drift apart.
func TestSharedNamesAreSuccessionOnly(t *testing.T) {
	byName := map[string][]string{}
	for key, e := range exportNames {
		if e.name == "" {
			continue
		}
		byName[e.name] = append(byName[e.name], key)
	}

	for name, keys := range byName {
		if len(keys) < 2 {
			continue
		}
		want := CanonicalKey(keys[0])
		for _, key := range keys[1:] {
			if got := CanonicalKey(key); got != want {
				t.Errorf("%q is shared by %s and %s, which Canonical() says are different metrics (%s vs %s): a real collision, not a rename pair",
					name, keys[0], key, want, got)
			}
		}
	}
}

// TestCanonicalWalksTheSuccessionTable checks the backwards walk against the
// table it is built from, for every non-split row.
func TestCanonicalWalksTheSuccessionTable(t *testing.T) {
	rows := pgversion.Successions()
	if len(rows) == 0 {
		t.Fatal("pgversion.Successions() is empty")
	}
	checked := 0
	for _, row := range rows {
		if row.Kind == pgversion.KindSplit {
			// A split's To members are deliberately not indexed backwards: three
			// successors sharing one pre-image would give three values one name,
			// which TestSharedNamesAreSuccessionOnly would (correctly) reject.
			continue
		}
		head := Canonical(row.From.View, row.From.Column)
		for _, to := range row.To {
			if got := Canonical(to.View, to.Column); got != head {
				t.Errorf("Canonical(%s.%s) = %s.%s, want %s.%s (the head of the chain through %s)",
					to.View, to.Column, got.View, got.Column, head.View, head.Column, row.Kind)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Error("no non-split succession rows to check")
	}
}

// TestCanonicalIsStableUnderTableOrder is the property predecessorsBySource's
// doc comment promises for ambiguous pairs: the answer must not change when the
// table is reordered. A naming function whose output depends on slice order is a
// name that two builds disagree about.
func TestCanonicalIsStableUnderTableOrder(t *testing.T) {
	// Every key the table mentions, resolved twice. The map is built once at
	// package init, so this is a cheap re-read rather than a rebuild — the real
	// protection is that predecessorsBySource is a map keyed by Source, and Go
	// gives a map no iteration order, so the build cannot depend on it.
	var keys []string
	for key := range exportNames {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		first := CanonicalKey(key)
		for i := 0; i < 3; i++ {
			if got := CanonicalKey(key); got != first {
				t.Fatalf("CanonicalKey(%s) is not stable: %s then %s", key, first, got)
			}
		}
	}
}

// TestMetricTypeOf covers the three answers the renderer can get.
func TestMetricTypeOf(t *testing.T) {
	cases := []struct {
		view   pgversion.View
		column string
		want   MetricType
		ok     bool
	}{
		{pgversion.ViewStatStatements, "total_exec_time", TypeCounter, true},
		{pgversion.ViewStatDatabase, "xact_commit", TypeCounter, true},
		{pgversion.ViewDatabase, "size", TypeGauge, true},
		{pgversion.ViewStatWAL, "wal_bytes", TypeCounter, true},
		// A pair with no row has no type either: MetricName still names it (the
		// fallback) but the renderer cannot be told a type for a family the table
		// does not know, and inventing one is how a text column becomes a counter.
		{pgversion.ViewDatabase, "acl", TypeNotExported, false},
	}
	for _, tc := range cases {
		got, ok := MetricTypeOf(tc.view, tc.column)
		if ok != tc.ok {
			t.Errorf("MetricTypeOf(%s, %s) ok = %v, want %v", tc.view, tc.column, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("MetricTypeOf(%s, %s) = %q, want %q", tc.view, tc.column, got, tc.want)
		}
	}
}

// TestExportedMetricNamesIsSortedAndDeduped checks the list's own shape, since
// its use is to be diffed against a document and a list with a duplicate in it
// diffs badly.
func TestExportedMetricNamesIsSortedAndDeduped(t *testing.T) {
	names := ExportedMetricNames()
	if !sort.StringsAreSorted(names) {
		t.Error("ExportedMetricNames() is not sorted")
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate name %q", n)
		}
		seen[n] = true
	}
	// R9.12's pgsi_up has no (view, column) — it is a per-instance health gauge —
	// so it is not in exportNames and ExportedMetricNames has to add it. Without
	// this the list the docs are diffed against would omit a metric the chart has
	// two alerting rules for.
	if !seen[UpMetricBase] {
		t.Errorf("ExportedMetricNames() omits %q (R9.12's family, added outside the table)", UpMetricBase)
	}
}
