package collector

import (
	"strings"
	"testing"
)

// TestSchedulerMetricsRender pins the exposition body for R2.9's two families by
// exact equality rather than by substring checks, for the reason
// api's TestNewAcceptsTypedNilPerQuery compares whole bodies: equality also
// catches a duplicated HELP pair, a family rendered with the wrong TYPE, and a
// reordered body — the three shapes `promtool check metrics` reports and a
// Contains-based test passes.
func TestSchedulerMetricsRender(t *testing.T) {
	if got := (*SchedulerMetrics)(nil).Render(""); got != "" {
		t.Fatalf("nil receiver Render = %q, want empty", got)
	}

	m := NewSchedulerMetrics(4)
	const emptyWant = "# HELP pgsi_scheduler_queue_depth Collections admitted to the scheduler queue and not yet finished (R2.9).\n" +
		"# TYPE pgsi_scheduler_queue_depth gauge\n" +
		"pgsi_scheduler_queue_depth 0\n"
	if got := m.Render(""); got != emptyWant {
		t.Fatalf("empty render =\n%s\n---\nwant\n%s", got, emptyWant)
	}
	// The gauge renders at zero and the counter renders absent: a counter with no
	// series is absent, not a 0 for a cluster that has never been refused, which
	// is the same rule metrics.go:36-40 states for a family with no samples.
	if strings.Contains(emptyWant, "shed_total") {
		t.Fatal("shed_total rendered before anything was shed")
	}

	r1, ok := m.Admit()
	if !ok {
		t.Fatal("Admit failed on a queue with capacity 4")
	}
	r2, ok := m.Admit()
	if !ok {
		t.Fatal("second Admit failed on a queue with capacity 4")
	}
	m.ObserveShed(tgtPG("pg", "pg-1", "primary"))
	m.ObserveShed(tgtPG("pg", "pg-2", "replica"))
	m.ObserveShed(tgtPG("other", "other-1", "primary"))

	const want = "# HELP pgsi_scheduler_queue_depth Collections admitted to the scheduler queue and not yet finished (R2.9).\n" +
		"# TYPE pgsi_scheduler_queue_depth gauge\n" +
		"pgsi_scheduler_queue_depth 2\n" +
		"# HELP pgsi_scheduler_shed_total Collections not run because the scheduler queue was full (R2.9).\n" +
		"# TYPE pgsi_scheduler_shed_total counter\n" +
		"pgsi_scheduler_shed_total{cluster=\"other\"} 1\n" +
		"pgsi_scheduler_shed_total{cluster=\"pg\"} 2\n"
	if got := m.Render(""); got != want {
		t.Fatalf("render =\n%s\n---\nwant\n%s", got, want)
	}

	// R9.2's configurable prefix applies to both families, the way UpMetricBase
	// and StoreMetricBase are concatenated rather than hard-coded.
	const prefixed = "# HELP x_scheduler_queue_depth Collections admitted to the scheduler queue and not yet finished (R2.9).\n" +
		"# TYPE x_scheduler_queue_depth gauge\n" +
		"x_scheduler_queue_depth 2\n" +
		"# HELP x_scheduler_shed_total Collections not run because the scheduler queue was full (R2.9).\n" +
		"# TYPE x_scheduler_shed_total counter\n" +
		"x_scheduler_shed_total{cluster=\"other\"} 1\n" +
		"x_scheduler_shed_total{cluster=\"pg\"} 2\n"
	if got := m.Render("x_"); got != prefixed {
		t.Fatalf("prefixed render =\n%s\n---\nwant\n%s", got, prefixed)
	}

	// Releasing returns the token, so the gauge follows the collections that are
	// actually in flight rather than ratcheting upward.
	r1()
	r1() // second release is a no-op, not a second token taken back out
	if got := m.Depth(); got != 1 {
		t.Fatalf("Depth after one release and a double call = %d, want 1", got)
	}
	r2()
	if got := m.Depth(); got != 0 {
		t.Fatalf("Depth after both releases = %d, want 0", got)
	}
}

// TestSchedulerMetricsAdmitCap checks that the depth is a cap and not a
// measurement: the (depth+1)th Admit must fail, and the default 512 must be what
// a Config that never set the field gets.
func TestSchedulerMetricsAdmitCap(t *testing.T) {
	m := NewSchedulerMetrics(3)
	var releases []func()
	for i := 0; i < 3; i++ {
		r, ok := m.Admit()
		if !ok {
			t.Fatalf("Admit %d failed on a queue with capacity 3", i)
		}
		releases = append(releases, r)
	}
	if _, ok := m.Admit(); ok {
		t.Fatal("fourth Admit succeeded on a queue with capacity 3")
	}
	if got := m.Depth(); got != 3 {
		t.Fatalf("Depth = %d, want 3", got)
	}
	for _, r := range releases {
		r()
	}
	if _, ok := m.Admit(); !ok {
		t.Fatal("Admit failed after the queue drained")
	}

	// The default: a Config that never set SchedulerQueueDepth gets R2.9's 512,
	// and 512 admits succeed where the 513th does not.
	d := NewSchedulerMetrics(0)
	for i := 0; i < DefaultSchedulerQueueDepth; i++ {
		if _, ok := d.Admit(); !ok {
			t.Fatalf("Admit %d failed on a default-capacity queue", i)
		}
	}
	if _, ok := d.Admit(); ok {
		t.Fatalf("Admit succeeded beyond DefaultSchedulerQueueDepth (%d)", DefaultSchedulerQueueDepth)
	}
}
