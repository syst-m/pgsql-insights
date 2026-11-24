package collector

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// QueueDepthMetricBase and ShedMetricBase are R2.9's two families without the
// R9.2 prefix, stored unprefixed for the same reason UpMetricBase is: R9.2 makes
// the prefix configurable through `metrics.prefix`, so every renderer
// concatenates prefix+base and a cluster renamed away from `pgsi_` still gets
// the families its dashboards query.
//
// ShedMetricBase carries the `_total` suffix in the base because the family is a
// counter and `promtool check metrics` — which Phase 2's acceptance runs —
// rejects a counter whose name lacks it. That is the same reason HTTPMetricBase
// carries `_seconds` rather than having the renderer append the unit.
const (
	QueueDepthMetricBase = "scheduler_queue_depth"
	ShedMetricBase       = "scheduler_shed_total"
)

// SchedulerMetrics is R2.9's observable half: the queue collections are admitted
// into, and the counter that records what the scheduler refused to run.
//
// It is its own type for the reason UpTracker is. Stats' two families are keyed
// (collector) and (collector, cluster, namespace); R2.9's two are keyed
// (nothing) and (cluster). Putting them on Stats would mean either widening two
// existing families' label sets or adding a third key shape to a struct whose
// whole content is those two shapes — which is the argument up.go:24-36 makes
// against folding pgsi_up in, and it applies identically here. The scheduler
// package's rule, stated at metrics.go:44 and repeated at metrics.go:41-47, is
// one exported renderer per family with its own HELP and TYPE lines; this type
// follows it for two families in one file, exactly as Stats does for its pair.
//
// The queue is a channel and the gauge is its length. That is not a shortcut —
// it is the only honest observable this architecture has. runTarget spawns one
// goroutine per (target, collector) pair, so a collection waiting for a global
// slot *is* a goroutine blocked on a channel; there is no list of pending work
// items to take a length from. A collection holds its queue token from admission
// until its run finishes, so the gauge reads queued-plus-running and is bounded
// by Config.SchedulerQueueDepth, which is what makes the flag observable rather
// than decorative: a reader can see the queue at 512 and know the cap is what
// filled it, and every sample above the cap is a shed, counted.
//
// Every method is nil-receiver safe the way Stats's and UpTracker's are, because
// a Scheduler not built by NewScheduler has no metrics and a caller outside the
// package must be able to render without a nil check at the call site.
type SchedulerMetrics struct {
	mu     sync.Mutex
	prefix string

	// queue is R2.9's bounded waiting room: cap Config.SchedulerQueueDepth, and
	// len(queue) is the exported gauge. It is a channel rather than a counter
	// because the cap has to be *enforced*, and a counter that counts past its
	// cap can only report the overshoot after the fact.
	queue chan struct{}

	// sheds is pgsi_scheduler_shed_total: cluster -> count. Keyed by cluster
	// alone because that is the one label R2.9 names. The consequence is worth
	// stating rather than leaving to be discovered: cluster is not unique across
	// namespaces, so two namespaces running a cluster named `pg` merge into one
	// series and their shed counts add. Widening the label set is R9.12's move
	// to make (it did exactly that to R9.8's pgsi_scrape_target_up{cluster,pod}),
	// and until it does, merging is what the spec's label set requires.
	sheds map[string]uint64
}

// NewSchedulerMetrics returns the metrics for a scheduler whose queue holds at
// most depth collections. depth <= 0 selects DefaultSchedulerQueueDepth, so a
// Config that never set the field still gets the documented 512.
func NewSchedulerMetrics(depth int) *SchedulerMetrics {
	if depth <= 0 {
		depth = DefaultSchedulerQueueDepth
	}
	return &SchedulerMetrics{
		prefix: DefaultPrefix,
		queue:  make(chan struct{}, depth),
		sheds:  map[string]uint64{},
	}
}

// Admit takes one queue slot for one collection, without blocking.
//
// Non-blocking is the requirement: R2.9's shed policy exists for the case where
// the queue is full, and a blocking send would wait for a token instead of
// shedding. A caller that gets ok=false has a shed on its hands and must report
// it (ObserveShed) rather than dropping it silently — "mark the shed cluster
// stale rather than dropping the sample" is a sentence about visibility, and the
// counter plus TargetReport.Shed are the two places it becomes visible.
//
// The returned release func is safe to call twice for the reason acquire's is:
// two releases for one take remove two tokens from the channel, and a queue that
// loses tokens every round reports a depth below the number of collections
// waiting in it.
func (m *SchedulerMetrics) Admit() (func(), bool) {
	if m == nil || m.queue == nil {
		return func() {}, true
	}
	select {
	case m.queue <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-m.queue }) }, true
	default:
		return func() {}, false
	}
}

// ObserveShed counts one refused collection against tgt's cluster.
func (m *SchedulerMetrics) ObserveShed(tgt Target) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sheds == nil {
		m.sheds = map[string]uint64{}
	}
	m.sheds[tgt.Cluster]++
}

// Depth reports how many collections are admitted and not finished — the value
// pgsi_scheduler_queue_depth exports, readable from a test without rendering.
func (m *SchedulerMetrics) Depth() int {
	if m == nil {
		return 0
	}
	return len(m.queue)
}

// Shed reports the exported count for one cluster, 0 if it has never been shed.
func (m *SchedulerMetrics) Shed(cluster string) uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sheds[cluster]
}

// Render returns both R2.9 families, each with its own HELP and TYPE lines, in
// the shape metrics.go:41-47 names as this project's contract for a family. An
// empty prefix selects DefaultPrefix.
//
// Numbers go through strconv, not the package's itoa. That helper is documented
// at up.go:210-212 as "the integer formatter for the 1/0 this family exports" —
// it returns "1" for every non-zero int, which is correct for pgsi_up and wrong
// for a queue depth of 2 or a shed count of 512. metrics.go:425's FormatFloat is
// the float half of the same split; for an integer counter family the package's
// existing form is strconv.FormatUint (metrics.go:359), which is what the shed
// series uses here, with strconv.Itoa for the int-typed gauge.
//
// Both families render absent when there is nothing to say — the queue gauge is
// rendered from a queue that exists (a SchedulerMetrics always has one, so the
// gauge is present from the first round rather than appearing when the first
// collection starts), and the shed counter renders nothing until something has
// been shed, because a counter with no series is absent and not a zero-valued
// series for a cluster that has never been refused.
func (m *SchedulerMetrics) Render(prefix string) string {
	if m == nil {
		return ""
	}
	if prefix == "" {
		prefix = DefaultPrefix
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder
	// pgsi_scheduler_queue_depth: one series, no labels, which is why it needs no
	// labelString call — R2.9 gives the family a label set of nothing at all, so
	// a fleet with 50 clusters still has one number here, not 50.
	name := prefix + QueueDepthMetricBase
	b.WriteString("# HELP " + name + " Collections admitted to the scheduler queue and not yet finished (R2.9).\n")
	b.WriteString("# TYPE " + name + " gauge\n")
	b.WriteString(name + " " + strconv.Itoa(len(m.queue)) + "\n")

	if len(m.sheds) > 0 {
		clusters := make([]string, 0, len(m.sheds))
		for c := range m.sheds {
			clusters = append(clusters, c)
		}
		sort.Strings(clusters)
		name := prefix + ShedMetricBase
		b.WriteString("# HELP " + name + " Collections not run because the scheduler queue was full (R2.9).\n")
		b.WriteString("# TYPE " + name + " counter\n")
		for _, c := range clusters {
			b.WriteString(name + labelString(map[string]string{"cluster": c}) + " " + strconv.FormatUint(m.sheds[c], 10) + "\n")
		}
	}
	return b.String()
}
