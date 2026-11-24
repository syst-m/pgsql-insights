// Tests for R8.3 — the PromQL pair, GET|POST /api/v1/metrics/query and
// GET|POST /api/v1/metrics/system.
//
// R8.3 gives these routes three possible behaviours and the tests are organised
// around that: proxy mode (a Prometheus URL is configured and its answer is
// returned), catalogue mode (a fixed table of named queries is served in
// Prometheus result form), and PromQLUnavailable (anything else). Most of the
// risk in this file is not logic, it is *shape* — the exact JSON field names the
// MCP contract promises — so the shape has exact-string tests rather than
// structural ones.
//
// One test, TestCatalogueMatchesOpenAPIDocument, is the reason the catalogue is
// its own file. R8.3's last sentence is "the catalogue must be enumerated in the
// OpenAPI doc", and the only thing that keeps that true after the next edit is a
// test that reads the document and compares. Every other test here would still
// pass if the Go table and the documented table quietly drifted apart.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	openapi "github.com/frankellis/pgsql-insights/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// promQLStubSource stands in for Phase 2's MetricHub: it hands back canned
// series keyed by metric name, so catalogue-mode tests can exercise the
// filtering and the response shape without a store.
type promQLStubSource struct {
	series map[string][]PromQLSeries
	err    error
	calls  []string
}

func (s *promQLStubSource) Series(_ context.Context, e CatalogueEntry) ([]PromQLSeries, error) {
	s.calls = append(s.calls, e.Name)
	if s.err != nil {
		return nil, s.err
	}
	return s.series[e.Metric], nil
}

// promQLMux mounts the pair on a fresh mux, which is how server.go's Handler()
// uses it. Driving the mux rather than the unexported handlers means a route
// that stops being registered fails a test instead of being silently bypassed.
func promQLMux(cfg PromQLConfig) http.Handler {
	mux := http.NewServeMux()
	NewPromQL(cfg).Mount(mux)
	return mux
}

func doPromQL(t *testing.T, h http.Handler, method, target, body string) (int, []byte) {
	t.Helper()
	// A nil *strings.Reader is not a nil io.Reader: httptest.NewRequest calls
	// .Len() on whatever it is handed, so the typed-nil version panics on every
	// GET. The interface has to stay nil when there is no body.
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, bodyReader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// promQLGet and promQLPost are the two verbs R8.3 specifies, spelled once.
func promQLGet(t *testing.T, h http.Handler, target string) (int, []byte) {
	t.Helper()
	return doPromQL(t, h, http.MethodGet, target, "")
}

func promQLPost(t *testing.T, h http.Handler, target, body string) (int, []byte) {
	t.Helper()
	return doPromQL(t, h, http.MethodPost, target, body)
}

func mustDecodeError(t *testing.T, b []byte) dto.ErrorBody {
	t.Helper()
	var body dto.ErrorBody
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("body is not an R8.5 error body: %v\n%s", err, b)
	}
	if body.Error.Code == "" {
		t.Fatalf("error body has an empty code: %s", b)
	}
	if body.Error.Details == nil {
		t.Fatalf("error body has null details (R8.5: always an array): %s", b)
	}
	return body
}

func mustDecodeResponse(t *testing.T, b []byte) PromQLQueryResponse {
	t.Helper()
	var resp PromQLQueryResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("body is not a PromQLQueryResponse: %v\n%s", err, b)
	}
	return resp
}

// stubPrometheus is a fake upstream returning one fixed answer, plus the path
// and query string of the request it received.
func stubPrometheus(t *testing.T, status int, body string) (*httptest.Server, *string, *url.Values) {
	t.Helper()
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotPath, &gotQuery
}

// ---------------------------------------------------------------------------
// catalogue mode: the pair answers from the fixed table
// ---------------------------------------------------------------------------

func TestPromQLQueryEndpoints(t *testing.T) {
	// No PrometheusURL, so every case here is catalogue mode.
	h := promQLMux(PromQLConfig{Catalogue: DefaultCatalogue()})

	cases := []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
		wantCode   string // "" = the body must be a success, not an error
		wantMsgSub string
	}{
		// One accepted name per family, on the endpoint that owns it. The names
		// come from the catalogue itself (see the sub-test below), so this table
		// cannot pass by testing a query the catalogue no longer has.
		{
			name: "GET query, aggregate family", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.execution_time",
			wantStatus: http.StatusOK,
		},
		{
			name: "GET query, per-query family", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=perquery.rows",
			wantStatus: http.StatusOK,
		},
		{
			name: "GET system, system family", method: http.MethodGet,
			target:     SystemMetricsPath + "?promqlQuery=system.tps",
			wantStatus: http.StatusOK,
		},
		{
			name: "POST query, body form", method: http.MethodPost,
			target:     QueryMetricsPath,
			body:       `{"parent":"namespaces/postgres/clusters/onepg","promqlQuery":"aggregate.cache_hit_ratio"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "POST system, body form", method: http.MethodPost,
			target:     SystemMetricsPath,
			body:       `{"promqlQuery":"system.xact_commit"}`,
			wantStatus: http.StatusOK,
		},
		// R8.3 specifies GET *and* POST because the MCP is POST-only and a
		// browser/curl/Grafana client is GET-only. GET also accepts Prometheus'
		// own parameter spellings, which costs one line each and matches the most
		// likely non-MCP client for these routes.
		{
			name: "GET with Prometheus' own `query` spelling", method: http.MethodGet,
			target:     QueryMetricsPath + "?query=aggregate.shared_blocks_read",
			wantStatus: http.StatusOK,
		},
		{
			name: "GET with Prometheus' `start`/`end` spellings", method: http.MethodGet,
			target:     QueryMetricsPath + "?query=aggregate.rows&start=2026-11-26T09:00:00Z&end=2026-11-26T10:00:00Z",
			wantStatus: http.StatusOK,
		},
		// The endpoints mean something because they are disjoint: a query-metrics
		// name asked of /metrics/system is not found, and the error says where it
		// lives. This is the whole reason there are two routes and not one.
		{
			name: "query-metrics name on the system route", method: http.MethodGet,
			target:     SystemMetricsPath + "?promqlQuery=aggregate.execution_time",
			wantStatus: http.StatusNotFound, wantCode: dto.ErrPromQLUnavailable,
			wantMsgSub: "catalogued on " + QueryMetricsPath,
		},
		{
			name: "system name on the query route", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=system.tps",
			wantStatus: http.StatusNotFound, wantCode: dto.ErrPromQLUnavailable,
			wantMsgSub: "catalogued on " + SystemMetricsPath,
		},
		// The third mode: not in the catalogue, and no Prometheus to evaluate it.
		{
			name: "uncatalogued query, no Prometheus configured", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=sum(node_load1)",
			wantStatus: http.StatusNotFound, wantCode: dto.ErrPromQLUnavailable,
			wantMsgSub: "no Prometheus is configured",
		},
		// The 400s. R8.5 gives a malformed request and an unservable query
		// different codes, so they get different statuses — and validation runs
		// before the catalogue lookup, which is why a bad parent on the *system*
		// route is a 400 about the parent rather than a 404 about the query.
		{
			name: "missing query", method: http.MethodGet,
			target:     QueryMetricsPath,
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "promqlQuery is required",
		},
		{
			name: "blank query", method: http.MethodPost,
			target: QueryMetricsPath, body: `{"promqlQuery":"   "}`,
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "promqlQuery is required",
		},
		{
			name: "empty POST body", method: http.MethodPost,
			target: QueryMetricsPath, body: "",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "POST body is required",
		},
		{
			name: "unparseable POST body", method: http.MethodPost,
			target: QueryMetricsPath, body: `{"promqlQuery":`,
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "decode request body",
		},
		{
			name: "parent missing the cluster half", method: http.MethodGet,
			target:     SystemMetricsPath + "?promqlQuery=system.tps&parent=namespaces/postgres",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "namespaces/<ns>/clusters/<name>",
		},
		{
			name: "parent with an empty cluster name", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.rows&parent=namespaces/postgres/clusters/",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "empty namespace or cluster name",
		},
		{
			name: "one-sided window", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.rows&startTime=2026-11-26T09:00:00Z",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "startTime is set without the other bound",
		},
		{
			name: "one-sided window, end half", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.rows&endTime=2026-11-26T09:00:00Z",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "endTime is set without the other bound",
		},
		{
			name: "window that is not RFC 3339", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.rows&startTime=26-11-2026T09:00:00Z&endTime=2026-11-26T10:00:00Z",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "must be RFC 3339 timestamps",
		},
		{
			name: "window that runs backwards", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.rows&startTime=2026-11-26T10:00:00Z&endTime=2026-11-26T09:00:00Z",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "is before startTime",
		},
		{
			name: "resource key that is not one of R9.3's labels", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.rows&resource=dbname=appdb",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: `resource key "dbname"`,
		},
		{
			name: "resource without a value", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.rows&resource=datname",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "comma-separated key=value",
		},
		{
			name: "resource repeating a key", method: http.MethodGet,
			target:     QueryMetricsPath + "?promqlQuery=aggregate.rows&resource=datname=a,datname=b",
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "repeats key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			var body []byte
			if tc.method == http.MethodPost {
				code, body = promQLPost(t, h, tc.target, tc.body)
			} else {
				code, body = promQLGet(t, h, tc.target)
			}
			if code != tc.wantStatus {
				t.Fatalf("status %d, want %d (%s)", code, tc.wantStatus, body)
			}
			if tc.wantCode == "" {
				resp := mustDecodeResponse(t, body)
				if resp.PromQLResult == nil {
					t.Fatalf("200 with no promqlResult: %s", body)
				}
				if resp.PromQLResult.ResultType == "" {
					t.Fatalf("200 with an empty resultType: %s", body)
				}
				if resp.PromQLResult.Result == nil {
					t.Fatalf("200 with a null result (R8.3's shape is an array, empty or not): %s", body)
				}
				return
			}
			got := mustDecodeError(t, body)
			if got.Error.Code != tc.wantCode {
				t.Errorf("code %q, want %q (%s)", got.Error.Code, tc.wantCode, body)
			}
			if tc.wantMsgSub != "" && !strings.Contains(got.Error.Message, tc.wantMsgSub) {
				t.Errorf("message %q does not contain %q", got.Error.Message, tc.wantMsgSub)
			}
		})
	}
}

// TestPromQLEveryCataloguedNameAnswers walks the whole catalogue — all 36 names
// on both routes — and requires each one to answer 200 on its own endpoint. The
// table above samples; this is the one that proves the table has no holes.
func TestPromQLEveryCataloguedNameAnswers(t *testing.T) {
	h := promQLMux(PromQLConfig{Catalogue: DefaultCatalogue()})
	for _, e := range DefaultCatalogue().Entries() {
		t.Run(e.Name, func(t *testing.T) {
			code, body := promQLGet(t, h, e.Endpoint+"?promqlQuery="+url.QueryEscape(e.Name))
			if code != http.StatusOK {
				t.Fatalf("catalogued entry %s answered %d on %s: %s", e.Name, code, e.Endpoint, body)
			}
			resp := mustDecodeResponse(t, body)
			if resp.PromQLResult.ResultType != e.ResultType {
				t.Errorf("%s answered resultType %q, catalogue says %q", e.Name, resp.PromQLResult.ResultType, e.ResultType)
			}
		})
	}
}

func TestPromQLMountsOnlyGetAndPost(t *testing.T) {
	h := promQLMux(PromQLConfig{Catalogue: DefaultCatalogue()})
	for _, path := range []string{QueryMetricsPath, SystemMetricsPath} {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
			// Go's ServeMux answers 405 itself for a registered path with an
			// unregistered method, with an empty body — which is the right answer
			// and the one worth pinning, because a handler registered without a
			// method prefix would answer 200 to a PUT.
			code, body := doPromQL(t, h, method, path, "")
			if code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d, want 405 (%s)", method, path, code, body)
			}
		}
	}
	// And the routes exist at their documented paths only: /api/v1/metrics (no
	// suffix) is not a route, and answering it would mean the pair's paths are
	// not the ones R8.3 names.
	if code, body := promQLGet(t, h, "/api/v1/metrics"); code != http.StatusNotFound {
		t.Errorf("GET /api/v1/metrics: status %d, want 404 (%s)", code, body)
	}
}

// ---------------------------------------------------------------------------
// the wire shape, as exact strings
// ---------------------------------------------------------------------------

// TestPromQLJSONFieldNames pins the field names by comparing whole documents,
// not by decoding into the struct and comparing values (which cannot tell a
// renamed field from a correctly-named one — the struct is the same struct).
func TestPromQLJSONFieldNames(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{
			name: "request, all five MCP fields",
			v: PromQLQueryRequest{
				Parent: "namespaces/postgres/clusters/onepg", Resource: "datname=appdb",
				PromQLQuery: "aggregate.execution_time",
				StartTime:   "2026-11-26T09:00:00Z", EndTime: "2026-11-26T10:00:00Z",
			},
			want: `{"parent":"namespaces/postgres/clusters/onepg","resource":"datname=appdb",` +
				`"promqlQuery":"aggregate.execution_time","startTime":"2026-11-26T09:00:00Z",` +
				`"endTime":"2026-11-26T10:00:00Z"}`,
		},
		{
			// The MCP's minimum legal call: a query and nothing else. Everything
			// but promqlQuery is omitempty, so the one-field body is one field.
			name: "request, query only",
			v:    PromQLQueryRequest{PromQLQuery: "system.tps"},
			want: `{"promqlQuery":"system.tps"}`,
		},
		{
			name: "response",
			v: PromQLQueryResponse{PromQLResult: &PromQLResult{
				ResultType: "vector", Result: json.RawMessage(`[]`),
			}},
			want: `{"promqlResult":{"resultType":"vector","result":[]}}`,
		},
		{
			// A vector series: `value`, and no `values` key at all.
			name: "series, vector",
			v: PromQLSeries{Metric: map[string]string{"cluster": "onepg"},
				Value: &SamplePair{Timestamp: 1764209523, Value: "0.4182"}},
			want: `{"metric":{"cluster":"onepg"},"value":[1764209523.000,"0.4182"]}`,
		},
		{
			// A matrix series: `values`, and no `value` key at all.
			name: "series, matrix",
			v: PromQLSeries{Metric: map[string]string{"cluster": "onepg"},
				Values: []SamplePair{{Timestamp: 1764209523, Value: "1"}, {Timestamp: 1764209583, Value: "2"}}},
			want: `{"metric":{"cluster":"onepg"},"values":[[1764209523.000,"1"],[1764209583.000,"2"]]}`,
		},
		{
			// A series with no sample at all. Without the pointer on Value this
			// would marshal `[0.000,""]` — the epoch, worth nothing — which is a
			// reading, not the absence of one.
			name: "series, no sample",
			v:    PromQLSeries{Metric: map[string]string{"cluster": "onepg"}},
			want: `{"metric":{"cluster":"onepg"}}`,
		},
		{
			name: "sample pair",
			v:    SamplePair{Timestamp: 1764209523.5, Value: "1.5"},
			want: `[1764209523.500,"1.5"]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestSamplePairUnmarshal covers the decode side of the tuple: the string form
// Prometheus emits, the numeric form a hand-written fixture tends to produce,
// and the shapes that are not a pair at all.
func TestSamplePairUnmarshal(t *testing.T) {
	cases := []struct {
		in      string
		want    SamplePair
		wantErr string
	}{
		{in: `[1764209523.000,"0.4182"]`, want: SamplePair{Timestamp: 1764209523, Value: "0.4182"}},
		// A number where Prometheus would put a string: coerced, so a fixture
		// that forgets the quotes still decodes instead of erroring.
		{in: `[1764209523,2]`, want: SamplePair{Timestamp: 1764209523, Value: "2"}},
		{in: `[0,"0"]`, want: SamplePair{Timestamp: 0, Value: "0"}},
		{in: `[1764209523]`, wantErr: "want 2"},
		{in: `{"ts":1}`, wantErr: "two-element array"},
		{in: `[1764209523,true]`, wantErr: "string or a number"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var got SamplePair
			err := json.Unmarshal([]byte(tc.in), &got)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("unmarshal %s: err %v, want one containing %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal %s: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestPromQLResultNeverNullsTheArray is R8.5's Details rule applied to results:
// a client should not branch on whether the array exists.
func TestPromQLResultNeverNullsTheArray(t *testing.T) {
	got, err := json.Marshal(buildResponse(ResultTypeVector, nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"promqlResult":{"resultType":"vector","result":[]}}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// proxy mode
// ---------------------------------------------------------------------------

const upstreamVector = `{"status":"success","data":{"resultType":"vector","result":[` +
	`{"metric":{"__name__":"pgsi_db_tps","namespace":"postgres","cluster":"onepg"},` +
	`"value":[1764209523.000,"0.4182"]}]}}`

const upstreamResultArray = `[{"metric":{"__name__":"pgsi_db_tps","namespace":"postgres","cluster":"onepg"},` +
	`"value":[1764209523.000,"0.4182"]}]`

func TestPromQLProxyMode(t *testing.T) {
	t.Run("instant query goes to /api/v1/query and the answer is byte-exact", func(t *testing.T) {
		srv, gotPath, gotQuery := stubPrometheus(t, http.StatusOK, upstreamVector)
		h := promQLMux(PromQLConfig{PrometheusURL: srv.URL})

		code, body := promQLGet(t, h, QueryMetricsPath+"?promqlQuery=sum(pgsi_db_tps)")
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		if *gotPath != "/api/v1/query" {
			t.Errorf("upstream path %q, want /api/v1/query", *gotPath)
		}
		if got := gotQuery.Get("query"); got != "sum(pgsi_db_tps)" {
			t.Errorf("upstream query %q, want sum(pgsi_db_tps)", got)
		}
		// An instant query has no window, so it must not send start/end/step —
		// and no `time` either, because the request did not name an eval time.
		for _, k := range []string{"start", "end", "step", "time"} {
			if gotQuery.Get(k) != "" {
				t.Errorf("instant query sent %s=%q, want it absent", k, gotQuery.Get(k))
			}
		}
		// The promise of proxy mode: the answer *is* the upstream answer. The
		// result is carried as json.RawMessage precisely so this holds — a
		// decoded-and-re-encoded result would reorder keys.
		resp := mustDecodeResponse(t, body)
		if string(resp.PromQLResult.Result) != upstreamResultArray {
			t.Errorf("result %s\nwant %s", resp.PromQLResult.Result, upstreamResultArray)
		}
		if resp.PromQLResult.ResultType != "vector" {
			t.Errorf("resultType %q, want vector", resp.PromQLResult.ResultType)
		}
	})

	t.Run("a window turns it into /api/v1/query_range", func(t *testing.T) {
		srv, gotPath, gotQuery := stubPrometheus(t, http.StatusOK, upstreamVector)
		h := promQLMux(PromQLConfig{PrometheusURL: srv.URL})

		code, body := promQLGet(t, h,
			SystemMetricsPath+"?promqlQuery=system.tps&startTime=2026-11-26T09:00:00Z&endTime=2026-11-26T10:00:00Z")
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		if *gotPath != "/api/v1/query_range" {
			t.Errorf("upstream path %q, want /api/v1/query_range", *gotPath)
		}
		// The bug this assertion exists for: the first version set
		// u.Path = base + "/api/v1/query_range" on an already-parsed URL, which
		// requested http://host/http://host/api/v1/query_range.
		if got := gotQuery.Get("start"); got != "1795683600.000" {
			t.Errorf("start %q, want 1795683600.000", got)
		}
		if got := gotQuery.Get("end"); got != "1795687200.000" {
			t.Errorf("end %q, want 1795687200.000", got)
		}
		if got := gotQuery.Get("step"); got != "60s" {
			t.Errorf("step %q, want 60s (the 60s floor for a 1h window)", got)
		}
	})

	t.Run("proxy mode answers queries outside the catalogue", func(t *testing.T) {
		// R8.3's first clause wins over the catalogue: a deployment with a
		// Prometheus answers *anything* Prometheus can answer. Without this,
		// configuring a Prometheus would make the pair *narrower*, not wider.
		srv, _, _ := stubPrometheus(t, http.StatusOK, upstreamVector)
		h := promQLMux(PromQLConfig{PrometheusURL: srv.URL})
		code, body := promQLGet(t, h, QueryMetricsPath+"?promqlQuery=sum(node_load1)")
		if code != http.StatusOK {
			t.Fatalf("uncatalogued query in proxy mode: status %d, want 200 (%s)", code, body)
		}
	})

	t.Run("catalogue mode is what a missing URL means", func(t *testing.T) {
		// The mirror of the case above, so "proxy wins" cannot be satisfied by
		// always proxying: with no URL, the same query is a 404.
		h := promQLMux(PromQLConfig{Catalogue: DefaultCatalogue()})
		code, body := promQLGet(t, h, QueryMetricsPath+"?promqlQuery=sum(node_load1)")
		if code != http.StatusNotFound {
			t.Fatalf("status %d, want 404 (%s)", code, body)
		}
	})
}

func TestPromQLProxyUpstreamAnswers(t *testing.T) {
	// Prometheus's own status codes map onto R8.5's enum, and the mapping is the
	// testable part: the caller's syntax becomes InvalidRequest, the dependency's
	// problems become Unavailable, and the upstream message is echoed either way.
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantCode   string
		wantMsgSub string
	}{
		{
			name: "200 success", status: http.StatusOK, body: upstreamVector,
			wantStatus: http.StatusOK,
		},
		{
			name: "400 bad query", status: http.StatusBadRequest,
			body:       `{"status":"error","errorType":"bad_data","error":"invalid parameter"}`,
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "invalid parameter",
		},
		{
			// Prometheus answers an out-of-max-resolution range with 422, and it
			// is still the caller's window, not our dependency.
			name: "422 unprocessable", status: http.StatusUnprocessableEntity,
			body:       `{"status":"error","errorType":"bad_data","error":"exceeded maximum resolution"}`,
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "exceeded maximum resolution",
		},
		{
			name: "500 upstream down", status: http.StatusInternalServerError,
			body:       `{"status":"error","errorType":"execution","error":"internal error"}`,
			wantStatus: http.StatusServiceUnavailable, wantCode: dto.ErrUnavailable,
			wantMsgSub: "internal error",
		},
		{
			// A 200 whose body says error: the status lies, and the errorType
			// decides whose fault that is.
			name: "200 with errorType bad_data", status: http.StatusOK,
			body:       `{"status":"error","errorType":"bad_data","error":"parse error"}`,
			wantStatus: http.StatusBadRequest, wantCode: dto.ErrInvalidRequest,
			wantMsgSub: "parse error",
		},
		{
			name: "200 with errorType execution", status: http.StatusOK,
			body:       `{"status":"error","errorType":"execution","error":"query timeout"}`,
			wantStatus: http.StatusServiceUnavailable, wantCode: dto.ErrUnavailable,
			wantMsgSub: "query timeout",
		},
		{
			// `result` is always present in Prometheus's JSON, and always an
			// array; a null one is a malformed upstream, and normalising it to
			// [] here would hide a version difference we should notice.
			name: "200 success with a null result", status: http.StatusOK,
			body:       `{"status":"success","data":{"resultType":"vector","result":null}}`,
			wantStatus: http.StatusServiceUnavailable, wantCode: dto.ErrUnavailable,
			wantMsgSub: "null result",
		},
		{
			name: "200 success with no data", status: http.StatusOK,
			body:       `{"status":"success"}`,
			wantStatus: http.StatusServiceUnavailable, wantCode: dto.ErrUnavailable,
			wantMsgSub: "no usable data",
		},
		{
			name: "200 with a body that is not JSON", status: http.StatusOK,
			body:       `<html>502 Bad Gateway</html>`,
			wantStatus: http.StatusServiceUnavailable, wantCode: dto.ErrUnavailable,
			wantMsgSub: "not its documented envelope",
		},
		{
			// An empty result from upstream is a *legitimate* empty answer, and
			// it must survive as [] rather than becoming an error.
			name: "200 success with an empty result", status: http.StatusOK,
			body:       `{"status":"success","data":{"resultType":"vector","result":[]}}`,
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := stubPrometheus(t, tc.status, tc.body)
			h := promQLMux(PromQLConfig{PrometheusURL: srv.URL})
			code, body := promQLGet(t, h, QueryMetricsPath+"?promqlQuery=sum(pgsi_db_tps)")
			if code != tc.wantStatus {
				t.Fatalf("status %d, want %d (%s)", code, tc.wantStatus, body)
			}
			if tc.wantCode == "" {
				return
			}
			got := mustDecodeError(t, body)
			if got.Error.Code != tc.wantCode {
				t.Errorf("code %q, want %q", got.Error.Code, tc.wantCode)
			}
			if !strings.Contains(got.Error.Message, tc.wantMsgSub) {
				t.Errorf("message %q does not contain %q", got.Error.Message, tc.wantMsgSub)
			}
		})
	}
}

func TestPromQLProxyUnreachable(t *testing.T) {
	// A configured Prometheus that is not there is R8.5's "dependency down":
	// 503 Unavailable naming the URL, not a 500 and not a silent fall back to
	// the catalogue (which would answer a question about node_load1 with a
	// catalogue miss and hide the outage).
	srv, _, _ := stubPrometheus(t, http.StatusOK, upstreamVector)
	url := srv.URL
	srv.Close()

	h := promQLMux(PromQLConfig{PrometheusURL: url})
	code, body := promQLGet(t, h, QueryMetricsPath+"?promqlQuery=sum(pgsi_db_tps)")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 (%s)", code, body)
	}
	got := mustDecodeError(t, body)
	if got.Error.Code != dto.ErrUnavailable {
		t.Errorf("code %q, want %q", got.Error.Code, dto.ErrUnavailable)
	}
	if !strings.Contains(got.Error.Message, url) || !strings.Contains(got.Error.Message, "unreachable") {
		t.Errorf("message %q should name the URL %q and say unreachable", got.Error.Message, url)
	}
}

func TestPromQLProxyURLIsNotValid(t *testing.T) {
	// A malformed --prometheus-url is a configuration bug, not a dependency
	// outage and not the caller's request: 500 Internal, and it says so.
	h := promQLMux(PromQLConfig{PrometheusURL: "http://exa mple/"})
	code, body := promQLGet(t, h, QueryMetricsPath+"?promqlQuery=sum(pgsi_db_tps)")
	if code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500 (%s)", code, body)
	}
	if got := mustDecodeError(t, body); got.Error.Code != dto.ErrInternal {
		t.Errorf("code %q, want %q", got.Error.Code, dto.ErrInternal)
	}
}

func TestPromQLTimeArgAndStep(t *testing.T) {
	// Prometheus takes seconds with millisecond precision; RFC 3339 also works
	// but the numeric form cannot be misread as a local time.
	cases := []struct {
		in   time.Time
		want string
	}{
		// Go's zero Time and the epoch are two different zeros, and the row
		// that pins the first one used to be missing. promQLTimeArg
		// (promql.go:677-679) formats time.Time{} as -6795364578.871 rather
		// than 0.000: the zero Time is year 1, its UnixNano (-62135596800e9)
		// does not fit an int64, and Go documents the result as undefined
		// outside 1678..2262 -- so the number is what this toolchain returns,
		// not a meaning. The number is not the point; that it is not "0.000"
		// is.
		//
		// Ruled (coordinator, on the epoch follow-up A2 flagged from the
		// document's sample timestamp): pin the value rather than add an
		// IsZero guard inside promQLTimeArg. A caller that reaches the
		// formatter with time.Time{} has a bug upstream, and silently emitting
		// 0.000 converts "the caller forgot to set a time" into "this sample is
		// from 1970" -- the failure PromQLSeries.Value's pointer exists to
		// prevent, in those words about [0,"0"] at promql.go:130-132. The guard
		// belongs at the call sites, and both R8.3 call sites already have one:
		// promql.go:526 rejects a zero start or end in the range arm, and
		// promql.go:599 writes `time` only when !start.IsZero(). So this row
		// pins a value no live caller can produce, which is the point: a later
		// guard becomes a visible diff here instead of a silent one.
		{time.Time{}, "-6795364578.871"},
		{time.Unix(0, 0), "0.000"},
		{time.Unix(1764209523, 0), "1764209523.000"},
		{time.Unix(1764209523, 123456789), "1764209523.123"},
		{time.Unix(1795683600, 0), "1795683600.000"},
	}
	for _, tc := range cases {
		if got := promQLTimeArg(tc.in); got != tc.want {
			t.Errorf("promQLTimeArg(%v) = %q, want %q", tc.in.UTC(), got, tc.want)
		}
	}

	// The step is derived because R8.3's pair has no step parameter: 1100
	// points (a chart panel's resolution, an order of magnitude under
	// Prometheus's own per-series cap) with a 60s floor (the chart's scrape
	// interval — below it every step returns the same scraped sample).
	steps := []struct {
		span time.Duration
		want string
	}{
		{time.Minute, "60s"},          // 0.05s → floor
		{time.Hour, "60s"},            // 3.27s → floor
		{1100 * time.Second, "60s"},   // exactly 1s per point → floor
		{1100 * time.Minute, "60s"},   // exactly 60s per point → the floor exactly
		{72 * time.Hour, "235s"},      // 259200/1100 = 235.6 → 235
		{7 * 24 * time.Hour, "549s"},  // 604800/1100 = 549.8 → 549
		{11000 * time.Minute, "600s"}, // 660000/1100 = 600 exactly
	}
	base := time.Unix(1795683600, 0)
	for _, tc := range steps {
		if got := promQLStep(base, base.Add(tc.span)); got != tc.want {
			t.Errorf("promQLStep over %v = %q, want %q", tc.span, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// catalogue mode: filtering, sources, lookup
// ---------------------------------------------------------------------------

func TestPromQLParentAndResourceFiltering(t *testing.T) {
	series := []PromQLSeries{
		{Metric: map[string]string{"namespace": "postgres", "cluster": "onepg", "datname": "appdb"}},
		{Metric: map[string]string{"namespace": "postgres", "cluster": "onepg", "datname": "metrics"}},
		{Metric: map[string]string{"namespace": "postgres", "cluster": "twopg", "datname": "appdb"}},
		{Metric: map[string]string{"namespace": "other", "cluster": "onepg", "datname": "appdb"}},
		// A series with no cluster label at all: not "the empty cluster", but a
		// series from before R9.3's contract. Shown in a fleet query, dropped
		// from a query about one cluster — otherwise someone else's number gets
		// attributed to onepg.
		{Metric: map[string]string{"datname": "appdb"}},
	}
	src := &promQLStubSource{series: map[string][]PromQLSeries{
		"pgsi_query_execution_time_seconds_total": series,
	}}
	h := promQLMux(PromQLConfig{Catalogue: DefaultCatalogue(), Source: src})

	cases := []struct {
		name   string
		target string
		want   int
	}{
		{"no parent, whole fleet", QueryMetricsPath + "?promqlQuery=aggregate.execution_time", 5},
		{"one cluster", QueryMetricsPath + "?promqlQuery=aggregate.execution_time&parent=namespaces/postgres/clusters/onepg", 2},
		{"one cluster, one database", QueryMetricsPath + "?promqlQuery=aggregate.execution_time&parent=namespaces/postgres/clusters/onepg&resource=datname%3Dappdb", 1},
		// Four, not three: the cluster-less series has datname=appdb too, and a
		// resource filter matches on the label alone — it is only a *parent*
		// filter that requires the series to name a cluster.
		{"one database across clusters", QueryMetricsPath + "?promqlQuery=aggregate.execution_time&resource=datname%3Dappdb", 4},
		{"a cluster with no series", QueryMetricsPath + "?promqlQuery=aggregate.execution_time&parent=namespaces/postgres/clusters/nopg", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := promQLGet(t, h, tc.target)
			if code != http.StatusOK {
				t.Fatalf("status %d: %s", code, body)
			}
			resp := mustDecodeResponse(t, body)
			var got []PromQLSeries
			if err := json.Unmarshal(resp.PromQLResult.Result, &got); err != nil {
				t.Fatalf("result is not a series array: %v\n%s", err, resp.PromQLResult.Result)
			}
			if len(got) != tc.want {
				t.Errorf("got %d series, want %d: %s", len(got), tc.want, resp.PromQLResult.Result)
			}
		})
	}
}

func TestPromQLSourceErrorIsUnavailable(t *testing.T) {
	// A catalogued metric that exists but cannot be read is R8.5's Unavailable
	// (dependency down), not a 500 and not an empty answer — the two are
	// different states and a client must be able to tell them apart.
	src := &promQLStubSource{err: errors.New("store: no such round")}
	h := promQLMux(PromQLConfig{Catalogue: DefaultCatalogue(), Source: src})
	code, body := promQLGet(t, h, SystemMetricsPath+"?promqlQuery=system.tps")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 (%s)", code, body)
	}
	got := mustDecodeError(t, body)
	if got.Error.Code != dto.ErrUnavailable {
		t.Errorf("code %q, want %q", got.Error.Code, dto.ErrUnavailable)
	}
	if !strings.Contains(got.Error.Message, "no such round") {
		t.Errorf("message %q should carry the source's error", got.Error.Message)
	}
}

func TestPromQLDefaultSourceAnswersEmpty(t *testing.T) {
	// NewPromQL's default source: every catalogued metric exists with no
	// samples. This is what makes the pair shippable in Phase 5 — an empty
	// result is the honest answer for a collector with no rounds, where a 500
	// about a store that does not exist yet would be a lie about *why*.
	h := promQLMux(PromQLConfig{})
	code, body := promQLGet(t, h, SystemMetricsPath+"?promqlQuery=system.tps")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if want := `{"promqlResult":{"resultType":"vector","result":[]}}`; !strings.Contains(string(body), `"result":[]`) {
		t.Errorf("body %s, want an empty result array (want shape %s)", body, want)
	}
}

func TestPromQLLookup(t *testing.T) {
	c := DefaultCatalogue()
	entry, ok := c.Lookup("aggregate.execution_time")
	if !ok || entry.Name != "aggregate.execution_time" {
		t.Fatalf("lookup by name failed")
	}
	// The MCP's clients get the PromQL text back from the catalogue and send it
	// back verbatim, so the query string is a second legal spelling of a name.
	if _, ok := c.Lookup(entry.Query); !ok {
		t.Errorf("lookup by exact PromQL failed")
	}
	// Whitespace-only differences (a query written across four lines in a
	// dashboard) still match; case differences do not, because PromQL label
	// values are case-sensitive.
	if _, ok := c.Lookup("sum by (namespace, cluster, datname, usename)\n  (rate(pgsi_query_execution_time_seconds_total[5m]))"); !ok {
		t.Errorf("lookup by a reflowed PromQL query failed")
	}
	if _, ok := c.Lookup("AGGREGATE.EXECUTION_TIME"); !ok {
		t.Errorf("the name is documented as case-insensitive")
	}
	for _, bad := range []string{"", "   ", "sum(node_load1)", "aggregate.execution", "aggregate.execution_time_total"} {
		if _, ok := c.Lookup(bad); ok {
			t.Errorf("Lookup(%q) unexpectedly matched", bad)
		}
	}
	// A nil catalogue answers nothing, so a zero-value PromQL cannot panic.
	var nilCat *Catalogue
	if _, ok := nilCat.Lookup("system.tps"); ok {
		t.Errorf("nil catalogue matched a query")
	}
}

// ---------------------------------------------------------------------------
// the catalogue itself
// ---------------------------------------------------------------------------

// derivedNoMetric is the set of entries allowed an empty Metric: R9.4's two
// derived members, cache_hit_ratio (R5.2's ratio, an expression over the hit
// and read counters) and io_time (the R9.4 io_time ruling at 5668cb3, the sum
// of the two block-time counters). A set rather than a name, because the
// R9.4 cell states the cost of its own amendment as "one catalogue entry plus
// one predicate" -- this is that predicate, so a third derived member has to be
// named here to exist rather than quietly passing. The collector side has had
// the same plural shape since the same ruling
// (internal/collector/naming_catalogue_test.go:34) and its code skips on
// e.Metric == "" generically, so only this side needed widening.
var derivedNoMetric = map[string]bool{
	FamilyAggregate + ".cache_hit_ratio": true,
	FamilyAggregate + ".io_time":         true,
}

func TestCatalogueIsWellFormed(t *testing.T) {
	c := DefaultCatalogue()
	seen := map[string]bool{}
	for _, e := range c.Entries() {
		t.Run(e.Name, func(t *testing.T) {
			if seen[e.Name] {
				t.Errorf("duplicate entry name %q", e.Name)
			}
			seen[e.Name] = true
			if !catalogueNameRE.MatchString(e.Name) {
				t.Errorf("name %q is not <family>.<metric>", e.Name)
			}
			if e.Family != strings.SplitN(e.Name, ".", 2)[0] {
				t.Errorf("family %q does not match the name's prefix", e.Family)
			}
			switch e.Family {
			case FamilyAggregate, FamilyPerQuery, FamilySystem:
			default:
				t.Errorf("family %q is not one of the three R9 families", e.Family)
			}
			if e.Endpoint != QueryMetricsPath && e.Endpoint != SystemMetricsPath {
				t.Errorf("endpoint %q is not one of R8.3's two routes", e.Endpoint)
			}
			// R9.2's prefix rule: every metric the API reads is pgsi_-prefixed.
			// The exception is the derived members (derivedNoMetric), which have
			// no metric of their own because each is an expression over two.
			if e.Metric != "" && !metricNameRE.MatchString(e.Metric) {
				t.Errorf("metric %q does not match R9.2's pgsi_ prefix rule", e.Metric)
			}
			if e.Metric == "" && !derivedNoMetric[e.Name] {
				t.Errorf("entry %q has no metric and is not one of the derived members", e.Name)
			}
			if e.ResultType != ResultTypeVector && e.ResultType != ResultTypeMatrix {
				t.Errorf("resultType %q is not vector or matrix", e.ResultType)
			}
			if strings.TrimSpace(e.Query) == "" {
				t.Errorf("entry %q has no PromQL", e.Name)
			}
			if strings.TrimSpace(e.Description) == "" {
				t.Errorf("entry %q has no description", e.Name)
			}
		})
	}
	// The two routes are disjoint, which is what makes them two routes.
	q, s := c.For(QueryMetricsPath), c.For(SystemMetricsPath)
	if len(q) == 0 || len(s) == 0 {
		t.Fatalf("an endpoint has no entries: %d query, %d system", len(q), len(s))
	}
	byName := map[string]string{}
	for _, e := range q {
		byName[e.Name] = QueryMetricsPath
	}
	for _, e := range s {
		if other, dup := byName[e.Name]; dup {
			t.Errorf("%s is on both %s and %s", e.Name, other, SystemMetricsPath)
		}
	}
	if len(q)+len(s) != len(c.Entries()) {
		t.Errorf("For() covers %d of %d entries", len(q)+len(s), len(c.Entries()))
	}
	if got := c.For("/api/v1/nope"); got != nil {
		t.Errorf("For(unknown endpoint) = %d entries, want none", len(got))
	}
	// Entries() is sorted and a copy: the doc and the tests depend on the order
	// being stable, and a caller mutating the result must not corrupt the table.
	names := []string{}
	for _, e := range c.Entries() {
		names = append(names, e.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("Entries() is not sorted by name")
	}
}

// catalogueNameRE mirrors the pattern scripts/check-openapi.py enforces, so a
// name that passes the Go tests cannot fail the CI document check.
var catalogueNameRE = regexp.MustCompile(`^(aggregate|perquery|system)\.[a-z0-9_]+$`)

// TestCatalogueMatchesOpenAPIDocument is R8.3's last sentence made executable:
// "The catalogue must be enumerated in the OpenAPI doc." It reads the *embedded*
// document (the bytes a running binary serves, per R8.1/D13) and requires the
// x-promql-catalogue list under each route to be exactly the set of names the
// Go catalogue serves there.
func TestCatalogueMatchesOpenAPIDocument(t *testing.T) {
	doc, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	docs := docCatalogues(t, string(doc))
	if len(docs) != 2 {
		t.Fatalf("the document enumerates catalogues for %d paths (%v), want exactly the two R8.3 routes",
			len(docs), keysOf(docs))
	}
	for _, endpoint := range []string{QueryMetricsPath, SystemMetricsPath} {
		want := namesOf(DefaultCatalogue().For(endpoint))
		got := docs[endpoint]
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s catalogue differs from the Go catalogue\nin doc: %s\nin go: %s",
				endpoint, strings.Join(got, ","), strings.Join(want, ","))
		}
	}
}

// docCatalogues pulls the x-promql-catalogue lists out of the document without a
// YAML parser (the api package has no yaml dependency and this test should not
// add one). A list is the run of `- name` lines at exactly keyIndent+2 after the
// key — the same bounding rule scripts/check-openapi.py uses, which is what
// keeps a path's own `parameters:` items from reading as more entries.
func docCatalogues(t *testing.T, text string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	var path string
	keyIndent := -1
	current := ""
	for _, raw := range strings.Split(text, "\n") {
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		trimmed := strings.TrimLeft(raw, " ")
		indent := len(raw) - len(trimmed)

		if keyIndent >= 0 {
			if indent == keyIndent+2 && strings.HasPrefix(trimmed, "- ") {
				out[current] = append(out[current], strings.TrimSpace(trimmed[2:]))
				continue
			}
			if indent <= keyIndent+2 {
				keyIndent = -1 // the run ended
			}
		}
		if strings.HasSuffix(trimmed, ":") && strings.HasPrefix(trimmed, "/") {
			path = strings.TrimSuffix(trimmed, ":")
			continue
		}
		if path != "" && trimmed == "x-promql-catalogue:" {
			keyIndent, current = indent, path
			if _, ok := out[path]; !ok {
				out[path] = []string{}
			}
		}
	}
	return out
}

func namesOf(entries []CatalogueEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
