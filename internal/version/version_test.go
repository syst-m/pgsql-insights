package version

import "testing"

// TestString covers both the built-in defaults and the ldflags-injected shape
// (R11.3) in one test: Version/Commit are package globals, so splitting them
// across tests would make the assertions depend on test order.
func TestString(t *testing.T) {
	if got, want := String(), "pgsi dev (commit unknown)"; got != want {
		t.Fatalf("String() with defaults = %q, want %q", got, want)
	}

	Version, Commit = "0.1.0", "abc1234"
	t.Cleanup(func() { Version, Commit = "dev", "unknown" })

	if got, want := String(), "pgsi 0.1.0 (commit abc1234)"; got != want {
		t.Fatalf("String() with injected build info = %q, want %q", got, want)
	}
}
