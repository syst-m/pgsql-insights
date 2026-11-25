// rollups_test.go is A3's S29 test file for rollups.go: R3.7's per-dimension
// rollups and R3.12's tags, both halves of the one dimension_rollup table.
//
// The five tests map to the file's five contracts rather than to its line count:
// digest (what a row's dim_hash is, including A3's S45's kind-scoping), validate
// (the gate: kind, presence, exclusivity), args (the 22 bind values and the column
// list they belong to), PutDimensionRollups (batching, INSERT OR REPLACE, and the
// validate-before-the-transaction rule), and Tags (the read: the kind filter, the
// window, the per-cluster denominator).
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/api/dto"
)

// The fixture's dimension values. One set of spellings shared by every test, so a
// row written by TestPutDimensionRollups and a row read back by TestTags are
// provably the same row rather than two tests that happen to agree on a string.
const (
	tagApp       = "checkout"
	tagDB        = "appdb"
	tagUser      = "appuser"
	tagAction    = "ListOrders"
	tagRoute     = "/v1/orders"
	tagControler = "OrderController"
	tagFramework = "gin"
	tagDriver    = "pgx"
	tagAddress   = "10.0.0.1"
)

// fullTag is the eight-key R3.12 tag with every key populated, as both a
// Dimensions and the dto.TagView that is its label half.
var (
	fullTag = Dimensions{
		Kind:        KindTag,
		Application: tagApp, Datname: tagDB, Usename: tagUser,
		Action: tagAction, Route: tagRoute, Controller: tagControler,
		Framework: tagFramework, DBDriver: tagDriver,
	}
	fullTagView = dto.TagView{
		Application: tagApp, Database: tagDB, User: tagUser,
		Action: tagAction, Route: tagRoute, Controller: tagControler,
		Framework: tagFramework, DBDriver: tagDriver,
	}
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestDimensionDigest covers what a row's dim_hash is for each of the five kinds.
//
// The contract under test is that the store never invents a second hash: four of
// the five kinds are dto.TagDigest of the right TagView, and the fifth is the
// address's own SHA-256. Every want below is computed in the test by calling the
// same function rollups.go calls, which is the only way to test "this file calls
// dto's function" without reading the file.
func TestDimensionDigest(t *testing.T) {
	tests := []struct {
		name string
		d    Dimensions
		want string
	}{
		{
			"database",
			Dimensions{Kind: KindDatabase, Datname: tagDB},
			dto.TagDigest(dto.TagView{Database: tagDB}),
		},
		{
			"user",
			Dimensions{Kind: KindUser, Usename: tagUser},
			dto.TagDigest(dto.TagView{User: tagUser}),
		},
		{
			"application",
			Dimensions{Kind: KindApplication, Application: tagApp},
			dto.TagDigest(dto.TagView{Application: tagApp}),
		},
		{"tag, all eight", fullTag, dto.TagDigest(fullTagView)},
		{
			"tag, two keys",
			Dimensions{Kind: KindTag, Action: tagAction, Route: tagRoute},
			dto.TagDigest(dto.TagView{Action: tagAction, Route: tagRoute}),
		},
		{
			"client address",
			Dimensions{Kind: KindClientAddress, ClientAddress: tagAddress},
			sha256Hex(tagAddress),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.d.digest()
			if got != tt.want {
				t.Errorf("digest() = %s, want %s", got, tt.want)
			}
			// The column holds one kind of string for every kind of row, and
			// validFingerprint is the store's own definition of that shape —
			// reused rather than re-spelled so the assertion cannot drift from
			// the check the write path applies.
			if !validFingerprint(got) {
				t.Errorf("digest %q is not the 64-lowercase-hex shape the column holds", got)
			}
		})
	}

	// A3's S45, stated as the hash rather than as the gate. A KindDatabase row is
	// a rollup of one database, so a caller that also filled in a usename must not
	// move the hash — before the fix it did, which is two dim_hash values for one
	// database and the series-splitting the function's own doc argues against.
	// (validate now refuses that row; TestDimensionsValidate covers the refusal,
	// this covers the hash for the rows that are still writable.)
	withStray := Dimensions{Kind: KindDatabase, Datname: tagDB, Usename: tagUser}
	if got, want := withStray.digest(), dto.TagDigest(dto.TagView{Database: tagDB}); got != want {
		t.Errorf("a database row carrying a stray usename digests to %s, want %s — "+
			"the hash must cover the kind's own fields only", got, want)
	}

	// The order the comment grammar lists keys in cannot move the hash. This is
	// dto.TagDigest's property, asserted from the store's side because it is the
	// reason rollups.go calls that function instead of hashing the struct.
	if a, b := (Dimensions{Kind: KindTag, Action: tagAction, Route: tagRoute}).digest(),
		(Dimensions{Kind: KindTag, Route: tagRoute, Action: tagAction}).digest(); a != b {
		t.Errorf("the same two keys in the other order digest to %s and %s", a, b)
	}

	// Two client addresses are two rows. Hashing the empty TagView instead — the
	// mistake the doc names — gives every address in the cluster one hash.
	if a, b := (Dimensions{Kind: KindClientAddress, ClientAddress: tagAddress}).digest(),
		(Dimensions{Kind: KindClientAddress, ClientAddress: "10.0.0.2"}).digest(); a == b {
		t.Errorf("two client addresses share the digest %s", a)
	}
}

// TestDimensionsValidate covers the write path's gate in all three of its halves:
// the kind is one of the five, the kind's own dimension is present, and nothing
// outside the kind's field set is populated.
func TestDimensionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		d       Dimensions
		wantErr string // "" means the row is accepted
	}{
		// Half one: the kind.
		{"unknown kind", Dimensions{Kind: "index"}, `store: unknown dimension kind "index"`},
		{"empty kind", Dimensions{}, `store: unknown dimension kind ""`},

		// Half two: the kind's own field must be there.
		{"database, no datname", Dimensions{Kind: KindDatabase}, `store: dimension kind "database" needs datname`},
		{"user, no usename", Dimensions{Kind: KindUser}, `store: dimension kind "user" needs usename`},
		{"application, no application", Dimensions{Kind: KindApplication}, `store: dimension kind "application" needs application`},
		{"client address, no address", Dimensions{Kind: KindClientAddress}, `store: dimension kind "client_address" needs client_address`},
		{"tag, nothing populated", Dimensions{Kind: KindTag}, `store: dimension kind "tag" needs at least one tag dimension`},

		// The five accepted shapes.
		{"database, clean", Dimensions{Kind: KindDatabase, Datname: tagDB}, ""},
		{"user, clean", Dimensions{Kind: KindUser, Usename: tagUser}, ""},
		{"application, clean", Dimensions{Kind: KindApplication, Application: tagApp}, ""},
		{"client address, clean", Dimensions{Kind: KindClientAddress, ClientAddress: tagAddress}, ""},
		{"tag, one key", Dimensions{Kind: KindTag, Route: tagRoute}, ""},
		{"tag, all eight", fullTag, ""},

		// Half three (A3's S45): a field the kind does not own. Each case names a
		// field that is legal for some other kind, which is the shape the bug took —
		// a caller filling in a dimension the row's kind never asked for.
		{
			"database carrying a user",
			Dimensions{Kind: KindDatabase, Datname: tagDB, Usename: tagUser},
			`store: dimension kind "database" cannot carry user`,
		},
		{
			"user carrying a database",
			Dimensions{Kind: KindUser, Usename: tagUser, Datname: tagDB},
			`store: dimension kind "user" cannot carry database`,
		},
		{
			"application carrying a route",
			Dimensions{Kind: KindApplication, Application: tagApp, Route: tagRoute},
			`store: dimension kind "application" cannot carry route`,
		},
		{
			"client address carrying a database",
			Dimensions{Kind: KindClientAddress, ClientAddress: tagAddress, Datname: tagDB},
			`store: dimension kind "client_address" cannot carry database`,
		},
		{
			"tag carrying a client address",
			Dimensions{Kind: KindTag, Route: tagRoute, ClientAddress: tagAddress},
			`store: dimension kind "tag" cannot carry client_address`,
		},
		{
			"tag carrying all eight plus an address",
			Dimensions{
				Kind:        KindTag,
				Application: tagApp, Datname: tagDB, Usename: tagUser,
				Action: tagAction, Route: tagRoute, Controller: tagControler,
				Framework: tagFramework, DBDriver: tagDriver,
				ClientAddress: tagAddress,
			},
			`store: dimension kind "tag" cannot carry client_address`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.d.validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("validate() = nil, want %q", tt.wantErr)
			case tt.wantErr != "" && err.Error() != tt.wantErr:
				t.Errorf("validate() = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestDimensionRollupArgs covers the row's bind values against the SQL's column
// list — the one place a mismatch is catchable without a database.
func TestDimensionRollupArgs(t *testing.T) {
	bucket := timeFromUnix(minuteBucket(seriesNow.Add(-time.Hour)))
	r := DimensionRollup{
		Dimensions: fullTag,
		Namespace:  "prod",
		Cluster:    "pg",
		Bucket:     bucket,
		Load: DimensionLoad{
			Calls: 7, TotalExecMs: 70, CPUMs: 20, IOWaitMs: 5,
			RowsReturned: 70, SharedBlocksHit: 6, SharedBlocksRead: 4, LockWaitMs: 3,
		},
	}
	args := r.args()

	// The placeholder count and the arg count are one contract. A column added to
	// upsertDimensionSQL without its entry in args() — or the reverse — is a
	// runtime "sql: expected 22 arguments, include 21" on every write, and this
	// assertion is the only place it can be caught before one runs.
	if got, want := strings.Count(upsertDimensionSQL, "?"), len(args); got != want {
		t.Fatalf("upsertDimensionSQL has %d placeholders, args() returns %d values", got, want)
	}

	// The order is the SQL's column order, not the struct's field order, and the
	// two differ: the SQL groups the label columns (kind, namespace, cluster,
	// dim_hash, then the seven tag fields and the address) before the bucket, while
	// the struct interleaves them. Every value here is distinct, so any swap of two
	// adjacent entries is visible.
	want := []any{
		"tag", "prod", "pg", r.digest(),
		tagApp, tagDB, tagUser,
		tagAction, tagRoute, tagControler, tagFramework, tagDriver,
		"", // client_address: a tag row has none, and args writes it anyway
		unix(bucket),
		int64(7), 70.0, 20.0, 5.0, 70.0, 6.0, 4.0, 3.0,
	}
	if len(args) != len(want) {
		t.Fatalf("args() returned %d values, want %d", len(args), len(want))
	}
	for i := range want {
		if got, w := fmt.Sprint(args[i]), fmt.Sprint(want[i]); got != w {
			t.Errorf("args()[%d] = %s, want %s", i, got, w)
		}
	}

	// bucket_kind is the one column with no placeholder: the const writes the
	// 'minute' literal, so no arg carries a bucket-kind string.
	if !strings.Contains(upsertDimensionSQL, "'minute'") {
		t.Error("upsertDimensionSQL no longer writes bucket_kind as the 'minute' literal")
	}
	for i, a := range args {
		if s, ok := a.(string); ok && s == "minute" {
			t.Errorf("args()[%d] carries %q — bucket_kind has no placeholder", i, s)
		}
	}
}

// TestPutDimensionRollups covers the batch write: the empty batch, one row per
// rollup, INSERT OR REPLACE's replace-not-sum, and the validate-before-the-open
// rule that makes a bad row cost the whole batch rather than the rows before it.
func TestPutDimensionRollups(t *testing.T) {
	ctx := context.Background()
	b0 := minuteBucket(seriesNow.Add(-time.Hour))

	// rollup is the fixture's DimensionRollup: the given dimensions in prod/pg at
	// the given minute with the given load.
	rollup := func(d Dimensions, bucket int64, calls int64, totalMs float64) DimensionRollup {
		return DimensionRollup{
			Dimensions: d,
			Namespace:  "prod",
			Cluster:    "pg",
			Bucket:     timeFromUnix(bucket),
			Load:       DimensionLoad{Calls: calls, TotalExecMs: totalMs},
		}
	}
	dbRollup := func(db string, calls int64, totalMs float64) DimensionRollup {
		return rollup(Dimensions{Kind: KindDatabase, Datname: db}, b0, calls, totalMs)
	}

	t.Run("an empty batch is not an error", func(t *testing.T) {
		s := openMem(t, seriesNow)
		if err := s.PutDimensionRollups(ctx, nil); err != nil {
			t.Fatalf("PutDimensionRollups(nil) = %v, want nil", err)
		}
		if n := countRows(t, s, "dimension_rollup"); n != 0 {
			t.Errorf("%d rows written for an empty batch, want 0", n)
		}
	})

	t.Run("a batch writes one row per rollup", func(t *testing.T) {
		s := openMem(t, seriesNow)
		rows := []DimensionRollup{
			dbRollup("appdb", 10, 100),
			rollup(Dimensions{Kind: KindUser, Usename: tagUser}, b0, 4, 40),
			rollup(fullTag, b0, 2, 20),
		}
		if err := s.PutDimensionRollups(ctx, rows); err != nil {
			t.Fatalf("PutDimensionRollups: %v", err)
		}
		if n := countRows(t, s, "dimension_rollup"); n != 3 {
			t.Errorf("%d rows written, want 3", n)
		}
		// Every row's stored hash is the one its dimensions produce, which is the
		// write path's whole job: the PK column comes from digest(), not from a
		// second computation at the call site.
		for _, r := range rows {
			if got := storedHash(t, s, r); got != r.digest() {
				t.Errorf("%s row in %s/%s: stored dim_hash %s, want %s",
					r.Kind, r.Namespace, r.Cluster, got, r.digest())
			}
		}
	})

	t.Run("re-writing a minute replaces it", func(t *testing.T) {
		s := openMem(t, seriesNow)
		if err := s.PutDimensionRollups(ctx, []DimensionRollup{dbRollup("appdb", 10, 100)}); err != nil {
			t.Fatalf("first write: %v", err)
		}
		if err := s.PutDimensionRollups(ctx, []DimensionRollup{dbRollup("appdb", 3, 30)}); err != nil {
			t.Fatalf("second write: %v", err)
		}
		if n := countRows(t, s, "dimension_rollup"); n != 1 {
			t.Fatalf("%d rows after re-writing one minute, want 1 (INSERT OR REPLACE)", n)
		}
		// The second write's numbers, not the two summed: a rollup row is that
		// minute's aggregate, so summing would double-count it on every re-run.
		calls, totalMs := storedLoad(t, s, dbRollup("appdb", 0, 0))
		if calls != 3 || !sameFloat(totalMs, 30) {
			t.Errorf("stored load = %d calls / %v ms, want 3 calls / 30 ms (the second write, not 13 / 130)",
				calls, totalMs)
		}
	})

	t.Run("the same tag in two clusters is two rows", func(t *testing.T) {
		// A3's S27 fix on schema.go put namespace and cluster in the PK so one tag seen
		// in two clusters is two rows. Both rows carry one dim_hash, which is what
		// Dimensions' doc means by "one tag's dimensions can be computed once and
		// written to each cluster it was seen in".
		s := openMem(t, seriesNow)
		rows := []DimensionRollup{
			{Dimensions: fullTag, Namespace: "prod", Cluster: "pg", Bucket: timeFromUnix(b0),
				Load: DimensionLoad{Calls: 2, TotalExecMs: 20}},
			{Dimensions: fullTag, Namespace: "prod", Cluster: "pg2", Bucket: timeFromUnix(b0),
				Load: DimensionLoad{Calls: 5, TotalExecMs: 50}},
		}
		if err := s.PutDimensionRollups(ctx, rows); err != nil {
			t.Fatalf("PutDimensionRollups: %v", err)
		}
		if n := countRows(t, s, "dimension_rollup"); n != 2 {
			t.Fatalf("%d rows, want 2 — one tag in two clusters is not one row", n)
		}
		if a, b := storedHash(t, s, rows[0]), storedHash(t, s, rows[1]); a != b {
			t.Errorf("the same tag in two clusters has two hashes: %s and %s", a, b)
		}
	})

	t.Run("a bad row in the batch writes nothing", func(t *testing.T) {
		s := openMem(t, seriesNow)
		if err := s.PutDimensionRollups(ctx, []DimensionRollup{dbRollup("first", 1, 10)}); err != nil {
			t.Fatalf("seed write: %v", err)
		}
		// The second batch is [valid, valid, invalid]. Validation runs over the
		// whole batch before the transaction opens, so the two good rows in front
		// of the bad one must not reach the table.
		bad := Dimensions{Kind: KindDatabase} // no datname
		err := s.PutDimensionRollups(ctx, []DimensionRollup{
			dbRollup("second", 1, 10), dbRollup("third", 1, 10), rollup(bad, b0, 1, 10),
		})
		if err == nil {
			t.Fatal("PutDimensionRollups accepted a batch with an invalid row")
		}
		want := "store: put dimension rollups: store: dimension kind \"database\" needs datname"
		if err.Error() != want {
			t.Errorf("error = %v, want %q", err, want)
		}
		if n := countRows(t, s, "dimension_rollup"); n != 1 {
			t.Errorf("%d rows after a rejected batch, want 1 (only the earlier write) — "+
				"validation must run before the transaction opens", n)
		}
	})
}

// storedHash reads back the dim_hash a row was written under, by the rest of its
// PK. Asserting against the table rather than against a recomputed value is what
// makes the test about the write.
func storedHash(t *testing.T, s *Store, r DimensionRollup) string {
	t.Helper()
	var got string
	err := s.DB().QueryRow(
		`SELECT dim_hash FROM dimension_rollup WHERE kind = ? AND namespace = ? AND cluster = ?`,
		r.Kind, r.Namespace, r.Cluster).Scan(&got)
	if err != nil {
		t.Fatalf("read dim_hash for %s in %s/%s: %v", r.Kind, r.Namespace, r.Cluster, err)
	}
	return got
}

// storedLoad reads back the two load columns a row carries, for the replace-vs-sum
// assertion. The load fields the test leaves at zero are not asserted on.
func storedLoad(t *testing.T, s *Store, r DimensionRollup) (int64, float64) {
	t.Helper()
	var calls int64
	var totalMs float64
	err := s.DB().QueryRow(
		`SELECT calls, total_exec_ms FROM dimension_rollup
		  WHERE kind = ? AND namespace = ? AND cluster = ? AND dim_hash = ? AND bucket = ?`,
		r.Kind, r.Namespace, r.Cluster, r.digest(), unix(r.Bucket)).Scan(&calls, &totalMs)
	if err != nil {
		t.Fatalf("read load for %s in %s/%s: %v", r.Kind, r.Namespace, r.Cluster, err)
	}
	return calls, totalMs
}

// TestTags covers R8.2's /insights/tags read: which rows it returns (kind='tag',
// bucket_kind='minute', inside the window, inside the requested scope), how it
// aggregates them (the window's minutes summed into one row per tag), and the
// denominator every percentage on the DTO is computed against.
func TestTags(t *testing.T) {
	ctx := context.Background()
	b0 := minuteBucket(seriesNow.Add(-time.Hour))
	b1 := minuteBucket(seriesNow)

	// The denominator is query_rollup's — clusterLoad sums that table, not
	// dimension_rollup — so the fixture seeds the query side too: prod/pg carries
	// 200 ms of query load (two rows, both at b0) and prod/pg2 carries 50 ms.
	// Without these the percentages are all zero and the test proves nothing about
	// which denominator a row was divided by.
	s := newSeriesStore(t,
		seriesSeed{fp: fpNamed("aa"), namespace: "prod", cluster: "pg", pod: "pg-1",
			datname: tagDB, usename: tagUser, kind: "minute", bucket: b0,
			calls: 10, totalMs: 100, rows: 100, hit: 6, read: 2, lockMs: 4},
		seriesSeed{fp: fpNamed("bb"), namespace: "prod", cluster: "pg", pod: "pg-1",
			datname: tagDB, usename: tagUser, kind: "minute", bucket: b0,
			calls: 5, totalMs: 100, rows: 50, hit: 6, read: 2, lockMs: 4},
		seriesSeed{fp: fpNamed("cc"), namespace: "prod", cluster: "pg2", pod: "pg-1",
			datname: tagDB, usename: tagUser, kind: "minute", bucket: b0,
			calls: 4, totalMs: 50, rows: 40, hit: 6, read: 2, lockMs: 4},
	)

	// The dimension side. T1 is the same tag in two clusters (one dim_hash, two
	// rows — A3's S27 shape), T2 is a second tag in prod/pg, T3 is a tag in a
	// cluster with no query load at all, and the database-kind row is what the
	// kind filter has to keep out.
	t1 := Dimensions{
		Kind: KindTag, Application: tagApp, Datname: tagDB, Usename: tagUser,
		Action: tagAction, Route: tagRoute,
	}
	t2 := Dimensions{Kind: KindTag, Application: "billing", Datname: "ledger", Usename: tagUser, Route: "/v1/users"}
	t3 := Dimensions{Kind: KindTag, Route: "/v1/health"}
	if err := s.PutDimensionRollups(ctx, []DimensionRollup{
		{Dimensions: t1, Namespace: "prod", Cluster: "pg", Bucket: timeFromUnix(b0),
			Load: DimensionLoad{Calls: 10, TotalExecMs: 50, CPUMs: 20, IOWaitMs: 5,
				RowsReturned: 100, SharedBlocksHit: 7, SharedBlocksRead: 3, LockWaitMs: 2}},
		{Dimensions: t1, Namespace: "prod", Cluster: "pg", Bucket: timeFromUnix(b1),
			Load: DimensionLoad{Calls: 5, TotalExecMs: 25, CPUMs: 10, IOWaitMs: 1,
				RowsReturned: 50, SharedBlocksHit: 4, SharedBlocksRead: 1, LockWaitMs: 1}},
		{Dimensions: t1, Namespace: "prod", Cluster: "pg2", Bucket: timeFromUnix(b0),
			Load: DimensionLoad{Calls: 4, TotalExecMs: 12.5, CPUMs: 5, IOWaitMs: 1,
				RowsReturned: 30, SharedBlocksHit: 3, SharedBlocksRead: 1, LockWaitMs: 0}},
		{Dimensions: t2, Namespace: "prod", Cluster: "pg", Bucket: timeFromUnix(b0),
			Load: DimensionLoad{Calls: 2, TotalExecMs: 25, CPUMs: 8, IOWaitMs: 2,
				RowsReturned: 20, SharedBlocksHit: 5, SharedBlocksRead: 1, LockWaitMs: 1}},
		{Dimensions: t3, Namespace: "prod", Cluster: "pg3", Bucket: timeFromUnix(b0),
			Load: DimensionLoad{Calls: 1, TotalExecMs: 5, CPUMs: 1, IOWaitMs: 0,
				RowsReturned: 1, SharedBlocksHit: 1, SharedBlocksRead: 0, LockWaitMs: 0}},
		// R3.7's database rollup: a row in the table that Tags must not return.
		{Dimensions: Dimensions{Kind: KindDatabase, Datname: tagDB}, Namespace: "prod",
			Cluster: "pg", Bucket: timeFromUnix(b0), Load: DimensionLoad{Calls: 3, TotalExecMs: 30}},
	}); err != nil {
		t.Fatalf("seed dimension rollups: %v", err)
	}

	// tags is the one-line Tags call every case makes.
	tags := func(q api.ListQuery) []dto.TagView {
		t.Helper()
		out, err := s.Tags(ctx, q)
		if err != nil {
			t.Fatalf("Tags(%+v): %v", q, err)
		}
		return out
	}
	// oneTag asks with q and returns the single row for the given dimensions.
	// The scope filter is what names the row: TagView carries no namespace or
	// cluster of its own, so keying a map by dim_hash collapses one tag's
	// per-cluster rows into whichever the query returned last — the exact shape
	// the per-cluster denominator test is about.
	oneTag := func(q api.ListQuery, d Dimensions) dto.TagView {
		t.Helper()
		var found []dto.TagView
		for _, v := range tags(q) {
			if v.TagHash == d.digest() {
				found = append(found, v)
			}
		}
		if len(found) != 1 {
			t.Fatalf("Tags(%+v) returned %d rows for %s, want 1", q, len(found), d.digest())
		}
		return found[0]
	}

	t.Run("only tag rows come back", func(t *testing.T) {
		out := tags(api.ListQuery{})
		if len(out) != 4 {
			t.Fatalf("%d tags, want 4 — the fixture has 4 tag rows and 1 database row", len(out))
		}
		for _, v := range out {
			if v.TagHash == (Dimensions{Kind: KindDatabase, Datname: tagDB}).digest() {
				t.Errorf("the database-kind row appeared in the tags result")
			}
		}
	})

	t.Run("the window's minutes are summed into one row", func(t *testing.T) {
		got := oneTag(api.ListQuery{Cluster: "pg"}, t1)
		// b0 and b1 summed: 10+5 calls, 50+25 ms, 20+10 cpu, 5+1 io, 100+50 rows.
		if got.LoadColumns.TimesCalled != 15 || !sameFloat(got.LoadColumns.TotalExecTimeMs, 75) {
			t.Errorf("load = %d calls / %v ms, want 15 calls / 75 ms (two minutes summed)",
				got.LoadColumns.TimesCalled, got.LoadColumns.TotalExecTimeMs)
		}
		if !sameFloat(got.LoadColumns.AvgExecTimeMs, 5) || !sameFloat(got.LoadColumns.AvgRowsReturned, 10) {
			t.Errorf("averages = %v ms / %v rows, want 5 ms / 10 rows (75/15 and 150/15)",
				got.LoadColumns.AvgExecTimeMs, got.LoadColumns.AvgRowsReturned)
		}
		if !sameFloat(got.LoadColumns.LoadByCPUMs, 30) || !sameFloat(got.LoadColumns.LoadByIOWaitMs, 6) {
			t.Errorf("cpu/io = %v / %v, want 30 / 6",
				got.LoadColumns.LoadByCPUMs, got.LoadColumns.LoadByIOWaitMs)
		}
		// LockWaitMs is the column R3.2 withholds per query and R3.7 grants at
		// this level; the TagView field is the row's own, passed through undivided.
		if !sameFloat(got.LockWaitMs, 3) {
			t.Errorf("lockWaitMs = %v, want 3 (2+1, the two minutes)", got.LockWaitMs)
		}
		// The labels come from the row, not from the request: Tags has no way to
		// echo them the way QuerySeries does.
		if got.Route != tagRoute || got.Action != tagAction || got.Application != tagApp ||
			got.Database != tagDB || got.User != tagUser {
			t.Errorf("labels = %s/%s/%s/%s/%s, want %s/%s/%s/%s/%s",
				got.Application, got.Database, got.User, got.Action, got.Route,
				tagApp, tagDB, tagUser, tagAction, tagRoute)
		}
	})

	t.Run("the percentage is against the row's own cluster", func(t *testing.T) {
		// prod/pg carries 200 ms of query load, and t1 there accounts for 75 ms.
		got := oneTag(api.ListQuery{Cluster: "pg"}, t1)
		if !sameFloat(got.LoadColumns.LoadByTotalTime, 37.5) ||
			!sameFloat(got.LoadColumns.PctLoadByQueries, 37.5) {
			t.Errorf("t1 in prod/pg: loadByTotalTime/pctLoadByQueries = %v/%v, want 37.5/37.5 (100 * 75 / 200)",
				got.LoadColumns.LoadByTotalTime, got.LoadColumns.PctLoadByQueries)
		}
		// The same tag in prod/pg2, which carries 50 ms of its own query load and
		// 12.5 ms of t1's, so 25%. Pooling the two clusters (250 ms) would answer
		// 5% for both rows — the number that belongs to neither, which is why the
		// GROUP BY carries the cluster and why one tag is two rows.
		got = oneTag(api.ListQuery{Cluster: "pg2"}, t1)
		if !sameFloat(got.LoadColumns.LoadByTotalTime, 25) {
			t.Errorf("t1 in prod/pg2: loadByTotalTime = %v, want 25 (100 * 12.5 / 50)",
				got.LoadColumns.LoadByTotalTime)
		}
	})

	t.Run("a cluster with no query load answers 0 percent", func(t *testing.T) {
		// prod/pg3 has dimension rows and no query_rollup rows, so the denominator
		// is absent. loadColumns answers 0 rather than dividing by zero, and the
		// row's own load still reports honestly.
		got := oneTag(api.ListQuery{Cluster: "pg3"}, t3)
		if !sameFloat(got.LoadColumns.LoadByTotalTime, 0) {
			t.Errorf("loadByTotalTime = %v, want 0 (no query load in prod/pg3)", got.LoadColumns.LoadByTotalTime)
		}
		if !sameFloat(got.LoadColumns.TotalExecTimeMs, 5) {
			t.Errorf("totalExecTimeMs = %v, want 5 — the row's own load is not the denominator",
				got.LoadColumns.TotalExecTimeMs)
		}
	})

	t.Run("the scope filters pick which rows answer", func(t *testing.T) {
		// The three optional columns, and the pairs. Every want is a count the
		// other cases cannot produce, so a filter the query dropped shows up as a
		// wrong number in at least one row of this table.
		tests := []struct {
			q    api.ListQuery
			want int
		}{
			{api.ListQuery{}, 4},
			{api.ListQuery{Namespace: "prod"}, 4},
			{api.ListQuery{Namespace: "staging"}, 0},
			{api.ListQuery{Cluster: "pg"}, 2},
			{api.ListQuery{Cluster: "pg2"}, 1},
			{api.ListQuery{Cluster: "pg3"}, 1},
			{api.ListQuery{DB: tagDB}, 2},
			{api.ListQuery{DB: "ledger"}, 1},
			{api.ListQuery{Cluster: "pg", DB: tagDB}, 1},
			{api.ListQuery{Namespace: "prod", Cluster: "pg", DB: "ledger"}, 1},
		}
		for _, tt := range tests {
			name := fmt.Sprintf("%s|%s|%s", tt.q.Namespace, tt.q.Cluster, tt.q.DB)
			t.Run(name, func(t *testing.T) {
				if got := len(tags(tt.q)); got != tt.want {
					t.Errorf("%d tags, want %d", got, tt.want)
				}
			})
		}
	})

	t.Run("the window bounds which minutes are summed", func(t *testing.T) {
		// A window of exactly b0 keeps one of t1's two minutes: 10 calls / 50 ms,
		// and the denominator (both prod/pg query rows are at b0) stays 200 ms, so
		// the percentage moves for one reason.
		got := oneTag(api.ListQuery{Cluster: "pg", From: timeFromUnix(b0), To: timeFromUnix(b0)}, t1)
		if got.LoadColumns.TimesCalled != 10 || !sameFloat(got.LoadColumns.TotalExecTimeMs, 50) {
			t.Errorf("t1 in a b0-only window = %d calls / %v ms, want 10 / 50",
				got.LoadColumns.TimesCalled, got.LoadColumns.TotalExecTimeMs)
		}
		if !sameFloat(got.LoadColumns.LoadByTotalTime, 25) {
			t.Errorf("loadByTotalTime = %v, want 25 (100 * 50 / 200)", got.LoadColumns.LoadByTotalTime)
		}
		// A window the rows are not in answers with nothing, not with zeros.
		// A window of exactly b1 keeps only t1's second minute — and it is the only
		// row in the fixture at that minute, so the answer is one row, not four.
		if out := tags(api.ListQuery{From: timeFromUnix(b1), To: timeFromUnix(b1)}); len(out) != 1 {
			t.Errorf("a b1-only window returned %d tags, want 1", len(out))
		}
	})

	t.Run("an empty store returns no tags", func(t *testing.T) {
		empty := openMem(t, seriesNow)
		out, err := empty.Tags(ctx, api.ListQuery{})
		if err != nil {
			t.Fatalf("Tags on an empty store: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("%d tags on an empty store, want 0", len(out))
		}
	})
}

// TestR313WriteGate covers the two write gates R3.13 names — --record-client-address
// and --application-tag-source — through the write path rather than by calling
// Config.filterRows directly, so the test covers the wiring in
// PutDimensionRollups (store.go:220 is the predicate) and not only the predicate.
//
// Each case writes one row of every kind and counts what the table kept, which
// makes the row count a statement about kinds: five kinds in, N kinds out.
func TestR313WriteGate(t *testing.T) {
	ctx := context.Background()
	b0 := minuteBucket(seriesNow.Add(-time.Hour))

	// One row of each of the five kinds rollups.go's validKind accepts.
	all := []Dimensions{
		{Kind: KindDatabase, Datname: tagDB},
		{Kind: KindUser, Usename: tagUser},
		{Kind: KindApplication, Application: tagApp},
		{Kind: KindClientAddress, ClientAddress: tagAddress},
		{Kind: KindTag, Route: tagRoute},
	}

	open := func(t *testing.T, cfg Config) *Store {
		t.Helper()
		cfg.Now = func() time.Time { return seriesNow }
		s, err := Open(cfg)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	// kinds returns the distinct kinds the table holds, sorted by the query.
	kinds := func(t *testing.T, s *Store) []string {
		t.Helper()
		rows, err := s.db.QueryContext(ctx, `SELECT kind FROM dimension_rollup ORDER BY kind`)
		if err != nil {
			t.Fatalf("select kinds: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatalf("scan kind: %v", err)
			}
			out = append(out, k)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate kinds: %v", err)
		}
		return out
	}

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			// The default posture, which is Cloud SQL's: addresses off, application
			// recorded. The row a team with an IP-privacy requirement loses is the
			// address row, and it is the only one missing here.
			name: "defaults drop the address row only",
			want: []string{"application", "database", "tag", "user"},
		},
		{
			name: "--record-client-address=true writes the address row",
			cfg:  Config{RecordClientAddress: true},
			want: []string{"application", "client_address", "database", "tag", "user"},
		},
		{
			name: "--application-tag-source=off drops the application row",
			cfg:  Config{ApplicationTagSource: ApplicationTagOff},
			want: []string{"database", "tag", "user"},
		},
		{
			// The two non-off values differ in where the application value comes
			// from, not in whether the row is written; only `off` changes the set.
			name: "--application-tag-source=query_comment still writes it",
			cfg:  Config{ApplicationTagSource: ApplicationTagFromQueryComment},
			want: []string{"application", "database", "tag", "user"},
		},
		{
			name: "both halves on writes all five kinds",
			cfg: Config{RecordClientAddress: true,
				ApplicationTagSource: ApplicationTagFromApplicationName},
			want: []string{"application", "client_address", "database", "tag", "user"},
		},
		{
			name: "address on, application off writes four",
			cfg: Config{RecordClientAddress: true,
				ApplicationTagSource: ApplicationTagOff},
			want: []string{"client_address", "database", "tag", "user"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := open(t, tt.cfg)
			rows := make([]DimensionRollup, 0, len(all))
			for _, d := range all {
				rows = append(rows, DimensionRollup{
					Dimensions: d,
					Namespace:  "prod",
					Cluster:    "pg",
					Bucket:     timeFromUnix(b0),
					Load:       DimensionLoad{Calls: 1, TotalExecMs: 1},
				})
			}
			if err := s.PutDimensionRollups(ctx, rows); err != nil {
				t.Fatalf("PutDimensionRollups: %v", err)
			}
			got := kinds(t, s)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("kinds written = %v, want %v", got, tt.want)
			}
		})
	}
}
