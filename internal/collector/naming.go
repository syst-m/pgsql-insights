package collector

import (
	"sort"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// MetricName is R9.4's naming function: the exported family name a (view, column)
// pair's values belong to, without the R9.2 prefix.
//
// It replaces the placeholder sql.go carried through Phase 2 steps 1-3. That
// version was `string(view) + "_" + column` and its own doc said so — "step 4's
// R9.4 work (which is the requirement that actually fixes the exported names) is
// a change here rather than a sweep through every collector". The sweep this file
// is the alternative to: nine call sites across four collectors all read their
// names from one function, so the function is the only thing that had to change.
//
// Three rules the function implements, in the order it applies them:
//
//  1. Canonicalise. succession.go's whole thesis is that "(view, column) is the
//     stable identity" — pg_stat_bgwriter.checkpoints_timed on PG13-16 and
//     pg_stat_checkpointer.num_timed on PG17+ are one thing, and R2.4's matrix
//     alone cannot say so because each major's column set is correct on its own.
//     Naming is done off the canonical spelling, which is what stops a 16→17
//     upgrade from forking every checkpoint dashboard in half. Canonical is the
//     exported walk; see its doc.
//
//  2. Look up the documented name. exportNames below is the table. R14.7 admits
//     no metric that exists in code but not in docs, so a name has to come from a
//     document before it can come from this function: every entry cites which —
//     R9.4's own sentence, api.DefaultCatalogue's committed list, or D20's
//     enumeration of R5.2's families.
//
//  3. Fall back. A column with no documented name keeps the old
//     `view + "_" + column` spelling. That is deliberate rather than lazy: it
//     means adopting the catalogue changed the names the catalogue names and
//     nothing else, so a dashboard built against the pre-catalogue spelling
//     survives everywhere the catalogue has not spoken. R9.7's D20 clause asks
//     this function to *enumerate* the union of R5.2's families and R2.4's
//     numeric columns; the table enumerates it for every (view, column) pair it
//     has rows for — exportNames' own doc says which views those are and which
//     are still on the fallback — and the views without rows gain theirs as their
//     collectors land (each one is "R9.7 (partial)" in §3.10's row for step 4,
//     which is the word the row uses).
//
// The prefix stays out. R9.2 makes it configurable (`metrics.prefix`), and
// metrics.go's DefaultPrefix is applied at render time, so a collector that baked
// `pgsi_` into Sample.Metric would make metrics.prefix a lie. clock.go's
// Sample.Metric doc is the normative statement of that ("pg_stat_database_xact_commit",
// not "pgsi_pg_stat_database_xact_commit") and this function honours it.
func MetricName(view pgversion.View, column string) string {
	if e, ok := exportNames[Canonical(view, column)]; ok {
		return e.name
	}
	return string(view) + "_" + column
}

// MetricType is the Prometheus type of a family, which is the other half of what
// the renderer needs and cannot be inferred at render time: `# TYPE` is per
// family, and a counter exported without its `_total` suffix is rejected by
// `promtool check metrics` — which Phase 2's acceptance runs.
type MetricType string

const (
	// TypeCounter is a monotonically increasing cumulative metric. Its name ends
	// in `_total` (the Prometheus convention R9.4's own names follow:
	// pgsi_query_rows_total, pgsi_db_xact_commit_total).
	TypeCounter MetricType = "counter"
	// TypeGauge is a value that goes up and down. No suffix.
	TypeGauge MetricType = "gauge"
	// TypeNotExported marks a column that is part of a view's projection but is
	// not a metric at all: a name column (datname, nspname), an identifier
	// (datid, pid, oid), or a timestamp that is a counter epoch rather than a
	// measurement (stats_reset). R9.1 puts these on the API plane; exporting
	// them as numbers is how an exporter ends up with pgsi_pg_stat_activity_pid.
	TypeNotExported MetricType = ""
)

// exportEntry is one row of the naming table.
type exportEntry struct {
	// name is the family name without the R9.2 prefix.
	name string
	// typ is the family's Prometheus type.
	typ MetricType
	// src names the document the name comes from, because R14.7 makes an
	// undocumented name a violation and this field is the answer to "where is it
	// documented" for every name this function returns.
	src string
}

// exportNames is R9.4's naming table, keyed by the canonical (view, column)
// identity.
//
// It is a table and not a formula because the committed names are not derivable:
// pgsi_db_xact_commit_total shortens pg_stat_database to `db`,
// pgsi_db_tuples_returned_total expands the column tup_returned to `tuples`,
// pgsi_wal_generated_bytes_total renames wal_bytes to `generated`, and
// pgsi_checkpoints_timed_total drops the view name entirely. Any rule that
// produced all four would be a per-view exception list wearing a formula's
// clothes, so the list is written out.
//
// Eleven view keys, not the eight this comment used to claim. Eight of them are
// views the four existing collectors read — all three of database.go's, one of
// settings.go's three (pg_file_settings; pg_settings and pg_extension have no
// rows and keep the fallback spelling), both of replication.go's and both of
// wal.go's. The old wording enumerated "database.go's three, settings.go's three,
// replication.go's two" and then filed wal.go's two as "inside that eight", which
// they are not: that list is eight views and pg_stat_wal and pg_stat_archiver are
// two more. The remaining three keys — pg_stat_statements, pg_stat_bgwriter,
// pg_stat_checkpointer — belong to collectors that have not landed, so the table
// is ahead of §3.3's step list there rather than behind it, and those collectors
// inherit names from rows already here when they do. The other §3.3 collectors
// contribute their views when they land.
var exportNames = map[pgversion.Source]exportEntry{
	// ---- R9.4's aggregate family: the five members R9.4's own sentence names ----
	//
	// "execution time, rows, shared block hits/reads, lock time, per (cluster,
	// db, user)". The names are the ones api.DefaultCatalogue commits to and
	// chart/templates/prometheusrule.yaml already uses (PGSILockWaitDominant
	// reads pgsi_query_lock_time_seconds_total), because the catalogue's header
	// says "Phase 2 implements to this table rather than the other way round".
	{View: pgversion.ViewStatStatements, Column: "total_exec_time"}:  {name: "query_execution_time_seconds_total", typ: TypeCounter, src: "R9.4 aggregate; catalogue aggregate.execution_time"},
	{View: pgversion.ViewStatStatements, Column: "rows"}:             {name: "query_rows_total", typ: TypeCounter, src: "R9.4 aggregate; catalogue aggregate.rows"},
	{View: pgversion.ViewStatStatements, Column: "shared_blks_hit"}:  {name: "query_shared_blocks_hit_total", typ: TypeCounter, src: "R9.4 aggregate; catalogue aggregate.shared_blocks_hit"},
	{View: pgversion.ViewStatStatements, Column: "shared_blks_read"}: {name: "query_shared_blocks_read_total", typ: TypeCounter, src: "R9.4 aggregate; catalogue aggregate.shared_blocks_read"},
	{View: pgversion.ViewStatStatements, Column: "lock_time"}:        {name: "query_lock_time_seconds_total", typ: TypeCounter, src: "R9.4's lock_time member (the R9.11 ruling); PGSILockWaitDominant reads it"},
	// calls is not one of R9.4's five but is R3.2's thirteen-column set's first
	// counter, and R9.5's per-query family cannot rank top-N without it.
	{View: pgversion.ViewStatStatements, Column: "calls"}: {name: "query_calls_total", typ: TypeCounter, src: "R3.2's Load column; R9.5's top-N ranking key"},
	// The two succession rows R2.4 enumerates for this view. Naming the pair once
	// each is the entire point of Canonical: without it, a PG16 cluster exports
	// query_shared_blk_read_time from blk_read_time and a PG17 cluster exports a
	// second family from shared_blk_read_time.
	{View: pgversion.ViewStatStatements, Column: "shared_blk_read_time"}:  {name: "query_shared_blk_read_time_seconds_total", typ: TypeCounter, src: "R2.4's enumerated rename pair (blk_read_time ≤PG16)"},
	{View: pgversion.ViewStatStatements, Column: "shared_blk_write_time"}: {name: "query_shared_blk_write_time_seconds_total", typ: TypeCounter, src: "R2.4's enumerated rename pair (blk_write_time ≤PG16)"},
	// The local/temp block counters R3.2's thirteen columns list alongside the
	// shared ones R9.4 names.
	{View: pgversion.ViewStatStatements, Column: "shared_blks_dirtied"}: {name: "query_shared_blocks_dirtied_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "shared_blks_written"}: {name: "query_shared_blocks_written_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "local_blks_hit"}:      {name: "query_local_blocks_hit_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "local_blks_read"}:     {name: "query_local_blocks_read_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "local_blks_dirtied"}:  {name: "query_local_blocks_dirtied_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "local_blks_written"}:  {name: "query_local_blocks_written_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "temp_blks_read"}:      {name: "query_temp_blocks_read_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "temp_blks_written"}:   {name: "query_temp_blocks_written_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "total_plan_time"}:     {name: "query_plan_time_seconds_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "wal_records"}:         {name: "query_wal_records_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "wal_fpi"}:             {name: "query_wal_fpi_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	{View: pgversion.ViewStatStatements, Column: "wal_bytes"}:           {name: "query_wal_bytes_total", typ: TypeCounter, src: "R3.2's thirteen columns"},
	// The pre-PG13 spelling of total_exec_time. Canonical() maps the PG13+ name
	// here, so this entry is the one MetricName actually resolves to; it is
	// written out rather than implied because the table is the enumeration R14.7
	// checks against.
	{View: pgversion.ViewStatStatements, Column: "total_time"}:     {name: "query_execution_time_seconds_total", typ: TypeCounter, src: "R2.4's first enumerated rename; R9.4 aggregate via its PG13+ successor"},
	{View: pgversion.ViewStatStatements, Column: "blk_read_time"}:  {name: "query_shared_blk_read_time_seconds_total", typ: TypeCounter, src: "R2.4's second enumerated pair"},
	{View: pgversion.ViewStatStatements, Column: "blk_write_time"}: {name: "query_shared_blk_write_time_seconds_total", typ: TypeCounter, src: "R2.4's second enumerated pair"},

	// ---- R9.7's system family, pg_stat_database half ----
	//
	// R5.2 names the families in prose (transactions, tuples, blocks, temp,
	// deadlocks) and D20 says that prose is a floor, not a ceiling: the export
	// set is the prose's families *plus* every numeric column R2.4's matrix
	// returns, "and R9.4's naming function must enumerate that union". The rows
	// below are that union for this view — including the four columns D20 names
	// by hand as the ones R5.2's prose omits (conflicts, checksum_failures,
	// blk_read_time, session_time) and the sessions_* counters it also names.
	//
	// The names are the catalogue's (system.xact_commit → pgsi_db_xact_commit_total
	// and the other twelve R5.2 rows), with the column spellings from the matrix:
	// the catalogue writes tuples_returned where PG spells it tup_returned, so
	// the table carries both and the catalogue's spelling wins.
	{View: pgversion.ViewStatDatabase, Column: "xact_commit"}:   {name: "db_xact_commit_total", typ: TypeCounter, src: "R5.2 transactions; catalogue system.xact_commit"},
	{View: pgversion.ViewStatDatabase, Column: "xact_rollback"}: {name: "db_xact_rollback_total", typ: TypeCounter, src: "R5.2 transactions; catalogue system.xact_rollback"},
	{View: pgversion.ViewStatDatabase, Column: "blks_hit"}:      {name: "db_blks_hit_total", typ: TypeCounter, src: "R5.2 blocks; catalogue system.blks_hit"},
	{View: pgversion.ViewStatDatabase, Column: "blks_read"}:     {name: "db_blks_read_total", typ: TypeCounter, src: "R5.2 blocks; catalogue system.blks_read"},
	{View: pgversion.ViewStatDatabase, Column: "tup_returned"}:  {name: "db_tuples_returned_total", typ: TypeCounter, src: "R5.2 tuples; catalogue system.tuples_returned"},
	{View: pgversion.ViewStatDatabase, Column: "tup_fetched"}:   {name: "db_tuples_fetched_total", typ: TypeCounter, src: "R5.2 tuples; catalogue system.tuples_fetched"},
	{View: pgversion.ViewStatDatabase, Column: "tup_inserted"}:  {name: "db_tuples_inserted_total", typ: TypeCounter, src: "R5.2 tuples; catalogue system.tuples_inserted"},
	{View: pgversion.ViewStatDatabase, Column: "tup_updated"}:   {name: "db_tuples_updated_total", typ: TypeCounter, src: "R5.2 tuples; catalogue system.tuples_updated"},
	{View: pgversion.ViewStatDatabase, Column: "tup_deleted"}:   {name: "db_tuples_deleted_total", typ: TypeCounter, src: "R5.2 tuples; catalogue system.tuples_deleted"},
	{View: pgversion.ViewStatDatabase, Column: "temp_files"}:    {name: "db_temp_files_total", typ: TypeCounter, src: "R5.2 temp; catalogue system.temp_files"},
	{View: pgversion.ViewStatDatabase, Column: "temp_bytes"}:    {name: "db_temp_bytes_total", typ: TypeCounter, src: "R5.2 temp; catalogue system.temp_bytes"},
	{View: pgversion.ViewStatDatabase, Column: "deadlocks"}:     {name: "db_deadlocks_total", typ: TypeCounter, src: "R5.2 deadlocks; catalogue system.deadlocks"},
	// The four D20 names as the prose's omissions, plus the sessions_* family it
	// names in the same breath.
	{View: pgversion.ViewStatDatabase, Column: "conflicts"}:                {name: "db_conflicts_total", typ: TypeCounter, src: "D20: named by R9.7 as an R5.2 omission"},
	{View: pgversion.ViewStatDatabase, Column: "checksum_failures"}:        {name: "db_checksum_failures_total", typ: TypeCounter, src: "D20: named by R9.7 as an R5.2 omission"},
	{View: pgversion.ViewStatDatabase, Column: "blk_read_time"}:            {name: "db_blk_read_time_seconds_total", typ: TypeCounter, src: "D20: named by R9.7 as an R5.2 omission"},
	{View: pgversion.ViewStatDatabase, Column: "blk_write_time"}:           {name: "db_blk_write_time_seconds_total", typ: TypeCounter, src: "D20: named by R9.7 as an R5.2 omission"},
	{View: pgversion.ViewStatDatabase, Column: "session_time"}:             {name: "db_session_time_seconds_total", typ: TypeCounter, src: "D20: named by R9.7 as an R5.2 omission"},
	{View: pgversion.ViewStatDatabase, Column: "active_time"}:              {name: "db_active_time_seconds_total", typ: TypeCounter, src: "D20: the sessions_* family R9.7 names"},
	{View: pgversion.ViewStatDatabase, Column: "idle_in_transaction_time"}: {name: "db_idle_in_transaction_time_seconds_total", typ: TypeCounter, src: "D20: the sessions_* family R9.7 names"},
	{View: pgversion.ViewStatDatabase, Column: "sessions"}:                 {name: "db_sessions_total", typ: TypeCounter, src: "D20: the sessions_* family R9.7 names"},
	{View: pgversion.ViewStatDatabase, Column: "sessions_abandoned"}:       {name: "db_sessions_abandoned_total", typ: TypeCounter, src: "D20: the sessions_* family R9.7 names"},
	{View: pgversion.ViewStatDatabase, Column: "sessions_fatal"}:           {name: "db_sessions_fatal_total", typ: TypeCounter, src: "D20: the sessions_* family R9.7 names"},
	{View: pgversion.ViewStatDatabase, Column: "sessions_killed"}:          {name: "db_sessions_killed_total", typ: TypeCounter, src: "D20: the sessions_* family R9.7 names"},
	// numbackends is the per-database connection gauge R5.2's "backends" family
	// covers from this side; the catalogue's backends_by_state and
	// backends_by_application_name are pg_stat_activity derivations and live with
	// that view.
	{View: pgversion.ViewStatDatabase, Column: "numbackends"}: {name: "db_backends", typ: TypeGauge, src: "R5.2 backends (per-database gauge)"},
	// checksum_last_failure and stats_reset are timestamps that qualify other
	// metrics rather than measurements of their own; R9.1 puts them on the API
	// plane and R3.3 reads stats_reset as the counter epoch.
	{View: pgversion.ViewStatDatabase, Column: "checksum_last_failure"}: {name: "", typ: TypeNotExported, src: "R9.1 text half: qualifies checksum_failures"},
	{View: pgversion.ViewStatDatabase, Column: "stats_reset"}:           {name: "", typ: TypeNotExported, src: "R3.3 counter epoch, not a metric"},
	// The projection's non-value columns, listed so the table is the complete
	// enumeration R14.7 checks rather than a list of the interesting subset.
	{View: pgversion.ViewStatDatabase, Column: "datid"}:   {name: "", typ: TypeNotExported, src: "R9.1: identifier, not a value"},
	{View: pgversion.ViewStatDatabase, Column: "datname"}: {name: "", typ: TypeNotExported, src: "R9.3: the datname label, not a value"},

	// ---- R9.7's system family, pg_database half ----
	//
	// Three of these are database.go's Extras rather than matrix columns: `size`
	// is pg_database_size(oid) and the two ages are age(datfrozenxid) /
	// age(datminmxid), computed in the SELECT because PG renders them as
	// functions of the stored xid. The matrix's own columns (datfrozenxid,
	// datminmxid) are identifiers and stay unexported.
	{View: pgversion.ViewDatabase, Column: "size"}:          {name: "db_size_bytes", typ: TypeGauge, src: "R5.2 database sizes; catalogue system.database_size_bytes"},
	{View: pgversion.ViewDatabase, Column: "frozenxid_age"}: {name: "db_txid_age", typ: TypeGauge, src: "R5.2/R7.5 TXID age; catalogue system.txid_age"},
	{View: pgversion.ViewDatabase, Column: "minmxid_age"}:   {name: "db_mxid_age", typ: TypeGauge, src: "R5.2/R7.5 MXID age; catalogue system.mxid_age"},
	{View: pgversion.ViewDatabase, Column: "datfrozenxid"}:  {name: "", typ: TypeNotExported, src: "R9.1: the xid behind frozenxid_age"},
	{View: pgversion.ViewDatabase, Column: "datminmxid"}:    {name: "", typ: TypeNotExported, src: "R9.1: the xid behind minmxid_age"},
	{View: pgversion.ViewDatabase, Column: "oid"}:           {name: "", typ: TypeNotExported, src: "R9.1: identifier"},
	{View: pgversion.ViewDatabase, Column: "datname"}:       {name: "", typ: TypeNotExported, src: "R9.3: the datname label, not a value"},

	// ---- R9.7's system family, pg_stat_database_conflicts ----
	//
	// D20 names `conflicts` as one of the omissions R5.2's prose leaves out; this
	// view is the per-type breakdown of the pg_stat_database.conflicts counter,
	// so the family names are db_conflicts_<type>_total alongside it.
	{View: pgversion.ViewStatDatabaseConflicts, Column: "confl_lock"}:               {name: "db_conflicts_lock_total", typ: TypeCounter, src: "D20: R5.2's conflicts omission, per type"},
	{View: pgversion.ViewStatDatabaseConflicts, Column: "confl_tablespace"}:         {name: "db_conflicts_tablespace_total", typ: TypeCounter, src: "D20: R5.2's conflicts omission, per type"},
	{View: pgversion.ViewStatDatabaseConflicts, Column: "confl_snapshot"}:           {name: "db_conflicts_snapshot_total", typ: TypeCounter, src: "D20: R5.2's conflicts omission, per type"},
	{View: pgversion.ViewStatDatabaseConflicts, Column: "confl_bufferpin"}:          {name: "db_conflicts_bufferpin_total", typ: TypeCounter, src: "D20: R5.2's conflicts omission, per type"},
	{View: pgversion.ViewStatDatabaseConflicts, Column: "confl_deadlock"}:           {name: "db_conflicts_deadlock_total", typ: TypeCounter, src: "D20: R5.2's conflicts omission, per type"},
	{View: pgversion.ViewStatDatabaseConflicts, Column: "confl_active_logicalslot"}: {name: "db_conflicts_active_logicalslot_total", typ: TypeCounter, src: "D20: R5.2's conflicts omission, per type (PG16+)"},
	{View: pgversion.ViewStatDatabaseConflicts, Column: "datid"}:                    {name: "", typ: TypeNotExported, src: "R9.1: identifier"},
	{View: pgversion.ViewStatDatabaseConflicts, Column: "datname"}:                  {name: "", typ: TypeNotExported, src: "R9.3: the datname label, not a value"},

	// ---- R9.7's system family, pg_stat_wal and pg_stat_archiver ----
	//
	// wal_bytes is the catalogue's system.wal_generated_bytes: R5.2's WAL family
	// covers "bytes generated" and the column is wal_bytes, so the committed name
	// renames it and the table carries the rename rather than the column.
	//
	// The four PG18 retirements (wal_write, wal_sync, wal_write_time,
	// wal_sync_time) keep names here even though PG18 stops exporting them,
	// because a metric that stops existing is still a documented metric — and
	// succession.go's four retire rows are what records that it stopped.
	{View: pgversion.ViewStatWAL, Column: "wal_records"}:      {name: "wal_records_total", typ: TypeCounter, src: "R5.2 WAL; D20's union over pg_stat_wal"},
	{View: pgversion.ViewStatWAL, Column: "wal_fpi"}:          {name: "wal_fpi_total", typ: TypeCounter, src: "R5.2 WAL; D20's union over pg_stat_wal"},
	{View: pgversion.ViewStatWAL, Column: "wal_bytes"}:        {name: "wal_generated_bytes_total", typ: TypeCounter, src: "R5.2 WAL; catalogue system.wal_generated_bytes"},
	{View: pgversion.ViewStatWAL, Column: "wal_buffers_full"}: {name: "wal_buffers_full_total", typ: TypeCounter, src: "R5.2 WAL; D20's union over pg_stat_wal"},
	{View: pgversion.ViewStatWAL, Column: "wal_write"}:        {name: "wal_write_total", typ: TypeCounter, src: "R5.2 WAL; retired at PG18 (succession.go)"},
	{View: pgversion.ViewStatWAL, Column: "wal_sync"}:         {name: "wal_sync_total", typ: TypeCounter, src: "R5.2 WAL; retired at PG18 (succession.go)"},
	{View: pgversion.ViewStatWAL, Column: "wal_write_time"}:   {name: "wal_write_time_seconds_total", typ: TypeCounter, src: "R5.2 WAL; retired at PG18 (succession.go)"},
	{View: pgversion.ViewStatWAL, Column: "wal_sync_time"}:    {name: "wal_sync_time_seconds_total", typ: TypeCounter, src: "R5.2 WAL; retired at PG18 (succession.go)"},
	// wal.go exports stats_reset as a gauge (the unix timestamp of the last
	// pg_stat_statements reset) rather than only as R3.3's in-band epoch, because
	// R3.3's counter-reset detection has to be able to answer the question from
	// the metric plane for a cluster whose samples it no longer holds.
	{View: pgversion.ViewStatWAL, Column: "stats_reset"}: {name: "wal_stats_reset_timestamp_seconds", typ: TypeGauge, src: "R3.3 counter epoch, exported as the conventional _timestamp_seconds gauge"},

	{View: pgversion.ViewStatArchiver, Column: "archived_count"}:     {name: "archiver_archived_count_total", typ: TypeCounter, src: "R5.2 WAL archiving; D20's union over pg_stat_archiver"},
	{View: pgversion.ViewStatArchiver, Column: "failed_count"}:       {name: "archiver_failed_count_total", typ: TypeCounter, src: "R5.2 WAL archiving; D20's union over pg_stat_archiver"},
	{View: pgversion.ViewStatArchiver, Column: "last_archived_time"}: {name: "archiver_last_archived_timestamp_seconds", typ: TypeGauge, src: "R5.2 WAL archiving; D20's union over pg_stat_archiver"},
	{View: pgversion.ViewStatArchiver, Column: "last_failed_time"}:   {name: "archiver_last_failed_timestamp_seconds", typ: TypeGauge, src: "R5.2 WAL archiving; D20's union over pg_stat_archiver"},
	{View: pgversion.ViewStatArchiver, Column: "stats_reset"}:        {name: "archiver_stats_reset_timestamp_seconds", typ: TypeGauge, src: "R3.3 counter epoch, exported as the conventional _timestamp_seconds gauge"},
	// The two WAL-file *name* columns are text (000000010000000000000001) and R9.1
	// puts them on the API plane; wal.go reads them for the detail response.
	{View: pgversion.ViewStatArchiver, Column: "last_archived_wal"}: {name: "", typ: TypeNotExported, src: "R9.1 text half"},
	{View: pgversion.ViewStatArchiver, Column: "last_failed_wal"}:   {name: "", typ: TypeNotExported, src: "R9.1 text half"},

	// ---- R9.7's system family, the pg_stat_bgwriter → pg_stat_checkpointer move ----
	//
	// This is the case succession.go exists for and the reason a formula-based
	// MetricName fails: R2.4 calls it out as "the case a rename table gets wrong",
	// and IMPLEMENTATION.md:661 already warns that the chart's two speculative
	// rules "change if Phase 2 picks others". The catalogue picked these two, so
	// both spellings of each pair resolve to one name and the chart's rules and
	// the exporter agree on PG16 and PG17 both.
	{View: pgversion.ViewStatBgwriter, Column: "checkpoints_timed"}:     {name: "checkpoints_timed_total", typ: TypeCounter, src: "R5.2 checkpoints; catalogue system.checkpoints_timed"},
	{View: pgversion.ViewStatBgwriter, Column: "checkpoints_req"}:       {name: "checkpoints_requested_total", typ: TypeCounter, src: "R5.2 checkpoints; catalogue system.checkpoints_requested"},
	{View: pgversion.ViewStatBgwriter, Column: "checkpoint_write_time"}: {name: "checkpoint_write_time_seconds_total", typ: TypeCounter, src: "R5.2 checkpoints; D20's union"},
	{View: pgversion.ViewStatBgwriter, Column: "checkpoint_sync_time"}:  {name: "checkpoint_sync_time_seconds_total", typ: TypeCounter, src: "R5.2 checkpoints; D20's union"},
	{View: pgversion.ViewStatBgwriter, Column: "buffers_checkpoint"}:    {name: "buffers_checkpoint_total", typ: TypeCounter, src: "R5.2 checkpoints; D20's union"},
	{View: pgversion.ViewStatBgwriter, Column: "buffers_backend"}:       {name: "buffers_backend_total", typ: TypeCounter, src: "R5.2 checkpoints; retired at PG18 (succession.go)"},
	{View: pgversion.ViewStatBgwriter, Column: "buffers_backend_fsync"}: {name: "buffers_backend_fsync_total", typ: TypeCounter, src: "R5.2 checkpoints; retired at PG18 (succession.go)"},
	// The four survivors at PG17 (matrix_data.go's PG17 bgwriter set is exactly
	// these plus stats_reset).
	{View: pgversion.ViewStatBgwriter, Column: "buffers_clean"}:    {name: "bgwriter_buffers_clean_total", typ: TypeCounter, src: "R5.2 checkpoints family; D20's union"},
	{View: pgversion.ViewStatBgwriter, Column: "maxwritten_clean"}: {name: "bgwriter_maxwritten_clean_total", typ: TypeCounter, src: "R5.2 checkpoints family; D20's union"},
	{View: pgversion.ViewStatBgwriter, Column: "buffers_alloc"}:    {name: "bgwriter_buffers_alloc_total", typ: TypeCounter, src: "R5.2 checkpoints family; D20's union"},
	{View: pgversion.ViewStatBgwriter, Column: "stats_reset"}:      {name: "bgwriter_stats_reset_timestamp_seconds", typ: TypeGauge, src: "R3.3 counter epoch"},

	{View: pgversion.ViewStatCheckpointer, Column: "num_timed"}:           {name: "checkpoints_timed_total", typ: TypeCounter, src: "R5.2 checkpoints; catalogue system.checkpoints_timed (PG17+ spelling)"},
	{View: pgversion.ViewStatCheckpointer, Column: "num_requested"}:       {name: "checkpoints_requested_total", typ: TypeCounter, src: "R5.2 checkpoints; catalogue system.checkpoints_requested (PG17+ spelling)"},
	{View: pgversion.ViewStatCheckpointer, Column: "write_time"}:          {name: "checkpoint_write_time_seconds_total", typ: TypeCounter, src: "R5.2 checkpoints (PG17+ spelling)"},
	{View: pgversion.ViewStatCheckpointer, Column: "sync_time"}:           {name: "checkpoint_sync_time_seconds_total", typ: TypeCounter, src: "R5.2 checkpoints (PG17+ spelling)"},
	{View: pgversion.ViewStatCheckpointer, Column: "buffers_written"}:     {name: "buffers_checkpoint_total", typ: TypeCounter, src: "R5.2 checkpoints (PG17+ spelling)"},
	{View: pgversion.ViewStatCheckpointer, Column: "restartpoints_timed"}: {name: "restartpoints_timed_total", typ: TypeCounter, src: "D20's union over pg_stat_checkpointer"},
	{View: pgversion.ViewStatCheckpointer, Column: "restartpoints_req"}:   {name: "restartpoints_requested_total", typ: TypeCounter, src: "D20's union over pg_stat_checkpointer"},
	{View: pgversion.ViewStatCheckpointer, Column: "restartpoints_done"}:  {name: "restartpoints_done_total", typ: TypeCounter, src: "D20's union over pg_stat_checkpointer"},
	{View: pgversion.ViewStatCheckpointer, Column: "stats_reset"}:         {name: "checkpointer_stats_reset_timestamp_seconds", typ: TypeGauge, src: "R3.3 counter epoch"},

	// ---- R9.7's system family, the replication views ----
	//
	// replication.go's families are per-cluster aggregates over rows the matrix
	// gives per walsender and per slot, so the aliases it projects (count,
	// active_count, lost_count, <lag>_seconds_max) are the table's keys for this
	// pair of views exactly as `size` and `frozenxid_age` are pg_database's.
	{View: pgversion.ViewStatReplication, Column: "count"}:                  {name: "replication_streaming_count", typ: TypeGauge, src: "R5.2 replication: walsenders on this primary"},
	{View: pgversion.ViewStatReplication, Column: "write_lag_seconds_max"}:  {name: "replication_write_lag_seconds", typ: TypeGauge, src: "R5.2 replication lag: worst standby"},
	{View: pgversion.ViewStatReplication, Column: "flush_lag_seconds_max"}:  {name: "replication_flush_lag_seconds", typ: TypeGauge, src: "R5.2 replication lag: worst standby"},
	{View: pgversion.ViewStatReplication, Column: "replay_lag_seconds_max"}: {name: "replication_replay_lag_seconds", typ: TypeGauge, src: "R5.2 replication lag: worst standby"},
	{View: pgversion.ViewStatReplication, Column: "sync_count"}:             {name: "replication_sync_count", typ: TypeGauge, src: "R5.2 replication: sync-state standbys (R5.5 topology)"},
	{View: pgversion.ViewReplicationSlots, Column: "count"}:                 {name: "replication_slots_count", typ: TypeGauge, src: "R5.2 slots: slots defined"},
	{View: pgversion.ViewReplicationSlots, Column: "active_count"}:          {name: "replication_slots_active_count", typ: TypeGauge, src: "R5.2 slots: active slots"},
	{View: pgversion.ViewReplicationSlots, Column: "lost_count"}:            {name: "replication_slots_lost_count", typ: TypeGauge, src: "R5.2 slots: wal_status='lost'"},
	{View: pgversion.ViewReplicationSlots, Column: "inactive_count"}:        {name: "replication_slots_inactive_count", typ: TypeGauge, src: "R5.2 slots: inactive slots (PG17+ inactive_since)"},

	// ---- R9.7's system family, the settings views ----
	//
	// settings.go names its families after the *row* (one per GUC, one per
	// extension) rather than after a column, because R9.3's label set has no slot
	// for a GUC name and a pg_settings_setting family with 400 rows all writing
	// the same series is the alternative. Those names are built by familyFor, not
	// by this table, and the table carries only the three counts pg_file_settings
	// collapses to.
	{View: pgversion.ViewFileSettings, Column: "count"}:         {name: "file_settings_count", typ: TypeGauge, src: "R5.2 settings card: lines postgresql.conf sets"},
	{View: pgversion.ViewFileSettings, Column: "applied_count"}: {name: "file_settings_applied_count", typ: TypeGauge, src: "R5.2 settings card: lines that applied"},
	{View: pgversion.ViewFileSettings, Column: "error_count"}:   {name: "file_settings_error_count", typ: TypeGauge, src: "R5.2 settings card: lines carrying an error (PGSISettingsNotApplied)"},
}

// Canonical returns the spelling a (view, column) pair's metric family is named
// after — the head of its succession chain — which is what makes one renamed
// column produce one metric name across two majors.
//
// The walk goes backwards (a pair that is some row's `To` becomes that row's
// `From`, repeatedly) for two reasons the exported MetricName depends on:
//
//   - The table is keyed by the canonical spelling, and the canonical has to be
//     computable from a column name alone. Resolve needs a major; this does not,
//     because the answer is a property of the identity and not of the major —
//     pg_stat_checkpointer.num_timed means the same thing on every major where it
//     exists, and asking "which major am I on" to name a column is how the
//     pre-R9.4 formula ended up with two names for one metric.
//   - Going *forwards* (the Resolve direction) needs the major to pick which
//     successor applies, and followChain already handles the case where that
//     successor changed again. Going backwards needs neither.
//
// Splits are the one kind the walk does not cross. A 1→3 split gives each of its
// three `To` members its own family (pg_stat_io.op_bytes becomes read, write and
// extend — three values, three names), so walking a split backwards would collapse
// three distinct metrics into one. Renames, moves and retires all cross: a move's
// two spellings are one value, and a retire's single spelling is its own head.
//
// A pair with no succession row is its own canonical, which is the case for the
// overwhelming majority of columns and the reason this is a lookup rather than a
// requirement.
func Canonical(view pgversion.View, column string) pgversion.Source {
	src := pgversion.Source{View: view, Column: column}
	// The seen set bounds the walk. ValidateSuccessions rejects a cycle, so this
	// is a guard against a table bug rather than a live case — the same reason
	// pgversion's own followChain carries one.
	seen := map[pgversion.Source]bool{}
	for {
		row, ok := predecessorsBySource[src]
		if !ok || seen[src] {
			return src
		}
		seen[src] = true
		src = row.From
	}
}

// predecessorsBySource indexes the succession table backwards: for every
// (view, column) that is somebody's successor, the row that produced it.
//
// pgversion exports the forward index (SuccessorOf) and keeps the backwards one
// private, so this builds its own from Successions() — which is exported for
// exactly this consumer ("returns the table itself, for the R9.4 naming review
// and for tests"). Building it once at init rather than rescanning fifteen rows
// per call is the same shape pgversion uses for its own two indexes.
//
// Two rules the map encodes, both of which Canonical depends on:
//
//   - A KindSplit row's To members are skipped. A split's successors are three
//     metrics (pg_stat_io.op_bytes → read_bytes, write_bytes, extend_bytes) and
//     pulling them back onto their shared pre-image would give three distinct
//     values one family name.
//   - A pair that appears in more than one non-split row's To list is dropped
//     rather than resolved to one of them. ValidateSuccessions makes that shape a
//     table bug rather than a real case, and "its own head" is the answer that
//     does not change when the table is reordered — which a naming function's
//     output must not.
var predecessorsBySource = func() map[pgversion.Source]pgversion.Succession {
	m := make(map[pgversion.Source]pgversion.Succession)
	// Source is comparable (View and Column are both strings); Succession is not
	// (To is a slice), so two rows claiming the same successor are detected by
	// comparing the two rows' From.
	ambiguous := make(map[pgversion.Source]bool)
	for _, s := range pgversion.Successions() {
		if s.Kind == pgversion.KindSplit {
			continue
		}
		for _, to := range s.To {
			if prev, ok := m[to]; ok && prev.From != s.From {
				ambiguous[to] = true
				continue
			}
			m[to] = s
		}
	}
	for src := range ambiguous {
		delete(m, src)
	}
	return m
}()

// MetricTypeOf returns the Prometheus type of the family a (view, column) pair's
// values belong to, and false for a pair that has no family (an identifier, a
// name column, a counter epoch).
//
// The name carries the `Of` because Go will not let a type and a function share
// one name, and MetricType — the type — is the half the renderer needs first.
//
// The renderer needs this and cannot derive it from the name: `# TYPE` is
// per-family, and a counter whose name lacks `_total` fails
// `promtool check metrics`, which Phase 2's acceptance runs.
func MetricTypeOf(view pgversion.View, column string) (MetricType, bool) {
	e, ok := exportNames[Canonical(view, column)]
	if !ok || e.typ == TypeNotExported {
		return TypeNotExported, false
	}
	return e.typ, true
}

// ExportedMetricNames returns every documented family name in the table, sorted,
// without the R9.2 prefix.
//
// It exists because R14.7 makes "a metric that exists in code but not in docs" a
// violation, and the only way to check that at the collector layer is to have one
// list the docs can be diffed against. api.Catalogue.MetricNames() is the same
// list on the API side, prefixed; the two are compared by
// TestExportedNamesCoverCatalogue.
func ExportedMetricNames() []string {
	out := make([]string, 0, len(exportNames))
	for _, e := range exportNames {
		if e.name == "" {
			continue
		}
		out = append(out, e.name)
	}
	// R9.12's pgsi_up has no (view, column) — it is a per-instance health gauge,
	// not a Postgres column — so it is not in the table and has to be added here
	// for the list to be the whole surface. UpMetricBase is its unprefixed half;
	// up.go renders prefix+base so R9.2's configurable prefix still applies.
	out = append(out, UpMetricBase)
	sort.Strings(out)
	return dedupe(out)
}

// dedupe removes adjacent duplicates from a sorted slice.
func dedupe(in []string) []string {
	out := in[:0:0]
	for i, s := range in {
		if i > 0 && in[i-1] == s {
			continue
		}
		out = append(out, s)
	}
	return out
}
