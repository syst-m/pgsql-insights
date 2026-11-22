package kube

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Config is the kube-client flag set (R1.7, R1.11). The full flag/env/ConfigMap
// precedence chain is R11.8 and lands in Phase 2; these are the fields Phase 1a
// needs to reach a cluster from inside or outside it.
type Config struct {
	// Kubeconfig is --kubeconfig. Empty means: in-cluster config when running
	// in a pod, otherwise the usual ~/.kube/config / $KUBECONFIG resolution.
	Kubeconfig string
	// Context is --kube-context, for a multi-cluster kubeconfig (the reference
	// environment has a k3s context plus tunnelled ones).
	Context string
	// APIServerURL is --api-server-url: an explicit endpoint, bypassing the
	// kubeconfig. R1.11's tunnelled-endpoint case is configured this way.
	APIServerURL string
	// BearerToken, ClientCertFile, ClientKeyFile and InsecureSkipVerify
	// authenticate an APIServerURL that the kubeconfig was not supplied for.
	// Without them a bare URL yields 401 from any real API server.
	BearerToken        string
	ClientCertFile     string
	ClientKeyFile      string
	InsecureSkipVerify bool

	// UseProxy is R1.7's --use-proxy=true: force the pods/proxy transport even
	// when the pod IP is directly reachable.
	UseProxy bool

	// QPS and Burst rate-limit the client so a 50-cluster fleet discovering
	// every 30s cannot hammer the control plane. Zero picks the defaults below.
	QPS   float32
	Burst int
	// Timeout bounds a single API request. Zero picks defaultTimeout.
	Timeout time.Duration
}

// Client is the process's single Kubernetes client (R1.7): Clientset, the
// typed one for pods/services/PVCs (and the pods/proxy transport), and
// Dynamic, the dynamic one for the CNPG CRDs, which have no generated types in
// this repo's dependency set. R1.7's per-port transport table is completed by
// the `direct` HTTP client for the pod-IP transport.
//
// Both fields are interfaces so tests can substitute fakes (k8s.io/client-go
// ships kubernetes/fake and dynamic/fake) without a live cluster.
type Client struct {
	cfg     Config
	restCfg *rest.Config

	Clientset kubernetes.Interface
	Dynamic   dynamic.Interface

	// direct is the HTTP client for R1.7's first transport (pod-IP URLs).
	direct *http.Client
}

// defaultTimeout bounds one API request. Discovery lists whole namespaces every
// 30s (R1.1); a request that hangs longer than this is a broken tunnel, not a
// slow API server, and R1.7 wants the failure fast enough to fall back.
const defaultTimeout = 15 * time.Second

// defaultQPS/defaultBurst: the client-go defaults (5/10) are tuned for a
// controller touching a handful of objects. pgsi watches every namespace in
// the fleet and Phase 2 scrapes every instance, so the client needs a wider
// bucket than the library default while still being bounded.
const (
	defaultQPS   = 25
	defaultBurst = 50
)

// BuildConfig resolves the rest.Config from Config. The order is the one R1.7
// implies: an explicit URL wins, then in-cluster, then the kubeconfig.
func BuildConfig(cfg Config) (*rest.Config, error) {
	var rc *rest.Config

	switch {
	case cfg.APIServerURL != "":
		// Explicit endpoint: build the config from the flags alone. This is
		// R1.11's tunnelled-endpoint path, where a kubeconfig for the real
		// cluster is the wrong thing to load.
		rc = &rest.Config{
			Host:            cfg.APIServerURL,
			BearerToken:     cfg.BearerToken,
			TLSClientConfig: rest.TLSClientConfig{Insecure: cfg.InsecureSkipVerify},
		}
		if cfg.ClientCertFile != "" || cfg.ClientKeyFile != "" {
			if cfg.ClientCertFile == "" || cfg.ClientKeyFile == "" {
				return nil, errors.New("--client-cert and --client-key must be set together")
			}
			rc.TLSClientConfig.CertFile = cfg.ClientCertFile
			rc.TLSClientConfig.KeyFile = cfg.ClientKeyFile
		}
	case inCluster():
		var err error
		rc, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
	default:
		// Out of cluster: --kubeconfig, else $KUBECONFIG, else ~/.kube/config
		// (all three are clientcmd's own resolution order).
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		if cfg.Kubeconfig != "" {
			rules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: cfg.Kubeconfig}
		}
		overrides := &clientcmd.ConfigOverrides{}
		if cfg.Context != "" {
			overrides.CurrentContext = cfg.Context
		}
		var err error
		rc, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("kubeconfig %q (context %q): %w", cfg.Kubeconfig, cfg.Context, err)
		}
	}

	if rc.QPS == 0 {
		rc.QPS = cfg.QPS
		if rc.QPS == 0 {
			rc.QPS = defaultQPS
		}
	}
	if rc.Burst == 0 {
		rc.Burst = cfg.Burst
		if rc.Burst == 0 {
			rc.Burst = defaultBurst
		}
	}
	if cfg.Timeout > 0 {
		rc.Timeout = cfg.Timeout
	} else if rc.Timeout == 0 {
		rc.Timeout = defaultTimeout
	}
	return rc, nil
}

// inCluster reports whether we are running inside a Kubernetes pod.
// rest.InClusterConfig keys off the same two signals but returns an error
// rather than a bool, and the caller needs to distinguish "not in a pod" from
// "in a pod with a broken mount".
func inCluster() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return false
	}
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return err == nil
}

// New builds the clientset and dynamic client from Config.
func New(cfg Config) (*Client, error) {
	rc, err := BuildConfig(cfg)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	// Direct pod-HTTPS (R1.10's 8000) is signed by the cluster server CA, a
	// per-cluster secret whose plumbing is Phase 2's (R1.8). Until that lands
	// the verified transport for 8000 is pods/proxy — the API server presents
	// its own certificate there — and direct pod-HTTPS skips verification.
	// R1.7's direct-first ordering is about latency, not trust.
	tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // see comment
	if rc.CAFile != "" || len(rc.CAData) > 0 {
		// An explicit cluster CA (from the kubeconfig we did load) is trusted
		// normally; skipping verification is only for the no-CA case.
		tlsCfg.InsecureSkipVerify = rc.TLSClientConfig.Insecure
	}
	c := &Client{
		cfg:       cfg,
		restCfg:   rc,
		Clientset: cs,
		Dynamic:   dyn,
		direct: &http.Client{
			Timeout: rc.Timeout,
			Transport: &http.Transport{
				TLSClientConfig: tlsCfg,
				MaxIdleConns:    64,
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}
	return c, nil
}

// RestConfig exposes the resolved config (Phase 2's informers and the leader
// election need it; it is not part of the collector surface).
func (c *Client) RestConfig() *rest.Config { return c.restCfg }

// UsesProxy reports whether the client is pinned to the pods/proxy transport
// (--use-proxy=true, R1.7).
func (c *Client) UsesProxy() bool { return c.cfg.UseProxy }

// Target is one HTTP endpoint of a pod, addressed both ways so R1.7's selection
// has both candidates in one value. Build it from the R1.10 table, never by
// hand: EndpointsFor in the discovery layer does that for every pod it reports.
type Target struct {
	Namespace string
	Pod       string
	// Port and Scheme come from the R1.10 table row being addressed.
	Port   int
	Scheme string
	// Path is the request path on that endpoint, e.g. "/metrics".
	Path string
	// DirectURL is the pod-IP URL from URLFor, empty when the pod has no
	// address or the port is pod-local (R1.10's 8010).
	DirectURL string
}

// Get performs an HTTP GET against a pod endpoint over the transport R1.7
// selects, returning the status code and body.
//
// Selection (R1.7): direct first when a direct URL exists and --use-proxy is
// false; the pods/proxy subresource otherwise, and also on any direct transport
// failure. A response with a non-2xx status is *not* a transport failure — a
// 404 from the instance manager means the connection worked — so it is returned
// rather than retried through the proxy.
//
// R1.11's caveat, in short: the proxy path only works when the API server can
// route to the pod CIDR. That holds in-cluster and does not reliably hold when
// the API endpoint is tunnelled (measured: 502 through a tunnel while exec and
// port-forward to the same pod both work). Direct-first is the mitigation; when
// both fail, the error text names both paths that were tried.
func (c *Client) Get(ctx context.Context, t Target) (int, []byte, error) {
	if !c.cfg.UseProxy && t.DirectURL != "" {
		status, body, err := c.getURL(ctx, t.DirectURL)
		if err == nil {
			return status, body, nil
		}
		// Direct addressing failed; fall through to the proxy (R1.7). A
		// non-2xx is not an error here — getURL only errors on transport
		// failures — so reaching here means the pod IP was unreachable.
		if t.Pod == "" {
			return 0, nil, fmt.Errorf("direct GET %s: %w (no pod name, cannot fall back to pods/proxy)", t.DirectURL, err)
		}
	}
	if t.Pod == "" {
		return 0, nil, fmt.Errorf("no address for %s/%s port %d: neither a pod IP nor a pod name", t.Namespace, t.Pod, t.Port)
	}
	// pods/proxy through the typed client, which reuses the rest.Config's
	// TLS/auth material — so this path is authenticated and verified even where
	// direct pod-HTTPS is not.
	body, err := c.Clientset.CoreV1().Pods(t.Namespace).
		ProxyGet(t.Scheme, t.Pod, strconv.Itoa(t.Port), t.Path, nil).
		DoRaw(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("GET %s via pods/proxy (direct: %q): %w",
			PodsProxyURL(t.Scheme, t.Namespace, t.Pod, t.Port, t.Path), t.DirectURL, err)
	}
	// The proxy subresource answers 200 with the upstream body and surfaces an
	// upstream non-2xx as an error, so a body here is a success.
	return http.StatusOK, body, nil
}

// getURL issues a plain HTTP GET against a direct URL.
func (c *Client) getURL(ctx context.Context, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("build request %s: %w", url, err)
	}
	resp, err := c.direct.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read %s: %w", url, err)
	}
	return resp.StatusCode, body, nil
}

// GetDirect fetches a URL through the direct transport only, with no proxy
// fallback. Phase 2's HTTP collectors use this for Service addresses, where
// pods/proxy is not applicable (R1.7's first transport covers Services too).
func (c *Client) GetDirect(ctx context.Context, url string) (int, []byte, error) {
	return c.getURL(ctx, url)
}

// clampTailLines bounds a log tail request: 0 means "the default 100 lines" and
// the ceiling stops a caller from asking for the whole log of a pod that has
// restarted 1551 times (freeidp-pg-1's restartCount) and streaming megabytes
// into a collector that renders 20 lines.
func clampTailLines(n int64) int64 {
	switch {
	case n <= 0:
		return 100
	case n > 10000:
		return 10000
	}
	return n
}

// PodLogs reads a pod's logs (R12.1 grants pods/log:[get,list] for R5.10; the
// flag that turns log collection on is Phase 5's --log-source).
func (c *Client) PodLogs(ctx context.Context, namespace, pod string, tailLines int64, previous bool) (string, error) {
	tailLines = clampTailLines(tailLines)
	opts := &corev1.PodLogOptions{TailLines: &tailLines, Previous: previous}
	stream, err := c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("logs %s/%s: %w", namespace, pod, err)
	}
	defer func() { _ = stream.Close() }()
	b, err := io.ReadAll(io.LimitReader(stream, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read logs %s/%s: %w", namespace, pod, err)
	}
	return string(b), nil
}
