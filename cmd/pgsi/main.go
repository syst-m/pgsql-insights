// Command pgsi is the single pgsql-insights binary (R11.1): API, collector and
// embedded UI in one process.
//
// Subcommand surface (R3.1): serve | collect | api | version.
// Per spec §3.1 and IMPLEMENTATION.md D9 (A3-M1), v1 ships exactly one working
// role — serve. `collect` and `api` are reserved stubs that exit without doing
// work, so the v1.x --role=collect|serve split can land without breaking the
// CLI contract that Phase 0 freezes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/frankellis/pgsql-insights/internal/api"
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
  --cors-allow-origin           CORS allow-list (R8.9); empty = same-origin  (none)
  --cors-allow-credentials      Access-Control-Allow-Credentials (R8.9)      false
  --cors-max-age                preflight cache lifetime (R8.9)              5m

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

	// --- R8.9 CORS -------------------------------------------------------
	// Empty --cors-allow-origin means no CORS headers at all. R8.6 serves the
	// SPA from this same binary, so the normal deployment is same-origin and
	// defaulting to "*" would open the API to every origin for nobody's
	// benefit (and "*" is invalid alongside credentials). See internal/api/cors.go.
	corsOrigin := fs.String("cors-allow-origin", "", "comma-separated CORS allow-list (R8.9); empty means same-origin only, no CORS headers")
	corsCreds := fs.Bool("cors-allow-credentials", false, "send Access-Control-Allow-Credentials on CORS responses (R8.9)")
	corsMaxAge := fs.Duration("cors-max-age", 0, "how long a browser may cache a preflight (R8.9); 0 selects 5m")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Auth is built before the listener is opened, and a bad combination is a
	// startup failure (exit 2, the same code a flag parse error gets) rather
	// than a serving pod that 401s everything: a deployment that rolled out
	// with an empty Secret looks healthy in the Deployment and broken through
	// the Service.
	authz, err := api.AuthzConfig{
		Mode:        api.AuthMode(*authMode),
		MetricsMode: api.AuthMode(*authMetrics),
		Token:       *authToken,
		TokenFile:   *authTokenFile,
	}.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 2
	}

	cfg := api.Config{Listen: *listen, ShutdownTimeout: *shutdown, Auth: authz}

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

	srv := api.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}
