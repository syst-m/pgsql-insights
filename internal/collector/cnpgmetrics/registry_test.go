package cnpgmetrics

import (
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/collector/metrictype"
)

func TestRegistryNoDuplicateNames(t *testing.T) {
	seen := map[string]Family{}
	for _, f := range families {
		if prev, ok := seen[f.Name]; ok {
			t.Errorf("duplicate registry row for %s: %+v and %+v", f.Name, prev, f)
		}
		seen[f.Name] = f
	}
	if len(seen) != len(families) {
		t.Errorf("index has %d entries, table has %d rows", len(seen), len(families))
	}
}

// TestIndexMatchesTable is the R16.8 guard. index is derived from families in init,
// so the two cannot disagree — but the derivation is the kind of thing a later edit
// turns into a second literal table, and this is the assertion that catches it.
func TestIndexMatchesTable(t *testing.T) {
	for _, f := range families {
		got, ok := Lookup(f.Name)
		if !ok {
			t.Errorf("Lookup(%s) missing", f.Name)
			continue
		}
		if got != f {
			t.Errorf("Lookup(%s) = %+v, table row is %+v", f.Name, got, f)
		}
	}
}

func TestRegistryNamesAreValidMetricNames(t *testing.T) {
	for _, f := range families {
		if !validMetricName(f.Name) {
			t.Errorf("%s is not a valid Prometheus metric name", f.Name)
		}
		if !strings.HasPrefix(f.Name, "cnpg") {
			t.Errorf("%s: every registry family must be a cnpg_* or cnpg_collector_* family (R2.6's scope)", f.Name)
		}
	}
}

func TestRegistryTypesAreExportable(t *testing.T) {
	for _, f := range families {
		switch f.Type {
		case metrictype.TypeCounter, metrictype.TypeGauge:
		default:
			t.Errorf("%s: type %q is neither counter nor gauge; the cnpg surface contains no untyped family, and TypeNotExported here would mean the registry knows a family it refuses to name", f.Name, f.Type)
		}
	}
}

func TestRegistryCountersEndInTotal(t *testing.T) {
	for _, f := range families {
		if f.Type == metrictype.TypeCounter && !strings.HasSuffix(f.Name, "_total") {
			// The Prometheus convention R9.4's own names follow. The cnpg exporter
			// predates strict adherence on some families, so this is a report, not a
			// failure: the registry mirrors the body, and renaming a family to satisfy
			// a linter is R9.4's call, not this table's.
			t.Logf("counter %s has no _total suffix (the exporter's spelling; R9.4's table owns the pgsi name)", f.Name)
		}
	}
}

func TestGroupsMatchesConstBlock(t *testing.T) {
	gs := Groups()
	if len(gs) == 0 {
		t.Fatal("Groups() is empty")
	}
	// Every group used by a row must be returned by Groups(), and vice versa.
	used := map[Group]bool{}
	for _, f := range families {
		used[f.Group] = true
	}
	for _, g := range gs {
		if !used[g] {
			t.Errorf("Groups() lists %s but no family is in it", g)
		}
	}
	for g := range used {
		found := false
		for _, x := range gs {
			if x == g {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("family set uses group %s which Groups() does not list", g)
		}
	}
}

func TestFamiliesByGroupAccountsForEveryFamily(t *testing.T) {
	var total int
	for _, g := range Groups() {
		total += len(FamiliesByGroup(g))
	}
	if total != len(families) {
		t.Errorf("FamiliesByGroup over every group yields %d families, table has %d", total, len(families))
	}
}

func TestFamiliesReturnsACopy(t *testing.T) {
	got := Families()
	if len(got) != len(families) {
		t.Fatalf("Families() len = %d, want %d", len(got), len(families))
	}
	got[0].Name = "mutated"
	if families[0].Name == "mutated" {
		t.Error("Families() returned the table itself, not a copy")
	}
}

func TestFamiliesByGroupUnknownGroup(t *testing.T) {
	if got := FamiliesByGroup(Group("no-such-group")); len(got) != 0 {
		t.Errorf("FamiliesByGroup(unknown) = %d families, want 0", len(got))
	}
}

// TestKnownGroupCounts pins the group census. These are the numbers the generator
// produced from the four fixtures; a row added to one group without the fixture set
// changing, or a group renamed, moves exactly one number here.
func TestKnownGroupCounts(t *testing.T) {
	want := map[Group]int{
		GroupPGBouncer:        51,
		GroupCollector:        24,
		GroupStatDatabase:     15,
		GroupStatBGWriter:     11,
		GroupStatArchiver:     9,
		GroupStatCheckpointer: 9,
		GroupStatReplication:  9,
		GroupReplication:      4,
		GroupBackends:         3,
		GroupDatabase:         3,
		GroupCache:            2,
		GroupInstance:         2,
		GroupReplicationSlots: 2,
		GroupExtensions:       1,
		GroupPostmaster:       1,
		GroupSettings:         1,
	}
	var sum int
	for _, g := range Groups() {
		n := len(FamiliesByGroup(g))
		w, ok := want[g]
		if !ok {
			t.Errorf("group %s has %d families but is not in the expected census", g, n)
			continue
		}
		if n != w {
			t.Errorf("group %s has %d families, want %d", g, n, w)
		}
		sum += n
		delete(want, g)
	}
	for g := range want {
		t.Errorf("census expects group %s, Groups() does not list it", g)
	}
	if sum != 147 {
		t.Errorf("census sums to %d, want 147", sum)
	}
}
