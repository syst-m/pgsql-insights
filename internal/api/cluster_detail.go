// GET /api/v1/clusters/{namespace}/{name} — R8.2's second core route.
//
// R8.2's catalogue names twenty routes; `discovery.Inventory` carries
// Clusters, Failed, Loading and GeneratedAt and nothing else, so the routes
// this package can serve out of it are this file and poolers.go (the two
// routes whose data the inventory already holds), and the rest of
// api/x-planned-paths.yaml is gated on data sources (the query store, the
// PG-side collectors, the metrics catalogue), not on handler code — a route
// written against data that does not exist yet answers 200 with an empty body
// and turns "not collected" into "none", which is the failure R8.7 already
// rules out for the list route.
//
// The sentence this replaces counted the manifest ("of the eighteen still
// planned, this is the only one…") and listed the poolers among the gated
// routes. Both halves were falsified by poolers.go landing, and a count of
// another file's lines is a second copy of a contract that file owns: the
// manifest's own contents are the artifact, so the paragraph now names the two
// handler files and points at the yaml instead of restating how many entries it
// has.
//
// The response is a bare dto.ClusterView, not an envelope. R8.4's `{data, meta}`
// contract is stated for *list* endpoints ("Every list endpoint supports
// ?namespace= … and returns a stable envelope"), and an envelope around one
// object would ask `meta.total` to be 1 and `meta.truncated` to be false — two
// fields carrying no information — while `meta.failed[]` would describe
// namespaces the response is not summarising. The alternative, minting a
// ClusterDetailEnvelope, would be a fourth name for a shape the spec does not
// name; the doc already has the type, and the route points at it.
//
// The route is registered in server.go (A1's file) as
// `mux.HandleFunc("GET /api/v1/clusters/{namespace}/{name}", …)`. Registering it
// moved `/api/v1/clusters/default/pg1` out of server_test.go's 404 list and into
// its 503 list in the same commit; the test had named the URL as the *planned*
// one it expected to disappear from the 404 list when Phase 5 landed it, and
// that is what happened. The rule the two lists encode is worth stating because
// every future route in `x-planned-paths` repeats the move: a path in `paths`
// belongs in the 503 list (registered, so it answers), and a path only in
// `x-planned-paths` belongs in the 404 list (documented, so it is not the SPA).

package api

import (
	"net/http"
	"net/url"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/discovery"
)

// ClusterDetailPath is R8.2's `/clusters/{ns}/{name}` spelled the way the
// router and the document spell it. R8.4's parameter is `namespace`, so the
// path variable is `{namespace}` rather than R8.2's shorthand `{ns}` — the
// same reason the list route's query parameter and the doc's components
// parameter agree on the long form.
//
// It is a const for the reason QuerySeriesPath is one: a //go:embed pattern and
// a mux pattern and a test URL that each spell a path their own way is how a
// route ends up documented under one name and served under another (R14.7).
const ClusterDetailPath = "/api/v1/clusters/{namespace}/{name}"

// handleClusterDetail serves GET /api/v1/clusters/{namespace}/{name}.
//
// The three-step order is handleClusters' order, and for the same reasons:
// parse the path first (a malformed request is malformed whether or not
// discovery has synced), then gate on the single R8.7 readiness value, then
// read the inventory.
//
// The fourth step is the one the list route does not have, and it is where
// R8.4's partial-render contract meets a route that has no `meta` to carry it
// in. Asking for a cluster in a namespace whose discovery read errored has two
// candidate answers, and only one of them is true:
//
//   - 404 NotFound — "there is no such cluster". That is a lie: the inventory
//     is missing the whole namespace, not the cluster, and a client told 404
//     will conclude the cluster was deleted.
//   - 503 Unavailable — "a dependency is down", which is R8.5's own definition
//     for exactly this state, and is what the list route reports through
//     meta.failed[] for the same namespace.
//
// So a namespace in meta.failed[] answers 503 Unavailable with the failure's
// own message, and a namespace still in meta.loading[] answers 503 NotReady —
// both of them the same R8.5 code the list route uses for the same fact, which
// is why the answer is reachable without adding a single new error code. The
// spec states this contract for list endpoints only; applying it to the detail
// route is a decision, recorded here rather than in a comment, and flagged to
// A3 as a spec-silent choice.
func (s *Server) handleClusterDetail(w http.ResponseWriter, r *http.Request) {
	// ServeMux guarantees both variables are non-empty for a path that matches
	// this pattern, so the empty check is not router paranoia — it is the
	// guard for a Server built without a pattern, i.e. for the handler being
	// called directly, which is how the tests exercise it.
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if ns == "" || name == "" {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"namespace and name are both required"))
		return
	}
	// A path segment arrives percent-decoded by net/http, but a handler called
	// directly (and any future router that does not decode) can still see an
	// escaped name. Rejecting it beats serving a cluster whose name contains a
	// literal %2F.
	if ns != url.PathEscape(ns) || name != url.PathEscape(name) {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"namespace and name must not be percent-encoded"))
		return
	}

	if !s.readiness.Ready() {
		// The same message the list route emits, so the two routes cannot
		// describe the same state in two different ways (R8.7's single signal).
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}

	inv := s.fleetInventory()
	view, ok := lookupCluster(inv, ns, name)
	if ok {
		writeJSON(w, http.StatusOK, view)
		return
	}

	// Not in the inventory. Before answering 404, ask whether the inventory is
	// even qualified to answer about this namespace.
	if msg, failed := failedNamespace(inv, ns); failed {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, "namespace "+ns+" failed to sync", msg))
		return
	}
	if loadingNamespace(inv, ns) {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "namespace "+ns+" is still loading"))
		return
	}
	// The namespace synced and the cluster is not in it: the only case where
	// 404 is the truth.
	writeErrorBody(w, http.StatusNotFound, dto.NewError(dto.ErrNotFound,
		"no cluster "+ns+"/"+name+" in the fleet inventory"))
}

// lookupCluster finds one cluster by identity in an inventory.
//
// It is a linear scan rather than a map lookup because discovery.Inventory
// exposes a sorted slice, not a keyed map (the keyed map lives inside
// Discoverer, and Inventory() flattens it). At R12's fleet sizes that is the
// right trade; if the inventory ever grows a lookup index, this is the one
// function that changes and every caller keeps working.
func lookupCluster(inv discovery.Inventory, ns, name string) (dto.ClusterView, bool) {
	for _, c := range inv.Clusters {
		if c.Namespace == ns && c.Name == name {
			return c, true
		}
	}
	return dto.ClusterView{}, false
}

// failedNamespace returns the failure message recorded for a namespace, if the
// inventory carries one. R8.4's NamespaceFailure already holds the message and
// the R8.5 code, so the detail route reuses both rather than rewording them.
func failedNamespace(inv discovery.Inventory, ns string) (string, bool) {
	for _, f := range inv.Failed {
		if f.Namespace == ns {
			return f.Message, true
		}
	}
	return "", false
}

// loadingNamespace reports whether a namespace is still listed in
// meta.loading[] — the informer has not synced, so the inventory has no
// authority over anything in that namespace yet.
func loadingNamespace(inv discovery.Inventory, ns string) bool {
	for _, l := range inv.Loading {
		if l == ns {
			return true
		}
	}
	return false
}
