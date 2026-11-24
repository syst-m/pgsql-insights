// Tests for R4.1's session list — GET /api/v1/active-queries.
//
// Two risks in this route, and the file is built around them rather than around
// line coverage:
//
//   - the *action column*. R4.1 says the Action column "renders disabled with a
//     link to the R12.8 gate whenever --allow-actions=false (the default), so the
//     column is never silently blank", and the same session appears in the response
//     up to four times — `data`, `scorecard.longestQuery`,
//     `scorecard.longestIdleInTransaction`, and a chain's `blocked` or `blockers`.
//     Each is a copy, so filling one and not the others puts two different `action`
//     values on the wire for one pid. TestActiveQueriesActionColumnIsConsistent
//     walks every site in both gate states, which is the only thing that catches a
//     fifth site being added without the walk being extended.
//   - the *doc pairing*. R14.7 forbids a route in code the document does not list
//     and a documented route that 404s, and this path spent its life in
//     api/x-planned-paths.yaml under the wrong requirement id (`# R3.11`, which is
//     requirements.md:316's plan-change detection). TestActiveQueriesRouteIsDocumented
//     requires the path under `paths` with the ActiveQueriesResponse $ref *and*
//     requires it gone from the manifest, so the handler and the document cannot be
//     moved apart in different commits and the id cannot silently revert.
//
// The rest covers the four answers handleActiveQueries can give: 200 from a source
// with rows, 200 from the default empty source, 400 for each parameter failure
// (checked before readiness), and 503 for not-ready and for a source that errors.
// Unlike the keyed routes there is no 404 here — a cluster with no backends is a
// cluster that exists — so the error table has one 503 row and no 404 row, which is
// itself the assertion.
//
// There is no fixture-canonicality test here for the same reason
// query_plans_test.go has none: dto/sessions_test.go decodes
// testdata/api/active-queries.json, byte-compares it, and checks the row set
// against R4.1's column sentence; a second copy here would be R16.8's failure
// inside the test suite. What this file checks is that the *served* body is the
// fixture — the handler forwarding its source rather than re-spelling it.
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
	"time"

	openapi "github.com/frankellis/pgsql-insights/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// activeQueriesFixturePath is R4.1's fixture at the location R16.8 fixes
// (repo-root testdata/api/), reached from this package's directory the way
// fixturePath and queryPlansFixturePath are.
const activeQueriesFixturePath = "../../testdata/api/active-queries.json"

// loadActiveQueriesFixture reads the fixture as the response type the route serves.
// The handler overwrites `meta` on every request, so the fixture's meta is not
// asserted here — TestActiveQueriesHandlerOwnsMeta covers it — but the rows, the
// scorecard and the chains are, because those are what the handler forwards.
func loadActiveQueriesFixture(t *testing.T) dto.ActiveQueriesResponse {
	t.Helper()
	raw, err := os.ReadFile(activeQueriesFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", activeQueriesFixturePath, err)
	}
	var v dto.ActiveQueriesResponse
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse %s: %v\n%s", activeQueriesFixturePath, err, raw)
	}
	if len(v.Data) == 0 {
		t.Fatalf("%s has no sessions", activeQueriesFixturePath)
	}
	return v
}

// stubActiveQueries is an ActiveQueriesSource with a fixed answer, recording the
// pair it was asked for so the tests can assert the handler read both halves out of
// the query string rather than re-deriving them (the stubQueryPlans pattern).
type stubActiveQueries struct {
	resp dto.ActiveQueriesResponse
	err  error

	called        int
	lastNamespace string
	lastCluster   string
}

func (st *stubActiveQueries) ActiveQueries(_ context.Context, namespace, cluster string) (dto.ActiveQueriesResponse, error) {
	st.called++
	st.lastNamespace, st.lastCluster = namespace, cluster
	return st.resp, st.err
}

// newActiveQueriesServer builds a ready Server whose only subsystem is the session
// source, with R12.8's action set set explicitly rather than defaulted so a test
// can exercise both sides of the gate. A process with no Fleet is ready once it is
// listening (R8.7(ii)), which is what makes the 200 paths reachable without a
// discovery stub.
func newActiveQueriesServer(t *testing.T, src ActiveQueriesSource, actions []string) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", ActiveQueries: src, Actions: actions})
	s.readiness.SetListening(true)
	return s
}

// activeQueriesTarget returns the request target for the route, using the
// namespace/name pair from the clusters fixture rather than a literal. The pair is
// the same one the cluster-detail and query-list tests use, so a cluster name in a
// test and a cluster name in the contract cannot disagree — and so the two required
// parameters are exercised with one pair everywhere.
func activeQueriesTarget(t *testing.T) string {
	t.Helper()
	env, _ := loadFixture(t)
	if len(env.Data) == 0 {
		t.Fatalf("%s has no clusters", fixturePath)
	}
	c := env.Data[0]
	return ActiveQueriesPath + "?namespace=" + c.Namespace + "&cluster=" + c.Name
}

// getActiveQueries performs the request and returns the body, failing on a non-200.
// Every 200-path assertion goes through it so a body that stops being valid JSON
// fails once, with the body quoted, rather than in several places.
func getActiveQueries(t *testing.T, s *Server, target string) string {
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

func TestActiveQueriesRouteRegistered(t *testing.T) {
	// The registration itself: server_test.go asserts the URL answers something;
	// this asserts it is *this* handler, by decoding the body as
	// dto.ActiveQueriesResponse — a shape only this route serves. The four keys are
	// the giveaway: every other list route answers Envelope[T], which has two.
	fixture := loadActiveQueriesFixture(t)
	st := &stubActiveQueries{resp: fixture}
	s := newActiveQueriesServer(t, st, nil)

	target := activeQueriesTarget(t)
	body := getActiveQueries(t, s, target)
	if !json.Valid([]byte(body)) {
		t.Fatalf("body is not valid JSON: %s", body)
	}
	var got dto.ActiveQueriesResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode ActiveQueriesResponse: %v\nbody: %s", err, body)
	}

	// Both halves of the pair must reach the source from the query string. A
	// handler that dropped one, or read the cluster out of the path, would still
	// answer 200 here if it were written to ignore its arguments.
	if st.called != 1 {
		t.Fatalf("source called %d times, want 1", st.called)
	}
	env, _ := loadFixture(t)
	if st.lastNamespace != env.Data[0].Namespace || st.lastCluster != env.Data[0].Name {
		t.Errorf("source asked for %s/%s, want %s/%s",
			st.lastNamespace, st.lastCluster, env.Data[0].Namespace, env.Data[0].Name)
	}
}

func TestActiveQueriesBodyIsTheFixture(t *testing.T) {
	// The handler forwards its source's rows, scorecard and chains rather than
	// rebuilding them. The comparison is on the encoded JSON of each section, not on
	// the structs, because the sections are what the UI imports: a field the handler
	// recomputed differently would encode differently, and a field it dropped would
	// too. `meta` is excluded — the handler owns it (TestActiveQueriesHandlerOwnsMeta).
	fixture := loadActiveQueriesFixture(t)
	st := &stubActiveQueries{resp: fixture}
	s := newActiveQueriesServer(t, st, nil)

	body := getActiveQueries(t, s, activeQueriesTarget(t))
	var got dto.ActiveQueriesResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}

	dataJSON, err := json.Marshal(got.Data)
	if err != nil {
		t.Fatal(err)
	}
	wantData, err := json.Marshal(fixture.Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(dataJSON) != string(wantData) {
		t.Errorf("served `data` is not the fixture's\n got %s\nwant %s", dataJSON, wantData)
	}

	scoreJSON, err := json.Marshal(got.Scorecard)
	if err != nil {
		t.Fatal(err)
	}
	wantScore, err := json.Marshal(fixture.Scorecard)
	if err != nil {
		t.Fatal(err)
	}
	if string(scoreJSON) != string(wantScore) {
		t.Errorf("served `scorecard` is not the fixture's\n got %s\nwant %s", scoreJSON, wantScore)
	}

	chainJSON, err := json.Marshal(got.BlockingChains)
	if err != nil {
		t.Fatal(err)
	}
	wantChains, err := json.Marshal(fixture.BlockingChains)
	if err != nil {
		t.Fatal(err)
	}
	if string(chainJSON) != string(wantChains) {
		t.Errorf("served `blockingChains` is not the fixture's\n got %s\nwant %s", chainJSON, wantChains)
	}
}

func TestActiveQueriesDefaultSourceIsAnEmptyList(t *testing.T) {
	// The route follows the list routes (emptyQuerySource), not the keyed ones
	// (emptyQueryDetailSource): "no sessions" is a complete answer to "what sessions
	// are on this cluster", so the default answers 200 with empty sections rather
	// than 404. The assertion is on the raw text as well as the decoded value,
	// because dto's contract is that its list and map fields are never null —
	// `[]` and `null` are two encodings of one fact and the UI would have to handle
	// both, which is the reason emptyActiveQueriesSource initialises three fields
	// instead of returning a zero value.
	s := newActiveQueriesServer(t, nil, nil)
	body := getActiveQueries(t, s, activeQueriesTarget(t))

	for _, want := range []string{`"data":[]`, `"blockingChains":[]`, `"byState":{}`} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %s (a null would be the second encoding):\n%s", want, body)
		}
	}
	if strings.Contains(body, "null") {
		t.Errorf("body contains a null, and dto's list/map fields are never null:\n%s", body)
	}

	var got dto.ActiveQueriesResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}
	if got.Meta.Total != 0 || got.Meta.Truncated {
		t.Errorf("meta = %+v, want total 0 and truncated false for an empty list", got.Meta)
	}
}

func TestActiveQueriesParameterValidation(t *testing.T) {
	// The two parameter failures, both checked before readiness so a malformed
	// request never gets the "try again later" answer for something that will never
	// succeed. The server is not ready in this test on purpose: a 503 here would
	// mean the checks were reordered, which is the bug the test exists for.
	//
	// Both parameters are required here and optional in the shared components —
	// pg_stat_activity is per-instance, so there is no fleet-wide session list to
	// fall back to. That asymmetry is why the document declares them inline, and
	// this test is the code-side half of that claim.
	st := &stubActiveQueries{err: errActiveQueriesShouldNotBeCalled}
	s := New(Config{Listen: "127.0.0.1:0", ActiveQueries: st})

	cases := []struct {
		target string
		want   string
	}{
		{ActiveQueriesPath, "namespace and cluster are required"},
		{ActiveQueriesPath + "?namespace=open-webui", "namespace and cluster are required"},
		{ActiveQueriesPath + "?cluster=open-webui-pg", "namespace and cluster are required"},
		{ActiveQueriesPath + "?namespace=%20&cluster=open-webui-pg", "namespace and cluster are required"},
		{ActiveQueriesPath + "?namespace=open-webui&cluster=%20", "namespace and cluster are required"},
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

// errActiveQueriesShouldNotBeCalled is a sentinel for a source that must not be
// consulted; a stub returning it turns "the handler asked the store" into a test
// failure even when the status code would otherwise look right. It is this file's
// own copy because query_plans_test.go's errPlansShouldNotBeCalled carries that
// route's message in its text, and a shared string would make a failure in one
// file read like a failure in the other.
var errActiveQueriesShouldNotBeCalled = errors.New("active queries source consulted but should not have been")

func TestActiveQueriesNotReady(t *testing.T) {
	// R8.7's single readiness signal, the same value /readyz answers with: a server
	// that has not synced must not answer "no sessions" for a cluster it has not
	// read yet. Both parameters are valid here, so the only thing that can answer is
	// the gate.
	s := New(Config{Listen: "127.0.0.1:0", ActiveQueries: &stubActiveQueries{err: errActiveQueriesShouldNotBeCalled}})
	target := activeQueriesTarget(t)

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

func TestActiveQueriesSourceErrors(t *testing.T) {
	// One row, where the keyed routes have two. R8.5's code set distinguishes "the
	// store answered and has nothing" (404 NotFound) from "the store did not answer"
	// (503 Unavailable), and this route has no first case to distinguish: a cluster
	// with no backends answers 200 with an empty list (the empty-source test above),
	// so there is no not-found answer to give and the document declares no 404
	// response for it. Everything that is not a 200 after the parameter and
	// readiness checks is therefore an unavailable, and the test asserts the single
	// branch rather than a table so that adding a 404 here later is a visible change.
	st := &stubActiveQueries{err: errors.New("pg_stat_activity: connection refused")}
	s := newActiveQueriesServer(t, st, nil)

	target := activeQueriesTarget(t)
	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s = %d, want 503\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrUnavailable {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrUnavailable)
	}
	if !strings.Contains(body.Error.Message, "connection refused") {
		t.Errorf("message %q does not carry the source's error text", body.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// R4.1's Action column / R12.8's gate
// ---------------------------------------------------------------------------

// actionSites returns every place a session's `action` appears in a response,
// labelled. The list is the test: R4.1's Action column is one column, and the same
// session is rendered in up to four places, so a response where the table's row says
// `disabled` and the scorecard's copy of the same pid says `terminate` is the
// second-copy failure in miniature. If a fifth site is ever added to the DTO, it
// belongs in this function, and forgetting to add it is what fails here.
func actionSites(t *testing.T, v dto.ActiveQueriesResponse) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i := range v.Data {
		out[fmt.Sprintf("data[%d].action (pid %d)", i, v.Data[i].Pid)] = v.Data[i].Action
	}
	if v.Scorecard.LongestQuery != nil {
		out[fmt.Sprintf("scorecard.longestQuery.action (pid %d)", v.Scorecard.LongestQuery.Pid)] =
			v.Scorecard.LongestQuery.Action
	}
	if v.Scorecard.LongestIdleInTransaction != nil {
		out[fmt.Sprintf("scorecard.longestIdleInTransaction.action (pid %d)", v.Scorecard.LongestIdleInTransaction.Pid)] =
			v.Scorecard.LongestIdleInTransaction.Action
	}
	for i := range v.BlockingChains {
		out[fmt.Sprintf("blockingChains[%d].blocked.action (pid %d)", i, v.BlockingChains[i].Blocked.Pid)] =
			v.BlockingChains[i].Blocked.Action
		for j := range v.BlockingChains[i].Blockers {
			b := v.BlockingChains[i].Blockers[j]
			out[fmt.Sprintf("blockingChains[%d].blockers[%d].action (pid %d)", i, j, b.Pid)] = b.Action
		}
	}
	return out
}

func TestActiveQueriesActionColumnIsConsistent(t *testing.T) {
	// Both sides of R12.8's gate, because R4.1's sentence is about the off state and
	// the on state is the one a reader would assume works. The off state is the
	// default, so it is what every stock server answers; R4.1's rule is that the
	// column "renders disabled with a link to the R12.8 gate … so the column is
	// never silently blank", and an empty string is exactly the silent blank that
	// sentence forbids.
	fixture := loadActiveQueriesFixture(t)

	cases := []struct {
		name    string
		actions []string
		want    string
	}{
		{"gate off (the default)", nil, ActionDisabled},
		{"gate off, empty non-nil list", []string{}, ActionDisabled},
		{"gate on for terminate", []string{ActionNameTerminate}, ActionNameTerminate},
		// R12.8 generalised the flag from "terminate session only" to a set in which
		// each mutation is "individually named in the flag's value", so a set that
		// enables some *other* action must not switch this column on. That is the
		// half a boolean Config field could not express.
		{"gate on for a different action", []string{"explain"}, ActionDisabled},
		{"gate on for terminate among others", []string{"explain", ActionNameTerminate}, ActionNameTerminate},
	}

	for _, tc := range cases {
		// A fresh copy per case: applyActions mutates, and sharing one fixture value
		// across cases would let the first case's write satisfy the last one.
		resp := fixture
		st := &stubActiveQueries{resp: resp}
		s := newActiveQueriesServer(t, st, tc.actions)

		body := getActiveQueries(t, s, activeQueriesTarget(t))
		var got dto.ActiveQueriesResponse
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("%s: decode: %v\nbody: %s", tc.name, err, body)
		}
		sites := actionSites(t, got)
		if len(sites) == 0 {
			t.Fatalf("%s: no action sites found in the response", tc.name)
		}
		for where, gotAction := range sites {
			if gotAction != tc.want {
				t.Errorf("%s: %s = %q, want %q", tc.name, where, gotAction, tc.want)
			}
		}
	}
}

func TestActiveQueriesActionForIsTheWholeGate(t *testing.T) {
	// actionFor is the field's only implementation (the collector leaves it empty
	// because the value "depends on a runtime flag on the API server … not a fact
	// about the session"). Table form rather than only the end-to-end version above,
	// because the function is where the membership test lives and a rewrite that
	// reads a bool instead of the list would keep the end-to-end cases passing for
	// the two shapes they share.
	cases := []struct {
		actions []string
		want    string
	}{
		{nil, ActionDisabled},
		{[]string{}, ActionDisabled},
		{[]string{"explain"}, ActionDisabled},
		{[]string{ActionNameTerminate}, ActionNameTerminate},
		{[]string{"explain", ActionNameTerminate}, ActionNameTerminate},
		{[]string{"terminate "}, ActionDisabled}, // exact match, not prefix or case-insensitive
		{[]string{"Terminate"}, ActionDisabled},
	}
	for _, tc := range cases {
		if got := actionFor(tc.actions, ActionNameTerminate); got != tc.want {
			t.Errorf("actionFor(%v, %q) = %q, want %q", tc.actions, ActionNameTerminate, got, tc.want)
		}
	}
}

func TestActiveQueriesHandlerOwnsMeta(t *testing.T) {
	// The division pageQueries makes for the queries list: the source returns rows
	// and the handler fills `meta`, because generatedAt is a property of the
	// response, not of the round that produced the rows. A source that set meta and
	// a handler that forwarded it would serve a timestamp from whenever the source
	// cached the value — and on this route `total` would be the source's, which for
	// a route that does not page must equal len(data).
	fixture := loadActiveQueriesFixture(t)
	stale := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	resp := fixture
	resp.Meta = dto.Meta{Total: 999, Truncated: true, NextCursor: "should-not-appear", GeneratedAt: stale}
	st := &stubActiveQueries{resp: resp}
	s := newActiveQueriesServer(t, st, nil)

	body := getActiveQueries(t, s, activeQueriesTarget(t))
	var got dto.ActiveQueriesResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}

	if got.Meta.Total != len(fixture.Data) {
		t.Errorf("meta.total = %d, want %d (len(data)); the source's 999 was forwarded",
			got.Meta.Total, len(fixture.Data))
	}
	if got.Meta.Truncated {
		t.Error("meta.truncated = true, want false: the route does not page, so the flag is false by construction")
	}
	if got.Meta.NextCursor != "" {
		t.Errorf("meta.nextCursor = %q, want empty for a route that does not page", got.Meta.NextCursor)
	}
	if got.Meta.GeneratedAt.Equal(stale) {
		t.Errorf("meta.generatedAt = %s, the source's value: the handler must stamp the response it is serving", stale)
	}
	if time.Since(got.Meta.GeneratedAt) > time.Minute {
		t.Errorf("meta.generatedAt = %s, more than a minute old", got.Meta.GeneratedAt)
	}
}

// ---------------------------------------------------------------------------
// doc pairing (R14.7)
// ---------------------------------------------------------------------------

func TestActiveQueriesRouteIsDocumented(t *testing.T) {
	// The test this route exists for. Until this commit the path's only home was
	// api/x-planned-paths.yaml — where it carried the wrong requirement id, `# R3.11`,
	// which is requirements.md:316's plan-change detection and not a session list —
	// and a route can sit there forever because nothing checks it. Now the route is
	// in code, so R14.7 requires it under `paths`, and the disjointness rule requires
	// it *gone* from the manifest: a planned path that is implemented is a second name
	// for a route that exists, and R14.7 counts one.
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + ActiveQueriesPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			ActiveQueriesPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", ActiveQueriesPath)
		}
		// The 200 body must be the type the handler serves. handleActiveQueries
		// encodes a dto.ActiveQueriesResponse and the doc must $ref exactly that
		// name — the R16.8 argument applied to the response type, the way the plans
		// route's test pins QueryPlansEnvelope and the list's pins QueriesResponse.
		if !strings.Contains(item, "$ref: '#/components/schemas/ActiveQueriesResponse'") {
			t.Errorf("%s's responses do not $ref ActiveQueriesResponse", ActiveQueriesPath)
		}
		// Both parameters are inline, not $refs: the shared Namespace and Cluster
		// components say required: false and this route requires both, so a $ref
		// here would document an optional parameter the handler rejects.
		for _, p := range []string{"name: namespace", "name: cluster"} {
			if !strings.Contains(item, p) {
				t.Errorf("%s's parameters do not include an inline %q", ActiveQueriesPath, p)
			}
		}
		if strings.Contains(item, "$ref: '#/components/parameters/Namespace'") ||
			strings.Contains(item, "$ref: '#/components/parameters/Cluster'") {
			t.Errorf("%s $refs the shared Namespace/Cluster parameters, which are optional; this route requires both",
				ActiveQueriesPath)
		}
		// No 404: the route has no not-found answer to give (see
		// TestActiveQueriesSourceErrors), so a documented 404 here would be a
		// response the code cannot produce.
		if strings.Contains(item, "404:") {
			t.Errorf("%s documents a 404, which this route cannot answer: an empty cluster is a 200 with empty data",
				ActiveQueriesPath)
		}
	}

	// The manifest half: gone, not marked. The retirement note this commit wrote
	// names the route in prose, so the check is over the `- ` entry lines only — a
	// substring check would fail on that prose forever, which is the warning
	// query_detail_test.go's version spells out.
	for _, line := range strings.Split(planned, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		fields := strings.Fields(line[2:])
		if len(fields) > 0 && fields[0] == ActiveQueriesPath {
			t.Errorf("%s is still in x-planned-paths — the route is implemented, and the two lists must be disjoint (R8.1c)",
				ActiveQueriesPath)
		}
	}
}
