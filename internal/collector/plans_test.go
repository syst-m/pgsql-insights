// R3.10's plan-capture tests.
//
// The harness follows statements_test.go rather than inventing a fourth one:
// stmtTarget/stmtRow/stmtInfoRow/stmtFixtureWith and collectStatements all come
// from there, and planTx below is the one addition the (c) path forces — see its
// comment for why database_test.go's fakeTx cannot express it.

package collector

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/query"
)

// planJSONFixture is one EXPLAIN (FORMAT JSON) result cell: the shape Postgres
// returns for a sequential scan, with the estimate fields R3.11's hash is
// sensitive to. It is a constant so every test that needs a plan agrees on one.
const planJSONFixture = `[{"Plan":{"Node Type":"Seq Scan","Parent Relationship":"Root","Parallel":false,"Plan Rows":1000,"Plan Width":24,"Total Cost":108.34,"Startup Cost":0.00,"Actual Rows":0,"Relation Name":"orders","Schema":"public"}}]`

// planAnswer is one canned reply: rows, or the error to return.
type planAnswer struct {
	rows []Row
	err  error
}

// planTx is database_test.go's fakeTx with a per-query error.
//
// fakeTx has one failOn string whose error text is fixed ("fake: query failed"),
// and R3.10's (c) is classified from the error *text* — `permission denied for
// table …` — so the (c) test needs a fake that can say that and nothing else can.
// Everything not in the map answers (nil, nil), which is what a real transaction
// returns for SET LOCAL, SAVEPOINT, PREPARE and DEALLOCATE.
type planTx struct {
	answers map[string]planAnswer
	queries []string
}

func (p *planTx) Query(_ context.Context, q string) ([]Row, error) {
	p.queries = append(p.queries, q)
	a := p.answers[q]
	return a.rows, a.err
}

func (p *planTx) Commit() error   { return nil }
func (p *planTx) Rollback() error { return nil }

// planFixtureFor builds a sampler, a transaction that answers every query a
// capture of these statements issues, and the captures themselves.
//
// The captures are derived with planSQLFor rather than retyped here because
// planSQLFor's exact strings are pinned by TestPlanSQLForIsTheTwoCapturePaths;
// retyping them in every fixture would put a second copy of that contract in the
// test file, which is the failure mode this repo names for a test that restates a
// URL instead of importing it.
func planFixtureFor(t *testing.T, raws []string) (*PlanSampler, *planTx, []planCapture) {
	t.Helper()
	s := NewPlanSampler(PlanConfig{Now: func() time.Time { return fixedClock }})
	tx := &planTx{answers: map[string]planAnswer{}}
	pcs := make([]planCapture, 0, len(raws))
	for _, raw := range raws {
		pc := s.planSQLFor(raw, query.Normalize(raw, s.cfg.Normalizer))
		pcs = append(pcs, pc)
		tx.answers[pc.Explain] = planAnswer{rows: []Row{{planJSONColumn: planJSONFixture}}}
		if pc.Prepare != "" {
			tx.answers[pc.Prepare] = planAnswer{}
		}
		if pc.Deallocate != "" {
			tx.answers[pc.Deallocate] = planAnswer{}
		}
	}
	return s, tx, pcs
}

// TestPlanConfigValidAndDefaults checks R3.10's knob against itself: the range the
// requirement names (0 through the Enterprise Plus 200), the default it names (5),
// and the two resolutions withDefaults owes any config type in this package —
// zero-means-default on the low end and a clamp on the high end.
func TestPlanConfigValidAndDefaults(t *testing.T) {
	valid := map[int]bool{
		0: true, 1: true, DefaultPlansPerMinute: true,
		MaxPlansPerMinute: true, 199: true,
		-1: false, 201: false, 1000: false, -1000: false,
	}
	for n, want := range valid {
		if got := ValidPlansPerMinute(n); got != want {
			t.Errorf("ValidPlansPerMinute(%d) = %v, want %v", n, got, want)
		}
	}
	if DefaultPlansPerMinute != 5 {
		t.Errorf("DefaultPlansPerMinute = %d, want 5 (Cloud SQL's queryPlansPerMinute default)", DefaultPlansPerMinute)
	}
	if MaxPlansPerMinute != 200 {
		t.Errorf("MaxPlansPerMinute = %d, want 200 (the Enterprise Plus ceiling R3.10 names)", MaxPlansPerMinute)
	}

	// Zero resolves to the default; the ceiling clamps; anything else round-trips.
	for _, tc := range []struct{ in, want int }{{0, 5}, {-1, 5}, {7, 7}, {200, 200}, {500, 200}} {
		got := NewPlanSampler(PlanConfig{PlansPerMinute: tc.in}).Config().PlansPerMinute
		if got != tc.want {
			t.Errorf("NewPlanSampler(PlansPerMinute:%d).Config().PlansPerMinute = %d, want %d", tc.in, got, tc.want)
		}
	}

	// The clock is resolved too, and Config() reports the resolved value rather
	// than the field the caller passed.
	s := NewPlanSampler(PlanConfig{})
	if s.Config().Now == nil {
		t.Fatal("Config().Now is nil, want the resolved time.Now")
	}
	stamp := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	fixed := NewPlanSampler(PlanConfig{Now: func() time.Time { return stamp }})
	if got := fixed.Config().Now(); !got.Equal(stamp) {
		t.Errorf("Config().Now() = %v, want %v", got, stamp)
	}
}

// TestPlanPickRanksSkipsAndCaps is R3.10's (a) plus the bound, as one function:
// which rows are eligible, in what order, and how many.
func TestPlanPickRanksSkipsAndCaps(t *testing.T) {
	tgt := stmtTarget(16)

	t.Run("ranks by total_exec_time descending", func(t *testing.T) {
		rows := []Row{
			stmtRow(t, tgt, map[string]string{"query": "SELECT a FROM t1", "total_exec_time": "10"}),
			stmtRow(t, tgt, map[string]string{"query": "SELECT b FROM t2", "total_exec_time": "90"}),
			stmtRow(t, tgt, map[string]string{"query": "SELECT c FROM t3", "total_exec_time": "50"}),
		}
		idx, norms := NewPlanSampler(PlanConfig{PlansPerMinute: 5}).pick(rows)
		want := []int{1, 2, 0}
		if !reflect.DeepEqual(idx, want) {
			t.Fatalf("pick indices = %v, want %v", idx, want)
		}
		if norms[0].Template != "SELECT b FROM t2" {
			t.Errorf("first pick template = %q, want %q", norms[0].Template, "SELECT b FROM t2")
		}
	})

	t.Run("cap cuts the list to PlansPerMinute", func(t *testing.T) {
		rows := []Row{
			stmtRow(t, tgt, map[string]string{"query": "SELECT a FROM t1", "total_exec_time": "10"}),
			stmtRow(t, tgt, map[string]string{"query": "SELECT b FROM t2", "total_exec_time": "90"}),
			stmtRow(t, tgt, map[string]string{"query": "SELECT c FROM t3", "total_exec_time": "50"}),
		}
		idx, _ := NewPlanSampler(PlanConfig{PlansPerMinute: 2}).pick(rows)
		want := []int{1, 2}
		if !reflect.DeepEqual(idx, want) {
			t.Fatalf("pick indices = %v, want %v (the two highest totals; the cap is the whole point)", idx, want)
		}
	})

	t.Run("tie on total breaks on template ascending", func(t *testing.T) {
		rows := []Row{
			stmtRow(t, tgt, map[string]string{"query": "SELECT b FROM t2", "total_exec_time": "50"}),
			stmtRow(t, tgt, map[string]string{"query": "SELECT a FROM t1", "total_exec_time": "50"}),
		}
		idx, _ := NewPlanSampler(PlanConfig{PlansPerMinute: 1}).pick(rows)
		if !reflect.DeepEqual(idx, []int{1}) {
			t.Fatalf("pick indices = %v, want [1] (equal totals, so the template orders them)", idx)
		}
	})

	t.Run("skips truncated rows (R3.10's (a))", func(t *testing.T) {
		long := "SELECT " + strings.Repeat("x", 200) + " FROM t6"
		rows := []Row{stmtRow(t, tgt, map[string]string{"query": long, "total_exec_time": "1000"})}
		// Length 64 makes the stored text shorter than the template, which is
		// exactly the state R3.10's (a) refuses to EXPLAIN.
		s := NewPlanSampler(PlanConfig{PlansPerMinute: 5, Normalizer: query.Options{Length: 64}})
		if n := s.Config().Normalizer; n.Length != 64 {
			t.Fatalf("Config().Normalizer.Length = %d, want 64", n.Length)
		}
		if idx, _ := s.pick(rows); len(idx) != 0 {
			t.Fatalf("pick indices = %v, want none (a truncated template is not the statement)", idx)
		}
	})

	t.Run("skips rows with no text", func(t *testing.T) {
		rows := []Row{
			stmtRow(t, tgt, map[string]string{"query": "", "total_exec_time": "99"}),
			stmtRow(t, tgt, map[string]string{"query": "   ", "total_exec_time": "98"}),
		}
		if idx, _ := NewPlanSampler(PlanConfig{PlansPerMinute: 5}).pick(rows); len(idx) != 0 {
			t.Fatalf("pick indices = %v, want none", idx)
		}
	})

	t.Run("skips statement kinds EXPLAIN cannot take", func(t *testing.T) {
		// VACUUM, SET and the transaction control rows are the common
		// non-explainable population in pg_stat_statements. Asking one for a plan
		// is an error that would abort the capture savepoint.
		rows := []Row{
			stmtRow(t, tgt, map[string]string{"query": "VACUUM VERBOSE t7", "total_exec_time": "900"}),
			stmtRow(t, tgt, map[string]string{"query": "SET work_mem = '64MB'", "total_exec_time": "800"}),
			stmtRow(t, tgt, map[string]string{"query": "BEGIN", "total_exec_time": "700"}),
			stmtRow(t, tgt, map[string]string{"query": "CREATE INDEX ON t8 (x)", "total_exec_time": "600"}),
		}
		if idx, _ := NewPlanSampler(PlanConfig{PlansPerMinute: 5}).pick(rows); len(idx) != 0 {
			t.Fatalf("pick indices = %v, want none", idx)
		}
	})

	t.Run("empty input yields nothing and issues nothing", func(t *testing.T) {
		idx, norms := NewPlanSampler(PlanConfig{}).pick(nil)
		if len(idx) != 0 || len(norms) != 0 {
			t.Fatalf("pick(nil) = %v, %v; want two empties", idx, norms)
		}
	})
}

// TestPlanSQLForIsTheTwoCapturePaths pins R3.10's (a) and (b) as SQL text: the
// two methods, the exact statements each one sends, and the prepared-statement
// name that is the invalidation strategy.
func TestPlanSQLForIsTheTwoCapturePaths(t *testing.T) {
	s := NewPlanSampler(PlanConfig{})

	t.Run("parameterized statement gets PREPARE and EXPLAIN EXECUTE", func(t *testing.T) {
		raw := "SELECT * FROM orders WHERE id = 42"
		n := query.Normalize(raw, query.Options{})
		if n.Params != 1 {
			t.Fatalf("Normalize(%q).Params = %d, want 1 (the fixture's premise)", raw, n.Params)
		}
		name := "pgsi_" + query.Fingerprint(n.Template)[:16]
		want := planCapture{
			Method:     dto.MethodPrepareExecute,
			Prepare:    "PREPARE " + name + " AS " + n.Template,
			Explain:    "EXPLAIN (FORMAT JSON) EXECUTE " + name + "(NULL)",
			Deallocate: "DEALLOCATE " + name,
		}
		got := s.planSQLFor(raw, n)
		if got != want {
			t.Errorf("planSQLFor = %+v\n            want %+v", got, want)
		}
		if got.queryCount() != 3 {
			t.Errorf("queryCount = %d, want 3 (PREPARE, EXPLAIN EXECUTE, DEALLOCATE)", got.queryCount())
		}
		// The name is R3.4's fingerprint, not a second digest and not a counter:
		// that is what bounds the prepared-statement set to the number of distinct
		// statements instead of the number of polls.
		if !strings.HasPrefix(name, "pgsi_") || len(name) != 21 {
			t.Errorf("prepare name %q is not pgsi_ + 16 hex characters", name)
		}
	})

	t.Run("two parameters get two NULL arguments", func(t *testing.T) {
		raw := "UPDATE orders SET status = 'shipped' WHERE id = 42"
		n := query.Normalize(raw, query.Options{})
		if n.Params != 2 {
			t.Fatalf("Normalize(%q).Params = %d, want 2", raw, n.Params)
		}
		name := "pgsi_" + query.Fingerprint(n.Template)[:16]
		got := s.planSQLFor(raw, n)
		if want := "EXPLAIN (FORMAT JSON) EXECUTE " + name + "(NULL, NULL)"; got.Explain != want {
			t.Errorf("Explain = %q, want %q", got.Explain, want)
		}
	})

	t.Run("statement without parameters is EXPLAINed directly", func(t *testing.T) {
		raw := "SELECT count(*) FROM customers;"
		n := query.Normalize(raw, query.Options{})
		if n.Params != 0 {
			t.Fatalf("Normalize(%q).Params = %d, want 0 (the fixture's premise)", raw, n.Params)
		}
		got := s.planSQLFor(raw, n)
		want := planCapture{Method: dto.MethodExplain, Explain: "EXPLAIN (FORMAT JSON) SELECT count(*) FROM customers"}
		if got != want {
			t.Errorf("planSQLFor = %+v\n            want %+v", got, want)
		}
		if got.queryCount() != 1 {
			t.Errorf("queryCount = %d, want 1 (a parameterless statement has one plan; PREPARE would add two queries for nothing)", got.queryCount())
		}
	})

	t.Run("nothing in the capture path says ANALYZE", func(t *testing.T) {
		// R3.10's prohibition: EXPLAIN ANALYZE executes the statement and is
		// never run automatically. The check is textual because the only defence
		// is not spelling it.
		for _, raw := range []string{"SELECT * FROM orders WHERE id = 42", "SELECT count(*) FROM customers"} {
			pc := s.planSQLFor(raw, query.Normalize(raw, query.Options{}))
			for _, q := range []string{pc.Prepare, pc.Explain, pc.Deallocate} {
				if strings.Contains(strings.ToUpper(q), "ANALYZE") {
					t.Errorf("capture SQL %q contains ANALYZE (R3.10 forbids running it automatically)", q)
				}
			}
		}
	})
}

// TestPlanCaptureRunsTheBatchAndReturnsThePlan is the happy path for the
// parameterized method: the exact query sequence, the savepoints around it, the
// counted number, and every field of the sample the dto names.
func TestPlanCaptureRunsTheBatchAndReturnsThePlan(t *testing.T) {
	raw := "SELECT * FROM orders WHERE id = 42"
	s, tx, pcs := planFixtureFor(t, []string{raw})
	pc := pcs[0]

	got, issued, err := s.capture(context.Background(), tx, raw, query.Normalize(raw, query.Options{}))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if issued != 3 {
		t.Errorf("issued = %d, want 3 (PREPARE, EXPLAIN EXECUTE, DEALLOCATE; the savepoints are not counted)", issued)
	}
	want := []string{
		savepointSQL,
		pc.Prepare,
		pc.Explain,
		pc.Deallocate,
		releaseSavepointSQL,
	}
	if !reflect.DeepEqual(tx.queries, want) {
		t.Errorf("queries =\n%v\nwant\n%v", tx.queries, want)
	}
	if got == nil {
		t.Fatal("capture returned a nil sample with no error")
	}
	if got.Method != dto.MethodPrepareExecute {
		t.Errorf("Method = %q, want %q", got.Method, dto.MethodPrepareExecute)
	}
	if got.Availability != dto.PlanAvailable {
		t.Errorf("Availability = %q, want %q", got.Availability, dto.PlanAvailable)
	}
	if got.SQL != raw {
		t.Errorf("SQL = %q, want the first-seen raw text %q (R3.10's (a))", got.SQL, raw)
	}
	if string(got.PlanJSON) != planJSONFixture {
		t.Errorf("PlanJSON = %s, want %s", got.PlanJSON, planJSONFixture)
	}
	if got.PlanHash != planHashFor(got.PlanJSON) || len(got.PlanHash) != 64 {
		t.Errorf("PlanHash = %q, want the 64-character digest of PlanJSON", got.PlanHash)
	}
	if !got.CapturedAt.Equal(fixedClock) {
		t.Errorf("CapturedAt = %v, want the configured clock %v", got.CapturedAt, fixedClock)
	}
	if got.Message != "" {
		t.Errorf("Message = %q, want empty on an available plan", got.Message)
	}
}

// TestPlanCapturePermissionDeniedIsAStateNotAFailure is R3.10's (c): the
// pg_monitor-only case produces a sample and no error, and the savepoint it rolls
// back to is what keeps the round's transaction alive.
func TestPlanCapturePermissionDeniedIsAStateNotAFailure(t *testing.T) {
	raw := "SELECT * FROM orders WHERE id = 42"
	n := query.Normalize(raw, query.Options{})
	name := "pgsi_" + query.Fingerprint(n.Template)[:16]
	pqErr := errors.New("pq: permission denied for table orders")

	tx := &planTx{answers: map[string]planAnswer{
		"PREPARE " + name + " AS " + n.Template: {},
		// The EXPLAIN is the query that fails: the role can read
		// pg_stat_statements (R12.4's pg_monitor grant) but not `orders`.
		"EXPLAIN (FORMAT JSON) EXECUTE " + name + "(NULL)": {err: pqErr},
	}}
	s := NewPlanSampler(PlanConfig{Now: func() time.Time { return fixedClock }})

	got, issued, err := s.capture(context.Background(), tx, raw, n)
	if err != nil {
		t.Fatalf("capture returned error %v; R3.10(c) makes this a state, not a failure", err)
	}
	if got == nil {
		t.Fatal("capture returned no sample and no error")
	}
	if got.Availability != dto.PlanUnavailableTablePrivileges {
		t.Errorf("Availability = %q, want %q", got.Availability, dto.PlanUnavailableTablePrivileges)
	}
	if got.Message != pqErr.Error() {
		t.Errorf("Message = %q, want the server's own text %q", got.Message, pqErr.Error())
	}
	if len(got.PlanJSON) != 0 {
		t.Errorf("PlanJSON = %s, want empty (there is no plan to report)", got.PlanJSON)
	}
	if got.Method != dto.MethodPrepareExecute {
		t.Errorf("Method = %q, want %q (the capture that was attempted)", got.Method, dto.MethodPrepareExecute)
	}
	// Two queries were issued before the failure (PREPARE, EXPLAIN); DEALLOCATE
	// never ran, so the count is 2 and not the batch's planned 3.
	if issued != 2 {
		t.Errorf("issued = %d, want 2 (D20 counts queries issued, and DEALLOCATE was never issued)", issued)
	}
	// The rollback is the whole reason (c) is survivable: without it the round's
	// transaction is aborted and every later query — and the COMMIT — fails.
	want := []string{savepointSQL, "PREPARE " + name + " AS " + n.Template,
		"EXPLAIN (FORMAT JSON) EXECUTE " + name + "(NULL)", rollbackSavepointSQL}
	if !reflect.DeepEqual(tx.queries, want) {
		t.Errorf("queries =\n%v\nwant\n%v", tx.queries, want)
	}
	if strings.Contains(strings.Join(tx.queries, "\n"), releaseSavepointSQL) {
		t.Error("RELEASE ran after a failed capture; only ROLLBACK TO should have")
	}
}

// TestPlanCapturePropagatesOtherErrors is the fourth-case rule: a failure the
// spec does not name as a state stays an error, so the caller aborts the round
// rather than labelling a dead connection as a privileges problem.
func TestPlanCapturePropagatesOtherErrors(t *testing.T) {
	raw := "SELECT count(*) FROM customers"
	n := query.Normalize(raw, query.Options{})
	bad := errors.New("pq: connection reset by peer")
	tx := &planTx{answers: map[string]planAnswer{
		"EXPLAIN (FORMAT JSON) " + raw: {err: bad},
	}}
	s := NewPlanSampler(PlanConfig{})
	got, issued, err := s.capture(context.Background(), tx, raw, n)
	if err != bad {
		t.Fatalf("err = %v, want the underlying error %v", err, bad)
	}
	if got != nil {
		t.Errorf("sample = %+v, want nil alongside an error", got)
	}
	if issued != 1 {
		t.Errorf("issued = %d, want 1", issued)
	}
}

// TestPlanCaptureRejectsAnUnusablePlanJSON covers the case where EXPLAIN answers
// without an error but the cell is not a plan.
func TestPlanCaptureRejectsAnUnusablePlanJSON(t *testing.T) {
	raw := "SELECT count(*) FROM customers"
	n := query.Normalize(raw, query.Options{})
	for name, rows := range map[string][]Row{
		"no rows":      nil,
		"empty cell":   {{planJSONColumn: "  "}},
		"not json":     {{planJSONColumn: "Seq Scan on customers"}},
		"wrong column": {{"QUERY PLAN ": "[]"}},
	} {
		t.Run(name, func(t *testing.T) {
			tx := &planTx{answers: map[string]planAnswer{"EXPLAIN (FORMAT JSON) " + raw: {rows: rows}}}
			got, issued, err := NewPlanSampler(PlanConfig{}).capture(context.Background(), tx, raw, n)
			if err != errNoPlanJSON {
				t.Fatalf("err = %v, want errNoPlanJSON", err)
			}
			if got != nil {
				t.Errorf("sample = %+v, want nil", got)
			}
			if issued != 1 {
				t.Errorf("issued = %d, want 1", issued)
			}
		})
	}
}

// TestPlanSampleCountsTheRound is R13.1's accounting for one sampler round: the
// once-per-round SET LOCAL, the per-capture counts, and the rule that the setting
// is issued only when something in the round needs it.
func TestPlanSampleCountsTheRound(t *testing.T) {
	tgt := stmtTarget(16)

	t.Run("mixed round: one SET LOCAL plus three captures", func(t *testing.T) {
		raws := []string{
			"SELECT * FROM orders WHERE id = 42", // 1 param  -> 3 queries
			"SELECT * FROM items WHERE id = 7",   // 1 param  -> 3 queries
			"SELECT count(*) FROM customers",     // 0 params -> 1 query
		}
		s, tx, _ := planFixtureFor(t, raws)
		rows := []Row{
			stmtRow(t, tgt, map[string]string{"query": raws[0], "total_exec_time": "300"}),
			stmtRow(t, tgt, map[string]string{"query": raws[1], "total_exec_time": "200"}),
			stmtRow(t, tgt, map[string]string{"query": raws[2], "total_exec_time": "100"}),
		}
		got, trips, err := s.sample(context.Background(), tx, rows)
		if err != nil {
			t.Fatalf("sample: %v", err)
		}
		// 1 (SET LOCAL) + 3 + 3 + 1 = 8.
		if trips != 8 {
			t.Errorf("trips = %d, want 8 (1 SET LOCAL + 3 + 3 + 1; the six savepoints are not counted)", trips)
		}
		if len(got) != 3 {
			t.Fatalf("got %d plans, want 3", len(got))
		}
		for i := 0; i < 3; i++ {
			if got[i] == nil || got[i].Availability != dto.PlanAvailable {
				t.Errorf("plans[%d] = %+v, want an available plan", i, got[i])
			}
		}
		if n := countQueries(tx.queries, genericPlanSQL); n != 1 {
			t.Errorf("SET LOCAL issued %d times, want exactly 1 for the whole round", n)
		}
		if n := countQueries(tx.queries, savepointSQL); n != 3 {
			t.Errorf("SAVEPOINT issued %d times, want 3 (one per capture)", n)
		}
	})

	t.Run("a round of plain EXPLAINs does not pay for SET LOCAL", func(t *testing.T) {
		raws := []string{"SELECT count(*) FROM customers", "SELECT count(*) FROM orders"}
		s, tx, _ := planFixtureFor(t, raws)
		rows := []Row{
			stmtRow(t, tgt, map[string]string{"query": raws[0], "total_exec_time": "20"}),
			stmtRow(t, tgt, map[string]string{"query": raws[1], "total_exec_time": "10"}),
		}
		got, trips, err := s.sample(context.Background(), tx, rows)
		if err != nil {
			t.Fatalf("sample: %v", err)
		}
		if trips != 2 {
			t.Errorf("trips = %d, want 2 (one EXPLAIN per capture)", trips)
		}
		if len(got) != 2 {
			t.Fatalf("got %d plans, want 2", len(got))
		}
		if n := countQueries(tx.queries, genericPlanSQL); n != 0 {
			t.Errorf("SET LOCAL issued %d times, want 0 (no parameterized capture needs the generic plan)", n)
		}
	})

	t.Run("a round with nothing to capture issues nothing", func(t *testing.T) {
		s, tx, _ := planFixtureFor(t, nil)
		got, trips, err := s.sample(context.Background(), tx, nil)
		if err != nil || got != nil || trips != 0 {
			t.Fatalf("sample(nil) = (%v, %d, %v), want (nil, 0, nil)", got, trips, err)
		}
		if len(tx.queries) != 0 {
			t.Errorf("queries = %v, want none", tx.queries)
		}
	})

	t.Run("a nil sampler issues nothing", func(t *testing.T) {
		// The collector's default: WithPlanSampler is optional, so every method
		// has to answer for a nil receiver instead of panicking.
		var s *PlanSampler
		got, trips, err := s.sample(context.Background(), &planTx{}, []Row{
			stmtRow(t, tgt, map[string]string{"query": "SELECT count(*) FROM customers"}),
		})
		if err != nil || got != nil || trips != 0 {
			t.Fatalf("nil sampler = (%v, %d, %v), want (nil, 0, nil)", got, trips, err)
		}
	})
}

// countQueries is the test-side tally of how many times a query text was issued.
func countQueries(queries []string, q string) int {
	n := 0
	for _, got := range queries {
		if got == q {
			n++
		}
	}
	return n
}

// TestPlanHashIsTheCanonicalDigest is R3.11's hash rule as the dto states it:
// "the SHA-256 of the plan JSON's canonical compact encoding, hex-encoded".
func TestPlanHashIsTheCanonicalDigest(t *testing.T) {
	compact := json.RawMessage(planJSONFixture)
	indented := json.RawMessage(`[
    {
        "Plan": {
            "Node Type": "Seq Scan",
            "Parent Relationship": "Root",
            "Parallel": false,
            "Plan Rows": 1000,
            "Plan Width": 24,
            "Total Cost": 108.34,
            "Startup Cost": 0.00,
            "Actual Rows": 0,
            "Relation Name": "orders",
            "Schema": "public"
        }
    }
]`)
	if got, want := planHashFor(indented), planHashFor(compact); got != want {
		t.Errorf("hash(indent) = %s\nhash(compact) = %s\nwant equal (re-indenting a plan is not a plan change)", got, want)
	}
	if h := planHashFor(compact); len(h) != 64 || strings.ToLower(h) != h {
		t.Errorf("hash = %q, want 64 lowercase hex characters", h)
	}
	// The canonical form is the compact encoding, not a full JSON canonicalisation:
	// json.Compact removes whitespace and nothing else, so key order and number
	// spelling survive. `0.00` and `0.0` are one number and two byte strings, so
	// they hash apart. Recorded because R3.11's change detection inherits that
	// limit, and a reader who assumes more should find the limit here first.
	spelling := json.RawMessage(`[{"Plan":{"Node Type":"Seq Scan","Parent Relationship":"Root","Parallel":false,"Plan Rows":1000,"Plan Width":24,"Total Cost":108.34,"Startup Cost":0.0,"Actual Rows":0,"Relation Name":"orders","Schema":"public"}}]`)
	if planHashFor(spelling) == planHashFor(compact) {
		t.Error("hash(0.0) == hash(0.00): json.Compact normalises whitespace only, so these are two byte strings and must hash apart")
	}
	// A different plan hashes differently — the property R3.11's change detection
	// depends on, and the reason the compact form matters.
	other := json.RawMessage(`[{"Plan":{"Node Type":"Index Scan","Relation Name":"orders"}}]`)
	if planHashFor(other) == planHashFor(compact) {
		t.Error("two different plans hashed equal")
	}
}

// TestStatementsAttachPlansToTheirRows is the wiring: the collector's round puts
// the sampler's plans on the rows they belong to, and the round's round-trip count
// grows by exactly the sampler's number.
func TestStatementsAttachPlansToTheirRows(t *testing.T) {
	tgt := stmtTarget(16)
	raw := "SELECT * FROM orders WHERE id = 42"
	stmts := []Row{stmtRow(t, tgt, map[string]string{"query": raw, "total_exec_time": "1500"})}
	info := []Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")}

	s, planTxRows, _ := planFixtureFor(t, []string{raw})
	c, _, tx := stmtFixtureWith(t, tgt, SQLConfig{Now: func() time.Time { return fixedClock }}, stmts, info)
	// The two view queries come from the statements fixture; the capture's queries
	// come from the plan fixture. One fake transaction answers both, which is the
	// point: R3.10's capture is on the round's own transaction, not a second one.
	for q, a := range planTxRows.answers {
		tx.rows[q] = a.rows
	}
	c = c.WithPlanSampler(s)

	snaps, res, err := c.CollectStatements(context.Background(), tgt)
	if err != nil {
		t.Fatalf("CollectStatements: %v", err)
	}
	if len(snaps) != 1 || len(snaps[0].Rows) != 1 {
		t.Fatalf("got %d snapshots / %d rows, want 1 / 1", len(snaps), len(snaps[0].Rows))
	}
	row := snaps[0].Rows[0]
	if row.Plan == nil {
		t.Fatal("row.Plan is nil, want the captured plan")
	}
	if row.Plan.Method != dto.MethodPrepareExecute {
		t.Errorf("row.Plan.Method = %q, want %q", row.Plan.Method, dto.MethodPrepareExecute)
	}
	if row.Plan.SQL != raw {
		t.Errorf("row.Plan.SQL = %q, want %q", row.Plan.SQL, raw)
	}
	// 2 view queries + 1 SET LOCAL + 3 capture queries.
	if res.RoundTrips != 6 {
		t.Errorf("RoundTrips = %d, want 6 (2 view queries + 4 from the sampler)", res.RoundTrips)
	}
}

// TestStatementsWithoutASamplerIssueNoPlanQueries is the half the Plan field's
// docblock now records: a collector built the way every deployment built one
// before plans.go existed still issues exactly its two view queries and still
// leaves every row's Plan nil.
func TestStatementsWithoutASamplerIssueNoPlanQueries(t *testing.T) {
	tgt := stmtTarget(16)
	raw := "SELECT * FROM orders WHERE id = 42"
	c, _, _ := stmtFixture(t, tgt,
		[]Row{stmtRow(t, tgt, map[string]string{"query": raw, "total_exec_time": "1500"})},
		[]Row{stmtInfoRow(t, tgt, "app", "2026-11-01 00:00:00+00")})

	snaps, res, err := c.CollectStatements(context.Background(), tgt)
	if err != nil {
		t.Fatalf("CollectStatements: %v", err)
	}
	if res.RoundTrips != 2 {
		t.Errorf("RoundTrips = %d, want 2 (no sampler, so no plan queries)", res.RoundTrips)
	}
	if len(snaps) != 1 || len(snaps[0].Rows) != 1 {
		t.Fatalf("got %d snapshots / %d rows, want 1 / 1", len(snaps), len(snaps[0].Rows))
	}
	if snaps[0].Rows[0].Plan != nil {
		t.Errorf("row.Plan = %+v, want nil without a sampler", snaps[0].Rows[0].Plan)
	}
}
