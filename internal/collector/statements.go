// statements.go is §3.3's statement collector: R3.1's pg_stat_statements feed.
//
// §3.3:196's row is `| statement | 60 s | S2 pg_stat_statements | The Query
// Insights feed; gated on extension presence (R3.1) |`, and the file is the
// implementation of that row. It has two products and the split is R9.1's:
//
//   - the metrics plane — Collect returns the R9.5 per-query family for the
//     top-N rows, which is what /metrics serves and what R9.5b's label rule
//     governs;
//   - the API plane — CollectStatements returns the rows themselves, which is
//     what the store persists and what R3.2's acceptance test compares against a
//     manual `SELECT … FROM pg_stat_statements ORDER BY total_exec_time DESC
//     LIMIT 50`.
//
// Collect runs one round through CollectStatements and derives its samples from
// the rows that round returned, so every metric value traces to a row and every
// row traces to the SQL — the same property activity.go states for its own
// split, and the reason the two methods exist rather than one.
//
// The row types live here and not in internal/store for a dependency reason.
// store/sink.go imports collector, so collector cannot import store, and the
// collector has to name the type it produces. StatementRow and Snapshot were
// authored in store (store/statements.go was their first consumer) and moved
// here with store's declarations kept as aliases — the repo's existing answer to
// "a type belongs to another package but the local name has consumers", used at
// internal/api/series.go:96-109 for the dto types, where an alias "creates no
// second type, so every existing spelling keeps compiling and keeps naming one
// definition". store.StatementRow, store.Snapshot and store.QuerySource now name
// these three; nothing in store changed a call site.
//
// Two framework answers from A1 are baked into the shape and are worth naming
// because both were open questions when this file was scoped:
//
//   - The database a round came from is a *column*, not a config field.
//     SQLOpener.Begin takes a Target, which is per-pod, and a pod hosts N
//     databases, so the target cannot carry the axis — which is why
//     store.QuerySource exists at all. `pg_catalog.current_database()` as an
//     Extra puts it on every row instead, so a round's rows name their own
//     database and the collector needs no per-database configuration to group
//     them. A1's `Database` field shipped in `6e88618` and the round loop fans
//     out to N databases per round, and the file did not change: each of the N
//     transactions returns rows that name the database they came from, and the
//     fan-out shows up as N snapshots and N round-trips, not as a new parameter.
//   - The typed entry point. activity.go shipped the pattern — `Collect` is a
//     four-line adapter over `CollectSessions` — and CollectStatements is the
//     same adapter with the rows typed as the store's rows, which is what closes
//     the "who calls store.PutSnapshot" question: the cmd/pgsi round loop does,
//     and it needs nothing but the value this method returns to call it.
package collector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/internal/collector/pgversion"
	"github.com/frankellis/pgsql-insights/internal/query"
)

// CollectorStatement is §3.3:196's collector name. Singular, as the table spells
// it, even though the view is plural: the name identifies the collector, and the
// registry, the sink's `collector` column and R2.1's Descriptor all take the
// table's spelling.
const CollectorStatement = "statement"

// StatementInterval is §3.3:196's interval: 60 s.
const StatementInterval = 60 * time.Second

// statementsViewGroup is R2.7's group name for this collector's SQL.
//
// The group is per-database, and this is one of two places where a collector's
// view group is not also its whole round — the other is the settings
// collector's `extensions` group (`6e88618`, S119). R2.7's rule is one
// read-only transaction per *view group*, not per round, so a pod hosting N
// databases is N transactions and N entries in Result.RoundTrips, the number
// R13.1's "<40 SQL round-trips per cluster" is measured from.
const statementsViewGroup = "statements"

// statementViews is the view set this collector reads, and it is the set that
// decides which majors the collector applies to.
//
// AppliesToViews is the all-views rule (R2.7's applicability is conjunctive: a
// collector applies to a major only if every view it reads has a column set
// there), and pg_stat_statements_info exists on PG 14–18 only —
// pgversion.StatStatementsInfoPG14to18 is the only column set it has, at
// matrix.go:551. So the collector applies to PG 14–18 and not to PG 13.
//
// That is not a reduction of R13.9's PG 15–18 range: PG 13 is outside it. It is
// also not a loss of R3.3's PG 13 behaviour, which is implemented in the store —
// store.Snapshot.StatsReset's doc is the normative statement that a zero means
// "no epoch available" and selects the per-row decrease-detection fallback, and
// that is exactly what a PG 13 deployment gets from a collector that never reads
// the info view. The alternative, reading pg_stat_statements alone on PG 13,
// would give the collector a second column set to maintain for a major R13.9
// excludes.
var statementViews = []pgversion.View{
	pgversion.ViewStatStatements,
	pgversion.ViewStatStatementsInfo,
}

// statementsWhere is R2.8's exclusion, applied at query level.
//
// R2.8: "Exclude our own sessions from every count we report, by setting
// application_name=pgsi-collector and filtering it out of
// pg_stat_activity-derived metrics (the postgres_exporter
// --collector.stat_statements.exclude_users pattern, applied by us at query
// level)." IMPLEMENTATION.md:763 assigns the pg_stat_statements half of that to
// this file: "the same for the `pg_stat_statements` `exclude_users` pattern the
// requirement cites as the model".
//
// The pattern's mechanism is a *user* filter, and a user filter is wrong here for
// a reason worth writing down: conn.go's credential precedence tries the
// `<cluster>-app` secret first, so on a zero-config cluster this collector
// connects as the application's own role — and filtering `userid = current_user`
// out of pg_stat_statements would then delete the application's entire workload,
// which is the population R3.2's top-N exists to rank. The requirement's other
// half is the answer: our sessions are identifiable by the SQL they run, because
// R2.5 guarantees every query this codebase issues is `SELECT … FROM
// pg_catalog.<view>` or `SELECT … FROM pg_stat_statements.<view>`, and Project
// builds them in one place. So the filter is on the statement text, and it
// matches the shape our own query builder produces rather than a role name.
//
// The `_` is escaped because in LIKE it is a single-character wildcard: an
// unescaped `pg_catalog` pattern would also match `pgXcatalog`. ValidateProjection
// accepts it because unqualifiedCall only inspects `(` characters and this clause
// has none.
//
// The filter is on the pg_stat_statements spec only. The info view has no `query`
// column, so a WHERE naming it would be invalid SQL there.
const statementsWhere = `query NOT LIKE 'SELECT%FROM pg\_catalog.%' ` +
	`AND query NOT LIKE 'SELECT%FROM pg\_stat\_statements.%'`

// statementSpecs gives each view its Extras and WHERE.
//
// The datname Extra on both is the "the database is a column" decision from the
// header: it is what lets CollectStatements group one round's rows into one
// snapshot per database, and it is what store.validateSnapshot requires (its
// fourth case rejects a snapshot whose source has no datname).
//
// The usename Extra on pg_stat_statements is how store.StatementRow's Usename
// doc gets satisfied — "the view has `userid` (an OID) on every major and never
// the name, so the collector resolves it through pg_authid and hands the store
// the name; empty means unresolved". pg_get_userbyid is the function that reads
// pg_authid, and Extra is the framework's answer to a value a column list cannot
// express (sql.go's Extra doc names R5.2's pg_database_size as the precedent).
// It is schema-qualified per R2.5, which is what ValidateProjection checks.
var statementSpecs = map[pgversion.View]QuerySpec{
	pgversion.ViewStatStatements: {
		Extras: []Extra{
			{Expr: "pg_catalog.pg_get_userbyid(userid)", Alias: "usename"},
			{Expr: "pg_catalog.current_database()", Alias: "datname"},
		},
		Where: statementsWhere,
	},
	pgversion.ViewStatStatementsInfo: {
		Extras: []Extra{
			{Expr: "pg_catalog.current_database()", Alias: "datname"},
		},
	},
}

// The top-N window, from R9.5: "for the **top-N only**: default **N=50 per
// cluster** (see §5 — `metrics.topN` is per cluster, not per database), range
// 0–1000. N=0 disables the family. This mirrors postgres_exporter's
// --collector.stat_statements.limit (default 100) as the cardinality lever."
const (
	// DefaultTopN is R9.5's default.
	DefaultTopN = 50
	// MaxTopN is the top of R9.5's range.
	MaxTopN = 1000
)

// ValidTopN reports whether n is inside R9.5's 0–1000 range.
//
// It is exported for the same stated reason query.ValidLength is: the range is a
// configuration rule, and the flag/config layer that reads `metrics.topN` needs
// to reject 1001 without the library refusing to normalise. The collector itself
// accepts any n >= 0 — WithTopN clamps rather than erroring — because a collector
// that returns an error for a number the constructor already accepted is two
// places with two opinions.
func ValidTopN(n int) bool { return n >= 0 && n <= MaxTopN }

// StatementRow is one pg_stat_statements row as the collector read it.
//
// The field set is the view's projection (collector/pgversion's
// StatStatementsPG15to16 and friends) minus the columns no DTO renders
// (local_blks_*, temp_blks_*, wal_*, jit_*, the plan-time columns) plus the two
// the store derives. What the view does *not* have, the collector does not
// claim: there is no lock_time column on any major, which is R3.2's "no per-query
// source" and why query_rollup stores -1 rather than a number (see the store's
// rollupTable).
//
// It moved here from store/statements.go so the collector can name its own
// output; store.StatementRow is now an alias for this type and the doc moved with
// it rather than being rewritten in two places.
type StatementRow struct {
	// QueryID is PostgreSQL's queryid, as a string. The view has a bigint and
	// dto.QueryView.QueryID is a string for the reason stated there — a JSON
	// number past 2^53 is an id JavaScript rounds, so "the UI would display an
	// id the API never sent". Storing the collector's decimal spelling is what
	// keeps the store's answer and the API's answer the same string.
	QueryID string

	// SQL is the raw statement text (the view's `query` column). The store
	// normalizes it in PutSnapshot, and that is the store's only normalization
	// site: R3.5's rules and R3.6's length cap applied in one place is what keeps
	// the fingerprint the API serves identical to the one the collector stored.
	//
	// The collector also needs the fingerprint, because R9.5b makes it the
	// per-query family's identifier label. It gets it from the same pure function
	// with the same Options the store uses (query.Normalize with the collector's
	// normalizer fields, defaulting to query.DefaultQueryStringLength), so the
	// metric's label and the API's fingerprint are one function's output on one
	// string rather than two rules that happen to agree. A deployment that sets a
	// non-default length has to pass it to both — WithNormalizer is the
	// collector's half of that.
	SQL string

	// Usename is the role the statement ran as. The view has `userid` (an OID)
	// on every major and never the name, so the collector resolves it through
	// pg_authid and hands the store the name; empty means unresolved, which the
	// DTOs omit rather than rendering blank.
	Usename string

	Calls            int64
	TotalExecTimeMs  float64
	RowsReturned     float64
	SharedBlocksHit  float64
	SharedBlocksRead float64

	// BlockReadMs and BlockWriteMs are the per-major IO pair the collector
	// picked: blk_read_time/blk_write_time on PG 15–16,
	// shared_blk_read_time/shared_blk_write_time on PG 17+ (R3.2's derivation,
	// quoted at dto.LoadColumns). The store sums them into io_wait_ms and
	// subtracts that sum from TotalExecTimeMs for cpu_ms, so R3.2's two derived
	// columns are derived once instead of in every read path — which is the only
	// way a PG16 row and a PG17 row end up with comparable numbers.
	//
	// The collector picks the pair by preferring the PG 17+ spelling and falling
	// back to the PG 15–16 one, because succession.go already declares the two
	// spellings one value and MetricName gives them one family name; a row that
	// has both (there is no such major) reads the preferred one.
	BlockReadMs  float64
	BlockWriteMs float64

	// Min, Max and Stddev complete dto.LatencyView's four named stats; Mean is
	// derived from Total/Calls and is not stored. StddevExecTimeMs is a pointer
	// because the view reports NULL stddev when the row has no sample, and the
	// store must tell "no sample" (stored as -1) from "0" — the same
	// not-available-vs-zero distinction as rollupTable's lock_wait_ms sentinel.
	MinExecTimeMs    float64
	MaxExecTimeMs    float64
	StddevExecTimeMs *float64

	// Plan is R3.10's captured plan for this statement, nil when this round
	// captured none. R3.10's sampling is bounded, so most rounds capture none
	// for most queries and nil is the common case, not the error case.
	//
	// The pg_stat_statements collector always leaves it nil: a plan is not a
	// column of the view, and R3.10's capture is the API plane's work. The field
	// is on this type because the store's row and the collector's row are one
	// type, and a type that exists twice is the second copy of a contract this
	// repo refuses.
	Plan *dto.PlanSampleView
}

// QuerySource is the (instance, database) a snapshot came from.
//
// It embeds Target — namespace, cluster, pod, role, address, source, pg major —
// because that is R9.3's instance identity and the store must key on the same
// triple the scheduler and the sink key on (A1's sink.go: "no two distinct
// triples can produce the same key"). Datname is the addition the target cannot
// carry: pg_stat_statements is per-database, so one pod yields one snapshot per
// database it hosts, and R9.4's row identity names the database.
type QuerySource struct {
	Target
	Datname string
}

// Snapshot is one collector round's rows for one source.
//
// Every field is something the collector knows: Source is the Target the round ran
// against plus the datname the rows carry, CapturedAt is the row's
// pgsi_source_now (R13.11's instance clock, which Project appends to every
// query), IngestedAt is SQLConfig.Now, StatsReset is the info view's value for
// that database, and Rows are the rows.
//
// That last pairing is the reason the type is here rather than being assembled by
// the caller. store.Snapshot's StatsReset is R3.3's counter epoch —
// "every rate is current − previous snapshot, with the snapshot's
// pg_stat_statements_info.stats_reset (PG14+) checked — a reset must reset the
// baseline rather than produce a negative or wrapped rate. On PG13, which has no
// stats_reset in the info view, fall back to detecting counter decreases per
// row." Zero means "no epoch available", which selects that fallback rather than
// disabling reset detection. A collector that returned rows without the epoch
// would leave its caller with no way to tell "no epoch" from "we did not look",
// which is the difference between two different behaviours of the same rule.
type Snapshot struct {
	Source     QuerySource
	CapturedAt time.Time
	IngestedAt time.Time

	// StatsReset is pg_stat_statements_info.stats_reset (PG14+; the view has no
	// row on PG13). See the type doc for what R3.3 does with it.
	StatsReset time.Time

	Rows []StatementRow
}

// StatementsCollector is §3.3's statement collector.
//
// It is the collector whose product is two things at once: the R9.5 metric family
// and the R3.2 rows. The Collector interface can only return the first, so
// CollectStatements returns both and Collect adapts one to the other — the
// activity.go shape, for the same stated reason.
type StatementsCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
	norm   query.Options
	topN   int
}

// NewStatementsCollector builds the collector on top of an opener (R2.7's
// transaction source) and a config (R2.7's timeout, R2.8's application name,
// R13.11's clock), with every unset field resolved by SQLConfig.withDefaults.
//
// The top-N window defaults to R9.5's 50 and the normalizer to query.Options{}
// (R3.6's default 1024-char cap); WithTopN and WithNormalizer change them.
func NewStatementsCollector(opener SQLOpener, cfg SQLConfig) *StatementsCollector {
	c := &StatementsCollector{
		opener: opener,
		cfg:    cfg.withDefaults(),
		norm:   query.Options{},
		topN:   DefaultTopN,
	}
	c.desc = Descriptor{
		Name:     CollectorStatement,
		Interval: StatementInterval,
		Source:   SourceSQL,
		// CostExpensive, which is the class collector.go names this collector for:
		// "a large result set, a multi-view group, or an interval short enough
		// that its per-round cost is paid four times a minute: the statements and
		// activity collectors." A pg_stat_statements round returns every
		// normalised statement in the database — thousands of rows, the largest
		// payload of the five §3.3 collectors — and R2.3's --disable-collector
		// "exists mostly so a production user can turn this class off", which is
		// the same lever R9.5's N exists for on the export side.
		Cost: CostExpensive,
		// AppliesTo is the all-views rule over statementViews; see that var's doc
		// for why it lands on PG 14–18.
		AppliesTo: AppliesToViews(statementViews...),
		// Timeout unset: R2.1's 10 s default, for the reason database.go and
		// settings.go give — the round timeout is a hang guard and R2.7's
		// statement_timeout is the tuning knob. It is also the right default here
		// for a reason the others do not have: the expensive class is the one a
		// raised statement_timeout would help, and that knob is SQLConfig's, not
		// the Descriptor's.
		ViewGroups: []string{statementsViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *StatementsCollector) Descriptor() Descriptor {
	return c.desc
}

// WithTopN sets the R9.5 window and returns the collector for chaining.
//
// It clamps rather than rejecting: n < 0 becomes 0 (R9.5's floor, which disables
// the family) and n > MaxTopN becomes MaxTopN (its ceiling). The exported
// ValidTopN is what a config layer uses to *reject* 1001; a constructor that
// silently accepted a number outside the documented range and a collector that
// exported 1001 series are both worse than a clamp the doc states.
func (c *StatementsCollector) WithTopN(n int) *StatementsCollector {
	switch {
	case n < 0:
		n = 0
	case n > MaxTopN:
		n = MaxTopN
	}
	c.topN = n
	return c
}

// WithNormalizer sets the query.Options the R9.5b fingerprint label is computed
// with, and returns the collector for chaining.
//
// The single reason this exists is the store's sentence: "the store normalizes it
// here, and this is the package's only normalization site: R3.5's rules and R3.6's
// length cap applied in one place is what keeps the fingerprint the API serves
// identical to the one the collector stored." The store reads
// store.Config.Normalizer; if a deployment sets R3.6's Enterprise-Plus 10 000 and
// the collector keeps the 1 024 default, the metric's fingerprint and the API's
// fingerprint are two hashes of one statement and the family no longer joins to
// the API, which is the entire content of R9.5b. Passing the same Options to both
// is what keeps that sentence true.
//
// A zero Options is the default, not a zero-length template: query.Normalize
// applies query.Options.withDefaults itself, so Options{} means R3.6's 1 024 and
// not "empty string".
func (c *StatementsCollector) WithNormalizer(opts query.Options) *StatementsCollector {
	c.norm = opts
	return c
}

// projections builds one viewProjection per view the collector reads, and it is
// the same three checks every collector makes: a major to build from, an
// applicability answer, and a projection that validates.
//
// The reason this collector is a file in package collector, rather than in a
// package of its own, is the framework's own location: Collector, Project, Tx and
// SQLConfig all live in sql.go, and the two subpackages that exist (pgversion,
// cnpgmetrics) hold data — the matrix and the registry — not collectors. R2.5's
// ruling exported ValidateProjection and retired the sentence that named its
// unexportedness as the constraint; sql.go:471-477 records that this function's
// stated justification had already gone stale once before (D20's S11), so the
// durable reason is written here rather than the one that expired.
func (c *StatementsCollector) projections(tgt Target) ([]viewProjection, error) {
	if tgt.PGMajor <= 0 {
		return nil, fmt.Errorf("collector %s: target %s has no PostgreSQL major, so R2.4 has no column set to build SQL from",
			CollectorStatement, tgt.Key())
	}
	if !c.desc.Applies(tgt.PGMajor) {
		names := make([]string, 0, len(statementViews))
		for _, v := range statementViews {
			names = append(names, string(v))
		}
		return nil, fmt.Errorf("collector %s: not applicable on PostgreSQL %d (one of [%s] has no column set there)",
			CollectorStatement, tgt.PGMajor, strings.Join(names, " "))
	}

	out := make([]viewProjection, 0, len(statementViews))
	for _, v := range statementViews {
		p, ok := Project(v, tgt.PGMajor, statementSpecs[v])
		if !ok {
			return nil, fmt.Errorf("collector %s: no projection for %s on PostgreSQL %d", CollectorStatement, v, tgt.PGMajor)
		}
		if err := ValidateProjection(p); err != nil {
			return nil, err
		}
		out = append(out, viewProjection{view: v, Projection: p})
	}
	return out, nil
}

// Collect is the Collector interface method: the metrics half of R9.1's split.
//
// It runs the round through CollectStatements and renders the R9.5 family from
// the rows that round returned, so a metric value is always derivable from a row
// the API can also show. The rows themselves are what the store wants and the
// interface has nowhere to put them, which is why the interface method drops them
// and the typed method returns them.
func (c *StatementsCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	snaps, res, err := c.CollectStatements(ctx, tgt)
	if err != nil {
		return res, err
	}
	res.Samples = append(res.Samples, c.samplesFor(snaps)...)
	return res, nil
}

// CollectStatements runs one round and returns one Snapshot per database the
// round covered, plus the round's accounting.
//
// The return is a slice because pg_stat_statements is per-database and a Target is
// per-pod. Today the opener connects to one database and the slice has one
// element; when the round loop fans out (A1's SQLConfig.Database), each
// transaction's rows name their own database through the datname Extra and the
// slice grows without this signature changing. That is the reason the database is
// read from the rows rather than from the config.
//
// The error paths return the round-trips issued so far, because the Collector
// contract requires it and R13.1's budget is measured from Result.RoundTrips.
func (c *StatementsCollector) CollectStatements(ctx context.Context, tgt Target) ([]Snapshot, Result, error) {
	projections, err := c.projections(tgt)
	if err != nil {
		// Zero is correct: nothing was issued, because building the SQL is what
		// tells us there is nothing to issue.
		return nil, Result{}, err
	}

	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		return nil, Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w", CollectorStatement, err)
	}

	var (
		trips int
		// rowsByView holds each view's rows until the transaction commits. The
		// alternative — turning rows into samples inside the loop — is what the
		// other collectors do, and it cannot work here: the info view's row is
		// needed to fill the stat_statements rows' Snapshot.StatsReset, and the
		// two views' rows have to be paired before either becomes a snapshot.
		rowsByView = make(map[pgversion.View][]Row, len(projections))
	)
	for _, pv := range projections {
		rows, qerr := tx.Query(ctx, pv.Query)
		trips++
		if qerr != nil {
			_ = tx.Rollback()
			return nil, Result{RoundTrips: trips},
				fmt.Errorf("collector %s: query %s: %w", CollectorStatement, pv.view, qerr)
		}
		rowsByView[pv.view] = rows
	}
	if cerr := tx.Commit(); cerr != nil {
		_ = tx.Rollback()
		return nil, Result{RoundTrips: trips},
			fmt.Errorf("collector %s: commit: %w", CollectorStatement, cerr)
	}

	// The info view is the database list. It has exactly one row per database —
	// `dealloc` and `stats_reset`, no keys — so on a connection to one database it
	// answers with that database's epoch, and the datname Extra names it. Reading
	// the list from the info rows rather than from the statement rows is what
	// makes an empty database produce a snapshot: a database with no statements
	// yet has zero pg_stat_statements rows and one info row, and R3.14's
	// answer-not-available-vs-zero rule needs the store to record the empty round
	// rather than skip it.
	resets := make(map[string]time.Time)
	order := make([]string, 0, len(rowsByView[pgversion.ViewStatStatementsInfo]))
	for _, r := range rowsByView[pgversion.ViewStatStatementsInfo] {
		dn, ok := r.Get("datname")
		if !ok || dn == "" {
			continue
		}
		if _, seen := resets[dn]; seen {
			continue
		}
		resets[dn], _ = parseTimestamp(mustGet(r, "stats_reset"))
		order = append(order, dn)
	}

	// Group the statement rows by the database they came from.
	grouped := make(map[string][]StatementRow, len(order))
	for _, r := range rowsByView[pgversion.ViewStatStatements] {
		dn, ok := r.Get("datname")
		if !ok {
			// A row with no datname is a projection that lost the Extra. It is
			// not a database, so it becomes its own group keyed "" and the store
			// rejects it with its own message ("snapshot source has no datname")
			// rather than the collector silently dropping a row it did read.
			dn = ""
		}
		grouped[dn] = append(grouped[dn], statementRowFrom(r))
		if _, seen := resets[dn]; !seen {
			order = append(order, dn)
		}
	}

	snapshots := make([]Snapshot, 0, len(order))
	for _, dn := range order {
		snap := Snapshot{
			Source:     QuerySource{Target: tgt, Datname: dn},
			CapturedAt: c.cfg.Now(),
			IngestedAt: c.cfg.Now(),
			StatsReset: resets[dn],
			Rows:       grouped[dn],
		}
		// R13.11's CapturedAt is the instance clock the rows were measured
		// against, not this process's clock: Project appends
		// `now() AS pgsi_source_now` to every query precisely so a collector on a
		// pod with clock drift can still report when the instance measured. The
		// rows in one transaction share one value, so the first row's is the
		// round's. SQLConfig.Now stays the source for IngestedAt, which is the
		// field R13.11 wants the local clock in.
		if rows := rowsByView[pgversion.ViewStatStatements]; len(rows) > 0 {
			if st, ok := parseTimestamp(mustGet(rows[0], SourceNowColumn)); ok {
				snap.CapturedAt = st
			}
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, Result{RoundTrips: trips}, nil
}

// statementMember is one member of R9.5's per-query family.
//
// It names a column and reads a StatementRow field, which is the narrowest shape
// that keeps the family a pure function of the row type: the metric and the API
// answer cannot disagree, because there is one parse (statementRowFrom) and the
// family reads what that parse produced. The alternative — re-reading the Row here
// — would put a second parse on the path between a row and its metric.
//
// The limit that comes with it is worth stating next to R3.14's
// answer-not-available-vs-zero rule: StatementRow's four counters are plain
// float64s with no way to say "absent" (only StddevExecTimeMs is a pointer, and
// for the reason its doc gives), so a row whose `rows` cell the driver returned as
// unparseable exports 0 rather than nothing. That is the shared row type's answer,
// not this file's invention, and it is the same answer the store gives for the
// same field.
type statementMember struct {
	// column is the spelling MetricName is called with to get the family name.
	// It is the canonical spelling rather than the per-major one because
	// MetricName canonicalises first (naming.go's rule 1): PG 13's total_time and
	// PG 14+'s total_exec_time are one value under two names — succession.go's
	// first enumerated rename, and naming.go:155 gives both the same family name —
	// so the family a row belongs to does not depend on which major produced it.
	column string
	// value reads the member's value off a row, already in the family's unit.
	value func(StatementRow) float64
}

// statementMembers is R9.5's member list, in the order A3's S25 enumerates it.
//
// R9.5: "**Ruling (S25, from A3's review of the Phase 3/6b surface): this cell's
// open question — which metrics are in the family — is answered `execution_time`,
// `rows`, `shared_blocks_hit`, `shared_blocks_read`, and not `lock_time`.**" The
// four are the R3.2 per-query sources that exist per query; lock_time is withheld
// here and admitted at R9.4's granularity instead, on the granularity-specific
// argument that cell gives (the divergence table renders per-query lock_time `n/a`
// and sources it at database/user level).
//
// `calls` is not in the list either, even though naming.go:130 has a row for it
// ("R3.2's Load column; R9.5's top-N ranking key"). A3's S25 closed this cell's
// list at four members and `calls` is not one of the four it names; the comment's
// "ranking key" claim is also not what R9.5 ranks by — see topN's doc for the
// ranking rule.
//
// The count gets one more sentence because two cells disagree about it. §5:544's
// per-query row budgets "N per cluster × 6 metric series = 50 × 6 = **300**", and
// R9.4's *aggregate* list is now six members — the four A3's S25 named plus
// lock_time and io_time, both admitted to that cell by later rulings. A3's S25
// closed this cell at four and says so in terms ("the four named members are the
// R3.2 per-query sources that exist per query"), and R9.4 protects the separation
// itself: "the two families declare their member lists separately", and "A3's S25
// closed R9.5's *per-query* member list". So this file implements four and §5's 300
// is the pre-ruling number. The discrepancy is A3's to rule on; it is not resolvable
// from here by picking a different count, and a collector that exported six would be
// minting two members A3's S25 withheld.
var statementMembers = []statementMember{
	{column: "total_exec_time", value: func(r StatementRow) float64 { return r.TotalExecTimeMs / msPerSecond }},
	{column: "rows", value: func(r StatementRow) float64 { return r.RowsReturned }},
	{column: "shared_blks_hit", value: func(r StatementRow) float64 { return r.SharedBlocksHit }},
	{column: "shared_blks_read", value: func(r StatementRow) float64 { return r.SharedBlocksRead }},
}

// msPerSecond is the scale between the view's time columns and the family's unit.
//
// pg_stat_statements reports every time column in milliseconds on every major
// R13.9 covers — PG 13's total_time and PG 14+'s total_exec_time are both ms, the
// rename changed the name and not the unit — and R9.4's family name is
// query_execution_time_**seconds**_total. The store keeps the millisecond value
// (StatementRow.TotalExecTimeMs is named for it, and dto.LatencyView is in ms);
// the metric divides. A collector that skipped this would export a cluster that
// spent 1.5 s in one query as pgsi_query_execution_time_seconds_total=1500.
const msPerSecond = 1000

// samplesFor renders the R9.5 family for a whole round.
//
// It takes every snapshot the round produced, not one snapshot, because the window
// is per cluster and a round's snapshots are the widest view of one instance the
// Collector interface allows. R9.5:397: "default **N=50 per cluster** (see §5 —
// `metrics.topN` is per cluster, **not per database**)". Applying N inside the
// per-database loop — the shape every other collector has, since the others have
// one snapshot's worth of rows to render — would give a pod hosting three
// databases 3 × 50 exported rows, which is the exact misreading the parenthetical
// exists to prevent and which the fan-out A1's SQLConfig.Database opens up makes
// live. The interface's unit is the Target and the config's unit is the cluster,
// so a per-round window is the closest the interface gets: on a primary-plus-
// replica cluster each target ranks its own rows, and no collector can see two
// targets in one Collect call.
//
// The window is applied here rather than in CollectStatements because the store
// needs every row (R3.2's top-N is a query over stored history, and R3.3's deltas
// need rows that fell out of this round's window to keep their baseline) while
// R9.5's family is "for the top-N only". The ranking rule is the one R10.7's
// ruling names as R9.5's — "Choosing by `total_exec_ms` is the rule R9.5 already
// applies to pick which queries to export, and internal/store/statements.go:384
// applies it inside the store for the same reason; naming it here stops a second
// implementation choosing alphabetically" — with the fingerprint and then the
// queryid as tie-breaks so a round with two equal rows exports a stable set.
//
// N=0 disables the family (R9.5's own sentence) without disabling the round: the
// rows still come back from CollectStatements and the round still counts its
// round-trips, because the collector's job is the feed and R9.5's lever bounds only
// what gets exported from it.
//
// One series set per row, not per fingerprint. R9.5b makes the fingerprint the
// identifier and `queryid` the secondary label, and §5:544's per-query row lists
// both as the series identity ("`…,fingerprint,queryid`"), so two rows that share
// a fingerprint — the same statement run by two roles, which the store keeps as
// two rows because its own key is (fingerprint, usename) — have different label
// sets here and need no collapsing. That matters for the reason the R9.7 ruling
// gives for summing pg_stat_wal instead of emitting per database: N rows against
// one label set is "N samples with identical label sets: an invalid exposition
// whose surviving value is whichever row the driver returned last" (D20's S8
// class). `queryid` is what keeps this family out of that class.
func (c *StatementsCollector) samplesFor(snaps []Snapshot) []Sample {
	if c.topN == 0 {
		return nil
	}

	// One entry per row, carrying the snapshot it came from so the sample's three
	// timestamps are that database's, and its fingerprint, which the ranking needs
	// before the emission loop gets it. Normalizing is the expensive part of this
	// function, so it happens once per row here rather than once per member below.
	type entry struct {
		snap *Snapshot
		row  StatementRow
		fp   string
	}
	all := make([]entry, 0, len(snaps)*8)
	fingerprints := make(map[string]string)
	for i := range snaps {
		snap := &snaps[i]
		for _, r := range snap.Rows {
			fp, ok := fingerprints[r.SQL]
			if !ok {
				fp = query.Normalize(r.SQL, c.norm).Fingerprint
				fingerprints[r.SQL] = fp
			}
			all = append(all, entry{snap: snap, row: r, fp: fp})
		}
	}
	if len(all) == 0 {
		return nil
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].row.TotalExecTimeMs != all[j].row.TotalExecTimeMs {
			return all[i].row.TotalExecTimeMs > all[j].row.TotalExecTimeMs
		}
		if all[i].fp != all[j].fp {
			return all[i].fp < all[j].fp
		}
		return all[i].row.QueryID < all[j].row.QueryID
	})
	if len(all) > c.topN {
		all = all[:c.topN]
	}

	samples := make([]Sample, 0, len(all)*len(statementMembers))
	for _, e := range all {
		labels := map[string]string{"fingerprint": e.fp}
		// R9.5b: "The per-query family's identifier label is the **fingerprint**
		// (canonical, joins to the API), with `queryid` as a secondary
		// informational label." The identity labels (namespace, cluster, pod,
		// instance_role) are deliberately absent — the scheduler's
		// ApplyIdentityLabels adds them and wins the four keys, which is why
		// every collector here sets only series-specific labels.
		if e.row.QueryID != "" {
			labels["queryid"] = e.row.QueryID
		}
		for _, m := range statementMembers {
			samples = append(samples, Sample{
				Metric:     MetricName(pgversion.ViewStatStatements, m.column),
				Labels:     labels,
				Value:      m.value(e.row),
				SourceTime: e.snap.CapturedAt,
				IngestTime: e.snap.IngestedAt,
				StatsReset: e.snap.StatsReset,
			})
		}
	}
	return samples
}

// statementRowFrom turns one raw row of the pg_stat_statements projection into a
// StatementRow.
//
// It reads the twelve fields the type has and no others. The projection has
// between 33 and 52 columns depending on the major (matrix_data.go's sets), and
// the type's doc states which ones it takes — the view's projection minus the
// columns no DTO renders — so this function is that sentence executed, not a
// per-column classification. database.go needs a columnKind classifier because its
// families come from column names and it has to decide per column whether a column
// is a value; here the field set is fixed by a type that already exists, so a new
// column in the matrix (a PG19 pg_stat_statements addition) is by definition not a
// new field and needs no change here.
//
// The two per-major pairs are resolved by preferring the PG 17+ spelling and
// falling back to the PG 15–16 one:
//
//   - total_exec_time, then total_time (succession.go's first enumerated rename);
//   - shared_blk_read_time/shared_blk_write_time, then
//     blk_read_time/blk_write_time (the pair R3.2's derivation names at
//     dto.LoadColumns, and the reason StatementRow has one BlockReadMs rather than
//     four columns).
//
// A field whose column the row does not have is left at its zero value, and
// StddevExecTimeMs stays nil — which is the type's own spelling for "no sample",
// not a fallback.
func statementRowFrom(row Row) StatementRow {
	r := StatementRow{
		QueryID: mustGet(row, "queryid"),
		SQL:     mustGet(row, "query"),
		// The usename Extra's alias. pg_get_userbyid returns NULL for a dropped
		// role, and the driver gives an empty string for a NULL, which is the
		// "empty means unresolved" case the field's doc names.
		Usename: mustGet(row, "usename"),
	}
	if v, ok := firstFloat(row, "total_exec_time", "total_time"); ok {
		r.TotalExecTimeMs = v
	}
	if v, ok := firstFloat(row, "rows"); ok {
		r.RowsReturned = v
	}
	if v, ok := firstFloat(row, "shared_blks_hit"); ok {
		r.SharedBlocksHit = v
	}
	if v, ok := firstFloat(row, "shared_blks_read"); ok {
		r.SharedBlocksRead = v
	}
	if v, ok := firstFloat(row, "shared_blk_read_time", "blk_read_time"); ok {
		r.BlockReadMs = v
	}
	if v, ok := firstFloat(row, "shared_blk_write_time", "blk_write_time"); ok {
		r.BlockWriteMs = v
	}
	if v, ok := firstFloat(row, "min_exec_time"); ok {
		r.MinExecTimeMs = v
	}
	if v, ok := firstFloat(row, "max_exec_time"); ok {
		r.MaxExecTimeMs = v
	}
	// A pointer, because the view reports NULL stddev when the row has no sample
	// and the store has to tell that from a 0. An absent or unparseable cell is
	// nil; a real 0 is &0.
	if v, ok := firstFloat(row, "stddev_exec_time"); ok {
		r.StddevExecTimeMs = &v
	}
	if v, ok := firstInt(row, "calls"); ok {
		r.Calls = v
	}
	return r
}

// firstFloat returns the value of the first of cols the row has and can parse.
//
// It is the parse every field on StatementRow goes through, which is what keeps
// one row parsed once: the metrics family reads the struct, and so does the store.
// settings.go's mustGet is the same idea for a string; this is its numeric half,
// shared with the multi-spelling lookup the two per-major pairs need.
func firstFloat(row Row, cols ...string) (float64, bool) {
	for _, c := range cols {
		raw, ok := row.Get(c)
		if !ok {
			continue
		}
		if v, ok := parseValue(raw); ok {
			return v, true
		}
		// Present and unparseable: stop rather than try the next spelling. The
		// callers' multi-spelling lists are alternates for one value, so a value
		// that exists but does not parse is a parse failure, not a hint that the
		// other spelling is the live one.
		return 0, false
	}
	return 0, false
}

// firstInt is firstFloat for the columns the store keeps as int64.
//
// It parses through parseValue and converts, because parseValue is the package's
// one numeric parser (it is what handles the boolean columns' t/f spellings) and a
// second ParseFloat call site would be a second answer to "what is a number here".
// The conversion is exact for every value pg_stat_statements puts in a bigint
// counter that fits a float64, which is every counter below 2^53; a calls column
// past that is a statement executed nine quadrillion times.
func firstInt(row Row, cols ...string) (int64, bool) {
	v, ok := firstFloat(row, cols...)
	if !ok {
		return 0, false
	}
	return int64(v), true
}
