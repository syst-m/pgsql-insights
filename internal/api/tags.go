// R3.12's tags — GET /api/v1/insights/tags.
//
// The route R8.2 lists, the tab R10.3's rollup group renders, and the HTTP face of
// the tag definition dto/queries.go already carries. Its three artifacts existed in
// the tree before this file did, and none of the three was written for this commit:
//
//   - dto/queries.go's TagView and TagDigest, with dto/queries_test.go:544's
//     TestTagViewCarriesR312sDimensions checking the field list against R3.12's own
//     wording (the (a) trio — application_name, usename, datname — plus the (b)
//     five the `/* pgsi:… */` grammar names, hashed to `tag_hash`);
//   - testdata/api/tags.json, the R16.8 fixture, which web/src/api/fixtures.ts:115
//     already imports and annotates `Envelope<TagView>` — the TS side has had a
//     contract for this route longer than the Go side has had a handler;
//   - internal/store/rollups.go:490's `Tags(ctx, api.ListQuery) ([]dto.TagView,
//     error)`, whose header predicted this route's signature: "Envelope[TagView]
//     means the handler pages it, and every paged list route takes the shared
//     api.ListQuery — so Tags takes that and returns []dto.TagView. When A2 writes
//     the route, the assertion goes in with it; if the signature that ships
//     differs, this file changes by one line and the rows it reads do not change at
//     all."
//
// So this file's job was to accept a signature, not to design one. The interface
// below is the one that docblock names, and the `var _ api.TagsSource =
// (*Store)(nil)` it promised goes into rollups.go in this commit — one line, which
// is what turns "the store implements the route's interface" from a sentence in a
// comment into a compile error the day it stops being true. That is the only reason
// the assertion exists, and it is the reason it cannot live here: internal/store
// imports internal/api, so an api-side test cannot name *store.Store without a cycle
// and the assertion has to sit on the implementing side of the boundary.
//
// # Why an interface when the store is already built
//
// queries.go's header answers this for a store that does not exist yet; the answer
// here is different and worth stating, because the usual reason no longer applies.
// The reason now is cmd/pgsi, and the two sides of the claim are in one sentence:
// main.go's entire wiring is the composite literal at :372 (Listen, ShutdownTimeout,
// ServeUI, Auth, Log) plus six assignments — CORS :381, PromQL :393, Fleet :441,
// Scheduler :504, Metrics :505, StoreSize :514 — and none of the six source fields
// this package declares (Series :143, Queries :150, QueryDetail :162,
// QueryPlans :183, ActiveQueries :205, Tags :223) is among them. The one
// store-shaped thing it wires is `collector.NewMemSink(0)` (:491) into StoreSize,
// which is the collector's sample buffer, not the query store. So every query-store
// route, this one included, answers from its empty default in the binary today. The
// interface is what lets main.go start assigning them one at a time, in whatever
// order A5's store lands, without a handler change; and it is what lets tags_test.go
// hand the handler rows without a SQLite file.
//
// # Paging, and the one place this route's row identity is weaker than its siblings'
//
// R8.10's shared pager does the work (pageTags is a wrapper over pageRows), and the
// per-route answer is the row's sort key plus its identity as the tiebreaker — the
// clusters.go and queries.go pattern, whose stated purpose is that without a
// tiebreaker two equal-ranked rows swap between pages.
//
// For a query the identity is `namespace/cluster/fingerprint` (queries.go's
// queryIdentity). For a tag it can only be `tagHash`, because dto.TagView has no
// namespace or cluster fields: rollups.go:445 says so outright — "dto.TagView has no
// namespace/cluster fields — the tag routes scope by query parameter instead of
// returning the scope in the body, the way every R8.2 route does". That clause is
// true of this DTO and false of the generalisation: QueryView *does* carry both, and
// its pager uses them. The consequence is narrow and worth naming rather than
// papering over — the store groups by (dim_hash, namespace, cluster), so one tag
// running in two clusters is two rows whose only on-wette distinction is their load
// numbers, and a fleet-wide request whose two rows happen to share every sorted
// value yields one key, which keyset paging can place on at most one page. Every
// cluster-scoped request (which is what web/src/routes.ts:263's `scope: "cluster"`
// and its `/c/:ns/:name/tags` path ask for) has exactly one row per tagHash and
// cannot hit it. Fixing the general case means two fields on TagView, which moves the
// fixture, types.ts and the document — a dto change and A1's call, not a line in this
// file.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// TagsPath is R8.2's tags route, named once so server.go's registration, this
// handler, the tests, api/openapi.yaml and web/src/routes.ts:260 all spell it
// identically — the pattern ActiveQueriesPath, QueriesPath, QueryDetailPath,
// QuerySeriesPath and QueryPlansPath set for their routes.
//
// The string is not chosen here: R8.2's catalogue lists it, dto/queries.go:9's
// route table publishes `GET /api/v1/insights/tags → Envelope[TagView] (R3.12)`,
// and A4's route table has carried `endpoint: "GET /api/v1/insights/tags"` since
// the page list was written.
const TagsPath = "/api/v1/insights/tags"

// TagsEnvelope is the response body of GET /api/v1/insights/tags: an alias over
// R8.4's one generic, named for the same reason and with the same caveat as
// QuerySeriesEnvelope and QueryPlansEnvelope — the name exists so the document
// has something to `$ref`, and an alias creates no second envelope for two names
// to disagree about. The queries route is no longer in that list: its body is a
// named struct (dto.QueriesResponse, R3.2 + R3.1) and a named struct needs no
// alias to be `$ref`-able.
type TagsEnvelope = dto.Envelope[dto.TagView]

// Sort keys this route accepts (R8.4), every one of them a TagView json tag — the
// rule queries.go states for its own list ("the JSON-tag spelling is the query
// spelling… so a sort key here cannot name a field the response does not have").
//
// Only the keys the query list does not already declare are consted here.
// SortDatabase, SortLoadByTotalTime, SortAvgExecTimeMs, SortTotalExecTimeMs and
// SortTimesCalled come from queries.go, and re-declaring a same-package const with
// the same value is the duplication this project's charter exists to prevent — the
// same reason queries.go imports rather than repeats SortNamespace and SortPGMajor.
//
// The two files together spell all eighteen of TagView's json tags: thirteen here,
// five there. queries.go curates (its list omits four of QueryView's fields), and
// this route is exhaustive instead for a reason specific to a switch: a key the
// parser accepts but tagSortKey has no case for does not fail, it sorts on the
// identity alone and returns a page ordered by hash. Making the set exactly the field
// list is what makes "every accepted key has a case" checkable by counting, which is
// what tags_test.go does.
const (
	SortTagHash          = "tagHash"
	SortApplication      = "application"
	SortUser             = "user"
	SortAction           = "action"
	SortRoute            = "route"
	SortController       = "controller"
	SortFramework        = "framework"
	SortDBDriver         = "dbDriver"
	SortPctLoadByQueries = "pctLoadByQueries"
	SortAvgRowsReturned  = "avgRowsReturned"
	SortLoadByCPUMs      = "loadByCPUMs"
	SortLoadByIOWaitMs   = "loadByIOWaitMs"
	SortLockWaitMs       = "lockWaitMs"
)

// tagsListQuerySpec is this route's R8.4 vocabulary, handed to the shared parser.
//
// The default is `loadByTotalTime` descending because R3.7 defines what a rollup is
// *for*: "Each rollup answers 'which X accounts for the most load' with the same
// load columns as R3.2". A rollup page whose first row was the cheapest dimension
// would answer the question backwards, which is why DefaultDesc is true here for the
// same reason queries.go sets it for its own list.
var tagsListQuerySpec = QuerySpec{
	Sorts: []string{
		SortLoadByTotalTime, SortPctLoadByQueries, SortAvgExecTimeMs, SortTotalExecTimeMs,
		SortTimesCalled, SortAvgRowsReturned, SortLoadByCPUMs, SortLoadByIOWaitMs,
		SortLockWaitMs, SortTagHash, SortApplication, SortDatabase, SortUser, SortAction,
		SortRoute, SortController, SortFramework, SortDBDriver,
	},
	Default:     SortLoadByTotalTime,
	DefaultDesc: true,
}

// TagsQuery is the parsed query string of GET /api/v1/insights/tags: R8.4's
// parameter set plus R8.10's cursor, aliased to ListQuery for the reason every other
// list route aliases it — R8.4 states the parameter set once.
type TagsQuery = ListQuery

// parseTagsQuery validates this route's parameters through the shared parser. The
// route takes all eight shared parameters with none inlined as required: the store
// filters on namespace, cluster and db each only when present (rollups.go:509-515),
// so a fleet-wide tags answer is a real answer here, unlike active-queries, where
// pg_stat_activity is per-instance and the route has to name one cluster to have a
// subject at all.
func parseTagsQuery(q url.Values) (TagsQuery, error) {
	return parseListQuery(q, tagsListQuerySpec)
}

// tagIdentity is a tag row's unique key — see the header's paging section for why it
// is one field here where queryIdentity composes three.
func tagIdentity(v dto.TagView) string { return v.TagHash }

// tagSortKey returns the string a tag row sorts on for one key, with the identity
// appended as the tiebreaker.
//
// The float columns go through sortableFloat (queries.go's, not a copy) and the one
// integral column through the same %09d rendering it uses, for the reason it gives:
// a lexicographic sort orders `99999.1` after `100000.2` unless both are padded to
// equal width, and a sort that is only sometimes numeric is the failure R8.10 names.
func tagSortKey(v dto.TagView, key string) string {
	primary := v.TagHash
	switch key {
	case SortApplication:
		primary = v.Application
	case SortDatabase:
		primary = v.Database
	case SortUser:
		primary = v.User
	case SortAction:
		primary = v.Action
	case SortRoute:
		primary = v.Route
	case SortController:
		primary = v.Controller
	case SortFramework:
		primary = v.Framework
	case SortDBDriver:
		primary = v.DBDriver
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
	return primary + "|" + tagIdentity(v)
}

// sortTags orders tag rows by one key and direction — the named instantiation of the
// shared sortRowsBy, kept for the reason clusters.go and queries.go keep theirs: a
// test can call it directly per key.
func sortTags(rows []dto.TagView, key string, desc bool) {
	sortRowsBy(rows, tagSortKey, key, desc)
}

// pageTags pages a set of tag rows into R8.4's envelope.
func pageTags(rows []dto.TagView, q TagsQuery, now time.Time) TagsEnvelope {
	return pageRows(rows, q, tagSortKey, dto.Meta{GeneratedAt: now})
}

// TagsSource is the read side of the tag rollups as this route needs it: given a
// parsed query, every tag with rows in its scope and window.
//
// The contract on an implementation is the one QueryList states and rollups.go
// follows: return the rows that match q's scope, not a page, because the handler
// pages them and that is what makes `meta.total` mean R8.4's "matching objects before
// the page was cut". rollups.go:488 adds the route-specific half — "q.Limit is unused
// for the reason QueryList gives (R8.10's paging is the handler's, and a source that
// returned a page makes meta.total report the page size)" — so a source that honoured
// `limit` would be wrong here in a way the compiler cannot catch and the shared pager
// can.
type TagsSource interface {
	Tags(ctx context.Context, q ListQuery) ([]dto.TagView, error)
}

// emptyTagsSource is the default, and it returns nil rows: pageRows turns that into
// `data: []` with total 0 and truncated false. An empty answer is the true answer for
// a process running without a store — the argument every list route's empty source
// makes, and the one a 500 would falsify by implying the data is unreadable rather
// than absent.
type emptyTagsSource struct{}

// Tags implements TagsSource by reporting no tags for any scope. The scope is not
// validated against a fleet here for the reason emptyQuerySource gives: a default that
// knows nothing about the fleet cannot distinguish "no such cluster" from "no tags",
// and guessing in a place that has no way to check is more expensive than not guessing.
func (emptyTagsSource) Tags(context.Context, ListQuery) ([]dto.TagView, error) {
	return nil, nil
}

// tagsSource is cfg.Tags with the default applied, so a Config that sets nothing still
// serves the documented route and New needs no change — the querySource and
// activeQueriesSource pattern, including why the accessor exists rather than a default
// inside New: the field is read at request time, so assigning a store after New takes
// effect without rebuilding the server. Their shared typed-nil caveat (2b63f1e)
// applies here in its usual form — `cfg.Tags = (*store.Store)(nil)` is not == nil, so
// the accessor hands back the typed nil and the implementation must answer on a nil
// receiver, which store/rollups.go's Tags does (it dereferences s.db on a nil
// receiver's field, and a nil *sql.DB returns an error rather than panicking).
func (s *Server) tagsSource() TagsSource {
	if s.cfg.Tags == nil {
		return emptyTagsSource{}
	}
	return s.cfg.Tags
}

// handleTags is R8.2's tags route, with the same three checks in the same order every
// list route uses: parameters first (a malformed request is malformed whether or not
// the store has data), readiness second (the same Readiness /readyz answers, so a
// server that has not synced cannot answer "no tags" for a fleet it has not read),
// then the rows, paged.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	q, err := parseTagsQuery(r.URL.Query())
	if err != nil {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	rows, err := s.tagsSource().Tags(r.Context(), q)
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("tags are not readable: %v", err)))
		return
	}
	env := pageTags(rows, q, time.Now().UTC())
	s.logDebug("tags served",
		"route", TagsPath,
		"namespace", q.Namespace,
		"cluster", q.Cluster,
		"db", q.DB,
		"sort", q.Sort,
		"rows", len(env.Data),
		"total", env.Meta.Total,
	)
	writeJSON(w, http.StatusOK, env)
}
