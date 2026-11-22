package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/discovery"
)

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
// (limit/cursor/truncated). Both are implemented here rather than in the
// handler body's prose: parseClusterQuery is the whole of R8.4's parameter
// handling, pageClusters is the whole of R8.10's paging, and the handler is
// eleven lines that call them in order.

// R8.10's page-size pair. They are constants, not a Config field, because R8.10
// fixes them ("default 50, max 500") rather than making them tunable, and a
// per-deployment knob would let a cluster be configured into a shape the spec
// does not describe.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// Sort keys accepted by ?sort= (R8.4). The JSON-tag spelling is the query
// spelling, so `?sort=pgMajor` matches the field it sorts on. Every sort
// carries the row identity as its secondary key (see sortKey), which is
// R8.10's rule for query lists and sessions generalised to every list.
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

// ClusterQuery is the parsed, validated query string of
// GET /api/v1/clusters: R8.4's parameter set plus R8.10's cursor.
type ClusterQuery struct {
	Namespace string
	Cluster   string
	// DB, From and To are part of R8.4's uniform parameter set. This endpoint
	// has no per-database or time-axis meaning — openapi.yaml documents `db` as
	// "accepted and ignored on /clusters" — but they are still *validated*,
	// because a client that sends ?from=yesterday has a bug worth reporting
	// while one that sends ?db=x on this route is simply asking a question this
	// route cannot answer.
	DB   string
	From time.Time
	To   time.Time

	// Sort is the resolved key without its direction prefix; Desc is that
	// prefix. R8.4 names `?sort=` without spelling out the direction syntax,
	// so this follows the convention the rest of the API uses: a leading "-"
	// sorts descending.
	Sort string
	Desc bool

	Limit int
	// cursor is the decoded form of ?cursor=. It is unexported so a ClusterQuery
	// can only carry a cursor that was successfully parsed — the shape R8.10
	// promises is validated at the edge, not re-derived per page.
	cursor *pageCursor
}

// parseClusterQuery validates R8.4's parameters. Every rejection is an
// R8.5 InvalidRequest with a message naming the parameter, because the
// alternative — clamping or ignoring — turns a client bug into a silently
// different answer.
func parseClusterQuery(q url.Values) (ClusterQuery, error) {
	out := ClusterQuery{
		Namespace: q.Get("namespace"),
		Cluster:   q.Get("cluster"),
		DB:        q.Get("db"),
		Sort:      defaultSort,
		Limit:     DefaultLimit,
	}

	// limit: R8.10's default/max pair, and the spec is explicit that a value
	// above the maximum is InvalidRequest, "not a silent clamp, so a client that
	// asks for 5000 learns about it instead of getting 500".
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return out, fmt.Errorf("invalid limit: %q is not an integer", s)
		}
		if n < 1 {
			return out, fmt.Errorf("invalid limit: %d is below the minimum of 1", n)
		}
		if n > MaxLimit {
			return out, fmt.Errorf("invalid limit: %d exceeds the maximum of %d", n, MaxLimit)
		}
		out.Limit = n
	}

	// sort: the "-" prefix is the direction, the rest must be a known key.
	if s := q.Get("sort"); s != "" {
		sort, desc := s, false
		if strings.HasPrefix(sort, "-") {
			sort, desc = sort[1:], true
		}
		switch sort {
		case SortNamespace, SortName, SortPhase, SortInstances, SortPGMajor:
		default:
			return out, fmt.Errorf("invalid sort: %q (supported: %s, %s, %s, %s, %s; prefix \"-\" for descending)",
				s, SortNamespace, SortName, SortPhase, SortInstances, SortPGMajor)
		}
		out.Sort, out.Desc = sort, desc
	}

	// from/to: RFC 3339 per openapi's `format: date-time`. Ignored by this
	// endpoint's data, validated anyway (see the DB/From/To comment).
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"from", &out.From}, {"to", &out.To}} {
		s := q.Get(f.name)
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return out, fmt.Errorf("invalid %s: %q is not RFC 3339", f.name, s)
		}
		*f.dst = t.UTC()
	}

	// cursor: opaque to the client (R8.10), which is exactly why the server may
	// put whatever it needs in it — see pageCursor.
	if s := q.Get("cursor"); s != "" {
		c, err := decodeCursor(s)
		if err != nil {
			return out, fmt.Errorf("invalid cursor: %q is not a pgsi cursor", s)
		}
		// A cursor issued for another sort is meaningless here: page 2 of
		// ?sort=phase is not a subset of page 1 of ?sort=name, so accepting it
		// would silently skip or repeat rows. R8.10's "two consecutive pages
		// never overlap or drop a row" is what this check protects.
		if c.Sort != out.Sort {
			return out, fmt.Errorf("invalid cursor: issued for sort %q, not %q", c.Sort, out.Sort)
		}
		out.cursor = &c
	}

	return out, nil
}

// pageCursor is the decoded form of R8.10's opaque cursor.
//
// R8.10 says it is "base64 of the last sort key, not an offset", and that is
// what this is: the sort key of the last row of the previous page, plus the
// sort it belongs to. The sort is in there because a cursor is only meaningful
// relative to an ordering — see parseClusterQuery's rejection of a cursor
// issued for a different sort — and it is *not* an offset, so a page that
// shrinks between requests neither skips nor repeats the row a client already
// saw.
type pageCursor struct {
	Sort string `json:"s"`
	Key  string `json:"k"`
}

func encodeCursor(sort, key string) string {
	b, _ := json.Marshal(pageCursor{Sort: sort, Key: key})
	return base64.URLEncoding.EncodeToString(b)
}

func decodeCursor(raw string) (pageCursor, error) {
	// URLEncoding emits "=" padding; a client that strips it from a query
	// string is common enough that both spellings are accepted rather than
	// being a compatibility footnote.
	b, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		if b, err = base64.RawURLEncoding.DecodeString(raw); err != nil {
			return pageCursor{}, err
		}
	}
	var c pageCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return pageCursor{}, err
	}
	if c.Sort == "" || c.Key == "" {
		return pageCursor{}, fmt.Errorf("cursor missing its sort key")
	}
	return c, nil
}

// identity is a row's unique key: R8.4's two identity fields joined. It is the
// secondary key of every sort and the cursor's payload.
func identity(c dto.ClusterView) string { return c.Namespace + "/" + c.Name }

// sortKey returns the string a row sorts on for one key.
//
// Every key ends with the identity, which is what makes the ordering *total*:
// two clusters with the same phase, the same instance count, or (for a
// multi-cluster fleet that reuses names across namespaces) the same name still
// have a defined order. Without the tiebreaker, sort.Slice's unstable ordering
// would let two consecutive pages overlap or drop a row — the exact failure
// R8.10 names — so the tiebreaker lives in this one function instead of being
// re-implemented per sort.
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

// sortRows orders rows by q's key and direction, ascending then reversed.
func sortRows(rows []dto.ClusterView, key string, desc bool) {
	sort.Slice(rows, func(i, j int) bool { return sortKey(rows[i], key) < sortKey(rows[j], key) })
	if desc {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
}

// pageClusters filters, sorts and pages an inventory into R8.4's envelope.
//
// It is a function rather than a method so the whole of R8.10's paging is
// testable without an HTTP server, a kube client, or a discovery run.
func pageClusters(inv discovery.Inventory, q ClusterQuery) dto.Envelope[dto.ClusterView] {
	// Filter first: R8.4's ?namespace= and ?cluster= narrow within the set
	// discovery returned, and meta.total counts *matching* rows, so a filtered
	// page reports the size of the filtered set, not of the fleet.
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
	total := len(rows)

	sortRows(rows, q.Sort, q.Desc)

	// Keyset paging: drop everything at or before the cursor's key. Comparing
	// against the key (rather than looking the row up by identity) is what makes
	// a row deleted between requests harmless — the rows after it still come
	// next instead of the cursor 404ing.
	if q.cursor != nil {
		for len(rows) > 0 && !afterCursor(rows[0], q) {
			rows = rows[1:]
		}
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	truncated := len(rows) > limit
	page := rows
	if truncated {
		page = rows[:limit]
	}
	next := ""
	if truncated && len(page) > 0 {
		next = encodeCursor(q.Sort, sortKey(page[len(page)-1], q.Sort))
	}

	return dto.Envelope[dto.ClusterView]{
		Data: page,
		Meta: dto.Meta{
			Total:       total,
			Truncated:   truncated,
			GeneratedAt: inv.GeneratedAt,
			NextCursor:  next,
			// Failed and Loading are namespace-level (R8.4/R10.22), so they are
			// the inventory's, not the page's: a page of one namespace still
			// reports the other namespaces that failed to sync.
			Failed:  inv.Failed,
			Loading: inv.Loading,
		},
	}
}

// afterCursor reports whether a row belongs after the query's cursor, given the
// sort direction. Ascending means "greater than the cursor key"; descending
// mirrors it. Both directions are the same comparison on the same key, so the
// cursor cannot mean two different things depending on which page asked.
func afterCursor(c dto.ClusterView, q ClusterQuery) bool {
	k := sortKey(c, q.Sort)
	if q.Desc {
		return k < q.cursor.Key
	}
	return k > q.cursor.Key
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
