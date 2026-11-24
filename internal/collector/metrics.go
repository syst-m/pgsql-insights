package collector

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultPrefix is R9.2's default metric prefix.
//
// What the prefix is for, since it is a parameter and not a literal: R9.2 makes
// the prefix *configurable* (`metrics.prefix`) and names `pgsi_` as its default
// value, so a family name is prefix + the rest. The two families below are
// therefore `pgsi_` + `collector_duration_seconds` and `pgsi_` +
// `collector_errors_total`, which is exactly how R2.2:292 and R9.8:400 spell
// them. A non-default prefix renames them, and that is R9.2's intent rather than
// an accident — which is why the value lives here as one constant the renderers
// read, so the default is spelled in one place and an override changes both
// families together.
const DefaultPrefix = "pgsi_"

// durationBuckets are the histogram boundaries for the duration family.
//
// Spec gap, flagged rather than resolved silently: R2.2 names the family
// `pgsi_collector_duration_seconds` and gives it one label, but does not name a
// type or list buckets, and R9.8 lists the family without histogram suffixes.
// `promtool check metrics` — which Phase 2's acceptance criteria run — rejects a
// `# TYPE histogram` whose samples lack `_bucket`/`_sum`/`_count`, so the
// exposition form is forced even though the requirement's prose does not spell
// it out. The boundaries below are placed around the two numbers the spec does
// commit to: R13.1's "< 3 s per cluster per 60 s" (so 2.5 s and 5 s straddle it)
// and R2.1's 10 s per-collector timeout (the top finite bucket, so a round that
// hits the timeout lands in a named bucket instead of only in +Inf).
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Stats is the process's self-observability state: the two R2.2 families, plus
// the last-success record that makes R2.2's "last-good data" answerable.
//
// It is a type and not a prometheus registry because the project has no
// client_golang dependency (§3.4's minimal-dependency stance) and internal/api
// already hand-writes the exposition format — api.BuildInfoMetric in
// internal/api/server.go is the precedent, and its own comment names this
// phase's hub as the thing that will render it. A2's shape is the contract here:
// family, returning a string, carrying its own `# HELP` and `# TYPE` lines, so
// the hub is a strings.Builder over N calls with no per-family special-casing.
// RenderDuration/RenderErrors follow it; Render concatenates them for callers
// that want the whole package's output in one call.
//
// The two families have different label *sets* — R2.2 gives the duration family
// `{collector}` alone and the errors family `{collector,cluster,namespace}` — and
// one struct reused across both would give the duration family a cluster and a
// namespace it must not have. Prometheus accepts the extra labels silently: no
// scrape error, no test failure, just one series per collector per cluster where
// the spec wants one per collector. So each renderer builds its own set, and
// labelString is the shared part: the escaping and brace form, not the contents.
//
// Every method on *Stats is nil-receiver safe, the way UpTracker's are: a nil
// *Stats behaves exactly like a Stats built by NewStatsWithPrefix("") that has
// observed nothing — Prefix reports DefaultPrefix, Age reports "never", the
// three renderers return "", and the three recorders are no-ops. The API's
// Config holds a *Stats that a caller with no collector leaves nil, and a
// handler must be able to render the exposition without a nil check at every
// call site. The zero value &Stats{} is NOT the same thing (its prefix is empty
// and its maps are nil, so it panics on the first ObserveDuration), which is why
// NewStats exists; the nil receiver is the third case, and it now has a defined
// answer instead of a panic.
type Stats struct {
	mu     sync.Mutex
	prefix string

	// durations is keyed by collector name alone (R2.2's label set), so a
	// 50-cluster fleet contributes 13 series total and not 650.
	durations map[string]*histogram
	// errors is keyed by the exact triple R2.2 names.
	errors map[errorKey]uint64
	// lastSuccess records the last successful round per (collector, cluster,
	// namespace) — the same triple the errors family uses, so "this collector
	// last succeeded 4 m ago on this cluster" is answerable for the exact
	// series a user is looking at.
	lastSuccess map[errorKey]time.Time
}

// errorKey is R2.2's label triple as a map key. A struct key rather than a
// concatenated string so the fields stay the ones the spec names, and a value
// containing a separator character cannot collide with a different triple.
type errorKey struct {
	Collector string
	Cluster   string
	Namespace string
}

// NewStats returns Stats with the default prefix.
func NewStats() *Stats { return NewStatsWithPrefix(DefaultPrefix) }

// NewStatsWithPrefix returns Stats rendering with the given R9.2 prefix. An
// empty prefix selects DefaultPrefix so a zero-value Config cannot silently
// export `collector_duration_seconds`.
func NewStatsWithPrefix(prefix string) *Stats {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return &Stats{
		prefix:      prefix,
		durations:   map[string]*histogram{},
		errors:      map[errorKey]uint64{},
		lastSuccess: map[errorKey]time.Time{},
	}
}

// Prefix reports the R9.2 prefix in use, so the API can render the family names
// it advertises without re-deriving them.
func (s *Stats) Prefix() string {
	if s == nil {
		return DefaultPrefix
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefix
}

// ObserveDuration records one collection round's wall duration. The scheduler
// calls this for every round, successful or not: R2.2's duration family is about
// how long collectors take, and a round that timed out at 10 s is the most
// interesting value the family can receive. Excluding failures would make the
// histogram lie in exactly the case a dashboard is opened to look at.
func (s *Stats) ObserveDuration(collector string, seconds float64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.durations[collector]
	if h == nil {
		h = newHistogram()
		s.durations[collector] = h
	}
	h.observe(seconds)
}

// IncError records one failed round against R2.2's error counter. The label
// triple is R2.2's, and the scheduler is the only caller — a collector that
// counts its own internal errors and also returns one would double-count the
// same failure, which is the R2.2 bug this shape prevents.
func (s *Stats) IncError(collector, cluster, namespace string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors[errorKey{collector, cluster, namespace}]++
}

// MarkSuccess records that a round succeeded, which is what R2.2's
// "last-good data" needs in order to be reportable: the samples themselves are
// in the sink, but "how old is the data this cluster is being served" is a
// property of the collector, and it is this map that answers it.
func (s *Stats) MarkSuccess(collector, cluster, namespace string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSuccess[errorKey{collector, cluster, namespace}] = time.Now()
}

// Age returns how long ago the named (collector, cluster, namespace) last
// succeeded, and false if it never has. The false case matters as much as the
// number: "never run" and "ran 3 s ago" are different states and a zero duration
// cannot tell them apart.
//
// This is served through the API rather than exported as a third metric family.
// R9.8's list of self-observability families is closed and R14.7 forbids adding
// a metric that isn't in the docs, so `pgsi_collector_last_success_timestamp`
// — the conventional answer — is not available to add here even though it is
// what every Prometheus exporter has. Flagged for A3 as the second place R2.2's
// prose implies a metric it does not name.
func (s *Stats) Age(collector, cluster, namespace string) (time.Duration, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.lastSuccess[errorKey{collector, cluster, namespace}]
	if !ok {
		return 0, false
	}
	return time.Since(t), true
}

// Render returns the package's full R2.2 exposition: both families, each with
// its own HELP and TYPE lines. This is the call the Phase 2 MetricHub makes
// alongside api.BuildInfoMetric() in internal/api/server.go, whose
// comment is what scheduled the hub in the first place.
func (s *Stats) Render() string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(s.RenderDuration())
	b.WriteString(s.RenderErrors())
	return b.String()
}

// RenderDuration renders pgsi_collector_duration_seconds (R2.2) — one series
// per collector, `{collector}` and nothing else.
//
// One HELP/TYPE pair for the family, with every collector's lines underneath it,
// which is what client_golang's handler emits and what promtool wants: a HELP
// line repeated per collector parses as a duplicate and promtool check metrics
// reports it ("Duplicate HELP and TYPE lines"). The earlier shape wrote the pair
// inside the loop, so thirteen collectors produced thirteen identical pairs.
func (s *Stats) RenderDuration() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	names := sortedKeys(s.durations)
	if len(names) == 0 {
		return ""
	}
	name := s.prefix + "collector_duration_seconds"

	var b strings.Builder
	fmt.Fprintf(&b, "# HELP %s Wall duration of one collection round (R2.2).\n", name)
	fmt.Fprintf(&b, "# TYPE %s histogram\n", name)
	for _, collector := range names {
		b.WriteString(s.durations[collector].render(name, map[string]string{"collector": collector}))
	}
	return b.String()
}

// RenderErrors renders pgsi_collector_errors_total (R2.2) — `{collector,
// cluster, namespace}`, the wider of the two label sets. One HELP/TYPE pair for
// the family, then one line per triple, so a fleet with 13 collectors over 50
// clusters renders two header lines and 650 samples rather than 1300 headers.
func (s *Stats) RenderErrors() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]errorKey, 0, len(s.errors))
	for k := range s.errors {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Collector != keys[j].Collector {
			return keys[i].Collector < keys[j].Collector
		}
		if keys[i].Namespace != keys[j].Namespace {
			return keys[i].Namespace < keys[j].Namespace
		}
		return keys[i].Cluster < keys[j].Cluster
	})
	if len(keys) == 0 {
		return ""
	}

	name := s.prefix + "collector_errors_total"
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP %s Failed collection rounds (R2.2).\n", name)
	fmt.Fprintf(&b, "# TYPE %s counter\n", name)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s%s %s\n", name,
			labelString(map[string]string{
				"collector": k.Collector, "cluster": k.Cluster, "namespace": k.Namespace,
			}),
			formatFloat(float64(s.errors[k])))
	}
	return b.String()
}

// histogram is one collector's duration distribution: per-bucket counts plus the
// sum and count Prometheus requires for a histogram sample set.
type histogram struct {
	counts []uint64 // per durationBuckets boundary, not cumulative
	sum    float64
	n      uint64
}

func newHistogram() *histogram {
	return &histogram{counts: make([]uint64, len(durationBuckets))}
}

func (h *histogram) observe(v float64) {
	h.sum += v
	h.n++
	for i, b := range durationBuckets {
		if v <= b {
			h.counts[i]++
			return
		}
	}
}

// render emits the `_bucket`/`_sum`/`_count` lines for one histogram. `labels`
// is the series' label set apart from `le` — for the duration family that is
// `{collector="wal"}`, and it has to be here: R2.2 gives the family one label,
// and a render that emitted only `{le=...}` would produce one identical set of
// sample lines per collector, which a Prometheus scrape rejects as duplicate
// metrics the moment a second collector exists.
//
// The bucket counts are stored per-boundary and emitted cumulative, because
// Prometheus's histogram contract is cumulative ("le" is inclusive and each
// bucket contains every observation in the buckets below it). Storing them
// cumulative instead would make observe O(n) on every write to save a sum at read
// time; the read happens once per scrape and the write once per round, so the
// work belongs here.
func (h *histogram) render(name string, labels map[string]string) string {
	var b strings.Builder
	var cum uint64
	for i, boundary := range durationBuckets {
		cum += h.counts[i]
		fmt.Fprintf(&b, "%s_bucket%s %s\n", name,
			labelString(withLabel(labels, "le", formatFloat(boundary))),
			strconv.FormatUint(cum, 10))
	}
	fmt.Fprintf(&b, "%s_bucket%s %s\n", name,
		labelString(withLabel(labels, "le", "+Inf")), strconv.FormatUint(h.n, 10))
	fmt.Fprintf(&b, "%s_sum%s %s\n", name, labelString(labels), formatFloat(h.sum))
	fmt.Fprintf(&b, "%s_count%s %s\n", name, labelString(labels), strconv.FormatUint(h.n, 10))
	return b.String()
}

// withLabel returns a copy of labels plus one extra entry. The copy is required,
// not stylistic: the caller's map is the series' label set and is reused for
// every line the render writes, so mutating it to add `le` would leave `le` in
// the set the next line renders.
func withLabel(labels map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for lk, lv := range labels {
		out[lk] = lv
	}
	out[k] = v
	return out
}

// labelString renders a label set as the exposition format wants it:
// `{a="1",b="2"}`, names sorted, values quoted with %q, and an empty string for
// an empty set (a sample with no labels has no braces at all, not `{}`).
//
// Sorting is the format's own convention — client_golang's expfmt sorts label
// names, and a deterministic render is what makes a golden file possible at all.
// It is also the repo's single escaping convention: api.BuildInfoMetric inlines
// the same %q form, and Go's %q and Prometheus's escaping are identical for the
// ASCII set R9.3's values come from (DNS labels and collector names).
func labelString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := sortedKeys(labels)
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%s=%q", k, labels[k])
	}
	b.WriteString("}")
	return b.String()
}

// formatFloat renders a float64 the way the exposition format wants it: shortest
// representation that round-trips, so 5 is "5" and not "5.000000", and 0.1 is
// "0.1". %v on a float64 gives the same thing; naming it in one function is what
// stops a future caller reaching for %f and producing "3.000000" for a count.
func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// sortedKeys returns a map's string keys in order. Prometheus does not require
// sorted output, but a deterministic render is what makes a golden test possible
// and a diff reviewable, and Go's map iteration order is randomised per run.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
