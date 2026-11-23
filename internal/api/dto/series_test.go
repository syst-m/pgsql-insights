// The fixture for the fifth shape in testdata/api/, and the last of R8.2's
// response types that had no canonical test.
//
// The gap this closes is named in two places before this file existed.
// web/src/api/types.ts:34 says the series block's "pin is the published example,
// inlined in client.test.ts, not a fixture: `4cef345` shipped the route without
// its R16.8 `testdata/api/query-series.json`, and when A2 add one the test should
// read the file instead of the literal". web/src/api/client.test.ts:238 repeats
// it: "Inlined because there is no fixture to import. R16.8 wants one JSON file
// per endpoint under `testdata/api/`, and `4cef345` shipped this route with
// `series.go`, its Go test, the openapi entry and the handler — and no
// `query-series.json`."
//
// Two things about that assignment are worth correcting rather than repeating.
//
// The first is the owner. Both comments say the file is A2's to add, which was
// true while the route's types lived in package `api` — types.ts:23 still
// describes the block as mirroring "internal/api/series.go, which is package `api`
// and not `dto`". The types moved: they are now dto.QuerySeriesPoint and
// dto.QuerySeries in internal/api/dto/series.go, with internal/api/series.go:96-109
// keeping api.QuerySeriesEnvelope/QuerySeries/QuerySeriesPoint as aliases. R16.8's
// rule is "created by the phase that first serves it", and dto/ is where every
// other fixture in that directory is authored and guarded — clusters_test.go for
// clusters.json, queries_test.go for the four query shapes. A fixture for a dto
// type written by a non-dto owner is a second copy of the contract's prose, which
// is the R16.8 class this whole directory exists to prevent. So it is authored
// here, and the alias means A2's document keeps naming the same type.
//
// The second is the content. The published example labels its entry
// namespace `postgres`, cluster `onepg`, pod `onepg-1` — and `postgres/onepg` is
// not one of the four clusters testdata/api/clusters.json commits to (requirements.md
// §8). Copying the example verbatim would put a fixture into the directory that
// fails the rule its siblings enforce — queries_test.go's
// TestQueryFixtureClustersExistInClustersFixture checks exactly this for query
// rows ("query row %s belongs to %s, which testdata/api/clusters.json does not
// list").
//
// So the fixture does not reproduce the published entry. It builds three entries
// from rows queries.json already has, all on open-webui-pg, a cluster the
// inventory does list. The document's own example object stays where it is and
// gets its own guard here: TestDocumentExampleIsCanonicalForTheCurrentStruct
// re-marshals it against the struct as declared today, which is the same
// canonical-encoding check the fixture gets, pointed at the document instead.
// That split — a fixture built from real rows, the document's example checked on
// its own — is deliberate. The alternative was a fixture that satisfied the
// document by contradicting the inventory, and the inventory is the file
// requirements.md §8 commits to.
//
// The three entries are not three copies of one shape. They are the three shapes
// QuerySeries can take, each pinning a different pair of fields:
//
//   - a single-instance answer (pod + instanceRole present) — R5.7's
//     (namespace, cluster, pod) triple, the case R10.7's Detail chart asks for;
//   - a cluster-aggregate answer (both absent) — the omitempty pair, which is
//     what makes one entry per fingerprint possible at all, and the field pair a
//     re-marshalling test alone cannot catch being added or removed;
//   - a fingerprint the store has never seen (points is `[]`) — the never-null
//     rule R8.3 states for every array the API emits.
package dto

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

const querySeriesPath = "../../../testdata/api/query-series.json"

func TestQuerySeriesFixtureIsCanonical(t *testing.T) {
	var env Envelope[QuerySeries]
	readFixture(t, querySeriesPath, &env)
	assertCanonical(t, querySeriesPath, env)
}

// TestQuerySeriesFixtureLabelsExistInClustersFixture applies the rule
// queries_test.go states for query rows to the fifth fixture: nothing in
// testdata/api/ may name a cluster the inventory does not have. The failure mode
// is the one that comment names — "the kind of inconsistency two hand-maintained
// files in one directory acquire within a week".
func TestQuerySeriesFixtureLabelsExistInClustersFixture(t *testing.T) {
	_, clusters := readGolden(t)

	var env Envelope[QuerySeries]
	readFixture(t, querySeriesPath, &env)

	known := map[string]bool{}
	for _, c := range clusters.Data {
		known[c.Namespace+"/"+c.Name] = true
	}
	for _, s := range env.Data {
		key := s.Namespace + "/" + s.Cluster
		if !known[key] {
			t.Errorf("series for %s belongs to %s, which testdata/api/clusters.json does not list",
				s.Fingerprint, key)
		}
	}
}

// TestQuerySeriesFixtureFingerprintsExistInQueriesFixture checks the pair, not
// just the fingerprint. The series route answers for a fingerprint the catalogue
// already has (R8.11's request is a list of fingerprints), so a fixture whose
// (fingerprint, queryid) pair disagrees with queries.json is two files describing
// the same query with two different ids — and R9.5b makes the id exactly the
// field most likely to be re-derived wrong, since R3.4 lets it change under an
// unchanged query.
func TestQuerySeriesFixtureFingerprintsExistInQueriesFixture(t *testing.T) {
	var queries Envelope[QueryView]
	readFixture(t, queriesPath, &queries)
	var series Envelope[QuerySeries]
	readFixture(t, querySeriesPath, &series)

	idByFP := map[string]string{}
	for _, row := range queries.Data {
		idByFP[row.Fingerprint] = row.QueryID
	}
	for _, s := range series.Data {
		want, ok := idByFP[s.Fingerprint]
		if !ok {
			t.Errorf("series entry %s is not a fingerprint testdata/api/queries.json has; the series route answers for catalogue rows, not for invented ones",
				s.Fingerprint)
			continue
		}
		if s.QueryID != want {
			t.Errorf("series entry %s carries queryid %s, queries.json says %s",
				s.Fingerprint, s.QueryID, want)
		}
	}
}

// TestQuerySeriesFixturePointsAreNeverNull pins the rule QuerySeries's own
// comment calls out — "Points is `[]`, never null, for a fingerprint the store
// has never seen: an absent array and an empty one mean the same thing to the UI".
// json.Marshal writes a nil slice as `null` and an empty one as `[]`, so the
// difference is real on the wire and invisible to a test that only re-marshals a
// fixture full of non-empty slices. This needs a fixture that contains the empty
// case, which is what the third entry is.
func TestQuerySeriesFixturePointsAreNeverNull(t *testing.T) {
	var env Envelope[QuerySeries]
	readFixture(t, querySeriesPath, &env)

	empty := 0
	for _, s := range env.Data {
		if s.Points == nil {
			t.Errorf("series entry %s has nil Points; R8.3's rule is to say \"none\" with []",
				s.Fingerprint)
		}
		if len(s.Points) == 0 {
			empty++
		}
	}
	if empty == 0 {
		t.Error("no fixture entry has an empty points array, so the [] -vs- null rule is unexercised")
	}
}

// TestQuerySeriesFixtureCoversBothLabelShapes pins the omitempty pair. A
// re-marshalling test catches a *changed* tag; it does not catch a fixture that
// only ever exercises one side of an optional pair, because deleting the other
// side's field from the struct leaves every byte of that fixture still correct.
// So the fixture carries both shapes and this test requires both to be present.
func TestQuerySeriesFixtureCoversBothLabelShapes(t *testing.T) {
	var env Envelope[QuerySeries]
	readFixture(t, querySeriesPath, &env)

	var withPod, withoutPod int
	for _, s := range env.Data {
		switch {
		case s.Pod != "" && s.InstanceRole != "":
			withPod++
			// api/openapi.yaml's QuerySeries.instanceRole is `enum: [primary,
			// replica]` — narrower than InstanceRole, which adds "unhealthy"
			// for R1.2's up-but-postgres-not-running pod. types.ts:655 names
			// that: a series belongs to an instance that was primary or replica
			// at sample time, and "unhealthy" is not a role a series can belong
			// to. A fixture value outside the enum is a payload the document
			// forbids and only the Go type accepts.
			if !slices.Contains([]string{"primary", "replica"}, s.InstanceRole) {
				t.Errorf("series entry %s has instanceRole %q, outside the document's [primary, replica]",
					s.Fingerprint, s.InstanceRole)
			}
		case s.Pod == "" && s.InstanceRole == "":
			withoutPod++
		default:
			// The pair is one decision in the Go type (both are the single-
			// instance case) so a fixture with exactly one set is a fixture
			// describing a state the type does not produce.
			t.Errorf("series entry %s sets pod=%q and instanceRole=%q; the two are present together or absent together",
				s.Fingerprint, s.Pod, s.InstanceRole)
		}
	}
	if withPod == 0 || withoutPod == 0 {
		t.Errorf("fixture has %d single-instance and %d aggregate entries; both shapes must be present to pin the omitempty pair",
			withPod, withoutPod)
	}
}

// TestQuerySeriesFixturePointTimesAreMinuteAligned checks R8.11's "per-minute
// series" in the one place it is checkable offline: the timestamps. The bucket is
// a minute and the range parameter selects the window, never the resolution
// (R10.15), so a point at 09:11:30 would mean the resolution moved — and the
// published pair is exactly 60 s apart, which is the spacing the fixture commits
// to. Ordering is asserted too, because the SPA draws these as a sparkline and a
// store that returns points out of order renders a sawtooth that looks like a
// real latency spike.
func TestQuerySeriesFixturePointTimesAreMinuteAligned(t *testing.T) {
	var env Envelope[QuerySeries]
	readFixture(t, querySeriesPath, &env)

	for _, s := range env.Data {
		for i, p := range s.Points {
			if p.Timestamp.Second() != 0 || p.Timestamp.Nanosecond() != 0 {
				t.Errorf("series %s point %d is at %s; R8.11's buckets start on a minute boundary",
					s.Fingerprint, i, p.Timestamp.Format(time.RFC3339Nano))
			}
			if i > 0 && !p.Timestamp.After(s.Points[i-1].Timestamp) {
				t.Errorf("series %s point %d (%s) is not after point %d (%s); points are in time order",
					s.Fingerprint, i, p.Timestamp.Format(time.RFC3339),
					i-1, s.Points[i-1].Timestamp.Format(time.RFC3339))
			}
		}
	}
}

// TestDocumentExampleIsCanonicalForTheCurrentStruct applies the canonical-
// encoding check to api/openapi.yaml's own example object for this route, which
// is the copy no other test in this package re-reads.
//
// The fixture and the document's example are deliberately not the same object.
// The document's entry is labelled namespace `postgres`, cluster `onepg`, pod
// `onepg-1` — a cluster testdata/api/clusters.json does not list, which is the
// divergence the header covers and
// TestQuerySeriesFixtureLabelsExistInClustersFixture refuses to reproduce. So
// this test does not ask "is the document's entry in the fixture"; it asks the
// narrower and more useful question, "is the document's example still the
// canonical encoding of the struct as it is today".
//
// That is the R16.8 check pointed at the document instead of at a fixture.
// json.Marshal emits struct fields in declaration order, so renaming a field in
// dto/series.go, adding one, reordering two, or adding an omitempty changes the
// re-marshalled bytes and fails here — and the document's example is the one
// place a reader of the API contract looks first. It shipped in `4cef345`,
// authored against the struct as it was then; the struct has since moved packages,
// and nothing re-derived the example.
func TestDocumentExampleIsCanonicalForTheCurrentStruct(t *testing.T) {
	// The example object, transcribed from api/openapi.yaml's `example:` block
	// for /api/v1/insights/queries/series (the route's 200 response), in the
	// compact form encoding/json produces.
	const docExample = `{"fingerprint":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08","queryid":"8123456789012345678","namespace":"postgres","cluster":"onepg","pod":"onepg-1","instanceRole":"primary","datname":"appdb","usename":"app","points":[{"timestamp":"2026-11-21T09:11:00Z","executionTimeSeconds":0.4182,"rows":1200,"sharedBlocksHit":48200,"sharedBlocksRead":12,"lockTimeSeconds":0.0031},{"timestamp":"2026-11-21T09:12:00Z","executionTimeSeconds":0.3907,"rows":1180,"sharedBlocksHit":47910,"sharedBlocksRead":8,"lockTimeSeconds":0.0028}]}`

	var s QuerySeries
	if err := json.Unmarshal([]byte(docExample), &s); err != nil {
		t.Fatalf("unmarshal api/openapi.yaml's example: %v", err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != docExample {
		t.Errorf("api/openapi.yaml's example for /api/v1/insights/queries/series is not the "+
			"canonical encoding of dto.QuerySeries as declared today.\n"+
			"example: %s\n\ncanonical: %s\n\n"+
			"If a field in dto/series.go changed, the document's example needs the same change "+
			"(that file is A2's; the -update path here only rewrites fixtures, not the document).",
			docExample, out)
	}

	// The spacing the example commits to: one minute apart, which is R8.11's
	// bucket and the only spacing the document shows.
	if d := s.Points[1].Timestamp.Sub(s.Points[0].Timestamp); d != time.Minute {
		t.Errorf("the document's example pair is %s apart, want 1m", d)
	}
}
