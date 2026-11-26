// GET /api/v1/poolers and GET /api/v1/poolers/{namespace}/{name} — R8.2's two
// pooler routes, and the API half of R6.
//
// # Which requirement asks for this, and what this route is not
//
// requirements.md:380 (R8.2) names both paths — `/poolers` and
// `/poolers/{ns}/{name}` — and until this commit their only home was
// api/x-planned-paths.yaml:141-142, under `r8.2-catalogue:`, where the entry
// carries the comment `# R6; PoolerView is already defined (R8.2 reuses it)`.
// That comment is the route's whole design note, and the type agrees:
// dto/clusters.go:363's doc says "The same shape backs GET /api/v1/poolers
// (R8.2), which is why Namespace is carried even though the nested copy sits
// under a cluster that already has one." The rows are therefore the poolers the
// inventory already carries, flattened out of `dto.ClusterView.Poolers`
// (dto/clusters.go:189), and the fixture is *derived* from
// testdata/api/clusters.json rather than authored beside it — R16.8's rule
// that a second hand-written copy of a shape the Go type owns is the defect,
// not the convenience.
//
// What this route does not serve is worth as much space, because R6's own text
// is about something else. R6.1 (requirements.md:354) renders pool state from
// `cnpg_pgbouncer_pools_*` (`cl_active`, `cl_waiting`, `sv_active`, …); R6.2
// (:355) renders pool utilisation and the client/server ratio; R6.3 (:356)
// renders throughput from `cnpg_pgbouncer_stats_*`. Those are metric families
// and `PoolerView` has no field for any of them — no `clWaiting`, no query
// count — so they arrive through the metrics surface (R9.4/R9.7's export plus
// internal/api/promql_catalogue.go), not through a resource route. What this
// file does serve is R6.5 (:358): "Surface pooler config (`poolMode`,
// `default_pool_size`, `max_client_conn`, `min_pool_size`, `type`) alongside
// the utilisation" — `PoolerView.PoolMode`, `.Parameters` and `.Type` are
// exactly that list. R6.4's attribution is a flag (`--pooler-attribution`),
// not a response field, and it belongs to R4.1's session list and R3.7's
// by-client rollup, both of which already exist.
//
// # Who builds it
//
// requirements.md:677 rows Phase 8 (R6.1–R6.5, R7.2, R7.4–R7.6, R5.9, R10.11)
// as A1's, and docs/agents.md:58 gives A2 `internal/api/*.go` except
// `server.go` and `dto/`. So this is A1's phase and A2's file — the same split
// active_queries.go records for Phase 6a, and for the same reason: the handler
// is the API surface, while the phase's other halves (the two collectors A3's
// second ruling at requirements.md:354 names, `pooler.go` for S5 and
// `pooler_clients.go` for S6) are A1's. The `mux.HandleFunc` lines are A1's
// too; the annotation entries, the two-line manifest deletion, the regenerated
// document and server_test.go's route lists are the half this file contributes
// to the commit that registers it.
//
// # Routing
//
// `/poolers` and `/poolers/{namespace}/{name}` share a prefix and are distinct
// patterns, so neither shadows the other: ServeMux matches the more specific
// pattern only when the two extra segments are present, and the list pattern
// matches only the bare path. The consequence for server_test.go is the move
// every registration repeats and cluster_detail.go:24 documents — a path in
// `paths` belongs in the 503 list (registered, so it answers) and a path only
// in `x-planned-paths` belongs in the 404 list (documented, so it is not the
// app shell). Both pooler URLs move on the day the route line lands.
package api

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/discovery"
)

// PoolersPath is R8.2's `/poolers`, and PoolerDetailPath is its
// `/poolers/{ns}/{name}` twin spelled the way the router spells a path
// variable — `{namespace}`, because R8.4's query parameter is `namespace` and
// R8.2's `{ns}` is prose shorthand. They are consts for the reason
// ClusterDetailPath is one: a mux pattern, a document key and a test URL that
// each spell a path their own way is how a route ends up documented under one
// name and served under another (R14.7).
const (
	PoolersPath      = "/api/v1/poolers"
	PoolerDetailPath = "/api/v1/poolers/{namespace}/{name}"
)

// Sort keys this route adds to the ones clusters.go already declares.
//
// `namespace`, `name`, `phase` and `instances` are reused from clusters.go's
// const block rather than re-declared: they spell the same strings, they sort
// the same-named fields of a different row type, and R16.8's one-copy rule is
// the reason a second `SortPhase = "phase"` in the same package would be a
// second answer to "what does ?sort=phase mean". What cannot be reused is the
// vocabulary itself — a pooler list has no `pgMajor` and a cluster list has no
// `poolMode` — which is why QuerySpec takes the list as data instead of
// pretending one vocabulary fits every route.
const (
	SortPoolerCluster  = "cluster"  // the owning cluster (R1.4's association)
	SortPoolerType     = "type"     // rw | ro (R6.5's config surface)
	SortPoolerPoolMode = "poolMode" // transaction | session | statement (R6.4's condition)
)

// defaultPoolerSort is namespace-then-name for the same two reasons
// defaultSort is for clusters: it is the order discovery produces (rebuild
// walks namespaces in sorted order and nests each cluster's poolers inside it),
// so an unfiltered first page is byte-identical to the fixture; and R10.11's
// pooler view is namespace-scoped, as every R10.24 surface is.
const defaultPoolerSort = SortNamespace

// poolersQuerySpec is this route's R8.4 vocabulary, handed to the shared
// parser. The const block above is the single source for both what `?sort=`
// accepts and what the rejection message lists.
var poolersQuerySpec = QuerySpec{
	Sorts: []string{
		SortNamespace, SortName, SortPoolerCluster, SortPoolerType,
		SortPoolerPoolMode, SortPhase, SortInstances,
	},
	Default: defaultPoolerSort,
}

// PoolersQuery is the parsed query string of GET /api/v1/poolers. It is an
// *alias* of ListQuery for the reason ClusterQuery is one: R8.4's parameter set
// is uniform across list endpoints by design, so a per-route struct would be a
// second copy of a contract the spec states once.
type PoolersQuery = ListQuery

// parsePoolersQuery validates this route's parameters through the shared parser.
func parsePoolersQuery(q url.Values) (PoolersQuery, error) {
	return parseListQuery(q, poolersQuerySpec)
}

// poolerIdentity is a pooler row's unique key: R8.4's two identity fields
// joined. It is every sort's secondary key and the cursor's payload, and it is
// why a pooler named `pooler` in two namespaces still has a defined order.
func poolerIdentity(p dto.PoolerView) string { return p.Namespace + "/" + p.Name }

// poolerSortKey returns the string a pooler row sorts on for one key, with the
// same total-order rule as clusters.go's sortKey: every key ends with the
// identity, so two poolers that share a phase, an instance count, or (the case
// a fleet actually has) a name in different namespaces still order
// deterministically. Without the tiebreaker, sort.Slice's unstable ordering
// lets two consecutive pages overlap or drop a row — R8.10's named failure —
// so it lives in this one function instead of being re-implemented per sort.
func poolerSortKey(p dto.PoolerView, key string) string {
	primary := poolerIdentity(p) // the default: namespace, then name
	switch key {
	case SortName:
		primary = p.Name
	case SortPoolerCluster:
		primary = p.Cluster
	case SortPoolerType:
		primary = p.Type
	case SortPoolerPoolMode:
		primary = p.PoolMode
	case SortPhase:
		primary = p.Phase
	case SortInstances:
		// Zero-padded so the string order matches the numeric order, which is
		// the only reason the shared pager can sort every resource with one
		// comparison.
		primary = fmt.Sprintf("%09d", p.Instances)
	}
	if key == SortNamespace {
		// primary is already the identity; appending it again would not change
		// the order but would put the namespace in the cursor twice.
		return primary
	}
	return primary + "|" + poolerIdentity(p)
}

// sortPoolers orders pooler rows by one key and direction — the concrete
// instantiation of the shared sortRowsBy, named so a test can call it per key.
func sortPoolers(rows []dto.PoolerView, key string, desc bool) {
	sortRowsBy(rows, poolerSortKey, key, desc)
}

// pagePoolers flattens, filters, sorts and pages an inventory into R8.4's
// envelope.
//
// The flatten is the route: R8.2's `/poolers` and dto's nested `Poolers` field
// are the same data seen from two sides, and this function is the only place
// the nesting is undone. Filtering happens on the *cluster* rather than on the
// pooler, because the inventory's nesting is the authority on which pooler
// belongs to which cluster — `PoolerView.Cluster` is a copy of that fact
// (dto/clusters.go:366's doc says so explicitly), and a pooler whose own copy
// disagrees is a DTO bug the fixture would catch, not something the filter
// should paper over by consulting two sources for one fact.
//
// Failed and Loading stay the inventory's, not the page's (R8.4/R10.22): a page
// of one namespace's poolers still reports the other namespaces that failed to
// sync.
func pagePoolers(inv discovery.Inventory, q PoolersQuery) dto.Envelope[dto.PoolerView] {
	var rows []dto.PoolerView
	for _, c := range inv.Clusters {
		if q.Namespace != "" && c.Namespace != q.Namespace {
			continue
		}
		if q.Cluster != "" && c.Name != q.Cluster {
			continue
		}
		rows = append(rows, c.Poolers...)
	}
	base := dto.Meta{
		GeneratedAt: inv.GeneratedAt,
		Failed:      inv.Failed,
		Loading:     inv.Loading,
	}
	return pageRows(rows, q, poolerSortKey, base)
}

// handlePoolers serves GET /api/v1/poolers.
//
// The three-step order is handleClusters' order and for the same reasons:
// parameters first (a malformed request is malformed whether or not discovery
// has synced, and answering 503 to it tells the client to retry something that
// will never succeed), readiness second (the same R8.7 value /readyz answers
// from, so the two cannot disagree), then the inventory, paged.
func (s *Server) handlePoolers(w http.ResponseWriter, r *http.Request) {
	q, err := parsePoolersQuery(r.URL.Query())
	if err != nil {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	if !s.readiness.Ready() {
		// openapi.yaml's documented example for NotReady, verbatim, so the
		// documented and the emitted body for this code agree — and so this
		// route cannot describe the state in a third way.
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	inv := s.fleetInventory()
	env := pagePoolers(inv, q)
	s.logDebug("poolers served",
		"route", PoolersPath,
		"rows", len(env.Data),
		"total", env.Meta.Total,
		"truncated", env.Meta.Truncated,
		"failedNamespaces", len(env.Meta.Failed),
	)
	writeJSON(w, http.StatusOK, env)
}

// handlePoolerDetail serves GET /api/v1/poolers/{namespace}/{name}.
//
// It answers with a bare dto.PoolerView, not an envelope, for the reason
// handleClusterDetail gives for its own body: R8.4's `{data, meta}` contract is
// stated for list endpoints, and an envelope around one object would ask
// `meta.total` to be 1 and `meta.truncated` to be false — two fields carrying
// no information — while `meta.failed[]` would describe namespaces the response
// is not summarising.
//
// The four-step order is the detail route's, and the fourth step is the one
// that keeps a 404 honest: a pooler is not found in a namespace whose discovery
// read *errored* is not a deleted pooler, so a failed namespace answers 503
// Unavailable (with the failure's own message) and a still-loading one answers
// 503 NotReady — the same two R8.5 codes the cluster detail route uses for the
// same two facts, reused here rather than reworded.
func (s *Server) handlePoolerDetail(w http.ResponseWriter, r *http.Request) {
	// ServeMux guarantees both variables are non-empty for a path that matches
	// the pattern; the check is the guard for the handler being called directly,
	// which is how poolers_test.go exercises it (the mux is A1's file).
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if ns == "" || name == "" {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"namespace and name are both required"))
		return
	}
	// A path segment arrives percent-decoded by net/http, but a handler called
	// directly can still see an escaped name; rejecting it beats serving a
	// pooler whose name contains a literal %2F.
	if ns != url.PathEscape(ns) || name != url.PathEscape(name) {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest,
			"namespace and name must not be percent-encoded"))
		return
	}

	if !s.readiness.Ready() {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}

	inv := s.fleetInventory()
	view, ok := lookupPooler(inv, ns, name)
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
	writeErrorBody(w, http.StatusNotFound, dto.NewError(dto.ErrNotFound,
		"no pooler "+ns+"/"+name+" in the fleet inventory"))
}

// lookupPooler finds one pooler by identity in an inventory.
//
// It walks the clusters and their nested poolers rather than keeping an index,
// for the reason lookupCluster does: discovery.Inventory exposes sorted slices,
// not keyed maps. A pooler that is nested under a cluster but whose own
// `Namespace` field names a different namespace is found here by its *parent's*
// namespace — the nesting is the authority, and the detail route's URL is
// therefore always the namespace the inventory put it in.
func lookupPooler(inv discovery.Inventory, ns, name string) (dto.PoolerView, bool) {
	for _, c := range inv.Clusters {
		if c.Namespace != ns {
			continue
		}
		for _, p := range c.Poolers {
			if p.Name == name {
				return p, true
			}
		}
	}
	return dto.PoolerView{}, false
}
