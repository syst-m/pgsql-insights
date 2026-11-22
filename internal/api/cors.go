// CORS (R8.9).
//
// R8.9 says "CORS configurable" in one clause, which hides the only real
// design decision here: what happens when nothing is configured. pgsi serves
// the SPA from the same binary as the API (R8.6), so the normal deployment has
// a same-origin frontend and needs no CORS at all. Defaulting to `Access-Control-Allow-Origin: *`
// would therefore open the API to every origin on the network for the benefit
// of nobody — and with R8.8's default being a bearer token, `*` is also the
// setting most likely to be paired with `AllowCredentials`, which RFC 6454
// forbids. So an empty configuration means "no CORS headers, same-origin
// only", and CORS is opt-in by naming origins.
//
// Like auth.go this is standalone middleware with its own tests: mounting it
// is one line in server.go, and it goes *outside* the auth middleware because
// a browser preflight carries no Authorization header — putting auth first
// would answer every OPTIONS with 401 and the real request would never be
// issued.

package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CORSConfig is the configurable half of R8.9. The flag surface in cmd/pgsi is
// --cors-allow-origin, --cors-allow-credentials and --cors-max-age; the
// method/header lists are fields so a chart value or config file can widen
// them without a code change, and so the preflight answer is derived from one
// place instead of being duplicated per route.
type CORSConfig struct {
	// AllowedOrigins is the allow-list. Empty disables CORS entirely (the
	// default, and correct for the same-origin deployment R8.6 describes).
	// The single value "*" allows any origin.
	AllowedOrigins []string
	// AllowedMethods defaults to the verbs the API actually registers
	// (R8.1's catalogue is GET-only today; the write verbs R8.2's saved views
	// need are listed so a Phase 3 addition does not need a config change).
	AllowedMethods []string
	// AllowedHeaders are the request headers permitted by a preflight.
	AllowedHeaders []string
	// ExposedHeaders are the response headers a browser may read. Without
	// this, an SPA cannot read X-Request-Id or the R8.10 pagination cursor off
	// a cross-origin response.
	ExposedHeaders []string
	// AllowCredentials sends Access-Control-Allow-Credentials. It is
	// incompatible with a "*" origin (RFC 6454), which this handles by
	// echoing the request origin instead of sending "*".
	AllowCredentials bool
	// MaxAge is how long a browser may cache the preflight. Zero means 5m.
	MaxAge time.Duration
}

// DefaultCORSConfig is the shape used when CORS is enabled without further
// configuration: any method the API serves, the two headers the API itself
// defines plus Authorization (which a credentialed SPA needs), and the
// response headers worth reading.
func DefaultCORSConfig() *CORSConfig {
	return &CORSConfig{
		AllowedMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders: []string{"Authorization", "Content-Type", "X-Requested-With"},
		ExposedHeaders: []string{"Content-Length", "X-Request-Id"},
		MaxAge:         5 * time.Minute,
	}
}

// ParseCORSAllowOrigin parses the --cors-allow-origin value: a comma-separated
// list. Empty entries are dropped and duplicates removed so
// `--cors-allow-origin=a,a,` and `--cors-allow-origin=a` configure the same
// thing, and a value with surrounding whitespace ("https://a, https://b")
// still works — the chart renders these from a values file where a stray space
// after a comma is normal.
func ParseCORSAllowOrigin(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var (
		out  []string
		seen = map[string]bool{}
	)
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

// withDefaults fills unset fields from DefaultCORSConfig while preserving
// AllowedOrigins and the two non-list settings.
func (c *CORSConfig) withDefaults() *CORSConfig {
	d := DefaultCORSConfig()
	out := *c
	if len(out.AllowedMethods) == 0 {
		out.AllowedMethods = d.AllowedMethods
	}
	if len(out.AllowedHeaders) == 0 {
		out.AllowedHeaders = d.AllowedHeaders
	}
	if len(out.ExposedHeaders) == 0 {
		out.ExposedHeaders = d.ExposedHeaders
	}
	if out.MaxAge <= 0 {
		out.MaxAge = d.MaxAge
	}
	return &out
}

// allowed reports whether origin is on the allow-list. Matching is exact and
// case-sensitive on the scheme/host (RFC 6454 origins are ASCII and
// case-sensitive below the host), with "*" as the wildcard.
func (c *CORSConfig) allowed(origin string) bool {
	for _, o := range c.AllowedOrigins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

// Middleware returns the CORS handler. A nil config, or one with no allowed
// origins, returns next untouched so a same-origin deployment pays nothing and
// gets no headers it did not ask for.
func (c *CORSConfig) Middleware(next http.Handler) http.Handler {
	if c == nil || len(c.AllowedOrigins) == 0 {
		return next
	}
	h := c.withDefaults()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Not a CORS request: curl, kubelet probes, Prometheus and every
			// other non-browser client. They must not see CORS headers, and
			// the request must not be rejected for lacking an Origin.
			next.ServeHTTP(w, r)
			return
		}
		if !h.allowed(origin) {
			// Serve it, but without ACAO: the browser is the enforcement point
			// and it will block the response. Answering 403 here would turn a
			// disallowed origin into an application error the SPA has to
			// distinguish from a real failure.
			next.ServeHTTP(w, r)
			return
		}

		// Vary: Origin goes on every CORS response. Without it a shared cache
		// (an ingress with a cache filter, a CDN in front of R11.5's ingress)
		// can serve a response cached for origin A to origin B, which the
		// browser reads as "A is allowed" for a request B made.
		w.Header().Add("Vary", "Origin")

		// The header value: "*" only when credentials are off. With
		// AllowCredentials the spec forbids "*" (a wildcard credentialed
		// response lets any origin read the body with the user's cookies), so
		// echo the specific origin we just validated.
		acao := origin
		if !h.AllowCredentials {
			acao = "*"
		}

		if isPreflight(r) {
			w.Header().Set("Access-Control-Allow-Origin", acao)
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(h.AllowedMethods, ", "))
			if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
				w.Header().Set("Access-Control-Allow-Headers", h.mergeRequestedHeaders(reqHeaders))
			} else {
				w.Header().Set("Access-Control-Allow-Headers", strings.Join(h.AllowedHeaders, ", "))
			}
			if h.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Max-Age", strconv.FormatInt(int64(h.MaxAge.Seconds()), 10))
			// A preflight has no body; 204 says so without a Content-Length.
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", acao)
		if h.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if len(h.ExposedHeaders) > 0 {
			w.Header().Set("Access-Control-Expose-Headers", strings.Join(h.ExposedHeaders, ", "))
		}
		next.ServeHTTP(w, r)
	})
}

// isPreflight identifies a CORS preflight: OPTIONS plus the header only a
// preflight sends. A bare OPTIONS (an API server answering OPTIONS on a
// route) is not one and is left to the mux, which answers 405 per R8.5's
// MethodNotAllowed.
func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

// mergeRequestedHeaders answers the preflight with the union of the configured
// allow-list and the headers the client actually asked for. Answering with
// only the configured list means a client requesting a header we forgot to
// list gets a hard preflight failure; the API has no header-sensitive
// semantics to protect here, so the permissive union is the cheaper failure
// mode and keeps R8.2's future endpoints from needing a config change.
func (c *CORSConfig) mergeRequestedHeaders(requested string) string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		key := strings.ToLower(v)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, v)
	}
	for _, h := range c.AllowedHeaders {
		add(h)
	}
	for _, h := range strings.Split(requested, ",") {
		add(h)
	}
	return strings.Join(out, ", ")
}
