package dto

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/query"
)

// The fixtures for the four shapes queries.go declares.
//
// R16.8's rule is "a checked-in JSON fixture per endpoint under `testdata/api/`,
// created by the phase that first serves it", and Phase 3 serves four of R8.2's
// endpoints from this file — so four fixtures, not one. A shape without a fixture
// has no canonical test, and a shape with no canonical test is a shape whose json
// tags drift out of `web/src/api/types.ts` silently, which is the failure R16.8
// exists to prevent.
//
// `queries.json` is the one the existing suite pre-announces: TestFixtureDirectory
// below (clusters_test.go) documents its glob as covering the files "Phase 5 adds
// (queries.json, instances.json, etc.)", so this file joins a set the test already
// expects rather than inventing a layout.
const (
	queriesPath     = "../../../testdata/api/queries.json"
	queryDetailPath = "../../../testdata/api/query-detail.json"
	queryPlansPath  = "../../../testdata/api/query-plans.json"
	tagsPath        = "../../../testdata/api/tags.json"
)

// The -update flag is declared once, in clusters_test.go, and shared here rather
// than re-declared: flag.Bool is package-level in a test binary, and a second
// `flag.Bool("update", …)` in the same package panics at TestMain time with
// "flag redefined" — which fails every test in the package, not just the new one.
// Reusing the existing var is what makes `go test ./internal/api/dto -update`
// regenerate clusters.json and the query fixtures in one run, which is what the
// charter's "the fixture regenerated in the same commit" rule assumes.

// assertCanonical re-marshals v and requires the bytes to equal the file at path,
// the same check TestGoldenFixtureIsCanonical runs on clusters.json. json.Marshal
// emits struct fields in declaration order and sorts map keys, so a changed tag,
// a renamed field, a reordered field, or a new omitempty all show up here even
// when every test that *uses* the struct still passes.
func assertCanonical(t *testing.T, path string, v any) {
	t.Helper()

	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if *updateGolden {
		if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("rewrote %s", path)
		raw, _ = os.ReadFile(path)
	}
	if string(out)+"\n" != string(raw) {
		t.Fatalf("%s is not the canonical encoding of its parsed form.\n"+
			"If you changed a field in queries.go, run `go test ./internal/api/dto -update` "+
			"and commit the fixture in the same commit (and tell the UI workstream).\n"+
			"want %d bytes, got %d bytes", path, len(raw), len(out))
	}
}

// readFixture reads path into a fresh T and returns the parsed value. The
// canonical tests need the parsed form to re-marshal it; the assertion tests need
// it to inspect the values.
func readFixture(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}

// TestQueriesFixtureIsCanonical is the commit queries.go's docblock
// pre-announced: "The fixture gains `pgStatStatements` in the commit that
// switches that test's type, and the -update run belongs to that commit" — the
// deferral a83612b recorded when it added QueriesResponse, open for the 74
// commits between that commit and this one (git rev-list --count a83612b..HEAD).
//
// Pinning to the type the route returns, rather than to the generic envelope it
// stopped returning, is the point. While this test named Envelope[QueryView], a
// field could exist on the body GET /insights/queries writes — and did, in
// internal/api/queries.go:211 — and be absent from the fixture with every test in
// the package green. The fixture has to be the canonical encoding of the response
// the handler builds, not of a type it no longer builds; the five sibling
// assertion tests in this file were switched to the same type in this commit for
// the same reason, so the file has one answer to "what is this fixture's type".
func TestQueriesFixtureIsCanonical(t *testing.T) {
	var env QueriesResponse
	readFixture(t, queriesPath, &env)
	assertCanonical(t, queriesPath, env)
}

func TestQueryDetailFixtureIsCanonical(t *testing.T) {
	var v QueryDetailView
	readFixture(t, queryDetailPath, &v)
	assertCanonical(t, queryDetailPath, v)
}

func TestQueryPlansFixtureIsCanonical(t *testing.T) {
	var env Envelope[PlanSampleView]
	readFixture(t, queryPlansPath, &env)
	assertCanonical(t, queryPlansPath, env)
}

func TestTagsFixtureIsCanonical(t *testing.T) {
	var env Envelope[TagView]
	readFixture(t, tagsPath, &env)
	assertCanonical(t, tagsPath, env)
}

// jsonKeys lists the property names encoding/json will emit for a struct type,
// flattening embedded structs the way the encoder does. It is the only honest way
// to ask "what does this struct put on the wire", because the answer is a property
// of the encoder, not of the field list.
func jsonKeys(rt reflect.Type) []string {
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" && opts == "" {
			continue
		}
		if name == "" && f.Anonymous {
			// An embedded struct without a tag is inlined into the parent object,
			// so its fields are the parent's properties, not a nested one. This is
			// the half a hand-written fixture gets wrong.
			if f.Type.Kind() == reflect.Struct {
				out = append(out, jsonKeys(f.Type)...)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// TestR32ColumnSetIsComplete is the Phase 3 acceptance check for R3.2's table:
// the requirement names its columns in one sentence, and this enumerates
// QueryView's wire properties and compares them to that sentence.
//
// It catches the two ways a contract drifts from a spec — a field added that the
// spec does not name, and a column the spec names that nobody implemented — and it
// is the test that makes the two deliberate absences (Load by lock wait, Analyze
// latency) assertible rather than a comment someone can delete.
func TestR32ColumnSetIsComplete(t *testing.T) {
	want := []string{
		// R3.2's per-query sourced columns, in the order requirements.md:306 lists
		// them: "Query, Database, Recommendations, Load by total time,
		// % load by Queries, Avg execution time (ms), Total execution time (ms),
		// Avg rows returned, Times called" plus the two derived columns
		// "Load by CPU and Load by IO wait".
		"query", "database", "recommendations",
		"loadByTotalTime", "pctLoadByQueries",
		"avgExecTimeMs", "totalExecTimeMs", "avgRowsReturned", "timesCalled",
		"loadByCPUMs", "loadByIOWaitMs",

		// Identity and scoping, which R8.2's route and R3.4's key require and
		// which R3.2's column list does not name: the fingerprint is the row's key
		// (R3.4), `queryid` is R9.5b's secondary label, `truncated` is R3.6's flag
		// and is only meaningful next to the text it describes, and namespace and
		// cluster are what make an unfiltered R8.4 list renderable at all.
		"fingerprint", "queryid", "truncated", "namespace", "cluster", "pgMajor",

		// R3.2 names two more columns that are deliberately NOT fields here, and
		// the reason each is absent is on LoadColumns:
		//   lockWait       — "no per-query source … renders `n/a (no per-query
		//                    source, §5)`", so the UI supplies the literal.
		//   analyzeLatency — "deferred … and marked **Gap**".
		// Asserting their absence is the point: a future "just add the field"
		// change has to delete a test assertion, not a paragraph.
	}
	got := jsonKeys(reflect.TypeOf(QueryView{}))
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("QueryView's wire properties do not match R3.2's column set.\n"+
			"got  %v\nwant %v", got, want)
	}
	for _, absent := range []string{"lockWait", "lockWaitMs", "analyzeLatency"} {
		if slices.Contains(got, absent) {
			t.Errorf("QueryView has %q; R3.2 withholds `Load by lock wait` per query "+
				"(the UI renders the n/a literal) and marks `Analyze latency` Gap", absent)
		}
	}
}

// TestLoadColumnsAreSharedByEveryRollup checks the claim R3.7 makes in one clause
// — "Each rollup answers 'which X accounts for the most load' with the same load
// columns as R3.2" — by checking the types rather than the prose. LoadColumns is
// embedded in both QueryView (R3.2's table) and TagView (R3.7's rollup shape), so
// "the same" is enforced by the compiler; this test is the assertion that neither
// side later swaps in a near-copy.
func TestLoadColumnsAreSharedByEveryRollup(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"QueryView", reflect.TypeOf(QueryView{})},
		{"TagView", reflect.TypeOf(TagView{})},
		{"QueryDetailView", reflect.TypeOf(QueryDetailView{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := tc.typ.FieldByName("LoadColumns")
			if !ok {
				t.Fatalf("%s does not embed LoadColumns", tc.name)
			}
			if !f.Anonymous {
				t.Errorf("%s.LoadColumns is a named field, not an embedded one; "+
					"R3.7's \"the same load columns as R3.2\" is only enforced by embedding", tc.name)
			}
			if f.Type != reflect.TypeOf(LoadColumns{}) {
				t.Errorf("%s.LoadColumns is %s, not dto.LoadColumns", tc.name, f.Type)
			}
		})
	}
}

// TestQueryFixtureFingerprintsAreTheNormalizers is the cross-fixture check: every
// fingerprint in the API fixture must be internal/query's fingerprint of the
// fixture's own `query` text.
//
// Without it, the two fixtures are two independent claims about the same string
// and can disagree without anything failing — the API fixture says a query's
// fingerprint is X, the normalizer corpus says the same template hashes to Y, and
// the UI renders an X that no collector would ever have produced. The check only
// applies to rows R3.6 did not truncate, because a truncated `query` is a prefix
// of the template and hashing the prefix is not hashing the template.
func TestQueryFixtureFingerprintsAreTheNormalizers(t *testing.T) {
	var env QueriesResponse
	readFixture(t, queriesPath, &env)
	if len(env.Data) == 0 {
		t.Fatalf("%s has no rows", queriesPath)
	}

	seen := map[string]string{}
	for _, row := range env.Data {
		t.Run(row.Namespace+"/"+row.Cluster+"/"+row.Query, func(t *testing.T) {
			if row.Truncated {
				// R3.6 cut the display text, so the fingerprint is over a template
				// this fixture does not carry. Assert the flag is at least honest
				// about the row being short.
				if len(row.Query) > query.DefaultQueryStringLength {
					t.Errorf("row is truncated but its display text is %d bytes, over the %d-byte default cap",
						len(row.Query), query.DefaultQueryStringLength)
				}
				return
			}
			if got := query.Fingerprint(row.Query); got != row.Fingerprint {
				t.Errorf("fingerprint %s is not internal/query's fingerprint of this text (%s).\n"+
					"The api fixture and the R3.4/R3.5 corpus disagree.", row.Fingerprint, got)
			}
		})
		if prev, dup := seen[row.Fingerprint]; dup {
			t.Errorf("fingerprint %s appears twice (also in %s): R3.4 makes it the row's key",
				row.Fingerprint, prev)
		}
		seen[row.Fingerprint] = row.Namespace + "/" + row.Cluster
	}
}

// TestQueryFixtureLoadArithmetic pins R3.2's derivation on every fixture row
// rather than trusting the numbers: `Load by CPU` is `total_exec_time` minus block
// I/O time, and `Avg execution time (ms)` is the mean over the R3.3 delta.
//
// A fixture is data, and data edited by hand goes stale: change
// totalExecTimeMs to make a row look busier and every derived column in that row
// becomes a lie. This test is what makes editing one number require editing three.
func TestQueryFixtureLoadArithmetic(t *testing.T) {
	var env QueriesResponse
	readFixture(t, queriesPath, &env)

	for _, row := range env.Data {
		t.Run(row.Namespace+"/"+row.Cluster+"/"+row.Query, func(t *testing.T) {
			// R3.2: Load by CPU = total_exec_time − Load by IO wait.
			if got := row.LoadByCPUMs + row.LoadByIOWaitMs; math.Abs(got-row.TotalExecTimeMs) > 0.01 {
				t.Errorf("loadByCPUMs (%v) + loadByIOWaitMs (%v) = %v, but totalExecTimeMs is %v; "+
					"R3.2 derives CPU load as total_exec_time minus block I/O time",
					row.LoadByCPUMs, row.LoadByIOWaitMs, got, row.TotalExecTimeMs)
			}
			// R3.3: the window's mean is the exec-time delta over the calls delta.
			if row.TimesCalled > 0 {
				want := row.TotalExecTimeMs / float64(row.TimesCalled)
				if math.Abs(want-row.AvgExecTimeMs) > 0.05 {
					t.Errorf("avgExecTimeMs = %v, but totalExecTimeMs/timesCalled = %.4f",
						row.AvgExecTimeMs, want)
				}
			}
			if row.PGMajor < 15 || row.PGMajor > 18 {
				t.Errorf("pgMajor %d is outside R2.4's supported matrix (15–18)", row.PGMajor)
			}
		})
	}
}

// TestQueryFixtureCoversBothPGMajors guards R10.28's "PG16 vs PG17 column
// differences" empty state. R3.2's block-I/O columns come from different
// pg_stat_statements columns on either side of PG17, so the fixture has to show
// the SPA both derivations in one response — which is the situation the UI has to
// render and the one a single-cluster fixture would never exercise.
func TestQueryFixtureCoversBothPGMajors(t *testing.T) {
	var env QueriesResponse
	readFixture(t, queriesPath, &env)

	majors := map[int]int{}
	for _, row := range env.Data {
		majors[row.PGMajor]++
	}
	if majors[16] == 0 {
		t.Error("no PG16 row: the fixture cannot exercise the ≤PG16 blk_read_time/blk_write_time derivation")
	}
	if majors[17] == 0 {
		t.Error("no PG17 row: the fixture cannot exercise the PG17+ shared_blk_read_time/shared_blk_write_time derivation")
	}
}

// TestQueryFixtureClustersExistInClustersFixture ties the two api fixtures
// together: every query row must belong to one of the four reference clusters
// requirements.md §8 commits to. A query from a cluster the inventory does not
// have is a fixture the SPA cannot cross-link, and the kind of inconsistency two
// hand-maintained files in one directory acquire within a week.
func TestQueryFixtureClustersExistInClustersFixture(t *testing.T) {
	// readGolden is clusters_test.go's reader for testdata/api/clusters.json; the
	// raw bytes are the same file assertCanonical already checked, so only the
	// parsed form is wanted here.
	_, clusters := readGolden(t)

	var env QueriesResponse
	readFixture(t, queriesPath, &env)

	known := map[string]bool{}
	for _, c := range clusters.Data {
		known[c.Namespace+"/"+c.Name] = true
	}
	for _, row := range env.Data {
		key := row.Namespace + "/" + row.Cluster
		if !known[key] {
			t.Errorf("query row %s belongs to %s, which testdata/api/clusters.json does not list",
				row.Query, key)
		}
	}
}

// TestQueryFixtureQueryIDIsAStringForAReason checks the claim QueryID's comment
// makes — that a bigint queryid exceeds JavaScript's 2^53 safe-integer range,
// which is why the field is a string and not a number. The claim is only worth
// documenting if the fixture proves it; a fixture of small ids would let someone
// "simplify" the field to int64 and break the SPA's rendering of real values.
func TestQueryFixtureQueryIDIsAStringForAReason(t *testing.T) {
	var env QueriesResponse
	readFixture(t, queriesPath, &env)

	const maxSafe = int64(1) << 53
	var big int
	for _, row := range env.Data {
		if row.QueryID == "" {
			continue
		}
		n, err := strconv.ParseInt(row.QueryID, 10, 64)
		if err != nil {
			t.Errorf("queryid %q is not a base-10 int64: %v", row.QueryID, err)
			continue
		}
		if n > maxSafe {
			big++
		}
	}
	if big == 0 {
		t.Error("no fixture row has a queryid above 2^53, so nothing here proves the field " +
			"has to be a string; a JSON number would have round-tripped and the type is unjustified")
	}
}

// r39BucketBounds is R3.9's named bucket set, quoted from requirements.md:313:
// "using our own named buckets (`1, 5, 25, 100, 500, 1000, 5000, 10000` ms)".
var r39BucketBounds = []string{"1", "5", "25", "100", "500", "1000", "5000", "10000"}

// TestR39BucketSetIsTheNamedOne checks the histogram against the eight numbers
// R3.9 names and the two series R9.13 accounts beside them.
func TestR39BucketSetIsTheNamedOne(t *testing.T) {
	var v QueryDetailView
	readFixture(t, queryDetailPath, &v)

	if len(v.Latency.Buckets) != len(r39BucketBounds)+1 {
		t.Fatalf("histogram has %d buckets, want R3.9's 8 named buckets plus the +Inf overflow (R9.13)",
			len(v.Latency.Buckets))
	}
	var got []string
	for _, b := range v.Latency.Buckets {
		got = append(got, b.Le)
	}
	if !slices.Equal(got[:len(r39BucketBounds)], r39BucketBounds) {
		t.Errorf("named buckets:\n got %v\nwant %v", got[:len(r39BucketBounds)], r39BucketBounds)
	}
	// "+Inf" is a string because encoding/json cannot marshal a float +Inf at all
	// (`json: unsupported value: +Inf`); R9.13 counts that bucket as the
	// histogram's _count, so a numeric field would make it unrepresentable.
	if got[len(r39BucketBounds)] != "+Inf" {
		t.Errorf("last bucket is %q, want \"+Inf\"", got[len(r39BucketBounds)])
	}

	// A Prometheus histogram is cumulative: each bucket counts every observation
	// at or below its bound, so counts are non-decreasing and the +Inf bucket is
	// the total. That invariant is what makes the histogram auditable, and it is
	// the one a hand-written fixture is most likely to break.
	var prev int64
	for _, b := range v.Latency.Buckets {
		if b.Count < prev {
			t.Errorf("bucket le=%q count %d is below the previous bucket's %d; histogram counts are cumulative",
				b.Le, b.Count, prev)
		}
		prev = b.Count
	}
	last := v.Latency.Buckets[len(v.Latency.Buckets)-1]
	if last.Count != v.Latency.Count {
		t.Errorf("+Inf bucket count %d != count %d (R9.13's _count); every bucketed duration "+
			"must be counted exactly once", last.Count, v.Latency.Count)
	}
}

// TestQueryDetailIsR107sFieldSet checks QueryDetailView against R10.7's sentence
// the same way TestR32ColumnSetIsComplete checks QueryView against R3.2's.
func TestQueryDetailIsR107sFieldSet(t *testing.T) {
	want := []string{
		// R10.7: "header (fingerprint, database, user, first-seen), the per-minute
		// load chart for that query, latency panel (P50/P95/P99 or the R3.9
		// fallback), plan samples list with a JSON/plan viewer, and
		// recommendations."
		"fingerprint", "database", "user", "firstSeen",
		"latency", "plans", "recommendations",
		// R3.11's plan-change events, which R10.7's sentence does not name but
		// which are the detail page's only source for that requirement.
		"planChanges",
		// From the embedded QueryView: the rest of R3.2's row.
		"queryid", "query", "truncated", "namespace", "cluster", "pgMajor",
		"loadByTotalTime", "pctLoadByQueries", "avgExecTimeMs", "totalExecTimeMs",
		"avgRowsReturned", "timesCalled", "loadByCPUMs", "loadByIOWaitMs",
	}
	got := jsonKeys(reflect.TypeOf(QueryDetailView{}))
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("QueryDetailView's wire properties do not match R10.7.\ngot  %v\nwant %v", got, want)
	}
	// The per-minute load chart is R10.7's one element with no field here. The
	// point type now lives in this package (dto/series.go), so the reason is no
	// longer an import direction — it is that the chart is served by R8.11's
	// route, and a copy inlined here would be a second answer to a question the
	// client can already ask.
	if slices.Contains(got, "points") || slices.Contains(got, "series") {
		t.Error("QueryDetailView inlines a series field; the detail chart is served by " +
			"R8.11's route (dto/series.go) — see this file's header")
	}
}

// TestQueryDetailPercentilesAreTheR39Fallback checks the pointer rule on
// LatencyView: an absent percentile and a zero one are different answers, and the
// fixture has to show the absent case because that is R3.9's v1 answer.
func TestQueryDetailPercentilesAreTheR39Fallback(t *testing.T) {
	var v QueryDetailView
	readFixture(t, queryDetailPath, &v)

	if v.Latency.P50Ms != nil || v.Latency.P95Ms != nil || v.Latency.P99Ms != nil {
		t.Error("the fixture populates P50/P95/P99, so it no longer exercises R10.7's " +
			"\"or the R3.9 fallback\" branch; v1 has no distribution to take them from")
	}
	if !v.Latency.Sampled {
		t.Error("sampled = false with a populated histogram; R3.9's v1 histogram is the " +
			"sampled one and the UI must render its \"sampled, biased toward long " +
			"queries\" label")
	}
	// The four stats are the fallback R10.7 falls back to, so they must be present
	// whenever the percentiles are not.
	if v.Latency.MeanMs <= 0 {
		t.Error("meanMs is 0 while the percentiles are absent: R3.9's fallback is min/mean/max/stddev")
	}
}

// TestPlanSampleStatesAreTheSpecs checks the two enum groups on PlanSampleView
// against the wording R3.10 puts in the spec, and that the fixture exercises both
// states — which is the half that decides whether the UI needs a state renderer at
// all.
func TestPlanSampleStatesAreTheSpecs(t *testing.T) {
	if PlanAvailable != "available" {
		t.Errorf("PlanAvailable = %q", PlanAvailable)
	}
	// R3.10's (c) quotes the string: "we report `unavailable (insufficient table
	// privileges)` as a state, not as a per-query failure". The constant is that
	// string verbatim so there is exactly one spelling of it in the codebase.
	if PlanUnavailableTablePrivileges != "unavailable (insufficient table privileges)" {
		t.Errorf("PlanUnavailableTablePrivileges = %q, want R3.10's verbatim wording",
			PlanUnavailableTablePrivileges)
	}
	for _, m := range []string{MethodExplain, MethodPrepareExecute, MethodAnalyze} {
		if m == "" {
			t.Error("empty method constant")
		}
	}
	if MethodPrepareExecute != "prepare_execute" {
		t.Errorf("MethodPrepareExecute = %q; R3.10's (b) names PREPARE + EXPLAIN EXECUTE", MethodPrepareExecute)
	}

	var env Envelope[PlanSampleView]
	readFixture(t, queryPlansPath, &env)

	seen := map[string]bool{}
	for _, p := range env.Data {
		seen[p.Availability] = true
		switch p.Availability {
		case PlanAvailable, PlanUnavailableTablePrivileges:
		default:
			t.Errorf("plan sample %q has unknown availability %q", p.Method, p.Availability)
		}
		switch p.Method {
		case MethodExplain, MethodPrepareExecute, MethodAnalyze:
		default:
			t.Errorf("plan sample has unknown method %q", p.Method)
		}
		if p.Availability == PlanAvailable && len(p.PlanJSON) == 0 {
			t.Errorf("an available sample carries no plan JSON")
		}
		if p.Availability == PlanAvailable && p.PlanHash == "" {
			t.Errorf("an available sample carries no planHash (R3.11 needs it to detect a change)")
		}
		if p.Availability == PlanUnavailableTablePrivileges && p.PlanJSON != nil {
			t.Errorf("an unavailable sample carries plan JSON; R3.10's (c) has no plan to carry")
		}
		if p.SQL == "" {
			t.Errorf("a sample with method %q names no sql; R3.10's (a) makes the plan belong "+
				"to the raw first-seen text, so the text is the sample's only subject", p.Method)
		}
	}
	for _, want := range []string{PlanAvailable, PlanUnavailableTablePrivileges} {
		if !seen[want] {
			t.Errorf("the fixture has no %q sample, so the UI's state rendering is untested", want)
		}
	}
}

// TestTagViewCarriesR312sDimensions checks TagView against the field list R3.12
// quotes from Cloud SQL's pertag metrics, plus the two spellings the requirement
// settles on.
func TestTagViewCarriesR312sDimensions(t *testing.T) {
	want := []string{
		// R3.12: "Cloud SQL's `pertag` metrics carry `action, application,
		// controller, db_driver, framework, route, tag_hash`" — plus R3.12's (a)
		// additions (usename) and R3.7's load columns.
		"tagHash",
		"action", "application", "controller", "dbDriver", "framework", "route",
		"database", "user", // R3.12's (a): datname, usename
		// R3.7's load columns (embedded LoadColumns) plus the one column R3.7
		// sources at rollup level and R3.2 withholds per query.
		"loadByTotalTime", "pctLoadByQueries", "avgExecTimeMs", "totalExecTimeMs",
		"avgRowsReturned", "timesCalled", "loadByCPUMs", "loadByIOWaitMs",
		"lockWaitMs",
	}
	got := jsonKeys(reflect.TypeOf(TagView{}))
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("TagView's wire properties do not match R3.12 + R3.7.\ngot  %v\nwant %v", got, want)
	}

	// R3.12 retires a spelling: "One spelling: `pgsi:` — the spec's earlier
	// `pgsio:` variant is retired." The comment key is `dbdriver` and Cloud SQL's
	// metric is `db_driver`; the field is `dbDriver`, which is documented as a
	// deliberate correspondence between the two rather than a typo.
	if slices.Contains(got, "db_driver") {
		t.Error(`TagView uses snake_case "db_driver"; this package's convention is camelCase`)
	}
}

// TestTagsFixtureIsSane checks the tag fixture's internal consistency: a tag's
// hash is a 64-hex digest, tags are unique, and the (a) dimensions — which R3.12
// calls "the always-available dimensions" — are populated on every row.
func TestTagsFixtureIsSane(t *testing.T) {
	var env Envelope[TagView]
	readFixture(t, tagsPath, &env)
	if len(env.Data) == 0 {
		t.Fatalf("%s has no rows", tagsPath)
	}

	seen := map[string]bool{}
	var withComment int
	for _, tag := range env.Data {
		if len(tag.TagHash) != 64 || strings.ToLower(tag.TagHash) != tag.TagHash {
			t.Errorf("tagHash %q is not 64 lowercase hex characters", tag.TagHash)
		}
		if seen[tag.TagHash] {
			t.Errorf("duplicate tagHash %s", tag.TagHash)
		}
		seen[tag.TagHash] = true
		// The fixture's hash must be TagDigest of the row's own dimensions, or the
		// fixture is asserting a hash the contract would never produce.
		if got := TagDigest(tag); got != tag.TagHash {
			t.Errorf("tagHash %s is not TagDigest of this row's dimensions (%s)", tag.TagHash, got)
		}

		// R3.12's (a) dimensions are always available; the (b) comment dimensions
		// are opt-in and may be empty.
		if tag.Application == "" {
			t.Errorf("tag %s has no application_name; R3.12's (a) makes it always available", tag.TagHash[:8])
		}
		if tag.Database == "" || tag.User == "" {
			t.Errorf("tag %s has database=%q user=%q; R3.12's (a) makes datname and usename "+
				"always available", tag.TagHash[:8], tag.Database, tag.User)
		}
		if tag.Action != "" || tag.Route != "" || tag.Controller != "" || tag.Framework != "" || tag.DBDriver != "" {
			withComment++
		}
	}
	if withComment == 0 {
		t.Error("no fixture tag has a comment dimension, so R3.12's (b) half is unexercised")
	}
	if withComment == len(env.Data) {
		t.Error("every fixture tag has comment dimensions; the (a)-only case — a statement " +
			"with no /* pgsi:… */ comment — is unexercised")
	}
}

// TestEnvelopeShapeMatchesR84 checks the three new envelopes against R8.4's
// sentence: "every list endpoint supports … and returns `{data, meta:{total,
// truncated, generatedAt, nextCursor}}`".
func TestEnvelopeShapeMatchesR84(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"Envelope[QueryView]", reflect.TypeOf(Envelope[QueryView]{})},
		{"Envelope[PlanSampleView]", reflect.TypeOf(Envelope[PlanSampleView]{})},
		{"Envelope[TagView]", reflect.TypeOf(Envelope[TagView]{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonKeys(tc.typ)
			slices.Sort(got)
			want := []string{"data", "meta"}
			if !slices.Equal(got, want) {
				t.Errorf("got %v, want %v (R8.4's {data, meta})", got, want)
			}
		})
	}
	if got := jsonKeys(reflect.TypeOf(Meta{})); !slices.Contains(got, "total") ||
		!slices.Contains(got, "truncated") || !slices.Contains(got, "generatedAt") ||
		!slices.Contains(got, "nextCursor") {
		t.Errorf("Meta is missing one of R8.4's total/truncated/generatedAt/nextCursor: %v", got)
	}
}

// TestQueriesResponseWireKeys pins the two types this commit adds to the wire
// surface they promise.
//
// jsonKeys is the instrument: it asks encoding/json what the struct emits rather
// than reading the field list, so a renamed tag, a reordered field or a new
// omitempty shows up here — the same bar TestR32ColumnSetIsComplete holds
// QueryView to. For QueriesResponse the assertion is that the body is R8.4's two
// envelope keys plus R3.1's one: a fourth key needs a sentence in
// requirements.md naming it before it needs a field here.
//
// The Installed assertion is the one that is not a naming check. The tri-state is
// a type fact — *bool has three states and bool has two — and a preference stated
// only in a docblock is the kind of claim this project has learned to distrust, so
// the pointer is asserted through reflect rather than argued in prose.
func TestQueriesResponseWireKeys(t *testing.T) {
	got := jsonKeys(reflect.TypeOf(QueriesResponse{}))
	want := []string{"data", "meta", "pgStatStatements"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(want, got) {
		t.Errorf("QueriesResponse's wire properties.\n got %v\nwant %v", got, want)
	}

	got = jsonKeys(reflect.TypeOf(ExtensionPresenceView{}))
	want = []string{"namespace", "cluster", "database", "installed"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(want, got) {
		t.Errorf("ExtensionPresenceView's wire properties.\n got %v\nwant %v", got, want)
	}

	f, ok := reflect.TypeOf(ExtensionPresenceView{}).FieldByName("Installed")
	if !ok {
		t.Fatal("ExtensionPresenceView has no Installed field")
	}
	if f.Type.Kind() != reflect.Ptr || f.Type.Elem().Kind() != reflect.Bool {
		t.Errorf("Installed is %s, want *bool (R3.1's two sources make `unknown` a real answer and bool has no encoding for it)", f.Type)
	}
	if tag := f.Tag.Get("json"); tag != "installed,omitempty" {
		t.Errorf("Installed json tag is %q, want `installed,omitempty` (a nil pointer must be absent from the body, not `null`)", tag)
	}
}

// TestQueryFixturePresenceRowsMatchItsRows is the cross-check the presence array
// needs now that the fixture carries one.
//
// A pgStatStatements list that disagrees with the fixture's own data rows is worse
// than the missing key it replaced: R10.27's "not installed" banner would render
// over a page full of rows measured from that database. So every
// (namespace, cluster, database) triple a query row names must have a presence
// row, every presence row's cluster must exist in clusters.json, and the fixture
// must exercise all three states of the *bool — a fixture of only `true` values
// would let the UI's false and unknown branches ship unrendered, which is the
// defect the presence field exists to make renderable.
func TestQueryFixturePresenceRowsMatchItsRows(t *testing.T) {
	_, clusters := readGolden(t)

	var resp QueriesResponse
	readFixture(t, queriesPath, &resp)

	known := map[string]bool{}
	for _, c := range clusters.Data {
		known[c.Namespace+"/"+c.Name] = true
	}

	present := map[string]bool{}
	states := map[string]int{}
	for _, p := range resp.PGStatStatements {
		if !known[p.Namespace+"/"+p.Cluster] {
			t.Errorf("presence row %s/%s names a cluster testdata/api/clusters.json does not list",
				p.Namespace, p.Cluster)
		}
		k := p.Namespace + "/" + p.Cluster + "/" + p.Database
		if present[k] {
			t.Errorf("duplicate presence row for %s", k)
		}
		present[k] = true

		switch {
		case p.Installed == nil:
			states["absent"]++
		case *p.Installed:
			states["true"]++
		default:
			states["false"]++
		}
	}

	for _, row := range resp.Data {
		k := row.Namespace + "/" + row.Cluster + "/" + row.Database
		if !present[k] {
			t.Errorf("query row %s is measured from %s, which has no pg_stat_statements presence row",
				row.Query, k)
		}
	}

	for _, want := range []string{"true", "false", "absent"} {
		if states[want] == 0 {
			t.Errorf("no presence row exercises installed=%s, so the UI branch that renders it ships unrendered", want)
		}
	}
}
