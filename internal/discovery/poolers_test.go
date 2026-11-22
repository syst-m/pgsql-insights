package discovery

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// R1.4's tests: the Pooler-to-cluster association in both of its CRD spellings,
// and which pods belong to which pooler.

// poolerObj builds a Pooler object from a spec map, in the shape the API server
// returns (apiVersion/kind/metadata plus whatever spec and status hold).
func poolerObj(ns, name string, spec map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Pooler",
		"metadata":   map[string]any{"namespace": ns, "name": name},
	}
	if spec != nil {
		obj["spec"] = spec
	}
	return &unstructured.Unstructured{Object: obj}
}

// TestPoolerClusterName is R1.4's association rule in both spellings. The
// requirement names spec.cluster.name; CNPG <= 1.25 wrote spec.cluster as a bare
// string, and a pooler on an older cluster is attributed correctly only if both
// are read.
func TestPoolerClusterName(t *testing.T) {
	cases := []struct {
		name   string
		spec   map[string]any
		want   string
		wantOK bool
	}{
		{
			"object form (1.26+, R1.4's wording)",
			map[string]any{"cluster": map[string]any{"name": "rev-proxy-pg"}},
			"rev-proxy-pg", true,
		},
		{
			"string form (<= 1.25)",
			map[string]any{"cluster": "rev-proxy-pg"},
			"rev-proxy-pg", true,
		},
		{
			// An empty object form is not an association: name is absent, and
			// falling back to "" would attach the pooler to a cluster named "".
			"object form with no name",
			map[string]any{"cluster": map[string]any{"kind": "Cluster"}},
			"", false,
		},
		{"no spec.cluster at all", map[string]any{"type": "rw"}, "", false},
		// spec.cluster as a number is malformed, not a cluster named "3": the
		// caller drops it rather than guessing.
		{"wrong type", map[string]any{"cluster": float64(3)}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := poolerClusterName(poolerObj("ns", "p", c.spec).Object)
			if ok != c.wantOK || got != c.want {
				t.Errorf("poolerClusterName = (%q, %v), want (%q, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestBuildPoolersAssociation covers the part of R1.4 that says "associating
// each to its spec.cluster.name": a pooler is only listed under the cluster it
// names, and the list is sorted so the API's order is stable across polls.
func TestBuildPoolersAssociation(t *testing.T) {
	cr := clusterCR("ai-api-gateway", "rev-proxy-pg", map[string]any{"instances": int64(2)}, nil)
	poolers := []*unstructured.Unstructured{
		poolerObj("ai-api-gateway", "zeta-pooler", map[string]any{"cluster": map[string]any{"name": "rev-proxy-pg"}}),
		poolerObj("ai-api-gateway", "alpha-pooler", map[string]any{"cluster": map[string]any{"name": "rev-proxy-pg"}}),
		// Another cluster's pooler, in the same namespace: it must not appear
		// here, and it is picked up when that cluster's view is built.
		poolerObj("ai-api-gateway", "other-pooler", map[string]any{"cluster": map[string]any{"name": "cookbook-pg"}}),
		// Unattributable: dropped rather than guessed at.
		poolerObj("ai-api-gateway", "orphan-pooler", map[string]any{"type": "rw"}),
	}
	got := buildPoolers(cr, poolers, nil)
	if len(got) != 2 {
		t.Fatalf("poolers = %d entries, want 2 (the two that name rev-proxy-pg): %+v", len(got), got)
	}
	if got[0].Name != "alpha-pooler" || got[1].Name != "zeta-pooler" {
		t.Errorf("poolers not sorted by name: %q, %q", got[0].Name, got[1].Name)
	}
	for _, p := range got {
		if p.Cluster != "rev-proxy-pg" {
			t.Errorf("pooler %s associated with %q", p.Name, p.Cluster)
		}
		if p.Namespace != "ai-api-gateway" {
			t.Errorf("pooler %s namespace = %q", p.Name, p.Namespace)
		}
	}
}

// TestBuildPoolersFields is the R1.5 pooler field list plus the two fallbacks
// the CRD forces: spec.instances is optional (it defaults to 1) so
// status.replicas fills it, and the Service comes from the pooler's own name
// when spec.serviceTemplate is unset (CNPG names it after the Pooler).
func TestBuildPoolersFields(t *testing.T) {
	cr := clusterCR("ai-api-gateway", "rev-proxy-pg", map[string]any{"instances": int64(2)}, nil)
	pooler := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Pooler",
		"metadata":   map[string]any{"namespace": "ai-api-gateway", "name": "rev-proxy-pg-pooler"},
		"spec": map[string]any{
			"cluster":   map[string]any{"name": "rev-proxy-pg", "kind": "Cluster"},
			"type":      "rw",
			"instances": int64(2),
			"pgbouncer": map[string]any{
				"poolMode": "transaction",
				"parameters": map[string]any{
					"default_pool_size": "20",
					"max_client_conn":   "400",
					"min_pool_size":     "4",
				},
			},
		},
		"status": map[string]any{
			"phase":          "active",
			"image":          "ghcr.io/cloudnative-pg/pgbouncer:1.25.1",
			"readyInstances": int64(2),
			"replicas":       int64(2),
		},
	}}
	pods := []corev1.Pod{
		podFixture{Name: "rev-proxy-pg-pooler-86b4bdd84d-n9vlv", Namespace: "ai-api-gateway",
			Cluster: "rev-proxy-pg", PodRole: "pooler", PoolerName: "rev-proxy-pg-pooler",
			Node: "dmaf5-1", IP: "10.42.1.222", Phase: "Running", Ready: true}.pod(),
		podFixture{Name: "rev-proxy-pg-pooler-86b4bdd84d-xg6sg", Namespace: "ai-api-gateway",
			Cluster: "rev-proxy-pg", PodRole: "pooler", PoolerName: "rev-proxy-pg-pooler",
			Node: "k8s-wrk-1", IP: "10.42.4.92", Phase: "Running", Ready: true}.pod(),
	}
	got := buildPoolers(cr, []*unstructured.Unstructured{pooler}, pods)
	if len(got) != 1 {
		t.Fatalf("buildPoolers = %d, want 1", len(got))
	}
	p := got[0]
	if p.Type != "rw" || p.PoolMode != "transaction" || p.Phase != "active" ||
		p.Image != "ghcr.io/cloudnative-pg/pgbouncer:1.25.1" {
		t.Errorf("pooler spec/status fields = %+v", p)
	}
	if p.Instances != 2 || p.ReadyInstances != 2 {
		t.Errorf("instances/readyInstances = %d/%d, want 2/2", p.Instances, p.ReadyInstances)
	}
	// No spec.serviceTemplate: the Service is the pooler's own name, which is
	// what CNPG creates and what the fixture has.
	if p.Service != "rev-proxy-pg-pooler" {
		t.Errorf("service = %q, want rev-proxy-pg-pooler (CNPG names the pooler Service "+
			"after the Pooler itself)", p.Service)
	}
	if want := map[string]string{"default_pool_size": "20", "max_client_conn": "400", "min_pool_size": "4"}; !equalStringMaps(p.Parameters, want) {
		t.Errorf("parameters = %v, want %v", p.Parameters, want)
	}
	// R1.10's pooler row: the 9127 exporter endpoint, from the first pod's address.
	if p.MetricsURL != "http://10.42.1.222:9127" {
		t.Errorf("metricsURL = %q, want http://10.42.1.222:9127", p.MetricsURL)
	}
	if len(p.Pods) != 2 {
		t.Fatalf("pods = %d, want 2", len(p.Pods))
	}
	if p.Pods[0].Name != "rev-proxy-pg-pooler-86b4bdd84d-n9vlv" || p.Pods[0].IP != "10.42.1.222" ||
		!p.Pods[0].Ready || p.Pods[0].Phase != "Running" || p.Pods[0].Node != "dmaf5-1" {
		t.Errorf("pods[0] = %+v", p.Pods[0])
	}

	// The spec.instances fallback: a pooler whose spec omits the field (it
	// defaults to 1 in the CRD) still reports the observed replica count.
	noCount := poolerObj("ns", "p", map[string]any{"cluster": map[string]any{"name": "c"}})
	noCount.Object["status"] = map[string]any{"replicas": int64(3), "readyInstances": int64(2)}
	one := buildPoolers(clusterCR("ns", "c", nil, nil), []*unstructured.Unstructured{noCount}, nil)
	if len(one) != 1 || one[0].Instances != 3 || one[0].ReadyInstances != 2 {
		t.Errorf("status fallback: %+v, want instances 3 / readyInstances 2", one)
	}

	// readyInstances falls back to the pods when the CR has no number yet.
	podOnly := poolerObj("ns", "p", map[string]any{"cluster": map[string]any{"name": "c"}, "instances": int64(2)})
	podOnly.Object["status"] = map[string]any{"phase": "active"}
	podsOnly := []corev1.Pod{
		podFixture{Name: "p-1", Namespace: "ns", Cluster: "c", PodRole: "pooler", PoolerName: "p", Ready: true}.pod(),
		podFixture{Name: "p-2", Namespace: "ns", Cluster: "c", PodRole: "pooler", PoolerName: "p", Ready: false}.pod(),
	}
	one = buildPoolers(clusterCR("ns", "c", nil, nil), []*unstructured.Unstructured{podOnly}, podsOnly)
	if len(one) != 1 || one[0].ReadyInstances != 1 {
		t.Errorf("pod-derived readyInstances = %+v, want 1 ready of 2 pods", one)
	}

	// A pooler with no pods has no metrics URL rather than one built on an empty
	// host.
	bare := buildPoolers(clusterCR("ns", "c", nil, nil), []*unstructured.Unstructured{
		poolerObj("ns", "p", map[string]any{"cluster": map[string]any{"name": "c"}}),
	}, nil)
	if len(bare) != 1 || bare[0].MetricsURL != "" {
		t.Errorf("pod-less pooler = %+v, want no metricsURL", bare)
	}
}

// TestPoolerPodSet is R1.4's "their pods": the cnpg.io/poolerName label is the
// only signal that separates two poolers on one cluster, and the pre-1.26
// cluster+podRole labelling is used only when no pod carries it.
func TestPoolerPodSet(t *testing.T) {
	pooler := poolerObj("ns", "p-a", map[string]any{"cluster": map[string]any{"name": "c"}})

	// Tagged set: only the pods labelled p-a, even though p-b's pods are in the
	// same namespace and on the same cluster.
	tagged := []corev1.Pod{
		podFixture{Name: "p-a-1", Cluster: "c", PodRole: "pooler", PoolerName: "p-a"}.pod(),
		podFixture{Name: "p-b-1", Cluster: "c", PodRole: "pooler", PoolerName: "p-b"}.pod(),
	}
	got := poolerPodSet(pooler, tagged)
	if len(got) != 1 || got[0].Name != "p-a-1" {
		t.Errorf("tagged set = %+v, want [p-a-1] only (attaching p-b-1 here would double-count it)", got)
	}

	// Untagged set (pre-1.26): every pooler pod of the cluster is shown, because
	// there is no other signal. An instance pod is still excluded.
	untagged := []corev1.Pod{
		podFixture{Name: "c-1", Cluster: "c", PodRole: "instance"}.pod(),
		podFixture{Name: "legacy-pooler", Cluster: "c"}.pod(),
	}
	got = poolerPodSet(pooler, untagged)
	if len(got) != 1 || got[0].Name != "legacy-pooler" {
		t.Errorf("untagged set = %+v, want [legacy-pooler]", got)
	}
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
