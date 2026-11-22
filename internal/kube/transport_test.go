package kube

import (
	"testing"
)

// TestTransportTable pins the R1.10 rows against the requirement text.
//
// R1.10 is explicit that the table is the requirement ("Per-port transport
// table as data, not as `if` statements") and lists four instance ports with
// their protocols, so the assertion is on the data itself: a row edited without
// a corresponding spec change — 8000 flipped to http, say, which is exactly the
// mistake R1.10 cites as measured in live-probe.md §4 F6 — fails here.
func TestTransportTable(t *testing.T) {
	want := []struct {
		port     int
		owner    string
		protocol string
		tlsFrom  string
		podLocal bool
		purpose  string
	}{
		// R1.10: `5432` = postgres wire (password/TLS)
		{5432, OwnerInstance, ProtocolPostgres, "", false, "postgres wire"},
		// R1.10: `9187` = HTTP, HTTPS only when spec.monitoring.tls.enabled
		{9187, OwnerInstance, ProtocolHTTP, "spec.monitoring.tls.enabled", false, "instance-manager metrics"},
		// R1.10: `8000` = HTTPS always
		{8000, OwnerInstance, ProtocolHTTPS, "", false, "instance-manager API"},
		// R1.10: `8010` = HTTP, pod-local only, not reachable externally
		{8010, OwnerInstance, ProtocolHTTP, "", true, "instance-manager local"},
		// R1.10's pooler row (R1.4's objects): pgbouncer metrics on 9127.
		{9127, OwnerPooler, ProtocolHTTP, "", false, "pgbouncer metrics"},
	}

	for _, w := range want {
		specs := Ports(w.owner)
		var found *PortSpec
		for i := range specs {
			if specs[i].Port == w.port {
				found = &specs[i]
				break
			}
		}
		if found == nil {
			t.Errorf("port %d (%s) missing from the %s table (R1.10)", w.port, w.purpose, w.owner)
			continue
		}
		if found.Protocol != w.protocol {
			t.Errorf("port %d: protocol = %q, want %q (R1.10)", w.port, found.Protocol, w.protocol)
		}
		if found.TLSFrom != w.tlsFrom {
			t.Errorf("port %d: tlsFrom = %q, want %q (R1.10: 9187 is HTTP unless spec.monitoring.tls.enabled)",
				w.port, found.TLSFrom, w.tlsFrom)
		}
		if found.PodLocal != w.podLocal {
			t.Errorf("port %d: podLocal = %v, want %v (R1.10: 8010 is not reachable externally)",
				w.port, found.PodLocal, w.podLocal)
		}
		if found.Name == "" {
			t.Errorf("port %d: name empty", w.port)
		}
	}

	// The tables must not overlap: a pooler pod has no 8000, an instance pod has
	// no 9127. Getting this wrong is how a collector scrapes a pooler's metrics
	// off an instance pod.
	if _, ok := specFor(OwnerPooler, 8000); ok {
		t.Error("the pooler table has an 8000 row; 8000 is the instance-manager API, which a pooler pod does not run")
	}
	if _, ok := specFor(OwnerInstance, 9127); ok {
		t.Error("the instance table has a 9127 row; 9127 is the pgbouncer exporter")
	}
}

func TestURLFor(t *testing.T) {
	cases := []struct {
		name          string
		owner         string
		port          int
		host          string
		monitoringTLS bool
		want          string
		wantOK        bool
	}{
		{"postgres is not a URL", OwnerInstance, 5432, "10.42.2.39", false, "10.42.2.39:5432", true},
		{"metrics over http", OwnerInstance, 9187, "10.42.2.39", false, "http://10.42.2.39:9187", true},
		{"metrics over https when monitoring.tls.enabled", OwnerInstance, 9187, "10.42.2.39", true, "https://10.42.2.39:9187", true},
		{"status is https always", OwnerInstance, 8000, "10.42.2.39", false, "https://10.42.2.39:8000", true},
		{"status ignores monitoringTLS", OwnerInstance, 8000, "10.42.2.39", true, "https://10.42.2.39:8000", true},
		// R1.10's 8010 row exists so that nothing builds http://pod:8010 and
		// then reports the port down: URLFor refuses instead.
		{"pod-local has no external URL", OwnerInstance, 8010, "10.42.2.39", false, "", false},
		{"pooler metrics", OwnerPooler, 9127, "10.42.3.7", false, "http://10.42.3.7:9127", true},
		{"unknown port", OwnerInstance, 9999, "10.42.2.39", false, "", false},
		// A Pending pod has no IP; the endpoints are omitted rather than
		// rendered as ":5432" (freeidp-pg-2 in the R16.8 fixture).
		{"no host", OwnerInstance, 5432, "", false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := URLFor(c.owner, c.port, c.host, c.monitoringTLS)
			if ok != c.wantOK || got != c.want {
				t.Errorf("URLFor(%q, %d, %q, tls=%v) = %q, %v; want %q, %v",
					c.owner, c.port, c.host, c.monitoringTLS, got, ok, c.want, c.wantOK)
			}
		})
	}

	// The named helpers are the API the discovery layer actually calls; they
	// must agree with the table.
	if got := PostgresAddress("10.42.2.39"); got != "10.42.2.39:5432" {
		t.Errorf("PostgresAddress = %q", got)
	}
	if got := MetricsURL("10.42.2.39", false); got != "http://10.42.2.39:9187" {
		t.Errorf("MetricsURL = %q", got)
	}
	if got := StatusURL("10.42.2.39"); got != "https://10.42.2.39:8000" {
		t.Errorf("StatusURL = %q", got)
	}
	if got := PoolerMetricsURL("10.42.3.7"); got != "http://10.42.3.7:9127" {
		t.Errorf("PoolerMetricsURL = %q", got)
	}
}

// TestPodsProxyURL pins R1.7's exact proxy path shape:
// /api/v1/namespaces/<ns>/pods/<scheme>:<pod>:<port>/proxy/<path>
func TestPodsProxyURL(t *testing.T) {
	cases := []struct {
		scheme, ns, pod string
		port            int
		path, want      string
	}{
		{
			"https", "freeidp", "freeidp-pg-1", 8000, "/metrics",
			"/api/v1/namespaces/freeidp/pods/https:freeidp-pg-1:8000/proxy/metrics",
		},
		{
			"http", "ai-api-gateway", "rev-proxy-pg-1", 9187, "/metrics",
			"/api/v1/namespaces/ai-api-gateway/pods/http:rev-proxy-pg-1:9187/proxy/metrics",
		},
		{
			// An empty path is "/" — "/proxy" with no trailing slash is a
			// different URL and the API server 404s it.
			"https", "cookbook", "cookbook-pg-1", 8000, "",
			"/api/v1/namespaces/cookbook/pods/https:cookbook-pg-1:8000/proxy/",
		},
		{
			// A path missing its leading slash gets one.
			"http", "cookbook", "cookbook-pg-1", 9187, "metrics",
			"/api/v1/namespaces/cookbook/pods/http:cookbook-pg-1:9187/proxy/metrics",
		},
	}
	for _, c := range cases {
		got := PodsProxyURL(c.scheme, c.ns, c.pod, c.port, c.path)
		if got != c.want {
			t.Errorf("PodsProxyURL(%q,%q,%q,%d,%q)\n got %s\nwant %s", c.scheme, c.ns, c.pod, c.port, c.path, got, c.want)
		}
	}
}

func TestClampTailLines(t *testing.T) {
	for _, c := range []struct{ in, want int64 }{
		{0, 100}, {-5, 100}, {1, 1}, {500, 500}, {10000, 10000}, {999999, 10000},
	} {
		if got := clampTailLines(c.in); got != c.want {
			t.Errorf("clampTailLines(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
