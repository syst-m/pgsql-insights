package main

// Tests for R3.15's Go half (`cmd/pgsi/enable.go`) and the R12.8 gate it reads.
//
// Everything here is a pure-function test: the four pieces of enable.go that carry
// a decision (parseAllowActions, splitEnableArgs, parseClusterRef, withLibrary)
// plus the two that render a promise (planEnable, buildEnablePatch) and the one
// loop (pollUntil). runEnable itself is not tested — it needs a kube client and a
// live CR, and the repo's rule for that boundary is the one main_test.go already
// draws: the tests cover the functions, not the wiring, and the wiring is covered
// by TestTableCoversEveryFlag on the flag side.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/frankellis/pgsql-insights/internal/api"
)

// TestParseAllowActions pins R12.8's value grammar (`requirements.md:475`), which
// is the half of the requirement a Go bool cannot express: the value names each
// mutation individually, so "which actions are allowed" is a set question and not
// a yes/no one.
//
// The two legacy spellings are the interesting rows. R4.1's row still says
// `--allow-actions=false` and R12.8 generalised it, so a deployment that kept the
// old spelling has to keep working — api.Config's docblock
// (`internal/api/server.go:104`) settles that as "the string `false` is simply not
// a member of any action set, so a config parsed from the old spelling yields an
// empty list, which is the off state", and the same sentence's "a list represents
// both" is what makes "true" the full set. A test that only covered the list
// spelling would leave both halves of that sentence unenforced.
func TestParseAllowActions(t *testing.T) {
	t.Parallel()

	all := append([]string(nil), knownActions...)

	tests := []struct {
		name string
		in   string
		want []string
		err  bool
	}{
		{"empty is off", "", nil, false},
		{"false is off (R4.1's spelling)", "false", nil, false},
		{"none is off", "none", nil, false},
		{"blank is off", "   ", nil, false},
		{"true is every known action", "true", all, false},
		{"one action", "terminate", []string{"terminate"}, false},
		{"the R3.15 action alone", "enable-extension", []string{"enable-extension"}, false},
		{"both", "terminate,enable-extension", []string{"terminate", "enable-extension"}, false},
		{"whitespace is trimmed", " terminate , enable-extension ", []string{"terminate", "enable-extension"}, false},
		{"empty members are dropped", "terminate,,enable-extension,", []string{"terminate", "enable-extension"}, false},
		{"duplicates collapse", "terminate,terminate", []string{"terminate"}, false},
		// An unknown name is an error, not a silent drop: a deployment that
		// misspells the flag gets told what the flag accepts, rather than running
		// with an empty set and looking configured.
		{"unknown name errors", "terminate-session", nil, true},
		{"one bad member poisons the value", "terminate,terminate-session", nil, true},
		{"TRUE is not true", "TRUE", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseAllowActions(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("parseAllowActions(%q) = %v, want an error", tc.in, got)
				}
				// The message has to name the valid set — that is the difference
				// between a diagnostic and a refusal.
				for _, known := range knownActions {
					if !strings.Contains(err.Error(), known) {
						t.Errorf("error %q does not name the valid action %q", err, known)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAllowActions(%q): %v", tc.in, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("parseAllowActions(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestKnownActionsCoversBothConsumers is the cross-package half of the gate.
//
// Both names are A2's now (`internal/api/actions.go:47` and `:62`) and
// api.KnownActions (`:69`) is the set this file aliases, so the two lists this test
// compared collapsed into one declaration — which is the fix the test was written
// to make unnecessary. The API's actionFor (`internal/api/active_queries.go:158`)
// answers for the first name, runEnable's gate for the second, and both read one
// []string off api.Config. If a name were in one list and not the other, the flag
// would accept an action no handler implements, or refuse one that does — and no
// test in either package would notice, because each only checks its own name.
func TestKnownActionsCoversBothConsumers(t *testing.T) {
	t.Parallel()

	if !slices.Contains(knownActions, api.ActionNameTerminate) {
		t.Errorf("knownActions %v does not contain api.ActionNameTerminate (%q); the flag would refuse an action the API implements",
			knownActions, api.ActionNameTerminate)
	}
	if !slices.Contains(knownActions, api.ActionNameEnableExtension) {
		t.Errorf("knownActions %v does not contain api.ActionNameEnableExtension (%q)", knownActions, api.ActionNameEnableExtension)
	}

	// actionAllowed is the membership test; it has to agree with the set for every
	// member and for a name that is not one.
	for _, name := range knownActions {
		if !actionAllowed(knownActions, name) {
			t.Errorf("actionAllowed(knownActions, %q) = false, want true", name)
		}
	}
	if actionAllowed(knownActions, "vacuum") {
		t.Error(`actionAllowed(knownActions, "vacuum") = true, want false`)
	}
	if actionAllowed(nil, api.ActionNameTerminate) {
		t.Error("actionAllowed(nil, ...) = true, want false: the empty set is R12.8's off state")
	}
}

// TestSplitEnableArgs covers R3.15's argv shape (`requirements.md:322`), which is
// the part of the requirement the flag package cannot parse on its own.
func TestSplitEnableArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		want    string
		wantRst []string
		err     bool
	}{
		// The spelling the requirement writes, verbatim.
		{"documented", []string{"pg-stat-statements", "--cluster", "postgres-data/demo"},
			"pg-stat-statements", []string{"--cluster", "postgres-data/demo"}, false},
		{"same with an equals form", []string{"pg-stat-statements", "--cluster=postgres-data/demo"},
			"pg-stat-statements", []string{"--cluster=postgres-data/demo"}, false},
		{"extension alone", []string{"pg-stat-statements"}, "pg-stat-statements", nil, false},
		// --cluster's value is a non-flag too, so it stays with the flags rather
		// than being mistaken for a second extension.
		{"flags after the extension are not positionals",
			[]string{"pg-stat-statements", "--cluster", "ns/name", "--dry-run"},
			"pg-stat-statements", []string{"--cluster", "ns/name", "--dry-run"}, false},
		{"nothing at all", nil, "", nil, true},
		{"flags only", []string{"--dry-run"}, "", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, rest, err := splitEnableArgs(tc.args)
			if tc.err {
				if err == nil {
					t.Fatalf("splitEnableArgs(%v) = %q, want an error", tc.args, got)
				}
				if !strings.Contains(err.Error(), "pgsi enable <extension> --cluster") {
					t.Errorf("error %q does not carry the usage string", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitEnableArgs(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("splitEnableArgs(%v) extension = %q, want %q", tc.args, got, tc.want)
			}
			if !slices.Equal(rest, tc.wantRst) {
				t.Errorf("splitEnableArgs(%v) rest = %v, want %v", tc.args, rest, tc.wantRst)
			}
		})
	}
}

// TestParseClusterRef enforces the qualified form. The rule and the reason are in
// the function's own docblock; the test exists so the rule cannot be relaxed by
// accident in whichever commit needs a bare name to work.
func TestParseClusterRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in          string
		ns, name    string
		err         bool
		errMentions string
	}{
		{in: "postgres-data/demo", ns: "postgres-data", name: "demo"},
		{in: " postgres-data/demo ", ns: "postgres-data", name: "demo"},
		{in: "", err: true, errMentions: "required"},
		{in: "demo", err: true, errMentions: "namespace/name"},
		{in: "postgres-data/", err: true, errMentions: "namespace/name"},
		{in: "/demo", err: true, errMentions: "namespace/name"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			ns, name, err := parseClusterRef(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("parseClusterRef(%q) = %q/%q, want an error", tc.in, ns, name)
				}
				if !strings.Contains(err.Error(), tc.errMentions) {
					t.Errorf("error %q does not mention %q", err, tc.errMentions)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseClusterRef(%q): %v", tc.in, err)
			}
			if ns != tc.ns || name != tc.name {
				t.Errorf("parseClusterRef(%q) = %q/%q, want %q/%q", tc.in, ns, name, tc.ns, tc.name)
			}
		})
	}
}

// TestWithLibraryIsIdempotent is the reversibility half of R3.15 seen from the
// write side: a patch that appends unconditionally produces
// `pg_stat_statements,pg_stat_statements` on the second run, which Postgres
// accepts and which makes the diff the offer printed a lie.
func TestWithLibraryIsIdempotent(t *testing.T) {
	t.Parallel()

	tests := []struct{ current, lib, want string }{
		{"", "pg_stat_statements", "pg_stat_statements"},
		{"   ", "pg_stat_statements", "pg_stat_statements"},
		{"pg_stat_statements", "pg_stat_statements", "pg_stat_statements"},
		{"pg_stat_statements ", "pg_stat_statements", "pg_stat_statements "},
		{"timescaledb", "pg_stat_statements", "timescaledb,pg_stat_statements"},
		{"timescaledb,pg_stat_statements", "pg_stat_statements", "timescaledb,pg_stat_statements"},
		{"timescaledb, pg_stat_statements", "pg_stat_statements", "timescaledb, pg_stat_statements"},
		// A substring is not a member: the check is per element, not per string,
		// so a cluster preloading pg_stat_statements_stat does not look like one
		// that already has pg_stat_statements.
		{"pg_stat_statements_stat", "pg_stat_statements", "pg_stat_statements_stat,pg_stat_statements"},
	}
	for _, tc := range tests {
		t.Run(tc.current+"+"+tc.lib, func(t *testing.T) {
			t.Parallel()
			if got := withLibrary(tc.current, tc.lib); got != tc.want {
				t.Errorf("withLibrary(%q, %q) = %q, want %q", tc.current, tc.lib, got, tc.want)
			}
		})
	}
}

// testClusterCR is the CR fixture the plan, patch and progress tests share.
//
// It exists in two encodings on purpose. The Go-built one holds the counts as
// int64; the JSON-decoded one holds the same numbers as float64, which is what
// k8s.io's unstructured decoder actually produces from an API response. A reader
// that handles only one of the two reports the other as "no instances", and in a
// rolling-restart progress line that is indistinguishable from a cluster with none.
func testClusterCR(t *testing.T, fromJSON bool) *unstructured.Unstructured {
	t.Helper()

	obj := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"namespace": "postgres-data",
			"name":      "demo",
		},
		"spec": map[string]any{
			"instances": int64(3),
			"parameters": map[string]any{
				"shared_preload_libraries": "",
				"work_mem":                 "64MB",
				"max_connections":          "200",
			},
		},
		"status": map[string]any{
			"clusterPhase":   "Cluster in healthy state",
			"readyInstances": int64(3),
			"instances": map[string]any{
				"demo-1": "node-a",
				"demo-2": "node-b",
				"demo-3": "node-c",
			},
			"pods": map[string]any{"primary": "demo-1"},
		},
	}

	if !fromJSON {
		return &unstructured.Unstructured{Object: obj}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal CR: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal CR: %v", err)
	}
	return &unstructured.Unstructured{Object: decoded}
}

// TestNestedCountReadsBothIntegerEncodings is the test for the claim in the
// comment above: the same JSON number arrives as int64 from Go and float64 from a
// decode, and both have to read.
func TestNestedCountReadsBothIntegerEncodings(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		fromGo bool
	}{
		{"Go-built (int64)", true},
		{"JSON-decoded (float64)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cr := testClusterCR(t, tc.fromGo)
			if got, ok := nestedCount(cr.Object, "spec", "instances"); !ok || got != 3 {
				t.Errorf(`nestedCount(spec.instances) = %d,%v, want 3,true`, got, ok)
			}
			if got, ok := nestedCount(cr.Object, "status", "readyInstances"); !ok || got != 3 {
				t.Errorf(`nestedCount(status.readyInstances) = %d,%v, want 3,true`, got, ok)
			}
			// A string is not a count, and a missing path is not a zero.
			if _, ok := nestedCount(cr.Object, "status", "clusterPhase"); ok {
				t.Error("nestedCount on a string path returned ok=true, want false")
			}
			if _, ok := nestedCount(cr.Object, "status", "nope"); ok {
				t.Error("nestedCount on a missing path returned ok=true, want false")
			}
		})
	}
}

// TestBuildEnablePatchKeepsEveryOtherParameter is the test for the trap in a merge
// patch: {"spec":{"parameters":{"shared_preload_libraries":…}}} replaces the whole
// parameters map, so a body built from the one key being changed silently deletes
// work_mem and max_connections from the cluster.
func TestBuildEnablePatchKeepsEveryOtherParameter(t *testing.T) {
	t.Parallel()

	for _, fromJSON := range []bool{true, false} {
		name := "Go-built CR"
		if !fromJSON {
			name = "JSON-decoded CR"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cr := testClusterCR(t, fromJSON)

			patch, err := buildEnablePatch(cr, "pg_stat_statements")
			if err != nil {
				t.Fatalf("buildEnablePatch: %v", err)
			}
			var body struct {
				Spec struct {
					Parameters map[string]string `json:"parameters"`
				} `json:"spec"`
			}
			if err := json.Unmarshal(patch, &body); err != nil {
				t.Fatalf("patch is not valid JSON: %v\n%s", err, patch)
			}
			if got := body.Spec.Parameters["shared_preload_libraries"]; got != "pg_stat_statements" {
				t.Errorf("patch sets shared_preload_libraries=%q, want pg_stat_statements", got)
			}
			// The two survivors are the point of the test.
			for key, want := range map[string]string{"work_mem": "64MB", "max_connections": "200"} {
				if got := body.Spec.Parameters[key]; got != want {
					t.Errorf("patch drops spec.parameters.%s: got %q, want %q (a merge patch "+
						"replaces the object it names, so the body must carry every parameter)",
						key, got, want)
				}
			}
			if len(body.Spec.Parameters) != 3 {
				t.Errorf("patch carries %d parameters, want 3: %v", len(body.Spec.Parameters), body.Spec.Parameters)
			}
		})
	}
}

// TestBuildEnablePatchOnClusterWithNoParameters covers the branch where
// spec.parameters is absent entirely — a CNPG cluster with no custom GUCs, which is
// the common case and the one where the extension is guaranteed missing.
func TestBuildEnablePatchOnClusterWithNoParameters(t *testing.T) {
	t.Parallel()

	cr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"namespace": "pg", "name": "bare"},
		"spec":       map[string]any{"instances": int64(1)},
	}}
	patch, err := buildEnablePatch(cr, "pg_stat_statements")
	if err != nil {
		t.Fatalf("buildEnablePatch: %v", err)
	}
	if !strings.Contains(string(patch), `"shared_preload_libraries":"pg_stat_statements"`) {
		t.Errorf("patch %s does not set shared_preload_libraries", patch)
	}
}

// TestPlanEnableStatesTheRestartCost is the test for R3.15's sentence "The restart
// cost is stated in the offer, not discovered afterwards." The only way to enforce
// a promise is to require the field to be populated in both branches — an empty
// cost on the branch that rolls the cluster is the failure the sentence exists for.
func TestPlanEnableStatesTheRestartCost(t *testing.T) {
	t.Parallel()

	t.Run("needs the patch", func(t *testing.T) {
		t.Parallel()
		cr := testClusterCR(t, true)
		plan, err := planEnable(cr, "pg_stat_statements", false)
		if err != nil {
			t.Fatalf("planEnable: %v", err)
		}
		if plan.Namespace != "postgres-data" || plan.Cluster != "demo" {
			t.Errorf("plan identifies %q/%q, want postgres-data/demo", plan.Namespace, plan.Cluster)
		}
		if plan.Current != "" || plan.Desired != "pg_stat_statements" {
			t.Errorf("diff = %q -> %q, want \"\" -> pg_stat_statements", plan.Current, plan.Desired)
		}
		if !plan.RequiresRestart {
			t.Error("RequiresRestart = false, want true: shared_preload_libraries is postmaster-level")
		}
		if !strings.Contains(plan.RestartCost, "rolling restart") {
			t.Errorf("RestartCost %q does not state the rolling restart", plan.RestartCost)
		}
		if plan.DryRun {
			t.Error("DryRun = true, want false")
		}
	})

	t.Run("already enabled", func(t *testing.T) {
		t.Parallel()
		cr := testClusterCR(t, true)
		if err := unstructured.SetNestedField(cr.Object, "pg_stat_statements", "spec", "parameters", "shared_preload_libraries"); err != nil {
			t.Fatalf("SetNestedField: %v", err)
		}
		plan, err := planEnable(cr, "pg_stat_statements", true)
		if err != nil {
			t.Fatalf("planEnable: %v", err)
		}
		if plan.RequiresRestart {
			t.Error("RequiresRestart = true, want false: the patch would change nothing, so nothing rolls")
		}
		if plan.RestartCost == "" {
			t.Error("RestartCost is empty on the no-op branch; the offer has to say why there is no cost")
		}
		if !plan.DryRun {
			t.Error("DryRun = false, want true")
		}
	})
}

// TestEnablePlanRender is the offer's shape: every field the requirement names has
// to be on the page, because a field that exists but is not printed is a promise
// the CLI does not make.
func TestEnablePlanRender(t *testing.T) {
	t.Parallel()

	cr := testClusterCR(t, true)
	plan, err := planEnable(cr, "pg_stat_statements", true)
	if err != nil {
		t.Fatalf("planEnable: %v", err)
	}
	out := plan.render()

	for _, want := range []string{
		"postgres-data/demo",
		"pg_stat_statements",
		"current: (unset)",
		"desired: pg_stat_statements",
		"restart:",
		"mode: dry-run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("offer does not contain %q:\n%s", want, out)
		}
	}
	// The dry-run line is the difference between "here is what I would do" and
	// "here is what I did", and it is the only thing on the page that says which.
	if !strings.Contains(out, "nothing applied") {
		t.Errorf("dry-run line does not say nothing was applied:\n%s", out)
	}
}

// TestPollUntil covers the loop's three exits. sleep is injected for the reason
// collector.SQLConfig has a Now field: a caller that cannot control the clock can
// only test an elapsed-time loop by sleeping in the test.
func TestPollUntil(t *testing.T) {
	t.Parallel()

	t.Run("stops when the check says done", func(t *testing.T) {
		t.Parallel()
		var slept []time.Duration
		calls := 0
		line, err := pollUntil(time.Minute, 2*time.Second,
			func(d time.Duration) { slept = append(slept, d) },
			func() (string, bool, error) {
				calls++
				return "1/3 ready", calls >= 2, nil
			})
		if err != nil {
			t.Fatalf("pollUntil: %v", err)
		}
		if line != "1/3 ready" {
			t.Errorf("line = %q, want the check's line", line)
		}
		if calls != 2 {
			t.Errorf("check ran %d times, want 2", calls)
		}
		if len(slept) != 1 || slept[0] != 2*time.Second {
			t.Errorf("slept %v, want one interval of 2s", slept)
		}
	})

	t.Run("propagates the check's error", func(t *testing.T) {
		t.Parallel()
		want := errors.New("kube: forbidden")
		_, err := pollUntil(time.Minute, time.Second, func(time.Duration) {},
			func() (string, bool, error) { return "x", false, want })
		if !errors.Is(err, want) {
			t.Errorf("pollUntil err = %v, want it to wrap %v", err, want)
		}
	})

	t.Run("times out", func(t *testing.T) {
		t.Parallel()
		_, err := pollUntil(time.Millisecond, time.Millisecond, func(time.Duration) {},
			func() (string, bool, error) { return "0/3 ready", false, nil })
		if err == nil {
			t.Fatal("pollUntil returned no error, want a timeout")
		}
		if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "0/3 ready") {
			t.Errorf("timeout error %q does not name the timeout or the last progress line", err)
		}
	})

	t.Run("zero budgets fall back to the defaults", func(t *testing.T) {
		t.Parallel()
		// runEnable calls pollUntil(0, 0, …). A zero timeout that fell through to
		// the loop would time out on the first pass; this proves the default
		// branch is taken.
		var slept []time.Duration
		_, err := pollUntil(0, 0, func(d time.Duration) { slept = append(slept, d) },
			func() (string, bool, error) { return "3/3 ready", true, nil })
		if err != nil {
			t.Fatalf("pollUntil(0,0): %v", err)
		}
		if len(slept) != 0 {
			t.Errorf("a done-on-first-pass check slept %v, want no sleeps", slept)
		}
	})
}

// TestEnableFlagsAreDeclaredByNewServeFlags is the main_test.go:299 shape for the
// three flags this commit adds: every flag the binary declares has to be set by
// newServeFlags, because a field declared in the struct and not assigned by the
// constructor is a nil dereference at the first read.
//
// TestTableCoversEveryFlag proves the flag set and config.go's table agree; this
// proves the struct and the flag set agree, which is the half that crashes.
func TestEnableFlagsAreDeclaredByNewServeFlags(t *testing.T) {
	t.Parallel()

	sf := newServeFlags(prescan{})
	if err := sf.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if sf.actions == nil || sf.cluster == nil || sf.dryRun == nil {
		t.Fatalf("actions=%v cluster=%v dryRun=%v, want all three set by newServeFlags",
			sf.actions, sf.cluster, sf.dryRun)
	}
	// The defaults are R12.8's off state and R3.15's "no cluster named yet": an
	// empty default is what makes `pgsi serve` with no flags still refuse every
	// action.
	if *sf.actions != "" {
		t.Errorf("--allow-actions default = %q, want \"\" (R12.8's off default)", *sf.actions)
	}
	if *sf.cluster != "" {
		t.Errorf("--cluster default = %q, want \"\"", *sf.cluster)
	}
	if *sf.dryRun {
		t.Error("--dry-run default = true, want false: applying is the documented behaviour once the gate is open")
	}
}

// TestKubeConfigMirrorsEveryKubeFlag is the guard on the extraction. runServe and
// runEnable now share one kube.Config builder, so a field added to kube.Config and
// a flag added to newServeFlags but not wired into kubeConfig() is invisible to
// both paths at once — which is the failure the extraction was meant to remove, and
// the reason it needs a test rather than only a call site.
func TestKubeConfigMirrorsEveryKubeFlag(t *testing.T) {
	t.Parallel()

	sf := newServeFlags(prescan{})
	if err := sf.Parse([]string{
		"--kubeconfig=/tmp/kc",
		"--kube-context=staging",
		"--api-server-url=https://10.0.0.9:6443",
		"--bearer-token=tok",
		"--client-cert=/tmp/c.crt",
		"--client-key=/tmp/c.key",
		"--insecure-skip-verify=true",
		"--use-proxy=true",
		"--qps=99",
		"--burst=77",
		"--request-timeout=42s",
	}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cfg := sf.kubeConfig()
	for _, tc := range []struct{ field, got, want string }{
		{"Kubeconfig", cfg.Kubeconfig, "/tmp/kc"},
		{"Context", cfg.Context, "staging"},
		{"APIServerURL", cfg.APIServerURL, "https://10.0.0.9:6443"},
		{"BearerToken", cfg.BearerToken, "tok"},
		{"ClientCertFile", cfg.ClientCertFile, "/tmp/c.crt"},
		{"ClientKeyFile", cfg.ClientKeyFile, "/tmp/c.key"},
		{"UseProxy", boolString(cfg.UseProxy), "true"},
	} {
		if tc.got != tc.want {
			t.Errorf("kubeConfig().%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if !cfg.InsecureSkipVerify {
		t.Error("kubeConfig().InsecureSkipVerify = false, want true")
	}
	if int(cfg.QPS) != 99 {
		t.Errorf("kubeConfig().QPS = %v, want 99", cfg.QPS)
	}
	if cfg.Burst != 77 {
		t.Errorf("kubeConfig().Burst = %d, want 77", cfg.Burst)
	}
	if cfg.Timeout != 42*time.Second {
		t.Errorf("kubeConfig().Timeout = %v, want 42s", cfg.Timeout)
	}
}
