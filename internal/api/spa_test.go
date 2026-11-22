// Tests for the embedded SPA (R10.1) and its routing fallback (R10.16).

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
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
	// R8.5's envelope, parsed with the same struct the API's own handlers
	// marshal — which is the point: a client that can read a 404 from
	// /api/v1/clusters must be able to read a 404 from this handler with the
	// same code path, not a second reader for a second shape.
	var body dto.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 body is not an R8.5 envelope: %v (%q)", err, rec.Body.String())
	}
	if body.Error.Code != dto.ErrNotFound {
		t.Fatalf("404 code = %q, want %q", body.Error.Code, dto.ErrNotFound)
	}
	if body.Error.Details == nil {
		t.Fatal("404 details is null; R8.5 requires the array always present")
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
