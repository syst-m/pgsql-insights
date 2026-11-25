package pgxopener

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
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

// ReadCA returns the same map value Read uses for the password.
//
// One map answering two readers is not the fake confusing the two keys — it is the
// minimum that satisfies the interface while keeping a test's fixture a literal.
// The map's value is "the secret's content", which is a password for a credential
// secret and a PEM for a -ca secret; a test that needs a real certificate puts one
// under the -ca name (TestBeginThreadsResolvedCA does), and a test that needs
// neither gets a password back from a ReadCA nobody asked it to distinguish.
func (f fakeSecrets) ReadCA(_ context.Context, namespace, name string) (string, error) {
	v, ok := f[namespace+"/"+name]
	if !ok {
		return "", errors.New("pgxopener_test: no such secret " + namespace + "/" + name)
	}
	return v, nil
}

// keyedSecrets carries both halves, for the tests that assert on the username.
type keyedSecrets struct {
	user, pass string
	// ca is the PEM ReadCA hands back, for a test that needs a specific certificate
	// rather than the map value fakeSecrets returns.
	ca string
}

func (k keyedSecrets) Exists(string, string) bool { return true }
func (k keyedSecrets) Read(context.Context, string, string) (string, string, error) {
	return k.user, k.pass, nil
}
func (k keyedSecrets) ReadCA(context.Context, string, string) (string, error) { return k.ca, nil }

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
	// The second argument is discarded here and asserted by
	// TestBeginThreadsResolvedCA: this test's subject is the pool cache, which is
	// keyed by the DSN alone, so the certificate is not what it is measuring.
	open := func(dsn string, _ []byte) (*sql.DB, error) {
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

// testCAPEM mints a self-signed ECDSA certificate — the content shape of CNPG's
// <cluster>-ca secret.
//
// Generated rather than embedded as a PEM literal because an embedded certificate
// expires, and the day it does, every test that uses it fails for a reason that has
// nothing to do with the code under test. ECDSA P-256 keeps the cost at
// milliseconds; the validity window is a day back and ten years forward, which is
// the shortest window that cannot rot inside the test's own lifetime.
func testCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("testCAPEM: generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "pg-ca", Organization: []string{"pgsi-test"}},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("testCAPEM: create certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// TestInstallRootCAs is the test for the fix itself.
//
// The defect this closes is not in DSN — DSN was always correct that the mode is
// verify-ca — it is that nothing handed pgx the certificate that mode verifies
// against, so every SQL collector's connection was rejected as an unknown
// authority. The assertion here is the one that distinguishes the two: not that
// openDefault returned no error, but that the parsed config's TLSConfig.RootCAs
// holds the PEM the caller supplied. Deleting the assignment makes it fail.
func TestInstallRootCAs(t *testing.T) {
	pemStr := testCAPEM(t)

	cases := []struct {
		name     string
		dsn      string
		rootCert string
		wantErr  string
		wantSet  bool
	}{
		{
			name:     "verify-ca with the CA installed",
			dsn:      "postgres://app:pw@127.0.0.1:1/postgres?sslmode=verify-ca",
			rootCert: pemStr,
			wantSet:  true,
		},
		{
			name: "no certificate, nothing to install",
			// The prefer case: conn.go's CASecret is empty for a mode that verifies
			// nothing, so rootCert is empty and the config must be left alone.
			dsn:     "postgres://app:pw@127.0.0.1:1/postgres?sslmode=prefer",
			wantSet: false,
		},
		{
			name:     "certificate for a connection that will not make TLS",
			dsn:      "postgres://app:pw@127.0.0.1:1/postgres?sslmode=disable",
			rootCert: pemStr,
			wantErr:  "uses no TLS",
		},
		{
			name:     "certificate that is not PEM",
			dsn:      "postgres://app:pw@127.0.0.1:1/postgres?sslmode=verify-ca",
			rootCert: "not a certificate",
			wantErr:  "not a PEM certificate",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc, err := pgx.ParseConfig(tc.dsn)
			if err != nil {
				t.Fatalf("ParseConfig(%q): %v", tc.dsn, err)
			}
			err = installRootCAs(cc, []byte(tc.rootCert))
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("installRootCAs err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			case err != nil:
				t.Fatalf("installRootCAs: %v", err)
			}
			got := cc.TLSConfig != nil && cc.TLSConfig.RootCAs != nil
			if got != tc.wantSet {
				t.Errorf("TLSConfig.RootCAs installed = %v, want %v (dsn %q, cert %d bytes)",
					got, tc.wantSet, tc.dsn, len(tc.rootCert))
			}
		})
	}
}

// TestBeginThreadsResolvedCA closes the gap between the two halves of R12.5.
//
// CredentialsFor names the CA in creds.CASecret and DSN writes sslmode=verify-ca;
// before rootCertFor, nothing read the secret, so the mode named a certificate the
// connection never had. This asserts the bytes that reach the pool are the bytes in
// the secret — for the cluster that has a -ca secret — and that no certificate is
// handed over for one that does not.
func TestBeginThreadsResolvedCA(t *testing.T) {
	pemStr := testCAPEM(t)

	t.Run("cluster with a -ca secret", func(t *testing.T) {
		// db/pg-app is the credential (R1.8 level 2) and db/pg-ca the anchor
		// (R12.5's default), so this target resolves to verify-ca and must present
		// the certificate.
		secrets := fakeSecrets{"db/pg-app": "pw", "db/pg-ca": pemStr}
		var gotCert []byte
		op, err := NewWithOpen(collector.ConnConfig{}, secrets, func(dsn string, rootCert []byte) (*sql.DB, error) {
			gotCert = rootCert
			return sql.Open("pgx", dsn)
		})
		if err != nil {
			t.Fatalf("NewWithOpen: %v", err)
		}
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Begin fails at BeginTx (nothing listens on 127.0.0.1:1); the assertion is
		// on what the pool was handed, which happens before that failure.
		_, _ = op.Begin(cctx, baseTarget(), collector.SQLConfig{})

		if string(gotCert) != pemStr {
			t.Errorf("rootCert passed to open = %d bytes, want the %d-byte CA from db/pg-ca", len(gotCert), len(pemStr))
		}
	})

	t.Run("cluster with no -ca secret", func(t *testing.T) {
		// conn.go:287's prefer fallback: no CA exists, so the mode needs none and the
		// opener must hand the pool nothing rather than an empty byte slice it has to
		// interpret.
		secrets := fakeSecrets{"db/pg-app": "pw"}
		var called bool
		var gotCert []byte
		op, err := NewWithOpen(collector.ConnConfig{}, secrets, func(dsn string, rootCert []byte) (*sql.DB, error) {
			called, gotCert = true, rootCert
			return sql.Open("pgx", dsn)
		})
		if err != nil {
			t.Fatalf("NewWithOpen: %v", err)
		}
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = op.Begin(cctx, baseTarget(), collector.SQLConfig{})

		if !called {
			t.Fatal("open was not called; Begin returned before the pool")
		}
		if len(gotCert) != 0 {
			t.Errorf("rootCert = %d bytes, want none for an sslmode that verifies nothing", len(gotCert))
		}
	})

	t.Run("CA secret exists but holds no certificate", func(t *testing.T) {
		// The error path R12.5 needs: a -ca secret that exists and is empty must not
		// become a verify-ca connection that verifies nothing.
		secrets := fakeSecrets{"db/pg-app": "pw", "db/pg-ca": ""}
		var called bool
		op, err := NewWithOpen(collector.ConnConfig{}, secrets, func(dsn string, _ []byte) (*sql.DB, error) {
			called = true
			return sql.Open("pgx", dsn)
		})
		if err != nil {
			t.Fatalf("NewWithOpen: %v", err)
		}
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = op.Begin(cctx, baseTarget(), collector.SQLConfig{})
		if called {
			t.Error("open was called despite an empty CA secret; Begin must fail before the pool")
		}
		if err == nil || !strings.Contains(err.Error(), "has no certificate") {
			t.Errorf("Begin err = %v, want one containing \"has no certificate\"", err)
		}
	})
}
