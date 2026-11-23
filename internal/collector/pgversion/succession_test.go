package pgversion

// succession_test.go covers the rename/move/split/retire half of R2.4. The
// tests were written in the same commit as the table (R16.1's rule for the
// matrix applies to its second file too) and the central one —
// TestResolveAcrossAllMajors — is generic over the whole succession table
// rather than sampling it: for every recorded succession and every major, it
// asserts the answer Resolve gives. That is what catches a `Since` off by one,
// which is the realistic bug here and the one a sampled test misses.

import (
	"reflect"
	"testing"
)

// TestResolveAcrossAllMajors walks every succession × every major.
//
// The rule it asserts is the one Resolve documents:
//
//	major >= Since          → the succession's To (empty for a retire, but ok=true)
//	major <  Since, view has the column on that major → the column itself
//	major <  Since, view not there at all             → nothing, ok=false
func TestResolveAcrossAllMajors(t *testing.T) {
	for _, s := range Successions() {
		for _, major := range Majors() {
			got, ok := Resolve(s.From.View, s.From.Column, major)
			cols, viewExists := Columns(s.From.View, major)
			var want []Source
			switch {
			case major >= s.Since:
				want = s.To
			case viewExists && hasCol(cols, s.From.Column):
				want = []Source{s.From}
			default:
				// The view itself is not there on this major (pg_stat_io before
				// PG16) or the pre-image predates the table (PG12's total_time).
				if ok {
					t.Errorf("Resolve(%s, %d) = (%v, true), want not-found", s.From, major, got)
				}
				continue
			}
			if !ok {
				t.Errorf("Resolve(%s, %d) not found, want %v", s.From, major, want)
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Resolve(%s, %d) = %v, want %v", s.From, major, got, want)
			}
		}
	}
}

func TestValidateSuccessions(t *testing.T) {
	if err := ValidateSuccessions(); err != nil {
		t.Fatalf("ValidateSuccessions(): %v", err)
	}
}

// TestR2_4CasesAreAllPresent pins the table itself against R2.4's enumerated
// list. The spec names four cases; the table holds fourteen rows, because two of
// the four (the bgwriter shrink and the pg_stat_wal shrink) are per-column
// changes that cannot be described by one row each.
func TestR2_4CasesAreAllPresent(t *testing.T) {
	want := []Source{
		// Case 1: total_time → total_exec_time at PG13.
		{ViewStatStatements, "total_time"},
		// Case 2: blk_read_time / blk_write_time → shared_* at PG17.
		{ViewStatStatements, "blk_read_time"},
		{ViewStatStatements, "blk_write_time"},
		// Case 3: op_bytes → read/write/extend_bytes at PG18.
		{ViewStatIO, "op_bytes"},
		// Case 4a: pg_stat_bgwriter 11 → 4 at PG17. Five columns move to
		// pg_stat_checkpointer, two are deleted, four stay (the four staying is
		// not a succession — it is the column set).
		{ViewStatBgwriter, "checkpoints_timed"},
		{ViewStatBgwriter, "checkpoints_req"},
		{ViewStatBgwriter, "checkpoint_write_time"},
		{ViewStatBgwriter, "checkpoint_sync_time"},
		{ViewStatBgwriter, "buffers_checkpoint"},
		{ViewStatBgwriter, "buffers_backend"},
		{ViewStatBgwriter, "buffers_backend_fsync"},
		// Case 4b: pg_stat_wal 9 → 5 at PG18. Four columns removed, no successors.
		{ViewStatWAL, "wal_write"},
		{ViewStatWAL, "wal_sync"},
		{ViewStatWAL, "wal_write_time"},
		{ViewStatWAL, "wal_sync_time"},
	}
	have := map[Source]bool{}
	for _, s := range Successions() {
		have[s.From] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("no succession row for %s (R2.4's enumerated cases)", w)
		}
	}
	if len(successions) != len(want) {
		t.Errorf("%d succession rows, want %d: the table holds exactly R2.4's cases, "+
			"so a new row needs a reason in the requirement or in a note", len(successions), len(want))
	}
}

func TestSuccessorOfUnknown(t *testing.T) {
	// The common case: a column with no history. pg_stat_archiver has been the
	// same seven columns on all six majors, so nothing is recorded for it.
	if _, ok := SuccessorOf(ViewStatArchiver, "archived_count"); ok {
		t.Error("pg_stat_archiver.archived_count has a recorded successor; it never changed")
	}
	if _, ok := SuccessorOf(View("pg_nope"), "x"); ok {
		t.Error("SuccessorOf on an unknown view returned true")
	}
}

func TestSuccessionsReturnsCopy(t *testing.T) {
	got := Successions()
	if len(got) == 0 {
		t.Fatal("Successions() empty")
	}
	first := got[0]
	got[0] = Succession{}
	if Successions()[0].From != first.From {
		t.Error("mutating the slice returned by Successions() changed the package's table")
	}
}

// TestSuccessionSuccessorsExist is the narrow version of
// ValidateSuccessions's cross-check, spelled per kind so a future entry that
// mislabels a split as a rename fails with a message that says which.
func TestSuccessionSuccessorsExist(t *testing.T) {
	for _, s := range Successions() {
		switch s.Kind {
		case KindRetire:
			if len(s.To) != 0 {
				t.Errorf("%s: retire with %d successors", s.From, len(s.To))
			}
		case KindSplit:
			if len(s.To) < 2 {
				t.Errorf("%s: split with %d successors", s.From, len(s.To))
			}
		case KindRename, KindMove:
			if len(s.To) != 1 {
				t.Errorf("%s: %s with %d successors", s.From, s.Kind, len(s.To))
			}
		}
		for _, to := range s.To {
			if _, ok := Columns(to.View, s.Since); !ok {
				t.Errorf("%s since %d: successor %s does not exist on %d", s.From, s.Since, to, s.Since)
			}
		}
	}
}
