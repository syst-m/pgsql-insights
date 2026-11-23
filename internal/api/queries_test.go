// Tests for R8.2's query list — GET /api/v1/insights/queries.
//
// The two risks in this route are the same two series_test.go names for its own
// route, and the file is built around them rather than around line coverage:
//
//   - the *paging*. R8.4's contract is that `meta.total` counts the rows that
//     match *before* the page is cut, that a page shorter than `limit` carries no
//     cursor, and that walking every cursor yields every row exactly once. That
//     is TestQueryPagination, and it is the test that would fail if this route
//     paged from a source that returned a page instead of a result set (the D18
//     argument in QueryListSource's doc).
//   - the *doc pairing*. R14.7 forbids a route in code that the document does
//     not list, and a documented route that 404s. TestQueriesRouteIsDocumented
//     reads the embedded document for the path under `paths` (with x-status) and
//     requires it gone from `x-planned-paths`, so the handler and the doc cannot
//     be moved apart after this commit.
//
// What this file deliberately does NOT contain is a fixture-canonicality test.
// dto/queries_test.go:89's TestQueriesFixtureIsCanonical already byte-compares
// testdata/api/queries.json against the DTO's own marshalling, and a second copy
// here would be R16.8's failure inside the test suite. The one fixture-derived
// assertion kept here is the one only this package can make: that the rows the
// *handler* emits are byte-identical to the fixture's rows (TestQueriesRowJSON
// MatchesFixture), which is the handler's projection rather than the struct's.
//
// The fixture's seven rows are not stored in default-sort order, so nothing here
// byte-compares a whole served envelope against the fixture file: the envelope's
// `data` is sorted by the handler and the file's is not. The comparison that
// matters — per-row field names and values — is exact.

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

// queriesFixturePath is R16.8's named location for this route's contract
// (repo-root testdata/api/queries.json), reached from this package's directory.
// The same file is the SPA's Vitest fixture and the subject of
// dto.TestQueriesFixtureIsCanonical; this is the third consumer.
const queriesFixturePath = "../../testdata/api/queries.json"

// loadQueryFixture returns the frozen query rows.
func loadQueryFixture(t *testing.T) []dto.QueryView {
	t.Helper()
	raw, err := os.ReadFile(queriesFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", queriesFixturePath, err)
	}
	var env dto.Envelope[dto.QueryView]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse %s: %v", queriesFixturePath, err)
	}
	if len(env.Data) == 0 {
		t.Fatalf("%s has no rows", queriesFixturePath)
	}
	return env.Data
}

// fp resolves an 8-character fingerprint prefix to the full fingerprint in the
// fixture. The alternative — pasting seven 64-hex strings into every table — is
// what makes a sort test unreadable, and reading the value from the fixture
// rather than restating it means the table cannot drift from the data it sorts.
func fp(t *testing.T, prefix string) string {
	t.Helper()
	var got []string
	for _, r := range loadQueryFixture(t) {
		if strings.HasPrefix(r.Fingerprint, prefix) {
			got = append(got, r.Fingerprint)
		}
	}
	if len(got) != 1 {
		t.Fatalf("fingerprint prefix %q matches %d fixture rows, want 1", prefix, len(got))
	}
	return got[0]
}

// stubQueries is a QueryListSource that returns a fixed answer and records the
// query it was asked, so the tests can assert the handler passed the *parsed*
// query through rather than the raw query string (the stubSeries pattern).
type stubQueries struct {
	rows []dto.QueryView
	err  error

	called int
	last   ListQuery
}

func (st *stubQueries) QueryList(_ context.Context, q ListQuery) ([]dto.QueryView, error) {
	st.called++
	st.last = q
	// The stub hands back a copy: pageRows sorts the slice it is given in place,
	// and a source that let the caller sort its own backing array would corrupt
	// the next request's order. Copying here is what makes a handler that mutates
	// the slice visible to TestQueryPagination running after a plain GET.
	rows := make([]dto.QueryView, len(st.rows))
	copy(rows, st.rows)
	return rows, st.err
}

// newQueriesServer builds a ready Server whose only subsystem is R8.2's source.
// A process with no Fleet is ready once it is listening (R8.7(ii)), which is
// what makes the 200 paths reachable without a discovery stub — the same reason
// newSeriesServer exists.
func newQueriesServer(t *testing.T, src QueryListSource) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", Queries: src})
	s.readiness.SetListening(true)
	return s
}

// getQueries performs the request and decodes the 200 body, failing the test on
// either step. Every assertion in this file goes through it so a body that stops
// being valid JSON fails once, with the body quoted, rather than in six places.
func getQueries(t *testing.T, s *Server, target string) QueriesEnvelope {
	t.Helper()
	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var env QueriesEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: decode body: %v\nbody: %s", target, err, rec.Body.String())
	}
	return env
}

// ---------------------------------------------------------------------------
// route
// ---------------------------------------------------------------------------

func TestQueriesRouteRegistered(t *testing.T) {
	// The registration itself. server_test.go asserts the same URL answers
	// something; this asserts it is *this* handler, by checking the envelope type
	// only this route serves (an Envelope[QueryView] has a `query` field in its
	// rows; an Envelope[ClusterView] does not).
	rows := loadQueryFixture(t)
	env := getQueries(t, newQueriesServer(t, &stubQueries{rows: rows}), QueriesPath)

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

// TestQueriesRowJSONMatchesFixture is the cross-check only this package can
// make: the rows the handler emits are byte-identical to the fixture's rows.
// dto's test proves the struct marshals to the fixture; this proves the *route*
// serves them — a handler that built its own response map, dropped a field or
// re-ordered the embedded LoadColumns fails here and nowhere else.
func TestQueriesRowJSONMatchesFixture(t *testing.T) {
	rows := loadQueryFixture(t)
	env := getQueries(t, newQueriesServer(t, &stubQueries{rows: rows}), QueriesPath)

	if len(env.Data) != len(rows) {
		t.Fatalf("served %d rows, fixture has %d", len(env.Data), len(rows))
	}
	// Compare as a set keyed by fingerprint: the handler sorts, the file does
	// not, and pairing by index would test the sort rather than the projection.
	byFP := map[string]dto.QueryView{}
	for _, r := range env.Data {
		byFP[r.Fingerprint] = r
	}
	for _, want := range rows {
		got, ok := byFP[want.Fingerprint]
		if !ok {
			t.Errorf("fingerprint %s… missing from the response", want.Fingerprint[:8])
			continue
		}
		if d := firstDiff(canonical(t, got), canonical(t, want)); d != "" {
			t.Errorf("row %s… differs from the fixture:\n%s", want.Fingerprint[:8], d)
		}
	}
}

// TestQueriesDefaultSourceIsAnEmptyPage covers the state the route ships in
// today: no store assigned, so the empty source answers. The three meta fields
// have to agree with each other (dto.Meta's own invariant) — `data: []`, total
// 0, truncated false — because an empty `data` beside a non-zero total is the
// bug this project's D18 ruling exists to prevent.
func TestQueriesDefaultSourceIsAnEmptyPage(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	s.readiness.SetListening(true)

	rec := do(t, s, http.MethodGet, QueriesPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", QueriesPath, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("body = %s, want `data` to be [] (not null) — R8.4's envelope always emits an array", body)
	}
	var env QueriesEnvelope
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

// TestQueriesPassesTheParsedQueryToTheSource is the reason the stub records its
// argument. A handler that parsed the query string, validated it, and then asked
// the source for *everything* would pass every other test in this file: the
// rows, the paging, the field names. Only the argument proves the parse is wired
// to the read.
func TestQueriesPassesTheParsedQueryToTheSource(t *testing.T) {
	st := &stubQueries{rows: loadQueryFixture(t)}
	s := newQueriesServer(t, st)

	const when = "2026-11-20T09:12:00Z"
	env := getQueries(t, s, QueriesPath+
		"?namespace=ai-api-gateway&cluster=rev-proxy-pg&db=gateway"+
		"&sort=-avgExecTimeMs&limit=1&from="+when+"&to="+when)

	want := ListQuery{
		Namespace: "ai-api-gateway",
		Cluster:   "rev-proxy-pg",
		DB:        "gateway",
		Sort:      "avgExecTimeMs",
		Desc:      true,
		Limit:     1,
		From:      time.Date(2026, 11, 20, 9, 12, 0, 0, time.UTC),
		To:        time.Date(2026, 11, 20, 9, 12, 0, 0, time.UTC),
	}
	if st.called != 1 {
		t.Fatalf("source called %d times, want 1", st.called)
	}
	// cursor is unexported and empty here (no cursor in the request), so a
	// struct comparison covers every field including it.
	if st.last != want {
		t.Errorf("source got %+v\n           want %+v", st.last, want)
	}
	// The limit was honoured by the pager, not just parsed: one row back, six
	// still matching.
	if len(env.Data) != 1 {
		t.Errorf("data has %d rows, want 1 (limit=1)", len(env.Data))
	}
	if env.Meta.Total != len(st.rows) {
		t.Errorf("meta.total = %d, want %d — the limit cuts the page, not the count",
			env.Meta.Total, len(st.rows))
	}
}

func TestQueriesNotReady(t *testing.T) {
	// R8.7's rule applied to this route: 503 NotReady, not a 200 whose empty
	// `data` looks like a cluster that has run no queries. The second half is the
	// ordering — the source must not be read at all, which is what stops a
	// not-ready process from issuing a query it cannot answer.
	s := New(Config{Listen: "127.0.0.1:0", Queries: &stubQueries{rows: loadQueryFixture(t)}})
	rec := do(t, s, http.MethodGet, QueriesPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s before start = %d, want 503", QueriesPath, rec.Code)
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("error code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
	if s.cfg.Queries.(*stubQueries).called != 0 {
		t.Errorf("source was read %d times while not ready, want 0", s.cfg.Queries.(*stubQueries).called)
	}
}

func TestQueriesSourceErrorIsUnavailable(t *testing.T) {
	// The store's failure mode. R8.5's Unavailable, and the message carries the
	// underlying error so an operator sees *why* — the clusters handler's rule.
	boom := errors.New("sqlite: no such table: queries")
	st := &stubQueries{err: boom}
	rec := do(t, newQueriesServer(t, st), http.MethodGet, QueriesPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s = %d, want 503\nbody: %s", QueriesPath, rec.Code, rec.Body.String())
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

// TestQueryListQueryValidation is R8.4's parameter set for this route, in the
// shape clusters_test.go's TestClusterQueryValidation set: a table of targets
// and the exact message each must produce. The messages are the ones
// openapi.yaml documents, so the documented and the emitted bodies agree, and
// the supported-sort list in the message is generated from the route's real
// vocabulary — which makes this the test that fails if a sort key is added to
// the const block without being added to the document.
func TestQueryListQueryValidation(t *testing.T) {
	const sorts = "fingerprint, query, database, namespace, cluster, loadByTotalTime, " +
		"avgExecTimeMs, totalExecTimeMs, timesCalled, pgMajor"

	tests := []struct {
		target string
		want   string
	}{
		{QueriesPath + "?limit=0", "invalid limit: 0 is below the minimum of 1"},
		{QueriesPath + "?limit=5000", "invalid limit: 5000 exceeds the maximum of 500"},
		{QueriesPath + "?limit=abc", `invalid limit: "abc" is not an integer`},
		{QueriesPath + "?sort=bogus", `invalid sort: "bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{QueriesPath + "?sort=-bogus", `invalid sort: "-bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{QueriesPath + "?from=yesterday", `invalid from: "yesterday" is not RFC 3339`},
		{QueriesPath + "?to=2026-13-40", `invalid to: "2026-13-40" is not RFC 3339`},
		{QueriesPath + "?cursor=not-a-cursor", `invalid cursor: "not-a-cursor" is not a pgsi cursor`},
	}
	st := &stubQueries{rows: loadQueryFixture(t)}
	s := newQueriesServer(t, st)
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
	// The ordering guarantee, asserted here rather than in TestQueriesNotReady
	// because it is a validation test: a malformed request on a *ready* server
	// must not reach the source either. Answering 400 requires the parse to run
	// first, and a handler that read the store before validating would leave the
	// stub's counter at 8.
	if st.called != 0 {
		t.Errorf("source was read %d times for eight malformed requests, want 0", st.called)
	}
}

// TestQueryListQueryAccepts is the other half: the values that must not be
// rejected. Every declared sort key parses, `db` is accepted on a route whose
// scoping it technically is, and the default sort is the documented one.
func TestQueryListQueryAccepts(t *testing.T) {
	q, err := parseQueryListQuery(url.Values{})
	if err != nil {
		t.Fatalf("empty query string: %v", err)
	}
	// R3.2 ranks the Queries table by total_exec_time, and clusters.go:36 named
	// this route's default before the route existed: "a cluster list sorts by
	// phase and pgMajor, a query list by total_exec_time".
	if q.Sort != SortTotalExecTimeMs || !q.Desc {
		t.Errorf("default sort = %q desc=%v, want %q desc=true (R3.2's ranking)",
			q.Sort, q.Desc, SortTotalExecTimeMs)
	}
	if q.Limit != DefaultLimit {
		t.Errorf("default limit = %d, want %d (R8.10)", q.Limit, DefaultLimit)
	}

	// Every key the route declares must parse in both directions — the const
	// block, the spec value and the document all list the same set, and this is
	// the assertion that ties the first two together.
	for _, key := range queryListQuerySpec.Sorts {
		for _, s := range []string{key, "-" + key} {
			if _, err := parseQueryListQuery(url.Values{"sort": {s}}); err != nil {
				t.Errorf("sort=%q: %v", s, err)
			}
		}
	}
	if _, err := parseQueryListQuery(url.Values{"db": {"gateway"}}); err != nil {
		t.Errorf("db=gateway: %v", err)
	}
}

// ---------------------------------------------------------------------------
// paging
// ---------------------------------------------------------------------------

// TestQueryPagination walks the fixture with limit=2 and requires that the four
// pages concatenate to the full set in sort order, that every non-final page
// carries a cursor and the final one does not, and that meta.total is the
// pre-cut count on every page. This is the test that fails if the route ever
// pages from a source that returns a page instead of a result set — the D18
// failure mode, where total reports the page size.
func TestQueryPagination(t *testing.T) {
	rows := loadQueryFixture(t)
	st := &stubQueries{rows: rows}
	s := newQueriesServer(t, st)

	// The full order, written out rather than computed from sortQueries: a test
	// that derives its expectation by calling the function under test can only
	// fail on a crash. Order is totalExecTimeMs descending (R3.2's ranking).
	want := []string{
		fp(t, "598d9680"), // 305120.8
		fp(t, "843923b9"), // 128410.5
		fp(t, "a615c7e5"), //  90233.1
		fp(t, "4e3b7f20"), //  76120.3
		fp(t, "16f40dfa"), //  44318.9
		fp(t, "0c8a03bf"), //  21877.4
		fp(t, "943e2f6c"), //  15204.6
	}

	// Every page of the walk carries limit=2, including the first: a walk that
	// requests the first page without a limit gets all seven rows and then a
	// single page that happens to look like a correct walk.
	target := QueriesPath + "?limit=2"
	var got []string
	pages := 0
	for {
		env := getQueries(t, s, target)
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
			t.Fatalf("page %d: %d rows, want %d\nbody rows: %d", pages, len(env.Data), wantSize, len(env.Data))
		}
		for _, r := range env.Data {
			got = append(got, r.Fingerprint)
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
		target = QueriesPath + "?limit=2&cursor=" + url.QueryEscape(env.Meta.NextCursor)
	}

	if pages != 4 {
		t.Errorf("walked %d pages, want 4 (7 rows at limit=2)", pages)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("walked order != the documented order\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestQuerySortKeys pins the row that sorts first, ascending and descending, for
// every key the route declares. The values come from the fixture (via fp) and
// the expectations are written out, so a key that reads the wrong field — the
// classic `sort=avgExecTimeMs` sorting on `totalExecTimeMs` — fails here.
func TestQuerySortKeys(t *testing.T) {
	tests := []struct {
		key      string
		wantAsc  string // fingerprint prefix of the first row ascending
		wantDesc string // …descending
	}{
		{SortFingerprint, "0c8a03bf", "a615c7e5"},
		// `query` sorts on the template text, and the two ends are the two rows
		// a reader mispredicts. Ascending is *not* "SELECT id FROM routes…": the
		// three "SELECT *…" rows come first because ' ' (0x20) sorts below every
		// letter, and among them "SELECT * FROM pg_catalog…" (a615c7e5) beats
		// "SELECT * FROM sessions…" because '*' (0x2a) < 's'. Descending is the
		// UPDATE row because 'U' (0x55) > 'S' (0x53) — case matters, and R3.6's
		// normaliser does not fold it.
		{SortQuery, "a615c7e5", "16f40dfa"},
		{SortDatabase, "598d9680", "943e2f6c"},
		{SortNamespace, "0c8a03bf", "943e2f6c"},
		{SortCluster, "598d9680", "a615c7e5"},
		{SortLoadByTotalTime, "943e2f6c", "a615c7e5"},
		{SortAvgExecTimeMs, "0c8a03bf", "598d9680"},
		{SortTotalExecTimeMs, "943e2f6c", "598d9680"},
		{SortTimesCalled, "943e2f6c", "a615c7e5"},
		// pgMajor's tiebreaker is the full identity, not the fingerprint: the
		// four PG 17 rows rank ai-api-gateway/… < cookbook/… < freeidp/…, so
		// descending starts at the freeidp row (4e3b7f20), not at the
		// lexically-largest fingerprint.
		{SortPGMajor, "16f40dfa", "4e3b7f20"},
	}
	for _, tt := range tests {
		for _, desc := range []bool{false, true} {
			rows := loadQueryFixture(t)
			sortQueries(rows, tt.key, desc)
			want := tt.wantAsc
			if desc {
				want = tt.wantDesc
			}
			if len(rows) == 0 {
				t.Fatalf("sortQueries(%q) returned no rows", tt.key)
			}
			if !strings.HasPrefix(rows[0].Fingerprint, want) {
				t.Errorf("sort=%q desc=%v: first row is %s…, want %s…",
					tt.key, desc, rows[0].Fingerprint[:8], want)
			}
			// The tiebreaker rule: sorting twice must produce an identical order,
			// which is what stops two rows with equal values swapping between
			// pages (sort.Slice is not stable without a total order).
			again := loadQueryFixture(t)
			sortQueries(again, tt.key, desc)
			if canonical(t, again) != canonical(t, rows) {
				t.Errorf("sort=%q desc=%v is not deterministic across two calls", tt.key, desc)
			}
		}
	}
}

// TestSortableFloatIsLexicographicallyNumeric is the unit check on the one
// helper whose entire job is a string comparison. `%v` on two floats of
// different digit counts sorts the shorter one last ("99999.1" > "100000.2"
// byte-wise), and a sort that is only sometimes numeric produces two pages that
// overlap or drop a row — R8.10's named failure. The test asserts both halves:
// the padded form compares right, the unpadded form compares wrong.
func TestSortableFloatIsLexicographicallyNumeric(t *testing.T) {
	a, b := sortableFloat(99999.1), sortableFloat(100000.2)
	if a >= b {
		t.Errorf("sortableFloat(99999.1)=%q >= sortableFloat(100000.2)=%q — the widths differ, so the comparison is not numeric", a, b)
	}
	if len(a) != len(b) {
		t.Errorf("widths differ: len(%q)=%d len(%q)=%d", a, len(a), b, len(b))
	}
	// The same pair un-padded, asserted so the contrast is in the test and not
	// only in the comment: byte-wise "99999.1" sorts *after* "100000.2", which is
	// exactly the bug the padding removes.
	if "99999.1" < "100000.2" {
		t.Errorf(`"99999.1" < "100000.2" as raw strings — if this ever becomes true the padding is not what makes the sort work`)
	}
	// Integral and sub-integer values keep the same width, so a whole set of
	// them sorts numerically as one string comparison chain.
	for _, v := range []float64{0, 4, 42.7, 1204, 128410.5, 999999999.999999} {
		if len(sortableFloat(v)) != 20 {
			t.Errorf("sortableFloat(%v) = %q, want 20 characters", v, sortableFloat(v))
		}
	}
}

// ---------------------------------------------------------------------------
// document pairing
// ---------------------------------------------------------------------------

// TestQueriesRouteIsDocumented is what keeps queries.go and api/openapi.yaml
// from being moved apart after this commit: the route must be under `paths` with
// an x-status, must $ref the envelope the handler actually returns, and must NOT
// still be under `x-planned-paths`. R14.7's two failure directions, both
// covered — the same test series_test.go and promql_test.go write for their own
// routes.
func TestQueriesRouteIsDocumented(t *testing.T) {
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + QueriesPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			QueriesPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", QueriesPath)
		}
		// The 200 body must be the envelope the handler serves. QueriesEnvelope
		// is an alias over dto.Envelope[dto.QueryView], so the name the doc refs
		// and the name Go declares cannot drift silently — the R16.8 argument,
		// applied to the response type.
		if !strings.Contains(item, "$ref: '#/components/schemas/QueriesEnvelope'") {
			t.Errorf("%s's responses do not $ref QueriesEnvelope", QueriesPath)
		}
	}
	// The manifest lists routes that are *not* in code. The entry was deleted
	// rather than marked when the route landed (the rule the r8.3-promql-pair
	// note states), so the check is over the `- ` lines, which is what
	// scripts/check-openapi.py treats as the list's contents — a prose note
	// naming the route is expected and carries no route.
	for _, line := range strings.Split(planned, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		fields := strings.Fields(line[2:])
		if len(fields) > 0 && fields[0] == QueriesPath {
			t.Errorf("%s is still listed in x-planned-paths: it is in code now, "+
				"and `paths` and x-planned-paths must be disjoint (R14.7)", QueriesPath)
		}
	}
}
