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
	// Metric is the underlying pgsi_ metric the query reads. Empty for derived
	// entries whose PromQL combines two metrics (cache_hit_ratio).
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

	var entries []CatalogueEntry
	for _, a := range aggregates {
		implemented := a.metric != "pgsi_query_rows_total"
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
			Implemented: implemented,
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
			Implemented: a.metric != "pgsi_query_rows_total",
		})
	}

	// R9.7's system family = the full R5.2 set. R5.2 names the families; the
	// names below are derived from R9.2's prefix plus postgres_exporter's
	// vocabulary, and are provisional until Phase 2's MetricHub ships them.
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
		{"txid_age", "pgsi_db_txid_age", "R5.2/R7.5: oldest transaction id age per database (1.5B warn / 1B critical bands are R7.5's)."},
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
			// None of the R5.2 set is exported yet (Phase 2), so every system
			// entry is provisional — including the cnpg re-exports, which exist
			// as cnpg_* but not yet under the pgsi_ prefix R9.7 requires.
			Implemented: false,
		})
	}

	// The one derived entry: R5.2 names "cache hit ratio" as a *ratio*, not a
	// gauge, so it has no single Metric. The formula is the one the committed
	// rule set already uses (PGSICacheHitRatioLow), so the rule and the API
	// answer with the same arithmetic.
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
	seen := map[string]bool{}
	var out []string
	for _, e := range c.Entries() {
		if e.Metric == "" || seen[e.Metric] {
			continue
		}
		seen[e.Metric] = true
		out = append(out, e.Metric)
	}
	sort.Strings(out)
	return out
}
