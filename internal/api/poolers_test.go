// Tests for R8.2's pooler routes — GET /api/v1/poolers and
// GET /api/v1/poolers/{namespace}/{name}.
//
// Two risks in this pair, and the file is built around them rather than around
// line coverage:
//
//   - the *flatten*. `/poolers` has no data of its own: its rows are
//     `dto.ClusterView.Poolers` (dto/clusters.json's own field) flattened out of
//     the nesting, so the route can be right about the flattening and wrong about
//     which pooler belongs to which cluster, or right about the rows and wrong
//     about the count. TestPoolersServesTheFrozenFixture pins both at once by
//     building the server's inventory from testdata/api/clusters.json and
//     byte-comparing the answer to testdata/api/poolers.json — which is also the
//     only thing that keeps the two fixtures from drifting apart, since one is
//     generated from the other (R16.8's one-copy rule).
//   - the *doc pairing*. R14.7 forbids a route in code the document does not list
//     and a documented route that 404s, and the two halves of this route's
//     landing are two commits: the handler is internal/api/poolers.go (A2's),
//     while the `mux.HandleFunc` lines, the annotation entries, the manifest
//     deletion and the regenerated api/openapi.yaml are the commit that registers
//     it. A test that hard-codes one side — which is what
//     active_queries_test.go's TestActiveQueriesRouteIsDocumented can afford,
//     because that route's handler and registration shipped together — would pin
//     the pair to a single commit. TestPoolersRouteIsDocumented asserts the
//     invariant instead of the snapshot: the path is under `paths:` XOR in
//     `x-planned-paths:`, and if it is under `paths:` it must carry
//     `x-status: implemented`. That is true before the route line lands (planned)
//     and after (implemented), and false in the one state R8.1c calls a defect.
//
// The rest covers what the two handlers can answer: 200 from a fleet with rows,
// 200 from an empty one (as `data: []`), 400 for each parameter failure checked
// before readiness, 503 for not-ready, and on the detail route 404 / 503
// Unavailable / 503 NotReady for the three ways a namespace can fail to answer.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	openapi "github.com/frankellis/pgsql-insights/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/discovery"
)

// poolersFixturePath is R16.8's named contract location for this route
// (repo-root testdata/api/), reached from this package's directory the way
// fixturePath and activeQueriesFixturePath are.
const poolersFixturePath = "../../testdata/api/poolers.json"

// loadPoolersFixture reads the route's envelope plus its raw bytes. The handler
// rebuilds `meta` from the inventory on every request, so comparing the whole
// document — not just `data` — is what proves the route's own meta agrees with
// the contract it publishes.
func loadPoolersFixture(t *testing.T) (dto.Envelope[dto.PoolerView], []byte) {
	t.Helper()
	raw, err := os.ReadFile(poolersFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", poolersFixturePath, err)
	}
	var env dto.Envelope[dto.PoolerView]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse %s: %v\n%s", poolersFixturePath, err, raw)
	}
	return env, raw
}

// servePoolersQuery calls handlePoolers directly rather than going through
// s.Handler().
//
// do() (server_test.go) drives the mux, and the mux is server.go — A1's file,
// where the route line lands in the commit that registers the route. A test that
// asked the mux for /api/v1/poolers before that line exists would be asserting
// the registration from the wrong commit and would fail for a reason this file
// cannot fix. The handler-level call is also what clusters_test.go falls back to
// for everything that isn't a route assertion.
func servePoolersQuery(t *testing.T, s *Server, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	target := PoolersPath
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	rec := httptest.NewRecorder()
	s.handlePoolers(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// getPoolers serves the query and decodes the envelope, failing on anything but
// a 200 — the shape tags_test.go's getTags uses, so a non-200 prints the body
// instead of hiding it behind a decode error.
func getPoolers(t *testing.T, s *Server, rawQuery string) dto.Envelope[dto.PoolerView] {
	t.Helper()
	rec := servePoolersQuery(t, s, rawQuery)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s?%s = %d, want 200\nbody: %s", PoolersPath, rawQuery, rec.Code, rec.Body.String())
	}
	var env dto.Envelope[dto.PoolerView]
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s?%s: decode body: %v\nbody: %s", PoolersPath, rawQuery, err, rec.Body.String())
	}
	return env
}

// servePoolerDetail calls handlePoolerDetail directly, setting the two path
// variables the mux would have set. httptest.NewRequest has no way to *match* a
// pattern, so SetPathValue is what stands in for the router's own extraction —
// and it is also the only way to hand the handler a value the router would never
// produce, which is what the percent-encoding test needs.
func servePoolerDetail(t *testing.T, s *Server, ns, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, PoolerDetailPath, nil)
	req.SetPathValue("namespace", ns)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	s.handlePoolerDetail(rec, req)
	return rec
}

// mustPoolersQuery parses a raw query string through *this route's* vocabulary.
// mustQuery (clusters_test.go) is typed to the cluster spec, so it rejects
// `sort=poolMode` — the per-route vocabulary is per-route, which is the whole
// reason QuerySpec is a parameter rather than a field of ListQuery.
func mustPoolersQuery(t *testing.T, raw string) PoolersQuery {
	t.Helper()
	u, err := url.Parse("http://x/?" + raw)
	if err != nil {
		t.Fatalf("parse query %q: %v", raw, err)
	}
	q, err := parsePoolersQuery(u.Query())
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return q
}

// poolerFixtureInventory is the inventory the poolers fixture describes: the
// same four clusters clusters.json commits to, so the server under test holds
// exactly the poolers the contract says the fleet has.
func poolerFixtureInventory(t *testing.T) discovery.Inventory {
	t.Helper()
	cenv, _ := loadFixture(t)
	return discovery.Inventory{
		Clusters:    cenv.Data,
		Failed:      cenv.Meta.Failed,
		Loading:     cenv.Meta.Loading,
		GeneratedAt: cenv.Meta.GeneratedAt,
	}
}

// ---------------------------------------------------------------------------
// list route
// ---------------------------------------------------------------------------

func TestPoolersServesTheFrozenFixture(t *testing.T) {
	// The route's own R16.8 cross-check, and the cross-fixture one: the server's
	// inventory comes from clusters.json, the answer is compared to poolers.json,
	// and the two fixtures agree only if the poolers nested in the clusters are
	// the poolers the route publishes. A pooler added to one file and not the
	// other fails here with a line diff naming both sides.
	penv, praw := loadPoolersFixture(t)
	s := newFleetServer(t, poolerFixtureInventory(t))

	rec := servePoolersQuery(t, s, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200; body %s", PoolersPath, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}
	var got dto.Envelope[dto.PoolerView]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode handler body: %v\n%s", err, rec.Body.String())
	}
	if diff := firstDiff(canonical(t, got), string(praw)); diff != "" {
		t.Errorf("handler output is not the frozen fixture:\n%s", diff)
	}
	if len(penv.Data) == 0 {
		t.Fatalf("%s has no rows", poolersFixturePath)
	}
}

func TestPoolersRouteIsDocumented(t *testing.T) {
	// The invariant, not the snapshot — see the file header.
	raw, err := openapi.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	pathsAt := strings.Index(doc, "\npaths:\n")
	plannedAt := strings.Index(doc, "\nx-planned-paths:\n")
	if pathsAt < 0 || plannedAt < 0 {
		t.Fatal("document has no paths: or x-planned-paths: section")
	}
	paths, planned := doc[pathsAt:plannedAt], doc[plannedAt:]

	for _, p := range []string{PoolersPath, PoolerDetailPath} {
		inPaths := strings.Contains(paths, "  "+p+":\n")
		inManifest := false
		for _, line := range strings.Split(planned, "\n") {
			// A manifest entry is a bare path with an optional trailing comment
			// (api/x-planned-paths.yaml:28-33: "Entry form — a bare path string…
			// Set membership is the whole claim"), so the comment is stripped
			// before comparing rather than matched through.
			entry := strings.TrimSpace(line)
			if i := strings.Index(entry, "#"); i >= 0 {
				entry = strings.TrimRight(entry[:i], " ")
			}
			if entry == "- "+p {
				inManifest = true
			}
		}

		switch {
		case inPaths && inManifest:
			t.Errorf("%s is under paths: *and* in x-planned-paths: — R8.1c's "+
				"disjointness rule makes a route in both a defect", p)
		case !inPaths && !inManifest:
			t.Errorf("%s is in neither paths: nor x-planned-paths: — the route is "+
				"either documented as implemented or planned, and it is neither", p)
		case inPaths:
			// Implemented: the annotation entry must say so, which is what
			// checkRouteTable's phantom direction reads.
			at := strings.Index(paths, "  "+p+":\n")
			item := paths[at:]
			if next := strings.Index(item[2:], "\n  /"); next >= 0 {
				item = item[:next+2]
			}
			if !strings.Contains(item, "x-status: implemented") {
				t.Errorf("%s has no `x-status: implemented`", p)
			}
		}
	}
}

func TestPoolersQueryValidation(t *testing.T) {
	// Every rejection is R8.5 InvalidRequest with a message naming the parameter,
	// and the supported-sort list in the message is generated from
	// poolersQuerySpec — so this table is also the assertion that the route's
	// documented vocabulary is the one const block, not a string kept in the
	// handler.
	const sorts = "namespace, name, cluster, type, poolMode, phase, instances"
	tests := []struct {
		query string
		want  string
	}{
		{"limit=0", "invalid limit: 0 is below the minimum of 1"},
		{"limit=5000", "invalid limit: 5000 exceeds the maximum of 500"},
		{"limit=abc", `invalid limit: "abc" is not an integer`},
		{"sort=bogus", `invalid sort: "bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{"sort=-bogus", `invalid sort: "-bogus" (supported: ` + sorts + `; prefix "-" for descending)`},
		{"sort=pgMajor", `invalid sort: "pgMajor" (supported: ` + sorts + `; prefix "-" for descending)`},
		{"from=yesterday", `invalid from: "yesterday" is not RFC 3339`},
		{"to=2026-13-40", `invalid to: "2026-13-40" is not RFC 3339`},
		{"cursor=not-a-cursor", `invalid cursor: "not-a-cursor" is not a pgsi cursor`},
	}
	s := newFleetServer(t, poolerFixtureInventory(t))
	for _, tt := range tests {
		rec := servePoolersQuery(t, s, tt.query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s?%s = %d, want 400", PoolersPath, tt.query, rec.Code)
			continue
		}
		var body dto.ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("GET %s?%s: decode: %v", PoolersPath, tt.query, err)
			continue
		}
		if body.Error.Code != dto.ErrInvalidRequest || body.Error.Message != tt.want {
			t.Errorf("GET %s?%s\n got %+v\nwant InvalidRequest %q", PoolersPath, tt.query, body.Error, tt.want)
		}
	}
}

func TestPoolersQueryAccepts(t *testing.T) {
	// The other half of R8.4's parameter set: what must *not* be rejected,
	// including `db` on a route with no per-database meaning (openapi documents it
	// as accepted and ignored on the routes that have no db axis) and every sort
	// key the spec block declares, in both directions.
	for _, raw := range []string{
		"", "limit=1", "limit=500", "db=app", "from=2026-11-20T00:00:00Z",
		"to=2026-11-20T00:00:00Z", "namespace=ai-api-gateway", "cluster=rev-proxy-pg",
		"sort=namespace", "sort=-namespace", "sort=name", "sort=-name",
		"sort=cluster", "sort=-cluster", "sort=type", "sort=-type",
		"sort=poolMode", "sort=-poolMode", "sort=phase", "sort=-phase",
		"sort=instances", "sort=-instances",
	} {
		if _, err := parsePoolersQuery(mustURLQuery(t, raw)); err != nil {
			t.Errorf("?%s rejected: %v", raw, err)
		}
	}
}

// mustURLQuery is the url.Values half of mustPoolersQuery, split out so the
// accept table can test the parser without the parse-twice round trip.
func mustURLQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse("http://x/?" + raw)
	if err != nil {
		t.Fatalf("parse query %q: %v", raw, err)
	}
	return u.Query()
}

// poolerRows is a three-row set built to make each sort's answer unique,
// including the two cases a per-route sortKey gets wrong most often: two poolers
// sharing a name across namespaces (the `|identity` tiebreak) and an instance
// count that would mis-order if the key were the decimal string rather than the
// zero-padded one.
func poolerRows() []dto.PoolerView {
	return []dto.PoolerView{
		{ // A
			Name: "alpha", Namespace: "ns-a", Cluster: "cluster-a",
			Type: "rw", PoolMode: "transaction", Phase: "active", Instances: 2, ReadyInstances: 2,
		},
		{ // B
			Name: "beta", Namespace: "ns-a", Cluster: "cluster-a",
			Type: "rw", PoolMode: "transaction", Phase: "unready", Instances: 0,
		},
		{ // C
			Name: "alpha", Namespace: "ns-b", Cluster: "cluster-b",
			Type: "ro", PoolMode: "session", Phase: "active", Instances: 1, ReadyInstances: 1,
		},
	}
}

func TestPoolerSortKeys(t *testing.T) {
	// Each key's expected order, spelled out rather than re-derived from the
	// function under test: an assertion computed from the same function it checks
	// cannot fail, and the failure this pins is a key reading the wrong field
	// (sort=name answering with the namespace), which a self-derived expectation
	// would agree with.
	tests := []struct {
		key  string
		want []string // "namespace/name" in order
	}{
		{SortNamespace, []string{"ns-a/alpha", "ns-a/beta", "ns-b/alpha"}},
		{SortName, []string{"ns-a/alpha", "ns-b/alpha", "ns-a/beta"}}, // alpha|ns-a < alpha|ns-b: the tiebreak
		{SortPoolerCluster, []string{"ns-a/alpha", "ns-a/beta", "ns-b/alpha"}},
		{SortPoolerType, []string{"ns-b/alpha", "ns-a/alpha", "ns-a/beta"}}, // ro before rw
		{SortPoolerPoolMode, []string{"ns-b/alpha", "ns-a/alpha", "ns-a/beta"}},
		{SortPhase, []string{"ns-a/alpha", "ns-b/alpha", "ns-a/beta"}}, // active, active, unready
		{SortInstances, []string{"ns-a/beta", "ns-b/alpha", "ns-a/alpha"}},
	}
	rows := poolerRows()
	for _, tt := range tests {
		got := append([]dto.PoolerView(nil), rows...)
		sortPoolers(got, tt.key, false)
		var order []string
		for _, p := range got {
			order = append(order, poolerIdentity(p))
		}
		if strings.Join(order, ",") != strings.Join(tt.want, ",") {
			t.Errorf("sort=%s gave %v, want %v", tt.key, order, tt.want)
		}
	}

	// Descending is the same order reversed, which is the assertion that Desc is
	// threaded through sortRowsBy rather than applied per key.
	desc := append([]dto.PoolerView(nil), rows...)
	sortPoolers(desc, SortNamespace, true)
	var order []string
	for _, p := range desc {
		order = append(order, poolerIdentity(p))
	}
	if want := "ns-b/alpha,ns-a/beta,ns-a/alpha"; strings.Join(order, ",") != want {
		t.Errorf("sort=-namespace gave %v, want %s", order, want)
	}
}

func TestPoolersPagination(t *testing.T) {
	// R8.10's walk: every row exactly once, in sort order, across pages of one.
	// The rows come from the fixture's nesting (one cluster in ns-a with two
	// poolers, one in ns-b with one), so this exercises the flatten and the
	// keyset cursor in the same walk — a flatten that dropped a cluster's second
	// pooler would end the walk after two rows and trip the total check.
	inv := discovery.Inventory{
		Clusters: []dto.ClusterView{
			{Namespace: "ns-a", Name: "cluster-a", Poolers: []dto.PoolerView{poolerRows()[0], poolerRows()[1]}},
			{Namespace: "ns-b", Name: "cluster-b", Poolers: []dto.PoolerView{poolerRows()[2]}},
		},
		GeneratedAt: time.Now().UTC(),
	}
	const limit = 1
	var (
		seen  []string
		page  int
		query = fmt.Sprintf("limit=%d", limit)
	)
	for {
		got := pagePoolers(inv, mustPoolersQuery(t, query))
		if page == 0 && got.Meta.Total != 3 {
			t.Fatalf("meta.total = %d, want 3 (total is the pre-cut match count, R8.4/R8.10)", got.Meta.Total)
		}
		if len(got.Data) == 0 {
			t.Fatalf("page %d is empty with 3 poolers in the fleet", page)
		}
		for _, p := range got.Data {
			seen = append(seen, poolerIdentity(p))
		}
		if !got.Meta.Truncated {
			if got.Meta.NextCursor != "" {
				t.Errorf("last page carries nextCursor %q, want empty", got.Meta.NextCursor)
			}
			break
		}
		if got.Meta.NextCursor == "" {
			t.Fatalf("page %d truncated with empty nextCursor", page)
		}
		if len(got.Data) != limit {
			t.Fatalf("page %d has %d rows, want %d", page, len(got.Data), limit)
		}
		query = fmt.Sprintf("limit=%d&cursor=%s", limit, got.Meta.NextCursor)
		page++
		if page > 10 {
			t.Fatalf("paging did not terminate after %d pages: %v", page, seen)
		}
	}
	if want := "ns-a/alpha,ns-a/beta,ns-b/alpha"; strings.Join(seen, ",") != want {
		t.Errorf("pages in order %v, want %v", seen, want)
	}
}

func TestPoolersFiltering(t *testing.T) {
	// R8.4's ?namespace= and ?cluster= narrow the flattened set, and meta.total
	// counts *matching* rows — the number a client uses to know how big the
	// filtered set is, not the size of the fleet.
	inv := discovery.Inventory{
		Clusters: []dto.ClusterView{
			{Namespace: "ns-a", Name: "cluster-a", Poolers: []dto.PoolerView{poolerRows()[0], poolerRows()[1]}},
			{Namespace: "ns-b", Name: "cluster-b", Poolers: []dto.PoolerView{poolerRows()[2]}},
		},
		GeneratedAt: time.Now().UTC(),
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"ns-a/alpha", "ns-a/beta", "ns-b/alpha"}},
		{"namespace=ns-a", []string{"ns-a/alpha", "ns-a/beta"}},
		{"namespace=ns-b", []string{"ns-b/alpha"}},
		{"cluster=cluster-a", []string{"ns-a/alpha", "ns-a/beta"}},
		{"cluster=cluster-b", []string{"ns-b/alpha"}},
		{"namespace=ns-a&cluster=cluster-b", nil}, // no pooler matches both
		{"namespace=no-such-ns", nil},
	}
	for _, tt := range tests {
		got := pagePoolers(inv, mustPoolersQuery(t, tt.query))
		var order []string
		for _, p := range got.Data {
			order = append(order, poolerIdentity(p))
		}
		if strings.Join(order, ",") != strings.Join(tt.want, ",") {
			t.Errorf("?%s gave %v, want %v", tt.query, order, tt.want)
		}
		if got.Meta.Total != len(tt.want) {
			t.Errorf("?%s meta.total = %d, want %d", tt.query, got.Meta.Total, len(tt.want))
		}
	}
}

func TestPoolersEmptyFleetRendersArrayNotNull(t *testing.T) {
	// R8.4's envelope always carries an array. pageRows normalises the nil case
	// once, which is the only reason a route with no rows still emits `data: []`.
	s := New(Config{Listen: "127.0.0.1:0", Fleet: &stubFleet{inv: discovery.Inventory{
		GeneratedAt: time.Now().UTC(),
	}, ready: true}})
	s.readiness.SetListening(true)

	rec := servePoolersQuery(t, s, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200; body %s", PoolersPath, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("body = %s, want it to contain \"data\":[]", rec.Body.String())
	}
}

func TestPoolersNotReady(t *testing.T) {
	// R8.7's single readiness signal, from the same value /readyz answers with
	// (TestReadyzAndClustersShareOneReadiness proves the sharing for clusters;
	// this pins that the pooler route reads the same one rather than a second).
	s := New(Config{Listen: "127.0.0.1:0", Fleet: &stubFleet{inv: poolerFixtureInventory(t)}})
	rec := servePoolersQuery(t, s, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET %s before sync = %d, want 503; body %s", PoolersPath, rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotReady || body.Error.Message != "discovery has not completed its first sync" {
		t.Errorf("body = %+v, want NotReady with the documented message", body.Error)
	}

	// And the order: a malformed query on a not-ready server is a 400, not a 503.
	// The alternative would tell the client to retry a request that can never
	// succeed once the fleet syncs either.
	rec = servePoolersQuery(t, s, "limit=0")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET %s?limit=0 before sync = %d, want 400 (parameters are checked first)",
			PoolersPath, rec.Code)
	}
}

// ---------------------------------------------------------------------------
// detail route
// ---------------------------------------------------------------------------

func TestPoolerDetailServesTheRow(t *testing.T) {
	// The detail body is the same object the list route publishes for the same
	// identity — not a re-spelling of it. That is the R16.8 pairing between the
	// two routes, and it is why dto carries one PoolerView rather than a list
	// shape and a detail shape.
	penv, _ := loadPoolersFixture(t)
	if len(penv.Data) == 0 {
		t.Fatalf("%s has no rows", poolersFixturePath)
	}
	want := penv.Data[0]
	s := newFleetServer(t, poolerFixtureInventory(t))

	rec := servePoolerDetail(t, s, want.Namespace, want.Name)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s/%s/%s = %d, want 200; body %s",
			PoolerDetailPath, want.Namespace, want.Name, rec.Code, rec.Body.String())
	}
	var got dto.PoolerView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if diff := firstDiff(canonical(t, got), canonical(t, want)); diff != "" {
		t.Errorf("detail body is not the list route's row:\n%s", diff)
	}
}

func TestPoolerDetailNotFound(t *testing.T) {
	// The namespace synced and the pooler is not in it: the only state where 404
	// is the truth.
	penv, _ := loadPoolersFixture(t)
	s := newFleetServer(t, poolerFixtureInventory(t))

	rec := servePoolerDetail(t, s, penv.Data[0].Namespace, "no-such-pooler")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("= %d, want 404; body %s", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotFound {
		t.Errorf("code = %q, want NotFound", body.Error.Code)
	}
	if want := "no pooler " + penv.Data[0].Namespace + "/no-such-pooler in the fleet inventory"; body.Error.Message != want {
		t.Errorf("message = %q, want %q", body.Error.Message, want)
	}
}

func TestPoolerDetailFailedNamespaceIsUnavailable(t *testing.T) {
	// A pooler missing from a namespace whose read *errored* is not a deleted
	// pooler. Answering 404 here would tell the client the pooler went away; the
	// inventory is missing the whole namespace, so the answer is the R8.5 code the
	// list route uses for the same fact, with the failure's own message.
	s := newFleetServer(t, discovery.Inventory{
		GeneratedAt: time.Now().UTC(),
		Failed: []dto.NamespaceFailure{
			{Namespace: "brand-new", Code: dto.ErrUnavailable, Message: "connection refused"},
		},
	})
	rec := servePoolerDetail(t, s, "brand-new", "anything")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("= %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrUnavailable {
		t.Errorf("code = %q, want Unavailable", body.Error.Code)
	}
	if body.Error.Message != "namespace brand-new failed to sync" {
		t.Errorf("message = %q, want the namespace-level failure", body.Error.Message)
	}
}

func TestPoolerDetailLoadingNamespaceIsNotReady(t *testing.T) {
	// The informer has not synced this namespace, so the inventory has no
	// authority over anything in it — which is NotReady, not NotFound.
	s := newFleetServer(t, discovery.Inventory{
		GeneratedAt: time.Now().UTC(),
		Loading:     []string{"brand-new"},
	})
	rec := servePoolerDetail(t, s, "brand-new", "anything")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("= %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotReady || body.Error.Message != "namespace brand-new is still loading" {
		t.Errorf("body = %+v, want NotReady with the still-loading message", body.Error)
	}
}

func TestPoolerDetailRejectsEncodedSegments(t *testing.T) {
	// A handler called directly can see an escaped name the router would have
	// decoded; serving it would mean a pooler whose name contains a literal %2F.
	s := newFleetServer(t, poolerFixtureInventory(t))
	rec := servePoolerDetail(t, s, "a%2Fb", "pooler")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("= %d, want 400; body %s", rec.Code, rec.Body.String())
	}
}

func TestPoolerDetailGatedOnReadiness(t *testing.T) {
	// The gate comes before the lookup, so a not-ready server never answers 404
	// for a pooler it has not read yet.
	s := New(Config{Listen: "127.0.0.1:0", Fleet: &stubFleet{inv: poolerFixtureInventory(t)}})
	rec := servePoolerDetail(t, s, "ai-api-gateway", "rev-proxy-pg-pooler")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("= %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("code = %q, want NotReady", body.Error.Code)
	}
}
