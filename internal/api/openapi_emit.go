// openapi_emit.go is the emit half of R8.1's input 2: given a Go type, produce the
// document lines that describe it.
//
// The check half (openapi_schemas.go) answers "does this authored entry match the
// struct it names?". The emit half answers the other question — "what would the
// entry be?" — and the two are separated because they land at different times. The
// check had to land first: deleting an authored entry is only safe once you know
// what it said, and every type the emitter gets wrong is a schema the document
// published, not a test that went red.
//
// # Per-type migration, not big-bang
//
// openAPISchema.Generated is a per-entry opt-in. An entry that is not marked
// Generated is emitted from its authored Lines exactly as before, so the emitter can
// be merged while describing nothing; each later commit flips one type, and the
// document changes by exactly that type's schema block. Flipping a type whose
// generated schema turns out worse than the authored prose reverts one entry instead
// of the emitter, which is the property a big-bang swap has no way to get.
//
// The readiness rule is the one the D-round ruling set: a type is generated only
// when its Go type has doc comments. A type with no prose keeps its authored entry,
// because generating `type: object` plus four bare property names over a struct with
// no comments replaces a documented schema with a skeleton, and the skeleton is what
// the annotations file exists to prevent.
//
// # Where the rendering rules come from
//
// Every convention below is measured from the committed document rather than
// invented — the required-list style, the property key order, the wrap width, the
// inline-vs-block description rule, the bare `$ref` and its single-member `allOf:`
// wrapper (see propSchema.lines for the pair that is a spec rule rather than a style).
// Where the authored file is inconsistent with itself — it quotes some short
// descriptions and not others — the emitter applies one rule and the difference is part
// of what a flip's diff shows.

package api

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// proseWidth is the column the document's prose wraps at. Measured, not chosen: at
// indent 8 the description lines cluster at 78-79 characters with a maximum of 87
// (a line carrying a long backticked identifier the author refused to break), so 80
// is the rule the file follows and the 87s are its exceptions.
const proseWidth = 80

// maxLineWidth is the longest line the document lets itself write, which is not the
// same measurement as proseWidth and was rendered as if it were. Measured over the
// 29 entries with a `required:` key: every flow form up to and including 87 columns
// stays on one line, and the three that break to one name per line are 107, 188 and
// 229 — all past it. InstanceView's is exactly 87, and a rule that measured prose
// (80) rendered it as a block, which is the difference between that entry's
// authored one line and the emitter's seven. The same constant governs `enum:`
// flow-or-block; the enum blocks that survive at 63 columns are the ones with
// per-value comments, which a flow list cannot hold, so they arrive through RawEnum
// rather than through this test.
const maxLineWidth = 87

// schemaContext is one parse of the two packages a `Go:` annotation can name, plus
// the entry table, plus the index from Go reference to schema name. GenerateOpenAPI
// builds one and hands it to both halves: check() for the R16.8 property diff and
// linesFor() for the entry bodies.
//
// Building it once is the point. A schema validated against one parse of the
// packages and rendered from another is the same class of bug as a second copy of a
// contract: the two halves can be individually correct and still describe different
// code.
type schemaContext struct {
	entries []openAPISchema
	pkgs    map[string]map[string]goStruct
	aliases map[string]map[string]string

	// names maps a Go reference ("dto.HistogramBucket") to the schema name an entry
	// uses for it ("HistogramBucket").
	names map[string]string
}

// newSchemaContext parses apiDir and dtoDir and indexes entries against them.
func newSchemaContext(apiDir, dtoDir string, entries []openAPISchema) (schemaContext, error) {
	pkgs, aliases, err := parseSchemaInputs(apiDir, dtoDir)
	if err != nil {
		return schemaContext{}, err
	}
	names, err := indexNames(entries, pkgs, aliases)
	if err != nil {
		return schemaContext{}, err
	}
	return schemaContext{entries: entries, pkgs: pkgs, aliases: aliases, names: names}, nil
}

// indexNames builds the Go-reference → schema-name index.
//
// The authored `Go:` string is not the only name a type has. `api.X = dto.X` aliases
// (five of the 41 entries that name a Go type: series.go's QuerySeriesEnvelope,
// QuerySeriesPoint and QuerySeries, tags.go's TagsEnvelope, query_plans.go's
// QueryPlansEnvelope) mean a field can name the same type two ways, and a table
// keyed only by the authored spelling makes the other one look undocumented:
// `dto.QuerySeries` has a field of type `QuerySeriesPoint`, which resolves to
// `dto.QuerySeriesPoint`, while the entry that documents it is written
// `api.QuerySeriesPoint`. Registering the canonical form as well is what lets the
// reference resolve.
//
// It takes two passes because the two kinds of claim have different precedence, and
// one interleaved loop lets table order pick which rule applies. A direct claim is
// an entry speaking for itself; a derived claim is the emitter inferring a second
// name for it. Interleaved, a later entry's direct assignment overwrote an earlier
// entry's derived registration (right, but only because that assignment was
// unguarded), while two derived claims resolved first-come — i.e. by table order.
// Split, the precedence is a property of the code:
//
//   - pass one claims every entry's authored reference, and a reference two entries
//     both author is a table defect, reported rather than silently reassigned (R8.4's
//     one-schema-per-type, checked at the index instead of left to the byte-compare);
//   - pass two adds derived references only where nothing already claimed them, so a
//     direct claim from any entry always beats a derived claim from any other.
func indexNames(entries []openAPISchema, pkgs map[string]map[string]goStruct, aliases map[string]map[string]string) (map[string]string, error) {
	names := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.Go == "" {
			continue
		}
		if prev, exists := names[e.Go]; exists {
			return nil, fmt.Errorf("schema entries %q and %q both author Go type %q (R16.8)", prev, e.Name, e.Go)
		}
		names[e.Go] = e.Name
	}
	for _, e := range entries {
		if e.Go == "" {
			continue
		}
		canon, ok := canonicalRef(e.Go, pkgOf(e.Go), pkgs, aliases)
		if !ok || canon == e.Go {
			continue
		}
		if _, exists := names[canon]; exists {
			continue
		}
		names[canon] = e.Name
	}
	return names, nil
}

// pkgOf is the package half of a qualified reference, and "" for an unqualified one
// (which the check already rejects, so canonicalRef's own lookup then fails rather
// than inventing a package).
func pkgOf(ref string) string {
	p, _, ok := splitQualified(ref)
	if !ok {
		return ""
	}
	return p
}

// check is the check half, over this context's parse. It is a method rather than a
// call to checkSchemas() so that GenerateOpenAPI cannot validate one parse and emit
// another.
func (c schemaContext) check() []string { return checkEntries(c.pkgs, c.aliases, c.entries) }

// linesFor renders one entry's document lines.
//
// An entry that is not marked Generated returns its authored Lines unchanged — that
// is the per-type migration rule, and it is why this function is total over the
// whole table rather than defined only over the generated subset.
func (c schemaContext) linesFor(e openAPISchema) ([]string, error) {
	if !e.Generated {
		return e.Lines, nil
	}
	if e.Go == "" {
		return nil, fmt.Errorf("schema %s: empty Go field (R8.1b)", e.Name)
	}
	pkg, _, ok := splitQualified(e.Go)
	if !ok {
		return nil, fmt.Errorf("schema %s: Go %q is not pkg.Name", e.Name, e.Go)
	}
	// The canonical form, not the authored string, is what the rest of the render
	// works from: an entry written `api.QuerySeriesPoint` documents a dto type, and
	// rendering from the api spelling would resolve the struct's own fields against
	// package api, where they are not defined.
	ref, ok := canonicalRef(e.Go, pkg, c.pkgs, c.aliases)
	if !ok {
		return nil, fmt.Errorf("schema %s: Go %q resolves to no type (R8.1b)", e.Name, e.Go)
	}
	pkg, _, _ = splitQualified(ref)
	s, ok := resolveGoRef(ref, c.pkgs, c.aliases)
	if !ok {
		return nil, fmt.Errorf("schema %s: Go %q resolves to no type (R8.1b)", e.Name, ref)
	}
	if s.CustomMarshal {
		return nil, fmt.Errorf("schema %s (Go %s): the type declares its own marshaller, so its wire shape is hand-written and not derivable from its fields — keep the entry authored",
			e.Name, e.Go)
	}
	if s.Doc == "" {
		return nil, fmt.Errorf("schema %s (Go %s): the type has no doc comment — the per-type migration keeps an entry authored until its Go type carries prose (R8.1 input 2)",
			e.Name, e.Go)
	}
	// Two body shapes, and the entry itself says which one it is (see entryHasAllOf):
	// the flat `type: object`, and the entry-level `allOf:` composition.
	var body []string
	var err error
	if entryHasAllOf(e) {
		body, err = c.composedBody(e, s, pkg, typeArgs(ref, s), authoredProseFor(e))
	} else {
		body, err = c.objectBody(s, pkg, typeArgs(ref, s), authoredProseFor(e))
	}
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", e.Name, err)
	}
	header := entryHeader(e)
	out := make([]string, 0, len(header)+1+len(body))
	out = append(out, header...)
	out = append(out, "    "+e.Name+":")
	return append(out, body...), nil
}

// entryHeader returns the lines an authored entry carries before its `    Name:`
// key — the section banner comment and the blank lines around it. A generated entry
// keeps them: the banners organise the schemas section into the same groups the
// annotations file uses, and the emitter has no other way to know them. An entry
// with no recognizable key line gets a bare blank line, which is the separator every
// entry in the document starts with.
func entryHeader(e openAPISchema) []string {
	key := "    " + e.Name + ":"
	for i, l := range e.Lines {
		if l == key {
			return e.Lines[:i]
		}
	}
	return []string{""}
}

// entryHasAllOf reports whether an authored entry's *body* is an allOf composition:
// the entry-level form, `    Name:` followed by `      allOf:`, which composes other
// schemas instead of listing properties. QueryDetailView's entry is that form — it
// composes QueryView by $ref and adds its own members, which is derivable in Go (the
// struct embeds it) but not by this emitter yet, because the flat renderer would inline
// the embedded properties instead and change the entry's shape as well as its prose.
// Refusing is the honest answer: the entry stays authored until the emitter learns
// composition.
//
// The check is scoped to indent 6, and a TrimSpace-only version misses why: `allOf:`
// also occurs at indent 10 as a *property's* schema, in the single-member wrapper
// OpenAPI 3.0 requires for attaching a description to a $ref (see propSchema.lines).
// The flat renderer does support that form, so the unscoped check refused PromQLSeries
// for a shape it can already render — and a gate that refuses more than it must is a
// gate nobody audits, because its failures stop being read.
func entryHasAllOf(e openAPISchema) bool {
	for _, l := range e.Lines {
		if indentOf(l) == 6 && strings.TrimSpace(l) == "allOf:" {
			return true
		}
	}
	return false
}

// typeArgs binds a generic instantiation's arguments to the base's parameter names:
// `dto.Envelope[dto.ClusterView]` over `Envelope[T any]` gives T → dto.ClusterView.
// A non-generic ref yields an empty map, which every caller reads as "no
// substitution". Without this the renderer meets a field whose declared type is the
// letter `T` and reports it as an unresolvable type — the check cannot see the
// problem because it compares property *names*, and `data` is a correct property
// name whichever type it holds.
func typeArgs(ref string, s goStruct) map[string]string {
	binds := map[string]string{}
	_, args, ok := splitGeneric(ref)
	if !ok || args == "" || len(s.Params) == 0 {
		return binds
	}
	// `Envelope[K, V]` is rare but legal; the split is on commas because an argument
	// is a type expression, not a quoted string, and the check already rejects a
	// malformed one.
	parts := strings.Split(args, ",")
	for i, p := range s.Params {
		if i >= len(parts) {
			break
		}
		if a := strings.TrimSpace(parts[i]); a != "" {
			binds[p] = a
		}
	}
	return binds
}

// authoredProse is the prose an authored entry carries for one owner: the schema
// itself (key "") or one of its properties. The emitter copies these lines instead of
// rendering them, for the reason written on propSchema.RawDesc.
type authoredProse struct {
	Desc    []string
	Example []string
	// Enum holds the authored `enum:` block verbatim, for the same reason Desc and
	// Example do: an enum's per-value comments are prose, not structure. The derived
	// propSchema.Enum can only ever hold the value list, and the document annotates
	// the values it names — `- PromQLUnavailable # R8.3: query outside the named
	// catalogue` in ErrorDetail, `# --credential-secret` in ClusterView — which no
	// reading of a Go field produces. The entry's list is copied; the derived one is
	// the fallback when the entry has no list at all.
	Enum []string
	// Format and Nullable are the authored `format:` and `nullable:` values, on the
	// same argument one more time. PromQLQueryRequest's startTime is a Go string the
	// document documents as a timestamp (`format: date-time`, R10.21's wire format),
	// which the type table cannot know because the field is not a time.Time.
	//
	// Nullable is here because pointer-ness turned out not to be the document's rule.
	// The key belongs to a pointer that *emits* null — a `*T` with no `omitempty` —
	// and not to one that omits the key, because `nullable: true` on an `omitempty`
	// field documents a JSON null the encoder cannot produce. R3.1 ruled it for
	// `installed` (c4e714e) and A3's S100 generalised the ruling to NamespaceFailure's
	// lastSuccessTime and InstanceView's startedAt, both `*time.Time` with `omitempty`
	// and both now stripped here. What the document keeps is three: ClusterView's
	// pgMajor (`*int`, dto/clusters.go:165) and QuerySeriesPoint's lockTimeSeconds and
	// ioTimeSeconds (`*float64`, dto/series.go:75-76) — none of the three carries
	// `omitempty`. The emitter used to derive the key from `*T`, which rendered it
	// onto LatencyView's three `*float64` percentiles as well. Their own entry
	// description says why not: `omitempty` already spells "no p95" as an absent
	// field, so marking them nullable would be a second, contradicting answer to a
	// question the entry answered elsewhere. The value is the authored string
	// ("true"/"false", "" when the entry wrote no `nullable:`) so the line is copied
	// rather than re-derived; propSchema.Nullable is the fallback for a property the
	// entry left undocumented, and authoredAny is what tells the two apart.
	Format   string
	Nullable string
	// OneOf is the authored `oneOf:` block, on the same footing as Enum one last time:
	// it is a list the entry writes and no Go type supplies. PromQLResult.result is a
	// json.RawMessage — which has no type to render — and the entry names in three
	// branches what the field can hold (an array of PromQLSeries, a number, a string),
	// because OpenAPI 3.0 cannot tie a property's type to a sibling's value and
	// Prometheus really does emit a bare number for a `scalar` result. The emitter has
	// no rule that derives a oneOf from a Go type, so the entry's block is the only
	// source there is, and copying it is the whole rule.
	OneOf []string
}

// authoredAny reports whether the entry documented this owner at all — the test that
// separates an authored entry from a fresh one, at both levels. An entry that wrote an
// enum and no description wrote no description on purpose, and an entry that wrote a
// type and no `nullable:` on a pointer field wrote none on purpose; the derived render
// fills the slots of an owner the entry said nothing about, and nothing else.
func (p authoredProse) authoredAny() bool {
	return len(p.Desc) > 0 || len(p.Example) > 0 || len(p.Enum) > 0 ||
		len(p.OneOf) > 0 || p.Format != "" || p.Nullable != ""
}

// authoredProseFor indexes one entry's authored prose by owner, picking the indent
// model from the entry's own shape.
func authoredProseFor(e openAPISchema) map[string]authoredProse {
	if entryHasAllOf(e) {
		return authoredProseForShape(e, composedShape)
	}
	return authoredProseForShape(e, flatShape)
}

// proseShape is the pair of indents authoredProseForShape reads owners from. A flat
// entry puts its own keys at 6 and its property keys at 8; an entry composed with
// `allOf` keeps its own keys at 6 and gains two levels before its properties — the
// sequence dash at 8, `type: object` at 10 — so its property keys sit at 12 and their
// schemas at 14. The pair is derived from the entry's shape rather than a second parser
// written for the second shape, because "which prose belongs to which owner" is one
// contract and two parsers over it is this file's recurring failure (R16.8): the
// first version of this function recognised property keys at indent 8 only, so every
// property of a composed entry parsed as "inside a nested block", and flipping such an
// entry would have rendered its properties from the Go doc while the entry's own
// curated text sat uncaptured.
type proseShape struct {
	schemaKeys int // indent of the schema's own keys (`description:`, `allOf:`)
	propKeys   int // indent of a property key line (`user:`)
}

var (
	flatShape     = proseShape{schemaKeys: 6, propKeys: 8}
	composedShape = proseShape{schemaKeys: 6, propKeys: 12}
)

// authoredProseForShape is the parser; authoredProseFor is the one caller that decides
// which shape an entry is.
//
// The owner is read from the indentation, which is the only place the information
// lives: at indent 4 is the schema key, at sp.schemaKeys the schema's own keys
// (type/description/required/properties/example), at sp.propKeys a property key, and
// deeper than that the property's own schema. A block deeper than a property's own
// keys belongs to a nested items: block rather than to the property, and the first
// capture for an owner wins, which is the property's own — the nested one comes after
// it in the entry.
func authoredProseForShape(e openAPISchema, sp proseShape) map[string]authoredProse {
	out := map[string]authoredProse{}
	owner := ""
	for i := 0; i < len(e.Lines); i++ {
		l := e.Lines[i]
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		ind := indentOf(l)
		switch {
		case ind <= 4:
			owner = ""
			continue
		case ind == sp.schemaKeys:
			owner = ""
		case ind == sp.propKeys:
			if !strings.HasSuffix(trimmed, ":") {
				continue
			}
			name := strings.TrimSuffix(trimmed, ":")
			if name == "properties" || name == "required" || name == "allOf" {
				continue
			}
			owner = name
			continue
		default:
			if owner == "" {
				continue // inside a nested items:/additionalProperties: block
			}
		}
		// The keys captured are exactly authoredProse's six fields. `type:` and
		// `required:` are not among them: they are structure the emitter derives from
		// the Go type, and copying them would let a marked entry keep a type its struct
		// no longer has — which is the whole claim `Generated: true` makes.
		isDesc := strings.HasPrefix(trimmed, "description:")
		isExample := strings.HasPrefix(trimmed, "example:")
		isEnum := strings.HasPrefix(trimmed, "enum:")
		isFormat := strings.HasPrefix(trimmed, "format:")
		isNullable := strings.HasPrefix(trimmed, "nullable:")
		isOneOf := strings.HasPrefix(trimmed, "oneOf:")
		if !isDesc && !isExample && !isEnum && !isFormat && !isNullable && !isOneOf {
			continue
		}
		block, n := rawProseBlock(e.Lines, i)
		// A map holds values, not variables: the edit happens on a copy that is
		// written back, which is also why the first block for an owner wins.
		a := out[owner]
		changed := false
		if isDesc && len(a.Desc) == 0 {
			a.Desc, changed = block, true
		}
		if isExample && len(a.Example) == 0 {
			a.Example, changed = block, true
		}
		if isEnum && len(a.Enum) == 0 {
			a.Enum, changed = block, true
		}
		// format and nullable are the two inline-only keys — `format: date-time` and
		// `nullable: true` are scalars, so the value is the key line's right-hand side
		// rather than a block to copy. The rendered line is rebuilt from it verbatim.
		if isFormat && a.Format == "" {
			a.Format, changed = inlineValue(trimmed), true
		}
		if isNullable && a.Nullable == "" {
			a.Nullable, changed = inlineValue(trimmed), true
		}
		// oneOf is the block form, captured the way enum is: the entry's branch list is
		// the content and rawProseBlock consumes it whole.
		if isOneOf && len(a.OneOf) == 0 {
			a.OneOf, changed = block, true
		}
		if changed {
			out[owner] = a
		}
		i += n - 1
	}
	return out
}

// inlineValue is the right-hand side of an authored inline `key: value` line, taken
// verbatim: `format: date-time` yields date-time, `nullable: true` yields true. It is
// not inlineScalar — that one renders a Go value and decides whether to quote it, and
// an authored value already carries the quoting its author chose, so running it back
// through that rule would re-quote a string the document quoted by hand.
func inlineValue(line string) string {
	_, v, _ := strings.Cut(line, ":")
	return strings.TrimSpace(v)
}

// rawProseBlock returns the authored lines that make up the `description:`,
// `example:`, `enum:` or `oneOf:` key at index i, and n, how many lines the block
// consumed, so the caller skips the body instead of re-reading it as a key. Three
// shapes occur in the document and each has to be recognised:
//
//   - a block scalar (`description: |`) — the prose is the deeper-indented lines
//     under the key, blank lines included when the scalar continues after them;
//   - a bare key whose value is a nested mapping (`example:` followed by
//     `onepg-server: "2026-12-22T08:39:48Z"`, `oneOf:` followed by its `- type:`
//     branches) — the mapping *is* the value, and reading it as the inline form
//     captures the key and drops the map, which is how four entries' schema examples
//     vanished from the render;
//   - the inline form (`example: 42`, `description: One line.`, `nullable: true`) —
//     the value is on the key line and the block is that one line.
func rawProseBlock(lines []string, i int) ([]string, int) {
	l := lines[i]
	r := strings.TrimRight(l, " \t")
	switch {
	case strings.HasSuffix(r, "|"), strings.HasSuffix(r, ">"), strings.HasSuffix(r, ":"):
		// block scalar or nested mapping: both continue on the deeper-indented lines
	default:
		return []string{l}, 1 // inline form: the value is on the key line
	}
	ind := indentOf(l)
	out := []string{l}
	for j := i + 1; j < len(lines); j++ {
		nl := lines[j]
		if nl == "" {
			if !deeperAfter(lines, j+1, ind) {
				break
			}
			out = append(out, nl)
			continue
		}
		if indentOf(nl) <= ind {
			break
		}
		out = append(out, nl)
	}
	return out, len(out)
}

// deeperAfter reports whether the next non-empty line at or after k is indented
// further than ind.
func deeperAfter(lines []string, k, ind int) bool {
	for ; k < len(lines); k++ {
		if lines[k] != "" {
			return indentOf(lines[k]) > ind
		}
	}
	return false
}

// objectBody renders everything under the `    Name:` line for a flat object
// schema: type, description, required, properties, in the order the document's flat
// entries use. binds carries a generic instantiation's argument substitutions (see
// typeArgs); a carries the entry's authored prose, which the emitter copies rather
// than re-renders.
func (c schemaContext) objectBody(s goStruct, pkg string, binds map[string]string, a map[string]authoredProse) ([]string, error) {
	// Prose comes from the entry (R8.1's input 4); the Go doc is the fallback only for
	// an entry that carries no authored prose at all, which is the fresh-entry case
	// the per-type migration rule is written for. An entry with a description but no
	// example, or the reverse, is a choice the emitter copies rather than
	// second-guesses, and so is an entry that documents no schema-level description at
	// all: ErrorDetail has none because its parent ErrorBody carries one, ServicePort
	// has none because the port list says everything the type needs. Measured over the
	// document those two are the only entries without a schema description, and both
	// have property prose — so "the entry authored anything anywhere" is what separates
	// an authored entry from a fresh one, and rendering the Go doc into their empty slot
	// adds a line the document deliberately omits.
	return c.objectCore(flattenFields(s, pkg, c.pkgs), pkg, binds, a, schemaDesc(s, a, 6), a[""].Example, 6)
}

// schemaDesc resolves the schema-level description an entry renders: the authored block
// if it wrote one, the Go doc if it authored nothing at all, nothing if it authored
// something else and left this slot empty. objectBody and composedBody both need it and
// both pass indent 6, because in both shapes the description sits at the entry's own
// level — a composed entry's `type: object` member gets no description of its own, which
// is why objectCore takes the prose as an argument rather than deriving it from `a`.
func schemaDesc(s goStruct, a map[string]authoredProse, indent int) []string {
	self := a[""]
	switch {
	case len(self.Desc) > 0:
		return self.Desc
	case len(a) == 0:
		// The entry authored nothing at all, so this is the fresh-entry case and the Go
		// doc is the only prose available.
		prose, _, _ := splitMarkers(s.DocLines)
		return descLines(prose, indent)
	}
	return nil
}

// objectCore renders an object schema's keys — type, description, required, properties
// and example — at `indent`, from an explicit field list. objectBody is the flat caller
// (flatten the Go struct, derive the prose the entry left empty, indent 6); composedBody
// is the composed caller (one allOf member's fields, the entry's prose already resolved,
// indent 10 under the member's dash). Splitting the render from the field discovery is
// what keeps the property loop — the authored-wins rules, the flow-vs-block required
// line, the empty-inner rule, the example-last placement — in one place. A second copy
// of that loop is this file's recurring failure (R16.8), and a second caller is what
// would have forced one.
func (c schemaContext) objectCore(fields []goFieldRef, pkg string, binds map[string]string, a map[string]authoredProse, desc, example []string, indent int) ([]string, error) {
	pad := strings.Repeat(" ", indent)
	out := []string{pad + "type: object"}
	out = append(out, desc...)
	if req := requiredNames(fields); len(req) > 0 {
		// Flow style (`required: [a, b]`) when the list fits the line, block style
		// (one `- name` per line) when it does not. Measured: the document uses flow
		// 26 times and block 3 times, and the three block cases are exactly the
		// entries whose flow form exceeds maxLineWidth — so the file has one rule and
		// the emitter follows it rather than picking a style. The rule is on the whole
		// rendered line, so an indented caller's list trips it two columns sooner.
		flow := pad + "required: [" + strings.Join(req, ", ") + "]"
		if len(flow) <= maxLineWidth {
			out = append(out, flow)
		} else {
			out = append(out, pad+"required:")
			for _, r := range req {
				out = append(out, pad+"  - "+r)
			}
		}
	}

	out = append(out, pad+"properties:")
	for _, fr := range fields {
		ps, err := c.propSchemaFor(fr.Field.TypeStr, fr.Pkg, binds)
		if err != nil {
			return nil, fmt.Errorf("property %s (Go %s): %w", fr.Field.PropertyName(), fr.Field.TypeStr, err)
		}
		// The field's own prose and `@example` belong to the property, not to the
		// type it references, so they are attached here rather than inside
		// propSchemaFor.
		fp, ex, en := splitMarkers(fr.Field.DocLines)
		ps.Desc, ps.Example = fp, ex
		if len(ps.Enum) == 0 {
			ps.Enum = en
		}
		// The field's authored prose wins over the Go field doc (propSchema.RawDesc);
		// Desc/Example above are what an entry without authored prose falls back to.
		name := fr.Field.PropertyName()
		authored := a[name]
		ps.RawDesc, ps.RawExample = authored.Desc, authored.Example
		ps.RawEnum, ps.RawOneOf = authored.Enum, authored.OneOf
		ps.RawFormat, ps.RawNullable = authored.Format, authored.Nullable
		// An entry that documented a property and left one of these three slots empty
		// left it empty on purpose, and clearing the derived value is what makes that
		// the render: PVCView's `phase` has an enum and no description, PoolerView's
		// `image` an example and none, LatencyView's three percentiles a type and no
		// `nullable:` despite being `*float64`. The measurement is on
		// authoredProse.authoredAny; Example and Enum need no clearing because the
		// derived values there are the entry's own, copied through RawExample/RawEnum.
		if authored.authoredAny() {
			if len(authored.Desc) == 0 {
				ps.Desc = nil
			}
			if authored.Format == "" {
				ps.Format = ""
			}
			if authored.Nullable == "" {
				ps.Nullable = false
			}
		}
		out = append(out, pad+"  "+fr.Field.PropertyName()+":")
		inner := ps.lines(indent + 4)
		if len(inner) == 0 {
			// A schema with nothing to say (a type-less, description-less field) still
			// needs the key line to exist on its own, which the line above provides.
			continue
		}
		out = append(out, inner...)
	}
	// The schema's own example, last: the document's flat entries put it after
	// `properties:`, which is why propSchema keeps it apart from the description. The
	// lines are the entry's own, so their indentation is the author's and the caller
	// passes them only when that indentation is the one it renders at — which is why
	// composedBody refuses an example rather than re-indenting one.
	if len(example) > 0 {
		out = append(out, example...)
	}
	return out, nil
}

// composedBody renders everything under the `    Name:` line for an entry whose shape is
// the entry-level `allOf:`: the description, then one `$ref` member per embedded type
// that has its own schema entry, then one `type: object` member holding the fields the
// composition leaves over. It is objectCore's second caller, and the reason
// entryHasAllOf's refusal used to be the honest answer — inlining a documented type's
// fields into its parent changes the parent's shape as well as its prose, and the
// document composes instead because R16.8 says the field list belongs to the entry that
// owns it.
func (c schemaContext) composedBody(e openAPISchema, s goStruct, pkg string, binds map[string]string, a map[string]authoredProse) ([]string, error) {
	refs, own, ok := c.composition(s, pkg)
	if !ok {
		return nil, fmt.Errorf("entry is authored with allOf but Go %s embeds no type a schema entry names, so there is nothing to compose", e.Go)
	}
	if len(a[""].Example) > 0 {
		// The document's one composed entry documents no example, and its own prose says
		// why: the row half is the fixture's and is already shown on `QueryView`. An
		// authored example block carries the entry's own indent, so placing it inside a
		// member at indent 10 needs a re-indent no measured case authorises, and
		// inventing one is this slot's characteristic failure. Refusing is the honest
		// answer until a composed entry with an example exists to copy.
		return nil, fmt.Errorf("composed entry has an authored example and no measured placement for it inside a member")
	}
	out := append([]string{}, schemaDesc(s, a, 6)...)
	out = append(out, "      allOf:")
	for _, r := range refs {
		out = append(out, "        - $ref: '#/components/schemas/"+r+"'")
	}
	if len(own) == 0 {
		return out, nil
	}
	member, err := c.objectCore(own, pkg, binds, a, nil, nil, 10)
	if err != nil {
		return nil, err
	}
	// The member is a sequence item, so its first line is `- type: object` at indent 8
	// rather than `type: object` at indent 10 — the text stays in the same column and the
	// two leading spaces become the dash. Every line after it is already indented from
	// objectCore's own pad.
	member[0] = "        - " + strings.TrimLeft(member[0], " ")
	return append(out, member...), nil
}

// composition splits a struct the way a composed entry composes it: the embedded types
// that have their own schema entries become `$ref` members, and everything else — the
// named fields, plus the fields of any embedded type with no entry to point at — is the
// `type: object` member's field list. The second half is the `LoadColumns` case the
// document's own prose names: not a response type on any route, so nothing exists to
// `$ref` and inlining was the only way to document it.
//
// ok is false when the struct has no embedded type with an entry, which is the
// contradiction linesFor routes here from entryHasAllOf: an entry authored with allOf
// whose Go type has nothing to compose.
func (c schemaContext) composition(s goStruct, pkg string) (refs []string, own []goFieldRef, ok bool) {
	seen := map[string]bool{}
	add := func(fr goFieldRef) {
		n := fr.Field.PropertyName()
		if n == "" || n == "-" || seen[n] {
			return // the same dedupe flattenFields applies, so a shadowed name appears once
		}
		seen[n] = true
		own = append(own, fr)
	}
	for _, f := range s.Fields {
		if f.Name != "" {
			add(goFieldRef{Field: f, Pkg: pkg})
			continue
		}
		// Embedded. The star is stripped and the generic split before the lookup for the
		// same reason flattenFields does it: `*CycleB` embeds a pointer, but the embedded
		// type is still named CycleB.
		embed := strings.TrimPrefix(strings.TrimSpace(f.TypeStr), "*")
		if base, _, ok := splitGeneric(embed); ok {
			embed = base
		}
		ref, ok := canonicalRef(embed, pkg, c.pkgs, c.aliases)
		if !ok {
			continue // unresolvable: contributes nothing, as flattenFields also contributes nothing
		}
		if name, ok := c.names[ref]; ok {
			refs = append(refs, name)
			continue
		}
		if es, ok := resolveGoRef(ref, c.pkgs, c.aliases); ok {
			for _, fr := range flattenFields(es, pkgOf(ref), c.pkgs) {
				add(fr)
			}
		}
	}
	return refs, own, len(refs) > 0
}

// requiredNames is every field that is not `omitempty`, in declaration order.
// omitempty governs `required` and nothing else — which is the half of the rule the
// check verifies from the other side (a property exists whether or not it is
// required), and the half only the emitter can render.
func requiredNames(fields []goFieldRef) []string {
	var out []string
	for _, fr := range fields {
		if fr.Field.OmitEmpty {
			continue
		}
		out = append(out, fr.Field.PropertyName())
	}
	return out
}

// propSchema is a rendered OpenAPI schema for one Go type, decomposed so the
// renderer can emit keys in the order the document uses them: type, format,
// nullable, description, enum, items/additionalProperties, example. The order is
// measured, not chosen — all twelve properties with both a description and an enum
// write the description first, and all eight property schemas with a `format` and the
// three with a `nullable` put those between `type` and the description. An earlier
// version of this comment had enum above description, which is why every
// enum-bearing property the emitter rendered came out reordered: the order lives in
// the renderer, so a wrong note here is a wrong render everywhere.
type propSchema struct {
	// Type is "" for a schema with no type at all — the document's `plan:` property,
	// whose Go type is json.RawMessage and which is deliberately untyped because the
	// bytes belong to Postgres.
	Type     string
	Format   string
	Nullable bool
	Ref      string
	Enum     []string
	Desc     []string
	Example  string
	// RawDesc, RawExample and RawEnum replace the description, example and enum slots
	// with lines copied verbatim from the authored entry. The emitter derives a
	// *schema* from a Go type; the prose in openapi_annotations.go is R8.1's separate
	// input, and it is not the Go doc comment — measured over the table, 0 of 31
	// entries' descriptions equal their type's doc comment (Meta's Go doc says "the
	// pagination/envelope metadata every list endpoint returns", the document says
	// "Pagination and partial-render metadata … extended by R10.22"). Rendering prose
	// from the struct would delete curated input-4 text on every flip and bury a
	// structural change under a rewrapped paragraph, so a generated entry keeps the
	// prose it was given and derives only what the struct actually decides. They are
	// separate fields because the document's key order puts the description above
	// `required:` and the example below `properties:` — one combined block could not
	// sit in both places. RawEnum wins over Enum the way RawDesc wins over Desc: the
	// entry's list carries the per-value comments the derived one cannot (see
	// authoredProse.Enum), and Enum — from the type's `@enum` marker — is what an entry
	// with no authored list falls back to. RawFormat and RawNullable are the same pair
	// of slots for the two keys added after the enum was: an entry's `format:` is
	// copied rather than looked up in the type table (PromQLQueryRequest.startTime is a
	// Go string the document documents as a timestamp) and its `nullable:` is copied
	// rather than derived from pointer-ness (see authoredProse.Nullable), and RawOneOf
	// is the same copy for the one property that has a `oneOf:` (see
	// authoredProse.OneOf).
	RawDesc     []string
	RawExample  []string
	RawEnum     []string
	RawOneOf    []string
	RawFormat   string
	RawNullable string
	Items       *propSchema
	MapValue    *propSchema
}

// lines renders the schema at the given indent.
func (p propSchema) lines(indent int) []string {
	pad := strings.Repeat(" ", indent)
	if p.Ref != "" {
		// Two forms, and the spec picks which one a property gets. OpenAPI 3.0 ignores
		// every key written beside a `$ref`, so a ref-typed property with no prose
		// renders bare (`certificates:`, `error:`) — emitting a description beside that
		// ref would be a promise the validators discard, and 3.1 is the version that
		// fixes it. A ref-typed property the entry *did* document cannot keep the prose
		// beside the ref, so the document wraps the ref in a single-member `allOf:` and
		// puts the description beside the wrapper (PromQLSeries's `value:`): the wrapper
		// is a schema that may have siblings, and the ref inside it keeps none. Dropping
		// the prose instead loses R8.1's input-4 text, so the wrapper is the only form
		// that neither overpromises the schema nor loses the prose.
		ref := pad + "  - $ref: '#/components/schemas/" + p.Ref + "'"
		// Only the *authored* prose triggers the wrapper. Prose derived from the Go
		// field doc is not enough: an entry that documented a ref-typed property with a
		// bare `$ref:` and no description said so on purpose (the same rule the flat
		// branch applies to an entry that documents no description — see
		// authoredProse.authoredAny), and wrapping it would add an `allOf:` the entry
		// refused. Measured: seven entries — ClusterView, InstanceView, PromQLQueryResponse,
		// QueriesResponse, ActiveQueriesResponse, ScorecardView, BlockingChainView — have
		// a ref-typed property whose Go doc has prose while their entry keeps the bare
		// `$ref:`; a version of this branch that fell back to the derived description
		// re-rendered all seven with a wrapper and failed the byte-compare on each.
		desc := p.RawDesc
		if len(desc) == 0 {
			return []string{pad + "$ref: '#/components/schemas/" + p.Ref + "'"}
		}
		out := []string{pad + "allOf:", ref}
		out = append(out, desc...)
		switch {
		case len(p.RawExample) > 0:
			// The same authored-wins rule as the flat branch below: an entry's example
			// is copied verbatim, the type's `@example` marker is the fallback.
			out = append(out, p.RawExample...)
		case p.Example != "":
			out = append(out, pad+"example: "+inlineScalar(p.Example))
		}
		return out
	}
	var out []string
	if p.Type != "" {
		out = append(out, pad+"type: "+p.Type)
	}
	// format and nullable follow the same authored-wins rule as the description below:
	// the entry's own line is copied, and the value derived from the Go type is the
	// fallback for a property the entry left undocumented.
	format, nullable := p.Format, p.Nullable
	if p.RawFormat != "" {
		format = p.RawFormat
	}
	if p.RawNullable != "" {
		nullable = p.RawNullable == "true"
	}
	if format != "" {
		out = append(out, pad+"format: "+format)
	}
	if nullable {
		out = append(out, pad+"nullable: true")
	}
	if len(p.RawDesc) > 0 {
		// Authored prose, verbatim, in the description slot: see propSchema.RawDesc.
		out = append(out, p.RawDesc...)
	} else {
		out = append(out, descLines(p.Desc, indent)...)
	}
	if len(p.RawEnum) > 0 {
		// Authored block, verbatim: see propSchema.RawEnum. It occupies the same slot
		// as the derived render below and takes it wholesale, because the entry's list
		// is the one with the per-value comments and the flow-or-block choice already
		// made.
		out = append(out, p.RawEnum...)
	} else if len(p.Enum) > 0 {
		// The derived enum, from a type's `@enum` marker: the same flow-or-line-length
		// rule as `required:` (see objectBody), because the document writes
		// `enum: [True, False, Unknown]` and switches to one value per line only when
		// the flow form passes maxLineWidth. An entry that authored its own list gets it
		// verbatim from RawEnum above instead — a flow list cannot carry the per-value
		// comments the authored ones have.
		flow := pad + "enum: [" + strings.Join(p.Enum, ", ") + "]"
		if len(flow) <= maxLineWidth {
			out = append(out, flow)
		} else {
			out = append(out, pad+"enum:")
			for _, v := range p.Enum {
				out = append(out, pad+"  - "+inlineScalar(v))
			}
		}
	}
	if len(p.RawOneOf) > 0 {
		// Authored block, verbatim: see propSchema.RawOneOf. Unlike the three slots
		// above it, this one has no derived twin — a Go type never yields a oneOf, so
		// the block is emitted or nothing is. The document's only property-level oneOf
		// sits directly under its description, which is the only evidence there is for
		// where the slot goes.
		out = append(out, p.RawOneOf...)
	}
	if p.Items != nil {
		out = append(out, pad+"items:")
		out = append(out, p.Items.lines(indent+2)...)
	}
	if p.MapValue != nil {
		out = append(out, pad+"additionalProperties:")
		out = append(out, p.MapValue.lines(indent+2)...)
	}
	switch {
	case len(p.RawExample) > 0:
		// The authored example, verbatim: an entry's example is often a multi-line
		// block (a whole sample object), which inlineScalar cannot reproduce.
		out = append(out, p.RawExample...)
	case p.Example != "":
		out = append(out, pad+"example: "+inlineScalar(p.Example))
	}
	return out
}

// propSchemaFor maps a Go type string onto the OpenAPI schema for it. The mapping is
// the one the committed document already follows (measured across the 32 authored
// entries), not the one the OpenAPI spec allows — the spec permits `format: int64`
// and the document never once writes it, so emitting the format would reflow two
// hundred lines on the first flip for no gain.
func (c schemaContext) propSchemaFor(typeStr, pkg string, binds map[string]string) (propSchema, error) {
	return c.propSchemaForDepth(typeStr, pkg, binds, 0)
}

// propSchemaForDepth carries the recursion depth so a type expression that resolves
// forever is an error rather than a hang: `type A = []A` alternates a prefix strip and
// a binding lookup with no base case, and the cap matches flattenFields' so the two
// walkers agree on what "too deep" means.
func (c schemaContext) propSchemaForDepth(typeStr, pkg string, binds map[string]string, depth int) (propSchema, error) {
	if depth > 8 {
		return propSchema{}, fmt.Errorf("%q: type resolves through more than 8 aliases, parameters or prefixes", typeStr)
	}
	t := strings.TrimSpace(typeStr)
	// A generic parameter resolves to its argument before anything else is tried: in
	// `Envelope[T]` instantiated as `Envelope[dto.ClusterView]`, a field declared `T`
	// is a ClusterView. Answering "`T` does not resolve to a type" would be literally
	// true and useless.
	if b, ok := binds[t]; ok && b != t {
		return c.propSchemaForDepth(b, pkg, binds, depth+1)
	}
	switch {
	case t == "":
		return propSchema{}, fmt.Errorf("empty field type")
	case t == "string":
		return propSchema{Type: "string"}, nil
	case t == "bool":
		return propSchema{Type: "boolean"}, nil
	case t == "int", t == "int8", t == "int16", t == "int32", t == "int64",
		t == "uint", t == "uint8", t == "uint16", t == "uint32", t == "uint64",
		t == "byte", t == "rune":
		return propSchema{Type: "integer"}, nil
	case t == "float32", t == "float64":
		return propSchema{Type: "number"}, nil
	case t == "time.Time":
		return propSchema{Type: "string", Format: "date-time"}, nil
	case t == "json.RawMessage":
		// No type key at all: see propSchema.Type.
		return propSchema{}, nil
	case t == "[]byte":
		return propSchema{Type: "string", Format: "binary"}, nil
	case strings.HasPrefix(t, "*"):
		// A pointer is the same schema plus nullable. For a *Struct the nullable is
		// then dropped by lines() — a $ref has no siblings in 3.0, and the allOf
		// wrapper carries the description rather than the nullable — which is the
		// documented cost of the reference form, not a bug in this branch.
		inner, err := c.propSchemaForDepth(t[1:], pkg, binds, depth+1)
		if err != nil {
			return propSchema{}, err
		}
		inner.Nullable = true
		return inner, nil
	case strings.HasPrefix(t, "[]"):
		inner, err := c.propSchemaForDepth(t[2:], pkg, binds, depth+1)
		if err != nil {
			return propSchema{}, err
		}
		return propSchema{Type: "array", Items: &inner}, nil
	case strings.HasPrefix(t, "map[string]"):
		inner, err := c.propSchemaForDepth(t[len("map[string]"):], pkg, binds, depth+1)
		if err != nil {
			return propSchema{}, err
		}
		return propSchema{Type: "object", MapValue: &inner}, nil
	}
	name, err := c.refName(t, pkg)
	if err != nil {
		return propSchema{}, err
	}
	return propSchema{Ref: name}, nil
}

// refName is the schema name a Go type is documented under, or an error naming the
// type. It is the emitter's version of "every schema a document references must
// exist": a field whose type has no entry produces an error, not a dangling $ref, so
// a dto type added without documenting it fails the build through the entry that
// references it.
func (c schemaContext) refName(typeStr, pkg string) (string, error) {
	ref, ok := canonicalRef(typeStr, pkg, c.pkgs, c.aliases)
	if !ok {
		return "", fmt.Errorf("%q does not resolve to a type in package %s (R8.1b)", typeStr, pkg)
	}
	if n, ok := c.names[ref]; ok {
		return n, nil
	}
	// An entry may name the base of a generic where the field names an
	// instantiation, or the other way round; try the base before giving up.
	if base, _, ok := splitGeneric(ref); ok {
		if n, ok := c.names[base]; ok {
			return n, nil
		}
	}
	return "", fmt.Errorf("no schema entry names Go type %q", ref)
}

// canonicalRef turns a type expression into the qualified reference string an entry
// would use for it: `Plain` inside package dto becomes `dto.Plain`, `api.Alias`
// becomes whatever the alias points at, and a generic instantiation keeps its
// argument — `dto.Envelope[dto.Plain]` stays bracketed, because the entry table
// names instantiations and the base names none of them (R8.4's one-envelope-per-
// resource rule means several Envelope[...] entries exist).
func canonicalRef(typeStr, pkg string, pkgs map[string]map[string]goStruct, aliases map[string]map[string]string) (string, bool) {
	if base, arg, ok := splitGeneric(typeStr); ok {
		b, ok1 := canonicalRef(base, pkg, pkgs, aliases)
		a, ok2 := canonicalRef(arg, pkg, pkgs, aliases)
		if !ok1 || !ok2 {
			return "", false
		}
		return b + "[" + a + "]", true
	}
	t := strings.TrimSpace(typeStr)
	seen := map[string]bool{}
	for {
		if seen[t] {
			return "", false // alias cycle: `type A = B; type B = A`
		}
		seen[t] = true
		p, n, ok := splitQualified(t)
		if !ok {
			p, n = pkg, t
		}
		// The bracket split has to happen here as well as at the top of the function:
		// an alias can name an instantiation (`api.QueryPlansEnvelope =
		// dto.Envelope[dto.PlanSampleView]`), and the bracketed form is not a key in
		// pkgs because the definition lives on the base. Without it, the api-side
		// envelope aliases report "resolves to no type" here while checkEntries —
		// which goes through resolveGoRef, and does split the bracket — is happy with
		// them. Two resolvers that disagree about the same string is the failure this
		// file keeps hitting, so the rule is: whatever the check can resolve, this can.
		if base, arg, ok := splitGeneric(n); ok {
			b, ok1 := canonicalRef(p+"."+base, pkg, pkgs, aliases)
			a, ok2 := canonicalRef(arg, pkg, pkgs, aliases)
			if !ok1 || !ok2 {
				return "", false
			}
			return b + "[" + a + "]", true
		}
		target, ok := aliases[p][n]
		if !ok {
			if _, ok := pkgs[p][n]; !ok {
				return "", false
			}
			return p + "." + n, true
		}
		t = strings.TrimSpace(target)
	}
}

// descLines renders a `description:` key from prose lines: an inline scalar when the
// prose is a single line that fits the width, a block scalar otherwise. The authored
// file uses both forms and picks between them by judgement; the emitter has one
// rule, which is the trade the migration makes and part of what a flip's diff shows.
func descLines(prose []string, indent int) []string {
	prose = trimBlankEdges(prose)
	if len(prose) == 0 {
		return nil
	}
	if len(prose) == 1 {
		if line := strings.Repeat(" ", indent) + "description: " + inlineScalar(prose[0]); len(line) <= proseWidth {
			return []string{line}
		}
	}
	out := []string{strings.Repeat(" ", indent) + "description: |"}
	return append(out, wrapProse(prose, indent+2)...)
}

// wrapProse reflows prose to proseWidth columns and returns the lines, already
// indented, with blank lines preserved between paragraphs. It is a greedy fill, not
// a justification algorithm: the authored blocks were typed by hand at ~80 columns
// and what the emitter needs is a stable, reproducible rendering, not the prettiest
// one.
func wrapProse(lines []string, indent int) []string {
	var out []string
	pad := strings.Repeat(" ", indent)
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		line := ""
		for _, w := range para {
			switch {
			case line == "":
				line = w
			// The budget is the whole line, indent included: proseWidth is what the
			// document's prose lines measure (mode 78 at indent 8, max 87), so the
			// pad has to be counted — filling 80 columns *after* an 8-space indent
			// yields 88-column lines the file never has.
			case indent+len(line)+1+len(w) <= proseWidth:
				line += " " + w
			default:
				out = append(out, pad+line)
				line = w
			}
		}
		if line != "" {
			out = append(out, pad+line)
		}
		para = nil
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			flush()
			out = append(out, "")
			continue
		}
		para = append(para, strings.Fields(l)...)
	}
	flush()
	// A trailing blank (from the empty `//` line every Go comment ends with before
	// the next declaration) is dropped: the block scalar's last content line is its
	// last prose line, and a trailing blank changes the scalar's clip indicator.
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// trimBlankEdges drops leading and trailing blank lines from prose.
func trimBlankEdges(lines []string) []string {
	i, j := 0, len(lines)
	for i < j && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	for j > i && strings.TrimSpace(lines[j-1]) == "" {
		j--
	}
	return lines[i:j]
}

// inlineScalar renders s as a one-line YAML scalar, quoting only when an unquoted
// rendering would parse as something other than the string it holds. The authored
// file quotes by judgement — `description: R1.5's "PVC list".` sits next to
// `description: "`status.conditions` (R1.5)."` — and the emitter's rule replaces
// that judgement with a rule, which is the point of generating the entry rather than
// typing it.
func inlineScalar(s string) string {
	if s == "" {
		return `""`
	}
	if !needsQuote(s) {
		return s
	}
	q := strings.ReplaceAll(s, `\`, `\\`)
	q = strings.ReplaceAll(q, `"`, `\"`)
	return `"` + q + `"`
}

// needsQuote is the quoting half of inlineScalar, split out so the rule is one
// readable list rather than an inline boolean expression.
func needsQuote(s string) bool {
	if s != strings.TrimSpace(s) {
		return true // leading/trailing space is significant only when quoted
	}
	if strings.ContainsAny(s, "\n\t") {
		return true
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") {
		return true // would parse as a mapping
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~", "":
		return true // would parse as a bool/null scalar
	}
	if strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", rune(s[0])) {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true // would come back as a number
	}
	return false
}

// splitMarkers separates a doc comment's prose from the emitter's own markers.
// `@example <value>` and `@enum a, b, c` are the only two, and they are markers in a
// comment rather than fields on the struct because OpenAPI's example and enum have
// no Go counterpart: nothing in the type says that `protocol`'s example is `TCP`. So
// an entry with an authored example loses it on the flip unless the marker is added
// to the Go comment first — and the second gate's diff shows exactly that loss,
// which is what makes it reviewable instead of silent.
func splitMarkers(lines []string) (prose []string, example string, enum []string) {
	for _, l := range lines {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
			prose = append(prose, "")
		case strings.HasPrefix(l, "@example"):
			example = strings.TrimSpace(strings.TrimPrefix(l, "@example"))
		case strings.HasPrefix(l, "@enum"):
			for _, v := range strings.Split(strings.TrimPrefix(l, "@enum"), ",") {
				if v = strings.TrimSpace(v); v != "" {
					enum = append(enum, v)
				}
			}
		default:
			prose = append(prose, l)
		}
	}
	return prose, example, enum
}

// schemaBlocks indexes a document's `  schemas:` section by schema name, mapping
// each name to its entry lines: the blank line and section-banner comments that
// precede it, the `    Name:` key, and every more-indented line under it.
//
// It is the second gate's scanner. The byte-compare in TestGeneratedOpenAPI can say
// the document changed but not *where*, and a per-type migration needs the where: a
// change outside the entries marked Generated is a hand edit to generated output,
// which is the one thing the R8.1b split forbids.
//
// The leading blank line and banner belong to the entry that follows them, because
// that is how the annotations table stores them: openAPISchema.Lines starts with a
// blank line and the banner comment, and GenerateOpenAPI writes the entries' Lines in
// order, so a block that starts at the name key is not the same string as the entry
// that produced it. The first version cut there and every generated entry looked
// changed by a banner it had just copied — which is the same class of bug as
// comparing a document to a table with different rules on each side. With the header
// included, schemaBlocks(committed)[e.Name] and linesFor(e) are directly comparable
// for every entry, marked or not.
func schemaBlocks(doc string) (map[string][]string, error) {
	lines := strings.Split(strings.TrimSuffix(doc, "\n"), "\n")
	start := -1
	for i, l := range lines {
		if l == "  schemas:" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("openapi_emit: no `  schemas:` key in the document")
	}
	blocks := map[string][]string{}
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if l == "" || strings.HasPrefix(strings.TrimSpace(l), "#") || indentOf(l) > 4 {
			continue // a blank, a comment, or a body line: owned by a block, not a key
		}
		if indentOf(l) < 4 {
			break // the next top-level section key
		}
		if !strings.HasSuffix(l, ":") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSpace(l), ":")
		if !isSchemaName(name) {
			continue
		}
		// Backward over the entry's own prefix: the blank separator and any banner
		// comments directly above the key. The annotations table stores these inside
		// Lines, so a block cut at the key is not the string the entry produced.
		from := i
		for from > start+1 {
			p := lines[from-1]
			if p == "" || strings.HasPrefix(strings.TrimSpace(p), "#") {
				from--
				continue
			}
			break
		}
		// Forward over the body: every deeper-indented line, plus a blank line only
		// when the body continues after it (a `description: |` paragraph break). The
		// blank that separates two entries is the *next* entry's prefix, claimed by the
		// walk above; a body that swallows it is the second way the two sides can
		// disagree about where an entry ends.
		to := i + 1
		for to < len(lines) {
			n := lines[to]
			if n != "" && indentOf(n) > 4 {
				to++
				continue
			}
			if n == "" && deeperThan4After(lines, to+1) {
				to++
				continue
			}
			break
		}
		blocks[name] = lines[from:to]
		i = to - 1
	}
	return blocks, nil
}

// deeperThan4After reports whether the next non-empty line at or after k is indented
// past the schema-key level — which is what makes a blank line a paragraph break
// inside a body rather than the gap between two entries.
func deeperThan4After(lines []string, k int) bool {
	for ; k < len(lines); k++ {
		if lines[k] != "" {
			return indentOf(lines[k]) > 4
		}
	}
	return false
}

// isSchemaName guards schemaBlocks against indent-4 keys that are not schema names
// (a commented banner ends in a dash, not a colon, but a key with a space in it
// would otherwise become a schema named "Not Ready").
func isSchemaName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

// changedSchemas names every schema whose body differs between two documents. A
// name present in one and absent from the other counts as changed, which is how an
// entry added to the table without running `make openapi` gets named rather than
// disappearing from the comparison.
//
// Together with openAPISchema.Generated this is the emitter's second gate. With no
// entry marked Generated the answer must be empty — the byte-compare's own claim —
// and once an entry is marked, the answer is the list of types the commit flipped,
// which the test then checks against the set marked in the table. "The golden file
// changed, is that right?" becomes a question the test answers.
func changedSchemas(committed, generated string) ([]string, error) {
	a, err := schemaBlocks(committed)
	if err != nil {
		return nil, err
	}
	b, err := schemaBlocks(generated)
	if err != nil {
		return nil, err
	}
	var out []string
	for name, body := range b {
		if old, ok := a[name]; !ok || !reflect.DeepEqual(old, body) {
			out = append(out, name)
		}
	}
	for name := range a {
		if _, ok := b[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}
