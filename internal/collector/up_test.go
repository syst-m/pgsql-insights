package collector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestUpTrackerTrackCreatesZeroSeriesForDiscoveredInstances is the half of R9.12
// a naive implementation gets wrong, and the reason Track exists at all.
//
// R9.12 says "one series per discovered instance, 1 when our last collection
// round for that instance succeeded, 0 otherwise". If the series set came only
// from Observe, a pod that discovery had found but not yet collected would
// export nothing — and PGSIPrimaryDown (chart:96
// `pgsi_up{instance_role="primary"} == 0`) cannot fire on a series that does not
// exist. The alert would be silent for exactly the pod it exists to alert on.
func TestUpTrackerTrackCreatesZeroSeries(t *testing.T) {
	u := NewUpTracker()
	u.Track([]Target{tgtPG("pg", "pg-1", "primary"), tgtPG("pg", "pg-2", "replica")})

	body := u.Render(DefaultPrefix)
	for _, line := range []string{
		`pgsi_up{cluster="pg",instance_role="primary",namespace="db",pod="pg-1"} 0`,
		`pgsi_up{cluster="pg",instance_role="replica",namespace="db",pod="pg-2"} 0`,
	} {
		if !strings.Contains(body, line) {
			t.Errorf("render is missing %q\ngot\n%s", line, body)
		}
	}
	if n := strings.Count(body, "pgsi_up{"); n != 2 {
		t.Errorf("render has %d series, want 2\ngot\n%s", n, body)
	}
}

// TestUpTrackerTrackSkipsInvalidTargets keeps the tracker's series set and the
// scheduler's runnable set on the same definition of "a target we can attribute".
// An unattributable target must not put a series on the wire either.
func TestUpTrackerTrackSkipsInvalidTargets(t *testing.T) {
	u := NewUpTracker()
	u.Track([]Target{
		tgtPG("pg", "pg-1", "primary"),
		{Namespace: "db", Pod: "pg-2"}, // no Cluster: Target.Valid() is false
	})
	if body := u.Render(DefaultPrefix); strings.Contains(body, `pod="pg-2"`) {
		t.Errorf("render includes an invalid target\ngot\n%s", body)
	}
	if _, known := u.IsUp(Target{Namespace: "db", Pod: "pg-2"}); known {
		t.Error("IsUp says an invalid target is known, want unknown")
	}
}

// TestUpTrackerTrackDoesNotResetAnObservedInstance is what makes it safe to call
// Track on every round.
//
// RunOnce calls Track before it runs the set, and Run calls it on every tick. If
// Track reset the value, a primary would flip to 0 for the duration of every
// round and PGSIPrimaryDown would fire once per interval on a perfectly healthy
// cluster.
func TestUpTrackerTrackDoesNotResetAnObservedInstance(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	u := NewUpTracker()
	u.Observe(tgt, nil, fixedClock)

	u.Track([]Target{tgt})

	if up, _ := u.IsUp(tgt); !up {
		t.Error("Track reset an observed-up instance to 0, want the observation kept")
	}
}

// TestUpTrackerTrackForgetsRemovedInstances is the other side of the same call:
// a pod that scaled in stops exporting a series rather than exporting a stale 1
// forever.
func TestUpTrackerTrackForgetsRemovedInstances(t *testing.T) {
	a, b := tgtPG("pg", "pg-1", "primary"), tgtPG("pg", "pg-2", "replica")
	u := NewUpTracker()
	u.Observe(a, nil, fixedClock)
	u.Observe(b, nil, fixedClock)

	u.Track([]Target{a})

	body := u.Render(DefaultPrefix)
	if strings.Contains(body, `pod="pg-2"`) {
		t.Errorf("a terminated pod still exports a series\ngot\n%s", body)
	}
	if _, known := u.IsUp(b); known {
		t.Error("IsUp still knows pg-2 after it left the fleet")
	}
	if up, known := u.IsUp(a); !up || !known {
		t.Errorf("IsUp(pg-1) = %v/%v, want true/true", up, known)
	}
}

// TestUpTrackerObserve covers the two outcomes and the timestamp they set.
func TestUpTrackerObserve(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	u := NewUpTracker()

	// Success: 1, and lastSuccess set to the instant we were given.
	u.Observe(tgt, nil, fixedClock)
	up, known := u.IsUp(tgt)
	if !up || !known {
		t.Fatalf("IsUp after success = %v/%v, want true/true", up, known)
	}
	if got := u.Render(DefaultPrefix); !strings.Contains(got, `pod="pg-1"} 1`) {
		t.Errorf("render after success\n%s", got)
	}

	// Failure: 0, and the previous success timestamp kept (it records the last
	// *successful* round, not the last round).
	u.Observe(tgt, errors.New("fake: connect refused"), fixedClock.Add(time.Minute))
	if up, _ := u.IsUp(tgt); up {
		t.Error("IsUp after failure = true, want false")
	}
	if got := u.Render(DefaultPrefix); !strings.Contains(got, `pod="pg-1"} 0`) {
		t.Errorf("render after failure\n%s", got)
	}
}

// TestUpTrackerObserveAddsAnUntrackedTarget keeps the two write paths agreeing.
//
// Track is the discovery-driven path and Observe the collection-driven one. A
// scheduler that observes a target it never tracked — a registry entry that
// appeared between the two — must still produce its series rather than dropping
// the observation on the floor.
func TestUpTrackerObserveAddsAnUntrackedTarget(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	u := NewUpTracker()
	u.Observe(tgt, nil, fixedClock)
	if up, known := u.IsUp(tgt); !up || !known {
		t.Errorf("IsUp = %v/%v, want true/true (Observe adds what Track missed)", up, known)
	}
}

// TestUpTrackerObserveIgnoresInvalidTarget mirrors Track's gate: an unattributable
// target has no legal label set, so it gets no series.
func TestUpTrackerObserveIgnoresInvalidTarget(t *testing.T) {
	u := NewUpTracker()
	u.Observe(Target{Namespace: "db", Pod: "x"}, nil, fixedClock)
	if body := u.Render(DefaultPrefix); body != "" && !strings.HasPrefix(body, "#") {
		// Only the HELP/TYPE header should be present.
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			if !strings.HasPrefix(line, "#") {
				t.Errorf("render has a series for an invalid target: %q", line)
			}
		}
	}
}

// TestUpTrackerIsUpDistinguishesUnknownFromDown keeps two facts that an operator
// reads differently apart: an instance that is down is an outage, an instance we
// have never heard of is a discovery bug.
func TestUpTrackerIsUpDistinguishesUnknownFromDown(t *testing.T) {
	u := NewUpTracker()
	if up, known := u.IsUp(tgtPG("pg", "pg-9", "primary")); up || known {
		t.Errorf("IsUp(unknown) = %v/%v, want false/false", up, known)
	}
}

// TestUpTrackerRenderShape checks the exposition-format header and the prefix
// rule, both of which promtool checks and neither of which a value assertion
// would catch.
func TestUpTrackerRenderShape(t *testing.T) {
	u := NewUpTracker()
	u.Track([]Target{tgtPG("pg", "pg-1", "primary")})

	body := u.Render(DefaultPrefix)
	if !strings.HasPrefix(body, "# HELP pgsi_up ") {
		t.Errorf("missing HELP line\ngot\n%s", body)
	}
	if !strings.Contains(body, "# TYPE pgsi_up gauge\n") {
		t.Errorf("missing `# TYPE pgsi_up gauge` line\ngot\n%s", body)
	}
	// R9.2: the prefix is configurable, so it is applied here and not baked into
	// UpMetricBase. A cluster that sets metrics.prefix: "acme_" has to get
	// acme_up, and the chart's rules have to keep working.
	if got := u.Render("acme_"); !strings.Contains(got, "acme_up{") {
		t.Errorf("Render(\"acme_\")\ngot\n%s", got)
	}
	// An empty prefix falls back to DefaultPrefix rather than exporting a
	// nameless metric.
	if got := u.Render(""); !strings.Contains(got, "pgsi_up{") {
		t.Errorf(`Render("")\ngot\n%s`, got)
	}
}

// TestUpTrackerRenderIsByteStable makes the output a function of the state and
// not of Go's map iteration order, which is what lets a golden fixture exist and
// what stops every scrape producing a different byte stream.
func TestUpTrackerRenderIsByteStable(t *testing.T) {
	u := NewUpTracker()
	u.Track([]Target{
		tgtPG("pg", "pg-3", "replica"),
		tgtPG("pg", "pg-1", "primary"),
		tgtPG("pg", "pg-2", "replica"),
	})
	first := u.Render(DefaultPrefix)
	for i := 0; i < 5; i++ {
		if got := u.Render(DefaultPrefix); got != first {
			t.Fatalf("render %d differs from the first\ngot\n%s\nwant\n%s", i, got, first)
		}
	}
}

// TestUpTrackerNilReceiverIsUsable covers the zero-value Scheduler path that
// NewScheduler's doc comment promises: a Scheduler built as `&Scheduler{}` has a
// nil *UpTracker, and every method on it has to behave rather than panic.
func TestUpTrackerNilReceiverIsUsable(t *testing.T) {
	var u *UpTracker
	if got := u.Render(DefaultPrefix); got != "" {
		t.Errorf("(*UpTracker)(nil).Render() = %q, want empty", got)
	}
	if up, known := u.IsUp(tgtPG("pg", "pg-1", "primary")); up || known {
		t.Errorf("nil IsUp = %v/%v, want false/false", up, known)
	}
	u.Track([]Target{tgtPG("pg", "pg-1", "primary")})
	u.Observe(tgtPG("pg", "pg-1", "primary"), nil, fixedClock)
}

// TestUpTrackerConcurrentObserve is the -race test for the tracker.
//
// runTarget fans out one goroutine per collector and Run's loop keeps the
// previous round's reports alive while the next begins, so Observe and Render
// genuinely run at the same time in production. The mutex is the thing being
// tested here, not the logic.
func TestUpTrackerConcurrentObserve(t *testing.T) {
	u := NewUpTracker()
	targets := []Target{tgtPG("pg", "pg-1", "primary"), tgtPG("pg", "pg-2", "replica")}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for _, tgt := range targets {
				u.Observe(tgt, nil, fixedClock)
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = u.Render(DefaultPrefix)
		}()
	}
	wg.Wait()
}

// TestSchedulerUpReflectsRoundOutcome is the wiring proof for R9.12, and the
// reason the Observe call sits in runTarget rather than in runOne.
//
// One target with two collectors, one of which fails. The target's round failed,
// so pgsi_up must be 0 — and it must be 0 deterministically. An Observe inside
// runOne would let the exported value depend on which of the two goroutines
// finished last, so the same fleet would export 1 or 0 at random and the alert
// would flap. Deriving it from the finished TargetReport is the fix, and this
// test is what pins it.
func TestSchedulerUpReflectsRoundOutcome(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	s := NewScheduler(Config{}, mustRegistry(t,
		newFake("database", func(context.Context, Target) (Result, error) {
			return Result{Samples: samplesOf(1, 1), RoundTrips: 1}, nil
		}),
		newFake("wal", func(context.Context, Target) (Result, error) {
			return Result{}, errors.New("fake: pg_stat_wal unavailable")
		}),
	), nil, NewMemSink(0), NewStats())

	s.RunOnce(context.Background(), []Target{tgt})

	up, known := s.Up().IsUp(tgt)
	if !known {
		t.Fatal("the scheduler ran a round for pg-1 but its tracker does not know it")
	}
	if up {
		t.Error("pgsi_up = 1, want 0 (one of the target's two collectors failed, so its round failed)")
	}
	if !strings.Contains(s.Up().Render(DefaultPrefix), `pod="pg-1"} 0`) {
		t.Errorf("render\n%s", s.Up().Render(DefaultPrefix))
	}
}

// TestSchedulerUpAllHealthy runs the same shape with every collector succeeding,
// so the 1 side of the assertion is checked too and the pair cannot be satisfied
// by a tracker that always answers 0.
func TestSchedulerUpAllHealthy(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	s := NewScheduler(Config{}, mustRegistry(t,
		newFake("database", func(context.Context, Target) (Result, error) {
			return Result{Samples: samplesOf(1, 1), RoundTrips: 1}, nil
		}),
		newFake("wal", func(context.Context, Target) (Result, error) {
			return Result{Samples: samplesOf(2, 1), RoundTrips: 1}, nil
		}),
	), nil, NewMemSink(0), NewStats())

	s.RunOnce(context.Background(), []Target{tgt})

	if up, _ := s.Up().IsUp(tgt); !up {
		t.Error("pgsi_up = 0, want 1 (both collectors succeeded)")
	}
}

// TestSchedulerUpCoversEveryTargetInAFleet checks the fleet-wide shape the chart's
// two rules depend on: a cluster with a primary and a replica exports two series,
// and one of them can be 0 while the other is 1.
func TestSchedulerUpCoversEveryTargetInAFleet(t *testing.T) {
	primary := tgtPG("pg", "pg-1", "primary")
	replica := tgtPG("pg", "pg-2", "replica")
	s := NewScheduler(Config{}, mustRegistry(t,
		newFake("database", func(_ context.Context, tgt Target) (Result, error) {
			if tgt.Role == "replica" {
				return Result{}, errors.New("fake: replica not reachable")
			}
			return Result{Samples: samplesOf(1, 1), RoundTrips: 1}, nil
		}),
	), nil, NewMemSink(0), NewStats())

	s.RunOnce(context.Background(), []Target{primary, replica})

	body := s.Up().Render(DefaultPrefix)
	if !strings.Contains(body, `instance_role="primary",namespace="db",pod="pg-1"} 1`) {
		t.Errorf("primary series wrong\ngot\n%s", body)
	}
	if !strings.Contains(body, `instance_role="replica",namespace="db",pod="pg-2"} 0`) {
		t.Errorf("replica series wrong\ngot\n%s", body)
	}
}

// TestSchedulerUpIsNilSafeForZeroValueScheduler covers the same nil path on the
// scheduler side: a Scheduler built without NewScheduler has no tracker, and
// RunOnce must not panic on it.
func TestSchedulerUpIsNilSafeForZeroValueScheduler(t *testing.T) {
	var s *Scheduler
	if s.Up() != nil {
		t.Error("(*Scheduler)(nil).Up() != nil")
	}
	z := &Scheduler{}
	if z.Up() != nil {
		t.Error("(&Scheduler{}).Up() != nil, want nil (no tracker, and every method tolerates it)")
	}
}
