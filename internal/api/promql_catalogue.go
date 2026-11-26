// The PromQL catalogue (R8.3).
//
// R8.3's pair answers a PromQL query from *our* store, and the sentence that
// makes it implementable is the parenthetical: "because §3.4 keeps raw samples
// out of SQLite and we ship no PromQL evaluator — the pair serves a **fixed
// catalogue of named queries** (the R9.4 aggregate and R9.5 per-query rollups,
// serialized in Prometheus result form) and returns error.code=PromQLUnavailable
// for anything else".
//
// That is the clause this file exists to make keepable. Without a named
// catalogue, "we answer from our own store" means "we answer whatever we happen
// to have cached", which is not a contract anyone can write a dashboard against:
// with an evaluator absent, an arbitrary PromQL string cannot be evaluated at
// all, so the honest surface is a list — the queries we do answer, named, with
// the exact PromQL behind each name. R8.3 requires the list to be enumerated in
// the OpenAPI document for exactly that reason, and the enumeration is enforced
// by TestCatalogueMatchesOpenAPIDocument rather than by prose.
//
// Two properties the table is built to hold:
//
//   - A name pins its PromQL *including the window*. Two clients asking for
//     `aggregate.execution_time` must get the same series, so `[5m]` is part of
//     the entry and not a caller argument; a client that wants a different
//     window asks for a different name (or runs in proxy mode, where any string
//     is legal because Prometheus evaluates it).
//   - Every metric name carries R9.2's `pgsi_` prefix. TestCatalogueIsWellFormed
//     asserts it, because a catalogue entry that reads `pg_stat_statements_*`
//     would put an un-prefixed family on the wire through the API after R9.2
//     spent a requirement preventing exactly that collision.
//
// PROVISIONAL, for the same reason and with the same caveat as the R9.11 rule
// set: Phase 2's MetricHub does not exist yet, so most of these metric names are
// ours rather than implemented. Where a name is already committed
// (chart/templates/prometheusrule.yaml, R9.11) it is reused verbatim — the
// rules and the catalogue cannot then disagree about what the exporter will
// call execution time. Where R9.4/R9.5/R5.2 name a family with no committed
// name, the name is derived from R9.2's prefix plus postgres_exporter's
// vocabulary, and Phase 2 implements to this table rather than the other way
// round. Renaming one after Phase 2 lands is a rename of a public API name, so
// it is worth getting right now while the only consumer is a document.

package api

import (
	"regexp"
	"sort"
	"strings"
)

// Catalogue families, named after the R9 requirements that define them.
const (
	// FamilyAggregate is R9.4's aggregate family: execution time, rows, shared
	// block hits/reads per (cluster, db, user), bounded cardinality, always on.
	FamilyAggregate = "aggregate"
	// FamilyPerQuery is R9.5's per-query family, top-N only (default N=50 per
	// cluster), labelled by `fingerprint` per R9.5b with `queryid` secondary.
	FamilyPerQuery = "perquery"
	// FamilySystem is R9.7's system family: the full R5.2 DB metric set plus the
	// parsed cnpg_* values re-exported under pgsi_.
	FamilySystem = "system"
)

// The two R8.3 endpoints, as the catalogue keys them. These are the URL paths so
// there is no third name for the same thing.
const (
	// QueryMetricsPath is R8.3's GET|POST /api/v1/metrics/query.
	QueryMetricsPath = "/api/v1/metrics/query"
	// SystemMetricsPath is R8.3's GET|POST /api/v1/metrics/system.
	SystemMetricsPath = "/api/v1/metrics/system"
)

// Prometheus result types. Our named queries are all `vector` (instant) or
// `matrix` (range); `scalar` and `string` exist in Prometheus's vocabulary and
// pass through proxy mode untouched, but no named query produces one — a
// catalogue entry that did would need the response type widened (see
// PromQLResult's doc).
const (
	ResultTypeVector = "vector"
	ResultTypeMatrix = "matrix"
)

// CatalogueEntry is one named query.
type CatalogueEntry struct {
	// Name is the addressable name a client asks for, `<family>.<metric>`.
	// It is the API-visible identifier, so it is stable across renames of the
	// PromQL behind it: changing the window or the aggregation is a new query
	// behind the same name only while the semantics hold, and a rename of the
	// name itself is a breaking API change.
	Name string
	// Family is one of FamilyAggregate / FamilyPerQuery / FamilySystem.
	Family string
	// Metric is the underlying pgsi_ metric the query reads. Empty for the three
	// derived entries whose PromQL combines two metrics
	// (aggregate.cache_hit_ratio, aggregate.io_time, and R6.2's
	// system.pgbouncer_pools_client_server_ratio); the names allowed to be empty
	// are listed in one place, TestCatalogueIsWellFormed's derivedNoMetric, so
	// another derived entry has to be named there to exist.
	Metric string
	// ResultType is ResultTypeVector or ResultTypeMatrix.
	ResultType string
	// Query is the exact PromQL behind the name, window included.
	Query string
	// Endpoint is which of R8.3's two routes answers this entry.
	Endpoint string
	// Description is the one-line contract text mirrored into the OpenAPI
	// document's x-promql-catalogue.
	Description string
	// Implemented reports whether the metric already exists in the committed
	// surface (the R9.11 rule set). false means the name is provisional and
	// Phase 2's MetricHub must adopt it for this entry to answer anything.
	Implemented bool
}

// DefaultCatalogue is the fixed catalogue R8.3 requires: R9.4's aggregate family
// and R9.5's per-query rollups behind /api/v1/metrics/query, and R9.7's system
// family behind /api/v1/metrics/system.
//
// The table is a function rather than a package var so a test can build a
// mutated copy (a duplicate name, a bad metric) without racing the one the
// handlers use.
func DefaultCatalogue() *Catalogue {
	// R9.4's five aggregate metrics, named once and reused by R9.5's per-query
	// variants so the two families cannot disagree about what "execution time"
	// means. R9.4's list is execution time, rows, shared block hits/reads;
	// lock_time is not in R9.4's sentence but is in the committed rule set
	// (PGSILockWaitDominant) and in Cloud SQL's aggregate equivalent, so it is
	// here with its provenance stated rather than silently added.
	aggregates := []struct{ name, metric string }{
		{"execution_time", "pgsi_query_execution_time_seconds_total"},
		{"rows", "pgsi_query_rows_total"},
		{"shared_blocks_hit", "pgsi_query_shared_blocks_hit_total"},
		{"shared_blocks_read", "pgsi_query_shared_blocks_read_total"},
		{"lock_time", "pgsi_query_lock_time_seconds_total"},
	}

	entries := make([]CatalogueEntry, 0, len(aggregates))
	for _, a := range aggregates {
		entries = append(entries, CatalogueEntry{
			Name:   FamilyAggregate + "." + a.name,
			Family: FamilyAggregate,
			Metric: a.metric,
			// R9.4's aggregate is per (cluster, db, user), so the `by` clause
			// carries namespace/cluster/datname/usename and deliberately drops
			// pod: an aggregate over a cluster's instances has no single
			// instance_role, which is why R9.3's pod label belongs to the
			// non-aggregated families and not to this query.
			Query:       "sum by (namespace, cluster, datname, usename) (rate(" + a.metric + "[5m]))",
			ResultType:  ResultTypeVector,
			Endpoint:    QueryMetricsPath,
			Description: "R9.4 aggregate: " + strings.ReplaceAll(a.name, "_", " ") + " per (cluster, db, user), 5m rate.",
			// All five are exported by the collector now. The loop used to carry
			// one exception — `a.metric != "pgsi_query_rows_total"` — written
			// while that family was a name with nothing behind it, and the
			// exception is gone because the name now has one:
			// collector.ExportedMetricNames() lists query_rows_total, which is
			// what the field's doc means by "exists in the committed surface".
			Implemented: true,
		})
	}

	for _, a := range aggregates {
		entries = append(entries, CatalogueEntry{
			Name:   FamilyPerQuery + "." + a.name,
			Family: FamilyPerQuery,
			Metric: a.metric,
			// R9.5's family is top-N per cluster, so the query pins the same N
			// the exporter exports (metrics.topN default 50) — asking for the
			// top 50 series of a family that only exports 50 per cluster is the
			// only reading where the number means the same thing on both sides.
			// R9.5b: fingerprint is the identifier label, queryid secondary and
			// informational, because PG18's jumbling change forks queryid.
			Query:       "topk(50, sum by (namespace, cluster, pod, instance_role, fingerprint, queryid) (rate(" + a.metric + "[5m])))",
			ResultType:  ResultTypeVector,
			Endpoint:    QueryMetricsPath,
			Description: "R9.5 per-query (top-N=50 per cluster): " + strings.ReplaceAll(a.name, "_", " ") + ", labelled by fingerprint (R9.5b).",
			// The same five metrics as the aggregate family, so the same answer
			// and the same note: all five are in the collector's table.
			Implemented: true,
		})
	}

	// provisionalSystem names the system metrics with no family behind them yet,
	// and so the only system entries this catalogue marks Implemented: false.
	//
	// It is a list of exceptions rather than a bool on each of the 25 rows
	// because the claim being made is "this name has no family", and the smallest
	// form of that claim is the list of names that lack one — the same reason the
	// aggregates loop above wrote its one exception inline. The list is verified
	// against collector.ExportedMetricNames() rather than against R5.2's prose,
	// and it is the half of the answer the prose cannot give: R5.2 names 25
	// families, the collector's naming table exports 20 of them by name, and
	// these five are the difference.
	//
	// Where a name's absence has a reason in the file itself, it is here:
	// db_tps is a rate Cloud SQL synthesises from xact_commit + xact_rollback
	// (its own Description says so) and both counters are exported while the
	// derived rate is not a family; the three cnpg_* rows exist on the pod as
	// cnpg_* but not yet under the pgsi_ prefix R9.7's re-export requires.
	// wal_size_bytes is named by R5.2 and absent from the table with no further
	// story worth inventing. Two rows that used to be here are gone rather than
	// edited: 49b3210 shipped the pg_stat_activity collector, whose naming rows
	// mint db_backends_by_state and db_backends_by_application_name, so both
	// families now exist and their lines were deleted — which is what the
	// paragraph below prescribes, and the reason the map is a list of exceptions
	// instead of a bool that gets flipped in place.
	//
	// When Phase 2 ships one of these, the fix is to delete its line: the entry
	// flips to implemented and no other entry changes, which is D13's bounded
	// blast radius applied to the catalogue instead of to the rule set.
	provisionalSystem := map[string]bool{
		"pgsi_db_tps":                     true,
		"pgsi_wal_size_bytes":             true,
		"pgsi_pg_status":                  true,
		"pgsi_pg_is_in_recovery":          true,
		"pgsi_pg_replication_lag_seconds": true,
	}

	// R9.7's system family = the full R5.2 set. R5.2 names the families; the
	// names below are derived from R9.2's prefix plus postgres_exporter's
	// vocabulary, and are provisional until Phase 2's MetricHub ships them.
	//
	// pgsi_db_backends_by_wait_event is not a row here because A3 ruled the
	// R4.3 rollup not a metric family (S50, implemented by A1's 9ff60c6 as
	// naming.go's TypeNotExported row): the name is no longer minted at all, so
	// there is nothing here to withhold and no row to add later. The prose that
	// used to sit here — "documented by its naming row and not published as a
	// system family", pending the ruling — described an exported-but-undocumented
	// name, which is the artefact S50 filed and 9ff60c6 deleted; the sentence
	// "adding the row later costs one entry" went with it.
	systems := []struct{ name, metric, desc string }{
		{"xact_commit", "pgsi_db_xact_commit_total", "R5.2 transactions: committed transactions per database."},
		{"xact_rollback", "pgsi_db_xact_rollback_total", "R5.2 transactions: rolled-back transactions per database."},
		{"tps", "pgsi_db_tps", "R5.2 transactions: transactions per second, the commit+rollback rate Cloud SQL surfaces as TPS."},
		{"tuples_returned", "pgsi_db_tuples_returned_total", "R5.2 tuples: rows returned."},
		{"tuples_fetched", "pgsi_db_tuples_fetched_total", "R5.2 tuples: rows fetched."},
		{"tuples_inserted", "pgsi_db_tuples_inserted_total", "R5.2 tuples: rows inserted."},
		{"tuples_updated", "pgsi_db_tuples_updated_total", "R5.2 tuples: rows updated."},
		{"tuples_deleted", "pgsi_db_tuples_deleted_total", "R5.2 tuples: rows deleted."},
		{"blks_hit", "pgsi_db_blks_hit_total", "R5.2 blocks: heap blocks served from cache."},
		{"blks_read", "pgsi_db_blks_read_total", "R5.2 blocks: heap blocks read from disk."},
		{"temp_files", "pgsi_db_temp_files_total", "R5.2 temp: temp files created."},
		{"temp_bytes", "pgsi_db_temp_bytes_total", "R5.2 temp: bytes written to temp files."},
		{"deadlocks", "pgsi_db_deadlocks_total", "R5.2 deadlocks: deadlocks detected per database."},
		{"backends_by_state", "pgsi_db_backends_by_state", "R5.2 backends: backend count grouped by pg_stat_activity state."},
		{"backends_by_application_name", "pgsi_db_backends_by_application_name", "R5.2 backends: backend count grouped by application_name."},
		{"database_size_bytes", "pgsi_db_size_bytes", "R5.2 database sizes: pg_database_size per database."},
		{"txid_age", "pgsi_db_txid_age", "R5.2/R7.5: oldest transaction id age per database (300M warn / 1.5B critical bands are R7.5's)."},
		{"mxid_age", "pgsi_db_mxid_age", "R5.2/R7.5: multixact id age per database."},
		{"wal_size_bytes", "pgsi_wal_size_bytes", "R5.2 WAL: on-disk WAL volume."},
		{"wal_generated_bytes", "pgsi_wal_generated_bytes_total", "R5.2 WAL: bytes generated (rate gives the generation rate)."},
		{"checkpoints_timed", "pgsi_checkpoints_timed_total", "R5.2 checkpoints: scheduled checkpoints."},
		{"checkpoints_requested", "pgsi_checkpoints_requested_total", "R5.2 checkpoints: requested (non-scheduled) checkpoints."},
		// R9.7's second half: the parsed cnpg_* values re-exported under pgsi_
		// "so a user without CNPG's PodMonitor still gets one consistent
		// namespace" (R9.7). These three are the cnpg values the API already
		// consumes through discovery.
		{"cnpg_pg_status", "pgsi_pg_status", "R9.7: cnpg_pg_status re-exported under pgsi_ (was cnpg_pg_status)."},
		{"cnpg_pg_is_in_recovery", "pgsi_pg_is_in_recovery", "R9.7: cnpg_pg_is_in_recovery re-exported under pgsi_; the role R9.3 labels as instance_role."},
		{"cnpg_pg_replication_lag_seconds", "pgsi_pg_replication_lag_seconds", "R9.7: cnpg_pg_replication_lag re-exported under pgsi_."},
	}
	for _, s := range systems {
		entries = append(entries, CatalogueEntry{
			Name:        FamilySystem + "." + s.name,
			Family:      FamilySystem,
			Metric:      s.metric,
			Query:       "sum by (namespace, cluster, pod, instance_role, datname) (" + s.metric + ")",
			ResultType:  ResultTypeVector,
			Endpoint:    SystemMetricsPath,
			Description: s.desc,
			// Everything but the seven names in provisionalSystem is exported by
			// the collector now, so the loop's answer is the exception list, not
			// the blanket false it carried while none of the set existed.
			Implemented: !provisionalSystem[s.metric],
		})
	}

	// R6.3's pooler throughput set: the pgbouncer stats families, on the same
	// /metrics/system route as the R5.2 set. R9.7's second half is "the parsed
	// cnpg_* values re-exported under pgsi_", and cnpg_pgbouncer_* is the largest
	// such family — 123 series on the fixture pooler pod.
	//
	// It is a second table rather than rows appended to `systems` above because
	// that loop hardcodes its `by` clause as `(..., datname)` and a pgbouncer
	// series has no `datname`. Measured on testdata/cnpg-metrics/ai-api-gateway__
	// rev-proxy-pg-pooler-86b4bdd84d-n9vlv__9127.txt: all 20
	// cnpg_pgbouncer_stats_* families carry {database} alone, and the 48
	// {database,user} series in that file are the 16 cnpg_pgbouncer_pools_*
	// families, three series each. A row in the first table would aggregate over a
	// label the series does not have; the pools_* families R6.2 renders are a
	// third table's problem, not this one's.
	//
	// Which fifteen: R6.3 names five totals explicitly (total_query_count,
	// total_xact_count, total_received, total_sent, total_wait_time) and then
	// writes "avg_* variants" as a wildcard, so this table is those five totals
	// plus all ten avg_* families in the naming table. The five totals R6.3 does
	// not name (bind_count, client_parse_count, query_time, server_parse_count,
	// xact_time) are absent — its totals are a list, not a wildcard, and minting
	// the other five would put names on the API surface the spec never asked for.
	// The asymmetry that follows, ten averages beside five totals, is the spec's
	// shape, not a gap here.
	//
	// The pair's names are not symmetric either: R6.3's `total_received` is
	// naming.go:625's `pgbouncer_stats_total_received`, whose avg counterpart is
	// naming.go:615's `pgbouncer_stats_avg_recv` — `avg_recv`, not `avg_received`.
	// Each side of the pair takes the name the naming table has, which is the only
	// place either name exists.
	//
	// rate() versus the raw gauge: naming.go:577-578 rules that "R6.3's
	// counter-reset language for the stats_* families is a query-side concern, not
	// a redeclaration of the type" — the table types every stats_* family
	// TypeGauge (naming.go:621-630), following the exporter's declared TYPE, and
	// the R3.3 handling R6.3 asks for lives in the query instead. rate() is that
	// instrument: Prometheus's rate treats a decrease as a counter reset, which is
	// the whole of the handling available to a query, and pooler_test.go:380-381
	// records why there is nothing further to surface — "StatsReset stays zero: no
	// cnpg_pgbouncer_*reset* family exists, so S5 has no counter epoch to report",
	// so the stats_reset timestamp R3.3 names has no family behind it on the
	// pooler side. The avg_* rows are averages by definition, so they get avg
	// by(...) and no rate(): rate() of an average is a rate of a rate, and sum
	// by(...) over two pooler pods' averages doubles a per-pod number.
	//
	// All fifteen are Implemented: true because all fifteen metrics are in the
	// collector's naming table (internal/collector/naming.go:701-720). This table carries no
	// provisional list of its own because there is nothing provisional in it — and
	// internal/collector/naming_catalogue_test.go's biconditional fails the build
	// if that claim is wrong, which is what lets the field be a constant here.
	pgbouncer := []struct {
		name    string
		metric  string
		counter bool
		desc    string
	}{
		{"pgbouncer_stats_total_query_count", "pgsi_pgbouncer_stats_total_query_count", true,
			"R6.3 throughput: total SQL queries pooled by pgbouncer (cnpg_pgbouncer_stats_total_query_count), as a 5m rate."},
		{"pgbouncer_stats_total_xact_count", "pgsi_pgbouncer_stats_total_xact_count", true,
			"R6.3 throughput: total SQL transactions pooled by pgbouncer (cnpg_pgbouncer_stats_total_xact_count), as a 5m rate."},
		{"pgbouncer_stats_total_received", "pgsi_pgbouncer_stats_total_received", true,
			"R6.3 throughput: total bytes of network traffic received by pgbouncer (cnpg_pgbouncer_stats_total_received), as a 5m rate."},
		{"pgbouncer_stats_total_sent", "pgsi_pgbouncer_stats_total_sent", true,
			"R6.3 throughput: total bytes of network traffic sent by pgbouncer (cnpg_pgbouncer_stats_total_sent), as a 5m rate."},
		{"pgbouncer_stats_total_wait_time", "pgsi_pgbouncer_stats_total_wait_time", true,
			"R6.3 throughput: microseconds clients spent waiting for a server (cnpg_pgbouncer_stats_total_wait_time), as a 5m rate."},
		{"pgbouncer_stats_avg_query_count", "pgsi_pgbouncer_stats_avg_query_count", false,
			"R6.3 throughput: average queries per second in the last stat period (cnpg_pgbouncer_stats_avg_query_count)."},
		{"pgbouncer_stats_avg_xact_count", "pgsi_pgbouncer_stats_avg_xact_count", false,
			"R6.3 throughput: average transactions per second in the last stat period (cnpg_pgbouncer_stats_avg_xact_count)."},
		{"pgbouncer_stats_avg_recv", "pgsi_pgbouncer_stats_avg_recv", false,
			"R6.3 throughput: average bytes per second received from clients (cnpg_pgbouncer_stats_avg_recv)."},
		{"pgbouncer_stats_avg_sent", "pgsi_pgbouncer_stats_avg_sent", false,
			"R6.3 throughput: average bytes per second sent to clients (cnpg_pgbouncer_stats_avg_sent)."},
		{"pgbouncer_stats_avg_wait_time", "pgsi_pgbouncer_stats_avg_wait_time", false,
			"R6.3 throughput: microseconds per second clients spend waiting for a server (cnpg_pgbouncer_stats_avg_wait_time)."},
		{"pgbouncer_stats_avg_bind_count", "pgsi_pgbouncer_stats_avg_bind_count", false,
			"R6.3 throughput: average prepared statements readied for execution and forwarded to PostgreSQL (cnpg_pgbouncer_stats_avg_bind_count)."},
		{"pgbouncer_stats_avg_client_parse_count", "pgsi_pgbouncer_stats_avg_client_parse_count", false,
			"R6.3 throughput: average prepared statements created by clients (cnpg_pgbouncer_stats_avg_client_parse_count)."},
		{"pgbouncer_stats_avg_query_time", "pgsi_pgbouncer_stats_avg_query_time", false,
			"R6.3 throughput: average query duration in microseconds (cnpg_pgbouncer_stats_avg_query_time)."},
		{"pgbouncer_stats_avg_server_parse_count", "pgsi_pgbouncer_stats_avg_server_parse_count", false,
			"R6.3 throughput: average prepared statements created by pgbouncer on a server (cnpg_pgbouncer_stats_avg_server_parse_count)."},
		{"pgbouncer_stats_avg_xact_time", "pgsi_pgbouncer_stats_avg_xact_time", false,
			"R6.3 throughput: average transaction duration in microseconds (cnpg_pgbouncer_stats_avg_xact_time)."},
	}
	for _, pb := range pgbouncer {
		// The by clause is the pgbouncer label set measured above — the identity
		// labels ApplyIdentityLabels stamps (pooler_test.go:390-392 names them)
		// plus `database`, the only label the stats families carry. `pooler` is
		// not here because PoolerConfig.PoolerName defaults to nil and pooler.go
		// omits the label for S5 (its field comment lists the three candidates
		// and ships the one that guesses nothing), so `pod` is the pooler's
		// identity on the wire and grouping on an absent label would add a
		// dimension with one value.
		query := "avg by (namespace, cluster, pod, instance_role, database) (" + pb.metric + ")"
		if pb.counter {
			query = "sum by (namespace, cluster, pod, instance_role, database) (rate(" + pb.metric + "[5m]))"
		}
		entries = append(entries, CatalogueEntry{
			Name:        FamilySystem + "." + pb.name,
			Family:      FamilySystem,
			Metric:      pb.metric,
			Query:       query,
			ResultType:  ResultTypeVector,
			Endpoint:    SystemMetricsPath,
			Description: pb.desc,
			Implemented: true,
		})
	}

	// R6.2's pool-utilisation set: the two pools_* families its clause names, its
	// client/server ratio, and its saturation predicate.
	//
	// This is the third table the one above defers: its comment says "the pools_*
	// families R6.2 renders are a third table's problem, not this one's", and the
	// reason is the by clause. The first table groups on `(..., datname)`, which a
	// pgbouncer series does not have; the second groups on `(..., database)`,
	// which pools_* series have but which is only half their label set -- the same
	// comment measures "the 48 {database,user} series in that file are the 16
	// cnpg_pgbouncer_pools_* families, three series each". So this table's clause
	// is the identity labels plus `database, user`: the pair R6.2's own words use
	// ("per (database, user)") and the pair the series carry, which neither of the
	// two existing clauses can express.
	//
	// Which two of the sixteen: R6.2's clause names exactly two metrics --
	// "clients waiting vs. servers active" -- and builds its other two renderings
	// from those two rather than from more rows. The other fourteen (cl_active,
	// sv_idle, sv_used, sv_tested, sv_login, cl_cancel_req, cl_active_cancel_req,
	// cl_waiting_cancel_req, load_balance_hosts, maxwait, maxwait_us, pool_mode,
	// sv_active_cancel, sv_wait_cancels) are R6.1's discovery list, and poolers.go
	// renders them through PoolerView; the metrics surface gets rows only for what
	// R6.2 asks to render, which is the same rule that left five stats_* totals out
	// of the table above.
	pools := []struct {
		name   string
		metric string
		desc   string
	}{
		{"pgbouncer_pools_cl_waiting", "pgsi_pgbouncer_pools_cl_waiting",
			"R6.2 pool utilisation: clients waiting for a server connection, per (database, user) (cnpg_pgbouncer_pools_cl_waiting)."},
		{"pgbouncer_pools_sv_active", "pgsi_pgbouncer_pools_sv_active",
			"R6.2 pool utilisation: server connections in use, per (database, user) (cnpg_pgbouncer_pools_sv_active)."},
	}
	for _, p := range pools {
		entries = append(entries, CatalogueEntry{
			Name:   FamilySystem + "." + p.name,
			Family: FamilySystem,
			Metric: p.metric,
			// sum, not avg: these are counts per pool, so two pooler pods
			// serving one (database, user) have six waiting clients between
			// them, not three averaged. The stats_* table above uses avg for
			// its non-counter rows because those rows are averages by
			// definition; a pool count is not, and averaging it is the answer
			// that depends on how many pods happen to be running.
			Query:       "sum by (namespace, cluster, pod, instance_role, database, user) (" + p.metric + ")",
			ResultType:  ResultTypeVector,
			Endpoint:    SystemMetricsPath,
			Description: p.desc,
			// Both metrics are in the naming table (naming.go:685-700 mints all
			// sixteen pools_* families) and naming_catalogue_test.go tests that
			// table against the parsed exposition biconditionally, so the
			// constant is as checked here as it is in the table above.
			Implemented: true,
		})
	}

	// The client/server ratio: R6.2's second rendering, and the third member of
	// the empty-Metric set. A3's ruling at requirements.md:357 answers where it
	// lives -- "a derived catalogue entry, the `cache_hit_ratio` pattern" -- and
	// rejects the two alternatives on the record: not a minted family, because
	// naming.go:685-700's sixteen pools_* families are tested biconditionally
	// against the parsed exposition and a ratio no exporter emits cannot join that
	// table; not a DTO field, because poolers.go's own header says "PoolerView has
	// no field for any of them ... so they arrive through the metrics surface". It
	// also rejects `cl_active / sv_active`, the pgbouncer clients-per-server
	// convention, because that needs a series the clause does not name.
	entries = append(entries, CatalogueEntry{
		Name:   FamilySystem + ".pgbouncer_pools_client_server_ratio",
		Family: FamilySystem,
		Metric: "",
		// The ruling's expression is `pgsi_pgbouncer_pools_cl_waiting /
		// pgsi_pgbouncer_pools_sv_active`, "the two series the row itself names,
		// in the order it names them". clamp_min on the denominator is the
		// cache_hit_ratio precedent below, and for the same reason: a pool with no
		// active server and no waiting client is otherwise 0/0, which Prometheus
		// drops instead of answering, where 0/1 answers 0 -- the correct answer,
		// and the finite one a chart needs. The by clause repeats on both sides
		// because a vector division matches series by label set, so the two sides
		// have to carry the same one (io_time below has the same shape).
		Query: "sum by (namespace, cluster, pod, instance_role, database, user) (pgsi_pgbouncer_pools_cl_waiting)" +
			" / clamp_min(sum by (namespace, cluster, pod, instance_role, database, user) (pgsi_pgbouncer_pools_sv_active), 1)",
		ResultType:  ResultTypeVector,
		Endpoint:    SystemMetricsPath,
		Description: "R6.2 client/server ratio, derived from the two pools_* families the row names (clients waiting over servers active, per (database, user)); no metric of its own.",
		Implemented: true,
	})

	// Pool saturation: R6.2's headline number and the ruling's third rendering.
	// It is its own entry rather than a second reading of the cl_waiting row above
	// because the name is what a client asks for and the PromQL answers differ --
	// one is a count, this is a predicate over a window -- the same reason
	// cache_hit_ratio is a name of its own rather than an alias for the hit
	// counter.
	entries = append(entries, CatalogueEntry{
		Name:   FamilySystem + ".pgbouncer_pools_saturation",
		Family: FamilySystem,
		Metric: "pgsi_pgbouncer_pools_cl_waiting",
		// The ruling's expression verbatim: `min_over_time(pgsi_pgbouncer_pools_cl_waiting[5m]) > 0`,
		// "5m being the window the catalogue uses throughout". The table's by
		// clause cannot wrap it the way it wraps the two rows above -- a range
		// function is per-series, so grouping it would mean
		// `min by (...) (min_over_time(...))`, which is not the expression A3
		// ruled -- so the ruled text wins and the ungrouped form is used.
		Query:       "min_over_time(pgsi_pgbouncer_pools_cl_waiting[5m]) > 0",
		ResultType:  ResultTypeVector,
		Endpoint:    SystemMetricsPath,
		Description: "R6.2 pool saturation: a pool whose minimum cl_waiting over 5m is above zero had clients waiting for the whole window (the ruling's min_over_time expression).",
		Implemented: true,
	})

	// The two derived entries: members of R9.4's list that are expressions over
	// other metrics, so they have no single Metric to name. With R6.2's ratio
	// above they are the set TestCatalogueIsWellFormed allows an empty Metric for,
	// which is the R9.4 cell's stated cost for the second of them ("one catalogue
	// entry plus one predicate") -- the predicate is that set, and another derived
	// member has to be named there to exist.
	//
	// cache_hit_ratio: R5.2 names "cache hit ratio" as a *ratio*, not a gauge.
	// The formula is the one the committed rule set already uses
	// (PGSICacheHitRatioLow), so the rule and the API answer with the same
	// arithmetic.
	entries = append(entries, CatalogueEntry{
		Name:        FamilyAggregate + ".cache_hit_ratio",
		Family:      FamilyAggregate,
		Metric:      "",
		Query:       "sum(rate(pgsi_query_shared_blocks_hit_total[5m])) / clamp_min(sum(rate(pgsi_query_shared_blocks_hit_total[5m]) + rate(pgsi_query_shared_blocks_read_total[5m])), 1)",
		ResultType:  ResultTypeVector,
		Endpoint:    QueryMetricsPath,
		Description: "R5.2/R9.4 cache hit ratio, derived from the hit and read counters (same formula as the PGSICacheHitRatioLow rule); no metric of its own.",
		Implemented: true,
	})
	// io_time: the second amendment to R9.4's frozen member list, ruled in
	// requirements.md (5668cb3) by the same mechanism that admitted lock_time --
	// "a post-0.3.0 scope change to a frozen requirement, added by ruling rather
	// than by drift". R10.5(a) plots "total execution time and IO time, from
	// S2/R9.4" and the cell had no IO member to serve it.
	//
	// It is the *sum* of the two block-time counters, which is the ruling's
	// explicit choice over a read-only member: R3.2's Load by IO wait column
	// (dto/queries.go:77-78) is blk_read_time + blk_write_time on PG 15/16 and
	// shared_blk_read_time + shared_blk_write_time on PG 17+, and the Query
	// Detail page already renders that scalar for every fingerprint -- a
	// read-only member would draw a chart band that is not the number the detail
	// page shows for the same query. The two metrics summed are the pair
	// naming.go:135-136 mints, so the aggregate family now documents a value the
	// collector has exported since Phase 2 rather than the reverse.
	entries = append(entries, CatalogueEntry{
		Name:   FamilyAggregate + ".io_time",
		Family: FamilyAggregate,
		Metric: "",
		Query: "sum by (namespace, cluster, datname, usename) (rate(pgsi_query_shared_blk_read_time_seconds_total[5m]))" +
			" + sum by (namespace, cluster, datname, usename) (rate(pgsi_query_shared_blk_write_time_seconds_total[5m]))",
		ResultType:  ResultTypeVector,
		Endpoint:    QueryMetricsPath,
		Description: "R9.4 io_time, derived from the two block-time counters (the value R3.2's Load by IO wait column computes); no metric of its own.",
		Implemented: true,
	})

	return &Catalogue{entries: entries}
}

// Catalogue is the named-query table for both R8.3 endpoints.
type Catalogue struct {
	entries []CatalogueEntry
}

// Entries returns every entry, sorted by name so a caller (the OpenAPI doc, a
// UI picker, a test) gets a deterministic order from a map-free table.
func (c *Catalogue) Entries() []CatalogueEntry {
	if c == nil {
		return nil
	}
	out := make([]CatalogueEntry, len(c.entries))
	copy(out, c.entries)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// For returns the entries behind one endpoint (QueryMetricsPath or
// SystemMetricsPath). The two catalogues are disjoint: a query-metrics name
// asked of /metrics/system is not found, which is what makes the endpoints mean
// something rather than being two aliases.
func (c *Catalogue) For(endpoint string) []CatalogueEntry {
	var out []CatalogueEntry
	for _, e := range c.Entries() {
		if e.Endpoint == endpoint {
			out = append(out, e)
		}
	}
	return out
}

// Lookup resolves what a client asked for. R8.3's request carries a PromQL
// *string*, so a name is not the only legal answer: Lookup accepts the
// registered name (case-insensitive) or the exact PromQL text (whitespace
// normalised), which is what lets a client that already has the query text from
// the catalogue — or from a dashboard built on it — send it back unchanged.
//
// It deliberately does not try to *interpret* PromQL. "SELECT"-style matching on
// an arbitrary string is a parser, and R8.3's whole point is that we ship no
// evaluator: either the string is byte-equal to a catalogued query (modulo
// whitespace) or it is not in the catalogue.
func (c *Catalogue) Lookup(q string) (CatalogueEntry, bool) {
	if c == nil {
		return CatalogueEntry{}, false
	}
	return matchEntry(c.entries, q)
}

// LookupOn is Lookup restricted to one endpoint's entries — and it is the form
// the handlers use, because R8.3's two routes differ by *which half* of the
// catalogue each answers. A global Lookup on /metrics/query would happily serve
// system.tps (the table has it), and the second route would then exist in the
// document and nowhere else. Ask the query route for a system name and the
// answer is 404 PromQLUnavailable naming the route that does answer it.
func (c *Catalogue) LookupOn(endpoint, q string) (CatalogueEntry, bool) {
	if c == nil {
		return CatalogueEntry{}, false
	}
	return matchEntry(c.For(endpoint), q)
}

// matchEntry is the shared matcher behind both lookups: a query string is a
// catalogue name (case-insensitive — the name is a label, not a PromQL string)
// or the exact PromQL behind one (whitespace-normalised, so a query reflowed
// across four lines in a dashboard still matches; case-sensitive, because
// PromQL metric and label names are).
func matchEntry(entries []CatalogueEntry, q string) (CatalogueEntry, bool) {
	want := normalizePromQL(q)
	if want == "" {
		return CatalogueEntry{}, false
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, strings.TrimSpace(q)) || normalizePromQL(e.Query) == want {
			return e, true
		}
	}
	return CatalogueEntry{}, false
}

// normalizePromQL collapses runs of whitespace to a single space so a query
// written across four lines in a dashboard matches the one-line form in the
// table. It is whitespace only: no case folding (PromQL label values are
// case-sensitive), no reordering of selectors.
func normalizePromQL(q string) string {
	return strings.Join(strings.Fields(q), " ")
}

var metricNameRE = regexp.MustCompile(`^pgsi_[a-z0-9_]+$`)

// MetricNames returns the distinct metric names the catalogue reads, sorted.
// Exported for the OpenAPI/CI consistency check and for Phase 2's MetricHub:
// this is the list of families the API promises to answer, so it is the list the
// exporter has to produce.
func (c *Catalogue) MetricNames() []string {
	entries := c.Entries()
	seen := map[string]bool{}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Metric == "" || seen[e.Metric] {
			continue
		}
		seen[e.Metric] = true
		out = append(out, e.Metric)
	}
	sort.Strings(out)
	return out
}
