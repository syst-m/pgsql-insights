package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// minTick and maxTick bound how long Run sleeps between rounds.
//
// The floor stops a hot loop: with several collectors sharing a due timestamp, a
// tick with no work would otherwise spin. The ceiling is DefaultInterval (60 s),
// which is shorter than some collectors' intervals — settings runs every 15
// minutes — and the reason for clamping anyway is that Run re-reads the target
// list on every tick. The fleet is dynamic (R8.2's inventory changes as pods
// scale), so sleeping for the full 15 minutes to the next settings run would let
// a cluster that appeared two minutes later go uncollected for thirteen. The
// clamp costs a few extra wakeups and keeps the fleet view within a minute of
// reality.
const (
	minTick = 250 * time.Millisecond
	maxTick = DefaultInterval
)

// Config configures a Scheduler.
type Config struct {
	// PerClusterConcurrency is R2.1's per-cluster cap (default 4). It is
	// per-cluster and not per-poller precisely because of R2.9's arithmetic: 50
	// clusters x 13 collectors with one global cap of 4 would serialise a whole
	// fleet through four slots, and one slow cluster would delay every other one
	// — the failure R13.2 names and R2.1's per-cluster scoping prevents.
	PerClusterConcurrency int
	// DefaultTimeout is the per-collector timeout applied to a Descriptor whose
	// own Timeout is zero (R2.1's default 10 s). A Descriptor that sets a
	// Timeout wins; this field exists so an operator can lower the floor globally
	// without editing thirteen collectors.
	DefaultTimeout time.Duration
	// Now is the clock the scheduler measures with. Nil selects time.Now.
	// It is injectable because R13.1's "< 3 s per cluster" and R2.1's timeout are
	// both assertions about elapsed time, and a test that cannot control the clock
	// can only assert them by sleeping — which makes the test slow and the
	// assertion flaky.
	Now func() time.Time
}

// withDefaults returns c with every unset field replaced by its documented
// default, in one place, so no method has to guess what a zero means.
func (c Config) withDefaults() Config {
	if c.PerClusterConcurrency <= 0 {
		c.PerClusterConcurrency = DefaultPerClusterConcurrency
	}
	if c.DefaultTimeout <= 0 {
		c.DefaultTimeout = DefaultTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Scheduler runs collectors independently (R2.1): one goroutine per target, a
// bounded number of concurrent collectors per cluster, a per-collector timeout on
// every round, and one error counter and one duration family for all of it
// (R2.2).
//
// The per-cluster cap is implemented as a buffered channel per cluster rather
// than a global semaphore because R2.1's cap is scoped to a cluster: two
// instances of one cluster share four slots, and a cluster with 13 collectors
// therefore runs four of them at a time while a second cluster runs its own four
// in parallel, uncorrelated.
type Scheduler struct {
	cfg   Config
	reg   *Registry
	log   *slog.Logger
	stats *Stats
	sink  Sink

	// mu guards clusters only. It is not the scheduler's main lock: the per-round
	// state lives in the callers' pre-allocated slices (one writer per index), so
	// a round does not serialise on a mutex while its goroutines run.
	mu       sync.Mutex
	clusters map[string]chan struct{}
}

// NewScheduler builds a scheduler over reg. A nil sink selects NewMemSink(0) and
// a nil stats selects NewStats(); a nil log discards, matching api.Config's rule
// that an unwired logger discards rather than panics. reg must come from
// NewRegistry, which is where every validation happens — NewScheduler returns no
// error because there is nothing left to reject once the registry exists.
func NewScheduler(cfg Config, reg *Registry, log *slog.Logger, sink Sink, stats *Stats) *Scheduler {
	cfg = cfg.withDefaults()
	if log == nil {
		log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if sink == nil {
		sink = NewMemSink(0)
	}
	if stats == nil {
		stats = NewStats()
	}
	return &Scheduler{
		cfg:      cfg,
		reg:      reg,
		log:      log,
		stats:    stats,
		sink:     sink,
		clusters: map[string]chan struct{}{},
	}
}

// targetSet is one target paired with the entries that may run against it.
type targetSet struct {
	tgt     Target
	entries []Entry
}

// RoundReport is one round's aggregate, and it is what makes R13.1's per-cluster
// budget measurable.
//
// R13.1 states "< 3 s and < 40 SQL round-trips on a healthy instance; measured and
// reported via pgsi_collector_duration_seconds". The metric carries durations; the
// round-trip half needs a number the framework can only get from the collectors,
// because the scheduler cannot count queries issued inside a collector's own
// transaction — so Result.RoundTrips exists, the scheduler sums it per cluster,
// and this struct is where the sum is readable. Without it, R13.1 is a sentence in
// a spec with no way to falsify it.
type RoundReport struct {
	// At is when the round began (Config.Now).
	At time.Time
	// Requested is how many targets were asked for, before filtering.
	Requested int
	// Targets is how many targets actually ran. Requested - Targets is the number
	// dropped as invalid or inapplicable, and it is reported rather than silently
	// dropped for the same reason R16.8's `raw.clusters ?? []` was a bug: a
	// filtered-away set that leaves no trace looks identical to a set that was
	// never there.
	Targets int
	// Skipped is Requested - Targets.
	Skipped int
	// Rounds counts (target, collector) runs that actually started. It is not
	// Targets x Collectors, because a round cut short by shutdown runs fewer than
	// it scheduled, and a report that inferred the product would overstate the work.
	Rounds int
	// Errors is the collector failures counted in pgsi_collector_errors_total.
	Errors int
	// SinkErrors is publishes the store rejected. They are separate because a sink
	// failure is not a collector failure, and folding them together sends the reader
	// to the wrong component.
	SinkErrors int
	// RoundTrips is the summed SQL round-trips across the round: R13.1's number.
	RoundTrips int
	// MaxTargetDuration is the slowest single target's round: R13.1's "< 3 s" is
	// asserted against this, per cluster, not against an average that a fast
	// cluster can hide a slow one inside.
	MaxTargetDuration time.Duration
	// MaxSkew is the largest clock skew the round observed (R13.11).
	MaxSkew time.Duration
	// PerTarget keeps the per-cluster detail so a fleet-level average never has to
	// be the only number available.
	PerTarget []TargetReport
}

// TargetReport is one target's half of a round.
type TargetReport struct {
	Target   Target
	Duration time.Duration
	// Collectors is how many entries were scheduled for this target; Rounds is how
	// many ran. They differ when the context was cancelled mid-target, and the
	// distinction is what stops a shutdown round reporting 13 when 4 ran.
	Collectors int
	Rounds     int
	Errors     int
	SinkErrors int
	// RoundTrips is R13.1's per-cluster count, summed over this target's
	// collectors.
	RoundTrips int
	// MaxCollectorDuration is the slowest single collector round on this target —
	// the number that shows whether a 2.9 s cluster round is one 2.8 s collector or
	// thirteen 200 ms ones.
	MaxCollectorDuration time.Duration
	MaxSkew              time.Duration
}

// RunOnce runs every enabled, applicable collector against every valid target
// exactly once and returns the round report.
//
// This is the primitive Run is built from, and it is also the thing R13.1's budget
// is asserted from in the tests: a single round, measured, with no interval
// bookkeeping in the way.
func (s *Scheduler) RunOnce(ctx context.Context, targets []Target) RoundReport {
	set := buildSet(targets, s.reg.Enabled())
	reports := s.runSet(ctx, set)
	return s.aggregate(len(targets), reports)
}

// Run is the long-running loop: it re-reads the target list, runs whichever
// collectors are due, and returns when ctx is cancelled.
//
// nextRun is keyed by collector name and not by (collector, target). §3.3 gives
// the interval to the *collector*, so `database` runs every 60 s against every
// cluster on the same tick; one map entry per collector is the model the spec
// describes, and keying per pair would let one cluster's slow round shift the
// collector's own interval while doing nothing about the cluster that was slow.
//
// Run returns nil on cancellation. A nil targets func is an error rather than a
// silent forever-sleep, because a caller that forgot to pass one would otherwise
// start a goroutine that does nothing and looks healthy.
func (s *Scheduler) Run(ctx context.Context, targets func() []Target) error {
	if targets == nil {
		return errors.New("collector: Scheduler.Run requires a targets func (nil would start a scheduler that runs nothing and looks healthy)")
	}
	next := map[string]time.Time{}
	for {
		// A clean cancellation is not an error, which is why this is a non-blocking
		// select and not `if err := ctx.Err(); err != nil { return nil }`: nilerr
		// flags the latter because swallowing a non-nil error is nearly always a
		// bug, and here swallowing it is the documented contract rather than an
		// oversight the linter has to be told about.
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		now := s.cfg.Now()
		entries := s.reg.Enabled()
		due := make([]Entry, 0, len(entries))
		soonest := now.Add(maxTick)
		for _, e := range entries {
			t, ok := next[e.Desc.Name]
			if ok && now.Before(t) {
				if t.Before(soonest) {
					soonest = t
				}
				continue
			}
			due = append(due, e)
		}

		if len(due) > 0 {
			tgs := targets()
			set := buildSet(tgs, due)
			reports := s.runSet(ctx, set)
			rr := s.aggregate(len(tgs), reports)
			// The next-run times are stamped from `now`, the tick's start, and not
			// from the time after the round finished. Deriving them from the end
			// time would make a 3 s round push every collector's next run out by 3 s
			// every minute, so the interval drifts and R13.1's budget silently
			// becomes the thing that sets the schedule.
			for _, e := range due {
				next[e.Desc.Name] = now.Add(e.Interval)
			}
			s.log.Debug("collection round complete",
				"collectors", len(due),
				"targets", rr.Targets,
				"round_trips", rr.RoundTrips,
				"errors", rr.Errors,
				"max_target_duration", rr.MaxTargetDuration.String(),
			)
		}

		d := soonest.Sub(s.cfg.Now())
		if d < minTick {
			d = minTick
		}
		if d > maxTick {
			d = maxTick
		}
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// runSet runs one round over an explicit set, one goroutine per target, and
// returns one report per target in input order.
//
// The reports live in a pre-allocated slice with one writer per index rather than
// being appended from the goroutines: appending from N goroutines needs a lock on
// every push, which serialises the very fan-out the function exists to perform,
// and it loses the input order that makes the result comparable to the input.
func (s *Scheduler) runSet(ctx context.Context, set []targetSet) []TargetReport {
	reports := make([]TargetReport, len(set))
	var wg sync.WaitGroup
	for i, ts := range set {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, ts targetSet) {
			defer wg.Done()
			reports[i] = s.runTarget(ctx, ts.tgt, ts.entries)
		}(i, ts)
	}
	wg.Wait()
	return reports
}

// runTarget runs entries against one target, bounded by that cluster's
// concurrency cap, and returns the target's report.
//
// R2.9's seam is here. The per-cluster cap is what this function enforces today;
// R2.9's *global* budget (--max-concurrent-sql 32, --max-concurrent-http-scrapes
// 64, --scheduler-queue-depth 512) is the phase's fifth commit, and it belongs
// between this line and runOne because that is the last point where a queued
// collection can still be shed. 50 targets x 13 collectors is ~650 concurrent
// collections against a global cap of 32 SQL + 64 HTTP, so the gate will shed, and
// R2.9's shed policy ("mark the shed cluster stale rather than dropping the work
// silently") needs the two scheduler metrics that come with it. Writing a channel
// here without those would ship a half-requirement, so the seam is a comment and
// the fifth commit adds the gate and its metrics together.
func (s *Scheduler) runTarget(ctx context.Context, tgt Target, entries []Entry) TargetReport {
	tr := TargetReport{Target: tgt, Collectors: len(entries)}
	if len(entries) == 0 {
		return tr
	}
	t0 := s.cfg.Now()
	outcomes := make([]roundOutcome, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, e Entry) {
			defer wg.Done()
			outcomes[i] = s.runOne(ctx, tgt, e)
		}(i, e)
	}
	wg.Wait()
	tr.Duration = s.cfg.Now().Sub(t0)

	for _, o := range outcomes {
		if o.ran {
			tr.Rounds++
		}
		if o.err != nil {
			tr.Errors++
		}
		if o.sinkErr != nil {
			tr.SinkErrors++
		}
		tr.RoundTrips += o.res.RoundTrips
		if o.dur > tr.MaxCollectorDuration {
			tr.MaxCollectorDuration = o.dur
		}
		if o.skew > tr.MaxSkew {
			tr.MaxSkew = o.skew
		}
	}
	return tr
}

// roundOutcome is one (target, collector) run's result, in the shape the report
// needs.
type roundOutcome struct {
	// ran is false when the run never started (the context was already done when
	// the slot could not be acquired). It exists because Rounds must count runs
	// that happened, not entries that were scheduled: a round cut short by
	// shutdown would otherwise report 13 collectors when 4 ran.
	ran     bool
	res     Result
	err     error
	sinkErr error
	dur     time.Duration
	skew    time.Duration
}

// runOne runs a single collector against a single target: acquire the cluster
// slot, bound the call with the collector's timeout, observe, and publish.
func (s *Scheduler) runOne(ctx context.Context, tgt Target, e Entry) roundOutcome {
	release, ok := s.acquire(ctx, tgt.ClusterKey())
	if !ok {
		return roundOutcome{err: ctx.Err()}
	}
	// sync.Once on the release because a double release inflates the cap: two
	// releases for one acquire leave a permanent extra token in the channel, and a
	// collector that releases twice every round ends a minute with a cluster cap of
	// 4 that admits 5, then 6. The cap stops being a cap and becomes advisory,
	// which is the exact thing R2.1 and R13.2 both rely on.
	defer release()

	name := e.Desc.Name
	t0 := s.cfg.Now()
	// The per-collector timeout (R2.1). Deriving a child context rather than
	// relying on the collector to honour its own deadline is what makes the
	// timeout real for a collector that ignores ctx: the deadline is set by the
	// framework, so a collector's own bug cannot widen it.
	cctx, cancel := context.WithTimeout(ctx, e.Desc.EffectiveTimeout())
	res, err := e.Col.Collect(cctx, tgt)
	cancel()
	dur := s.cfg.Now().Sub(t0)

	out := roundOutcome{ran: true, res: res, err: err, dur: dur}

	// Observed for every round, success or failure. A round that timed out at the
	// full 10 s is the most interesting value the duration family can receive, and
	// excluding failures would make the histogram lie in exactly the case someone
	// opens a dashboard to look at.
	s.stats.ObserveDuration(name, dur.Seconds())

	if err != nil {
		s.stats.IncError(name, tgt.Cluster, tgt.Namespace)
		// And nothing is published. That non-publish *is* R2.2's "a failed
		// collector leaves its last-good data served and marks it with an age": the
		// sink still holds the previous round's samples because this round wrote
		// none, and Stats.Age still reports the last success. There is no restore
		// path to get wrong.
		return out
	}

	// R13.11's check runs on the success path because it needs the sample set to
	// compare; a failed round has no samples to carry timestamps.
	out.skew = CheckClock(s.log, tgt, res.Samples)

	if perr := s.sink.Publish(tgt, name, res.Samples); perr != nil {
		// A sink failure is not a collector failure and is deliberately not counted
		// in pgsi_collector_errors_total: the collector produced usable samples,
		// and charging the store's problem to the collector's error rate would send
		// the reader to the wrong component. It is logged and reported.
		out.sinkErr = perr
		s.log.Error("sink rejected collected samples",
			"collector", name, "cluster", tgt.Cluster, "namespace", tgt.Namespace,
			"error", perr.Error())
		return out
	}
	s.stats.MarkSuccess(name, tgt.Cluster, tgt.Namespace)
	return out
}

// acquire takes one of clusterKey's concurrency slots, blocking until one is free
// or ctx is done. The returned release func is safe to call twice; the second
// call is a no-op rather than a cap inflation.
func (s *Scheduler) acquire(ctx context.Context, clusterKey string) (func(), bool) {
	ch := s.slotsFor(clusterKey)
	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, true
	case <-ctx.Done():
		return func() {}, false
	}
}

// slotsFor returns clusterKey's semaphore, creating it on first use. Lazy
// creation, because the fleet is discovered rather than configured: pre-creating
// slots for clusters that do not exist yet would make the scheduler own a list of
// clusters that discovery already owns.
func (s *Scheduler) slotsFor(clusterKey string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.clusters[clusterKey]; ok {
		return ch
	}
	ch := make(chan struct{}, s.cfg.PerClusterConcurrency)
	s.clusters[clusterKey] = ch
	return ch
}

// ClusterCount reports how many clusters the scheduler has created slots for,
// which is the observable that tells a reader whether the cap is being applied to
// the fleet discovery actually found.
func (s *Scheduler) ClusterCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clusters)
}

// aggregate folds per-target reports into the round report.
func (s *Scheduler) aggregate(requested int, reports []TargetReport) RoundReport {
	rr := RoundReport{
		At:        s.cfg.Now(),
		Requested: requested,
		Targets:   len(reports),
		Skipped:   requested - len(reports),
		PerTarget: reports,
	}
	for _, tr := range reports {
		rr.Rounds += tr.Rounds
		rr.Errors += tr.Errors
		rr.SinkErrors += tr.SinkErrors
		rr.RoundTrips += tr.RoundTrips
		if tr.Duration > rr.MaxTargetDuration {
			rr.MaxTargetDuration = tr.Duration
		}
		if tr.MaxSkew > rr.MaxSkew {
			rr.MaxSkew = tr.MaxSkew
		}
	}
	return rr
}

// buildSet pairs targets with an entry list, dropping invalid targets and targets
// with nothing applicable. A target with no applicable entries is dropped rather
// than reported with zero collectors: running it would produce an empty round and
// a duration of ~0 that pollutes R13.1's per-cluster distribution with a target
// that did no work.
func buildSet(targets []Target, entries []Entry) []targetSet {
	out := make([]targetSet, 0, len(targets))
	for _, t := range targets {
		if !t.Valid() {
			continue
		}
		app := applicable(t, entries)
		if len(app) == 0 {
			continue
		}
		out = append(out, targetSet{tgt: t, entries: app})
	}
	return out
}

// applicable filters entries to those R2.1's PG-version applicability allows
// against tgt.
func applicable(tgt Target, entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Desc.Applies(tgt.PGMajor) {
			out = append(out, e)
		}
	}
	return out
}
