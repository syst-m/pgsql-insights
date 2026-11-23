package collector

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Overrides is R2.3's two override knobs, as data.
//
// R2.3 asks for them so that "a lab can run with two collectors, and a
// production user can disable an expensive one". Both are expressed here as
// plain values rather than as flags because the flag layer is R11.8's job
// (internal/config/, the phase's fifth commit) and the precedence chain it
// defines — flag, then env, then ConfigMap, then default — has to be applied to
// these the same way it is applied to every other setting. A registry that took
// a *flag.FlagSet would be a registry that could only be configured by flags.
type Overrides struct {
	// Intervals replaces a collector's declared interval, keyed by collector
	// name: R2.3's `--interval.database=5m`. A name present here but not
	// registered is an error, not a no-op — see Validate's comment for why an
	// ignored key is the dangerous default.
	Intervals map[string]time.Duration
	// Disabled names collectors switched off: R2.3's `--disable-collector=a,b`.
	// A disabled collector stays in the registry (so /api/v1 can report that it
	// exists and is off, and so re-enabling it is a flag change rather than a
	// code change) and is simply never scheduled.
	Disabled []string
}

// Registry is the set of collectors this process runs, with R2.3's overrides
// applied.
//
// The registry exists as a type rather than a []Collector argument because
// applying R2.3 is where the mistakes are: an override addressed to a collector
// that does not exist, a duplicate registration, a disabled list with a typo.
// Each of those is a configuration that looks applied and is not, which is the
// failure class this project has already been bitten by twice on the API side
// (R16.8's `raw.clusters ?? []` and the readiness flag) — the fix in both cases
// was to make the wrong answer impossible rather than to document it, and that
// is what NewRegistry's validation is.
type Registry struct {
	entries []Entry
	byName  map[string]Entry
}

// Entry is one registered collector: the collector plus the effective schedule
// the registry resolved for it.
type Entry struct {
	Col Collector
	// Desc is Col.Descriptor(), cached at registration so the scheduler does not
	// call a collector method in a hot loop and give a badly written collector a
	// reason to be slow.
	Desc Descriptor
	// Interval is the declared interval with any R2.3 override applied. The
	// scheduler reads this and never Desc.Interval, so there is exactly one
	// answer to "how often does this run".
	Interval time.Duration
	// Enabled is false when R2.3's --disable-collector named this one.
	Enabled bool
}

// NewRegistry validates and registers cols, applying ov.
//
// The checks, each because the alternative fails silently:
//
//   - A duplicate name is an error. Two collectors both called `wal` produce one
//     pgsi_collector_duration_seconds{collector="wal"} series fed by two
//     sources, which is a metric that lies rather than one that is missing.
//   - An override or disable entry naming an unregistered collector is an
//     error. `--disable-collector=databse` silently leaves the collector on and
//     the user believing they turned it off; R2.3's whole use case is that the
//     flag does what it says.
//   - A non-positive interval override is an error. A zero here would mean
//     "run as fast as the scheduler loops", which on a 50-cluster fleet is a
//     hot loop against every instance — and R2.9 exists precisely to stop that
//     class of fan-out.
//   - A descriptor that fails Descriptor.Validate() is an error, checked before
//     anything else, because the name it reports is the key every other check
//     uses.
func NewRegistry(cols []Collector, ov Overrides) (*Registry, error) {
	r := &Registry{
		entries: make([]Entry, 0, len(cols)),
		byName:  make(map[string]Entry, len(cols)),
	}
	for i, c := range cols {
		if c == nil {
			return nil, fmt.Errorf("collector: nil collector at index %d", i)
		}
		d := c.Descriptor()
		if err := d.Validate(); err != nil {
			return nil, err
		}
		if _, dup := r.byName[d.Name]; dup {
			return nil, fmt.Errorf("collector %q: registered twice (two collectors with one name feed one R2.2 series from two sources)", d.Name)
		}
		e := Entry{Col: c, Desc: d, Interval: d.Interval, Enabled: true}
		r.entries = append(r.entries, e)
		r.byName[d.Name] = e
	}

	for name, iv := range ov.Intervals {
		if _, ok := r.byName[name]; !ok {
			return nil, fmt.Errorf("collector %q: --interval.%s given for an unregistered collector (R2.3 addresses a collector by name; a typo is not a no-op)", name, name)
		}
		if iv <= 0 {
			return nil, fmt.Errorf("collector %q: interval override %s must be > 0", name, iv)
		}
	}
	for _, name := range ov.Disabled {
		if _, ok := r.byName[name]; !ok {
			return nil, fmt.Errorf("collector %q: --disable-collector names an unregistered collector (R2.3 addresses a collector by name; a typo silently leaves it enabled)", name)
		}
	}
	for name, iv := range ov.Intervals {
		e := r.byName[name]
		e.Interval = iv
		r.byName[name] = e
		for i := range r.entries {
			if r.entries[i].Desc.Name == name {
				r.entries[i].Interval = iv
			}
		}
	}
	for _, name := range ov.Disabled {
		e := r.byName[name]
		e.Enabled = false
		r.byName[name] = e
		for i := range r.entries {
			if r.entries[i].Desc.Name == name {
				r.entries[i].Enabled = false
			}
		}
	}
	return r, nil
}

// Enabled returns the entries the scheduler should run, sorted by name so the
// order a round dispatches in is deterministic. Sorting is not cosmetic: an
// unsorted map iteration makes each round's dispatch order differ, which turns
// a timing regression that reproduces locally into one that does not.
func (r *Registry) Enabled() []Entry {
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if e.Enabled {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Desc.Name < out[j].Desc.Name })
	return out
}

// All returns every registered entry, enabled or not, sorted by name. This is
// what an API surface reports so that a disabled collector is visible as
// disabled rather than absent.
func (r *Registry) All() []Entry {
	out := make([]Entry, len(r.entries))
	copy(out, r.entries)
	sort.Slice(out, func(i, j int) bool { return out[i].Desc.Name < out[j].Desc.Name })
	return out
}

// Get looks one collector up by name.
func (r *Registry) Get(name string) (Entry, bool) {
	e, ok := r.byName[name]
	return e, ok
}

// intervalFlagPrefix is the prefix of R2.3's `--interval.<collector>=<dur>`
// form. The flag name is dynamic — the collector name is part of it — which is
// why this needs a pre-scan instead of a flag.String.
const intervalFlagPrefix = "--interval."

// IntervalOverridesFromArgs pre-scans a raw argument list for R2.3's
// `--interval.<collector>=<duration>` entries and returns what it found plus the
// args with those entries removed, so the remainder can go to flag.Parse without
// tripping over names the flag set does not define.
//
// This mirrors logx.FieldsFromArgs exactly, for the same reason: Go's flag
// package only accepts names it was told about and cannot enumerate them
// dynamically, so `--interval.database=45s` would be "flag provided but not
// defined". CNPG/machinery get the shape because cobra allows arbitrary flags;
// the scan reproduces the same CLI surface with the stdlib.
//
// The asymmetry with --disable-collector is deliberate and worth stating: that
// one *is* a single static flag (its value is the list, `--disable-collector=a,b`),
// so cmd/pgsi declares it with fs.String and splits on commas. Only the interval
// form has a dynamic *name*, and only it needs this function.
//
// Forms handled:
//
//	--interval.database=45s   -> {database: 45s}
//	--interval.database 45s   -> {database: 45s}  (only when the next arg is
//	                                               not itself a flag)
//	--interval.wal=0          -> error at parse time, not at registration
//
// Names are not validated against the registry here — the registry is built
// after parsing, and logx's scanner validates nothing either. NewRegistry is
// what turns `--interval.datbase=45s` into an error.
//
// Scanning stops at a bare `--`, after which the stdlib treats everything as
// positional and this function stops rewriting the list.
func IntervalOverridesFromArgs(args []string) (map[string]time.Duration, []string, error) {
	out := map[string]time.Duration{}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if !strings.HasPrefix(a, intervalFlagPrefix) {
			rest = append(rest, a)
			continue
		}
		// --interval.<name>=<value> or --interval.<name> <value>. The name is
		// everything up to the first `=` after the prefix; a name containing
		// whitespace would mean the shell split the argument, which is a usage
		// error worth naming rather than a collector called "database ".
		name, value, hasValue := strings.Cut(strings.TrimPrefix(a, intervalFlagPrefix), "=")
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, " \t") {
			return nil, nil, fmt.Errorf("invalid %s: expected --interval.<collector>=<duration>", a)
		}
		if !hasValue {
			// The space-separated form is only valid when the next argument is
			// not itself a flag: `--interval.wal --disable-collector=x` must not
			// consume `--disable-collector=x` as a duration.
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return nil, nil, fmt.Errorf("invalid %s: missing duration value", a)
			}
			value = args[i+1]
			i++
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return nil, nil, fmt.Errorf("collector %q: invalid interval %q: %w", name, value, err)
		}
		if d <= 0 {
			return nil, nil, fmt.Errorf("collector %q: interval %s must be > 0", name, d)
		}
		if _, dup := out[name]; dup {
			return nil, nil, fmt.Errorf("collector %q: interval given twice (the last one wins in Go's flag package; here that is an error so a duplicated flag is noticed)", name)
		}
		out[name] = d
	}
	return out, rest, nil
}

// DisabledFromFlag splits R2.3's `--disable-collector=a,b` value into names.
// Empty entries are dropped so `a,,b` and `a,` are accepted (a trailing comma in
// a shell-quoted list is a common typo) while a value that is entirely empty
// means "nothing disabled".
func DisabledFromFlag(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
