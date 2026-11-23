// Package-level pagination machinery — R8.4's parameter set and R8.10's paging,
// factored out of clusters.go so every list endpoint shares one implementation.
//
// This file exists because of a counting problem. R8.2 names eighteen core
// endpoints and R8.4 says *every* list endpoint supports the same seven query
// parameters and returns the same envelope; R8.10 fixes one paging contract for
// all of them (default 50, max 500, opaque cursor, a stable secondary key per
// sort). Phase 1a implemented that contract for one route, inline in
// clusters.go, and the inline version was correct: the cursor carried its sort
// so a cross-sort cursor is rejected rather than silently skipping rows, the
// row identity is appended to every sort key so ties have a defined order, and
// `total` is the pre-cut match count per D18. Those are not per-route rules —
// they are R8.10's rules, proved once.
//
// So the generic half lives here and the per-resource half stays with the
// resource. What is *mechanical* (base64 a {sort, key} pair; reject a limit
// above the max rather than clamping it; drop rows at-or-before the cursor;
// cut the page; mint the next cursor from the last row's key) is here, once,
// over a Go type parameter. What is *semantic* (which sort keys a cluster
// list has, how a row's identity is built, which rows match a filter) stays in
// the resource's own file, because that answer is different per route and
// cannot be shared without becoming a union of every route's answers.
//
// The rule this file enforces is the one this slot broke once already: never
// author a second copy of a contract. A second cursor implementation is a
// second place to be wrong, and the two copies drift silently — the UI types
// were the same mistake in TypeScript, and nothing went red there either.

package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// R8.10's page-size pair, for every list route. They are constants, not a
// Config field, because R8.10 fixes them ("default 50, max 500") rather than
// making them tunable, and a per-deployment knob would let a deployment be
// configured into a shape the spec does not describe.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// pageCursor is the decoded form of R8.10's opaque cursor.
//
// R8.10 says it is "base64 of the last sort key, not an offset", and that is
// what this is: the sort key of the last row of the previous page, plus the
// sort it belongs to. The sort is in there because a cursor is only meaningful
// relative to an ordering — page 2 of ?sort=phase is not a subset of page 1 of
// ?sort=name — and it is *not* an offset, so a page that shrinks between
// requests neither skips nor repeats the row a client already saw.
//
// It is resource-independent by construction: {sort, key} says nothing about
// clusters, queries or poolers, which is exactly why it can live here. The
// resource-specific part is how the *key* is built (a resource's sortKey), not
// how the cursor carrying it is encoded.
type pageCursor struct {
	Sort string `json:"s"`
	Key  string `json:"k"`
}

// encodeCursor renders a cursor for the given sort and row key.
func encodeCursor(sort, key string) string {
	b, _ := json.Marshal(pageCursor{Sort: sort, Key: key})
	return base64.URLEncoding.EncodeToString(b)
}

// decodeCursor parses a cursor, accepting both the padded and unpadded base64
// spellings. URLEncoding emits "=" padding; a client that strips it from a
// query string is common enough that both spellings are accepted rather than
// being a compatibility footnote.
func decodeCursor(raw string) (pageCursor, error) {
	b, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		if b, err = base64.RawURLEncoding.DecodeString(raw); err != nil {
			return pageCursor{}, err
		}
	}
	var c pageCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return pageCursor{}, err
	}
	if c.Sort == "" || c.Key == "" {
		return pageCursor{}, fmt.Errorf("cursor missing its sort key")
	}
	return c, nil
}

// ListQuery is R8.4's uniform parameter set plus R8.10's cursor, parsed and
// validated. Every list endpoint's query is this shape — R8.4 says so in one
// sentence ("Every list endpoint supports ?namespace=, ?cluster=, ?db=,
// ?from=, ?to=, ?limit=, ?sort=") — so it is one type, not one type per route.
//
// A route that has no per-database or time-axis meaning (the fleet list has
// neither) still parses and validates DB/From/To: openapi.yaml documents `db`
// as "accepted and ignored on /clusters", and a client that sends
// ?from=yesterday has a bug worth reporting even on a route where the value
// changes nothing. Validating a parameter the route ignores is the cheap half
// of R8.4; silently dropping it is the version that hides client bugs.
type ListQuery struct {
	Namespace string
	Cluster   string
	DB        string
	From      time.Time
	To        time.Time

	// Sort is the resolved key without its direction prefix; Desc is that
	// prefix. R8.4 names `?sort=` without spelling out the direction syntax,
	// so this follows the convention the rest of the API uses: a leading "-"
	// sorts descending.
	Sort string
	Desc bool

	Limit int
	// cursor is the decoded form of ?cursor=. It is unexported so a ListQuery
	// can only carry a cursor that was successfully parsed — the shape R8.10
	// promises is validated at the edge, not re-derived per page.
	cursor *pageCursor
}

// QuerySpec is the per-resource vocabulary the generic parser needs: the sort
// keys this route accepts and the one it defaults to. It is a parameter rather
// than a generic type because the vocabulary is data (a list of names), and
// turning it into a type would mean a type per route that carries no behaviour.
//
// The list doubles as the error message: an unsupported `?sort=` is rejected
// with the supported set spelled out from this field, so the message cannot
// drift from what the route actually accepts. That is the same argument that
// made the R8.5 codes constants instead of per-call-site strings.
type QuerySpec struct {
	// Sorts lists the accepted keys, without direction prefixes. The order is
	// the order the error message prints, so it is written in the order the
	// route's own documentation lists them.
	Sorts []string
	// Default is the key used when `?sort=` is absent. It must be a member of
	// Sorts; a spec that violates that is a programming error, not a runtime
	// condition, so parseListQuery answers it rather than defaulting quietly.
	Default string
	// DefaultDesc is the direction that default sorts in — true means the
	// default is the `-` form of Default.
	//
	// It exists because direction is per-route, not global. R3.1 orders the
	// cluster list by phase ascending (not-started first), so clusters' default
	// needs no direction and got none; R3.2 ranks the Queries table by
	// *most* expensive first, so a query list whose default sorted ascending
	// would answer with the cheapest queries on page one. A spec that could only
	// name the key could express one route's default and not the other's, and
	// the route that lost was the one added second.
	DefaultDesc bool
}

// parseListQuery validates R8.4's parameters against a route's vocabulary.
// Every rejection is an R8.5 InvalidRequest with a message naming the parameter,
// because the alternative — clamping or ignoring — turns a client bug into a
// silently different answer.
func parseListQuery(q url.Values, spec QuerySpec) (ListQuery, error) {
	// The default is required and must be a key the route declares. A spec that
	// violates either half is a programming error, not a runtime condition, and
	// answering it with an error beats picking an undocumented default: the
	// alternative is a route whose default sort appears in no document.
	if spec.Default == "" || !slices.Contains(spec.Sorts, spec.Default) {
		return ListQuery{}, fmt.Errorf("invalid sort: %q (supported: %s)",
			spec.Default, strings.Join(spec.Sorts, ", "))
	}

	out := ListQuery{
		Namespace: q.Get("namespace"),
		Cluster:   q.Get("cluster"),
		DB:        q.Get("db"),
		Sort:      spec.Default,
		// The spec's default direction, not a hardcoded false: see QuerySpec's
		// DefaultDesc for why the direction is per-route. An explicit `?sort=`
		// below overwrites both fields, so this only applies when the parameter
		// is absent.
		Desc:  spec.DefaultDesc,
		Limit: DefaultLimit,
	}

	// limit: R8.10's default/max pair, and the spec is explicit that a value
	// above the maximum is InvalidRequest, "not a silent clamp, so a client that
	// asks for 5000 learns about it instead of getting 500".
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return out, fmt.Errorf("invalid limit: %q is not an integer", s)
		}
		if n < 1 {
			return out, fmt.Errorf("invalid limit: %d is below the minimum of 1", n)
		}
		if n > MaxLimit {
			return out, fmt.Errorf("invalid limit: %d exceeds the maximum of %d", n, MaxLimit)
		}
		out.Limit = n
	}

	// sort: the "-" prefix is the direction, the rest must be a key this route
	// declares. The supported list in the message is spec.Sorts, so it is the
	// route's real vocabulary rather than a string that has to be remembered.
	if s := q.Get("sort"); s != "" {
		sortKey, desc := s, false
		if strings.HasPrefix(sortKey, "-") {
			sortKey, desc = sortKey[1:], true
		}
		if !slices.Contains(spec.Sorts, sortKey) {
			return out, fmt.Errorf("invalid sort: %q (supported: %s; prefix %q for descending)",
				s, strings.Join(spec.Sorts, ", "), "-")
		}
		out.Sort, out.Desc = sortKey, desc
	}

	// from/to: RFC 3339 per openapi's `format: date-time`. Routes that ignore
	// the values still reject a malformed one (see ListQuery's doc).
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"from", &out.From}, {"to", &out.To}} {
		s := q.Get(f.name)
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return out, fmt.Errorf("invalid %s: %q is not RFC 3339", f.name, s)
		}
		*f.dst = t.UTC()
	}

	// cursor: opaque to the client (R8.10), which is exactly why the server may
	// put whatever it needs in it — see pageCursor.
	if s := q.Get("cursor"); s != "" {
		c, err := decodeCursor(s)
		if err != nil {
			return out, fmt.Errorf("invalid cursor: %q is not a pgsi cursor", s)
		}
		// A cursor issued for another sort is meaningless here: page 2 of
		// ?sort=phase is not a subset of page 1 of ?sort=name, so accepting it
		// would silently skip or repeat rows. R8.10's "two consecutive pages
		// never overlap or drop a row" is what this check protects.
		if c.Sort != out.Sort {
			return out, fmt.Errorf("invalid cursor: issued for sort %q, not %q", c.Sort, out.Sort)
		}
		out.cursor = &c
	}

	return out, nil
}

// pageRows pages already-filtered rows into R8.4's envelope: sort, cut at the
// cursor, cut at the limit, mint the next cursor.
//
// It is generic over the row type and takes the row's sort-key function as a
// value, which is what makes one implementation cover every list: the only
// per-route answer is "what string does this row sort on", and that answer is
// the resource's own sortKey. Filtering is deliberately *not* here — a route
// filters on its own fields, and the caller passes the rows that matched so
// `meta.total` is the match count (D18's pre-cut ruling) rather than a number
// this function could only guess at.
func pageRows[T any](
	rows []T,
	q ListQuery,
	key func(T, string) string,
	base dto.Meta,
) dto.Envelope[T] {
	// A nil result set renders `data: []`, not `data: null`. R8.4's envelope
	// always carries an array and dto.Envelope's Data field has no omitempty, so
	// a nil slice is the one case where the JSON says something the Go type does
	// not: a client doing `data.length` gets undefined. clusters.go dodges it by
	// building rows with make(); every empty fallback source in this package
	// returns `nil, nil`, so the normalisation belongs here, once, rather than in
	// each route's wrapper — which is the whole argument for pageRows existing.
	if rows == nil {
		rows = []T{}
	}

	total := len(rows)

	sortRowsBy(rows, key, q.Sort, q.Desc)

	// Keyset paging: drop everything at or before the cursor's key. Comparing
	// against the key (rather than looking the row up by identity) is what makes
	// a row deleted between requests harmless — the rows after it still come
	// next instead of the cursor 404ing.
	if q.cursor != nil {
		for len(rows) > 0 && !rowAfterCursor(key(rows[0], q.Sort), q) {
			rows = rows[1:]
		}
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	truncated := len(rows) > limit
	page := rows
	if truncated {
		page = rows[:limit]
	}
	next := ""
	if truncated && len(page) > 0 {
		next = encodeCursor(q.Sort, key(page[len(page)-1], q.Sort))
	}

	env := dto.Envelope[T]{Data: page, Meta: base}
	env.Meta.Total = total
	env.Meta.Truncated = truncated
	env.Meta.NextCursor = next
	return env
}

// sortRowsBy orders rows by one key and direction, ascending then reversed.
// The generic form; a resource wraps it with its own key function (see
// clusters.go's sortRows) so callers keep the short per-resource signature.
func sortRowsBy[T any](rows []T, key func(T, string) string, sortKey string, desc bool) {
	sort.Slice(rows, func(i, j int) bool { return key(rows[i], sortKey) < key(rows[j], sortKey) })
	if desc {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
}

// rowAfterCursor reports whether a row's key belongs after the query's cursor,
// given the sort direction. Ascending means "greater than the cursor key";
// descending mirrors it. Both directions are the same comparison on the same
// key, so the cursor cannot mean two different things depending on which page
// asked — which is why it takes the key, not the row.
func rowAfterCursor(key string, q ListQuery) bool {
	if q.cursor == nil {
		return true
	}
	if q.Desc {
		return key < q.cursor.Key
	}
	return key > q.cursor.Key
}
