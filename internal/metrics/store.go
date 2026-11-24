package metrics

// R13.5's store-size family: pgsi_store_size_bytes.
//
// Sixth of R9.8's eight families, and the one whose blocker was never the
// renderer. The value exists — collector.MemSink.Used() returns the bytes the
// in-memory sample store accounts for, which is the number R13.5's
// `--max-store-bytes` ceiling is enforced against — and IMPLEMENTATION.md §3.9's
// R9.8 row filed the family as pending for exactly one reason: "the sink a
// running server writes to is not reachable from where the exposition is
// composed". This file is the renderer half of that answer; the api.Config field
// that hands a sink to the hub is A1's, and until it exists a hub built without
// WithStoreSize renders the family absent, which is what every other family with
// no source already does.
//
// The source is an interface rather than a *collector.MemSink for the reason
// PerQueryFamily is one. collector.Sink — the interface the scheduler and
// api.Config actually hold — declares Publish and Samples only, so a value typed
// as that interface cannot answer Used() at all, and the SQLite profile keeps
// its size on *store.Store, whose fileBytes is unexported. `Used() int64` is
// the method collector.MemSink already has and the one the SQLite store can grow
// into, so declaring it here costs neither package an import and keeps the hub as
// the single composition point.
//
// R9.8 names the family bare — `pgsi_store_size_bytes`, no label set, the way it
// names build_info's {version,commit} and unlike the up family's R9.3 set — so
// this renders one labelless gauge and does not invent a `store="memory"` label
// the spec does not have. The ceiling stays in the HELP text rather than becoming
// a second metric: pgsi_store_max_bytes is not one of the eight, and minting it
// here would move the count this package's header table is the authority on —
// the same D13 reasoning that made the two speculative PrometheusRule rules
// provisional rather than free additions.

import (
	"strconv"

	"github.com/frankellis/pgsql-insights/internal/collector"
)

// StoreMetricBase is the family's base name: R9.8's name minus R9.2's prefix,
// which is applied at render time so a deployment that sets metrics.prefix gets
// this family prefixed like every other one.
const StoreMetricBase = "store_size_bytes"

// StoreSizer is the family's value source: the bytes a store currently holds.
//
// It is one method and it is deliberately not collector.Sink: that interface is
// the write contract (Publish plus Samples) and it has no size method, which is
// the exact gap this family's pending status records.
type StoreSizer interface {
	Used() int64
}

// collector.MemSink — the sink a scheduler built with a nil sink writes to, and
// the one api.Config will hold once it carries a sink field — satisfies the
// source this family reads, with no adapter. The assertion is the fact, compiled:
// if collector.MemSink.Used's signature ever moves, this file stops building
// rather than the hub discovering it at scrape time.
var _ StoreSizer = (*collector.MemSink)(nil)

// StoreSize renders pgsi_store_size_bytes for src.
//
// A source with nothing to report returns "" — this package's rule for a family
// with no series, the same one collector.Stats.Render and
// collector.UpTracker.Render implement on their nil receivers. Two shapes count
// as nothing to report and both are handled: the untyped nil, and the typed nil
// (*collector.MemSink)(nil) that `src == nil` does not catch and whose Used()
// would panic on m.mu. The second is the hazard d2415b1 removed from
// store.RenderPerQuery and internal/api already carries in
// TestNewAcceptsTypedNilPerQuery (22023a3), so it is checked here rather than rediscovered.
//
// A third typed nil exists and is handled by the source, not here: a StoreSizer
// holding (*store.Store)(nil) fails the MemSink assertion, falls through to
// src.Used(), and renders 0 — store.Used()'s answer for a nil receiver since
// f634142 added the method (A5), which is where the check has to live. This file
// cannot generalise the assertion above: metrics cannot import store (store
// imports api, api imports metrics), so there is no
// `var _ StoreSizer = (*store.Store)(nil)` line possible here and the type
// switch would otherwise need a new entry per new source type — which is the
// argument for putting the rule on the receiver. The two answers differ (a nil
// MemSink renders the family absent, a nil Store renders it at 0) and that
// asymmetry is store's to own; this function forwards whatever Used() says.
// d2451de's TestStoreSizeOfNilStore pins the store half from inside store, and
// 22023a3's TestNewAcceptsTypedNilPerQuery (internal/api/server_test.go) pins
// the MemSink half from inside api — the same division, and for the same reason:
// the package that names the type is the one that can assert on it.
//
// prefix follows R9.2 the way UpTracker and HTTPRequests do it: "" selects
// collector.DefaultPrefix, any other value is used verbatim.
func StoreSize(src StoreSizer, prefix string) string {
	if src == nil {
		return ""
	}
	if s, ok := src.(*collector.MemSink); ok && s == nil {
		return ""
	}
	if prefix == "" {
		prefix = collector.DefaultPrefix
	}
	name := prefix + StoreMetricBase
	return "# HELP " + name + " Bytes held in the process's sample store (R13.5).\n" +
		"# TYPE " + name + " gauge\n" +
		name + " " + strconv.FormatInt(src.Used(), 10) + "\n"
}
