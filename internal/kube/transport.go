// Package kube is the Kubernetes access layer: the rest.Config, the clientset,
// the pods/proxy helper (R1.7) and the per-port transport table (R1.10).
//
// The package exists as a seam so that nothing outside it needs to know how a
// pod is reached. R1.10's requirement is specifically that the answer is a
// table and not `if` statements, and R1.7's is that there are two HTTP
// transports with an automatic selection between them; both are encoded here as
// data and one method, so a collector asking for "the metrics endpoint of this
// pod" cannot accidentally invent its own scheme.
package kube

import (
	"fmt"
	"strconv"
)

// Protocol names the transport a port speaks (R1.10's "protocol" column).
// Postgres is not HTTP: rendering 5432 as "http://pod:5432" is the mistake the
// table exists to prevent.
const (
	ProtocolPostgres = "postgres"
	ProtocolHTTP     = "http"
	ProtocolHTTPS    = "https"
)

// Owner separates the instance table from the pooler one; R1.10 lists ports for
// both kinds of pod and a pooler pod has none of the instance rows.
const (
	OwnerInstance = "instance"
	OwnerPooler   = "pooler"
)

// PortSpec is one row of the R1.10 per-port transport table.
//
// R1.10 (verbatim): "Per-port transport table as data, not as `if` statements.
// The endpoint scheme is resolved from a table: `5432` = postgres wire
// (password/TLS); `9187` = HTTP, HTTPS only when `spec.monitoring.tls.enabled`;
// `8000` = HTTPS always; `8010` = HTTP, pod-local only, not reachable
// externally. A collector that probes `http://pod:8000` reports the status
// endpoint as down — measured (live-probe.md §4, F6). Every HTTP collector
// reads its scheme from this table."
//
// The struct is exported and JSON-tagged so the table itself is serialisable:
// it is the kind of data that belongs in a `GET /api/v1/ports` later, and
// TestTransportTable asserts the rows against the requirement text so editing a
// row without editing the requirement is caught.
type PortSpec struct {
	// Port is the container port number.
	Port int `json:"port"`
	// Name is the port's name. CNPG declares `postgres` (5432) and `metrics`
	// (9187) as named containerPorts on the pod spec; 8000 and 8010 are served
	// by the instance-manager process and are not always declared, so those
	// two names are ours, not CNPG's.
	Name string `json:"name"`
	// Owner is instance or pooler.
	Owner string `json:"owner"`
	// Protocol is postgres | http | https.
	Protocol string `json:"protocol"`
	// TLSFrom is the field that flips Protocol from http to https, empty when
	// the protocol is fixed. R1.10 has exactly one entry: the 9187 row is HTTP
	// unless spec.monitoring.tls.enabled. Reading it here rather than at each
	// call site is what stops one collector from guessing.
	TLSFrom string `json:"tlsFrom,omitempty"`
	// PodLocal is true for a port not reachable from outside the pod (R1.10's
	// 8010). The only ways to read it are `pods/exec` or a port-forward, so
	// URLFor refuses to hand back a URL for external addressing rather than
	// returning one that cannot connect.
	PodLocal bool `json:"podLocal,omitempty"`
	// Auth documents what the transport needs to authenticate.
	Auth string `json:"auth,omitempty"`
	// UsedBy names the consumers of this row (R1.10's implicit "used by"
	// column) so adding a port can be reviewed against who reads it.
	UsedBy []string `json:"usedBy,omitempty"`
}

// InstancePorts is R1.10's table for a CloudNativePG instance pod.
var InstancePorts = []PortSpec{
	{
		Port:     5432,
		Name:     "postgres",
		Owner:    OwnerInstance,
		Protocol: ProtocolPostgres,
		Auth:     "password and/or client certificate (R1.8's credential chain)",
		UsedBy:   []string{"sql collectors (R2)", "pg-status fixture collector (R16.3)"},
	},
	{
		Port:     9187,
		Name:     "metrics",
		Owner:    OwnerInstance,
		Protocol: ProtocolHTTP,
		TLSFrom:  "spec.monitoring.tls.enabled",
		Auth:     "none when HTTP; client certificate when spec.monitoring.tls.enabled",
		UsedBy:   []string{"cnpg_* exposition collector (R2.6)", "re-export (R9.8)"},
	},
	{
		Port:     8000,
		Name:     "instance-manager-api",
		Owner:    OwnerInstance,
		Protocol: ProtocolHTTPS,
		Auth:     "client certificate signed by the cluster server CA",
		UsedBy:   []string{"instance-manager API (R5.10 logs, R1.7 proxy path)"},
	},
	{
		// R1.10: HTTP, pod-local only. Reachable from inside the pod (and via
		// pods/exec or a port-forward) but not from another pod, so it is in
		// the table precisely so that nothing builds http://pod:8010 and
		// reports it down.
		Port:     8010,
		Name:     "instance-manager-local",
		Owner:    OwnerInstance,
		Protocol: ProtocolHTTP,
		PodLocal: true,
		Auth:     "none",
		UsedBy:   []string{"instance-manager local endpoint (exec/port-forward only)"},
	},
}

// PoolerPorts is R1.10's table for a pgbouncer Pooler pod (R1.4's objects).
var PoolerPorts = []PortSpec{
	{
		Port:     9127,
		Name:     "metrics",
		Owner:    OwnerPooler,
		Protocol: ProtocolHTTP,
		Auth:     "none",
		UsedBy:   []string{"pgbouncer exporter (R6, S5)"},
	},
	{
		Port:     5432,
		Name:     "pgbouncer",
		Owner:    OwnerPooler,
		Protocol: ProtocolPostgres,
		Auth:     "password from the pooler secret (R6.1)",
		UsedBy:   []string{"connection surface (R6)"},
	},
}

// Ports returns the table for an owner: InstancePorts or PoolerPorts. It is the
// single accessor the rest of the code uses, so the two tables can never be
// consulted in the wrong order for a pod kind.
func Ports(owner string) []PortSpec {
	if owner == OwnerPooler {
		return PoolerPorts
	}
	return InstancePorts
}

// specFor looks a port up in the owner's table.
func specFor(owner string, port int) (PortSpec, bool) {
	for _, p := range Ports(owner) {
		if p.Port == port {
			return p, true
		}
	}
	return PortSpec{}, false
}

// URLFor returns the address string for one table row at a given host, plus
// whether the row is addressable from outside the pod at all.
//
// The return type is a string rather than a *url.URL because the postgres row is
// not a URL: R1.10 types 5432 as the postgres wire protocol, so the correct
// rendering is "10.42.2.39:5432" and forcing "postgres://10.42.2.39:5432" onto
// it would be inventing a URI scheme (libpq's connection string is not a URL
// scheme) that the UI then has to special-case.
func URLFor(owner string, port int, host string, monitoringTLS bool) (string, bool) {
	spec, ok := specFor(owner, port)
	if !ok || host == "" {
		return "", false
	}
	if spec.PodLocal {
		return "", false
	}
	return spec.url(host, monitoringTLS), true
}

// url renders the address for a row. It is split out from URLFor so the
// pod-local rejection above stays the only place that decides addressability.
func (p PortSpec) url(host string, monitoringTLS bool) string {
	if p.Protocol == ProtocolPostgres {
		return host + ":" + strconv.Itoa(p.Port)
	}
	scheme := p.Protocol
	// R1.10's one conditional: 9187 is HTTP, HTTPS only when the CR enables
	// monitoring TLS. TLSFrom names the field; the caller resolves it from the
	// Cluster CR and passes the boolean here, so this function never has to
	// re-read the CR.
	if p.TLSFrom != "" && monitoringTLS {
		scheme = ProtocolHTTPS
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, p.Port)
}

// PostgresAddress is the "host:5432" wire address of an instance pod.
func PostgresAddress(host string) string {
	u, _ := URLFor(OwnerInstance, 5432, host, false)
	return u
}

// MetricsURL is instance-manager's Prometheus endpoint (9187) for an instance
// pod. monitoringTLS is the CR's spec.monitoring.tls.enabled (R1.10).
func MetricsURL(host string, monitoringTLS bool) string {
	u, _ := URLFor(OwnerInstance, 9187, host, monitoringTLS)
	return u
}

// StatusURL is instance-manager's API endpoint (8000), HTTPS always (R1.10).
func StatusURL(host string) string {
	u, _ := URLFor(OwnerInstance, 8000, host, false)
	return u
}

// PoolerMetricsURL is the pgbouncer exporter endpoint (9127) of a pooler pod.
func PoolerMetricsURL(host string) string {
	u, _ := URLFor(OwnerPooler, 9127, host, false)
	return u
}

// PodsProxyURL builds the Kubernetes API path for one pod port — R1.7's second
// transport, whose exact shape the requirement spells out:
//
//	/api/v1/namespaces/<ns>/pods/<scheme>:<pod>:<port>/proxy/<path>
//
// The scheme is part of the path, which is why R1.10's table has to be
// consulted to build the URL at all: asking the API server to proxy
// http:pod:8000 gets the same "Client sent an HTTP request to an HTTPS server"
// answer as probing it directly.
//
// R1.11's caveat is documented on Client.Get: this path works when the API
// server can route to the pod CIDR, which is true in-cluster and unreliable
// through a tunnelled API endpoint.
func PodsProxyURL(scheme, namespace, pod string, port int, path string) string {
	if path == "" {
		path = "/"
	}
	if path[0] != '/' {
		path = "/" + path
	}
	return fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%s:%d/proxy%s",
		namespace, scheme, pod, port, path)
}
