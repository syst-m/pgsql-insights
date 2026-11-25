// The SQL opener for this process: the collector.SQLOpener value the seven SQL
// collectors take as their first argument.
//
// Before this file the argument had no production value anywhere in the
// repository, and sql.go:34-48 is the record of why. The sentence there is the
// exhibit, and its tense is the point: it reads "It was written as an interface
// because go.mod had no Postgres driver" — had, not has — because the paragraph
// now names the answer in the same breath it names the deferral. Before
// internal/collector/pgxopener the only implementation of the interface in the
// module was database_test.go:52's fakeOpener, and conn.go:22-23 carried the same
// deferral ("consumed by whichever SQLOpener implementation the driver decision
// produces"), which is now a statement about that file rather than an open
// question. This file is the two pieces of cmd/pgsi-side glue the real opener
// needs, which are the two things it cannot build itself without importing
// internal/kube.
//
// Why the glue lives here and not in pgxopener. pgxopener.Secrets is an interface
// with three methods, and pooler.go:56-61 states the reason for that shape for the
// HTTP half: "A collector taking a func keeps the dependency a comment, which is
// what lets every rule in this file be tested without a cluster." The same
// argument applies to the secret reader — importing internal/kube into pgxopener
// would make a collector package depend on a Kubernetes client, which is exactly
// what the interface-based seams in that package exist to avoid. So the
// implementation over a clientset lives in the composition root, which already
// imports both.
package main

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/frankellis/pgsql-insights/internal/collector"
	"github.com/frankellis/pgsql-insights/internal/collector/pgxopener"
	"github.com/frankellis/pgsql-insights/internal/kube"
)

// kubeSecrets reads Postgres credentials and cluster CAs out of the cluster's
// Secrets, implementing pgxopener.Secrets over a clientset.
//
// It takes kubernetes.Interface rather than *kube.Client for the reason
// pgxopener's docblock gives for every seam in that package: the interface is
// what makes the type testable without a cluster. k8s.io/client-go ships a fake
// clientset (k8s.io/client-go/kubernetes/fake, already used that way in
// internal/kube/client_test.go:213), so opener_test.go can seed the exact
// secrets R1.8's precedence walks — <cluster>-app, <cluster>-superuser, the
// configured one — and assert what the opener connects with.
type kubeSecrets struct {
	cs kubernetes.Interface
}

// Exists reports whether the Secret exists, which is the probe conn.go:85's
// SecretExists type asks for.
//
// It uses context.Background rather than taking one: SecretExists is
// `func(namespace, name string) bool` (conn.go:85) and the signature has no
// context to thread, which is a property of conn.go's type and not of this
// implementation. The read is a single namespaced GET against the informer-backed
// clientset, so the missing cancellation path costs nothing in practice.
func (s kubeSecrets) Exists(namespace, name string) bool {
	if s.cs == nil {
		return false
	}
	_, err := s.cs.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

// Read returns the Secret's username and password keys.
//
// The key names are conn.go's, not CNPG's: CredentialsFor's docblock says the
// username "lives in the secret's own `username` key" (conn.go:117-119) and
// Credentials.Password is "set only for a configured credential … a
// secret-sourced one leaves it empty for the opener to read" (conn.go:124).
// CNPG's -app secret also carries `db`, which is why SQLConfig is expected to
// grow a Database field (statements.go's head docblock anticipates it); until it
// does, pgxopener.DefaultDatabase answers that question and this reader ignores
// the key.
func (s kubeSecrets) Read(ctx context.Context, namespace, name string) (username, password string, err error) {
	if s.cs == nil {
		return "", "", fmt.Errorf("cmd/pgsi: no clientset to read secret %s/%s from", namespace, name)
	}
	sec, err := s.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	return string(sec.Data["username"]), string(sec.Data["password"]), nil
}

// ReadCA returns the Secret's PEM certificate, the third method
// pgxopener.Secrets needs and the one that carries R12.5's whole TLS half.
//
// The key names are CNPG's, not conn.go's. R12.5 names the secret (<cluster>-ca)
// and conn.go:130-132 names the reference, but neither names the key, and the
// answer is on the wire: CNPG 1.30.2's cluster CA secret carries the PEM under
// `ca.crt` — verified against the reference instance's four namespaces, every one
// of which has a <cluster>-ca whose only key is that one. `tls.crt` is the
// kubernetes.io/tls convention and the fallback for a cluster whose CA secret came
// from a cert-manager-style issuer rather than from CNPG itself; the two are tried
// in that order rather than probed for existence, because a Secret's key set is not
// something the informer can answer more cheaply than the read itself.
//
// An empty result is not an error here. pgxopener's rootCertFor decides what an
// empty certificate means for the mode in use — it errors for verify-ca, because a
// mode that verifies with nothing is the downgrade R12.5 exists to prevent — and a
// reader that turned "the key is absent" into an error would take that judgement
// away from the one place that has the mode to judge with.
func (s kubeSecrets) ReadCA(ctx context.Context, namespace, name string) (pem string, err error) {
	if s.cs == nil {
		return "", fmt.Errorf("cmd/pgsi: no clientset to read CA secret %s/%s from", namespace, name)
	}
	sec, err := s.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	for _, key := range []string{"ca.crt", "tls.crt"} {
		if v, ok := sec.Data[key]; ok && len(v) > 0 {
			return string(v), nil
		}
	}
	return "", nil
}

// newSQLOpener builds the process's SQL opener, or nil when it cannot have one.
//
// The nil return is the shape that keeps the registry honest about the driver.
// newRegistry takes a collector.SQLOpener and registers the seven SQL collectors
// only when it is non-nil, so a process with no usable clientset gets a registry
// holding the pooler alone — which is what it had before the driver existed —
// rather than seven collectors that fail every round with a nil-pointer error
// from a client nobody built. The alternative, panicking on a nil clientset,
// would make `pgsi serve --discover=false` unusable, and D9's `api` role is
// exactly the role that serves without collecting.
//
// connCfg is the connection half of R1.8's credential flags, passed in rather
// than re-read here so the opener and discovery.CredentialsConfig are
// constructed from one set of parsed flags. It is not built from argv in
// main.go's call yet for the same reason the Overrides argument is still empty:
// R11.8's flag pre-scan is filed with Phase 2 step 5's R11.8 work, not with this
// wiring, so the flags exist in serveFlags and are plumbed to discovery but not
// yet to the opener.
func newSQLOpener(kc *kube.Client, connCfg collector.ConnConfig) collector.SQLOpener {
	if kc == nil || kc.Clientset == nil {
		return nil
	}
	opener, err := pgxopener.New(connCfg, kubeSecrets{cs: kc.Clientset})
	if err != nil {
		// New's only failure is a nil Secrets, which the line above cannot produce.
		// Returning nil rather than panicking keeps the caller's "no opener" path
		// the single path, so there is exactly one way to end up with a
		// pooler-only registry.
		return nil
	}
	return opener
}
