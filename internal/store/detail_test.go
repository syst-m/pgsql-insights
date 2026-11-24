// detail_test.go is A3's S29 per-file test for detail.go: R10.7's Query Detail.
//
// The file has two halves and they fail for different reasons. Half is SQL — the
// three SELECTs, their WHERE clauses and their LIMIT — and its failures look like
// a wrong row count. Half is foldPairs/foldOne, pure Go over a []detailSource, and
// its failures look like a wrong number: a min that got summed, a stddev that got
// averaged instead of pooled, a queryid taken from the first row instead of the
// busiest one. The pure half gets table-driven tests with hand-built rows because
// a fixture that comes only from PutSnapshot cannot express "two pods whose
// per-pod stats differ" without also testing statements.go again.
//
// The fixture is detail.go's own, not topn_test.go's topnFixture: the plan tests
// need every row to carry a dto.PlanSampleView, and topnFixture's rows carry none.
// The numbers are the same on purpose (same three sources, same counters) so the
// two files' assertions are comparable — and the pair totals are the reason
// TestQueryDetailHeaderPair can name db1 as the header's database without
// restating the fixture.

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// detailFixture builds the store the QueryDetail tests read, and returns the
// fingerprint they ask about.
//
// The shape is the one the route is scoped by: one fingerprint, two databases,
// two pods. That is the minimum that can express every rule in this file — R10.7's
// pair selection needs two (datname, usename) pairs, the five-column plan reads
// need a plan per pair so one can be shown and one hidden, and the pod fold needs
// two pods behind one pair.
//
// The three sources and their counters are topnFixture's, so the pair totals are
// 220 for db1 (140 + 80) and 50 for db2 and the header rule has one right answer.
// The plans are what the fixture adds:
//
//	(pg-1, db1)  t0 aaa1 → t1 bbb1   a plan change, so plan_change has a row
//	(pg-2, db1)  t0 aaa2 → t1 aaa2   the same hash, so no change
//	(pg-1, db2)  t0 aaa3 → t1 bbb3   a change, in the *other* database
//
// Two of these rows are load-bearing for reasons beyond the fixture's shape.
// The third is what makes the plan tests mean something: db2 has a plan and a
// change too, so a detail page for the db1 pair that used the old
// `WHERE fingerprint = ?` reads three plans and two changes where the widened key
// gives it two and one. The second is A3's S43: a pair whose two rounds carry the
// same hash must log no change row, and while previousPlanHashSQL keyed on the
// fingerprint alone it read pg-1's aaa1 as db2's predecessor and logged
// `aaa2<-aaa1` — a change that never happened. That row is the one this fixture
// exists to make impossible, and it is why the assertion is a full set and not a
// count.
func detailFixture(t *testing.T) (*Store, time.Time, string) {
	t.Helper()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()
	t0, t1 := now.Add(-time.Hour), now.Add(-30*time.Minute)
	epoch := now.Add(-720 * time.Hour)

	for _, f := range []struct {
		src                 QuerySource
		queryid             string
		baseCalls, curCalls int64
		baseTotal, curTotal float64
		baseHash, curHash   string
	}{
		{sourceOn("pg-1", "db1"), "1111111111", 4, 14, 40, 140, "aaa1", "bbb1"},
		{sourceOn("pg-2", "db1"), "9999999999", 2, 8, 20, 80, "aaa2", "aaa2"},
		{sourceOn("pg-1", "db2"), "5555555555", 1, 5, 10, 50, "aaa3", "bbb3"},
	} {
		base := rowWithPlan(f.queryid, topnSQL, "appuser", f.baseCalls, f.baseTotal, t0, f.baseHash)
		cur := rowWithPlan(f.queryid, topnSQL, "appuser", f.curCalls, f.curTotal, t1, f.curHash)
		if err := s.PutSnapshot(ctx, snapFrom(f.src, t0, epoch, base)); err != nil {
			t.Fatalf("baseline for %s/%s: %v", f.src.Pod, f.src.Datname, err)
		}
		if err := s.PutSnapshot(ctx, snapFrom(f.src, t1, epoch, cur)); err != nil {
			t.Fatalf("second round for %s/%s: %v", f.src.Pod, f.src.Datname, err)
		}
	}
	return s, now, oneFingerprint(t, s)
}

// rowWithPlan is rowFor plus the R3.10 sample the row carries.
//
// The plan rides on the StatementRow rather than being inserted directly, because
// that is how the collector delivers it and it is the only way a test exercises
// the plan_json passthrough: the bytes written here are the bytes
// dto.PlanSampleView.PlanJSON has to hand back, through a []byte column and a
// json.RawMessage.
func rowWithPlan(queryid, sql, usename string, calls int64, totalMs float64, at time.Time, planHash string) StatementRow {
	r := rowFor(queryid, sql, usename, calls, totalMs)
	r.Plan = &dto.PlanSampleView{
		CapturedAt:   at,
		Method:       dto.MethodExplain,
		Availability: dto.PlanAvailable,
		SQL:          sql,
		PlanJSON:     json.RawMessage(`[{"Plan":{"Node Type":"Seq Scan","Relation Name":"invoices"}}]`),
		PlanHash:     planHash,
	}
	return r
}

// TestQueryDetailNotFound pins the one answer that is not a 200, and the two
// things the handler needs from it.
//
// The error must be ErrUnknownFingerprint (so api's mapping answers 404) and not a
// bare fmt.Errorf — a store that returned errors.New("not found") here gets a 503
// for a query that simply does not exist, which is the failure A3's S35 split on store.go
// exists to prevent. And the message must carry the triple, which is what api's
// doc asks a wrapping store to add: the fingerprint alone tells a caller nothing
// about which of the two namespaces it looked in.
func TestQueryDetailNotFound(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	const fp = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, err := s.QueryDetail(ctx, "prod", "pg", fp)
	if err == nil {
		t.Fatal("QueryDetail on an empty store returned no error, want not-found")
	}
	if !errors.Is(err, ErrUnknownFingerprint) {
		t.Errorf("err = %v, want it to be ErrUnknownFingerprint", err)
	}
	// The second hop of the chain: ErrUnknownFingerprint wraps
	// api.ErrQueryDetailNotFound, and it is that one the handler matches on.
	// Asserting both is the difference between testing the store's own sentinel and
	// testing the contract the route actually consumes.
	if !errors.Is(err, api.ErrQueryDetailNotFound) {
		t.Errorf("err = %v, want it to wrap api.ErrQueryDetailNotFound", err)
	}
	if msg := err.Error(); !strings.Contains(msg, fp) || !strings.Contains(msg, "in prod/pg") {
		t.Errorf("error message %q does not name the triple (fingerprint and namespace/cluster)", msg)
	}

	// A malformed fingerprint is the same answer, for the same reason: the store
	// has no row. R3.4's shape rule is enforced on the write side, so a read
	// cannot tell "bad key" from "absent key" and must not try.
	for _, bad := range []string{"", "not-a-hash", "ABC"} {
		if _, err := s.QueryDetail(ctx, "prod", "pg", bad); !errors.Is(err, ErrUnknownFingerprint) {
			t.Errorf("QueryDetail(%q) err = %v, want ErrUnknownFingerprint", bad, err)
		}
	}
}

// TestQueryDetailHeaderPair is R10.7's row-selection ruling, on both of its halves.
//
// The route's signature carries one fingerprint and R9.4's row identity is
// (cluster, db, user), so the fingerprint names several rows and A3 ruled the
// header names the highest-total_exec_ms pair, ties broken on datname then
// usename. The fixture gives db1 220 ms and db2 50 ms, so the unfiltered call has
// exactly one right answer, and the filtered calls have the answer the ?db=
// parameter names — which is the ruling's second half (a viewer who arrived
// through R10.6's `user` filter gets a header that agrees with the filter).
func TestQueryDetailHeaderPair(t *testing.T) {
	s, _, fp := detailFixture(t)
	ctx := context.Background()

	// The no-parameter case: the highest-load pair is (db1, appuser).
	d, err := s.QueryDetail(ctx, "prod", "pg", fp)
	if err != nil {
		t.Fatalf("QueryDetail: %v", err)
	}
	if d.User != "appuser" {
		t.Errorf("User = %q, want appuser", d.User)
	}
	if d.Database != "db1" {
		t.Errorf("Database = %q, want db1 (220 ms) over db2 (50 ms)", d.Database)
	}

	// The filtered cases: the parameter names the row, so the header follows it
	// even when it contradicts what the load rule would have picked.
	for _, tc := range []struct{ db, want string }{
		{"db1", "db1"},
		{"db2", "db2"},
	} {
		d, err := s.queryDetail(ctx, "prod", "pg", fp, tc.db, "")
		if err != nil {
			t.Fatalf("queryDetail(db=%s): %v", tc.db, err)
		}
		if d.Database != tc.want {
			t.Errorf("queryDetail(db=%s).Database = %q, want %q", tc.db, d.Database, tc.want)
		}
	}

	// A filter that matches nothing is a not-found, not an empty detail page: the
	// candidate set is empty, and the same 404 the missing fingerprint gets is the
	// honest answer for a missing (db, user) pair.
	if _, err := s.queryDetail(ctx, "prod", "pg", fp, "db1", "postgres"); !errors.Is(err, ErrUnknownFingerprint) {
		t.Errorf("queryDetail(user=postgres) err = %v, want ErrUnknownFingerprint", err)
	}
	if _, err := s.queryDetail(ctx, "prod", "nosuchcluster", fp, "", ""); !errors.Is(err, ErrUnknownFingerprint) {
		t.Errorf("QueryDetail(cluster=nosuchcluster) err = %v, want ErrUnknownFingerprint", err)
	}
}

// TestQueryDetailPlansAndChanges is the read-side payoff of A3's R10.7 extension.
//
// Before the widening, detailPlans was `WHERE fingerprint = ?` and a detail page
// for (db1, appuser) listed the plans belonging to every other (datname, usename)
// pair that shares the fingerprint — postgres's plans on an appuser page. The
// fixture has three plan pairs, two of them in db1 and one in db2, so the
// assertion is a set comparison: the db1 page gets db1's four samples and none of
// db2's, and the db2 page gets db2's two.
//
// Five of the PK's six columns and not six — the route has no pod parameter and
// the pair is a fold across pods — which is why the db1 page lists both pg-1's and
// pg-2's samples even though the header names neither pod. The two pods' hashes
// are distinguishable (aaa1/bbb1 vs aaa2) so the set proves both pods came through.
func TestQueryDetailPlansAndChanges(t *testing.T) {
	s, _, fp := detailFixture(t)
	ctx := context.Background()

	t1 := time.Date(2026, 11, 23, 11, 30, 0, 0, time.UTC)
	t0 := time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC)
	planJSON := json.RawMessage(`[{"Plan":{"Node Type":"Seq Scan","Relation Name":"invoices"}}]`)

	// --- the db1 pair (the unfiltered detail) ---------------------------------
	d, err := s.QueryDetail(ctx, "prod", "pg", fp)
	if err != nil {
		t.Fatalf("QueryDetail: %v", err)
	}
	if got := planHashes(d.Plans); !equalStrings(got, []string{"aaa1", "aaa2", "aaa2", "bbb1"}) {
		t.Errorf("db1 plans = %v, want [aaa1 aaa2 aaa2 bbb1] — db2's aaa3/bbb3 must not appear", got)
	}
	// One change for db1: pg-1 moved aaa1 → bbb1, pg-2 did not change. The
	// fixture's db2 change must not be here.
	if got := changeTuples(d.PlanChanges); !equalStrings(got, []string{"bbb1<-aaa1@" + rfc3339(t1)}) {
		t.Errorf("db1 plan changes = %v, want one event bbb1<-aaa1 at t1", got)
	}

	// --- the db2 pair (the filtered detail) -----------------------------------
	d2, err := s.queryDetail(ctx, "prod", "pg", fp, "db2", "")
	if err != nil {
		t.Fatalf("queryDetail(db=db2): %v", err)
	}
	// Two samples, newest first: the DESC order is checkable here because db2 has
	// one pod, so the two rows have distinct captured_at and no tie is possible.
	if len(d2.Plans) != 2 {
		t.Fatalf("db2 plans = %d rows, want 2: %+v", len(d2.Plans), d2.Plans)
	}
	if d2.Plans[0].PlanHash != "bbb3" || d2.Plans[1].PlanHash != "aaa3" {
		t.Errorf("db2 plan order = [%s %s], want [bbb3 aaa3] (captured_at DESC)",
			d2.Plans[0].PlanHash, d2.Plans[1].PlanHash)
	}
	// The timestamps ride along with the order: the second row is the t0 sample,
	// and a scan that dropped a column would shift every field after it, so the
	// hash and the instant are checked as a pair.
	if !d2.Plans[1].CapturedAt.Equal(t0) {
		t.Errorf("db2's older sample capturedAt = %v, want %v", d2.Plans[1].CapturedAt, t0)
	}
	if got := changeTuples(d2.PlanChanges); !equalStrings(got, []string{"bbb3<-aaa3@" + rfc3339(t1)}) {
		t.Errorf("db2 plan changes = %v, want one event bbb3<-aaa3 at t1", got)
	}

	// The passthrough fields, on the newest db2 sample: every column of
	// dto.PlanSampleView has to be filled from the row, and a scan list that
	// drops one silently shifts every field after it.
	p := d2.Plans[0]
	if p.Method != dto.MethodExplain || p.Availability != dto.PlanAvailable {
		t.Errorf("plan method/availability = %q/%q, want %q/%q",
			p.Method, p.Availability, dto.MethodExplain, dto.PlanAvailable)
	}
	if p.SQL != topnSQL {
		t.Errorf("plan sql = %q, want %q", p.SQL, topnSQL)
	}
	if !equalJSON(p.PlanJSON, planJSON) {
		t.Errorf("plan json = %s, want %s", p.PlanJSON, planJSON)
	}
	if !p.CapturedAt.Equal(t1) {
		t.Errorf("plan capturedAt = %v, want %v", p.CapturedAt, t1)
	}
}

// TestQueryDetailWindowFallback covers the branch the api's doc separates from
// not-found: the triple exists, but the requested window has no rollup rows in it.
//
// The list route would show no row at all, so there is nothing for the embedded
// QueryView to agree with, and the honest answer is the pair's cumulative counters.
// The fixture puts both snapshots 30 days back, outside the memory store's 24 h
// window, so the two branches produce different numbers and the test can tell them
// apart: the window row would report the last delta (2 calls / 20 ms) and the
// cumulative row reports 6 calls / 60 ms.
//
// It also catches the +Inf the fallback used to be able to produce. With no
// rollup rows in the window, the cluster total is 0, and 100 * 60 / 0 is +Inf —
// which encoding/json refuses to marshal, turning the 200 into a 500 after the
// headers are sent. loadColumns guards it to 0, and the marshal here is what
// proves the guard covers this path and not just the list's.
func TestQueryDetailWindowFallback(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	old := now.Add(-30 * 24 * time.Hour)
	epoch := old.Add(-time.Hour)
	if err := s.PutSnapshot(ctx, snap(old, epoch, row("1111111111", topnSQL, 4, 40))); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if err := s.PutSnapshot(ctx, snap(old.Add(time.Hour), epoch, row("1111111111", topnSQL, 6, 60))); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	fp := oneFingerprint(t, s)

	// The window has no rows, so the list is empty and the detail still answers.
	views, err := s.QueryList(ctx, api.ListQuery{})
	if err != nil {
		t.Fatalf("QueryList: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("QueryList over the default window = %d rows, want 0 (both snapshots are 30 d old)", len(views))
	}

	d, err := s.QueryDetail(ctx, "prod", "pg", fp)
	if err != nil {
		t.Fatalf("QueryDetail: %v", err)
	}
	if d.TimesCalled != 6 || d.TotalExecTimeMs != 60 {
		t.Errorf("view = %d calls / %g ms, want the cumulative 6 / 60 (the window's delta would be 2 / 20)",
			d.TimesCalled, d.TotalExecTimeMs)
	}
	if d.AvgExecTimeMs != 10 {
		t.Errorf("AvgExecTimeMs = %g, want 10", d.AvgExecTimeMs)
	}
	if d.LoadByTotalTime != 0 {
		t.Errorf("LoadByTotalTime = %g, want 0 (cluster total is 0; 100*60/0 is +Inf and json refuses it)",
			d.LoadByTotalTime)
	}
	if _, err := json.Marshal(d); err != nil {
		t.Errorf("json.Marshal(detail) = %v", err)
	}
}

// TestDetailPairLatency is R10.7's latency panel, and the three things it must not
// invent.
//
// The percentiles stay nil: pg_stat_statements has min/mean/max/stddev and no
// distribution, and dto's doc makes nil the R3.9 fallback's answer — a store that
// derived p95 from a mean and a stddev would render a number the source never
// measured. Buckets is empty and not nil, for the same reason topn.go's
// Recommendations is: the field has no omitempty and `null` is what a nil slice
// marshals to, which the UI reads as missing rather than as none. Sampled is
// false because it labels the histogram, and there is none.
//
// The zero-calls row is the marshal guard: 0/0 in Go is NaN, and encoding/json
// refuses NaN, so a panel computed from a row with no calls would 500 the request.
func TestDetailPairLatency(t *testing.T) {
	p := detailPair{
		datname: "db1", usename: "appuser", rows: 2,
		calls: 22, totalExecMs: 220, cpuMs: 200, ioWaitMs: 12,
		rowsReturned: 44, sharedHit: 900, sharedRead: 3,
		minMs: 1.5, maxMs: 41.25, stddevMs: 6.5,
		pgMajor: 16, queryid: "1111111111", display: topnSQL,
	}
	v := p.latency()

	if v.MinMs != 1.5 || v.MaxMs != 41.25 || v.StddevMs != 6.5 {
		t.Errorf("min/max/stddev = %v/%v/%v, want 1.5/41.25/6.5", v.MinMs, v.MaxMs, v.StddevMs)
	}
	if v.MeanMs != 10 {
		t.Errorf("MeanMs = %g, want 10 (220/22)", v.MeanMs)
	}
	if v.SumMs != 220 || v.Count != 22 {
		t.Errorf("SumMs/Count = %g/%d, want 220/22", v.SumMs, v.Count)
	}
	if v.Sampled {
		t.Error("Sampled = true, want false (the flag labels the histogram, and Phase 3 has none)")
	}
	if v.Buckets == nil {
		t.Error("Buckets is nil, want an empty non-nil slice (`null` is not `none`)")
	}
	if len(v.Buckets) != 0 {
		t.Errorf("Buckets = %v, want empty (R3.9's buckets are Phase 4's)", v.Buckets)
	}
	if v.P50Ms != nil || v.P95Ms != nil || v.P99Ms != nil {
		t.Errorf("percentiles = %v/%v/%v, want all nil (no distribution to take them from)",
			v.P50Ms, v.P95Ms, v.P99Ms)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(latency) = %v", err)
	}
	if !strings.Contains(string(b), `"buckets":[]`) {
		t.Errorf("marshalled latency %s does not contain \"buckets\":[]", b)
	}
	for _, key := range []string{"p50Ms", "p95Ms", "p99Ms"} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("marshalled latency contains %q, want it omitted (nil + omitempty)", key)
		}
	}

	// The zero-calls pair: mean must be 0, not NaN.
	zero := detailPair{datname: "db1", usename: "appuser", totalExecMs: 5}.latency()
	if zero.MeanMs != 0 {
		t.Errorf("MeanMs with 0 calls = %v, want 0", zero.MeanMs)
	}
	if math.IsNaN(zero.MeanMs) || math.IsInf(zero.MeanMs, 0) {
		t.Errorf("MeanMs = %v, want a marshalable number", zero.MeanMs)
	}
}

// TestDetailPairView is the field-by-field copy from detailPair to queryListRow.
//
// detailPair.view exists so the detail's embedded QueryView is built by the same
// mapping the list route uses, and the copy is where a detail-only field goes
// missing: the struct literal has fifteen fields and a dropped one (sharedRead,
// say, or firstSeen) changes the JSON without changing any SQL. The assertion is
// every field of dto.QueryView, not the interesting subset, because the bug this
// guards against is a forgotten line.
func TestDetailPairView(t *testing.T) {
	const fp = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	first := time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC)
	p := detailPair{
		datname: "db1", usename: "appuser", rows: 2,
		calls: 22, totalExecMs: 220, cpuMs: 200, ioWaitMs: 12,
		rowsReturned: 44, sharedHit: 900, sharedRead: 3,
		minMs: 1.5, maxMs: 41.25, stddevMs: 6.5,
		pgMajor: 16, queryid: "1111111111", display: topnSQL, truncated: 0,
		firstSeen: unix(first),
	}
	v := p.view("prod", "pg", fp)

	// The fields the pair carries.
	if v.Fingerprint != fp || v.QueryID != "1111111111" || v.Query != topnSQL {
		t.Errorf("identity fields = %q/%q/%q", v.Fingerprint, v.QueryID, v.Query)
	}
	if v.Database != "db1" || v.Namespace != "prod" || v.Cluster != "pg" {
		t.Errorf("scope fields = %q/%q/%q, want db1/prod/pg", v.Database, v.Namespace, v.Cluster)
	}
	if v.PGMajor != 16 {
		t.Errorf("PGMajor = %d, want 16", v.PGMajor)
	}
	if v.Truncated {
		t.Error("Truncated = true, want false (the int 0 is the untruncated encoding)")
	}
	if v.Recommendations == nil {
		t.Error("Recommendations is nil, want an empty non-nil slice")
	}
	// The load columns, with no cluster total: the pair's own numbers and a 0
	// percentage rather than the +Inf that 100*220/0 would give.
	if v.TotalExecTimeMs != 220 || v.TimesCalled != 22 {
		t.Errorf("TotalExecTimeMs/TimesCalled = %g/%d, want 220/22", v.TotalExecTimeMs, v.TimesCalled)
	}
	if v.AvgExecTimeMs != 10 || v.AvgRowsReturned != 2 {
		t.Errorf("averages = %g/%g, want 10/2", v.AvgExecTimeMs, v.AvgRowsReturned)
	}
	if v.LoadByCPUMs != 200 || v.LoadByIOWaitMs != 12 {
		t.Errorf("LoadByCPUMs/LoadByIOWaitMs = %g/%g, want 200/12", v.LoadByCPUMs, v.LoadByIOWaitMs)
	}
	if v.LoadByTotalTime != 0 || v.PctLoadByQueries != 0 {
		t.Errorf("LoadByTotalTime/PctLoadByQueries = %g/%g, want 0/0", v.LoadByTotalTime, v.PctLoadByQueries)
	}

	// The truncated encoding's other value: the column is an int, the DTO's is a
	// bool, and the only way a 1 becomes true is this line.
	truncated := p
	truncated.truncated = 1
	if !truncated.view("prod", "pg", fp).Truncated {
		t.Error("truncated=1 did not become Truncated=true")
	}
}

// TestFoldPairs is R10.7's ordering rule, applied to a hand-built row set.
//
// Three pairs, built so one fixture exercises all three comparison branches at
// once: (db2, appuser) has the highest total and must come first; (db1, appuser)
// and (db1, postgres) tie on total, so the tie-breaks decide, and they are the two
// lines a sort written from memory gets wrong (the tie order is datname then
// usename, both ascending, while the total is descending).
//
// It also pins the text fields' source. foldOne starts `best` at rows[0] and
// replaces it on a higher total, so the fixture's db1 group lists the quiet pod
// first and the busy pod second: the pair's queryid must be the busy pod's (1111…),
// and a fold that kept rows[0] answers 9999…. That is the same rule topn.go's
// correlated subquery applies to the list row, applied here to the detail's.
func TestFoldPairs(t *testing.T) {
	rows := []detailSource{
		// (db1, appuser): two pods. The quiet one is listed first so the
		// highest-load selection cannot pass by accident.
		{datname: "db1", usename: "appuser", pod: "pg-2", queryid: "9999999999", pgMajor: 16,
			calls: 2, totalExecMs: 20, cpuMs: 18, ioWaitMs: 1, rowsReturned: 4, sharedHit: 90, sharedRead: 1,
			minExecMs: 3, maxExecMs: 12, stddevExecMs: 2, display: topnSQL, firstSeen: 100},
		{datname: "db1", usename: "appuser", pod: "pg-1", queryid: "1111111111", pgMajor: 16,
			calls: 14, totalExecMs: 140, cpuMs: 130, ioWaitMs: 6, rowsReturned: 28, sharedHit: 600, sharedRead: 9,
			minExecMs: 1, maxExecMs: 40, stddevExecMs: 5, display: topnSQL, firstSeen: 90},
		// (db1, postgres): ties with (db1, appuser) on total.
		{datname: "db1", usename: "postgres", pod: "pg-1", queryid: "7777777777", pgMajor: 15,
			calls: 16, totalExecMs: 160, cpuMs: 148, ioWaitMs: 7, rowsReturned: 160, sharedHit: 40, sharedRead: 2,
			minExecMs: 2, maxExecMs: 20, stddevExecMs: 3, display: topnSQL, firstSeen: 80},
		// (db2, appuser): the highest total of the three.
		{datname: "db2", usename: "appuser", pod: "pg-1", queryid: "5555555555", pgMajor: 16,
			calls: 5, totalExecMs: 500, cpuMs: 480, ioWaitMs: 20, rowsReturned: 10, sharedHit: 300, sharedRead: 4,
			minExecMs: 8, maxExecMs: 120, stddevExecMs: 9, display: topnSQL, firstSeen: 70},
	}
	pairs := foldPairs(rows)
	if len(pairs) != 3 {
		t.Fatalf("foldPairs returned %d pairs, want 3", len(pairs))
	}

	got := [][2]string{}
	for _, p := range pairs {
		got = append(got, [2]string{p.datname, p.usename})
	}
	want := [][2]string{{"db2", "appuser"}, {"db1", "appuser"}, {"db1", "postgres"}}
	if !equalPairs(got, want) {
		t.Errorf("pair order = %v, want %v (highest total first, ties on datname then usename)", got, want)
	}

	// The fold itself, on the two-pod pair: counters sum, min/max are the
	// extremes, and the text comes from the busiest row.
	p := pairs[1]
	if p.rows != 2 {
		t.Errorf("rows = %d, want 2", p.rows)
	}
	if p.calls != 16 || p.totalExecMs != 160 || p.cpuMs != 148 || p.ioWaitMs != 7 {
		t.Errorf("summed counters = %d/%g/%g/%g, want 16/160/148/7", p.calls, p.totalExecMs, p.cpuMs, p.ioWaitMs)
	}
	if p.rowsReturned != 32 || p.sharedHit != 690 || p.sharedRead != 10 {
		t.Errorf("summed block/row counters = %g/%g/%g, want 32/690/10", p.rowsReturned, p.sharedHit, p.sharedRead)
	}
	// A min is not a sum: the two pods' mins are 3 and 1, and the pair's is 1.
	if p.minMs != 1 || p.maxMs != 40 {
		t.Errorf("min/max = %g/%g, want 1/40 (the extremes across pods, not their sum 4 and 52)", p.minMs, p.maxMs)
	}
	if p.queryid != "1111111111" || p.pgMajor != 16 {
		t.Errorf("queryid/pgMajor = %q/%d, want the busiest row's 1111111111/16 (rows[0] is 9999999999)",
			p.queryid, p.pgMajor)
	}
	if p.display != topnSQL || p.firstSeen != 90 {
		t.Errorf("display/firstSeen = %q/%d, want the busiest row's %q/90", p.display, p.firstSeen, topnSQL)
	}
}

// TestFoldOneStddev is the pooled-variance math in foldOne.
//
// Averaging per-pod stddevs is the obvious implementation and it is wrong: it
// drops the between-pod spread. The pooled form sqrt(Σ nᵢ(σᵢ² + μᵢ²)/N − μ²) keeps
// it, and the fixture that shows the difference is two pods with the same spread
// but different means — pooled 5.099, averaged 1.0. A test that only checked the
// averaged answer would pass either way, so the table names both numbers.
//
// The -1 sentinel (schema.go's encoding for "PG reported NULL stddev") is excluded
// from the pool: a row with no sample has no distribution to pool. A pair whose
// only real row has calls 0 is excluded too, because μ = total/calls is undefined
// there, and a pair with no real stddev at all reports 0 — the DTO has a float64
// where the column has -1, so "unknown" renders as 0 and that is a DTO limitation
// the store cannot fix.
func TestFoldOneStddev(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rows  []detailSource
		want  float64
		naive float64 // what averaging the per-pod stddevs would give
	}{
		{
			// Two pods, same spread, different means: 10 and 20.
			name: "pooled, not averaged",
			rows: []detailSource{
				{datname: "db1", usename: "appuser", pod: "pg-1", queryid: "1", calls: 2, totalExecMs: 20, stddevExecMs: 1},
				{datname: "db1", usename: "appuser", pod: "pg-2", queryid: "1", calls: 2, totalExecMs: 40, stddevExecMs: 1},
			},
			want:  math.Sqrt(26), // 5.0990…; averaging the two stddevs gives 1
			naive: 1,
		},
		{
			// The sentinel row contributes nothing, so the answer is the other
			// row's own stddev exactly.
			name: "the -1 sentinel is excluded",
			rows: []detailSource{
				{datname: "db1", usename: "appuser", pod: "pg-1", queryid: "1", calls: 4, totalExecMs: 40, stddevExecMs: 3},
				{datname: "db1", usename: "appuser", pod: "pg-2", queryid: "1", calls: 4, totalExecMs: 40, stddevExecMs: -1},
			},
			want:  3,
			naive: 1.5, // averaging 3 and -1, the bug the exclusion prevents
		},
		{
			name: "every row is a sentinel",
			rows: []detailSource{
				{datname: "db1", usename: "appuser", pod: "pg-1", queryid: "1", calls: 4, totalExecMs: 40, stddevExecMs: -1},
				{datname: "db1", usename: "appuser", pod: "pg-2", queryid: "1", calls: 4, totalExecMs: 40, stddevExecMs: -1},
			},
			want:  0,
			naive: -1,
		},
		{
			// calls 0 has no mean, so the row is excluded even with a real
			// stddev on it.
			name: "a row with no calls is excluded",
			rows: []detailSource{
				{datname: "db1", usename: "appuser", pod: "pg-1", queryid: "1", calls: 4, totalExecMs: 40, stddevExecMs: 3},
				{datname: "db1", usename: "appuser", pod: "pg-2", queryid: "1", calls: 0, totalExecMs: 0, stddevExecMs: 5},
			},
			want:  3,
			naive: 4,
		},
		{
			// Identical rows: the pooled value is exactly the per-row one, and
			// the v > 0 guard is what keeps float cancellation out of sqrt.
			name: "identical rows, no NaN",
			rows: []detailSource{
				{datname: "db1", usename: "appuser", pod: "pg-1", queryid: "1", calls: 4, totalExecMs: 40, stddevExecMs: 3},
				{datname: "db1", usename: "appuser", pod: "pg-2", queryid: "1", calls: 4, totalExecMs: 40, stddevExecMs: 3},
			},
			want:  3,
			naive: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := foldOne("db1", "appuser", tc.rows)
			if math.Abs(p.stddevMs-tc.want) > 1e-9 {
				t.Errorf("stddev = %.10f, want %.10f (averaging the per-pod values gives %.4f)",
					p.stddevMs, tc.want, tc.naive)
			}
			if math.IsNaN(p.stddevMs) {
				t.Error("stddev is NaN, which encoding/json refuses to marshal")
			}
		})
	}
}

// TestDetailSourcesFilter is detailSources' WHERE clause, including the two
// optional R10.7 parameters.
//
// QueryDetail's shipped signature has three parameters and the ruling adds two
// optionals that the interface does not carry yet, so the only way to exercise
// them is through queryDetail — which is what the fixture's callers do — and
// through this direct read, which is where the SQL is. The assertion is the row
// set and its order: the ORDER BY is the tie-break half of the ruling (datname,
// then usename, then pod so a group's row order is fixed for the fold), and a
// missing `s.pod` in it makes the fold's `best` selection depend on insertion
// order for a pair whose pods tie on total.
func TestDetailSourcesFilter(t *testing.T) {
	s, _, fp := detailFixture(t)
	ctx := context.Background()

	all, err := s.detailSources(ctx, "prod", "pg", fp, "", "")
	if err != nil {
		t.Fatalf("detailSources: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("detailSources = %d rows, want 3: %+v", len(all), all)
	}
	got := make([]string, 0, len(all))
	for _, r := range all {
		got = append(got, r.datname+"/"+r.usename+"/"+r.pod)
	}
	want := []string{"db1/appuser/pg-1", "db1/appuser/pg-2", "db2/appuser/pg-1"}
	if !equalStrings(got, want) {
		t.Errorf("rows = %v, want %v (ORDER BY datname, usename, pod)", got, want)
	}

	// The two optionals, and their combination.
	for _, tc := range []struct {
		db, user string
		want     []string
	}{
		{"db1", "", []string{"db1/appuser/pg-1", "db1/appuser/pg-2"}},
		{"db2", "", []string{"db2/appuser/pg-1"}},
		{"", "appuser", want},
		{"", "postgres", nil},
		{"db1", "appuser", []string{"db1/appuser/pg-1", "db1/appuser/pg-2"}},
		{"db2", "appuser", []string{"db2/appuser/pg-1"}},
		{"db1", "postgres", nil},
		{"nosuchdb", "", nil},
	} {
		rows, err := s.detailSources(ctx, "prod", "pg", fp, tc.db, tc.user)
		if err != nil {
			t.Fatalf("detailSources(db=%q, user=%q): %v", tc.db, tc.user, err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.datname+"/"+r.usename+"/"+r.pod)
		}
		if !equalStrings(got, tc.want) {
			t.Errorf("detailSources(db=%q, user=%q) = %v, want %v", tc.db, tc.user, got, tc.want)
		}
	}

	// The joined columns: the query-table fields arrive on every source row, which
	// is what proves the JOIN is not dropping rows and what fills the QueryView's
	// Query with the stored display_text — the template, not the raw statement.
	// Asserting `display == topnSQL` (A3's S44, the first draft's bug) reads the wrong
	// column: topnSQL is the raw text, which lives in first_seen_text, and the two
	// are not re-derivable from each other (schema.go's argument). The same
	// $1-not-17 pair topn_test.go asserts for the list route.
	if !strings.Contains(all[0].display, "$1") || strings.Contains(all[0].display, "17") {
		t.Errorf("joined display_text = %q, want the normalised template (the raw %q is first_seen_text)",
			all[0].display, topnSQL)
	}
	if all[0].truncated != 0 {
		t.Errorf("joined truncated = %d, want 0", all[0].truncated)
	}
	if all[0].pgMajor != 16 || all[0].queryid != "1111111111" {
		t.Errorf("joined source fields = pgmajor %d, queryid %q; want 16, 1111111111",
			all[0].pgMajor, all[0].queryid)
	}
}

// TestDetailHistoryLimit is detailHistoryLimit's cap on both inline lists.
//
// R8.2 gives the plan list its own paged route, so the inline copy is the page's
// summary: a fingerprint sampled every round for the full 7 d window has ~10 000
// plan rows and inlining them turns R10.14's page budget into a download. Twenty
// is the const, and the test puts twenty-one rows in and asks for twenty — the
// newest twenty, with the oldest one dropped. Both lists get the check from one
// fixture because the two lists share a page and share the const.
func TestDetailHistoryLimit(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := openMem(t, now)
	ctx := context.Background()

	// One real snapshot to create the query row the plan tables' FK requires;
	// the 21 samples go in as direct SQL so the count is the test's, not the
	// collector's.
	if err := s.PutSnapshot(ctx, snap(now.Add(-time.Hour), now.Add(-720*time.Hour),
		row("1111111111", topnSQL, 4, 40))); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	fp := oneFingerprint(t, s)

	const n = 21
	for i := 0; i < n; i++ {
		ts := now.Add(-time.Duration(n-i) * time.Minute).Unix()
		hash := "h" + string(rune('a'+i))
		if _, err := s.DB().Exec(`
			INSERT INTO plan_sample
				(fingerprint, namespace, cluster, pod, datname, usename, captured_at,
				 method, availability, sql, plan_json, plan_hash, message)
			VALUES (?, 'prod', 'pg', 'pg-1', 'appdb', 'appuser', ?, 'explain', 'available', ?, '', ?, '')`,
			fp, ts, topnSQL, hash); err != nil {
			t.Fatalf("insert plan_sample %s: %v", hash, err)
		}
		if _, err := s.DB().Exec(`
			INSERT INTO plan_change
				(fingerprint, namespace, cluster, pod, datname, usename, detected_at,
				 previous_plan_hash, current_plan_hash)
			VALUES (?, 'prod', 'pg', 'pg-1', 'appdb', 'appuser', ?, 'prev', ?)`,
			fp, ts, hash); err != nil {
			t.Fatalf("insert plan_change %s: %v", hash, err)
		}
	}

	plans, err := s.detailPlans(ctx, "prod", "pg", fp, "appdb", "appuser")
	if err != nil {
		t.Fatalf("detailPlans: %v", err)
	}
	if len(plans) != detailHistoryLimit {
		t.Fatalf("detailPlans returned %d, want %d", len(plans), detailHistoryLimit)
	}
	// The newest one is first and the oldest (ha) is the one that fell off.
	if plans[0].PlanHash != "h"+string(rune('a'+n-1)) {
		t.Errorf("newest plan = %q, want %q", plans[0].PlanHash, "h"+string(rune('a'+n-1)))
	}
	for _, p := range plans {
		if p.PlanHash == "ha" {
			t.Error("the oldest sample (ha) is present, want it cut by the LIMIT")
		}
	}

	changes, err := s.detailPlanChanges(ctx, "prod", "pg", fp, "appdb", "appuser")
	if err != nil {
		t.Fatalf("detailPlanChanges: %v", err)
	}
	if len(changes) != detailHistoryLimit {
		t.Fatalf("detailPlanChanges returned %d, want %d", len(changes), detailHistoryLimit)
	}
	if changes[0].CurrentPlanHash != "h"+string(rune('a'+n-1)) {
		t.Errorf("newest change = %q, want %q", changes[0].CurrentPlanHash, "h"+string(rune('a'+n-1)))
	}
	for _, c := range changes {
		if c.CurrentPlanHash == "ha" {
			t.Error("the oldest change (ha) is present, want it cut by the LIMIT")
		}
	}
}

// --- small assertion helpers ---------------------------------------------------

// planHashes is a plan list's hashes, sorted so a tie on captured_at (two pods
// sampled at one instant) cannot make the comparison order-sensitive.
func planHashes(views []dto.PlanSampleView) []string {
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.PlanHash)
	}
	sort.Strings(out)
	return out
}

// changeTuples is a change list as "current<-previous@detectedAt" strings, sorted
// for the same reason.
func changeTuples(views []dto.PlanChangeView) []string {
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.CurrentPlanHash+"<-"+v.PreviousPlanHash+"@"+rfc3339(v.DetectedAt))
	}
	sort.Strings(out)
	return out
}

// rfc3339 is dto's timestamp rendering (R10.21), so a test compares the string the
// API would emit rather than a time.Time whose location the assertion has to know.
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalPairs(a, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// equalJSON compares two json.RawMessage values by their bytes, after
// json.Compact, so a re-indented plan is not a difference.
func equalJSON(a, b json.RawMessage) bool {
	var buf bytes.Buffer
	if err := json.Compact(&buf, a); err != nil {
		return false
	}
	compact := buf.String()
	buf.Reset()
	if err := json.Compact(&buf, b); err != nil {
		return false
	}
	return compact == buf.String()
}
