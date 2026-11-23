// Tests for GET /api/v1/clusters/{namespace}/{name} (cluster_detail.go).
//
// Two families, for the two ways this route can be wrong. The first is the R14.7
// pairing: the handler registered but the document not updated, or the route
// still listed as planned while code serves it. The second is the response
// contract, where the interesting cases are not the 200 — they are the three
// different 4xx/5xx answers for "not there", "that namespace failed" and "that
// namespace is still loading", which R8.7's list route solves with `meta` and
// this one has to solve with a status code.

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/discovery"

	openapi "github.com/frankellis/pgsql-insights/api"
)

// TestClusterDetailRouteIsDocumented is the R14.7 pairing test, in the shape
// series_test.go establishes for the R8.11 route: the path must be under
// `paths` with `x-status: implemented`, its 200 must $ref the schema the
// handler actually encodes, and it must no longer appear in `x-planned-paths`.
// The last check is the disjointness invariant — a route in both lists is a
// document that calls an existing route "planned", which is R14.7's defect in
// the other direction.
func TestClusterDetailRouteIsDocumented(t *testing.T) {
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

	key := "  " + ClusterDetailPath + ":\n"
	at := strings.Index(paths, key)
	if at < 0 {
		t.Fatalf("%s is not under paths: — the handler is registered, so R14.7 requires it documented",
			ClusterDetailPath)
	}
	item := paths[at+len(key):]
	if next := strings.Index(item, "\n  /"); next >= 0 {
		item = item[:next]
	}
	if !strings.Contains(item, "x-status: implemented") {
		t.Errorf("%s has no `x-status: implemented`", ClusterDetailPath)
	}
	// The 200 schema must be the type the handler encodes. The handler writes a
	// bare dto.ClusterView, so a document that invented a ClusterDetailEnvelope
	// would fail here — which is the point: the assertion is on the Go type's
	// name, not on prose describing it.
	if !strings.Contains(item, "$ref: '#/components/schemas/ClusterView'") {
		t.Errorf("%s's responses do not $ref ClusterView, the type handleClusterDetail encodes",
			ClusterDetailPath)
	}
	// Every error code the handler can emit must be documented, via the
	// components/responses block that R8.5's enum is spelled out in.
	for _, code := range []string{"InvalidRequest", "Unauthorized", "Forbidden", "NotFound", "NotReady"} {
		if !strings.Contains(item, "$ref: '#/components/responses/"+code+"'") {
			t.Errorf("%s does not document %s, which the handler emits", ClusterDetailPath, code)
		}
	}

	for _, line := range strings.Split(planned, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue // a comment or a group key carries no route
		}
		fields := strings.Fields(line[2:])
		if len(fields) > 0 && fields[0] == ClusterDetailPath {
			t.Errorf("%s is still listed in x-planned-paths: it is in code now, "+
				"and R14.7 counts one name per route", ClusterDetailPath)
		}
	}
}

// TestClusterDetailServesTheSameViewAsTheList is the R16.8 half of this route:
// the detail body must be byte-identical to the entry the same cluster
// contributes to the list. Two handlers rendering one struct can still disagree
// if one of them re-projects it into a local shape, and the only check that
// catches that is comparing the encoded bytes, not the decoded fields.
func TestClusterDetailServesTheSameViewAsTheList(t *testing.T) {
	env, _ := loadFixture(t)
	if len(env.Data) == 0 {
		t.Fatal("fixture has no clusters")
	}
	s := newFleetServer(t, discovery.Inventory{
		Clusters:    env.Data,
		Failed:      env.Meta.Failed,
		Loading:     env.Meta.Loading,
		GeneratedAt: env.Meta.GeneratedAt,
	})

	for _, want := range env.Data {
		target := "/api/v1/clusters/" + want.Namespace + "/" + want.Name
		rec := do(t, s, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (body %s)", target, rec.Code, rec.Body.String())
		}
		got, err := json.Marshal(mustDecodeCluster(t, rec.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		wantBytes, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(wantBytes) {
			t.Errorf("GET %s re-projected the view:\n got %s\nwant %s", target, got, wantBytes)
		}
	}
}

// mustDecodeCluster decodes a detail response back into the dto type. Decoding
// then re-encoding (rather than diffing raw text) is what makes the comparison
// about the *fields the handler populated* and not about key order or the
// fixture's indentation, which is a file-formatting rule (R16.8), not a wire one.
func mustDecodeCluster(t *testing.T, body []byte) dto.ClusterView {
	t.Helper()
	var v dto.ClusterView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("response is not a ClusterView: %v\n%s", err, body)
	}
	return v
}

// TestClusterDetailNotFound pins the one case where 404 is the truth: the
// namespace synced, and the cluster is not in it.
func TestClusterDetailNotFound(t *testing.T) {
	env, _ := loadFixture(t)
	s := newFleetServer(t, discovery.Inventory{Clusters: env.Data, GeneratedAt: env.Meta.GeneratedAt})

	rec := do(t, s, http.MethodGet, "/api/v1/clusters/"+env.Data[0].Namespace+"/no-such-cluster")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// R8.5's shape, and the code spelled exactly as the Go constant spells it —
	// the same argument that made the codes constants instead of per-call-site
	// strings.
	if body.Error.Code != dto.ErrNotFound {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrNotFound)
	}
	if body.Error.Details == nil {
		t.Error("details is null; R8.5 requires it always present")
	}
}

// TestClusterDetailFailedNamespaceIsUnavailable is the partial-render case.
// The cluster is absent from the inventory only because its namespace's read
// errored, so "no such cluster" would be a lie and 404 would tell the client
// the cluster was deleted. R8.5's answer for a down dependency is Unavailable,
// and the failure's own message is carried through rather than reworded.
func TestClusterDetailFailedNamespaceIsUnavailable(t *testing.T) {
	const ns = "gone"
	s := newFleetServer(t, discovery.Inventory{
		Failed: []dto.NamespaceFailure{{
			Namespace: ns,
			Code:      dto.ErrForbidden,
			Message:   "cannot list clusters: forbidden",
		}},
	})

	rec := do(t, s, http.MethodGet, "/api/v1/clusters/"+ns+"/anything")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != dto.ErrUnavailable {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrUnavailable)
	}
	// The namespace's own failure text is preserved in details[], so the client
	// learns *why* the namespace is unreadable instead of only that it is.
	if len(body.Error.Details) == 0 || body.Error.Details[0] != "cannot list clusters: forbidden" {
		t.Errorf("details = %v, want the failure's own message", body.Error.Details)
	}
}

// TestClusterDetailLoadingNamespaceIsNotReady is the same distinction with the
// other of R8.4's two partial states: the informer has not synced, so the
// inventory has no authority over that namespace yet.
func TestClusterDetailLoadingNamespaceIsNotReady(t *testing.T) {
	const ns = "syncing"
	s := newFleetServer(t, discovery.Inventory{Loading: []string{ns}})

	rec := do(t, s, http.MethodGet, "/api/v1/clusters/"+ns+"/anything")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
}

// TestClusterDetailGatedOnReadiness proves the readiness gate runs *before* the
// lookup. A server that has not synced has an empty inventory, so without the
// gate every request would answer 404 — "no such cluster" from a process that
// has never listed anything. This is the same R8.7 single-signal rule the list
// route asserts, applied to the route where the wrong answer is a 404 rather
// than an empty page.
func TestClusterDetailGatedOnReadiness(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0", Fleet: &stubFleet{ready: false}})
	rec := do(t, s, http.MethodGet, "/api/v1/clusters/postgres/onepg")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != dto.ErrNotReady {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrNotReady)
	}
	// The message is the list route's, verbatim: two routes describing the same
	// state in two different ways is how a client learns to distrust both.
	if body.Error.Message != "discovery has not completed its first sync" {
		t.Errorf("message = %q, want the list route's message verbatim", body.Error.Message)
	}
}

// TestClusterDetailRejectsEncodedSegments covers the handler's input guard.
// ServeMux hands back a decoded segment, so a name that still needs escaping
// can only arrive from a caller that bypassed the router — and serving it would
// mean a cluster whose name contains a literal %2F.
func TestClusterDetailRejectsEncodedSegments(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0", Fleet: &stubFleet{ready: true}})
	s.readiness.SetListening(true)
	// "%2F" is not a valid namespace name, and the handler refuses it rather
	// than answering 404 about a cluster it cannot name.
	rec := do(t, s, http.MethodGet, "/api/v1/clusters/a%2Fb/c")
	if rec.Code == http.StatusOK {
		t.Fatalf("GET with an encoded namespace returned 200: %s", rec.Body.String())
	}
}
