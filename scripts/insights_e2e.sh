#!/usr/bin/env bash
# S28 §9: Insights suggestions end to end, on a THROWAWAY PostgreSQL 18 cluster (port 55497, never the
# founder's 55432) and a dev serve on 127.0.0.1:18297. Dev only.
#
#   DEEDGLASS_IT_THROWAWAY=1 scripts/insights_e2e.sh          the acceptance run, then everything is stopped and deleted
#   DEEDGLASS_IT_THROWAWAY=1 KEEP=1 scripts/insights_e2e.sh   stop after the suggestions exist and keep the stack up (Ctrl-C ends it)
#
# The run: dev simulate (seed 7, ending 2 days ago) → detect --backfill 9d → suggestions generate →
# accept the loosen suggestion into shadow → simulate 2 more days → insights shadow → promote with
# policy activate --mode enforce → deeds export → verify. Each step prints its own result.
set -euo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"
[ "${DEEDGLASS_IT_THROWAWAY:-}" = "1" ] || { echo "refusing: set DEEDGLASS_IT_THROWAWAY=1 (throwaway clusters only)" >&2; exit 2; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="/opt/homebrew/opt/postgresql@18/bin"
PGPORT="${INSIGHTS_E2E_PGPORT:-55497}"
PORT="${INSIGHTS_E2E_PORT:-18297}"
PW=deedglass-insights-throwaway
DATA="$ROOT/.tmp/insights-e2e-pg"
WORK="$ROOT/.tmp/insights-e2e"
DG="$ROOT/bin/deedglass"
[ -x "$DG" ] || { echo "bin/deedglass is missing: run make build-only" >&2; exit 2; }
[ -x "$BIN/postgres" ] || { echo "no Homebrew postgresql@18 at $BIN" >&2; exit 2; }

SERVE_PID=""
cleanup() {
  if [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null; then
    for _ in $(seq 1 40); do kill -0 "$SERVE_PID" 2>/dev/null || break; sleep 0.25; done
    kill -9 "$SERVE_PID" 2>/dev/null || true
  fi
  "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$DATA" "$WORK"
}
trap cleanup EXIT
trap "exit 130" INT TERM

mkdir -p "$ROOT/.tmp"
if [ -d "$DATA" ]; then "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$DATA"; fi
rm -rf "$WORK" && mkdir -p "$WORK" && chmod 700 "$WORK"
"$BIN/initdb" -D "$DATA" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
printf "listen_addresses = 'localhost'\nport = %s\nunix_socket_directories = ''\nmax_connections = 100\n" "$PGPORT" >> "$DATA/postgresql.conf"
"$BIN/pg_ctl" -D "$DATA" -l "$ROOT/.tmp/insights-e2e-pg.log" -w start >/dev/null
psql_admin() { "$BIN/psql" -h localhost -p "$PGPORT" -U postgres -v ON_ERROR_STOP=1 -qAt "$@"; }
psql_admin -d postgres -c "CREATE ROLE deedglass_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '$PW'" \
  -c "CREATE ROLE deedglass_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '$PW'" \
  -c "CREATE DATABASE deedglass OWNER deedglass_migrator" -c "REVOKE ALL ON DATABASE deedglass FROM PUBLIC" \
  -c "GRANT CONNECT ON DATABASE deedglass TO deedglass_app"

export DEEDGLASS_ENV=dev
export DEEDGLASS_DATABASE_URL="postgres://deedglass_app:$PW@127.0.0.1:$PGPORT/deedglass?sslmode=disable"
export DEEDGLASS_MIGRATE_DATABASE_URL="postgres://deedglass_migrator:$PW@127.0.0.1:$PGPORT/deedglass?sslmode=disable"
export DEEDGLASS_BASE_URL="http://127.0.0.1:$PORT"
export DEEDGLASS_LISTEN="127.0.0.1:$PORT"
export DEEDGLASS_LOG_LEVEL=warn
unset DEEDGLASS_ANTHROPIC_API_KEY # template wording only; the run makes no outbound call
cd "$WORK"
"$DG" migrate up --no-color >/dev/null

step() { printf '\n$ %s\n' "$*"; }
# The window ends two days ago at 12:00 IST, the end S27's seed-7 fixtures use (the scenario's days
# are 24-hour spans from the start, so the hour the window ends at moves incidents between hours).
END="$(python3 -c 'import datetime as d;print((d.datetime.now(d.timezone.utc)-d.timedelta(days=2)).strftime("%Y-%m-%dT06:30:00Z"))')"
step "deedglass dev simulate --days 7 --end $END --seed 7"
"$DG" dev simulate --days 7 --end "$END" --seed 7 --no-color
org="$(psql_admin -d deedglass -c "SELECT id FROM deedglass.orgs WHERE slug = 'acme-sim'")"
step "deedglass insights detect --backfill 9d --org acme-sim"
"$DG" insights detect --backfill 9d --org acme-sim --no-color
# serve starts after the simulated week: its approval-expiry sweep runs on the real clock, and the
# simulator's approvals expire on the virtual one (days ago), so a serve running beside it would
# expire approvals the scenario means people to decide.
if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then echo "port $PORT is busy before serve started; stop that process first" >&2; exit 2; fi
"$DG" serve > "$ROOT/.tmp/insights-e2e-serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 60); do curl -fsS -m 1 "http://127.0.0.1:$PORT/v1/health" >/dev/null 2>&1 && break; sleep 0.25; done
curl -fsS -m 2 "http://127.0.0.1:$PORT/v1/health" >/dev/null || { cat "$ROOT/.tmp/insights-e2e-serve.log" >&2; exit 1; }
admin="$("$DG" org keys create --org acme-sim --name insights-e2e --mode test --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret"])')"
printf 'admin=%s\norg=%s\nserver=http://127.0.0.1:%s\n' "$admin" "$org" "$PORT" > "$WORK/keys"
api() { DEEDGLASS_API_KEY="$admin" DEEDGLASS_SERVER="http://127.0.0.1:$PORT" "$DG" --no-color "$@"; }
step "deedglass insights suggestions generate"
api insights suggestions generate
api insights suggestions generate --json > "$WORK/second.json"
python3 -c "import json;r=json.load(open('$WORK/second.json'));assert len(r['created'])==0, r;print('  second run: 0 created ·', r['unchanged'], 'unchanged')"
loosen="$(api insights suggestions list --kind loosen --json | python3 -c 'import sys,json;d=json.load(sys.stdin)["data"];print(d[0]["id"] if d else "")')"
[ -n "$loosen" ] || { echo "no loosen suggestion was generated" >&2; exit 1; }
if [ "${KEEP:-}" = "1" ]; then
  echo
  echo "insights-e2e: kept up. Server http://127.0.0.1:$PORT · org $org · keys in $WORK/keys · Ctrl-C stops and deletes it."
  # A person who can sign in to the Console: the simulated org's first approver.
  "$DG" org login-link --org acme-sim --email priya@example.test --no-color 2>/dev/null || true
  wait "$SERVE_PID"
  exit 0
fi
step "deedglass insights suggestions show $loosen"
api insights suggestions show "$loosen"
step "deedglass insights suggestions accept $loosen --reason \"Ops approves these every time\" --replace-shadow"
api insights suggestions accept "$loosen" --reason "Ops approves these every time" --replace-shadow
step "deedglass dev simulate --org-slug acme-sim --append --days 2 --seed 8"
"$DG" dev simulate --org-slug acme-sim --append --days 2 --seed 8 --no-color
step "deedglass insights shadow"
api insights shadow
v="$(psql_admin -d deedglass -c "SELECT v.version FROM deedglass.policy_versions v WHERE v.org_id = '$org' AND v.origin = 'suggestion' ORDER BY v.version DESC LIMIT 1")"
step "deedglass policy activate acme-sim --version $v --mode enforce --reason \"Promoted after two days in shadow\" --yes"
api policy activate acme-sim --version "$v" --mode enforce --reason "Promoted after two days in shadow" --yes
step "select origin, suggestion_id is not null from deedglass.policy_versions where version = $v"
psql_admin -d deedglass -c "SELECT origin || ' | ' || CASE WHEN suggestion_id IS NOT NULL THEN 't' ELSE 'f' END FROM deedglass.policy_versions WHERE org_id = '$org' AND version = $v"
step "deedglass deeds export --org acme-sim --out $WORK/sim.tar.gz && deedglass verify $WORK/sim.tar.gz"
api deeds export --out "$WORK/sim.tar.gz" >/dev/null
"$DG" verify "$WORK/sim.tar.gz" --no-color
step "deedglass insights digest --preview --org acme-sim --html $WORK/digest.html"
"$DG" insights digest --preview --org acme-sim --html "${DIGEST_HTML:-$WORK/digest.html}" --no-color
