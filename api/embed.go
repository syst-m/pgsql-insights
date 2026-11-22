// The embedded OpenAPI document (R8.1).
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
// The package exists for exactly one export. R8.1 asks for a *committed*
// document, and a committed document no running binary can show you is a file,
// not an API surface: serving it is one handler over these bytes, and in the
// distroless image (R11.4) an os.ReadFile of a repo-relative path would fail
// where the embed always works.
//
// Callers use OpenAPIDocument() rather than openapiFS so that widening what the
// embed exposes is a deliberate edit to this file. The function returns bytes,
// not an fs.FS: an fs.FS rooted here would let a caller open any sibling this
// package later embeds and quietly depend on it.

package api

import (
	"embed"
	"fmt"
)

// OpenAPIDocumentPath is the package-relative path of the R8.1 document. It is
// a constant so callers, the CI check and the docs all name the same string;
// go:embed itself cannot take a constant, which is why the directive below
// repeats it and why this is the one line a future `mv` has to touch.
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
