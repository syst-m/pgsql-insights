package store

// The store's DDL, one const per table.
//
// One const per table rather than one big string, for a reason that is not
// cosmetic: a Go raw string cannot contain a backtick, and the house style
// quotes requirement names with backticks. As one string, every `retention…`
// and `queryid` in the SQL comments either had to lose its backticks or close
// the literal early (which is the first version of this file's syntax error).
// Per-table consts put the prose in a Go comment, where backticks are legal, and
// leave the SQL string holding only SQL.
//
// Why not numbered migration files: the store has no released version to
// migrate *from* (R13.10 forbids `replace` directives and 0.1 is unreleased),
// every statement is CREATE TABLE IF NOT EXISTS, and Open on an existing PVC
// must be idempotent because R11.15's file outlives the process. When 0.1 ships
// and a column has to change, that is the moment this becomes a directory and
// the first entry is "the 0.1 schema".
//
// The tables and the requirement each answers:
//
//	query            R3.4:308  the fingerprint as the canonical key
//	query_source     R3.3:307  the per-instance cumulative counters R3.3 deltas
//	query_rollup     R3.2:306  the load columns; R8.11:386's per-minute series;
//	                           R3.14:318's week-over-week window
//	plan_sample      R3.10:314 plan samples (retention.plans)
//	plan_change      R3.11:315 plan-change events
//	dimension_rollup R3.7:311  the per-dimension rollups (+ R3.12:316's tags)
//	sink_current     sink.go   the SQLite half of collector.Sink
//
// Two tables §3.4:220 names are deliberately absent, and the reason is the same
// each time — a table with no writer is a schema that lies about what the
// product stores:
//
//   - The latency histogram (R3.9:313, R9.13:405's eight named buckets): its
//     writer is Phase 4's activity collector, not this phase's statement store,
//     and R3.9's histogram is explicitly *sampled* from pg_stat_activity rather
//     than derived from pg_stat_statements. Declaring it now would let a reader
//     conclude the store has distributions while every dto.LatencyView.Buckets
//     is still empty.
//   - Collector errors and discovery history: A1's data, from the scheduler and
//     the discoverer. Neither owner has asked for a table, and creating one they
//     have not agreed to write to is how a schema accretes dead tables.
//
// Four columns the proposed schema did not have, each one forced by a DTO this
// package has to fill and each documented at its own table:
//
//	query.display_text                   dto.QueryView.Query
//	query_source.min/max/stddev_exec_ms  dto.LatencyView (mean is derived, these three are not)
//	query_source.usename, in the PK too  dto.QuerySeries.Usename, R9.4's "(cluster, db, user)"
//	plan_sample.sql                      dto.PlanSampleView.SQL
const schema = queryTable + querySourceTable + rollupTable + planSampleTable +
	planChangeTable + dimensionTable + sinkTable

// queryTable is R3.4:308 — one row per fingerprint, the store's canonical key.
//
// R3.4's full text: "Key the query store on PostgreSQL's own queryid, and
// additionally compute our own normalized fingerprint (literal-stripped,
// whitespace-collapsed, keyword-cased template). Reasons, all real: (a) PG18
// changes query-id jumbling, so ids change for unchanged queries across a
// 17→18 upgrade; (b) we need displayable text; (c) we need cross-database
// grouping. Store the template plus the first-seen raw text truncated per R3.6.
// The **fingerprint is the canonical key**; queryid is informational (R9.5b)."
//
// The sentence opens by keying on queryid and closes by making the fingerprint
// canonical; its own reason (c) resolves that. A queryid is per
// (database, jumble), so grouping one statement across databases is only
// possible if the fingerprint is the key and the queryids are a set hanging off
// it — which is why queryid is a column on query_source (one row per database)
// and not part of this table's identity.
const queryTable = `
CREATE TABLE IF NOT EXISTS query (
	fingerprint     TEXT    NOT NULL PRIMARY KEY,
	template        TEXT    NOT NULL,
	-- display_text is dto.QueryView.Query verbatim: "It is
	-- query.Normalized.DisplayText -- the truncated form". A column rather
	-- than a re-derivation, because DisplayText is the *template* cut to
	-- R3.6's cap, which is not the same string as first_seen_text (the *raw*
	-- text cut to the cap) and is not re-derivable from it: the normalizer's
	-- own doc notes that hashing the truncated form yields a different
	-- fingerprint from hashing the whole template, and the same asymmetry
	-- applies to the display text. Re-normalizing the template on every read
	-- would make the API's answer depend on the normalizer being idempotent.
	display_text    TEXT    NOT NULL,
	first_seen_text TEXT    NOT NULL,
	truncated       INTEGER NOT NULL DEFAULT 0,
	text_bytes      INTEGER NOT NULL DEFAULT 0,
	first_seen      INTEGER NOT NULL,
	last_seen       INTEGER NOT NULL
);

-- idx_query_last_seen is A3's S31. Purge's age query -- "every fingerprint whose
-- newest sighting is older than the retention window" -- reads the one column
-- the query table exists to answer by, and that table is the one §4's byte math
-- sizes as the largest in the store, so without this the retention pass is a
-- full scan of the biggest table, run on every tick. The file already creates two
-- indexes (query_source :175, query_rollup :238) for their own range reads and
-- had none for this one: the index was written twice for the child tables and
-- never for the parent whose WHERE clause names its column.
CREATE INDEX IF NOT EXISTS idx_query_last_seen ON query (last_seen);
`

// querySourceTable is one row per (fingerprint, instance, database): every
// place a template was seen, and the cumulative counters R3.3:307 deltas
// against.
//
// R3.3: "every rate is current − previous snapshot, with the snapshot's
// pg_stat_statements_info.stats_reset (PG14+) checked — a reset must reset the
// baseline rather than produce a negative or wrapped rate. On PG13, which has no
// stats_reset in the info view, fall back to detecting counter decreases per
// row." The row *is* the previous snapshot: the collector computes the delta and
// the store persists the new value, which becomes the baseline the next round
// diffs against.
//
// The PK is R9.3's instance identity plus datname and usename -- the same
// (namespace, cluster, pod) triple A1's sinkKey uses, for the same reason he
// states there ("no two distinct triples can produce the same key"), extended
// with the other half of R9.4's row identity: "per (cluster, db, user)".
// WITHOUT ROWID
// because the PK is the whole row identity, so the rowid would be a second index
// over data that is already unique, and this is the hottest table in the store.
const querySourceTable = `
CREATE TABLE IF NOT EXISTS query_source (
	fingerprint      TEXT    NOT NULL REFERENCES query(fingerprint) ON DELETE CASCADE,
	namespace        TEXT    NOT NULL,
	cluster          TEXT    NOT NULL,
	pod              TEXT    NOT NULL,
	instance_role    TEXT    NOT NULL DEFAULT '',
	datname          TEXT    NOT NULL,
	-- R9.5b:397 -- "The **fingerprint is the canonical key**; queryid is
	-- informational". TEXT, not INTEGER: a bigint queryid exceeds 2^53 more
	-- often than not, and dto.QueryView.QueryID's ruling is that a JSON number
	-- JavaScript rounds is an id the API never sent. Empty string means PG
	-- reported none, which is legal (R9.5b makes it informational).
	queryid          TEXT    NOT NULL DEFAULT '',
	pg_major         INTEGER NOT NULL DEFAULT 0,
	-- usename completes R9.4's row identity, which naming.go:109 quotes as
	-- "per (cluster, db, user)" and dto.QuerySeries.Usename renders. pg_stat_statements
	-- has userid (an OID) on every major and never the name, so the collector
	-- resolves it through pg_authid and stores the result; empty means "the
	-- collector could not resolve it", which the DTO omits rather than rendering
	-- as a blank name. It is in the PK, not just a column, because two users
	-- running one statement in one database have two sets of counters: one row
	-- for both makes the second one's write look like a counter decrease (R3.3's
	-- reset condition) and erases one of R9.5's series.
	usename          TEXT    NOT NULL DEFAULT '',
	-- The cumulative counters, as pg_stat_statements reports them (R3.2:306's
	-- set). Cumulative since stats_reset, which is why the per-interval delta
	-- lives in query_rollup and not here.
	calls            INTEGER NOT NULL DEFAULT 0,
	total_exec_ms    REAL    NOT NULL DEFAULT 0,
	rows_returned    REAL    NOT NULL DEFAULT 0,
	shared_blks_hit  REAL    NOT NULL DEFAULT 0,
	shared_blks_read REAL    NOT NULL DEFAULT 0,
	-- R3.2:306's IO derivation is per-major: blk_read_time/blk_write_time on
	-- PG15-16, shared_blk_read_time/shared_blk_write_time on PG17+. The
	-- collector picks the pair the target's major has and stores the sum, so
	-- the read paths agree on one number instead of each re-deriving it.
	io_wait_ms       REAL    NOT NULL DEFAULT 0,
	-- R3.2's Load by CPU = total_exec_time minus block IO, derived at capture
	-- (it needs the major to pick the IO pair) and stored for the same reason.
	cpu_ms           REAL    NOT NULL DEFAULT 0,
	-- R3.9:313's four stats. pg_stat_statements has min_time, max_time and
	-- stddev as well as mean_time, and dto.LatencyView names all four. Mean is
	-- derived (total/calls) rather than stored; these three have no derivation
	-- and are otherwise unrepresentable -- a LatencyView with MinMs 0 asserts
	-- the query once ran in zero time. stddev_exec_ms uses -1 for "no source"
	-- for the same reason query_rollup does: PG reports NULL stddev when the
	-- view has no sample, and 0 is a different answer from "unknown".
	min_exec_ms      REAL    NOT NULL DEFAULT 0,
	max_exec_ms      REAL    NOT NULL DEFAULT 0,
	stddev_exec_ms   REAL    NOT NULL DEFAULT -1,
	-- R3.3:307's counter epoch. 0 means "no epoch available" -- the PG13
	-- fallback R3.3 names, where a per-row decrease is all the reset detection
	-- there is.
	stats_reset      INTEGER NOT NULL DEFAULT 0,
	captured_at      INTEGER NOT NULL,
	ingested_at      INTEGER NOT NULL,
	PRIMARY KEY (fingerprint, namespace, cluster, pod, datname, usename)
) WITHOUT ROWID;

-- No index on instance_role: the PK prefix (fingerprint) serves every
-- fingerprint lookup and the index below serves the per-source ones, so a third
-- index over role alone would be a third copy of data the PK already orders.
CREATE INDEX IF NOT EXISTS idx_query_source_source
	ON query_source (namespace, cluster, pod, datname);
`

// rollupTable is the rollup, and the one table that answers three requirements
// at once: R3.2:306's top-N table, R8.11:386's "per-minute series", and
// R3.14:318's "answer week-over-week 'is this query slower than last week'".
//
// One row per (fingerprint, source, bucket), where "source" is R9.4's identity:
// (namespace, cluster, pod, datname, usename). The values are the interval's
// *delta*, not the cumulative counters -- R3.3's current − previous snapshot.
// That is what makes a window of minutes answer "last week": the cumulative
// counters reset (R3.3's stats_reset) and a rollup row does not.
//
// The coordinator's ruling on the bucket scheme was "(b), spec-literal": minutes
// for the whole retention.statementSnapshots window, no hour/day tiering --
// "(c) breaks R3.14 for exactly the queries the feature exists to catch ... and
// (a) costs A2 a behavioural change that we shouldn't spend on a storage
// optimisation while the spec-literal option still fits the PVC at the default."
// So 'minute' is the only bucket_kind anything writes today; the column exists
// because the ruling named minute/hour/day as the shape the tiered alternative
// would have had, and adding it later is an ALTER that a populated table makes
// expensive.
//
// lock_wait_ms is -1 for "no source", not 0. R3.2:306: "Load by lock wait has
// **no per-query source** (pg_stat_statements has no lock-time column -- §5
// divergence table) and renders n/a (no per-query source, §5)". A 0 there
// asserts "this query never waited on a lock", which is a different claim from
// "there is no per-query source for it". -1 is the interim encoding the
// coordinator approved ("it distinguishes a real 0 from not-available ... and it
// survives whichever way A3 rules"), filed as A3's S25 because
// api.QuerySeriesPoint.LockTimeSeconds and promql_catalogue.go:129 do carry a
// per-fingerprint lock_time that R3.2 withholds.
const rollupTable = `
CREATE TABLE IF NOT EXISTS query_rollup (
	fingerprint      TEXT    NOT NULL,
	namespace        TEXT    NOT NULL,
	cluster          TEXT    NOT NULL,
	pod              TEXT    NOT NULL,
	datname          TEXT    NOT NULL,
	-- usename, and so the PK, is R9.4's third identity member — see
	-- querySourceTable for why it cannot be a plain column. R9.5's per-query
	-- family is keyed on (fingerprint, cluster, db, user), and this table is
	-- what answers it, so a rollup without it could only ever serve one user's
	-- series per database.
	usename          TEXT    NOT NULL DEFAULT '',
	bucket_kind      TEXT    NOT NULL DEFAULT 'minute',
	bucket           INTEGER NOT NULL,
	calls            INTEGER NOT NULL,
	total_exec_ms    REAL    NOT NULL,
	cpu_ms           REAL    NOT NULL,
	io_wait_ms       REAL    NOT NULL,
	rows_returned    REAL    NOT NULL,
	shared_blks_hit  REAL    NOT NULL,
	shared_blks_read REAL    NOT NULL,
	lock_wait_ms     REAL    NOT NULL DEFAULT -1,
	PRIMARY KEY (fingerprint, namespace, cluster, pod, datname, usename, bucket_kind, bucket)
) WITHOUT ROWID;

-- The top-N read is "every fingerprint's load in this window, for this
-- cluster", which is a scan of (cluster, bucket_kind, bucket). This index is
-- what keeps R3.2's table from being a full-table scan once the 7 d window holds
-- the ~2 M minute rows the §4 byte math predicts.
CREATE INDEX IF NOT EXISTS idx_rollup_cluster
	ON query_rollup (cluster, bucket_kind, bucket, total_exec_ms DESC);
`

// planSampleTable is R3.10:314's captured plans, kept for retention.plans (7 d,
// §3.4:222).
//
// R3.10 constrains capture in three ways and all three are columns: the method
// (dto.MethodExplain / MethodPrepareExecute / MethodAnalyze), the availability
// (dto.PlanAvailable vs dto.PlanUnavailableTablePrivileges), and the SQL the
// plan was captured for.
//
// fingerprint carries REFERENCES query(fingerprint) ON DELETE CASCADE (A3's S30).
// Without it a plan sample outlives the query it belongs to: Purge deletes aged
// `query` rows, and the orphaned plan rows are then rows for a fingerprint the
// store no longer has — reachable only through plan_sample, which is the
// "schema that lies about what the store keeps" failure this file's header names.
// The constraint itself is not new to the schema (query_source:114 has it); it was
// written once and not copied, which is what makes the omission a bug rather than
// a design choice — a FK on one child table and bare columns on the other two says
// the cascade was meant to be universal.
const planSampleTable = `
CREATE TABLE IF NOT EXISTS plan_sample (
	fingerprint  TEXT    NOT NULL REFERENCES query(fingerprint) ON DELETE CASCADE,
	captured_at  INTEGER NOT NULL,
	method       TEXT    NOT NULL,
	availability TEXT    NOT NULL,
	-- The statement text EXPLAIN was run on (dto.PlanSampleView.SQL). It is not
	-- redundant with query.template: R3.10's capture explains one *actual*
	-- statement, and the fingerprint's stored text is the normalized template —
	-- for a parameterized statement the two differ by the literals the
	-- EXPLAIN'd form had.
	sql          TEXT    NOT NULL DEFAULT '',
	-- NULL when the plan is unavailable. The read side is json.RawMessage, so
	-- the store keeps exactly the bytes the collector captured and the DTO
	-- re-emits them without reformatting -- a re-marshal would reorder keys and
	-- change the plan hash below.
	plan_json    TEXT,
	-- R3.11:315's plan hash, on every sample including unavailable ones.
	plan_hash    TEXT    NOT NULL DEFAULT '',
	message      TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (fingerprint, captured_at)
);
`

// planChangeTable is R3.11:315: "keep plan hashes per fingerprint and surface a
// plan-change event when the hash changes for a stable fingerprint -- the
// regression detection Cloud SQL gets from auto_explain and we get from bounded
// sampling."
//
// A table rather than a boolean on query, because R3.11's value is the series:
// one event says the plan moved, two says it moved twice, and the regression
// question is answered by lining the change up against the latency curve above
// it (dto.QueryDetailView.PlanChanges).
// fingerprint carries the same REFERENCES … ON DELETE CASCADE as plan_sample
// (A3's S30) — see that table for why one occurrence in the schema was the bug.
const planChangeTable = `
CREATE TABLE IF NOT EXISTS plan_change (
	fingerprint        TEXT    NOT NULL REFERENCES query(fingerprint) ON DELETE CASCADE,
	detected_at        INTEGER NOT NULL,
	previous_plan_hash TEXT    NOT NULL,
	current_plan_hash  TEXT    NOT NULL,
	PRIMARY KEY (fingerprint, detected_at)
);
`

// dimensionTable is R3.7:311's per-dimension rollups, plus R3.12:316's tags.
//
// R3.7: "Per-dimension rollups: which database, which user, which application
// name, which client address accounts for the most load ... Each rollup answers
// 'which X accounts for the most load' with the same load columns as R3.2 --
// including Load by lock wait, which is available at this level from pg_locks +
// pg_stat_activity even though it is not available per query."
//
// So lock_wait_ms here has no -1 sentinel: R3.7 says the column *is* available
// at this level, and this is the only place in the store that can fill it.
//
// kind='tag' rows answer R8.2's /insights/tags route and their writer is
// R3.12's comment-grammar parser -- not this phase's statement store, which is
// why the write path (PutDimensionRollups) takes the dimensions as data rather
// than growing a tag-specific method the parser's owner has not asked for.
// R3.13:317's --record-client-address gates kind='client_address' rows.
//
// namespace and cluster are in the PK, and the read path is what proved they had
// to be. The first version of this table keyed on (kind, dim_hash, bucket_kind,
// bucket) alone, which makes one tag's minute one row *for the whole deployment*:
// two clusters running the same `/* pgsi:route=/orders */` statement collapse
// into one row, and R8.2's /insights/tags route — which takes ?namespace= and
// ?cluster= as filters per R8.4's uniform parameter set, and whose R9.6 metric
// family carries both labels — then has no way to answer a cluster-scoped
// request. The same argument R9.3 makes for (namespace, cluster, pod) being the
// identity of anything the API scopes to a cluster applies to a rollup.
const dimensionTable = `
CREATE TABLE IF NOT EXISTS dimension_rollup (
	kind        TEXT NOT NULL,
	namespace   TEXT NOT NULL DEFAULT '',
	cluster     TEXT NOT NULL DEFAULT '',
	dim_hash    TEXT NOT NULL,
	application TEXT NOT NULL DEFAULT '',
	datname     TEXT NOT NULL DEFAULT '',
	usename     TEXT NOT NULL DEFAULT '',
	action      TEXT NOT NULL DEFAULT '',
	route       TEXT NOT NULL DEFAULT '',
	controller  TEXT NOT NULL DEFAULT '',
	framework   TEXT NOT NULL DEFAULT '',
	dbdriver    TEXT NOT NULL DEFAULT '',
	bucket_kind TEXT NOT NULL DEFAULT 'minute',
	bucket      INTEGER NOT NULL,
	calls            INTEGER NOT NULL DEFAULT 0,
	total_exec_ms    REAL NOT NULL DEFAULT 0,
	cpu_ms           REAL NOT NULL DEFAULT 0,
	io_wait_ms       REAL NOT NULL DEFAULT 0,
	rows_returned    REAL NOT NULL DEFAULT 0,
	shared_blks_hit  REAL NOT NULL DEFAULT 0,
	shared_blks_read REAL NOT NULL DEFAULT 0,
	lock_wait_ms     REAL NOT NULL DEFAULT 0,
	PRIMARY KEY (kind, namespace, cluster, dim_hash, bucket_kind, bucket)
) WITHOUT ROWID;
`

// sinkTable is the SQLite half of collector.Sink. A1's sink.go:26-29 assigns it
// in as many words: "This commit ships the memory half; the SQLite half is
// Phase 3's own commit and the scheduler's call site does not move when it
// lands."
//
// It is a cache, not a history, and the schema says so: the PK is
// (namespace, cluster, pod, collector) with no time component, because
// collector.Sink's contract is "Publish replaces the stored sample set for
// (tgt, collector) with samples" -- one set per key, the newest one. That is
// what keeps this table from contradicting §3.4:221 ("Raw metric samples --
// those live in Prometheus; we serve them from our registry, not from SQLite"):
// a replaced cache of what the collectors last produced is not a TSDB, and
// R2.2's "a failed collector leaves its last-good data served" needs exactly one
// stored set per key and nothing older.
const sinkTable = `
CREATE TABLE IF NOT EXISTS sink_current (
	namespace   TEXT NOT NULL,
	cluster     TEXT NOT NULL,
	pod         TEXT NOT NULL,
	collector   TEXT NOT NULL,
	payload     BLOB NOT NULL,
	ingested_at INTEGER NOT NULL,
	PRIMARY KEY (namespace, cluster, pod, collector)
) WITHOUT ROWID;
`

// migrate creates the schema. Every statement is IF NOT EXISTS, so this is safe
// to run against an existing PVC — Open runs it unconditionally.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	return nil
}
