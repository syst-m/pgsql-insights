// topn.go is R3.2's Queries table: the rows behind
// GET /api/v1/insights/queries.
//
// The file is named for the requirement (R3.2's top-N) and the method is named
// for the contract it satisfies — api.QueryListSource's QueryList, not TopN.
// The difference matters: R8.10's paging lives in the handler (pageRows cuts
// the page and mints the cursor, which is what makes meta.total mean D18's
// "matching objects before the page was cut"), so the store's contract is
// "return the rows that match q's scope, not a page". A store method named
// TopN(limit) would invite the reader to apply the limit in SQL, and a store
// that returns a page makes meta.total report the page size — the exact failure
// queries.go's doc names. So q.Limit is deliberately unused here.
//
// # The row identity all three read paths share
//
// (fingerprint, namespace, cluster, datname).
//
// That is the projection R8.2's QueryView describes — it has no pod and no user
// column, and its own doc says why ("R3.2's table has no user column — users
// are R3.7's rollup and R10.7's detail header"). query_rollup's PK is finer
// (it adds pod and usename, per R9.4's row identity), so the list row is a
// GROUP BY over the two columns the DTO omits. detail.go and series.go group by
// the same four columns, which is the only way the three routes can agree on
// one query's numbers without a shared row type — and api.QueryDetailSource's
// contract makes the agreement checkable, not aspirational: the detail view's
// embedded QueryView must agree "with the row the list route would have shown
// for the same triple".
//
// # Why the load columns come from query_rollup and not query_source
//
// query_source holds the cumulative counters (R3.3's baseline); query_rollup
// holds the per-minute deltas. A window question — and R8.4's ?from=/?to= make
// every list request a window question — is answerable only from the deltas,
// because the cumulative counters reset (R3.3's stats_reset) and a rollup row
// does not. dto.LoadColumns says the same thing field by field: AvgExecTimeMs is
// "the window's mean … an R3.3 delta, not the view's cumulative mean".
package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// queryListSQL is the R3.2 aggregate: one row per (fingerprint, namespace,
// cluster, datname), with the window's deltas summed over every pod and user.
//
// The JOIN to query_source is not redundant with the GROUP BY. It is 1:1 with
// each rolled-up row (query_source's PK is exactly the six columns the rollup's
// PK groups on), so it cannot inflate the sums, and it is the only source for
// pg_major — which dto.QueryView needs for a reason R3.2 makes load-bearing:
// LoadByIOWaitMs came from blk_read_time/blk_write_time on PG 15–16 and from
// shared_blk_read_time/shared_blk_write_time on PG 17+, and R10.28 makes "PG16
// vs PG17 column differences" a named empty state the UI must handle.
//
// queryid comes from a correlated subquery rather than MAX(s.queryid) because
// the group spans pods and users, each with its own queryid, and the answer the
// DTO wants is the one belonging to the busiest source — MAX() would return the
// lexicographically largest, which is a different string. The subquery's
// ORDER BY ends on pod and usename so the choice is deterministic when two
// sources tie on total_exec_ms.
const queryListSQL = `
SELECT r.fingerprint, r.namespace, r.cluster, r.datname,
       SUM(r.calls)            AS calls,
       SUM(r.total_exec_ms)    AS total_exec_ms,
       SUM(r.cpu_ms)           AS cpu_ms,
       SUM(r.io_wait_ms)       AS io_wait_ms,
       SUM(r.rows_returned)    AS rows_returned,
       SUM(r.shared_blks_hit)  AS shared_blks_hit,
       SUM(r.shared_blks_read) AS shared_blks_read,
       MAX(s.pg_major)         AS pg_major,
       (SELECT s2.queryid
          FROM query_source s2
         WHERE s2.fingerprint = r.fingerprint
           AND s2.namespace   = r.namespace
           AND s2.cluster     = r.cluster
           AND s2.datname     = r.datname
         ORDER BY s2.total_exec_ms DESC, s2.pod ASC, s2.usename ASC
         LIMIT 1)              AS queryid,
       q.display_text          AS display_text,
       q.truncated             AS truncated,
       q.first_seen            AS first_seen
FROM query_rollup r
JOIN query q        ON q.fingerprint = r.fingerprint
JOIN query_source s ON s.fingerprint = r.fingerprint
                   AND s.namespace   = r.namespace
                   AND s.cluster     = r.cluster
                   AND s.pod         = r.pod
                   AND s.datname     = r.datname
                   AND s.usename     = r.usename
`

// queryListGroupBy closes the aggregate. display_text, truncated and first_seen
// are in the GROUP BY rather than wrapped in MAX() because they are functionally
// dependent on r.fingerprint (they are columns of the joined query table) and
// naming them is portable SQL where SQLite's bare-column extension is not.
const queryListGroupBy = `
GROUP BY r.fingerprint, r.namespace, r.cluster, r.datname,
         q.display_text, q.truncated, q.first_seen
`

// clusterLoadSQL is the denominator of R3.2's `Load by total time`.
//
// dto.LoadColumns.LoadByTotalTime is "this query's share of the cluster's total
// load", so the denominator is the cluster's total — not the filtered subset's.
// That distinction is the difference between a page that means something and one
// that does not: with ?db=appdb the rows shown are appdb's, and if the
// denominator were also appdb-only then every database's queries would sum to
// 100% and the column would stop meaning "share of the cluster". So this query
// deliberately ignores the db filter while queryListSQL honours it. The
// namespace and cluster filters do apply, because they scope which cluster the
// percentage is measured against.
const clusterLoadSQL = `
SELECT r.namespace, r.cluster, SUM(r.total_exec_ms)
FROM query_rollup r
`

// queryListRow is one aggregated row, before it becomes a dto.QueryView.
//
// It exists as a type because the same row feeds two DTOs: QueryView directly
// (topn.go) and QueryDetailView by embedding (detail.go). Deriving the view from
// the row in one place is what keeps the two responses from disagreeing about
// one query's load columns, which is the contract api/query_detail.go states and
// the one thing a second mapping would break.
type queryListRow struct {
	fingerprint string
	namespace   string
	cluster     string
	datname     string

	calls        int64
	totalExecMs  float64
	cpuMs        float64
	ioWaitMs     float64
	rowsReturned float64
	sharedHit    float64
	sharedRead   float64

	pgMajor   int
	queryid   string
	display   string
	truncated int
	firstSeen int64
}

// view turns the row into the DTO, given its cluster's total load.
//
// The three derived columns are the three the DTO documents as derived:
// LoadByTotalTime is the share of clusterTotal, PctLoadByQueries carries the
// same value (dto's own doc: "the wording that distinguishes them is the spec's
// to supply, not ours to invent. Until it does, both carry the R3.2 value"), and
// the two averages divide by calls. Every division guards its denominator,
// because a zero calls with a non-zero total is a row the collector can produce
// (a stats reset mid-interval, R3.3) and 0/0 in Go is NaN, which encoding/json
// then refuses to marshal — the one value that turns a 200 into a 500 at the
// encoding step, after the headers are already sent.
func (r queryListRow) view(clusterTotal float64) dto.QueryView {
	pct := 0.0
	if clusterTotal > 0 {
		pct = 100 * r.totalExecMs / clusterTotal
	}
	var avgMs, avgRows float64
	if r.calls > 0 {
		avgMs = r.totalExecMs / float64(r.calls)
		avgRows = r.rowsReturned / float64(r.calls)
	}
	return dto.QueryView{
		Fingerprint: r.fingerprint,
		QueryID:     r.queryid,
		Query:       r.display,
		Truncated:   r.truncated != 0,
		Database:    r.datname,
		Namespace:   r.namespace,
		Cluster:     r.cluster,
		PGMajor:     r.pgMajor,
		// R7.1's recommender is post-0.1 and the insight engine is Phase 6's, so
		// Phase 3 answers with the empty list the DTO documents. make, not nil:
		// the field has no omitempty, and `"recommendations": null` is what a nil
		// slice marshals to — the UI's `.length` reads undefined instead of 0.
		Recommendations: []dto.RecommendationView{},
		LoadColumns: dto.LoadColumns{
			LoadByTotalTime:  pct,
			PctLoadByQueries: pct,
			AvgExecTimeMs:    avgMs,
			TotalExecTimeMs:  r.totalExecMs,
			AvgRowsReturned:  avgRows,
			TimesCalled:      r.calls,
			LoadByCPUMs:      r.cpuMs,
			LoadByIOWaitMs:   r.ioWaitMs,
		},
	}
}

// QueryList implements api.QueryListSource: every query row matching q's scope.
//
// The rows come back ordered by fingerprint, not by q.Sort. The handler sorts
// them — pageRows calls sortRowsBy with the route's own querySortKey before it
// cuts — and api's sort-key function is unexported, so a store that sorted too
// would need a second copy of it. Two copies of a sort key is the duplication
// this project's charter exists to prevent (the same argument paging.go makes
// about the cursor), and the store's order is not observable in the response
// anyway. Fingerprint order is used so the store's own output is deterministic
// for its tests and for any future caller that does not page.
func (s *Store) QueryList(ctx context.Context, q api.ListQuery) ([]dto.QueryView, error) {
	from, to := s.window(q.From, q.To)

	totals, err := s.clusterLoad(ctx, from, to, q.Namespace, q.Cluster)
	if err != nil {
		return nil, err
	}

	rows, err := s.queryRows(ctx, from, to, q.Namespace, q.Cluster, q.DB, "")
	if err != nil {
		return nil, err
	}

	views := make([]dto.QueryView, 0, len(rows))
	for _, r := range rows {
		views = append(views, r.view(totals[clusterKey(r.namespace, r.cluster)]))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Fingerprint < views[j].Fingerprint })
	return views, nil
}

// queryRows runs the R3.2 aggregate for one scope.
//
// fingerprint is empty for the list route and set for the detail route, which is
// how QueryDetail gets LoadColumns that agree with the list's row by
// construction: it runs the identical query with one more equality, so the two
// responses cannot disagree about a query's totals the way two hand-written
// mappings can.
func (s *Store) queryRows(
	ctx context.Context,
	from, to time.Time,
	namespace, cluster, db, fingerprint string,
) ([]queryListRow, error) {
	w := newWhere()
	w.raw("r.bucket_kind = 'minute'")
	w.raw("r.bucket >= ? AND r.bucket <= ?", unix(from), unix(to))
	if fingerprint != "" {
		w.eq("r.fingerprint", fingerprint)
	}
	if namespace != "" {
		w.eq("r.namespace", namespace)
	}
	if cluster != "" {
		w.eq("r.cluster", cluster)
	}
	if db != "" {
		w.eq("r.datname", db)
	}

	rows, err := s.db.QueryContext(ctx, queryListSQL+w.String()+queryListGroupBy, w.Args()...)
	if err != nil {
		return nil, fmt.Errorf("store: query list: %w", err)
	}
	defer rows.Close()

	var out []queryListRow
	for rows.Next() {
		var r queryListRow
		if err := rows.Scan(
			&r.fingerprint, &r.namespace, &r.cluster, &r.datname,
			&r.calls, &r.totalExecMs, &r.cpuMs, &r.ioWaitMs,
			&r.rowsReturned, &r.sharedHit, &r.sharedRead,
			&r.pgMajor, &r.queryid, &r.display, &r.truncated, &r.firstSeen,
		); err != nil {
			return nil, fmt.Errorf("store: query list scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: query list rows: %w", err)
	}
	return out, nil
}

// clusterLoad is the LoadByTotalTime denominator: total load per cluster in the
// window. See clusterLoadSQL for why the db filter is not applied here.
func (s *Store) clusterLoad(ctx context.Context, from, to time.Time, namespace, cluster string) (map[string]float64, error) {
	w := newWhere()
	w.raw("r.bucket_kind = 'minute'")
	w.raw("r.bucket >= ? AND r.bucket <= ?", unix(from), unix(to))
	if namespace != "" {
		w.eq("r.namespace", namespace)
	}
	if cluster != "" {
		w.eq("r.cluster", cluster)
	}

	rows, err := s.db.QueryContext(ctx, clusterLoadSQL+w.String()+" GROUP BY r.namespace, r.cluster", w.Args()...)
	if err != nil {
		return nil, fmt.Errorf("store: cluster load: %w", err)
	}
	defer rows.Close()

	totals := map[string]float64{}
	for rows.Next() {
		var ns, cl string
		var total float64
		if err := rows.Scan(&ns, &cl, &total); err != nil {
			return nil, fmt.Errorf("store: cluster load scan: %w", err)
		}
		totals[clusterKey(ns, cl)] = total
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cluster load rows: %w", err)
	}
	return totals, nil
}

// clusterKey is the (namespace, cluster) map key. \x00 is the separator for the
// same reason collector.sinkKey uses it: it cannot appear in a Kubernetes name,
// so ("a/b", "c") and ("a", "b/c") cannot collide.
func clusterKey(namespace, cluster string) string {
	return namespace + "\x00" + cluster
}

// window resolves a [from, to] pair against the store's own retention.
//
// An unset pair becomes [now - RetentionWindow(), now], which is what makes the
// two storage profiles answer honestly without a second code path: a file store
// looks back 7 d (retention.statementSnapshots) and an in-memory one 24 h
// (§3.4:219's window), because RetentionWindow is what answers, not a constant
// in the query. A caller that asks for a window wider than the store's is
// clipped rather than rejected — the rows simply are not there, and R3.14's
// "answer not-available rather than zero" is satisfied by returning fewer rows,
// not by an error.
func (s *Store) window(from, to time.Time) (time.Time, time.Time) {
	if to.IsZero() {
		to = s.now()
	}
	if from.IsZero() {
		from = to.Add(-s.RetentionWindow())
	}
	// A reversed window is a client bug worth keeping rather than swapping:
	// swapping turns "to is before from" into a silent rewrite of the request,
	// and the empty answer it produces is the honest one.
	if from.After(to) {
		from = to
	}
	return from.UTC(), to.UTC()
}
