// GET /api/v1/clusters — R8.2's `/clusters`, the fleet inventory rendered from
// the frozen dto.Envelope[dto.ClusterView] contract.
//
// The contract (dto/clusters.go, openapi.yaml, testdata/api/clusters.json) was
// frozen before this handler existed so Phase 1b could build the Fleet page
// against it; this file is the implementation that has to match it. The
// byte-level assertion lives in clusters_test.go, which serves the fixture
// through this handler and compares canonical forms — so a field the handler
// drops, renames or reorders fails CI, which is the class of bug R16.8 exists
// to catch.
//
// The endpoint's own contract is R8.4 (the parameter set + envelope) and R8.10
// (limit/cursor/truncated). Both live in paging.go now, because R8.4 states
// them once for *every* list endpoint and R8.2 names eighteen of them: this
// file keeps the cluster-specific answers — which sort keys exist, what a row's
// identity is, which rows a filter matches — and calls the shared parser and
// pager for everything else. The handler is still the eleven lines that parse,
// gate on readiness, and page.

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/discovery"
)

// Sort keys accepted by ?sort= on this route (R8.4). The JSON-tag spelling is
// the query spelling, so `?sort=pgMajor` matches the field it sorts on. Every
// sort carries the row identity as its secondary key (see sortKey), which is
// R8.10's rule for query lists and sessions generalised to every list.
//
// The list is per-route data: a cluster list sorts by phase and pgMajor, a
// query list by total_exec_time, a session list by pid. R8.10's machinery is
// shared; the vocabulary of *what you can sort by* is not, and pretending it
// was shared is how a generic ends up with a field per resource.
const (
	SortNamespace = "namespace" // the default: the row identity's first half
	SortName      = "name"
	SortPhase     = "phase"
	SortInstances = "instances"
	SortPGMajor   = "pgMajor"
)

// defaultSort is namespace-then-name, i.e. the row identity. Two reasons: it is
// the order discovery itself produces (rebuild walks namespaces in sorted
// order), so an unfiltered first page is byte-identical to the fixture; and a
// fleet UI is organised by namespace (R10.24's switcher is namespace-scoped).
// `?sort=name` is the alternative the Fleet table's Name column sorts by.
const defaultSort = SortNamespace

// clusterQuerySpec is this route's R8.4 vocabulary, handed to the shared parser.
// It exists so `?sort=` acceptance and the error message that lists the
// supported keys come from one place: the const block above.
var clusterQuerySpec = QuerySpec{
	Sorts:   []string{SortNamespace, SortName, SortPhase, SortInstances, SortPGMajor},
	Default: defaultSort,
}

// ClusterQuery is the parsed, validated query string of
// GET /api/v1/clusters: R8.4's parameter set plus R8.10's cursor.
//
// It is an *alias* of ListQuery rather than a new struct. R8.4's parameter set
// is uniform across list endpoints by design, so a per-route struct would be a
// second copy of a contract the spec states once — and the failure mode of
// per-route copies here is not theoretical: the UI's types were written from
// spec prose instead of imported, disagreed in every nested type, and nothing
// went red. The alias keeps the name this route's callers and tests use
// (`parseClusterQuery`'s return type, clusters_test.go's `mustQuery`) while
// there is exactly one set of fields to keep in step.
type ClusterQuery = ListQuery

// parseClusterQuery validates this route's parameters through the shared
// parser. Three lines, and the third is the whole reason the route keeps a
// named wrapper: the route's vocabulary is passed in here, so a sort key added
// to the const block is accepted by the parser and named in the error message
// in the same edit.
func parseClusterQuery(q url.Values) (ClusterQuery, error) {
	return parseListQuery(q, clusterQuerySpec)
}

// identity is a row's unique key: R8.4's two identity fields joined. It is the
// secondary key of every sort and the cursor's payload.
func identity(c dto.ClusterView) string { return c.Namespace + "/" + c.Name }

// sortKey returns the string a cluster row sorts on for one key.
//
// Every key ends with the identity, which is what makes the ordering *total*:
// two clusters with the same phase, the same instance count, or (for a
// multi-cluster fleet that reuses names across namespaces) the same name still
// have a defined order. Without the tiebreaker, sort.Slice's unstable ordering
// would let two consecutive pages overlap or drop a row — the exact failure
// R8.10 names — so the tiebreaker lives in this one function instead of being
// re-implemented per sort.
//
// This is the per-resource half of R8.10: the pager is generic over the row
// type and receives this function as a value, so "how a row sorts" stays a
// cluster answer while "how a page is cut" stays a shared one.
func sortKey(c dto.ClusterView, key string) string {
	primary := identity(c) // the default: namespace, then name
	switch key {
	case SortName:
		primary = c.Name
	case SortPhase:
		primary = c.Phase
	case SortInstances:
		primary = fmt.Sprintf("%09d", c.Instances)
	case SortPGMajor:
		// A nil pgMajor (R1.3: neither source has answered yet) becomes 0 and so
		// sorts first ascending, last descending — the same "unknown is not a
		// real version" stance the DTO takes by making the field nullable.
		major := 0
		if c.PGMajor != nil {
			major = *c.PGMajor
		}
		primary = fmt.Sprintf("%09d", major)
	}
	if key == SortNamespace {
		// primary is already the identity; appending it again would not change
		// the order but would put the namespace in the cursor twice.
		return primary
	}
	return primary + "|" + identity(c)
}

// sortRows orders cluster rows by one key and direction. It is the concrete
// instantiation of the shared sortRowsBy, kept as a named function because
// clusters_test.go's TestSortKeys calls it directly for each of the five keys.
func sortRows(rows []dto.ClusterView, key string, desc bool) {
	sortRowsBy(rows, sortKey, key, desc)
}

// pageClusters filters, sorts and pages an inventory into R8.4's envelope.
//
// It is a function rather than a method so the whole of R8.10's paging is
// testable without an HTTP server, a kube client, or a discovery run.
func pageClusters(inv discovery.Inventory, q ClusterQuery) dto.Envelope[dto.ClusterView] {
	// Filter first: R8.4's ?namespace= and ?cluster= narrow within the set
	// discovery returned, and meta.total counts *matching* rows, so a filtered
	// page reports the size of the filtered set, not of the fleet. Filtering
	// stays here because it reads cluster fields; paging is the shared part.
	rows := make([]dto.ClusterView, 0, len(inv.Clusters))
	for _, c := range inv.Clusters {
		if q.Namespace != "" && c.Namespace != q.Namespace {
			continue
		}
		if q.Cluster != "" && c.Name != q.Cluster {
			continue
		}
		rows = append(rows, c)
	}

	// Failed and Loading are namespace-level (R8.4/R10.22), so they are the
	// inventory's, not the page's: a page of one namespace still reports the
	// other namespaces that failed to sync.
	base := dto.Meta{
		GeneratedAt: inv.GeneratedAt,
		Failed:      inv.Failed,
		Loading:     inv.Loading,
	}
	return pageRows(rows, q, sortKey, base)
}

// handleClusters serves GET /api/v1/clusters.
//
// The order of the three checks is the contract:
//
//  1. Parameters first. A malformed request is malformed whether or not
//     discovery has synced, and answering 503 to it would tell the client to
//     retry something that will never succeed.
//  2. Readiness second, through the same Readiness the /readyz probe answers
//     from (R8.7(i)). This is the R8.5 ErrNotReady path openapi.yaml documents
//     for this route: 503 with code NotReady rather than a 200 whose empty
//     `data` looks like an empty fleet.
//  3. Then the inventory, paged.
func (s *Server) handleClusters(w http.ResponseWriter, r *http.Request) {
	q, err := parseClusterQuery(r.URL.Query())
	if err != nil {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	if !s.readiness.Ready() {
		// The message is openapi.yaml's documented example, verbatim, so the
		// documented and the emitted body for this code agree.
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	inv := s.fleetInventory()
	env := pageClusters(inv, q)
	s.logDebug("clusters served",
		"route", "/api/v1/clusters",
		"rows", len(env.Data),
		"total", env.Meta.Total,
		"truncated", env.Meta.Truncated,
		"failedNamespaces", len(env.Meta.Failed),
	)
	writeJSON(w, http.StatusOK, env)
}

// fleetInventory returns the inventory this process serves. A Server with no
// fleet source (Phase 0's shape, and D9's reserved `api` role until the Phase 2
// store lands) yields the empty inventory rather than dereferencing nil — and
// because New() gives such a server a Readiness with no sources, the readiness
// check above is what answers for it in practice: the handler's nil branch is
// reachable only for a Server whose readiness was supplied by something else.
func (s *Server) fleetInventory() discovery.Inventory {
	if s.cfg.Fleet == nil {
		return discovery.Inventory{}
	}
	return s.cfg.Fleet.Inventory()
}

// writeJSON is the package's single JSON response path (R8.1's "JSON" and
// R8.5's one error shape). Compact on the wire — the fixture's two-space
// indentation is a *file* formatting rule that keeps it diffable
// (dto.TestGoldenFixtureIsCanonical enforces it), not a wire contract, and
// R10.14's payload budget is served by not shipping whitespace.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErrorBody renders an R8.5 ErrorBody with a status. auth.go's
// writeAuthError is the 401/403 twin of this — it adds WWW-Authenticate and
// then calls here, so every error body the package emits is built by
// dto.NewError and encoded one way.
func writeErrorBody(w http.ResponseWriter, status int, body dto.ErrorBody) {
	writeJSON(w, status, body)
}
