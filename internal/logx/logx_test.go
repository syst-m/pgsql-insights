package logx

import (
	"bytes"
	"encoding/json"
	"flag"
	"log/slog"
	"strings"
	"testing"
)

// capture swaps DefaultWriter for a buffer for the duration of one test. The
// package writes to a variable rather than os.Stdout inline precisely so this
// is possible: R13.7's contract is "JSON on stdout", and the only way to test
// the shape of that JSON is to redirect it.
func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := DefaultWriter
	DefaultWriter = buf
	t.Cleanup(func() { DefaultWriter = old })
	return buf
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in    string
		want  slog.Level
		isErr bool
	}{
		{in: "", want: slog.LevelInfo},         // unset means the default
		{in: "info", want: slog.LevelInfo},     //
		{in: "INFO", want: slog.LevelInfo},     // case-insensitive
		{in: " debug ", want: slog.LevelDebug}, // trimmed
		{in: "warn", want: slog.LevelWarn},     //
		{in: "warning", want: slog.LevelWarn},  // slog's own spelling
		{in: "error", want: slog.LevelError},   //
		{in: "verbse", isErr: true},            // a typo must not silently become info
		{in: "DEBUG", want: slog.LevelDebug},   //
		{in: "0", isErr: true},                 // numeric levels are slog's, not the flag's
		{in: "info,debug", isErr: true},        // one level, not a list
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseLevel(c.in)
			if c.isErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) = %v, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q): unexpected error: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("ParseLevel(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestNewFromFlagsRejectsBadLevel(t *testing.T) {
	// The error path matters more than the happy path here: a deployed
	// --log-level=verbse has to fail startup loudly (R13.7), not quietly log at
	// a different level than the operator asked for.
	if l, err := NewFromFlags("verbse", nil); err == nil || l != nil {
		t.Fatalf("NewFromFlags(\"verbse\") = (%v, %v), want (nil, error)", l, err)
	}
}

func TestNewEmitsJSONWithConfiguredFields(t *testing.T) {
	buf := capture(t)
	New(slog.LevelInfo, Field{Key: "cluster", Value: "pg1"}).
		Info("serving", "listen", ":8080")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	for key, want := range map[string]any{
		"msg":     "serving",
		"level":   "INFO",
		"cluster": "pg1",   // the R13.7 --log-field-* value
		"listen":  ":8080", // a call-site attribute
	} {
		if got := rec[key]; got != want {
			t.Errorf("record[%q] = %v, want %v (%s)", key, got, want, buf.String())
		}
	}
	if _, ok := rec["time"]; !ok {
		t.Errorf("record has no timestamp: %s", buf.String())
	}
}

func TestNewLevelGatesRecords(t *testing.T) {
	// R13.7's second clause makes the level a product surface: the SQL a
	// collector executes is logged at Debug, so the default (info) process must
	// emit nothing for those calls, and --log-level=debug must emit exactly them.
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		t.Run(level.String(), func(t *testing.T) {
			buf := capture(t)
			New(level).Debug("query", "sql", "SELECT 1")
			if buf.Len() != 0 {
				t.Fatalf("debug record emitted at %v: %s", level, buf.String())
			}
		})
	}
	t.Run(slog.LevelDebug.String(), func(t *testing.T) {
		buf := capture(t)
		New(slog.LevelDebug).Debug("query", "sql", "SELECT 1")
		if !strings.Contains(buf.String(), `"sql":"SELECT 1"`) {
			t.Fatalf("debug record missing at debug level: %s", buf.String())
		}
	})
	// The level gates records, it does not gate the fields: an info-level line
	// from an info-level logger still carries every configured field.
	buf := capture(t)
	New(slog.LevelInfo).Info("serving")
	if buf.Len() == 0 {
		t.Fatal("info record suppressed at info level")
	}
}

func TestNewWithoutFields(t *testing.T) {
	buf := capture(t)
	l := New(slog.LevelInfo)
	if l == nil {
		t.Fatal("New returned nil")
	}
	l.Info("hello")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Fatalf("missing msg: %s", buf.String())
	}
}

func TestFieldsSet(t *testing.T) {
	cases := []struct {
		in      string
		want    Field
		wantErr bool
	}{
		{in: "cluster=pg1", want: Field{Key: "cluster", Value: "pg1"}},
		{in: "shard=", want: Field{Key: "shard", Value: ""}},             // empty value is a fact
		{in: "dsn=user=app", want: Field{Key: "dsn", Value: "user=app"}}, // split on the first =
		{in: " cluster = pg1 ", want: Field{Key: "cluster", Value: " pg1 "}},
		{in: "novalue", wantErr: true},
		{in: "=pg1", wantErr: true},
		{in: "a b=c", wantErr: true},
		{in: "a\tb=c", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			var f Fields
			err := f.Set(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Set(%q) = nil, want error", c.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("Set(%q): %v", c.in, err)
			}
			if len(f) != 1 || f[0] != c.want {
				t.Fatalf("Set(%q) -> %+v, want %+v", c.in, f, c.want)
			}
		})
	}
}

func TestFieldsIsAFlagValue(t *testing.T) {
	// Proves the repeatable `--log-field k=v --log-field env=lab` form works
	// through the stdlib flag package rather than a hand-rolled parser.
	var f Fields
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Var(&f, "log-field", "")
	if err := fs.Parse([]string{"--log-field", "cluster=pg1", "--log-field=env=lab"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f) != 2 || f[0].Key != "cluster" || f[1].Value != "lab" {
		t.Fatalf("parsed %+v", f)
	}
	if got, want := f.String(), "cluster=pg1,env=lab"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, ok := f.Get().(Fields); !ok || len(got) != 2 {
		t.Fatalf("Get() = %#v", f.Get())
	}
	var empty Fields
	if empty.String() != "" {
		t.Fatalf("empty String() = %q, want \"\"", empty.String())
	}
}

func TestFieldsFromArgs(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		want     Fields
		wantRest []string
		wantErr  bool
	}{
		{
			name:     "prefixed equals form",
			in:       []string{"--log-field-cluster=pg1", "--listen=:9090"},
			want:     Fields{{Key: "cluster", Value: "pg1"}},
			wantRest: []string{"--listen=:9090"},
		},
		{
			name:     "prefixed space form",
			in:       []string{"--log-field-cluster", "pg1", "--auth=none"},
			want:     Fields{{Key: "cluster", Value: "pg1"}},
			wantRest: []string{"--auth=none"},
		},
		{
			name: "several fields, mixed spellings",
			// The plain `--log-field env=lab` pair is *not* consumed here — it is
			// a declared flag, so it stays in the list for flag.Parse. Only the
			// prefixed spellings are lifted out.
			in:       []string{"--log-field-cluster=pg1", "--log-field", "env=lab", "--log-field-shard=", "--auth=none"},
			want:     Fields{{Key: "cluster", Value: "pg1"}, {Key: "shard", Value: ""}},
			wantRest: []string{"--log-field", "env=lab", "--auth=none"},
		},
		{
			name:     "no fields",
			in:       []string{"--auth=none"},
			wantRest: []string{"--auth=none"},
		},
		{
			name:     "nothing",
			in:       nil,
			wantRest: []string{},
		},
		{
			name:     "after the double-dash separator, verbatim",
			in:       []string{"--auth=none", "--", "--log-field-cluster=pg1"},
			wantRest: []string{"--auth=none", "--", "--log-field-cluster=pg1"},
		},
		{
			name:     "prefixed with no value",
			in:       []string{"--log-field-cluster"},
			wantRest: []string{"--log-field-cluster"},
			wantErr:  true,
		},
		{
			name:    "prefixed followed by another flag",
			in:      []string{"--log-field-cluster", "--auth=none"},
			wantErr: true,
		},
		{
			name:    "bare prefix",
			in:      []string{"--log-field-"},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rest, err := FieldsFromArgs(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("FieldsFromArgs(%v) = (%v, %v, nil), want error", c.in, got, rest)
				}
				return
			}
			if err != nil {
				t.Fatalf("FieldsFromArgs(%v): %v", c.in, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("fields = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("fields[%d] = %v, want %v", i, got[i], c.want[i])
				}
			}
			if strings.Join(rest, " ") != strings.Join(c.wantRest, " ") {
				t.Errorf("rest = %v, want %v", rest, c.wantRest)
			}
		})
	}
}

// TestFieldsFromArgsFeedsTheFlagSet is the wiring cmd/pgsi uses, end to end:
// the pre-scan output is seeded into the flag value, so a chart arg of
// --log-field-cluster=pg1 and a hand-written --log-field cluster=pg1 land in
// the same set and therefore on the same log lines.
func TestFieldsFromArgsFeedsTheFlagSet(t *testing.T) {
	buf := capture(t)
	args := []string{"--log-field-cluster=pg1", "--log-level=debug", "--listen=:8080"}
	prefixed, rest, err := FieldsFromArgs(args)
	if err != nil {
		t.Fatalf("FieldsFromArgs: %v", err)
	}
	fields := prefixed
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Var(&fields, "log-field", "")
	level := fs.String("log-level", "info", "")
	listen := fs.String("listen", ":8080", "")
	if err := fs.Parse(rest); err != nil {
		t.Fatalf("flag.Parse(%v): %v", rest, err)
	}
	if *level != "debug" || *listen != ":8080" {
		t.Fatalf("flags lost: level=%q listen=%q", *level, *listen)
	}
	l, err := NewFromFlags(*level, fields)
	if err != nil {
		t.Fatalf("NewFromFlags: %v", err)
	}
	l.Debug("query", "sql", "SELECT 1")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if rec["cluster"] != "pg1" || rec["sql"] != "SELECT 1" || rec["level"] != "DEBUG" {
		t.Fatalf("record = %v", rec)
	}
}
