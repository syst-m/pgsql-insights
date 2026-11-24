package main

import (
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// TestNewRegistryHoldsOnlyThePooler pins the registry's contents to the one
// collector the driver decision leaves constructible today.
//
// The assertion is deliberately exact rather than "contains a pooler": when the
// driver lands and the seven SQL collectors are added, this test fails, and the
// failure message is the reminder that the docblock's "one collector today"
// sentence and the sql.go:34-40 quote it cites both need updating in the same
// commit. A registry test that only checked for the pooler's presence would let
// the docblock go stale silently — prose about a sibling file with no checker is
// the second contract, and this is the checker for it.
//
// The *kube.Client is nil on purpose. NewPoolerCollector's only use of the
// receiver is to take the method value kc.GetDirect, and a method value of a
// pointer-receiver method on a nil pointer is legal Go and does not dereference
// until it is called — which the registry never does, since Registry stores
// collectors and only Scheduler.Run invokes Collect. Building a real client here
// would make a cmd/pgsi test depend on a kubeconfig or an in-cluster env.
func TestNewRegistryHoldsOnlyThePooler(t *testing.T) {
	reg, err := newRegistry((*kube.Client)(nil), collector.Overrides{})
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}
	entries := reg.Enabled()
	if len(entries) != 1 {
		t.Fatalf("len(Enabled()) = %d, want 1:\n %+v", len(entries), entries)
	}
	if entries[0].Desc.Name != collector.CollectorPooler {
		t.Errorf("Enabled()[0].Desc.Name = %q, want %q", entries[0].Desc.Name, collector.CollectorPooler)
	}
	if entries[0].Desc.Source != collector.SourcePoolerMetrics {
		t.Errorf("Enabled()[0].Desc.Source = %q, want %q (S5)", entries[0].Desc.Source, collector.SourcePoolerMetrics)
	}
}

// TestNewRegistryAppliesOverrides pins the R2.3 seam: the Overrides argument is
// plumbed through, so a caller that starts building it from argv gets the
// behaviour without changing newRegistry's signature.
func TestNewRegistryAppliesOverrides(t *testing.T) {
	disabled := collector.Overrides{Disabled: []string{collector.CollectorPooler}}
	reg, err := newRegistry((*kube.Client)(nil), disabled)
	if err != nil {
		t.Fatalf("newRegistry(disabled): %v", err)
	}
	if got := reg.Enabled(); len(got) != 0 {
		t.Errorf("Enabled() with pooler disabled = %+v, want empty", got)
	}

	interval := collector.Overrides{Intervals: map[string]time.Duration{
		collector.CollectorPooler: 90 * time.Second,
	}}
	reg, err = newRegistry((*kube.Client)(nil), interval)
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
