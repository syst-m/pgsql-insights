// Fixture for TestRoutesInDirFixture in openapi_gen_test.go. Nothing here is
// compiled — the go tool ignores testdata/ — and routesInDir parses it with
// go/parser directly, which is the capability routesInDir's own header advertises
// ("so the test can run it against a fixture as well as against the real
// package") and that the test package did not exercise until this file existed.
//
// The three consts are deliberately in reverse dependency order. A single
// top-to-bottom pass sees Deep3 first, cannot resolve Deep2, records nothing for
// Deep3, and every registration using Deep3 disappears from the route table with
// no diagnostic. A fixture with the consts in dependency order cannot tell the
// single-pass and fixed-point implementations apart, which is why the order *is*
// the content of this file.
package routes

const Deep3 = "GET " + Deep2
const Deep2 = Deep1 + "/deep2"
const Deep1 = "/api/v1"

// Plain is a plain literal path, the shape server.go writes for /api/v1/clusters.
const Plain = "/api/v1/plain"
