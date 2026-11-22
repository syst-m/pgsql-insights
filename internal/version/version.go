// Package version holds build metadata injected at link time (R11.3).
//
// The Makefile passes -ldflags "-X ...Version=$(VERSION) -X ...Commit=$(COMMIT)"
// and the same values become the org.opencontainers.image.{version,revision}
// image labels, the /api/v1/version body, and the pgsi_build_info labels.
package version

// Version is the semantic version, e.g. "0.1.0" or "v0.1.0-3-gabcdef".
// "dev" when built without ldflags (a plain `go build`).
var Version = "dev"

// Commit is the *full* 40-character git SHA the binary was built from, e.g.
// "db4f2371c8d4…" — not the 7-character short form.
//
// D6: the first CI-published binary stamped `commit:"a47e149"` while the commit
// it was built from was `db4f237`, because the short form is easy to carry
// forward from a stale build and impossible to trace back to a tree. The
// Makefile now takes `git rev-parse HEAD` (full), CI asserts the built binary's
// commit equals $GITHUB_SHA, and the image's 7-character tag is checked to be
// that same SHA's first 7 characters — R11.3's "tag pair" made checkable.
//
// The image tag stays 7 characters (R11.3's convention); this variable and the
// org.opencontainers.image.revision label carry the full SHA, which is the OCI
// image-spec convention for a revision.
var Commit = "unknown"

// String returns the one-line form printed by `pgsi version`.
func String() string {
	return "pgsi " + Version + " (commit " + Commit + ")"
}
