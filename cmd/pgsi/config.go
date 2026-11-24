package main

// R11.8 — the config precedence chain and `pgsi config dump`.
//
// requirements.md:451 is one sentence with two deliverables in it:
//
//	"Config precedence: CLI flags > `PGSI_*` env > mounted ConfigMap > built-in
//	defaults, documented per flag; `pgsi config dump` prints the resolved config
//	for debugging."
//
// The second half is what makes the first half checkable. A precedence chain you
// cannot inspect is a chain you cannot debug: the failure it exists to serve is
// "I set --listen and it is still on :8080" / "the chart sets 10s and the pod is
// using 15s", and the answer to both is *which layer won*, which is a fact about
// the chain rather than about any one value. So `dump` prints the winning layer
// per flag as a first-class column, not the values alone — a dump that shows
// `:8080` without saying it came from the built-in default is the same string
// the flag's own -h already printed and answers nothing.
//
// The chain itself is implemented by apply, and the mechanism is the flag
// package's own rule rather than thirty hand-written if-ladders: flag.Parse
// assigns a value only for a flag that appears on the command line, so a value
// placed on the FlagSet *before* Parse is kept iff the command line did not
// mention it. Writing the env/ConfigMap winner in with fs.Set and then letting
// Parse run is therefore exactly "CLI flags beat env beats ConfigMap beats
// default", with the flag package enforcing the top of the chain for free — and
// fs.Set parsing the value on the way in, so PGSI_RESYNC=5x is a startup error
// from the same Duration parser --resync=5x would trip, not a silent zero.
//
// The table below is the third source of truth this file refuses to add.
// main.go's package doc says the flag set "is the contract R11.8's precedence
// chain will read", and the only way that stays true is if the chain reads the
// FlagSet instead of restating it: the default comes from flag.Flag.DefValue and
// the documentation string from flag.Flag.Usage, so a row is a flag *name* plus
// the requirement id it implements. Nothing here can drift from main.go, because
// there is no second copy of either field to drift from — and
// TestTableCoversEveryFlag in config_test.go is what stops the table and the
// FlagSet from diverging in either direction, which is the cmd/pgsi equivalent of
// what scripts/check-parity-matrix.sh is for requirement ids.

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/frankellis/pgsql-insights/internal/logx"

	"go.yaml.in/yaml/v3"
)

// ConfigFile is the ConfigMap layer of R11.8's chain: the path the binary reads
// for the mounted ConfigMap.
//
// It is the chart's path, not a new one — chart/templates/deployment.yaml mounts
// the ConfigMap at mountPath /etc/pgsi and chart/templates/configmap.yaml's only
// key is config.yaml, so the file is /etc/pgsi/config.yaml. The pairing is the
// same argument api.TokenFile makes for the auth Secret: the path appears in
// A4's chart and in this binary, and exporting it from one of the two is what
// stops them disagreeing.
//
// R11.8 names no --config flag, so this is a constant with a PGSI_CONFIG
// override rather than a new flag: adding a flag is a change to the CLI surface
// Phase 0 froze (docs/agents.md §5's rule about frozen contracts), and the
// requirement's own list of four levels does not include one.
const ConfigFile = "/etc/pgsi/config.yaml"

// ConfigFileEnv is the variable that overrides ConfigFile. It is not a flag
// because R11.8's chain enumerates four levels and "where the ConfigMap is
// mounted" is a property of the deployment, not a setting with a default.
const ConfigFileEnv = "PGSI_CONFIG"

// layer names which of R11.8's four levels produced a flag's value. It is a
// string type rather than a set of bools because the whole point of the dump is
// to print it, and because "flag" < "env" < "configmap" < "default" is an
// ordering the resolver depends on having stated somewhere.
type layer string

const (
	// layerFlag: the value was given on the command line. It wins over every
	// other layer, which is R11.8's first position.
	layerFlag layer = "flag"
	// layerEnv: the value came from the flag's PGSI_* environment variable.
	layerEnv layer = "env"
	// layerConfigMap: the value came from a key in the mounted ConfigMap.
	layerConfigMap layer = "configmap"
	// layerDefault: none of the three above supplied a value, so the flag's own
	// declared default (flag.Flag.DefValue) is what is in use.
	layerDefault layer = "default"
)

// setting is one row of the precedence table: a flag's name, the requirement id
// it exists to implement, and two properties the resolver cannot infer from the
// FlagSet.
type setting struct {
	// Flag is the name as declared on the serve FlagSet, without the leading
	// dashes: "shutdown-timeout", not "--shutdown-timeout".
	Flag string
	// Req is the requirement this flag implements. The flag's own usage string
	// already cites it in prose ("graceful-shutdown drain budget (R13.8)"), but
	// prose is not parseable and the dump prints the id in a column, so it is
	// also a field. Every value here is copied out of the flag declaration it
	// names; TestEveryRowCitesARequirement checks the shape.
	Req string
	// Secret marks a value that must not be printed in the clear. R11.8 says
	// dump "prints the resolved config for debugging", and the debugging use is
	// usually remote — pasted into an issue, shown to a colleague — where
	// echoing R1.8's password or R1.11's bearer token turns a diagnostic into a
	// credential leak. Secret rows print `***` and keep their SOURCE column, so
	// "the token came from the ConfigMap" is still answerable.
	Secret bool
	// NoEnv marks a flag with no PGSI_* spelling. It exists for exactly one row:
	// --log-field is repeatable (R13.7's `k=v`, repeatable, plus the
	// --log-field-<key> shape), and a single-valued env var cannot represent a
	// list the flag package accumulates across N occurrences. Everything else
	// has an env name, derived by EnvVar rather than listed.
	NoEnv bool
}

// settings is R11.8's table: every flag `pgsi serve` declares, with the
// requirement each one implements.
//
// Order is the order the flags are declared in main.go, because that order is
// grouped by requirement (auth together, CORS together, kube together) and the
// dump is meant to be read by a human who just read the flag set. The resolver
// does not depend on it; TestTableCoversEveryFlag does the set comparison.
var settings = []setting{
	// R8.7 — the listener; R13.8 — the drain budget.
	{Flag: "listen", Req: "R8.7"},
	{Flag: "shutdown-timeout", Req: "R13.8"},
	{Flag: "serve-ui", Req: "R8.6"},

	// R8.8 — auth. Four of the six are tokens; the two -file variants are paths
	// and print in the clear, because a path that does not exist is the
	// diagnosis and hiding it would hide the answer.
	{Flag: "auth", Req: "R8.8"},
	{Flag: "auth-metrics", Req: "R8.8"},
	{Flag: "auth-token", Req: "R8.8", Secret: true},
	{Flag: "auth-token-file", Req: "R8.8"},
	{Flag: "auth-metrics-token", Req: "R8.8", Secret: true},
	{Flag: "auth-metrics-token-file", Req: "R8.8"},

	// R8.9 — CORS.
	{Flag: "cors-allow-origin", Req: "R8.9"},
	{Flag: "cors-allow-credentials", Req: "R8.9"},
	{Flag: "cors-max-age", Req: "R8.9"},

	// R8.3 — the PromQL pair's upstream.
	{Flag: "prometheus-url", Req: "R8.3"},

	// R13.7 — logging. log-field is the one NoEnv row (see the field's doc).
	{Flag: "log-level", Req: "R13.7"},
	{Flag: "log-field", Req: "R13.7", NoEnv: true},

	// R3.1 / D9 — the role knob.
	{Flag: "discover", Req: "R3.1"},

	// R1.1 / R1.9 — discovery scope.
	{Flag: "namespaces", Req: "R1.1"},
	{Flag: "exclude-namespaces", Req: "R1.1"},
	{Flag: "resync", Req: "R1.1"},
	{Flag: "stale-grace", Req: "R1.9"},

	// R1.8 — the configured credential. Password is the secret; username,
	// dbname and the secret *name* are not (R1.8's own text treats the secret
	// reference as config and its contents as the credential).
	{Flag: "credential-secret", Req: "R1.8"},
	{Flag: "credential-username", Req: "R1.8"},
	{Flag: "credential-password", Req: "R1.8", Secret: true},
	{Flag: "credential-dbname", Req: "R1.8"},

	// R1.7 / R1.11 — the kube client.
	{Flag: "kubeconfig", Req: "R1.7"},
	{Flag: "kube-context", Req: "R1.7"},
	{Flag: "api-server-url", Req: "R1.11"},
	{Flag: "bearer-token", Req: "R1.11", Secret: true},
	{Flag: "client-cert", Req: "R1.11"},
	{Flag: "client-key", Req: "R1.11"},
	{Flag: "insecure-skip-verify", Req: "R1.11"},
	{Flag: "use-proxy", Req: "R1.7"},
	{Flag: "qps", Req: "R1.7"},
	{Flag: "burst", Req: "R1.7"},
	{Flag: "request-timeout", Req: "R1.7"},

	// R2.9 — the global scheduler budget. Three rows because the requirement
	// names three flags; none is Secret (they are three integers), and all three
	// are read at the one NewScheduler call site in runServe rather than being
	// dump-only.
	{Flag: "max-concurrent-sql", Req: "R2.9"},
	{Flag: "max-concurrent-http-scrapes", Req: "R2.9"},
	{Flag: "scheduler-queue-depth", Req: "R2.9"},
}

// EnvVar is R11.8's `PGSI_*` naming rule, stated as a function rather than as
// thirty strings.
//
// The requirement says "`PGSI_*`" and stops there, so the mapping from a flag
// name to a variable name is a decision, and a decision spelled 33 times is 33
// chances to spell it differently — the R16.8 failure class (producer and
// consumer disagreeing on a name) waiting to happen between the chart and the
// binary. One function makes the rule the single answer: uppercase the name and
// prefix PGSI_ (dashes become underscores, since a shell variable name cannot
// carry one), so --shutdown-timeout is PGSI_SHUTDOWN_TIMEOUT and --auth-token is
// PGSI_AUTH_TOKEN. Uppercasing alone would give PGSI_SHUTDOWN-TIMEOUT, which is
// not a settable name and would make every multi-word flag unconfigurable by env
// while looking configured — TestEnvVarNames is what keeps that from coming back.
//
// It returns "" for NoEnv rows rather than a name the resolver would then ignore,
// because a caller that asks for the env name of a flag that has none should get
// the empty string as the answer rather than a plausible-looking string that is
// wrong.
func EnvVar(name string) string {
	if name == "" {
		return ""
	}
	for _, s := range settings {
		if s.Flag == name {
			if s.NoEnv {
				return ""
			}
			break
		}
	}
	return "PGSI_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// ConfigMapKey is the ConfigMap spelling of a flag name: camelCase, because that
// is what the chart writes.
//
// The two layers genuinely disagree and the disagreement is frozen, not an
// accident to normalise away. chart/templates/configmap.yaml renders
// `shutdownTimeout:` while the flag is --shutdown-timeout, and its comment says
// the file "is shipped now to freeze the contract before Phase 2 reads it" — so
// by R11.8's own words the ConfigMap's key set is the contract and this function
// has to honour it, not rename it. The chart's own values-default.yaml uses the
// same camelCase for the same two keys, so the pair is consistent on their side.
//
// The rule is: first dash-separated segment lower, each later segment's first
// letter upper-cased, dashes removed. --auth-metrics-token-file becomes
// authTokenFile. A name with no dashes is unchanged (qps, kubeconfig, listen).
func ConfigMapKey(name string) string {
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "-")
	out := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		out += strings.ToUpper(p[:1]) + p[1:]
	}
	return out
}

// Env is the environment the resolver reads.
//
// It is a function type and not os.Getenv so the tests can hand the resolver an
// environment without mutating the process's — t.Setenv is per-test and cannot
// express "these three are set and nothing else is", which is the shape every
// interesting case has. OSEnv is the production value.
type Env func(string) string

// OSEnv returns the process environment as an Env.
func OSEnv() Env { return os.Getenv }

// envMap builds an Env from a literal map, so a test reads as a table of set
// variables rather than a chain of t.Setenv calls.
func envMap(kv map[string]string) Env {
	return func(k string) string { return kv[k] }
}

// resolved is one table row after the chain has run: the value that won and the
// layer it came from.
type resolved struct {
	Flag  string
	Req   string
	Value string
	From  layer

	// secret is carried through from the setting so render can mask the value
	// without re-looking the row up.
	secret bool
}

// display is the value as the dump prints it: secret rows mask, every other row
// prints verbatim.
//
// A flag-given secret prints in the clear: whoever typed it already has it, and
// `--auth-token=x` dumping as `***` would hide the one copy of the value the
// operator is actually checking. Every layer below the command line — env,
// ConfigMap, the empty default — masks.
func (r resolved) display() string {
	if r.secret && r.From != layerFlag {
		return "***"
	}
	return r.Value
}

// source renders the "where did this come from" half of a dump row. The env and
// ConfigMap cases name the specific variable or key, because "env" alone does not
// tell you which of the two env spellings it read when a flag has both a token and
// a token-file.
func (r resolved) source() string {
	switch r.From {
	case layerEnv:
		return "env " + EnvVar(r.Flag)
	case layerConfigMap:
		return "configmap " + ConfigMapKey(r.Flag)
	default:
		return string(r.From)
	}
}

// resolveTable runs R11.8's chain over every row and returns the winners, in
// table order.
//
// It is a pure read of fs: it does not mutate the FlagSet. apply is the half that
// writes, and the split exists because `config dump` needs the table without
// wanting the side effect, and because a resolver you can call twice on the same
// FlagSet is testable in a way one that mutates is not.
//
// The flag-given set comes from fs.Visit, which the flag package documents as
// visiting "only the flags that were set" — that is the only way to distinguish
// "--listen=:8080 typed explicitly" from "--listen left at its :8080 default",
// and the distinction is the whole requirement: a chain that cannot tell those
// apart cannot let a flag beat an env var, because it does not know a flag was
// given. It therefore expects fs.Parse to have run; on an unparsed FlagSet Visit
// sees nothing and every row resolves to env/ConfigMap/default, which is the
// correct answer for "no arguments" and not an error.
func resolveTable(fs *flag.FlagSet, env Env, cm map[string]string) ([]resolved, error) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	out := make([]resolved, 0, len(settings))
	for _, s := range settings {
		f := fs.Lookup(s.Flag)
		if f == nil {
			// A table row naming a flag the FlagSet does not declare. This is a
			// bug in this file, not a runtime condition — the FlagSet is built by
			// newServeFlags in the same package — so it is an error rather than a
			// logged warning, and TestTableCoversEveryFlag makes it unproducible.
			return nil, fmt.Errorf("R11.8 table row %q names a flag the serve FlagSet does not declare", s.Flag)
		}
		r := resolved{Flag: s.Flag, Req: s.Req, secret: s.Secret}
		switch {
		case given[s.Flag]:
			// Top of the chain. The value is whatever Parse stored, which is the
			// command line's own spelling re-rendered by the flag's Stringer.
			r.Value, r.From = f.Value.String(), layerFlag
		default:
			// Ascending order of precedence, last writer wins: default, then
			// ConfigMap, then env. A row with both an env var and a ConfigMap key
			// resolves to the env var, which is R11.8's second position beating
			// its third.
			r.Value, r.From = f.DefValue, layerDefault
			if v, ok := lookupConfigMap(cm, s); ok {
				r.Value, r.From = v, layerConfigMap
			}
			if v, ok := lookupEnv(env, s); ok {
				r.Value, r.From = v, layerEnv
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// lookupEnv returns the env value for a row, and false if the row has no env name
// or the variable is unset/empty.
//
// Empty counts as unset. R11.8's chain has four levels and no "explicitly empty"
// marker, so PGSI_LISTEN="" cannot mean "bind to the empty address" — it means
// "not set here", which is also what the flag package does with an empty value for
// the string flags that default to "" (auth-token, prometheus-url).
func lookupEnv(env Env, s setting) (string, bool) {
	name := EnvVar(s.Flag)
	if name == "" || env == nil {
		return "", false
	}
	v := env(name)
	if v == "" {
		return "", false
	}
	return v, true
}

// lookupConfigMap returns the ConfigMap value for a row, and false if the key is
// absent or empty. Same empty-is-unset rule as lookupEnv — which matters here more
// than it does there, because the chart renders every value through `| quote`, so
// a key the chart renders as `listen: ""` arrives as the empty string and has to
// mean the same thing as the key being absent.
func lookupConfigMap(cm map[string]string, s setting) (string, bool) {
	if cm == nil {
		return "", false
	}
	v, ok := cm[ConfigMapKey(s.Flag)]
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// apply runs the chain and writes the result back into fs, then returns the table
// it resolved.
//
// The write is the mechanism the package doc describes: fs.Set on a flag the
// command line did not set replaces the value the flag's pointer holds, and since
// runServe reads those pointers, one call here routes every downstream `*listen`,
// `*shutdown` and kube.Config/discovery.Config field through the chain without any
// of them changing. A flag the command line *did* set is skipped, so Parse's value
// survives — that skip is the "CLI flags win" clause, and it is why apply must run
// after Parse and exactly once: fs.Set marks a flag as visited, so a second apply
// would see every flag as command-line-given and resolve the whole table to
// layerFlag. The skip of the untouched-default rows below is what keeps a repeated
// apply honest anyway.
//
// The error from fs.Set is a feature, not a hazard: fs.Set runs the flag's own
// parser, so PGSI_SHUTDOWN_TIMEOUT=banana fails with the same
// `invalid duration "banana"` the flag would produce, and a deployment with a
// malformed env value is a startup failure naming the variable — not a zero value
// that silently means "0s" and turns a typo into a pod that kills itself instantly.
func apply(fs *flag.FlagSet, env Env, cm map[string]string) ([]resolved, error) {
	rows, err := resolveTable(fs, env, cm)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.From == layerFlag {
			continue // the command line already won; do not touch it
		}
		if r.From == layerDefault && r.Value == "" {
			// Nothing above the default supplied a value and the default is
			// already in the pointer. Writing it back through fs.Set would be a
			// no-op for the value but would mark the flag visited, which is how a
			// second apply gets fooled; skipping keeps apply idempotent for the
			// rows it did not change.
			continue
		}
		if err := fs.Set(r.Flag, r.Value); err != nil {
			return nil, fmt.Errorf("R11.8: %s value %q is not valid for --%s (%s): %w",
				r.source(), r.Value, r.Flag, r.Req, err)
		}
	}
	return rows, nil
}

// loadConfigMap reads and parses the ConfigMap layer at path.
//
// A missing file is not an error and returns a nil map: off-cluster, the binary has
// no ConfigMap mounted and R11.8's chain has to run anyway, so the common case
// cannot be an error path. A file that exists but does not parse is an error,
// because at that point the operator mounted something and the honest answer is
// "what you mounted is not readable" rather than silently serving the defaults and
// letting them wonder why the ConfigMap had no effect.
//
// The parse is go.yaml.in/yaml/v3 — already in go.mod as k8s.io/apimachinery's YAML
// dependency, so reading the chart's format costs no new module (R13.10's
// no-`replace` rule and `make tidy-check` both stay satisfied). A hand-written
// scanner is the alternative and it is the one that gets quoting wrong: the chart
// renders `listen: ":8080"` quoted, and an unquote bug there is exactly the
// producer/consumer value disagreement R16.8 files.
//
// Only scalar values become keys. The chart's `config.extra` block renders nested
// maps, and a nested value has no flag to feed, so it is dropped rather than
// stringified into something the resolver would compare against a flag it was never
// meant for.
func loadConfigMap(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ConfigMap %s: %w", path, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse ConfigMap %s: %w", path, err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch tv := v.(type) {
		case string:
			out[k] = tv
		case bool:
			out[k] = fmt.Sprintf("%t", tv)
		case int:
			// yaml.v3 decodes a YAML integer that fits an int as Go `int`, not
			// `int64`; matching only int64 silently drops every numeric scalar
			// the chart writes (qps, burst) — which the test caught.
			out[k] = fmt.Sprintf("%d", tv)
		case int64:
			out[k] = fmt.Sprintf("%d", tv)
		case float64:
			out[k] = fmt.Sprintf("%v", tv)
		default:
			// map / slice / null: no flag takes a nested value, so it is not a
			// config key from this resolver's point of view.
		}
	}
	return out, nil
}

// configPath returns the ConfigMap path to read: PGSI_CONFIG if set, else
// ConfigFile. It is the one place the override is applied, so `serve` and
// `config dump` cannot disagree about which file they read.
func configPath(env Env) string {
	if v := env(ConfigFileEnv); v != "" {
		return v
	}
	return ConfigFile
}

// renderDump formats the resolved table as `pgsi config dump`'s output.
//
// The header states the chain rather than just labelling the table, because the
// output is what a user reads when they are trying to work out what the binary will
// do, and the rule that produced it belongs next to the answer.
func renderDump(rows []resolved) string {
	wFlag, wValue := len("FLAG"), len("VALUE")
	for _, r := range rows {
		if n := len("--" + r.Flag); n > wFlag {
			wFlag = n
		}
		if n := len(r.display()); n > wValue {
			wValue = n
		}
	}
	var b strings.Builder
	b.WriteString("R11.8 resolved config\n")
	b.WriteString("  precedence: CLI flags > PGSI_* env > mounted ConfigMap > built-in defaults\n")
	fmt.Fprintf(&b, "  ConfigMap: %s (override with %s)\n", ConfigFile, ConfigFileEnv)
	b.WriteString("\n")
	fmt.Fprintf(&b, "%-*s  %-*s  %s\n", wFlag, "FLAG", wValue, "VALUE", "SOURCE")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-*s  %-*s  %s\n", wFlag, "--"+r.Flag, wValue, r.display(), r.source())
	}
	return b.String()
}

// configFlagNames returns the table's flag names, sorted — the set the coverage
// test compares against the FlagSet's own enumeration.
func configFlagNames() []string {
	out := make([]string, 0, len(settings))
	for _, s := range settings {
		out = append(out, s.Flag)
	}
	sort.Strings(out)
	return out
}

// applyFlags loads the ConfigMap layer and runs the chain, for runServe.
//
// It is the one call runServe makes, and the reason the ConfigMap read lives here
// rather than inline in runServe is that `serve` and `config dump` must read the
// same file through the same code path — otherwise the dump can report a value
// the server never saw, which is the exact bug the dump exists to catch.
func applyFlags(fs *flag.FlagSet, env Env) ([]resolved, error) {
	cm, err := loadConfigMap(configPath(env))
	if err != nil {
		return nil, err
	}
	return apply(fs, env, cm)
}

// runConfig implements `pgsi config dump [serve flags…]` (R11.8).
//
// It parses the serve flag set with whatever flags were passed, resolves the
// chain, prints the table, and exits — it does not open a listener, build a kube
// client, or contact a cluster. That is the whole point of the subcommand: the
// question it answers ("what would this process actually use, and which layer
// said so") is answerable from the four inputs alone, and answering it without
// starting a server means you can ask it in a pod that is failing to start, or on
// a laptop with no kubeconfig at all.
func runConfig(args []string) int {
	if len(args) == 0 || args[0] != "dump" {
		fmt.Fprint(os.Stderr, "usage: pgsi config dump [serve flags...]\n")
		return 2
	}
	// The same pre-scan runServe does, so `config dump --log-field-cluster=x`
	// resolves that field the same way `serve` would.
	prefixed, rest, err := logx.FieldsFromArgs(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}
	sf := newServeFlags(prefixed)
	if err := sf.Parse(rest); err != nil {
		return 2
	}
	rows, err := applyFlags(sf.FlagSet, OSEnv())
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}
	fmt.Print(renderDump(rows))
	return 0
}
