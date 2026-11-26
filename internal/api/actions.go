// R12.8's action-name vocabulary: the names `--allow-actions` accepts, in one file.
//
// # Why this file exists
//
// The set had two homes, one per consumer. `terminate` was declared in
// active_queries.go — the file for the route that renders it (R4.1) — and
// `enable-extension` in cmd/pgsi/enable.go, the file for the command that performs
// it (R3.15). A1 filed the split and asked for one home ("give the names one home —
// export the set from `internal/api` and have `cmd/pgsi` import it"), and the shape
// is theirs: a new file, not active_queries.go, because a file named for one route
// is the wrong home for the names of all of them.
//
// This is R16.8's rule — never author a second copy of a contract — applied where it
// bites on the Go side, where the contract is a set rather than a type. R12.8's own
// sentence is the spec text: the flag's value names "the full set of mutations the
// product performs: pg_terminate_backend (R4.5), the spec.parameters patch (R3.15),
// and any future action — each individually named in the flag's value, not covered by
// one global boolean". A set declared in two files is two sets: either side can add
// a name the other does not know, and no test in either package sees it.
// cmd/pgsi/enable_test.go's TestKnownActionsCoversBothConsumers exists only because
// of that split; with one declaration its cross-package half collapses to a
// membership test on one variable, which is what its body already is.
//
// Two gates read the set, from two packages, and both already import this one:
// actionFor (active_queries.go:158) answers "is this one action allowed" for R4.1's
// `Action` column and for R4.5's terminate route, and parseAllowActions
// (cmd/pgsi/enable.go) validates a whole flag value for the CLI.
//
// # Provisional names (D13/R9.11)
//
// The spec names the gate and the mutations but not the spellings, so every string
// here is provisional under D13/R9.11's rule — name it, mark it provisional in the
// file itself, bound the blast radius. The bound is this file plus actionFor: if
// R12.8's flag ends up spelling an action differently, that constant changes, the
// fixture rows that carry it change, and nothing else in the response shape moves.
// Renaming one is free until a later ruling fixes the flag's spelling. The marking
// each of the two pre-existing names carried in the files that used to hold them
// (active_queries.go, cmd/pgsi/enable.go) now lives here, in the file that owns both,
// which is what keeps it from being restated per consumer.
package api

// ActionNameTerminate is R4.5's `pg_terminate_backend` action as it appears in
// R12.8's `--allow-actions` value. R12.8 generalised the flag from "terminate
// session only" to a set in which each mutation is "individually named in the
// flag's value, not covered by one global boolean", which is why this is a name in
// a list and not a bool on Config.
const ActionNameTerminate = "terminate"

// ActionDisabled is what the column carries when the gate is off — which is the
// default, so it is what every row answers on a stock server. R4.1's rule is that
// the column "renders disabled with a link to the R12.8 gate … so the column is
// never silently blank", and an empty string is exactly the silent blank that
// sentence forbids. The UI renders the link; the value says why there is no action.
const ActionDisabled = "disabled"

// ActionNameEnableExtension is R3.15's spec.parameters patch as it appears in
// R12.8's --allow-actions value. Provisional under D13 for the same reason as the
// two above: the spec names the gate and the mutation but not the string. It was
// declared in cmd/pgsi/enable.go, the command that performs the patch, and moves
// here so the flag's set has one declaration; that file's own const is deleted in
// the commit switching its `knownActions` over to KnownActions below.
const ActionNameEnableExtension = "enable-extension"

// KnownActions is the set R12.8's value draws from — the two names above, and the
// place any future action gets added. cmd/pgsi/enable.go declared the same two
// elements as its package-private `knownActions`; that var becomes an alias of this
// one (`var knownActions = api.KnownActions`) in A1's commit, so the flag's set and
// the API's set are one declaration that no test has to police for agreement.
var KnownActions = []string{ActionNameTerminate, ActionNameEnableExtension}
