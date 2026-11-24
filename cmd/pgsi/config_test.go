package main

// Tests for R11.8 — the config precedence chain and `pgsi config dump`.
//
// The suite is built around the two things the requirement actually promises.
// The first is an ordering, and an ordering is only testable if you can name
// which layer won — so every precedence case asserts a (value, layer) pair, not
// just a value. The second is `config dump`, and a dump is testable if you can
// prove it covers every flag rather than the ones someone remembered to render;
// that is what TestTableCoversEveryFlag and TestRenderDumpHasEveryRow pin, and
// they are the cmd/pgsi analogue of scripts/check-parity-matrix.sh's rule that
// every requirement id appears exactly once.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/logx"
)

// rowFor pulls one row out of a resolved table by flag name.
func rowFor(t *testing.T, rows []resolved, name string) resolved {
	t.Helper()
	for _, r := range rows {
		if r.Flag == name {
			return r
		}
	}
	t.Fatalf("no resolved row for --%s; the table and the FlagSet disagree", name)
	return resolved{}
}

// TestTableCoversEveryFlag is the guard against the table rotting away from the
// flag set it describes, in both directions: a flag declared but not in the table
// would silently resolve to its default forever (the env var and ConfigMap key
// for it would be ignored and `config dump` would not even print it), and a table
// row with no flag behind it is a lie about the CLI surface.
func TestTableCoversEveryFlag(t *testing.T) {
	declared := map[string]bool{}
	newServeFlags(logx.Fields{}).VisitAll(func(f *flag.Flag) { declared[f.Name] = true })

	for _, name := range configFlagNames() {
		if !declared[name] {
			t.Errorf("R11.8 table has a row for --%s but newServeFlags does not declare it", name)
		}
	}
	for name := range declared {
		found := false
		for _, s := range settings {
			if s.Flag == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--%s is declared on the serve FlagSet but has no R11.8 table row (it will ignore PGSI_%s)",
				strings.ToUpper(name), strings.ToUpper(name))
		}
	}
}

// TestEveryRowCitesARequirement keeps the Req column honest: a row that cites
// nothing is a flag nobody specified, which is the R14.7 failure class (something
// exists in code with no requirement behind it) in its flag form.
func TestEveryRowCitesARequirement(t *testing.T) {
	re := regexp.MustCompile(`^R\d+\.\d+$`)
	for _, s := range settings {
		if !re.MatchString(s.Req) {
			t.Errorf("setting %q cites %q, want an id shaped like R11.8", s.Flag, s.Req)
		}
	}
}

func TestNoDuplicateRows(t *testing.T) {
	seen := map[string]int{}
	for _, s := range settings {
		seen[s.Flag]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("setting %q appears %d times, want 1", name, n)
		}
	}
}

// TestEnvVarNames pins R11.8's `PGSI_*` spelling as a rule rather than a list.
func TestEnvVarNames(t *testing.T) {
	tests := map[string]string{
		"listen":               "PGSI_LISTEN",
		"shutdown-timeout":     "PGSI_SHUTDOWN_TIMEOUT",
		"auth-token-file":      "PGSI_AUTH_TOKEN_FILE",
		"qps":                  "PGSI_QPS",
		"insecure-skip-verify": "PGSI_INSECURE_SKIP_VERIFY",
		"no-such-flag":         "PGSI_NO_SUCH_FLAG", // not a row: the rule still applies
	}
	for flagName, want := range tests {
		if got := EnvVar(flagName); got != want {
			t.Errorf("EnvVar(%q) = %q, want %q", flagName, got, want)
		}
	}
	// The one row with no env spelling must answer "" and not a plausible name,
	// so a caller cannot accidentally read PGSI_LOG_FIELD as if it were set.
	if got := EnvVar("log-field"); got != "" {
		t.Errorf("EnvVar(\"log-field\") = %q, want \"\" (R13.7's flag is repeatable; a single env var cannot carry a list)", got)
	}
	// Every other row's name must be settable-by-construction: uppercase, no
	// dashes, PGSI_ prefix. A lowercase or dashed name would be a second spelling
	// the chart could disagree with.
	for _, s := range settings {
		if s.NoEnv {
			continue
		}
		got := EnvVar(s.Flag)
		if !strings.HasPrefix(got, "PGSI_") {
			t.Errorf("EnvVar(%q) = %q, want a PGSI_ prefix", s.Flag, got)
		}
		if strings.ToLower(got) != got && strings.ToUpper(got) != got {
			t.Errorf("EnvVar(%q) = %q, want all-uppercase", s.Flag, got)
		}
		if strings.Contains(got, "-") {
			t.Errorf("EnvVar(%q) = %q, want dashes converted to underscores", s.Flag, got)
		}
	}
}

// TestConfigMapKeys pins the kebab→camel bridge. The two keys the chart actually
// renders are asserted by name because the chart's configmap.yaml comment calls
// its key set a frozen contract; the rest are asserted to prove the rule, not
// just the two examples.
func TestConfigMapKeys(t *testing.T) {
	tests := map[string]string{
		"listen":                  "listen",
		"shutdown-timeout":        "shutdownTimeout", // the chart's own spelling
		"serve-ui":                "serveUi",
		"auth-token-file":         "authTokenFile",
		"auth-metrics-token-file": "authMetricsTokenFile",
		"cors-allow-origin":       "corsAllowOrigin",
		"insecure-skip-verify":    "insecureSkipVerify",
		"request-timeout":         "requestTimeout",
		"exclude-namespaces":      "excludeNamespaces",
		"credential-username":     "credentialUsername",
		"qps":                     "qps",
		"kubeconfig":              "kubeconfig",
		"api-server-url":          "apiServerUrl",
		"prometheus-url":          "prometheusUrl",
		"discover":                "discover",
		"auth":                    "auth",
		"auth-metrics":            "authMetrics",
		"auth-token":              "authToken",
		"cors-allow-credentials":  "corsAllowCredentials",
		"cors-max-age":            "corsMaxAge",
		"log-level":               "logLevel",
		"log-field":               "logField",
		"namespaces":              "namespaces",
		"resync":                  "resync",
		"stale-grace":             "staleGrace",
		"credential-secret":       "credentialSecret",
		"credential-password":     "credentialPassword",
		"credential-dbname":       "credentialDbname",
		"kube-context":            "kubeContext",
		"bearer-token":            "bearerToken",
		"client-cert":             "clientCert",
		"client-key":              "clientKey",
		"use-proxy":               "useProxy",
		"burst":                   "burst",
		"":                        "",

		// R2.9's three. Every row in settings has an entry in this map except
		// auth-metrics-token, so a new flag with no entry here is the gap the test
		// exists to close. They sit after a blank line rather than inside the block
		// above because max-concurrent-http-scrapes is a longer key than any of
		// them, and gofmt would otherwise re-pad all 36 existing lines to match it.
		"max-concurrent-sql":          "maxConcurrentSql",
		"max-concurrent-http-scrapes": "maxConcurrentHttpScrapes",
		"scheduler-queue-depth":       "schedulerQueueDepth",
	}
	for flagName, want := range tests {
		if got := ConfigMapKey(flagName); got != want {
			t.Errorf("ConfigMapKey(%q) = %q, want %q", flagName, got, want)
		}
	}
	// The table and the rule must agree for every row: this is the assertion that
	// catches a row whose name the rule would spell differently from what the
	// chart renders (a double dash, a stray capital).
	for _, s := range settings {
		if strings.ToUpper(s.Flag) != strings.ToUpper(s.Flag) {
			continue
		}
		if strings.Contains(s.Flag, "--") {
			t.Errorf("flag name %q contains a double dash; ConfigMapKey cannot derive a stable camelCase from it", s.Flag)
		}
		if s.Flag != strings.ToLower(s.Flag) {
			t.Errorf("flag name %q is not all-lowercase; the flag package convention and ConfigMapKey both assume it", s.Flag)
		}
	}
}

// TestPrecedenceChain is the requirement itself, as a table. Each case supplies
// some subset of the four levels and asserts both halves of the answer: the value
// AND the layer it came from. Asserting only the value would pass a resolver that
// picked the right string for the wrong reason, and `config dump`'s whole purpose
// is to report the reason.
func TestPrecedenceChain(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		cm   map[string]string
		flag string
		want string
		from layer
	}{
		{
			name: "all four levels present: the flag wins",
			args: []string{"--listen=:1"},
			env:  map[string]string{"PGSI_LISTEN": ":2"},
			cm:   map[string]string{"listen": ":3"},
			flag: "listen", want: ":1", from: layerFlag,
		},
		{
			name: "no flag: env beats ConfigMap",
			env:  map[string]string{"PGSI_LISTEN": ":2"},
			cm:   map[string]string{"listen": ":3"},
			flag: "listen", want: ":2", from: layerEnv,
		},
		{
			name: "no flag, no env: ConfigMap beats default",
			cm:   map[string]string{"listen": ":3"},
			flag: "listen", want: ":3", from: layerConfigMap,
		},
		{
			name: "nothing set: the declared default",
			flag: "listen", want: ":8080", from: layerDefault,
		},
		{
			// The empty-string rule. R11.8 has no "explicitly empty" level, so an
			// empty env value means "not set here" and the next level down still
			// answers — which is also what the chart produces when a value is
			// templated from an unset .Values key.
			name: "empty env var is unset, ConfigMap still answers",
			env:  map[string]string{"PGSI_LISTEN": ""},
			cm:   map[string]string{"listen": ":3"},
			flag: "listen", want: ":3", from: layerConfigMap,
		},
		{
			name: "empty ConfigMap key is unset, default answers",
			cm:   map[string]string{"listen": ""},
			flag: "listen", want: ":8080", from: layerDefault,
		},
		{
			// A second flag type, so the chain is proven for the flag package's
			// parser and not just for pass-through strings.
			name: "a duration flag beats its env var",
			args: []string{"--shutdown-timeout=3s"},
			env:  map[string]string{"PGSI_SHUTDOWN_TIMEOUT": "45s"},
			flag: "shutdown-timeout", want: "3s", from: layerFlag,
		},
		{
			name: "a duration flag takes its env var",
			env:  map[string]string{"PGSI_SHUTDOWN_TIMEOUT": "45s"},
			flag: "shutdown-timeout", want: "45s", from: layerEnv,
		},
		{
			name: "a bool flag takes its env var",
			env:  map[string]string{"PGSI_DISCOVER": "false"},
			flag: "discover", want: "false", from: layerEnv,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sf := newServeFlags(logx.Fields{})
			if err := sf.Parse(tt.args); err != nil {
				t.Fatalf("Parse(%v): %v", tt.args, err)
			}
			rows, err := resolveTable(sf.FlagSet, envMap(tt.env), tt.cm)
			if err != nil {
				t.Fatalf("resolveTable: %v", err)
			}
			got := rowFor(t, rows, tt.flag)
			if got.Value != tt.want || got.From != tt.from {
				t.Errorf("--%s resolved to (%q, %s), want (%q, %s)",
					tt.flag, got.Value, got.From, tt.want, tt.from)
			}
		})
	}
}

// TestApplyWritesThroughToThePointers is the test that proves the chain reaches
// the code. resolveTable can be right about every row and still change nothing if
// the winner is never written back, and runServe reads its whole api.Config,
// kube.Config and discovery.Config off those pointers — so this asserts, through
// the same field accesses runServe uses, that an env-supplied value lands in each
// typed pointer.
func TestApplyWritesThroughToThePointers(t *testing.T) {
	sf := newServeFlags(logx.Fields{})
	if err := sf.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	env := envMap(map[string]string{
		"PGSI_LISTEN":            ":9999",
		"PGSI_SHUTDOWN_TIMEOUT":  "45s",
		"PGSI_AUTH":              "none",
		"PGSI_AUTH_METRICS":      "token",
		"PGSI_AUTH_TOKEN":        "env-token",
		"PGSI_CORS_ALLOW_ORIGIN": "https://ui.example",
		"PGSI_PROMETHEUS_URL":    "http://prom:9090",
		"PGSI_LOG_LEVEL":         "debug",
		"PGSI_DISCOVER":          "false",
		"PGSI_NAMESPACES":        "alpha,beta",
		"PGSI_RESYNC":            "5m",
		"PGSI_QPS":               "12.5",
		"PGSI_BURST":             "80",
		"PGSI_REQUEST_TIMEOUT":   "30s",
	})
	if _, err := applyFlags(sf.FlagSet, env); err != nil {
		t.Fatalf("applyFlags: %v", err)
	}

	// Exactly the reads runServe performs, field by field.
	got := []struct {
		name string
		s    string
		want string
	}{
		{"listen", *sf.listen, ":9999"},
		{"shutdown-timeout", sf.shutdown.String(), "45s"},
		{"auth", *sf.authMode, "none"},
		{"auth-metrics", *sf.authMetrics, "token"},
		{"auth-token", *sf.authToken, "env-token"},
		{"cors-allow-origin", *sf.corsOrigin, "https://ui.example"},
		{"prometheus-url", *sf.prometheusURL, "http://prom:9090"},
		{"log-level", *sf.logLevel, "debug"},
		{"discover", boolString(*sf.discover), "false"},
		{"namespaces", *sf.namespaces, "alpha,beta"},
		// The pointer's String() is Go's canonical duration form ("5m0s"), not
		// the env spelling ("5m"): fs.Set parsed the value and the flag's
		// Stringer re-renders it. The table row keeps the raw "5m" it read, which
		// is the difference between what a layer said and what the flag holds.
		{"resync", sf.resync.String(), "5m0s"},
		{"qps", floatString(*sf.qps), "12.5"},
		{"burst", intString(*sf.burst), "80"},
		{"request-timeout", sf.reqTimeout.String(), "30s"},
	}
	for _, g := range got {
		if g.s != g.want {
			t.Errorf("--%s = %q after apply, want %q (the resolved value did not reach the pointer runServe reads)",
				g.name, g.s, g.want)
		}
	}
}

// TestApplyLeavesFlagGivenPointersAlone is the other half of the write-through: a
// flag the command line set must survive apply untouched, because that survival
// IS the "CLI flags > everything else" clause once the value is in a pointer.
func TestApplyLeavesFlagGivenPointersAlone(t *testing.T) {
	sf := newServeFlags(logx.Fields{})
	if err := sf.Parse([]string{"--listen=:1234"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := applyFlags(sf.FlagSet, envMap(map[string]string{"PGSI_LISTEN": ":9999"})); err != nil {
		t.Fatalf("applyFlags: %v", err)
	}
	if *sf.listen != ":1234" {
		t.Errorf("--listen = %q after apply, want :1234 (the env value overwrote a flag the command line set)", *sf.listen)
	}
}

// TestApplyRejectsMalformedValue: fs.Set runs the flag's own parser, so a bad
// PGSI_* value is a startup error naming the variable rather than a silent zero.
// The test asserts the variable name is in the message, because "invalid
// duration" alone does not tell an operator which of thirty env vars to fix.
func TestApplyRejectsMalformedValue(t *testing.T) {
	sf := newServeFlags(logx.Fields{})
	if err := sf.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err := applyFlags(sf.FlagSet, envMap(map[string]string{"PGSI_SHUTDOWN_TIMEOUT": "banana"}))
	if err == nil {
		t.Fatal("applyFlags accepted PGSI_SHUTDOWN_TIMEOUT=banana, want a parse error")
	}
	for _, want := range []string{"PGSI_SHUTDOWN_TIMEOUT", "banana", "shutdown-timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// TestApplyIsIdempotentOnValues documents the one constraint apply puts on its
// caller: it must run once. fs.Set marks a flag as visited, so a second apply
// sees every previously-written flag as command-line-given and re-labels it
// layerFlag. The values are stable across the repeat — which is what matters for
// `serve` — and the attribution is not, which is why runServe calls it exactly
// once and why `config dump` resolves rather than applies twice.
func TestApplyIsIdempotentOnValues(t *testing.T) {
	sf := newServeFlags(logx.Fields{})
	if err := sf.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	env := envMap(map[string]string{"PGSI_LISTEN": ":9999", "PGSI_LOG_LEVEL": "warn"})
	first, err := apply(sf.FlagSet, env, nil)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second, err := apply(sf.FlagSet, env, nil)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("second apply returned %d rows, want %d", len(second), len(first))
	}
	for i := range first {
		if first[i].Value != second[i].Value {
			t.Errorf("--%s value changed on the second apply: %q then %q", first[i].Flag, first[i].Value, second[i].Value)
		}
	}
	if rowFor(t, first, "listen").From != layerEnv {
		t.Errorf("first apply attributed --listen to %s, want env", rowFor(t, first, "listen").From)
	}
	if rowFor(t, second, "listen").From != layerFlag {
		t.Errorf("second apply attributed --listen to %s, want flag (fs.Set marks the flag visited; see apply's doc)",
			rowFor(t, second, "listen").From)
	}
}

// TestSecretsMaskedInDump: R11.8's dump is printed for debugging, usually
// somewhere other than the terminal that produced it, so a token in the clear
// turns a diagnostic into a credential leak.
func TestSecretsMaskedInDump(t *testing.T) {
	sf := newServeFlags(logx.Fields{})
	if err := sf.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rows, err := applyFlags(sf.FlagSet, envMap(map[string]string{
		"PGSI_AUTH_TOKEN":          "s3cr3t-from-env",
		"PGSI_BEARER_TOKEN":        "kube-token-value",
		"PGSI_CREDENTIAL_PASSWORD": "app-password",
	}))
	if err != nil {
		t.Fatalf("applyFlags: %v", err)
	}
	out := renderDump(rows)
	for _, leak := range []string{"s3cr3t-from-env", "kube-token-value", "app-password"} {
		if strings.Contains(out, leak) {
			t.Errorf("dump leaked %q; secret rows must print ***", leak)
		}
	}
	// The source column survives masking, which is the point: "which layer did
	// the token come from" is the question being asked.
	for _, want := range []string{"env PGSI_AUTH_TOKEN", "env PGSI_BEARER_TOKEN", "env PGSI_CREDENTIAL_PASSWORD"} {
		if !strings.Contains(out, want) {
			t.Errorf("dump is missing source %q", want)
		}
	}

	// A flag-given secret prints in the clear: the operator typed it, so hiding it
	// would hide the one copy they are checking.
	sf2 := newServeFlags(logx.Fields{})
	if err := sf2.Parse([]string{"--auth-token=cli-token"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rows2, err := resolveTable(sf2.FlagSet, envMap(nil), nil)
	if err != nil {
		t.Fatalf("resolveTable: %v", err)
	}
	if got := rowFor(t, rows2, "auth-token").display(); got != "cli-token" {
		t.Errorf("a flag-given token displayed %q, want cli-token", got)
	}
	// A non-secret row's value is never masked.
	if got := rowFor(t, rows2, "listen").display(); got != ":8080" {
		t.Errorf("--listen displayed %q, want :8080", got)
	}
}

// TestRenderDumpHasEveryRow stops the renderer from quietly dropping a row: the
// line count is pinned to the table's length, so a flag that exists but does not
// appear in the dump fails here rather than in someone's debugging session.
func TestRenderDumpHasEveryRow(t *testing.T) {
	sf := newServeFlags(logx.Fields{})
	if err := sf.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rows, err := resolveTable(sf.FlagSet, envMap(nil), nil)
	if err != nil {
		t.Fatalf("resolveTable: %v", err)
	}
	out := renderDump(rows)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	want := 5 + len(settings) // 3 header lines + blank + column row, then one per setting
	if len(lines) != want {
		t.Errorf("dump has %d lines, want %d (3 header + blank + columns + %d rows)", len(lines), want, len(settings))
	}
	if !strings.Contains(lines[1], "CLI flags > PGSI_* env > mounted ConfigMap > built-in defaults") {
		t.Errorf("dump header does not state the chain: %q", lines[1])
	}
	for _, s := range settings {
		if !strings.Contains(out, "--"+s.Flag+" ") && !strings.Contains(out, "--"+s.Flag+"\n") {
			// Column padding means the flag is followed by spaces, never by nothing.
			if !strings.Contains(out, "--"+s.Flag) {
				t.Errorf("dump has no line for --%s", s.Flag)
			}
		}
	}
}

// TestLoadConfigMap covers the ConfigMap layer's parser, including the one case a
// hand-written scanner gets wrong: the chart renders every value through `|
// quote`, so `listen: ":8080"` has to unquote to :8080 and not to ":8080".
func TestLoadConfigMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "" +
		"listen: \":1234\"\n" +
		"shutdownTimeout: 45s\n" +
		"discover: false\n" +
		"qps: 12\n" +
		"extra:\n" +
		"  nested: value\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cm, err := loadConfigMap(path)
	if err != nil {
		t.Fatalf("loadConfigMap: %v", err)
	}
	if cm["listen"] != ":1234" {
		t.Errorf("cm[listen] = %q, want :1234 (the quoted scalar must unquote)", cm["listen"])
	}
	if cm["shutdownTimeout"] != "45s" {
		t.Errorf("cm[shutdownTimeout] = %q, want 45s", cm["shutdownTimeout"])
	}
	if cm["discover"] != "false" {
		t.Errorf("cm[discover] = %q, want false", cm["discover"])
	}
	if cm["qps"] != "12" {
		t.Errorf("cm[qps] = %q, want 12", cm["qps"])
	}
	if _, ok := cm["extra"]; ok {
		t.Error("cm[extra] present; nested values are not config keys and must be dropped")
	}

	// A missing ConfigMap is the off-cluster normal case, not an error.
	empty, err := loadConfigMap(filepath.Join(dir, "absent.yaml"))
	if err != nil {
		t.Errorf("loadConfigMap on a missing file returned %v, want no error", err)
	}
	if empty != nil {
		t.Errorf("loadConfigMap on a missing file returned %v, want nil", empty)
	}

	// A file that exists but does not parse IS an error: something was mounted,
	// and silently serving defaults is how "my ConfigMap does nothing" happens.
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("listen: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := loadConfigMap(bad); err == nil {
		t.Error("loadConfigMap accepted unparseable YAML, want an error")
	}
}

// TestConfigPathOverride pins the PGSI_CONFIG layer and, with it, the path the
// chart mounts to.
func TestConfigPathOverride(t *testing.T) {
	if got := configPath(envMap(nil)); got != ConfigFile {
		t.Errorf("configPath with no override = %q, want %q", got, ConfigFile)
	}
	if got := configPath(envMap(map[string]string{"PGSI_CONFIG": "/tmp/other.yaml"})); got != "/tmp/other.yaml" {
		t.Errorf("configPath = %q, want /tmp/other.yaml (PGSI_CONFIG must win)", got)
	}
	if ConfigFile != "/etc/pgsi/config.yaml" {
		t.Errorf("ConfigFile = %q, want /etc/pgsi/config.yaml", ConfigFile)
	}
}

// TestConfigFilePathMatchesChart is the cross-layer half of the same assertion:
// the path exists in A4's chart and in this binary, and the only thing that stops
// them drifting is one of them computing it. If the chart is present in the tree,
// compose the path from its mountPath and its ConfigMap key and require it to
// equal ConfigFile.
func TestConfigFilePathMatchesChart(t *testing.T) {
	root := filepath.Join("..", "..")
	tmpl := filepath.Join(root, "chart", "templates", "configmap.yaml")
	dep := filepath.Join(root, "chart", "templates", "deployment.yaml")
	if _, err := os.Stat(tmpl); err != nil {
		t.Skip("chart/templates/configmap.yaml not present")
	}
	key := chartKey(t, tmpl)
	mount := chartMountPath(t, dep)
	if key == "" || mount == "" {
		t.Skip("chart does not expose a config.yaml key and an /etc/pgsi mountPath in the shapes this test reads")
	}
	if got := mount + "/" + key; got != ConfigFile {
		t.Errorf("chart composes %s/%s = %q, but cmd/pgsi ConfigFile is %q", mount, key, got, ConfigFile)
	}
}

// chartKey returns the ConfigMap's data key ("config.yaml"), "" if unreadable.
func chartKey(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "config.yaml:") {
			return "config.yaml"
		}
	}
	return ""
}

// chartMountPath returns the first mountPath ending in /etc/pgsi, "" if absent.
func chartMountPath(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "mountPath:") {
			continue
		}
		v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "mountPath:")), `"'`)
		if strings.HasSuffix(v, "/etc/pgsi") {
			return v
		}
	}
	return ""
}

// Small formatters so the pointer assertions can compare like with like: the
// flag package's own String() is what a resolved value is, so the test compares
// the same rendering rather than re-deriving it.
func boolString(b bool) string     { return fmt.Sprintf("%t", b) }
func intString(i int) string       { return fmt.Sprintf("%d", i) }
func floatString(f float64) string { return fmt.Sprintf("%v", f) }
