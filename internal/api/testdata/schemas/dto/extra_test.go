// This file's existence is the assertion. parseGoPackage must skip _test.go files,
// so TestOnly is invisible to the schema check even though it is a real struct in a
// real file in the fixture directory — and an entry naming it has to report the same
// "resolves to no type" message it gives for a type that was deleted. That is what
// makes the skip observable rather than assumed: the routes fixture has a
// register_test.go for the same reason, and the two skip rules live in different
// functions, so one cannot stand in for the other.
package dto

type TestOnly struct {
	Ghost string `json:"ghost"`
}
