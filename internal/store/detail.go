// detail.go is R10.7's Query Detail: the object behind
// GET /api/v1/insights/queries/{fingerprint}?namespace=&cluster=.
//
// The route's contract (api/query_detail.go:88-91) has two halves: not-found for
// a triple with no row, and — for a triple that has one — a view whose embedded
// QueryView "agrees with the row the list route would have shown for the same
// triple". The second half is why this file calls topn.go's queryRows instead of
// growing a second mapping: the list row and the detail's embedded view are the
// same SELECT over the same window, so they cannot disagree, which is the property
// the api's doc says the embedding exists to make *checkable* rather than
// aspirational. A detail.go that re-derived LoadColumns would be the second copy
// of a contract the charter exists to prevent, and the first thing a UI bug report
// finds ("the table says 4.2 ms, the detail page says 4.1").
//
// # Which row the fingerprint names (A3's R10.7 ruling)
//
// The path carries one fingerprint and R9.4's row identity is (cluster, db, user),
// so a fingerprint has several (datname, usename) rows per cluster. A3 ruled on
// exactly that (requirements.md R10.7: "Ruling (A3, on the row-selection question
// A2 routed from internal/api/query_detail.go:93)"): the header's database and
// user name the highest-total_exec_ms (datname, usename) pair, ties broken
// lexicographically on datname then usename. detailSourcesSQL and foldPairs
// implement that rule literally. The ruling's own reasoning — "Choosing by
// total_exec_ms is the rule R9.5 already applies to pick which queries to export,
// and internal/store/statements.go:369-370 applies it inside the store for the
// same reason; naming it here stops a second implementation choosing alphabetically
// and rendering a different page for the same URL" — is the same rule mergeRows
// now applies in statements.go, so the store has one selection rule and three
// routes that use it.
//
// The ruling also adds optional ?db= and ?user= to the route, because "omitting
// them entirely is not enough: R10.6's Queries table filters on `user`, so a viewer
// filtered to `appuser` who opens a row whose highest-load user is `postgres` would
// get a detail page whose header contradicts the filter they arrived through". The
// shipped interface has three parameters and the ruling says so
// ("The route's shipped signature (query_detail.go:93, (ctx, namespace, cluster,
// fingerprint)) implements the no-parameter case and needs the two optionals added
// to implement the filtered one"), so QueryDetail implements the shipped three and
// the selection logic lives behind two extra parameters: adding them to the
// interface becomes a signature change with no logic change behind it.
//
// # Why five queries
//
// They are the DTO's five field groups: the pair set (query_source joined to query,
// for the header, the latency panel and the cumulative fallback), the window
// aggregate (query_rollup, for the embedded QueryView), the cluster totals
// (query_rollup again, for LoadByTotalTime's denominator), plan samples, and plan
// changes. One join across four tables would answer all five in one round trip and
// is how two endpoints start disagreeing about one query's numbers — each field
// group's SQL then has one owner, and the aggregate the detail renders is
// textually the aggregate the list renders. SQLite serves all five from the PK and
// the two indexes in well under a millisecond; R10.14's page budget is not spent on
// query count here.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// The compile-time assertion that QueryDetail is api.QueryDetailSource's method and
// not a method with a similar name. The first draft of this file had TopNQuery and
// DetailQuery methods that matched nothing in internal/api and still compiled,
// because a Go method with no interface naming it is never checked; the assertion is
// what turns "the store has a method the route calls" from a claim in a comment into
// something the build enforces. topn.go gets the same guarantee for free from
// api.ListQuery appearing in its signature; here the type is only referenced in
// prose, so the reference has to be real.
var _ api.QueryDetailSource = (*Store)(nil)

// detailSourcesSQL is every query_source row for one (fingerprint, namespace,
// cluster) triple, joined to the query table for the text the DTO renders.
//
// It answers three of the detail's fields at once — which (datname, usename) pairs
// exist (R10.7's header), the queryid and pg_major the QueryView reports, and the
// min/max/stddev the LatencyView reports — because all three are columns of the one
// table, and the alternative is three queries that each have to agree on which rows
// they are talking about.
//
// The JOIN to query is not optional and cannot drop a row: query_source.fingerprint
// carries REFERENCES query(fingerprint) (schema.go:114, and since A3's S30 the two
// plan tables do too), so every source row has its query row. It is what fills
// QueryView.Query with the stored display_text rather than a re-normalized copy —
// schema.go documents why the two are not the same string.
const detailSourcesSQL = `
SELECT s.datname, s.usename, s.pod, s.queryid, s.pg_major,
       s.calls, s.total_exec_ms, s.cpu_ms, s.io_wait_ms,
       s.rows_returned, s.shared_blks_hit, s.shared_blks_read,
       s.min_exec_ms, s.max_exec_ms, s.stddev_exec_ms,
       q.display_text, q.truncated, q.first_seen
FROM query_source s
JOIN query q ON q.fingerprint = s.fingerprint
`

// detailPlansSQL is R3.10's plan-sample list for one row, newest first — the same
// rows R8.2's /insights/queries/{fingerprint}/plans route serves, inlined here
// because R10.7 puts the list on this page.
//
// The WHERE clause is the row's identity, not the fingerprint. Before A3's R10.7
// extension the table's key was (fingerprint, captured_at) and `WHERE fingerprint
// = ?` addressed every plan the fingerprint had, in every database, under every
// user — a detail page for appuser showing postgres's plans. Now that the PK
// carries the identity, the query names the rows the ruling scopes: a filtered
// detail page reads the filtered pair's plans and nothing else.
//
// Five of the PK's six columns, not six. The route has no pod parameter (R10.7's
// set is namespace, cluster, fingerprint plus the two optionals) and detailPair is
// a fold across pods, so the page legitimately shows the pair's samples from every
// pod in the cluster; naming one of them would hide the others behind a choice no
// caller made. SQLite still serves the predicate from the PK's leading columns.
const detailPlansSQL = `
SELECT captured_at, method, availability, sql, plan_json, plan_hash, message
FROM plan_sample
WHERE fingerprint = ? AND namespace = ? AND cluster = ? AND datname = ? AND usename = ?
ORDER BY captured_at DESC
LIMIT ?
`

// detailPlanChangesSQL is R3.11's event list for one row, newest first.
// dto.QueryDetailView.PlanChanges is the field R3.11's series renders: "one event
// says the plan moved, two say it moved twice".
//
// Same identity in the WHERE clause, same reason and the same five columns — and it
// is the reason the two lists now agree with each other. A page whose header names
// (db1, appuser) used to list plan changes detected for db2's copy of the
// fingerprint while its plan list came from db1's; both queries now take the one
// identity the header does.
const detailPlanChangesSQL = `
SELECT detected_at, previous_plan_hash, current_plan_hash
FROM plan_change
WHERE fingerprint = ? AND namespace = ? AND cluster = ? AND datname = ? AND usename = ?
ORDER BY detected_at DESC
LIMIT ?
`

// detailHistoryLimit caps both inline history lists.
//
// R8.2 gives the plan list its own paged route, so the inline copy is the page's
// summary and not the route's dataset: a fingerprint sampled every round for the
// full 7 d window has ~10 000 plan rows, and inlining them turns R10.14's page
// budget into a download. Twenty is the newest handful a viewer opens; the paged
// route is where the rest lives. One const for both lists because the two lists
// share a page and will be argued about together.
const detailHistoryLimit = 20

// detailSource is one query_source row plus the query-table columns the DTO needs.
//
// It is the row shape detailSourcesSQL selects. Its field set is deliberately the
// same as queryListRow's plus the per-pod latency columns, so a detailPair folded
// from it can build a dto.QueryView through queryListRow.view — one mapping from a
// stored row to the DTO, shared by the list route and this one.
type detailSource struct {
	datname, usename, pod, queryid string
	pgMajor                        int
	calls                          int64
	totalExecMs, cpuMs, ioWaitMs   float64
	rowsReturned, sharedHit        float64
	sharedRead                     float64
	minExecMs, maxExecMs           float64
	// stddevExecMs is the schema's -1 sentinel for "PG reported NULL stddev"
	// (a row with no sample). It is a float here rather than a *float64 because
	// the column is NOT NULL with -1 as the encoding; statements.go's
	// StatementRow uses the pointer form because the collector's input genuinely
	// has no value to encode.
	stddevExecMs float64

	display   string
	truncated int
	firstSeen int64
}

// detailPair is one (datname, usename) pair folded from every pod's rows: the
// summed counters, the pooled latency stats, and the text fields the QueryView
// renders.
type detailPair struct {
	datname, usename             string
	rows                         int
	calls                        int64
	totalExecMs, cpuMs, ioWaitMs float64
	rowsReturned, sharedHit      float64
	sharedRead                   float64
	minMs, maxMs, stddevMs       float64

	pgMajor   int
	queryid   string
	display   string
	truncated int
	firstSeen int64
}

// view builds the QueryView this pair renders, without a cluster total.
//
// It is the fallback for the case the api's doc separates from not-found: the
// triple exists but the requested window has no rollup rows in it. The list route
// would show no row at all, so there is nothing to agree with, and the honest
// answer is the pair's cumulative counters — which is what query_source holds and
// what the detail's latency panel is computed from anyway. The window case goes
// through queryListRow.view instead, so the number the two routes share has one
// implementation.
//
// The fingerprint is a parameter and not a field: the pair is a fold over rows that
// all share one fingerprint (it is the key the query was run with), so storing it
// again would be a second copy of the caller's argument.
func (p detailPair) view(namespace, cluster, fingerprint string) dto.QueryView {
	return queryListRow{
		fingerprint:  fingerprint,
		namespace:    namespace,
		cluster:      cluster,
		datname:      p.datname,
		calls:        p.calls,
		totalExecMs:  p.totalExecMs,
		cpuMs:        p.cpuMs,
		ioWaitMs:     p.ioWaitMs,
		rowsReturned: p.rowsReturned,
		sharedHit:    p.sharedHit,
		sharedRead:   p.sharedRead,
		pgMajor:      p.pgMajor,
		queryid:      p.queryid,
		display:      p.display,
		truncated:    p.truncated,
		firstSeen:    p.firstSeen,
	}.view(0)
}

// QueryDetail implements api.QueryDetailSource: the detail view of one row.
func (s *Store) QueryDetail(ctx context.Context, namespace, cluster, fingerprint string) (dto.QueryDetailView, error) {
	return s.queryDetail(ctx, namespace, cluster, fingerprint, "", "")
}

// queryDetail is QueryDetail with R10.7's two optional parameters filled in.
//
// db and user are empty for the shipped three-parameter route, which is the
// ruling's no-parameter case: the pair selection picks the highest-load pair. Set
// either and the same code narrows the candidate set before the selection, which is
// the filtered case the ruling adds — the filter names the row, so a viewer who
// arrived through R10.6's `user` filter gets a header that agrees with it instead
// of one that contradicts it.
func (s *Store) queryDetail(
	ctx context.Context,
	namespace, cluster, fingerprint, db, user string,
) (dto.QueryDetailView, error) {
	// The route has no ?from=/?to= (R8.4's window set governs list endpoints, and
	// R10.7's route has no window parameter), so the detail asks the store's own
	// window — the same s.window(zero, zero) the list uses when its caller omits
	// them, which is what keeps the two routes' LoadColumns comparable.
	from, to := s.window(time.Time{}, time.Time{})

	sources, err := s.detailSources(ctx, namespace, cluster, fingerprint, db, user)
	if err != nil {
		return dto.QueryDetailView{}, err
	}
	if len(sources) == 0 {
		// The one answer that is not a 200. The message carries the triple, which
		// is what api's doc asks a wrapping store to add ("no row for 550e8400… in
		// default/pg1"), and it wraps ErrUnknownFingerprint — which wraps
		// api.ErrQueryDetailNotFound — so the handler answers 404 rather than 503.
		//
		// A malformed fingerprint lands here too, and 404 is right for the same
		// reason it is right for an absent one: the store has no row. The handler's
		// own 400 path covers the only malformed case it checks (an empty
		// fingerprint), and R3.4's shape rule is enforced on the write side by
		// validFingerprint, so a read cannot tell "bad key" from "no row" any
		// better than the store could before S35 split the two sentinels.
		return dto.QueryDetailView{}, fmt.Errorf("store: no row for %s in %s/%s: %w",
			fingerprint, namespace, cluster, ErrUnknownFingerprint)
	}

	pairs := foldPairs(sources)
	pair := pairs[0] // R10.7's named row: highest total, ties on datname then usename.

	totals, err := s.clusterLoad(ctx, from, to, namespace, cluster)
	if err != nil {
		return dto.QueryDetailView{}, err
	}
	rows, err := s.queryRows(ctx, from, to, namespace, cluster, pair.datname, fingerprint)
	if err != nil {
		return dto.QueryDetailView{}, err
	}

	// The embedded QueryView: the list row when the window has one (so the two
	// routes agree by construction), the pair's cumulative row when it does not.
	view := pair.view(namespace, cluster, fingerprint)
	if len(rows) > 0 {
		view = rows[0].view(totals[clusterKey(namespace, cluster)])
	}

	// Both plan reads take the pair the header names, so the page cannot show a
	// header for (db1, appuser) and a plan list for (db2, postgres) — the failure
	// the widened PK makes impossible now that the reads have to name a row to
	// exist at all.
	plans, err := s.detailPlans(ctx, namespace, cluster, fingerprint, pair.datname, pair.usename)
	if err != nil {
		return dto.QueryDetailView{}, err
	}
	changes, err := s.detailPlanChanges(ctx, namespace, cluster, fingerprint, pair.datname, pair.usename)
	if err != nil {
		return dto.QueryDetailView{}, err
	}

	return dto.QueryDetailView{
		QueryView: view,
		// R10.7's header `user`: the pair's role, not the QueryView's — R3.2's
		// table has no user column, which is why the field exists only here.
		User:      pair.usename,
		FirstSeen: timeFromUnix(pair.firstSeen),
		Latency:   pair.latency(),
		// Both lists are built as empty-and-non-nil in their own helpers, because
		// neither has omitempty and `"plans": null` is what a nil slice marshals
		// to — the UI's `.length` reads undefined instead of 0.
		Plans:       plans,
		PlanChanges: changes,
	}, nil
}

// latency is R10.7's latency panel for this pair: the four stats pg_stat_statements
// has, and nothing it does not.
//
// The three percentiles stay nil. dto's doc is explicit that nil is the R3.9
// fallback's answer ("Present only when a real distribution exists to produce them;
// nil (absent) is the R3.9 fallback's answer and means 'render the four stats and
// the histogram instead'"), and pg_stat_statements has min/mean/max/stddev and no
// percentiles at all — a store that invented p95 from a mean and a stddev would
// render a number the source never measured.
//
// Buckets is empty, not nil, for the same reason it is empty in topn.go's
// Recommendations: R3.9's eight named buckets are Phase 4's activity collector's
// (schema.go names the table's absence as deliberate), so Phase 3 has no histogram
// to serve, and `[]` is the difference between "none" and "the field is missing".
//
// Sampled is false because it describes the histogram, and there is none: R3.9's
// "sampled, biased toward long queries" label belongs to the 60 s
// pg_stat_activity sample, not to these four stats.
func (p detailPair) latency() dto.LatencyView {
	v := dto.LatencyView{
		MinMs:    p.minMs,
		MaxMs:    p.maxMs,
		StddevMs: p.stddevMs,
		Sampled:  false,
		Buckets:  []dto.HistogramBucket{},
		SumMs:    p.totalExecMs,
		Count:    p.calls,
	}
	if p.calls > 0 {
		v.MeanMs = p.totalExecMs / float64(p.calls)
	}
	return v
}

// detailSources runs detailSourcesSQL for one triple, narrowed by the two optional
// R10.7 parameters when they are set.
func (s *Store) detailSources(
	ctx context.Context,
	namespace, cluster, fingerprint, db, user string,
) ([]detailSource, error) {
	w := newWhere()
	w.eq("s.fingerprint", fingerprint)
	w.eq("s.namespace", namespace)
	w.eq("s.cluster", cluster)
	if db != "" {
		w.eq("s.datname", db)
	}
	if user != "" {
		w.eq("s.usename", user)
	}
	// The ORDER BY is the tie-break half of R10.7's rule (datname, then usename,
	// then pod so a group's row order is fixed for the fold). foldPairs applies the
	// total-first half.
	rows, err := s.db.QueryContext(ctx,
		detailSourcesSQL+w.String()+" ORDER BY s.datname, s.usename, s.pod", w.Args()...)
	if err != nil {
		return nil, fmt.Errorf("store: query detail sources: %w", err)
	}
	defer rows.Close()

	var out []detailSource
	for rows.Next() {
		var r detailSource
		if err := rows.Scan(
			&r.datname, &r.usename, &r.pod, &r.queryid, &r.pgMajor,
			&r.calls, &r.totalExecMs, &r.cpuMs, &r.ioWaitMs,
			&r.rowsReturned, &r.sharedHit, &r.sharedRead,
			&r.minExecMs, &r.maxExecMs, &r.stddevExecMs,
			&r.display, &r.truncated, &r.firstSeen,
		); err != nil {
			return nil, fmt.Errorf("store: query detail sources scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: query detail sources rows: %w", err)
	}
	return out, nil
}

// detailPlans reads detailPlansSQL into the DTO's plan list.
//
// Its five string parameters are detailPlansSQL's five placeholders in order — the
// route's three plus the pair's datname and usename — rather than a struct, because
// the only struct that spells that set is the one the caller already built.
func (s *Store) detailPlans(ctx context.Context, namespace, cluster, fingerprint, datname, usename string) ([]dto.PlanSampleView, error) {
	rows, err := s.db.QueryContext(ctx, detailPlansSQL,
		fingerprint, namespace, cluster, datname, usename, detailHistoryLimit)
	if err != nil {
		return nil, fmt.Errorf("store: detail plans: %w", err)
	}
	defer rows.Close()

	out := []dto.PlanSampleView{}
	for rows.Next() {
		var (
			capturedAt           int64
			method, availability string
			sqlText              string
			planHash, message    string
			planJSON             []byte
		)
		if err := rows.Scan(&capturedAt, &method, &availability, &sqlText, &planJSON, &planHash, &message); err != nil {
			return nil, fmt.Errorf("store: detail plans scan: %w", err)
		}
		// plan_json is the nullable column: an unavailable plan (R3.10's (c),
		// insufficient table privileges) has a row with no plan bytes, and
		// json.RawMessage of a nil []byte marshals to nothing under omitempty —
		// which is exactly dto's rule ("Omitted when there is no plan, which is
		// R3.10's (c)").
		out = append(out, dto.PlanSampleView{
			CapturedAt:   timeFromUnix(capturedAt),
			Method:       method,
			Availability: availability,
			SQL:          sqlText,
			PlanJSON:     json.RawMessage(planJSON),
			PlanHash:     planHash,
			Message:      message,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: detail plans rows: %w", err)
	}
	return out, nil
}

// detailPlanChanges reads detailPlanChangesSQL into R3.11's event list, with the
// same five parameters and the same order for the same reason.
func (s *Store) detailPlanChanges(ctx context.Context, namespace, cluster, fingerprint, datname, usename string) ([]dto.PlanChangeView, error) {
	rows, err := s.db.QueryContext(ctx, detailPlanChangesSQL,
		fingerprint, namespace, cluster, datname, usename, detailHistoryLimit)
	if err != nil {
		return nil, fmt.Errorf("store: detail plan changes: %w", err)
	}
	defer rows.Close()

	out := []dto.PlanChangeView{}
	for rows.Next() {
		var (
			detectedAt        int64
			previous, current string
		)
		if err := rows.Scan(&detectedAt, &previous, &current); err != nil {
			return nil, fmt.Errorf("store: detail plan changes scan: %w", err)
		}
		out = append(out, dto.PlanChangeView{
			DetectedAt:       timeFromUnix(detectedAt),
			PreviousPlanHash: previous,
			CurrentPlanHash:  current,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: detail plan changes rows: %w", err)
	}
	return out, nil
}

// foldPairs groups the per-pod rows into one detailPair per (datname, usename) and
// returns them in R10.7's selection order: highest total_exec_ms first, ties broken
// lexicographically on datname then usename.
//
// pairs[0] is therefore the row the route's header names, which is the answer the
// ruling fixed and the reason this is a function with one job rather than an inline
// loop in queryDetail: the same rule is applied by the list route's row set and by
// the detail's header, and a rule applied twice has to be written once.
func foldPairs(rows []detailSource) []detailPair {
	groups := map[[2]string][]detailSource{}
	var keys [][2]string
	for _, r := range rows {
		k := [2]string{r.datname, r.usename}
		if _, seen := groups[k]; !seen {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], r)
	}

	out := make([]detailPair, 0, len(keys))
	for _, k := range keys {
		out = append(out, foldOne(k[0], k[1], groups[k]))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].totalExecMs != out[j].totalExecMs {
			return out[i].totalExecMs > out[j].totalExecMs
		}
		if out[i].datname != out[j].datname {
			return out[i].datname < out[j].datname
		}
		return out[i].usename < out[j].usename
	})
	return out
}

// foldOne folds one (datname, usename) group's per-pod rows into a detailPair.
//
// The counters sum and Min/Max are the extremes across pods — the same rule
// statements.go's mergeRows applies inside one snapshot, applied across pods this
// time, and for the same reason: a min is not a sum.
//
// Stddev is the pooled within-group value, sqrt(Σ nᵢ(σᵢ² + μᵢ²)/N − μ²) with
// μ = Σ nᵢμᵢ/N. Averaging the per-pod stddevs would be wrong — it drops the
// between-pod spread the pooled form keeps — and the one-pass form is algebraically
// the same number without a second loop. Rows carrying the schema's -1 sentinel are
// excluded from the pooled math (a row with no sample has no distribution to
// pool), and a pair with no real stddev reports 0: dto.LatencyView.StddevMs is a
// float64 with no not-available encoding, unlike the column that stores -1, so 0 is
// what "unknown" renders as. That is a DTO limitation and not a store bug — the
// store keeps the -1, the DTO cannot show it.
func foldOne(datname, usename string, rows []detailSource) detailPair {
	p := detailPair{datname: datname, usename: usename}
	best := rows[0]
	var n int64
	var sumNMean, sumNVarPlusMean2 float64

	for _, r := range rows {
		p.calls += r.calls
		p.totalExecMs += r.totalExecMs
		p.cpuMs += r.cpuMs
		p.ioWaitMs += r.ioWaitMs
		p.rowsReturned += r.rowsReturned
		p.sharedHit += r.sharedHit
		p.sharedRead += r.sharedRead
		switch {
		case p.rows == 0:
			p.minMs, p.maxMs = r.minExecMs, r.maxExecMs
		default:
			if r.minExecMs < p.minMs {
				p.minMs = r.minExecMs
			}
			if r.maxExecMs > p.maxMs {
				p.maxMs = r.maxExecMs
			}
		}
		p.rows++
		if r.totalExecMs > best.totalExecMs {
			best = r
		}
		if r.calls > 0 && r.stddevExecMs >= 0 {
			mean := r.totalExecMs / float64(r.calls)
			n += r.calls
			sumNMean += float64(r.calls) * mean
			sumNVarPlusMean2 += float64(r.calls) * (r.stddevExecMs*r.stddevExecMs + mean*mean)
		}
	}

	// The text fields come from the highest-load row, so the pair's QueryView
	// reports the queryid and pg_major of the source that did the work — the same
	// rule topn.go's correlated subquery applies to the list row.
	p.queryid, p.pgMajor = best.queryid, best.pgMajor
	p.display, p.truncated, p.firstSeen = best.display, best.truncated, best.firstSeen

	if n > 0 {
		mean := sumNMean / float64(n)
		// A tiny negative from float cancellation is possible when every row has
		// the same mean; sqrt of a negative is NaN, which encoding/json refuses to
		// marshal, so the guard is load-bearing and not decoration.
		if v := sumNVarPlusMean2/float64(n) - mean*mean; v > 0 {
			p.stddevMs = math.Sqrt(v)
		}
	}
	return p
}
