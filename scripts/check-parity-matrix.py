import re, sys, pathlib, subprocess
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

# §4 defined ids (def4: "8.6"-style) and the bare group ids they imply (groups: "R8")
in4, def4, groups = False, set(), set()
for line in src:
    if line.startswith("## 4."): in4 = True; continue
    if in4 and line.startswith("## ") and not line.startswith("## 4."): break
    if not in4: continue
    c = split_row(line)
    if c and re.match(r"^R\d+\.\d+[a-z]?$", c[0].replace("*", "")):
        tok = c[0].replace("*", "")
        def4.add(tok[1:])
        groups.add(tok.split(".")[0])   # "R8.6" -> "R8" (tok keeps the R; def4 strips it)

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

# --- citation check (A2's suggestion, accepted) --------------------------------------
# §5.1 rule 4 says requirement ids are the contract and that this script enforces it, but
# the enforcement only covered §4 vs §7.3 vs §6: an id cited anywhere *else* in the repo
# could be invented and nothing would notice. Two phantoms in one week — "R15.4"/"R15.5"
# in docs/licenses/README.md, and `coverage gate (R14.5)` in ci.yml (the gate is R11.11) —
# were both caught by reading, and reading is not a gate. So every R-id in the repo's
# tracked docs and config must be an id §4 defines, or a bare group id that §4 implies.
CITE_EXTS = {".md", ".yml", ".yaml", ".go"}
CITE_NAMES = {"Makefile", "makefile"}
# Ids that are not §4 ids but are not inventions either, each with the reason it is exempt.
EXEMPT = {
    "R17": "§11 rule 4's ceiling — 'R17+' is the ban on minting past R16, not an id",
    "R18": "round-1 review numbering, folded into R1–R16",
    "R19": "round-1 review numbering, folded into R1–R16",
    "R20": "round-1 review numbering, folded into R1–R16",
    "R21": "round-1 review numbering, folded into R1–R16",
    "R22": "round-1 review numbering, folded into R1–R16",
    "R23": "A3's pre-taxonomy numbering; shipped as R11.16 (requirements.md:459)",
    "R24": "A3's pre-taxonomy numbering; shipped as R16.8",
    "R25": "A3's pre-taxonomy numbering; shipped as R14.9",
    "R9.9b": "retired letter-suffix alias of R9.11 (A3-N2); the spec's alias table at :902 lists it",
    "R10.4a": "v0.3.0 review's proposed split of R10.4; shipped unsplit",
    "R10.4b": "v0.3.0 review's proposed split of R10.4; shipped unsplit",
    "R15.4": "cited in docs/licenses/README.md only to record that it never existed",
    "R15.5": "cited in docs/licenses/README.md only to record that it never existed",
    # The same treatment the licenses README gets, applied to the sentence that describes it:
    # docs/agents.md §6 cites R8.12 to say it was a phantom, and the checker correctly refuses
    # to let an undefined id through just because the sentence about it is an apology.
    "R8.12": "cited in docs/agents.md §6 only to record that it never existed (shipped as R8.6)",
}
# \d{1,2} on both halves, not \d+: without the bound, the Cloud SQL research note's
# POSTGRES_$version.R20250727.00_14 reads as an id (the prototype's one false positive).
CITE_RE = re.compile(r"\bR(\d{1,2})(?:\.(\d{1,2})([a-z])?)?\b")

def tracked_files(root):
    """Every tracked path, with a non-git fallback so the script still runs in an export."""
    try:
        out = subprocess.run(["git", "-C", str(root), "ls-files", "-z"], capture_output=True,
                             check=True).stdout.decode("utf-8", "replace").split("\0")
        return [p for p in out if p]
    except Exception:
        return [str(p.relative_to(root)) for p in root.rglob("*")
                if p.is_file() and not {".git", "node_modules", "dist", "build"} & set(p.parts)]

def cited_tokens(files):
    """(file, lineno, token) for every R-id-shaped string in the tracked files in scope."""
    out = []
    for rel in files:
        p = root / rel
        if not p.is_file(): continue
        if p.suffix not in CITE_EXTS and p.name not in CITE_NAMES: continue
        for n, line in enumerate(p.read_text(errors="replace").splitlines(), 1):
            for m in CITE_RE.finditer(line):
                out.append((rel, n, f"R{m.group(1)}"
                            + (f".{m.group(2)}{m.group(3) or ''}" if m.group(2) else "")))
    return out

root = pathlib.Path(sys.argv[1]).resolve().parent
files_all = tracked_files(root)
defined = {"R" + x for x in def4} | groups
cited = cited_tokens(files_all)
if not cited:
    print("GUARD: citation check found 0 ids — file discovery broke, not a pass")
    sys.exit(2)
cited_ids = {t for _, _, t in cited}
cite_bad = sorted({(f, n, t) for f, n, t in cited if t not in defined and t not in EXEMPT})
# An EXEMPT id that nobody cites is a dead exemption. The table is read at exactly one site —
# the filter above — so a key matching nothing is otherwise invisible and the table still looks
# populated while the check it performs has quietly stopped running. Failing here is the
# assertion form of the rule check_paths applies to PATH_EXEMPT: an exemption that would expire
# silently now leaves a red line saying so.
id_orphans = sorted(set(EXEMPT) - cited_ids)
print(f"citations: {len(cited_ids)} distinct ids across "
      f"{len({f for f, _, _ in cited})} files · {len(cite_bad)} not defined by §4 · "
      f"{len(id_orphans)} orphan exemptions")
for f, n, t in cite_bad[:40]:
    print(f"  {f}:{n}: {t}")
if len(cite_bad) > 40:
    print(f"  … {len(cite_bad) - 40} more")
for k in id_orphans[:20]:
    print(f"  orphan exemption: {k} — {EXEMPT[k]}")

# --- path citations (A4's chart/README.md find, generalised) -------------------------
# A `path:N` citation is only as durable as the file's line count. `ed58452` inserted 65
# lines at chart/README.md:93 and silently invalidated every number below the insertion;
# three of them survived two rounds of reading before A4's audit caught them, because a
# wrong number is still a plausible number. Three citation forms exist in the repo's prose
# and all three are checked:
#   A  `path:123` / `path:123-456`    — the number is attached to the path
#   B  `path` § "Heading" (:158-253)  — the durable form: the number must point at that heading
#   C  `:197`                         — bare, inherits the file from the line's first citation
# What fails the build: a resolvable path whose number is past the file's end (A, C), and a
# durable-form citation whose quoted heading is not the text at that line (B). An
# unresolvable or ambiguous path is reported but not failed — external file, deleted file
# and typo are not separable without a human. docs/research/ is excluded from this half
# entirely: those notes cite the upstream projects' own files (postgres_exporter's
# README.md:310-321), which this repo's file list cannot resolve.
PATH_SKIP_DIRS = ("docs/research/",)
# Cited paths that are known-not-to-resolve, keyed on the (citing file, cited path) pair with
# the reason, mirroring EXEMPT above. Keyed on the pair, not the bare path string, and applied
# only while the path still fails to resolve: if the repo ever gains a real
# references/components.md, IMPLEMENTATION.md's cite of it becomes checkable and gets checked,
# rather than being silently exempted forever. Without self-expiry the table is a standing
# warning that outlives its own reason — which is how real findings get skipped.
PATH_EXEMPT = {
    ("IMPLEMENTATION.md", "references/components.md"):
        "the dataviz skill bundle's own file; the bundle is cited by absolute path + sha256 in "
        "the same cell (D12), so the relative form there refers outside this repo",
}
P = r"[A-Za-z0-9_./-]+\.(?:md|go|ya?ml|tsx?|json|sh|py|js|css)"
CITE_A = re.compile(r"`(" + P + r"):(\d+)(?:\s*[-–]\s*(\d+))?`")
# The colon inside the parens is part of the form (`… (:254)`), and it is load-bearing: the
# first version of this regex allowed only `(254)` and so matched none of the five citations
# that exist — a tier that could not fail, which is the defect this script exists to catch.
CITE_B = re.compile(r"`(" + P + r")`\s*§\s*\"([^\"]+)\"\s*\(\s*:(\d+)(?:\s*[-–]\s*(\d+))?")
CITE_C = re.compile(r"`:(\d+)(?:\s*[-–]\s*(\d+))?`")

_lines = {}
def lines_of(root, rel):
    if rel not in _lines:
        _lines[rel] = (root / rel).read_text(errors="replace").splitlines()
    return _lines[rel]

def resolve_path(tracked, tracked_set, citing, path):
    """Exact, then same-directory, then unique suffix. [] = unresolvable, 2+ = ambiguous."""
    if path in tracked_set: return [path]
    d = str(pathlib.PurePosixPath(citing).parent)
    cand = path if d == "." else f"{d}/{path}"
    if cand in tracked_set: return [cand]
    return [p for p in tracked if p.endswith("/" + path)]

def path_citations(root, tracked):
    """(file, lineno, kind, cited_path, start, end, heading) for every citation in scope."""
    out = []
    for rel in tracked:
        if not rel.endswith(".md") or rel.startswith(PATH_SKIP_DIRS): continue
        for n, line in enumerate(lines_of(root, rel), 1):
            anchored = [(m.start(), "B", m.group(1), int(m.group(3)),
                         int(m.group(4) or m.group(3)), m.group(2))
                        for m in CITE_B.finditer(line)]
            anchored += [(m.start(), "A", m.group(1), int(m.group(2)),
                          int(m.group(3) or m.group(2)), None)
                         for m in CITE_A.finditer(line)]
            anchored.sort()
            for _, k, p, s, e, h in anchored:
                out.append((rel, n, k, p, s, e, h))
            if anchored:                       # C: bare `:N` inherits the line's first path
                first = anchored[0][2]
                out += [(rel, n, "C", first, int(m.group(1)), int(m.group(2) or m.group(1)), None)
                        for m in CITE_C.finditer(line)]
    return out

def check_paths(root, tracked):
    checked, bad, unresolved, exempt = 0, [], [], 0
    pairs = set()                                  # every (citing file, cited path) it found
    tset = set(tracked)
    for rel, n, kind, path, s, e, head in path_citations(root, tracked):
        pairs.add((rel, path))
        r = resolve_path(tracked, tset, rel, path)
        if not r and (rel, path) in PATH_EXEMPT:
            exempt += 1; continue              # exempt only while it still fails to resolve
        if len(r) != 1:
            unresolved.append((rel, n, kind, path, len(r))); continue
        checked += 1
        L = lines_of(root, r[0])
        if e > len(L):
            bad.append((rel, n, kind, f"{path}:{s}-{e} — {r[0]} has {len(L)} lines")); continue
        if kind == "B":                        # the durable form asserts the heading
            actual = re.sub(r"^#+\s*", "", L[s - 1]).strip()
            if not L[s - 1].startswith("#"):
                bad.append((rel, n, kind, f"{path}:{s} is not a heading ({actual[:48]!r})"))
            elif actual != head.strip():
                bad.append((rel, n, kind, f"quotes {head!r}, {path}:{s} reads {actual!r}"))
    return checked, bad, unresolved, exempt, pairs

# A4's L1, applied to this half too: gate on the file set, not on the per-half count. A repo
# with markdown but zero path citations is legal, and "file discovery broke" is false when
# discovery worked and simply found nothing to check.
scanned_md = sum(1 for rel in files_all
                 if rel.endswith(".md") and not rel.startswith(PATH_SKIP_DIRS))
if not scanned_md:
    print("GUARD: no scannable markdown — file discovery broke, not a pass")
    sys.exit(2)
checked_path, path_bad, unresolved, exempt_path, cited_pairs = check_paths(root, files_all)
# A PATH_EXEMPT key that matches no citation is a dead exemption — the table is read at one
# site, so an orphan key is invisible and the check it performs has quietly stopped running.
# Unlike an external path (reported, not failed: external/deleted/typo aren't separable), an
# orphan key is unambiguous, so it fails.
path_orphans = sorted(set(PATH_EXEMPT) - cited_pairs)
print(f"path citations: {checked_path} resolved · {len(path_bad)} wrong · "
      f"{len(unresolved)} unresolvable (reported, not failed) · {exempt_path} exempt · "
      f"{len(path_orphans)} orphan exemptions")
for f, n, kind, msg in path_bad[:20]:
    print(f"  {f}:{n} [{kind}] {msg}")
for f, n, kind, path, n_alt in unresolved[:10]:
    print(f"  {f}:{n} [{kind}] unresolved: {path} ({n_alt} match{'es' if n_alt != 1 else ''})")
for k in path_orphans[:20]:
    print(f"  orphan path exemption: {k[0]} -> {k[1]} — {PATH_EXEMPT[k]}")

# --- relative markdown links (A4's docs/README.md find, generalised) ---
# The half above validates `path:N` citations and says nothing about `[text](path)` links,
# which are the other way a doc points at a file — and in an index like docs/README.md the
# only way. A rotting link there is worse than no index, because the index is the file
# people use to find things. http(s)/mailto and pure-`#anchor` targets are skipped; a
# `#fragment` is stripped before the path is resolved.
LINK_RE = re.compile(r"\[([^\]]*)\]\(([^()\s]+)\)")
FENCE_RE = re.compile(r"^\s*(`{3,}|~{3,})")

def code_free_lines(lines):
    r"""[(lineno, text)] with fenced code blocks dropped (A3's L2).

    A `[x](y)` written inside a ``` block is illustrative markdown, not a link the doc
    asserts exists, so resolving it can report a break the doc never made. The strict
    LINK_RE hides this today only because its `[^()\s]+` rejects the spaced code targets
    that the permissive form would match — a luck, not a guard. A closing fence must be
    the same character and at least as long as the opening run, which is what lets a
    4-backtick block print a 3-backtick block inside it.
    """
    fence, out = "", []
    for n, line in enumerate(lines, 1):
        m = FENCE_RE.match(line)
        if fence:
            if m and m.group(1)[0] == fence[0] and len(m.group(1)) >= len(fence):
                fence = ""
            continue
        if m:
            fence = m.group(1)
            continue
        out.append((n, line))
    return out

def check_links(root, tracked):
    """(checked, broken, label_bad) for every relative [text](path) link in markdown.

    The broken half resolves the target. The label half checks the bracket text, but only
    when that text *names* a file: the text is prose ("see the chart README"), so comparing
    it whole to the target false-positives on the repo's own fixed form —
    .claude/agents/a4-frontend.md:58 reads [`docs/agents.md` §5.1](../../docs/agents.md),
    whose text is "docs/agents.md` §5.1" and whose target is "docs/agents.md".

    The 18/0 figure, with its recipe, because the number is recipe-dependent and this docstring
    has now been wrong about its own numbers twice: f14d974 shipped "18 of the 70" with no
    command (A4's review could not rebuild it and listed five other whole-text variants), and
    ac6dfc2 added the command but attributed the neighbour numbers to the wrong recipes. The
    whole matrix, over the 70 links this function scans, text side x target side:

        raw text             vs full path 69   vs basename(path) 67   both basenamed 62
        strip-end backticks  vs full path 28   vs basename(path) 53   both basenamed 18
        strip-all backticks  vs full path 28   vs basename(path) 53   both basenamed 18

    The shipped label half is the token branch — re.search(P, text), then basename-vs-basename —
    which gives 0 on every one of those rows' link set; the whole-text rows are the counterfactual
    that justifies it. "strip-end" is .strip("`") (backticks at the ends only), "strip-all" is
    .replace("`", "") — identical numbers here, so the distinction is named rather than assumed.
    (The :58 cite here was :52 when f14d974 was written: the line moved in db41cbb. The
    path-citation half of this script checks .md files only, so a stale cite inside this .py
    docstring is invisible to the tool that exists to catch it.)
    """
    checked, broken, label_bad = 0, [], []
    for rel in tracked:
        if not rel.endswith(".md") or rel.startswith(PATH_SKIP_DIRS): continue
        d = (root / rel).parent
        for n, line in code_free_lines(lines_of(root, rel)):
            for m in LINK_RE.finditer(line):
                tgt, text = m.group(2), m.group(1)
                if tgt.startswith(("http://", "https://", "mailto:")): continue
                path = tgt.split("#")[0]
                if not path: continue              # pure #anchor: no file to resolve
                checked += 1
                if not (d / path).resolve().exists():
                    broken.append((rel, n, tgt))
                named = re.search(P, text)
                if named and named.group(0).rsplit("/", 1)[-1] != path.rsplit("/", 1)[-1]:
                    label_bad.append((rel, n, text, path))
    return checked, broken, label_bad

# ---------------------------------------------------------------------------
# R14.8's status column: the closed enum, asserted.
#
# requirements.md R14.8 names the status values; before this check existed the
# tables used twenty distinct spellings across 77 rows -- `satisfied (A4)`,
# `implemented, **provisional, not validated**`, `contract validated in Phase 1a`,
# `transferred to Phase 5`, `documented` -- which is why three agents quoted three
# different numbers for Phase 4 and why the coordinator reported overall progress
# to the user wrong twice. The enum was in the spec's prose, and prose is not a
# gate: this is. Every requirement row's last cell must be `**<enum>**`, optionally
# ` (A<n>)` for the R14.9 owner attribution, then ` -- <prose>`.
#
# split_row() at the top splits on every "|", which is wrong for a cell holding an
# escaped pipe (`--auth=none\|token\|kube-rbac-proxy` in §3.7's R8.8 row): the
# escape is exactly what makes that cell render as one cell, and a naive split turns
# it into three. This check splits on unescaped pipes only.
STATUS_ENUM = ("not-started", "in-progress", "implemented", "validated", "blocked", "tabled")
STATUS_CELL_RE = re.compile(
    r"^\*\*(" + "|".join(STATUS_ENUM) + r")(?: \((A[1-6])\))?\*\*(?: \u2014 .*)?$",
    re.S)
STATUS_ROW_ID_RE = re.compile(r"^R\d+\.\d+[a-z]?$")

def split_row_esc(line):
    return [c.strip() for c in re.split(r"(?<!\\)\|", line.strip().strip("|"))]

def check_status_enum(root):
    """Every R-id row in IMPLEMENTATION.md's R14.8 tables, and its status cell."""
    rows, bad = 0, []
    for n, line in enumerate(lines_of(root, "IMPLEMENTATION.md"), 1):
        if not line.startswith("| ") or re.match(r"^\|[\s:|-]+\|$", line):
            continue
        cells = split_row_esc(line)
        if not cells or not STATUS_ROW_ID_RE.match(cells[0]):
            continue
        rows += 1
        if not STATUS_CELL_RE.match(cells[-1]):
            bad.append((n, cells[0], cells[-1][:60]))
    return rows, bad

status_rows, status_bad = check_status_enum(root)
print(f"status cells: {status_rows} requirement rows \u00b7 {len(status_bad)} non-canonical "
      f"(enum: {'|'.join(STATUS_ENUM)})")
for n, rid, cell in status_bad[:20]:
    print(f"  IMPLEMENTATION.md:{n} {rid}: {cell!r}")

checked_links, links_bad, labels_bad = check_links(root, files_all)
if not scanned_md:
    print("GUARD: link check scanned 0 markdown files — file discovery broke, not a pass")
    sys.exit(2)
print(f"markdown links: {checked_links} checked \u00b7 {len(links_bad)} broken \u00b7 "
      f"{len(labels_bad)} label mismatches")
for f, n, tgt in links_bad[:20]:
    print(f"  {f}:{n} -> {tgt}")
for f, n, text, path in labels_bad[:20]:
    print(f"  {f}:{n} label {text!r} names {path.rsplit('/', 1)[-1]}, target is "
          f"{path!r}")

sys.exit(1 if (bad or missing or cite_bad or path_bad or links_bad
              or id_orphans or path_orphans or labels_bad or status_bad) else 0)

