// Package metrics is pgsi's MetricHub: the R9.8 self-observability surface, and
// the package R9.8's own row names as the home of the eight named self-metrics
// (IMPLEMENTATION.md §3.9's R9.8 row: "`internal/metrics/` (A2)").
//
// The hub exists because of a sentence in A1's internal/api/server.go. The
// function that rendered pgsi_build_info was exported "so the Phase 2 MetricHub
// renders the same line rather than a second copy of it". This package is that
// hub, and the move is the entire change: leaving BuildInfoMetric in the api
// package while a second package composed the exposition would be the second
// copy that sentence existed to prevent, and R16.8 files a second copy of a
// contract more often than this project files anything else.
//
// # The eight families, and which four render here
//
// R9.8 names eight. IMPLEMENTATION.md §3.9's R9.8 row is the authority on the
// count; this is the same accounting read from the package's side:
//
//	here      pgsi_build_info{version,commit}     R11.3   BuildInfo
//	here      pgsi_collector_duration_seconds     R2.2    collector.Stats.RenderDuration
//	here      pgsi_collector_errors_total         R2.2    collector.Stats.RenderErrors
//	here      pgsi_up{namespace,cluster,pod,instance_role}  R9.12  collector.UpTracker.Render
//	pending   pgsi_store_size_bytes               R13.5   collector.MemSink.Used — no route to it yet
//	pending   pgsi_http_request_duration_seconds  R9.8    needs the HTTP instrumentation middleware
//	pending   pgsi_scheduler_queue_depth          R2.9    needs the scheduler queue and its budget
//	pending   pgsi_scheduler_shed_total           R2.9    needs the scheduler queue and its budget
//
// One spelling note the two rows force. R9.8's list names this family
// `pgsi_scrape_target_up{cluster,pod}`; R9.12's row — added later, per the
// requirements.md §9 changelog entry reading "+R9.12 `pgsi_up`" — names it
// `pgsi_up{namespace,cluster,pod,instance_role}`, and `pgsi_up` is what
// collector.UpTracker renders. The table follows the code and R9.12, the newer
// of the two texts and the one the collector implements.
//
// The four pending families are not stubbed, and a stub is the wrong move here
// for a reason the exposition format states rather than one this package
// invents: a family with no series is absent, not a HELP/TYPE pair beside a
// value the process never measured. collector.Stats.Render and
// collector.UpTracker.Render both implement that rule on their nil receivers,
// and `promtool check metrics` reports the empty-family shape the stub would
// produce. Each pending family therefore lands in the commit that gives it a
// value, and adding one costs one Hub field plus one line in Render — the
// growth shape collector/metrics.go:44 documents as this package's contract:
// "one exported function per family, returning a string, carrying its own
// `# HELP` and `# TYPE` lines, so the hub is a strings.Builder over N calls
// with no per-family special-casing".
//
// Two of the four are blocked on a subsystem, not on a metric. The store's size
// is measurable today (collector.MemSink.Used exists and returns bytes), but
// nothing hands it to the hub: *collector.Scheduler exposes Up() and Stats()
// and no sink accessor, and api.Config carries no sink field, so the sink a
// running server writes to is not reachable from where the exposition is
// composed. The two scheduler families and the HTTP duration family need code
// that does not exist yet — R2.9's global budget with its shed counter, and the
// response-writing middleware that would observe request durations, which
// IMPLEMENTATION.md §3.9's R9.8 row files as shipping with this hub.
//
// R9.2's configurable `metrics.prefix` is the config half of the same contract,
// and IMPLEMENTATION.md §3.9's R9.2/R9.3 row assigns that config key to this
// package. Until R11.8's flag/env/ConfigMap chain lands, the hub renders with
// collector.DefaultPrefix; Render's doc explains why the empty string is the way
// to ask for it rather than an unset field.
package metrics

import (
	"fmt"
	"strings"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/version"
)

// BuildInfo renders pgsi_build_info — R11.3's build metadata, and the first
// family R9.8's list names. R9.8 fixes its label set as {version,commit} and
// nothing else: R9.3's normative identity labels (namespace, cluster, pod,
// instance_role) describe per-instance series, and the running binary's own
// build is not per-instance, so this family carries the pair R9.8 names and no
// others.
//
// This is the moved body of internal/api/server.go's BuildInfoMetric, not a
// re-derivation of it: the three lines are byte-identical to what that function
// returned, which is what TestBuildInfoRendersTheMovedFamily pins and what
// lets internal/api's TestBuildInfoMetricMatchesHandler keep comparing the
// /metrics response against this function. R11.3's acceptance is that the same
// two ldvar values appear in the OCI label, in the /api/v1/version body, and in
// this metric (IMPLEMENTATION.md §5's D6 note is how CI asserts the three
// agree), and reading version.Version and version.Commit here, at render time,
// is what keeps the metric and the endpoint from ever disagreeing — a string
// cached at construction could not, because the test surface and the linker
// both set those vars after a Server is built.
func BuildInfo() string {
	return "# HELP pgsi_build_info Build metadata of the running pgsi binary.\n" +
		"# TYPE pgsi_build_info gauge\n" +
		fmt.Sprintf("pgsi_build_info{version=%q,commit=%q} 1\n", version.Version, version.Commit)
}

// Hub composes the self-observability families one process can render (R9.8).
//
// It is a value over its sources, not a registry: every family arrives as a
// string from the package that owns it, and Render concatenates them in the
// order R9.8 lists them. That is the shape collector/metrics.go:44 names as
// this package's contract — "the hub is a strings.Builder over N calls with no
// per-family special-casing" — and the reason the hub takes *collector.Stats
// and *collector.UpTracker rather than strings: a string argument would let a
// caller hand the hub a pre-rendered body and bypass the owner's own rules about
// when a family is absent, which is the second-copy risk again.
type Hub struct {
	// collectors is the R2.2 pair — pgsi_collector_duration_seconds and
	// pgsi_collector_errors_total — as the collector package renders it.
	// A nil *collector.Stats is legal and renders nothing.
	collectors *collector.Stats

	// up is R9.12's pgsi_scrape_target_up: one series per discovered instance.
	// A nil *collector.UpTracker is legal and renders nothing.
	up *collector.UpTracker
}

// New returns the hub over the given sources. Both are nil-safe —
// *collector.Stats and *collector.UpTracker each define Render on a nil
// receiver, returning "" — so a server with no scheduler and no recorded
// collector still has a hub, and it renders exactly BuildInfo.
func New(collectors *collector.Stats, up *collector.UpTracker) *Hub {
	return &Hub{collectors: collectors, up: up}
}

// Render concatenates the families this hub can render, in R9.8's listed order:
// build info, then the collector pair, then the scrape-target gauge.
//
// A source with nothing to say contributes no bytes rather than an empty family.
// That rule is not this package's invention — collector.Stats.Render and
// collector.UpTracker.Render both return "" for a nil receiver and for an empty
// set, and collector/metrics.go:186 and collector/up.go:178 each document that
// a family with no series is absent because promtool reports the alternative.
// The consequence for a bare server is that its whole exposition is
// BuildInfo(), which is what internal/api's TestBuildInfoMetricMatchesHandler
// pins from the handler side and TestHubOnEmptySourcesRendersBuildInfoOnly from
// this one.
//
// The Up render gets an empty prefix, which is collector.UpTracker's documented
// way of selecting collector.DefaultPrefix: R9.2's `metrics.prefix` is
// configurable and the tracker applies the default when the caller has no
// config to hand, so "" is a request rather than an oversight. It is not an
// accident of api.Config having no prefix field yet — when R11.8's precedence
// chain lands, the prefix becomes a Hub field threaded to both sources, and the
// collector side takes it at construction (collector.NewStatsWithPrefix), which
// is the other reason New takes a *Stats rather than a rendered string.
func (h *Hub) Render() string {
	if h == nil {
		// The zero hub has no sources, and the only family a sourceless process
		// has is its own build info. Reachable because api.Config's Scheduler can
		// be the zero value, and because a caller can declare `var h *Hub`.
		return BuildInfo()
	}

	var b strings.Builder
	b.WriteString(BuildInfo())
	b.WriteString(h.collectors.Render())
	b.WriteString(h.up.Render(""))
	return b.String()
}
