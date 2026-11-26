package dto

import (
	"math"
	"testing"
)

// The fixtures for the three shapes dimensions.go declares, under the same R16.8
// rule the other four files follow ("a checked-in JSON fixture per endpoint under
// `testdata/api/`, created by the phase that first serves it" — requirements.md's
// words, quoted in full at queries_test.go:15-27). The -update flag lives in
// clusters_test.go:32 and assertCanonical/readFixture in queries_test.go:49 and
// :78; this file declares no flag of its own, for the reason queries_test.go:35-40
// gives (a second flag.Bool("update") panics the whole test binary).
const (
	databasesPath       = "../../../testdata/api/databases.json"
	usersPath           = "../../../testdata/api/users.json"
	clientAddressesPath = "../../../testdata/api/client-addresses.json"
)

func TestDatabasesFixtureIsCanonical(t *testing.T) {
	var env Envelope[DatabaseView]
	readFixture(t, databasesPath, &env)
	assertCanonical(t, databasesPath, env)
}

func TestUsersFixtureIsCanonical(t *testing.T) {
	var env Envelope[UserView]
	readFixture(t, usersPath, &env)
	assertCanonical(t, usersPath, env)
}

func TestClientAddressesFixtureIsCanonical(t *testing.T) {
	var env Envelope[ClientAddressView]
	readFixture(t, clientAddressesPath, &env)
	assertCanonical(t, clientAddressesPath, env)
}

// rollupRow is the common part of the three fixtures' rows: a dimension key and the
// LoadColumns+LockWaitMs pair R3.7 requires of every rollup. The three canonical
// tests above each pin one type; the two tests below want to run the same checks
// over all three fixtures at once, which needs the rows in one slice — so the
// fixtures are re-read here and flattened. Re-reading is not redundant with the
// canonical tests: those prove the bytes are the type's canonical encoding, these
// prove the numbers inside are consistent, which is a different question and the
// one a fixture author can get wrong while the canonical test stays green.
type rollupRow struct {
	fixture string
	key     string
	c       LoadColumns
	lock    float64
}

func dimensionRows(t *testing.T) []rollupRow {
	t.Helper()

	var rows []rollupRow

	var dbs Envelope[DatabaseView]
	readFixture(t, databasesPath, &dbs)
	for _, r := range dbs.Data {
		rows = append(rows, rollupRow{databasesPath, r.Database, r.LoadColumns, r.LockWaitMs})
	}

	var users Envelope[UserView]
	readFixture(t, usersPath, &users)

	for _, r := range users.Data {
		rows = append(rows, rollupRow{usersPath, r.UserName, r.LoadColumns, r.LockWaitMs})
	}

	var addrs Envelope[ClientAddressView]
	readFixture(t, clientAddressesPath, &addrs)
	for _, r := range addrs.Data {
		rows = append(rows, rollupRow{clientAddressesPath, r.ClientAddress, r.LoadColumns, r.LockWaitMs})
	}

	return rows
}

// TestDimensionFixtureLoadArithmetic applies the two invariants
// TestQueryFixtureLoadArithmetic (queries_test.go:287) enforces on the queries
// fixture to the three rollup fixtures: R3.2's `Load by CPU = total_exec_time −
// Load by IO wait`, and R3.3's window mean `total_exec_time / times_called`.
//
// They are LoadColumns invariants, not QueryView invariants — R3.7 says so in the
// sentence that puts LoadColumns on every rollup — so a rollup fixture whose CPU
// and I/O halves do not sum to its total is a fixture that contradicts the spec
// while its canonical test passes, which is exactly the gap the pair of tests
// closes.
func TestDimensionFixtureLoadArithmetic(t *testing.T) {
	for _, r := range dimensionRows(t) {
		t.Run(r.fixture+"/"+r.key, func(t *testing.T) {
			if got := r.c.LoadByCPUMs + r.c.LoadByIOWaitMs; math.Abs(got-r.c.TotalExecTimeMs) > 0.01 {
				t.Errorf("loadByCPUMs (%v) + loadByIOWaitMs (%v) = %v, but totalExecTimeMs is %v; "+
					"R3.2 derives CPU load as total_exec_time minus block I/O time",
					r.c.LoadByCPUMs, r.c.LoadByIOWaitMs, got, r.c.TotalExecTimeMs)
			}
			if r.c.TimesCalled > 0 {
				want := r.c.TotalExecTimeMs / float64(r.c.TimesCalled)
				if math.Abs(want-r.c.AvgExecTimeMs) > 0.05 {
					t.Errorf("avgExecTimeMs = %v, but totalExecTimeMs/timesCalled = %.4f",
						r.c.AvgExecTimeMs, want)
				}
			}
		})
	}
}

// TestDimensionFixturesPartitionLoad checks that each fixture's loadByTotalTime
// column sums to 100, and it is the *difference* between these three fixtures and
// tags.json that makes the check worth writing.
//
// Database, user and client address each partition the query population: every
// statement runs in exactly one database, under exactly one role, from exactly one
// client address (R6.4's (b) is the route that makes the third true under a
// pooler). So the percentages of a complete rollup must sum to 100, and a fixture
// that sums to 241 — which is what testdata/api/tags.json sums to, because R3.12's
// tags come from an opt-in comment and most statements carry none, so the tag rows
// are a subset of the population and not a partition — would be a fixture that
// cannot be a rollup of anything. The check is on the fixture, not on the type:
// R3.7's arithmetic lives in the data, and the type cannot express "these rows are
// all the rows there are".
func TestDimensionFixturesPartitionLoad(t *testing.T) {
	totals := map[string]float64{}
	for _, r := range dimensionRows(t) {
		totals[r.fixture] += r.c.LoadByTotalTime
	}
	if len(totals) != 3 {
		t.Fatalf("expected three fixtures, got %d", len(totals))
	}
	for fixture, sum := range totals {
		if math.Abs(sum-100) > 0.1 {
			t.Errorf("%s: loadByTotalTime sums to %.2f, not 100; database, user and client "+
				"address each partition the query population, so a complete rollup's percentages "+
				"must sum to 100 (tags.json does not, and must not — see the test docblock)", fixture, sum)
		}
	}
}

// TestDimensionFixturesCoverTheOtherFixtures is the cross-fixture check for these
// three, in the shape TestQueryFixtureClustersExistInClustersFixture
// (queries_test.go:340) sets: a dimension value that appears in one fixture has to
// exist in the rollup that groups on it, or the two fixtures are two independent
// claims about the same population and can disagree with nothing failing.
//
// The direction is deliberate. The top-N fixture is a *subset* of the population
// (R9.5's N=50 of however many statements exist), so every database in
// queries.json must appear in databases.json, while databases.json legitimately
// names databases no top-50 query ran in. The active-session fixture is a 60 s
// sample of the same population, so the same subset rule applies to its users and
// addresses.
func TestDimensionFixturesCoverTheOtherFixtures(t *testing.T) {
	var queries QueriesResponse
	readFixture(t, queriesPath, &queries)
	var active ActiveQueriesResponse
	readFixture(t, activeQueriesPath, &active)

	databases := map[string]bool{}
	var dbs Envelope[DatabaseView]
	readFixture(t, databasesPath, &dbs)
	for _, r := range dbs.Data {
		databases[r.Database] = true
	}
	users := map[string]bool{}
	var usr Envelope[UserView]
	readFixture(t, usersPath, &usr)
	for _, r := range usr.Data {
		users[r.UserName] = true
	}
	addresses := map[string]bool{}
	var addr Envelope[ClientAddressView]
	readFixture(t, clientAddressesPath, &addr)
	for _, r := range addr.Data {
		addresses[r.ClientAddress] = true
	}

	for _, row := range queries.Data {
		if !databases[row.Database] {
			t.Errorf("queries.json row %s runs in database %q, which databases.json does not list",
				row.Query, row.Database)
		}
	}
	for _, row := range active.Data {
		if !users[row.UserName] {
			t.Errorf("active-queries.json pid %d runs as user %q, which users.json does not list",
				row.Pid, row.UserName)
		}
		if !addresses[row.ClientAddress] {
			t.Errorf("active-queries.json pid %d connects from %q, which client-addresses.json does not list",
				row.Pid, row.ClientAddress)
		}
	}
}
