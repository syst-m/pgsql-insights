// Command pgsi is the single pgsql-insights binary (R11.1): API, collector and
// embedded UI in one process.
//
// Subcommand surface (R3.1): serve | collect | api | version.
// Per spec §3.1 and IMPLEMENTATION.md D9 (A3-M1), v1 ships exactly one working
// role — serve. `collect` and `api` are reserved stubs that exit without doing
// work, so the v1.x --role=collect|serve split can land without breaking the
// CLI contract that Phase 0 freezes.
//
// Phase 1a wired the two subsystems `serve` actually runs: the discovery loop
// (internal/discovery, R1.1-R1.9) and the logger (internal/logx, R13.7). The
// flags below are those two packages' Config structs spelled as CLI flags —
// there is no third source of truth, and the flag set is the contract R11.8's
// precedence chain (CLI > PGSI_* env > ConfigMap > defaults) will read in
// Phase 2.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/discovery"
	"github.com/frankellis/pgsql-insights/internal/kube"
	"github.com/frankellis/pgsql-insights/internal/logx"
	"github.com/frankellis/pgsql-insights/internal/version"
)

const usage = `pgsi — pgsql-insights (see requirements.md)

usage: pgsi <command> [flags]

commands:
  serve     run the API + collector + UI in one process (the v1 role, §3.1)
  collect   reserved: the collector role of the v1.x role split (D9) — no-op in v1
  api       reserved: the API-only role of the v1.x role split (D9) — no-op in v1
  version   print the build version and commit (R11.3)

serve flags (run "pgsi serve -h" for the full set):
  --listen                      address the HTTP server binds (R8.7)         :8080
  --shutdown-timeout            graceful-shutdown drain budget (R13.8)       10s
  --auth                        none|token|kube-rbac-proxy (R8.8)            token
  --auth-metrics                auth for /metrics only (R8.9)                none
  --auth-token / --auth-token-file
                                bearer token, or the Secret file holding it  /etc/pgsi/auth/token
  --auth-metrics-token / --auth-metrics-token-file
                                R8.8's separate /metrics credential; falls    (unset)
                                back to --auth-token[-file] when unset
  --cors-allow-origin           CORS allow-list (R8.9); empty = same-origin  (none)
  --cors-allow-credentials      Access-Control-Allow-Credentials (R8.9)      false
  --cors-max-age                preflight cache lifetime (R8.9)              5m

  --discover                    run discovery in this process (D9's role      true
                                knob; --discover=false is the ` + "`api`" + ` role,
                                which serves an empty fleet and is still Ready)
  --namespaces                  comma-separated allow list (R1.1)             (all visible)
  --exclude-namespaces          comma-separated deny list (R1.1)              (none)
  --resync                      informer resync period (R1.1)                 30s
  --stale-grace                 post-deletion series grace (R1.9)             15m
  --credential-secret           R1.8's app-secret override (ns/name or name)  (unset)
  --credential-username/-password/-dbname
                                R1.8's single configured credential           (unset)

  --kubeconfig                  kubeconfig path (R1.7)                        $KUBECONFIG/~/.kube/config
  --kube-context                context within it (R1.7)                      (current)
  --api-server-url              explicit endpoint, bypassing the kubeconfig   (unset)
                                (R1.11's tunnelled cluster)
  --bearer-token / --client-cert / --client-key / --insecure-skip-verify
                                auth for --api-server-url (R1.11)             (unset)
  --use-proxy                   force pods/proxy for instance access (R1.7)   false
  --qps / --burst               client rate limit (25/50; the 5/10 client-go
                                default is tuned for one controller)
  --request-timeout             bound on one API request                      15s

  --log-level                   debug|info|warn|error (R13.7)                 info
  --log-field                   k=v structured field, repeatable; also accepts
                                R13.7's --log-field-<key>=<value> shape       (none)

`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "collect", "api":
		// Reserved for the v1.x role split (D9). Exiting 0 keeps scripts and
		// CI honest about the contract without pretending the split exists.
		// The knob that actually selects the split today is `serve --discover`
		// (see runServe): `pgsi api` stays a stub so the subcommand names
		// Phase 0 froze keep their documented behaviour.
		fmt.Printf("pgsi %s: v1 ships a single role (serve); %q is reserved for v1.x (IMPLEMENTATION.md D9)\n",
			os.Args[1], os.Args[1])
	case "version", "-v", "--version":
		fmt.Println(version.String())
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "pgsi: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func runServe(args []string) int {
	// R13.7's `--log-field-*` shape cannot be declared as a Go flag (the flag
	// package has no dynamic flag names), so the prefixed form is lifted out of
	// argv before Parse sees it. Everything else parses normally, including the
	// plain repeatable `--log-field k=v`.
	prefixed, rest, err := logx.FieldsFromArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 2
	}

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":8080", "address the HTTP server binds (R8.7)")
	shutdown := fs.Duration("shutdown-timeout", 0, "graceful-shutdown drain budget (R13.8); 0 selects 10s")

	// --- R8.8 auth -------------------------------------------------------
	// The defaults are the requirement's own words: "Default `token` with a
	// bearer token from a Secret; `none` for lab", plus "--auth-metrics=none so
	// Prometheus need not hold a token". A bare `pgsi serve` therefore requires
	// a token and fails to start without one — see resolveAuth below.
	//
	// Which path each flag governs is R8.9's table (f08b6ac), not a detail:
	// --auth covers / and /api/v1/* and /debug/pprof/*; --auth-metrics covers
	// /metrics alone; /healthz and /readyz are never auth-gated under any value
	// of either flag, because kubelet's httpGet probe cannot present a bearer
	// token and a 401 on a probe is a pod that never becomes Ready.
	authMode := fs.String("auth", string(api.DefaultAuthMode), "auth mode for /, /api/v1/* and /debug/pprof/*: none|token|kube-rbac-proxy (R8.8)")
	authMetrics := fs.String("auth-metrics", string(api.AuthNone), "auth mode for /metrics only (R8.8/R8.9); /healthz and /readyz are never auth-gated. none lets Prometheus scrape without a token")
	authToken := fs.String("auth-token", "", "bearer token for --auth=token; takes precedence over --auth-token-file")
	// The default is the path the chart mounts the Secret at, exported from
	// internal/api as TokenFile so the chart and the binary cannot disagree
	// about it. Off-pod, `--auth=token` with no --auth-token produces a clear
	// "no such file" error rather than a silently unauthenticated server.
	authTokenFile := fs.String("auth-token-file", api.TokenFile, "file holding the bearer token (R8.8's Secret, mounted by the chart); used when --auth-token is empty")

	// --- R8.8's second secret: /metrics' own token ------------------------
	// R8.8 says the /metrics credential is separate from the API credential,
	// and A3's review of f9aae41 ruled that "separate" means a separate
	// *secret*, not merely a separate mode: --auth-metrics alone let a
	// deployment run --auth-metrics=token while /metrics accepted the API's
	// bearer, so the Prometheus scrape credential and the human UI credential
	// were the same string and rotating one rotated the other.
	//
	// Both default to empty rather than to api.TokenFile, and that is what
	// keeps every existing invocation working: an empty pair falls back to
	// --auth-token/--auth-token-file inside AuthzConfig.Build, so a chart that
	// sets only the API flags gets the old behaviour and a chart that sets
	// --auth-metrics-token-file gets a real second Secret. A default of
	// api.TokenFile here would instead make the fallback unreachable, because
	// "unset" and "the chart's API path" would be the same string.
	authMetricsToken := fs.String("auth-metrics-token", "", "bearer token for --auth-metrics=token (R8.8's separate /metrics credential); falls back to --auth-token when empty; takes precedence over --auth-metrics-token-file")
	authMetricsTokenFile := fs.String("auth-metrics-token-file", "", "file holding the /metrics bearer token; falls back to --auth-token-file when empty and --auth-metrics-token is unset")

	// --- R8.9 CORS -------------------------------------------------------
	// Empty --cors-allow-origin means no CORS headers at all. R8.6 serves the
	// SPA from this same binary, so the normal deployment is same-origin and
	// defaulting to "*" would open the API to every origin for nobody's
	// benefit (and "*" is invalid alongside credentials). See internal/api/cors.go.
	corsOrigin := fs.String("cors-allow-origin", "", "comma-separated CORS allow-list (R8.9); empty means same-origin only, no CORS headers")
	corsCreds := fs.Bool("cors-allow-credentials", false, "send Access-Control-Allow-Credentials on CORS responses (R8.9)")
	corsMaxAge := fs.Duration("cors-max-age", 0, "how long a browser may cache a preflight (R8.9); 0 selects 5m")

	// --- R13.7 logging ---------------------------------------------------
	logLevel := fs.String("log-level", "info", "log level: debug|info|warn|error (R13.7); debug is where a collector dumps the SQL it ran")
	// The flag value is seeded with the --log-field-* fields the pre-scan
	// found, so both spellings accumulate into one set and flag's usage output
	// still shows --log-field.
	logFields := prefixed
	fs.Var(&logFields, "log-field", "extra structured log field as k=v (R13.7), repeatable; --log-field-<key>=<value> is also accepted")

	// --- R1.1/R1.9 discovery ---------------------------------------------
	// D9's role knob, one flag ahead of the v1.x subcommand split: --discover=
	// false runs the API without the collector, which is exactly the reserved
	// `api` role. R8.7(ii) is what makes that useful — a process that runs no
	// discovery is ready once it is listening, not permanently unready under a
	// discovery-only rule.
	discover := fs.Bool("discover", true, "run cluster discovery in this process (D9's role knob; --discover=false serves the API with an empty fleet)")
	namespaces := fs.String("namespaces", "", "comma-separated namespace allow list (R1.1); empty means every namespace the RBAC can see")
	exclude := fs.String("exclude-namespaces", "", "comma-separated namespace deny list (R1.1); applies to the allow list, or to the visible set when it is empty")
	resync := fs.Duration("resync", 0, "informer resync period (R1.1's 30s); 0 selects 30s")
	staleGrace := fs.Duration("stale-grace", 0, "how long a deleted cluster's series stay labelled stale (R1.9); 0 selects 15m")

	// --- R1.8 credentials -------------------------------------------------
	// The configured side of R1.8's precedence; the cluster side (<cluster>-app)
	// is read by discovery itself. These three flags are the pre-multi-cluster
	// single-credential shape, kept working.
	credSecret := fs.String("credential-secret", "", "app credential Secret as namespace/name or a bare name (R1.8's first level); outranks every other source")
	credUser := fs.String("credential-username", "", "configured app username (R1.8's third level) when no secret is configured")
	credPass := fs.String("credential-password", "", "configured app password (R1.8)")
	credDB := fs.String("credential-dbname", "", "configured app database name (R1.8)")

	// --- R1.7/R1.11 kube client ------------------------------------------
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (R1.7); empty uses $KUBECONFIG/~/.kube/config, or in-cluster config inside a pod")
	kubeContext := fs.String("kube-context", "", "kubeconfig context to use (R1.7); empty uses the current-context")
	apiServer := fs.String("api-server-url", "", "explicit API server endpoint (R1.11's tunnelled cluster); bypasses the kubeconfig")
	bearer := fs.String("bearer-token", "", "bearer token for --api-server-url (R1.11); required unless the endpoint accepts anonymous")
	clientCert := fs.String("client-cert", "", "client certificate for --api-server-url (R1.11)")
	clientKey := fs.String("client-key", "", "client key for --api-server-url (R1.11)")
	insecure := fs.Bool("insecure-skip-verify", false, "skip TLS verification of --api-server-url (R1.11's self-signed tunnels)")
	useProxy := fs.Bool("use-proxy", false, "force the pods/proxy transport for instance access even when the pod IP is reachable (R1.7's --use-proxy=true)")
	qps := fs.Float64("qps", 0, "API request rate limit (R1.7); 0 selects 25 — client-go's 5 default is tuned for one controller, not a fleet-wide watch")
	burst := fs.Int("burst", 0, "request burst for --qps (R1.7); 0 selects 50")
	reqTimeout := fs.Duration("request-timeout", 0, "bound on a single API request (R1.7); 0 selects 15s")

	if err := fs.Parse(rest); err != nil {
		return 2
	}

	// The logger is built before anything else that can talk, so a startup
	// failure is a log line and not a printf of unknown shape (R13.7).
	log, err := logx.NewFromFlags(*logLevel, logFields)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 2
	}

	// Auth is built before the listener is opened, and a bad combination is a
	// startup failure (exit 2, the same code a flag parse error gets) rather
	// than a serving pod that 401s everything: a deployment that rolled out
	// with an empty Secret looks healthy in the Deployment and broken through
	// the Service.
	authz, err := api.AuthzConfig{
		Mode:             api.AuthMode(*authMode),
		MetricsMode:      api.AuthMode(*authMetrics),
		Token:            *authToken,
		TokenFile:        *authTokenFile,
		MetricsToken:     *authMetricsToken,
		MetricsTokenFile: *authMetricsTokenFile,
	}.Build()
	if err != nil {
		log.Error("auth configuration", "error", err.Error())
		return 2
	}

	cfg := api.Config{Listen: *listen, ShutdownTimeout: *shutdown, Auth: authz, Log: log}

	// --- CORS (R8.9) -----------------------------------------------------
	// An empty allow-list leaves cfg.CORS nil, which cors.go treats as "no CORS
	// headers at all" — the correct answer for the same-origin deployment R8.6
	// describes, and the reason "*" is not the default here.
	if origins := api.ParseCORSAllowOrigin(*corsOrigin); len(origins) > 0 {
		cfg.CORS = &api.CORSConfig{
			AllowedOrigins:   origins,
			AllowCredentials: *corsCreds,
			MaxAge:           *corsMaxAge,
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- discovery (R1.1) -------------------------------------------------
	// The Discoverer is the API's Fleet, so New derives the single readiness
	// signal from it (R8.7(i)): the socket opens, /readyz answers 503 and
	// /api/v1/clusters answers ErrNotReady until the first sync completes,
	// which is the failure D6-era `Run()` had (ready at net.Listen, so a pod
	// served an empty fleet and still passed its probe).
	//
	// It runs in a goroutine because Run blocks until ctx is done. Its failure
	// cancels the shared context, so a dead watch takes the process with it
	// rather than leaving a pod serving a fleet it can no longer update.
	var disc *discovery.Discoverer
	discDone := make(chan error, 1)
	if *discover {
		kc, err := kube.New(kube.Config{
			Kubeconfig:         *kubeconfig,
			Context:            *kubeContext,
			APIServerURL:       *apiServer,
			BearerToken:        *bearer,
			ClientCertFile:     *clientCert,
			ClientKeyFile:      *clientKey,
			InsecureSkipVerify: *insecure,
			UseProxy:           *useProxy,
			QPS:                float32(*qps),
			Burst:              *burst,
			Timeout:            *reqTimeout,
		})
		if err != nil {
			log.Error("kube client", "error", err.Error())
			return 2
		}
		disc = discovery.New(discovery.Config{
			Namespaces:        splitList(*namespaces),
			ExcludeNamespaces: splitList(*exclude),
			Resync:            *resync,
			StaleGrace:        *staleGrace,
			Credentials: discovery.CredentialsConfig{
				Secret:   *credSecret,
				Username: *credUser,
				Password: *credPass,
				DBName:   *credDB,
			},
		}, kc, log)
		cfg.Fleet = disc
		go func() { discDone <- disc.Run(ctx) }()
	} else {
		// D9's `api` role: no fleet, so New gives the server a source-less
		// Readiness that turns true once the socket opens (R8.7(ii)).
		log.Info("discovery disabled", "role", "api", "reason", "--discover=false (D9's reserved API role)")
		discDone <- nil
	}

	srv := api.New(cfg)

	serveErr := srv.Run(ctx)
	// R13.8: Run has drained the in-flight requests; collect discovery's exit
	// status before deciding the process's own. Run returns ctx.Err() on
	// cancellation, which is the normal path, not a failure.
	discErr := <-discDone
	if discErr != nil && !errors.Is(discErr, context.Canceled) {
		log.Error("discovery stopped", "error", discErr.Error())
		if serveErr == nil {
			return 1
		}
	}
	if serveErr != nil {
		return 1
	}
	return 0
}

// splitList turns a comma-separated flag value into a slice, dropping empties
// and trimming spaces so "--namespaces=a, b" and "--namespaces=a,b" agree.
// R1.1's flag is a list, and the chart passes it as one quoted argument, so
// this is the only place the separator is interpreted.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
