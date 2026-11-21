// Package web embeds the built SPA (R10.1, R11.4).
//
// R10.1 requires the SPA to be served by the Go binary itself — no second
// deployment, no nginx sidecar, no CDN. The Vite build in this directory
// produces web/dist; that directory is committed (see the `!/web/dist/` rule in
// .gitignore) and embedded here, so `go build ./...` on a machine with no node
// toolchain still links a binary that serves the full UI.
//
// Committing dist is deliberate and has one cost: the bundle is not content-
// hashed (vite.config.ts pins assets/index.js), so a rebuild always touches the
// same two files and the diff is reviewable. R11.4's reported "SPA bundle"
// number is measured from these same files by scripts/image-size.py.
package web

import (
	"embed"
	"io/fs"
	"path"
	"strings"
)

// all: includes the files vite marks with a leading underscore
// (dist/assets/*.css is fine, but `all:` also covers .vite/ and any future
// dotfile in the tree, which plain `dist` would silently skip).
//
//go:embed all:dist
var dist embed.FS

// Assets returns the embedded bundle rooted at web/dist, so callers address
// files as "index.html" and "assets/index.js" rather than "dist/index.html".
func Assets() fs.FS { return assets }

var assets = mustSub()

func mustSub() fs.FS {
	// fs.Sub on a literal path embedded above cannot fail; panicking keeps the
	// API surface error-free for the one caller that would otherwise have to
	// handle an impossible case forever.
	f, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: embedded dist tree is missing: " + err.Error())
	}
	return f
}

// Index is the path of the SPA entry document inside Assets().
const Index = "index.html"

// Exists reports whether path p is a real file in the bundle. The API server
// uses it to answer a request for /assets/index.js directly instead of
// falling back to index.html.
func Exists(p string) bool {
	fi, err := fs.Stat(assets, strings.TrimPrefix(path.Clean("/"+p), "/"))
	return err == nil && !fi.IsDir()
}
