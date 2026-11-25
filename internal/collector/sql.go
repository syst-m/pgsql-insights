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
// The seam is an interface (SQLOpener) and not a *sql.DB.
//
// It was written as an interface because go.mod had no Postgres driver, and the
// choice between lib/pq, pgx, and the kubelet pod-proxy was deferred as a
// separate decision from writing the collectors. That decision is now made —
// pgx/v5, through its database/sql-compatible pgx/stdlib subpackage, in
// internal/collector/pgxopener, which is the driver R13.10 now names — and the
// interface survives it for the reason this paragraph has always given: an
// interface makes R2.7 and R2.8 assertable, because a test can count the
// transactions a collector opened and read the options it passed to the opener,
// which is the difference between "the rule is written" and "the rule is
// checked". A *sql.DB in the signature would have made every collector test
// depend on a driver, which is why the pgx implementation is a child package and
// not a file in this one: the seam stops being a seam the moment the thing it
// abstracts is imported by the package that declares it.
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

// DefaultDatabase is the database a SQL collector connects to when nothing names
// one: postgres.
//
// It is R3.1's "per database" question answered at the connection layer.
// pg_extension, pg_stat_statements and the rest of the per-database catalogue live
// *inside* one database, so a collector cannot read them for a cluster's other
// databases over one connection: the database the connection opened against is the
// only one those rows can describe. SQLConfig.Database carries the value; this is
// what withDefaults gives an empty one, because a CNPG cluster always has a
// `postgres` database and the cluster-wide views (pg_stat_database, pg_database,
// pg_locks) are reachable from it.
//
// It lives here rather than in pgxopener — which had the constant first, as the
// standing answer while SQLConfig had no field to be the answer for — because
// withDefaults is where every other SQLConfig default is resolved, and a default
// that lives only in the driver package is one the collector package cannot state.
// pgxopener.DefaultDatabase is now an alias of this one, so the value has one home
// and the two packages cannot drift.
const DefaultDatabase = "postgres"

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
// settings R2.7 and R2.8 make mandatory, the database R3.1's per-database
// detection needs, and the clock R13.11's IngestTime comes from.
//
// It is one struct rather than a parameter list per constructor because every SQL
// collector takes the same values, and a constructor per collector with optional
// fields each is how a phase ends up with collectors that disagree about what a
// zero means. withDefaults resolves every zero in one place, the same way Config
// does for the scheduler.
type SQLConfig struct {
	// StatementTimeout is R2.7's per-query bound; zero selects
	// DefaultStatementTimeout (5 s). It is the tuning knob Descriptor.Timeout
	// points at: R2.1's per-collector timeout bounds the whole round, this bounds
	// one query inside it, so a sick instance cannot hold a poller open past 5 s
	// per query.
	StatementTimeout time.Duration
	// ApplicationName is R2.8's session name; empty selects ApplicationName.
	ApplicationName string
	// Database is the database the transaction opens against, and therefore the
	// database whose catalogue every row that transaction returns describes; empty
	// selects DefaultDatabase.
	//
	// It exists because R3.1's unit of detection is a database while R2.7's unit of
	// execution is a transaction, and a Postgres transaction belongs to exactly one
	// database: a collector that answers "which databases have pg_stat_statements"
	// from one fixed connection answers it for one database, which is the failure
	// A3's S119 names (the fixed `postgres` connection made the endpoint report one
	// presence row and render every other database as not-known). The settings
	// collector's extension probe now fans out — one connection per database from
	// the pg_database enumeration — and each connection's SQLConfig names the
	// database it opened, so the datname a sample carries is the one the query ran
	// in rather than one the collector guessed. That field is the first link of the
	// producer R3.1's second ruling asked for; the rest is
	// viewProjection.database and settings.go.
	Database string
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
	if c.Database == "" {
		c.Database = DefaultDatabase
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
	//
	// ValidateProjection checks it (D20's S10). This is the one field in the
	// package a collector author types by hand, and an unqualified call here is
	// shadowable by anything on search_path, which is the failure R2.5 names; the
	// check existed for the column names and the FROM clause and not for this
	// field, so `Extra{Expr: "age(datfrozenxid)"}` used to pass.
	Expr string
	// Alias is the name the value is returned under ("size"), and therefore the
	// column name the collector reads back and the metric name's last component.
	Alias string
}

// QuerySpec is the per-view SQL a collector adds to what the matrix supplies: the
// derived expressions in Extras and, when the view needs one, a WHERE clause.
//
// It replaced Project's variadic `extra ...Extra` parameter (D20's S7). The
// database collector needed a filter on pg_database — see database.go's
// databaseSpecs — and a second parameter bolted onto the variadic one
// (`Project(view, major, extras, where)`) reads as four constructors once the next
// collector needs a fourth thing. One struct with two optional fields is what this
// file's own SQLConfig doc argues for over "four constructors with three optional
// fields each". Every caller now passes one struct, and the common case is the
// empty one: `Project(v, major, QuerySpec{})`.
type QuerySpec struct {
	// Extras are the derived expressions to append after the matrix's columns.
	// Empty for a view whose column set is the whole projection, which is every
	// view so far except pg_database.
	Extras []Extra
	// Where is the query's WHERE clause without the keyword, and "" for no filter.
	//
	// It is part of the spec rather than baked into a query string because a
	// collector never writes a query string: Project assembles it. Carrying the
	// clause as a field is what lets ValidateProjection check it the same way it
	// checks an Extra (unqualifiedCall) — a check that had to re-parse SQL to find
	// its own inputs is a check that gets skipped, which is D20's S10.
	Where string
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
	// Extras and Where echo the QuerySpec Query was built from. The query string
	// already contains both; the fields exist so ValidateProjection can check the
	// spec's own fields rather than re-derive them from SQL (D20's S10).
	Extras []Extra
	Where  string
}

// Project builds the query R2.4 and R2.5 prescribe for one view on one major.
//
// The column set comes from pgversion.Columns (R2.4's answer) and the schema
// prefix from pgversion.Schema (R2.5's), which is the whole requirement: the
// collector never writes a table name, so it cannot write one unqualified or
// misspell it, and it cannot select a column that this major does not have.
//
// The spec carries the two things the matrix cannot express — derived expressions
// and a filter — and every caller passes one, the empty one being the common case;
// see QuerySpec for why it is a struct and not a variadic parameter.
//
// now() is appended to every query as SourceNowColumn so R13.11's instance
// timestamp comes back with the data rather than costing an extra round-trip;
// see the constant for why the value is the transaction's start.
func Project(view pgversion.View, major int, spec QuerySpec) (Projection, bool) {
	cols, ok := pgversion.Columns(view, major)
	if !ok {
		return Projection{}, false
	}

	list := make([]string, 0, len(cols)+len(spec.Extras)+1)
	list = append(list, cols...)
	names := make([]string, 0, len(cols)+len(spec.Extras)+1)
	names = append(names, cols...)

	for _, e := range spec.Extras {
		list = append(list, e.Expr+" AS "+e.Alias)
		names = append(names, e.Alias)
	}
	list = append(list, "now() AS "+SourceNowColumn)
	names = append(names, SourceNowColumn)

	query := "SELECT " + strings.Join(list, ", ") + " FROM " + pgversion.Schema(view) + "." + string(view)
	if spec.Where != "" {
		query += " WHERE " + spec.Where
	}

	return Projection{
		Query:   query,
		Columns: names,
		Extras:  spec.Extras,
		Where:   spec.Where,
	}, true
}

// MetricName lives in naming.go, not here.
//
// It was this file's one-line `string(view) + "_" + column` through Phase 2
// steps 1-3, with a doc comment promising that "step 4's R9.4 work ... is a
// change here rather than a sweep through every collector". Step 4 kept that
// promise by moving the function to naming.go, where the R9.4 table is, rather
// than by editing the nine call sites that call it.

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

// knownSchemas is the closed set of schemas a projection may reference, and the
// set R2.5's qualification rule is checked against.
//
// It is the same pair the FROM-clause check used to spell inline (" FROM
// pg_catalog." / " FROM pg_stat_statements."), pulled out so the FROM check and
// the expression check (unqualifiedCall) consult one answer instead of two. The
// set is closed because pgversion.Schema is the only other place a schema is
// named in the package, and both members come from its view→schema mapping.
var knownSchemas = []string{"pg_catalog", "pg_stat_statements"}

// knownSchema reports whether schema is one of knownSchemas.
func knownSchema(schema string) bool {
	for _, s := range knownSchemas {
		if s == schema {
			return true
		}
	}
	return false
}

// sqlKeywords is the closed set of words that can sit immediately left of a "(" in
// valid SQL without being a callee, and it is S143's fix.
//
// Without it the byte scan reads SQL's own syntax as a function call. In
// "CASE WHEN x IS NULL THEN (SELECT …) END" the identifier before the subselect's
// "(" is "THEN"; in "coalesce(a, b)" it is "coalesce". Neither is a function, so
// neither can be schema-qualified, and both used to be returned as unqualified calls
// — the false positives bloat.go:120 documents and works around by writing the
// default branch as 0 + (…) instead of a bare subselect.
//
// Membership rule: a word is in the set when Postgres treats it as syntax with no
// callable pg_catalog counterpart. That is the discriminator the finding names.
// COALESCE and NULLIF are SQL-standard conditional expressions — there is no
// pg_catalog.coalesce to qualify them with (bloat.go:120), so they are in. GREATEST
// and LEAST are real pg_catalog functions — bloat.go:201's pg_catalog.greatest(...)
// passes the check — so they are out, and a bare greatest(a, b) is still reported.
// The same rule keeps substring, trim, overlay, position and extract out despite
// being keywords: each has a pg_catalog implementation, which is exactly what makes
// the bare form shadowable and therefore worth reporting.
//
// The set is Postgres's reserved words (both reserved columns of Appendix C) plus
// coalesce and nullif. It is not a grammar and never needs to be: the scan consults
// it only for an identifier it has already found before a "(", so listing a word
// that never appears in that position costs nothing, while listing a word that IS a
// function costs a false negative. That asymmetry is why the list is grouped by
// category here rather than pasted as one flat word list.
//
// It is a map where knownSchemas is a slice because it has 94 members instead of
// two; the loop in knownSchema is fine for two and noise for 94.
var sqlKeywords = map[string]struct{}{
	// Postgres's reserved words (Appendix C, "reserved").
	"all": struct{}{}, "analyse": struct{}{}, "analyze": struct{}{}, "and": struct{}{}, "any": struct{}{},
	"array": struct{}{}, "as": struct{}{}, "asc": struct{}{}, "asymmetric": struct{}{}, "authorization": struct{}{},
	"binary": struct{}{}, "both": struct{}{}, "case": struct{}{}, "cast": struct{}{}, "check": struct{}{},
	"collate": struct{}{}, "collation": struct{}{}, "column": struct{}{}, "concurrently": struct{}{}, "constraint": struct{}{},
	"create": struct{}{}, "cross": struct{}{}, "current_catalog": struct{}{}, "current_date": struct{}{}, "current_role": struct{}{},
	"current_schema": struct{}{}, "current_time": struct{}{}, "current_timestamp": struct{}{}, "current_user": struct{}{}, "default": struct{}{},
	"deferrable": struct{}{}, "desc": struct{}{}, "distinct": struct{}{}, "do": struct{}{}, "else": struct{}{},
	"end": struct{}{}, "except": struct{}{}, "false": struct{}{}, "fetch": struct{}{}, "for": struct{}{},
	"foreign": struct{}{}, "from": struct{}{}, "grant": struct{}{}, "group": struct{}{}, "having": struct{}{},
	"in": struct{}{}, "initially": struct{}{}, "intersect": struct{}{}, "into": struct{}{}, "lateral": struct{}{},
	"leading": struct{}{}, "limit": struct{}{}, "localtime": struct{}{}, "localtimestamp": struct{}{}, "not": struct{}{},
	"null": struct{}{}, "offset": struct{}{}, "on": struct{}{}, "only": struct{}{}, "or": struct{}{},
	"order": struct{}{}, "overlaps": struct{}{}, "returning": struct{}{}, "select": struct{}{}, "session_user": struct{}{},
	"show": struct{}{}, "similar": struct{}{}, "table": struct{}{}, "then": struct{}{}, "to": struct{}{},
	"true": struct{}{}, "union": struct{}{}, "unique": struct{}{}, "user": struct{}{}, "using": struct{}{},
	"variadic": struct{}{}, "when": struct{}{}, "where": struct{}{}, "window": struct{}{}, "with": struct{}{},
	// Appendix C, "reserved (can be function or type name)", minus the members
	// that name a pg_catalog function and so stay reportable.
	"freeze": struct{}{}, "ilike": struct{}{}, "is": struct{}{}, "isnull": struct{}{}, "join": struct{}{},
	"notnull": struct{}{}, "tablesample": struct{}{}, "verbose": struct{}{}, "over": struct{}{}, "partition": struct{}{},
	"within": struct{}{}, "require": struct{}{},
	// Non-reserved, cannot be a function or type name, and syntax not a callee.
	"coalesce": struct{}{}, "nullif": struct{}{},
}

// isSQLKeyword reports whether name is in sqlKeywords. The comparison folds case
// because the scan hands it the raw bytes: Postgres folds unquoted identifiers to
// lower case, and this codebase writes SQL both ways — "CASE WHEN" in bloat.go's
// expressions, "coalesce(" in the same file's comment at :120.
func isSQLKeyword(name string) bool {
	_, ok := sqlKeywords[strings.ToLower(name)]
	return ok
}

// isIdentByte reports whether b can appear in a Postgres identifier.
func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// validIdent reports whether s is a valid Postgres identifier: the first byte a
// letter or underscore, the rest also allowing digits.
//
// This is the rule ValidateProjection has always applied to Projection.Columns,
// factored out so the column check and unqualifiedCall consult one answer. The
// byte loop and the rune loop it replaced agree on everything that matters: a
// non-ASCII rune (a "é" is 0xC3 0xA9) fails both, because neither byte is in the
// ASCII ranges — and an unquoted identifier containing one is folded away by
// Postgres anyway.
func validIdent(s string) bool {
	if s == "" {
		return false
	}
	first := s[0]
	if !(first == '_' || (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

// identBefore returns the identifier that ends just before index p — the run of
// identifier bytes immediately left of p — and the index it starts at. name is
// empty when expr[p-1] is not an identifier byte or p <= 0.
func identBefore(expr string, p int) (name string, start int) {
	start = p
	for start > 0 && isIdentByte(expr[start-1]) {
		start--
	}
	return expr[start:p], start
}

// unqualifiedCall returns the first function call in expr that is not qualified
// with a schema from knownSchemas, or "" when every call in it is qualified.
//
// It is a byte scan rather than a regexp because Go's RE2 has no lookbehind: the
// question is what precedes each "(", and the only way to ask that with a regexp
// is to match the whole prefix. For each "(", it takes the identifier immediately
// before it (skipping spaces) and, if that identifier follows a dot, the
// identifier before the dot too — then asks whether the schema half is one Postgres
// has.
//
//   - "pg_catalog.age(datfrozenxid)" → "" (qualified with a known schema)
//   - "age(datfrozenxid)" → "age" (no schema: shadowable by search_path, which is
//     the failure R2.5 exists to prevent)
//   - "other_schema.f(x)" → "other_schema.f" (a schema we do not know)
//   - "1 + 2", "(a)" → "" (nothing before the "(" is an identifier: a grouped
//     expression, not a call)
//   - "pg_catalog.has_database_privilege(oid, 'CONNECT')" → "" (one call; the
//     second argument is a string literal, not a call)
//   - "coalesce(a, b)", "CASE WHEN x THEN (SELECT 1) END" → "" (the identifier left
//     of the "(" is a keyword from sqlKeywords, so it is syntax, not a callee — S143)
func unqualifiedCall(expr string) string {
	for i := 0; i < len(expr); i++ {
		if expr[i] != '(' {
			continue
		}
		// "f (x)" is still a call: skip the spaces between the callee and its "(".
		p := i
		for p > 0 && (expr[p-1] == ' ' || expr[p-1] == '\t') {
			p--
		}
		name, start := identBefore(expr, p)
		if name == "" {
			continue
		}
		if start > 0 && expr[start-1] == '.' {
			schema, _ := identBefore(expr, start-1)
			if knownSchema(schema) {
				continue
			}
			return schema + "." + name
		}
		if isSQLKeyword(name) {
			continue // S143: "coalesce(a, b)" is syntax, not a call to coalesce
		}
		return name
	}
	return ""
}

// ValidateProjection checks the three things about a projection that a typo would
// break at runtime on the instance rather than in a test: that every column name
// is a valid Postgres identifier, that the query names a schema Postgres has
// (R2.5), and that every hand-written expression — each Extra.Expr and the Where
// clause — qualifies every function it calls (D20's S10).
//
// Every collector's projections() calls it on the projections it just built, on
// every round (database.go, settings.go). The doc used to justify it as something
// "used by the collectors' tests rather than by Project itself", which was written
// when Project was the only other caller; the collectors call it themselves now,
// and the reason to run it per round is that the matrix and the QuerySpecs are
// data that changes without the collectors changing (D20's S11: the rationale was
// stale, not the call).
func ValidateProjection(p Projection) error {
	for _, c := range p.Columns {
		if !validIdent(c) {
			return fmt.Errorf("collector: column name %q in projection %q is not a valid identifier", c, p.Query)
		}
	}
	from := ""
	if i := strings.Index(p.Query, " FROM "); i >= 0 {
		rest := p.Query[i+len(" FROM "):]
		if j := strings.IndexByte(rest, '.'); j > 0 {
			from = rest[:j]
		}
	}
	if !knownSchema(from) {
		return fmt.Errorf("collector: projection %q has no schema-qualified FROM (R2.5)", p.Query)
	}
	// The hand-written half. The column names come from the matrix and the FROM
	// clause from pgversion.Schema, so between them they cover everything Project
	// assembles *except* the extras and the filter — which is exactly where D20's
	// S10 found the gap: an Extra of {Expr: "age(datfrozenxid)"} passed while the
	// same expression written in a query string would not.
	for _, e := range p.Extras {
		if bad := unqualifiedCall(e.Expr); bad != "" {
			return fmt.Errorf("collector: extra expression %q in projection %q calls %s without a schema (R2.5: schema-qualify every catalog reference)", e.Expr, p.Query, bad)
		}
	}
	if bad := unqualifiedCall(p.Where); bad != "" {
		return fmt.Errorf("collector: WHERE clause %q in projection %q calls %s without a schema (R2.5: schema-qualify every catalog reference)", p.Where, p.Query, bad)
	}
	return nil
}
