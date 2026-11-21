package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/version"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New(Config{Listen: "127.0.0.1:0"})
}

func do(t *testing.T, s *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	// R11.2: the distroless image has no shell, so the pod's probes are httpGet
	// — /healthz must answer 200 on a plain GET.
	rec := do(t, newTestServer(t), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "ok\n" {
		t.Fatalf("GET /healthz body = %q, want %q", body, "ok\n")
	}
}

func TestReadyzGatedOnReadyFlag(t *testing.T) {
	s := newTestServer(t)
	// Before Run() binds a socket the server is not ready, so the probe fails
	// closed: a pod that has not started serving must not be added to the
	// Service endpoints (R8.7).
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz before start = %d, want 503", rec.Code)
	}
	s.ready.Store(true)
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("GET /readyz after start = %d, want 200", rec.Code)
	}
	// R13.8: shutting down clears readiness so the pod drains.
	s.ready.Store(false)
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz after shutdown = %d, want 503", rec.Code)
	}
}

func TestVersionEndpoint(t *testing.T) {
	// R11.3: the ldflags-injected version and commit are exposed at /api/v1/version.
	version.Version = "9.9.9-test"
	version.Commit = "deadbee"
	t.Cleanup(func() { version.Version, version.Commit = "dev", "unknown" })

	rec := do(t, newTestServer(t), http.MethodGet, "/api/v1/version")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/version = %d, want 200", rec.Code)
	}
	var got VersionResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.Version != "9.9.9-test" || got.Commit != "deadbee" {
		t.Fatalf("got %+v, want {9.9.9-test deadbee}", got)
	}
}

func TestMetricsExposesBuildInfo(t *testing.T) {
	// R11.3 requires pgsi_build_info; R9.8 defines its label set as
	// {version,commit}. Phase 2's MetricHub takes over the rest of the
	// catalogue, so Phase 0 serves exactly this one family.
	version.Version = "9.9.9-test"
	version.Commit = "deadbee"
	t.Cleanup(func() { version.Version, version.Commit = "dev", "unknown" })

	rec := do(t, newTestServer(t), http.MethodGet, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# HELP pgsi_build_info",
		"# TYPE pgsi_build_info gauge",
		`pgsi_build_info{version="9.9.9-test",commit="deadbee"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q; got:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestBuildInfoMetricMatchesHandler(t *testing.T) {
	s := newTestServer(t)
	if got, want := do(t, s, http.MethodGet, "/metrics").Body.String(), BuildInfoMetric(); got != want {
		t.Fatalf("handler body != BuildInfoMetric():\n%s\n---\n%s", got, want)
	}
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	s := newTestServer(t)
	if rec := do(t, s, http.MethodGet, "/api/v1/clusters"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/clusters = %d, want 404 (Phase 1 adds it, R8.1)", rec.Code)
	}
	// Method-pattern routing must reject POST with 405, not 404.
	if rec := do(t, s, http.MethodPost, "/healthz"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want 405", rec.Code)
	}
}

func TestDefaultListenAddress(t *testing.T) {
	// R8.7: the chart's probes and Service target 8080, so an empty config must
	// not silently bind a different port.
	if got := New(Config{}).cfg.Listen; got != ":8080" {
		t.Fatalf("default Listen = %q, want :8080", got)
	}
	if got := New(Config{}).cfg.ShutdownTimeout.String(); got != "10s" {
		t.Fatalf("default ShutdownTimeout = %s, want 10s", got)
	}
}
