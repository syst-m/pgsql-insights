// Package query is pgsi's query engine (R3): the SQL normalizer and the
// fingerprint built on it (R3.4, R3.5, R3.6), which Phase 3's statement
// collector writes through and the query store keys on.
//
// This file is the Phase 3 normalizer and nothing else — no store, no top-N
// ranking, no metrics. Those are the phase's other deliverables and each cites
// its own requirement id from here:
//
//	R3.4  fingerprinting — the template plus the fingerprint (Fingerprint here,
//	      because the fingerprint is a pure function of the template this file
//	      produces and splitting it into its own file would separate a value
//	      from the only thing it is defined over).
//	R3.5  the normalization rules themselves (Normalize).
//	R3.6  the query-text length profiles and the truncation flag (the Length
//	      option, ValidLength, and Normalized.DisplayText/Truncated).
//
// R3.9 (percentiles), R3.10 (plan samples), R3.11 (plan changes) and R3.12
// (tags) are Phase 6b per §7.2, so this file deliberately does not implement
// them. R3.12 matters here only as a constraint: it promotes tags out of a
// `/* pgsi:… */` SQL comment, and this normalizer strips comments from the
// template, so 6b must parse tags from the *raw* text (which R3.4 says the
// store keeps) and not from the template.
package query

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// R3.6's query-text length profiles, stated as constants so the flag, the
// config default and the fixture all name the same four numbers.
//
// R3.6's sentence is: "Two profiles, matching both Cloud SQL editions rather
// than picking one: range 256–100 000 bytes, default 1 024 (Cloud SQL
// Enterprise's exact queryStringLength default), with 10 000 documented as the
// Enterprise Plus default and available as the recommended production setting."
// The 0.1.0 defect R3.6 records — shipping Enterprise's 256–4 500 range while
// §1.2 declared Enterprise Plus the baseline — is why the constants are four
// named values and not a min/max pair with one default.
const (
	// MinQueryStringLength is the low end of R3.6's range (256 bytes).
	MinQueryStringLength = 256
	// DefaultQueryStringLength is R3.6's default (1 024 bytes, Cloud SQL
	// Enterprise's exact queryStringLength).
	DefaultQueryStringLength = 1024
	// EnterprisePlusQueryStringLength is R3.6's Enterprise Plus default
	// (10 000 bytes), which the requirement also names as "the recommended
	// production setting". It is a named constant rather than only a documented
	// number so a config can offer it without a caller typing a literal.
	EnterprisePlusQueryStringLength = 10000
	// MaxQueryStringLength is the high end of R3.6's range (100 000 bytes).
	MaxQueryStringLength = 100000
)

// ValidLength reports whether n is inside R3.6's 256–100 000 range.
//
// It is exported because R3.6's range is a *configuration* rule and the config
// layer (R11.8) needs to reject `--query-string-length=100` with an error
// rather than silently clamp it — the same stance R8.10 takes for a `limit`
// above its max. Normalize itself is more permissive than ValidLength (see
// Options.withDefaults) so a caller can normalize at any length it likes; the
// range is what a flag parser enforces.
func ValidLength(n int) bool {
	return n >= MinQueryStringLength && n <= MaxQueryStringLength
}

// Options configures one Normalize call.
type Options struct {
	// Length is R3.6's queryStringLength: how many bytes of the template are
	// kept for storage and display. Zero selects DefaultQueryStringLength
	// (1 024). Values outside ValidLength are accepted here and rejected by
	// the config layer, so a library caller can normalise at 64 bytes for a
	// test while the shipped default stays R3.6's.
	Length int
}

// withDefaults resolves the zero-means-default rule in one place.
func (o Options) withDefaults() Options {
	if o.Length <= 0 {
		o.Length = DefaultQueryStringLength
	}
	return o
}

// Normalized is the result of normalizing one statement: everything R3.4 says
// the store keeps, plus the fingerprint R9.5b makes the metric label.
type Normalized struct {
	// Template is the full normalized statement — literals replaced, whitespace
	// collapsed, keywords cased (R3.4's "literal-stripped, whitespace-collapsed,
	// keyword-cased template", which is R3.5's rule set applied).
	Template string
	// Fingerprint is Fingerprint(Template): the canonical key R3.4 names and
	// R9.5b turns into the per-query family's identifier label.
	Fingerprint string
	// DisplayText is Template cut to Options.Length bytes. R3.4 says the store
	// keeps "the template plus the first-seen raw text truncated per R3.6", and
	// R3.6's title is "Query text length" with "Longer text is truncated and the
	// truncation flagged" — so the truncation applies to the stored text while
	// the identity (Fingerprint) is computed over the whole template. Hashing
	// the truncated form instead would give two queries that agree on their
	// first 1 024 bytes and differ after it one fingerprint, which is a
	// grouping bug R3.4's whole cross-database argument is against. Flagged for
	// A3 as a wording question: R3.6 does not say which of the two the cap
	// applies to, and this is the reading that keeps R3.4's key injective.
	DisplayText string
	// Truncated is R3.6's "the truncation flagged": true when DisplayText is
	// shorter than Template.
	Truncated bool
	// Params is how many literal placeholders the template contains. Not named
	// by R3.5 but free to compute while tokenizing, and R3.10 needs it: a
	// template with placeholders is a parameterized statement, and R3.10(b)
	// says those get PREPARE + EXPLAIN EXECUTE rather than a plain EXPLAIN.
	Params int
}

// Normalize applies R3.5's normalization rules to one statement and returns the
// template, its fingerprint, and R3.6's truncated display form.
//
// R3.5's own text is one rule and one note:
//
//	R3.5 — Mirror Cloud SQL's normalization rules for the stored template:
//	       literals → `$1`,`$2`,… for PG 11+. *(Cloud SQL's `?` form for PG ≤10
//	       is noted in `cloud-sql-insights.md` §3.5 but is dead code inside a
//	       PG15–18 spec — documented as a note, not a requirement.)*
//
// The `?` spelling is therefore not implemented: R3.4's product supports PG
// 15–18 (R13.9), all of which are "PG 11+", so `$n` is the only form. The
// research doc's worked example — cloud-sql-insights.md §3.5, an UPDATE of
// "demo_customer" — is fixture case `update-set-demo-customer` and is the only
// normalization output Google documents, so it is the one case this
// implementation is checked against the source rather than against itself.
//
// R3.4 adds the other two rules the requirement's short form omits: the
// template is *whitespace-collapsed* and *keyword-cased*. Both are implemented
// here because R3.4 names them in the same sentence as the fingerprint.
//
// The empty statement normalizes to an empty template with a fingerprint over
// the empty string. A background session (autovacuum, walsender, or our own
// pgsi-collector session per R2.8) has no query text, and R3.4 gives no rule
// for it; returning an empty template rather than an error is what lets the
// collector store such a row instead of dropping it, and R1.3's `—`-for-unknown
// rule is the precedent for rendering nothing rather than inventing something.
func Normalize(sql string, opts Options) Normalized {
	opts = opts.withDefaults()
	toks := tokenize(sql)
	template := join(toks)

	n := Normalized{
		Template:    template,
		Fingerprint: Fingerprint(template),
		Params:      countPlaceholders(toks),
	}
	n.DisplayText, n.Truncated = truncate(template, opts.Length)
	return n
}

// Fingerprint is R3.4's "our own normalized fingerprint": the lowercase hex
// SHA-256 of the template.
//
// The shape is not invented here — api/openapi.yaml already publishes one
// (R8.11's series response example, `"fingerprint":
// "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`), which
// is 64 lowercase hex characters, i.e. a SHA-256 digest. Matching it matters
// because R8.2 puts the value in a URL path segment
// (`/insights/queries/{fingerprint}`), so it has to be URL-safe, and R9.5b puts
// it in a Prometheus label, where cardinality is the budget and a fixed-length
// digest is what keeps the per-query family at its documented 300 series per
// cluster instead of one variable-length string per query.
//
// It hashes the template and not the raw statement text (R3.4's "the
// fingerprint is the canonical key"): two statements that differ only in their
// literals must share a fingerprint, which is the entire point of normalizing
// before keying.
func Fingerprint(template string) string {
	sum := sha256.Sum256([]byte(template))
	return hex.EncodeToString(sum[:])
}

// truncate cuts s to at most n bytes and reports whether it cut anything.
//
// R3.6 counts in bytes ("range 256–100 000 bytes"), so the cut is a byte cut,
// but the cut backs off to a UTF-8 rune boundary: a template truncated
// mid-rune puts an invalid byte sequence into a JSON string, and encoding/json
// replaces invalid UTF-8 with U+FFFD on marshal, which means the stored text
// would not be a prefix of the template any more. Backing off keeps the
// invariant that DisplayText is always a prefix of Template.
//
// The consequence worth knowing: a cut can land inside a `$12` placeholder and
// leave `… $1`. That is what "the text was truncated" looks like when the cap
// is measured in bytes, and R3.6's answer is the Truncated flag rather than a
// smarter cut.
func truncate(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := n
	// Walk back off a multi-byte rune's continuation bytes (10xxxxxx).
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut], true
}

// tokenKind classifies one lexical token of a statement.
type tokenKind uint8

const (
	tokWord  tokenKind = iota // an identifier or a SQL keyword
	tokParam                  // a literal replaced by `$n`, or a `$n` already present
	tokPunct                  // an operator or punctuation
)

// token is one lexical token. text holds the exact text to emit for the token:
// the uppercased keyword, the identifier as written, the operator, or the
// `$n` placeholder a literal became.
//
// spaced records whether the source had whitespace immediately before this
// token, and it is the reason join can collapse runs without reformatting.
// R3.4's rule is “whitespace-collapsed” — a rule about *runs* of whitespace, not
// a licence to re-derive every space from the token stream. A formatter that
// inserts or removes spaces turns `INSERT INTO t (a, b)` into `INSERT INTO
// t(a, b)` and `count(*)` into `count ( * )`, and neither string is what the
// statement said. Carrying the flag is what lets join collapse `a   b` to `a b`
// while leaving `count(*)` and `t (a, b)` exactly as far apart as they were.
type token struct {
	kind   tokenKind
	text   string
	spaced bool
}

// tokenize splits a statement into tokens, replacing literals with numbered
// placeholders and dropping comments along the way.
//
// The order of the checks is the whole correctness of R3.5, because each one
// exists to stop a preceding rule from eating something it must not:
//
//   - A line comment is checked before the `-` operator, or `-- x` tokenizes as
//     a minus followed by a word.
//   - A block comment is checked before the `/` operator, and PG block comments
//     nest (unlike C's), so skipBlockComment counts depth.
//   - A dollar-quoted string is checked before the `$n` parameter form, or
//     `$tag$body$tag$` reads as a parameter named `$tagbody…`.
//   - A `$n` parameter is checked before the lone-`$` operator, and it is
//     *preserved* rather than renumbered: pg_stat_statements stores already
//     parameterized statements as `… $1 …`, and renumbering one would make the
//     same prepared statement produce a different template depending on how
//     many literals happened to precede it in this one call.
//   - A single-quoted string is checked before the `'` character is ever
//     treated as punctuation, and `”` inside it is an escaped quote, not the
//     end of the literal.
//   - A number is checked before the word rule, so `1e10` is one literal and
//     not the number `1` followed by the identifier `e10`.
//   - A *signed* number is checked before a plain one, and only where the `-` is
//     a unary sign (see unaryMinus), so `x = -1` becomes `x = $1` and not the
//     `x = - $1` a tokenizer that treats every `-` as an operator produces.
func tokenize(sql string) []token {
	var toks []token
	params := 0 // the highest placeholder number emitted so far
	space := false

	// emit appends one token and carries the pending whitespace flag with it, so
	// no branch below has to remember to set it.
	emit := func(t token) {
		t.spaced = space
		toks = append(toks, t)
		space = false
	}

	for i := 0; i < len(sql); {
		c := sql[i]

		// Whitespace: one flag per run, which is R3.4's "whitespace-collapsed"
		// half — every run of spaces, tabs and newlines between two tokens
		// becomes exactly one space at join time.
		if isASCIISpace(c) {
			space = true
			i++
			continue
		}

		// Line comment. PG's lexer treats a comment as a token separator, so it
		// sets the whitespace flag rather than vanishing: `SELECT/*x*/1` has a
		// separator between its two words and must not join to `SELECT$1`.
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			space = true
			if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				i = len(sql)
			}
			continue
		}

		// Block comment (nesting, per PG). An unterminated one consumes the rest.
		if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			space = true
			i = skipBlockComment(sql, i)
			continue
		}

		// Single-quoted string literal → placeholder.
		if c == '\'' {
			j, _ := scanSingleQuoted(sql, i)
			params++
			emit(token{kind: tokParam, text: placeholder(params)})
			i = j
			continue
		}

		// Double-quoted identifier → kept verbatim, quotes included. In PG
		// double quotes mean an identifier, not a string, so `"demo_customer"`
		// is a name and `'demo_customer'` is a literal; normalizing the first
		// to `$1` would delete a column name from the template.
		if c == '"' {
			if j, ok := scanDoubleQuoted(sql, i); ok {
				emit(token{kind: tokWord, text: sql[i:j]})
				i = j
				continue
			}
		}

		// Dollar-quoted string → placeholder; `$n` parameter → preserved.
		if c == '$' {
			if j, text, ok := scanDollar(sql, i); ok {
				if isDigit(sql[i+1]) {
					// `$n`: a placeholder this statement already had. Preserve
					// the text and let it set the counter, so a later literal
					// continues the numbering instead of colliding with it.
					emit(token{kind: tokParam, text: text})
					if n, err := strconv.Atoi(text[1:]); err == nil && n > params {
						params = n
					}
					i = j
					continue
				}
				// `$tag$…$tag$`: a string literal.
				params++
				emit(token{kind: tokParam, text: placeholder(params)})
				i = j
				continue
			}
		}

		// A signed numeric literal: `-1` is one literal when the `-` is a unary
		// sign, and an operator plus a literal when it is subtraction.
		if c == '-' && unaryMinus(toks) {
			if j := scanNumber(sql, i+1); j > i+1 {
				params++
				emit(token{kind: tokParam, text: placeholder(params)})
				i = j
				continue
			}
		}

		// Numeric literal → placeholder.
		if isDigit(c) {
			j := scanNumber(sql, i)
			params++
			emit(token{kind: tokParam, text: placeholder(params)})
			i = j
			continue
		}

		// Identifier or keyword.
		if isWordStart(c) || isMultiByteLetter(sql, i) {
			j := scanWord(sql, i)
			w := sql[i:j]
			// R3.4's "keyword-cased" half: keywords are uppercased, identifiers
			// keep the case they were written in. Casing identifiers too would
			// merge `SELECT * FROM Orders` and `SELECT * FROM orders` into one
			// fingerprint on a case-sensitive catalog where they are two tables.
			if isKeyword(w) {
				w = strings.ToUpper(w)
			}
			emit(token{kind: tokWord, text: w})
			i = j
			continue
		}

		// Operator, longest match first, so `<=` is one token and not `<` + `=`.
		if op := longestOperator(sql, i); op != "" {
			emit(token{kind: tokPunct, text: op})
			i += len(op)
			continue
		}

		// Anything else (a stray byte we cannot classify) is copied verbatim
		// rather than dropped: losing a byte the normalizer did not understand
		// is how a template stops being a faithful rendering of the statement.
		_, size := utf8.DecodeRuneInString(sql[i:])
		emit(token{kind: tokPunct, text: sql[i : i+size]})
		i += size
	}

	return toks
}

// join renders tokens back into one line: every run of whitespace between two
// tokens becomes exactly one space, and no space is inserted where the source
// had none (R3.4's "whitespace-collapsed").
//
// It is deliberately not a formatter. Re-deriving spacing from the token stream
// is what turns `INSERT INTO t (a, b)` into `INSERT INTO t(a, b)` and
// `count(*)` into `count ( * )`, and R3.4 asks for collapse, not for a style.
// The inputs this matters for are already well-formatted — pg_stat_statements
// stores PG's ruleutils rendering, not whatever the client typed — so the
// collapse is doing real work on indentation and line breaks while leaving the
// single spaces PG emitted exactly where PG put them.
func join(toks []token) string {
	if len(toks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(toks[0].text)
	for i := 1; i < len(toks); i++ {
		if toks[i].spaced {
			b.WriteByte(' ')
		}
		b.WriteString(toks[i].text)
	}
	return b.String()
}

// countPlaceholders counts the `$n` tokens, which is Normalized.Params.
func countPlaceholders(toks []token) int {
	n := 0
	for _, t := range toks {
		if t.kind == tokParam {
			n++
		}
	}
	return n
}

// placeholder is the `$n` text R3.5 specifies for a literal.
func placeholder(n int) string { return "$" + strconv.Itoa(n) }

// isKeyword reports whether w is a SQL keyword (case-insensitively), which is
// what decides whether it gets uppercased.
func isKeyword(w string) bool {
	if w == "" || !isASCIILetter(w[0]) {
		return false // a quoted identifier or a `$…` never is a keyword
	}
	_, ok := keywords[strings.ToUpper(w)]
	return ok
}

// keywords is the word list R3.4's "keyword-cased" applies to.
//
// It is a closed set of PostgreSQL's reserved words plus the type names and the
// CNPG-relevant verbs, not an exhaustive grammar. Two membership rules keep it
// from doing damage:
//
//   - Aggregate and function names are *not* in it (count, sum, max, now,
//     pg_stat_statements). They are not keywords in PG's grammar, and uppercasing
//     them would rename a user's column called `count` to `COUNT`.
//   - Words that are keywords in some contexts and identifiers in others (user,
//     name, type, key, value) are included only where PG's own grammar reserves
//     them (USER, KEY) and excluded where it does not (name, type, value). The
//     cost of getting one of these wrong is a merged fingerprint, and the cost
//     of over-including is that a column named `value` renders as `VALUE`.
var keywords = map[string]struct{}{
	// Query verbs and clause keywords.
	"SELECT": {}, "FROM": {}, "WHERE": {}, "INSERT": {}, "INTO": {}, "VALUES": {},
	"UPDATE": {}, "SET": {}, "DELETE": {}, "ALL": {}, "DISTINCT": {}, "AS": {},
	"JOIN": {}, "INNER": {}, "LEFT": {}, "RIGHT": {}, "FULL": {}, "OUTER": {},
	"CROSS": {}, "NATURAL": {}, "ON": {}, "GROUP": {}, "BY": {}, "ORDER": {},
	"HAVING": {}, "LIMIT": {}, "OFFSET": {}, "FETCH": {}, "NEXT": {}, "ROW": {},
	"ROWS": {}, "ONLY": {}, "UNION": {}, "INTERSECT": {}, "EXCEPT": {},
	"AND": {}, "OR": {}, "NOT": {}, "NULL": {}, "IS": {}, "ASC": {}, "DESC": {},
	"NULLS": {}, "FIRST": {}, "LAST": {}, "FOR": {}, "SHARE": {}, "EXISTS": {},
	"IN": {}, "BETWEEN": {}, "LIKE": {}, "ILIKE": {}, "SIMILAR": {}, "TO": {},
	"CASE": {}, "WHEN": {}, "THEN": {}, "ELSE": {}, "END": {}, "CAST": {},
	"DEFAULT": {}, "WITH": {}, "RECURSIVE": {},
	// DDL and session verbs.
	"CREATE": {}, "ALTER": {}, "DROP": {}, "TABLE": {}, "INDEX": {}, "VIEW": {},
	"SCHEMA": {}, "DATABASE": {}, "ROLE": {}, "USER": {}, "GRANT": {},
	"REVOKE": {}, "ADD": {}, "COLUMN": {}, "PRIMARY": {}, "FOREIGN": {},
	"REFERENCES": {}, "UNIQUE": {}, "CHECK": {}, "RETURNING": {}, "REPLACE": {},
	"IF": {}, "AUTHORIZATION": {}, "OWNER": {}, "LANGUAGE": {}, "FUNCTION": {},
	"PROCEDURE": {}, "RETURNS": {}, "SECURITY": {}, "BEGIN": {}, "COMMIT": {},
	"ROLLBACK": {}, "ABORT": {}, "START": {}, "TRANSACTION": {},
	"TRUNCATE": {}, "VACUUM": {}, "ANALYZE": {}, "ANALYSE": {}, "CLUSTER": {},
	"REINDEX": {}, "REFRESH": {}, "MATERIALIZED": {}, "COPY": {}, "DO": {},
	"PREPARE": {}, "EXECUTE": {}, "DEALLOCATE": {}, "EXPLAIN": {}, "VERBOSE": {},
	"COSTS": {}, "SETTINGS": {}, "BUFFERS": {}, "TIMING": {}, "FORMAT": {},
	"REASSIGN": {}, "LOCK": {}, "TABLES": {}, "RESET": {},
	// Type names, so `::int` and `AS text` case consistently.
	"INT": {}, "INTEGER": {}, "BIGINT": {}, "SMALLINT": {}, "INT2": {}, "INT4": {},
	"INT8": {}, "NUMERIC": {}, "DECIMAL": {}, "REAL": {}, "DOUBLE": {},
	"PRECISION": {}, "FLOAT": {}, "FLOAT4": {}, "FLOAT8": {}, "BOOLEAN": {},
	"BOOL": {}, "CHAR": {}, "CHARACTER": {}, "VARYING": {}, "VARCHAR": {},
	"DATE": {}, "TIME": {}, "TIMESTAMP": {}, "TIMESTAMPTZ": {}, "INTERVAL": {},
	"UUID": {}, "JSON": {}, "JSONB": {}, "BYTEA": {}, "SERIAL": {},
	"BIGSERIAL": {}, "SMALLSERIAL": {}, "MONEY": {}, "XML": {}, "YAML": {},
	"TEXT": {},
	// Extension names appear as identifiers, not keywords, so pg_stat_statements
	// is left alone: uppercasing it would rename the extension.
}

// scanSingleQuoted returns the index just past the closing quote of the
// single-quoted literal starting at i, and whether it terminated. `”` inside
// the literal is an escaped quote (PG's spelling), not the end.
func scanSingleQuoted(s string, i int) (int, bool) {
	for j := i + 1; j < len(s); {
		if s[j] != '\'' {
			j++
			continue
		}
		if j+1 < len(s) && s[j+1] == '\'' { // escaped quote
			j += 2
			continue
		}
		return j + 1, true
	}
	return len(s), false // unterminated: consume the rest
}

// scanDoubleQuoted returns the index just past the closing `"`, or false when
// the identifier is unterminated (in which case the caller falls through to
// treating `"` as punctuation).
func scanDoubleQuoted(s string, i int) (int, bool) {
	if j := strings.IndexByte(s[i+1:], '"'); j >= 0 {
		return i + 1 + j + 1, true
	}
	return 0, false
}

// scanDollar handles both `$n` parameter placeholders and `$tag$…$tag$`
// dollar-quoted strings, returning the end index and the text to emit.
//
// For `$n` the text is `$n` itself (preserved); for a dollar-quoted string the
// text is the whole `$tag$body$tag$` span, which the caller replaces with a
// placeholder anyway — the span is only needed to find the end.
func scanDollar(s string, i int) (end int, text string, ok bool) {
	// `$n`: a dollar sign immediately followed by digits is a parameter.
	if i+1 < len(s) && isDigit(s[i+1]) {
		j := i + 1
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		return j, s[i:j], true
	}
	// `$tag$…$tag$` — the tag is empty (`$$`) or starts with a letter/underscore.
	j := i + 1
	for j < len(s) && (isASCIILetter(s[j]) || isDigit(s[j]) || s[j] == '_') {
		j++
	}
	if j >= len(s) || s[j] != '$' {
		return 0, "", false
	}
	// The tag is either empty (`$$…$$`) or a name starting with a letter or
	// underscore. `$-` and `$ ` are not dollar quotes and fall through to the
	// lone-`$` operator.
	if j > i+1 && !isASCIILetter(s[i+1]) {
		return 0, "", false
	}
	tag := s[i : j+1]
	k := strings.Index(s[j+1:], tag)
	if k < 0 {
		return 0, "", false
	}
	return j + 1 + k + len(tag), tag, true
}

// unaryMinus reports whether the `-` immediately after the last emitted token is
// a sign on a number rather than the subtraction operator.
//
// It exists because R3.4 wants a *displayable* template, and `WHERE x = - $1` is
// what a naive tokenizer produces for `WHERE x = -1`. PG's own grammar parses
// `-1` as the unary operator applied to `1` and then constant-folds it back into
// one constant, which is why pg_stat_statements' own jumble reports `$1`; the
// rule here matches that result. The test is what precedes the `-`:
//
//   - nothing, a keyword, an operator, or an opening bracket → unary. `SELECT -1`,
//     `x = -1`, `(-1)`, `IN (1, -2)` all have a sign, not a subtraction.
//   - an identifier, a placeholder, or a closing bracket → binary. `a - 1`,
//     `$1 - 2`, `(a) - 1` are subtractions, and swallowing the `-` in `a - 1`
//     would silently turn a subtraction into a negative literal.
func unaryMinus(toks []token) bool {
	if len(toks) == 0 {
		return true // a statement that begins with `-1` has a sign, not a subtraction
	}
	p := toks[len(toks)-1]
	if p.kind == tokParam {
		return false
	}
	if p.kind == tokWord && !isKeyword(p.text) {
		return false
	}
	switch p.text {
	case ")", "]", "}":
		return false
	}
	return true
}

// scanNumber returns the index just past the numeric literal starting at i,
// covering integers, decimals and exponents in one pass so `1e10` is a single
// literal rather than `1` followed by the identifier `e10`.
func scanNumber(s string, i int) int {
	j := i
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	if j < len(s) && s[j] == '.' {
		j++
		for j < len(s) && isDigit(s[j]) {
			j++
		}
	}
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		if k < len(s) && isDigit(s[k]) {
			for k < len(s) && isDigit(s[k]) {
				k++
			}
			j = k
		}
	}
	return j
}

// scanWord returns the index just past the identifier starting at i. PG
// identifiers are `[A-Za-z_][A-Za-z0-9_$]*` plus non-ASCII letters; the `$` is
// consumed here so `foo$bar` is one identifier.
func scanWord(s string, i int) int {
	j := i
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		switch {
		case size == 1 && isASCIIWordByte(s[j]):
			j++
		case size > 1 && unicode.IsLetter(r):
			j += size
		default:
			return j
		}
	}
	return j
}

// skipBlockComment returns the index just past the `*/` closing the block
// comment at i. PG block comments nest, so depth is counted.
//
// It cannot fail: an unterminated `/*` runs to end of input, which is how PG's
// own lexer reads it. Returning the end of the string for that case is what
// stops `SELECT 1 /* never closed` from re-emitting the comment as the
// operators `/` and `*` plus two identifiers.
func skipBlockComment(s string, i int) int {
	depth := 0
	for j := i; j+1 < len(s); {
		switch {
		case s[j] == '/' && s[j+1] == '*':
			depth++
			j += 2
		case s[j] == '*' && s[j+1] == '/':
			depth--
			j += 2
			if depth == 0 {
				return j
			}
		default:
			j++
		}
	}
	return len(s)
}

// operators lists the multi-character operators, longest first, so the match is
// greedy: `->>` wins over `->`, and `<=` wins over `<`. Without the ordering,
// `a <= 1` tokenizes as `a`, `<`, `=`, `$1` and joins to `a < = $1`.
var operators = []string{
	"->>", "!~~", "||/", "?-|", "?||", "#>>",
	"<=", ">=", "<>", "!=", "::", "||", "->", "@>", "<@", "?|", "?.", "&&",
	"<<", ">>", "@@", "~*", "^@", "!~", "!<", "!>", "#>", "|-", "|/", "~=",
	"~~", "!~~",
}

// longestOperator returns the operator at i, preferring the longest match.
func longestOperator(s string, i int) string {
	for _, op := range operators {
		if strings.HasPrefix(s[i:], op) {
			return op
		}
	}
	return ""
}

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isASCIIWordByte(c byte) bool {
	return isASCIILetter(c) || isDigit(c) || c == '_' || c == '$'
}

// isWordStart reports whether c can begin an ASCII identifier.
func isWordStart(c byte) bool { return isASCIILetter(c) || c == '_' }

// isMultiByteLetter reports whether the rune at i is a non-ASCII letter, which
// PG allows in a bare identifier (unquoted `café` is a valid column name).
func isMultiByteLetter(s string, i int) bool {
	r, size := utf8.DecodeRuneInString(s[i:])
	return size > 1 && unicode.IsLetter(r)
}
