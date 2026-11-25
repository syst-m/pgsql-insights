package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// CollectorBloat is §3.3's fourteenth name, and the `collector` label value on both
// R2.2 families for this collector's rounds.
//
// The row exists because A3's second ruling on R7.2 added it to the end of §3.3's
// table (`requirements.md:208`):
//
//	| `bloat` | 5 min | S2 `pg_stat_user_tables`, `pg_stat_user_indexes` | R7.2's two pairs; table size via `pg_total_relation_size`, index size via `pg_relation_size` |
//
// and the ruling (`requirements.md:366`) is the answer to the four asks this file's
// author put to A3 — name,
// interval, cost class, family names — with "the name is `bloat`, the interval is 5
// min, the cost class is `standard`, and the two families are `table_*` and
// `index_*`", all four confirmed as offered. The ruling also says why the row had to
// exist rather than the name alone: `collector.go:252-253`'s "one of §3.3's collector
// names; the schedule table is the list" forbids a partial inventory, and a rowless
// bloat would be one — the pooler pair got away with one row for two collectors only
// because `requirements.md:205`'s row covers both names (its Source cell reads
// `S5 + S6`), and nothing here covers this one.
//
// What R7.2 itself obliges is narrower than the two views, and the first ruling says
// so: "a phase satisfies this row by collecting and exporting the two pairs the row
// names (dead-tuple ratio from `pg_stat_user_tables` with `pg_total_relation_size`;
// `pg_stat_user_indexes.idx_scan = 0` with index size); the view surface rides with
// R7.1." The two pairs are the first three Extras below. The rest of the two views' columns
// ride along because R9.7's union rule makes the matrix's numeric column set the
// export set once a collector reads a view — "the export set is therefore the union of
// R5.2's enumerated families and the numeric columns R2.4's matrix returns for the
// target major, and R9.4's naming function must enumerate that union"
// (`requirements.md:401`) — so naming them is required rather than optional, and no
// amendment is needed for the naming half.
//
// R7.4 lives here too, and the ruling on its four asks (`requirements.md:368`) is the
// reason it is not its own file: "R7.4 extends `internal/collector/bloat.go` rather
// than gaining an `autovacuum` collector, and the reason is the shape of
// `internal/collector/naming.go:159`'s `exportNames`: one `map[string]exportEntry`
// keyed by `Canonical(view, column).String()` … so a metric name is a property of a
// *column*, never of a collector." A second collector over pg_stat_user_tables reads
// the same columns and therefore mints the same names, and the family word rides with
// the key — all 35 rows are `table_*` because R7.2's second ruling assigned them there
// (`requirements.md:366`), so an `autovacuum` collector would have to move them to
// `autovacuum_*` and shrink this projection back to the R7.2 half. The three R7.4
// Extras below are the answer to that ruling's ask 2 ("the three names, as offered"),
// and their SQL is the answer to ask 3 — see bloatNextAutovacuumSQL,
// bloatOverThresholdSQL and bloatNeverAnalyzedSQL for the constraint each one works
// around.
const CollectorBloat = "bloat"

// BloatInterval is §3.3's interval for the bloat collector (5 min), stated as a
// constant so R2.3's --interval.bloat override has a declared default to override
// rather than a literal buried in a struct literal.
//
// The ruling's reasoning is a cost argument, not a freshness one: "The cost scales
// with the size of the schema, not with the rate at which dead tuples appear, and the
// table already spans 15 s to 15 min, so 5 min sits inside the existing range beside
// `settings`'s 15 min rather than inventing a tier".
const BloatInterval = 5 * time.Minute

// bloatViewGroup is this collector's single R2.7 view group.
//
// One group for the two views, for the reason database.go's databaseViewGroup gives
// for three: two groups would be two transactions, two connection checkouts from the
// pooler, and — the part that changes the data rather than the budget — two separate
// snapshots, so a pg_stat_user_indexes row could name an index of a table whose
// pg_stat_user_tables row, read one transaction later, says does not exist. The name
// follows the collector, which is the convention every SQL collector follows
// (activity.go's activityViewGroup is "activity", database.go's is "database").
const bloatViewGroup = "bloat"

// bloatViews are §3.3's two sources for this collector, in the order their queries
// are issued.
var bloatViews = []pgversion.View{
	pgversion.ViewStatUserTables,
	pgversion.ViewStatUserIndexes,
}

// bloatNextAutovacuumSQL is R7.4's "next vacuum per table" as an Extra.
//
// The ruled expression is `requirements.md:368`'s: "the 0.1 model is `last_autovacuum
// + autovacuum_naptime` (the next launcher cycle), NULL when `last_autovacuum` is
// NULL". Two things about writing that in this builder are not obvious from the
// formula.
//
// The first is where naptime comes from. autovacuum_naptime is a server setting, not a
// column of pg_stat_user_tables, so the bare word in the ruling's formula has to be
// resolved the way ask 3 was ruled: a scalar subquery, not a join (`sql.go:309` emits
// exactly one FROM and `sql.go:242`'s QuerySpec has only Extras and Where, so a join
// cannot be produced — the ruling's words are "the builder is the constraint, not the
// checker"). Reading it from the settings collector instead was option (b) and it
// failed on schedule: SettingsInterval is 15 min against this collector's 5 min, so the
// value is up to ten minutes stale and absent entirely on this collector's first
// rounds.
//
// The second is that the expression emits a sample where the six last_* columns do
// not. The ruling calls the asymmetry intended: `sql.go:363`'s parseValue parses
// numbers and booleans only, so a bare timestamptz column is kindText and silent
// (`bloat.go:380-385`, following `wal.go:281-292`), while date_part('epoch', …) is the
// unix-seconds conversion that same ruling points at and yields a number. NULL is the
// correct answer for a never-vacuumed table and it costs nothing here: date_part is
// strict, so NULL in gives NULL out, and parseValue's failure to parse an empty cell is
// what turns that into "no sample" rather than a 0.
const bloatNextAutovacuumSQL = "pg_catalog.date_part('epoch', last_autovacuum + (SELECT setting::interval FROM pg_catalog.pg_settings WHERE name = 'autovacuum_naptime'))"

// bloatOverThresholdSQL is R7.4's "tables over the autovacuum threshold" as an Extra,
// and the expression is the one the ruling's coalesce finding forces.
//
// The threshold for one table is its own autovacuum_vacuum_threshold if the table sets
// one in pg_class.reloptions, and the autovacuum_vacuum_threshold setting otherwise.
// Expressing "override, else default" is what coalesce is for, and `requirements.md:368`
// rules that coalesce cannot be written here at all: `sql.go:484`'s unqualifiedCall is a
// bare byte scan with no keyword exclusion list, so bare `coalesce(a, b)` returns
// "coalesce" and fails validation, and `pg_catalog.coalesce(...)` is not the fix because
// COALESCE is a SQL-standard conditional *expression* (PostgreSQL §9.22), not a
// pg_catalog function, so the qualified form is a syntax error on the instance. The
// ruling's mandated shape is `CASE … END`.
//
// The ruling's own reasoning — a CASE "has no parentheses and is therefore invisible to
// the check" — is true of the keywords but not of the expression this one needs, and the
// difference is worth stating because it is the reason the two branches are written the
// way they are. unqualifiedCall inspects every "(" and requires the identifier
// immediately left of it (after skipping spaces and tabs) to be a known schema; a "("
// preceded by anything else, including a SQL keyword, is read as a call to that keyword.
// So `THEN (SELECT …)` fails validation with "calls THEN without a schema", and so does
// `WHEN (SELECT …)` or `AND (SELECT …)`. The two branches dodge it in the two ways the
// check leaves open: the override branch puts its subselect inside a qualified call
// (pg_catalog.substring, whose "(" is preceded by "substring" preceded by "pg_catalog."),
// and the default branch puts its subselect behind an operator (`0 + (…)`, whose "(" is
// preceded by "+", which is not an identifier byte). The `0 +` is also the null guard:
// it keeps the branch NULL when pg_settings has no such row, which is what a plain
// greatest(x, 0) would not do, because greatest ignores NULL arguments and would report
// a threshold of 0 — making every table with a dead tuple look over threshold.
//
// The override is read out of reloptions with substring rather than with unnest because
// reloptions is a text[] and the Extra needs a scalar: the ruling reaches reloptions as
// `(SELECT reloptions FROM pg_catalog.pg_class WHERE oid = relid)` — "not by a join" —
// and casting that array to its text form and matching the key is the scalar form of the
// same read. A table with no options at all has NULL reloptions, so substring returns
// NULL, so the CASE takes the default branch, which is the correct answer.
const bloatOverThresholdSQL = "CASE WHEN pg_catalog.substring((SELECT reloptions::text FROM pg_catalog.pg_class WHERE oid = relid), 'autovacuum_vacuum_threshold=([0-9]+)') IS NULL THEN 0 + (SELECT setting::float8 FROM pg_catalog.pg_settings WHERE name = 'autovacuum_vacuum_threshold') ELSE pg_catalog.substring((SELECT reloptions::text FROM pg_catalog.pg_class WHERE oid = relid), 'autovacuum_vacuum_threshold=([0-9]+)')::float8 END"

// bloatNeverAnalyzedSQL is R7.4's "tables never analyzed" as an Extra.
//
// The simplest of the three, and the only one whose SQL is a plain expression: a table
// has never been analyzed when both the manual and the automatic column are NULL. It is
// also the only one of the three with no parenthesis anywhere, which is the property
// `requirements.md:368` names as the reason CASE is safe to write at all — see
// bloatOverThresholdSQL for the case where a CASE still needs help.
//
// It pairs with next_autovacuum rather than duplicating it: a never-analyzed table also
// has no next_autovacuum value (last_autovacuum is NULL for a table that has never been
// vacuumed, and a table can be vacuumed without ever being analyzed), and the ruling
// says so — "NULL when `last_autovacuum` is NULL … is also the correct answer for a
// never-vacuumed table and pairs with `never_analyzed`".
const bloatNeverAnalyzedSQL = "CASE WHEN last_analyze IS NULL AND last_autoanalyze IS NULL THEN 1 ELSE 0 END"

// bloatSpecs is the per-view SQL the matrix cannot express: the two sizes and the
// ratio.
//
// Six Extras, and none of the six is a column: the three R7.2 ones below are its two
// pairs, and the three R7.4 ones are documented on their own constants above.
//
// pg_stat_user_tables stores n_live_tup and n_dead_tup, not their ratio; a table's
// size is pg_total_relation_size(relid) and an index's is pg_relation_size(indexrelid),
// and neither view has a size column at all. They are declared as Extras so the matrix
// stays the single source of the column set (R2.4) while R7.2's three named values
// still exist — the same mechanism database.go uses for R5.2's "database sizes, TXID
// and MXID age", and for the same reason: an Extra is how a derived expression joins a
// projection without forking the query builder.
//
// dead_ratio's denominator is greatest(n_live_tup + n_dead_tup, 1) rather than the sum
// alone because a table that has never been written to has both halves at zero, and a
// float8 division by zero is not an empty cell in Postgres — it is
// `ERROR: division by zero`, which fails the whole SELECT. That is D20's S7 class
// exactly (database.go's WHERE clause is that ruling's fix, "not just the size column,
// the whole row set"): one fresh table would take the entire round down, including the
// index view's query in the same transaction. With the guard the fresh table reports
// 0/1 = 0, which is also the correct answer — a table with no tuples has no dead ones.
//
// Neither view needs a WHERE clause. Both are defined over the connected database's
// pg_class already scoped to user relations, and the role can only see rows in a
// database it has CONNECT on, so there is no template0-style privilege failure to
// filter here; R2.5's schema prefix comes from pgversion.Schema, and ValidateProjection
// checks the three expressions themselves (D20's S10) rather than trusting this
// paragraph — every one of them is schema-qualified, including greatest, which the
// unqualifiedCall check at sql.go:484 inspects exactly like the other two.
var bloatSpecs = map[pgversion.View]QuerySpec{
	pgversion.ViewStatUserTables: {
		Extras: []Extra{
			// R7.2's two pairs.
			{Expr: "pg_catalog.pg_total_relation_size(relid)", Alias: "total_bytes"},
			{Expr: "n_dead_tup::float8 / pg_catalog.greatest(n_live_tup + n_dead_tup, 1)", Alias: "dead_ratio"},
			// R7.4's autovacuum insight, ruled into this file by `requirements.md:368`.
			{Expr: bloatNextAutovacuumSQL, Alias: "next_autovacuum"},
			{Expr: bloatOverThresholdSQL, Alias: "over_autovacuum_threshold"},
			{Expr: bloatNeverAnalyzedSQL, Alias: "never_analyzed"},
		},
	},
	pgversion.ViewStatUserIndexes: {
		Extras: []Extra{
			{Expr: "pg_catalog.pg_relation_size(indexrelid)", Alias: "size"},
		},
	},
}

// BloatCollector is §3.3's bloat collector: the per-table dead-tuple counters and
// size, the dead-tuple ratio, and the per-index scan counters and size that make up
// R7.2's two pairs, plus the three R7.4 autovacuum values the ruling put in this file
// rather than in a collector of their own.
//
// The one judgement in the file is which columns become metrics, and it lives in
// bloatKindOf rather than being spread through the code — the shape wal.go's
// walKindOf and io.go's ioKindOf give their views. The column sets come from the
// matrix (R2.4); the columns that are row identity, a name, or a timestamp are
// classified out of it because they are not values.
//
// The timestamps are the flagged half, and the flag is wal.go's rather than a new one.
// parseValue (sql.go:363) parses numbers and booleans only, so a timestamptz column
// classified kindMetric yields no sample; wal.go:281-292 rules the identical case for
// pg_stat_archiver.last_archived_time — "exporting it as a float would mint a metric
// R5.2 does not name out of a value that is a date", and the metric form "would have to
// be a unix-seconds conversion — a naming rule R9.4 owns (step 4), not one this file
// invents". R9.4 has since minted those names (the archiver row is
// archiver_last_archived_timestamp_seconds), and this file's rows follow it: the six
// last_* columns get _timestamp_seconds Gauge names and are classified kindText, so the
// name exists and the collector emits no sample for it. Extending parseValue to parse
// timestamps is the alternative, and it is a sql.go change that changes every
// collector's behaviour for every text column — not R7.2's subject, which is a ratio
// and two sizes, all three of them numbers.
type BloatCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewBloatCollector builds the collector on top of an opener (R2.7's transaction
// source) and a config (R2.7's timeout, R2.8's application name, R13.11's clock), with
// every unset field resolved by SQLConfig.withDefaults.
func NewBloatCollector(opener SQLOpener, cfg SQLConfig) *BloatCollector {
	c := &BloatCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorBloat,
		Interval: BloatInterval,
		Source:   SourceSQL,
		// CostStandard, per R7.2's second ruling: "one read-only transaction over two
		// views is R2.7's shape, and pg_total_relation_size and pg_relation_size are
		// relcache reads rather than content scans, so the collector does not earn
		// `expensive` the way `statement` and `activity` do."
		Cost:      CostStandard,
		AppliesTo: AppliesToViews(bloatViews...),
		// Timeout unset: R2.1's 10 s default, for the reason database.go gives for the
		// same choice — the timeout is a hang guard and R2.7's statement_timeout is the
		// tuning knob.
		ViewGroups: []string{bloatViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *BloatCollector) Descriptor() Descriptor { return c.desc }

// Collect runs one round: one read-only transaction (R2.7) holding one query per view
// (R2.4), with every row parsed into samples.
//
// The error paths each return the round-trips issued so far, because the Collector
// contract requires it: R13.1's budget is measured from Result.RoundTrips, and a
// collector that returns a zero alongside a partial failure makes a cluster that issued
// one query look like one that issued none. The full argument is on
// DatabaseCollector.Collect; this file inherits it rather than restating it a third
// time after io.go.
func (c *BloatCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	projections, err := c.projections(tgt)
	if err != nil {
		// Zero is correct here: nothing was issued, because building the SQL is what
		// tells us there is nothing to issue.
		return Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		// Also zero: Begin is the round-trip that opens the transaction, and a Begin
		// that failed issued nothing.
		return Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorBloat, err)
	}

	var (
		samples []Sample
		trips   int
	)
	for _, pv := range projections {
		rows, qerr := tx.Query(ctx, pv.Query)
		trips++
		if qerr != nil {
			// The samples parsed from the view that did answer come back with the
			// error; the scheduler publishes nothing on a failed round (R2.2's
			// last-good rule), so returning them costs nothing.
			_ = tx.Rollback()
			return Result{Samples: samples, RoundTrips: trips},
				fmt.Errorf("collector %s: query %s: %w", CollectorBloat, pv.view, qerr)
		}
		samples = append(samples, bloatSamples(pv, c.cfg.Now(), rows)...)
	}

	if cerr := tx.Commit(); cerr != nil {
		// D20's S9: a failed Commit is an error path, and the best-effort rollback that
		// closes the transaction is the driver's job this call performs. The samples
		// come back with the error and the scheduler keeps serving the previous round.
		_ = tx.Rollback()
		return Result{Samples: samples, RoundTrips: trips},
			fmt.Errorf("collector %s: commit: %w", CollectorBloat, cerr)
	}
	return Result{Samples: samples, RoundTrips: trips}, nil
}

// projections builds one query per view for the target's major.
//
// Every failure here is a refusal to guess: an unknown major means the matrix has no
// column set to use (and the scheduler's own applicability filter should have kept the
// round from happening), and a view with no columns on this major means the
// collector's declared applicability and the matrix disagree.
func (c *BloatCollector) projections(tgt Target) ([]viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return nil, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorBloat, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		names := make([]string, 0, len(bloatViews))
		for _, v := range bloatViews {
			names = append(names, string(v))
		}
		return nil, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (one of [%s] has no column set there)",
			CollectorBloat, tgt.PGMajor, strings.Join(names, " "))
	}

	out := make([]viewProjection, 0, len(bloatViews))
	for _, v := range bloatViews {
		p, ok := Project(v, tgt.PGMajor, bloatSpecs[v])
		if !ok {
			return nil, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d", CollectorBloat, v, tgt.PGMajor)
		}
		if err := ValidateProjection(p); err != nil {
			return nil, err
		}
		out = append(out, viewProjection{view: v, Projection: p, database: c.cfg.Database})
	}
	return out, nil
}

// bloatKindOf classifies one column of one of this collector's two views.
//
// The rule is database.go's "metric unless it is not a value", stated as the exceptions
// because the exceptions are the short list — but listed by name rather than by a
// permissive `default: return kindMetric`, which is wal.go's argument rather than
// database.go's. The matrix's column sets for these two views differ by major
// (pg_stat_user_tables is 23 columns on 13-15, 26 on 16-17, 30 on 18;
// pg_stat_user_indexes is 8 on 13-15 and 9 on 16+), so a new column the matrix learns
// about on a future major should be classified here deliberately, not absorbed by a
// default that would classify a future text column as a number.
func bloatKindOf(view pgversion.View, column string) columnKind {
	if column == SourceNowColumn {
		return kindSourceNow
	}

	switch view {
	case pgversion.ViewStatUserTables:
		switch column {
		case "relid":
			return kindID
		case "schemaname", "relname":
			return kindName
		case "last_vacuum", "last_autovacuum", "last_analyze", "last_autoanalyze",
			"last_seq_scan", "last_idx_scan":
			// timestamptz columns: named in the table (R9.4 mints the
			// _timestamp_seconds spellings) and not emitted, per the flag on the type
			// doc and wal.go:281-292's ruling on the same case.
			return kindText
		case "seq_scan", "seq_tup_read", "idx_scan", "idx_tup_fetch",
			"n_tup_ins", "n_tup_upd", "n_tup_del", "n_tup_hot_upd", "n_tup_newpage_upd",
			"vacuum_count", "autovacuum_count", "analyze_count", "autoanalyze_count",
			"total_vacuum_time", "total_autovacuum_time", "total_analyze_time", "total_autoanalyze_time",
			"total_bytes", "dead_ratio",
			// R7.4's three. next_autovacuum is a date_part number and so emits, which is
			// the asymmetry `requirements.md:368` calls intended against the six last_*
			// rows above; the other two are CASE expressions that yield 1 or 0.
			"next_autovacuum", "over_autovacuum_threshold", "never_analyzed":
			return kindMetric
		case "n_live_tup", "n_dead_tup", "n_mod_since_analyze", "n_ins_since_vacuum":
			// Gauges, not counters: a live-tuple count goes down when a vacuum runs, so
			// rate() over it is meaningless and R9.4's type is gauge. They are still
			// values, so they are kindMetric here — the counter/gauge choice lives in
			// the table's typ field, not in this enum.
			return kindMetric
		}
	case pgversion.ViewStatUserIndexes:
		switch column {
		case "relid", "indexrelid":
			return kindID
		case "schemaname", "relname", "indexrelname":
			return kindName
		case "last_idx_scan":
			return kindText
		case "idx_scan", "idx_tup_read", "idx_tup_fetch", "size":
			return kindMetric
		}
	}

	return kindUnknown
}

// bloatSamples turns one view's rows into samples.
//
// It is io.go's ioLabelled shape — one sample per metric column per row, labelled with
// the row's own identity columns — and it has no ioSummed twin, because every
// projection of both views carries at least one name column: pg_stat_user_tables has
// schemaname and relname, pg_stat_user_indexes adds indexrelname. io.go needs the
// branch because pg_stat_io's identity is a three-column tuple and its aggregate shape
// exists for a projection with metrics but no names; here that case cannot happen, so
// the function is the labelled half on its own.
//
// The rules it inherits from ioLabelled are the ones that make a labelled view honest:
// a row with a missing or blank identity column yields no samples at all (a partial
// label set is the invalid-exposition class, not a smaller version of the right one); a
// value that is neither a number nor a boolean yields no sample rather than a 0,
// because storing 0 measures nothing and a 0 for table_dead_tuples reads as a table
// with no dead rows; and the identity half (namespace, cluster, pod, instance_role) is
// the scheduler's to fill from the Target — clock.go's Sample doc says so — so this
// builds only the series-specific labels.
//
// The columns come from the projection rather than from each row's map, for the two
// reasons database.go gives for the same choice: a map's iteration order is random, so
// a collector whose sample order changes every round cannot be tested against a golden
// list; and a row missing a column must not silently shrink the set of families a view
// reports.
//
// StatsReset is zero on every sample here, and that is the matrix's answer rather than
// an omission: neither view has a stats_reset column — their counters reset with the
// database's, which pg_stat_database.stats_reset reports and this collector does not
// read — exactly as database.go's pg_stat_database_conflicts arm notes for the same
// reason. R3.3's epoch is therefore unavailable to these counters by construction, and
// the field stays zero rather than borrowing a value from a view this round did not
// query.
func bloatSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	var (
		metrics []string
		names   []string
	)
	for _, col := range pv.Columns {
		switch bloatKindOf(pv.view, col) {
		case kindMetric:
			metrics = append(metrics, col)
		case kindName:
			names = append(names, col)
		}
	}

	out := make([]Sample, 0, len(rows)*len(metrics))
	for _, row := range rows {
		labels := make(map[string]string, len(names))
		for _, n := range names {
			raw, ok := row.Get(n)
			if !ok || strings.TrimSpace(raw) == "" {
				// One missing identity column means the row cannot be labelled at all.
				labels = nil
				break
			}
			labels[n] = raw
		}
		if labels == nil {
			continue
		}

		for _, col := range metrics {
			raw, ok := row.Get(col)
			if !ok {
				continue
			}
			v, ok := parseValue(raw)
			if !ok {
				continue
			}
			out = append(out, Sample{
				Metric:     MetricName(pv.view, col),
				Labels:     labels,
				Value:      v,
				SourceTime: sourceTimeOf(row),
				IngestTime: now,
				// No StatsReset: see the doc above.
			})
		}
	}
	return out
}
