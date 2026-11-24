package collector

import (
	"context"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/cnpgmetrics"
	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// The compile-time half of the contract: the pooler collector is a Collector,
// checked by the compiler rather than by a test that has to remember to call it.
var _ Collector = (*PoolerCollector)(nil)

// poolerFixture is the S5 exposition every pooler assertion here is measured
// against: the pgbouncer exporter's :9127 body for
// ai-api-gateway/rev-proxy-pg-pooler-86b4bdd84d-n9vlv.
//
// The path is relative to this package and mirrors cnpgmetrics/parse_test.go's
// fixtureDir const ("../../../testdata/cnpg-metrics"). The fixtures live at the
// repo root so one copy serves both packages — which matters here because this
// file's central assertion is about the same bytes parse_test.go asserts on, seen
// through the collector instead of through the parser.
const poolerFixture = "../../testdata/cnpg-metrics/ai-api-gateway__rev-proxy-pg-pooler-86b4bdd84d-n9vlv__9127.txt"

// poolerFixturePGBouncerSamples is the number of cnpg_pgbouncer_* sample lines in
// that file, and therefore the number of Samples Collect must return.
//
// 123, measured by:
//
//	grep -v '^#' <fixture> | grep -c 'cnpg_pgbouncer_'          -> 123
//	grep -v '^#' <fixture> | wc -l                              -> 158 (35 are go_*)
//
// The number is asserted absolutely rather than derived from the parser, because a
// derived assertion is a tautology: a test that compares poolerSamples against
// whatever the parser returned still passes if the registry loses a family. The
// absolute count is the one that notices.
const poolerFixturePGBouncerSamples = 123

// stubFetch is the MetricsFetch seam filled with a canned response.
//
// It is a struct with a method rather than a closure so the calls it received are
// assertable: the seam's contract is not only what it returns but what it was
// asked for, and Target.Address is the only place a pooler collector can learn the
// URL. A collector that fetched a hardcoded :9127 instead of tgt.Address would
// pass a return-value-only test.
type stubFetch struct {
	status int
	body   string
	err    error

	calls int
	urls  []string
}

func (s *stubFetch) fetch(_ context.Context, url string) (int, []byte, error) {
	s.calls++
	s.urls = append(s.urls, url)
	return s.status, []byte(s.body), s.err
}

// poolerFixtureBody reads the fixture once per test and fails loudly rather than
// skipping: a missing fixture here means the S5 collector has no measured input at
// all, which is a failure and not a condition to tolerate.
func poolerFixtureBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(poolerFixture)
	if err != nil {
		t.Fatalf("read fixture %s: %v", poolerFixture, err)
	}
	return string(b)
}

// poolerTarget is a Target whose Address is the string the stub asserts it was
// called with.
func poolerTarget() Target {
	return Target{
		Namespace: "ai-api-gateway",
		Cluster:   "rev-proxy-pg",
		Pod:       "rev-proxy-pg-pooler-86b4bdd84d-n9vlv",
		Role:      "",
		Address:   "http://10.42.7.19:9127/metrics",
		Source:    SourcePoolerMetrics,
		PGMajor:   16,
	}
}

// poolerCfg is the config every pooler test uses: the package's shared injectable
// clock (database_test.go:77's fixedClock, the one every SQL collector test uses)
// as the Now, and no PoolerName, which is the shipped default.
//
// Reusing that var rather than minting a pooler-specific clock is what keeps
// "the collector stamped the injected time" one fact in the package instead of two.
var poolerCfg = PoolerConfig{Now: func() time.Time { return fixedClock }}

// mustPoolerCollector builds a collector over a stub and fails instead of
// returning a half-built one, so the tests below never assert against a nil.
func mustPoolerCollector(t *testing.T, s *stubFetch, cfg PoolerConfig) *PoolerCollector {
	t.Helper()
	c, err := NewPoolerCollector(s.fetch, cfg)
	if err != nil {
		t.Fatalf("NewPoolerCollector: %v", err)
	}
	if c == nil {
		t.Fatal("NewPoolerCollector returned a nil collector with a nil error")
	}
	return c
}

func TestNewPoolerCollectorRejectsNilFetch(t *testing.T) {
	// The constructor refuses rather than defaulting. The SQL collectors get an
	// opener they can fall back to a zero-value pool on; a pooler collector with
	// no way to issue an HTTP GET cannot produce a sample, so returning one with
	// a nil error would move a start-up failure to a run-time one.
	c, err := NewPoolerCollector(nil, poolerCfg)
	if err == nil {
		t.Fatal("NewPoolerCollector(nil, ...) error = nil, want one")
	}
	if c != nil {
		t.Errorf("NewPoolerCollector(nil, ...) = %v, want nil alongside the error", c)
	}
	// The message names the collector, because this error surfaces in a scheduler
	// log line alongside thirteen other constructors' failures.
	if !strings.Contains(err.Error(), CollectorPooler) {
		t.Errorf("error %q does not name collector %q", err, CollectorPooler)
	}
}

func TestPoolerDescriptor(t *testing.T) {
	c := mustPoolerCollector(t, &stubFetch{status: http.StatusOK, body: poolerFixtureBody(t)}, PoolerConfig{})
	d := c.Descriptor()

	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor.Validate: %v", err)
	}
	if d.Name != CollectorPooler {
		t.Errorf("Name = %q, want %q (§3.3's row name, and the R2.2 `collector` label)", d.Name, CollectorPooler)
	}
	if d.Interval != PoolerInterval {
		t.Errorf("Interval = %s, want %s (§3.3's 60 s for the pooler row)", d.Interval, PoolerInterval)
	}
	if d.Source != SourcePoolerMetrics {
		t.Errorf("Source = %s, want %s (§3.2's S5, the pooler pod's :9127/metrics)", d.Source, SourcePoolerMetrics)
	}
	if d.Cost != CostCheap {
		t.Errorf("Cost = %s, want %s (an HTTP GET costs no SQL round-trip, collector.go:131)", d.Cost, CostCheap)
	}
	// Empty by design. collector.go's rule is that a non-SQL collector leaves
	// ViewGroups empty, "which is how the framework tells 'no transactions' from
	// 'one empty one'". A non-empty value here would claim a transaction the
	// collector never opens.
	if len(d.ViewGroups) != 0 {
		t.Errorf("ViewGroups = %v, want empty (no SQL, so no view group to wrap in a transaction)", d.ViewGroups)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's 10 s default; a cheap collector does not set a shorter one)", d.Timeout)
	}
	// nil AppliesTo is the framework's "no opinion about versions", and it is the
	// correct answer here for the reason collector.go:168-172 gives: the
	// pgbouncer exporter's output varies by pgbouncer version, not by PostgreSQL
	// major.
	if d.AppliesTo != nil {
		t.Errorf("AppliesTo = non-nil, want nil (AllMajors: this source does not vary by PG major)")
	}
	for _, major := range pgversion.Majors() {
		if !d.Applies(major) {
			t.Errorf("Applies(%d) = false, want true (nil AppliesTo is AllMajors)", major)
		}
	}
	for _, major := range []int{11, 12, 19, 20} {
		if !d.Applies(major) {
			t.Errorf("Applies(%d) = false, want true (off-matrix majors are still AllMajors)", major)
		}
	}
}

func TestPoolerDescriptorIsStable(t *testing.T) {
	// R2.1: "It must be side-effect-free and return the same value every call:
	// the registry calls it once at registration, and the scheduler calls it per
	// round." The descriptor is a stored field, so this is cheap to pin.
	//
	// The func field needs its own comparison rather than riding along in the
	// struct one. reflect.DeepEqual's rule is "Func values are deeply equal if
	// both are nil; otherwise they are not deeply equal" — so a DeepEqual over the
	// whole Descriptor fails for ANY non-nil AppliesTo, and fails printing two
	// identical values. A3's S56 mutation (pooler.go:152 AppliesTo nil ->
	// AllMajors) prints `AppliesTo:0x104e76fb0` on both sides and calls that a
	// difference. Comparing the struct with the func zeroed is the form that can
	// fail for the right reason; the func field itself is only assertable directly
	// when it is nil, which is what this collector declares, so its stability is
	// checked behaviourally by sampling Applies() on both descriptors.
	c := mustPoolerCollector(t, &stubFetch{status: http.StatusOK}, PoolerConfig{})
	first := c.Descriptor()
	second := c.Descriptor()

	noFunc := func(d Descriptor) Descriptor { d.AppliesTo = nil; return d }
	if !reflect.DeepEqual(noFunc(first), noFunc(second)) {
		t.Errorf("Descriptor() differs between calls:\n first  = %+v\n second = %+v", noFunc(first), noFunc(second))
	}

	// The func field, compared by what it answers instead of by what it is. This
	// is the assertion that survives an AppliesTo that is a real function: two
	// distinct funcs with the same behaviour pass, which is what "same value every
	// call" means for a field whose value is a computation.
	for _, major := range []int{11, 12, 15, 16, 17, 18, 19, 20} {
		if first.Applies(major) != second.Applies(major) {
			t.Errorf("Applies(%d) differs between calls: first %v, second %v",
				major, first.Applies(major), second.Applies(major))
		}
	}
}

func TestPoolerConfigWithDefaults(t *testing.T) {
	t.Run("Now nil becomes time.Now", func(t *testing.T) {
		got := PoolerConfig{}.withDefaults()
		if got.Now == nil {
			t.Fatal("withDefaults left Now nil, want time.Now")
		}
		if got.Now().IsZero() {
			t.Error("withDefaults produced a Now that returns the zero time")
		}
	})

	t.Run("Now set is preserved", func(t *testing.T) {
		got := poolerCfg.withDefaults()
		if !got.Now().Equal(fixedClock) {
			t.Errorf("withDefaults replaced Now: got %s, want %s", got.Now(), fixedClock)
		}
	})

	t.Run("PoolerName nil stays nil", func(t *testing.T) {
		// withDefaults must not invent one. Omitting the label is the shipped
		// default (PoolerConfig.PoolerName's doc lists three candidate sources
		// and picks none of them), and a default that guessed — say by returning
		// t.Pod — would ship a guess inside a function named withDefaults.
		got := PoolerConfig{}.withDefaults()
		if got.PoolerName != nil {
			t.Errorf("withDefaults set PoolerName, want it left nil (omitting the label is the default)")
		}
	})

	t.Run("PoolerName set is preserved", func(t *testing.T) {
		want := func(Target) string { return "rev-proxy-pg-pooler" }
		got := PoolerConfig{PoolerName: want}.withDefaults()
		if got.PoolerName == nil {
			t.Fatal("withDefaults dropped a PoolerName that was set")
		}
		if got.PoolerName(Target{}) != "rev-proxy-pg-pooler" {
			t.Errorf("PoolerName changed behaviour: got %q", got.PoolerName(Target{}))
		}
	})
}

func TestPoolerCollectWrapsFetchError(t *testing.T) {
	// The fetch error must survive unwrapping: a scheduler that classifies a
	// failed round by error kind cannot do it if the collector flattened the
	// cause into a string.
	sentinel := errors.New("dial tcp 10.42.7.19:9127: connect: connection refused")
	c := mustPoolerCollector(t, &stubFetch{err: sentinel}, poolerCfg)

	res, err := c.Collect(context.Background(), poolerTarget())
	if err == nil {
		t.Fatal("Collect error = nil, want the fetch error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("Collect error %q does not wrap the fetch error %v (the %%w verb is what keeps it unwrappable)", err, sentinel)
	}
	if !strings.Contains(err.Error(), poolerTarget().Address) {
		t.Errorf("Collect error %q does not name the URL it failed to fetch", err)
	}
	// An error round returns no samples, not a partial set.
	if len(res.Samples) != 0 {
		t.Errorf("error round returned %d samples, want 0", len(res.Samples))
	}
}

func TestPoolerCollectRejectsNon200(t *testing.T) {
	// A non-200 is an error, not an empty round. An exporter that answers 500
	// with a truncated body has not measured anything; parsing what came back and
	// publishing it is the R8.7 failure shape in collector form — a round that
	// reports success and is empty.
	body := poolerFixtureBody(t)
	for _, status := range []int{http.StatusCreated, http.StatusMovedPermanently, http.StatusBadRequest,
		http.StatusUnauthorized, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c := mustPoolerCollector(t, &stubFetch{status: status, body: body}, poolerCfg)
			res, err := c.Collect(context.Background(), poolerTarget())
			if err == nil {
				t.Fatalf("status %d: error = nil, want one (a non-200 is not an empty round)", status)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Errorf("status %d: error %q does not report the status it got", status, err)
			}
			if len(res.Samples) != 0 {
				t.Errorf("status %d: %d samples returned alongside the error, want 0 (the body was not parsed)",
					status, len(res.Samples))
			}
		})
	}
}

func TestPoolerCollectFixture(t *testing.T) {
	body := poolerFixtureBody(t)
	s := &stubFetch{status: http.StatusOK, body: body}
	c := mustPoolerCollector(t, s, poolerCfg)
	tgt := poolerTarget()

	res, err := c.Collect(context.Background(), tgt)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// The URL came from Target.Address — the seam Target's own doc promises
	// ("host:port for an S2 target and a full URL for an HTTP source"). One call
	// per round: a collector that retried inside Collect would blow the R13.1
	// budget arithmetic that RoundTrips exists to make measurable.
	if s.calls != 1 {
		t.Errorf("fetch called %d times, want exactly 1 per round", s.calls)
	}
	if len(s.urls) != 1 || s.urls[0] != tgt.Address {
		t.Errorf("fetch URL = %v, want [%s] (Target.Address is the only address a pooler collector has)", s.urls, tgt.Address)
	}

	// R13.1 counts SQL round-trips and an HTTP GET is not one. Reporting 1 here
	// would make the budget depend on how many collectors happen to be non-SQL.
	if res.RoundTrips != 0 {
		t.Errorf("RoundTrips = %d, want 0 (a non-SQL collector reports 0, collector.go)", res.RoundTrips)
	}

	if len(res.Samples) != poolerFixturePGBouncerSamples {
		t.Errorf("samples = %d, want %d (the fixture's pgbouncer series count)",
			len(res.Samples), poolerFixturePGBouncerSamples)
	}

	// A real value from the fixture, not a constructed one: the count and the
	// names prove the right series exist, this proves the number attached to one
	// of them is the number the exposition carried. Fixture line
	//   cnpg_pgbouncer_pools_pool_mode{database="gateway",user="revproxy"} 2
	// which naming.go:507 re-exports as pgbouncer_pools_pool_mode.
	t.Run("fixture value survives the projection", func(t *testing.T) {
		for _, sm := range res.Samples {
			if sm.Metric != "pgbouncer_pools_pool_mode" {
				continue
			}
			if sm.Labels["database"] != "gateway" || sm.Labels["user"] != "revproxy" {
				continue
			}
			if sm.Value != 2 {
				t.Errorf(`pgbouncer_pools_pool_mode{database="gateway",user="revproxy"} = %v, want 2`, sm.Value)
			}
			return
		}
		t.Error(`no sample for pgbouncer_pools_pool_mode{database="gateway",user="revproxy"} in the projection`)
	})

	for i, sm := range res.Samples {
		// An empty Metric renders as a family named "" — the exact failure
		// collector.go's Validate exists to prevent.
		if sm.Metric == "" {
			t.Fatalf("sample %d has an empty Metric (a family the naming table has no row for)", i)
		}
		// IngestTime is the collector's job, not the scheduler's: the SQL
		// collectors stamp it themselves (database.go:435, activity.go:878) and
		// so must this one, from the injected clock.
		if !sm.IngestTime.Equal(fixedClock) {
			t.Errorf("sample %d IngestTime = %s, want %s (the injected Now)", i, sm.IngestTime, fixedClock)
		}
		// SourceTime stays zero because cnpgmetrics.Sample has no timestamp field
		// to carry — the text format allows one and this exporter never sends it.
		if !sm.SourceTime.IsZero() {
			t.Errorf("sample %d SourceTime = %s, want zero (the exposition carries no source clock)", i, sm.SourceTime)
		}
		// StatsReset stays zero: no cnpg_pgbouncer_*reset* family exists, so S5
		// has no counter epoch to report.
		if !sm.StatsReset.IsZero() {
			t.Errorf("sample %d StatsReset = %s, want zero (no pgbouncer reset family exists)", i, sm.StatsReset)
		}
		// The four identity labels are scheduler.go:498's ApplyIdentityLabels'
		// job — "the framework's single contact point for them and the reason no
		// collector builds them itself" (the R16.8 failure class). A collector
		// that set them here would be a fourteenth implementation of R9.3.
		for k := range sm.Labels {
			switch k {
			case "namespace", "cluster", "pod", "instance_role":
				t.Errorf("sample %d carries identity label %q=%q; ApplyIdentityLabels owns those, not the collector",
					i, k, sm.Labels[k])
			}
		}
	}
}

func TestPoolerSamplesScopesToPGBouncerGroup(t *testing.T) {
	// The fixture alone cannot test the scoping rule on its own: a 9127 body
	// contains only pgbouncer and go_* families, and the go_* families land in
	// Scrape.Other, which poolerSamples never reads. Building the Scrape by hand
	// is what makes "the group check is the whole scoping rule" an assertion
	// rather than an observation.
	pgFam := &cnpgmetrics.Family{
		Name:  "cnpg_pgbouncer_pools_cl_active",
		Type:  TypeGauge,
		Group: cnpgmetrics.GroupPGBouncer,
	}
	instFam := &cnpgmetrics.Family{
		Name:  "cnpg_instance_scrape_duration_seconds",
		Type:  TypeGauge,
		Group: cnpgmetrics.GroupInstance,
	}
	pgSamples := []cnpgmetrics.Sample{
		{Labels: map[string]string{"database": "appdb", "user": "appuser"}, Value: 3},
		{Labels: map[string]string{"database": "appdb", "user": ""}, Value: 0},
	}
	scrape := &cnpgmetrics.Scrape{
		CNPG: []cnpgmetrics.Metric{
			{
				Name: pgFam.Name, Type: "gauge", Family: pgFam,
				Samples: pgSamples,
			},
			{
				Name: instFam.Name, Type: "gauge", Family: instFam,
				Samples: []cnpgmetrics.Sample{{Labels: map[string]string{"instance": "pg-1"}, Value: 0.012}},
			},
			// A nil Family is not a live shape (parse.go's contract says every
			// Scrape.CNPG entry has one) but pooler.go guards it anyway, so the
			// guard is exercised rather than left uncovered.
			{Name: "", Type: "gauge", Family: nil, Samples: []cnpgmetrics.Sample{{Value: 1}}},
		},
	}

	got := poolerSamples(scrape, poolerTarget(), poolerCfg, fixedClock)
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 (the two pgbouncer series; the instance family and the nil-Family entry are both out of scope)", len(got))
	}
	for i, sm := range got {
		if sm.Metric != MetricNameFor(pgFam.Name) {
			t.Errorf("Metric = %q, want %q", sm.Metric, MetricNameFor(pgFam.Name))
		}
		// The value is the one thing the collector does to a sample and the one
		// thing a shape-only suite cannot see. With the Metric and timestamp
		// assertions in place, pooler.go:245's `Value: s.Value` can be replaced by
		// `Value: 42.0` and every other test in this file still passes — A3's S57
		// mutation. poolerSamples appends in source order, so got[i] pairs with
		// pgSamples[i].
		if sm.Value != pgSamples[i].Value {
			t.Errorf("sample %d Value = %v, want %v for Metric %q", i, sm.Value, pgSamples[i].Value, sm.Metric)
		}
	}
}

func TestPoolerSamplesSkipsFamilyWithNoNamingRow(t *testing.T) {
	// D20's S8 class: a family present in the exporter and absent from the naming
	// table. MetricNameFor returns "" for it, and emitting that would produce a
	// Sample with an empty Metric — which renders as a family named "" and is the
	// failure collector.go's Validate exists to prevent.
	fam := &cnpgmetrics.Family{
		Name:  "cnpg_pgbouncer_not_in_the_table",
		Type:  TypeGauge,
		Group: cnpgmetrics.GroupPGBouncer,
	}
	if MetricNameFor(fam.Name) != "" {
		t.Fatalf("precondition: MetricNameFor(%q) = %q, want \"\" (the test needs a name the table truly lacks)",
			fam.Name, MetricNameFor(fam.Name))
	}
	scrape := &cnpgmetrics.Scrape{
		CNPG: []cnpgmetrics.Metric{
			{Name: fam.Name, Type: "gauge", Family: fam, Samples: []cnpgmetrics.Sample{{Value: 1}}},
		},
	}
	if got := poolerSamples(scrape, poolerTarget(), poolerCfg, fixedClock); len(got) != 0 {
		t.Errorf("samples = %d, want 0 (a family with no naming row is skipped, not emitted with an empty Metric)", len(got))
	}
}

func TestPoolerSamplesNilScrape(t *testing.T) {
	if got := poolerSamples(nil, poolerTarget(), poolerCfg, fixedClock); got != nil {
		t.Errorf("poolerSamples(nil, ...) = %v, want nil", got)
	}
}

func TestPoolerLabelsCopiesTheSourceMap(t *testing.T) {
	// cnpgmetrics hands out the map it parsed. The scheduler's
	// ApplyIdentityLabels writes into the samples it is given, so sharing one map
	// between the parse result and a published Sample means a later round mutates
	// the earlier one's labels. The copy is the contract, and the way to test a
	// copy is to mutate the copy.
	src := map[string]string{"database": "appdb", "user": "appuser"}
	got := poolerLabels(src, poolerTarget(), PoolerConfig{})

	if len(got) != 2 {
		t.Fatalf("labels = %v, want the two source labels copied", got)
	}
	if got["database"] != "appdb" || got["user"] != "appuser" {
		t.Errorf("copied labels = %v, want {database:appdb user:appuser}", got)
	}
	got["injected"] = "x"
	if _, ok := src["injected"]; ok {
		t.Error("mutating the returned map also mutated the source map (poolerLabels returned the map, not a copy)")
	}
}

func TestPoolerLabelsPoolerName(t *testing.T) {
	tgt := poolerTarget()

	tests := []struct {
		name       string
		poolerName func(Target) string
		want       string // "" means the label must be absent
	}{
		{"nil derives nothing, so the label is omitted", nil, ""},
		{"a derived name is set", func(Target) string { return "rev-proxy-pg-pooler" }, "rev-proxy-pg-pooler"},
		// An empty string is not a value: R9.3's label set either names a pooler
		// or does not, and `pooler=""` is a series with a label that answers
		// nothing.
		{"an empty derivation is omitted", func(Target) string { return "" }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := PoolerConfig{PoolerName: tc.poolerName}
			got := poolerLabels(map[string]string{"database": "appdb"}, tgt, cfg)

			v, ok := got["pooler"]
			if tc.want == "" {
				if ok {
					t.Errorf("labels = %v, want no `pooler` key", got)
				}
				return
			}
			if !ok {
				t.Fatalf("labels = %v, want pooler=%q", got, tc.want)
			}
			if v != tc.want {
				t.Errorf("pooler = %q, want %q", v, tc.want)
			}
		})
	}
}

func TestPoolerLabelsPassesTheRealTarget(t *testing.T) {
	// PoolerName's whole job is to derive a per-target value, so a caller that
	// passed a zero Target would make every pooler in the fleet look identical —
	// and with a nil-returning default that bug is invisible, because nil never
	// gets called. Capturing the argument is the only way to see it.
	want := poolerTarget()
	var gotTarget Target
	called := false
	cfg := PoolerConfig{PoolerName: func(tg Target) string {
		gotTarget, called = tg, true
		return "rev-proxy-pg-pooler"
	}}

	poolerLabels(nil, want, cfg)
	if !called {
		t.Fatal("PoolerName was not called")
	}
	if gotTarget != want {
		t.Errorf("PoolerName received %+v, want %+v", gotTarget, want)
	}
}

func TestPoolerCollectUsesInjectedClock(t *testing.T) {
	// The clock is injected rather than read inline so R13.11's ingest timestamp
	// is testable at all; this pins that Collect actually calls cfg.Now() and does
	// not reach for time.Now, which would make every test's IngestTime assertion
	// depend on wall-clock luck.
	body := poolerFixtureBody(t)
	c := mustPoolerCollector(t, &stubFetch{status: http.StatusOK, body: body}, PoolerConfig{})
	res, err := c.Collect(context.Background(), poolerTarget())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Samples) == 0 {
		t.Fatal("no samples to check IngestTime against")
	}
	// Default config: Now is time.Now, so IngestTime must be recent rather than
	// the zero time.
	if res.Samples[0].IngestTime.IsZero() {
		t.Error("IngestTime is zero with a default config (withDefaults did not install time.Now)")
	}
	if time.Since(res.Samples[0].IngestTime) > time.Minute {
		t.Errorf("IngestTime = %s, want within a minute of now (Collect read an injected clock, not time.Now)",
			res.Samples[0].IngestTime)
	}
}
