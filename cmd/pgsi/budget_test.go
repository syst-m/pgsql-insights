package main

import (
	"context"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// r131RoundTripBudget is R13.1's number: one cluster's full 60 s round "issues
// < 40 SQL round-trips on a healthy instance". It is a named const rather than a
// literal inside the assertion so that the number R13.1 owns appears once in this
// file, is greppable by the requirement it comes from, and can be moved by a
// mutation run without editing a sentence.
const r131RoundTripBudget = 40

// budgetTx is a collector.Tx that answers every query with no rows. It counts
// nothing itself on purpose: Result.RoundTrips is the collector's own arithmetic
// — one per query it issues, per D20's ruling that the field counts queries
// issued — and a fake that added its own count would be measuring the fake.
type budgetTx struct{}

func (budgetTx) Query(context.Context, string) ([]collector.Row, error) { return nil, nil }
func (budgetTx) Commit() error                                          { return nil }
func (budgetTx) Rollback() error                                        { return nil }

// budgetOpener is a collector.SQLOpener that hands out a budgetTx and records the
// SQLConfig each Begin was called with. The recording is what makes R2.7's
// threading testable: the Registry stores collectors, not configs, so once
// newRegistry takes a SQLConfig there is no way to read it back off the registry —
// the only witness is the opener the collectors were built with. The same shape
// one package over, internal/collector/database_test.go:56's fakeOpener, records
// cfgs for the same reason.
type budgetOpener struct {
	cfgs []collector.SQLConfig
}

func (o *budgetOpener) Begin(_ context.Context, _ collector.Target, cfg collector.SQLConfig) (collector.Tx, error) {
	o.cfgs = append(o.cfgs, cfg)
	return budgetTx{}, nil
}

// r131Target is the reference instance the budget is measured against: a PG17
// primary, the matrix's current major, which is the shape R13.1's "on a healthy
// instance" describes. One target, because R13.1's budget is per cluster.
var r131Target = collector.Target{
	Namespace: "postgres",
	Cluster:   "pg",
	Pod:       "pg-1",
	Role:      "primary",
	Address:   "pg-rw.postgres.svc:5432",
	Source:    collector.SourceSQL,
	PGMajor:   17,
}

// TestSQLConfigThreadsToTheOpener is R2.7's config half, measured at the seam the
// requirement is about.
//
// R2.7's row in the Phase 2 table filed this as pending with the words "the value
// exists (DefaultStatementTimeout = 5 * time.Second, carried in SQLConfig,
// asserted by the test) but nothing reads a config yet" — the value was a constant
// in internal/collector with no path from cmd/pgsi's config to it. This test is
// that path: a registry built with a SQLConfig whose StatementTimeout is a value
// no default produces (3 s, where every default is 5 s), and an assertion that the
// opener the SQL collectors were built with was handed exactly that. A registry
// that ignored its sqlCfg argument — which is what the code did before this commit,
// with its inline `cfg := collector.SQLConfig{}` — fails here, because the opener
// would record 5 s.
func TestSQLConfigThreadsToTheOpener(t *testing.T) {
	const wantTimeout = 3 * time.Second

	opener := &budgetOpener{}
	reg, err := newRegistry((*kube.Client)(nil), opener, collector.Overrides{},
		collector.SQLConfig{StatementTimeout: wantTimeout})
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}

	// The expectation is computed, not typed: R2.7's rule is one transaction per
	// *view group*, and the registry publishes each collector's group list in
	// Descriptor.ViewGroups, so the number of transactions a round should open is
	// the sum of those lengths. The registry's ten wire collectors have eleven
	// groups between them — settings.go:167 declares two (settings and extensions)
	// and every other collector declares one — so a per-collector implementation
	// that opened one transaction per collector would record ten and fail here.
	// That is the R13.4 cardinality-check precedent applied to a count: the
	// expected figure comes from the data the code already carries.
	wantTx := 0
	for _, e := range reg.Enabled() {
		wantTx += len(e.Desc.ViewGroups)
	}

	collected := 0
	for _, e := range reg.Enabled() {
		if e.Desc.Source == collector.SourcePoolerMetrics {
			continue
		}
		if _, err := e.Col.Collect(context.Background(), r131Target); err != nil {
			t.Errorf("%s: Collect: %v", e.Desc.Name, err)
		}
		collected++
	}
	if collected == 0 {
		t.Fatal("no Postgres-wire collectors in the registry; nothing to thread a config to")
	}
	if len(opener.cfgs) != wantTx {
		t.Errorf("R2.7: %d transactions opened for %d view groups across %d collectors, want %d (one read-only transaction per view group)",
			len(opener.cfgs), wantTx, collected, wantTx)
	}
	if len(opener.cfgs) == 0 {
		t.Fatal("opener recorded no Begin calls")
	}

	// One report, not one per call: every SQL collector gets the same SQLConfig
	// from the same append, so a per-call loop would print ten identical failures
	// for one bug. The break keeps the first and the count stays in the message.
	for _, cfg := range opener.cfgs {
		if cfg.StatementTimeout != wantTimeout {
			t.Errorf("opener was begun with SQLConfig{StatementTimeout: %v} across %d Begins, want %v (R2.7: the resolved value must reach the collectors' opener)",
				cfg.StatementTimeout, len(opener.cfgs), wantTimeout)
			break
		}
	}
}

// TestPollCostBudget is R13.1's measurement: one cluster's full collection round,
// counted in SQL round-trips, against the requirement's < 40 budget.
//
// The scope is the registry's Postgres-wire collectors — the nine S2 SQL ones plus
// pooler-clients, whose SHOW CLIENTS is an S6 pooler-runtime query on 5432 rather
// than an S2 one, and which therefore counts toward the budget even though its
// source id is not SourceSQL. D20's ruling is why the scope is the wire and not
// the id: the field counts queries issued over the Postgres wire. The one
// non-wire collector is `pooler` (S5, an HTTP scrape whose fetch is a
// kube.Client.GetDirect method value); it reports 0 round-trips by construction
// and needs a live clientset to be collected at all, so it is skipped on its
// source id. The three §3.3 collectors still without a file (progress/S1,
// instance-metrics/S3, k8s-events/S11) are non-wire too and add 0 when they land,
// which is where the headroom under the budget goes.
//
// The measured figure is 23. D20's own arithmetic assumed thirteen collectors at
// three queries each (39); the registry's ten SQL collectors average 2.3 queries
// each because most of them carry two views rather than three. That is a wider
// margin than the spec's arithmetic implies, not a contradiction of it — the
// ruling is about what gets counted (queries, not BEGIN/COMMIT), and this test
// counts exactly that.
func TestPollCostBudget(t *testing.T) {
	const wantWireCollectors = 10

	opener := &budgetOpener{}
	reg, err := newRegistry((*kube.Client)(nil), opener, collector.Overrides{},
		collector.SQLConfig{StatementTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}

	total, wire := 0, 0
	for _, e := range reg.Enabled() {
		if e.Desc.Source == collector.SourcePoolerMetrics {
			continue
		}
		res, err := e.Col.Collect(context.Background(), r131Target)
		if err != nil {
			t.Errorf("%s: Collect: %v", e.Desc.Name, err)
		}
		t.Logf("%-16s round_trips=%d", e.Desc.Name, res.RoundTrips)
		total += res.RoundTrips
		wire++
	}

	if wire != wantWireCollectors {
		t.Errorf("Postgres-wire collectors registered = %d, want %d", wire, wantWireCollectors)
	}
	if total >= r131RoundTripBudget {
		t.Errorf("R13.1: one cluster round issued %d SQL round-trips, want < %d", total, r131RoundTripBudget)
	}
	t.Logf("R13.1 measured %d SQL round-trips across %d Postgres-wire collectors (budget %d)",
		total, wire, r131RoundTripBudget)
}
