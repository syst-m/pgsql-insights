// Tests for R10.7's query detail — GET /api/v1/insights/queries/{fingerprint}.
//
// Two risks in this route, and the file is built around them rather than around
// line coverage:
//
//   - the *embedded half*. The detail's body must contain the list's row, not a
//     re-spelling of it: dto.QueryDetailView embeds dto.QueryView, and if the
//     embedding ever stops being flat on the wire — a `queryView:` wrapper
//     appearing, a field renamed on one side — the detail page and the table row
//     drift apart and nothing goes red, because both types still compile and the
//     JSON on the wire is what changed. TestQueryDetailBodyCarriesTheListRow
//     byte-compares the served object's keys and values against the fixture row
//     the list serves for the same triple, and requires the extra keys to be
//     exactly the five R10.7 adds.
//   - the *doc pairing*. R14.7 forbids a route in code the document does not
//     list, and a documented route that 404s. TestQueryDetailRouteIsDocumented
//     reads the embedded document for the path under `paths` (with x-status and
//     the QueryDetailView $ref) and requires it gone from `x-planned-paths`, so
//     the handler and the doc cannot be moved apart after this commit — the
//     TestQueriesRouteIsDocumented pattern, applied to the sibling route.
//
// The rest covers the four answers handleQueryDetail can give: 200 from a source
// that has the row, 404 from one that does not (including through a wrapped
// sentinel), 400 for each of the two parameter failures, and 503 for not-ready
// and for a source that errors. The empty source answers two of those today and
// a Phase 3 store answers all four, so the switch is tested at the width of the
// switch, not the width of today's default.
//
// There is no fixture-canonicality test here for the same reason queries_test.go
// has none: dto/queries_test.go's TestQueriesFixtureIsCanonical already
// byte-compares testdata/api/queries.json against the DTO's marshalling, and a
// second copy here would be R16.8's failure inside the test suite.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	openapi "github.com/frankellis/pgsql-insights/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// stubQueryDetail is a QueryDetailSource with a fixed answer, recording the
// triple it was asked for so the tests can assert the handler read the
// fingerprint out of the *path* and the pair out of the query string rather than
// re-deriving them (the stubQueries pattern).
type stubQueryDetail struct {
	view dto.QueryDetailView
	err  error

	called          int
	lastNamespace   string
	lastCluster     string
	lastFingerprint string
}

func (st *stubQueryDetail) QueryDetail(_ context.Context, namespace, cluster, fingerprint string) (dto.QueryDetailView, error) {
	st.called++
	st.lastNamespace, st.lastCluster, st.lastFingerprint = namespace, cluster, fingerprint
	return st.view, st.err
}

// newQueryDetailServer builds a ready Server whose only subsystem is the detail
// source. A process with no Fleet is ready once it is listening (R8.7(ii)),
// which is what makes the 200 paths reachable without a discovery stub.
func newQueryDetailServer(t *testing.T, src QueryDetailSource) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", QueryDetail: src})
	s.readiness.SetListening(true)
	return s
}

// detailPathFor returns the request target for one fixture row: the route's path
// with the row's fingerprint substituted, and the row's own namespace and cluster
// as the query pair. Building it from the fixture rather than from literals means
// a fingerprint in a test and a fingerprint in the contract cannot disagree.
func detailPathFor(t *testing.T, prefix string) string {
	t.Helper()
	row := fixtureRow(t, prefix)
	return "/api/v1/insights/queries/" + row.Fingerprint +
		"?namespace=" + row.Namespace + "&cluster=" + row.Cluster
}

// fixtureRow returns the first fixture row whose fingerprint starts with prefix.
// queries_test.go's fp does the same for the list's tests; this is the detail
// file's own copy because that helper returns only the fingerprint and the
// detail route needs the whole triple.
func fixtureRow(t *testing.T, prefix string) dto.QueryView {
	t.Helper()
	for _, r := range loadQueryFixture(t) {
		if len(r.Fingerprint) >= len(prefix) && r.Fingerprint[:len(prefix)] == prefix {
			return r
		}
	}
	t.Fatalf("no fixture row with fingerprint prefix %q", prefix)
	return dto.QueryView{}
}

// sampleDetail builds the detail view for a fixture row: the row embedded, plus
// the five detail-only fields filled with values that are not zero so an
// omitted field is distinguishable from a zero one.
func sampleDetail(t *testing.T, prefix string) dto.QueryDetailView {
	t.Helper()
	p95 := 118.4
	return dto.QueryDetailView{
		QueryView: fixtureRow(t, prefix),
		User:      "openwebui",
		FirstSeen: time.Date(2026, 11, 12, 8, 41, 3, 0, time.UTC),
		Latency: dto.LatencyView{
			MinMs:    0.4,
			MeanMs:   42.7,
			MaxMs:    918.2,
			StddevMs: 61.9,
			P95Ms:    &p95,
			Sampled:  true,
			Buckets: []dto.HistogramBucket{
				{Le: "1", Count: 1180},
				{Le: "5", Count: 1442},
				{Le: "+Inf", Count: 3007},
			},
			SumMs: 128410.5,
			Count: 3007,
		},
		Plans: []dto.PlanSampleView{{
			CapturedAt:   time.Date(2026, 11, 19, 6, 0, 0, 0, time.UTC),
			Method:       dto.MethodExplain,
			Availability: dto.PlanAvailable,
			SQL:          "SELECT * FROM users WHERE id = 17",
			PlanJSON:     json.RawMessage(`[{"Plan":{"Node Type":"Index Scan","Index Name":"users_pkey"}}]`),
			PlanHash:     "6b1d1f0e4a2c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e",
		}},
		PlanChanges: []dto.PlanChangeView{{
			DetectedAt:       time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC),
			PreviousPlanHash: "1111111111111111111111111111111111111111111111111111111111111111",
			CurrentPlanHash:  "6b1d1f0e4a2c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e",
		}},
	}
}

// getDetail performs the request and returns the status and body, failing on a
// non-200. Every 200-path assertion goes through it so a body that stops being
// valid JSON fails once, with the body quoted, rather than in several places.
func getDetail(t *testing.T, s *Server, target string) string {
	t.Helper()
	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// jsonMap marshals v and decodes it into a generic map, so two JSON objects can
// be compared key-by-key without depending on key order.
func jsonMap(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %T as object: %v\n%s", v, err, raw)
	}
	return m
}

// ---------------------------------------------------------------------------
// route
// ---------------------------------------------------------------------------

func TestQueryDetailRouteRegistered(t *testing.T) {
	// The registration itself: server_test.go asserts the URL answers something;
	// this asserts it is *this* handler, by checking a field only this route's
	// body has. `planChanges` is on QueryDetailView and on nothing else the
	// router serves, so a request that reaches the list or the series handler
	// fails here rather than passing on a coincidental 200.
	row := fixtureRow(t, "8439")
	st := &stubQueryDetail{view: sampleDetail(t, "8439")}
	s := newQueryDetailServer(t, st)

	target := "/api/v1/insights/queries/" + row.Fingerprint + "?namespace=" + row.Namespace + "&cluster=" + row.Cluster
	body := getDetail(t, s, target)

	if !json.Valid([]byte(body)) {
		t.Fatalf("body is not valid JSON: %s", body)
	}
	var view dto.QueryDetailView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("decode QueryDetailView: %v\nbody: %s", err, body)
	}
	if view.Fingerprint != row.Fingerprint {
		t.Errorf("fingerprint = %q, want the fixture row's %q", view.Fingerprint, row.Fingerprint)
	}
	if st.called != 1 {
		t.Fatalf("source called %d times, want 1", st.called)
	}
	// The handler must pass the path parameter and the query pair through
	// unchanged — the triple is the route's whole input, and a handler that
	// dropped one of the three would still return 200 for the wrong row.
	if st.lastNamespace != row.Namespace || st.lastCluster != row.Cluster || st.lastFingerprint != row.Fingerprint {
		t.Errorf("source asked for %s/%s/%s, want %s/%s/%s",
			st.lastNamespace, st.lastCluster, st.lastFingerprint,
			row.Namespace, row.Cluster, row.Fingerprint)
	}
}

func TestQueryDetailBodyCarriesTheListRow(t *testing.T) {
	// The embedded half, on the wire. Every key and value the list serves for this
	// fingerprint must be present and identical in the detail body, and the only
	// extra keys are the five R10.7 adds. This is the check that the Go embedding
	// is flat JSON: a `queryView` wrapper, a renamed column, or a detail type that
	// stops embedding QueryView all fail here.
	row := fixtureRow(t, "8439")
	view := sampleDetail(t, "8439")
	s := newQueryDetailServer(t, &stubQueryDetail{view: view})
	body := getDetail(t, s, detailPathFor(t, "8439"))

	var detail map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatalf("decode body: %v\nbody: %s", err, body)
	}
	for key, want := range jsonMap(t, row) {
		got, ok := detail[key]
		if !ok {
			t.Errorf("detail body is missing %q, which the list serves for this row", key)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("detail's %q is %s, the list's row has %s", key, got, want)
		}
	}
	// The five additions, and nothing else. `user` is omitempty in Go, so a
	// missing user would drop the key — sampleDetail sets one, which is what makes
	// this set exact rather than "at most".
	want := map[string]bool{
		"user": true, "firstSeen": true, "latency": true, "plans": true, "planChanges": true,
	}
	for key := range detail {
		if _, ok := jsonMap(t, row)[key]; ok {
			continue // one of the row's own keys, already compared above
		}
		if !want[key] {
			t.Errorf("detail body has unexpected key %q", key)
		}
		delete(want, key)
	}
	for key := range want {
		t.Errorf("detail body is missing the detail-only field %q", key)
	}
}

func TestQueryDetailEmptySourceIsNotFound(t *testing.T) {
	// The default source's answer, which is every answer until Phase 3 assigns a
	// store: 404, not 200-with-an-empty-object and not 503. The body must name the
	// triple it looked for, because "no row for that fingerprint" without the
	// fingerprint is an error a client cannot act on.
	s := newQueryDetailServer(t, nil)
	rec := do(t, s, http.MethodGet, "/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3?namespace=open-webui&cluster=open-webui-pg")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET = %d, want 404\nbody: %s", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotFound {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrNotFound)
	}
	for _, want := range []string{"open-webui", "open-webui-pg", "843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3"} {
		if !strings.Contains(body.Error.Message, want) {
			t.Errorf("message %q does not name %q", body.Error.Message, want)
		}
	}
}

func TestQueryDetailParameterValidation(t *testing.T) {
	// The two parameter failures, both checked before readiness so a malformed
	// request never gets the "try again later" answer for something that will
	// never succeed. The server is not ready in this test on purpose: a 503 here
	// would mean the checks were reordered, which is the bug the test exists for.
	st := &stubQueryDetail{err: errShouldNotBeCalled}
	s := New(Config{Listen: "127.0.0.1:0", QueryDetail: st})

	cases := []struct {
		target string
		want   string
	}{
		{"/api/v1/insights/queries/%20", "fingerprint is required"},
		{"/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3", "namespace and cluster are required"},
		{"/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3?namespace=open-webui", "namespace and cluster are required"},
		{"/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3?cluster=open-webui-pg", "namespace and cluster are required"},
	}
	for _, tc := range cases {
		rec := do(t, s, http.MethodGet, tc.target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400\nbody: %s", tc.target, rec.Code, rec.Body.String())
			continue
		}
		var body dto.ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("GET %s: decode ErrorBody: %v\nbody: %s", tc.target, err, rec.Body.String())
			continue
		}
		if body.Error.Code != dto.ErrInvalidRequest {
			t.Errorf("GET %s: code = %q, want %q", tc.target, body.Error.Code, dto.ErrInvalidRequest)
		}
		if !strings.Contains(body.Error.Message, tc.want) {
			t.Errorf("GET %s: message %q does not say %q", tc.target, body.Error.Message, tc.want)
		}
	}
	if st.called != 0 {
		t.Errorf("source called %d times for malformed requests, want 0", st.called)
	}
}

// errShouldNotBeCalled is a sentinel for a source that must not be consulted; a
// stub returning it turns "the handler asked the store" into a test failure even
// when the status code would otherwise look right.
var errShouldNotBeCalled = errors.New("source consulted but should not have been")

func TestQueryDetailNotReady(t *testing.T) {
	// R8.7's single readiness signal, the same value /readyz answers with: a
	// server that has not synced must not answer 404 for a query it has not read
	// yet. The fingerprint and the pair are both valid here, so the only thing
	// that can answer is the gate.
	s := New(Config{Listen: "127.0.0.1:0", QueryDetail: &stubQueryDetail{err: errShouldNotBeCalled}})
	rec := do(t, s, http.MethodGet, "/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3?namespace=open-webui&cluster=open-webui-pg")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET = %d, want 503\nbody: %s", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
}

func TestQueryDetailSourceErrors(t *testing.T) {
	// The two answers a real source adds, distinguished the way R8.5's code set
	// exists to distinguish them: "the store answered and has nothing" is 404
	// NotFound, "the store did not answer" is 503 Unavailable. A single `err != nil`
	// branch answering 503 for both would make every missing query look like an
	// outage, and answering 404 for both would make an unreadable store look like
	// an empty one.
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantStr  string
	}{
		{"bare sentinel", ErrQueryDetailNotFound, http.StatusNotFound, dto.ErrNotFound},
		{"wrapped sentinel", fmt.Errorf("store: no row in open-webui/open-webui-pg: %w", ErrQueryDetailNotFound),
			http.StatusNotFound, dto.ErrNotFound},
		{"other error", errors.New("sqlite: no such table: queries"), http.StatusServiceUnavailable, dto.ErrUnavailable},
	}
	for _, tc := range cases {
		st := &stubQueryDetail{err: tc.err}
		s := newQueryDetailServer(t, st)
		rec := do(t, s, http.MethodGet, "/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3?namespace=open-webui&cluster=open-webui-pg")

		if rec.Code != tc.wantCode {
			t.Errorf("%s: GET = %d, want %d\nbody: %s", tc.name, rec.Code, tc.wantCode, rec.Body.String())
			continue
		}
		var body dto.ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: decode ErrorBody: %v\nbody: %s", tc.name, err, rec.Body.String())
			continue
		}
		if body.Error.Code != tc.wantStr {
			t.Errorf("%s: code = %q, want %q", tc.name, body.Error.Code, tc.wantStr)
		}
		// The wrapped message must survive to the client: it is the only place the
		// store's own detail ("no row in open-webui/open-webui-pg") is reported,
		// and errors.Is-based switches that build a fresh message lose it.
		if !strings.Contains(body.Error.Message, tc.err.Error()) {
			t.Errorf("%s: message %q does not carry the source's error %q", tc.name, body.Error.Message, tc.err)
		}
	}
}

func TestQueryDetailDoesNotShadowSeriesRoute(t *testing.T) {
	// The routing claim in query_detail.go's header, tested rather than asserted:
	// Go's mux prefers a literal over a wildcard, so /api/v1/insights/queries/
	// series still answers R8.11's batched route now that
	// /api/v1/insights/queries/{fingerprint} is registered. If the two patterns
	// ever swap precedence, the detail handler starts swallowing every sparkline
	// request and this is where it shows.
	st := &stubQueryDetail{err: errShouldNotBeCalled}
	s := New(Config{
		Listen:      "127.0.0.1:0",
		Series:      &stubSeries{},
		QueryDetail: st,
	})
	s.readiness.SetListening(true)

	rec := do(t, s, http.MethodGet, "/api/v1/insights/queries/series?fingerprints="+testFingerprint)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/insights/queries/series = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if st.called != 0 {
		t.Errorf("detail source called %d times for the series URL, want 0 — the wildcard matched a path the literal owns", st.called)
	}
}

// ---------------------------------------------------------------------------
// document pairing (R14.7)
// ---------------------------------------------------------------------------

func TestQueryDetailRouteIsDocumented(t *testing.T) {
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + QueryDetailPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			QueryDetailPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", QueryDetailPath)
		}
		// The 200 body must be the type the handler serves. QueryDetailPath's
		// handler writes a dto.QueryDetailView and the doc must $ref exactly that
		// name — the R16.8 argument applied to the response type, the way the
		// list route's test pins QueriesEnvelope.
		if !strings.Contains(item, "$ref: '#/components/schemas/QueryDetailView'") {
			t.Errorf("%s's responses do not $ref QueryDetailView", QueryDetailPath)
		}
		// The three parameters are inline, not $refs: the shared Namespace and
		// Cluster components say required: false and this route requires both, so
		// a $ref here would document an optional parameter the handler rejects.
		for _, p := range []string{"name: fingerprint", "name: namespace", "name: cluster"} {
			if !strings.Contains(item, p) {
				t.Errorf("%s's parameters do not include an inline %q", QueryDetailPath, p)
			}
		}
		if strings.Contains(item, "$ref: '#/components/parameters/Namespace'") ||
			strings.Contains(item, "$ref: '#/components/parameters/Cluster'") {
			t.Errorf("%s $refs the shared Namespace/Cluster parameters, which are optional; this route requires both",
				QueryDetailPath)
		}
	}
	// The manifest listed this route before the route existed. The entry was
	// deleted rather than marked (the rule the r8.3-promql-pair note states), so
	// the check is over the `- ` lines: a prose note naming the route is expected
	// and carries no route.
	for _, line := range strings.Split(planned, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		fields := strings.Fields(line[2:])
		if len(fields) > 0 && fields[0] == QueryDetailPath {
			t.Errorf("%s is still listed in x-planned-paths: it is in code now, "+
				"and `paths` and x-planned-paths must be disjoint (R14.7)", QueryDetailPath)
		}
	}
}
