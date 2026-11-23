// sink.go is the SQLite half of collector.Sink — §3.4:218's `--store=sqlite`
// answer to the same interface A1's collector/sink.go ships as
// §3.4:219's `--store=memory` MemSink.
//
// The interface exists so the scheduler has one thing to call and the two storage
// profiles are a swap rather than a scheduler change (collector/sink.go says it,
// and "the SQLite half is Phase 3's own commit and the scheduler's call site does
// not move when it lands" is the sentence that makes this file a requirement
// rather than an option). So the methods are exactly collector.Sink's two, the
// key is exactly its sinkKey's triple, and nothing here is a third storage API
// the scheduler would have to learn.
//
// # Why the payload is a gob blob
//
// schema.go's sink_current declares `payload BLOB NOT NULL`, and a blob needs an
// encoding. gob, not JSON, for three reasons that are each checkable rather than
// stylistic:
//
//   - The payload is Go-native data that never crosses a language boundary. The
//     JSON the product emits is R9.x's contract, rendered by internal/api from
//     DTOs; storing a sample set as JSON would make the storage format a second
//     copy of that contract, and the first place they disagree (a field renamed,
//     an omitempty added, a float formatted differently) is a store that cannot
//     read its own history.
//   - time.Time round-trips exactly. collector.Sample carries three of them and
//     R13.11's whole point is that two of them are compared to the second; JSON
//     drops the monotonic reading, gob's time.Time encoder keeps it, so Skew()
//     after a read measures what it measured before the write.
//   - Size. R13.5's ceiling is DefaultMaxStoreBytes (64 MiB) and the store's own
//     file budget is DefaultMaxFileBytes; gob writes each field name once per
//     type per stream instead of repeating "metric"/"labels"/"value" per sample,
//     which on a 500-sample round is the difference between the payload and the
//     ceiling being a non-question and a question.
//
// The payload is wrapped in sinkPayload rather than encoding the bare slice so
// the blob has a named shape to grow into (a captured_at alongside the samples,
// say) without every reader having to guess whether a blob is a slice or a struct.
//
// # Where the size ceiling moves to
//
// MemSink enforces R13.5's ceiling inside Publish, evicting whole buckets
// oldest-first. This Publish does not measure anything, because a SQLite file's
// size is not the sum of its rows — WAL bytes, the freelist and page overhead all
// count toward the file and none of them appear in a row — so a byte count taken
// at publish time enforces a number that is not the thing R13.5 caps. The cap
// lives in retention.go's Purge, which measures the file with os.Stat (A3's S33).
package store

import (
	"bytes"
	"encoding/gob"
	"fmt"

	"github.com/frankellis/pgsql-insights/internal/collector"
)

// The store implements the collector's sink contract, asserted rather than
// implied: a Go method no interface names is never checked, and this is the file
// where a signature drift would be invisible (detail.go gives the full argument).
var _ collector.Sink = (*Store)(nil)

// sinkPayload is what one sink_current row's blob holds.
//
// The wrapper exists so the blob has a name and a shape to extend. Encoding
// []collector.Sample directly would work today and would need every reader to
// know which of the two encodings a given row used the first time a second field
// is added to it.
type sinkPayload struct {
	Samples []collector.Sample
}

// upsertSinkSQL writes one (namespace, cluster, pod, collector) row.
//
// INSERT OR REPLACE is the interface's own word — "Publish replaces the stored
// sample set for (tgt, collector) with samples" — and the replace is what makes
// the write atomic at the granularity the interface promises. MemSink makes the
// same promise structurally ("eviction granularity is a whole (target, collector)
// sample set, never a subset of one"); here it is one statement, so a failure
// mid-write leaves the previous round's blob in place rather than half a slice.
const upsertSinkSQL = `
	INSERT OR REPLACE INTO sink_current
		(namespace, cluster, pod, collector, payload, ingested_at)
	VALUES (?, ?, ?, ?, ?, ?)`

// selectSinkSQL reads one row's blob back.
const selectSinkSQL = `
	SELECT payload FROM sink_current
	WHERE namespace = ? AND cluster = ? AND pod = ? AND collector = ?`

// Publish implements collector.Sink: it replaces the stored sample set for
// (tgt, name).
//
// The interface names its second parameter `collector`, which in this file would
// shadow the package the first parameter's type comes from; the implementation
// calls it `name`. Parameter names are not part of an interface, so the two
// spellings coexist.
//
// An empty sample set is stored, not skipped. R2.2's last-good rule is enforced by
// the scheduler not calling Publish on a failed round, so an empty set here means a
// collector that succeeded and genuinely produced nothing — which is a fact worth
// recording, and the difference between "this round was empty" and "we have never
// run this collector" that R3.14's answer-not-available-vs-zero rule is about.
func (s *Store) Publish(tgt collector.Target, name string, samples []collector.Sample) error {
	switch {
	case name == "":
		return fmt.Errorf("store: publish: empty collector name for %s", tgt.Key())
	case tgt.Namespace == "" || tgt.Cluster == "":
		// The PK's two required parts, each on its own (A3's S47). The message names
		// both fields, so it has to fire when either one is missing; with `&&` a
		// target that had a namespace and no cluster — exactly the half the message
		// describes — fell through and wrote a row with an empty cluster column.
		// collector.Target.Valid() requires both, and this matches it. The same
		// switch allows an empty Pod (a cluster-scoped source), and Role/Address/
		// PGMajor are not validated here because they are not columns of this table —
		// see the note below.
		return fmt.Errorf("store: publish %s: target has no namespace or cluster", name)
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(sinkPayload{Samples: samples}); err != nil {
		return fmt.Errorf("store: encode %s samples for %s: %w", name, tgt.Key(), err)
	}

	// The two columns the Target's Key() is made of, plus pod. Role, Address and
	// PGMajor are deliberately not columns: every sample in the payload carries
	// its own R9.3 label set (the scheduler fills the identity labels, per
	// collector/clock.go), so a role column beside an `instance_role` label is two
	// places one label lives — and the two drift the moment the collector's role
	// for a target changes while the stored samples' labels do not.
	_, err := s.db.Exec(upsertSinkSQL,
		tgt.Namespace, tgt.Cluster, tgt.Pod, name, buf.Bytes(), unix(s.now()))
	if err != nil {
		return fmt.Errorf("store: publish %s for %s: %w", name, tgt.Key(), err)
	}
	return nil
}

// Samples implements collector.Sink: the stored set for (tgt, name).
//
// The interface has no error return, and that is a real limit on this half of it:
// a blob that fails to decode is indistinguishable here from a row that was never
// written, so both answer empty. The mem sink has the same shape and no such case
// (its value is a Go slice, never an encoding). Flagged for the interface's owner:
// if the distinction is wanted, it belongs in collector/sink.go's signature, not
// in a second method on this one.
//
// The returned slice is always fresh — decoding produces a new one — which is the
// interface's copy rule ("It returns a copy, so a caller cannot mutate stored
// samples through the slice header") satisfied by construction rather than by an
// append([]Sample(nil), ...) the way MemSink does it.
func (s *Store) Samples(tgt collector.Target, name string) []collector.Sample {
	var blob []byte
	err := s.db.QueryRow(selectSinkSQL, tgt.Namespace, tgt.Cluster, tgt.Pod, name).Scan(&blob)
	if err != nil {
		// sql.ErrNoRows is the common case and means "never published"; any other
		// error is a read failure on a store the caller already has open. Both
		// answer empty because the signature has nowhere to put the difference.
		return nil
	}

	var p sinkPayload
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&p); err != nil {
		return nil
	}
	if p.Samples == nil {
		// An empty non-nil slice, so a caller's `len(x)` and `x != nil` agree. The
		// API's `data: []` rule, applied to the sink read.
		return []collector.Sample{}
	}
	return p.Samples
}
