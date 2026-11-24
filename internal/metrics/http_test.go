package metrics_test

import (
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/metrics"
)

// TestHTTPRenderExact is the golden for the R9.8 HTTP-duration family: one
// observed request, and the exact bytes its series must produce.
//
// The golden is a literal rather than a call to collector.Histogram.Render,
// because calling the renderer to check the renderer is an assertion that cannot
// fail: http.go's Render would pass whatever it did, including rendering the
// wrong family name, the wrong prefix, or one HELP line per triple. The literal
// is also what pins the label set — `{code,le,method,route}` sorted, which is
// collector.LabelString's contract and the reason a second copy of the renderer
// was worth avoiding.
const httpGoldenOneSeries = "# HELP pgsi_http_request_duration_seconds HTTP response latency by route (R9.8).\n" +
	"# TYPE pgsi_http_request_duration_seconds histogram\n" +
	`pgsi_http_request_duration_seconds_bucket{code="200",le="0.005",method="GET",route="/api/v1/clusters"} 0` + "\n" +
	`pgsi_http_request_duration_seconds_bucket{code="200",le="0.01",method="GET",route="/api/v1/clusters"} 0` + "\n" +
	`pgsi_http_request_duration_seconds_bucket{code="200",le="0.025",method="GET",route="/api/v1/clusters"} 0` + "\n" +
	`pgsi_http_request_duration_seconds_bucket{code="200",le="0.05",method="GET",route="/api/v1/clusters"} 1` + "\n" +
	`pgsi_http_request_duration_seconds_bucket{code="200",le="0.1",method="GET",route="/api/v1/clusters"} 1` + "\n" +
	`pgsi_http_request_duration_seconds_bucket{code="200",le="0.25",method="GET",route="/api/v1/clusters"} 1` + "\n" +
	`pgsi_http_request_duration_seconds_bucket{code="200",le="0.5",method="GET",route="/api/v1/clusters"} 1` + "\n" +
	`pgsi_http_request_duration_seconds_bucket{code="200",le="+Inf",method="GET",route="/api/v1/clusters"} 1` + "\n" +
	`pgsi_http_request_duration_seconds_sum{code="200",method="GET",route="/api/v1/clusters"} 0.03` + "\n" +
	`pgsi_http_request_duration_seconds_count{code="200",method="GET",route="/api/v1/clusters"} 1` + "\n"

func TestHTTPRenderExact(t *testing.T) {
	m := metrics.NewHTTPRequests()
	m.Observe("GET", "/api/v1/clusters", 200, 0.03)

	if got := m.Render(""); got != httpGoldenOneSeries {
		t.Errorf("Render(\"\") =\n%s\nwant\n%s", got, httpGoldenOneSeries)
	}
}

// TestHTTPRenderIsOneHelpTypePairForTheFamily pins the exposition rule that a
// second triple does not add a second HELP/TYPE pair: a family has one pair and
// every series underneath it, and a repeated pair is what
// `promtool check metrics` reports as a duplicate (collector/metrics.go's
// RenderDuration docblock is the precedent this copies).
func TestHTTPRenderIsOneHelpTypePairForTheFamily(t *testing.T) {
	m := metrics.NewHTTPRequests()
	m.Observe("GET", "/api/v1/clusters", 200, 0.03)
	m.Observe("GET", "/api/v1/version", 500, 1.5)

	got := m.Render("")
	if n := strings.Count(got, "# HELP "); n != 1 {
		t.Errorf("%d HELP lines for one family, want 1\n%s", n, got)
	}
	if n := strings.Count(got, "# TYPE "); n != 1 {
		t.Errorf("%d TYPE lines for one family, want 1\n%s", n, got)
	}
	// Two triples, ten series each (seven finite buckets + +Inf + _sum + _count).
	if n := strings.Count(got, "\n") - 2; n != 20 {
		t.Errorf("%d sample lines for two triples, want 20\n%s", n, got)
	}
	// Sorted output is the deterministic-render requirement, not cosmetics.
	i := strings.Index(got, `route="/api/v1/clusters"`)
	j := strings.Index(got, `route="/api/v1/version"`)
	if i < 0 || j < 0 || i > j {
		t.Errorf("series not sorted by route: clusters at %d, version at %d", i, j)
	}
}

// TestHTTPEmptyAndNilAreAbsent is this package's rule for a family with no
// series: absent, not a HELP/TYPE pair beside a value nobody measured. The nil
// case is the one a server hits first — the option is passed before the
// middleware has served a request — and it is why the Hub needs no nil check.
func TestHTTPEmptyAndNilAreAbsent(t *testing.T) {
	if got := metrics.NewHTTPRequests().Render(""); got != "" {
		t.Errorf("empty family rendered %q, want no bytes", got)
	}
	var nilFamily *metrics.HTTPRequests
	if got := nilFamily.Render(""); got != "" {
		t.Errorf("nil family rendered %q, want no bytes", got)
	}
}

// TestHTTPPrefixArgument pins R9.2's contract as it applies to this family: the
// base name carries no prefix, "" selects the default, and any other value is
// used verbatim — the same three answers collector.UpTracker gives.
func TestHTTPPrefixArgument(t *testing.T) {
	m := metrics.NewHTTPRequests()
	m.Observe("GET", "/api/v1/clusters", 200, 0.03)

	if m.Render("pgsi_") != m.Render("") {
		t.Error(`Render("pgsi_") != Render(""): "" must select collector.DefaultPrefix`)
	}
	custom := metrics.NewHTTPRequests()
	custom.Observe("GET", "/api/v1/clusters", 200, 0.03)
	got := custom.Render("acme_")
	if !strings.HasPrefix(got, "# HELP acme_http_request_duration_seconds ") {
		t.Errorf("custom prefix not applied:\n%s", got)
	}
	if strings.Contains(got, "pgsi_") {
		t.Errorf("default prefix leaked into a custom-prefix render:\n%s", got)
	}
}

// TestHTTPBucketSetIsR9_13sShape pins the two numbers the bucket set was derived
// from, so a future edit to httpBuckets has to argue with the spec rather than
// with a comment.
func TestHTTPBucketSetIsR9_13sShape(t *testing.T) {
	// R9.13 accounts a histogram as "8 buckets + _sum + _count" per label set —
	// 450 = 50 × 9 — which means eight _bucket lines including the +Inf that
	// collector.Histogram.Render always appends, so seven finite boundaries.
	// That is the spec's only stated bucket count and this family reuses it
	// rather than inventing a second shape.
	m := metrics.NewHTTPRequests()
	m.Observe("GET", "/api/v1/clusters", 200, 0.03)
	got := m.Render("")
	if n := strings.Count(got, "_bucket{"); n != 8 {
		t.Errorf("%d _bucket lines, want 8 (7 finite + +Inf, per R9.13's accounting)\n%s", n, got)
	}

	// R13.3: "API p50 < 100 ms for cached endpoints, p95 < 400 ms". The p50
	// target must be a boundary, not a gap, or histogram_quantile(0.5) answers
	// the spec's question by interpolating between two unrelated numbers.
	if !hasBucket(0.1) {
		t.Error("no 0.1 boundary: R13.3's p50 target must be a bucket edge")
	}
	// And the p95 target must be straddled by an adjacent pair.
	if !straddles(0.4) {
		t.Error("no bucket pair straddles R13.3's 400 ms p95 target")
	}
}

func hasBucket(v float64) bool {
	for _, b := range metrics.HTTPBuckets() {
		if b == v {
			return true
		}
	}
	return false
}

func straddles(v float64) bool {
	bs := metrics.HTTPBuckets()
	for i := 1; i < len(bs); i++ {
		if bs[i-1] < v && v <= bs[i] {
			return true
		}
	}
	return false
}

// TestHTTPRouteNeverEmpty: an unmatched request must not become a series with an
// empty label value, which is the shape that makes a dashboard's route dropdown
// sprain a blank entry.
func TestHTTPRouteNeverEmpty(t *testing.T) {
	m := metrics.NewHTTPRequests()
	m.Observe("GET", "", 404, 0.001)
	if !strings.Contains(m.Render(""), `route="unmatched"`) {
		t.Errorf("empty route not recorded as unmatched:\n%s", m.Render(""))
	}
}

// TestHTTPMethodIsUppercased keeps `get` and `GET` from becoming two series for
// one route, which is the one way a label value can double a family's cost.
func TestHTTPMethodIsUppercased(t *testing.T) {
	m := metrics.NewHTTPRequests()
	m.Observe("get", "/api/v1/clusters", 200, 0.03)
	if !strings.Contains(m.Render(""), `method="GET"`) {
		t.Errorf("method not normalised:\n%s", m.Render(""))
	}
}
