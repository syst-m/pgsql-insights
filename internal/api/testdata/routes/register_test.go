package routes

import "net/http"

// A registration in a _test.go file must not appear in the table: a test that
// registers a route on its own mux is not part of the served surface, and
// including it would make the document claim routes the binary does not serve.
func testOnly(mux *http.ServeMux, h http.Handler) {
	mux.HandleFunc("GET /api/v1/testonly", h)
}
