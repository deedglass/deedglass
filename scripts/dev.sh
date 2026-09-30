#!/usr/bin/env bash
# The local Deedglass stack (docs/DEV_LOOP.md): Postgres, a one-shot migrate and the server, from
# deploy/docker-compose.yml. Bash 3.2 compatible (macOS).
#
#   scripts/dev.sh up      make dev: create .env on first run, build deedglass:dev, start Postgres, run
#                          migrations, start the server, then print a summary
#   scripts/dev.sh down    make dev-down: stop the stack; the database volume is kept
#   scripts/dev.sh reset   make dev-reset: delete the stack and its database volume (asks you to type reset)
#   scripts/dev.sh logs    make dev-logs: follow the logs of every service
#   scripts/dev.sh db      make run (first half): create .env if needed, start Postgres only, and stop the
#                          deedglass container so the server you run from source can use port 8080
#   scripts/dev.sh env     create or complete .env only (CI's docker-smoke job and scripts/ci-local.sh)
#
# Env (tooling names; the binary ignores DEEDGLASS_DEV_*):
#   NO_COLOR                 no colour (colour is only ever used on a TTY)
#   DEEDGLASS_DEV_CONFIRM    set to reset to skip the reset prompt
#   DEEDGLASS_DEV_VERBOSE=1  stream Docker's output instead of writing it to .tmp/dev.log
#   DEEDGLASS_DEV_ENV_FILE   the env file to create and use (default .env)
#   DEEDGLASS_DEV_PROJECT    the compose project name (default deedglass)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

ENV_FILE="${DEEDGLASS_DEV_ENV_FILE:-.env}"
COMPOSE_FILE="deploy/docker-compose.yml"
PROJECT="${DEEDGLASS_DEV_PROJECT:-deedglass}"
API="http://127.0.0.1:8080"
LOG_DIR=".tmp"
LOG="$LOG_DIR/dev.log"
HEALTH_WAIT=90
AGAIN="make dev"   # the command each error message tells you to run again
SECRET_KEYS="DEEDGLASS_DEV_PG_ADMIN_PASSWORD DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD DEEDGLASS_DEV_PG_APP_PASSWORD DEEDGLASS_VAULT_KEY DEEDGLASS_API_KEY_PEPPER"

# Colour the glyph only, only on a TTY, and never with NO_COLOR (DEEDGLASS_BRAND.md, ADR-016).
TEAL="" VIOLET="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  TEAL=$'\033[36m' VIOLET=$'\033[35m' RED=$'\033[31m' RESET=$'\033[0m'
fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
pend() { printf '%s◐%s %s\n' "$VIOLET" "$RESET" "$*"; }
note() { printf '  %s\n' "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

usage() { awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; }

# --- Docker -----------------------------------------------------------------------------------------

need_docker() {
  command -v docker >/dev/null 2>&1 || fail "Docker isn't installed. Install Docker Desktop, then run $AGAIN again."
  docker info >/dev/null 2>&1 || fail "Docker isn't running. Start Docker Desktop, then run $AGAIN again."
  docker compose version >/dev/null 2>&1 || fail "Docker Compose isn't available. Update Docker Desktop (it includes Compose), then run $AGAIN again."
}

# compose runs docker compose on the stack. Without an env file (down, reset or logs before the first
# make dev), the required ${VAR:?} values get inert placeholders so Compose can still read the file.
compose() {
  if [ -f "$ENV_FILE" ]; then
    docker compose -p "$PROJECT" -f "$COMPOSE_FILE" --env-file "$ENV_FILE" "$@"
  else
    env DEEDGLASS_DEV_PG_ADMIN_PASSWORD=unset DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD=unset \
      DEEDGLASS_DEV_PG_APP_PASSWORD=unset DEEDGLASS_VAULT_KEY=unset DEEDGLASS_API_KEY_PEPPER=unset \
      docker compose -p "$PROJECT" -f "$COMPOSE_FILE" "$@"
  fi
}

# run_compose writes Docker's output to .tmp/dev.log, or streams it with DEEDGLASS_DEV_VERBOSE=1.
run_compose() {
  mkdir -p "$LOG_DIR"
  if [ "${DEEDGLASS_DEV_VERBOSE:-}" = "1" ]; then
    compose "$@" 2>&1 | tee -a "$LOG"
    return "${PIPESTATUS[0]}"
  fi
  compose "$@" >>"$LOG" 2>&1
}

show_log() {
  [ -f "$LOG" ] || return 0
  echo "  The last lines of $LOG:" >&2
  tail -n 20 "$LOG" | sed 's/^/    /' >&2
}

service_running() { [ -n "$(compose ps -q --status running "$1" 2>/dev/null)" ]; }

# migrate_state prints "<status> <exit code>" of the one-shot migrate container, or nothing.
migrate_state() {
  local id
  id="$(compose ps -a -q migrate 2>/dev/null | head -n 1)"
  [ -n "$id" ] || return 0
  docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$id" 2>/dev/null || true
}

port_busy() {
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
    return
  fi
  if command -v nc >/dev/null 2>&1; then
    nc -z 127.0.0.1 "$1" >/dev/null 2>&1
    return
  fi
  return 1
}

check_ports() { # $1: postgres | all
  if ! service_running postgres && port_busy 55432; then
    fail "Port 55432 is already in use, perhaps by another Postgres. Find it with lsof -nP -iTCP:55432 -sTCP:LISTEN, stop it, then run $AGAIN again."
  fi
  if [ "$1" = all ] && ! service_running deedglass && port_busy 8080; then
    fail "Port 8080 is already in use, perhaps by make run. Stop that server (Ctrl-C in its terminal), then run $AGAIN again."
  fi
}

# --- .env ------------------------------------------------------------------------------------------

env_get() {
  local v
  v="$(sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1)"
  case "$v" in \"*\"|\'*\') v="${v:1:${#v}-2}" ;; esac
  printf '%s' "$v"
}

# env_set replaces KEY=… (or appends it), keeping the file at mode 0600.
env_set() {
  local tmp
  tmp="$(umask 077; mktemp "$ENV_FILE.XXXXXX")"
  awk -v k="$1" -v v="$2" '
    index($0, k "=") == 1 { print k "=" v; done = 1; next }
    { print }
    END { if (!done) print k "=" v }' "$ENV_FILE" > "$tmp"
  mv "$tmp" "$ENV_FILE"
  chmod 600 "$ENV_FILE"
}

# env_replace swaps every occurrence of a placeholder for a value (the passwords inside the URLs).
env_replace() {
  local tmp
  tmp="$(umask 077; mktemp "$ENV_FILE.XXXXXX")"
  awk -v p="$1" -v v="$2" '{
    out = ""; rest = $0
    while ((i = index(rest, p)) > 0) { out = out substr(rest, 1, i - 1) v; rest = substr(rest, i + length(p)) }
    print out rest }' "$ENV_FILE" > "$tmp"
  mv "$tmp" "$ENV_FILE"
  chmod 600 "$ENV_FILE"
}

# ensure_env creates .env from .env.example on first run and fills any empty secret. Passwords are hex
# (URL-safe); keys are base64 of 32 random bytes. Existing values are never replaced.
ensure_env() {
  command -v openssl >/dev/null 2>&1 || fail "openssl isn't installed. Install it, then run $AGAIN again."
  if [ ! -f "$ENV_FILE" ]; then
    [ -f .env.example ] || fail ".env.example is missing. Restore it with git checkout -- .env.example, then run $AGAIN again."
    pend "Creating $ENV_FILE with fresh local secrets (git-ignored, mode 0600)"
    (umask 077; cp .env.example "$ENV_FILE")
  fi
  chmod 600 "$ENV_FILE"
  local key
  for key in $SECRET_KEYS; do
    [ -z "$(env_get "$key")" ] || continue
    case "$key" in
      *_PASSWORD) env_set "$key" "$(openssl rand -hex 24)" ;;
      *) env_set "$key" "$(openssl rand -base64 32)" ;;
    esac
  done
  env_replace "__DEEDGLASS_DEV_PG_APP_PASSWORD__" "$(env_get DEEDGLASS_DEV_PG_APP_PASSWORD)"
  env_replace "__DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD__" "$(env_get DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD)"
}

# --- Postgres --------------------------------------------------------------------------------------

psql_admin() { compose exec -T postgres psql -X -A -t -q -U postgres -d postgres -c "$1"; }

# start_postgres starts Postgres alone, then checks the volume was set up by init/10-deedglass.sh and
# that the passwords in .env still open it. Sets DATABASES.
DATABASES=""
start_postgres() {
  if ! run_compose up -d --wait postgres; then
    show_log
    fail "Postgres didn't start. See make dev-logs, then run $AGAIN again."
  fi
  local roles
  roles="$(psql_admin "SELECT count(*) FROM pg_roles WHERE rolname IN ('deedglass_migrator', 'deedglass_app')" 2>/dev/null || true)"
  DATABASES="$(psql_admin "SELECT string_agg(datname, ', ' ORDER BY datname) FROM pg_database WHERE datname IN ('deedglass', 'demo')" 2>/dev/null || true)"
  if [ "$roles" != "2" ] || [ "$DATABASES" != "deedglass, demo" ]; then
    fail "The Postgres volume predates this setup (it lacks the deedglass roles or the demo database). Run make dev-reset for an empty database, then $AGAIN."
  fi
  # The container's DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD comes from the current .env. Connect over the
  # container's network address, so the password is checked (local socket connections are trusted).
  # shellcheck disable=SC2016 # expanded inside the container, not here
  if ! compose exec -T postgres bash -c \
      'PGPASSWORD="$DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD" psql -X -A -t -q -h postgres -U deedglass_migrator -d deedglass -c "SELECT 1"' \
      >/dev/null 2>&1; then
    fail "The passwords in $ENV_FILE don't open this Postgres volume (was $ENV_FILE recreated?). Run make dev-reset, then $AGAIN."
  fi
}

# --- Health ----------------------------------------------------------------------------------------

# json_str / json_int read the first matching key: the top-level status and version come before
# checks in the health body. jq is used when it is installed.
json_str() {
  if command -v jq >/dev/null 2>&1; then printf '%s' "$2" | jq -r "$3 // empty" 2>/dev/null; return 0; fi
  printf '%s' "$2" | tr -d '\n' | grep -o "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | head -n 1 | sed 's/.*:[[:space:]]*"\(.*\)"$/\1/'
}
json_int() {
  if command -v jq >/dev/null 2>&1; then printf '%s' "$2" | jq -r "$3 // empty" 2>/dev/null; return 0; fi
  printf '%s' "$2" | tr -d '\n' | grep -o "\"$1\"[[:space:]]*:[[:space:]]*[0-9][0-9]*" | head -n 1 | sed 's/.*:[[:space:]]*//'
}

# wait_health polls GET /v1/health until "status" is "ok", for up to HEALTH_WAIT seconds. Sets HEALTH.
HEALTH=""
wait_health() {
  local deadline=$(( $(date +%s) + HEALTH_WAIT ))
  while :; do
    HEALTH="$(curl -sS -m 2 "$API/v1/health" 2>/dev/null || true)"
    [ "$(json_str status "$HEALTH" .status)" = "ok" ] && return 0
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 1
  done
}

# --- Commands --------------------------------------------------------------------------------------

cmd_up() {
  need_docker
  ensure_env
  check_ports all
  mkdir -p "$LOG_DIR"
  : > "$LOG"
  local pg
  pg="$(sed -n 's/.*image: postgres:\([0-9.]*\).*/\1/p' "$COMPOSE_FILE" | head -n 1)"
  pend "Building deedglass:dev · starting Postgres ${pg:-18}"
  start_postgres
  ok "Postgres · 127.0.0.1:55432 · databases $DATABASES"

  if ! run_compose up -d --build --wait; then
    # Some Compose versions count the exited one-shot migrate as a failure under --wait. If it
    # exited 0, start without --wait and poll /v1/health instead.
    local state
    state="$(migrate_state)"
    case "$state" in
      "exited 0")
        if ! run_compose up -d --build; then
          show_log
          fail "The stack didn't start. See make dev-logs, then run $AGAIN again."
        fi
        ;;
      exited\ *)
        compose logs --no-color --tail 20 migrate 2>/dev/null | sed 's/^/    /' >&2 || true
        fail "Migrations failed (migrate exited ${state#exited }). Read the lines above or make dev-logs, fix the cause, then run $AGAIN again."
        ;;
      *)
        show_log
        if grep -q -i -e 'failed to solve' -e 'failed to build' "$LOG" 2>/dev/null; then
          fail "The deedglass:dev image didn't build. The full log is $LOG; fix the build, then run $AGAIN again."
        fi
        fail "The stack didn't start. See make dev-logs, then run $AGAIN again."
        ;;
    esac
  fi

  if ! wait_health; then
    local st
    st="$(json_str status "$HEALTH" .status)"
    fail "Deedglass didn't report healthy within ${HEALTH_WAIT} s (status: ${st:-no answer}). See make dev-logs, then run $AGAIN again."
  fi
  local applied version api
  applied="$(json_int applied "$HEALTH" .checks.migrations.applied)"
  version="$(json_str version "$HEALTH" .version)"
  api="$(json_str api_version "$HEALTH" .api_version)"
  ok "Migrations · schema at $(printf '%04d' "${applied:-0}")"
  ok "Deedglass ${version:-dev} · $API · api ${api:-unknown}"
  note "Try: curl -s localhost:8080/v1/health"
  note "Logs: make dev-logs · Stop: make dev-down"
}

cmd_db() {
  AGAIN="make run"
  need_docker
  ensure_env
  check_ports postgres
  mkdir -p "$LOG_DIR"
  : > "$LOG"
  if service_running deedglass; then
    run_compose stop deedglass || fail "Couldn't stop the deedglass container. Run make dev-down, then $AGAIN again."
    pend "Stopped the deedglass container so the server from source can use port 8080"
  fi
  if port_busy 8080; then
    fail "Port 8080 is already in use. Find it with lsof -nP -iTCP:8080 -sTCP:LISTEN, stop it, then run $AGAIN again."
  fi
  start_postgres
  ok "Postgres · 127.0.0.1:55432 · databases $DATABASES"
}

cmd_down() {
  AGAIN="make dev-down"
  need_docker
  run_compose down --remove-orphans || { show_log; fail "The stack didn't stop cleanly. Try docker compose -f $COMPOSE_FILE down."; }
  ok "Stopped · your database is kept · make dev starts it again"
}

cmd_reset() {
  AGAIN="make dev-reset"
  need_docker
  echo "This deletes the local Deedglass stack and its Postgres volume: every org, agent and deed in your"
  echo "local database. $ENV_FILE is kept, so the next make dev reuses its passwords and keys."
  if [ "${DEEDGLASS_DEV_CONFIRM:-}" != "reset" ]; then
    local answer=""
    if [ -t 0 ]; then
      printf 'Type reset to continue: '
      read -r answer || answer=""
    fi
    [ "$answer" = "reset" ] || { echo "Nothing was deleted. Run make dev-reset and type reset, or set DEEDGLASS_DEV_CONFIRM=reset."; exit 1; }
  fi
  run_compose down -v --remove-orphans || { show_log; fail "The reset didn't finish. See $LOG, then run make dev-reset again."; }
  ok "Deleted the local stack and its database volume · make dev starts fresh"
}

cmd_logs() {
  AGAIN="make dev-logs"
  need_docker
  compose logs -f --tail 200
}

case "${1:-}" in
  up) cmd_up ;;
  down) cmd_down ;;
  reset) cmd_reset ;;
  logs) cmd_logs ;;
  db) cmd_db ;;
  env) ensure_env; ok "$ENV_FILE is ready (mode 0600)" ;;
  -h|--help|help) usage ;;
  *) usage >&2; exit 2 ;;
esac
