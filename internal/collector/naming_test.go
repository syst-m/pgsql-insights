package collector

import (
	"regexp"
	"slices"
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

// TestMetricNameTable pins the names the SQL collectors export.
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

		// R9.7's system family. The sentence this one replaces scoped itself to
		// "the four collectors", and activity.go's rows landed without it being
		// updated — the off-by-one the docblock at naming.go:140-149 documents, and the
		// reason a count in prose needs a checker. io.go now adds a fifth SQL
		// collector and two more view keys; TestExportNameViewKeyCount owns the
		// numbers, and this list has a case for ten of the sixteen, including both
		// of the two io.go resolves and both of the two bloat.go adds.
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
		// io.go's two views. The pg_stat_io cases are the shape the table's own
		// canonical example forces: op_bytes is R2.4's 1→3 split pre-image
		// (succession.go:132-148), and a split is the one succession Canonical
		// refuses to walk backwards, so the pre-image and each of its three PG18
		// successors carry their own name. One case for the pre-image and one for one
		// successor is the minimum that fails if someone collapses them into a shared
		// `io_bytes_total`.
		{pgversion.ViewStatIO, "reads", "io_reads_total"},
		{pgversion.ViewStatIO, "op_bytes", "io_op_bytes_total"},
		{pgversion.ViewStatIO, "read_bytes", "io_read_bytes_total"},
		{pgversion.ViewStatIO, "stats_reset", "io_stats_reset_timestamp_seconds"},
		// pg_stat_slru's cases pin the blks → blocks expansion, precedented by
		// query_shared_blocks_hit_total above: the abbreviation is the source's, the
		// expansion is the naming table's.
		{pgversion.ViewStatSLRU, "blks_hit", "slru_blocks_hit_total"},
		{pgversion.ViewStatSLRU, "flushes", "slru_flushes_total"},
		{pgversion.ViewFileSettings, "count", "file_settings_count"},
		// R7.2's bloat families. The two views' 42 rows are ruled names
		// (requirements.md:366); these five are the two names bloat.go's Extras mint
		// (total_bytes, size), the ratio, the counter R7.2's unused-index detection
		// reads, and the ratio's numerator.
		{pgversion.ViewStatUserTables, "n_dead_tup", "table_dead_tuples"},
		{pgversion.ViewStatUserTables, "total_bytes", "table_total_bytes"},
		{pgversion.ViewStatUserTables, "dead_ratio", "table_dead_ratio"},
		{pgversion.ViewStatUserIndexes, "idx_scan", "index_idx_scan_total"},
		{pgversion.ViewStatUserIndexes, "size", "index_size_bytes"},
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
// ("db_xact_commit_total", not "pgsi_db_xact_commit_total") and
// this is the assertion that enforces it across every named row at once.
//
// The sentence used to end "across all 114 rows at once". Four numbers have
// those five words — 114, then 139, then 171, now 213 — and each went stale on the
// commit that added rows, which is the same failure the docblock at
// naming.go:140-149 documents for the view-key count. TestExportNameViewKeyCount
// owns the numbers now, so this paragraph says "every named row" and lets the test be
// the measurement rather than restating one that rots.
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

// TestExportNameViewKeyCount is the checker the docblock on exportNames points at.
//
// That docblock has corrected its own count once (eight, then eleven) without
// correcting the enumeration under it — the paragraph at naming.go:134 records how
// that happened — and a count in prose is only worth anything once something
// asserts it. This asserts the three numbers the docblock states, and asserts the
// set of view names rather than only its size, because the recorded defect was a
// right number over a wrong list.
//
// It is also the test that makes io.go's two views visible to the table: a new
// view's rows landing without a bump here means the rows exist under a view the
// docblock does not name, which is the shape of every past count bug here.
func TestExportNameViewKeyCount(t *testing.T) {
	const (
		// The 16 dotted view keys, the 213 rows under them, and the 51 dotless
		// cnpg/pgbouncer keys the R9.4 amendment let in. The bgwriter →
		// checkpointer pair is two of the 16 keys for one source (R2.4's fourth
		// enumerated case), which is why 16 keys is 15 sources; the pair's own
		// section header carries that argument.
		wantViewKeys = 16
		wantDotted   = 213
		wantDotless  = 51
	)
	wantViews := []string{
		"pg_database", "pg_file_settings", "pg_replication_slots",
		"pg_stat_activity", "pg_stat_archiver", "pg_stat_bgwriter",
		"pg_stat_checkpointer", "pg_stat_database", "pg_stat_database_conflicts",
		"pg_stat_io", "pg_stat_replication", "pg_stat_slru",
		"pg_stat_statements", "pg_stat_user_indexes", "pg_stat_user_tables", "pg_stat_wal",
	}

	got := map[string]int{}
	var dotted, dotless int
	for key := range exportNames {
		if view, _, ok := splitExportKey(key); ok {
			got[view]++
			dotted++
			continue
		}
		dotless++
	}

	var views []string
	for view := range got {
		views = append(views, view)
	}
	sort.Strings(views)
	if len(views) != wantViewKeys {
		t.Errorf("exportNames has %d view keys, want %d\n%s", len(views), wantViewKeys, strings.Join(views, "\n"))
	}
	if !slices.Equal(views, wantViews) {
		t.Errorf("the view keys do not match the set the docblock enumerates\ngot  %v\nwant %v", views, wantViews)
	}
	if dotted != wantDotted {
		t.Errorf("%d dotted rows, want %d (the number naming.go:126 states)", dotted, wantDotted)
	}
	if dotless != wantDotless {
		t.Errorf("%d dotless rows, want %d (the cnpg/pgbouncer population)", dotless, wantDotless)
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

// TestStringKeyAccessorsAgreeWithTheTypedOnes is R9.4's amendment's acceptance test.
//
// The amendment added MetricNameFor and MetricTypeFor as the general-form readers
// and named no test for them, so between b499de6 and here the two functions had
// zero call sites and zero assertions anywhere in the repo — the claim that
// "the renderer needs # TYPE for every family it prints" lived in a docblock and
// nowhere else. The pgbouncer rows added in the same commit are what make the
// test worth writing rather than merely cheap: they are the table's first dotless
// keys, and a dotless key has no typed reader at all, so MetricNameFor is the
// only way anything in the codebase will ever read one of those 51 names back.
//
// What this checks, stated precisely so it is not oversold: for every row it
// asserts MetricNameFor(key) and MetricTypeFor(key) return that row's own
// name and typ. That is the non-tautological half — it fails if either accessor
// reads a different map, applies a transform the other does not, or gets the
// TypeNotExported ok-flag rule wrong. The equivalence to the typed forms is
// tautological by construction (MetricName's body is
// exportNames[Canonical(view, column).String()].name, so for a canonical key the
// two expressions are the same expression) and is asserted anyway, because the
// day MetricName grows a branch the string form lacks, that assertion is what
// notices. The dotless keys are covered by the first half and by nothing else.
func TestStringKeyAccessorsAgreeWithTheTypedOnes(t *testing.T) {
	var canonical, nonCanonical, dotless int
	for key, e := range exportNames {
		if got := MetricNameFor(key); got != e.name {
			t.Errorf("MetricNameFor(%s) = %q, want %q", key, got, e.name)
		}
		typ, ok := MetricTypeFor(key)
		if e.typ == TypeNotExported {
			if ok {
				t.Errorf("MetricTypeFor(%s) reports ok for a TypeNotExported row", key)
			}
			continue
		}
		if !ok {
			t.Errorf("MetricTypeFor(%s) reports not-exported but the row's type is %q", key, e.typ)
			continue
		}
		if typ != e.typ {
			t.Errorf("MetricTypeFor(%s) = %q, want %q", key, typ, e.typ)
		}
		if e.name == "" {
			t.Errorf("%s has type %q but an empty name", key, e.typ)
			continue
		}

		view, column, dotted := splitExportKey(key)
		if !dotted {
			// A dotless key is its own canonical and has no typed reader; the
			// two assertions above are the only path to it.
			dotless++
			continue
		}
		if head := CanonicalKey(key); head != key {
			// Non-canonical: MetricName walks to the head, so it answers with
			// the row filed under head, not with this row.
			if got, want := MetricName(pgversion.View(view), column), MetricNameFor(head); got != want {
				t.Errorf("MetricName(%s, %s) = %q, want %q (the name at the canonical key %s)",
					view, column, got, want, head)
			}
			nonCanonical++
			continue
		}
		if got := MetricName(pgversion.View(view), column); got != e.name {
			t.Errorf("MetricName(%s, %s) = %q, want %q (MetricNameFor(%s) agrees)",
				view, column, got, e.name, key)
		}
		canonical++
	}
	if canonical == 0 || nonCanonical == 0 || dotless == 0 {
		t.Errorf("expected all three key shapes exercised, got %d canonical, %d non-canonical, %d dotless",
			canonical, nonCanonical, dotless)
	}
}
