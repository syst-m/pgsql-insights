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
//	GET /metrics          pgsi_build_info only (R11.3; the full R9.2 catalogue lands in Phase 2)
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

	"github.com/frankellis/pgsql-insights/internal/discovery"
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
	s := &Server{cfg: cfg, readiness: cfg.Readiness, log: cfg.Log}
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
	// R8.11's batched series route (series.go). Listed inline rather than mounted
	// through a subsystem like PromQL/CORS, because it is one pattern with one
	// dependency — and the path comes from the package's QuerySeriesPath const so
	// the registration and api/openapi.yaml cannot spell it two ways.
	mux.HandleFunc("GET "+QuerySeriesPath, s.handleQuerySeries)
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
	var h http.Handler = mux
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
// pgsi_build_info carry (see BuildInfoMetric below and the D6 note in
// IMPLEMENTATION.md §5 for how CI asserts they agree).
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, VersionResponse{Version: version.Version, Commit: version.Commit})
}

// BuildInfoMetric renders pgsi_build_info (R11.3, R9.8). Exported so the
// Phase 2 MetricHub renders the same line rather than a second copy of it.
func BuildInfoMetric() string {
	return "# HELP pgsi_build_info Build metadata of the running pgsi binary.\n" +
		"# TYPE pgsi_build_info gauge\n" +
		fmt.Sprintf("pgsi_build_info{version=%q,commit=%q} 1\n", version.Version, version.Commit)
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(BuildInfoMetric()))
}
