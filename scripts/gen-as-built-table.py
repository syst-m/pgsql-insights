#!/usr/bin/env python3
"""Generate/verify the "As-built vs target" table in docs/architecture.md.

R14.3's honesty rule says a reader must be able to tell what runs today from what is
planned. The table's *Where it lives* column is a hand-maintained copy of `git ls-tree`:
every file count, test count, component count and file name in that column is derivable
from git, and every one of them rots the moment anyone commits a file without editing the
row. That is the same failure mode `docs/reviews/finding-index.md`'s commit column had
(S145), and it is fixed the same way `api/openapi.yaml` fixed its copy: generate it.

What this script owns is the derivable half of each row:

    | Box | Requirement | Where it lives | Status |
      ---- ------------- ---------------- --------
      the integers and file names in column 3, from `git ls-tree -r --name-only HEAD`

The Box name, the Requirement ids and the Status prose are authored, not derived, and are
left byte-for-byte alone -- so `--write` moves digits and nothing else.

Usage:
    python3 scripts/gen-as-built-table.py [--repo R] [--doc P] [--check|--write]

    --check   default; exit 1 and print one line per drifting row (the CI form)
    --write   rewrite the derived integers in the doc, then report what changed
"""

from __future__ import annotations

import argparse
import collections
import os
import re
import subprocess
import sys

TABLE_HDR = "| Box | Requirement | Where it lives | Status |"
TABLE_SEP = "|---|---|---|---|"

BACKTICK = re.compile(r"`([^`]+)`")
N_FILES = re.compile(r"\b(\d+) files\b")
N_TESTS = re.compile(r"\b(\d+) tests\b")
N_COMPS = re.compile(r"\b(\d+) components\b")
# "61 files: 31 + 30 tests" -- the pair after the total is (non-test, test).
N_NONTEST_PLUS = re.compile(r"\b(\d+) \+ (\d+) tests\b")

TEST_SUFFIX = re.compile(r"(_test\.go|\.test\.tsx|\.test\.ts|\.test\.js|\.test\.jsx)$")
COMP_SUFFIX = re.compile(r"\.(tsx|jsx|vue)$")
# A backticked token is a path candidate only if it looks like a path: a slash, a known
# extension, and no spaces (so `go:embed all:dist` and `helm install` are not paths).
PATHISH = re.compile(r"^[A-Za-z0-9_.@-]+(/[A-Za-z0-9_.@-]+)*$")


def git_files(repo: str, rev: str = "HEAD") -> list[str]:
    out = subprocess.run(
        ["git", "-C", repo, "ls-tree", "-r", "--name-only", rev],
        capture_output=True, text=True, check=True,
    ).stdout
    return [line for line in out.split("\n") if line]


def dir_stats(files: list[str]) -> dict[str, tuple[int, int, int]]:
    """dir -> (files, tests, components) for every ancestor dir of every tracked file."""
    counts: dict[str, list[int]] = collections.defaultdict(lambda: [0, 0, 0])
    for f in files:
        parts = f.split("/")
        for i in range(1, len(parts)):
            d = "/".join(parts[:i])
            counts[d][0] += 1
            if TEST_SUFFIX.search(f):
                counts[d][1] += 1
            elif COMP_SUFFIX.search(f):
                counts[d][2] += 1
    return {d: (n, t, c) for d, (n, t, c) in counts.items()}


def parse_rows(lines: list[str]) -> tuple[int, int, list[tuple[int, list[str]]]]:
    """-> (header line index, last table line index, [(line index, cells)])."""
    try:
        hdr = next(i for i, l in enumerate(lines) if l.strip() == TABLE_HDR)
    except StopIteration:
        raise SystemExit(f"{TABLE_HDR!r} not found in doc")
    if lines[hdr + 1].strip() != TABLE_SEP:
        raise SystemExit(f"expected {TABLE_SEP!r} at line {hdr + 2}, found {lines[hdr + 1]!r}")
    rows: list[tuple[int, list[str]]] = []
    i = hdr + 2
    while i < len(lines) and lines[i].lstrip().startswith("|"):
        cells = [c.strip() for c in lines[i].split("|")]
        # "| a | b | c | d |" -> ['', 'a', 'b', 'c', 'd', '']
        if len(cells) < 6 or cells[0] != "" or cells[-1] != "":
            raise SystemExit(f"row at line {i + 1} does not split into 4 cells")
        rows.append((i, cells[1:-1]))
        i += 1
    return hdr, i - 1, rows


def check_cell(cell: str, stats: dict[str, tuple[int, int, int]],
               files: list[str]) -> tuple[list[str], dict[str, int]]:
    """-> (problems, derived) for one *Where it lives* cell.

    `derived` maps each integer pattern found in the cell to its correct value, so
    --write can substitute without re-deriving.
    """
    problems: list[str] = []
    derived: dict[str, int] = {}
    tokens = BACKTICK.findall(cell)

    # Strip the trailing slash *before* the path test: `internal/store/` is how the doc
    # writes a directory, and PATHISH only accepts a slash-free final segment.
    norm = [t.rstrip("/") for t in tokens if PATHISH.match(t.rstrip("/"))]
    dirs = [t for t in norm if t in stats]
    # A token with a dot is a file name, one without is a directory name; a token that is
    # itself a known directory is the row's subject, never one of its own contents.
    files_named = [t for t in norm if "." in t and t not in stats]
    subdirs_named = [t for t in norm if "." not in t and t not in stats]

    primary = dirs[0] if dirs else None
    if primary is None:
        # No directory in the cell: the row names individual files, so the only check is
        # that those files exist. (Rows like `cmd/pgsi/main.go` land here.)
        for f in files_named:
            # A bare name (`cors.go`) resolves by basename anywhere in the repo; a path
            # (`internal/api/auth.go`) must match exactly.
            ok = f in files or any(p.endswith("/" + f) for p in files)
            if not ok:
                problems.append(f"names `{f}`, which git does not track")
        for d in subdirs_named:
            if not any(p.startswith(f"{d}/") for p in files):
                problems.append(f"names `{d}/`, which git has no files under")
        return problems, derived

    n_files, n_tests, n_comps = stats[primary]

    m = N_FILES.search(cell)
    if m:
        derived["files"] = n_files
        if int(m.group(1)) != n_files:
            problems.append(f"cell says {m.group(1)} files, git has {n_files} in `{primary}/`")
    m = N_TESTS.search(cell)
    if m:
        derived["tests"] = n_tests
        if int(m.group(1)) != n_tests:
            problems.append(f"cell says {m.group(1)} tests, git has {n_tests} in `{primary}/`")
    m = N_COMPS.search(cell)
    if m:
        derived["components"] = n_comps
        if int(m.group(1)) != n_comps:
            problems.append(f"cell says {m.group(1)} components, git has {n_comps} "
                            f"non-test .tsx/.jsx in `{primary}/`")
    m = N_NONTEST_PLUS.search(cell)
    if m:
        nontest = n_files - n_tests
        derived["nontest"] = nontest
        if int(m.group(1)) != nontest:
            problems.append(f"cell says {m.group(1)} + {m.group(2)} tests, git has "
                            f"{nontest} non-test + {n_tests} test in `{primary}/`")

    # Every file the cell names must be tracked, and every subdirectory it names must exist
    # under the row's directory (the `cnpgmetrics/` names in the Collectors row).
    prefix = primary + "/"
    under = [f for f in files if f.startswith(prefix)]
    under_basenames = {os.path.basename(f) for f in under}
    for name in files_named:
        if name not in files and name not in under_basenames:
            problems.append(f"names `{name}`, not a file in `{primary}/`")
    for d in subdirs_named:
        if f"{primary}/{d}" not in stats and d not in stats:
            problems.append(f"names `{d}/`, not a directory under `{primary}/`")
    return problems, derived


def write_cell(cell: str, derived: dict[str, int]) -> str:
    """Substitute the derived integers back into the cell, leaving prose alone."""
    if "files" in derived:
        cell = N_FILES.sub(f"{derived['files']} files", cell, count=1)
    if "tests" in derived:
        cell = N_TESTS.sub(f"{derived['tests']} tests", cell, count=1)
    if "components" in derived:
        cell = N_COMPS.sub(f"{derived['components']} components", cell, count=1)
    if "nontest" in derived:
        cell = N_NONTEST_PLUS.sub(f"{derived['nontest']} + {derived['tests']} tests", cell, count=1)
    return cell


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--repo", default=".", help="git repo to read the file list from (default .)")
    ap.add_argument("--rev", default="HEAD", help="rev for the file list (default HEAD)")
    ap.add_argument("--doc", default=None, help="markdown file (default <repo>/docs/architecture.md)")
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="report drift, exit 1 if any (default)")
    mode.add_argument("--write", action="store_true", help="rewrite the derived integers in place")
    args = ap.parse_args()

    doc = args.doc or os.path.join(args.repo, "docs", "architecture.md")
    files = git_files(args.repo, args.rev)
    stats = dir_stats(files)
    with open(doc, encoding="utf-8") as fh:
        lines = fh.read().split("\n")
    hdr, last, rows = parse_rows(lines)

    drift = 0
    for idx, cells in rows:
        box, _, where, _status = cells
        problems, derived = check_cell(where, stats, files)
        if not problems:
            continue
        drift += 1
        print(f"{idx + 1}: {box}")
        for p in problems:
            print(f"    {p}")
        if args.write:
            lines[idx] = "| " + " | ".join([box, cells[1], write_cell(where, derived), cells[3]]) + " |"

    if args.write and drift:
        with open(doc, "w", encoding="utf-8") as fh:
            fh.write("\n".join(lines))
        print(f"wrote {doc}: {drift} row(s) corrected")
    elif args.write:
        print(f"{doc}: nothing to write")

    total = len(rows)
    if args.check or not args.write:
        print(f"as-built table: {total} rows checked · {drift} drifting · "
              f"{total - drift} clean (table at line {hdr + 1})")
    return 1 if drift else 0


if __name__ == "__main__":
    sys.exit(main())
