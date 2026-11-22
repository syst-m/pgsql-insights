// Package discovery turns Kubernetes state into the inventory the API serves.
//
// R1.1's requirement is a watch over Cluster objects with a 30 s resync plus
// namespace allow/deny lists; R1.5's is the field set each cluster exposes.
// This package is split in three: discovery.go owns the watches and the cache,
// build.go owns the pure conversion from Kubernetes objects to the frozen
// dto.ClusterView, and fields.go owns the field readers the CRDs need. The
// split matters for testing — every R1.2/R1.3/R1.4 rule the acceptance criteria
// check lives in build.go and runs offline, with no cluster and no informers.
package discovery

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// CNPG's label keys (R1.2). The names are the contract CNPG documents; the
// deprecated `role` is the pre-1.20 spelling R1.2 requires us to fall back to.
const (
	labelCluster      = "cnpg.io/cluster"
	labelPodRole      = "cnpg.io/podRole"
	labelInstanceRole = "cnpg.io/instanceRole"
	labelRole         = "role"
	labelInstanceName = "cnpg.io/instanceName"
	labelK8sVersion   = "app.kubernetes.io/version"
	podRoleInstance   = "instance"
	podRolePooler     = "pooler"
	// annotationFenced is R1.5's fencing flag, written on the Cluster.
	annotationFenced = "cnpg.io/fencedInstances"
	// containerPostgres is the name of the PostgreSQL container in a CNPG
	// instance pod. R1.2's correction depends on reading *that* container's
	// status rather than the pod's aggregate.
	containerPostgres = "postgres"
)

// PGVersion is one entry of R1.3's per-cluster version cache.
type PGVersion struct {
	Major int
	// Detail is the full version string when known ("17.11").
	Detail string
	// Source records how it was resolved: "label" (app.kubernetes.io/version),
	// "sql" (current_setting('server_version_num'), R1.3's fallback) or
	// "image" (the image tag). R1.3 requires the answer be cached per cluster;
	// recording which source answered is what makes a wrong version
	// debuggable later.
	Source string
}

// ClusterInputs is everything discovery holds for one cluster. R1.5's field set
// is assembled from all of these: the CR alone answers phase/primary/certs, but
// servingInstances needs the pods (R1.2), services and PVCs come from their own
// objects, and poolers are separate CRs (R1.4).
type ClusterInputs struct {
	// Cluster is the postgresql.cnpg.io/v1 Cluster object.
	Cluster *unstructured.Unstructured
	// Pods are the cluster's pods, both instances and pooler pods; BuildCluster
	// splits them by cnpg.io/podRole (R1.2, R1.4).
	Pods []corev1.Pod
	// Services and PVCs are the cluster's own objects (R1.5).
	Services []corev1.Service
	PVCs     []corev1.PersistentVolumeClaim
	// Poolers are the Pooler CRs whose spec.cluster points at this cluster
	// (R1.4's association rule).
	Poolers []*unstructured.Unstructured
	// CredentialSource is R1.8's resolution for this cluster (dto.Credential*).
	CredentialSource string
	// Cached is R1.3's SQL-derived version, used when the pod label is absent.
	// Nil until the SQL collectors run, which is why the label is first.
	Cached *PGVersion
}

// BuildCluster assembles the API's view of one cluster (R1.5).
//
// It returns an error only when the Cluster object is unusable (no name, not a
// Cluster); per-field gaps are represented in the value, not as errors, because
// R1.6 requires a renderable inventory on a cluster where monitoring, poolers
// and credentials are all absent.
func BuildCluster(in ClusterInputs) (dto.ClusterView, error) {
	if in.Cluster == nil {
		return dto.ClusterView{}, fmt.Errorf("no cluster object")
	}
	if in.Cluster.GetKind() != "" && in.Cluster.GetKind() != "Cluster" {
		return dto.ClusterView{}, fmt.Errorf("object %s/%s is a %s, not a Cluster",
			in.Cluster.GetNamespace(), in.Cluster.GetName(), in.Cluster.GetKind())
	}
	name := in.Cluster.GetName()
	if name == "" {
		return dto.ClusterView{}, fmt.Errorf("cluster object has no name")
	}
	obj := in.Cluster.Object

	v := dto.ClusterView{
		Namespace: in.Cluster.GetNamespace(),
		Name:      name,
		Phase:     nestedString(obj, "status", "phase"),
		// R1.5 wants phaseReason too; freeidp-pg's "Not enough disk space" is
		// the acceptance case (§8 check 2).
		PhaseReason:      nestedString(obj, "status", "phaseReason"),
		Instances:        nestedInt(obj, "spec", "instances"),
		ReadyInstances:   nestedInt(obj, "status", "readyInstances"),
		CurrentPrimary:   nestedString(obj, "status", "currentPrimary"),
		PGImage:          clusterImage(in.Cluster),
		CredentialSource: in.CredentialSource,
		// .UTC() is not cosmetic. apimachinery's UnmarshalQueryParameter parses
		// metadata.creationTimestamp with time.ParseInLocation(..., time.Local),
		// so GetCreationTimestamp hands back the same instant in the process's
		// zone — "2026-09-23T01:44:48-07:00" on a laptop in PDT and
		// "2026-09-23T08:44:48Z" in the distroless container. The fixture pins
		// the UTC form, so the builder normalises instead of inheriting whatever
		// TZ the pod happens to run in.
		CreatedAt: in.Cluster.GetCreationTimestamp().Time.UTC(),
	}
	if v.CredentialSource == "" {
		// R1.8's fourth level: nothing configured. The field is never empty in
		// a response, because the UI switches on it to decide whether the SQL
		// tabs are available.
		v.CredentialSource = dto.CredentialNone
	}

	// R1.2: the CR's own readyInstances is kept as its own number, and
	// servingInstances is computed from the pod container statuses. On
	// freeidp-pg the CR reports 2/0 and the pods say 0 are serving.
	fenced := fencedFromCluster(in.Cluster)
	instances, poolerPods := splitPods(in.Pods, name, fenced)
	if instances == nil {
		// instanceList has no omitempty (R8.4's data is always an array, and the
		// UI maps over it), so a pod-less cluster renders [] rather than null.
		instances = []dto.InstanceView{}
	}
	v.InstanceList = instances
	v.ServingInstances = countServing(instances)
	if v.ReadyInstances == 0 {
		// A CR that predates status.readyInstances (or one whose status has not
		// been written yet) would otherwise render 0 ready while the pods say
		// otherwise. The pod-derived count is the fallback, never the other way
		// round: R1.2's whole point is that the CR's number can be wrong, so
		// the CR wins when the two disagree.
		v.ReadyInstances = countReady(instances)
	}

	// R1.3: the version, resolved label -> cache -> image tag, nullable.
	if pv := resolvePGVersion(in); pv != nil {
		v.PGMajor = &pv.Major
		// The detail is only emitted when it says more than pgMajor does. The
		// image-tag source answers "17" for a cluster whose pgMajor is already
		// 17, and the fixture leaves pgVersionDetail empty in exactly that case
		// (freeidp-pg) while filling it for every label- or SQL-resolved cluster
		// (rev-proxy-pg: "17.11"). Re-emitting "17" would make the Fleet table
		// read as if the minor were known when it is not.
		if pv.Detail != "" && pv.Detail != strconv.Itoa(pv.Major) {
			v.PGVersionDetail = pv.Detail
		}
	}

	v.Conditions = buildConditions(obj)
	v.Certificates = buildCertificates(obj)
	v.PVCs = buildPVCs(in.Cluster, in.PVCs)
	v.Services = buildServices(in.Cluster, in.Services)

	// R1.4's poolers, plus any pooler pods the caller passed in.
	v.Poolers = buildPoolers(in.Cluster, in.Poolers, poolerPods)
	if v.Poolers == nil {
		v.Poolers = []dto.PoolerView{}
	}
	return v, nil
}

// clusterImage is status.image falling back to spec.imageName (R1.5's "PG
// image"). CNPG writes status.image once the cluster is bootstrapped; before
// that only the spec has it, and the Fleet table would render a blank cell.
func clusterImage(cr *unstructured.Unstructured) string {
	if s := nestedString(cr.Object, "status", "image"); s != "" {
		return s
	}
	return nestedString(cr.Object, "spec", "imageName")
}

// resolvePGVersion implements R1.3's precedence: the app.kubernetes.io/version
// pod label first (no SELECT version() per poll), the per-cluster SQL cache
// second, and the image tag last. It returns nil when no source has answered,
// which the DTO renders as JSON null — a 0 would look like PostgreSQL 0.
func resolvePGVersion(in ClusterInputs) *PGVersion {
	// 1. The pod label (R1.3's first choice). Any instance pod answers for the
	// whole cluster: CNPG pins one image per cluster.
	for i := range in.Pods {
		if v := in.Pods[i].Labels[labelK8sVersion]; v != "" {
			if major, ok := pgMajorFromVersion(v); ok {
				return &PGVersion{Major: major, Detail: v, Source: "label"}
			}
		}
	}
	// 2. The cached SQL answer (R1.3's fallback, written by the SQL collectors
	// via current_setting('server_version_num')).
	if in.Cached != nil && in.Cached.Major > 0 {
		c := *in.Cached
		if c.Source == "" {
			c.Source = "sql"
		}
		return &c
	}
	// 3. The image tag. Not in R1.3's two-source list, but it is the only
	// remaining source that costs no query, and without it a cluster whose pods
	// have not been created yet renders pgMajor: null forever.
	if v := imageTag(in.Cluster); v != "" {
		if major, ok := pgMajorFromVersion(v); ok {
			return &PGVersion{Major: major, Detail: v, Source: "image"}
		}
	}
	return nil
}

// pgMajorFromVersion pulls the major out of a version string in any of the
// shapes R1.3's three sources produce:
//
//	"17", "17.11", "17.11-amd64"             — the app.kubernetes.io/version label
//	"PostgreSQL 16.4", "16.11 (Debian …)"    — server_version, as SQL and psql print it
//	"160011"                                 — server_version_num, R1.3's SQL source
//
// The 10..20 range guard is what stops a build tag ("1.25.1" for pgbouncer, or a
// stray "latest") from being read as PostgreSQL 1.
//
// The six-digit form needs its own rule because server_version_num is not a
// dotted version: PostgreSQL numbers a release major*10000+minor, so 160011 is
// 16.11 and 170011 is 17.11. Reading it as an integer would give major 160011
// and the range guard would reject it, which means the SQL fallback R1.3 names
// would never resolve — the cache would stay empty and every cluster would fall
// through to the image tag.
func pgMajorFromVersion(s string) (int, bool) {
	s = strings.TrimSpace(s)
	const prefix = "PostgreSQL "
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		s = s[len(prefix):]
	}
	// The leading run of digits and dots: "16.11 (Debian 16.11-1.pgdg120+1)" ->
	// "16.11", "17.11-amd64" -> "17.11", "160011" -> "160011".
	end := 0
	for end < len(s) && (s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	run := strings.Trim(s[:end], ".")
	if run == "" {
		return 0, false
	}
	digits := strings.SplitN(run, ".", 2)[0]
	major, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	if !strings.Contains(run, ".") && len(run) >= 6 {
		major /= 10000 // server_version_num: 160011 -> 16
	}
	if major < 10 || major > 20 {
		return 0, false
	}
	return major, true
}

// imageTag is the tag part of the cluster image ("ghcr.io/...postgresql:17" ->
// "17"), empty when the reference has no tag or is digest-pinned.
func imageTag(cr *unstructured.Unstructured) string {
	return imageTagFromRef(clusterImage(cr))
}

// imageTagFromRef is imageTag's pure-string half, so a DTO row's pgImage can be
// re-read without the CR it came from (discovery.go's R1.3 cache needs that).
func imageTagFromRef(img string) string {
	if img == "" {
		return ""
	}
	if i := strings.IndexByte(img, '@'); i >= 0 {
		return "" // digest-pinned: the tag is not in the reference
	}
	i := strings.LastIndex(img, ":")
	if i < 0 {
		return ""
	}
	// A colon inside the registry host (registry:5000/img) is not a tag.
	if slash := strings.LastIndex(img, "/"); i < slash {
		return ""
	}
	return img[i+1:]
}

// splitPods partitions pods into the cluster's instances (R1.2) and its pooler
// pods (R1.4), and drops pods belonging to another cluster.
//
// R1.2's selector is `cnpg.io/cluster=<name>,cnpg.io/podRole=instance`. Pods
// that carry the deprecated `role` label but no podRole are still instances —
// that fallback is in the requirement, and it is how a pre-1.20 cluster's pods
// are found at all.
//
// fenced is the parsed cnpg.io/fencedInstances annotation of the cluster the
// pods belong to. It is passed in rather than read per pod because the
// annotation is cluster-scoped and parsing it is not free.
func splitPods(pods []corev1.Pod, cluster string, fenced map[string]bool) (instances []dto.InstanceView, poolerPods []corev1.Pod) {
	for i := range pods {
		p := &pods[i]
		if p.Labels[labelCluster] != "" && p.Labels[labelCluster] != cluster {
			continue
		}
		isFenced := fenced["*"] || fenced[p.Name]
		switch p.Labels[labelPodRole] {
		case podRolePooler:
			poolerPods = append(poolerPods, *p)
		case podRoleInstance:
			instances = append(instances, BuildInstance(p, isFenced))
		default:
			// No podRole label: the deprecated `role` label marks an instance.
			if _, deprecated := p.Labels[labelRole]; deprecated || p.Labels[labelCluster] == cluster {
				instances = append(instances, BuildInstance(p, isFenced))
			}
		}
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	sort.Slice(poolerPods, func(i, j int) bool { return poolerPods[i].Name < poolerPods[j].Name })
	return instances, poolerPods
}

// BuildInstance converts one pod to the DTO row (R1.2, R1.5). The cluster name
// is not part of the row — the pod's labels carry it and the DTO nests instances
// under their cluster — so it is deliberately not a parameter.
func BuildInstance(p *corev1.Pod, fenced bool) dto.InstanceView {
	iv := dto.InstanceView{
		Name:  p.Name,
		Node:  p.Spec.NodeName,
		Phase: string(p.Status.Phase),
		PodIP: p.Status.PodIP,
	}
	// R1.2's role chain: cnpg.io/instanceRole, then the deprecated `role`.
	// Empty when the pod has neither, which is normal for a pod CNPG has not
	// assigned a role to yet (freeidp-pg-2 in the fixture).
	iv.Role = p.Labels[labelInstanceRole]
	if iv.Role == "" {
		iv.Role = p.Labels[labelRole]
	}
	iv.Fenced = fenced
	// The DTO's startedAt is the pod's startTime (R1.5), which is nil while the
	// pod is unscheduled — the field is then omitted rather than zeroed.
	if !p.Status.StartTime.IsZero() {
		t := p.Status.StartTime.Time
		iv.StartedAt = &t
	}
	iv.Ready = podReady(p)
	iv.Serving, iv.ContainerStatus, iv.RestartCount = postgresContainer(p)

	// R1.10's transport table, rendered for this pod's address. A Pending pod
	// has no IP, so every endpoint is empty and the JSON is "endpoints": {} —
	// which is exactly the fixture's freeidp-pg-2 shape.
	iv.Endpoints = EndpointsFor(p.Status.PodIP, false)
	return iv
}

// EndpointsFor renders the R1.10 transport table for a pod address.
// monitoringTLS is spec.monitoring.tls.enabled, the one conditional in the
// table (9187 is HTTP unless it is set). Every HTTP endpoint a collector later
// scrapes comes from here, which is R1.10's "every HTTP collector reads its
// scheme from this table" made structural.
func EndpointsFor(podIP string, monitoringTLS bool) dto.InstanceEndpoints {
	var e dto.InstanceEndpoints
	if podIP == "" {
		return e
	}
	e.Postgres, _ = kube.URLFor(kube.OwnerInstance, 5432, podIP, false)
	e.Metrics, _ = kube.URLFor(kube.OwnerInstance, 9187, podIP, monitoringTLS)
	e.Status, _ = kube.URLFor(kube.OwnerInstance, 8000, podIP, false)
	// 8010 is pod-local, so URLFor refuses and Local stays empty: the field
	// exists in the contract so the UI can show it as "exec only" rather than
	// as a link that 404s.
	e.Local, _ = kube.URLFor(kube.OwnerInstance, 8010, podIP, false)
	return e
}

// postgresContainer reads the `postgres` container's status, which is where
// R1.2's correction comes from. It returns (serving, statusString, restarts):
//
//   - serving is true only when that container is actually running. A pod can
//     be Running with postgres in CrashLoopBackOff (freeidp-pg-1), and a
//     cluster can report 2 instances with zero serving.
//   - statusString renders the container state verbatim: "running",
//     "CrashLoopBackOff", "ImagePullBackOff", "PodInitializing", "Terminated".
//   - restartCount is that container's restartCount (1551 on freeidp-pg-1).
//
// A pod with no container statuses yet (Pending, just scheduled) reports
// serving=false and status "PodInitializing" — the same answer the fixture
// gives for freeidp-pg-2.
func postgresContainer(p *corev1.Pod) (serving bool, status string, restarts int) {
	status = "unknown"
	if p.Status.Phase == corev1.PodPending {
		status = "PodInitializing"
	}
	for _, cs := range p.Status.ContainerStatuses {
		// CNPG names it `postgres`; the prefix match covers a pod spec that
		// renamed it (sidecar layouts from older operators) without the
		// instance row going blank.
		if cs.Name != containerPostgres && !strings.HasPrefix(cs.Name, containerPostgres) {
			continue
		}
		restarts = int(cs.RestartCount)
		switch {
		case cs.State.Waiting != nil:
			status = cs.State.Waiting.Reason
			if status == "" {
				status = "Waiting"
			}
		case cs.State.Running != nil:
			status = "running"
			serving = true
		case cs.State.Terminated != nil:
			status = cs.State.Terminated.Reason
			if status == "" {
				status = "Terminated"
			}
		}
		return serving, status, restarts
	}
	// No `postgres` container status. An init-container-only pod reports
	// PodInitializing; anything else is genuinely unknown.
	if p.Status.Phase == corev1.PodRunning {
		status = "unknown"
	}
	return false, status, restarts
}

// podReady is the pod's ContainersReady condition (R1.5's "ready flags").
func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.ContainersReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// fencedFromCluster parses the cluster's cnpg.io/fencedInstances annotation
// into a set keyed by pod name. CNPG writes it as a JSON array
// (["cluster-1","cluster-2"]), as the wildcard "*", and — in older versions —
// as a bare comma-separated list; all three spellings parse here so a fenced
// instance is fenced regardless of which the operator used.
func fencedFromCluster(cr *unstructured.Unstructured) map[string]bool {
	set := map[string]bool{}
	raw := strings.TrimSpace(cr.GetAnnotations()[annotationFenced])
	if raw == "" {
		return set
	}
	if raw == "*" {
		set["*"] = true
		return set
	}
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	for _, part := range strings.Split(raw, ",") {
		part = strings.Trim(strings.TrimSpace(part), `"`)
		if part != "" {
			set[part] = true
		}
	}
	return set
}

// countServing counts instances whose postgres container runs (R1.5's
// servingInstances). This is the number R1.2 exists to get right: it is not
// status.readyInstances and not the pod count.
func countServing(instances []dto.InstanceView) int {
	n := 0
	for _, i := range instances {
		if i.Serving {
			n++
		}
	}
	return n
}

// countReady counts instances with the pod's ContainersReady condition set.
func countReady(instances []dto.InstanceView) int {
	n := 0
	for _, i := range instances {
		if i.Ready {
			n++
		}
	}
	return n
}

// buildConditions converts status.conditions (R1.5).
func buildConditions(obj map[string]any) []dto.ConditionView {
	raw, ok := nestedSlice(obj, "status", "conditions")
	if !ok {
		return []dto.ConditionView{}
	}
	out := make([]dto.ConditionView, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, dto.ConditionView{
			Type:               mapString(m, "type"),
			Status:             mapString(m, "status"),
			Reason:             mapString(m, "reason"),
			Message:            mapString(m, "message"),
			LastTransitionTime: parseTime(mapString(m, "lastTransitionTime")),
		})
	}
	return out
}

// buildCertificates converts status.certificates (R1.5's "cert expirations").
// CNPG's expirations map holds Go's default time.String() format
// ("2026-12-22 08:39:48 +0000 UTC"); the DTO re-emits RFC3339 so the UI never
// parses two time formats.
func buildCertificates(obj map[string]any) dto.CertificateExpirations {
	c := dto.CertificateExpirations{
		ClientCA:             nestedString(obj, "status", "certificates", "clientCA"),
		ServerTLSSecret:      nestedString(obj, "status", "certificates", "serverTLSSecret"),
		ReplicationTLSSecret: nestedString(obj, "status", "certificates", "replicationTLSSecret"),
		Expirations:          map[string]string{},
	}
	// CNPG 1.29 renamed serverSecret -> serverTLSSecret; read both so a cluster
	// whose status still has the old key resolves.
	if c.ServerTLSSecret == "" {
		c.ServerTLSSecret = nestedString(obj, "status", "certificates", "serverSecret")
	}
	raw, err := nestedStringMapE(obj, "status", "certificates", "expirations")
	if err != nil || raw == nil {
		return c
	}
	for name, ts := range raw {
		if t := parseTime(ts); !t.IsZero() {
			c.Expirations[name] = t.UTC().Format(time.RFC3339)
		} else {
			// Unparseable: pass the raw string through rather than dropping the
			// entry, so the UI shows something odd instead of showing nothing
			// and implying the cert has no expiry.
			c.Expirations[name] = ts
		}
	}
	return c
}

// buildPVCs converts the cluster's PVCs (R1.5's "PVC list").
func buildPVCs(cr *unstructured.Unstructured, pvcs []corev1.PersistentVolumeClaim) []dto.PVCView {
	cluster := cr.GetName()
	out := make([]dto.PVCView, 0, len(pvcs))
	for i := range pvcs {
		p := &pvcs[i]
		if p.Labels[labelCluster] != "" && p.Labels[labelCluster] != cluster {
			continue
		}
		v := dto.PVCView{
			Name:         p.Name,
			Phase:        string(p.Status.Phase),
			StorageClass: derefString(p.Spec.StorageClassName),
			AccessModes:  accessModes(p),
		}
		// R1.5 needs the PVC tied to its instance. CNPG names a PVC after the
		// instance and labels it cnpg.io/instanceName; the label wins and the
		// name is the fallback, so a renamed or orphaned volume still renders
		// against the right row (the dto documents the same rule).
		v.Instance = p.Labels[labelInstanceName]
		if v.Instance == "" && strings.HasPrefix(p.Name, cluster+"-") {
			v.Instance = p.Name
		}
		if qty, ok := p.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			v.Size = qty.String()
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// accessModes flattens the access modes to strings (the DTO keeps them as
// strings so a new mode does not need a Go type change).
func accessModes(p *corev1.PersistentVolumeClaim) []string {
	out := make([]string, 0, len(p.Spec.AccessModes))
	for _, m := range p.Spec.AccessModes {
		out = append(out, string(m))
	}
	return out
}

// buildServices converts the cluster's Services (R1.5's "services").
func buildServices(cr *unstructured.Unstructured, svcs []corev1.Service) []dto.ServiceView {
	cluster := cr.GetName()
	out := make([]dto.ServiceView, 0, len(svcs))
	for i := range svcs {
		s := &svcs[i]
		if s.Labels[labelCluster] != "" && s.Labels[labelCluster] != cluster {
			continue
		}
		v := dto.ServiceView{
			Name:      s.Name,
			Type:      string(s.Spec.Type),
			ClusterIP: s.Spec.ClusterIP,
			// The role comes from the name suffix: CNPG's service role label
			// differs between 1.29 and 1.30, so the suffix is the stable read.
			Role: serviceRole(cluster, s.Name),
		}
		for _, port := range s.Spec.Ports {
			v.Ports = append(v.Ports, dto.ServicePort{
				Name:       port.Name,
				Protocol:   string(port.Protocol),
				Port:       int(port.Port),
				TargetPort: port.TargetPort.String(),
			})
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// serviceRole is rw | ro | r | pooler, read from the CNPG name suffix.
func serviceRole(cluster, name string) string {
	switch strings.TrimPrefix(name, cluster) {
	case "-rw":
		return "rw"
	case "-ro":
		return "ro"
	case "-r":
		return "r"
	}
	if strings.HasSuffix(name, "-pooler") {
		return "pooler"
	}
	return ""
}

// parseTime accepts the formats CNPG writes: RFC3339 (conditions, and the
// fixture's normalised form) and Go's default time.String() (the certificate
// expirations map).
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	// "2026-12-22 08:39:48 +0000 UTC" — what status.certificates.expirations
	// contains, because CNPG serialises that map with Go's String() format.
	if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	return time.Time{}
}

// derefString flattens an optional string field (a PVC without a
// storageClassName has a nil pointer, not an empty one).
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
