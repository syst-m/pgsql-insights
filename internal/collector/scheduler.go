package collector

import (
	"context"
	"errors"
	"fmt"
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
	// MaxConcurrentSQL is R2.9's global cap on concurrent Postgres-wire
	// collections (default 32). It is global where PerClusterConcurrency above it
	// is per-cluster, and both apply to an S2 collection: R2.1's cap of 4 bounds
	// one cluster's own fan-out, and R2.9 exists because 50 clusters each obeying
	// their own cap of 4 still means ~200 concurrent queries against instances
	// where max_connections is commonly 100 — which is the sentence "R2.1 bounds
	// concurrency per cluster only" is conceding.
	MaxConcurrentSQL int
	// MaxConcurrentHTTPScrapes is R2.9's global cap on concurrent HTTP scrapes
	// (default 64), the budget every non-Postgres source draws from. See
	// SourceID.Class for which sources that is and for why the number is twice
	// the SQL one.
	MaxConcurrentHTTPScrapes int
	// SchedulerQueueDepth is R2.9's bound on how many collections may be waiting
	// for the global budget before new work is shed instead of queued (default
	// 512). A collection holds its queue token from admission until its run
	// finishes, so this bounds admitted-and-not-finished work, and
	// pgsi_scheduler_queue_depth is literally its length. The number is
	// deliberately larger than the two slot counts it fronts (32 + 64 = 96): the
	// queue is where a round's ~650 scheduled collections wait for 96 slots, and
	// the depth decides how many of them get in before the round starts shedding.
	SchedulerQueueDepth int
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
	if c.MaxConcurrentSQL <= 0 {
		c.MaxConcurrentSQL = DefaultMaxConcurrentSQL
	}
	if c.MaxConcurrentHTTPScrapes <= 0 {
		c.MaxConcurrentHTTPScrapes = DefaultMaxConcurrentHTTPScrapes
	}
	if c.SchedulerQueueDepth <= 0 {
		c.SchedulerQueueDepth = DefaultSchedulerQueueDepth
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

	// up is R9.12's pgsi_up tracker: one series per discovered instance, 1 when
	// that instance's last round succeeded and 0 otherwise. It is not part of
	// Stats — Stats' families are keyed (collector, cluster, namespace) and
	// (collector), and R9.12's label set has no collector dimension. See up.go.
	up *UpTracker

	// metrics is R2.9's pair: the queue whose length is
	// pgsi_scheduler_queue_depth and the counter behind
	// pgsi_scheduler_shed_total{cluster}. Built by NewScheduler from
	// Config.SchedulerQueueDepth, and exported through Metrics() the way up is
	// exported through Up() so /metrics renders the queue the collection loop
	// writes rather than a copy of it.
	metrics *SchedulerMetrics

	// sqlSlots and httpSlots are R2.9's two class semaphores, sized from
	// Config.MaxConcurrentSQL and Config.MaxConcurrentHTTPScrapes. Two channels
	// rather than one shared cap of 96 because R2.9 sets two numbers and the
	// reason for each is different: a fleet running 64 HTTP scrapes must not have
	// to wait for one of 32 SQL slots, and the SQL number is small because
	// Postgres max_connections is commonly 100 while the HTTP one is large
	// because a scrape costs a socket, not a connection slot. Which channel a
	// collection uses comes from SourceID.Class.
	sqlSlots  chan struct{}
	httpSlots chan struct{}

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
		cfg:       cfg,
		reg:       reg,
		log:       log,
		stats:     stats,
		sink:      sink,
		up:        NewUpTracker(),
		metrics:   NewSchedulerMetrics(cfg.SchedulerQueueDepth),
		sqlSlots:  make(chan struct{}, cfg.MaxConcurrentSQL),
		httpSlots: make(chan struct{}, cfg.MaxConcurrentHTTPScrapes),
		clusters:  map[string]chan struct{}{},
	}
}

// Up returns the scheduler's R9.12 tracker, so the /metrics handler renders the
// same tracker the collection loop writes rather than a snapshot of it.
//
// It is exported rather than kept private because the metric is only useful to a
// caller outside the scheduler, and a nil receiver is tolerated: a Scheduler
// built by a zero-value literal has no tracker, and Up() returns nil, and
// (*UpTracker)(nil).Render is defined.
func (s *Scheduler) Up() *UpTracker {
	if s == nil {
		return nil
	}
	return s.up
}

// Stats returns the scheduler's R2.2 stats, so the /metrics handler renders the
// same value the collection loop records into rather than a second one.
//
// It exists because the field is unexported and api.Config.Metrics is not. A
// caller that builds a Scheduler and passes it as Config.Scheduler has no way to
// name the *Stats that scheduler writes into, so api.New — which defaults
// Config.Metrics to collector.NewStats() when the field is nil — ends up
// rendering an empty family while the loop records into an unreachable one: two
// values where Config.Metrics' own comment requires one ("assigns that
// scheduler's own *Stats here, so /metrics and the collection loop read one
// value"). Without this accessor that rule is unsatisfiable from cmd/pgsi.
//
// A nil receiver is tolerated the way Up()'s is, and the result is safe to hand
// to api.New for the reason b4e4b56 added: (*Stats)(nil).Render is defined.
func (s *Scheduler) Stats() *Stats {
	if s == nil {
		return nil
	}
	return s.stats
}

// Metrics returns the scheduler's R2.9 pair, so the /metrics handler renders the
// same queue and shed counter the collection loop writes rather than a snapshot
// of it. It is the third accessor beside Up() and Stats() for the same reason
// they exist: the fields are unexported, the families are not, and a caller that
// builds a Scheduler has no other way to name the values it writes.
func (s *Scheduler) Metrics() *SchedulerMetrics {
	if s == nil {
		return nil
	}
	return s.metrics
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
	// Shed is the round's total across targets: R2.9's count of collections the
	// global budget refused. The metric reports the same number per cluster and
	// this reports it per round; TestShedAccountingMatchesTheMetric pins that the
	// two agree, because a report and a metric disagreeing about one count is
	// R16.8's failure class with both halves in one package.
	Shed int
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
	// Shed is how many of this target's entries R2.9's queue refused instead of
	// running. It is not folded into Errors: a shed is not a collector failure,
	// and charging it there would send a reader to a collector's code for a
	// problem with the scheduler's capacity — the same argument SinkErrors
	// two lines above makes about the store.
	Shed int
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
	// R9.12's series set comes from discovery, not from collection: an instance
	// that has been discovered but not yet collected must already export
	// pgsi_up ... 0, because "0 otherwise" covers it. Track is idempotent, so
	// calling it on every round costs a map probe per target and cannot reset an
	// instance that already has a result.
	s.up.Track(targets)

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
			// R9.12: the re-read target list is also the discovery refresh, so a
			// pod that scaled in stops exporting a series and one that scaled out
			// starts exporting a 0 before its first round finishes.
			s.up.Track(tgs)
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
// concurrency cap and by R2.9's global budget, and returns the target's report.
//
// The R2.9 seam that used to be this function's whole second paragraph is now
// gated below it. The comment named the gate's position ("between this line and
// runOne because that is the last point where a queued collection can still be
// shed") and said the commit that filled it would add the gate and its two
// metrics together, since a channel with nothing exported from it would be a
// half-requirement; that commit is this one, the gate is gated, the families are
// in scheduler_metrics.go, and the seam comment went with it — a comment
// describing a future commit is a claim that goes stale the moment the commit
// lands, and this file has already been burned once by a docblock that outlived
// the code it described.
//
// The gate is per entry, not per target, which is what makes a shed reportable
// per collector: each of the goroutines below admits itself, so a target whose
// seven entries hit a full queue reports the four that ran and the three that
// were refused instead of a round that looks like it did nothing.
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
			outcomes[i] = s.gated(ctx, tgt, e)
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
		if o.shed {
			tr.Shed++
		}
		tr.RoundTrips += o.res.RoundTrips
		if o.dur > tr.MaxCollectorDuration {
			tr.MaxCollectorDuration = o.dur
		}
		if o.skew > tr.MaxSkew {
			tr.MaxSkew = o.skew
		}
	}

	// R9.12's observation is once per (target, round) and is derived from the
	// finished report, not from any single runOne. runTarget fans out
	// len(entries) goroutines, so an Observe inside runOne would let the
	// exported value depend on which goroutine finished last: a target with
	// twelve working collectors and one broken one would export pgsi_up 1
	// whenever a healthy collector happened to win the race, which is the
	// opposite of "our last collection round for that instance succeeded".
	var upErr error
	switch {
	case tr.Errors > 0:
		upErr = fmt.Errorf("%d of %d collectors failed", tr.Errors, tr.Collectors)
	case tr.Shed > 0:
		// R2.9's "mark the shed cluster stale rather than dropping the sample".
		// pgsi_up is the one per-instance health signal this codebase has (R9.12),
		// and a round that ran four of nine collectors is not a round that
		// succeeded, so the shed folds into the same Observe call the failure
		// branch uses rather than getting a second one that the trailing call
		// would overwrite. The sink needs no marking: a shed publishes nothing, so
		// the stored samples age on their own, which is sink.go:33-38's point that
		// there is no MarkStale/Restore pair to get wrong. The counter and this
		// gauge are what make the shed visible.
		upErr = fmt.Errorf("%d of %d collectors shed", tr.Shed, tr.Collectors)
	}
	s.up.Observe(tgt, upErr, s.cfg.Now())

	return tr
}

// gated is R2.9's queue admission wrapped around runOne: take a queue slot, or
// shed; only an admitted collection reaches the run.
//
// It is its own function rather than three lines in runTarget's goroutine so
// there is exactly one place where an entry becomes a roundOutcome — the seam
// comment's "last point where a queued collection can still be shed", as a call
// site rather than as a sentence. The class semaphore is not here: it is inside
// runOne, after the per-cluster slot, so a global token is held only while a
// collection is in flight. Admitting here and running there is the whole point —
// between these two calls sits a collection that has been admitted and has not
// finished, which is the thing pgsi_scheduler_queue_depth counts.
func (s *Scheduler) gated(ctx context.Context, tgt Target, e Entry) roundOutcome {
	release, ok := s.metrics.Admit()
	if !ok {
		s.metrics.ObserveShed(tgt)
		s.log.Debug("collector run shed",
			"collector", e.Desc.Name, "cluster", tgt.Cluster, "namespace", tgt.Namespace,
			"queue_depth", s.metrics.Depth())
		return roundOutcome{shed: true}
	}
	defer release()
	return s.runOne(ctx, tgt, e)
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
	// shed is true when R2.9's queue refused the collection. It is not err: a
	// shed did not run, so it has no collector error, and runTarget counts it in
	// TargetReport.Shed rather than in Errors for the reason sinkErr exists —
	// charging a non-collector problem to the collector's error rate sends the
	// reader to the wrong component.
	shed bool
	dur  time.Duration
	skew time.Duration
}

// runOne runs a single collector against a single target: acquire the cluster
// slot, then R2.9's class slot, bound the call with the collector's timeout,
// observe, and publish.
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

	// R2.9's class slot, taken after the cluster slot and released before it (the
	// two defers unwind in that order). The order is what keeps the global cap
	// honest: a class token is held only while the collection is in flight, so
	// --max-concurrent-sql 32 means 32 queries in flight rather than 32 tokens
	// parked on goroutines waiting for a per-cluster slot. Take it the other way
	// round and a cluster whose four slots are full holds a global token for a
	// collection that cannot start — R13.2's "one slow cluster delays every other
	// one", reintroduced at the level R2.9 exists to fix.
	classRelease, ok := s.acquireClass(ctx, e.Desc.Source.Class())
	if !ok {
		return roundOutcome{err: ctx.Err()}
	}
	defer classRelease()

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

	// R9.3's identity half is the scheduler's to fill — clock.go's Sample doc says
	// so ("The scheduler fills the identity labels from the
	// Target so a collector returns only the series-specific ones"), and until
	// step 4 that sentence had no implementation behind it.
	//
	// It is applied here, on the success path, and not above the error branch,
	// because that is where the samples are consumed. A failed round publishes
	// nothing — that non-publish *is* R2.2's "a failed collector leaves its
	// last-good data served and marks it with an age", so the sink keeps the
	// *previous* round's samples, which were labelled when that round succeeded.
	// Labelling the failed round's res.Samples would allocate a map per sample for
	// the one field of that Result that anyone reads back (RoundTrips).
	res.Samples = ApplyIdentityLabels(tgt, res.Samples)

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

// acquireClass takes one of the global slots for c — R2.9's --max-concurrent-sql
// or --max-concurrent-http-scrapes, picked by SourceID.Class — blocking until one
// is free or ctx is done.
//
// It is acquire's twin one level up and copies its two details for the same two
// reasons: a sync.Once on the release, because a double release inflates the cap
// from 32 to 33 and keeps inflating it, and a select on ctx.Done(), because a
// caller must be able to walk away from a slot it cannot get rather than hold a
// round hostage to one class being busy. A Scheduler not built by NewScheduler
// has nil slots and gets an unlimited pass, matching the nil-receiver rule the
// rest of the package follows.
func (s *Scheduler) acquireClass(ctx context.Context, c Class) (func(), bool) {
	var ch chan struct{}
	switch c {
	case ClassSQL:
		ch = s.sqlSlots
	case ClassHTTP:
		ch = s.httpSlots
	}
	if ch == nil {
		return func() {}, true
	}
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
		rr.Shed += tr.Shed
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

// applicable filters entries to the ones this target may run, which is two
// questions, and for most of Phase 1a it asked only one of them.
//
// The first is R2.1's required-source match. Target.Source's own docblock
// (collector.go:321-323) states what the field is for — "so a scheduler can hand
// a collector only the targets it declared (R2.1) instead of every target it
// has" — and until this commit the scheduler had the field and did not use it:
// runSet built every pairing from the whole registry, so the declaration was
// written down and never read. The cost is countable from the registry as it
// stands: nine entries, seven of them S2, and a pooler pod now yields two
// targets (S5 and S6), so each pooler-pod target was paired with all nine
// entries — nine collectors run against a pgbouncer HTTP endpoint and nine
// against a pgbouncer console, where one each can succeed — and every instance
// target ran pooler.go and pooler_clients.go against Postgres, two collectors
// whose queries are `SHOW POOLS` and `SHOW CLIENTS` and which exist only on a
// pgbouncer port. The version filter below cannot catch either, because both
// pooler descriptors are AllMajors: their applicability is a statement about
// Postgres versions, and a pgbouncer endpoint does not have one to compare.
//
// The second is R2.1's PG-version applicability, unchanged.
//
// The source test goes first because it is the cheaper and the more absolute of
// the two: a source mismatch is a pairing the registry could never run anywhere,
// a version mismatch is one this particular instance cannot run.
func applicable(tgt Target, entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Desc.Source != tgt.Source {
			continue
		}
		if e.Desc.Applies(tgt.PGMajor) {
			out = append(out, e)
		}
	}
	return out
}
