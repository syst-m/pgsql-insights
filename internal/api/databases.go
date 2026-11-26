// GET /api/v1/insights/databases — R3.7's by-database rollup.
//
// # Which requirement asks for this
//
// requirements.md:314 (R3.7) names four per-dimension rollups — "by database, by
// user, by application name, by client address" — and requirements.md:380 (R8.2)
// names the route that serves this one. Until this commit the path's only home was
// api/x-planned-paths.yaml:147, under `r8.2-catalogue:`, carrying the comment
// `# R3.7, R10.3 (was: R10.8)`; this commit deletes that line and the route appears
// under `paths` in api/openapi.yaml, which is the delete-rather-than-mark rule every
// earlier promotion states — the rule api/x-planned-paths.yaml:120-124 writes for
// itself and internal/api/poolers.go:19-22 quotes.
//
// The row type is internal/api/dto/dimensions.go:61's DatabaseView: the dimension
// key, the embedded LoadColumns (dto/queries.go:86), and LockWaitMs, and nothing
// else. This file adds no field to it. R3.7's sentence asks "which database accounts
// for the most load" and the DTO answers it with R3.2's columns, so the route's whole
// job is R8.4's envelope around rows the DTO already shapes — the same job tags.go
// took for the fourth rollup, and for the same stated reason ("this file's job was to
// accept a signature, not to design one", internal/api/tags.go:19-20).
//
// # Why this file is named databases.go
//
// A1's dto commit named its file `dimensions.go` rather than `rollups.go` because a
// second file with the store's basename would turn four existing cites of the form
// `rollups.go:445` ambiguous — the parity matrix resolves a bare basename by unique
// suffix and fails on 2+ matches (scripts/check-parity-matrix.py:409-414, the reason
// spelled out at dto/dimensions.go:39-46). That constraint does not bind on this side:
// internal/api has no rollups.go, and the package's convention is one file per route
// (tags.go, poolers.go, series.go, query_detail.go), so each of R3.7's three
// remaining routes gets its own file here even though their DTOs share one file — the
// dto groups by resource because one struct set describes all three, and the api
// groups by route because each route has its own source, its own sort vocabulary and
// its own annotation.
//
// # Paging, and the identity caveat this DTO shares with TagView
//
// pageDatabases is a wrapper over R8.10's pageRows, and the identity used as every
// sort's tiebreaker is `database` alone, because dto.DatabaseView carries no
// namespace or cluster fields — the route scopes by query parameter instead of
// returning the scope in the body, the way every R8.2 route does. That is true of
// this DTO and false of the generalisation, exactly as tags.go:60-72 says of TagView:
// QueryView carries both fields and its pager uses them. The consequence is the same
// narrow one tags.go names rather than papers over — the store groups by
// (dim_hash, namespace, cluster), so `openwebui` in two clusters is two rows whose
// only on-wire distinction is their load numbers, and a fleet-wide request whose two
// rows share every sorted value yields one key, which keyset paging can place on at
// most one page. Every cluster-scoped request — which is what web/src/routes.ts's
// `scope: "cluster"` pages ask for — has exactly one row per database and cannot hit
// it. Fixing the general case means two fields on the DTO, which moves the fixture,
// web/src/api/types.ts and the document: A1's call, not a line in this file.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// DatabasesPath is R8.2's databases route, named once so server.go's registration,
// this handler, databases_test.go, api/openapi.yaml and web/src/routes.ts all spell it
// identically — the pattern TagsPath, PoolersPath and QueriesPath set for their
// routes, and the reason R14.7's pairing is checkable rather than asserted.
const DatabasesPath = "/api/v1/insights/databases"

// DatabasesEnvelope is the response body of GET /api/v1/insights/databases: an alias
// over R8.4's one generic, named for the reason TagsEnvelope is one — the name exists
// so the document has something to `$ref`, and an alias creates no second envelope for
// two names to disagree about.
type DatabasesEnvelope = dto.Envelope[dto.DatabaseView]

// SortUserName and SortClientAddress are the two sort keys R3.7's other two routes
// need and no existing file declares; they are declared in their own files
// (users.go:62, client_addresses.go:79), not here, because a sort key belongs with the
// route whose vocabulary contains it. The ten keys this route needs all exist
// already: SortDatabase, SortLoadByTotalTime, SortAvgExecTimeMs,
// SortTotalExecTimeMs and SortTimesCalled come from queries.go:70-75, and
// SortPctLoadByQueries, SortAvgRowsReturned, SortLoadByCPUMs, SortLoadByIOWaitMs and
// SortLockWaitMs from tags.go:129-133. Re-declaring any of them here would be a
// second answer to "what does ?sort=avgExecTimeMs mean" — the duplication
// tags.go:112-116 names as the reason it imports rather than repeats.
var databasesListQuerySpec = QuerySpec{
	Sorts: []string{
		SortDatabase, SortLoadByTotalTime, SortPctLoadByQueries, SortAvgExecTimeMs,
		SortTotalExecTimeMs, SortTimesCalled, SortAvgRowsReturned, SortLoadByCPUMs,
		SortLoadByIOWaitMs, SortLockWaitMs,
	},
	// The default is `loadByTotalTime` descending for the reason tags.go gives for
	// the same choice: R3.7 defines a rollup as the answer to "which X accounts for
	// the most load", and a page whose first row was the cheapest database answers
	// that question backwards.
	Default:     SortLoadByTotalTime,
	DefaultDesc: true,
}

// DatabasesQuery is the parsed query string of GET /api/v1/insights/databases: R8.4's
// parameter set plus R8.10's cursor, aliased to ListQuery because R8.4 states the
// parameter set once for every list route.
type DatabasesQuery = ListQuery

// parseDatabasesQuery validates this route's parameters through the shared parser.
// No parameter is required: the route answers fleet-wide (every database across every
// cluster) when `namespace` and `cluster` are absent, which is what makes the
// cross-cluster duplicate case in the header visible at all.
func parseDatabasesQuery(q url.Values) (DatabasesQuery, error) {
	return parseListQuery(q, databasesListQuerySpec)
}

// databaseIdentity is a row's unique key — see the header for why it is one field
// here where queryIdentity composes three.
func databaseIdentity(v dto.DatabaseView) string { return v.Database }

// databaseSortKey returns the string a row sorts on for one key, with the identity
// appended as the tiebreaker. The float columns go through sortableFloat
// (queries.go's, not a copy) and the one integral column through the same %09d
// rendering it uses, for the reason it gives: a lexicographic sort orders `99999.1`
// after `100000.2` unless both are padded to equal width.
//
// Every key in databasesListQuerySpec has a case here, which is the property
// tags_test.go's TestTagsSortKeys proves by table for its own route: a key the parser
// accepts but the switch omits does not fail to compile, it sorts on the identity
// alone and returns a page ordered by name.
func databaseSortKey(v dto.DatabaseView, key string) string {
	primary := v.Database
	switch key {
	case SortLoadByTotalTime:
		primary = sortableFloat(v.LoadByTotalTime)
	case SortPctLoadByQueries:
		primary = sortableFloat(v.PctLoadByQueries)
	case SortAvgExecTimeMs:
		primary = sortableFloat(v.AvgExecTimeMs)
	case SortTotalExecTimeMs:
		primary = sortableFloat(v.TotalExecTimeMs)
	case SortTimesCalled:
		primary = fmt.Sprintf("%09d", v.TimesCalled)
	case SortAvgRowsReturned:
		primary = sortableFloat(v.AvgRowsReturned)
	case SortLoadByCPUMs:
		primary = sortableFloat(v.LoadByCPUMs)
	case SortLoadByIOWaitMs:
		primary = sortableFloat(v.LoadByIOWaitMs)
	case SortLockWaitMs:
		primary = sortableFloat(v.LockWaitMs)
	}
	return primary + "|" + databaseIdentity(v)
}

// sortDatabases orders rows by one key and direction — the named instantiation of the
// shared sortRowsBy, kept so a test can call it directly per key.
func sortDatabases(rows []dto.DatabaseView, key string, desc bool) {
	sortRowsBy(rows, databaseSortKey, key, desc)
}

// pageDatabases pages a set of rows into R8.4's envelope.
func pageDatabases(rows []dto.DatabaseView, q DatabasesQuery, now time.Time) DatabasesEnvelope {
	return pageRows(rows, q, databaseSortKey, dto.Meta{GeneratedAt: now})
}

// DatabasesSource is the read side of the by-database rollup as this route needs it:
// given a parsed query, every database with rows in its scope and window.
//
// The contract on an implementation is the one QueryList states and rollups.go
// follows: return the rows that match q's scope, not a page, because the handler pages
// them and that is what makes `meta.total` mean R8.4's "matching objects before the
// page was cut". A source that honoured `q.Limit` would be wrong here in a way the
// compiler cannot catch and the shared pager can.
//
// The store method this interface names does not exist yet: internal/store exposes
// Tags (rollups.go:490) and QueryList (topn.go:229) but no Databases, and
// dto/dimensions.go:24-31 explains why the rollup rows exist in
// store.DimensionRollup without a per-dimension accessor. The interface is what lets
// that method land without a handler change — the arrangement tags.go:22-38 describes
// for its own route, where the store's docblock predicted the signature and the
// `var _ api.TagsSource = (*Store)(nil)` assertion went in on the implementing side
// because internal/store imports internal/api and an api-side test cannot name
// *store.Store without a cycle. The same assertion for this interface belongs in
// internal/store when the method lands, not here.
type DatabasesSource interface {
	Databases(ctx context.Context, q ListQuery) ([]dto.DatabaseView, error)
}

// emptyDatabasesSource is the default, and it returns nil rows: pageRows turns that
// into `data: []` with total 0 and truncated false. An empty answer is the true answer
// for a process running without a store — the argument every list route's empty source
// makes, and the one a 500 would falsify by implying the data is unreadable rather
// than absent.
type emptyDatabasesSource struct{}

// Databases implements DatabasesSource by reporting no rows for any scope. The scope
// is not validated against a fleet here for the reason emptyTagsSource gives: a
// default that knows nothing about the fleet cannot distinguish "no such cluster" from
// "no databases", and guessing in a place that has no way to check is more expensive
// than not guessing.
func (emptyDatabasesSource) Databases(context.Context, ListQuery) ([]dto.DatabaseView, error) {
	return nil, nil
}

// databasesSource is cfg.Databases with the default applied, so a Config that sets
// nothing still serves the documented route and New needs no change. The field is read
// at request time, so assigning a store after New takes effect without rebuilding the
// server; the shared typed-nil caveat (2b63f1e) applies in its usual form —
// `cfg.Databases = (*store.Store)(nil)` is not == nil, so the accessor hands back the
// typed nil and the implementation must answer on a nil receiver.
func (s *Server) databasesSource() DatabasesSource {
	if s.cfg.Databases == nil {
		return emptyDatabasesSource{}
	}
	return s.cfg.Databases
}

// handleDatabases is R3.7's by-database route, with the same three checks in the same
// order every list route uses: parameters first (a malformed request is malformed
// whether or not the store has data, and answering 503 to it tells the client to retry
// something that will never succeed), readiness second (the same Readiness /readyz
// answers, so a server that has not synced cannot answer "no databases" for a fleet it
// has not read), then the rows, paged.
func (s *Server) handleDatabases(w http.ResponseWriter, r *http.Request) {
	q, err := parseDatabasesQuery(r.URL.Query())
	if err != nil {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	rows, err := s.databasesSource().Databases(r.Context(), q)
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("databases are not readable: %v", err)))
		return
	}
	env := pageDatabases(rows, q, time.Now().UTC())
	s.logDebug("databases served",
		"route", DatabasesPath,
		"namespace", q.Namespace,
		"cluster", q.Cluster,
		"sort", q.Sort,
		"rows", len(env.Data),
		"total", env.Meta.Total,
	)
	writeJSON(w, http.StatusOK, env)
}
