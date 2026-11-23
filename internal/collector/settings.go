package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
)

// CollectorSettings is §3.3's name for this collector, and the `collector` label
// value on both R2.2 families for its rounds.
//
// §3.3's row is the whole scope statement:
//
//	| `settings` | 15 min | S2 `pg_settings`, `pg_file_settings`, `pg_extension` | Slow-changing; also gives `max_connections`, `shared_buffers`, `work_mem` for the gauge set |
//
// which is three views, one source, one interval. The interval is the row's
// contribution to R13.1's budget: at 15 minutes this collector runs four times an
// hour where the database collector runs sixty, and "slow-changing" is the reason
// the phase's SQL budget is achievable at all.
const CollectorSettings = "settings"

// SettingsInterval is §3.3's interval for the settings collector (15 min), stated
// as a constant so R2.3's --interval.settings override has a declared default to
// override rather than a literal buried in a struct literal.
const SettingsInterval = 15 * time.Minute

// settingsViewGroup is the collector's single R2.7 view group.
//
// One group for the three views, for the reason database.go's equivalent spells out
// (R13.1's < 40 round-trips per cluster round is only reachable at one transaction
// per collector, not one per query).
const settingsViewGroup = "settings"

// settingsViews are §3.3's three sources for this collector, in the order their
// queries are issued.
var settingsViews = []pgversion.View{
	pgversion.ViewSettings,
	pgversion.ViewFileSettings,
	pgversion.ViewExtension,
}

// SettingsCollector is §3.3's settings collector: the running GUC set, the
// postgresql.conf lines behind it, and the installed extensions.
//
// The one judgement in the file is which *families* a row set produces, and it is
// forced by two rules rather than chosen. R9.1 keeps text on the API plane, so a
// GUC whose `setting` is a string (DateStyle, search_path) produces no sample —
// a sample whose value is a string is a sample whose number is a lie. And R9.3's
// label contract is closed (namespace, cluster, pod, instance_role, +pooler,
// +datname, +usename), which means a table keyed by a text column cannot put that
// key in a label. The consequence is that this collector names its families after
// the *row* — one per GUC, one per extension — rather than after the column, which
// is what sql.go's MetricName does for the other SQL collectors: `pg_settings` has
// eighteen columns and only one of them (`setting`) is a value, so `MetricName`
// per column would give one `pg_settings_setting` family that every GUC's row
// overwrites.
//
// Flagged for A3, because it is the second half of a question database.go already
// flagged. §3.3's row names three gauges — `max_connections`, `shared_buffers`,
// `work_mem` — and per-GUC families are the only shape that delivers them under
// R9.3 (a `pg_settings_setting` family with a `name` label would deliver none, and
// `name` is not in the contract). The cost is that the unit is lost: `shared_buffers`
// renders as a count of 8 kB pages with nothing on the wire saying so, because R9.3
// has no unit label and R9.1 sends the unit string to the API. R9.4 (step 4) is the
// normative naming surface where a `_bytes`/`_seconds` suffix rule belongs; this
// file does not invent one.
type SettingsCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewSettingsCollector builds the collector on top of an opener (R2.7's
// transaction source) and a config (R2.7's timeout, R2.8's application name,
// R13.11's clock), with every unset field resolved by SQLConfig.withDefaults.
func NewSettingsCollector(opener SQLOpener, cfg SQLConfig) *SettingsCollector {
	c := &SettingsCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorSettings,
		Interval: SettingsInterval,
		Source:   SourceSQL,
		// CostCheap rather than CostStandard: pg_settings returns a few hundred
		// rows, but at §3.3's 15-minute interval the work per hour is a fraction of
		// the database collector's, and CostClass's cheap/standard split is about
		// what the instance does per round and how often it does it.
		Cost:      CostCheap,
		AppliesTo: AppliesToViews(settingsViews...),
		// Timeout unset: R2.1's 10 s default, for the reason database.go gives —
		// the round timeout is a hang guard and R2.7's statement_timeout is the
		// tuning knob.
		ViewGroups: []string{settingsViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *SettingsCollector) Descriptor() Descriptor {
	return c.desc
}

// Collect runs one round: one read-only transaction (R2.7) holding one query per
// view (R2.4), with every row parsed into samples.
//
// The error paths each return the round-trips issued so far, because the Collector
// contract requires it and R13.1's budget is measured from Result.RoundTrips.
func (c *SettingsCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	projections, err := c.projections(tgt)
	if err != nil {
		return Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		return Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorSettings, err)
	}

	var (
		samples []Sample
		trips   int
	)
	for _, pv := range projections {
		rows, qerr := tx.Query(ctx, pv.Query)
		trips++
		if qerr != nil {
			_ = tx.Rollback()
			return Result{Samples: samples, RoundTrips: trips},
				fmt.Errorf("collector %s: query %s: %w", CollectorSettings, pv.view, qerr)
		}
		samples = append(samples, settingsSamples(pv, c.cfg.Now(), rows)...)
	}

	if cerr := tx.Commit(); cerr != nil {
		return Result{Samples: samples, RoundTrips: trips},
			fmt.Errorf("collector %s: commit: %w", CollectorSettings, cerr)
	}
	return Result{Samples: samples, RoundTrips: trips}, nil
}

// projections builds one query per view for the target's major, reusing the
// viewProjection pairing and the refusal rules database.go established: no major
// means no column set to build SQL from, and a view the matrix does not have on
// this major means the declared applicability and the matrix disagree. Both are
// checked before Begin, so a round with nothing to run costs no connection.
func (c *SettingsCollector) projections(tgt Target) ([]viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return nil, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorSettings, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		names := make([]string, 0, len(settingsViews))
		for _, v := range settingsViews {
			names = append(names, string(v))
		}
		return nil, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (one of [%s] has no column set there)",
			CollectorSettings, tgt.PGMajor, strings.Join(names, " "))
	}

	out := make([]viewProjection, 0, len(settingsViews))
	for _, v := range settingsViews {
		p, ok := Project(v, tgt.PGMajor, QuerySpec{})
		if !ok {
			return nil, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d", CollectorSettings, v, tgt.PGMajor)
		}
		if err := validateProjection(p); err != nil {
			return nil, err
		}
		out = append(out, viewProjection{view: v, Projection: p})
	}
	return out, nil
}

// settingsSamples turns one view's rows into samples.
//
// It dispatches on the view rather than classifying columns, because the three
// views have three different answers to "what names the family and what is the
// value" and each answer is one line of its own: one family per GUC, three counts,
// one family per extension. database.go needs a columnKind classifier because its
// families come from column names and it has to decide per column whether a column
// is a value; here the family name comes from row data, so a new column in the
// matrix (a PG19 pg_settings addition) is by definition not a new family and needs
// no classification — which is why there is no settingsRoleOf to test.
func settingsSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	switch pv.view {
	case pgversion.ViewSettings:
		return gucSamples(pv, now, rows)
	case pgversion.ViewFileSettings:
		return fileSettingSamples(pv, now, rows)
	case pgversion.ViewExtension:
		return extensionSamples(pv, now, rows)
	}
	return nil
}

// gucSamples turns pg_settings rows into one family per GUC.
//
// The family name is the GUC's own name (pg_settings_max_connections) and the value
// is its `setting`; the other sixteen columns are R9.1's text half — the category,
// the description, the context, the vartype, the source, the bounds — and are read
// by the API, not exported. The one text column this collector does turn into a
// number is a boolean GUC, via gucValue.
//
// The view half of every name comes from pv.view rather than from the constant, so
// the dispatch in settingsSamples and the families it produces cannot disagree —
// the same reason database.go's samplesFromRows derives its names from pv.
func gucSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	// Capacity len(rows): a row yields at most one sample, and the rows that yield
	// none (a string GUC) leave the slack unused rather than costing a regrowth.
	out := make([]Sample, 0, len(rows))
	for _, row := range rows {
		name, ok := row.Get("name")
		if !ok || name == "" {
			// No name means no family to attach the value to, and a sample named
			// after an empty string is a sample nobody can query for.
			continue
		}
		raw, ok := row.Get("setting")
		if !ok {
			continue
		}
		v, ok := gucValue(raw)
		if !ok {
			continue
		}
		out = append(out, Sample{
			Metric: familyFor(pv.view, name),
			// No labels: R9.3's contract has no slot for a GUC name or a unit, and
			// the identity half is the scheduler's to add from the Target.
			Labels:     map[string]string{},
			Value:      v,
			SourceTime: sourceTimeOf(row),
			IngestTime: now,
		})
	}
	return out
}

// fileSettingSamples turns pg_file_settings into three counts.
//
// pg_file_settings has one row per line of postgresql.conf (plus every line in
// every file it includes), so it has the same problem pg_settings has — a text key
// R9.3 will not let us label — and a second one the GUC table does not have: the
// same setting can appear in two included files, so a per-name family would have
// two rows competing for one series and the exported value would be whichever line
// the driver returned last. Counts are the shape that survives: how many settings
// the file sets, how many of them applied, how many carry an error. The per-line
// detail (which file, which line, what the error said) is R9.1's text half, and
// `pgsi_pg_file_settings_error_count > 0` is the alertable form of "the running
// config is not the file's config", which is the fact R5.2's settings card wants.
func fileSettingSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	var lines, applied, errored int
	for _, row := range rows {
		lines++
		if v, ok := parseValue(mustGet(row, "applied")); ok && v != 0 {
			applied++
		}
		if mustGet(row, "error") != "" {
			errored++
		}
	}
	if lines == 0 {
		return nil
	}
	source := sourceTimeOf(rows[0])
	return []Sample{
		{Metric: MetricName(pv.view, "count"), Labels: map[string]string{},
			Value: float64(lines), SourceTime: source, IngestTime: now},
		{Metric: MetricName(pv.view, "applied_count"), Labels: map[string]string{},
			Value: float64(applied), SourceTime: source, IngestTime: now},
		{Metric: MetricName(pv.view, "error_count"), Labels: map[string]string{},
			Value: float64(errored), SourceTime: source, IngestTime: now},
	}
}

// extensionSamples turns pg_extension into one family per installed extension.
//
// The value is 1 and the fact is existence: pg_extension's numbers (oid, extowner,
// extnamespace) are identifiers, and its version is text ("1.1.0"), which R9.1 puts
// on the API plane. A family per extension is what makes "is postgis installed on
// this pod" a query rather than a scrape of the API, and it is the R3.1/R3.15
// extension surface expressed in the metric plane — `count({__name__=~"pgsi_pg_extension_.*"})`
// for a count, one series for one extension.
func extensionSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	// Capacity len(rows): one sample per row, and the rows with no extname leave the
	// slack unused rather than costing a regrowth.
	out := make([]Sample, 0, len(rows))
	for _, row := range rows {
		name, ok := row.Get("extname")
		if !ok || name == "" {
			continue
		}
		out = append(out, Sample{
			Metric:     familyFor(pv.view, name),
			Labels:     map[string]string{},
			Value:      1,
			SourceTime: sourceTimeOf(row),
			IngestTime: now,
		})
	}
	return out
}

// familyFor is MetricName's sibling for the case where the family name comes from
// a row's data rather than from a column name: one family per GUC, one per
// extension, instead of one per column.
//
// It keeps MetricName's shape (view + "_" + the thing the column names) so every
// family in the package is built by one of two adjacent functions and step 4's
// R9.4 naming work is a change to both in one place.
func familyFor(view pgversion.View, subject string) string {
	return string(view) + "_" + sanitizeName(subject)
}

// sanitizeName turns a value that arrives in a column — a GUC name, an extension
// name — into something a Prometheus family name's last component can be.
//
// A family name must match [a-zA-Z_:][a-zA-Z0-9_:]*, so the punctuation Postgres
// allows in a GUC name has to go: `pg_stat_statements.max` becomes
// `pg_stat_statements_max`. Case is preserved rather than folded, because two GUCs
// differing only by case (Postgres has both `SSL`-style and lowercase names across
// majors) would otherwise collide, and R9.2's whole argument for the prefix is that
// names must not collide.
func sanitizeName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// gucValue returns the number a GUC's `setting` column means, and false when the
// value has no number.
//
// It is parseValue plus the one spelling only a GUC uses: Postgres renders a
// boolean *column* as t/f but a boolean *setting* as on/off, so `log_connections`
// (setting "on") maps to 1 the way every other exporter does, while a string GUC
// (DateStyle "ISO, MDY") has no number at all and produces no sample rather than a
// sample whose value is a lie (R9.1).
func gucValue(setting string) (float64, bool) {
	switch setting {
	case "on", "ON", "On":
		return 1, true
	case "off", "OFF", "Off":
		return 0, true
	}
	return parseValue(setting)
}

// sourceTimeOf reads R13.11's instance clock off one row. It is this file's half of
// the rule database.go's samplesFromRows follows the same way: the query carries
// now() as SourceNowColumn so the instance's clock comes back with the data instead
// of costing a round-trip.
//
// It takes a row and nothing else. The column is a constant rather than a per-view
// answer, so the viewProjection parameter it used to take was unused — the fix is
// to drop it rather than to rename it to `_`, because a parameter that is never
// part of the answer is not part of the signature.
//
// A row the driver returned without the column leaves the time zero, which
// clock.go's HasSourceClock turns into "not compared" rather than a 56-year skew.
func sourceTimeOf(row Row) time.Time {
	raw, ok := row.Get(SourceNowColumn)
	if !ok {
		return time.Time{}
	}
	t, _ := parseTimestamp(raw)
	return t
}

// mustGet reads a column's value or the empty string, for the builders that need a
// value's text without caring whether the column was there: an absent column and an
// empty one both mean "no value" for a count.
func mustGet(row Row, column string) string {
	v, _ := row.Get(column)
	return v
}
