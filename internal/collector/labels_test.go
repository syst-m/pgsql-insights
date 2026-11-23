package collector

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestIdentityLabelsAreExactlyTheContractedFour is R9.3 read as a set.
//
// R9.3's label contract is closed: namespace, cluster, pod, instance_role are on
// every series, and pooler/datname/usename are conditional extras owned by the
// collector that knows them. That means the *identity* half has exactly four keys
// and no fifth — and a fifth key here (a `collector`, say, or a `role` alias) is
// not a cosmetic addition but a change to what every exported series looks like,
// which is the class of change R16.8 exists to catch. So this asserts the key set
// by equality, not by containing.
func TestIdentityLabelsAreExactlyTheContractedFour(t *testing.T) {
	got := IdentityLabels(Target{
		Namespace: "db",
		Cluster:   "pg",
		Pod:       "pg-1",
		Role:      "primary",
		Address:   "10.0.0.1:5432",
		Source:    SourceSQL,
		PGMajor:   16,
	})

	want := map[string]string{
		"namespace":     "db",
		"cluster":       "pg",
		"pod":           "pg-1",
		"instance_role": "primary",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("IdentityLabels = %v, want %v (R9.3's four; Address/Source/PGMajor are not labels)", got, want)
	}
}

// TestIdentityLabelsEmitEmptyValues checks the shape a replica in a fresh
// install produces: a Target whose Role is empty.
//
// Dropping the empty label would be the natural Go instinct and it is wrong here.
// chart/templates/prometheusrule.yaml:96 is `pgsi_up{instance_role="primary"} == 0`
// and the equivalent replica rule at :159; both match on label *presence* as much
// as on value, so a series that omits instance_role disappears from the rule
// rather than being reported as a role-less instance.
func TestIdentityLabelsEmitEmptyValues(t *testing.T) {
	got := IdentityLabels(Target{Namespace: "db", Cluster: "pg"})
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4 (all four are emitted even when empty)\n%v", len(got), got)
	}
	for _, k := range []string{"namespace", "cluster", "pod", "instance_role"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing label %q (an empty value must still be emitted)", k)
		}
	}
	if got["pod"] != "" || got["instance_role"] != "" {
		t.Errorf("pod/instance_role = %q/%q, want empty strings preserved", got["pod"], got["instance_role"])
	}
}

// TestApplyIdentityLabelsKeepsCollectorLabels is the second half of the split
// R9.3 implies: the scheduler adds the four identity labels and touches nothing
// else, so a collector's datname survives.
func TestApplyIdentityLabelsKeepsCollectorLabels(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	in := []Sample{
		{Metric: "db_xact_commit_total", Labels: map[string]string{"datname": "postgres"}, Value: 7},
		{Metric: "db_xact_rollback_total", Labels: map[string]string{"datname": "app"}, Value: 1},
		{Metric: "db_size_bytes", Labels: map[string]string{}, Value: 4096},
	}

	out := ApplyIdentityLabels(tgt, in)
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}

	wantExtra := map[string]map[string]string{
		"db_xact_commit_total": {
			"namespace": "db", "cluster": "pg", "pod": "pg-1", "instance_role": "primary",
			"datname": "postgres",
		},
		"db_xact_rollback_total": {
			"namespace": "db", "cluster": "pg", "pod": "pg-1", "instance_role": "primary",
			"datname": "app",
		},
		"db_size_bytes": {
			"namespace": "db", "cluster": "pg", "pod": "pg-1", "instance_role": "primary",
		},
	}
	for _, s := range out {
		want, ok := wantExtra[s.Metric]
		if !ok {
			t.Errorf("unexpected sample %q", s.Metric)
			continue
		}
		if !reflect.DeepEqual(s.Labels, want) {
			t.Errorf("%s labels = %v, want %v", s.Metric, s.Labels, want)
		}
	}
}

// TestApplyIdentityLabelsTargetWinsOnIdentityKeys pins the precedence rule.
//
// A collector that sets `cluster` itself is legal Go and a mistake: the value it
// would use came from a row, while the scheduler holds the Target the discovery
// loop built. Without a documented precedence the outcome depends on map
// iteration order in the merge, which is the same "producer and consumer disagree
// about a name" failure R16.8 files.
func TestApplyIdentityLabelsTargetWinsOnIdentityKeys(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	in := []Sample{{
		Metric: "db_xact_commit_total",
		Labels: map[string]string{
			"namespace":     "WRONG",
			"cluster":       "WRONG",
			"pod":           "WRONG",
			"instance_role": "WRONG",
			"datname":       "postgres",
		},
	}}

	got := ApplyIdentityLabels(tgt, in)[0].Labels
	for k, v := range map[string]string{"namespace": "db", "cluster": "pg", "pod": "pg-1", "instance_role": "primary"} {
		if got[k] != v {
			t.Errorf("label %s = %q, want %q (the Target overrides a collector's own identity value)", k, got[k], v)
		}
	}
	if got["datname"] != "postgres" {
		t.Errorf("datname = %q, want postgres (the collector wins on everything outside the four)", got["datname"])
	}
}

// TestApplyIdentityLabelsDoesNotMutateInput is what lets the same []Sample go to
// two sinks, and what keeps a collector's own test asserting on its own output.
//
// settings_test.go:227 asserts `len(s.Labels) != 0` fails for a settings sample —
// a scheduler that merged in place would turn that collector test into one that
// passes or fails depending on whether something ran the collector first.
func TestApplyIdentityLabelsDoesNotMutateInput(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	orig := map[string]string{"datname": "postgres"}
	in := []Sample{{Metric: "db_xact_commit_total", Labels: orig, Value: 3}}

	out := ApplyIdentityLabels(tgt, in)

	if !reflect.DeepEqual(in[0].Labels, orig) {
		t.Errorf("input Labels mutated to %v, want %v", in[0].Labels, orig)
	}
	if len(in[0].Labels) != 1 {
		t.Errorf("input Labels len = %d, want 1", len(in[0].Labels))
	}
	// The returned Sample's Value must survive the copy; a copy that dropped it
	// would render `db_xact_commit_total{...} 0`.
	if out[0].Value != 3 || out[0].Metric != "db_xact_commit_total" {
		t.Errorf("copied sample = %+v, want Metric db_xact_commit_total Value 3", out[0])
	}
}

// TestApplyIdentityLabelsEmptyInput covers the two empty shapes: nil and a
// non-nil zero-length slice. A collector that found nothing returns nil, and the
// scheduler calls this on every round's result including those.
func TestApplyIdentityLabelsEmptyInput(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	if got := ApplyIdentityLabels(tgt, nil); got != nil {
		t.Errorf("ApplyIdentityLabels(nil) = %v, want nil", got)
	}
	empty := []Sample{}
	if got := ApplyIdentityLabels(tgt, empty); len(got) != 0 {
		t.Errorf("ApplyIdentityLabels(empty) len = %d, want 0", len(got))
	}
}

// TestSchedulerAppliesIdentityLabelsToSinkSamples is the wiring proof.
//
// The unit tests above prove the function is right; this proves it is *called*.
// That distinction is the whole reason the R9.3 half of step 4 was a gap: the
// contract existed in clock.go's doc comment ("The scheduler fills the identity
// labels from the Target") with nothing implementing the sentence, and a function
// that is correct and uncalled fails the same acceptance check.
func TestSchedulerAppliesIdentityLabelsToSinkSamples(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	sink := NewMemSink(0)
	s := NewScheduler(Config{}, mustRegistry(t, newFake("database", func(context.Context, Target) (Result, error) {
		return Result{Samples: samplesOf(5, 2), RoundTrips: 1}, nil
	})), nil, sink, nil)

	s.RunOnce(context.Background(), []Target{tgt})

	got := sink.Samples(tgt, "database")
	if len(got) != 2 {
		t.Fatalf("sink has %d samples for (pg-1, database), want 2", len(got))
	}
	for i, s := range got {
		for k, v := range IdentityLabels(tgt) {
			if s.Labels[k] != v {
				t.Errorf("sample %d (%s) label %s = %q, want %q", i, s.Metric, k, s.Labels[k], v)
			}
		}
		if s.Labels["datname"] != "postgres" {
			t.Errorf("sample %d datname = %q, want postgres", i, s.Labels["datname"])
		}
	}
}

// TestSchedulerLabelsSurviveAFailedRound is the R2.2 last-good case, and the
// reason ApplyIdentityLabels sits on the success path rather than above the error
// branch.
//
// A failed round publishes nothing — scheduler.go's own comment calls that
// non-publish "R2.2's 'a failed collector leaves its last-good data served and
// marks it with an age'". So the samples on the wire after a failure are the
// *previous* round's, and the question this answers is whether they kept their
// identity labels. They did, because the round that produced them ran through
// ApplyIdentityLabels before publishing; labelling the failed round's own
// res.Samples would allocate a map per sample for the one field of that Result
// anyone reads back (RoundTrips) and change nothing on the wire.
func TestSchedulerLabelsSurviveAFailedRound(t *testing.T) {
	tgt := tgtPG("pg", "pg-1", "primary")
	sink := NewMemSink(0)

	var fail bool
	s := NewScheduler(Config{}, mustRegistry(t, newFake("database", func(context.Context, Target) (Result, error) {
		if fail {
			return Result{}, errors.New("fake: collector failed")
		}
		return Result{Samples: samplesOf(5, 1), RoundTrips: 1}, nil
	})), nil, sink, nil)

	// Round one succeeds: the sink gets labelled samples.
	s.RunOnce(context.Background(), []Target{tgt})
	if got := sink.Samples(tgt, "database"); len(got) != 1 {
		t.Fatalf("after the successful round the sink has %d samples, want 1", len(got))
	}

	// Round two fails: nothing is published, and what remains must still be
	// labelled. An unlabelled survivor is a value that cannot be attributed to an
	// instance, which is R9.3's prohibition.
	fail = true
	s.RunOnce(context.Background(), []Target{tgt})

	got := sink.Samples(tgt, "database")
	if len(got) != 1 {
		t.Fatalf("after the failed round the sink has %d samples, want 1 (R2.2: the last-good round is still served)", len(got))
	}
	if got[0].Value != 5 {
		t.Errorf("surviving sample value = %v, want 5 (round one's value, not round two's)", got[0].Value)
	}
	for k, v := range IdentityLabels(tgt) {
		if got[0].Labels[k] != v {
			t.Errorf("surviving sample label %s = %q, want %q", k, got[0].Labels[k], v)
		}
	}
}
