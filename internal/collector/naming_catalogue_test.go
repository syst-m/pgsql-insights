// This file is the cross-package half of the R9.4 naming check, and it is in
// package collector_test rather than collector for one reason: it imports
// internal/api, and internal/api is going to import internal/collector (conn.go:115
// says so outright — "internal/api (which owns the DTO and will import the
// collectors at step 4)"). An in-package test file cannot import a package that
// imports the package under test — Go rejects it as an import cycle in test — but
// an external test package can. Putting it here means the test survives the day
// that import lands, which is the day the alternative would have broken it.
package collector_test

import (
	"strings"
	"testing"

	"github.com/frankellis/pgsql-insights/internal/api"
	"github.com/frankellis/pgsql-insights/internal/collector"
)

// TestExportedNamesCoverCatalogue is the test naming.go's doc comment names.
//
// The invariant it checks is narrower than "the two lists are equal", and the
// narrowness is the point. api.CatalogueEntry has an Implemented field whose doc
// says false means "the name is provisional and Phase 2's MetricHub must adopt it
// for this entry to answer anything" — so a provisional catalogue entry is
// *allowed* to name a family the exporter does not produce yet, and requiring
// equality would fail on every entry Phase 2 has not implemented.
//
// What is not allowed is an entry the catalogue marks Implemented while the
// exporter produces nothing behind it. That is R14.7's violation seen from the
// other side: R14.7 forbids a metric in code with no doc; this forbids a metric in
// the docs with no code, which is the same disagreement between producer and
// consumer that R16.8 files.
//
// The derived entries (cache_hit_ratio, whose Metric is empty by design because
// its PromQL combines two families) are skipped for the same reason the field's
// doc gives: there is no single family to look up.
func TestExportedNamesCoverCatalogue(t *testing.T) {
	exported := map[string]bool{}
	for _, n := range collector.ExportedMetricNames() {
		exported[n] = true
	}

	entries := api.DefaultCatalogue().Entries()
	if len(entries) == 0 {
		t.Fatal("api.DefaultCatalogue() has no entries")
	}

	var (
		checked, implemented int
		missing              []string
	)
	for _, e := range entries {
		if e.Metric == "" {
			continue // derived entry: no single family to resolve
		}
		checked++
		if !e.Implemented {
			continue
		}
		implemented++
		// The catalogue's names carry R9.2's prefix (TestCatalogueIsWellFormed
		// asserts it); the collector's list is the unprefixed half, because the
		// prefix is applied at render time. Stripping it is the comparison.
		base := strings.TrimPrefix(e.Metric, collector.DefaultPrefix)
		if !exported[base] {
			missing = append(missing, e.Name+" -> "+e.Metric)
		}
	}

	if len(missing) > 0 {
		t.Errorf("%d of %d checked catalogue entries are marked Implemented but have no family in the collector's naming table:\n  %s",
			len(missing), implemented, strings.Join(missing, "\n  "))
	}
	if checked == 0 {
		t.Error("no catalogue entry with a Metric to check (all derived?)")
	}
}

// TestProvisionalSetIsExactlyTheUnexportedNames is the assertion behind the
// catalogue header's sentence "The list is verified against
// collector.ExportedMetricNames()", which until now had no test behind it.
//
// TestExportedNamesCoverCatalogue checks one direction only: an entry marked
// Implemented must have a family behind it. It cannot see the other direction,
// because a provisional entry is *allowed* to be unexported — so an entry that
// stays provisional after its collector lands is invisible to it. That is not
// hypothetical. 49b3210 shipped the activity collector, which added
// db_backends_by_state and db_backends_by_application_name to the naming table,
// and their provisionalSystem rows stayed put until A2 deleted them by hand at
// a826f84. The header's sentence was true in prose and unenforced in code, which
// is R16.8's producer/consumer disagreement with both sides being tests that
// read past each other.
//
// So the rule is a biconditional, and it is the only rule that fits the field's
// own doc: Implemented=false means "the name is provisional and Phase 2's
// MetricHub must adopt it for this entry to answer anything". Once the naming
// table has the name, MetricHub has adopted it, so `false` is now a false
// statement about the table — and deleting the row is the fix the catalogue's own
// header prescribes ("When Phase 2 ships one of these, the fix is to delete its
// line"), which this test makes mechanical instead of remembered.
func TestProvisionalSetIsExactlyTheUnexportedNames(t *testing.T) {
	exported := map[string]bool{}
	for _, n := range collector.ExportedMetricNames() {
		exported[n] = true
	}

	entries := api.DefaultCatalogue().Entries()
	if len(entries) == 0 {
		t.Fatal("api.DefaultCatalogue() has no entries")
	}

	var (
		checked, implemented int
		disagree             []string
	)
	for _, e := range entries {
		if e.Metric == "" {
			continue // derived entry: no single family to resolve
		}
		checked++
		// The catalogue's names carry R9.2's prefix; the collector's list is the
		// unprefixed half because the prefix is applied at render time. Stripping
		// it is the comparison, for the same reason TestExportedNamesCoverCatalogue
		// strips it.
		base := strings.TrimPrefix(e.Metric, collector.DefaultPrefix)
		if e.Implemented {
			implemented++
		}
		if e.Implemented == exported[base] {
			continue
		}
		if e.Implemented {
			disagree = append(disagree, e.Name+" -> "+e.Metric+
				": marked implemented, but collector.ExportedMetricNames() has no such family")
			continue
		}
		disagree = append(disagree, e.Name+" -> "+e.Metric+
			": still marked provisional, but the naming table has it — delete the provisional row")
	}

	if len(disagree) > 0 {
		t.Errorf("%d of %d entries disagree with collector.ExportedMetricNames() (%d marked implemented):\n  %s",
			len(disagree), checked, implemented, strings.Join(disagree, "\n  "))
	}
	if checked == 0 {
		t.Error("no catalogue entry with a Metric to check (all derived?)")
	}
}
