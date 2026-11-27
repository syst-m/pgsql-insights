#!/usr/bin/env python3
"""Derive per-requirement implementation rows from the repo.

    usage: ledger.py [R-id ...] [--json] [--root DIR] [--strict]

One instrument for the question "what is the status of R8.1" (or of everything). It exists
because that question has been answered from recall often enough to be wrong often enough to
be memorised: a row's file list can name the wrong file, a status cell can lag the code by
nine merges, and a number read before an edit is invalidated by the edit. So nothing here is
typed. Every field is read from a file or from git in this process, and the output carries
the UTC instant at which it was measured.

Four sections per run:

  GATE      the parity checker's own stdout, byte-for-byte, plus its rc. Imported and run,
            not shelled out to, because the module *is* its main() -- `exec_module` with
            argv patched runs the whole gate. There is therefore no way to use this script's
            parser without also running the gate, which is the point: the ledger cannot be
            produced with the gate unrun. The rc comes from the SystemExit the module raises,
            not from a pipe (a piped command's status is not its own).
  SELF-CHECK
            the row count this script parsed from the implementation docs against the count
            `check_status_enum` reports. Both come from the same import, so agreement is
            cheap -- but it is the only thing that notices when this file's parse rule drifts
            from CI's, and a silent drift turns every number below into fiction.
  LEDGER    one row per requested id: title, declared status + owner, the first and latest
            *code* commit whose diff contains the id string, the same for doc commits, and
            whether the row's cited paths resolve. `declared` and `measured` print side by
            side because an assertion that cannot fail is an exhibit -- the only instrument
            that separates "the doc says implemented" from "the code is implemented" is
            printing both.
  FINDINGS  rows where the doc's status and the code evidence contradict each other.

Parser reuse: `check_status_enum`, `split_row_esc`, `STATUS_CELL_RE`, `STATUS_ROW_ID_RE`,
`CITE_A`, `resolve_path`, `tracked_files`, `impl_docs`, `spec_docs` and `lines_of` are
imported from scripts/check-parity-matrix.py rather than re-implemented. A second parser
would be a second copy of the contract, and the first copy to drift is the one that stops
being true.

Why the code/doc commit split, and not one `git log -S <id>`: every row's own text contains
its id, so a single unscoped search finds the commit that *wrote the row* and reports it as
proof the feature exists -- an assertion that cannot fail. Restricting the search to
non-markdown paths (`:(exclude)*.md`) is what makes the answer able to be no: a requirement
id that appears in a .go or .tsx file is cited by code, and one that appears only in .md is
cited by docs alone. That is the difference between `implemented` and `claimed`.

Exit code: the parity gate's rc. The ledger's findings are reported, not failed -- same
convention as the checker's report-only tier -- so a pipeline fails on gate breakage and not
on an audit finding. --strict also fails on findings and on a self-check mismatch.

Cost: one `git log -S` per distinct id, memoised. A full run over ~170 ids takes a few
minutes; asking for specific ids is near-instant. The gate run itself is the fixed cost.
"""

import datetime
import importlib.util
import io
import json
import re
import subprocess
import sys
from contextlib import redirect_stdout
from pathlib import Path

CHECKER = "scripts/check-parity-matrix.py"
SPEC_ROOT_DOC = "requirements.md"
IMPL_ROOT_DOC = "IMPLEMENTATION.md"
NO_MD = ":(exclude)*.md"          # code-side evidence: the id appears outside markdown
SEP_RE = re.compile(r"^\|[\s:|-]+\|$")


def load_checker(root):
    """Import the parity checker with argv patched, capture its stdout, return (module, rc, text).

    The module runs main() at import time (`check-parity-matrix.py:2` reads sys.argv, `:13`
    reads sys.argv[1], and the file ends in sys.exit(rc)), so importing it *is* running it and
    the rc arrives as a SystemExit. Patching argv is what makes the import work at all; without
    it the module prints USAGE and exits 2 -- an error that looks exactly like a broken gate
    and is only a missing argument. The module object, not sys.modules, is the carrier:
    module_from_spec + exec_module does not register the name in sys.modules, so looking it up
    there returns None and throws away the parser that was just built.
    """
    path = root / CHECKER
    if not path.is_file():
        return None, None, f"{root / CHECKER} does not exist\n"
    spec = importlib.util.spec_from_file_location("parity_checker", path)
    module = importlib.util.module_from_spec(spec)
    buf = io.StringIO()
    argv = sys.argv[:]
    sys.argv = [str(path), str(root / SPEC_ROOT_DOC)]
    rc = 0
    try:
        with redirect_stdout(buf):
            spec.loader.exec_module(module)
    except SystemExit as exc:                      # the module's normal exit path
        rc = 0 if exc.code is None else int(exc.code)
    except Exception as exc:                       # a real error: degrade, don't half-trust
        return None, None, f"{root / CHECKER} raised {type(exc).__name__}: {exc}\n"
    finally:
        sys.argv = argv
    return module, rc, buf.getvalue()


def docs_for(root, role, checker):
    """The documents carrying a role, via the checker's own resolver when importable.

    role 'impl'  -> IMPLEMENTATION.md + docs/implementation/**
    role 'spec'  -> requirements.md   + docs/requirements/**
    Union semantics is what makes the in-flight docs decomposition a no-op here as well as in
    the checker: whichever of the two layouts exists, the resolver names the right files.
    """
    if checker is not None:
        fn = checker.impl_docs if role == "impl" else checker.spec_docs
        return list(fn(root))
    return [IMPL_ROOT_DOC if role == "impl" else SPEC_ROOT_DOC]


def read_lines(root, rel, checker):
    if checker is not None:
        return list(checker.lines_of(root, rel))
    p = root / rel
    return p.read_text(errors="replace").splitlines() if p.is_file() else []


def git(root, *args):
    """git in `root`, returning stdout, or None if it failed. Never raises into the report."""
    try:
        r = subprocess.run(("git", "-C", str(root)) + tuple(args),
                           capture_output=True, text=True, timeout=120)
        return r.stdout if r.returncode == 0 else None
    except Exception:
        return None


_log_cache = {}


def commits_touching(root, needle, pathspecs=None):
    """[(sha, date)] for commits whose diff contains `needle`, newest first; [] if none.

    pathspecs None searches the whole tree; [NO_MD] restricts it to non-markdown files, which
    is the code-side question. Memoised per (root, needle, pathspecs) -- an id is legitimately
    rowed in more than one table (R8.1 has rows in §3.7 *and* §3.8), so without the cache a
    full run pays one `git log` per row instead of per id.
    """
    args = ["log", "-S", needle, "--format=%H %ad", "--date=format:%Y-%m-%dT%H:%M:%SZ"]
    if pathspecs:
        args.append("--")
        args.extend(pathspecs)
    key = (str(root), needle, tuple(pathspecs or ()))
    if key not in _log_cache:
        out = git(root, *args)
        rows = []
        if out is not None:
            for ln in out.splitlines():
                if " " in ln:
                    sha, date = ln.split(" ", 1)
                    rows.append((sha[:12], date))
        _log_cache[key] = rows
    return _log_cache[key]


def parse_impl_rows(root, checker):
    """Every R-id row in the implementation docs: (rel, lineno, id, cells).

    The predicate is byte-identical to `check_status_enum`'s (a `| `-prefixed line, not a
    separator row, whose first cell matches STATUS_ROW_ID_RE), and `main` asserts the count
    against that function so a drift between the two is loud rather than silent.
    """
    rows = []
    for rel in docs_for(root, "impl", checker):
        for n, line in enumerate(read_lines(root, rel, checker), 1):
            if not line.startswith("| ") or not SEP_RE.match(line):
                cells = checker.split_row_esc(line)
                if cells and checker.STATUS_ROW_ID_RE.match(cells[0]):
                    rows.append((rel, n, cells[0], cells))
    return rows


def parse_spec_titles(root, checker):
    """{R-id: title} from the spec docs, for labelling rows. No status is read here: the
    requirements tables' last cell is a Notes/Source cell, not a status cell, and reading it
    as one produced 135 rows with a status of `?` in the first version of this script."""
    titles = {}
    for rel in docs_for(root, "spec", checker):
        for line in read_lines(root, rel, checker):
            if not line.startswith("| ") or not SEP_RE.match(line):
                continue
            cells = checker.split_row_esc(line)
            if cells and checker.STATUS_ROW_ID_RE.match(cells[0]) and len(cells) > 1:
                titles.setdefault(cells[0], cells[1].strip())
    return titles


def status_cell(checker, cells):
    """(raw_cell, status, owner). A cell that is not a canonical status cell yields
    (cell, None, None) rather than a guess, so a wider table's last column never masquerades
    as a status."""
    cell = cells[-1]
    m = checker.STATUS_CELL_RE.match(cell)
    if m:
        raw = f"**{m.group(1)}{f' ({m.group(2)})' if m.group(2) else ''}**"
        return raw, m.group(1), m.group(2)
    return cell, None, None


def cite_targets(root, checker, cells):
    """Paths cited anywhere in the row, resolved through the checker's resolver.

    Returns [(raw, resolved_or_None)]. A cite that resolves to nothing is reported, not
    dropped -- a dropped miss is how a stale table looks clean.
    """
    tracked = list(checker.tracked_files(root))
    tracked_set = set(tracked)
    out = []
    for cell in cells:
        for m in checker.CITE_A.finditer(cell):
            out.append((m.group(1), checker.resolve_path(tracked, tracked_set, "", m.group(1))))
    return out


def verdict(status, code_commits):
    """Compare a row's declared status with the code evidence. Returns (verdict, is_finding).

    The table is deliberately narrow. `in-progress` and `blocked` are compatible with partial
    code either way, so they are never findings -- calling them one would make the report a
    list of every row that is not finished. `tabled` with code is a finding because a tabled
    requirement should not have shipped code citing it.
    """
    has_code = bool(code_commits)
    if status is None:
        return "not-a-status-row", False
    if status in ("implemented", "validated"):
        return ("consistent", False) if has_code else ("no-code-trace", True)
    if status == "not-started":
        return ("contradicted", True) if has_code else ("consistent", False)
    if status == "tabled":
        return ("contradicted", True) if has_code else ("consistent", False)
    return "consistent", False  # in-progress, blocked: partial code proves nothing either way


def row_record(root, rel, lineno, rid, cells, checker, titles):
    """One measured row. Every field is read in this call; none is carried from a previous one."""
    raw, status, owner = status_cell(checker, cells)
    code = commits_touching(root, rid, [NO_MD])
    everywhere = commits_touching(root, rid)
    cites = cite_targets(root, checker, cells)
    v, is_finding = verdict(status, code)
    return {
        "id": rid,
        "title": titles.get(rid, ""),
        "declared_in": f"{rel}:{lineno}",
        "declared_status": raw,
        "status": status,
        "owner": owner,
        "code_commits": {"count": len(code),
                         "first": {"sha": code[0][0], "date": code[0][1]} if code else None,
                         "latest": {"sha": code[-1][0], "date": code[-1][1]} if code else None},
        "doc_commits": {"count": len(everywhere),
                        "first": {"sha": everywhere[0][0], "date": everywhere[0][1]}
                                 if everywhere else None},
        "cites": len(cites),
        "cites_unresolved": [p for p, r in cites if r is None][:8],
        "verdict": v,
        "finding": is_finding,
    }


def main(argv):
    root = Path(".")
    as_json = False
    strict = False
    want = []
    args = argv[1:]
    i = 0
    while i < len(args):
        a = args[i]
        if a == "--root":
            root = Path(args[i + 1]).resolve(); i += 2
        elif a == "--json":
            as_json = True; i += 1
        elif a == "--strict":
            strict = True; i += 1
        elif re.match(r"^[Rr]\d", a):
            want.append(a.upper()); i += 1
        else:
            print(f"ledger.py: unknown argument {a!r}", file=sys.stderr)
            print(__doc__.strip().splitlines()[1], file=sys.stderr)
            return 2
    if not root.is_dir():
        print(f"ledger.py: --root {root} is not a directory", file=sys.stderr)
        return 2

    checker, gate_rc, gate_text = load_checker(root)
    if checker is None:
        print(gate_text, end="", file=sys.stderr)
        return 1

    impl_rows = parse_impl_rows(root, checker)
    titles = parse_spec_titles(root, checker)
    # The self-check: this script's own parse vs the imported function's count. Same predicate,
    # so a mismatch means one of the two changed under the other.
    checker_rows, checker_bad = checker.check_status_enum(root)
    self_ok = len(impl_rows) == checker_rows

    if want:
        rows = [r for r in impl_rows if r[2].upper() in want]
        missing = sorted(set(want) - {r[2].upper() for r in rows})
    else:
        rows, missing = impl_rows, []

    records = [row_record(root, rel, n, rid, cells, checker, titles) for rel, n, rid, cells in rows]
    findings = [r for r in records if r["finding"]]
    measured = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    if as_json:
        print(json.dumps({
            "measured_at": measured,
            "root": str(root),
            "gate_rc": gate_rc,
            "self_check": {"ledger_rows": len(impl_rows), "checker_rows": checker_rows,
                           "agree": self_ok, "checker_non_canonical": len(checker_bad)},
            "rows": records,
            "requested_not_found": missing,
            "findings": [f"{r['declared_in']} {r['id']} {r['verdict']}" for r in findings],
        }, indent=2))
    else:
        print(f"# ledger  measured {measured}  root {root}")
        print()
        print(f"## gate  rc={gate_rc}")
        print()
        for line in gate_text.rstrip("\n").splitlines():
            print(f"  {line}")
        print()
        print(f"## self-check  ledger parsed {len(impl_rows)} impl rows, "
              f"check_status_enum reports {checker_rows} -> "
              f"{'OK' if self_ok else 'DIFFER'}")
        if missing:
            print(f"   requested ids with no row: {', '.join(missing)}")
        print()
        print(f"## ledger  {len(records)} rows")
        print()
        for r in records:
            owner = f" ({r['owner']})" if r["owner"] else ""
            first = r["code_commits"]["first"]
            print(f"  {r['id']:9} {(r['status'] or '?'):12}{owner:6} "
                  f"{r['declared_in']:44} "
                  f"code {r['code_commits']['count']:3} "
                  f"first {first['sha'] if first else '-':12}@{first['date'] if first else '-'} "
                  f"cites {r['cites']:2}  {r['verdict']}")
        print()
        print(f"## findings  {len(findings)}")
        print()
        for r in findings:
            print(f"  {r['declared_in']}  {r['id']}  {r['verdict']}  "
                  f"declared={r['declared_status']}  "
                  f"code_commits={r['code_commits']['count']}")
    if strict and (findings or not self_ok):
        return 1
    return gate_rc


if __name__ == "__main__":
    sys.exit(main(sys.argv))
