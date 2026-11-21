// Package dto holds the JSON response contracts of the REST API (R8.1).
//
// These structs are the wire format the Web UI renders (R10), and they are
// API-critical: Phase 1b builds the Fleet page against them in parallel with
// the handlers, so a json tag changed here is a breaking change for the SPA.
// testdata/api/clusters.json is the byte-level contract — R16.8 names that
// path, and TestGoldenFixtureIsCanonical re-marshals the fixture and fails on
// any difference, so a field rename cannot land without the fixture being
// updated in the same commit. The SPA's Vitest suite consumes the same file.
//
// Naming follows the CNPG v1 API wherever a field has a CNPG counterpart
// (phase, phaseReason, currentPrimary, instances, readyInstances, pvcs) so
// that reading our response and reading `kubectl get cluster -o json` use the
// same vocabulary. Where requirements.md renames something — servingInstances
// is R1.2's correction to the CNPG counts — the spec's name wins.
package dto

import "time"

// Meta is the pagination/envelope metadata every list endpoint returns (R8.4).
type Meta struct {
	// Total is the number of matching objects before the page was cut.
	Total int `json:"total"`
	// Truncated is true when Data holds fewer objects than Total, i.e. there
	// is another page (R8.4).
	Truncated bool `json:"truncated"`
	// GeneratedAt is when this inventory snapshot was produced (R8.4). The
	// UI shows it as "inventory age", which is how a stale discovery cache
	// becomes visible instead of looking like an empty cluster list.
	GeneratedAt time.Time `json:"generatedAt"`
	// NextCursor is the opaque base64 cursor for the following page (R8.10).
	// Empty on the last page. Callers must treat it as opaque: it encodes the
	// sort key plus a secondary (namespace, name) key so paging is stable
	// while the inventory mutates.
	NextCursor string `json:"nextCursor,omitempty"`

	// Failed and Loading are R10.22's partial-render banner: the page that
	// shows 3 of 4 clusters has to say so instead of looking plausibly
	// complete. R8.4's envelope predates R10.22 and has no field for it, so
	// they live here rather than as a second top-level key, and the envelope
	// stays exactly R8.4's shape.
	//
	// The unit is the namespace, not the cluster, because that is the unit
	// discovery fails at (R1.1's allow-list and R12's RBAC both scope by
	// namespace): a namespace whose watch errored contributes no clusters, so
	// there is no ClusterView to hang an `error` field on. That is also why
	// meta.total counts what the response can serve (3), not what was
	// attempted (4) — "attempted" is total + len(failed) + len(loading), and
	// putting it in total would make total mean two different things.
	//
	// Truncated stays purely about pagination (R8.10): a page can be complete
	// and still have a failed namespace, and a truncated page can have zero
	// failures. The UI composes the banner sentence from these fields; there
	// is no server-side `message` because the wording is R10.22's, not the
	// API's.
	Failed  []NamespaceFailure `json:"failed,omitempty"`
	Loading []string           `json:"loading,omitempty"`
}

// NamespaceFailure is one namespace whose discovery pass errored (R10.22).
type NamespaceFailure struct {
	// Namespace is the namespace that failed to list/watch.
	Namespace string `json:"namespace"`
	// Code is from the same R8.5 enum as ErrorBody.Code — typically
	// ErrForbidden (the ServiceAccount cannot read that namespace, R12.3),
	// ErrUnavailable (kube API timeout) or ErrInternal. Reusing the enum is
	// deliberate: the UI already switches on these codes for whole-request
	// errors and should not need a second vocabulary for partial ones.
	Code string `json:"code"`
	// Message is the underlying error text, e.g. `cannot list clusters:
	// forbidden`.
	Message string `json:"message"`
	// LastSuccessTime is when this namespace last synced successfully, so the
	// UI can say "stale since 09:12" rather than only "failed". A pointer
	// because encoding/json's omitempty never fires on a struct value — a bare
	// time.Time would emit "0001-01-01T00:00:00Z" for "never synced" instead
	// of omitting the field.
	LastSuccessTime *time.Time `json:"lastSuccessTime,omitempty"`
}

// Envelope is the response wrapper for every list endpoint (R8.4).
type Envelope[T any] struct {
	Data []T  `json:"data"`
	Meta Meta `json:"meta"`
}

// ErrorBody is the single error shape for every endpoint (R8.5), modelled on
// the CNPG instance-manager envelope so the ecosystem's error-reading code
// transfers unchanged.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the body of ErrorBody.Error (R8.5).
type ErrorDetail struct {
	// Code is one of the Err* constants below; clients switch on it.
	Code string `json:"code"`
	// Message is a human-readable, single-sentence explanation.
	Message string `json:"message"`
	// Details carries per-item messages (validation failures, per-collector
	// errors). Always present, possibly empty, so clients never branch on
	// its existence.
	Details []string `json:"details"`
}

// Error code enum (R8.5 requires the codes be documented, so they are
// constants rather than string literals at each call site).
const (
	ErrInvalidRequest    = "InvalidRequest"    // bad query parameter or body
	ErrUnauthorized      = "Unauthorized"      // missing/bad token (R8.8)
	ErrForbidden         = "Forbidden"         // token lacks scope for the object (R12)
	ErrNotFound          = "NotFound"          // no such cluster/pooler/query
	ErrMethodNotAllowed  = "MethodNotAllowed"  // route exists, verb does not
	ErrNotReady          = "NotReady"          // discovery has not synced yet (R8.7)
	ErrUnavailable       = "Unavailable"       // dependency down (store, kube API)
	ErrPromQLUnavailable = "PromQLUnavailable" // R8.3: query outside the named catalogue
	ErrUnsupported       = "Unsupported"       // feature gated off by config
	ErrInternal          = "Internal"          // anything else
)

// NewError builds an ErrorBody for an error response. Always use it instead of
// composing the struct by hand: Details has no omitempty precisely so clients
// never have to branch on whether the array exists, and a nil slice marshals
// to `null` — which is a different JSON type from `[]` and breaks that
// guarantee. NewError normalises it to the empty slice.
func NewError(code, message string, details ...string) ErrorBody {
	if details == nil {
		details = []string{}
	}
	return ErrorBody{Error: ErrorDetail{Code: code, Message: message, Details: details}}
}

// ClusterView is one CloudNativePG Cluster as GET /api/v1/clusters renders it.
// The field set is R1.5's list, verbatim and in order.
type ClusterView struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	// Phase and PhaseReason are CNPG's status.phase / status.phaseReason
	// passed through as the human strings CNPG writes ("Cluster in healthy
	// state", "Not enough disk space") — R1.5 asks for the CNPG phase, not an
	// enum we invent, and the UI renders the string verbatim.
	Phase       string `json:"phase"`
	PhaseReason string `json:"phaseReason,omitempty"`

	// Instances is the desired count (spec.instances) and ReadyInstances
	// counts pods Kubernetes reports ready — both are CNPG's own numbers.
	// ServingInstances is R1.2's correction: pods whose `postgres` container
	// is actually running. On freeidp-pg the CR reports 2 and 0 are serving;
	// a collector that renders `instances` as health shows a green cluster
	// that is fully down (live-probe.md §6, Finding 9).
	Instances        int `json:"instances"`
	ReadyInstances   int `json:"readyInstances"`
	ServingInstances int `json:"servingInstances"`

	// CurrentPrimary is status.currentPrimary, empty while no pod is elected.
	CurrentPrimary string `json:"currentPrimary,omitempty"`

	// PGMajor is the PostgreSQL major version resolved per R1.3: the
	// app.kubernetes.io/version pod label first, SQL
	// current_setting('server_version_num') as the fallback, cached per
	// cluster. Null when neither source has answered yet — a 0 would render
	// as a real version in the Fleet table's "PG major" column, and R1.3's
	// whole point is that the answer is sometimes not known yet.
	PGMajor *int `json:"pgMajor"`
	// PGImage is the image the CR reports (status.image, falling back to
	// spec.imageName) — kept because the label's "17" cannot express 17.11,
	// and R2.4's matrix keys off the exact minor.
	PGImage string `json:"pgImage,omitempty"`
	// PGVersionDetail is the full version string when known ("17.11"),
	// derived from PGImage's tag or from SQL. Empty when unresolved.
	PGVersionDetail string `json:"pgVersionDetail,omitempty"`

	// InstanceList is R1.5's "instance list with roles, node names and ready
	// flags". It is named instanceList because `instances` above is the count
	// — which is what both CNPG's `kubectl get` column and R1.5 call
	// "instances".
	InstanceList []InstanceView `json:"instanceList"`

	// Conditions is status.conditions (R1.5).
	Conditions []ConditionView `json:"conditions"`
	// Certificates is R1.5's "cert expirations".
	Certificates CertificateExpirations `json:"certificates"`
	// PVCs is R1.5's "PVC list".
	PVCs []PVCView `json:"pvcs"`
	// Services is R1.5's "services" (the -rw/-ro/-r Services plus pooler ones).
	Services []ServiceView `json:"services"`
	// Poolers is R1.5's "any Poolers"; discovery rule in R1.4.
	Poolers []PoolerView `json:"poolers"`

	// CredentialSource records which of R1.8's four precedence levels
	// supplied this cluster's Postgres credentials: "secret-flag",
	// "secret-cluster", "configured", or "none". R1.8 requires the SQL
	// collectors to report `unconfigured` rather than fail when it is "none"
	// while non-SQL collectors keep working, so the resolution has to be
	// observable through the API — this is the field R1.6's zero-prerequisite
	// acceptance reads.
	CredentialSource string `json:"credentialSource"`

	// CreatedAt is metadata.creationTimestamp. Not in R1.5's list; the Fleet
	// table shows cluster age, and adding it after the contract freezes is a
	// change A4 has to react to, so it is here from the start.
	CreatedAt time.Time `json:"createdAt"`
}

// The R10.4 metric columns (TPS, p95 latency, connections %, disk %) and the
// "top finding" column are deliberately absent from ClusterView: they are
// Phase 5's R9 contract and Phase 6's insight engine, and the UI derives both
// the metric values and the healthy/degraded/down rollup from the counts above
// rather than reading a server-side judgement. Adding them here later is
// additive; guessing their shape now would commit Phase 1a to a contract Phase
// 5 owns.

// CredentialSource values (R1.8's precedence chain, most to least specific).
const (
	CredentialFlag    = "secret-flag"    // --credential-secret
	CredentialCluster = "secret-cluster" // <cluster>-app, the CNPG convention
	CredentialConfig  = "configured"     // the single configured credential
	CredentialNone    = "none"           // unconfigured: SQL collectors report it (R1.8)
)

// InstanceView is one instance pod of a Cluster (R1.2, R1.5).
type InstanceView struct {
	Name string `json:"name"`

	// Role is cnpg.io/instanceRole — primary | replica | unhealthy — read
	// per R1.2, falling back to the deprecated `role` label when only that is
	// present. Empty when the pod carries neither, which is the normal state
	// of a pod that has not been assigned a role yet (freeidp-pg-2).
	Role string `json:"role,omitempty"`

	// Node is spec.nodeName, empty while the pod is unscheduled (R1.5's
	// "node names").
	Node string `json:"node,omitempty"`

	// Ready is the pod's ContainersReady condition (R1.5's "ready flags").
	Ready bool `json:"ready"`
	// Serving is true only when the `postgres` container is running. It is
	// not the same as Ready: a pod can be Running with postgres in
	// CrashLoopBackOff, which is exactly freeidp-pg-1 (R1.2).
	Serving bool `json:"serving"`

	// Phase is the pod phase: Running | Pending | Succeeded | Failed | Unknown.
	Phase string `json:"phase"`
	// ContainerStatus renders the `postgres` container's state verbatim:
	// "running", "CrashLoopBackOff", "PodInitializing", "ImagePullBackOff",
	// "Terminating". This is the field that makes R1.2's crash-loop case
	// legible in the UI instead of just a wrong count.
	ContainerStatus string `json:"containerStatus"`
	// RestartCount is the postgres container's restartCount — 1551 on
	// freeidp-pg-1 is the number the UI shows beside the crash loop.
	RestartCount int `json:"restartCount"`

	// PodIP is status.podIP, empty while the pod has no address. R1.10's
	// direct transport needs it; Endpoints below is built from it.
	PodIP string `json:"podIP,omitempty"`

	// Fenced is true when the pod is listed in the Cluster's
	// cnpg.io/fencedInstances annotation — a pod CNPG is deliberately keeping
	// out of the topology, which the UI must not render as a healthy member.
	Fenced bool `json:"fenced,omitempty"`

	// StartedAt is the pod's startTime; omitted while Pending.
	StartedAt *time.Time `json:"startedAt,omitempty"`

	// Endpoints are the URLs pgsi uses to reach this instance, built from the
	// R1.10 per-port transport table. They are part of the contract because
	// the scheme is per-port data, not a guess: probing http://pod:8000
	// reports the status endpoint as down (live-probe.md §4, Finding 6).
	Endpoints InstanceEndpoints `json:"endpoints"`
}

// InstanceEndpoints is the R1.10 transport table rendered for one instance.
// Each field is a full URL, empty when the pod has no address to build one from.
type InstanceEndpoints struct {
	// Postgres is the 5432 wire address ("10.42.2.39:5432"), not an HTTP URL:
	// R1.10 types that port as the postgres protocol with password/TLS auth.
	Postgres string `json:"postgres,omitempty"`
	// Metrics is 9187 — the instance-manager Prometheus endpoint. HTTP, and
	// HTTPS only when spec.monitoring.tls.enabled (R1.10).
	Metrics string `json:"metrics,omitempty"`
	// Status is 8000 — the instance-manager API. HTTPS always (R1.10/F6);
	// an HTTP probe of this port returns "Client sent an HTTP request to an
	// HTTPS server."
	Status string `json:"status,omitempty"`
	// Local is 8010, the pod-local instance-manager endpoint that is not
	// reachable from outside the pod (R1.10). Populated only when the probe
	// path is exec/port-forward rather than a direct URL.
	Local string `json:"local,omitempty"`
}

// ConditionView is one entry of a Cluster's status.conditions (R1.5).
type ConditionView struct {
	Type    string `json:"type"`
	Status  string `json:"status"` // True | False | Unknown
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// LastTransitionTime is CNPG's lastTransitionTime.
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

// CertificateExpirations renders status.certificates (R1.5's "cert expirations").
// CNPG reports expirations as a map of secret name to timestamp; we re-emit the
// map rather than flattening it to three fields, because a cluster with extra
// secrets (a custom server cert, a pooler client cert) has more than three and
// the UI should show all of them.
type CertificateExpirations struct {
	// ClientCA, ServerTLSSecret and ReplicationTLSSecret are the secret names
	// CNPG records in status.certificates, empty when a cluster uses custom
	// secrets.
	ClientCA             string `json:"clientCA,omitempty"`
	ServerTLSSecret      string `json:"serverTLSSecret,omitempty"`
	ReplicationTLSSecret string `json:"replicationTLSSecret,omitempty"`
	// Expirations maps secret name to the certificate's notAfter, normalised
	// to RFC3339 UTC. CNPG writes these as Go's default time.String() format
	// ("2026-12-22 08:39:48 +0000 UTC"); the discovery layer parses and
	// re-emits RFC3339 so the UI never has to parse two time formats.
	Expirations map[string]string `json:"expirations"`
}

// PVCView is one PVC of a Cluster (R1.5's "PVC list"). CNPG names a PVC
// exactly after its instance, but Instance is carried explicitly rather than
// derived so a renamed or orphaned volume still renders against the right row.
type PVCView struct {
	Name         string   `json:"name"`
	Instance     string   `json:"instance,omitempty"`
	Size         string   `json:"size,omitempty"`
	StorageClass string   `json:"storageClass,omitempty"`
	AccessModes  []string `json:"accessModes,omitempty"`
	// Phase is Bound | Pending | Lost.
	Phase string `json:"phase,omitempty"`
}

// ServiceView is one Service of a Cluster (R1.5's "services"). CNPG creates
// -rw, -ro and -r per cluster plus one per Pooler.
type ServiceView struct {
	Name string `json:"name"`
	// Type is ClusterIP | LoadBalancer | NodePort.
	Type string `json:"type,omitempty"`
	// Role is rw | ro | r | pooler, read from the name suffix: CNPG's service
	// role label differs between 1.29 and 1.30, so the suffix is the stable
	// read and the label is not.
	Role  string        `json:"role,omitempty"`
	Ports []ServicePort `json:"ports,omitempty"`
	// ClusterIP is spec.clusterIP ("None" for headless services).
	ClusterIP string `json:"clusterIP,omitempty"`
}

// ServicePort is one port of a Service.
type ServicePort struct {
	Name     string `json:"name,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Port     int    `json:"port"`
	// TargetPort normalises Kubernetes' IntOrString to a string: "5432" for a
	// numeric port, "pgbouncer" for a named one.
	TargetPort string `json:"targetPort,omitempty"`
}

// PoolerView is a pgbouncer Pooler discovered per R1.4 and attached to its
// Cluster through spec.cluster.name. The same shape backs
// GET /api/v1/poolers (R8.2), which is why Namespace is carried even though
// the nested copy sits under a cluster that already has one.
type PoolerView struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Cluster is spec.cluster.name — R1.4's association rule.
	Cluster string `json:"cluster,omitempty"`
	// Type is spec.type: rw | ro.
	Type string `json:"type,omitempty"`
	// PoolMode is spec.pgbouncer.poolMode: transaction | session | statement.
	// R6's whole client-attribution problem keys off this value.
	PoolMode string `json:"poolMode,omitempty"`

	// Instances is spec.instances (the Deployment's replicas) and
	// ReadyInstances the pods actually ready — the same three-number pattern
	// as ClusterView, minus a serving count because a pooler pod has no
	// second container whose death hides behind a Running pod.
	Instances      int `json:"instances"`
	ReadyInstances int `json:"readyInstances"`
	// Phase is status.phase ("active", ...).
	Phase string `json:"phase,omitempty"`
	// Image is status.image, e.g. "ghcr.io/cloudnative-pg/pgbouncer:1.25.1".
	Image string `json:"image,omitempty"`
	// Service is the pooler's Service name — R6's connection surface.
	Service string `json:"service,omitempty"`
	// Parameters is spec.pgbouncer.parameters (default_pool_size,
	// max_client_conn, min_pool_size, ...), which R6.2's pool sizing reads.
	Parameters map[string]string `json:"parameters,omitempty"`
	// Pods are the pooler pods (cnpg.io/podRole=pooler, R1.4).
	Pods []PoolerPodView `json:"pods,omitempty"`
	// MetricsURL is the pgbouncer exporter endpoint on 9127 (R1.10's row for
	// pooler pods) — the S5 source for R6.
	MetricsURL string `json:"metricsURL,omitempty"`
}

// PoolerPodView is one pod of a Pooler Deployment.
type PoolerPodView struct {
	Name  string `json:"name"`
	Node  string `json:"node,omitempty"`
	Phase string `json:"phase"`
	Ready bool   `json:"ready"`
	IP    string `json:"ip,omitempty"`
}
