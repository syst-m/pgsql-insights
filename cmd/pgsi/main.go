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
	"github.com/frankellis/pgsql-insights/internal/metrics"
	"github.com/frankellis/pgsql-insights/internal/store"
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
	serveUI              *bool
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

	// R2.9 — the global scheduler budget. R2.1's per-cluster cap has no flag of
	// its own (its default of 4 is a constant the scheduler applies), but R2.9's
	// three do, because that requirement names each one by name and R11.8's dump
	// has to print what the scheduler was configured with. They are a separate
	// alignment group from the block above only so that adding a 24-character
	// field name does not re-pad thirty existing lines.
	maxConcurrentSQL         *int
	maxConcurrentHTTPScrapes *int
	schedulerQueueDepth      *int

	// R2.3's two halves, which the requirement spells in two shapes and so cannot
	// share one declaration. disableCollector is a declared flag: R2.3's
	// `--disable-collector=a,b` is a single static string whose value is the list.
	// intervals is a pre-scan product: `--interval.<collector>=` puts the
	// collector name inside the flag *name*, and the flag package cannot declare a
	// name it has to invent at runtime, so scanArgs lifts those entries out of
	// argv before Parse and this field holds what it found — the same reason
	// logField above carries logx's pre-scan result alongside its fs.Var.
	disableCollector *string
	intervals        map[string]time.Duration

	// store selects which store the process writes to: R11.6's two profiles,
	// §3.4:218's SQLite file and §3.4:219's ring buffers. Its group boundary here
	// is readability, not gofmt: at five characters it re-pads nothing, because
	// gofmt only re-pads a run when a name *longer* than that run's widest joins
	// it — which is the real reason the R2.9 trio above is separate (24 characters
	// against that block's 20) and the reason the sentence this one replaces, "a
	// five-character name joined to that block re-pads all three", was never true
	// of store.
	store *string
}

// prescan is everything argv holds that the serve FlagSet cannot declare.
//
// Go's flag package only accepts names it was told about, so the two flag
// families whose names get built at runtime have to be lifted out of argv before
// Parse runs: R13.7's `--log-field-<key>=<value>` set, which logx's scanner
// returns, and R2.3's `--interval.<collector>=<duration>` set, which
// collector.IntervalOverridesFromArgs returns. This is what the lift produces,
// plus the argument list left over for Parse.
//
// One type and one function because two commands have to agree about it. `serve`
// and `config dump` both read argv, and R11.8's claim for the dump is that it
// answers "what would serve use" — two commands each writing their own scanner
// calls is the bug that claim exists to catch, which is the same argument
// applyFlags' docblock makes about the ConfigMap layer. With one function here,
// the two call sites differ only in their error prefix.
type prescan struct {
	// logFields is R13.7's --log-field-* set.
	logFields logx.Fields
	// intervals is R2.3's --interval.<collector>= set.
	intervals map[string]time.Duration
	// rest is argv with both sets removed: the list to hand to flag.Parse.
	rest []string
}

// scanArgs runs both pre-scans, in one order, for both commands.
//
// The order between the two scanners does not matter — each strips only its own
// prefix, so logx's output is a valid input for the interval scanner and the
// reverse would do — but having *an* order does. It is the difference between one
// rule about argv and two that can drift apart.
func scanArgs(args []string) (prescan, error) {
	fields, afterFields, err := logx.FieldsFromArgs(args)
	if err != nil {
		return prescan{}, err
	}
	intervals, rest, err := collector.IntervalOverridesFromArgs(afterFields)
	if err != nil {
		return prescan{}, err
	}
	return prescan{logFields: fields, intervals: intervals, rest: rest}, nil
}

// newServeFlags declares every `pgsi serve` flag and returns them as one value.
//
// `pre` is the argv pre-scan, the two dynamic flag names lifted out before Parse
// (see prescan). Its logFields half seeds the --log-field flag's value so both
// R13.7 spellings accumulate into one set, exactly as runServe did before the
// extraction; its intervals half is stored on the struct for newRegistry, the
// only consumer, because there is no flag name to declare it under.
func newServeFlags(pre prescan) *serveFlags {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sf := &serveFlags{FlagSet: fs}

	sf.listen = fs.String("listen", ":8080", "address the HTTP server binds (R8.7)")
	sf.shutdown = fs.Duration("shutdown-timeout", 0, "graceful-shutdown drain budget (R13.8); 0 selects 10s")
	sf.serveUI = fs.Bool("serve-ui", true, "serve the embedded SPA from this binary (R8.6); false is an API-only deployment")

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
	sf.logField = pre.logFields
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

	// --- R2.9 scheduler budget ---------------------------------------------
	//
	// All three default to 0 and name the real number in the usage string, which
	// is the pattern the three R1.7 lines above them set (--qps 0 "0 selects 25",
	// --burst 0 "0 selects 50", --request-timeout 0 "0 selects 15s"). Here it is
	// more than a convention: collector.Config.withDefaults maps every non-positive
	// field to its Default constant, so 0 is the value that means "use 32" and a
	// --max-concurrent-sql -1 also lands on 32 instead of reaching makechan, which
	// panics outright on a negative capacity and would crash the binary at startup.
	sf.maxConcurrentSQL = fs.Int("max-concurrent-sql", 0, "global cap on concurrent Postgres-wire collections (R2.9); 0 selects 32")
	sf.maxConcurrentHTTPScrapes = fs.Int("max-concurrent-http-scrapes", 0, "global cap on concurrent HTTP scrapes (R2.9); 0 selects 64")
	sf.schedulerQueueDepth = fs.Int("scheduler-queue-depth", 0, "collections the scheduler queues before it starts shedding them (R2.9); 0 selects 512")

	// --- R2.3 collector selection --------------------------------------------
	//
	// One requirement, two flags, and only one of them is declarable here.
	// registry.go:186-189 says which one and why: --disable-collector "is a single
	// static flag (its value is the list, `--disable-collector=a,b`), so cmd/pgsi
	// declares it with fs.String and splits on commas. Only the interval form has a
	// dynamic *name*, and only it needs this function." So the interval half is
	// assigned from the pre-scan rather than declared — there is no single name to
	// declare, since --interval.wal and --interval.database are two names and R2.3's
	// lab profile passes whichever subset it wants. The assignment sits here, next
	// to the flag it is the other half of, exactly as the R13.7 block above assigns
	// sf.logField from the same pre-scan before declaring its fs.Var.
	sf.disableCollector = fs.String("disable-collector", "", "collectors to leave out of the registry (R2.3), comma-separated; empty enables every collector this process can build")
	sf.intervals = pre.intervals

	// --- R11.6 store profile ------------------------------------------------
	// requirements.md spells the flag once — `--store=memory`, §3.4:219's
	// Alternative row, whose rationale names the lab profile (R11.6) — and the
	// chart spells the same key's value set as `sqlite | memory`
	// (chart/values.yaml:64, still a commented Phase 2+ key under config.extra).
	// Both spellings are accepted here, plus a bare path, because
	// internal/store/store.go:314's isMemory already recognises ":memory:",
	// "memory", "mem" and "" as one profile and treats any other string as a
	// filename; a flag that rejected a value its own store accepts would be a
	// second answer to "what did we configure". The default is memory, which is
	// what the binary ran on before this flag existed and what R11.6's lab
	// profile ships.
	sf.store = fs.String("store", defaultStoreProfile, "query store (R11.6's two profiles, §3.4:218-219): memory (R13.5's ring buffers) | sqlite (the "+defaultStoreFile+" file) | an explicit SQLite path")
	return sf
}

func runServe(args []string) int {
	// Both dynamic flag names come out of argv before Parse sees it: R13.7's
	// `--log-field-*` family and R2.3's `--interval.<collector>=` family. Neither
	// can be declared as a Go flag (the flag package has no dynamic flag names),
	// and scanArgs is the one place the two scanners are composed, so `serve` and
	// `config dump` cannot disagree about what argv meant. Everything else parses
	// normally, including the plain repeatable `--log-field k=v`.
	pre, err := scanArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 2
	}

	sf := newServeFlags(pre)

	// Parse, then resolve. The order IS R11.8's precedence chain expressed as
	// control flow: flag.Parse records which flags the command line actually set,
	// and apply writes only to the ones it did not — so a flag given on the
	// command line beats the env and ConfigMap layers by construction rather than
	// by a comparison. Running apply before Parse would invert that, because
	// every env value would then be overwritten by the flag's own default.
	if err := sf.Parse(pre.rest); err != nil {
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

	cfg := api.Config{Listen: *sf.listen, ShutdownTimeout: *sf.shutdown, ServeUI: sf.serveUI, Auth: authz, Log: log}

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
		// rather than as eight collectors that fail every round.
		opener := newSQLOpener(kc, collector.ConnConfig{
			Secret:   *sf.credSecret,
			Username: *sf.credUser,
			Password: *sf.credPass,
			SSLMode:  "",
		})
		// The overrides are built from argv now: the interval map the pre-scan
		// lifted out of it, and the disable list split out of --disable-collector
		// by registry.go's own DisabledFromFlag. The argument used to be a bare
		// collector.Overrides{}, which newRegistry's docblock documented as a gap
		// left open on purpose pending R2.3's cmd/pgsi half — this is that half, so
		// the two registry.go helpers that existed uncalled are now called.
		reg, err := newRegistry(kc, opener, collector.Overrides{
			Intervals: pre.intervals,
			Disabled:  collector.DisabledFromFlag(*sf.disableCollector),
		})
		if err != nil {
			log.Error("collector registry", "error", err.Error())
			return 2
		}
		// The sink comes from --store. The comment that used to sit here called
		// the SQLite sink "A5's" and said selecting between the two was
		// "R11.8's --store= flag, which serveFlags does not have yet" — this
		// commit closes that gap and fixes two misattributions in the sentence
		// with it: the requirement that names the flag is R11.6's
		// (requirements.md:219's `--store=memory` row, whose rationale is the lab
		// profile), while R11.8 is the config-*precedence* requirement the flag is
		// listed under in config.go, and R13.5's named flag is --max-store-bytes,
		// not --store. The selection is now openSink below rather than a permanent
		// NewMemSink(0).
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
		bundle, err := openSink(*sf.store)
		if err != nil {
			log.Error("store", "error", err.Error(), "store", *sf.store)
			return 2
		}
		// The SQLite handle closes on the way out, after both loops are drained:
		// the scheduler is the writer (R11.15's single-writer half), so closing
		// before srv.Run returns would fail in-flight publishes rather than the
		// ones after shutdown. The memory profile owns no handle, so bundle.close
		// is nil and nothing is deferred for it.
		if bundle.close != nil {
			defer func() {
				if err := bundle.close(); err != nil {
					log.Error("store close", "error", err.Error())
				}
			}()
		}
		// The three R2.9 flags are read here, at the one place a Config is built for
		// a real round. A flag declared and never passed is the failure R11.8's dump
		// exists to expose — the dump would print --max-concurrent-sql 8 while the
		// scheduler ran on 32 — so this literal is the flags' only consumer and each
		// field is named rather than left at zero. A flag left at 0 still reaches
		// withDefaults and gets its documented default, which is what lets 0 mean
		// "unset" in the flag and in the Config without two separate rules.
		sched := collector.NewScheduler(collector.Config{
			MaxConcurrentSQL:         *sf.maxConcurrentSQL,
			MaxConcurrentHTTPScrapes: *sf.maxConcurrentHTTPScrapes,
			SchedulerQueueDepth:      *sf.schedulerQueueDepth,
		}, reg, log, bundle.sink, nil)
		cfg.Scheduler = sched
		cfg.Metrics = sched.Stats()
		// The store-size gauge gets the sink the scheduler writes into, for the
		// reason cfg.Metrics gets the scheduler's own *Stats: one value, read out of
		// the consumer rather than built a second time. api.Config.StoreSize's
		// docblock states the rule (defaulting it would report the size of a store
		// nobody writes to); this assignment is what makes the gauge report the one
		// that is being filled. It is inside the discovery branch with the sink
		// itself, so --discover=false leaves it nil and the family absent — the same
		// answer that branch already gives for the scheduler's two families.
		cfg.StoreSize = bundle.storeSize
		// R9.5's per-query family is the third field fed by the same object, and
		// the reason the two profiles cannot be assigned identically: only
		// *store.Store has a RenderPerQuery, so the memory profile leaves this nil
		// (the nil-interface case api.Config.PerQueryMetrics' docblock describes,
		// not a typed nil) and /metrics renders the family absent. Assigning a
		// store while the scheduler wrote to a MemSink — the shape A5's request
		// asked for on its own — would give a family that renders nothing but
		// exists: internal/store/perquery.go returns "" for a store with no rows,
		// which TestRenderPerQueryEmptyStore pins, so the assignment is only
		// meaningful once the store is the sink. That is why it lands in this commit
		// rather than as the one-line change on its own.
		cfg.PerQueryMetrics = bundle.perQuery
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

// defaultStoreProfile is --store's default value: §3.4:219's memory profile,
// spelled as requirements.md:219 spells it. It is a constant rather than the
// literal "memory" in the flag declaration because openSink compares against
// it, and a flag default and a switch case that are the same string in two
// places is how a binary ends up defaulting to a profile its own switch does
// not recognise.
const defaultStoreProfile = "memory"

// defaultStoreFile is the SQLite file the sqlite profile opens when --store
// names the profile without a path. §3.4:218 names the engine and R11.15 names
// the RWO PVC, but neither names a filename; the repo spells one in two places
// that agree — chart/values.yaml:192's persistence.mountPath (/var/lib/pgsi)
// and internal/store/store_test.go:257's Config{Path: "/var/lib/pgsi/store.db"}
// — so this is the third copy of an existing agreement, not a new one. A future
// --store-path flag would replace it rather than contradict it.
const defaultStoreFile = "/var/lib/pgsi/store.db"

// sinkBundle is one store resolved into every api.Config field that reads it.
//
// The first three fields are separate types — collector.Sink for the scheduler,
// metrics.StoreSizer for R13.5's gauge, metrics.PerQueryFamily for R9.5's
// family — and a process that fills them with three different objects reports
// the size of one store while serving queries from another.
// api.Config.StoreSize's docblock states the rule ("cmd/pgsi assigns the sink it
// already builds for the scheduler rather than a second one"); constructing the
// bundle once is how the code enforces it instead of restating it. The field
// types make each assignment a compile-time check that the concrete type
// implements that interface, which is why the branches below assign concrete
// values rather than passing a collector.Sink around and asserting it back at
// the call site — an assertion without the ok form panics, and with the ok form
// it silently degrades to "family absent" on a type the day someone adds a
// fourth sink implementation.
type sinkBundle struct {
	sink      collector.Sink
	storeSize metrics.StoreSizer
	perQuery  metrics.PerQueryFamily

	// The four Phase 3 read-side views of the same handle: R3.2's query list,
	// R3.9's series, R3.13's detail and R3.11's tags. They are in this struct for
	// the reason the three above are — api.Config names one field per route and
	// each docblock says "Phase 3's query store replaces the default by assigning
	// this field", the store shipped, and nothing in cmd/pgsi did the assigning, so
	// a SQLite-backed process served emptyQuerySource on four documented routes
	// while holding the rows it serves. Declaring them as the api interface types
	// rather than as *store.Store keeps each assignment a compile-time check that
	// the store satisfies that route's contract, which is the argument the three
	// fields above give for their own types.
	queries api.QueryListSource
	series  api.QuerySeriesSource
	detail  api.QueryDetailSource
	tags    api.TagsSource

	// close is the SQLite handle's Close, nil for the memory profile because a
	// collector.MemSink owns no file. A func field rather than an io.Closer
	// assertion so the nil case is a nil check, not a type switch.
	close func() error
}

// openSink resolves --store's value into the sinkBundle the server and the
// scheduler share.
//
// Three value shapes, tested in this order:
//
//   - "memory" (case-insensitive) → collector.NewMemSink(0), R13.5's ring-buffer
//     store at its own DefaultMaxBytes. R9.5's family is nil here: MemSink has no
//     RenderPerQuery to offer, so the field stays nil and /metrics renders the
//     family absent. The four read-side views are nil for the same reason — a
//     MemSink holds samples, not the normalised query rows R3.2's route serves —
//     and api's accessors turn each nil into its documented empty source.
//   - "sqlite" / "sqlite:" (case-insensitive) → the §3.4:218 profile at
//     defaultStoreFile, the path the chart's PVC mounts. All three fields fill
//     from the one *store.Store. The trailing-colon spelling is accepted because
//     store.Config.isMemory accepts "memory:" and a value that only half the
//     spellings of one profile resolve to is how "sqlite:" ends up opening a
//     file named "sqlite:" in the working directory.
//   - anything else → that string as a SQLite path. This is what
//     store.Config.Path takes, and it is what a deployment with a non-default
//     mount needs; chart/values.yaml:64 says the value is passed through
//     verbatim, and this arm is what honours that. One consequence is worth
//     naming: --store=:memory: lands here rather than in the MemSink arm, so it
//     opens a SQLite database in RAM instead of the ring buffers — which is what
//     internal/store/store.go:314's isMemory does with that string, and the
//     reason this switch recognises the profile *name* and lets the store
//     interpret every other spelling of it.
//
// The sqlite arms open eagerly and hand back the handle's Close, so a bad path
// fails `pgsi serve` at startup with the path in the error rather than one
// collection round later inside a Publish.
func openSink(spec string) (sinkBundle, error) {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case defaultStoreProfile:
		sink := collector.NewMemSink(0)
		return sinkBundle{sink: sink, storeSize: sink}, nil
	case "sqlite", "sqlite:":
		return openStoreFile(defaultStoreFile)
	default:
		return openStoreFile(spec)
	}
}

// openStoreFile fills all seven fields from one *store.Store, which is the whole
// point: the sink the scheduler writes to, the gauge /metrics reads, the family
// R9.5 renders and the four read routes the API serves are one handle, opened
// once, closed once. Splitting the write side from the read side — a store the
// scheduler fills and a second store the routes read — is the shape the struct's
// docblock refuses, and it is the shape the four read fields had before this
// function filled them: the store existed, the assignment did not.
func openStoreFile(path string) (sinkBundle, error) {
	st, err := store.Open(store.Config{Path: path})
	if err != nil {
		return sinkBundle{}, err
	}
	return sinkBundle{
		sink: st, storeSize: st, perQuery: st,
		queries: st, series: st, detail: st, tags: st,
		close: st.Close,
	}, nil
}

// newRegistry builds this process's collector set: the []Collector argument
// R2.1's registry takes, with R2.3's overrides applied over each Descriptor.
//
// It holds ten collectors when the opener is non-nil and one when it is nil,
// and the two shapes exist because the two halves of §3.3 have different
// dependencies.
//
// The eight SQL collectors — activity, database, io, locks, replication,
// settings, statements and wal — all take an `opener SQLOpener` as their first
// argument, and for most of this repository's life there was no value to pass
// them: the paragraph documenting the SQL seam (sql.go:34-48, inside
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
// The pooler-clients collector is the ninth and the pooler itself the tenth;
// the pooler is constructible independently of the opener: NewPoolerCollector
// takes a MetricsFetch, and pooler.go:51-54 says the production implementation
// "is a method value — kube.Client.GetDirect(ctx, url) has exactly this signature
// (client.go:300) — so wiring it in cmd/pgsi is `kube.GetDirect` with no
// adapter". That is what the first argument below is: a method value, no adapter,
// exactly as promised.
//
// The remaining three of §3.3's thirteen — progress, instance-metrics (S3) and
// k8s-events (S4) — have no file yet, so there is nothing to construct; see
// targets.go for why their targets are not produced either.
//
// The overrides argument is built from argv now, by runServe's scanArgs. The
// paragraph this replaces said it was "empty rather than built from argv, and that
// is a gap this commit leaves open on purpose": R2.3's two flags needed R11.8's
// dynamic flag pre-scan before cmd/pgsi had anything to pass, because the interval
// form has a dynamic *name* and so needs the logx.FieldsFromArgs pattern applied
// to a second flag name, and registry.go's IntervalOverridesFromArgs and
// DisabledFromFlag existed for that and stayed uncalled. R2.3's own row filed the
// cmd/pgsi half with step 5's R11.8 work rather than with that wiring; the chain
// landed at bf55762, the second scan landed with the prescan/scanArgs pair in this
// file, and both helpers are called from runServe now — so the registry gets the
// intervals and the disable list the operator actually passed instead of defaults.
func newRegistry(kc *kube.Client, opener collector.SQLOpener, ov collector.Overrides) (*collector.Registry, error) {
	pooler, err := collector.NewPoolerCollector(kc.GetDirect, collector.PoolerConfig{})
	if err != nil {
		return nil, err
	}
	collectors := []collector.Collector{pooler}

	// The eight §3.3 SQL collectors plus pooler-clients ride the opener — nine
	// constructors in the append below — and the nil check is the reason the
	// signature has an opener parameter at all. Each of the nine constructors is
	// `func NewXCollector(opener SQLOpener, cfg SQLConfig) *XCollector` — one
	// return value, no error — so each accepts a nil opener at construction and
	// only fails when Collect runs. Registering nine collectors that cannot
	// collect would turn a wiring gap into nine error logs per round on a healthy
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
			collector.NewIOCollector(opener, cfg),
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
