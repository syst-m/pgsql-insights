package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// CollectorDatabase is §3.3's name for this collector, and the `collector` label
// value on both R2.2 families for its rounds.
//
// §3.3's row is the whole scope statement for the file:
//
//	| `database` | 60 s | S2 `pg_stat_database`, `pg_database`, `pg_stat_database_conflicts` | |
//
// which is three views, one source, one interval, and nothing about how the SQL
// is written — that part is R2.5, R2.7 and R2.8, and all three live in sql.go.
// This collector's job is to use them, not to restate them: the file below builds
// its queries through the matrix (R2.4), runs them in one read-only transaction
// (R2.7), and reports the round-trips it issued (R13.1).
const CollectorDatabase = "database"

// DatabaseInterval is §3.3's interval for the database collector (60 s), stated
// as a constant so R2.3's --interval.database override has a declared default to
// override rather than a literal buried in a struct literal.
const DatabaseInterval = 60 * time.Second

// databaseViewGroup is the collector's single R2.7 view group.
//
// One group for the three views, not three groups for three views. The wording
// here used to justify that with "six to nine round-trips for one collector, 78+
// for the phase" (D20's S6), which counted *round-trips* in the statement sense
// for a field that reports the query sense: R13.1 counts queries issued — thirteen
// collectors at three queries each is 39, and a statement-counting reading puts
// the same round at 39 + 78 = 117, unsatisfiable at any collector count above four
// (requirements.md R13.1, D20's ruling on it). So say transactions and statements
// where those are meant, and reserve "round-trips" for Result.RoundTrips.
//
// What three groups would actually cost: three transactions instead of one (three
// BEGIN/COMMIT pairs, six control statements R13.1 does not count), three
// connection checkouts from the pooler instead of one, and — the part that changes
// the data rather than the budget — three separate snapshots instead of one, so a
// pg_database row and a pg_stat_database row could disagree about whether a
// database exists. One group per collector is what Descriptor.ViewGroups exists to
// express, and R2.7's "one read-only transaction per view group" is the rule it
// implements.
const databaseViewGroup = "database"

// databaseViews are §3.3's three sources for this collector, in the order their
// queries are issued.
var databaseViews = []pgversion.View{
	pgversion.ViewStatDatabase,
	pgversion.ViewDatabase,
	pgversion.ViewStatDatabaseConflicts,
}

// databaseSpecs is the per-view SQL the matrix cannot express: pg_database's
// derived extras, and its CONNECT filter.
//
// The extras are R5.2's "database sizes, TXID and MXID age", and none of the three
// is a column: pg_database stores datfrozenxid and datminmxid, whose *age* is
// age(datfrozenxid) — a function of the server's current transaction ID, so it
// cannot be computed client-side — and a database's size is pg_database_size(oid),
// not a column at all. They are declared as Extras so the matrix stays the single
// source of the column set (R2.4) while R5.2's three named values still exist.
// Both functions are schema-qualified per R2.5, and validateProjection now checks
// the expression itself (D20's S10) instead of trusting this comment.
//
// The WHERE clause is D20's S7, and it is the reason QuerySpec exists:
// pg_database_size(oid) requires CONNECT on that database (or pg_read_all_stats),
// and R1.8's zero-config <cluster>-app login has CONNECT on the app database but
// not on template0. Without the filter the whole SELECT throws — not just the size
// column, the whole row set — so the round loses pg_database *and*
// pg_stat_database_conflicts and reports an error where it should report the two
// databases the role can see. With it, the query returns exactly the databases we
// can measure, which is also the honest answer to "what databases are there": the
// ones this credential is allowed to look at.
var databaseSpecs = map[pgversion.View]QuerySpec{
	pgversion.ViewDatabase: {
		Extras: []Extra{
			{Expr: "pg_catalog.pg_database_size(oid)", Alias: "size"},
			{Expr: "pg_catalog.age(datfrozenxid)", Alias: "frozenxid_age"},
			{Expr: "pg_catalog.age(datminmxid)", Alias: "minmxid_age"},
		},
		Where: "pg_catalog.has_database_privilege(oid, 'CONNECT')",
	},
}

// DatabaseCollector is §3.3's database collector: the per-database counters
// (backends, transactions, tuples, blocks, temp usage, conflicts, deadlocks) and
// the per-database sizes and XID/MXID ages that make up R5.2's database card.
//
// The one judgement in the file is which columns become metrics, and it lives in
// columnKind rather than being spread through the code: the matrix's column list
// is the candidate set (R2.4), and the columns that are row identity, session
// settings or a second timestamp are classified out of it because they are not
// values.
//
// Spec judgement, flagged rather than resolved silently: R5.2's prose names
// families ("transactions (commit/rollback/TPS), tuples
// (returned/fetched/inserted/updated/deleted), blocks (hit/read, cache hit ratio),
// temp (files/bytes), deadlocks, backends by state and by application name,
// database sizes, TXID and MXID age, WAL volume and generation rate,
// checkpoints"), which is a capability list and not a closed set of metric names —
// it does not name conflicts, checksum_failures, blk_read_time, session_time, the
// sessions_* counters or PG18's parallel_workers_* columns, all of which
// pg_stat_database has and R2.4 puts in the query. This collector exports every
// numeric column the matrix returns, on the reading that §3.3 names the *view* as
// the source and R2.4 says the query is the matrix's column set; the alternative —
// a curated subset mirroring R5.2's wording — would be a second table to maintain
// beside the matrix's own, and R9.4/R9.7 (step 4) are the normative naming surface
// that has to reconcile the two. A3 should rule on which set R9.7's "the full R5.2
// set" exports.
type DatabaseCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewDatabaseCollector builds the collector on top of an opener (R2.7's
// transaction source) and a config (R2.7's timeout, R2.8's application name,
// R13.11's clock), with every unset field resolved by SQLConfig.withDefaults.
func NewDatabaseCollector(opener SQLOpener, cfg SQLConfig) *DatabaseCollector {
	c := &DatabaseCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:      CollectorDatabase,
		Interval:  DatabaseInterval,
		Source:    SourceSQL,
		Cost:      CostStandard,
		AppliesTo: AppliesToViews(databaseViews...),
		// Timeout unset: R2.1's 10 s default. Descriptor's own rule says a
		// collector whose SQL is known to be cheap does not set a shorter one,
		// because the timeout is a hang guard and R2.7's statement_timeout is the
		// tuning knob.
		ViewGroups: []string{databaseViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *DatabaseCollector) Descriptor() Descriptor {
	return c.desc
}

// Collect runs one round: one read-only transaction (R2.7) holding one query per
// view (R2.4), with every row parsed into samples.
//
// The error paths each return the round-trips issued so far, because the
// Collector contract requires it: R13.1's budget is measured from Result.
// RoundTrips, and a collector that returns a zero alongside a partial failure
// makes a cluster that issued two queries look like one that issued none.
func (c *DatabaseCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	projections, err := c.projections(tgt)
	if err != nil {
		// Zero is correct here: nothing was issued, because building the SQL is
		// what tells us there is nothing to issue.
		return Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		// Also zero: Begin is the round-trip that opens the transaction, and a
		// Begin that failed issued nothing.
		return Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorDatabase, err)
	}

	var (
		samples []Sample
		trips   int
	)
	for _, pv := range projections {
		rows, qerr := tx.Query(ctx, pv.Query)
		trips++
		if qerr != nil {
			// The samples parsed from the views that did answer come back with the
			// error. The scheduler publishes nothing on a failed round (that
			// non-publish is R2.2's last-good rule), so returning them costs
			// nothing and gives a caller that logs the result something to log.
			_ = tx.Rollback()
			return Result{Samples: samples, RoundTrips: trips},
				fmt.Errorf("collector %s: query %s: %w", CollectorDatabase, pv.view, qerr)
		}
		samples = append(samples, samplesFromRows(pv, c.cfg.Now(), rows)...)
	}

	if cerr := tx.Commit(); cerr != nil {
		// The data was read; the transaction just failed to close. That is still a
		// failed round — a connection that cannot commit is a connection whose next
		// query is suspect — so the samples come back with the error and the
		// scheduler keeps serving the previous round.
		//
		// D20's S9: sql.go's Tx doc says "Rollback on every error path", and a
		// failed Commit is an error path. A driver that failed the COMMIT may still
		// hold the transaction open on a connection the pooler hands to the next
		// round, and the rollback that closes it is best-effort — its error is the
		// commit's, which is why the result is discarded here exactly as it is in
		// the query path above.
		_ = tx.Rollback()
		return Result{Samples: samples, RoundTrips: trips},
			fmt.Errorf("collector %s: commit: %w", CollectorDatabase, cerr)
	}
	return Result{Samples: samples, RoundTrips: trips}, nil
}

// viewProjection pairs one view with the query built for a specific major.
type viewProjection struct {
	view pgversion.View
	Projection
}

// projections builds one query per view for the target's major.
//
// Every failure here is a refusal to guess: an unknown major means the matrix has
// no column set to use (and the scheduler's own applicability filter should have
// kept the round from happening), and a view with no columns on this major means
// the collector's declared applicability and the matrix disagree.
func (c *DatabaseCollector) projections(tgt Target) ([]viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return nil, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorDatabase, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		names := make([]string, 0, len(databaseViews))
		for _, v := range databaseViews {
			names = append(names, string(v))
		}
		return nil, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (one of [%s] has no column set there)",
			CollectorDatabase, tgt.PGMajor, strings.Join(names, " "))
	}

	out := make([]viewProjection, 0, len(databaseViews))
	for _, v := range databaseViews {
		p, ok := Project(v, tgt.PGMajor, databaseSpecs[v])
		if !ok {
			return nil, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d", CollectorDatabase, v, tgt.PGMajor)
		}
		if err := validateProjection(p); err != nil {
			return nil, err
		}
		out = append(out, viewProjection{view: v, Projection: p})
	}
	return out, nil
}

// columnKind is what one column of one of this collector's views contributes to
// a result.
type columnKind int

const (
	// kindMetric is a value: it becomes a sample.
	kindMetric columnKind = iota
	// kindName is the database name — R9.3's datname label, not a value.
	kindName
	// kindID is a row identifier (oid, datid, datdba, dattablespace) or a raw
	// transaction-ID column. IDs are join keys and function arguments; exporting
	// pg_database_datfrozenxid as a metric would put a number on a wire that only
	// means something as age(datfrozenxid), which R5.2 is the one that asks for.
	kindID
	// kindEpoch is the view's stats_reset: it fills Sample.StatsReset (R3.3's
	// counter epoch) rather than becoming a sample of its own.
	kindEpoch
	// kindSourceNow is the now() alias every query carries: it fills
	// Sample.SourceTime (R13.11's first timestamp), not a sample.
	kindSourceNow
	// kindText is a text value with no metric meaning (an encoding name, a
	// collation, an ACL, a failure timestamp that is not the view's epoch). It is
	// parsed and not exported: a sample whose value is a string is a sample whose
	// number is a lie.
	kindText
	// kindUnknown is a column this file has no opinion about — which the test
	// treats as a failure, because it means the matrix gained a column and the
	// collector did not notice.
	kindUnknown
)

// String names a kind for test output.
func (k columnKind) String() string {
	switch k {
	case kindMetric:
		return "metric"
	case kindName:
		return "name"
	case kindID:
		return "id"
	case kindEpoch:
		return "epoch"
	case kindSourceNow:
		return "source_now"
	case kindText:
		return "text"
	default:
		return "unknown"
	}
}

// kindOf classifies one column of one of the database collector's views.
//
// The rule is "metric unless it is not a value", stated as the exceptions because
// the exceptions are the short list: identifiers, the database name, the view's
// epoch, the now() alias, and text settings. Everything else in these three views
// is a counter or a gauge, and a new column the matrix learns about (PG18's
// parallel_workers_to_launch, PG16's confl_active_logicalslot) is a metric with no
// change to this file — which is the point of classifying by role instead of
// listing metrics one by one.
func kindOf(view pgversion.View, column string) columnKind {
	if column == SourceNowColumn {
		return kindSourceNow
	}

	switch view {
	case pgversion.ViewDatabase:
		switch column {
		case "datname":
			return kindName
		case "oid", "datdba", "datfrozenxid", "datminmxid", "dattablespace":
			return kindID
		case "encoding", "datcollate", "datctype", "datlocprovider",
			"daticulocale", "daticurules", "datcollversion", "datlocale", "datacl":
			return kindText
		}
		// The three derived expressions (size, frozenxid_age, minmxid_age) and the
		// booleans (datistemplate, datallowconn, dathasloginevt) fall through to
		// kindMetric: a boolean is a value, and R9.3 has no boolean type, so
		// parseValue maps it to 1 and 0 the way every Postgres exporter does.
		return kindMetric

	case pgversion.ViewStatDatabase:
		switch column {
		case "datid":
			return kindID
		case "datname":
			return kindName
		case "stats_reset":
			return kindEpoch
		case "checksum_last_failure":
			// A timestamp, and not the view's epoch (that is stats_reset, which is
			// the one R3.3 needs). Sample has one timestamp slot per role and this
			// column is neither; exporting it as a float would mint a metric R5.2
			// does not name out of a value that is a date.
			return kindText
		}
		return kindMetric

	case pgversion.ViewStatDatabaseConflicts:
		switch column {
		case "datid":
			return kindID
		case "datname":
			return kindName
		}
		// This view has no stats_reset: the conflict counters are reset with the
		// database's, and the matrix's column set for it says so.
		return kindMetric
	}

	return kindUnknown
}

// samplesFromRows turns one view's rows into samples.
//
// The columns come from the projection rather than from each row's map, for two
// reasons that are both load-bearing: a map's iteration order is random, and a
// collector whose sample order changes every round cannot be tested against a
// golden sample list; and a row that is missing a column (a driver that dropped a
// NULL) must not silently shrink the set of families a database reports.
func samplesFromRows(pv viewProjection, now time.Time, rows []Row) []Sample {
	var out []Sample

	for _, row := range rows {
		var (
			source  time.Time
			epoch   time.Time
			datname string
			hasName bool
		)
		for _, col := range pv.Columns {
			raw, ok := row.Get(col)
			if !ok {
				continue
			}
			switch kindOf(pv.view, col) {
			case kindSourceNow:
				if t, ok := parseTimestamp(raw); ok {
					source = t
				}
			case kindEpoch:
				if t, ok := parseTimestamp(raw); ok {
					epoch = t
				}
			case kindName:
				datname, hasName = raw, true
			}
		}

		// D20's S8: every pg_stat_database row set includes the shared-objects row,
		// whose datname is NULL — and R9.3's closed label set has no slot for
		// datname="", so the row used to export pgsi_stat_database_xact_commit{datname=""},
		// a series the contract does not have. The row is real data (shared buffers,
		// shared block hits) and the honest answer is that R9.3 cannot name it, so it
		// is skipped rather than labelled with an empty string. Only a view that
		// *should* have a name and has none is the case: a row whose projection has
		// no name column at all (hasName false) is not filtered.
		if hasName && datname == "" {
			continue
		}

		// R9.3's label contract: database series add datname. The identity half
		// (namespace, cluster, pod, instance_role) is the scheduler's to fill from
		// the Target — clock.go's Sample doc says so, and step 1's own scheduler
		// tests build samples carrying only the series-specific label.
		labels := map[string]string{"datname": datname}

		for _, col := range pv.Columns {
			if kindOf(pv.view, col) != kindMetric {
				continue
			}
			raw, ok := row.Get(col)
			if !ok {
				continue
			}
			v, ok := parseValue(raw)
			if !ok {
				// A value that is neither a number nor a boolean is not a value, and
				// storing 0 for it would be a measurement of nothing.
				continue
			}
			out = append(out, Sample{
				Metric:     MetricName(pv.view, col),
				Labels:     labels,
				Value:      v,
				SourceTime: source,
				IngestTime: now,
				StatsReset: epoch,
			})
		}
	}

	return out
}
