// SPA serving (R10.1) — the second half of "one binary".
//
// R10.1 states the SPA is served by the Go binary at `/`, with no separate web
// server; R10.16 makes every view addressable by a *path* (/c/<ns>/<name>), so
// the server must answer those paths with the app shell rather than a 404.
// That combination is what this file implements: real files are served from the
// embedded bundle, and any other non-API path returns index.html so the
// client-side router can take over.

package api

import (
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"github.com/frankellis/pgsql-insights/web"
)

// handleSPA is mounted at `GET /`, the mux's root pattern. Go's routing picks
// the most specific registered pattern first, so /healthz, /readyz and
// /api/v1/version are never seen here; everything else is.
func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

	// An unknown /api/... path must answer 404, not the app shell. Without this
	// guard a client calling an endpoint this build doesn't have (the Phase 1a
	// /api/v1/clusters, before it lands) would get HTTP 200 + HTML and a JSON
	// decode error instead of a clean 404.
	if name == "api" || strings.HasPrefix(name, "api/") {
		writeNotFound(w, r)
		return
	}

	if name == "" {
		name = web.Index
	}
	if !web.Exists(name) {
		// A missing asset with a file extension (a stale /assets/index-abc.js
		// after an upgrade, a missing favicon) is a 404. Serving index.html here
		// would hand the browser an HTML body where it asked for a script.
		if path.Ext(name) != "" {
			writeNotFound(w, r)
			return
		}
		// R10.16: a deep route the router owns (/c/freeidp/freeidp-pg) returns
		// the shell, which then parses the URL and renders that view.
		name = web.Index
	}

	// index.html is the one file that must always be revalidated: the bundle it
	// references is emitted un-hashed (vite.config.ts), so a cached shell is
	// what pins a stale bundle after a deploy. Assets get a short TTL for the
	// same reason — a long one would keep serving the previous release's JS for
	// its whole lifetime.
	if name == web.Index {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	http.ServeFileFS(w, r, web.Assets(), name)
}

// writeNotFound answers a 404 as JSON. Phase 1a introduces the API's error
// envelope (R8.1); until then this is the smallest shape that a JSON client can
// parse, so an unknown endpoint never looks like a successful call.
func writeNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	// HEAD responses carry the headers and no body; writing one is a lie the
	// transport silently swallows, so skip it.
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "not_found"})
}
