package kube

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	restclient "k8s.io/client-go/rest"
	coretesting "k8s.io/client-go/testing"
)

// caPEM is a throwaway self-signed certificate, standing in for the cluster CA
// a real kubeconfig points at. kubernetes.NewForConfig parses it eagerly, so a
// placeholder string is not enough — TestNew fails with "unable to parse bytes
// as PEM block" unless the fixture carries a real PEM.
const caPEM = `-----BEGIN CERTIFICATE-----
MIICqjCCAZICCQCesEC4X3WrIjANBgkqhkiG9w0BAQsFADAXMRUwEwYDVQQDDAxw
Z3NpLXRlc3QtY2EwHhcNMjYxMTIxMDcwMDQ0WhcNMzYxMTE4MDcwMDQ0WjAXMRUw
EwYDVQQDDAxwZ3NpLXRlc3QtY2EwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEK
AoIBAQDDSzfJja5rClF4wIW/skSpgm5HhgwM313u6yMgZlrS5CDxuCcih8+DwOAg
eY+ugB/BAUAyZdtYIv9xjHN3soKI16NUV6DDWKy3EyiQpfJTTtNi89QS6bYrEwxq
+hIUs+aqzvO14Ml9cbhdrra/kt3cfWWPfmLjbKF5YRIuG6kp0D4/Yyv9yOj2a3D0
H7A/Rm81s28yWuH7DnF8v2FGONFmjmqWD1EcSqHgFueZyCzUoSK+zKCvRz5Cr+EU
toA+nrKVfLMTCawOcaf0p1mlocMCoipRFNWKA8RpuvjL0WJabxASGk3oZHdCzHVr
SZK6NcTwteocsjUsSY04pykAWI5TAgMBAAEwDQYJKoZIhvcNAQELBQADggEBABlA
pZD9Yuq9UOSVESD4rm2WpGOqRqoVu46oqFl4WOgufisUNzGtu8HTXwCYCS3+0tRO
465EMtRGZhWkFC4zhrhwCr0WxjdtzNMD79FNPlH34ZBp9of2EONnB4U+39A/pZIs
gKlviivWMaP4hAN6pmJJZM0f8xkbFLBOm2fWB0Dh3zZ2O7Dbuk84kBQM86R1FGjp
0imow/YziqWcSoksRpZMJkKfH/BjKuLN9FGR96se3v/Jy/xJahYQKwzb18tY8YPw
/kII4HxAfkD/nIW2iV0bH7B3MvfydtiYw7jnM69yDQRDk1bSCpHm/WWxixF4/I9f
um5ozm+1jv74YsHv0NE=
-----END CERTIFICATE-----`

// kubeconfigYAML is a two-context kubeconfig: the shape R1.7's --kube-context
// flag exists for. The reference environment has a direct k3s context plus
// tunnelled ones, and picking the wrong context is the failure mode R1.11
// documents. __CA__ is replaced with the path of caPEM by writeKubeconfig.
const kubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- name: k3s-direct
  cluster:
    server: https://192.168.177.20:6443
    certificate-authority: __CA__
- name: tunnel
  cluster:
    server: https://tunnel.example:9443
    insecure-skip-tls-verify: true
contexts:
- name: k3s-direct
  context:
    cluster: k3s-direct
    user: admin
- name: tunnel
  context:
    cluster: tunnel
    user: admin
current-context: k3s-direct
users:
- name: admin
  user:
    token: tok-direct
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(ca, []byte(caPEM), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	p := filepath.Join(dir, "kubeconfig")
	body := strings.ReplaceAll(kubeconfigYAML, "__CA__", ca)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return p
}

func TestBuildConfigFromKubeconfig(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "") // force the out-of-cluster branch

	p := writeKubeconfig(t)

	t.Run("current-context", func(t *testing.T) {
		rc, err := BuildConfig(Config{Kubeconfig: p})
		if err != nil {
			t.Fatalf("BuildConfig: %v", err)
		}
		if rc.Host != "https://192.168.177.20:6443" {
			t.Errorf("host = %q, want the current-context server", rc.Host)
		}
		if rc.BearerToken != "tok-direct" {
			t.Errorf("token = %q, want tok-direct", rc.BearerToken)
		}
	})

	// --kube-context (R1.7's flag): the tunnel context is the one that is
	// insecure-skip-tls-verify, so the flag's effect is observable in the config.
	t.Run("explicit-context", func(t *testing.T) {
		rc, err := BuildConfig(Config{Kubeconfig: p, Context: "tunnel"})
		if err != nil {
			t.Fatalf("BuildConfig: %v", err)
		}
		if rc.Host != "https://tunnel.example:9443" {
			t.Errorf("host = %q, want https://tunnel.example:9443", rc.Host)
		}
		if !rc.TLSClientConfig.Insecure {
			t.Error("insecure = false, want true (the tunnel cluster sets insecure-skip-tls-verify)")
		}
	})

	t.Run("missing-context-is-an-error", func(t *testing.T) {
		if _, err := BuildConfig(Config{Kubeconfig: p, Context: "nope"}); err == nil {
			t.Error("BuildConfig with an unknown context succeeded; it must name the file and context in the error")
		}
	})
}

func TestBuildConfigExplicitAPIServer(t *testing.T) {
	// R1.11's tunnelled endpoint: an explicit URL bypasses the kubeconfig, and
	// without a token or client cert any real API server answers 401.
	rc, err := BuildConfig(Config{
		APIServerURL:       "https://tunnel.example:9443",
		BearerToken:        "tok",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if rc.Host != "https://tunnel.example:9443" || rc.BearerToken != "tok" || !rc.TLSClientConfig.Insecure {
		t.Errorf("config = %+v, want host/token/insecure from the flags", rc)
	}

	if _, err := BuildConfig(Config{APIServerURL: "https://x:6443", ClientCertFile: "/tmp/c.crt"}); err == nil {
		t.Error("client-cert without client-key succeeded; the two must be set together")
	}
}

func TestBuildConfigRateLimits(t *testing.T) {
	p := writeKubeconfig(t)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	rc, err := BuildConfig(Config{Kubeconfig: p})
	if err != nil {
		t.Fatal(err)
	}
	// The client-go default (5 QPS/10 burst) is too small for a fleet-wide
	// watch plus per-instance scrapes; the package raises it explicitly.
	if rc.QPS != defaultQPS || rc.Burst != defaultBurst {
		t.Errorf("qps/burst = %v/%v, want the package defaults %v/%v", rc.QPS, rc.Burst, defaultQPS, defaultBurst)
	}
	if rc.Timeout != defaultTimeout {
		t.Errorf("timeout = %v, want %v", rc.Timeout, defaultTimeout)
	}

	rc, err = BuildConfig(Config{Kubeconfig: p, QPS: 3, Burst: 9, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if rc.QPS != 3 || rc.Burst != 9 || rc.Timeout != time.Second {
		t.Errorf("qps/burst/timeout = %v/%v/%v, want 3/9/1s", rc.QPS, rc.Burst, rc.Timeout)
	}
}

func TestNew(t *testing.T) {
	p := writeKubeconfig(t)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	c, err := New(Config{Kubeconfig: p})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Clientset == nil || c.Dynamic == nil {
		t.Fatal("New returned nil clientset or dynamic client")
	}
	if c.UsesProxy() {
		t.Error("UsesProxy() = true without --use-proxy")
	}
	if c.RestConfig().Host != "https://192.168.177.20:6443" {
		t.Errorf("RestConfig().Host = %q", c.RestConfig().Host)
	}
}

// stubProxy is a restclient.ResponseWrapper standing in for the pods/proxy
// subresource so the fallback path can be tested without an API server.
type stubProxy struct {
	body []byte
	err  error
}

func (s stubProxy) DoRaw(context.Context) ([]byte, error) { return s.body, s.err }
func (s stubProxy) Stream(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.body)), s.err
}

// newTestClient builds a Client whose direct transport is srv (nil = an
// unreachable address) and whose proxy transport is a fake clientset that
// answers every pods/proxy get with "proxied:<pod>" and records the action.
func newTestClient(t *testing.T, cfg Config, srv *httptest.Server) (*Client, *capturedProxy) {
	t.Helper()
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cookbook", Name: "cookbook-pg-1"},
	})
	prx := &capturedProxy{}
	cs.AddProxyReactor("pods", func(action coretesting.Action) (bool, restclient.ResponseWrapper, error) {
		return prx.handle(action)
	})

	direct := &http.Client{Transport: http.DefaultTransport}
	if srv == nil {
		// An address nothing listens on: the direct transport fails, which is
		// R1.7's trigger for the proxy.
		direct.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})
	}
	return &Client{cfg: cfg, Clientset: cs, direct: direct}, prx
}

type capturedProxy struct {
	action coretesting.Action
	called int
}

func (c *capturedProxy) handle(action coretesting.Action) (bool, restclient.ResponseWrapper, error) {
	c.called++
	c.action = action
	name := "unknown"
	if g, ok := action.(coretesting.GetAction); ok {
		name = g.GetName()
	}
	return true, stubProxy{body: []byte("proxied:" + name)}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestGetTransportSelection exercises R1.7's rule: direct first, pods/proxy on
// failure or when forced, and a non-2xx response treated as a working transport.
func TestGetTransportSelection(t *testing.T) {
	t.Run("direct wins when it works", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("direct"))
		}))
		defer srv.Close()

		c, prx := newTestClient(t, Config{}, srv)
		status, body, err := c.Get(context.Background(), Target{
			Namespace: "cookbook", Pod: "cookbook-pg-1", Port: 9187,
			Scheme: "http", Path: "/metrics", DirectURL: srv.URL,
		})
		if err != nil || status != 200 || string(body) != "direct" {
			t.Fatalf("Get = (%d, %q, %v), want (200, \"direct\", nil)", status, body, err)
		}
		if prx.called != 0 {
			t.Errorf("pods/proxy was used %d times; R1.7 says direct-first, proxy only on failure", prx.called)
		}
	})

	t.Run("non-2xx is not a transport failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("no such path"))
		}))
		defer srv.Close()

		c, prx := newTestClient(t, Config{}, srv)
		status, body, err := c.Get(context.Background(), Target{
			Namespace: "cookbook", Pod: "cookbook-pg-1", Port: 9187,
			Scheme: "http", Path: "/nope", DirectURL: srv.URL,
		})
		if err != nil || status != 404 || string(body) != "no such path" {
			t.Fatalf("Get = (%d, %q, %v), want (404, \"no such path\", nil)", status, body, err)
		}
		if prx.called != 0 {
			t.Error("a 404 from the app triggered the proxy; the connection worked, so it must not")
		}
	})

	t.Run("direct failure falls back to the proxy", func(t *testing.T) {
		c, prx := newTestClient(t, Config{}, nil) // direct transport errors
		status, body, err := c.Get(context.Background(), Target{
			Namespace: "cookbook", Pod: "cookbook-pg-1", Port: 8000,
			Scheme: "https", Path: "/metrics", DirectURL: "https://10.42.1.9:8000/metrics",
		})
		if err != nil || status != 200 || string(body) != "proxied:cookbook-pg-1" {
			t.Fatalf("Get = (%d, %q, %v), want (200, \"proxied:cookbook-pg-1\", nil)", status, body, err)
		}
		pg, ok := prx.action.(coretesting.ProxyGetAction)
		if !ok {
			t.Fatalf("proxy action is %T, want a pods proxy-get action", prx.action)
		}
		// R1.7's URL shape, as the typed client actually built it:
		// scheme + pod + port + path must match the Target.
		if pg.GetNamespace() != "cookbook" || pg.GetName() != "cookbook-pg-1" ||
			pg.GetScheme() != "https" || pg.GetPort() != "8000" || pg.GetPath() != "/metrics" {
			t.Errorf("proxy request = %s %s/%s port %s path %s, want https cookbook/cookbook-pg-1 port 8000 path /metrics",
				pg.GetVerb(), pg.GetNamespace(), pg.GetName(), pg.GetPort(), pg.GetPath())
		}
	})

	t.Run("--use-proxy=true skips the direct attempt", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("direct"))
		}))
		defer srv.Close()

		c, prx := newTestClient(t, Config{UseProxy: true}, srv)
		_, body, err := c.Get(context.Background(), Target{
			Namespace: "cookbook", Pod: "cookbook-pg-1", Port: 9187,
			Scheme: "http", Path: "/metrics", DirectURL: srv.URL,
		})
		if err != nil || string(body) != "proxied:cookbook-pg-1" {
			t.Fatalf("Get = (%q, %v), want the proxy body", body, err)
		}
		if prx.called != 1 {
			t.Errorf("proxy called %d times, want 1", prx.called)
		}
	})

	t.Run("nothing to address", func(t *testing.T) {
		c, _ := newTestClient(t, Config{}, nil)
		if _, _, err := c.Get(context.Background(), Target{Namespace: "cookbook", Port: 9187}); err == nil {
			t.Error("Get with no pod and no direct URL returned no error")
		}
	})
}

// TestGetDirectNoFallback: GetDirect is the Service-address path (R1.7's first
// transport without a pod to proxy through), so it must surface the error
// instead of falling back.
func TestGetDirectNoFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer srv.Close()

	c, prx := newTestClient(t, Config{}, srv)
	status, body, err := c.GetDirect(context.Background(), srv.URL)
	if err != nil || status != 503 || string(body) != "unavailable" {
		t.Fatalf("GetDirect = (%d, %q, %v), want (503, \"unavailable\", nil)", status, body, err)
	}
	if prx.called != 0 {
		t.Error("GetDirect used pods/proxy")
	}
}

// TestPodLogs exercises the pods/log path R12.1 grants for R5.10, including the
// tail clamp: the fake records the PodLogOptions it was handed, so the test can
// prove clampTailLines's result is what reaches the API call rather than just
// that it exists.
func TestPodLogs(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cookbook", Name: "cookbook-pg-1"},
	})
	c := &Client{Clientset: cs}

	got, err := c.PodLogs(context.Background(), "cookbook", "cookbook-pg-1", 0, false)
	if err != nil {
		t.Fatalf("PodLogs: %v", err)
	}
	if got != "fake logs" {
		t.Errorf("PodLogs = %q, want the pod's log body", got)
	}

	acts := cs.Actions()
	if len(acts) == 0 {
		t.Fatal("PodLogs recorded no clientset action")
	}
	last := acts[len(acts)-1]
	if last.GetVerb() != "get" || last.GetResource().Resource != "pods" || last.GetSubresource() != "log" {
		t.Errorf("recorded %s on %s/%s, want get on pods/log",
			last.GetVerb(), last.GetResource().Resource, last.GetSubresource())
	}
	g, ok := last.(coretesting.GenericAction)
	if !ok {
		t.Fatalf("action is %T, want a generic action carrying the log options", last)
	}
	opts, ok := g.GetValue().(*corev1.PodLogOptions)
	if !ok || opts == nil {
		t.Fatalf("log options = %#v, want *corev1.PodLogOptions", g.GetValue())
	}
	if opts.TailLines == nil || *opts.TailLines != 100 {
		t.Errorf("tailLines = %v, want the clamped default 100", opts.TailLines)
	}
	if opts.Previous {
		t.Error("previous = true, want false")
	}
}
