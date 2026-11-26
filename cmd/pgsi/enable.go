package main

// R3.15 — pg_stat_statements enablement as a product flow. This file is the
// `pgsi enable` command: the CLI half of the four steps `requirements.md:322`
// orders as detect → offer → patch → verify.
//
// The requirement states why a command exists at all: "CREATE EXTENSION alone
// fails on a stock CNPG cluster (`required extension \"pg_stat_statements\" is not
// loaded`) because `shared_preload_libraries` is postmaster-level and empty by
// default on all three reachable reference clusters." The extension cannot be
// created by connecting and running DDL — the library has to be preloaded by the
// postmaster, which on CNPG means patching spec.parameters, which triggers a
// rolling restart. That is why this command prints a diff and a restart cost
// before it applies anything, and why applying is gated rather than default.
//
// The requirement's own spelling of the command is `pgsi enable
// pg-stat-statements --cluster X`: the extension name is positional and the
// cluster is a flag. Go's flag package stops parsing at the first non-flag
// argument, so splitEnableArgs lifts the positional out before Parse — the same
// reason scanArgs exists for serve's two dynamic flag families.
//
// R12.8 (`requirements.md:475`) is the gate that makes the patch safe: the flag's
// value names each mutation individually, and this command's action is
// api.ActionNameEnableExtension. Gate closed → print the offer and refuse with exit 1.
// Gate open → print the offer, patch, verify. --dry-run prints the offer and exits
// 0 without consulting the gate, which is how the offer gets inspected on a
// cluster nobody has opted into mutating yet.
//
// The other half of R3.15's flow — the UI action — is A6's, and the API field both
// halves read is api.Config.Actions (`internal/api/server.go:127`). This file is
// the first thing in cmd/pgsi to populate that field from the flag it is documented
// to come from; before it, main.go declared no --allow-actions flag at all, so
// every action was permanently "disabled" on every deployment no matter what the
// chart passed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
	"github.com/frankellis/pgsql-insights/internal/discovery"
	"github.com/frankellis/pgsql-insights/internal/kube"
	"github.com/frankellis/pgsql-insights/internal/logx"
)

// knownActions is the set R12.8's value draws from, which the requirement
// enumerates in the sentence that generalised the flag: "the full set of mutations
// the product performs: pg_terminate_backend (R4.5), the spec.parameters patch
// (R3.15), and any future action — each individually named in the flag's value,
// not covered by one global boolean."
//
// It lives here because this is the package that parses the flag, and because the
// two consumers ask different questions: the API asks "is this one action allowed"
// (`internal/api/active_queries.go:158`), which needs a membership test, while
// parseAllowActions needs the whole set to validate a name it has never seen.
//
// The set itself is api.KnownActions (`internal/api/actions.go:69`) — the single
// home A2 built for the two names, and where this file's own
// ActionNameEnableExtension const moved to. Aliasing rather than restating is the
// point: a second []string in cmd/pgsi is a second list the flag's value can
// disagree with, which is what the D13 provisional-marking rule exists to prevent.
var knownActions = api.KnownActions

// parseAllowActions is R12.8's flag value grammar.
//
// The value is a comma-separated list of action names, and the requirement is
// explicit that a list — not a boolean — is the generalised form. api.Config's
// docblock (`internal/api/server.go:104`) settles the two legacy spellings and
// this function follows it verbatim: "the string `false` is simply not a member of
// any action set, so a config parsed from the old spelling yields an empty list,
// which is the off state." So "false" is the empty set rather than an error, and by
// the same reading "true" is the full set — the two halves of what that docblock
// calls "a list represents both".
//
// An unrecognised name is an error naming the valid set, not a silent drop. A
// deployment that spells the flag --allow-actions=terminate-session gets a message
// saying what the flag accepts, rather than a server that quietly allows nothing
// while answering 200 — the same failure R8.7 exists to prevent, one requirement
// over.
func parseAllowActions(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return nil, nil
	case "false", "none":
		return nil, nil
	case "true":
		return append([]string(nil), knownActions...), nil
	}

	var out []string
	for _, part := range strings.Split(v, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !slices.Contains(knownActions, name) {
			return nil, fmt.Errorf("allow-actions: unknown action %q; %s accepts %s",
				name, "--allow-actions", strings.Join(knownActions, ", "))
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// actionAllowed is the gate question this command asks: may this process perform
// `action`. It is the same membership test `internal/api/active_queries.go:158` runs for R4.1's
// Action column, reached from the other side — that one returns the column's value
// ("terminate" or "disabled"), this one returns a bool for a command that has to
// choose between applying a patch and refusing.
//
// Two three-line functions over one rule is a known duplication, not a new one: the
// fix is to export the membership test from internal/api and have both call it,
// which is A2's edit to make in their file. Until then the rule is stated once per
// consumer and each cites the other.
func actionAllowed(actions []string, action string) bool {
	return slices.Contains(actions, action)
}

// splitEnableArgs lifts the positional extension name out of argv.
//
// R3.15 spells the command `pgsi enable pg-stat-statements --cluster X`, with the
// extension as the first argument and the cluster as a flag after it. Go's flag
// package stops parsing at the first argument that is not a flag, so a Parse call
// on that argv takes "pg-stat-statements" as the whole command line and leaves
// --cluster unparsed. Taking the first argument out is what makes the documented
// spelling parse, and it is the same shape scanArgs exists for: a pre-pass that
// removes what flag.Parse cannot hold and hands it the rest.
//
// It takes args[0] and nothing else, which is the whole rule for the spelling the
// requirement writes — and the one thing it cannot do is the reversed order,
// `pgsi enable --cluster X pg-stat-statements`. Distinguishing a flag's value from
// a second positional needs the flag set itself (is `ns/name` the value of
// --cluster, or another extension?), and the only parser for that is the FlagSet
// this function exists to feed. The reversed order therefore reports "no extension
// named" with the usage string attached, which is a wrong-but-actionable answer
// for an order the spec does not write.
func splitEnableArgs(args []string) (extension string, rest []string, err error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, errors.New("enable: no extension named (usage: pgsi enable <extension> --cluster <ns/name>)")
	}
	return args[0], args[1:], nil
}

// parseClusterRef resolves R3.15's --cluster value into a namespace and a name.
//
// It requires the qualified `namespace/name` form. --credential-secret accepts a
// bare name because R1.8 resolves that one against the target it is being asked
// about, so the namespace is already known from context; --cluster has no such
// context — it *is* the context — and a bare name there would mean "the cluster
// called X", in whichever namespace the process happens to look first. Requiring
// the qualifier is the difference between one answer and a search order.
func parseClusterRef(s string) (namespace, name string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", errors.New("enable: --cluster is required (R3.15's flow is per-cluster, and \"never auto-applied to more than one cluster per invocation\")")
	}
	ns, n, ok := strings.Cut(s, "/")
	if !ok || ns == "" || n == "" {
		return "", "", fmt.Errorf("enable: --cluster %q is not namespace/name; a bare cluster name has no namespace to resolve against", s)
	}
	return ns, n, nil
}

// EnablePlan is R3.15's "offer": the documented diff and the restart cost, in the
// shape both the CLI and the UI action render.
//
// The requirement's sentence that forces the last two fields is "The restart cost
// is stated in the offer, not discovered afterwards." A caller who has to read the
// operator's docs to learn that the patch rolls the cluster is a caller who finds
// it out afterwards, so the cost is a field of the plan rather than a sentence in
// this file.
type EnablePlan struct {
	Namespace string `json:"namespace"`
	Cluster   string `json:"cluster"`
	// Extension is what is being enabled; R3.15's flow names one per invocation.
	Extension string `json:"extension"`
	// Current and Desired are the two sides of the diff — the CR's present
	// shared_preload_libraries and the value the patch would write.
	Current string `json:"current"`
	Desired string `json:"desired"`
	// RequiresRestart is false when Current already equals Desired, which is the
	// already-enabled case: the patch would be a no-op and the restart would not
	// be.
	RequiresRestart bool   `json:"requiresRestart"`
	RestartCost     string `json:"restartCost"`
	DryRun          bool   `json:"dryRun"`
}

// sharedPreloadLibraries reads spec.parameters.shared_preload_libraries off a
// Cluster CR.
//
// It goes through unstructured rather than discovery's field readers because those
// are unexported (internal/discovery/fields.go) and are written for the CR shape
// discovery's own builders consume; the CRD's spec.parameters is a
// map[string]string of Postgres GUCs, and reading one key out of it does not need
// a typed client for a CRD that has no generated one.
func sharedPreloadLibraries(cr *unstructured.Unstructured) string {
	v, _, _ := unstructured.NestedString(cr.Object, "spec", "parameters", "shared_preload_libraries")
	return v
}

// withLibrary returns current with lib added, keeping the list idempotent.
//
// Idempotence is the requirement's "reversible" clause seen from the other side: a
// patch that appends the library on every invocation grows the value forever and
// eventually produces `pg_stat_statements,pg_stat_statements`, which Postgres
// accepts and which makes the diff meaningless. Empty current yields the bare name,
// not a leading comma.
func withLibrary(current, lib string) string {
	for _, existing := range strings.Split(current, ",") {
		if strings.TrimSpace(existing) == lib {
			return current
		}
	}
	if strings.TrimSpace(current) == "" {
		return lib
	}
	return current + "," + lib
}

// buildEnablePatch renders the merge-patch body that writes desired.
//
// A merge patch replaces spec.parameters wholesale with the object given here, so
// the body has to carry every parameter the cluster already has, not just the one
// being changed. That is why the function takes the CR and not just the value: it
// copies spec.parameters, overwrites the one key, and patches the whole map back.
// Patching `{"spec":{"parameters":{"shared_preload_libraries":…}}}` alone would
// drop every other parameter the cluster had set.
func buildEnablePatch(cr *unstructured.Unstructured, desired string) ([]byte, error) {
	params, found, err := unstructured.NestedStringMap(cr.Object, "spec", "parameters")
	if err != nil {
		return nil, fmt.Errorf("enable: spec.parameters is not a string map: %w", err)
	}
	if !found {
		params = map[string]string{}
	}
	params["shared_preload_libraries"] = desired

	body, err := json.Marshal(map[string]any{
		"spec": map[string]any{"parameters": params},
	})
	if err != nil {
		return nil, fmt.Errorf("enable: encode spec.parameters patch: %w", err)
	}
	return body, nil
}

// planEnable is R3.15's offer step for one cluster: read the CR, compute the diff,
// state the cost.
//
// "never auto-applied to more than one cluster per invocation" is what makes this
// take one CR and return one plan. A plan that covered a fleet would need a per-
// cluster restart cost and a per-cluster gate, and the requirement deliberately
// does not have either yet.
func planEnable(cr *unstructured.Unstructured, extension string, dryRun bool) (EnablePlan, error) {
	current := sharedPreloadLibraries(cr)
	desired := withLibrary(current, extension)

	p := EnablePlan{
		Namespace: cr.GetNamespace(),
		Cluster:   cr.GetName(),
		Extension: extension,
		Current:   current,
		Desired:   desired,
		DryRun:    dryRun,
	}
	// The cost is stated in the offer because the requirement says the cost is
	// stated in the offer. It is conditional on RequiresRestart because a patch
	// that changes nothing does not roll anything, and promising a restart there
	// would be a cost the caller never pays.
	p.RequiresRestart = current != desired
	if p.RequiresRestart {
		p.RestartCost = "spec.parameters is postmaster-level: CNPG applies it with a rolling restart, one instance at a time, primary last"
	} else {
		p.RestartCost = "none: shared_preload_libraries already lists " + extension
	}
	return p, nil
}

// render prints the offer. It is a method rather than a Printf at the call site
// because two callers render it — the CLI here and, when R3.15's UI action is
// built, the API — and the requirement's "stated in the offer" is one statement
// with two renderings that must not diverge in what they promise.
func (p EnablePlan) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cluster: %s/%s\n", p.Namespace, p.Cluster)
	fmt.Fprintf(&b, "extension: %s\n", p.Extension)
	fmt.Fprintf(&b, "  spec.parameters.shared_preload_libraries\n")
	fmt.Fprintf(&b, "    current: %s\n", emptyAs(p.Current, "(unset)"))
	fmt.Fprintf(&b, "    desired: %s\n", emptyAs(p.Desired, "(unset)"))
	fmt.Fprintf(&b, "  restart: %s\n", p.RestartCost)
	if p.DryRun {
		b.WriteString("  mode: dry-run (nothing applied)\n")
	}
	return b.String()
}

func emptyAs(v, instead string) string {
	if strings.TrimSpace(v) == "" {
		return instead
	}
	return v
}

// clusterProgress is the per-instance line R3.15's verify step reports: "report
// the rolling-restart progress per instance".
//
// It reads status.instances (pod → node), status.readyInstances and
// status.clusterPhase. The count is read through nestedCount rather than
// NestedInt64 alone because a CR decoded from JSON holds the number as a float64
// while one built by a Go test holds it as an int64, and a reader that handles one
// of the two reports the other as "no instances" — which in a progress report is
// indistinguishable from a cluster that has none.
type clusterProgress struct {
	Phase   string
	Ready   int64
	Want    int64
	Pods    []string
	Summary string
}

func clusterProgressOf(cr *unstructured.Unstructured) clusterProgress {
	p := clusterProgress{}
	p.Phase, _, _ = unstructured.NestedString(cr.Object, "status", "clusterPhase")
	if v, ok := nestedCount(cr.Object, "status", "readyInstances"); ok {
		p.Ready = v
	}
	if v, ok := nestedCount(cr.Object, "spec", "instances"); ok {
		p.Want = v
	}
	if instances, ok, _ := unstructured.NestedStringMap(cr.Object, "status", "instances"); ok {
		for pod := range instances {
			p.Pods = append(p.Pods, pod)
		}
		slices.Sort(p.Pods)
	}
	p.Summary = fmt.Sprintf("%d/%d ready, phase %q", p.Ready, p.Want, p.Phase)
	return p
}

// nestedCount reads an integer at path, accepting the int64 and float64 encodings
// an unstructured object can hold for the same JSON number.
func nestedCount(obj map[string]any, path ...string) (int64, bool) {
	if v, ok, _ := unstructured.NestedInt64(obj, path...); ok {
		return v, true
	}
	if v, ok, _ := unstructured.NestedFloat64(obj, path...); ok {
		return int64(v), true
	}
	return 0, false
}

// pollUntil drives R3.15's "poll until the extension is queryable".
//
// check reports a progress line and whether the condition is met. sleep is a
// parameter rather than time.Sleep for the reason collector.SQLConfig's Now field
// gives for its clock: a caller that cannot control the clock can only test an
// elapsed-time loop by sleeping in the test, and a test that sleeps is a test that
// gets skipped when it gets slow.
func pollUntil(timeout, interval time.Duration, sleep func(time.Duration), check func() (string, bool, error)) (string, error) {
	if timeout <= 0 {
		timeout = defaultVerifyTimeout
	}
	if interval <= 0 {
		interval = defaultVerifyInterval
	}
	deadline := time.Now().Add(timeout)
	for {
		line, done, err := check()
		if err != nil {
			return line, err
		}
		if done {
			return line, nil
		}
		if time.Now().After(deadline) {
			return line, fmt.Errorf("enable: timed out after %s waiting for the extension to become queryable (last: %s)", timeout, line)
		}
		sleep(interval)
	}
}

// The two verify budgets. They are constants and not flags because R3.15 names
// neither, and a knob nobody asked for is a knob nobody reads the documentation
// for; the values come from what the restart actually costs — a CNPG rolling
// restart of a three-instance cluster is minutes, and one probe of the CR is
// seconds.
const (
	defaultVerifyTimeout  = 10 * time.Minute
	defaultVerifyInterval = 5 * time.Second
)

// extensionProbe asks one instance whether an extension is installed.
//
// It is a function type so runEnable's verify step is testable without a cluster:
// the tests hand it a closure, and the production wiring is probeExtension below.
type extensionProbe func(ctx context.Context, tgt collector.Target) (bool, error)

// probeExtension is R3.1's detection query, run once against one instance.
//
// The SQL comes from pgversion.Query rather than a string literal here, because
// that matrix is the repo's single answer to "what is pg_extension's shape on this
// major" and a fourth copy of the query would be a fourth answer to it.
//
// It probes one database. pg_extension is per-database — an extension installed in
// `postgres` is not installed in `appdb` — while R3.15's patch is postmaster-level
// and cluster-wide, so a yes here means "loaded and created in the database the
// opener connected to". That is the honest scope of the answer, and it is the
// distinction A3's S119 makes about the collector: the question asked and the
// database asked in have to be named together.
func probeExtension(ctx context.Context, opener collector.SQLOpener, tgt collector.Target, ext string) (bool, error) {
	if opener == nil {
		return false, errors.New("enable: no SQL opener; configure --credential-secret or --credential-username to verify by query")
	}
	query, ok := pgversion.Query(pgversion.ViewExtension, tgt.PGMajor)
	if !ok {
		return false, fmt.Errorf("enable: no pg_extension query for PostgreSQL major %d on %s", tgt.PGMajor, tgt.Key())
	}
	tx, err := opener.Begin(ctx, tgt, collector.SQLConfig{})
	if err != nil {
		return false, fmt.Errorf("enable: begin on %s: %w", tgt.Key(), err)
	}
	rows, err := tx.Query(ctx, query)
	if err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("enable: query pg_extension on %s: %w", tgt.Key(), err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("enable: commit on %s: %w", tgt.Key(), err)
	}
	for _, row := range rows {
		if name, _ := row.Get("extname"); name == ext {
			return true, nil
		}
	}
	return false, nil
}

// runEnable is `pgsi enable <extension> --cluster <ns/name>`.
//
// It parses through newServeFlags — the same function serve and config dump use —
// so there is one declaration of every flag in the binary and the three subcommands
// cannot disagree about what argv meant. That is the reason runConfig calls it
// rather than declaring its own subset, and the reason this does too.
func runEnable(args []string) int {
	extension, rest, err := splitEnableArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	pre, err := scanArgs(rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "enable: %v\n", err)
		return 2
	}
	sf := newServeFlags(pre)
	if err := sf.Parse(pre.rest); err != nil {
		return 2
	}
	if _, err := applyFlags(sf.FlagSet, OSEnv()); err != nil {
		fmt.Fprintf(os.Stderr, "enable: %v\n", err)
		return 2
	}

	log, err := logx.NewFromFlags(*sf.logLevel, sf.logField)
	if err != nil {
		fmt.Fprintf(os.Stderr, "enable: %v\n", err)
		return 2
	}

	actions, err := parseAllowActions(*sf.actions)
	if err != nil {
		log.Error("allow-actions", "error", err.Error())
		return 2
	}
	ns, name, err := parseClusterRef(*sf.cluster)
	if err != nil {
		log.Error("cluster", "error", err.Error())
		return 2
	}

	kc, err := kube.New(sf.kubeConfig())
	if err != nil {
		log.Error("kube client", "error", err.Error())
		return 2
	}
	if kc.Dynamic == nil {
		log.Error("kube client", "error", "no dynamic client; the CNPG Cluster CR cannot be read")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*sf.reqTimeout)+30*time.Second)
	defer cancel()

	// --- detect + offer -------------------------------------------------
	cr, err := kc.Dynamic.Resource(discovery.ClusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		log.Error("read cluster CR", "error", err.Error(), "cluster", ns+"/"+name)
		return 1
	}
	plan, err := planEnable(cr, extension, *sf.dryRun)
	if err != nil {
		log.Error("plan", "error", err.Error(), "cluster", ns+"/"+name)
		return 1
	}
	fmt.Print(plan.render())

	// --- gate -----------------------------------------------------------
	if plan.DryRun {
		return 0
	}
	if !actionAllowed(actions, api.ActionNameEnableExtension) {
		// R12.8's whole point: a read-only product does not become a mutation
		// surface by surprise. The offer is printed above so the operator can see
		// exactly what the gate is withholding, and the exit code is 1 — the
		// command ran and declined, which is not the same as it failing to run.
		fmt.Println("refused: " + api.ActionNameEnableExtension + " is not in --allow-actions (R12.8); add it, or re-run with --dry-run")
		return 1
	}

	// --- patch ----------------------------------------------------------
	patch, err := buildEnablePatch(cr, plan.Desired)
	if err != nil {
		log.Error("build patch", "error", err.Error(), "cluster", ns+"/"+name)
		return 1
	}
	log.Info("patching cluster CR", "cluster", ns+"/"+name, "patch", string(patch))
	if _, err := kc.Dynamic.Resource(discovery.ClusterGVR).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		log.Error("patch cluster CR", "error", err.Error(), "cluster", ns+"/"+name)
		return 1
	}

	// --- verify ---------------------------------------------------------
	opener := newSQLOpener(kc, collector.ConnConfig{
		Secret:   *sf.credSecret,
		Username: *sf.credUser,
		Password: *sf.credPass,
	})
	major := crPGMajor(cr)
	verify := func() (string, bool, error) {
		fresh, err := kc.Dynamic.Resource(discovery.ClusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", false, fmt.Errorf("enable: re-read cluster CR: %w", err)
		}
		progress := clusterProgressOf(fresh)
		if sharedPreloadLibraries(fresh) != plan.Desired {
			return progress.Summary, false, nil
		}
		pod, ok := primaryPod(fresh)
		if !ok || opener == nil || major == 0 {
			return progress.Summary, false, nil
		}
		tgt, err := targetForPod(kc, ns, name, pod, major)
		if err != nil {
			return progress.Summary, false, err
		}
		ok, err = probeExtension(ctx, opener, tgt, extension)
		if err != nil {
			return progress.Summary, false, err
		}
		return progress.Summary, ok, nil
	}

	line, err := pollUntil(0, 0, func(d time.Duration) { time.Sleep(d) }, verify)
	if err != nil {
		log.Error("verify", "error", err.Error(), "cluster", ns+"/"+name, "progress", line)
		return 1
	}
	fmt.Printf("verified: %s\n", line)
	return 0
}

// crPGMajor reads the PostgreSQL major off the CR so the probe can pick the right
// pg_extension query. CNPG records it as status.cachedSerial / the pod label
// cnpg.io/pgMajorVersion; the CR itself carries it only when the operator has
// cached it, so 0 here means unknown and the caller treats unknown as "cannot
// probe by query yet".
func crPGMajor(cr *unstructured.Unstructured) int {
	if v, ok := nestedCount(cr.Object, "status", "pgMajorVersion"); ok {
		return int(v)
	}
	return 0
}

// primaryPod returns the primary instance's pod name from status.pods.primary.
func primaryPod(cr *unstructured.Unstructured) (string, bool) {
	pod, ok, _ := unstructured.NestedString(cr.Object, "status", "pods", "primary")
	return pod, ok && pod != ""
}

// targetForPod builds the collector.Target the SQL probe connects to, from the pod
// the CR names as primary.
//
// The address comes from discovery.EndpointsFor, which is the table every other
// consumer of a pod's 5432 address reads; building "ip:5432" here instead would be
// a second copy of R1.10's port table in the one package that is not allowed to
// have one.
func targetForPod(kc *kube.Client, ns, cluster, pod string, major int) (collector.Target, error) {
	p, err := kc.Clientset.CoreV1().Pods(ns).Get(context.Background(), pod, metav1.GetOptions{})
	if err != nil {
		return collector.Target{}, fmt.Errorf("enable: get pod %s/%s: %w", ns, pod, err)
	}
	return targetFromPod(p, cluster, major), nil
}

func targetFromPod(p *corev1.Pod, cluster string, major int) collector.Target {
	return collector.Target{
		Namespace: p.GetNamespace(),
		Cluster:   cluster,
		Pod:       p.GetName(),
		Role:      p.GetLabels()["role"],
		Address:   discovery.EndpointsFor(p.Status.PodIP, false).Postgres,
		Source:    collector.SourceSQL,
		PGMajor:   major,
	}
}
