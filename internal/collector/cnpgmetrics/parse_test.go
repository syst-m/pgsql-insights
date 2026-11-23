package cnpgmetrics

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fixtureDir is R16.3's recorded exposition set. The four files are named in
// requirements.md's R16.3 row, and the counts below are that row's measurements:
// "Measured 107 / 115 / 118 # TYPE families on the three reachable reference pods"
// (:170), plus the 9127 pooler pod whose 80 the S3 row does not count because S3
// describes the instance-manager endpoint.
const fixtureDir = "../../../testdata/cnpg-metrics"

// fixtureCase is one recorded body with its verified family census. The four
// numbers per row are the whole point of the test: a parser that groups families by
// sample-line name instead of by `# TYPE` reports 120/82/109/117 — two phantom
// families per file, from go_gc_duration_seconds_sum and _count — and every one of
// these assertions catches it.
var fixtureCases = []struct {
	file        string
	total       int
	cnpg        int
	other       int
	cnpgGauge   int
	cnpgCounter int
}{
	{"ai-api-gateway__rev-proxy-pg-2__9187.txt", 118, 89, 29, 58, 31},
	{"ai-api-gateway__rev-proxy-pg-pooler-86b4bdd84d-n9vlv__9127.txt", 80, 51, 29, 50, 1},
	{"cookbook__cookbook-pg-1__9187.txt", 107, 78, 29, 49, 29},
	{"open-webui__open-webui-pg-2__9187.txt", 115, 86, 29, 56, 30},
}

func TestParseFixtures(t *testing.T) {
	for _, tc := range fixtureCases {
		t.Run(tc.file, func(t *testing.T) {
			s, err := ParseFile(filepath.Join(fixtureDir, tc.file))
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			if got := s.Total(); got != tc.total {
				t.Errorf("Total() = %d, want %d", got, tc.total)
			}
			if got := len(s.CNPG); got != tc.cnpg {
				t.Errorf("len(CNPG) = %d, want %d", got, tc.cnpg)
			}
			if got := len(s.Other); got != tc.other {
				t.Errorf("len(Other) = %d, want %d", got, tc.other)
			}

			var gauge, counter int
			for _, m := range s.CNPG {
				if m.Family == nil {
					t.Errorf("CNPG family %s has a nil Family pointer", m.Name)
					continue
				}
				if m.Family.Name != m.Name {
					t.Errorf("CNPG family %s resolved to registry entry %q", m.Name, m.Family.Name)
				}
				switch m.Type {
				case "gauge":
					gauge++
				case "counter":
					counter++
				default:
					t.Errorf("CNPG family %s has type %q; every cnpg_* family in the fixtures is a counter or a gauge", m.Name, m.Type)
				}
				if len(m.Samples) == 0 {
					t.Errorf("CNPG family %s declared but has no samples", m.Name)
				}
			}
			if gauge != tc.cnpgGauge {
				t.Errorf("cnpg gauges = %d, want %d", gauge, tc.cnpgGauge)
			}
			if counter != tc.cnpgCounter {
				t.Errorf("cnpg counters = %d, want %d", counter, tc.cnpgCounter)
			}

			// The summary fold, asserted precisely. A name ending in _count is not
			// by itself a phantom — cnpg_pg_stat_archiver_archived_count and the ten
			// cnpg_pgbouncer_stats_*_count families are real families with _count in
			// their actual names. A phantom is specifically a declared summary or
			// histogram's derived name showing up as its own family, so that is what
			// gets asserted, off the declared set rather than off a suffix guess.
			for _, m := range append(append([]Metric{}, s.CNPG...), s.Other...) {
				if m.Type != "summary" && m.Type != "histogram" {
					continue
				}
				for _, sfx := range []string{"_sum", "_count", "_bucket"} {
					if _, ok := s.Lookup(m.Name + sfx); ok {
						t.Errorf("%s%s is a family, but %s is a %s: %s%s is a sample name and must fold into %s", m.Name, sfx, m.Name, m.Type, m.Name, sfx, m.Name)
					}
				}
			}
			// The concrete case, named rather than inferred: the one summary in the
			// set is go_gc_duration_seconds, and its two derived names must not be
			// families in any fixture.
			if m, ok := s.Lookup("go_gc_duration_seconds"); !ok {
				t.Error("go_gc_duration_seconds missing from the parsed body")
			} else if m.Type != "summary" {
				t.Errorf("go_gc_duration_seconds type = %q, want summary", m.Type)
			}
		})
	}
}

// TestFixtureSetUnionIs147 pins the number the registry was generated from. The
// per-file counts move when CNPG ships; the union moving means registry.go and the
// fixture set have diverged, which is the thing TestParseFixtures turns into a
// compile-time-ish failure instead of a runtime surprise in production.
func TestFixtureSetUnionIs147(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("found %d fixture files, want 4 (R16.3 names exactly seven fixtures across cnpg-metrics/ and pg-status/; four are here)", len(files))
	}
	cnpg := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(line, "# TYPE ") {
				continue
			}
			name, typ, _ := strings.Cut(strings.TrimPrefix(line, "# TYPE "), " ")
			if strings.HasPrefix(name, "cnpg") {
				if prev, ok := cnpg[name]; ok && prev != typ {
					t.Errorf("%s: type %q in %s conflicts with %q in an earlier fixture", name, typ, filepath.Base(f), prev)
				}
				cnpg[name] = typ
			}
		}
	}
	if len(cnpg) != 147 {
		t.Errorf("union of cnpg_* families across the fixtures = %d, want 147 (the size of registry.go's table)", len(cnpg))
	}
	for name, typ := range cnpg {
		f, ok := Lookup(name)
		if !ok {
			t.Errorf("fixture family %s has no registry entry", name)
			continue
		}
		if string(f.Type) != typ {
			t.Errorf("registry types %s as %q, the fixtures declare it as %q", name, f.Type, typ)
		}
	}
}

func TestParseSummaryFolding(t *testing.T) {
	body := `# HELP go_gc_duration_seconds A summary of the pause duration of garbage collection cycles.
# TYPE go_gc_duration_seconds summary
go_gc_duration_seconds{quantile="0"} 2.7432e-05
go_gc_duration_seconds{quantile="0.25"} 5.9124e-05
go_gc_duration_seconds{quantile="0.5"} 0.000101235
go_gc_duration_seconds{quantile="0.75"} 0.000128866
go_gc_duration_seconds{quantile="1"} 0.000463146
go_gc_duration_seconds_sum 5.20792039
go_gc_duration_seconds_count 51226
`
	s, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := s.Total(); got != 1 {
		t.Fatalf("Total() = %d, want 1 (a summary is one family, not seven)", got)
	}
	m, ok := s.Lookup("go_gc_duration_seconds")
	if !ok {
		t.Fatal("Lookup(go_gc_duration_seconds) missing")
	}
	if m.Type != "summary" {
		t.Errorf("Type = %q, want summary", m.Type)
	}
	if len(m.Samples) != 7 {
		t.Fatalf("len(Samples) = %d, want 7", len(m.Samples))
	}
	if m.Samples[5].Value != 5.20792039 {
		t.Errorf("_sum sample value = %v, want 5.20792039", m.Samples[5].Value)
	}
	if m.Samples[6].Value != 51226 {
		t.Errorf("_count sample value = %v, want 51226", m.Samples[6].Value)
	}
	if m.Family != nil {
		t.Error("a go_* family must not carry a cnpg registry entry")
	}
}

func TestParseHistogramFolding(t *testing.T) {
	body := `# TYPE pgsi_test_http_seconds histogram
# HELP pgsi_test_http_seconds doc
pgsi_test_http_seconds_bucket{le="0.005"} 0
pgsi_test_http_seconds_bucket{le="0.01"} 1
pgsi_test_http_seconds_bucket{le="+Inf"} 3
pgsi_test_http_seconds_sum 1.25
pgsi_test_http_seconds_count 3
`
	// The HELP-after-TYPE ordering is deliberate: the spec puts HELP first, the
	// exporter always sends HELP first, and the parser must not care, because the
	// declaration pass reads both kinds before touching a single sample line.
	s, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	m, ok := s.Lookup("pgsi_test_http_seconds")
	if !ok {
		t.Fatal("Lookup missing")
	}
	if len(m.Samples) != 5 {
		t.Fatalf("len(Samples) = %d, want 5", len(m.Samples))
	}
	if m.Help != "doc" {
		t.Errorf("Help = %q, want doc", m.Help)
	}
	if m.Samples[0].Labels["le"] != "0.005" {
		t.Errorf("first bucket le = %q, want 0.005", m.Samples[0].Labels["le"])
	}
}

func TestParseLabels(t *testing.T) {
	cases := []struct {
		name string
		body string
		want map[string]string
	}{
		{"no braces", "# TYPE x gauge\nx 1\n", nil},
		{"empty braces", "# TYPE x gauge\nx{} 1\n", map[string]string{}},
		{"two labels", "# TYPE x gauge\nx{a=" + `"one"` + ",b=" + `"two"` + `} 1`, map[string]string{"a": "one", "b": "two"}},
		{"escaped quote", "# TYPE x gauge\nx{a=" + `"say \"hi\""` + `} 1`, map[string]string{"a": `say "hi"`}},
		{"escaped backslash", "# TYPE x gauge\nx{a=" + `"a\\b"` + `} 1`, map[string]string{"a": `a\b`}},
		{"escaped newline", "# TYPE x gauge\nx{a=" + `"a\nb"` + `} 1`, map[string]string{"a": "a\nb"}},
		{"value with a space", "# TYPE x gauge\nx{a=" + `"one two"` + `} 1`, map[string]string{"a": "one two"}},
		{"trailing timestamp", "# TYPE x gauge\nx 1 1659781676000\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Parse(tc.body)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			m, ok := s.Lookup("x")
			if !ok {
				t.Fatal("Lookup(x) missing")
			}
			if len(m.Samples) != 1 {
				t.Fatalf("len(Samples) = %d, want 1", len(m.Samples))
			}
			got := m.Samples[0].Labels
			if len(got) != len(tc.want) {
				t.Fatalf("labels = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("label %s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"malformed TYPE", "# TYPE x\n", "malformed # TYPE"},
		{"unknown type", "# TYPE x coutner\nx 1\n", `unknown type "coutner"`},
		{"bad metric name", "# TYPE 9x gauge\n9x 1\n", "not a valid metric name"},
		{"duplicate TYPE", "# TYPE x gauge\n# TYPE x gauge\nx 1\n", "duplicate # TYPE for x"},
		{"conflicting TYPE", "# TYPE x gauge\n# TYPE x counter\nx 1\n", "duplicate # TYPE for x"},
		{"sample with no TYPE", "x 1\n", "metric \"x\" has no # TYPE declaration"},
		{"bad value", "# TYPE x gauge\nx banana\n", `invalid value "banana"`},
		{"unterminated braces", "# TYPE x gauge\nx{a=\"1\" 1\n", "unterminated label block"},
		{"unquoted label value", "# TYPE x gauge\nx{a=1} 1\n", "value is not quoted"},
		{"dangling label", "# TYPE x gauge\nx{a=\"1\",b} 1\n", "dangling"},
		{"duplicate label", "# TYPE x gauge\nx{a=\"1\",a=\"2\"} 1\n", "duplicate label a"},
		{"trailing backslash", "# TYPE x gauge\nx{a=\"a\\} 1\n", "trailing backslash"},
		// The R2.6 rule, tested head-on: a cnpg family the registry has never heard
		// of is an error, not a passthrough.
		{"unknown cnpg family", "# TYPE cnpg_totally_new_metric gauge\ncnpg_totally_new_metric 1\n", "no registry entry"},
		// A cnpg family whose declared type disagrees with the registry's. The
		// registry says cnpg_backends_total is a gauge.
		{"type mismatch", "# TYPE cnpg_backends_total counter\ncnpg_backends_total 1\n", `declared "counter" but registry says "gauge"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.body)
			if err == nil {
				t.Fatalf("Parse(%q) = nil error, want %q", tc.body, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseEmptyBody(t *testing.T) {
	s, err := Parse("")
	if err != nil {
		t.Fatalf("Parse(\"\") = %v", err)
	}
	if s.Total() != 0 {
		t.Errorf("Total() = %d, want 0", s.Total())
	}
}

func TestParseFileMissing(t *testing.T) {
	if _, err := ParseFile(filepath.Join(fixtureDir, "does-not-exist.txt")); err == nil {
		t.Fatal("ParseFile on a missing file = nil error")
	}
}

// TestParseFileNamesEveryFixtureInItsErrors is the multi-file debugging case: with
// four bodies in the fixture set, an error that says only "line 41" is not
// actionable. ParseFile must put the path in front of Parse's line number.
func TestParseFileNamesEveryFixtureInItsErrors(t *testing.T) {
	body := "# TYPE x\ngarbage\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "pod__9187.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "pod__9187.txt") || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error = %q, want it to name the file and the line", err)
	}
}

// TestNoFixtureFamilyIsOnlyReachableViaOther guards the CNPG/Other split itself:
// every family the parser puts in Other must be a non-cnpg name, and every cnpg
// family must be in CNPG. Without this, a prefix bug would silently move families
// between the two slices while the totals still added up.
func TestNoFixtureFamilyIsOnlyReachableViaOther(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		s, err := ParseFile(f)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		for _, m := range s.Other {
			if strings.HasPrefix(m.Name, "cnpg") {
				t.Errorf("%s: %s is in Other but its name is a cnpg family", filepath.Base(f), m.Name)
			}
		}
		for _, m := range s.CNPG {
			if !strings.HasPrefix(m.Name, "cnpg") {
				t.Errorf("%s: %s is in CNPG but its name is not a cnpg family", filepath.Base(f), m.Name)
			}
		}
	}
}

// TestFixtureSampleCounts checks the parser against a direct line count of one
// fixture: the number of non-comment, non-empty lines must equal the total number of
// samples across all families. This is the assertion that catches a parser that
// drops a line it cannot parse instead of erroring on it.
func TestFixtureSampleCounts(t *testing.T) {
	for _, tc := range fixtureCases {
		t.Run(tc.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(fixtureDir, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			var lines int
			for _, l := range strings.Split(string(b), "\n") {
				l = strings.TrimSpace(l)
				if l == "" || strings.HasPrefix(l, "#") {
					continue
				}
				lines++
			}
			s, err := ParseFile(filepath.Join(fixtureDir, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			var samples int
			for _, m := range append(append([]Metric{}, s.CNPG...), s.Other...) {
				samples += len(m.Samples)
			}
			if samples != lines {
				t.Errorf("parsed %d samples from %d sample lines", samples, lines)
			}
		})
	}
}

// TestScrapeLookupCoversBothSlices checks byName was populated for Other as well as
// CNPG — a Lookup that only worked for cnpg families would make the go_* families
// unaddressable once parsed.
func TestScrapeLookupCoversBothSlices(t *testing.T) {
	body := "# TYPE cnpg_backends_total gauge\ncnpg_backends_total 4\n# TYPE go_info untyped\ngo_info 1\n"
	s, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cnpg_backends_total", "go_info"} {
		if _, ok := s.Lookup(name); !ok {
			t.Errorf("Lookup(%s) missing", name)
		}
	}
	if _, ok := s.Lookup("nope"); ok {
		t.Error("Lookup(nope) = ok")
	}
}

// TestRegistryOrderIsStableAcrossParses is the Go-map-randomisation guard: the
// CNPG slice must come out in the same order for two parses of the same body.
func TestFixtureParseIsDeterministic(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(fixtureDir, "cookbook__cookbook-pg-1__9187.txt"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, m := range first.CNPG {
		want = append(want, m.Name)
	}
	for i := 0; i < 5; i++ {
		s, err := Parse(string(b))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range s.CNPG {
			got = append(got, m.Name)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("parse %d produced a different CNPG order than parse 1", i+1)
		}
	}
	if !sort.StringsAreSorted(want) {
		t.Log("note: CNPG slice is in body order, not alphabetical — asserted only for stability")
	}
}
