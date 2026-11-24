package metrics_test

// Tests for the R9.8 hub. The package's job is composition, so most of what is
// pinned here is about what the hub does *not* emit: an empty family, a second
// copy of a line its source already renders, or a family in an order R9.8 does
// not list.
//
// internal/api/server_test.go keeps TestBuildInfoMetricMatchesHandler, which
// compares the /metrics response to BuildInfo from the handler side. This file
// is the package side: the family's own bytes, the nil sources, and the
// composition order.

import (
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/metrics"
	"github.com/frankellis/pgsql-insights/internal/version"
)

// withVersion sets the two ldvars the way the test binary's defaults do not:
// every test here needs a known pair, and R11.3's acceptance is that the metric
// carries exactly the values the linker set.
func withVersion(t *testing.T, v, c string) {
	t.Helper()
	prevV, prevC := version.Version, version.Commit
	version.Version, version.Commit = v, c
	t.Cleanup(func() { version.Version, version.Commit = prevV, prevC })
}

// TestBuildInfoRendersTheMovedFamily pins the byte-for-byte body of
// pgsi_build_info: the three lines internal/api/server.go used to return, now
// from this package. An exact-equality assertion rather than three Contains
// calls, because the family's whole contract is its exact exposition — a second
// HELP line, a reordered label, or a missing trailing newline all parse as
// different output, and the api handler test compares this string against a
// response body with ==.
func TestBuildInfoRendersTheMovedFamily(t *testing.T) {
	withVersion(t, "9.9.9-test", "deadbee")

	const want = "# HELP pgsi_build_info Build metadata of the running pgsi binary.\n" +
		"# TYPE pgsi_build_info gauge\n" +
		"pgsi_build_info{version=\"9.9.9-test\",commit=\"deadbee\"} 1\n"

	if got := metrics.BuildInfo(); got != want {
		t.Fatalf("BuildInfo()\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestBuildInfoLabelSetIsR9_8sPair checks the label set rather than the values.
// R9.8 fixes this family as pgsi_build_info{version,commit}; R9.3's normative
// identity labels (namespace, cluster, pod, instance_role) belong to
// per-instance series, and a hub that added them here would create a second
// series per build and break every query written against the documented shape.
func TestBuildInfoLabelSetIsR9_8sPair(t *testing.T) {
	withVersion(t, "1.2.3", "abc123")

	body := metrics.BuildInfo()
	line, ok := strings.CutPrefix(strings.SplitN(body, "\n", 3)[2], "pgsi_build_info")
	if !ok {
		t.Fatalf("sample line is not pgsi_build_info: %q", body)
	}
	labels, _, ok := strings.Cut(line, " ")
	if !ok {
		t.Fatalf("sample line has no value: %q", line)
	}
	const want = `{version="1.2.3",commit="abc123"}`
	if labels != want {
		t.Errorf("labels = %s, want %s (R9.8's pair and nothing else)", labels, want)
	}
}

// TestBuildInfoReadsTheLdvarsAtRenderTime pins the reason BuildInfo is a
// function and not a package-level string: R11.3's acceptance is that the
// metric, the /api/v1/version body and the OCI label agree, and a value cached
// at init could drift from the endpoint the moment either is set later.
func TestBuildInfoReadsTheLdvarsAtRenderTime(t *testing.T) {
	withVersion(t, "0.0.1", "aaaaaaa")
	first := metrics.BuildInfo()

	version.Version, version.Commit = "0.0.2", "bbbbbbb"
	second := metrics.BuildInfo()

	if first == second {
		t.Fatalf("render did not change after the ldvars did:\n%s", second)
	}
	if !strings.Contains(second, `version="0.0.2"`) || !strings.Contains(second, `commit="bbbbbbb"`) {
		t.Errorf("render does not carry the new pair:\n%s", second)
	}
}

// TestHubOnEmptySourcesRendersBuildInfoOnly is the composition rule for a
// server that has collected nothing: the two empty sources contribute no bytes,
// so the exposition is exactly the build family. This is the invariant
// internal/api's TestBuildInfoMetricMatchesHandler relies on, and the reason
// neither source's nil branch needed special-casing in the hub.
func TestHubOnEmptySourcesRendersBuildInfoOnly(t *testing.T) {
	withVersion(t, "9.9.9-test", "deadbee")

	want := metrics.BuildInfo()
	if got := metrics.New(nil, nil).Render(); got != want {
		t.Errorf("New(nil, nil).Render()\ngot:\n%s\nwant:\n%s", got, want)
	}

	// The nil hub is the same case reached a different way: no sources at all.
	var zero *metrics.Hub
	if got := zero.Render(); got != want {
		t.Errorf("(*Hub)(nil).Render()\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestHubComposesFamiliesInR9_8Order is the hub's actual job: every available
// family present, once each, in the order R9.8 lists them. Duplicated HELP
// lines are the failure mode this checks for as much as a missing one —
// promtool reports a repeated HELP/TYPE pair as a duplicate, which is why
// collector.Stats renders one pair per family with every collector underneath
// and why the hub concatenates rather than looping.
func TestHubComposesFamiliesInR9_8Order(t *testing.T) {
	withVersion(t, "9.9.9-test", "deadbee")

	stats := collector.NewStats()
	stats.ObserveDuration("database", 0.012)
	stats.IncError("settings", "pg", "db")

	up := collector.NewUpTracker()
	up.Track([]collector.Target{{
		Namespace: "db",
		Cluster:   "pg",
		Pod:       "pg-1",
		Role:      "primary",
		Address:   "10.0.0.1:5432",
		Source:    collector.SourceSQL,
		PGMajor:   16,
	}})

	body := metrics.New(stats, up).Render()

	order := []string{
		"# HELP pgsi_build_info ",
		"# HELP pgsi_collector_duration_seconds ",
		"# HELP pgsi_collector_errors_total ",
		"# HELP pgsi_up ",
	}
	prev := -1
	for _, help := range order {
		n := strings.Count(body, help)
		if n != 1 {
			t.Errorf("%q appears %d times, want 1\nbody:\n%s", help, n, body)
		}
		i := strings.Index(body, help)
		if i < prev {
			t.Errorf("%q is out of R9.8's order\nbody:\n%s", help, body)
		}
		prev = i
	}

	for _, sample := range []string{
		`pgsi_build_info{version="9.9.9-test",commit="deadbee"} 1`,
		"pgsi_collector_errors_total{cluster=\"pg\",collector=\"settings\",namespace=\"db\"} 1",
		`pgsi_up{cluster="pg",instance_role="primary",namespace="db",pod="pg-1"} 0`,
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("body missing %q\nbody:\n%s", sample, body)
		}
	}
}

// TestHubOmitsAnAbsentFamilyWithoutOmittingItsNeighbour is the half of the
// "no empty families" rule that a single-source test cannot reach: a Stats with
// a duration but no errors renders the duration family and not the errors one,
// and the hub must not fill the gap. A hub that emitted a bare HELP/TYPE pair
// for the empty family is what `promtool check metrics` reports as the
// empty-family shape, and it is the reason each source decides its own absence.
func TestHubOmitsAnAbsentFamilyWithoutOmittingItsNeighbour(t *testing.T) {
	withVersion(t, "9.9.9-test", "deadbee")

	stats := collector.NewStats()
	stats.ObserveDuration("database", 0.012)

	body := metrics.New(stats, nil).Render()

	if !strings.Contains(body, "# HELP pgsi_collector_duration_seconds ") {
		t.Errorf("duration family missing, though a collector was observed\nbody:\n%s", body)
	}
	if !strings.Contains(body, "pgsi_collector_duration_seconds_count{collector=\"database\"} 1") {
		t.Errorf("duration series missing\nbody:\n%s", body)
	}
	if strings.Contains(body, "pgsi_collector_errors_total") {
		t.Errorf("errors family present though no error was counted\nbody:\n%s", body)
	}
	if strings.Contains(body, "pgsi_up{") {
		t.Errorf("pgsi_up present though the tracker is nil\nbody:\n%s", body)
	}
}
