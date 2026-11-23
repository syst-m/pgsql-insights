package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// CollectorReplication is §3.3's name for this collector, and the `collector`
// label value on both R2.2 families for its rounds.
//
// §3.3's row is the whole scope statement:
//
//	| `replication` | 60 s | **S4 `replicationInfo[]`/`replicationSlotsInfo[]`** (preferred, no SQL, no creds); S2 `pg_stat_replication`, `pg_replication_slots`; S3 `cnpg_pg_replication_*` | Empty on single-instance clusters (R5.5) |
//
// which names two S2 views, and this file implements the S2 half. The row's
// fourth column — "Empty on single-instance clusters (R5.5)" — is a behaviour
// requirement, not a note, and it is implemented literally: a cluster with no
// standbys returns no rows from pg_stat_replication, and no rows produces no
// samples with a nil error (see the Result doc on that in collector.go). The
// `cookbook/cookbook-pg` fixture in §8 is exactly that case, and R16.6's
// ground-truth table lists it under R5.5/R9.3.
const CollectorReplication = "replication"

// ReplicationInterval is §3.3's interval for the replication collector (60 s),
// stated as a constant so R2.3's --interval.replication override has a declared
// default to override rather than a literal buried in a struct literal.
const ReplicationInterval = 60 * time.Second

// replicationViewGroup is the collector's single R2.7 view group.
//
// One group for the two views, for the reason database.go's equivalent spells out
// (one transaction so the two views agree about one moment, and so R13.1's
// round-trip count is the query count rather than the query count plus a second
// BEGIN/COMMIT pair).
const replicationViewGroup = "replication"

// replicationViews are §3.3's two S2 sources for this collector, in the order
// their queries are issued.
//
// The matrix has a third replication view — pg_stat_replication_slots, the
// logical-replication statistics view, 10 columns on 14-18 — and §3.3's row does
// not name it, so it is not in this set. R2.4's rule is that the row's source
// list is the collector's view list, and adding a view the row does not name is a
// scope change that belongs in requirements.md first.
var replicationViews = []pgversion.View{
	pgversion.ViewStatReplication,
	pgversion.ViewReplicationSlots,
}

// replicationSpecs is the per-view SQL the matrix cannot express: the three
// streaming lag columns, which are intervals and need a function to become
// numbers.
//
// pg_stat_replication's write_lag/flush_lag/replay_lag are `interval` columns.
// parseValue has no interval grammar (a value of "00:00:01.5" is not a number,
// and R9.1 will not accept a sample whose value is a string), so the number has to
// be produced in SQL the same way pg_database's size is: as an Extra. The three
// aliases are what the metric names are built from (MetricName applied to an alias
// is exactly database.go's pg_database_size-from-`size` precedent), and
// date_part is schema-qualified per R2.5 so validateProjection's expression check
// (D20's S10) passes on it.
//
// pg_replication_slots needs no spec: every family it produces is a count over
// its rows, and the columns the counts read (active, wal_status) are matrix
// columns.
var replicationSpecs = map[pgversion.View]QuerySpec{
	pgversion.ViewStatReplication: {
		Extras: []Extra{
			{Expr: "pg_catalog.date_part('epoch', write_lag)", Alias: "write_lag_seconds"},
			{Expr: "pg_catalog.date_part('epoch', flush_lag)", Alias: "flush_lag_seconds"},
			{Expr: "pg_catalog.date_part('epoch', replay_lag)", Alias: "replay_lag_seconds"},
		},
	},
}

// ReplicationCollector is §3.3's replication collector: the streaming walsender
// connections and the replication slots, which together are R5.5's replication
// section and R5.1's replica-lag card.
//
// The one judgement in the file is which *families* a row set produces, and it is
// the same judgement settings.go makes for the same two reasons, so this is the
// second of the three SQL collectors that cannot use database.go's
// one-family-per-numeric-column shape.
//
// R9.3's label contract is closed (namespace, cluster, pod, instance_role,
// +pooler, +datname, +usename) and has no slot for either view's row identity.
// pg_stat_replication's rows are per *walsender connection*: the identity is
// application_name (which in CNPG *is* the standby's pod name — but R9.3's `pod`
// label means the pod being scraped, so labelling a primary's series with a
// standby's pod name is a category error, and the two are different pods),
// client_addr, and pid. The one contract label that does exist, `usename`, is the
// same string on every row of a CNPG cluster (`<cluster>-replication`), so
// per-column families labelled by it would have N rows competing for one series
// and the exported value would be whichever row the driver returned last.
// pg_replication_slots is keyed by slot_name, which is not in the contract at
// all.
//
// So the shape is aggregates, following fileSettingSamples's precedent ("Counts
// are the shape that survives"): how many walsenders, the worst lag on each of the
// three planes, how many slots, how many in use, how many lost. §5's divergence
// table assigns the *per-standby* series (replica_byte_lag, replication/state,
// available_for_failover) to Phase 7, sourced S4+S2 — which is the spec agreeing:
// the per-row detail is R9.1's API half and Phase 7's, and the aggregate numbers
// are what Phase 2's /metrics needs.
//
// Flagged for A3, in the same register as database.go's and settings.go's
// flags: the aggregate shape means pg_replication_slots's per-slot numbers
// (safe_wal_size, and on 17+ the failover/synced booleans) are not exported,
// because a max across slots of different types and majors is not a number
// anyone would query. R9.7's export set is "the union of R5.2's families and the
// numeric columns R2.4's matrix returns", and this file's answer to R5.2's
// replication card is the five count/lag families above rather than one series per
// numeric column; if A3 rules that R9.7 means per-column series here too, the fix
// is a `slotKindOf` classifier beside the two builders, not a rewrite.
type ReplicationCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewReplicationCollector builds the collector on top of an opener (R2.7's
// transaction source) and a config (R2.7's timeout, R2.8's application name,
// R13.11's clock), with every unset field resolved by SQLConfig.withDefaults.
func NewReplicationCollector(opener SQLOpener, cfg SQLConfig) *ReplicationCollector {
	c := &ReplicationCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorReplication,
		Interval: ReplicationInterval,
		// SourceSQL, and the reason is collector.go's Descriptor.Source rule:
		// "One collector, one source: the `replication` row in §3.3 lists S4 as
		// preferred and S2/S3 as fallbacks, and that preference order is the
		// replication collector's own logic, not a licence to declare two
		// sources." So the field declares one of the three, and it declares S2
		// because that is the half Phase 2 owns: the S4 path (the instance-manager
		// HTTPS GET on :8000/pg/status) is R5.5's source and lands with the
		// discovery work in Phases 5-7 — there is no replicationInfo reader in the
		// tree to declare — and §3.2's spine is S1 → S2 → S3. The preference order
		// itself (S4 first, S2 only when S4 is unreachable) stays the collector's
		// own logic, exactly as that doc says, and this file does not pretend to
		// implement it.
		Source: SourceSQL,
		// CostStandard: collector.go's own wording names this collector —
		// "CostStandard costs one read-only transaction per view group (R2.7) and
		// single-digit queries: the database, wal, settings and replication
		// collectors".
		Cost:      CostStandard,
		AppliesTo: AppliesToViews(replicationViews...),
		// Timeout unset: R2.1's 10 s default, for the reason database.go gives —
		// the round timeout is a hang guard and R2.7's statement_timeout is the
		// tuning knob.
		ViewGroups: []string{replicationViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *ReplicationCollector) Descriptor() Descriptor {
	return c.desc
}

// Collect runs one round: one read-only transaction (R2.7) holding one query per
// view (R2.4), with every row parsed into samples.
//
// The error paths each return the round-trips issued so far, because the Collector
// contract requires it and R13.1's budget is measured from Result.RoundTrips.
func (c *ReplicationCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	projections, err := c.projections(tgt)
	if err != nil {
		return Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		return Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorReplication, err)
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
				fmt.Errorf("collector %s: query %s: %w", CollectorReplication, pv.view, qerr)
		}
		samples = append(samples, replicationSamples(pv, c.cfg.Now(), rows)...)
	}

	if cerr := tx.Commit(); cerr != nil {
		// The data was read; the transaction just failed to close. That is still a
		// failed round — a connection that cannot commit is a connection whose next
		// query is suspect — so the samples come back with the error and the
		// scheduler keeps serving the previous round.
		//
		// D20's S9, filed against database.go and applied here by S16: sql.go's Tx
		// doc says "Rollback on every error path", and a failed Commit is an error
		// path. A driver that failed the COMMIT may still hold the transaction open
		// on a connection the pooler hands to the next round, and the rollback that
		// closes it is best-effort — its error is the commit's, which is why the
		// result is discarded here exactly as it is in the query path above.
		_ = tx.Rollback()
		return Result{Samples: samples, RoundTrips: trips},
			fmt.Errorf("collector %s: commit: %w", CollectorReplication, cerr)
	}
	return Result{Samples: samples, RoundTrips: trips}, nil
}

// projections builds one query per view for the target's major, reusing the
// viewProjection pairing and the refusal rules database.go established: no major
// means no column set to build SQL from, and a view the matrix does not have on
// this major means the declared applicability and the matrix disagree. Both are
// checked before Begin, so a round with nothing to run costs no connection.
func (c *ReplicationCollector) projections(tgt Target) ([]viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return nil, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorReplication, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		names := make([]string, 0, len(replicationViews))
		for _, v := range replicationViews {
			names = append(names, string(v))
		}
		return nil, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (one of [%s] has no column set there)",
			CollectorReplication, tgt.PGMajor, strings.Join(names, " "))
	}

	out := make([]viewProjection, 0, len(replicationViews))
	for _, v := range replicationViews {
		p, ok := Project(v, tgt.PGMajor, replicationSpecs[v])
		if !ok {
			return nil, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d", CollectorReplication, v, tgt.PGMajor)
		}
		if err := validateProjection(p); err != nil {
			return nil, err
		}
		out = append(out, viewProjection{view: v, Projection: p})
	}
	return out, nil
}

// replicationSamples turns one view's rows into samples.
//
// It dispatches on the view rather than classifying columns, for the settings.go
// reason: the families come from row data (counts, and maxima over declared
// extras), so a new column in the matrix — a PG19 pg_replication_slots addition —
// is by definition not a new family and needs no classification. database.go
// needs a columnKind classifier because its families are named after columns and
// it has to decide per column whether a column holds a value.
func replicationSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	switch pv.view {
	case pgversion.ViewStatReplication:
		return streamingSamples(pv, now, rows)
	case pgversion.ViewReplicationSlots:
		return slotSamples(pv, now, rows)
	}
	return nil
}

// streamingSamples turns pg_stat_replication's rows into one count plus the worst
// lag on each of the three replication planes.
//
// The count is the R5.5 replica count (one row per walsender, so the row count is
// the answer to "how many standbys are connected"), and it counts every row
// including a walsender still in state `startup` — a connecting standby is a
// standby, and `state` is R9.1's text half, not something the metric plane
// filters on.
//
// The three lag families exist only if at least one row has a parseable value for
// them. A walsender that has not streamed yet has NULL for all three LSNs and
// therefore for all three date_part aliases, and emitting 0 there would say "this
// standby is fully caught up" — the one answer that is definitely wrong. No series
// is the honest answer, and it is the same rule gucSamples applies to a string
// GUC.
//
// The family names come from pv.Extras rather than from a list in this function,
// so the SQL and the metrics cannot disagree about which lag planes exist: adding
// a fourth Extra adds a fourth family.
func streamingSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	if len(rows) == 0 {
		return nil
	}
	source := sourceTimeOf(rows[0])

	// Capacity 1+len(pv.Extras): the count plus one maximum per lag alias, and the
	// only way to under-fill it is a plane with no parseable value.
	out := make([]Sample, 0, 1+len(pv.Extras))
	out = append(out, Sample{
		Metric: MetricName(pv.view, "count"),
		// No labels: the identity half is the scheduler's to add from the Target,
		// and R9.3 has no slot for the row's own key (see the type doc).
		Labels:     map[string]string{},
		Value:      float64(len(rows)),
		SourceTime: source,
		IngestTime: now,
	})

	for _, e := range pv.Extras {
		var (
			worst float64
			have  bool
		)
		for _, row := range rows {
			v, ok := parseValue(mustGet(row, e.Alias))
			if !ok {
				continue
			}
			if !have || v > worst {
				worst, have = v, true
			}
		}
		if !have {
			continue
		}
		// The _max suffix names the aggregation, the way fileSettingSamples's
		// applied_count and error_count do: a family whose value is a max has to
		// say so, or a reader cannot tell it from a per-row value.
		out = append(out, Sample{
			Metric:     MetricName(pv.view, e.Alias+"_max"),
			Labels:     map[string]string{},
			Value:      worst,
			SourceTime: source,
			IngestTime: now,
		})
	}
	return out
}

// slotSamples turns pg_replication_slots's rows into three counts.
//
// The three are the ones R5.5's slot surface reduces to: how many slots exist, how
// many are being consumed (`active`, the difference between a slot in use and a
// slot holding WAL for nothing), and how many are `lost` — a lost slot means the
// WAL it needed has been recycled, so the standby behind it needs a rebuild, which
// is the alertable fact and the one a dashboard cannot get from the count alone.
//
// The per-slot detail is not exported and cannot be: slot_name is the row identity
// and R9.3 has no slot for it, so a per-slot series is unreachable; and the
// numeric columns (datoid, active_pid, xmin, catalog_xmin, safe_wal_size) are
// identifiers and LSN-adjacent byte counts that mean nothing summed or maxed
// across slots of two different types. The failover/synced booleans are not
// exported either: they exist only on 17 and 18, so a family that appears on two
// of the six supported majors is a query that breaks by major, and CNPG does not
// use them for its async topology.
func slotSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	if len(rows) == 0 {
		return nil
	}
	source := sourceTimeOf(rows[0])

	var active, lost int
	for _, row := range rows {
		if v, ok := parseValue(mustGet(row, "active")); ok && v != 0 {
			active++
		}
		// wal_status is text ('reserved', 'extended', 'unreserved', 'lost'), and
		// 'lost' is the only value that changes what an operator does, so it is
		// counted rather than turned into a per-status family. On a standby the
		// column is NULL for every slot, which parses as neither and counts as
		// not lost — correct, since a slot on a standby has no status to report.
		if mustGet(row, "wal_status") == "lost" {
			lost++
		}
	}

	return []Sample{
		{Metric: MetricName(pv.view, "count"), Labels: map[string]string{},
			Value: float64(len(rows)), SourceTime: source, IngestTime: now},
		{Metric: MetricName(pv.view, "active_count"), Labels: map[string]string{},
			Value: float64(active), SourceTime: source, IngestTime: now},
		{Metric: MetricName(pv.view, "lost_count"), Labels: map[string]string{},
			Value: float64(lost), SourceTime: source, IngestTime: now},
	}
}
