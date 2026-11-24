package metrics_test

// Tests for R13.5's pgsi_store_size_bytes family (store.go), R9.8's sixth.
//
// The family is a gauge with no labels, so unlike the HTTP family there is no
// histogram shape to pin — the whole contract is the name, the type line, and
// where the number comes from. That last part is what
// TestStoreSizeTracksTheRealSink is for: the family exists because
// collector.MemSink.Used() exists, and a renderer that printed a constant would
// pass every other test in this file.
//
// External package for the same reason metrics_test.go and http_test.go are: the
// family's whole API surface is exported, and withVersion — the test helper that
// pins the two ldvars so BuildInfo's golden is stable — lives here.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/metrics"
)

// fakeSizer is a metrics.StoreSizer whose entire state is the number it returns.
// The tests that need a value use this rather than a MemSink so the expected
// exposition is a literal; TestStoreSizeTracksTheRealSink covers the real type.
type fakeSizer int64

func (f fakeSizer) Used() int64 { return int64(f) }

const storeGolden4096 = `# HELP pgsi_store_size_bytes Bytes held in the process's sample store (R13.5).
# TYPE pgsi_store_size_bytes gauge
pgsi_store_size_bytes 4096
`

func TestStoreSizeRenderExact(t *testing.T) {
	if got := metrics.StoreSize(fakeSizer(4096), ""); got != storeGolden4096 {
		t.Errorf("StoreSize(fakeSizer(4096), \"\")\ngot:\n%s\nwant:\n%s", got, storeGolden4096)
	}
	// The exported base name is the string the golden's metric line is built
	// from, so a rename that leaves the golden behind fails here rather than
	// leaving the constant and the renderer disagreeing.
	if got := metrics.StoreMetricBase; got != "store_size_bytes" {
		t.Errorf("StoreMetricBase = %q, want \"store_size_bytes\"", got)
	}
}

// TestStoreSizeLabelSetIsEmpty is R9.8's naming rule checked separately from the
// value: the entry lists this family as `pgsi_store_size_bytes` with no braces,
// the way it lists build_info's {version,commit} and unlike the up family's R9.3
// set. A `store="memory"` label — the obvious thing to add once a SQLite store
// exists — is not in the spec, so this asserts the rendered line has no `{`.
func TestStoreSizeLabelSetIsEmpty(t *testing.T) {
	line := strings.TrimSuffix(strings.SplitN(metrics.StoreSize(fakeSizer(1), ""), "\n", 3)[2], "\n")
	if strings.Contains(line, "{") {
		t.Errorf("R9.8 names this family bare, got %q", line)
	}
	if line != "pgsi_store_size_bytes 1" {
		t.Errorf("sample line = %q, want %q", line, "pgsi_store_size_bytes 1")
	}
}

// TestStoreSizeNilSourceIsAbsent covers the package's "a family with no series is
// absent, not a HELP/TYPE pair beside an unmeasured value" rule, in both shapes a
// nil source arrives as. The typed-nil case is the one a plain `src == nil`
// misses — a nil *collector.MemSink in a StoreSizer interface is not == nil, and
// Used() dereferences m.mu, so without the type check this test panics. It is the
// same hazard d2415b1 removed from store.RenderPerQuery.
func TestStoreSizeNilSourceIsAbsent(t *testing.T) {
	withVersion(t, "9.9.9-test", "deadbee")

	if got := metrics.StoreSize(nil, ""); got != "" {
		t.Errorf("nil source rendered %q, want \"\"", got)
	}
	var sink *collector.MemSink
	if got := metrics.StoreSize(sink, ""); got != "" {
		t.Errorf("typed-nil *collector.MemSink rendered %q, want \"\"", got)
	}
	// The same typed nil through the hub, which is the shape a server whose sink
	// field is unset actually produces.
	if got, want := metrics.New(nil, nil, metrics.WithStoreSize(sink)).Render(), metrics.BuildInfo(); got != want {
		t.Errorf("hub with a typed-nil store =\n%s\nwant\n%s", got, want)
	}
}

func TestStoreSizePrefixArgument(t *testing.T) {
	// R9.2's contract, same as UpTracker.Render and HTTPRequests.Render: ""
	// selects collector.DefaultPrefix, any other value is used verbatim.
	if got, want := metrics.StoreSize(fakeSizer(7), ""), metrics.StoreSize(fakeSizer(7), collector.DefaultPrefix); got != want {
		t.Errorf("\"\" is not the default-prefix selector:\n%s\nvs\n%s", got, want)
	}
	want := "# HELP acme_store_size_bytes Bytes held in the process's sample store (R13.5).\n" +
		"# TYPE acme_store_size_bytes gauge\n" +
		"acme_store_size_bytes 7\n"
	if got := metrics.StoreSize(fakeSizer(7), "acme_"); got != want {
		t.Errorf("StoreSize(fakeSizer(7), \"acme_\")\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestStoreSizeTracksTheRealSink is the test that connects the family to its
// source: the rendered number must be collector.MemSink.Used() for a sink that
// has actually been written to. The expected string is built with the same
// strconv.FormatInt the renderer uses, so the formatting is not independently
// checked here (TestStoreSizeRenderExact pins that); what this pins is that the
// value read is the sink's, and that publishing changes it.
func TestStoreSizeTracksTheRealSink(t *testing.T) {
	sink := collector.NewMemSink(1 << 20)
	before := sink.Used()

	target := collector.Target{
		Namespace: "db",
		Cluster:   "pg",
		Pod:       "pg-1",
		Role:      "primary",
		Address:   "10.0.0.1:5432",
		Source:    collector.SourceSQL,
		PGMajor:   16,
	}
	samples := []collector.Sample{{
		Metric: "db_xact_commit_total",
		Labels: map[string]string{"datname": "postgres"},
		Value:  1234,
	}}
	if err := sink.Publish(target, "database", samples); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	after := sink.Used()
	if after <= before {
		t.Fatalf("sink.Used() went %d -> %d after Publish; the fixture stores nothing", before, after)
	}

	want := "# HELP pgsi_store_size_bytes Bytes held in the process's sample store (R13.5).\n" +
		"# TYPE pgsi_store_size_bytes gauge\n" +
		"pgsi_store_size_bytes " + strconv.FormatInt(after, 10) + "\n"
	if got := metrics.StoreSize(sink, ""); got != want {
		t.Errorf("rendered value is not sink.Used()\ngot:\n%s\nwant:\n%s", got, want)
	}
	// The same sink through the hub, which is the call path a server uses.
	withVersion(t, "9.9.9-test", "deadbee")
	if got, want := metrics.New(nil, nil, metrics.WithStoreSize(sink)).Render(), metrics.BuildInfo()+metrics.StoreSize(sink, ""); got != want {
		t.Errorf("hub with a real sink =\n%s\nwant\n%s", got, want)
	}
}

// TestHubRendersStoreFamily is the hub half: WithStoreSize must reach the
// exposition, and it must land between the up gauge and the HTTP histogram, which
// is where R9.8 lists store_size. The family's own bytes are pinned by
// TestStoreSizeRenderExact, so this compares against StoreSize's output rather
// than re-checking them.
func TestHubRendersStoreFamily(t *testing.T) {
	withVersion(t, "9.9.9-test", "deadbee")

	m := metrics.NewHTTPRequests()
	m.Observe("GET", "/api/v1/clusters", 200, 0.03)

	got := metrics.New(nil, nil, metrics.WithStoreSize(fakeSizer(4096)), metrics.WithHTTPRequests(m)).Render()
	want := metrics.BuildInfo() + metrics.StoreSize(fakeSizer(4096), "") + m.Render("")
	if got != want {
		t.Errorf("hub with the store option =\n%s\nwant\n%s", got, want)
	}
	// And with no store option at all, the family contributes nothing.
	if got := metrics.New(nil, nil, metrics.WithHTTPRequests(m)).Render(); got != metrics.BuildInfo()+m.Render("") {
		t.Errorf("hub without the store option =\n%s\nwant\n%s", got, metrics.BuildInfo()+m.Render(""))
	}
}
