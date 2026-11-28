package collector

// plans.go is R3.10's plan-sample capture, as a component of the
// pg_stat_statements collector rather than a collector of its own.
//
// §3.3's collector table has no `plans` row, and collector.go:288 rules that the
// set of collector names is the spec's — a name the spec does not use is a second
// vocabulary — so R3.10 cannot register a `plans` collector. It attaches to the
// collector whose rows are the population being sampled, through
// StatementsCollector.WithPlanSampler: that round already owns the transaction,
// the rows and the ranking, and a plan is captured for rows the round returned. A
// collector built without a sampler captures no plans and issues no extra queries,
// which is the state every deployment was in before this file existed — the Plan
// field's doc at statements.go:258 said the collector "always leaves it nil", and
// now says it leaves it nil unless a sampler is configured.
//
// R3.10's text (requirements.md:101) sets three constraints and one prohibition.
// All four are here, each naming its clause:
//
//   - the bound — "default 5 plans/minute/cluster, Cloud SQL's
//     queryPlansPerMinute default; Enterprise Plus allows 200 — we expose the knob
//     and default to the cheap value" → PlansPerMinute, DefaultPlansPerMinute,
//     MaxPlansPerMinute, ValidPlansPerMinute.
//   - (a) — "the stored template is normalized (`$1`,`$2`) and truncated, so
//     `EXPLAIN` of it is a syntax error — we EXPLAIN the **first-seen raw text**
//     (R3.4) and **skip truncated rows**" → pick reads the raw `query` column and
//     skips rows whose normalized form is truncated, and rows with no text at all.
//   - (b) — "for a parameterized statement we use `PREPARE` + `EXPLAIN EXECUTE` to
//     get the generic plan rather than a pet plan" → planSQLFor prepares the
//     template and executes it with NULLs, under genericPlanSQL.
//   - (c) — "on a `pg_monitor`-only deployment (R12.4) the role has statistics
//     visibility but **no `SELECT` on application tables**, so EXPLAIN returns
//     `permission denied for table …` — we report `unavailable (insufficient
//     table privileges)` as a state, not as a per-query failure" → capture
//     classifies the error and returns the dto's PlanUnavailableTablePrivileges
//     with no error.
//   - the prohibition — "`EXPLAIN ANALYZE` is **never** run automatically (it
//     executes the statement); it is available on explicit user request, and only
//     for `SELECT`s unless `--allow-analyze-write=true`" → nothing in this file
//     spells ANALYZE. The dto's MethodAnalyze is that on-request path (R8.2's API
//     surface); this capture produces MethodExplain and MethodPrepareExecute only.
//
// R13.1's budget (requirements.md:266) is why the shape is this narrow. D20's
// ruling is that the reported field counts "queries issued, not transaction
// control statements", so the savepoints around each capture are free and the
// counted queries are:
//
//	1                             SET LOCAL plan_cache_mode, once per round, and
//	                              only when the round has a parameterized capture
//	1 per zero-parameter capture  EXPLAIN (FORMAT JSON) <raw>
//	3 per parameterized capture   PREPARE + EXPLAIN EXECUTE + DEALLOCATE
//
// At the default cap of 5 the worst case is 5×3+1 = 16. The round A1 measured at
// cmd/pgsi/budget_test.go (194aaa9) is 23, so the worst case lands at 39 < 40;
// that test's fixture answers every query with no rows, so it measures 23
// unchanged — the sampler picks nothing and issues nothing.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/query"
)

// DefaultPlansPerMinute is R3.10's default — Cloud SQL's `queryPlansPerMinute`
// default of 5, which the requirement tells us to ship as ours ("we expose the
// knob and default to the cheap value").
const DefaultPlansPerMinute = 5

// MaxPlansPerMinute is the top of the range R3.10 exposes: "Enterprise Plus
// allows 200". It is a ceiling on the knob, not a target — the requirement's own
// default is the cheap value.
const MaxPlansPerMinute = 200

// planJSONColumn is the column `EXPLAIN (FORMAT JSON)` returns its plan in. The
// spelling is uppercase because Postgres created it quoted: `query plan`
// unquoted is not a legal identifier, so every major names it "QUERY PLAN" and
// the caller has to ask for it with that exact spelling.
const planJSONColumn = "QUERY PLAN"

// genericPlanSQL is R3.10's (b) made executable.
//
// Postgres plans a prepared statement with no parameters generically, and for a
// parameterized one it uses a custom plan for the first five executions before it
// will consider a generic one — so `EXPLAIN EXECUTE` of a freshly prepared
// statement returns the custom plan, which is exactly the "pet plan" R3.10 rules
// out. Forcing generic mode is what makes EXPLAIN EXECUTE return the generic plan
// on the first execution, the plan Cloud SQL reports.
//
// It is SET LOCAL and not SET: the capture runs inside the round's transaction
// (R2.7's one-per-view-group), and a session-level SET would outlive the round on
// a pooled connection and change how the application's later queries are planned.
const genericPlanSQL = "SET LOCAL plan_cache_mode = force_generic_plan"

// The three transaction-control statements that wrap one capture.
//
// They exist because of R3.10's (c). A `permission denied for table …` from
// EXPLAIN is a state the requirement wants reported, but in a real transaction
// any error aborts the transaction, so without a savepoint the first unavailable
// plan would make every later capture — and the round's COMMIT — fail with
// `current transaction is aborted`. One savepoint per capture, released on the
// success path and rolled back to on the failure path, keeps a failed capture from
// reaching the rest of the round. D20 does not count them (transaction control),
// so the isolation costs nothing against R13.1's budget.
const (
	savepointSQL         = "SAVEPOINT pgsi_plan"
	releaseSavepointSQL  = "RELEASE SAVEPOINT pgsi_plan"
	rollbackSavepointSQL = "ROLLBACK TO SAVEPOINT pgsi_plan"
)

// ValidPlansPerMinute reports whether n is a legal value for R3.10's knob.
//
// Zero is legal and means unset: R3.10's default is 5, and every config type in
// this package resolves its zero in one place rather than making callers write
// the default (SQLConfig.withDefaults, query.Options.withDefaults, ValidTopN's
// 0-means-off precedent). The ceiling is the Enterprise Plus 200 the requirement
// names, so a typo like 500 is a validation failure rather than a round that
// issues 300 queries against a 40-query budget.
func ValidPlansPerMinute(n int) bool { return n >= 0 && n <= MaxPlansPerMinute }

// PlanConfig is the sampler's configuration.
type PlanConfig struct {
	// PlansPerMinute is R3.10's knob, named after Cloud SQL's
	// `queryPlansPerMinute` because that is the value the requirement is
	// matching. It bounds one round's captures, and one round is one minute
	// (R2.1's default interval), so the per-minute and per-round readings are the
	// same number.
	PlansPerMinute int

	// Normalizer is R3.5's option set, passed through so the template this file
	// prepares is the template the store stores. WithNormalizer's doc states the
	// rule this satisfies: a deployment that sets R3.6's length on one side and
	// not the other ends up with two hashes of one statement. A zero Options is
	// R3.6's default, not an empty template — query.Normalize applies its own
	// defaults.
	Normalizer query.Options

	// Now is the clock R13.11's timestamps come from, the same one SQLConfig
	// gives the collector, so a plan and the row it belongs to are stamped by one
	// clock. Nil selects time.Now.
	Now func() time.Time
}

// withDefaults resolves every zero in one place, the way SQLConfig does.
// withDefaults resolves every zero in one place, the way SQLConfig does, and
// clamps the top the way WithTopN clamps its knob: ValidPlansPerMinute is the
// predicate a config loader calls, and a value above the ceiling is pulled back to
// MaxPlansPerMinute rather than passed through, because the R13.1 budget argument
// in this file's header is stated from the cap and only holds while the cap holds.
func (c PlanConfig) withDefaults() PlanConfig {
	switch {
	case c.PlansPerMinute <= 0:
		c.PlansPerMinute = DefaultPlansPerMinute
	case c.PlansPerMinute > MaxPlansPerMinute:
		c.PlansPerMinute = MaxPlansPerMinute
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// PlanSampler captures R3.10's plan samples for the rows a statements round
// returned.
//
// It holds no state between rounds on purpose. R3.11's plan-change detection is
// the part that has to remember a previous hash, and it lives in the store
// (PlanChangeView), not here: a sampler that cached hashes would be a second copy
// of R3.11's storage with a shorter memory.
type PlanSampler struct {
	cfg PlanConfig
}

// NewPlanSampler builds a sampler on cfg with every unset field resolved by
// withDefaults.
func NewPlanSampler(cfg PlanConfig) *PlanSampler {
	return &PlanSampler{cfg: cfg.withDefaults()}
}

// Config returns the resolved configuration, so a caller can check what a sampler
// was built with (the same reason StatementsCollector exposes its own config).
func (s *PlanSampler) Config() PlanConfig { return s.cfg }

// sample captures plans for the statement rows a round read and returns the
// plans keyed by the index into rows they came from, plus the number of counted
// SQL round-trips it issued.
//
// The key is an index rather than a fingerprint because a caller has two
// fingerprints for one row (the view's `queryid` and R3.4's normalized one) and
// R3.10 names neither as the plan's key; the index is the one identity that cannot
// disagree, and it lets the caller attach a plan to a row without re-deriving
// anything.
//
// The error is only for a failure R3.10 does not turn into a state: a query that
// fails for a reason other than a missing table privilege (the savepoint itself
// failing, a dead connection). Every per-statement outcome the spec names is in
// the returned map, and a caller that ignores the error gets a partial map —
// which is why the caller in CollectStatements treats it like any other query
// failure and aborts the round.
func (s *PlanSampler) sample(ctx context.Context, tx Tx, rows []Row) (map[int]*dto.PlanSampleView, int, error) {
	if s == nil {
		return nil, 0, nil
	}
	idx, norms := s.pick(rows)
	if len(idx) == 0 {
		return nil, 0, nil
	}

	trips := 0
	// One SET LOCAL for the whole round, and only when the round contains a
	// parameterized capture: the setting exists to make EXPLAIN EXECUTE return
	// the generic plan (R3.10's (b)), so a round of plain EXPLAINs does not need
	// it and does not pay for it.
	needsGeneric := false
	for _, n := range norms {
		if n.Params > 0 {
			needsGeneric = true
			break
		}
	}
	if needsGeneric {
		if _, err := tx.Query(ctx, genericPlanSQL); err != nil {
			return nil, 1, err
		}
		trips++
	}

	out := make(map[int]*dto.PlanSampleView, len(idx))
	for k, i := range idx {
		raw, ok := rows[i].Get("query")
		if !ok {
			continue
		}
		sample, n, err := s.capture(ctx, tx, raw, norms[k])
		trips += n
		if err != nil {
			return out, trips, err
		}
		if sample != nil {
			out[i] = sample
		}
	}
	return out, trips, nil
}

// pick chooses which rows get a plan this round and returns their indices
// alongside their normalized forms (which the caller needs to build the SQL, so
// normalizing once here is the cheaper contract than normalizing twice).
//
// Three rules and one ordering, all from R3.10's (a) and the bound:
//
//   - a row with no text gets nothing (an autovacuum or walsender row, or our own
//     session's, has no statement to EXPLAIN);
//   - a row whose normalized form is truncated gets nothing — the requirement's
//     words are "skip truncated rows", and the flag R3.6 defines for exactly that
//     is Normalized.Truncated;
//   - a statement kind EXPLAIN cannot take gets nothing (explainable);
//   - what remains is ranked by total_exec_time descending, the same key R3.2's
//     top-N ranks on and the reason the ranking is "the expensive queries get
//     plans", with the template ascending as the tiebreak so a round is
//     reproducible rather than dependent on map or driver order;
//   - the list is cut to PlansPerMinute.
func (s *PlanSampler) pick(rows []Row) ([]int, []query.Normalized) {
	type candidate struct {
		index int
		norm  query.Normalized
		rank  float64
	}
	cands := make([]candidate, 0, len(rows))
	for i, r := range rows {
		raw, ok := r.Get("query")
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		n := query.Normalize(raw, s.cfg.Normalizer)
		if n.Truncated {
			continue
		}
		if !explainable(raw) {
			continue
		}
		// A row whose total_exec_time the driver returned unparseable ranks 0 and
		// sorts last, which is the answer firstFloat gives everywhere else in the
		// file.
		rank, _ := firstFloat(r, "total_exec_time", "total_time")
		cands = append(cands, candidate{index: i, norm: n, rank: rank})
	}

	sort.Slice(cands, func(a, b int) bool {
		if cands[a].rank != cands[b].rank {
			return cands[a].rank > cands[b].rank
		}
		return cands[a].norm.Template < cands[b].norm.Template
	})
	if len(cands) > s.cfg.PlansPerMinute {
		cands = cands[:s.cfg.PlansPerMinute]
	}

	idx := make([]int, len(cands))
	norms := make([]query.Normalized, len(cands))
	for i, c := range cands {
		idx[i], norms[i] = c.index, c.norm
	}
	return idx, norms
}

// explainable reports whether a plan can be obtained for this statement text.
//
// EXPLAIN takes SELECT, INSERT, UPDATE, DELETE, MERGE and their WITH forms (plus
// CREATE TABLE AS and REFRESH, which pg_stat_statements reports far less often
// than the DML set and which the classifier below does not name as its own
// vocabulary). The rest of what the view reports — SET, SHOW, VACUUM, ANALYZE,
// LOCK, CALL, DO, the transaction control statements the normalizer also sees —
// has no plan to capture, and asking for one is an error that would abort the
// capture savepoint and cost the round a plan it could have had.
//
// R3.10's (a) already establishes "skip the rows we cannot EXPLAIN" for truncated
// text; this is the same rule applied to statement kinds, and it reuses
// activity.go's queryTypeFrom rather than adding a second keyword classifier to
// the package.
func explainable(raw string) bool {
	switch queryTypeFrom(raw) {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "WITH":
		return true
	}
	return false
}

// planCapture is the SQL one capture runs, in the order it has to run in.
type planCapture struct {
	// Method is the dto's method value this capture produces.
	Method string
	// Prepare is the PREPARE statement, empty on the plain-EXPLAIN path.
	Prepare string
	// Explain is the EXPLAIN statement whose rows are the plan.
	Explain string
	// Deallocate is the DEALLOCATE statement, empty on the plain-EXPLAIN path.
	Deallocate string
}

// queryCount is the number of counted round-trips this capture issues, which is
// what R13.1's budget needs. The savepoints are excluded because D20 excludes
// transaction control from the count.
func (pc planCapture) queryCount() int {
	n := 1
	if pc.Prepare != "" {
		n++
	}
	if pc.Deallocate != "" {
		n++
	}
	return n
}

// planSQLFor picks R3.10's two capture paths from the statement's parameter
// count, which is Normalized.Params — the field the normalizer documents as
// existing because "R3.10 needs it".
//
// With no parameters the raw text is EXPLAINed directly (R3.10's (a): the plan's
// subject is the first-seen raw text, not the template). A parameterless
// statement has exactly one plan, so PREPARE would add two queries for nothing.
//
// With parameters the statement is prepared and executed under
// genericPlanSQL — R3.10's (b) — and what gets prepared is the *template*, not
// the raw text: the parameterized statement is the template, and preparing the
// literal instance would produce the plan for that instance, which is the pet
// plan (b) rules out. The literals are supplied back as NULL, which is the only
// value that is type-compatible with every parameter Postgres infers and the one
// that cannot bias a plan toward a value (Postgres does not use parameter values
// in a generic plan).
func (s *PlanSampler) planSQLFor(raw string, n query.Normalized) planCapture {
	if n.Params == 0 {
		return planCapture{
			Method:  dto.MethodExplain,
			Explain: "EXPLAIN (FORMAT JSON) " + strings.TrimRight(strings.TrimSpace(raw), ";"),
		}
	}
	name := prepareName(n.Template)
	args := make([]string, n.Params)
	for i := range args {
		args[i] = "NULL"
	}
	return planCapture{
		Method:     dto.MethodPrepareExecute,
		Prepare:    "PREPARE " + name + " AS " + n.Template,
		Explain:    "EXPLAIN (FORMAT JSON) EXECUTE " + name + "(" + strings.Join(args, ", ") + ")",
		Deallocate: "DEALLOCATE " + name,
	}
}

// prepareName is R3.10's prepared-statement invalidation strategy, which is the
// part of this design that has to be named rather than left to the implementation.
//
// The failure mode is unbounded growth: a poller that prepares a statement on
// every poll and never removes them leaves one prepared statement per
// (statement, poll) on the session it prepared them on, because Postgres keeps
// prepared statements per session until they are deallocated or the session ends.
// Two properties bound it here:
//
//   - the name is a pure function of the prepared text — the first 16 hex
//     characters of R3.4's fingerprint, which is already sha256(template) — so
//     re-preparing a statement the session already has replaces that name rather
//     than adding a numbered twin. The set's size is the number of distinct
//     statements captured, never the number of polls. Reusing the fingerprint
//     rather than a second digest is the repo's rule against a contract that
//     exists twice, and the name stays 21 characters, well inside NAMEDATALEN.
//   - every capture deallocates what it prepared (capture), so the steady-state
//     set is empty and a round's peak is its own batch.
//
// The transaction is the backstop: Postgres discards prepared statements created
// in a transaction that rolls back, so an aborted round cannot leave any behind
// either — which is why the failure path's ROLLBACK TO SAVEPOINT also undoes a
// PREPARE that succeeded before the failure.
func prepareName(text string) string {
	return "pgsi_" + query.Fingerprint(text)[:16]
}

// capture runs one statement's capture and returns its sample.
//
// The return is (sample, counted queries, error) with three outcomes and no
// fourth: a plan (available), R3.10's (c) state (unavailable, no error), or an
// error. The (c) case returns no error because the requirement says so — "we
// report `unavailable (insufficient table privileges)` as a state, not as a
// per-query failure" — and the caller's job is to attach a sample to a row, which
// it can do for both of the first two.
func (s *PlanSampler) capture(ctx context.Context, tx Tx, raw string, n query.Normalized) (*dto.PlanSampleView, int, error) {
	pc := s.planSQLFor(raw, n)

	// issued counts the queries this capture actually sent. D20 counts "queries
	// issued, not transaction control statements", so the savepoints around the
	// batch are not counted at all and a batch that died at its second query
	// reports two, not the three its plan named — pc.queryCount() is the cost of a
	// batch that ran to the end, which is the number the header's budget table uses,
	// and this counter is the number the round-trip field needs.
	issued := 0
	run := func(q string) ([]Row, error) {
		issued++
		rows, err := tx.Query(ctx, q)
		if err == nil {
			return rows, nil
		}
		// Rolling back to the savepoint undoes whatever the capture did — including
		// a PREPARE that succeeded — without touching the round's transaction, which
		// is what keeps a failed capture from poisoning the rest of the round.
		_, _ = tx.Query(ctx, rollbackSavepointSQL)
		// The server's error travels on unrewritten: failed is the one place that
		// decides whether it is R3.10's (c) state or a real failure, and it needs the
		// original text to say so — the (c) sample's Message is the server's wording,
		// which is the only thing in the sample that names which table was unreadable.
		return nil, err
	}

	if _, err := tx.Query(ctx, savepointSQL); err != nil {
		return nil, issued, err
	}
	if pc.Prepare != "" {
		if _, err := run(pc.Prepare); err != nil {
			return s.failed(pc, raw, err, issued)
		}
	}
	rows, err := run(pc.Explain)
	if err != nil {
		return s.failed(pc, raw, err, issued)
	}
	if pc.Deallocate != "" {
		if _, err := run(pc.Deallocate); err != nil {
			return s.failed(pc, raw, err, issued)
		}
	}
	if _, err := tx.Query(ctx, releaseSavepointSQL); err != nil {
		return nil, issued, err
	}

	plan, ok := planJSONFrom(rows)
	if !ok {
		return nil, issued, errNoPlanJSON
	}
	return &dto.PlanSampleView{
		CapturedAt:   s.cfg.Now(),
		Method:       pc.Method,
		Availability: dto.PlanAvailable,
		SQL:          raw,
		PlanJSON:     plan,
		PlanHash:     planHashFor(plan),
	}, issued, nil
}

// failed turns a capture error into either R3.10's (c) sample or a plain error,
// and is the single place the two are told apart.
func (s *PlanSampler) failed(pc planCapture, raw string, err error, count int) (*dto.PlanSampleView, int, error) {
	if isPermissionDenied(err) {
		return &dto.PlanSampleView{
			CapturedAt:   s.cfg.Now(),
			Method:       pc.Method,
			Availability: dto.PlanUnavailableTablePrivileges,
			// The text is still named: R3.10's (c) is about a specific table the
			// role cannot read, so the statement was submitted and the text is the
			// only evidence of which one.
			SQL:     raw,
			Message: err.Error(),
		}, count, nil
	}
	return nil, count, err
}

// planJSONFrom pulls the plan out of EXPLAIN's result rows.
//
// EXPLAIN (FORMAT JSON) returns one row with one column, so this is a lookup
// rather than a fold; the loop is there because a multi-statement capture returns
// one row per statement and the first non-empty one is the plan.
func planJSONFrom(rows []Row) (json.RawMessage, bool) {
	for _, r := range rows {
		v, ok := r.Get(planJSONColumn)
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		b := []byte(v)
		if !json.Valid(b) {
			return nil, false
		}
		return json.RawMessage(b), true
	}
	return nil, false
}

// planHashFor is R3.11's plan hash: the SHA-256 of the plan JSON's compact
// encoding, hex.
//
// The dto's PlanHash doc names this derivation and flags its cost (an
// EXPLAIN ANALYZE capture changes hash whenever the planner's estimates move),
// which R3.11 leaves open and the dto records as an open question rather than
// this file settling it. json.Compact first so re-indenting a stored plan is not
// a plan change; a value that is not valid JSON has no compact form, so it hashes
// as-is and the caller's json.Valid check is what rejects it.
func planHashFor(plan json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, plan); err != nil {
		sum := sha256.Sum256([]byte(plan))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// errNoPlanJSON is returned when EXPLAIN answered without an error but gave no
// usable plan: no rows, an empty "QUERY PLAN" cell, or a value that is not JSON.
//
// R3.10 names exactly one unavailable state — the privileges one, (c) — and the
// dto has no generic unavailable string, so this is an error and not a state: the
// caller aborts the round rather than inventing a fourth outcome the spec does not
// name. It is a sentinel rather than an inline fmt.Errorf so the test can assert
// on the cause and not on a sentence.
var errNoPlanJSON = errorString("explain returned no plan json")

// errorString is the package's minimal error type for a sentinel. (sql.go's
// helpers return errors through fmt.Errorf, and an unexported string type is the
// narrowest thing that gives capture a value to compare against.)
type errorString string

func (e errorString) Error() string { return string(e) }

// isPermissionDenied classifies R3.10's (c) out of an error.
//
// Postgres reports a missing table privilege as SQLSTATE 42501 with the text
// `permission denied for table orders`. lib/pq surfaces the code in a typed error
// and pgx in *pgconn.PgError, but this package's Tx interface (sql.go:128)
// returns a plain `error` with no type to match on, so the message is what is
// reachable. The string matched is Postgres's own wording — stable across the
// majors in R13.9's range, and the exact phrase R3.10 quotes in the spec — with
// the older `permission denied for relation …` spelling covered by the same
// prefix.
func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "permission denied")
}
