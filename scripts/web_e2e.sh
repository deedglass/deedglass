#!/usr/bin/env bash
# The Console against a real server (S24 §6): a THROWAWAY PostgreSQL 18 cluster, migrations, the Acme
# org with an owner, an admin and a viewer, and bin/deedglass serve (dev) on 127.0.0.1:$WEB_E2E_PORT.
#
#   scripts/web_e2e.sh test         Playwright (system Chrome): shell, keyboard, palette, Stop all, live events, axe, CSP
#   scripts/web_e2e.sh lighthouse   Lighthouse 13 on /app, /app/_kit and /login (desktop and mobile)
#   scripts/web_e2e.sh shots        the screenshot pass (writes ~/txv/ops/plan/prompts-v7/reports/screens/S24)
#   scripts/web_e2e.sh serve        keep the stack up until Ctrl-C (for playwright-cli and manual looks)
#
# The cluster lives in .tmp/web-e2e-pg (port 5546x, never the founder's 55432); the server's working
# directory is .tmp/web-e2e (its dev seal key and secrets). Everything is stopped and deleted on exit.
set -euo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"
[ "${DEEDGLASS_IT_THROWAWAY:-}" = "1" ] || { echo "refusing: set DEEDGLASS_IT_THROWAWAY=1 (throwaway clusters only)" >&2; exit 2; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-test}"
BIN="/opt/homebrew/opt/postgresql@18/bin"
PGPORT=55468
PORT="${WEB_E2E_PORT:-18241}"
PW=deedglass-e2e-throwaway
DATA="$ROOT/.tmp/web-e2e-pg"
WORK="$ROOT/.tmp/web-e2e"
DG="$ROOT/bin/deedglass"
[ -x "$DG" ] || { echo "bin/deedglass is missing: run make build-only" >&2; exit 2; }
[ -x "$BIN/postgres" ] || { echo "no Homebrew postgresql@18 at $BIN" >&2; exit 2; }

SERVE_PID=""
cleanup() {
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null || true
  "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$DATA" "$WORK"
}
trap cleanup EXIT

mkdir -p "$ROOT/.tmp"
if [ -d "$DATA" ]; then "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$DATA"; fi
rm -rf "$WORK" && mkdir -p "$WORK" && chmod 700 "$WORK"
"$BIN/initdb" -D "$DATA" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
cat >> "$DATA/postgresql.conf" <<EOF
listen_addresses = 'localhost'
port = $PGPORT
unix_socket_directories = ''
wal_level = logical
max_connections = 200
EOF
"$BIN/pg_ctl" -D "$DATA" -l "$ROOT/.tmp/web-e2e-pg.log" -w start >/dev/null
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
cd "$WORK"
"$DG" migrate up --no-color >/dev/null
"$DG" org create --name Acme --slug acme --owner-email owner@acme.example --no-color >/dev/null
"$DG" org create --name "Acme Staging" --slug acme-staging --owner-email owner@acme.example --no-color >/dev/null
# An admin and a viewer in Acme (direct rows in this throwaway database, as the superuser).
org="$(psql_admin -d deedglass -c "SELECT id FROM deedglass.orgs WHERE slug = 'acme'")"
newid() { python3 -c "import secrets;a='0123456789abcdefghjkmnpqrstvwxyz';print('$1_'+secrets.choice('01234567')+''.join(secrets.choice(a) for _ in range(25)))"; }
for spec in "admin|priya.raman@acme.example|Priya Raman" "viewer|sam.okafor@acme.example|Sam Okafor"; do
  IFS='|' read -r role email name <<<"$spec"
  uid="$(newid usr)"
  psql_admin -d deedglass -c "INSERT INTO deedglass.users (id, email, display_name, status) VALUES ('$uid', '$email', '$name', 'active')" \
    -c "INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at) VALUES ('$(newid mem)', '$org', '$uid', '$role', 'active', now())"
done
psql_admin -d deedglass -c "UPDATE deedglass.users SET display_name = 'Arjun Mehta' WHERE email = 'owner@acme.example'"

"$DG" serve > "$ROOT/.tmp/web-e2e-serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 60); do curl -fsS -m 1 "http://127.0.0.1:$PORT/v1/health" >/dev/null 2>&1 && break; sleep 0.25; done
curl -fsS -m 2 "http://127.0.0.1:$PORT/v1/health" >/dev/null || { cat "$ROOT/.tmp/web-e2e-serve.log" >&2; exit 1; }
echo "web-e2e: Acme on http://127.0.0.1:$PORT (PostgreSQL 18 on $PGPORT)"

export WEB_E2E_BASE="http://127.0.0.1:$PORT"
export WEB_E2E_DG="$DG"
export WEB_E2E_WORK="$WORK"
export SHOTS_DIR="${SHOTS_DIR:-$HOME/txv/ops/plan/prompts-v7/reports/screens/S24}"
export LIGHTHOUSE_DIR="${LIGHTHOUSE_DIR:-$HOME/txv/ops/plan/prompts-v7/reports/lighthouse/S24}"
cd "$ROOT/web"
st=0
case "$MODE" in
  test) npx playwright test --project=chromium || st=$? ;;
  lighthouse) node e2e/lighthouse.mjs || st=$? ;;
  shots) npx playwright test --project=shots || st=$? ;;
  serve) echo "web-e2e: up. Sign-in links: $DG org login-link --email owner@acme.example --org acme (cwd $WORK). Ctrl-C to stop."; wait "$SERVE_PID" || st=$? ;;
  *) echo "unknown mode $MODE" >&2; st=2 ;;
esac
exit $st
