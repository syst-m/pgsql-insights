// Tests for R8.9 (CORS configurable).

package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseCORSAllowOrigin(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: "   ", want: nil},
		{in: "https://a.example", want: []string{"https://a.example"}},
		// The chart renders this list from a values file, where a space after
		// a comma is normal.
		{in: "https://a, https://b", want: []string{"https://a", "https://b"}},
		// Duplicates and empty entries collapse so `a,a,` and `a` are the same
		// config — otherwise a values file with a trailing comma silently
		// allows an empty-string origin.
		{in: "https://a,https://a,", want: []string{"https://a"}},
		{in: "https://a,,https://b", want: []string{"https://a", "https://b"}},
		{in: "*", want: []string{"*"}},
		{in: "*,https://a", want: []string{"*", "https://a"}},
		// Origin matching is case-sensitive below the host, so the parser must
		// not lowercase the values — only trim them.
		{in: "https://A.example", want: []string{"https://A.example"}},
	}
	for _, tc := range cases {
		t.Run("ParseCORSAllowOrigin("+tc.in+")", func(t *testing.T) {
			got := ParseCORSAllowOrigin(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseCORSAllowOrigin(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ParseCORSAllowOrigin(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

// TestCORSDisabledIsPassThrough is the secure default R8.9 implies: pgsi
// serves the SPA from the same binary as the API (R8.6), so an unconfigured
// deployment needs no CORS headers and must not get `*` for free.
func TestCORSDisabledIsPassThrough(t *testing.T) {
	h := okHandler(t)

	var nilCfg *CORSConfig
	if got := nilCfg.Middleware(h); !sameHandler(got, h) {
		t.Fatal("nil CORSConfig wrapped the handler instead of returning it unchanged")
	}

	empty := &CORSConfig{}
	if got := empty.Middleware(h); !sameHandler(got, h) {
		t.Fatal("empty AllowedOrigins wrapped the handler; an empty list must mean no CORS at all")
	}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	r.Header.Set("Origin", "https://elsewhere.example")
	empty.Middleware(h).ServeHTTP(rec, r)
	if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Errorf("Access-Control-Allow-Origin = %q on an unconfigured CORS; want no header", v)
	}
}

// sameHandler reports whether two http.Handler values wrap the same function.
// Func values are not comparable with == (only nil is), so the code pointer is
// the way to assert "Middleware returned next unchanged" rather than a wrapper.
func sameHandler(a, b http.Handler) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Kind() != reflect.Func || vb.Kind() != reflect.Func {
		return false
	}
	return va.Pointer() == vb.Pointer()
}

func TestCORSSimpleRequest(t *testing.T) {
	cfg := &CORSConfig{
		AllowedOrigins:   []string{"https://app.example"},
		AllowCredentials: true,
		MaxAge:           10 * time.Minute,
	}
	h := cfg.Middleware(okHandler(t))

	t.Run("allowed origin gets ACAO echoing the origin", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		r.Header.Set("Origin", "https://app.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		// With credentials on, "*" is invalid (RFC 6454) so the validated
		// origin is echoed.
		if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "https://app.example" {
			t.Errorf("ACAO = %q, want the echoed origin", v)
		}
		if v := rec.Header().Get("Access-Control-Allow-Credentials"); v != "true" {
			t.Errorf("Access-Control-Allow-Credentials = %q, want true", v)
		}
		if v := rec.Header().Get("Vary"); !strings.Contains(v, "Origin") {
			t.Errorf("Vary = %q, must contain Origin so a shared cache cannot serve origin A's response to origin B", v)
		}
		if v := rec.Header().Get("Access-Control-Expose-Headers"); v == "" {
			t.Error("no Access-Control-Expose-Headers; an SPA cannot read X-Request-Id cross-origin")
		}
	})

	t.Run("no credentials allows the wildcard", func(t *testing.T) {
		c := &CORSConfig{AllowedOrigins: []string{"*"}}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
		r.Header.Set("Origin", "https://anywhere.example")
		rec := httptest.NewRecorder()
		c.Middleware(okHandler(t)).ServeHTTP(rec, r)
		if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "*" {
			t.Errorf("ACAO = %q, want *", v)
		}
		if v := rec.Header().Get("Access-Control-Allow-Credentials"); v != "" {
			t.Errorf("Access-Control-Allow-Credentials = %q with credentials off, want it absent", v)
		}
	})

	t.Run("disallowed origin is served without CORS headers", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
		r.Header.Set("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		// The browser is the enforcement point. A 403 here would surface as an
		// application error the SPA has to special-case.
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (the browser blocks the response, not us)", rec.Code)
		}
		if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("ACAO = %q for a disallowed origin, want none", v)
		}
	})

	t.Run("non-browser request gets no CORS headers", func(t *testing.T) {
		// curl, kubelet probes and Prometheus send no Origin. They must not
		// gain headers, and must not be rejected for lacking one.
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("ACAO = %q on a request with no Origin, want none", v)
		}
	})
}

func TestCORSPreflight(t *testing.T) {
	cfg := &CORSConfig{
		AllowedOrigins:   []string{"https://app.example"},
		AllowedMethods:   []string{http.MethodGet, http.MethodPost},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           10 * time.Minute,
	}
	h := cfg.Middleware(okHandler(t))

	newPreflight := func(origin, reqMethod, reqHeaders string) *http.Request {
		r := httptest.NewRequest(http.MethodOptions, "/api/v1/clusters", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", reqMethod)
		if reqHeaders != "" {
			r.Header.Set("Access-Control-Request-Headers", reqHeaders)
		}
		return r
	}

	t.Run("answers 204 with the full header set", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newPreflight("https://app.example", http.MethodGet, "Authorization"))

		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
		if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "https://app.example" {
			t.Errorf("ACAO = %q", v)
		}
		if v := rec.Header().Get("Access-Control-Allow-Methods"); v != "GET, POST" {
			t.Errorf("ACAM = %q, want %q", v, "GET, POST")
		}
		if v := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(v, "Authorization") {
			t.Errorf("ACAH = %q, must include the requested Authorization", v)
		}
		if v := rec.Header().Get("Access-Control-Max-Age"); v != "600" {
			t.Errorf("ACMax-Age = %q, want 600 (10m)", v)
		}
		if v := rec.Header().Get("Vary"); !strings.Contains(v, "Origin") {
			t.Errorf("Vary = %q, must contain Origin", v)
		}
		// A preflight must not reach the handler: the mux has no OPTIONS
		// route and would answer 405 (R8.5's MethodNotAllowed).
		if rec.Body.Len() != 0 {
			t.Errorf("preflight body = %q, want empty (the handler must not run)", rec.Body.String())
		}
	})

	t.Run("requested headers not in the config are still allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newPreflight("https://app.example", http.MethodGet, "X-Custom-Thing, Authorization"))
		v := rec.Header().Get("Access-Control-Allow-Headers")
		if !strings.Contains(v, "X-Custom-Thing") {
			t.Errorf("ACAH = %q, want the union including X-Custom-Thing", v)
		}
		if !strings.Contains(v, "Authorization") {
			t.Errorf("ACAH = %q, want the configured headers too", v)
		}
		// Deduplicated case-insensitively: "Authorization" appears once.
		if n := strings.Count(strings.ToLower(v), "authorization"); n != 1 {
			t.Errorf("ACAH = %q, Authorization listed %d times, want 1", v, n)
		}
	})

	t.Run("disallowed origin gets no preflight answer", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newPreflight("https://evil.example", http.MethodGet, "Authorization"))
		if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("ACAO = %q for a disallowed preflight origin, want none", v)
		}
	})

	// A bare OPTIONS with no Access-Control-Request-Method is not a preflight;
	// it belongs to the mux, which answers 405 per R8.5.
	t.Run("bare OPTIONS is not a preflight", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodOptions, "/api/v1/clusters", nil)
		r.Header.Set("Origin", "https://app.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code == http.StatusNoContent {
			t.Error("bare OPTIONS answered 204; only OPTIONS with Access-Control-Request-Method is a preflight")
		}
	})
}

// TestCORSMaxAgeDefault pins the 5m default so a config that forgets the field
// does not emit `Access-Control-Max-Age: 0`, which forces a preflight on every
// single API call the Fleet page makes.
func TestCORSMaxAgeDefault(t *testing.T) {
	cfg := &CORSConfig{AllowedOrigins: []string{"https://app.example"}}
	r := httptest.NewRequest(http.MethodOptions, "/api/v1/clusters", nil)
	r.Header.Set("Origin", "https://app.example")
	r.Header.Set("Access-Control-Request-Method", http.MethodGet)
	rec := httptest.NewRecorder()
	cfg.Middleware(okHandler(t)).ServeHTTP(rec, r)

	if v := rec.Header().Get("Access-Control-Max-Age"); v != "300" {
		t.Errorf("ACMax-Age = %q, want 300 (the 5m default)", v)
	}
	if v := rec.Header().Get("Access-Control-Allow-Methods"); v == "" {
		t.Error("ACAM empty; DefaultCORSConfig's method list should fill it")
	}
}

// TestCORSHeadersSetBeforeWriteHeader proves the headers are set on the
// header map before WriteHeader runs. Setting a header after the status line
// is written silently drops it, and the SPA's first cross-origin GET is the
// response where that loss is invisible until it isn't.
func TestCORSHeadersSetBeforeWriteHeader(t *testing.T) {
	var sawACAOAtWriteTime string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawACAOAtWriteTime = w.Header().Get("Access-Control-Allow-Origin")
		w.WriteHeader(http.StatusOK)
	})

	cfg := &CORSConfig{AllowedOrigins: []string{"https://app.example"}}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	r.Header.Set("Origin", "https://app.example")
	cfg.Middleware(h).ServeHTTP(httptest.NewRecorder(), r)

	if sawACAOAtWriteTime != "*" {
		t.Fatalf("ACAO visible to the handler = %q, want * (headers must be set before WriteHeader)", sawACAOAtWriteTime)
	}
}
