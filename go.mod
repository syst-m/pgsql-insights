module github.com/frankellis/pgsql-insights

go 1.26.0

// Phase 0 (R11.1, R13.10): the binary is stdlib-only.
// modernc.org/sqlite (pure Go, no cgo — §3.4) lands in Phase 2 with the store,
// and the k8s client-go dependency lands in Phase 1 with discovery.
// R13.10 forbids `replace` directives; `make tidy-check` enforces tidiness in CI.
