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
//     property schemas. NOT YET CHECKED here; see
//     "What this does not do" below.
//  3. the planned manifest — api/x-planned-paths.yaml, embedded by the api
//     package and re-emitted verbatim.
//  4. the prose            — openapi_annotations.go, the authored strings a
//     generator cannot derive.
//
// # What this does not do (yet)
//
// Input 2 is unimplemented: the generator does not yet validate an authored
// schema's property list against the named Go struct's json tags, so a dto field
// renamed without a document edit is still caught by the web types' tsc import
// (R16.8) and not by this file. The route table (input 1) and the manifest
// (input 3) are implemented, which is the half this commit exists for: it is
// what makes `paths` and `x-planned-paths` disjoint by construction, and so what
// makes the extraction of the manifest into its own file a source/output pair
// rather than a second authored copy.
//
// # Why assembly is string concatenation
//
// A yaml.dump of the document would reflow every hand-wrapped description and
// turn a prose edit into a 2000-line diff — D20's recorded failure mode for
// gen-pgmatrix.py's whitespace. So the generator concatenates strings that are
// the document's lines verbatim, and openapi_gen_test.go byte-compares the
// result against the committed file. The committed file is therefore generated
// output; the editable sources are the annotation table and the manifest.
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
// which is why the bytes come from openapipkg.OpenAPIDocument() and only the
// -update write needs a path at all.
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
		writeLines(&b, s.Lines...)
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
func checkRouteTable(routes []string) error {
	registered := map[string]bool{}
	for _, r := range routes {
		method, path, ok := strings.Cut(r, " ")
		if !ok || method == "" || path == "" {
			return fmt.Errorf("openapi_gen: malformed route registration %q (want \"METHOD /path\")", r)
		}
		registered[path] = true
	}

	var undocumented, phantom []string
	for _, e := range openAPIPaths {
		if e.Path == "" {
			return fmt.Errorf("openapi_gen: an openAPIPaths entry has no Path")
		}
		if strings.HasPrefix(e.Status, "implemented") {
			if !registered[e.Path] {
				phantom = append(phantom, e.Path)
			}
		}
	}
	for path := range registered {
		found := false
		for _, e := range openAPIPaths {
			if e.Path == path {
				found = true
				break
			}
		}
		if !found {
			undocumented = append(undocumented, path)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(phantom)

	if len(undocumented) > 0 || len(phantom) > 0 {
		var msgs []string
		if len(undocumented) > 0 {
			msgs = append(msgs, fmt.Sprintf("registered in code, absent from the document (R14.7): %s",
				strings.Join(undocumented, ", ")))
		}
		if len(phantom) > 0 {
			msgs = append(msgs, fmt.Sprintf("documented as implemented, not registered in code (R14.7): %s",
				strings.Join(phantom, ", ")))
		}
		return fmt.Errorf("openapi_gen: route table and document disagree: %s", strings.Join(msgs, "; "))
	}
	return nil
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
// single-name declarations are recorded; anything else is left unset so that a
// registration using it fails the caller's ok check rather than silently
// resolving to "".
func collectStringConsts(f *ast.File, consts map[string]string) {
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
			if s, ok := stringValue(vs.Values[0], consts); ok {
				consts[vs.Names[0].Name] = s
			}
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
