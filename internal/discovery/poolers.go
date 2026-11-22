package discovery

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// R1.4: "Discover Pooler objects and their pods (cnpg.io/podRole=pooler),
// associating each to its spec.cluster.name."
//
// The association rule has a version wrinkle the requirement's wording papers
// over: `spec.cluster` was a plain string in CNPG <= 1.25 and became an object
// with a `name` field in 1.26+ (R1.4 names the object form). poolerClusterName
// reads both, so a pooler is attributed correctly on a 1.24-era CR and a 1.30
// one without a per-version branch at the call site.

// labelPoolerName is the CNPG 1.26+ label identifying which Pooler a pod
// belongs to. It is not in R1.4's text, which names only podRole=pooler, and it
// is the only signal that separates two poolers on one cluster.
const labelPoolerName = "cnpg.io/poolerName"

// poolerClusterName resolves a Pooler's associated cluster name from either
// spelling of spec.cluster: the object form spec.cluster.name (R1.4's wording,
// CNPG 1.26+) or the bare string form (CNPG <= 1.25).
//
// It returns ok=false when neither is present, which is a Pooler that cannot be
// attributed to a cluster; the caller drops it rather than guessing, because
// attaching a pooler to the wrong cluster puts its pods in the wrong row of the
// UI and there is no signal in the object that would let us pick right.
func poolerClusterName(obj map[string]any) (string, bool) {
	// Object form first: nestedStringE on spec.cluster.name succeeds only when
	// spec.cluster is a map, i.e. the 1.26+ spelling.
	if s, err := nestedStringE(obj, "spec", "cluster", "name"); err == nil && s != "" {
		return s, true
	}
	// String form: nestedStringE on spec.cluster succeeds only when
	// spec.cluster is a string.
	if s, err := nestedStringE(obj, "spec", "cluster"); err == nil && s != "" {
		return s, true
	}
	return "", false
}

// buildPoolers assembles the R1.5 "poolers" list for one cluster: the Pooler
// CRs associated with it (R1.4), each with the pods that belong to it.
//
// A Pooler whose spec.cluster names a different cluster is skipped here and
// picked up when that cluster's view is built.
func buildPoolers(cluster *unstructured.Unstructured, poolers []*unstructured.Unstructured, poolerPods []corev1.Pod) []dto.PoolerView {
	name := cluster.GetName()
	out := make([]dto.PoolerView, 0, len(poolers))
	for _, p := range poolers {
		obj := p.Object
		owner, ok := poolerClusterName(obj)
		if !ok || owner != name {
			continue
		}
		pods := poolerPodSet(p, poolerPods)
		v := dto.PoolerView{
			Name:      p.GetName(),
			Namespace: p.GetNamespace(),
			Cluster:   owner,
			Type:      nestedString(obj, "spec", "type"),
			PoolMode:  nestedString(obj, "spec", "pgbouncer", "poolMode"),
			Phase:     nestedString(obj, "status", "phase"),
			Image:     nestedString(obj, "status", "image"),
			Service:   nestedString(obj, "spec", "serviceTemplate", "metadata", "name"),
			Pods:      pods,
		}
		// spec.instances is optional in the CRD (it defaults to 1) and
		// status.replicas carries the observed count, so a pooler whose spec
		// omits the field still reports a number the UI can render.
		v.Instances = nestedInt(obj, "spec", "instances")
		if v.Instances == 0 {
			v.Instances = nestedInt(obj, "status", "replicas")
		}
		v.ReadyInstances = nestedInt(obj, "status", "readyInstances")
		if v.ReadyInstances == 0 {
			// Same fallback rule as ClusterView.ReadyInstances: the CR's number
			// wins when it has one, the pod-derived count fills it otherwise.
			v.ReadyInstances = countReadyPods(pods)
		}
		// pgbouncer.parameters is a map[string]string (default_pool_size,
		// min_pool_size, max_client_conn), which R6.2's pool sizing reads. It
		// is passed through verbatim rather than modelled field by field, so a
		// new pgbouncer parameter needs no change here.
		if params, err := nestedStringMapE(obj, "spec", "pgbouncer", "parameters"); err == nil {
			v.Parameters = params
		}
		// The pooler's 9127 exporter endpoint (R1.10's pooler row, S5's source
		// for R6). The DTO carries one URL per pooler, so the first pod's
		// address is used; a pooler with no pod yet has none.
		for _, pod := range pods {
			if pod.IP != "" {
				v.MetricsURL, _ = kube.URLFor(kube.OwnerPooler, 9127, pod.IP, false)
				break
			}
		}
		// A Pooler with no spec.serviceTemplate still has a Service in
		// practice, and CNPG names it after the Pooler itself — the fixture's
		// rev-proxy-pg-pooler Service belongs to the Pooler of the same name.
		// Deriving it keeps R6's connection surface populated for clusters that
		// do not customise the template.
		if v.Service == "" {
			v.Service = p.GetName()
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// poolerPodSet returns the pods belonging to one pooler.
//
// R1.4 says "their pods" and there are two ways to know which are theirs:
//  1. cnpg.io/poolerName == the pooler's name — the label CNPG 1.26+ puts on
//     every pooler pod, and the only one that distinguishes two poolers on the
//     same cluster.
//  2. cnpg.io/cluster == the cluster plus cnpg.io/podRole == pooler — the
//     pre-1.26 labelling, used only when no pod in the set carries the
//     poolerName label at all.
//
// The fallback is gated on the label being absent *altogether* rather than
// applied per pod: if some pods carry it, the ones that do not belong to a
// different pooler, and attaching them here would double-count them.
func poolerPodSet(pooler *unstructured.Unstructured, poolerPods []corev1.Pod) []dto.PoolerPodView {
	name := pooler.GetName()
	cluster, _ := poolerClusterName(pooler.Object)

	_, tagged := findPoolerTag(poolerPods)

	// Upper bound: the loop filters this cluster's pod set down to the pods
	// belonging to `pooler`, so the result can never be longer than the input.
	out := make([]dto.PoolerPodView, 0, len(poolerPods))
	for i := range poolerPods {
		pod := &poolerPods[i]
		if pod.Labels[labelPodRole] != "" && pod.Labels[labelPodRole] != podRolePooler {
			continue
		}
		if tagged {
			if pod.Labels[labelPoolerName] != name && pod.Labels["app.kubernetes.io/name"] != name {
				continue
			}
		} else if pod.Labels[labelCluster] != cluster {
			// Untagged fallback: every pooler pod of this cluster is shown on
			// every pooler row of this cluster. Imperfect with two poolers, and
			// the only signal available before 1.26.
			continue
		}
		out = append(out, buildPoolerPod(pod))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// findPoolerTag reports whether any pod carries the cnpg.io/poolerName label,
// and with which value. The second return is the first name seen, which is what
// makes the untagged branch's decision ("no pod carries it") a single pass.
func findPoolerTag(pods []corev1.Pod) (string, bool) {
	for i := range pods {
		if n := pods[i].Labels[labelPoolerName]; n != "" {
			return n, true
		}
	}
	return "", false
}

// buildPoolerPod converts a pooler pod to the DTO row (name, node, phase,
// ready, IP — the pod view has no container-status fields because a pooler pod
// has no second container whose death hides behind a Running pod, which is the
// whole reason InstanceView carries them).
func buildPoolerPod(pod *corev1.Pod) dto.PoolerPodView {
	v := dto.PoolerPodView{
		Name:  pod.Name,
		Node:  pod.Spec.NodeName,
		Phase: string(pod.Status.Phase),
		IP:    pod.Status.PodIP,
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.ContainersReady {
			v.Ready = c.Status == corev1.ConditionTrue
			break
		}
	}
	return v
}

// countReadyPods counts ready pooler pods.
func countReadyPods(pods []dto.PoolerPodView) int {
	n := 0
	for _, p := range pods {
		if p.Ready {
			n++
		}
	}
	return n
}
