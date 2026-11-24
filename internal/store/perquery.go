// perquery.go is R9.5's per-query metrics family: the exposition body for the
// four members R9.5's S25 ruling names, rendered from the rows the store keeps.
//
// This is the half of IMPLEMENTATION.md §5-D18 its row calls "the code half is
// the family body" — the half that stayed open after `76b0f56` shipped the
// collector, because the collector's `samplesFor` returns []Sample into the sink
// and nothing turned stored rows into the text /metrics serves.
//
// # Which source the family reads
//
// The rule, written down because R9.5's family now has a second candidate
// producer and requirements.md states no rule picking one: **the exposition
// renders from `query_source`, and `query_rollup` is not a candidate at all.**
// The collector's `samplesFor` (internal/collector/statements.go:694) is the
// producer of the *rows* — sample → sink → `query_source` — and this file is the
// producer of the *metrics text*. There is no "store now, collector later"
// handover to record, because there is only one path from a pg_stat_statements
// row to a Prometheus line: the collector's sample is persisted, and this render
// reads what was persisted. When the registry registers statements.go (the
// remaining blocker in §3.11's R9.5 cell) the rows arrive live and this file
// changes not one line; until then it answers from whatever the store holds.
//
// Why the rollup is excluded is a Prometheus rule rather than a preference. The
// family's members are `_total` counters, and a counter must not decrease except
// at a reset. `query_rollup` holds per-minute deltas with a retention window
// (24 h for a memory store, 7 d for a file one — §3.4:222), so a sum over the
// rollup *falls* as its oldest buckets age out: a query that stopped running
// would export a counter that shrinks, which every reader interprets as a
// counter reset and rate() flattens to zero. `query_source` holds the cumulative
// values pg_stat_statements itself reports since `stats_reset` — the exact number
// `samplesFor` exported — so the metric and the store's own answer to R3.3 are
// one column rather than two numbers that have to be kept in step.
//
// # The label set, and why it is not the one topn.go uses
//
// Six labels: namespace, cluster, pod, instance_role, fingerprint, queryid. That
// is the set promql_catalogue.go's per-query entry groups by —
// `sum by (namespace, cluster, pod, instance_role, fingerprint, queryid)` — and
// the set collector/statements.go:755 sets on every sample it mints (fingerprint
// and queryid itself, the four identity labels by the scheduler's
// ApplyIdentityLabels). R9.5b:398 is the ruling for the pair that matters: "The
// per-query family's identifier label is the **fingerprint** (canonical, joins to
// the API), with `queryid` as a secondary informational label."
//
// It is deliberately not topn.go's four (fingerprint, namespace, cluster,
// datname). That projection is R8.2's QueryView, which has no pod column because
// R3.2's table has no user or pod axis; R9.3's rule is the opposite — every
// series carries namespace, cluster, pod and instance_role — and a metrics
// family that dropped pod would merge a primary's counters with a replica's.
//
// The consequence is stated rather than hidden: two `query_source` rows sharing
// one fingerprint in two databases (the same statement run in `appdb` and
// `reportdb`) render as ONE series whose counters are the sum of both. That is
// what the catalogue's `sum by (...)` does with the same two rows, and it is what
// keeps the exposition valid — two lines with identical label sets is D20's S8
// class ("an invalid exposition whose surviving value is whichever row the driver
// returned last"), and the store's PK is finer than the family's label set in
// exactly one column pair (datname, usename), so grouping is required, not
// optional.
//
// # Four members, named by the table and not by a literal
//
// R9.5's member list is closed at four by A3's S25 ruling — execution_time, rows,
// shared_blocks_hit, shared_blocks_read — and lock_time is withheld ("the four
// named members are the R3.2 per-query sources that exist per query"). The
// catalogue has a fifth per-query entry (perquery.lock_time) because its loop
// reuses R9.4's five aggregates, but `query_source` has no lock column to render
// it from — only `query_rollup.lock_wait_ms`, whose -1 sentinel says "no
// per-query source" — and collector/statements.go:636's `statementMembers`
// implements the same four. This file matches the collector's list, and takes
// each family name from collector.MetricName rather than a string literal, so
// the render, the naming table and the catalogue cannot drift apart by one
// spelling. The R9.2 prefix is applied here and never baked into the name
// (naming.go's "The prefix stays out" rule), with "" selecting
// collector.DefaultPrefix the way UpTracker.Render's prefix parameter does.
//
// # The window
//
// R9.5:397: "for the **top-N only**: default **N=50 per cluster** (see §5 —
// `metrics.topN` is per cluster, not per database), range 0–1000. N=0 disables
// the family." So the ranking partitions by (namespace, cluster) — the pair R9.3
// makes one cluster, since two namespaces may share a cluster name and one
// cluster's pods must share one budget — and orders by total_exec_ms DESC with
// fingerprint then queryid as tie-breaks, which is the rule
// collector/statements.go:723's sort applies and the one R10.7's ruling names as
// R9.5's ("naming it here stops a second implementation choosing
// alphabetically"). The range check is collector.ValidTopN, not a second range
// rule: WithTopN clamps and ValidTopN is the exported validator the config layer
// uses, and a store that invented its own bounds would be the third opinion
// about one number.
//
// N=0 and an empty store both return "" — the absence rule the hub's docblock
// states ("a family with no series is absent, not a HELP/TYPE pair beside a value
// the process never measured"), which is what `promtool check metrics` enforces.
package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// perQuerySQL is the family's row set: every series the store can export, ranked
// within its own cluster, cut to the top N.
//
// Three levels, because each one answers a different question and SQLite will not
// let any two of them share a SELECT. The innermost does the grouping the
// catalogue's PromQL does (the six labels, the four counters summed). The middle
// one ranks: ROW_NUMBER partitioned by the cluster, which is what makes N a
// per-cluster budget rather than a global one — a fleet with four clusters and
// N=50 exports 200 series, not 50, which is the reading R9.5's "per cluster"
// parenthetical exists to force. The outermost filters, because a window function
// cannot appear in the WHERE of the SELECT that computes it.
//
// The tie-break order is the collector's, and the ORDER BY of the outermost
// select is the same key rather than a lexicographic sort of the rendered line:
// the lines come out ranked, so a reader comparing this body against the API's
// top-N list reads the same order on both sides.
const perQuerySQL = `
SELECT namespace, cluster, pod, instance_role, fingerprint, queryid,
       total_exec_ms, rows_returned, shared_blks_hit, shared_blks_read
FROM (
	SELECT s.*,
	       ROW_NUMBER() OVER (
			PARTITION BY s.namespace, s.cluster
			ORDER BY s.total_exec_ms DESC, s.fingerprint ASC, s.queryid ASC
	       ) AS rn
	FROM (
		SELECT namespace, cluster, pod, instance_role, fingerprint, queryid,
		       SUM(total_exec_ms)    AS total_exec_ms,
		       SUM(rows_returned)    AS rows_returned,
		       SUM(shared_blks_hit)  AS shared_blks_hit,
		       SUM(shared_blks_read) AS shared_blks_read
		FROM query_source
		GROUP BY namespace, cluster, pod, instance_role, fingerprint, queryid
	) s
)
WHERE rn <= ?
ORDER BY namespace, cluster, rn
`

// perQuerySeries is one exported series set: the six labels the family groups by
// and the four counters it exports.
//
// It is not `identity` (store/statements.go:471) embedded, even though four of its
// six fields are members of that tuple: `identity` is (fingerprint, namespace,
// cluster, pod, datname, usename) and this series has neither datname nor usename
// and gains instance_role, which the tuple carries as a field of sourceRow rather
// than as part of the identity. Embedding the wrong tuple is worse than repeating
// four field names, because the embedding is the assertion that the two are the
// same projection and here they are not.
type perQuerySeries struct {
	namespace, cluster, pod, instanceRole, fingerprint, queryid string

	totalExecMs    float64
	rowsReturned   float64
	sharedBlksHit  float64
	sharedBlksRead float64
}

// labelString renders the series' label set in the exposition's braces.
//
// Sorted by key, and %q for the value, which is the collector's convention
// (collector/metrics.go:398's LabelString) and not an accident of style: the
// fingerprint is a hex string and the six values are all plain, but a cluster
// name with a quote in it would produce an unparseable line, and %q is the one
// place the escaping rule can live.
//
// Why a method here and not a call of the collector's, as of A2's 69d2f8d
// exporting it: the export removed half the original reason — it used to be
// unexported — and left the half that is about the signature. The collector's
// takes a map[string]string, and for a fixed six-label set a map is how a missing
// label becomes possible at runtime instead of at compile time; this one walks a
// [6][2]string built from six named struct fields, so the set cannot be short.
// The value formatter has no such argument, which is why RenderPerQuery calls
// collector.FormatFloat directly instead of keeping a second copy of it.
func (s perQuerySeries) labelString() string {
	pairs := [6][2]string{
		{"cluster", s.cluster},
		{"fingerprint", s.fingerprint},
		{"instance_role", s.instanceRole},
		{"namespace", s.namespace},
		{"pod", s.pod},
		{"queryid", s.queryid},
	}
	var b strings.Builder
	b.WriteString("{")
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%s=%q", p[0], p[1])
	}
	b.WriteString("}")
	return b.String()
}

// perQueryMember is one member of R9.5's family: the naming table's key, the
// HELP text, and the column it reads.
//
// `column` is the canonical pg_stat_statements spelling rather than the family
// name, so the name comes from collector.MetricName every time it is needed —
// the same typed entry point every collector uses, and the reason this file has
// no metric-name string in it at all.
type perQueryMember struct {
	column string
	help   string
	value  func(perQuerySeries) float64
}

// perQueryMembers is the family in the order R9.5's S25 ruling enumerates it.
//
// Four, not five: lock_time is withheld by that ruling and has no column in
// query_source, and `calls` — which naming.go:167 does have a row for, and which
// is the number the ranking is keyed on in R3.2's table — is not one of the four
// the ruling names. collector/statements.go:636 states the same two exclusions
// for the same reasons; this list is that list read from the store's side.
var perQueryMembers = []perQueryMember{
	{
		column: "total_exec_time",
		help:   "Cumulative execution time of the statement, from pg_stat_statements (R9.5 per-query family).",
		// msPerSecond is the store's own (store/series.go:116); the collector
		// has an unexported one for the same conversion. A metric named
		// execution_time_*seconds* rendered from a *_ms* column without the
		// division exports a 1.5 s query as 1500.
		value: func(s perQuerySeries) float64 { return s.totalExecMs / msPerSecond },
	},
	{
		column: "rows",
		help:   "Rows the statement returned, from pg_stat_statements (R9.5 per-query family).",
		value:  func(s perQuerySeries) float64 { return s.rowsReturned },
	},
	{
		column: "shared_blks_hit",
		help:   "Shared blocks the statement found in the cache (R9.5 per-query family).",
		value:  func(s perQuerySeries) float64 { return s.sharedBlksHit },
	},
	{
		column: "shared_blks_read",
		help:   "Shared blocks the statement read from disk (R9.5 per-query family).",
		value:  func(s perQuerySeries) float64 { return s.sharedBlksRead },
	},
}

// RenderPerQuery returns the R9.5 per-query family's exposition body for the
// store's current rows: the top N series per cluster, four members each.
//
// topN is R9.5's `metrics.topN` (default 50, range 0–1000, 0 disables). It is a
// parameter rather than a Config field because the value belongs to the
// exposition, not to the database: the collector takes it with WithTopN and the
// config layer validates it with collector.ValidTopN, and a second copy on
// Config would be a third place the same number lives. A value outside the range
// is an error rather than a clamp — the collector clamps because a constructor
// already accepted the value, and this call has no constructor to absolve it.
//
// prefix is R9.2's `metrics.prefix`; "" selects collector.DefaultPrefix, the
// same spelling UpTracker.Render uses to ask for the default without the caller
// having config to hand.
//
// The return is "" — no HELP, no TYPE — when the family has nothing to export:
// a nil *Store, N=0 (R9.5's own disable), or a store with no rows. That is the
// rule the hub's docblock states for every family it composes, and it is what
// lets the hub add this family with one line and no special case.
//
// The nil-receiver guard is what makes that rule reachable through the hub's
// field. The hub's check is on the interface — `h.perQuery != nil` — and a typed
// nil (`cfg.PerQueryMetrics = (*store.Store)(nil)`) is a non-nil interface
// holding a nil pointer, so the hub calls this method rather than skipping it and
// the dereference below would panic in the /metrics handler. Every other source
// the hub composes answers a nil receiver with the empty string
// (collector/metrics.go:198, collector/up.go:179); this one now does the same.
// The check precedes the topN range so the receiver decides before the argument
// does, which is up.go:179's order too: a nil store renders nothing for any
// topN, including one outside R9.5's range.
func (s *Store) RenderPerQuery(ctx context.Context, topN int, prefix string) (string, error) {
	if s == nil {
		return "", nil
	}
	if !collector.ValidTopN(topN) {
		return "", fmt.Errorf("store: render per-query family: topN %d outside R9.5's 0..%d", topN, collector.MaxTopN)
	}
	if prefix == "" {
		prefix = collector.DefaultPrefix
	}
	// R9.5: "N=0 disables the family." Checked after the range so 0 is a legal
	// value that produces absence rather than the error a range check would give
	// it if the range started at 1.
	if topN == 0 {
		return "", nil
	}

	series, err := s.perQuerySeriesFor(ctx, topN)
	if err != nil {
		return "", err
	}
	if len(series) == 0 {
		return "", nil
	}

	var b strings.Builder
	for _, m := range perQueryMembers {
		name := prefix + collector.MetricName(pgversion.ViewStatStatements, m.column)
		fmt.Fprintf(&b, "# HELP %s %s\n", name, m.help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, collector.TypeCounter)
		for _, ser := range series {
			fmt.Fprintf(&b, "%s%s %s\n", name, ser.labelString(), collector.FormatFloat(m.value(ser)))
		}
	}
	return b.String(), nil
}

// perQuerySeriesFor runs perQuerySQL and returns the rows it selects.
func (s *Store) perQuerySeriesFor(ctx context.Context, topN int) ([]perQuerySeries, error) {
	rows, err := s.db.QueryContext(ctx, perQuerySQL, topN)
	if err != nil {
		return nil, fmt.Errorf("store: per-query family: %w", err)
	}
	defer rows.Close()

	var out []perQuerySeries
	for rows.Next() {
		var ser perQuerySeries
		if err := rows.Scan(
			&ser.namespace, &ser.cluster, &ser.pod, &ser.instanceRole,
			&ser.fingerprint, &ser.queryid,
			&ser.totalExecMs, &ser.rowsReturned, &ser.sharedBlksHit, &ser.sharedBlksRead,
		); err != nil {
			return nil, fmt.Errorf("store: per-query family scan: %w", err)
		}
		out = append(out, ser)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: per-query family rows: %w", err)
	}
	return out, nil
}
