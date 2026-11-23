package collector

import (
	"fmt"
	"sort"
	"sync"
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
type Sink interface {
	// Publish replaces the stored sample set for (tgt, collector) with samples.
	Publish(tgt Target, collector string, samples []Sample) error
	// Samples returns the stored set for (tgt, collector): the last-good data
	// R2.2 says is still served. It returns a copy, so a caller cannot mutate
	// stored samples through the slice header.
	Samples(tgt Target, collector string) []Sample
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

// memBucket is one (target, collector) sample set and the bytes it accounts for.
type memBucket struct {
	samples []Sample
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
func (m *MemSink) Publish(tgt Target, collector string, samples []Sample) error {
	key := sinkKey(tgt, collector)

	m.mu.Lock()
	defer m.mu.Unlock()

	if old, ok := m.byKey[key]; ok {
		m.used -= old.bytes
		delete(m.byKey, key)
	}
	b := &memBucket{
		samples: append([]Sample(nil), samples...),
		bytes:   samplesSize(samples),
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
	return nil
}

// Samples returns the stored set for (tgt, collector), or nil if nothing is
// stored. The copy is not paranoia: a caller that sorts or truncates the returned
// slice would otherwise mutate the store's own backing array through the shared
// slice header.
func (m *MemSink) Samples(tgt Target, collector string) []Sample {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.byKey[sinkKey(tgt, collector)]
	if !ok {
		return nil
	}
	return append([]Sample(nil), b.samples...)
}

// Keys returns every stored (target, collector) key, sorted. Test and debug
// surface: it is what lets a test assert which buckets survived eviction without
// reaching into an unexported field, and it is the same list an R13.4 cardinality
// assertion would count.
func (m *MemSink) Keys() []string {
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.used
}

// MaxBytes returns the configured ceiling.
func (m *MemSink) MaxBytes() int64 {
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

// String renders the sink's occupancy for a debug log line.
func (m *MemSink) String() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fmt.Sprintf("MemSink{used=%d max=%d buckets=%d}", m.used, m.maxBytes, len(m.byKey))
}
