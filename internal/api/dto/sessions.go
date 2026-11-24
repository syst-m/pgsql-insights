// R4.1's active-session contract — the body of GET /api/v1/active-queries.
//
// Three types, one per thing R4.1 and R4.4 ask the API to show:
//
//   - SessionView      — one row of R4.1's live session list (R10.8's table)
//   - ScorecardView    — R4.1's summary scorecard, the header above that table
//   - BlockingChainView — R4.4's blocked-query investigation tree (R10.8's row expansion)
//
// and ActiveQueriesResponse, which is the route's body: the three of them plus
// R8.4's meta.
//
// WHY THESE LIVE IN dto AND NOT IN THE HANDLER. The rule is R16.8's — one
// definition per contract, with the JSON fixture and the TypeScript type derived
// from the Go struct rather than written beside it — and this package is where
// every response shape in the product is declared (clusters.go, queries.go,
// series.go). A struct in internal/api/active_queries.go would be a second
// place response shapes live, which is the same defect as typing the field list
// out twice: the two copies agree only until someone edits one.
//
// WHY THE FIELD LIST IS NOT INVENTED HERE. Every field is the projection of a
// field on collector.Session (internal/collector/activity.go:147), which is the
// type the activity collector already builds from pg_stat_activity and whose
// docblock maps each member onto its Cloud SQL column by name. R4.1's list of
// columns — `Process ID`, `Query`, `Query type`, `Status`, `Session duration`,
// `Transaction duration`, `Query duration`, `Wait event type`, `Wait event`,
// `Database`, `Application name`, `User name`, `Client address`, `Action` — is
// therefore not re-derived from the requirement text here; it is read off the
// struct that was written against that requirement and tested against the live
// clusters. That is the difference between importing a contract and restating
// it, and the restatement is what broke the UI's types once already.
//
// The one field the collector deliberately leaves empty is `action`, and the
// reason is on the struct (activity.go:231): R4.1's rule for that column
// depends on `--allow-actions` (R12.8), which is a runtime flag on the API
// server and not a fact about the session, so the API owns the value. The
// handler fills it — see active_queries.go's actionFor.
package dto

import "time"

// SessionView is one row of R4.1's live session list, and R10.8's Active
// Queries table renders exactly these columns.
//
// It is a projection of collector.Session, field for field, and the field
// comments say which Cloud SQL column each one is because R4.1 names them and a
// renamed json tag has to be checkable against a name (the canonical fixture test
// in sessions_test.go re-marshals this struct and byte-compares it to
// testdata/api/active-queries.json, which is what makes a tag change fail CI
// rather than pass silently).
type SessionView struct {
	// Pid is Cloud SQL's `Process ID` (PG's `pid`). It is also the value R4.5's
	// `POST /api/v1/sessions/{pid}/terminate` addresses, so the row and the
	// action route name the same thing the same way.
	Pid int `json:"pid"`

	// Query is Cloud SQL's `Query` — the statement text. On an idle session PG
	// keeps the last statement here, which is why R4.1 lists the column for the
	// idle rows too rather than leaving them blank.
	Query string `json:"query"`

	// QueryType is Cloud SQL's `Query type` — the statement kind, derived from
	// the text by the collector (activity.go:499's queryTypeFrom, with its
	// documented limits and its `UNKNOWN` value for anything it cannot classify).
	QueryType string `json:"queryType"`

	// Status is Cloud SQL's `Status` (PG's `state`). R4.1 requires both `active`
	// and `idle in transaction` rows to be present and R4.6 requires PG18's new
	// `starting` to render rather than error, which is why this is a string and
	// not a closed enum in either layer.
	Status string `json:"status"`

	// The three duration columns, in R4.1's order. All three are measured by the
	// collector against the row's own `pgsi_source_now` rather than against our
	// clock — the reason is R4.8's [A] assertion ("each row's durations match the
	// SQL values within tolerance — with both numbers printed"), which two clocks
	// cannot satisfy — and the explanation lives on collector.Session:165. The
	// view carries the seconds as floats because that is the unit R4.1's columns
	// render in and the unit the collector computes.
	SessionDurationSeconds     float64 `json:"sessionDurationSeconds"`
	TransactionDurationSeconds float64 `json:"transactionDurationSeconds"`
	QueryDurationSeconds       float64 `json:"queryDurationSeconds"`

	// WaitEventType and WaitEvent are Cloud SQL's `Wait event type` and `Wait
	// event` — the pair R4.3's wait-event analysis groups, and the pair R10.9's
	// Wait Events page is built from.
	WaitEventType string `json:"waitEventType"`
	WaitEvent     string `json:"waitEvent"`

	// UnknownWaitEvent is R4.3's per-row validation verdict: the server reported
	// a (type, event) pair its own pg_wait_events catalogue does not contain.
	// Its meaning is "absent from a catalogue that exists", so it is false on
	// PG13–PG16 by construction — the collector's field doc is the argument, and
	// the field is carried rather than dropped because a rollup that labels event
	// names has to say which rows the label is about.
	UnknownWaitEvent bool `json:"unknownWaitEvent"`

	// Database, ApplicationName and UserName are R4.1's last three identity
	// columns and R9.3's two label-bearing ones plus the Cloud SQL third. They
	// are the exact strings R9.3 names as `datname` and `usename`, so the
	// identity-labelling step and this response cannot spell them apart.
	Database        string `json:"database"`
	ApplicationName string `json:"applicationName"`
	UserName        string `json:"userName"`

	// ClientAddress is Cloud SQL's `Client address` (PG's `client_addr`).
	// ClientPort is separate because PG separates the two; Cloud SQL renders them
	// as one column, and formatting the pair is the UI's job — the same division
	// the collector documents at activity.go:209.
	ClientAddress string `json:"clientAddress"`
	ClientPort    string `json:"clientPort"`

	// BackendType is R4.6's requirement made visible: client, autovacuum worker,
	// parallel worker, walsender. R4.1's table is a *client* session list, so
	// this is the field that lets the page answer "why is there an autovacuum row
	// in my client list" without the collector filtering rows R4.1 asked for.
	BackendType string `json:"backendType"`

	// QueryID is R4.7's link to the R3 query detail, and R4.7's "when the id is
	// known" is the clause that makes an empty string correct on PG13 (no
	// query_id column) rather than a missing value — hence omitempty.
	//
	// Spelled `queryid`, not `queryId`, to match the one place this codebase
	// already puts a query id on the wire: QueryView:169 and QuerySeries both use
	// `queryid`, and a second spelling of the same concept in a second file is
	// the second copy R16.8 exists to prevent.
	QueryID string `json:"queryid,omitempty"`

	// BlockingPids is R4.4's edge list for this row: the pids directly blocking
	// it, from pg_catalog.pg_blocking_pids(pid). Empty means not blocked, and it
	// is an empty array on the wire rather than null for the same reason `data`
	// is: dto's list fields are never null, because `[]` and `null` would be two
	// encodings of one fact and the UI would have to handle both.
	BlockingPids []int `json:"blockingPids"`

	// Action is Cloud SQL's `Action` column — the Terminate affordance (R4.5).
	//
	// The collector leaves it empty and says why on its own struct (activity.go:231):
	// the value depends on `--allow-actions` (R12.8), a runtime flag on the API
	// server, so the API owns it. active_queries.go's actionFor fills it from
	// Config.Actions and names the provisional value set there, not here, so the
	// route's gate logic has one home.
	Action string `json:"action"`

	// SourceTime is the row's own `pgsi_source_now` — the instance clock the
	// three durations above were measured against, and the value that makes
	// R4.8's value-equality assertion checkable rather than tautological.
	// Zero means the driver returned something the parser did not recognise
	// (activity.go:249), which is a missing clock, not the epoch.
	SourceTime time.Time `json:"sourceTime"`
}

// ScorecardView is R4.1's summary scorecard — the header above the session table
// holding "counts by state, longest-running query, longest idle-in-transaction".
//
// It is the API projection of collector.Scorecard (activity.go:583), which is
// computed by collector.Summarize over the *whole* session set while the table
// shows the top 50. That asymmetry is the field worth reading twice: `total` is
// the number of sessions the collector read before R4.1's top-50 cut, so the
// header and the rows beneath it cannot disagree — a count that changed when the
// list was truncated would make the scorecard contradict its own table, which is
// the failure the collector's field doc names at activity.go:604.
type ScorecardView struct {
	// ByState is R4.1's "counts by state": one entry per `state` value present,
	// keyed by the same string SessionView.Status carries. R9.7's
	// `db_backends_by_state` family is the same number from the metrics plane.
	//
	// A map, so an empty scorecard marshals as `{}` rather than `null` — the
	// collector's Summarize initialises it for the same reason (activity.go:608).
	ByState map[string]int `json:"byState"`

	// LongestQuery and LongestIdleInTransaction are R4.1's two named findings.
	// They are separate fields because they are separate rows in Cloud SQL's
	// scorecard and are usually different sessions — a backend idle in
	// transaction is by definition not running a statement, so ranking only by
	// running-statement duration hides the long-idle-in-transaction case, which
	// is the one that holds locks and blocks vacuum. The collector's field doc at
	// activity.go:591 is the argument; this is the shape.
	//
	// Pointers because "no such session exists" is a third state besides every
	// row: a cluster with no active query has no longest query, and omitting the
	// key says that, where a zero-valued SessionView would say "pid 0, 0 seconds".
	LongestQuery             *SessionView `json:"longestQuery,omitempty"`
	LongestIdleInTransaction *SessionView `json:"longestIdleInTransaction,omitempty"`

	// Total is the session count before the top-50 cut (collector.MaxSessions),
	// which is what lets the header say "137 sessions, showing 50" instead of
	// counting its own rows.
	Total int `json:"total"`
}

// BlockingChainView is one entry of R4.4's blocked-query investigation tree, and
// the content of R10.8's "row expansion to the blocking chain".
//
// It is the API projection of collector.BlockingChain (activity.go:723). The
// collector's own doc is the reason the shape is a pair of sessions rather than a
// rendered string: "Blocked is the session the row is about and Blockers are its
// ancestors, nearest first, to MaxChainDepth levels", and R4.4's "with the
// blocking statement text shown for each ancestor" is satisfied by carrying the
// ancestors themselves — so a blocker's statement text always comes from the same
// round as the blocked row's, which is the property that stops the tree showing a
// blocker a statement it may not have been running.
type BlockingChainView struct {
	// Blocked is the session this chain is about: the row the user expanded.
	Blocked SessionView `json:"blocked"`

	// Blockers are its ancestors, nearest first, to collector.MaxChainDepth (3)
	// levels. Empty is not possible here — the collector only builds a chain for
	// a session with at least one known blocker (activity.go:728) — but it is an
	// empty array rather than null on the wire for the usual reason.
	Blockers []SessionView `json:"blockers"`
}

// ActiveQueriesResponse is the body of GET /api/v1/active-queries: R4.1's list,
// R4.1's scorecard, R4.4's chains, and R8.4's envelope metadata.
//
// R8.4 states the envelope once, as `dto.Envelope[T]`, and every list route
// projects that generic (FleetEnvelope, QueriesEnvelope, QuerySeriesEnvelope,
// QueryPlansEnvelope). This route cannot, for a reason R4.1 states rather than
// one this struct invents: the requirement asks for a scorecard *above the
// table*, and `Envelope[T]` has exactly two fields — `data` and `meta` — so a
// fourth key would need a type that is not the generic. The alternative was to
// put the scorecard in `meta`, which R8.4 reserves for paging and the
// partial-render contract (R10.22's `failed[]`/`loading[]`), and a per-route
// field in a shared meta is the same second-copy mistake in the other direction.
// So this is a named struct with the generic's two keys plus R4.1's two, and the
// document publishes it as such.
//
// The source returns this type with `meta` left zero and the handler fills it in
// (active_queries.go), which is the division pageQueries makes for the queries
// list: `generatedAt` is a property of the response, not of the round that
// produced the rows, and a source that stamped it would be answering "when was
// this page served" from inside the data.
type ActiveQueriesResponse struct {
	// Data is R4.1's top-50 list, ranked by the collector's own order
	// (activity.go:792's TopSessions). Never null: an empty list marshals to [].
	Data []SessionView `json:"data"`

	// Scorecard is R4.1's header. It is always present, including when Data is
	// empty, because "0 sessions" is an answer the header has to give.
	Scorecard ScorecardView `json:"scorecard"`

	// BlockingChains is R4.4's tree for the blocked rows in Data. It is a
	// separate key rather than a field on each row because a chain names two
	// sessions (the blocked one and its ancestors) and nesting ancestors inside
	// the row that has them would make one session appear in the response twice —
	// once as a row and once as somebody's blocker — with nothing guaranteeing the
	// two copies agree.
	BlockingChains []BlockingChainView `json:"blockingChains"`

	// Meta is R8.4's envelope metadata, identical in shape to the other four
	// list routes'.
	Meta Meta `json:"meta"`
}
