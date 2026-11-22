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

// TestReadyzGatedOnListening covers the process's own contribution to readiness:
// the socket. R8.7's other half — the subsystems this process runs — is
// TestReadyzAndClustersShareOneReadiness in clusters_test.go, which is where it
// lives now that both surfaces read one Readiness value instead of Run's
// `ready` flag.
func TestReadyzGatedOnListening(t *testing.T) {
	s := newTestServer(t)
	// Before Run() binds a socket the server is not ready, so the probe fails
	// closed: a pod that has not started serving must not be added to the
	// Service endpoints (R8.7).
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz before start = %d, want 503", rec.Code)
	}
	s.readiness.SetListening(true)
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("GET /readyz after start = %d, want 200", rec.Code)
	}
	// R13.8: shutting down clears readiness so the pod drains.
	s.readiness.SetListening(false)
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
	// /api/v1/clusters was the route this assertion expected to 404; Phase 1a
	// registered it, so it now answers 503 (no fleet, not listening) and the 404
	// coverage moves to routes this build genuinely lacks. They are named as
	// openapi.yaml's x-planned-paths names them — /api/v1/clusters/{ns}/{name}
	// and /api/v1/insights/queries (R8.2, Phase 5) — so the test asserts the
	// *planned* URL is absent, not some ad-hoc one. The SPA guard in spa.go is
	// what answers them, and it is what stops any unknown /api/... path from
	// returning the app shell with a 200.
	for _, path := range []string{"/api/v1/queries", "/api/v1/insights/queries", "/api/v1/clusters/default/pg1"} {
		if rec := do(t, s, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/clusters"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/clusters = %d, want 503 (registered, not yet ready)", rec.Code)
	}
	// R8.7's third surface, registered in the same commit that documented it in
	// openapi.yaml's `paths`.
	if rec := do(t, s, http.MethodGet, "/debug/pprof/"); rec.Code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/ = %d, want 200", rec.Code)
	}
	// Method-pattern routing must reject POST with 405, not 404.
	if rec := do(t, s, http.MethodPost, "/healthz"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want 405", rec.Code)
	}
	if rec := do(t, s, http.MethodPost, "/api/v1/clusters"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/v1/clusters = %d, want 405", rec.Code)
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
