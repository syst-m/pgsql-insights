// R8.11 — the batched series endpoint.
//
//	GET /api/v1/insights/queries/series?fingerprints=a,b,c&range=1h
//
// One request answers the per-minute series for up to 50 fingerprints. The
// requirement's own justification is a request count: without it R10.6's
// per-row sparklines need one request per row, and R10.14's 1.5 s page-load
// budget forbids 50 detail calls on page load. So the response is a *batch* —
// one entry per requested fingerprint inside one envelope — and the Queries
// table renders a page of sparklines from a single round trip.
//
// Two contract rules decide the shape:
//
//   - R8.4's envelope is not re-authored here. The response type is an alias
//     over dto.Envelope[QuerySeries], the same generic the clusters route
//     serves and the one web/src/api/types.ts mirrors (R16.8). A slot that has
//     already shipped a second hand-written copy of a contract does not get to
//     write a third one; the alias gives the document a name to $ref
//     (QuerySeriesEnvelope) without inventing fields.
//   - R8.10's paging does not apply, because a batch is not a page: the client
//     names the fingerprints it wants, so there is nothing to page through and
//     meta.nextCursor stays empty. meta.total is the number of fingerprints the
//     request asked for (after de-duplication) — R8.4's "matching objects
//     before the page was cut", read against a request that *is* the set — and
//     meta.truncated follows the invariant dto.Meta states (truncated iff
//     len(data) < total). A source that answers 40 of 50 therefore says
//     total 50 / truncated true rather than quietly reporting 40.

package api

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// QuerySeriesPath is R8.11's route, named once so server.go's registration,
// this handler and the tests all spell it identically — the pattern promql.go
// set with QueryMetricsPath and SystemMetricsPath.
const QuerySeriesPath = "/api/v1/insights/queries/series"

// MaxBatchFingerprints is R8.11's cap: "up to 50 fingerprints in one
// response". Above it the request is InvalidRequest rather than silently cut,
// which is R8.10's rule for a limit above its max ("above-max is
// InvalidRequest, not a clamp") applied to the batch parameter: a client that
// asks for 80 and gets 200 with 50 entries cannot tell "30 have no data" from
// "you were truncated", and keeping those two answers distinguishable is the
// whole reason R8.10 forbids the clamp.
const MaxBatchFingerprints = 50

// R10.15's time-range vocabulary: the five presets the UI's range picker
// offers, plus `custom`, which is how R8.4's from/to pair is reached on this
// route. A preset is a window, not a resolution — the bucket is fixed at one
// minute by R8.11's own wording ("the per-minute series").
const (
	Range1Hour  = "1h"
	Range6Hours = "6h"
	Range1Day   = "1d"
	Range7Days  = "7d"
	Range30Days = "30d"
	RangeCustom = "custom"

	// DefaultRange is what a request with no `range=` asks for. R8.4's other
	// routes default a missing parameter too (limit defaults to 50), and 1h is
	// the first preset in R10.15's list and the one the Queries page opens on.
	DefaultRange = Range1Hour
)

// rangeWindows maps each preset to its duration. `custom` is absent by
// construction — its window is whatever from and to say, so it has no duration
// to look up — and that absence is what the parser uses to route it.
var rangeWindows = map[string]time.Duration{
	Range1Hour:  time.Hour,
	Range6Hours: 6 * time.Hour,
	Range1Day:   24 * time.Hour,
	Range7Days:  7 * 24 * time.Hour,
	Range30Days: 30 * 24 * time.Hour,
}

// presetRanges is the sorted key list of rangeWindows: derived from the map
// rather than written out beside it, so the error message, the parser and the
// OpenAPI enum cannot disagree about which presets exist.
var presetRanges = slices.Sorted(maps.Keys(rangeWindows))

// QuerySeriesEnvelope is the response body of GET /api/v1/insights/queries/series.
// It is an alias, not a struct: dto.Envelope[T] is the one definition of R8.4's
// envelope in this codebase, and this name exists so the OpenAPI document can
// $ref it the way it $refs FleetEnvelope for dto.Envelope[ClusterView].
type QuerySeriesEnvelope = dto.Envelope[QuerySeries]

// QuerySeriesPoint is one minute of one fingerprint's series. The five values
// are R9.4's aggregate family members, named from the member list in
// promql_catalogue.go rather than re-invented here: execution_time, rows,
// shared_blocks_hit, shared_blocks_read, lock_time.
//
// Carrying all five per point is what lets one response serve both R10.6's row
// sparkline and R10.7's Query Detail chart, and it is why the route takes no
// `metric=` parameter — R8.11's signature does not name one, and a per-metric
// request would need five requests per page to fill one table of sparklines,
// which is the R10.14 budget spent on the wrong thing.
//
// float64 for all five because that is the type the R9.4 metric family carries
// (a Prometheus sample value); a per-minute delta derived from a counter is not
// necessarily integral even when the underlying count is.
type QuerySeriesPoint struct {
	// Timestamp is the start of the minute, RFC 3339 in UTC (R10.21: every
	// timestamp the API emits is RFC 3339 UTC, and R8.4's meta.generatedAt is
	// the same type for the same reason).
	Timestamp time.Time `json:"timestamp"`

	ExecutionTimeSeconds float64 `json:"executionTimeSeconds"`
	Rows                 float64 `json:"rows"`
	SharedBlocksHit      float64 `json:"sharedBlocksHit"`
	SharedBlocksRead     float64 `json:"sharedBlocksRead"`
	LockTimeSeconds      float64 `json:"lockTimeSeconds"`
}

// QuerySeries is one requested fingerprint's answer.
//
// The label set is R9.3's contract. namespace and cluster are always present
// because the request is scoped to them. pod and instanceRole are present only
// when the answer is a single instance's — R5.7's compare-up-to-5 needs the
// (namespace, cluster, pod) triple to label a per-instance series — and empty
// when the points aggregate across the cluster's instances, which is what
// R10.6's table row asks for and why one entry per fingerprint is possible at
// all. datname and usename are R9.3's database/user additions and the other
// half of R9.4's row identity (per cluster, db, user). queryid is R9.5b's
// secondary, informational identifier: R3.4 is why it is optional and why it is
// never the key — `fingerprint` is.
//
// Points is `[]`, never null, for a fingerprint the store has never seen: an
// absent array and an empty one mean the same thing to the UI, and R8.3's house
// rule (and clusters_test.go's TestEmptyFleetRendersArrayNotNull) is that the
// API says "none" with `[]`.
type QuerySeries struct {
	Fingerprint  string `json:"fingerprint"`
	QueryID      string `json:"queryid,omitempty"`
	Namespace    string `json:"namespace"`
	Cluster      string `json:"cluster"`
	Pod          string `json:"pod,omitempty"`
	InstanceRole string `json:"instanceRole,omitempty"`
	Datname      string `json:"datname,omitempty"`
	Usename      string `json:"usename,omitempty"`

	Points []QuerySeriesPoint `json:"points"`
}

// SeriesQuery is the parsed, validated query string of an R8.11 request: what
// the client asked for, with the range already resolved to a concrete window so
// a source never re-derives it from a preset name.
type SeriesQuery struct {
	// Fingerprints is the de-duplicated list in request order.
	Fingerprints []string

	Namespace string
	Cluster   string
	Datname   string

	// Range is the normalised range name — one of the Range* constants.
	Range string
	// Start/End is the window the range resolved to: now-duration for a preset,
	// the request's own from/to for range=custom. RFC 3339 UTC on the wire
	// (R10.21); the response echoes neither, because the client already named
	// the window it asked for and R8.4's envelope has no field for it.
	Start time.Time
	End   time.Time
}

// parseSeriesQuery is the whole of R8.11's parameter handling, in the shape
// clusters.go set with parseClusterQuery: the handler parses, reads and writes,
// and every rule about what a legal query string is lives here where a table
// test can cover it.
//
// `now` is the clock the presets are measured against. It is a parameter rather
// than a time.Now() call so the window math is testable to the nanosecond; the
// handler passes time.Now().UTC().
func parseSeriesQuery(q url.Values, now time.Time) (SeriesQuery, error) {
	var out SeriesQuery

	fingerprints, err := parseFingerprints(q.Get("fingerprints"))
	if err != nil {
		return out, err
	}
	out.Fingerprints = fingerprints
	out.Namespace = strings.TrimSpace(q.Get("namespace"))
	out.Cluster = strings.TrimSpace(q.Get("cluster"))
	// R8.4's database scope. The *parameter* is `db` — the shared
	// components/parameters/DB spelling every list route takes — while the
	// response field is R9.3's `datname`. The doc's path item refs the DB
	// component, so the two spellings are pinned by the document rather than
	// invented here; reading `datname=` instead would accept a parameter the
	// document does not name and ignore the one it does.
	out.Datname = strings.TrimSpace(q.Get("db"))

	// Lower-cased because R10.15's vocabulary is lowercase and a client that
	// sends `1H` means the same thing; an unknown spelling is still an error,
	// not a silent default, so the tolerance costs nothing.
	rng := strings.ToLower(strings.TrimSpace(q.Get("range")))
	if rng == "" {
		rng = DefaultRange
	}
	out.Range = rng

	if rng == RangeCustom {
		// R8.4's from/to pair, parsed by the same function promql.go uses so
		// the two routes accept exactly the same set of spellings (RFC 3339 and
		// RFC 3339Nano) rather than each having its own idea of a timestamp.
		from, to := parsePromQLTime(q.Get("from")), parsePromQLTime(q.Get("to"))
		switch {
		case from.IsZero() || to.IsZero():
			return out, fmt.Errorf("range=custom requires from and to as RFC 3339 UTC timestamps; the presets are %s",
				strings.Join(presetRanges, ", "))
		case !to.After(from):
			return out, fmt.Errorf("to %s must be after from %s",
				to.UTC().Format(time.RFC3339), from.UTC().Format(time.RFC3339))
		}
		out.Start, out.End = from, to
		return out, nil
	}

	window, ok := rangeWindows[rng]
	if !ok {
		return out, fmt.Errorf("range %q is not one of %s or custom (R10.15)", rng, strings.Join(presetRanges, ", "))
	}
	// The preset wins and from/to are ignored: on this route they are only read
	// for range=custom, so a client that sends `range=1h&from=…` gets the hour
	// and no error about a parameter that does not apply to it.
	out.Start, out.End = now.Add(-window), now
	return out, nil
}

// parseFingerprints splits, trims and de-duplicates the comma-separated list,
// preserving request order — the same split/trim/dedupe shape as
// ParseCORSAllowOrigin, for the same reason: a stray space after a comma is
// normal in a hand-written URL and must not become 50 empty fingerprints.
//
// Order is preserved because R10.6 renders rows in the order it asked, and a
// response that shuffles them makes the client sort to match — the same reason
// R8.10 pins a stable order on every list it pages.
func parseFingerprints(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("fingerprints is required (R8.11): the batched endpoint answers the fingerprints you name")
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("fingerprints=%q contains no fingerprint", raw)
	}
	if len(out) > MaxBatchFingerprints {
		return nil, fmt.Errorf("fingerprints requests %d fingerprints, the maximum is %d (R8.11); split the page into two requests",
			len(out), MaxBatchFingerprints)
	}
	return out, nil
}

// QuerySeriesSource is the read side of the per-minute series store as the
// batch endpoint needs it: given a parsed query, return one QuerySeries per
// requested fingerprint. Phase 3's store implements it; the interface exists
// now so the route is written, mounted and tested before the data is — the same
// move promql.go made with PromQLSource, for the same reason (R14.7 forbids
// documenting a route that 404s, and the document already lists this one).
//
// The contract on an implementation: one entry per fingerprint in
// q.Fingerprints, in any order, with empty (not missing) Points for a
// fingerprint it has never seen. Returning fewer entries than requested is
// legal and surfaces as meta.truncated, because one stale row in a 50-row page
// load must not turn into a 404 for the other 49.
type QuerySeriesSource interface {
	QuerySeries(ctx context.Context, q SeriesQuery) ([]QuerySeries, error)
}

// emptyQuerySeries is the default source: every requested fingerprint exists
// and has no points yet. promql.go's emptySource states the honesty argument
// and it holds here unchanged — an empty answer is the true answer for a store
// that has not filled in, where a 500 would be a lie about *why* there is no
// data and a 404 would fail the whole batch over one missing row.
//
// It answers one entry per requested fingerprint rather than an empty slice,
// because that is what QuerySeriesSource asks of an implementation and because
// the alternative makes R8.4's meta lie: `data: []` beside `total: 50` reads as
// "you were truncated", when the truth is "all 50 exist, none has points yet".
// The labels it fills are the request's own scoping — the only ones an empty
// store can honestly copy — and the rest stay empty.
type emptyQuerySeries struct{}

// QuerySeries implements QuerySeriesSource by returning every requested
// fingerprint with an empty, non-nil Points.
func (emptyQuerySeries) QuerySeries(_ context.Context, q SeriesQuery) ([]QuerySeries, error) {
	out := make([]QuerySeries, 0, len(q.Fingerprints))
	for _, fp := range q.Fingerprints {
		out = append(out, QuerySeries{
			Fingerprint: fp,
			Namespace:   q.Namespace,
			Cluster:     q.Cluster,
			Datname:     q.Datname,
			Points:      []QuerySeriesPoint{},
		})
	}
	return out, nil
}

// seriesSource is cfg.Series with the default applied, so New needs no change
// and a Config that sets nothing still serves the documented route.
func (s *Server) seriesSource() QuerySeriesSource {
	if s.cfg.Series == nil {
		return emptyQuerySeries{}
	}
	return s.cfg.Series
}

// handleQuerySeries is R8.11's handler in the shape clusters.go set for
// /api/v1/clusters: parse, readiness, read, log, write — five steps, each of
// which delegates the actual rule to the function that owns it.
func (s *Server) handleQuerySeries(w http.ResponseWriter, r *http.Request) {
	q, err := parseSeriesQuery(r.URL.Query(), time.Now().UTC())
	if err != nil {
		writeErrorBody(w, http.StatusBadRequest, dto.NewError(dto.ErrInvalidRequest, err.Error()))
		return
	}
	if !s.readiness.Ready() {
		// R8.5's ErrNotReady, the same 503 body /api/v1/clusters emits, from the
		// same single Readiness value /readyz answers with (R8.7(i)). The route
		// reads the series store rather than the inventory, but readiness is the
		// process's answer about whether it has collected anything yet, and a
		// process that has not should say so rather than serve 50 empty rows.
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrNotReady, "discovery has not completed its first sync"))
		return
	}
	series, err := s.seriesSource().QuerySeries(r.Context(), q)
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable,
			dto.NewError(dto.ErrUnavailable, fmt.Sprintf("query series are not readable: %v", err)))
		return
	}
	env := buildSeriesEnvelope(q, series, time.Now().UTC())
	s.logDebug("query series served",
		"route", QuerySeriesPath,
		"range", q.Range,
		"requested", len(q.Fingerprints),
		"rows", len(env.Data),
		"truncated", env.Meta.Truncated,
	)
	writeJSON(w, http.StatusOK, env)
}

// buildSeriesEnvelope assembles R8.4's meta from the request and the answer.
//
// The three rules worth stating, because all three are the R16.8 class of bug
// (a field that means something different than the document says):
//
//   - data is never null. A nil slice from the source becomes `[]`, and a nil
//     Points inside an entry becomes `[]` too, so the UI never has to
//     distinguish "no data" from "the field was missing" — the mistake that
//     turned a missing key into a plausible empty page once already.
//   - meta.total is the number of fingerprints requested, not the number of
//     entries returned (R8.4's "matching objects before the page was cut",
//     where the requested set *is* the matching set here).
//   - meta.truncated follows dto.Meta's stated invariant, truncated iff
//     len(data) < total. For a batch it is normally false; it is true when the
//     source answers fewer entries than were asked for, which is the honest way
//     to say "some of what you asked about is missing" without a 404.
func buildSeriesEnvelope(q SeriesQuery, series []QuerySeries, now time.Time) QuerySeriesEnvelope {
	data := make([]QuerySeries, len(series))
	copy(data, series)
	for i := range data {
		if data[i].Points == nil {
			data[i].Points = []QuerySeriesPoint{}
		}
	}
	total := len(q.Fingerprints)
	if len(data) > total {
		// A source that answers more than was asked for would otherwise make
		// truncated true by R8.4's formula, which would claim a next page that
		// cannot exist. total follows the bigger of the two so the invariant
		// holds in both directions.
		total = len(data)
	}
	return QuerySeriesEnvelope{
		Data: data,
		Meta: dto.Meta{
			Total:       total,
			Truncated:   len(data) < total,
			GeneratedAt: now,
			// NextCursor stays empty: a batch has no next page (the client names
			// the set), and Failed/Loading are discovery's per-namespace
			// partial-render fields — this route reads the store, so it has no
			// namespace discovery to report on and leaves both unset.
		},
	}
}
