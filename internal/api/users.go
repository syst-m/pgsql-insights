// GET /api/v1/insights/users — R3.7's by-user rollup.
//
// # Which requirement asks for this
//
// requirements.md:314 (R3.7) names four per-dimension rollups and requirements.md:380
// (R8.2) names the route that serves this one, `/insights/users`. Until this commit the
// path's only home was api/x-planned-paths.yaml:148, under `r8.2-catalogue:`, with the
// comment `# R3.7, R10.3 (was: R10.9)`; this commit deletes that line and the route
// appears under `paths` in api/openapi.yaml (the delete-rather-than-mark rule,
// api/x-planned-paths.yaml:120-124).
//
// The row type is internal/api/dto/dimensions.go:93's UserView. The shared reasoning —
// why the DTO is one file for three routes, why the api side is one file per route,
// what the route's job is relative to the DTO, and the single-field-identity caveat
// that keyset paging inherits from it — is written once in databases.go's header
// (databases.go:1-51) and not restated here; this header covers only what differs.
//
// # The two spellings of "user", and why this route needs a new sort const
//
// requirements.md:314 now carries A3's ruling on the field-name divergence I raised
// from the dto commit (`A3's S153`): two vocabularies decide a field's spelling by what
// it names — a tag dimension takes the name with `_name` dropped (`user`, at
// dto/queries.go:631-633), and a field naming a column read from `pg_stat_activity` or
// `pg_roles` takes the column's camelCase form (`userName`, at dto/sessions.go:103 and
// dto/dimensions.go:95). Both are correct inside their own vocabulary, so neither is
// renamed.
//
// That ruling is why this file declares `SortUserName` rather than reusing tags.go's
// `SortUser` (tags.go:123, `user`). The two are different strings answering two
// different questions — `?sort=user` on /insights/tags sorts a TagView's tag dimension,
// `?sort=userName` on this route sorts the role name — and one const holding two values
// is impossible, so reusing it would mean either changing TagView's wire spelling (a
// breaking change to web/src/api/types.ts, which the ruling forbids) or sorting this
// route on the wrong field. The reverse case is why queries.go declares `SortDatabase`
// and tags.go re-declares nothing: `database` is the one spelling both vocabularies
// print (requirements.md:314), so one const serves both routes.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// UsersPath is R8.2's users route, named once so server.go's registration, this
// handler, users_test.go, api/openapi.yaml and web/src/routes.ts all spell it
// identically (the DatabasesPath pattern, databases.go:64-68).
const UsersPath = "/api/v1/insights/users"

// UsersEnvelope is the response body of GET /api/v1/insights/users: an alias over
// R8.4's one generic, named so the document has something to `$ref` without creating a
// second envelope (TagsEnvelope's reason, tags.go).
type UsersEnvelope = dto.Envelope[dto.UserView]

// SortUserName is this route's dimension sort key, spelled `userName` for the reason in
// the header: it is the pg_stat_activity/pg_roles column form the R3.7 ruling assigns to
// this DTO, not TagView's tag-dimension form.
const SortUserName = "userName"

// usersListQuerySpec is this route's sort vocabulary: the dimension key plus the eight
// embedded load columns and `lockWaitMs` that R3.7 requires of every rollup — ten
// keys for ten json tags, all of which already exist as consts in queries.go and
// tags.go (the list is databases.go:79-85's, minus SortDatabase). The default is
// `loadByTotalTime` descending for the reason databases.go and tags.go give for the
// same choice: R3.7 defines a rollup as the answer to "which X accounts for the most
// load".
var usersListQuerySpec = QuerySpec{
	Sorts: []string{
		SortUserName, SortLoadByTotalTime, SortPctLoadByQueries, SortAvgExecTimeMs,
		SortTotalExecTimeMs, SortTimesCalled, SortAvgRowsReturned, SortLoadByCPUMs,
		SortLoadByIOWaitMs, SortLockWaitMs,
	},
	Default:     SortLoadByTotalTime,
	DefaultDesc: true,
}

// UsersQuery is the parsed query string of GET /api/v1/insights/users: R8.4's parameter
// set plus R8.10's cursor, aliased to ListQuery because R8.4 states it once for every
// list route.
type UsersQuery = ListQuery

// parseUsersQuery validates this route's parameters through the shared parser. No
// parameter is required; the route answers fleet-wide when `namespace` and `cluster`
// are absent, which is what makes the cross-cluster identity caveat in databases.go's
// header reachable at all.
func parseUsersQuery(q url.Values) (UsersQuery, error) {
	return parseListQuery(q, usersListQuerySpec)
}

// userIdentity is a row's unique key: `userName` alone, because dto.UserView carries no
// namespace or cluster fields. See databases.go:36-51 for what that costs and when it
// is paid.
func userIdentity(v dto.UserView) string { return v.UserName }

// userSortKey returns the string a row sorts on for one key, with the identity appended
// as the tiebreaker. Floats go through the shared sortableFloat and the one integral
// column through the same `%09d` rendering queries.go uses, for the reason it gives.
//
// Every key in usersListQuerySpec has a case here — the property users_test.go proves
// by table, because a key the parser accepts but the switch omits compiles fine and
// silently sorts on the identity alone.
func userSortKey(v dto.UserView, key string) string {
	primary := v.UserName
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
	return primary + "|" + userIdentity(v)
}

// sortUsers orders rows by one key and direction — the named instantiation of the
// shared sortRowsBy, kept so a test can call it directly per key.
func sortUsers(rows []dto.UserView, key string, desc bool) {
	sortRowsBy(rows, userSortKey, key, desc)
}

// pageUsers pages a set of rows into R8.4's envelope.
func pageUsers(rows []dto.UserView, q UsersQuery, now time.Time) UsersEnvelope {
	return pageRows(rows, q, userSortKey, dto.Meta{GeneratedAt: now})
}

// UsersSource is the read side of the by-user rollup as this route needs it: given a
// parsed query, every role with rows in its scope and window. The contract on an
// implementation is the one QueryList states and rollups.go follows — return the rows
// that match q's scope, not a page, because the handler pages them and that is what
// makes `meta.total` mean R8.4's "matching objects before the page was cut".
//
// The store method this names does not exist yet, and the `var _ api.UsersSource =
// (*Store)(nil)` assertion belongs on the implementing side when it lands, for the
// import-cycle reason tags.go:22-38 gives and databases.go repeats.
type UsersSource interface {
	Users(ctx context.Context, q ListQuery) ([]dto.UserView, error)
}

// emptyUsersSource is the default, and it returns nil rows: pageRows turns that into
// `data: []` with total 0 and truncated false — the true answer for a process running
// without a store, which a 500 would falsify by implying the data is unreadable rather
// than absent.
type emptyUsersSource struct{}

// Users implements UsersSource by reporting no rows for any scope; the scope is not
// validated here for the reason emptyTagsSource gives.
func (emptyUsersSource) Users(context.Context, ListQuery) ([]dto.UserView, error) {
	return nil, nil
}

// usersSource is cfg.Users with the default applied, so a Config that sets nothing still
// serves the documented route and New needs no change. The field is read at request
// time, so assigning a store after New takes effect without rebuilding the server; the
// shared typed-nil caveat (2b63f1e) applies in its usual form.
func (s *Server) usersSource() UsersSource {
	if s.cfg.Users == nil {
		return emptyUsersSource{}
	}
	return s.cfg.Users
}

// handleUsers is R3.7's by-user route, with the same three checks in the same order
// every list route uses: parameters, then readiness, then the rows paged. The ordering
// argument is databases.go:215-220's and is not restated.
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	q, err := parseUsersQuery(r.URL.Query())
	if err != nil {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	rows, err := s.usersSource().Users(r.Context(), q)
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("users are not readable: %v", err)))
		return
	}
	env := pageUsers(rows, q, time.Now().UTC())
	s.logDebug("users served",
		"route", UsersPath,
		"namespace", q.Namespace,
		"cluster", q.Cluster,
		"sort", q.Sort,
		"rows", len(env.Data),
		"total", env.Meta.Total,
	)
	writeJSON(w, http.StatusOK, env)
}
