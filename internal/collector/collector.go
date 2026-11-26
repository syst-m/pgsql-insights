// Package collector is pgsi's collection framework: the contract every collector
// implements (R2.1), the registry that applies R2.3's per-collector overrides,
// the scheduler that runs collectors independently (R2.1), the sink that holds
// what they produce (R13.5), and the two self-observability families every run
// feeds (R2.2).
//
// Phase 2 is landed in five commits and this is the first, so the package
// documents which of its phase requirements it closes and which it leaves open —
// a gap that is named reads as a plan, and the same gap unnamed reads as an
// omission the next reader has to re-derive:
//
//	R2.1    the Collector contract and its five declared fields, the
//	        per-collector timeout (default 10 s) and the per-cluster
//	        concurrency cap (default 4).
//	R2.2    pgsi_collector_duration_seconds and pgsi_collector_errors_total,
//	        failure isolation, and the last-good/age record.
//	R2.3    --interval.<collector>= and --disable-collector=a,b.
//	R13.1   the per-cluster round report that makes "< 3 s / < 40 round-trips"
//	        a measured number instead of a claim.
//	R13.5   the memory ceiling and size-based eviction in its --store=memory
//	        form (§3.4); the SQLite half of the same flag lands with the store.
//	R13.11  the clock contract — every sample carries the instance's time and
//	        ours, and a > 30 s disagreement warns naming the cluster.
//
// Deliberately not here yet: the SQL collectors themselves (R2.5–R2.8, the
// phase's third commit), the pgversion matrix that answers R2.1's
// PG-version-applicability field (R2.4, the second commit — the seam is
// Descriptor.AppliesTo), R2.9's global budget (the fifth commit — the seam is
// the comment on Scheduler.runTarget), and the pg_stat_statements enablement
// flow (R3.15, also the fifth).
package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultInterval is §3.3's collection period (60 s), which it names as
// matching Cloud SQL's sampling period. Every one of §3.3's thirteen collectors
// declares its own interval, so this is the value an override falls back to and
// the one the R11.8 config chain defaults to — not a licence to leave
// Descriptor.Interval empty (see Validate).
const DefaultInterval = 60 * time.Second

// DefaultTimeout is R2.1's per-collector timeout default (10 s). It is a
// per-collector ceiling, not a per-round one: R2.1's sentence pairs it with the
// concurrency cap so a collector that hangs on one instance consumes one slot
// for 10 s and then loses it, rather than holding a cluster's schedule open.
const DefaultTimeout = 10 * time.Second

// DefaultPerClusterConcurrency is R2.1's per-cluster concurrency default (4).
// R2.9's own text explains why the number is small and per-cluster: 50 clusters
// × 13 collectors with no cap fans out to ~200 concurrent queries against
// instances where max_connections is commonly 100.
const DefaultPerClusterConcurrency = 4

// DefaultMaxConcurrentSQL, DefaultMaxConcurrentHTTPScrapes and
// DefaultSchedulerQueueDepth are R2.9's three defaults, quoted from the
// requirement's own parenthetical: "--max-concurrent-sql (default 32),
// --max-concurrent-http-scrapes (default 64) and a shed policy ...
// --scheduler-queue-depth (default 512)". They sit beside
// DefaultPerClusterConcurrency because the four numbers are one table of
// concurrency limits — one per cluster (R2.1) and three global (R2.9) — and a
// reader checking the two requirements' arithmetic needs them in one place to
// see that 32 SQL + 64 HTTP global slots is what bounds the ~200 concurrent
// queries that R2.1's per-cluster cap of 4 alone would let 50 clusters produce.
const (
	DefaultMaxConcurrentSQL         = 32
	DefaultMaxConcurrentHTTPScrapes = 64
	DefaultSchedulerQueueDepth      = 512
)

// SourceID names a data source. R2.1 requires every collector to declare which
// one it needs, so the set is enumerated here rather than invented: a collector
// that says "S3" is saying "GET http://<pod>:9187/metrics", and the constant's
// value is the same identifier the spec prints so a log line, the spec and the
// code name the source identically.
//
// Eleven values, and they come from two spec places. S1-S10 are §3.2's table
// verbatim (`requirements.md:168-177`, the ten rows it ranks by cost and by
// always-available-ness). S11 is not in that table: it is R5.9's source, ruled at
// `requirements.md:349` from §3.3's `k8s-events` collector row
// (`requirements.md:208`, whose source column reads "`events` in the cluster's
// namespace"). The ruling exists because §3.2's ten do not cover a pod-event
// watch and Validate rejects every value outside this block, so a source-less
// collector is a compile-time-impossible option rather than a second design
// choice -- see SourceK8sEvents's own docblock, and the R7.2-rule marking note
// there.
type SourceID string

const (
	// SourceClusterStatus is S1: the Cluster CR's status via the informers.
	// Free — it needs no metrics and no SQL, and §3.2 marks it as driving every
	// other collector.
	SourceClusterStatus SourceID = "S1"
	// SourceSQL is S2: direct SQL against <cluster>-rw.<ns>.svc:5432 (or the
	// pod IP S1 gives us), needing the <cluster>-app secret or a documented
	// pg_monitor grant. Available on any serving instance.
	SourceSQL SourceID = "S2"
	// SourceInstanceMetrics is S3: the instance manager's cnpg_* exposition on
	// :9187 (HTTPS when spec.monitoring.tls.enabled). Compiled into the
	// instance manager, so it is not gated on spec.monitoring and not a sidecar.
	SourceInstanceMetrics SourceID = "S3"
	// SourceInstanceStatus is S4: HTTPS GET on :8000/pg/status — the
	// postgres.PodStatus document, unauthenticated, whose replicationInfo[] and
	// lastFailedWAL fields are why R5.5's replication view needs no SQL.
	SourceInstanceStatus SourceID = "S4"
	// SourcePoolerMetrics is S5: the pooler pod's :9127/metrics (cnpg_pgbouncer_*).
	SourcePoolerMetrics SourceID = "S5"
	// SourcePoolerRuntime is S6: pgbouncer SHOW POOLS/CLIENTS/STATS over 5432,
	// which is the client→server mapping that fixes attribution.
	SourcePoolerRuntime SourceID = "S6"
	// SourceK8sMetrics is S7: cAdvisor / node-exporter / metrics-server, the
	// OS half of System Insights. Optional for us (§3.2).
	SourceK8sMetrics SourceID = "S7"
	// SourceKubectlStatus is S8: `kubectl cnpg status -o json`. Optional; we
	// implement the queries natively rather than shelling out.
	SourceKubectlStatus SourceID = "S8"
	// SourcePostgresExporter is S9: a postgres_exporter sidecar's /metrics.
	// Optional third path, off by default.
	SourcePostgresExporter SourceID = "S9"
	// SourcePodLogs is S10: the pods/log subresource, off by default and only
	// live when --log-source is enabled (R5.10).
	SourcePodLogs SourceID = "S10"
	// SourceK8sEvents is S11: a watch on the `events` API resource in a cluster's
	// namespace, and the source R5.9's events timeline needs.
	//
	// It is the one value here that §3.2's table does not print. The id was ruled
	// at `requirements.md:349` in answer to A1's question -- "does the k8s-events
	// collector gain an S11, or does it get no SourceID because it is not a
	// Postgres source?" -- and the ruling is that "no SourceID" is not
	// implementable: Source is a required field and Validate rejects every other
	// value, so the only two options were a new id or a collector that cannot be
	// declared at all. The "not a Postgres source" objection is answered by the
	// block itself, which already holds two non-Postgres surfaces (S7 is cAdvisor
	// and friends, S10 is pods/log), so the block is §3.2's source list rather
	// than a Postgres list. Folding it into S1 -- which the row also draws on, for
	// *phase transitions* -- was rejected: S1 is the Cluster CR's status via the
	// informers, a pod-event watch reads a different API resource, and one id
	// naming two reads is what R6.1's ruling allowed only where a schedule row
	// covers both names (S5 + S6).
	//
	// R5.9 is a Phase 8 row (requirements.md:760 maps 5.9 -> 8), so this constant
	// is a Phase 8 artefact landing in Phase 2-era code. The ruling applies
	// R7.2's rule and forbids any phase table from marking R5.9 before Phase 8
	// runs, which is why adding it here marks nothing.
	SourceK8sEvents SourceID = "S11"
)

// Class names which of R2.9's two global budgets a source draws from.
//
// R2.9 names two caps and §3.2 names ten sources, and the requirement never
// draws the line between them, so the mapping is a decision. It is made here,
// beside the vocabulary it maps from, rather than in a switch inside the
// scheduler where every reader would have to reconstruct it: the two SQL sources
// are named explicitly and the other eight fall through to HTTP.
//
// The line is the one R2.9's own justification draws. The SQL cap exists because
// "50 clusters × 13 collectors fans out to ~200 concurrent SQL queries with no
// global cap, against Postgres instances where max_connections is commonly 100"
// — so the class is *sources that open a Postgres connection*, which is S2
// (direct SQL on :5432) and S6 (pgbouncer's console on 5432, whose SHOW
// POOLS/CLIENTS/STATS are Postgres-wire queries like any other and take a
// connection slot on the pooler). Everything else — the informer cache S1, the
// exposition scrapes S3, S4, S5, S7 and S9, the kubectl exec S8, the log
// subresource S10 — costs a socket rather than a connection slot and is charged
// to the HTTP budget, which is why that number is twice the SQL one. An
// eleventh source in §3.2 gets the HTTP class by default, and that default is
// stated here rather than implied by a missing case.
type Class int

const (
	// ClassSQL is R2.9's --max-concurrent-sql budget (default 32): S2 and S6.
	ClassSQL Class = iota
	// ClassHTTP is R2.9's --max-concurrent-http-scrapes budget (default 64):
	// the other eight sources.
	ClassHTTP
)

// String names the class for a log line or a test failure, so a scheduler log
// says "sql" rather than "0".
func (c Class) String() string {
	switch c {
	case ClassSQL:
		return "sql"
	case ClassHTTP:
		return "http"
	}
	return "unknown"
}

// Class returns the budget a collector declaring src draws from.
//
// The two SQL sources are named explicitly and the rest fall to default, so
// ValidSource's closed set and this mapping cannot drift apart silently: a
// SourceID that ValidSource rejects still gets a class here (HTTP), which is the
// safe direction, because an unregistered source cannot be a Postgres query.
func (src SourceID) Class() Class {
	switch src {
	case SourceSQL, SourcePoolerRuntime:
		return ClassSQL
	default:
		return ClassHTTP
	}
}

// ValidSource reports whether id is one of the SourceID constants (S1–S11).
// R2.1's declaration is only checkable if the source vocabulary is closed, so an
// unknown value is a registration error rather than a string that happens to look
// wrong. See the type's docblock for where each of the eleven comes from.
func ValidSource(id SourceID) bool {
	switch id {
	case SourceClusterStatus, SourceSQL, SourceInstanceMetrics, SourceInstanceStatus,
		SourcePoolerMetrics, SourcePoolerRuntime, SourceK8sMetrics,
		SourceKubectlStatus, SourcePostgresExporter, SourcePodLogs,
		SourceK8sEvents:
		return true
	}
	return false
}

// CostClass is R2.1's fifth declared field.
//
// Spec gap, flagged rather than resolved silently: R2.1 requires each collector
// to declare a cost class and §3.2's heading ranks sources "by cost", but §4
// never enumerates the vocabulary — no requirement names the set of legal
// values, so any implementation picks one. The three below are derived from the
// two numbers the spec does commit to: R13.1's < 40 SQL round-trips per cluster
// per 60 s, and R2.7's "single read-only transaction per view group". A4/A3
// should replace this with the spec's own wording when R2.1 gains one; until
// then the classes are documented as derived, and every collector in the phase's
// third commit states its class in its own file.
type CostClass string

const (
	// CostCheap costs no SQL round-trip at all: an informer read (S1), an HTTP
	// GET on a metrics or status endpoint (S3, S4, S5), or a watch. These are the
	// sources §3.2 calls free and always-available.
	CostCheap CostClass = "cheap"
	// CostStandard costs one read-only transaction per view group (R2.7) and
	// single-digit queries: the database, wal, settings and replication
	// collectors. This is the class R13.1's 40-round-trip budget is sized for.
	CostStandard CostClass = "standard"
	// CostExpensive costs a large result set, a multi-view group, or an interval
	// short enough that its per-round cost is paid four times a minute: the
	// statements and activity collectors. R2.3's --disable-collector exists
	// mostly so a production user can turn this class off.
	CostExpensive CostClass = "expensive"
)

// ValidCostClass reports whether c is one of the three declared classes.
func ValidCostClass(c CostClass) bool {
	switch c {
	case CostCheap, CostStandard, CostExpensive:
		return true
	}
	return false
}

// Applicability answers R2.1's fourth field — "PG-version applicability" — for
// one collector: given a target's PostgreSQL major, may this collector run
// against it?
//
// It is a func and not a field because the answer comes from the pgversion
// matrix (R2.4), which is the phase's second commit and lives in
// internal/collector/pgversion/. A collector whose SQL is identical on every
// major (the S3 and S4 collectors, which read no SQL at all) sets AllMajors;
// a SQL collector sets a func that asks the matrix. Making it a func now is
// what lets the framework compile before the matrix exists, and it is the seam
// the second commit plugs into without touching this file.
type Applicability func(pgMajor int) bool

// AllMajors is the nil-equivalent Applicability: valid on every major the
// product supports. A nil Descriptor.AppliesTo is treated as AllMajors rather
// than as an error, because "no opinion about versions" is the correct answer
// for the non-SQL sources and requiring each of them to say so is noise.
func AllMajors(int) bool { return true }

// Descriptor is R2.1's declaration: the five fields a collector must declare
// (name, interval, required source, PG-version applicability, cost class), plus
// the two the scheduler needs to run it safely (a timeout, and the view groups
// R2.7 wraps in one read-only transaction).
type Descriptor struct {
	// Name is the collector's stable identifier — one of §3.3's collector names;
	// the schedule table is the list. It is the
	// `collector` label value on both R2.2 families and the key R2.3's
	// --interval.<collector>= and --disable-collector= flags address, so
	// renaming it is a breaking change to a user's flags.
	Name string
	// Interval is how often this collector runs, from §3.3's table (15 s for
	// activity, 30 s for locks, 60 s for most, 15 min for settings). R2.3's
	// --interval.<collector>= override is applied over it in the registry, not
	// here, so a collector's file always shows its own default.
	Interval time.Duration
	// Source is R2.1's "required source" — which of §3.2's ten this collector
	// reads. One collector, one source: the `replication` row in §3.3 lists
	// S4 as preferred and S2/S3 as fallbacks, and that preference order is the
	// replication collector's own logic, not a licence to declare two sources.
	Source SourceID
	// Cost is R2.1's cost class.
	Cost CostClass
	// AppliesTo is R2.1's PG-version applicability. Nil means AllMajors.
	AppliesTo Applicability
	// Timeout bounds one collection round (R2.1's per-collector timeout,
	// default 10 s). Zero means DefaultTimeout; a collector whose SQL is known
	// to be cheap does not set it shorter, because the timeout is a hang guard
	// and not a tuning knob — see R2.7's statement_timeout for the per-query
	// bound that is a tuning knob.
	Timeout time.Duration
	// ViewGroups names the view groups this collector reads, and it is what
	// R2.7's "single read-only transaction per view group" is expressed in: one
	// entry per group, and the SQL collector opens one transaction per group
	// per round rather than one per query. A non-SQL collector leaves it empty,
	// which is how the framework tells "no transactions" from "one empty one".
	ViewGroups []string
}

// Validate reports whether the descriptor is complete enough to register.
//
// Every check here exists because a malformed descriptor fails silently in the
// place it is noticed latest: an empty name produces a metric with an empty
// `collector` label that Prometheus accepts and a dashboard cannot select; an
// empty interval produces a collector the scheduler either never runs or runs
// in a hot loop, depending on which default it guesses; an unknown source is a
// collector that will be written against a source §3.2 does not have.
func (d Descriptor) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("collector: Descriptor.Name is required (it is the `collector` label on both R2.2 families)")
	}
	if d.Interval <= 0 {
		return fmt.Errorf("collector %q: Descriptor.Interval must be > 0 (R2.1 declares an interval per collector; §3.3's table gives one for all thirteen, and DefaultInterval is 60s)", d.Name)
	}
	if !ValidSource(d.Source) {
		return fmt.Errorf("collector %q: unknown source %q (R2.1's required source must be one of this package's SourceID constants, S1–S11)", d.Name, d.Source)
	}
	if !ValidCostClass(d.Cost) {
		return fmt.Errorf("collector %q: unknown cost class %q (R2.1's cost class must be cheap, standard or expensive)", d.Name, d.Cost)
	}
	if d.Timeout < 0 {
		return fmt.Errorf("collector %q: Descriptor.Timeout must be >= 0 (0 selects DefaultTimeout %s)", d.Name, DefaultTimeout)
	}
	for _, g := range d.ViewGroups {
		if strings.TrimSpace(g) == "" {
			return fmt.Errorf("collector %q: empty entry in ViewGroups (R2.7 names one entry per view group; an empty list means \"no SQL\", not \"one unnamed group\")", d.Name)
		}
	}
	return nil
}

// EffectiveTimeout returns the collector's per-round timeout with the
// zero-means-default rule resolved in one place, so the scheduler and the SQL
// executor cannot disagree about what an unset Timeout means. (Go forbids a
// method and a field sharing a name, which is why this is EffectiveTimeout and
// not Timeout.)
func (d Descriptor) EffectiveTimeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return DefaultTimeout
}

// Applies reports whether this collector may run against a target of the given
// PostgreSQL major, resolving a nil AppliesTo to AllMajors.
func (d Descriptor) Applies(pgMajor int) bool {
	if d.AppliesTo == nil {
		return AllMajors(pgMajor)
	}
	return d.AppliesTo(pgMajor)
}

// Collector is one unit of collection (R2.1). The framework knows nothing about
// what a collector does — SQL, an HTTP GET, a log parse — which is what lets one
// scheduler run §3.3's thirteen collectors with one timeout, one concurrency
// rule, one error counter and one failure-isolation story.
type Collector interface {
	// Descriptor returns the collector's R2.1 declaration. It must be
	// side-effect-free and return the same value every call: the registry calls
	// it once at registration, and the scheduler calls it per round to name the
	// metric it is about to observe.
	Descriptor() Descriptor

	// Collect performs one collection round against one target.
	//
	// Contract the scheduler relies on, each clause preventing a specific
	// failure:
	//
	//   - ctx carries the per-collector timeout (R2.1) and the shutdown signal
	//     (R13.8). A collector that ignores ctx and blocks past the deadline
	//     holds its cluster's concurrency slot for the whole round, which is
	//     exactly the R2.1 failure the timeout exists to prevent.
	//   - On error, the returned Result must still carry the RoundTrips it
	//     actually issued. R13.1's < 40 round-trip assertion is measured from
	//     here, and a collector that returns (zero, err) on a partial failure
	//     makes a cluster that issued 39 queries look like one that issued 0.
	//   - A non-nil error means the round produced nothing usable, so the
	//     scheduler publishes nothing and the sink keeps serving the previous
	//     round's samples — that is R2.2's "a failed collector leaves its
	//     last-good data served", and it is a property of not publishing rather
	//     than of any restore logic.
	//   - Collect must not mutate the target and must be safe to call
	//     concurrently for different targets (R2.1 runs collectors
	//     independently).
	Collect(ctx context.Context, tgt Target) (Result, error)
}

// Target is one addressable endpoint a collector runs against.
//
// Its identity fields are R9.3's label contract — (namespace, cluster, pod) is
// the stable instance identity R9.3 names and R5.7's compare-up-to-5 depends on
// — carried alongside the address so a collector does not have to reassemble it
// from a kube object. internal/kube builds these: kube.PostgresAddress(host)
// fills Address for an S2 target, kube.MetricsURL/StatusURL/PoolerMetricsURL
// fill it for S3/S4/S5, and kube.Ports says which ports exist at all.
type Target struct {
	Namespace string
	Cluster   string
	// Pod is the instance pod. Empty for cluster-scoped sources (S1's CR status,
	// S8's CLI render), which is why Key and ClusterKey are separate methods
	// rather than one string.
	Pod string
	// Role is R9.3's instance_role ("primary", "replica"). Empty when the
	// source does not report one.
	Role string
	// Address is host:port for an S2 target and a full URL for an HTTP source.
	Address string
	// Source records which of §3.2's sources this target represents, so a
	// scheduler can hand a collector only the targets it declared (R2.1)
	// instead of every target it has.
	Source SourceID
	// PGMajor is the target's PostgreSQL major, which is what
	// Descriptor.AppliesTo is asked about. 0 means unknown, and an unknown
	// major admits only collectors whose AppliesTo is AllMajors — a SQL
	// collector cannot know whether its view exists.
	PGMajor int
}

// Key is the full instance identity R9.3 defines: namespace/cluster/pod. For a
// cluster-scoped target the pod part is empty and this is namespace/cluster/.
func (t Target) Key() string {
	if t.Pod == "" {
		return t.ClusterKey()
	}
	return t.Namespace + "/" + t.Cluster + "/" + t.Pod
}

// ClusterKey is the cluster half of the identity, which is the scope R2.1's
// per-cluster concurrency cap and R2.9's shed policy apply to. Two instances of
// one cluster share one cap; they do not each get four slots.
func (t Target) ClusterKey() string { return t.Namespace + "/" + t.Cluster }

// Valid reports whether the target has enough to be collected. A target with no
// cluster name is dropped rather than run: every R2.2 metric and R9.3 label is
// derived from it, so an empty one produces a series that cannot be attributed
// to anything.
func (t Target) Valid() bool {
	return t.Cluster != "" && t.Namespace != "" && ValidSource(t.Source)
}

// Result is what one collection round produced.
type Result struct {
	// Samples are the values the round produced. Empty with a nil error is a
	// legitimate answer — a single-instance cluster has no rows for the
	// replication collector (R5.5 says so explicitly) — and the sink records the
	// empty set as the new last-good rather than treating it as a failure.
	Samples []Sample
	// RoundTrips counts the SQL round-trips the round issued, which is how
	// R13.1's "< 40 SQL round-trips per cluster" becomes a measured number: the
	// framework cannot count queries a collector issues inside its own
	// transaction, so the collector reports the count and the scheduler sums it
	// per cluster. A non-SQL collector reports 0, and the round report
	// distinguishes "0 because it issues no SQL" from "0 because it is broken"
	// via Descriptor.Source rather than by guessing.
	RoundTrips int
	// Probes carries the extension-probe inventory for rounds that produced one:
	// the settings collector's answer for every database it connected to,
	// including the ones with no extensions installed. Every other collector
	// leaves it nil, and the scheduler publishes it through the sink's
	// ProbePublisher half when the sink has one — see ProbeResult, whose whole
	// reason to exist is that Samples cannot express R3.1's not-known state.
	//
	// It is a field on Result rather than a second Collect return because the
	// scheduler's error path is the one that decides what gets stored: a failed
	// round's Result is discarded entirely, so anything the framework is meant to
	// keep has to be inside the value it discards or keeps as a unit. A second
	// return value would have to be threaded through that decision separately,
	// which is the half-publish shape Sink's docblock refuses.
	Probes []ProbeResult
	// Events carries the R5.9 timeline entries a round observed, and it is the
	// third field for the same reason Probes is the second: the scheduler's error
	// path discards a whole Result, so anything the framework keeps has to be
	// inside the value it discards or keeps. The scheduler publishes it through
	// EventPublisher when the sink has one and the slice is non-empty -- see
	// EventPublisher's docblock for why that is a separate interface rather than a
	// fourth Sink method, which is the same argument ProbePublisher's is.
	//
	// Every collector except the k8s-events one leaves it nil. R5.9 asks for "one
	// timeline per cluster" while Result is per-Target, so a cluster with three
	// instances gets three Event slices that a consumer merges on
	// Target.ClusterKey(); Event carries no instance identity of its own for that
	// reason, exactly as Probes carries none.
	Events []Event
}
