package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/query"
)

// openMem opens an in-memory store with a fixed clock, and closes it with the
// test. Every test in this package goes through here so the clock is one value
// and the retention math is deterministic.
func openMem(t *testing.T, now time.Time) *Store {
	t.Helper()
	s, err := Open(Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestOpenMemory covers §3.4:219's profile: the store opens with no file, and
// its retention window is the spec's 24 h rather than the file profile's 7 d.
//
// The 24 h matters beyond bookkeeping: R3.14:318 asks the store to answer
// week-over-week, and a 24 h store cannot. RetentionWindow is how a caller
// learns that, so it can answer not-available instead of a zero that asserts the
// query was fast last week.
func TestOpenMemory(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 34, 56, 0, time.UTC)

	// Subtests, not a bare loop with Fatalf: a Fatalf in a loop body aborts the
	// whole table, so the first bad spelling hides every spelling after it. The
	// first version of this test failed on "" and never ran the other three.
	for _, path := range []string{"", ":memory:", "memory", "memory:", "MEMORY:", "mem"} {
		name := path
		if name == "" {
			name = "(empty)"
		}
		t.Run(name, func(t *testing.T) {
			s, err := Open(Config{Path: path, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatalf("Open(%q): %v", path, err)
			}
			defer s.Close()

			if !s.IsMemory() {
				t.Errorf("Open(%q).IsMemory() = false, want true", path)
			}
			if got := s.RetentionWindow(); got != MemoryWindow {
				t.Errorf("Open(%q).RetentionWindow() = %v, want %v (§3.4:219's 24 h)",
					path, got, MemoryWindow)
			}
			// The single-connection rule is what makes ":memory:" one database
			// rather than one database per pooled connection.
			if got := s.DB().Stats().MaxOpenConnections; got != 1 {
				t.Errorf("Open(%q) MaxOpenConnections = %d, want 1", path, got)
			}
			// The path the store actually handed the driver. A spelling that
			// reaches the driver as a relative filename creates a database file
			// in the process's working directory — which is how "memory" and
			// "MEMORY:" ended up as two 61 KB files in the source tree — and
			// this assertion is what keeps it from coming back.
			if s.cfg.Path != memoryPath {
				t.Errorf("Open(%q) opened %q, want %q", path, s.cfg.Path, memoryPath)
			}
		})
	}
}

// TestOpenFile checks §3.4:218's profile: a real file, WAL on (§3.4:223), and
// the §3.4:222 retention defaults applied.
func TestOpenFile(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 34, 56, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "pgsi.db")

	s, err := Open(Config{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer s.Close()

	if s.IsMemory() {
		t.Error("IsMemory() = true for a file path")
	}
	if got := s.RetentionWindow(); got != DefaultStatementRetention {
		t.Errorf("RetentionWindow() = %v, want %v (retention.statementSnapshots: 7d, §3.4:222)",
			got, DefaultStatementRetention)
	}
	if mode := s.journalMode; mode != "wal" {
		t.Errorf("journal_mode = %q, want wal (§3.4:223)", mode)
	}
	// A file store keeps a pool: §3.4:223's "many-reader" half needs more than
	// one connection, unlike the memory profile.
	if got := s.DB().Stats().MaxOpenConnections; got != DefaultFileConns {
		t.Errorf("MaxOpenConnections = %d, want %d (DefaultFileConns; 0 would mean an unbounded pool)",
			got, DefaultFileConns)
	}
	// The explicit retention set from §3.4:222, not just the statement one.
	if got := s.cfg.PlanRetention; got != DefaultPlanRetention {
		t.Errorf("PlanRetention = %v, want %v", got, DefaultPlanRetention)
	}
	if got := s.cfg.MaxBytes; got != DefaultMaxFileBytes {
		t.Errorf("MaxBytes = %d, want %d (retention.maxBytes: 1Gi)", got, DefaultMaxFileBytes)
	}
}

// TestOpenFileIsPersistent is the R11.15 property: the PVC outlives the process,
// so a second Open on the same path sees what the first wrote, and migrate's
// IF NOT EXISTS makes the second Open a no-op rather than an error.
func TestOpenFileIsPersistent(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 34, 56, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "pgsi.db")

	s1, err := Open(Config{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const raw = "SELECT 1"
	n := query.Normalize(raw, query.Options{Length: query.DefaultQueryStringLength})
	if _, err := s1.DB().Exec(
		`INSERT INTO query (fingerprint, template, display_text, first_seen_text, truncated, text_bytes, first_seen, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.Fingerprint, n.Template, n.DisplayText,
		firstSeenText(raw, query.DefaultQueryStringLength), boolInt(n.Truncated),
		len(n.Template), unix(now), unix(now)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(Config{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("second Open (migrate on an existing schema): %v", err)
	}
	defer s2.Close()

	var (
		template    string
		displayText string
	)
	err = s2.DB().QueryRow(
		`SELECT template, display_text FROM query WHERE fingerprint = ?`, n.Fingerprint,
	).Scan(&template, &displayText)
	if err != nil {
		t.Fatalf("row written before reopen is gone: %v", err)
	}
	if template != n.Template {
		t.Errorf("template = %q, want %q", template, n.Template)
	}
	// The column the DTO's Query field reads, checked on the reopen path rather
	// than only at insert: if it were derived at read time instead of stored,
	// this is the assertion that would notice.
	if displayText != n.DisplayText {
		t.Errorf("display_text = %q, want %q", displayText, n.DisplayText)
	}
}

// TestFingerprintShape pins the key's shape. The store validates it on every
// write, and R8.2's path segment and R9.5b's label both take the string
// verbatim, so a row keyed on anything else is a row nothing can ask for.
func TestFingerprintShape(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"normalizer output", query.Fingerprint("SELECT $1"), true},
		{"empty", "", false},
		{"too short", "0123456789", false},
		{"uppercase hex", "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", false},
		{"64 with a non-hex byte", "g123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false},
		{"63 hex", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde", false},
		{"65 hex", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0", false},
	}
	for _, tc := range cases {
		if got := validFingerprint(tc.in); got != tc.want {
			t.Errorf("%s: validFingerprint(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestUnixRoundTrip covers the store's time encoding, including the zero-time
// sentinel that keeps a NOT NULL column from storing 0001-01-01 as a plausible
// timestamp.
func TestUnixRoundTrip(t *testing.T) {
	cases := []struct {
		in   time.Time
		want int64
	}{
		{time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), 1795392000},
		// Sub-second precision is dropped on purpose: every stored timestamp is
		// a minute bucket or a capture second.
		{time.Date(2026, 11, 23, 12, 34, 56, 789123456, time.UTC), 1795437296},
		{time.Time{}, 0},
	}
	for _, tc := range cases {
		if got := unix(tc.in); got != tc.want {
			t.Errorf("unix(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
	if got := timeFromUnix(0); !got.IsZero() {
		t.Errorf("timeFromUnix(0) = %v, want the zero time", got)
	}
	if got := timeFromUnix(1795437296); got.Format(time.RFC3339) != "2026-11-23T12:34:56Z" {
		t.Errorf("timeFromUnix(1795437296) = %v, want 2026-11-23T12:34:56Z UTC", got)
	}
	// The inverse property the DTO depends on: a 0 decodes to the zero time,
	// which encoding/json omits, rather than rendering as year 1.
	if got := timeFromUnix(0).Format(time.RFC3339); got != "0001-01-01T00:00:00Z" {
		t.Logf("zero time formats as %s (omitted by omitempty)", got)
	}
}

// TestMinuteBucket is R3.8:312's "minute-boundary aggregation".
func TestMinuteBucket(t *testing.T) {
	cases := []struct {
		in   time.Time
		want int64
	}{
		{time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC), 1795435200},
		{time.Date(2026, 11, 23, 12, 0, 59, 999000000, time.UTC), 1795435200},
		{time.Date(2026, 11, 23, 12, 1, 0, 0, time.UTC), 1795435260},
		// A capture at 12:00:58 ingested at 12:01:04 belongs to 12:00: the
		// bucket comes from the source clock, not the ingest clock.
		{time.Date(2026, 11, 23, 12, 0, 58, 0, time.UTC), 1795435200},
	}
	for _, tc := range cases {
		if got := minuteBucket(tc.in); got != tc.want {
			t.Errorf("minuteBucket(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestConfigDSN checks the pragma plumbing. The pragmas are DSN parameters
// because a file store keeps a pool and a post-open Exec configures one
// connection while the rest of the pool keeps the driver's defaults.
func TestConfigDSN(t *testing.T) {
	file := Config{Path: "/var/lib/pgsi/store.db"}.withDefaults()
	dsn := file.dsn()
	for _, want := range []string{
		"/var/lib/pgsi/store.db",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=busy_timeout(5000)",
	} {
		if !contains(dsn, want) {
			t.Errorf("file dsn %q missing %q", dsn, want)
		}
	}

	// Every memory spelling must reach the driver as memoryPath, not as the
	// spelling itself: "memory?_pragma=…" is a *file* named "memory" with query
	// params bolted on, which is how two stray .db files once appeared in the
	// source directory.
	for _, path := range []string{"", ":memory:", "memory", "memory:", "MEMORY:", "mem"} {
		// Not `if dsn := Config{...}.dsn(); ...` — a composite literal in an if
		// header needs parentheses, and the statement form is clearer anyway.
		got := Config{Path: path}.withDefaults().dsn()
		if !strings.HasPrefix(got, memoryPath+"?") {
			t.Errorf("Config{Path: %q}.dsn() = %q, want it to start with %q", path, got, memoryPath+"?")
		}
	}

	// An in-memory database has no WAL to enter, so the journal pragma is
	// dropped rather than requested and declined.
	mem := Config{Path: ":memory:"}.withDefaults()
	if contains(mem.dsn(), "journal_mode") {
		t.Errorf("memory dsn %q requests journal_mode(WAL), which SQLite cannot honour for :memory:", mem.dsn())
	}
	if !contains(mem.dsn(), "foreign_keys") {
		t.Errorf("memory dsn %q missing foreign_keys", mem.dsn())
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// TestNormalizerDefault pins the R3.6 default the store normalizes at when the
// config says nothing: 1 024 bytes, Cloud SQL Enterprise's exact value.
func TestNormalizerDefault(t *testing.T) {
	got := Config{Path: "/tmp/x.db"}.withDefaults().Normalizer
	if got.Length != query.DefaultQueryStringLength {
		t.Errorf("Normalizer.Length = %d, want %d (R3.6's default)", got.Length, query.DefaultQueryStringLength)
	}
	explicit := Config{Path: "/tmp/x.db", Normalizer: query.Options{Length: 64}}.withDefaults()
	if explicit.Normalizer.Length != 64 {
		t.Errorf("an explicit Normalizer.Length was overwritten: %d", explicit.Normalizer.Length)
	}
}
