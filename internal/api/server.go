// Package api is pgsi's HTTP surface (R8.1).
//
// Phase 0 shipped the four endpoints the packaging requirements need; Phase 1b
// added the SPA at `/` (R10.1). Together they satisfy R11.14's deploy-and-
// observe acceptance ("GET /healthz answers through the Service") with real code
// rather than a stub binary:
//
//	GET /healthz          liveness probe target (R8.7, R11.2 httpGet probes)
//	GET /readyz           readiness probe target (R8.7)
//	GET /api/v1/version   build metadata (R11.3)
//	GET /metrics          pgsi_build_info only (R11.3; the full R9.2 catalogue lands in Phase 2)
//	GET /                 the embedded SPA and its assets (R10.1 — see spa.go)
//
// R10.16's client-side routes (/c/<ns>/<name>) also answer from `GET /`, which
// returns the shell for any non-API path so the router can render the view.
//
// The R8.2 resource catalogue, the auth gate (R8.8) and the PromQL pair
// (R8.9/R8.10) are Phase 1 and Phase 5 work. R14.7 requires every route in
// code to be documented, which is what this comment and the IMPLEMENTATION.md
// endpoint table are for until api/openapi.yaml is generated in Phase 1.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/frankellis/pgsql-insights/internal/version"
)

// Config configures the HTTP server. The full flag/env/ConfigMap precedence
// chain is R11.8 (Phase 2); Phase 0 takes only the listen address and the
// shutdown budget.
type Config struct {
	// Listen is the address the server binds, e.g. ":8080" (R8.7).
	Listen string
	// ShutdownTimeout bounds the graceful-shutdown drain (R13.8). Zero means 10s.
	// The chart keeps this below the pod's terminationGracePeriodSeconds.
	ShutdownTimeout time.Duration
}

// Server serves the documented endpoints.
type Server struct {
	cfg   Config
	ready atomic.Bool
	http  *http.Server
}

// New builds a Server without binding the socket.
func New(cfg Config) *Server {
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 10 * time.Second
	}
	s := &Server{cfg: cfg}
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
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	// R10.1: the SPA is served by this binary. `GET /` is the mux's root
	// pattern — it matches every GET the patterns above do not, because a
	// trailing-slash pattern is a subtree match.
	mux.HandleFunc("GET /", s.handleSPA)
	return mux
}

// Run binds the socket, serves until ctx is cancelled, then drains.
// It marks the server ready once the listener is up; Phase 1 (R8.7) moves
// /readyz behind real readiness (kube client + first discovery pass).
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Listen, err)
	}
	s.ready.Store(true)

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
		s.ready.Store(false)
		return err
	case <-ctx.Done():
		// R13.8: stop advertising readiness, then drain in-flight requests.
		s.ready.Store(false)
		shutCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		return s.http.Shutdown(shutCtx)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

// VersionResponse is the body of GET /api/v1/version (R11.3).
type VersionResponse struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(VersionResponse{
		Version: version.Version,
		Commit:  version.Commit,
	})
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
