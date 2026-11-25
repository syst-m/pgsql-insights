// Tests for probe_presence.go — the wired ExtensionPresenceSource.
//
// The one risk in this file's subject is the tri-state, so every test here
// distinguishes the three answers rather than counting rows: `installed: true`,
// `installed: false`, and the key absent. The distinction is R3.1's ranking and
// R10.27's rule that the "not installed" screen renders only on an explicit false,
// and the only instrument that separates "the source returned a row with a nil
// pointer" from "the source returned no row" is a test that asks which one it got.
// Each assertion on the not-known state therefore checks the marshalled JSON too:
// a nil pointer that failed to carry `omitempty` would serve `installed: null`,
// which the DTO's own test suite would not catch (dto/queries_test.go byte-compares
// the fixture, not this source's output) and the SPA renders as a third state.
//
// The route-level test at the bottom is the wiring proof — that a Config with this
// source attached serves these rows rather than emptyPresenceSource's — and it is
// deliberately thin, because the paging and doc-pairing risks of the route itself
// are queries_test.go's, not this file's.

package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/collector"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// fakeProbeReader is the collector.ProbeReader double. It returns a canned answer
// and records both arguments, because half of this source's contract is which
// collector name it asks for: the settings round's inventory is keyed by it, and
// asking for anything else returns an empty inventory that looks identical to
// "never probed".
type fakeProbeReader struct {
	results []collector.ProbeResult
	ok      bool

	gotTarget collector.Target
	gotName   string
	called    int
}

func (f *fakeProbeReader) Probes(tgt collector.Target, collectorName string) ([]collector.ProbeResult, bool) {
	f.called++
	f.gotTarget = tgt
	f.gotName = collectorName
	return f.results, f.ok
}

// targetFn returns a collector.Target for any (namespace, cluster) pair, so a test
// can pin what the source does with the resolved target rather than with the
// resolution.
func targetFn(tgt collector.Target) func(namespace, cluster string) (collector.Target, bool) {
	return func(string, string) (collector.Target, bool) { return tgt, true }
}

// testTarget is the fleet entry the tests resolve to. Its namespace/cluster
// spelling deliberately differs from the ListQuery's in the tests that use it,
// which is what makes TestProbePresenceIdentityComesFromTarget mean anything.
var testTarget = collector.Target{
	Namespace: "payments",
	Cluster:   "payments-eu1",
	Pod:       "payments-eu1-0",
	Role:      "primary",
	Address:   "10.1.2.3:5432",
}

// rowJSONHasInstalled reports whether the marshalled row carries an `installed`
// key, and with what value. Every not-known assertion goes through this rather than
// comparing the struct alone, because the served shape is the contract.
func rowJSONHasInstalled(t *testing.T, row dto.ExtensionPresenceView) (present bool, value bool) {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	v, ok := m["installed"]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	if !ok {
		t.Fatalf("row %s: `installed` = %v (%T), want a bool", row.Database, v, v)
	}
	return true, b
}

// ask is the one-call form: build the source over `probes`/`target`, ask `q`, and
// fail on a non-nil error (this source's error return is documented as always nil,
// so an error here is a bug rather than a case to branch on).
func ask(t *testing.T, probes collector.ProbeReader, target func(namespace, cluster string) (collector.Target, bool), q ListQuery) []dto.ExtensionPresenceView {
	t.Helper()
	rows, err := NewProbePresenceSource(probes, target).ExtensionPresence(context.Background(), q)
	if err != nil {
		t.Fatalf("ExtensionPresence: unexpected error %v", err)
	}
	return rows
}

// ---------------------------------------------------------------------------
// the named-database path: the only two paths that may be explicit
// ---------------------------------------------------------------------------

// TestProbePresenceNamedDatabase pins the two answers a named database can get
// explicitly, and the collector name the source asks for.
func TestProbePresenceNamedDatabase(t *testing.T) {
	for _, tc := range []struct {
		name       string
		extensions []string
		want       bool
	}{
		{
			name:       "extension present",
			extensions: []string{"pg_stat_statements", "pgcrypto"},
			want:       true,
		},
		{
			name:       "extension absent",
			extensions: []string{"pgcrypto"},
			want:       false,
		},
		{
			// The state the metric plane cannot express at all: a database the
			// settings round connected to and found nothing in. R10.27's screen
			// is for this row and only this row.
			name:       "probed, no extensions at all",
			extensions: nil,
			want:       false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes := &fakeProbeReader{
				ok: true,
				results: []collector.ProbeResult{
					{Database: "ledger", Extensions: tc.extensions},
				},
			}
			q := ListQuery{Namespace: testTarget.Namespace, Cluster: testTarget.Cluster, DB: "ledger", Limit: 50}

			rows := ask(t, probes, targetFn(testTarget), q)
			if len(rows) != 1 {
				t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
			}
			got := rows[0]
			if got.Namespace != testTarget.Namespace || got.Cluster != testTarget.Cluster || got.Database != "ledger" {
				t.Errorf("identity = %s/%s/%s, want %s/%s/ledger",
					got.Namespace, got.Cluster, got.Database, testTarget.Namespace, testTarget.Cluster)
			}
			present, value := rowJSONHasInstalled(t, got)
			if !present {
				t.Fatalf("row %s: `installed` absent, want explicit %v", got.Database, tc.want)
			}
			if value != tc.want {
				t.Errorf("installed = %v, want %v", value, tc.want)
			}

			// The collector name is part of the contract A1 set: the inventory is
			// keyed by it, and a string literal that drifts from the constant
			// returns an empty inventory that reads as "never probed".
			if probes.gotName != collector.CollectorSettings {
				t.Errorf("Probes asked for collector %q, want collector.CollectorSettings (%q)",
					probes.gotName, collector.CollectorSettings)
			}
			if probes.gotTarget != testTarget {
				t.Errorf("Probes got target %+v, want %+v", probes.gotTarget, testTarget)
			}
		})
	}
}

// TestProbePresenceNamedDatabaseNotInInventory is the third state: the query names
// a database the inventory has no answer for. The row must exist (the caller asked
// about it) with `installed` absent (it was never measured).
func TestProbePresenceNamedDatabaseNotInInventory(t *testing.T) {
	probes := &fakeProbeReader{
		ok: true,
		results: []collector.ProbeResult{
			{Database: "ledger", Extensions: []string{"pg_stat_statements"}},
			{Database: "users", Extensions: nil},
		},
	}
	q := ListQuery{Namespace: testTarget.Namespace, Cluster: testTarget.Cluster, DB: "archive", Limit: 50}

	rows := ask(t, probes, targetFn(testTarget), q)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (the asked-for database): %+v", len(rows), rows)
	}
	got := rows[0]
	if got.Database != "archive" {
		t.Errorf("row database = %q, want %q", got.Database, "archive")
	}
	if got.Installed != nil {
		t.Errorf("installed = %v, want the not-known state (nil pointer)", *got.Installed)
	}
	if present, _ := rowJSONHasInstalled(t, got); present {
		t.Error("marshalled row carries `installed`, want the key absent")
	}
}

// TestProbePresenceNeverProbedIsNotKnown pins what the `ok` return is for. With
// ok=false the inventory is empty, and a source that read only the slice could not
// tell that from a round that found nothing — the difference is whether
// `installed: false` is a truthful answer, and R10.27 says it is not.
func TestProbePresenceNeverProbedIsNotKnown(t *testing.T) {
	probes := &fakeProbeReader{ok: false, results: nil}
	q := ListQuery{Namespace: testTarget.Namespace, Cluster: testTarget.Cluster, DB: "ledger", Limit: 50}

	rows := ask(t, probes, targetFn(testTarget), q)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 with `installed` absent: %+v", len(rows), rows)
	}
	if rows[0].Installed != nil {
		t.Fatalf("installed = %v, want nil: a target that has never been probed cannot be reported as not installed", *rows[0].Installed)
	}
	if probes.called != 1 {
		t.Errorf("Probes called %d times, want 1", probes.called)
	}
}

// ---------------------------------------------------------------------------
// the unnamed path: enumerate what was probed
// ---------------------------------------------------------------------------

// TestProbePresenceEnumeratesInventory covers the no-?db= case: one row per
// probed database, every one explicit, and the empty-database row dropped.
func TestProbePresenceEnumeratesInventory(t *testing.T) {
	probes := &fakeProbeReader{
		ok: true,
		results: []collector.ProbeResult{
			{Database: "ledger", Extensions: []string{"pg_stat_statements"}},
			{Database: "users", Extensions: []string{"pgcrypto"}},
			// A stored row with no database name cannot be attributed to a
			// cluster; the DTO's own docblock says so, and this is the path that
			// has to honour it.
			{Database: "", Extensions: []string{"pg_stat_statements"}},
		},
	}
	q := ListQuery{Namespace: testTarget.Namespace, Cluster: testTarget.Cluster, Limit: 50}

	rows := ask(t, probes, targetFn(testTarget), q)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (the empty-database row is not a row): %+v", len(rows), rows)
	}
	want := []struct {
		db  string
		val bool
	}{
		{"ledger", true},
		{"users", false},
	}
	for i, w := range want {
		got := rows[i]
		if got.Database != w.db {
			t.Errorf("row %d database = %q, want %q (order follows the inventory)", i, got.Database, w.db)
			continue
		}
		if present, value := rowJSONHasInstalled(t, got); !present || value != w.val {
			t.Errorf("row %d: installed present=%v value=%v, want an explicit %v", i, present, value, w.val)
		}
	}
}

// TestProbePresenceNoRoundHasNoRows is the enumerate-path counterpart of
// TestProbePresenceNeverProbedIsNotKnown: with nothing probed there is no
// database set to enumerate, so the answer is no rows. Inventing one would make
// this route a second source of database identity, which is the fleet's half of
// R3.1, not the presence report's.
func TestProbePresenceNoRoundHasNoRows(t *testing.T) {
	probes := &fakeProbeReader{ok: false, results: nil}
	q := ListQuery{Namespace: testTarget.Namespace, Cluster: testTarget.Cluster, Limit: 50}

	if rows := ask(t, probes, targetFn(testTarget), q); len(rows) != 0 {
		t.Fatalf("got %d rows, want 0: %+v", len(rows), rows)
	}
}

// TestProbePresenceEmptyInventoryWithNoDB covers the other half of the same
// branch: ok=true with an empty result list. Same answer, different reason, and
// the reason is why the branch keys on the slice rather than on `ok`.
func TestProbePresenceEmptyInventoryWithNoDB(t *testing.T) {
	probes := &fakeProbeReader{ok: true, results: nil}
	q := ListQuery{Namespace: testTarget.Namespace, Cluster: testTarget.Cluster, Limit: 50}

	if rows := ask(t, probes, targetFn(testTarget), q); len(rows) != 0 {
		t.Fatalf("got %d rows, want 0: %+v", len(rows), rows)
	}
}

// ---------------------------------------------------------------------------
// the degenerate inputs
// ---------------------------------------------------------------------------

// TestProbePresenceNilDependencies pins the constructor's degradation instead of
// a panic. Config's other sources document that an unwired field serves empty
// rather than 500ing; a half-wired Config (a target fn with no reader, or the
// reverse) has to answer the same way, because the alternative is that wiring one
// half of a pair crashes the route the other half is on.
func TestProbePresenceNilDependencies(t *testing.T) {
	t.Run("nil reader", func(t *testing.T) {
		rows := ask(t, nil, targetFn(testTarget), ListQuery{Namespace: "ns", Cluster: "cl", DB: "db"})
		if len(rows) != 0 {
			t.Fatalf("got %d rows, want 0: %+v", len(rows), rows)
		}
	})
	t.Run("nil target fn", func(t *testing.T) {
		probes := &fakeProbeReader{ok: true, results: []collector.ProbeResult{{Database: "db"}}}
		rows := ask(t, probes, nil, ListQuery{Namespace: "ns", Cluster: "cl", DB: "db"})
		if len(rows) != 0 {
			t.Fatalf("got %d rows, want 0: %+v", len(rows), rows)
		}
		if probes.called != 0 {
			t.Errorf("Probes called %d times with no way to resolve a target, want 0", probes.called)
		}
	})
}

// TestProbePresenceUnknownTarget covers the target function's own no: a
// (namespace, cluster) the fleet has no entry for.
func TestProbePresenceUnknownTarget(t *testing.T) {
	probes := &fakeProbeReader{ok: true, results: []collector.ProbeResult{{Database: "db"}}}
	target := func(string, string) (collector.Target, bool) { return collector.Target{}, false }

	rows := ask(t, probes, target, ListQuery{Namespace: "gone", Cluster: "gone-cluster", DB: "db"})
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0: %+v", len(rows), rows)
	}
	if probes.called != 0 {
		t.Errorf("Probes called %d times for an unresolvable target, want 0 (no target, nothing to ask about)", probes.called)
	}
}

// TestProbePresenceIdentityComesFromTarget pins the projection's identity half.
// The query's spelling and the fleet's spelling are deliberately different; the
// row must carry the fleet's, which is the same argument QueryView makes for
// taking namespace/cluster from the row it came from rather than from the query.
func TestProbePresenceIdentityComesFromTarget(t *testing.T) {
	probes := &fakeProbeReader{
		ok:      true,
		results: []collector.ProbeResult{{Database: "ledger", Extensions: []string{"pg_stat_statements"}}},
	}
	// A query whose pair is a normalised or partial spelling of the target's.
	q := ListQuery{Namespace: "PAYMENTS", Cluster: "payments-eu1", DB: "ledger"}

	rows := ask(t, probes, targetFn(testTarget), q)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Namespace != testTarget.Namespace {
		t.Errorf("namespace = %q, want the target's %q, not the query's %q",
			rows[0].Namespace, testTarget.Namespace, q.Namespace)
	}
	if rows[0].Cluster != testTarget.Cluster {
		t.Errorf("cluster = %q, want the target's %q", rows[0].Cluster, testTarget.Cluster)
	}
}

// ---------------------------------------------------------------------------
// wiring
// ---------------------------------------------------------------------------

// TestProbePresenceServedThroughRoute is the wiring proof: a Config with this
// source attached serves these rows. queries_test.go already pins the route's
// paging and its doc pairing, and its TestQueriesServesPresenceRows proves the
// handler copies a source's rows into the envelope; what only this test can show
// is that the probe-backed source and the route fit together without a stub in
// between.
func TestProbePresenceServedThroughRoute(t *testing.T) {
	queries := loadQueryFixture(t)
	probes := &fakeProbeReader{
		ok: true,
		results: []collector.ProbeResult{
			{Database: "ledger", Extensions: []string{"pg_stat_statements"}},
			{Database: "users", Extensions: nil},
		},
	}
	s := newQueriesServerWithPresence(t, &stubQueries{rows: queries},
		NewProbePresenceSource(probes, targetFn(testTarget)))

	env := getQueries(t, s, "/api/v1/insights/queries?namespace=payments&cluster=payments-eu1")
	if len(env.PGStatStatements) != 2 {
		t.Fatalf("served %d presence rows, want 2: %+v", len(env.PGStatStatements), env.PGStatStatements)
	}
	for i, want := range []struct {
		db  string
		val bool
	}{
		{"ledger", true},
		{"users", false},
	} {
		got := env.PGStatStatements[i]
		if got.Database != want.db {
			t.Errorf("served row %d database = %q, want %q", i, got.Database, want.db)
			continue
		}
		if present, value := rowJSONHasInstalled(t, got); !present || value != want.val {
			t.Errorf("served row %d: present=%v value=%v, want explicit %v", i, present, value, want.val)
		}
	}
}
