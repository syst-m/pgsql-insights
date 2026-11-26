package cnpgmetrics

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Sample is one sample line of a family: the label set that identifies the series
// and the value.
//
// These are the *exposition's* labels — `cnpg_pg_stat_database_xact_commit`
// carries `datname`, `cnpg_pgbouncer_pools_cl_active` carries `pool`, `database`,
// `user`. They are not the R9.3 pgsi label contract, and they must not be
// conflated with it: R9.3's set (cluster, namespace, pod, instance_role, plus the
// per-family source labels) is assembled by internal/collector/labels.go at
// export time from the collector Target, and a cnpg sample's own labels are the
// per-series keys that go *into* it. Copying the exposition's labels straight onto
// an exported pgsi series is how an exporter ends up with a series labelled
// `datname` in one place and `database` in another for the same fact.
type Sample struct {
	// Labels is the parsed brace block. Nil for a sample line with no braces, and
	// an empty (non-nil) map for `name{} 1`, which the text format allows and the
	// fixtures contain.
	Labels map[string]string
	// Value is the parsed float. Prometheus's text format has no integer type —
	// counters and gauges are both float64 — so there is no second numeric type
	// here to reach for.
	Value float64
}

// Metric is one parsed family.
type Metric struct {
	// Name is the family name exactly as the `# TYPE` line declared it, including
	// the `cnpg_` prefix. It is never the pgsi name: R9.2's prefix is applied at
	// render time by internal/collector/metrics.go, and a family name that bakes
	// a prefix in is a family that cannot change its prefix.
	Name string
	// Type is the declared type verbatim — one of the five in TypeValid. It is a
	// plain string rather than metrictype.MetricType because the text format has
	// five types and metrictype.MetricType has three (counter, gauge, and the empty
	// "not exported" marker); widening that enum for summary and histogram is
	// R9.13's business, and until it happens the parser reports what the body
	// said instead of rounding it to the nearest thing the enum can spell.
	Type string
	// Help is the family's `# HELP` text, empty when the exporter sent none.
	Help string
	// Family is the registry entry this metric came from, nil when the family is
	// outside the cnpg surface R2.6 scopes (a `go_*` family, say). Non-nil for
	// every family in Scrape.CNPG.
	//
	// The pointer is the point: a caller can ask "is this one of ours" without a
	// second lookup, and the zero Family (empty name, empty type) is not a value
	// that could be mistaken for a real entry.
	Family *Family
	// Samples are the family's series. A counter family with no labels has exactly
	// one; a labelled one has one per label set; a summary has one per quantile
	// plus _sum and _count, all folded into this one Metric rather than spread
	// across phantom families.
	Samples []Sample
}

// Scrape is one parsed `/metrics` body.
//
// The two slices are the requirement's two halves. CNPG is the `cnpg_*` and
// `cnpg_collector_*` surface R2.6 names; Other is everything else in the same body
// (`go_*` on an instance pod, and on a pooler pod the pgbouncer exporter's own
// runtime families), parsed so that a caller can count what was served but kept
// out of the typed surface because R2.6's sentence does not extend to it.
type Scrape struct {
	// CNPG holds every family in the registry that the body served, in body order.
	CNPG []Metric
	// Other holds every family the body served that is not in the registry.
	Other []Metric

	byName map[string]*Metric
}

// Parse turns a raw exposition body into a Scrape.
//
// The rules it enforces, and why each one is a rule rather than a detail:
//
//   - Families come from `# TYPE` lines, never from sample-line prefixes. This is
//     the one place a naive Prometheus parser is wrong, and it is wrong on every
//     one of our fixtures: `go_gc_duration_seconds` is a summary whose samples are
//     `go_gc_duration_seconds_sum` and `go_gc_duration_seconds_count`, so a parser
//     that groups by sample-line name reports two extra families per file — eight
//     across the R16.3 set. TestParseFixtures asserts the exact family count per
//     fixture, which is the only way that stays caught.
//   - A sample line whose family has no `# TYPE` declaration is an error. The text
//     format requires the declaration, and a body that lost one mid-write (the
//     classic truncated scrape) otherwise parses fine and reports a family with a
//     type nobody declared.
//   - A `cnpg_*` family the registry has no row for is an error. This is R2.6's
//     "not a generic Prometheus scrape passthrough" turned into a behaviour: a
//     passthrough accepts any name, so a CNPG version that renames a family reads
//     as "the metric vanished" with nothing to compare against. Refusing to parse
//     an unrecognised cnpg family is what forces the registry to be updated in the
//     same change that ships against a new exporter.
//   - A `cnpg_*` family whose declared type disagrees with the registry's is an
//     error. A family that is a gauge in the body and a counter in our table means
//     one of the two is wrong about a fact we can only learn from the body, and
//     `promtool check metrics` rejects the export for it.
//   - A duplicate `# TYPE` for one family is an error, including a duplicate that
//     agrees. Two declarations is a body that concatenated two scrapes.
func Parse(raw string) (*Scrape, error) {
	return parse(raw, "")
}

// parse is Parse with a location label. Every error it returns is
// "cnpgmetrics: <label>line N: ...", where the label is empty for Parse and the
// path for ParseFile — so a caller reading an error from a fixture set knows which
// of the four bodies it came from without the two entry points prefixing twice.
func parse(raw, at string) (*Scrape, error) {
	declared, err := scanDeclarations(raw, at)
	if err != nil {
		return nil, err
	}

	s := &Scrape{byName: make(map[string]*Metric, len(declared.order))}
	// Build the metrics in declaration order so the slices are stable for a given
	// body; the samples are appended in body order.
	for _, name := range declared.order {
		d := declared.all[name]
		m := &Metric{Name: name, Type: d.typ, Help: d.help}
		if strings.HasPrefix(name, "cnpg") {
			f, ok := Lookup(name)
			if !ok {
				return nil, fmt.Errorf("cnpgmetrics: %sline %d: family %q is a cnpg family with no registry entry (R2.6 forbids parsing an unrecognised cnpg_* family as a passthrough — add it to registry.go)", at, d.line, name)
			}
			if string(f.Type) != d.typ {
				return nil, fmt.Errorf("cnpgmetrics: %sline %d: family %s declared %q but registry says %q", at, d.line, name, d.typ, f.Type)
			}
			m.Family = &f
		}
		s.byName[name] = m
	}

	// Second pass: the sample lines.
	line := 0
	for _, text := range strings.Split(raw, "\n") {
		line++
		text = strings.TrimRight(text, "\r")
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, labels, value, err := parseSampleLine(trimmed)
		if err != nil {
			return nil, fmt.Errorf("cnpgmetrics: %sline %d: %w", at, line, err)
		}
		owner, err := resolveOwner(declared, name)
		if err != nil {
			return nil, fmt.Errorf("cnpgmetrics: %sline %d: %w", at, line, err)
		}
		m := s.byName[owner]
		m.Samples = append(m.Samples, Sample{Labels: labels, Value: value})
	}

	for _, name := range declared.order {
		m := s.byName[name]
		if m.Family != nil {
			s.CNPG = append(s.CNPG, *m)
		} else {
			s.Other = append(s.Other, *m)
		}
	}
	return s, nil
}

// ParseFile reads a fixture off disk and parses it. R16.3's recorded bodies are the
// only inputs this is tested against, so the file-reading half lives beside Parse
// rather than in the test, and a caller collecting live (R2.6's "from S3") gets the
// same entry point shape.
func ParseFile(path string) (*Scrape, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cnpgmetrics: read %s: %w", path, err)
	}
	return parse(string(b), path+": ")
}

// Lookup returns a parsed family by name, covering both the cnpg surface and the
// families in Other.
func (s *Scrape) Lookup(name string) (*Metric, bool) {
	m, ok := s.byName[name]
	return m, ok
}

// Total returns the number of families in the body, which is len(CNPG)+len(Other)
// and exists so a caller can assert "this body served 118 families" without
// knowing the split.
func (s *Scrape) Total() int { return len(s.CNPG) + len(s.Other) }

// TypeValid are the five types the Prometheus text format defines. The parser
// rejects anything else rather than defaulting it to untyped, because a typo'd
// `# TYPE x coutner` otherwise becomes a family with a type nobody asked for.
var TypeValid = []string{"counter", "gauge", "summary", "histogram", "untyped"}

func validType(t string) bool {
	for _, v := range TypeValid {
		if v == t {
			return true
		}
	}
	return false
}

// decl is a parsed `# TYPE`/`# HELP` pair for one family.
type decl struct {
	line int
	typ  string
	help string
}

// declarations is the pass-one result: the map plus the order the declarations
// appeared in. The order is kept because Go map iteration is randomised and a
// parser whose output order depends on a hash is a parser whose tests fail at
// random.
type declarations struct {
	all   map[string]decl
	order []string
}

func scanDeclarations(raw, at string) (*declarations, error) {
	d := &declarations{all: map[string]decl{}}
	line := 0
	for _, text := range strings.Split(raw, "\n") {
		line++
		text = strings.TrimRight(text, "\r")
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		// "# TYPE name type" / "# HELP name text"; anything else is a comment we
		// are told to ignore.
		body := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		kind, rest, ok := strings.Cut(body, " ")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		switch kind {
		case "TYPE":
			name, typ, ok := strings.Cut(rest, " ")
			if !ok || name == "" || typ == "" {
				return nil, fmt.Errorf("cnpgmetrics: %sline %d: malformed # TYPE %q", at, line, rest)
			}
			if !validMetricName(name) {
				return nil, fmt.Errorf("cnpgmetrics: %sline %d: %q is not a valid metric name", at, line, name)
			}
			if !validType(typ) {
				return nil, fmt.Errorf("cnpgmetrics: %sline %d: unknown type %q for %s", at, line, typ, name)
			}
			if _, dup := d.all[name]; dup {
				return nil, fmt.Errorf("cnpgmetrics: %sline %d: duplicate # TYPE for %s", at, line, name)
			}
			d.all[name] = decl{line: line, typ: typ}
			d.order = append(d.order, name)
		case "HELP":
			name, help, ok := strings.Cut(rest, " ")
			if !ok {
				continue
			}
			existing, dup := d.all[name]
			if !dup {
				// A HELP with no TYPE anywhere is not an error at scan time; the
				// sample-line pass is where a family without a declaration is
				// caught, and a HELP-only line declares nothing.
				continue
			}
			if existing.help == "" {
				existing.help = help
				d.all[name] = existing
			}
		}
	}
	return d, nil
}

// resolveOwner maps a sample line's metric name to the family that owns it.
//
// Direct match first. If that fails, the summary/histogram suffix fold: a summary's
// samples are `x_sum`, `x_count`, `x_created` and a histogram's are `x_bucket`,
// `x_sum`, `x_count`, none of which is the declared family name. The fold is
// suffix-and-type paired (`_bucket` only folds onto a histogram) rather than
// suffix-only, so a counter named `x` and a gauge named `x_sum` cannot be merged by
// accident.
func resolveOwner(d *declarations, name string) (string, error) {
	if _, ok := d.all[name]; ok {
		return name, nil
	}
	for _, sfx := range []string{"_sum", "_count", "_bucket", "_created"} {
		base, ok := strings.CutSuffix(name, sfx)
		if !ok {
			continue
		}
		d0, ok := d.all[base]
		if !ok {
			continue
		}
		switch {
		case sfx == "_bucket" && d0.typ != "histogram":
			continue
		case (sfx == "_sum" || sfx == "_count") && d0.typ != "summary" && d0.typ != "histogram":
			continue
		case sfx == "_created" && d0.typ != "counter":
			continue
		}
		return base, nil
	}
	return "", fmt.Errorf("metric %q has no # TYPE declaration", name)
}

// parseSampleLine splits "name{a=\"1\",b=\"2\"} 3.5" into its three parts.
func parseSampleLine(s string) (name string, labels map[string]string, value float64, err error) {
	name, rest, ok := strings.Cut(s, " ")
	if !ok {
		return "", nil, 0, fmt.Errorf("malformed sample line %q", s)
	}
	rest = strings.TrimSpace(rest)

	if i := strings.IndexByte(name, '{'); i >= 0 {
		// "name{...} value" — the brace block is part of the name field here
		// because the space that ends the name is inside the braces when the
		// labels contain a space, so re-split from the brace.
		block, after, ok := strings.Cut(s[i+1:], "}")
		if !ok {
			return "", nil, 0, fmt.Errorf("unterminated label block in %q", s)
		}
		labels, err = parseLabels(block)
		if err != nil {
			return "", nil, 0, err
		}
		name = name[:i]
		rest = strings.TrimSpace(after)
	}
	if !validMetricName(name) {
		return "", nil, 0, fmt.Errorf("%q is not a valid metric name", name)
	}
	// A sample line may carry a trailing timestamp: "x 1 1659781676000". The text
	// format allows it; the exporter never sends one on 9187, and R2.6's S3 row
	// has no timestamp either, so it is accepted and dropped rather than parsed —
	// the sample's time comes from the scrape, not from the line.
	v, _, _ := strings.Cut(rest, " ")
	value, err = strconv.ParseFloat(v, 64)
	if err != nil {
		return "", nil, 0, fmt.Errorf("invalid value %q", rest)
	}
	return name, labels, value, nil
}

// parseLabels parses the inside of a brace block: `a="1",b="2"`.
func parseLabels(block string) (map[string]string, error) {
	if strings.TrimSpace(block) == "" {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	for block != "" {
		i := strings.IndexByte(block, '=')
		if i < 0 {
			return nil, fmt.Errorf("label block ends with a dangling %q", block)
		}
		key := strings.TrimSpace(block[:i])
		if !validLabelName(key) {
			return nil, fmt.Errorf("%q is not a valid label name", key)
		}
		block = block[i+1:]
		if block == "" || block[0] != '"' {
			return nil, fmt.Errorf("label %s: value is not quoted", key)
		}
		var sb strings.Builder
		block = block[1:]
		closed := false
		for len(block) > 0 {
			switch block[0] {
			case '\\':
				if len(block) < 2 {
					return nil, fmt.Errorf("label %s: trailing backslash", key)
				}
				switch block[1] {
				case '"', '\\':
					sb.WriteByte(block[1])
				case 'n':
					sb.WriteByte('\n')
				default:
					return nil, fmt.Errorf("label %s: invalid escape \\%c", key, block[1])
				}
				block = block[2:]
			case '"':
				block = block[1:]
				closed = true
			default:
				sb.WriteByte(block[0])
				block = block[1:]
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, fmt.Errorf("label %s: unterminated value", key)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("duplicate label %s", key)
		}
		out[key] = sb.String()
		block = strings.TrimPrefix(block, ",")
		block = strings.TrimLeft(block, " ")
	}
	return out, nil
}

func validMetricName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9' && i > 0) || (c == ':' && i > 0)
		if !ok {
			return false
		}
	}
	return true
}

func validLabelName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && i > 0)) {
			return false
		}
	}
	return true
}
