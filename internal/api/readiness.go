package api

import "sync/atomic"

// Readiness is R8.7's single readiness signal: one value, one read path, and
// every surface that needs the answer goes through it.
//
// Before this type existed, Server held an `ready atomic.Bool` that Run() set
// immediately after net.Listen, and the API's R8.5 ErrNotReady path was a
// separate decision. A pod could therefore answer 200 on /readyz while its
// fleet was empty — the process was up, but the data it would serve was not
// correct. R8.9's amendment makes that a routing fact rather than a cosmetic
// one: the two probes are now exempt from auth *unconditionally*, so /readyz
// decides which pods a Service sends traffic to, and a probe that lies is a
// pod receiving traffic.
//
// The three clauses R8.7 now spells out, and where each is answered:
//
//	(i)   one signal, read by both surfaces. Ready() is the only read path, and
//	      both handleReadyz (the kubelet/Service probe) and the R8.5 503 path in
//	      clusters.go call it. The per-subsystem sources are assembled once at
//	      startup from whatever this process actually runs — for `pgsi serve`,
//	      the discoverer's Ready() — and New() wires them so a Server can never
//	      hold a fleet whose readiness it does not report. The API never asks the
//	      discoverer directly, so the probe and the API cannot disagree.
//
//	(ii)  readiness covers only the subsystems this process runs. A Readiness is
//	      built from the sources the role starts: discovery's first sync gates it
//	      when the process runs discovery, and D9's reserved `api` role — serve
//	      without collecting, reachable today via `serve --discover=false` — is
//	      assembled with no sources and is therefore ready as soon as it is
//	      listening, not permanently unready under a discovery-only rule. R8.7
//	      asks for the general rule now rather than at the role split, because
//	      adding the second role later would otherwise have to re-derive it.
//
//	(iii) healthz answers "is the process up", readyz answers "will it serve
//	      correct data". handleHealthz still returns 200 to any request that
//	      reaches it — if the mux answered, the process is up — and reads nothing
//	      here. Only handleReadyz does.
type Readiness struct {
	// listening is the process's own contribution: false before Run binds the
	// socket, and false again the moment shutdown begins (R13.8) so a draining
	// pod stops being handed new requests. A request that reaches handleReadyz
	// proves the socket is up, so this is only load-bearing for the drain
	// window — but R13.8 requires that window, and Phase 0's test asserted it.
	listening atomic.Bool
	// sources holds one readiness func per subsystem this process runs
	// (R8.7(ii)). Empty means the process runs nothing that can be unready.
	sources []func() bool
}

// NewReadiness assembles the signal from the subsystems this process runs
// (R8.7(ii)). Each source is a func, not an interface, because the shapes that
// answer "are you ready" differ per subsystem — discovery has Ready(), Phase 2's
// store will have its own probe — and the API only ever needs the boolean.
func NewReadiness(sources ...func() bool) *Readiness {
	return &Readiness{sources: sources}
}

// SetListening is the HTTP process's own contribution to the signal: Run() sets
// it true once the socket is bound and false before it drains (R13.8).
func (r *Readiness) SetListening(up bool) {
	if r == nil {
		return
	}
	r.listening.Store(up)
}

// Ready is the single read path R8.7(i) requires: the process is listening AND
// every subsystem it runs reports ready. A Readiness with no sources is ready
// once it is listening — that is D9's api-only role, which has nothing to
// collect and so nothing to wait for (R8.7(ii)).
//
// A nil *Readiness reports false, so a Server built without going through New
// fails closed on the probe rather than claiming readiness it cannot support.
func (r *Readiness) Ready() bool {
	if r == nil {
		return false
	}
	if !r.listening.Load() {
		return false
	}
	for _, ok := range r.sources {
		if ok == nil || !ok() {
			return false
		}
	}
	return true
}
