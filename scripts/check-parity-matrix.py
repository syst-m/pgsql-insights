import re, sys, pathlib
src = pathlib.Path(sys.argv[1]).read_text().splitlines()

def split_row(line):
    if not line.startswith("|"): return None
    return [c.strip() for c in line.strip().strip("|").split("|")]

def clean(tok):
    tok = re.sub(r"\([^)]*\)", "", tok)   # drop (...) annotations
    return tok.replace("*", "").strip()   # drop emphasis markers

def split_tokens(cell):
    out, buf, depth = [], "", 0
    for ch in cell:
        if ch == "(": depth += 1
        elif ch == ")": depth = max(0, depth - 1)
        if depth == 0 and ch in ",·":
            out.append(buf); buf = ""
        else:
            buf += ch
    out.append(buf)
    return [t for t in (x.strip() for x in out) if t]

def expand(cell):
    out = []
    for tok in split_tokens(cell):
        tok = clean(tok)
        m = re.match(r"^(\d+)\.(\d+)([a-z])$", tok)          # 1.9b
        if m: out.append(f"{m.group(1)}.{m.group(2)}{m.group(3)}"); continue
        m = re.match(r"^(\d+)\.(\d+)$", tok)                  # 1.9
        if m: out.append(f"{m.group(1)}.{m.group(2)}"); continue
        m = re.match(r"^(\d+)\.(\d+)[–-](\d+)$", tok)         # 11.1-11.7 (same group)
        if m:
            for i in range(int(m.group(2)), int(m.group(3)) + 1): out.append(f"{m.group(1)}.{i}")
            continue
        m = re.match(r"^(\d+)\.(\d+)[–-](\d+)\.(\d+)$", tok)  # 1.1-1.9 (explicit group)
        if m and m.group(1) == m.group(3):
            for i in range(int(m.group(2)), int(m.group(4)) + 1): out.append(f"{m.group(1)}.{i}")
    return out

# §4 defined ids
in4, def4 = False, set()
for line in src:
    if line.startswith("## 4."): in4 = True; continue
    if in4 and line.startswith("## ") and not line.startswith("## 4."): break
    if not in4: continue
    c = split_row(line)
    if c and re.match(r"^R\d+\.\d+[a-z]?$", c[0].replace("*", "")):
        def4.add(c[0].replace("*", "")[1:])

# §7.3 index  id -> set(phase-token)
def phase_tokens(phase):
    phase = clean(phase).replace("**", "")
    t = set()
    if "post-0.1" in phase: t.add("post-0.1"); phase = phase.replace("post-0.1", "")
    if "every phase" in phase: t.add("every phase")
    t |= set(re.findall(r"\b(\d+[ab]?)\b", phase))
    return t

index, in73 = {}, False
for line in src:
    if line.startswith("### 7.3"): in73 = True; continue
    if in73 and line.startswith("## ") and not line.startswith("### "): break
    if not in73: continue
    c = split_row(line)
    if not c or len(c) < 2 or not re.match(r"^R\d+$", c[0]): continue
    for part in re.split(r"[·]", c[1]):
        if "→" not in part: continue
        ids, phase = part.split("→", 1)
        ph = phase_tokens(phase)
        if not ph: continue
        for fid in expand(ids): index.setdefault(fid, set()).update(ph)

# §6 rows: id -> set(phase)  (per-id pairing keyed by ID, not by phase)
rows, in6 = [], False
for line in src:
    if line.startswith("## 6."): in6 = True; continue
    if in6 and line.startswith("## ") and not line.startswith("## 6."): break
    if not in6: continue
    c = split_row(line)
    if not c or len(c) != 5: continue
    if c[0] in ("Cloud SQL capability",) or set(c[0]) <= set("-"): continue
    rows.append((c[0], c[3], c[4]))

bad = []
for cap, reqcell, phasecell in rows:
    if reqcell in ("—", "") or phasecell in ("—", ""): continue
    reqs = re.findall(r"R\d+\.\d+[a-z]?", reqcell)
    if not reqs: continue
    per_id = {}
    for ph, rid in re.findall(r"(\d+[a-z]?)\s*\((R\d+\.\d+[a-z]?)\)", phasecell):
        per_id.setdefault(rid, set()).add(ph)
    tmp = phasecell
    bare = set()
    if "post-0.1" in tmp: bare.add("post-0.1"); tmp = tmp.replace("post-0.1", "")
    tmp = re.sub(r"\(R\d+\.\d+[a-z]?\)", "", tmp)
    bare |= set(re.findall(r"\b(\d+[ab]?)\b", tmp))
    for r in reqs:
        own = index.get(r[1:])
        if own is None:
            bad.append((cap, r, "NOT IN §7.3 INDEX", sorted(bare))); continue
        want = per_id.get(r, bare)
        if not want: continue
        if not (own & want or "every phase" in own):
            bad.append((cap, r, f"§7.3 {sorted(own)}", f"§6 {sorted(want)}"))

print(f"§4 defines {len(def4)} ids · §7.3 indexes {len(index)} ids · §6 rows {len(rows)}")
print("in §4 not in §7.3:", sorted(def4 - set(index)))
print("in §7.3 not in §4:", sorted(set(index) - def4))
# --- A3-Q1.2: the annotated-ID list, generated (was hand-maintained in §7.3's prose) ---
annotated = {}
for line in src:
    if line.startswith("### 7.3"): a_line = True
    c = split_row(line)
    if not c or not re.match(r"^R\d+$", c[0]): continue
    for part in re.split(r"[·]", c[1]):
        if "→" not in part: continue
        ids, phase = part.split("→", 1)
        note = re.findall(r"\(([^)]*)\)", part)
        for fid in expand(ids):
            if note:
                annotated.setdefault(fid, set()).update(n.strip() for n in note if n.strip())
print("annotated ids (generated):", ", ".join(f"{k}:{sorted(v)[0]}" for k, v in sorted(annotated.items())) or "none")
print(f"mismatches: {len(bad)}")
for b in bad: print("  ", b)

# --- enforcement (added to A3's reference impl, which printed but always exited 0) ---
missing = sorted(def4 - set(index)) + sorted(set(index) - def4)
if not rows:
    print("GUARD: §6 parsed 0 rows — the table shape changed, not a pass")
    sys.exit(2)
if not def4:
    print("GUARD: §4 parsed 0 ids — the table shape changed, not a pass")
    sys.exit(2)
sys.exit(1 if (bad or missing) else 0)
