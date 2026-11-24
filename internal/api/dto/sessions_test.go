package dto

import (
	"reflect"
	"slices"
	"testing"
)

// The fixture for the shape sessions.go declares.
//
// R16.8's rule is one checked-in JSON fixture per endpoint under `testdata/api/`,
// created by the phase that first serves it, and the four fixtures above this one
// (clusters.json, queries.json, query-detail.json, query-plans.json, tags.json)
// are the precedent this joins. The fixture is not documentation: the canonical
// test below re-marshals the parsed struct and byte-compares it to the file, so a
// renamed tag, a reordered field or a new omitempty fails CI in the same way it
// does for the other four, and `web/src/api/types.ts` imports the same file —
// which is what makes `tsc` the drift check rather than a second hand-written
// copy of the field list.
const activeQueriesPath = "../../../testdata/api/active-queries.json"

func TestActiveQueriesFixtureIsCanonical(t *testing.T) {
	var v ActiveQueriesResponse
	readFixture(t, activeQueriesPath, &v)
	assertCanonical(t, activeQueriesPath, v)
}

// TestR41ColumnSetIsComplete is R4.1's acceptance check in the form the sibling
// TestR32ColumnSetIsComplete sets for R3.2: the requirement names its columns in
// one sentence — "the exact Cloud SQL columns: `Process ID`, `Query`, `Query
// type`, `Status`, `Session duration`, `Transaction duration`, `Query duration`,
// `Wait event type`, `Wait event`, `Database`, `Application name`, `User name`,
// `Client address`, `Action`" (requirements.md:326) — and this enumerates
// SessionView's wire properties and compares them to that sentence.
//
// The two names in the fixture that R4.1's sentence does not have are asserted
// rather than ignored, because "the spec doesn't name that" is the question this
// test exists to make expensive:
//
//	clientPort       — PG splits client_addr from client_port and Cloud SQL joins
//	                   them into one column; sessions.go:112 keeps both so the UI
//	                   formats the pair instead of the API choosing a separator.
//	backendType      — R4.6's requirement, not R4.1's column list.
//	queryid          — R4.7's link to the query detail, for the row R4.1 lists.
//	unknownWaitEvent — R4.3's per-row validation verdict.
//	sourceTime       — R4.8's second clock reading, the one that makes the three
//	                   durations checkable instead of tautological.
func TestR41ColumnSetIsComplete(t *testing.T) {
	want := []string{
		// R4.1's fourteen Cloud SQL columns, in the order requirements.md:326 lists them.
		"pid", "query", "queryType", "status",
		"sessionDurationSeconds", "transactionDurationSeconds", "queryDurationSeconds",
		"waitEventType", "waitEvent", "database", "applicationName", "userName",
		"clientAddress", "action",

		// Named by R4.3/R4.6/R4.7/R4.8 rather than by R4.1's sentence, each with a
		// requirement id behind it and each asserted so a deletion names a rule.
		"clientPort", "backendType", "queryid", "unknownWaitEvent", "sourceTime",

		// R4.4's edge list, which is the field R10.8's row expansion reads before
		// it asks for a chain.
		"blockingPids",
	}
	got := jsonKeys(reflect.TypeOf(SessionView{}))
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("SessionView's wire properties do not match R4.1's column set.\ngot  %v\nwant %v", got, want)
	}
}

// TestScorecardArithmeticMatchesRows checks R4.1's scorecard against the rows it
// summarises, in the fixture, the way TestQueryFixtureLoadArithmetic checks the
// query fixture's load columns against their inputs: the scorecard is a derived
// value, so a fixture whose header contradicts its own table is a fixture that
// documents a bug.
//
// It is also the only place the top-50 rule is asserted from the API side. R4.1
// caps the list at 50 and the collector's Scorecard.Total is deliberately the
// count *before* the cut (activity.go:604) so the header cannot contradict the
// table; here the fixture has three rows and no cut, so total must equal 3. A
// fixture with 60 rows and total 60 would be the stronger test, and the reason it
// is not one is that the fixture is also the UI's import — three rows is what
// makes the file readable next to the page it renders.
func TestScorecardArithmeticMatchesRows(t *testing.T) {
	var v ActiveQueriesResponse
	readFixture(t, activeQueriesPath, &v)

	// byState: recount the rows and compare the map.
	counts := map[string]int{}
	for _, s := range v.Data {
		counts[s.Status]++
	}
	if !reflect.DeepEqual(counts, v.Scorecard.ByState) {
		t.Errorf("scorecard.byState = %v, want %v recomputed from data", v.Scorecard.ByState, counts)
	}
	if v.Scorecard.Total != len(v.Data) {
		t.Errorf("scorecard.total = %d, want %d (rows in data; no top-50 cut in this fixture)",
			v.Scorecard.Total, len(v.Data))
	}

	// longestQuery must be the max by queryDurationSeconds, and
	// longestIdleInTransaction the max transactionDurationSeconds among the
	// idle-in-transaction rows — the two are separate findings in R4.1's
	// scorecard and this fixture has them in two different rows on purpose.
	var longest, longestIdle *SessionView
	for i := range v.Data {
		s := v.Data[i]
		if longest == nil || s.QueryDurationSeconds > longest.QueryDurationSeconds {
			longest = &v.Data[i]
		}
		if s.Status == "idle in transaction" &&
			(longestIdle == nil || s.TransactionDurationSeconds > longestIdle.TransactionDurationSeconds) {
			longestIdle = &v.Data[i]
		}
	}
	// DeepEqual, not ==: SessionView carries a []int (blockingPids), which makes
	// the struct non-comparable in Go's sense — a compile error, not a wrong
	// answer, and the kind of thing a field addition to a "simple" row type does.
	if v.Scorecard.LongestQuery == nil || !reflect.DeepEqual(*v.Scorecard.LongestQuery, *longest) {
		t.Errorf("scorecard.longestQuery is not the longest-running row in data (want pid %d)", longest.Pid)
	}
	if v.Scorecard.LongestIdleInTransaction == nil ||
		!reflect.DeepEqual(*v.Scorecard.LongestIdleInTransaction, *longestIdle) {
		t.Errorf("scorecard.longestIdleInTransaction is not the longest idle-in-transaction row (want pid %d)", longestIdle.Pid)
	}
}

// TestBlockingChainsMatchRowEdges is R4.4's consistency check on the fixture: a
// chain's blockers must be exactly the pids the blocked row lists in
// `blockingPids`, and every blocker in a chain must itself be a row in `data`.
//
// The second half is the clause that matters. R4.4's tree is built from
// pg_blocking_pids plus the same session set, and the collector's doc at
// activity.go:715 says the reason it carries ancestors rather than rendering
// text is that "a blocker's statement text read from a different round would be a
// statement the session may not have been running". A fixture where a chain names
// a pid that is not in `data` encodes exactly that bug, so it is asserted.
func TestBlockingChainsMatchRowEdges(t *testing.T) {
	var v ActiveQueriesResponse
	readFixture(t, activeQueriesPath, &v)

	byPid := map[int]SessionView{}
	for _, s := range v.Data {
		byPid[s.Pid] = s
	}
	for i, chain := range v.BlockingChains {
		if _, ok := byPid[chain.Blocked.Pid]; !ok {
			t.Errorf("blockingChains[%d].blocked pid %d is not a row in data", i, chain.Blocked.Pid)
			continue
		}
		if chain.Blocked.Action == "" {
			t.Errorf("blockingChains[%d].blocked.action is empty: R4.1 forbids a silently blank Action column", i)
		}
		var pids []int
		for _, b := range chain.Blockers {
			if _, ok := byPid[b.Pid]; !ok {
				t.Errorf("blockingChains[%d] names blocker pid %d, which is not a row in data", i, b.Pid)
			}
			pids = append(pids, b.Pid)
		}
		got := append([]int{}, pids...)
		slices.Sort(got)
		want := append([]int{}, chain.Blocked.BlockingPids...)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("blockingChains[%d].blockers = %v, want the blocked row's blockingPids %v", i, got, want)
		}
	}
}

// TestResponseCarriesTheEnvelopeKeys is R8.4's envelope rule applied to a body
// that is not `dto.Envelope[T]`.
//
// R8.4 states the envelope once as a generic, and this route is the one list
// endpoint that cannot use it (sessions.go:196 gives the reason: R4.1 asks for a
// scorecard above the table and the generic has exactly two fields). The rule
// still applies, so the assertion is that the four keys are the generic's two
// plus R4.1's two — `data` and `meta` present and typed as the generic's are, so
// a client that reads `data`/`meta` from this body reads the same types it reads
// from FleetEnvelope or QueriesEnvelope.
func TestResponseCarriesTheEnvelopeKeys(t *testing.T) {
	got := jsonKeys(reflect.TypeOf(ActiveQueriesResponse{}))
	for _, want := range []string{"data", "meta", "scorecard", "blockingChains"} {
		if !slices.Contains(got, want) {
			t.Errorf("ActiveQueriesResponse has no %q key", want)
		}
	}
	rt := reflect.TypeOf(ActiveQueriesResponse{})
	for _, tc := range []struct{ field, want string }{
		{"Data", "[]dto.SessionView"},
		{"Meta", "dto.Meta"},
		{"Scorecard", "dto.ScorecardView"},
		{"BlockingChains", "[]dto.BlockingChainView"},
	} {
		f, ok := rt.FieldByName(tc.field)
		if !ok {
			t.Errorf("ActiveQueriesResponse has no %s field", tc.field)
			continue
		}
		if got := f.Type.String(); got != tc.want {
			t.Errorf("ActiveQueriesResponse.%s is %s, want %s — R8.4's envelope types data as the row slice and meta as dto.Meta",
				tc.field, got, tc.want)
		}
	}
}
