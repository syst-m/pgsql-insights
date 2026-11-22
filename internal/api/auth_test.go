// Tests for R8.8 (auth modes) and R8.9 (per-route-group policy).
//
// These drive the middleware directly rather than through Server.Handler():
// route registration is A1's Phase 1a file, and the policy needs to be testable
// independently of which routes happen to be mounted today.

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// okHandler is the pass-through target: it echoes the path so a test can prove
// the request reached the handler, and the attached Identity.
func okHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		body := r.URL.Path
		if id, ok := IdentityFrom(r); ok {
			body += "|" + id.User
		}
		_, _ = w.Write([]byte(body))
	})
}

func TestParseAuthMode(t *testing.T) {
	cases := []struct {
		in      string
		want    AuthMode
		wantErr bool
	}{
		// "" is the unset value; R8.8's default is token, not none.
		{in: "", want: DefaultAuthMode},
		{in: "none", want: AuthNone},
		{in: "token", want: AuthToken},
		{in: "kube-rbac-proxy", want: AuthKubeRBACProxy},
		// Flag values are case-insensitive and whitespace-tolerant: a chart
		// value rendered from a template picks up both.
		{in: "TOKEN", want: AuthToken},
		{in: "None", want: AuthNone},
		{in: " token ", want: AuthToken},
		{in: "none ", want: AuthNone},
		{in: "\ttoken\n", want: AuthToken},
		{in: "kube-rbac-proxy ", want: AuthKubeRBACProxy},
		// Rejections: not one of R8.8's three values.
		{in: "basic", wantErr: true},
		{in: "kube_rbac_proxy", wantErr: true},
		{in: "none|token", wantErr: true},
		// R10.6's OIDC is deferred to the auth epic, not a Phase 5 flag value.
		{in: "oidc", wantErr: true},
	}
	for _, tc := range cases {
		t.Run("ParseAuthMode("+tc.in+")", func(t *testing.T) {
			got, err := ParseAuthMode(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseAuthMode(%q) = %q, want error", tc.in, got)
				}
				if !strings.Contains(err.Error(), "none|token|kube-rbac-proxy") {
					t.Errorf("error %q should name the valid values", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAuthMode(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseAuthMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTokenModeRequiresToken is R8.8's fail-closed rule: `--auth=token` with
// no token must be a startup error, not a server that accepts "".
func TestTokenModeRequiresToken(t *testing.T) {
	if _, err := NewAuthenticator(AuthToken, ""); err == nil {
		t.Fatal("NewAuthenticator(token, \"\") = nil error; token mode must fail closed on an empty token")
	}
	if _, err := NewAuthenticator(AuthMode("bogus"), "x"); err == nil {
		t.Fatal("NewAuthenticator(bogus) = nil error, want error")
	}
	// none and kube-rbac-proxy need no token.
	for _, m := range []AuthMode{AuthNone, AuthKubeRBACProxy} {
		if _, err := NewAuthenticator(m, ""); err != nil {
			t.Errorf("NewAuthenticator(%q, \"\"): unexpected error: %v", m, err)
		}
	}
}

func TestLoadToken(t *testing.T) {
	t.Run("trims the trailing newline", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "token")
		// `kubectl create secret generic --from-file=x` writes the file
		// verbatim, and every editor leaves a trailing newline.
		if err := os.WriteFile(p, []byte("s3cr3t\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadToken(p)
		if err != nil {
			t.Fatal(err)
		}
		if got != "s3cr3t" {
			t.Fatalf("LoadToken = %q, want %q", got, "s3cr3t")
		}
	})

	t.Run("whitespace-only file is an error", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(p, []byte("  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadToken(p); err == nil {
			t.Fatal("LoadToken on a whitespace-only file = nil error, want error")
		}
	})

	t.Run("missing file is an error naming the path", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "absent")
		_, err := LoadToken(p)
		if err == nil {
			t.Fatal("LoadToken on a missing file = nil error, want error")
		}
		if !strings.Contains(err.Error(), "absent") {
			t.Errorf("error %q should name the file", err)
		}
	})
}

func TestAuthenticateToken(t *testing.T) {
	a, err := NewAuthenticator(AuthToken, "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		header     string
		wantUser   string
		wantCode   string
		wantStatus int
		// wantMsgSub, when set, is a substring the AuthError message must
		// contain. It exists because "missing Authorization header" and
		// "invalid bearer token" are different answers and a test on the code
		// alone cannot tell them apart — both are 401/Unauthorized.
		wantMsgSub string
	}{
		{name: "valid", header: "Bearer s3cr3t", wantUser: "token"},
		{name: "scheme case-insensitive", header: "bearer s3cr3t", wantUser: "token"},
		{name: "BEARER", header: "BEARER s3cr3t", wantUser: "token"},
		// A *wrong* token is 401, not 403. dto's enum is the authority:
		// `ErrUnauthorized // missing/bad token (R8.8)` vs
		// `ErrForbidden // token lacks scope for the object (R12)`.
		{name: "wrong token", header: "Bearer wrong", wantCode: dto.ErrUnauthorized, wantStatus: http.StatusUnauthorized, wantMsgSub: "invalid bearer token"},
		// An empty bearer string must not match a configured token; this is
		// the case a naive `strings.TrimPrefix` implementation gets wrong by
		// comparing "" to "" when the header is absent.
		{name: "empty bearer value", header: "Bearer ", wantCode: dto.ErrUnauthorized, wantStatus: http.StatusUnauthorized, wantMsgSub: "invalid bearer token"},
		{name: "empty header", header: "", wantCode: dto.ErrUnauthorized, wantStatus: http.StatusUnauthorized, wantMsgSub: "missing Authorization header"},
		{name: "wrong scheme", header: "Basic s3cr3t", wantCode: dto.ErrUnauthorized, wantStatus: http.StatusUnauthorized, wantMsgSub: `unsupported Authorization scheme "Basic"`},
		{name: "token without scheme", header: "s3cr3t", wantCode: dto.ErrUnauthorized, wantStatus: http.StatusUnauthorized, wantMsgSub: `unsupported Authorization scheme "s3cr3t"`},
		// Token comparison is case-sensitive: EqualFold would accept these.
		{name: "case-changed token", header: "Bearer S3CR3T", wantCode: dto.ErrUnauthorized, wantStatus: http.StatusUnauthorized, wantMsgSub: "invalid bearer token"},
		// RFC 7235 allows optional whitespace between the scheme and the
		// credential. Splitting on a single literal space gets both of these
		// wrong: "Bearer" alone answers "missing Authorization header" when a
		// header is plainly present, and "Bearer  s3cr3t" yields the credential
		// " tok" — a wrong token that happens to be wrong for the wrong reason.
		{name: "scheme only, no space", header: "Bearer", wantCode: dto.ErrUnauthorized, wantStatus: http.StatusUnauthorized, wantMsgSub: "invalid bearer token"},
		{name: "extra whitespace is still valid", header: "Bearer    s3cr3t", wantUser: "token"},
		{name: "tab separator", header: "Bearer\ts3cr3t", wantUser: "token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			id, err := a.Authenticate(r)
			if tc.wantCode != "" {
				var ae *AuthError
				if err == nil {
					t.Fatalf("Authenticate(%q) = nil error, want %s", tc.header, tc.wantCode)
				}
				if !asAuthError(err, &ae) {
					t.Fatalf("Authenticate(%q) error %v is not an *AuthError", tc.header, err)
				}
				if ae.Code != tc.wantCode {
					t.Errorf("code = %q, want %q", ae.Code, tc.wantCode)
				}
				if ae.Status != tc.wantStatus {
					t.Errorf("status = %d, want %d", ae.Status, tc.wantStatus)
				}
				if tc.wantMsgSub != "" && !strings.Contains(ae.Message, tc.wantMsgSub) {
					t.Errorf("message = %q, want it to contain %q", ae.Message, tc.wantMsgSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("Authenticate(%q): unexpected error: %v", tc.header, err)
			}
			if id.User != tc.wantUser {
				t.Errorf("identity = %q, want %q", id.User, tc.wantUser)
			}
		})
	}
}

func TestAuthenticateKubeRBACProxy(t *testing.T) {
	a, err := NewAuthenticator(AuthKubeRBACProxy, "")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("reads the injected identity", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		r.Header.Set("X-Remote-User", "alice")
		r.Header.Add("X-Remote-Group", "dev")
		r.Header.Add("X-Remote-Group", "pg-readers")

		id, err := a.Authenticate(r)
		if err != nil {
			t.Fatal(err)
		}
		if id.User != "alice" {
			t.Errorf("user = %q, want alice", id.User)
		}
		if len(id.Groups) != 2 || id.Groups[0] != "dev" || id.Groups[1] != "pg-readers" {
			t.Errorf("groups = %v, want [dev pg-readers]", id.Groups)
		}
	})

	// A request with no X-Remote-User reached us without going through the
	// proxy (a port-forward around it). Serving it as authenticated would let
	// anyone with cluster network access impersonate the API.
	t.Run("missing X-Remote-User is rejected", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		_, err := a.Authenticate(r)
		var ae *AuthError
		if !asAuthError(err, &ae) {
			t.Fatalf("error = %v, want *AuthError", err)
		}
		if ae.Code != dto.ErrUnauthorized || ae.Status != http.StatusUnauthorized {
			t.Errorf("got %s/%d, want Unauthorized/401", ae.Code, ae.Status)
		}
	})

	t.Run("blank X-Remote-User is rejected", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.Header.Set("X-Remote-User", "  ")
		if _, err := a.Authenticate(r); err == nil {
			t.Fatal("blank X-Remote-User accepted, want rejection")
		}
	})
}

// TestAuthzGroupFor pins the three-way split R8.9's f08b6ac ruling requires:
// the two probes have their own group, /metrics has its own, and everything
// else — including every /debug/pprof/* sub-path, which follows --auth — is
// API.
func TestAuthzGroupFor(t *testing.T) {
	cases := map[string]RouteGroup{
		"/":                    GroupAPI,
		"/index.html":          GroupAPI,
		"/c/prod/pg-primary":   GroupAPI,
		"/api/v1/clusters":     GroupAPI,
		"/api/v1/version":      GroupAPI,
		"/api/v1/insights/x":   GroupAPI, // a Phase 3 route needs no code change
		"/api/":                GroupAPI,
		"/api/v1/clusters/":    GroupAPI,
		"/debug/pprof":         GroupAPI, // R8.9: pprof follows --auth
		"/debug/pprof/":        GroupAPI,
		"/debug/pprof/profile": GroupAPI,
		"/metrics":             GroupMetrics,
		"/healthz":             GroupProbe,
		"/readyz":              GroupProbe,
		"/healthz/":            GroupAPI, // exact-match: a sub-path is not the probe
		"/metrics/":            GroupAPI,
	}
	for path, want := range cases {
		if got := (&Authz{}).GroupFor(path); got != want {
			t.Errorf("GroupFor(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestAuthzPerRouteGroup is R8.8's separate-auth clause as a test: with
// --auth=token and --auth-metrics=none, the API needs the token and the
// metrics surface does not. R8.9's f08b6ac ruling is the same assertion plus
// the two probes, which are open under *any* value of either flag (see
// TestProbesAreAlwaysOpen for the case where they differ).
func TestAuthzPerRouteGroup(t *testing.T) {
	apiAuth, err := NewAuthenticator(AuthToken, "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	metricsAuth, err := NewAuthzMetrics(AuthNone, "")
	if err != nil {
		t.Fatal(err)
	}
	z := NewAuthz(apiAuth, metricsAuth)
	h := z.Middleware(okHandler(t))

	cases := []struct {
		path       string
		header     string
		wantStatus int
	}{
		{path: "/api/v1/clusters", header: "", wantStatus: http.StatusUnauthorized},
		{path: "/api/v1/clusters", header: "Bearer s3cr3t", wantStatus: http.StatusOK},
		{path: "/api/v1/version", header: "", wantStatus: http.StatusUnauthorized},
		{path: "/", header: "", wantStatus: http.StatusUnauthorized},
		{path: "/metrics", header: "", wantStatus: http.StatusOK},
		{path: "/healthz", header: "", wantStatus: http.StatusOK},
		{path: "/readyz", header: "", wantStatus: http.StatusOK},
		// A token presented to a probe route is harmless: the probe group does
		// not consult it, so a Prometheus scrape configured with a stale token
		// still succeeds.
		{path: "/metrics", header: "Bearer stale", wantStatus: http.StatusOK},
	}

	for _, tc := range cases {
		name := tc.path
		if tc.header != "" {
			name += " with token"
		} else {
			name += " anonymous"
		}
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s (%q) = %d, want %d; body %q", tc.path, tc.header, rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestAuthzSwapsAuthenticatorPerPath proves the two configurable groups are
// genuinely different policies rather than one policy applied twice: the same
// anonymous request is 401 on one path and 200 on another.
func TestAuthzSwapsAuthenticatorPerPath(t *testing.T) {
	metricsOnly, _ := NewAuthenticator(AuthToken, "metrics-token")
	z := NewAuthz(nil, metricsOnly) // API group unconfigured → allow-all
	h := z.Middleware(okHandler(t))

	r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("unconfigured API group = %d, want 200 (nil authenticator is allow-all)", rec.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("metrics group with a token policy, anonymous request = %d, want 401", rec.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer metrics-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics group with the right token = %d, want 200", rec.Code)
	}
}

// TestAuthzMiddlewareBuildsHandlersOnce pins the shape of Middleware rather
// than its behaviour: the per-group handlers are constructed at mount time and
// the request path only picks between them. The first version built one
// per request — `z.For(z.GroupFor(path)).Middleware(next).ServeHTTP(w, r)` —
// which allocates a fresh http.HandlerFunc on every request, including on the
// two probe routes kubelet hits forever, where the handler it builds has no
// policy at all and exists only to call next.
func TestAuthzMiddlewareBuildsHandlersOnce(t *testing.T) {
	apiAuth, _ := NewAuthenticator(AuthToken, "s3cr3t")
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	z := NewAuthz(apiAuth, apiAuth)
	h := z.Middleware(noop)
	rec := httptest.NewRecorder()

	// The assertion is a *difference*, not a number: routing a request must cost
	// no more than calling the handler it routes to. A hard alloc count would
	// test strings.Fields inside Authenticate; the difference isolates the
	// middleware, which is the thing that used to build a handler per request.
	//
	// (z, path) -> the handler the middleware picks for it, measured against
	// that handler called directly.
	cases := []struct {
		path   string
		direct http.Handler
		creds  bool
	}{
		// GroupProbe has no policy: the correct handler is `noop` itself, so
		// routing to it must cost exactly what calling noop costs — zero.
		{path: "/readyz", direct: noop},
		{path: "/healthz", direct: noop},
		// GroupMetrics with a token policy: Authenticate allocates on its own
		// (strings.Fields on the header, the byte-slice conversions), and the
		// middleware must add nothing on top of that.
		{path: "/metrics", direct: apiAuth.Middleware(noop), creds: true},
		{path: "/api/v1/clusters", direct: apiAuth.Middleware(noop), creds: true},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			newReq := func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, tc.path, nil)
				if tc.creds {
					r.Header.Set("Authorization", "Bearer s3cr3t")
				}
				return r
			}
			viaMiddleware := testing.AllocsPerRun(20, func() { h.ServeHTTP(rec, newReq()) })
			direct := testing.AllocsPerRun(20, func() { tc.direct.ServeHTTP(rec, newReq()) })
			if viaMiddleware > direct {
				t.Errorf("AllocsPerRun = %v through the middleware, %v calling the group handler directly; routing must not allocate a handler per request", viaMiddleware, direct)
			}
		})
	}
}

// TestProbesAreAlwaysOpen is the regression test for the defect f08b6ac
// rules on. The pre-ruling code put /healthz and /readyz in the same group as
// /metrics, so --auth-metrics=token gated them — and a gated probe is a broken
// pod: kubelet's httpGet sends no Authorization header (the chart mounts the
// token as a file, not as httpGet.httpHeaders), so the probe gets 401, the pod
// never goes Ready, and the Service drops its endpoints. The two probes are
// exempt unconditionally, so this configuration must serve them anonymously
// while /metrics demands the token.
func TestProbesAreAlwaysOpen(t *testing.T) {
	apiAuth, _ := NewAuthenticator(AuthToken, "s3cr3t")
	metricsAuth, _ := NewAuthzMetrics(AuthToken, "metrics-token")
	z := NewAuthz(apiAuth, metricsAuth)
	h := z.Middleware(okHandler(t))

	// /metrics at --auth-metrics=token is gated — this is the setting the old
	// grouping got wrong.
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /metrics anonymous = %d, want 401", rec.Code)
	}

	for _, path := range []string{"/healthz", "/readyz"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s anonymous = %d, want 200 (probes are exempt under any --auth/--auth-metrics)", path, rec.Code)
		}
	}

	// /debug/pprof/* follows --auth, so it is gated here too.
	r = httptest.NewRequest(http.MethodGet, "/debug/pprof/heap", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /debug/pprof/heap anonymous = %d, want 401 (R8.9: pprof follows --auth)", rec.Code)
	}
}

// TestNilAuthzPassesThrough keeps the Phase 0 behaviour: a Config with no Auth
// must not change any existing response.
func TestNilAuthzPassesThrough(t *testing.T) {
	var z *Authz
	h := z.Middleware(okHandler(t))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil Authz = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "/api/v1/clusters" {
		t.Fatalf("body = %q, want the path echoed", body)
	}
}

// TestAuthErrorBodyIsR8_5 checks the 401 body is dto.NewError's shape, not an
// ad-hoc object: R8.5 requires one error shape for every endpoint, and the SPA
// already decodes this type.
func TestAuthErrorBodyIsR8_5(t *testing.T) {
	a, _ := NewAuthenticator(AuthToken, "s3cr3t")
	h := a.Middleware(okHandler(t))

	r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if wa := rec.Header().Get("WWW-Authenticate"); wa == "" {
		t.Error("401 without WWW-Authenticate: an SPA cannot tell \"no token yet\" from \"wrong token\"")
	}

	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not a valid ErrorBody: %v", rec.Body.String(), err)
	}
	if body.Error.Code != dto.ErrUnauthorized {
		t.Errorf("code = %q, want %q", body.Error.Code, dto.ErrUnauthorized)
	}
	if body.Error.Message == "" {
		t.Error("empty error message")
	}
	// R8.5's Details is always present. A nil slice marshals to `null`, which
	// is a different JSON type from `[]` — NewError normalises it, and this
	// asserts the middleware goes through NewError rather than the struct.
	if body.Error.Details == nil {
		t.Error("error.details is null; R8.5 requires it always present (use dto.NewError)")
	}
}

// TestMiddlewareAttachesIdentity is the R12 hook: a handler downstream of auth
// can read who the caller is, which is what Phase 2's R12.4/R12.5 authorization
// needs and the reason Identity is returned rather than discarded.
func TestMiddlewareAttachesIdentity(t *testing.T) {
	a, _ := NewAuthenticator(AuthToken, "s3cr3t")
	h := a.Middleware(okHandler(t))

	r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	r.Header.Set("Authorization", "Bearer s3cr3t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if body := rec.Body.String(); body != "/api/v1/clusters|token" {
		t.Fatalf("body = %q, want the path plus the identity", body)
	}

	// And the negative: a handler reached without auth finds no identity.
	r = httptest.NewRequest(http.MethodGet, "/x", nil)
	if _, ok := IdentityFrom(r); ok {
		t.Error("IdentityFrom on an unauthenticated request = ok, want false")
	}
}

// asAuthError is errors.As behind a name the table tests can read. Using the
// stdlib call (rather than a bare type assertion) means a wrapped AuthError —
// which is what AuthzConfig.Build produces for the "--auth: ..." prefix — is
// still matched.
func asAuthError(err error, target **AuthError) bool {
	return errors.As(err, target)
}

// TestServerMountsAuthPerRouteGroup proves the middleware is actually wired
// into Server.Handler(), not just correct in isolation. It builds a Server
// with --auth=token on the API group and --auth-metrics=none on /metrics —
// R8.8's documented default pair — and drives the real mux.
func TestServerMountsAuthPerRouteGroup(t *testing.T) {
	apiAuth, err := NewAuthenticator(AuthToken, "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Listen: "127.0.0.1:0", Auth: NewAuthz(apiAuth, nil)})
	// /readyz answers 503 until Run() binds a socket (R8.7); the auth tests are
	// about the gate, so mark the server started the way server_test.go does.
	s.readiness.SetListening(true)

	cases := []struct {
		path       string
		header     string
		wantStatus int
	}{
		{path: "/api/v1/version", header: "", wantStatus: http.StatusUnauthorized},
		{path: "/api/v1/version", header: "Bearer s3cr3t", wantStatus: http.StatusOK},
		// A wrong token is 401 (R8.8's "missing/bad token"), not 403 — 403 is
		// R12's "authenticated, but this cluster is not yours to read".
		{path: "/api/v1/version", header: "Bearer nope", wantStatus: http.StatusUnauthorized},
		// /metrics is unconfigured (nil) → allow-all, which is what
		// --auth-metrics=none gives a Prometheus scrape; the two probes are
		// exempt outright (R8.9), so they answer either way.
		{path: "/metrics", header: "", wantStatus: http.StatusOK},
		{path: "/healthz", header: "", wantStatus: http.StatusOK},
		{path: "/readyz", header: "", wantStatus: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.path+" "+tc.header, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, r)
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s (%q) = %d, want %d", tc.path, tc.header, rec.Code, tc.wantStatus)
			}
		})
	}
}

// TestServerWithoutAuthIsUnchanged guards the Phase 0/1b behaviour: a Config
// with no Auth and no CORS must answer exactly as it did before these files
// existed, so mounting them cannot regress server_test.go.
func TestServerWithoutAuthIsUnchanged(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	s.readiness.SetListening(true)
	for _, path := range []string{"/healthz", "/readyz", "/metrics", "/api/v1/version"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d with no Auth configured, want 200", path, rec.Code)
		}
	}
	// POST /healthz must still be 405 (R8.5's MethodNotAllowed), proving the
	// middleware did not swallow the mux's verb handling.
	r := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", rec.Code)
	}
}

// TestAuthzConfigBuild covers the flag surface as a unit: R8.8's two defaults
// (--auth=token, --auth-metrics=none) and the rule that the token file is only
// read when a group needs it.
func TestAuthzConfigBuild(t *testing.T) {
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokFile, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("empty config takes R8.8's defaults", func(t *testing.T) {
		// An empty AuthzConfig with a readable token file is exactly
		// `pgsi serve --auth-token-file=…`: API group token, metrics group none.
		z, err := AuthzConfig{TokenFile: tokFile}.Build()
		if err != nil {
			t.Fatal(err)
		}
		if got := z.API.Mode(); got != AuthToken {
			t.Errorf("--auth default = %q, want token (R8.8)", got)
		}
		if got := z.Metrics.Mode(); got != AuthNone {
			t.Errorf("--auth-metrics default = %q, want none (R8.9)", got)
		}
	})

	t.Run("none mode does not require the token file to exist", func(t *testing.T) {
		// The default --auth-token-file points at /etc/pgsi/auth/token, which
		// does not exist on a laptop. `--auth=none --auth-metrics=none` must
		// still start, or lab would have to unset two flags to disable one.
		z, err := AuthzConfig{Mode: AuthNone, MetricsMode: AuthNone, TokenFile: filepath.Join(dir, "absent")}.Build()
		if err != nil {
			t.Fatalf("Build(--auth=none) with a missing token file: %v", err)
		}
		if z.API.Mode() != AuthNone {
			t.Fatalf("mode = %q, want none", z.API.Mode())
		}
	})

	t.Run("token mode reads the file", func(t *testing.T) {
		z, err := AuthzConfig{Mode: AuthToken, MetricsMode: AuthNone, TokenFile: tokFile}.Build()
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		r.Header.Set("Authorization", "Bearer file-token")
		if _, err := z.API.Authenticate(r); err != nil {
			t.Fatalf("token from --auth-token-file not accepted: %v", err)
		}
	})

	t.Run("--auth-token wins over the file", func(t *testing.T) {
		z, err := AuthzConfig{Mode: AuthToken, MetricsMode: AuthNone, Token: "inline", TokenFile: tokFile}.Build()
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		r.Header.Set("Authorization", "Bearer inline")
		if _, err := z.API.Authenticate(r); err != nil {
			t.Fatalf("--auth-token not honoured: %v", err)
		}
	})

	// R8.8's "--auth-metrics=none so Prometheus need not hold a token" is about
	// the *secret*, not only the mode. Before --auth-metrics-token existed,
	// `--auth=token --auth-metrics=token` handed the identical string to both
	// groups, so the credential a scrape job holds was the credential every user
	// authenticates with — and rotating it logged everyone out.
	t.Run("--auth-metrics-token gives the metrics group its own secret", func(t *testing.T) {
		z, err := AuthzConfig{
			Mode: AuthToken, MetricsMode: AuthToken,
			Token: "api-token", MetricsToken: "metrics-token",
		}.Build()
		if err != nil {
			t.Fatal(err)
		}

		newReq := func(tok string) *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			r.Header.Set("Authorization", "Bearer "+tok)
			return r
		}
		if _, err := z.Metrics.Authenticate(newReq("metrics-token")); err != nil {
			t.Errorf("metrics group rejected its own token: %v", err)
		}
		if _, err := z.Metrics.Authenticate(newReq("api-token")); err == nil {
			t.Error("metrics group accepted the API token; the two credentials must be distinct")
		}
		// And the API group must not have picked up the metrics token.
		r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		r.Header.Set("Authorization", "Bearer metrics-token")
		if _, err := z.API.Authenticate(r); err == nil {
			t.Error("API group accepted the metrics token; --auth-token is the API's credential")
		}
	})

	t.Run("metrics token falls back to --auth-token when unset", func(t *testing.T) {
		// The back-compat half of the ruling: an invocation that predates the
		// new flags behaves exactly as before, sharing the API's credential.
		z, err := AuthzConfig{Mode: AuthToken, MetricsMode: AuthToken, Token: "shared"}.Build()
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		r.Header.Set("Authorization", "Bearer shared")
		if _, err := z.Metrics.Authenticate(r); err != nil {
			t.Errorf("metrics group did not fall back to --auth-token: %v", err)
		}
	})

	t.Run("--auth-metrics-token-file is read for the metrics group", func(t *testing.T) {
		metricsTokFile := filepath.Join(t.TempDir(), "metrics-token")
		if err := os.WriteFile(metricsTokFile, []byte("metrics-file-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// The file wins over the TokenFile fallback, and the API group keeps
		// reading its own pair — two Secret mounts, two credentials, one flag
		// pair each.
		z, err := AuthzConfig{
			Mode: AuthToken, MetricsMode: AuthToken,
			Token: "api-token", TokenFile: tokFile,
			MetricsTokenFile: metricsTokFile,
		}.Build()
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		r.Header.Set("Authorization", "Bearer metrics-file-token")
		if _, err := z.Metrics.Authenticate(r); err != nil {
			t.Errorf("--auth-metrics-token-file not honoured: %v", err)
		}
		r = httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		r.Header.Set("Authorization", "Bearer api-token")
		if _, err := z.API.Authenticate(r); err != nil {
			t.Errorf("--auth-token not honoured alongside the metrics file: %v", err)
		}
	})

	// The metrics file is read only when the metrics group needs a token, for
	// the same reason the API's file is: a default path that does not exist must
	// not break a run that disabled auth.
	t.Run("metrics token file is not read when metrics mode is none", func(t *testing.T) {
		_, err := AuthzConfig{
			Mode: AuthNone, MetricsMode: AuthNone,
			MetricsTokenFile: filepath.Join(t.TempDir(), "absent"),
		}.Build()
		if err != nil {
			t.Fatalf("Build(--auth-metrics=none) with a missing metrics token file: %v", err)
		}
	})

	t.Run("bad mode names the flag", func(t *testing.T) {
		_, err := AuthzConfig{Mode: AuthMode("bogus"), Token: "x"}.Build()
		if err == nil {
			t.Fatal("Build(Mode=bogus) = nil error, want error")
		}
		if !strings.Contains(err.Error(), "--auth:") {
			t.Errorf("error %q should name --auth", err)
		}

		_, err = AuthzConfig{Mode: AuthNone, MetricsMode: AuthMode("bogus"), Token: "x"}.Build()
		if err == nil {
			t.Fatal("Build(MetricsMode=bogus) = nil error, want error")
		}
		if !strings.Contains(err.Error(), "--auth-metrics:") {
			t.Errorf("error %q should name --auth-metrics", err)
		}
	})

	t.Run("token mode with no token anywhere fails", func(t *testing.T) {
		_, err := AuthzConfig{Mode: AuthToken, MetricsMode: AuthNone}.Build()
		if err == nil {
			t.Fatal("Build(token, no token, no file) = nil error, want the fail-closed error")
		}
	})
}
