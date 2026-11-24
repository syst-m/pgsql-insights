package api

// R9.8's HTTP instrumentation middleware: the caller that feeds
// pgsi_http_request_duration_seconds (internal/metrics/http.go).
//
// Two things this has to get right and one it deliberately does not.
//
// The route label is the matched pattern, never the URL. That is why the
// function takes the *http.ServeMux rather than just the handler: on this
// toolchain the pattern is not on the request at all (http.Request has no
// RoutePath field — `r.RoutePath undefined` on go1.26.1), so the only way to
// learn it from outside the mux is the exported lookup ServeMux.Handler(r),
// which is what this wrapper calls. Without it the label would be r.URL.Path,
// and the API's own latency metric would gain a series per fingerprint it
// serves — 50 per cluster from R9.5's top-N alone — which is the cardinality
// failure R13.4's budget exists to prevent, caused by the metric that measures
// the API.
//
// Handler's first return value is the handler itself and the second is the
// pattern, and the pattern carries the method: "GET /api/v1/clusters", not
// "/api/v1/clusters". So routeFromPattern takes the path from the first '/'
// rather than trimming r.Method+" " off the front — Go answers a HEAD request
// with the *GET* pattern, and a method trim would then record the route
// "GET /api/v1/clusters" for a HEAD. An unmatched request gets the empty
// pattern, which Observe records as "unmatched": a 404 still costs the process
// time, and a spike in unmatched requests is exactly what a renamed route looks
// like.
//
// The status code comes from the wrapper below, not from a handler contract:
// net/http writes 200 implicitly on the first Write, so a handler that never
// calls WriteHeader reports nothing to read. The wrapper records the first
// WriteHeader call and defaults to 200, which is the same answer net/http gives.
//
// Where it runs is server.go's to say, and as of aee9046 it does: Handler's
// innermost handler is `s.instrumentRequests(mux, s.cfg.HTTPMetrics)`, with
// CORS and auth wrapping it. That call site and the comment above it in
// server.go are the authority on the placement — including why putting this
// around the finished chain instead would route requests past auth, and why an
// auth-rejected 401 therefore never reaches this wrapper and is not recorded.
// An earlier draft of this paragraph restated that reasoning in prose here,
// which is the second contract this paragraph's own rule warns about: a comment
// in this file describing a line in a file A1 owns has no checker. The sentence
// is now a dated pointer to the commit rather than a paraphrase of its reasoning.

import (
	"net/http"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/metrics"
)

// instrumentRequests wraps mux and returns the handler that serves through it,
// recording every request into m.
//
// It takes the mux explicitly, rather than a handler, for the route-label
// reason above: the pattern set is the mux's and the label has to come from it.
// Serving through mux after asking it a question costs a second routing pass —
// a tree walk with no side effects — which is cheaper than the alternative of
// wrapping every registered handler individually.
//
// m may be nil — metrics.HTTPRequests.Observe is defined on a nil receiver and
// drops the observation — so the middleware can be wired before the server has a
// family to record into, and the family's absence from /metrics is then the
// documented "no series yet" answer rather than a special case here.
func (s *Server) instrumentRequests(mux *http.ServeMux, m *metrics.HTTPRequests) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		route := routeFromPattern(pattern)

		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		mux.ServeHTTP(rw, r)
		m.Observe(r.Method, route, rw.status, time.Since(start).Seconds())
	})
}

// routeFromPattern returns the path part of a ServeMux pattern.
//
// A pattern is "[METHOD ][HOST ]/path" (net/http's documented form), so the
// path is everything from the first '/' onward and the method and host
// prefixes, if present, are dropped. The empty pattern — what Handler returns
// for a request the mux would 404 — stays empty here and becomes "unmatched" in
// metrics.HTTPRequests.Observe, which is the single place that name is decided
// (see TestHTTPRouteNeverEmpty).
func routeFromPattern(pattern string) string {
	if i := strings.IndexByte(pattern, '/'); i >= 0 {
		return pattern[i:]
	}
	return pattern
}

// statusWriter is the response recorder: it forwards everything and remembers
// the first status code.
//
// Only the first is kept because net/http treats a second WriteHeader as a bug
// and logs it, and a recorder that kept the last would silently disagree with
// what the client received. The zero value is not usable — a handler that never
// calls WriteHeader gets the implicit 200 — so instrumentRequests initialises
// the field instead of relying on the zero value.
type statusWriter struct {
	http.ResponseWriter
	status int
	// wrote guards the implicit-WriteHeader path: the first Write must record a
	// 200 exactly once, and every later Write must not re-record it.
	wrote bool
}

// WriteHeader records the status and forwards it.
func (w *statusWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write records the implicit 200 on the first call, then forwards.
func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards the Flusher interface when the wrapped writer has it, and is
// absent when it does not. Without this, wrapping would downgrade every
// response to a non-flushable one, and pprof's streaming profile (R8.7's
// /debug/pprof/profile) is the handler in this route table that streams.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
