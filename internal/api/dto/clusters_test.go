package dto

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// goldenPath is the committed contract for GET /api/v1/clusters: the four
// reference clusters (requirements.md §8) serialised exactly as the API emits
// them. Its location is not a local choice — R16.8 names it:
// "Phase 1a: `testdata/api/clusters.json` with the 4 reference clusters", at
// the repo-root testdata/api/ tree, "consumed by the SPA's Vitest suite and
// R16.7's visual-regression snapshots". So the web tooling and this test read
// the same file; there is no second copy in Go's tree.
//
// Phase 1b renders the Fleet page from this shape while the handler is
// still being written, so it is guarded two ways:
//
//   - TestGoldenFixtureIsCanonical fails if a json tag changes without the
//     fixture being regenerated in the same commit (the file must be the
//     canonical encoding of its own parsed form).
//   - TestReferenceEnvironmentAcceptance asserts the §8 acceptance facts
//     against the parsed fixture, which is the Phase 1a acceptance criterion
//     ("all 4 clusters with correct phase, instance counts, servingInstances,
//     primary and PG major") expressed as a unit test that runs offline.
const goldenPath = "../../../testdata/api/clusters.json"

var updateGolden = flag.Bool("update", false, "rewrite testdata/clusters.golden.json from its parsed form")

func readGolden(t *testing.T) ([]byte, Envelope[ClusterView]) {
	t.Helper()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	var env Envelope[ClusterView]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}
	return raw, env
}

func TestGoldenFixtureIsCanonical(t *testing.T) {
	raw, env := readGolden(t)

	// Re-marshalling the parsed fixture must reproduce the file byte for byte.
	// json.Marshal emits struct fields in declaration order and sorts map
	// keys, so any change to a tag, a field name, an order, or an omitempty
	// that the fixture was not updated for shows up here.
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if *updateGolden {
		if err := os.WriteFile(goldenPath, append(out, '\n'), 0o600); err != nil {
			t.Fatalf("write %s: %v", goldenPath, err)
		}
		t.Logf("rewrote %s", goldenPath)
		raw, _ = os.ReadFile(goldenPath)
	}
	if string(out)+"\n" != string(raw) {
		t.Fatalf("%s is not the canonical encoding of its parsed form.\n"+
			"If you changed a field in clusters.go, run `go test ./internal/api/dto -update` "+
			"and commit the fixture in the same commit (and tell the UI workstream).\n"+
			"want %d bytes, got %d bytes", goldenPath, len(raw), len(out))
	}
}

// TestReferenceEnvironmentAcceptance encodes requirements.md §8 acceptance
// checks 1 and 2 for Phase 1a.
func TestReferenceEnvironmentAcceptance(t *testing.T) {
	_, env := readGolden(t)

	if env.Meta.Total != 4 || len(env.Data) != 4 {
		t.Fatalf("inventory has %d clusters (meta.total %d), want 4 (requirements.md §8)",
			len(env.Data), env.Meta.Total)
	}
	if env.Meta.GeneratedAt.IsZero() {
		t.Error("meta.generatedAt is zero; R8.4 requires it on every list response")
	}

	// §8 check 1: "GET /api/v1/clusters returns exactly the 4 clusters with
	// correct phase/instances/servingInstances/primary/major."
	//
	// The 16-vs-17 distinction is the part the spec calls out explicitly:
	// open-webui-pg is the only PG 16 cluster in the reference set and is what
	// proves the version matrix reads per-cluster state rather than assuming
	// one version per install.
	cases := []struct {
		ns, name, phase, primary string
		pgMajor                  int
		desired, ready, serving  int
	}{
		{"ai-api-gateway", "rev-proxy-pg", "Cluster in healthy state", "rev-proxy-pg-2", 17, 2, 2, 2},
		{"cookbook", "cookbook-pg", "Cluster in healthy state", "cookbook-pg-1", 17, 1, 1, 1},
		{"freeidp", "freeidp-pg", "Not enough disk space", "freeidp-pg-1", 17, 2, 0, 0},
		{"open-webui", "open-webui-pg", "Cluster in healthy state", "open-webui-pg-2", 16, 2, 2, 2},
	}
	byName := map[string]ClusterView{}
	for _, c := range env.Data {
		key := c.Namespace + "/" + c.Name
		if _, dup := byName[key]; dup {
			t.Fatalf("duplicate cluster %s in inventory", key)
		}
		byName[key] = c
	}
	for _, want := range cases {
		key := want.ns + "/" + want.name
		got, ok := byName[key]
		if !ok {
			t.Errorf("cluster %s missing from the inventory", key)
			continue
		}
		if got.Phase != want.phase {
			t.Errorf("%s: phase = %q, want %q", key, got.Phase, want.phase)
		}
		if got.CurrentPrimary != want.primary {
			t.Errorf("%s: currentPrimary = %q, want %q", key, got.CurrentPrimary, want.primary)
		}
		// pgMajor is a pointer (R1.3's answer is sometimes not known yet), so
		// a missing one is an error rather than a zero rendering as a version.
		if got.PGMajor == nil {
			t.Errorf("%s: pgMajor is null, want %d (R1.3)", key, want.pgMajor)
		} else if *got.PGMajor != want.pgMajor {
			t.Errorf("%s: pgMajor = %d, want %d (R1.3 must distinguish 16 from 17)", key, *got.PGMajor, want.pgMajor)
		}
		if got.Instances != want.desired || got.ReadyInstances != want.ready || got.ServingInstances != want.serving {
			t.Errorf("%s: instances/ready/serving = %d/%d/%d, want %d/%d/%d",
				key, got.Instances, got.ReadyInstances, got.ServingInstances,
				want.desired, want.ready, want.serving)
		}
	}

	// §8 check 2: the freeidp-pg entry reports the disk-pressure phaseReason,
	// the Pending instance, and servingInstances: 0 (R1.2 [A]).
	fp := byName["freeidp/freeidp-pg"]
	if fp.PhaseReason == "" {
		t.Error("freeidp-pg: phaseReason empty; R1.2 requires the disk-pressure reason to be surfaced")
	}
	if fp.ServingInstances != 0 {
		t.Errorf("freeidp-pg: servingInstances = %d, want 0 — the CR reports 2 instances and zero are serving (live-probe.md Finding 9)", fp.ServingInstances)
	}
	var pending *InstanceView
	for i := range fp.InstanceList {
		if fp.InstanceList[i].Name == "freeidp-pg-2" {
			pending = &fp.InstanceList[i]
		}
	}
	if pending == nil {
		t.Fatal("freeidp-pg: the Pending instance freeidp-pg-2 is missing from instanceList")
	}
	if pending.Phase != "Pending" || pending.Serving || pending.Ready {
		t.Errorf("freeidp-pg-2: got phase=%q ready=%v serving=%v, want Pending/false/false",
			pending.Phase, pending.Ready, pending.Serving)
	}
	if pending.ContainerStatus != "PodInitializing" {
		t.Errorf("freeidp-pg-2: containerStatus = %q, want PodInitializing", pending.ContainerStatus)
	}
	if pending.Role != "" {
		t.Errorf("freeidp-pg-2: role = %q, want empty — the pod carries neither cnpg.io/instanceRole nor role (R1.2 fallback chain)", pending.Role)
	}
	// The crash-looping primary is the second R1.2 variant: Running, but its
	// postgres container is not running, so it is not serving.
	for _, in := range fp.InstanceList {
		if in.Name != "freeidp-pg-1" {
			continue
		}
		if in.Phase != "Running" || in.Serving || in.ContainerStatus != "CrashLoopBackOff" {
			t.Errorf("freeidp-pg-1: got phase=%q serving=%v containerStatus=%q, want Running/false/CrashLoopBackOff",
				in.Phase, in.Serving, in.ContainerStatus)
		}
		if in.Endpoints.Status != "https://10.42.0.198:8000" {
			t.Errorf("freeidp-pg-1: endpoints.status = %q, want https://10.42.0.198:8000 (R1.10: 8000 is HTTPS always)", in.Endpoints.Status)
		}
		if in.Endpoints.Metrics != "http://10.42.0.198:9187" {
			t.Errorf("freeidp-pg-1: endpoints.metrics = %q, want http://10.42.0.198:9187 (R1.10: 9187 is HTTP unless spec.monitoring.tls.enabled)", in.Endpoints.Metrics)
		}
	}

	// §8: "rev-proxy-pg shows its Pooler" (R1.4).
	rp := byName["ai-api-gateway/rev-proxy-pg"]
	if len(rp.Poolers) != 1 {
		t.Fatalf("rev-proxy-pg: %d poolers, want 1", len(rp.Poolers))
	}
	p := rp.Poolers[0]
	if p.Name != "rev-proxy-pg-pooler" || p.Cluster != "rev-proxy-pg" {
		t.Errorf("pooler: name=%q cluster=%q, want rev-proxy-pg-pooler / rev-proxy-pg (R1.4 associates via spec.cluster.name)", p.Name, p.Cluster)
	}
	if p.PoolMode != "transaction" || p.Type != "rw" {
		t.Errorf("pooler: poolMode=%q type=%q, want transaction/rw", p.PoolMode, p.Type)
	}
	if p.Instances != 2 || p.ReadyInstances != 2 || len(p.Pods) != 2 {
		t.Errorf("pooler: instances=%d ready=%d pods=%d, want 2/2/2", p.Instances, p.ReadyInstances, len(p.Pods))
	}
	if p.Parameters["default_pool_size"] != "20" || p.Parameters["max_client_conn"] != "400" {
		t.Errorf("pooler: parameters = %v, want default_pool_size 20 / max_client_conn 400", p.Parameters)
	}
	if p.MetricsURL == "" {
		t.Error("pooler: metricsURL empty; R1.10 gives pgbouncer metrics on 9127")
	}

	// R1.6 zero-prerequisite rule: every cluster renders with its Services and
	// PVCs populated, and three of the four have no Pooler at all — the
	// inventory must still be renderable, which is what the empty (not null)
	// slices guarantee.
	for _, c := range env.Data {
		key := c.Namespace + "/" + c.Name
		if c.Services == nil {
			t.Errorf("%s: services is null; R8.4/R1.5 require an array so the UI never renders a missing field", key)
		}
		if c.PVCs == nil || len(c.PVCs) == 0 {
			t.Errorf("%s: pvcs empty; R1.5 requires the PVC list", key)
		}
		if c.Poolers == nil {
			t.Errorf("%s: poolers is null; must serialise as [] when a cluster has none (R1.6)", key)
		}
		if c.CredentialSource == "" {
			t.Errorf("%s: credentialSource empty; R1.8's resolution must be observable", key)
		}
		if c.PGImage == "" {
			t.Errorf("%s: pgImage empty; R1.3 needs the image to resolve the version", key)
		}
		if c.CreatedAt.IsZero() {
			t.Errorf("%s: createdAt zero", key)
		}
		for _, in := range c.InstanceList {
			if in.Name == "" || in.Phase == "" {
				t.Errorf("%s: instance %+v missing name/phase", key, in)
			}
		}
	}

	// R8.10: the fixture is a full page, so there is no cursor.
	if env.Meta.Truncated || env.Meta.NextCursor != "" {
		t.Errorf("meta.truncated=%v nextCursor=%q; a 4-cluster page with the default limit of 50 must not be paginated",
			env.Meta.Truncated, env.Meta.NextCursor)
	}
}

// TestErrorShape pins R8.5's error body: {error:{code,message,details[]}} with
// details always present. The UI's error reader depends on the array existing
// even when empty, so it is not omitempty — and a nil slice would marshal to
// `null`, a different JSON type, which is why handlers build errors through
// NewError rather than by hand.
func TestErrorShape(t *testing.T) {
	b, err := json.Marshal(NewError(ErrNotFound, "no such cluster"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":{"code":"NotFound","message":"no such cluster","details":[]}}`
	if string(b) != want {
		t.Fatalf("error body =\n%s\nwant\n%s", b, want)
	}

	// The hand-composed nil case, kept as a regression marker: without
	// NewError's normalisation this is what the API used to emit.
	raw, err := json.Marshal(ErrorBody{Error: ErrorDetail{Code: ErrInternal, Message: "boom"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"error":{"code":"Internal","message":"boom","details":null}}`; string(raw) != want {
		t.Fatalf("hand-composed error body =\n%s\nwant\n%s", raw, want)
	}

	// Details passed through verbatim, and the whole enum is exercised so a
	// renamed constant is caught here rather than at a call site.
	b, err = json.Marshal(NewError(ErrPromQLUnavailable, "not in catalogue", "q1: unknown metric", "q2: range too wide"))
	if err != nil {
		t.Fatal(err)
	}
	want = `{"error":{"code":"PromQLUnavailable","message":"not in catalogue","details":["q1: unknown metric","q2: range too wide"]}}`
	if string(b) != want {
		t.Fatalf("error body =\n%s\nwant\n%s", b, want)
	}
}

// TestMetaPartial pins R10.22's partial-render contract and settles the three
// questions it raises:
//
//   - meta.total counts clusters the response can serve, not namespaces
//     attempted, so a 3-of-4 page says total:3 and explains the fourth in
//     meta.failed — "attempted" would be total + len(failed) + len(loading).
//   - truncated stays purely R8.10's pagination flag; a complete page with a
//     failed namespace has truncated:false and a non-empty failed[].
//   - failures are per namespace, not per cluster, because a namespace whose
//     watch errored contributes no ClusterView to hang an error on.
func TestMetaPartial(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 12, 0, 0, time.UTC)
	last := time.Date(2026, 11, 20, 8, 45, 0, 0, time.UTC)

	b, err := json.Marshal(Meta{
		Total: 3, GeneratedAt: at,
		Failed: []NamespaceFailure{
			{Namespace: "legacy", Code: ErrForbidden, Message: "cannot list clusters: forbidden", LastSuccessTime: &last},
		},
		Loading: []string{"billing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"total":3,"truncated":false,"generatedAt":"2026-11-20T09:12:00Z",` +
		`"failed":[{"namespace":"legacy","code":"Forbidden","message":"cannot list clusters: forbidden","lastSuccessTime":"2026-11-20T08:45:00Z"}],` +
		`"loading":["billing"]}`
	if string(b) != want {
		t.Fatalf("meta =\n%s\nwant\n%s", b, want)
	}

	// A complete page omits both (the reference fixture's case).
	b, err = json.Marshal(Meta{Total: 4, GeneratedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if want = `{"total":4,"truncated":false,"generatedAt":"2026-11-20T09:12:00Z"}`; string(b) != want {
		t.Fatalf("meta =\n%s\nwant\n%s", b, want)
	}

	// A namespace that has never synced omits lastSuccessTime — the UI renders
	// "never synced" from its absence rather than from a zero timestamp.
	b, err = json.Marshal(Meta{Total: 0, GeneratedAt: at,
		Failed: []NamespaceFailure{{Namespace: "brand-new", Code: ErrUnavailable, Message: "connection refused"}}})
	if err != nil {
		t.Fatal(err)
	}
	want = `{"total":0,"truncated":false,"generatedAt":"2026-11-20T09:12:00Z","failed":[{"namespace":"brand-new","code":"Unavailable","message":"connection refused"}]}`
	if string(b) != want {
		t.Fatalf("meta =\n%s\nwant\n%s", b, want)
	}
}

// TestFixtureDirectory checks the R16.8 tree as a whole: the spec names
// testdata/api/clusters.json for Phase 1a, and every file that lands there
// (Phase 5 adds queries.json, instances.json, etc.) must be valid JSON so a
// broken fixture fails here rather than in the SPA's Vitest run.
func TestFixtureDirectory(t *testing.T) {
	dir := filepath.Dir(goldenPath)
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	if len(matches) == 0 {
		t.Fatalf("no fixtures in %s (R16.8)", dir)
	}
	var want = map[string]bool{goldenPath: false}
	for _, m := range matches {
		if _, err := os.Stat(m); err != nil {
			t.Errorf("stat %s: %v", m, err)
			continue
		}
		raw, err := os.ReadFile(m)
		if err != nil {
			t.Errorf("read %s: %v", m, err)
			continue
		}
		if !json.Valid(raw) {
			t.Errorf("%s is not valid JSON", m)
		}
		for k := range want {
			if m == k {
				want[k] = true
			}
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("%s missing (R16.8 names it as the Phase 1a fixture)", path)
		}
	}
}
