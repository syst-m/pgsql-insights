package dto

// This file is the three R3.7 rollups that complete the set R10.3's page list
// names.
//
// R3.7 (requirements.md:314) names four per-dimension rollups — "by database, by
// user, by application name, by client address" — and R10.3 (requirements.md:416)
// names the four pages that render them: Databases, Users, Tags, Client Addresses.
// Tags is already served — TagView (queries.go:624), testdata/api/tags.json, read
// by internal/store/rollups.go:490 — because R3.12's tag page is the same rollup
// shape wearing R3.12's dimension set. The three structs here are the other three,
// and R8.2 gives each its own route:
//
//	GET /api/v1/insights/databases
//	GET /api/v1/insights/users
//	GET /api/v1/insights/client-addresses
//
// All three are still listed in api/x-planned-paths.yaml's r8.2-catalogue
// (api/x-planned-paths.yaml:147-149) and each leaves that list in the same commit
// as the handler that serves it — the rule the file states for itself when a route
// lands ("so this commit deletes the entry rather than marking it, same rule as the
// five above", api/x-planned-paths.yaml:120-124).
//
// The three share one shape because R3.7 gives them one rule rather than four: each
// rollup "answers 'which X accounts for the most load' with the same load columns
// as R3.2 — including `Load by lock wait`, which is available at this level from
// `pg_locks` + `pg_stat_activity` even though it is not available per query." So
// each struct is exactly three things: the dimension key, the embedded LoadColumns
// (queries.go:86), and LockWaitMs. Nothing else, for the reason clusters.go:206
// gives for its own absent columns — a field here is a field web/src/api/types.ts
// has to react to, and R3.7 does not ask for more than this.
//
// The embedding is the point, not a convenience. TestLoadColumnsAreSharedByEveryRollup
// (queries_test.go:209) enumerates the rollup types and requires each to embed
// LoadColumns anonymously; its list is the repo's working definition of "every
// rollup", so these three are added to it in this commit rather than left outside a
// test whose name claims to cover them.
//
// Why one file for three endpoints: the package groups by resource, not by route —
// queries.go holds four of R8.2's routes and clusters.go holds two. And why not
// `rollups.go`, the name the store uses for the same concept: the parity matrix
// resolves a bare `rollups.go:445` by unique suffix
// (scripts/check-parity-matrix.py:409-414), and internal/api/tags.go:57 cites the
// store's file that way. A second file with that basename would turn four existing
// cites ambiguous — 2+ matches is a failure, not a warning — so the file is named
// for R3.7's own phrase instead.

// DatabaseView is one row of GET /api/v1/insights/databases: R3.7's by-database
// rollup, the row R10.3's Databases page renders.
//
// Database is pg_database.datname, spelled `database` as it is at
// internal/api/dto/sessions.go:101 and queries.go:185.
//
// The database *size* Cloud SQL's Databases tab shows beside the load columns is
// deliberately not here. That number is R5.2's metric family
// (internal/collector/database.go), served by R9.4's series endpoints; R3.7's
// sentence asks only "which database accounts for the most load" and answers it
// with R3.2's columns. Adding a size field would also force the question of which
// of the three reference clusters' sizes a fixture row carries, which is a Phase 7
// question (R5, R10.10) that this route does not own.
type DatabaseView struct {
	// Database is datname — R3.7's "by database".
	Database string `json:"database"`

	// LoadColumns is R3.2's set, which R3.7 requires of every rollup.
	LoadColumns

	// LockWaitMs is the column R3.2 denies to a per-query row but grants at this
	// level: R3.7 sources it "at database/user level from `pg_locks` +
	// `pg_stat_activity`" in the same sentence that withholds it per query.
	LockWaitMs float64 `json:"lockWaitMs"`
}

// UserView is one row of GET /api/v1/insights/users: R3.7's by-user rollup, the
// row R10.3's Users page renders.
//
// UserName is pg_roles.rolname as pg_stat_activity surfaces it in `usename`,
// spelled `userName` as it is at internal/api/dto/sessions.go:103.
//
// Flagged for A3: TagView (queries.go:624) spells the same column `user`. The two
// are not reconcilable by editing one and re-running -update — TagView's name comes
// from R3.12's dimension list, which names the PG columns (`application_name`,
// `usename`, `datname`) and whose struct drops each `_name` suffix
// (application_name→application, usename→user, datname→database), while
// SessionView's comes from the column itself. This struct follows SessionView:
// R3.7's rollup groups on the role, not on a tag dimension, and `user` is a
// reserved word in the language the field name is derived from. `userName` is also
// the majority spelling — internal/api/dto/sessions.go:103 and
// web/src/api/types.ts:813 both use it, and TagView is the only `user` in the
// repo. The divergence is a finding for A3 to rule on, not a precedent to copy a
// third time; renaming either one is a breaking change to web/src/api/types.ts and
// belongs in the same commit as the fixture regeneration.
type UserView struct {
	// UserName is usename — R3.7's "by user".
	UserName string `json:"userName"`

	// LoadColumns is R3.2's set, which R3.7 requires of every rollup.
	LoadColumns

	// LockWaitMs is the column R3.2 denies to a per-query row but grants at this
	// level; R3.7 names the database/user level explicitly.
	LockWaitMs float64 `json:"lockWaitMs"`
}

// ClientAddressView is one row of GET /api/v1/insights/client-addresses: R3.7's
// by-client-address rollup, and one of the two surfaces R6.4
// (requirements.md:359) names as in scope for its pooler-attribution fix —
// "Attribution applies to (a) the R4.1 active-session list, via `SHOW CLIENTS`
// (S6) mapping each server backend to its real client address, and (b) the R3.7
// by-client-address rollup, which Cloud SQL itself models as a separate dimension."
//
// ClientAddress is pg_stat_activity.client_addr, spelled `clientAddress` as it is
// at internal/api/dto/sessions.go:109.
//
// Two things R6.4 mentions and this struct does not carry:
//
//   - clientPort. A per-session row can name one (sessions.go:110 does, as a
//     string, because PG splits client_addr from client_port and Cloud SQL joins
//     them into one column); a rollup row cannot, because one client address is
//     many sessions and each has its own port. The rollup groups on the address,
//     which is what R3.7's dimension is.
//   - the attribution mode. R6.4's `--pooler-attribution=off|cloudsql|pgbouncer`
//     is config, and R8.4's envelope echoes no request state — the rule QueryView's
//     docblock states for the window ("the window itself is not echoed here … the
//     client named the window it asked for") applies here too. The value is
//     self-describing: under `cloudsql` it is the literal `local` Google renders,
//     under `pgbouncer` it is the real address SHOW CLIENTS maps the backend to,
//     and under `off` (R3.13's `--record-client-address=false`) there are no rows
//     to carry it.
type ClientAddressView struct {
	// ClientAddress is client_addr — R3.7's "by client address", sourced per
	// R6.4's (b) and R3.13's recording flag.
	ClientAddress string `json:"clientAddress"`

	// LoadColumns is R3.2's set, which R3.7 requires of every rollup.
	LoadColumns

	// LockWaitMs is the column R3.2 denies to a per-query row but grants at this
	// level, from pg_locks + pg_stat_activity.
	LockWaitMs float64 `json:"lockWaitMs"`
}
