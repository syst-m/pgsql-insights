// Fixture for TestCheckSchemasFixture in openapi_schemas_test.go. Nothing here is
// compiled — the go tool ignores testdata/ — and parseGoPackage reads it with
// go/parser directly, the same capability the routes fixture establishes for
// routesInDir.
//
// The content of this file is the *tag shapes*, not the field names: a named tag, a
// named tag with omitempty, a `-` tag (not part of the JSON object at all), and no
// tag at all (encoding/json falls back to the Go name). Those four are the whole
// vocabulary of "what properties does this struct have", and each one is a distinct
// way an authored document can disagree with its struct while looking plausible —
// which is why the fixture is one struct holding all four rather than four structs.
package dto

import "time"

// Plain holds the four tag shapes in one struct.
type Plain struct {
	Alpha string `json:"alpha"`
	// Bravo is optional on the wire. omitempty changes `required`, not whether the
	// property exists, so a correct entry still lists it.
	Bravo *float64 `json:"bravo,omitempty"`
	// Charlie is excluded from JSON by `-`: the document must NOT have a `charlie`
	// property even though the Go struct has a field. An entry that lists one from
	// reading the struct top-to-bottom, and an entry that omits `bravo` because it
	// is optional, are the two errors this pair of fields exists to catch.
	Charlie time.Time `json:"-"`
	// Echo has no tag at all, so the property name is the Go field name — `Echo`,
	// capitalised. Conflating "no tag" with `json:"-"` drops it silently.
	Echo int64
}

// Wrapper reaches Plain's three properties through one line of Go. An authored entry
// listing four properties (alpha, bravo, Echo, zulu) matches the struct only if the
// generator expands the embed; one that lists `Plain` as a property does not.
type Wrapper struct {
	Plain
	Zulu string `json:"zulu"`
}

// Envelope is generic, so an annotation names an *instantiation* of it
// (`dto.Envelope[dto.Plain]`) while the definition lives on the base — the shape
// R8.4's one-named-envelope-per-resource rule forces, since OpenAPI 3.0 has no way
// to name a generic.
type Envelope[T any] struct {
	Data T      `json:"data"`
	Meta *Plain `json:"meta,omitempty"`
}

// CycleA and CycleB embed each other through pointers. A value embed (`type CycleA
// struct { CycleB }`) is an invalid recursive type and would not compile, which is
// the only reason the fixture uses pointers; jsonProps has to terminate on the pair
// anyway, because an unbounded walk turns `go test` into a hang instead of a failure.
type CycleA struct {
	*CycleB
	Alpha string `json:"alpha"`
}

type CycleB struct {
	*CycleA
	Bravo string `json:"bravo"`
}
