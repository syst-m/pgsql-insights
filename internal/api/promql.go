// R8.3 — the PromQL pair: GET|POST /api/v1/metrics/query and
// GET|POST /api/v1/metrics/system.
//
// R8.3 copies the Cloud SQL MCP's get_query_metrics / get_system_metrics: the
// same request object ({parent, resource, promqlQuery, startTime, endTime}) and
// the same answer ({promqlResult}). The reason to copy a shape rather than
// design one is a client: a tool written against Google's MCP can be repointed
// at pgsi by changing a base URL, which is the whole parity argument for R8.3
// existing as a requirement instead of being folded into R8.2's route table.
//
// Three answer modes, tried in this order:
//
//  1. proxy — a Prometheus URL is configured (--prometheus-url). The query goes
//     to /api/v1/query or /api/v1/query_range and the upstream result is passed
//     through untouched, so a proxied answer is exactly Prometheus's answer.
//  2. catalogue — no Prometheus configured. §3.4 keeps raw samples out of
//     SQLite and we ship no PromQL evaluator, so the only honest answer is the
//     fixed table in promql_catalogue.go: R9.4's aggregate and R9.5's per-query
//     rollups behind /metrics/query, R9.7's system family behind
//     /metrics/system, serialized in Prometheus result form.
//  3. PromQLUnavailable — a query in neither mode. R8.5 added
//     error.code=PromQLUnavailable for this one sentence, and 404 is its status.
//
// Why 404 and not 400 for a query we do not have: 400 InvalidRequest says the
// *request* is malformed, and `sum(node_load1)` is a perfectly well-formed
// PromQL string — the client did nothing wrong, the server cannot serve it.
// R8.5 gives the two cases different codes, so they get different statuses. A
// genuinely malformed request (no query at all, a parent that is not an
// address, an unparseable startTime) is still 400 InvalidRequest.
//
// The two handlers are separate functions over one code path, and the pair is
// not two aliases: Catalogue.For(endpoint) partitions the table, so a
// per-query name asked of /metrics/system is not found. Two URLs that answer
// identically for every input would be one URL, and R8.3 specifies two because
// the MCP specifies two.
//
// The writers here are named writePromQLJSON/writePromQLError rather than
// reusing clusters.go's writeJSON/writeErrorBody. Not style: clusters.go is
// this branch's in-flight work, and a handler in a committed file calling into
// an uncommitted one compiles in the working tree and breaks the moment anyone
// checks out the commit on its own. The bodies are three lines and identical
// apart from their names; when clusters.go lands, one pair can be deleted in a
// one-line-per-call follow-up.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// PromQLQueryRequest is R8.3's request object, field-for-field the MCP's
// get_query_metrics / get_system_metrics parameters.
type PromQLQueryRequest struct {
	// Parent is the cluster the query is scoped to, in R8.2's addressing:
	// `namespaces/<ns>/clusters/<name>`. That is the pgsi translation of the
	// MCP's `projects/<p>/instances/<i>` — same two-level shape, our two levels
	// being namespace and Cluster instead of project and instance. Empty means
	// the whole fleet, which is the reading a Prometheus-backed dashboard uses
	// when it has no single cluster in mind.
	Parent string `json:"parent,omitempty"`
	// Resource is the MCP's optional sub-resource. In pgsi it is a
	// comma-separated list of R9.3's remaining label selectors —
	// `datname=appdb,usename=app`, or `pooler=rw` — validated here and applied
	// as a label filter in catalogue mode. In proxy mode it is validated and
	// *not* applied: the PromQL string we forward already carries its own
	// selectors, and injecting labels into someone else's query is exactly the
	// evaluator R8.3 says we do not ship.
	Resource string `json:"resource,omitempty"`
	// PromQLQuery is the query. In catalogue mode this is a catalogue name
	// (`aggregate.execution_time`) or the exact PromQL behind one; in proxy mode
	// it is any PromQL string.
	PromQLQuery string `json:"promqlQuery"`
	// StartTime and EndTime bound a range query (RFC 3339, the format Prometheus
	// itself accepts). Both present → range; both absent → instant; exactly one
	// present → 400, because "the last hour of a range with no end" is not a
	// range and Prometheus has no endpoint for it.
	StartTime string `json:"startTime,omitempty"`
	EndTime   string `json:"endTime,omitempty"`
}

// PromQLQueryResponse is R8.3's answer object. Both endpoints answer with it,
// which is why there is one type and not two: the MCP's two tools return the
// same field, and a second type with the same fields would be a second name for
// one shape.
type PromQLQueryResponse struct {
	// PromQLResult is null-able on purpose: an error response carries no
	// result at all (it is an ErrorBody instead), and on success this is never
	// nil — a query that matched nothing answers with resultType plus an empty
	// result array, not with a null result.
	PromQLResult *PromQLResult `json:"promqlResult"`
}

// PromQLResult is the Prometheus result envelope: a resultType and the body
// that type licenses.
type PromQLResult struct {
	// ResultType is ResultTypeVector or ResultTypeMatrix for anything we
	// generate. In proxy mode it is whatever Prometheus reported, which can
	// also be `scalar` or `string`; the field is a string rather than an
	// enum-typed constant set precisely so a proxied answer we did not
	// anticipate still round-trips.
	ResultType string `json:"resultType"`
	// Result is the raw JSON array (or scalar) Prometheus returned, or the
	// array we built. json.RawMessage rather than []PromQLSeries so a proxied
	// answer keeps the exact bytes Prometheus wrote: re-marshalling a decoded
	// value reorders object keys and changes a payload a client may hash or
	// diff, and R8.3's proxy promise is that the answer is Prometheus's.
	Result json.RawMessage `json:"result"`
}

// PromQLSeries is one series in a result: a label set plus its sample(s).
// Prometheus's vocabulary puts `value` on a vector and `values` on a matrix;
// both are optional here so one type serves both result types, and
// TestPromQLSeriesMarshalsPerResultType pins that a vector marshals without
// `values` and a matrix without `value`.
type PromQLSeries struct {
	Metric map[string]string `json:"metric"`
	// Value is the single sample of a vector result. A pointer so "no value"
	// is representable: a plain SamplePair would marshal a missing sample as
	// `[0,"0"]`, which is a real reading (the epoch, worth zero) rather than
	// the absence of one.
	Value *SamplePair `json:"value,omitempty"`
	// Values is the samples of a matrix result.
	Values []SamplePair `json:"values,omitempty"`
}

// SamplePair is Prometheus's `[unix_seconds, "value"]` tuple.
//
// Prometheus's JSON has no tuple type — it encodes the pair as a two-element
// *array* — and neither does OpenAPI 3.0 (which is why the document describes it
// with `items: {oneOf: [...]}` instead of a property object). The Go type is a
// struct so the fields are nameable and documentable; the marshalling is what
// makes it a tuple on the wire.
type SamplePair struct {
	Timestamp float64
	Value     string
}

// MarshalJSON emits the two-element array form: `[1700000000.000,"1.5"]`.
//
// The timestamp keeps three decimal places because that is what Prometheus
// emits (millisecond resolution, sub-second precision preserved) and the value
// is a *string* because Prometheus renders sample values as strings — a float
// on the wire loses the distinction between "1.5" and "1.5000000000000002" that
// the upstream text keeps.
func (s SamplePair) MarshalJSON() ([]byte, error) {
	ts := strconv.FormatFloat(s.Timestamp, 'f', 3, 64)
	v, err := json.Marshal(s.Value)
	if err != nil {
		return nil, err
	}
	return []byte("[" + ts + "," + string(v) + "]"), nil
}

// UnmarshalJSON accepts the array form so a proxied result can be decoded into
// PromQLSeries (the tests do this; the handlers keep the raw bytes). It also
// accepts a bare `[ts, value]` where value is a number, coercing it to the
// string form, so a hand-written fixture that forgets the quotes still decodes.
func (s *SamplePair) UnmarshalJSON(b []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(b, &pair); err != nil {
		return fmt.Errorf("sample pair must be a two-element array: %w", err)
	}
	if len(pair) != 2 {
		return fmt.Errorf("sample pair has %d elements, want 2", len(pair))
	}
	if err := json.Unmarshal(pair[0], &s.Timestamp); err != nil {
		return fmt.Errorf("sample pair timestamp: %w", err)
	}
	// Try the string form first (what Prometheus emits), then any number.
	if err := json.Unmarshal(pair[1], &s.Value); err == nil {
		return nil
	}
	var f float64
	if err := json.Unmarshal(pair[1], &f); err != nil {
		return fmt.Errorf("sample pair value must be a string or a number: %w", err)
	}
	s.Value = strconv.FormatFloat(f, 'f', -1, 64)
	return nil
}

// PromQLSource is where catalogue-mode series come from.
//
// Phase 2's MetricHub implements it. Until then NewPromQL installs an empty
// source, which is what makes the pair shippable in Phase 5: the routes exist,
// the catalogue is enumerated and tested, the response shape is fixed, and a
// query that matches an entry answers with an empty result set instead of a
// 500 about a store that does not exist yet. An empty result is the honest
// answer for a collector that has run no rounds; a 500 would be a lie about
// *why* there is no data.
type PromQLSource interface {
	Series(ctx context.Context, entry CatalogueEntry) ([]PromQLSeries, error)
}

// emptySource is NewPromQL's default: every catalogued metric exists and has no
// samples yet.
type emptySource struct{}

func (emptySource) Series(context.Context, CatalogueEntry) ([]PromQLSeries, error) {
	return nil, nil
}

// PromQLConfig configures the pair.
type PromQLConfig struct {
	// Catalogue is the named-query table. Nil selects DefaultCatalogue().
	Catalogue *Catalogue
	// PrometheusURL switches the pair from catalogue mode to proxy mode when
	// non-empty (R8.3's first sentence: "When a Prometheus URL is configured,
	// the query is proxied to it"). From --prometheus-url / the chart's
	// metrics.prometheusURL.
	PrometheusURL string
	// Source answers catalogue-mode queries. Nil selects an empty source (see
	// PromQLSource's doc for why that is the Phase 5 default).
	Source PromQLSource
	// HTTPClient is the client used in proxy mode. Nil selects a client with a
	// 10s timeout — a fixed budget rather than a flag, because a slow Prometheus
	// is a dependency problem and R8.5's Unavailable should arrive before a
	// client's own timeout does.
	HTTPClient *http.Client
}

// PromQL serves R8.3's pair.
type PromQL struct {
	cfg PromQLConfig
}

// NewPromQL builds the pair from cfg, filling the defaults PromQLConfig
// documents.
func NewPromQL(cfg PromQLConfig) *PromQL {
	if cfg.Catalogue == nil {
		cfg.Catalogue = DefaultCatalogue()
	}
	if cfg.Source == nil {
		cfg.Source = emptySource{}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &PromQL{cfg: cfg}
}

// Mount registers all four of R8.3's method+path pairs on mux.
//
// It takes the mux rather than an http.Handler because server.go's Handler()
// builds one ServeMux holding the whole route table and then wraps it in CORS
// and auth; mounting here puts the pair inside both wrappers, which is what
// puts them under --auth (R8.8's default branch: everything that is not a probe
// or /metrics is GroupAPI).
func (p *PromQL) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET "+QueryMetricsPath, p.handleQuery)
	mux.HandleFunc("POST "+QueryMetricsPath, p.handleQuery)
	mux.HandleFunc("GET "+SystemMetricsPath, p.handleSystem)
	mux.HandleFunc("POST "+SystemMetricsPath, p.handleSystem)
}

func (p *PromQL) handleQuery(w http.ResponseWriter, r *http.Request) {
	p.serve(w, r, QueryMetricsPath)
}

func (p *PromQL) handleSystem(w http.ResponseWriter, r *http.Request) {
	p.serve(w, r, SystemMetricsPath)
}

// serve is the whole R8.3 contract in eight lines: decode, resolve, write.
func (p *PromQL) serve(w http.ResponseWriter, r *http.Request, endpoint string) {
	req, err := decodePromQLRequest(r)
	if err != nil {
		writePromQLError(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	resp, perr := p.resolve(r.Context(), endpoint, req)
	if perr != nil {
		writePromQLError(w, perr.status, perr.body)
		return
	}
	writePromQLJSON(w, http.StatusOK, resp)
}

// promQLError carries the status and the R8.5 body together, so resolve can
// answer "404 PromQLUnavailable" as one value instead of returning a pair of
// loosely-coupled things that a future edit can mismatch.
type promQLError struct {
	status int
	body   dto.ErrorBody
}

func (e promQLError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.status, e.body.Error.Code, e.body.Error.Message)
}

func newPromQLError(status int, code, msg string, details ...string) *promQLError {
	return &promQLError{status: status, body: dto.NewError(code, msg, details...)}
}

// resolve applies the three modes. It returns either a response or an error,
// never both.
func (p *PromQL) resolve(ctx context.Context, endpoint string, req PromQLQueryRequest) (*PromQLQueryResponse, *promQLError) {
	q := strings.TrimSpace(req.PromQLQuery)
	if q == "" {
		return nil, newPromQLError(http.StatusBadRequest, dto.ErrInvalidRequest,
			"promqlQuery is required")
	}
	ns, cluster, err := parsePromQLParent(req.Parent)
	if err != nil {
		return nil, newPromQLError(http.StatusBadRequest, dto.ErrInvalidRequest, err.Error())
	}
	resource, err := parsePromQLResource(req.Resource)
	if err != nil {
		return nil, newPromQLError(http.StatusBadRequest, dto.ErrInvalidRequest, err.Error())
	}
	// windowErr, not err: `err` is already bound above to parsePromQLParent's
	// plain `error`, so `:=` here would assign it rather than redeclare it and
	// the return would not type-check.
	start, end, isRange, windowErr := parsePromQLWindow(req)
	if windowErr != nil {
		return nil, windowErr
	}

	// Mode 1: proxy. R8.3's first clause, and it wins whenever a URL is set —
	// a deployment that points at a Prometheus answers *every* query, including
	// ones not in our catalogue, because Prometheus evaluates them for us.
	if p.cfg.PrometheusURL != "" {
		return p.proxy(ctx, endpoint, q, start, end, isRange)
	}

	// Mode 2: catalogue — scoped to *this* endpoint, which is what makes the two
	// routes two. LookupOn(/metrics/query) never returns a system.* entry, so
	// asking the query route for one falls through to mode 3 below, whose message
	// names the route that does answer it.
	entry, ok := p.cfg.Catalogue.LookupOn(endpoint, q)
	if !ok {
		// Mode 3. The message names the endpoint and points at the enumeration,
		// because the fix for this error is always "look at the list", and the
		// list is in the document the same request could have fetched.
		msg := fmt.Sprintf("query %q is not in the %s catalogue and no Prometheus is configured to evaluate it (R8.3)", q, endpoint)
		if other := p.otherEndpointHas(q, endpoint); other != "" {
			msg = fmt.Sprintf("query %q is catalogued on %s, not %s", q, other, endpoint)
		}
		return nil, newPromQLError(http.StatusNotFound, dto.ErrPromQLUnavailable, msg)
	}
	series, err := p.cfg.Source.Series(ctx, entry)
	if err != nil {
		return nil, newPromQLError(http.StatusServiceUnavailable, dto.ErrUnavailable,
			fmt.Sprintf("catalogued metric %s is not readable: %v", entry.Metric, err))
	}
	series = filterSeries(series, ns, cluster, resource)
	return buildResponse(entry.ResultType, series), nil
}

// otherEndpointHas reports the *other* R8.3 endpoint that knows this query, or
// "". It turns a bare 404 into one that says where to go, which is the difference
// between an error a client recovers from and one it has to read the spec for.
func (p *PromQL) otherEndpointHas(q, endpoint string) string {
	other := SystemMetricsPath
	if endpoint == SystemMetricsPath {
		other = QueryMetricsPath
	}
	if _, ok := p.cfg.Catalogue.LookupOn(other, q); ok {
		return other
	}
	return ""
}

// buildResponse serialises series in Prometheus result form. The empty case
// marshals to `[]`, never `null`: R8.5's Details rule applies to results too —
// a client should not have to branch on whether the array exists.
func buildResponse(resultType string, series []PromQLSeries) *PromQLQueryResponse {
	if series == nil {
		series = []PromQLSeries{}
	}
	raw, err := json.Marshal(series)
	if err != nil {
		// A []PromQLSeries always marshals (map[string]string and two scalars),
		// so this is unreachable in practice; an error here is a bug, not a
		// runtime condition, and 500 is the honest answer for a bug.
		return &PromQLQueryResponse{PromQLResult: &PromQLResult{
			ResultType: resultType,
			Result:     json.RawMessage("[]"),
		}}
	}
	return &PromQLQueryResponse{PromQLResult: &PromQLResult{
		ResultType: resultType,
		Result:     raw,
	}}
}

// decodePromQLRequest reads R8.3's request from either verb.
//
// GET carries the same fields as query parameters (the MCP is POST-only; R8.3
// adds GET so a browser, a curl one-liner and a Prometheus-style dashboard can
// all issue the pair without a body), POST carries them as a JSON body. The
// Prometheus parameter names are accepted as aliases — `query` for promqlQuery,
// `start`/`end` for startTime/endTime — because the single most likely client
// for these routes is a Grafana-style explore box that already speaks
// Prometheus' spelling, and accepting both costs one line each.
func decodePromQLRequest(r *http.Request) (PromQLQueryRequest, error) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		q := r.URL.Query()
		return PromQLQueryRequest{
			Parent:      q.Get("parent"),
			Resource:    q.Get("resource"),
			PromQLQuery: firstNonEmpty(q.Get("promqlQuery"), q.Get("query")),
			StartTime:   firstNonEmpty(q.Get("startTime"), q.Get("start")),
			EndTime:     firstNonEmpty(q.Get("endTime"), q.Get("end")),
		}, nil
	case http.MethodPost:
		var req PromQLQueryRequest
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return req, fmt.Errorf("read request body: %v", err)
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			return req, fmt.Errorf("a POST body is required (an empty body asks no query)")
		}
		if err := json.Unmarshal(b, &req); err != nil {
			return req, fmt.Errorf("decode request body: %v", err)
		}
		return req, nil
	default:
		return PromQLQueryRequest{}, fmt.Errorf("%s is not allowed on this route", r.Method)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parsePromQLParent accepts R8.2's cluster addressing. The grammar is exactly
// `namespaces/<ns>/clusters/<name>` — the MCP's two-level parent with our two
// levels — plus the empty string meaning "every cluster the caller can see".
//
// It deliberately does not accept `namespaces/<ns>` alone. That looks harmless
// and is not: it would make "all of a namespace" and "one cluster" the same
// parameter shape, so a typo that drops `/clusters/x` silently widens a query
// from one cluster to every cluster in the namespace and returns a correct-
// looking answer to the wrong question.
func parsePromQLParent(s string) (namespace, cluster string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", nil
	}
	parts := strings.Split(s, "/")
	if len(parts) != 4 || parts[0] != "namespaces" || parts[2] != "clusters" {
		return "", "", fmt.Errorf("parent %q is not of the form namespaces/<ns>/clusters/<name>", s)
	}
	if parts[1] == "" || parts[3] == "" {
		return "", "", fmt.Errorf("parent %q has an empty namespace or cluster name", s)
	}
	return parts[1], parts[3], nil
}

// parsePromQLResource parses the `datname=x,usname=y` sub-resource into a label
// filter. The accepted keys are exactly R9.3's optional labels — datname,
// usename, pooler — because a key outside that set is a label no pgsi series
// has, and filtering on it answers "no series" for a question that meant
// something else.
func parsePromQLResource(s string) (map[string]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("resource %q is not a comma-separated key=value list", s)
		}
		switch k {
		case "datname", "usename", "pooler":
		default:
			return nil, fmt.Errorf("resource key %q is not one of R9.3's labels (datname, usename, pooler)", k)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("resource %q repeats key %q", s, k)
		}
		out[k] = v
	}
	return out, nil
}

// parsePromQLWindow validates the time bounds and decides instant vs range.
//
// The format is RFC 3339 (Prometheus's own accepted format, and R8.4's rule for
// every timestamp the API takes). A fractional-second timestamp is accepted and
// normalised to UTC; a relative expression like Prometheus's own `-1h` is not,
// because that syntax belongs to a UI's URL bar and not to a machine-readable
// API contract.
func parsePromQLWindow(req PromQLQueryRequest) (start, end time.Time, isRange bool, err *promQLError) {
	hasStart, hasEnd := strings.TrimSpace(req.StartTime) != "", strings.TrimSpace(req.EndTime) != ""
	switch {
	case !hasStart && !hasEnd:
		return time.Time{}, time.Time{}, false, nil
	case hasStart != hasEnd:
		which := "startTime"
		if hasEnd {
			which = "endTime"
		}
		return time.Time{}, time.Time{}, false, newPromQLError(http.StatusBadRequest, dto.ErrInvalidRequest,
			which+" is set without the other bound; a range needs both startTime and endTime")
	}
	s, e := parsePromQLTime(req.StartTime), parsePromQLTime(req.EndTime)
	if s.IsZero() || e.IsZero() {
		return time.Time{}, time.Time{}, false, newPromQLError(http.StatusBadRequest, dto.ErrInvalidRequest,
			fmt.Sprintf("startTime %q and endTime %q must be RFC 3339 timestamps", req.StartTime, req.EndTime))
	}
	if e.Before(s) {
		return time.Time{}, time.Time{}, false, newPromQLError(http.StatusBadRequest, dto.ErrInvalidRequest,
			fmt.Sprintf("endTime %q is before startTime %q", req.EndTime, req.StartTime))
	}
	return s, e, true, nil
}

// parsePromQLTime parses RFC 3339 with or without fractional seconds and returns
// the zero time on failure, so the caller decides which spelling to report.
func parsePromQLTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// upstreamResponse is Prometheus's /api/v1/query envelope.
type upstreamResponse struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
}

// upstreamData is the inside of a successful upstream response.
type upstreamData struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

// proxy forwards the query to Prometheus and returns its answer untouched.
func (p *PromQL) proxy(ctx context.Context, endpoint, query string, start, end time.Time, isRange bool) (*PromQLQueryResponse, *promQLError) {
	base := strings.TrimRight(p.cfg.PrometheusURL, "/")
	// The path is picked before the parse, not assigned to u.Path afterwards:
	// u.Path on a parsed URL is only the path component, so
	// `u.Path = base + "/api/v1/query_range"` yields
	// `http://host/http://host/api/v1/query_range` — which is what the first
	// version of this function requested for every range query.
	path := "/api/v1/query"
	if isRange {
		path = "/api/v1/query_range"
	}
	u, err := url.Parse(base + path)
	if err != nil {
		return nil, newPromQLError(http.StatusInternalServerError, dto.ErrInternal,
			fmt.Sprintf("--prometheus-url %q is not a valid URL: %v", p.cfg.PrometheusURL, err))
	}
	q := u.Query()
	q.Set("query", query)
	if isRange {
		// R8.3's pair has no `step` parameter — the MCP's range queries take only
		// a window — so the step is derived. 1100 points is the resolution of a
		// chart panel and an order of magnitude under Prometheus's own 11 000
		// point-per-series cap, so a proxied range never fails with
		// "exceeded maximum resolution"; the 60s floor is the chart's
		// metrics.serviceMonitor.interval, below which every step returns the
		// same scraped sample.
		q.Set("start", promQLTimeArg(start))
		q.Set("end", promQLTimeArg(end))
		q.Set("step", promQLStep(start, end))
	} else if !start.IsZero() {
		q.Set("time", promQLTimeArg(start))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, newPromQLError(http.StatusInternalServerError, dto.ErrInternal,
			fmt.Sprintf("build upstream request: %v", err))
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, newPromQLError(http.StatusServiceUnavailable, dto.ErrUnavailable,
			fmt.Sprintf("prometheus at %s is unreachable: %v", base, err))
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, newPromQLError(http.StatusServiceUnavailable, dto.ErrUnavailable,
			fmt.Sprintf("read prometheus response: %v", err))
	}
	var up upstreamResponse
	if err := json.Unmarshal(b, &up); err != nil {
		return nil, newPromQLError(http.StatusServiceUnavailable, dto.ErrUnavailable,
			fmt.Sprintf("prometheus at %s returned a response that is not its documented envelope: %v", base, err))
	}
	if resp.StatusCode != http.StatusOK {
		// Prometheus answers a bad query with 400 and an out-of-range one with
		// 422; both are the caller's syntax, so they become InvalidRequest and
		// echo the upstream message. Everything else is the dependency's
		// problem: 503 Unavailable (R8.5's "dependency down").
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
			return nil, newPromQLError(http.StatusBadRequest, dto.ErrInvalidRequest,
				fmt.Sprintf("prometheus rejected the query: %s", upstreamMessage(up, resp.Status)))
		}
		return nil, newPromQLError(http.StatusServiceUnavailable, dto.ErrUnavailable,
			fmt.Sprintf("prometheus at %s answered %s", base, upstreamMessage(up, resp.Status)))
	}
	if up.Status != "success" {
		if up.ErrorType == "bad_data" {
			return nil, newPromQLError(http.StatusBadRequest, dto.ErrInvalidRequest,
				fmt.Sprintf("prometheus rejected the query: %s", upstreamMessage(up, resp.Status)))
		}
		return nil, newPromQLError(http.StatusServiceUnavailable, dto.ErrUnavailable,
			fmt.Sprintf("prometheus at %s reported %s: %s", base, up.Status, upstreamMessage(up, resp.Status)))
	}
	var data upstreamData
	if err := json.Unmarshal(up.Data, &data); err != nil {
		return nil, newPromQLError(http.StatusServiceUnavailable, dto.ErrUnavailable,
			fmt.Sprintf("prometheus at %s returned success with no usable data: %v", base, err))
	}
	// Two shapes are "no result": the key missing (nil RawMessage) and the key
	// present as JSON null — which unmarshals to the four bytes `null`, not to a
	// nil slice, so the nil check alone lets it through and the API ends up
	// serving `{"result":null}`, a shape R8.3's schema does not allow. Prometheus
	// itself always emits an array, `[]` when there is nothing; normalising
	// either spelling to [] here would hide an upstream we should notice.
	if data.Result == nil || string(data.Result) == "null" {
		return nil, newPromQLError(http.StatusServiceUnavailable, dto.ErrUnavailable,
			fmt.Sprintf("prometheus at %s returned success with a null result", base))
	}
	return &PromQLQueryResponse{PromQLResult: &PromQLResult{
		ResultType: data.ResultType,
		Result:     data.Result,
	}}, nil
}

func upstreamMessage(up upstreamResponse, status string) string {
	if up.Error != "" {
		return up.Error
	}
	return status
}

// promQLTimeArg renders a timestamp the way Prometheus's API takes it: seconds
// since the epoch with millisecond precision. RFC 3339 also works, and the two
// differ only in readability — the numeric form cannot be misread as a local
// time.
func promQLTimeArg(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
}

// promQLStep derives the range step from the window (see the call site for the
// 1100-point and 60s rules).
func promQLStep(start, end time.Time) string {
	const targetPoints = 1100
	const minStep = 60 * time.Second
	span := end.Sub(start)
	step := span / time.Duration(targetPoints)
	if step < minStep {
		step = minStep
	}
	return strconv.FormatInt(int64(step.Seconds()), 10) + "s"
}

// filterSeries keeps the series whose labels match the requested scope.
//
// In catalogue mode this is how `parent` and `resource` mean anything: the
// catalogued PromQL aggregates across the fleet, so the answer is narrowed here
// to the cluster and sub-resource the caller asked about. A series missing a
// label the request names is dropped — a series with no `cluster` label is not
// "the empty cluster", it is a series from before R9.3's contract, and showing
// it in a query about one cluster would attribute someone else's number to it.
func filterSeries(series []PromQLSeries, namespace, cluster string, resource map[string]string) []PromQLSeries {
	if namespace == "" && cluster == "" && len(resource) == 0 {
		return series
	}
	out := make([]PromQLSeries, 0, len(series))
	for _, s := range series {
		if !matches(s.Metric, "namespace", namespace) || !matches(s.Metric, "cluster", cluster) {
			continue
		}
		ok := true
		for k, v := range resource {
			if s.Metric[k] != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

func matches(labels map[string]string, key, want string) bool {
	if want == "" {
		return true
	}
	return labels[key] == want
}

// writePromQLJSON encodes a success body. See the file header for why this is
// not clusters.go's writeJSON.
func writePromQLJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writePromQLError encodes an R8.5 error body. Every error this handler emits
// goes through dto.NewError, so `details` is `[]` and never `null`.
func writePromQLError(w http.ResponseWriter, status int, body dto.ErrorBody) {
	writePromQLJSON(w, status, body)
}
