// Tests for R3.7's by-user rollup — GET /api/v1/insights/users.
//
// The file repeats the shape databases_test.go and tags_test.go establish for the two
// rollups this commit completes, because the shape is the answer to the two risks every
// paged list route shares — R8.4's paging contract (TestUsersPagination) and R14.7's
// doc pairing (TestUsersRouteIsDocumented) — and because a third copy of the *shape*
// costs nothing next to a third copy of a *contract*.
//
// What this file deliberately does NOT contain is a fixture-canonicality test.
// dto/dimensions_test.go:27's TestUsersFixtureIsCanonical already byte-compares
// testdata/api/users.json against the DTO's own marshalling; a second copy here would
// be R16.8's failure inside the test suite. The one fixture-derived assertion kept
// here is the one only this package can make: that the rows the *handler* emits are
// byte-identical to the fixture's rows (TestUsersRowJSONMatchesFixture).
//
// Two things about this route's fixture are worth stating where the tables read them.
// First, the fixture has no tie in any column — every one of the five rows carries a
// distinct value in each of the nine load columns — so unlike databases_test.go, no
// row of TestUsersSortKeys is a tie case, and the tiebreaker is exercised here only by
// the determinism check inside that loop (sort twice, require an identical order).
// Second, `openwebui` appears in this fixture as a *user* and in databases.json as a
// *database* (dto/dimensions_test.go:153's TestDimensionFixturesCoverTheOtherFixtures
// is what keeps the two files describing one fleet). That overlap is the reason
// TestUsersPassesTheParsedQueryToTheSource sets `db=openwebui` and `namespace`
// alongside it: the route's answer must be scoped by the query parameters, not by
// whatever the word in the row happens to also name.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	openapi "github.com/frankellis/pgsql-insights/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// usersFixturePath is R16.8's named location for this route's contract
// (repo-root testdata/api/users.json), reached from this package's directory. The same
// file is the SPA's Vitest fixture and the subject of
// dto.TestUsersFixtureIsCanonical; this is the third consumer.
const usersFixturePath = "../../testdata/api/users.json"

// loadUserFixture returns the frozen user rows.
func loadUserFixture(t *testing.T) []dto.UserView {
	t.Helper()
	raw, err := os.ReadFile(usersFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", usersFixturePath, err)
	}
	var env UsersEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse %s: %v", usersFixturePath, err)
	}
	if len(env.Data) == 0 {
		t.Fatalf("%s has no rows", usersFixturePath)
	}
	return env.Data
}

// stubUsers is a UsersSource that returns a fixed answer and records the query it was
// asked, so the tests can assert the handler passed the *parsed* query through rather
// than the raw query string (the stubTags/stubDatabases pattern).
type stubUsers struct {
	rows []dto.UserView
	err  error

	called int
	last   ListQuery
}

func (st *stubUsers) Users(_ context.Context, q ListQuery) ([]dto.UserView, error) {
	st.called++
	st.last = q
	// The stub hands back a copy: pageRows sorts the slice it is given in place, and
	// a source that let the caller sort its own backing array would corrupt the next
	// request's order.
	rows := make([]dto.UserView, len(st.rows))
	copy(rows, st.rows)
	return rows, st.err
}

// newUsersServer builds a ready Server whose only subsystem is R3.7's by-user source.
// A process with no Fleet is ready once it is listening (R8.7(ii)), which is what
// makes the 200 paths reachable without a discovery stub.
func newUsersServer(t *testing.T, src UsersSource) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", Users: src})
	s.readiness.SetListening(true)
	return s
}

// getUsers performs the request and decodes the 200 body, failing the test on either
// step, so a body that stops being valid JSON fails once with the body quoted.
func getUsers(t *testing.T, s *Server, target string) UsersEnvelope {
	t.Helper()
	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var env UsersEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: decode body: %v\nbody: %s", target, err, rec.Body.String())
	}
	return env
}

// ---------------------------------------------------------------------------
// route
// ---------------------------------------------------------------------------

func TestUsersRouteRegistered(t *testing.T) {
	// The registration itself. server_test.go asserts the same URL answers something;
	// this asserts it is *this* handler, by decoding the body into UsersEnvelope and
	// requiring the fixture's five `userName` values back — a response from the
	// databases route would decode to an empty Data here (no `userName` key), so the
	// length check below is what distinguishes the two.
	rows := loadUserFixture(t)
	env := getUsers(t, newUsersServer(t, &stubUsers{rows: rows}), UsersPath)

	if len(env.Data) != len(rows) {
		t.Fatalf("data has %d rows, want %d", len(env.Data), len(rows))
	}
	if env.Meta.Total != len(rows) {
		t.Errorf("meta.total = %d, want %d (R8.4: rows matching before the page was cut)",
			env.Meta.Total, len(rows))
	}
	if env.Meta.Truncated {
		t.Errorf("meta.truncated = true for %d rows at the default limit of %d",
			len(rows), DefaultLimit)
	}
	if env.Meta.NextCursor != "" {
		t.Errorf("meta.nextCursor = %q, want empty when the whole set fits in one page",
			env.Meta.NextCursor)
	}
}

// TestUsersRowJSONMatchesFixture is the cross-check only this package can make: the
// rows the handler emits are byte-identical to the fixture's rows. dto's test proves
// the struct marshals to the fixture; this proves the *route* serves them — a handler
// that built its own response map, dropped one of R3.2's shared columns, or reordered
// the embedded LoadColumns fails here and nowhere else.
func TestUsersRowJSONMatchesFixture(t *testing.T) {
	rows := loadUserFixture(t)
	env := getUsers(t, newUsersServer(t, &stubUsers{rows: rows}), UsersPath)

	if len(env.Data) != len(rows) {
		t.Fatalf("served %d rows, fixture has %d", len(env.Data), len(rows))
	}
	// Compare as a set keyed by the row identity: the handler sorts, the file does
	// not, and pairing by index would test the sort rather than the projection.
	byName := map[string]dto.UserView{}
	for _, r := range env.Data {
		byName[r.UserName] = r
	}
	for _, want := range rows {
		got, ok := byName[want.UserName]
		if !ok {
			t.Errorf("userName %q missing from the response", want.UserName)
			continue
		}
		if d := canonical(t, got); d != canonical(t, want) {
			t.Errorf("row %q differs from the fixture:\n%s", want.UserName, d)
		}
	}
}

// TestUsersDefaultSourceIsAnEmptyPage covers the state the route ships in today: no
// store assigned, so the empty source answers. The three meta fields have to agree with
// each other (dto.Meta's own invariant) — `data: []`, total 0, truncated false —
// because an empty `data` beside a non-zero total is the bug D18 exists to prevent.
func TestUsersDefaultSourceIsAnEmptyPage(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	s.readiness.SetListening(true)

	rec := do(t, s, http.MethodGet, UsersPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", UsersPath, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("body = %s, want `data` to be [] (not null) — R8.4's envelope always emits an array", body)
	}
	var env UsersEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}
	if env.Meta.Total != 0 || env.Meta.Truncated {
		t.Errorf("meta = %+v, want total 0 and truncated false for an empty store", env.Meta)
	}
	if strings.Contains(body, "nextCursor") {
		t.Errorf("body carries a nextCursor for an empty page: %s", body)
	}
}

// TestUsersPassesTheParsedQueryToTheSource is the reason the stub records its
// argument. A handler that parsed the query string, validated it, and then asked the
// source for *everything* would pass every other test in this file. Only the argument
// proves the parse is wired to the read — and for this route it is the only thing that
// proves the store receives the scope at all, since UserView carries no namespace or
// cluster of its own.
func TestUsersPassesTheParsedQueryToTheSource(t *testing.T) {
	st := &stubUsers{rows: loadUserFixture(t)}
	s := newUsersServer(t, st)

	const when = "2026-11-20T09:12:00Z"
	env := getUsers(t, s, UsersPath+
		"?namespace=open-webui&cluster=open-webui-pg&db=openwebui"+
		"&sort=-avgExecTimeMs&limit=1&from="+when+"&to="+when)

	want := ListQuery{
		Namespace: "open-webui",
		Cluster:   "open-webui-pg",
		DB:        "openwebui",
		Sort:      "avgExecTimeMs",
		Desc:      true,
		Limit:     1,
		From:      time.Date(2026, 11, 20, 9, 12, 0, 0, time.UTC),
		To:        time.Date(2026, 11, 20, 9, 12, 0, 0, time.UTC),
	}
	if st.called != 1 {
		t.Fatalf("source called %d times, want 1", st.called)
	}
	// cursor is unexported and empty here (no cursor in the request), so a struct
	// comparison covers every field including it.
	if st.last != want {
		t.Errorf("source got %+v\n           want %+v", st.last, want)
	}
	// The limit was honoured by the pager, not just parsed: one row back, five still
	// matching. This is also the assertion that the source did not honour `limit`
	// itself — a source that returned one row would report total 1 (R8.10's D18 rule,
	// spelled out in UsersSource's doc).
	if len(env.Data) != 1 {
		t.Errorf("data has %d rows, want 1 (limit=1)", len(env.Data))
	}
	if env.Meta.Total != len(st.rows) {
		t.Errorf("meta.total = %d, want %d — the limit cuts the page, not the count",
			env.Meta.Total, len(st.rows))
	}
}

func TestUsersNotReady(t *testing.T) {
	// R8.7's rule applied to this route: 503 NotReady, not a 200 whose empty `data`
	// looks like a fleet that has run nothing. The second half is the ordering — the
	// source must not be read at all.
	s := New(Config{Listen: "127.0.0.1:0", Users: &stubUsers{rows: loadUserFixture(t)}})
	rec := do(t, s, http.MethodGet, UsersPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s before start = %d, want 503", UsersPath, rec.Code)
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("error code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
	if s.cfg.Users.(*stubUsers).called != 0 {
		t.Errorf("source was read %d times while not ready, want 0", s.cfg.Users.(*stubUsers).called)
	}
}

func TestUsersSourceErrorIsUnavailable(t *testing.T) {
	// The store's failure mode. R8.5's Unavailable, and the message carries the
	// underlying error so an operator sees *why* — the clusters handler's rule.
	boom := errors.New("sqlite: no such table: dimension_rollup")
	st := &stubUsers{err: boom}
	rec := do(t, newUsersServer(t, st), http.MethodGet, UsersPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s = %d, want 503\nbody: %s", UsersPath, rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != dto.ErrUnavailable {
		t.Errorf("error code = %q, want %q", body.Error.Code, dto.ErrUnavailable)
	}
	if !strings.Contains(body.Error.Message, boom.Error()) {
		t.Errorf("message %q does not carry the source error %q", body.Error.Message, boom)
	}
}

// ---------------------------------------------------------------------------
// parameter validation
// ---------------------------------------------------------------------------

// TestUsersQueryValidation is R8.4's parameter set for this route. The messages are
// the ones openapi.yaml documents, so the documented and the emitted bodies agree, and
// the supported-sort list in the message is generated from the route's real vocabulary
// — which makes this the test that fails if a sort key is added to
// usersListQuerySpec without being added to the document.
func TestUsersQueryValidation(t *testing.T) {
	const sorts = "userName, loadByTotalTime, pctLoadByQueries, avgExecTimeMs, " +
		"totalExecTimeMs, timesCalled, avgRowsReturned, loadByCPUMs, loadByIOWaitMs, lockWaitMs"

	tests := []struct {
		target string
		want   string
	}{
		{UsersPath + "?limit=0", "invalid limit: 0 is below the minimum of 1"},
		{UsersPath + "?limit=5000", "invalid limit: 5000 exceeds the maximum of 500"},
		{UsersPath + "?limit=abc", `invalid limit: "abc" is not an integer`},
		{UsersPath + "?sort=bogus", `invalid sort: "bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{UsersPath + "?sort=-bogus", `invalid sort: "-bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		// `user` is the key the *queries* route uses (queries.go:70's SortUser); this
		// route's spelling is `userName`, and rejecting `user` is the assertion that
		// the two vocabularies were not quietly merged into one.
		{UsersPath + "?sort=user", `invalid sort: "user" (supported: ` + sorts + `; prefix "-" for descending)`},
		{UsersPath + "?from=yesterday", `invalid from: "yesterday" is not RFC 3339`},
		{UsersPath + "?to=2026-13-40", `invalid to: "2026-13-40" is not RFC 3339`},
		{UsersPath + "?cursor=not-a-cursor", `invalid cursor: "not-a-cursor" is not a pgsi cursor`},
	}
	st := &stubUsers{rows: loadUserFixture(t)}
	s := newUsersServer(t, st)
	for _, tt := range tests {
		rec := do(t, s, http.MethodGet, tt.target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400\nbody: %s", tt.target, rec.Code, rec.Body.String())
			continue
		}
		var body dto.ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("GET %s: decode: %v", tt.target, err)
			continue
		}
		if body.Error.Code != dto.ErrInvalidRequest || body.Error.Message != tt.want {
			t.Errorf("GET %s\n got %+v\nwant InvalidRequest %q", tt.target, body.Error, tt.want)
		}
	}
	// The ordering guarantee: a malformed request on a *ready* server must not reach
	// the source either. Answering 400 requires the parse to run first.
	if st.called != 0 {
		t.Errorf("source was read %d times for nine malformed requests, want 0", st.called)
	}
}

// TestUsersQueryAccepts is the other half: the values that must not be rejected. Every
// declared sort key parses, the route takes all eight shared parameters with none
// inlined as required, and the default sort is the documented one.
func TestUsersQueryAccepts(t *testing.T) {
	q, err := parseUsersQuery(url.Values{})
	if err != nil {
		t.Fatalf("empty query string: %v", err)
	}
	// R3.7 defines a rollup as answering "which X accounts for the most load", so the
	// default is the load column, descending — the direction is why DefaultDesc exists
	// rather than a hardcoded false in the parser.
	if q.Sort != SortLoadByTotalTime || !q.Desc {
		t.Errorf("default sort = %q desc=%v, want %q desc=true (R3.7's ranking)",
			q.Sort, q.Desc, SortLoadByTotalTime)
	}
	if q.Limit != DefaultLimit {
		t.Errorf("default limit = %d, want %d (R8.4's shared default)", q.Limit, DefaultLimit)
	}

	// Every declared key parses, and every one of them has a case in userSortKey. The
	// second half is the reason the const set is exhaustive rather than curated: a key
	// the parser accepts but the switch omits silently sorts on the identity alone,
	// and no compiler can see it. A row whose key for key X equals its identity is only
	// correct when X *is* the identity — which here is SortUserName itself
	// (users.go:97), the one key excluded below.
	rows := loadUserFixture(t)
	row := rows[0]
	for _, key := range usersListQuerySpec.Sorts {
		got, err := parseUsersQuery(url.Values{"sort": {key}})
		if err != nil {
			t.Errorf("sort=%q rejected: %v", key, err)
			continue
		}
		if got.Sort != key || got.Desc {
			t.Errorf("sort=%q parsed as %+v, want Sort=%q Desc=false", key, got, key)
		}
		if k := userSortKey(row, key); key != SortUserName &&
			k == userIdentity(row)+"|"+userIdentity(row) {
			t.Errorf("userSortKey(_, %q) = %q, the no-case rendering — the switch has no case "+
				"for it, so the route would page by name while claiming to sort by it", key, k)
		}
	}
}

// ---------------------------------------------------------------------------
// paging and sorting
// ---------------------------------------------------------------------------

// TestUsersPagination walks every cursor and requires every row exactly once. The
// expectation is written out rather than computed from sortUsers: a test that derives
// its expectation by calling the function under test can only fail on a crash.
func TestUsersPagination(t *testing.T) {
	rows := loadUserFixture(t)
	st := &stubUsers{rows: rows}
	s := newUsersServer(t, st)

	// Order is loadByTotalTime descending (the route's default): 46.5, 20.5, 14.2,
	// 12.1, 6.7 — five distinct values, so the order is fully determined and no page
	// boundary depends on the tiebreaker.
	want := []string{"app_rw", "openwebui", "gateway", "frank", "cookbook"}

	// Every page carries limit=2, including the first: a walk that requests the first
	// page without a limit gets all five rows and then a single page that happens to
	// look like a correct walk.
	target := UsersPath + "?limit=2"
	var got []string
	pages := 0
	for {
		env := getUsers(t, s, target)
		pages++
		if pages > 10 {
			t.Fatalf("walked %d pages without terminating; cursor loop is not converging", pages)
		}
		if env.Meta.Total != len(rows) {
			t.Fatalf("page %d: meta.total = %d, want %d (the count is pre-cut)", pages, env.Meta.Total, len(rows))
		}
		wantSize := 2
		if remaining := len(rows) - (pages-1)*2; remaining < wantSize {
			wantSize = remaining
		}
		if len(env.Data) != wantSize {
			t.Fatalf("page %d: %d rows, want %d", pages, len(env.Data), wantSize)
		}
		for _, r := range env.Data {
			got = append(got, r.UserName)
		}
		if env.Meta.NextCursor == "" {
			if env.Meta.Truncated {
				t.Errorf("page %d: truncated=true but nextCursor is empty", pages)
			}
			break
		}
		if !env.Meta.Truncated {
			t.Errorf("page %d: nextCursor set but truncated=false", pages)
		}
		target = UsersPath + "?limit=2&cursor=" + url.QueryEscape(env.Meta.NextCursor)
	}

	if pages != 3 {
		t.Errorf("walked %d pages, want 3 (5 rows at limit=2)", pages)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("walked order != the documented order\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestUsersSortKeys pins the row that sorts first, ascending and descending, for every
// key the route declares — all ten, which is what makes it a check on the switch's
// coverage rather than a spot check. The values come from the fixture and the
// expectations are written out, so a key that reads the wrong field — the classic
// `sort=avgExecTimeMs` sorting on `totalExecTimeMs` — fails here.
func TestUsersSortKeys(t *testing.T) {
	tests := []struct {
		key      string
		wantAsc  string // userName of the first row ascending
		wantDesc string // …descending
	}{
		// The dimension key itself: primary and identity are the same string, so the
		// key is `app_rw|app_rw` and the order is the plain alphabetical one.
		{SortUserName, "app_rw", "openwebui"}, // app_rw, cookbook, frank, gateway, openwebui
		// R3.2's pair. loadByTotalTime and pctLoadByQueries are the same number on
		// every fixture row (dto/dimensions_test.go's TestDimensionFixtureLoadArithmetic
		// pins that), so these two rows agree by construction.
		{SortLoadByTotalTime, "cookbook", "app_rw"},  // 6.7 … 46.5
		{SortPctLoadByQueries, "cookbook", "app_rw"}, // same values
		// No column in this fixture has a repeated value, so every row below is
		// decided by its primary alone; the tiebreaker is covered by the determinism
		// check in the loop, not by a tie row here.
		{SortAvgExecTimeMs, "gateway", "frank"},        // 8.1, 9.7, 12.5, 43.9, 323.2
		{SortTotalExecTimeMs, "gateway", "app_rw"},     // 112110.5 … 429326.4
		{SortTimesCalled, "frank", "app_rw"},           // 944, 4311, 13914, 20096, 44120
		{SortAvgRowsReturned, "openwebui", "cookbook"}, // 1, 1.4, 8.9, 14, 18.4
		{SortLoadByCPUMs, "frank", "app_rw"},           // 16420.7 … 388120.6
		{SortLoadByIOWaitMs, "gateway", "frank"},       // 7625, 14622.1, 36404.4, 41205.8, 288700.1
		{SortLockWaitMs, "gateway", "frank"},           // 198.2, 2810.7, 3120.4, 3921.6, 4410.9
	}
	for _, tt := range tests {
		for _, desc := range []bool{false, true} {
			rows := loadUserFixture(t)
			sortUsers(rows, tt.key, desc)
			want := tt.wantAsc
			if desc {
				want = tt.wantDesc
			}
			if len(rows) == 0 {
				t.Fatalf("sortUsers(%q) returned no rows", tt.key)
			}
			if rows[0].UserName != want {
				t.Errorf("sort=%q desc=%v: first row is %q, want %q", tt.key, desc, rows[0].UserName, want)
			}
			// The tiebreaker rule: sorting twice must produce an identical order,
			// which is what stops two rows with equal values swapping between pages
			// (sort.Slice is not stable without a total order).
			again := loadUserFixture(t)
			sortUsers(again, tt.key, desc)
			if canonical(t, again) != canonical(t, rows) {
				t.Errorf("sort=%q desc=%v is not deterministic across two calls", tt.key, desc)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// document pairing (R14.7)
// ---------------------------------------------------------------------------

// TestUsersRouteIsDocumented is the R14.7 pairing: the path must be under `paths` with
// x-status implemented and $ref the envelope the handler serves, and it must be gone
// from x-planned-paths. A route in code and absent from the doc is a defect, and so is
// one in both lists — the two lists are disjoint by construction.
func TestUsersRouteIsDocumented(t *testing.T) {
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + UsersPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			UsersPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", UsersPath)
		}
		// The 200 body must be the envelope the handler serves. UsersEnvelope is an
		// alias over dto.Envelope[dto.UserView], so the name the doc refs and the name
		// Go declares cannot drift silently — the R16.8 argument, applied to the
		// response type.
		if !strings.Contains(item, "$ref: '#/components/schemas/UsersEnvelope'") {
			t.Errorf("%s's responses do not $ref UsersEnvelope", UsersPath)
		}
	}
	// The manifest lists routes that are *not* in code. The entry was deleted rather
	// than marked when the route landed (the rule the earlier promotions state), so the
	// check is over the `- ` lines, which is what scripts/check-openapi.py treats as the
	// list's contents — a prose note naming the route is expected and carries no route.
	for _, line := range strings.Split(planned, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		fields := strings.Fields(line[2:])
		if len(fields) > 0 && fields[0] == UsersPath {
			t.Errorf("%s is still listed in x-planned-paths: it is in code now, "+
				"and `paths` and x-planned-paths must be disjoint (R14.7)", UsersPath)
		}
	}
}
