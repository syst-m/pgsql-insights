#!/usr/bin/env python3
"""Negative tests for gen-licenses.py's README-count assertion (R15.3).

`make licenses-check` runs two checks: that docs/licenses/ matches the two
manifests, and that docs/licenses/README.md's own ten restated counts agree with
them. The first is self-evidently graded — it diffs files. The second is a
function returning an int, and a function that always returns 0 passes it
forever. This file is the evidence that `check_readme_counts` grades: twenty-two
cases, each built by changing the README in exactly one place and requiring the
failure to name that place.

The cases:

  * one per number (10) — the five README_COUNT_PATTERNS capture ten numbers
    between them (3 + 2 + 1 + 2 + 2). Each captured group is bumped by one, in
    turn, and the run must exit 1 with a line naming that pattern *and* quoting
    the value it expected. Bumping only the first group of each pattern would let
    a check that compares group 1 and ignores the rest pass all five; the
    pattern list is read off the module, so the test cannot drift from the
    patterns it tests.
  * one per pattern (5) — the matched sentence replaced with a digit-free
    placeholder. Must exit 1 with `sentence not found` naming that label, and
    name it exactly once: a reworded sentence has to fail as "no longer checked",
    not drop the site silently.
  * the README missing — must exit 1 with GUARD, not raise.
  * the README unmutated — must exit 0 and report all five sites.
  * one per pattern, again (5) — the patterns' shape: every pattern must capture
    exactly as many groups as the keys it names, or `found.groups() != want`
    fails for a reason that has nothing to do with the README.

Needs no toolchain and no network: it imports gen-licenses.py from its sibling
path and passes the committed docs/licenses/ bodies in as the `files` dict, so it
runs on a machine without go-licenses — where `make licenses-check` cannot go
green at all, because the generator's first half short-circuits on the missing
binary and never reaches the README assertion. That is the second reason this
file exists: it is the only way to exercise the README assertion locally without
installing the tool CI pins.

Usage: gen-licenses-selftest.py    (exit 0 if all pass, 1 if any fail)
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
GEN_LICENSES = os.path.join(HERE, "gen-licenses.py")
LICENSES_DIR = os.path.join(ROOT, "docs", "licenses")
MANIFESTS = ("go-dependencies.txt", "web-dependencies.txt", "go-licenses-report.csv")


def read(path: str) -> str:
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def load_gen():
    """Import gen-licenses.py from its path, not from a copy of its source."""
    spec = importlib.util.spec_from_file_location("gen_licenses_under_test", GEN_LICENSES)
    if spec is None or spec.loader is None:
        raise SystemExit(f"selftest: cannot import {GEN_LICENSES}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def check(gen, readme_text: str, files: dict[str, str], tmpdir: str) -> tuple[int, str]:
    """Run check_readme_counts against one README body, capturing both streams.

    `check_readme_counts` reads the path in the module global `README_MD`, so the
    body under test is written to a temp file and the global is repointed at it —
    the function is exercised as `--check` exercises it, not through a copy of its
    body that could differ from the shipped one.
    """
    path = os.path.join(tmpdir, "README.md")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(readme_text)
    previous = gen.README_MD
    gen.README_MD = path
    try:
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf):
            rc = gen.check_readme_counts(files)
        return rc, buf.getvalue()
    finally:
        gen.README_MD = previous


def check_missing(gen, files: dict[str, str]) -> tuple[int, str]:
    """Same, with README_MD pointing at a path that does not exist."""
    previous = gen.README_MD
    gen.README_MD = os.path.join(tempfile.gettempdir(), "gen-licenses-selftest-absent.md")
    try:
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf):
            rc = gen.check_readme_counts(files)
        return rc, buf.getvalue()
    finally:
        gen.README_MD = previous


def splice(text: str, start: int, end: int, replacement: str) -> str:
    return text[:start] + replacement + text[end:]


def main() -> int:
    gen = load_gen()
    if not os.path.isdir(LICENSES_DIR):
        print(f"selftest: {LICENSES_DIR} does not exist", file=sys.stderr)
        return 1
    files = {name: read(os.path.join(LICENSES_DIR, name)) for name in MANIFESTS}
    readme = read(gen.README_MD)
    expected = gen.expected_counts(files)

    with tempfile.TemporaryDirectory(prefix="gen-licenses-selftest-") as tmpdir:
        failures: list[str] = []
        total = 0

        def record(name: str, ok: bool, detail: str) -> None:
            nonlocal total
            total += 1
            print(f"{'PASS' if ok else 'FAIL'}  {name}: {detail}")
            if not ok:
                failures.append(name)

        # --- the patterns' shape, before anything is graded against them -----
        for label, pattern, keys in gen.README_COUNT_PATTERNS:
            ok = pattern.groups == len(keys)
            record(
                f"shape {label}",
                ok,
                f"{pattern.groups} captured groups for {len(keys)} keys "
                f"({', '.join(keys)})",
            )

        # --- one case per number: bump it, require the failure to name it ----
        for label, pattern, keys in gen.README_COUNT_PATTERNS:
            match = pattern.search(readme)
            if match is None:
                record(f"mutate {label}", False, "pattern does not match the committed README")
                continue
            want = tuple(expected[k] for k in keys)
            for index, key in enumerate(keys):
                bumped = str(int(want[index]) + 1)
                mutated = splice(readme, match.start(index + 1), match.end(index + 1), bumped)
                rc, out = check(gen, mutated, files, tmpdir)
                # The body must name the row *and* quote the value it expected:
                # a failure for the wrong number is a failure that gets ignored.
                want_line = f"{label}: README says {' / '.join(want[:index] + (bumped,) + want[index + 1:])},"
                ok = rc == 1 and want_line in out
                detail = f"README says {bumped} for {key}, generated says {want[index]} -> rc={rc}"
                if not ok:
                    detail += f"; output was: {out.strip().splitlines()[-1] if out.strip() else '(none)'}"
                record(f"mutate {label} [{key}]", ok, detail)

        # --- one case per pattern: reword it, require "sentence not found" ---
        for label, pattern, keys in gen.README_COUNT_PATTERNS:
            match = pattern.search(readme)
            if match is None:
                continue  # already reported above
            mutated = splice(readme, match.start(), match.end(), "[reworded by hand]")
            rc, out = check(gen, mutated, files, tmpdir)
            n = out.count("sentence not found")
            ok = rc == 1 and f"{label}: sentence not found" in out and n == 1
            record(
                f"reword {label}",
                ok,
                f"rc={rc}, {n} site(s) reported as not found, label "
                f"{'named' if f'{label}: sentence not found' in out else 'missing'}",
            )

        # --- the README missing, and the README untouched --------------------
        rc, out = check_missing(gen, files)
        record("missing README", rc == 1 and "GUARD" in out, f"rc={rc} :: {out.strip()}")

        rc, out = check(gen, readme, files, tmpdir)
        sites = f"({len(gen.README_COUNT_PATTERNS)} sites)"
        record("clean README", rc == 0 and sites in out, f"rc={rc} :: {out.strip()}")

    print(f"\n{len(failures)} failing of {total}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
