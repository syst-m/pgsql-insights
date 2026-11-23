#!/usr/bin/env python3
"""Generate internal/collector/pgversion/matrix_data.go — R2.4's matrix as data.

    python3 scripts/gen-pgmatrix.py                      # fetch, write to stdout
    python3 scripts/gen-pgmatrix.py --cache DIR           # read DIR/<slug>.html
    python3 scripts/gen-pgmatrix.py > internal/collector/pgversion/matrix_data.go

Why a generator and not a hand-written table: the matrix is ~1,100 column names
across six majors, and the defect class R2.4 exists to prevent is a *transcription*
error in exactly such a table ("11 columns" written where the view has 12). Every
name here is parsed out of the postgresql.org documentation for that major, so the
Go file is a rendering of the docs rather than a transcription of someone's memory —
which is the only way the six-major table R16.1 asks for is checkable at all.

Sources are the per-major documentation pages listed in PAGES (the reference set
postgres.org publishes for each major: the statistics-collector chapter, the
pg_stat_statements reference, and the catalog pages for the non-stats relations).
--cache exists so the fetch can happen once into a directory and the generation be
re-run offline; without it every page is fetched from the URL.
"""

import argparse, os, re, sys, urllib.request
from collections import OrderedDict

BASE = "https://www.postgresql.org/docs/{major}/{page}"

# (major, page, cache slug) for every documentation page we read. The page, not
# the file name, is the unit: one page documents several views
# (monitoring-stats.html documents pg_stat_activity, pg_stat_archiver, …;
# pgstatstatements.html documents both pg_stat_statements and
# pg_stat_statements_info; view-pg-replication-slots.html documents
# pg_replication_slots). The slug is the file name --cache looks for.
PAGES = [
    (m, page, slug)
    for m in range(13, 19)
    for page, slug in (
        ("monitoring-stats.html", "pg%s"),
        ("pgstatstatements.html", "pgs%s"),
        ("catalog-pg-settings.html", "set%s"),
        ("view-pg-replication-slots.html", "slot%s"),
        ("view-pg-locks.html", "v%s-locks"),
        ("view-pg-file-settings.html", "v%s-file-settings"),
        ("catalog-pg-database.html", "cat%s-database"),
        ("catalog-pg-extension.html", "ext%s"),
    )
]

# --- parsing the three HTML shapes the docs use ------------------------------------
# <table class="table" summary="pg_locks Columns" border="1">        single-view pages
# <table class="table" summary="pg_stat_activity View" border="1">   chapter pages
# rows as <tr>…</tr> (newer) or <row>…</row> (older DocBook), the column name being
# the row's first <code class="structfield">x</code> / <structfield>x</structfield>;
# some pages instead use <p class="column_definition"><code class="structfield">x</code>
TABLE = re.compile(
    r'<table class="table"[^>]*?summary="([A-Za-z0-9_.\- ]+?)\s+(?:View|Table|Columns)"[^>]*>(.*?)</table>',
    re.S,
)
ROW = re.compile(r"<(?:tr|row)[ >]", re.I)
FIELD = re.compile(r'<(?:code class="structfield"|structfield)>([A-Za-z0-9_]+)<')
DEFN = re.compile(r'<p class="column_definition">\s*<code class="structfield">([A-Za-z0-9_]+)</code>')


def tables_in(html):
    """{table name: [column, …]} for every table in one documentation page."""
    out = OrderedDict()
    for name, body in TABLE.findall(html):
        cols = []
        for row in ROW.split(body)[1:]:
            m = FIELD.search(row)
            if m and m.group(1) not in cols:
                cols.append(m.group(1))
        if not cols:  # column_definition markup, no rows
            cols = list(OrderedDict.fromkeys(DEFN.findall(body)))
        if cols:
            out[name] = cols
    return out


def fetch(major, page, slug, cache):
    if cache:
        with open(os.path.join(cache, (slug % major) + ".html"),
                  encoding="utf-8", errors="replace") as fh:
            return fh.read()
    with urllib.request.urlopen(urllib.request.Request(
            BASE.format(major=major, page=page),
            headers={"User-Agent": "Mozilla/5.0"}), timeout=60) as resp:
        return resp.read().decode("utf-8", "replace")


def collect(cache):
    """{major: {view: [columns]}} across every page in PAGES."""
    matrix = {}
    for major, page, slug in PAGES:
        for view, cols in tables_in(fetch(major, page, slug, cache)).items():
            prev = matrix.setdefault(major, {}).get(view)
            if prev is not None and prev != cols:
                # Two pages documenting the same view with different columns. The
                # near-miss that is *not* a conflict is pg_replication_slots (the
                # slot catalog view) vs pg_stat_replication_slots (the logical
                # replication statistics view): different views, different names,
                # so they land on different keys. A real conflict here is a docs
                # change or a parser bug — loud, not silent.
                raise SystemExit(
                    "pg%d: %s documented twice with different columns (%d vs %d)"
                    % (major, view, len(prev), len(cols)))
            matrix[major][view] = cols
    return matrix


# --- emitting the Go file -----------------------------------------------------------
# Go initialisms: revive's var-naming rule (enabled in .golangci.yml) rejects
# "Ssl"/"Io"/"Wal" for an initialism, so the parts that are acronyms are uppercased
# whole rather than title-cased.
INITIALISMS = {"io": "IO", "ssl": "SSL", "gssapi": "GSSAPI", "wal": "WAL",
               "slru": "SLRU", "sql": "SQL", "pg": "PG", "oid": "OID", "max": "MAX"}


def go_ident(view):
    """pg_stat_activity -> ViewStatActivity, pg_stat_wal -> ViewStatWAL."""
    parts = view.split("_")
    if parts[0] == "pg":
        parts = parts[1:]
    return "View" + "".join(
        INITIALISMS.get(p, p[:1].upper() + p[1:]) for p in parts)


def wrap(names, width=74):
    """Wrap a column list into indented raw-string lines of <= width columns."""
    lines, line = [], ""
    for n in names:
        cand = (line + " " + n).strip()
        if line and len(cand) > width:
            lines.append(line)
            line = n
        else:
            line = cand
    if line:
        lines.append(line)
    return "\n".join("\t%s" % l for l in lines)


def set_name(ident, majors):
    """StatActivityPG13 / StatActivityPG14to18 for one column set."""
    base = ident[len("View"):]
    lo, hi = min(majors), max(majors)
    span = str(lo) if lo == hi else "%dto%d" % (lo, hi)
    return "%sPG%s" % (base, span)


def groups_for(matrix, view):
    """[(majors, columns)] for one view: majors grouped by identical column list.

    This is the whole point of the file's shape. One constant per *distinct* set,
    referenced by every major that has it, so "identical on 14 through 17" is
    stated by construction instead of by four copies that may or may not agree.
    """
    majors = sorted(m for m in matrix if view in matrix[m])
    groups = OrderedDict()
    for m in majors:
        groups.setdefault(tuple(matrix[m][view]), []).append(m)
    return [(ms, list(cols)) for cols, ms in groups.items()]


def emit(matrix):
    out = []
    a = out.append
    a("// Code generated by scripts/gen-pgmatrix.py. DO NOT EDIT.")
    a("")
    a("// R2.4's capability matrix as data: per-view column sets per major, for")
    a("// PostgreSQL 13 through 18. Every name here is parsed out of the")
    a("// postgresql.org documentation for that major by the generator named above")
    a("// (its docstring lists the exact pages), so this file is a rendering of the")
    a("// docs rather than a transcription of memory — which is what makes the")
    a("// six-major table R16.1 asks for checkable rather than merely asserted.")
    a("//")
    a("// The shape is deliberate: a named constant per *distinct* column set,")
    a("// referenced by every major that has it, instead of one list per major.")
    a("// R2.4's requirement is that this be data and not scattered `if` statements,")
    a("// and the defect class it names is a rename table that loses columns")
    a("// (`pg_stat_bgwriter at PG17 shrinks from 11 columns to 4 … while checkpoint")
    a("// stats move to a new, separate view`). Copy-pasting one list per major")
    a("// reproduces that defect: a typo in one major's copy reads as a real")
    a("// per-major difference. Sharing a constant between the majors that share a")
    a("// shape states the equivalence, so a difference can only exist where the")
    a("// table says it does. matrix_test.go asserts the equivalences and the")
    a("// differences; succession.go carries the rename/move/split cases.")
    a("")
    a("package pgversion")
    a("")

    views = sorted({v for per in matrix.values() for v in per})
    idents = {v: go_ident(v) for v in views}

    a("// Views names every relation the matrix holds columns for, spelled exactly")
    a("// as PostgreSQL spells it. The names are the map keys the rest of the")
    a("// package and the SQL collectors both use, so a collector asks about")
    a("// pgversion.ViewStatWAL rather than carrying its own string.")
    a("//")
    a("// Two names here are worth distinguishing because the spec uses the plural")
    a("// one and the docs document both as separate relations: pg_replication_slots")
    a("// (the slot catalog: slot_name, slot_type, restart_lsn, …) and")
    a("// pg_stat_replication_slots (the logical-replication statistics view:")
    a("// spill/stream counters). Conflating them is the same class of error as the")
    a("// bgwriter/checkpointer one R2.4 calls out.")
    a("const (")
    width = max(len(i) for i in idents.values())
    for v in views:
        a("\t%-*s View = %s" % (width, idents[v], '"%s"' % v))
    a(")")
    a("")

    a("// Column sets: one constant per distinct set, named <View>PG<majors>; a set")
    a("// shared by four majors is one constant referenced by those four majors.")
    names = {}  # (view, major) -> constant name, so the table below can reference groups
    for v in views:
        for ms, cols in groups_for(matrix, v):
            name = set_name(idents[v], ms)
            for m in ms:
                names[(v, m)] = name
            span = ", ".join(str(m) for m in ms) if len(ms) <= 3 \
                else "%d–%d" % (min(ms), max(ms))
            a("// %s is %s's %d columns on PostgreSQL %s." % (name, v, len(cols), span))
            a("const %s = `%s`" % (name, "\n" + wrap(cols) + "\n"))
            a("")

    a("// columnsByMajor is the matrix: view → major → the column-set constant for")
    a("// that pair. A (view, major) pair absent from the inner map is a major on")
    a("// which the view does not exist — pg_stat_checkpointer at 16, pg_stat_io at")
    a("// 15, pg_stat_wal at 13 — which is why Columns returns (nil, false) rather")
    a("// than an empty slice: an empty column list would generate `SELECT FROM x`.")
    a("var columnsByMajor = map[View]map[int]string{")
    for v in views:
        inner = ", ".join("%d: %s" % (m, names[(v, m)])
                          for m in sorted(m for m in matrix if v in matrix[m]))
        a("\t%s: {%s}," % (idents[v], inner))
    a("}")
    a("")
    return "\n".join(out)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--cache", help="directory of previously fetched pages, named <slug>.html")
    ap.add_argument("-o", "--output", help="write here instead of stdout")
    args = ap.parse_args()

    text = emit(collect(args.cache))
    if args.output:
        with open(args.output, "w", encoding="utf-8") as fh:
            fh.write(text)
    else:
        sys.stdout.write(text)


if __name__ == "__main__":
    main()
