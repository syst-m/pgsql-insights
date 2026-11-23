package routes

import "net/http"

// register is the fixture's stand-in for server.go's registration block. Every
// shape the generator has to handle appears exactly once, and the numbered
// comments are the case list TestRoutesInDirFixture asserts against.
func register(mux *http.ServeMux, h http.Handler) {
	// 1. A plain literal.
	mux.HandleFunc("GET /api/v1/literal", h)
	// 2. A literal concatenated onto a const — server.go's own shape
	//    (`"GET "+ClusterDetailPath`), and the reason the const table exists.
	mux.HandleFunc("GET "+Plain, h)
	// 3. A bare const whose value needs a two-hop chain to resolve (Deep3 ->
	//    Deep2 -> Deep1). This is the case the single-pass collector dropped.
	mux.HandleFunc(Deep3, h)
	// 4. A duplicate of #1: the table dedupes, so two registrations of one
	//    "METHOD /path" yield one entry.
	mux.HandleFunc("GET /api/v1/literal", h)
	// 5. A pattern that is a function call: not statically knowable, so the
	//    registration is skipped rather than guessed at.
	mux.HandleFunc(computed(), h)
	// 6. mux.Handle, not HandleFunc: the generator matches HandleFunc only.
	mux.Handle("GET /api/v1/notafunc", h)
}

func computed() string { return "GET /api/v1/computed" }
