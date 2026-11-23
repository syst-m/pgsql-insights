package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// CollectorWAL is §3.3's name for this collector, and the `collector` label value
// on both R2.2 families for its rounds.
//
// §3.3's row is the whole scope statement:
//
//	| `wal` | 60 s | S2 `pg_stat_wal` (14+), `pg_stat_archiver`, `pg_wal` dir via S3 | |
//
// which names two S2 views and one S3 source, and this file implements the two S2
// halves. The "(14+)" is the row's own applicability statement for pg_stat_wal and
// it is implemented literally: AppliesToViews below derives 14-18 from the matrix,
// so a PG13 target is refused outright rather than collected with a hole in it.
// R13.9 makes that cost nothing real — PG13 and PG14 are `impossible` on CNPG 1.30
// and are "documented as fixture-tested SQL-layer rows only".
//
// The row's third source, the pg_wal directory size, is not here because it is not
// SQL: it is cnpg_collector_pg_wal (docs/research/cloudnativepg.md §5.1), which
// §3.3 assigns to the instance-metrics collector (requirements.md:206, "Parses the
// `cnpg_*` exposition") and R9.7 re-exports under pgsi_ in step 4. collector.go's
// Descriptor.Source rule — "One collector, one source" — settles the rest: this
// collector declares SourceSQL, and the directory size arrives with the S3 parse
// rather than as a SQL query that has no directory in it.
const CollectorWAL = "wal"

// WALInterval is §3.3's interval for the wal collector (60 s), stated as a constant
// so R2.3's --interval.wal override has a declared default to override rather than a
// literal buried in a struct literal.
const WALInterval = 60 * time.Second

// walViewGroup is the collector's single R2.7 view group.
//
// One group for the two views, for the reason database.go's equivalent spells out
// (one transaction so the two views agree about one moment, and so R13.1's
// round-trip count is the query count rather than the query count plus a second
// BEGIN/COMMIT pair).
const walViewGroup = "wal"

// walViews are §3.3's two S2 sources for this collector, in the order their queries
// are issued.
//
// The matrix has a third wal view — pg_stat_wal_receiver, the standby-side receiver
// stats, 15 identical columns on 13-18 — and §3.3's row does not name it, so it is
// not in this set. R2.4's rule is that the row's source list is the collector's view
// list, and adding a view the row does not name is a scope change that belongs in
// requirements.md first.
//
// Nor are the checkpoint counters, which R5.2 lists in the same sentence as WAL
// volume: §3.3's io row (requirements.md:200) names pg_stat_bgwriter and
// pg_stat_checkpointer, so R5.2's "checkpoints" family arrives with that collector
// and this file does not mint one from pg_stat_wal.
var walViews = []pgversion.View{
	pgversion.ViewStatWAL,
	pgversion.ViewStatArchiver,
}

// WALCollector is §3.3's wal collector: the WAL generation counters and the
// archiver, which together are R5.2's "WAL volume and generation rate" and the
// archive half of R5.1's card set.
//
// The one judgement in the file is which *families* a row set produces and what a
// family's value means when a view returns more than one row. It is the third
// distinct answer to that question among the SQL collectors: database.go names one
// family per numeric column and labels it datname, settings.go and replication.go
// count and take maxima, and this file sums.
//
// pg_stat_wal is one row per database. PostgreSQL has eleven columns on PG14
// (datid, datname, and the nine the matrix lists) and seven on PG18 (datid, datname,
// and the five it lists), and R9.3's contract says database series add datname — so
// the per-database shape is what the contract asks for and what the projection
// cannot express, because the matrix's column set for this view has no datname to
// put in the label. That set came from docs/research/cloudnativepg.md:2099-2107,
// which counts statistic columns ("PG14-17 = 9, PG18 = 5") where its pg_database and
// pg_stat_database sets do include datname; the collector inherited the omission.
// Without the column, N rows against one unlabelled family is N samples with
// identical label sets — D20's S8 bug in a new costume: the exposition is invalid,
// and the surviving value is whichever row the driver returned last.
//
// The sum is the answer that is correct at every major and survives the matrix fix.
// Σ over databases of wal_bytes is the cluster's WAL volume — R5.2's word is
// "volume", and rate() over it is the "generation rate" it names — and it keeps
// R13.4's CI-checkable series count computable from R9.7's union (8 families from
// pg_stat_wal on 14-17, 4 on 18, plus 2 from pg_stat_archiver) instead of depending
// on how many databases a cluster happens to have. CNPG's collector faces the same
// view with the same shape (docs/research/cloudnativepg.md:1518: one series per
// column, no database label), so the reference implementation also declines to label
// these per database; its label choice is stats_reset, which R9.3 forbids copying
// (the epoch is R3.3's Sample.StatsReset field, not a label) and which CNPG itself
// had to fix when a NULL there broke Prometheus scraping on PG18
// (cloudnativepg.md:329). When the matrix gains datid/datname, per-database series
// become possible *in addition* and the sum stays as the unlabelled aggregate beside
// them, so nothing here has to be undone — which is why the note below is a flag and
// not a rewrite.
//
// Flagged for A3, in the same register as database.go's and settings.go's flags: the
// matrix's pg_stat_wal column set is missing datid and datname, and this file's
// answer is the cluster-wide sum. If A3 rules that R9.7's union means per-database
// series here, the fix is a matrix change (matrix_data.go's two pg_stat_wal sets,
// the research doc's §7.4 table, the Render golden) plus a per-row branch in
// walSamples — and walKindOf already makes that change loud, by returning
// kindUnknown for both new columns so TestWALKindOfCoversEveryMatrixColumn fails
// before anyone ships a collector that classifies a name as a number.
type WALCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewWALCollector builds the collector on top of an opener (R2.7's transaction
// source) and a config (R2.7's timeout, R2.8's application name, R13.11's clock),
// with every unset field resolved by SQLConfig.withDefaults.
func NewWALCollector(opener SQLOpener, cfg SQLConfig) *WALCollector {
	c := &WALCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorWAL,
		Interval: WALInterval,
		// SourceSQL, from the two S2 views this file queries. §3.3's row also lists
		// an S3 source (the pg_wal directory), and collector.go's rule is that a
		// collector declares one source rather than two: the S3 half belongs to the
		// instance-metrics collector, so declaring S3 here would claim a collector
		// that reads a directory this file never opens.
		Source: SourceSQL,
		// CostStandard: collector.go's own wording names this collector —
		// "CostStandard costs one read-only transaction per view group (R2.7) and
		// single-digit queries: the database, wal, settings and replication
		// collectors".
		Cost: CostStandard,
		// AppliesToViews over both views: pg_stat_wal exists on 14-18 and
		// pg_stat_archiver on 13-18, so the intersection is 14-18 — which is the
		// row's "(14+)" derived from the matrix instead of typed in by hand.
		AppliesTo: AppliesToViews(walViews...),
		// Timeout unset: R2.1's 10 s default, for the reason database.go gives —
		// the round timeout is a hang guard and R2.7's statement_timeout is the
		// tuning knob.
		ViewGroups: []string{walViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *WALCollector) Descriptor() Descriptor {
	return c.desc
}

// Collect runs one round: one read-only transaction (R2.7) holding one query per
// view (R2.4), with every row parsed into samples.
//
// The error paths each return the round-trips issued so far, because the Collector
// contract requires it and R13.1's budget is measured from Result.RoundTrips.
func (c *WALCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	projections, err := c.projections(tgt)
	if err != nil {
		return Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		return Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorWAL, err)
	}

	var (
		samples []Sample
		trips   int
	)
	for _, pv := range projections {
		rows, qerr := tx.Query(ctx, pv.Query)
		trips++
		if qerr != nil {
			_ = tx.Rollback()
			return Result{Samples: samples, RoundTrips: trips},
				fmt.Errorf("collector %s: query %s: %w", CollectorWAL, pv.view, qerr)
		}
		samples = append(samples, walSamples(pv, c.cfg.Now(), rows)...)
	}

	if cerr := tx.Commit(); cerr != nil {
		return Result{Samples: samples, RoundTrips: trips},
			fmt.Errorf("collector %s: commit: %w", CollectorWAL, cerr)
	}
	return Result{Samples: samples, RoundTrips: trips}, nil
}

// projections builds one query per view for the target's major, reusing the
// viewProjection pairing and the refusal rules database.go established: no major
// means no column set to build SQL from, and a view the matrix does not have on
// this major means the declared applicability and the matrix disagree. Both are
// checked before Begin, so a round with nothing to run costs no connection.
//
// Neither view needs a QuerySpec: every column either becomes a sample directly or
// is one of the two non-metric kinds, so there is no derived expression to add —
// unlike pg_stat_replication, whose interval columns need date_part Extras to
// become numbers at all.
func (c *WALCollector) projections(tgt Target) ([]viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return nil, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorWAL, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		names := make([]string, 0, len(walViews))
		for _, v := range walViews {
			names = append(names, string(v))
		}
		return nil, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (one of [%s] has no column set there)",
			CollectorWAL, tgt.PGMajor, strings.Join(names, " "))
	}

	out := make([]viewProjection, 0, len(walViews))
	for _, v := range walViews {
		p, ok := Project(v, tgt.PGMajor, QuerySpec{})
		if !ok {
			return nil, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d", CollectorWAL, v, tgt.PGMajor)
		}
		if err := validateProjection(p); err != nil {
			return nil, err
		}
		out = append(out, viewProjection{view: v, Projection: p})
	}
	return out, nil
}

// walKindOf classifies one column of one of the wal collector's views.
//
// It is this file's classifier rather than database.go's kindOf because kindOf knows
// only the three database views and returns kindUnknown for everything else, and
// because the answer differs: pg_stat_wal's numbers are summed into one unlabelled
// family, so its metric columns need no name column to label them, and its two
// "last … time" columns are a case pg_stat_database only has as checksum_last_failure.
//
// Every column is listed by name, including the metrics, and anything unlisted
// returns kindUnknown. That is the enforcement half of the flag on the type doc: the
// matrix's pg_stat_wal set is missing datid and datname, and when it gains them this
// function returns kindUnknown for both and TestWALKindOfCoversEveryMatrixColumn
// fails. A permissive `default: return kindMetric` — which is what database.go's
// pg_stat_database arm uses — would instead classify datname as a number and the
// test would pass while the collector exported pgsi_stat_wal_datname.
func walKindOf(view pgversion.View, column string) columnKind {
	if column == SourceNowColumn {
		return kindSourceNow
	}

	switch view {
	case pgversion.ViewStatWAL:
		switch column {
		case "wal_records", "wal_fpi", "wal_bytes", "wal_buffers_full",
			"wal_write", "wal_sync", "wal_write_time", "wal_sync_time":
			// The first four exist on every major this collector applies to; the
			// last four are the PG14-17 set (PG18 removed them, which is R2.4's
			// 9 → 5 shrink and why the 18 projection yields four families).
			return kindMetric
		case "stats_reset":
			return kindEpoch
		}
	case pgversion.ViewStatArchiver:
		switch column {
		case "archived_count", "failed_count":
			return kindMetric
		case "last_archived_wal", "last_failed_wal":
			// WAL segment file names ("000000010000000000000001"): text in
			// R9.1's sense, and the API's half of the archive answer.
			return kindText
		case "last_archived_time", "last_failed_time":
			// Timestamps, and not the view's epoch (that is stats_reset, the one
			// R3.3 needs) — database.go's checksum_last_failure is the same case in
			// pg_stat_database: "Sample has one timestamp slot per role and this
			// column is neither; exporting it as a float would mint a metric R5.2
			// does not name out of a value that is a date." R5.2's WAL half names
			// volume and rate, not last-archive-time; and parseValue cannot parse a
			// timestamp, so the metric form would have to be a unix-seconds
			// conversion — a naming rule R9.4 owns (step 4), not one this file
			// invents.
			return kindText
		case "stats_reset":
			return kindEpoch
		}
	}
	return kindUnknown
}

// walSamples turns one view's rows into one sample per metric column, summed over
// the rows.
//
// The columns come from the projection rather than from each row's map, for the two
// reasons samplesFromRows gives: a map's iteration order is random, so a collector
// whose sample order changes every round cannot be tested against a golden list; and
// a row missing a column must not silently shrink the set of families a view reports.
//
// Summing is also what lets one builder serve both views: pg_stat_wal is one row per
// database and pg_stat_archiver is exactly one row cluster-wide, so for the archiver
// the sum over rows is the value itself. A column with no parseable value in any row
// produces no sample at all rather than a 0 — the rule streamingSamples states ("No
// series is the honest answer, and it is the same rule gucSamples applies to a string
// GUC"), and the one a sum needs most, because a 0 for wal_bytes on a cluster whose
// rows all failed to parse reads as a database that has written no WAL.
func walSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	var (
		metrics  []string
		epochCol string
	)
	for _, col := range pv.Columns {
		switch walKindOf(pv.view, col) {
		case kindMetric:
			metrics = append(metrics, col)
		case kindEpoch:
			epochCol = col
		}
	}

	out := make([]Sample, 0, len(metrics))
	for _, col := range metrics {
		var (
			sum   float64
			have  bool
			epoch time.Time
		)
		for _, row := range rows {
			raw, ok := row.Get(col)
			if !ok {
				continue
			}
			v, ok := parseValue(raw)
			if !ok {
				continue
			}
			sum += v
			have = true

			// R3.3's counter epoch for a summed family is the latest stats_reset
			// among the rows that contributed to it. With per-database resets the
			// sum has one discontinuity per component and the newest is the one a
			// rate() over the sum breaks on, so it is the only answer that describes
			// the series rather than one row of it. A NULL stats_reset parses to the
			// zero time and never wins a max, so a row without an epoch contributes
			// its value and not an epoch (the PG18 case cloudnativepg.md:329 records
			// breaking CNPG's scraping).
			if epochCol != "" {
				if raw, ok := row.Get(epochCol); ok {
					if t, ok := parseTimestamp(raw); ok && t.After(epoch) {
						epoch = t
					}
				}
			}
		}
		if !have {
			continue
		}

		out = append(out, Sample{
			Metric: MetricName(pv.view, col),
			// No labels: the identity half is the scheduler's to add from the Target,
			// and this is a cluster aggregate, not one of R9.3's database series (see
			// the type doc for why it cannot be one yet).
			Labels:     map[string]string{},
			Value:      sum,
			SourceTime: sourceTimeOf(rows[0]),
			IngestTime: now,
			StatsReset: epoch,
		})
	}
	return out
}
