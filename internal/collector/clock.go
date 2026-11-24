package collector

import (
	"fmt"
	"log/slog"
	"time"
)

// MaxClockSkew is R13.11's threshold: two timestamps for the same sample that
// disagree by more than 30 s must produce a warning naming the cluster.
const MaxClockSkew = 30 * time.Second

// Sample is one value a collector produced, and it is where R13.11's clock
// contract lives.
//
// R13.11 requires every stored sample to carry two timestamps — the source's and
// our own ingest time — and to warn when they disagree by more than 30 s. The
// reason it gives is R3.3's: a counter delta divided by a duration measured on
// two different clocks yields a rate that is wrong in a way nothing else in the
// product catches, because both numbers look individually plausible.
//
// Which value counts as "the source's" is where the requirement's wording and
// the data disagree, and the resolution is worth stating because it is the kind
// of thing that gets silently implemented twice. R13.11 names
// "the source's `stats_reset`/`capturedAt` from the instance". `capturedAt` is
// Cloud SQL's field name for the moment the instance took the measurement, and
// for a SQL collector the equivalent is the instance's own clock read at
// capture time — `pg_catalog.now()`, which R2.7's read-only transaction gives us
// in the same round-trip budget for free. `stats_reset` is a different fact: it
// is the counter *epoch*, not the capture time, and treating it as the
// clock-comparable value would warn on every cluster whose statistics were reset
// more than 30 s ago, which is nearly all of them, and a warning that always
// fires is a warning nobody reads. So SourceTime carries the instance's clock
// (the value the skew check compares) and StatsReset carries the epoch (the value
// R3.3's counter-reset detection needs), and both are on the sample so neither
// requirement has to borrow the other's field. Flagged for A3 as a spec-wording
// question, not resolved as one.
type Sample struct {
	// Metric is the family name this value belongs to, without the R9.2 prefix
	// (the prefix is applied at render time so it stays configurable):
	// "db_xact_commit_total", not "pgsi_db_xact_commit_total".
	Metric string
	// Labels carries the R9.3 label contract for this value — namespace,
	// cluster, pod, instance_role, plus datname for database series and usename
	// for user series. The scheduler fills the identity labels from the Target
	// so a collector returns only the series-specific ones, which is what stops
	// thirteen collectors from each re-implementing R9.3's contract and getting
	// it slightly differently (the R16.8 failure class, on the producer's side).
	Labels map[string]string
	// Value is the sample's value. float64 because every Prometheus sample is
	// one, and because a counter read as int64 and a gauge read as float64 have
	// to share a field to land in the same registry.
	Value float64

	// SourceTime is the instance's own clock at capture (R13.11's first
	// timestamp). Zero means the collector has no source clock to report — see
	// HasSourceClock for why that is not compared.
	SourceTime time.Time
	// IngestTime is our clock at the moment the value was read back (R13.11's
	// second timestamp).
	IngestTime time.Time
	// StatsReset is the view's stats_reset — the counter epoch R3.3 needs to tell
	// a counter reset from a counter decrease. Zero for a view that has no
	// stats_reset (pg_locks, pg_settings, pg_stat_activity), which is not a
	// missing value: those views have no epoch.
	StatsReset time.Time
}

// Skew returns IngestTime minus SourceTime: positive when our clock is ahead of
// the instance's, negative when behind. It is signed on purpose — R13.11's
// warning has to name a direction, because "the instance's clock is four minutes
// behind ours" and "ours is" are different fixes, and a caller that only kept the
// absolute value could not say which.
func (s Sample) Skew() time.Duration { return s.IngestTime.Sub(s.SourceTime) }

// HasSourceClock reports whether this sample carries a source timestamp worth
// comparing.
//
// A zero SourceTime is "unknown", not "the Unix epoch". Comparing it to our
// ingest time would report a ~56-year skew and fire R13.11's warning on every
// such sample, which is the always-firing-warning failure again in its most
// literal form. A collector that cannot read the instance's clock leaves the
// field zero and the check skips it; the sample is still stored, because
// R13.11's storage clause needs two timestamps and the collector that has one
// real timestamp and one unknown is not the collector the requirement is about.
func (s Sample) HasSourceClock() bool { return !s.SourceTime.IsZero() }

// CheckClock applies R13.11's warning clause to one collector round and returns
// the largest absolute skew it saw (0 when nothing was comparable).
//
// One log line per (collector, target) round, not one per sample. A round that
// reads 40 queries' worth of values would otherwise emit 40 identical warnings
// every 60 s per cluster, and on a 50-cluster fleet that is 120 000 lines an
// hour for one fact. The requirement says "the collector logs a skew warning
// naming the cluster" — singular — and the aggregate is what makes it readable:
// the line names the collector, the cluster and the worst skew, which is the
// information a operator needs to decide which clock to fix.
//
// R13.11's second clause — "the UI surfaces clock skew as a finding" — is not
// this function's job: the finding surface is Phase 5's, and it reads the value
// this returns (via Stats, which records it per collector/target) rather than
// parsing the log line it writes.
func CheckClock(log *slog.Logger, tgt Target, samples []Sample) time.Duration {
	var worst time.Duration
	found := false
	for _, s := range samples {
		if !s.HasSourceClock() {
			continue
		}
		d := s.Skew()
		abs := d
		if abs < 0 {
			abs = -abs
		}
		if !found || abs > worst {
			worst, found = abs, true
		}
	}
	if !found || worst <= MaxClockSkew {
		return worst
	}
	if log != nil {
		log.Warn("clock skew exceeds the R13.11 contract",
			"cluster", tgt.Cluster,
			"namespace", tgt.Namespace,
			"pod", tgt.Pod,
			"skew", worst.String(),
			"threshold", MaxClockSkew.String(),
			"direction", func() string {
				// The sign of the worst sample's skew, not of the absolute value
				// the loop kept: "behind" and "ahead" are the actionable half of
				// the message and the magnitude alone does not carry them.
				for _, s := range samples {
					if s.HasSourceClock() && absDuration(s.Skew()) == worst {
						if s.Skew() > 0 {
							return "our clock is ahead of the instance's"
						}
						return "our clock is behind the instance's"
					}
				}
				return "unknown"
			}(),
		)
	}
	return worst
}

// absDuration is math.Abs for time.Duration, spelled out because the math
// package's Abs takes a float and importing it for one integer negation costs
// more than the three lines it saves.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// String renders the two-timestamp pair the way the R13.11 log line and the
// Phase 5 finding both want it, so the wording is written once.
func (s Sample) String() string {
	if !s.HasSourceClock() {
		return fmt.Sprintf("%s=%g ingested=%s source_time=unknown", s.Metric, s.Value, s.IngestTime.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("%s=%g source=%s ingested=%s skew=%s",
		s.Metric, s.Value,
		s.SourceTime.UTC().Format(time.RFC3339),
		s.IngestTime.UTC().Format(time.RFC3339),
		s.Skew().String())
}
