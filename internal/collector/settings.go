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

// settingsViewGroup is the first of this collector's two R2.7 view groups: the two
// cluster-wide views.
//
// One group for pg_settings and pg_file_settings, for the reason database.go's
// equivalent spells out (R13.1's < 40 round-trips per cluster round is only
// reachable at one transaction per collector, not one per query). The pg_extension
// probe used to be in this group; S119 moved it to extensionsViewGroup because a
// cluster-wide transaction and a per-database probe cannot share one transaction.
const settingsViewGroup = "settings"

// extensionsViewGroup is the collector's second R2.7 view group: the extension
// catalogue, plus the pg_database enumeration that says which databases to read it
// from.
//
// It is a separate group because R2.7's rule is one read-only transaction per *view
// group* and a Postgres transaction belongs to exactly one database. pg_extension is
// a per-database catalogue — A3's second ruling on R3.1: "a `postgres` connection
// cannot read `appdb`'s extensions at all" — so probing N databases needs N
// transactions, and a single group holding them would have N transactions where its
// pg_settings query has one. statements.go's `statements` group is the first such
// group, and its docblock — statements.go:74-76 — now names this one as the second
// ("this is one of two places where a collector's view group is not also its whole
// round"). That sentence read "the one place in the codebase" until A5's `202931d`
// retitled it, which is the change my `6e88618` forced: the quote that used to sit
// here was that older sentence, and a quote of a sentence its commit deleted is
// worth keeping the date on. The reason is the same one A5's file gives — the axis
// is the database, and a transaction cannot span it.
const extensionsViewGroup = "extensions"

// settingsViews are the views the settings group queries: §3.3's two cluster-wide
// sources, in the order their queries are issued.
var settingsViews = []pgversion.View{
	pgversion.ViewSettings,
	pgversion.ViewFileSettings,
}

// extensionViews are the views the extensions group queries: the catalogue this
// collector exists to read, and the pg_database enumeration that supplies the list of
// databases to read it from.
//
// pg_database is database.go's view and it is read here as an input, not as a family
// this collector owns — the settings collector mints no pg_database_* samples from
// those rows (database.go owns those families, and two collectors exporting one
// series is two writers on one number). It is here because R3.1's second ruling names
// it as the enumeration source: "the producer enumerates databases from `pg_database`
// per instance and probes each one's `pg_extension`".
var extensionViews = []pgversion.View{
	pgversion.ViewDatabase,
	pgversion.ViewExtension,
}

// allSettingsViews is both groups' view set and the only correct input to AppliesTo:
// R2.7's applicability is conjunctive over every view the collector reads, and since
// S119 that includes pg_database. The set of majors the answer covers is unchanged —
// pg_database has a column set on all six (matrix_data.go:529), where the other three
// already limited this collector to PG13–18.
var allSettingsViews = append(append([]pgversion.View{}, settingsViews...), extensionViews...)

// viewSpecs is the per-view SQL this collector adds to the matrix's column set.
//
// pg_database's entry reuses database.go's CONNECT filter verbatim — D20's S7's
// reason applies unchanged, a database this credential cannot open cannot be probed —
// and deliberately drops the size/XID extras, which are R5.2's values for
// database.go and would cost a pg_database_size() call per row for a number this
// collector does not read. The other three views need nothing, and the zero QuerySpec
// is their answer; the map exists so the one non-zero spec is not a special case
// inside the loop that builds the round.
var viewSpecs = map[pgversion.View]QuerySpec{
	pgversion.ViewDatabase: {Where: databaseSpecs[pgversion.ViewDatabase].Where},
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
// seventeen columns (matrix_data.go:159) and only one of them (`setting`) is a
// value, so `MetricName`
// per column would give one `pg_settings_setting` family that every GUC's row
// overwrites.
//
// The second judgement is newer and much smaller. S119 made the extension probe fan
// out over every connectable database instead of reading only the configured one,
// because pg_extension is per-database and a `postgres` connection cannot see
// `appdb`'s extensions at all. Inside this file that is three changes: the round
// gains a second view group, extensionSamples gains a datname label sourced from the
// projection rather than from a row that has no such column, and a pod with N
// databases now costs N+1 transactions instead of one. On the metric plane it is one
// change: `pgsi_pg_extension_postgis` gains a series per database that has it, which
// is what R3.1's `database` cell and dto's ExtensionPresenceView.Database have asked
// for since Phase 1a. The GUC and file-settings families are untouched — R9.3 gives
// datname to *database* series, and a pg_settings family is not one (there is no
// settings DTO at all, where the extension surface has a Database field).
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
// R3.1's database, R13.11's clock), with every unset field resolved by
// SQLConfig.withDefaults.
func NewSettingsCollector(opener SQLOpener, cfg SQLConfig) *SettingsCollector {
	c := &SettingsCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorSettings,
		Interval: SettingsInterval,
		Source:   SourceSQL,
		// CostCheap rather than CostStandard: pg_settings returns a few hundred
		// rows, but at §3.3's 15-minute interval the work per hour is a fraction of
		// the database collector's, and CostClass's cheap/standard split is about
		// what the instance does per round and how often it does it. The N extension
		// probes S119 adds are one catalogue SELECT each, so they move the round from
		// three queries to N+2 without moving the class.
		Cost:      CostCheap,
		AppliesTo: AppliesToViews(allSettingsViews...),
		// Timeout unset: R2.1's 10 s default, for the reason database.go gives —
		// the round timeout is a hang guard and R2.7's statement_timeout is the
		// tuning knob.
		ViewGroups: []string{settingsViewGroup, extensionsViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *SettingsCollector) Descriptor() Descriptor {
	return c.desc
}

// Collect runs one round: the settings group's single read-only transaction holding
// one query per cluster-wide view (R2.7, R2.4), and the extensions group's one
// read-only transaction per connectable database (R3.1's fan-out).
//
// The error paths each return the round-trips issued so far, because the Collector
// contract requires it and R13.1's budget is measured from Result.RoundTrips.
func (c *SettingsCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	rnd, err := c.projections(tgt)
	if err != nil {
		// Zero is correct here: building the SQL is what tells us there is nothing to
		// run, and it happens before any connection is checked out.
		return Result{}, err
	}
	// One ingest timestamp per round, not one per query: R13.11's our-clock answers
	// "when did this round's data arrive", and a round whose rows were ingested at
	// four different microseconds says the round took four microseconds.
	now := c.cfg.Now()

	// Group 1 — settings: the two cluster-wide views, in one read-only transaction
	// on the configured database.
	settings, err := c.runTx(ctx, tgt, c.cfg, settingsViewGroup, rnd.settings, now)
	if err != nil {
		return Result{Samples: settings.samples, RoundTrips: settings.trips}, err
	}

	// Group 2 — extensions: one read-only transaction per connectable database.
	// The first is the configured database's and carries the pg_database enumeration
	// in the same transaction as that database's own probe, which is why discovering
	// the other databases costs no extra connection.
	extensions, err := c.runTx(ctx, tgt, c.cfg, extensionsViewGroup,
		[]viewProjection{rnd.enumeration, rnd.probe}, now)
	if err != nil {
		return Result{Samples: joinSamples(settings, extensions),
			RoundTrips: settings.trips + extensions.trips}, err
	}

	// Every remaining database gets its own transaction and its own probe. The
	// configured one is skipped because the transaction above already ran there:
	// probing it twice would mint two identical series for one extension in one
	// round, which is the duplicate-series class R9.2's prefix exists to prevent.
	// The stored inventory starts with the configured database, whose extensions
	// came back in the transaction above. Building it from extensions.rows rather
	// than from a query of its own is what keeps the inventory and the samples in
	// agreement: they are two views of one projection's rows, both built through
	// extnameOf, so a database cannot read as installed in one and absent from the
	// other. The empty-RoundTrip Result above correctly carries no inventory — that
	// transaction failed, so nothing was probed.
	probes := []ProbeResult{{
		Database:   c.cfg.Database,
		Extensions: extensionNames(extensions.rows[rnd.probe.Query]),
	}}
	for _, db := range probedDatabases(extensions.rows[rnd.enumeration.Query]) {
		if db == c.cfg.Database {
			continue
		}
		cfg := c.cfg
		cfg.Database = db

		next, err := c.runTx(ctx, tgt, cfg, extensionsViewGroup, []viewProjection{rnd.probe.in(db)}, now)
		extensions.samples = append(extensions.samples, next.samples...)
		extensions.trips += next.trips
		// Appended before the error check, mirroring the two lines above it: the
		// rule in this loop is that a partial round's work is returned intact even
		// when it fails, and an inventory that listed three databases while the
		// samples listed four would be a second answer to one question.
		probes = append(probes, ProbeResult{
			Database:   db,
			Extensions: extensionNames(next.rows[rnd.probe.in(db).Query]),
		})
		if err != nil {
			return Result{Samples: joinSamples(settings, extensions),
				RoundTrips: settings.trips + extensions.trips,
				Probes:     probes}, err
		}
	}

	return Result{
		Samples:    joinSamples(settings, extensions),
		RoundTrips: settings.trips + extensions.trips,
		Probes:     probes,
	}, nil
}

// joinSamples concatenates two groups' samples into one slice.
//
// It exists because append(a, b...) on a slice the caller still holds can write into
// a's spare capacity, and a round that returns the joined slice while also keeping
// `settings` alive would have the two aliases share a backing array. Copying is the
// answer that does not require the caller to remember that.
func joinSamples(groups ...txResult) []Sample {
	var n int
	for _, g := range groups {
		n += len(g.samples)
	}
	out := make([]Sample, 0, n)
	for _, g := range groups {
		out = append(out, g.samples...)
	}
	return out
}

// txResult is what one transaction produced: the samples its projections built, the
// rows those projections returned (keyed by query text), and the round-trips it
// issued.
//
// The rows are in the result because one caller needs the data and not the metric:
// the extension probe's pg_database enumeration is read to learn *which databases to
// probe*, and a runTx that returned only samples would throw away the answer it was
// called for. Every other caller ignores the field.
type txResult struct {
	samples []Sample
	rows    map[string][]Row
	trips   int
}

// runTx opens one read-only transaction on cfg, runs every projection in it, commits,
// and returns what the transaction produced.
//
// It is shared by the collector's two view groups so the error paths — which the
// Collector contract requires to return whatever was issued before the failure, and
// which are otherwise written once per group per collector — exist once. The group
// name goes into the error text because a collector with two groups has two Begin
// calls, and a caller reading "collector settings: begin" cannot otherwise tell
// which one failed.
//
// There is a fourth failure shape, and it is this file's own rather than
// database.go's: a projection paired with the wrong database is refused before
// Begin, because a transaction opened on appdb running postgres's projection is a
// wrong answer with no symptom (see the check below).
//
// The other three are all the ones database.go documents: a failed Begin issues
// nothing (so the zero Result is the honest one), a failed query returns the samples
// parsed from the projections that did answer, and a failed Commit is still an error
// path that deserves the best-effort Rollback D20's S9 asks for — its error is the
// commit's, which is why the rollback's own result is discarded.
func (c *SettingsCollector) runTx(
	ctx context.Context,
	tgt Target,
	cfg SQLConfig,
	group string,
	projections []viewProjection,
	now time.Time,
) (txResult, error) {
	// Every projection must be paired with the database this transaction opens on.
	// The check is pre-flight — before Begin, so a mismatch costs no connection, the
	// same reason Collect builds its SQL before it opens anything — and it is the
	// invariant the fan-out is built on: a projection whose SQL runs in appdb but
	// whose `database` field still says postgres mints a sample that reports the wrong
	// database, and nothing downstream can tell, because the label *is* the answer.
	for _, pv := range projections {
		if pv.database != cfg.Database {
			return txResult{}, fmt.Errorf("collector %s: group %s: %s projection is built for database %q but the transaction opens on %q",
				CollectorSettings, group, pv.view, pv.database, cfg.Database)
		}
	}

	tx, err := c.opener.Begin(ctx, tgt, cfg)
	if err != nil {
		return txResult{}, fmt.Errorf("collector %s: group %s: begin read-only transaction: %w",
			CollectorSettings, group, err)
	}

	var out txResult
	out.rows = make(map[string][]Row, len(projections))
	for _, pv := range projections {
		rows, qerr := tx.Query(ctx, pv.Query)
		out.trips++
		if qerr != nil {
			// The samples parsed from the projections that did answer come back with
			// the error. The scheduler publishes nothing on a failed round (that
			// non-publish is R2.2's last-good rule), so returning them costs nothing
			// and gives a caller that logs the result something to log.
			_ = tx.Rollback()
			return txResult{samples: out.samples, rows: out.rows, trips: out.trips},
				fmt.Errorf("collector %s: group %s: query %s: %w", CollectorSettings, group, pv.view, qerr)
		}
		out.rows[pv.Query] = rows
		out.samples = append(out.samples, settingsSamples(pv, now, rows)...)
	}

	if cerr := tx.Commit(); cerr != nil {
		// D20's S9, applied here from database.go: sql.go's Tx doc says "Rollback on
		// every error path", and a failed Commit is an error path. A driver that
		// failed the COMMIT may still hold the transaction open on a connection the
		// pooler hands to the next round, and the rollback that closes it is
		// best-effort — its error is the commit's, which is why the result is
		// discarded here exactly as it is in the query path above.
		_ = tx.Rollback()
		return txResult{samples: out.samples, rows: out.rows, trips: out.trips},
			fmt.Errorf("collector %s: group %s: commit: %w", CollectorSettings, group, cerr)
	}
	return out, nil
}

// settingsRound is one round's query set: the settings group's projections, the
// pg_database enumeration, and the pg_extension projection that gets run once per
// database.
//
// It is a struct rather than a []viewProjection because the extensions group's two
// projections are not a list to run in order — one (pg_database) is read for its
// *rows* and the other (pg_extension) is read once per database the first one named —
// and naming the three fields is what stops Collect from having to remember which
// index is which.
type settingsRound struct {
	settings    []viewProjection
	enumeration viewProjection
	probe       viewProjection
}

// projections builds every query this round runs, for the target's major, reusing the
// viewProjection pairing and the refusal rules database.go established: no major
// means no column set to build SQL from, and a view the matrix does not have on this
// major means the declared applicability and the matrix disagree. Both are checked
// before Begin, so a round with nothing to run costs no connection.
func (c *SettingsCollector) projections(tgt Target) (settingsRound, error) {
	if tgt.PGMajor <= 0 {
		return settingsRound{}, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorSettings, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		names := make([]string, 0, len(allSettingsViews))
		for _, v := range allSettingsViews {
			names = append(names, string(v))
		}
		return settingsRound{}, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (one of [%s] has no column set there)",
			CollectorSettings, tgt.PGMajor, strings.Join(names, " "))
	}

	var rnd settingsRound
	rnd.settings = make([]viewProjection, 0, len(settingsViews))
	for _, v := range settingsViews {
		p, err := c.project(v, tgt.PGMajor)
		if err != nil {
			return settingsRound{}, err
		}
		rnd.settings = append(rnd.settings, viewProjection{view: v, Projection: p, database: c.cfg.Database})
	}

	// The extensions group's two. Both carry c.cfg.Database, and the probe's
	// projection is built once and run N times: the SQL text does not depend on the
	// database, because pg_extension resolves in whichever database the connection
	// opened against — which is the whole reason the fan-out exists.
	p, err := c.project(pgversion.ViewDatabase, tgt.PGMajor)
	if err != nil {
		return settingsRound{}, err
	}
	rnd.enumeration = viewProjection{view: pgversion.ViewDatabase, Projection: p, database: c.cfg.Database}

	p, err = c.project(pgversion.ViewExtension, tgt.PGMajor)
	if err != nil {
		return settingsRound{}, err
	}
	rnd.probe = viewProjection{view: pgversion.ViewExtension, Projection: p, database: c.cfg.Database}

	return rnd, nil
}

// project builds one view's projection for one major and validates it, so every
// projection this collector runs went through ValidateProjection — the same rule
// database.go's projections loop applies to each of its three views. The spec comes
// from viewSpecs, which is where pg_database's CONNECT filter lives.
func (c *SettingsCollector) project(v pgversion.View, major int) (Projection, error) {
	p, ok := Project(v, major, viewSpecs[v])
	if !ok {
		return Projection{}, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d", CollectorSettings, v, major)
	}
	if err := ValidateProjection(p); err != nil {
		return Projection{}, err
	}
	return p, nil
}

// probedDatabases is the list of databases the extension probe fans out to, from the
// rows of one pg_database projection.
//
// Three rules, all from R3.1's second ruling:
//
//   - a row with no datname names no database, so it yields nothing — the same rule
//     database.go's samplesFromRows applies to a NULL datname (D20's S8);
//   - datallowconn = false cannot be connected to, so it cannot be probed. The SQL
//     already filtered on has_database_privilege (D20's S7), which is about the
//     *credential*; datallowconn is about the database, and the two disagree — a
//     superuser has CONNECT on template0 and still cannot open it;
//   - template0 is named alongside datallowconn because that is how the ruling names
//     it ("`datallowconn = false` databases and `template0` cannot be probed and
//     yield no row"). Postgres ships it with datallowconn = false, so the rule above
//     already covers it and this one is belt-and-braces for a cluster where an
//     operator flipped the flag.
//
// The configured database is in the list: it is a database like any other. Collect
// skips it in the fan-out loop because the transaction that ran this query *is* that
// database's and already probed it.
func probedDatabases(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		name, ok := row.Get("datname")
		if !ok || name == "" {
			continue
		}
		if name == "template0" {
			continue
		}
		// An absent datallowconn column is treated as connectable: the SQL's own
		// privilege filter is the authority, and a driver that dropped the column
		// should not silently shrink the fleet.
		if raw, ok := row.Get("datallowconn"); ok {
			if v, ok := parseValue(raw); !ok || v == 0 {
				continue
			}
		}
		out = append(out, name)
	}
	return out
}

// settingsSamples turns one view's rows into samples.
//
// It dispatches on the view rather than classifying columns, because the views have
// different answers to "what names the family and what is the value" and each answer
// is one line of its own: one family per GUC, three counts, one family per extension.
// database.go needs a columnKind classifier because its families come from column
// names and it has to decide per column whether a column is a value; here the family
// name comes from row data, so a new column in the matrix (a PG19 pg_settings
// addition) is by definition not a new family and needs no classification — which is
// why there is no settingsRoleOf to test.
func settingsSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	switch pv.view {
	case pgversion.ViewSettings:
		return gucSamples(pv, now, rows)
	case pgversion.ViewFileSettings:
		return fileSettingSamples(pv, now, rows)
	case pgversion.ViewExtension:
		return extensionSamples(pv, now, rows)
	case pgversion.ViewDatabase:
		// The extension probe's enumeration. Its rows answer "which databases exist",
		// which is data the collector consumes (see probedDatabases) rather than
		// something it exports: database.go owns the pg_database_* families, and
		// minting them from a second collector would put two writers on one series.
		return nil
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
//
// No datname, even though pv.database names the database this query ran in: R9.3
// gives that label to *database* series, and a pg_settings family is not one — the
// GUC set is the instance's (the DTO has no settings type at all, where the extension
// surface has ExtensionPresenceView with a Database field). Only extensionSamples
// reads pv.database, because pg_extension is the one view here whose rows are
// per-database and have no column saying so.
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

// extensionSamples turns pg_extension into one family per installed extension, in one
// database.
//
// The value is 1 and the fact is existence: pg_extension's numbers (oid, extowner,
// extnamespace) are identifiers, and its version is text ("1.1.0"), which R9.1 puts
// on the API plane. A family per extension is what makes "is postgis installed on
// this pod" a query rather than a scrape of the API, and it is the R3.1/R3.15
// extension surface expressed in the metric plane — `count({__name__=~"pgsi_pg_extension_.*"})`
// for a count, one series for one extension.
//
// The datname label is the half S119 was filed over. pg_extension has no database
// column to read it from — the catalogue *is* the database's, so the answer is the
// database the query ran in — and this collector used to emit `Labels:
// map[string]string{}`, which is why a pod with four databases reported one presence
// row and rendered the other three as not-known. The answer comes from the projection
// now (viewProjection's doc is the normative statement of where a builder gets it).
func extensionSamples(pv viewProjection, now time.Time, rows []Row) []Sample {
	// Capacity len(rows): one sample per row, and the rows with no extname leave the
	// slack unused rather than costing a regrowth.
	out := make([]Sample, 0, len(rows))
	for _, row := range rows {
		name, ok := extnameOf(row)
		if !ok {
			continue
		}
		out = append(out, Sample{
			Metric: familyFor(pv.view, name),
			// R9.3's series-specific half: an extension series belongs to a database,
			// and the only place this row can learn which one is the projection it
			// came through. The identity half stays the scheduler's.
			Labels:     map[string]string{"datname": pv.database},
			Value:      1,
			SourceTime: sourceTimeOf(row),
			IngestTime: now,
		})
	}
	return out
}

// extnameOf is the one place a pg_extension row's extension name is read.
//
// extensionSamples mints a family per name and extensionNames builds the stored
// inventory, and both need the same two-line rule — the column is "extname", and
// an absent or empty value names no extension. Two inline copies in one file are
// the drift this package keeps documenting rather than fixing (see the settings.go
// quote 202931d deleted, which was the same class in prose), so the rule lives here
// and both callers call it.
func extnameOf(row Row) (string, bool) {
	name, ok := row.Get("extname")
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// extensionNames returns every extension installed in the database the rows came
// from, for the ProbeResult inventory Collect stores.
//
// It is the same set extensionSamples turned into families, and that is the point:
// the inventory and the samples must agree because they are built from one
// projection's rows by one extraction rule, not because two queries happened to
// agree. A database with no extensions yields an empty slice, which is the answer
// Samples cannot express and the reason ProbeResult exists (see sink.go).
func extensionNames(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		name, ok := extnameOf(row)
		if !ok {
			continue
		}
		out = append(out, name)
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
