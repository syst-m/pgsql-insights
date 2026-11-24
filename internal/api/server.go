// Package api is pgsi's HTTP surface (R8.1).
//
// Phase 0 shipped the four endpoints the packaging requirements need; Phase 1b
// added the SPA at `/` (R10.1). Together they satisfy R11.14's deploy-and-
// observe acceptance ("GET /healthz answers through the Service") with real code
// rather than a stub binary:
//
//	GET /healthz          liveness probe target (R8.7, R11.2 httpGet probes)
//	GET /readyz           readiness probe target (R8.7)
//	GET /api/v1/clusters  the fleet inventory (R8.2; see clusters.go)
//	GET /api/v1/insights/queries/series  batched per-minute series for up to 50
//	                                    fingerprints (R8.11; see series.go)
//	GET /api/v1/version   build metadata (R11.3)
//	GET /metrics          the R9.8 hub (internal/metrics/):
//	                      R11.3's pgsi_build_info, the R2.2 pair,
//	                      pgsi_up
//	GET /debug/pprof/*    Go runtime profiles (R8.7; follows --auth per R8.9)
//	GET /                 the embedded SPA and its assets (R10.1 — see spa.go)
//
// R10.16's client-side routes (/c/<ns>/<name>) also answer from `GET /`, which
// returns the shell for any non-API path so the router can render the view.
//
// The auth gate is in auth.go (R8.8) and the CORS policy in cors.go (R8.9);
// both are mounted by Handler() below. The R8.2 resource catalogue and the
// PromQL pair are Phase 1 and Phase 5 work. R14.7 requires every route in code
// to be documented: openapi.yaml is that document, and this comment lists the
// routes so the package still reads standalone.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/discovery"
	"github.com/frankellis/pgsql-insights/internal/metrics"
	"github.com/frankellis/pgsql-insights/internal/version"
)

// Fleet is the read side of discovery, as the API needs it: the current
// inventory plus the readiness flag R8.7 makes both /readyz and the R8.5
// ErrNotReady path answer from. *discovery.Discoverer satisfies it, and so does
// the stub in clusters_test.go, which is the point of naming the interface here
// rather than taking the concrete type — the handler is testable without a kube
// client, and Phase 2's store-backed fleet needs no change to be plugged in.
type Fleet interface {
	Inventory() discovery.Inventory
	Ready() bool
}

// Config configures the HTTP server. The full flag/env/ConfigMap precedence
// chain is R11.8 (Phase 2); Phase 0 takes only the listen address and the
// shutdown budget.
type Config struct {
	// Listen is the address the server binds, e.g. ":8080" (R8.7).
	Listen string
	// ShutdownTimeout bounds the graceful-shutdown drain (R13.8). Zero means 10s.
	// The chart keeps this below the pod's terminationGracePeriodSeconds.
	ShutdownTimeout time.Duration

	// Auth is R8.8's per-route-group policy. Nil means every route is
	// unauthenticated, which is what Phase 0's Config (and its tests) had
	// before the auth flag existed — so leaving it unset is not a behaviour
	// change. cmd/pgsi builds it from --auth/--auth-metrics/--auth-token*.
	Auth *Authz
	// CORS is R8.9. Nil, or a config with no allowed origins, adds no headers:
	// R8.6 serves the SPA from this same binary, so the default deployment is
	// same-origin and needs none. See cors.go for why "*" is not the default.
	CORS *CORSConfig

	// PromQL is R8.3's pair: GET|POST /api/v1/metrics/query and
	// GET|POST /api/v1/metrics/system. It registers its own four routes, so
	// Handler mounts it rather than listing them (see Mount). Nil leaves the
	// four documented routes unregistered, which is the honest state for a
	// process that has no catalogue and no Prometheus to proxy through — an
	// unknown path answers 404, which is what R8.3 says a query nothing can
	// answer deserves. cmd/pgsi builds it from --prometheus-url.
	PromQL *PromQL

	// Series is R8.11's batched series route (GET /api/v1/insights/queries/series,
	// implemented in series.go). Nil selects the empty source, so the route still
	// answers `data: []` instead of 404 — the document lists it, and R14.7 forbids
	// a documented route that 404s. Phase 3's per-minute store replaces the default
	// by assigning this field; nothing else in the wiring changes when it does.
	Series QuerySeriesSource

	// Queries is R8.2's query list (GET /api/v1/insights/queries, implemented in
	// queries.go). Nil selects the empty source, so the route answers an empty
	// page instead of 404 — the same rule Series follows and for the same reason:
	// the document lists the path, and R14.7 forbids a documented route that 404s.
	// Phase 3's query store replaces the default by assigning this field.
	Queries QueryListSource

	// QueryDetail is R10.7's query detail (GET /api/v1/insights/queries/
	// {fingerprint}, implemented in query_detail.go). Nil selects the not-found
	// source, so the route answers 404 for every fingerprint instead of
	// panicking or inventing a body — the same rule Queries follows, with the
	// answer that route's shape allows: a list can be empty, an object cannot.
	//
	// It is a separate field from Queries because R8.2 has two routes here and
	// QueryDetailSource is a separate interface, so a store can serve the list
	// before the detail (and A5's Phase 3 store, which is being written now,
	// assigns whichever of the two it has implemented without stubbing the other).
	QueryDetail QueryDetailSource

	// Metrics is the collector self-observability state whose two R2.2 families
	// (pgsi_collector_duration_seconds, pgsi_collector_errors_total) /metrics
	// renders alongside pgsi_build_info — R9.8's composition, and the call
	// BuildInfoMetric's own comment was written for ("Exported so the Phase 2
	// MetricHub renders the same line rather than a second copy of it").
	//
	// New defaults it to collector.NewStats(), which is not the same as leaving
	// it nil and letting the handler cope: (*Stats).Render locks s.mu, so a nil
	// *Stats panics where the zero value answers "". It is also not
	// &collector.Stats{} — the zero value's prefix is empty, so it would render
	// `collector_duration_seconds`, and R9.4's prefix rule names `pgsi_` as the
	// only correct spelling. NewStats sets it; that is why the default is the
	// constructor and not the literal.
	//
	// A process that does run a scheduler assigns that scheduler's own *Stats
	// here, so /metrics and the collection loop read one value — the same
	// single-source rule R8.7(i) states for readiness. The import itself is the
	// one internal/collector/conn.go:115
	// predicts ("internal/api … will import the collectors at step 4"), and
	// naming the type here rather than an interface is deliberate: Stats has no
	// second implementation to plan for, and an interface with one implementation
	// and no test double is a second name for a concrete type.
	Metrics *collector.Stats
	// Scheduler is the Phase 2 collection loop, and the only source of
	// pgsi_scrape_target_up (R9.4, R9.12) — the metric is per-target state, so it
	// lives with the loop that observes it and is read through Scheduler.Up().
	//
	// Unlike Metrics, nil needs no default: (*Scheduler).Up is defined on a nil
	// receiver and returns nil, and (*UpTracker).Render is defined on a nil
	// tracker and returns "". A server with no scheduler therefore renders the
	// family as absent rather than panicking or emitting an empty HELP/TYPE pair
	// for a set of targets it has never seen.
	Scheduler *collector.Scheduler
	// PerQueryMetrics is R9.5's per-query family, the fourth family the R9.8 hub
	// composes (internal/metrics's PerQueryFamily, implemented by
	// internal/store/perquery.go's RenderPerQuery). It is an interface rather than
	// a *store.Store for the same reason PromQL is one and Metrics is not: PromQL
	// has a second implementation to plan for (the catalogue-only source vs the
	// Prometheus proxy) and a test double, and so does this — the empty source
	// queries_test can hand it, versus a hard dependency on the SQLite store in
	// every api test.
	//
	// Nil renders the family absent, which is the hub's rule for every optional
	// source ("a source with nothing to say contributes no bytes rather than an
	// empty family"), so this field needs no default in New the way Metrics does.
	//
	// The hub's check is `h.perQuery != nil`, so a typed nil
	// (`cfg.PerQueryMetrics = (*store.Store)(nil)`) passes it and the call reaches
	// a nil receiver — a hazard A2 raised and A5 closed at d2415b1, where
	// RenderPerQuery now opens with `if s == nil { return "", nil }`, matching
	// collector/metrics.go's Render and collector/up.go's. All three of the hub's
	// sources are therefore nil-safe, so both nils — the nil interface and a typed
	// nil — render the family absent, and the assignment cmd/pgsi makes has no
	// constraint on which one it uses.
	PerQueryMetrics metrics.PerQueryFamily
	// HTTPMetrics is R9.8's pgsi_http_request_duration_seconds family, the fifth
	// one the hub composes (internal/metrics's HTTPRequests, fed by
	// internal/api/metrics_mw.go's instrumentRequests and rendered by
	// metrics.WithHTTPRequests).
	//
	// It is a concrete pointer rather than an interface because there is only one
	// implementation to plan for — the reason Metrics is a *collector.Stats and
	// PromQL is an interface — and it is a field rather than a value for the same
	// reason: a caller that has not wired it gets a family that is absent from
	// /metrics, which is the documented answer for a source with nothing to say,
	// not a broken one.
	//
	// New defaults it to metrics.NewHTTPRequests(), so a server built by New
	// always records. The type is nil-safe on both paths (Observe drops, Render
	// returns ""), so the default is a convenience rather than a requirement and a
	// caller can still set it to nil to opt out.
	HTTPMetrics *metrics.HTTPRequests

	// Fleet is the inventory /api/v1/clusters serves (R8.2) and, through
	// Readiness below, the thing /readyz reports on. In `pgsi serve` it is the
	// *discovery.Discoverer; nil means this process runs no discovery — Phase 0's
	// shape and D9's reserved `api` role — so the route answers 503 NotReady
	// rather than rendering a fleet it never collected.
	Fleet Fleet
	// Readiness is R8.7's single readiness signal, read by both /readyz and the
	// API's R8.5 ErrNotReady path. Nil makes New derive it from Fleet (see
	// readiness.go), which is the wiring that stops the probe and the API from
	// disagreeing; set it explicitly only to add a subsystem's source.
	Readiness *Readiness

	// Log is the structured logger (R13.7). Nil discards, so a caller that has
	// not wired internal/logx yet still gets a working Server; cmd/pgsi builds it
	// from --log-level and --log-field.
	Log *slog.Logger
}

// Server serves the documented endpoints.
type Server struct {
	cfg  Config
	http *http.Server
	// readiness is cfg.Readiness (never nil after New), kept as a field so
	// handleReadyz and handleClusters read the identical value (R8.7(i)).
	readiness *Readiness
	// log is cfg.Log (never nil after New).
	log *slog.Logger
	// metrics is cfg.Metrics (never nil after New, for the reason its Config doc
	// gives), kept as a field for the same reason readiness is: the handler reads
	// the defaulted value instead of re-deriving it on every request.
	metrics *collector.Stats
}

// New builds a Server without binding the socket.
func New(cfg Config) *Server {
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 10 * time.Second
	}
	// The readiness derivation is the whole of R8.7(i) in three lines: a server
	// given a fleet gets one whose sources are that fleet's Ready(), so the probe
	// and the API cannot report different answers, and a server given no fleet
	// gets a source-less one that is ready once it is listening (R8.7(ii): only
	// the subsystems this process run).
	if cfg.Readiness == nil {
		if cfg.Fleet != nil {
			cfg.Readiness = NewReadiness(cfg.Fleet.Ready)
		} else {
			cfg.Readiness = NewReadiness()
		}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	// The R9.8 default, applied here rather than at the handler for the reason
	// cfg.Metrics documents: the type is not nil-safe, and a default that lives
	// in one place cannot be forgotten by the next caller.
	if cfg.Metrics == nil {
		cfg.Metrics = collector.NewStats()
	}
	// The same one-place default for the HTTP family. Unlike Metrics this type is
	// nil-safe, so the default is not preventing a panic — it is making sure the
	// family exists for every server built by New, so a deployment cannot ship the
	// middleware without shipping the thing the middleware records into.
	if cfg.HTTPMetrics == nil {
		cfg.HTTPMetrics = metrics.NewHTTPRequests()
	}
	s := &Server{cfg: cfg, readiness: cfg.Readiness, log: cfg.Log, metrics: cfg.Metrics}
	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Handler returns the routed handler, separated from Run so tests can drive it
// with httptest instead of binding a port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /api/v1/clusters", s.handleClusters)
	// R8.2's `/clusters/{ns}/{name}` (cluster_detail.go) — Phase 5's first new
	// route. Same inline-listing reason as the series route below, and the path
	// comes from ClusterDetailPath so the registration and api/openapi.yaml
	// cannot spell it two ways (R14.7).
	mux.HandleFunc("GET "+ClusterDetailPath, s.handleClusterDetail)
	// R8.2's query list (queries.go). Listed inline for the same reason as the two
	// routes above it — one pattern, one dependency — and listed *before* the
	// series route so the two `/insights/queries` patterns read in path order.
	// The path comes from QueriesPath, which is also the string A4's
	// web/src/routes.ts:182 carries as its endpoint.
	mux.HandleFunc("GET "+QueriesPath, s.handleQueries)
	// R8.11's batched series route (series.go). Listed inline rather than mounted
	// through a subsystem like PromQL/CORS, because it is one pattern with one
	// dependency — and the path comes from the package's QuerySeriesPath const so
	// the registration and api/openapi.yaml cannot spell it two ways.
	mux.HandleFunc("GET "+QuerySeriesPath, s.handleQuerySeries)
	// R10.7's query detail (query_detail.go), listed after the two literals above
	// because that is the order the mux resolves them in: a literal pattern beats
	// a wildcard, so /insights/queries/series stays the batched route's and
	// /insights/queries/{fingerprint} is this one's. The path comes from
	// QueryDetailPath, which is also the string A4's web/src/routes.ts:193 carries
	// as the query-detail page's endpoint (R14.7).
	mux.HandleFunc("GET "+QueryDetailPath, s.handleQueryDetail)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	// R8.3's pair registers four patterns of its own (GET and POST on each of
	// /api/v1/metrics/query and /api/v1/metrics/system), so it mounts here
	// instead of being listed — same shape as CORS and Auth: an unconfigured
	// subsystem is not mounted at all, rather than mounted and answering wrong.
	if s.cfg.PromQL != nil {
		s.cfg.PromQL.Mount(mux)
	}
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	// R8.7's third surface, and R8.9's ruling that it follows --auth (it is
	// introspection, not a probe, so auth.go's default branch puts it in
	// GroupAPI). These five are what net/http/pprof registers on the default
	// mux; listing them here rather than mounting a second ServeMux keeps the
	// whole route table readable from one function.
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	// R10.1: the SPA is served by this binary. `GET /` is the mux's root
	// pattern — it matches every GET the patterns above do not, because a
	// trailing-slash pattern is a subtree match.
	mux.HandleFunc("GET /", s.handleSPA)

	// Middleware order is CORS-then-auth, and the reason is a browser detail:
	// a CORS preflight (OPTIONS) carries no Authorization header, so if auth
	// ran first every preflight would answer 401 and the real request would
	// never be issued. cors.go returns `next` unchanged when unconfigured, so
	// this line is a no-op for a same-origin deployment.
	//
	// The innermost handler is the R9.8 instrumentation rather than the bare mux,
	// and the position is not free. instrumentRequests serves through mux by
	// construction (it needs the mux to ask which pattern matched, because the
	// route label has to be the pattern and not the URL), so wrapping the finished
	// chain instead of the mux would route every request past auth. Inside auth is
	// the consequence: a 401 answered by R8.9's middleware never reaches this
	// wrapper and so is not recorded — correct on the merits, since the response
	// came from auth and has no route latency to attribute to the pattern it was
	// addressed to.
	var h http.Handler = s.instrumentRequests(mux, s.cfg.HTTPMetrics)
	if s.cfg.CORS != nil {
		h = s.cfg.CORS.Middleware(h)
	}
	// auth.go's middleware is per-route-group, so this single wrap covers
	// R8.8's whole surface including "--auth-metrics=none so Prometheus need
	// not hold a token" (R8.9). A nil Authz returns next unchanged.
	h = s.cfg.Auth.Middleware(h)
	return h
}

// Run binds the socket, serves until ctx is cancelled, then drains.
//
// Readiness is not Run's to decide (R8.7(i)): the socket being up is only the
// process's own contribution to it, and what the probe reports is the Readiness
// New assembled — for `pgsi serve`, discovery's first sync. Run tells that value
// when the socket is bound and when it starts draining (R13.8), which is the
// half of readiness the HTTP server actually owns. Before this, Run's
// `ready.Store(true)` *was* the readiness, which is how a pod passed its probe
// while its fleet was empty.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Listen, err)
	}
	s.readiness.SetListening(true)
	// R13.7: the startup line carries the readiness verdict so a pod that is
	// serving-but-not-ready is distinguishable from the logs alone, without
	// curling the probe.
	s.log.Info("serving", "listen", s.cfg.Listen, "ready", s.readiness.Ready())

	errCh := make(chan error, 1)
	go func() {
		err := s.http.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		s.readiness.SetListening(false)
		if err != nil {
			s.log.Error("server stopped", "error", err.Error())
		} else {
			s.log.Info("server stopped")
		}
		return err
	case <-ctx.Done():
		// R13.8: stop advertising readiness, then drain in-flight requests.
		// Clearing it first is what makes the kubelet pull the pod from the
		// Service endpoints while the drain runs (R8.9's unconditional probe
		// exemption is what lets it do so without a token).
		s.readiness.SetListening(false)
		s.log.Info("shutting down", "timeout", s.cfg.ShutdownTimeout.String())
		shutCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		// The fresh background context is deliberate, not a dropped ctx: R13.8's
		// drain must outlive the context that just fired (ctx is already Done, so
		// deriving from it would abort the drain at once), and Shutdown's own
		// deadline is the budget for it.
		//nolint:contextcheck // intentional context.Background() for the R13.8 drain window
		if err := s.http.Shutdown(shutCtx); err != nil {
			s.log.Error("drain", "error", err.Error())
			return err
		}
		return nil
	}
}

// handleHealthz answers R8.7's liveness question — "is the process up and the
// mux serving" — and nothing else. If a request reached this handler, the answer
// is yes, so it is a constant 200 with no reference to readiness: R8.7(iii)
// splits the two probes precisely because folding readiness in here would make a
// pod that is up-but-not-ready look dead and get restarted for no reason.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleReadyz answers R8.7's readiness question from the single Readiness value
// the API's R8.5 ErrNotReady path also answers from (R8.7(i)). R8.9 makes this
// route's answer load-bearing: it is exempt from auth under every --auth value,
// so it is what the kubelet and the Service both consult, and a 200 here is a pod
// receiving traffic.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.readiness.Ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

// logDebug emits a debug-level structured line (R13.7's level-configurable
// logging) from alternating key/value pairs, so handlers do not each re-derive
// that slog wants pairs.
func (s *Server) logDebug(msg string, kv ...any) { s.log.Debug(msg, kv...) }

// VersionResponse is the body of GET /api/v1/version (R11.3).
type VersionResponse struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// handleVersion answers R11.3's third surface: the running binary's own
// version/commit pair, the same two ldvar values the OCI label and
// pgsi_build_info carry (see metrics.BuildInfo in internal/metrics/ and the D6
// note in IMPLEMENTATION.md §5 for how CI asserts they agree).
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, VersionResponse{Version: version.Version, Commit: version.Commit})
}

// handleMetrics is R9.1's exposition surface: the R9.8 hub's body, plus the
// content type and status the endpoint contract requires.
//
// The body is metrics.Hub's, composed in internal/metrics/metrics.go. This
// handler used to build that string itself, which is what the exported
// BuildInfoMetric it once existed for ("Exported so the Phase 2 MetricHub
// renders the same line rather than a second copy of it"); the hub has landed, so
// the composition lives there and this handler is the HTTP half only.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.metricsBody(r.Context())))
}

// metricsBody is the one call site connecting this server to the R9.8 hub. The
// hub composes five families (internal/metrics/metrics.go's RenderContext: the
// build info, the collector duration/error pair, the scrape-target gauge,
// R9.5's per-query family, and R9.8's own HTTP-request family) and this server
// supplies four of them — its own *collector.Stats, its scheduler's
// *collector.UpTracker, cfg.PerQueryMetrics, and cfg.HTTPMetrics — as values
// rather than as rendered strings, which is what keeps the server owning its
// sources and the hub owning the composition, the part that stops the move being
// a rewrite of the Config fields.
//
// The ctx is the request's, threaded to metrics.RenderContext rather than
// dropped on Render()'s context.Background() floor: one of the five families now
// reads the SQLite store, so a scrape whose client disconnects mid-render can
// cancel that read instead of finishing a query for a body nobody is reading.
func (s *Server) metricsBody(ctx context.Context) string {
	return metrics.New(s.metrics, s.cfg.Scheduler.Up(),
		metrics.WithPerQuery(s.cfg.PerQueryMetrics),
		metrics.WithHTTPRequests(s.cfg.HTTPMetrics)).RenderContext(ctx)
}
