// Package logx builds the process logger (R13.7).
//
// R13.7 asks for three things in one sentence: JSON output, a configurable
// level, and `--log-field-*` "compatible with the CNPG/machinery log flag shape
// so log pipelines treat us like CNPG". The first two are a slog call; the
// third is the reason this package exists rather than a slog.New line in
// cmd/pgsi.
//
// # The flag shape
//
// Go's stdlib flag package cannot invent flag names at runtime, so a literal
// `--log-field-cluster=pg1` (the CNPG shape, where the field *name* is part of
// the flag) needs a pre-scan of the argument list before flag.Parse sees it.
// FieldsFromArgs does that scan and hands back the remaining args, so both
// spellings work and neither is a lie:
//
//	--log-field cluster=pg1        repeatable, standard flag.Value semantics
//	--log-field-cluster=pg1        R13.7's literal `--log-field-*` shape
//	--log-field-cluster pg1        same, space-separated (flag.Value style)
//
// Both end up as structured fields on every log line, which is what the
// requirement is actually for: a pipeline that indexes CNPG's `cluster` field
// finds ours under the same key.
//
// # The level
//
// R13.7's second clause — "`--log-level=debug` that dumps the SQL executed per
// collector" — makes the level a product surface rather than a preference:
// debug is where a collector's executed queries appear. The collectors
// themselves are Phase 3, so the gate lives here (every collector logs through
// this logger at Debug) and the SQL lines arrive with them.
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// DefaultWriter is where the process logger writes. stdout, not stderr: the
// chart's pods are distroless and everything that reads the logs (kubectl
// logs, Loki, CNPG's own pipeline conventions) expects normal output on stdout.
var DefaultWriter io.Writer = os.Stdout

// Field is one structured key/value pair attached to every log line.
type Field struct {
	Key   string
	Value string
}

// ParseLevel maps R13.7's `--log-level` values onto slog levels. The set is
// the four names every Go logger agrees on, plus `warning` as an alias for
// `warn` (the spelling slog's own String() prints) and `""` meaning info.
// Anything else is an error rather than a silent fallback to info: a typo in a
// deployed flag value (`--log-level=verbse`) should fail loudly at startup,
// not quietly change what the pod logs.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (supported: debug, info, warn, error)", s)
	}
}

// New returns the JSON logger R13.7 asks for, at `level`, with `fields` on
// every line. It never returns nil.
//
// AddSource is off: slog's caller lookup costs a runtime.CallersFrames walk
// per record, and R13.7 asks for the CNPG field set, not Go file/line
// (CNPG's own JSON emits `logging.googleapis.com/sourceLocation`, which a
// pipeline keyed on it cannot map onto a Go frame anyway).
func New(level slog.Level, fields ...Field) *slog.Logger {
	l := slog.New(slog.NewJSONHandler(DefaultWriter, &slog.HandlerOptions{Level: level}))
	if len(fields) == 0 {
		return l
	}
	attrs := make([]any, 0, len(fields))
	for _, f := range fields {
		attrs = append(attrs, f.Key, f.Value)
	}
	return l.With(attrs...)
}

// NewFromFlags is the one call cmd/pgsi makes: parse the two R13.7 flags and
// build the logger. A bad level is an error the command prints and exits on,
// not a logger that silently picks a different one.
func NewFromFlags(level string, fields Fields) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	return New(lvl, fields...), nil
}

// Fields is the flag value behind `--log-field` (R13.7). It implements
// flag.Value, so the stdlib package gives it repeatable semantics for free:
// the last form in the flag set's own usage output is `--log-field k=v`, and
// repeating the flag accumulates.
//
// It is a pointer-receiver value so flag.Set can append; the zero Fields is a
// valid empty set.
type Fields []Field

// Set parses one `key=value` pair. The split is on the *first* `=`, so a value
// may contain more (`--log-field dsn=user=app` keeps `user=app` intact). An
// empty value is allowed (`--log-field shard=`) — a field that exists with an
// empty value is a different fact from one that is absent, which is the CNPG
// convention for "no cluster selected yet".
func (f *Fields) Set(s string) error {
	key, value, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("invalid log field %q: expected key=value", s)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("invalid log field %q: empty key", s)
	}
	if strings.ContainsAny(key, " \t") {
		return fmt.Errorf("invalid log field %q: key contains whitespace", s)
	}
	*f = append(*f, Field{Key: key, Value: value})
	return nil
}

// String renders the set for flag's usage output.
func (f Fields) String() string {
	if len(f) == 0 {
		return ""
	}
	parts := make([]string, 0, len(f))
	for _, fl := range f {
		parts = append(parts, fl.Key+"="+fl.Value)
	}
	return strings.Join(parts, ",")
}

// Get returns the value for flag's reflection path.
func (f *Fields) Get() any { return *f }

// logFieldPrefix is the prefix of R13.7's `--log-field-*` spelling.
const logFieldPrefix = "--log-field-"

// FieldsFromArgs pre-scans a raw argument list for the `--log-field-<key>`
// form and returns the fields it found plus the args with those entries
// removed, so the remainder can go to flag.Parse without tripping over names
// the flag set does not define.
//
// Why a pre-scan: Go's flag package only accepts names it was told about, and
// it cannot enumerate them dynamically — `--log-field-cluster=pg1` would be
// "flag provided but not defined". CNPG/machinery gets the shape because
// cobra allows arbitrary flags; here the scan reproduces the same CLI surface.
//
// Forms handled:
//
//	--log-field-cluster=pg1   -> {cluster: pg1}
//	--log-field-cluster pg1   -> {cluster: pg1}   (only when the next arg is
//	                                               not itself a flag)
//	--log-field-cluster=      -> {cluster: ""}
//
// Scanning stops at a bare `--`, after which the stdlib treats everything as
// positional and this function stops rewriting the list.
func FieldsFromArgs(args []string) (Fields, []string, error) {
	var fields Fields
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// Everything after the separator is positional; keep it verbatim.
			rest = append(rest, args[i:]...)
			break
		}
		if !strings.HasPrefix(a, logFieldPrefix) {
			rest = append(rest, a)
			continue
		}
		// `--log-field=x` (no trailing dash) is the *other* spelling — a
		// flag.Value named log-field — and does not carry this prefix, so it
		// falls through to flag.Parse above rather than being eaten here.
		rest0 := strings.TrimPrefix(a, logFieldPrefix)
		if rest0 == "" {
			return nil, nil, fmt.Errorf("invalid --log-field-: expected --log-field-<key>=<value>")
		}
		if key, _, hasEq := strings.Cut(rest0, "="); !hasEq {
			// `--log-field-cluster pg1`: the value is the next arg, unless that
			// arg is another flag (which would make the field value ambiguous).
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				if err := (&fields).Set(key + "=" + args[i+1]); err != nil {
					return nil, nil, err
				}
				i++
				continue
			}
			return nil, nil, fmt.Errorf("invalid --log-field-%s: missing value", key)
		}
		if err := (&fields).Set(rest0); err != nil {
			return nil, nil, err
		}
		continue
	}
	return fields, rest, nil
}
