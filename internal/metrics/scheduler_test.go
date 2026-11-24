package metrics_test

// Tests for R2.9's scheduler pair as the hub renders it:
// pgsi_scheduler_queue_depth and pgsi_scheduler_shed_total.
//
// The rendering itself is collector/scheduler_metrics_test.go's — the gauge
// line, the counter's per-cluster series, the label set, the
// absent-until-something-is-shed rule. What this file pins is the hub half, the
// only part this package can assert: that WithScheduler's value reaches
// RenderContext, that the empty prefix the hub passes resolves to
// collector.DefaultPrefix rather than to a name with no prefix, and that a hub
// built without the option omits both families instead of emitting empty ones.
//
// The assertions compare against m.Render("") — the owner's own bytes — rather
// than against a transcription of them here. A golden copied into this file would
// be a second copy of a contract the collector package already owns (R16.8), and
// it would pass on a day the collector changed the HELP text and this file had
// not followed; containing the owner's render cannot.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/metrics"
)

// schedulerFixture is a SchedulerMetrics with a known depth and two shed
// clusters: one admitted-and-not-yet-finished collection, and three refusals
// across two clusters (two on pg-a, one on pg-b), so the gauge is a number that
// is not the zero value a stub would print and the counter has two series in
// sorted order.
func schedulerFixture(t *testing.T) *collector.SchedulerMetrics {
	t.Helper()

	m := collector.NewSchedulerMetrics(4)
	// The release func is deliberately not called: releasing it would drain the
	// queue and the gauge would read 0, which is the value the stub this family
	// replaced would have printed, and the test could not tell the two apart.
	if _, ok := m.Admit(); !ok {
		t.Fatal("Admit() on a fresh queue of depth 4 reported ok=false")
	}
	m.ObserveShed(collector.Target{Namespace: "pg", Cluster: "pg-a"})
	m.ObserveShed(collector.Target{Namespace: "pg", Cluster: "pg-a"})
	m.ObserveShed(collector.Target{Namespace: "pg", Cluster: "pg-b"})

	// The two accessors are the family's own read path, so checking them here
	// means a failure names the counter rather than the exposition.
	if got := m.Depth(); got != 1 {
		t.Fatalf("Depth() = %d, want 1", got)
	}
	if got := m.Shed("pg-a"); got != 2 {
		t.Fatalf(`Shed("pg-a") = %d, want 2`, got)
	}
	return m
}

// TestSchedulerPairRendersThroughTheHub is the wiring assertion: the hub's body
// contains, verbatim, what the source renders on its own.
func TestSchedulerPairRendersThroughTheHub(t *testing.T) {
	withVersion(t, "9.9.9-test", "deadbee")
	m := schedulerFixture(t)

	want := m.Render("")
	body := metrics.New(nil, nil, metrics.WithScheduler(m)).Render()
	if !strings.Contains(body, want) {
		t.Errorf("hub body does not contain the scheduler source's own render\nbody:\n%s\n\nwant contained:\n%s", body, want)
	}

	// Both families, not one. strings.Contains on the pair's names is the check
	// that survives a rename, because the names come from the collector's
	// exported bases rather than from literals in this file.
	for _, base := range []string{collector.QueueDepthMetricBase, collector.ShedMetricBase} {
		if !strings.Contains(body, "# HELP pgsi_"+base+" ") {
			t.Errorf("hub body has no HELP line for pgsi_%s\n%s", base, body)
		}
	}
}

// TestSchedulerPrefixResolvesToTheDefault pins the one argument this package
// passes to the renderer. RenderContext hands every collector-family an empty
// prefix, which collector.SchedulerMetrics documents as "select DefaultPrefix";
// if the default ever stopped being pgsi_ — R9.2 makes metrics.prefix
// configurable — this test is where the hub's assumption becomes visible, which
// is the reason it is asserted here rather than left inside the collector's own
// table of prefixes.
func TestSchedulerPrefixResolvesToTheDefault(t *testing.T) {
	m := schedulerFixture(t)

	gauge := "pgsi_" + collector.QueueDepthMetricBase
	if !strings.HasPrefix(m.Render(""), "# HELP "+gauge+" ") {
		t.Errorf("Render(\"\") did not resolve the empty prefix to pgsi_\ngot:\n%s", m.Render(""))
	}
	// And the value on that line is the accessor's number, not a constant: the
	// same argument TestStoreSizeTracksTheRealSink makes for the store family —
	// a renderer that printed a literal passes every other test in the file.
	lines := strings.Split(m.Render(""), "\n")
	if len(lines) < 3 {
		t.Fatalf("render is %d lines, want at least 3:\n%s", len(lines), m.Render(""))
	}
	if got, want := lines[2], gauge+" "+strconv.Itoa(m.Depth()); got != want {
		t.Errorf("gauge line = %q, want %q", got, want)
	}
}

// TestSchedulerAbsentWithoutTheOption is this package's "a family with no series
// is absent" rule applied to the pair: a hub built by New alone — the state of
// every server that has not been given a scheduler — renders neither family,
// rather than a HELP/TYPE pair beside a number the process never measured.
func TestSchedulerAbsentWithoutTheOption(t *testing.T) {
	withVersion(t, "9.9.9-test", "deadbee")

	body := metrics.New(nil, nil).Render()
	for _, base := range []string{collector.QueueDepthMetricBase, collector.ShedMetricBase} {
		if strings.Contains(body, "pgsi_"+base) {
			t.Errorf("hub with no WithScheduler rendered pgsi_%s\n%s", base, body)
		}
	}
	// The typed-nil shape, which is what `var m *collector.SchedulerMetrics` in
	// the option produces. Render's nil receiver returns "", so the family is
	// absent rather than panicking — the hazard 2b63f1e removed from
	// api.Config's accessors, in its option form.
	var nilSource *collector.SchedulerMetrics
	if got := metrics.New(nil, nil, metrics.WithScheduler(nilSource)).Render(); strings.Contains(got, "pgsi_"+collector.QueueDepthMetricBase) {
		t.Errorf("typed-nil scheduler rendered the gauge\n%s", got)
	}
}
