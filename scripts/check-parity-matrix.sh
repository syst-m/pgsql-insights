#!/usr/bin/env bash
# R14.8 / §6-vs-§7.3 parity gate.
#
# The parser lives in check-parity-matrix.py (A3's v0.3.0 reference implementation, plus
# the sys.exit their version omitted — theirs printed "mismatches: 3" and still exited 0,
# which is the same report-without-enforcing class their review found in mine).
#
# CI runs this from the lint job, and requirements.md §6's preamble cites it as the reason
# the Phase column is enforced rather than merely claimed to be generated.
set -euo pipefail
cd "$(dirname "$0")/.."
exec python3 scripts/check-parity-matrix.py requirements.md
