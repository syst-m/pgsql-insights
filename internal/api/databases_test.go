// Tests for R3.7's by-database rollup — GET /api/v1/insights/databases.
//
// The file is built around the same two risks tags_test.go names for the rollup it
// already had, because they are the two risks every paged list route shares:
//
//   - the *paging*. R8.4's contract is that `meta.total` counts the rows that match
//     *before* the page is cut, that a page shorter than `limit` carries no cursor,
//     and that walking every cursor yields every row exactly once. That is
//     TestDatabasesPagination.
//   - the *doc pairing*. R14.7 forbids a route in code that the document does not
//     list, and a documented route that 404s. TestDatabasesRouteIsDocumented reads the
//     embedded document for the path under `paths` (with x-status) and requires it
//     gone from `x-planned-paths`, so the handler and the doc cannot be moved apart
//     after this commit.
//
// What this file deliberately does NOT contain is a fixture-canonicality test.
// dto/dimensions_test.go:21's TestDatabasesFixtureIsCanonical already byte-compares
// testdata/api/databases.json against the DTO's own marshalling, and a second copy
// here would be R16.8's failure inside the test suite. The one fixture-derived
// assertion kept here is the one only this package can make: that the rows the
// *handler* emits are byte-identical to the fixture's rows
// (TestDatabasesRowJSONMatchesFixture) — the handler's projection, not the struct's.
//
// The third thing pinned here mirrors tags_test.go's TestTagsSortKeys for this
// route's own vocabulary: TestDatabasesSortKeys covers all ten keys in
// databasesListQuerySpec, which is what makes it a check on databaseSortKey's switch
// coverage rather than a spot check. A key the parser accepts but the switch omits
// does not fail to compile — it sorts on the identity alone, so the route would page
// by database name while reporting `sort=loadByCPUMs`. One row of that table is a tie
// case (freeidp and orders share avgExecTimeMs 4.8), which is where the identity
// tiebreaker is actually load-bearing. Unlike tags_test.go, the tables here name rows
// by their literal `database` strings rather than through a `hash()` helper: the
// identity of a DatabaseView *is* its name (databases.go:115), so there is no 64-hex
// value to shorten, and naming it directly is the shorter form of the same citation.
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

// databasesFixturePath is R16.8's named location for this route's contract
// (repo-root testdata/api/databases.json), reached from this package's directory. The
// same file is the SPA's Vitest fixture and the subject of
// dto.TestDatabasesFixtureIsCanonical; this is the third consumer.
const databasesFixturePath = "../../testdata/api/databases.json"

// loadDatabaseFixture returns the frozen database rows.
func loadDatabaseFixture(t *testing.T) []dto.DatabaseView {
	t.Helper()
	raw, err := os.ReadFile(databasesFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", databasesFixturePath, err)
	}
	var env DatabasesEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse %s: %v", databasesFixturePath, err)
	}
	if len(env.Data) == 0 {
		t.Fatalf("%s has no rows", databasesFixturePath)
	}
	return env.Data
}

// stubDatabases is a DatabasesSource that returns a fixed answer and records the query
// it was asked, so the tests can assert the handler passed the *parsed* query through
// rather than the raw query string (the stubTags/stubQueries pattern).
type stubDatabases struct {
	rows []dto.DatabaseView
	err  error

	called int
	last   ListQuery
}

func (st *stubDatabases) Databases(_ context.Context, q ListQuery) ([]dto.DatabaseView, error) {
	st.called++
	st.last = q
	// The stub hands back a copy: pageRows sorts the slice it is given in place, and
	// a source that let the caller sort its own backing array would corrupt the next
	// request's order.
	rows := make([]dto.DatabaseView, len(st.rows))
	copy(rows, st.rows)
	return rows, st.err
}

// newDatabasesServer builds a ready Server whose only subsystem is R3.7's
// by-database source. A process with no Fleet is ready once it is listening
// (R8.7(ii)), which is what makes the 200 paths reachable without a discovery stub.
func newDatabasesServer(t *testing.T, src DatabasesSource) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", Databases: src})
	s.readiness.SetListening(true)
	return s
}

// getDatabases performs the request and decodes the 200 body, failing the test on
// either step, so a body that stops being valid JSON fails once with the body quoted.
func getDatabases(t *testing.T, s *Server, target string) DatabasesEnvelope {
	t.Helper()
	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var env DatabasesEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: decode body: %v\nbody: %s", target, err, rec.Body.String())
	}
	return env
}

// ---------------------------------------------------------------------------
// route
// ---------------------------------------------------------------------------

func TestDatabasesRouteRegistered(t *testing.T) {
	// The registration itself. server_test.go asserts the same URL answers
	// something; this asserts it is *this* handler, by checking the envelope type only
	// this route serves (a DatabaseView row has a `database` and no `userName`, so it
	// cannot be a Users or Tags response decoded under the wrong name).
	rows := loadDatabaseFixture(t)
	env := getDatabases(t, newDatabasesServer(t, &stubDatabases{rows: rows}), DatabasesPath)

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

// TestDatabasesRowJSONMatchesFixture is the cross-check only this package can make:
// the rows the handler emits are byte-identical to the fixture's rows. dto's test
// proves the struct marshals to the fixture; this proves the *route* serves them — a
// handler that built its own response map, dropped one of R3.2's shared columns, or
// reordered the embedded LoadColumns fails here and nowhere else.
func TestDatabasesRowJSONMatchesFixture(t *testing.T) {
	rows := loadDatabaseFixture(t)
	env := getDatabases(t, newDatabasesServer(t, &stubDatabases{rows: rows}), DatabasesPath)

	if len(env.Data) != len(rows) {
		t.Fatalf("served %d rows, fixture has %d", len(env.Data), len(rows))
	}
	// Compare as a set keyed by the row identity: the handler sorts, the file does
	// not, and pairing by index would test the sort rather than the projection.
	byName := map[string]dto.DatabaseView{}
	for _, r := range env.Data {
		byName[r.Database] = r
	}
	for _, want := range rows {
		got, ok := byName[want.Database]
		if !ok {
			t.Errorf("database %q missing from the response", want.Database)
			continue
		}
		if d := canonical(t, got); d != canonical(t, want) {
			t.Errorf("row %q differs from the fixture:\n%s", want.Database, d)
		}
	}
}

// TestDatabasesDefaultSourceIsAnEmptyPage covers the state the route ships in today:
// no store assigned, so the empty source answers. The three meta fields have to agree
// with each other (dto.Meta's own invariant) — `data: []`, total 0, truncated false —
// because an empty `data` beside a non-zero total is the bug D18 exists to prevent.
func TestDatabasesDefaultSourceIsAnEmptyPage(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	s.readiness.SetListening(true)

	rec := do(t, s, http.MethodGet, DatabasesPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", DatabasesPath, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("body = %s, want `data` to be [] (not null) — R8.4's envelope always emits an array", body)
	}
	var env DatabasesEnvelope
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

// TestDatabasesPassesTheParsedQueryToTheSource is the reason the stub records its
// argument. A handler that parsed the query string, validated it, and then asked the
// source for *everything* would pass every other test in this file. Only the argument
// proves the parse is wired to the read — and for this route it is the only thing that
// proves the store receives the scope at all, since DatabaseView carries no namespace
// or cluster of its own (databases.go's identity section).
func TestDatabasesPassesTheParsedQueryToTheSource(t *testing.T) {
	st := &stubDatabases{rows: loadDatabaseFixture(t)}
	s := newDatabasesServer(t, st)

	const when = "2026-11-20T09:12:00Z"
	env := getDatabases(t, s, DatabasesPath+
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
	// The limit was honoured by the pager, not just parsed: one row back, six still
	// matching. This is also the assertion that the source did not honour `limit`
	// itself — a source that returned one row would report total 1 (R8.10's D18 rule,
	// spelled out in DatabasesSource's doc).
	if len(env.Data) != 1 {
		t.Errorf("data has %d rows, want 1 (limit=1)", len(env.Data))
	}
	if env.Meta.Total != len(st.rows) {
		t.Errorf("meta.total = %d, want %d — the limit cuts the page, not the count",
			env.Meta.Total, len(st.rows))
	}
}

func TestDatabasesNotReady(t *testing.T) {
	// R8.7's rule applied to this route: 503 NotReady, not a 200 whose empty `data`
	// looks like a fleet that has run nothing. The second half is the ordering — the
	// source must not be read at all.
	s := New(Config{Listen: "127.0.0.1:0", Databases: &stubDatabases{rows: loadDatabaseFixture(t)}})
	rec := do(t, s, http.MethodGet, DatabasesPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s before start = %d, want 503", DatabasesPath, rec.Code)
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("error code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
	if s.cfg.Databases.(*stubDatabases).called != 0 {
		t.Errorf("source was read %d times while not ready, want 0", s.cfg.Databases.(*stubDatabases).called)
	}
}

func TestDatabasesSourceErrorIsUnavailable(t *testing.T) {
	// The store's failure mode. R8.5's Unavailable, and the message carries the
	// underlying error so an operator sees *why* — the clusters handler's rule.
	boom := errors.New("sqlite: no such table: dimension_rollup")
	st := &stubDatabases{err: boom}
	rec := do(t, newDatabasesServer(t, st), http.MethodGet, DatabasesPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s = %d, want 503\nbody: %s", DatabasesPath, rec.Code, rec.Body.String())
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

// TestDatabasesQueryValidation is R8.4's parameter set for this route. The messages
// are the ones openapi.yaml documents, so the documented and the emitted bodies agree,
// and the supported-sort list in the message is generated from the route's real
// vocabulary — which makes this the test that fails if a sort key is added to
// databasesListQuerySpec without being added to the document.
func TestDatabasesQueryValidation(t *testing.T) {
	const sorts = "database, loadByTotalTime, pctLoadByQueries, avgExecTimeMs, " +
		"totalExecTimeMs, timesCalled, avgRowsReturned, loadByCPUMs, loadByIOWaitMs, lockWaitMs"

	tests := []struct {
		target string
		want   string
	}{
		{DatabasesPath + "?limit=0", "invalid limit: 0 is below the minimum of 1"},
		{DatabasesPath + "?limit=5000", "invalid limit: 5000 exceeds the maximum of 500"},
		{DatabasesPath + "?limit=abc", `invalid limit: "abc" is not an integer`},
		{DatabasesPath + "?sort=bogus", `invalid sort: "bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{DatabasesPath + "?sort=-bogus", `invalid sort: "-bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{DatabasesPath + "?from=yesterday", `invalid from: "yesterday" is not RFC 3339`},
		{DatabasesPath + "?to=2026-13-40", `invalid to: "2026-13-40" is not RFC 3339`},
		{DatabasesPath + "?cursor=not-a-cursor", `invalid cursor: "not-a-cursor" is not a pgsi cursor`},
	}
	st := &stubDatabases{rows: loadDatabaseFixture(t)}
	s := newDatabasesServer(t, st)
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
		t.Errorf("source was read %d times for eight malformed requests, want 0", st.called)
	}
}

// TestDatabasesQueryAccepts is the other half: the values that must not be rejected.
// Every declared sort key parses, the route takes all eight shared parameters with
// none inlined as required, and the default sort is the documented one.
func TestDatabasesQueryAccepts(t *testing.T) {
	q, err := parseDatabasesQuery(url.Values{})
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

	// Every declared key parses, and every one of them has a case in
	// databaseSortKey. The second half is the reason the const set is exhaustive
	// rather than curated: a key the parser accepts but the switch omits silently
	// sorts on the identity alone, and no compiler can see it. A row whose key for
	// key X equals its identity is only correct when X *is* the identity — which here
	// is SortDatabase itself (databases.go:115), the one key excluded below.
	rows := loadDatabaseFixture(t)
	row := rows[0]
	for _, key := range databasesListQuerySpec.Sorts {
		got, err := parseDatabasesQuery(url.Values{"sort": {key}})
		if err != nil {
			t.Errorf("sort=%q rejected: %v", key, err)
			continue
		}
		if got.Sort != key || got.Desc {
			t.Errorf("sort=%q parsed as %+v, want Sort=%q Desc=false", key, got, key)
		}
		if k := databaseSortKey(row, key); key != SortDatabase &&
			k == databaseIdentity(row)+"|"+databaseIdentity(row) {
			t.Errorf("databaseSortKey(_, %q) = %q, the no-case rendering — the switch has no case "+
				"for it, so the route would page by name while claiming to sort by it", key, k)
		}
	}
}

// ---------------------------------------------------------------------------
// paging and sorting
// ---------------------------------------------------------------------------

// TestDatabasesPagination walks every cursor and requires every row exactly once. The
// expectation is written out rather than computed from sortDatabases: a test that
// derives its expectation by calling the function under test can only fail on a crash.
func TestDatabasesPagination(t *testing.T) {
	rows := loadDatabaseFixture(t)
	st := &stubDatabases{rows: rows}
	s := newDatabasesServer(t, st)

	// Order is loadByTotalTime descending (the route's default): 41.2, 27.9, 12.8,
	// 6.9, 6.3, 4.9 — six distinct values, so the order is fully determined and no
	// page boundary depends on the tiebreaker.
	want := []string{"openwebui", "gateway", "freeidp", "orders", "billing", "cookbook"}

	// Every page carries limit=2, including the first: a walk that requests the first
	// page without a limit gets all six rows and then a single page that happens to
	// look like a correct walk.
	target := DatabasesPath + "?limit=2"
	var got []string
	pages := 0
	for {
		env := getDatabases(t, s, target)
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
			got = append(got, r.Database)
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
		target = DatabasesPath + "?limit=2&cursor=" + url.QueryEscape(env.Meta.NextCursor)
	}

	if pages != 3 {
		t.Errorf("walked %d pages, want 3 (6 rows at limit=2)", pages)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("walked order != the documented order\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestDatabasesSortKeys pins the row that sorts first, ascending and descending, for
// every key the route declares — all ten, which is what makes it a check on the
// switch's coverage rather than a spot check. The values come from the fixture and the
// expectations are written out, so a key that reads the wrong field — the classic
// `sort=avgExecTimeMs` sorting on `totalExecTimeMs` — fails here.
func TestDatabasesSortKeys(t *testing.T) {
	tests := []struct {
		key      string
		wantAsc  string // database of the first row ascending
		wantDesc string // …descending
	}{
		// The dimension key itself: primary and identity are the same string, so the
		// key is `billing|billing` and the order is the plain alphabetical one.
		{SortDatabase, "billing", "orders"}, // billing, cookbook, freeidp, gateway, openwebui, orders
		// R3.2's pair. loadByTotalTime and pctLoadByQueries are the same number on
		// every fixture row (dto/dimensions_test.go's TestDimensionFixtureLoadArithmetic
		// pins that), so these two rows agree by construction.
		{SortLoadByTotalTime, "cookbook", "openwebui"},  // 4.9 … 41.2
		{SortPctLoadByQueries, "cookbook", "openwebui"}, // same values
		// The one tie in the fixture: freeidp and orders both report 4.8.
		// sortableFloat renders both as the same 20-character string, so the order
		// falls to the identity — freeidp < orders — which is the tiebreaker doing
		// the job the pager exists for.
		{SortAvgExecTimeMs, "freeidp", "cookbook"},    // 4.8, 4.8, 8.1, 12.5, 43.9, 323.2
		{SortTotalExecTimeMs, "orders", "cookbook"},   // 47411.5 … 305120.8
		{SortTimesCalled, "cookbook", "billing"},      // 944, 4311, 9880, 13914, 19652, 20096
		{SortAvgRowsReturned, "openwebui", "billing"}, // 1, 1.4, 2.6, 3.1, 14, 18.4
		{SortLoadByCPUMs, "cookbook", "billing"},      // 16420.7 … 214800.1
		{SortLoadByIOWaitMs, "orders", "cookbook"},    // 3290.6 … 288700.1
		{SortLockWaitMs, "gateway", "cookbook"},       // 198.2 … 4410.9
	}
	for _, tt := range tests {
		for _, desc := range []bool{false, true} {
			rows := loadDatabaseFixture(t)
			sortDatabases(rows, tt.key, desc)
			want := tt.wantAsc
			if desc {
				want = tt.wantDesc
			}
			if len(rows) == 0 {
				t.Fatalf("sortDatabases(%q) returned no rows", tt.key)
			}
			if rows[0].Database != want {
				t.Errorf("sort=%q desc=%v: first row is %q, want %q", tt.key, desc, rows[0].Database, want)
			}
			// The tiebreaker rule: sorting twice must produce an identical order,
			// which is what stops two rows with equal values swapping between pages
			// (sort.Slice is not stable without a total order).
			again := loadDatabaseFixture(t)
			sortDatabases(again, tt.key, desc)
			if canonical(t, again) != canonical(t, rows) {
				t.Errorf("sort=%q desc=%v is not deterministic across two calls", tt.key, desc)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// document pairing (R14.7)
// ---------------------------------------------------------------------------

// TestDatabasesRouteIsDocumented is the R14.7 pairing: the path must be under `paths`
// with x-status implemented and $ref the envelope the handler serves, and it must be
// gone from x-planned-paths. A route in code and absent from the doc is a defect, and
// so is one in both lists — the two lists are disjoint by construction.
func TestDatabasesRouteIsDocumented(t *testing.T) {
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + DatabasesPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			DatabasesPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", DatabasesPath)
		}
		// The 200 body must be the envelope the handler serves. DatabasesEnvelope is
		// an alias over dto.Envelope[dto.DatabaseView], so the name the doc refs and
		// the name Go declares cannot drift silently — the R16.8 argument, applied to
		// the response type.
		if !strings.Contains(item, "$ref: '#/components/schemas/DatabasesEnvelope'") {
			t.Errorf("%s's responses do not $ref DatabasesEnvelope", DatabasesPath)
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
		if len(fields) > 0 && fields[0] == DatabasesPath {
			t.Errorf("%s is still listed in x-planned-paths: it is in code now, "+
				"and `paths` and x-planned-paths must be disjoint (R14.7)", DatabasesPath)
		}
	}
}
