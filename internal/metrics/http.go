package metrics

// R9.8's HTTP-request-duration family: pgsi_http_request_duration_seconds.
//
// This is the fifth of R9.8's eight families and the last one that was blocked on
// code rather than on a value. The blocker was that rendering a histogram meant
// reaching collector's renderer, which was unexported; 69d2f8d exported it
// (`collector.Histogram`) instead of letting this package write a second copy of
// the cumulative-count / `le` / `_sum` / `_count` rules — the R16.8 failure, and
// the one part of the exposition that `promtool check metrics` actually grades.
// Everything here is therefore a caller of that renderer, not a re-derivation of
// it.
//
// What the spec gives and what it does not. R9.8's entry names the family and
// stops there: no label set (the §5 row's Labels cell for Self is "—", and the
// only two families in that entry that carry labels are build_info's
// `{version,commit}` and the up family's R9.3 set), and no buckets. Both are
// named here and both are marked provisional in the file, per D13's doctrine —
// name it, say it is provisional, and bound the blast radius so a later ruling
// changes one line and not the family.
//
// The labels are `{method,route,code}`. `route` is the mux pattern
// (`/api/v1/insights/queries/{fingerprint}`) and never the request path, because
// the path is unbounded — 50 fingerprints per cluster would make the API's own
// latency metric the largest family in the exposition, which is the cardinality
// failure R13.4 exists to catch. The R9.3 label set (namespace, cluster, pod,
// instance_role) is deliberately absent: those describe a Postgres instance being
// scraped, and this family describes our own process, which is why
// pgsi_build_info omits them too.
//
// The cardinality cost, stated rather than discovered: one triple costs
// len(httpBuckets)+1 `_bucket` lines plus `_sum` and `_count` — ten series. The
// mux table has twelve routes and a response class per status code, so a busy
// server is on the order of 360 series against §5's ~120 for the whole Self row.
// That row is already over its own estimate — the collector duration family
// renders 13 collectors x 13 lines = 169 once every collector has run — because
// the ~120 predates the histogram shape, which is the same staleness
// scripts/cardinality-check.py documents for the 24 480 figure. Fitting the Self
// row is R13.4's script's job, not this file's.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/frankellis/pgsql-insights/internal/collector"
)

// HTTPMetricBase is the family's base name: R9.8's name minus R9.2's prefix,
// which is applied at render time so a deployment that sets metrics.prefix gets
// the same prefix on this family as on every other.
const HTTPMetricBase = "http_request_duration_seconds"

// httpBuckets are this family's histogram boundaries — provisional (R9.8 names
// the family and no buckets).
//
// Seven finite boundaries, so the render emits eight `_bucket` lines with the
// `+Inf` that collector.Histogram.Render always appends, plus `_sum` and
// `_count`: ten series per label set. That is R9.13's arithmetic for the sampled
// histogram ("50 x (8 buckets + `_sum` + `_count`) = 450"), which is the spec's
// only stated bucket count and the one number worth reusing rather than inventing
// a second shape.
//
// The placement answers R13.3, the requirement this family exists to measure:
// "API p50 < 100 ms for cached endpoints, p95 < 400 ms". 0.1 is exactly the p50
// target, so `histogram_quantile(0.5, ...)` resolves on the boundary the spec
// names rather than between two arbitrary ones, and 0.25/0.5 straddle the 400 ms
// p95 target. The three below 0.05 are what distinguishes R10.14's sub-second
// first paint from a slow response.
var httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5}

// HTTPBuckets is the exported view of httpBuckets. The set itself stays a package
// var because a caller must not be able to append to the boundaries a live
// histogram was built from; the accessor exists so this package's external test
// can assert the set's shape, which is what TestHTTPBucketSetIsR9_13sShape does
// (collector's own durationBuckets needs no accessor only because its tests are
// in-package).
func HTTPBuckets() []float64 { return httpBuckets }

// httpRequestKey is one series' label set as a value, so the map key and the
// rendered labels cannot drift apart the way a joined string key can.
type httpRequestKey struct {
	method string
	route  string
	code   string
}

// labels returns the key as the label map collector.Histogram.Render wants.
func (k httpRequestKey) labels() map[string]string {
	return map[string]string{"method": k.method, "route": k.route, "code": k.code}
}

// HTTPRequests is the process's HTTP latency state: one histogram per
// (method, route, code) triple.
//
// It is a type and not a prometheus registry for the standing reason in this
// package's header, and it holds collector.Histogram values rather than
// duplicating them for the reason in that type's own doc.
type HTTPRequests struct {
	mu    sync.Mutex
	hists map[httpRequestKey]*collector.Histogram
}

// NewHTTPRequests returns an empty family. An empty family renders as nothing at
// all (see Render), which is what lets the hub advertise it before the API
// middleware has served a single request.
func NewHTTPRequests() *HTTPRequests {
	return &HTTPRequests{hists: map[httpRequestKey]*collector.Histogram{}}
}

// Observe records one served request. `route` must be the matched pattern, not
// the URL — the caller in internal/api/metrics_mw.go takes it from
// http.Mux.FindRoute for exactly that reason, and an empty route is recorded as
// "unmatched" rather than as a series with an empty label value.
func (m *HTTPRequests) Observe(method, route string, code int, seconds float64) {
	if m == nil {
		return
	}
	if route == "" {
		route = "unmatched"
	}
	k := httpRequestKey{
		method: strings.ToUpper(method),
		route:  route,
		code:   strconv.Itoa(code),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.hists[k]
	if h == nil {
		h = collector.NewHistogram(httpBuckets)
		m.hists[k] = h
	}
	h.Observe(seconds)
}

// Render returns the family's exposition: one HELP/TYPE pair, then every triple's
// `_bucket`/`_sum`/`_count` lines underneath it.
//
// One pair for the family and not one per triple is the format's rule, not a
// style choice — collector.Stats.RenderDuration's doc records that a repeated
// HELP line parses as a duplicate and `promtool check metrics` reports it. An
// empty family returns the empty string rather than a bare HELP/TYPE pair, which
// is this package's rule for a family with no series (the header's "absent, not a
// stub" paragraph) and the same answer a nil receiver gives.
//
// prefix follows the R9.2 contract collector.UpTracker.Render uses: "" selects
// collector.DefaultPrefix, any other value is used verbatim.
func (m *HTTPRequests) Render(prefix string) string {
	if m == nil {
		return ""
	}
	if prefix == "" {
		prefix = collector.DefaultPrefix
	}
	m.mu.Lock()
	keys := make([]httpRequestKey, 0, len(m.hists))
	for k := range m.hists {
		keys = append(keys, k)
	}
	hists := make(map[httpRequestKey]*collector.Histogram, len(m.hists))
	for k, h := range m.hists {
		hists[k] = h
	}
	m.mu.Unlock()

	if len(keys) == 0 {
		return ""
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.method != b.method {
			return a.method < b.method
		}
		if a.route != b.route {
			return a.route < b.route
		}
		return a.code < b.code
	})

	name := prefix + HTTPMetricBase
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP %s HTTP response latency by route (R9.8).\n", name)
	fmt.Fprintf(&b, "# TYPE %s histogram\n", name)
	for _, k := range keys {
		b.WriteString(hists[k].Render(name, k.labels()))
	}
	return b.String()
}
