package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// CollectorIO is §3.3's name for this collector, and the `collector` label value on
// both R2.2 families for its rounds.
//
// §3.3's row is the whole scope statement:
//
//	| `io` | 60 s | S2 `pg_stat_io` (16+), `pg_stat_slru` (13+), `pg_stat_bgwriter`/`pg_stat_checkpointer` | Version-gated; S3 carries no `pg_stat_io` (§3.2 caveat) |
//
// which names four S2 views and one version caveat, and this file implements all
// four in one collector. R2.1's rule is one collector per source and R2.7's is one
// transaction per view group; four views that are all S2 and all read in one round
// are one group, the same way wal.go's two views are one group rather than two.
//
// A3's ruling at 1b11199:IMPLEMENTATION.md:766 settled the phase question this file
// reopens — it is Phase 2's, not Phase 7's, because §3.3's collection table is
// Phase 2's scope document and `collector.Registry` (R2.1, Phase 2's) is the only
// host a SQL collector can have; :116's scope line names "database/settings/
// replication/wal" and omits io, which the ruling calls a scope-line omission
// rather than a re-assignment, fixed by §3.10 gaining the row and §3.12's entry
// gaining an amendment that keeps its wording (the D18 pattern). The same ruling
// files this file's ~30 new `exportNames` rows under R9.4 (Phase 2's, at :116's
// "R9.2–R9.4") rather than R9.7 (Phase 7's), because R9.7's own sentence routes
// them in: "the export set is therefore the union of R5.2's enumerated families and
// the numeric columns R2.4's matrix returns for the target major, and R9.4's naming
// function must enumerate that union" (requirements.md:400). The rows themselves,
// and the reasoning behind each name, are in naming.go's pg_stat_io and
// pg_stat_slru sections; this docblock carries only the two consequences that are
// visible from here.
//
// The first is that the row's fourth view is not a fourth independent source.
// pg_stat_bgwriter and pg_stat_checkpointer are one source with two spellings —
// R2.4's fourth enumerated case, modelled in succession.go as a move of five
// columns at PG17 plus two retirements and four stays — and R2.4's whole argument
// against a rename table is that counting them as two views double-counts one
// thing. So the pair cannot be an independent member of this collector's
// applicability intersection: intersecting four views where two are one source
// yields a major set derived from a duplicated input. The pair is handled in
// `projections` instead, by including whichever member the matrix has on the
// target major, and the applicability function takes the two views that *are*
// independent. See the AppliesTo comment on the descriptor for the major set that
// produces and why it is the row's own "(16+)" rather than a third opinion.
//
// The second is wal.go:57-60's standing promise:
//
//	Nor are the checkpoint counters, which R5.2 lists in the same sentence as WAL
//	volume: §3.3's io row (requirements.md:200) names pg_stat_bgwriter and
//	pg_stat_checkpointer, so R5.2's "checkpoints" family arrives with that collector
//	and this file does not mint one from pg_stat_wal.
//
// which this file honours by reading both checkpoint sources. The *families* R5.2
// names are already in naming.go (rows :412-434, written by step 4 from the
// succession pair) and the R5.2 requirement itself is Phase 7's and A2's — the
// ruling's last paragraph says so explicitly ("the R5.2 checkpoints/blocks family
// that renders its numbers is Phase 7's and A2's ... so A1's file unblocks A2's
// requirement without moving it"). Nothing here mints a checkpoint name; it feeds
// rows to names that exist.
const CollectorIO = "io"

// IOInterval is §3.3's interval for the io collector (60 s), stated as a constant so
// R2.3's --interval.io override has a declared default to override rather than a
// literal buried in a struct literal.
const IOInterval = 60 * time.Second

// ioViewGroup is the collector's single R2.7 view group.
//
// One group for the four views, for the reason database.go's equivalent spells out
// (one transaction so the views agree about one moment, and so R13.1's round-trip
// count is the query count rather than the query count plus extra BEGIN/COMMIT
// pairs). Four views in one group is four queries, which is inside the same
// single-digit budget collector.go assigns to CostStandard.
const ioViewGroup = "io"

// ioViews are §3.3's four S2 sources for this collector, in the order their queries
// are issued: the two views that name the collector first, then the succession pair.
//
// The order is not cosmetic. `projections` walks this list and the two independent
// views come first, so a target where the matrix and the declared applicability
// disagree fails on the first projection it checks rather than on the fourth.
var ioViews = []pgversion.View{
	pgversion.ViewStatIO,
	pgversion.ViewStatSLRU,
	pgversion.ViewStatBgwriter,
	pgversion.ViewStatCheckpointer,
}

// IOCollector is §3.3's io collector: the block-IO counters per backend type, the
// SLRU counters per segment class, and the checkpoint counters — which together are
// R5.2's "checkpoints" family plus the I/O half of R5.1's System Insights card set.
//
// The one judgement in the file is the same one wal.go's type doc names as the only
// real question a multi-row SQL view poses: which *families* a row set produces and
// what a family's value means when a view returns more than one row. Among the
// existing SQL collectors there are now three answers — database.go names one family
// per numeric column and labels it datname, settings.go and replication.go count and
// take maxima, wal.go sums — and this file takes a fourth, because the four views it
// reads are not one shape.
//
// pg_stat_io and pg_stat_slru are labelled per row. Their matrix column sets carry
// the row's identity columns (backend_type/object/context and name respectively)
// *in the projection*, so the labels R9.3's shape rule admits are available, and
// database.go's precedent — one family per numeric column, labelled with the column
// that names the row — applies directly. Summing is the wrong answer here for a
// reason stronger than taste: pg_stat_io's entire content is the breakdown. Σ
// `reads` across backend types answers "how many block reads did the cluster do",
// which is a question pg_stat_database's already-exported db_blocks_read_total
// answers at database granularity, so the sum would duplicate an existing family's
// meaning while discarding the only thing the new view adds — and R5.2 never names
// an I/O family at all, so there is no consumer asking for the sum.
//
// pg_stat_bgwriter and pg_stat_checkpointer are summed. Both are single-row
// cluster-wide views with no identity column in their matrix sets (there is no
// datname to label with, and on PG17+ the two views' column sets are disjoint —
// bgwriter keeps four columns, checkpointer has nine — so summing them cannot
// merge two same-named counters into one sample). For a one-row view the sum is the
// value, which is exactly wal.go's pg_stat_archiver case, and it means the pair's
// samples carry the same unlabelled shape the names in naming.go:412-434 were
// written for.
//
// Flagged for A3, in the same register as database.go's and settings.go's flags:
// the label vocabulary. R9.3's enumerated set is seven labels and none of them is
// `backend_type`, `object`, `context` or `name`; A3's ruling on activity.go:842-849
// settled that the per-family half "enumerates; only the base set closes" and is
// "not a ceiling on the label vocabulary", and its shape rule is that "a per-family
// label must be a dimension the family's own source view groups by, and the family
// name must name that dimension". The four labels this file adds are all dimensions
// their views group by, which satisfies the first clause; the second is satisfied
// the way R9.5b satisfies it (its per-query family carries `fingerprint` and
// `queryid` and its name names neither), and the alternative that satisfies it
// literally — minting one family per (backend_type, object, context) tuple, which is
// activity.go's rejected "one family per state value, seven families for one grouped
// count" in a larger costume — costs the cardinality R13.4 budgets for. If A3 rules
// the literal reading, the fix is in naming.go's name column, not here: the
// classifier already separates the three dimension columns from the metrics, so
// changing the family shape does not require re-deriving which columns are labels.
type IOCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewIOCollector builds the collector on top of an opener (R2.7's transaction
// source) and a config (R2.7's timeout, R2.8's application name, R13.11's clock),
// with every unset field resolved by SQLConfig.withDefaults.
func NewIOCollector(opener SQLOpener, cfg SQLConfig) *IOCollector {
	c := &IOCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorIO,
		Interval: IOInterval,
		// SourceSQL: all four views are S2 (PostgreSQL's own catalogs). §3.3's row
		// adds the §3.2 caveat that S3 carries no pg_stat_io, which is a note about
		// what the CNPG exporter does not expose, not a second source here — the
		// sentence exists to stop a reader concluding pg_stat_io is unavailable
		// because CNPG's metrics don't have it.
		Source: SourceSQL,
		// CostStandard: collector.go's wording names "the database, wal, settings
		// and replication collectors" as the single-digit-query tier, and this one
		// is the same shape — one read-only transaction, four projections, no
		// cross-join and no pg_stat_statements-scale row set. pg_stat_io returns one
		// row per (backend_type, object, context) tuple, which is tens of rows, not
		// thousands.
		Cost: CostStandard,
		// AppliesToViews over the two independent views: pg_stat_io exists on 16-18
		// and pg_stat_slru on 13-18, so the intersection is 16-18 — which is the
		// row's "(16+)" derived from the matrix instead of typed in by hand, the same
		// derivation wal.go:137-140 makes for its "(14+)".
		//
		// The succession pair is deliberately not an input. Intersecting all four
		// views would also give 17-18 (checkpointer exists only from 17), which
		// contradicts the row's own "(16+)" by refusing PG16, where the row says
		// pg_stat_io exists and Phase 2's acceptance requires collection. Union would
		// give 13-18, which claims the collector runs on PG13 where two of the row's
		// four named sources have no data at all. The pair's contribution on PG16 is
		// pg_stat_bgwriter — which is what R2.4's move row says the checkpointer's
		// PG16 counters *are* — so the two-view intersection is both the row's
		// answer and the one that leaves no view with a hole.
		AppliesTo: AppliesToViews(pgversion.ViewStatIO, pgversion.ViewStatSLRU),
		// Timeout unset: R2.1's 10 s default, for the reason database.go gives — the
		// round timeout is a hang guard and R2.7's statement_timeout is the tuning
		// knob.
		ViewGroups: []string{ioViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *IOCollector) Descriptor() Descriptor {
	return c.desc
}

// Collect runs one round: one read-only transaction (R2.7) holding one query per
// view that exists on the target's major (R2.4), with every row parsed into samples.
//
// The error paths each return the round-trips issued so far, because the Collector
// contract requires it and R13.1's budget is measured from Result.RoundTrips.
func (c *IOCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	projections, err := c.projections(tgt)
	if err != nil {
		return Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		return Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorIO, err)
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
				fmt.Errorf("collector %s: query %s: %w", CollectorIO, pv.view, qerr)
		}
		samples = append(samples, ioSamples(pv, c.cfg.Now(), rows)...)
	}

	if cerr := tx.Commit(); cerr != nil {
		// The data was read; the transaction just failed to close. D20's S9, applied
		// here as it is in wal.go:185-200: sql.go's Tx doc says "Rollback on every
		// error path", a failed Commit is an error path, and a driver that failed the
		// COMMIT may still hold the transaction open on a connection the pooler hands
		// to the next round. The samples come back with the error and the scheduler
		// keeps serving the previous round; the rollback's own error is the commit's,
		// which is why it is discarded.
		_ = tx.Rollback()
		return Result{Samples: samples, RoundTrips: trips},
			fmt.Errorf("collector %s: commit: %w", CollectorIO, cerr)
	}
	return Result{Samples: samples, RoundTrips: trips}, nil
}

// projections builds one query per view for the target's major, reusing the
// viewProjection pairing and the refusal rules database.go established: no major
// means no column set to build SQL from, and a view the matrix does not have on a
// major this collector declared itself applicable to means the declaration and the
// matrix disagree. Both are checked before Begin, so a round with nothing to run
// costs no connection.
//
// The one thing wal.go's version does not have is a skip. wal.go's two views are
// both applicability inputs, so a missing projection is always a contradiction and
// always an error. Here, two of the four views are the succession pair, and R2.4
// models that pair as one source with two spellings — pg_stat_checkpointer has no
// PG16 set because on PG16 the checkpointer's counters are pg_stat_bgwriter's — so
// exactly one of the two exists on any major below 17 and the other must be skipped
// rather than failed. The two independent views keep wal.go's rule: they are what
// AppliesToViews was computed from, so a missing projection among them is the
// disagreement the descriptor check exists to catch, and it is an error.
//
// None of the four views needs a QuerySpec: every column either becomes a sample
// directly, names a row, or is the view's epoch — unlike pg_stat_replication, whose
// interval columns need date_part Extras to become numbers at all.
func (c *IOCollector) projections(tgt Target) ([]viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return nil, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorIO, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		names := make([]string, 0, len(ioViews))
		for _, v := range ioViews {
			names = append(names, string(v))
		}
		return nil, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (the matrix has no column set for [%s] on this major)",
			CollectorIO, tgt.PGMajor, strings.Join(names, " "))
	}

	out := make([]viewProjection, 0, len(ioViews))
	for _, v := range ioViews {
		p, ok := Project(v, tgt.PGMajor, QuerySpec{})
		if !ok {
			if v == pgversion.ViewStatBgwriter || v == pgversion.ViewStatCheckpointer {
				// The succession pair: one source, two spellings, one of them present
				// per major. Skipping is the pair's rule, not a fallback — see the
				// type doc for why the pair is not an applicability input.
				continue
			}
			return nil, fmt.Errorf("collector %s: declared applicable on PostgreSQL %d, but %s has no column set there",
				CollectorIO, tgt.PGMajor, v)
		}
		if err := ValidateProjection(p); err != nil {
			return nil, err
		}
		out = append(out, viewProjection{view: v, Projection: p})
	}
	if len(out) == 0 {
		// Unreachable while the applicability check above passes, and asserted anyway
		// because a collector that opens a transaction to run zero queries reports
		// RoundTrips 1 for a round that read nothing, which R13.1 counts as a query.
		return nil, fmt.Errorf("collector %s: no view projects on PostgreSQL %d", CollectorIO, tgt.PGMajor)
	}
	return out, nil
}

// ioKindOf classifies one column of one of the io collector's four views.
//
// It is this file's classifier rather than database.go's kindOf because kindOf knows
// only the three database views and returns kindUnknown for everything else, and
// because the answers differ in two places: pg_stat_io has three identity columns
// where pg_stat_database has one, and pg_stat_checkpointer's `write_time` is a
// different metric from pg_stat_io's `write_time` (checkpoint seconds versus I/O
// seconds), which a shared classifier would have to name per view anyway.
//
// Every column of all four views is listed by name, including the metrics, and
// anything unlisted returns kindUnknown. That is wal.go's enforcement pattern
// (wal.go:250-256) rather than database.go's permissive `default: return kindMetric`,
// and it earns its keep on exactly one column: pg_stat_io's `op_bytes`, which exists
// on 16-17 and not on 18, and whose three PG18 successors are named individually.
// A permissive default would classify a future matrix addition as a number and
// TestIOKindOfCoversEveryMatrixColumn would pass while the collector exported a
// metric R9.4 never named.
func ioKindOf(view pgversion.View, column string) columnKind {
	if column == SourceNowColumn {
		return kindSourceNow
	}

	switch view {
	case pgversion.ViewStatIO:
		switch column {
		case "backend_type", "object", "context":
			// The row's identity: what kind of backend, what kind of object, which
			// operation context. R9.3's per-family labels, not values — the same
			// treatment pg_stat_database.datname gets (naming.go:339).
			return kindName
		case "reads", "read_time", "writes", "write_time", "writebacks", "writeback_time",
			"extends", "extend_time", "op_bytes", "read_bytes", "write_bytes",
			"extend_bytes", "hits", "evictions", "reuses", "fsyncs", "fsync_time":
			// Fourteen of these exist on 16-17 and sixteen on 18: op_bytes is replaced
			// by read_bytes, write_bytes and extend_bytes at PG18, which is R2.4's
			// third enumerated case (succession.go's 1→3 split row) and why both the
			// old column and the three new ones are listed rather than one of them
			// being derived from the others.
			return kindMetric
		case "stats_reset":
			return kindEpoch
		}
	case pgversion.ViewStatSLRU:
		switch column {
		case "name":
			// The segment class ("commit", "multi", "xact", "wal"): R9.3's label for
			// this view's families, not a value.
			return kindName
		case "blks_zeroed", "blks_hit", "blks_read", "blks_written", "blks_exists",
			"flushes", "truncates":
			return kindMetric
		case "stats_reset":
			return kindEpoch
		}
	case pgversion.ViewStatBgwriter:
		switch column {
		case "checkpoints_timed", "checkpoints_req", "checkpoint_write_time",
			"checkpoint_sync_time", "buffers_checkpoint", "buffers_backend",
			"buffers_backend_fsync", "buffers_clean", "maxwritten_clean",
			"buffers_alloc":
			// The first seven exist on 13-16 only: five move to pg_stat_checkpointer at
			// PG17 and two are deleted (R2.4's fourth enumerated case), which is why
			// the PG17-18 projection yields three families from this view and the
			// moved ones appear under the checkpointer's own key instead.
			return kindMetric
		case "stats_reset":
			return kindEpoch
		}
	case pgversion.ViewStatCheckpointer:
		switch column {
		case "num_timed", "num_requested", "num_done", "restartpoints_timed",
			"restartpoints_req", "restartpoints_done", "write_time", "sync_time",
			"buffers_written", "slru_written":
			// num_done and slru_written are PG18-only (9 columns on 17, 11 on 18).
			// slru_written is the column that overlaps pg_stat_slru's subject matter
			// without overlapping its identity: it is a per-checkpointer count of
			// SLRU pages written, where pg_stat_slru's flushes is a per-segment-class
			// one, and R9.4 names them separately (naming.go:426-434 versus the
			// pg_stat_slru rows) because they are two measurements of two things.
			return kindMetric
		case "stats_reset":
			return kindEpoch
		}
	}
	return kindUnknown
}

// ioSamples turns one view's rows into samples, choosing the shape the view has:
// labelled per row when the projection carries identity columns, summed when it does
// not.
//
// The columns come from the projection rather than from each row's map, for the two
// reasons database.go gives for the same choice: a map's iteration order is random,
// so a collector whose sample order changes every round cannot be tested against a
// golden list; and a row missing a column must not silently shrink the set of
// families a view reports.
func ioSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	var (
		metrics  []string
		names    []string
		epochCol string
	)
	for _, col := range pv.Columns {
		switch ioKindOf(pv.view, col) {
		case kindMetric:
			metrics = append(metrics, col)
		case kindName:
			names = append(names, col)
		case kindEpoch:
			epochCol = col
		}
	}
	if len(names) == 0 {
		return ioSummed(pv, metrics, epochCol, now, rows)
	}
	return ioLabelled(pv, metrics, names, epochCol, now, rows)
}

// ioLabelled is the per-row shape: one sample per metric column per row, labelled
// with the row's own identity columns.
//
// It is database.go's samplesFromRows generalised from one name column to N, because
// pg_stat_io's identity is a three-column tuple where pg_stat_database's is one
// column, and generalising is what keeps the two views in one file. The rules it
// inherits from that function are the ones that make a labelled view honest:
//
//   - a row whose name column is empty is skipped, not labelled with "" (D20's S8:
//     R9.3's label set has no slot for an empty value, and a series with an empty
//     dimension is a series the contract does not have). For pg_stat_io and
//     pg_stat_slru every identity column is NOT NULL in PostgreSQL's own definition,
//     so this is a guard against a driver returning an empty string for a value it
//     did not scan rather than a live case;
//   - a value that is neither a number nor a boolean produces no sample rather than a
//     0, because storing 0 measures nothing and a 0 for io_reads reads as a backend
//     that performed no reads;
//   - the identity half (namespace, cluster, pod, instance_role) is the scheduler's to
//     fill from the Target — clock.go's Sample doc says so — so this builds only the
//     series-specific labels.
func ioLabelled(pv viewProjection, metrics, names []string, epochCol string, now time.Time, rows []Row) []Sample {
	out := make([]Sample, 0, len(rows)*len(metrics))
	for _, row := range rows {
		labels := make(map[string]string, len(names))
		for _, n := range names {
			raw, ok := row.Get(n)
			if !ok || strings.TrimSpace(raw) == "" {
				// One missing identity column means the row cannot be labelled at all:
				// a partial label set would put the row's metrics in a series that
				// differs from its siblings by an absent label, which is the invalid
				// exposition class rather than a smaller version of the right one.
				labels = nil
				break
			}
			labels[n] = raw
		}
		if labels == nil {
			continue
		}

		// R3.3's counter epoch is per row here (each row is its own series), so it is
		// read once per row and shared by that row's metrics — unlike the summed
		// shape, where a family's epoch is the latest among the rows that were added.
		var epoch time.Time
		if epochCol != "" {
			if raw, ok := row.Get(epochCol); ok {
				if t, ok := parseTimestamp(raw); ok {
					epoch = t
				}
			}
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
				StatsReset: epoch,
			})
		}
	}
	return out
}

// ioSummed is the aggregate shape: one sample per metric column, summed over the
// rows. It is wal.go's walSamples applied to the succession pair, and it exists in
// this file rather than by calling wal.go's version for the same reason wal.go has
// its own builder instead of calling database.go's: the builder's classifier is the
// file's (walSamples calls walKindOf), and a shared builder would need the view as a
// parameter anyway.
//
// For a single-row view the sum is the value, which is what makes this correct for
// pg_stat_bgwriter and pg_stat_checkpointer: both are one row cluster-wide, so the
// sum is the counter itself and the shape costs nothing. A column with no parseable
// value in any row produces no sample at all rather than a 0 — wal.go states the
// reason a sum needs most: a 0 for checkpoints_timed on a cluster whose row failed
// to parse reads as a cluster that has taken no timed checkpoints.
func ioSummed(pv viewProjection, metrics []string, epochCol string, now time.Time, rows []Row) []Sample {
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

			// R3.3's counter epoch for a summed family is the latest stats_reset among
			// the rows that contributed to it: with per-row resets the sum has one
			// discontinuity per component and the newest is the one a rate() over the
			// sum breaks on. A NULL stats_reset parses to the zero time and never wins
			// a max, so a row without an epoch contributes its value and not an epoch.
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
			// and this is a cluster aggregate — for these two views it is the only
			// shape available, since their matrix column sets carry no identity column
			// to label with.
			Labels:     map[string]string{},
			Value:      sum,
			SourceTime: sourceTimeOf(rows[0]),
			IngestTime: now,
			StatsReset: epoch,
		})
	}
	return out
}
