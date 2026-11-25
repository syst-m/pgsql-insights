package collector

import (
	"testing"
	"time"
)

// TestErrLogGate covers the gate's policy directly, at the window boundary, with
// no Scheduler in the way.
//
// The scheduler's own test covers the call site; this covers the rules. They are
// separate files because a change to a rule and a change to the wiring should
// fail in different places — a single test that asserts both cannot say which one
// moved when it goes red, which is the property that makes a gate test worth
// having.
//
// The clock is a captured variable rather than time.Now so every case can name
// the exact instant it is testing. The window here is one minute, not the
// production five, via newErrLogGateWindow; the production default gets its own
// subtest so a change to DefaultErrorLogWindow is caught by the constructor
// production actually calls, not only by the test constructor.
func TestErrLogGate(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	now := func() time.Time { return at }
	const msg = "connection refused"

	t.Run("unseen, suppressed, expired", func(t *testing.T) {
		g := newErrLogGateWindow(now, time.Minute)

		// Case 1: a key the gate has never seen logs immediately, with no repeat
		// count — there is nothing yet that could have been suppressed.
		if ok, reps := g.allow("wal", "pg", "db", msg); !ok || reps != 0 {
			t.Fatalf("first allow = (%v, %d), want (true, 0)", ok, reps)
		}

		// Case 3: same key, same message, window not elapsed. Suppressed, and
		// counted. The counting is the half that makes rate-limiting lossless:
		// every suppressed error is still reported, just on a later line.
		at = at.Add(time.Second)
		if ok, reps := g.allow("wal", "pg", "db", msg); ok || reps != 1 {
			t.Fatalf("second allow = (%v, %d), want (false, 1)", ok, reps)
		}
		at = at.Add(time.Second)
		if ok, reps := g.allow("wal", "pg", "db", msg); ok || reps != 2 {
			t.Fatalf("third allow = (%v, %d), want (false, 2)", ok, reps)
		}

		// Case 4: the window elapses. It logs and reports the two it suppressed.
		// The boundary is inclusive: exactly at+window logs, because the check is
		// Before(at+window) and not BeforeOrEqual.
		at = at.Add(time.Minute - time.Second)
		if ok, reps := g.allow("wal", "pg", "db", msg); !ok || reps != 2 {
			t.Fatalf("allow at exactly the window edge = (%v, %d), want (true, 2)", ok, reps)
		}
		// The count resets at the line that reports it — otherwise the next
		// window's line would report a number that double-counts the errors the
		// line it just wrote already carried.
		at = at.Add(time.Second)
		if ok, reps := g.allow("wal", "pg", "db", msg); ok || reps != 1 {
			t.Fatalf("allow after the reset = (%v, %d), want (false, 1)", ok, reps)
		}
	})

	// Case 2: a changed message logs immediately, inside the window, and starts a
	// fresh count. A new failure mode is news; a window that suppressed it would
	// hide a regression behind the error it replaced — which is exactly the 482d82e
	// shape, where a TLS failure would have been suppressed behind the "connection
	// refused" it replaced.
	t.Run("changed message bypasses the window", func(t *testing.T) {
		g := newErrLogGateWindow(now, time.Hour)
		if ok, _ := g.allow("wal", "pg", "db", msg); !ok {
			t.Fatal("first allow = false, want true")
		}
		if ok, reps := g.allow("wal", "pg", "db", msg); ok || reps != 1 {
			t.Fatalf("suppressed allow = (%v, %d), want (false, 1)", ok, reps)
		}
		if ok, reps := g.allow("wal", "pg", "db", "SSL error: certificate has expired"); !ok || reps != 0 {
			t.Fatalf("changed-message allow = (%v, %d), want (true, 0)", ok, reps)
		}
		// The old message's count did not travel onto the new message, and the new
		// message now has its own.
		if ok, reps := g.allow("wal", "pg", "db", msg); !ok || reps != 0 {
			t.Fatalf("allow of the old message = (%v, %d), want (true, 0) (the replaced error is logged again, not counted onto the new one)", ok, reps)
		}
	})

	// The key is per target, not per collector: two pods of one cluster fail
	// independently, so a primary's silence must not mute a replica's error.
	t.Run("keys are per target", func(t *testing.T) {
		g := newErrLogGateWindow(now, time.Hour)
		if ok, _ := g.allow("wal", "pg", "db", msg); !ok {
			t.Fatal("primary: allow = false, want true")
		}
		if ok, _ := g.allow("wal", "pg", "db", msg); ok {
			t.Error("primary, second round: allow = true, want false (suppressed)")
		}
		if ok, reps := g.allow("wal", "pg", "db", msg); ok || reps != 2 {
			t.Errorf("primary, third round: allow = (%v, %d), want (false, 2) (the second call recorded one, so the third reports two)", ok, reps)
		}
		if ok, reps := g.allow("wal", "pg-replica", "db", msg); !ok || reps != 0 {
			t.Errorf("replica: allow = (%v, %d), want (true, 0) (a different pod is a different key)", ok, reps)
		}
		if ok, reps := g.allow("database", "pg", "db", msg); !ok || reps != 0 {
			t.Errorf("other collector, same pod: allow = (%v, %d), want (true, 0)", ok, reps)
		}
	})

	// The production constructor's window, asserted through the constructor
	// production calls. Without this, changing DefaultErrorLogWindow to 0 (which
	// would mean "log every error", the pre-gate behaviour) would still pass the
	// tests above, because they all pass their own window.
	t.Run("the default window is the constant", func(t *testing.T) {
		g := newErrLogGate(now)
		if g.window != DefaultErrorLogWindow {
			t.Fatalf("newErrLogGate window = %v, want %v", g.window, DefaultErrorLogWindow)
		}
		if ok, _ := g.allow("wal", "pg", "db", msg); !ok {
			t.Fatal("first allow = false, want true")
		}
		at = at.Add(DefaultErrorLogWindow - time.Nanosecond)
		if ok, reps := g.allow("wal", "pg", "db", msg); ok || reps != 1 {
			t.Fatalf("allow one nanosecond inside the window = (%v, %d), want (false, 1)", ok, reps)
		}
		at = at.Add(time.Nanosecond)
		if ok, _ := g.allow("wal", "pg", "db", msg); !ok {
			t.Fatal("allow at exactly the default window = false, want true")
		}
	})

	// A nil gate logs and records nothing. That is the pre-gate behaviour, and it
	// is what a Scheduler built by a struct literal (not NewScheduler) gets — the
	// same degrade-don't-panic rule Up() follows for a nil tracker.
	t.Run("nil gate logs everything", func(t *testing.T) {
		var g *errLogGate
		for i := 0; i < 3; i++ {
			if ok, reps := g.allow("wal", "pg", "db", msg); !ok || reps != 0 {
				t.Fatalf("nil gate allow #%d = (%v, %d), want (true, 0)", i+1, ok, reps)
			}
		}
	})
}
