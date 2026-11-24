// Tests for R3.10's plan samples — GET /api/v1/insights/queries/{fingerprint}/plans.
//
// Two risks in this route, and the file is built around them rather than around
// line coverage:
//
//   - the *doc pairing*. R14.7 forbids a route in code the document does not
//     list, and a documented route that 404s. This route spent its whole life in
//     api/x-planned-paths.yaml, which is the one place a route can be *named*
//     without existing, so the pairing test is the whole argument:
//     TestQueryPlansRouteIsDocumented requires the path under `paths` (with
//     x-status and the QueryPlansEnvelope $ref) and requires it gone from
//     `x-planned-paths`, so the handler and the document cannot be moved apart in
//     different commits. The two lists being disjoint is what makes that checkable
//     at all, and it is the invariant scripts/check-openapi.py owns.
//   - the *routing*. Three patterns now share the /insights/queries prefix: the
//     literal /series, the literal-ish {fingerprint}, and this one's
//     {fingerprint}/plans. TestQueryPlansDoesNotShadowOtherRoutes pins all three
//     resolutions in one test, including the one this commit creates:
//     /insights/queries/series/plans now matches *this* route with fingerprint
//     "series", which is only harmless because R3.4 makes fingerprints hashes.
//
// The rest covers the four answers handleQueryPlans can give: 200 from a source
// that has samples, 404 from one that does not (including through a wrapped
// sentinel), 400 for each of the two parameter failures, and 503 for not-ready
// and for a source that errors. The empty source answers two of those today and
// Phase 6b's capture answers all four, so the switch is tested at the width of
// the switch, not the width of today's default.
//
// There is no fixture-canonicality test here for the same reason
// query_detail_test.go has none: dto/queries_test.go decodes
// testdata/api/query-plans.json into Envelope[PlanSampleView] and validates every
// field against R3.10's enum groups, and a second copy here would be R16.8's
// failure inside the test suite. What this file does check is that the *served*
// body is the fixture's `data` — the handler forwarding its source rather than
// re-spelling it.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	openapi "github.com/frankellis/pgsql-insights/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// queryPlansFixturePath is R3.10's fixture at the location R16.8 fixes
// (repo-root testdata/api/), reached from this package's directory the way
// queriesFixturePath and fixturePath are.
const queryPlansFixturePath = "../../testdata/api/query-plans.json"

// loadQueryPlansFixture reads the fixture's samples. The file is an
// Envelope[PlanSampleView], so the envelope is parsed and the samples returned:
// the fixture's own meta (total 2, truncated false) is checked in
// TestQueryPlansBodyIsTheFixture, where the served meta can be compared to it.
func loadQueryPlansFixture(t *testing.T) (QueryPlansEnvelope, []dto.PlanSampleView) {
	t.Helper()
	raw, err := os.ReadFile(queryPlansFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", queryPlansFixturePath, err)
	}
	var env QueryPlansEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse %s: %v\n%s", queryPlansFixturePath, err, raw)
	}
	if len(env.Data) == 0 {
		t.Fatalf("%s has no samples", queryPlansFixturePath)
	}
	return env, env.Data
}

// stubQueryPlans is a QueryPlansSource with a fixed answer, recording the triple
// it was asked for so the tests can assert the handler read the fingerprint out
// of the *path* and the pair out of the query string rather than re-deriving them
// (the stubQueryDetail pattern).
type stubQueryPlans struct {
	plans []dto.PlanSampleView
	err   error

	called          int
	lastNamespace   string
	lastCluster     string
	lastFingerprint string
}

func (st *stubQueryPlans) QueryPlans(_ context.Context, namespace, cluster, fingerprint string) ([]dto.PlanSampleView, error) {
	st.called++
	st.lastNamespace, st.lastCluster, st.lastFingerprint = namespace, cluster, fingerprint
	return st.plans, st.err
}

// newQueryPlansServer builds a ready Server whose only subsystem is the plans
// source. A process with no Fleet is ready once it is listening (R8.7(ii)), which
// is what makes the 200 paths reachable without a discovery stub.
func newQueryPlansServer(t *testing.T, src QueryPlansSource) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", QueryPlans: src})
	s.readiness.SetListening(true)
	return s
}

// plansPathFor returns the request target for one fixture row's plans: the
// route's path with the row's fingerprint substituted and /plans appended, then
// the row's own namespace and cluster as the query pair. Built from the fixture
// rather than from literals, so a fingerprint in a test and a fingerprint in the
// contract cannot disagree — and so the three query routes are exercised with one
// triple, which is what makes TestQueryPlansDoesNotShadowOtherRoutes a routing
// test rather than three coincidental 200s.
//
// The path is assembled here rather than derived from detailPathFor by appending
// "/plans", because detailPathFor ends with a query string and appending after it
// yields ".../8439…?namespace=open-webui&cluster=open-webui-pg/plans" — a URL
// whose path is the *detail* route's and whose cluster parameter ends in
// "/plans". That request resolves, answers 404, and names a triple no row has,
// which is a failure the mux reports rather than one this file should manufacture.
func plansPathFor(t *testing.T, prefix string) string {
	t.Helper()
	row := fixtureRow(t, prefix)
	return "/api/v1/insights/queries/" + row.Fingerprint + "/plans" +
		"?namespace=" + row.Namespace + "&cluster=" + row.Cluster
}

// getPlans performs the request and returns the body, failing on a non-200. Every
// 200-path assertion goes through it so a body that stops being valid JSON fails
// once, with the body quoted, rather than in several places.
func getPlans(t *testing.T, s *Server, target string) string {
	t.Helper()
	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// ---------------------------------------------------------------------------
// route
// ---------------------------------------------------------------------------

func TestQueryPlansRouteRegistered(t *testing.T) {
	// The registration itself: server_test.go asserts the URL answers something;
	// this asserts it is *this* handler, by decoding the body as
	// Envelope[PlanSampleView] — a shape only this route serves. The list and the
	// series routes both answer 200 for a URL under /insights/queries, and neither
	// of their bodies decodes into this type.
	_, plans := loadQueryPlansFixture(t)
	st := &stubQueryPlans{plans: plans}
	s := newQueryPlansServer(t, st)

	target := plansPathFor(t, "8439")
	body := getPlans(t, s, target)
	if !json.Valid([]byte(body)) {
		t.Fatalf("body is not valid JSON: %s", body)
	}
	var env QueryPlansEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode QueryPlansEnvelope: %v\nbody: %s", err, body)
	}

	// The triple the source was asked for must be the one in the URL: the
	// fingerprint from the path, the pair from the query string. A handler that
	// read the fingerprint from the query string, or dropped the pair, would still
	// answer 200 here if it were written to ignore its arguments.
	if st.called != 1 {
		t.Fatalf("source called %d times, want 1", st.called)
	}
	row := fixtureRow(t, "8439")
	if st.lastNamespace != row.Namespace || st.lastCluster != row.Cluster || st.lastFingerprint != row.Fingerprint {
		t.Errorf("source asked for %s/%s/%s, want %s/%s/%s",
			st.lastNamespace, st.lastCluster, st.lastFingerprint,
			row.Namespace, row.Cluster, row.Fingerprint)
	}
}

func TestQueryPlansBodyIsTheFixture(t *testing.T) {
	// The handler forwards its source. `data` must be the fixture's samples,
	// byte-for-byte, and `meta` must agree with the fixture's own meta — total 2,
	// truncated false, no cursor — which is the three-field agreement dto.Meta
	// requires and the shape this route's non-paging claim rests on.
	fixture, plans := loadQueryPlansFixture(t)
	s := newQueryPlansServer(t, &stubQueryPlans{plans: plans})
	body := getPlans(t, s, plansPathFor(t, "8439"))

	var served map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &served); err != nil {
		t.Fatalf("decode body: %v\nbody: %s", err, body)
	}
	gotData, ok := served["data"]
	if !ok {
		t.Fatalf("body has no `data` key: %s", body)
	}
	if want, err := json.Marshal(plans); err != nil {
		t.Fatalf("marshal fixture samples: %v", err)
	} else if string(gotData) != string(want) {
		t.Errorf("served data is not the fixture's\n got: %s\nwant: %s", gotData, want)
	}

	// The served meta, compared field by field rather than byte for byte: the
	// fixture's generatedAt is a fixed 2026-11-20T09:12:00Z and the handler stamps
	// the response with time.Now(), which is the one field a live response cannot
	// reproduce from a fixture and the reason the envelope is not byte-compared
	// whole (the same reason query_detail_test.go compares the object's keys).
	var servedEnv QueryPlansEnvelope
	if err := json.Unmarshal([]byte(body), &servedEnv); err != nil {
		t.Fatalf("decode envelope: %v\nbody: %s", err, body)
	}
	if servedEnv.Meta.Total != fixture.Meta.Total {
		t.Errorf("meta.total = %d, want the fixture's %d", servedEnv.Meta.Total, fixture.Meta.Total)
	}
	if servedEnv.Meta.Truncated != fixture.Meta.Truncated {
		t.Errorf("meta.truncated = %v, want the fixture's %v", servedEnv.Meta.Truncated, fixture.Meta.Truncated)
	}
	if servedEnv.Meta.NextCursor != fixture.Meta.NextCursor {
		t.Errorf("meta.nextCursor = %q, want the fixture's %q — this route does not page",
			servedEnv.Meta.NextCursor, fixture.Meta.NextCursor)
	}
	if servedEnv.Meta.GeneratedAt.IsZero() {
		t.Error("meta.generatedAt is zero: the handler must stamp every response")
	}
}

// TestQueryPlansEmptySourceIsNotFound is the default's answer, and the one
// behaviour this route and the detail route share that the list route does not.
// The two sibling defaults disagree on purpose — a list with no store behind it
// can answer 200 with an empty page, a keyed question cannot — and the comment on
// emptyQueryPlansSource is the argument. This test is what keeps a future edit
// from "fixing" the disagreement by making all three answer 200.
func TestQueryPlansEmptySourceIsNotFound(t *testing.T) {
	s := newQueryPlansServer(t, nil)
	row := fixtureRow(t, "8439")
	target := plansPathFor(t, "8439")

	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET %s = %d, want 404\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotFound {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrNotFound)
	}
	// The message must name the triple it looked for: "no row for that fingerprint"
	// without the triple is an error a client cannot act on, and it is the only
	// place the three inputs are reported together.
	for _, want := range []string{row.Namespace, row.Cluster, row.Fingerprint} {
		if !strings.Contains(body.Error.Message, want) {
			t.Errorf("message %q does not name %q", body.Error.Message, want)
		}
	}
}

func TestQueryPlansParameterValidation(t *testing.T) {
	// The two parameter failures, both checked before readiness so a malformed
	// request never gets the "try again later" answer for something that will
	// never succeed. The server is not ready in this test on purpose: a 503 here
	// would mean the checks were reordered, which is the bug the test exists for.
	st := &stubQueryPlans{err: errPlansShouldNotBeCalled}
	s := New(Config{Listen: "127.0.0.1:0", QueryPlans: st})

	const fp = "843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3"
	cases := []struct {
		target string
		want   string
	}{
		{"/api/v1/insights/queries/%20/plans", "fingerprint is required"},
		{"/api/v1/insights/queries/" + fp + "/plans", "namespace and cluster are required"},
		{"/api/v1/insights/queries/" + fp + "/plans?namespace=open-webui", "namespace and cluster are required"},
		{"/api/v1/insights/queries/" + fp + "/plans?cluster=open-webui-pg", "namespace and cluster are required"},
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

// errPlansShouldNotBeCalled is a sentinel for a source that must not be consulted;
// a stub returning it turns "the handler asked the store" into a test failure even
// when the status code would otherwise look right. It is the plans file's own copy
// because query_detail_test.go's errShouldNotBeCalled carries that route's message
// in its text, and a shared string would make a failure in one file read like a
// failure in the other.
var errPlansShouldNotBeCalled = errors.New("query plans source consulted but should not have been")

func TestQueryPlansNotReady(t *testing.T) {
	// R8.7's single readiness signal, the same value /readyz answers with: a
	// server that has not synced must not answer 404 for a query it has not read
	// yet. The fingerprint and the pair are both valid here, so the only thing
	// that can answer is the gate.
	s := New(Config{Listen: "127.0.0.1:0", QueryPlans: &stubQueryPlans{err: errPlansShouldNotBeCalled}})
	target := "/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3/plans?namespace=open-webui&cluster=open-webui-pg"

	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s = %d, want 503\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
}

func TestQueryPlansSourceErrors(t *testing.T) {
	// The two answers a real source adds, distinguished the way R8.5's code set
	// exists to distinguish them: "the store answered and has nothing" is 404
	// NotFound, "the store did not answer" is 503 Unavailable. A single `err != nil`
	// branch answering 503 for both would make every query without captured plans
	// look like an outage, and answering 404 for both would make an unreadable
	// store look like an empty one.
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantStr  string
	}{
		{"bare sentinel", ErrQueryPlansNotFound, http.StatusNotFound, dto.ErrNotFound},
		{"wrapped sentinel", fmt.Errorf("store: no row in open-webui/open-webui-pg: %w", ErrQueryPlansNotFound),
			http.StatusNotFound, dto.ErrNotFound},
		{"other error", errors.New("sqlite: no such table: plan_samples"), http.StatusServiceUnavailable, dto.ErrUnavailable},
	}
	for _, tc := range cases {
		st := &stubQueryPlans{err: tc.err}
		s := newQueryPlansServer(t, st)
		target := "/api/v1/insights/queries/843923b9a0257cf80f1dff554e7dc8fdfc05f489328e8376513124dfb42996e3/plans?namespace=open-webui&cluster=open-webui-pg"

		rec := do(t, s, http.MethodGet, target)
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

func TestQueryPlansDoesNotShadowOtherRoutes(t *testing.T) {
	// The routing claim in query_plans.go's header, tested rather than asserted.
	// Three patterns now share /api/v1/insights/queries and this test pins all
	// three resolutions in one server:
	//
	//   - /insights/queries/series          → the literal, R8.11's batched route
	//   - /insights/queries/{fingerprint}   → the detail route
	//   - /insights/queries/{fingerprint}/plans → this route, including with
	//     fingerprint "series", which the literal does not claim because the
	//     literal has no /plans suffix.
	//
	// If the patterns ever reorder, one of these three fails, and which one fails
	// says which pair collided.
	_, plans := loadQueryPlansFixture(t)
	st := &stubQueryPlans{plans: plans}
	// The detail stub needs a *view*, not an error: the detail URL does consult this
	// source, so a stub whose answer is "should not have been called" answers 503
	// there and the test can no longer tell a routing failure from a store failure.
	// The error stub belongs on the source that must *not* be consulted — here that
	// is the plans source, checked against the series and detail URLs below.
	detail := &stubQueryDetail{view: sampleDetail(t, "8439")}
	s := New(Config{
		Listen:      "127.0.0.1:0",
		Series:      &stubSeries{},
		QueryDetail: detail,
		QueryPlans:  st,
	})
	s.readiness.SetListening(true)

	// The literal series route still answers its own handler.
	rec := do(t, s, http.MethodGet, "/api/v1/insights/queries/series?fingerprints="+testFingerprint)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/insights/queries/series = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if st.called != 0 {
		t.Errorf("plans source called %d times for the series URL, want 0 — the wildcard matched a path the literal owns", st.called)
	}

	// The detail route still answers its own handler.
	detailTarget := detailPathFor(t, "8439")
	if rec := do(t, s, http.MethodGet, detailTarget); rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", detailTarget, rec.Code, rec.Body.String())
	}
	if detail.called != 1 {
		t.Errorf("detail source called %d times for %s, want 1", detail.called, detailTarget)
	}

	// And /insights/queries/series/plans is *this* route's, with fingerprint
	// "series" — the collision query_plans.go records. It is only harmless because
	// R3.4 makes fingerprints hashes, so "series" is not one; the handler asks the
	// source for it and gets whatever answer the store has.
	st2 := &stubQueryPlans{plans: plans}
	s2 := New(Config{Listen: "127.0.0.1:0", QueryPlans: st2})
	s2.readiness.SetListening(true)
	rec = do(t, s2, http.MethodGet, "/api/v1/insights/queries/series/plans?namespace=open-webui&cluster=open-webui-pg")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /insights/queries/series/plans = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if st2.lastFingerprint != "series" {
		t.Errorf("fingerprint = %q, want \"series\" — the path parameter is not being read from the {fingerprint} segment", st2.lastFingerprint)
	}
}

// ---------------------------------------------------------------------------
// document pairing (R14.7)
// ---------------------------------------------------------------------------

func TestQueryPlansRouteIsDocumented(t *testing.T) {
	// The test this route exists for. Until this commit the path's only home was
	// api/x-planned-paths.yaml — R8.1b's list of routes that are named but not
	// written — and a route can sit there forever because nothing checks it. Now
	// the route is in code, so R14.7 requires it under `paths`, and the disjointness
	// rule requires it *gone* from the manifest: a planned path that is implemented
	// is a second name for a route that exists, and R14.7 counts one.
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + QueryPlansPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			QueryPlansPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", QueryPlansPath)
		}
		// The 200 body must be the type the handler serves. handleQueryPlans
		// encodes a QueryPlansEnvelope and the doc must $ref exactly that name —
		// the R16.8 argument applied to the response type, the way the detail
		// route's test pins QueryDetailView and the list's pins QueriesEnvelope.
		if !strings.Contains(item, "$ref: '#/components/schemas/QueryPlansEnvelope'") {
			t.Errorf("%s's responses do not $ref QueryPlansEnvelope", QueryPlansPath)
		}
		// The three parameters are inline, not $refs: the shared Namespace and
		// Cluster components say required: false and this route requires both, so
		// a $ref here would document an optional parameter the handler rejects.
		for _, p := range []string{"name: fingerprint", "name: namespace", "name: cluster"} {
			if !strings.Contains(item, p) {
				t.Errorf("%s's parameters do not include an inline %q", QueryPlansPath, p)
			}
		}
		if strings.Contains(item, "$ref: '#/components/parameters/Namespace'") ||
			strings.Contains(item, "$ref: '#/components/parameters/Cluster'") {
			t.Errorf("%s $refs the shared Namespace/Cluster parameters, which are optional; this route requires both",
				QueryPlansPath)
		}
	}

	// The manifest half: gone, not marked. This is the half that used to be a
	// separate test per route and is now enforced by the generator itself
	// (openapi_gen.go re-emits the manifest verbatim and
	// TestGeneratedOpenAPI byte-compares), so this assertion is a restatement of
	// what the generator guarantees — kept because it names the failure in the
	// file where the route lives, which is where a future editor looks.
	//
	// Over the `- ` lines, not the section's text: the retirement note this commit
	// wrote names the route in prose, exactly as the notes the detail and series
	// routes left behind name theirs, and a substring check would fail on that
	// prose forever (query_detail_test.go's version spells the same warning). The
	// comment carries no route; only an entry does.
	for _, line := range strings.Split(planned, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		fields := strings.Fields(line[2:])
		if len(fields) > 0 && fields[0] == QueryPlansPath {
			t.Errorf("%s is still in x-planned-paths — the route is implemented, and the two lists must be disjoint (R8.1c)",
				QueryPlansPath)
		}
	}
}
