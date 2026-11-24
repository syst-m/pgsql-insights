// R4.1's active queries — GET /api/v1/active-queries.
//
// The route behind R10.8's Active Queries page and one of the two pages the
// requirement pairs with it: R4.1's live session list with its scorecard, and
// R4.4's blocking chain, which R10.8 renders as the row expansion. The body is
// `dto.ActiveQueriesResponse`, declared in dto/sessions.go and fixed there to
// collector.Session's field set rather than restated from the requirement text —
// the reason is R16.8's and the argument is on that file's header.
//
// # Which requirement this is, and why the manifest said another one
//
// `api/x-planned-paths.yaml` listed this path as `- /api/v1/active-queries  # R3.11`,
// and R3.11 is not this route. requirements.md:316 is R3.11: "Detect **plan
// changes**: keep plan hashes per fingerprint and surface a plan-change event
// when the hash changes for a stable fingerprint" — an R3 statement-plane feature
// whose API surface is the `planChanges` array on the query detail (dto/queries.go:494's
// PlanChangeView), not a session list. The route's real ids are R4.1 and R4.2
// (requirements.md:326-327) with R10.8 (:419) as the page, which is what
// web/src/routes.ts:214 has always said: `page: "active", requirement: "R10.8",
// endpoint: "GET /api/v1/active-queries"`.
//
// The annotation is corrected in the manifest alongside this commit, and the
// correction is not cosmetic: the two lists being disjoint is what makes R14.7
// checkable, and a route filed under the wrong id is a route whose requirement
// row can be marked done while the requirement that actually asks for it is not.
// The same class of error is measurable on six sibling lines of that file and is
// reported separately rather than fixed here, because a fix that touches seven
// lines belongs in a commit whose subject is the fix.
//
// # Who builds it
//
// requirements.md:673 rows R4.1–R4.8 as Phase **6a**, owned by A1, and
// docs/agents.md:75-78 keeps A2 in Phases 4 and 5 and gives 6b's pages to A6.
// This route is Phase 6a work built by A2, which is an assignment decision by the
// coordinator rather than a charter change, and the reason stated was that it is
// the missing endpoint keeping a second slot idle: A6's page list is Queries,
// Query Detail, Active Queries, Wait Events and System Insights, and three of the
// five have no server route. The collector half is A1's and already exists
// (internal/collector/activity.go, with its own tests against the live clusters);
// what was missing was the route, which is this file.
//
// # Routing
//
// `/api/v1/active-queries` is a literal path with no parameters of its own
// beyond the query string, so it cannot shadow anything: it shares no prefix with
// `/api/v1/insights/…` and no segment with `/api/v1/clusters…`. The one thing
// worth naming is that R4.5's `POST /api/v1/sessions/{pid}/terminate` is a
// different route on a different path — the terminate action is not a verb on
// this route, and adding it as one would make one path answer two requirements
// with two verbs while R8.2 counts them as two entries.
package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// ActiveQueriesPath is R4.1's session-list route, named once so server.go's
// registration, this handler, the tests, the document and web/src/routes.ts:215
// all spell it identically — the pattern QueriesPath, QueryDetailPath,
// QuerySeriesPath, ClusterDetailPath and QueryPlansPath set for their routes.
//
// The frontend already carries this exact string in its route table, so the
// constant is not choosing a spelling; it is the Go side of one that was picked
// when R8.2's catalogue listed `/active-queries` (requirements.md:378).
const ActiveQueriesPath = "/api/v1/active-queries"

// The two values R4.1's `Action` column can carry on the wire, and the name
// R12.8 uses for the action behind them.
//
// The spec names the column and the gate but not the strings, so these are
// provisional names under D13/R9.11's rule — name it, mark it provisional in the
// file itself, bound the blast radius. The bound here is one constant pair and
// one function (actionFor): if R12.8's flag ends up spelling the action
// differently, ActionNameTerminate changes, the fixture's three rows change, and
// nothing else in the response shape moves.
const (
	// ActionNameTerminate is R4.5's `pg_terminate_backend` action as it appears
	// in R12.8's `--allow-actions` value. R12.8 generalised the flag from
	// "terminate session only" to a set in which each mutation is "individually
	// named in the flag's value, not covered by one global boolean", which is why
	// this is a name in a list and not a bool on Config.
	ActionNameTerminate = "terminate"

	// ActionDisabled is what the column carries when the gate is off — which is
	// the default, so it is what every row answers on a stock server. R4.1's rule
	// is that the column "renders disabled with a link to the R12.8 gate … so the
	// column is never silently blank", and an empty string is exactly the silent
	// blank that sentence forbids. The UI renders the link; the value says why
	// there is no action.
	ActionDisabled = "disabled"
)

// ActiveQueriesSource is the read side of the session list as this route needs
// it: given a cluster, the response body minus its meta.
//
// It returns the whole `dto.ActiveQueriesResponse` rather than a row slice, and
// that is a different shape from QueryListSource for a reason R4.1 and the
// collector both state. R4.1's scorecard and R4.4's chains are computed over the
// full session set while the table shows the top 50, and collector.ActivityCollector's
// header (activity.go:257) makes the requirement explicit: the rows, the
// scorecard and the chains all come from one read-only transaction (R2.7), "so a
// number in the scorecard can be traced to a row and a row can be traced to the
// SQL". A source that returned only rows would force the handler to recompute
// Summarize and BlockingChains — a second copy of two algorithms the collector
// already owns — and a handler that recomputed them from a *paged* row set would
// produce the header/table disagreement the collector's Scorecard.Total field
// exists to prevent.
//
// The source leaves `meta` zero and the handler fills it, which is the division
// pageQueries makes for the queries list: `generatedAt` is a property of the
// response, not of the round that produced the rows. TestActiveQueriesHandlerOwnsMeta
// pins it by handing the handler a source that set meta and checking the served
// body carries the handler's.
type ActiveQueriesSource interface {
	ActiveQueries(ctx context.Context, namespace, cluster string) (dto.ActiveQueriesResponse, error)
}

// emptyActiveQueriesSource is the default, and its answer is an empty list.
//
// This route follows the list routes (emptyQuerySource), not the keyed ones
// (emptyQueryDetailSource, emptyQueryPlansSource), and for their shared reason:
// "no rows" is a complete answer to "what sessions are on this cluster". The
// keyed routes answer 404 because a question that names a row and gets nothing
// has one true answer; this route names a cluster, and a cluster with no backends
// is a cluster that exists.
//
// The empty response is not a zero value. `data`, `scorecard.byState` and
// `blockingChains` are all initialised to empty rather than nil, because
// encoding/json renders nil as `null` and dto's contract is that its list and map
// fields are never null — `[]` and `null` would be two encodings of one fact and
// every consumer would have to handle both.
type emptyActiveQueriesSource struct{}

// ActiveQueries implements ActiveQueriesSource by reporting no sessions for any
// cluster. The cluster is not validated against a fleet here: that is
// emptyQuerySource's problem too, and the reason is the same — a default that
// knows nothing about the fleet cannot distinguish "no such cluster" from "no
// sessions", and guessing wrong on a route whose real answer is one round away is
// cheaper than guessing in a store that has the answer.
func (emptyActiveQueriesSource) ActiveQueries(_ context.Context, _, _ string) (dto.ActiveQueriesResponse, error) {
	return dto.ActiveQueriesResponse{
		Data:           []dto.SessionView{},
		Scorecard:      dto.ScorecardView{ByState: map[string]int{}},
		BlockingChains: []dto.BlockingChainView{},
	}, nil
}

// activeQueriesSource is cfg.ActiveQueries with the default applied, so a Config
// that sets nothing still serves the documented route and New needs no change —
// the querySource, queryDetailSource and queryPlansSource pattern, including why
// the accessor exists rather than a default inside New: the field is read at
// request time, so assigning a store after New takes effect without rebuilding
// the server. The typed-nil caveat those three document (2b63f1e) applies here in
// its usual form — `cfg.ActiveQueries = (*store.Store)(nil)` is not == nil, so
// the accessor hands back the typed nil and the implementation must answer on a
// nil receiver.
func (s *Server) activeQueriesSource() ActiveQueriesSource {
	if s.cfg.ActiveQueries == nil {
		return emptyActiveQueriesSource{}
	}
	return s.cfg.ActiveQueries
}

// actionFor is R4.1's Action column for one row, derived from R12.8's gate.
//
// The collector leaves the field empty and says why on its own struct
// (activity.go:231): the value depends on a runtime flag on the API server, "not
// a fact about the session", so the API owns it. That makes this three-line
// function the field's only implementation, and the reason it takes the action
// list rather than a bool is R12.8's own generalisation — the flag names each
// mutation separately, so "is terminate allowed" is a membership test and not a
// flag read.
func actionFor(actions []string, action string) string {
	if slices.Contains(actions, action) {
		return action
	}
	return ActionDisabled
}

// applyActions fills the Action column on every session in the response.
//
// A row appears in up to four places — `data`, `scorecard.longestQuery`,
// `scorecard.longestIdleInTransaction`, and a chain's `blocked` or `blockers` —
// and each is a *copy* of the same session, so filling only the `data` copy would
// put two different `action` values on the wire for one pid. That is the
// second-copy failure in miniature, and it is why this walks all four sites rather
// than the one the table renders from. TestActiveQueriesActionColumnIsConsistent
// is the assertion that keeps the walk complete if a fifth site is ever added.
func applyActions(v *dto.ActiveQueriesResponse, action string) {
	for i := range v.Data {
		v.Data[i].Action = action
	}
	if v.Scorecard.LongestQuery != nil {
		v.Scorecard.LongestQuery.Action = action
	}
	if v.Scorecard.LongestIdleInTransaction != nil {
		v.Scorecard.LongestIdleInTransaction.Action = action
	}
	for i := range v.BlockingChains {
		v.BlockingChains[i].Blocked.Action = action
		for j := range v.BlockingChains[i].Blockers {
			v.BlockingChains[i].Blockers[j].Action = action
		}
	}
}

// handleActiveQueries is R4.1's read path, with the same three checks in the
// same order the other list routes use: parameters first (a malformed request is
// malformed whether or not the store has data), readiness second (the same
// Readiness /readyz answers, so a server that has not synced cannot answer "no
// sessions" for a cluster it has not read yet), then the rows.
//
// # The R8.4 parameter set, and the four this route does not take
//
// R8.4 lists seven parameters for every list endpoint. This route declares two,
// and each of the five absent ones has a reason rather than an omission:
//
//	namespace, cluster — required here, not optional as the shared components
//	  declare them, because pg_stat_activity is per-instance: R4.1's list is one
//	  cluster's sessions and there is no fleet-wide session list to fall back to.
//	  They are inline in the document rather than $refs for the reason query_plans
//	  gives — the shared Namespace/Cluster components say `required: false`.
//	db                 — R8.4's database filter exists to narrow a query store to
//	  one database. R4.1's session list is the instance's backends, and the
//	  database is a column of each row (SessionView.Database), not a scope.
//	from, to           — R4.2's list is a live snapshot refreshed every 15 s; it
//	  has no window to name. The 60 s server-side history R4.2 also requires is
//	  the metrics plane's (R9.7's db_backends family), not this route's.
//	limit, sort        — R4.1 fixes both: the list is "Cloud SQL's top 50" ranked
//	  by the collector's own order (activity.go:792's TopSessions), so a `limit`
//	  above 50 would page past a list that is already the whole top-N and a `sort`
//	  would reorder rows whose ranking is the requirement. The route therefore
//	  does not page: `meta.total` is the length of `data`, `truncated` is false by
//	  construction and `nextCursor` is empty — the three-field agreement dto.Meta
//	  requires. The pre-cut count is `scorecard.total`, which is the field R4.1's
//	  header reads when the two differ.
func (s *Server) handleActiveQueries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespace := strings.TrimSpace(q.Get("namespace"))
	cluster := strings.TrimSpace(q.Get("cluster"))
	if namespace == "" || cluster == "" {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"namespace and cluster are required (R4.1): pg_stat_activity is per-instance, "+
				"so the route names one cluster's sessions and cannot answer for the fleet"))
		return
	}
	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}

	resp, err := s.activeQueriesSource().ActiveQueries(r.Context(), namespace, cluster)
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("active queries are not readable: %v", err)))
		return
	}

	// The two normalisations the dto contract requires of a response it did not
	// build: the Action column, which the collector leaves empty because the gate
	// is the server's (actionFor), and the nil-slice-to-empty rule, which a source
	// returning a zero response would otherwise put on the wire as `null`.
	applyActions(&resp, actionFor(s.cfg.Actions, ActionNameTerminate))
	if resp.Data == nil {
		resp.Data = []dto.SessionView{}
	}
	if resp.BlockingChains == nil {
		resp.BlockingChains = []dto.BlockingChainView{}
	}
	if resp.Scorecard.ByState == nil {
		resp.Scorecard.ByState = map[string]int{}
	}
	resp.Meta = dto.Meta{
		Total:       len(resp.Data),
		Truncated:   false,
		GeneratedAt: time.Now().UTC(),
	}

	s.logDebug("active queries served",
		"route", ActiveQueriesPath,
		"namespace", namespace,
		"cluster", cluster,
		"sessions", len(resp.Data),
		"scorecardTotal", resp.Scorecard.Total,
		"chains", len(resp.BlockingChains),
	)
	writeJSON(w, http.StatusOK, resp)
}
