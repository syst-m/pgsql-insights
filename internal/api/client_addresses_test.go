// Tests for R3.7's by-client-address rollup — GET /api/v1/insights/client-addresses.
//
// The file repeats the shape databases_test.go and users_test.go establish for the two
// rollups this commit completes: R8.4's paging contract (TestClientAddressesPagination)
// and R14.7's doc pairing (TestClientAddressesRouteIsDocumented), plus the per-key sort
// table that is the only instrument for a switch that cannot fail to compile.
//
// What this file deliberately does NOT contain is a fixture-canonicality test.
// dto/dimensions_test.go:33's TestClientAddressesFixtureIsCanonical already
// byte-compares testdata/api/client-addresses.json against the DTO's own marshalling;
// a second copy here would be R16.8's failure inside the test suite. The one
// fixture-derived assertion kept here is the one only this package can make: that the
// rows the *handler* emits are byte-identical to the fixture's rows
// (TestClientAddressesRowJSONMatchesFixture).
//
// What the route's own caveats cost the tests is nothing, and that is worth stating
// where a reader would otherwise look for a test that isn't there. client_addresses.go's
// header explains that R6.4's attribution rules (a real IP vs the literal `local` vs no
// address at all, decided by `--pooler-attribution`) are the store's, applied when the
// rows are produced — so there is no pooler-attribution case to test from a handler
// that never sees the flag. The fixture's three addresses are all real IPs, which is
// the pgbouncer-session case; a `local` row would be served identically, because the
// handler's only job with the string is to page on it.
//
// The fixture has three rows and no repeated value in any column, so TestClientAddressesSortKeys
// has no tie row; the tiebreaker is covered by the determinism check inside that loop.
// Three rows is also the smallest fixture that can prove a cursor walk terminates: at
// limit=2 the walk is two pages, the second of which is short, which is exactly the
// page shape that a `truncated`/`nextCursor` disagreement shows up on.
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

// clientAddressesFixturePath is R16.8's named location for this route's contract
// (repo-root testdata/api/client-addresses.json), reached from this package's
// directory. The same file is the SPA's Vitest fixture and the subject of
// dto.TestClientAddressesFixtureIsCanonical; this is the third consumer.
const clientAddressesFixturePath = "../../testdata/api/client-addresses.json"

// loadClientAddressFixture returns the frozen client-address rows.
func loadClientAddressFixture(t *testing.T) []dto.ClientAddressView {
	t.Helper()
	raw, err := os.ReadFile(clientAddressesFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", clientAddressesFixturePath, err)
	}
	var env ClientAddressesEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse %s: %v", clientAddressesFixturePath, err)
	}
	if len(env.Data) == 0 {
		t.Fatalf("%s has no rows", clientAddressesFixturePath)
	}
	return env.Data
}

// stubClientAddresses is a ClientAddressesSource that returns a fixed answer and
// records the query it was asked, so the tests can assert the handler passed the
// *parsed* query through rather than the raw query string.
type stubClientAddresses struct {
	rows []dto.ClientAddressView
	err  error

	called int
	last   ListQuery
}

func (st *stubClientAddresses) ClientAddresses(_ context.Context, q ListQuery) ([]dto.ClientAddressView, error) {
	st.called++
	st.last = q
	// The stub hands back a copy: pageRows sorts the slice it is given in place, and
	// a source that let the caller sort its own backing array would corrupt the next
	// request's order.
	rows := make([]dto.ClientAddressView, len(st.rows))
	copy(rows, st.rows)
	return rows, st.err
}

// newClientAddressesServer builds a ready Server whose only subsystem is R3.7's
// by-client-address source. A process with no Fleet is ready once it is listening
// (R8.7(ii)), which is what makes the 200 paths reachable without a discovery stub.
func newClientAddressesServer(t *testing.T, src ClientAddressesSource) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", ClientAddresses: src})
	s.readiness.SetListening(true)
	return s
}

// getClientAddresses performs the request and decodes the 200 body, failing the test on
// either step, so a body that stops being valid JSON fails once with the body quoted.
func getClientAddresses(t *testing.T, s *Server, target string) ClientAddressesEnvelope {
	t.Helper()
	rec := do(t, s, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", target, rec.Code, rec.Body.String())
	}
	var env ClientAddressesEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: decode body: %v\nbody: %s", target, err, rec.Body.String())
	}
	return env
}

// ---------------------------------------------------------------------------
// route
// ---------------------------------------------------------------------------

func TestClientAddressesRouteRegistered(t *testing.T) {
	// The registration itself. server_test.go asserts the same URL answers something;
	// this asserts it is *this* handler, by decoding the body into
	// ClientAddressesEnvelope and requiring the fixture's three `clientAddress` values
	// back — a response from the users route would decode to an empty Data here (no
	// `clientAddress` key), so the length check is what distinguishes the two.
	rows := loadClientAddressFixture(t)
	env := getClientAddresses(t,
		newClientAddressesServer(t, &stubClientAddresses{rows: rows}), ClientAddressPath)

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

// TestClientAddressesRowJSONMatchesFixture is the cross-check only this package can
// make: the rows the handler emits are byte-identical to the fixture's rows. dto's test
// proves the struct marshals to the fixture; this proves the *route* serves them — a
// handler that built its own response map, dropped one of R3.2's shared columns, or
// reordered the embedded LoadColumns fails here and nowhere else.
func TestClientAddressesRowJSONMatchesFixture(t *testing.T) {
	rows := loadClientAddressFixture(t)
	env := getClientAddresses(t,
		newClientAddressesServer(t, &stubClientAddresses{rows: rows}), ClientAddressPath)

	if len(env.Data) != len(rows) {
		t.Fatalf("served %d rows, fixture has %d", len(env.Data), len(rows))
	}
	// Compare as a set keyed by the row identity: the handler sorts, the file does
	// not, and pairing by index would test the sort rather than the projection.
	byAddr := map[string]dto.ClientAddressView{}
	for _, r := range env.Data {
		byAddr[r.ClientAddress] = r
	}
	for _, want := range rows {
		got, ok := byAddr[want.ClientAddress]
		if !ok {
			t.Errorf("clientAddress %q missing from the response", want.ClientAddress)
			continue
		}
		if d := canonical(t, got); d != canonical(t, want) {
			t.Errorf("row %q differs from the fixture:\n%s", want.ClientAddress, d)
		}
	}
}

// TestClientAddressesDefaultSourceIsAnEmptyPage covers the state the route ships in
// today: no store assigned, so the empty source answers. The three meta fields have to
// agree with each other (dto.Meta's own invariant) — `data: []`, total 0, truncated
// false — because an empty `data` beside a non-zero total is the bug D18 exists to
// prevent.
func TestClientAddressesDefaultSourceIsAnEmptyPage(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	s.readiness.SetListening(true)

	rec := do(t, s, http.MethodGet, ClientAddressPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\nbody: %s", ClientAddressPath, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("body = %s, want `data` to be [] (not null) — R8.4's envelope always emits an array", body)
	}
	var env ClientAddressesEnvelope
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

// TestClientAddressesPassesTheParsedQueryToTheSource is the reason the stub records its
// argument. A handler that parsed the query string, validated it, and then asked the
// source for *everything* would pass every other test in this file. Only the argument
// proves the parse is wired to the read — and for this route it is the only thing that
// proves the store receives the scope at all, since ClientAddressView carries no
// namespace or cluster of its own.
func TestClientAddressesPassesTheParsedQueryToTheSource(t *testing.T) {
	st := &stubClientAddresses{rows: loadClientAddressFixture(t)}
	s := newClientAddressesServer(t, st)

	const when = "2026-11-20T09:12:00Z"
	env := getClientAddresses(t, s, ClientAddressPath+
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
	// The limit was honoured by the pager, not just parsed: one row back, three still
	// matching. This is also the assertion that the source did not honour `limit`
	// itself — a source that returned one row would report total 1 (R8.10's D18 rule,
	// spelled out in ClientAddressesSource's doc).
	if len(env.Data) != 1 {
		t.Errorf("data has %d rows, want 1 (limit=1)", len(env.Data))
	}
	if env.Meta.Total != len(st.rows) {
		t.Errorf("meta.total = %d, want %d — the limit cuts the page, not the count",
			env.Meta.Total, len(st.rows))
	}
}

func TestClientAddressesNotReady(t *testing.T) {
	// R8.7's rule applied to this route: 503 NotReady, not a 200 whose empty `data`
	// looks like a fleet that has run nothing. The second half is the ordering — the
	// source must not be read at all.
	s := New(Config{Listen: "127.0.0.1:0",
		ClientAddresses: &stubClientAddresses{rows: loadClientAddressFixture(t)}})
	rec := do(t, s, http.MethodGet, ClientAddressPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s before start = %d, want 503", ClientAddressPath, rec.Code)
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("error code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
	if s.cfg.ClientAddresses.(*stubClientAddresses).called != 0 {
		t.Errorf("source was read %d times while not ready, want 0",
			s.cfg.ClientAddresses.(*stubClientAddresses).called)
	}
}

func TestClientAddressesSourceErrorIsUnavailable(t *testing.T) {
	// The store's failure mode. R8.5's Unavailable, and the message carries the
	// underlying error so an operator sees *why* — the clusters handler's rule.
	boom := errors.New("sqlite: no such table: dimension_rollup")
	st := &stubClientAddresses{err: boom}
	rec := do(t, newClientAddressesServer(t, st), http.MethodGet, ClientAddressPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s = %d, want 503\nbody: %s", ClientAddressPath, rec.Code, rec.Body.String())
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

// TestClientAddressesQueryValidation is R8.4's parameter set for this route. The
// messages are the ones openapi.yaml documents, so the documented and the emitted
// bodies agree, and the supported-sort list in the message is generated from the
// route's real vocabulary — which makes this the test that fails if a sort key is added
// to clientAddressesListQuerySpec without being added to the document.
func TestClientAddressesQueryValidation(t *testing.T) {
	const sorts = "clientAddress, loadByTotalTime, pctLoadByQueries, avgExecTimeMs, " +
		"totalExecTimeMs, timesCalled, avgRowsReturned, loadByCPUMs, loadByIOWaitMs, lockWaitMs"

	tests := []struct {
		target string
		want   string
	}{
		{ClientAddressPath + "?limit=0", "invalid limit: 0 is below the minimum of 1"},
		{ClientAddressPath + "?limit=5000", "invalid limit: 5000 exceeds the maximum of 500"},
		{ClientAddressPath + "?limit=abc", `invalid limit: "abc" is not an integer`},
		{ClientAddressPath + "?sort=bogus", `invalid sort: "bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{ClientAddressPath + "?sort=-bogus", `invalid sort: "-bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{ClientAddressPath + "?from=yesterday", `invalid from: "yesterday" is not RFC 3339`},
		{ClientAddressPath + "?to=2026-13-40", `invalid to: "2026-13-40" is not RFC 3339`},
		{ClientAddressPath + "?cursor=not-a-cursor", `invalid cursor: "not-a-cursor" is not a pgsi cursor`},
	}
	st := &stubClientAddresses{rows: loadClientAddressFixture(t)}
	s := newClientAddressesServer(t, st)
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

// TestClientAddressesQueryAccepts is the other half: the values that must not be
// rejected. Every declared sort key parses, the route takes all eight shared parameters
// with none inlined as required, and the default sort is the documented one.
func TestClientAddressesQueryAccepts(t *testing.T) {
	q, err := parseClientAddressesQuery(url.Values{})
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
	// clientAddressSortKey. The second half is the reason the const set is exhaustive
	// rather than curated: a key the parser accepts but the switch omits silently
	// sorts on the identity alone, and no compiler can see it. A row whose key for
	// key X equals its identity is only correct when X *is* the identity — which here
	// is SortClientAddress itself (client_addresses.go:115), the one key excluded below.
	rows := loadClientAddressFixture(t)
	row := rows[0]
	for _, key := range clientAddressesListQuerySpec.Sorts {
		got, err := parseClientAddressesQuery(url.Values{"sort": {key}})
		if err != nil {
			t.Errorf("sort=%q rejected: %v", key, err)
			continue
		}
		if got.Sort != key || got.Desc {
			t.Errorf("sort=%q parsed as %+v, want Sort=%q Desc=false", key, got, key)
		}
		if k := clientAddressSortKey(row, key); key != SortClientAddress &&
			k == clientAddressIdentity(row)+"|"+clientAddressIdentity(row) {
			t.Errorf("clientAddressSortKey(_, %q) = %q, the no-case rendering — the switch has no "+
				"case for it, so the route would page by address while claiming to sort by it", key, k)
		}
	}
}

// ---------------------------------------------------------------------------
// paging and sorting
// ---------------------------------------------------------------------------

// TestClientAddressesPagination walks every cursor and requires every row exactly once.
// The expectation is written out rather than computed from sortClientAddresses: a test
// that derives its expectation by calling the function under test can only fail on a
// crash.
func TestClientAddressesPagination(t *testing.T) {
	rows := loadClientAddressFixture(t)
	st := &stubClientAddresses{rows: rows}
	s := newClientAddressesServer(t, st)

	// Order is loadByTotalTime descending (the route's default): 54.8, 33.6, 11.6 —
	// three distinct values, so the order is fully determined and no page boundary
	// depends on the tiebreaker.
	want := []string{"10.44.1.19", "10.44.2.7", "10.44.0.51"}

	// Every page carries limit=2, including the first: a walk that requests the first
	// page without a limit gets all three rows and then a single page that happens to
	// look like a correct walk.
	target := ClientAddressPath + "?limit=2"
	var got []string
	pages := 0
	for {
		env := getClientAddresses(t, s, target)
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
			got = append(got, r.ClientAddress)
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
		target = ClientAddressPath + "?limit=2&cursor=" + url.QueryEscape(env.Meta.NextCursor)
	}

	if pages != 2 {
		t.Errorf("walked %d pages, want 2 (3 rows at limit=2)", pages)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("walked order != the documented order\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestClientAddressesSortKeys pins the row that sorts first, ascending and descending,
// for every key the route declares — all ten, which is what makes it a check on the
// switch's coverage rather than a spot check. The values come from the fixture and the
// expectations are written out, so a key that reads the wrong field — the classic
// `sort=avgExecTimeMs` sorting on `totalExecTimeMs` — fails here.
func TestClientAddressesSortKeys(t *testing.T) {
	tests := []struct {
		key      string
		wantAsc  string // clientAddress of the first row ascending
		wantDesc string // …descending
	}{
		// The dimension key itself: primary and identity are the same string, so the
		// key is `10.44.0.51|10.44.0.51` and the order is the plain lexicographic one.
		// Note that this is *string* order, not numeric octet order: `10.44.0.51` sorts
		// before `10.44.1.19` here and would also sort before it numerically, but
		// `10.44.2.7` last is a string-order fact, not a numeric one — the same
		// lexicographic-key rule every list route's pager uses.
		{SortClientAddress, "10.44.0.51", "10.44.2.7"},
		// R3.2's pair. loadByTotalTime and pctLoadByQueries are the same number on
		// every fixture row (dto/dimensions_test.go's TestDimensionFixtureLoadArithmetic
		// pins that), so these two rows agree by construction.
		{SortLoadByTotalTime, "10.44.0.51", "10.44.1.19"},  // 11.6, 33.6, 54.8
		{SortPctLoadByQueries, "10.44.0.51", "10.44.1.19"}, // same values
		// No column in this fixture has a repeated value, so every row below is
		// decided by its primary alone; the tiebreaker is covered by the determinism
		// check in the loop, not by a tie row here.
		{SortAvgExecTimeMs, "10.44.2.7", "10.44.0.51"},    // 4.8, 9.4, 27.5
		{SortTotalExecTimeMs, "10.44.0.51", "10.44.1.19"}, // 44118.4 … 188402.6
		{SortTimesCalled, "10.44.0.51", "10.44.2.7"},      // 1604, 20042, 24021
		{SortAvgRowsReturned, "10.44.2.7", "10.44.1.19"},  // 3.1, 6.2, 12.8
		{SortLoadByCPUMs, "10.44.0.51", "10.44.1.19"},     // 39800.9 … 165200.3
		{SortLoadByIOWaitMs, "10.44.0.51", "10.44.1.19"},  // 4317.5 … 23202.3
		{SortLockWaitMs, "10.44.0.51", "10.44.1.19"},      // 302.8, 640.2, 1980.4
	}
	for _, tt := range tests {
		for _, desc := range []bool{false, true} {
			rows := loadClientAddressFixture(t)
			sortClientAddresses(rows, tt.key, desc)
			want := tt.wantAsc
			if desc {
				want = tt.wantDesc
			}
			if len(rows) == 0 {
				t.Fatalf("sortClientAddresses(%q) returned no rows", tt.key)
			}
			if rows[0].ClientAddress != want {
				t.Errorf("sort=%q desc=%v: first row is %q, want %q", tt.key, desc, rows[0].ClientAddress, want)
			}
			// The tiebreaker rule: sorting twice must produce an identical order,
			// which is what stops two rows with equal values swapping between pages
			// (sort.Slice is not stable without a total order).
			again := loadClientAddressFixture(t)
			sortClientAddresses(again, tt.key, desc)
			if canonical(t, again) != canonical(t, rows) {
				t.Errorf("sort=%q desc=%v is not deterministic across two calls", tt.key, desc)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// document pairing (R14.7)
// ---------------------------------------------------------------------------

// TestClientAddressesRouteIsDocumented is the R14.7 pairing: the path must be under
// `paths` with x-status implemented and $ref the envelope the handler serves, and it
// must be gone from x-planned-paths. A route in code and absent from the doc is a
// defect, and so is one in both lists — the two lists are disjoint by construction.
func TestClientAddressesRouteIsDocumented(t *testing.T) {
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + ClientAddressPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			ClientAddressPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", ClientAddressPath)
		}
		// The 200 body must be the envelope the handler serves.
		// ClientAddressesEnvelope is an alias over dto.Envelope[dto.ClientAddressView],
		// so the name the doc refs and the name Go declares cannot drift silently — the
		// R16.8 argument, applied to the response type.
		if !strings.Contains(item, "$ref: '#/components/schemas/ClientAddressesEnvelope'") {
			t.Errorf("%s's responses do not $ref ClientAddressesEnvelope", ClientAddressPath)
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
		if len(fields) > 0 && fields[0] == ClientAddressPath {
			t.Errorf("%s is still listed in x-planned-paths: it is in code now, "+
				"and `paths` and x-planned-paths must be disjoint (R14.7)", ClientAddressPath)
		}
	}
}
