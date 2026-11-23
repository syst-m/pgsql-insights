// The schema half of generator input 2: resolve every openAPISchema's `Go:`
// reference against the parsed Go sources and check the authored property list
// against the struct's json tags (R16.8's one-copy rule, enforced on the
// document).
//
// WHAT THIS FILE FIXES, and the exact shape of the bug. openapi_annotations.go's
// `openAPISchema` type doc says: "The generator resolves it against the parsed
// Go sources: a schema naming a type that does not exist is an error, and for a
// struct the entry's property list is checked against the struct's json tags."
// The banner at the top of that file makes the same promise ("An authored
// schema's property list is checked against the struct's json tags, so a renamed
// tag without a document edit fails the build"). Before this file, neither half
// existed: `openAPISchemas` had exactly one consumer in the package — the emit
// loop at openapi_gen.go:124 — and the only go/ast parsing was routesInDir,
// which reads route registrations. So `Go:` was a field that was read by nobody:
// a schema could name `dto.ClusterView`, `dto.Clusterview`, or a type deleted
// three phases ago, and `make openapi-check` stayed green while it did.
//
// That is the same defect class as the x-status field, found in the adjacent
// struct. mismatchedStatusLines fixed the `Status` field by deriving the value
// from the route table instead of trusting the authored string; this fixes the
// `Go` field by resolving the string. Both exist because a documented-but-unread
// field is worse than an absent one: the reader stops checking by hand.
//
// WHY THIS IS THE FIRST STEP OF THE GENERATOR, not the emitter. The migration
// plan is per-type — a dto type's schema moves from authored to generated once
// the Go side carries the prose, and the authored entry is deleted in the same
// commit. Deleting an authored entry is only safe once you know what it said:
// the check here is what proves the generated schema and the one it replaces
// agree, so a type flips with evidence rather than with a diff someone eyeballs.
// Land the check, find the drift, fix the drift; then the emitter has a baseline.
//
// WHAT IT DELIBERATELY DOES NOT CHECK. Types and formats — a schema saying
// `type: integer` for a Go `int64` is not verified here, only the property
// *names*. Type derivation is the emitter's job (the mapping is
// int64→integer/number→number/time.Time→date-time/[]byte→binary, and the
// document already follows it in all 17 `format:` lines), and checking names
// first is the order that catches the common break: a renamed or dropped json
// tag. R8.4's envelope rule ("`data` is always present, even when empty") and
// the required/optional split are checked by scripts/check-openapi.py against
// the emitted document, not here — it refuses a `required` name with no matching
// `properties` entry in the same node, which is the same assertion at the
// document level and would duplicate into two checkers for one rule.

package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// goField is one field of a parsed struct, as far as a JSON contract cares.
//
// Name is empty for an embedded field, which is the case the doc schema has to
// get right and the case a hand-written property list most often gets wrong:
// `QueryDetailView` embeds `QueryView`, so the Go side contributes sixteen
// property names through one line of Go, and an authored entry that lists them
// inline (or, worse, lists fourteen) matches the struct only if the expansion
// happens. jsonProps does the expansion; TestSchemaGoFieldResolves is what makes
// the claim in openAPISchema's doc true.
type goField struct {
	Name      string // Go field name; "" for an embedded field
	JSONName  string // from the json tag; "" means the Go name is the property name
	OmitEmpty bool
	TypeStr   string // rendered Go type: "string", "*float64", "[]HistogramBucket", "time.Time"
	Doc       string // the field's doc comment, flattened to one line
}

// PropertyName is the key encoding/json would emit for this field.
func (f goField) PropertyName() string {
	if f.JSONName != "" {
		return f.JSONName
	}
	return f.Name
}

// goStruct is one parsed struct type.
type goStruct struct {
	Name    string
	Generic bool // `type Envelope[T any] struct` — OpenAPI 3.0 cannot name it, R8.4
	Fields  []goField
	// CustomMarshal is true when the type declares its own MarshalJSON (or
	// MarshalText). Such a type's Go field names are not JSON property names at
	// all — the marshaller decides the wire shape — so the property diff does not
	// apply to it. SamplePair is the case this exists for: two fields, and a
	// document entry that correctly says `type: array` with
	// `items: {oneOf: [number, string]}`, because Prometheus encodes a sample as
	// the two-element array `[1700000000.000, "1.5"]`. Diffing that struct's fields
	// against a `properties:` block demands two properties the document must not
	// have; the first run of this file reported exactly that false mismatch.
	CustomMarshal bool
}

// parseGoPackage parses every non-test .go file in dir and returns the package's
// struct types by name plus its type aliases by name (`type QuerySeries =
// dto.QuerySeries`, which the `Go:` annotations use to name a dto type under the
// api package's own name).
//
// It mirrors routesInDir's conventions on purpose — same parser options, same
// skip-_test.go rule, same dir-as-argument shape so a fixture can be checked
// without touching the real package — because two parsers of the same package
// that disagree about what counts as a file is how a generator ends up validating
// a subset of the code and reporting success over the whole of it.
func parseGoPackage(dir string) (structs map[string]goStruct, aliases map[string]string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("openapi_schemas: read %s: %w", dir, err)
	}
	structs = map[string]goStruct{}
	aliases = map[string]string{}
	// Parse every file up front, then walk the set twice. The second walk needs
	// the first's answer: a method declaration (`func (s SamplePair) MarshalJSON`)
	// lives in the same file as its type but is not part of the ast.StructType, so
	// knowing which types have a custom marshaller has to happen before the structs
	// are built, not while they are.
	var files []*ast.File
	var parsed int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, fmt.Errorf("openapi_schemas: parse %s: %w", name, err)
		}
		parsed++
		files = append(files, f)
	}
	if parsed == 0 {
		return nil, nil, fmt.Errorf("openapi_schemas: no non-test .go files in %s", dir)
	}
	marshalers := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			if fd.Name.Name != "MarshalJSON" && fd.Name.Name != "MarshalText" {
				continue
			}
			if n, ok := recvTypeName(fd.Recv.List[0].Type); ok {
				marshalers[n] = true
			}
		}
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if st, ok := ts.Type.(*ast.StructType); ok {
					s := goStruct{Name: ts.Name.Name, Generic: ts.TypeParams != nil}
					for _, fld := range st.Fields.List {
						if len(fld.Names) == 0 {
							// Embedded: `QueryView` on its own line, no name of its own.
							s.Fields = append(s.Fields, goField{
								TypeStr: types.ExprString(fld.Type),
								Doc:     docText(fld.Doc),
							})
							continue
						}
						// A grouped declaration (`A, B string`) shares one tag and one
						// type across every name it lists.
						for _, nm := range fld.Names {
							s.Fields = append(s.Fields, goFieldFrom(nm.Name, fld))
						}
					}
					s.CustomMarshal = marshalers[ts.Name.Name]
					structs[ts.Name.Name] = s
					continue
				}
				// `type X = Y` is an alias, not a definition: ast marks it with a valid
				// Assign position. The annotations use aliases (api.QuerySeries =
				// dto.QuerySeries) to name a dto type by the api package's own name, so
				// resolving a reference has to follow them or every aliased entry reads
				// as "no such type".
				if ts.Assign.IsValid() {
					aliases[ts.Name.Name] = types.ExprString(ts.Type)
				}
			}
		}
	}
	return structs, aliases, nil
}

func goFieldFrom(name string, f *ast.Field) goField {
	gf := goField{Name: name, TypeStr: types.ExprString(f.Type), Doc: docText(f.Doc)}
	if f.Tag != nil {
		tag := reflect.StructTag(strings.Trim(f.Tag.Value, "`"))
		gf.JSONName, gf.OmitEmpty = parseJSONTag(tag.Get("json"))
	}
	return gf
}

// recvTypeName extracts the type name a method is declared on: `s SamplePair`,
// `x *SamplePair`, and the generic forms `x *Envelope[T]` / `x *Pair[T, U]`.
// The receiver's *name* is irrelevant; only the type it annotates matters, and the
// pointer is stripped because a value-receiver and a pointer-receiver method
// belong to the same named type as far as encoding/json is concerned.
func recvTypeName(e ast.Expr) (string, bool) {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name, true
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.IndexExpr: // `x *Envelope[T]`
		return recvTypeName(t.X)
	case *ast.IndexListExpr: // `x *Pair[T, U]`
		return recvTypeName(t.X)
	}
	return "", false
}

// parseJSONTag splits a json tag value into its name and its options.
//
// `-` means the field is not part of the JSON object at all, which the caller
// handles by not emitting a property for it; an *absent* tag is the opposite
// case and is not the same thing — encoding/json falls back to the Go field
// name, which is why goField.PropertyName returns the name when JSONName is
// empty. Conflating the two is how a field gets documented as `data` and served
// as `Data`.
func parseJSONTag(v string) (name string, omitEmpty bool) {
	if v == "" {
		return "", false
	}
	parts := strings.Split(v, ",")
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omitEmpty = true
		}
	}
	return parts[0], omitEmpty
}

// docText flattens a comment group to a single line: `// a` and `// b` become
// "a b". The emitter quotes field prose into a YAML block scalar; the validator
// only carries it so the two agree on what a field's description is.
func docText(c *ast.CommentGroup) string {
	if c == nil {
		return ""
	}
	var lines []string
	for _, l := range c.List {
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(l.Text, "//")))
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

// resolveGoRef turns a `Go:` annotation value into the struct it names.
//
// The accepted grammar is `pkg.Name`, `pkg.Base[pkg.Arg]` (a generic
// instantiation — OpenAPI 3.0 has no generics, so R8.4's rule is one named
// envelope schema per resource and the annotation names the instantiation while
// the *definition* lives on the generic base), and anything reachable from those
// through a type alias. The package set is closed to the two the annotations
// actually use, dto and api: a third package would have to be added here on
// purpose, because silently resolving a reference the generator has never seen
// is how a check starts passing over code it is not looking at.
func resolveGoRef(ref string, pkgs map[string]map[string]goStruct, aliasPkgs map[string]map[string]string) (goStruct, bool) {
	return resolveGoRefDepth(ref, pkgs, aliasPkgs, 0)
}

func resolveGoRefDepth(ref string, pkgs map[string]map[string]goStruct, aliasPkgs map[string]map[string]string, depth int) (goStruct, bool) {
	if depth > 8 {
		return goStruct{}, false
	}
	ref = strings.TrimSpace(ref)
	pkg, name, ok := splitQualified(ref)
	if !ok {
		return goStruct{}, false
	}
	// An alias wins over a struct name: `type QuerySeries = dto.QuerySeries` is
	// resolved before looking for a struct of that name, so a package that
	// re-exports a dto type resolves to the dto definition rather than to itself.
	if target, ok := aliasPkgs[pkg][name]; ok {
		return resolveGoRefDepth(target, pkgs, aliasPkgs, depth+1)
	}
	if s, ok := pkgs[pkg][name]; ok {
		return s, true
	}
	// Generic instantiation: `Envelope[dto.ClusterView]` is defined as
	// `Envelope[T any]`, so the bracketed form is not a name in the file.
	if base, _, ok := splitGeneric(name); ok {
		if s, ok := pkgs[pkg][base]; ok {
			return s, true
		}
	}
	return goStruct{}, false
}

// splitQualified splits "dto.ClusterView" into ("dto", "ClusterView").
func splitQualified(ref string) (pkg, name string, ok bool) {
	i := strings.Index(ref, ".")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// splitGeneric splits "Envelope[dto.ClusterView]" into ("Envelope", "dto.ClusterView").
func splitGeneric(name string) (base, arg string, ok bool) {
	i := strings.IndexByte(name, '[')
	if i <= 0 || !strings.HasSuffix(name, "]") {
		return "", "", false
	}
	return name[:i], name[i+1 : len(name)-1], true
}

// jsonProps is every property name a struct contributes to its JSON object,
// with embedded structs expanded and the result sorted.
//
// The depth cap and the cycle guard are not decoration: `type A struct { B }`
// and `type B struct { A }` compile, and an unbounded expansion of an embedded
// cycle turns this function into the reason `go test` hangs rather than the
// reason it fails.
func jsonProps(s goStruct, pkg string, pkgs map[string]map[string]goStruct) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(goStruct, string, int)
	walk = func(s goStruct, pkg string, depth int) {
		if depth > 8 {
			return
		}
		for _, f := range s.Fields {
			if f.Name == "" {
				// Embedded field: its properties are the embedded type's properties.
				// The star is stripped — `*CycleB` embeds a pointer, but the embedded type
				// is still *named* CycleB — and a generic instantiation resolves to its
				// base, for the same reason resolveGoRef splits brackets. Without the
				// strip, a pointer embed silently contributes nothing: the fixture's
				// CycleA lost every property it reached through `*CycleB`.
				embed := strings.TrimPrefix(f.TypeStr, "*")
				if base, _, ok := splitGeneric(embed); ok {
					embed = base
				}
				epkg, ename, ok := splitQualified(embed)
				if !ok {
					// Unqualified (`QueryView` inside package dto) — same package.
					epkg, ename = pkg, embed
				}
				if es, ok := pkgs[epkg][ename]; ok {
					walk(es, epkg, depth+1)
					continue
				}
				// An embedded type the generator cannot resolve contributes nothing
				// and is reported by checkSchemasInDirs through the entry that names it.
			}
			n := f.PropertyName()
			if n == "" || n == "-" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	walk(s, pkg, 0)
	sort.Strings(out)
	return out
}

// yNode is one node of the YAML subset the annotation entries are written in:
// mappings (`key:` or `key: value`) and sequences (`- key: value`, `-`, and
// nested combinations). It is a parser rather than a line scan because the two
// shapes the document uses — a plain schema, and an `allOf` composition whose
// members carry their own `properties:` — need the *tree*, and a scan that
// approximates a tree with indent arithmetic silently misreads the third shape it
// meets. The first version of this file did exactly that and reported every entry
// as documenting nothing.
type yNode struct {
	key      string // "" for a sequence item; "$ref" for a reference; "properties" for a property block
	value    string // text after ": " ("" when the value is a nested block)
	children []yNode
}

// normalizeLines rewrites every inline sequence item (`- type: object`) into a
// bare `-` plus its content indented two deeper, so parseBlock sees one grammar:
// a line is either a bare sequence marker or a `key: value` mapping entry.
// Without this, `- type: object` at indent 8 and `type: object` at indent 10 are
// two spellings of the same node and the parser has to know both.
func normalizeLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			out = append(out, l)
			continue
		}
		if strings.HasPrefix(t, "- ") {
			ind := indentOf(l)
			out = append(out,
				strings.Repeat(" ", ind)+"-",
				strings.Repeat(" ", ind+2)+strings.TrimSpace(t[2:]),
			)
			continue
		}
		out = append(out, l)
	}
	return out
}

// parseBlock parses the mapping or sequence whose members sit at indent `want`,
// starting at index i, and returns its nodes plus the index of the first line
// that is not part of the block.
//
// A line indented *past* want is content belonging to the node just parsed — a
// block scalar body — and is skipped rather than ending the block. Stopping there
// instead is the bug the first version shipped with: every entry's
// `description: |` is followed by prose at the next indent, so a scan that ends a
// mapping at the first deeper line stops inside the description and never reaches
// the `properties:` key below it. All 32 entries then read as documenting nothing,
// which is a failure that looks like a finding rather than a parser bug — and is
// the reason this comment is here.
func parseBlock(lines []string, i, want int) ([]yNode, int) {
	var out []yNode
	for i < len(lines) {
		raw := lines[i]
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			i++
			continue
		}
		in := indentOf(raw)
		if in < want {
			return out, i
		}
		if in > want {
			i++ // content of the previous node, not a sibling
			continue
		}
		if t == "-" {
			item := yNode{}
			kids, next := parseBlock(lines, i+1, want+2)
			item.children = kids
			// `- $ref: '...'` normalises to a bare `-` whose only child is the $ref
			// key; surface that key on the item so a sequence of refs reads as refs.
			if len(kids) == 1 {
				item.key, item.value = kids[0].key, kids[0].value
			}
			out = append(out, item)
			i = next
			continue
		}
		key, value, ok := splitKV(t)
		if !ok {
			i++ // a scalar line with no colon: block-scalar body reached directly
			continue
		}
		n := yNode{key: key, value: value}
		if value == "" {
			kids, next := parseBlock(lines, i+1, want+2)
			n.children = kids
			i = next
		} else {
			i++
		}
		out = append(out, n)
	}
	return out, i
}

// splitKV splits `key: value` into its two halves. A key is anything up to the
// first colon that contains no space, which admits `$ref` and rejects a prose
// line from a block scalar (`Composed with `allOf“ has no colon before a space).
func splitKV(t string) (key, value string, ok bool) {
	i := strings.IndexByte(t, ':')
	if i <= 0 {
		return "", "", false
	}
	key = t[:i]
	if strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.TrimSpace(t[i+1:]), true
}

// entrySchema parses an entry's Lines and returns its schema node (the key at
// indent 4, which is the schema name) — the shape every openAPISchema.Lines has.
func entrySchema(lines []string) (yNode, bool) {
	norm := normalizeLines(lines)
	nodes, _ := parseBlock(norm, 0, 4)
	if len(nodes) == 0 {
		return yNode{}, false
	}
	return nodes[0], true
}

// schemaProps collects the property names a schema node declares, and the schema
// names it references, through `properties:` blocks and through `allOf` members
// alike.
//
// The allOf walk is what makes the composition checkable: QueryDetailView's
// document half is `$ref: QueryView` plus five properties, and its Go half is an
// embedded QueryView plus five fields. Comparing the five to the twenty-one is a
// false mismatch, so the reference is followed and its properties counted — the
// same expansion the Go side does for the embedded field, from the other side.
func schemaProps(n yNode, doc map[string]yNode, seen map[string]bool) (props, refs []string) {
	for _, c := range n.children {
		switch {
		case c.key == "properties":
			for _, p := range c.children {
				if p.key != "" {
					props = append(props, p.key)
				}
			}
		case c.key == "allOf":
			for _, item := range c.children {
				p, r := schemaProps(item, doc, seen)
				props = append(props, p...)
				refs = append(refs, r...)
			}
		case c.key == "$ref":
			target := refTarget(c.value)
			refs = append(refs, target)
			if doc != nil && !seen[target] {
				if seen == nil {
					seen = map[string]bool{}
				}
				seen[target] = true
				if sub, ok := doc[target]; ok {
					p, r := schemaProps(sub, doc, seen)
					props = append(props, p...)
					refs = append(refs, r...)
				}
			}
		}
	}
	return props, refs
}

// docSchemaProps is every authored entry's full property set, $refs expanded.
func docSchemaProps(entries []openAPISchema) map[string][]string {
	doc := map[string]yNode{}
	for _, e := range entries {
		if n, ok := entrySchema(e.Lines); ok {
			doc[e.Name] = n
		}
	}
	out := map[string][]string{}
	for _, e := range entries {
		n, ok := doc[e.Name]
		if !ok {
			continue
		}
		props, _ := schemaProps(n, doc, map[string]bool{e.Name: true})
		sort.Strings(props)
		out[e.Name] = dedupe(props)
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func indentOf(l string) int {
	return len(l) - len(strings.TrimLeft(l, " "))
}

// refTarget extracts the schema name from a `$ref` value, quoted or not:
// `'#/components/schemas/Meta'` and `#/components/schemas/Meta` both give `Meta`.
func refTarget(v string) string {
	v = strings.Trim(strings.TrimSpace(v), "'\"")
	i := strings.LastIndex(v, "/")
	if i < 0 {
		return v
	}
	return v[i+1:]
}

// checkSchemas is GenerateOpenAPI's schema-side gate, over the real package and
// the real dto directory.
func checkSchemas() []string { return checkSchemasInDirs(".", "dto", openAPISchemas) }

// checkSchemasInDirs is the same over any pair of directories and any entry
// list, so the test can pin the failure cases against fixtures the way
// routesInDir does for routes. An entry list argument rather than a hardcoded
// read of openAPISchemas is what lets a fixture entry that *should* fail exist
// without editing the real table.
func checkSchemasInDirs(apiDir, dtoDir string, entries []openAPISchema) []string {
	apiStructs, apiAliases, err := parseGoPackage(apiDir)
	if err != nil {
		return []string{err.Error()}
	}
	dtoStructs, dtoAliases, err := parseGoPackage(dtoDir)
	if err != nil {
		return []string{err.Error()}
	}
	pkgs := map[string]map[string]goStruct{"api": apiStructs, "dto": dtoStructs}
	aliasPkgs := map[string]map[string]string{"api": apiAliases, "dto": dtoAliases}
	docProps := docSchemaProps(entries)

	var problems []string
	for _, e := range entries {
		ref := strings.TrimSpace(e.Go)
		if ref == "" {
			problems = append(problems, fmt.Sprintf("schema %s: empty Go field — every entry must name the type it documents (R8.1b)", e.Name))
			continue
		}
		pkg, _, ok := splitQualified(ref)
		if !ok {
			problems = append(problems, fmt.Sprintf("schema %s: Go %q is not pkg.Name", e.Name, ref))
			continue
		}
		s, ok := resolveGoRef(ref, pkgs, aliasPkgs)
		if !ok {
			problems = append(problems, fmt.Sprintf("schema %s: Go %q resolves to no type in %s (R8.1b: an authored schema naming a type that does not exist is a generator error)", e.Name, ref, pkg))
			continue
		}
		if s.CustomMarshal {
			// The reference resolved, which is the half R8.1b actually asserts. The
			// property diff does not apply: a type with its own MarshalJSON has a
			// hand-written wire shape and its Go field names are not JSON property
			// names — see goStruct.CustomMarshal for the SamplePair case this covers.
			continue
		}
		want := jsonProps(s, pkg, pkgs)
		got := docProps[e.Name]
		missing, extra := diffNames(want, got)
		if len(missing) > 0 || len(extra) > 0 {
			problems = append(problems, fmt.Sprintf("schema %s (Go %s): properties differ from the struct's json tags — in Go not in doc: [%s]; in doc not in Go: [%s] (R16.8)",
				e.Name, ref, strings.Join(missing, " "), strings.Join(extra, " ")))
		}
	}
	sort.Strings(problems)
	return problems
}

// diffNames returns the names in `want` that are absent from `got`, and the names
// in `got` that are absent from `want`, both sorted. Two lists rather than one
// symmetric diff because the two halves mean different things to fix: a name in
// Go and not in the doc is an undocumented field, and a name in the doc and not
// in Go is a documented field the server does not serve.
func diffNames(want, got []string) (missing, extra []string) {
	w := map[string]bool{}
	g := map[string]bool{}
	for _, n := range want {
		w[n] = true
	}
	for _, n := range got {
		g[n] = true
	}
	for _, n := range want {
		if !g[n] {
			missing = append(missing, n)
		}
	}
	for _, n := range got {
		if !w[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}
