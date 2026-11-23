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

// Resolve answers the question a collector actually asks: "I want this logical
// measurement, on this major — what do I SELECT?"
//
// It returns the concrete sources to read, which is one source most of the time
// and several on a split. The three cases it has to get right are the reason
// the two files exist together:
//
//   - The column exists as-named on major → that one source. (The overwhelmingly
//     common case, and the one that must not be slowed down by the history table.)
//   - The column was renamed/moved/split at or before major → its successors.
//     Asking for pg_stat_statements.blk_read_time on PG17 yields
//     shared_blk_read_time; asking for pg_stat_io.op_bytes on PG18 yields three
//     columns; asking for pg_stat_bgwriter.checkpoints_timed on PG17 yields
//     pg_stat_checkpointer.num_timed — a different view, which is why a
//     collector cannot just qualify the name out of matrix_data.go.
//   - The column was retired at or before major → no sources and ok=true, the
//     honest "this measurement no longer exists", which renders as n/a rather
//     than as a zero. ok=false means the pair was never a thing at all.
func Resolve(view View, col string, major int) ([]Source, bool) {
	cols, exists := Columns(view, major)
	if exists {
		for _, c := range cols {
			if c == col {
				return []Source{{view, col}}, true
			}
		}
	}
	s, ok := SuccessorOf(view, col)
	if !ok {
		return nil, false
	}
	if s.Since > major {
		// The change hasn't happened yet on this major, and the column isn't
		// there (the loop above already said so) — so this pair is not a thing
		// on this major. A pre-image that predates the table (PG12's
		// total_time) is out of scope by the same argument.
		return nil, false
	}
	return s.To, true
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
