package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// existsFrom turns a list of "namespace/name" references into the SecretExists
// probe CredentialsFor and SSLModeFor take, so R1.8's precedence and R12.5's
// derivation are tested with a set literal rather than a clientset — the same
// injection discovery.ResolveCredential uses for the same reason.
func existsFrom(refs ...string) SecretExists {
	set := make(map[string]bool, len(refs))
	for _, r := range refs {
		set[r] = true
	}
	return func(ns, name string) bool { return set[ns+"/"+name] }
}

// TestCredentialsFor is R1.8's four-level precedence and R12.5's three TLS
// branches in one table, because the two are resolved together by one call and a
// case that checks only the credential half would not notice a TLS half that
// regressed alongside it.
func TestCredentialsFor(t *testing.T) {
	cases := []struct {
		name    string
		tgt     Target
		cfg     ConnConfig
		secrets []string // what exists() says exists, as "namespace/name"
		noProbe bool     // pass a nil SecretExists instead of a set
		want    Credentials
		wantErr string
	}{
		{
			// R1.8 level 1: --credential-secret, spelled "ns/name" and matching
			// the target's own namespace.
			name:    "flag secret, same namespace",
			tgt:     tgtPG("pg", "pg-1", "primary"),
			cfg:     ConnConfig{Secret: "db/pg-app"},
			secrets: []string{"db/pg-app", "db/pg-ca"},
			want:    Credentials{Secret: "db/pg-app", SSLMode: "verify-ca", CASecret: "db/pg-ca"},
		},
		{
			// R1.8 level 1, the other spelling: a bare name means the target's own
			// namespace.
			name:    "flag secret, bare name",
			tgt:     tgtPG("pg", "pg-1", "primary"),
			cfg:     ConnConfig{Secret: "pg-app"},
			secrets: []string{"db/pg-app"},
			want:    Credentials{Secret: "db/pg-app", SSLMode: "prefer"},
		},
		{
			// An "ns/name" pointing at another namespace is checked *there*, which
			// is a stricter probe than discovery's (its informer is scoped to the
			// cluster's namespace) and the same precedence.
			name:    "flag secret in another namespace",
			tgt:     tgtPG("pg", "pg-1", "primary"),
			cfg:     ConnConfig{Secret: "other/pg-app"},
			secrets: []string{"other/pg-app"},
			want:    Credentials{Secret: "other/pg-app", SSLMode: "prefer"},
		},
		{
			// A flag naming a secret that does not exist continues the chain rather
			// than erroring, so the cluster renders and the collector falls back.
			name:    "flag secret missing, falls back to -app",
			tgt:     tgtPG("pg", "pg-1", "primary"),
			cfg:     ConnConfig{Secret: "db/pg-typo"},
			secrets: []string{"db/pg-app"},
			want:    Credentials{Secret: "db/pg-app", SSLMode: "prefer"},
		},
		{
			// R1.8 level 2: CNPG's app-user secret, and R12.4's named zero-config
			// path.
			name:    "cluster app secret",
			tgt:     tgtPG("pg", "pg-1", "primary"),
			secrets: []string{"db/pg-app"},
			want:    Credentials{Secret: "db/pg-app", SSLMode: "prefer"},
		},
		{
			// R1.8 level 3: a configured username with no secret, so the password
			// comes from the config and is the only place the opener can get it.
			name: "configured username",
			tgt:  tgtPG("pg", "pg-1", "primary"),
			cfg:  ConnConfig{Username: "pg_monitor_user", Password: "s3cret"},
			want: Credentials{Role: "pg_monitor_user", Password: "s3cret", SSLMode: "prefer"},
		},
		{
			// R1.8 level 4: nothing at all. The cluster still renders (R1.6) and
			// this error is what a SQL collector reports.
			name:    "no credential",
			tgt:     tgtPG("pg", "pg-1", "primary"),
			wantErr: "no credential",
		},
		{
			// A nil probe cannot confirm -app, so level 2 is skipped; an explicit
			// flag still counts (precedence, not existence), which is the same
			// tolerance discovery has.
			name:    "nil probe, flag set",
			tgt:     tgtPG("pg", "pg-1", "primary"),
			cfg:     ConnConfig{Secret: "pg-app"},
			noProbe: true,
			want:    Credentials{Secret: "db/pg-app", SSLMode: "prefer"},
		},
		{
			name:    "nil probe, username only",
			tgt:     tgtPG("pg", "pg-1", "primary"),
			cfg:     ConnConfig{Username: "monitor"},
			noProbe: true,
			want:    Credentials{Role: "monitor", SSLMode: "prefer"},
		},
		{
			name:    "no cluster name",
			tgt:     Target{Namespace: "db", Pod: "pg-1", Role: "primary", Source: SourceSQL, PGMajor: 16},
			cfg:     ConnConfig{Username: "monitor"},
			wantErr: "no cluster name",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exists SecretExists
			if !tc.noProbe {
				exists = existsFrom(tc.secrets...)
			}
			got, err := CredentialsFor(tc.tgt, tc.cfg, exists)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("CredentialsFor = %+v, want an error containing %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CredentialsFor: %v", err)
			}
			if got != tc.want {
				t.Errorf("CredentialsFor = %+v\nwant              %+v", got, tc.want)
			}
		})
	}
}

// TestCredentialsForNeverSuperuser is R12.4's last clause — "never connect as the
// superuser" — checked on both routes to it, plus the negative control that proves
// the check is a rule about the superuser rather than a rule about every secret.
func TestCredentialsForNeverSuperuser(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ConnConfig
		secrets []string
		wantErr string
		want    Credentials
	}{
		{
			// CNPG names the superuser's secret <cluster>-superuser (docs/research
			// §2.7), so the name alone says whose credential it is.
			name:    "flag names the superuser secret",
			cfg:     ConnConfig{Secret: "db/pg-superuser"},
			secrets: []string{"db/pg-superuser", "db/pg-app"},
			wantErr: "forbids connecting as the superuser",
		},
		{
			// The configured username is postgres: Postgres' one built-in superuser,
			// and the role CNPG's exporter used before CVE-2026-44477.
			name:    "configured username is postgres",
			cfg:     ConnConfig{Username: "postgres", Password: "x"},
			secrets: []string{},
			wantErr: "forbids connecting as the superuser",
		},
		{
			// Case folded: an operator typing POSTGRES means the same role, and
			// Postgres folds unquoted identifiers anyway.
			name:    "configured username is POSTGRES",
			cfg:     ConnConfig{Username: "POSTGRES"},
			secrets: []string{},
			wantErr: "forbids connecting as the superuser",
		},
		{
			// The negative control: -app is not the superuser's secret, and a
			// cluster that has both an -app secret and a configured postgres
			// username resolves to -app (R1.8 ranks it higher), so the rule fires
			// on the *selected* credential and not on every input.
			name:    "app secret outranks a postgres username",
			cfg:     ConnConfig{Username: "postgres"},
			secrets: []string{"db/pg-app"},
			want:    Credentials{Secret: "db/pg-app", SSLMode: "prefer"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CredentialsFor(tgtPG("pg", "pg-1", "primary"), tc.cfg, existsFrom(tc.secrets...))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("CredentialsFor = %+v, want an error containing %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CredentialsFor: %v", err)
			}
			if got != tc.want {
				t.Errorf("CredentialsFor = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSSLModeFor is R12.5's derivation as a table, including the half the
// requirement is really about: which secret is *not* read.
func TestSSLModeFor(t *testing.T) {
	cases := []struct {
		name     string
		cfg      ConnConfig
		secrets  []string
		wantMode string
		wantCA   string
		wantErr  string
	}{
		// The default, with the CA present: R12.5's named default.
		{
			name: "default with ca", secrets: []string{"db/pg-ca"},
			wantMode: "verify-ca", wantCA: "db/pg-ca",
		},
		// The default, with no CA: R12.5's "fall back to prefer when no -ca
		// secret exists".
		{
			name: "default without ca", secrets: []string{"db/pg-app"},
			wantMode: "prefer", wantCA: "",
		},
		// Every explicit mode, with a CA available. The modes that verify get the
		// CA; the ones that do not get none.
		{
			name: "explicit disable", cfg: ConnConfig{SSLMode: "disable"}, secrets: []string{"db/pg-ca"},
			wantMode: "disable", wantCA: "",
		},
		{
			name: "explicit allow", cfg: ConnConfig{SSLMode: "allow"}, secrets: []string{"db/pg-ca"},
			wantMode: "allow", wantCA: "",
		},
		{
			name: "explicit prefer", cfg: ConnConfig{SSLMode: "prefer"}, secrets: []string{"db/pg-ca"},
			wantMode: "prefer", wantCA: "",
		},
		{
			name: "explicit require", cfg: ConnConfig{SSLMode: "require"}, secrets: []string{"db/pg-ca"},
			wantMode: "require", wantCA: "",
		},
		{
			name: "explicit verify-ca", cfg: ConnConfig{SSLMode: "verify-ca"}, secrets: []string{"db/pg-ca"},
			wantMode: "verify-ca", wantCA: "db/pg-ca",
		},
		{
			name: "explicit verify-full", cfg: ConnConfig{SSLMode: "verify-full"}, secrets: []string{"db/pg-ca"},
			wantMode: "verify-full", wantCA: "db/pg-ca",
		},
		// An explicit mode that needs a CA, with none: an override we cannot
		// honour errors rather than downgrading, because R12.5's prefer fallback is
		// the default's fallback and not an override's.
		{
			name: "explicit verify-ca without ca", cfg: ConnConfig{SSLMode: "verify-ca"},
			secrets: []string{"db/pg-app"}, wantErr: "requires a CA",
		},
		{
			name: "explicit verify-full without ca", cfg: ConnConfig{SSLMode: "verify-full"},
			secrets: []string{"db/pg-app"}, wantErr: "requires a CA",
		},
		// A mode Postgres does not have: an error, not a silent fallback — a typo
		// in a TLS setting that silently disables verification is the failure
		// R12.5 exists to prevent.
		{
			name: "unrecognised mode", cfg: ConnConfig{SSLMode: "verify_cas"}, secrets: []string{"db/pg-ca"},
			wantErr: "not one of",
		},
		{
			name: "wrong case", cfg: ConnConfig{SSLMode: "Verify-CA"}, secrets: []string{"db/pg-ca"},
			wantErr: "not one of",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, ca, err := SSLModeFor(tgtPG("pg", "pg-1", "primary"), tc.cfg, existsFrom(tc.secrets...))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("SSLModeFor = %q/%q, want an error containing %q", mode, ca, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SSLModeFor: %v", err)
			}
			if mode != tc.wantMode || ca != tc.wantCA {
				t.Errorf("SSLModeFor = %q/%q, want %q/%q", mode, ca, tc.wantMode, tc.wantCA)
			}
			// R12.5's whole point, asserted on every branch rather than trusted to
			// the code path: the CA is <cluster>-ca and never <cluster>-server,
			// because the server secret holds a private key we should not hold.
			if strings.HasSuffix(ca, "-server") {
				t.Errorf("CA secret %q is the server's leaf (R12.5: reading it hands us a private key we do not need)", ca)
			}
		})
	}

	t.Run("no cluster name", func(t *testing.T) {
		if _, _, err := SSLModeFor(Target{Namespace: "db", Pod: "pg-1"}, ConnConfig{}, existsFrom()); err == nil {
			t.Error("SSLModeFor = nil error, want a refusal (no cluster name means no <cluster>-ca to name)")
		}
	})
}

// TestValidSSLMode checks the closed set from both sides: every member of SSLModes
// is valid, and the shapes that are not members (wrong case, a near-miss, the empty
// string, which means "derive" rather than a mode) are not.
func TestValidSSLMode(t *testing.T) {
	for _, m := range SSLModes {
		if !ValidSSLMode(m) {
			t.Errorf("ValidSSLMode(%q) = false, want true (it is in SSLModes)", m)
		}
	}
	for _, m := range []string{"", "Verify-CA", "verify_cas", "VERIFY-CA", "required", "no-tls", "SSL"} {
		if ValidSSLMode(m) {
			t.Errorf("ValidSSLMode(%q) = true, want false", m)
		}
	}
}

// TestMetricsExporterRoleSQL is the "document the exact CREATE ROLE/GRANT
// statements" half of R12.4 checked as data: the two clauses that matter are
// present, the role named in the SQL is the role the constant names, and the string
// is still byte-for-byte the one docs/research/cloudnativepg.md recommends.
func TestMetricsExporterRoleSQL(t *testing.T) {
	if !strings.Contains(MetricsExporterRoleSQL, "GRANT pg_monitor TO "+MetricsExporterRole+";") {
		t.Errorf("the SQL does not grant pg_monitor to %s:\n%s", MetricsExporterRole, MetricsExporterRoleSQL)
	}
	if !strings.Contains(MetricsExporterRoleSQL, "CREATE ROLE "+MetricsExporterRole+" ") {
		t.Errorf("the SQL does not create %s:\n%s", MetricsExporterRole, MetricsExporterRoleSQL)
	}
	// R12.4's reason the pattern exists (CNPG CVE-2026-44477: the exporter used to
	// connect as the superuser) is the NOSUPERUSER clause; without it the role is
	// just another login.
	if !strings.Contains(MetricsExporterRoleSQL, "NOSUPERUSER") {
		t.Error("the SQL omits NOSUPERUSER, which is the clause the CVE-2026-44477 fix is about")
	}

	// The code and the doc carry one copy of the recommendation, and the test is
	// what makes that claim instead of the comment: if either side is edited
	// alone, this fails.
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "research", "cloudnativepg.md"))
	if err != nil {
		t.Fatalf("read docs/research/cloudnativepg.md: %v", err)
	}
	doc := string(raw)
	if !strings.Contains(doc, MetricsExporterRoleSQL) {
		t.Errorf("MetricsExporterRoleSQL is not the SQL in docs/research/cloudnativepg.md §4.7:\n%s", MetricsExporterRoleSQL)
	}
}
