#!/usr/bin/env python3
"""mutation-sweep.py — A5's mutation sweep over internal/query and internal/store.

Spec: requirements.md R14.10 (Mutation sweep), the requirement that names this file —
rowed by A3 on 2026-11-24 from A5's wording, under the §11 rule-4 precedent. Cited by
id rather than by line number, because an id survives the rewording a `:499`-style cite
cannot; the row's own text is the source for what the sweep owes (exit status, the
SITES coverage rule, and the row-or-refute obligation for a survivor).

A test that cannot fail is not a test. The cheapest way to find one is to break the
code it covers and watch the test fail; a test that still passes measured nothing,
which is the defect class finding-index S56/S57 rows (an assertion comparing a
value against the constant the code assigns it from — moving the constant moves
the expectation with it, so the pair can never disagree).

v1 lived in /tmp and classified a mutant by its `go test` rc alone, which counts a
compile error as a killed mutant: rc=1 from "did not compile" and rc=1 from "a
test caught it" are the same digit for different reasons. v2 runs `go vet` on the
package first (vet compiles the package *and* its _test.go files, which
`go build ./...` does not) and reports three states: build-failed, killed (naming
the first failing test), survived.

The SITES list is the sweep's whole knowledge: each entry is a (file, package,
before, after) mutation. A site is only worth listing if the mutation is one a
real bug could make — a constant off by one, a guard deleted, a string literal
typoed — and the file has a test that is supposed to notice. `NOT-UNIQUE` is
reported when the `before` string appears zero or several times, which means the
source moved under the entry and the site needs re-deriving rather than trusting.

Exit status: 0 when every mutant was killed, 1 when any survived. That is the
reason it lives in scripts/ rather than in a scratch directory — a harness that
cannot fail a build is a comment about a test, not a test.
"""
import subprocess, sys, pathlib

SITES = [
    # --- internal/query: the R3.6 length caps (S59's query-side half) ---------
    ("internal/query/normalizer.go", "internal/query", "MinQueryStringLength = 256", "MinQueryStringLength = 257"),
    ("internal/query/normalizer.go", "internal/query", "DefaultQueryStringLength = 1024", "DefaultQueryStringLength = 1025"),
    ("internal/query/normalizer.go", "internal/query", "EnterprisePlusQueryStringLength = 10000", "EnterprisePlusQueryStringLength = 10001"),
    ("internal/query/normalizer.go", "internal/query", "MaxQueryStringLength = 100000", "MaxQueryStringLength = 100001"),
    ("internal/query/normalizer.go", "internal/query", "\t\tsize = 1\n", "\t\tsize = 2\n"),
    # --- internal/store: the §3.4 config defaults (S59's store-side half) -----
    ("internal/store/store.go", "internal/store", "DefaultPlanRetention = 7 * 24 * time.Hour", "DefaultPlanRetention = 8 * 24 * time.Hour"),
    ("internal/store/store.go", "internal/store", "DefaultMaxFileBytes int64 = 1 << 30", "DefaultMaxFileBytes int64 = 1 << 29"),
    ("internal/store/store.go", "internal/store", "DefaultFileConns = 4", "DefaultFileConns = 5"),
    ("internal/store/store.go", "internal/store", 'const memoryPath = ":memory:"', 'const memoryPath = ":memorx:"'),
    ("internal/store/series.go", "internal/store", "const msPerSecond = 1000.0", "const msPerSecond = 1001.0"),
    ("internal/store/retention.go", "internal/store", "const daySeconds = 86400", "const daySeconds = 86401"),
    ("internal/store/detail.go", "internal/store", "const detailHistoryLimit = 20", "const detailHistoryLimit = 21"),
    ("internal/store/topn.go", "internal/store", "pct = 100 * r.totalExecMs / clusterTotal", "pct = 99 * r.totalExecMs / clusterTotal"),
    ("internal/store/rollups.go", "internal/store", 'KindTag           = "tag"', 'KindTag           = "tagx"'),
    ("internal/store/rollups.go", "internal/store", 'KindDatabase      = "database"', 'KindDatabase      = "databasex"'),
    # --- the nil-receiver guards (d2415b1 / f634142) ---------------------------
    # These two are guard *deletions* rather than constant changes: the code under
    # test is the `if s == nil` line itself, and the only mutation that asks
    # "is the guard there" is to remove it. Both were verified by hand before being
    # listed here — the Used() one fails TestStoreUsed and TestStoreSizeOfNilStore
    # with a nil dereference, the RenderPerQuery one panics inside the method.
    ("internal/store/retention.go", "internal/store",
     "func (s *Store) Used() int64 {\n\tif s == nil {\n\t\treturn 0\n\t}\n",
     "func (s *Store) Used() int64 {\n"),
    ("internal/store/perquery.go", "internal/store",
     "func (s *Store) RenderPerQuery(ctx context.Context, topN int, prefix string) (string, error) {\n\tif s == nil {\n\t\treturn \"\", nil\n\t}\n",
     "func (s *Store) RenderPerQuery(ctx context.Context, topN int, prefix string) (string, error) {\n"),
]


def run(cmd):
    p = subprocess.run(cmd, shell=True, capture_output=True, text=True)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def classify(path, pkg, old, new):
    """Apply one mutation, classify it, and put the file back.

    Returns (class, detail, artifacts). `artifacts` lists untracked files the
    mutated run created under the package directory and that were removed again:
    mutating a path constant (":memory:" -> ":memorx:") makes a sqlite test open a
    *file* by the new name, so the .go restore alone leaves a 100 KB database
    behind. Only files under `pkg` are touched, so a concurrent slot's new file is
    never deleted by this sweep.
    """
    src = pathlib.Path(path).read_text()
    if src.count(old) != 1:
        return "NOT-UNIQUE", f"{path}: {src.count(old)} matches for {old[:40]!r}", []
    _, un_before = run("git status --porcelain --untracked-files=all")
    pathlib.Path(path).write_text(src.replace(old, new, 1))
    artifacts = []
    try:
        vrc, vout = run(f"go vet ./{pkg}/")
        if vrc != 0:
            verdict = "build-failed", vout.strip().splitlines()[0][:70] if vout.strip() else "rc=1"
        else:
            trc, tout = run(f"go test -count=1 ./{pkg}/")
            if trc == 0:
                verdict = "survived", ""
            else:
                fails = [l for l in tout.splitlines() if l.startswith("--- FAIL")]
                verdict = "killed", (fails[0][:70] if fails else f"rc={trc}")
        # Sweep the artifacts before the .go restore, so the untracked set is the
        # one the mutated code produced.
        _, un_after = run("git status --porcelain --untracked-files=all")
        for f in sorted(set(un_after.splitlines()) - set(un_before.splitlines())):
            f = f[3:] if f.startswith("?? ") else f
            created = pathlib.Path(f)
            if f.startswith(pkg + "/") and created.is_file():
                created.unlink()
                artifacts.append(f)
        return verdict[0], verdict[1], artifacts
    finally:
        # The restore is in a finally because v1 left the mutation on disk when a
        # subprocess raised, and a tree left mutated mid-sweep makes every later
        # site measure the wrong file.
        pathlib.Path(path).write_text(src)


def main():
    root = pathlib.Path(__file__).resolve().parent.parent
    import os
    os.chdir(root)

    # The tree must be exactly as it was when the sweep started: a sweep that
    # leaves a mutation behind is a sweep that breaks the next commit. Compared
    # against the *starting* status rather than against empty, because the
    # repository normally has other slots' work in flight and this script is
    # itself usually untracked the first time it runs.
    _, before = run("git status --porcelain")

    rows = [classify(path, pkg, old, new) + (path, old) for path, pkg, old, new in SITES]

    _, out = run("git status --porcelain")
    survivors = [f"{p}: {m}" for c, d, a, p, m in rows if c == "survived"]
    unverified = [f"{p}: {m}" for c, d, a, p, m in rows if c == "NOT-UNIQUE"]
    artifacts = [(p, a) for c, d, arts, p, m in rows for a in arts]

    print(f"{'file':<24} {'mutation':<38} {'class':<13} first failing test")
    for cls, detail, arts, path, old in rows:
        print(f"{path.split('/')[-1]:<24} {old.strip()[:38]:<38} {cls:<13} {detail}")

    print()
    print(f"{len(rows)} sites: "
          f"{sum(1 for c, *_ in rows if c == 'killed')} killed, "
          f"{len(survivors)} survived, "
          f"{sum(1 for c, *_ in rows if c == 'build-failed')} build-failed, "
          f"{len(unverified)} not-unique")
    for path, a in artifacts:
        print(f"  artifact removed after {path}: {a}")

    if out != before:
        print(f"WARNING: tree changed during the sweep.\nbefore:\n{before[:200]}\nafter:\n{out[:200]}")
    else:
        print("post-sweep git status: identical to the pre-sweep status")

    if survivors or unverified or out != before:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
