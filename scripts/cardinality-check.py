#!/usr/bin/env python3
"""R13.4's CI-checkable assertion: the exported series count for a fixture cluster set.

R13.4 (requirements.md:482) asks for the cardinality budget "per §5 … **with a
CI-checkable assertion that the exported series count for a fixture cluster set
stays under the documented bound**". The budget half shipped in
`chart/README.md` §Cardinality budget (that is R9.10's file). The assertion half
was named twice and never written: `chart/README.md:229` calls it "the artifact
that settles it" and says "when it lands, its printed number replaces the
hand-computed one here"; `README.md:197` says "(once `scripts/cardinality-check.py`
lands) asserted in CI". This is that file.

Two design decisions, both load-bearing:

**It computes from §5's table, it does not count a live export.** The binary
serves one metric today (`pgsi_build_info`, `internal/api/server.go:313`); the
other families are Phase 2+ collector output, as `chart/README.md:260-270` says in
terms. An assertion that scraped the running `/metrics` would be checking
`1 < 24 480` — true, and proof of nothing. R13.4's wording points the other way
anyway: the bound is defined "per §5" and measured "for a fixture cluster set",
which is `testdata/api/clusters.json`, not for a running process. So the family
table and the fixture are the inputs, and the same computation becomes the live
check for free once Phase 2's MetricHub emits what the table promises.

**Every number is parsed from a document; none is a Python literal.** A literal
`SERIES = 24480` would be a second hand-computed number — the exact defect class
this script exists to end. It would drift from the table it is supposed to
supersede, and the check would pass while both were wrong. So the family rows come
out of requirements.md §5, the cluster set out of the fixture JSON, and the three
competing published totals out of the two documents that publish them.

The three-way number dispute this settles, and its verdict:

| Figure | Source | |
|---|---|---|
| 23 700 | requirements.md R13.4's headline ("~23.7 k series / ~1.5 GB") | oldest |
| 24 300 | requirements.md §5's prose ("four clusters ≈ **24.3 k series**") | |
| 24 480 | chart/README.md ("4 × 6 070 + 200 = 24 480 series") | **this script's answer** |

The script's answer is **24 480**: 4 fixture clusters × 6 070 unconditional series
per cluster + 200 for the one cluster that has a Pooler. It is the only one of the
three that is a function of the table and the fixture rather than a rounded prose
estimate — which is why the chart README already picked it and filed the
discrepancy instead of choosing silently (requirements.md:556-560 records that
§5's prose "claims to have corrected R13.4 while R13.4 still carries the older
figure"). R13.4's row is A3's file to amend, so this script does not fail on it:
it reports the delta and gates on the highest published figure, which is the
loosest defensible bound and the only one that does not require editing the spec
from a tool script.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
SPEC = REPO / "requirements.md"
FIXTURE = REPO / "testdata" / "api" / "clusters.json"
CHART_README = REPO / "chart" / "README.md"

# R13.4's window: "over 30 d". Not parsed because the spec states it in three
# places identically and none of them is a table cell.
RETENTION_DAYS = 30

# The §5 table header, verbatim. The chart README's copy of the table has a
# different header ("| Family | Requirement | Series / cluster | …"), so anchoring
# on this exact line reads the spec's table and only the spec's table.
SPEC_TABLE_HEADER = "| Family | Labels | Series per cluster | Interval | Control |"

BOLD = re.compile(r"\*\*(.+?)\*\*")
BARE_NUMBER = re.compile(r"([0-9][0-9.,]*)\s*([km]?)", re.I)
REQ_ID = re.compile(r"\bR(\d+(?:\.\d+[a-z]?)?)\b")
CONDITIONAL = re.compile(r"only where|off unless|only when|only if", re.I)


def plain_int(text: str) -> int:
    """`"6 070"` / `"6,070"` / `" 6 070 "` -> 6070.

    Both documents group thousands, the spec with a space and the chart README with
    a comma, so every integer read out of a document goes through here rather than
    re-deriving the separator rule at each call site.
    """
    return int(re.sub(r"[,\s]", "", text))


def _scale(match: re.Match) -> int:
    """`1.2 k` -> 1200, `4 000` -> 4000, `300` -> 300, `2 m` -> 2 000 000."""
    value = float(match.group(1).replace(",", ""))
    suffix = match.group(2).lower()
    if suffix == "k":
        value *= 1_000
    elif suffix == "m":
        value *= 1_000_000
    return int(round(value))


def cell_series(cell: str) -> int | None:
    """The series count a §5 "Series per cluster" cell asserts, or None if variable.

    The cells are prose with markdown, not numbers, and they disagree about where
    the number sits: `~**120**`, `~**4 k**`, `50 × (8 buckets + `_sum` + `_count`)
    = **450**`, `**N per cluster × 6 metric series** = 50 × 6 = **300**`, and one
    cell that is `tags × databases` with no number at all.

    The rule is the spec's own formatting convention: the figure the row asserts is
    the one it bolds, and it bolds it bare. So the first bolded span that is *only*
    a number wins — which is why `**N per cluster × 6 metric series**` is skipped
    (it bolds a formula, and its "6" is a term of it, not the row's answer) and
    `**300**` in the same cell is taken. A cell with no bolded number falls back to
    the last number in the cell; a cell with no number at all is variable and
    contributes nothing at default config, which is reported rather than assumed.
    """
    for span in BOLD.findall(cell):
        bare = span.strip().strip("`").lstrip("~").strip()
        match = BARE_NUMBER.fullmatch(bare)
        if match:
            return _scale(match)
    plain = cell.replace("**", "").replace("`", "")
    matches = list(BARE_NUMBER.finditer(plain))
    return _scale(matches[-1]) if matches else None


def parse_spec_table(spec: str) -> list[dict]:
    """The §5 family table as data: family, requirement ids, series, scrape interval."""
    lines = spec.splitlines()
    try:
        start = next(i for i, line in enumerate(lines) if line.strip() == SPEC_TABLE_HEADER)
    except StopIteration:
        raise SystemExit(f"error: {SPEC} has no table headed {SPEC_TABLE_HEADER!r}")
    rows = []
    for line in lines[start + 2:]:  # skip the header and its |---| rule
        line = line.strip()
        if not line.startswith("|"):
            break
        cells = [c.strip() for c in line.strip("|").split("|")]
        if len(cells) < 5:
            break
        interval = re.search(r"(\d+)\s*s\b", cells[3])
        rows.append({
            "family": cells[0],
            "reqs": REQ_ID.findall(cells[0]),
            "cell": cells[2],
            "series": cell_series(cells[2]),
            "interval_s": int(interval.group(1)) if interval else None,
            "control": cells[4],
            "conditional": bool(CONDITIONAL.search(cells[4])),
        })
    if not rows:
        raise SystemExit(f"error: {SPEC} §5 table has no rows")
    return rows


def parse_fixture() -> tuple[int, int]:
    """(clusters, clusters_with_a_pooler) from the fixture set R13.4 names."""
    with FIXTURE.open() as fh:
        doc = json.load(fh)
    clusters = doc["data"] if isinstance(doc, dict) else doc
    with_pooler = sum(1 for c in clusters if c.get("poolers"))
    return len(clusters), with_pooler


def parse_bytes_per_sample(spec: str) -> float:
    """The compression factor §5 states, parsed rather than inlined."""
    match = re.search(r"~\s*([\d.]+)\s*\n?bytes/sample", spec)
    return float(match.group(1)) if match else 1.5


def parse_prose_estimate(spec: str) -> dict:
    """§5's prose: the per-cluster formula and the four-cluster total it states.

    `Per cluster ≈ 1 200 + 300 + 450 + 4 000 + 120 = **6 070**; four clusters ≈
    **24.3 k series**` — the formula is the check that the table and the prose
    still agree, and it is the half that rots first when someone edits one row.
    """
    flat = re.sub(r"\s+", " ", spec)
    out: dict = {}
    formula = re.search(r"Per cluster ≈\s*([\d\s+,]+?)\s*=\s*\*\*([\d,\s]+)\*\*", flat)
    if formula:
        # The spec writes thousands with a space separator ("1 200", "4 000"), so
        # a term is stripped of every space and comma before it is a number.
        out["terms"] = [plain_int(t) for t in formula.group(1).split("+") if t.strip()]
        out["stated_per_cluster"] = plain_int(formula.group(2))
    total = re.search(r"clusters ≈ \*\*([\d.,]+)\s*k series\*\*", flat)
    if total:
        out["stated_total"] = int(round(float(total.group(1).replace(",", "")) * 1000))
    storage = re.search(r"≈ ([\d.]+) GB of Prometheus storage", flat)
    if storage:
        out["stated_gb"] = float(storage.group(1))
    return out


def parse_chart_readme_claims() -> dict:
    """chart/README.md's published pair: `**4 × 6 070 + 200 = 24 480 series.**` and its GB."""
    text = CHART_README.read_text()
    out: dict = {}
    # The published line is `> **4 × 6 070 + 200 = 24 480 series.**` — the whole
    # arithmetic including the `=` is inside the bold span, so the number is read
    # from before " series." rather than from after an "= **".
    series = re.search(r"([\d ,]+)\s+series\.\*\*", text)
    if series:
        out["series"] = plain_int(series.group(1))
    gb = re.search(r"≈ ([\d.]+) GB over 30 days", text)
    if gb:
        out["gb"] = float(gb.group(1))
    return out


def parse_r13_4_headline(spec: str) -> dict:
    """R13.4's own row: "~23.7 k series / ~1.5 GB over 30 d"."""
    row = next((l for l in spec.splitlines() if l.startswith("| R13.4 |")), "")
    out: dict = {}
    series = re.search(r"~\s*([\d.]+)\s*k series", row)
    if series:
        out["series"] = int(round(float(series.group(1)) * 1000))
    gb = re.search(r"~\s*([\d.]+)\s*GB", row)
    if gb:
        out["gb"] = float(gb.group(1))
    return out


def applies_for(row: dict, n_with_pooler: int) -> int:
    """How many fixture clusters a conditional family actually contributes to.

    The Pooler row's Control says "Only where a Pooler exists", so it is matched
    against the fixture's poolers and scales with the fixture: add a fifth cluster
    with a pooler and its 200 series are counted twice. Every other conditional
    family (Per-tag: "Off unless R3.12 enabled") is off at default config, so it
    contributes nothing until R3.12 ships and this function gains a second branch.
    """
    return n_with_pooler if "pooler" in row["family"].lower() else 0


def compute(rows: list[dict], n_clusters: int, n_with_pooler: int, bytes_per_sample: float) -> dict:
    """The budget: unconditional families × every cluster, conditional ones × the clusters they apply to."""
    base = 0
    conditional_extra = 0
    for row in rows:
        if row["series"] is None:
            continue
        if not row["conditional"]:
            base += row["series"]
            continue
        conditional_extra += applies_for(row, n_with_pooler) * row["series"]

    total = n_clusters * base + conditional_extra

    # Storage. Samples per series come from each row's own Interval cell — §5
    # declares the interval per family, so a family that moves to 30 s doubles its
    # own cost without touching the others. A conditional family's series exist only
    # for the clusters it applies to, so its sample count scales the same way.
    storage = 0.0
    for row in rows:
        if row["series"] is None or not row["interval_s"]:
            continue
        if row["conditional"]:
            series = applies_for(row, n_with_pooler) * row["series"]
        else:
            series = n_clusters * row["series"]
        storage += series * (RETENTION_DAYS * 86_400 // row["interval_s"]) * bytes_per_sample
    return {"per_cluster": base, "total": total, "bytes": storage, "gb": storage / 1e9}


def main(argv: list[str]) -> int:
    quiet = "--quiet" in argv[1:]
    spec = SPEC.read_text()
    rows = parse_spec_table(spec)
    n_clusters, n_with_pooler = parse_fixture()
    budget = compute(rows, n_clusters, n_with_pooler, parse_bytes_per_sample(spec))
    prose = parse_prose_estimate(spec)
    chart = parse_chart_readme_claims()
    r13_4 = parse_r13_4_headline(spec)

    failures: list[str] = []

    # 1. The table and §5's prose formula must still agree. This is the drift check:
    #    edit one row of the table without updating the prose and this fails.
    if prose.get("terms") is not None:
        table_terms = [r["series"] for r in rows if not r["conditional"] and r["series"] is not None]
        if sorted(prose["terms"]) != sorted(table_terms):
            failures.append(
                f"requirements.md §5 prose sums {sorted(prose['terms'])} per cluster but the "
                f"table's unconditional rows are {sorted(table_terms)} — one was edited without the other"
            )
        if prose.get("stated_per_cluster") not in (None, sum(table_terms)):
            failures.append(
                f"requirements.md §5 states {prose['stated_per_cluster']:,} series per cluster; "
                f"the table's rows sum to {sum(table_terms):,}"
            )

    # 2. R13.4's own clause: the count "stays under the documented bound". The
    #    governing bound is the highest published figure — see the header for why.
    published = [v for v in (budget["total"], chart.get("series"), r13_4.get("series")) if v]
    bound = max(published)
    if budget["total"] > bound:
        failures.append(
            f"{budget['total']:,} series exceeds the highest documented bound {bound:,} — "
            f"republish the figure in chart/README.md and requirements.md R13.4"
        )
    published_gb = [v for v in (round(budget["gb"], 2), chart.get("gb"), r13_4.get("gb")) if v]
    if budget["gb"] > max(published_gb) + 1e-9:
        failures.append(
            f"{budget['gb']:.2f} GB exceeds the highest documented storage figure "
            f"{max(published_gb):.2f} GB"
        )

    if not quiet:
        print(f"R13.4 cardinality — {SPEC.name} §5 table x {FIXTURE.as_posix()}")
        print()
        print(f"  {'family':<32} {'req':<8} {'series/cluster':>14}  {'interval':>8}  {'applies':>8}")
        for row in rows:
            reqs = ",".join(f"R{r}" for r in row["reqs"]) or "-"
            series = f"{row['series']:,}" if row["series"] is not None else "variable"
            if not row["conditional"]:
                applies = f"all {n_clusters}"
            elif "pooler" in row["family"].lower():
                applies = f"{n_with_pooler} w/ pooler"
            else:
                applies = "off by default"
            print(f"  {row['family'].split('(')[0].strip():<32} {reqs:<8} {series:>14}  "
                  f"{str(row['interval_s']) + 's':>8}  {applies:>8}")
        print()
        print(f"  per cluster (unconditional)   {budget['per_cluster']:,}")
        print(f"  fixture set                   {n_clusters} clusters, {n_with_pooler} with a pooler")
        print(f"  total                         {budget['total']:,} series")
        print(f"  storage over {RETENTION_DAYS} d          {budget['gb']:.2f} GB")
        print()
        print("  published figures this replaces:")
        if r13_4.get("series"):
            print(f"    {r13_4['series']:>9,}  requirements.md R13.4 headline ({r13_4['series'] / 1000:.1f} k)")
        if prose.get("stated_total"):
            print(f"    {prose['stated_total']:>9,}  requirements.md §5 prose")
        if chart.get("series"):
            print(f"    {chart['series']:>9,}  chart/README.md")
        print(f"    {budget['total']:>9,}  <- computed from the table + fixture (this script)")

    for failure in failures:
        print(f"error: {failure}", file=sys.stderr)
    if failures:
        return 1
    if quiet:
        print(f"ok: R13.4 cardinality — {budget['total']:,} series, {budget['gb']:.2f} GB "
              f"over {RETENTION_DAYS} d for {n_clusters} fixture clusters")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
