#!/usr/bin/env python3
"""Generate internal/collector/pgversion/matrix_data.go — R2.4's matrix as data.

    python3 scripts/gen-pgmatrix.py                      # fetch, write to stdout
    python3 scripts/gen-pgmatrix.py --cache DIR           # read DIR/<slug>.html
    python3 scripts/gen-pgmatrix.py > internal/collector/pgversion/matrix_data.go

Why a generator and not a hand-written table: the matrix holds two thousand-odd
column names across six majors, and the defect class R2.4 exists to prevent is a
*transcription* error in exactly such a table ("11 columns" written where the view
has 12). Every name here is parsed out of the postgresql.org documentation for that
major, so the Go file is a rendering of the docs rather than a transcription of
someone's memory — which is the only way the six-major table R16.1 asks for is
checkable at all.

Counts are deliberately not typed here. The exact view count, distinct-set count
and column-name total are computed from the parsed matrix and written into the
generated header (see emit), because a count written in prose is a count nobody
re-sums: this docstring said "~1,100" while the table it describes held 2,189, and
the same wrong number had been copied into matrix_test.go and IMPLEMENTATION.md.

Sources are the per-major documentation pages listed in PAGES (the reference set
postgres.org publishes for each major: the statistics-collector chapter, the
pg_stat_statements reference, and the catalog pages for the non-stats relations).
--cache exists so the fetch can happen once into a directory and the generation be
re-run offline; without it every page is fetched from the URL.
"""

import argparse, os, re, sys, urllib.request
from collections import OrderedDict

BASE = "https://www.postgresql.org/docs/{major}/{page}"

# The documentation pages every one of the six majors publishes, as
# (page, cache slug) pairs. The page, not the file name, is the unit: one page
# documents several views (monitoring-stats.html documents pg_stat_activity,
# pg_stat_archiver, …; pgstatstatements.html documents both pg_stat_statements
# and pg_stat_statements_info; view-pg-replication-slots.html documents
# pg_replication_slots). The slug is the file name --cache looks for.
BASE_PAGES = (
    ("monitoring-stats.html", "pg%s"),
    ("pgstatstatements.html", "pgs%s"),
    ("view-pg-settings.html", "set%s"),
    ("view-pg-replication-slots.html", "slot%s"),
    ("view-pg-locks.html", "v%s-locks"),
    ("view-pg-file-settings.html", "v%s-file-settings"),
    ("catalog-pg-database.html", "cat%s-database"),
    ("catalog-pg-extension.html", "ext%s"),
)

# Pages the docs publish for some majors and not others, keyed by major.
#
# This exists because the majors do not all document the same set of relations,
# and a page that 404s is not a parse failure to tolerate silently — it is the
# difference between "this major has no such view" and "the fetch broke".
# pg_wait_events is the case: view-pg-wait-events.html answers 200 for 17 and 18
# and 404 for 13, 14, 15 and 16, and R4.3's own parenthetical — "Use
# `pg_wait_events` (PG17+)" — is the spec saying the same thing. Before this
# entry the generator could not emit the view at all, which made the table's
# pg_wait_events row the one piece of hand-written content in a file whose first
# line says DO NOT EDIT (added in f5bb41c), and meant any regeneration silently
# reverted it.
EXTRA_PAGES = {
    17: (("view-pg-wait-events.html", "vwe%s"),),
    18: (("view-pg-wait-events.html", "vwe%s"),),
}

# (major, page, cache slug) for every documentation page we read.
PAGES = [
    (m, page, slug)
    for m in range(13, 19)
    for page, slug in BASE_PAGES + tuple(EXTRA_PAGES.get(m, ()))
]

# Extra prose for a column-set constant, keyed by the constant's name.
#
# The generated docblock is one line — "<Name> is <view>'s N columns on
# PostgreSQL <majors>" — and every fact in that line is derived from the table.
# Some constants carry a fact the table cannot derive. pg_wait_events exists on 17
# and 18 alone and R4.3's "(PG17+)" is why the activity collector reads it
# conditionally rather than declaring it in the set that drives its applicability;
# that prose is about the *consumer*, and no amount of parsing the docs page
# produces it.
#
# It lived in matrix_data.go as a hand-written comment, which is a comment in a
# file whose first line says DO NOT EDIT: the first regeneration after f5bb41c
# would have deleted it. Keying it here is what makes hand prose survive a
# regeneration — the alternative is prose that silently vanishes the first time
# someone regenerates the table for another reason, which is how f5bb41c's wrong
# column names survived too.
DOC_NOTES = {
    "WaitEventsPG17to18": (
        "//",
        "// R4.3 is the requirement that names this view: \"Use `pg_wait_events` (PG17+) to",
        "// validate and label event names on versions that have it.\" The parenthetical is",
        "// the reason the row is keyed 17 and 18 and not 13–18 — the view does not exist",
        "// before PG17 — and it is why the activity collector reads it conditionally",
        "// rather than declaring it in the set that drives its applicability:",
        "// AppliesToViews is the all-views rule, so a view that exists on two majors would",
        "// make the collector inapplicable on the four majors where pg_stat_activity is",
        "// perfectly readable.",
        "//",
        "// The view is a catalogue, not a statistic: it lists the event names the running",
        "// server can report, so its rows are the legal values of pg_stat_activity's",
        "// wait_event_type and wait_event columns. That is what makes it a validator — an",
        "// event string on a session row that is not in this catalogue came from a server",
        "// this matrix does not agree with, which is the producer/consumer disagreement",
        "// R16.8 files and R4.3 names a use for.",
    ),
}

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


# The two index tables monitoring-stats.html publishes — "Dynamic Statistics
# Views" and "Collected Statistics Views" — list every statistics view the
# chapter documents, and their description column says outright which ones are
# copies: "pg_stat_user_tables — Same as pg_stat_all_tables, except that only
# user tables are shown." The detail tables the parser above reads are titled
# after the *all_* member of each family only, so without this the matrix holds
# pg_stat_all_tables and not the user/sys members that share its columns. That is
# how pg_stat_user_tables — the view R7.2's bloat estimate and R7.4's autovacuum
# insight both read (requirements.md:613) — was missing from a file whose whole
# argument is that it is a rendering of the docs.
INDEX_TABLES = ("Dynamic Statistics Views", "Collected Statistics Views")
SAME_AS = re.compile(r'Same as\s+(?:<a[^>]*>)?<code class="structname">(\w+)</code>')


def aliases_in(html):
    """{alias view: source view} for the copies the chapter's index tables name.

    Only the "Same as X" wording becomes an alias. "Similar to X, but counts
    actions taken so far within the current transaction" — the pg_stat_xact_*
    family — is a *different* column set described in similar words (10 columns
    against pg_stat_all_tables' 24), and an alias built on that wording would give
    pg_stat_xact_user_tables a column set the docs never state for it.
    """
    out = OrderedDict()
    for table in INDEX_TABLES:
        m = re.search(r'<table class="table"[^>]*?summary="%s"[^>]*>(.*?)</table>'
                      % re.escape(table), html, re.S)
        if not m:
            continue
        for row in re.split(r"<tr[ >]", m.group(1))[1:]:
            cells = re.findall(r"<td>(.*?)</td>", row, re.S)
            if len(cells) < 2:
                continue
            name = re.search(r'<code class="structname">(\w+)</code>', cells[0])
            src = SAME_AS.search(cells[1])
            if name and src:
                out[name.group(1)] = src.group(1)
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
    """(matrix, aliases) across every page in PAGES.

    matrix is {major: {view: [columns]}}. aliases is {copy view: source view}, the
    union over majors of the aliases_in pairs that were actually merged — the
    pairs skipped below (source has no columns on that page) are skipped here too,
    so the map never names a pair the table does not contain. emit renders it as
    documentedCopies, which is what tells Validate a shared column set is the
    docs' own statement rather than a hand edit.
    """
    matrix = {}
    aliases = OrderedDict()

    def merge(major, view, cols):
        """Write one (major, view) column set, refusing a disagreement.

        Two pages documenting the same view with different columns. The near-miss
        that is *not* a conflict is pg_replication_slots (the slot catalog view)
        vs pg_stat_replication_slots (the logical replication statistics view):
        different views, different names, so they land on different keys. A real
        conflict here is a docs change or a parser bug — loud, not silent. It is a
        closure rather than a second copy of the check because the detail tables
        and the index-table aliases both write through it, and an alias check
        written separately from the table check is the point where the two can
        start disagreeing about what a conflict is.
        """
        prev = matrix.setdefault(major, {}).get(view)
        if prev is not None and prev != cols:
            raise SystemExit(
                "pg%d: %s documented twice with different columns (%d vs %d)"
                % (major, view, len(prev), len(cols)))
        matrix[major][view] = cols

    for major, page, slug in PAGES:
        html = fetch(major, page, slug, cache)
        tables = tables_in(html)
        for view, cols in tables.items():
            merge(major, view, cols)
        # Documented copies (see aliases_in): the index table says
        # "pg_stat_user_tables — Same as pg_stat_all_tables", so the copy takes
        # the source's columns from the same page. An alias whose source has no
        # columns on this page is skipped, not emitted empty —
        # pg_stat_xact_all_tables is listed in the index and documented in prose
        # only, so its two aliases have nothing to inherit here, and an empty
        # column list is the `SELECT FROM x` that Columns() exists to avoid.
        for alias, src in aliases_in(html).items():
            cols = tables.get(src)
            if cols:
                merge(major, alias, cols)
                aliases[alias] = src
    return matrix, aliases


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


def counts(matrix):
    """(views, distinct_sets, total_names, [(major, names), …]) for one matrix.

    Computed rather than typed, and emitted into the header, because a count that
    lives in prose is a count nobody re-sums: this file's docstring, the generated
    table and IMPLEMENTATION.md all said ~1,100 column names while the table held
    2,189 (matrix_test.go's own per-major goldens sum to it). One computation, in
    the tool that builds the table, is the only version of the number that can
    disagree with the table only by being wrong.
    """
    views = sorted({v for per in matrix.values() for v in per})
    sets = sum(len(groups_for(matrix, v)) for v in views)
    total = sum(len(cols) for per in matrix.values() for cols in per.values())
    per_major = [(m, sum(len(cols) for cols in matrix[m].values())) for m in sorted(matrix)]
    return views, sets, total, per_major


def emit(matrix, aliases):
    out = []
    a = out.append

    views, sets, total, per_major = counts(matrix)
    a("// Code generated by scripts/gen-pgmatrix.py. DO NOT EDIT.")
    a("")
    a("// Counted at generation time, not typed: %d views, %d distinct column sets,\n"
      "// %s column names across the majors it covers (%s)."
      % (len(views), sets, "{:,}".format(total),
         ", ".join("PG%d %d" % (m, n) for m, n in per_major)))
    a("//")
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
    a("//")
    a("// Views the docs define by reference — the index tables' \"Same as")
    a("// pg_stat_all_tables, except that only user tables are shown\" family — are")
    a("// here too, with the source's columns, and documentedCopies at the end of")
    a("// this file records the pair so Validate can tell a licensed copy from a")
    a("// hand edit. See the generator's aliases_in for what does and does not")
    a("// become a copy.")
    a("")
    a("package pgversion")
    a("")

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
            # Hand prose that the table itself cannot produce (see DOC_NOTES). Without
            # this the generator overwrites it on the next run, which is how a
            # DO-NOT-EDIT file ends up with a comment in it that nobody can regenerate.
            for note in DOC_NOTES.get(name, ()):
                a(note)
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
        # gofmt aligns a run of key-value lines by padding the *cell*, and for a map
        # entry the cell ends after the colon — so the spaces go between ":" and the
        # value, not between the key and the colon (that shape is what a const block
        # uses, and the two are not interchangeable). Width is the same max ident
        # length the View const block above pads to. Without this the generator's
        # output needs a manual `gofmt` pass, and "regenerate and commit" stops being
        # reproducible: ~30 lines of the diff are the formatter's work rather than the
        # command the header names. The check is that `gofmt -l` on the generated file
        # is empty.
        a("\t%s:%s {%s}," % (idents[v], " " * (width - len(idents[v])), inner))
    a("}")
    a("")

    # The licensed copies. Same alignment rule as the map above: pad after the
    # colon to the longest key in *this* block, not to the View const block's
    # width — gofmt aligns each contiguous run separately and the two differ.
    if aliases:
        a("// documentedCopies maps each view the docs define by reference to the view")
        a("// whose columns it copies: the chapter's index tables state the equivalence")
        a("// in words (\"pg_stat_user_tables — Same as pg_stat_all_tables, except that")
        a("// only user tables are shown\") and publish no column list of their own, so")
        a("// the copy's columns are the source's by the docs' own sentence.")
        a("//")
        a("// It exists for Validate. A column-set string under two view names is")
        a("// otherwise the copy-paste the shape above exists to prevent, and with the")
        a("// copies in the table that reading is a false positive once per copy; this")
        a("// map is the data that says which shares are licensed. The pg_stat_xact_*")
        a("// family is absent because the docs describe it as \"Similar to … but counts")
        a("// actions taken so far within the current transaction\" — a different column")
        a("// set in similar words, so there is nothing to copy. See the generator's")
        a("// aliases_in for the wording rule.")
        cw = max(len(idents[k]) for k in aliases)
        a("var documentedCopies = map[View]View{")
        for k in sorted(aliases):
            a("\t%s:%s %s," % (idents[k], " " * (cw - len(idents[k])), idents[aliases[k]]))
        a("}")
        a("")
    return "\n".join(out)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--cache", help="directory of previously fetched pages, named <slug>.html")
    ap.add_argument("-o", "--output", help="write here instead of stdout")
    args = ap.parse_args()

    matrix, aliases = collect(args.cache)
    text = emit(matrix, aliases)
    if args.output:
        with open(args.output, "w", encoding="utf-8") as fh:
            fh.write(text)
    else:
        sys.stdout.write(text)


if __name__ == "__main__":
    main()
