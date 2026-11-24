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
// # The eight families, and which five render here
//
// R9.8 names eight. IMPLEMENTATION.md §3.9's R9.8 row is the authority on the
// count; this is the same accounting read from the package's side:
//
//	here      pgsi_build_info{version,commit}     R11.3   BuildInfo
//	here      pgsi_collector_duration_seconds     R2.2    collector.Stats.RenderDuration
//	here      pgsi_collector_errors_total         R2.2    collector.Stats.RenderErrors
//	here      pgsi_up{namespace,cluster,pod,instance_role}  R9.12  collector.UpTracker.Render
//	pending   pgsi_store_size_bytes               R13.5   collector.MemSink.Used — no route to it yet
//	here      pgsi_http_request_duration_seconds  R9.8    HTTPRequests.Render (http.go)
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
// The three pending families are not stubbed, and a stub is the wrong move here
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
// composed. The two scheduler families need R2.9's global budget with its shed
// counter, which does not exist yet. The HTTP duration family needed a renderer
// rather than a subsystem, and it is in http.go; the api-side middleware that
// feeds it is the one line in internal/api not yet wired, and until it is, the
// family has no observations and renders absent — the same answer the rule above
// gives for any family with no series.
//
// # Catalogue families, and why one is here too
//
// R9.5's per-query family is not one of the eight. It is a catalogue family —
// top-N only, `metrics.topN` default 50, range 0..1000, N=0 disabling it — and
// requirements.md's own repo layout at :245 names this package "MetricHub: our
// registry, label contract, top-N (R9)", which is the spec assigning top-N to
// the hub rather than to a handler. IMPLEMENTATION.md §3.11 files the family's
// absence as "R14.7's inverse: a metric documented and absent", so composing it
// here closes the gap that sentence records.
//
// The body is A5's internal/store/perquery.go (3418da3), and metrics cannot
// import store: store imports api and api imports metrics, so that edge closes
// a cycle. The consumer-side interface is the answer — PerQueryFamily declares
// only the method store already has, so *store.Store satisfies it without
// importing anything, and the exposition keeps one composition point instead of
// the handler composing hub-plus-family, which is the split 0f4b154 removed.
//
// R9.2's configurable `metrics.prefix` is the config half of the same contract,
// and IMPLEMENTATION.md §3.9's R9.2/R9.3 row assigns that config key to this
// package. Until R11.8's flag/env/ConfigMap chain lands, the hub renders with
// collector.DefaultPrefix; Render's doc explains why the empty string is the way
// to ask for it rather than an unset field.
package metrics

import (
	"context"
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

// PerQueryFamily is R9.5's per-query family as its owner provides it — the one
// method internal/store/perquery.go:271 defines, declared here rather than
// imported because importing store from here closes the cycle
// api→metrics→store→api. Declaring an interface in the consuming package is the
// Go answer to that shape and it is the one that keeps R16.8 intact: the family
// body stays A5's, the hub stays the single composition point, and neither
// package gains a second copy of the render.
//
// The signature is store's exactly — ctx, topN, prefix — so *store.Store
// satisfies it with no adapter. topN and prefix are arguments rather than Hub
// fields for the reason store's own docblock gives ("the value belongs to the
// exposition, not to the database"); Render passes R9.5's defaults and the
// Option below is where a configured value arrives once R11.8's chain lands.
type PerQueryFamily interface {
	RenderPerQuery(ctx context.Context, topN int, prefix string) (string, error)
}

// Option configures a Hub beyond its two collector sources. Options exist so
// that adding a family never changes New's signature again: 0f4b154 shipped
// New(collectors, up), and every family this package gains since — R9.5's and
// R9.8's HTTP-duration one, with the store-size family still pending — would
// otherwise cost a signature change plus a call-site change in internal/api,
// which is how a one-line addition turns into a two-package commit.
type Option func(*Hub)

// WithPerQuery adds R9.5's per-query family to the hub. The zero value (no
// option passed) renders the family absent, which is the same rule the two
// collector sources follow when they have nothing to say.
func WithPerQuery(f PerQueryFamily) Option {
	return func(h *Hub) { h.perQuery = f }
}

// WithHTTPRequests adds R9.8's pgsi_http_request_duration_seconds family, the
// one R9.8 family this package renders itself (http.go) rather than importing
// from the collector package. A nil *HTTPRequests is legal and renders the
// family absent, so a server can pass the option before its middleware has
// served a request.
func WithHTTPRequests(m *HTTPRequests) Option {
	return func(h *Hub) { h.http = m }
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

	// perQuery is R9.5's catalogue family, supplied through WithPerQuery. A nil
	// interface — the state of a hub built by New alone — renders the family
	// absent rather than empty, and that is also the state of a server whose
	// store has not been constructed.
	perQuery PerQueryFamily

	// http is R9.8's pgsi_http_request_duration_seconds, supplied through
	// WithHTTPRequests. Unlike perQuery it is a concrete type, so its nil case
	// is handled by HTTPRequests.Render's own nil receiver rather than by a
	// check here — the same way collectors and up are handled above.
	http *HTTPRequests
}

// New returns the hub over the given sources. Both are nil-safe —
// *collector.Stats and *collector.UpTracker each define Render on a nil
// receiver, returning "" — so a server with no scheduler and no recorded
// collector still has a hub, and it renders exactly BuildInfo.
//
// Families that arrive from outside the collector package are passed as Options
// (WithPerQuery and WithHTTPRequests today, and the pending store-size family
// later), which is what lets this signature stay put while the hub grows.
func New(collectors *collector.Stats, up *collector.UpTracker, opts ...Option) *Hub {
	h := &Hub{collectors: collectors, up: up}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Render is RenderContext with a background context — the form every existing
// call site uses, kept as its own method so a caller with no context to hand
// (internal/api's metricsBody today, a test, a CLI dump) does not have to
// invent one. A caller that has a request context should call RenderContext
// with it: the per-query family reads the store, and a scrape whose client goes
// away should cancel that read rather than finish it.
func (h *Hub) Render() string { return h.RenderContext(context.Background()) }

// RenderContext concatenates the families this hub can render, in R9.8's listed
// order: build info, then the collector pair, then the scrape-target gauge, then
// the HTTP-duration family, then the catalogue families.
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
//
// A family that errors is absent too, and its error is not returned. Render's
// contract is a string because /metrics' contract is a body with a 200, and the
// only errors the one family that can fail returns are an out-of-range topN
// (a construction bug, caught by TestHubPerQueryFamilyGetsR9_5sDefaults) and a
// closed store (shutdown, where the endpoint is already failing readiness).
// Returning an error would make the handler choose between dropping the whole
// exposition and inventing a status code R9.1 does not define; omitting the
// family is the same call the nil sources already get.
func (h *Hub) RenderContext(ctx context.Context) string {
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
	if h.perQuery != nil {
		// R9.5's default N and R9.2's default prefix, both as arguments: the
		// constants are the single home of each default (collector.DefaultTopN,
		// collector.DefaultPrefix), so passing them here is not a second copy of
		// either number.
		if body, err := h.perQuery.RenderPerQuery(ctx, collector.DefaultTopN, ""); err == nil {
			b.WriteString(body)
		}
	}
	// R9.8's HTTP-duration family. No nil check: HTTPRequests.Render is defined
	// on a nil receiver and returns "", which is this package's answer for a
	// family with no series and the same shape the two collector sources get.
	b.WriteString(h.http.Render(""))
	return b.String()
}
