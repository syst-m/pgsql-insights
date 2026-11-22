#!/usr/bin/env python3
"""R15.3 — generate docs/licenses/ from the two dependency manifests.

R15.3 asks for a third-party license audit "committed as `docs/licenses/` and
regenerated in CI". Two things have to be true for that to be a gate rather than
a report, and the shell one-liner this replaces satisfied neither:

  1. The output has to be deterministic. The old file carried a
     `# Generated: <timestamp>` line, so every run produced a diff — which is
     what kept `make licenses` a thing you ran by hand and made it impossible to
     gate the way `make render-check` gates deploy/plain-yaml. Git already
     records when the file changed; a timestamp inside it buys nothing and costs
     the diff test.
  2. It has to cover both halves R15.3 names: `go-licenses` *and* the SPA's
     license set. The old target wrote the Go module list only, and its own
     header line admitted the license text was still a Phase 9 promise.

Sources, both committed, so the generator needs no toolchain and no network:

  go.mod                — Go 1.17+ lists every module a build needs, direct and
                          indirect, and `make tidy-check` (R13.10) enforces that
                          the list is complete. That guarantee is what lets this
                          read the file instead of shelling out to `go list -m
                          all`, so the check runs on a runner with no Go SDK and
                          produces byte-identical output on every machine.
  web/package-lock.json — npm's lockfile carries an SPDX `license` field on
                          every package entry, so the SPA half is exact rather
                          than inferred from package.json's 7 direct deps.

go-licenses: when the binary is on PATH, `go-licenses report ./...` runs and its
output is written to docs/licenses/go-licenses-report.csv. When it is not, the
Go manifest says so in its header and `make licenses` prints the skip line — the
same shape `lint-go` uses for golangci-lint, so a machine without the tool
reports the gap instead of pretending the audit is complete. The manifest is
still valid without it: the module list is the audit's subject, and the license
names are a column that fills in as soon as the tool is installed.

Usage: gen-licenses.py [--check]
  no args    write the files
  --check    write to a temp dir and diff against the committed files; exit 1 on
             any difference. This is `make licenses-check`, and it is the half
             that makes "regenerated in CI" true rather than merely claimed.
"""

from __future__ import annotations

import csv
import io
import json
import os
import re
import shutil
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT_DIR = os.path.join(ROOT, "docs", "licenses")
GO_MOD = os.path.join(ROOT, "go.mod")
LOCK = os.path.join(ROOT, "web", "package-lock.json")
GO_LICENSES = shutil.which("go-licenses")

# The project's own license, from LICENSE at the repo root (R15.1). Hard-coded
# rather than parsed because a module's license is not derivable from its source
# without a matcher, and this value is a decision, not a fact about a dependency.
MODULE_LICENSE = "Apache-2.0"


def parse_go_mod(path: str) -> tuple[str, list[tuple[str, str, str]]]:
    """Return (module path, [(module, version, direct|indirect), ...]).

    Handles both require forms Go emits: a single-line `require x v1.2.3` and a
    parenthesised block of `x v1.2.3 // indirect` lines.
    """
    module = ""
    requires: list[tuple[str, str, str]] = []
    in_block = False
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = raw.strip()
            if not line or line.startswith("//"):
                continue
            if not in_block and line.startswith("module "):
                module = line.split(None, 1)[1].strip()
                continue
            if not in_block and line.startswith("require ("):
                in_block = True
                continue
            if in_block:
                if line == ")":
                    in_block = False
                    continue
                requires.append(_require(line))
                continue
            m = re.match(r"^require\s+(\S+)\s+(\S+)", line)
            if m:
                requires.append((m.group(1), m.group(2), _direct(m.group(0))))
    return module, requires


def _require(line: str) -> tuple[str, str, str]:
    parts = line.split()
    module, version = parts[0], parts[1]
    return module, version, _direct(line)


def _direct(text: str) -> str:
    return "indirect" if "// indirect" in text else "direct"


def render_go_manifest(module: str, requires: list[tuple[str, str, str]]) -> str:
    direct = sum(1 for _, _, d in requires if d == "direct")
    indirect = len(requires) - direct
    lic_source = (
        "docs/licenses/go-licenses-report.csv"
        if GO_LICENSES
        else "go-licenses not on PATH — license names pending, module list complete"
    )
    lines = [
        "# Dependency manifest — generated by 'make licenses', do not edit. (R15.3)",
        f"# Source: go.mod (module {module})",
        f"# License names: {lic_source}",
        "#",
        f"# {len(requires)} dependencies: {direct} direct, {indirect} indirect.",
        "# No `replace` directives (R13.10) — `make tidy-check` is the gate that",
        "# keeps this list equal to what the build actually imports, which is why",
        "# reading go.mod here is not a stale copy of `go list -m all`.",
        "#",
        "# <module> <version> <direct|indirect>",
    ]
    rows = sorted(requires, key=lambda r: (r[0], r[1]))
    for mod, ver, kind in rows:
        lines.append(f"{mod} {ver} {kind}")
    # The module itself has no version in go.mod; `go list -m all` lists it too,
    # so it is listed here with its own license rather than dropped.
    lines.append(f"{module} - direct  # this module ({MODULE_LICENSE})")
    return "\n".join(lines) + "\n"


def render_web_manifest(lock_path: str, go_module_count: int) -> str:
    with open(lock_path, encoding="utf-8") as fh:
        lock = json.load(fh)
    packages = lock.get("packages", {})
    rows: list[tuple[str, str, str]] = []
    missing = 0
    for path, meta in packages.items():
        if not path:  # the root entry describes web/ itself
            continue
        name = meta.get("name") or path.split("node_modules/")[-1]
        version = meta.get("version", "")
        license_ = meta.get("license")
        if license_ is None:
            # npm lockfiles carry `license` on every well-formed package; a
            # missing one is a fact about the dependency, not a gap here.
            license_ = "NOASSERTION"
            missing += 1
        rows.append((name, version, str(license_)))
    rows.sort(key=lambda r: (r[0], r[1]))
    lines = [
        "# SPA dependency licenses — generated by 'make licenses', do not edit. (R15.3)",
        "# Source: web/package-lock.json",
        "#",
        f"# {len(rows)} packages, {missing} without a license field.",
        "# R15.3 names this half explicitly ('the SPA's license set'): the Go audit",
        f"# alone would miss every transitive npm dep — {len(rows)} packages here against",
        f"# {go_module_count} Go modules, so the Go-only audit covers the smaller surface.",
        "#",
        "# <package> <version> <license>",
    ]
    for name, version, license_ in rows:
        lines.append(f"{name} {version} {license_}")
    return "\n".join(lines) + "\n"


def run_go_licenses() -> str | None:
    """Return the go-licenses report CSV, or None if it cannot be produced."""
    if not GO_LICENSES:
        return None
    try:
        proc = subprocess.run(
            [GO_LICENSES, "report", "./..."],
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=600,
            check=True,
        )
    except (subprocess.SubprocessError, OSError):
        return None
    # `go-licenses report` emits CSV with no header row, columns in the order
    # <package>, <license URL>, <SPDX id> — not the tab-separated form the first
    # draft of this function assumed, which silently folded every row into one
    # column. csv.reader is used rather than str.split(",") because a license URL
    # can contain a comma-quoted segment, and the tool quotes accordingly.
    rows = [r for r in csv.reader(proc.stdout.splitlines()) if any(c.strip() for c in r)]
    if not rows:
        return None
    # io.StringIO, not a list: csv.writer needs an object with a write() method, and
    # the first draft passed a list — which never raised until the tool was installed,
    # because every earlier run returned early at the `if not GO_LICENSES` guard.
    buf = io.StringIO()
    writer = csv.writer(buf, lineterminator="\n")
    writer.writerow(["package", "license_url", "license"])
    for row in sorted(rows, key=lambda r: r[0]):
        writer.writerow((row + ["", ""])[:3])
    return buf.getvalue()


def main(argv: list[str]) -> int:
    check = "--check" in argv[1:]
    module, requires = parse_go_mod(GO_MOD)
    files = {
        "go-dependencies.txt": render_go_manifest(module, requires),
        "web-dependencies.txt": render_web_manifest(LOCK, len(requires)),
    }
    report = run_go_licenses()
    if report is not None:
        files["go-licenses-report.csv"] = report

    if check:
        stale = []
        for name, body in files.items():
            target = os.path.join(OUT_DIR, name)
            current = ""
            if os.path.exists(target):
                with open(target, encoding="utf-8") as fh:
                    current = fh.read()
            if current != body:
                stale.append(name)
        if stale:
            print(
                "FAIL: docs/licenses is stale — run 'make licenses' and commit (R15.3):\n  "
                + "\n  ".join(stale),
                file=sys.stderr,
            )
            if GO_LICENSES is None:
                # Without the tool the generated set is two files and go-dependencies.txt
                # carries the "pending" header; the committed pair carries the real one.
                # Say so, or the diff looks like an unrelated edit to a generated file.
                print(
                    "cause: go-licenses is not on PATH, so go-licenses-report.csv cannot be\n"
                    "       regenerated and go-dependencies.txt's license-names header\n"
                    "       reverts to its pending form. Install it (CI pins v1.6.0).",
                    file=sys.stderr,
                )
            return 1
        print(f"ok: docs/licenses matches the manifests ({len(files)} files)")
        return 0

    os.makedirs(OUT_DIR, exist_ok=True)
    for name, body in files.items():
        target = os.path.join(OUT_DIR, name)
        tmp = target + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            fh.write(body)
        os.replace(tmp, target)
        print(f"generated docs/licenses/{name}")
    if report is None:
        print(
            "note: go-licenses not on PATH; license names for Go deps pending "
            "(CI installs it; the module list is complete either way)"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
