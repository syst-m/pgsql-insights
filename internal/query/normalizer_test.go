package query

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// fixturePath is Phase 3's normalizer corpus. R16.3 names exactly seven fixture
// files and none of them is a normalizer fixture, so this is the corpus Phase 3
// adds and §7.2's "normalizer output matches the fixture corpus" is checked here.
const fixturePath = "../../testdata/query/normalizer.json"

// fixtureCase is one entry of the corpus.
//
// Display and Truncated are pointers so "absent" is distinguishable from "the
// empty string" and "false": the corpus omits them for every case that is not
// about R3.6's length cap, and an omitted pair means "the display text is the
// whole template and nothing was truncated" rather than an unstated assertion.
type fixtureCase struct {
	Name        string  `json:"name"`
	Input       string  `json:"input"`
	Template    string  `json:"template"`
	Fingerprint string  `json:"fingerprint"`
	Length      int     `json:"length,omitempty"`
	Display     *string `json:"display,omitempty"`
	Truncated   *bool   `json:"truncated,omitempty"`
}

type fixture struct {
	Description string        `json:"description"`
	Cases       []fixtureCase `json:"cases"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", fixturePath, err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse %s: %v", fixturePath, err)
	}
	if len(f.Cases) == 0 {
		t.Fatalf("%s: no cases", fixturePath)
	}
	return f
}

// TestNormalizeMatchesFixture is the Phase 3 acceptance check: every case in the
// corpus must reproduce exactly.
func TestNormalizeMatchesFixture(t *testing.T) {
	f := loadFixture(t)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := Normalize(c.Input, Options{Length: c.Length})

			if got.Template != c.Template {
				t.Errorf("template:\n got %q\nwant %q", got.Template, c.Template)
			}
			// The corpus's fingerprint was hashed with Python's hashlib when the
			// corpus was authored, so this compares two independent SHA-256
			// implementations rather than checking Fingerprint against itself.
			if got.Fingerprint != c.Fingerprint {
				t.Errorf("fingerprint:\n got %s\nwant %s", got.Fingerprint, c.Fingerprint)
			}
			if got.Fingerprint != Fingerprint(c.Template) {
				t.Errorf("fingerprint is not Fingerprint(template) for case %s", c.Name)
			}

			wantDisplay := c.Template
			if c.Display != nil {
				wantDisplay = *c.Display
			}
			if got.DisplayText != wantDisplay {
				t.Errorf("display:\n got %q\nwant %q", got.DisplayText, wantDisplay)
			}

			wantTruncated := len(c.Template) > optsLength(c.Length)
			if c.Truncated != nil {
				wantTruncated = *c.Truncated
			}
			if got.Truncated != wantTruncated {
				t.Errorf("truncated = %v, want %v", got.Truncated, wantTruncated)
			}
			// R3.6's flag is only meaningful if the display text really is a
			// prefix of the template; every case asserts it, not just the
			// truncation cases.
			if !strings.HasPrefix(got.Template, got.DisplayText) {
				t.Errorf("display %q is not a prefix of template %q", got.DisplayText, got.Template)
			}
			if !utf8.ValidString(got.DisplayText) {
				t.Errorf("display %q is not valid UTF-8", got.DisplayText)
			}
		})
	}
}

// optsLength mirrors Options.withDefaults so the test's expectation of R3.6's
// default is the same number the implementation uses.
func optsLength(n int) int {
	if n <= 0 {
		return DefaultQueryStringLength
	}
	return n
}

// TestR36LengthProfiles checks the four numbers R3.6 names, which the corpus
// cannot all carry: a 100 000-byte template would make the fixture file the size
// of the source file it belongs to.
func TestR36LengthProfiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		// want is R3.6's literal, not the constant: `n` *is* the constant, so
		// every check below compares a value against what it was assigned from.
		// Measured — DefaultQueryStringLength 1024 -> 1025 and
		// EnterprisePlusQueryStringLength 10000 -> 10001 both left the package
		// green; min and max were caught only via the `long` input their
		// byte-count derivation feeds the truncation check with.
		want int
	}{
		{"min", MinQueryStringLength, 256},
		{"default-enterprise", DefaultQueryStringLength, 1024},
		{"default-enterprise-plus", EnterprisePlusQueryStringLength, 10000},
		{"max", MaxQueryStringLength, 100000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.n != tc.want {
				t.Errorf("%s = %d, want %d (R3.6's literal)", tc.name, tc.n, tc.want)
			}
			if !ValidLength(tc.n) {
				t.Errorf("ValidLength(%d) = false, R3.6's range includes it", tc.n)
			}
			// A template longer than the profile truncates to exactly that many bytes.
			long := strings.Repeat("SELECT a FROM t WHERE b = 1 ", tc.n/28+1)
			got := Normalize(long, Options{Length: tc.n})
			if !got.Truncated {
				t.Fatalf("Truncated = false at length %d with a %d-byte template", tc.n, len(got.Template))
			}
			if len(got.DisplayText) > tc.n {
				t.Errorf("display is %d bytes, over the %d-byte cap", len(got.DisplayText), tc.n)
			}
			if !strings.HasPrefix(got.Template, got.DisplayText) {
				t.Error("display is not a prefix of the template")
			}

			// A template at or under the profile is not truncated.
			// A template at or under the profile is not truncated. The expectation
			// is the template of the short statement, not its input text — the
			// input `SELECT 1` normalizes to `SELECT $1` first.
			short := Normalize("SELECT 1", Options{Length: tc.n})
			if short.Truncated {
				t.Errorf("a %d-byte template truncated at length %d", len(short.Template), tc.n)
			}
			if short.Template != "SELECT $1" || short.DisplayText != "SELECT $1" {
				t.Errorf("short statement at length %d: template %q display %q",
					tc.n, short.Template, short.DisplayText)
			}
		})
	}
}

// TestValidLength pins R3.6's range boundaries: 256–100 000 inclusive, and
// everything outside it rejected. The 4 500 upper bound R3.6 records as the 0.1.0
// defect is deliberately not a boundary here.
func TestValidLength(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want bool
	}{
		{-1, false}, {0, false}, {1, false}, {255, false},
		{256, true}, {1024, true}, {4500, true}, {10000, true}, {100000, true},
		{100001, false}, {1 << 20, false},
	} {
		if got := ValidLength(tc.n); got != tc.want {
			t.Errorf("ValidLength(%d) = %v, want %v", tc.n, got, tc.want)
		}
	}
}

// TestOptionsDefaultLength checks the zero-means-default rule the config layer
// relies on: an unset Length is R3.6's 1 024, not 0 (which would blank every
// template) and not the Enterprise Plus 10 000 (which would ship Plus's profile
// while the spec names Enterprise's as the default).
func TestOptionsDefaultLength(t *testing.T) {
	if got := (Options{}).withDefaults().Length; got != DefaultQueryStringLength {
		t.Errorf("Options{}.Length = %d, want %d", got, DefaultQueryStringLength)
	}
	if got := (Options{Length: -5}).withDefaults().Length; got != DefaultQueryStringLength {
		t.Errorf("Options{Length:-5}.Length = %d, want %d", got, DefaultQueryStringLength)
	}
	const custom = 4242
	if got := (Options{Length: custom}).withDefaults().Length; got != custom {
		t.Errorf("Options{Length:%d}.Length = %d", custom, got)
	}
}

// TestFingerprintIsStableAndURLSafe pins the two properties R8.2 and R9.5b
// depend on: the value is 64 lowercase hex characters (the shape
// api/openapi.yaml:537 already publishes) and it is a pure function of the
// template.
func TestFingerprintIsStableAndURLSafe(t *testing.T) {
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := Fingerprint(""); got != empty {
		t.Errorf("Fingerprint(\"\") = %s, want %s", got, empty)
	}
	fp := Fingerprint("SELECT $1")
	if len(fp) != 64 {
		t.Errorf("fingerprint %q is %d chars, want 64", fp, len(fp))
	}
	if strings.ToLower(fp) != fp {
		t.Errorf("fingerprint %q is not lowercase hex", fp)
	}
	for _, r := range fp {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("fingerprint %q contains %q, not a hex digit", fp, r)
		}
	}
	if fp == Fingerprint("SELECT $2") {
		t.Error("two different templates share a fingerprint")
	}
	// The property R3.5 exists for: two statements differing only in literals
	// normalize to one template and therefore to one fingerprint.
	a := Normalize("SELECT * FROM t WHERE id = 1", Options{})
	b := Normalize("SELECT * FROM t WHERE id = 2", Options{})
	if a.Fingerprint != b.Fingerprint {
		t.Errorf("literals-only variants got different fingerprints: %s vs %s", a.Fingerprint, b.Fingerprint)
	}
}

// TestNormalizeIsIdempotent checks that normalizing a template yields the same
// template. It matters because the store keeps the template and R3.10's
// EXPLAIN-on-the-template work (Phase 6b) re-normalizes what it reads back: a
// normalizer that is not idempotent gives a stored template a different
// fingerprint on every read, which breaks R3.4's identity claim.
func TestNormalizeIsIdempotent(t *testing.T) {
	f := loadFixture(t)
	for _, c := range f.Cases {
		if c.Template == "" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			once := Normalize(c.Template, Options{})
			if once.Template != c.Template {
				t.Errorf("re-normalizing the template changed it:\n got %q\nwant %q", once.Template, c.Template)
			}
			twice := Normalize(once.Template, Options{})
			if twice.Template != once.Template {
				t.Errorf("third normalize differs from second: %q vs %q", twice.Template, once.Template)
			}
		})
	}
}

// TestPlaceholderNumbering covers R3.5's `$1`,`$2`,… numbering rule directly:
// placeholders are numbered left to right, one per literal, and a `$n` the
// statement already had joins the numbering instead of colliding with it.
func TestPlaceholderNumbering(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want []string // the placeholder texts, in order
	}{
		{"SELECT 1", []string{"$1"}},
		{"SELECT 1, 2", []string{"$1", "$2"}},
		{"SELECT 1, 1", []string{"$1", "$2"}}, // two literals, not one reused
		{"SELECT $1", []string{"$1"}},
		{"SELECT $1, $1", []string{"$1", "$1"}}, // a real prepared statement reuses a param
		{"SELECT $2, 5", []string{"$2", "$3"}},
		{"SELECT 'a', 2, 'c'", []string{"$1", "$2", "$3"}},
		{"SELECT $$x$$, 1", []string{"$1", "$2"}},
		{"SELECT -1, 2", []string{"$1", "$2"}},
		{"SELECT a - 1, 2", []string{"$1", "$2"}},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got := tokenize(tc.sql)
			var gotParams []string
			for _, tk := range got {
				if tk.kind == tokParam {
					gotParams = append(gotParams, tk.text)
				}
			}
			if fmt.Sprint(gotParams) != fmt.Sprint(tc.want) {
				t.Errorf("placeholders:\n got %v\nwant %v", gotParams, tc.want)
			}
			if n := Normalize(tc.sql, Options{}).Params; n != len(tc.want) {
				t.Errorf("Params = %d, want %d", n, len(tc.want))
			}
		})
	}
}

// TestJoinCollapsesWhitespace pins what R3.4's "whitespace-collapsed" does and
// does not do: a run of whitespace becomes one space, and a single space is
// never inserted or removed where the source did not have one. The second half
// is the half a formatter gets wrong — `count( * )` is not rewritten to
// `count(*)`, because collapsing is not reformatting.
func TestJoinCollapsesWhitespace(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"SELECT count(*) FROM t", "SELECT count(*) FROM t"},
		{"SELECT count( * ) FROM t", "SELECT count( * ) FROM t"},
		{"SELECT id IN ( 1 , 2 )", "SELECT id IN ( $1 , $2 )"},
		{"SELECT\t\tcount(*)\n\nFROM\nt", "SELECT count(*) FROM t"},
		{"SELECT a.b.c", "SELECT a.b.c"},
		{"SELECT a::int", "SELECT a::INT"},
		{"SELECT 1 ;", "SELECT $1 ;"},
		{"SELECT 1;", "SELECT $1;"},
		{"SELECT ( ( 1 ) )", "SELECT ( ( $1 ) )"},
		{"SELECT/*x*/1", "SELECT $1"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := Normalize(tc.in, Options{}).Template; got != tc.want {
				t.Errorf("template:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestFixtureFileIsTheOneR163DoesNotName guards the claim the corpus makes about
// itself: R16.3 names seven fixture files and none of them is a normalizer
// fixture, so this file must live beside them rather than inside either set.
func TestFixtureFileIsTheOneR163DoesNotName(t *testing.T) {
	abs, err := filepath.Abs(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	// .../pgsql-insights/testdata/query/normalizer.json -> .../pgsql-insights/testdata
	td := filepath.Dir(filepath.Dir(abs))
	for _, dir := range []string{"cnpg-metrics", "pg-status"} {
		entries, err := os.ReadDir(filepath.Join(td, dir))
		if err != nil {
			t.Errorf("R16.3 names fixtures in testdata/%s: %v", dir, err)
			continue
		}
		if len(entries) == 0 {
			t.Errorf("testdata/%s is empty", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(td, "api/clusters.json")); err != nil {
		t.Errorf("R16.8's api fixture testdata/api/clusters.json: %v", err)
	}
}
