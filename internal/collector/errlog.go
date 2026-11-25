// The rate limiter behind runOne's error log.
//
// R13.7 makes the scheduler's logging structured and level-configurable, and
// R2.2 makes every collector run increment a counter, but neither says anything
// about how much the scheduler should *say* when a collector fails, and the
// reference instance is the case that shows the gap. A collector on a 15 s
// interval that fails every round — which is what 482d82e fixed for eight of the
// nine SQL collectors — writes 240 identical lines an hour, and across the nine
// opener-backed collectors the intervals in §3.3 (15 s, 30 s, 60 s, 15 min) add
// up to roughly a thousand lines an hour of the same nine sentences. The counter
// is the aggregate; the log line is the detail; a detail repeated 240 times an
// hour drowns the lines that are news.
//
// So the gate keeps one entry per (collector, cluster, namespace) and logs on
// three conditions: the first time it sees that key at all, any time the error
// string changes (a new failure mode is news, and a window that suppressed it
// would hide a regression behind the error it replaced), and once per window for
// an identical repeat. The suppressed count rides along on the next line the gate
// writes, so rate-limiting loses nothing: "repeated 23" is the whole content of
// the 23 lines it did not write.
package collector

import (
	"sync"
	"time"
)

// DefaultErrorLogWindow is how long one error string is suppressed for a
// (collector, target) pair after being logged.
//
// It is a constant and not a Config field because R11.x names no flag for it and
// adding one is an R11.8 amendment rather than a scheduler decision; a Config
// field can be added later without touching a caller. Five minutes is two rounds
// of the 15 s collectors and one of the 60 s ones, which is the shortest span
// that still lets the slow collectors report every round they fail — the window
// rate-limits the fast ones without muting the slow ones, which is the asymmetry
// a single number has to serve.
const DefaultErrorLogWindow = 5 * time.Minute

// errLogKey is the gate's unit of memory: one entry per collector per target.
//
// It is (collector, cluster, namespace) and not (collector, pod) because that is
// Stats' own error-counter key — R2.2's
// pgsi_collector_errors_total{collector,cluster,namespace} — and a gate keyed
// differently from the counter it accompanies would let one pod's error silence
// another pod's. Keying on the pod instead would be the other mistake: two pods
// of one cluster fail independently (a primary and a replica do not have the
// same pg_stat_activity), so collapsing them to the cluster would suppress a
// real replica failure behind a healthy primary's silence. The pair below is
// exactly the counter's pair, which is what makes the log and the metric
// reconcilable — a reader who sees 42 in the counter and three lines in the log
// can account for the difference from the repeated attrs.
type errLogKey struct {
	collector string
	cluster   string
	namespace string
}

// errLogEntry is what the gate remembers about the last error it logged for a
// key.
type errLogEntry struct {
	// msg is the error string that was last logged. The comparison is on the
	// string and not on error identity because these errors come from nine
	// different collectors with no shared sentinel type — a pgx handshake failure,
	// an HTTP non-200, a sink rejection — and the message is the only thing they
	// all have. It is also the thing a reader compares, so two errors that render
	// identically are one error as far as the log is concerned.
	msg string
	// at is when that message was last logged, from the injected clock.
	at time.Time
	// repeats counts the identical errors suppressed since that log line. It is
	// reported on the next line the gate writes and reset there.
	repeats int
}

// errLogGate decides whether a collector error may be logged this round.
//
// Its zero value logs everything and records nothing, which is the pre-gate
// behaviour; newErrLogGate is the constructor every Scheduler uses.
type errLogGate struct {
	// window is how long an identical error is suppressed after being logged.
	window time.Duration
	// now is the scheduler's injected clock, passed in rather than called as
	// time.Now so a test can advance it without sleeping — the same reason
	// Config.Now exists (R13.1's "< 3 s per cluster" and R2.1's timeout are both
	// assertions about elapsed time, and a test that cannot control the clock can
	// only assert them by waiting).
	now func() time.Time

	mu      sync.Mutex
	entries map[errLogKey]errLogEntry
}

// newErrLogGate builds a gate with the default window over the given clock.
func newErrLogGate(now func() time.Time) *errLogGate {
	return newErrLogGateWindow(now, DefaultErrorLogWindow)
}

// newErrLogGateWindow is the constructor the tests use to exercise the window
// boundary without waiting five real minutes. newErrLogGate above is the
// production path and the only place the default is named, so a test that pins
// the default pins it through the production constructor.
func newErrLogGateWindow(now func() time.Time, window time.Duration) *errLogGate {
	if now == nil {
		now = time.Now
	}
	if window <= 0 {
		window = DefaultErrorLogWindow
	}
	return &errLogGate{window: window, now: now, entries: map[errLogKey]errLogEntry{}}
}

// allow reports whether an error with msg should be logged for the given
// (collector, cluster, namespace), and how many identical errors were suppressed
// since the last line written for that key.
//
// The four cases are the whole policy:
//
//   - unseen key: log, no repeat count.
//   - known key, different msg: log immediately and reset the count. A changed
//     error string is a different fact; carrying the old count onto it would
//     attribute the replaced error's occurrences to the new one.
//   - known key, same msg, window not elapsed: suppress and count it.
//   - known key, same msg, window elapsed: log, report the count, reset it.
func (g *errLogGate) allow(collector, cluster, namespace, msg string) (log bool, repeats int) {
	if g == nil {
		// A Scheduler built by a zero-value literal has no gate. Logging every
		// error is the pre-gate behaviour and the safe default for a construction
		// path that never ran NewScheduler, matching the rule Up() follows for a
		// nil tracker: degrade, do not panic.
		return true, 0
	}

	k := errLogKey{collector: collector, cluster: cluster, namespace: namespace}

	g.mu.Lock()
	defer g.mu.Unlock()

	prev, seen := g.entries[k]
	switch {
	case !seen, prev.msg != msg:
		g.entries[k] = errLogEntry{msg: msg, at: g.now()}
		return true, 0
	case g.now().Before(prev.at.Add(g.window)):
		prev.repeats++
		g.entries[k] = prev
		return false, prev.repeats
	default:
		g.entries[k] = errLogEntry{msg: msg, at: g.now()}
		return true, prev.repeats
	}
}

// logCollectorError writes R13.7's line for a failed collector round, if the
// gate allows one this round.
//
// It is a method rather than an inline slog call in runOne because the call site
// has to be the thing the test exercises: an inlined call lets a test assert the
// gate's verdict while production logs something else. runOne's error branch is
// now two lines — IncError, then this — and both take exactly the same
// (name, cluster, namespace) triple, which is the pairing that keeps R2.2's
// counter labels and R13.7's log attributes from drifting apart. A reader who
// sees 42 in pgsi_collector_errors_total and three lines in the log can account
// for the difference from the repeated attrs, and only because the two are keyed
// identically.
//
// The level is Error to match the sibling call at scheduler.go:652, which uses
// Error for a sink rejection; a round that produced no samples is the worse of
// the two failures, and the only real question is which line an operator running
// --log-level=error expects to see. err must be non-nil — the caller's
// `if err != nil` is the only call site and the only guard.
//
// The sink-rejection line at scheduler.go:652 stays un-gated on purpose. It is
// deliberately outside pgsi_collector_errors_total (the comment there explains
// why), so gating it would extend the gate's key space to a pair the counter does
// not have and would make the reconciliation above ambiguous. Widening the gate
// to cover that line is a follow-up, not part of this change.
func (s *Scheduler) logCollectorError(name string, tgt Target, err error) {
	ok, repeats := s.errLog.allow(name, tgt.Cluster, tgt.Namespace, err.Error())
	if !ok {
		return
	}
	attrs := []any{
		"collector", name,
		"cluster", tgt.Cluster,
		"namespace", tgt.Namespace,
		"error", err.Error(),
	}
	if repeats > 0 {
		attrs = append(attrs, "repeated", repeats)
	}
	s.log.Error("collector round failed", attrs...)
}
