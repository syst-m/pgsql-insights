// Package metrictype holds the Prometheus metric-type enum and nothing else.
//
// It is a leaf — it imports nothing, internal or external — and that is the whole
// reason it exists. The enum was defined in internal/collector/naming.go, and
// internal/collector/cnpgmetrics imported internal/collector for exactly one
// field, Family.Type (cnpgmetrics/registry.go:100). That fixed the dependency
// direction as cnpgmetrics -> collector, and the comment then above that struct
// justified it in writing: "importing it is also acyclic, since collector's
// registry takes its collectors as a []Collector argument and does not import this
// package back." Those are the pre-extraction words; the comment at
// registry.go:84-97 now states the rule as it stands after the move.
//
// pooler.go (R6.1, S5) is the first collector that needs the cnpg exposition
// parser from inside the collector package, and it falsifies that sentence: a
// collector in internal/collector importing internal/collector/cnpgmetrics closes
// the cycle cnpgmetrics -> collector -> cnpgmetrics. This is not a pooler problem.
// Three of the package's remaining collectors — pooler (S5), instance-metrics (S3,
// the instance manager's 9187 endpoint) and k8s-events (S4) — parse an exposition,
// so all three are blocked by the enum's placement, not by their own design.
//
// Moving the enum here makes the dependency one-directional by construction rather
// than by convention: a leaf imports nothing, so no package that uses the enum can
// become one that imports its definer. internal/collector keeps a type alias
// (naming.go), so every existing collector.MetricType and collector.TypeGauge
// reference compiles unchanged — the move renames the definition's address, not
// the names in use.
//
// It follows the internal/collector/pgversion precedent: a leaf holding one typed
// domain that the collector package imports, extracted precisely so that collector
// is not its only possible consumer.
package metrictype

// MetricType is the Prometheus type of a family, which is the other half of what
// the renderer needs and cannot be inferred at render time: `# TYPE` is per
// family, and a counter exported without its `_total` suffix is rejected by
// `promtool check metrics` — which Phase 2's acceptance runs.
type MetricType string

const (
	// TypeCounter is a monotonically increasing cumulative metric. Its name ends
	// in `_total` (the Prometheus convention R9.4's own names follow:
	// pgsi_query_rows_total, pgsi_db_xact_commit_total).
	TypeCounter MetricType = "counter"
	// TypeGauge is a value that goes up and down. No suffix.
	TypeGauge MetricType = "gauge"
	// TypeNotExported marks a column that is part of a view's projection but is
	// not a metric at all: a name column (datname, nspname), an identifier
	// (datid, pid, oid), or a timestamp that is a counter epoch rather than a
	// measurement (stats_reset). R9.1 puts these on the API plane; exporting
	// them as numbers is how an exporter ends up with pgsi_pg_stat_activity_pid.
	TypeNotExported MetricType = ""
)
