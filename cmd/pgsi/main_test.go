package main

import (
	"context"
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
// weaker instrument: `len == 8` passes if a collector is registered twice and one
// is missing, and NewRegistry itself rejects duplicates, so the only failure the
// count can hide is a wrong name. Naming all eight means a constructor that
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

	// Shape 2: an opener. All eight.
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
