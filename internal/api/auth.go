// Authentication (R8.8) and the per-route-group policy it forces (R8.9).
//
// R8.8 gives pgsi three modes — none | token | kube-rbac-proxy — plus one knob
// that changes the shape of the whole design: --auth-metrics=none, so
// Prometheus can scrape /metrics without holding a token while / and
// /api/v1/* do require one. A single global middleware on the mux cannot
// express "the token is required here and not there", so this file models auth
// as a *table of route groups*: Authz holds one Authenticator per group and
// picks the right one by request path. R8.9's "…/healthz never require auth
// when --auth-metrics=none" is then a row in that table rather than a
// special case bolted onto the handler.
//
// R8.9 was rewritten in f08b6ac and the table follows it. The old wording
// named /metrics and /healthz and left /readyz and /debug/pprof/* unaddressed;
// the ruling is now three clauses, and the shape of Authz is exactly that:
//
//	/healthz, /readyz  → always open, no policy consulted, under any --auth
//	/metrics           → --auth-metrics
//	everything else    → --auth  (the SPA, /api/v1/*, and /debug/pprof/*,
//	                           which R8.9 rules follows --auth because it is
//	                           introspection rather than a probe)
//
// The probes are exempt unconditionally rather than via --auth-metrics
// because a gated probe is a broken pod, not a preference: R11.2's probes are
// httpGet (distroless has no shell, so an exec probe cannot run), the chart
// mounts the token as a *file* and not as httpGet.httpHeaders, and kubelet
// sends no Authorization header — so at --auth-metrics=token a gated /readyz
// answers 401 to the kubelet, the pod never goes Ready, and the Service drops
// its endpoints. TestProbesAreAlwaysOpen pins that case.
//
// The middleware is deliberately route-agnostic — it takes an http.Handler and
// returns an http.Handler — because route registration belongs to server.go
// (A1's Phase 1a). Mounting this is one line there; the policy, the token
// comparison and the error shape all live here and are tested here without
// needing the real mux.

package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// AuthMode is a value of --auth or --auth-metrics (R8.8).
type AuthMode string

const (
	// AuthNone serves requests without checking anything. R8.8 scopes it to
	// "lab"; --auth-metrics=none uses it for the scrape surface permanently.
	AuthNone AuthMode = "none"
	// AuthToken requires `Authorization: Bearer <token>`, the token coming
	// from a Secret (R8.8's default).
	AuthToken AuthMode = "token"
	// AuthKubeRBACProxy trusts the headers kube-rbac-proxy injects after it
	// authenticates the caller against the Kubernetes API.
	AuthKubeRBACProxy AuthMode = "kube-rbac-proxy"
)

// DefaultAuthMode is R8.8's documented default: token, not none. A binary that
// silently served unauthenticated because a flag was forgotten is the failure
// mode that clause exists to prevent.
const DefaultAuthMode = AuthToken

// TokenFile is the in-pod path the chart mounts the token Secret at
// (chart/templates/deployment.yaml mounts it as /etc/pgsi/auth). Exported so
// main.go's flag default and the chart cannot drift apart.
const TokenFile = "/etc/pgsi/auth/token"

// Header names kube-rbac-proxy sets on every request it authenticates. They
// are the only thing AuthKubeRBACProxy reads.
const (
	headerRemoteUser  = "X-Remote-User"
	headerRemoteGroup = "X-Remote-Group"
)

// ParseAuthMode validates a flag value, mapping "" to DefaultAuthMode so a
// caller can pass an unset string through without a second branch.
func ParseAuthMode(v string) (AuthMode, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return DefaultAuthMode, nil
	}
	switch m := AuthMode(v); m {
	case AuthNone, AuthToken, AuthKubeRBACProxy:
		return m, nil
	default:
		return "", fmt.Errorf("unknown auth mode %q (want none|token|kube-rbac-proxy)", v)
	}
}

// Identity is the authenticated caller the middleware attaches to the request
// context. R12's authorization work (Phase 2's R12.4/R12.5) reads it to decide
// what the caller may see; R8.8's token mode has exactly one principal, so the
// value there is constant, which is also why R10.6's per-user saved views are
// deferred with OIDC rather than built on this type.
type Identity struct {
	User   string
	Groups []string
}

type contextKey int

const identityContextKey contextKey = iota

// WithIdentity returns a context carrying id. Exported so a handler or a
// Phase 2 authorization layer can construct the same shape the middleware
// constructs, and so the key is never redeclared elsewhere (two `type
// contextKey int` declarations in one package compile, but two keys holding
// the same value silently shadow each other).
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityContextKey, id)
}

// IdentityFrom reads the Identity the auth middleware attached to r, reporting
// false when the request never passed through an authenticator. It takes the
// request rather than the context so callers cannot pass a context built
// without WithContext.
func IdentityFrom(r *http.Request) (Identity, bool) {
	id, ok := r.Context().Value(identityContextKey).(Identity)
	return id, ok
}

// AuthError is a failed authentication. It carries both the R8.5 code and the
// HTTP status so the middleware can render it without a second switch, and so
// the same enum serves whole-request failures (R8.5) and per-namespace ones
// (R8.4's meta.failed[].code).
type AuthError struct {
	Code    string // one of the dto.Err* constants
	Message string
	Status  int
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Authenticator enforces the policy of one route group. A nil *Authenticator
// is valid and allows everything, which is what an unconfigured Config
// collapses to so Phase 0's tests keep passing while the flags are unset.
type Authenticator struct {
	mode  AuthMode
	token string
}

// NewAuthenticator builds the policy for one mode. It fails closed: token mode
// with an empty token is an error rather than a server that accepts the empty
// bearer string, because "the Secret exists but is empty" is otherwise
// indistinguishable from "auth is on" in a running pod.
func NewAuthenticator(mode AuthMode, token string) (*Authenticator, error) {
	switch mode {
	case AuthNone:
		return &Authenticator{mode: AuthNone}, nil
	case AuthToken:
		if token == "" {
			return nil, errors.New("auth mode \"token\" requires a token (--auth-token or --auth-token-file); refusing to start rather than accept an empty bearer token")
		}
		return &Authenticator{mode: AuthToken, token: token}, nil
	case AuthKubeRBACProxy:
		return &Authenticator{mode: AuthKubeRBACProxy}, nil
	default:
		return nil, fmt.Errorf("unknown auth mode %q", mode)
	}
}

// LoadToken reads a bearer token from a file, tolerating the trailing newline
// every writer (and `kubectl create secret generic --from-file`) leaves behind.
// A whitespace-only file is an error, not an empty token.
func LoadToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read auth token file %s: %w", path, err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("auth token file %s is empty", path)
	}
	return tok, nil
}

// Mode reports the configured mode; used by the router and by tests.
func (a *Authenticator) Mode() AuthMode {
	if a == nil {
		return AuthNone
	}
	return a.mode
}

// Authenticate checks r against the policy and returns the caller it
// identifies. The token comparison is constant-time (crypto/subtle): a
// timing-leaky compare on a bearer token is the classic side channel, and
// strings.EqualFold would also have accepted a case-changed token.
func (a *Authenticator) Authenticate(r *http.Request) (Identity, error) {
	if a == nil || a.mode == AuthNone {
		return Identity{User: "anonymous"}, nil
	}

	switch a.mode {
	case AuthKubeRBACProxy:
		// kube-rbac-proxy has already authenticated the caller against the
		// kube API and stamped the identity on the request. Our half is to
		// refuse requests with no identity — a request that bypassed the proxy
		// (a port-forward around it, a misconfigured Service) must not be
		// served as if it were authenticated.
		user := strings.TrimSpace(r.Header.Get(headerRemoteUser))
		if user == "" {
			return Identity{}, &AuthError{
				Code:    dto.ErrUnauthorized,
				Message: "missing X-Remote-User: request did not pass through kube-rbac-proxy",
				Status:  http.StatusUnauthorized,
			}
		}
		return Identity{User: user, Groups: r.Header.Values(headerRemoteGroup)}, nil

	case AuthToken:
		// Split on whitespace, not on a single literal space. RFC 6454/7235
		// allow optional whitespace between the scheme and the credential, and
		// `strings.Cut(h, " ")` mis-reports two cases that follow from it:
		// "Bearer" with no space yields ok=false and answers "missing
		// Authorization header" when one is plainly present, and "Bearer  tok"
		// yields the credential " tok" — a two-character string that can never
		// match, so the caller is told their token is wrong when the real
		// problem is the separator. strings.Fields handles both, and a header
		// with a scheme and no credential falls through to the comparison
		// below, where it fails against the non-empty configured token.
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) == 0 {
			return Identity{}, &AuthError{
				Code:    dto.ErrUnauthorized,
				Message: "missing Authorization header",
				Status:  http.StatusUnauthorized,
			}
		}
		scheme, provided := fields[0], ""
		if len(fields) > 1 {
			provided = fields[1]
		}
		if !strings.EqualFold(scheme, "bearer") {
			return Identity{}, &AuthError{
				Code:    dto.ErrUnauthorized,
				Message: fmt.Sprintf("unsupported Authorization scheme %q (want Bearer)", scheme),
				Status:  http.StatusUnauthorized,
			}
		}
		// A *wrong* token is 401 Unauthorized, not 403 Forbidden. dto's enum
		// is the authority on the split and it says so in its own comments:
		// `ErrUnauthorized // missing/bad token (R8.8)` and `ErrForbidden
		// // token lacks scope for the object (R12)`. Answering 403 here
		// collapses "your credential is wrong" into the code Phase 2's
		// R12.4/R12.5 use for "authenticated, but not allowed to see this
		// cluster", and a client that branches on the two — an SPA that
		// re-prompts on 401 and shows an access-denied page on 403 — re-prompts
		// forever. 403 is R12's to emit; auth.go's whole vocabulary is 401.
		if subtle.ConstantTimeCompare([]byte(provided), []byte(a.token)) != 1 {
			return Identity{}, &AuthError{
				Code:    dto.ErrUnauthorized,
				Message: "invalid bearer token",
				Status:  http.StatusUnauthorized,
			}
		}
		return Identity{User: "token"}, nil
	}

	return Identity{}, &AuthError{Code: dto.ErrInternal, Message: "unreachable auth mode", Status: http.StatusInternalServerError}
}

// Middleware returns the http.Handler form of this policy.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	if a == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Authenticate(r)
		if err != nil {
			var ae *AuthError
			if !errors.As(err, &ae) {
				ae = &AuthError{Code: dto.ErrInternal, Message: err.Error(), Status: http.StatusInternalServerError}
			}
			writeAuthError(w, ae)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// writeAuthError renders an auth failure as an R8.5 ErrorBody. Every 401
// carries WWW-Authenticate — the header is the scheme advertisement, so it
// belongs on "no token" and "wrong token" alike, and the two are distinguished
// by the message ("missing Authorization header" vs "invalid bearer token")
// rather than by the status. Status 403 is deliberately never emitted from
// here: dto's enum reserves it for R12's "authenticated, not allowed to see
// this object", and R8.8's vocabulary is entirely 401. The body is
// dto.NewError rather than an ad-hoc object so every error the API emits —
// here, on a route, or in meta.failed — is the same shape (R8.5).
func writeAuthError(w http.ResponseWriter, ae *AuthError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if ae.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="pgsi"`)
	}
	w.WriteHeader(ae.Status)
	_ = json.NewEncoder(w).Encode(dto.NewError(ae.Code, ae.Message))
}

// RouteGroup is one row of the auth table (R8.8, as rewritten for /readyz in
// f08b6ac). R8.8's "--auth-metrics=none so Prometheus need not hold a token"
// is only implementable if the surface is partitioned into groups with
// independent modes; the ruling names three, and they are these.
type RouteGroup string

const (
	// GroupAPI is the SPA at / plus every /api/v1/* route — and, per R8.9's
	// ruling, /debug/pprof/*, which is introspection rather than a probe and
	// so follows --auth. Adding a Phase 3 endpoint needs no change here: the
	// switch below defaults everything to this group.
	GroupAPI RouteGroup = "api"
	// GroupMetrics is /metrics — the only surface --auth-metrics governs.
	GroupMetrics RouteGroup = "metrics"
	// GroupProbe is /healthz and /readyz. It has no Authenticator: R8.9 makes
	// the two probes exempt *unconditionally*, under any value of --auth or
	// --auth-metrics, because a kubelet httpGet probe cannot present a bearer
	// token (the chart mounts the Secret as a file, not as
	// httpGet.httpHeaders) and a 401 on a probe is a pod that never goes
	// Ready, not a policy the operator chose.
	GroupProbe RouteGroup = "probe"
)

// Authz is the per-route-group auth policy: one Authenticator per configurable
// group, and the path→group mapping that picks one. This is the shape R8.8's
// separate-auth clause implies and the reason R8.9 needs no extra work.
type Authz struct {
	// API governs GroupAPI (--auth).
	API *Authenticator
	// Metrics governs GroupMetrics (--auth-metrics).
	Metrics *Authenticator
	// Probe is deliberately absent: GroupProbe has no policy to hold, so
	// For() returns nil for it and the middleware passes through. Modelling
	// it as a field someone could set to token is exactly the mistake the
	// f08b6ac ruling removes.
}

// NewAuthz wires the two configurable groups. Either may be nil (allow-all);
// the probe group is not a parameter because it is not configurable.
func NewAuthz(api, metrics *Authenticator) *Authz {
	return &Authz{API: api, Metrics: metrics}
}

// GroupFor classifies a request path. The two probes and /metrics are named;
// everything else — the SPA, every /api/v1/* route present or future, and
// /debug/pprof/* with its sub-paths — is API, which is what lets R8.9's
// "/debug/pprof/* follows --auth" be satisfied by the default branch instead
// of a prefix match.
func (z *Authz) GroupFor(p string) RouteGroup {
	switch p {
	case "/healthz", "/readyz":
		return GroupProbe
	case "/metrics":
		return GroupMetrics
	default:
		return GroupAPI
	}
}

// For returns the authenticator governing g. GroupProbe always returns nil,
// which the middleware treats as allow-all — the code form of "exempt
// unconditionally".
func (z *Authz) For(g RouteGroup) *Authenticator {
	if z == nil {
		return nil
	}
	switch g {
	case GroupMetrics:
		return z.Metrics
	case GroupProbe:
		return nil
	default:
		return z.API
	}
}

// Middleware wraps next with the per-group policy. One line in server.go's
// Handler() mounts the whole R8.8 surface: the mux stays route-agnostic and
// every policy decision — the always-open probes, --auth-metrics on /metrics,
// --auth everywhere else — happens here.
func (z *Authz) Middleware(next http.Handler) http.Handler {
	if z == nil {
		return next
	}
	// The three per-group handlers are built here, once, and the request path
	// only picks between them. Building them per request — the obvious
	// `z.For(z.GroupFor(r.URL.Path)).Middleware(next).ServeHTTP(w, r)` —
	// allocates a fresh http.HandlerFunc for every request that arrives, and
	// for the probe group (which has no policy at all) it allocates one whose
	// only job is to call `next`, on the two routes kubelet hits every few
	// seconds forever. The map is built at mount time, so the per-request work
	// is one string switch and one map read.
	handlers := map[RouteGroup]http.Handler{
		// For() returns a nil *Authenticator for an unconfigured group (and
		// always for GroupProbe), and (*Authenticator)(nil).Middleware returns
		// next unchanged — so an unset group is an allow-all pass-through, not
		// a special case here.
		GroupAPI:     z.API.Middleware(next),
		GroupMetrics: z.Metrics.Middleware(next),
		GroupProbe:   next,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := handlers[z.GroupFor(r.URL.Path)]
		if !ok || h == nil {
			h = next
		}
		h.ServeHTTP(w, r)
	})
}

// AuthzConfig is R8.8's flag surface as a struct, so cmd/pgsi owns the flags
// and this package owns the policy built from them.
type AuthzConfig struct {
	// Mode is --auth: the policy for GroupAPI.
	Mode AuthMode
	// MetricsMode is --auth-metrics: the policy for /metrics alone (R8.9's
	// ruling narrowed it to that — the two probes are exempt unconditionally
	// and /debug/pprof/* follows Mode). R8.8's default here is none so a
	// Prometheus scrape needs no token.
	MetricsMode AuthMode
	// Token is --auth-token, the literal bearer value for GroupAPI.
	Token string
	// TokenFile is --auth-token-file, read when Token is empty. The chart
	// mounts the Secret here, so production sets this and not Token.
	TokenFile string
	// MetricsToken is --auth-metrics-token: the bearer value for /metrics
	// alone. R8.8's parenthetical — "--auth-metrics=none so Prometheus need
	// not hold a token" — is about the *secret*, not only the mode: with one
	// shared Token field, `--auth=token --auth-metrics=token` forces the
	// scrape job to hold the API's token, so the credential that reads every
	// cluster's data is the same one a human uses. Giving the metrics group
	// its own value lets the operator rotate or revoke the scrape credential
	// without logging every user out. Empty falls back to Token/TokenFile, so
	// an invocation that predates the flag behaves exactly as before.
	MetricsToken string
	// MetricsTokenFile is --auth-metrics-token-file, read when MetricsToken is
	// empty; falls back to TokenFile when both are unset.
	MetricsTokenFile string
}

// Build resolves the two credentials and constructs the Authz table.
//
// A token file is read only when a group actually needs a token. Reading it
// unconditionally would make `--auth=none` fail on a machine where the default
// --auth-token-file path does not exist, so lab and dev would have to unset the
// path as well as the mode to run the binary — two knobs where one should do.
// The metrics group gets the same treatment with its own flag pair
// (--auth-metrics-token[-file]), falling back to the API's.
func (c AuthzConfig) Build() (*Authz, error) {
	// The two groups have different empty-string defaults — --auth defaults to
	// token (R8.8) and --auth-metrics to none (R8.9) — so the defaults are
	// resolved here rather than by ParseAuthMode, which can only have one.
	mode, err := ParseAuthMode(string(c.Mode))
	if err != nil {
		return nil, fmt.Errorf("--auth: %w", err)
	}
	metricsMode := AuthMode(AuthNone)
	if c.MetricsMode != "" {
		metricsMode, err = ParseAuthMode(string(c.MetricsMode))
		if err != nil {
			return nil, fmt.Errorf("--auth-metrics: %w", err)
		}
	}

	// Each group's credential is resolved separately, each file read gated on
	// the group that needs it, and the metrics group falls back to the API's
	// pair when its own flags are unset.
	token := c.Token
	if mode == AuthToken {
		t, err := resolveToken(c.Token, c.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("--auth: %w", err)
		}
		token = t
	}

	// The metrics group's credential: --auth-metrics-token[-file] when set,
	// the API's pair as the fallback. The fallback is what keeps an existing
	// `--auth=token --auth-metrics=token` invocation working unchanged — it
	// just no longer *has* to share the API's secret.
	//
	// The whole block is gated on the metrics group actually needing a token,
	// the same rule the API group applies to its own file: a chart that leaves
	// --auth-metrics-token-file pointing at a path while running
	// --auth-metrics=none must still start, or disabling one policy needs two
	// flags unset.
	metricsToken := ""
	if metricsMode == AuthToken {
		metricsToken = c.MetricsToken
		if metricsToken == "" {
			switch {
			case c.MetricsTokenFile != "":
				t, err := LoadToken(c.MetricsTokenFile)
				if err != nil {
					return nil, fmt.Errorf("--auth-metrics: %w", err)
				}
				metricsToken = t
			case mode == AuthToken:
				// Already resolved above (and gated on the same mode).
				metricsToken = token
			default:
				// Metrics needs a token, the API does not, and no
				// metrics-specific credential was given: read the shared
				// --auth-token[-file].
				t, err := resolveToken(c.Token, c.TokenFile)
				if err != nil {
					return nil, fmt.Errorf("--auth-metrics: %w", err)
				}
				metricsToken = t
			}
		}
	}

	apiAuth, err := NewAuthenticator(mode, token)
	if err != nil {
		return nil, fmt.Errorf("--auth: %w", err)
	}
	metricsAuth, err := NewAuthzMetrics(metricsMode, metricsToken)
	if err != nil {
		return nil, fmt.Errorf("--auth-metrics: %w", err)
	}
	return NewAuthz(apiAuth, metricsAuth), nil
}

// resolveToken picks a literal token over a file, and returns "" when neither
// is set. It never reads a file the caller did not name, so gating a call on
// "this group needs a token" is enough to make an unconfigured group's
// nonexistent default path harmless.
func resolveToken(value, file string) (string, error) {
	if value != "" {
		return value, nil
	}
	if file == "" {
		return "", nil
	}
	return LoadToken(file)
}

// NewAuthzMetrics builds the /metrics-group authenticator (--auth-metrics). It
// exists as its own constructor because R8.8's default for that group is none
// while the default for the API group is token; routing both through
// NewAuthenticator with one shared default is how the two would drift.
//
// It does NOT cover /healthz and /readyz: R8.9's f08b6ac ruling exempts those
// unconditionally, so they have no authenticator to construct (see Authz).
func NewAuthzMetrics(mode AuthMode, token string) (*Authenticator, error) {
	if mode == "" {
		mode = AuthNone
	}
	return NewAuthenticator(mode, token)
}
