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
	if err := fs.Parse(args); err != nil {
		return 2
	}

	srv := api.New(api.Config{Listen: *listen, ShutdownTimeout: *shutdown})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}
