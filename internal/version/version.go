// Package version holds build metadata injected at link time (R11.3).
//
// The Makefile passes -ldflags "-X ...Version=$(VERSION) -X ...Commit=$(COMMIT)"
// and the same values become the org.opencontainers.image.{version,revision}
// image labels, the /api/v1/version body, and the pgsi_build_info labels.
package version

// Version is the semantic version, e.g. "0.1.0" or "v0.1.0-3-gabcdef".
// "dev" when built without ldflags (a plain `go build`).
var Version = "dev"

// Commit is the short git SHA the binary was built from.
var Commit = "unknown"

// String returns the one-line form printed by `pgsi version`.
func String() string {
	return "pgsi " + Version + " (commit " + Commit + ")"
}
