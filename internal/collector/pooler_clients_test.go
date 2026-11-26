package collector

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"
)

// The test file for the S6 half of §3.3's pooler row. It reuses the package's
// shared fakeOpener/fakeTx (database_test.go:25 and :56) rather than minting a
// third fake, because the fakes' value is that they record what they were asked for
// — and the assertion this collector most needs is "it asked for SHOW CLIENTS and
// nothing else", which only a recording fake can answer.

var _ Collector = (*PoolerClientsCollector)(nil)

// showClientsFields is pgbouncer's documented SHOW CLIENTS column set, paired with
// the Client field each one lands in.
//
// This table is the test's authority, and it is copied from the source rather than
// from the parser: pgbouncer.org/usage.html, "SHOW CLIENTS" (fetched 2026-11-24;
// the changelog's current release that day is 1.26.1). That matters more here than
// anywhere else in the package, because this collector's parsing is a 21-line
// field-by-field copy with no logic in it — a fixture built from the parser's own
// output would test the copy against itself and pass on a table with the columns
// shuffled. Building the fixture from the documented list, and asserting the struct
// against the same list, is what makes the copy checkable: a column the parser
// forgets, misnames, or maps to the wrong field fails here even though the parser
// and the fixture would otherwise agree with each other.
var showClientsFields = []struct{ column, field string }{
	{"type", "Type"},
	{"user", "User"},
	{"database", "Database"},
	{"replication", "Replication"},
	{"state", "State"},
	{"addr", "Addr"},
	{"port", "Port"},
	{"local_addr", "LocalAddr"},
	{"local_port", "LocalPort"},
	{"connect_time", "ConnectTime"},
	{"request_time", "RequestTime"},
	{"wait", "Wait"},
	{"wait_us", "WaitUs"},
	{"close_needed", "CloseNeeded"},
	{"ptr", "Ptr"},
	{"link", "Link"},
	{"remote_pid", "RemotePid"},
	{"tls", "TLS"},
	{"application_name", "ApplicationName"},
	{"prepared_statements", "PreparedStatements"},
	{"id", "ID"},
}

// poolerClientsTarget is the S6 target shape: a pooler pod addressed on 5432
// rather than on the exporter's 9127.
func poolerClientsTarget() Target {
	return Target{
		Namespace: "db",
		Cluster:   "pg",
		Pod:       "pg-pooler-7d9-aaa",
		Address:   "10.42.1.21:5432",
		Source:    SourcePoolerRuntime,
	}
}

// clientsRowFromTable builds one Row carrying every documented column, each with a
// value derived from its own name.
//
// The value is "v_" + the column name so that an assertion on a field is an
// assertion about which column it came from: a parser that reads "wait" into
// WaitUs is caught by the value saying "v_wait", which a fixture of all-ones is not.
// `over` replaces named columns, which is how a test sets an address without
// restating the other twenty.
func clientsRowFromTable(over map[string]string) Row {
	row := Row{}
	for _, f := range showClientsFields {
		row[f.column] = "v_" + f.column
	}
	for k, v := range over {
		row[k] = v
	}
	return row
}

// poolerClientsFixture wires the collector to a fake opener serving rows for the
// one query it is supposed to issue.
func poolerClientsFixture(rows []Row) (*PoolerClientsCollector, *fakeOpener, *fakeTx) {
	tx := &fakeTx{rows: map[string][]Row{showClientsQuery: rows}}
	opener := &fakeOpener{tx: tx}
	return NewPoolerClientsCollector(opener, SQLConfig{}), opener, tx
}

// TestShowClientsTableIsTheDocumentedSet pins the table itself: 21 columns, no
// duplicates. The count is the number pgbouncer's docs give; a duplicate would make
// the count lie without changing it, so both are checked.
func TestShowClientsTableIsTheDocumentedSet(t *testing.T) {
	if len(showClientsFields) != 21 {
		t.Errorf("showClientsFields has %d rows, want 21 (pgbouncer.org/usage.html's SHOW CLIENTS list)", len(showClientsFields))
	}
	seen := map[string]bool{}
	for _, f := range showClientsFields {
		if f.column == "" || f.field == "" {
			t.Errorf("table row %+v has an empty column or field", f)
			continue
		}
		if seen[f.column] {
			t.Errorf("column %q appears twice in the table", f.column)
		}
		seen[f.column] = true
	}
}

// TestClientStructFieldSetMatchesDocumentedColumns is the check that the struct and
// the table describe the same 21 values.
//
// It exists because the parser is a copy and the struct is a declaration, and the
// two can disagree without either one failing to compile: a field added to Client
// with no column behind it silently returns "" forever, and a column dropped from
// the struct silently stops being read. Both are caught by comparing the reflected
// field set against the table's field set.
func TestClientStructFieldSetMatchesDocumentedColumns(t *testing.T) {
	want := make([]string, 0, len(showClientsFields))
	for _, f := range showClientsFields {
		want = append(want, f.field)
	}
	sort.Strings(want)

	var got []string
	rt := reflect.TypeOf(Client{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.String {
			t.Errorf("field %s is %s, want string (every SHOW CLIENTS column is a string here; struct doc)", f.Name, f.Type.Kind())
		}
		got = append(got, f.Name)
	}
	sort.Strings(got)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Client fields =\n %v\nwant (from the documented column table)\n %v", got, want)
	}
}

// TestClientFromRowMapsEveryDocumentedColumn is the one that tests the copy.
//
// Every field of the parsed Client must hold the value its column carried in the
// row — checked by reflection over the struct and lookup through the table, so the
// assertion covers all 21 fields including the twelve the struct leaves undocumented
// (Wait through ID), which are exactly the fields a hand-written per-field test
// would forget.
func TestClientFromRowMapsEveryDocumentedColumn(t *testing.T) {
	row := clientsRowFromTable(nil)
	got, ok := clientFromRow(row)
	if !ok {
		t.Fatalf("clientFromRow(full row) ok = false, want true")
	}

	v := reflect.ValueOf(got)
	rt := v.Type()
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		var want string
		for _, f := range showClientsFields {
			if f.field == name {
				want = row[f.column]
			}
		}
		if want == "" {
			t.Errorf("field %s is not in the documented table, so its value cannot be asserted", name)
			continue
		}
		if f := v.Field(i).String(); f != want {
			t.Errorf("Client.%s = %q, want %q (the value column %q carried)", name, f, want, columnForField(name))
		}
	}
}

// columnForField inverts the table for an error message, so a failure names the
// column to go look at rather than only the field.
func columnForField(field string) string {
	for _, f := range showClientsFields {
		if f.field == field {
			return f.column
		}
	}
	return "?"
}

func TestPoolerClientsDescriptor(t *testing.T) {
	c := NewPoolerClientsCollector(&fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}, SQLConfig{})
	d := c.Descriptor()

	if d.Name != CollectorPoolerClients {
		t.Errorf("Name = %q, want %q", d.Name, CollectorPoolerClients)
	}
	// The interval is pooler.go's constant, not a new one: §3.3's row has one
	// interval for both pooler collectors and pooler.go:40-43 says so in the
	// constant's own docblock. Asserting equality against the constant is the
	// check; a literal 60s here would pass even if PoolerInterval moved.
	if d.Interval != PoolerInterval {
		t.Errorf("Interval = %s, want PoolerInterval (%s) — the two pooler collectors share §3.3's one interval", d.Interval, PoolerInterval)
	}
	if d.Source != SourcePoolerRuntime {
		t.Errorf("Source = %q, want %q (S6)", d.Source, SourcePoolerRuntime)
	}
	if d.Cost != CostStandard {
		t.Errorf("Cost = %q, want %q (CostCheap is defined as costing no SQL round-trip at all; this costs one)", d.Cost, CostStandard)
	}
	if d.AppliesTo != nil {
		t.Errorf("AppliesTo = %v, want nil (pgbouncer output varies by pgbouncer version, not PG major)", d.AppliesTo)
	}
	if d.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0 (R2.1's default; the Descriptor rule says a cheap collector does not set a shorter one)", d.Timeout)
	}
	wantGroups := []string{poolerClientsViewGroup}
	if !reflect.DeepEqual(d.ViewGroups, wantGroups) {
		t.Errorf("ViewGroups = %v, want %v", d.ViewGroups, wantGroups)
	}
	if poolerClientsViewGroup != CollectorPoolerClients {
		t.Errorf("poolerClientsViewGroup = %q, want it to equal the collector name %q (the convention every SQL collector follows)",
			poolerClientsViewGroup, CollectorPoolerClients)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Descriptor.Validate() = %v, want nil", err)
	}
}

// TestPoolerClientsDescriptorIsStable pins that the descriptor is built once at
// construction. activity.go:184's pooler twin is TestPoolerDescriptorIsStable; the
// rule is the same and so is the reason — a Descriptor() that rebuilt on each call
// would let a field drift between the round the scheduler scheduled and the round
// the metrics label.
func TestPoolerClientsDescriptorIsStable(t *testing.T) {
	c := NewPoolerClientsCollector(&fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}, SQLConfig{})
	first := c.Descriptor()
	second := c.Descriptor()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Descriptor() differs between calls:\n first  = %+v\n second = %+v", first, second)
	}
}

// TestPoolerClientsCollectClientsRoundTrip is the happy path's accounting: one
// Begin, one query, one Commit, no Rollback, and exactly one round-trip reported.
// The query string is compared to the constant rather than to a literal so the test
// fails if the collector and the fake stop agreeing on how it is spelled.
func TestPoolerClientsCollectClientsRoundTrip(t *testing.T) {
	tgt := poolerClientsTarget()
	rows := []Row{
		clientsRowFromTable(map[string]string{"addr": "10.42.1.51", "port": "52344", "link": "0x7f8c1", "id": "1001"}),
		clientsRowFromTable(map[string]string{"addr": "10.42.1.52", "port": "52345", "link": "0x7f8c2", "id": "1002"}),
	}
	c, opener, tx := poolerClientsFixture(rows)

	clients, res, err := c.CollectClients(context.Background(), tgt)
	if err != nil {
		t.Fatalf("CollectClients() error = %v, want nil", err)
	}

	if opener.calls != 1 {
		t.Errorf("Begin calls = %d, want 1 (R2.7: one read-only transaction per view group)", opener.calls)
	}
	if len(opener.targets) == 1 && opener.targets[0] != tgt {
		t.Errorf("Begin target = %+v, want %+v", opener.targets[0], tgt)
	}
	if !reflect.DeepEqual(tx.queries, []string{showClientsQuery}) {
		t.Errorf("queries = %v, want [%s]", tx.queries, showClientsQuery)
	}
	if tx.commits != 1 {
		t.Errorf("commits = %d, want 1", tx.commits)
	}
	if tx.rollbacks != 0 {
		t.Errorf("rollbacks = %d, want 0 on a successful round", tx.rollbacks)
	}
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1 (one query, one transaction)", res.RoundTrips)
	}
	if len(clients) != 2 {
		t.Fatalf("len(clients) = %d, want 2", len(clients))
	}
	if clients[0].Addr != "10.42.1.51" || clients[1].Addr != "10.42.1.52" {
		t.Errorf("client addrs = %q, %q, want 10.42.1.51, 10.42.1.52", clients[0].Addr, clients[1].Addr)
	}
	if clients[0].Link != "0x7f8c1" || clients[1].Link != "0x7f8c2" {
		t.Errorf("client links = %q, %q, want 0x7f8c1, 0x7f8c2 (Link is the R6.4 join key)", clients[0].Link, clients[1].Link)
	}
}

// TestPoolerClientsUsesConfiguredSQLConfig is the R2.7/R2.8 pass-through check:
// what the constructor was given is what the opener was given. The func field is
// compared by nil-ness rather than by value because funcs are not comparable, and
// the two scalars are named individually for the same reason activity_test.go
// avoids DeepEqual on SQLConfig.
func TestPoolerClientsUsesConfiguredSQLConfig(t *testing.T) {
	tx := &fakeTx{rows: map[string][]Row{showClientsQuery: nil}}
	opener := &fakeOpener{tx: tx}
	c := NewPoolerClientsCollector(opener, SQLConfig{StatementTimeout: 3 * time.Second, ApplicationName: "custom"})

	if _, _, err := c.CollectClients(context.Background(), poolerClientsTarget()); err != nil {
		t.Fatalf("CollectClients() error = %v, want nil", err)
	}
	if len(opener.cfgs) != 1 {
		t.Fatalf("Begin called %d times, want 1", len(opener.cfgs))
	}
	got := opener.cfgs[0]
	if got.StatementTimeout != 3*time.Second {
		t.Errorf("Begin cfg.StatementTimeout = %s, want 3s (the configured value must reach the connection)", got.StatementTimeout)
	}
	if got.ApplicationName != "custom" {
		t.Errorf("Begin cfg.ApplicationName = %q, want \"custom\" (R2.8)", got.ApplicationName)
	}
}

// TestPoolerClientsAppliesDefaults checks the other half of the constructor: an
// empty SQLConfig reaches the opener as the documented defaults, not as a zero
// value. A zero StatementTimeout would put `SET LOCAL statement_timeout = 0` — no
// timeout at all — on every connection.
func TestPoolerClientsAppliesDefaults(t *testing.T) {
	tx := &fakeTx{rows: map[string][]Row{showClientsQuery: nil}}
	opener := &fakeOpener{tx: tx}
	c := NewPoolerClientsCollector(opener, SQLConfig{})

	if _, _, err := c.CollectClients(context.Background(), poolerClientsTarget()); err != nil {
		t.Fatalf("CollectClients() error = %v, want nil", err)
	}
	got := opener.cfgs[0]
	if got.StatementTimeout != DefaultStatementTimeout {
		t.Errorf("Begin cfg.StatementTimeout = %s, want DefaultStatementTimeout (%s)", got.StatementTimeout, DefaultStatementTimeout)
	}
	if got.ApplicationName != ApplicationName {
		t.Errorf("Begin cfg.ApplicationName = %q, want %q (R2.8's default)", got.ApplicationName, ApplicationName)
	}
}

// TestPoolerClientsCollectReturnsNoSamples pins the deliberate empty. The
// interface method's whole job is to be the scheduler's entry point; the family it
// would export does not exist (Collect's docblock gives the three citations), so an
// empty Samples with a nil error is the correct answer and the test is what stops a
// future "let's add a gauge" from making it an accidental one.
func TestPoolerClientsCollectReturnsNoSamples(t *testing.T) {
	rows := []Row{clientsRowFromTable(map[string]string{"addr": "10.42.1.51", "port": "52344"})}
	c, _, _ := poolerClientsFixture(rows)

	res, err := c.Collect(context.Background(), poolerClientsTarget())
	if err != nil {
		t.Fatalf("Collect() error = %v, want nil", err)
	}
	if len(res.Samples) != 0 {
		t.Errorf("Collect() returned %d samples, want 0 (no R9.5 family exists for client attribution)", len(res.Samples))
	}
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
	}
}

// TestPoolerClientsDropsUnidentifiableRow pins the parser's only drop rule.
func TestPoolerClientsDropsUnidentifiableRow(t *testing.T) {
	rows := []Row{
		clientsRowFromTable(map[string]string{"addr": "10.42.1.51"}),
		// Neither addr nor ptr: no identity, no address, dropped.
		clientsRowFromTable(map[string]string{"addr": "", "ptr": ""}),
		// addr empty but ptr present: identifiable, so kept.
		clientsRowFromTable(map[string]string{"addr": "", "ptr": "0x7f0001"}),
		// addr present but ptr empty: kept for the same reason.
		clientsRowFromTable(map[string]string{"addr": "10.42.1.53", "ptr": ""}),
	}
	c, _, _ := poolerClientsFixture(rows)

	clients, _, err := c.CollectClients(context.Background(), poolerClientsTarget())
	if err != nil {
		t.Fatalf("CollectClients() error = %v, want nil", err)
	}
	if len(clients) != 3 {
		t.Fatalf("len(clients) = %d, want 3 (only the addr-and-ptr-empty row is dropped):\n %+v", len(clients), clients)
	}
	if clients[1].Ptr != "0x7f0001" {
		t.Errorf("second client Ptr = %q, want 0x7f0001 (a ptr-only row is identifiable)", clients[1].Ptr)
	}
}

// TestPoolerClientsEmptyResultIsNotAnError pins the empty-fleet case: SHOW CLIENTS
// on a pooler with no clients returns no rows, and that is a successful round with
// an empty list, not an error and not a nil slice the caller has to guard.
func TestPoolerClientsEmptyResultIsNotAnError(t *testing.T) {
	c, _, _ := poolerClientsFixture(nil)

	clients, res, err := c.CollectClients(context.Background(), poolerClientsTarget())
	if err != nil {
		t.Fatalf("CollectClients() error = %v, want nil", err)
	}
	if clients == nil {
		t.Errorf("clients = nil, want an empty non-nil slice (clientsFromRows always allocates)")
	}
	if len(clients) != 0 {
		t.Errorf("len(clients) = %d, want 0", len(clients))
	}
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
	}
}

func TestPoolerClientsBeginErrorIssuesNothing(t *testing.T) {
	beginErr := errors.New("fake: begin failed")
	c := NewPoolerClientsCollector(&fakeOpener{beginErr: beginErr}, SQLConfig{})

	clients, res, err := c.CollectClients(context.Background(), poolerClientsTarget())
	if err == nil {
		t.Fatalf("CollectClients() error = nil, want the begin error")
	}
	if !errors.Is(err, beginErr) {
		t.Errorf("error = %v, want it to wrap the begin error", err)
	}
	// Result contains a slice, so it is not comparable and the zero value has to be
	// checked field by field: nothing issued, nothing sampled.
	if res.RoundTrips != 0 || len(res.Samples) != 0 {
		t.Errorf("res = %+v, want the zero Result (nothing was issued)", res)
	}
	if clients != nil {
		t.Errorf("clients = %+v, want nil on an error round", clients)
	}
	if !errors.Is(err, beginErr) || err.Error() != "collector pooler-clients: begin read-only transaction: fake: begin failed" {
		t.Errorf("error string = %q, want the collector-prefixed wrap", err.Error())
	}
}

func TestPoolerClientsQueryErrorRollsBackAndReportsRoundTrips(t *testing.T) {
	tx := &fakeTx{rows: map[string][]Row{showClientsQuery: nil}, failOn: showClientsQuery}
	opener := &fakeOpener{tx: tx}
	c := NewPoolerClientsCollector(opener, SQLConfig{})

	clients, res, err := c.CollectClients(context.Background(), poolerClientsTarget())
	if err == nil {
		t.Fatalf("CollectClients() error = nil, want the query error")
	}
	if tx.rollbacks != 1 {
		t.Errorf("rollbacks = %d, want 1 (sql.go's Tx doc: rollback on every error path)", tx.rollbacks)
	}
	if tx.commits != 0 {
		t.Errorf("commits = %d, want 0 after a failed query", tx.commits)
	}
	// The failed query still counts: the cluster saw one round-trip, and R13.1's
	// rule is that a partial round reports what it issued.
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1 (the query was issued and failed; it still counts)", res.RoundTrips)
	}
	if clients != nil {
		t.Errorf("clients = %+v, want nil on an error round", clients)
	}
}

func TestPoolerClientsCommitErrorRollsBack(t *testing.T) {
	commitErr := errors.New("fake: commit failed")
	tx := &fakeTx{
		rows:      map[string][]Row{showClientsQuery: {clientsRowFromTable(nil)}},
		commitErr: commitErr,
	}
	c := NewPoolerClientsCollector(&fakeOpener{tx: tx}, SQLConfig{})

	clients, res, err := c.CollectClients(context.Background(), poolerClientsTarget())
	if err == nil {
		t.Fatalf("CollectClients() error = nil, want the commit error")
	}
	if !errors.Is(err, commitErr) {
		t.Errorf("error = %v, want it to wrap the commit error", err)
	}
	if tx.rollbacks != 1 {
		t.Errorf("rollbacks = %d, want 1 (rollback after a failed commit, activity.go:348's rule)", tx.rollbacks)
	}
	if res.RoundTrips != 1 {
		t.Errorf("RoundTrips = %d, want 1", res.RoundTrips)
	}
	// The rows were read but the round failed, and the scheduler publishes nothing
	// on a failed round — so nil is both correct and free.
	if clients != nil {
		t.Errorf("clients = %+v, want nil on a failed round", clients)
	}
}

// TestPoolerClientsCollectorNameIsHyphenated pins the spelling A3's second ruling
// fixed: pooler-clients, with a hyphen, because no multi-word name in §3.3's
// enumeration uses an underscore. The value is asserted as a literal here — not
// derived from the constant — because the constant is the thing under test.
func TestPoolerClientsCollectorNameIsHyphenated(t *testing.T) {
	if CollectorPoolerClients != "pooler-clients" {
		t.Errorf("CollectorPoolerClients = %q, want \"pooler-clients\"", CollectorPoolerClients)
	}
	if CollectorPoolerClients == "pooler_clients" {
		t.Error("underscore spelling; the enumeration uses hyphens (instance-metrics, k8s-events)")
	}
}

// TestPoolerClientsRegistryRegistration is the registry.go:97 half: the two pooler
// collectors coexist in one registry. This is the assertion the whole two-name
// ruling exists to satisfy — one name for two collectors is rejected there, and
// without this test the rejection is the only thing telling you the names differ.
func TestPoolerClientsRegistryRegistration(t *testing.T) {
	pooler, err := NewPoolerCollector(func(context.Context, string) (int, []byte, error) {
		return 200, nil, nil
	}, PoolerConfig{})
	if err != nil {
		t.Fatalf("NewPoolerCollector() error = %v", err)
	}
	clients := NewPoolerClientsCollector(&fakeOpener{tx: &fakeTx{rows: map[string][]Row{}}}, SQLConfig{})

	r, err := NewRegistry([]Collector{pooler, clients}, Overrides{})
	if err != nil {
		t.Fatalf("NewRegistry(pooler, pooler-clients) error = %v, want nil", err)
	}
	if got := len(r.All()); got != 2 {
		t.Errorf("registry has %d collectors, want 2", got)
	}
	for _, name := range []string{CollectorPooler, CollectorPoolerClients} {
		e, ok := r.Get(name)
		if !ok {
			t.Errorf("registry.Get(%q) ok = false, want true", name)
			continue
		}
		if e.Desc.Name != name {
			t.Errorf("registry.Get(%q).Desc.Name = %q, want %q", name, e.Desc.Name, name)
		}
	}
}
