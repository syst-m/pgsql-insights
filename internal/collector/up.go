package collector

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// UpMetricBase is R9.12's family name without the R9.2 prefix.
//
// R9.12 names the metric `pgsi_up`, and the chart writes exactly that
// (chart/templates/prometheusrule.yaml:77 `min by (namespace, cluster) (pgsi_up) == 0`,
// :96 `pgsi_up{instance_role="primary"} == 0`, :159 the replica twin). The base
// is stored unprefixed for the same reason every other family is: R9.2 makes the
// prefix configurable via `metrics.prefix`, so the renderer concatenates
// prefix+base and a cluster renamed away from `pgsi_` still gets a working
// PGSIPrimaryDown.
const UpMetricBase = "up"

// UpTracker is the R9.12 surface: one series per discovered instance, 1 when our
// last collection round for that instance succeeded and 0 otherwise.
//
// It is a separate type rather than a field on Stats for a reason the two
// existing families make visible. Stats' error family is keyed
// (collector, cluster, namespace) and its duration family is keyed
// (collector) — neither has a `pod`, and adding one to Stats means adding it to
// both, which changes what those two metrics mean. R9.12's label set is
// (namespace, cluster, pod, instance_role) with no collector: it answers "is
// this instance healthy", not "did this collector work", and one instance's
// thirteen collectors running means thirteen successes for one instance. Keying
// it off Stats would make a cluster with nine working collectors and four
// broken ones answer the question four different ways depending on which
// collector was asked.
//
// R9.8's `pgsi_scrape_target_up{cluster,pod}` is a third shape — 0.1.0's
// per-scrape-target metric, which R9.12's text names as what 0.1.0 had "only" —
// and it is not this one. It measures whether *Prometheus* could scrape the
// exporter; `pgsi_up` measures whether *we* could collect from Postgres. An
// exporter that answers /metrics instantly while every Postgres connection times
// out has scrape_target_up 1 and up 0, and that distinction is the whole content
// of R9.12.
type UpTracker struct {
	mu     sync.RWMutex
	states map[string]*upState // keyed by Target.Key()
}

// upState is one instance's row in the tracker.
type upState struct {
	// target is kept so Render can rebuild the label set without a second map
	// from Key() back to the fields, which would be a second place the four
	// R9.3 labels get assembled.
	target Target
	// up is the exported value: true renders 1, false renders 0.
	up bool
	// lastSuccess is the instant of the most recent successful round, and is
	// zero for an instance we have discovered but never collected from. It is
	// not exported — R9.12 defines one metric, and R14.7 does not allow a second
	// one on the strength of this struct's fields — but it is what makes
	// IsUp answer "have we ever" separately from "did the last one work".
	lastSuccess time.Time
}

// NewUpTracker returns an empty tracker: no known instances, so no series.
func NewUpTracker() *UpTracker {
	return &UpTracker{states: map[string]*upState{}}
}

// Track registers targets as known instances without changing their up value.
//
// This is the half of R9.12 that a naive implementation gets wrong. "One series
// per discovered instance" means the series set comes from discovery, not from
// collection: an instance that has been discovered but has not yet completed a
// round must already export `pgsi_up ... 0`, because "0 otherwise" covers it. If
// the series set came only from Observe, a brand-new pod would export nothing at
// all, and PGSIPrimaryDown — which is `pgsi_up{instance_role="primary"} == 0` —
// cannot fire on a series that does not exist. The same reasoning is R8.7's: a
// pod that answers before it has data must not look healthy, and here it must not
// look absent either.
//
// Calling Track with a shrunk fleet forgets the instances that went away, so a
// terminated pod stops exporting a series rather than exporting a stale 1 forever.
func (u *UpTracker) Track(targets []Target) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	// Rebuild rather than upsert. The upsert this replaces added the new targets
	// and left every old one in place, which meant a pod that scaled away kept
	// exporting its last value forever: a terminated primary sits on `1` until
	// the process restarts, and PGSIPrimaryDown — which is the alert for exactly
	// that pod — never fires because the series it watches never changes.
	// Rebuilding makes the target list the authority on the fleet, which is what
	// "one series per discovered instance" says it is.
	next := make(map[string]*upState, len(targets))
	for _, t := range targets {
		if !t.Valid() {
			// Target.Valid is the same gate the scheduler uses to drop a target
			// it cannot attribute; exporting a series for it would put an
			// unattributable value on the wire.
			continue
		}
		// Carry the existing row forward so re-registering a known instance does
		// not reset an observation it already has. Without this, Track on every
		// round would flip every healthy instance to 0 for the duration of that
		// round and the alert would fire once per interval on a healthy cluster.
		if st, ok := u.states[t.Key()]; ok {
			next[t.Key()] = st
			continue
		}
		next[t.Key()] = &upState{target: t}
	}
	u.states = next
}

// Observe records one instance's round outcome, and is what the scheduler calls
// after each collector run.
//
// An instance is up when its last round succeeded and down when it failed, with
// the failure of *any* collector on the instance counting as the instance being
// down. That is R9.12's wording — "our last collection round for that instance
// succeeded" — and it is deliberately harsher than a per-collector view: an
// instance whose replication collector cannot run is a broken instance even when
// its database collector answered fine, and a metric that said otherwise would
// make PGSIPrimaryDown fire on a primary that is actually serving reads.
//
// An unknown target is added rather than ignored, so a scheduler that observes a
// target it never called Track with still produces its series. Track is the
// discovery-driven path; Observe is the collection-driven one, and the two agree
// because both key on Target.Key().
func (u *UpTracker) Observe(t Target, err error, at time.Time) {
	if u == nil || !t.Valid() {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.states == nil {
		u.states = map[string]*upState{}
	}
	st, ok := u.states[t.Key()]
	if !ok {
		st = &upState{target: t}
		u.states[t.Key()] = st
	}
	st.up = err == nil
	if err == nil {
		st.lastSuccess = at
	}
}

// IsUp reports the exported value for one instance and whether it is known at
// all. The two results are separate because "down" and "never heard of it" are
// different facts: an alert on the first is an outage, an alert on the second is
// a discovery bug.
func (u *UpTracker) IsUp(t Target) (up bool, known bool) {
	if u == nil {
		return false, false
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	st, ok := u.states[t.Key()]
	if !ok {
		return false, false
	}
	return st.up, true
}

// Render returns the exposition-format body for the family: one HELP, one TYPE,
// and one line per known instance, sorted so the output is byte-stable.
//
// prefix is R9.2's `metrics.prefix` (DefaultPrefix when the caller has no
// config), concatenated with UpMetricBase the way Stats.Render concatenates it
// with its own two families. Sorting by the rendered line rather than by the
// target key is deliberate: it makes the output stable under any map iteration
// order without the caller having to know how Key() is composed.
func (u *UpTracker) Render(prefix string) string {
	if u == nil {
		// No tracker means no known instances, and a family with no series has no
		// lines to emit. The nil case is reachable because Scheduler's zero value
		// has a nil *UpTracker and Up() returns it rather than building one.
		return ""
	}
	if prefix == "" {
		prefix = DefaultPrefix
	}
	u.mu.RLock()
	lines := make([]string, 0, len(u.states))
	for _, st := range u.states {
		value := 0
		if st.up {
			value = 1
		}
		lines = append(lines, prefix+UpMetricBase+labelString(IdentityLabels(st.target))+" "+itoa(value))
	}
	u.mu.RUnlock()
	sort.Strings(lines)

	var b strings.Builder
	b.WriteString("# HELP " + prefix + UpMetricBase + " Whether our last collection round for this instance succeeded.\n")
	b.WriteString("# TYPE " + prefix + UpMetricBase + " gauge\n")
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// itoa is the integer formatter for the 1/0 this family exports. metrics.go's
// formatFloat is the float one and using it here would render `1` as `1`, so
// this is not a duplicate of it so much as the half of it this family needs.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	return "1"
}
