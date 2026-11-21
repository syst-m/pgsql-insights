// Tests for the embedded SPA (R10.1) and its routing fallback (R10.16).

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/web"
)

func TestSPARootServesTheBundle(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200 (R10.1: the binary serves the SPA)", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="root"`) {
		t.Fatal("GET / did not return the app shell: no #root element in index.html")
	}
	// The shell must reference the bundle by an absolute path, and that path
	// must exist in the embed. A relative `./assets/index.js` (Vite's default)
	// resolves against the *current* path, so every deep route would 404 its own
	// bundle and render a blank page — this is the one assertion that catches it
	// from the Go side.
	if !strings.Contains(body, `src="/assets/index.js"`) {
		t.Fatalf("index.html does not reference /assets/index.js absolutely:\n%s", body)
	}
	if !web.Exists("assets/index.js") {
		t.Fatal("embedded bundle is missing assets/index.js")
	}
}

func TestSPAAssetServedWithCaching(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/assets/index.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /assets/index.js = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q, want a javascript mime", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Fatal("asset served with no Cache-Control")
	}
	// The shell must always revalidate; the bundle it names is un-hashed.
	if cc := do(t, newTestServer(t), http.MethodGet, "/").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("index.html Cache-Control = %q, want no-cache", cc)
	}
}

func TestDeepRouteFallsBackToTheShell(t *testing.T) {
	// R10.16: /c/<ns>/<name> is a client-side route. The server cannot know it,
	// so it must return the shell rather than a 404 — a 404 here means the back
	// button works in-app but a refresh or a shared link breaks.
	for _, target := range []string{"/c/freeidp/freeidp-pg", "/settings"} {
		rec := do(t, newTestServer(t), http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (shell)", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Fatalf("GET %s did not return the app shell", target)
		}
	}
}

func TestUnknownAPIPathIsNotTheShell(t *testing.T) {
	// The catch-all must not swallow the API namespace: an endpoint this build
	// doesn't implement answers 404 + JSON, never 200 + HTML.
	rec := do(t, newTestServer(t), http.MethodGet, "/api/v1/does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/does-not-exist = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["error"] != "not_found" {
		t.Fatalf("404 body = %v, want error=not_found", body)
	}
}

func TestMissingAssetIsNotTheShell(t *testing.T) {
	// A request for a file that isn't there (a stale hashed name, a missing
	// favicon) must 404 — handing the browser HTML where it asked for an image
	// is the classic SPA misconfiguration.
	rec := do(t, newTestServer(t), http.MethodGet, "/assets/definitely-missing.css")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /assets/definitely-missing.css = %d, want 404", rec.Code)
	}
}
