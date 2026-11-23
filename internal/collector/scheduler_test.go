package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tgtPG(cluster, pod, role string) Target {
	return Target{Namespace: "db", Cluster: cluster, Pod: pod, Role: role, Source: SourceSQL, PGMajor: 16}
}

// mustRegistry builds a registry from the collectors, failing the test if
// NewRegistry rejects them, so a scheduler test's setup error points at the
// registry rather than at a nil dereference later.
func mustRegistry(t *testing.T, cols ...Collector) *Registry {
	t.Helper()
	r, err := NewRegistry(cols, Overrides{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

// samplesOf builds n one-label samples with a fixed value, for asserting that
// exactly the right set reached the sink.
func samplesOf(value float64, n int) []Sample {
	out := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Sample{
			Metric: "db_xact_commit_total",
			Labels: map[string]string{"datname": "postgres"},
			Value:  value,
		})
	}
	return out
}

// colName gives distinct valid collector names for i in [0, 26).
func colName(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	return "c" + string(letters[i%len(letters)])
}

func TestRunOnceAggregation(t *testing.T) {
	// Two targets x three collectors, each returning two samples and reporting one
	// round-trip. Every number in the report is checkable against that product,
	// which is the point: R13.1's budget is asserted from a real round.
	cols := []Collector{
		newFake("database", func(context.Context, Target) (Result, error) {
			return Result{Samples: samplesOf(1, 2), RoundTrips: 1}, nil
		}),
		newFake("wal", func(context.Context, Target) (Result, error) {
			return Result{Samples: samplesOf(2, 2), RoundTrips: 1}, nil
		}),
		newFake("locks", func(context.Context, Target) (Result, error) {
			return Result{Samples: samplesOf(3, 2), RoundTrips: 1}, nil
		}),
	}
	sink := NewMemSink(0)
	stats := NewStats()
	s := NewScheduler(Config{}, mustRegistry(t, cols...), nil, sink, stats)

	targets := []Target{tgtPG("pg", "pg-1", "primary"), tgtPG("pg", "pg-2", "replica")}
	rr := s.RunOnce(context.Background(), targets)

	if rr.Requested != 2 || rr.Targets != 2 || rr.Skipped != 0 {
		t.Errorf("requested/targets/skipped = %d/%d/%d, want 2/2/0", rr.Requested, rr.Targets, rr.Skipped)
	}
	if rr.Rounds != 6 {
		t.Errorf("Rounds = %d, want 6 (2 targets x 3 collectors)", rr.Rounds)
	}
	if rr.Errors != 0 || rr.SinkErrors != 0 {
		t.Errorf("errors/sinkErrors = %d/%d, want 0/0", rr.Errors, rr.SinkErrors)
	}
	if rr.RoundTrips != 6 {
		t.Errorf("RoundTrips = %d, want 6 (R13.1's number, summed over 6 rounds of 1)", rr.RoundTrips)
	}
	if len(rr.PerTarget) != 2 {
		t.Fatalf("PerTarget = %d, want 2", len(rr.PerTarget))
	}
	for _, tr := range rr.PerTarget {
		if tr.Collectors != 3 || tr.Rounds != 3 || tr.RoundTrips != 3 {
			t.Errorf("per-target %s: collectors/rounds/roundtrips = %d/%d/%d, want 3/3/3",
				tr.Target.Key(), tr.Collectors, tr.Rounds, tr.RoundTrips)
		}
	}
	// Six buckets in the sink: one per (target, collector) pair.
	if got := len(sink.Keys()); got != 6 {
		t.Errorf("sink has %d buckets, want 6", got)
	}
	// One header pair for the family, three sample sets — one per collector, not
	// one per (collector, target). This is R2.2's label set, and the cardinality
	// claim R13.4 makes depends on it.
	if n := strings.Count(stats.RenderDuration(), "# TYPE"); n != 1 {
		t.Errorf("duration render has %d TYPE lines, want 1 (one per family, not per collector)", n)
	}
	if n := strings.Count(stats.RenderDuration(), "_count{"); n != 3 {
		t.Errorf("duration render has %d _count lines, want 3", n)
	}
	// Each series carries 2, not 1: the family is keyed by collector alone, so both
	// of this round's targets contributed to the same three histograms. Asserting the
	// value, not just the line count above, is what proves the two targets merged
	// into one series instead of producing six.
	for _, c := range []string{"database", "locks", "wal"} {
		want := fmt.Sprintf("pgsi_collector_duration_seconds_count{collector=%q} 2", c)
		if !strings.Contains(stats.RenderDuration(), want) {
			t.Errorf("duration render is missing %q\ngot\n%s", want, stats.RenderDuration())
		}
	}
}

func TestRunOnceSkipsInvalidTargets(t *testing.T) {
	s := NewScheduler(Config{}, mustRegistry(t, newFake("wal", nil)), nil, nil, nil)
	rr := s.RunOnce(context.Background(), []Target{
		tgtPG("pg", "pg-1", "primary"),
		{Namespace: "db", Source: SourceSQL}, // no cluster: invalid
		{Cluster: "pg", Source: SourceSQL},   // no namespace: invalid
	})
	if rr.Requested != 3 {
		t.Errorf("Requested = %d, want 3", rr.Requested)
	}
	if rr.Targets != 1 || rr.Skipped != 2 {
		t.Errorf("targets/skipped = %d/%d, want 1/2 (invalid targets are reported, not silently dropped)",
			rr.Targets, rr.Skipped)
	}
}

func TestRunOnceAppliesPGVersionFilter(t *testing.T) {
	// A collector valid only on PG 16 must not run against a PG 15 target — that
	// is R2.1's fourth field doing work, and the seam the pgversion matrix plugs
	// into.
	col := fakeCollector{
		desc: Descriptor{
			Name: "wal", Interval: DefaultInterval, Source: SourceSQL, Cost: CostStandard,
			AppliesTo: func(m int) bool { return m == 16 },
		},
		fn: func(context.Context, Target) (Result, error) { return Result{}, nil },
	}
	s := NewScheduler(Config{}, mustRegistry(t, col), nil, nil, nil)

	rr := s.RunOnce(context.Background(), []Target{tgtPG("pg", "pg-16", "primary")})
	if rr.Targets != 1 {
		t.Fatalf("Targets = %d, want 1", rr.Targets)
	}
	pg15 := Target{Namespace: "db", Cluster: "pg", Pod: "pg-15", Role: "primary", Source: SourceSQL, PGMajor: 15}
	rr = s.RunOnce(context.Background(), []Target{pg15})
	// The target is valid but nothing is applicable, so it is dropped rather than
	// run with zero collectors.
	if rr.Targets != 0 || rr.Skipped != 1 {
		t.Errorf("targets/skipped = %d/%d, want 0/1 for a PG15 target with a PG16-only registry", rr.Targets, rr.Skipped)
	}
}

func TestRunOnceFailureIsolationAndLastGood(t *testing.T) {
	// R2.2's last-good property, end to end: a round succeeds and publishes; the
	// next round fails; the first round's samples are still served and the error
	// counter moved.
	var fail atomic.Bool
	col := newFake("wal", func(context.Context, Target) (Result, error) {
		if fail.Load() {
			return Result{RoundTrips: 1}, errors.New("connection refused")
		}
		return Result{Samples: samplesOf(7, 3), RoundTrips: 1}, nil
	})
	sink := NewMemSink(0)
	stats := NewStats()
	s := NewScheduler(Config{}, mustRegistry(t, col), nil, sink, stats)
	target := tgtPG("pg", "pg-1", "primary")

	if rr := s.RunOnce(context.Background(), []Target{target}); rr.Errors != 0 {
		t.Fatalf("first round errors = %d, want 0", rr.Errors)
	}
	first := sink.Samples(target, "wal")
	if len(first) != 3 || first[0].Value != 7 {
		t.Fatalf("first round samples = %v, want three samples of 7", first)
	}

	fail.Store(true)
	rr := s.RunOnce(context.Background(), []Target{target})
	if rr.Errors != 1 {
		t.Errorf("second round errors = %d, want 1", rr.Errors)
	}
	// The failed round reported one round-trip even though it failed, and the
	// report counts it: a collector that returns (zero, err) on a partial failure
	// is the thing collector.go's contract forbids, and this is where that would
	// be visible.
	if rr.RoundTrips != 1 {
		t.Errorf("second round RoundTrips = %d, want 1 (the failed round still issued one query)", rr.RoundTrips)
	}
	// The old samples survive. This non-publish is the whole of R2.2's
	// "a failed collector leaves its last-good data served".
	after := sink.Samples(target, "wal")
	if len(after) != 3 || after[0].Value != 7 {
		t.Fatalf("samples after a failed round = %v, want the previous round's three samples of 7", after)
	}
	if _, ok := stats.Age("wal", "pg", "db"); !ok {
		t.Error("Stats.Age reports the collector never succeeded, but the first round did")
	}
	if got := stats.RenderErrors(); !strings.Contains(got, `pgsi_collector_errors_total{cluster="pg",collector="wal",namespace="db"} 1`) {
		t.Errorf("errors render = %q, want a wal/pg/db count of 1", got)
	}
	// The failed round is still in the duration histogram: observing only
	// successes would make the family lie in the case a reader opens it for.
	if !strings.Contains(stats.RenderDuration(), "pgsi_collector_duration_seconds_count{collector=\"wal\"} 2") {
		t.Errorf("duration render = %q, want a count of 2 (both rounds observed)", stats.RenderDuration())
	}
}

func TestPerClusterConcurrencyCapSingleTarget(t *testing.T) {
	// One target, eight collectors, cap 4: at most four may be in flight at once,
	// and more than one must be (otherwise a serial scheduler also passes).
	assertCap(t, 4, []Target{tgtPG("pg", "pg-1", "primary")}, 8)
}

func TestPerClusterConcurrencyCapSharedAcrossPods(t *testing.T) {
	// Two pods of one cluster, four collectors each: the cap is per *cluster*, so
	// eight collectors across two instances still admit four at a time and not
	// eight. This is R2.1's scoping, and a per-target semaphore would report 8.
	assertCap(t, 4, []Target{
		tgtPG("pg", "pg-1", "primary"),
		tgtPG("pg", "pg-2", "replica"),
	}, 4)
}

func TestPerClusterConcurrencyCapTwoClusters(t *testing.T) {
	// Two clusters, cap 4 each: eight collectors across two clusters admit up to
	// eight at once, because the cap is per-cluster and the two clusters are
	// uncorrelated (R13.2: a slow cluster must not delay the others).
	assertCap(t, 8, []Target{
		tgtPG("pg", "pg-1", "primary"),
		tgtPG("other", "other-1", "primary"),
	}, 4)
}

// assertCap runs one round over targets with nCols collectors each and checks the
// peak number of Collect calls in flight is at most wantMax and at least 2.
func assertCap(t *testing.T, wantMax int, targets []Target, nCols int) {
	t.Helper()

	var cur, peak int32
	cols := make([]Collector, 0, nCols)
	for i := 0; i < nCols; i++ {
		name := colName(i)
		cols = append(cols, fakeCollector{desc: okDesc(name), fn: func(context.Context, Target) (Result, error) {
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&peak)
				if n <= m || atomic.CompareAndSwapInt32(&peak, m, n) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
			return Result{}, nil
		}})
	}

	s := NewScheduler(Config{PerClusterConcurrency: 4}, mustRegistry(t, cols...), nil, nil, nil)
	s.RunOnce(context.Background(), targets)

	got := int(atomic.LoadInt32(&peak))
	if got > wantMax {
		t.Errorf("peak concurrent collectors = %d, want <= %d", got, wantMax)
	}
	if got < 2 {
		t.Errorf("peak concurrent collectors = %d, want > 1 (a serial scheduler passes the <= test by accident)", got)
	}
}

func TestPerCollectorTimeoutApplied(t *testing.T) {
	// R2.1's per-collector timeout must be applied by the framework, not merely
	// documented: the ctx handed to Collect must carry a deadline no later than
	// the Descriptor's Timeout, and a collector that blocks must be cut off.
	t.Run("deadline is set from the descriptor", func(t *testing.T) {
		var (
			mu       sync.Mutex
			deadline time.Time
			has      bool
		)
		col := fakeCollector{
			desc: Descriptor{Name: "wal", Interval: DefaultInterval, Source: SourceSQL, Cost: CostStandard, Timeout: 2 * time.Second},
			fn: func(ctx context.Context, _ Target) (Result, error) {
				mu.Lock()
				defer mu.Unlock()
				deadline, has = ctx.Deadline()
				return Result{}, nil
			},
		}
		s := NewScheduler(Config{}, mustRegistry(t, col), nil, nil, nil)
		s.RunOnce(context.Background(), []Target{tgtPG("pg", "pg-1", "primary")})
		mu.Lock()
		defer mu.Unlock()
		if !has {
			t.Fatal("Collect received a context with no deadline (R2.1's timeout is not applied)")
		}
		if d := time.Until(deadline); d > 2*time.Second || d <= 0 {
			t.Errorf("deadline is %s away, want <= 2s and > 0 (the descriptor's Timeout)", d)
		}
	})

	t.Run("a blocking collector is cut off", func(t *testing.T) {
		col := fakeCollector{
			desc: Descriptor{Name: "wal", Interval: DefaultInterval, Source: SourceSQL, Cost: CostStandard, Timeout: 60 * time.Millisecond},
			fn: func(ctx context.Context, _ Target) (Result, error) {
				<-ctx.Done()
				return Result{}, ctx.Err()
			},
		}
		stats := NewStats()
		s := NewScheduler(Config{}, mustRegistry(t, col), nil, nil, stats)
		target := tgtPG("pg", "pg-1", "primary")
		start := time.Now()
		rr := s.RunOnce(context.Background(), []Target{target})
		elapsed := time.Since(start)
		if rr.Errors != 1 {
			t.Fatalf("errors = %d, want 1 (the collector returned DeadlineExceeded)", rr.Errors)
		}
		if elapsed < 60*time.Millisecond {
			t.Errorf("round took %s, want >= 60ms (the timeout the descriptor declared)", elapsed)
		}
		if elapsed > 3*time.Second {
			t.Errorf("round took %s, want well under the 10s default (the 60ms timeout should have fired)", elapsed)
		}
		// A timed-out round is still observed in the duration family: it is the
		// most interesting value the family can carry.
		if !strings.Contains(stats.RenderDuration(), "pgsi_collector_duration_seconds_count{collector=\"wal\"} 1") {
			t.Errorf("duration render = %q, want a count of 1 for the timed-out round", stats.RenderDuration())
		}
	})

	t.Run("unset timeout uses the default", func(t *testing.T) {
		var (
			mu sync.Mutex
			dl time.Time
		)
		col := fakeCollector{
			desc: okDesc("wal"), // Timeout unset
			fn: func(ctx context.Context, _ Target) (Result, error) {
				mu.Lock()
				defer mu.Unlock()
				dl, _ = ctx.Deadline()
				return Result{}, nil
			},
		}
		s := NewScheduler(Config{}, mustRegistry(t, col), nil, nil, nil)
		s.RunOnce(context.Background(), []Target{tgtPG("pg", "pg-1", "primary")})
		mu.Lock()
		defer mu.Unlock()
		if d := time.Until(dl); d > DefaultTimeout || d <= 0 {
			t.Errorf("deadline is %s away, want <= %s (DefaultTimeout)", d, DefaultTimeout)
		}
	})
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.PerClusterConcurrency != DefaultPerClusterConcurrency {
		t.Errorf("PerClusterConcurrency = %d, want %d", c.PerClusterConcurrency, DefaultPerClusterConcurrency)
	}
	if c.DefaultTimeout != DefaultTimeout {
		t.Errorf("DefaultTimeout = %s, want %s", c.DefaultTimeout, DefaultTimeout)
	}
	if c.Now == nil {
		t.Fatal("Now = nil after withDefaults")
	}
	set := Config{PerClusterConcurrency: 9, DefaultTimeout: time.Minute, Now: func() time.Time { return time.Unix(1, 0) }}.withDefaults()
	if set.PerClusterConcurrency != 9 || set.DefaultTimeout != time.Minute {
		t.Errorf("explicit values were overwritten: %+v", set)
	}
	if !set.Now().Equal(time.Unix(1, 0)) {
		t.Error("an injected Now must survive withDefaults")
	}
	// A negative value is not "unset" in any useful sense; it must fall back
	// rather than become a zero-capacity channel that deadlocks every round.
	neg := Config{PerClusterConcurrency: -3, DefaultTimeout: -1}.withDefaults()
	if neg.PerClusterConcurrency != DefaultPerClusterConcurrency || neg.DefaultTimeout != DefaultTimeout {
		t.Errorf("negative values = %+v, want the defaults", neg)
	}
}

func TestAcquireReleaseIsIdempotent(t *testing.T) {
	// A double release must not inflate the cap. Without the sync.Once, the second
	// `<-ch` blocks forever on an empty channel and the test hangs; with it, the
	// second call is a no-op and the slot is still acquirable.
	s := NewScheduler(Config{PerClusterConcurrency: 1}, mustRegistry(t, newFake("wal", nil)), nil, nil, nil)
	rel, ok := s.acquire(context.Background(), "db/pg")
	if !ok {
		t.Fatal("acquire failed on an empty semaphore")
	}
	rel()
	done := make(chan struct{})
	go func() { rel(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a second release blocked (the release func is not idempotent, so the cap inflates every round)")
	}
	// Re-acquiring proves the token came back. The release func is kept rather than
	// discarded: on a cap-1 channel a dropped func *is* a permanently held slot, and
	// every later acquire on that cluster then blocks until its ctx is done. (The
	// first draft of this test did discard it, and the test hung for the full
	// 600 s default timeout on the very next line.)
	rel2, ok := s.acquire(context.Background(), "db/pg")
	if !ok {
		t.Fatal("the slot could not be re-acquired after one release")
	}
	// Slots are per-cluster: other/cluster has its own capacity while db/pg's only
	// slot is held. Doing it here, rather than after rel2(), is what makes the
	// "own slots" claim mean something.
	rel3, ok := s.acquire(context.Background(), "other/cluster")
	if !ok {
		t.Error("a different cluster must have its own slots")
	}
	rel3()
	// Asking for a second slot on the now-full cap-1 cluster must fail when the ctx
	// is cancelled rather than block forever — the property runOne depends on when a
	// round's context is cancelled mid-flight.
	blocked, cancel := context.WithCancel(context.Background())
	got := make(chan bool, 1)
	go func() {
		_, ok := s.acquire(blocked, "db/pg")
		got <- ok
	}()
	// Prove it is actually blocking before cancelling: an acquire that reports
	// success while the channel is full is a cap inflation, and would pass a
	// cancel-then-check test just as easily.
	time.Sleep(50 * time.Millisecond)
	select {
	case ok := <-got:
		t.Fatalf("acquire on a full cap-1 semaphore returned %v before its ctx was cancelled, want it to block", ok)
	default:
	}
	cancel()
	select {
	case ok := <-got:
		if ok {
			t.Error("acquire reported success after its ctx was cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire did not return after its ctx was cancelled")
	}
	rel2()
}

func TestClusterCount(t *testing.T) {
	s := NewScheduler(Config{}, mustRegistry(t, newFake("wal", nil)), nil, nil, nil)
	if s.ClusterCount() != 0 {
		t.Fatalf("ClusterCount() = %d before any round, want 0 (slots are created lazily)", s.ClusterCount())
	}
	s.RunOnce(context.Background(), []Target{
		tgtPG("pg", "pg-1", "primary"),
		tgtPG("pg", "pg-2", "replica"), // same cluster
		tgtPG("other", "other-1", "primary"),
	})
	if got := s.ClusterCount(); got != 2 {
		t.Errorf("ClusterCount() = %d, want 2 (two clusters, three pods)", got)
	}
}

func TestRunNilTargetsFunc(t *testing.T) {
	s := NewScheduler(Config{}, mustRegistry(t, newFake("wal", nil)), nil, nil, nil)
	if err := s.Run(context.Background(), nil); err == nil {
		t.Fatal("Run(nil) = nil, want an error (a scheduler that runs nothing must not look healthy)")
	}
}

func TestRunDispatchesAndReturnsOnCancel(t *testing.T) {
	// Run's loop is a ticker over an interval the tests cannot wait out (60 s), so
	// this asserts the two things that are observable in milliseconds: the first
	// tick dispatched the collector, and a cancel returns Run promptly with nil.
	var calls int32
	col := newFake("wal", func(context.Context, Target) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{}, nil
	})
	s := NewScheduler(Config{}, mustRegistry(t, col), nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, func() []Target { return []Target{tgtPG("pg", "pg-1", "primary")} })
	}()
	// Give the first tick a moment to dispatch, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, want nil on cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Error("Run never dispatched the first round (the collector was never called)")
	}
}
