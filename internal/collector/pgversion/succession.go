// succession.go is the half of R2.4 that column sets alone cannot answer.
//
// R2.4's wording is "per-view column sets per major, **not a rename table**",
// and the reason is the bgwriter case: a table of renames written at PG17
// records `pg_stat_bgwriter` shrinking 11 columns to 4 and loses the four
// checkpoint families that moved to `pg_stat_checkpointer`, because a rename
// table has no way to say "these five columns moved to a different view *and*
// these two were deleted *and* these four stayed". The column sets in
// matrix_data.go answer that per view.
//
// What they cannot answer is *identity*: that `pg_stat_statements.total_time`
// on PG12 and `total_exec_time` on PG13 are the same measurement, so a 12→13
// upgrade is not a new query and a metric family must not restart. That is what
// this file is for — the rename/move/split/retire cases, as data, next to the
// sets they describe rather than replacing them.
//
// It deliberately does not name metrics. R5.2 names metric *families* in prose
// and R9.4 fixes the names, both of which are Phase 2 step 4's work; minting
// metric names here would put names in front of the contract that mints them
// (R14.7's "never add a metric that isn't in the docs"). The (view, column)
// pair is the stable identity; the metric name is a function of it that step 4
// writes.

package pgversion

import (
	"fmt"
	"strings"
)

// Source is one (view, column) pair — the identity a metric is derived from.
type Source struct {
	View   View
	Column string
}

// String renders the pair the way SQL spells it, for error messages and for the
// R9.4 review's cross-check list.
func (s Source) String() string { return string(s.View) + "." + s.Column }

// Kind is how a column changed between majors. The four values are exactly the
// change classes R2.4 enumerates; anything else is a plain addition, which is
// not a succession (a new column has no history to carry — the column set is
// where an addition shows up).
type Kind string

const (
	// KindRename is the same column, renamed, in the same view.
	KindRename Kind = "rename"
	// KindMove is the same measurement, now published by a different view.
	// R2.4's checkpoint case: bgwriter's checkpoint counters move to
	// pg_stat_checkpointer while bgwriter itself keeps four columns.
	KindMove Kind = "move"
	// KindSplit is the case where one column becomes several, each keeping part
	// of the old measurement. R2.4's op_bytes case at PG18.
	KindSplit Kind = "split"
	// KindRetire is a column that disappears with no successor. A retire is a
	// succession because the answer to "what happened to wal_write" must be
	// "nothing, it is gone" rather than "not in the table".
	KindRetire Kind = "retire"
)

// Succession is one column's history: what it was, what replaced it, and from
// which major.
//
// The shape is one row per *source* column with a list of successors, because
// that is the only shape that expresses all four kinds with one type: a rename
// has one To, a split has several, a move has one in a different view, and a
// retire has none. The alternative — a flat pair list — cannot express a retire
// at all and has to duplicate the source row three times for a split.
type Succession struct {
	// From is the column as it was called before Since.
	From Source
	// To is what the column is called from Since on. Empty iff Kind is
	// KindRetire; Validate enforces that pairing.
	To []Source
	// Since is the major the change took effect in — the first major where `To`
	// is the correct spelling and `From` is not.
	Since int
	// Kind classifies the change.
	Kind Kind
	// Note carries what the four fields cannot: why the change happened, or
	// where the measurement went when the answer is "a different view, for a
	// different reason" (the retired bgwriter counters live on in pg_stat_io,
	// which is not a successor of theirs so much as a replacement for the whole
	// family — a distinction R3.2's derivations depend on).
	Note string
}

// successions is R2.4's enumerated list, plus the retires its two shrink cases
// imply.
//
// "Among others" is R2.4's own wording, so this is not claimed to be exhaustive
// for every column PostgreSQL ever renamed. It is exhaustive for the four cases
// R2.4 names, and Validate cross-checks every entry here against the column
// sets in matrix_data.go — a succession that disagrees with the table is an
// error, which is what keeps this file from drifting away from the generated
// half.
var successions = []Succession{
	// R2.4's first enumerated case.
	{
		From:  Source{ViewStatStatements, "total_time"},
		To:    []Source{{ViewStatStatements, "total_exec_time"}},
		Since: Major13,
		Kind:  KindRename,
		Note: "PG12's total_time became total_exec_time in PG13. R3.2 ranks the " +
			"top-N table by total_exec_time on every supported major; on a 12→13 " +
			"upgrade the two are one series, not two. The table starts at 13, so " +
			"the pre-image is documented rather than present — matrix_test.go " +
			"asserts the PG13 side from the docs.",
	},
	// R2.4's second enumerated case, both halves of the pair.
	{
		From:  Source{ViewStatStatements, "blk_read_time"},
		To:    []Source{{ViewStatStatements, "shared_blk_read_time"}},
		Since: Major17,
		Kind:  KindRename,
		Note: "R2.4 and R3.2 both pair these two explicitly (`blk_read_time` ≤PG16, " +
			"`shared_blk_read_time` PG17+) because R3.2's Load-by-IO derivation " +
			"has to pick one per major. PG17 also adds local_blk_read_time as a " +
			"separate column — an addition, not part of this rename: shared_* is " +
			"the successor of the old unqualified column, and the local_* family is " +
			"new data. Modelling the pair as a split would double-count.",
	},
	{
		From:  Source{ViewStatStatements, "blk_write_time"},
		To:    []Source{{ViewStatStatements, "shared_blk_write_time"}},
		Since: Major17,
		Kind:  KindRename,
		Note:  "The write half of the same PG17 pair; see the read half.",
	},
	// R2.4's third enumerated case: one column becomes three.
	{
		From: Source{ViewStatIO, "op_bytes"},
		To: []Source{
			{ViewStatIO, "read_bytes"},
			{ViewStatIO, "write_bytes"},
			{ViewStatIO, "extend_bytes"},
		},
		Since: Major18,
		Kind:  KindSplit,
		Note: "R2.4 names this one explicitly (`op_bytes removed from pg_stat_io at " +
			"PG18`). A rename table records it as a deletion and the three new " +
			"columns as three additions, which loses the fact that the old metric's " +
			"meaning is now the sum of three — the mistake R9.4's naming review " +
			"exists to catch. pg_stat_io itself only exists from PG16, so there is " +
			"no pre-16 spelling to worry about.",
	},
	// R2.4's fourth case, and the one it calls out as the case a rename table
	// gets wrong. pg_stat_bgwriter at PG17 keeps exactly four columns
	// (buffers_clean, maxwritten_clean, buffers_alloc, stats_reset — R2.4 lists
	// these four by name and the generated set for 17 matches them exactly),
	// five columns move to pg_stat_checkpointer, and two are deleted.
	{
		From:  Source{ViewStatBgwriter, "checkpoints_timed"},
		To:    []Source{{ViewStatCheckpointer, "num_timed"}},
		Since: Major17,
		Kind:  KindMove,
		Note:  "One of five bgwriter columns that move to pg_stat_checkpointer at PG17.",
	},
	{
		From:  Source{ViewStatBgwriter, "checkpoints_req"},
		To:    []Source{{ViewStatCheckpointer, "num_requested"}},
		Since: Major17,
		Kind:  KindMove,
		Note: "Renamed on the way, which is why the matrix needs both halves: the " +
			"column set says pg_stat_checkpointer has num_requested, only this row " +
			"says it is bgwriter's checkpoints_req.",
	},
	{
		From:  Source{ViewStatBgwriter, "checkpoint_write_time"},
		To:    []Source{{ViewStatCheckpointer, "write_time"}},
		Since: Major17,
		Kind:  KindMove,
		Note:  "One of five bgwriter columns that move to pg_stat_checkpointer at PG17.",
	},
	{
		From:  Source{ViewStatBgwriter, "checkpoint_sync_time"},
		To:    []Source{{ViewStatCheckpointer, "sync_time"}},
		Since: Major17,
		Kind:  KindMove,
		Note:  "One of five bgwriter columns that move to pg_stat_checkpointer at PG17.",
	},
	{
		From:  Source{ViewStatBgwriter, "buffers_checkpoint"},
		To:    []Source{{ViewStatCheckpointer, "buffers_written"}},
		Since: Major17,
		Kind:  KindMove,
		Note: "Renamed on the way too. R2.4's point is that a rename table keeps " +
			"bgwriter's four survivors and loses all five of these, so the " +
			"checkpoint metrics vanish from our PG17 clusters while bgwriter's " +
			"survivors look fine.",
	},
	{
		From:  Source{ViewStatBgwriter, "buffers_backend"},
		To:    nil,
		Since: Major17,
		Kind:  KindRetire,
		Note: "Deleted at PG17, not moved: since PG16 the per-backend write counts " +
			"come from pg_stat_io (context=normal, backend_type=client backend), " +
			"which is a replacement for the whole bgwriter write family rather than " +
			"a successor of this column. The distinction matters to R3.2's " +
			"derivations, which is why it is in the note and not in a To entry.",
	},
	{
		From:  Source{ViewStatBgwriter, "buffers_backend_fsync"},
		To:    nil,
		Since: Major17,
		Kind:  KindRetire,
		Note:  "Deleted at PG17 alongside buffers_backend; see that entry's note.",
	},
	// R2.4's fifth enumerated case: pg_stat_wal shrinking 9 columns to 5 at PG18.
	// The four that go are the write/sync half of the view — pg_stat_io has
	// covered them per object since PG16.
	{
		From:  Source{ViewStatWAL, "wal_write"},
		To:    nil,
		Since: Major18,
		Kind:  KindRetire,
		Note: "One of four pg_stat_wal columns removed at PG18 (R2.4's 9→5 case). " +
			"The survivors are wal_records, wal_fpi, wal_bytes, wal_buffers_full and " +
			"stats_reset; the write/sync counts moved into pg_stat_io's per-object " +
			"write_time/fsync_time, which is why they are retired here rather than " +
			"moved — pg_stat_wal and pg_stat_io measure different things and the " +
			"PG18 view did not gain a column that stands in for wal_write.",
	},
	{
		From:  Source{ViewStatWAL, "wal_sync"},
		To:    nil,
		Since: Major18,
		Kind:  KindRetire,
		Note:  "One of four pg_stat_wal columns removed at PG18; see wal_write.",
	},
	{
		From:  Source{ViewStatWAL, "wal_write_time"},
		To:    nil,
		Since: Major18,
		Kind:  KindRetire,
		Note:  "One of four pg_stat_wal columns removed at PG18; see wal_write.",
	},
	{
		From:  Source{ViewStatWAL, "wal_sync_time"},
		To:    nil,
		Since: Major18,
		Kind:  KindRetire,
		Note:  "One of four pg_stat_wal columns removed at PG18; see wal_write.",
	},
}

// successionsByFrom indexes successions by their source pair. Built at init so
// a duplicate source is a startup-time finding rather than a lookup that
// silently returns whichever copy the map kept.
var successionsByFrom = func() map[Source]Succession {
	m := make(map[Source]Succession, len(successions))
	for _, s := range successions {
		m[s.From] = s
	}
	return m
}()

// successionsByTo is the mirror index: every successor pair → the successions
// that produced it.
//
// It exists because the From index alone answers only the forward question —
// "what did this column become?" — so a pair that exists only as a successor
// answers "never was a thing". Resolve asking for
// pg_stat_statements.shared_blk_read_time on PG16 gets nothing from the From
// index (PG17 renamed blk_read_time *to* it), when the answer on PG16 is
// blk_read_time. Same shape for the move (pg_stat_checkpointer.num_timed on
// PG16 is pg_stat_bgwriter.checkpoints_timed) and the split
// (pg_stat_io.read_bytes on PG17 is op_bytes).
//
// The value is a []Succession and not the []Source the mirror suggests, because
// the answer needs the succession, not just the pair: the split case is lossy
// and only the row's Kind says so (see Resolution). A slice because a chain —
// A renamed to B, B later renamed to C — puts B in the index as the To of one
// row and the From of another, and the correct back-answer on a major before the
// first rename is A, which means choosing the earliest Since, not the first
// entry the map happens to keep.
var successionsByTo = func() map[Source][]Succession {
	m := make(map[Source][]Succession)
	for _, s := range successions {
		for _, to := range s.To {
			m[to] = append(m[to], s)
		}
	}
	return m
}()

// Successions returns the table itself, for the R9.4 naming review and for
// tests. The slice is a copy of the rows (the To slices are shared, being
// read-only by convention) so a caller cannot append into the package's table.
func Successions() []Succession {
	out := make([]Succession, len(successions))
	copy(out, successions)
	return out
}

// SuccessorOf returns the recorded history of one (view, column) pair, and
// false if the column has none — which is the common case: most columns are
// simply the same column on every major they exist on.
func SuccessorOf(view View, col string) (Succession, bool) {
	s, ok := successionsByFrom[Source{view, col}]
	return s, ok
}

// Resolution is Resolve's answer: the sources to read for one measurement on one
// major, and how much the caller can trust the pairing.
//
// The second half is why Resolve does not return a bare []Source. Most answers
// are exact and there is nothing to say about them; the ones that come from the
// history table have to say *how* they were arrived at, and the one case where
// the answer is not the measurement asked for — a back-resolved split — has to
// say so, because a caller that reads pg_stat_io.op_bytes on PG17 in place of
// read_bytes is reading a number that also contains write and extend. Presenting
// that pair silently is worse than presenting nothing: it is a plausible wrong
// number, which is the failure class R2.4 exists to remove.
type Resolution struct {
	// Sources are the concrete (view, column) pairs to SELECT. One most of the
	// time, several on a split, and empty exactly for a retire — the honest "this
	// measurement no longer exists", which renders as n/a rather than as a zero.
	Sources []Source
	// Kind classifies the change that produced Sources, and is "" when the answer
	// was the column exactly as asked for (no change had happened on this major).
	Kind Kind
	// Faithful reports whether Sources measures the measurement that was asked
	// about, and not something that contains it. False in exactly one case: a
	// back-resolved split, where the pre-image is the sum of several successors
	// and the question named one of them.
	Faithful bool
	// Note carries the succession's note whenever the answer came from the table,
	// so the explanation of the pairing travels with the pairing instead of
	// having to be looked up a second time.
	Note string
}

// Resolve answers the question a collector actually asks: "I want this logical
// measurement, on this major — what do I SELECT?"
//
// The four cases it has to get right are the reason the two files exist together:
//
//   - The column exists as-named on major → that one source. (The overwhelmingly
//     common case, and the one that must not be slowed down by the history table.)
//   - The column was renamed/moved/split at or before major → its successors.
//     Asking for pg_stat_statements.blk_read_time on PG17 yields
//     shared_blk_read_time; asking for pg_stat_io.op_bytes on PG18 yields three
//     columns; asking for pg_stat_bgwriter.checkpoints_timed on PG17 yields
//     pg_stat_checkpointer.num_timed — a different view, which is why a
//     collector cannot just qualify the name out of matrix_data.go. A retire
//     lands here too: no sources, ok=true.
//   - The column is the *successor* half of a change that happens after major →
//     the pre-image. Asking for pg_stat_statements.shared_blk_read_time on PG16
//     yields blk_read_time, and asking for pg_stat_checkpointer.num_timed on PG16
//     yields pg_stat_bgwriter.checkpoints_timed. This direction is the one the
//     first version of Resolve could not answer, because the From index has no
//     entry for a pair that has only ever been a successor — so the honest answer
//     ("PG16 spells this blk_read_time") came out as ok=false ("never was a
//     thing"), which is how a PG16 cluster loses a metric step 4 is about to
//     mint a name for.
//   - Neither → ok=false, the pair was never a thing on this major. A pre-image
//     that predates the table (PG12's total_time) is out of scope by the same
//     argument it always was.
func Resolve(view View, col string, major int) (Resolution, bool) {
	src := Source{View: view, Column: col}

	// The exact answer, and the common one: the column is right there on this
	// major, so the history table has nothing to say about it.
	if cols, exists := Columns(view, major); exists {
		for _, c := range cols {
			if c == col {
				return Resolution{Sources: []Source{src}, Faithful: true}, true
			}
		}
	}

	// Forward: the pair existed before this major and changed at or before it, so
	// what it became is the answer.
	if s, ok := SuccessorOf(view, col); ok && s.Since <= major {
		// A split's To set is the whole successor set, so the old name still gets
		// everything the old name measured — faithful, even though the answer is
		// three sources for a question about one.
		//
		// followChain is what makes this the spelling on `major` rather than the
		// spelling on `s.Since` (D20's S5).
		return Resolution{Sources: followChain(s.To, major), Kind: s.Kind, Faithful: true, Note: s.Note}, true
	}

	// Backward: the pair is somebody's successor on a later major, so on this
	// major the measurement is spelled by that succession's From.
	if s, ok := predecessor(src, major); ok {
		return Resolution{
			Sources: []Source{s.From},
			Kind:    s.Kind,
			// The one unfaithful answer: back-resolving one arm of a split yields
			// the sum of all three arms. A rename and a move are 1:1 by definition,
			// and a retire has no To entry so it can never be reached from here.
			Faithful: s.Kind != KindSplit,
			Note:     s.Note,
		}, true
	}

	return Resolution{}, false
}

// followChain returns the set of sources `to` is spelled by on major, walking the
// succession table forward for as long as the changes happened at or before major.
//
// D20's S5: Resolve's forward path used to return s.To verbatim. That is the right
// answer only when no member of To changed again at or before major, and with a
// chain — A renamed to B at 15, B renamed to C at 17 — it is not: Resolve(A, 18)
// answered B, a column PG18 does not have, because the row said "B" and nothing
// asked whether B had a history of its own. The backward path already walks chains
// (predecessor picks the earliest Since); this is the forward half of the same rule.
//
// The table has no chains today — fifteen rows, and no pair is both a To and a From
// — so this changes no current answer, which is why S5 is filed as latent and not
// as a live bug. The test that pins it is succession_test.go's
// TestResolveSourcesExistOnMajor: a future matrix update that adds a chain cannot
// ship an answer that is not a column on the major it was asked about.
//
// The seen set bounds the walk. Validate's injectivity checks make a cycle the only
// way the walk fails to terminate, and a cycle in the table is a data bug that
// should not cost every caller an infinite loop.
func followChain(to []Source, major int) []Source {
	if len(to) == 0 {
		return nil // a retire: the empty answer stays nil, not an empty slice
	}

	out := make([]Source, 0, len(to))
	seen := make(map[Source]bool, len(to))
	queue := append([]Source(nil), to...)
	for len(queue) > 0 {
		src := queue[0]
		queue = queue[1:]
		if seen[src] {
			continue
		}
		seen[src] = true

		if s, ok := successionsByFrom[src]; ok && s.Since <= major {
			// This member changed again on or before major, so on major it is
			// spelled by its own successors and not by itself.
			queue = append(queue, s.To...)
			continue
		}
		out = append(out, src)
	}
	return out
}

// predecessor returns the succession whose To contains src and whose change has
// not happened yet on major, preferring the earliest such Since.
//
// Earliest-first is what makes a rename chain answer correctly: with A→B at 15
// and B→C at 17, asking for B on 14 must yield A (the spelling 14 used), not C's
// predecessor row and not nothing. A candidate whose From does not exist on
// major is skipped — back-resolving to a column that is not there either is the
// same "never was a thing" error the index exists to fix, and it is what keeps
// pg_stat_io.read_bytes on PG13 from answering pg_stat_io.op_bytes, which
// pg_stat_io does not have on 13 either.
func predecessor(src Source, major int) (Succession, bool) {
	var (
		best Succession
		have bool
	)
	for _, s := range successionsByTo[src] {
		if s.Since <= major {
			continue // the forward path already answered this one
		}
		if !exists(s.From, major) {
			continue
		}
		if !have || s.Since < best.Since {
			best, have = s, true
		}
	}
	return best, have
}

// exists reports whether one source pair is a real column of its view on major.
func exists(src Source, major int) bool {
	cols, ok := Columns(src.View, major)
	if !ok {
		return false
	}
	return contains(cols, src.Column)
}

// ValidateSuccessions cross-checks the succession table against the column
// table, and is what keeps this file honest about the generated one.
//
// Every claim here is checkable against matrix_data.go except one: a pre-image
// on a major the table does not cover (PG12's total_time). Validate checks the
// covered side — that `From` really was a column of `From.View` on major
// Since-1 whenever Since-1 is in the table — and matrix_test.go covers the
// uncovered side by asserting the PG13 set directly against the docs.
func ValidateSuccessions() error {
	var errs []string
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if len(successionsByFrom) != len(successions) {
		add("%d successions but %d distinct sources: a column has two history rows",
			len(successions), len(successionsByFrom))
	}

	// The mirror half of the same invariant: the From index is injective above,
	// and the To index must be too. The count that matters is the number of To
	// *entries* (a split contributes three), compared against the number of
	// distinct keys the index built — a pair that two successions both name as
	// their successor collapses to one key holding two rows, which Resolve's
	// back-direction copes with by taking the earliest, but which the table should
	// not contain in the first place. It also catches the failure the index exists
	// to fix: an entry the index lost is a pair Resolve would answer "never was a
	// thing" on every major before its rename.
	var toEntries int
	for _, s := range successions {
		toEntries += len(s.To)
	}
	if len(successionsByTo) != toEntries {
		add("%d successor entries across the table but %d distinct in the To index: a pair has two predecessors",
			toEntries, len(successionsByTo))
	}

	for _, s := range successions {
		where := fmt.Sprintf("%s since %d", s.From, s.Since)

		switch s.Kind {
		case KindRename, KindMove, KindSplit, KindRetire:
		default:
			add("%s: unknown kind %q", where, s.Kind)
		}

		if len(s.To) == 0 && s.Kind != KindRetire {
			add("%s: %s with no successors (a retire is the only kind with an empty To)",
				where, s.Kind)
		}
		if len(s.To) > 1 && s.Kind != KindSplit {
			add("%s: %d successors but kind %s (only a split has more than one)",
				where, len(s.To), s.Kind)
		}
		if s.Since < Major13 || s.Since > Major18 {
			add("%s: major %d is outside the matrix's 13–18", where, s.Since)
			continue
		}
		if strings.TrimSpace(s.Note) == "" {
			add("%s: empty note (every succession needs the reason, not just the pair)", where)
		}

		// The pre-image: checkable whenever the table covers major Since-1.
		if s.Since > Major13 {
			prev, ok := Columns(s.From.View, s.Since-1)
			if !ok {
				add("%s: %s has no columns on %d, so it cannot have been renamed from there",
					where, s.From.View, s.Since-1)
			} else if !contains(prev, s.From.Column) {
				add("%s: %s is not a column of %s on %d",
					where, s.From.Column, s.From.View, s.Since-1)
			}
		}

		// The successors: each must be a real column of its view on Since.
		for _, to := range s.To {
			cols, ok := Columns(to.View, s.Since)
			if !ok {
				add("%s: successor %s does not exist on %d", where, to, s.Since)
				continue
			}
			if !contains(cols, to.Column) {
				add("%s: successor %s is not a column of %s on %d",
					where, to.Column, to.View, s.Since)
			}
		}
	}

	switch len(errs) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("pgversion: %s", errs[0])
	default:
		return fmt.Errorf("pgversion: %d succession problems, first: %s", len(errs), errs[0])
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
