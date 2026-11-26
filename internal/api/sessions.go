// R4.5's terminate action — POST /api/v1/sessions/{pid}/terminate.
//
// requirements.md:330 is one sentence with five obligations, and this file is
// the half that lives on the server. Taking it in order, because each clause
// becomes a different line here and the requirement's wording is the only
// ordering worth defending:
//
//   - "POST /api/v1/sessions/{pid}/terminate" — the route, registered in
//     server.go as `POST SessionsTerminatePath`. The path is a const for the
//     reason every other route's path is one: the annotation table, the
//     generated document and the tests all name it, and a literal repeated in
//     three files is three chances to typo it.
//   - "→ pg_terminate_backend(pid)" — the SQL. It is not in this file: the
//     function runs on a Postgres instance, and this package's rule is that a
//     handler owns the HTTP half and a source owns the data half
//     (active_queries.go:99's ActiveQueriesSource, clusters.go's ClusterSource,
//     query_plans.go's QueryPlansSource). TerminateSession is that seam here,
//     and it is narrower than the three of them for a reason the interface's own
//     docblock gives.
//   - "gated behind --allow-actions=true (default false)" — R12.8's
//     Config.Actions, read through the same actionFor active_queries.go:158
//     already uses to fill R4.1's `Action` column. One function answers "is
//     terminate allowed" for both routes, which is the point: the column the
//     list renders and the route behind it cannot disagree, and a second gate
//     implementation is the second copy that would let them.
//   - "requiring a confirmed UI action" — the UI's dialog is R10.8's, and the
//     server cannot observe a click. `?confirm=true` is the clause's server-side
//     expression: the client states that it confirmed, the route requires the
//     statement, and the value is on the wire where the audit line can quote it.
//     Without it the clause has no server-side implementation at all, and a
//     retried or prefetched POST kills a backend nobody confirmed.
//   - "audited to a log line with actor and target" — the Info line at the end
//     of handleTerminateSession. `actor` is api.IdentityFrom(r).User
//     (auth.go:126), the same identity the auth middleware resolved; `target` is
//     the `<namespace>/<cluster>` pair the request names. Both are also in the
//     response body (dto.TerminateResponse), read once and written to both, so
//     the line and the body cannot disagree.
//   - "and refused on a replica" — the source's answer, not a guess here. Only
//     the thing that opens the connection knows the role; the handler maps its
//     ErrNotPrimary to the same 403 the config gate gives, for the reason the
//     two-refusals-one-code argument below gives.
//
// # Why the gate answers 403 Unsupported and not a new code
//
// R8.5's enum (dto/clusters.go:107) has ten codes. Two of R4.5's refusals map
// onto one of them cleanly — `Unsupported`, "feature gated off by config", is
// literally what --allow-actions=false is — and the second does not: a replica
// is not a config setting. The alternatives are all worse. `Forbidden` is, per
// R8.8's own words, "R12's scope verdict", and R8.8 refuses to let one code
// answer two different failures "without costing the client the distinction" —
// using it for a role refusal is exactly that mistake. `NotFound` would say the
// session does not exist, and it does. `Unavailable` would say the dependency is
// down, and it is up and answering, which is how it knows it is a replica.
//
// Minting an eleventh code is the move R8.3 made for its own narrow case
// (ErrPromQLUnavailable, requirements.md's R8.3), and it is available. It is
// not taken here because R4.5 unites both refusals in one sentence as two ways
// the action is not available, and a client that handles one must handle the
// other: two codes for one requirement's two refusals buys a distinction nobody
// acts on, and every new code is a permanent addition to a documented enum. The
// message distinguishes them ("--allow-actions does not include terminate" vs
// "pg_terminate_backend is refused on a replica"), which is where R8.5 puts
// detail; the code says which class, and both are the class "this server will
// not perform this action".
//
// # Routing
//
// `/api/v1/sessions/{pid}/terminate` shares no prefix with any existing
// pattern: it is not under `/api/v1/insights/…`, not under
// `/api/v1/clusters/{namespace}/{name}`, and Go's ServeMux matches `{pid}`
// against one segment, so no request can route to two handlers. The verb is
// POST only — active_queries.go's header already rules that the terminate
// action "is not a verb on this route", so GET /api/v1/active-queries and POST
// /api/v1/sessions/{pid}/terminate are two routes answering two requirements,
// which is how R8.2 counts them.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// SessionsTerminatePath is R4.5's route (requirements.md:330). It is a const for
// the reason ActiveQueriesPath and QueriesPath are ones: the annotation table,
// the generated document and the tests all name the same string, and a literal
// repeated across three files is three chances to spell it differently.
const SessionsTerminatePath = "/api/v1/sessions/{pid}/terminate"

// ErrNotPrimary is the source's refusal to act on a replica.
//
// R4.5's last clause — "and refused on a replica" — is a fact about the
// instance, and the only component that can answer it is the one that opens the
// connection: the API's cluster inventory carries R1.5's per-instance roles, but
// mapping a pid to the instance that owns it would mean listing every instance's
// sessions to find the row, which is a full activity read to answer a question
// the connection answers with its own role. So the source decides and the
// handler maps, and the exported var exists so a real implementation returns
// this value rather than a string that happens to contain "replica" —
// errors.Is is the check, and an unexported error would leave a second
// implementation free to invent its own.
var ErrNotPrimary = errors.New("target instance is a replica: pg_terminate_backend is refused (R4.5)")

// errNoTerminateSource is emptyTerminateSource's answer: this process has no
// connection to any Postgres instance, so the action cannot be performed.
//
// It is an error and not a `terminated: false`, because "the backend was already
// gone" and "nobody asked Postgres" are different facts and the response has a
// field that says which. The route answers 503 Unavailable, which is the code
// dto/clusters.go:115 defines as "dependency down (store, kube API)" — the
// dependency here is the SQL endpoint, and cmd/pgsi builds one from the
// collector's opener (internal/collector/pgxopener.go) the moment a deployment
// asks for actions.
var errNoTerminateSource = errors.New("no SQL endpoint configured for this process")

// TerminateSource is the write side of R4.5's action: run pg_terminate_backend
// on the named instance and report Postgres's own answer.
//
// It returns a bool rather than the response body, and that is deliberately
// narrower than the three read-side interfaces it sits beside.
// ActiveQueriesSource, QueryListSource and QueryPlansSource each return a whole
// dto response because the source *computes* that response — the rows, the
// scorecard and the chains are its work, and the handler adds only the fields
// that are properties of the response rather than of the data (meta, and the
// Action column the gate owns). Here the source computes exactly one fact. The
// other three fields of dto.TerminateResponse are echoes of the request: the
// pid and target the caller supplied and the actor the auth middleware
// attached. Having the source return them too would put a second opinion where
// the request is the authority — a source that echoed the wrong pid would ship a
// body contradicting the URL that asked for it — so the handler assembles the
// body from the request and takes only `terminated` from the source.
//
// The interface is a method and not a func field for the same reason its three
// siblings are interfaces: a deployment swaps in an implementation, and an
// interface with one method documents what the route needs and nothing more.
type TerminateSource interface {
	// TerminateSession runs pg_terminate_backend(pid) on the instance named by
	// namespace and cluster, returning PG's boolean result. It returns
	// ErrNotPrimary when that instance is a replica (R4.5's refusal) and must
	// not attempt the call in that case.
	TerminateSession(ctx context.Context, namespace, cluster string, pid int) (terminated bool, err error)
}

// emptyTerminateSource is the default, and its answer is that the action cannot
// be performed here.
//
// This route follows the keyed sources (emptyQueryDetailSource,
// emptyQueryPlansSource), not the list sources, and for their reason: a request
// that names a pid and gets nothing has one true answer, and "no rows" is not it.
// It is not a 404 either — the default knows nothing about the fleet, so it
// cannot say the session is absent, only that it cannot ask. That is
// errNoTerminateSource's 503.
//
// In practice the default is rarely reached, because the gate is checked first
// and --allow-actions defaults to false: a stock server answers 403 Unsupported
// without consulting the source. The default exists so the route is registered
// and answering (R14.7 forbids a documented route that 404s for the wrong
// reason) on a process that serves the API but does not collect.
type emptyTerminateSource struct{}

// TerminateSession implements TerminateSource by refusing every request with
// errNoTerminateSource. The three parameters are not validated here for the
// reason emptyQuerySource gives: a default that knows nothing about the fleet
// cannot distinguish "no such cluster" from "no connection", and guessing in a
// default is more expensive than answering with the one fact it does have.
func (emptyTerminateSource) TerminateSession(context.Context, string, string, int) (bool, error) {
	return false, errNoTerminateSource
}

// terminateSource is cfg.Terminate with the default applied, so a Config that
// sets nothing still serves the documented route. The accessor exists rather
// than a default inside New for the reason the four source fields above it give
// — the field is read at request time, so assigning a source after New takes
// effect without rebuilding the server — and carries their shared typed-nil
// caveat (2b63f1e): `cfg.Terminate = (*collector.SQLExecutor)(nil)` is not
// == nil, so the accessor hands back the typed nil and the implementation must
// answer on a nil receiver.
func (s *Server) terminateSource() TerminateSource {
	if s.cfg.Terminate == nil {
		return emptyTerminateSource{}
	}
	return s.cfg.Terminate
}

// handleTerminateSession is R4.5's endpoint: the gate, the confirmation, the
// call, and the audit line.
//
// # Why the gate is checked before readiness
//
// The other routes order it parameters → readiness → source, and the reason
// queries.go gives is that "a malformed request is malformed whether or not the
// store has data". This route inserts one more check between those two, and the
// order is the one that keeps each answer true to the fact it reports:
//
//   - parameters first, unchanged — a request with no pid is malformed in every
//     server state, and R4.5's `confirm` requirement is a parameter check
//     because it arrives as a query parameter.
//   - the gate second, before readiness. --allow-actions is a permanent
//     property of this process; readiness is a transient one. Checking readiness
//     first would answer 503 NotReady during the pre-sync window to a request
//     whose real answer is 403 Unsupported, and it would be wrong the moment the
//     sync completes — a transient answer to a permanent question. The gate's
//     answer does not depend on the fleet at all, so it can be given immediately
//     and is the same answer the route gives at t+1s and at t+1h.
//   - readiness third, then the source. A server that has not synced cannot name
//     the instance a pid belongs to, so the 503 is real for this route's own
//     reason rather than inherited from the pattern.
func (s *Server) handleTerminateSession(w http.ResponseWriter, r *http.Request) {
	// --- parameters -------------------------------------------------------
	//
	// pid comes from the path, namespace and cluster from the query string. The
	// path cannot carry them: pg_stat_activity is per-instance, so a pid is
	// unique only within one instance and the request has to name which. A2's
	// ruling put them in the query string rather than a JSON body, which is what
	// makes this POST have no requestBody in the document at all — the same
	// shape R4.1's GET uses, so one parameter set describes both routes.
	pidStr := strings.TrimSpace(r.PathValue("pid"))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			fmt.Sprintf("invalid pid %q: pg_terminate_backend takes a positive integer backend pid", pidStr)))
		return
	}

	q := r.URL.Query()
	namespace := strings.TrimSpace(q.Get("namespace"))
	cluster := strings.TrimSpace(q.Get("cluster"))
	if namespace == "" || cluster == "" {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"namespace and cluster are required (R4.5): pg_terminate_backend runs on one "+
				"instance and a pid is unique only within an instance, so the request must name it"))
		return
	}

	// R4.5's "requiring a confirmed UI action", as the client's statement. Any
	// value other than "true" is a refusal to confirm, and it answers 400 rather
	// than 403 because it is a request the server will not act on for a reason
	// inside the request itself — the same class as a missing parameter, which is
	// what R8.5's InvalidRequest covers.
	if confirm := strings.TrimSpace(q.Get("confirm")); confirm != "true" {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"confirm=true is required (R4.5): the route requires a confirmed UI action, so the "+
				"client must state the confirmation instead of the server assuming it"))
		return
	}

	// --- the R12.8 gate ---------------------------------------------------
	//
	// actionFor is active_queries.go:158's, shared rather than restated: the
	// list's `Action` column and this route's gate are one answer about one flag,
	// and R4.1's whole reason for rendering `disabled` is that the column must not
	// be blank while the action behind it is unbuilt. A column that says
	// `terminate` on a route that answers 403 — or the reverse — is the
	// second-copy failure this shared call prevents.
	if actionFor(s.cfg.Actions, ActionNameTerminate) == ActionDisabled {
		s.auditTerminate(pid, namespace, cluster, "", "refused: --allow-actions does not include terminate")
		writeErrorBody(w, http.StatusForbidden, dto.NewError(dto.ErrUnsupported,
			"--allow-actions does not include terminate (R4.5, R12.8): the action is gated off "+
				"in this process, so the route exists and declines"))
		return
	}

	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}

	// --- the call ---------------------------------------------------------
	terminated, err := s.terminateSource().TerminateSession(r.Context(), namespace, cluster, pid)
	if err != nil {
		// R4.5's replica refusal and the no-source case share a status and a
		// code and differ in their message, which is where R8.5 puts the
		// distinction. The replica branch is audited: a refused action is the
		// event an audit log exists to record, and a trail that only logs
		// successes cannot answer the question the UI asks next.
		if errors.Is(err, ErrNotPrimary) {
			s.auditTerminate(pid, namespace, cluster, "", err.Error())
			writeErrorBody(w, http.StatusForbidden, dto.NewError(dto.ErrUnsupported, err.Error()))
			return
		}
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("session termination is not available: %v", err)))
		return
	}

	// --- the audit line ---------------------------------------------------
	//
	// R4.5's "audited to a log line with actor and target", and the same three
	// values go into the body, so the line the operator reads and the body the
	// client reads are one read of one request. Info and not Debug: the default
	// level is Info, so an audit line logged at Debug — which is what every other
	// handler's s.logDebug call uses — would be compiled out of the default
	// deployment's log, and an audit trail that only exists at a non-default
	// verbosity is not an audit trail.
	actor, _ := IdentityFrom(r)
	s.auditTerminate(pid, namespace, cluster, actor.User,
		fmt.Sprintf("pg_terminate_backend(%d) returned %t", pid, terminated))

	writeJSON(w, http.StatusOK, dto.TerminateResponse{
		Pid:        pid,
		Terminated: terminated,
		Actor:      actor.User,
		Target:     namespace + "/" + cluster,
	})
}

// auditTerminate writes R4.5's audit line in the one shape every outcome of this
// route uses.
//
// It exists as a function rather than three slog calls because the line's field
// set is the requirement's (actor and target, plus the pid the path named and
// what happened) and a field set spelled out three times drifts — the same
// failure applyActions exists to prevent on the Action column, one level down.
// `actor` is empty on the two pre-auth-of-the-action paths only because the
// gate and the replica refusal happen before the handler has a result to
// attribute to it; the identity is still resolvable and logged as the empty
// string when the request never passed an authenticator, which is
// IdentityFrom's documented second return rather than a guess at a name.
func (s *Server) auditTerminate(pid int, namespace, cluster, actor, outcome string) {
	s.log.Info("session terminate",
		"route", SessionsTerminatePath,
		"pid", pid,
		"namespace", namespace,
		"cluster", cluster,
		"target", namespace+"/"+cluster,
		"actor", actor,
		"outcome", outcome,
	)
}
