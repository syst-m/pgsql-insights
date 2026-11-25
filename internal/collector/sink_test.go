package collector

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestSinkInterface is the compile-time assertion that MemSink satisfies §3.4's
// storage interface, so a method rename on either side fails the build rather
// than surfacing when the scheduler's call site changes.
func TestSinkInterface(_ *testing.T) {
	var _ Sink = (*MemSink)(nil)
	var _ Sink = NewMemSink(0)
}

func TestNewMemSinkCap(t *testing.T) {
	tests := []struct {
		in   int64
		want int64
	}{
		{0, DefaultMaxStoreBytes},
		{-1, DefaultMaxStoreBytes},
		{DefaultMaxStoreBytes, DefaultMaxStoreBytes},
		{4096, 4096},
		{1, 1},
	}
	for _, tt := range tests {
		if got := NewMemSink(tt.in).MaxBytes(); got != tt.want {
			t.Errorf("NewMemSink(%d).MaxBytes() = %d, want %d", tt.in, got, tt.want)
		}
	}
	// R13.5 names no default; the value is derived (see sink.go) and pinned here
	// so a change is a deliberate edit with a commit message, not a typo.
	if DefaultMaxStoreBytes != 64<<20 {
		t.Errorf("DefaultMaxStoreBytes = %d, want 64 MiB", DefaultMaxStoreBytes)
	}
}

func TestSinkKey(t *testing.T) {
	a := sinkKey(Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Source: SourceSQL}, "wal")
	if a != "db/pg/pg-1\x00wal" {
		t.Errorf("sinkKey = %q, want db/pg/pg-1\\x00wal", a)
	}
	// The NUL separator is what makes the key injective: with "-" as the
	// separator, (db-pg) + "x" and (db) + ("pg-x") collide.
	b := sinkKey(Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Source: SourceSQL}, "database")
	if a == b {
		t.Error("two collectors on one target produced the same key")
	}
	c := sinkKey(Target{Namespace: "db", Cluster: "pg", Pod: "pg-2", Source: SourceSQL}, "wal")
	if a == c {
		t.Error("two pods of one cluster produced the same key")
	}
	// A cluster-scoped target (no pod) and a pod named "wal" must not collide.
	d := sinkKey(Target{Namespace: "db", Cluster: "pg", Source: SourceClusterStatus}, "wal")
	e := sinkKey(Target{Namespace: "db", Cluster: "pg", Pod: "wal", Source: SourceSQL}, "")
	if d == e {
		t.Errorf("sinkKey collision between a podless target and a pod named wal: %q", d)
	}
}

func TestMemSinkPublishRead(t *testing.T) {
	m := NewMemSink(0)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	want := samplesOf(42, 3)
	if err := m.Publish(target, "wal", want); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got := m.Samples(target, "wal")
	if len(got) != len(want) {
		t.Fatalf("Samples() returned %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Metric != want[i].Metric || got[i].Value != want[i].Value {
			t.Errorf("Samples()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if m.Used() != samplesSize(want) {
		t.Errorf("Used() = %d, want %d (samplesSize of the published set)", m.Used(), samplesSize(want))
	}
	// Publishing an empty set is legal and readable: the bucket exists with zero
	// samples, which is distinguishable from "never ran" only if Publish does not
	// treat empty as a no-op.
	if err := m.Publish(target, "locks", nil); err != nil {
		t.Fatalf("Publish(nil): %v", err)
	}
	keys := m.Keys()
	if len(keys) != 2 {
		t.Fatalf("Keys() = %v, want two entries", keys)
	}
	if got := m.Samples(target, "locks"); len(got) != 0 {
		t.Errorf("Samples() for the empty set = %v, want empty", got)
	}
}

func TestMemSinkSamplesIsCopy(t *testing.T) {
	m := NewMemSink(0)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	if err := m.Publish(target, "wal", samplesOf(1, 2)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got := m.Samples(target, "wal")
	got[0].Value = 999
	got[0].Metric = "clobbered"
	if again := m.Samples(target, "wal"); again[0].Value != 1 || again[0].Metric != "db_xact_commit_total" {
		t.Errorf("mutating the returned slice changed the store: %v", again[0])
	}
	if m.Samples(target, "nosuchcollector") != nil {
		t.Error("Samples() for an unstored collector returned non-nil")
	}
}

func TestMemSinkEvictsOldestFirst(t *testing.T) {
	one := samplesOf(1, 1)
	size := samplesSize(one)
	m := NewMemSink(2 * size)
	pods := []string{"pg-1", "pg-2", "pg-3"}
	for i, pod := range pods[:2] {
		tgt := Target{Namespace: "db", Cluster: "pg", Pod: pod, Role: "primary", Source: SourceSQL}
		if err := m.Publish(tgt, "wal", samplesOf(float64(i+1), 1)); err != nil {
			t.Fatalf("Publish(%s): %v", pod, err)
		}
	}
	if got := m.Used(); got != 2*size {
		t.Fatalf("Used() = %d, want %d (two buckets, at the cap, nothing to evict)", got, 2*size)
	}

	// The third publish is over the cap, so the oldest (pg-1) goes and the two
	// newest remain. Evicting anything else is the bug this asserts against.
	tgt3 := Target{Namespace: "db", Cluster: "pg", Pod: "pg-3", Role: "primary", Source: SourceSQL}
	if err := m.Publish(tgt3, "wal", samplesOf(3, 1)); err != nil {
		t.Fatalf("Publish(pg-3): %v", err)
	}
	want := []string{"db/pg/pg-2\x00wal", "db/pg/pg-3\x00wal"}
	if got := m.Keys(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Keys() = %v, want %v (pg-1 is the oldest and must be the one evicted)", got, want)
	}
	if got := m.Used(); got != 2*size {
		t.Errorf("Used() = %d, want %d", got, 2*size)
	}
	// The evicted bucket reads as empty, not as a partial set.
	evicted := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	if got := m.Samples(evicted, "wal"); got != nil {
		t.Errorf("Samples() for the evicted bucket = %v, want nil", got)
	}
}

// TestMemSinkRepublishMovesToNewest is the test for the order-list rebuild: a
// republished key becomes the newest, so the *next* eviction takes the second
// oldest. Splicing the old entry out of the order slice (the obvious
// implementation, and the one with the i++ skip bug) would evict pg-1 here.
func TestMemSinkRepublishMovesToNewest(t *testing.T) {
	one := samplesOf(1, 1)
	size := samplesSize(one)
	m := NewMemSink(3 * size)
	mk := func(pod string) Target {
		return Target{Namespace: "db", Cluster: "pg", Pod: pod, Role: "primary", Source: SourceSQL}
	}
	for _, pod := range []string{"pg-1", "pg-2", "pg-3"} {
		if err := m.Publish(mk(pod), "wal", samplesOf(0, 1)); err != nil {
			t.Fatalf("Publish(%s): %v", pod, err)
		}
	}
	// Republish pg-1 with a new value: it is now the newest bucket.
	if err := m.Publish(mk("pg-1"), "wal", samplesOf(9, 1)); err != nil {
		t.Fatalf("republish pg-1: %v", err)
	}
	if err := m.Publish(mk("pg-4"), "wal", samplesOf(0, 1)); err != nil {
		t.Fatalf("Publish(pg-4): %v", err)
	}
	want := []string{"db/pg/pg-1\x00wal", "db/pg/pg-3\x00wal", "db/pg/pg-4\x00wal"}
	got := m.Keys()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("Keys() = %v, want %v (pg-2 is now the oldest, because pg-1 was republished)", got, want)
	}
	// And the republished content is the new set, not the old one.
	if s := m.Samples(mk("pg-1"), "wal"); len(s) != 1 || s[0].Value != 9 {
		t.Errorf("republished bucket holds %v, want the new sample with value 9", s)
	}
}

func TestMemSinkRepublishDoesNotGrow(t *testing.T) {
	m := NewMemSink(0)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	if err := m.Publish(target, "wal", samplesOf(1, 1)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	first := m.Used()
	if err := m.Publish(target, "wal", samplesOf(2, 5)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	want := samplesSize(samplesOf(2, 5))
	if m.Used() != want {
		t.Errorf("Used() = %d, want %d (a republish replaces the old set, it does not add to it)", m.Used(), want)
	}
	if first == m.Used() {
		t.Error("Used() did not change after publishing five samples over one")
	}
	if len(m.Keys()) != 1 {
		t.Errorf("Keys() = %v, want one key", m.Keys())
	}
}

// TestMemSinkOversizedBucketSurvives: a bucket larger than the whole cap must not
// vanish in the call that stored it. The alternative makes Publish a silent no-op
// and makes the ceiling look broken when the configuration is the problem.
func TestMemSinkOversizedBucketSurvives(t *testing.T) {
	set := samplesOf(1, 10)
	size := samplesSize(set)
	m := NewMemSink(size / 2)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	if err := m.Publish(target, "wal", set); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := m.Used(); got != size {
		t.Errorf("Used() = %d, want %d (the oversized bucket is stored, not dropped)", got, size)
	}
	if keys := m.Keys(); len(keys) != 1 {
		t.Errorf("Keys() = %v, want exactly the one bucket just published", keys)
	}
	if got := len(m.Samples(target, "wal")); got != 10 {
		t.Errorf("Samples() = %d samples, want 10", got)
	}
	// A second, smaller publish evicts the oversized one and nothing else.
	small := samplesOf(2, 1)
	if err := m.Publish(target, "locks", small); err != nil {
		t.Fatalf("Publish(locks): %v", err)
	}
	if got := m.Used(); got != samplesSize(small) {
		t.Errorf("Used() = %d, want %d (the wal bucket should now be evicted)", got, samplesSize(small))
	}
}

func TestMemSinkString(t *testing.T) {
	m := NewMemSink(4096)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	if err := m.Publish(target, "wal", samplesOf(1, 1)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got := m.String()
	for _, want := range []string{
		fmt.Sprintf("max=%d", 4096),
		fmt.Sprintf("used=%d", samplesSize(samplesOf(1, 1))),
		"buckets=1",
		"MemSink{",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
}

// TestMemSinkConcurrentPublish covers the RWMutex: the scheduler publishes from
// one goroutine per (target, collector), so Publish and Samples run concurrently
// by design. `go test -race` is what makes this more than a compile check.
func TestMemSinkConcurrentPublish(t *testing.T) {
	m := NewMemSink(0)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				tgt := Target{Namespace: "db", Cluster: fmt.Sprintf("c%d", g), Pod: fmt.Sprintf("p%d", i), Source: SourceSQL}
				if err := m.Publish(tgt, "wal", samplesOf(float64(i), 1)); err != nil {
					t.Errorf("Publish: %v", err)
					return
				}
				_ = m.Samples(tgt, "wal")
				_ = m.Keys()
			}
		}(g)
	}
	wg.Wait()
	if got := len(m.Keys()); got != 160 {
		t.Errorf("Keys() = %d, want 160 (8 clusters x 20 pods)", got)
	}
}

// TestProbeInterfaces is the compile-time half of the split Sink docblock
// describes: MemSink satisfies Sink *and* the two probe interfaces, while the
// interfaces stay out of Sink itself so *store.Store can satisfy Sink without
// being forced to answer a question it has no table for. A rename on either side
// fails the build here rather than at the scheduler's type assertion, which is
// the call site that would otherwise silently start taking the ok=false branch.
func TestProbeInterfaces(_ *testing.T) {
	var _ ProbeReader = (*MemSink)(nil)
	var _ ProbePublisher = (*MemSink)(nil)
	var _ Sink = (*MemSink)(nil)
}

// TestMemSinkPublishProbesRoundTrip pins the write/read pair and the half the
// other write method owns: PublishProbes replaces the inventory and leaves the
// bucket's samples exactly as they were.
func TestMemSinkPublishProbesRoundTrip(t *testing.T) {
	m := NewMemSink(0)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	samples := samplesOf(7, 3)
	if err := m.Publish(target, CollectorSettings, samples); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	inv := []ProbeResult{
		{Database: "appdb", Extensions: []string{PGStatStatements, "postgis"}},
		{Database: "postgres", Extensions: []string{PGStatStatements}},
	}
	if err := m.PublishProbes(target, CollectorSettings, inv); err != nil {
		t.Fatalf("PublishProbes: %v", err)
	}
	got, ok := m.Probes(target, CollectorSettings)
	if !ok {
		t.Fatal("Probes() ok = false, want true after PublishProbes")
	}
	if len(got) != len(inv) {
		t.Fatalf("Probes() = %+v, want %d rows", got, len(inv))
	}
	for i := range inv {
		if got[i].Database != inv[i].Database || len(got[i].Extensions) != len(inv[i].Extensions) {
			t.Errorf("Probes()[%d] = %+v, want %+v", i, got[i], inv[i])
		}
	}
	// The samples half survives the inventory write. This is the assertion that
	// separates the shared putLocked from a PublishProbes that rebuilt the bucket
	// from its own arguments — which would store the inventory and silently drop
	// every setting series the same round had just published.
	if s := m.Samples(target, CollectorSettings); len(s) != len(samples) {
		t.Errorf("Samples() = %d rows after PublishProbes, want %d (the inventory write must not clear the samples)", len(s), len(samples))
	}
	if want := samplesSize(samples) + probesSize(inv); m.Used() != want {
		t.Errorf("Used() = %d, want %d (both halves accounted)", m.Used(), want)
	}
}

// TestMemSinkPublishPreservesProbes is the other direction of the same rule, and
// the one the scheduler depends on: nine of the ten collectors publish samples
// and nothing else, so if Publish rebuilt the bucket from its own arguments every
// settings round's inventory would be wiped by the next activity round.
func TestMemSinkPublishPreservesProbes(t *testing.T) {
	m := NewMemSink(0)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	inv := []ProbeResult{{Database: "appdb", Extensions: []string{PGStatStatements}}}
	if err := m.PublishProbes(target, CollectorSettings, inv); err != nil {
		t.Fatalf("PublishProbes: %v", err)
	}
	if err := m.Publish(target, CollectorSettings, samplesOf(1, 2)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, ok := m.Probes(target, CollectorSettings)
	if !ok || len(got) != 1 || got[0].Database != "appdb" {
		t.Errorf("Probes() = %+v (ok=%v) after a samples republish, want the inventory preserved", got, ok)
	}
}

// TestMemSinkProbesOKForm pins the two answers R3.1's tri-state is built on. The
// bug this exists to prevent is a Probes that returns nil for both cases, which
// collapses "nobody measured this database" into "measured, nothing installed"
// and lets R10.27's not-installed screen render on an unmeasured database.
func TestMemSinkProbesOKForm(t *testing.T) {
	m := NewMemSink(0)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}

	if got, ok := m.Probes(target, CollectorSettings); ok {
		t.Errorf("Probes() ok = true on a bucket that never existed, want false (R3.1's not-known state)")
	} else if got != nil {
		t.Errorf("Probes() = %v with ok=false, want nil", got)
	}

	// A round that ran and found nothing: the bucket exists, the inventory is
	// empty, and ok must flip. This is the answer Samples cannot express, because
	// zero extension rows and zero samples look identical in the sample set.
	if err := m.PublishProbes(target, CollectorSettings, []ProbeResult{}); err != nil {
		t.Fatalf("PublishProbes(empty): %v", err)
	}
	got, ok := m.Probes(target, CollectorSettings)
	if !ok {
		t.Fatal("Probes() ok = false after publishing an empty inventory, want true (probed, nothing installed)")
	}
	if len(got) != 0 {
		t.Errorf("Probes() = %v, want empty", got)
	}
}

// TestMemSinkProbesIsCopy mirrors TestMemSinkSamplesIsCopy for the new half: the
// caller must not reach the stored backing array through the returned slice.
func TestMemSinkProbesIsCopy(t *testing.T) {
	m := NewMemSink(0)
	target := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}
	inv := []ProbeResult{{Database: "appdb", Extensions: []string{PGStatStatements}}}
	if err := m.PublishProbes(target, CollectorSettings, inv); err != nil {
		t.Fatalf("PublishProbes: %v", err)
	}
	got, _ := m.Probes(target, CollectorSettings)
	got[0].Database = "mutated"
	if again, _ := m.Probes(target, CollectorSettings); again[0].Database != "appdb" {
		t.Errorf("stored Database = %q after mutating the returned slice, want %q", again[0].Database, "appdb")
	}
	// The copy is one level deep, and saying so is the honest version of "returns a
	// copy": append([]ProbeResult(nil), inv...) copies the element structs, and
	// each struct's Extensions header still points at the caller's backing array —
	// so writing through the caller's inner slice writes through to the stored one.
	// That is exactly what Samples does for a Sample's Labels map, so the two
	// halves share one convention rather than inventing a second one; a caller that
	// wants isolation passes a slice it keeps no other handle on, which is what the
	// scheduler does with a Result it hands over and never reads again.
	inv[0].Extensions[0] = "mutated"
	if again, _ := m.Probes(target, CollectorSettings); again[0].Extensions[0] != "mutated" {
		t.Errorf("stored Extensions[0] = %q, want %q (the documented one-level copy)",
			again[0].Extensions[0], "mutated")
	}
}

// TestProbesSize pins the accounting formula's shape rather than its exactness
// (samplesSize's docblock owns that caveat): an empty inventory costs zero, and
// each row costs its own header plus every name under it, so a bucket's bytes
// field can be recomputed from its contents by one formula rather than two.
func TestProbesSize(_ *testing.T) {
	if got := probesSize(nil); got != 0 {
		panic(fmt.Sprintf("probesSize(nil) = %d, want 0", got))
	}
	one := ProbeResult{Database: "appdb", Extensions: []string{"a", "bb"}}
	want := int64(16+24+8+len("appdb")) + (16 + 1) + (16 + 2)
	if got := probesSize([]ProbeResult{one}); got != want {
		panic(fmt.Sprintf("probesSize = %d, want %d", got, want))
	}
	two := []ProbeResult{one, one}
	if got := probesSize(two); got != 2*want {
		panic(fmt.Sprintf("probesSize of two rows = %d, want %d", got, 2*want))
	}
}

// TestMemSinkNilReceiver calls all eight exported methods on a typed nil and pins
// the documented answer for each — the test half of the rule the type's docblock
// states, and the same shape as TestNilStats for Stats. It covers every method on
// purpose rather than only Probes: the failure mode is a guard added to the one
// method an ok-form assertion makes visible and forgotten on the other seven.
func TestMemSinkNilReceiver(t *testing.T) {
	var m *MemSink
	tgt := Target{Namespace: "ns", Cluster: "pg"}

	// (nil, false), not a panic. This is the method cmd/pgsi/main.go:669's
	// ok-form assertion can hand a typed nil to — ok proves the dynamic type
	// implements ProbeReader, not that the pointer is non-nil — and false is what
	// ProbeReader documents for "no inventory stored".
	if probes, ok := m.Probes(tgt, CollectorSettings); probes != nil || ok {
		t.Errorf("(*MemSink)(nil).Probes() = (%v, %v), want (nil, false)", probes, ok)
	}
	if got := m.Samples(tgt, CollectorSettings); got != nil {
		t.Errorf("(*MemSink)(nil).Samples() = %v, want nil", got)
	}
	if got := m.Keys(); got != nil {
		t.Errorf("(*MemSink)(nil).Keys() = %v, want nil", got)
	}
	// 0, not DefaultMaxStoreBytes: MaxBytes reports what this receiver was
	// configured with, and a nil receiver has not been configured — NewMemSink is
	// the only thing that applies the default and it cannot run on nil.
	if got := m.MaxBytes(); got != 0 {
		t.Errorf("(*MemSink)(nil).MaxBytes() = %d, want 0", got)
	}
	if got := m.Used(); got != 0 {
		t.Errorf("(*MemSink)(nil).Used() = %d, want 0", got)
	}
	if got := m.String(); got != "MemSink(nil)" {
		t.Errorf("(*MemSink)(nil).String() = %q, want \"MemSink(nil)\"", got)
	}

	// The two writers return a nil error: storing nothing and reporting success,
	// which is the answer internal/store/perquery.go:278's RenderPerQuery gives
	// with ("", nil) and the alternative to the package's first error value for a
	// sink that cannot fail for any other reason.
	if err := m.Publish(tgt, CollectorSettings, []Sample{{Metric: "x"}}); err != nil {
		t.Errorf("(*MemSink)(nil).Publish() = %v, want nil", err)
	}
	if err := m.PublishProbes(tgt, CollectorSettings, []ProbeResult{{Database: "d"}}); err != nil {
		t.Errorf("(*MemSink)(nil).PublishProbes() = %v, want nil", err)
	}

	// And the write must not resurrect a bucket through the nil map: a guard that
	// returned nil *after* writing to m.byKey would panic anyway, and this is the
	// assertion that says the guard precedes the write rather than following it.
	if got := m.Keys(); len(got) != 0 {
		t.Errorf("(*MemSink)(nil).Keys() after two writes = %v, want empty", got)
	}
}
