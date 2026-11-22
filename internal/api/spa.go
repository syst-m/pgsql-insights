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

	"github.com/frankellis/pgsql-insights/internal/api/dto"
	"github.com/frankellis/pgsql-insights/web"
)

// handleSPA is mounted at `GET /`, the mux's root pattern. Go's routing picks
// the most specific registered pattern first, so /healthz, /readyz and
// /api/v1/version are never seen here; everything else is.
func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

	// An unknown /api/... path must answer 404, not the app shell. Without this
	// guard a client calling an endpoint this build doesn't have (the Phase 3
	// /api/v1/queries, until it lands) would get HTTP 200 + HTML and a JSON
	// decode error instead of a clean 404. Phase 1a's /api/v1/clusters is
	// registered on the mux now, so the mux's own routing answers it and it
	// never reaches here; this guard is for the routes after it.
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

// writeNotFound answers a 404 with R8.5's error envelope — the same ErrorBody
// every /api/v1/* handler emits, built by the same constructor, so one JSON
// reader parses a 404 from here and a 404 from the clusters endpoint without
// branching on the shape.
//
// Before Phase 1a it hand-rolled {"error":"not_found"}: `error` as a string
// where R8.5 makes it an object, and `not_found` where the enum spells it
// `NotFound`. Two spellings of one code is exactly what R8.5's "single error
// shape for every endpoint" exists to prevent, and the SPA's own envelope.ts
// reader is the client that notices.
//
// Both callers use it: an unknown /api/... path, whose client is an API client
// and is the case the envelope is written for, and a missing asset, whose
// client is a browser that reads the status and ignores the body. One shape
// for both is the cheaper contract than two shapes for one handler.
func writeNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	// HEAD responses carry the headers and no body; writing one is a lie the
	// transport silently swallows, so skip it.
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(dto.NewError(dto.ErrNotFound, "no such path "+r.URL.Path))
}
