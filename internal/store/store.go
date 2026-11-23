// Package store is the SQLite query store: §3.4's storage layer.
//
// The read side it serves today is R3.2's Queries table (topn.go, satisfying
// api.QueryListSource). R3.8's detail, R8.11's batched series, R3.7's rollups and
// the SQLite half of collector.Sink that A1's sink.go:26-29 assigns to this commit
// are the files that follow it in this directory, named here as following rather
// than claimed.
//
// The file list is part of the sentence on purpose (A3's S32). The first version
// of this doc said "the read side of every Phase 3 endpoint" while the only
// methods on *Store were the Open plumbing and the PutSnapshot write path, and
// the first criterion in §3.3's set ("Top-N table matches a manual SELECT within
// tolerance") was unexecutable from the store side until topn.go existed. A
// package doc that ships ahead of its code is what teaches a reader to stop
// reading package docs, so this one names the file that answers each route.
//
// # What §3.4 says lives here, and what does not
//
// requirements.md:218 names the engine: "**SQLite** via `modernc.org/sqlite`
// (pure Go, no CGO)", with the rationale "a single 1 Gi PVC gives 7–30 days of
// history". requirements.md:220 enumerates the contents — "Statement snapshots
// (per interval), normalized query text + fingerprints, plan samples,
// active-session snapshots, collector errors, discovery history" — and
// requirements.md:221 states the negative half, which is the constraint that
// shapes the whole schema:
//
//	Raw metric samples — those live in Prometheus; we serve them from our
//	registry, not from SQLite | Avoids rebuilding a TSDB. This is why R8.3
//	cannot answer arbitrary PromQL from the store
//
// So this package stores *derived, keyed, windowed* rows — the fingerprint-keyed
// statement store R3.4 defines — and not a sample archive. The one place a raw
// sample set is kept is sink_current (sink.go), and it is kept the way a cache
// keeps it: replaced wholesale on every publish, with no time axis, which is
// exactly the distinction :221 draws. A1's sink.go says as much: "This commit
// ships the memory half; the SQLite half is Phase 3's own commit and the
// scheduler's call site does not move when it lands."
//
// # The two storage profiles are one code path
//
// requirements.md:219 offers `--store=memory` as the alternative to the PVC:
// "(ring buffers, 24 h window, no PVC)". Config.Path selects between them
// without a second implementation: a file path opens the §3.4:218 profile and
// ":memory:" opens the §3.4:219 one, both through this package.
//
// That is not only a wiring convenience — it changes what the store is allowed to
// claim. R3.14 (requirements.md:318) requires the store to "answer
// week-over-week 'is this query slower than last week' from it", and a 24 h
// window cannot answer a 7 d question. The answer must then be
// *not-available*, not zero: a zero says "it was fast last week", which is a
// different claim from "I have no last week", and collapsing the two is the
// R16.8 class of bug on the store's side. RetentionWindow is the exported
// surface that lets a caller tell them apart, and every read path in this
// package derives its window from it rather than assuming the 7 d default.
//
// # Two size ceilings, deliberately not merged
//
// requirements.md:222's `retention.maxBytes: 1Gi` bounds the SQLite *file*
// (R11.15's RWO PVC), and R13.5's `--max-store-bytes` bounds the *in-memory*
// store — two different resources that need two different numbers, which is why
// A1's collector.DefaultMaxStoreBytes is 64 MiB (sized against R13.2's RSS
// baseline) while this package's default is 1 GiB (sized against the PVC).
// Merging them into one knob would make a memory-profile deployment inherit a
// 1 GiB heap or a file deployment inherit a 64 MiB file. Config.MaxBytes is the
// §3.4:222 number; A1's flag keeps its own.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/query"

	// The driver registers itself as "sqlite". modernc's pure-Go build is the
	// point of §3.4:218's "(pure Go, no CGO)" — a CGO driver would make the
	// static single-binary packaging R14.1 depends on need a C toolchain.
	_ "modernc.org/sqlite"
)

// The defaults §3.4:222 commits to, as named constants rather than inline
// literals, for the same reason R13.5's flag defaults are named in collector:
// a config layer (R11.8) has to be able to name the default it is overriding.
const (
	// DefaultStatementRetention is `retention.statementSnapshots: 7d`. R3.14
	// (requirements.md:318) is the requirement it serves, and requirements.md:568
	// is why 7 d and not 30 d is the default: "7 d is Enterprise's number and
	// fits a 1 Gi PVC; 30 d is one config change".
	DefaultStatementRetention = 7 * 24 * time.Hour

	// DefaultPlanRetention is `retention.plans: 7d` (§3.4:222), the window
	// R3.10's plan samples and R3.11's plan-change events are kept for.
	DefaultPlanRetention = 7 * 24 * time.Hour

	// DefaultMaxFileBytes is `retention.maxBytes: 1Gi` (§3.4:222) — the PVC
	// ceiling from R11.15, and the number §3.4:218's "a single 1 Gi PVC gives
	// 7–30 days of history" promise is measured against.
	DefaultMaxFileBytes int64 = 1 << 30

	// DefaultFileConns bounds the connection pool for a file store.
	// database/sql's default is unlimited, which is not a neutral default
	// against SQLite: §3.4:223's "single-writer" half means every writer
	// contends one lock, so an unbounded pool turns a burst of API readers into
	// goroutines parked on busy_timeout. Four serves §3.4:223's "many-reader"
	// half (R8.2's endpoints are the readers, in one process per R13.2's
	// baseline) while keeping the pool's cost bounded. The memory profile does
	// not use it — see Open's SetMaxOpenConns(1).
	DefaultFileConns = 4

	// MemoryWindow is §3.4:219's "24 h window" for the --store=memory profile.
	// It is a constant, not a configurable, because the spec states it as a
	// property of the profile: the memory profile is a 24 h ring, and a
	// `retention.statementSnapshots: 7d` set alongside --store=memory is a
	// contradiction the spec resolves in favour of the 24 h.
	MemoryWindow = 24 * time.Hour
)

// Config configures one Store.
//
// Every field has a default, so `store.Open(store.Config{Path: p})` is a valid
// configuration for both §3.4 profiles and applies the §3.4:222 retention set.
type Config struct {
	// Path is the SQLite file (the §3.4:218 profile, R11.15's RWO PVC) or
	// ":memory" / "memory" (the §3.4:219 profile). Empty means ":memory:" —
	// defaulting to a file path would create a file nobody asked for, whereas
	// defaulting to memory matches a process that was given no PVC.
	Path string

	// StatementRetention is `retention.statementSnapshots` (R3.14). Zero means
	// DefaultStatementRetention for a file store, and MemoryWindow for an
	// in-memory one — see RetentionWindow for why the two differ.
	StatementRetention time.Duration

	// PlanRetention is `retention.plans`. Zero means DefaultPlanRetention.
	PlanRetention time.Duration

	// MaxBytes is `retention.maxBytes`. Zero means DefaultMaxFileBytes.
	// Purge evicts the oldest rollup rows once the store exceeds it (§3.4:222's
	// "with size-based eviction").
	MaxBytes int64

	// RecordClientAddress is R3.13's `--record-client-address`
	// (requirements.md:317). Off — the default — the store writes no
	// client_address dimension rows, because R3.13 makes recording the client
	// address opt-in rather than on-by-default.
	RecordClientAddress bool

	// Normalizer is R3.5/R3.6's normalization configuration, carried here so the
	// store is the one place a statement is normalized. R3.6's
	// `queryStringLength` is a config value (R11.8's flag), and the fingerprint
	// must be computed with the same length the API renders with — a store that
	// normalized at its own default and an API that rendered at the configured
	// value would disagree about what a fingerprint's display text is.
	Normalizer query.Options

	// Now is the store's clock. Every stored timestamp is UTC (R10.21's
	// "RFC 3339 UTC like every timestamp the API emits" starts at the store),
	// and it is injectable for the same reason collector/clock.go's is and
	// internal/api's parseSeriesQuery's is: retention and bucket math has to be
	// testable to the second without sleeping.
	Now func() time.Time
}

// Store is an open query store.
//
// Concurrency is §3.4:223's "Single-writer, many-reader; WAL mode": one
// connection writes, the pool serves readers. The Go type is safe for concurrent
// use because database/sql is — there is no mutex here and adding one would
// serialize the many-reader half of that contract behind a single Go lock.
type Store struct {
	db  *sql.DB
	cfg Config

	// journalMode is the `PRAGMA journal_mode` the database actually reported.
	// It is read back at Open rather than trusted from the pragma string
	// because §3.4:223's WAL requirement is only satisfied if the database
	// entered WAL, and SQLite silently declines it on filesystems that cannot
	// support it (and always declines it for an in-memory database, where the
	// mode is "memory").
	journalMode string
}

// Open opens (creating if needed) the store Config describes.
func Open(cfg Config) (*Store, error) {
	cfg = cfg.withDefaults()

	db, err := sql.Open("sqlite", cfg.dsn())
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", cfg.Path, err)
	}

	// An in-memory SQLite database is per-connection, not per-connection-pool:
	// two connections to ":memory:" are two distinct, empty databases. Without
	// this, a pool of two gives a store where a row written on one connection
	// is invisible from the other — which reproduces as "the top-N is empty
	// half the time" rather than as an error, so it is set here unconditionally
	// for the memory profile rather than left to the caller.
	if cfg.isMemory() {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(DefaultFileConns)
	}

	s := &Store{db: db, cfg: cfg}

	// The pragmas go through the DSN (see dsn) so every pooled connection gets
	// them, not just the one that happened to execute the first statement.
	// Reading journal_mode back is the one that needs an explicit query: it is
	// the only pragma whose failure is silent.
	mode, err := s.pragma("journal_mode")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: read journal_mode on %s: %w", cfg.Path, err)
	}
	s.journalMode = mode

	// §3.4:223 requires WAL, and a file database that did not enter it is not
	// the storage layer the spec describes: it would serialize readers against
	// the writer and pass every functional test while doing so. An in-memory
	// database reports "memory" — SQLite has no WAL for in-memory databases —
	// and that is the spec's own alternative profile, not a failure.
	if !cfg.isMemory() && !strings.EqualFold(mode, "wal") {
		_ = db.Close()
		return nil, fmt.Errorf("store: %s opened with journal_mode=%q, want wal (§3.4:223 requires WAL mode)",
			cfg.Path, mode)
	}

	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

// DB exposes the pooled handle for tests and for a future health check that
// wants to measure the file (R13.5's size enforcement) without adding a method
// per measurement. It is not the API's surface: nothing in internal/api should
// grow to use it.
func (s *Store) DB() *sql.DB { return s.db }

// RetentionWindow is how far back this store can honestly answer a historical
// question, and it is the field that keeps R3.14's week-over-week answer honest.
//
// A file store answers `retention.statementSnapshots` (7 d by default, and
// 30 d when the R5.8 knob is turned — requirements.md:568's "30 d is one config
// change"). An in-memory store answers 24 h, because §3.4:219 says so, and
// therefore cannot answer R3.14 at all: the caller that asks "was this slower
// last week" must render *not-available* rather than a zero, since a zero
// asserts it was fast.
func (s *Store) RetentionWindow() time.Duration {
	return s.cfg.StatementRetention
}

// IsMemory reports whether this is §3.4:219's --store=memory profile.
func (s *Store) IsMemory() bool { return s.cfg.isMemory() }

// withDefaults applies the zero-means-default rule in one place, the way
// query.Options.withDefaults and collector's config chain do, so Open and every
// test that builds a Config agree on what the defaults are.
func (cfg Config) withDefaults() Config {
	// The path is normalized before anything else reads it, so a caller that
	// wrote "MEMORY:" and a caller that wrote ":memory:" end up with the same
	// DSN. That matters more than tidiness: the driver's own parser only
	// recognises the ":memory:" spelling, so every other spelling of the
	// §3.4:219 profile would otherwise open a *file* named "memory" in the
	// working directory — a store that answers reads correctly for the
	// connection that wrote to it and is empty for every other one.
	if cfg.isMemory() {
		cfg.Path = memoryPath
	}
	if cfg.Normalizer.Length == 0 {
		cfg.Normalizer = query.Options{Length: query.DefaultQueryStringLength}
	}
	if cfg.StatementRetention == 0 {
		// The memory profile's window is the spec's 24 h, not the file
		// profile's 7 d: §3.4:219 states the window as part of the profile.
		if cfg.isMemory() {
			cfg.StatementRetention = MemoryWindow
		} else {
			cfg.StatementRetention = DefaultStatementRetention
		}
	}
	if cfg.PlanRetention == 0 {
		cfg.PlanRetention = DefaultPlanRetention
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultMaxFileBytes
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return cfg
}

// memoryPath is the canonical spelling of the §3.4:219 profile's path.
const memoryPath = ":memory:"

// isMemory recognises every spelling the flag can take for the memory profile.
// R13.5's --store flag takes a value and the chart passes it through verbatim,
// so "memory", "memory:", ":memory:" and "" all mean the same profile; a
// spelling test is cheaper than a store that silently opened a file named
// "memory" in the working directory.
func (cfg Config) isMemory() bool {
	switch strings.ToLower(strings.TrimSpace(cfg.Path)) {
	// "" is in the set because an unset Path is the memory profile: defaulting
	// it to a file path would create a database nobody asked for, and the
	// alternative — a file named "" — is what the first version of this switch
	// produced, which the driver opens as a file called
	// "?_pragma=journal_mode(WAL)&…" in the working directory.
	case "", memoryPath, "memory", "memory:", "mem", "file::memory:", "file::memory":
		return true
	default:
		return false
	}
}

// dsn builds the driver connection string.
//
// The pragmas are DSN parameters rather than post-open Exec calls because
// modernc applies them to *every* connection it opens, and a file store keeps a
// pool open (§3.4:223's many-reader half): a pragma Executed once configures one
// connection and leaves the rest of the pool on the driver's defaults —
// journal_mode is persistent in the file so WAL survives, but busy_timeout and
// synchronous are per-connection, and the pool's other connections would
// silently have none.
func (cfg Config) dsn() string {
	pragmas := "?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)"
	if cfg.isMemory() {
		// An in-memory database cannot enter WAL (SQLite has no WAL for :memory:),
		// so the journal pragma is dropped for it and Open accepts the "memory"
		// mode it reports instead. synchronous and foreign_keys still apply.
		pragmas = "?_pragma=synchronous(NORMAL)" +
			"&_pragma=foreign_keys(1)" +
			"&_pragma=busy_timeout(5000)"
	}
	return cfg.Path + pragmas
}

// pragma reads one pragma back. journal_mode is a query (it returns a row), so
// this uses QueryRow rather than Exec for all of them.
func (s *Store) pragma(name string) (string, error) {
	var v string
	if err := s.db.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// now is the single clock every write path in this package uses, UTC-normalized
// (R10.21). Nothing else in the package calls time.Now directly, so a test that
// sets Config.Now controls every timestamp the store produces.
func (s *Store) now() time.Time { return s.cfg.Now().UTC() }

// unix encodes a time as the store stores it: epoch seconds, UTC, with the zero
// time as 0.
//
// Seconds, not milliseconds and not a string: every timestamp in the store is a
// bucket boundary (R3.8's "minute-boundary aggregation") or a capture instant,
// and R10.21's RFC 3339 rendering is the API's job, not the storage layer's.
// 0 for the zero time keeps a NOT NULL column from storing the sentinel as a
// real date (0001-01-01 renders as a plausible-looking timestamp in a UI).
func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

// timeFromUnix is unix's inverse: 0 decodes to the zero time, which the DTOs
// then omit (R8.4's omitempty) rather than render as year 1.
func timeFromUnix(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// minuteBucket is R3.8's "minute-boundary aggregation": the bucket a capture
// belongs to, as the store stores it.
//
// Truncation is on the UTC minute, and the bucket is derived from the *source*
// clock (collector.Sample.SourceTime, R13.11's first timestamp) rather than the
// ingest clock: a sample captured at 12:00:58 and ingested at 12:01:04 belongs
// to 12:00, and bucketing it at 12:01 would move load between minutes based on
// how loaded the collector was.
func minuteBucket(t time.Time) int64 { return unix(t.Truncate(time.Minute)) }

// firstSeenText is R3.4's "the first-seen raw text truncated per R3.6": the raw
// statement as the collector read it, cut to the same Options.Length cap the
// display text gets.
//
// The normalizer returns DisplayText (the *template* cut to the cap) but not the
// raw text's truncated form, and the schema has both columns because
// dto.QueryView.Query renders the former while R3.4 requires storing the latter.
// The cut mirrors query's own truncate — bytes, walking back off a multi-byte
// rune's continuation bytes — so the two truncations agree on what "cut to
// 1 024 bytes" means instead of one splitting a rune the other one doesn't.
func firstSeenText(raw string, length int) string {
	if length <= 0 || len(raw) <= length {
		return raw
	}
	cut := length
	for cut > 0 && raw[cut]&0xC0 == 0x80 {
		cut--
	}
	return raw[:cut]
}

// boolInt encodes a Go bool as the store's INTEGER 0/1 — SQLite has no boolean
// type — so every boolean column is written the same way rather than one table
// using 0/1 and another using 'true'/'false'.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// validFingerprint reports whether s is the shape R3.4's key has: 64 lowercase
// hex characters, the output of query.Fingerprint.
//
// The store validates it on every write because a malformed fingerprint is
// otherwise invisible until the API 404s on it: R8.2's
// `/insights/queries/{fingerprint}` path segment and R9.5b's `fingerprint`
// label both take the string verbatim, so a row keyed on "abc" is a row nothing
// can ask for. Catching it at the write boundary is also what stops one
// collector's home-grown hash from silently forking the query store in two.
func validFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// ErrUnknownFingerprint is what the read paths return for a fingerprint the
// store has never seen.
//
// It exists so "no such query" is distinguishable from "the query exists and its
// window is empty" — the same distinction R8.4's envelope draws for a whole
// response (data:[] plus meta.total:0 is not an error), and the one
// dto.LatencyView's pointer percentiles make at field level. A handler that
// cannot tell the two apart answers a 404 with a 200-and-nothing or vice versa,
// which is the R16.8 class of bug on the route's side.
//
// It wraps api.ErrQueryDetailNotFound rather than being a second unrelated
// sentinel (A3's S35), because that is the value the handler answers 404 on:
// query_detail.go's doc says errors.Is must match "over wrapping [so that] a
// Phase 3 store [can] add context ('no row for 550e8400… in default/pg1')
// without changing what the client sees". A store error that did not wrap it
// would make the handler answer 500 to a 404 question. Wrapping keeps one
// vocabulary — callers inside this package match on ErrUnknownFingerprint and
// inherit api's message, and callers outside it match on the api name.
var ErrUnknownFingerprint = fmt.Errorf("store: unknown fingerprint: %w", api.ErrQueryDetailNotFound)

// ErrMalformedFingerprint is the write-side sibling of ErrUnknownFingerprint:
// the string in hand is not a fingerprint at all (R3.4's key shape — see
// validFingerprint), as opposed to a well-formed one the store has no row for.
//
// Before the split, statements.go used ErrUnknownFingerprint for both, which
// collapses exactly the two states the doc above exists to separate: a caller
// reading "unknown fingerprint" back from PutSnapshot cannot tell "this
// collector is hashing wrong" from "you asked about a query that aged out".
// A3's S35; the split costs one var and makes both messages true.
var ErrMalformedFingerprint = errors.New("store: malformed fingerprint")

// whereBuilder assembles the WHERE clause every read path in this package
// shares.
//
// Each read is the same shape — a fixed FROM plus a set of optional equality
// filters — and the alternative to a builder is the same `if ns != "" { sql +=
// " AND namespace = ?"; args = append(...) }` chain written five times, which is
// where a missing AND, or an args slice that has fallen out of step with the
// placeholders, lives. The seeded `WHERE 1=1` is what lets the first condition be
// appended without knowing whether any condition exists: an all-empty filter set
// yields `WHERE 1=1`, valid SQL meaning "everything", whereas emitting `WHERE`
// and then nothing is a syntax error, and prefixing the first clause conditionally
// is the branch the seed removes.
//
// It is a type rather than a `buildWhere(cfg…) func` because the read paths each
// apply a different subset of their DTO's filters (topn.go applies db,
// clusterLoadSQL deliberately does not), so the set is per-call, not per-store.
type whereBuilder struct {
	clause []string
	args   []any
}

// newWhere returns an empty builder, seeded so any number of conditions —
// including zero — produces valid SQL.
func newWhere() *whereBuilder {
	return &whereBuilder{clause: []string{"WHERE 1=1"}}
}

// raw adds one condition with its own placeholders and arguments, for the
// conditions that are not a bare `column = ?`: `r.bucket >= ? AND r.bucket <= ?`
// is one fragment with two args, and `r.bucket_kind = 'minute'` is one with none.
func (w *whereBuilder) raw(fragment string, args ...any) {
	w.clause = append(w.clause, "AND "+fragment)
	w.args = append(w.args, args...)
}

// eq adds `AND <column> = ?` and its argument.
func (w *whereBuilder) eq(column, value string) {
	w.raw(column+" = ?", value)
}

// in adds `AND <column> IN (?, ?, …)`.
//
// An empty list emits `AND 0`, not `IN ()`: SQLite rejects the empty
// parenthesised list outright, and the honest answer to "these no fingerprints"
// is no rows, which is what `AND 0` gives. Dropping the clause instead would
// silently widen the query to every row in the table — the one case where an
// empty filter means "everything" while the caller meant "nothing", and the
// reason api.SeriesQuery's empty Fingerprints needs its own rule (series.go).
func (w *whereBuilder) in(column string, values []string) {
	if len(values) == 0 {
		w.raw("0")
		return
	}
	ph := make([]string, len(values))
	args := make([]any, len(values))
	for i, v := range values {
		ph[i] = "?"
		args[i] = v
	}
	w.raw(column+" IN ("+strings.Join(ph, ", ")+")", args...)
}

// String renders the clause.
func (w *whereBuilder) String() string { return strings.Join(w.clause, " ") }

// Args returns the arguments in placeholder order. The slice is the builder's
// own, not a copy: callers pass it straight to QueryContext and never mutate it.
func (w *whereBuilder) Args() []any { return w.args }
