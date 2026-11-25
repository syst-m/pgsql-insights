import re, sys, pathlib, subprocess, inspect
if len(sys.argv) < 2:
    # The runner is scripts/check-parity-matrix.sh, which passes requirements.md. Called
    # bare, this raised IndexError and exited 1 -- a usage error wearing the exit code of a
    # failed gate, which is how a green tree got reported red (the coordinator ran
    # `python3 scripts/check-parity-matrix.py` with no argument on 2026-11-24 and read the
    # traceback as a gate failure). Usage exits 2, the code the GUARD paths already use for
    # "the table shape changed, not a pass"; 1 means the gate ran and found something; 0
    # means it ran and found nothing. Three states, three codes.
    print("USAGE: scripts/check-parity-matrix.sh"
          "  (or: python3 scripts/check-parity-matrix.py requirements.md)")
    sys.exit(2)
src = pathlib.Path(sys.argv[1]).read_text().splitlines()

def dump(items, render, cap=20):
    """Print at most `cap` rendered lines for `items`, then say how many it withheld.

    The finding-index line printed "25 rows never cited" and enumerated 20 of them with no
    line saying five were withheld, so the summary and the list under it disagreed and the
    output gave no hint (coordinator's finding, 2026-11-24). The cause was a bare
    `for t in uncited_f[:20]:` sitting under a summary that printed `len(uncited_f)`: a
    silent slice. The file had fifteen of them, measured at `e6a4921` (A3's S82: the row and this docstring
    both said seventeen, which is the number of data sets routed through `dump()`, not the number of
    bare slices; eighteen is the number of `dump()` call sites including the self-lint) -- and
    exactly one, `cite_bad`, which had
    its own `if len(cite_bad) > 40: print("  ... more")` two lines -- printed the remainder;
    the other sixteen truncated without saying so. This is that one line, generalised, and
    SLICE_LOOP_RE at the bottom fails the build on any new bare slice so the convention
    cannot rot back to one site out of seventeen.
    """
    items = list(items)
    # Arity dispatch: the converted call sites pass either a one-parameter lambda (the item is
    # the value: a finding id, an exemption key) or a lambda with one parameter per tuple field
    # (a citation is (file, line, id)). render(x) on a 3-tuple raises TypeError, and at the
    # commit that added this helper every multi-parameter site had an empty set, so the loop
    # body never ran and the crash waited until the first non-empty path_bad two commits later.
    # A helper whose broken branches are the ones no data exercised is the same failure as the
    # silent slice it replaced: the shape that is never executed is the one that is untested.
    n = len(inspect.signature(render).parameters)
    for x in items[:cap]:
        print(render(*x) if n > 1 else render(x))
    if len(items) > cap:
        print(f"  \u2026 {len(items) - cap} more not listed")

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
# A3's S39: the list used to be print-only -- assigned, populated, printed, read by no check
# and not by the exit line, so a note the parser cannot read just made the line shorter.
# text_notes is the source side of the same count: every parenthesised note the §7.3
# cells contain, whether or not the parser managed to attach it to an id. A note in the
# text that reaches no id is now a failure rather than a shorter print.
text_notes = set()
a_line = False   # A3's S40: was assigned-only; the two reads added with it need an init
for line in src:
    if line.startswith("### 7.3"): a_line = True; continue
    # A3's S40: a_line used to be assigned and never read, so this loop walked every
    # `^R\d+$` row in the document -- Appendix A's change-log rows included, which
    # is why `(A3-M10)` at requirements.md:924 was being reported as a §7.3 note.
    # The two sibling parsers (:63 for §7.3's index, :77 for §6's rows) both close
    # their section with the same two lines; this one now does too.
    if a_line and line.startswith("## ") and not line.startswith("### "): break
    if not a_line: continue
    c = split_row(line)
    if not c or not re.match(r"^R\d+$", c[0]): continue
    for part in re.split(r"[·]", c[1]):
        if "→" not in part: continue
        ids, phase = part.split("→", 1)
        # Two placements, and the two forms name them differently. A scope-split attaches
        # the note to an id (`10.4 *(inventory columns)*`, `9.7 (partial)`); a re-validation
        # attaches it to the phase (`→ **1a** *(partial)*`) and applies to every id in the
        # part. The first version ran one findall over the whole part and applied every note
        # to every id, so `10.1, 10.2, 10.4 *(inventory columns)*, 10.12, 10.16 *(page,
        # sort)*` recorded `page, sort` on R10.1 and `inventory columns` on R10.16 -- a
        # cross product, not an attribution. Splitting on `·` alone wasn't enough either:
        # the ids side is comma-separated and `*(page, sort)*` contains a comma, so the
        # token boundary has to respect paren depth, which is what split_tokens does. The
        # first patch tried to do that with a regex over the whole ids string and matched
        # `4` where it meant `10.4` -- `\d+` gave back `10` at the first position that let
        # the rest of the pattern match, because the optional `(?:\.\d+[a-z])?` demands a
        # letter after the minor number and `10.4 ` has a space there.
        for tok in split_tokens(ids):
            m = re.search(r"\(([^)]*)\)", tok)
            note = m.group(1).strip() if m else ""
            if note:
                # Recorded before the expand() loop, so a note on a token that expands
                # to zero ids is still counted as present in the text -- which is the
                # only way the coverage check below can see it.
                text_notes.add(note)
                for fid in expand(tok):
                    annotated.setdefault(fid, set()).add(note)
        for note in (n.strip() for n in re.findall(r"\(([^)]*)\)", phase)):
            if not note: continue
            text_notes.add(note)
            for fid in expand(ids):
                annotated.setdefault(fid, set()).add(note)
print("annotated ids (generated):", ", ".join(f"{k}:{'/'.join(sorted(v))}" for k, v in sorted(annotated.items())) or "none")
gen_notes = {n for v in annotated.values() for n in v}
unbound_notes = sorted(text_notes - gen_notes)
print(f"annotation coverage: {len(text_notes)} notes in \u00a77.3 \u00b7 {len(gen_notes)} "
      f"attached to an id \u00b7 {len(unbound_notes)} unbound")
dump(unbound_notes, lambda n: f"  ({n}) is written in \u00a77.3 and attached to no id -- the parser cannot read it", cap=10)
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
dump(cite_bad, lambda f, n, t: f"  {f}:{n}: {t}", cap=40)
dump(id_orphans, lambda k: f"  orphan exemption: {k} — {EXEMPT[k]}")

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
# unresolvable path is reported but not failed — external file, deleted file and typo are
# not separable without a human. An ambiguous one (2+ matches) fails: len(r) >= 2 proves the
# name exists in the tree, so it cannot be external, and the defect is the cite itself. That is
# the same rule the orphan-exemption check states at :410-413, applied to the case it left out
# -- A3's S117, filed against e2c042b's `statements.go:487`. docs/research/ is excluded from this half
# entirely: those notes cite the upstream projects' own files (postgres_exporter's
# README.md:310-321), which this repo's file list cannot resolve.
PATH_SKIP_DIRS = ("docs/research/",)
# Cited paths that are known-not-to-resolve, keyed on the (citing file, cited path) pair with
# the reason, mirroring EXEMPT above. Keyed on the pair, not the bare path string, and applied
# only while the path fails to resolve -- which since the ambiguity gate means 0 matches OR
# 2+ matches, not just 0: a quoted exhibit of a fixed ambiguous cite is unresolvable by
# design and needs the same keyed reason, or the gate fails on the row that documents the fix
# (A3's S116). If the repo ever gains a real references/components.md, IMPLEMENTATION.md's
# cite of it becomes checkable and gets checked, rather than being silently exempted forever.
# references/components.md, IMPLEMENTATION.md's cite of it becomes checkable and gets checked,
# rather than being silently exempted forever. Without self-expiry the table is a standing
# warning that outlives its own reason — which is how real findings get skipped.
PATH_EXEMPT = {
    ("docs/reviews/finding-index.md", "statements.go"):
        "S116's exhibit: the row quotes the bad cite verbatim to document the fix, and that "
        "basename matches internal/collector/statements.go and internal/store/statements.go, "
        "so the quoted form is unresolvable by design while the live reference at "
        "IMPLEMENTATION.md:1066 is now spelled and checked",
    ("IMPLEMENTATION.md", "references/components.md"):
        "the dataviz skill bundle's own file; the bundle is cited by absolute path + sha256 in "
        "the same cell (D12), so the relative form there refers outside this repo",
}
# The cited-path pattern, consumed by CITE_A and CITE_B below and by the link-label search in
# check_links; the dot-less names come from CITE_NAMES above, the same table that gates which
# files cited_tokens and finding_cites scan. Before this, P required a dot-extension, so
# `Makefile:58` was not citable -- registered for scanning but not for citing -- and the 8
# such citations in IMPLEMENTATION.md, requirements.md and docs/reviews/finding-index.md
# counted as nothing while the rows that contain them reported as resolving: A3's S113, whose
# block cited :328/:332/:486, the pre-edit positions of the three consumers named above.
P = ("(?:" + r"[A-Za-z0-9_./-]+\.(?:md|go|ya?ml|tsx?|json|sh|py|js|css)"
     + "|" + "|".join(re.escape(n) for n in sorted(CITE_NAMES)) + ")")
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
    checked, bad, unresolved, ambiguous, exempt = 0, [], [], [], 0
    pairs = set()                                  # every (citing file, cited path) it found
    tset = set(tracked)
    for rel, n, kind, path, s, e, head in path_citations(root, tracked):
        pairs.add((rel, path))
        r = resolve_path(tracked, tset, rel, path)
        if (rel, path) in PATH_EXEMPT and len(r) != 1:
            exempt += 1; continue              # exempt only while it still fails to resolve
        if len(r) == 0:
            unresolved.append((rel, n, kind, path, 0)); continue
        if len(r) > 1:                       # the cite under-specified a path the repo has 2+ of
            ambiguous.append((rel, n, kind, path, len(r))); continue
        checked += 1
        L = lines_of(root, r[0])
        if e > len(L):
            bad.append((rel, n, kind, f"{path}:{s}-{e} — {r[0]} has {len(L)} lines")); continue
        if not L[s - 1].strip():              # A3's S118: a blank line cannot be what a cite names
            bad.append((rel, n, kind, f"{path}:{s} is blank")); continue
        if kind == "B":                        # the durable form asserts the heading
            actual = re.sub(r"^#+\s*", "", L[s - 1]).strip()
            if not L[s - 1].startswith("#"):
                bad.append((rel, n, kind, f"{path}:{s} is not a heading ({actual[:48]!r})"))
            elif actual != head.strip():
                bad.append((rel, n, kind, f"quotes {head!r}, {path}:{s} reads {actual!r}"))
    return checked, bad, unresolved, ambiguous, exempt, pairs

# A4's L1, applied to this half too: gate on the file set, not on the per-half count. A repo
# with markdown but zero path citations is legal, and "file discovery broke" is false when
# discovery worked and simply found nothing to check.
scanned_md = sum(1 for rel in files_all
                 if rel.endswith(".md") and not rel.startswith(PATH_SKIP_DIRS))
if not scanned_md:
    print("GUARD: no scannable markdown — file discovery broke, not a pass")
    sys.exit(2)
checked_path, path_bad, unresolved, ambiguous, exempt_path, cited_pairs = check_paths(root, files_all)
# A PATH_EXEMPT key that matches no citation is a dead exemption — the table is read at one
# site, so an orphan key is invisible and the check it performs has quietly stopped running.
# Unlike an external path (reported, not failed: external/deleted/typo aren't separable), an
# orphan key is unambiguous, so it fails.
path_orphans = sorted(set(PATH_EXEMPT) - cited_pairs)
print(f"path citations: {checked_path} resolved · {len(path_bad)} wrong · "
      f"{len(unresolved)} unresolvable (reported, not failed) · "
      f"{len(ambiguous)} ambiguous (failed) · {exempt_path} exempt · "
      f"{len(path_orphans)} orphan exemptions")
dump(path_bad, lambda f, n, kind, msg: f"  {f}:{n} [{kind}] {msg}")
dump(unresolved, lambda f, n, kind, path, n_alt: f"  {f}:{n} [{kind}] unresolved: {path} ({n_alt} match{'es' if n_alt != 1 else ''})", cap=10)
dump(ambiguous, lambda f, n, kind, path, n_alt: f"  {f}:{n} [{kind}] ambiguous: {path} ({n_alt} matches)", cap=10)
dump(path_orphans, lambda k: f"  orphan path exemption: {k[0]} -> {k[1]} — {PATH_EXEMPT[k]}")

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
dump(status_bad, lambda n, rid, cell: f"  IMPLEMENTATION.md:{n} {rid}: {cell!r}")

# ---------------------------------------------------------------------------
# Table width: every row in a markdown table must carry its header's cell count.
#
# check_status_enum above reads cells[0] and cells[-1] and nothing in between, so a row with
# an unescaped pipe in its prose -- or, the way A3 shipped IMPLEMENTATION.md:710 at 5cb8c24,
# two literal `||` from concatenating three cell fragments that each carried their own pipes --
# passes the enum check and renders as six columns under a four-column header. The coordinator
# filed it; the gate could not have. The only length check in this file was `if len(cells) < 5`
# in the §3 phase-table parser -- a parse guard for one table, not a rule about tables.
#
# Tables are found structurally: a `|...|` line whose next line is a `|---|` separator is a
# header, and every `|...|` line after it, up to the first non-table line, is its row. An
# earlier attempt keyed on a vocabulary of header texts (`Req`, `#`, `Phase`, ...) and produced
# 16 false positives on tables whose header name it did not know -- the same failure as a
# linter that only checks the files it has heard of. Widths come from split_row_esc, so
# `--auth=none\|token` counts as one cell, which is how it renders.
#
# WIDTH_EXEMPT holds the pre-existing rows the first run found, so the check gates on new
# damage without turning this commit into a rewrite of prose it did not write. Entries are
# (file, line): a fix deletes its line, and so does any edit that moves the row -- an exemption
# that expires when the line moves re-reports the row instead of silently passing it, which is
# the direction worth failing in. The eight §11 rows at requirements.md:843-850 are missing a
# column outright (the Reviewer cell absorbed the Scope cell's `-> docs/reviews/...` suffix);
# :841, the ninth data row of that table, has five cells against its header's five and needs no
# exemption -- the count here said nine until A5 measured it, which is the cost of writing a
# number in a comment instead of deriving it from the set below. IMPLEMENTATION.md's D12 row was
# the ninth entry: three pipes whose role -- separator or text -- was a judgement, not a
# mechanical fix. The judgement is now made. All three are shell pipes inside one inline-code
# span (`find <bundle> -type f \| sort \| xargs shasum -a 256 \| shasum -a 256`), so they are
# text and the row escapes them, which is the outcome this comment's own rule prefers ("a fix
# deletes its line"). Measured 2026-11-24: the row sat at :867 at `aee9046` and at :1016 in the
# tree that added §3.12, and the line-keyed exemption expired on the move -- escaping the pipes
# ends that churn, where re-pointing the number would have restarted it on the next edit above
# the row.
WIDTH_EXEMPT = {
    ("requirements.md", 843), ("requirements.md", 844), ("requirements.md", 845),
    ("requirements.md", 846), ("requirements.md", 847), ("requirements.md", 848),
    ("requirements.md", 849), ("requirements.md", 850),
}
# WIDTH_DOC is the whole scan set -- four named files, not "the docs" generally. The three
# round-1 documents (docs/reviews/round-1-architecture.md, round-1-product.md and
# v0.3.0-architecture-review.md) are deliberately excluded: they are point-in-time review
# documents whose tables are prose-shaped the way the exempt §11 rows are, and their content
# is frozen at the round they name. The gate's line reads as a statement about tables in
# general and this tuple is the only thing that makes it not one, so the boundary lives here
# rather than only in the message that shipped it.
WIDTH_DOC = ("IMPLEMENTATION.md", "requirements.md",
             "docs/reviews/finding-index.md", "docs/agents.md")
SEP_ROW_RE = re.compile(r"^\|[\s:|-]+\|$")

def check_table_widths(root):
    # (tables, rows, bad) -- bad rows are those whose width != their header's.
    tables = rows = 0
    bad = []
    for rel in WIDTH_DOC:
        lines = lines_of(root, rel)
        i = 0
        while i < len(lines):
            line = lines[i].rstrip()
            nxt = lines[i + 1].strip() if i + 1 < len(lines) else ""
            if line.startswith("|") and SEP_ROW_RE.match(nxt):
                tables += 1
                width = len(split_row_esc(line))
                j = i + 2
                while j < len(lines):
                    row = lines[j].rstrip()
                    if not row.startswith("|") or SEP_ROW_RE.match(row.strip()):
                        break
                    rows += 1
                    n = len(split_row_esc(row))
                    if n != width and (rel, j + 1) not in WIDTH_EXEMPT:
                        bad.append((rel, j + 1, width, n,
                                    split_row_esc(row)[0][:24]))
                    j += 1
                i = j
            else:
                i += 1
    return tables, rows, bad

# ---------------------------------------------------------------------------
# id-vs-cite: a requirement id written next to a `path:N` cite has to be the
# requirement at line N. check_paths asserts a cited line exists (and, for a
# markdown heading target, that it is that heading) -- never that the line is the
# row the prose is talking about, so an off-by-one row cite is green. First run
# found one: IMPLEMENTATION.md:729 read "`scripts/validate-env.sh` is R16.5
# (`requirements.md:521`)" and :521 is R16.4's row; R16.5 is at :522. The check
# ships with its own proof -- the tree one commit earlier makes it print 1 and
# exit 1 -- which is the standard this file holds itself to: a checker that
# cannot fail is a comment.
#
# Read the printed line as two denominators, not one coverage number. The
# adjacency count is a regression gate: it grows only when someone writes a *new*
# id+cite sentence, so a small value (1 at 460bf28) is not a weak check, and a
# large one is not a strong one -- it counts sentences written in a shape the
# regex recognises, and prose that cites a row without the connector ("see the
# table", "per requirements.md") is outside it by design. The row count is the
# census of every tracked .md file -- 53 files and 279 requirement rows at
# 80a09eb -- and it is NOT the four files in WIDTH_DOC. 8cdf27a scoped this
# sentence to WIDTH_DOC, which described a number this function never computes:
# check_table_widths iterates WIDTH_DOC (:563) and check_id_cites iterates
# tracked_files filtered to .md (:664), so the printed 279 is 268 from
# WIDTH_DOC's two .md files plus 11 from docs/reviews/round-1-product.md -- a
# file the WIDTH_DOC comment names as deliberately excluded from the width scan.
# The exclusion does not carry over, and the reason is the two checks' different
# failure modes: width-checking a frozen document can only produce exemptions,
# because its tables are prose-shaped the way the exempt §11 rows are, while
# cite-checking it can still find a live defect, because a wrong pairing is not
# frozen by the document being point-in-time. Four files is right for widths and
# every tracked .md is right for cites; the numbers each section prints are
# numbers about its own scan set and not about markdown in general.
#
# The two forms are not equally strong per line. Form 2 (the table row) is the
# stronger: a requirement row's first cell *is* the id, so the check needs no
# syntax to know what the cite should point at, and it validates 279 pairings for
# the cost of parsing rows the width check already parses. Form 1 (adjacency)
# exists only to cover prose outside a table, where the connector words are the
# only signal of what a cite is being claimed to be -- which is why its pattern is
# narrow and its count small, and why widening it further buys false positives
# before it buys coverage.
IDCITE_RX = re.compile(
    # "R16.5 (`requirements.md:521`)", "R9.8 (requirements.md:401)", and the prose
    # forms "the R9.8 row at `requirements.md:401`" / "this R2.2 cell, in
    # `requirements.md:330`" -- the gap is capped at one short connector so a random
    # id and a random cite in the same sentence do not pair up.
    r"(?<![\w.])(?P<id>R\d+\.\d+[a-z]?)"
    r"(?:\s{0,2}|\s+(?:the|this|that|its)\s+(?:row|cell|entry|line|section|half|step|table|spec)\s+at\s+)"
    r"(?:\()?"
    r"(?:`(?P<p1>[\w./-]+\.md):(?P<n1>\d+)`"
    r"|\((?P<p2>[\w./-]+\.md):(?P<n2>\d+)\))")
IDCITE_ROW = re.compile(r"^\|\s*\*?\*?(R\d+\.\d+[a-z]?)\*?\*?\s*\|")

QUOTED_SPAN = re.compile(r'\"[^\"]*\"|\u201c[^\u201d]*\u201d')


def dequote(line):
    # Blank every quoted span before pairing ids with cites. A row that *documents*
    # a wrong cite has to quote it, and quoting is not asserting: S55's own row
    # quotes "R16.5 (`requirements.md:521`)" -- the bad pairing it exists to record --
    # and the check flagged the quotation as a second instance of the defect.
    # Blank the quotations first and the check asks the right question: does the
    # prose *assert* a pairing, not does it contain the string.
    return QUOTED_SPAN.sub(lambda m: " " * len(m.group(0)), line)


def check_id_cites(root):
    # (checked, bad) -- bad is a named id whose cited line is another requirement's
    # row. Two forms, both needing a requirement-table row on the end to compare
    # against (a .go/.yml line has no id column, so .md targets only):
    #   1. adjacency -- "R16.5 (`requirements.md:521`)": the id written next to the
    #      cite must be the requirement at that line.
    #   2. table row -- a row whose first cell is an id (or "R9.2 / R9.3") may only
    #      cite lines belonging to one of its own ids; this catches a row citing a
    #      neighbouring requirement without naming it, which form 1 misses.
    checked = 0
    rows = 0
    bad = []
    cache = {}

    def target(path):
        if path not in cache:
            cache[path] = lines_of(root, path)
        return cache[path]

    for rel in tracked_files(root):
        if not rel.endswith(".md"):
            continue
        for i, line in enumerate(lines_of(root, rel), 1):
            line = dequote(line)
            # form 1: id adjacent to a cite
            for m in IDCITE_RX.finditer(line):
                idc = m.group("id")
                path = m.group("p1") or m.group("p2")
                n = int(m.group("n1") or m.group("n2"))
                checked += 1
                tgt = target(path)
                if n > len(tgt):
                    continue  # out-of-bounds is check_paths' verdict, not ours
                r = IDCITE_ROW.match(tgt[n - 1])
                if r and r.group(1) != idc:
                    bad.append((rel, i, idc, path, n, r.group(1)))
            # form 2: a requirement row's own ids vs every .md cite in the row
            if not line.startswith("|"):
                continue
            seen_pairs = {(rel, m.group("p1") or m.group("p2"),
                           int(m.group("n1") or m.group("n2")))
                          for m in IDCITE_RX.finditer(line)}
            cells = split_row_esc(line)
            if not cells or not re.match(r"^\*?\*?R\d+\.\d+[a-z]?\*?\*?$",
                                         cells[0].strip()):
                continue
            own = set(re.findall(r"R\d+\.\d+[a-z]?", cells[0]))
            rows += 1
            for m in re.finditer(r"`([\w./-]+\.md):(\d+)`", line):
                path, n = m.group(1), int(m.group(2))
                if (rel, path, n) in seen_pairs:
                    continue  # already counted by form 1
                tgt = target(path)
                if n > len(tgt):
                    continue
                r = IDCITE_ROW.match(tgt[n - 1])
                if r and r.group(1) not in own:
                    bad.append((rel, i, ",".join(sorted(own)), path, n, r.group(1)))
    return checked, rows, bad

width_tables, width_rows, width_bad = check_table_widths(root)
print(f"table widths: {width_tables} tables \u00b7 {width_rows} rows \u00b7 "
      f"{len(width_bad)} mismatched \u00b7 {len(WIDTH_EXEMPT)} exempt")
dump(width_bad, lambda rel, n, w, n2, first: f"  {rel}:{n}: {n2} cells under a {w}-cell header -- first cell {first!r}")

idc_checked, idc_rows, idc_bad = check_id_cites(root)
print(f"id-vs-cite: {idc_checked} id+path:N adjacencies \u00b7 {idc_rows} requirement rows' "
      f"own cites \u00b7 {len(idc_bad)} naming a different requirement's row")
dump(idc_bad, lambda rel, n, idc, path, ln, actual: f"  {rel}:{n}: names {idc} but {path}:{ln} is {actual}'s row")

# ---------------------------------------------------------------------------
# §3's phase table and §1's milestone table: the phase-level status, derived.
#
# check_status_enum above gates the 77 R-id rows and nothing else -- it is keyed on
# STATUS_ROW_ID_RE, and neither `**Phase 0**` nor `Phases 2–9` is an R-id. The two
# tables that carry *phase* status were therefore unvalidated content in a file this
# script claims to gate. `0b440df` moved README's status line and left §3's Phase 9
# row at ⬜; `fe61929` moved §3's row and left §1 reading `Phases 2–9 · ⬜ not
# started`; `6af534c` fixed that cell by hand, which is the state that rots -- a
# corrected cell with no generator. docs/agents.md §5 item 10 is the rule; this is
# the generator A3's ruling asked for, and the §1 cells ship in the same commit as
# the check that pins them, because a check that ships red and a cell that ships
# ungenerated are the same failure wearing different clothes.
#
# Three tables, three vocabularies: §3.1's R-id rows use the word enum above, §3's
# phase table uses the bare glyph legend at :103, §1's milestone table uses glyph +
# label. The legend is the single source for the last two -- parsed, never hardcoded,
# so editing it moves both vocabularies the way STATUS_ENUM moves the first. It is
# split on ` · ` rather than matched as a character class because `⚠️` is two code
# points (0x26a0 0xfe0f) where the other three are one: a class like [⬜🔄✅⚠️]
# fullmatches the three singletons and rejects every ⚠️, so Phase 0 -- the row every
# rule about it is written for -- would fail at tokenisation, before any of them ran.

LEGEND_RE = re.compile(r"^Status legend:\s*(.+)$")
PHASE_TABLE_RE = re.compile(r"^\|\s*Phase\s*\|\s*Scope\s*\|")
MILESTONE_TABLE_RE = re.compile(r"^\|\s*Milestone\s*\|\s*Status\s*\|")
PHASE_TOKEN_RE = re.compile(r"^\*\*(\d+[ab]?)\*\*$")
MILESTONE_PHASE_RE = re.compile(r"^\*\*Phase (\d+[ab]?)\*\*")
MILESTONE_RANGE_RE = re.compile(r"^Phases (\d+)[–\-](\d+)$")
MILESTONE_RELEASE_RE = re.compile(r"^v\d+\.\d+\.\d+(?:-[A-Za-z]+)? release$")
SEPARATOR_RE = re.compile(r"^\|[\s:|-]+\|$")


def parse_legend(root):
    """{glyph: label} from IMPLEMENTATION.md's legend line -- the one place the
    phase-level vocabulary is written. Split on ` · `, not a character class, so
    multi-codepoint glyphs survive intact."""
    for line in lines_of(root, "IMPLEMENTATION.md"):
        m = LEGEND_RE.match(line.strip())
        if not m:
            continue
        out = {}
        for entry in m.group(1).split(" · "):
            glyph, _, label = entry.strip().partition(" ")
            if glyph:
                out[glyph] = label.strip()
        return out
    return {}


def table_rows(root, header_re):
    """Cells of every row under the first table whose header matches header_re."""
    lines = lines_of(root, "IMPLEMENTATION.md")
    for i, line in enumerate(lines):
        if not header_re.match(line.strip()):
            continue
        j = i + 1
        if j < len(lines) and SEPARATOR_RE.match(lines[j].strip()):
            j += 1                                  # skip the |---|---| row
        rows = []
        while j < len(lines) and lines[j].strip().startswith("|"):
            rows.append(split_row_esc(lines[j].strip()))
            j += 1
        return rows
    return []


def rollup(glyphs, by_label):
    """Phase-level glyph from the glyphs of that phase's rows: all not-started ->
    not-started, all done (either flavour) -> done, anything else -> in-progress.
    Needs the legend's *labels*, not just its glyphs -- the rule is about which glyph
    means 'done', and that is exactly what the legend says. A phase with one ✅ row
    and one 🔄 row resolves to 🔄, which is how Phase 1 reads."""
    ns, ip = by_label["not started"], by_label["in progress"]
    done = (by_label["done"], by_label["done with deviations"])
    if all(g == ns for g in glyphs):
        return ns
    if all(g in done for g in glyphs):
        return by_label["done"]
    return ip


def check_phase_and_milestone_status(root):
    """§3's phase table -> the phase-level status; §1's cells must equal what it says.

    Returns (phase_rows, phase_bad, ms_rows, ms_bad): §3's row count with its
    malformed status cells, and §1's in-scope row count with the cells that disagree
    with the derived value. A3's structure ruling is that this is its own function
    with its own printed line rather than an extension of check_status_enum -- that
    one is keyed on STATUS_ROW_ID_RE and speaks the word enum, and folding glyph rows
    into it would make the next person who needs to unblock CI revert a function that
    also gates the 77 R-id cells.
    """
    by_glyph = parse_legend(root)
    if not by_glyph:
        print("GUARD: phase status check parsed 0 legend glyphs -- the legend line "
              "moved, not a pass")
        sys.exit(2)
    by_label = {v: k for k, v in by_glyph.items()}
    DONE_LABELS = (by_label["done"], by_label["done with deviations"])

    def cell_for(g):
        """§1's cell format, derived: the glyph plus the exact label the legend maps
        it to. Pinned from one place rather than typed per row, which is what lets
        `⬜ not started` (two words) and `✅ done` (one) both be correct without a
        word-count rule -- A3's P3, and the reason the existing STATUS_CELL_RE is
        not a template here: it pins `**word** (A1)` forms, not glyph+label pairs."""
        return f"{g} {by_glyph[g]}"

    def is_done(g):
        return by_glyph[g] in DONE_LABELS
    missing = [w for w in ("not started", "in progress", "done", "done with deviations")
               if w not in by_label]
    if missing:
        print(f"GUARD: legend has no glyph for {missing} -- not a pass")
        sys.exit(2)

    # §3's status cell is pinned, not scraped. Two of its twelve rows are not a bare
    # glyph -- `**4**` is `🔄 (§3.9 — one requirement transferred, recorded there)` --
    # and taking cell[4] verbatim makes Phase 4's derived value that whole string,
    # which can never equal §1's `🔄 in progress`: the check would ship red on a row
    # that is correct. glyph = the cell's first whitespace-delimited token, and the
    # rest of the cell must be a parenthesised note or the row is reported.
    glyph_alt = "|".join(re.escape(g) for g in sorted(by_glyph, key=len, reverse=True))
    phase_status_re = re.compile(r"^(%s)(?: \(.*\))?$" % glyph_alt, re.S)

    phase_rows, phase_bad, rows_by_token = 0, [], {}
    for cells in table_rows(root, PHASE_TABLE_RE):
        m = PHASE_TOKEN_RE.match(cells[0])
        if not m:
            continue
        phase_rows += 1
        if len(cells) < 5:
            phase_bad.append((cells[0], "<row has %d cells>" % len(cells)))
            continue
        sm = phase_status_re.match(cells[4])
        if not sm:
            phase_bad.append((cells[0], cells[4][:60]))
            continue
        rows_by_token[m.group(1)] = sm.group(1)

    ms_rows, ms_bad = 0, []
    for cells in table_rows(root, MILESTONE_TABLE_RE):
        if len(cells) < 2:
            continue
        label, cell = cells[0], cells[1]
        expected = None
        m = MILESTONE_PHASE_RE.match(label)
        if m:
            # Single-phase row: compare to that exact §3 token, not to the phase
            # number's rollup -- §1 has a row per token, and the Phase 1a/1b pair is
            # the pair this catches (a rollup would hide 1a's 🔄 behind Phase 1).
            glyph = rows_by_token.get(m.group(1))
            expected = cell_for(glyph) if glyph else None
        else:
            m = MILESTONE_RANGE_RE.match(label)
            if m:
                lo, hi = int(m.group(1)), int(m.group(2))
                nums = sorted({int(re.match(r"\d+", t).group(0))
                               for t in rows_by_token
                               if lo <= int(re.match(r"\d+", t).group(0)) <= hi})
                if nums:
                    glyphs = [rollup([rows_by_token[t] for t in rows_by_token
                                      if int(re.match(r"\d+", t).group(0)) == n],
                                     by_label) for n in nums]
                    g = rollup(glyphs, by_label)
                    n_open = sum(1 for x in glyphs if x != by_label["not started"])
                    expected = "%s (%d of %d)" % (cell_for(g), n_open, len(nums))
            elif MILESTONE_RELEASE_RE.match(label):
                # The release ships at the end of Phase 9, so it is not started until
                # Phase 9 is done. The other ten §1 rows (the two Research rows, the
                # three requirements.md versions, the two reviews, this file, and the
                # two single-phase rows counted above) have no §3 counterpart.
                g = rows_by_token.get("9")
                if g is not None:
                    expected = cell_for(g) if is_done(g) \
                        else cell_for(by_label["not started"])
        if expected is None:
            continue
        ms_rows += 1
        if cell != expected:
            ms_bad.append((label, cell, expected))
    return phase_rows, phase_bad, ms_rows, ms_bad


# --- §3's validation-entry headings vs §3's phase table ----------------------------------
#
# R14.2 makes a validation entry a phase's artifact and R14.9 splits it per owner, but
# nothing tied the entries to the table that is supposed to point at them. §3's phase table
# has a `Validation entry` column and the only rule ever written against its cells was the
# width rule -- so a cell could name any section number, and on the first run this rule was
# written, one did: Phase 2's cell named §3.10, which is `Task-level breakdown (Phase 2,
# expanded)`, while Phase 2's validation entry is §3.12. The cell was written when no Phase
# 2 entry existed and pointed at the nearest thing; `1be97e2` wrote the entry and left the
# cell behind (A3's S79). The coordinator's ask was exactly this -- "if §3.12's heading is
# the canonical address for the Phase 2 entry, that's the kind of fact the checker should be
# able to answer in one grep" -- and the answer is one grep now, plus the failure it needed.
#
# Two rules, one per direction:
#   dangling -- every §3.N written in a Validation entry cell names an existing `### 3.N`
#               heading of any kind (entry, requirement table, or task breakdown), so a
#               renumbered or deleted section can't be cited by a row that survived it;
#   orphan   -- every `### 3.N Phase X validation entry (R14.2)` heading is named by at
#               least one phase-table row, and every row that names it is a row for phase X.
#               The shared-entry case is R14.9's: a heading that says `Phase 1` is named by
#               the `1a` and `1b` rows, so a row token starting with the heading's phase
#               token counts as that phase, not as a mismatch.
# Both directions are needed and neither subsumes the other: the dangling rule is silent on
# §3.10-vs-§3.12 (both headings exist), and the orphan rule is silent on a cell citing a
# heading that was deleted. Zero headings parsed is a GUARD exit, not a pass.
H3_ANY_RE = re.compile(r"^###\s+(\d+\.\d+)\s+(.+?)\s*$")
H3_VENTRY_RE = re.compile(
    r"^###\s+(\d+\.\d+)\s+Phase\s+(\d+[ab]?)\s+validation entry \(R14\.2\)\s*$")
SEC_REF_RE = re.compile(r"\u00a7(\d+\.\d+)")


def check_validation_entries(root):
    """(headings, entries, rows, dangling, orphans).

    headings  -- every `### N.N <title>` in IMPLEMENTATION.md, {section: title}
    entries   -- the subset that are R14.2 validation entries, {section: phase token}
    rows      -- phase-table rows with a full six cells, counted
    dangling  -- (phase token, §ref) for a cell citing a section with no heading
    orphans   -- (section, phase, why) for an entry its phase row fails to name
    """
    headings, entries = {}, {}
    for line in lines_of(root, "IMPLEMENTATION.md"):
        m = H3_ANY_RE.match(line)
        if m:
            headings[m.group(1)] = m.group(2)
        v = H3_VENTRY_RE.match(line)
        if v:
            entries[v.group(1)] = v.group(2)
    if not headings:
        print("GUARD: 0 `### N.N` headings parsed in IMPLEMENTATION.md -- the heading shape "
              "moved, not a pass")
        sys.exit(2)
    if not entries:
        print("GUARD: 0 `### N.N Phase X validation entry (R14.2)` headings parsed -- R14.2 "
              "requires them, so an empty set is the regex breaking, not a pass")
        sys.exit(2)

    # A cell's *address* is its first §ref; everything after it is annotation. Phase 2's
    # cell cites §3.11 (Phase 3's entry) in prose and Phase 3's cites §3.12 the same way,
    # so treating every §ref in the cell as a claim about the entry produced two false
    # orphans on a file with nothing wrong with them. The address is the cell's first ref;
    # the existence rule still applies to all of them.
    primary, rows, dangling = {}, 0, []
    for cells in table_rows(root, PHASE_TABLE_RE):
        m = PHASE_TOKEN_RE.match(cells[0])
        if not m or len(cells) < 6:
            continue
        rows += 1
        refs = SEC_REF_RE.findall(cells[5])
        if refs:
            primary.setdefault(refs[0], set()).add(m.group(1))
        for ref in refs:
            if ref not in headings:
                dangling.append((m.group(1), ref))

    orphans = []
    for sec, phase in sorted(entries.items()):
        named_by = primary.get(sec, set())
        valid = {t for t in named_by if t == phase or t.startswith(phase)}
        if not named_by:
            orphans.append((sec, phase, "no phase-table row names it as its Validation entry"))
        for t in sorted(named_by - valid):
            orphans.append((sec, phase,
                            "named as the Validation entry of row **%s**, which is phase %s"
                            % (t, phase)))
    return len(headings), len(entries), rows, dangling, orphans


phase_rows, phase_bad, ms_rows, ms_bad = check_phase_and_milestone_status(root)
print(f"phase cells: {phase_rows} §3 phase rows · {len(phase_bad)} malformed status "
      f"cells · {ms_rows} §1 phase rows · {len(ms_bad)} disagreeing with §3")
dump(ms_bad, lambda label, cell, expected: f"  IMPLEMENTATION.md §1 {label}: {cell!r}, §3 derives {expected!r}")
dump(phase_bad, lambda token, cell: f"  IMPLEMENTATION.md §3 **{token}**: status cell {cell!r} is not a glyph")

ve_headings, ve_entries, ve_rows, ve_dangling, ve_orphans = check_validation_entries(root)
print(f"validation entries: {ve_headings} `### N.N` headings \u00b7 {ve_entries} of them "
      f"R14.2 entries \u00b7 {ve_rows} phase rows \u00b7 {len(ve_dangling)} dangling "
      f"\u00a7 refs \u00b7 {len(ve_orphans)} entries not named by their phase row")
dump(ve_dangling, lambda token, ref: f"  IMPLEMENTATION.md \u00a73 row **{token}**: \u00a7{ref} has no heading")
dump(ve_orphans, lambda sec, phase, why: f"  IMPLEMENTATION.md \u00a7{sec} (Phase {phase} entry): {why}")



checked_links, links_bad, labels_bad = check_links(root, files_all)
if not scanned_md:
    print("GUARD: link check scanned 0 markdown files — file discovery broke, not a pass")
    sys.exit(2)
print(f"markdown links: {checked_links} checked \u00b7 {len(links_bad)} broken \u00b7 "
      f"{len(labels_bad)} label mismatches")
dump(links_bad, lambda f, n, tgt: f"  {f}:{n} -> {tgt}")
dump(labels_bad, lambda f, n, text, path: f"  {f}:{n} label {text!r} names {path.rsplit('/', 1)[-1]}, target is " f"{path!r}")

# --- finding ids (docs/reviews/finding-index.md, the S-ledger) -----------------------
# "A3's S26" is cited from internal/store/statements.go, and before the ledger existed the id
# was defined only inside a commit-message body -- a different repository object, so a reader
# could not check what the finding said, what fixed it, or whether the id was real at all. The
# ledger is now the definition and this is the gate. One direction fails the build: a cite
# naming an id with no row is a typo or a renumbering, which is what the R-id check above
# exists to catch, and the S-series has the same failure surface with none of the coverage.
# The other direction -- a row nothing cites -- is printed, not failed: S33 and S36 are
# findings that exist without being cited yet, and a hard rule there would force the table to
# invent citations to satisfy its own tool. The printed count is what stops rows quietly
# accruing that nobody asks about.
FINDING_DOC = "docs/reviews/finding-index.md"
FINDING_ROW_RE = re.compile(r"^\|\s*(S\d{1,3})\s*\|")
FINDING_CITE_RE = re.compile(r"\bA3's\s+(S\d{1,3})\b")
# Wider than CITE_EXTS on purpose. The R-id check scans .md/.yml/.go because an R-id in a
# .tsx file has to resolve against §4, and adding .tsx there would change what that check
# accepts. A finding id only has to resolve against the ledger, which is cheap and closed, so
# the six S-cites living in web/src/**.tsx -- S21, S22, S23 -- are in scope here.
FINDING_EXTS = CITE_EXTS | {".tsx", ".ts"}

def finding_ledger():
    p = root / FINDING_DOC
    if not p.is_file():
        print(f"GUARD: {FINDING_DOC} missing -- it is the definition of every \"A3's S<NN>\" "
              f"cite in the tree; deleting it is not a pass")
        sys.exit(2)
    rows = {m.group(1) for line in p.read_text(errors="replace").splitlines()
            for m in [FINDING_ROW_RE.match(line)] if m}
    if not rows:
        print(f"GUARD: {FINDING_DOC} parsed 0 finding ids -- the table shape changed, not a pass")
        sys.exit(2)
    return rows

def finding_cites():
    out = []
    for rel in files_all:
        p = root / rel
        if not p.is_file(): continue
        if p.suffix not in FINDING_EXTS and p.name not in CITE_NAMES: continue
        for n, line in enumerate(p.read_text(errors="replace").splitlines(), 1):
            for m in FINDING_CITE_RE.finditer(line):
                out.append((rel, n, m.group(1)))
    return out

ledger_ids = finding_ledger()
fcites = finding_cites()
cited_f = {t for _, _, t in fcites}
finding_bad = sorted({(f, n, t) for f, n, t in fcites if t not in ledger_ids})
uncited_f = sorted(ledger_ids - cited_f, key=lambda s: int(s[1:]))
print(f"finding ids: {len(ledger_ids)} in {FINDING_DOC} \u00b7 {len(cited_f)} cited across "
      f"{len({f for f, _, _ in fcites})} files \u00b7 {len(finding_bad)} cites with no row \u00b7 "
      f"{len(uncited_f)} rows never cited")
dump(finding_bad, lambda f, n, t: f"  {f}:{n}: {t} is cited but has no row in {FINDING_DOC}")
dump(uncited_f, lambda t: f"  {t} has a row and no cite -- its status column says whether that is expected")

# The header's declared counts are now checked against the parsed table instead of asserted
# in prose. The doc used to claim the number was "the same number
# `scripts/check-parity-matrix.py` prints as `finding ids:`, so a disagreement between the two
# is a stale header rather than a missing row" -- and nothing compared them: with the table at
# 85 rows, editing the sentence to `The table holds 84 rows` printed `finding ids: 85` and
# exited 0 (A3's S91). The sentence described a check rather than being one, which is S80's
# class in the header's own voice. Both declared forms are parsed now: the prose count, and the
# `N ids, S1-SNN` cell of the id-form table, whose range end is checked against the maximum
# parsed id as well as against the count.
HDR_PROSE_RE = re.compile(r"The table holds (\d+) rows")
HDR_FORM_RE = re.compile(r"^\| `A3's S<NN>` \| current \| this table \| (\d+) ids, S1[\u2013-]S(\d+) \|")
_ledger_text = (root / FINDING_DOC).read_text(errors="replace")
_max_id = max(int(x[1:]) for x in ledger_ids)
header_bad = []
_m = HDR_PROSE_RE.search(_ledger_text)
if not _m:
    header_bad.append("header prose: no `The table holds N rows` sentence -- the coverage "
                      "claim was deleted rather than updated")
elif int(_m.group(1)) != len(ledger_ids):
    header_bad.append(f"header prose: declares {_m.group(1)} rows, the table parses to "
                      f"{len(ledger_ids)}")
_forms = [line for line in _ledger_text.splitlines() if HDR_FORM_RE.match(line)]
if not _forms:
    header_bad.append("id-form table: no `| `A3\'s S<NN>` | current | this table | "
                      "N ids, S1-SNN |` row to check the count against")
for _row in _forms:
    _f = HDR_FORM_RE.match(_row)
    if int(_f.group(1)) != len(ledger_ids):
        header_bad.append(f"id-form table: declares {_f.group(1)} ids, the table parses to "
                          f"{len(ledger_ids)}")
    if int(_f.group(2)) != _max_id:
        header_bad.append(f"id-form table: declares the range ending at S{_f.group(2)}, the "
                          f"highest parsed id is S{_max_id}")
print(f"finding header: {len(header_bad)} declared counts disagreeing with the parsed table")
dump(header_bad, lambda t: f"  {t}")

# --- this file's own shape ---------------------------------------------------------------
# dump() is the only sanctioned way to print a capped enumeration, because it prints the
# withheld count: a summary line and the list under it cannot disagree (dump's docstring
# records the 25-vs-20 report that made this necessary). A bare `for x in things[:20]:` at
# module level is the shape there were seventeen of, and it now fails the build -- the rule
# has a source in code, not a comment saying "prefer a helper".
SELF = pathlib.Path(__file__).read_text(errors="replace").splitlines()
SELF_SLICE_RE = re.compile(r"^for .* in [A-Za-z_][A-Za-z0-9_]*\[:\d+\]:\s*$")
self_bad = [n for n, line in enumerate(SELF, 1) if SELF_SLICE_RE.match(line)]
print(f"checker self-lint: {len(self_bad)} bare capped loops "
      f"(dump() is the only sanctioned form)")
dump(self_bad, lambda n: f"  {__file__}:{n}: {SELF[n - 1]}", cap=5)

sys.exit(1 if (bad or missing or cite_bad or path_bad or ambiguous or links_bad or header_bad
              or id_orphans or path_orphans or labels_bad or status_bad
              or phase_bad or ms_bad or finding_bad or unbound_notes
              or width_bad or idc_bad or ve_dangling or ve_orphans
              or self_bad) else 0)

