package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// The test file for the cmd/pgsi-side opener glue. It exists because the two
// pieces in opener.go are the ones the pgxopener package deliberately refuses to
// own: the clientset read and the nil-vs-real decision.

// secret builds a CNPG-shaped Secret: the username and password keys conn.go's
// credential rules name, plus the `db` key CNPG also writes and pgxopener ignores
// until SQLConfig grows a Database field.
func secret(ns, name, user, pass string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Data: map[string][]byte{
			"username": []byte(user),
			"password": []byte(pass),
			"db":       []byte("postgres"),
		},
	}
}

func TestKubeSecretsExistsAndRead(t *testing.T) {
	cs := fake.NewSimpleClientset(
		secret("db", "pg-app", "pguser", "app-password"),
		secret("db", "pg-standby", "standby", "standby-password"),
	)
	s := kubeSecrets{cs: cs}

	for _, tc := range []struct {
		ns, name, wantUser, wantPass string
		wantExists                   bool
	}{
		{"db", "pg-app", "pguser", "app-password", true},
		{"db", "pg-standby", "standby", "standby-password", true},
		// The negative case: a name that does not exist must report false rather
		// than an empty credential, because CredentialsFor's precedence walk
		// decides whether to fall through to the next source on this answer alone.
		{"db", "pg-missing", "", "", false},
		// The other namespace: a Secret named pg-app in `other` is not the one in
		// `db`, and R1.8's "namespace/name" reference is the only thing that
		// distinguishes them.
		{"other", "pg-app", "", "", false},
	} {
		if got := s.Exists(tc.ns, tc.name); got != tc.wantExists {
			t.Errorf("Exists(%s/%s) = %v, want %v", tc.ns, tc.name, got, tc.wantExists)
		}
		if !tc.wantExists {
			continue
		}
		user, pass, err := s.Read(context.Background(), tc.ns, tc.name)
		if err != nil {
			t.Errorf("Read(%s/%s): %v", tc.ns, tc.name, err)
			continue
		}
		if user != tc.wantUser || pass != tc.wantPass {
			t.Errorf("Read(%s/%s) = (%q, %q), want (%q, %q)", tc.ns, tc.name, user, pass, tc.wantUser, tc.wantPass)
		}
	}
}

// TestKubeSecretsNilClientset pins the degenerate client. Exists must be false
// rather than a nil-pointer dereference: kubeSecrets is built from
// kc.Clientset, and a *kubernetes.Clientset that is nil is a value this package
// can be handed.
func TestKubeSecretsNilClientset(t *testing.T) {
	s := kubeSecrets{}
	if s.Exists("db", "pg-app") {
		t.Error("Exists on a nil clientset = true, want false")
	}
	if _, _, err := s.Read(context.Background(), "db", "pg-app"); err == nil {
		t.Error("Read on a nil clientset: err = nil, want an error")
	}
}

// TestKubeSecretsSecretWithoutUsername pins the shape a non-CNPG secret has. The
// pgxopener docblock says the username "lives in the secret's own `username` key"
// (conn.go:117-119), so a secret without that key yields an empty username — and
// DSN rejects an empty Role rather than building a connection string with a blank
// user. Together the two halves mean a malformed secret surfaces as an error
// rather than as a connection attempt as an unnamed role.
func TestKubeSecretsSecretWithoutUsername(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg-app"},
		Data:       map[string][]byte{"password": []byte("only-a-password")},
	})
	s := kubeSecrets{cs: cs}
	user, pass, err := s.Read(context.Background(), "db", "pg-app")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if user != "" {
		t.Errorf("username = %q, want \"\" (the secret has no username key)", user)
	}
	if pass != "only-a-password" {
		t.Errorf("password = %q, want \"only-a-password\"", pass)
	}
}

// TestKubeSecretsReadCA pins the two key names the CA reader accepts and the order
// it tries them in.
//
// The keys are CNPG's, not conn.go's: R12.5 names the secret and conn.go:130-132
// names the reference, but the key inside it is only answerable by looking at a
// real cluster, where <cluster>-ca carries the PEM under `ca.crt`. `tls.crt` is the
// kubernetes.io/tls convention, accepted second so a CA secret from a
// cert-manager-style issuer works too. A secret with neither returns an empty
// string and no error — the judgement about what an empty CA means belongs to
// pgxopener.rootCertFor, which is the side that knows the sslmode.
func TestKubeSecretsReadCA(t *testing.T) {
	cases := []struct {
		name string
		data map[string][]byte
		want string
	}{
		{"ca.crt (CNPG)", map[string][]byte{"ca.crt": []byte("PEM-A")}, "PEM-A"},
		{"tls.crt (kubernetes.io/tls)", map[string][]byte{"tls.crt": []byte("PEM-B")}, "PEM-B"},
		// Both keys: ca.crt wins, because it is CNPG's and the cluster's own CA is
		// the one R12.5 names.
		{"both keys, ca.crt wins", map[string][]byte{"ca.crt": []byte("PEM-A"), "tls.crt": []byte("PEM-B")}, "PEM-A"},
		// Neither key: empty, not an error.
		{"no certificate key", map[string][]byte{"username": []byte("u")}, ""},
		// Present but empty: also empty. len(v) > 0 is what makes the two agree
		// rather than returning "" from a key that exists and "" from one that does
		// not, which is the same answer for the caller either way.
		{"ca.crt present but empty", map[string][]byte{"ca.crt": {}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg-ca"},
				Data:       tc.data,
			})
			got, err := (kubeSecrets{cs: cs}).ReadCA(context.Background(), "db", "pg-ca")
			if err != nil {
				t.Fatalf("ReadCA: %v", err)
			}
			if got != tc.want {
				t.Errorf("ReadCA = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := (kubeSecrets{}).ReadCA(context.Background(), "db", "pg-ca"); err == nil {
		t.Error("ReadCA on a nil clientset: err = nil, want an error")
	}
}

// TestNewSQLOpenerNilWithoutAClientset pins the decision newRegistry branches on.
// Three inputs, two answers: no client at all and a client with no clientset both
// produce nil, and only a client with a clientset produces an opener.
//
// The nil cases matter more than the real one. A process started with
// --discover=false, or D9's `api` role, has no reason to have built a clientset,
// and the alternative to returning nil there is a registry of nine collectors
// that fail every round — which is R8.7's shape (a process that answers 200 and is
// not working) reproduced one layer down.
func TestNewSQLOpenerNilWithoutAClientset(t *testing.T) {
	if got := newSQLOpener(nil, collector.ConnConfig{}); got != nil {
		t.Errorf("newSQLOpener(nil, …) = %v, want nil", got)
	}
	if got := newSQLOpener(&kube.Client{}, collector.ConnConfig{}); got != nil {
		t.Errorf("newSQLOpener(client with nil clientset, …) = %v, want nil", got)
	}

	cs := fake.NewSimpleClientset(secret("db", "pg-app", "pguser", "pw"))
	got := newSQLOpener(&kube.Client{Clientset: cs}, collector.ConnConfig{})
	if got == nil {
		t.Fatal("newSQLOpener(client with a clientset, …) = nil, want an opener")
	}
	// The compile-time half: the return type is collector.SQLOpener, so this
	// assignment is the assertion. It is written as a runtime check here because
	// the compile-time one lives in opener.go's signature.
	var _ collector.SQLOpener = got
}

// TestNewSQLOpenerCarriesTheConnConfig is the only way to observe that the
// ConnConfig argument reaches the opener: Begin resolves credentials through it,
// so a ConnConfig naming a secret produces a different DSN than an empty one, and
// the difference is observable through the error Begin returns.
func TestNewSQLOpenerCarriesTheConnConfig(t *testing.T) {
	cs := fake.NewSimpleClientset(secret("db", "pg-app", "pguser", "the-configured-password"))
	tgt := collector.Target{
		Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary",
		Address: "127.0.0.1:1", // nothing listens; the error names the host
		Source:  collector.SourceSQL,
		PGMajor: 16,
	}

	opener := newSQLOpener(&kube.Client{Clientset: cs}, collector.ConnConfig{Secret: "db/pg-app"})
	if opener == nil {
		t.Fatal("newSQLOpener: nil")
	}
	_, err := opener.Begin(context.Background(), tgt, collector.SQLConfig{})
	if err == nil {
		t.Fatal("Begin: err = nil, want a connection error naming the host")
	}
	// The error is a dial error, not a credential error: that is the proof the
	// ConnConfig's Secret was found and read, because a failed read returns before
	// the dial.
	if want := "127.0.0.1:1"; !contains(err.Error(), want) {
		t.Errorf("Begin error = %q, want it to contain %q (the DSN's host)", err, want)
	}
}

// contains is strings.Contains, aliased so the test reads as an assertion.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
