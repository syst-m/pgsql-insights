package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// CollectorLocks is §3.3's name for this collector, and the `collector` label
// value on both R2.2 families for its rounds.
//
// §3.3's row is the whole scope statement:
//
//	| `locks` | 30 s | S2 `pg_locks` | Feeds the blocking chain |
//
// which is one view, one source, one interval, and a Notes column naming exactly
// one consumer. Everything about how the SQL is written is R2.4, R2.5 and R2.7,
// and all three live in sql.go and the matrix — this file builds its query through
// `Project`, opens one read-only transaction through `SQLOpener`, and reports the
// round-trips it issued, exactly the way database.go does.
const CollectorLocks = "locks"

// LocksInterval is §3.3's interval for the locks collector (30 s), stated as a
// constant so R2.3's --interval.locks override has a declared default to override
// rather than a literal buried in a struct literal.
const LocksInterval = 30 * time.Second

// locksViewGroup is the collector's single R2.7 view group.
//
// One group holding one view is the shortest answer R2.7 can be given, and the one
// collector where "one transaction per view group" and "one transaction per
// collector" cannot disagree about the number. It is still named rather than
// implied, because Descriptor.ViewGroups is the field the framework reads to tell
// "one transaction" from "no transactions", and a SQL collector that leaves it
// empty is a collector that claims to issue no SQL.
const locksViewGroup = "locks"

// locksViews is §3.3's single source for this collector.
//
// pg_locks exists on every major the matrix knows (matrix_data.go:532 maps 13
// through 18), so AppliesToViews over this one view is AllMajors in effect — and
// still asked of the matrix rather than written as a range, which is the rule
// AppliesToViews' doc gives for why the answer is a func and not a field.
var locksViews = []pgversion.View{pgversion.ViewLocks}

// locksSpecs is the empty spec, and the emptiness is the content.
//
// pg_locks needs no Extras: every value R4.4 renders is a column. pg_database
// needed three because its sizes and XID ages are functions, not columns
// (pg_database_size(oid), age(datfrozenxid)), and pg_stat_activity needed one
// because pg_blocking_pids(pid) has no column form (activity.go's
// ColumnBlockingPids). Neither case exists here.
//
// It needs no WHERE clause either. pg_database's filter exists because
// pg_database_size(oid) requires CONNECT on that database, so a row the credential
// cannot read makes the *whole SELECT* throw rather than dropping one column
// (D20's S7); pg_locks has no per-row privilege gate — pg_monitor reads the entire
// view — so there is no row whose presence can fail the query that selects it.
//
// Declared as a map entry rather than passed as a zero QuerySpec at the call site
// so the file has one place where "this view has no extras and no filter" is
// written down, which is the same reason databaseSpecs is a map with one entry for
// a three-view collector.
var locksSpecs = map[pgversion.View]QuerySpec{
	pgversion.ViewLocks: {},
}

// LockRow is one row of pg_locks — the lock table R4.4 renders.
//
// The fields are the matrix's column set (LocksPG13's fifteen, LocksPG14to18's
// sixteen) carried as strings, with the two booleans as bools and the one
// timestamp as a time.Time. Strings for the identifiers is deliberate and it is
// the same rule database.go states for pg_database's oids: an oid is a join key,
// not a quantity, and formatting one as a number in Go invites arithmetic on it.
// The exception is Pid, which activity.go types as an int because there a pid *is*
// the row's identity and R4.8's test compares pids; here a pid is an attribute of
// a lock and is NULL for a lock no backend holds, so an int would have to invent a
// 0 that means both "shared lock" and "no such process".
//
// The row exists because R4.4's blocking chain needs it and cannot get it from
// activity.go. activity.go's doc states the split: pg_blocking_pids "is the
// pg_locks half" for the *edge*, and "locks/ is the lock *table*, which R4.4 needs
// for the lock mode and relation a session waits on, not for the edge". This file
// is that half.
type LockRow struct {
	// LockType is the kind of lockable object: relation, tuple, transactionid,
	// fastpath, advisory, and the rest of PG's set. It is the column that gives the
	// other columns their meaning — relation is NULL unless this is a relation
	// lock, page and tuple are only meaningful for a tuple lock — which is why it
	// is the field a row is dropped for (see lockFromRow) and the one a grouped
	// family would be named after.
	LockType string

	// Database is the oid of the database the lock belongs to, empty when the
	// server leaves it NULL (a lock on a shared object, or a transaction lock).
	// R9.3 names `datname` as the database family's label and this is a datid, so
	// it is carried as data and never becomes a label here — the label comes from
	// the Target, and activity.go carries the name.
	Database string
	// Relation is the oid of the locked relation, empty unless LockType is
	// `relation` (or a partitioned index's child). Cloud SQL's blocked-query tree
	// renders this as the object being waited on, which is why R4.4 asks for the
	// table and not only the edge.
	Relation string
	// Page and Tuple locate the locked row for a tuple lock and are empty
	// otherwise. Both are block-locator halves: neither means anything on its own,
	// which is the reason neither becomes a metric.
	Page  string
	Tuple string

	// VirtualXID and TransactionID are the two transaction-lock identities: the
	// holder's virtual transaction id and, for a lock on a real transaction id,
	// the id itself. Exactly one of the two is set on a transaction lock, and
	// VirtualTransaction is set on every row regardless of lock type.
	VirtualXID         string
	TransactionID      string
	VirtualTransaction string
	// ClassID and ObjID are the (class, object) pair identifying a lockable object
	// inside a system catalog — for a transactionid lock, classid 0 and objid the
	// xid — and ObjSubID is the sub-object. All three are empty for lock types the
	// pair does not describe.
	ClassID  string
	ObjID    string
	ObjSubID string

	// Pid is the holding backend, empty for a lock no backend holds (a lock the
	// startup process holds, or a lock granted to no one). See the type doc for why
	// this is a string here and an int in Session.
	Pid string
	// Mode is the lock mode — AccessShare, RowExclusive, AccessExclusive and the
	// rest — and it is the half of R4.4's "the lock mode a session waits on" that
	// the edge list does not carry.
	Mode string

	// Granted is whether the backend holds the lock or is waiting for it. This is
	// the field that turns the table into a wait list, and it is the reason a
	// per-row metric is not possible — see LocksSamples.
	Granted bool
	// Fastpath is PG's fast-path lock flag: the lock is held and recorded only in
	// the backend's local array, not in the shared lock table. It is a real column
	// on every major in the matrix's set, including 18.
	Fastpath bool

	// WaitStart is when a waiting backend began waiting, and it is the field whose
	// absence is a version gap rather than a missing value: the matrix's own column
	// split — LocksPG13's fifteen columns against LocksPG14to18's sixteen — is the
	// proof that waitstart is PG14+. Lock wait *duration* is therefore not
	// derivable from pg_locks on PG13.
	//
	// That gap does not reach R3.7's `Load by lock wait` or R9.4's `lock_time`
	// member: §5's divergence table sources both from pg_stat_statements' own
	// lock-time counters at database/user level, not from pg_locks, so a PG13
	// cluster loses the per-lock "waiting since" column on the Locks page and
	// nothing else. A zero here means "no value" — the PG13 case, or a granted lock
	// the server leaves NULL — and clock.go's convention for a zero timestamp is
	// "not compared", not the epoch.
	WaitStart time.Time

	// SourceTime is the row's own pgsi_source_now, the instance clock every
	// timestamp on this row was measured against (R13.11). Project appends it to
	// every query, so it is present even on a view with no stats_reset — which
	// pg_locks has none of, the same way pg_settings has none (clock.go:64 names
	// the three views that lack one).
	SourceTime time.Time
}

// LocksCollector is §3.3's locks collector.
//
// The one judgement in the file is which columns become metrics, and the answer is
// none of them, which is the answer S50's fix taught rather than the one a first
// pass writes.
//
// R9.7's export set is "the union of R5.2's enumerated families and the numeric
// columns R2.4's matrix returns for the target major", and it adds that "R9.4's
// naming function must enumerate that union: R14.7 admits no metric that exists in
// code but not in docs, so exporting a column R9.4 never named is a violation".
// pg_locks's numeric columns for PG16 are database, relation, classid, objid,
// objsubid, page, tuple and pid — every one of them a row identifier, which
// database.go's classifier already rules out in general ("IDs are join keys and
// function arguments; exporting pg_database_datfrozenxid as a metric would put a
// number on a wire that only means something as age(datfrozenxid)"). A sum of page
// numbers is not a measurement of anything, and a per-row series for a relation oid
// is a number outside the row that named it.
//
// The two booleans are values — database.go: "a boolean is a value, and R9.3 has no
// boolean type, so parseValue maps it to 1 and 0" — and they are the whole problem.
// pg_locks has one row per lock held, tens to thousands on a busy instance, and
// R9.3's base label set (namespace, cluster, pod, instance_role) is identical on
// every one. That is D20's S8 class verbatim: "N rows against one unlabelled family
// is N samples with identical label sets: an invalid exposition whose surviving
// value is whichever row the driver returned last". The fix for that class, as
// ruled for pg_stat_wal, is the aggregate — "wal.go's answer is the cluster-wide
// sum over the rows". A sum over pg_locks's booleans is a *count*, and R9.7's
// rename rule ("drop the source prefix, keep the member name verbatim, add the unit
// suffix only where the source declares one") has no name for a count of a boolean:
// `pgsi_locks_granted` reads as a per-lock flag and means a fleet-wide tally.
// R5.2's family list — transactions, tuples, blocks, temp, deadlocks, backends by
// state and by application name, database sizes, TXID and MXID age, WAL,
// checkpoints — does not name a locks family at all.
//
// So a locks metric is a metric with no document behind it, which is the exact
// defect A3 filed as S50 and which 9ff60c6 deleted rather than document. The
// grouped alternative (a count by mode, a count by lock type — both of which
// satisfy R9.3's shape rule that a per-family label be a dimension the source view
// groups by and the family name name that dimension) is admissible in shape and
// still unnamed in every document, so it needs the same ruling.
//
// LocksSamples is where that ruling lands without this file having to guess: it is
// the named place the metrics half lives, it returns nil today, and adding a family
// later is a body here rather than a change to Collect. The rows are the half §3.3's
// Notes column and R4.4 both name, and they are stable under whatever the ruling
// says, which is why they are built now.
type LocksCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewLocksCollector builds the collector on top of an opener (R2.7's transaction
// source) and a config (R2.7's timeout, R2.8's application name, R13.11's clock),
// with every unset field resolved by SQLConfig.withDefaults.
func NewLocksCollector(opener SQLOpener, cfg SQLConfig) *LocksCollector {
	c := &LocksCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorLocks,
		Interval: LocksInterval,
		Source:   SourceSQL,
		Cost:     CostStandard,
		// CostStandard: one read-only transaction and one query, which is the
		// class's definition ("one read-only transaction per view group and
		// single-digit queries"). The argument for CostExpensive is the result set —
		// a lock table is one row per lock, so a busy instance returns thousands —
		// and it is the same argument activity.go accepted at 15 s. It does not
		// apply here: the class's third clause is about intervals short enough that
		// "the per-round cost is paid four times a minute", and 30 s is twice a
		// minute; the row count is bounded by max_locks_per_transaction times the
		// backends, not by data volume, and the query has no aggregate and no sort.
		AppliesTo: AppliesToViews(locksViews...),
		// Timeout unset: R2.1's 10 s default. Descriptor's rule is that a collector
		// whose SQL is known to be cheap does not set a shorter one, because the
		// timeout is a hang guard and R2.7's statement_timeout is the tuning knob.
		ViewGroups: []string{locksViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *LocksCollector) Descriptor() Descriptor {
	return c.desc
}

// Collect is the Collector interface method: the metrics half of R9.1's split.
//
// It runs the round and returns whatever LocksSamples derives from the rows, which
// is an empty set today for the reasons on the type doc. The rows come back from
// CollectLocks, which is what an API handler calls; Collect delegates to it and
// drops them, because a scheduler that publishes samples to a registry has nowhere
// to put a lock mode and R9.1 says it should not — "query text, plan bodies,
// active-session rows, blocking chains → API + store only".
//
// An empty sample set is a legal result and not a failed round: the scheduler
// publishes nothing new, the collector's R2.2 families still record the round, and
// R2.2's last-good rule is a property of not publishing rather than of the sample
// count.
func (c *LocksCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	rows, res, err := c.CollectLocks(ctx, tgt)
	if err != nil {
		return res, err
	}
	res.Samples = LocksSamples(rows, c.cfg.Now())
	return res, nil
}

// CollectLocks runs one round and returns the lock table plus the round's
// accounting.
//
// This is the entry point R9.1's API plane needs: R4.4's chain renders a blocked
// session's lock mode and relation, and the only way to answer that is the rows
// themselves. It is the same shape as activity.go's CollectSessions for the same
// reason — the interface method answers the metrics plane and a typed row method
// answers the API plane, and both come from one round so the two planes cannot
// disagree about which round they describe.
//
// The error paths return the round-trips issued so far, because R13.1's budget is
// measured from Result.RoundTrips and a collector that returns a zero alongside a
// partial failure makes a cluster that issued a query look like one that issued
// none.
func (c *LocksCollector) CollectLocks(ctx context.Context, tgt Target) ([]LockRow, Result, error) {
	pv, err := c.projection(tgt)
	if err != nil {
		// Zero is correct: building the SQL is what tells us there is nothing to
		// issue, and nothing was issued.
		return nil, Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		// Also zero: Begin is the round-trip that opens the transaction, and a
		// Begin that failed issued nothing.
		return nil, Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorLocks, err)
	}

	rows, qerr := tx.Query(ctx, pv.Query)
	if qerr != nil {
		// D20's S9: sql.go's Tx doc says "Rollback on every error path", and a
		// failed query is one. The error is wrapped, not discarded, because the
		// query's failure is the one worth reporting and the rollback's is its
		// consequence.
		_ = tx.Rollback()
		return nil, Result{RoundTrips: 1},
			fmt.Errorf("collector %s: query %s: %w", CollectorLocks, pv.view, qerr)
	}

	if cerr := tx.Commit(); cerr != nil {
		// The data was read; the transaction just failed to close. That is still a
		// failed round — a connection that cannot commit is a connection whose next
		// query is suspect — so the rows come back with the error and the scheduler
		// keeps serving the previous round.
		_ = tx.Rollback()
		return lockRows(pv, rows), Result{RoundTrips: 1},
			fmt.Errorf("collector %s: commit: %w", CollectorLocks, cerr)
	}

	return lockRows(pv, rows), Result{RoundTrips: 1}, nil
}

// projection builds the collector's one query for the target's major.
//
// Every failure here is a refusal to guess: an unknown major means the matrix has
// no column set to use (and the scheduler's own applicability filter should have
// kept the round from happening), and a view with no columns on this major means
// the collector's declared applicability and the matrix disagree.
//
// It returns a bare viewProjection rather than the slice database.go returns,
// because one view is one projection and a caller that has to index a slice of
// length one is a caller that can index the wrong one.
func (c *LocksCollector) projection(tgt Target) (viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return viewProjection{}, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorLocks, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		return viewProjection{}, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (%s has no column set there)",
			CollectorLocks, tgt.PGMajor, pgversion.ViewLocks)
	}

	p, ok := Project(pgversion.ViewLocks, tgt.PGMajor, locksSpecs[pgversion.ViewLocks])
	if !ok {
		return viewProjection{}, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d",
			CollectorLocks, pgversion.ViewLocks, tgt.PGMajor)
	}
	if err := validateProjection(p); err != nil {
		return viewProjection{}, err
	}
	return viewProjection{view: pgversion.ViewLocks, Projection: p}, nil
}

// LocksSamples is the metrics half of this collector, and it is empty.
//
// It exists as a function rather than as a comment because the S50 lesson is not
// "emit nothing" — it is "do not emit a number no document names". The day R9.7's
// union is ruled for pg_locks (a count by mode, a count by lock type, or the two
// booleans aggregated some other way), the answer is a body in this function, and
// Collect, CollectLocks, the tests and the registry all keep working unchanged.
//
// The alternative it replaces was a per-row sample per numeric column: eight
// numeric columns, one row per lock, one label set per instance — the S8 invalid
// exposition, and eight names R9.4 never minted, which is R14.7's violation on top
// of it.
func LocksSamples(rows []LockRow, now time.Time) []Sample {
	// rows is deliberately unread here: a derivation that ignores its input is
	// wrong in a way the reader can see, and one that reads it and returns nil is
	// wrong in a way they cannot. The parameter is kept because the day it is used
	// is the day this function's signature stops being a placeholder, and because
	// now is the R13.11 clock every future sample's timestamp comes from.
	_ = rows
	_ = now
	return nil
}

// lockRows turns one query's rows into lock rows.
//
// The columns come from the projection rather than from each row's map, for the two
// reasons database.go gives and both apply here: a map's iteration order is random,
// and a row missing a column must not silently shrink the fields a lock reports.
// Here the argument is stronger still — the projection's column set differs by
// major (fifteen on PG13, sixteen on PG14+), so the projection is the only place
// that knows which fields this row could have had.
func lockRows(pv viewProjection, rows []Row) []LockRow {
	var out []LockRow
	for _, row := range rows {
		l, ok := lockFromRow(pv, row)
		if !ok {
			continue
		}
		out = append(out, l)
	}
	return out
}

// lockFromRow parses one pg_locks row.
//
// It returns false when the row has no locktype. That is the only row this
// collector drops, and it is the analogue of activity.go's "no usable pid" and
// database.go's shared-objects rule (D20's S8): locktype is the column that gives
// every other column on the row its meaning — relation is only a relation oid if
// this is a relation lock, page and tuple are only locators if it is a tuple lock —
// so a row without one is not a lock, and keeping it would put a row into the table
// that the lock table does not have.
//
// It does not drop a row for an empty pid, which is the difference from
// activity.go: a pg_locks row with no pid is a real lock (a shared lock, or one the
// startup process holds), and R4.4's table has to render it.
func lockFromRow(pv viewProjection, row Row) (LockRow, bool) {
	get := func(col string) string {
		v, _ := row.Get(col)
		return v
	}

	lockType := get("locktype")
	if lockType == "" {
		return LockRow{}, false
	}

	l := LockRow{
		LockType:           lockType,
		Database:           get("database"),
		Relation:           get("relation"),
		Page:               get("page"),
		Tuple:              get("tuple"),
		VirtualXID:         get("virtualxid"),
		TransactionID:      get("transactionid"),
		VirtualTransaction: get("virtualtransaction"),
		ClassID:            get("classid"),
		ObjID:              get("objid"),
		ObjSubID:           get("objsubid"),
		Pid:                get("pid"),
		Mode:               get("mode"),
		WaitStart:          zeroIfEmpty(get("waitstart")),
		SourceTime:         zeroIfEmpty(get(SourceNowColumn)),
	}
	// The two booleans go through parseValue rather than a string compare, which is
	// the one place this file reuses sql.go's value parser: it accepts both the
	// wire format ("t"/"f") and the stringified one ("true"/"false") that some
	// drivers return, and a compare against "t" alone silently reports every
	// lock as not granted when the driver strings the value.
	if v, ok := parseValue(get("granted")); ok {
		l.Granted = v != 0
	}
	if v, ok := parseValue(get("fastpath")); ok {
		l.Fastpath = v != 0
	}
	return l, true
}

// zeroIfEmpty parses a timestamp column that may be NULL, returning the zero
// time.Time for an empty string.
//
// It exists because parseTimestamp already returns false for "" but a caller that
// ignores the bool has to write the zero explicitly, and because both timestamp
// fields on LockRow have a documented "no value" case (waitstart on PG13 and for
// granted locks; SourceTime when the driver returns something unrecognised) that
// the zero value carries.
func zeroIfEmpty(raw string) time.Time {
	t, _ := parseTimestamp(raw)
	return t
}
