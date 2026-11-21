#!/usr/bin/env bash
# R14.8 / §6 CI check: the parity matrix's Phase column must agree with the §7.3 index.
#
# §6's preamble claims the Phase column "is generated from §7.3's requirement→phase index".
# That claim was written in v0.2.0 while the cells were in fact hand-maintained, and four of
# them disagreed with the index (A3-M5, v0.3.0 review). This script enforces the claim so the
# next edit can't silently break it again.
#
# Usage: scripts/check-parity-matrix.sh [path/to/requirements.md]
set -euo pipefail
exec python3 - "${1:-requirements.md}" <<'PY'
import re, sys, pathlib, json

src = pathlib.Path(sys.argv[1]).read_text().splitlines()

def split_row(line):
    if not line.startswith("|"): return None
    return [c.strip() for c in line.strip().strip("|").split("|")]

# ---- §7.3 index: group -> {full_id: set(phase)} -------------------------------
def expand(cell):
    """'11.1–11.7, 11.9' plus 'R11.8' -> ['11.1','11.2',...,'11.7','11.9']"""
    out = []
    for tok in re.split(r"[,·]", cell):
        tok = re.sub(r"\*.*?\*", "", tok).strip().strip("*").strip()
        m = re.match(r"^(\d+)\.(\d+)(?:[–-](?:\d+\.)?(\d+))?(?:[a-z])?$", tok)
        if not m:
            m2 = re.match(r"^(\d+)\.(\d+)([a-z])$", tok)
            if m2: out.append(f"{m2.group(1)}.{m2.group(2)}{m2.group(3)}")
            continue
        g, a, b = m.group(1), int(m.group(2)), m.group(3)
        if b is None:
            out.append(f"{g}.{a}")
        else:
            for i in range(a, int(b) + 1): out.append(f"{g}.{i}")
    return out

index, in73 = {}, False
for line in src:
    if line.startswith("### 7.3"): in73 = True; continue
    if in73 and line.startswith("## ") and not line.startswith("### "): break
    if not in73: continue
    c = split_row(line)
    if not c or len(c) < 2 or not re.match(r"^R\d+$", c[0]): continue
    g = c[0][1:]
    for part in re.split(r"[·]", c[1]):
        if "→" not in part: continue
        ids, phase = part.split("→", 1)
        phase = re.sub(r"\*.*?\*", "", phase).strip().strip("*").strip()
        if not phase: continue
        for fid in expand(ids):
            index.setdefault(fid, set()).add(phase)

# ---- §6 matrix rows -----------------------------------------------------------
rows, in6 = [], False
for line in src:
    if line.startswith("## 6."): in6 = True; continue
    if in6 and line.startswith("## ") and not line.startswith("## 6."): break
    if not in6: continue
    c = split_row(line)
    if not c or len(c) != 5: continue
    if c[0] in ("Cloud SQL capability",) or set(c[0]) <= set("-"): continue
    rows.append((c[0], c[3], c[4]))

# ---- cross-check --------------------------------------------------------------
bad = []
for cap, reqcell, phasecell in rows:
    if reqcell in ("—", "") or phasecell in ("—", ""): continue
    reqs = re.findall(r"R\d+\.\d+[a-z]?", reqcell)
    if not reqs: continue
    # Phase cell either enumerates per-id phases ("7 (R5.7), 2 (R9.3)") or is a bare list.
    per_id = dict(re.findall(r"(\d+[a-z]?)\s*\((R\d+\.\d+[a-z]?)\)", phasecell))
    tmp = phasecell
    bare = set()
    if "post-0.1" in tmp:            # a phase *named* post-0.1, not phases 0 and 1
        bare.add("post-0.1"); tmp = tmp.replace("post-0.1", "")
    tmp = re.sub(r"\(R\d+\.\d+[a-z]?\)", "", tmp)
    bare |= set(re.findall(r"\b(\d+[ab]?)\b", tmp))
    for r in reqs:
        own = index.get(r[1:]) if r.startswith("R") else index.get(r)
        if own is None:
            bad.append((cap, r, "not in §7.3 index", sorted(bare))); continue
        want = {per_id[r]} if r in per_id else bare
        if not want: continue
        if not own & want:
            bad.append((cap, r, f"§7.3 says {sorted(own)}", f"§6 says {sorted(want)}"))

print(f"§7.3 index: {len(index)} ids · §6 rows checked: {len(rows)}")
if bad:
    print(f"\n{len(bad)} mismatch(es) — §6 Phase column vs §7.3 index:\n")
    for cap, r, a, b in bad:
        print(f"  {r:<8} {cap[:46]:<46}  {a}  vs  {b}")
    sys.exit(1)
print("§6 Phase column agrees with the §7.3 index.")
PY
