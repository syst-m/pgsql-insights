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

// TestGeneratedOpenAPI is the R8.1b gate, in two gates.
//
// Gate 1 is the byte-compare the header describes: the committed document must be
// exactly what the generator produces. That is the whole test while every schema is
// authored, and it is still the whole test whenever the generator's output matches
// the file — a matching document cannot have moved a schema it doesn't mention.
//
// Gate 2 exists because of what the emitter changed. Once openapi_emit.go renders a
// schema from its Go type instead of copying the table's Lines, a generator change
// can move a schema block anywhere in the file, and "the golden file changed, is
// that right?" stops having an answer in the test's own output — you read 2000 lines
// or you trust the author. So when the bytes differ, the test names every schema
// whose body differs (changedSchemas, openapi_emit.go:949) and requires each name to
// be marked Generated in the table. A schema that moved without declaring it is the
// failure the gate catches: the commit flipped a type its message didn't name, or the
// emitter changed a schema it shouldn't have.
//
// Together the two gates make the migration bisectable. A flip that produces a worse
// schema shows up as that one entry named in the failure, so the fix is to unmark
// that entry rather than revert the emitter.
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

	// Gate 1: byte-identical.
	if got == string(want) {
		return
	}

	flipped := generatedEntryNames()
	if len(flipped) == 0 {
		// Nothing is marked Generated, so the generator's only source is the table
		// itself and any difference is an unexplained edit to generated output.
		// firstDiff is the package's existing line-naming diff helper
		// (clusters_test.go:87, written for the JSON golden). Reusing it is the
		// R16.8 rule applied inside the test package: the helper already exists,
		// so a second copy of the same function is the drift this project keeps
		// hitting, in miniature.
		t.Errorf("%s is not what the generator produces (%d bytes generated vs %d committed) and no schema is marked Generated, so nothing was declared as changing; run `make openapi`\n%s",
			CommittedDocumentRelPath, len(got), len(want), firstDiff(got, string(want)))
		return
	}

	// Gate 2: the document moved, and entries are marked — name what moved.
	changed, err := changedSchemas(string(want), got)
	if err != nil {
		t.Fatalf("changedSchemas: %v", err)
	}
	for _, name := range changed {
		if !containsName(flipped, name) {
			t.Errorf("schema %s changed in the generated document but openapi_annotations.go does not mark it Generated — the commit moved a schema it did not declare (R8.1b's per-type migration requires the flag to name every flip)",
				name)
		}
	}
	if len(changed) == 0 {
		// Marked entries exist, and every one of them rendered to its committed block
		// (TestEmitMarkedEntries), so the bytes differ somewhere outside `schemas:` —
		// the paths section, a prose block, the banner. Say so rather than reporting
		// an empty changed-list as a pass.
		t.Errorf("%s differs from the generator outside components.schemas (%d bytes generated vs %d committed) while %d entries are marked Generated; run `make openapi`\n%s",
			CommittedDocumentRelPath, len(got), len(want), len(flipped), firstDiff(got, string(want)))
	}
}

// generatedEntryNames returns the schema names the table marks Generated — the set
// gate 2 compares against. It reads the table rather than the emitter's output so
// that the set comes from the same file a reviewer reads in the diff.
func generatedEntryNames() []string {
	var out []string
	for _, e := range openAPISchemas {
		if e.Generated {
			out = append(out, e.Name)
		}
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
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
		"GET /api/v1/active-queries",
		"GET /api/v1/clusters",
		"GET /api/v1/clusters/{namespace}/{name}",
		"GET /api/v1/insights/queries",
		"GET /api/v1/insights/queries/series",
		"GET /api/v1/insights/queries/{fingerprint}",
		"GET /api/v1/insights/queries/{fingerprint}/plans",
		"GET /api/v1/insights/tags",
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
	// a grep-based generator gets wrong. Nine of the twenty distinct paths in
	// `want` are registered as a method concatenated onto a path constant — seven
	// in server.go (ActiveQueriesPath, ClusterDetailPath, QueriesPath,
	// QuerySeriesPath, QueryDetailPath, QueryPlansPath and TagsPath, the R3.12
	// route this commit registers) and two in promql.go (QueryMetricsPath and
	// SystemMetricsPath, each registered twice, GET and POST) — and this loop names
	// all nine. The other eleven are plain strings, which the `want` comparison
	// above already pins; this loop exists for the nine a text-only generator would
	// read as `"GET "`.
	//
	// The comment here used to say "five of these paths", and name
	// GET /api/v1/metrics/query as "the control — it is a literal in promql.go".
	// Both halves went stale: promql.go's two paths are constants now, and
	// active-queries was a seventh server.go constant that the five-item list
	// never named. The count is measured from the registrations rather than kept,
	// because a count in a comment is a second copy of the list this test generates
	// — which is the project's own R16.8 argument, turned on the test suite.
	for _, s := range []string{
		"GET /api/v1/active-queries",
		"GET /api/v1/clusters/{namespace}/{name}",
		"GET /api/v1/insights/queries",
		"GET /api/v1/insights/queries/series",
		"GET /api/v1/insights/queries/{fingerprint}",
		"GET /api/v1/insights/queries/{fingerprint}/plans",
		"GET /api/v1/insights/tags",
		"GET /api/v1/metrics/query",
		"GET /api/v1/metrics/system",
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

// TestRoutesInDirFixture runs the generator's engine against
// testdata/routes/ instead of against this package.
//
// routesInDir's own header advertises the capability — "RouteTable with the
// directory as an argument, so the test can run it against a fixture as well as
// against the real package" — and for two commits the test only ever passed ".".
// The argument existed without the test that used it, which is the same gap
// openapi_annotations.go:33 used to be: a capability named in prose and not
// exercised, so it cannot fail.
//
// The fixture's six cases (their numbered comments are in testdata/routes/) are
// the ones the real package cannot pin by themselves, because server.go happens
// to declare its consts in dependency order and to contain exactly one
// registration per shape:
//
//  1. a plain literal,
//  2. a literal concatenated onto a const (server.go's shape),
//  3. a const that needs a two-hop chain to resolve — the case the single-pass
//     collector dropped, and the reason the fixture's consts are in reverse
//     dependency order,
//  4. a duplicate registration (the table dedupes),
//  5. a pattern that is a function call (skipped: not statically knowable),
//  6. mux.Handle instead of mux.HandleFunc (skipped: the generator matches
//     HandleFunc).
//
// Cases 5 and 6 are asserted by their absence from `want`, which is why `want` is
// an exact slice rather than a contains-check: an exact comparison is the only
// form in which a skipped case can fail. A contains-check passes whether or not
// the skip happens.
func TestRoutesInDirFixture(t *testing.T) {
	got, err := routesInDir("testdata/routes")
	if err != nil {
		t.Fatalf("routesInDir(testdata/routes): %v", err)
	}
	want := []string{
		"GET /api/v1/deep2",   // case 3, via Deep3 -> Deep2 -> Deep1
		"GET /api/v1/literal", // cases 1 and 4, deduped
		"GET /api/v1/plain",   // case 2, via "GET " + Plain
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("routesInDir(testdata/routes)\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
