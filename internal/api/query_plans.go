// R3.10's plan samples — GET /api/v1/insights/queries/{fingerprint}/plans.
//
// This is the fourth of the four query-surface routes R8.2's catalogue lists
// (dto/queries.go's header is the table: the list, the detail, this one, and
// R3.12's tags), and the last one that existed only as a line in
// api/x-planned-paths.yaml. The manifest's own note is why the route is not
// folded into the detail response it duplicates:
//
//	/api/v1/insights/queries/{fingerprint}/plans (R3.10) stays planned even
//	though the detail response carries the same list inline in `plans`: R8.2
//	counts the route separately, and an inline array on one response is not the
//	route.
//
// That sentence answers the R16.8 objection before it can be raised. The detail
// body embeds `Plans []dto.PlanSampleView` and this route serves the same type
// in an envelope, so the two bodies will hold the same samples for the same
// triple — but the manifest says the array is not the route, and the reason is
// the same one query_detail.go gives for not inlining the series array: the
// client's two questions ("show me this query" and "show me this query's plans")
// have different costs on the server, and R3.10's capture is rate-limited
// independently of R3.2's row. A page that renders the table row must not pay
// for EXPLAIN output, and R8.2's answer is a route, not a field.
//
// # What the samples are, and why the shape is R3.10's
//
// The element type is dto.PlanSampleView, whose docblock carries R3.10's three
// constraints — the normalized-and-truncated template that cannot be EXPLAINed
// (so the sample names the raw first-seen text), the PREPARE + EXPLAIN EXECUTE
// path for parameterized statements (so the sample carries a Method), and the
// pg_monitor-only deployment where EXPLAIN answers `permission denied for table
// …` (so the sample carries an Availability that is a state, not an error). This
// handler forwards that type and adds nothing to it: R3.10's capture is Phase
// 6b's, and a route that invented its own plan shape would be the second copy
// the dto exists to prevent.
//
// # Which row a fingerprint names
//
// Same answer as the detail route, for the same reason: R3.4 makes a fingerprint
// canonical *within* a cluster, so `namespace` and `cluster` are required query
// parameters alongside the path's `{fingerprint}`, and the three together are the
// row. query_detail.go documents the residual spec gap (which row the triple
// names when a cluster has two for one fingerprint) and sends it to A3; this
// route inherits whatever that ruling says, because both routes ask the store the
// same question with the same three values.
//
// # Routing
//
// `/api/v1/insights/queries/{fingerprint}/plans` is a distinct pattern from both
// `/api/v1/insights/queries/{fingerprint}` and the literal
// `/api/v1/insights/queries/series`, so registering it changes nothing about how
// those two resolve. It does mean `/api/v1/insights/queries/series/plans` now
// matches this route with fingerprint "series" — the same class of collision
// query_detail.go records for `/insights/queries/series`, and equally harmless
// for the same reason: R3.4 makes fingerprints hashes, so "series" is not one.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// QueryPlansPath is R3.10's plan-samples route, named once so server.go's
// registration, this handler, the tests and the document all spell it
// identically — the pattern QueriesPath, QueryDetailPath, QuerySeriesPath and
// ClusterDetailPath set for their routes.
//
// The spelling is R8.2's catalogue's own, and it is the string
// api/x-planned-paths.yaml carried under r8.2-catalogue until this commit moved
// it up to `paths` (the two lists are disjoint by construction — that is what
// makes R14.7 checkable, and what makes TestQueryPlansRouteIsDocumented able to
// fail if the handler and the document are moved apart in different commits).
const QueryPlansPath = "/api/v1/insights/queries/{fingerprint}/plans"

// QueryPlansEnvelope is the response body of this route: R8.4's one generic over
// R3.10's sample type. It is an alias for the same reason QuerySeriesEnvelope and
// TagsEnvelope are — the name exists so the document has something to `$ref`, not
// because the envelope has a second definition to keep in step. The queries route
// left that list when its body became dto.QueriesResponse: a route with a named
// struct has a name already, and an alias over it is a second one.
type QueryPlansEnvelope = dto.Envelope[dto.PlanSampleView]

// ErrQueryPlansNotFound is what a source returns for a triple with no query row,
// and the only error this handler answers with 404.
//
// A sentinel for the reason ErrQueryDetailNotFound is one: the handler's decision
// is one bit — "the store answered" versus "the store has nothing" — and
// errors.Is over wrapping lets a store add context ("no row for 550e8400… in
// default/pg1") without changing what the client sees.
var ErrQueryPlansNotFound = errors.New("no query row for that fingerprint")

// QueryPlansSource is the read side of the plan store as this route needs it.
//
// It is a separate interface from QueryListSource and QueryDetailSource for the
// reason those two are separate from each other: R8.2's routes are separate, so a
// store can serve one before the others, and merging them would force a store to
// stub the routes it has not written in order to ship the one it has. R3.10's
// capture is Phase 6b's, so this is the route whose real implementation is
// furthest away — which is an argument for the interface existing now, so that
// landing the capture is assigning a field rather than adding a route.
//
// The method returns the samples rather than the envelope, which is what
// QueryList and QuerySeries both do and for the reason queries.go states: a
// source that returned only a page would make `meta.total` report the page size
// (D18's ruling for the fleet list). There is no paging argument here to
// contradict it — see handleQueryPlans for why this route does not page.
type QueryPlansSource interface {
	QueryPlans(ctx context.Context, namespace, cluster, fingerprint string) ([]dto.PlanSampleView, error)
}

// emptyQueryPlansSource is the default, and its answer is 404.
//
// The two sibling defaults disagree, and both are right for their own route:
// emptyQuerySource answers 200 with an empty page, because "no rows" is a
// complete answer to "what rows are there"; emptyQueryDetailSource answers 404,
// because a question that names a row and gets nothing has one true answer. This
// route asks a keyed question *and* wants a collection, and the keyed half wins:
// a source with no rows has no row for this fingerprint, which is not-found
// whatever the sample list would have been.
//
// A real source distinguishes the two cases this default cannot: a query row with
// zero captured samples answers 200 with `data: []` (R3.10's capture is bounded
// at 5 plans/minute/cluster, so most rows legitimately have none yet), and only a
// fingerprint the store has never seen answers 404.
type emptyQueryPlansSource struct{}

// QueryPlans implements QueryPlansSource by reporting not-found for every triple.
func (emptyQueryPlansSource) QueryPlans(_ context.Context, namespace, cluster, fingerprint string) ([]dto.PlanSampleView, error) {
	return nil, fmt.Errorf("%w: %s/%s/%s", ErrQueryPlansNotFound, namespace, cluster, fingerprint)
}

// queryPlansSource is cfg.QueryPlans with the default applied, so a Config that
// sets nothing still serves the documented route and New needs no change — the
// querySource and queryDetailSource pattern, including why the accessor exists
// rather than a default inside New: the field is read at request time, so
// assigning a store after New takes effect without rebuilding the server.
func (s *Server) queryPlansSource() QueryPlansSource {
	if s.cfg.QueryPlans == nil {
		return emptyQueryPlansSource{}
	}
	return s.cfg.QueryPlans
}

// handleQueryPlans is R3.10's read path, with the same three checks in the same
// order the other three query routes use: parameters first (a malformed request
// is malformed whether or not the store has data, and a 503 tells the client to
// retry something that will never succeed), readiness second (the same Readiness
// /readyz answers, so a server that has not synced cannot answer 404 for a query
// it has not read yet), then the samples.
//
// The route does not page, and the reason is R3.10's own bound rather than an
// omission: capture is 5 plans/minute/cluster by default, so the sample set for
// one fingerprint is bounded by the retention window, not by how many plans the
// cluster has. testdata/api/query-plans.json says so on its own body — two
// samples, `total: 2`, `truncated: false`, and no `nextCursor`, which is the
// three-field agreement dto.Meta requires. So `total` is the length of the data
// and `truncated` is false by construction; if a source ever pages, the interface
// gains a ListQuery argument the way QueryList's did, and this line is where it
// gets used.
func (s *Server) handleQueryPlans(w http.ResponseWriter, r *http.Request) {
	fingerprint := strings.TrimSpace(r.PathValue("fingerprint"))
	if fingerprint == "" {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"fingerprint is required (R3.10): the path parameter names the query whose plans these are"))
		return
	}
	// namespace and cluster are query parameters, not path segments, because R8.2
	// spells the route with one path parameter — and they are required for R3.4's
	// reason, the same one query_detail.go states: a fingerprint is canonical
	// within a cluster, so without the pair the server cannot tell which cluster's
	// statement this is.
	q := r.URL.Query()
	namespace, cluster := strings.TrimSpace(q.Get("namespace")), strings.TrimSpace(q.Get("cluster"))
	if namespace == "" || cluster == "" {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"namespace and cluster are required (R3.4): a fingerprint is canonical within a cluster, "+
				"so the same normalised statement in two clusters is two queries"))
		return
	}
	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	plans, err := s.queryPlansSource().QueryPlans(r.Context(), namespace, cluster, fingerprint)
	switch {
	case errors.Is(err, ErrQueryPlansNotFound):
		writeErrorBody(w, http.StatusNotFound, dto.NewError(dto.ErrNotFound, err.Error()))
		return
	case err != nil:
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("query plans are not readable: %v", err)))
		return
	}
	// `data` is never null on the wire: dto.Envelope's contract is a list, and a
	// nil slice marshals to `null` where an empty one marshals to `[]`. The list
	// route's pageQueries normalises this; this route has no pager to do it in, so
	// it happens here.
	data := plans
	if data == nil {
		data = []dto.PlanSampleView{}
	}
	env := QueryPlansEnvelope{
		Data: data,
		Meta: dto.Meta{
			Total:       len(plans),
			Truncated:   false,
			GeneratedAt: time.Now().UTC(),
		},
	}
	s.logDebug("query plans served",
		"route", QueryPlansPath,
		"namespace", namespace,
		"cluster", cluster,
		"fingerprint", fingerprint,
		"samples", len(data),
	)
	writeJSON(w, http.StatusOK, env)
}
