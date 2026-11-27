# Conventions for whoever picks this up

- **Commit messages**: end with the `Co-Authored-By:` line for the model that made the commit —
  `Claude Opus 5.5 (1M context)` from an opus agent, `Claude Sonnet 5.5` from a sonnet one.
  Phase 0's commit (opus) and Phase 1b's (sonnet) differ for this reason; the line is not
  a fixed string to copy across agents, it is the committing model's own attribution.
- **One commit per phase**, containing that phase's code **and** its `IMPLEMENTATION.md` update
  (R14.1 scopes this to the last commit of the phase).
- **Requirement IDs are the contract.** Code comments reference `R3.4`, not "the fingerprinting bit".
- **Never add a metric or route that isn't in the docs** (R14.7) — the metrics catalogue and
  `api/openapi.yaml` are generated and CI-checked.
- **Read the `dataviz` skill before writing any chart code** (R10.2), and record the chosen chart
  library in §4 of this file. That is a Phase 5 gate, not a Phase 5 suggestion.
- **Do not spawn a fifth subagent.** The cap is four (§2).
- **Every §4 requirement ID maps to exactly one phase** (spec §7.3). Adding a requirement without a
  phase assignment is a spec defect, not a scheduling detail — that is how 0.1.0 lost 18 IDs.
