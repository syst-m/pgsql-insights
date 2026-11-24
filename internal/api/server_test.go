package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/metrics"
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
	if got, want := do(t, s, http.MethodGet, "/metrics").Body.String(), metrics.BuildInfo(); got != want {
		t.Fatalf("handler body != metrics.BuildInfo():\n%s\n---\n%s", got, want)
	}
}

// nilPerQuerySource is this package's own metrics.PerQueryFamily implementation.
// It has to be local: internal/store imports internal/api, so an api test cannot
// import store to get a *store.Store to make a typed nil from — and that
// limitation is what makes the test about api's handling rather than about the
// implementation's. The compile-time line below is what keeps a signature drift
// in PerQueryFamily from passing here silently.
//
// RenderPerQuery is on a pointer receiver with a nil check, mirroring the shape
// d2415b1 gave store.RenderPerQuery (`if s == nil { return "", nil }`). A value
// receiver would panic when the hub calls a nil *nilPerQuerySource, and the panic
// would be the fake's rather than the code under test's — which is the difference
// between a test that pins the contract and one that documents its own fixture.
type nilPerQuerySource struct{}

func (s *nilPerQuerySource) RenderPerQuery(context.Context, int, string) (string, error) {
	if s == nil {
		return "", nil
	}
	return "", nil
}

var _ metrics.PerQueryFamily = (*nilPerQuerySource)(nil)

// TestNewAcceptsTypedNilPerQuery is the test internal/metrics/store.go cites
// twice — at :70 for the PerQueryMetrics half ("the hazard d2415b1 removed from
// store.RenderPerQuery … internal/api already carries in
// TestNewAcceptsTypedNilPerQuery") and at :84 for the StoreSize half ("the same
// division that makes TestNewAcceptsTypedNilPerQuery the MemSink half's test").
// Both citations name one test, so it covers both fields in one New call: the
// point is that api passes a typed nil through to a renderer that handles it,
// rather than dereferencing it somewhere that requires a non-nil value.
//
// The assertion is the body equality rather than a per-family substring check,
// because equality is the wider instrument: it fails if either typed nil
// contributes any byte — an empty `# TYPE` pair, which is what promtool reports as
// the wrong shape, or a duplicated family, or a render that ordered the body
// differently. The explicit store-size absence check stays alongside it only
// because that is the family whose nil the citing docblock is arguing about, and
// a failure naming it is easier to read than a body diff.
func TestNewAcceptsTypedNilPerQuery(t *testing.T) {
	var perQuery *nilPerQuerySource // typed nil, not == nil
	var sink *collector.MemSink     // typed nil, not == nil
	s := New(Config{
		Listen:          "127.0.0.1:0",
		PerQueryMetrics: perQuery,
		StoreSize:       sink,
	})

	body := s.metricsBody(context.Background())
	if want := metrics.BuildInfo(); body != want {
		t.Errorf("metrics body with two typed-nil sources != metrics.BuildInfo():\n%s\n---\n%s", body, want)
	}
	if strings.Contains(body, "pgsi_store_size_bytes") {
		t.Errorf("body has pgsi_store_size_bytes for a typed-nil (*collector.MemSink)(nil):\n%s", body)
	}
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	s := newTestServer(t)
	// The 404 coverage is kept pointed at routes this build genuinely lacks, and
	// named the way openapi.yaml's x-planned-paths names them, so the test
	// asserts the *planned* URL is absent rather than some ad-hoc one. Three
	// routes that used to be in this list have since been registered and moved to
	// the 503 assertions below: /api/v1/clusters (Phase 1a),
	// /api/v1/clusters/{namespace}/{name} (Phase 5, cluster_detail.go) and
	// /api/v1/insights/queries (R8.2, queries.go). The one left is
	// /api/v1/queries, which the spec never names at all — the singular form is
	// here because a client that guesses it must get a 404, not the app shell.
	// The SPA guard in spa.go is what answers it, and it is what stops any
	// unknown /api/... path from returning the app shell with a 200.
	for _, path := range []string{"/api/v1/queries"} {
		if rec := do(t, s, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	// Registered routes on a server that has not synced answer 503, not 200 with
	// an empty body and not 404: R8.7's single readiness signal, asserted for both
	// cluster routes so the detail route cannot quietly answer 404 for a fleet it
	// has not read yet, and for the query routes so neither can quietly answer 200
	// with an empty page — or 404 for a query it has not read yet — for a fleet it
	// has not read yet either (R8.7's rule is about the readiness *value*, not
	// about which route reads it). The query-detail URL carries a valid
	// fingerprint/namespace/cluster triple so the request reaches the gate: the
	// handler validates parameters first, and a 400 here would prove nothing about
	// readiness (query_detail_test.go's TestQueryDetailParameterValidation pins
	// the order).
	for _, path := range []string{
		"/api/v1/clusters",
		"/api/v1/clusters/default/pg1",
		"/api/v1/insights/queries",
		"/api/v1/insights/queries/series?fingerprints=" + testFingerprint,
		"/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3?namespace=open-webui&cluster=open-webui-pg",
	} {
		if rec := do(t, s, http.MethodGet, path); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s = %d, want 503 (registered, not yet ready)", path, rec.Code)
		}
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
