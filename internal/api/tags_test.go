// Tests for R3.12's tags — GET /api/v1/insights/tags.
//
// The file is built around the same two risks queries_test.go names for its own
// route, because they are the two risks every paged list route has:
//
//   - the *paging*. R8.4's contract is that `meta.total` counts the rows that match
//     *before* the page is cut, that a page shorter than `limit` carries no cursor,
//     and that walking every cursor yields every row exactly once. That is
//     TestTagsPagination.
//   - the *doc pairing*. R14.7 forbids a route in code that the document does not
//     list, and a documented route that 404s. TestTagsRouteIsDocumented reads the
//     embedded document for the path under `paths` (with x-status) and requires it
//     gone from `x-planned-paths`, so the handler and the doc cannot be moved apart
//     after this commit.
//
// What this file deliberately does NOT contain is a fixture-canonicality test.
// dto/queries_test.go:107's TestTagsFixtureIsCanonical already byte-compares
// testdata/api/tags.json against the DTO's own marshalling, and a second copy here
// would be R16.8's failure inside the test suite. The one fixture-derived assertion
// kept here is the one only this package can make: that the rows the *handler* emits
// are byte-identical to the fixture's rows (TestTagsRowJSONMatchesFixture) — the
// handler's projection rather than the struct's.
//
// The third thing pinned here has no analogue in queries_test.go, because only this
// route's field set creates the risk: TestTagsSortKeys covers all eighteen keys and
// thereby proves every key in tagsListQuerySpec has a case in tagSortKey. A key the
// parser accepts but the switch omits does not fail to compile — it silently sorts on
// the identity alone, so the only instrument that catches it is a table with one row
// per key. Four of those rows are the tie cases (controller, framework, dbDriver and
// avgRowsReturned each have two rows sharing a value), which is where the identity
// tiebreaker is actually load-bearing.
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

// tagsFixturePath is R16.8's named location for this route's contract
// (repo-root testdata/api/tags.json), reached from this package's directory. The
// same file is the SPA's Vitest fixture (web/src/api/fixtures.ts:115) and the
// subject of dto.TestTagsFixtureIsCanonical; this is the third consumer.
const tagsFixturePath = "../../testdata/api/tags.json"

// loadTagFixture returns the frozen tag rows.
func loadTagFixture(t *testing.T) []dto.TagView {
	t.Helper()
	raw, err := os.ReadFile(tagsFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", tagsFixturePath, err)
	}
	var env TagsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse %s: %v", tagsFixturePath, err)
	}
	if len(env.Data) == 0 {
		t.Fatalf("%s has no rows", tagsFixturePath)
	}
	return env.Data
}

// hash resolves an 8-character tagHash prefix to the full hash in the fixture — the
// tagIdentity counterpart of queries_test.go's fp, for the same two reasons: pasting
// 64-hex strings into every table makes it unreadable, and reading the value from the
// fixture rather than restating it means the table cannot drift from the data it sorts.
func hash(t *testing.T, prefix string) string {
	t.Helper()
	var got []string
	for _, r := range loadTagFixture(t) {
		if strings.HasPrefix(r.TagHash, prefix) {
			got = append(got, r.TagHash)
		}
	}
	if len(got) != 1 {
		t.Fatalf("tagHash prefix %q matches %d fixture rows, want 1", prefix, len(got))
	}
	return got[0]
}

// stubTags is a TagsSource that returns a fixed answer and records the query it was
// asked, so the tests can assert the handler passed the *parsed* query through rather
// than the raw query string (the stubQueries pattern).
type stubTags struct {
	rows []dto.TagView
	err  error

	called int
	last   ListQuery
}

func (st *stubTags) Tags(_ context.Context, q ListQuery) ([]dto.TagView, error) {
	st.called++
	st.last = q
	// The stub hands back a copy: pageRows sorts the slice it is given in place, and
	// a source that let the caller sort its own backing array would corrupt the next
	// request's order.
	rows := make([]dto.TagView, len(st.rows))
	copy(rows, st.rows)
	return rows, st.err
}

// newTagsServer builds a ready Server whose only subsystem is R3.12's source. A
// process with no Fleet is ready once it is listening (R8.7(ii)), which is what makes
// the 200 paths reachable without a discovery stub — the reason newQueriesServer exists.
func newTagsServer(t *testing.T, src TagsSource) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", Tags: src})
	s.readiness.SetListening(true)
	return s
}

// getTags performs the request and decodes the 200 body, failing the test on either
// step, so a body that stops being valid JSON fails once with the body quoted.
func getTags(t *testing.T, s *Server, target string) TagsEnvelope {
	t.Helper()
	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var env TagsEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: decode body: %v\nbody: %s", target, err, rec.Body.String())
	}
	return env
}

// ---------------------------------------------------------------------------
// route
// ---------------------------------------------------------------------------

func TestTagsRouteRegistered(t *testing.T) {
	// The registration itself. server_test.go asserts the same URL answers
	// something; this asserts it is *this* handler, by checking the envelope type only
	// this route serves (a TagView row has a `tagHash`; a QueryView row does not).
	rows := loadTagFixture(t)
	env := getTags(t, newTagsServer(t, &stubTags{rows: rows}), TagsPath)

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

// TestTagsRowJSONMatchesFixture is the cross-check only this package can make: the
// rows the handler emits are byte-identical to the fixture's rows. dto's test proves
// the struct marshals to the fixture; this proves the *route* serves them — a handler
// that built its own response map, dropped one of the (b) five, or re-ordered the
// embedded LoadColumns fails here and nowhere else.
func TestTagsRowJSONMatchesFixture(t *testing.T) {
	rows := loadTagFixture(t)
	env := getTags(t, newTagsServer(t, &stubTags{rows: rows}), TagsPath)

	if len(env.Data) != len(rows) {
		t.Fatalf("served %d rows, fixture has %d", len(env.Data), len(rows))
	}
	// Compare as a set keyed by tagHash: the handler sorts, the file does not, and
	// pairing by index would test the sort rather than the projection.
	byHash := map[string]dto.TagView{}
	for _, r := range env.Data {
		byHash[r.TagHash] = r
	}
	for _, want := range rows {
		got, ok := byHash[want.TagHash]
		if !ok {
			t.Errorf("tagHash %s… missing from the response", want.TagHash[:8])
			continue
		}
		if d := canonical(t, got); d != canonical(t, want) {
			t.Errorf("row %s… differs from the fixture:\n%s", want.TagHash[:8], d)
		}
	}
}

// TestTagsDefaultSourceIsAnEmptyPage covers the state the route ships in today: no
// store assigned (cmd/pgsi assigns none — see tags.go's "Why an interface" section),
// so the empty source answers. The three meta fields have to agree with each other
// (dto.Meta's own invariant) — `data: []`, total 0, truncated false — because an empty
// `data` beside a non-zero total is the bug D18 exists to prevent.
func TestTagsDefaultSourceIsAnEmptyPage(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	s.readiness.SetListening(true)

	rec := do(t, s, http.MethodGet, TagsPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", TagsPath, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("body = %s, want `data` to be [] (not null) — R8.4's envelope always emits an array", body)
	}
	var env TagsEnvelope
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

// TestTagsPassesTheParsedQueryToTheSource is the reason the stub records its argument.
// A handler that parsed the query string, validated it, and then asked the source for
// *everything* would pass every other test in this file. Only the argument proves the
// parse is wired to the read — and for this route it is the only thing that proves the
// store receives the scope at all, since TagView itself carries no namespace or cluster.
func TestTagsPassesTheParsedQueryToTheSource(t *testing.T) {
	st := &stubTags{rows: loadTagFixture(t)}
	s := newTagsServer(t, st)

	const when = "2026-11-20T09:12:00Z"
	env := getTags(t, s, TagsPath+
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
	// The limit was honoured by the pager, not just parsed: one row back, two still
	// matching. This is also the assertion that the source did not honour `limit`
	// itself — a source that returned one row would report total 1 (R8.10's D18 rule,
	// spelled out in TagsSource's doc).
	if len(env.Data) != 1 {
		t.Errorf("data has %d rows, want 1 (limit=1)", len(env.Data))
	}
	if env.Meta.Total != len(st.rows) {
		t.Errorf("meta.total = %d, want %d — the limit cuts the page, not the count",
			env.Meta.Total, len(st.rows))
	}
}

func TestTagsNotReady(t *testing.T) {
	// R8.7's rule applied to this route: 503 NotReady, not a 200 whose empty `data`
	// looks like a fleet that has run nothing. The second half is the ordering — the
	// source must not be read at all.
	s := New(Config{Listen: "127.0.0.1:0", Tags: &stubTags{rows: loadTagFixture(t)}})
	rec := do(t, s, http.MethodGet, TagsPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s before start = %d, want 503", TagsPath, rec.Code)
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("error code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
	if s.cfg.Tags.(*stubTags).called != 0 {
		t.Errorf("source was read %d times while not ready, want 0", s.cfg.Tags.(*stubTags).called)
	}
}

func TestTagsSourceErrorIsUnavailable(t *testing.T) {
	// The store's failure mode. R8.5's Unavailable, and the message carries the
	// underlying error so an operator sees *why* — the clusters handler's rule.
	boom := errors.New("sqlite: no such table: dimension_rollup")
	st := &stubTags{err: boom}
	rec := do(t, newTagsServer(t, st), http.MethodGet, TagsPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s = %d, want 503\nbody: %s", TagsPath, rec.Code, rec.Body.String())
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

// TestTagsQueryValidation is R8.4's parameter set for this route. The messages are the
// ones openapi.yaml documents, so the documented and the emitted bodies agree, and the
// supported-sort list in the message is generated from the route's real vocabulary —
// which makes this the test that fails if a sort key is added to the const block
// without being added to the document.
func TestTagsQueryValidation(t *testing.T) {
	const sorts = "loadByTotalTime, pctLoadByQueries, avgExecTimeMs, totalExecTimeMs, " +
		"timesCalled, avgRowsReturned, loadByCPUMs, loadByIOWaitMs, lockWaitMs, tagHash, " +
		"application, database, user, action, route, controller, framework, dbDriver"

	tests := []struct {
		target string
		want   string
	}{
		{TagsPath + "?limit=0", "invalid limit: 0 is below the minimum of 1"},
		{TagsPath + "?limit=5000", "invalid limit: 5000 exceeds the maximum of 500"},
		{TagsPath + "?limit=abc", `invalid limit: "abc" is not an integer`},
		{TagsPath + "?sort=bogus", `invalid sort: "bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{TagsPath + "?sort=-bogus", `invalid sort: "-bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{TagsPath + "?from=yesterday", `invalid from: "yesterday" is not RFC 3339`},
		{TagsPath + "?to=2026-13-40", `invalid to: "2026-13-40" is not RFC 3339`},
		{TagsPath + "?cursor=not-a-cursor", `invalid cursor: "not-a-cursor" is not a pgsi cursor`},
	}
	st := &stubTags{rows: loadTagFixture(t)}
	s := newTagsServer(t, st)
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

// TestTagsQueryAccepts is the other half: the values that must not be rejected. Every
// declared sort key parses, the route takes all eight shared parameters with none
// inlined as required, and the default sort is the documented one.
func TestTagsQueryAccepts(t *testing.T) {
	q, err := parseTagsQuery(url.Values{})
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

	// Every declared key parses, and every one of them has a case in tagSortKey. The
	// second half is the reason the const set is exhaustive rather than curated: a key
	// the parser accepts but the switch omits silently sorts on the identity alone,
	// and no compiler can see it. A row whose key for key X equals its identity is only
	// correct when X *is* the identity.
	rows := loadTagFixture(t)
	row := rows[0]
	for _, key := range tagsListQuerySpec.Sorts {
		got, err := parseTagsQuery(url.Values{"sort": {key}})
		if err != nil {
			t.Errorf("sort=%q rejected: %v", key, err)
			continue
		}
		if got.Sort != key || got.Desc {
			t.Errorf("sort=%q parsed as %+v, want Sort=%q Desc=false", key, got, key)
		}
		if k := tagSortKey(row, key); key != SortTagHash && k == tagIdentity(row)+"|"+tagIdentity(row) {
			t.Errorf("tagSortKey(_, %q) = %q, the no-case rendering — the switch has no case "+
				"for it, so the route would page by hash while claiming to sort by it", key, k)
		}
	}
}

// ---------------------------------------------------------------------------
// paging and sorting
// ---------------------------------------------------------------------------

// TestTagsPagination walks every cursor and requires every row exactly once. The
// expectation is written out rather than computed from sortTags: a test that derives
// its expectation by calling the function under test can only fail on a crash.
func TestTagsPagination(t *testing.T) {
	rows := loadTagFixture(t)
	st := &stubTags{rows: rows}
	s := newTagsServer(t, st)

	// Order is loadByTotalTime descending (the route's default): 88.7, 88.3, 64.1.
	want := []string{
		hash(t, "37610975"), // 88.7  gateway
		hash(t, "76201a9f"), // 88.3  openwebui
		hash(t, "27826ba5"), // 64.1  cookbook
	}

	// Every page carries limit=2, including the first: a walk that requests the first
	// page without a limit gets all three rows and then a single page that happens to
	// look like a correct walk.
	target := TagsPath + "?limit=2"
	var got []string
	pages := 0
	for {
		env := getTags(t, s, target)
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
			got = append(got, r.TagHash)
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
		target = TagsPath + "?limit=2&cursor=" + url.QueryEscape(env.Meta.NextCursor)
	}

	if pages != 2 {
		t.Errorf("walked %d pages, want 2 (3 rows at limit=2)", pages)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("walked order != the documented order\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestTagsSortKeys pins the row that sorts first, ascending and descending, for every
// key the route declares — all eighteen, which is what makes it a check on the switch's
// coverage rather than a spot check. The values come from the fixture (via hash) and
// the expectations are written out, so a key that reads the wrong field — the classic
// `sort=avgExecTimeMs` sorting on `totalExecTimeMs` — fails here.
func TestTagsSortKeys(t *testing.T) {
	tests := []struct {
		key      string
		wantAsc  string // tagHash prefix of the first row ascending
		wantDesc string // …descending
	}{
		{SortTagHash, "27826ba5", "76201a9f"},
		// The (a) trio are all three distinct, so these three rows are the plain case.
		{SortApplication, "27826ba5", "76201a9f"}, // cookbook < gateway < openwebui
		{SortDatabase, "27826ba5", "76201a9f"},
		{SortUser, "27826ba5", "76201a9f"},
		// The (b) five are where a reader mispredicts, twice over. The gateway row has
		// no pgsi comment, so its action/route/controller/framework/dbDriver are empty
		// strings — and an empty primary does *not* sort first ascending. The key is
		// primary + "|" + identity, and '|' (ASCII 124) outranks every letter and
		// every '/', so an empty row sorts last ascending and first descending. The
		// same is true of queries.go's keys; it is the shared pager's format, not this
		// route's. Second surprise: the cookbook row has action and route but no
		// controller, so two rows are empty there and the hash breaks the tie
		// (27826ba5 < 37610975) — the tiebreaker doing its job, which is the one place
		// in this table where an empty value is ordered by anything other than itself.
		{SortAction, "27826ba5", "37610975"},     // recipe.list < session.load < ""
		{SortRoute, "27826ba5", "37610975"},      // /api/recipes < /api/v1/… < ""
		{SortController, "76201a9f", "37610975"}, // UsersController < "", the pair tied on hash
		{SortFramework, "76201a9f", "37610975"},  // fastapi < ""
		{SortDBDriver, "76201a9f", "37610975"},   // pgx < ""
		// The load columns. loadByTotalTime and pctLoadByQueries are the same number
		// on every fixture row (R3.2's pair), so their rows agree by construction.
		{SortLoadByTotalTime, "27826ba5", "37610975"},  // 64.1, 88.3, 88.7
		{SortPctLoadByQueries, "27826ba5", "37610975"}, // same values
		{SortAvgExecTimeMs, "37610975", "27826ba5"},    // 8.1, 43.9, 323.2
		{SortTotalExecTimeMs, "37610975", "27826ba5"},  // 112110.5, 189244.6, 305120.8
		{SortTimesCalled, "27826ba5", "37610975"},      // 944, 4311, 13914
		{SortAvgRowsReturned, "27826ba5", "37610975"},  // 1, 1, 14 — the 1s tie, broken by hash
		{SortLoadByCPUMs, "27826ba5", "76201a9f"},      // 16420.7, 104485.5, 174622.5
		{SortLoadByIOWaitMs, "37610975", "27826ba5"},   // 7625, 14622.1, 288700.1
		{SortLockWaitMs, "37610975", "27826ba5"},       // 198.2, 3120.4, 4410.9
	}
	for _, tt := range tests {
		for _, desc := range []bool{false, true} {
			rows := loadTagFixture(t)
			sortTags(rows, tt.key, desc)
			want := tt.wantAsc
			if desc {
				want = tt.wantDesc
			}
			if len(rows) == 0 {
				t.Fatalf("sortTags(%q) returned no rows", tt.key)
			}
			if !strings.HasPrefix(rows[0].TagHash, want) {
				t.Errorf("sort=%q desc=%v: first row is %s…, want %s…",
					tt.key, desc, rows[0].TagHash[:8], want)
			}
			// The tiebreaker rule: sorting twice must produce an identical order,
			// which is what stops two rows with equal values swapping between pages
			// (sort.Slice is not stable without a total order).
			again := loadTagFixture(t)
			sortTags(again, tt.key, desc)
			if canonical(t, again) != canonical(t, rows) {
				t.Errorf("sort=%q desc=%v is not deterministic across two calls", tt.key, desc)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// document pairing (R14.7)
// ---------------------------------------------------------------------------

// TestTagsRouteIsDocumented is the R14.7 pairing: the path must be under `paths` with
// x-status implemented and $ref the envelope the handler serves, and it must be gone
// from x-planned-paths. A route in code and absent from the doc is a defect, and so is
// one in both lists — the two lists are disjoint by construction.
func TestTagsRouteIsDocumented(t *testing.T) {
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + TagsPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			TagsPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", TagsPath)
		}
		// The 200 body must be the envelope the handler serves. TagsEnvelope is an
		// alias over dto.Envelope[dto.TagView], so the name the doc refs and the name
		// Go declares cannot drift silently — the R16.8 argument, applied to the
		// response type.
		if !strings.Contains(item, "$ref: '#/components/schemas/TagsEnvelope'") {
			t.Errorf("%s's responses do not $ref TagsEnvelope", TagsPath)
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
		if len(fields) > 0 && fields[0] == TagsPath {
			t.Errorf("%s is still listed in x-planned-paths: it is in code now, "+
				"and `paths` and x-planned-paths must be disjoint (R14.7)", TagsPath)
		}
	}
}
