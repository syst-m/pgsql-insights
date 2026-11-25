// The emitter's own tests (openapi_emit.go): the block splitter, the per-entry
// render, and the gate-2 comparison helper that TestGeneratedOpenAPI uses.
//
// Five tests, each pinning a different way this code fails:
//
//   - TestSchemaBlocksRealDocument — schemaBlocks() recovers exactly the entries the
//     table declares, and for an authored entry the recovered block is byte-for-byte
//     the table's Lines. Without this, a Generated entry could pass the byte-compare
//     because the *block splitter* normalises something away rather than because the
//     emitter produced the entry, and TestGeneratedOpenAPI's second gate would be
//     comparing two different definitions of "an entry".
//   - TestEmitMarkedEntries — every entry marked Generated renders, and renders to
//     exactly its committed block. This is the invariant that makes a flip safe to
//     land: marking an entry is a claim that the emitter reproduces it, and a false
//     claim fails here with the entry's name instead of in a 2000-line golden diff.
//   - TestEmitUnmarkedEntries — the not-yet-flipped entries, force-rendered. Their
//     contract is weaker (an authored entry may legitimately be unrenderable) but not
//     empty: the emitter refuses with a fixed set of reasons, and anything else is
//     the emitter breaking on a type it handled last commit.
//   - TestChangedSchemas — the gate-2 helper's fixture cases, including the two
//     directions a schema can change (body differs, name present on one side only),
//     which the real document pair cannot show at once.
//   - TestNamesIndex — the Go-reference index's precedence: an entry's authored
//     reference maps to that entry, and every other key is a canonical form some entry
//     derives, naming an entry that derives it. The render tests only see entries the
//     table already renders, so a name reassigned by table order is invisible there
//     and visible here.

package api

import (
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"
)

// emitDump names the schemas whose authored-vs-rendered line diff the emit tests
// print. It is the scratch harness kept as a flag instead of a file:
// `go test -run TestEmitMarkedEntries -emitdump=PVCView` prints the pair for one
// entry, and the pair it prints is the same pair the test already compares — so the
// dump cannot drift from the assertion the way a separate experiment file could.
//
// It exists because closing an emitter gap means reading the difference, not the
// pass/fail bit, and the migration is 20 more entries deep.
var emitDump = flag.String("emitdump", "",
	"comma-separated schema names: print the authored-vs-rendered line diff for them")

// dumpIfRequested prints the diff between an entry's committed block and the
// emitter's render when -emitdump named that entry.
func dumpIfRequested(t *testing.T, name string, authored, rendered []string) {
	for _, want := range strings.Split(*emitDump, ",") {
		if strings.TrimSpace(want) != name {
			continue
		}
		t.Logf("%s: authored %d lines, rendered %d\n%s",
			name, len(authored), len(rendered), diffLines(authored, rendered))
		return
	}
}

// diffLines is a line-oriented diff (longest-common-subsequence edit script) used
// only by the dump above. firstDiff names the first differing line, which is enough
// to fail a test and not enough to fix an emitter: a `required:` that renders as
// three lines where the entry has one produces a diff whose every later line is
// "changed" by index, and the shape of the difference is the thing being diagnosed.
func diffLines(a, b []string) string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "-"+a[i])
			i++
		default:
			out = append(out, "+"+b[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "-"+a[i])
	}
	for ; j < m; j++ {
		out = append(out, "+"+b[j])
	}
	if len(out) == 0 {
		return "  (identical)"
	}
	return strings.Join(out, "\n")
}

// TestSchemaBlocksRealDocument checks the block splitter against the committed
// document rather than a fixture, because the real file is where the splitter's edge
// cases actually live: banner comments above a key, blank separator lines between
// entries, and entries whose bodies contain blank lines between description
// paragraphs (Meta and ErrorBody both do).
//
// The second assertion is the one the migration depends on. For an entry that is not
// Generated, GenerateOpenAPI writes e.Lines verbatim (openapi_gen.go's emit loop),
// so the block recovered from the document must equal e.Lines exactly. If it
// doesn't, the document holds text that isn't in the table — a hand-edit — and every
// Generated comparison built on schemaBlocks is measuring the wrong thing.
func TestSchemaBlocksRealDocument(t *testing.T) {
	doc, err := os.ReadFile(CommittedDocumentRelPath)
	if err != nil {
		t.Fatalf("read %s: %v (R8.1 names this path; run `make openapi` to regenerate it)",
			CommittedDocumentRelPath, err)
	}
	blocks, err := schemaBlocks(string(doc))
	if err != nil {
		t.Fatalf("schemaBlocks: %v", err)
	}

	if len(blocks) != len(openAPISchemas) {
		t.Errorf("document has %d schema blocks, table has %d entries", len(blocks), len(openAPISchemas))
	}
	for _, e := range openAPISchemas {
		b, ok := blocks[e.Name]
		if !ok {
			t.Errorf("schema %s: in the table, not in the document (R14.7's schema-side twin)", e.Name)
			continue
		}
		if e.Generated {
			continue // compared against the emitter's output by TestEmitMarkedEntries
		}
		if !reflect.DeepEqual(b, e.Lines) {
			t.Errorf("schema %s: the document's block is not the table's Lines (%d lines vs %d) — the entry was hand-edited, and R8.1b says this file is generated output\n%s",
				e.Name, len(b), len(e.Lines), firstDiff(strings.Join(b, "\n"), strings.Join(e.Lines, "\n")))
		}
	}
}

// TestEmitMarkedEntries is the per-type migration's safety net: an entry may be
// marked Generated in openapi_annotations.go only if the emitter reproduces its
// committed block byte for byte.
//
// This is what turns "the golden file changed, is that right?" into a question with a
// name attached. The byte-compare in TestGeneratedOpenAPI says the document moved;
// this says which entry moved and whether that entry claimed it would render. A flip
// that produces a worse schema reverts one entry from the table rather than
// reverting the emitter.
func TestEmitMarkedEntries(t *testing.T) {
	c, err := newSchemaContext(".", "dto", openAPISchemas)
	if err != nil {
		t.Fatalf("newSchemaContext: %v", err)
	}
	doc, err := os.ReadFile(CommittedDocumentRelPath)
	if err != nil {
		t.Fatalf("read %s: %v", CommittedDocumentRelPath, err)
	}
	blocks, err := schemaBlocks(string(doc))
	if err != nil {
		t.Fatalf("schemaBlocks: %v", err)
	}

	var marked int
	for _, e := range openAPISchemas {
		if !e.Generated {
			continue
		}
		marked++
		lines, err := c.linesFor(e)
		if err != nil {
			// linesFor refuses rather than falling back to Lines for a marked entry
			// (openapi_emit.go:119): a silent fallback would make the flag a no-op
			// that still reads as a flip in the table.
			t.Errorf("schema %s is marked Generated but does not render: %v", e.Name, err)
			continue
		}
		dumpIfRequested(t, e.Name, blocks[e.Name], lines)
		if !reflect.DeepEqual(lines, blocks[e.Name]) {
			t.Errorf("schema %s: marked Generated, and the emitter's %d lines are not the committed entry's %d\n%s",
				e.Name, len(lines), len(blocks[e.Name]),
				firstDiff(strings.Join(lines, "\n"), strings.Join(blocks[e.Name], "\n")))
		}
	}
	if marked == 0 {
		t.Log("no entry is marked Generated: the emitter is exercised only via TestEmitUnmarkedEntries")
	}
}

// documentedRefusals are the reasons openapi_emit.go refuses to render an entry. An
// unmarked entry that fails for one of these is in the expected set; one that fails
// for any other reason is a regression, because the emitter used to render it (or,
// for a fresh entry, used to be able to).
//
// The list is the strings linesFor and its callees actually produce, not a set of
// words that might appear in an error: a permissive matcher is how this test would
// stop being able to fail, which is the failure mode this package documents at
// length (openapi_gen_test.go's header, R16.8).
//
// The strings are named by their guard in linesFor, not by line number: a line-number
// citation is only worth anything while something asserts it, and these guards move
// whenever the emitter gains a case.
var documentedRefusals = []string{
	"empty Go field",                       // e.Go == ""
	"is not pkg.Name",                      // splitQualified
	"resolves to no type",                  // canonicalRef, resolveGoRef
	"the type declares its own marshaller", // goStruct.CustomMarshal — SamplePair
	"the type has no doc comment",          // s.Doc == "" — R8.1 input 2 is empty
	"the entry is composed with allOf",     // entryHasAllOf — PromQLSeries, QueryDetailView
}

// TestEmitUnmarkedEntries force-renders every entry that has not been flipped.
//
// linesFor returns e.Lines untouched for an unmarked entry (openapi_emit.go:120), so
// calling it as-is would test the table's own copy rather than the emitter. The test
// renders a copy of the entry with Generated set — the same thing the migration would
// do — and classifies the result: renders (a candidate the next commit can flip) or
// refuses for a documented reason. Anything else fails.
func TestEmitUnmarkedEntries(t *testing.T) {
	c, err := newSchemaContext(".", "dto", openAPISchemas)
	if err != nil {
		t.Fatalf("newSchemaContext: %v", err)
	}
	doc, err := os.ReadFile(CommittedDocumentRelPath)
	if err != nil {
		t.Fatalf("read %s: %v", CommittedDocumentRelPath, err)
	}
	blocks, err := schemaBlocks(string(doc))
	if err != nil {
		t.Fatalf("schemaBlocks: %v", err)
	}
	for _, e := range openAPISchemas {
		if e.Generated {
			continue
		}
		forced := e
		forced.Generated = true
		lines, err := c.linesFor(forced)
		if err != nil {
			if !knownRefusal(err.Error()) {
				t.Errorf("schema %s: unexpected emitter error (not one of the documented refusals): %v", e.Name, err)
			}
			continue
		}
		dumpIfRequested(t, e.Name, blocks[e.Name], lines)
	}
}

// knownRefusal reports whether msg is one of documentedRefusals.
func knownRefusal(msg string) bool {
	for _, s := range documentedRefusals {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// TestChangedSchemas pins the gate-2 helper on fixtures. The real-document case is
// TestGeneratedOpenAPI's; what a fixture adds is the two directions a schema can
// "change" — a body that differs, and a name present on one side only — which the
// real pair cannot show at once because the committed file and the generated one have
// the same 32 names.
func TestChangedSchemas(t *testing.T) {
	const doc = "  schemas:\n\n    A:\n      type: object\n\n    B:\n      type: string\n"
	cases := []struct {
		name string
		a    string
		b    string
		want []string
	}{
		{"identical", doc, doc, nil},
		{"body changed", doc, "  schemas:\n\n    A:\n      type: string\n\n    B:\n      type: string\n", []string{"A"}},
		// Present on one side only: an entry added to the table without running
		// `make openapi`, and one deleted from the document.
		{"added", doc, "  schemas:\n\n    A:\n      type: object\n", []string{"B"}},
		{"removed", "  schemas:\n\n    A:\n      type: object\n", doc, []string{"B"}},
		// Two at once, and the result is sorted rather than map-ordered: an unsorted
		// slice makes the failure message non-reproducible.
		{"both", doc, "  schemas:\n\n    A:\n      type: string\n\n    B:\n      type: object\n", []string{"A", "B"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := changedSchemas(tc.a, tc.b)
			if err != nil {
				t.Fatalf("changedSchemas: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("changedSchemas()\n got %v\nwant %v", got, tc.want)
			}
		})
	}
}

// TestNamesIndex pins the precedence indexNames documents: every entry's authored
// reference maps to that entry, and every other key in the index is a canonical form
// that some entry derives, mapping back to an entry that derives it. The render tests
// exercise entries the table already renders, so this is the only place the index
// itself is checked — which makes it the place a reassigned name shows up before a
// field starts resolving to the wrong schema.
func TestNamesIndex(t *testing.T) {
	c, err := newSchemaContext(".", "dto", openAPISchemas)
	if err != nil {
		t.Fatalf("newSchemaContext: %v", err)
	}

	authored := map[string]string{}
	derivers := 0
	derivedBy := map[string]map[string]bool{} // canonical ref -> every entry that derives it
	for _, e := range openAPISchemas {
		if e.Go == "" {
			continue
		}
		authored[e.Go] = e.Name
		if got := c.names[e.Go]; got != e.Name {
			t.Errorf("names[%q] = %q, want %q: an entry's own reference must map to itself", e.Go, got, e.Name)
		}
		canon, ok := canonicalRef(e.Go, pkgOf(e.Go), c.pkgs, c.aliases)
		if !ok || canon == e.Go {
			continue
		}
		derivers++
		if derivedBy[canon] == nil {
			derivedBy[canon] = map[string]bool{}
		}
		derivedBy[canon][e.Name] = true
	}

	// Every key that is not an authored reference has to be a derived one, and it has
	// to name an entry that derives it. A key nothing derives is a name the indexer
	// invented; a derived key naming an entry that never derived it is a claim left
	// over from a table order, which is what the two-pass split removes.
	for ref, name := range c.names {
		if _, ok := authored[ref]; ok {
			continue
		}
		if !derivedBy[ref][name] {
			t.Errorf("names[%q] = %q, but no entry derives %q from %q", ref, name, name, ref)
		}
	}

	// The counts are logged, not asserted: how many entries carry a second name moves
	// whenever the table gains an alias, and an assertion here would be a second copy
	// of the table. The command that reproduces them is in the commit body of the
	// change that split the index into two passes.
	t.Logf("%d entries author %d distinct references; %d of them derive a second name; the index has %d keys",
		len(authored), len(authored), derivers, len(c.names))
}
