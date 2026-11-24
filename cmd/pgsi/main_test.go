package main

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/kube"
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
// weaker instrument: `len == 9` passes if a collector is registered twice and one
// is missing, and NewRegistry itself rejects duplicates, so the only failure the
// count can hide is a wrong name. Naming all nine means a constructor that
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
		collector.CollectorDatabase,
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
		reg, err := newRegistry((*kube.Client)(nil), nil, collector.Overrides{})
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

	// Shape 2: an opener. All nine — the seven §3.3 SQL collectors plus the two
	// pooler names, because pooler-clients needs an opener (SHOW CLIENTS is a
	// psql round-trip) and so belongs to this shape rather than to shape 1.
	t.Run("with opener", func(t *testing.T) {
		reg, err := newRegistry((*kube.Client)(nil), fakeSQLOpener{}, collector.Overrides{})
		if err != nil {
			t.Fatalf("newRegistry(fake opener): %v", err)
		}
		entries := reg.Enabled()
		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, string(e.Desc.Name))
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
	reg, err := newRegistry((*kube.Client)(nil), nil, disabled)
	if err != nil {
		t.Fatalf("newRegistry(disabled): %v", err)
	}
	if got := reg.Enabled(); len(got) != 0 {
		t.Errorf("Enabled() with pooler disabled = %+v, want empty", got)
	}

	interval := collector.Overrides{Intervals: map[string]time.Duration{
		collector.CollectorPooler: 90 * time.Second,
	}}
	reg, err = newRegistry((*kube.Client)(nil), nil, interval)
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
	if _, err := newRegistry((*kube.Client)(nil), nil, ov); err == nil {
		t.Error("newRegistry with a wal interval and no opener: err = nil, want an error (wal is not registered without an opener)")
	}
	if _, err := newRegistry((*kube.Client)(nil), fakeSQLOpener{}, ov); err != nil {
		t.Errorf("newRegistry with a wal interval and an opener: err = %v, want nil (wal is registered with an opener)", err)
	}
}

// TestOpenSinkProfiles pins the two shapes --store resolves to, which is the
// half of R11.6's profile pair that cmd/pgsi owns: the memory profile fills two
// of the three api.Config fields and leaves R9.5's family nil, and a file store
// fills all three from one handle. The "sqlite" profile word itself is not
// exercised here because it resolves to defaultStoreFile, a path that exists
// only where the chart's PVC is mounted; the arm's body is openStoreFile, which
// the explicit-path subtest covers directly.
func TestOpenSinkProfiles(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		b, err := openSink(defaultStoreProfile)
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
		if b.close != nil {
			t.Error("close is set, want nil (a MemSink owns no handle to close)")
		}
	})

	t.Run("explicit path", func(t *testing.T) {
		b, err := openStoreFile(filepath.Join(t.TempDir(), "store.db"))
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
		if b.close == nil {
			t.Error("close = nil, want *store.Store's Close")
		}
	})

	t.Run("unopenable path fails at startup", func(t *testing.T) {
		if _, err := openStoreFile(filepath.Join(t.TempDir(), "missing", "store.db")); err == nil {
			t.Error("openStoreFile in a missing directory = nil error, want one (fail at startup, not one round later in Publish)")
		}
	})
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
