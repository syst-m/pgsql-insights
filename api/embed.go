// Package api exposes the embedded OpenAPI document (R8.1).
//
// R8.1 names one path for one artifact: `api/openapi.yaml`. It appears twice in
// requirements.md — in R8.1's own requirement text (`api/openapi.yaml`) and in
// the repo tree diagram — which makes the path part of the requirement rather
// than a layout choice. The file used to live at `internal/api/openapi.yaml`,
// and the reason given at the time was that go:embed cannot reach across
// packages: an embed directive only sees the directory containing the file it
// sits in, and `..` is not allowed in an embed path, so a document in
// `internal/api/` was not embeddable from anywhere else. That reason explains
// why the file was where it was; it does not say it had to stay there, and this
// package is the three lines that satisfy R8.1 without a spec edit.
//
// The package exists for two exports, both of them a file R8 names by path.
// R8.1 asks for a *committed* document, and a committed document no running
// binary can show you is a file, not an API surface: serving it is one handler
// over these bytes, and in the distroless image (R11.4) an os.ReadFile of a
// repo-relative path would fail where the embed always works.
//
// The second is the planned-route manifest, api/x-planned-paths.yaml. It is not
// served — it is R8.1b's third generator input, and internal/api/openapi_gen.go
// reads it from here so the generator has no path assumption to get wrong (a
// //go:embed argument cannot be a constant, and `..` is not allowed in an embed
// path, so the only way internal/api can see an api/ file is this package).
//
// Callers use OpenAPIDocument()/XPlannedPaths() rather than the embed.FS values
// so that widening what the embed exposes is a deliberate edit to this file. The
// functions return bytes, not an fs.FS: an fs.FS rooted here would let a caller
// open any sibling this package later embeds and quietly depend on it.
package api

import (
	"embed"
	"fmt"
)

// OpenAPIDocumentPath is the package-relative path of the R8.1 document. It is
// a constant so callers, the CI check and the docs all name the same string.
// The embed directive below cannot take a constant - a //go:embed argument is
// not an expression - so the path is repeated there, and this is the one line a
// future `mv` has to touch.
const OpenAPIDocumentPath = "openapi.yaml"

//go:embed openapi.yaml
var openapiFS embed.FS

// OpenAPIDocument returns the embedded R8.1 document.
func OpenAPIDocument() ([]byte, error) {
	b, err := openapiFS.ReadFile(OpenAPIDocumentPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s (R8.1): %w", OpenAPIDocumentPath, err)
	}
	return b, nil
}

// XPlannedPathsPath is the package-relative path of the planned-route manifest,
// R8.1b's third generator input. Same reason for the constant as above: a
// //go:embed argument is not an expression.
const XPlannedPathsPath = "x-planned-paths.yaml"

//go:embed x-planned-paths.yaml
var xPlannedPathsFS embed.FS

// XPlannedPaths returns the embedded planned-route manifest (R8.1b input 3).
//
// The generator re-emits these bytes verbatim instead of regenerating them:
// "planned" is the one route set code cannot enumerate, so the manifest is
// authored and copied. That is what keeps `paths` and `x-planned-paths` disjoint
// (R8.1c) — a route that lands moves out of the manifest and into `paths` in the
// same edit, because a manifest entry that is also a route is a second name for
// one route and R14.7 counts one.
func XPlannedPaths() ([]byte, error) {
	b, err := xPlannedPathsFS.ReadFile(XPlannedPathsPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s (R8.1b): %w", XPlannedPathsPath, err)
	}
	return b, nil
}
