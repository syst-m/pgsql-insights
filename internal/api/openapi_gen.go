// The R8.1b generator: api/openapi.yaml assembled from the code it describes.
//
// R8.1 asked for a document "generated from the handlers and checked in CI", and
// for two phases the answer to "generated" was "it isn't, the stdlib has no
// OpenAPI generator" — which was true of the toolchain and not a reason. R8.1b
// splits the document into what a generator can derive and what it cannot, and
// names its inputs (see openapi_annotations.go's header for the argument):
//
//  1. the route table      — RouteTable(), a go/ast walk over this package's
//     non-test files, with package-level string constants
//     resolved so `"GET "+ClusterDetailPath` expands to
//     the string the router and the tests use.
//  2. the dto structs      — internal/api/dto/*.go, whose json tags are the
//     property schemas. Checked by openapi_schemas.go and
//     emitted, per type, by openapi_emit.go.
//  3. the planned manifest — api/x-planned-paths.yaml, embedded by the api
//     package and re-emitted verbatim.
//  4. the prose            — openapi_annotations.go, the authored strings a
//     generator cannot derive.
//
// # What this does not do (yet)
//
// Input 2 exists in two halves that are deliberately separate. openapi_schemas.go
// *checks* every entry against the Go structs — a schema naming a type that does
// not exist, or listing a property the struct's json tags do not have, fails the
// build — and that half applies to all 32 entries at once. openapi_emit.go
// *renders* an entry from its struct, and that half is per-type: an entry flips
// from authored to generated one at a time through openAPISchema.Generated, and
// TestEmitMarkedEntries requires a marked entry's render to equal its committed
// block byte for byte. Twenty-nine are flipped, which closes the seventeen the
// migration was scoped around. What stays authored is exactly the three entries that
// cannot render at all: PromQLSeries and QueryDetailView, composed with allOf when the
// emitter generates flat objects only, and SamplePair, whose MarshalJSON emits an
// array form its named fields do not describe. The last of the seventeen was
// PromQLResult, and it was the one entry that needed a rule rather than a field — a
// property's `oneOf:` is a list no Go type supplies, so propSchema carries it the way
// it carries the other authored blocks and copies it. Every entry the emitter is able
// to render is now rendered.
//
// # Why assembly is string concatenation
//
// A yaml.dump of the document would reflow every hand-wrapped description and
// turn a prose edit into a 2000-line diff — D20's recorded failure mode for
// gen-pgmatrix.py's whitespace. So the generator concatenates strings that are
// the document's lines verbatim, and openapi_gen_test.go compares the result
// against the committed file (byte-for-byte, and when entries are marked
// Generated, schema-by-schema). The committed file is therefore generated output;
// the editable sources are the annotation table, the emitter, and the manifest.
package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	openapipkg "github.com/frankellis/pgsql-insights/api"
)

// CommittedDocumentRelPath is the generated document's path relative to this
// package's directory. go:embed cannot reach it from here (`..` is not allowed
// in an embed path, and the document lives in a different package's directory),
// which is why the -update write takes a path and TestGeneratedOpenAPI reads the
// file with os.ReadFile rather than through the embed.
//
// The two are the same bytes within one build — api/embed.go embeds this file and
// openapipkg.OpenAPIDocument() returns the embedded copy — so the test could read
// either. It reads the path because the thing under test is *this file's content*
// versus the generator's output, and reading the embed would compare the
// generator against a snapshot the build took of the same file.
const CommittedDocumentRelPath = "../../api/openapi.yaml"

// GenerateOpenAPI assembles the R8.1 document from the generator's inputs and
// returns it as a string with a trailing newline on every line, so the return
// value is byte-identical to the file it is written to.
//
// It returns an error rather than a partial document when the route table and
// the annotation table disagree (R14.7): a route in code with no entry here, or
// an entry here claiming a route that is not registered. That check used to be
// one Test*RouteIsDocumented function per route; here it is one pass over the
// two lists, and it runs on every generation.
func GenerateOpenAPI() (string, error) {
	routes, err := RouteTable()
	if err != nil {
		return "", err
	}
	if err := checkRouteTable(routes); err != nil {
		return "", err
	}
	// The schema-side gate, run before anything is written: openapi_schemas.go
	// resolves every openAPISchema's `Go:` reference and diffs the authored
	// property list against the struct's json tags. It sits next to
	// checkRouteTable because the two are the same rule on the two derived
	// sections — R14.7 for `paths:` (a route in code with no entry, or an entry
	// with no route) and R16.8 for `schemas:` (an entry whose properties are not
	// the struct's tags) — and both have to fail the build rather than print.
	//
	// The context is built once and used twice, here and in the emit loop below:
	// parsing the packages separately for the check and for the render would let the
	// two halves describe different code, which is the second-copy failure this
	// generator exists to remove.
	c, err := newSchemaContext(".", "dto", openAPISchemas)
	if err != nil {
		return "", err
	}
	if problems := c.check(); len(problems) > 0 {
		return "", fmt.Errorf("openapi_gen: %d schema/struct mismatch(es):\n%s",
			len(problems), strings.Join(problems, "\n"))
	}
	planned, err := openapipkg.XPlannedPaths()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	writeLines(&b, openAPIBanner...)
	writeLines(&b, openAPIOpenapiHead...)
	writeLines(&b, openAPIOpenapi...)
	writeLines(&b, openAPIInfoHead...)
	writeLines(&b, openAPIInfo...)
	writeLines(&b, openAPIServersHead...)
	writeLines(&b, openAPIServers...)
	writeLines(&b, openAPISecurityHead...)
	writeLines(&b, openAPISecurity...)
	writeLines(&b, openAPITagsHead...)
	writeLines(&b, openAPITags...)

	// `paths:` has no annotation var of its own: unlike every other section it is
	// derived, and its content is openAPIPaths. The blank line is the separator
	// the committed file has between the tags block and this one.
	writeLines(&b, "", "paths:")
	for _, p := range openAPIPaths {
		writeLines(&b, p.Lines...)
	}

	// The manifest is re-emitted verbatim, leading blank line included — that
	// blank line is the separator between the paths block and this section in the
	// committed file, so nothing else may emit one here.
	b.Write(planned)

	writeLines(&b, openAPIComponentsHead...)

	// `  schemas:` is the second derived section key, for the same reason as
	// `paths:`. openAPIComponentsHead is documented as "everything before
	// `schemas:`", so the key belongs to neither the head nor the schema entries
	// and the generator supplies it.
	writeLines(&b, "  schemas:")
	for _, s := range openAPISchemas {
		// linesFor returns the authored Lines for an entry that is not marked
		// Generated, and the rendered schema for one that is (openapi_emit.go). The
		// per-entry flag is what makes the migration reviewable: one flip is one
		// schema block in the diff, and TestGeneratedOpenAPI's second gate checks
		// that nothing else moved.
		lines, err := c.linesFor(s)
		if err != nil {
			return "", err
		}
		writeLines(&b, lines...)
	}
	return b.String(), nil
}

// writeLines appends each argument followed by a newline. Every annotation slice
// holds complete lines with no trailing newline of their own, which is what makes
// the byte-compare in TestGeneratedOpenAPI meaningful rather than a comparison
// that forgives whitespace.
func writeLines(b *strings.Builder, lines ...string) {
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
}

// WriteDocument generates the document and writes it to path. This is what
// `make openapi` and `go test ./internal/api -run TestGeneratedOpenAPI -update`
// run; the test itself only compares.
func WriteDocument(path string) error {
	doc, err := GenerateOpenAPI()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// checkRouteTable is R14.7's two directions, checked against the annotation
// table instead of one test per route:
//
//   - every route registered in code has a `paths` entry — the direction that
//     used to rot silently, because registering a handler and forgetting the
//     document produced no failure anywhere;
//   - every `paths` entry claiming to be implemented has a route — the direction
//     that catches a document advertising a route that 404s.
//
// An entry whose status does not start with "implemented" is allowed to have no
// route: that is what `x-planned-paths` is for, and the manifest is checked for
// disjointness separately (by scripts/check-openapi.py and, now, by this file
// emitting the manifest as its own section rather than duplicating it).
//
// The comparison is per *method*, not per path. Keying `registered` by the path
// alone — which is what this function did — makes a second verb invisible in both
// directions at once: register `DELETE /api/v1/clusters/{ns}/{name}` without
// documenting a `delete:` operation and the phantom check finds the path already
// registered via the GET, the undocumented check finds the path documented, and
// the byte-compare passes because Lines is emitted verbatim. The document then
// advertises one method and the binary serves two, and every gate agrees.
func checkRouteTable(routes []string) error {
	registered := map[string]bool{}
	for _, r := range routes {
		method, path, ok := strings.Cut(r, " ")
		if !ok || method == "" || path == "" {
			return fmt.Errorf("openapi_gen: malformed route registration %q (want \"METHOD /path\")", r)
		}
		registered[strings.ToUpper(method)+" "+path] = true
	}

	// Every documented "METHOD /path" pair. Two entries documenting the same pair
	// is not an error the way two entries with the same Path would be — the
	// PromQL pair is one entry with two keys — so the set is a set, not a
	// multiset.
	documented := map[string]bool{}
	var noMethod, badStatus []string
	for _, e := range openAPIPaths {
		if e.Path == "" {
			return fmt.Errorf("openapi_gen: an openAPIPaths entry has no Path")
		}
		if _, ok := knownStatusTokens[statusToken(e.Status)]; !ok {
			badStatus = append(badStatus, fmt.Sprintf("%s: unrecognised x-status %q", e.Path, e.Status))
		}
		methods := documentedMethods(e.Lines)
		if len(methods) == 0 {
			noMethod = append(noMethod, e.Path)
		}
		for _, m := range methods {
			documented[m+" "+e.Path] = true
		}
	}

	var phantom []string
	for _, e := range openAPIPaths {
		if statusToken(e.Status) != "implemented" {
			continue
		}
		for _, m := range documentedMethods(e.Lines) {
			if !registered[m+" "+e.Path] {
				phantom = append(phantom, m+" "+e.Path)
			}
		}
	}
	statusLines := mismatchedStatusLines()
	var undocumented []string
	for k := range registered {
		if !documented[k] {
			undocumented = append(undocumented, k)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(phantom)

	if len(undocumented) > 0 || len(phantom) > 0 || len(noMethod) > 0 || len(badStatus) > 0 || len(statusLines) > 0 {
		var msgs []string
		if len(undocumented) > 0 {
			msgs = append(msgs, fmt.Sprintf("registered in code, absent from the document (R14.7): %s",
				strings.Join(undocumented, ", ")))
		}
		if len(phantom) > 0 {
			msgs = append(msgs, fmt.Sprintf("documented as implemented, not registered in code (R14.7): %s",
				strings.Join(phantom, ", ")))
		}
		if len(noMethod) > 0 {
			// An entry with no operation key documents nothing, so it cannot make
			// any route implemented — the check used to accept it as documentation
			// of its Path.
			msgs = append(msgs, fmt.Sprintf("paths entries with no HTTP method key in their lines: %s",
				strings.Join(noMethod, ", ")))
		}
		if len(badStatus) > 0 {
			msgs = append(msgs, fmt.Sprintf("%s", strings.Join(badStatus, "; ")))
		}
		if len(statusLines) > 0 {
			msgs = append(msgs, fmt.Sprintf("x-status lines that do not spell their entry's Status: %s",
				strings.Join(statusLines, "; ")))
		}
		return fmt.Errorf("openapi_gen: route table and document disagree: %s", strings.Join(msgs, "; "))
	}
	return nil
}

// openAPIOperationKeys are the HTTP methods OpenAPI spells as a lowercase
// operation key under a path item. It is the complete set the spec allows rather
// than the two this API uses, so a `delete:` added to an entry is recognised as
// an operation instead of failing to match anything.
var openAPIOperationKeys = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// documentedMethods returns the methods one path entry declares, uppercased to
// the spelling RouteTable uses.
//
// It is derived from Lines rather than stored in a `Methods` field because the
// alternative is a second copy of a fact the same entry already states: the
// `get:` and `post:` keys *are* the documented method set, and an authored field
// would need its own check that it agrees with Lines — R16.8's failure
// reproduced inside the fix for it. Deriving it also stops an entry whose Lines
// contain no operation key from counting as documentation of anything.
func documentedMethods(lines []string) []string {
	var out []string
	for _, l := range lines {
		// Operation keys sit at exactly four spaces of indent — the path key is
		// at two and an operation's fields at six — so the indent is what tells
		// `    get:` apart from `      '200':`, from `  /path:`, and from a
		// comment line at the same depth.
		if !strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "     ") {
			continue
		}
		key := strings.TrimSuffix(strings.TrimSpace(l), ":")
		for _, m := range openAPIOperationKeys {
			if key == m {
				out = append(out, strings.ToUpper(m))
			}
		}
	}
	return out
}

// knownStatusTokens is the closed set of leading tokens an x-status may start
// with. The full value is not a closed enum — the generator accepts a qualified
// string, "implemented (pgsi_build_info only; the R9.2 catalogue is Phase 2)",
// as the per-route annotation R8.1b requires for /metrics, the one partial a
// boolean cannot hold — but the first word is a fixed vocabulary.
//
// Without validating it, the old `strings.HasPrefix(status, "implemented")` test
// classed a typo ("implmented") as not-implemented, which exempts the entry from
// the phantom check, and the document ships a status that asserts nothing.
//
// Which token a Status carries is statusToken's business, and the space after the
// token is part of this vocabulary, not formatting — see its doc.
var knownStatusTokens = map[string]bool{
	"implemented": true,
	"planned":     true,
	"deprecated":  true,
}

// statusToken returns the leading word of an x-status value, or "" when the
// value has no word at all.
//
// The whitespace after the token is load-bearing. strings.Fields ends a token at
// the first space, so "implemented (4 of R9.8's 8 named families …)" — the
// /metrics entry, the document's one qualified status — splits into the token
// "implemented" plus a qualification the check ignores. "implemented(pending)",
// the same words with no space, is a single token that is not in
// knownStatusTokens, and checkRouteTable fails the entry. That is the intended
// outcome rather than a bug to smooth over: a parenthesised status is a status
// whose author meant something the vocabulary does not have, and a silent
// acceptance of it is how "implemented (pgsi_build_info only)" once went stale
// (mismatchedStatusLines). Write the grammar as `word` or `word (qualification)`,
// never `word(qualification)`, and the failure message quotes both spellings.
func statusToken(status string) string {
	fields := strings.Fields(status)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// mismatchedStatusLines is R16.8 applied to a duplication inside the annotation
// table itself. A path entry states its status twice: in the Status field, which
// is what checkRouteTable reads to decide implemented-vs-planned, and as the
// `x-status:` line inside Lines, which is the string the document actually shows.
// Lines are emitted verbatim, so nothing compared the two — and the first time a
// status changed, only the field moved: /metrics's Status became the four-family
// string while the document went on advertising "pgsi_build_info only", with
// `make openapi`, `make openapi-check` and the test suite all green.
//
// It also enforces the banner's quoting rule, because the value's spelling and
// the value's quoting are one contract: a qualified status containing ": " has to
// be double-quoted, and an unquoted one is a line the parser refuses — the
// seven-bug class the banner records, caught in Go instead of only in Python.
//
// The banner's prose quote of the string is a third copy and stays prose (it
// documents *that* an override exists, not what it says), so it is not checked
// here; the two machine-readable copies are.
func mismatchedStatusLines() []string {
	const prefix = "      x-status: "
	var out []string
	for _, e := range openAPIPaths {
		for _, l := range e.Lines {
			// Six spaces is the operation-field indent — the same rule
			// documentedMethods uses to tell `    get:` from `      '200':`. A
			// deeper indent or a comment line cannot match this prefix, so no
			// extra indent check is needed: the seventh character has to be the
			// `x`, and on a seven-space line it is a space.
			if !strings.HasPrefix(l, prefix) {
				continue
			}
			if got := strings.TrimPrefix(l, prefix); got != yamlScalar(e.Status) {
				out = append(out, fmt.Sprintf("%s: got %q, Status is %q", e.Path, got, e.Status))
			}
		}
	}
	return out
}

// yamlScalar renders a string the way the document writes plain scalars: bare
// when it can be, double-quoted when the banner's rule requires it (text
// containing ": ", or a leading backtick). Escaping is the subset the annotation
// table actually uses — an embedded double quote — which is also what Go's own
// quoting does, so the two never disagree about a value's bytes.
func yamlScalar(s string) string {
	if strings.Contains(s, ": ") || strings.HasPrefix(s, "`") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// RouteTable returns every `mux.HandleFunc("METHOD /path", h)` registration in
// this package's non-test files, as "METHOD /path" strings, sorted.
//
// It is a go/ast walk rather than a grep because the registrations are not all
// literals: server.go writes `"GET "+ClusterDetailPath` and promql.go writes
// `"GET "+QueryMetricsPath`, and the constant's value is what the router and the
// per-route tests use. Resolving identifiers against the package's own constant
// declarations is the difference between a route table that matches the running
// binary and one that matches the text of server.go.
//
// Test files are excluded on purpose: a test that registers a route on its own
// mux is not part of the served surface, and including them would make the
// document claim routes the binary does not serve.
func RouteTable() ([]string, error) { return routesInDir(".") }

// routesInDir is RouteTable with the directory as an argument, so the test can
// run it against a fixture as well as against the real package.
func routesInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("openapi_gen: read %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("openapi_gen: parse %s: %w", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("openapi_gen: no non-test .go files in %s", dir)
	}

	consts := map[string]string{}
	for _, f := range files {
		collectStringConsts(f, consts)
	}

	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			s, ok := stringValue(call.Args[0], consts)
			if !ok || s == "" {
				return true
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
			return true
		})
	}
	sort.Strings(out)
	return out, nil
}

// collectStringConsts records every package-level `const X = "..."` (and
// `var X = "..."`, which some packages use where a const would do) so that a
// registration written as `"GET " + X` can be resolved. Only string-typed
// single-name declarations are recorded; anything else is left unset, which the
// caller at routesInDir handles by skipping that registration.
//
// It iterates to a fixed point rather than walking the declarations once. A
// single pass in file order can only resolve `const B = "GET " + A` when A is
// declared *earlier in the same file*: stringValue reports ok=false for the
// unknown ident, B is never recorded, and every registration using B is dropped
// by the caller with no diagnostic — so `mux.HandleFunc("GET "+RouteX, h)`
// silently vanishes from the route table the moment someone hoists a pattern into
// a named const, which is the refactor the annotation table's `Go:` fields
// invite. Repeating the pass until nothing new resolves resolves chains of any
// depth in any order, and the loop terminates because each iteration either adds
// at least one name to a finite set or returns.
//
// The caller's silence is worth naming: skipping an unresolvable registration is
// correct for `mux.HandleFunc(computed(), h)` and was a bug for every other case
// this function used to miss. TestRoutesInDirFixture in openapi_gen_test.go is
// the fixture that pins which cases are which — the fixture routesInDir's own
// header promised ("so the test can run it against a fixture as well as against
// the real package") and that the test did not have until that file existed.
func collectStringConsts(f *ast.File, consts map[string]string) {
	for {
		added := false
		for _, d := range f.Decls {
			gen, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				if vs.Names[0].Name == "_" {
					continue
				}
				s, ok := stringValue(vs.Values[0], consts)
				if !ok {
					continue
				}
				if prev, exists := consts[vs.Names[0].Name]; exists && prev == s {
					continue // already recorded; not progress
				}
				consts[vs.Names[0].Name] = s
				added = true
			}
		}
		if !added {
			return
		}
	}
}

// stringValue evaluates the string expressions a route pattern is written with:
// a literal, a named constant, a concatenation of either, or a parenthesised
// version of any of those. Anything else (a function call, a field selector) is
// not a value the generator can know statically, and reports ok == false.
func stringValue(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.Ident:
		s, ok := consts[v.Name]
		return s, ok
	case *ast.ParenExpr:
		return stringValue(v.X, consts)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		left, ok := stringValue(v.X, consts)
		if !ok {
			return "", false
		}
		right, ok := stringValue(v.Y, consts)
		if !ok {
			return "", false
		}
		return left + right, true
	}
	return "", false
}
