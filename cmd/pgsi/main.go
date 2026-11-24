// Command pgsi is the single pgsql-insights binary (R11.1): API, collector and
// embedded UI in one process.
//
// Subcommand surface (R3.1): serve | collect | api | version, plus R11.8's
// `config dump`, which prints the resolved config and exits without starting
// anything.
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
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/collector"
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
  config    dump the resolved config for a set of serve flags (R11.8)

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
  --prometheus-url              Prometheus the R8.3 pair proxies PromQL to;  (unset)
                                empty serves the fixed catalogue only (R8.3)

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
	case "config":
		// R11.8 — resolve the chain and print it, without starting anything.
		// The flags it accepts are serve's, so `pgsi config dump --listen=:9`
		// answers "what would serve use", which is the question the requirement
		// says the dump is for.
		os.Exit(runConfig(os.Args[2:]))
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

// serveFlags is the `pgsi serve` flag set: the FlagSet itself plus a typed
// pointer to every flag's value.
//
// It exists so the flag set has exactly one enumeration. Before R11.8 the
// declarations lived inline in runServe, and the only way to hand the same set
// to the resolver was to rebuild it — and a rebuilt set is a second list of
// flag names, which is the third source of truth main.go's package doc promises
// does not exist. Embedding the FlagSet and naming each pointer as a field keeps
// every downstream `*listen` / `*resync` read working while making the
// constructor the single place a flag is declared, and it is what
// TestTableCoversEveryFlag compares the R11.8 table against.
//
// Field types mirror the flag constructors.
type serveFlags struct {
	*flag.FlagSet

	listen               *string
	shutdown             *time.Duration
	authMode             *string
	authMetrics          *string
	authToken            *string
	authTokenFile        *string
	authMetricsToken     *string
	authMetricsTokenFile *string
	corsOrigin           *string
	corsCreds            *bool
	corsMaxAge           *time.Duration
	prometheusURL        *string
	logLevel             *string
	logField             logx.Fields
	discover             *bool
	namespaces           *string
	exclude              *string
	resync               *time.Duration
	staleGrace           *time.Duration
	credSecret           *string
	credUser             *string
	credPass             *string
	credDB               *string
	kubeconfig           *string
	kubeContext          *string
	apiServer            *string
	bearer               *string
	clientCert           *string
	clientKey            *string
	insecure             *bool
	useProxy             *bool
	qps                  *float64
	burst                *int
	reqTimeout           *time.Duration
}

// newServeFlags declares every `pgsi serve` flag and returns them as one value.
//
// `prefixed` is the --log-field-* set the R13.7 pre-scan lifted out of argv; it
// seeds the --log-field flag's value so both spellings accumulate into one set,
// exactly as runServe did before the extraction.
func newServeFlags(prefixed logx.Fields) *serveFlags {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sf := &serveFlags{FlagSet: fs}

	sf.listen = fs.String("listen", ":8080", "address the HTTP server binds (R8.7)")
	sf.shutdown = fs.Duration("shutdown-timeout", 0, "graceful-shutdown drain budget (R13.8); 0 selects 10s")

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
	sf.authMode = fs.String("auth", string(api.DefaultAuthMode), "auth mode for /, /api/v1/* and /debug/pprof/*: none|token|kube-rbac-proxy (R8.8)")
	sf.authMetrics = fs.String("auth-metrics", string(api.AuthNone), "auth mode for /metrics only (R8.8/R8.9); /healthz and /readyz are never auth-gated. none lets Prometheus scrape without a token")
	sf.authToken = fs.String("auth-token", "", "bearer token for --auth=token; takes precedence over --auth-token-file")
	// The default is the path the chart mounts the Secret at, exported from
	// internal/api as TokenFile so the chart and the binary cannot disagree
	// about it. Off-pod, `--auth=token` with no --auth-token produces a clear
	// "no such file" error rather than a silently unauthenticated server.
	sf.authTokenFile = fs.String("auth-token-file", api.TokenFile, "file holding the bearer token (R8.8's Secret, mounted by the chart); used when --auth-token is empty")

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
	sf.authMetricsToken = fs.String("auth-metrics-token", "", "bearer token for --auth-metrics=token (R8.8's separate /metrics credential); falls back to --auth-token when empty; takes precedence over --auth-metrics-token-file")
	sf.authMetricsTokenFile = fs.String("auth-metrics-token-file", "", "file holding the /metrics bearer token; falls back to --auth-token-file when empty and --auth-metrics-token is unset")

	// --- R8.9 CORS -------------------------------------------------------
	// Empty --cors-allow-origin means no CORS headers at all. R8.6 serves the
	// SPA from this same binary, so the normal deployment is same-origin and
	// defaulting to "*" would open the API to every origin for nobody's
	// benefit (and "*" is invalid alongside credentials). See internal/api/cors.go.
	sf.corsOrigin = fs.String("cors-allow-origin", "", "comma-separated CORS allow-list (R8.9); empty means same-origin only, no CORS headers")
	sf.corsCreds = fs.Bool("cors-allow-credentials", false, "send Access-Control-Allow-Credentials on CORS responses (R8.9)")
	sf.corsMaxAge = fs.Duration("cors-max-age", 0, "how long a browser may cache a preflight (R8.9); 0 selects 5m")

	// --- R8.3 PromQL pair --------------------------------------------------
	// One flag picks the mode the pair answers in. Set, and
	// /api/v1/metrics/{query,system} proxy the query to that Prometheus, so any
	// PromQL the cluster's Prometheus can evaluate is answerable. Empty — the
	// default — and they answer only the fixed catalogue of named queries (R9.4's
	// aggregates, R9.5's per-query rollups, R9.7's system family) and return
	// PromQLUnavailable for anything else, because §3.4 keeps raw samples out of
	// SQLite and the binary ships no PromQL evaluator. The pair is mounted either
	// way; the flag changes what it can answer, not whether it exists.
	sf.prometheusURL = fs.String("prometheus-url", "", "base URL of the Prometheus the R8.3 pair proxies PromQL to (R8.3); empty serves the fixed named-query catalogue only")

	// --- R13.7 logging ---------------------------------------------------
	sf.logLevel = fs.String("log-level", "info", "log level: debug|info|warn|error (R13.7); debug is where a collector dumps the SQL it ran")
	// The flag value is seeded with the --log-field-* fields the pre-scan
	// found, so both spellings accumulate into one set and flag's usage output
	// still shows --log-field.
	sf.logField = prefixed
	fs.Var(&sf.logField, "log-field", "extra structured log field as k=v (R13.7), repeatable; --log-field-<key>=<value> is also accepted")

	// --- R1.1/R1.9 discovery ---------------------------------------------
	// D9's role knob, one flag ahead of the v1.x subcommand split: --discover=
	// false runs the API without the collector, which is exactly the reserved
	// `api` role. R8.7(ii) is what makes that useful — a process that runs no
	// discovery is ready once it is listening, not permanently unready under a
	// discovery-only rule.
	sf.discover = fs.Bool("discover", true, "run cluster discovery in this process (D9's role knob; --discover=false serves the API with an empty fleet)")
	sf.namespaces = fs.String("namespaces", "", "comma-separated namespace allow list (R1.1); empty means every namespace the RBAC can see")
	sf.exclude = fs.String("exclude-namespaces", "", "comma-separated namespace deny list (R1.1); applies to the allow list, or to the visible set when it is empty")
	sf.resync = fs.Duration("resync", 0, "informer resync period (R1.1's 30s); 0 selects 30s")
	sf.staleGrace = fs.Duration("stale-grace", 0, "how long a deleted cluster's series stay labelled stale (R1.9); 0 selects 15m")

	// --- R1.8 credentials -------------------------------------------------
	// The configured side of R1.8's precedence; the cluster side (<cluster>-app)
	// is read by discovery itself. These three flags are the pre-multi-cluster
	// single-credential shape, kept working.
	sf.credSecret = fs.String("credential-secret", "", "app credential Secret as namespace/name or a bare name (R1.8's first level); outranks every other source")
	sf.credUser = fs.String("credential-username", "", "configured app username (R1.8's third level) when no secret is configured")
	sf.credPass = fs.String("credential-password", "", "configured app password (R1.8)")
	sf.credDB = fs.String("credential-dbname", "", "configured app database name (R1.8)")

	// --- R1.7/R1.11 kube client ------------------------------------------
	sf.kubeconfig = fs.String("kubeconfig", "", "kubeconfig path (R1.7); empty uses $KUBECONFIG/~/.kube/config, or in-cluster config inside a pod")
	sf.kubeContext = fs.String("kube-context", "", "kubeconfig context to use (R1.7); empty uses the current-context")
	sf.apiServer = fs.String("api-server-url", "", "explicit API server endpoint (R1.11's tunnelled cluster); bypasses the kubeconfig")
	sf.bearer = fs.String("bearer-token", "", "bearer token for --api-server-url (R1.11); required unless the endpoint accepts anonymous")
	sf.clientCert = fs.String("client-cert", "", "client certificate for --api-server-url (R1.11)")
	sf.clientKey = fs.String("client-key", "", "client key for --api-server-url (R1.11)")
	sf.insecure = fs.Bool("insecure-skip-verify", false, "skip TLS verification of --api-server-url (R1.11's self-signed tunnels)")
	sf.useProxy = fs.Bool("use-proxy", false, "force the pods/proxy transport for instance access even when the pod IP is reachable (R1.7's --use-proxy=true)")
	sf.qps = fs.Float64("qps", 0, "API request rate limit (R1.7); 0 selects 25 — client-go's 5 default is tuned for one controller, not a fleet-wide watch")
	sf.burst = fs.Int("burst", 0, "request burst for --qps (R1.7); 0 selects 50")
	sf.reqTimeout = fs.Duration("request-timeout", 0, "bound on a single API request (R1.7); 0 selects 15s")
	return sf
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

	sf := newServeFlags(prefixed)

	// Parse, then resolve. The order IS R11.8's precedence chain expressed as
	// control flow: flag.Parse records which flags the command line actually set,
	// and apply writes only to the ones it did not — so a flag given on the
	// command line beats the env and ConfigMap layers by construction rather than
	// by a comparison. Running apply before Parse would invert that, because
	// every env value would then be overwritten by the flag's own default.
	if err := sf.Parse(rest); err != nil {
		return 2
	}
	if _, err := applyFlags(sf.FlagSet, OSEnv()); err != nil {
		// Before the logger exists, so this is the same Fprintf the pre-scan
		// failure gets: R13.7 wants a startup failure to be a log line, and there
		// is no logger to make one yet.
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 2
	}

	// The logger is built before anything else that can talk, so a startup
	// failure is a log line and not a printf of unknown shape (R13.7).
	log, err := logx.NewFromFlags(*sf.logLevel, sf.logField)
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
		Mode:             api.AuthMode(*sf.authMode),
		MetricsMode:      api.AuthMode(*sf.authMetrics),
		Token:            *sf.authToken,
		TokenFile:        *sf.authTokenFile,
		MetricsToken:     *sf.authMetricsToken,
		MetricsTokenFile: *sf.authMetricsTokenFile,
	}.Build()
	if err != nil {
		log.Error("auth configuration", "error", err.Error())
		return 2
	}

	cfg := api.Config{Listen: *sf.listen, ShutdownTimeout: *sf.shutdown, Auth: authz, Log: log}

	// --- CORS (R8.9) -----------------------------------------------------
	// An empty allow-list leaves cfg.CORS nil, which cors.go treats as "no CORS
	// headers at all" — the correct answer for the same-origin deployment R8.6
	// describes, and the reason "*" is not the default here.
	if origins := api.ParseCORSAllowOrigin(*sf.corsOrigin); len(origins) > 0 {
		cfg.CORS = &api.CORSConfig{
			AllowedOrigins:   origins,
			AllowCredentials: *sf.corsCreds,
			MaxAge:           *sf.corsMaxAge,
		}
	}

	// --- R8.3 PromQL pair --------------------------------------------------
	// Built unconditionally, unlike the nil-unless-configured CORS above: with
	// --prometheus-url the pair proxies, without it it serves the catalogue
	// (NewPromQL fills in DefaultCatalogue and an empty source), and there is no
	// configuration in which the two documented routes should not exist.
	cfg.PromQL = api.NewPromQL(api.PromQLConfig{PrometheusURL: *sf.prometheusURL})

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
	schedDone := make(chan error, 1)
	if *sf.discover {
		kc, err := kube.New(kube.Config{
			Kubeconfig:         *sf.kubeconfig,
			Context:            *sf.kubeContext,
			APIServerURL:       *sf.apiServer,
			BearerToken:        *sf.bearer,
			ClientCertFile:     *sf.clientCert,
			ClientKeyFile:      *sf.clientKey,
			InsecureSkipVerify: *sf.insecure,
			UseProxy:           *sf.useProxy,
			QPS:                float32(*sf.qps),
			Burst:              *sf.burst,
			Timeout:            *sf.reqTimeout,
		})
		if err != nil {
			log.Error("kube client", "error", err.Error())
			return 2
		}
		disc = discovery.New(discovery.Config{
			Namespaces:        splitList(*sf.namespaces),
			ExcludeNamespaces: splitList(*sf.exclude),
			Resync:            *sf.resync,
			StaleGrace:        *sf.staleGrace,
			Credentials: discovery.CredentialsConfig{
				Secret:   *sf.credSecret,
				Username: *sf.credUser,
				Password: *sf.credPass,
				DBName:   *sf.credDB,
			},
		}, kc, log)
		cfg.Fleet = disc
		go func() { discDone <- disc.Run(ctx) }()

		// --- collectors (Phase 2 step 5's wiring, R2.1) -------------------
		// The registry, the sink and the loop. Before this block the three
		// framework types existed with no caller outside a test: NewRegistry,
		// NewScheduler and NewMemSink were all constructed in their own package's
		// tests and nowhere in the binary, so `pgsi serve` answered /metrics with
		// an empty pgsi_collector_* family and no pgsi_scrape_target_up at all
		// while every collector in internal/collector sat unrun.
		//
		// It is inside the discovery branch because the target set comes from the
		// inventory: with --discover=false there is no fleet, so there is nothing
		// to collect from, and Run over an empty target set is a goroutine that
		// logs rounds of nothing. The `api` role D9 reserves is exactly the role
		// that serves without collecting.
		//
		// The opener is built from the same credential flags the discovery block
		// above gets (R1.8's --credential-secret / --credential-username /
		// --credential-password), so the process has one answer to "who do we
		// connect as" rather than one per consumer. It is nil when kc has no
		// clientset, and newRegistry treats a nil opener as "no SQL collectors"
		// rather than as seven collectors that fail every round.
		opener := newSQLOpener(kc, collector.ConnConfig{
			Secret:   *sf.credSecret,
			Username: *sf.credUser,
			Password: *sf.credPass,
			SSLMode:  "",
		})
		reg, err := newRegistry(kc, opener, collector.Overrides{})
		if err != nil {
			log.Error("collector registry", "error", err.Error())
			return 2
		}
		// The sink is the memory one (R13.5's --store=memory form, §3.4) at
		// NewMemSink's own default ceiling, 64 MiB. The SQLite sink exists
		// (internal/store/sink.go:60 asserts `var _ collector.Sink = (*Store)(nil)`)
		// and is A5's; selecting between the two is R11.8's --store= flag, which
		// serveFlags does not have yet, so the framework default is what runs.
		//
		// The Stats is not passed in and then re-named — it is read back out of the
		// scheduler, which is the only shape that cannot produce two of them.
		// api.Config.Metrics' docblock states the rule ("A process that does run a
		// scheduler assigns that scheduler's own *Stats here, so /metrics and the
		// collection loop read one value") and Scheduler.Stats()'s docblock exists
		// to satisfy it ("A caller that builds a Scheduler ... has no way to name
		// the *Stats that scheduler writes into"). Passing collector.NewStats()
		// into NewScheduler and assigning the same variable to cfg.Metrics is
		// equally correct; reading it back makes the equality structural instead of
		// a thing to remember.
		sched := collector.NewScheduler(collector.Config{}, reg, log, collector.NewMemSink(0), nil)
		cfg.Scheduler = sched
		cfg.Metrics = sched.Stats()
		// The targets func is called once per round rather than snapshotted here,
		// which is what makes R1.9's "drop collectors within one resync" work: a
		// pod that scaled out appears in the next round's set and starts
		// exporting, and one that scaled in stops, with no resync plumbing in the
		// scheduler beyond calling the func again.
		go func() {
			schedDone <- sched.Run(ctx, func() []collector.Target { return collectTargets(disc.Inventory()) })
		}()
	} else {
		// D9's `api` role: no fleet, so New gives the server a source-less
		// Readiness that turns true once the socket opens (R8.7(ii)).
		log.Info("discovery disabled", "role", "api", "reason", "--discover=false (D9's reserved API role)")
		discDone <- nil
		// No scheduler was started, so there is nothing to drain; the channel
		// needs a value for the same reason discDone does, and Run's error is the
		// one thing this branch can guarantee.
		schedDone <- nil
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
	// The scheduler is drained second, in start order after discovery, so the
	// shutdown log reads in the order the subsystems started. Both loops share the
	// signal ctx, so both return at the instant srv.Run returns; the only failure
	// this can surface that discovery's cannot is Run's own "nil targets func"
	// error, which is a wiring bug rather than a runtime one and belongs at the
	// same exit code as a discovery failure.
	schedErr := <-schedDone
	if schedErr != nil && !errors.Is(schedErr, context.Canceled) {
		log.Error("collector scheduler stopped", "error", schedErr.Error())
		if serveErr == nil {
			return 1
		}
	}
	if serveErr != nil {
		return 1
	}
	return 0
}

// newRegistry builds this process's collector set: the []Collector argument
// R2.1's registry takes, with R2.3's overrides applied over each Descriptor.
//
// It holds eight collectors when the opener is non-nil and one when it is nil,
// and the two shapes exist because the two halves of §3.3 have different
// dependencies.
//
// The seven SQL collectors — activity, database, locks, replication, settings,
// statements and wal — all take an `opener SQLOpener` as their first argument,
// and for most of this repository's life there was no value to pass them: the
// paragraph documenting the SQL seam (sql.go:34-48, inside
// DefaultStatementTimeout's docblock) declared the seam an interface precisely
// because go.mod had no driver, and declined to choose one. That paragraph now
// names the answer in the same breath — pgx/v5 via pgx/stdlib, in
// internal/collector/pgxopener, the driver R13.10 names — and this function is
// the caller that deferral was waiting for.
//
// go.mod's own module comment was rewritten in the same commit that added the
// driver, for the reason A3's S67 records: the old comment said the module
// "names only client-go plus, for Phase 3, modernc.org/sqlite" while the direct
// require block listed five modules. Prose describing its own file has no
// checker, so the only cheap moment to fix it is the commit that makes it false.
//
// The pooler is the eighth and is constructible independently: NewPoolerCollector
// takes a MetricsFetch, and pooler.go:51-54 says the production implementation
// "is a method value — kube.Client.GetDirect(ctx, url) has exactly this signature
// (client.go:300) — so wiring it in cmd/pgsi is `kube.GetDirect` with no
// adapter". That is what the first argument below is: a method value, no adapter,
// exactly as promised.
//
// The remaining two of §3.3's thirteen — instance-metrics (S3) and k8s-events
// (S4) — have no file yet, so there is nothing to construct; see targets.go for
// why their targets are not produced either.
//
// The overrides argument is empty rather than built from argv, and that is a gap
// this commit leaves open on purpose: R2.3's --interval.<collector>= and
// --disable-collector= flags need R11.8's dynamic flag pre-scan (the logx.
// FieldsFromArgs pattern applied to a second flag name), and registry.go's
// IntervalOverridesFromArgs/DisabledFromFlag helpers exist for that. R2.3's own
// row files the cmd/pgsi half of it with step 5's R11.8 work rather than with
// this wiring, so the registry is built with defaults and the helpers stay
// uncalled.
func newRegistry(kc *kube.Client, opener collector.SQLOpener, ov collector.Overrides) (*collector.Registry, error) {
	pooler, err := collector.NewPoolerCollector(kc.GetDirect, collector.PoolerConfig{})
	if err != nil {
		return nil, err
	}
	collectors := []collector.Collector{pooler}

	// The seven SQL collectors ride the opener, and the nil check is the reason
	// the signature has an opener parameter at all. Each of the seven constructors
	// is `func NewXCollector(opener SQLOpener, cfg SQLConfig) *XCollector` — one
	// return value, no error — so each accepts a nil opener at construction and
	// only fails when Collect runs. Registering seven collectors that cannot
	// collect would turn a wiring gap into eight error logs per round on a healthy
	// /metrics endpoint, which is R8.7's shape (a process that answers 200 and is
	// not actually working) reproduced inside the registry. Gating here makes the
	// registry's contents say what the process can actually do.
	//
	// pooler-clients joins this branch rather than riding with its S5 sibling above
	// it, and the branch is chosen by what the collector needs, not by which §3.3
	// row it came from: pooler_clients.go's constructor takes an SQLOpener because
	// SHOW CLIENTS is a psql round-trip on 5432, so with no opener it cannot
	// collect, which is the exact condition this gate exists to detect. pooler.go
	// above needs kc.GetDirect and nothing else, which is why it is outside.
	if opener != nil {
		cfg := collector.SQLConfig{}
		collectors = append(collectors,
			collector.NewPoolerClientsCollector(opener, cfg),
			collector.NewActivityCollector(opener, cfg),
			collector.NewDatabaseCollector(opener, cfg),
			collector.NewLocksCollector(opener, cfg),
			collector.NewReplicationCollector(opener, cfg),
			collector.NewSettingsCollector(opener, cfg),
			collector.NewStatementsCollector(opener, cfg),
			collector.NewWALCollector(opener, cfg),
		)
	}
	return collector.NewRegistry(collectors, ov)
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
