// rollups.go is R3.7's per-dimension rollups and R3.12's tags: the write side
// (PutDimensionRollups) and R8.2's tags read (Tags).
//
// The two halves are one file because they are one table. schema.go's
// dimension_rollup holds R3.7's four rollup kinds and R3.12's tag kind under one
// PK, and the reason it can is the reason they belong together here: a tag *is* a
// dimension set, and R3.7's database/user/application rows are the same shape with
// fewer fields populated. Splitting them into two files would mean two places that
// know the column list and one table that has to agree with both.
//
// # Who calls the write path, and why it takes data
//
// schema.go states the rule this file follows: "their writer is R3.12's
// comment-grammar parser -- not this phase's statement store, which is why the
// write path (PutDimensionRollups) takes the dimensions as data rather than
// growing a tag-specific method the parser's owner has not asked for." So
// PutDimensionRollups takes rows. R3.12's /* pgsi:… */ parser, when R7's owner
// writes it, becomes a caller: it parses a comment into a Dimensions, the caller
// pairs it with a cluster and a minute, and this file never learns whether the
// dimensions came from a comment, from a pg_stat_statements row, or from a test.
//
// # Which api interface Tags satisfies, and when
//
// QueryList and QueryDetail each carry a var _ assertion against the api interface
// they implement, and this one cannot yet: internal/api has no tags route. The
// route exists in the spec (R8.2:377 lists /insights/tags) and in dto's route
// table (dto/queries.go:9: "GET /api/v1/insights/tags → Envelope[TagView] (R3.12)")
// and in api/x-planned-paths.yaml:39, but no handler has been written, so there is
// no interface to name. The signature is the one that follows from those three:
// Envelope[TagView] means the handler pages it, and every paged list route takes
// the shared api.ListQuery — so Tags takes that and returns []dto.TagView. When
// A2 writes the route, the assertion goes in with it; if the signature that
// ships differs, this file changes by one line and the rows it reads do not change
// at all.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// The one-line assertion the header's "Which api interface Tags satisfies, and when"
// section promised: "When A2 writes the route, the assertion goes in with it; if the
// signature that ships differs, this file changes by one line and the rows it reads do
// not change at all." The route landed as api.TagsSource with exactly the predicted
// signature — Tags(ctx, api.ListQuery) ([]dto.TagView, error) — so this is that one
// line and nothing else in this file moved.
var _ api.TagsSource = (*Store)(nil)

// The dimension kinds R3.7 and R3.12 name.
//
// R3.7:311 names four — "which database, which user, which application name,
// which client address accounts for the most load" — and R3.12:316 adds the fifth,
// tags, from the /* pgsi: */ comment grammar. The strings are what the
// dimension_rollup.kind column stores and what R8.2's routes put in their URLs, and
// this is the first Go spelling of the vocabulary rather than a copy of one: the
// schema has the kinds only in SQL prose and comments, and dto has no dimension-kind
// type because its views are per-kind (TagView, and the Databases/Users routes' own
// types) rather than keyed on one.
const (
	KindDatabase      = "database"
	KindUser          = "user"
	KindApplication   = "application"
	KindClientAddress = "client_address"
	KindTag           = "tag"
)

// validKind is the gate PutDimensionRollups runs before writing anything.
//
// The five are an exhaustive list, not an open set: a kind outside them is a row
// the schema can store (the column is a plain TEXT with no CHECK) and no route can
// serve, which is the definition of stored-and-unreachable that this schema has
// been built to avoid. Adding a sixth kind means adding it here and to a route in
// the same change.
func validKind(kind string) bool {
	switch kind {
	case KindDatabase, KindUser, KindApplication, KindClientAddress, KindTag:
		return true
	}
	return false
}

// Dimensions is the dimension set of one rollup row.
//
// The field set is R3.12's, not R3.7's, and the two differ by one: R3.12's tag
// grammar names application, usename and datname plus the five app-supplied keys,
// while R3.7's list has a client address and no five keys. The union is what the
// table has columns for. Which fields are populated is a function of Kind — a
// KindDatabase row has Datname and nothing else, a KindTag row has whatever the
// comment named plus R3.12's (a) three — and validate is what enforces that.
//
// One struct for all five kinds rather than a type per kind, because the table is
// one table with one PK and because R3.12's whole point is that its (a) and (b)
// field groups are one tag: the digest hashes all eight keys together, and a type
// per kind would have to decide which subset hashes, which is the question the
// requirement answers.
type Dimensions struct {
	Kind string

	// R3.12's (a): the always-available dimensions.
	Application string
	Datname     string
	Usename     string

	// R3.12's (b): the five keys the comment grammar names, empty unless the app
	// opted in. DBDriver is the comment's `dbdriver` spelling; dto's field is
	// `dbDriver` for the metric-side reason its doc gives, and dto's TagDigest is
	// the comment-side one — this struct follows the comment, because the digest
	// has to be computed from one spelling and that is the one the parser produces.
	Action     string
	Route      string
	Controller string
	Framework  string
	DBDriver   string

	// R3.7's fourth dimension and R6.4's scoped one. Populated only for
	// KindClientAddress, which R3.13's --record-client-address gates.
	ClientAddress string
}

// tagView is the label half of dto.TagView for these dimensions.
//
// The load columns are left zero: TagDigest reads only the labels, and the read
// path fills the rest from the stored row. Building the DTO here rather than
// hashing the struct's fields directly is the point — dto.TagDigest is the
// requirement's definition of tag_hash and it takes a TagView.
func (d Dimensions) tagView() dto.TagView {
	return dto.TagView{
		Application: d.Application,
		Database:    d.Datname,
		User:        d.Usename,
		Action:      d.Action,
		Route:       d.Route,
		Controller:  d.Controller,
		Framework:   d.Framework,
		DBDriver:    d.DBDriver,
	}
}

// digest is the value for dimension_rollup.dim_hash.
//
// For the four tag-shaped kinds it is dto.TagDigest — R3.12's tag_hash, the SHA-256
// of the populated keys as sorted key=value pairs — and calling that function
// rather than hashing here is the whole point. dto's doc makes the digest
// load-bearing ("two apps that agree on the dimensions agree on the tag, which is
// the only reason a tag can be R9.6's metric key and R8.2's URL segment at the same
// time"), and a store that hashed the struct's fields in struct order would give
// `/* pgsi:route=/x,action=y */` and `/* pgsi:action=y,route=/x */` different
// hashes and silently split every series R9.6 exports.
//
// What goes into the hash is the kind's own field set, not the whole struct
// (A3's S45). A KindDatabase row is a rollup of one database, so its tag is
// `database=db1`; hashing the struct's other fields in as well made one database
// rollup hash two different ways depending on whether the caller happened to also
// fill in a usename — which is the same series-splitting the paragraph above spends
// itself on, arrived at from the other direction. The pairing with validate is what
// makes the change safe to land: validate now rejects the fields a kind does not
// own, so no row that used to be accepted changes hash. The rows whose digest moves
// are exactly the rows that can no longer be written.
//
// KindClientAddress is the exception, and it is not a second copy of tag_hash:
// R3.12's key set has no client address in it (Cloud SQL's pertag list has none, and
// R3.12's (a) adds only application/usename/datname), so TagDigest of a
// client-address row would hash the empty set and give every address in the cluster
// the same hash. The address's own SHA-256 is its identity, in the same 64-hex shape
// so the column holds one kind of string for every kind of row.
func (d Dimensions) digest() string {
	switch d.Kind {
	case KindDatabase:
		return dto.TagDigest(dto.TagView{Database: d.Datname})
	case KindUser:
		return dto.TagDigest(dto.TagView{User: d.Usename})
	case KindApplication:
		return dto.TagDigest(dto.TagView{Application: d.Application})
	case KindClientAddress:
		sum := sha256.Sum256([]byte(d.ClientAddress))
		return hex.EncodeToString(sum[:])
	default: // KindTag, the only kind left once validKind has run.
		return dto.TagDigest(d.tagView())
	}
}

// namedField is one dimension field paired with the key dto's TagDigest gives it,
// so the exclusivity check below and the digest above name fields the same way the
// hash does rather than inventing a third spelling.
type namedField struct{ name, value string }

// allFields is every dimension field the struct has: the eight tag keys plus the
// client address R3.7 adds.
func (d Dimensions) allFields() []namedField {
	return []namedField{
		{"application", d.Application},
		{"database", d.Datname},
		{"user", d.Usename},
		{"action", d.Action},
		{"route", d.Route},
		{"controller", d.Controller},
		{"framework", d.Framework},
		{"dbdriver", d.DBDriver},
		{"client_address", d.ClientAddress},
	}
}

// allowsField is the exclusivity table: which fields each kind may populate. A
// function with an exhaustive switch rather than a map, so a sixth kind added to
// validKind without a case here answers false to everything and fails loud on its
// first row instead of quietly inheriting "everything allowed".
func allowsField(kind, name string) bool {
	switch kind {
	case KindDatabase:
		return name == "database"
	case KindUser:
		return name == "user"
	case KindApplication:
		return name == "application"
	case KindClientAddress:
		return name == "client_address"
	case KindTag:
		// All eight tag keys and only them: the client address is R3.7's fourth
		// dimension, not one of R3.12's.
		return name != "client_address"
	}
	return false
}

// validate is the gate on the write path: the kind has to be one of the five, the
// kind's own dimension has to be present, and no field outside the kind's set may be
// populated.
//
// The second half is not pedantry. An empty tag-shaped set digests to the SHA-256
// of the empty string (dto.TagDigest drops empty values and sorts nothing), which is
// a real hash meaning "no tag at all" — a row of nothing, filed under a hash no
// caller will ever ask for. A KindDatabase row with no Datname is worse than empty:
// it is a database rollup for no database, and it will be summed with the real ones.
//
// The third half (A3's S45) and the second are one contract, and digest() is why:
// the fields it hashes are exactly the fields the kind owns, so a populated field
// outside that set is a value that moves the hash without being part of the dimension
// the row claims to be. Checking presence alone left the struct's other fields
// writable, and the switch below answers "is the required field there" while the loop
// after it answers "is anything else there".
func (d Dimensions) validate() error {
	if !validKind(d.Kind) {
		return fmt.Errorf("store: unknown dimension kind %q", d.Kind)
	}
	empty := func(what string) error {
		return fmt.Errorf("store: dimension kind %q needs %s", d.Kind, what)
	}
	switch d.Kind {
	case KindDatabase:
		if d.Datname == "" {
			return empty("datname")
		}
	case KindUser:
		if d.Usename == "" {
			return empty("usename")
		}
	case KindApplication:
		if d.Application == "" {
			return empty("application")
		}
	case KindClientAddress:
		if d.ClientAddress == "" {
			return empty("client_address")
		}
	case KindTag:
		if d.tagView() == (dto.TagView{}) {
			return empty("at least one tag dimension")
		}
	}
	for _, f := range d.allFields() {
		if f.value != "" && !allowsField(d.Kind, f.name) {
			return fmt.Errorf("store: dimension kind %q cannot carry %s", d.Kind, f.name)
		}
	}
	return nil
}

// DimensionLoad is the eight measured columns a rollup row carries — the same eight
// query_rollup has, in the same units, and the raw form of what the read paths
// render.
//
// It is a separate type from dto.LoadColumns because the DTO is a rendering and this
// is a storage shape: LoadColumns has derived fields whose denominator the row does
// not carry (LoadByTotalTime is a share of the cluster), and it has no place for the
// shared-block counts or lock wait, which the table stores because R3.7 grants them
// at this level — "including Load by lock wait, which is available at this level
// from pg_locks + pg_stat_activity even though it is not available per query".
type DimensionLoad struct {
	Calls            int64
	TotalExecMs      float64
	CPUMs            float64
	IOWaitMs         float64
	RowsReturned     float64
	SharedBlocksHit  float64
	SharedBlocksRead float64
	LockWaitMs       float64
}

// DimensionRollup is one dimension's one-minute aggregate.
type DimensionRollup struct {
	Dimensions

	// Namespace and Cluster are which cluster this row belongs to, and they are
	// not part of the dimension set: the same /* pgsi:route=/orders */ tag in two
	// clusters is two rows (A3's S27 fix on schema.go, which added both columns to the
	// PK), and Dimensions has no cluster fields precisely so one tag's dimensions
	// can be computed once and written to each cluster it was seen in.
	Namespace string
	Cluster   string

	// Bucket is the minute the load belongs to, stored as the schema's
	// bucket_kind='minute' + bucket pair, the way query_rollup stores it.
	Bucket time.Time
	Load   DimensionLoad
}

// upsertDimensionSQL writes one rollup row.
//
// INSERT OR REPLACE, so the newest write for a (kind, namespace, cluster, dim_hash,
// minute) wins rather than the values summing. A rollup row is that minute's
// aggregate, so re-writing it with a corrected value must replace it; summing would
// double-count the minute on every re-run. It is sink_current's rule ("Publish
// replaces the stored sample set") applied to a table keyed by minute instead of by
// collector, and it matches insertRollupSQL's choice for the same reason.
const upsertDimensionSQL = `
	INSERT OR REPLACE INTO dimension_rollup
		(kind, namespace, cluster, dim_hash, application, datname, usename,
		 action, route, controller, framework, dbdriver, client_address,
		 bucket_kind, bucket,
		 calls, total_exec_ms, cpu_ms, io_wait_ms, rows_returned,
		 shared_blks_hit, shared_blks_read, lock_wait_ms)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'minute', ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// args is the row's 22 bind values, in the order upsertDimensionSQL lists its
// columns.
//
// bucket_kind is the one column with no placeholder: the const writes the literal
// 'minute', which is the same choice insertRollupSQL makes and for the same reason —
// §4's ruling (b) settled on minutes for the whole retention window, so a row that
// could name another kind would be a row the store cannot yet fill.
func (r DimensionRollup) args() []any {
	return []any{
		r.Kind, r.Namespace, r.Cluster, r.digest(),
		r.Application, r.Datname, r.Usename,
		r.Action, r.Route, r.Controller, r.Framework, r.DBDriver,
		r.ClientAddress,
		unix(r.Bucket),
		r.Load.Calls, r.Load.TotalExecMs, r.Load.CPUMs, r.Load.IOWaitMs,
		r.Load.RowsReturned, r.Load.SharedBlocksHit, r.Load.SharedBlocksRead,
		r.Load.LockWaitMs,
	}
}

// PutDimensionRollups writes a batch of rollup rows in one transaction.
//
// Batched with one prepared statement per batch rather than one Exec per row, for
// the reason PutSnapshot gives: a collector round produces hundreds of dimension
// rows and SQLite's per-statement cost dominates the work.
//
// Validation runs over the whole batch before the transaction opens, so a bad row in
// position 40 does not leave 39 rows written and the rest not — the all-or-nothing
// property PutSnapshot's doc argues for and the reason the tx is opened after the
// loop rather than around it.
func (s *Store) PutDimensionRollups(ctx context.Context, rows []DimensionRollup) error {
	if rows = s.cfg.filterRows(rows); len(rows) == 0 { // R3.13's gate, store.go:220
		return nil
	}
	for _, r := range rows {
		if err := r.Dimensions.validate(); err != nil {
			return fmt.Errorf("store: put dimension rollups: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin dimension rollups: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, upsertDimensionSQL)
	if err != nil {
		return fmt.Errorf("store: prepare dimension rollup: %w", err)
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.args()...); err != nil {
			return fmt.Errorf("store: put dimension rollup %s/%s: %w", r.Kind, r.digest(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit dimension rollups: %w", err)
	}
	return nil
}

// tagSelectSQL is R8.2's /insights/tags read: one row per tag in the window, with
// the window's minutes summed into the load columns.
//
// The labels are in the SELECT and the GROUP BY rather than re-derived from
// dim_hash, because the hash is not reversible and the DTO needs the values: R3.12's
// (a) three and (b) five are what the Tags tab renders, and dim_hash is only the key
// that groups them. Listing them in the GROUP BY is the same portability choice
// queryListGroupBy makes for the query table's columns.
const tagSelectSQL = `
SELECT d.dim_hash, d.namespace, d.cluster,
       d.application, d.datname, d.usename,
       d.action, d.route, d.controller, d.framework, d.dbdriver,
       SUM(d.calls)            AS calls,
       SUM(d.total_exec_ms)    AS total_exec_ms,
       SUM(d.cpu_ms)           AS cpu_ms,
       SUM(d.io_wait_ms)       AS io_wait_ms,
       SUM(d.rows_returned)    AS rows_returned,
       SUM(d.shared_blks_hit)  AS shared_blks_hit,
       SUM(d.shared_blks_read) AS shared_blks_read,
       SUM(d.lock_wait_ms)     AS lock_wait_ms
FROM dimension_rollup d
`

// tagSelectGroupBy closes the aggregate and orders the result by hash, so the
// store's own output is deterministic the way QueryList's is by fingerprint. The
// handler re-sorts with its own key; this order exists for the store's tests and for
// any caller that does not page.
const tagSelectGroupBy = `
GROUP BY d.dim_hash, d.namespace, d.cluster,
         d.application, d.datname, d.usename,
         d.action, d.route, d.controller, d.framework, d.dbdriver
ORDER BY d.dim_hash, d.namespace, d.cluster
`

// tagRow is one aggregated tag, before it becomes a dto.TagView.
type tagRow struct {
	// The label half of the DTO, filled straight from the row: TagView's label
	// fields are exactly the table's label columns, so there is no mapping here to
	// get wrong — which is the point of naming the struct the same way.
	dto.TagView

	// Which cluster the row belongs to. dto.TagView has no namespace/cluster
	// fields — the tag routes scope by query parameter instead of returning the
	// scope in the body, the way every R8.2 route does — so these two are not
	// copied into the DTO. They are here because the row needs them for one thing:
	// the LoadByTotalTime denominator is per cluster, and without them a tag seen
	// in two clusters is one summed row with no way to know which cluster's total
	// to divide by. Grouping by them makes the row's identity the table's PK minus
	// the bucket, which is the same rule queryListGroupBy follows for the query
	// table, and it means the same tag in two clusters is two rows with two
	// correct percentages rather than one row with a number that belongs to
	// neither.
	namespace string
	cluster   string

	calls                               int64
	totalExecMs, cpuMs, ioWaitMs        float64
	rowsReturned, sharedHit, sharedRead float64
	lockWaitMs                          float64
}

// view is the DTO for this tag against its cluster's total load.
//
// The LoadColumns come from queryListRow.loadColumns — built here from the row's
// five shared fields — because R3.7 requires "the same load columns as R3.2" of
// every rollup and one function is the only way to guarantee that. LockWaitMs is
// the field TagView adds on top: R3.2 withholds it from a per-query row and R3.7
// grants it here, and the comment on that field says a rollup that omitted it "would
// be missing the one column the spec says is available here and not there".
func (r tagRow) view(clusterTotal float64) dto.TagView {
	v := r.TagView
	v.LoadColumns = queryListRow{
		calls:        r.calls,
		totalExecMs:  r.totalExecMs,
		cpuMs:        r.cpuMs,
		ioWaitMs:     r.ioWaitMs,
		rowsReturned: r.rowsReturned,
	}.loadColumns(clusterTotal)
	v.LockWaitMs = r.lockWaitMs
	return v
}

// Tags is R8.2's /insights/tags read: every tag with rows in q's window.
//
// q.Limit is unused for the reason QueryList gives (R8.10's paging is the handler's,
// and a source that returned a page makes meta.total report the page size).
func (s *Store) Tags(ctx context.Context, q api.ListQuery) ([]dto.TagView, error) {
	from, to := s.window(q.From, q.To)

	// The denominator is the cluster's total query load, from query_rollup — the
	// same clusterLoad the list route uses, which is what keeps "share of the
	// cluster's total load" meaning one number across the three routes that report
	// it. The alternative denominator (the sum of all tags' load) is a different
	// quantity and a smaller one: R3.12's (b) tags exist only for the statements
	// whose apps opted in, so a tag-share-of-all-tags would report 100% for one
	// instrumented application in a cluster running a hundred uninstrumented ones.
	totals, err := s.clusterLoad(ctx, from, to, q.Namespace, q.Cluster)
	if err != nil {
		return nil, err
	}

	w := newWhere()
	w.eq("d.kind", KindTag)
	w.raw("d.bucket_kind = 'minute'")
	w.raw("d.bucket >= ? AND d.bucket <= ?", unix(from), unix(to))
	if q.Namespace != "" {
		w.eq("d.namespace", q.Namespace)
	}
	if q.Cluster != "" {
		w.eq("d.cluster", q.Cluster)
	}
	if q.DB != "" {
		w.eq("d.datname", q.DB)
	}

	rows, err := s.db.QueryContext(ctx, tagSelectSQL+w.String()+tagSelectGroupBy, w.Args()...)
	if err != nil {
		return nil, fmt.Errorf("store: tags: %w", err)
	}
	defer rows.Close()

	var found []tagRow
	for rows.Next() {
		var r tagRow
		if err := rows.Scan(
			&r.TagHash, &r.namespace, &r.cluster,
			&r.Application, &r.Database, &r.User,
			&r.Action, &r.Route, &r.Controller, &r.Framework, &r.DBDriver,
			&r.calls, &r.totalExecMs, &r.cpuMs, &r.ioWaitMs,
			&r.rowsReturned, &r.sharedHit, &r.sharedRead,
			&r.lockWaitMs,
		); err != nil {
			return nil, fmt.Errorf("store: tags scan: %w", err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: tags rows: %w", err)
	}

	out := make([]dto.TagView, 0, len(found))
	for _, r := range found {
		out = append(out, r.view(totals[clusterKey(r.namespace, r.cluster)]))
	}
	return out, nil
}
