// GET /api/v1/insights/client-addresses — R3.7's by-client-address rollup.
//
// # Which requirements ask for this
//
// Two rows name this route, and their comments say so in a different order.
// requirements.md:314 (R3.7) names the fourth per-dimension rollup — "by client
// address" — and requirements.md:380 (R8.2) lists `/insights/client-addresses` among
// the core endpoints; requirements.md:416 (R10.3) names the Client Addresses page and
// says the four rollup pages were missing from its list in 0.1.0.
//
// R6.4 is the third id on the manifest line, and it is the row that scopes this
// dimension's correctness. requirements.md:359 (R6.4) is an "Attribution fix, scoped":
// Cloud SQL's own answer for pgbouncer is to disable client-IP attribution rather than
// mis-attribute it (IPs render as `local`), and under transaction pooling a
// `pg_stat_statements` row has no session at all, so per-query client attribution is
// impossible and the spec declines to claim it. It then applies attribution to two
// places, lettered (a) and (b): the R4.1 active-session list via `SHOW CLIENTS`, and
// "the **R3.7 by-client-address rollup**, which Cloud SQL itself models as a separate
// dimension". This route is (b).
//
// What that costs this handler is nothing and it is worth saying why: the row's caveats
// are the store's, not the route's. Whether a row's address is a real IP, the literal
// `local`, or absent at all is decided by `--pooler-attribution=off|cloudsql|pgbouncer`
// (requirements.md:359, default `pgbouncer`) when the rows are produced, and R3.2's
// per-query rows carry the pooler name as their connection route instead of an address.
// A handler that tried to re-derive attribution from the query rows would be a second
// implementation of R6.4 with no access to the config that governs it.
//
// Until this commit the path's only home was api/x-planned-paths.yaml:149, under
// `r8.2-catalogue:`, with the comment `# R3.7, R6.4, R10.3 (was: R6, R10.12)`; this
// commit deletes that line and the route appears under `paths` in api/openapi.yaml (the
// delete-rather-than-mark rule, api/x-planned-paths.yaml:120-124).
//
// The row type is internal/api/dto/dimensions.go:130's ClientAddressView. The shared
// reasoning — one file per route on the api side, the route's job relative to the DTO,
// and the single-field-identity caveat keyset paging inherits from a DTO with no
// namespace or cluster fields — is written once in databases.go's header
// (databases.go:1-51) and not restated here.
//
// # Why this route needs a new sort const, unlike databases.go
//
// requirements.md:314's ruling (`A3's S153`) assigns field spellings by what the field
// names, and `clientAddress` is a column-derived name (the `client_address` column of
// `pg_stat_activity`, camelCased) — the same vocabulary that gives UserView `userName`
// (dto/dimensions.go:95) and SessionView its own spelling (dto/sessions.go:101-103).
// No existing route sorts on it: TagView's dimension set (dto/queries.go:631-633) has
// no address member, so there is no const to reuse and no risk of forking one. The
// string is `clientAddress`, matching the json tag at dto/dimensions.go:133 exactly —
// the rule every route follows, that a sort key is the field's wire spelling and not a
// second name for it.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// ClientAddressPath is R8.2's client-addresses route, named once so server.go's
// registration, this handler, client_addresses_test.go, api/openapi.yaml and
// web/src/routes.ts all spell it identically (the DatabasesPath pattern,
// databases.go:64-68). The const is singular because the Go identifier names the
// dimension, not the route: the route is `/insights/client-addresses` and the DTO is
// ClientAddressView, and a `ClientAddressesPath` would be the only path const in the
// package whose name disagrees with the type it serves.
const ClientAddressPath = "/api/v1/insights/client-addresses"

// ClientAddressesEnvelope is the response body of GET /api/v1/insights/client-addresses:
// an alias over R8.4's one generic, named so the document has something to `$ref`
// without creating a second envelope (TagsEnvelope's reason, tags.go).
type ClientAddressesEnvelope = dto.Envelope[dto.ClientAddressView]

// SortClientAddress is this route's dimension sort key, spelled exactly as the json tag
// at dto/dimensions.go:133 spells it, for the reason in the header.
const SortClientAddress = "clientAddress"

// clientAddressesListQuerySpec is this route's sort vocabulary: the dimension key plus
// the eight embedded load columns and `lockWaitMs` that R3.7 requires of every rollup —
// ten keys for ten json tags, all of which already exist as consts in queries.go and
// tags.go (the list is databases.go:79-85's, with SortClientAddress where it has
// SortDatabase). The default is `loadByTotalTime` descending for the reason
// databases.go and tags.go give for the same choice.
var clientAddressesListQuerySpec = QuerySpec{
	Sorts: []string{
		SortClientAddress, SortLoadByTotalTime, SortPctLoadByQueries, SortAvgExecTimeMs,
		SortTotalExecTimeMs, SortTimesCalled, SortAvgRowsReturned, SortLoadByCPUMs,
		SortLoadByIOWaitMs, SortLockWaitMs,
	},
	Default:     SortLoadByTotalTime,
	DefaultDesc: true,
}

// ClientAddressesQuery is the parsed query string of GET /api/v1/insights/client-addresses:
// R8.4's parameter set plus R8.10's cursor, aliased to ListQuery because R8.4 states it
// once for every list route.
type ClientAddressesQuery = ListQuery

// parseClientAddressesQuery validates this route's parameters through the shared parser.
// No parameter is required; the route answers fleet-wide when `namespace` and `cluster`
// are absent.
func parseClientAddressesQuery(q url.Values) (ClientAddressesQuery, error) {
	return parseListQuery(q, clientAddressesListQuerySpec)
}

// clientAddressIdentity is a row's unique key: `clientAddress` alone, because
// dto.ClientAddressView carries no namespace or cluster fields. See databases.go:36-51
// for what that costs and when it is paid — for this dimension the cost is lower than
// for the other two, since one address is one client host and the same address appearing
// under two clusters is the common case rather than an artefact (a jump host queried by
// two clusters is one address with two sets of load rows).
func clientAddressIdentity(v dto.ClientAddressView) string { return v.ClientAddress }

// clientAddressSortKey returns the string a row sorts on for one key, with the identity
// appended as the tiebreaker. Floats go through the shared sortableFloat and the one
// integral column through the same `%09d` rendering queries.go uses, for the reason it
// gives.
//
// Every key in clientAddressesListQuerySpec has a case here — the property
// client_addresses_test.go proves by table, because a key the parser accepts but the
// switch omits compiles fine and silently sorts on the identity alone.
func clientAddressSortKey(v dto.ClientAddressView, key string) string {
	primary := v.ClientAddress
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
	return primary + "|" + clientAddressIdentity(v)
}

// sortClientAddresses orders rows by one key and direction — the named instantiation of
// the shared sortRowsBy, kept so a test can call it directly per key.
func sortClientAddresses(rows []dto.ClientAddressView, key string, desc bool) {
	sortRowsBy(rows, clientAddressSortKey, key, desc)
}

// pageClientAddresses pages a set of rows into R8.4's envelope.
func pageClientAddresses(rows []dto.ClientAddressView, q ClientAddressesQuery, now time.Time) ClientAddressesEnvelope {
	return pageRows(rows, q, clientAddressSortKey, dto.Meta{GeneratedAt: now})
}

// ClientAddressesSource is the read side of the by-client-address rollup as this route
// needs it: given a parsed query, every address with rows in its scope and window. The
// contract on an implementation is the one QueryList states and rollups.go follows —
// return the rows that match q's scope, not a page, because the handler pages them and
// that is what makes `meta.total` mean R8.4's "matching objects before the page was cut".
//
// The store method this names does not exist yet, and the `var _
// api.ClientAddressesSource = (*Store)(nil)` assertion belongs on the implementing side
// when it lands, for the import-cycle reason tags.go:22-38 gives and databases.go repeats.
type ClientAddressesSource interface {
	ClientAddresses(ctx context.Context, q ListQuery) ([]dto.ClientAddressView, error)
}

// emptyClientAddressesSource is the default, and it returns nil rows: pageRows turns
// that into `data: []` with total 0 and truncated false — the true answer for a process
// running without a store, which a 500 would falsify by implying the data is unreadable
// rather than absent.
type emptyClientAddressesSource struct{}

// ClientAddresses implements ClientAddressesSource by reporting no rows for any scope;
// the scope is not validated here for the reason emptyTagsSource gives.
func (emptyClientAddressesSource) ClientAddresses(context.Context, ListQuery) ([]dto.ClientAddressView, error) {
	return nil, nil
}

// clientAddressesSource is cfg.ClientAddresses with the default applied, so a Config
// that sets nothing still serves the documented route and New needs no change. The field
// is read at request time, so assigning a store after New takes effect without rebuilding
// the server; the shared typed-nil caveat (2b63f1e) applies in its usual form.
func (s *Server) clientAddressesSource() ClientAddressesSource {
	if s.cfg.ClientAddresses == nil {
		return emptyClientAddressesSource{}
	}
	return s.cfg.ClientAddresses
}

// handleClientAddresses is R3.7's by-client-address route, with the same three checks in
// the same order every list route uses: parameters, then readiness, then the rows paged.
// The ordering argument is databases.go:215-220's and is not restated.
func (s *Server) handleClientAddresses(w http.ResponseWriter, r *http.Request) {
	q, err := parseClientAddressesQuery(r.URL.Query())
	if err != nil {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	rows, err := s.clientAddressesSource().ClientAddresses(r.Context(), q)
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("client addresses are not readable: %v", err)))
		return
	}
	env := pageClientAddresses(rows, q, time.Now().UTC())
	s.logDebug("client addresses served",
		"route", ClientAddressPath,
		"namespace", q.Namespace,
		"cluster", q.Cluster,
		"sort", q.Sort,
		"rows", len(env.Data),
		"total", env.Meta.Total,
	)
	writeJSON(w, http.StatusOK, env)
}
