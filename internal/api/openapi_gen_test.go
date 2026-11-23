// The R8.1b gate: the committed document must be exactly what the generator
// produces.
//
// This is the test the annotations file's own header names, and until this
// commit it existed only inside a comment — openapi_annotations.go:33 promised
// "openapi_gen_test.go byte-compares the committed copy against its output"
// while no such file existed. That gap is the project's own recurring failure:
// a check referenced in prose, absent from the tree, and therefore unable to
// fail.
//
// What it buys: api/openapi.yaml stops being a hand-edited file that happens to
// describe the code and becomes generated output, so the document and its inputs
// cannot disagree quietly. The same argument the project already accepts for the
// web types — types.ts is imported from the Go DTO's published JSON rather than
// retyped, so `tsc` is the drift check (R16.8) — applied to the document.
//
// The -update flag rewrites the committed file from the generator, which is how
// `make openapi` works: change an input, run the target, and the document
// changes with it. Reviewing that diff is the review of the change.

package api

import (
	"flag"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false,
	"rewrite api/openapi.yaml from GenerateOpenAPI() instead of comparing it")

func TestGeneratedOpenAPI(t *testing.T) {
	got, err := GenerateOpenAPI()
	if err != nil {
		t.Fatalf("GenerateOpenAPI(): %v", err)
	}

	if *updateGolden {
		if err := WriteDocument(CommittedDocumentRelPath); err != nil {
			t.Fatalf("-update: %v", err)
		}
		t.Logf("wrote %s (%d bytes)", CommittedDocumentRelPath, len(got))
		return
	}

	want, err := os.ReadFile(CommittedDocumentRelPath)
	if err != nil {
		t.Fatalf("read %s: %v (R8.1 names this path; run `make openapi` to regenerate it)",
			CommittedDocumentRelPath, err)
	}
	if got != string(want) {
		// firstDiff is the package's existing line-naming diff helper
		// (clusters_test.go:87, written for the JSON golden). Reusing it is the
		// R16.8 rule applied inside the test package: the helper already exists,
		// so a second copy of the same function is the drift this project keeps
		// hitting, in miniature.
		t.Errorf("%s is not what the generator produces (%d bytes generated vs %d committed); run `make openapi`\n%s",
			CommittedDocumentRelPath, len(got), len(want), firstDiff(got, string(want)))
	}
}

// TestRouteTable pins the route table the generator derives from the package
// against the routes the document's `paths` section documents.
//
// server_test.go covers the handlers; this covers the registration list, which
// is the input R8.1b generates from and R14.7 audits. The two failure directions
// are both covered by GenerateOpenAPI's own check (an unregistered documented
// path, an undocumented registered path), so what this test adds is the shape of
// RouteTable itself: that `"GET "+ClusterDetailPath` resolves to the constant's
// value rather than to the literal text, that test-file registrations are
// excluded, and that a method-qualified route registered twice (the PromQL pair
// is GET and POST on one path) yields two entries.
func TestRouteTable(t *testing.T) {
	routes, err := RouteTable()
	if err != nil {
		t.Fatalf("RouteTable(): %v", err)
	}

	want := []string{
		"GET /",
		"GET /api/v1/clusters",
		"GET /api/v1/clusters/{namespace}/{name}",
		"GET /api/v1/insights/queries/series",
		"GET /api/v1/metrics/query",
		"GET /api/v1/metrics/system",
		"GET /api/v1/version",
		"GET /debug/pprof/",
		"GET /debug/pprof/cmdline",
		"GET /debug/pprof/profile",
		"GET /debug/pprof/symbol",
		"GET /debug/pprof/trace",
		"GET /healthz",
		"GET /metrics",
		"GET /readyz",
		"POST /api/v1/metrics/query",
		"POST /api/v1/metrics/system",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(routes, want) {
		t.Errorf("RouteTable() != the routes server.go and promql.go register\ngot:\n  %s\nwant:\n  %s",
			strings.Join(routes, "\n  "), strings.Join(want, "\n  "))
	}

	// The constant-resolution case, called out separately because it is the one
	// a grep-based generator gets wrong: three of these paths exist only as
	// constants concatenated onto the method.
	for _, s := range []string{
		"GET /api/v1/clusters/{namespace}/{name}",
		"GET /api/v1/insights/queries/series",
		"GET /api/v1/metrics/query",
	} {
		found := false
		for _, r := range routes {
			if r == s {
				found = true
			}
		}
		if !found {
			t.Errorf("RouteTable() lost %q — a path registered as \"GET \"+Const must resolve through the constant", s)
		}
	}
}
