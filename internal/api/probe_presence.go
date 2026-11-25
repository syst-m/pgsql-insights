// The wired ExtensionPresenceSource: R3.1's per-database presence report derived
// from the settings collector's probe inventory.
//
// The seam this implements is `internal/api/queries.go:315`
// (`ExtensionPresenceSource`), and the `Config.ExtensionPresence` field that
// defaults to `emptyPresenceSource`; this file is what gets assigned to that
// field once a collector exists, and it is the second of the two halves that
// header describes — R3.2's rows come from the query store, R3.1's from here.
//
// # Why it reads ProbeReader and not Samples
//
// The metric plane cannot answer R3.1. collector.MemSink.Samples returns the
// samples a round published, and settings.go's extensionSamples emits one sample
// per `pg_extension` row it read — so a database with the extension installed
// contributes a sample and a database without it contributes nothing, and the two
// are indistinguishable in a sample list. R3.1's ranking (A3's first ruling on the
// family, and the reason dto.ExtensionPresenceView.Installed is a `*bool`) needs
// three answers, not two: present, absent, and not-known. The absent answer needs
// to know the database was probed, which is a fact about the probe, not about the
// samples.
//
// collector.ProbeResult is A1's type for exactly that fact — one database's answer
// from a settings round, with an empty `Extensions` meaning "probed, nothing
// installed" rather than "not probed" — and collector.ProbeReader is the read half
// that returns it with an `ok` so "no round has ever run for this target" is
// separable from "a round ran and found nothing". This file's whole job is the
// projection from that shape into the DTO's.
//
// # Which extension
//
// R3.1 names one: `pg_stat_statements`. The DTO agrees —
// `internal/api/dto/queries.go:270` (`ExtensionPresenceView`) has no
// extension-name field, and its response field on QueriesResponse is literally
// `PGStatStatements` — so the extension is fixed by the contract and this file
// imports the name rather than restating it: collector.PGStatStatements, which is
// the collector's own const and the one A1 put next to ProbeResult for this
// purpose. A local `const extName = "pg_stat_statements"` here would be a second
// copy of a string the producer already owns, and nothing in CI would notice it
// drift.

package api

import (
	"context"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/collector"
)

// NewProbePresenceSource returns the ExtensionPresenceSource backed by `probes`,
// resolving the (namespace, cluster) pair on an incoming ListQuery to a collector
// target with `target`.
//
// `target` is a function rather than a discovery.Inventory because the projection
// from a fleet entry to a collector.Target lives in cmd/pgsi (targets.go's
// collectTargets), and moving it into this package would put a discovery→collector
// projection in a package that imports discovery only for the `Fleet` read
// interface. cmd/pgsi passes its own function — it already has the inventory — and
// the function shape is what lets the pairing be tested without a discovery fake.
//
// A nil `probes` or a nil `target` answers the way emptyPresenceSource does (no
// rows, no error) rather than panicking, so a partially wired Config degrades to
// the documented empty field. The typed-nil caveat queries.go's accessor names
// still applies one level in: `probes` holding a typed nil (a `(*MemSink)(nil)`
// assigned to the interface) is not `== nil`, and the behaviour in that case is the
// implementation's, not this constructor's.
func NewProbePresenceSource(
	probes collector.ProbeReader,
	target func(namespace, cluster string) (collector.Target, bool),
) ExtensionPresenceSource {
	return probePresenceSource{probes: probes, target: target}
}

// probePresenceSource is the ExtensionPresenceSource NewProbePresenceSource
// returns. It holds the two read dependencies and no state.
type probePresenceSource struct {
	probes collector.ProbeReader
	target func(namespace, cluster string) (collector.Target, bool)
}

// ExtensionPresence implements ExtensionPresenceSource.
//
// The rule it implements is A3's R3.1 ranking, restated for the two shapes the
// reader can answer with:
//
//   - The ListQuery names a database (`?db=`) and that database is in the
//     inventory: one row, `installed` set explicitly to whether
//     collector.PGStatStatements is among the extensions the probe found. This is
//     the only path that may produce `installed: false`, and therefore the only
//     path that may render R10.27's "not installed" screen.
//   - The ListQuery names a database that is not in the inventory — including the
//     whole time no settings round has ever published for this target, since then
//     the inventory is empty whatever the query asks for: one row with `installed`
//     left off the body. The database was not measured, and R10.27's screen is for
//     databases that were.
//   - The ListQuery names no database: one row per database in the inventory, all
//     explicit. If no round has ever published (`ok` false), there is no database
//     set to enumerate and the answer is no rows — inventing one from discovery
//     would make this route a second source of database identity, which is the
//     half R3.1's sentence assigns to the fleet, not to the presence report.
//
// The error result exists to match the interface's shape (every source in this
// package returns one, and handleQueries degrades a non-nil to an empty field
// rather than failing the whole read). This implementation has no failure mode:
// ProbeReader returns no error, and a projection from stored values cannot fail.
// A store-backed reader that can fail returns through the same field.
func (s probePresenceSource) ExtensionPresence(_ context.Context, q ListQuery) ([]dto.ExtensionPresenceView, error) {
	if s.probes == nil || s.target == nil {
		return nil, nil
	}

	tgt, ok := s.target(q.Namespace, q.Cluster)
	if !ok {
		// A (namespace, cluster) the fleet has no target for has no probed
		// databases, which is the same answer emptyPresenceSource gives and for
		// the same reason: no rows is the truth, a 500 would be a claim about why.
		return nil, nil
	}

	stored, _ := s.probes.Probes(tgt, collector.CollectorSettings)

	if q.DB != "" {
		for _, pr := range stored {
			if pr.Database != q.DB {
				continue
			}
			return []dto.ExtensionPresenceView{presenceRow(tgt, pr)}, nil
		}
		// Not in the inventory: either no round has run, or this database was not
		// connectable in the one that did. Both are the not-known state.
		return []dto.ExtensionPresenceView{{
			Namespace: tgt.Namespace,
			Cluster:   tgt.Cluster,
			Database:  q.DB,
		}}, nil
	}

	if len(stored) == 0 {
		return nil, nil
	}

	out := make([]dto.ExtensionPresenceView, 0, len(stored))
	for _, pr := range stored {
		if pr.Database == "" {
			// dto.ExtensionPresenceView's own docblock: a per-database row without
			// the identity pair cannot be attributed to the cluster whose page
			// renders it. A stored row with no database name is not a row.
			continue
		}
		out = append(out, presenceRow(tgt, pr))
	}
	return out, nil
}

// presenceRow projects one stored probe onto the DTO. The identity half comes from
// the target rather than from the ListQuery, so a query that asked with a
// normalised spelling still gets the fleet's own spelling back — the same argument
// QueryView makes for taking namespace/cluster from the row it came from.
func presenceRow(tgt collector.Target, pr collector.ProbeResult) dto.ExtensionPresenceView {
	return dto.ExtensionPresenceView{
		Namespace: tgt.Namespace,
		Cluster:   tgt.Cluster,
		Database:  pr.Database,
		Installed: boolPtr(hasExtension(pr, collector.PGStatStatements)),
	}
}

// hasExtension reports whether name is in the probe's installed set. It is a
// linear scan rather than a set because the set is the extensions installed in one
// database — single digits in practice — and building a map per query to answer
// one membership question is the shape that costs more than it saves.
func hasExtension(pr collector.ProbeResult, name string) bool {
	for _, ext := range pr.Extensions {
		if ext == name {
			return true
		}
	}
	return false
}

// boolPtr is the constructor for the DTO's tri-state half: the only way to build
// the `installed: true` and `installed: false` states, as against leaving the
// pointer nil for not-known. Taking the value and returning a new pointer (rather
// than the caller's) is what keeps a caller from mutating a served row afterwards.
func boolPtr(v bool) *bool { return &v }
