package pgversion

// succession_test.go covers the rename/move/split/retire half of R2.4. The
// tests were written in the same commit as the table (R16.1's rule for the
// matrix applies to its second file too) and the two central ones are generic
// over the whole succession table rather than sampling it:
// TestResolveAcrossAllMajors asks about every *From* pair on every major,
// TestResolveBackwardsAcrossAllMajors about every *To* pair. Together they are
// what catches a `Since` off by one — the realistic bug here, and the one a
// sampled test misses — and the second is what catches a Resolve that only
// answers the forward direction, which the first one structurally cannot see
// (see the comment on TestResolveBackwardsAcrossAllMajors).

import (
	"fmt"
	"reflect"
	"testing"
)

// TestResolveAcrossAllMajors walks every succession's *From* pair × every major.
//
// The rule it asserts is the one Resolve documents:
//
//	major >= Since, pair exists on major → the succession's To (empty for a
//	                                       retire, but ok=true)
//	major <  Since, view has the column on that major → the column itself
//	major <  Since, view not there at all             → nothing, ok=false
func TestResolveAcrossAllMajors(t *testing.T) {
	for _, s := range Successions() {
		for _, major := range Majors() {
			got, ok := Resolve(s.From.View, s.From.Column, major)
			cols, viewExists := Columns(s.From.View, major)
			var want Resolution
			switch {
			case major >= s.Since:
				want = Resolution{Sources: s.To, Kind: s.Kind, Faithful: true, Note: s.Note}
			case viewExists && hasCol(cols, s.From.Column):
				// The change has not happened on this major yet, so the answer is
				// the column exactly as asked for and there is nothing to say
				// about how it was arrived at: no Kind, no Note.
				want = Resolution{Sources: []Source{s.From}, Faithful: true}
			default:
				// The view itself is not there on this major (pg_stat_io before
				// PG16) or the pre-image predates the table (PG12's total_time).
				if ok {
					t.Errorf("Resolve(%s, %d) = (%+v, true), want not-found", s.From, major, got)
				}
				continue
			}
			if !ok {
				t.Errorf("Resolve(%s, %d) not found, want %+v", s.From, major, want)
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Resolve(%s, %d) = %+v, want %+v", s.From, major, got, want)
			}
		}
	}
}

// TestResolveBackwardsAcrossAllMajors is the half the first version of this file
// could not ask.
//
// TestResolveAcrossAllMajors walks the table's *From* pairs, so every question it
// asks is one the From index answers — which makes a missing back-direction
// structurally invisible: the suite cannot see the gap because it never asks the
// question that exposes it. That is exactly how Resolve shipped answering
// "never was a thing" for pg_stat_statements.shared_blk_read_time on PG16, where
// the truth is blk_read_time, and step 4's R9.4 naming function would have
// inherited it.
//
// So this test walks the *To* pairs: every successor pair in the table, on every
// major. The expectation is derived from the two tables in Resolve's documented
// order (see wantResolve), not from the row being iterated, because for a To pair
// the row being iterated is not always the one that answers — on its own major the
// answer is the pair itself, and only before that major is it the row's From.
func TestResolveBackwardsAcrossAllMajors(t *testing.T) {
	for _, s := range Successions() {
		for _, to := range s.To {
			for _, major := range Majors() {
				got, ok := Resolve(to.View, to.Column, major)
				want, wantOK := wantResolve(to.View, to.Column, major)
				if ok != wantOK {
					t.Errorf("Resolve(%s, %d) ok = %v, want %v (%+v)", to, major, ok, wantOK, want)
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("Resolve(%s, %d) = %+v, want %+v", to, major, got, want)
				}
			}
		}
	}
}

// wantResolve derives Resolve's answer for one pair on one major from the two
// tables, in the order Resolve documents them. It is deliberately a second,
// smaller statement of the rule rather than a call into Resolve: a test that asks
// the function to check itself catches regressions but not a wrong rule, and the
// rule is the part S1 was about.
//
//  1. the matrix has the pair on major → the pair itself, no Kind, faithful;
//  2. else the pair has a history row that already happened on major → that row's
//     To set, its Kind, faithful (a split's To set is the whole successor set, so
//     the old name still measures what the old name measured);
//  3. else the earliest row whose To contains the pair, changing on a *later*
//     major and whose From exists on major → that row's From, its Kind, and
//     Faithful only if the change was not a split (back-resolving one arm of a
//     split yields the sum of all its arms);
//  4. else not found.
func wantResolve(view View, col string, major int) (Resolution, bool) {
	src := Source{View: view, Column: col}
	if cols, ok := Columns(view, major); ok {
		for _, c := range cols {
			if c == col {
				return Resolution{Sources: []Source{src}, Faithful: true}, true
			}
		}
	}
	if s, ok := SuccessorOf(view, col); ok && s.Since <= major {
		return Resolution{Sources: s.To, Kind: s.Kind, Faithful: true, Note: s.Note}, true
	}
	var (
		best Succession
		have bool
	)
	for _, s := range Successions() {
		// Only the To half matters here: the From half is answered by the two
		// checks above (the matrix itself, then a row that already happened).
		var isTo bool
		for _, to := range s.To {
			if to == src {
				isTo = true
			}
		}
		if !isTo || s.Since <= major || !hasColumn(s.From, major) {
			continue
		}
		if !have || s.Since < best.Since {
			best, have = s, true
		}
	}
	if !have {
		return Resolution{}, false
	}
	return Resolution{
		Sources:  []Source{best.From},
		Kind:     best.Kind,
		Faithful: best.Kind != KindSplit,
		Note:     best.Note,
	}, true
}

// hasColumn is the test-side spelling of "the matrix has this pair on this major".
func hasColumn(src Source, major int) bool {
	cols, ok := Columns(src.View, major)
	return ok && hasCol(cols, src.Column)
}

// TestResolveSourcesExistOnMajor is D20's S5's invariant stated as a test: every
// source in every answer Resolve gives must be a column the matrix actually has on
// the major it was asked about.
//
// The two generic tests above assert the *expected* answer per table row, so they
// catch an answer that disagrees with the table; this catches the class of answer the
// table can produce and still not check — a forward answer naming a column that
// existed on the major the change happened on but not on the major asked about. With
// a chain (A→B at 15, B→C at 17) the old forward path answered Resolve(A, 18) with
// B, which PG18 does not have. followChain is the fix; this is the test that stops
// the next chain from reintroducing it, and it is the one test here that inspects
// Resolve's output rather than comparing it to a remembered expectation.
//
// It walks both halves of the table — every From pair and every To pair, on all six
// majors — because the forward and backward paths build Sources from different
// indexes and each has its own way of naming something that is not there.
func TestResolveSourcesExistOnMajor(t *testing.T) {
	var pairs []Source
	for _, s := range Successions() {
		pairs = append(pairs, s.From)
		pairs = append(pairs, s.To...)
	}
	if len(pairs) == 0 {
		t.Fatal("no pairs to check")
	}

	checked := 0
	for _, src := range pairs {
		for _, major := range Majors() {
			got, ok := Resolve(src.View, src.Column, major)
			if !ok {
				continue // "never was a thing" has no sources to be wrong about
			}
			checked++
			for _, s := range got.Sources {
				if !hasColumn(s, major) {
					t.Errorf("Resolve(%s, %d) = %+v: %s is not a column of %s on %d (Kind %q)",
						src, major, got, s, s.View, major, got.Kind)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no Resolve answers found, so nothing was checked")
	}
}

// TestResolveNamedCases pins the answers worth naming. The two generic tests
// cover every pair on every major; this one exists so the interesting shapes are
// in a failure message as cases rather than reconstructed from a loop index — and
// so the one answer that is *not* what was asked for (case 5) has a test whose
// name says which field it is checking.
func TestResolveNamedCases(t *testing.T) {
	cases := []struct {
		view    View
		column  string
		major   int
		found   bool
		sources []Source
		kind    Kind
		// faithful is checked only when found; it is the field S1 added.
		faithful bool
	}{
		// Forward, R2.4's case 2: PG17 spells blk_read_time shared_blk_read_time.
		{ViewStatStatements, "blk_read_time", Major17, true,
			[]Source{{ViewStatStatements, "shared_blk_read_time"}}, KindRename, true},
		// Backward, the same row read from the other side: on PG16 the measurement
		// named shared_blk_read_time in PG17 is blk_read_time. This is the answer
		// the first version of Resolve could not give.
		{ViewStatStatements, "shared_blk_read_time", Major16, true,
			[]Source{{ViewStatStatements, "blk_read_time"}}, KindRename, true},
		// Backward across a move: pg_stat_checkpointer.num_timed on PG16 is
		// pg_stat_bgwriter.checkpoints_timed — a different view, so a collector
		// that only qualifies names out of the matrix cannot find it.
		{ViewStatCheckpointer, "num_timed", Major16, true,
			[]Source{{ViewStatBgwriter, "checkpoints_timed"}}, KindMove, true},
		// The same pair three majors earlier, proving the answer comes from the
		// table and not from a "one major back" rule.
		{ViewStatCheckpointer, "num_timed", Major13, true,
			[]Source{{ViewStatBgwriter, "checkpoints_timed"}}, KindMove, true},
		// Backward across a *split*: PG17's read_bytes is op_bytes, which is also
		// write_bytes and extend_bytes. The pairing is real and the number is not
		// the one asked for, so Faithful is false — the field that stops the
		// answer being passed off as a 1:1 source.
		{ViewStatIO, "read_bytes", Major17, true,
			[]Source{{ViewStatIO, "op_bytes"}}, KindSplit, false},
		// Forward across the same split: the old name's answer is all three arms,
		// which is everything the old name measured, so it is faithful even though
		// it is three sources for a question about one.
		{ViewStatIO, "op_bytes", Major18, true,
			[]Source{
				{ViewStatIO, "read_bytes"},
				{ViewStatIO, "write_bytes"},
				{ViewStatIO, "extend_bytes"},
			}, KindSplit, true},
		// R2.4's case 1, the 12→13 rename the matrix exists to remember.
		{ViewStatStatements, "total_time", Major13, true,
			[]Source{{ViewStatStatements, "total_exec_time"}}, KindRename, true},
		// A retire: no sources, ok=true. The empty Sources slice is the answer —
		// "this measurement no longer exists", which renders as n/a and not as a
		// zero — and it is the only kind with an empty To.
		{ViewStatWAL, "wal_write", Major18, true, nil, KindRetire, true},
		// Neither direction has anything to say: pg_stat_io does not exist on PG13
		// at all, so read_bytes is not a thing there — and the back-resolved
		// candidate (op_bytes, in the same view) is filtered because it is not on
		// 13 either. Answering pg_stat_io.op_bytes here would be the same "never
		// was a thing" error the To index exists to fix, wearing the index's
		// clothes.
		{ViewStatIO, "read_bytes", Major13, false, nil, "", false},
	}

	for _, tc := range cases {
		where := fmt.Sprintf("Resolve(%s, %s, %d)", tc.view, tc.column, tc.major)
		got, ok := Resolve(tc.view, tc.column, tc.major)
		if ok != tc.found {
			t.Errorf("%s ok = %v, want %v (%+v)", where, ok, tc.found, got)
			continue
		}
		if !ok {
			continue
		}
		if !reflect.DeepEqual(got.Sources, tc.sources) {
			t.Errorf("%s Sources = %v, want %v", where, got.Sources, tc.sources)
		}
		if got.Kind != tc.kind {
			t.Errorf("%s Kind = %q, want %q", where, got.Kind, tc.kind)
		}
		if got.Faithful != tc.faithful {
			t.Errorf("%s Faithful = %v, want %v", where, got.Faithful, tc.faithful)
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
