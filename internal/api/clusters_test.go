package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/discovery"
)

// fixturePath is R16.8's named contract location (repo-root testdata/api/),
// reached from this package's directory. The same file is the SPA's Vitest
// fixture and the subject of dto.TestGoldenFixtureIsCanonical; this file is the
// third consumer, and the only one that tests a *handler* rather than a struct.
const fixturePath = "../../testdata/api/clusters.json"

// stubFleet is a Fleet with a fixed inventory and a settable readiness flag —
// the test double that makes the handler testable without a kube client, which
// is why server.go declares the Fleet interface instead of taking
// *discovery.Discoverer. Pointer receiver so a test can flip readiness after
// New() has already derived the Readiness from it (R8.7(i)).
type stubFleet struct {
	inv   discovery.Inventory
	ready bool
}

func (f *stubFleet) Inventory() discovery.Inventory { return f.inv }
func (f *stubFleet) Ready() bool                    { return f.ready }

// newFleetServer builds a ready Server serving inv.
//
// It also marks the server listening, which Run() would have done: a test that
// drives Handler() directly with httptest never binds a socket, and R8.7(i)'s
// Readiness requires the socket *and* the sources. Without this the handler
// answers 503 for a fleet that is perfectly ready — correct behaviour, wrong
// test, and the first thing this file had to learn about the new wiring.
//
// There is no `ready` parameter: every caller here wants a synced fleet, and
// the two states that need the flag off — not-yet-listening and
// not-yet-synced — are tested where they belong, by constructing the Server (or
// the stubFleet, whose field stays mutable) directly in
// TestReadyzGatedOnListening and TestReadyzAndClustersShareOneReadiness.
func newFleetServer(t *testing.T, inv discovery.Inventory) *Server {
	t.Helper()
	s := New(Config{Listen: "127.0.0.1:0", Fleet: &stubFleet{inv: inv, ready: true}})
	s.readiness.SetListening(true)
	return s
}

// loadFixture returns the frozen envelope plus its raw bytes.
func loadFixture(t *testing.T) (dto.Envelope[dto.ClusterView], []byte) {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", fixturePath, err)
	}
	var env dto.Envelope[dto.ClusterView]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("parse %s: %v", fixturePath, err)
	}
	return env, raw
}

// canonical re-encodes a value the way the fixture file is formatted: two-space
// indent, trailing newline. Comparing in this form is what catches a dropped,
// renamed or reordered field, which a decode-into-the-same-struct comparison
// cannot see (the handler's own body is compact — see writeJSON).
func canonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b) + "\n"
}

// firstDiff describes the first line at which two canonical JSON documents
// differ, or "" when they are identical. A whole-file diff is useless here — the
// fixture is 400 lines and one renamed field changes every line after it — so
// the report names the line and quotes both sides.
func firstDiff(got, want string) string {
	if got == want {
		return ""
	}
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gs, ws string
		if i < len(g) {
			gs = g[i]
		}
		if i < len(w) {
			ws = w[i]
		}
		if gs != ws {
			return fmt.Sprintf("line %d:\n got: %s\nwant: %s", i+1, gs, ws)
		}
	}
	return "differ only in trailing whitespace"
}

// TestClustersServesTheFrozenFixture is R16.8's cross-check for the handler:
// the inventory the fixture describes, served through GET /api/v1/clusters,
// must produce a body that is byte-identical to the fixture once normalised.
// The handler can therefore not drop, rename or reorder a field without this
// failing — the class of drift R16.8 exists to catch, and the reason the
// contract was frozen before the handler body was written.
func TestClustersServesTheFrozenFixture(t *testing.T) {
	env, raw := loadFixture(t)
	s := newFleetServer(t, discovery.Inventory{
		Clusters:    env.Data,
		Failed:      env.Meta.Failed,
		Loading:     env.Meta.Loading,
		GeneratedAt: env.Meta.GeneratedAt,
	})

	rec := do(t, s, http.MethodGet, "/api/v1/clusters")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/clusters = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}

	var got dto.Envelope[dto.ClusterView]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode handler body: %v\n%s", err, rec.Body.String())
	}
	if diff := firstDiff(canonical(t, got), string(raw)); diff != "" {
		t.Errorf("handler output is not the frozen fixture:\n%s", diff)
	}
}

// TestReadyzAndClustersShareOneReadiness is R8.7(i) proven structurally: the two
// surfaces answer from one value, so there is no state in which the probe says
// "ready" while the API says NotReady (or the reverse). Before readiness.go,
// /readyz read Run's `ready` flag and the API decided separately, which is
// exactly the pair that could disagree.
func TestReadyzAndClustersShareOneReadiness(t *testing.T) {
	env, _ := loadFixture(t)
	fleet := &stubFleet{inv: discovery.Inventory{
		Clusters:    env.Data,
		GeneratedAt: env.Meta.GeneratedAt,
	}}
	s := New(Config{Listen: "127.0.0.1:0", Fleet: fleet})
	// Not listening yet: both surfaces must refuse.
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before listen = %d, want 503", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/clusters"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/api/v1/clusters before listen = %d, want 503", rec.Code)
	}

	// Listening, discovery not yet synced: the case R8.7 was written for. The
	// pod is up (healthz 200) but not routable (readyz 503) and the API answers
	// 503 rather than serving an empty fleet that looks like an empty cluster.
	s.readiness.SetListening(true)
	if rec := do(t, s, http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 (R8.7(iii): liveness is not readiness)", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with unsynced fleet = %d, want 503", rec.Code)
	}
	rec := do(t, s, http.MethodGet, "/api/v1/clusters")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/api/v1/clusters with unsynced fleet = %d, want 503", rec.Code)
	}
	var errBody dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	// The message is openapi.yaml's documented example for NotReady, verbatim.
	if errBody.Error.Code != dto.ErrNotReady ||
		errBody.Error.Message != "discovery has not completed its first sync" {
		t.Errorf("error body = %+v, want NotReady / %q", errBody.Error,
			"discovery has not completed its first sync")
	}

	// Synced: both surfaces flip together, because there is only one value.
	fleet.ready = true
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("/readyz after sync = %d, want 200", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/clusters"); rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/clusters after sync = %d, want 200", rec.Code)
	}
}

// TestServerWithoutFleetIsReadyWhenListening covers R8.7(ii) for D9's reserved
// `api` role: a process that runs no discovery has nothing to wait for, so it is
// ready once it is listening rather than permanently unready under a
// discovery-only rule. Honoured now via New's source-less Readiness so the role
// split does not have to re-derive it.
func TestServerWithoutFleetIsReadyWhenListening(t *testing.T) {
	s := newTestServer(t) // no Fleet
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before listen = %d, want 503", rec.Code)
	}
	s.readiness.SetListening(true)
	if rec := do(t, s, http.MethodGet, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("/readyz for an api-only server = %d, want 200 (R8.7(ii))", rec.Code)
	}
	// Its /api/v1/clusters answers 200 with an empty fleet: this process's
	// correct answer is "I collect nothing", not "I have not collected yet".
	rec := do(t, s, http.MethodGet, "/api/v1/clusters")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/clusters = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "{\"data\":[],\"meta\":{\"total\":0,\"truncated\":false,\"generatedAt\":\"0001-01-01T00:00:00Z\"}}\n" {
		t.Errorf("empty fleet body = %s", body)
	}
}

func TestClusterQueryValidation(t *testing.T) {
	// Every rejection is R8.5 InvalidRequest with a message naming the parameter
	// — the messages are the ones openapi.yaml documents, so the documented and
	// the emitted bodies agree.
	tests := []struct {
		target string
		want   string
	}{
		{"/api/v1/clusters?limit=0", "invalid limit: 0 is below the minimum of 1"},
		{"/api/v1/clusters?limit=5000", "invalid limit: 5000 exceeds the maximum of 500"},
		{"/api/v1/clusters?limit=abc", `invalid limit: "abc" is not an integer`},
		{"/api/v1/clusters?sort=bogus", `invalid sort: "bogus" (supported: namespace, name, phase, instances, pgMajor; prefix "-" for descending)`},
		{"/api/v1/clusters?sort=-bogus", `invalid sort: "-bogus" (supported: namespace, name, phase, instances, pgMajor; prefix "-" for descending)`},
		{"/api/v1/clusters?from=yesterday", `invalid from: "yesterday" is not RFC 3339`},
		{"/api/v1/clusters?to=2026-13-40", `invalid to: "2026-13-40" is not RFC 3339`},
		{"/api/v1/clusters?cursor=not-a-cursor", `invalid cursor: "not-a-cursor" is not a pgsi cursor`},
	}
	s := newFleetServer(t, discovery.Inventory{GeneratedAt: time.Now().UTC()})
	for _, tt := range tests {
		rec := do(t, s, http.MethodGet, tt.target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", tt.target, rec.Code)
			continue
		}
		var body dto.ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("GET %s: decode: %v", tt.target, err)
			continue
		}
		if body.Error.Code != dto.ErrInvalidRequest || body.Error.Message != tt.want {
			t.Errorf("GET %s\n got %+v\nwant InvalidRequest %q", tt.target, body.Error, tt.want)
		}
	}
}

// TestClusterQueryValidAccepts documents the other half of R8.4's parameter
// set: the values that must *not* be rejected, including `db` on a route that
// has no per-database meaning (openapi: "accepted and ignored on /clusters").
func TestClusterQueryValidAccepts(t *testing.T) {
	q, err := parseClusterQuery(urlValues(map[string]string{
		"namespace": "freeidp",
		"cluster":   "freeidp-pg",
		"db":        "appdb",
		"from":      "2026-11-20T08:00:00Z",
		"to":        "2026-11-20T09:00:00Z",
		"sort":      "-pgMajor",
		"limit":     "500",
	}))
	if err != nil {
		t.Fatalf("parseClusterQuery: %v", err)
	}
	if q.Limit != 500 || q.Sort != SortPGMajor || !q.Desc || q.DB != "appdb" {
		t.Errorf("parsed %+v, want limit 500, sort pgMajor desc, db appdb", q)
	}
	if q.From.IsZero() || q.To.IsZero() {
		t.Errorf("from/to not parsed: %+v", q)
	}
	// Defaults, per R8.10.
	d, err := parseClusterQuery(urlValues(nil))
	if err != nil {
		t.Fatalf("parseClusterQuery(empty): %v", err)
	}
	if d.Limit != DefaultLimit || d.Sort != defaultSort || d.Desc || d.cursor != nil {
		t.Errorf("defaults = %+v, want limit %d sort %s asc no cursor", d, DefaultLimit, defaultSort)
	}
}

func urlValues(m map[string]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range m {
		out[k] = []string{v}
	}
	return out
}

// TestClusterPagination walks every page of the fixture at limit=2 and asserts
// R8.10's two guarantees: pages never overlap or drop a row, and the cursor is
// the only paging state (no offset anywhere).
func TestClusterPagination(t *testing.T) {
	env, _ := loadFixture(t)
	inv := discovery.Inventory{Clusters: env.Data, GeneratedAt: env.Meta.GeneratedAt}
	const limit = 2

	var (
		seen  []string
		page  int
		query = fmt.Sprintf("limit=%d", limit)
	)
	for {
		got := pageClusters(inv, mustQuery(t, query))
		if page == 0 && got.Meta.Total != len(env.Data) {
			t.Fatalf("meta.total = %d, want %d (total is the pre-cut match count, R8.4/R8.10)",
				got.Meta.Total, len(env.Data))
		}
		if len(got.Data) == 0 {
			t.Fatalf("page %d is empty with %d rows in the fleet", page, len(env.Data))
		}
		for _, c := range got.Data {
			seen = append(seen, c.Namespace+"/"+c.Name)
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

	want := []string{
		"ai-api-gateway/rev-proxy-pg",
		"cookbook/cookbook-pg",
		"freeidp/freeidp-pg",
		"open-webui/open-webui-pg",
	}
	if len(seen) != len(want) {
		t.Fatalf("walked %d rows across pages, want %d: %v", len(seen), len(want), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("pages in order %v, want %v", seen, want)
		}
	}
}

// TestCursorIsBoundToItsSort covers R8.10's "two consecutive pages never
// overlap or drop a row": a cursor minted under one sort is meaningless under
// another, so it is rejected rather than silently skipping rows.
func TestCursorIsBoundToItsSort(t *testing.T) {
	env, _ := loadFixture(t)
	inv := discovery.Inventory{Clusters: env.Data, GeneratedAt: env.Meta.GeneratedAt}

	first := pageClusters(inv, mustQuery(t, "limit=2"))
	// Re-ask page 2 with the default sort: works.
	if _, err := parseClusterQuery(urlValues(map[string]string{
		"limit": "2", "cursor": first.Meta.NextCursor,
	})); err != nil {
		t.Fatalf("cursor from a namespace page rejected on a namespace sort: %v", err)
	}
	// Ask page 2 of a *different* sort: rejected.
	rec := do(t, newFleetServer(t, inv), http.MethodGet,
		"/api/v1/clusters?limit=2&sort=name&cursor="+first.Meta.NextCursor)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-sort cursor = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body == "" {
		t.Error("empty error body")
	}
}

// TestSortKeys pins each sort's ordering, including the two rules that are easy
// to get wrong: a null pgMajor sorts as 0 (R1.3's "neither source answered"),
// and the row identity is every sort's secondary key so ties have a defined
// order (R8.10).
func TestSortKeys(t *testing.T) {
	major16, major17 := 16, 17
	rows := []dto.ClusterView{
		{Namespace: "b", Name: "z", Phase: "Cluster in healthy state", Instances: 1, PGMajor: &major17},
		{Namespace: "a", Name: "y", Phase: "Not enough disk space", Instances: 3, PGMajor: nil},
		{Namespace: "a", Name: "x", Phase: "Cluster in healthy state", Instances: 3, PGMajor: &major16},
	}
	cases := []struct {
		sort string
		want []string // "ns/name" in expected order
	}{
		{SortNamespace, []string{"a/x", "a/y", "b/z"}},
		{SortName, []string{"a/x", "a/y", "b/z"}},
		{SortPhase, []string{"a/x", "b/z", "a/y"}},     // "Cluster…" < "Not…", tie broken by identity
		{SortInstances, []string{"b/z", "a/x", "a/y"}}, // 1, then 3/x, then 3/y
		{SortPGMajor, []string{"a/y", "a/x", "b/z"}},   // nil→0, 16, 17
	}
	for _, c := range cases {
		got := namesInOrder(rows, c.sort, false)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("sort %s asc = %v, want %v", c.sort, got, c.want)
		}
		rev := namesInOrder(rows, c.sort, true)
		wantDesc := reverse(c.want)
		if fmt.Sprint(rev) != fmt.Sprint(wantDesc) {
			t.Errorf("sort %s desc = %v, want %v", c.sort, rev, wantDesc)
		}
	}
}

func namesInOrder(rows []dto.ClusterView, sort string, desc bool) []string {
	cp := append([]dto.ClusterView(nil), rows...)
	sortRows(cp, sort, desc)
	out := make([]string, 0, len(cp))
	for _, r := range cp {
		out = append(out, r.Namespace+"/"+r.Name)
	}
	return out
}

func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// mustQuery parses a raw query string ("limit=2&cursor=…") the same way the
// handler does — through url.Parse, so the base64 cursor's padding survives —
// then through parseClusterQuery. It exists so the pagination tests can feed
// pageClusters exactly what handleClusters would.
func mustQuery(t *testing.T, raw string) ClusterQuery {
	t.Helper()
	u, err := url.Parse("http://x/?" + raw)
	if err != nil {
		t.Fatalf("parse query %q: %v", raw, err)
	}
	q, err := parseClusterQuery(u.Query())
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return q
}

// TestFailedNamespacesSurviveFiltering is R10.22's partial-render guarantee:
// meta.failed and meta.loading describe the *inventory*, not the page, so
// filtering to one namespace still reports the namespaces that failed to sync.
// Without this, `?namespace=freeidp` would hide the fact that two other
// namespaces are down, which is the failure mode R10.22 names.
func TestFailedNamespacesSurviveFiltering(t *testing.T) {
	when := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	inv := discovery.Inventory{
		Clusters: []dto.ClusterView{{Namespace: "freeidp", Name: "freeidp-pg", Phase: "Not enough disk space"}},
		Failed: []dto.NamespaceFailure{{
			Namespace: "cookbook", Code: dto.ErrForbidden,
			Message: "cannot list clusters: forbidden", LastSuccessTime: &when,
		}},
		Loading:     []string{"open-webui"},
		GeneratedAt: when,
	}
	rec := do(t, newFleetServer(t, inv), http.MethodGet, "/api/v1/clusters?namespace=freeidp")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var got dto.Envelope[dto.ClusterView]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data) != 1 || got.Meta.Total != 1 {
		t.Fatalf("page = %+v, want the one freeidp cluster", got.Data)
	}
	if len(got.Meta.Failed) != 1 || got.Meta.Failed[0].Namespace != "cookbook" {
		t.Errorf("meta.failed = %+v, want cookbook/Forbidden carried through the filter", got.Meta.Failed)
	}
	if len(got.Meta.Loading) != 1 || got.Meta.Loading[0] != "open-webui" {
		t.Errorf("meta.loading = %+v, want [open-webui]", got.Meta.Loading)
	}
	if !got.Meta.GeneratedAt.Equal(when) {
		t.Errorf("meta.generatedAt = %s, want %s (the snapshot's time, not now)", got.Meta.GeneratedAt, when)
	}
}

// TestEmptyFleetRendersArrayNotNull pins the one JSON detail the SPA cannot work
// around: `data` must be `[]`, never `null` (R8.4's envelope, and the reason
// dto.NewError normalises Details the same way).
func TestEmptyFleetRendersArrayNotNull(t *testing.T) {
	inv := discovery.Inventory{GeneratedAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
	rec := do(t, newFleetServer(t, inv), http.MethodGet, "/api/v1/clusters")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "{\"data\":[],\"meta\":{\"total\":0,\"truncated\":false,\"generatedAt\":\"2026-11-20T09:00:00Z\"}}\n" {
		t.Errorf("body = %s, want data:[]", body)
	}
}
