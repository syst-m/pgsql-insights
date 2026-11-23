package collector

// IdentityLabels is R9.3's always-present half of the label contract: the four
// labels every exported series carries, derived from the Target it was collected
// from.
//
// R9.3's contract is closed and it has exactly two halves. This is the half that
// identifies *which instance* the value came from — namespace, cluster, pod,
// instance_role — and it is the scheduler's, because the scheduler is the only
// component that holds a Target. The other half is series-specific (datname for
// database series, usename for user series, pooler for pooler series) and belongs
// to the collector that knows which row the value came from. clock.go's Sample
// doc states the split ("The scheduler fills the identity labels from the Target
// so a collector returns only the series-specific ones"); this file is the
// implementation of the first clause, which until now existed only as that
// sentence.
//
// The contract being closed is what makes this a function rather than a struct
// field with a comment: thirteen collectors each assembling the same four labels
// is thirteen chances to spell one of them differently, and R16.8's failure class
// is exactly that — a fixture whose producer and consumer disagree about a name.
// One function, called from one place, has no such surface.
//
// All four are emitted even when empty. A replica whose source reports no role
// exports instance_role="" rather than dropping the label, because a query like
// `pgsi_up{instance_role="primary"} == 0` (chart's PGSIPrimaryDown) matches on
// label *presence* as much as on its value, and a series that omits a label the
// contract promises is a series that silently disappears from every query that
// filters on it.
func IdentityLabels(t Target) map[string]string {
	return map[string]string{
		"namespace":     t.Namespace,
		"cluster":       t.Cluster,
		"pod":           t.Pod,
		"instance_role": t.Role,
	}
}

// ApplyIdentityLabels returns a copy of samples with t's identity labels merged
// into each one, and is the scheduler's single point of contact with R9.3.
//
// The merge rule is that the Target wins on the four identity keys and the
// collector wins on everything else. A collector that sets `cluster` itself —
// which is a mistake, but a legal one in Go — is overruled, because the value it
// would use is a string it read out of a row while the scheduler holds the
// authoritative Target. A collector's datname is kept, because the scheduler has
// no way to know which database a row belonged to.
//
// It returns a copy rather than mutating in place for the reason the collectors'
// own tests depend on: a collector's test asserts on the samples it built
// (settings_test.go's "want none" check reads len(s.Labels) on the collector's
// own output), and a scheduler that rewrote those maps in place would make a
// collector's result depend on whether something had run it. Copying also keeps
// the input's maps untouched, so the same []Sample can be published to two sinks.
func ApplyIdentityLabels(t Target, samples []Sample) []Sample {
	if len(samples) == 0 {
		return samples
	}
	identity := IdentityLabels(t)
	out := make([]Sample, len(samples))
	for i, s := range samples {
		merged := make(map[string]string, len(identity)+len(s.Labels))
		for k, v := range s.Labels {
			merged[k] = v
		}
		// Applied second so the identity half overwrites a collector's own
		// namespace/cluster/pod/instance_role if it set one.
		for k, v := range identity {
			merged[k] = v
		}
		s.Labels = merged
		out[i] = s
	}
	return out
}
