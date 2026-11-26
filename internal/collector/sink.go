package collector

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// DefaultMaxStoreBytes is the memory ceiling for the in-memory sink when
// --max-store-bytes is unset.
//
// Spec gap, flagged rather than resolved silently: R13.5 requires the ceiling to
// be "configurable and enforced (`--max-store-bytes`)" but names no default, and
// R14.7's rule applies to flags as much as to metrics — an unset value needs an
// answer that is not "unlimited". 64 MiB is derived from the two numbers the spec
// does commit to: R13.2's "< 150 MB RSS baseline" for a 50-cluster poller and
// R13.6's "lab mode in < 100 MB RAM". A store that grew to 140 MB would satisfy
// R13.5 and break R13.2, so the ceiling has to leave room for the Go runtime, the
// informer caches and the SPA bundle; 64 MiB does, and it is the value the
// R11.8 config chain will default the flag to. A3 should replace this with the
// spec's own number when R13.5 gains one.
const DefaultMaxStoreBytes int64 = 64 << 20

// Sink is where a completed collection round puts what it produced.
//
// The interface exists so the scheduler has one thing to call and §3.4's two
// storage profiles (`--store=memory` ring buffers, `--store=sqlite` via
// modernc.org/sqlite) are a swap rather than a scheduler change. This commit
// ships the memory half; the SQLite half is Phase 3's own commit and the
// scheduler's call site does not move when it lands.
//
// Publish is the only write method, and that is deliberate. R2.2's
// "a failed collector leaves its last-good data served" is implemented by the
// scheduler *not calling* Publish on a failed round — there is no
// MarkStale/Restore pair to get the ordering wrong, and no way for a caller to
// half-publish. Either the round's samples replace the previous ones or nothing
// happens.
//
// "Only write method" is scoped to Sink, and two writers exist outside it:
// ProbePublisher (the settings collector's inventory, which Publish itself
// preserves rather than wiping) and EventPublisher (R5.9's timeline, which the
// ruling at `requirements.md:349` rules into being "not a fourth `Sink` method
// -- which is what A1 proposed, and is ruled yes"). Read either docblock before
// adding another — the reason each is a separate interface rather than a Sink
// method is the same reason this interface has one method.
type Sink interface {
	// Publish replaces the stored sample set for (tgt, collector) with samples.
	Publish(tgt Target, collector string, samples []Sample) error
	// Samples returns the stored set for (tgt, collector): the last-good data
	// R2.2 says is still served. It returns a copy, so a caller cannot mutate
	// stored samples through the slice header.
	Samples(tgt Target, collector string) []Sample
}

// PGStatStatements is the extension name R3.1's detection and R10.27's
// "not installed" screen are both about, as a Go symbol.
//
// Three other exported symbols carry the same spelling and none of them is this
// one: pgversion.ViewStatStatements (matrix_data.go:60) is a *view* name,
// pgversion.Schema's mapping (matrix.go:185) and sql.go's knownSchemas (:413)
// name a *schema*, and collector.CollectorStatement (statements.go:67) is
// "statement", not this string. Extension, schema and view are three distinct
// PostgreSQL namespaces that happen to share a word, so importing the view
// constant here would be a narrower lie than a new literal: it would make the
// code claim the presence of a view where R3.1 asks about the presence of an
// extension. The DTO agrees — dto.ExtensionPresenceView (api/dto/queries.go:270)
// has no extension-name field and its response field is named
// PGStatStatements, so the extension is fixed by the contract and this const is
// the only place that word is written down as a Go value.
const PGStatStatements = "pg_stat_statements"

// ProbeResult is one database's answer from a settings round: the database it
// was read from, and the extensions installed there.
//
// It exists because []Sample cannot express R3.1's tri-state. The samples the
// settings collector publishes are per-extension rows, so a database with no
// extensions installed contributes zero rows, and a database that was never
// probed also contributes zero rows — the two are indistinguishable in the
// sample set, and R3.1's rule is that they get different answers (an unprobed
// database must leave `installed` off the response entirely; only an explicit
// false may render R10.27's "not installed" screen). The probed-database list is
// therefore a fact the store has to keep alongside the samples, not something a
// reader can infer from them.
//
// Extensions is every extension installed in Database, not just
// PGStatStatements: the collector's probe query is the whole pg_extension
// catalogue (R3.1's detection covers the catalogue, and the collector already
// samples every row of it), so narrowing the stored type to one extension would
// throw away data the collector already has in order to match the one DTO that
// renders a subset of it. Consumers pick the extension they want — the queries
// route filters on PGStatStatements above.
type ProbeResult struct {
	// Database is the pg_database name this row was read from. It is the field
	// that makes the row mean "this database was probed", which is the half
	// Samples cannot carry.
	Database string
	// Extensions are the extname values installed in Database. An empty slice is
	// a real answer — probed, nothing installed. The stored copy normalises it to
	// nil (append to a nil slice with no elements yields nil), and that is fine:
	// the answer to "was it probed" lives in Probes's ok, not in nil-versus-empty,
	// so nothing reads the difference.
	Extensions []string
}

// ProbeReader is the read half of the extension-probe inventory.
//
// It is a separate interface rather than a method on Sink for the reason Sink's
// own docblock gives for having one write method: adding it to Sink would force
// every implementation — including the Phase 3 SQLite store, which has no probe
// table — to grow a method it cannot answer. The store satisfies Sink and
// R9.5's RenderPerQuery without satisfying this, and api's accessor turns that
// into its documented empty source rather than a compile error.
//
// ok distinguishes the two answers R3.1 needs: ok=false means no settings round
// has ever published to this (target, collector) bucket, so nothing is known and
// the DTO must omit `installed`; ok=true with a zero-length slice means a round
// ran and the target has no connectable databases. Collapsing them — returning
// nil for both — is the exact bug this type exists to prevent.
type ProbeReader interface {
	// Probes returns the stored inventory for (tgt, collectorName) and whether
	// one is stored at all.
	Probes(tgt Target, collectorName string) ([]ProbeResult, bool)
}

// ProbePublisher is the write half of the extension-probe inventory.
//
// The window this leaves is worth naming, because it is the cost of not widening
// Publish's signature: Publish and PublishProbes are two calls, so between them
// the bucket holds this round's samples and last round's inventory. The
// scheduler calls it on the line after Publish's error check, so on the success
// path the window is one mutex release wide, and on the samples-publish failure
// path it never opens at all (the round failed, so neither half is written). It is
// still a window — a reader that takes the lock between the two calls sees this
// round's samples with last round's inventory — and the narrower fix is a breaking
// change to Sink that the SQLite store has to take before the framework can.
// Publish
// itself preserves whatever probes are already stored, so a collector that
// publishes samples without probes (every collector except settings) cannot
// wipe an inventory it did not write.
type ProbePublisher interface {
	// PublishProbes replaces the stored inventory for (tgt, collectorName),
	// leaving that bucket's samples intact.
	PublishProbes(tgt Target, collectorName string, probes []ProbeResult) error
}

// EventClass is the closed set of signal classes R5.9 names for the events
// timeline, in the same shape SourceID is: the spec enumerates the members, so
// the code lists them rather than inventing a wider vocabulary.
//
// The five are the row's own list, in the row's own order — "phase transitions
// from S1, Kubernetes events, fencing changes (`cnpg.io/fencedInstances`
// annotation), promotions (`currentPrimaryTimestamp`), and backup outcomes"
// (`requirements.md:349`) — and each const names the source its class is read
// from. That mapping lives here rather than in a Source field on Event so it is
// written once: a per-event Source would be a second copy of a fact the class
// already fixes, and the only thing a second copy can add is disagreement.
type EventClass string

const (
	// EventPhaseTransition is a cluster or instance moving between phases. Read
	// from S1 (SourceClusterStatus), which is where R5.9 sources "phase
	// transitions from S1".
	EventPhaseTransition EventClass = "phase-transition"
	// EventK8sEvent is one `events` API resource in a cluster's namespace: the
	// S11 (SourceK8sEvents) payload, and the reason that id exists at all.
	EventK8sEvent EventClass = "k8s-event"
	// EventFencing is a change to the cnpg.io/fencedInstances annotation, which
	// names the instances CNPG has fenced out of a role. The annotation is a
	// field of the Cluster CR, so this class is read from S1 like any other.
	EventFencing EventClass = "fencing"
	// EventPromotion is an instance becoming the primary, detected from S4's
	// instancesReportedState.currentPrimaryTimestamp — the field the row names.
	EventPromotion EventClass = "promotion"
	// EventBackup is a backup's outcome: the Cluster CR's backup status and S4's
	// lastFailedWAL, which together are what the row means by "backup outcomes".
	EventBackup EventClass = "backup"
)

// ValidEventClass reports whether class is one of the five. Same argument as
// ValidSource's: an unvalidated string in a rendered timeline becomes a category
// the spec never declared, and R5.9's vocabulary is closed for the same reason
// R2.1's source list is.
func ValidEventClass(class EventClass) bool {
	switch class {
	case EventPhaseTransition, EventK8sEvent, EventFencing, EventPromotion, EventBackup:
		return true
	}
	return false
}

// Event is one entry on the R5.9 events timeline.
//
// It is not a Sample, and the difference is why R5.9's ruling refuses to express
// it as one. A Sample is "this metric had this value at this instant", stored per
// (target, collector) as a set the next round replaces; an Event is "something
// happened", and the timeline the row asks for is the accumulation of those across
// rounds. That is also why the class set is an enum rather than a metric-name
// string: a new metric name is a new data point, while a new event class is a new
// signal category the spec has to declare — the R2.1/SourceID rule, applied to
// R5.9's vocabulary.
//
// Reason and Detail are the machine name and the human text, and both exist for
// the reason Sample.Metric and a log message both do: Reason is stable and
// filterable (a Kubernetes event's `.reason`, a Cluster CR's `phase`,
// "fenced"/"unfenced"), Detail is free text a UI renders but never matches on
// (the event's `.message`, a CR's `phaseReason`, a backup's error). Folding the
// human text into Reason is the bug that makes a filter unwriteable; folding the
// machine name into Detail is the bug that leaves a UI rendering an empty cell.
type Event struct {
	// Class is which of the five R5.9 signal classes this is.
	Class EventClass
	// At is when the thing happened, not when it was observed: a Kubernetes
	// event's eventTime/lastTimestamp, a CR's transition timestamp, a promotion's
	// currentPrimaryTimestamp. It is the field that makes a set of events a
	// timeline, and a zero At sorts the entry to the Unix epoch rather than to
	// "now" — so a collector that cannot read a real time omits the event instead
	// of stamping it, which is the same not-known rule ProbeResult's ok carries
	// for the probe inventory.
	At time.Time
	// Reason is the machine-readable name, stable enough to filter on.
	Reason string
	// Subject is what the event is about: the pod a fencing change fenced, the new
	// primary on a promotion, the database a backup ran for. Empty means the
	// cluster itself, which is the right answer for a cluster-level phase
	// transition and the wrong one for a per-instance event — the field exists so
	// the two stay distinguishable.
	Subject string
	// Detail is the human-readable text, rendered and never matched on.
	Detail string
}

// EventPublisher is the write half of the R5.9 timeline, and the third writer
// Sink's docblock tells a reader to check before adding.
//
// It is a separate interface rather than a fourth Sink method — the ruling says
// so in terms ("`Events []Event` on `Result` plus an `EventPublisher` beside
// `ProbePublisher`, not a fourth `Sink` method"), and the reason is ProbePublisher's
// own: adding it to Sink would force every implementation, including the Phase 3
// SQLite store, to grow a method it has no table to answer from. The store
// satisfies Sink and R9.5's RenderPerQuery without satisfying this, and a caller
// type-asserts.
//
// PublishEvents replaces, which is the one place it departs from the timeline
// metaphor and it is worth naming why. Publish and PublishProbes replace because
// samples and inventories are current-state snapshots. A timeline is not: the
// full history is the artefact, and a store that replaces can only ever serve one
// round's worth of it. The alternative — the store appending, with a cap and an
// identity to dedup on — needs an event id and a retention window that R5.9's row
// does not name and that the ruling did not rule, so it is Phase 8's decision to
// make alongside the collector that produces the events. Until then the
// k8s-events collector owns its own window and reports the whole of it each round,
// exactly as the settings collector reports the whole inventory it holds, and the
// store serves the last-good window.
type EventPublisher interface {
	// PublishEvents replaces the stored events for (tgt, collectorName), leaving
	// that bucket's samples and probes intact.
	PublishEvents(tgt Target, collectorName string, events []Event) error
}

// sinkKey is the storage key for one (target, collector) pair. The separator is
// NUL because the fields above it are DNS labels and a collector name, none of
// which may contain it, so no two distinct triples can produce the same key —
// the same argument R9.3 makes for (namespace, cluster, pod) being a stable
// identity.
func sinkKey(tgt Target, collector string) string {
	return tgt.Key() + "\x00" + collector
}

// MemSink is §3.4's `--store=memory` profile: recent samples held in a map, with
// R13.5's size-based eviction.
//
// Eviction granularity is a whole (target, collector) sample set, never a
// subset of one. Serving half of a collector's rows from this round and half from
// the last is a data-integrity failure of exactly the kind R2.2's last-good rule
// exists to prevent, and it is the easy bug when the unit of storage is a slice
// you can truncate.
//
// Every exported method begins with `if m == nil`, which is collector.Stats'
// convention taken whole (8 methods, 8 guards, metrics.go:118-244) rather than a
// new one. The reason it matters here is the ok-form assertion at
// cmd/pgsi/main.go:708: `ok` proves the dynamic type implements ProbeReader, not
// that the pointer is non-nil, so Probes has to answer (nil, false) where an
// unguarded method would panic on m.mu. A nil receiver's two write methods return
// a nil error — storing nothing and reporting success, the answer
// internal/store/perquery.go:278's RenderPerQuery gives with ("", nil) — because
// the alternative is the package's first error value for a sink that cannot fail
// for any other reason. putLocked is the one method without the guard: it is
// unexported and both callers are already guarded, so a third check there is
// unreachable code.
type MemSink struct {
	mu       sync.RWMutex
	maxBytes int64
	used     int64
	byKey    map[string]*memBucket
	// order is every live key, oldest-published first. A map alone cannot answer
	// "what is oldest", and R13.5 says eviction is *size-based*, which requires
	// knowing what to evict — not merely that something must go.
	order []string
}

// memBucket is one (target, collector) storage slot and the bytes it accounts
// for.
//
// probes is the settings collector's inventory (see ProbeResult) and events is
// the R5.9 timeline (see Event). A collector's bucket leaves the two it does not
// own nil, and bytes accounts for all three halves so the one subtraction on
// eviction removes exactly what was written — a bucket whose probes were
// unaccounted would drift from the R13.5 ceiling by however much inventory the
// settings collector carries, and the same argument now applies one half wider.
type memBucket struct {
	samples []Sample
	probes  []ProbeResult
	events  []Event
	bytes   int64
}

// NewMemSink returns a sink with the given R13.5 ceiling. maxBytes <= 0 selects
// DefaultMaxStoreBytes.
func NewMemSink(maxBytes int64) *MemSink {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxStoreBytes
	}
	return &MemSink{
		maxBytes: maxBytes,
		byKey:    map[string]*memBucket{},
	}
}

// Publish replaces the stored set for (tgt, collector) and evicts oldest-first
// until the total is back under the ceiling.
//
// The just-published key is never evicted, even when it alone exceeds maxBytes.
// The alternative is a collector whose result set is larger than the whole store
// publishing samples that vanish in the same call, which makes Publish a silent
// no-op and makes the ceiling look broken when it is the configuration that is.
// The cap is therefore an approximate ceiling enforced at bucket granularity —
// which is what "size-based eviction rather than OOM" (R13.5) is describing: the
// guarantee is bounded memory, not an exact byte count.
//
// It replaces the samples half of the bucket and carries the probes half forward
// unchanged, which is what lets a collector that publishes no inventory (every
// collector but settings) write samples without wiping the inventory the settings
// collector wrote. Before that rule existed the two write methods each clobbered
// the other's half, and the scheduler's call order decided whether the queries
// route could see the fleet.
func (m *MemSink) Publish(tgt Target, collector string, samples []Sample) error {
	if m == nil {
		return nil
	}
	key := sinkKey(tgt, collector)

	m.mu.Lock()
	defer m.mu.Unlock()

	var probes []ProbeResult
	var events []Event
	if old, ok := m.byKey[key]; ok {
		probes = old.probes
		events = old.events
	}
	m.putLocked(key, samples, probes, events)
	return nil
}

// PublishProbes replaces the stored inventory for (tgt, collectorName) and leaves
// that bucket's samples intact — the mirror image of Publish, and the
// ProbePublisher half of the pair. See ProbePublisher for why this is a second
// method rather than a wider Publish signature, and Publish's docblock for the
// preservation rule that makes the two safe in either order.
func (m *MemSink) PublishProbes(tgt Target, collectorName string, probes []ProbeResult) error {
	if m == nil {
		return nil
	}
	key := sinkKey(tgt, collectorName)

	m.mu.Lock()
	defer m.mu.Unlock()

	var samples []Sample
	var events []Event
	if old, ok := m.byKey[key]; ok {
		samples = old.samples
		events = old.events
	}
	m.putLocked(key, samples, probes, events)
	return nil
}

// PublishEvents replaces the stored events for (tgt, collectorName) and leaves
// that bucket's samples and probes intact — the third writer, and the
// EventPublisher half of the set. See EventPublisher for why it is a separate
// interface rather than a Sink method, and for the replace-versus-append question
// this method's signature settles one way.
func (m *MemSink) PublishEvents(tgt Target, collectorName string, events []Event) error {
	if m == nil {
		return nil
	}
	key := sinkKey(tgt, collectorName)

	m.mu.Lock()
	defer m.mu.Unlock()

	var samples []Sample
	var probes []ProbeResult
	if old, ok := m.byKey[key]; ok {
		samples = old.samples
		probes = old.probes
	}
	m.putLocked(key, samples, probes, events)
	return nil
}

// Events returns the stored timeline for (tgt, collectorName) and whether one is
// stored at all. It is a method on MemSink rather than a named interface because
// R5.9's ruling names only the publisher; the read interface is Phase 8's to
// define alongside the route that renders the timeline. The copy is the same rule
// Samples' and Probes' are: a caller that sorts the result must not reach the
// stored backing array through the shared slice header.
func (m *MemSink) Events(tgt Target, collectorName string) ([]Event, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.byKey[sinkKey(tgt, collectorName)]
	if !ok {
		return nil, false
	}
	return append([]Event(nil), b.events...), true
}

// putLocked writes one bucket and re-enforces the ceiling. Both write methods
// route through it so the eviction rules — the just-written key is never evicted,
// the order list is rebuilt rather than spliced — exist in one place and cannot
// be written differently by the second writer, which is the failure mode a second
// Publish method normally introduces. The caller holds m.mu.
func (m *MemSink) putLocked(key string, samples []Sample, probes []ProbeResult, events []Event) {
	if old, ok := m.byKey[key]; ok {
		m.used -= old.bytes
		delete(m.byKey, key)
	}
	b := &memBucket{
		samples: append([]Sample(nil), samples...),
		probes:  append([]ProbeResult(nil), probes...),
		events:  append([]Event(nil), events...),
		bytes:   samplesSize(samples) + probesSize(probes) + eventsSize(events),
	}
	m.byKey[key] = b
	m.used += b.bytes

	// Rebuild the order list: every surviving key oldest-first, with the new key
	// last (most recently published). Rebuilding rather than splicing is
	// deliberate — deleting from a slice while iterating it with i++ skips the
	// following element every time, which is how an already-evicted key survives
	// in the order list and has its bytes decremented twice on a later pass.
	order := make([]string, 0, len(m.byKey))
	for _, k := range m.order {
		if k != key {
			if _, ok := m.byKey[k]; ok {
				order = append(order, k)
			}
		}
	}
	order = append(order, key)

	// Evict oldest-first until under the cap. `len(order) > 1` is what guarantees
	// the bucket just written survives: if it alone exceeds maxBytes, the
	// smallest storable unit is already over the ceiling and the loop stops with
	// that one bucket rather than emptying the store.
	for len(order) > 1 && m.used > m.maxBytes {
		oldest := order[0]
		m.used -= m.byKey[oldest].bytes
		delete(m.byKey, oldest)
		order = order[1:]
	}
	m.order = order
}

// Samples returns the stored set for (tgt, collector), or nil if nothing is
// stored. The copy is not paranoia: a caller that sorts or truncates the returned
// slice would otherwise mutate the store's own backing array through the shared
// slice header.
func (m *MemSink) Samples(tgt Target, collector string) []Sample {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.byKey[sinkKey(tgt, collector)]
	if !ok {
		return nil
	}
	return append([]Sample(nil), b.samples...)
}

// Probes returns the stored inventory for (tgt, collectorName) and whether one is
// stored at all — the ProbeReader half, and the read side of the ok semantics
// ProbeResult documents. The copy is the same rule Samples' is: a caller that
// sorts the result must not reach the stored backing array through the shared
// slice header.
//
// The ok is about a bucket existing, which is only the same fact as "a settings
// round ran" when the name is the one that owns an inventory. Ask with any other
// collector name after that collector has published samples and you get
// (nil, true) — a bucket with no inventory — which a reader must not read as
// "probed, nothing installed". Callers ask with CollectorSettings. The interface
// cannot enforce that — collectorName is a plain parameter — which is the same
// pairing weakness this type replaced the `NewSamplePresenceSource(sink,
// probed []string)` fallback over; it is narrower here only because the two
// arguments come from one Target rather than from two lists a caller keeps in
// agreement by hand.
func (m *MemSink) Probes(tgt Target, collectorName string) ([]ProbeResult, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.byKey[sinkKey(tgt, collectorName)]
	if !ok {
		return nil, false
	}
	return append([]ProbeResult(nil), b.probes...), true
}

// Keys returns every stored (target, collector) key, sorted. Test and debug
// surface: it is what lets a test assert which buckets survived eviction without
// reaching into an unexported field, and it is the same list an R13.4 cardinality
// assertion would count.
func (m *MemSink) Keys() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.byKey))
	for k := range m.byKey {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Used returns the bytes currently accounted for.
func (m *MemSink) Used() int64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.used
}

// MaxBytes returns the configured ceiling.
func (m *MemSink) MaxBytes() int64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxBytes
}

// samplesSize estimates the bytes a sample set occupies.
//
// It is an estimate, and saying so is the honest version of "size-based
// eviction": an exact figure would require the store to own the samples'
// allocation, which a Go map of slices does not. The formula counts what a
// Sample actually holds — the metric string, each label pair, the 8-byte float,
// and the three time.Time fields at 24 bytes each — plus the 16-byte string
// header and the 8-byte slice element, and 32 bytes per label pair for the map
// slot. These are the standard sizes rather than a profiler measurement, so the
// ceiling is enforced to within a few percent and is not presented as an exact
// count.
func samplesSize(samples []Sample) int64 {
	var n int64
	for _, s := range samples {
		n += int64(len(s.Metric)) + 16 + 8 + 3*24 + 8
		for k, v := range s.Labels {
			n += int64(len(k)) + int64(len(v)) + 32
		}
	}
	return n
}

// probesSize estimates the bytes a probe inventory occupies, and carries the same
// estimate-not-measurement caveat samplesSize documents — the standard sizes of
// what a ProbeResult holds, not a profiler figure.
//
// Per element it counts 16 (Database's string header) + 24 (Extensions' slice
// header) + 8 (the slot this element occupies in the outer slice's backing
// array) + the bytes of Database, then 16 plus the bytes for every extension name
// under it. It mirrors samplesSize's accounting rather than inventing a second
// convention: both count string headers and slice slots rather than asking the Go
// allocator, and both are there to keep the R13.5 ceiling honest to within a few
// percent, not to report an exact byte count.
func probesSize(probes []ProbeResult) int64 {
	var n int64
	for _, p := range probes {
		n += 16 + 24 + 8 + int64(len(p.Database))
		for _, e := range p.Extensions {
			n += int64(len(e)) + 16
		}
	}
	return n
}

// eventsSize estimates the bytes an event set occupies, and carries the same
// estimate-not-measurement caveat samplesSize and probesSize document — the
// standard sizes of what an Event holds, not a profiler figure.
//
// Per element it counts 8 (the slot this element occupies in the outer slice's
// backing array) + 24 (At, a time.Time) + 16 for each of the four string
// headers (Class, Reason, Subject, Detail) + the bytes of each. It mirrors
// probesSize's accounting rather than inventing a third convention: all three
// count string headers and slice slots rather than asking the Go allocator, and
// all three exist to keep the R13.5 ceiling honest to within a few percent.
func eventsSize(events []Event) int64 {
	var n int64
	for _, e := range events {
		n += 8 + 24 + 4*16 + int64(len(e.Class)+len(e.Reason)+len(e.Subject)+len(e.Detail))
	}
	return n
}

// String renders the sink's occupancy for a debug log line.
func (m *MemSink) String() string {
	if m == nil {
		return "MemSink(nil)"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fmt.Sprintf("MemSink{used=%d max=%d buckets=%d}", m.used, m.maxBytes, len(m.byKey))
}
