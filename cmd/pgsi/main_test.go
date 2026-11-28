package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/kube"
	"github.com/frankellis/pgsql-insights/internal/store"
)

// fakeSQLOpener is a collector.SQLOpener that exists. It never gets called by
// NewRegistry — Registry stores collectors and only Scheduler.Run invokes
// Collect — so its whole job is to be non-nil and correctly typed, which is the
// value newRegistry branches on. The `var _ collector.SQLOpener` line below is
// what makes that a compile-time fact rather than a claim in this comment.
type fakeSQLOpener struct{}

func (fakeSQLOpener) Begin(context.Context, collector.Target, collector.SQLConfig) (collector.Tx, error) {
	return nil, errFakeNoTx
}

// errFakeNoTx documents that this opener is never invoked; a test that hits it is
// a test that started calling collectors, which is not what a registry test does.
var errFakeNoTx = errString("cmd/pgsi: fake opener has no transaction")

// The compile-time check that fakeSQLOpener implements the interface newRegistry's
// opener parameter takes. Without it, a signature drift in collector.SQLOpener
// would not fail here — the fake would simply stop satisfying the parameter type,
// and the compiler would say so at the call site; this line says so at the type.
var _ collector.SQLOpener = fakeSQLOpener{}

// errString keeps the fake dependency-free (no fmt import for one error).
type errString string

func (e errString) Error() string { return string(e) }

// TestNewRegistryShapes pins the two registry shapes to the opener argument, which
// is the whole reason newRegistry has an opener parameter.
//
// The assertion is a sorted name list rather than a count, because a count is the
// weaker instrument: `len == 10` passes if a collector is registered twice and
// one is missing, and NewRegistry itself rejects duplicates, so the only failure
// the count can hide is a wrong name. Naming all ten means a constructor that
// changes its Descriptor.Name, or a collector added without this test being
// updated, fails here.
//
// It is one test with two sub-shapes rather than two tests because the pair is the
// contract: the interesting property is the delta between them, and putting them
// in one function makes the delta the thing a reader compares.
func TestNewRegistryShapes(t *testing.T) {
	const wantPoolerOnly = "pooler"

	wantSQL := []string{
		collector.CollectorActivity,
		collector.CollectorBloat,
		collector.CollectorDatabase,
		collector.CollectorIO,
		collector.CollectorLocks,
		collector.CollectorPooler,
		collector.CollectorPoolerClients,
		collector.CollectorReplication,
		collector.CollectorSettings,
		collector.CollectorStatement,
		collector.CollectorWAL,
	}
	sort.Strings(wantSQL)

	// Shape 1: no opener. The pooler alone — which is the registry the binary had
	// before the driver existed, kept reachable so a process with no clientset
	// (D9's `api` role, or --discover=false) still has the same set it had then.
	t.Run("nil opener", func(t *testing.T) {
		reg, err := newRegistry((*kube.Client)(nil), nil, collector.Overrides{}, collector.SQLConfig{})
		if err != nil {
			t.Fatalf("newRegistry(nil opener): %v", err)
		}
		entries := reg.Enabled()
		if len(entries) != 1 {
			t.Fatalf("len(Enabled()) = %d, want 1:\n %+v", len(entries), entries)
		}
		if entries[0].Desc.Name != wantPoolerOnly {
			t.Errorf("Enabled()[0].Desc.Name = %q, want %q", entries[0].Desc.Name, wantPoolerOnly)
		}
		if entries[0].Desc.Source != collector.SourcePoolerMetrics {
			t.Errorf("Enabled()[0].Desc.Source = %q, want %q (S5)", entries[0].Desc.Source, collector.SourcePoolerMetrics)
		}
	})

	// Shape 2: an opener. All eleven — the nine §3.3 SQL collectors plus the two
	// pooler names, because pooler-clients needs an opener (SHOW CLIENTS is a
	// psql round-trip) and so belongs to this shape rather than to shape 1.
	t.Run("with opener", func(t *testing.T) {
		reg, err := newRegistry((*kube.Client)(nil), fakeSQLOpener{}, collector.Overrides{}, collector.SQLConfig{})
		if err != nil {
			t.Fatalf("newRegistry(fake opener): %v", err)
		}
		entries := reg.Enabled()
		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, e.Desc.Name)
		}
		sort.Strings(got)
		if len(got) != len(wantSQL) {
			t.Fatalf("len(Enabled()) = %d, want %d\ngot  %v\nwant %v", len(got), len(wantSQL), got, wantSQL)
		}
		for i := range got {
			if got[i] != wantSQL[i] {
				t.Errorf("registered names differ at %d: got %v, want %v", i, got, wantSQL)
				break
			}
		}
	})
}

// TestNewRegistryAppliesOverrides pins the R2.3 seam: the Overrides argument is
// plumbed through, so a caller that starts building it from argv gets the
// behaviour without changing newRegistry's signature.
//
// Both cases run against the opener-less registry on purpose. An interval
// override for "pooler" is the one override that is valid in both shapes, so
// testing it on the one-collector registry isolates the override plumbing from the
// registration set that TestNewRegistryShapes covers.
func TestNewRegistryAppliesOverrides(t *testing.T) {
	disabled := collector.Overrides{Disabled: []string{collector.CollectorPooler}}
	reg, err := newRegistry((*kube.Client)(nil), nil, disabled, collector.SQLConfig{})
	if err != nil {
		t.Fatalf("newRegistry(disabled): %v", err)
	}
	if got := reg.Enabled(); len(got) != 0 {
		t.Errorf("Enabled() with pooler disabled = %+v, want empty", got)
	}

	interval := collector.Overrides{Intervals: map[string]time.Duration{
		collector.CollectorPooler: 90 * time.Second,
	}}
	reg, err = newRegistry((*kube.Client)(nil), nil, interval, collector.SQLConfig{})
	if err != nil {
		t.Fatalf("newRegistry(interval override): %v", err)
	}
	entries := reg.Enabled()
	if len(entries) != 1 {
		t.Fatalf("len(Enabled()) = %d, want 1", len(entries))
	}
	if entries[0].Interval != 90*time.Second {
		t.Errorf("Entry.Interval = %v, want 90s (R2.3's --interval.pooler override)", entries[0].Interval)
	}
}

// TestNewRegistryOverrideNamesAnUnregisteredCollector pins the failure mode that
// only exists now that the registry has two shapes. An interval for a SQL
// collector is valid with an opener and an error without one, and registry.go's
// rule is that "a typo is not a no-op". This is the test that says the rule fires
// from newRegistry too, not just from registry.go's own tests.
func TestNewRegistryOverrideNamesAnUnregisteredCollector(t *testing.T) {
	ov := collector.Overrides{Intervals: map[string]time.Duration{
		collector.CollectorWAL: 30 * time.Second,
	}}
	if _, err := newRegistry((*kube.Client)(nil), nil, ov, collector.SQLConfig{}); err == nil {
		t.Error("newRegistry with a wal interval and no opener: err = nil, want an error (wal is not registered without an opener)")
	}
	if _, err := newRegistry((*kube.Client)(nil), fakeSQLOpener{}, ov, collector.SQLConfig{}); err != nil {
		t.Errorf("newRegistry with a wal interval and an opener: err = %v, want nil (wal is registered with an opener)", err)
	}
}

// TestOpenSinkProfiles pins the two shapes --store resolves to, which is the
// half of R11.6's profile pair that cmd/pgsi owns: the memory profile fills two
// of the seven api.Config fields and leaves R9.5's family and the four read
// routes nil, and a file store fills all seven from one handle. The "sqlite"
// profile word itself is not exercised here because it resolves to
// defaultStoreFile, a path that exists only where the chart's PVC is mounted;
// the arm's body is openStoreFile, which the explicit-path subtest covers
// directly.
//
// The four read-side assertions are the point of the file-store subtest rather
// than a rounding of it: the store has implemented QueryList, QuerySeries,
// QueryDetail and Tags since Phase 3, cmd/pgsi has had seven fields to assign
// them to since then, and nothing assigned them, so the routes were documented,
// mounted, tested against a stub, and empty in the binary. Assigning a field
// whose type the compiler checks is the fix; asserting it here is what stops the
// next field added to api.Config from being the next one nobody notices.
func TestOpenSinkProfiles(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		b, err := openSink(defaultStoreProfile, store.Config{})
		if err != nil {
			t.Fatalf("openSink(%q) = %v, want nil", defaultStoreProfile, err)
		}
		if _, ok := b.sink.(*collector.MemSink); !ok {
			t.Errorf("sink = %T, want *collector.MemSink", b.sink)
		}
		if b.storeSize == nil {
			t.Error("storeSize = nil, want the same MemSink the scheduler writes to")
		}
		if b.perQuery != nil {
			t.Errorf("perQuery = %v, want nil (MemSink has no RenderPerQuery, so R9.5's family is absent)", b.perQuery)
		}
		// All four read views are nil here for the same reason perQuery is: a
		// MemSink holds samples, not the normalised rows the routes serve. The
		// assertion is that they are the *plain* nil, not a typed nil — a typed
		// nil in an interface field is not == nil (2b63f1e's caveat), and the four
		// api accessors would then hand a nil *store.Store to a route instead of
		// their documented empty source.
		for _, f := range []struct {
			name string
			got  any
		}{
			{"queries", b.queries},
			{"series", b.series},
			{"detail", b.detail},
			{"tags", b.tags},
		} {
			if f.got != nil {
				t.Errorf("%s = %v, want nil (MemSink implements no read route)", f.name, f.got)
			}
		}
		if b.close != nil {
			t.Error("close is set, want nil (a MemSink owns no handle to close)")
		}
	})

	t.Run("explicit path", func(t *testing.T) {
		b, err := openStoreFile(filepath.Join(t.TempDir(), "store.db"), store.Config{})
		if err != nil {
			t.Fatalf("openStoreFile = %v, want nil", err)
		}
		defer func() {
			if err := b.close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
		if b.sink == nil || b.storeSize == nil || b.perQuery == nil {
			t.Errorf("file profile filled sink=%v storeSize=%v perQuery=%v, want all three non-nil",
				b.sink != nil, b.storeSize != nil, b.perQuery != nil)
		}
		// The four read routes. Each field's type is the api interface, so the
		// compiler already proved *store.Store satisfies it; what this loop pins is
		// that openStoreFile *assigns* it, which is the half that was missing.
		for _, f := range []struct {
			name string
			got  any
		}{
			{"queries", b.queries},
			{"series", b.series},
			{"detail", b.detail},
			{"tags", b.tags},
		} {
			if f.got == nil {
				t.Errorf("%s = nil, want the same *store.Store the sink fields use", f.name)
			}
		}
		if b.close == nil {
			t.Error("close = nil, want *store.Store's Close")
		}
	})

	t.Run("unopenable path fails at startup", func(t *testing.T) {
		if _, err := openStoreFile(filepath.Join(t.TempDir(), "missing", "store.db"), store.Config{}); err == nil {
			t.Error("openStoreFile in a missing directory = nil error, want one (fail at startup, not one round later in Publish)")
		}
	})

	// The R3.13 pair has to reach store.Open, not just reach openStoreFile's
	// signature. Store.cfg is unexported and cmd/pgsi is a different package, so
	// the value can't be read back; what can be observed is that Open validates
	// the tag source (store.go:289, before the file is even opened), so a Config
	// the caller passed and a Config the function built for itself are
	// distinguishable by error. Before the pair existed, openStoreFile built
	// `store.Config{Path: path}` internally and this subtest's bogus value would
	// have been silently dropped — which is the shape this assertion exists to
	// rule out, not a restatement of the two lines above.
	t.Run("the caller's Config is the one store.Open sees", func(t *testing.T) {
		_, err := openStoreFile(filepath.Join(t.TempDir(), "store.db"), store.Config{
			ApplicationTagSource: "application", // R3.12's key name, not R3.13's flag value
		})
		if err == nil {
			t.Fatal("openStoreFile with ApplicationTagSource \"application\" = nil error, want store.Open's rejection")
		}
		if !strings.Contains(err.Error(), "application_tag_source") {
			t.Errorf("error = %v, want the unknown application_tag_source error", err)
		}
	})
}

// TestStoreRecordingFlags pins R3.13's two CLI halves to the store's own answers.
// The bool's default is false because store.go:145-149 documents Off as the
// default ("the store writes no client_address dimension rows"), and a flag
// defaulting to true would make the documented default unreachable from the flag
// that exists to expose it. The string's default is taken from
// store.DefaultApplicationTagSource rather than retyped here, so the flag and
// store.Config cannot disagree about what "no flag given" means — and the value it
// yields is run through store.ValidApplicationTagSource, because a default the
// store rejects is a default configuration that fails at Open. TestTableCoversEveryFlag
// proves the rows exist; this proves the two pointers carry values, which is what
// runServe's dereferences and openSink's Config both read.
func TestStoreRecordingFlags(t *testing.T) {
	sf := newServeFlags(prescan{})
	if err := sf.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if sf.recordClientAddress == nil || sf.applicationTagSource == nil {
		t.Fatalf("recordClientAddress=%v applicationTagSource=%v, want both set by newServeFlags",
			sf.recordClientAddress, sf.applicationTagSource)
	}
	if *sf.recordClientAddress {
		t.Error("--record-client-address defaults to true, want false (R3.13 makes recording opt-in)")
	}
	if got := *sf.applicationTagSource; got != store.DefaultApplicationTagSource {
		t.Errorf("--application-tag-source defaults to %q, want store.DefaultApplicationTagSource's %q",
			got, store.DefaultApplicationTagSource)
	}
	if !store.ValidApplicationTagSource(*sf.applicationTagSource) {
		t.Errorf("flag default %q is not a value store.ValidApplicationTagSource accepts",
			*sf.applicationTagSource)
	}

	// The other shape runServe can get: both set on the command line.
	sf2 := newServeFlags(prescan{})
	if err := sf2.Parse([]string{"--record-client-address", "--application-tag-source=query_comment"}); err != nil {
		t.Fatalf("Parse(--record-client-address --application-tag-source=query_comment): %v", err)
	}
	if !*sf2.recordClientAddress {
		t.Error("--record-client-address did not parse to true")
	}
	if *sf2.applicationTagSource != store.ApplicationTagFromQueryComment {
		t.Errorf("--application-tag-source = %q, want %q",
			*sf2.applicationTagSource, store.ApplicationTagFromQueryComment)
	}
}

// TestScanArgs pins the composed pre-scan that R2.3's cmd/pgsi half and R13.7's
// both ride on: the two dynamic-name families come out of argv, everything else
// survives intact for Parse, and a bad interval is an error here rather than a
// failure two steps later.
//
// The case that matters most is the pair in one argv. logx's scanner and
// collector.IntervalOverridesFromArgs strip disjoint prefixes, so each one's
// output is a valid input for the other, and the only way to catch a regression
// where one scanner starts eating the other's argument is to feed both families
// together — which is exactly what runServe and runConfig now do via scanArgs.
// Testing them separately would leave that untested, and the two-call-sites-drift
// bug this function exists to prevent is precisely a scanner change that one
// command picks up and the other does not.
func TestScanArgs(t *testing.T) {
	t.Run("interval only", func(t *testing.T) {
		pre, err := scanArgs([]string{"--interval.wal=45s", "--listen=:9"})
		if err != nil {
			t.Fatalf("scanArgs = %v, want nil", err)
		}
		if got := pre.intervals["wal"]; got != 45*time.Second {
			t.Errorf("intervals[wal] = %v, want 45s", got)
		}
		// The contract with flag.Parse: the consumed entries are gone and the rest
		// is byte-identical, not re-rendered. A scanner that rewrote
		// `--listen=:9` to `--listen :9` would still parse but would break
		// runConfig's claim that it reports what serve would use.
		if len(pre.rest) != 1 || pre.rest[0] != "--listen=:9" {
			t.Errorf("rest = %q, want [--listen=:9]", pre.rest)
		}
	})

	t.Run("both dynamic families in one argv", func(t *testing.T) {
		pre, err := scanArgs([]string{"--log-field-cluster=lab", "--interval.wal=45s", "--auth=none"})
		if err != nil {
			t.Fatalf("scanArgs = %v, want nil", err)
		}
		if got := pre.logFields.String(); got != "cluster=lab" {
			t.Errorf("logFields = %q, want %q", got, "cluster=lab")
		}
		if got := pre.intervals["wal"]; got != 45*time.Second {
			t.Errorf("intervals[wal] = %v, want 45s", got)
		}
		if len(pre.rest) != 1 || pre.rest[0] != "--auth=none" {
			t.Errorf("rest = %q, want [--auth=none]", pre.rest)
		}
	})

	// registry.go's own docblock promises "--interval.wal=0 -> error at parse
	// time, not at registration". scanArgs is where that promise is kept now, so
	// the error has to surface here rather than at newRegistry.
	t.Run("a bad interval is a scan error", func(t *testing.T) {
		if _, err := scanArgs([]string{"--interval.wal=0s"}); err == nil {
			t.Error("scanArgs(--interval.wal=0s) = nil error, want one")
		}
	})

	// The end-to-end shape both commands now produce, and the regression this
	// change fixes: `pgsi config dump --interval.wal=45s` used to exit 2 with
	// "flag provided but not defined" because runConfig parsed argv with no
	// interval pre-scan. Feeding the pair through the same two calls runServe
	// makes — scanArgs then newServeFlags then Parse — is the cheapest way to
	// assert the two commands cannot disagree, since they now share the code.
	t.Run("serve and config agree on the pair", func(t *testing.T) {
		pre, err := scanArgs([]string{"--interval.database=2m", "--disable-collector=locks"})
		if err != nil {
			t.Fatalf("scanArgs = %v, want nil", err)
		}
		sf := newServeFlags(pre)
		if err := sf.Parse(pre.rest); err != nil {
			t.Fatalf("Parse(%q) = %v, want nil (disable-collector is declared now, so the pre-scan's rest parses clean)", pre.rest, err)
		}
		if *sf.disableCollector != "locks" {
			t.Errorf("--disable-collector = %q, want %q", *sf.disableCollector, "locks")
		}
		if got := pre.intervals["database"]; got != 2*time.Minute {
			t.Errorf("intervals[database] = %v, want 2m", got)
		}
		if got := collector.DisabledFromFlag(*sf.disableCollector); len(got) != 1 || got[0] != "locks" {
			t.Errorf("DisabledFromFlag(%q) = %v, want [locks]", *sf.disableCollector, got)
		}
	})
}
