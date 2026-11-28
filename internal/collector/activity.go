package collector

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// CollectorActivity is §3.3's name for this collector, and the `collector` label
// value on its rounds.
//
// §3.3's row is:
//
//	| `activity` | 15 s | S2 `pg_stat_activity` | The Active Queries feed; also our wait-event and blocking sample |
//
// which is one view, one source, one interval, and a third column that names
// three product surfaces at once: the session table (R4.1), the wait-event
// analysis (R4.3) and the blocking chain (R4.4). All three are computed here from
// the one query, and the reason they are computed rather than stored is R4.8's:
// the acceptance is value equality against a direct SELECT, and a chain or a
// scorecard derived from a *different* read of pg_stat_activity than the rows it
// describes cannot be checked against one.
const CollectorActivity = "activity"

// ActivityInterval is §3.3's and R4.2's interval for the activity collector
// (15 s).
//
// R4.2 states the number twice — §3.3's table says 15 s and R4.2 says "Refresh
// the live list at 15 s (Cloud SQL refreshes at 60 s; we can afford to be fresher
// because the query is cheap)" — and then adds the half the table omits: "while
// keeping the 60 s server-side snapshot for history." That second clause is not
// this constant. It is the store's retention question (R8.11's one-minute bucket),
// and reading 15 s as the answer to it would put a live-list refresh cadence in
// the place a snapshot resolution belongs.
//
// The cost claim in the parenthetical is why CostCheap is the right Cost here and
// why this is the only SQL collector that does not use CostStandard: R4.2
// justifies the four-fold interval reduction on cheapness alone, and a collector
// that runs four times as often as its siblings while being described as cheaper
// than them is the one the cost table exists to distinguish.
const ActivityInterval = 15 * time.Second

// activityViewGroup is the collector's single R2.7 view group.
//
// One group, and on PG17+ that group holds two queries: pg_stat_activity and
// pg_wait_events. R2.7's rule is one transaction per *group*, not per query, so
// the second query costs a round-trip and not a transaction — which matters here
// for a reason beyond R13.1's budget: the two views have to agree. pg_wait_events
// is the catalogue of event names the server can report, and validating a
// session's wait_event against a catalogue read in a different transaction,
// seconds later, is validating it against a table that may have moved.
const activityViewGroup = "activity"

// activityViews is the set that drives the collector's applicability.
//
// pg_stat_activity alone, and pg_wait_events is deliberately absent. R4.3's own
// wording is "Use `pg_wait_events` (PG17+) to validate and label event names on
// versions that have it" — the parenthetical is the whole reason. AppliesToViews
// is the all-views rule (sql.go's doc says so outright: "the collector applies to
// a major only if *every* view it reads exists there"), so declaring
// pg_wait_events here would make the collector inapplicable on PG13 through PG16,
// where pg_stat_activity is perfectly readable and R4.1's table has always worked.
//
// So the view that exists on two majors is read conditionally, by asking the
// matrix, and the view that exists on six is the one that answers for the
// collector.
var activityViews = []pgversion.View{
	pgversion.ViewStatActivity,
}

// activitySpecs carries the one expression R4.4 forces into the query.
//
// R4.4 is explicit about the mechanism: "using **`pg_catalog.pg_blocking_pids(pid)`**
// (not a manual lock join)". The requirement names the function because the
// alternative it rejects is the one a first pass writes — joining pg_locks on
// relation and mode to derive who-blocks-whom, which is wrong for row locks, wrong
// for multiple lockers of one row, and wrong for a session that waits on a lock
// the other session has not been granted yet. pg_blocking_pids is the server's own
// answer, and it returns an array, which is why the Extra exists: the matrix's
// column set has a scalar `pid` and no way to express "the pids blocking this one"
// as a column, so the expression goes in the spec and the alias names it.
//
// The consequence for R4.4's wording is worth stating plainly: pg_blocking_pids
// *is* the pg_locks half. R4.4 says "from `pg_locks` + `pg_stat_activity`" and the
// function is the join, so the collector reads one view and gets both halves. That
// is also why the 6a critical path does not need locks/ before activity/ — the
// blocking edges come from this Extra; locks/ is the lock *table*, which R4.4
// needs for the lock mode and relation a session waits on, not for the edge.
var activitySpecs = map[pgversion.View]QuerySpec{
	pgversion.ViewStatActivity: {
		Extras: []Extra{
			{Expr: "pg_catalog.pg_blocking_pids(pid)", Alias: ColumnBlockingPids},
		},
	},
}

// ColumnBlockingPids is the alias for R4.4's pg_blocking_pids(pid) Extra.
//
// It is a constant because the name is referenced from three places (the spec
// above, the row parser, and the test that asserts the projection carries it) and
// an alias spelled twice is an alias that drifts.
const ColumnBlockingPids = "blocking_pids"

// MaxSessions is R4.1's "top 50" — the size of the list Cloud SQL renders and the
// number R4.1 names in the same sentence as the column list.
const MaxSessions = 50

// MaxChainDepth is R4.4's "render the chain up to **3 levels deep**".
//
// Three is a rendering limit, not a data limit: pg_blocking_pids gives every
// blocker of a session at every level, and Cloud SQL's blocked-query investigation
// tree shows three of them. The collector enforces the limit rather than leaving
// it to the UI because R4.4's "with the blocking statement text shown for each
// ancestor" is a statement about what the data carries — a chain built to five
// levels and rendered at three has ancestors whose text was fetched for nothing,
// and a chain built to three and rendered at five has a hole the UI cannot fill.
const MaxChainDepth = 3

// Session is one row of pg_stat_activity, shaped as R4.1's table.
//
// R4.1 names thirteen Cloud SQL columns and this struct is that list, in that
// order, with two additions the requirement makes in later sentences rather than
// in the column list: BackendType (R4.6: "surface `backend_type` so
// autovacuum/parallel/walsender sessions are distinguishable from client
// backends") and QueryID (R4.7: "Show per-session `query_id` and link it to the
// R3 query detail when the id is known").
//
// The struct is the API-plane product. R9.1's two-plane rule is explicit that
// "query text, plan bodies, active-session rows, blocking chains" are "API +
// store only" and that "Metrics carry the numbers; the API carries the text" — so
// the row is a struct with strings in it, not a set of labelled floats, and the
// reason is in R9.3 — and the sentence has to be read with the two clauses apart,
// which is A3's ruling on it. "every series carries `namespace`, `cluster`, `pod`,
// and `instance_role`" is closed: that is the base set and no family may drop it,
// and a session's identity is a pid, which is not in it. "pooler series carry
// `pooler`; database series add `datname`; user series add `usename`" is a mapping
// from family-kind to label, and it enumerates the extras so far rather than
// capping them. Reading the second clause as closed is what would make R9.5b's
// `fingerprint` and `queryid` labels a violation, and they are not one. naming.go
// says the same thing from the other side, calling `pgsi_pg_stat_activity_pid` the
// artefact you get when you export an identifier as a number.
type Session struct {
	// Pid is Cloud SQL's `Process ID`, from the matrix's `pid`.
	Pid int
	// Query is Cloud SQL's `Query` — the statement text. On an idle session PG
	// keeps the last statement here, which is why R4.1 lists the column for idle
	// rows too and why R4.6 separates the states rather than filtering them.
	Query string
	// QueryType is Cloud SQL's `Query type`: the statement kind, derived from the
	// text. See queryTypeFrom for the derivation and its limits.
	QueryType string
	// Status is Cloud SQL's `Status`, from `state`. R4.1 requires both `active`
	// and `idle in transaction` to be present; R4.6 requires PG18's new `starting`
	// to render rather than error, which is why this is a string and not a closed
	// enum — see the doc on queryTypeFrom for why the same reasoning does not
	// apply to QueryType.
	Status string

	// SessionDurationSeconds is Cloud SQL's `Session duration`: now - backend_start.
	// TransactionDurationSeconds is `Transaction duration`: now - xact_start, and
	// zero when the session has no open transaction (PG leaves xact_start NULL
	// outside one, and a duration of 0 for "no transaction" is the honest reading
	// of a value that has no transaction to have a duration in).
	// QueryDurationSeconds is `Query duration`: now - query_start.
	//
	// All three are computed from the row's own columns and the row's own
	// pgsi_source_now, never from our clock. That is R4.8's requirement in
	// mechanical form: the [A] test asserts "each row's durations match the SQL
	// values within tolerance — with both numbers printed", and a duration
	// computed as our-now minus their-timestamp mixes two clocks, so the number
	// printed and the number the SQL implies differ by the skew R13.11 exists to
	// catch. One clock per row is what makes the two numbers comparable.
	SessionDurationSeconds     float64
	TransactionDurationSeconds float64
	QueryDurationSeconds       float64

	// WaitEventType and WaitEvent are Cloud SQL's `Wait event type` and `Wait
	// event`. R4.3's rollup is these two fields grouped; on PG17+ their values are
	// validated against pg_wait_events, which is what makes an unmatched pair a
	// finding rather than a typo.
	WaitEventType string
	WaitEvent     string
	// UnknownWaitEvent is R4.3's validation verdict for this row: true when the
	// server reported a (wait_event_type, wait_event) pair that its own
	// pg_wait_events catalogue does not contain.
	//
	// It is a field on the row rather than a return value from ValidateWaitEvents
	// because the pair is per-session and the rollup is per-pair: R4.3's
	// "validate and label event names" has to mark the *sessions* whose event is
	// unrecognised so the Wait-events tab can say which of the 12 backends waiting
	// on `Lock/transactionid` is a name the server should not know. It is false on
	// PG13–PG16 by construction — there is no catalogue there to be missing from —
	// which is why the field's meaning is "absent from a catalogue that exists" and
	// not "not recognised".
	UnknownWaitEvent bool

	// Database, ApplicationName and UserName are R9.3's two label-bearing columns
	// plus the third Cloud SQL column. Database and UserName are the exact strings
	// R9.3 names as `datname` and `usename`; carrying them here is what lets the
	// identity-labelling step and the API agree on one spelling.
	Database        string
	ApplicationName string
	UserName        string
	// ClientAddress is Cloud SQL's `Client address`, from client_addr. ClientPort
	// is separate because PG separates them; Cloud SQL renders the pair as one
	// column, and formatting is the UI's job, not the collector's.
	ClientAddress string
	ClientPort    string

	// BackendType is R4.6's requirement: client, autovacuum worker, parallel
	// worker, walsender, and the rest. R4.1's table is a *client* session list, so
	// this field is what lets the UI answer "why is there an autovacuum row in my
	// client list" without the collector filtering rows R4.1 asked for.
	BackendType string
	// QueryID is R4.7's link to the R3 query detail. Empty on PG13, which has no
	// query_id column — the matrix's own column set says so — and R4.7's "when the
	// id is known" is the clause that makes an empty string correct there rather
	// than a missing value.
	QueryID string
	// BlockingPids is R4.4's edge list for this session: the pids directly blocking
	// it, straight from pg_catalog.pg_blocking_pids(pid). Empty means not blocked.
	BlockingPids []int
	// Action is Cloud SQL's `Action` column — the Terminate affordance (R4.5).
	//
	// The collector leaves it empty. R4.1's rule for the column is that it "renders
	// **disabled with a link to the R12.8 gate** whenever `--allow-actions=false`
	// (the default)", and the gate is a runtime flag on the API server, not a fact
	// about the session. A collector that filled this in would be answering a
	// question about the server's flags from inside a round that does not read
	// them; the API owns the field and the field exists on the struct so the
	// contract has one place to be stated.
	Action string

	// SourceTime is the row's own pgsi_source_now — the instance clock the three
	// durations were measured against. Zero means the driver returned a value the
	// parser did not recognise, which clock.go treats as "no source clock" rather
	// than as the epoch.
	SourceTime time.Time
}

// ActivityCollector is §3.3's activity collector.
//
// It is the collector whose product is not its samples. R9.1 splits the
// capabilities into two planes and puts active-session rows and blocking chains on
// the API side, so the interface method Collect returns the metrics half (the
// counts R9.7 names) and CollectSessions returns the rows half. Both come from one
// round, and one round means one read-only transaction (R2.7) holding one query per
// view — which is the property that makes R4.8's "the returned pid set equals a
// direct SELECT" checkable at all: the scorecard, the rollup and the chain are all
// derived from the same set of rows the API returns, so a number in the scorecard
// can be traced to a row and a row can be traced to the SQL.
type ActivityCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewActivityCollector builds the collector on top of an opener (R2.7's
// transaction source) and a config (R2.7's timeout, R2.8's application name,
// R13.11's clock), with every unset field resolved by SQLConfig.withDefaults.
func NewActivityCollector(opener SQLOpener, cfg SQLConfig) *ActivityCollector {
	c := &ActivityCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:      CollectorActivity,
		Interval:  ActivityInterval,
		Source:    SourceSQL,
		Cost:      CostCheap,
		AppliesTo: AppliesToViews(activityViews...),
		// Timeout unset: R2.1's 10 s default. R4.2 calls the query cheap, and
		// Descriptor's rule is that a collector whose SQL is known to be cheap does
		// not set a shorter one, because the timeout is a hang guard and R2.7's
		// statement_timeout is the tuning knob.
		ViewGroups: []string{activityViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *ActivityCollector) Descriptor() Descriptor {
	return c.desc
}

// Collect is the Collector interface method: the metrics half of R9.1's split.
//
// It runs the round and returns the derived samples. The rows come back from
// CollectSessions, which is what an API handler calls; Collect delegates to it and
// drops them, because a scheduler that publishes samples to a registry has nowhere
// to put a query text and R9.1 says it should not.
func (c *ActivityCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	sessions, res, err := c.CollectSessions(ctx, tgt)
	if err != nil {
		return res, err
	}
	res.Samples = append(res.Samples, ActivitySamples(sessions, c.cfg.Now())...)
	return res, nil
}

// CollectSessions runs one round and returns the rows plus the round's accounting.
//
// This is the entry point R9.1's API plane needs and R4.8's acceptance test calls:
// the requirement's [A] clause is "the returned pid set equals a direct `SELECT pid
// FROM pg_catalog.pg_stat_activity` within ±2 s of `capturedAt`", and the thing
// being compared to the SELECT is a list of sessions, not a list of samples.
//
// The error paths return the round-trips issued so far, because R13.1's budget is
// measured from Result.RoundTrips and a collector that returns a zero alongside a
// partial failure makes a cluster that issued two queries look like one that
// issued none.
func (c *ActivityCollector) CollectSessions(ctx context.Context, tgt Target) ([]Session, Result, error) {
	projections, err := c.projections(tgt)
	if err != nil {
		// Zero is correct: nothing was issued, because building the SQL is what
		// tells us there is nothing to issue.
		return nil, Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		return nil, Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorActivity, err)
	}

	var (
		sessions  []Session
		catalogue map[waitKey]bool
		trips     int
	)
	for _, pv := range projections {
		rows, qerr := tx.Query(ctx, pv.Query)
		trips++
		if qerr != nil {
			_ = tx.Rollback()
			return nil, Result{RoundTrips: trips},
				fmt.Errorf("collector %s: query %s: %w", CollectorActivity, pv.view, qerr)
		}
		switch pv.view {
		case pgversion.ViewStatActivity:
			sessions = sessionsFromRows(rows)
		case pgversion.ViewWaitEvents:
			catalogue = waitCatalogue(rows)
		}
	}

	if cerr := tx.Commit(); cerr != nil {
		// The data was read; the transaction just failed to close. That is still a
		// failed round, and the samples from the views that did answer come back
		// with the error for the same reason database.go returns them: the
		// scheduler publishes nothing on a failed round, so returning them costs
		// nothing and gives a caller that logs the result something to log.
		_ = tx.Rollback()
		return nil, Result{RoundTrips: trips},
			fmt.Errorf("collector %s: commit: %w", CollectorActivity, cerr)
	}

	// R4.3's validation half, after the commit: the catalogue and the sessions came
	// from the same transaction, which is the only reason the comparison means
	// anything.
	for i := range sessions {
		sessions[i].UnknownWaitEvent = !ValidateWaitEvents(sessions[i], catalogue)
	}

	return sessions, Result{RoundTrips: trips}, nil
}

// projections builds one query per view for the target's major.
//
// The set is pg_stat_activity always, plus pg_wait_events on the majors that have
// it. Asking the matrix rather than the major number is what keeps this correct
// when the matrix gains a major: the same `Exists` call the applicability filter
// uses answers "does this server have the table R4.3 wants validated against".
func (c *ActivityCollector) projections(tgt Target) ([]viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return nil, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorActivity, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		return nil, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (pg_stat_activity has no column set there)",
			CollectorActivity, tgt.PGMajor)
	}

	views := make([]pgversion.View, 0, 2)
	views = append(views, activityViews...)
	if pgversion.Exists(pgversion.ViewWaitEvents, tgt.PGMajor) {
		views = append(views, pgversion.ViewWaitEvents)
	}

	out := make([]viewProjection, 0, len(views))
	for _, v := range views {
		p, ok := Project(v, tgt.PGMajor, c.specFor(v))
		if !ok {
			return nil, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d", CollectorActivity, v, tgt.PGMajor)
		}
		if err := ValidateProjection(p); err != nil {
			return nil, err
		}
		out = append(out, viewProjection{view: v, Projection: p})
	}
	return out, nil
}

// sessionsFromRows turns pg_stat_activity's rows into sessions.
//
// The columns come from the projection rather than from the row map, for the two
// reasons database.go states and this file inherits: a map's iteration order is
// random, and a row missing a column must not silently shrink what the collector
// reports. Here there is a third reason specific to this collector — R4.8's
// acceptance compares a *set of pids*, and a pid read from a map that happened to
// drop the `pid` key would vanish from the set, which is exactly the failure the
// requirement's ±2 s equality clause is written to catch.
func sessionsFromRows(rows []Row) []Session {
	out := make([]Session, 0, len(rows))
	for _, row := range rows {
		s, ok := sessionFromRow(row)
		if !ok {
			continue
		}
		out = append(out, s)
	}
	return out
}

// sessionFromRow parses one pg_stat_activity row.
//
// It returns false when the row has no usable pid. That is the only row this
// collector drops, and it is the analogue of database.go's shared-objects rule
// (D20's S8): a pg_stat_activity row without a pid is a row whose identity the
// contract cannot express, and R4.8's test compares pids, so keeping a row with
// pid 0 would put a process into the set that the direct SELECT does not return.
func sessionFromRow(row Row) (Session, bool) {
	get := func(col string) string {
		v, _ := row.Get(col)
		return v
	}

	rawPid := get("pid")
	pid, err := strconv.Atoi(strings.TrimSpace(rawPid))
	if err != nil || pid <= 0 {
		return Session{}, false
	}

	s := Session{
		Pid:              pid,
		Query:            get("query"),
		Status:           get("state"),
		WaitEventType:    get("wait_event_type"),
		WaitEvent:        get("wait_event"),
		Database:         get("datname"),
		ApplicationName:  get("application_name"),
		UserName:         get("usename"),
		ClientAddress:    get("client_addr"),
		ClientPort:       get("client_port"),
		BackendType:      get("backend_type"),
		QueryID:          get("query_id"),
		BlockingPids:     parseBlockingPids(get(ColumnBlockingPids)),
		UnknownWaitEvent: false,
	}
	s.QueryType = queryTypeFrom(s.Query)

	// The three durations, all measured on the row's own clock.
	source, hasSource := parseTimestamp(get(SourceNowColumn))
	if hasSource {
		s.SourceTime = source
		if started, ok := parseTimestamp(get("backend_start")); ok {
			s.SessionDurationSeconds = source.Sub(started).Seconds()
		}
		// xact_start is NULL outside a transaction and query_start is NULL for a
		// session that has not run a statement yet. parseTimestamp returns false for
		// the empty string, so both durations stay 0 — which is the honest reading of
		// "no transaction" and not the lie a 0 measured from a real clock would be.
		if started, ok := parseTimestamp(get("xact_start")); ok {
			s.TransactionDurationSeconds = source.Sub(started).Seconds()
		}
		if started, ok := parseTimestamp(get("query_start")); ok {
			s.QueryDurationSeconds = source.Sub(started).Seconds()
		}
	}
	return s, true
}

// queryTypeFrom derives Cloud SQL's `Query type` from the statement text.
//
// The requirement lists the column and never says where the value comes from, and
// pg_stat_activity has no such column on any major — so it is derived, and the
// derivation is deliberately narrow. Only a closed set of statement keywords is
// returned; anything else is "UNKNOWN". That is the opposite of Status, which is
// the raw `state` string because R4.6 requires an unrecognised value (PG18's
// `starting`) to render rather than error.
//
// The asymmetry is deliberate and it is R4.8's. Query type is not one of the
// values the [A] test compares — R4.8 names the pid set and the durations — so a
// conservative closed set costs nothing and prevents the failure mode an open
// derivation has: a statement beginning with a quoted string, a CTE, or a
// multi-line comment yields a "query type" that is a fragment of SQL, and once it
// is in the payload it is indistinguishable from a real one.
func queryTypeFrom(query string) string {
	s := strings.TrimSpace(stripLeadingSQLComments(query))
	if s == "" {
		return QueryTypeUnknown
	}
	word := s
	if i := strings.IndexAny(word, " \t\n\r("); i >= 0 {
		word = word[:i]
	}
	word = strings.ToUpper(strings.Trim(word, ";"))

	switch word {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "WITH",
		"BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT",
		"CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE",
		"VACUUM", "ANALYZE", "ANALYSE", "COPY", "PREPARE", "EXECUTE", "DEALLOCATE",
		"SET", "SHOW", "EXPLAIN", "LOCK", "CALL", "DO", "REFRESH", "REINDEX", "CLUSTER":
		return word
	}
	return QueryTypeUnknown
}

// QueryTypeUnknown is queryTypeFrom's answer for text it cannot classify.
const QueryTypeUnknown = "UNKNOWN"

// stripLeadingSQLComments removes leading whitespace and `--` / `/* */` comments so
// a statement that opens with a comment still yields its real keyword.
func stripLeadingSQLComments(query string) string {
	s := query
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return ""
		}
		return s
	}
}

// parseBlockingPids parses the text form of an integer[] into pids.
//
// PG renders an empty array as `{}` and a populated one as `{1234,5678}`. The
// function accepts both, plus the bare `1234` a driver that stringifies scalars
// would produce, and returns nil for anything it cannot read — a nil, not an
// error, because R4.4's edge list being unreadable means "no edges known for this
// session", which is the same answer as an empty array and must not fail the round.
func parseBlockingPids(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "NULL" || raw == "null" {
		return nil
	}
	raw = strings.TrimPrefix(raw, "{")
	raw = strings.TrimSuffix(raw, "}")
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			// One unparseable element makes the whole array unparseable rather than
			// partially parsed: a chain built from three blockers where the array
			// named five is a chain that silently lost two.
			return nil
		}
		out = append(out, n)
	}
	return out
}

// Scorecard is R4.1's "summary scorecard above the table (counts by state,
// longest-running query, longest idle-in-transaction)".
type Scorecard struct {
	// ByState is the count of sessions in each `state` value present. R4.1's
	// "counts by state" and R9.7's `db_backends_by_state` family are the same
	// number seen from the two planes.
	ByState map[string]int
	// LongestQuery is R4.1's "longest-running query" and LongestIdleInTransaction
	// is its "longest idle-in-transaction". They are separate fields because they
	// are separate rows in Cloud SQL's scorecard and are usually different
	// sessions: a backend idle in transaction is, by definition, not running a
	// statement, so its QueryDurationSeconds is the duration of the statement it
	// already finished. R4.1 asks for both numbers because the second one is the
	// finding — a long idle-in-transaction is the vacuum-blocking and
	// lock-holding case, and it is invisible in a scorecard that ranks only by
	// running-statement duration.
	LongestQuery             *Session
	LongestIdleInTransaction *Session
	// Total is the number of sessions the collector read, before R4.1's top-50
	// cut. The scorecard is computed over all of them and the table over the
	// first 50, because a count that changed when the list was truncated would
	// make the header disagree with the rows beneath it.
	Total int
}

// Summarize computes R4.1's scorecard from a session set.
func Summarize(sessions []Session) Scorecard {
	sc := Scorecard{ByState: map[string]int{}, Total: len(sessions)}
	for i := range sessions {
		s := &sessions[i]
		sc.ByState[s.Status]++
		if sc.LongestQuery == nil || s.QueryDurationSeconds > sc.LongestQuery.QueryDurationSeconds {
			sc.LongestQuery = s
		}
		if s.Status == "idle in transaction" &&
			(sc.LongestIdleInTransaction == nil || s.TransactionDurationSeconds > sc.LongestIdleInTransaction.TransactionDurationSeconds) {
			sc.LongestIdleInTransaction = s
		}
	}
	return sc
}

// WaitEventCount is one cell of R4.3's "wait_event_type × wait_event counts".
type WaitEventCount struct {
	Type  string
	Event string
	Count int
	// Unknown marks the cell's events as absent from pg_wait_events on a version
	// that has the catalogue — R4.3's "validate" half. Always false on PG13–16,
	// where there is no catalogue to validate against and the field's doc says the
	// answer is not a finding.
	Unknown bool
}

// waitKey is the (type, event) pair pg_wait_events is keyed by.
type waitKey struct{ Type, Event string }

// waitCatalogue turns pg_wait_events's rows into the set of legal (type, event)
// pairs.
//
// The two sides spell the pair differently and the mapping is the whole function.
// The catalogue's columns are `type` and `name` (pgversion/matrix_data.go:519,
// which is the docs page R2.4's generator parses rather than a transcription of
// it); pg_stat_activity's are `wait_event_type` and `wait_event`, which is the
// spelling R4.3's "(type, event)" wording uses and Session's fields carry. The
// names are not interchangeable: reading the catalogue with the activity column
// names yields two empty strings, the blank check below skips every row, and the
// catalogue comes back empty — which validates every waiting session on PG17 as
// unknown, the exact false-positive R4.3's "(PG17+)" exists to avoid.
func waitCatalogue(rows []Row) map[waitKey]bool {
	cat := make(map[waitKey]bool, len(rows))
	for _, r := range rows {
		typ, _ := r.Get("type")
		ev, _ := r.Get("name")
		if typ == "" && ev == "" {
			continue
		}
		cat[waitKey{Type: typ, Event: ev}] = true
	}
	return cat
}

// ValidateWaitEvents is R4.3's validation clause for one session.
//
// It returns true when the session's wait pair is legal, which includes the two
// cases that are legal by construction: a session with no wait event at all (PG
// leaves wait_event_type/wait_event NULL for a session that is not waiting, and
// R4.3's rollup is over the sessions that are), and any session on a major without
// pg_wait_events, where catalogue is nil and there is nothing to validate against.
// The second case is why the catalogue is a parameter and not a field — R4.3's
// "(PG17+)" is a fact about the server, and a validator that assumed the catalogue
// always exists would mark every PG16 event unknown.
func ValidateWaitEvents(s Session, catalogue map[waitKey]bool) bool {
	if s.WaitEventType == "" && s.WaitEvent == "" {
		return true
	}
	if catalogue == nil {
		return true
	}
	return catalogue[waitKey{Type: s.WaitEventType, Event: s.WaitEvent}]
}

// WaitEventRollup groups a session set into R4.3's counts.
//
// Sessions with no wait event are excluded: R4.3's tabs are "Wait event types" and
// "Wait events", and a row for the empty string is a row for "not waiting", which
// is not an event type. The result is sorted by count descending then by the pair,
// so the rollup is deterministic and the test that pins it does not depend on map
// order.
func WaitEventRollup(sessions []Session) []WaitEventCount {
	type agg struct {
		count   int
		unknown bool
	}
	m := map[waitKey]*agg{}
	for _, s := range sessions {
		if s.WaitEventType == "" && s.WaitEvent == "" {
			continue
		}
		k := waitKey{Type: s.WaitEventType, Event: s.WaitEvent}
		a := m[k]
		if a == nil {
			a = &agg{unknown: s.UnknownWaitEvent}
			m[k] = a
		}
		a.count++
	}

	out := make([]WaitEventCount, 0, len(m))
	for k, a := range m {
		out = append(out, WaitEventCount{Type: k.Type, Event: k.Event, Count: a.count, Unknown: a.unknown})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Event < out[j].Event
	})
	return out
}

// BlockingChain is one entry of R4.4's "blocked-query investigation tree".
//
// Blocked is the session the row is about and Blockers are its ancestors, nearest
// first, to MaxChainDepth levels. R4.4's "with the blocking statement text shown
// for each ancestor" is satisfied by carrying the ancestor pids and letting the
// caller resolve each one's text from the same session set — which is the property
// that makes the tree honest, since a blocker's statement text read from a
// different round would be a statement the session may not have been running.
type BlockingChain struct {
	Blocked  Session
	Blockers []Session
}

// BlockingChains builds R4.4's graph from a session set.
//
// Only sessions with at least one known blocker produce a chain: R4.4's tree is an
// investigation of blocked queries, and a row for every idle session would bury the
// one row that has a blocker.
//
// The traversal is breadth-first from the blocked session, resolving each pid
// against the set, and it stops at MaxChainDepth. A pid with no row in the set ends
// that branch rather than failing it — a blocker in a different database is a real
// blocker whose row this query did not return, and the tree says so by being
// shorter, not by being wrong. A cycle (the pathological A-blocks-B-blocks-A) is
// cut by a visited set, because without one the traversal of a two-node cycle
// inside a three-level budget visits the same session twice and renders the same
// statement twice in one column of the tree.
func BlockingChains(sessions []Session) []BlockingChain {
	byPid := make(map[int]*Session, len(sessions))
	for i := range sessions {
		byPid[sessions[i].Pid] = &sessions[i]
	}

	out := make([]BlockingChain, 0, len(sessions))
	for i := range sessions {
		s := &sessions[i]
		if len(s.BlockingPids) == 0 {
			continue
		}
		visited := map[int]bool{s.Pid: true}
		frontier := []*Session{s}
		var chain []Session
		for depth := 0; depth < MaxChainDepth && len(frontier) > 0; depth++ {
			var next []*Session
			for _, cur := range frontier {
				for _, bp := range byPid[cur.Pid].BlockingPids {
					if visited[bp] {
						continue
					}
					visited[bp] = true
					b, ok := byPid[bp]
					if !ok {
						continue
					}
					chain = append(chain, *b)
					next = append(next, b)
				}
			}
			frontier = next
		}
		if len(chain) == 0 {
			continue
		}
		out = append(out, BlockingChain{Blocked: *s, Blockers: chain})
	}
	return out
}

// TopSessions applies R4.1's "top 50" cut.
//
// R4.1 names the number and not the ordering, so the ordering is stated here:
// active sessions first, then idle-in-transaction, then everything else, and within
// each group the longest query first. That is Cloud SQL's ordering — the list leads
// with what is running, which is the whole reason R4.1 pairs the table with a
// scorecard whose first row is the longest-running query.
//
// n <= 0 means MaxSessions.
func TopSessions(sessions []Session, n int) []Session {
	if n <= 0 {
		n = MaxSessions
	}
	ranked := make([]Session, len(sessions))
	copy(ranked, sessions)
	sort.SliceStable(ranked, func(i, j int) bool {
		ri, rj := rankOf(ranked[i]), rankOf(ranked[j])
		if ri != rj {
			return ri < rj
		}
		if ranked[i].QueryDurationSeconds != ranked[j].QueryDurationSeconds {
			return ranked[i].QueryDurationSeconds > ranked[j].QueryDurationSeconds
		}
		return ranked[i].Pid < ranked[j].Pid
	})
	if len(ranked) > n {
		ranked = ranked[:n]
	}
	return ranked
}

// rankOf is the ordering key TopSessions uses: 0 active, 1 idle in transaction, 2
// everything else, including PG18's `starting`.
func rankOf(s Session) int {
	switch s.Status {
	case "active":
		return 0
	case "idle in transaction":
		return 1
	default:
		return 2
	}
}

// ActivitySamples derives the metrics half of R9.1's split from a session set.
//
// Two families, and the reason there are two and not twenty-two is naming.go's
// classification of pg_stat_activity's columns: every one of the 22 columns the
// matrix selects is TypeNotExported. pid, usesysid and client_port are
// identifiers — naming.go names `pgsi_pg_stat_activity_pid` as the artefact you get
// when you export one as a number — and datname, usename, application_name,
// client_addr, state, wait_event_type, wait_event, backend_type, query and
// query_id are R9.3 labels or API-plane text, and backend_start, xact_start,
// query_start and state_change are per-session start times, not the view's
// stats_reset (pg_stat_activity has no epoch, which clock.go says outright).
//
// So the numbers R9.7 asks this view for are the grouped counts, and they are the
// two families R9.7's catalogue carries: pgsi_db_backends_by_state and
// pgsi_db_backends_by_application_name. They were provisional there until this
// collector shipped; A2 deleted the provisional rows at a826f84, and
// naming_catalogue_test.go's biconditional is what stops that pair drifting again.
//
// R9.3's label contract, as A3 ruled it — the ruling this function used to ask
// for, now in the cell itself: the base set (namespace, cluster, pod,
// instance_role) is closed, and the per-family half ("database series add
// `datname`; user series add `usename`") enumerates the extras so far rather than
// capping them. The proof is inside the spec — R9.5b mints `fingerprint` and
// `queryid` as labels of the per-query family and has never been filed as a
// violation. What binds `state` and `application_name` here is the shape rule in
// R9.3's purpose clause: a per-family label must be a dimension the family's own
// source view groups by, and the family name must name that dimension. The
// rejected alternative — one family per state value — would mint seven families to
// express one grouped count, the same over-naming R9.4's table avoids by using
// labels for the database dimension.
//
// The third family this function emitted, pgsi_db_backends_by_wait_event, no
// longer exists. R4.3's rollup is the Wait Events page's payload and not a metric
// family (A3's S50, ruling on the naming row A2 had asked about): R9.1 puts
// active-session rows on the API plane, and a count derived from rows the API
// already carries is not a second product. The data is not lost — Session
// carries wait_event_type and wait_event per row, which is what the page renders.
func ActivitySamples(sessions []Session, now time.Time) []Sample {
	byState := map[string]int{}
	byApp := map[string]int{}
	for _, s := range sessions {
		byState[s.Status]++
		byApp[s.ApplicationName]++
	}

	out := make([]Sample, 0, len(byState)+len(byApp))
	for state, n := range byState {
		out = append(out, Sample{
			Metric:     MetricName(pgversion.ViewStatActivity, "backends_by_state"),
			Labels:     map[string]string{"state": state},
			Value:      float64(n),
			IngestTime: now,
		})
	}
	for app, n := range byApp {
		out = append(out, Sample{
			Metric:     MetricName(pgversion.ViewStatActivity, "backends_by_application_name"),
			Labels:     map[string]string{"application_name": app},
			Value:      float64(n),
			IngestTime: now,
		})
	}
	return out
}

// specFor returns the query spec for one of the collector's views, and it is
// where R2.8's own-session filter is applied.
//
// activitySpecs holds the static half (the pg_blocking_pids Extra) and this
// method adds the dynamic half, which cannot live in the map because it reads
// c.cfg. R2.8's operative clause is "exclude *our own* sessions from every count
// we report, by setting application_name=pgsi-collector and filtering it out of
// pg_stat_activity-derived metrics"; the setting half shipped in fea3571b as
// SQLConfig.ApplicationName on every Begin, and the filtering half was not there
// — the collector queried pg_stat_activity and counted itself among the backends.
// matrix.go:159-163 is the placement ruling this method obeys: the exclusion is a
// per-collector concern, so the collector adds it and the matrix does not.
//
// IS DISTINCT FROM rather than <> because application_name is nullable: a
// comparison against NULL evaluates to NULL, so a plain inequality would drop
// every session that never set a name — background workers, and any session the
// operator did not name — which is a wider exclusion than the requirement asks
// for. The filter is on the stat_activity spec only: pg_stat_wait_events (the
// conditional second view) has no application_name column to filter on.
func (c *ActivityCollector) specFor(v pgversion.View) QuerySpec {
	spec := activitySpecs[v]
	if v == pgversion.ViewStatActivity && spec.Where == "" {
		spec.Where = "application_name IS DISTINCT FROM " + quoteSQLLiteral(c.cfg.ApplicationName)
	}
	return spec
}
