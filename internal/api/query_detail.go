// R10.7's Query Detail — GET /api/v1/insights/queries/{fingerprint}.
//
// queries.go and this file are the two routes R8.2's catalogue splits into a
// list and a detail, and the manifest said so out loud when the list moved up:
// "Its sibling two lines below — /api/v1/insights/queries/{fingerprint}, R10.7's
// detail route — is a separate catalogue entry and is still planned: the list and
// the detail are two routes, and landing one does not land the other." This is
// the sibling, and the line that note promised is the one this commit deletes.
//
// The body is dto.QueryDetailView: R3.2's row embedded, so the header's
// fingerprint, database and every load column are the same object the table row
// renders (one row, one shape, no second copy for the detail page to drift from),
// plus the fields R10.7 asks for and R3.2's table has no column for — user,
// firstSeen, latency, plans, planChanges.
//
// The load chart R10.7 puts on this page is not in the body, and the reason is
// dto/queries.go's own sentence: series.go's per-point set "is what lets one
// response serve both R10.6's row sparkline and R10.7's Query Detail chart", and
// a batch of one fingerprint is legal (MaxBatchFingerprints is 50). Inlining the
// array here would duplicate an answer the client can already ask for — the
// second-copy failure this project's charter exists to prevent.
//
// # Which row a fingerprint names
//
// The path has room for one fingerprint and R9.4's row identity is
// (cluster, db, user): the same normalised statement run by two roles is two rows
// in the store, which is what dto.QueryDetailView.User means by "a statement run
// by two roles is two rows in the store and two rows in the table, each naming
// its own user". queries.go already answers "which row is this" for the list —
// its queryIdentity is namespace/cluster/fingerprint — so this route asks for the
// same three values and the two routes cannot disagree about what a row is.
//
// What the spec does not settle is which row the triple names when a cluster has
// two of them for one fingerprint, and this file flags that rather than inventing
// a rule: the source decides, and a Phase 3 store keyed on the R9.4 triple has to
// answer it anyway to serve the list. It goes to A3 to rule on because the answer
// changes the route's contract, not only its code.
//
// # Routing
//
// Go's mux prefers a literal over a wildcard, so /api/v1/insights/queries/series
// keeps answering R8.11's batched route and a fingerprint literally spelled
// "series" resolves to that handler instead of this one. That is the correct
// precedence — R3.4 makes fingerprints hashes, so "series" is not one — and it is
// why the two routes coexist without the detail route swallowing the series path.

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// QueryDetailPath is R8.2's query-detail route, named once so server.go's
// registration, this handler, the tests and the document all spell it
// identically — the pattern QueriesPath, QuerySeriesPath and ClusterDetailPath
// set for their routes.
//
// The spelling is R8.2's catalogue's own (`/api/v1/insights/queries/
// {fingerprint}`) and A4's client already builds it: web/src/routes.ts:193
// carries `endpoint: "GET /api/v1/insights/queries/{fingerprint}"` for the
// query-detail page, so the const and the consumer were written against one
// string rather than each inventing one.
const QueryDetailPath = "/api/v1/insights/queries/{fingerprint}"

// ErrQueryDetailNotFound is what a source returns for a fingerprint it has no row
// for, and the only error this handler answers with 404.
//
// It is a sentinel rather than a type because the handler's decision is one bit —
// "the store answered" versus "the store has nothing" — and errors.Is over
// wrapping lets a Phase 3 store add context ("no row for 550e8400… in
// default/pg1") without changing what the client sees. dto.ErrNotFound is the
// R8.5 *code* string; this is the Go value that selects it.
var ErrQueryDetailNotFound = errors.New("no query detail for that fingerprint")

// QueryDetailSource is the read side of the query store as the detail route needs
// it: given a row's identity, the detail view of that row. It is a separate
// interface from QueryListSource for the same reason QuerySeriesSource is one —
// R8.2's routes are separate, so a store can serve one before the others, and
// making the three methods one interface would force a store to stub the two it
// has not written in order to ship the one it has.
//
// The contract on an implementation: return ErrQueryDetailNotFound (or an error
// wrapping it) for a triple with no row, and a view whose embedded QueryView
// agrees with the row the list route would have shown for the same triple — the
// embedding is what makes that checkable rather than aspirational.
type QueryDetailSource interface {
	QueryDetail(ctx context.Context, namespace, cluster, fingerprint string) (dto.QueryDetailView, error)
}

// emptyQueryDetailSource is the default, and its answer is 404: a store that has
// not been built has no rows, so every fingerprint is one it has never seen.
//
// That is the detail route's version of the honesty promql.go and series.go state
// for their defaults. A 200 with a zero-value QueryDetailView would be a lie —
// the body's own fingerprint, database and latency would all be empty, which is
// a page rendering a query that does not exist — and a 503 would claim the store
// is unreadable when it is simply empty. The list route answers 200 with an empty
// page in the same situation because "no rows" is a complete answer to "what rows
// are there"; here the question names a row, and the true answer is "not that one".
type emptyQueryDetailSource struct{}

// QueryDetail implements QueryDetailSource by reporting not-found for every
// fingerprint.
func (emptyQueryDetailSource) QueryDetail(_ context.Context, namespace, cluster, fingerprint string) (dto.QueryDetailView, error) {
	return dto.QueryDetailView{},
		fmt.Errorf("%w: %s/%s/%s", ErrQueryDetailNotFound, namespace, cluster, fingerprint)
}

// queryDetailSource is cfg.QueryDetail with the default applied, so a Config that
// sets nothing still serves the documented route and New needs no change — the
// querySource and querySeriesSource pattern, including the reason the accessor
// exists instead of a default inside New: the field is read at request time, so
// assigning a store after New takes effect without rebuilding the server.
func (s *Server) queryDetailSource() QueryDetailSource {
	if s.cfg.QueryDetail == nil {
		return emptyQueryDetailSource{}
	}
	return s.cfg.QueryDetail
}

// handleQueryDetail is R10.7's detail route, with the same three checks in the
// same order queries.go and cluster_detail.go use:
//
//  1. Parameters first — a request naming no fingerprint, or no cluster to
//     resolve it in, is malformed whether or not the store has data, and
//     answering 503 to it tells the client to retry something that will never
//     succeed.
//  2. Readiness second, through the same Readiness /readyz answers from
//     (R8.7(i)): 503 NotReady, so a server that has not synced cannot answer 404
//     for a query it has not read yet (cluster_detail.go's comment names this
//     exact failure for the cluster routes).
//  3. Then the row, with not-found and unreadable kept as the two different
//     answers R8.5's code set exists to distinguish.
func (s *Server) handleQueryDetail(w http.ResponseWriter, r *http.Request) {
	fingerprint := strings.TrimSpace(r.PathValue("fingerprint"))
	if fingerprint == "" {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"fingerprint is required (R10.7): the path parameter names the query this detail is for"))
		return
	}
	// namespace and cluster are query parameters here, not path segments, because
	// R8.2 spells the route with one path parameter. They are required rather
	// than optional because R3.4 makes a fingerprint canonical *within* a
	// cluster: without them the server cannot tell which cluster's statement this
	// is, and a fingerprint is not a global key.
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
	view, err := s.queryDetailSource().QueryDetail(r.Context(), namespace, cluster, fingerprint)
	switch {
	case errors.Is(err, ErrQueryDetailNotFound):
		writeErrorBody(w, http.StatusNotFound, dto.NewError(dto.ErrNotFound, err.Error()))
		return
	case err != nil:
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("query detail is not readable: %v", err)))
		return
	}
	// R8.2's table gives this route a bare QueryDetailView, not an envelope: the
	// envelope's job (R8.4) is to carry paging for a collection, and a single
	// object has no total, no page and no cursor to carry.
	writeJSON(w, http.StatusOK, view)
}
