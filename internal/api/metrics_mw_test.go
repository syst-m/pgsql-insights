package api

// Tests for the R9.8 HTTP instrumentation middleware (metrics_mw.go).
//
// These are in-package rather than api_test because instrumentRequests is
// unexported — which is also what lets the test call it directly instead of
// reaching it through server.go's Handler(), the one line that does not call it
// yet. When A1 wires it, these tests keep covering the middleware itself and
// server_test.go's existing routes get instrumented for free.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/metrics"
)

// serve builds a mux from the given patterns, wraps it with the middleware over
// a fresh family, serves one request, and returns the recorder plus the family's
// rendered exposition. Serving through a real *http.ServeMux rather than a
// hand-built handler is the point: the route label comes from the mux's pattern
// set, so a test that skipped the mux would not be testing the label at all.
func serve(t *testing.T, m *metrics.HTTPRequests, routes map[string]http.HandlerFunc, method, target string) (*httptest.ResponseRecorder, string) {
	t.Helper()

	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.Handle(pattern, h)
	}
	h := (&Server{}).instrumentRequests(mux, m)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	h.ServeHTTP(rec, req)

	return rec, m.Render("")
}

// static returns a handler that answers with code and body.
func static(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func TestRouteFromPattern(t *testing.T) {
	// The shapes net/http's pattern grammar produces: "[METHOD ][HOST ]/path".
	// The empty pattern is what ServeMux.Handler returns for a request it would
	// 404, and it stays empty here — metrics.HTTPRequests turns it into
	// "unmatched", and this file deliberately does not duplicate that name.
	cases := map[string]string{
		"GET /api/v1/clusters":              "/api/v1/clusters",
		"GET /api/v1/insights/queries/{fp}": "/api/v1/insights/queries/{fp}",
		"/api/v1/version":                   "/api/v1/version",
		"GET example.com/api/v1/clusters":   "/api/v1/clusters",
		"POST /api/v1/metrics/query":        "/api/v1/metrics/query",
		"":                                  "",
		"GET ":                              "GET ",
		"PUT /api/v1/clusters/{namespace}/{name}": "/api/v1/clusters/{namespace}/{name}",
	}
	for pattern, want := range cases {
		if got := routeFromPattern(pattern); got != want {
			t.Errorf("routeFromPattern(%q) = %q, want %q", pattern, got, want)
		}
	}
}

// TestInstrumentRecordsRouteMethodAndCode is the middleware's whole contract in
// one pass: two routes, two status codes, and the family that should exist
// afterwards. The _count lines are asserted verbatim because they are the one
// part of a histogram line whose value is deterministic for one request — _sum
// is a wall-clock duration and the bucket it lands in depends on machine speed.
func TestInstrumentRecordsRouteMethodAndCode(t *testing.T) {
	m := metrics.NewHTTPRequests()
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/clusters": static(http.StatusCreated, "clusters"),
		"GET /api/v1/version":  func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("v1")) },
	}

	rec, rendered := serve(t, m, routes, "GET", "/api/v1/clusters")
	if rec.Code != http.StatusCreated || rec.Body.String() != "clusters" {
		t.Errorf("wrapper changed the response: code %d body %q, want 201 %q", rec.Code, rec.Body.String(), "clusters")
	}
	want := `pgsi_http_request_duration_seconds_count{code="201",method="GET",route="/api/v1/clusters"} 1`
	if !strings.Contains(rendered, want) {
		t.Errorf("missing series %s\n%s", want, rendered)
	}

	rec, rendered = serve(t, m, routes, "GET", "/api/v1/version")
	if rec.Code != http.StatusOK || rec.Body.String() != "v1" {
		t.Errorf("wrapper changed the response: code %d body %q, want 200 %q", rec.Code, rec.Body.String(), "v1")
	}
	want = `pgsi_http_request_duration_seconds_count{code="200",method="GET",route="/api/v1/version"} 1`
	if !strings.Contains(rendered, want) {
		t.Errorf("missing series %s\n%s", want, rendered)
	}
}

// TestInstrumentStatusIsFirstWriteHeader pins the recorder's rule: net/http
// treats a second WriteHeader as a bug, and a recorder that kept the last would
// report a status the client never saw.
func TestInstrumentStatusIsFirstWriteHeader(t *testing.T) {
	m := metrics.NewHTTPRequests()
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/clusters": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.WriteHeader(http.StatusInternalServerError) // superfluous; net/http logs it
			_, _ = w.Write([]byte("x"))
		},
	}
	if _, rendered := serve(t, m, routes, "GET", "/api/v1/clusters"); !strings.Contains(
		rendered, `pgsi_http_request_duration_seconds_count{code="404",method="GET",route="/api/v1/clusters"} 1`) {
		t.Errorf("second WriteHeader won\n%s", rendered)
	}
}

// TestInstrumentUnmatchedRoute covers the request that matches nothing: Go's mux
// answers 405 here (the path is registered, the method is not) and
// ServeMux.Handler returns the empty pattern. Both halves matter — a 404 or 405
// still costs the process time, and a spike in unmatched requests is what a
// renamed route looks like.
func TestInstrumentUnmatchedRoute(t *testing.T) {
	m := metrics.NewHTTPRequests()
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/clusters": static(http.StatusOK, "clusters"),
	}
	rec, rendered := serve(t, m, routes, "DELETE", "/api/v1/clusters")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mux returned %d for a method mismatch, want 405", rec.Code)
	}
	want := `pgsi_http_request_duration_seconds_count{code="405",method="DELETE",route="unmatched"} 1`
	if !strings.Contains(rendered, want) {
		t.Errorf("missing series %s\n%s", want, rendered)
	}
}

// TestInstrumentHeadUsesTheGetPatternRoute is the regression test for the trim
// this replaced. Go answers a HEAD request with the *GET* pattern, so a route
// derived by trimming r.Method+" " off the front produced
// "GET /api/v1/clusters" as the route label for a HEAD request — one series per
// route per method the mux happens to answer, which is the cardinality this
// file's whole route-label rule exists to avoid.
func TestInstrumentHeadUsesTheGetPatternRoute(t *testing.T) {
	m := metrics.NewHTTPRequests()
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/clusters": static(http.StatusOK, "clusters"),
	}
	if _, rendered := serve(t, m, routes, "HEAD", "/api/v1/clusters"); !strings.Contains(
		rendered, `pgsi_http_request_duration_seconds_count{code="200",method="HEAD",route="/api/v1/clusters"} 1`) {
		t.Errorf("HEAD route not derived from the GET pattern\n%s", rendered)
	}
}

// TestInstrumentNilFamilyServesNormally is the wiring state A1 can adopt today:
// Observe is nil-safe, so the middleware can sit in the chain before the server
// has a family, and the family's absence from /metrics stays the documented
// "no series yet" answer rather than a special case in the handler.
func TestInstrumentNilFamilyServesNormally(t *testing.T) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/clusters": static(http.StatusOK, "clusters"),
	}
	rec, _ := serve(t, nil, routes, "GET", "/api/v1/clusters")
	if rec.Code != http.StatusOK || rec.Body.String() != "clusters" {
		t.Errorf("nil family changed the response: code %d body %q", rec.Code, rec.Body.String())
	}
}

// TestInstrumentKeepsFlusher: wrapping a ResponseWriter in a struct that does
// not implement http.Flusher silently downgrades every response, and R8.7's
// /debug/pprof/profile is the streaming handler in this route table. The
// handler asks the writer it was given, so the answer is the wrapper's.
func TestInstrumentKeepsFlusher(t *testing.T) {
	m := metrics.NewHTTPRequests()
	routes := map[string]http.HandlerFunc{
		"GET /debug/pprof/profile": func(w http.ResponseWriter, r *http.Request) {
			_, ok := w.(http.Flusher)
			if !ok {
				t.Error("wrapped writer is not an http.Flusher")
			}
			w.WriteHeader(http.StatusOK)
		},
	}
	if _, rendered := serve(t, m, routes, "GET", "/debug/pprof/profile"); !strings.Contains(
		rendered, `pgsi_http_request_duration_seconds_count{code="200",method="GET",route="/debug/pprof/profile"} 1`) {
		t.Errorf("missing series\n%s", rendered)
	}
}
