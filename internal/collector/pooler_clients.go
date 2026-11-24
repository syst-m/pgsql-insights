package collector

import (
	"context"
	"fmt"
)

// CollectorPoolerClients is §3.3's second pooler name, and the `collector` label
// value on this collector's rounds.
//
// requirements.md:205's schedule row is one entry:
//
//	| `pooler` | 60 s | S5 + S6 | Only for clusters with a Pooler |
//
// and requirements.md:354's first A3 ruling splits that entry into two collectors
// — "two transports, two ports, two cost classes (an HTTP GET against the
// exporter's port vs a psql round-trip against 5432), and different applicability
// (R6.4's attribution applies only under transaction pooling; R6.1's applies to any
// Pooler). Those are two schedulable units". pooler.go is the S5 half; this file is
// the S6 half, and pooler.go:26-27 is where this file's name was promised.
//
// The second ruling is the one that fixes the spelling: pooler.go keeps `pooler`
// and this file takes `pooler-clients` — "hyphenated, matching instance-metrics and
// k8s-events in the same enumeration, not pooler_clients, which no multi-word name
// in the set uses". registry.go:95 is what forces two names (two collectors cannot
// register one, because one pgsi_collector_duration_seconds{collector=...} series
// cannot be fed by two transports), and the cost A3 accepted for the split is that
// §3.3's table keeps 13 rows while the name set goes to 14.
const CollectorPoolerClients = "pooler-clients"

// poolerClientsViewGroup is this collector's single R2.7 view group.
//
// The value is the collector's own name, which is the convention every SQL
// collector follows: activity.go:57's activityViewGroup is "activity",
// database.go:50's is "database", and locks.go, replication.go, settings.go,
// statements.go and wal.go each repeat it.
//
// What the group holds is not a view, and that is the one place this collector
// departs from the framework's meaning of the word. R2.7's group exists to say
// "these queries read one snapshot"; the snapshot here is pgbouncer's own client
// table, which is not a pg_catalog view and never will be. The group is still one
// group, one query, one transaction — which is the part R2.7's rule constrains — and
// the name is the part it does not, so the name follows the convention rather than
// the noun.
const poolerClientsViewGroup = "pooler-clients"

// showClientsQuery is the query this collector issues, verbatim.
//
// It is a literal and not a Project() result. Project (sql.go:251) assembles
// `SELECT <columns> FROM <schema>.<view>` from the pgversion matrix, and pgbouncer's
// admin commands are not matrix views: knownSchemas (sql.go:373) is the closed set
// {pg_catalog, pg_stat_statements}, and ValidateProjection (sql.go:483) rejects a
// projection whose FROM clause names a schema outside it. "SHOW CLIENTS" has no FROM
// clause at all, so R2.5's qualification rule has nothing here to check — the
// projection machinery is not being bypassed, it is not applicable, which is the
// same reason pooler.go's HTTP collector leaves ViewGroups empty rather than
// inventing a group of URL paths.
//
// The query string is a constant for the reason activity.go:107 makes
// ColumnBlockingPids one: it is referenced from the collector, the fake in the test,
// and the test that asserts the fake was asked for exactly it, and a query spelled
// twice is a query that drifts.
const showClientsQuery = "SHOW CLIENTS"

// Client is one row of pgbouncer's SHOW CLIENTS.
//
// Two of its fields are why the collector exists. requirements.md:357 (R6.4) scopes
// the attribution fix to "the R4.1 active-session list, via SHOW CLIENTS (S6)
// mapping each server backend to its real client address" — and the mapping is a
// pair of columns: Addr/Port is the real client, and Link is pgbouncer's own answer
// to which server connection that client is paired with (its documentation:
// "Address of server connection the client is paired with."). Link is the join key
// to the pg_stat_activity row whose client_addr shows the pooler instead of the
// client, which is the exact defect R6.4 exists to fix. The other nineteen fields
// are the row that came back with them.
//
// Every field is a string, including the two timestamps and the two counters. That
// is sql.go's Row type carried through rather than parsed, and it is deliberate for
// the same reason activity.go's Session keeps ClientAddress and ClientPort as
// strings ("formatting is the UI's job, not the collector's") — plus one reason
// specific to this source: pgbouncer renders its timestamps without the UTC offset
// sql.go's parseTimestamp (:344) requires in all six of its layouts, so a parse
// attempt returns false for every row and clock.go degrades the value to "no source
// clock". An unparsed timestamp is a value the UI can format; a parsed-to-zero one is
// a lie about 1970.
//
// The field list is asserted in pooler_clients_test.go against pgbouncer's
// documented column set rather than restated here, because a list in the product and
// a second list in a comment is a pair that drifts — and this file has no checker
// that would notice.
//
// Field spelling follows the column name in CamelCase, with one package rule
// inherited rather than invented: an initialism in a column name keeps the spelling
// the package already gave its counterpart. remote_pid becomes RemotePid because
// activity.go:149 and locks.go:131 both spell the same concept `Pid` — the one
// initialism this package declines to capitalize, and the one that recurs here. tls
// becomes TLS and id becomes ID because conn.go:105's SSLMode shows the package
// capitalizing every other initialism it meets, and there is no prior spelling of
// "tls" or "id" in the package to inherit. Where a second spelling would be free,
// the existing one wins; where there is none, Go's rule applies.
type Client struct {
	// Type is the row kind, and pgbouncer's docs give it as the single letter "C"
	// on a client row ("C, for client") — the column exists so SHOW SERVERS can
	// return "S" in the same shape. It is carried rather than filtered on, because
	// a filter on a constant is a check that cannot fail, and the letter is the
	// documented value while "client" is a guess about it.
	Type string
	// User and Database are the pgbouncer pool this client belongs to, and the
	// same two labels R9.3 puts on the S5 pooler series. R6.4's rollup groups by
	// client address, not by pool, so these are context rather than keys.
	User     string
	Database string
	// Replication is whether the client connection uses replication; the docs
	// give the value set as none, logical or physical, so a normal client's value
	// is the string "none" and not an empty string. Carried as the source renders
	// it, which is the rule for every field here.
	Replication string
	// State is the client's state in pgbouncer's own machine. The docs enumerate
	// it as active (linked to a server connection), idle (connected, no query
	// waiting), waiting, active_cancel_req and waiting_cancel_req. It is a string
	// and not an enum for the reason activity.go gives for Session.Status: the
	// source renames states between versions — this very set reads differently in
	// older releases, which listed LOGIN and BEING_CANCEL_REQ — so a closed enum
	// turns an upstream addition into a parse failure.
	State string
	// Addr and Port are the client's real address — the value R6.4 recovers, and
	// the value pg_stat_activity cannot show for a pooled connection because it
	// shows the pooler's address there instead.
	Addr string
	Port string
	// LocalAddr and LocalPort are the address on the pgbouncer side of the client
	// connection: the address pg_stat_activity.client_addr reports for the pooled
	// backend. Carrying both halves is what makes the fix visible — R6.4's
	// acceptance criterion (requirements.md:800) is that the two differ, "proving
	// attribution works on the surface where it is derivable".
	LocalAddr string
	LocalPort string
	// ConnectTime is when pgbouncer accepted the client connection, and
	// RequestTime when the client's current request started, both as pgbouncer
	// renders them (see the struct doc for why they are not parsed).
	ConnectTime string
	RequestTime string
	// Wait is the current waiting time in seconds and WaitUs the microsecond part
	// of it — one duration split across two columns by a source that has no
	// interval type. Both are kept as the strings that came back rather than
	// recombined, because the pair is only meaningful for a waiting client and
	// recombining two strings into a duration the UI then reformats is the same
	// formatting-over-data move the struct doc declines.
	Wait   string
	WaitUs string
	// CloseNeeded is, per the docs, "not used for clients" — the column exists for
	// the server side of the shared shape. Carried as the empty string it is.
	CloseNeeded string
	// Ptr is the "Address of internal object for this connection": pgbouncer's own
	// handle for the client. Together with Addr it is what makes a row identifiable
	// at all, which is why clientFromRow's drop rule tests both.
	Ptr string
	// Link is the join R6.4 is built on: "Address of server connection the client
	// is paired with." It is how a client row is matched to the pg_stat_activity
	// backend it is being served by — the field the whole collector exists to read.
	Link string
	// RemotePid is, per the docs, the "Process ID, in case client connects over
	// Unix socket and OS supports getting it" — a client-side OS pid, not the
	// Postgres backend pid, and empty for a TCP client. Carrying it as a string
	// rather than an int is what lets the empty case stay empty instead of
	// becoming a pid 0 that looks like a real process.
	RemotePid string
	// TLS is "A string with TLS connection information, or empty if not using
	// TLS" — a rendered protocol/cipher string, not a boolean, so a client with
	// TLS and a client without it are distinguishable rather than both false.
	TLS string
	// ApplicationName is the application_name the client set, empty if it set
	// none. The same value R4.1 shows for a session, and the reason R6.4's fix is
	// visible to a user: the app name was always right while the address was wrong.
	ApplicationName string
	// PreparedStatements is "The amount of prepared statements that the client has
	// prepared" — a count, kept as the string it arrived as (struct doc).
	PreparedStatements string
	// ID is pgbouncer's "Unique ID for client", stable for the life of the
	// connection. It is the row's own key, and the field the R4.1 join would prefer
	// over Ptr if a future revision of the docs makes it address-independent.
	ID string
}

// PoolerClientsCollector is §3.3's S6 half of the pooler row: pgbouncer's admin
// console over 5432, read for the client→server mapping R6.4 attributes with.
//
// It is the package's second collector whose product is not its samples, and the
// reason is the same one activity.go:248 gives for the first: R9.1's two-plane rule
// puts rows and mappings on the API side, so the interface method returns the
// metrics half and the rows come from a second method. This one has no metrics half
// at all — see Collect — which makes it the framework's purest case of a collector
// whose Collector interface method is a formality and whose real entry point is the
// other one.
type PoolerClientsCollector struct {
	opener SQLOpener
	cfg    SQLConfig
	desc   Descriptor
}

// NewPoolerClientsCollector builds the S6 collector on top of an opener and a
// config, with every unset field resolved by SQLConfig.withDefaults — the same
// constructor shape activity.go:266 uses, including the missing error return: an
// SQL collector with no opener gets a zero-value pool to fall back on, which is why
// the SQL constructors return no error and pooler.go's (which has no such fallback)
// does.
func NewPoolerClientsCollector(opener SQLOpener, cfg SQLConfig) *PoolerClientsCollector {
	c := &PoolerClientsCollector{opener: opener, cfg: cfg.withDefaults()}
	c.desc = Descriptor{
		Name:     CollectorPoolerClients,
		Interval: PoolerInterval,
		Source:   SourcePoolerRuntime,
		// CostStandard, from the class definitions' own words. CostCheap "costs
		// no SQL round-trip at all" (collector.go:130) and this costs one;
		// CostStandard "costs one read-only transaction per view group (R2.7)
		// and single-digit queries" (:134), which is exactly this shape. A3's
		// ruling used those two definitions when it split the row on "two cost
		// classes", so the class here is the one that ruling assigned to the
		// psql half — and it is the reason the split was made rather than an
		// exception to the one-source rule.
		Cost: CostStandard,
		// AppliesTo nil = AllMajors. pgbouncer's output varies by pgbouncer
		// version, not by PostgreSQL major, and this collector reads no
		// pg_catalog view, so the pgversion matrix has no answer to give about
		// it. That is pooler.go:146's reason for its own nil, restated because
		// the two files answer the field separately and a reader of one should
		// not have to open the other to learn the answer was given.
		AppliesTo: nil,
		// Timeout unset: R2.1's 10 s default, per Descriptor's rule that a
		// collector whose work is known to be cheap does not set a shorter one.
		Timeout: 0,
		// One group, one query, one transaction — R2.7's rule in its smallest
		// legal form.
		ViewGroups: []string{poolerClientsViewGroup},
	}
	return c
}

// Descriptor returns the R2.1 declaration built at construction.
func (c *PoolerClientsCollector) Descriptor() Descriptor { return c.desc }

// Collect is the Collector interface method, and it returns no samples.
//
// That is not a gap in this file; it is the absence of a family. R9.5's metric list
// has no entry for client attribution: requirements.md:548's cardinality row is
// scoped to "Pooler (R6.1)" — the S5 series — and naming.go:262 marks
// pg_stat_activity.client_addr as TypeNotExported ("R9.1 text half (inet)"), the
// framework's standing refusal to export an address as a number. R14.7 forbids a
// metric that exists in code and not in the docs, so the honest Result has an empty
// Samples slice rather than an invented pgsi_pooler_* family that a dashboard would
// have to be told to ignore.
//
// collector.go's Result doc states the empty case outright — "Empty with a nil error
// is a legitimate answer ... and the sink records the empty set as the new last-good
// rather than treating it as a failure" — and this is the collector it was written
// for: a round that succeeds, publishes nothing, and produced the data anyway.
func (c *PoolerClientsCollector) Collect(ctx context.Context, tgt Target) (Result, error) {
	// The rows are dropped here on purpose. A scheduler publishing samples to a
	// registry has nowhere to put a client address and R9.1 says it should not;
	// CollectSessions is activity.go's API-plane entry point and CollectClients
	// is this collector's, and the interface method exists so the scheduler can
	// run this collector at all.
	_, res, err := c.CollectClients(ctx, tgt)
	return res, err
}

// CollectClients runs one round and returns pgbouncer's client rows plus the
// round's accounting.
//
// This is the entry point both R6.4 consumers need: the R4.1 active-session list
// joins a Session to a Client through Client.Link to replace the pooler address with
// the real one, and the R3.7 by-client-address rollup groups by Client.Addr. Both
// need rows, not samples, which is why the method exists separately from Collect in
// the same pair activity.go:303 documents.
//
// The error paths return the round-trips issued so far, for R13.1's reason: a
// collector that returns a zero alongside a partial failure makes a cluster that
// issued a query look like one that issued none.
func (c *PoolerClientsCollector) CollectClients(ctx context.Context, tgt Target) ([]Client, Result, error) {
	tx, err := c.opener.Begin(ctx, tgt, c.cfg)
	if err != nil {
		return nil, Result{}, fmt.Errorf("collector %s: begin read-only transaction: %w",
			CollectorPoolerClients, err)
	}

	rows, qerr := tx.Query(ctx, showClientsQuery)
	if qerr != nil {
		// The query counts as a round-trip even though it failed, which is
		// activity.go:334's rule (trips++ runs after the call, on both paths) and
		// the correct answer to "how many queries did this cluster see": one.
		_ = tx.Rollback()
		return nil, Result{RoundTrips: 1},
			fmt.Errorf("collector %s: query %s: %w", CollectorPoolerClients, showClientsQuery, qerr)
	}

	clients := clientsFromRows(rows)

	if cerr := tx.Commit(); cerr != nil {
		// Same rule as activity.go:348: the data was read and the transaction
		// failed to close, the scheduler publishes nothing on a failed round, so
		// returning nil costs nothing and gives a caller that logs the result
		// something to log.
		_ = tx.Rollback()
		return nil, Result{RoundTrips: 1},
			fmt.Errorf("collector %s: commit: %w", CollectorPoolerClients, cerr)
	}

	return clients, Result{RoundTrips: 1}, nil
}

// clientsFromRows parses every row, dropping the ones that identify nothing.
func clientsFromRows(rows []Row) []Client {
	out := make([]Client, 0, len(rows))
	for _, row := range rows {
		cl, ok := clientFromRow(row)
		if !ok {
			continue
		}
		out = append(out, cl)
	}
	return out
}

// clientFromRow parses one SHOW CLIENTS row into a Client.
//
// The false case is the row that identifies nothing: Addr and Ptr both empty. Ptr is
// pgbouncer's own handle for the client and Addr is the value R6.4 exists to
// recover, so a row with neither has no identity to key a list on and no address to
// attribute — the analogue of activity.go dropping a pg_stat_activity row with no
// pid, and the only row this collector drops. Every other row is returned with
// whatever subset of the columns it carries, because a missing column is a value the
// source did not send and not a reason to lose the row's two key fields.
func clientFromRow(row Row) (Client, bool) {
	get := func(col string) string {
		v, _ := row.Get(col)
		return v
	}

	c := Client{
		Type:               get("type"),
		User:               get("user"),
		Database:           get("database"),
		Replication:        get("replication"),
		State:              get("state"),
		Addr:               get("addr"),
		Port:               get("port"),
		LocalAddr:          get("local_addr"),
		LocalPort:          get("local_port"),
		ConnectTime:        get("connect_time"),
		RequestTime:        get("request_time"),
		Wait:               get("wait"),
		WaitUs:             get("wait_us"),
		CloseNeeded:        get("close_needed"),
		Ptr:                get("ptr"),
		Link:               get("link"),
		RemotePid:          get("remote_pid"),
		TLS:                get("tls"),
		ApplicationName:    get("application_name"),
		PreparedStatements: get("prepared_statements"),
		ID:                 get("id"),
	}

	if c.Addr == "" && c.Ptr == "" {
		return Client{}, false
	}
	return c, true
}
