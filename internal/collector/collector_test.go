package collector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the R2.1/R2.4 seam. collector.go declares
// `type Applicability func(pgMajor int) bool` and its comment says the answer
// comes from internal/collector/pgversion — the phase's second commit, which is
// what let the framework compile before the matrix existed. This line is the
// proof the answer fits the field: a signature change on either side now fails
// to compile here rather than at the first collector that assigns it. It lives
// in this package's test file because importing collector from pgversion's own
// package would be an import cycle.
var _ Applicability = pgversion.AppliesTo(pgversion.ViewStatWAL)

// okDesc is the minimal valid Descriptor a test can register: the four R2.1
// fields filled from §3.2/§3.3's vocabulary and nothing else.
func okDesc(name string) Descriptor {
	return Descriptor{
		Name:     name,
		Interval: DefaultInterval,
		Source:   SourceSQL,
		Cost:     CostStandard,
	}
}

// fakeCollector is the Collector used by every test in this package: a
// Descriptor plus a func, so a test states exactly what one round does.
type fakeCollector struct {
	desc Descriptor
	fn   func(ctx context.Context, tgt Target) (Result, error)
}

func (f fakeCollector) Descriptor() Descriptor { return f.desc }

func (f fakeCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	if f.fn == nil {
		return Result{}, nil
	}
	return f.fn(ctx, tgt)
}

func newFake(name string, fn func(context.Context, Target) (Result, error)) fakeCollector {
	return fakeCollector{desc: okDesc(name), fn: fn}
}

func TestDescriptorValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Descriptor)
		wantErr string
	}{
		{"valid", func(*Descriptor) {}, ""},
		{"empty name", func(d *Descriptor) { d.Name = "" }, "Descriptor.Name is required"},
		{"whitespace name", func(d *Descriptor) { d.Name = "  " }, "Descriptor.Name is required"},
		{"zero interval", func(d *Descriptor) { d.Interval = 0 }, "Interval must be > 0"},
		{"negative interval", func(d *Descriptor) { d.Interval = -time.Second }, "Interval must be > 0"},
		{"unknown source", func(d *Descriptor) { d.Source = "S42" }, "unknown source"},
		{"empty source", func(d *Descriptor) { d.Source = "" }, "unknown source"},
		{"unknown cost", func(d *Descriptor) { d.Cost = "budget" }, "unknown cost class"},
		{"empty cost", func(d *Descriptor) { d.Cost = "" }, "unknown cost class"},
		{"negative timeout", func(d *Descriptor) { d.Timeout = -1 }, "Timeout must be >= 0"},
		{"empty view group", func(d *Descriptor) { d.ViewGroups = []string{"database", ""} }, "empty entry in ViewGroups"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := okDesc("database")
			tt.mutate(&d)
			err := d.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %q, want it to contain %q", err, tt.wantErr)
			}
			// Every error from a valid-named descriptor must name the collector,
			// because a user with twelve registered collectors cannot act on
			// "invalid descriptor".
			if tt.name != "empty name" && tt.name != "whitespace name" &&
				!strings.Contains(err.Error(), "database") {
				t.Fatalf("Validate() = %q, want it to name the collector", err)
			}
		})
	}
}

func TestValidSource(t *testing.T) {
	// The set is §3.2's ten, and the test pins the count so a future edit that
	// adds an S11 without updating §3.2 is caught here rather than in review.
	want := []SourceID{
		SourceClusterStatus, SourceSQL, SourceInstanceMetrics, SourceInstanceStatus,
		SourcePoolerMetrics, SourcePoolerRuntime, SourceK8sMetrics,
		SourceKubectlStatus, SourcePostgresExporter, SourcePodLogs,
	}
	if len(want) != 10 {
		t.Fatalf("§3.2 defines ten sources, got %d constants", len(want))
	}
	for _, id := range want {
		if !ValidSource(id) {
			t.Errorf("ValidSource(%q) = false, want true", id)
		}
	}
	for _, id := range []SourceID{"", "S0", "S11", "s1", "S2 ", "postgres"} {
		if ValidSource(id) {
			t.Errorf("ValidSource(%q) = true, want false", id)
		}
	}
}

func TestValidCostClass(t *testing.T) {
	for _, c := range []CostClass{CostCheap, CostStandard, CostExpensive} {
		if !ValidCostClass(c) {
			t.Errorf("ValidCostClass(%q) = false, want true", c)
		}
	}
	for _, c := range []CostClass{"", "Cheap", "EXPENSIVE", "moderate"} {
		if ValidCostClass(c) {
			t.Errorf("ValidCostClass(%q) = true, want false", c)
		}
	}
}

func TestEffectiveTimeout(t *testing.T) {
	tests := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"unset selects the R2.1 default", 0, DefaultTimeout},
		{"explicit wins", 3 * time.Second, 3 * time.Second},
		{"explicit longer than default", time.Minute, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := okDesc("wal")
			d.Timeout = tt.set
			if got := d.EffectiveTimeout(); got != tt.want {
				t.Fatalf("EffectiveTimeout() = %s, want %s", got, tt.want)
			}
		})
	}
	if DefaultTimeout != 10*time.Second {
		t.Errorf("DefaultTimeout = %s, want 10s (R2.1 names 10 s)", DefaultTimeout)
	}
	if DefaultPerClusterConcurrency != 4 {
		t.Errorf("DefaultPerClusterConcurrency = %d, want 4 (R2.1 names 4)", DefaultPerClusterConcurrency)
	}
	if DefaultInterval != 60*time.Second {
		t.Errorf("DefaultInterval = %s, want 60s (§3.3's period)", DefaultInterval)
	}
}

func TestApplies(t *testing.T) {
	d := okDesc("database")
	if !d.Applies(15) || !d.Applies(16) || !d.Applies(99) {
		t.Fatal("a nil AppliesTo must behave as AllMajors")
	}
	d.AppliesTo = func(major int) bool { return major == 16 }
	if !d.Applies(16) {
		t.Fatal("Applies(16) = false, want true")
	}
	if d.Applies(15) {
		t.Fatal("Applies(15) = true, want false")
	}
	// PGMajor 0 is "unknown"; AllMajors admits it, which is why a SQL collector
	// must set an explicit AppliesTo rather than relying on the nil default.
	if !AllMajors(0) {
		t.Fatal("AllMajors(0) = false, want true")
	}
}

func TestTargetKeys(t *testing.T) {
	full := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	if got := full.Key(); got != "db/pg/pg-1" {
		t.Errorf("Key() = %q, want db/pg/pg-1 (R9.3's instance identity)", got)
	}
	if got := full.ClusterKey(); got != "db/pg" {
		t.Errorf("ClusterKey() = %q, want db/pg", got)
	}
	// A cluster-scoped source (S1's CR status) has no pod, and its Key must not
	// end in a trailing slash that reads as an empty pod name.
	scoped := Target{Namespace: "db", Cluster: "pg", Source: SourceClusterStatus}
	if got := scoped.Key(); got != "db/pg" {
		t.Errorf("cluster-scoped Key() = %q, want db/pg", got)
	}
}

func TestTargetValid(t *testing.T) {
	tests := []struct {
		name string
		tgt  Target
		want bool
	}{
		{"complete", Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Source: SourceSQL}, true},
		{"cluster scoped", Target{Namespace: "db", Cluster: "pg", Source: SourceClusterStatus}, true},
		{"no cluster", Target{Namespace: "db", Source: SourceSQL}, false},
		{"no namespace", Target{Cluster: "pg", Source: SourceSQL}, false},
		{"no source", Target{Namespace: "db", Cluster: "pg"}, false},
		{"unknown source", Target{Namespace: "db", Cluster: "pg", Source: "S99"}, false},
		{"empty", Target{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tgt.Valid(); got != tt.want {
				t.Fatalf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}
