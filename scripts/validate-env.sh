#!/usr/bin/env bash
#
# scripts/validate-env.sh — R16.5 (requirements.md §4 "R16.5"; §7.2's Phase 2
# row names this file and assigns it to A1; IMPLEMENTATION.md §3.10 step 5
# carries it as "scripts/validate-env.sh (R16.5)").
#
# What this is. §8 lists ten acceptance checks — the "[A]" requirements — and
# R16.5's claim is that the bracket exists only if something scripts them:
# "this is what makes '[A]' requirements actually checkable". This file is that
# something. It runs the checks whose phase has landed, prints one row per
# check, and exits non-zero if any applicable check fails.
#
# Which checks run here, and why the rest do not. §8's checks carry the phase
# that produces the thing they check:
#
#   1  R1.5           Phase 1a  implemented
#   2  R1.2           Phase 1a  implemented
#   3  R9.12          Phase 2   implemented
#   4  R2.4           Phase 2   implemented
#   5  R9.9, R9.11    Phase 4   SKIP — the promtool pair; the rules file is Phase 4's
#   6  R9.9           Phase 4   SKIP — the existing prom-operator scrape
#   7  R11.6/12/13    Phase 5   SKIP — helm goldens + packaged install
#   8  R4.1           Phase 6a  SKIP — active-queries vs pg_stat_activity
#   9  R5.1           Phase 7   SKIP — the five System Insights cards
#   10 R6.4           Phase 8   SKIP — pooler attribution
#
# A deferred check prints SKIP and names its phase. It never prints PASS. That
# is R8.7's rule applied to this script's own output: a probe that answers 200
# with an empty fleet must not read as a healthy fleet, and a check that has not
# been written must not read as a check that passed.
#
# Where the expectations come from. Checks 1, 2 and 4's live half need to know
# what the fleet should look like. They read it from
# testdata/api/clusters.json — the same file internal/api/dto's
# TestGoldenFixtureIsCanonical re-marshals byte-for-byte and
# web/src/api/types.ts imports — not from cluster names retyped in shell. A
# value copied out of the contract into its checker is a second copy of the
# contract; the fixture is the contract, so the script reads the fixture.
#
# Free-text expectations are checked as presence and shape, not as one string
# copied forward: check 2 asserts the freeidp entry carries a non-empty
# phaseReason naming disk space, not a specific sentence, because the sentence
# is CNPG's and CNPG rewords it between releases.
#
# Transports. The API is reached three ways, in this order:
#   --url BASE          curl BASE directly (the only mode that can send --token)
#   --port-forward      kubectl port-forward svc/<ns>/<service>:<port>, then curl
#   (default)           kubectl get --raw service-proxy
# The default cannot send an Authorization header — `kubectl get --raw` has no
# header flag, verified: `error: unknown shorthand flag: 'H' in -H` — so a
# token-gated API answers 401 there. The script reports that as an auth
# requirement and names --url/--token as the fix, rather than reporting a 401 as
# a missing route.
#
# Exit code: 0 if every applicable check passes, 1 otherwise. SKIP does not
# affect the exit code.

set -u

# ---------------------------------------------------------------------------
# defaults and flags
# ---------------------------------------------------------------------------

NAMESPACE="${PGSI_NAMESPACE:-pgsi}"
SERVICE="${PGSI_SERVICE:-pgsi}"
PORT="${PGSI_PORT:-8080}"
BASE_URL="${PGSI_URL:-}"
TOKEN="${PGSI_TOKEN:-}"
USE_PORT_FORWARD=0
ONLY=""
SKIP_BUILD=0
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURE="$ROOT/testdata/api/clusters.json"

usage() {
	cat <<'EOF'
usage: scripts/validate-env.sh [flags]

  --url BASE        API base URL to curl directly (e.g. http://127.0.0.1:18080)
  --token TOKEN     bearer token for the API (needs --url; kubectl --raw cannot send one)
  --namespace NS    namespace of the pgsi deployment  (default: pgsi)
  --service NAME    service name                      (default: pgsi)
  --port PORT       service port                      (default: 8080)
  --port-forward    start `kubectl port-forward svc/<ns>/<service>:<port>` and use it
  --only LIST       run only these check numbers, e.g. --only 1,2
  --skip-build      skip the Go-test half of check 4
  --help            this text

Environment: PGSI_NAMESPACE, PGSI_SERVICE, PGSI_PORT, PGSI_URL, PGSI_TOKEN.
Runs §8's acceptance checks 1-4 (R1.5, R1.2, R9.12, R2.4) and reports 5-10 as
SKIP with the phase that owns them. See the file header for the check table.
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--url) BASE_URL="$2"; shift 2 ;;
	--token) TOKEN="$2"; shift 2 ;;
	--namespace) NAMESPACE="$2"; shift 2 ;;
	--service) SERVICE="$2"; shift 2 ;;
	--port) PORT="$2"; shift 2 ;;
	--port-forward) USE_PORT_FORWARD=1; shift ;;
	--only) ONLY="$2"; shift 2 ;;
	--skip-build) SKIP_BUILD=1; shift ;;
	--help | -h) usage; exit 0 ;;
	*)
		echo "validate-env: unknown flag: $1" >&2
		echo "try: scripts/validate-env.sh --help" >&2
		exit 2
		;;
	esac
done

# ---------------------------------------------------------------------------
# the checker
#
# The JSON and metrics parsing lives in one python program written to a temp
# file at startup, rather than in four heredocs or a second committed file:
# scripts/ keeps its python flat (cardinality-check.py, check-openapi.py,
# gen-pgmatrix.py, …), and a second committed file for four call sites would
# make R16.5 a two-path change where one path is the deliverable.
# ---------------------------------------------------------------------------

PYLIB="$(mktemp -t pgsi-validate-env.XXXXXX.py)"
cat >"$PYLIB" <<'PYEOF'
"""§8 acceptance-check assertions for validate-env.sh (R16.5).

Every subcommand prints one line of evidence on stdout and exits 0 (pass) or
1 (fail); `replication` adds 2 for "deferred", because a check whose phase has
not landed must not exit 0 either. Expectations come from
testdata/api/clusters.json — the DTO golden fixture — so the checker never
carries a second copy of the contract.
"""
import json
import re
import sys


def load(path):
    with open(path) as fh:
        return json.load(fh)


def clusters(fixture_path, live_path):
    """Check 1 (R1.5): the list route returns the fixture's clusters, correctly.

    Compares the set of (namespace, name) pairs and, for each cluster the two
    documents share, the five fields §8 names: phase, instances,
    servingInstances, currentPrimary, pgMajor. readyInstances is compared too
    because the DTO carries it and R1.5's row names it.
    """
    want = load(fixture_path).get("data", [])
    got = load(live_path).get("data", [])
    want_by = {(c.get("namespace"), c.get("name")): c for c in want}
    got_by = {(c.get("namespace"), c.get("name")): c for c in got}

    missing = sorted(set(want_by) - set(got_by))
    extra = sorted(set(got_by) - set(want_by))
    if missing or extra:
        bits = []
        if missing:
            bits.append("missing %s" % ", ".join("%s/%s" % p for p in missing))
        if extra:
            bits.append("unexpected %s" % ", ".join("%s/%s" % p for p in extra))
        print("live set %d vs fixture %d: %s" % (len(got), len(want), "; ".join(bits)))
        return 1

    diffs = []
    for key in sorted(want_by):
        w, g = want_by[key], got_by[key]
        for field in ("phase", "instances", "readyInstances",
                      "servingInstances", "currentPrimary", "pgMajor"):
            if w.get(field) != g.get(field):
                diffs.append("%s/%s %s: live %r fixture %r" %
                             (key[0], key[1], field, g.get(field), w.get(field)))
    if diffs:
        print("; ".join(diffs[:4]))
        if len(diffs) > 4:
            print("(%d field mismatches total)" % len(diffs))
        return 1
    print("%d clusters match the fixture on phase/instances/ready/serving/primary/major"
          % len(got))
    return 0


def freeidp(fixture_path, live_path):
    """Check 2 (R1.2): the crash-looping cluster reports its degraded state.

    §8 requires three things of the freeidp entry: the disk-pressure
    phaseReason, the Pending instance, and servingInstances: 0. The reason is
    checked as a non-empty string naming disk space, not as one sentence copied
    forward — the sentence is CNPG's and CNPG rewords it.
    """
    want = load(fixture_path).get("data", [])
    got = load(live_path).get("data", [])
    w = next((c for c in want if c.get("name") == "freeidp-pg"), None)
    g = next((c for c in got if c.get("name") == "freeidp-pg"), None)
    if g is None:
        print("no freeidp/freeidp-pg entry in the live response")
        return 1

    problems = []
    reason = (g.get("phaseReason") or "").strip()
    if not reason:
        problems.append("phaseReason empty (fixture: %r)" % (w or {}).get("phaseReason"))
    elif "disk" not in reason.lower():
        problems.append("phaseReason does not name disk space: %r" % reason[:70])

    serving = g.get("servingInstances")
    want_serving = (w or {}).get("servingInstances")
    if serving != want_serving:
        problems.append("servingInstances live %r fixture %r" % (serving, want_serving))

    # The Pending instance: the fixture has exactly one instance whose phase is
    # Pending and which carries no role — R1.2's non-serving instance.
    want_pending = [i for i in (w or {}).get("instanceList", [])
                    if i.get("phase") == "Pending"]
    got_pending = [i for i in g.get("instanceList", [])
                   if i.get("phase") == "Pending"]
    if want_pending and not got_pending:
        problems.append("no Pending instance (fixture has %s)" %
                        want_pending[0].get("name"))

    if problems:
        print("; ".join(problems[:3]))
        return 1
    print("phaseReason names disk space; %d Pending instance(s) present; servingInstances=%s"
          % (len(got_pending), serving))
    return 0


def metrics(text):
    """Check 3 (R9.12): pgsi_up{cluster="cookbook-pg"} == 1.

    R9.12 is the per-instance liveness family. §8 names the cookbook-pg series
    because it is the single-instance cluster, so the assertion is on that one
    series and its value, plus a sanity count of the pgsi_ families so an empty
    /metrics cannot pass by having the one line.
    """
    wanted = re.compile(r'^pgsi_up\{[^}]*cluster="cookbook-pg"[^}]*\}\s+(\S+)')
    hit = None
    families = set()
    for line in text.splitlines():
        if line.startswith("# TYPE "):
            families.add(line.split(" ", 2)[1])
        m = wanted.match(line)
        if m and hit is None:
            hit = m.group(1)
    if hit is None:
        print("no pgsi_up{cluster=\"cookbook-pg\"} series in /metrics (%d pgsi_ families)"
              % len(families))
        return 1
    if hit != "1":
        print("pgsi_up{cluster=\"cookbook-pg\"} = %s, want 1" % hit)
        return 1
    print("pgsi_up{cluster=\"cookbook-pg\"}=1; %d pgsi_ families exported" % len(families))
    return 0


def replication(fixture_path, live_path):
    """Check 3's second half (R5.5): cookbook-pg's replication section renders
    "single instance".

    §8's check 3 is one sentence with two halves: the pgsi_up series above
    (R9.12, Phase 2 — this script's own phase) and the replication section
    (R5.5). R5.5 is rowed in §7.2's Phase 7 table (requirements.md:609, and
    :736 states the assignment outright), and the check's own parenthetical
    agrees — "the UI that renders it, R5.5, lands at 7". The API DTO has no
    replication field today: clusters.go's only match for the word is
    certificates.replicationTLSSecret. So this half reports deferred and exits
    2 rather than printing PASS, which is R8.7's rule applied per-half as well
    as per-row. The row's status still comes from the half whose phase has
    landed; this string rides along in the evidence so the row says what it
    actually looked at.

    When the field lands, the assertion below is what runs, and it is written
    now so the day it lands is not the day someone decides what to assert: a
    1-instance cluster's section must carry R5.5's explanation ("not a broken
    chart"), and the expectation of 1 instance comes from the fixture, not from
    §8's prose retyped here.
    """
    want = load(fixture_path).get("data", [])
    live = load(live_path).get("data", [])
    fixture_row = next((c for c in want if c.get("name") == "cookbook-pg"), None)
    live_row = next((c for c in live if c.get("name") == "cookbook-pg"), None)
    if fixture_row is None or live_row is None:
        print("cookbook-pg missing from %s"
              % ("fixture" if fixture_row is None else "live response"))
        return 1
    keys = [k for k in live_row if "replic" in k.lower() and not k.endswith("TLSSecret")]
    if not keys:
        print("replication half deferred to 7 (R5.5: no such field in the DTO yet)")
        return 2
    section = live_row[keys[0]]
    # The field's shape is Phase 7's to choose, so accept the three plausible
    # ones rather than betting on one: a bare string, an object with a summary,
    # or anything else, which is compared as its own JSON.
    if isinstance(section, str):
        rendered = section
    elif isinstance(section, dict):
        rendered = section.get("summary") or section.get("state") or json.dumps(section, sort_keys=True)
    else:
        rendered = json.dumps(section, sort_keys=True)
    if fixture_row.get("instances") == 1 and "single instance" not in rendered.lower():
        print("%s renders %r, want \"single instance\" for a 1-instance cluster (R5.5)"
              % (keys[0], rendered))
        return 1
    print("%s renders %r" % (keys[0], rendered))
    return 0


def live_majors(fixture_path):
    """Check 4's live half: one PG16 and one PG17 instance to probe.

    Prints `major|namespace/pod` entries, space separated, one per major the
    fixture names that has a queryable instance. The shell probes each with
    `psql ... show server_version` and compares the major, which is the
    checkable half of R2.4's "live collection succeeds on PG 16 and PG 17".
    """
    want = load(fixture_path).get("data", [])
    by_major = {}
    for c in want:
        major = c.get("pgMajor")
        # §8 marks freeidp "not reachable for SQL" (zero serving instances), so
        # a cluster with no serving instance is not a probe target.
        if not c.get("servingInstances"):
            continue
        instances = c.get("instanceList", [])
        if not instances:
            continue
        pick = next((i for i in instances if i.get("role") == "primary"), instances[0])
        if major not in by_major:
            by_major[major] = "%s|%s/%s" % (major, c.get("namespace"), pick.get("name"))
    if not by_major:
        print("fixture names no SQL-reachable clusters")
        return 1
    print(" ".join(by_major[m] for m in sorted(by_major)))
    return 0


def main(argv):
    if len(argv) < 2:
        print("usage: validate-env.py {clusters|freeidp|metrics|replication|live-majors} ...",
              file=sys.stderr)
        return 2
    sub = argv[1]
    try:
        # Each subcommand takes its own arity: metrics reads stdin and names no
        # file, live-majors names only the fixture, clusters/freeidp/replication
        # name the fixture plus the live body. A single `len(argv) < 4` guard in
        # front of all of them — which is what this had — made live-majors print
        # the clusters usage line and exit 2, because it passes three argv, not
        # four.
        if sub == "metrics":
            return metrics(sys.stdin.read())
        if sub == "replication":
            if len(argv) < 4:
                print("usage: validate-env.py replication <fixture> <live>", file=sys.stderr)
                return 2
            return replication(argv[2], argv[3])
        if sub == "live-majors":
            if len(argv) < 3:
                print("usage: validate-env.py live-majors <fixture>", file=sys.stderr)
                return 2
            return live_majors(argv[2])
        if len(argv) < 4:
            print("usage: validate-env.py {clusters|freeidp} <fixture> <live>", file=sys.stderr)
            return 2
        if sub == "clusters":
            return clusters(argv[2], argv[3])
        if sub == "freeidp":
            return freeidp(argv[2], argv[3])
    except (OSError, ValueError) as exc:
        print("%s: %s" % (sub, exc), file=sys.stderr)
        return 1
    print("unknown subcommand %s" % sub, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
PYEOF

# ---------------------------------------------------------------------------
# result recording
# ---------------------------------------------------------------------------

ROWS=()
PASSED=0
FAILED=0
SKIPPED=0

# record <num> <status> <req> <phase> <evidence>
record() {
	local num="$1" status="$2" req="$3" phase="$4" evidence="$5"
	ROWS+=("$(printf '%-5s %-6s %-22s %-8s %s' "$num" "$status" "$req" "$phase" "$evidence")")
	case "$status" in
	PASS) PASSED=$((PASSED + 1)) ;;
	FAIL) FAILED=$((FAILED + 1)) ;;
	SKIP) SKIPPED=$((SKIPPED + 1)) ;;
	*) echo "validate-env: bad status '$status' for check $num" >&2 ;;
	esac
}

# wanted <num> — is this check in scope under --only?
wanted() {
	local num="$1"
	[ -z "$ONLY" ] && return 0
	case ",$ONLY," in
	*,"$num",*) return 0 ;;
	esac
	return 1
}

# ---------------------------------------------------------------------------
# transport
# ---------------------------------------------------------------------------

PF_PID=""
cleanup() {
	[ -n "$PF_PID" ] && kill "$PF_PID" >/dev/null 2>&1
	return 0
}
trap cleanup EXIT

# api_get <path> — prints the response.
#
# curl mode prints "body\n<http_code>" so the caller can split status from body;
# kubectl-proxy mode prints the body (or kubectl's own error line) with no
# status, because `kubectl get --raw` has no header/status flag. Callers
# therefore branch on transport, not on a fake uniform contract.
api_get() {
	local path="$1"
	if [ -n "$BASE_URL" ]; then
		local auth=()
		[ -n "$TOKEN" ] && auth=(-H "Authorization: Bearer $TOKEN")
		# ${auth[@]+…} is not decoration. bash 3.2 (what /usr/bin/env bash
		# resolves to on macOS) treats an empty array expansion as *unbound*
		# under `set -u`, so with no --token the curl line died with
		# `auth[@]: unbound variable` and checks 1-3 reported FAIL on an empty
		# response instead of the API's. bash 4+ accepts the bare form.
		curl -sS -m 20 -w '\n%{http_code}' ${auth[@]+"${auth[@]}"} "$BASE_URL$path" 2>/dev/null
		return $?
	fi
	kubectl --request-timeout=20s get --raw \
		"/api/v1/namespaces/${NAMESPACE}/services/http:${SERVICE}:${PORT}/proxy${path}" 2>&1
}

api_status() { printf '%s' "$1" | tail -n 1; }
api_body() { printf '%s' "$1" | sed '$d'; }

# fetch <path> — normalises both transports into two globals, BODY and STATUS,
# and returns non-zero with a one-line reason when the endpoint did not answer.
# A 401 from a token-gated API is reported as an auth requirement, not as a
# missing route, because kubectl-proxy cannot send the header.
fetch() {
	local path="$1" raw
	BODY=""
	STATUS=""
	raw="$(api_get "$path")" || {
		REASON="$(printf '%s' "$raw" | head -1 | cut -c1-90)"
		return 1
	}
	if [ -n "$BASE_URL" ]; then
		STATUS="$(api_status "$raw")"
		BODY="$(api_body "$raw")"
		if [ "$STATUS" = "401" ] || [ "$STATUS" = "403" ]; then
			REASON="HTTP $STATUS — the API gates /api/v1 with a bearer token; pass --token (curl mode only)"
			return 1
		fi
		if [ "$STATUS" = "404" ]; then
			REASON="HTTP 404 — route not present in the deployed build"
			return 1
		fi
		if [ "$STATUS" != "200" ]; then
			REASON="HTTP $STATUS"
			return 1
		fi
		return 0
	fi
	# kubectl-proxy: a JSON body means the route answered; anything else is
	# kubectl's error line for a missing route or an unreachable cluster.
	if printf '%s' "$raw" | grep -q '^[[:space:]]*[{#]'; then
		BODY="$raw"
		return 0
	fi
	REASON="$(printf '%s' "$raw" | head -1 | cut -c1-90)"
	return 1
}

# ---------------------------------------------------------------------------
# checks 1 and 2 — both read GET /api/v1/clusters
# ---------------------------------------------------------------------------

# The clusters body is fetched once and cached to a file: checks 1 and 2 read
# the same response, and the python takes a path rather than stdin so that
# `python3 "$PYLIB" …` keeps the program on disk and the data in argv.
CLUSTERS_FILE=""
CLUSTERS_ERR=""
load_clusters() {
	[ -n "$CLUSTERS_FILE" ] && return 0
	if fetch /api/v1/clusters; then
		CLUSTERS_FILE="$(mktemp -t pgsi-clusters.XXXXXX.json)"
		printf '%s' "$BODY" >"$CLUSTERS_FILE"
		return 0
	fi
	CLUSTERS_ERR="$REASON"
	return 1
}

check_1() {
	if ! load_clusters; then
		record 1 FAIL R1.5 1a "GET /api/v1/clusters: $CLUSTERS_ERR"
		return
	fi
	local out rc
	out="$(python3 "$PYLIB" clusters "$FIXTURE" "$CLUSTERS_FILE" 2>&1)"
	rc=$?
	[ $rc -eq 0 ] && record 1 PASS R1.5 1a "$out" || record 1 FAIL R1.5 1a "$(printf '%s' "$out" | head -2 | tr '\n' ';' | cut -c1-150)"
}

check_2() {
	if ! load_clusters; then
		record 2 FAIL R1.2 1a "GET /api/v1/clusters: $CLUSTERS_ERR"
		return
	fi
	local out rc
	out="$(python3 "$PYLIB" freeidp "$FIXTURE" "$CLUSTERS_FILE" 2>&1)"
	rc=$?
	[ $rc -eq 0 ] && record 2 PASS R1.2 1a "$out" || record 2 FAIL R1.2 1a "$(printf '%s' "$out" | head -2 | tr '\n' ';' | cut -c1-150)"
}

# §8's check 3 is one sentence with two halves: the pgsi_up series (R9.12,
# Phase 2) and the cluster's replication section (R5.5, Phase 7). The row's
# status comes from the half whose phase has landed — R9.12's — and R5.5's
# verdict rides along in the evidence so the row names everything it looked at
# instead of silently dropping half the sentence. The half-2 verdict reads the
# clusters body check 1 already fetched, not a second request.
check_3() {
	local out rc repl=""
	if ! fetch /metrics; then
		record 3 FAIL R9.12 2 "GET /metrics: $REASON"
		return
	fi
	out="$(printf '%s' "$BODY" | python3 "$PYLIB" metrics 2>&1)"
	rc=$?

	if load_clusters; then
		repl="; $(python3 "$PYLIB" replication "$FIXTURE" "$CLUSTERS_FILE" 2>&1)"
	fi

	if [ $rc -eq 0 ]; then
		record 3 PASS R9.12 2 "$out$repl"
	else
		record 3 FAIL R9.12 2 "$(printf '%s' "$out" | head -2 | tr '\n' ';' | cut -c1-150)$repl"
	fi
}

check_4() {
	local offline="go test skipped (--skip-build)"
	if [ "$SKIP_BUILD" -eq 0 ]; then
		local out rc
		out="$(cd "$ROOT" && go test -count=1 ./internal/collector/pgversion 2>&1)"
		rc=$?
		if [ $rc -ne 0 ]; then
			record 4 FAIL R2.4 2 "go test ./internal/collector/pgversion rc=$rc: $(printf '%s' "$out" | tail -1 | cut -c1-90)"
			return
		fi
		offline="go test ./internal/collector/pgversion rc=0"
	fi

	local targets rc
	targets="$(python3 "$PYLIB" live-majors "$FIXTURE" 2>&1)"
	rc=$?
	if [ $rc -ne 0 ]; then
		record 4 SKIP R2.4 2 "$offline; live half unavailable: $(printf '%s' "$targets" | head -1 | cut -c1-90)"
		return
	fi
	local entry major ref got gotmajor
	for entry in $targets; do
		major="${entry%%|*}"
		ref="${entry#*|}"
		got="$(kubectl --request-timeout=25s -n "${ref%%/*}" exec "${ref##*/}" -c postgres -- \
			psql -U postgres -Atc 'show server_version' 2>/dev/null | head -1)"
		gotmajor="${got%%.*}"
		if [ -z "$got" ]; then
			record 4 FAIL R2.4 2 "$offline; $ref: psql returned no version (fixture says PG$major)"
			return
		fi
		if [ "$gotmajor" != "$major" ]; then
			record 4 FAIL R2.4 2 "$offline; $ref: server_version $got, fixture says PG$major"
			return
		fi
	done
	record 4 PASS R2.4 2 "$offline; live majors confirmed: $(printf '%s' "$targets" | tr ' ' ',')"
}

check_deferred() {
	record "$1" SKIP "$2" "$3" "$4"
}

# ---------------------------------------------------------------------------
# run
# ---------------------------------------------------------------------------

if [ ! -f "$FIXTURE" ]; then
	echo "validate-env: $FIXTURE not found — checks 1, 2 and 4 read their expectations from it" >&2
	exit 2
fi

if [ "$USE_PORT_FORWARD" -eq 1 ] && [ -z "$BASE_URL" ]; then
	BASE_URL="http://127.0.0.1:18080"
	kubectl -n "$NAMESPACE" port-forward "svc/$SERVICE" 18080:"$PORT" >/dev/null 2>&1 &
	PF_PID=$!
	sleep 2
fi

echo "pgsi §8 validation — R16.5"
echo "api: ${BASE_URL:-kubectl proxy $NAMESPACE/$SERVICE:$PORT}"
echo "fixture: $FIXTURE"
echo

wanted 1 && check_1
wanted 2 && check_2
wanted 3 && check_3
wanted 4 && check_4

if [ -z "$ONLY" ]; then
	check_deferred 5 R9.9,R9.11 4 "promtool check metrics + check rules — chart/templates/prometheusrule.yaml is Phase 4's artifact"
	check_deferred 6 R9.9 4 "the existing monitoring/prom-operator scrape — Phase 4, needs the ServiceMonitor applied"
	check_deferred 7 R11.6,R11.12,R11.13 5 "helm template goldens + packaged install — Phase 5"
	check_deferred 8 R4.1 6a "active-queries vs a direct pg_stat_activity dump — Phase 6a"
	check_deferred 9 R5.1 7 "the five System Insights cards, each value matched to its source query — Phase 7"
	check_deferred 10 R6.4 8 "pgbouncer vs Postgres client_addr attribution — Phase 8"
fi

echo
printf '%-5s %-6s %-22s %-8s %s\n' CHECK STATUS REQ PHASE EVIDENCE
printf '%-5s %-6s %-22s %-8s %s\n' '-----' '------' '----------------------' '-------' '--------'
for r in "${ROWS[@]}"; do printf '%s\n' "$r"; done
echo
echo "pass=$PASSED fail=$FAILED skip=$SKIPPED"

[ "$FAILED" -gt 0 ] && exit 1
exit 0
