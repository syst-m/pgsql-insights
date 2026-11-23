package collector

import (
	"fmt"
	"strings"
)

// This file is R12.4 and R12.5: which Postgres role a SQL collector
// authenticates as, and how the connection to it is encrypted. IMPLEMENTATION.md's
// Phase 2 step-3 row lists them beside the SQL rules — "the connection rules (the
// `pg_monitor` role, `sslmode` derivation)" — because a collector's SQL is only
// half of "how we read this instance"; the other half is who we are to it.
//
// Why here, and not in internal/discovery, which already answers a credential
// question for the API: the two answer different questions from overlapping
// inputs. discovery's ResolveCredential is R1.8's precedence rendered as the
// dto string the API reports (credentialSource: flag/cluster/config/none) — a
// reporting answer, about a cluster object. This file's question is connect-time:
// which role, from which secret, verifying the server with what. The reason it
// lives with the collectors rather than inside a driver type is that go.mod has
// no Postgres driver yet (sql.go's doc explains why the seam is an interface), so
// the rules are written now, tested now, and consumed by whichever SQLOpener
// implementation the driver decision produces — instead of being discovered later
// to have been implemented inside a type nobody can build a test against.
//
// The precedence itself is therefore implemented twice, here and in
// discovery.ResolveCredential, and that duplication is deliberate rather than an
// oversight: that function takes a *unstructured.Unstructured (it is called while
// iterating clusters) and this one takes a Target (it is called while connecting to
// one pod), and neither can call the other without importing a package it should
// not — collector must not import api/dto, and importing internal/discovery for
// one string would put the API's vocabulary in the collector layer. conn_test.go's
// table mirrors R1.8's four levels so a change to the precedence has to be made in
// both places and is caught in both.

// MetricsExporterRole is the role name R12.4 names: CNPG's own
// cnpg_metrics_exporter, the role the CNPG CVE-2026-44477 fix introduced so the
// exporter stopped authenticating as the postgres superuser.
//
// R12.4 says to *prefer* a dedicated pg_monitor-granted role and cites this one as
// the pattern. It is not our default login — our default is whatever R1.8's
// precedence resolves, which for a zero-config cluster is <cluster>-app — because
// CNPG 1.30.2 creates the role for its *own* pod-local exporter (docs/research
// §4.7: peer authentication on the pod-local Unix socket) and creates no secret for
// it (docs/research §2.7: "There is no `<cluster>-metrics` secret in v1.30.2"). A
// remote poller cannot use peer auth on a socket, so the role is only usable by us
// once an operator creates it and gives us its secret — which is exactly the
// --credential-secret path R1.8 ranks first. The name and the SQL are here so the
// documented answer to "what should I create?" is one string rather than a paragraph.
const MetricsExporterRole = "cnpg_metrics_exporter"

// MetricsExporterRoleSQL is the exact CREATE ROLE/GRANT pair R12.4 requires be
// documented, quoted verbatim from docs/research/cloudnativepg.md §4.7.
//
// R12.4's wording is "document the exact `CREATE ROLE`/`GRANT` statements", and the
// requirement is satisfied by a constant rather than by prose because a constant can
// be asserted: conn_test.go checks the two clauses that matter (the GRANT to
// pg_monitor, the NOSUPERUSER) and checks byte-for-byte that the string still
// appears in the research doc it was taken from, so the code and the doc cannot
// drift into two different recommendations.
const MetricsExporterRoleSQL = `CREATE ROLE cnpg_metrics_exporter WITH LOGIN NOSUPERUSER NOCREATEDB
    NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT;
GRANT pg_monitor TO cnpg_metrics_exporter;`

// The two secret-name suffixes CNPG uses for the credentials R12.4 ranks, from
// docs/research/cloudnativepg.md §2.7's table of per-cluster secrets.
//
// appSecretSuffix is R12.4's named zero-config path ("support reading
// <cluster>-app"). superuserSecretSuffix matters for the opposite reason: §2.7
// lists <cluster>-superuser as *the superuser's* secret, so a secret name ending in
// -superuser tells us the credential behind it is the superuser's without us having
// to read the secret — which is how R12.4's "never connect as the superuser" is
// enforceable by a caller that only has an existence probe.
const (
	appSecretSuffix       = "-app"
	superuserSecretSuffix = "-superuser"
)

// SecretExists reports whether a Secret exists in a namespace.
//
// It is a function type rather than a clientset so the rules in this file are
// testable with a set literal, the same way discovery.ResolveCredential injects its
// probe. The production implementation is a secret informer lookup; nothing in the
// collector package needs one to answer R12.4 or R12.5.
type SecretExists func(namespace, name string) bool

// ConnConfig is the operator's Postgres-connection inputs: the credential the
// cluster has no secret for, and the sslmode override.
//
// It is the connection half of what cmd/pgsi's flags carry, kept separate from
// SQLConfig because the two are consumed by different things: SQLConfig travels
// with each collector's Begin call (R2.7's timeout, R2.8's application_name), while
// ConnConfig belongs to the opener, which is one object per poller and resolves
// per-target credentials from it. Password is here because R1.8's third level is a
// username *and* password configured directly; it is never logged (R12.6's rule
// about not logging credentials applies to more than query parameters).
type ConnConfig struct {
	// Secret is --credential-secret: "namespace/name", or a bare name meaning
	// "in each target's own namespace". R1.8 ranks it above every other source.
	Secret string
	// Username and Password are R1.8's third level — the single configured
	// credential, used when no secret is named and the cluster has no -app secret.
	Username string
	Password string
	// SSLMode is R12.5's "TLS to Postgres configurable": an explicit sslmode.
	// Empty means derive it, which is the documented default (see SSLModeFor).
	SSLMode string
}

// Credentials is the resolved answer for one target: R12.4's role half and
// R12.5's TLS half, in the one struct an opener needs to open a connection.
//
// The fields are strings rather than dto values on purpose: the DTO is the API's
// wire contract, and a collector that imports it cannot be imported back by
// internal/api (which owns the DTO and will import the collectors at step 4).
type Credentials struct {
	// Role is the role we authenticate as. Empty for a secret-sourced credential,
	// because the username lives in the secret's own `username` key and this
	// resolution has an existence probe, not a reader: the opener reads the key.
	// It is set when the credential is the configured username instead.
	Role string
	// Secret is the "namespace/name" of the Secret to read the password from, or
	// empty when the credential is configured directly and there is no secret.
	Secret string
	// Password is set only for a configured credential (R1.8's third level); a
	// secret-sourced one leaves it empty for the opener to read.
	Password string
	// SSLMode is R12.5's resolved sslmode for this target.
	SSLMode string
	// CASecret is the "namespace/name" of the secret to verify the server with —
	// <cluster>-ca (R12.5), empty when the mode needs no CA.
	CASecret string
}

// CredentialsFor resolves R12.4's role and R12.5's TLS for one target.
//
// The credential half follows R1.8's precedence — --credential-secret, then
// <cluster>-app, then the configured username, then nothing — with R12.4's one hard
// rule applied to whatever that precedence produces: never the superuser. The
// rule is applied as an error rather than as a fall-through, so a --credential-secret
// naming <cluster>-superuser fails loudly instead of quietly connecting as a
// different role than the operator configured; and a cluster whose only credential
// is the superuser secret has no usable credential at all, which is R1.8's
// "the cluster still renders and the SQL collectors report why they cannot
// connect" case reached by a new route.
//
// The TLS half is SSLModeFor, called here so a caller asking "who am I to this
// pod" gets both halves in one call and cannot get the role without the mode.
func CredentialsFor(tgt Target, cfg ConnConfig, exists SecretExists) (Credentials, error) {
	if tgt.Cluster == "" {
		return Credentials{}, fmt.Errorf("collector: target %s has no cluster name, so neither R12.4's <cluster>-app nor R12.5's <cluster>-ca can be named", tgt.Key())
	}

	creds, err := resolveCredential(tgt, cfg, exists)
	if err != nil {
		return Credentials{}, err
	}
	mode, ca, err := SSLModeFor(tgt, cfg, exists)
	if err != nil {
		return Credentials{}, err
	}
	creds.SSLMode = mode
	creds.CASecret = ca
	return creds, nil
}

// resolveCredential walks R1.8's four levels for one target and returns the
// role/secret half of Credentials, applying R12.4's "never connect as the
// superuser" to whatever the walk lands on.
//
// The shape is four ifs rather than a switch with a fallthrough because the
// first level's fall-through is conditional: a --credential-secret that does not
// exist continues the chain, and one that exists stops it, and a switch expresses
// only the unconditional version of that (Go's fallthrough skips the next case's
// condition, which would let a missing -app secret be selected anyway). discovery
// has the same four ifs for the same reason.
func resolveCredential(tgt Target, cfg ConnConfig, exists SecretExists) (Credentials, error) {
	var c Credentials

	// 1. --credential-secret.
	if cfg.Secret != "" {
		ns, name := splitSecret(tgt.Namespace, cfg.Secret)
		switch {
		case name == "":
			return Credentials{}, fmt.Errorf("collector: target %s: --credential-secret %q names no secret", tgt.Key(), cfg.Secret)
		// exists == nil counts as "the configured answer": with no way to check,
		// R1.8's first level is about precedence rather than existence — the same
		// tolerance discovery.ResolveCredential has, and for the same reason.
		case exists == nil || exists(ns, name):
			// Unlike discovery — whose probe is scoped to the cluster's own
			// namespace and so cannot confirm an "ns/name" pointing elsewhere —
			// this one checks the namespace it is given, in both spellings. The
			// precedence is unchanged; the check is just not skipped.
			//
			// R12.4's superuser rule lives in this branch because this is the only
			// branch that can name the superuser's secret (docs/research §2.7 lists
			// <cluster>-superuser as exactly that), and it errors rather than
			// falling through: silently connecting as a different role than the
			// operator configured is how a forbidden credential becomes invisible.
			if isSuperuser("", name) {
				return Credentials{}, fmt.Errorf("collector: target %s: --credential-secret %s is the superuser's secret and R12.4 forbids connecting as the superuser", tgt.Key(), ns+"/"+name)
			}
			c.Secret = ns + "/" + name
			return c, nil
		}
		// The named secret does not exist: R1.8's chain continues rather than
		// erroring, so the cluster renders and the collectors fall back.
	}

	// 2. <cluster>-app — R1.8's second level, and R12.4's named zero-config path
	// ("support reading <cluster>-app as the zero-config path").
	if exists != nil && exists(tgt.Namespace, tgt.Cluster+appSecretSuffix) {
		c.Secret = tgt.Namespace + "/" + tgt.Cluster + appSecretSuffix
		return c, nil
	}

	// 3. The single configured credential (R1.8's third level): a username with
	// no secret, so the password comes from the config too.
	if cfg.Username != "" {
		if isSuperuser(cfg.Username, "") {
			return Credentials{}, fmt.Errorf("collector: target %s: configured username %q is the superuser and R12.4 forbids connecting as the superuser", tgt.Key(), cfg.Username)
		}
		c.Role, c.Password = cfg.Username, cfg.Password
		return c, nil
	}

	// 4. None. R1.6's zero-prerequisite case: the cluster still renders in the
	// API, and this error is what a SQL collector reports when it has nothing to
	// connect with.
	return Credentials{}, fmt.Errorf("collector: target %s: no credential — no --credential-secret, no %s%s secret, no configured username",
		tgt.Key(), tgt.Cluster, appSecretSuffix)
}

// SSLModeFor is R12.5's sslmode derivation, on its own so a caller that has a
// credential from somewhere else can still ask the TLS question, and so the
// rule's three branches are testable as a table.
//
// The three, in order:
//
//  1. An explicit sslmode wins (R12.5's "configurable"). It must be one Postgres
//     knows (SSLModes); an unrecognised value is an error rather than a silent
//     fallback, because a typo in a TLS setting that silently downgrades
//     verification is the failure mode R12.5 exists to prevent. A mode that needs a
//     CA (verify-ca, verify-full) also needs <cluster>-ca to exist, and an explicit
//     request we cannot honour errors rather than downgrading: R12.5's `prefer`
//     fallback is the *default's* fallback, not an override's.
//  2. Otherwise, if <cluster>-ca exists: verify-ca with that CA. This is R12.5's
//     named default — "a client verifies the server with <cluster>-ca, so the
//     default is `verify-ca` using `<cluster>-ca`".
//  3. Otherwise: prefer. R12.5's "fall back to `prefer` when no `-ca` secret
//     exists".
//
// The rule the whole requirement turns on is which secret is *not* read:
// <cluster>-server holds the server's certificate and private key, and R12.5 says
// reading it "hands us a private key we do not need and should not hold" — a
// client verifies with the CA, not with the leaf. So CASecret is only ever
// <cluster>-ca, and conn_test.go asserts that across every branch rather than
// trusting the code path not to reach for the leaf.
func SSLModeFor(tgt Target, cfg ConnConfig, exists SecretExists) (mode, caSecret string, err error) {
	if tgt.Cluster == "" {
		return "", "", fmt.Errorf("collector: target %s has no cluster name, so R12.5's <cluster>-ca cannot be named", tgt.Key())
	}

	ca := ""
	if exists != nil && exists(tgt.Namespace, tgt.Cluster+"-ca") {
		ca = tgt.Namespace + "/" + tgt.Cluster + "-ca"
	}

	if cfg.SSLMode != "" {
		if !ValidSSLMode(cfg.SSLMode) {
			return "", "", fmt.Errorf("collector: target %s: sslmode %q is not one of %s (R12.5)",
				tgt.Key(), cfg.SSLMode, strings.Join(SSLModes, " "))
		}
		if modeNeedsCA(cfg.SSLMode) {
			if ca == "" {
				return "", "", fmt.Errorf("collector: target %s: sslmode %q requires a CA and %s%s does not exist (R12.5: a client verifies the server with <cluster>-ca)",
					tgt.Key(), cfg.SSLMode, tgt.Cluster, "-ca")
			}
			return cfg.SSLMode, ca, nil
		}
		return cfg.SSLMode, "", nil
	}

	if ca != "" {
		return "verify-ca", ca, nil
	}
	return "prefer", "", nil
}

// SSLModes is the sslmode set Postgres accepts, and the closed set ValidSSLMode
// allows through. R12.5's "configurable" needs a closed set: an arbitrary string
// reaching a connection string is a TLS setting whose failure only appears at
// connect time.
const (
	SSLModeDisable    = "disable"
	SSLModeAllow      = "allow"
	SSLModePrefer     = "prefer"
	SSLModeRequire    = "require"
	SSLModeVerifyCA   = "verify-ca"
	SSLModeVerifyFull = "verify-full"
)

// SSLModes lists every accepted sslmode, in Postgres' own order (no TLS → TLS
// attempted → TLS required → chain verified → hostname verified).
var SSLModes = []string{
	SSLModeDisable,
	SSLModeAllow,
	SSLModePrefer,
	SSLModeRequire,
	SSLModeVerifyCA,
	SSLModeVerifyFull,
}

// ValidSSLMode reports whether mode is one of SSLModes.
func ValidSSLMode(mode string) bool {
	for _, m := range SSLModes {
		if m == mode {
			return true
		}
	}
	return false
}

// modeNeedsCA reports whether an sslmode verifies the server against a CA, and so
// cannot be used without one. disable/allow/prefer/require do not verify anything
// (allow and prefer try TLS but accept a plaintext fallback, so there is no
// certificate to verify); verify-ca and verify-full do.
func modeNeedsCA(mode string) bool {
	return mode == SSLModeVerifyCA || mode == SSLModeVerifyFull
}

// isSuperuser reports whether a resolved credential is the superuser's, which
// R12.4 forbids in two words: "never connect as the superuser".
//
// Two ways to know it without reading the secret's contents: the username is
// postgres (Postgres' one built-in superuser, and the role CNPG's pre-1.29.1
// exporter used before CVE-2026-44477), or the secret is the cluster's superuser
// secret (docs/research §2.7 lists <cluster>-superuser as exactly that). Case is
// folded on the username because Postgres folds unquoted identifiers and an
// operator typing POSTGRES means the same role.
func isSuperuser(role, secret string) bool {
	if role != "" && strings.EqualFold(role, "postgres") {
		return true
	}
	return strings.HasSuffix(secret, superuserSecretSuffix)
}

// splitSecret resolves the two spellings R1.8 accepts for a secret reference —
// "namespace/name", and a bare name meaning "in the target's own namespace" — into
// both halves.
//
// discovery.ResolveCredential has the same rule inline for the same two spellings;
// this is the second copy for the reason given in the file header, and the two
// agree because both are R1.8's one sentence about it.
func splitSecret(targetNS, ref string) (ns, name string) {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return targetNS, ref
}
