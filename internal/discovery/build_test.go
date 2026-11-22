package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// The builder tests cover R1.2's instance rules, R1.3's version rules and
// R1.5's field assembly. R1.4's Pooler rules are in poolers_test.go and the
// watch/cache layer in discovery_test.go.
//
// The fixture at testdata/api/clusters.json is the frozen contract (R16.8), so
// these tests are written against it rather than around it: TestBuildClusterMatchesFixture
// rebuilds every fixture row from inputs derived from that row and requires the
// result to marshal back byte-identically, and the freeidp-pg test spells out the
// §8 acceptance case field by field.

// clusterCR builds a Cluster object the way the API server hands one back: the
// spec and status sub-maps exactly as CNPG writes them.
func clusterCR(ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"namespace":         ns,
			"name":              name,
			"creationTimestamp": "2026-10-09T07:55:00Z",
		},
	}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

// fixturePath is R16.8's named location; the dto package's own test reads the
// same file (its golden test guards the struct tags, this one the builder).
// It is read with os.ReadFile directly by the tests that need it (the fixture
// is parsed into a dto.Envelope in one and into a cluster map in another, so a
// shared []byte helper had exactly zero reuse).
const fixturePath = "../../testdata/api/clusters.json"

// containerFixture is one entry of a pod's containerStatuses.
type containerFixture struct {
	Name     string
	State    string // running | CrashLoopBackOff | PodInitializing | ImagePullBackOff | Terminated
	Ready    bool
	Restarts int32
}

// podFixture is the pod shape BuildInstance reads, expressed once so every test
// states only the fields it is about.
type podFixture struct {
	Name       string
	Namespace  string
	Cluster    string // cnpg.io/cluster
	PodRole    string // cnpg.io/podRole
	Role       string // cnpg.io/instanceRole
	Deprecated string // the deprecated `role` label (R1.2's fallback)
	PoolerName string // cnpg.io/poolerName (pooler pods only, R1.4)
	Version    string // app.kubernetes.io/version (R1.3's first source)
	Node       string
	IP         string
	Phase      string
	Ready      bool
	StartTime  string // RFC3339; empty leaves startTime unset
	Containers []containerFixture
}

// pod returns the fixture as a value; podPtr is the same pod as a pointer, which
// is what BuildInstance and postgresContainer take (they never mutate it).
func (f podFixture) podPtr() *corev1.Pod {
	p := f.pod()
	return &p
}

func (f podFixture) pod() corev1.Pod {
	labels := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			labels[k] = v
		}
	}
	set(labelCluster, f.Cluster)
	set(labelPodRole, f.PodRole)
	set(labelInstanceRole, f.Role)
	set(labelRole, f.Deprecated)
	set(labelPoolerName, f.PoolerName)
	set(labelK8sVersion, f.Version)

	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: f.Name, Namespace: f.Namespace, Labels: labels,
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 10, 9, 7, 55, 0, 0, time.UTC)},
		},
		Spec:   corev1.PodSpec{NodeName: f.Node},
		Status: corev1.PodStatus{Phase: corev1.PodPhase(f.Phase)},
	}
	if f.IP != "" {
		p.Status.PodIP = f.IP
	}
	if f.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, f.StartTime); err == nil {
			p.Status.StartTime = &metav1.Time{Time: t}
		}
	}
	// The ContainersReady condition is R1.5's "ready" flag; a fixture that says
	// nothing about readiness gets False, which is the honest default for a pod
	// whose containers are not running.
	cond := corev1.ConditionFalse
	if f.Ready {
		cond = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.ContainersReady, Status: cond}}
	for _, c := range f.Containers {
		cs := corev1.ContainerStatus{Name: c.Name, Ready: c.Ready, RestartCount: c.Restarts}
		switch c.State {
		case "running":
			cs.State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		case "":
			// No state at all: a pod whose statuses are still empty.
		default:
			cs.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: c.State}}
		}
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, cs)
	}
	return p
}

// TestBuildClusterFreeIDP is the R1.2 acceptance case (§8 check 2), field by
// field: the reference cluster whose CR reports 2 instances and 0 ready, whose
// primary pod is Running with postgres in CrashLoopBackOff (1551 restarts) and
// whose second pod is Pending with no role and no address. R1.2's [A]
// amendment — read the per-pod container statuses, not status.instances — is
// the whole reason servingInstances exists.
func TestBuildClusterFreeIDP(t *testing.T) {
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var env dto.Envelope[dto.ClusterView]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var want *dto.ClusterView
	for i := range env.Data {
		if env.Data[i].Name == "freeidp-pg" {
			want = &env.Data[i]
		}
	}
	if want == nil {
		t.Fatal("fixture has no freeidp-pg cluster")
	}

	v, err := BuildCluster(inputsFromView(t, *want))
	if err != nil {
		t.Fatalf("BuildCluster: %v", err)
	}

	// R1.2's three counts, the reason the DTO has three of them.
	if v.Instances != 2 {
		t.Errorf("instances = %d, want 2 (spec.instances)", v.Instances)
	}
	if v.ReadyInstances != 0 {
		t.Errorf("readyInstances = %d, want 0", v.ReadyInstances)
	}
	if v.ServingInstances != 0 {
		t.Errorf("servingInstances = %d, want 0 (R1.2: a Running pod whose postgres "+
			"container is in CrashLoopBackOff is not serving — this is the number a "+
			"collector that trusts status.instances gets wrong)", v.ServingInstances)
	}
	if v.Phase != "Not enough disk space" {
		t.Errorf("phase = %q", v.Phase)
	}
	if v.CurrentPrimary != "freeidp-pg-1" {
		t.Errorf("currentPrimary = %q", v.CurrentPrimary)
	}
	// R1.3's third source (the image tag) with R1.5's detail rule: pgMajor 17 is
	// known, the minor is not, so pgVersionDetail stays empty — exactly what the
	// fixture has for this cluster and for this cluster only.
	if v.PGMajor == nil || *v.PGMajor != 17 {
		t.Fatalf("pgMajor = %v, want 17", v.PGMajor)
	}
	if v.PGVersionDetail != "" {
		t.Errorf("pgVersionDetail = %q, want empty (the image tag adds nothing to "+
			"pgMajor=17; the fixture leaves it empty here and fills it for every "+
			"label- or SQL-resolved cluster)", v.PGVersionDetail)
	}
	if v.CredentialSource != want.CredentialSource {
		t.Errorf("credentialSource = %q, want %q", v.CredentialSource, want.CredentialSource)
	}
	if len(v.InstanceList) != 2 {
		t.Fatalf("instanceList has %d entries, want 2", len(v.InstanceList))
	}
	i1 := v.InstanceList[0] // sorted by name, so [0] is freeidp-pg-1
	if i1.Name != "freeidp-pg-1" || i1.Role != "primary" || i1.Node != "k8s-ctrl" {
		t.Errorf("instance[0] = %+v", i1)
	}
	if i1.Serving || i1.Ready {
		t.Errorf("instance[0] serving=%v ready=%v, want both false", i1.Serving, i1.Ready)
	}
	if i1.Phase != "Running" || i1.ContainerStatus != "CrashLoopBackOff" || i1.RestartCount != 1551 {
		t.Errorf("instance[0] phase/status/restarts = %q/%q/%d, want Running/CrashLoopBackOff/1551",
			i1.Phase, i1.ContainerStatus, i1.RestartCount)
	}
	// R1.10's transport table rendered for this pod: 8000 is https (F6's
	// measured mistake is rendering it as http).
	if i1.Endpoints.Postgres != "10.42.0.198:5432" ||
		i1.Endpoints.Metrics != "http://10.42.0.198:9187" ||
		i1.Endpoints.Status != "https://10.42.0.198:8000" {
		t.Errorf("instance[0].endpoints = %+v", i1.Endpoints)
	}
	i2 := v.InstanceList[1]
	if i2.Name != "freeidp-pg-2" || i2.Role != "" || i2.Phase != "Pending" {
		t.Errorf("instance[1] = %+v, want name freeidp-pg-2, no role, Pending", i2)
	}
	if i2.ContainerStatus != "PodInitializing" {
		t.Errorf("instance[1].containerStatus = %q, want PodInitializing", i2.ContainerStatus)
	}
	if i2.PodIP != "" {
		t.Errorf("instance[1].podIP = %q, want empty", i2.PodIP)
	}
	// A pod with no address has no endpoints, and the fixture renders that as an
	// empty object rather than a missing key (every InstanceEndpoints field is
	// omitempty, so the object is there and empty).
	if b, err := json.Marshal(i2.Endpoints); err != nil || string(b) != "{}" {
		t.Errorf("instance[1].endpoints = %s (%v), want {}", b, err)
	}
	if len(v.Poolers) != 0 || v.Poolers == nil {
		t.Errorf("poolers = %v, want an empty non-nil slice (R8.4: data is always an array)", v.Poolers)
	}
}

// TestPGMajorSources is R1.3's precedence: the app.kubernetes.io/version pod
// label first, the cached SQL answer second, the image tag last — plus the
// acceptance's "16 vs 17 distinguished".
func TestPGMajorSources(t *testing.T) {
	// A cluster whose pods carry the label: the label wins over everything.
	withLabel := clusterCR("ns", "c", map[string]any{"imageName": "ghcr.io/cloudnative-pg/postgresql:16"}, nil)
	pods := []corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "c-1", Namespace: "ns",
			Labels: map[string]string{labelCluster: "c", labelPodRole: "instance", labelK8sVersion: "17.11"},
		},
	}}
	v, err := BuildCluster(ClusterInputs{Cluster: withLabel, Pods: pods})
	if err != nil {
		t.Fatal(err)
	}
	if v.PGMajor == nil || *v.PGMajor != 17 || v.PGVersionDetail != "17.11" {
		t.Errorf("label source: pgMajor=%v detail=%q, want 17/17.11 (the label outranks "+
			"the image tag, which says 16 — this is the 16-vs-17 distinction R1.3 makes)",
			v.PGMajor, v.PGVersionDetail)
	}

	// No pods: the cached SQL answer wins over the image tag.
	noPods := clusterCR("ns", "c", map[string]any{"imageName": "ghcr.io/cloudnative-pg/postgresql:16"}, nil)
	v, err = BuildCluster(ClusterInputs{Cluster: noPods, Cached: &PGVersion{Major: 16, Detail: "16.11", Source: "sql"}})
	if err != nil {
		t.Fatal(err)
	}
	if v.PGMajor == nil || *v.PGMajor != 16 || v.PGVersionDetail != "16.11" {
		t.Errorf("cache source: pgMajor=%v detail=%q, want 16/16.11", v.PGMajor, v.PGVersionDetail)
	}

	// No pods, no cache: the image tag is the last source.
	v, err = BuildCluster(ClusterInputs{Cluster: noPods})
	if err != nil {
		t.Fatal(err)
	}
	if v.PGMajor == nil || *v.PGMajor != 16 {
		t.Errorf("image source: pgMajor=%v, want 16", v.PGMajor)
	}

	// Nothing at all answers: null, not 0. R1.3's nullability is why the DTO
	// field is *int — a 0 renders in the Fleet table as PostgreSQL 0.
	empty := clusterCR("ns", "c", nil, nil)
	v, err = BuildCluster(ClusterInputs{Cluster: empty})
	if err != nil {
		t.Fatal(err)
	}
	if v.PGMajor != nil {
		t.Errorf("pgMajor = %d, want null", *v.PGMajor)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"pgMajor":null`) {
		t.Errorf("marshalled cluster has no `\"pgMajor\":null`: %.120s", b)
	}
}

// TestPGMajorFromVersion pins the parser that keeps the 16-vs-17 distinction
// honest across every source's spelling.
func TestPGMajorFromVersion(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"17", 17, true},
		{"17.11", 17, true},
		{"17.11-amd64", 17, true},
		{"PostgreSQL 16.4", 16, true},
		{" 16.11 ", 16, true},
		{"16.11 (Debian 16.11-1.pgdg120+1)", 16, true},
		// server_version_num, R1.3's SQL source: PostgreSQL numbers a release
		// major*10000+minor, so 160011 is 16.11. Without this rule the SQL
		// answer never resolves and the cache stays empty.
		{"160011", 16, true},
		{"170011", 17, true},
		{"100000", 10, true},
		// The guard band: a pgbouncer build tag ("1.25.1") or a stray "latest"
		// must not read as PostgreSQL 1, and 9.6 predates the CRD's supported set.
		{"1.25.1", 0, false},
		{"9.6", 0, false},
		{"21", 0, false},
		{"latest", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := pgMajorFromVersion(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("pgMajorFromVersion(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestImageTag covers the reference-parsing half of R1.3's third source.
func TestImageTag(t *testing.T) {
	cases := []struct{ image, want string }{
		{"ghcr.io/cloudnative-pg/postgresql:17", "17"},
		{"ghcr.io/cloudnative-pg/postgresql:16.11", "16.11"},
		{"registry:5000/postgres:15.4", "15.4"}, // the first colon is the registry port
		{"ghcr.io/cloudnative-pg/postgresql@sha256:abc", ""},
		{"ghcr.io/cloudnative-pg/postgresql", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := imageTagFromRef(c.image); got != c.want {
			t.Errorf("imageTagFromRef(%q) = %q, want %q", c.image, got, c.want)
		}
	}
	// imageTag is the CR-shaped wrapper over it (status.image, then spec.imageName).
	cr := clusterCR("ns", "c", map[string]any{"imageName": "ghcr.io/cloudnative-pg/postgresql:16"}, nil)
	if got := imageTag(cr); got != "16" {
		t.Errorf("imageTag(spec.imageName) = %q, want 16", got)
	}
	cr.SetUnstructuredContent(map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"name": "c", "namespace": "ns"},
		"spec":     map[string]any{"imageName": "ghcr.io/cloudnative-pg/postgresql:16"},
		"status":   map[string]any{"image": "ghcr.io/cloudnative-pg/postgresql:17.11"},
	})
	if got := imageTag(cr); got != "17.11" {
		t.Errorf("imageTag = %q, want 17.11 (status.image wins over spec.imageName)", got)
	}
}

// TestBuildInstanceRoleChain is R1.2's role rule: cnpg.io/instanceRole is the
// source, the deprecated `role` label is the fallback, and a pod with neither
// reports no role rather than a guess.
func TestBuildInstanceRoleChain(t *testing.T) {
	cases := []struct {
		name string
		f    podFixture
		want string
	}{
		{"instanceRole wins", podFixture{Name: "p", Cluster: "c", PodRole: "instance", Role: "primary", Deprecated: "replica"}, "primary"},
		{"deprecated role fallback", podFixture{Name: "p", Cluster: "c", PodRole: "instance", Deprecated: "replica"}, "replica"},
		{"neither label", podFixture{Name: "p", Cluster: "c", PodRole: "instance"}, ""},
		{"unhealthy is a real role", podFixture{Name: "p", Cluster: "c", PodRole: "instance", Role: "unhealthy"}, "unhealthy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildInstance(c.f.podPtr(), false)
			if got.Role != c.want {
				t.Errorf("role = %q, want %q", got.Role, c.want)
			}
			if got.Name != "p" {
				t.Errorf("name = %q", got.Name)
			}
		})
	}

	// The fenced flag comes from the cluster's annotation and is threaded in as
	// BuildInstance's second argument (it is cluster-scoped, so parsing it per
	// pod would repeat the same work per instance).
	fenced := BuildInstance(podFixture{Name: "p", Cluster: "c", PodRole: "instance", Role: "replica"}.podPtr(), true)
	if !fenced.Fenced {
		t.Error("fenced = false, want true")
	}
}

// TestPostgresContainer is the R1.2 [A] rule in isolation: serving comes from
// the postgres container's state, not from the pod phase.
func TestPostgresContainer(t *testing.T) {
	cases := []struct {
		name     string
		f        podFixture
		serving  bool
		status   string
		restarts int
	}{
		{"running pod, running container", podFixture{Phase: "Running", Containers: []containerFixture{{Name: "postgres", State: "running", Ready: true}}}, true, "running", 0},
		{"running pod, crash-looping container", podFixture{Phase: "Running", Containers: []containerFixture{{Name: "postgres", State: "CrashLoopBackOff", Restarts: 1551}}}, false, "CrashLoopBackOff", 1551},
		{"pending pod, no statuses", podFixture{Phase: "Pending"}, false, "PodInitializing", 0},
		{"pending pod, initializing container", podFixture{Phase: "Pending", Containers: []containerFixture{{Name: "postgres", State: "PodInitializing"}}}, false, "PodInitializing", 0},
		{"image pull failure", podFixture{Phase: "Pending", Containers: []containerFixture{{Name: "postgres", State: "ImagePullBackOff"}}}, false, "ImagePullBackOff", 0},
		// A pod whose only container is a sidecar has no postgres status, so it
		// cannot be counted as serving even while the pod reports Running.
		{"no postgres container", podFixture{Phase: "Running", Containers: []containerFixture{{Name: "envoy", State: "running", Ready: true}}}, false, "unknown", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			serving, status, restarts := postgresContainer(c.f.podPtr())
			if serving != c.serving || status != c.status || restarts != c.restarts {
				t.Errorf("postgresContainer = (%v, %q, %d), want (%v, %q, %d)",
					serving, status, restarts, c.serving, c.status, c.restarts)
			}
		})
	}
}

// TestSplitPods is R1.2's selector: pods labelled for this cluster with
// podRole=instance become instances, pooler pods come back separately, and pods
// labelled for a different cluster are dropped.
func TestSplitPods(t *testing.T) {
	pods := []corev1.Pod{
		podFixture{Name: "c-1", Cluster: "c", PodRole: "instance", Role: "primary", Phase: "Running", Containers: []containerFixture{{Name: "postgres", State: "running", Ready: true}}}.pod(),
		podFixture{Name: "c-2", Cluster: "c", PodRole: "instance", Role: "replica", Phase: "Running", Containers: []containerFixture{{Name: "postgres", State: "running", Ready: true}}}.pod(),
		podFixture{Name: "c-pooler-abc", Cluster: "c", PodRole: "pooler", Phase: "Running"}.pod(),
		podFixture{Name: "other-1", Cluster: "other", PodRole: "instance", Phase: "Running"}.pod(),
	}
	instances, poolerPods := splitPods(pods, "c", nil)
	if len(instances) != 2 {
		t.Fatalf("instances = %d, want 2 (c-1, c-2 only)", len(instances))
	}
	for _, i := range instances {
		if i.Name != "c-1" && i.Name != "c-2" {
			t.Errorf("unexpected instance %q", i.Name)
		}
	}
	if len(poolerPods) != 1 || poolerPods[0].Name != "c-pooler-abc" {
		t.Errorf("poolerPods = %v, want [c-pooler-abc]", poolerPods)
	}

	// A pod with no cnpg.io/cluster label at all is accepted into whichever
	// cluster asks: the label check only excludes pods labelled for a *different*
	// cluster, which is the tolerance R1.2's deprecated-label fallback needs.
	stray := []corev1.Pod{podFixture{Name: "stray", PodRole: "instance", Phase: "Running"}.pod()}
	got, _ := splitPods(stray, "c", nil)
	if len(got) != 1 {
		t.Errorf("unlabelled pod: instances = %d, want 1 (only a *conflicting* cluster label excludes)", len(got))
	}

	// Instances come back sorted by name, so the API's instanceList order is
	// stable across polls (the informer delivers pods in watch order).
	unsorted := []corev1.Pod{
		podFixture{Name: "c-3", Cluster: "c", PodRole: "instance"}.pod(),
		podFixture{Name: "c-1", Cluster: "c", PodRole: "instance"}.pod(),
		podFixture{Name: "c-2", Cluster: "c", PodRole: "instance"}.pod(),
	}
	got, _ = splitPods(unsorted, "c", nil)
	for i, want := range []string{"c-1", "c-2", "c-3"} {
		if got[i].Name != want {
			t.Errorf("instanceList[%d] = %q, want %q (sorted)", i, got[i].Name, want)
		}
	}
}

// TestFencedFromCluster covers the three spellings CNPG has used for the
// fencing annotation.
func TestFencedFromCluster(t *testing.T) {
	withAnnotation := func(v string) *unstructured.Unstructured {
		cr := clusterCR("ns", "c", nil, nil)
		cr.SetAnnotations(map[string]string{annotationFenced: v})
		return cr
	}
	// The JSON-array spelling is the current one and the only one that names
	// more than one instance.
	got := fencedFromCluster(withAnnotation(`["c-1","c-2"]`))
	if !got["c-1"] || !got["c-2"] || got["c-3"] {
		t.Errorf(`fenced(["c-1","c-2"]) = %v, want c-1 and c-2 only`, got)
	}
	// The wildcard fences every instance of the cluster, so the set holds the
	// "*" key and splitPods expands it.
	got = fencedFromCluster(withAnnotation("*"))
	if !got["*"] || len(got) != 1 {
		t.Errorf(`fenced("*") = %v, want {"*":true}`, got)
	}
	// The pre-1.22 comma list.
	got = fencedFromCluster(withAnnotation("c-1,c-3"))
	if !got["c-1"] || !got["c-3"] || got["c-2"] {
		t.Errorf(`fenced("c-1,c-3") = %v, want c-1 and c-3`, got)
	}
	if got := fencedFromCluster(clusterCR("ns", "c", nil, nil)); len(got) != 0 {
		t.Errorf("no annotation: fenced = %v, want empty", got)
	}

	// The wildcard must reach the instances it fences.
	pods := []corev1.Pod{
		podFixture{Name: "c-1", Cluster: "c", PodRole: "instance"}.pod(),
		podFixture{Name: "c-2", Cluster: "c", PodRole: "instance"}.pod(),
	}
	all, _ := splitPods(pods, "c", fencedFromCluster(withAnnotation("*")))
	for _, i := range all {
		if !i.Fenced {
			t.Errorf("%s not fenced by the wildcard annotation", i.Name)
		}
	}
}

// TestBuildClusterErrors pins the only three inputs BuildCluster refuses.
func TestBuildClusterErrors(t *testing.T) {
	if _, err := BuildCluster(ClusterInputs{}); err == nil {
		t.Error("nil cluster: want error")
	}
	nameless := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"namespace": "ns"},
	}}
	if _, err := BuildCluster(ClusterInputs{Cluster: nameless}); err == nil {
		t.Error("nameless cluster: want error")
	}
	// A Backup, not a Cluster, is refused rather than rendered as a cluster row.
	backup := clusterCR("ns", "b", nil, nil)
	backup.SetKind("Backup")
	if _, err := BuildCluster(ClusterInputs{Cluster: backup}); err == nil {
		t.Error("Backup object: want error")
	}
}

// TestEndpointsForIsTheTable renders R1.10's rows for one pod address. The
// requirement's last sentence — "every HTTP collector reads its scheme from this
// table" — is enforced structurally here: the DTO's endpoints and the collector
// URLs are both built by kube.URLFor, so there is no second place to get a
// scheme wrong.
func TestEndpointsFor(t *testing.T) {
	got := EndpointsFor("10.42.2.39", false)
	if got.Postgres != "10.42.2.39:5432" || got.Metrics != "http://10.42.2.39:9187" ||
		got.Status != "https://10.42.2.39:8000" {
		t.Errorf("endpoints = %+v", got)
	}
	if got.Local != "" {
		t.Errorf("local = %q, want empty (R1.10: 8010 is pod-local, not reachable externally)", got.Local)
	}
	tls := EndpointsFor("10.42.2.39", true)
	if tls.Metrics != "https://10.42.2.39:9187" {
		t.Errorf("metrics with monitoring.tls.enabled = %q, want https", tls.Metrics)
	}
	if tls.Status != "https://10.42.2.39:8000" {
		t.Errorf("status with monitoring.tls.enabled = %q, want https (8000 is https always)", tls.Status)
	}
	if empty := EndpointsFor("", false); empty != (dto.InstanceEndpoints{}) {
		t.Errorf("endpoints for a pod with no IP = %+v, want all empty", empty)
	}
}

// TestBuildClusterMatchesFixture is the R16.8 contract check at the builder's
// boundary: every cluster in testdata/api/clusters.json is rebuilt from inputs
// derived from the fixture itself and must marshal back to the fixture's bytes.
// That is what makes "the fixture is the contract" true of the builder and not
// only of the dto structs — a field the builder stops filling, or starts
// filling differently, fails here even though the struct still has the tag.
func TestBuildClusterMatchesFixture(t *testing.T) {
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var env dto.Envelope[dto.ClusterView]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("fixture does not unmarshal into Envelope[ClusterView]: %v", err)
	}
	if len(env.Data) != 4 {
		t.Fatalf("fixture has %d clusters, want the 4 reference clusters (§8)", len(env.Data))
	}
	for _, want := range env.Data {
		t.Run(want.Namespace+"/"+want.Name, func(t *testing.T) {
			got, err := BuildCluster(inputsFromView(t, want))
			if err != nil {
				t.Fatalf("BuildCluster: %v", err)
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("rebuilt cluster differs from the fixture: %s", firstDiff(string(gotJSON), string(wantJSON)))
			}
		})
	}
}

// inputsFromView reconstructs the Kubernetes inputs a fixture row was produced
// from: the CR (its status fields plus the spec counts), one pod per
// instanceList entry plus one per pooler pod, and the Services and PVCs it
// lists. Reconstructing the inputs from the output is what turns the fixture
// into a golden test of the builder rather than only of the struct: every field
// the row carries has to be reachable from a Kubernetes object, and every field
// the builder emits has to come from one.
func inputsFromView(t *testing.T, v dto.ClusterView) ClusterInputs {
	t.Helper()

	spec := map[string]any{"instances": int64(v.Instances)}
	if v.PGImage != "" {
		spec["imageName"] = v.PGImage
	}
	status := map[string]any{}
	if v.Phase != "" {
		status["phase"] = v.Phase
	}
	if v.PhaseReason != "" {
		status["phaseReason"] = v.PhaseReason
	}
	if v.CurrentPrimary != "" {
		status["currentPrimary"] = v.CurrentPrimary
	}
	if v.PGImage != "" {
		status["image"] = v.PGImage
	}
	// The CR's own readyInstances. R1.2 keeps it as its own number and only
	// falls back to the pod count when it is 0, so a fixture row with a nonzero
	// count has to put it back on the CR or the rebuild reads the pods instead.
	if v.ReadyInstances != 0 {
		status["readyInstances"] = int64(v.ReadyInstances)
	}
	if v.Certificates.ClientCA != "" || v.Certificates.ServerTLSSecret != "" ||
		v.Certificates.ReplicationTLSSecret != "" || len(v.Certificates.Expirations) > 0 {
		certs := map[string]any{}
		if v.Certificates.ClientCA != "" {
			certs["clientCA"] = v.Certificates.ClientCA
		}
		if v.Certificates.ServerTLSSecret != "" {
			certs["serverTLSSecret"] = v.Certificates.ServerTLSSecret
		}
		if v.Certificates.ReplicationTLSSecret != "" {
			certs["replicationTLSSecret"] = v.Certificates.ReplicationTLSSecret
		}
		if len(v.Certificates.Expirations) > 0 {
			exp := map[string]any{}
			for k, s := range v.Certificates.Expirations {
				exp[k] = s
			}
			certs["expirations"] = exp
		}
		status["certificates"] = certs
	}
	if len(v.Conditions) > 0 {
		conds := []any{}
		for _, c := range v.Conditions {
			conds = append(conds, map[string]any{
				"type":               c.Type,
				"status":             c.Status,
				"reason":             c.Reason,
				"message":            c.Message,
				"lastTransitionTime": c.LastTransitionTime.Format(time.RFC3339),
			})
		}
		status["conditions"] = conds
	}
	cr := clusterCR(v.Namespace, v.Name, spec, status)
	cr.SetCreationTimestamp(metav1.Time{Time: v.CreatedAt})

	// Fencing is cluster-scoped (the cnpg.io/fencedInstances annotation), so the
	// fenced instance rows are collected back into the annotation the builder
	// parses.
	var fenced []string
	for _, iv := range v.InstanceList {
		if iv.Fenced {
			fenced = append(fenced, iv.Name)
		}
	}
	if len(fenced) > 0 {
		b, err := json.Marshal(fenced)
		if err != nil {
			t.Fatal(err)
		}
		cr.SetAnnotations(map[string]string{annotationFenced: string(b)})
	}

	var pods []corev1.Pod
	for _, iv := range v.InstanceList {
		f := podFixture{
			Name: iv.Name, Namespace: v.Namespace, Cluster: v.Name, PodRole: "instance",
			Role: iv.Role, Version: v.PGVersionDetail, Node: iv.Node, IP: iv.PodIP,
			Phase: iv.Phase, Ready: iv.Ready,
		}
		// The version label is R1.3's first source and the fixture's detail is
		// only filled when a pod label or SQL answered, so putting the detail on
		// the pods reproduces the label source exactly. For freeidp-pg (detail
		// empty, image-tag source) the label is absent and the image tag answers.
		if iv.ContainerStatus != "" && iv.ContainerStatus != "unknown" {
			f.Containers = []containerFixture{{
				Name: "postgres", State: iv.ContainerStatus, Ready: iv.Ready, Restarts: int32(iv.RestartCount),
			}}
		}
		if iv.StartedAt != nil {
			f.StartTime = iv.StartedAt.Format(time.RFC3339)
		}
		pods = append(pods, f.pod())
	}
	for _, p := range v.Poolers {
		for _, pp := range p.Pods {
			pods = append(pods, podFixture{
				Name: pp.Name, Namespace: v.Namespace, Cluster: v.Name,
				PodRole: "pooler", PoolerName: p.Name,
				Node: pp.Node, IP: pp.IP, Phase: pp.Phase, Ready: pp.Ready,
			}.pod())
		}
	}

	var svcs []corev1.Service
	for _, s := range v.Services {
		sv := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: v.Namespace}}
		sv.Spec.Type = corev1.ServiceType(s.Type)
		sv.Spec.ClusterIP = s.ClusterIP
		for _, p := range s.Ports {
			sv.Spec.Ports = append(sv.Spec.Ports, corev1.ServicePort{
				Name: p.Name, Protocol: corev1.Protocol(p.Protocol),
				Port:       int32(p.Port),
				TargetPort: intstr.Parse(p.TargetPort),
			})
		}
		svcs = append(svcs, sv)
	}

	var pvcs []corev1.PersistentVolumeClaim
	for _, p := range v.PVCs {
		pv := corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: p.Name, Namespace: v.Namespace,
			Labels: map[string]string{labelCluster: v.Name, labelInstanceName: p.Instance},
		}}
		pv.Status.Phase = corev1.PersistentVolumeClaimPhase(p.Phase)
		pv.Spec.AccessModes = accessModesOf(p.AccessModes)
		if p.StorageClass != "" {
			sc := p.StorageClass
			pv.Spec.StorageClassName = &sc
		}
		if p.Size != "" {
			pv.Spec.Resources.Requests = corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse(p.Size),
			}
		}
		pvcs = append(pvcs, pv)
	}

	poolers := make([]*unstructured.Unstructured, 0, len(v.Poolers))
	for _, p := range v.Poolers {
		poolers = append(poolers, poolerCR(p))
	}

	return ClusterInputs{
		Cluster: cr, Pods: pods, Services: svcs, PVCs: pvcs, Poolers: poolers,
		CredentialSource: v.CredentialSource,
		Cached:           cachedFrom(v),
	}
}

// firstDiff describes the first byte where two JSON documents diverge, with
// enough context on both sides to name the field that changed. A whole-document
// dump of a 19 KB fixture buries the one field that moved.
func firstDiff(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	if i == len(got) && i == len(want) {
		return "identical"
	}
	const ctx = 60
	lo := i - ctx
	if lo < 0 {
		lo = 0
	}
	return fmt.Sprintf("at byte %d:\n got  ...%s\nwant ...%s", i, trimTail(got[lo:]), trimTail(want[lo:]))
}

func trimTail(s string) string {
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

// accessModesOf turns the DTO's strings back into the corev1 enum (the builder
// stringifies them, so the round trip has to start from the typed form).
func accessModesOf(modes []string) []corev1.PersistentVolumeAccessMode {
	out := make([]corev1.PersistentVolumeAccessMode, 0, len(modes))
	for _, m := range modes {
		out = append(out, corev1.PersistentVolumeAccessMode(m))
	}
	return out
}

// poolerCR rebuilds a Pooler CR from its DTO row, in the CNPG 1.26+ shape
// (spec.cluster as an object). R1.4's string spelling is covered in
// poolers_test.go.
func poolerCR(p dto.PoolerView) *unstructured.Unstructured {
	spec := map[string]any{
		"cluster": map[string]any{"name": p.Cluster},
	}
	if p.Type != "" {
		spec["type"] = p.Type
	}
	if p.Instances != 0 {
		spec["instances"] = int64(p.Instances)
	}
	pgb := map[string]any{}
	if p.PoolMode != "" {
		pgb["poolMode"] = p.PoolMode
	}
	if len(p.Parameters) > 0 {
		params := map[string]any{}
		for k, s := range p.Parameters {
			params[k] = s
		}
		pgb["parameters"] = params
	}
	if len(pgb) > 0 {
		spec["pgbouncer"] = pgb
	}
	if p.Service != "" {
		spec["serviceTemplate"] = map[string]any{
			"metadata": map[string]any{"name": p.Service},
		}
	}
	status := map[string]any{}
	if p.Phase != "" {
		status["phase"] = p.Phase
	}
	if p.Image != "" {
		status["image"] = p.Image
	}
	if p.ReadyInstances != 0 {
		status["readyInstances"] = int64(p.ReadyInstances)
	}
	obj := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Pooler",
		"metadata": map[string]any{
			"name": p.Name, "namespace": p.Namespace,
			"creationTimestamp": "2026-09-23T08:44:48Z",
		},
	}
	if len(spec) > 0 {
		obj["spec"] = spec
	}
	if len(status) > 0 {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

// cachedFrom returns the R1.3 per-cluster cache entry a fixture row implies.
// The cache only matters when no pod answered (R1.3's second source), so a row
// with instances needs none — their labels are the answer.
func cachedFrom(v dto.ClusterView) *PGVersion {
	if len(v.InstanceList) > 0 || v.PGVersionDetail == "" {
		return nil
	}
	major, ok := pgMajorFromVersion(v.PGVersionDetail)
	if !ok {
		return nil
	}
	return &PGVersion{Major: major, Detail: v.PGVersionDetail, Source: "sql"}
}

// The strconv import is used by the fixture-derived helpers' neighbours; keep
// the compiler honest about it if a future edit drops the last use.
var _ = strconv.Itoa
