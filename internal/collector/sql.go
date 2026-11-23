package collector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// DefaultStatementTimeout is R2.7's statement_timeout default (5 s).
//
// R2.7 names it as "honour statement_timeout from config (default 5 s) so a sick
// instance cannot wedge the poller", and it is a per-query bound: R2.1's
// per-collector timeout (DefaultTimeout, 10 s) bounds the whole round, so a
// collector whose group holds three queries can spend 5 s on each and still
// never reach the 10 s round ceiling. Descriptor.Timeout's comment points here
// for the tuning knob, and this is the constant it points at.
//
// This file is the SQL seam: R2.5's schema qualification, R2.7's
// one-read-only-transaction-per-view-group plus statement_timeout, and R2.8's
// application_name, stated once for every SQL collector instead of once per
// collector. The three rules are about *how* SQL is issued rather than what a
// collector reports, and the phase's four SQL collectors (database, settings,
// replication, wal) all need the same three answers — so the database
// collector's commit, the first SQL collector to land, is where they live, and
// the other three import them rather than re-deriving them. A change to one
// (a new statement_timeout default, a different application_name) is then one
// edit and not four that can drift apart, which is the same argument R2.4 makes
// about the matrix: a rule expressed in four places is four rules.
//
// The seam is an interface (SQLOpener) and not a *sql.DB because go.mod has no
// Postgres driver. Phase 0 (R11.1) shipped stdlib-only and the module comment
// names only client-go plus, for Phase 3, modernc.org/sqlite; choosing between
// lib/pq, pgx, and the kubelet pod-proxy is a separate decision from writing
// the collectors, and R13.10 constrains `replace` directives rather than
// dependencies, so adding one is allowed — it is just not this commit's
// decision to make. An interface keeps that decision open *and* makes R2.7 and
// R2.8 assertable: a test can count the transactions a collector opened and
// read the options it passed to the opener, which is the difference between
// "the rule is written" and "the rule is checked".
const DefaultStatementTimeout = 5 * time.Second

// ApplicationName is R2.8's application_name value: pgsi-collector.
//
// R2.8's mechanism is a pair — set the name, then filter it out of the counts —
// and the setting half belongs here because it is a property of the connection
// every SQL collector opens, while the filtering half is per-view: only
// pg_stat_activity-derived metrics can filter a session out, and the collector
// that owns those metrics (activity, statements) is where the WHERE clause
// lives. The value is a constant rather than a setting because R2.8 states it
// as a literal, and a name an operator changes is a name the filter no longer
// matches. R2.8 cites CNPG's own exporter doing the same thing with
// cnpg_metrics_exporter, which is why ours has to be ours and not theirs.
const ApplicationName = "pgsi-collector"

// SourceNowColumn is the alias every generated query gives now(), and the
// column through which a SQL collector fills R13.11's first timestamp.
//
// R13.11 requires the *instance's* clock on every sample, and inside a query is
// the only place a SQL collector can read it. Postgres returns the transaction's
// start timestamp for every now() in that transaction, so the alias gives every
// row in a view group the same instance time — which is the answer R13.11 wants
// (one instance clock per round, matching the one ingest clock) rather than the
// one a per-row clock would give (one per row, so a slow view group looks like
// clock skew). A value the collector cannot parse leaves Sample.SourceTime zero,
// and Sample.HasSourceClock then skips the comparison instead of reporting the
// 56-year skew that comparing against the Unix epoch would.
const SourceNowColumn = "pgsi_source_now"

// Row is one result row: every column value as the text Postgres returns for it.
//
// Text rather than typed columns because that is what a row gives a caller with
// no driver to tell it the types — every value is a string the collector parses
// anyway — and a map rather than a slice because every collector addresses
// columns by name. The order a query's columns arrive in is Projection.Columns'
// job, not the row's.
type Row map[string]string

// Get returns the raw text for one column.
func (r Row) Get(column string) (string, bool) {
	v, ok := r[column]
	return v, ok
}

// Tx is one read-only SQL transaction, which is R2.7's unit of execution.
//
// R2.7's "single read-only transaction per view group" is a consistency rule and
// a cost rule at once. Consistency: every row a collector reads comes from one
// snapshot, so a counter read from two views cannot disagree in a way that looks
// like a decrease (R3.3's counter-reset problem, one size up). Cost: the number
// of transactions a cluster sees per round equals its number of view groups
// rather than its number of queries, which is what keeps R13.1's "< 40 SQL
// round-trips" budget from being a function of how many views a collector
// happens to read.
//
// Commit and Rollback are both on the interface because a read-only transaction
// still has to be ended: Commit on the success path (a read-only commit writes no
// WAL and is the cheap way to close one) and Rollback on every error path, so a
// failed round cannot leak a transaction open on the instance.
type Tx interface {
	// Query runs one query inside the transaction and returns its rows.
	Query(ctx context.Context, query string) ([]Row, error)
	// Commit ends the transaction, keeping what it did.
	Commit() error
	// Rollback ends the transaction, discarding what it did.
	Rollback() error
}

// SQLOpener opens the transactions SQL collectors run in (R2.7).
//
// One Begin call per view group is the whole rule expressed as a type: a
// collector with a one-group descriptor that calls Begin once per round cannot
// accidentally open one transaction per query, and a test can count the calls to
// prove it did.
type SQLOpener interface {
	// Begin opens one read-only transaction against tgt with cfg applied.
	Begin(ctx context.Context, tgt Target, cfg SQLConfig) (Tx, error)
}

// SQLConfig is the configuration every SQL collector shares: the two connection
// settings R2.7 and R2.8 make mandatory, and the clock R13.11's IngestTime comes
// from.
//
// It is one struct rather than three constructor parameters because all four SQL
// collectors take the same three values, and four constructors with three
// optional fields each is how a phase ends up with four collectors that disagree
// about what a zero means. withDefaults resolves every zero in one place, the
// same way Config does for the scheduler.
type SQLConfig struct {
	// StatementTimeout is R2.7's per-query bound; zero selects
	// DefaultStatementTimeout (5 s). It is the tuning knob Descriptor.Timeout
	// points at: R2.1's per-collector timeout bounds the whole round, this bounds
	// one query inside it, so a sick instance cannot hold a poller open past 5 s
	// per query.
	StatementTimeout time.Duration
	// ApplicationName is R2.8's session name; empty selects ApplicationName.
	ApplicationName string
	// Now is the collector's clock, for Sample.IngestTime (R13.11's second
	// timestamp); nil selects time.Now. Injectable for the same reason the
	// scheduler's is: a test that cannot control the clock can only assert
	// elapsed-time behaviour by sleeping.
	Now func() time.Time
}

// withDefaults returns c with every unset field replaced by its documented
// default.
func (c SQLConfig) withDefaults() SQLConfig {
	if c.StatementTimeout <= 0 {
		c.StatementTimeout = DefaultStatementTimeout
	}
	if c.ApplicationName == "" {
		c.ApplicationName = ApplicationName
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Extra is one derived SELECT expression and the column name it comes back as.
//
// It exists because the matrix models *columns* and some requirements ask for
// values a column list cannot express — R5.2's "database sizes, TXID and MXID
// age" are pg_database_size(oid) and age(datfrozenxid), not columns of
// pg_database. An Extra is how a collector adds such an expression without
// forking the query builder: the matrix still supplies the column set (R2.4) and
// the schema (R2.5), and the extras are appended as named expressions.
type Extra struct {
	// Expr is the expression, schema-qualified per R2.5 —
	// "pg_catalog.pg_database_size(oid)", not "pg_database_size(oid)".
	Expr string
	// Alias is the name the value is returned under ("size"), and therefore the
	// column name the collector reads back and the metric name's last component.
	Alias string
}

// Projection is one view's query and the column names its rows carry, in SELECT
// order.
//
// The column list travels with the query because a collector that parses rows by
// name still needs to know which names exist on this major — and iterating a
// deterministic list rather than a map's random order is what makes a collector's
// sample order stable enough to test.
type Projection struct {
	// Query is the full SELECT statement.
	Query string
	// Columns names every column Query returns, in SELECT order: the matrix's
	// columns for this major, then any Extra aliases, then SourceNowColumn.
	Columns []string
}

// Project builds the query R2.4 and R2.5 prescribe for one view on one major.
//
// The column set comes from pgversion.Columns (R2.4's answer) and the schema
// prefix from pgversion.Schema (R2.5's), which is the whole requirement: the
// collector never writes a table name, so it cannot write one unqualified or
// misspell it, and it cannot select a column that this major does not have.
//
// now() is appended to every query as SourceNowColumn so R13.11's instance
// timestamp comes back with the data rather than costing an extra round-trip;
// see the constant for why the value is the transaction's start.
func Project(view pgversion.View, major int, extra ...Extra) (Projection, bool) {
	cols, ok := pgversion.Columns(view, major)
	if !ok {
		return Projection{}, false
	}

	list := make([]string, 0, len(cols)+len(extra)+1)
	list = append(list, cols...)
	names := make([]string, 0, len(cols)+len(extra)+1)
	names = append(names, cols...)

	for _, e := range extra {
		list = append(list, e.Expr+" AS "+e.Alias)
		names = append(names, e.Alias)
	}
	list = append(list, "now() AS "+SourceNowColumn)
	names = append(names, SourceNowColumn)

	return Projection{
		Query:   "SELECT " + strings.Join(list, ", ") + " FROM " + pgversion.Schema(view) + "." + string(view),
		Columns: names,
	}, true
}

// MetricName is the family name one column's values belong to: the view name
// plus the column name, without the R9.2 prefix.
//
// It is the convention clock.go's Sample.Metric documents — "pg_stat_database_xact_commit",
// not "pgsi_pg_stat_database_xact_commit" — as a function, so that the four SQL
// collectors share one naming rule instead of each inventing one, and so that
// step 4's R9.4 work (which is the requirement that actually fixes the exported
// names) is a change here rather than a sweep through every collector. R9.4's
// prefix rule stays out: the prefix is applied at render time so it can stay
// configurable (R9.2), and a collector that baked it in would make
// metrics.prefix a lie.
func MetricName(view pgversion.View, column string) string {
	return string(view) + "_" + column
}

// AppliesToViews is R2.1's PG-version applicability for a collector that reads
// more than one view: the collector applies to a major only if *every* view it
// reads exists there.
//
// The all-views rule, not the any-views rule, is what the matrix makes
// checkable. A collector that runs on a major where one of its views is missing
// has to either fail the whole round or report half a result set, and R2.7's
// one-transaction-per-group contract means the failure is discovered mid-
// transaction. Asking the matrix for each view is also what stops the answer from
// being a hand-written range that rots when the matrix gains a major.
func AppliesToViews(views ...pgversion.View) Applicability {
	return func(major int) bool {
		if len(views) == 0 {
			return false
		}
		for _, v := range views {
			if !pgversion.Exists(v, major) {
				return false
			}
		}
		return true
	}
}

// parseValue converts one raw column value to the float64 a Sample carries.
//
// Postgres renders every type as text and the three a collector meets are
// numeric (int8/int4/float8/numeric, all parseable by ParseFloat), boolean
// ("t"/"f" in the wire format, "true"/"false" from drivers that string the value
// — both accepted, mapping to 1 and 0 because a Sample is a float64 and R9.3's
// contract has no boolean type), and everything else, which is not a value and
// returns false so the caller can skip it rather than store a 0 that looks like a
// measurement of something.
func parseValue(raw string) (float64, bool) {
	switch raw {
	case "t", "true", "TRUE", "True":
		return 1, true
	case "f", "false", "FALSE", "False":
		return 0, true
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseTimestamp converts a timestamptz column's text to a time.Time.
//
// The layouts are Postgres' ISO output with and without fractional seconds, in
// both the "2026-11-22 10:00:00+00" form a driver returns and the RFC3339 form
// the JSON paths use. A value that matches none returns false, which callers
// turn into a zero time.Time — and clock.go treats a zero SourceTime as "no
// source clock" rather than as the epoch, so a format we do not know degrades to
// "not compared" instead of firing R13.11's warning on every sample.
func parseTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// validateProjection checks the two things about a projection that a typo would
// break at runtime on the instance rather than in a test: that every column name
// is a valid Postgres identifier, and that the query names its source schema.
//
// It is used by the collectors' tests rather than by Project itself — Project
// gets its identifiers from the matrix, which Validate already checked — so that
// a hand-written Extra (the one place a collector author types a column name) is
// still checked somewhere before it reaches a server.
func validateProjection(p Projection) error {
	for _, c := range p.Columns {
		if c == "" {
			return fmt.Errorf("collector: empty column name in projection %q", p.Query)
		}
		for i, r := range c {
			ok := r == '_' ||
				(r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				(i > 0 && r >= '0' && r <= '9')
			if !ok {
				return fmt.Errorf("collector: column name %q in projection %q is not a valid identifier", c, p.Query)
			}
		}
	}
	if !strings.Contains(p.Query, " FROM pg_catalog.") && !strings.Contains(p.Query, " FROM pg_stat_statements.") {
		return fmt.Errorf("collector: projection %q has no schema-qualified FROM (R2.5)", p.Query)
	}
	return nil
}
