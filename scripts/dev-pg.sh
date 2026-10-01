#!/usr/bin/env bash
# A Docker-free dev Postgres (S06): a THROWAWAY Homebrew PostgreSQL 18 cluster on 127.0.0.1:55432
# (the compose port), the control-plane roles, the deedglass database and migrations. Use it when
# Docker Desktop isn't available; `make dev` stays the default.
#
#   scripts/dev-pg.sh up     create (or start) the cluster, apply migrations, write .tmp/dev-pg.env
#   scripts/dev-pg.sh down   stop the cluster (the data directory is kept)
#   scripts/dev-pg.sh reset  stop it and delete .tmp/devpg
#   scripts/dev-pg.sh env    print the env file's path
#
# Then, in each terminal:  set -a; . .tmp/dev-pg.env; set +a
# The passwords are dev-only constants for a cluster that listens on localhost only.
set -euo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"   # macOS: the Homebrew postmaster aborts "became multithreaded" without it

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="/opt/homebrew/opt/postgresql@18/bin"
DATA="$ROOT/.tmp/devpg"
LOG="$ROOT/.tmp/devpg.log"
ENVF="$ROOT/.tmp/dev-pg.env"
PORT=55432
PW=deedglass-dev-only

TEAL="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' RESET=$'\033[0m'; fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

psql_admin() { "$BIN/psql" -h localhost -p "$PORT" -U postgres -v ON_ERROR_STOP=1 -qAt "$@"; }

up() {
  [ -x "$BIN/postgres" ] || fail "No Homebrew postgresql@18 at $BIN. Install it with: brew install postgresql@18"
  mkdir -p "$ROOT/.tmp"
  if [ ! -d "$DATA" ]; then
    "$BIN/initdb" -D "$DATA" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
    cat >> "$DATA/postgresql.conf" <<EOF

# --- deedglass dev (throwaway) ---
listen_addresses = 'localhost'
port = $PORT
unix_socket_directories = ''
wal_level = logical
EOF
  fi
  if ! "$BIN/pg_ctl" -D "$DATA" status >/dev/null 2>&1; then
    "$BIN/pg_ctl" -D "$DATA" -l "$LOG" -w start >/dev/null || fail "Postgres didn't start. See $LOG."
  fi
  if [ "$(psql_admin -d postgres -c "SELECT count(*) FROM pg_roles WHERE rolname = 'deedglass_app'")" = "0" ]; then
    psql_admin -d postgres -c "CREATE ROLE deedglass_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '$PW'" \
      -c "CREATE ROLE deedglass_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '$PW'" \
      -c "CREATE DATABASE deedglass OWNER deedglass_migrator" \
      -c "REVOKE ALL ON DATABASE deedglass FROM PUBLIC" \
      -c "GRANT CONNECT ON DATABASE deedglass TO deedglass_app"
  fi
  cat > "$ENVF" <<EOF
DEEDGLASS_ENV=dev
DEEDGLASS_DATABASE_URL=postgres://deedglass_app:$PW@127.0.0.1:$PORT/deedglass?sslmode=disable
DEEDGLASS_MIGRATE_DATABASE_URL=postgres://deedglass_migrator:$PW@127.0.0.1:$PORT/deedglass?sslmode=disable
DEEDGLASS_BASE_URL=http://127.0.0.1:8080
EOF
  chmod 600 "$ENVF"
  ( set -a; . "$ENVF"; set +a; cd "$ROOT" && go run ./cmd/deedglass migrate up >/dev/null ) || fail "Migrations failed. Run deedglass migrate up to see why."
  ok "Dev Postgres on 127.0.0.1:$PORT · migrated · env in .tmp/dev-pg.env"
  printf '  next  set -a; . .tmp/dev-pg.env; set +a; deedglass serve\n'
}

case "${1:-}" in
  up) up ;;
  down) "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; ok "Dev Postgres stopped (data kept in .tmp/devpg)" ;;
  reset) "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$DATA" "$ENVF"; ok "Dev Postgres deleted" ;;
  env) echo "$ENVF" ;;
  *) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 2 ;;
esac
