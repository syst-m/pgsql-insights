// The fixture-side pin for openapi_schemas.go.
//
// TestGeneratedOpenAPI proves the real table has no drift *today*; on its own it
// cannot tell a check that works from a check that always says "fine" — which is
// precisely the failure openapi_schemas.go exists to fix (the `Go:` field it now
// resolves was read by nobody for three phases and every test stayed green). So the
// check needs negative cases, and they come from testdata/schemas the way
// TestRoutesInDirFixture's come from testdata/routes: a fixture directory plus an
// entry list passed in as an argument. That argument is why checkSchemasInDirs takes
// its entries instead of reading openAPISchemas itself — without it, a case that
// *should* fail could only exist by breaking the real table.
package api

import (
	"reflect"
	"strings"
	"testing"
)

// plainEntry renders an authored entry whose body is a description block plus a flat
// `properties:` block holding props. The description is not decoration: a
// `description: |` followed by prose at the next indent is the shape that made the
// first version of the parser report every entry as documenting nothing, so every
// fixture entry carries one to keep that case exercised.
func plainEntry(name, goRef string, props ...string) openAPISchema {
	lines := []string{
		"",
		"    " + name + ":",
		"      description: |",
		"        fixture entry",
		"      properties:",
	}
	for _, p := range props {
		lines = append(lines, "        "+p+":", "          type: string")
	}
	return openAPISchema{Name: name, Go: goRef, Lines: append(lines, "")}
}

// allOfEntry renders the *other* shape the document uses — an `allOf` whose members
// are a `$ref` and a `- type: object` carrying the local properties, which is
// QueryDetailView's shape and the one the first parser could not read (it handled
// `-` and `- properties:` but not `- type: object`, so a schema composed by allOf
// lost every property its members declared).
func allOfEntry(name, goRef string, refs []string, props []string) openAPISchema {
	lines := []string{
		"",
		"    " + name + ":",
		"      description: |",
		"        fixture entry",
		"      allOf:",
	}
	for _, r := range refs {
		lines = append(lines, "        - $ref: '#/components/schemas/"+r+"'")
	}
	lines = append(lines, "        - type: object", "          properties:")
	for _, p := range props {
		lines = append(lines, "            "+p+":", "              type: string")
	}
	return openAPISchema{Name: name, Go: goRef, Lines: append(lines, "")}
}

func TestCheckSchemasFixture(t *testing.T) {
	const (
		apiDir = "testdata/schemas/api"
		dtoDir = "testdata/schemas/dto"
	)
	cases := []struct {
		Name    string
		Entries []openAPISchema
		Want    []string
	}{
		{
			// The baseline: a correct entry produces no problem. Every failure case below
			// is this entry with one thing changed, so a case that passes for the wrong
			// reason still has to agree with this one.
			Name:    "1 correct entry",
			Entries: []openAPISchema{plainEntry("Plain", "dto.Plain", "alpha", "bravo", "Echo")},
			Want:    nil,
		},
		{
			// A field the struct has and the document does not — the common break, and the
			// one the type doc promised to catch.
			Name:    "2 property missing from doc",
			Entries: []openAPISchema{plainEntry("Plain", "dto.Plain", "alpha")},
			Want: []string{"schema Plain (Go dto.Plain): properties differ from the struct's json tags — " +
				"in Go not in doc: [Echo bravo]; in doc not in Go: [] (R16.8)"},
		},
		{
			// The mirror case: documented but not served. Equally a defect, and the one
			// `as T` in a hand-written frontend type never catches.
			Name:    "3 property in doc but not in Go",
			Entries: []openAPISchema{plainEntry("Plain", "dto.Plain", "alpha", "bravo", "Echo", "delta")},
			Want: []string{"schema Plain (Go dto.Plain): properties differ from the struct's json tags — " +
				"in Go not in doc: []; in doc not in Go: [delta] (R16.8)"},
		},
		{
			// The `json:"-"` rule from the other side: an entry written by listing every Go
			// field gains `charlie`, a property the server never serves. Cases 2, 3 and 4 are
			// the same entry with one property added or removed — the correct list is
			// [alpha bravo Echo], where bravo is listed despite omitempty (which governs
			// `required`, not existence) and charlie is absent despite being a real field.
			Name:    "4 json:\"-\" field listed in doc",
			Entries: []openAPISchema{plainEntry("Plain", "dto.Plain", "alpha", "bravo", "charlie", "Echo")},
			Want: []string{"schema Plain (Go dto.Plain): properties differ from the struct's json tags — " +
				"in Go not in doc: []; in doc not in Go: [charlie] (R16.8)"},
		},
		{
			Name: "5 empty Go field",
			Entries: []openAPISchema{{
				Name:  "Nothing",
				Go:    "",
				Lines: []string{"", "    Nothing:", "      type: object"},
			}},
			Want: []string{"schema Nothing: empty Go field — every entry must name the type it documents (R8.1b)"},
		},
		{
			Name: "6 Go is not pkg.Name",
			Entries: []openAPISchema{{
				Name:  "Plain",
				Go:    "Plain",
				Lines: []string{"", "    Plain:", "      type: object"},
			}},
			Want: []string{`schema Plain: Go "Plain" is not pkg.Name`},
		},
		{
			// The exact claim openAPISchema's doc makes: "a schema naming a type that does
			// not exist is an error". Before this file it was not.
			Name: "7 Go names a type that does not exist",
			Entries: []openAPISchema{{
				Name:  "Ghost",
				Go:    "dto.Ghost",
				Lines: []string{"", "    Ghost:", "      type: object"},
			}},
			Want: []string{"schema Ghost: Go \"dto.Ghost\" resolves to no type in dto " +
				"(R8.1b: an authored schema naming a type that does not exist is a generator error)"},
		},
		{
			// dto.TestOnly exists only in a _test.go file, which parseGoPackage skips, so it
			// is indistinguishable from a deleted type — the observable form of the skip rule.
			Name: "8 Go names a _test.go-only type",
			Entries: []openAPISchema{{
				Name:  "TestOnly",
				Go:    "dto.TestOnly",
				Lines: []string{"", "    TestOnly:", "      type: object"},
			}},
			Want: []string{"schema TestOnly: Go \"dto.TestOnly\" resolves to no type in dto " +
				"(R8.1b: an authored schema naming a type that does not exist is a generator error)"},
		},
		{
			// `type Alias = dto.Plain` in package api: the reference has to be followed
			// through the alias. A resolver that looked only at struct definitions would
			// report three of the real table's 32 entries as naming types that do not exist.
			Name:    "9 alias resolves to the dto struct",
			Entries: []openAPISchema{plainEntry("Alias", "api.Alias", "alpha", "bravo", "Echo")},
			Want:    nil,
		},
		{
			// `dto.Envelope[dto.Plain]` is defined as `Envelope[T any]`; the bracketed form is
			// not a name in the file, so it has to be split before the lookup.
			Name:    "10 generic instantiation resolves to the base",
			Entries: []openAPISchema{plainEntry("EnvelopePlain", "dto.Envelope[dto.Plain]", "data", "meta")},
			Want:    nil,
		},
		{
			// One line of Go (`Plain`) contributes three properties. An entry that lists
			// `Plain` as a property, or lists only zulu, fails here.
			Name:    "11 embedded field expands",
			Entries: []openAPISchema{plainEntry("Wrapper", "dto.Wrapper", "alpha", "bravo", "Echo", "zulu")},
			Want:    nil,
		},
		{
			// The document-side twin of case 11: the entry composes Plain by $ref and adds
			// zulu, so its property set is the union of both. Comparing five to four is the
			// false mismatch this avoids, and `- type: object` is the allOf member shape the
			// first parser could not read.
			//
			// Both entries are in the list because a $ref can only be expanded against the
			// entry set the check is given — pass Wrapper alone and the reference resolves to
			// nothing, which is the correct reading of an incomplete document, not a way to
			// test composition. The real table passes all 32 for the same reason.
			Name: "12 allOf over a $ref matches an embedding struct",
			Entries: []openAPISchema{
				plainEntry("Plain", "dto.Plain", "alpha", "bravo", "Echo"),
				allOfEntry("Wrapper", "dto.Wrapper", []string{"Plain"}, []string{"zulu"}),
			},
			Want: nil,
		},
		{
			// A type with its own MarshalJSON has a hand-written wire shape: its Go field
			// names are not JSON property names, so the diff does not apply. This is the
			// SamplePair class — the first run of the check flagged it as a false mismatch.
			Name: "13 value-receiver MarshalJSON: fields are not properties",
			Entries: []openAPISchema{{
				Name: "Pair",
				Go:   "api.Pair",
				Lines: []string{
					"",
					"    Pair:",
					"      type: array",
					"      minItems: 2",
					"      maxItems: 2",
					"      items:",
					"        oneOf:",
					"          - type: number",
					"          - type: string",
				},
			}},
			Want: nil,
		},
		{
			// MarshalText on a *pointer* receiver: the receiver's type is `*Tag`, which
			// recvTypeName has to unstar, and the method name is not MarshalJSON. Either
			// half missed and Tag's `note` property would be demanded of a `type: string`
			// schema that correctly has none.
			Name: "14 pointer-receiver MarshalText: fields are not properties",
			Entries: []openAPISchema{{
				Name:  "Tag",
				Go:    "api.Tag",
				Lines: []string{"", "    Tag:", "      type: string"},
			}},
			Want: nil,
		},
		{
			// A pointer embed (`*CycleB`) resolves, and the mutual cycle terminates: the
			// depth cap and the seen-set are what stop `go test` hanging on this pair.
			Name:    "15 pointer embed resolves and the cycle terminates",
			Entries: []openAPISchema{plainEntry("CycleA", "dto.CycleA", "alpha", "bravo")},
			Want:    nil,
		},
		{
			// Two broken entries in one call produce two problems, sorted. The sort is the
			// assertion: an unsorted list makes the output depend on map iteration order,
			// and a generator whose error output changes between runs is one nobody diffs.
			Name: "16 several problems are all reported, sorted",
			Entries: []openAPISchema{
				plainEntry("Plain", "dto.Plain", "alpha"),
				{Name: "Ghost", Go: "dto.Ghost", Lines: []string{"", "    Ghost:", "      type: object"}},
			},
			Want: []string{
				"schema Ghost: Go \"dto.Ghost\" resolves to no type in dto " +
					"(R8.1b: an authored schema naming a type that does not exist is a generator error)",
				"schema Plain (Go dto.Plain): properties differ from the struct's json tags — " +
					"in Go not in doc: [Echo bravo]; in doc not in Go: [] (R16.8)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := checkSchemasInDirs(apiDir, dtoDir, tc.Entries)
			if !reflect.DeepEqual(got, tc.Want) {
				t.Errorf("checkSchemasInDirs(...)\ngot:\n  %s\nwant:\n  %s",
					strings.Join(got, "\n  "), strings.Join(tc.Want, "\n  "))
			}
		})
	}
}

// TestCheckSchemasRealTable runs the fixture-tested check over the real table and
// the real dto package. It is the assertion that the 32 authored entries agree with
// the structs they name today — and the regression pin for tomorrow: a dto field
// renamed without a document edit fails here, in the same build that fails a route
// added without a document edit.
func TestCheckSchemasRealTable(t *testing.T) {
	if got := checkSchemas(); len(got) > 0 {
		t.Errorf("checkSchemas(): %d schema/struct mismatch(es):\n  %s",
			len(got), strings.Join(got, "\n  "))
	}
}
