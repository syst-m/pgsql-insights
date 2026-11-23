package collector

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureHandler is a slog.Handler that keeps every record, so a test can assert
// on the log line R13.11 requires rather than only on the value it returns.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

// lines returns the captured records as "LEVEL msg key=value" strings, which is
// the shape a test asserts on without re-implementing slog's JSON encoder.
func (h *captureHandler) lines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.records))
	for _, r := range h.records {
		var b strings.Builder
		b.WriteString(r.Level.String())
		b.WriteString(" ")
		b.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			b.WriteString(" ")
			b.WriteString(a.Key)
			b.WriteString("=")
			b.WriteString(a.Value.String())
			return true
		})
		out = append(out, b.String())
	}
	return out
}

func TestSampleSkew(t *testing.T) {
	src := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name string
		s    Sample
		want time.Duration
	}{
		{
			"our clock ahead",
			Sample{SourceTime: src, IngestTime: src.Add(45 * time.Second)},
			45 * time.Second,
		},
		{
			"our clock behind",
			Sample{SourceTime: src, IngestTime: src.Add(-45 * time.Second)},
			-45 * time.Second,
		},
		{
			"identical",
			Sample{SourceTime: src, IngestTime: src},
			0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.Skew(); got != tt.want {
				t.Fatalf("Skew() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestHasSourceClock(t *testing.T) {
	if (Sample{}).HasSourceClock() {
		t.Fatal("a zero SourceTime must not be comparable (it would report a ~56-year skew)")
	}
	if !(Sample{SourceTime: time.Unix(1, 0)}).HasSourceClock() {
		t.Fatal("a non-zero SourceTime must be comparable")
	}
	// StatsReset alone is not a source clock: it is the counter epoch (R3.3), and
	// treating it as the capture time is the misreading clock.go's doc records.
	if (Sample{StatsReset: time.Unix(1, 0)}).HasSourceClock() {
		t.Fatal("StatsReset must not satisfy HasSourceClock")
	}
}

func TestCheckClock(t *testing.T) {
	src := time.Unix(1_700_000_000, 0).UTC()
	tgt := Target{Namespace: "db", Cluster: "pg", Pod: "pg-1", Role: "primary", Source: SourceSQL}

	tests := []struct {
		name     string
		samples  []Sample
		wantSkew time.Duration
		wantLog  bool
		contains []string
	}{
		{
			name:     "no samples",
			samples:  nil,
			wantSkew: 0,
			wantLog:  false,
		},
		{
			name:     "no comparable source clock",
			samples:  []Sample{{Metric: "x", Value: 1, IngestTime: src}},
			wantSkew: 0,
			wantLog:  false,
		},
		{
			name:     "under the threshold",
			samples:  []Sample{{Metric: "x", SourceTime: src, IngestTime: src.Add(29 * time.Second)}},
			wantSkew: 29 * time.Second,
			wantLog:  false,
		},
		{
			// The boundary is inclusive: R13.11 says ">", so exactly 30 s does not
			// warn. A test at the boundary is what stops an >= from shipping.
			name:     "exactly at the threshold",
			samples:  []Sample{{Metric: "x", SourceTime: src, IngestTime: src.Add(MaxClockSkew)}},
			wantSkew: MaxClockSkew,
			wantLog:  false,
		},
		{
			name:     "over the threshold, ahead",
			samples:  []Sample{{Metric: "x", SourceTime: src, IngestTime: src.Add(31 * time.Second)}},
			wantSkew: 31 * time.Second,
			wantLog:  true,
			contains: []string{"WARN", "pg", "db", "pg-1", "31s", "30s", "our clock is ahead of the instance's"},
		},
		{
			name:     "over the threshold, behind",
			samples:  []Sample{{Metric: "x", SourceTime: src, IngestTime: src.Add(-90 * time.Second)}},
			wantSkew: 90 * time.Second,
			wantLog:  true,
			// "1m30s", not "90s": time.Duration's String renders 90 seconds as a
			// minute and a half, and the log line carries that rendering. The 31s and
			// 45s cases below read the same way — only sub-minute values look like
			// plain seconds, which is why the first drafts of these two rows failed.
			contains: []string{"WARN", "pg", "1m30s", "our clock is behind the instance's"},
		},
		{
			name: "worst skew wins, not the first",
			samples: []Sample{
				{Metric: "a", SourceTime: src, IngestTime: src.Add(31 * time.Second)},
				{Metric: "b", SourceTime: src, IngestTime: src.Add(-120 * time.Second)},
				{Metric: "c", SourceTime: src, IngestTime: src.Add(40 * time.Second)},
			},
			wantSkew: 120 * time.Second,
			wantLog:  true,
			contains: []string{"2m0s", "our clock is behind the instance's"},
		},
		{
			// Mixed comparable and non-comparable: the check reports the comparable
			// ones and does not invent a skew for the others.
			name: "skips samples with no source clock",
			samples: []Sample{
				{Metric: "a", SourceTime: src, IngestTime: src.Add(45 * time.Second)},
				{Metric: "b", IngestTime: src},
			},
			wantSkew: 45 * time.Second,
			wantLog:  true,
			contains: []string{"45s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &captureHandler{}
			got := CheckClock(slog.New(h), tgt, tt.samples)
			if got != tt.wantSkew {
				t.Fatalf("CheckClock() = %s, want %s", got, tt.wantSkew)
			}
			lines := h.lines()
			if !tt.wantLog {
				if len(lines) != 0 {
					t.Fatalf("CheckClock() logged %d line(s) %v, want none", len(lines), lines)
				}
				return
			}
			// One line per round, not one per sample: the case with three samples
			// must produce exactly one warning, which is the readability rule
			// clock.go documents (40 warnings per round per cluster is a warning
			// nobody reads).
			if len(lines) != 1 {
				t.Fatalf("CheckClock() logged %d lines, want exactly 1: %v", len(lines), lines)
			}
			for _, want := range tt.contains {
				if !strings.Contains(lines[0], want) {
					t.Errorf("log line %q does not contain %q", lines[0], want)
				}
			}
		})
	}
}

func TestCheckClockNilLogger(t *testing.T) {
	// A nil logger must not panic: api.Config's rule is that an unwired logger
	// discards, and CheckClock is called from a scheduler goroutine where a panic
	// would take the process down.
	src := time.Unix(1_700_000_000, 0).UTC()
	got := CheckClock(nil, Target{Namespace: "db", Cluster: "pg"}, []Sample{
		{Metric: "x", SourceTime: src, IngestTime: src.Add(time.Minute)},
	})
	if got != time.Minute {
		t.Fatalf("CheckClock(nil, ...) = %s, want 1m", got)
	}
}

func TestSampleString(t *testing.T) {
	src := time.Unix(1_700_000_000, 0).UTC()
	withSrc := Sample{Metric: "db_xact_commit_total", Value: 42, SourceTime: src, IngestTime: src.Add(2 * time.Second)}
	got := withSrc.String()
	for _, want := range []string{"db_xact_commit_total=42", "skew=2s", "source=", "ingested="} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
	noSrc := Sample{Metric: "pg_locks_count", Value: 3, IngestTime: src}
	if got := noSrc.String(); !strings.Contains(got, "source_time=unknown") {
		t.Errorf("String() = %q, want source_time=unknown for a sample with no source clock", got)
	}
}

func TestMaxClockSkewIs30s(t *testing.T) {
	if MaxClockSkew != 30*time.Second {
		t.Errorf("MaxClockSkew = %s, want 30s (R13.11 names 30 s)", MaxClockSkew)
	}
}
