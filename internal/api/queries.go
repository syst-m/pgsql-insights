// R8.2's query list — R3.2's Queries table over HTTP.
//
//	GET /api/v1/insights/queries?namespace=&cluster=&db=&limit=&sort=&cursor=
//
// The path is not this file's to name. R8.2's catalogue lists it, R10.4 is the
// page that renders it, `internal/api/dto/queries.go:6` publishes the response
// as `Envelope[QueryView]`, and `web/src/routes.ts:182` already carries
// `endpoint: "GET /api/v1/insights/queries"` in A4's route table — so the
// string here matches the consumer that was written against it, which is the
// only way a path constant and a client can't end up spelling one route two
// ways.
//
// # Why this route exists before its store does
//
// R3.2's rows come from the Phase 3 query store, which is not built yet. The
// answer is the one series.go set for the identical problem: an interface
// (QueryListSource), an empty default (emptyQuerySource), and a Config field
// that falls back to it (server.go's `Queries`). That shape is what keeps R14.7
// honest — "every documented route works" cannot be satisfied by a handler that
// waits for data, because the document already lists the path and a path in
// `paths` that 404s is a defect. The route is servable now and A5's store
// replaces the default by assigning one field; nothing in the handler, the
// path, the DTO or the document changes when it does.
//
// The alternative this file does not take is returning an empty page from a
// handler with no source at all and no way to install one: that ships a route
// whose first real implementation is a rewrite, and the rewrite is where the
// contract breaks.
//
// # Paging
//
// R8.10's machinery is shared, not re-authored: the query is `ListQuery` (the
// one parsed-parameter struct every list route uses, aliased here the way
// clusters.go aliases it), and the page is cut by `pageRows` in paging.go. What
// is per-route — as clusters.go's header says it must be — is the *vocabulary*
// of sort keys and the answer to "what string does this row sort on". That is
// two functions and a spec value here, and clusters.go:36's own sentence names
// the default for this route: "a cluster list sorts by phase and pgMajor, a
// query list by total_exec_time".

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// QueriesPath is R8.2's query-list route, named once so server.go's
// registration, this handler, the tests and the document all spell it
// identically — the pattern QuerySeriesPath and ClusterDetailPath set.
const QueriesPath = "/api/v1/insights/queries"

// Sort keys this route accepts (R8.4). Every value is a dto.QueryView json tag,
// which is the rule clusters.go states for its own list ("The JSON-tag spelling
// is the query spelling, so `?sort=pgMajor` matches the field it sorts on") —
// so a sort key here cannot name a field the response does not have.
//
// SortNamespace and SortPGMajor are not repeated: they are declared in
// clusters.go with the values this route needs ("namespace", "pgMajor"), and a
// second const with the same value in the same package is the duplication this
// project's charter exists to prevent.
const (
	SortFingerprint     = "fingerprint"
	SortQuery           = "query"
	SortDatabase        = "database"
	SortCluster         = "cluster"
	SortLoadByTotalTime = "loadByTotalTime"
	SortAvgExecTimeMs   = "avgExecTimeMs"
	SortTotalExecTimeMs = "totalExecTimeMs"
	SortTimesCalled     = "timesCalled"
)

// queryListQuerySpec is this route's R8.4 vocabulary, handed to the shared
// parser. The set is R3.2's sortable columns plus the two scoping fields
// (namespace, cluster) that QueryView carries for the reason its own doc gives:
// an unfiltered request returns rows from every cluster, and a row that does
// not say which cluster it belongs to cannot be rendered, linked, or paged
// stably.
var queryListQuerySpec = QuerySpec{
	Sorts: []string{
		SortFingerprint, SortQuery, SortDatabase, SortNamespace, SortCluster,
		SortLoadByTotalTime, SortAvgExecTimeMs, SortTotalExecTimeMs, SortTimesCalled,
		SortPGMajor,
	},
	// R3.2 ranks the Queries table by *most* expensive first, so the default is
	// totalExecTimeMs descending — the answer clusters.go:36 gave for this route
	// before the route existed ("a cluster list sorts by phase and pgMajor, a
	// query list by total_exec_time"). QuerySpec.Default carries the bare key
	// because parseListQuery requires membership in Sorts, and the direction is
	// DefaultDesc: clusters' default needs no direction (R3.1 orders the fleet
	// by phase ascending) and this one does, which is why the direction is a
	// field on the spec rather than a hardcoded false in the parser.
	Default:     SortTotalExecTimeMs,
	DefaultDesc: true,
}

// QueryListQuery is the parsed query string of GET /api/v1/insights/queries:
// R8.4's parameter set plus R8.10's cursor. An alias of ListQuery for the same
// reason ClusterQuery is one — R8.4 states the parameter set once, and a
// per-route struct would be a second copy of it.
type QueryListQuery = ListQuery

// parseQueryListQuery validates this route's parameters through the shared
// parser. Three lines, and the third is the reason the route keeps a named
// wrapper: the route's vocabulary is passed in here, so a sort key added to the
// const block is accepted by the parser and named in the error message in the
// same edit.
func parseQueryListQuery(q url.Values) (QueryListQuery, error) {
	return parseListQuery(q, queryListQuerySpec)
}

// queryIdentity is a query row's unique key. R8.10 documents the cursor as
// encoding "the sort key plus a secondary (namespace, name) key"; for a query
// the row identity is the cluster's identity plus the fingerprint, because
// R3.4 makes the fingerprint canonical *within* a cluster and the same
// normalised statement runs in two clusters.
func queryIdentity(qv dto.QueryView) string {
	return qv.Namespace + "/" + qv.Cluster + "/" + qv.Fingerprint
}

// sortableFloat renders a float64 as the fixed-width string a lexicographic
// sort orders numerically.
//
// The width is the whole point. Zero-padding to 20 characters yields
// `0000000000099999.100000` and `0000000000100000.200000` — equal length, so
// `99999.1 < 100000.2` compares true as strings. `99999.1` and `100000.2`
// un-padded compare the other way, and a sort that is only sometimes numeric is
// the failure R8.10 names: two consecutive pages that overlap or drop a row.
//
// The bound is 13 integer digits (~8 years of accumulated exec time in
// milliseconds), past which the string grows a character and the comparison
// stops being exact. Exec times in the fixture are 10.7–128410.5 ms; a value
// above the bound is a data bug, and sorting it slightly wrong is a smaller
// consequence than the alternative of a float sort with no defined order.
func sortableFloat(v float64) string { return fmt.Sprintf("%020.6f", v) }

// querySortKey returns the string a query row sorts on for one key, with the
// row identity appended as the tiebreaker — the clusters.go pattern, and for
// the stated reason: without a tiebreaker, sort.Slice's unstable ordering lets
// two rows with equal load swap between pages.
//
// The `query` key sorts on the normalised template text. R3.6 truncates that
// text, so two statements that differ only past the cap sort equal and fall
// back to the fingerprint — which is the correct answer, because R3.4 makes
// them the same query.
func querySortKey(qv dto.QueryView, key string) string {
	primary := qv.Fingerprint
	switch key {
	case SortQuery:
		primary = qv.Query
	case SortDatabase:
		primary = qv.Database
	case SortLoadByTotalTime:
		primary = sortableFloat(qv.LoadByTotalTime)
	case SortAvgExecTimeMs:
		primary = sortableFloat(qv.AvgExecTimeMs)
	case SortTotalExecTimeMs:
		primary = sortableFloat(qv.TotalExecTimeMs)
	case SortTimesCalled:
		primary = fmt.Sprintf("%09d", qv.TimesCalled)
	case SortPGMajor:
		// QueryView's pgMajor is a plain int, not ClusterView's nullable one:
		// R3.2's row is produced by a collector that has already resolved the
		// version to derive the load columns (LoadColumns' per-major rule), so
		// a query row without one cannot exist.
		primary = fmt.Sprintf("%09d", qv.PGMajor)
	case SortNamespace:
		// The identity's first two segments already are namespace and cluster,
		// so appending the identity again would not change the order — the same
		// argument clusters.go makes for its own default key.
		return qv.Namespace + "/" + qv.Cluster + "/" + qv.Fingerprint
	case SortCluster:
		return qv.Cluster + "/" + qv.Fingerprint
	}
	return primary + "|" + queryIdentity(qv)
}

// sortQueries orders query rows by one key and direction. The concrete
// instantiation of the shared sortRowsBy, kept as a named function for the same
// reason clusters.go keeps sortRows: a test can call it directly per key.
func sortQueries(rows []dto.QueryView, key string, desc bool) {
	sortRowsBy(rows, querySortKey, key, desc)
}

// pageQueries pages a set of query rows into this route's body,
// `dto.QueriesResponse` (R3.2's rows, R8.4's `meta`, R3.1's
// `pgStatStatements`). A function rather than a method so the paging is testable
// without an HTTP server, and a thin wrapper over pageRows so this route's page
// is cut by the same code every other list's is: the page comes from pageRows
// and the two keys the generic defines are copied out of it, because
// dto.QueriesResponse is a struct with `data` and `meta` fields rather than an
// embedding of the generic — the shape dto/sessions.go documents for
// ActiveQueriesResponse. That is also why the route's `QueriesEnvelope` alias is
// gone rather than renamed: an alias names a type, and the type this route serves
// is no longer `dto.Envelope[dto.QueryView]`.
//
// The third key is empty here and says so. R3.1's presence rows come from the
// collector (internal/collector/settings.go), not from the query store this route
// reads, and there is no seam between the two yet — the handler hands this
// function the rows and the query and nothing else. An empty array rather than a
// null, because the field has no omitempty and `pgStatStatements: null` would
// tell R10.27's screen something the Go type does not, which is the argument
// pageRows makes for `data` one line up. The rows arrive with a presence source;
// the fixture gained it in ae9cc86, which switched dto/queries_test.go's
// TestQueriesFixtureIsCanonical to this type; its docblock says the same.
func pageQueries(rows []dto.QueryView, q QueryListQuery, now time.Time) dto.QueriesResponse {
	env := pageRows(rows, q, querySortKey, dto.Meta{GeneratedAt: now})

	return dto.QueriesResponse{
		Data: env.Data,
		Meta: env.Meta,
		// Non-nil so a fleet with nothing measured renders `[]`, not `null`.
		PGStatStatements: []dto.ExtensionPresenceView{},
	}
}

// QueryListSource is the read side of the query store as the list route needs
// it: given a parsed query, the rows that match it. Phase 3's store implements
// it; the interface exists now so the route is written, mounted, documented and
// tested before the data is — the move promql.go and series.go made for the same
// reason, because R14.7 forbids documenting a route that 404s and the document
// already lists this one.
//
// The contract on an implementation: return the rows that match q's scope, not
// a page. The handler pages them with the shared pager, which is what makes
// `meta.total` mean R8.4's "matching objects before the page was cut" — a
// source that returned only a page would make total report the page size, and
// that is the D18 ruling this project already settled for the fleet list.
//
// The set is finite by construction rather than by luck: R3.7's store keeps
// top-N per window, so the rows matching a (namespace, cluster, db) scope are
// bounded by the retention policy, not by how many statements a cluster has
// ever run.
type QueryListSource interface {
	QueryList(ctx context.Context, q ListQuery) ([]dto.QueryView, error)
}

// emptyQuerySource is the default: a store that has not been built yet has no
// rows, and says so with an empty page. promql.go and series.go both state the
// honesty argument and it holds here unchanged — an empty answer is the true
// answer for a store that has not filled in, where a 500 would be a lie about
// *why* there is no data.
//
// It returns nil rows, and pageRows turns that into `data: []` with total 0 and
// truncated false: the three fields agree, which is the invariant dto.Meta
// states and the reason `data: []` beside `total: 50` is a bug rather than a
// style choice.
type emptyQuerySource struct{}

// QueryList implements QueryListSource by returning no rows and no error.
func (emptyQuerySource) QueryList(context.Context, ListQuery) ([]dto.QueryView, error) {
	return nil, nil
}

// querySource is cfg.Queries with the default applied, so a Config that sets
// nothing still serves the documented route and New needs no change.
func (s *Server) querySource() QueryListSource {
	if s.cfg.Queries == nil {
		return emptyQuerySource{}
	}
	return s.cfg.Queries
}

// handleQueries is R8.2's query list in the shape clusters.go set for
// /api/v1/clusters, with the same three checks in the same order:
//
//  1. Parameters first — a malformed request is malformed whether or not the
//     store has data, and answering 503 to it tells the client to retry
//     something that will never succeed.
//  2. Readiness second, through the same Readiness /readyz answers from
//     (R8.7(i)): 503 NotReady rather than a 200 whose empty `data` looks like a
//     cluster that has run no queries.
//  3. Then the rows, paged.
func (s *Server) handleQueries(w http.ResponseWriter, r *http.Request) {
	q, err := parseQueryListQuery(r.URL.Query())
	if err != nil {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	rows, err := s.querySource().QueryList(r.Context(), q)
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("queries are not readable: %v", err)))
		return
	}
	env := pageQueries(rows, q, time.Now().UTC())
	s.logDebug("queries served",
		"route", QueriesPath,
		"namespace", q.Namespace,
		"cluster", q.Cluster,
		"sort", q.Sort,
		"rows", len(env.Data),
		"total", env.Meta.Total,
	)
	writeJSON(w, http.StatusOK, env)
}
