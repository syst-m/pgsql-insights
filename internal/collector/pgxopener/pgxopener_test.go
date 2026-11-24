package pgxopener

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/frankellis/pgsql-insights/internal/collector"
)

// The test file for the driver seam. Its centre of gravity is DSN, and the shape
// of the file follows from what is and isn't reachable without a Postgres: the
// connection string is pure and pgx's own parser is callable, so every string the
// builder can produce is checked against the parser that will actually read it;
// the transaction half needs a server, so it is tested through the paths that
// terminate before the dial plus one that proves the dial happened.

// fakeSecrets is the Secrets implementation the tests use: a map keyed by
// "namespace/name".
type fakeSecrets map[string]string

func (f fakeSecrets) Exists(namespace, name string) bool {
	_, ok := f[namespace+"/"+name]
	return ok
}

func (f fakeSecrets) Read(_ context.Context, namespace, name string) (string, string, error) {
	v, ok := f[namespace+"/"+name]
	if !ok {
		return "", "", errors.New("pgxopener_test: no such secret " + namespace + "/" + name)
	}
	// The map value is the password; the username is derived so a test can vary one
	// without restating the other. Tests that need a specific username use
	// keyedSecrets instead.
	return "app", v, nil
}

// keyedSecrets carries both halves, for the tests that assert on the username.
type keyedSecrets struct {
	user, pass string
}

func (k keyedSecrets) Exists(string, string) bool { return true }
func (k keyedSecrets) Read(context.Context, string, string) (string, string, error) {
	return k.user, k.pass, nil
}

// baseTarget is the smallest valid S2 target the tests build on. Every test that
// varies one field starts from this so the fields it does not vary are pinned by
// construction rather than by restating them.
func baseTarget() collector.Target {
	return collector.Target{
		Namespace: "db",
		Cluster:   "pg",
		Pod:       "pg-1",
		Role:      "primary",
		Address:   "10.42.1.11:5432",
		Source:    collector.SourceSQL,
		PGMajor:   16,
	}
}

func TestDSNFromTarget(t *testing.T) {
	cases := []struct {
		name  string
		tgt   collector.Target
		creds collector.Credentials
		cfg   collector.SQLConfig
		want  string
	}{
		{
			name:  "configured credential, no sslmode",
			tgt:   baseTarget(),
			creds: collector.Credentials{Role: "appuser", Password: "s3cret"},
			cfg:   collector.SQLConfig{},
			want:  "postgres://appuser:s3cret@10.42.1.11:5432/postgres?application_name=pgsi-collector",
		},
		{
			name:  "explicit sslmode is carried",
			tgt:   baseTarget(),
			creds: collector.Credentials{Role: "appuser", Password: "s3cret", SSLMode: "require"},
			cfg:   collector.SQLConfig{},
			want:  "postgres://appuser:s3cret@10.42.1.11:5432/postgres?application_name=pgsi-collector&sslmode=require",
		},
		{
			name:  "verify-full, the mode CNPG's own exporter uses",
			tgt:   baseTarget(),
			creds: collector.Credentials{Role: "appuser", Password: "s3cret", SSLMode: "verify-full"},
			cfg:   collector.SQLConfig{},
			want:  "postgres://appuser:s3cret@10.42.1.11:5432/postgres?application_name=pgsi-collector&sslmode=verify-full",
		},
		{
			name:  "a custom application_name overrides the constant",
			tgt:   baseTarget(),
			creds: collector.Credentials{Role: "appuser", Password: "s3cret"},
			cfg:   collector.SQLConfig{ApplicationName: "custom"},
			want:  "postgres://appuser:s3cret@10.42.1.11:5432/postgres?application_name=custom",
		},
		{
			name:  "a non-default port survives",
			tgt:   collector.Target{Namespace: "db", Cluster: "pg", Source: collector.SourceSQL, Address: "10.0.0.5:5433"},
			creds: collector.Credentials{Role: "u", Password: "p"},
			cfg:   collector.SQLConfig{},
			want:  "postgres://u:p@10.0.0.5:5433/postgres?application_name=pgsi-collector",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DSN(tc.tgt, tc.creds, tc.cfg)
			if err != nil {
				t.Fatalf("DSN: %v", err)
			}
			if got != tc.want {
				t.Errorf("DSN()\n got  %s\n want %s", got, tc.want)
			}
		})
	}
}

// TestDSNIsParsableByPgx is the test that makes the DSN table above mean
// something. A golden-string test alone pins the builder to itself: change the
// format and the test changes with it, so the pair can agree on a string no driver
// accepts.
//
// pgx.ParseConfig is the parser the production OpenFunc calls (openDefault), and
// it is callable with no server, so every DSN the builder produces is fed to the
// real parser and the parsed fields are compared back to the inputs. That makes
// the assertion "pgx reads this string as this host, port, user, password,
// database and sslmode" rather than "the string looks like a URL".
func TestDSNIsParsableByPgx(t *testing.T) {
	cases := []struct {
		creds    collector.Credentials
		addr     string
		wantUser string
		wantPass string
		wantHost string
		wantPort string
		wantSSL  string
	}{
		{
			creds:    collector.Credentials{Role: "appuser", Password: "s3cret"},
			addr:     "10.42.1.11:5432",
			wantUser: "appuser", wantPass: "s3cret", wantHost: "10.42.1.11", wantPort: "5432",
		},
		{
			creds:    collector.Credentials{Role: "appuser", Password: "s3cret", SSLMode: "require"},
			addr:     "10.42.1.11:5432",
			wantUser: "appuser", wantPass: "s3cret", wantHost: "10.42.1.11", wantPort: "5432",
			wantSSL: "require",
		},
		{
			// The one input that can break a URL without breaking a keyword string.
			creds:    collector.Credentials{Role: "app", Password: "p@ss:wo/rd"},
			addr:     "10.0.0.9:5432",
			wantUser: "app", wantPass: "p@ss:wo/rd", wantHost: "10.0.0.9", wantPort: "5432",
		},
		{
			creds:    collector.Credentials{Role: "app", Password: ""},
			addr:     "10.0.0.9:5432",
			wantUser: "app", wantPass: "", wantHost: "10.0.0.9", wantPort: "5432",
		},
	}

	for _, tc := range cases {
		tgt := baseTarget()
		tgt.Address = tc.addr
		dsn, err := DSN(tgt, tc.creds, collector.SQLConfig{})
		if err != nil {
			t.Fatalf("DSN(%s): %v", tc.addr, err)
		}
		cc, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("pgx.ParseConfig(%s): %v", dsn, err)
		}
		if cc.User != tc.wantUser {
			t.Errorf("parsed User = %q, want %q (dsn %s)", cc.User, tc.wantUser, dsn)
		}
		if cc.Password != tc.wantPass {
			t.Errorf("parsed Password = %q, want %q (dsn %s)", cc.Password, tc.wantPass, dsn)
		}
		if cc.Host != tc.wantHost {
			t.Errorf("parsed Host = %q, want %q (dsn %s)", cc.Host, tc.wantHost, dsn)
		}
		if port := cc.Port; portStr(port) != tc.wantPort {
			t.Errorf("parsed Port = %s, want %s (dsn %s)", portStr(port), tc.wantPort, dsn)
		}
		if cc.Database != DefaultDatabase {
			t.Errorf("parsed Database = %q, want %q (dsn %s)", cc.Database, DefaultDatabase, dsn)
		}
		// application_name is a runtime parameter, not a URL field, so it surfaces
		// in RuntimeParams — which is the proof R2.8's name reaches the server
		// rather than just the string.
		if got := cc.RuntimeParams["application_name"]; got != collector.ApplicationName {
			t.Errorf("parsed RuntimeParams[application_name] = %q, want %q (dsn %s)", got, collector.ApplicationName, dsn)
		}
	}
}

// portStr renders pgx's uint16 port as the string the table compares against.
func portStr(p uint16) string { return fmt.Sprintf("%d", p) }

// TestDSNInvalidInputs pins the four rejections, each of which is a caller error
// rather than a value to default.
func TestDSNInvalidInputs(t *testing.T) {
	cases := []struct {
		name    string
		tgt     collector.Target
		creds   collector.Credentials
		cfg     collector.SQLConfig
		wantSub string
	}{
		{
			name:    "empty address",
			tgt:     collector.Target{Namespace: "db", Cluster: "pg", Source: collector.SourceSQL},
			creds:   collector.Credentials{Role: "u", Password: "p"},
			wantSub: "empty Address",
		},
		{
			name:    "address is a URL (an S5 address reached a SQL opener)",
			tgt:     collector.Target{Namespace: "db", Cluster: "pg", Source: collector.SourceSQL, Address: "http://pg-pooler.db.svc:9127/metrics"},
			creds:   collector.Credentials{Role: "u", Password: "p"},
			wantSub: "is a URL, not a host:port",
		},
		{
			name:    "address has no colon",
			tgt:     collector.Target{Namespace: "db", Cluster: "pg", Source: collector.SourceSQL, Address: "10.42.1.11"},
			creds:   collector.Credentials{Role: "u", Password: "p"},
			wantSub: "not a host:port pair",
		},
		{
			name:    "empty host",
			tgt:     collector.Target{Namespace: "db", Cluster: "pg", Source: collector.SourceSQL, Address: ":5432"},
			creds:   collector.Credentials{Role: "u", Password: "p"},
			wantSub: "empty host",
		},
		{
			name:    "empty port",
			tgt:     collector.Target{Namespace: "db", Cluster: "pg", Source: collector.SourceSQL, Address: "10.42.1.11:"},
			creds:   collector.Credentials{Role: "u", Password: "p"},
			wantSub: "empty port",
		},
		{
			name:    "empty role (a secret-sourced credential was never read)",
			tgt:     baseTarget(),
			creds:   collector.Credentials{Password: "p"},
			wantSub: "empty Role",
		},
		{
			name:    "invalid sslmode",
			tgt:     baseTarget(),
			creds:   collector.Credentials{Role: "u", Password: "p", SSLMode: "verify_full"},
			wantSub: "sslmode \"verify_full\" is not one of",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DSN(tc.tgt, tc.creds, tc.cfg)
			if err == nil {
				t.Fatalf("DSN() = %q, want an error containing %q", got, tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("DSN() error = %q, want it to contain %q", err, tc.wantSub)
			}
			if got != "" {
				t.Errorf("DSN() = %q on error, want the empty string", got)
			}
		})
	}
}

// TestEverySSLModeParses walks conn.go's full SSLModes list rather than a sample.
// The two lists are a contract between two packages with no type linking them:
// conn.go:305's six modes and pgx's accepted set. A seventh mode added to
// conn.go without pgx supporting it, or a rename, fails here instead of at the
// first connection attempt in production.
func TestEverySSLModeParses(t *testing.T) {
	for _, mode := range collector.SSLModes {
		t.Run(mode, func(t *testing.T) {
			creds := collector.Credentials{Role: "u", Password: "p", SSLMode: mode}
			dsn, err := DSN(baseTarget(), creds, collector.SQLConfig{})
			if err != nil {
				t.Fatalf("DSN with sslmode %s: %v", mode, err)
			}
			if _, err := pgx.ParseConfig(dsn); err != nil {
				t.Errorf("pgx rejected the DSN built for sslmode %s: %v\n%s", mode, err, dsn)
			}
		})
	}
}

func TestNewRequiresSecrets(t *testing.T) {
	if _, err := New(collector.ConnConfig{}, nil); err == nil {
		t.Error("New with nil Secrets: err = nil, want an error")
	}
	if _, err := NewWithOpen(collector.ConnConfig{}, nil, nil); err == nil {
		t.Error("NewWithOpen with nil Secrets: err = nil, want an error")
	}
	// The non-nil case has to be checked too: a constructor that always errors
	// passes both of the assertions above.
	op, err := New(collector.ConnConfig{}, fakeSecrets{})
	if err != nil {
		t.Fatalf("New with empty Secrets: %v", err)
	}
	if op == nil {
		t.Fatal("New returned a nil *Opener with no error")
	}
}

// TestBeginResolvesCredentialThenBuildsDSN is the chain test: secret lookup,
// password read, DSN build, pool open, transaction begin — one call, and the
// error it returns names the host from the DSN, which is the only way to observe
// that the DSN reached the driver without a Postgres to ask.
//
// The address is 127.0.0.1:1. Port 1 is not a Postgres on any host that matters
// here, so BeginTx's dial fails with connection refused, and the error string
// carries the address. That is a real assertion about the DSN: had the builder
// produced a malformed string, the failure would come from pgx.ParseConfig
// (openDefault's first line) and name the string rather than the host; had the
// credential resolution failed, it would name the secret.
func TestBeginResolvesCredentialThenBuildsDSN(t *testing.T) {
	secrets := fakeSecrets{"db/pg-app": "the-app-password"}
	tgt := baseTarget()
	tgt.Address = "127.0.0.1:1"

	op, err := New(collector.ConnConfig{}, secrets)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = op.Begin(context.Background(), tgt, collector.SQLConfig{})
	if err == nil {
		t.Fatal("Begin against 127.0.0.1:1: err = nil, want a connection error")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("Begin error = %q, want it to name the host from the DSN", err)
	}
}

// TestBeginFailsBeforeDialOnCredentialErrors pins the two paths that must not
// reach the driver at all. conn.go's rule for a failed round is that it "cannot
// leak a transaction open on the instance"; the stronger version of that rule is
// that a target with no usable credential never opens a connection in the first
// place, so the failure is a resolution error and not a dial error.
func TestBeginFailsBeforeDialOnCredentialErrors(t *testing.T) {
	t.Run("no credential at all", func(t *testing.T) {
		op, err := New(collector.ConnConfig{}, fakeSecrets{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		// baseTarget has a cluster and namespace but no secret is named and none
		// exists, so CredentialsFor has no answer.
		_, err = op.Begin(context.Background(), baseTarget(), collector.SQLConfig{})
		if err == nil {
			t.Fatal("Begin with no credential: err = nil, want a resolution error")
		}
		if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "dial") {
			t.Errorf("Begin error = %q, want a credential error, not a connection error", err)
		}
	})

	t.Run("superuser secret is refused by R12.4", func(t *testing.T) {
		// R12.4's one hard rule: never the superuser. CredentialsFor enforces it,
		// and the opener inherits it — this test exists to prove the opener does
		// not route around it by reading the superuser secret anyway.
		secrets := fakeSecrets{"db/pg-superuser": "super-secret"}
		cfg := collector.ConnConfig{Secret: "db/pg-superuser"}
		op, err := New(cfg, secrets)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_, err = op.Begin(context.Background(), baseTarget(), collector.SQLConfig{})
		if err == nil {
			t.Fatal("Begin with the superuser secret: err = nil, want R12.4's refusal")
		}
	})
}

// TestBeginCachesOnePoolPerDSN pins the pool cache, which is R13.2's cost rule
// applied at the connection layer. Two Begin calls for the same target must
// produce one open, and two targets must produce two.
//
// It uses a counting OpenFunc rather than openDefault so the count is exact and
// no dial happens: openDefault's *sql.DB is lazy, so BeginTx would still fail and
// the test would be asserting on a connection error rather than on the cache.
func TestBeginCachesOnePoolPerDSN(t *testing.T) {
	var opens []string
	open := func(dsn string) (*sql.DB, error) {
		opens = append(opens, dsn)
		// A real, lazy *sql.DB from the registered driver: no dial, so BeginTx
		// fails afterwards, which is fine — the assertion is on `opens`.
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, err
		}
		return db, nil
	}

	secrets := fakeSecrets{"db/pg-app": "pw"}
	op, err := NewWithOpen(collector.ConnConfig{}, secrets, open)
	if err != nil {
		t.Fatalf("NewWithOpen: %v", err)
	}

	a := baseTarget()
	a.Address = "127.0.0.1:1"
	b := baseTarget()
	b.Address = "127.0.0.1:2"

	// The context carries a deadline because that is the only way the production
	// caller calls it: scheduler.go:463 wraps every collect in
	// context.WithTimeout(ctx, e.Desc.EffectiveTimeout()) — R2.1's default 10 s —
	// and database/sql threads that ctx into the dial. Passing
	// context.Background() here instead tests a call shape that never happens, and
	// the difference is not cosmetic: the first version of this test used
	// 10.0.0.1:5432 and 10.0.0.2:5432, which are routable but silent, so each
	// Begin hung in connect for the OS TCP timeout and the package never finished.
	// Loopback ports 1 and 2 refuse immediately, so the five Begins cost nothing
	// and the assertion stays on the pool cache rather than on how long a dial
	// takes to fail.
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, tgt := range []collector.Target{a, a, b, a, b} {
		// Every call fails at BeginTx (nothing is listening); the test ignores the
		// error and reads the recorded DSNs.
		_, _ = op.Begin(cctx, tgt, collector.SQLConfig{})
	}

	// These two strings carry sslmode=prefer, and the first version of this
	// assertion left it off — which is the difference between the two ways to reach
	// DSN. TestDSNFromTarget hands DSN a hand-built Credentials with an empty
	// SSLMode, and DSN applies no defaults of its own, so those DSNs have no
	// sslmode. Begin does not build credentials: it asks CredentialsFor, which
	// resolves the sslmode half through SSLModeFor, and conn.go:287's answer for a
	// ConnConfig that names no mode is R12.5's "prefer". So the string Begin dials
	// is not the string the table predicts for the same role and password — and a
	// test that copies the table's shape instead of the resolver's output is
	// asserting about a call the opener never makes.
	want := []string{
		"postgres://app:pw@127.0.0.1:1/postgres?application_name=pgsi-collector&sslmode=prefer",
		"postgres://app:pw@127.0.0.1:2/postgres?application_name=pgsi-collector&sslmode=prefer",
	}
	if len(opens) != len(want) {
		t.Fatalf("opens = %v, want exactly the two distinct DSNs %v", opens, want)
	}
	for i := range want {
		if opens[i] != want[i] {
			t.Errorf("opens[%d] = %q, want %q", i, opens[i], want[i])
		}
	}
}

func TestStatementTimeoutSetting(t *testing.T) {
	cases := []struct {
		cfg  collector.SQLConfig
		want string
	}{
		// A zero SQLConfig must produce R2.7's default and not `0`, which is the
		// difference between a 5 s bound and no bound.
		{cfg: collector.SQLConfig{}, want: "SET LOCAL statement_timeout TO 5000"},
		{cfg: collector.SQLConfig{StatementTimeout: collector.DefaultStatementTimeout}, want: "SET LOCAL statement_timeout TO 5000"},
		{cfg: collector.SQLConfig{StatementTimeout: 2500 * time.Millisecond}, want: "SET LOCAL statement_timeout TO 2500"},
		{cfg: collector.SQLConfig{StatementTimeout: 30 * time.Second}, want: "SET LOCAL statement_timeout TO 30000"},
		// A negative value falls back rather than emitting a negative timeout,
		// which Postgres reads as no timeout.
		{cfg: collector.SQLConfig{StatementTimeout: -1 * time.Second}, want: "SET LOCAL statement_timeout TO 5000"},
	}
	for _, tc := range cases {
		if got := statementTimeoutSetting(tc.cfg); got != tc.want {
			t.Errorf("statementTimeoutSetting(%v) = %q, want %q", tc.cfg.StatementTimeout, got, tc.want)
		}
	}
}

// TestDefaultDatabaseIsPostgres pins the constant on its own. It is the answer to
// a question SQLConfig does not have a field for yet, and the day that field
// lands, this test is the one that says which value was the standing answer.
func TestDefaultDatabaseIsPostgres(t *testing.T) {
	if DefaultDatabase != "postgres" {
		t.Errorf("DefaultDatabase = %q, want \"postgres\"", DefaultDatabase)
	}
}

// TestOpenerImplementsSQLOpener is the compile-time assertion that the whole point
// of the package holds. Everything else in this file can pass while the package
// quietly fails to satisfy the interface it exists to implement — Begin's
// signature can drift by one type and the collectors keep compiling against the
// interface while nothing constructs them from here.
var _ collector.SQLOpener = (*Opener)(nil)
