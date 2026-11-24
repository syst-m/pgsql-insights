// Package-level note: this is the package's first non-SQL collector. The seven
// SQL collectors (activity, database, locks, replication, settings, statements,
// wal) all reach PostgreSQL through sql.go's SQLOpener and take their clock from
// SQLConfig.Now. The three remaining non-SQL collectors this file precedes —
// inventory (S1), instance-metrics (S3) and k8s-events (S4) — have no opener and
// no SQLConfig, so the shape this file establishes for "a collector whose source
// is not a Postgres connection" is the shape they inherit.
package collector

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/cnpgmetrics"
)

// CollectorPooler is §3.3's name for this collector, and the `collector` label
// value on both R2.2 families for its rounds.
//
// §3.3's schedule row (requirements.md:205) is one entry that names two sources:
//
//	| `pooler` | 60 s | S5 + S6 | Only for clusters with a Pooler |
//
// and requirements.md:354's two A3 rulings split that entry into two collectors:
// pooler.go (S5, this file) and pooler_clients.go (S6, CollectorPoolerClients).
// The split is by transport — an HTTP GET against the pgbouncer exporter's 9127
// versus a psql round-trip against 5432 — and this file is the HTTP half. The
// second ruling is the one that fixes the name: `pooler.go` keeps `pooler`, and
// the hyphenated `pooler-clients` goes to the other file, because
// registry.go:95 forbids two collectors registering one name and the set's only
// multi-word names are hyphenated (instance-metrics, k8s-events).
const CollectorPooler = "pooler"

// PoolerInterval is §3.3's interval for the pooler row (60 s), stated as a
// constant so R2.3's --interval.pooler override has a declared default to
// override rather than a literal buried in a struct literal.
//
// The one interval covers both pooler collectors. §3.3 gave the row a single
// interval and the split into two collectors did not add a second one, so
// pooler_clients.go uses this constant too rather than minting a PoolerClients
// interval that would let the two halves of one schedule row drift apart.
const PoolerInterval = 60 * time.Second

// MetricsFetch performs one HTTP GET against a metrics endpoint and reports the
// status line and the body.
//
// It is a function type rather than an HTTP client for the same reason
// conn.go:85's SecretExists is one: "a function type rather than a clientset so
// the rules in this file are testable with a set literal". The production
// implementation is a method value — kube.Client.GetDirect(ctx, url) has exactly
// this signature (client.go:300) — so wiring it in cmd/pgsi is `kube.GetDirect`
// with no adapter.
//
// The alternative that was rejected is importing internal/kube here. Nothing in
// internal/collector imports it today: the package's internal dependencies are
// internal/api, internal/api/dto, internal/collector/pgversion and
// internal/query, and collector.go:307 mentions kube only in a comment. A
// collector taking a func keeps the dependency a comment, which is what lets
// every rule in this file be tested without a cluster.
type MetricsFetch func(ctx context.Context, url string) (status int, body []byte, err error)

// PoolerConfig is this collector's inputs, the non-SQL counterpart of SQLConfig.
//
// SQLConfig is the wrong carrier even though it holds a Now: its fields are the
// SQL half (R2.7's transaction timeout, R2.8's application_name, R13.11's
// clock), and a collector that issues no SQL has no business setting an
// application_name. The scheduler passes a Target and gets Samples back; what it
// cannot infer is the clock and the pooler identity, so those two are declared
// here.
type PoolerConfig struct {
	// Now is R13.11's clock, the same role SQLConfig.Now plays for the SQL
	// collectors: it fills Sample.IngestTime. Nil means time.Now.
	Now func() time.Time

	// PoolerName derives the R9.3 `pooler` label value for a target. Nil means
	// the label is omitted; see the field comment below for why omitting is the
	// default and what the alternatives are.
	//
	// R9.3 (requirements.md:395) says "pooler series carry `pooler`", and the
	// cardinality table at :548 repeats it for this exact row
	// (`…,pooler,database,user`). The S5 exposition does not supply one:
	// measured on testdata/cnpg-metrics/ai-api-gateway__rev-proxy-pg-pooler-
	// 86b4bdd84d-n9vlv__9127.txt, its 123 pgbouncer series carry {database}
	// (60), {database,user} (48), {collector} (1) and no label block at all
	// (14); zero carry a `pooler` label. The exporter cannot: pgbouncer's own
	// vocabulary is (database,
	// user), and the pooler's identity is a Kubernetes fact, not a pgbouncer
	// one.
	//
	// Three candidates, and the reason this is a seam rather than an answer:
	//   - Target.Pooler, a new field. Correct, and the one that matches R9.3's
	//     intent (a Pooler CR name is stable; a pod name is not), but Target is
	//     the framework's identity type and adding a fifth identity field is
	//     A3's call, not a pooler collector's.
	//   - Derive from Target.Pod. The fixture's pod is
	//     rev-proxy-pg-pooler-86b4bdd84d-n9vlv; stripping the ReplicaSet hash
	//     and pod suffix yields rev-proxy-pg-pooler. String-fragile, and it
	//     breaks the moment a cluster has two poolers with generated names.
	//   - Omit it for S5 and let the `pod` label identify the pooler pod. This
	//     is what nil does, and it is the choice that ships no guess: the
	//     scheduler already puts `pod` on every series, so the data is
	//     attributable, and R9.3's letter is the only thing unmet.
	PoolerName func(t Target) string
}

// PoolerCollector is the S5 half of §3.3's pooler row: the pgbouncer exporter's
// 9127 metrics endpoint, parsed and re-exported under the R9.7 names.
//
// It is the package's first non-SQL collector, and the two framework rules that
// only have meaning for a non-SQL one both show up here: Result.RoundTrips is 0
// ("A non-SQL collector reports 0", collector.go), and Descriptor.ViewGroups is
// empty ("A non-SQL collector leaves it empty, which is how the framework tells
// 'no transactions' from 'one empty one'", also collector.go).
type PoolerCollector struct {
	fetch MetricsFetch
	cfg   PoolerConfig
	desc  Descriptor
}

// NewPoolerCollector builds the S5 collector on top of a fetch func and a config,
// with every unset field resolved by withDefaults.
//
// A nil fetch is an error rather than a default because there is nothing to
// default it to: the SQL collectors get an opener they can fall back to a
// zero-value pool on, and a pooler collector with no way to issue an HTTP GET
// cannot produce a sample, so the constructor refuses instead of returning a
// collector whose every round fails at run time.
func NewPoolerCollector(fetch MetricsFetch, cfg PoolerConfig) (*PoolerCollector, error) {
	if fetch == nil {
		return nil, fmt.Errorf("collector %s: MetricsFetch is required (a pooler collector with no fetch cannot produce a sample)", CollectorPooler)
	}
	c := &PoolerCollector{fetch: fetch, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorPooler,
		Interval: PoolerInterval,
		Source:   SourcePoolerMetrics,
		// CostCheap is the class's own definition — "an HTTP GET on a metrics or
		// status endpoint" (collector.go:131). The parenthetical on that clause
		// read "(S3, S4)" until this file existed and now reads "(S3, S4, S5)",
		// widened in the same commit that added the first S5 collector: a
		// cost-class comment that omits the collector it is describing is the
		// stale-prose class this package already tracks.
		Cost: CostCheap,
		// AppliesTo nil = AllMajors. The pgbouncer exporter's output does not
		// vary by PostgreSQL major — it varies by pgbouncer version — and the
		// "Only for clusters with a Pooler" gate in §3.3's row is enforced by
		// Target.Source matching (the scheduler hands a collector only the
		// targets it declared, R2.1), not by AppliesTo, which answers only the
		// PG-major question.
		AppliesTo: nil,
		// Timeout unset: R2.1's 10 s default, per Descriptor's rule that a
		// collector whose work is known to be cheap does not set a shorter one.
		Timeout: 0,
		// Empty by design: no view group, because there is no transaction.
		ViewGroups: nil,
	}
	return c, nil
}

// withDefaults fills the config's nil fields.
func (c PoolerConfig) withDefaults() PoolerConfig {
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Descriptor returns the collector's R2.1 declaration.
func (c *PoolerCollector) Descriptor() Descriptor { return c.desc }

// Collect fetches the pooler's 9127 exposition and returns one Sample per series
// in its pgbouncer families.
//
// The whole method is three steps — fetch, parse, project — and the interesting
// part is what it refuses to invent at each one.
func (c *PoolerCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	status, body, err := c.fetch(ctx, tgt.Address)
	if err != nil {
		return Result{}, fmt.Errorf("collector %s: fetch %s: %w", CollectorPooler, tgt.Address, err)
	}
	// A non-200 is an error, not an empty round. An exporter that answers 500
	// with a truncated body has not measured anything, and the alternative —
	// parsing whatever came back and publishing it — is the R8.7 failure shape
	// in collector form: a round that succeeds and is empty.
	if status != http.StatusOK {
		return Result{}, fmt.Errorf("collector %s: fetch %s: status %d, want %d",
			CollectorPooler, tgt.Address, status, http.StatusOK)
	}

	scrape, err := cnpgmetrics.Parse(string(body))
	if err != nil {
		return Result{}, fmt.Errorf("collector %s: parse %s: %w", CollectorPooler, tgt.Address, err)
	}

	return Result{
		Samples: poolerSamples(scrape, tgt, c.cfg, c.cfg.Now()),
		// 0, per collector.go's "A non-SQL collector reports 0". R13.1 counts
		// SQL round-trips and an HTTP GET is not one; reporting 1 here would
		// make the R13.1 budget arithmetic depend on how many collectors happen
		// to be non-SQL.
		RoundTrips: 0,
	}, nil
}

// poolerSamples projects a parsed scrape onto the package's Sample type.
//
// Scope: every family in the scrape that belongs to the pgbouncer group. R6.1
// names pools_* (16 families) and lists_* (12) and R6.3 names stats_* (20) — 48
// of the 51 the registry carries. The other three (collection_duration_seconds,
// collections_total, last_collection_error) are the exporter's own
// self-observability and no requirement names them. All 51 are emitted anyway,
// because the alternative is a silent gap: the parser validated the family, the
// naming table has a row for it, and a collector that emits 48 of the 51 it was
// handed has a gap no test can catch, since nothing in the fixture says which 48
// were meant.
func poolerSamples(scrape *cnpgmetrics.Scrape, tgt Target, cfg PoolerConfig, now time.Time) []Sample {
	if scrape == nil {
		return nil
	}
	var out []Sample
	for _, m := range scrape.CNPG {
		// The group check is the whole scoping rule. m.Family is non-nil for
		// every entry in Scrape.CNPG (parse.go's own contract), so the nil test
		// is a guard against a future Scrape that breaks it, not a live branch.
		if m.Family == nil || m.Family.Group != cnpgmetrics.GroupPGBouncer {
			continue
		}
		// MetricNameFor returns "" for a key the table has no row for. That is
		// D20's S8 class — a family present in the exporter and absent from the
		// naming table — and here it cannot happen for a family that came from
		// the registry, because every registry family has a naming row. It is
		// still checked, because the alternative is a Sample with an empty
		// Metric, which renders as a family named "" and is the exact failure
		// collector.go's Validate exists to prevent.
		name := MetricNameFor(m.Name)
		if name == "" {
			continue
		}
		for _, s := range m.Samples {
			out = append(out, Sample{
				Metric: name,
				Labels: poolerLabels(s.Labels, tgt, cfg),
				Value:  s.Value,
				// SourceTime stays zero. cnpgmetrics.Sample is {Labels,
				// Value} only (parse.go:22-31) — the text format allows a
				// per-sample timestamp and this exporter never sends one,
				// so there is no source clock to report. clock.go's field
				// doc says as much: "Zero means the collector has no source
				// clock to report."
				SourceTime: time.Time{},
				// IngestTime is the collector's job, not the scheduler's:
				// the scheduler sets it for the round (scheduler.go:498
				// labels, and the SQL collectors stamp it themselves at
				// database.go:435 and activity.go:878). Stamping it here
				// is what keeps the two collector kinds honest in the same
				// way.
				IngestTime: now,
				// StatsReset stays zero, and the reason is a spec gap filed
				// to A3: R6.3 says "with the `stats_reset` counter-reset
				// handling from R3.3", but no cnpg_pgbouncer_*reset* family
				// exists in the registry (grep over registry.go's 51
				// pgbouncer entries returns none), so S5 has no epoch to
				// report. R3.3's own text (requirements.md:308) scopes the
				// reset to pg_stat_statements_info.stats_reset on PG14+ — a
				// PostgreSQL view, not a pgbouncer metric — so the
				// cross-reference names a mechanism whose source is the
				// database, and the S6 collector over 5432 is where a
				// pgbouncer-side reset could be read at all.
				StatsReset: time.Time{},
			})
		}
	}
	return out
}

// poolerLabels copies the series' own label set and adds the R9.3 `pooler` label
// when the config can derive one.
//
// The copy matters: cnpgmetrics hands out the map it parsed, and the scheduler's
// ApplyIdentityLabels writes into the samples it is given. Sharing one map
// between the parse result and a published Sample means a later round mutates the
// earlier one's labels.
//
// The four identity labels (namespace, cluster, pod, instance_role) are NOT
// added here — that is scheduler.go:498's job via ApplyIdentityLabels, which is
// the framework's single contact point for them and the reason no collector
// builds them itself.
func poolerLabels(src map[string]string, tgt Target, cfg PoolerConfig) map[string]string {
	out := make(map[string]string, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	if cfg.PoolerName != nil {
		if name := cfg.PoolerName(tgt); name != "" {
			out["pooler"] = name
		}
	}
	return out
}
