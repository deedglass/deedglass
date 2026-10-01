#!/usr/bin/env bash
# The Acme demo stack (S15; examples/demo/README.md). Bash 3.2 compatible (macOS).
#
#   demo.sh up        make demo-up: write .env and the seal key on first run, start the stack, seed
#                     the org, agents, keys and policy; HTTP=1 also starts the everything upstream
#   demo.sh down      make demo-down: stop the stack; the volumes are kept
#   demo.sh reset     make demo-reset: reload the orders database from sql/ and reset
#                     /tmp/deedglass-demo; HARD=1 also wipes the Ledger (and re-seeds)
#   demo.sh status    make demo-status: one line per part, ✓ or ✕
#   demo.sh snippet   make demo-snippet CLIENT=claude-desktop|claude-code|cursor|vscode|windsurf [KEYS=1]
#   demo.sh psql      make demo-psql: psql on the orders database as acme_owner
#   demo.sh follow    make demo-follow: deedglass actions list --follow --connector orders
#   demo.sh verify    make demo-verify [SINCE=10m]: export recent deeds and verify the chain
#
# Env: DEMO_PORT (default 8080, or what .env says), HTTP=1, HARD=1, CLIENT, KEYS=1, SINCE, NO_COLOR.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
DEMO="$ROOT/examples/demo"
ENV_FILE="$DEMO/.env"
KEYS_FILE="$DEMO/.demo-keys"
STATE="$DEMO/.demo"
PROJECT="deedglass-demo"
LOG="$STATE/demo.log"
BIN="$ROOT/bin/deedglass"
FOLDER="/tmp/deedglass-demo"

TEAL="" RED="" VIOLET="" DIM="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  TEAL=$'\033[36m' RED=$'\033[31m' VIOLET=$'\033[35m' DIM=$'\033[2m' RESET=$'\033[0m'
fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
bad()  { printf '%s✕%s %s\n' "$RED" "$RESET" "$*"; }
pend() { printf '%s◐%s %s\n' "$VIOLET" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

envget() { [ -f "$ENV_FILE" ] && grep -E "^$1=" "$ENV_FILE" | head -1 | cut -d= -f2- || true; }
PORT="${DEMO_PORT:-$(envget DEMO_PORT)}"
PORT="${PORT:-8080}"

need_docker() {
  command -v docker >/dev/null 2>&1 || fail "Docker isn't installed. Install Docker Desktop, then run make demo-up again."
  docker info >/dev/null 2>&1 || fail "Docker isn't running. Start Docker Desktop, then run make demo-up again."
}

compose() {
  docker compose -p "$PROJECT" -f "$DEMO/compose.yaml" --env-file "$ENV_FILE" "$@"
}

profiles() { if [ "${HTTP:-}" = "1" ]; then echo "--profile http"; fi; }

rand_hex() { openssl rand -hex "$1"; }

# write_env creates .env on first run (random local-only secrets) and keeps DEMO_PORT current.
write_env() {
  mkdir -p "$STATE"
  if [ ! -f "$ENV_FILE" ]; then
    umask 077
    {
      echo "# Written by make demo-up. Local demo secrets only; never commit or share this file."
      echo "DEEDGLASS_DEV_PG_ADMIN_PASSWORD=$(rand_hex 16)"
      echo "DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD=$(rand_hex 16)"
      echo "DEEDGLASS_DEV_PG_APP_PASSWORD=$(rand_hex 16)"
      echo "DEEDGLASS_VAULT_KEY=$(openssl rand -base64 32)"
      echo "DEEDGLASS_API_KEY_PEPPER=$(openssl rand -base64 32)"
      echo "ORDERS_DB_PASSWORD=$(rand_hex 16)"
      echo "ACME_OWNER_PASSWORD=$(rand_hex 16)"
      echo "DEMO_SEAL_KEY="
      echo "DEMO_PORT=$PORT"
    } > "$ENV_FILE"
    chmod 600 "$ENV_FILE"
  fi
  # DEMO_PORT follows the environment when given.
  if [ "$(envget DEMO_PORT)" != "$PORT" ]; then
    sed -i.bak "s/^DEMO_PORT=.*/DEMO_PORT=$PORT/" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
  fi
}

build_cli() {
  if command -v go >/dev/null 2>&1; then
    (cd "$ROOT" && go build -trimpath -o bin/deedglass ./cmd/deedglass) || fail "bin/deedglass didn't build. Run make build to see why."
  fi
  [ -x "$BIN" ] || fail "bin/deedglass is missing. Run make build, then make demo-up again."
}

# seal_key creates the demo's seal key once, readable by the container's user.
seal_key() {
  if [ -z "$(envget DEMO_SEAL_KEY)" ]; then
    mkdir -p "$STATE/keys"
    (cd "$STATE" && "$BIN" keys gen --dir "$STATE/keys" >/dev/null) || fail "The demo's seal key couldn't be created. Run bin/deedglass keys gen --dir $STATE/keys to see why."
    name="$(cd "$STATE/keys" && ls sk_*.json | sort | tail -1)"
    sed -i.bak "s/^DEMO_SEAL_KEY=.*/DEMO_SEAL_KEY=$name/" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
  fi
  chmod 700 "$STATE/keys"
  chmod 600 "$STATE/keys/"*.json   # Docker Desktop's file sharing lets the container read it
}

# host_env runs a deedglass command on this machine against the demo's database (DB-direct), from
# a folder with no .env or deedglass.yaml of its own.
host_env() {
  env -u DEEDGLASS_SERVER -u DEEDGLASS_API_KEY -u DEEDGLASS_BASE_URL \
    DEEDGLASS_ENV=dev \
    DEEDGLASS_DATABASE_URL="postgres://deedglass_app:$(envget DEEDGLASS_DEV_PG_APP_PASSWORD)@127.0.0.1:55433/deedglass?sslmode=disable" \
    DEEDGLASS_SEAL_KEY_FILE="$STATE/keys/$(envget DEMO_SEAL_KEY)" \
    DEEDGLASS_VAULT_KEY="$(envget DEEDGLASS_VAULT_KEY)" \
    DEEDGLASS_API_KEY_PEPPER="$(envget DEEDGLASS_API_KEY_PEPPER)" \
    DEEDGLASS_BASE_URL="http://127.0.0.1:$PORT" \
    "$@"
}

port_free() { ! lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

# ours reports whether the stack is already running (then its ports are ours, not a conflict).
ours() { [ -f "$ENV_FILE" ] && [ -n "$(compose ps -q deedglass 2>/dev/null)" ]; }

orders_sql() { compose exec -T orders-db psql -X -q -At -U postgres -d acme -c "$1"; }

reset_folder() {
  mkdir -p "$FOLDER/allowed"
  find "$FOLDER" -mindepth 1 -not -path "$FOLDER/allowed" -not -path "$FOLDER/allowed/hello.txt" -exec rm -rf {} + 2>/dev/null || true
  echo hello > "$FOLDER/allowed/hello.txt"
}

up() {
  echo "deedglass demo · Acme Store"
  need_docker
  if ours; then
    ok "Docker running · the demo stack is already up"
  else
    busy=""
    for p in "$PORT" 55433 55434; do port_free "$p" || busy="$busy $p"; done
    if [ "${HTTP:-}" = "1" ]; then port_free 3001 || busy="$busy 3001"; fi
    if [ -n "$busy" ]; then
      fail "Port${busy} in use (8080 is probably deedglass serve or make dev). Stop it, or run make demo-up DEMO_PORT=8091."
    fi
    ok "Docker running · ports $PORT, 55433, 55434 free"
  fi
  write_env
  build_cli
  seal_key
  mkdir -p "$STATE"
  # shellcheck disable=SC2046
  compose $(profiles) up -d --build --wait >"$LOG" 2>&1 || { tail -n 20 "$LOG" >&2; fail "The stack didn't start. The log is $LOG; make demo-status shows which part."; }
  counts="$(orders_sql "select (select count(*) from orders), (select count(*) from customers), (select count(*) from refunds)")"
  IFS='|' read -r n_orders n_customers n_refunds <<EOF
$counts
EOF
  ok "orders-db · $n_orders orders · $n_customers customers · $n_refunds refunds"
  ok "deedglass serve · http://127.0.0.1:$PORT · migrations applied"
  out="$(cd "$STATE" && host_env "$BIN" dev seed --demo --keys-file "$KEYS_FILE" 2>&1)" || { echo "$out" >&2; fail "Seeding failed. Run make demo-status, then make demo-up again."; }
  echo "$out"
  chmod 600 "$KEYS_FILE" 2>/dev/null || true
  reset_folder
  if [ "${HTTP:-}" = "1" ]; then ok "everything · http://127.0.0.1:3001/mcp · gateway at http://127.0.0.1:$PORT/mcp/everything"; fi
  echo "Demo ready. Next: make demo-snippet CLIENT=claude-desktop"
}

down() {
  [ -f "$ENV_FILE" ] || { ok "The demo stack isn't set up; nothing to stop."; return; }
  need_docker
  compose --profile http down >/dev/null 2>&1
  ok "The demo stack is stopped; its data is kept. make demo-up starts it again."
}

reset() {
  [ -f "$ENV_FILE" ] || fail "The demo stack isn't set up. Run make demo-up first."
  need_docker
  if [ "${HARD:-}" = "1" ]; then
    compose --profile http down -v >/dev/null 2>&1
    rm -f "$KEYS_FILE"
    ok "Ledger and orders volumes removed; starting again"
    up
    return
  fi
  compose exec -T orders-db psql -X -q -U postgres -d postgres -c "DROP DATABASE IF EXISTS acme WITH (FORCE)" -c "CREATE DATABASE acme" >/dev/null
  for f in 01_schema.sql 02_seed.sql 03_roles.sql; do
    compose exec -T orders-db sh -c "psql -X -q -U postgres -d acme -f /docker-entrypoint-initdb.d/$f" >/dev/null
  done
  reset_folder
  ok "orders-db reloaded · $(orders_sql "select count(*) from orders") orders · $(orders_sql "select count(*) from refunds") refunds · $(orders_sql "select count(*) from orders where status = 'cancelled'") cancelled"
  ok "$FOLDER reset · allowed/hello.txt"
  echo "Ready for a new take. The Ledger keeps every deed (HARD=1 wipes it)."
}

status() {
  st=0
  [ -f "$ENV_FILE" ] || { bad "Not set up. Run make demo-up."; exit 1; }
  need_docker
  for s in ledger-db orders-db deedglass; do
    h="$(docker inspect -f '{{.State.Health.Status}}' "$(compose ps -q "$s" 2>/dev/null)" 2>/dev/null || echo missing)"
    if [ "$h" = "healthy" ]; then ok "$s · healthy"; else bad "$s · $h"; st=1; fi
  done
  if [ -n "$(compose --profile http ps -q everything 2>/dev/null)" ]; then
    h="$(docker inspect -f '{{.State.Health.Status}}' "$(compose --profile http ps -q everything)" 2>/dev/null || echo missing)"
    if [ "$h" = "healthy" ]; then ok "everything · healthy"; else bad "everything · $h"; st=1; fi
  else
    pend "everything · off (make demo-up HTTP=1 starts it; /mcp/everything answers that it is down)"
  fi
  if curl -fsS "http://127.0.0.1:$PORT/v1/health" >/dev/null 2>&1; then ok "http://127.0.0.1:$PORT/v1/health"; else bad "http://127.0.0.1:$PORT/v1/health"; st=1; fi
  n="$(orders_sql "select count(*) from public.refunds" 2>/dev/null || echo "?")"
  if [ "$n" = "60" ]; then ok "refunds · 60 rows"; else bad "refunds · $n rows (make demo-reset reloads them)"; st=1; fi
  if [ -s "$KEYS_FILE" ]; then ok "agent keys · $KEYS_FILE"; else bad "agent keys · missing (make demo-up)"; st=1; fi
  pub="$(docker compose -p "$PROJECT" -f "$DEMO/compose.yaml" --env-file "$ENV_FILE" --profile http ps --format '{{.Publishers}}' 2>/dev/null | tr ' ' '\n' | grep -E 'URL:' | grep -v 'URL:127.0.0.1' || true)"
  if [ -z "$pub" ]; then ok "every port bound to 127.0.0.1"; else bad "a port is bound beyond 127.0.0.1: $pub"; st=1; fi
  exit $st
}

snippet() {
  [ -f "$ENV_FILE" ] || fail "Run make demo-up first."
  build_cli >/dev/null
  args="-client ${CLIENT:-claude-desktop} -port $PORT"
  if [ "${KEYS:-}" = "1" ]; then args="$args -keys"; fi
  # shellcheck disable=SC2086
  (cd "$ROOT" && go run ./examples/demo/snippet $args)
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  reset) reset ;;
  status) status ;;
  snippet) snippet ;;
  psql) need_docker; compose exec orders-db psql -U acme_owner -d acme ;;
  follow) build_cli >/dev/null; cd "$STATE"; host_env "$BIN" actions list --follow --connector "${CONNECTOR:-orders}" --org acme-demo ;;
  verify)
    build_cli >/dev/null; cd "$STATE"
    host_env "$BIN" deeds export --org acme-demo --since "${SINCE:-10m}" --out - | host_env "$BIN" verify - ;;
  *) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 2 ;;
esac
