package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/query"
)

// QuerySource is the (instance, database) a snapshot came from.
//
// It embeds A1's collector.Target — namespace, cluster, pod, role, address,
// source, pg major — because that is R9.3's instance identity and the store must
// key on the same triple the scheduler and the sink key on (A1's sink.go:
// "no two distinct triples can produce the same key"). Datname is the addition
// the target cannot carry: pg_stat_statements is per-database, so one pod yields
// one snapshot per database it hosts, and R9.4's row identity names the database.
type QuerySource struct {
	collector.Target
	Datname string
}

// StatementRow is one pg_stat_statements row as the collector read it.
//
// The field set is the view's projection (collector/pgversion's
// StatStatementsPG15to16 and friends) minus the columns no DTO renders
// (local_blks_*, temp_blks_*, wal_*, jit_*, the plan-time columns) plus the two
// the store derives. What the view does *not* have, the store does not claim:
// there is no lock_time column on any major, which is R3.2's "no per-query
// source" and why query_rollup stores -1 rather than a number (see rollupTable).
type StatementRow struct {
	// QueryID is PostgreSQL's queryid, as a string. The view has a bigint and
	// dto.QueryView.QueryID is a string for the reason stated there — a JSON
	// number past 2^53 is an id JavaScript rounds, so "the UI would display an
	// id the API never sent". Storing the collector's decimal spelling is what
	// keeps the store's answer and the API's answer the same string.
	QueryID string

	// SQL is the raw statement text (the view's `query` column). The store
	// normalizes it here, and this is the package's only normalization site:
	// R3.5's rules and R3.6's length cap applied in one place is what keeps the
	// fingerprint the API serves identical to the one the collector stored.
	SQL string

	// Usename is the role the statement ran as. The view has `userid` (an OID)
	// on every major and never the name, so the collector resolves it through
	// pg_authid and hands the store the name; empty means unresolved, which the
	// DTOs omit rather than rendering blank.
	Usename string

	Calls            int64
	TotalExecTimeMs  float64
	RowsReturned     float64
	SharedBlocksHit  float64
	SharedBlocksRead float64

	// BlockReadMs and BlockWriteMs are the per-major IO pair the collector
	// picked: blk_read_time/blk_write_time on PG 15–16,
	// shared_blk_read_time/shared_blk_write_time on PG 17+ (R3.2's derivation,
	// quoted at dto.LoadColumns). The store sums them into io_wait_ms and
	// subtracts that sum from TotalExecTimeMs for cpu_ms, so R3.2's two derived
	// columns are derived once instead of in every read path — which is the only
	// way a PG16 row and a PG17 row end up with comparable numbers.
	BlockReadMs  float64
	BlockWriteMs float64

	// Min, Max and Stddev complete dto.LatencyView's four named stats; Mean is
	// derived from Total/Calls and is not stored. StddevExecTimeMs is a pointer
	// because the view reports NULL stddev when the row has no sample, and the
	// store must tell "no sample" (stored as -1) from "0" — the same
	// not-available-vs-zero distinction as rollupTable's lock_wait_ms sentinel.
	MinExecTimeMs    float64
	MaxExecTimeMs    float64
	StddevExecTimeMs *float64

	// Plan is R3.10's captured plan for this statement, nil when this round
	// captured none. R3.10's sampling is bounded, so most rounds capture none
	// for most queries and nil is the common case, not the error case.
	Plan *dto.PlanSampleView
}

// Snapshot is one collector round's rows for one source.
type Snapshot struct {
	Source     QuerySource
	CapturedAt time.Time
	IngestedAt time.Time

	// StatsReset is pg_stat_statements_info.stats_reset (PG14+; the view has no
	// row on PG13). R3.3: "every rate is current − previous snapshot, with the
	// snapshot's pg_stat_statements_info.stats_reset (PG14+) checked — a reset
	// must reset the baseline rather than produce a negative or wrapped rate.
	// On PG13, which has no stats_reset in the info view, fall back to detecting
	// counter decreases per row." Zero means "no epoch available", which selects
	// that fallback rather than disabling reset detection.
	StatsReset time.Time

	Rows []StatementRow
}

// The SQL PutSnapshot runs. They are consts rather than inline strings so the
// column lists are visible next to each other and a column added to the schema
// is missing from exactly one of these.
const (
	upsertQuerySQL = `
		INSERT INTO query (fingerprint, template, display_text, first_seen_text,
		                   truncated, text_bytes, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			last_seen = MAX(query.last_seen, excluded.last_seen)`

	upsertSourceSQL = `
		INSERT INTO query_source (fingerprint, namespace, cluster, pod, instance_role,
		                          datname, queryid, pg_major, usename,
		                          calls, total_exec_ms, rows_returned,
		                          shared_blks_hit, shared_blks_read, io_wait_ms, cpu_ms,
		                          min_exec_ms, max_exec_ms, stddev_exec_ms,
		                          stats_reset, captured_at, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint, namespace, cluster, pod, datname, usename) DO UPDATE SET
			instance_role    = excluded.instance_role,
			queryid          = excluded.queryid,
			pg_major         = excluded.pg_major,
			calls            = excluded.calls,
			total_exec_ms    = excluded.total_exec_ms,
			rows_returned    = excluded.rows_returned,
			shared_blks_hit  = excluded.shared_blks_hit,
			shared_blks_read = excluded.shared_blks_read,
			io_wait_ms       = excluded.io_wait_ms,
			cpu_ms           = excluded.cpu_ms,
			min_exec_ms      = excluded.min_exec_ms,
			max_exec_ms      = excluded.max_exec_ms,
			stddev_exec_ms   = excluded.stddev_exec_ms,
			stats_reset      = excluded.stats_reset,
			captured_at      = excluded.captured_at,
			ingested_at      = excluded.ingested_at`

	insertRollupSQL = `
		INSERT OR REPLACE INTO query_rollup
			(fingerprint, namespace, cluster, pod, datname, usename, bucket_kind, bucket,
			 calls, total_exec_ms, cpu_ms, io_wait_ms, rows_returned,
			 shared_blks_hit, shared_blks_read, lock_wait_ms)
		VALUES (?, ?, ?, ?, ?, ?, 'minute', ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	insertPlanSampleSQL = `
		INSERT OR REPLACE INTO plan_sample
			(fingerprint, captured_at, method, availability, sql, plan_json, plan_hash, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	insertPlanChangeSQL = `
		INSERT OR REPLACE INTO plan_change
			(fingerprint, detected_at, previous_plan_hash, current_plan_hash)
		VALUES (?, ?, ?, ?)`

	// previousPlanHashSQL is R3.11's "previous" for a new sample: the newest
	// stored hash for that fingerprint, whatever database it came from —
	// R3.11's plan-change event is per fingerprint, not per source.
	previousPlanHashSQL = `
		SELECT plan_hash FROM plan_sample
		WHERE fingerprint = ? AND plan_hash <> ''
		ORDER BY captured_at DESC LIMIT 1`
)

// PutSnapshot stores one collector round: the new cumulative counters, and the
// per-minute rollup rows R3.3's deltas produce.
//
// The order of operations is the requirement's. R3.3 makes every rate
// "current − previous snapshot", so the previous values have to be read *before*
// anything is written — reading them per row inside the write loop would let two
// rows of the same snapshot see each other's write, which for a fingerprint that
// appears twice (two queryids normalizing to one template) turns the second row's
// delta into a spurious 0. So: normalize and deduplicate the incoming rows, read
// the source's existing rows, compute every delta from that snapshot-in-time view,
// then write.
//
// The rules the deltas follow, all three from R3.3:
//
//   - No previous row for a (fingerprint, usename) → no rollup row. The first
//     observation establishes the baseline only; the cumulative counters include
//     time before we looked, and a rollup row of "12 000 calls" for a query we
//     just discovered would bill the whole history to the minute it was seen.
//   - StatsReset changed → every delta is 0, and the new values become the
//     baseline. R3.3's "a reset must reset the baseline rather than produce a
//     negative or wrapped rate".
//   - Any counter decreased (same epoch) → that counter's delta is 0. On PG13
//     this is the only reset signal there is, and it is also the guard against a
//     collector bug writing a smaller value over a larger one.
func (s *Store) PutSnapshot(ctx context.Context, snap Snapshot) error {
	if err := validateSnapshot(snap); err != nil {
		return err
	}
	rows, err := s.normalizeAndDedupe(snap)
	if err != nil {
		return err
	}
	prev, err := s.existingSources(ctx, snap.Source)
	if err != nil {
		return fmt.Errorf("store: read previous rows for %s/%s/%s/%s: %w",
			snap.Source.Namespace, snap.Source.Cluster, snap.Source.Pod, snap.Source.Datname, err)
	}

	var (
		queries []queryRow
		sources []sourceRow
		rollups []rollupRow
		plans   []planRow
	)
	for _, r := range rows {
		queries = append(queries, queryRow{
			fingerprint:  r.n.Fingerprint,
			template:     r.n.Template,
			displayText:  r.n.DisplayText,
			freshText:    firstSeenText(r.r.SQL, s.cfg.Normalizer.Length),
			truncated:    boolInt(r.n.Truncated),
			textBytes:    len(r.n.Template),
			capturedAt:   unix(snap.CapturedAt),
			existingText: prev.queries[r.n.Fingerprint].existingText,
			existingAt:   prev.queries[r.n.Fingerprint].existingAt,
		})

		key := sourceKey{r.n.Fingerprint, r.r.Usename}
		cur := sourceRow{
			fingerprint:  r.n.Fingerprint,
			namespace:    snap.Source.Namespace,
			cluster:      snap.Source.Cluster,
			pod:          snap.Source.Pod,
			instanceRole: snap.Source.Role,
			datname:      snap.Source.Datname,
			queryid:      r.r.QueryID,
			pgMajor:      snap.Source.PGMajor,
			usename:      r.r.Usename,
			calls:        r.r.Calls,
			totalExecMs:  r.r.TotalExecTimeMs,
			rowsReturned: r.r.RowsReturned,
			shbHit:       r.r.SharedBlocksHit,
			shbRead:      r.r.SharedBlocksRead,
			ioWaitMs:     r.r.BlockReadMs + r.r.BlockWriteMs,
			cpuMs:        r.r.TotalExecTimeMs - (r.r.BlockReadMs + r.r.BlockWriteMs),
			minMs:        r.r.MinExecTimeMs,
			maxMs:        r.r.MaxExecTimeMs,
			stddevMs:     stddevValue(r.r.StddevExecTimeMs),
			statsReset:   unix(snap.StatsReset),
			capturedAt:   unix(snap.CapturedAt),
			ingestedAt:   unix(snap.IngestedAt),
		}
		sources = append(sources, cur)

		// R3.3's three rules, in order: no previous row means no rollup row; a
		// changed epoch zeroes every delta; a decreased counter zeroes that one.
		p, hadPrev := prev.sources[key]
		if hadPrev {
			resetChanged := p.statsReset != cur.statsReset
			rollups = append(rollups, rollupRow{
				sourceRow:   cur,
				bucket:      minuteBucket(snap.CapturedAt),
				callsDelta:  deltaInt(r.r.Calls, p.calls, resetChanged),
				rollupTotal: deltaFloat(r.r.TotalExecTimeMs, p.totalExecMs, resetChanged),
				rollupCPU:   deltaFloat(cur.cpuMs, p.cpuMs, resetChanged),
				rollupIO:    deltaFloat(cur.ioWaitMs, p.ioWaitMs, resetChanged),
				rowsDelta:   deltaFloat(r.r.RowsReturned, p.rowsReturned, resetChanged),
				hitDelta:    deltaFloat(r.r.SharedBlocksHit, p.shbHit, resetChanged),
				readDelta:   deltaFloat(r.r.SharedBlocksRead, p.shbRead, resetChanged),
				// lock_wait_ms keeps its -1 sentinel: R3.2 withholds the column
				// per query, and no delta can be computed for a value that has no
				// source. A3's S25 decides what replaces it.
				lockWaitMs: noLockWait,
			})
		}

		if r.r.Plan != nil {
			plans = append(plans, planRow{
				fingerprint:  r.n.Fingerprint,
				capturedAt:   unix(r.r.Plan.CapturedAt),
				method:       r.r.Plan.Method,
				availability: r.r.Plan.Availability,
				sql:          r.r.Plan.SQL,
				planJSON:     string(r.r.Plan.PlanJSON),
				planHash:     r.r.Plan.PlanHash,
				message:      r.r.Plan.Message,
			})
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, q := range queries {
		if _, err := tx.Exec(upsertQuerySQL, q.args()...); err != nil {
			return fmt.Errorf("store: upsert query %s: %w", q.fingerprint[:12], err)
		}
	}
	for _, src := range sources {
		if _, err := tx.Exec(upsertSourceSQL, src.args()...); err != nil {
			return fmt.Errorf("store: upsert query_source %s/%s: %w", src.fingerprint[:12], src.usename, err)
		}
	}
	for _, r := range rollups {
		if _, err := tx.Exec(insertRollupSQL, r.args()...); err != nil {
			return fmt.Errorf("store: insert query_rollup %s/%d: %w", r.fingerprint[:12], r.bucket, err)
		}
	}
	for _, p := range plans {
		if _, err := tx.Exec(insertPlanSampleSQL,
			p.fingerprint, p.capturedAt, p.method, p.availability,
			p.sql, p.planJSON, p.planHash, p.message); err != nil {
			return fmt.Errorf("store: insert plan_sample %s: %w", p.fingerprint[:12], err)
		}
	}
	// R3.11's plan-change detection runs after the samples are written, so
	// "previous" for the newest row is the row before it — which is why the
	// change row is written from a read-back rather than from the row in hand.
	for _, p := range plans {
		c, ok := detectPlanChange(tx, p)
		if !ok {
			continue
		}
		if _, err := tx.Exec(insertPlanChangeSQL,
			c.fingerprint, c.detectedAt, c.previousPlanHash, c.currentPlanHash); err != nil {
			return fmt.Errorf("store: insert plan_change %s: %w", p.fingerprint[:12], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// validateSnapshot rejects the inputs that would produce a row nothing can read
// back: an unnamed source (every column of R9.3's identity is required to build
// the key) or a snapshot with no capture time (the minute bucket, and therefore
// R8.11's x-axis, comes from it).
func validateSnapshot(snap Snapshot) error {
	switch {
	case snap.Source.Namespace == "":
		return errors.New("store: snapshot source has no namespace")
	case snap.Source.Cluster == "":
		return errors.New("store: snapshot source has no cluster")
	case snap.Source.Pod == "":
		return errors.New("store: snapshot source has no pod")
	case snap.Source.Datname == "":
		return errors.New("store: snapshot source has no datname")
	case snap.CapturedAt.IsZero():
		return errors.New("store: snapshot has no CapturedAt (the minute bucket comes from it)")
	}
	return nil
}

// normalizedRow pairs a row with the normalization of its SQL, so the
// fingerprint is computed once per row and every later stage (the query upsert,
// the source upsert, the rollup insert) reads the same value.
type normalizedRow struct {
	n query.Normalized
	r StatementRow
}

// normalizeAndDedupe normalizes every row in the snapshot (R3.5/R3.6, via the
// configured query.Options) and collapses duplicates.
//
// Duplicates are real: pg_stat_statements' own key is (userid, dbid, queryid),
// so two queryids whose statements normalize to one template — `SELECT $1` from
// two prepared statements, say — produce two rows with one fingerprint. Keeping
// the higher-total one is the same "highest-load source" rule QueryDetail uses,
// and deciding it here is what stops the survivor depending on slice order.
func (s *Store) normalizeAndDedupe(snap Snapshot) ([]normalizedRow, error) {
	out := make([]normalizedRow, 0, len(snap.Rows))
	at := map[sourceKey]int{}
	for _, r := range snap.Rows {
		n := query.Normalize(r.SQL, s.cfg.Normalizer)
		if !validFingerprint(n.Fingerprint) {
			return nil, fmt.Errorf("store: fingerprint for %s: %w", describe(r), ErrUnknownFingerprint)
		}
		k := sourceKey{n.Fingerprint, r.Usename}
		if i, ok := at[k]; ok {
			if r.TotalExecTimeMs > out[i].r.TotalExecTimeMs {
				out[i].r = r
			}
			continue
		}
		at[k] = len(out)
		out = append(out, normalizedRow{n: n, r: r})
	}
	return out, nil
}

// queryRow is one row of the `query` table, the shape the upsert takes.
//
// The upsert's DO UPDATE branch touches last_seen only, so template,
// display_text and first_seen_text keep their stored values on a repeat sighting;
// the fresh values here matter only for the first insert. first_seen is the one
// column the spec makes about the *first* sighting rather than the newest, so the
// stored value wins whenever there is one (existingAt) and the snapshot's capture
// time is the answer only for a brand-new fingerprint.
type queryRow struct {
	fingerprint, template, displayText string
	truncated, textBytes               int

	// freshText is this snapshot's truncation of the raw SQL (R3.4's "the
	// first-seen raw text truncated per R3.6"), used when the fingerprint is new.
	freshText string
	// capturedAt is the snapshot's capture time, used as first_seen when the
	// fingerprint is new and as last_seen always.
	capturedAt int64

	// existingText/existingAt are the stored row's first_seen_text and
	// first_seen, empty/0 for a fingerprint seen for the first time.
	existingText string
	existingAt   int64
}

func (q queryRow) args() []any {
	text, first := q.freshText, q.capturedAt
	if q.existingText != "" {
		text, first = q.existingText, q.existingAt
	}
	return []any{
		q.fingerprint, q.template, q.displayText, text,
		q.truncated, q.textBytes, first, q.capturedAt,
	}
}

// sourceRow is one row of `query_source`; rollupRow embeds it because the rollup
// row's identity is the source's identity plus a bucket.
type sourceRow struct {
	fingerprint, namespace, cluster, pod, instanceRole, datname, queryid string
	pgMajor                                                              int
	usename                                                              string
	calls                                                                int64
	totalExecMs, rowsReturned, shbHit, shbRead, ioWaitMs, cpuMs          float64
	minMs, maxMs, stddevMs                                               float64
	statsReset, capturedAt, ingestedAt                                   int64
}

func (r sourceRow) args() []any {
	return []any{
		r.fingerprint, r.namespace, r.cluster, r.pod, r.instanceRole, r.datname,
		r.queryid, r.pgMajor, r.usename,
		r.calls, r.totalExecMs, r.rowsReturned, r.shbHit, r.shbRead, r.ioWaitMs, r.cpuMs,
		r.minMs, r.maxMs, r.stddevMs,
		r.statsReset, r.capturedAt, r.ingestedAt,
	}
}

// rollupRow is one row of `query_rollup`: a source's identity, a minute, and the
// deltas for that minute.
//
// The delta fields are named rollupTotal/rollupCPU/rollupIO rather than reusing
// the source's totalExecMs/cpuMs/ioWaitMs because embedding sourceRow would make
// `r.totalExecMs` mean two different things in one struct — the cumulative
// counter and the per-minute delta — and the whole point of R3.3's split is that
// they are not the same number.
type rollupRow struct {
	sourceRow
	bucket                           int64
	callsDelta                       int64
	rollupTotal, rollupCPU, rollupIO float64
	rowsDelta, hitDelta, readDelta   float64
	lockWaitMs                       float64
}

func (r rollupRow) args() []any {
	return []any{
		r.fingerprint, r.namespace, r.cluster, r.pod, r.datname, r.usename, r.bucket,
		r.callsDelta, r.rollupTotal, r.rollupCPU, r.rollupIO,
		r.rowsDelta, r.hitDelta, r.readDelta, r.lockWaitMs,
	}
}

// planRow is one row of `plan_sample` (R3.10).
type planRow struct {
	fingerprint                 string
	capturedAt                  int64
	method, availability, sql   string
	planJSON, planHash, message string
}

// planChange is one row of `plan_change` (R3.11).
type planChange struct {
	fingerprint                       string
	detectedAt                        int64
	previousPlanHash, currentPlanHash string
}

// noLockWait is query_rollup.lock_wait_ms's "no source" value, -1. See
// rollupTable for the argument and A3's S25 for the open question.
const noLockWait = -1

// stddevValue maps the view's nullable stddev onto the store's encoding: nil
// (NULL, no sample) is -1, anything else is the value. A plain 0 is a real
// answer — every execution took the same time — and must not be conflated with
// "the view had nothing to say".
func stddevValue(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}

// deltaFloat is R3.3's rule for one counter pair within one epoch: current −
// previous, or 0 when the counters went backwards (a reset the epoch field did
// not cover, or a collector writing a smaller value over a larger one).
func deltaFloat(cur, prev float64, resetChanged bool) float64 {
	if resetChanged || cur < prev {
		return 0
	}
	return cur - prev
}

// deltaInt is deltaFloat for the integral counters (calls).
func deltaInt(cur, prev int64, resetChanged bool) int64 {
	if resetChanged || cur < prev {
		return 0
	}
	return cur - prev
}

// sourceKey is a row's identity within one source: the fingerprint and the user.
// Together with the source's (namespace, cluster, pod, datname) it is the
// query_source PK — R9.4's "(cluster, db, user)" plus the fingerprint R9.5b
// makes canonical.
type sourceKey struct {
	fingerprint string
	usename     string
}

// existingState is everything the store already has for one source, read in one
// pass before PutSnapshot writes anything.
type existingState struct {
	// sources is keyed by (fingerprint, usename) — the part of the PK the
	// snapshot's Source does not already fix.
	sources map[sourceKey]sourceRow
	// queries holds the `query` rows those rows point at, keyed by fingerprint.
	// It is read in the same query as sources (a join) rather than per row so
	// PutSnapshot's "read everything, then write everything" rule costs one
	// round trip instead of one per statement.
	queries map[string]queryRow
}

// existingSources reads every row the store already has for one source, plus the
// `query` text columns those rows point at, in one query.
//
// One query per snapshot rather than one per row is not a micro-optimisation:
// PutSnapshot needs *all* the previous rows before it writes *any* of the new
// ones (see PutSnapshot's ordering argument), and the join pulls the stored
// first_seen_text in the same pass so a repeat sighting keeps its original text
// without a second read of the query table.
func (s *Store) existingSources(ctx context.Context, src QuerySource) (existingState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT qs.fingerprint, qs.namespace, qs.cluster, qs.pod, qs.instance_role,
		       qs.datname, qs.queryid, qs.pg_major, qs.usename,
		       qs.calls, qs.total_exec_ms, qs.rows_returned,
		       qs.shared_blks_hit, qs.shared_blks_read, qs.io_wait_ms, qs.cpu_ms,
		       qs.min_exec_ms, qs.max_exec_ms, qs.stddev_exec_ms,
		       qs.stats_reset, qs.captured_at, qs.ingested_at,
		       q.template, q.display_text, q.first_seen_text, q.first_seen
		FROM query_source qs
		JOIN query q ON q.fingerprint = qs.fingerprint
		WHERE qs.namespace = ? AND qs.cluster = ? AND qs.pod = ? AND qs.datname = ?`,
		src.Namespace, src.Cluster, src.Pod, src.Datname)
	if err != nil {
		return existingState{}, err
	}
	defer rows.Close()

	out := existingState{
		sources: map[sourceKey]sourceRow{},
		queries: map[string]queryRow{},
	}
	for rows.Next() {
		var (
			r sourceRow
			q queryRow
		)
		if err := rows.Scan(
			&r.fingerprint, &r.namespace, &r.cluster, &r.pod, &r.instanceRole,
			&r.datname, &r.queryid, &r.pgMajor, &r.usename,
			&r.calls, &r.totalExecMs, &r.rowsReturned,
			&r.shbHit, &r.shbRead, &r.ioWaitMs, &r.cpuMs,
			&r.minMs, &r.maxMs, &r.stddevMs,
			&r.statsReset, &r.capturedAt, &r.ingestedAt,
			&q.template, &q.displayText, &q.existingText, &q.existingAt,
		); err != nil {
			return existingState{}, err
		}
		out.sources[sourceKey{r.fingerprint, r.usename}] = r
		// The query table has one row per fingerprint and the source rows share
		// it, so N source rows yield the same query row N times; last write wins
		// and every write is the same value.
		q.fingerprint = r.fingerprint
		out.queries[r.fingerprint] = q
	}
	if err := rows.Err(); err != nil {
		return existingState{}, err
	}
	return out, nil
}

// detectPlanChange is R3.11:315's event for one freshly written sample: the
// previous hash for the fingerprint, and whether it differs from this one. ok is
// false when there is no previous hash (the first sample, so nothing changed) or
// when the sample carries no hash (an unavailable plan — R3.11's hash exists
// "whenever there is a plan", and dto.PlanSampleView's omitempty says the
// unavailable ones have none to compare).
func detectPlanChange(tx *sql.Tx, p planRow) (planChange, bool) {
	if p.planHash == "" {
		return planChange{}, false
	}
	var prev string
	err := tx.QueryRow(previousPlanHashSQL, p.fingerprint).Scan(&prev)
	if err != nil || prev == "" || prev == p.planHash {
		return planChange{}, false
	}
	return planChange{
		fingerprint:      p.fingerprint,
		detectedAt:       p.capturedAt,
		previousPlanHash: prev,
		currentPlanHash:  p.planHash,
	}, true
}

// describe names a row in an error message without dumping its SQL.
func describe(r StatementRow) string {
	if r.QueryID != "" {
		return fmt.Sprintf("queryid %s", r.QueryID)
	}
	if len(r.SQL) > 32 {
		return fmt.Sprintf("statement %.32q…", r.SQL)
	}
	return fmt.Sprintf("statement %q", r.SQL)
}
