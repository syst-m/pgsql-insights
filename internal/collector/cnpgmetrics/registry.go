// Package cnpgmetrics is requirements.md §3.5's `cnpgmetrics/` entry (:240,
// "S3 parser: cnpg_* + cnpg_collector_* (R2.6)") and R2.6 itself (:296):
// "Parse the `cnpg_*` / `cnpg_collector_*` exposition from S3 into typed structs
// (not a generic Prometheus scrape passthrough), so we can serve the same numbers
// through the API and re-export them under our own prefix."
//
// The package splits in two, and the split is the requirement.
//
// registry.go is the typed set: every `cnpg_*` family the exporter can emit, with
// its Prometheus type and the exporter's own grouping. It is what makes this a
// cnpg parser rather than a Prometheus one. A generic text parser accepts any
// family name and returns a map; R2.6 forbids that, because a map keyed by
// whatever the endpoint happened to serve gives the API and the re-exporter no
// stable surface to read — the family set changes when CNPG ships, and a passthrough
// hides that behind "the metric disappeared" with no way to tell a removed family
// from a renamed one. So Parse consults the registry and errors on a `cnpg_*`
// family it does not have a row for. That single rule is the difference between
// parsing the cnpg exposition and parsing a scrape that happens to contain it.
//
// parse.go is the lexer: the Prometheus text format, plus the one rule a naive
// implementation gets wrong. `go_gc_duration_seconds` is a summary, and its
// samples are `go_gc_duration_seconds_sum` and `go_gc_duration_seconds_count`;
// a parser that derives families from sample lines instead of from `# TYPE` lines
// invents two families per fixture. All four fixtures have exactly two such
// names, and TestParseFixtures asserts the count rather than eyeballing it.
//
// The `go_*` families are parsed but parked. R2.6's sentence names two prefixes,
// `cnpg_*` and `cnpg_collector_*`; the runtime metrics beside them belong to the
// instance manager's Go runtime, not to the Postgres surface R2.6 scopes, and
// collector.MetricType has no summary or histogram member to type them with. They
// come back as Scrape.Other with their declared type as a string and no registry
// entry, which keeps them visible to a caller without pretending they are ours.
//
// What is deliberately NOT here: the cnpg-family -> pgsi-metric-name mapping. That
// is R9.4's table (internal/collector/naming.go), and only 35 of the 59 cnpg
// families that map onto a pgversion view/column pair have a row there today; the
// rest have no PG column at all (cnpg_backends_total is a COUNT(*) over
// pg_stat_activity, cnpg_pg_database_size_bytes is pg_database_size()). Minting
// names here would be a second naming table, which is R16.8's producer/consumer
// disagreement class — the same reason naming.go's "eight views" comment had to be
// deleted rather than corrected. The re-export bridge waits until R9.4's table
// covers the surface.
package cnpgmetrics

import "github.com/frankellis/pgsql-insights/internal/collector"

// Group is one family grouping of the CNPG instance manager's exporter. The
// groups are the exporter's own, not ours: each is either a Postgres view the
// exporter's default query set reads (GroupStatDatabase is the 14
// pg_stat_database families plus pg_stat_database_conflicts) or one of its
// collector packages (GroupCollector is the cnpg_collector_* set, GroupPGBouncer
// the cnpg_pgbouncer_* set). GroupInstance is the two stragglers — cnpg_last_error
// and cnpg_last_update_timestamp — that the exporter emits at instance level with
// no view behind them.
//
// The grouping is data because R2.6's "typed structs" need a shape: without it the
// registry is 147 unrelated names and the API cannot answer "give me the replication
// numbers" without a second, ad-hoc classification written at the call site.
type Group string

const (
	GroupPGBouncer        Group = "pgbouncer"
	GroupCollector        Group = "collector"
	GroupStatDatabase     Group = "pg_stat_database"
	GroupStatBGWriter     Group = "pg_stat_bgwriter"
	GroupStatArchiver     Group = "pg_stat_archiver"
	GroupStatCheckpointer Group = "pg_stat_checkpointer"
	GroupStatReplication  Group = "pg_stat_replication"
	GroupReplication      Group = "pg_replication"
	GroupBackends         Group = "backends"
	GroupDatabase         Group = "pg_database"
	GroupCache            Group = "cache"
	GroupInstance         Group = "instance"
	GroupReplicationSlots Group = "pg_replication_slots"
	GroupExtensions       Group = "pg_extensions"
	GroupPostmaster       Group = "pg_postmaster"
	GroupSettings         Group = "pg_settings"
)

// Family is one known cnpg_* family: its name, its Prometheus type, and its group.
//
// Type reuses collector.MetricType rather than declaring a local enum. That enum
// (internal/collector/naming.go) is the project's one answer to "what Prometheus
// type is this family", and a second enum with the same three members is the
// stated-twice failure class; importing it is also acyclic, since collector's
// registry takes its collectors as a []Collector argument and does not import this
// package back.
type Family struct {
	Name  string
	Type  collector.MetricType
	Group Group
}

// families is the registry: every cnpg_* family present in the four R16.3
// fixtures, grouped and sorted. The union across the fixtures is 147 families;
// the per-file counts are lower (89/51/78/86) because a 9127 pooler pod serves the
// pgbouncer set and no pg_stat_* set, and a 9187 instance pod serves no
// cnpg_pgbouncer_* set.
//
// The table is derived from the fixtures, not from the CNPG docs, and that is the
// honest source: docs/research/cloudnativepg.md documents the exporter's default
// query list, but the query list and the exposition it produces are not the same
// artefact (IMPLEMENTATION.md D19 is a worked example of exactly that gap — the
// doc's `pg_extensions` query reads pg_available_extensions and exports
// cnpg_pg_extensions_update_available). A registry validated against recorded
// output cannot drift from the output; one validated against prose can.
var families = []Family{
	// ---- pgbouncer (51) ----
	{Name: "cnpg_pgbouncer_collection_duration_seconds", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_collections_total", Type: collector.TypeCounter, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_last_collection_error", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_databases", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_dns_names", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_dns_pending", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_dns_queries", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_dns_zones", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_free_clients", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_free_servers", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_login_clients", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_pools", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_used_clients", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_used_servers", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_lists_users", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_cl_active", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_cl_active_cancel_req", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_cl_cancel_req", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_cl_waiting", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_cl_waiting_cancel_req", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_load_balance_hosts", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_maxwait", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_maxwait_us", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_pool_mode", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_sv_active", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_sv_active_cancel", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_sv_idle", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_sv_login", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_sv_tested", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_sv_used", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_pools_sv_wait_cancels", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_bind_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_client_parse_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_query_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_query_time", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_recv", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_sent", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_server_parse_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_wait_time", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_xact_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_avg_xact_time", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_bind_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_client_parse_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_query_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_query_time", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_received", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_sent", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_server_parse_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_wait_time", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_xact_count", Type: collector.TypeGauge, Group: GroupPGBouncer},
	{Name: "cnpg_pgbouncer_stats_total_xact_time", Type: collector.TypeGauge, Group: GroupPGBouncer},
	// ---- collector (24) ----
	{Name: "cnpg_collector_collection_duration_seconds", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_collections_total", Type: collector.TypeCounter, Group: GroupCollector},
	{Name: "cnpg_collector_fencing_on", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_first_recoverability_point", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_last_available_backup_timestamp", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_last_collection_error", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_last_failed_backup_timestamp", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_lo_pages", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_manual_switchover_required", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_nodes_used", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_pg_wal", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_pg_wal_archive_status", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_postgres_version", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_replica_mode", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_sync_replicas", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_up", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_wal_buffers_full", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_wal_bytes", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_wal_fpi", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_wal_records", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_wal_sync", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_wal_sync_time", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_wal_write", Type: collector.TypeGauge, Group: GroupCollector},
	{Name: "cnpg_collector_wal_write_time", Type: collector.TypeGauge, Group: GroupCollector},
	// ---- pg_stat_database (15) ----
	{Name: "cnpg_pg_stat_database_blk_read_time", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_blk_write_time", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_blks_hit", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_blks_read", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_conflicts", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_deadlocks", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_temp_bytes", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_temp_files", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_tup_deleted", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_tup_fetched", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_tup_inserted", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_tup_returned", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_tup_updated", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_xact_commit", Type: collector.TypeCounter, Group: GroupStatDatabase},
	{Name: "cnpg_pg_stat_database_xact_rollback", Type: collector.TypeCounter, Group: GroupStatDatabase},
	// ---- pg_stat_bgwriter (11) ----
	{Name: "cnpg_pg_stat_bgwriter_buffers_alloc", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_buffers_backend", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_buffers_backend_fsync", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_buffers_checkpoint", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_buffers_clean", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_checkpoint_sync_time", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_checkpoint_write_time", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_checkpoints_req", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_checkpoints_timed", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_maxwritten_clean", Type: collector.TypeCounter, Group: GroupStatBGWriter},
	{Name: "cnpg_pg_stat_bgwriter_stats_reset_time", Type: collector.TypeGauge, Group: GroupStatBGWriter},
	// ---- pg_stat_archiver (9) ----
	{Name: "cnpg_pg_stat_archiver_archived_count", Type: collector.TypeCounter, Group: GroupStatArchiver},
	{Name: "cnpg_pg_stat_archiver_failed_count", Type: collector.TypeCounter, Group: GroupStatArchiver},
	{Name: "cnpg_pg_stat_archiver_last_archived_time", Type: collector.TypeGauge, Group: GroupStatArchiver},
	{Name: "cnpg_pg_stat_archiver_last_archived_wal_start_lsn", Type: collector.TypeGauge, Group: GroupStatArchiver},
	{Name: "cnpg_pg_stat_archiver_last_failed_time", Type: collector.TypeGauge, Group: GroupStatArchiver},
	{Name: "cnpg_pg_stat_archiver_last_failed_wal_start_lsn", Type: collector.TypeGauge, Group: GroupStatArchiver},
	{Name: "cnpg_pg_stat_archiver_seconds_since_last_archival", Type: collector.TypeGauge, Group: GroupStatArchiver},
	{Name: "cnpg_pg_stat_archiver_seconds_since_last_failure", Type: collector.TypeGauge, Group: GroupStatArchiver},
	{Name: "cnpg_pg_stat_archiver_stats_reset_time", Type: collector.TypeGauge, Group: GroupStatArchiver},
	// ---- pg_stat_checkpointer (9) ----
	{Name: "cnpg_pg_stat_checkpointer_buffers_written", Type: collector.TypeCounter, Group: GroupStatCheckpointer},
	{Name: "cnpg_pg_stat_checkpointer_checkpoints_req", Type: collector.TypeCounter, Group: GroupStatCheckpointer},
	{Name: "cnpg_pg_stat_checkpointer_checkpoints_timed", Type: collector.TypeCounter, Group: GroupStatCheckpointer},
	{Name: "cnpg_pg_stat_checkpointer_restartpoints_done", Type: collector.TypeCounter, Group: GroupStatCheckpointer},
	{Name: "cnpg_pg_stat_checkpointer_restartpoints_req", Type: collector.TypeCounter, Group: GroupStatCheckpointer},
	{Name: "cnpg_pg_stat_checkpointer_restartpoints_timed", Type: collector.TypeCounter, Group: GroupStatCheckpointer},
	{Name: "cnpg_pg_stat_checkpointer_stats_reset_time", Type: collector.TypeGauge, Group: GroupStatCheckpointer},
	{Name: "cnpg_pg_stat_checkpointer_sync_time", Type: collector.TypeCounter, Group: GroupStatCheckpointer},
	{Name: "cnpg_pg_stat_checkpointer_write_time", Type: collector.TypeCounter, Group: GroupStatCheckpointer},
	// ---- pg_stat_replication (9) ----
	{Name: "cnpg_pg_stat_replication_backend_start", Type: collector.TypeCounter, Group: GroupStatReplication},
	{Name: "cnpg_pg_stat_replication_backend_xmin_age", Type: collector.TypeCounter, Group: GroupStatReplication},
	{Name: "cnpg_pg_stat_replication_flush_diff_bytes", Type: collector.TypeGauge, Group: GroupStatReplication},
	{Name: "cnpg_pg_stat_replication_flush_lag_seconds", Type: collector.TypeGauge, Group: GroupStatReplication},
	{Name: "cnpg_pg_stat_replication_replay_diff_bytes", Type: collector.TypeGauge, Group: GroupStatReplication},
	{Name: "cnpg_pg_stat_replication_replay_lag_seconds", Type: collector.TypeGauge, Group: GroupStatReplication},
	{Name: "cnpg_pg_stat_replication_sent_diff_bytes", Type: collector.TypeGauge, Group: GroupStatReplication},
	{Name: "cnpg_pg_stat_replication_write_diff_bytes", Type: collector.TypeGauge, Group: GroupStatReplication},
	{Name: "cnpg_pg_stat_replication_write_lag_seconds", Type: collector.TypeGauge, Group: GroupStatReplication},
	// ---- pg_replication (4) ----
	{Name: "cnpg_pg_replication_in_recovery", Type: collector.TypeGauge, Group: GroupReplication},
	{Name: "cnpg_pg_replication_is_wal_receiver_up", Type: collector.TypeGauge, Group: GroupReplication},
	{Name: "cnpg_pg_replication_lag", Type: collector.TypeGauge, Group: GroupReplication},
	{Name: "cnpg_pg_replication_streaming_replicas", Type: collector.TypeGauge, Group: GroupReplication},
	// ---- backends (3) ----
	{Name: "cnpg_backends_max_tx_duration_seconds", Type: collector.TypeGauge, Group: GroupBackends},
	{Name: "cnpg_backends_total", Type: collector.TypeGauge, Group: GroupBackends},
	{Name: "cnpg_backends_waiting_total", Type: collector.TypeGauge, Group: GroupBackends},
	// ---- pg_database (3) ----
	{Name: "cnpg_pg_database_mxid_age", Type: collector.TypeGauge, Group: GroupDatabase},
	{Name: "cnpg_pg_database_size_bytes", Type: collector.TypeGauge, Group: GroupDatabase},
	{Name: "cnpg_pg_database_xid_age", Type: collector.TypeGauge, Group: GroupDatabase},
	// ---- cache (2) ----
	{Name: "cnpg_cache_hits", Type: collector.TypeGauge, Group: GroupCache},
	{Name: "cnpg_cache_miss", Type: collector.TypeGauge, Group: GroupCache},
	// ---- instance (2) ----
	{Name: "cnpg_last_error", Type: collector.TypeGauge, Group: GroupInstance},
	{Name: "cnpg_last_update_timestamp", Type: collector.TypeGauge, Group: GroupInstance},
	// ---- pg_replication_slots (2) ----
	{Name: "cnpg_pg_replication_slots_active", Type: collector.TypeGauge, Group: GroupReplicationSlots},
	{Name: "cnpg_pg_replication_slots_pg_wal_lsn_diff", Type: collector.TypeGauge, Group: GroupReplicationSlots},
	// ---- pg_extensions (1) ----
	{Name: "cnpg_pg_extensions_update_available", Type: collector.TypeGauge, Group: GroupExtensions},
	// ---- pg_postmaster (1) ----
	{Name: "cnpg_pg_postmaster_start_time", Type: collector.TypeGauge, Group: GroupPostmaster},
	// ---- pg_settings (1) ----
	{Name: "cnpg_pg_settings_setting", Type: collector.TypeGauge, Group: GroupSettings},
}

// groupOrder is the declaration order behind Groups(), so the accessor's
// ordering is the const block's ordering and not a sort that could differ from it.

// groupOrder is the declaration order behind Groups(), so the accessor's
// ordering is the const block's ordering and not a sort that could differ from it.
var groupOrder = []Group{
	GroupPGBouncer,
	GroupCollector,
	GroupStatDatabase,
	GroupStatBGWriter,
	GroupStatArchiver,
	GroupStatCheckpointer,
	GroupStatReplication,
	GroupReplication,
	GroupBackends,
	GroupDatabase,
	GroupCache,
	GroupInstance,
	GroupReplicationSlots,
	GroupExtensions,
	GroupPostmaster,
	GroupSettings,
}

// index is the by-name view of families, built once at init.
//
// A map rather than a binary search over the slice because Parse does one lookup
// per family per scrape and the scrape is on the collection path. The map is
// derived from the slice, never written to separately, so the two cannot disagree
// — which is the property R16.8 exists to protect.
var index = func() map[string]Family {
	m := make(map[string]Family, len(families))
	for _, f := range families {
		m[f.Name] = f
	}
	return m
}()

// Lookup returns the registry entry for a family name.
//
// The second result matters more than the first: R2.6's "not a generic Prometheus
// scrape passthrough" is enforced by callers being able to ask, exactly, whether a
// family the endpoint served is one we know. A Lookup that returned a zero Family
// and no bool would let a caller treat an unknown family as a known one with an
// empty type.
func Lookup(name string) (Family, bool) {
	f, ok := index[name]
	return f, ok
}

// Families returns every registered family, in the order they are declared in the
// table (grouped, and alphabetical inside each group).
func Families() []Family {
	out := make([]Family, len(families))
	copy(out, families)
	return out
}

// FamiliesByGroup returns the registered families in one group.
func FamiliesByGroup(g Group) []Family {
	var out []Family
	for _, f := range families {
		if f.Group == g {
			out = append(out, f)
		}
	}
	return out
}

// Groups returns every group the registry uses, in declaration order.
func Groups() []Group {
	out := make([]Group, 0, len(groupOrder))
	for _, g := range groupOrder {
		out = append(out, g)
	}
	return out
}
