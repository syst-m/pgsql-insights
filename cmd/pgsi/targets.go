// collectTargets is the producer for the argument Scheduler.Run takes.
//
// Run(ctx, targets func() []Target) asks for the target set once per round, and
// before this file the answer existed only as a test literal: the only
// []collector.Target value ever constructed in the repository was
// internal/metrics/metrics_test.go:127's one-element literal. So the scheduler
// had a parameter with no caller, which is the same half of the Phase 2 wiring
// gap that the missing Registry is the other half of — constructing a scheduler
// without a target set produces a scheduler that runs nothing, and Run's own
// docblock makes that a hard error rather than a silent one ("A nil targets func
// is an error rather than a silent forever-sleep, because a caller that forgot to
// pass one would otherwise start a goroutine that does nothing and looks
// healthy").
//
// It lives here, in the composition root, rather than in either package it joins.
// internal/collector does not import internal/discovery and internal/discovery
// does not import internal/collector; putting this function in either one creates
// an edge that did not exist, and the metrictype extraction
// (internal/collector/metrictype) is the recent proof that a new cross-package
// edge is a permanent constraint rather than a temporary one — that file exists
// only because cnpgmetrics needed one field from internal/collector.
// cmd/pgsi already imports both packages, so the mapping costs no edge, and
// "which discovered objects does this process collect from" is a per-process
// decision: D9's role split's whole point is that a future `pgsi api` process
// answers that question differently from a `pgsi collect` one, which is an
// argument for the answer living in the binary that has a role, not in the
// library that has a type.
//
// The addresses come from internal/kube, which is what collector.go:306-308
// promises in Target's own docblock: "internal/kube builds these:
// kube.PostgresAddress(host) fills Address for an S2 target,
// kube.MetricsURL/StatusURL/PoolerMetricsURL fill it for S3/S4/S5, and kube.Ports
// says which ports exist at all." This is the caller that promise was written for.
package main

import (
	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/discovery"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// collectTargets returns every addressable target in one inventory snapshot.
//
// Two of §3.2's ten sources are produced, and the choice is by constructibility
// rather than by preference:
//
//   - S2 (SourceSQL) from each instance pod. The address is kube.PostgresAddress
//     over InstanceView.PodIP, which is the field R1.10's direct transport needs
//     and the only one a 5432 target can be built from.
//   - S5 (SourcePoolerMetrics) from each pooler pod, via
//     kube.PoolerMetricsURL over PoolerPodView.IP, falling back to the
//     pooler-level PoolerView.MetricsURL when a Pooler reports an endpoint but
//     lists no pods (R1.4 discovers Poolers separately from Clusters, so the two
//     lists are not guaranteed to be populated together).
//
// S3 and S4 are deliberately not produced, and the reason is a missing DTO field
// rather than a missing collector: kube.MetricsURL's second parameter is
// spec.monitoring.tls.enabled — R1.10's one conditional, 9187 is HTTP and HTTPS
// only when the CR enables monitoring TLS — and dto.ClusterView does not carry
// that field. An S3 target built here would have to hardcode false and would
// therefore produce a wrong http:// address for every cluster with monitoring TLS
// on: a silent failure in the address column rather than a loud one. The
// instance-metrics (S3) and k8s-events (S4) collectors do not exist yet either,
// so nothing is lost by waiting for the field that makes the address answerable;
// S4's StatusURL needs no such flag and is skipped for the single consistent
// reason that its collector does not exist.
//
// S1, S7, S8, S9 and S10 produce no targets at all: S1 is the inventory this
// function reads, S7 is optional per §3.2, S8 is implemented natively rather than
// by shelling out (collector.go:93-95), S9 is off by default, and S10 is gated on
// --log-source (R5.10).
func collectTargets(inv discovery.Inventory) []collector.Target {
	var out []collector.Target

	// add appends t if it is collectable. Validation lives here, on the completed
	// target, and the order is not a style choice: Target.Valid's three clauses
	// are `t.Cluster != "" && t.Namespace != "" && ValidSource(t.Source)`
	// (collector.go:350-352) and ValidSource("") is false, so a target validated
	// before its Source is assigned is invalid no matter how good its identity is.
	// The first version of this function built a namespace+cluster "base" and
	// called Valid() on it before the per-source fields were set; it returned zero
	// targets for a well-formed two-instance cluster, and only the test caught it.
	// The general rule is the one the type already states: Valid is a predicate on
	// a whole Target, so it runs on a whole Target.
	add := func(t collector.Target) {
		// Address is checked alongside Valid rather than inside it. Valid does not
		// check Address — the type's docblock says the address is "host:port for an
		// S2 target and a full URL for an HTTP source" but leaves it out of the
		// predicate — and an empty address is the failure shape that produces a
		// series with a blank host. kube's own builders return "" for a host they
		// cannot render (transport.go:168's `if !ok || host == "" { return "",
		// false }`, whose bool PostgresAddress discards), so this catches both an
		// empty PodIP and a port that does not exist for the owner.
		if t.Address == "" || !t.Valid() {
			return
		}
		out = append(out, t)
	}

	for _, c := range inv.Clusters {
		// S2: one target per instance pod.
		for _, inst := range c.InstanceList {
			// R1.5's fenced set: a pod CNPG is deliberately keeping out of the
			// topology. Collecting SQL from it costs a connection and produces a
			// series for an instance the Cluster says is not in play, so the
			// fence — which the UI must not render as healthy either — gates the
			// target too. Checked before the add so the reason is attributable to
			// the fence rather than to Valid().
			if inst.Fenced {
				continue
			}
			// R1.2's Serving flag is deliberately NOT a gate. A pod that is
			// Running with postgres in CrashLoopBackOff (freeidp-pg-1) still has
			// an address, and letting the scheduler attempt it and fail is what
			// puts a line in pgsi_collector_errors_total for the instance that is
			// actually broken; gating on Serving would make a dead instance stop
			// reporting rather than report as failing, which is R8.7's failure
			// shape restated one layer down.
			add(collector.Target{
				Namespace: c.Namespace,
				Cluster:   c.Name,
				Pod:       inst.Name,
				Role:      inst.Role,
				Address:   kube.PostgresAddress(inst.PodIP),
				Source:    collector.SourceSQL,
				PGMajor:   pgMajor(c.PGMajor),
			})
		}

		// S5: one target per pooler pod, plus the pooler-level endpoint when the
		// Pooler reports one and lists no pods.
		for _, p := range c.Poolers {
			ns, clusterName := p.Namespace, p.Cluster
			if ns == "" {
				ns = c.Namespace
			}
			if clusterName == "" {
				// R1.4's association rule puts spec.cluster.name on the Pooler;
				// a Pooler discovered without one still belongs to the cluster
				// its entry was attached to, and that is the attribution R9.3
				// needs on the pooler series.
				clusterName = c.Name
			}
			for _, pod := range p.Pods {
				// Role stays empty. R9.3's instance_role is primary|replica and a
				// pgbouncer pod is neither; pooler_test.go's target leaves it empty
				// for the same reason, and inventing "pooler" here would put a
				// value in a field whose vocabulary R9.3 fixes.
				//
				// PGMajor stays 0. A pooler's Postgres major is not the pgbouncer
				// pod's property, and 0 means unknown — which admits only
				// AllMajors collectors, and pooler.go's AppliesTo is nil, so 0 is
				// the value that keeps the version question unanswered rather than
				// answered wrongly.
				add(collector.Target{
					Namespace: ns,
					Cluster:   clusterName,
					Pod:       pod.Name,
					Address:   kube.PoolerMetricsURL(pod.IP),
					Source:    collector.SourcePoolerMetrics,
				})
			}
			// The pooler-level endpoint, gated on there being no pods. Without the
			// gate a Pooler with both a MetricsURL and a pod list would emit the
			// service URL and every pod URL, and the scheduler would scrape the
			// same exporter N+1 times per round.
			if len(p.Pods) == 0 && p.MetricsURL != "" {
				add(collector.Target{
					Namespace: ns,
					Cluster:   clusterName,
					Address:   p.MetricsURL,
					Source:    collector.SourcePoolerMetrics,
				})
			}
		}
	}

	// The Failed and Loading namespace lists are not consulted. They are R8.4's
	// reporting fields — a namespace whose informer has not synced yet has no
	// clusters in Clusters, so it contributes no targets and the absence is
	// already expressed by the empty cluster list. Reading them here would be a
	// second answer to "is this namespace ready", which is R8.7's single-signal
	// argument applied one layer down.
	return out
}

// pgMajor dereferences dto.ClusterView.PGMajor, which is a *int precisely because
// R1.5's field is optional: an unknown version must stay 0, and collector.go's
// rule for 0 is "an unknown major admits only collectors whose AppliesTo is
// AllMajors — a SQL collector cannot know whether its view exists". Coercing an
// unknown to a plausible 16 would let a SQL collector run against an instance
// whose view set the matrix has not been consulted for.
func pgMajor(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
