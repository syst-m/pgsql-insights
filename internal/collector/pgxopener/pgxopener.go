// Package pgxopener is the production collector.SQLOpener.
//
// sql.go declared the interface for two years of commits without an
// implementation outside tests. The paragraph that documents the SQL seam
// (internal/collector/sql.go:34-48, inside DefaultStatementTimeout's docblock)
// records why, and now records the answer in the same breath: the seam was an
// interface because go.mod "had no Postgres driver", the choice between lib/pq,
// pgx and the kubelet pod-proxy was deferred as a separate decision from writing
// the collectors, and that decision is made — this package, pgx/v5 through its
// database/sql-compatible pgx/stdlib subpackage, which is the driver R13.10
// names. conn.go:22-23 carried the same deferral ("consumed by whichever
// SQLOpener implementation the driver decision produces"); that sentence is now
// a statement about this file.
//
// Why the driver lives in a child package and not in internal/collector
// alongside the interface it implements. The interface's stated purpose was to
// keep a driver out of the collector package — sql.go:34 says the seam is an
// interface "and not a *sql.DB", and pooler.go:56-61 makes the same argument for
// the HTTP half: "A collector taking a func keeps the dependency a comment, which
// is what lets every rule in this file be tested without a cluster." Putting the
// pgx implementation in internal/collector would undo that: every collector test
// would start compiling pgx, and the interface would stop being a seam and become
// a formality. As a child package the dependency runs one way — pgxopener imports
// collector for Target, SQLConfig, Tx and Row; collector imports nothing back —
// which is the same shape internal/collector/metrictype and
// internal/collector/cnpgmetrics already have.
//
// Why pgx rather than the other two options the quote names. lib/pq is in
// maintenance mode upstream, so its maintenance cost lands here; pgx is the
// actively maintained default. The kubelet pod-proxy option produces a net.Conn
// rather than a *sql.DB and so still needs a driver on the far end — a transport
// question, not a driver question, and this package does not foreclose it.
package pgxopener

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/frankellis/pgsql-insights/internal/collector"
)

// Pool bounds for one DSN.
//
// These exist because stdlib.OpenDB does not set them. It calls sql.OpenDB with a
// pgx connector and returns the *sql.DB, which carries database/sql's own
// defaults: unlimited open connections and two idle ones. An unbounded pool is
// the cost rule R13.2 exists to stop, restated at the connection layer — a
// collector that issues five queries in a transaction needs one connection, and
// a pool with no ceiling lets a slow target accumulate connections across rounds
// until the target itself is the bottleneck for the wrong reason.
//
// Two open, one idle. One would be enough for R2.7's single-transaction-per-group
// rule, and it is the honest answer for a single collector; two is the number that
// lets the eight collectors sharing one target's DSN overlap a transaction without
// queueing behind a slow one, which is the only reason the pool is shared at all.
// Idle is below open on purpose: an idle connection to a Postgres holds a
// postmaster child process, and R13.2's cost argument counts those.
const (
	maxConnsPerTarget = 2
	maxIdlePerTarget  = 1
)

// DefaultDatabase is the database a connection string names when the target's
// address does not carry one.
//
// R2.7's transaction is per view group and R3.x's statements live per database,
// so "which database" is a real question the opener has to answer. SQLConfig has
// no Database field yet — statements.go's own head docblock anticipates one ("When
// SQLConfig gains A1's `Database` field and the round loop fans out to N databases
// per round, this file does not change") — so this constant is the standing answer
// until that field exists: CNPG's clusters always have a `postgres` database, and
// connecting to it is what every SQL collector's queries need, because the views
// they read (pg_stat_database, pg_database, pg_locks) are cluster-wide catalog
// views, not per-database data.
//
// It is a constant rather than a literal in the DSN builder so the day the field
// lands, the fallback has one place to change.
const DefaultDatabase = "postgres"

// Secrets reads Kubernetes Secrets, the two halves conn.go's credential rules need.
//
// conn.go declares SecretExists (conn.go:85) as a bare func type — "a function
// type rather than a clientset so the rules in this file are testable with a set
// literal" — and pooler.go:62 does the same for MetricsFetch. This is the same
// pattern with two methods instead of one, because the opener needs both halves
// and CredentialsFor needs only the first:
//
//   - Exists is what CredentialsFor probes with. It cannot be derived from Read
//     without turning every precedence probe into a network round-trip: the probe
//     runs up to twice per target (the named secret, then <cluster>-app), and
//     deriving existence from a read means the probe pays for a fetch it only
//     wanted a yes/no from.
//   - Read is what fills Credentials.Password. CredentialsFor deliberately leaves
//     Password empty for a secret-sourced credential — "a secret-sourced one
//     leaves it empty for the opener to read", conn.go:124 — because its own probe
//     is an existence check and not a reader. The opener is the thing that reads.
//
// The production implementation is a two-method type in cmd/pgsi over
// kube.Client's exported Clientset; a test supplies a struct of values. The
// alternative — importing internal/kube here — is what pooler.go rejected for the
// same reason: it would make a collector package depend on a client.
type Secrets interface {
	// Exists reports whether the named Secret exists in the namespace.
	Exists(namespace, name string) bool
	// Read returns the Secret's username and password keys.
	Read(ctx context.Context, namespace, name string) (username, password string, err error)
}

// OpenFunc opens a connection pool for a DSN without running a query.
//
// It is a field rather than a call because the pool is the one half of Begin that
// cannot be exercised without something on the other end of the TCP connection.
// The default (openDefault) is lazy in the way database/sql is lazy — it parses
// and returns without dialling — so a test can use the real one and still get a
// real error for a malformed DSN with no Postgres anywhere; a test that wants a
// working transaction supplies a func returning a fake collector.Tx and asserts
// the DSN it was handed.
type OpenFunc func(dsn string) (*sql.DB, error)

// Opener is the pgx-backed collector.SQLOpener.
//
// It caches one *sql.DB per DSN. That is R13.2's cost rule applied at the layer
// R2.7's own argument reaches: R2.7's "single read-only transaction per view
// group" exists to stop the number of transactions being a function of the number
// of queries, and a per-Begin sql.Open would reintroduce the same mistake one
// layer down — the number of TCP handshakes and TLS negotiations becoming a
// function of the number of rounds. One pool per DSN means a cluster with four
// SQL collectors and a 60 s interval opens one pool once and reuses it, and the
// pool's own limits are the driver's, not a per-collector setting.
type Opener struct {
	connCfg collector.ConnConfig
	secrets Secrets
	open    OpenFunc

	mu    sync.Mutex
	pools map[string]*sql.DB
}

// New builds the opener on the real driver.
//
// A nil Secrets is an error rather than a default for the same reason
// NewPoolerCollector refuses a nil fetch: the credential rules have no zero value
// that works, and an opener that cannot resolve a credential produces a collector
// whose every round fails at run time.
func New(connCfg collector.ConnConfig, secrets Secrets) (*Opener, error) {
	if secrets == nil {
		return nil, errors.New("pgxopener: Secrets is required (an opener that cannot read a credential cannot connect)")
	}
	return NewWithOpen(connCfg, secrets, nil)
}

// NewWithOpen builds the opener over a supplied OpenFunc, the seam for a test
// that wants a pool without a server. Nil open selects openDefault.
func NewWithOpen(connCfg collector.ConnConfig, secrets Secrets, open OpenFunc) (*Opener, error) {
	if secrets == nil {
		return nil, errors.New("pgxopener: Secrets is required (an opener that cannot read a credential cannot connect)")
	}
	if open == nil {
		open = openDefault
	}
	return &Opener{
		connCfg: connCfg,
		secrets: secrets,
		open:    open,
		pools:   make(map[string]*sql.DB),
	}, nil
}

// openDefault is the production OpenFunc: pgx's own connection-string parser, then
// stdlib.OpenDB.
//
// It calls pgx.ParseConfig directly rather than database/sql's
// sql.Open("pgx", dsn) for two reasons that are both about what gets checked.
// sql.Open takes a driver *name* and looks the driver up in a global registry
// populated by the stdlib package's init — so the string is parsed by whichever
// version registered itself, and the answer depends on import side effects.
// ParseConfig is the parser itself: it returns a typed *pgx.ConnConfig or an
// error, so a malformed DSN fails at the call that built it, and pgxopener_test.go
// can assert the parse for every DSN the builder produces without a Postgres
// listening. stdlib.OpenDB then wraps the parsed config in a connector, and the
// pool bounds above are applied to the *sql.DB it returns.
func openDefault(dsn string) (*sql.DB, error) {
	cc, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxopener: pgx rejected the connection string: %w", err)
	}
	db := stdlib.OpenDB(*cc)
	db.SetMaxOpenConns(maxConnsPerTarget)
	db.SetMaxIdleConns(maxIdlePerTarget)
	return db, nil
}

// DSN renders the connection string for one target.
//
// It is exported and pure — no I/O, no clock, no receiver state — because this is
// where the failure modes are. A wrong host is an unreachable target, a wrong
// sslmode is a connection that silently downgrades, and a password containing
// `@` or `:` is a connection string that parses as the wrong thing entirely.
// pgx's own parser is the arbiter and it is reachable without a server, so
// pgxopener_test.go feeds every DSN this function can produce through
// sql.Open("pgx", …) and asserts the driver accepts it.
//
// The URL form is used rather than the keyword/value form ("host=… port=…")
// because the keyword form requires reimplementing Postgres' quoting rules —
// single quotes with embedded quotes doubled — while url.UserPassword does the
// percent-encoding the URL form needs, in stdlib, correctly, for the one field
// that is arbitrary bytes: R1.8's password.
func DSN(tgt collector.Target, creds collector.Credentials, cfg collector.SQLConfig) (string, error) {
	// Address is the field kube builds: "host:port for an S2 target"
	// (collector.go:306-308). An empty one is not a connectable target, and
	// Target.Valid does not check it, so this is the only guard.
	if tgt.Address == "" {
		return "", fmt.Errorf("pgxopener: target %s has an empty Address", tgt.Key())
	}
	host, port, err := splitHostPort(tgt.Address)
	if err != nil {
		return "", err
	}

	// The role half. Credentials.Role is empty for a secret-sourced credential
	// ("the username lives in the secret's own `username` key and this resolution
	// has an existence probe, not a reader: the opener reads the key",
	// conn.go:117-119), so an empty Role here means the caller did not fill it from
	// the secret — which is a caller error, not a default to paper over.
	if creds.Role == "" {
		return "", fmt.Errorf("pgxopener: credentials for %s have an empty Role (a secret-sourced credential must be read before it is used)", tgt.Key())
	}

	// The TLS half. conn.go's six SSLModes are exactly the six pgx accepts
	// (disable, allow, prefer, require, verify-ca, verify-full — conn.go:294-301
	// versus pgx's own list), so there is no mode to translate. An empty mode is
	// left out of the string rather than written as empty: pgx defaults an absent
	// sslmode to `prefer`, and writing sslmode= would be a different answer
	// (invalid) for the question the empty value asks (unspecified).
	if creds.SSLMode != "" && !collector.ValidSSLMode(creds.SSLMode) {
		return "", fmt.Errorf("pgxopener: sslmode %q is not one of %s", creds.SSLMode, strings.Join(collector.SSLModes, ", "))
	}

	// R2.8's session name. SQLConfig.withDefaults fills the package constant, so
	// a zero SQLConfig still produces the name R2.8 requires; the reason the DSN
	// carries it rather than issuing a SET is that application_name cannot be
	// changed inside a transaction — it is a connection property, so the only place
	// a per-target value can be set is the string that opens the connection.
	appName := cfg.ApplicationName
	if appName == "" {
		appName = collector.ApplicationName
	}

	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(creds.Role, creds.Password),
		Host:     host + ":" + port,
		Path:     DefaultDatabase,
		RawQuery: "application_name=" + url.QueryEscape(appName),
	}
	if creds.SSLMode != "" {
		u.RawQuery += "&sslmode=" + url.QueryEscape(creds.SSLMode)
	}
	return u.String(), nil
}

// splitHostPort splits a Target.Address into host and port.
//
// Target.Address is documented as "host:port for an S2 target and a full URL for
// an HTTP source" (collector.go:306-308), and this opener only ever receives the
// first kind — the scheduler hands a SQL collector only S2 targets, because that
// is the Source its Descriptor declares (R2.1). A URL is rejected rather than
// stripped, because a `http://` address reaching a Postgres driver means the
// target's source and the collector's source disagree, which is a wiring bug worth
// an error rather than a silent substring.
func splitHostPort(addr string) (host, port string, err error) {
	if strings.Contains(addr, "://") {
		return "", "", fmt.Errorf("pgxopener: address %q is a URL, not a host:port (a SQL target's Address has no scheme)", addr)
	}
	host, port, err = splitOnce(addr)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", fmt.Errorf("pgxopener: address %q has an empty host", addr)
	}
	if port == "" {
		return "", "", fmt.Errorf("pgxopener: address %q has an empty port", addr)
	}
	return host, port, nil
}

// splitOnce splits on the last colon, so an address with no port at all yields an
// empty port rather than a host of "5432".
func splitOnce(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("pgxopener: address %q is not a host:port pair (no colon)", addr)
	}
	return addr[:i], addr[i+1:], nil
}

// Begin opens one read-only transaction against tgt with cfg applied — R2.7's
// whole requirement, expressed as the one method the interface has.
//
// The order is the requirement's: resolve who we are (R12.4's role, R12.5's
// sslmode, both from conn.go), render the connection string, get the pool for it,
// open the transaction with ReadOnly set, then apply the per-query bound. Each
// step's failure is returned before the next, so a target with no credential never
// opens a connection.
func (o *Opener) Begin(ctx context.Context, tgt collector.Target, cfg collector.SQLConfig) (collector.Tx, error) {
	creds, err := o.credentials(ctx, tgt)
	if err != nil {
		return nil, err
	}
	dsn, err := DSN(tgt, creds, cfg)
	if err != nil {
		return nil, err
	}
	db, err := o.pool(dsn)
	if err != nil {
		return nil, err
	}

	// R2.7's read-only-ness. conn.go's row names this as the thing the Tx
	// interface cannot assert and the opener must supply: "Read-only-ness is an
	// opener property the `Tx` interface cannot assert: the signature has no
	// `SET TRANSACTION READ ONLY`, so it is proven by whichever `SQLOpener` the
	// driver decision produces, not by a fake that cannot reject a write"
	// (IMPLEMENTATION.md:762). database/sql has the field the assertion wants —
	// sql.TxOptions.ReadOnly — and pgx implements it as the transaction the
	// server sees, which is the proof R2.7 asks for.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("pgxopener: begin %s: %w", tgt.Key(), err)
	}

	// R2.7's statement_timeout, applied inside the transaction so it is scoped to
	// it and not to the pooled session. SET LOCAL is the only form with that scope:
	// a session-level SET would let one collector's 5 s bound persist into the next
	// collector's round on a shared pool, and R2.7's rule is per-query, not
	// per-session.
	if _, err := tx.ExecContext(ctx, statementTimeoutSetting(cfg)); err != nil {
		// Every error path rolls back. conn.go's rule for a failed round is that it
		// "cannot leak a transaction open on the instance", and a SET that fails
		// inside a transaction we then abandon is exactly the leak.
		_ = tx.Rollback()
		return nil, fmt.Errorf("pgxopener: apply statement_timeout on %s: %w", tgt.Key(), err)
	}

	return &txWrapper{tx: tx}, nil
}

// credentials resolves the credential half for one target, filling the password
// the resolver deliberately leaves empty.
func (o *Opener) credentials(ctx context.Context, tgt collector.Target) (collector.Credentials, error) {
	creds, err := collector.CredentialsFor(tgt, o.connCfg, o.secrets.Exists)
	if err != nil {
		return collector.Credentials{}, err
	}
	// CredentialsFor sets Password only for a configured credential (R1.8's third
	// level) and leaves it empty for a secret-sourced one, "for the opener to
	// read" (conn.go:124). This is the read.
	if creds.Password == "" && creds.Secret != "" {
		ns, name := splitSecretRef(tgt.Namespace, creds.Secret)
		user, pass, err := o.secrets.Read(ctx, ns, name)
		if err != nil {
			return collector.Credentials{}, fmt.Errorf("pgxopener: read secret %s/%s for %s: %w", ns, name, tgt.Key(), err)
		}
		// The secret's username wins over the resolved Role. CredentialsFor can
		// name a role it derived from a secret reference (R12.4's
		// cnpg_metrics_exporter) while the secret itself carries a different
		// `username` key, and the key is what the server authenticates.
		if user != "" {
			creds.Role = user
		}
		creds.Password = pass
	}
	return creds, nil
}

// splitSecretRef splits a "namespace/name" reference, applying the same bare-name
// rule conn.go:355's splitSecret applies: a reference with no slash is a name in
// the target's own namespace.
//
// It is duplicated rather than called because conn.go's is unexported and this
// package cannot reach it; the duplication is the same one conn.go itself accepts
// for R1.8's precedence ("implemented twice, here and in discovery.ResolveCredential,
// and that duplication is deliberate rather than an oversight").
func splitSecretRef(targetNS, ref string) (ns, name string) {
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return targetNS, ref
}

// pool returns the cached pool for a DSN, opening it on first use.
func (o *Opener) pool(dsn string) (*sql.DB, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if db, ok := o.pools[dsn]; ok {
		return db, nil
	}
	db, err := o.open(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxopener: open pool for %s: %w", dsn, err)
	}
	o.pools[dsn] = db
	return db, nil
}

// statementTimeoutSetting renders the SET LOCAL statement.
//
// It is a separate pure function because the string is the thing worth pinning:
// Postgres accepts `5s` and `5000ms` and `'5 s'` and rejects `5` unquoted in a
// SET LOCAL context, and the SQLConfig that reaches here has already been through
// withDefaults, so a zero SQLConfig must still produce R2.7's 5 s and not `0s`.
func statementTimeoutSetting(cfg collector.SQLConfig) string {
	d := cfg.StatementTimeout
	if d <= 0 {
		d = collector.DefaultStatementTimeout
	}
	return fmt.Sprintf("SET LOCAL statement_timeout TO %d", int64(d/time.Millisecond))
}

// txWrapper adapts *sql.Tx to collector.Tx.
//
// collector.Tx.Query returns []Row and Row is map[string]string (sql.go:80), so
// the adapter's job is the stringification every collector already assumes: the
// SQL collectors read row["dbname"], row["max_size"] and parse them as strings,
// and a *sql.Rows scan into []byte then string is the whole conversion.
type txWrapper struct {
	tx *sql.Tx
}

// Query runs one query inside the transaction and returns its rows as strings.
func (t *txWrapper) Query(ctx context.Context, query string) ([]collector.Row, error) {
	rows, err := t.tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("pgxopener: query: %w", err)
	}
	defer rows.Close()
	return rowsToRows(rows)
}

// Commit ends the transaction, keeping what it did.
func (t *txWrapper) Commit() error { return t.tx.Commit() }

// Rollback ends the transaction, discarding what it did.
func (t *txWrapper) Rollback() error { return t.tx.Rollback() }

// rowsToRows converts a driver result set into the collector's row type.
//
// Every column becomes a string, which is what collector.Row is and what the
// collectors expect: they parse every value themselves (sql.go:80's Row is
// map[string]string precisely so a collector never has to know whether a driver
// returns int64 or float64 for a COUNT). NULL becomes the empty string rather
// than "NULL", because a collector that parses an empty numeric as 0 is right
// about a NULL size and wrong about the literal word.
func rowsToRows(rows *sql.Rows) ([]collector.Row, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("pgxopener: columns: %w", err)
	}
	var out []collector.Row
	for rows.Next() {
		// One *any per column, then deref: scanning into *any gives the driver's
		// native type, and the deref is what turns a NULL (nil) into "" instead of
		// into "<nil>".
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("pgxopener: scan: %w", err)
		}
		row := make(collector.Row, len(cols))
		for i, v := range vals {
			row[cols[i]] = stringify(v)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgxopener: rows: %w", err)
	}
	return out, nil
}

// stringify renders one scanned value as the string collector.Row stores.
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
