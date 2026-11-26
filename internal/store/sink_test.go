// sink_test.go is A3's S29 test file for sink.go: the SQLite half of
// collector.Sink. Three contracts, one per group of tests — the target gate
// Publish runs before it writes (A3's S47), the gob blob's fidelity on the way
// out and back in, and what Samples answers for the rows that are not there:
// never published, published empty, published undecodable.
package store

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector"
)

// sinkTarget is the fixture's fully-populated Target: every field set, so a test
// that publishes it and reads the row back can tell which fields became columns.
var sinkTarget = collector.Target{
	Namespace: "prod",
	Cluster:   "pg",
	Pod:       "pg-1",
	Role:      "primary",
	Address:   "10.0.0.1:5432",
	Source:    collector.SourceSQL,
	PGMajor:   16,
}

// sinkSample is one Sample with all three timestamps and a label set, at a
// sub-second precision that a lossy encoding would round away.
func sinkSample(metric string, value float64, source, ingest time.Time) collector.Sample {
	return collector.Sample{
		Metric: metric,
		Labels: map[string]string{
			"namespace": "prod", "cluster": "pg", "pod": "pg-1", "instance_role": "primary",
		},
		Value:      value,
		SourceTime: source,
		IngestTime: ingest,
	}
}

// TestStoreImplementsCollectorSink is the compile-time half of sink.go's opening
// argument ("a Go method no interface names is never checked"). The source file
// asserts it with var _ collector.Sink = (*Store)(nil); this is the same
// assertion from the test side, so a signature that drifts fails the test build
// and not only the package build.
func TestStoreImplementsCollectorSink(t *testing.T) {
	// The assertion is the declaration: assigning openMem's concrete *Store to
	// collector.Sink is what a drifting signature fails on. The old `if s == nil`
	// was SA4023's "this comparison is never true" -- the value is never nil.
	var _ collector.Sink = openMem(t, seriesNow)
}

// TestPublishValidatesTarget covers the gate in front of the write, including the
// half A3's S47 fixed: the message names both the namespace and the cluster, so
// it has to fire when either one is missing, not only when both are.
func TestPublishValidatesTarget(t *testing.T) {
	const name = "pg_stat_statements"
	tests := []struct {
		name    string
		tgt     collector.Target
		col     string
		wantErr string // "" means the publish succeeds
		wantRow bool   // whether a row must exist afterwards
	}{
		{
			name:    "a full target",
			tgt:     sinkTarget,
			col:     name,
			wantRow: true,
		},
		{
			// A cluster-scoped source (R2.1's CR status, the same shape as the kubectl
			// one) has no pod, and the sink allows it: the PK's pod column is NOT
			// NULL, not non-empty, and the empty string is that target's real value.
			name:    "a cluster-scoped target with no pod",
			tgt:     collector.Target{Namespace: "prod", Cluster: "pg", Source: collector.SourceClusterStatus},
			col:     "cr_status",
			wantRow: true,
		},
		{
			// Role, Address, Source and PGMajor are not columns of sink_current, so
			// Publish does not validate them; collector.Target.Valid() is the
			// scheduler's gate, and this file's gate is the two columns it writes.
			name:    "a target with no role, address, source or major",
			tgt:     collector.Target{Namespace: "prod", Cluster: "pg", Pod: "pg-1"},
			col:     name,
			wantRow: true,
		},
		{
			// A3's S47, the half the old `&&` fell through: a namespace and no
			// cluster. The message names both fields, so it must fire here.
			name:    "a namespace and no cluster",
			tgt:     collector.Target{Namespace: "prod", Pod: "pg-1", Source: collector.SourceSQL},
			col:     name,
			wantErr: "store: publish pg_stat_statements: target has no namespace or cluster",
		},
		{
			// The other half, which the old condition did catch.
			name:    "a cluster and no namespace",
			tgt:     collector.Target{Cluster: "pg", Pod: "pg-1", Source: collector.SourceSQL},
			col:     name,
			wantErr: "store: publish pg_stat_statements: target has no namespace or cluster",
		},
		{
			name:    "neither namespace nor cluster",
			tgt:     collector.Target{Pod: "pg-1", Source: collector.SourceSQL},
			col:     name,
			wantErr: "store: publish pg_stat_statements: target has no namespace or cluster",
		},
		{
			// The switch's order is the contract: the name is checked first, so a
			// target that is also broken reports the name.
			name:    "an empty name on a broken target reports the name",
			tgt:     collector.Target{Pod: "pg-1"},
			col:     "",
			wantErr: "store: publish: empty collector name for //pg-1",
		},
		{
			name:    "an empty name on a good target",
			tgt:     sinkTarget,
			col:     "",
			wantErr: "store: publish: empty collector name for prod/pg/pg-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openMem(t, seriesNow)
			err := s.Publish(tt.tgt, tt.col, []collector.Sample{
				sinkSample("xact_commit", 1, time.Unix(1700000000, 0), time.Unix(1700000000, 0)),
			})
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Publish = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Publish = nil, want %q", tt.wantErr)
			case tt.wantErr != "" && err.Error() != tt.wantErr:
				t.Fatalf("Publish = %v, want %q", err, tt.wantErr)
			}
			// A rejected publish must write nothing. The gate runs before the
			// statement, so there is no half-written row to find — and for A3's S47
			// case specifically, the row that used to be written had an empty
			// cluster column, which is the row this test exists to keep unwritten.
			if n := countRows(t, s, "sink_current"); tt.wantErr != "" && n != 0 {
				t.Errorf("%d rows after a rejected publish, want 0", n)
			}
			if n := countRows(t, s, "sink_current"); tt.wantErr == "" && n != 1 {
				t.Errorf("%d rows after a successful publish, want 1", n)
			}
		})
	}
}

// TestPublishWritesTheTargetColumns checks the row's columns against the Target
// it was published for, and the table's columns against the set sink.go argues
// for: the three identity fields plus the collector name, the blob, and the
// ingest stamp — and no role or address column, which would be a second place
// for a label the samples already carry.
func TestPublishWritesTheTargetColumns(t *testing.T) {
	s := openMem(t, seriesNow)
	if err := s.Publish(sinkTarget, "pg_stat_statements", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var (
		ns, cluster, pod, collectorName string
		payload                         []byte
		ingestedAt                      int64
	)
	err := s.DB().QueryRow(`
		SELECT namespace, cluster, pod, collector, payload, ingested_at
		FROM sink_current`).Scan(&ns, &cluster, &pod, &collectorName, &payload, &ingestedAt)
	if err != nil {
		t.Fatalf("read the published row: %v", err)
	}
	if ns != sinkTarget.Namespace || cluster != sinkTarget.Cluster || pod != sinkTarget.Pod {
		t.Errorf("identity columns = %q/%q/%q, want %q/%q/%q",
			ns, cluster, pod, sinkTarget.Namespace, sinkTarget.Cluster, sinkTarget.Pod)
	}
	if collectorName != "pg_stat_statements" {
		t.Errorf("collector column = %q, want pg_stat_statements", collectorName)
	}
	// openMem pins the clock, so the ingest stamp is exactly the store's now — the
	// column is written from s.now(), not from a second time source.
	if want := unix(seriesNow); ingestedAt != want {
		t.Errorf("ingested_at = %d, want %d (unix of the store's now)", ingestedAt, want)
	}
	// The blob is a gob of sinkPayload, not of a bare slice: the wrapper is the
	// shape the doc commits to so a future field does not make readers guess.
	var p sinkPayload
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&p); err != nil {
		t.Fatalf("the payload is not a decodable sinkPayload: %v", err)
	}
	if len(p.Samples) != 0 {
		t.Errorf("payload holds %d samples, want 0 (published empty)", len(p.Samples))
	}

	// The column set, from SQLite rather than from the Go source.
	got := columns(t, s, "sink_current")
	want := []string{"namespace", "cluster", "pod", "collector", "payload", "ingested_at"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sink_current columns = %v, want %v — role and address are deliberately "+
			"absent, every sample carries its own instance_role label", got, want)
	}
}

// columns returns a table's column names in declaration order.
func columns(t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.DB().Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    interface{}
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return out
}

// TestSampleRoundTrip is the encoding's contract: what goes in comes back, field
// for field, including the three timestamps to the nanosecond. The doc's argument
// for gob over JSON is that R13.11's Skew() has to measure the same interval
// after a read as before the write, and a float or a truncated timestamp is the
// failure this test catches.
func TestSampleRoundTrip(t *testing.T) {
	s := openMem(t, seriesNow)
	want := []collector.Sample{
		{
			Metric: "db_xact_commit_total",
			Labels: map[string]string{
				"namespace": "prod", "cluster": "pg", "pod": "pg-1",
				"instance_role": "primary", "datname": "appdb",
			},
			Value:      1234.5,
			SourceTime: time.Unix(1700000000, 123456789),
			IngestTime: time.Unix(1700000003, 987654321),
			// A view with no stats_reset (pg_locks, pg_settings) carries the zero
			// time, and the zero time has to survive the round trip as the zero time.
			StatsReset: time.Time{},
		},
		{
			Metric:     "db_blks_read_total",
			Labels:     map[string]string{"namespace": "prod", "cluster": "pg", "pod": "pg-1", "datname": "ledger"},
			Value:      -0.5, // a gauge may be negative; a uint-typed column would not survive
			SourceTime: time.Unix(1700000001, 500000000),
			IngestTime: time.Unix(1700000004, 250000000),
			StatsReset: time.Unix(1690000000, 0),
		},
		{
			Metric:     "db_backends_by_state",
			Labels:     map[string]string{"namespace": "prod", "cluster": "pg", "pod": "pg-1", "usename": "appuser"},
			Value:      7,
			SourceTime: time.Unix(1700000002, 0),
			IngestTime: time.Unix(1700000002, 250000000),
			StatsReset: time.Unix(1690000000, 1),
		},
	}
	if err := s.Publish(sinkTarget, "pg_stat_activity", want); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got := s.Samples(sinkTarget, "pg_stat_activity")

	if len(got) != len(want) {
		t.Fatalf("read back %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("sample %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
		// Skew is the value R13.11 warns on, and it is derived from two stored
		// fields — asserted separately because a pair of timestamps that each
		// round-trip cannot produce a wrong skew, and this is the check that says
		// the pair is the one that was written.
		if got[i].Skew() != want[i].Skew() {
			t.Errorf("sample %d: skew after the read = %v, want %v", i, got[i].Skew(), want[i].Skew())
		}
	}
	// HasSourceClock is the other consumer of SourceTime's zero-vs-nonzero
	// distinction; the sample that has one must still have one after the read.
	if !got[0].HasSourceClock() {
		t.Error("sample 0 lost its source clock through the round trip")
	}
}

// TestPublishReplaces covers the interface's own word — Publish *replaces* the
// stored set — and the key it replaces within: one row per
// (namespace, cluster, pod, collector), so a second collector or a second pod is
// a second row and never an overwrite of the first.
func TestPublishReplaces(t *testing.T) {
	s := openMem(t, seriesNow)
	first := []collector.Sample{
		sinkSample("a", 1, time.Unix(1700000000, 0), time.Unix(1700000000, 0)),
		sinkSample("b", 2, time.Unix(1700000000, 0), time.Unix(1700000000, 0)),
		sinkSample("c", 3, time.Unix(1700000000, 0), time.Unix(1700000000, 0)),
	}
	if err := s.Publish(sinkTarget, "pg_stat_statements", first); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if got := s.Samples(sinkTarget, "pg_stat_statements"); len(got) != 3 {
		t.Fatalf("first round stored %d samples, want 3", len(got))
	}

	// The next round's set is smaller. The old one is gone, not appended to: a
	// reader that saw three values and now sees one must not see four.
	second := []collector.Sample{
		sinkSample("a", 9, time.Unix(1700000100, 0), time.Unix(1700000100, 0)),
	}
	if err := s.Publish(sinkTarget, "pg_stat_statements", second); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	got := s.Samples(sinkTarget, "pg_stat_statements")
	if len(got) != 1 {
		t.Fatalf("after the second publish, %d samples, want 1 (replace, not append)", len(got))
	}
	if got[0].Metric != "a" || got[0].Value != 9 {
		t.Errorf("the surviving sample is %+v, want the second round's a=9", got[0])
	}
	if n := countRows(t, s, "sink_current"); n != 1 {
		t.Errorf("%d rows after two publishes of one (target, collector), want 1", n)
	}

	// A second collector on the same target is a second row, and neither read sees
	// the other's payload.
	if err := s.Publish(sinkTarget, "pg_locks", []collector.Sample{
		sinkSample("locks", 4, time.Unix(1700000100, 0), time.Unix(1700000100, 0)),
	}); err != nil {
		t.Fatalf("publish pg_locks: %v", err)
	}
	// A second pod of the same cluster, same collector: a third row.
	replica := sinkTarget
	replica.Pod = "pg-2"
	replica.Role = "replica"
	if err := s.Publish(replica, "pg_stat_statements", []collector.Sample{
		sinkSample("a", 5, time.Unix(1700000100, 0), time.Unix(1700000100, 0)),
	}); err != nil {
		t.Fatalf("publish to pg-2: %v", err)
	}
	if n := countRows(t, s, "sink_current"); n != 3 {
		t.Fatalf("%d rows, want 3 (one per (target, collector) triple)", n)
	}
	if got := s.Samples(sinkTarget, "pg_stat_statements"); len(got) != 1 || got[0].Value != 9 {
		t.Errorf("pg-1/pg_stat_statements reads %+v, want the single a=9 row — "+
			"a second pod's publish replaced the first pod's set", got)
	}
	if got := s.Samples(replica, "pg_stat_statements"); len(got) != 1 || got[0].Value != 5 {
		t.Errorf("pg-2/pg_stat_statements reads %+v, want the single a=5 row", got)
	}
	if got := s.Samples(sinkTarget, "pg_locks"); len(got) != 1 || got[0].Metric != "locks" {
		t.Errorf("pg-1/pg_locks reads %+v, want the single locks=4 row", got)
	}
	// A collector that was never published on a target that has others is empty,
	// not a neighbour's payload.
	if got := s.Samples(replica, "pg_locks"); len(got) != 0 {
		t.Errorf("pg-2/pg_locks reads %d samples, want 0", len(got))
	}
}

// TestSamplesEmptyAnswers covers the three ways a read comes back empty and the
// two shapes that distinguish them. The signature has no error return, so the
// shape of the empty is all the caller gets — and the file splits it deliberately:
// a stored empty set is non-nil, a row that is not there (or not readable) is nil.
func TestSamplesEmptyAnswers(t *testing.T) {
	t.Run("never published is nil", func(t *testing.T) {
		s := openMem(t, seriesNow)
		if got := s.Samples(sinkTarget, "pg_stat_statements"); got != nil {
			t.Errorf("Samples on an empty store = %#v, want nil", got)
		}
	})

	t.Run("published empty is an empty non-nil slice", func(t *testing.T) {
		s := openMem(t, seriesNow)
		// An empty set is stored, not skipped: a collector that succeeded and
		// produced nothing is a fact, and it is the difference between "this round
		// was empty" and "we have never run this collector" that R3.14's
		// answer-not-available-vs-zero rule is about.
		if err := s.Publish(sinkTarget, "pg_locks", []collector.Sample{}); err != nil {
			t.Fatalf("Publish(empty): %v", err)
		}
		got := s.Samples(sinkTarget, "pg_locks")
		if got == nil {
			t.Fatal("a stored empty set reads back nil — len(x) and x != nil must agree")
		}
		if len(got) != 0 {
			t.Errorf("stored empty set reads %d samples, want 0", len(got))
		}
		if n := countRows(t, s, "sink_current"); n != 1 {
			t.Errorf("%d rows, want 1 — the empty round is a row, not a no-op", n)
		}
	})

	t.Run("an empty round replaces a full one", func(t *testing.T) {
		s := openMem(t, seriesNow)
		if err := s.Publish(sinkTarget, "pg_locks", []collector.Sample{
			sinkSample("locks", 4, time.Unix(1700000000, 0), time.Unix(1700000000, 0)),
		}); err != nil {
			t.Fatalf("first publish: %v", err)
		}
		if err := s.Publish(sinkTarget, "pg_locks", nil); err != nil {
			t.Fatalf("empty publish: %v", err)
		}
		got := s.Samples(sinkTarget, "pg_locks")
		if got == nil || len(got) != 0 {
			t.Fatalf("after an empty round, Samples = %#v, want an empty non-nil slice", got)
		}
	})

	t.Run("an undecodable blob is empty", func(t *testing.T) {
		s := openMem(t, seriesNow)
		// The row exists and its payload is not a gob stream. The interface has no
		// error return, so this is indistinguishable from never-published — which
		// is the limit the doc flags for the interface's owner rather than a bug to
		// fix here. The test pins the current answer so a future change to it is a
		// deliberate one.
		_, err := s.DB().Exec(upsertSinkSQL, "prod", "pg", "pg-1", "broken", []byte("not a gob stream"), unix(seriesNow))
		if err != nil {
			t.Fatalf("insert the corrupt row: %v", err)
		}
		if got := s.Samples(sinkTarget, "broken"); got != nil {
			t.Errorf("an undecodable payload reads %#v, want nil", got)
		}
		if n := countRows(t, s, "sink_current"); n != 1 {
			t.Errorf("%d rows, want 1 — the read answers empty without deleting the row", n)
		}
	})
}
