package collector

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRenderDurationExact is the golden for the histogram exposition: one
// collector, one observation of 0.2 s, asserted as a whole byte-for-byte string
// rather than with Contains. A partial assertion would let the two bugs this file
// already caught through — a missing `collector` label on the sample lines, and a
// HELP/TYPE pair repeated per collector instead of once per family.
func TestRenderDurationExact(t *testing.T) {
	s := NewStats()
	s.ObserveDuration("wal", 0.2)

	want := `# HELP pgsi_collector_duration_seconds Wall duration of one collection round (R2.2).
# TYPE pgsi_collector_duration_seconds histogram
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.005"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.01"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.025"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.05"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.1"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.25"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.5"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="1"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="2.5"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="5"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="10"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="+Inf"} 1
pgsi_collector_duration_seconds_sum{collector="wal"} 0.2
pgsi_collector_duration_seconds_count{collector="wal"} 1
`
	if got := s.RenderDuration(); got != want {
		t.Errorf("RenderDuration() =\n%s\nwant\n%s", got, want)
	}
}

// TestRenderDurationCumulative pins the histogram contract: `le` is inclusive and
// every bucket contains all the observations in the buckets below it. Storing
// per-boundary counts and emitting a running sum is the implementation; this is
// the observable it has to satisfy.
func TestRenderDurationCumulative(t *testing.T) {
	s := NewStats()
	// 0.25 and 1.5 are exactly representable, so the sum renders as "1.75"
	// rather than "1.7000000000000002" — a float64 artefact would otherwise make
	// this assertion depend on addition order.
	s.ObserveDuration("wal", 0.25)
	s.ObserveDuration("wal", 1.5)

	got := s.RenderDuration()
	want := map[string]string{
		`le="0.005"`: "0", `le="0.01"`: "0", `le="0.025"`: "0", `le="0.05"`: "0",
		`le="0.1"`: "0", `le="0.25"`: "1", `le="0.5"`: "1", `le="1"`: "1",
		`le="2.5"`: "2", `le="5"`: "2", `le="10"`: "2", `le="+Inf"`: "2",
	}
	for le, count := range want {
		line := fmt.Sprintf("pgsi_collector_duration_seconds_bucket{collector=\"wal\",%s} %s", le, count)
		if !strings.Contains(got, line) {
			t.Errorf("missing bucket line %q\nrender:\n%s", line, got)
		}
	}
	for _, line := range []string{
		`pgsi_collector_duration_seconds_sum{collector="wal"} 1.75`,
		`pgsi_collector_duration_seconds_count{collector="wal"} 2`,
	} {
		if !strings.Contains(got, line) {
			t.Errorf("missing line %q\nrender:\n%s", line, got)
		}
	}
}

// TestRenderDurationAboveTopBucket: a round that exceeds every finite boundary
// still lands in +Inf and in the sum. R2.1's 10 s timeout is the top finite
// bucket precisely so a timed-out round is visible somewhere other than +Inf, but
// a collector configured with a longer timeout can still exceed it.
func TestRenderDurationAboveTopBucket(t *testing.T) {
	s := NewStats()
	s.ObserveDuration("database", 60)

	got := s.RenderDuration()
	for _, line := range []string{
		`pgsi_collector_duration_seconds_bucket{collector="database",le="10"} 0`,
		`pgsi_collector_duration_seconds_bucket{collector="database",le="+Inf"} 1`,
		`pgsi_collector_duration_seconds_sum{collector="database"} 60`,
		`pgsi_collector_duration_seconds_count{collector="database"} 1`,
	} {
		if !strings.Contains(got, line) {
			t.Errorf("missing line %q\nrender:\n%s", line, got)
		}
	}
}

// TestDurationBucketsStraddleTheSpec checks the bucket list against the two
// numbers metrics.go's doc comment claims they are placed around: R13.1's 3 s
// per-cluster budget and R2.1's 10 s per-collector timeout.
func TestDurationBucketsStraddleTheSpec(t *testing.T) {
	if len(durationBuckets) == 0 {
		t.Fatal("durationBuckets is empty")
	}
	top := durationBuckets[len(durationBuckets)-1]
	if top != DefaultTimeout.Seconds() {
		t.Errorf("top finite bucket = %s, want %s (R2.1's per-collector timeout, so a timed-out round lands in a named bucket)",
			formatFloat(top), formatFloat(DefaultTimeout.Seconds()))
	}
	// R13.1's "< 3 s" must be straddled: a bucket below it and one above, so a
	// 3.1 s round and a 2.9 s round are distinguishable in the histogram.
	var below, above bool
	for _, b := range durationBuckets {
		if b < 3 {
			below = true
		}
		if b > 3 {
			above = true
		}
	}
	if !below || !above {
		t.Errorf("no bucket pair straddles R13.1's 3 s budget (buckets %v)", durationBuckets)
	}
	// Buckets must be strictly increasing, or the cumulative counts are nonsense.
	for i := 1; i < len(durationBuckets); i++ {
		if durationBuckets[i] <= durationBuckets[i-1] {
			t.Fatalf("durationBuckets not increasing at %d: %v", i, durationBuckets)
		}
	}
}

func TestRenderErrorsExact(t *testing.T) {
	s := NewStats()
	s.IncError("wal", "pg", "db")
	s.IncError("wal", "pg", "db")
	s.IncError("wal", "other", "db")
	s.IncError("database", "pg", "db")

	want := `# HELP pgsi_collector_errors_total Failed collection rounds (R2.2).
# TYPE pgsi_collector_errors_total counter
pgsi_collector_errors_total{cluster="pg",collector="database",namespace="db"} 1
pgsi_collector_errors_total{cluster="other",collector="wal",namespace="db"} 1
pgsi_collector_errors_total{cluster="pg",collector="wal",namespace="db"} 2
`
	if got := s.RenderErrors(); got != want {
		t.Errorf("RenderErrors() =\n%s\nwant\n%s", got, want)
	}
}

// TestRenderIsTheTwoFamilies pins Render() as the concatenation the MetricHub
// composes, so the hub's one line cannot silently drop a family.
func TestRenderIsTheTwoFamilies(t *testing.T) {
	s := NewStats()
	s.ObserveDuration("wal", 0.02)
	s.IncError("wal", "pg", "db")
	if got, want := s.Render(), s.RenderDuration()+s.RenderErrors(); got != want {
		t.Errorf("Render() != RenderDuration()+RenderErrors()\ngot\n%s\nwant\n%s", got, want)
	}
	if NewStats().Render() != "" {
		t.Errorf("an empty Stats renders %q, want the empty string (no headers for a family with no series)",
			NewStats().Render())
	}
}

// TestRenderExact is the byte-for-byte golden for the third renderer, the one
// the MetricHub actually calls. TestRenderIsTheTwoFamilies above pins Render()
// *relationally* (it equals RenderDuration()+RenderErrors()), which catches a
// dropped family but not the seam: a future edit that joins the two blocks with
// "\n", or that emits errors before durations, satisfies the relational test and
// changes every scrape. This is the same standard the API applies to
// testdata/api/clusters.json via TestGoldenFixtureIsCanonical — the exported
// text is a contract with a consumer, so it is asserted as a whole string — and
// it is the standard the coordinator asked be applied to every renderer in this
// file rather than kept as a one-off on RenderDuration.
func TestRenderExact(t *testing.T) {
	s := NewStats()
	s.ObserveDuration("database", 0.02)
	s.ObserveDuration("wal", 0.2)
	s.IncError("database", "pg", "db")
	s.IncError("wal", "pg", "db")

	want := `# HELP pgsi_collector_duration_seconds Wall duration of one collection round (R2.2).
# TYPE pgsi_collector_duration_seconds histogram
pgsi_collector_duration_seconds_bucket{collector="database",le="0.005"} 0
pgsi_collector_duration_seconds_bucket{collector="database",le="0.01"} 0
pgsi_collector_duration_seconds_bucket{collector="database",le="0.025"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="0.05"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="0.1"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="0.25"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="0.5"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="1"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="2.5"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="5"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="10"} 1
pgsi_collector_duration_seconds_bucket{collector="database",le="+Inf"} 1
pgsi_collector_duration_seconds_sum{collector="database"} 0.02
pgsi_collector_duration_seconds_count{collector="database"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.005"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.01"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.025"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.05"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.1"} 0
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.25"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="0.5"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="1"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="2.5"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="5"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="10"} 1
pgsi_collector_duration_seconds_bucket{collector="wal",le="+Inf"} 1
pgsi_collector_duration_seconds_sum{collector="wal"} 0.2
pgsi_collector_duration_seconds_count{collector="wal"} 1
# HELP pgsi_collector_errors_total Failed collection rounds (R2.2).
# TYPE pgsi_collector_errors_total counter
pgsi_collector_errors_total{cluster="pg",collector="database",namespace="db"} 1
pgsi_collector_errors_total{cluster="pg",collector="wal",namespace="db"} 1
`
	if got := s.Render(); got != want {
		t.Errorf("Render() =\n%s\nwant\n%s", got, want)
	}
}

func TestPrefix(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", DefaultPrefix},
		{DefaultPrefix, DefaultPrefix},
		{"x_", "x_"},
		{"acme_", "acme_"},
	}
	for _, tt := range tests {
		s := NewStatsWithPrefix(tt.in)
		if got := s.Prefix(); got != tt.want {
			t.Errorf("NewStatsWithPrefix(%q).Prefix() = %q, want %q", tt.in, got, tt.want)
			continue
		}
		s.ObserveDuration("wal", 0.02)
		s.IncError("wal", "pg", "db")
		for _, want := range []string{
			tt.want + "collector_duration_seconds",
			tt.want + "collector_errors_total",
		} {
			if !strings.Contains(s.Render(), want) {
				t.Errorf("render with prefix %q is missing %q\n%s", tt.in, want, s.Render())
			}
		}
	}
	// An empty prefix must not export `collector_duration_seconds`: R9.2's
	// default exists to be applied, not to be omitted.
	if strings.Contains(NewStatsWithPrefix("").Render(), "\ncollector_duration_seconds") {
		t.Error("an empty prefix exported an unprefixed family name")
	}
}

func TestAge(t *testing.T) {
	s := NewStats()
	if _, ok := s.Age("wal", "pg", "db"); ok {
		t.Fatal("Age() reported ok for a triple that never ran (\"never\" and \"0s ago\" must be distinguishable)")
	}
	s.MarkSuccess("wal", "pg", "db")
	age, ok := s.Age("wal", "pg", "db")
	if !ok {
		t.Fatal("Age() = false after MarkSuccess")
	}
	if age < 0 || age > time.Second {
		t.Errorf("Age() = %s, want a few ms after a MarkSuccess that just happened", age)
	}
	// The age is per-triple: a second cluster's success must not refresh this one.
	if _, ok := s.Age("wal", "other", "db"); ok {
		t.Error("Age() reported ok for a different cluster's triple")
	}
	s.MarkSuccess("wal", "other", "db")
	if _, ok := s.Age("wal", "other", "db"); !ok {
		t.Error("Age() = false for the cluster that just succeeded")
	}
}

func TestLabelString(t *testing.T) {
	tests := []struct {
		in   map[string]string
		want string
	}{
		{nil, ""},
		{map[string]string{}, ""},
		{map[string]string{"le": "1"}, `{le="1"}`},
		// Sorted, not insertion-ordered: the render must be byte-stable across
		// runs, which is what makes a golden file possible.
		{map[string]string{"namespace": "db", "cluster": "pg", "collector": "wal"},
			`{cluster="pg",collector="wal",namespace="db"}`},
		{map[string]string{"a": `x"y`, "b": "p\nq"}, `{a="x\"y",b="p\nq"}`},
	}
	for _, tt := range tests {
		if got := labelString(tt.in); got != tt.want {
			t.Errorf("labelString(%v) = %s, want %s", tt.in, got, tt.want)
		}
	}
	// withLabel must not mutate the input map — the renderer reuses it for every
	// line, so a mutation would leak `le` into the _sum line.
	base := map[string]string{"collector": "wal"}
	withLabel(base, "le", "1")
	if len(base) != 1 {
		t.Errorf("withLabel mutated the input map: %v", base)
	}
}

func TestFormatFloat(t *testing.T) {
	// The exposition wants the shortest form that round-trips: "5", not
	// "5.000000" (%f) and not "5e+00" (%g with a precision that switches styles).
	tests := map[float64]string{
		0: "0", 1: "1", 5: "5", 0.1: "0.1", 0.005: "0.005", 1.75: "1.75", 10: "10",
	}
	for in, want := range tests {
		if got := formatFloat(in); got != want {
			t.Errorf("formatFloat(%v) = %s, want %s", in, got, want)
		}
	}
}

// TestStatsConcurrent exercises the mutex: the scheduler calls all three mutators
// from N goroutines per round, and `go test -race` is what turns this from a
// compile check into a real one.
func TestStatsConcurrent(t *testing.T) {
	s := NewStats()
	const goroutines, per = 8, 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			name := colName(g)
			for i := 0; i < per; i++ {
				s.ObserveDuration(name, float64(i)/100.0)
				s.IncError(name, "pg", "db")
				s.MarkSuccess(name, "pg", "db")
			}
		}(g)
	}
	wg.Wait()

	got := s.RenderDuration()
	for g := 0; g < goroutines; g++ {
		line := fmt.Sprintf("pgsi_collector_duration_seconds_count{collector=%q} %d", colName(g), per)
		if !strings.Contains(got, line) {
			t.Errorf("missing %q", line)
		}
	}
	for g := 0; g < goroutines; g++ {
		line := fmt.Sprintf("pgsi_collector_errors_total{cluster=\"pg\",collector=%q,namespace=\"db\"} %d", colName(g), per)
		if !strings.Contains(s.RenderErrors(), line) {
			t.Errorf("missing %q", line)
		}
	}
}

// TestNilStats pins the nil-receiver contract documented on Stats. It exists
// because a nil *Stats used to panic: Render -> RenderDuration -> s.mu.Lock()
// dereferences the receiver, so the API's Config holding a nil *Stats — the
// state a server with no collector is in — crashed the /metrics handler instead
// of answering with the families it does have. The assertion shape here is
// "nil renders the same as a Stats that observed nothing", not just "nil returns
// empty": that equality is the whole claim, and asserting only Render() == ""
// would pass even if Prefix() returned "" (which would break R9.2 by exporting
// an unprefixed family the moment someone did configure a collector).
func TestNilStats(t *testing.T) {
	var s *Stats

	if got := s.Render(); got != "" {
		t.Errorf("(*Stats)(nil).Render() = %q, want \"\"", got)
	}
	if got := s.RenderDuration(); got != "" {
		t.Errorf("(*Stats)(nil).RenderDuration() = %q, want \"\"", got)
	}
	if got := s.RenderErrors(); got != "" {
		t.Errorf("(*Stats)(nil).RenderErrors() = %q, want \"\"", got)
	}

	// DefaultPrefix, not "": an empty prefix is exactly what NewStatsWithPrefix's
	// doc forbids, and Prefix() is what the API reads to name the families.
	if got := s.Prefix(); got != DefaultPrefix {
		t.Errorf("(*Stats)(nil).Prefix() = %q, want %q", got, DefaultPrefix)
	}

	// "Never succeeded" is the correct answer for a collector set that does not
	// exist, and it must be the false case rather than a zero duration.
	if d, ok := s.Age("wal", "pg", "db"); d != 0 || ok {
		t.Errorf("(*Stats)(nil).Age() = (%s, %v), want (0s, false)", d, ok)
	}

	// The three recorders are no-ops. Without the guards these panic on the
	// nil map / nil mutex, which is the failure mode that would come back first
	// if someone added a nil check to Render but not to the recorders.
	s.ObserveDuration("wal", 0.5)
	s.IncError("wal", "pg", "db")
	s.MarkSuccess("wal", "pg", "db")
	if got := s.Render(); got != "" {
		t.Errorf("Render() after recording on a nil *Stats = %q, want \"\" (a no-op recorder must not fabricate a series)", got)
	}

	// The equality the doc actually claims: nil and an unobserved NewStats are
	// indistinguishable through every method.
	fresh := NewStats()
	if s.Render() != fresh.Render() || s.Prefix() != fresh.Prefix() {
		t.Error("a nil *Stats and an unobserved NewStats() disagree; the doc says they are the same value")
	}
	if sd, sok := s.Age("wal", "pg", "db"); sd != 0 || sok {
		t.Error("nil Age() disagrees with unobserved NewStats()")
	}
}
