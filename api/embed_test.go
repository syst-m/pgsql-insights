// Tests for the R8.1 embed (api/embed.go).
//
// The move from internal/api/ to api/ is a rename of a *named* path (R8.1's own
// requirement text names `api/openapi.yaml`), and the failure mode of getting it
// wrong is not a compile error — go:embed fails at build time if the file is
// absent, but a document that moved while a caller kept building its own path
// compiles forever and reads the wrong bytes at runtime. These tests are the
// cheapest thing that catches that.
//
// The full validation (schema version, every path has an operation, the
// catalogue enumeration matches) is scripts/check-openapi.py, which `make
// openapi-check` and the CI lint job run. What belongs in a Go test is the
// narrower claim: the embed resolves, and what it resolves to is the document.

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAPIDocumentEmbedsTheCommittedFile(t *testing.T) {
	b, err := OpenAPIDocument()
	if err != nil {
		t.Fatalf("OpenAPIDocument(): %v", err)
	}
	if len(b) == 0 {
		t.Fatal("embedded openapi.yaml is empty")
	}
	head := string(b)
	if len(head) > 40 {
		head = head[:40]
	}
	if !strings.HasPrefix(head, "#") && !strings.HasPrefix(head, "openapi:") {
		t.Errorf("document starts with %q, want a comment header or the openapi: key", head)
	}
	if !strings.Contains(string(b), "openapi: 3.0.3") {
		t.Error("document has no `openapi: 3.0.3` version key")
	}

	// The embed and the file on disk must be the same bytes. This is the
	// assertion that catches a caller (or a future refactor) reading the file
	// from a path instead of the embed: in a test the file exists, in the
	// distroless image (R11.4) it does not.
	onDisk, err := os.ReadFile(filepath.Join(".", OpenAPIDocumentPath))
	if err != nil {
		t.Fatalf("read %s: %v (R8.1 names this path; it must exist next to embed.go)", OpenAPIDocumentPath, err)
	}
	if string(onDisk) != string(b) {
		t.Error("embedded document differs from api/openapi.yaml on disk")
	}
}
