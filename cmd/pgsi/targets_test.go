package main

import (
	"reflect"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/discovery"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// The test file for the target producer. Each test pins one rule from
// collectTargets' docblock, and the docblock's claims about what is NOT produced
// (S3, S4) get the same treatment as the ones about what is: an omission that no
// test names is an omission a future edit can silently fill wrong.

func TestCollectTargetsSQLFromInstanceList(t *testing.T) {
	major := 16
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			Namespace: "db",
			Name:      "pg",
			PGMajor:   &major,
			InstanceList: []dto.InstanceView{
				{Name: "pg-1", Role: "primary", PodIP: "10.42.1.11", Serving: true, Ready: true},
				{Name: "pg-2", Role: "replica", PodIP: "10.42.1.12", Serving: true, Ready: true},
			},
		},
	}}

	got := collectTargets(inv)
	want := []collector.Target{
		{
			Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary",
			Address: kube.PostgresAddress("10.42.1.11"),
			Source:  collector.SourceSQL,
			PGMajor: 16,
		},
		{
			Namespace: "db", Cluster: "pg", Pod: "pg-2", Role: "replica",
			Address: kube.PostgresAddress("10.42.1.12"),
			Source:  collector.SourceSQL,
			PGMajor: 16,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectTargets() =\n %+v\nwant\n %+v", got, want)
	}
}

// TestCollectTargetsPGMajorUnknown pins the nil-major rule. dto.ClusterView.PGMajor
// is a *int because R1.5's field is optional, and collector.go's rule for an
// unknown major is that it "admits only collectors whose AppliesTo is AllMajors".
// A test that only exercised the set case would let a `return 16` pass, which is
// the coercion the docblock says it refuses.
func TestCollectTargetsPGMajorUnknown(t *testing.T) {
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			Namespace: "db", Name: "pg", PGMajor: nil,
			InstanceList: []dto.InstanceView{{Name: "pg-1", Role: "primary", PodIP: "10.42.1.11"}},
		},
	}}

	got := collectTargets(inv)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].PGMajor != 0 {
		t.Errorf("PGMajor = %d, want 0 for a nil *int (0 is the value that means unknown)", got[0].PGMajor)
	}
}

// TestCollectTargetsSkipsFencedAndAddressless pins the two instance-level gates,
// and pins the one thing deliberately NOT gated.
func TestCollectTargetsSkipsFencedAndAddressless(t *testing.T) {
	major := 15
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			Namespace: "db", Name: "pg", PGMajor: &major,
			InstanceList: []dto.InstanceView{
				// The control: a normal serving instance.
				{Name: "pg-1", Role: "primary", PodIP: "10.42.1.11", Serving: true, Ready: true},
				// R1.5's fenced set: CNPG is deliberately keeping this pod out of
				// the topology, so it gets no target.
				{Name: "pg-2", Role: "replica", PodIP: "10.42.1.12", Fenced: true, Serving: true, Ready: true},
				// A Pending pod: an empty PodIP means no address, and Target.Valid
				// does not check Address, so this is the only guard.
				{Name: "pg-3", Role: "replica", PodIP: "", Serving: false, Ready: false},
				// NOT a gate: R1.2's crash-looping shape (freeidp-pg-1) — Running
				// with postgres in CrashLoopBackOff, so not Ready and not Serving,
				// but it has an address. Excluding it would make a broken instance
				// stop reporting instead of reporting as failing.
				{
					Name: "pg-4", Role: "replica", PodIP: "10.42.1.14",
					Serving: false, Ready: false,
					Phase: "Running", ContainerStatus: "CrashLoopBackOff", RestartCount: 7,
				},
			},
		},
	}}

	got := collectTargets(inv)
	var pods []string
	for _, tg := range got {
		pods = append(pods, tg.Pod)
	}
	want := []string{"pg-1", "pg-4"}
	if !reflect.DeepEqual(pods, want) {
		t.Errorf("pods = %v, want %v (pg-2 is fenced, pg-3 has no address, pg-4 is unserved but must still be collected)", pods, want)
	}
}

// TestCollectTargetsPoolerPerPod pins the pooler shape: two targets per pooler
// pod — the S5 exporter scrape on 9127 and the S6 pgbouncer console on 5432 — and
// no pooler-level target once the pods are listed.
func TestCollectTargetsPoolerPerPod(t *testing.T) {
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			Namespace: "db", Name: "pg",
			Poolers: []dto.PoolerView{
				{
					Name: "pg-pooler", Namespace: "db", Cluster: "pg",
					MetricsURL: "http://pg-pooler.db.svc:9127/metrics",
					Pods: []dto.PoolerPodView{
						{Name: "pg-pooler-7d9-aaa", IP: "10.42.1.21"},
						{Name: "pg-pooler-7d9-bbb", IP: "10.42.1.22"},
						// A pod with no IP: no address, so no target, and the
						// pooler-level URL must not resurrect it — the
						// fallback is gated on len(Pods) == 0.
						{Name: "pg-pooler-7d9-ccc", IP: ""},
					},
				},
			},
		},
	}}

	got := collectTargets(inv)
	want := []collector.Target{
		{
			Namespace: "db", Cluster: "pg", Pod: "pg-pooler-7d9-aaa",
			Address: kube.PoolerMetricsURL("10.42.1.21"),
			Source:  collector.SourcePoolerMetrics,
		},
		// The S6 half of the same pod. The address is the literal rather than
		// kube.URLFor(...) — the producer calls that function, so a want built by
		// calling it too would compare the copy against itself and pass on a table
		// whose 5432 row had moved. transport.go:180-182 fixes what a postgres row
		// renders as: `host + ":" + strconv.Itoa(port)`, no scheme.
		{
			Namespace: "db", Cluster: "pg", Pod: "pg-pooler-7d9-aaa",
			Address: "10.42.1.21:5432",
			Source:  collector.SourcePoolerRuntime,
		},
		{
			Namespace: "db", Cluster: "pg", Pod: "pg-pooler-7d9-bbb",
			Address: kube.PoolerMetricsURL("10.42.1.22"),
			Source:  collector.SourcePoolerMetrics,
		},
		{
			Namespace: "db", Cluster: "pg", Pod: "pg-pooler-7d9-bbb",
			Address: "10.42.1.22:5432",
			Source:  collector.SourcePoolerRuntime,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectTargets() =\n %+v\nwant\n %+v", got, want)
	}
	// Role is asserted by the DeepEqual above being exactly two entries with no
	// Role set; naming it explicitly because R9.3's vocabulary is the reason.
	for _, tg := range got {
		if tg.Role != "" {
			t.Errorf("target %s Role = %q, want \"\" (R9.3's instance_role is primary|replica and a pgbouncer pod is neither)", tg.Pod, tg.Role)
		}
		if tg.PGMajor != 0 {
			t.Errorf("target %s PGMajor = %d, want 0 (a pooler's PG major is not the pgbouncer pod's property)", tg.Pod, tg.PGMajor)
		}
	}
}

// TestCollectTargetsPoolerEndpointWithoutPods pins the fallback branch: R1.4
// discovers Poolers separately from Clusters, so a Pooler can report an endpoint
// and list no pods.
func TestCollectTargetsPoolerEndpointWithoutPods(t *testing.T) {
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			Namespace: "db", Name: "pg",
			Poolers: []dto.PoolerView{
				{
					Name: "pg-pooler", Namespace: "db", Cluster: "pg",
					MetricsURL: "http://pg-pooler.db.svc:9127/metrics",
				},
			},
		},
	}}

	got := collectTargets(inv)
	want := []collector.Target{{
		Namespace: "db", Cluster: "pg",
		Address: "http://pg-pooler.db.svc:9127/metrics",
		Source:  collector.SourcePoolerMetrics,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectTargets() =\n %+v\nwant\n %+v", got, want)
	}
}

// TestCollectTargetsPoolerIdentityFallback pins the namespace/cluster backfill.
// A Pooler discovered without spec.cluster.name still belongs to the cluster its
// entry was attached to, and that attribution is what R9.3's labels need.
func TestCollectTargetsPoolerIdentityFallback(t *testing.T) {
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			Namespace: "apps", Name: "gateway",
			Poolers: []dto.PoolerView{
				// Both identity fields empty: inherits apps/gateway.
				{Name: "gw-pooler", Pods: []dto.PoolerPodView{{Name: "gw-pooler-x1", IP: "10.42.2.31"}}},
				// The pooler's own fields win when present. R1.4 permits a Pooler to
				// sit in a different namespace than the Cluster it references, so
				// the enclosing entry's namespace is a fallback and not the answer.
				{Name: "gw-pooler-ro", Namespace: "messaging", Cluster: "broker",
					Pods: []dto.PoolerPodView{{Name: "gw-pooler-ro-x2", IP: "10.42.2.32"}}},
			},
		},
	}}

	got := collectTargets(inv)
	want := []collector.Target{
		{Namespace: "apps", Cluster: "gateway", Pod: "gw-pooler-x1",
			Address: kube.PoolerMetricsURL("10.42.2.31"), Source: collector.SourcePoolerMetrics},
		{Namespace: "apps", Cluster: "gateway", Pod: "gw-pooler-x1",
			Address: "10.42.2.31:5432", Source: collector.SourcePoolerRuntime},
		{Namespace: "messaging", Cluster: "broker", Pod: "gw-pooler-ro-x2",
			Address: kube.PoolerMetricsURL("10.42.2.32"), Source: collector.SourcePoolerMetrics},
		{Namespace: "messaging", Cluster: "broker", Pod: "gw-pooler-ro-x2",
			Address: "10.42.2.32:5432", Source: collector.SourcePoolerRuntime},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectTargets() =\n %+v\nwant\n %+v", got, want)
	}
}

// TestCollectTargetsInvalidCluster pins the cluster-level gate. Target.Valid is
// the rule ("A target with no cluster name is dropped rather than run"), and
// collectTargets applies it in add(), on the completed target — so a sparse
// Inventory entry with a populated InstanceList contributes nothing, because
// every target built from it inherits the empty Cluster.
//
// This test is the one that would have caught the pre-commit draft of 74485a4's
// bug on its own: that draft validated a namespace+cluster base before setting
// Source, which made Valid() reject every target, so this test passed for the
// wrong reason — it expected zero targets and got zero. The pair that actually
// exposed the bug is TestCollectTargetsSQLFromInstanceList and
// TestCollectTargetsPoolerPerPod, which expect non-empty results. A
// zero-expecting test needs a non-zero sibling to mean anything.
func TestCollectTargetsInvalidCluster(t *testing.T) {
	major := 16
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			// Name empty: every R2.2 metric and R9.3 label derives from it.
			Namespace: "db", PGMajor: &major,
			InstanceList: []dto.InstanceView{{Name: "x-1", Role: "primary", PodIP: "10.42.9.1"}},
			Poolers: []dto.PoolerView{
				{Name: "x-pooler", Pods: []dto.PoolerPodView{{Name: "x-pooler-y1", IP: "10.42.9.2"}}},
			},
		},
		{
			// Namespace empty: same rule, other half.
			Name: "pg", InstanceList: []dto.InstanceView{{Name: "pg-1", PodIP: "10.42.9.3"}},
		},
	}}

	if got := collectTargets(inv); len(got) != 0 {
		t.Errorf("collectTargets() = %+v, want no targets for clusters with an empty name or namespace", got)
	}
}

// TestCollectTargetsEmptyInventory pins the zero case. A nil Clusters slice is
// the answer discovery gives before its first sync, and the scheduler calls the
// targets func every round — including the rounds before that sync.
func TestCollectTargetsEmptyInventory(t *testing.T) {
	if got := collectTargets(discovery.Inventory{}); got != nil {
		t.Errorf("collectTargets(empty) = %+v, want nil", got)
	}
}

// TestCollectTargetsEveryTargetIsValidAndAddressed is the property check over a
// rich inventory: everything returned must satisfy Target.Valid and carry a
// non-empty address. Valid does not check Address, so the second half is not
// implied by the first, and an empty address is the failure shape that produces a
// series with a blank host.
func TestCollectTargetsEveryTargetIsValidAndAddressed(t *testing.T) {
	major := 14
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			Namespace: "db", Name: "pg", PGMajor: &major,
			InstanceList: []dto.InstanceView{
				{Name: "pg-1", Role: "primary", PodIP: "10.42.1.11", Serving: true},
				{Name: "pg-2", Role: "replica", PodIP: "10.42.1.12", Serving: true},
				{Name: "pg-3", Role: "replica", PodIP: "10.42.1.13", Fenced: true},
			},
			Poolers: []dto.PoolerView{
				{
					Name: "pg-pooler", Namespace: "db", Cluster: "pg",
					Pods: []dto.PoolerPodView{{Name: "pg-pooler-z1", IP: "10.42.1.21"}},
				},
				{Name: "pg-pooler-ro", Namespace: "db", Cluster: "pg", Type: "ro",
					MetricsURL: "http://pg-pooler-ro.db.svc:9127/metrics"},
			},
		},
		{
			Namespace: "db", Name: "pg2",
			InstanceList: []dto.InstanceView{{Name: "pg2-1", Role: "primary", PodIP: "10.42.1.31"}},
		},
	}}

	got := collectTargets(inv)
	// 2 clusters: pg has 2 S2 (pg-3 fenced) + 1 S5 and 1 S6 (the one pooler pod,
	// two ports) + 1 S5 (the ro endpoint, which has no pods and so no S6 half);
	// pg2 has 1 S2. Total 6.
	if len(got) != 6 {
		t.Fatalf("len(got) = %d, want 6:\n %+v", len(got), got)
	}
	for i, tg := range got {
		if !tg.Valid() {
			t.Errorf("target %d (%s) fails Valid(): %+v", i, tg.Key(), tg)
		}
		if tg.Address == "" {
			t.Errorf("target %d (%s) has an empty Address", i, tg.Key())
		}
		if tg.Pod == "" && tg.Source == collector.SourceSQL {
			t.Errorf("target %d (%s) is S2 with an empty Pod", i, tg.Key())
		}
	}

	// Source counts, so a bug that swaps S2 and S5 on one branch fails here even
	// if the total happens to match.
	counts := map[collector.SourceID]int{}
	for _, tg := range got {
		counts[tg.Source]++
	}
	if counts[collector.SourceSQL] != 3 ||
		counts[collector.SourcePoolerMetrics] != 2 ||
		counts[collector.SourcePoolerRuntime] != 1 {
		t.Errorf("source counts = %v, want map[S2:3 S5:2 S6:1]", counts)
	}
}

// TestCollectTargetsIsDeterministic pins the ordering contract. The scheduler
// hands the same list to R9.12's UpTracker every round, and a producer whose
// order moved between calls would make the tracker see churn that is not a
// topology change. The implementation uses range over a slice, not a map, so the
// order is stable; the test is what stops a future map-based rewrite from
// changing that silently.
func TestCollectTargetsIsDeterministic(t *testing.T) {
	major := 16
	inv := discovery.Inventory{Clusters: []dto.ClusterView{
		{
			Namespace: "db", Name: "pg", PGMajor: &major,
			InstanceList: []dto.InstanceView{
				{Name: "pg-1", Role: "primary", PodIP: "10.42.1.11"},
				{Name: "pg-2", Role: "replica", PodIP: "10.42.1.12"},
			},
			Poolers: []dto.PoolerView{
				{Name: "pg-pooler", Namespace: "db", Cluster: "pg",
					Pods: []dto.PoolerPodView{{Name: "pg-pooler-a", IP: "10.42.1.21"}}},
			},
		},
	}}

	first := collectTargets(inv)
	second := collectTargets(inv)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("collectTargets() differs between calls:\n first  = %+v\n second = %+v", first, second)
	}
}

// TestPGMajor covers the deref helper directly, including the case the inventory
// tests cannot reach without a cluster per value.
func TestPGMajor(t *testing.T) {
	if got := pgMajor(nil); got != 0 {
		t.Errorf("pgMajor(nil) = %d, want 0", got)
	}
	for _, v := range []int{10, 11, 12, 13, 14, 15, 16, 17, 18} {
		v := v
		if got := pgMajor(&v); got != v {
			t.Errorf("pgMajor(&%d) = %d, want %d", v, got, v)
		}
	}
}
