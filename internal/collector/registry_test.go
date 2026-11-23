package collector

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewRegistry(t *testing.T) {
	cols := []Collector{
		newFake("wal", nil),
		newFake("database", nil),
		newFake("locks", nil),
	}
	r, err := NewRegistry(cols, Overrides{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	all := r.All()
	if len(all) != 3 {
		t.Fatalf("All() returned %d entries, want 3", len(all))
	}
	// Sorted by name, so a round's dispatch order is deterministic.
	want := []string{"database", "locks", "wal"}
	for i, w := range want {
		if all[i].Desc.Name != w {
			t.Errorf("All()[%d].Desc.Name = %q, want %q (All must be sorted)", i, all[i].Desc.Name, w)
		}
		if !all[i].Enabled {
			t.Errorf("All()[%d].Enabled = false, want true", i)
		}
		if all[i].Interval != DefaultInterval {
			t.Errorf("All()[%d].Interval = %s, want %s", i, all[i].Interval, DefaultInterval)
		}
		// Entry.Desc must be the collector's own descriptor, cached verbatim.
		if all[i].Desc.Name != all[i].Col.Descriptor().Name {
			t.Errorf("Entry.Desc.Name %q != Col.Descriptor().Name %q", all[i].Desc.Name, all[i].Col.Descriptor().Name)
		}
	}
	for _, w := range want {
		if _, ok := r.Get(w); !ok {
			t.Errorf("Get(%q) not found", w)
		}
	}
	if _, ok := r.Get("activity"); ok {
		t.Error(`Get("activity") found, want not found`)
	}
}

func TestNewRegistryErrors(t *testing.T) {
	tests := []struct {
		name    string
		cols    []Collector
		ov      Overrides
		wantErr string
	}{
		{
			name:    "nil collector",
			cols:    []Collector{newFake("wal", nil), nil},
			wantErr: "nil collector at index 1",
		},
		{
			name:    "invalid descriptor",
			cols:    []Collector{fakeCollector{}},
			wantErr: "Descriptor.Name is required",
		},
		{
			name:    "duplicate name",
			cols:    []Collector{newFake("wal", nil), newFake("wal", nil)},
			wantErr: "registered twice",
		},
		{
			name:    "interval for an unregistered collector",
			cols:    []Collector{newFake("wal", nil)},
			ov:      Overrides{Intervals: map[string]time.Duration{"databse": 45 * time.Second}},
			wantErr: "unregistered collector",
		},
		{
			name:    "non-positive interval override",
			cols:    []Collector{newFake("wal", nil)},
			ov:      Overrides{Intervals: map[string]time.Duration{"wal": 0}},
			wantErr: "must be > 0",
		},
		{
			name:    "negative interval override",
			cols:    []Collector{newFake("wal", nil)},
			ov:      Overrides{Intervals: map[string]time.Duration{"wal": -time.Second}},
			wantErr: "must be > 0",
		},
		{
			// The R2.3 bug this check exists for: a typo in --disable-collector
			// silently leaves the collector enabled.
			name:    "disable names an unregistered collector",
			cols:    []Collector{newFake("wal", nil)},
			ov:      Overrides{Disabled: []string{"databse"}},
			wantErr: "unregistered collector",
		},
		{
			name:    "empty string in disabled",
			cols:    []Collector{newFake("wal", nil)},
			ov:      Overrides{Disabled: []string{""}},
			wantErr: "unregistered collector",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewRegistry(tt.cols, tt.ov)
			if err == nil {
				t.Fatalf("NewRegistry() = nil error, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NewRegistry() = %q, want it to contain %q", err, tt.wantErr)
			}
			if r != nil {
				t.Errorf("NewRegistry() returned a non-nil registry alongside an error")
			}
		})
	}
}

func TestNewRegistryAppliesOverrides(t *testing.T) {
	r, err := NewRegistry(
		[]Collector{newFake("wal", nil), newFake("database", nil), newFake("locks", nil)},
		Overrides{
			Intervals: map[string]time.Duration{"wal": 5 * time.Minute},
			Disabled:  []string{"database"},
		},
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if e, _ := r.Get("wal"); e.Interval != 5*time.Minute {
		t.Errorf("wal interval = %s, want 5m (R2.3 override)", e.Interval)
	}
	if e, _ := r.Get("wal"); !e.Enabled {
		t.Error("wal Enabled = false, want true")
	}
	// The override must not leak to a sibling collector.
	if e, _ := r.Get("locks"); e.Interval != DefaultInterval {
		t.Errorf("locks interval = %s, want %s (an override must not leak)", e.Interval, DefaultInterval)
	}
	db, _ := r.Get("database")
	if db.Enabled {
		t.Error("database Enabled = true, want false (--disable-collector=database)")
	}
	// A disabled collector stays visible in All() and absent from Enabled(), so
	// the API can report it as off rather than as missing.
	enabled := r.Enabled()
	if len(enabled) != 2 {
		t.Fatalf("Enabled() returned %d, want 2", len(enabled))
	}
	for _, e := range enabled {
		if e.Desc.Name == "database" {
			t.Error("Enabled() includes the disabled collector")
		}
	}
	if len(r.All()) != 3 {
		t.Errorf("All() returned %d, want 3 (a disabled collector is not deleted)", len(r.All()))
	}
}

func TestRegistryEnabledAllDoNotAlias(t *testing.T) {
	// Mutating the returned slice must not corrupt the registry: All() copies, and
	// a caller that sorts or truncates it in place would otherwise rewrite the
	// registry's own backing array through the shared header.
	r, err := NewRegistry([]Collector{newFake("wal", nil), newFake("locks", nil)}, Overrides{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	all := r.All()
	all[0].Col = nil
	if got, _ := r.Get("locks"); got.Col == nil {
		t.Fatal("mutating All()[0] changed the registry's stored entry")
	}
}

func TestIntervalOverridesFromArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    map[string]time.Duration
		wantErr string
	}{
		{
			name: "empty",
			args: nil,
			want: map[string]time.Duration{},
		},
		{
			name: "equals form",
			args: []string{"--interval.database=45s"},
			want: map[string]time.Duration{"database": 45 * time.Second},
		},
		{
			name: "space form",
			args: []string{"--interval.database", "45s"},
			want: map[string]time.Duration{"database": 45 * time.Second},
		},
		{
			name: "several, mixed forms",
			args: []string{"serve", "--interval.database=45s", "--interval.wal", "10m"},
			want: map[string]time.Duration{"database": 45 * time.Second, "wal": 10 * time.Minute},
		},
		{
			name:    "zero duration",
			args:    []string{"--interval.wal=0"},
			wantErr: "must be > 0",
		},
		{
			name:    "negative duration",
			args:    []string{"--interval.wal=-5s"},
			wantErr: "must be > 0",
		},
		{
			name:    "unparseable duration",
			args:    []string{"--interval.wal=forty"},
			wantErr: "invalid interval",
		},
		{
			name:    "missing value",
			args:    []string{"--interval.wal"},
			wantErr: "missing duration value",
		},
		{
			// The space form must not swallow the next flag: consuming
			// "--disable-collector=x" as a duration is the misparse.
			name:    "missing value before another flag",
			args:    []string{"--interval.wal", "--disable-collector=x"},
			wantErr: "missing duration value",
		},
		{
			name:    "empty name",
			args:    []string{"--interval.=45s"},
			wantErr: "expected --interval.<collector>=<duration>",
		},
		{
			name:    "name with a space",
			args:    []string{"--interval.data base=45s"},
			wantErr: "expected --interval.<collector>=<duration>",
		},
		{
			name:    "duplicate",
			args:    []string{"--interval.wal=45s", "--interval.wal=1m"},
			wantErr: "interval given twice",
		},
		{
			// An empty value is not a duration; it is a malformed flag.
			name:    "empty value",
			args:    []string{"--interval.wal="},
			wantErr: "invalid interval",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// rest is asserted in TestIntervalOverridesFromArgsRest; this table
			// covers the parsed map and the error cases.
			got, _, err := IntervalOverridesFromArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("IntervalOverridesFromArgs: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for name, d := range tt.want {
				if got[name] != d {
					t.Errorf("got[%q] = %s, want %s", name, got[name], d)
				}
			}
		})
	}
}

func TestIntervalOverridesFromArgsRest(t *testing.T) {
	got, rest, err := IntervalOverridesFromArgs([]string{
		"serve", "--auth=token", "--interval.database=45s", "--log-level", "debug", "--", "--interval.wal=1m",
	})
	if err != nil {
		t.Fatalf("IntervalOverridesFromArgs: %v", err)
	}
	if len(got) != 1 || got["database"] != 45*time.Second {
		t.Fatalf("overrides = %v, want only database=45s (everything after `--` is positional)", got)
	}
	want := []string{"serve", "--auth=token", "--log-level", "debug", "--", "--interval.wal=1m"}
	if strings.Join(rest, " ") != strings.Join(want, " ") {
		t.Errorf("rest = %v, want %v", rest, want)
	}
}

func TestDisabledFromFlag(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"wal", []string{"wal"}},
		{"wal,database", []string{"wal", "database"}},
		{"wal, database", []string{"wal", "database"}},
		// A trailing comma in a shell-quoted list is a common typo and must not
		// become a collector named "".
		{"wal,", []string{"wal"}},
		{"wal,,database", []string{"wal", "database"}},
		{",,", nil},
		{" wal , database ", []string{"wal", "database"}},
	}
	for _, tt := range tests {
		got := DisabledFromFlag(tt.in)
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("DisabledFromFlag(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// TestFlagShapeClosesTheLoop drives the two R2.3 flags end to end the way
// cmd/pgsi will: pre-scan the args for the dynamic name, take the static one as
// a plain string, and build a registry from both. It is the test that proves the
// two halves compose, which neither half's own test can.
func TestFlagShapeClosesTheLoop(t *testing.T) {
	args := []string{"serve", "--interval.database=45s", "--disable-collector=locks"}
	intervals, rest, err := IntervalOverridesFromArgs(args)
	if err != nil {
		t.Fatalf("IntervalOverridesFromArgs: %v", err)
	}
	// The static flag survives the pre-scan and reaches the flag set.
	wantRest := []string{"serve", "--disable-collector=locks"}
	if strings.Join(rest, " ") != strings.Join(wantRest, " ") {
		t.Fatalf("rest = %v, want %v", rest, wantRest)
	}
	r, err := NewRegistry(
		[]Collector{newFake("database", nil), newFake("locks", nil), newFake("wal", nil)},
		Overrides{Intervals: intervals, Disabled: DisabledFromFlag("locks")},
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if e, _ := r.Get("database"); e.Interval != 45*time.Second {
		t.Errorf("database interval = %s, want 45s", e.Interval)
	}
	if e, _ := r.Get("locks"); e.Enabled {
		t.Error("locks Enabled = true, want false")
	}
	if e, _ := r.Get("wal"); e.Interval != DefaultInterval || !e.Enabled {
		t.Errorf("wal = %+v, want default interval and enabled", e)
	}
}

// TestCollectContractIsHonouredByTheFake pins that the test double obeys the
// Collector contract's ctx clause, so a scheduler test that trusts the fake is
// not trusting an object that ignores its context.
func TestCollectContractIsHonouredByTheFake(t *testing.T) {
	var sawCtx context.Context
	f := newFake("wal", func(ctx context.Context, _ Target) (Result, error) {
		sawCtx = ctx
		return Result{}, nil
	})
	if f.Descriptor().Name != "wal" {
		t.Fatalf("Descriptor().Name = %q, want wal", f.Descriptor().Name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := f.Collect(ctx, Target{}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if sawCtx != ctx {
		t.Error("fake Collector did not receive the ctx it was given")
	}
}
