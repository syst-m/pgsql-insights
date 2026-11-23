// Fixture for TestCheckSchemasFixture — the api side of the pair. See
// testdata/schemas/dto/types.go for why a testdata .go file is not compiled.
package api

import (
	"encoding/json"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// Pair declares its own MarshalJSON, so its Go field names are not JSON property
// names and its document entry describes an array. This is the SamplePair class:
// without the CustomMarshal rule the check demands a `stamp`/`text` property pair on
// a schema that correctly has neither.
type Pair struct {
	Stamp float64
	Text  string
}

func (p Pair) MarshalJSON() ([]byte, error) { return json.Marshal(nil) }

// Tag has a *pointer*-receiver MarshalText, covering the `x *T` receiver form in
// recvTypeName plus the MarshalText half of the detection. A marshaller declared on
// *Tag makes Tag's fields non-properties just as a value-receiver one does, and a
// parser that only handled `x T` with the name MarshalJSON would miss it.
type Tag struct {
	Note string `json:"note"`
}

func (t *Tag) MarshalText() ([]byte, error) { return nil, nil }

// Alias re-exports a dto type under the api name — the third reference grammar the
// real annotations use (api.QuerySeries = dto.QuerySeries). Resolving it means
// following the alias, not looking for a struct named Alias: an implementation that
// checked structs first would find nothing here and report a false "no such type",
// which is the failure the real table would have hit on three of its 32 entries.
type Alias = dto.Plain
