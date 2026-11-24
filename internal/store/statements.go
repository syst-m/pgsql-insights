package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/query"
)

// QuerySource is the (instance, database) a snapshot came from.
//
// It is an alias for collector.QuerySource. The three types this file used to
// define — QuerySource, StatementRow and Snapshot — moved to the collector, which
// is the package that has to name them and cannot import store (store/sink.go
// imports collector, so the dependency only runs one way). The collector's
// statements.go carries their docs; internal/api/series.go:96-109 is the same
// pattern and states the guarantee: "an alias creates no second type, so every
// existing `api.QuerySeriesPoint` spelling ... keeps compiling and keeps naming
// one definition."
//
// Nothing in this file changed a call site. PutSnapshot's argument is now the
// value StatementsCollector.CollectStatements returns, which is the seam the
// cmd/pgsi round loop needed and did not have.
type QuerySource = collector.QuerySource

// StatementRow is one pg_stat_statements row as the collector read it.
//
// It is an alias for collector.StatementRow; see QuerySource above for why the
// definitions live there. The field set's doc — the view's projection minus the
// columns no DTO renders, plus the two the store derives — moved with the type
// rather than being copied here.
type StatementRow = collector.StatementRow

// Snapshot is one collector round's rows for one source.
//
// It is an alias for collector.Snapshot; see QuerySource above. R3.3's
// counter-reset epoch (StatsReset) is documented on the type, and the store's
// three delta rules read it from there.
type Snapshot = collector.Snapshot

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

	// The two plan inserts list the identity tuple A3's R10.7 extension put in
	// their PKs (schema.go's planSampleTable gives the argument), because a
	// column list that stops at `fingerprint` is a column list that cannot
	// address the row it is writing: the PK has six members before the timestamp
	// and an INSERT that supplies one of them names a row no SELECT can ask for.
	insertPlanSampleSQL = `
		INSERT OR REPLACE INTO plan_sample
			(fingerprint, namespace, cluster, pod, datname, usename, captured_at,
			 method, availability, sql, plan_json, plan_hash, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	insertPlanChangeSQL = `
		INSERT OR REPLACE INTO plan_change
			(fingerprint, namespace, cluster, pod, datname, usename, detected_at,
			 previous_plan_hash, current_plan_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// previousPlanHashSQL is R3.11's "previous" for a new sample: the newest hash
	// the *same row* had before the sample being written.
	//
	// The six identity columns are A3's S43, and they are the R10.7 widening's last
	// unlanded site. The query keyed on `fingerprint` alone — "whatever database it
	// came from — R3.11's plan-change event is per fingerprint, not per source" —
	// which described the pre-extension table, whose key was (fingerprint,
	// captured_at). Now that plan_change's PK is the 6-tuple plus detected_at, the
	// event *is* per source, and a per-fingerprint read-back compares one pair's
	// new hash against a different pair's old one: a db2 sample reported
	// `bbb3<-aaa1`, where aaa1 is db1's plan and the db2 pair never had it. The
	// consequence is that every new (pod, datname, usename) appearing for an
	// existing fingerprint logs a plan change on its first round, which is not a
	// plan change but a new row — and R3.11's regression signal is the series of
	// real changes, so a spurious one per new source is noise in the one number the
	// feature produces. detectPlanChange already agrees with the widened key (its
	// planChange{identity: p.identity} is the sample's own identity); the SQL was
	// the half that had not caught up.
	//
	// The `captured_at < ?` is A3's S26 and it is not a refinement, it is the
	// requirement. Detection runs after the insert, inside the same tx, so a
	// read-back without the bound returns the row the insert just wrote: prev
	// equals the hash in hand, the equality check at detectPlanChange trips, and
	// every plan change is missed forever — not just the first, which would be
	// correct. A3 proved it against the DDL extracted from this file (a second
	// snapshot with hash h2 returns prev = h2 and zero change rows where R3.11
	// wants one; with `AND captured_at < 200` it returns h1 and the event fires).
	// The comment that shipped with the first version described the query *with*
	// the bound; the SQL did not have it, which is the failure mode of writing the
	// prose and the query in separate passes.
	previousPlanHashSQL = `
		SELECT plan_hash FROM plan_sample
		WHERE fingerprint = ? AND namespace = ? AND cluster = ?
		  AND pod = ? AND datname = ? AND usename = ?
		  AND plan_hash <> '' AND captured_at < ?
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

		// The row's identity, built once per deduped row and then embedded in
		// every table this row writes to: query_source here, query_rollup via
		// rollupRow below, and plan_sample via planRow. The dedupe key is two
		// members of the same tuple, so it reads from the tuple rather than
		// re-picking them out of the row.
		id := identity{
			fingerprint: r.n.Fingerprint,
			namespace:   snap.Source.Namespace,
			cluster:     snap.Source.Cluster,
			pod:         snap.Source.Pod,
			datname:     snap.Source.Datname,
			usename:     r.r.Usename,
		}
		key := sourceKey{id.fingerprint, id.usename}
		cur := sourceRow{
			identity:     id,
			instanceRole: snap.Source.Role,
			queryid:      r.r.QueryID,
			pgMajor:      snap.Source.PGMajor,
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
				identity:     id,
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
		if _, err := tx.Exec(insertPlanSampleSQL, p.args()...); err != nil {
			return fmt.Errorf("store: insert plan_sample %s/%s: %w", p.fingerprint[:12], p.usename, err)
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
		if _, err := tx.Exec(insertPlanChangeSQL, c.args()...); err != nil {
			return fmt.Errorf("store: insert plan_change %s/%s: %w", p.fingerprint[:12], p.usename, err)
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
// two prepared statements, say — produce two rows with one fingerprint.
//
// The merge sums the counters (R3.4's keying, the reason the fingerprint is the
// canonical key); it does not keep the larger row. The
// first version kept the higher-total one — the "highest-load source" rule
// QueryDetail renders — which imports a display rule into a storage path. With
// A=100 and B=50 in one snapshot the minute stored 100 ms where 150 happened, and
// since query_source is R3.3's baseline for the fingerprint's whole life, the
// error is permanent rather than one round's. The second symptom is the worse one:
// a later round whose slice order flips the survivor from A to B stores 60 where
// 110 was stored, and R3.3's counter-reset rule fires on a minute in which no
// reset occurred — a false reset is precisely what R3.3 exists to prevent.
//
// So the numbers sum and the text comes from the highest-load row, which is how
// both rules survive: R3.3 gets the sum it needs to diff the next round against,
// and queryRow.sql keeps the text of the row that actually scored highest (A3's
// stated wrinkle — the fingerprint's stored text must not migrate to the smaller
// statement just because it arrived second).
func (s *Store) normalizeAndDedupe(snap Snapshot) ([]normalizedRow, error) {
	out := make([]normalizedRow, 0, len(snap.Rows))
	at := map[sourceKey]int{}
	for _, r := range snap.Rows {
		n := query.Normalize(r.SQL, s.cfg.Normalizer)
		if !validFingerprint(n.Fingerprint) {
			return nil, fmt.Errorf("store: fingerprint for %s: %w", describe(r), ErrMalformedFingerprint)
		}
		k := sourceKey{n.Fingerprint, r.Usename}
		i, seen := at[k]
		if !seen {
			at[k] = len(out)
			out = append(out, normalizedRow{n: n, r: r})
			continue
		}
		out[i].r = mergeRows(out[i].r, r)
	}
	return out, nil
}

// mergeRows folds a second StatementRow that shares a sourceKey into the first.
//
// Every counter sums. Every non-additive field — the three text fields, Min, Max,
// Stddev, Plan — comes from whichever row has the higher TotalExecTimeMs: a min
// is not a sum, and taking the whole non-additive set from one row keeps the
// latency stats consistent with the SQL they were measured on instead of pairing
// one row's min with another row's stddev.
//
// The Normalized struct is not merged here: both rows normalized to the same
// fingerprint, which is a hash of the template, so the template (and therefore the
// display text and truncated flag) is the same string either way.
func mergeRows(prev, r StatementRow) StatementRow {
	best := prev
	if r.TotalExecTimeMs > prev.TotalExecTimeMs {
		best = r
	}
	return StatementRow{
		QueryID:          best.QueryID,
		SQL:              best.SQL,
		Usename:          best.Usename,
		Calls:            prev.Calls + r.Calls,
		TotalExecTimeMs:  prev.TotalExecTimeMs + r.TotalExecTimeMs,
		RowsReturned:     prev.RowsReturned + r.RowsReturned,
		SharedBlocksHit:  prev.SharedBlocksHit + r.SharedBlocksHit,
		SharedBlocksRead: prev.SharedBlocksRead + r.SharedBlocksRead,
		BlockReadMs:      prev.BlockReadMs + r.BlockReadMs,
		BlockWriteMs:     prev.BlockWriteMs + r.BlockWriteMs,
		MinExecTimeMs:    best.MinExecTimeMs,
		MaxExecTimeMs:    best.MaxExecTimeMs,
		StddevExecTimeMs: best.StddevExecTimeMs,
		Plan:             best.Plan,
	}
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

// identity is the (fingerprint, namespace, cluster, pod, datname, usename) tuple
// that R10.7's ruling makes one expression across the four tables that carry it:
// query_source itself, query_rollup (whose PK adds bucket_kind and bucket),
// plan_sample (adds captured_at) and plan_change (adds detected_at).
//
// Embedding it in all three Go row types is the type-level spelling of a sentence
// the ruling writes in prose. Three field lists that happen to match are three
// lists that drift — schema.go's own evidence is the FK A3's S30 found "written
// once and not copied" — and a planRow missing one member is exactly the bug the
// ruling exists to fix, so the fix is to make the tuple a thing that can be
// embedded rather than a thing that can be forgotten.
//
// The field order is the PK order in all four tables, which is what lets every
// args() method emit the tuple first and the table's own columns after, in the
// order its INSERT lists them.
type identity struct {
	fingerprint, namespace, cluster, pod, datname, usename string
}

// sourceRow is one row of `query_source`; rollupRow embeds it because the rollup
// row's identity is the source's identity plus a bucket.
type sourceRow struct {
	identity
	instanceRole, queryid                                       string
	pgMajor                                                     int
	calls                                                       int64
	totalExecMs, rowsReturned, shbHit, shbRead, ioWaitMs, cpuMs float64
	minMs, maxMs, stddevMs                                      float64
	statsReset, capturedAt, ingestedAt                          int64
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
//
// It embeds identity for the reason the schema's PK now spells the tuple out: the
// row's own key is the source's key plus a timestamp, and a struct that names only
// the fingerprint cannot address the row it inserts. The coordinator's note on the
// ruling expected this to be a column-list change with the struct untouched; it is
// not — planRow had no namespace/cluster/pod/datname/usename to supply, which is
// the same omission the DDL had, one layer up.
type planRow struct {
	identity
	capturedAt                  int64
	method, availability, sql   string
	planJSON, planHash, message string
}

// args is the row's 13 bind values, in insertPlanSampleSQL's column order.
//
// Every insert in this file goes through an args() method for one reason: the
// column list and the value list are two lists that have to agree, and a method is
// where the agreement lives. The plan inserts used to inline their values at the
// Exec call, which is how a widened column list ends up with eight placeholders
// and thirteen columns.
func (p planRow) args() []any {
	return []any{
		p.fingerprint, p.namespace, p.cluster, p.pod, p.datname, p.usename,
		p.capturedAt,
		p.method, p.availability, p.sql, p.planJSON, p.planHash, p.message,
	}
}

// planChange is one row of `plan_change` (R3.11).
type planChange struct {
	identity
	detectedAt                        int64
	previousPlanHash, currentPlanHash string
}

// args is the row's 9 bind values, in insertPlanChangeSQL's column order.
func (c planChange) args() []any {
	return []any{
		c.fingerprint, c.namespace, c.cluster, c.pod, c.datname, c.usename,
		c.detectedAt,
		c.previousPlanHash, c.currentPlanHash,
	}
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
// previous hash for the sample's own row — the row, not the fingerprint, which is
// A3's S43 and is what the widened PK means — and whether it differs from this one. ok is
// false when there is no previous hash (the first sample, so nothing changed) or
// when the sample carries no hash (an unavailable plan — R3.11's hash exists
// "whenever there is a plan", and dto.PlanSampleView's omitempty says the
// unavailable ones have none to compare).
func detectPlanChange(tx *sql.Tx, p planRow) (planChange, bool) {
	if p.planHash == "" {
		return planChange{}, false
	}
	var prev string
	// The last argument is the bound A3's S26 asked for. Without it the read-back
	// returns the row the insert loop above just wrote, prev equals p.planHash, the
	// check below returns false, and no plan change is ever recorded — a compile
	// green, test-green (until A3's S29 tests exist) way to miss the requirement. The
	// six before it are A3's S43 identity: the row whose previous hash we want is the
	// row this sample belongs to, not the newest one the fingerprint happens to have
	// somewhere else.
	err := tx.QueryRow(previousPlanHashSQL,
		p.fingerprint, p.namespace, p.cluster, p.pod, p.datname, p.usename,
		p.capturedAt).Scan(&prev)
	if err != nil || prev == "" || prev == p.planHash {
		return planChange{}, false
	}
	// The event's identity is the sample's: with the widened PK the change row is
	// per (fingerprint, source), so appuser's plan change and postgres's in the
	// same round are two events with the same two hashes and different identities.
	return planChange{
		identity:         p.identity,
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
