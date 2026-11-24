// Tests for R8.11 — the batched series endpoint,
// GET /api/v1/insights/queries/series?fingerprints=a,b,c&range=1h.
//
// Two things carry the risk in this route and the tests are built around them
// rather than around line coverage:
//
//   - the *shape*. R8.11's whole purpose is to answer 50 rows in one body, so
//     the field names of one entry and the meaning of its meta are the contract.
//     TestQuerySeriesJSONFieldNames byte-compares the envelope the way
//     promql_test.go's TestPromQLJSONFieldNames and clusters_test.go's
//     TestEmptyFleetRendersArrayNotNull byte-compare theirs — a renamed or
//     dropped tag has to fail something, and R16.8 exists because once it did
//     not.
//   - the *doc pairing*. R14.7 forbids a route in code that the document does
//     not list, and a documented route that 404s. TestQuerySeriesRouteIsDocumented
//     reads the embedded document and requires the path under `paths` (with
//     x-status) and not under `x-planned-paths`, so the handler and the doc
//     cannot be moved apart after this commit.
//
// The parsing rules — the fingerprint list's cap and de-duplication, the range
// vocabulary — are table tests on parseSeriesQuery itself, in the shape
// clusters.go set with TestClusterQueryValidation, because a handler that
// parses inline cannot be enumerated.

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

// stubSeries is a QuerySeriesSource that returns a fixed answer and records the
// query it was asked, so the tests can assert the handler passed the *parsed*
// query through rather than the raw query string.
type stubSeries struct {
	series []QuerySeries
	err    error

	called int
	last   SeriesQuery
}

func (st *stubSeries) QuerySeries(_ context.Context, q SeriesQuery) ([]QuerySeries, error) {
	st.called++
	st.last = q
	return st.series, st.err
}

// newSeriesServer builds a server whose only subsystem is R8.11's source. A
// process with no Fleet is ready once it is listening (R8.7(ii),
// TestServerWithoutFleetIsReadyWhenListening), which is what makes the 200
// paths reachable without dragging in a discovery stub.
func newSeriesServer(t *testing.T, src QuerySeriesSource) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", Series: src})
	s.readiness.SetListening(true)
	return s
}

const testFingerprint = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func testPoint(t *testing.T) QuerySeriesPoint {
	t.Helper()
	return QuerySeriesPoint{
		Timestamp:            time.Date(2026, 11, 21, 9, 12, 0, 0, time.UTC),
		ExecutionTimeSeconds: 0.4182,
		Rows:                 1200,
		SharedBlocksHit:      48200,
		SharedBlocksRead:     12,
		LockTimeSeconds:      0.0031,
		IOTimeSeconds:        0.0412,
	}
}

func testSeries(t *testing.T) QuerySeries {
	t.Helper()
	return QuerySeries{
		Fingerprint:  testFingerprint,
		QueryID:      "8123456789012345678",
		Namespace:    "postgres",
		Cluster:      "onepg",
		Pod:          "onepg-1",
		InstanceRole: "primary",
		Datname:      "appdb",
		Usename:      "app",
		Points:       []QuerySeriesPoint{testPoint(t)},
	}
}

// ---------------------------------------------------------------------------
// route
// ---------------------------------------------------------------------------

func TestQuerySeriesRouteRegistered(t *testing.T) {
	src := &stubSeries{series: []QuerySeries{testSeries(t)}}
	s := newSeriesServer(t, src)

	rec := do(t, s, http.MethodGet, "/api/v1/insights/queries/series?fingerprints="+testFingerprint)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200; body %s", QuerySeriesPath, rec.Code, rec.Body.String())
	}
	if src.called != 1 {
		t.Fatalf("source called %d times, want 1", src.called)
	}
	// The handler hands the source the parsed query, not the raw string.
	if len(src.last.Fingerprints) != 1 || src.last.Fingerprints[0] != testFingerprint {
		t.Errorf("source got fingerprints %v, want [%s]", src.last.Fingerprints, testFingerprint)
	}
	if src.last.Range != DefaultRange {
		t.Errorf("source got range %q, want %q (R10.15's default)", src.last.Range, DefaultRange)
	}
	if src.last.Start.IsZero() || src.last.End.IsZero() || !src.last.End.After(src.last.Start) {
		t.Errorf("source got window %v..%v, want a non-empty window ending after it starts",
			src.last.Start, src.last.End)
	}

	// Method-pattern routing rejects POST with 405, not 404 — the same assertion
	// server_test.go makes for /healthz and /api/v1/clusters.
	if rec := do(t, s, http.MethodPost, QuerySeriesPath); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST %s = %d, want 405", QuerySeriesPath, rec.Code)
	}
}

func TestQuerySeriesServesSourceAnswer(t *testing.T) {
	src := &stubSeries{series: []QuerySeries{testSeries(t)}}
	rec := do(t, newSeriesServer(t, src), http.MethodGet,
		fmt.Sprintf("%s?fingerprints=%s&namespace=postgres&cluster=onepg&range=6h",
			QuerySeriesPath, testFingerprint))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var env QuerySeriesEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %s: %v", rec.Body.String(), err)
	}
	if len(env.Data) != 1 || env.Data[0].Fingerprint != testFingerprint {
		t.Fatalf("data = %+v, want one entry for %s", env.Data, testFingerprint)
	}
	if env.Meta.Total != 1 || env.Meta.Truncated {
		t.Errorf("meta = %+v, want total 1 / truncated false", env.Meta)
	}
	if env.Meta.NextCursor != "" {
		t.Errorf("meta.nextCursor = %q, want empty: a batch has no next page", env.Meta.NextCursor)
	}
	if len(env.Data[0].Points) != 1 {
		t.Errorf("points = %+v, want the source's one point", env.Data[0].Points)
	}
}

// TestQuerySeriesJSONFieldNames is the R16.8 test for this route: every json
// tag in QuerySeries, QuerySeriesPoint and the envelope, pinned as bytes. It
// marshals the envelope directly (rather than through the handler) because
// meta.generatedAt is time.Now() on the wire and only a direct build has a
// clock the test can name.
func TestQuerySeriesJSONFieldNames(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 12, 3, 0, time.UTC)
	q := SeriesQuery{Fingerprints: []string{testFingerprint}}
	env := buildSeriesEnvelope(q, []QuerySeries{testSeries(t)}, now)

	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"data":[{"fingerprint":"` + testFingerprint +
		`","queryid":"8123456789012345678","namespace":"postgres","cluster":"onepg",` +
		`"pod":"onepg-1","instanceRole":"primary","datname":"appdb","usename":"app",` +
		`"points":[{"timestamp":"2026-11-21T09:12:00Z","executionTimeSeconds":0.4182,` +
		`"rows":1200,"sharedBlocksHit":48200,"sharedBlocksRead":12,"lockTimeSeconds":0.0031,` +
		`"ioTimeSeconds":0.0412}]}],` +
		`"meta":{"total":1,"truncated":false,"generatedAt":"2026-11-21T09:12:03Z"}}`
	if string(b) != want {
		t.Errorf("body =\n%s\nwant\n%s", b, want)
	}
}

// TestQuerySeriesEmptyPointsNeverNull: an absent array and an empty one mean
// the same thing to the UI, and the house rule FleetEnvelope.data states for
// its own array applies to points (the mistake that turned a missing key into a
// plausible empty page once already).
func TestQuerySeriesEmptyPointsNeverNull(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 12, 3, 0, time.UTC)
	bare := QuerySeries{Fingerprint: testFingerprint, Namespace: "postgres", Cluster: "onepg"}
	b, err := json.Marshal(buildSeriesEnvelope(SeriesQuery{Fingerprints: []string{testFingerprint}},
		[]QuerySeries{bare}, now))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"points":[]`) {
		t.Errorf("body = %s, want \"points\":[] for a fingerprint with no points", b)
	}
	if strings.Contains(string(b), `"points":null`) {
		t.Errorf("body = %s, points serialised as null", b)
	}
	// The optional labels are omitted when empty, not emitted as "".
	if strings.Contains(string(b), `"queryid":""`) || strings.Contains(string(b), `"pod":""`) {
		t.Errorf("body = %s, an empty optional label was emitted instead of omitted", b)
	}
}

// TestQuerySeriesBatchCounts pins meta.total and meta.truncated against
// R8.4's invariant (truncated ⟺ len(data) < total) with total read as the
// number of fingerprints *requested* — the batch analogue of R8.10's
// pre-cut count.
func TestQuerySeriesBatchCounts(t *testing.T) {
	multi := func(n int) []string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, fmt.Sprintf("fp-%02d", i))
		}
		return out
	}
	answered := func(n int) []QuerySeries {
		out := make([]QuerySeries, 0, n)
		for i := range n {
			out = append(out, QuerySeries{Fingerprint: fmt.Sprintf("fp-%02d", i),
				Namespace: "postgres", Cluster: "onepg"})
		}
		return out
	}
	now := time.Date(2026, 11, 21, 9, 12, 3, 0, time.UTC)

	for _, tc := range []struct {
		name      string
		requested int
		returned  int
		wantTotal int
		wantTrunc bool
	}{
		{"one asked one answered", 1, 1, 1, false},
		{"fifty asked fifty answered", 50, 50, 50, false},
		// The case R8.4's invariant exists for: a stale row in a 50-row page load
		// says so in meta instead of quietly reporting 40.
		{"fifty asked forty answered", 50, 40, 50, true},
		{"one asked none answered", 1, 0, 1, true},
		// A source that over-answers would otherwise make truncated true by
		// R8.4's formula, claiming a next page that cannot exist.
		{"fifty asked sixty answered", 50, 60, 60, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := buildSeriesEnvelope(SeriesQuery{Fingerprints: multi(tc.requested)},
				answered(tc.returned), now)
			if env.Meta.Total != tc.wantTotal || env.Meta.Truncated != tc.wantTrunc {
				t.Errorf("total=%d truncated=%v, want total=%d truncated=%v",
					env.Meta.Total, env.Meta.Truncated, tc.wantTotal, tc.wantTrunc)
			}
			if len(env.Data) != tc.returned {
				t.Errorf("len(data) = %d, want %d", len(env.Data), tc.returned)
			}
		})
	}
}

func TestQuerySeriesNotReady(t *testing.T) {
	// No SetListening: the process is up but not serving, so R8.7's single
	// readiness value gates this route with 503 rather than 200 and an empty
	// data array that looks like "no queries".
	src := &stubSeries{series: []QuerySeries{testSeries(t)}}
	s := New(Config{Listen: "127.0.0.1:0", Series: src})

	rec := do(t, s, http.MethodGet, QuerySeriesPath+"?fingerprints="+testFingerprint)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET = %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	// The message is openapi.yaml's documented NotReady example, verbatim — the
	// same string clusters_test.go pins for /api/v1/clusters.
	if body.Error.Code != dto.ErrNotReady ||
		body.Error.Message != "discovery has not completed its first sync" {
		t.Errorf("error body = %+v, want NotReady / %q", body.Error,
			"discovery has not completed its first sync")
	}
	if src.called != 0 {
		t.Errorf("source called %d times before readiness, want 0", src.called)
	}
}

func TestQuerySeriesSourceErrorIsUnavailable(t *testing.T) {
	src := &stubSeries{err: errors.New("store is closed")}
	rec := do(t, newSeriesServer(t, src), http.MethodGet,
		QuerySeriesPath+"?fingerprints="+testFingerprint)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET = %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != dto.ErrUnavailable {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrUnavailable)
	}
	if !strings.Contains(body.Error.Message, "store is closed") {
		t.Errorf("message = %q, want it to carry the source's error", body.Error.Message)
	}
}

// TestQuerySeriesDefaultSourceAnswersEveryFingerprint is the requirement's
// headline claim, tested against the default source: ONE request naming 50
// fingerprints, ONE response carrying 50 entries — the 50 detail calls R10.6's
// sparkline table would otherwise need on page load, which R10.14's 1.5 s
// budget forbids. It also pins the empty-source contract (every entry exists
// with an empty, non-nil Points), because the alternative — `data: []` beside
// `total: 50` — would claim a next page that cannot exist.
func TestQuerySeriesDefaultSourceAnswersEveryFingerprint(t *testing.T) {
	fps := make([]string, 0, MaxBatchFingerprints)
	for range MaxBatchFingerprints {
		fps = append(fps, fmt.Sprintf("fp-%02d", len(fps)))
	}
	// A nil Series is the default source (series.go's seriesSource applies it).
	rec := do(t, newSeriesServer(t, nil), http.MethodGet,
		QuerySeriesPath+"?fingerprints="+strings.Join(fps, ","))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200; body %.200s", rec.Code, rec.Body.String())
	}
	var env QuerySeriesEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(env.Data) != MaxBatchFingerprints {
		t.Fatalf("one response carried %d entries, want %d (R8.11 replaces one request per row)",
			len(env.Data), MaxBatchFingerprints)
	}
	for i, entry := range env.Data {
		if entry.Fingerprint != fps[i] {
			t.Fatalf("entry %d = %q, want %q (request order is preserved)", i, entry.Fingerprint, fps[i])
		}
		if entry.Points == nil {
			t.Errorf("entry %s has nil points, want []", entry.Fingerprint)
		}
	}
	if env.Meta.Total != MaxBatchFingerprints || env.Meta.Truncated {
		t.Errorf("meta = %+v, want total %d / truncated false", env.Meta, MaxBatchFingerprints)
	}
}

// ---------------------------------------------------------------------------
// parameter parsing
// ---------------------------------------------------------------------------

func TestParseFingerprints(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 12, 3, 0, time.UTC)
	fifty := make([]string, 0, MaxBatchFingerprints)
	for range MaxBatchFingerprints {
		fifty = append(fifty, fmt.Sprintf("fp-%02d", len(fifty)))
	}
	fiftyOne := append(append([]string{}, fifty...), "fp-50")

	for _, tc := range []struct {
		name    string
		raw     string
		want    string // joined with "," — empty means "expect an error"
		wantErr string
	}{
		{"missing", "", "", "required"},
		{"blank", "   ", "", "required"},
		{"only commas", ",,,", "", "no fingerprint"},
		{"one", "abc", "abc", ""},
		{"three", "a,b,c", "a,b,c", ""},
		// A stray space after a comma is normal in a hand-written URL and must not
		// become 50 empty fingerprints; a repeated fingerprint must not become two
		// entries (which would make meta.total lie).
		{"spaces and duplicates", " a , b , a ", "a,b", ""},
		{"empty member", "a,,b", "a,b", ""},
		{"at the cap", strings.Join(fifty, ","), strings.Join(fifty, ","), ""},
		// R8.10's above-max-is-an-error rule, applied to the batch parameter.
		{"one over the cap", strings.Join(fiftyOne, ","), "", "maximum is 50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := parseSeriesQuery(map[string][]string{"fingerprints": {tc.raw}}, now)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := strings.Join(q.Fingerprints, ","); got != tc.want {
				t.Errorf("fingerprints = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseSeriesQueryRanges(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 12, 3, 0, time.UTC)
	from := "2026-11-21T08:00:00Z"
	to := "2026-11-21T09:00:00Z"

	for _, tc := range []struct {
		name      string
		rng       string
		from, to  string
		wantRange string
		wantStart string // RFC3339 UTC, "" means "not asserted"
		wantEnd   string
		wantErr   string
	}{
		// R10.15's five presets, each resolving to now-duration..now.
		{"default", "", "", "", "1h", "2026-11-21T08:12:03Z", "2026-11-21T09:12:03Z", ""},
		{"1h", "1h", "", "", "1h", "2026-11-21T08:12:03Z", "2026-11-21T09:12:03Z", ""},
		{"6h", "6h", "", "", "6h", "2026-11-21T03:12:03Z", "2026-11-21T09:12:03Z", ""},
		{"1d", "1d", "", "", "1d", "2026-11-20T09:12:03Z", "2026-11-21T09:12:03Z", ""},
		{"7d", "7d", "", "", "7d", "2026-11-14T09:12:03Z", "2026-11-21T09:12:03Z", ""},
		{"30d", "30d", "", "", "30d", "2026-10-22T09:12:03Z", "2026-11-21T09:12:03Z", ""},
		// The vocabulary is lowercase; a client that sends 1H means the same
		// thing, and the tolerance costs nothing because an unknown spelling is
		// still an error rather than a silent default.
		{"uppercase", "1H", "", "", "1h", "2026-11-21T08:12:03Z", "2026-11-21T09:12:03Z", ""},
		// custom is how R8.4's from/to pair is reached on this route.
		{"custom", "custom", from, to, "custom", from, to, ""},
		{"custom missing to", "custom", from, "", "", "", "", "requires from and to"},
		{"custom missing from", "custom", "", to, "", "", "", "requires from and to"},
		{"custom bad from", "custom", "yesterday", to, "", "", "", "requires from and to"},
		{"custom backwards", "custom", to, from, "", "", "", "must be after"},
		{"custom equal bounds", "custom", from, from, "", "", "", "must be after"},
		// Not in R10.15's vocabulary.
		{"unknown", "5m", "", "", "", "", "", "not one of"},
		{"unknown with bounds", "5m", from, to, "", "", "", "not one of"},
		// A preset wins and from/to are ignored, so no error about a parameter
		// that does not apply to it.
		{"bounds ignored on preset", "6h", from, to, "6h", "2026-11-21T03:12:03Z", "2026-11-21T09:12:03Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := map[string][]string{"fingerprints": {"a"}}
			if tc.rng != "" {
				q["range"] = []string{tc.rng}
			}
			if tc.from != "" {
				q["from"] = []string{tc.from}
			}
			if tc.to != "" {
				q["to"] = []string{tc.to}
			}
			got, err := parseSeriesQuery(q, now)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Range != tc.wantRange {
				t.Errorf("range = %q, want %q", got.Range, tc.wantRange)
			}
			if got.Start.UTC().Format(time.RFC3339) != tc.wantStart ||
				got.End.UTC().Format(time.RFC3339) != tc.wantEnd {
				t.Errorf("window = %s..%s, want %s..%s",
					got.Start.UTC().Format(time.RFC3339), got.End.UTC().Format(time.RFC3339),
					tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestParseSeriesQueryScoping(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 12, 3, 0, time.UTC)
	q, err := parseSeriesQuery(map[string][]string{
		"fingerprints": {"a"},
		"namespace":    {" postgres "},
		"cluster":      {"onepg"},
		"db":           {" appdb "},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// R8.4's scoping parameters, trimmed the way clusters.go trims them. The
	// route takes `db`, not `datname`: the parameter name is the shared
	// components/parameters/DB spelling even though the response field is
	// datname, which is the same asymmetry /api/v1/clusters has.
	if q.Namespace != "postgres" || q.Cluster != "onepg" || q.Datname != "appdb" {
		t.Errorf("scope = %+v, want postgres/onepg/appdb", q)
	}
}

// ---------------------------------------------------------------------------
// document pairing (R14.7)
// ---------------------------------------------------------------------------

// TestQuerySeriesRouteIsDocumented is what keeps the handler and
// api/openapi.yaml from being moved apart after this commit: the route must be
// under `paths` with an x-status, and must NOT still be under
// `x-planned-paths`. A route in code that the document does not list is R14.7's
// defect, and so is a documented route that 404s — promql_test.go reads the
// embedded document for the same reason on the R8.3 pair.
func TestQuerySeriesRouteIsDocumented(t *testing.T) {
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const key = "  " + QuerySeriesPath + ":\n"
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatalf("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	if at := strings.Index(paths, key); at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			QuerySeriesPath)
	} else {
		item := paths[at:]
		if next := strings.Index(item[2:], "\n  /"); next >= 0 {
			item = item[:next+2]
		}
		if !strings.Contains(item, "x-status: implemented") {
			t.Errorf("%s has no `x-status: implemented`", QuerySeriesPath)
		}
		// The 200 body must be the envelope the handler actually serves. The
		// type is an alias over dto.Envelope[QuerySeries], so the name the doc
		// refs and the name Go declares cannot drift silently.
		if !strings.Contains(item, "$ref: '#/components/schemas/QuerySeriesEnvelope'") {
			t.Errorf("%s's responses do not $ref QuerySeriesEnvelope", QuerySeriesPath)
		}
	}
	// The retirement note names the route in prose, exactly as the retired
	// r8.3-promql-pair note names its two routes, so a substring check would
	// always fail. Compare the *entries* — the `- /path` lines, which is what
	// scripts/check-openapi.py treats as the list's contents.
	for _, line := range strings.Split(planned, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue // a comment or a group key carries no route
		}
		fields := strings.Fields(line[2:])
		if len(fields) > 0 && fields[0] == QuerySeriesPath {
			t.Errorf("%s is still listed in x-planned-paths: it is in code now, "+
				"and R14.7 counts one name per route", QuerySeriesPath)
		}
	}
}
