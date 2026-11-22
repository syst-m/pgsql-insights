package discovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// R1.1 (verbatim): "Discover Cluster objects in all namespaces visible to the
// configured RBAC, via a watch with a 30 s resync; support --namespaces=a,b,c
// and --exclude-namespaces allow/deny lists."
//
// This file is the watch/cache half of the package. The conversion from cached
// objects to dto.ClusterView is in build.go, the CRD field readers in
// fields.go, and the Pooler rules in poolers.go — so the requirement that is
// API-critical (the response shape) never depends on the informer plumbing.
//
// Cluster and Pooler come from dynamic informers (their CRDs have no generated
// types here — see fields.go); pods, services, PVCs and secrets come from the
// typed factory. Both are started per namespace, because R1.1's
// --namespaces=a,b,c needs one informer set per namespace: a shared factory is
// scoped to exactly one namespace or to the whole cluster, never to a named
// subset.

// The CNPG CRDs watched (R1.1 clusters, R1.4 poolers).
var (
	// ClusterGVR is postgresql.cnpg.io/v1 Cluster.
	ClusterGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	// PoolerGVR is postgresql.cnpg.io/v1 Pooler (R1.4).
	PoolerGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "poolers"}
)

// DefaultResync is R1.1's resync interval: 30 seconds.
const DefaultResync = 30 * time.Second

// DefaultStaleGrace is R1.9's grace: a deleted cluster's series are labelled
// stale for 15 minutes rather than dropped with the cluster.
const DefaultStaleGrace = 15 * time.Minute

// eventCoalesce is how long a burst of watch events is collected before the
// inventory is rebuilt. A rolling restart fires one event per pod; rebuilding
// per event would rebuild a 4-instance cluster's view four times inside a
// second. The informer resync (R1.1's 30 s) is the outer bound.
const eventCoalesce = time.Second

// Config is the discovery flag set (R1.1, R1.8, R1.9).
type Config struct {
	// Namespaces is --namespaces=a,b,c (R1.1's allow list). Empty means every
	// namespace the configured RBAC can see, which is what R1.1 asks for.
	Namespaces []string
	// ExcludeNamespaces is --exclude-namespaces (R1.1's deny list). It applies
	// to the allow list when one is given, and to the RBAC-visible set when the
	// allow list is empty.
	ExcludeNamespaces []string
	// Resync is the informer resync period; zero picks DefaultResync (R1.1).
	Resync time.Duration
	// StaleGrace is R1.9's post-deletion grace; zero picks DefaultStaleGrace.
	StaleGrace time.Duration
	// Credentials is R1.8's configured credential set, resolved per cluster
	// into the DTO's credentialSource.
	Credentials CredentialsConfig
}

// CredentialsConfig is R1.8's configured credential inputs.
type CredentialsConfig struct {
	// Secret is --credential-secret: "namespace/name", or a bare name meaning
	// "in each cluster's own namespace". Its presence outranks the other
	// sources (R1.8's first level).
	Secret string
	// Username, Password and DBName are the single configured credential
	// (R1.8's third level) — the pre-multi-cluster shape, kept working.
	Username string
	Password string
	DBName   string
}

// ResolveCredential implements R1.8's precedence for one cluster and returns
// the dto.Credential* value the API reports.
//
// The four levels, in order:
//  1. --credential-secret (dto.CredentialFlag) — an explicit override, used
//     whenever it is set and the secret exists.
//  2. <cluster>-app (dto.CredentialCluster) — CNPG's own convention for the
//     app user secret, with keys username/password/dbname.
//  3. The single configured credential (dto.CredentialConfig) — a username set
//     without any secret.
//  4. None (dto.CredentialNone) — R1.6's zero-prerequisite case: the cluster
//     still renders, and the SQL collectors report why they cannot connect.
//
// secretExists is injected rather than read through the clientset so the
// precedence is testable without a cluster. Discoverer passes a probe backed by
// the secret informer; tests pass a set literal.
func ResolveCredential(cluster *unstructured.Unstructured, creds CredentialsConfig, secretExists func(namespace, name string) bool) string {
	if cluster == nil {
		return dto.CredentialNone
	}
	ns, name := cluster.GetNamespace(), cluster.GetName()
	// exists tolerates a nil probe: with no way to check, an explicit
	// --credential-secret is still the configured answer (R1.8's first level is
	// about precedence, not existence), while <cluster>-app cannot be confirmed
	// and so falls through to the next level.
	exists := func(n string) bool {
		return secretExists != nil && secretExists(ns, n)
	}

	// 1. --credential-secret.
	if creds.Secret != "" {
		// "ns/name" addresses a secret in another namespace; a bare name is
		// resolved in the cluster's own namespace.
		want := creds.Secret
		if i := strings.IndexByte(want, '/'); i >= 0 {
			if want[:i] != ns {
				// The flag points at another namespace, so the cluster's own
				// secrets cannot confirm it. It is still the answer when set,
				// because R1.8 ranks it above every other source.
				return dto.CredentialFlag
			}
			want = want[i+1:]
		}
		if want == "" {
			return dto.CredentialNone
		}
		if secretExists == nil || exists(want) {
			return dto.CredentialFlag
		}
		// The named secret does not exist. R1.8's chain continues rather than
		// erroring, so the cluster renders and the SQL collector reports the
		// missing secret.
	}
	// 2. <cluster>-app, CNPG's app-user secret convention.
	if name != "" && exists(name+"-app") {
		return dto.CredentialCluster
	}
	// 3. The single configured credential.
	if creds.Username != "" {
		return dto.CredentialConfig
	}
	// 4. None.
	return dto.CredentialNone
}

// SelectNamespaces applies R1.1's allow/deny lists to a set of namespaces.
//
// With an allow list, the result is the allow list minus the deny list
// (duplicates dropped). Without one, the result is `all` minus the deny list.
// An entry in the deny list that is not in the allow list is simply not
// selected; that is the whole semantics, so the function is pure and R1.1's
// flag pair is accepted with one table of cases.
func SelectNamespaces(all []string, cfg Config) []string {
	deny := map[string]bool{}
	for _, n := range cfg.ExcludeNamespaces {
		if n = strings.TrimSpace(n); n != "" {
			deny[n] = true
		}
	}
	src := all
	if len(cfg.Namespaces) > 0 {
		src = cfg.Namespaces
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(src))
	for _, n := range src {
		n = strings.TrimSpace(n)
		if n == "" || deny[n] || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Inventory is one snapshot of the fleet: what discovery currently believes,
// plus the two partial-render fields R8.4 requires (meta.failed and
// meta.loading) so a response that could not read every namespace still
// renders the ones it could.
type Inventory struct {
	Clusters []dto.ClusterView
	// Failed lists namespaces whose read errored (R8.4). A list from three good
	// namespaces plus one forbidden namespace is a valid response, not an error
	// response.
	Failed []dto.NamespaceFailure
	// Loading lists namespaces whose informers have not synced yet (R8.4).
	Loading []string
	// GeneratedAt is when this snapshot was built (R8.4's meta.generatedAt).
	GeneratedAt time.Time
}

// Discoverer watches the cluster and reports the inventory the API serves.
//
// It is the single owner of cluster state: the API handler reads Inventory(),
// /readyz reads Ready() (R8.7's "true once discovery has produced at least one
// inventory sync"), and Phase 2's collector manager reads the same snapshot to
// decide which collectors to run (R1.9's "drop collectors within one resync").
type Discoverer struct {
	cfg Config
	log *slog.Logger
	// cs and dyn are the two clients the informers need, and nothing else:
	// discovery lists and watches objects, it never proxies a pod's HTTP port
	// (that is kube.Client's job, and it belongs to the collectors). Holding the
	// interfaces rather than a *kube.Client is what lets the tests drive the
	// whole package with fakes.
	cs  kubernetes.Interface
	dyn dynamic.Interface

	mu         sync.RWMutex
	clusters   map[string]dto.ClusterView // key: namespace/name
	failed     []dto.NamespaceFailure
	loading    []string
	generated  time.Time
	everSynced bool
	// versions is R1.3's per-cluster version cache, surviving across rebuilds so
	// a cluster whose pod labels vanish keeps the version SQL already answered.
	versions map[string]*PGVersion
	// deleted is R1.9's bookkeeping: cluster key -> when it disappeared, kept
	// for StaleGrace so the store can label its series stale before dropping
	// them. The cluster itself leaves `clusters` immediately.
	deleted map[string]time.Time
}

// New builds a Discoverer from a kube.Client (the shape main.go uses).
func New(cfg Config, c *kube.Client, log *slog.Logger) *Discoverer {
	if c == nil {
		return NewWithClients(cfg, nil, nil, log)
	}
	return NewWithClients(cfg, c.Clientset, c.Dynamic, log)
}

// NewWithClients builds a Discoverer from the two clients directly. This is the
// primitive: main.go reaches it through New, and the tests pass
// k8s.io/client-go/kubernetes/fake and dynamic/fake so the whole package is
// testable without a cluster. A nil logger becomes one that discards, so
// callers that have not wired R13.7's logging yet still get a usable value.
func NewWithClients(cfg Config, cs kubernetes.Interface, dyn dynamic.Interface, log *slog.Logger) *Discoverer {
	if cfg.Resync <= 0 {
		cfg.Resync = DefaultResync
	}
	if cfg.StaleGrace <= 0 {
		cfg.StaleGrace = DefaultStaleGrace
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Discoverer{
		cfg:      cfg,
		log:      log,
		cs:       cs,
		dyn:      dyn,
		clusters: map[string]dto.ClusterView{},
		versions: map[string]*PGVersion{},
		deleted:  map[string]time.Time{},
	}
}

// Config returns the effective configuration (tests assert the defaults were
// applied; Phase 1b echoes the flags from /api/v1/version).
func (d *Discoverer) Config() Config { return d.cfg }

// namespaces resolves which namespaces to watch (R1.1).
func (d *Discoverer) namespaces(ctx context.Context) ([]string, error) {
	if len(d.cfg.Namespaces) > 0 {
		// Allow list given: one informer set per namespace, minus the deny list.
		list := SelectNamespaces(nil, d.cfg)
		if len(list) == 0 {
			return nil, fmt.Errorf("--namespaces is set but every entry is excluded")
		}
		return list, nil
	}
	// No allow list: R1.1 says "all namespaces visible to the configured RBAC".
	// With a ClusterRole (R12.3's rbac.allNamespaces=true) that is
	// NamespaceAll and one informer set covers it.
	if len(d.cfg.ExcludeNamespaces) == 0 {
		return []string{metav1.NamespaceAll}, nil
	}
	// A deny list with no allow list needs the real namespace set to subtract
	// from; listing it needs namespaces:list, which R12.1's namespaced Role
	// does not grant. When the list is denied, fall back to NamespaceAll and
	// apply the deny list to the results instead.
	list, err := d.cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		d.log.Debug("namespace list unavailable, applying --exclude-namespaces to results", "err", err)
		return []string{metav1.NamespaceAll}, nil
	}
	all := make([]string, 0, len(list.Items))
	for _, n := range list.Items {
		all = append(all, n.Name)
	}
	return SelectNamespaces(all, d.cfg), nil
}

// excluded reports whether a namespace is in the deny list. It is applied to
// object namespaces during rebuild so a NamespaceAll informer still honours
// --exclude-namespaces.
func (d *Discoverer) excluded(ns string) bool {
	for _, n := range d.cfg.ExcludeNamespaces {
		if strings.TrimSpace(n) == ns {
			return true
		}
	}
	return false
}

// nsStore is one namespace's informer set: the five watches R1.5's fields come
// from, plus their listers.
type nsStore struct {
	// namespace is "" for the NamespaceAll set (no --namespaces).
	namespace string
	// informers are every watch in this set; AddEventHandler and HasSynced work
	// on each.
	informers []cache.SharedIndexInformer

	clusters cache.GenericLister // R1.1
	poolers  cache.GenericLister // R1.4
	pods     corev1listers.PodLister
	services corev1listers.ServiceLister
	secrets  corev1listers.SecretLister
	pvcs     corev1listers.PersistentVolumeClaimLister
}

// startInformers starts one informer set per namespace (R1.1's watch) and
// returns their caches. The resync period is R1.1's 30 s, passed to both the
// typed and the dynamic factories so every watch resyncs on the same clock.
func (d *Discoverer) startInformers(ctx context.Context, namespaces []string) ([]nsStore, error) {
	if d.cs == nil || d.dyn == nil {
		return nil, errors.New("discovery needs a kube client (both clientset and dynamic)")
	}
	// len(namespaces) is an upper bound — the loop skips every namespace the
	// R1.1 deny list excludes — so this allocates once instead of growing.
	out := make([]nsStore, 0, len(namespaces))
	for _, ns := range namespaces {
		if d.excluded(ns) {
			continue
		}
		opts := []informers.SharedInformerOption{}
		if ns != "" {
			opts = append(opts, informers.WithNamespace(ns))
		}
		tf := informers.NewSharedInformerFactoryWithOptions(d.cs, d.cfg.Resync, opts...)
		df := dynamicinformer.NewFilteredDynamicSharedInformerFactory(d.dyn, d.cfg.Resync, ns, nil)

		s := nsStore{
			namespace: ns,
			informers: []cache.SharedIndexInformer{
				tf.Core().V1().Pods().Informer(),
				tf.Core().V1().Services().Informer(),
				tf.Core().V1().Secrets().Informer(),
				tf.Core().V1().PersistentVolumeClaims().Informer(),
				df.ForResource(ClusterGVR).Informer(),
				df.ForResource(PoolerGVR).Informer(),
			},
			clusters: df.ForResource(ClusterGVR).Lister(),
			poolers:  df.ForResource(PoolerGVR).Lister(),
			pods:     tf.Core().V1().Pods().Lister(),
			services: tf.Core().V1().Services().Lister(),
			secrets:  tf.Core().V1().Secrets().Lister(),
			pvcs:     tf.Core().V1().PersistentVolumeClaims().Lister(),
		}
		tf.Start(ctx.Done())
		df.Start(ctx.Done())
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no namespaces to watch (check --namespaces and --exclude-namespaces)")
	}
	return out, nil
}

// nsSnapshot is one namespace's contents, read out of the caches in a single
// pass. Holding them as locals for the rest of rebuild is what makes R8.4's
// per-namespace failure accounting exact: either this namespace's read
// succeeded and every cluster in it renders, or it failed and the namespace is
// reported in meta.failed with nothing half-built.
type nsSnapshot struct {
	namespace   string
	clusters    []*unstructured.Unstructured
	poolers     []*unstructured.Unstructured
	pods        []corev1.Pod
	services    []corev1.Service
	pvcs        []corev1.PersistentVolumeClaim
	secretNames map[string]bool // "namespace/name", for R1.8's existence checks
}

// snapshot reads one namespace's caches. The listers are used instead of the
// informers' List() so typed objects come back typed (no runtime.Object
// assertions) and an empty namespace yields empty lists, not garbage.
func (d *Discoverer) snapshot(s nsStore) (nsSnapshot, error) {
	sel := labels.Everything()
	out := nsSnapshot{namespace: s.namespace, secretNames: map[string]bool{}}
	var errs []error

	if crs, err := s.clusters.List(sel); err != nil {
		errs = append(errs, fmt.Errorf("clusters: %w", err))
	} else {
		out.clusters = toUnstructured(crs)
	}
	if pl, err := s.poolers.List(sel); err != nil {
		errs = append(errs, fmt.Errorf("poolers: %w", err))
	} else {
		out.poolers = toUnstructured(pl)
	}
	if pods, err := s.pods.List(sel); err != nil {
		errs = append(errs, fmt.Errorf("pods: %w", err))
	} else {
		for _, p := range pods {
			out.pods = append(out.pods, *p)
		}
	}
	if svcs, err := s.services.List(sel); err != nil {
		errs = append(errs, fmt.Errorf("services: %w", err))
	} else {
		for _, v := range svcs {
			out.services = append(out.services, *v)
		}
	}
	if pvcs, err := s.pvcs.List(sel); err != nil {
		errs = append(errs, fmt.Errorf("persistentvolumeclaims: %w", err))
	} else {
		for _, v := range pvcs {
			out.pvcs = append(out.pvcs, *v)
		}
	}
	if secrets, err := s.secrets.List(sel); err != nil {
		errs = append(errs, fmt.Errorf("secrets: %w", err))
	} else {
		for _, sec := range secrets {
			out.secretNames[sec.Namespace+"/"+sec.Name] = true
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s: %w", nsLabel(s.namespace), joinErrs(errs))
	}
	return out, nil
}

// secretExists answers R1.8's existence checks from the snapshot's secret set.
// It is the probe ResolveCredential takes, so the credential answer comes from
// the watched cache and costs no API call per cluster.
func (s nsSnapshot) secretExists(namespace, name string) bool {
	if s.namespace != "" && namespace != s.namespace {
		return false
	}
	return s.secretNames[namespace+"/"+name]
}

// toUnstructured narrows the generic lister's []runtime.Object to the typed
// slice the builders take. The dynamic client always yields
// *unstructured.Unstructured, so the converter branch exists for the one case
// where it does not: a fake seeded with typed objects, and any future client
// that decodes into a registered type.
func toUnstructured(objs []runtime.Object) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		if u, ok := o.(*unstructured.Unstructured); ok {
			out = append(out, u)
			continue
		}
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: m})
	}
	return out
}

// nsLabel names a namespace in a log line, spelling out the empty string so a
// NamespaceAll failure does not read as "namespace ”".
func nsLabel(ns string) string {
	if ns == "" {
		return "all namespaces"
	}
	return ns
}

// joinErrs renders the lister errors as one message.
func joinErrs(errs []error) error {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return errors.New(strings.Join(msgs, "; "))
}

// Run starts the watches, builds the first inventory, and rebuilds on every
// watch event and on every resync until ctx is cancelled (R1.1's watch,
// R13.8's shutdown: Run returns as soon as ctx is done, and the last rebuild
// has already happened).
//
// It blocks until ctx is cancelled, so main.go runs it in a goroutine and the
// API's readiness gate uses WaitReady.
func (d *Discoverer) Run(ctx context.Context) error {
	namespaces, err := d.namespaces(ctx)
	if err != nil {
		return fmt.Errorf("resolve namespaces: %w", err)
	}
	st, err := d.startInformers(ctx, namespaces)
	if err != nil {
		return err
	}
	// Watch events coalesce into this channel; the loop rebuilds at most once
	// per eventCoalesce, so a rolling restart costs one rebuild, not one per pod.
	changed := make(chan struct{}, 1)
	d.registerHandlers(st, changed)

	if !cache.WaitForCacheSync(ctx.Done(), flatten(st)...) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("discovery: informer caches did not sync")
	}
	// First inventory: the moment R8.7's /readyz turns true.
	d.rebuild(st)

	tick := time.NewTicker(d.cfg.Resync)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			// The 30 s resync (R1.1). Rebuilding from the caches is cheap and
			// makes R1.9's "within one resync" exact: a cluster deleted between
			// resyncs leaves the inventory at the next one.
			d.rebuild(st)
		case <-changed:
			drain(changed, eventCoalesce)
			d.rebuild(st)
		}
	}
}

// drain coalesces a burst of watch events: it waits dur, discarding every
// signal that arrives in the window, so one rebuild follows many events.
func drain(ch chan struct{}, dur time.Duration) {
	timer := time.NewTimer(dur)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return
		case <-ch:
		}
	}
}

// registerHandlers wires each informer's AddFunc/UpdateFunc/DeleteFunc to the
// shared change channel. The handlers carry no payload — rebuild() re-reads the
// caches — which is what lets one channel coalesce events from six informers
// across N namespaces.
//
// It is a method rather than a free function so a failed registration is a log
// line: AddEventHandler only errors when the informer is already stopped, and
// an informer that never delivers events leaves a fleet that silently stops
// updating — the failure R8.7's readiness gate exists to catch, so it gets
// named rather than discarded.
func (d *Discoverer) registerHandlers(st []nsStore, changed chan struct{}) {
	nudge := func(any) {
		select {
		case changed <- struct{}{}:
		default: // already pending: one rebuild covers every pending change
		}
	}
	for _, s := range st {
		for _, inf := range s.informers {
			if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc:    nudge,
				UpdateFunc: func(_, _ any) { nudge(nil) },
				DeleteFunc: nudge,
			}); err != nil {
				d.log.Warn("informer event handler",
					"namespace", s.namespace, "error", err.Error())
			}
		}
	}
}

// flatten collects every informer's HasSynced in the set, which is what
// WaitForCacheSync takes.
func flatten(st []nsStore) []cache.InformerSynced {
	var all []cache.InformerSynced
	for _, s := range st {
		for _, inf := range s.informers {
			all = append(all, inf.HasSynced)
		}
	}
	return all
}

// WaitReady blocks until discovery has produced its first inventory (R8.7) or
// ctx is done. The API server calls this at startup so /readyz cannot answer
// true before there is anything to serve.
func (d *Discoverer) WaitReady(ctx context.Context) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if d.Ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// Ready reports whether at least one inventory has been produced — R8.7's
// readiness definition, verbatim: "true once discovery has produced at least one
// inventory sync".
func (d *Discoverer) Ready() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.everSynced
}

// LastSync is when the inventory was last rebuilt (R8.7's readiness detail).
func (d *Discoverer) LastSync() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.generated
}

// Inventory returns the current snapshot (the API handler's only input).
func (d *Discoverer) Inventory() Inventory {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := Inventory{
		Failed:      append([]dto.NamespaceFailure(nil), d.failed...),
		Loading:     append([]string(nil), d.loading...),
		GeneratedAt: d.generated,
	}
	keys := make([]string, 0, len(d.clusters))
	for k := range d.clusters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out.Clusters = make([]dto.ClusterView, 0, len(keys))
	for _, k := range keys {
		out.Clusters = append(out.Clusters, d.clusters[k])
	}
	return out
}

// StaleSince returns when a cluster disappeared, for clusters inside R1.9's
// grace window. Phase 2's store uses it to label series stale instead of
// deleting them; the cluster itself is already gone from Inventory().
func (d *Discoverer) StaleSince(key string) (time.Time, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	t, ok := d.deleted[key]
	return t, ok
}

// rebuild recomputes the whole inventory from the informer caches and applies
// R1.9's deletion bookkeeping.
func (d *Discoverer) rebuild(st []nsStore) {
	clusters := map[string]dto.ClusterView{}
	var failed []dto.NamespaceFailure
	var loading []string

	for _, s := range st {
		snap, err := d.snapshot(s)
		if err != nil {
			// R8.4's partial-render contract: the namespaces that read fine
			// still render, and this one is named in meta.failed.
			failed = append(failed, dto.NamespaceFailure{
				Namespace: nsLabel(s.namespace),
				Code:      "read_failed",
				Message:   err.Error(),
			})
			continue
		}
		for _, cr := range snap.clusters {
			if d.excluded(cr.GetNamespace()) {
				continue
			}
			key := cr.GetNamespace() + "/" + cr.GetName()
			v, err := BuildCluster(ClusterInputs{
				Cluster:          cr,
				Pods:             snap.pods,
				Services:         snap.services,
				PVCs:             snap.pvcs,
				Poolers:          snap.poolers,
				CredentialSource: ResolveCredential(cr, d.cfg.Credentials, snap.secretExists),
				Cached:           d.version(key),
			})
			if err != nil {
				failed = append(failed, dto.NamespaceFailure{
					Namespace: cr.GetNamespace(),
					Code:      "build_failed",
					Message:   fmt.Sprintf("cluster %s: %v", cr.GetName(), err),
				})
				continue
			}
			clusters[key] = v
			// R1.3's cache: keep the resolved version so a cluster whose pods
			// disappear keeps the answer SQL already gave.
			if v.PGMajor != nil && *v.PGMajor > 0 {
				d.putVersion(key, &PGVersion{Major: *v.PGMajor, Detail: v.PGVersionDetail, Source: versionSource(v)})
			}
		}
	}

	d.mu.Lock()
	now := time.Now().UTC()
	// R1.9: a cluster that vanished is stamped with the time it disappeared and
	// kept in `deleted` for StaleGrace, then forgotten. The inventory drops it
	// now (R1.9's "drop collectors within one resync").
	for key := range d.clusters {
		if _, still := clusters[key]; still {
			continue
		}
		if prev, ok := d.deleted[key]; ok && now.Sub(prev) > d.cfg.StaleGrace {
			delete(d.deleted, key)
			continue
		}
		d.deleted[key] = now
	}
	d.clusters = clusters
	d.failed = failed
	d.loading = loading
	d.generated = now
	d.everSynced = true
	d.mu.Unlock()

	d.log.Debug("discovery rebuild",
		"clusters", len(clusters), "failed", len(failed), "loading", len(loading))
}

// version reads R1.3's cached version for one cluster key.
func (d *Discoverer) version(key string) *PGVersion {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.versions[key]
}

// putVersion writes R1.3's cached version for one cluster key.
func (d *Discoverer) putVersion(key string, v *PGVersion) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.versions[key] = v
}

// versionSource recovers which source produced the DTO's pgMajor, for the cache
// entry. R1.5's field list has no source field, so it is inferred from the
// value's shape: a detail equal to the image tag means the image answered,
// otherwise the pod label did.
func versionSource(v dto.ClusterView) string {
	if v.PGVersionDetail == "" {
		return "sql"
	}
	if tag := imageTagFromRef(v.PGImage); tag == v.PGVersionDetail {
		return "image"
	}
	return "label"
}
