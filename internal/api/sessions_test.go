// Tests for R4.5's terminate action — POST /api/v1/sessions/{pid}/terminate.
//
// Two risks in this route, and the file is built around them rather than around
// line coverage:
//
//   - the *gate ordering*. R4.5's action is gated by R12.8's --allow-actions,
//     and the route also has a readiness gate, and the two answer different
//     questions: one is permanent (this process does not perform actions), the
//     other transient (this process has not synced yet). Checking readiness first
//     would answer 503 during the pre-sync window to a request whose real answer
//     is 403 — a transient answer to a permanent question.
//     TestSessionsTerminateGatePrecedesReadiness pins the order by building a
//     server that is *not* ready and *not* allowed and requiring the 403.
//   - the *doc pairing*. R14.7 forbids a route in code the document does not list
//     and a documented route that 404s, and it forbids the same route in `paths`
//     and in x-planned-paths. This path moves between the two in this commit, so
//     TestSessionsTerminateRouteIsDocumented asserts both halves at once: the
//     entry under `paths` with the TerminateResponse $ref, and the r4.5-actions
//     key gone from the manifest. A commit that registers the route and forgets
//     the manifest edit fails here, which is the whole point.
//
// The rest is the answer table: 200 from a source that says yes and one that says
// no (both real answers, and the body distinguishes them), 400 for each parameter
// failure including the missing confirm, 403 for the two refusals, 503 for
// not-ready and for a source that errors. There is no 404 — the route names a pid
// and the default source cannot say a session is absent, only that it cannot ask.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openapi "github.com/frankellis/pgsql-insights/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// stubTerminate is a TerminateSource with a fixed answer, recording all three
// arguments so the tests can assert the handler read the pid out of the path and
// the pair out of the query string rather than re-deriving either (the
// stubActiveQueries pattern).
type stubTerminate struct {
	terminated bool
	err        error

	called      int
	lastPID     int
	lastNS      string
	lastCluster string
}

func (st *stubTerminate) TerminateSession(_ context.Context, namespace, cluster string, pid int) (bool, error) {
	st.called++
	st.lastPID = pid
	st.lastNS = namespace
	st.lastCluster = cluster
	return st.terminated, st.err
}

// newTerminateServer builds a server with the route's two configuration inputs set:
// the source and the action list. It is listening, so readiness passes and the
// only variables left are the ones each test names — the way
// newActiveQueriesServer is built for the same reason.
func newTerminateServer(t *testing.T, src TerminateSource, actions []string) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", Terminate: src, Actions: actions})
	s.readiness.SetListening(true)
	return s
}

// terminateTarget builds the request target for a valid R4.5 call: the fixture's
// namespace/name pair (the same pair every other route test uses, so a cluster
// name in a test and a cluster name in the contract cannot disagree) with a pid
// that matches the sessions fixture's first row, so the pid the UI would have
// taken the action on and the pid the test asks about are one value.
func terminateTarget(t *testing.T, pid string, extra string) string {
	t.Helper()
	env, _ := loadFixture(t)
	if len(env.Data) == 0 {
		t.Fatalf("%s has no clusters", fixturePath)
	}
	q := "namespace=" + env.Data[0].Namespace + "&cluster=" + env.Data[0].Name + "&confirm=true"
	if extra != "" {
		q = extra
	}
	return fmt.Sprintf("%s/%s/terminate?%s", "/api/v1/sessions", pid, q)
}

// validTarget is the canonical valid call: the fixture cluster, the fixture's
// first session pid, confirm=true.
func validTarget(t *testing.T) string {
	t.Helper()
	return terminateTarget(t, "48213", "")
}

// doTerminate issues the POST with an Identity attached, because R4.5's audit
// requires an actor and the response body carries one. `do` builds a bare request
// with no identity, which is correct for the auth-off case but cannot test the
// actor field, so this attaches one the way the auth middleware would.
func doTerminate(t *testing.T, s *Server, target, user string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req = req.WithContext(WithIdentity(req.Context(), Identity{User: user}))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSessionsTerminateRouteRegistered(t *testing.T) {
	// The registration itself: the path answers something, and it is *this*
	// handler, proved by decoding the body as dto.TerminateResponse — a shape only
	// this route serves. server_test.go's URL table asserts the route exists; this
	// asserts the right one was registered, and that all three arguments reached
	// the source.
	st := &stubTerminate{terminated: true}
	s := newTerminateServer(t, st, []string{ActionNameTerminate})

	rec := doTerminate(t, s, validTarget(t), "system:serviceaccount:freeidp:pgsi-api")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s = %d, want 200\nbody: %s", validTarget(t), rec.Code, rec.Body.String())
	}
	var got dto.TerminateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode TerminateResponse: %v\nbody: %s", err, rec.Body.String())
	}
	if st.called != 1 {
		t.Fatalf("source called %d times, want 1", st.called)
	}

	// All three arguments must reach the source. A handler that dropped the
	// namespace, or read the pid out of the query string instead of the path, would
	// still answer 200 here if it were written to ignore its arguments.
	env, _ := loadFixture(t)
	if st.lastNS != env.Data[0].Namespace || st.lastCluster != env.Data[0].Name {
		t.Errorf("source asked for %s/%s, want %s/%s",
			st.lastNS, st.lastCluster, env.Data[0].Namespace, env.Data[0].Name)
	}
	if st.lastPID != 48213 {
		t.Errorf("source asked for pid %d, want 48213 (the path's {pid})", st.lastPID)
	}
}

func TestSessionsTerminateBodyIsTheRequestPlusOneFact(t *testing.T) {
	// The body's four fields come from exactly two places, and the split is the
	// design: pid, target and actor are echoes of the request (the URL and the
	// attached Identity), and terminated is the source's. A source cannot put a
	// wrong pid on the wire, because the handler does not ask it for one.
	st := &stubTerminate{terminated: true}
	s := newTerminateServer(t, st, []string{ActionNameTerminate})

	const actor = "system:serviceaccount:freeidp:pgsi-api"
	rec := doTerminate(t, s, validTarget(t), actor)
	var got dto.TerminateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode TerminateResponse: %v\nbody: %s", err, rec.Body.String())
	}
	env, _ := loadFixture(t)

	if got.Pid != 48213 {
		t.Errorf("pid = %d, want 48213 from the path", got.Pid)
	}
	if want := env.Data[0].Namespace + "/" + env.Data[0].Name; got.Target != want {
		t.Errorf("target = %q, want %q (namespace/cluster from the query string)", got.Target, want)
	}
	if got.Actor != actor {
		t.Errorf("actor = %q, want %q from the request Identity", got.Actor, actor)
	}
	if !got.Terminated {
		t.Error("terminated = false, want true (the source's answer)")
	}
}

func TestSessionsTerminateTable(t *testing.T) {
	// Every answer the route can give, in one table, so a new branch cannot be
	// added without a row. The two 403s are separate rows on purpose: they share a
	// status and a code and differ only in their message, which is where R8.5 puts
	// the distinction.
	env, _ := loadFixture(t)
	target := fmt.Sprintf("/api/v1/sessions/48213/terminate?namespace=%s&cluster=%s&confirm=true",
		env.Data[0].Namespace, env.Data[0].Name)

	tests := []struct {
		name    string
		actions []string
		src     TerminateSource
		ready   bool
		want    int
		code    string
	}{
		{"allowed, pg says yes", []string{ActionNameTerminate}, &stubTerminate{terminated: true}, true,
			http.StatusOK, ""},
		{"allowed, pg says no", []string{ActionNameTerminate}, &stubTerminate{terminated: false}, true,
			http.StatusOK, ""},
		{"gate off, actions empty", nil, nil, true,
			http.StatusForbidden, dto.ErrUnsupported},
		{"gate off, actions names another action", []string{"patch-parameters"}, &stubTerminate{}, true,
			http.StatusForbidden, dto.ErrUnsupported},
		{"gate on, not ready", []string{ActionNameTerminate}, &stubTerminate{}, false,
			http.StatusServiceUnavailable, dto.ErrNotReady},
		{"gate on, source unavailable", []string{ActionNameTerminate}, &stubTerminate{err: errors.New("dial tcp: refused")}, true,
			http.StatusServiceUnavailable, dto.ErrUnavailable},
		{"gate on, target is a replica", []string{ActionNameTerminate}, &stubTerminate{err: ErrNotPrimary}, true,
			http.StatusForbidden, dto.ErrUnsupported},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Config{Listen: "127.0.0.1:0", Terminate: tc.src, Actions: tc.actions})
			if tc.ready {
				s.readiness.SetListening(true)
			}
			rec := doTerminate(t, s, target, "tester")
			if rec.Code != tc.want {
				t.Fatalf("POST %s = %d, want %d\nbody: %s", target, rec.Code, tc.want, rec.Body.String())
			}
			if tc.code == "" {
				return
			}
			var body dto.ErrorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
			}
			if body.Error.Code != tc.code {
				t.Errorf("code = %q, want %q\nbody: %s", body.Error.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestSessionsTerminateGatePrecedesReadiness(t *testing.T) {
	// The ordering test. A server that is neither ready nor allowed must answer
	// 403, not 503: --allow-actions is a permanent property of this process and
	// readiness is a transient one, so the gate's answer has to win or the route
	// gives a transient answer to a permanent question during the pre-sync window.
	//
	// The stub's error is the one that would make a wrong order loud: if the
	// handler consulted the source before the gate, this test would see the
	// source's error surfaced as 503 instead of the gate's 403.
	s := New(Config{Listen: "127.0.0.1:0", Terminate: &stubTerminate{err: errors.New("source consulted before the gate")}})
	env, _ := loadFixture(t)
	target := fmt.Sprintf("/api/v1/sessions/48213/terminate?namespace=%s&cluster=%s&confirm=true",
		env.Data[0].Namespace, env.Data[0].Name)

	rec := doTerminate(t, s, target, "tester")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST %s on a not-ready, not-allowed server = %d, want 403\nbody: %s",
			target, rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrUnsupported {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrUnsupported)
	}
	if !strings.Contains(body.Error.Message, "--allow-actions") {
		t.Errorf("message = %q, want it to name --allow-actions", body.Error.Message)
	}
}

func TestSessionsTerminateParameterTable(t *testing.T) {
	// Every parameter failure, each checked before the ones behind it. The pid
	// comes from the path and the other three from the query string, so the table
	// covers both sources and the order between them.
	env, _ := loadFixture(t)
	ns := env.Data[0].Namespace
	cl := env.Data[0].Name

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"pid not a number",
			fmt.Sprintf("/api/v1/sessions/abc/terminate?namespace=%s&cluster=%s&confirm=true", ns, cl),
			"invalid pid"},
		{"pid negative",
			fmt.Sprintf("/api/v1/sessions/-1/terminate?namespace=%s&cluster=%s&confirm=true", ns, cl),
			"invalid pid"},
		{"pid zero",
			fmt.Sprintf("/api/v1/sessions/0/terminate?namespace=%s&cluster=%s&confirm=true", ns, cl),
			"invalid pid"},
		{"missing namespace",
			fmt.Sprintf("/api/v1/sessions/48213/terminate?cluster=%s&confirm=true", cl),
			"namespace and cluster are required"},
		{"missing cluster",
			fmt.Sprintf("/api/v1/sessions/48213/terminate?namespace=%s&confirm=true", ns),
			"namespace and cluster are required"},
		{"confirm absent",
			fmt.Sprintf("/api/v1/sessions/48213/terminate?namespace=%s&cluster=%s", ns, cl),
			"confirm=true is required"},
		{"confirm false",
			fmt.Sprintf("/api/v1/sessions/48213/terminate?namespace=%s&cluster=%s&confirm=false", ns, cl),
			"confirm=true is required"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The gate is ON for every row: a parameter failure must be reported as
			// a parameter failure even on a server that would otherwise perform the
			// action, which is the queries.go rule ("a malformed request is
			// malformed whether or not the store has data") with the gate on rather
			// than off.
			s := newTerminateServer(t, &stubTerminate{terminated: true}, []string{ActionNameTerminate})
			rec := doTerminate(t, s, tc.target, "tester")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST %s = %d, want 400\nbody: %s", tc.target, rec.Code, rec.Body.String())
			}
			var body dto.ErrorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
			}
			if body.Error.Code != dto.ErrInvalidRequest {
				t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrInvalidRequest)
			}
			if !strings.Contains(body.Error.Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", body.Error.Message, tc.want)
			}
		})
	}
}

func TestSessionsTerminateDefaultSource(t *testing.T) {
	// cfg.Terminate nil with the gate on: the route answers 503 Unavailable rather
	// than 404 or a panic. This is the keyed-source rule (a request that names a
	// pid and gets nothing has one true answer, and "no rows" is not it) and it is
	// the state a process that serves the API without collecting is in.
	s := New(Config{Listen: "127.0.0.1:0", Actions: []string{ActionNameTerminate}})
	s.readiness.SetListening(true)
	env, _ := loadFixture(t)
	target := fmt.Sprintf("/api/v1/sessions/48213/terminate?namespace=%s&cluster=%s&confirm=true",
		env.Data[0].Namespace, env.Data[0].Name)

	rec := doTerminate(t, s, target, "tester")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST %s = %d, want 503\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ErrorBody: %v\nbody: %s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrUnavailable {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrUnavailable)
	}
	if !strings.Contains(body.Error.Message, "SQL endpoint") {
		t.Errorf("message = %q, want it to name the missing SQL endpoint", body.Error.Message)
	}
}

func TestSessionsTerminateMethodIsPostOnly(t *testing.T) {
	// R4.5 names one verb. active_queries.go's header rules that terminate "is not
	// a verb on" R4.1's route, so the two requirements have two paths and each has
	// one method; a GET on this path must not answer 200 with a terminated body.
	env, _ := loadFixture(t)
	target := fmt.Sprintf("/api/v1/sessions/48213/terminate?namespace=%s&cluster=%s&confirm=true",
		env.Data[0].Namespace, env.Data[0].Name)
	s := newTerminateServer(t, &stubTerminate{terminated: true}, []string{ActionNameTerminate})

	if rec := do(t, s, http.MethodGet, target); rec.Code == http.StatusOK {
		t.Errorf("GET %s = 200; the route is POST-only, so GET must not perform or report the action\nbody: %s",
			target, rec.Body.String())
	}
}

func TestSessionsTerminateRouteIsDocumented(t *testing.T) {
	// The pairing half of R14.7, in both directions at once: the path must be under
	// `paths:` with the TerminateResponse $ref and an x-status, and it must be gone
	// from x-planned-paths. The two halves are one test because the defect they
	// prevent is the commit that does one and not the other.
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + SessionsTerminatePath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	at := strings.Index(paths, key)
	if at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			SessionsTerminatePath)
	}
	item := paths[at:]
	if next := strings.Index(item[2:], "\n  /"); next >= 0 {
		item = item[:next+2]
	}
	if !strings.Contains(item, "x-status: implemented") {
		t.Errorf("%s has no `x-status: implemented`", SessionsTerminatePath)
	}
	if !strings.Contains(item, "post:") {
		t.Errorf("%s has no `post:` operation — R4.5 names one verb", SessionsTerminatePath)
	}
	if !strings.Contains(item, "$ref: '#/components/schemas/TerminateResponse'") {
		t.Errorf("%s's responses do not $ref TerminateResponse", SessionsTerminatePath)
	}
	// The gate's answer must be documented too: a route that answers 403 for a
	// reason R12.8 controls needs the response in the document, or the client has
	// no documented way to learn the action is off.
	if !strings.Contains(item, "$ref: '#/components/responses/Unsupported'") {
		t.Errorf("%s does not $ref the Unsupported response — the route's main answer is 403 Unsupported (R4.5, R12.8)",
			SessionsTerminatePath)
	}

	// The manifest half: gone, not marked. The retirement note this commit writes
	// names the route in prose, so the check is over the `- ` entry lines only — a
	// substring check would fail on that prose forever, which is the warning
	// query_detail_test.go's version spells out.
	for _, line := range strings.Split(planned, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		fields := strings.Fields(line[2:])
		if len(fields) > 0 && fields[0] == SessionsTerminatePath {
			t.Errorf("%s is still in x-planned-paths — the route is implemented, and the two lists must be disjoint (R14.7)",
				SessionsTerminatePath)
		}
	}
}
