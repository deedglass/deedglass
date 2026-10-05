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
#   demo.sh verify    make demo-verify [SINCE=10m]: export the chain (or a window) and verify it
#   demo.sh check     make demo-check: the Postgres connector's readiness report (S17)
#   demo.sh trigger-on   make demo-trigger-on: the hero's legacy trigger (S21; orders-db only)
#   demo.sh trigger-off  make demo-trigger-off: drop it
#   demo.sh order-reset  make demo-order-reset [ORDER=42]: one order back to paid, or every order
#                        back to the seed's status (as acme_owner)
#   demo.sh hero      make demo-hero: the L2 hero, VERIFIED then MISMATCH (examples/demo/scripts/hero.sh)
#   demo.sh diff      make demo-diff [ID=act_…]: deedglass actions show --diff (default: the newest
#                     postgres.execute action)
#   demo.sh approvals-on   make demo-approvals-on: enforce acme-demo-approvals.yaml (S22: Claude
#                          Desktop's writes to the orders database wait for a person)
#   demo.sh approvals-off  make demo-approvals-off: enforce acme-demo.yaml again
#   demo.sh approver  make demo-approver EMAIL=…: add EMAIL as an admin of the demo org (approval emails)
#   demo.sh lan       make demo-lan: serve on this Mac's Wi-Fi address so a phone can open approval
#                     links (DEEDGLASS_BASE_URL follows); anyone on the network can reach it
#   demo.sh lan-off   make demo-lan-off: back to 127.0.0.1 only, and approval emails back to the log
#
# Env: DEMO_PORT (default 8080, or what .env says), HTTP=1, HARD=1, CLIENT, KEYS=1, SINCE, ORDER, ID, NO_COLOR.
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

# compose runs docker compose on the demo stack. Two opt-ins live in .demo/ (S22): lan.yaml (make
# demo-lan) publishes Deedglass on the Wi-Fi address, and mail-on (make demo-approver) lets the
# server read DEEDGLASS_RESEND_API_KEY and DEEDGLASS_MAIL_FROM from the repo's .env, so approval
# emails reach a real inbox. Without mail-on, emails go to the server's log. The demo's own .env
# comes last, so its values win.
compose() {
  set -- -p "$PROJECT" -f "$DEMO/compose.yaml" "$@"
  if [ -f "$STATE/lan.yaml" ]; then set -- -f "$STATE/lan.yaml" "$@"; fi
  if [ -f "$STATE/mail-on" ] && [ -f "$ROOT/.env" ]; then
    docker compose --env-file "$ROOT/.env" --env-file "$ENV_FILE" "$@"
  else
    docker compose --env-file "$ENV_FILE" "$@"
  fi
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

# admin_key is the demo org's admin key for the CLI's API steps (S17), created once DB-direct and
# kept in .demo/admin-key (0600). A key the server no longer knows (after HARD=1) is replaced.
admin_key() {
  kf="$STATE/admin-key"
  if [ -s "$kf" ] && env DEEDGLASS_API_KEY="$(cat "$kf")" DEEDGLASS_SERVER="http://127.0.0.1:$PORT" "$BIN" connector list >/dev/null 2>&1; then
    cat "$kf"; return
  fi
  k="$(cd "$STATE" && host_env "$BIN" org keys create --org acme-demo --name demo-admin --mode test --json 2>/dev/null | sed -n 's/.*"secret": *"\(dg_[^"]*\)".*/\1/p')"
  [ -n "$k" ] || fail "The demo's admin key couldn't be created. Run make demo-status, then make demo-up again."
  umask 077; printf '%s' "$k" > "$kf"; chmod 600 "$kf"
  printf '%s' "$k"
}

api() { env DEEDGLASS_API_KEY="$(admin_key)" DEEDGLASS_SERVER="http://127.0.0.1:$PORT" "$BIN" "$@"; }

# postgres_up is S17's native Postgres connector on the orders database: the connector (once), then
# init --apply. The demo database is marked throwaway in its config, so no --confirm; the server
# reaches it as orders-db:5432 inside Docker. Passwords rotate on every up.
postgres_up() {
  if [ "$(orders_sql "select pg_catalog.to_regprocedure('public.ship_order(bigint)') is not null")" != "t" ]; then
    compose exec -T orders-db sh -c "psql -X -q -U postgres -d acme -f /docker-entrypoint-initdb.d/20_functions.sql" >/dev/null || fail "public.ship_order couldn't be created."
  fi
  if ! api connector show postgres >/dev/null 2>&1; then
    out="$(api connector create --config "$DEMO/connectors/postgres.yaml" 2>&1)" || { echo "$out" >&2; fail "The postgres connector couldn't be created."; }
  fi
  out="$(cd "$STATE" && DEEDGLASS_INIT_DSN="postgres://postgres:$(envget ORDERS_DB_PASSWORD)@127.0.0.1:55434/acme?sslmode=disable" \
    api connector postgres init --connector postgres --apply --rotate-passwords --dsn-host orders-db:5432 2>&1)" || { echo "$out" >&2; fail "deedglass connector postgres init failed. The log is above."; }
  echo "$out" > "$STATE/postgres-init.log"
  ok "postgres connector · deedglass_agent · credentials in the vault · make demo-check"
}

# orders_only refuses to touch any database but the demo's own orders-db (published on
# 127.0.0.1:55434): the hero's trigger must never reach anything else (S21 §7).
orders_only() {
  [ -f "$ENV_FILE" ] || fail "The demo stack isn't set up. Run make demo-up first."
  need_docker
  published="$(compose port orders-db 5432 2>/dev/null || true)"
  [ "$published" = "127.0.0.1:55434" ] || fail "The demo's orders-db isn't running on 127.0.0.1:55434 (found '${published:-nothing}'). Run make demo-up first."
}

orders_file() { compose exec -T -e PGOPTIONS='-c client_min_messages=warning' orders-db psql -X -q -v ON_ERROR_STOP=1 -U "${2:-postgres}" -d acme -f "/docker-entrypoint-initdb.d/$1" >/dev/null; }

trigger_on() {
  orders_only
  n="$(orders_sql "select count(*) from public.orders where id <> 42 and status <> 'shipped'")"
  if [ "${n:-0}" -lt 300 ]; then
    fail "The seed has only $n open orders besides 42; the hero needs 300. Run make demo-order-reset, or make demo-reset."
  fi
  orders_file 10_trigger_on.sql || fail "The trigger couldn't be created. Run make demo-psql and check public.orders."
  ok "Trigger on · orders_ship_open_batch · $n open orders besides 42"
}

trigger_off() {
  orders_only
  orders_file 11_trigger_off.sql || fail "The trigger couldn't be dropped. Run make demo-psql and check public.orders."
  ok "Trigger off · orders_ship_open_batch dropped"
}

order_reset() {
  orders_only
  if [ -n "${ORDER:-}" ]; then
    case "$ORDER" in *[!0-9]*|'') fail "ORDER must be an order id, like ORDER=42." ;; esac
    compose exec -T orders-db psql -X -q -v ON_ERROR_STOP=1 -U acme_owner -d acme \
      -c "UPDATE public.orders SET status = 'paid' WHERE id = $ORDER AND status IS DISTINCT FROM 'paid'" >/dev/null ||
      fail "Order $ORDER couldn't be reset. Run make demo-psql."
    ok "Order $ORDER · paid"
    return
  fi
  orders_file 12_order_statuses.sql acme_owner || fail "The orders couldn't be reset. Run make demo-reset."
  ok "Orders reset · $(orders_sql "select count(*) from public.orders where status <> 'shipped' and id <> 42") open besides 42 · order 42 paid"
}

# decoder_running waits (up to 30 s) until the decoder reads the orders database's slot.
decoder_running() {
  for _ in $(seq 1 60); do
    if api decoder status --connector postgres --json 2>/dev/null | grep -q '"status": *"running"'; then return 0; fi
    sleep 0.5
  done
  return 1
}

# decoder_fresh: the orders database was just recreated, so its replication slot went with it.
# Record that (decoder drop seals an admin deed) and let the decoder start a new slot (S21).
decoder_fresh() {
  api decoder drop --connector postgres --confirm postgres >/dev/null 2>&1 || true
  if decoder_running; then ok "decoder · running on a new slot"; else bad "decoder · not running yet (deedglass decoder status says why)"; fi
}

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
  postgres_up
  if decoder_running; then ok "decoder · running · Postgres's change log is witnessed (L2)"; else bad "decoder · not running yet (deedglass decoder status says why)"; fi
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
    rm -f "$KEYS_FILE" "$STATE/admin-key"
    ok "Ledger and orders volumes removed; starting again"
    up
    return
  fi
  compose exec -T orders-db psql -X -q -U postgres -d postgres -c "DROP DATABASE IF EXISTS acme WITH (FORCE)" -c "CREATE DATABASE acme" >/dev/null
  for f in 01_schema.sql 02_seed.sql 03_roles.sql 20_functions.sql; do
    compose exec -T orders-db sh -c "psql -X -q -U postgres -d acme -f /docker-entrypoint-initdb.d/$f" >/dev/null
  done
  reset_folder
  ok "orders-db reloaded · $(orders_sql "select count(*) from orders") orders · $(orders_sql "select count(*) from refunds") refunds · $(orders_sql "select count(*) from orders where status = 'cancelled'") cancelled"
  ok "$FOLDER reset · allowed/hello.txt"
  build_cli >/dev/null
  postgres_up
  decoder_fresh
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

# activate_policy pushes a demo policy file (a new acme-demo version only when it changed) and
# enforces that version, with its activation deed.
activate_policy() {
  file="$1"
  out="$(api --json policy push "$file" 2>&1)" || { echo "$out" >&2; fail "The policy couldn't be pushed. The error is above."; }
  v="$(printf '%s' "$out" | sed -n 's/.*"version": *\([0-9][0-9]*\).*/\1/p' | head -1)"
  [ -n "$v" ] || fail "The pushed version couldn't be read. Run bin/deedglass policy push $file to see why."
  out="$(api policy activate acme-demo --version "$v" --mode enforce --reason "$2" --yes 2>&1)" || { echo "$out" >&2; fail "The policy couldn't be activated."; }
  echo "$v"
}

approvals_on() {
  [ -f "$ENV_FILE" ] || fail "Run make demo-up first."
  build_cli >/dev/null
  v="$(activate_policy "$DEMO/policies/acme-demo-approvals.yaml" "S22 approvals demo: Claude Desktop's writes wait for a person")"
  ok "Approvals on · acme-demo v$v (acme-demo-approvals.yaml) · Claude Desktop's writes wait for a person"
}

approvals_off() {
  [ -f "$ENV_FILE" ] || fail "Run make demo-up first."
  build_cli >/dev/null
  v="$(activate_policy "$DEMO/policies/acme-demo.yaml" "Back to the demo policy (make demo-approvals-off)")"
  ok "Approvals off · acme-demo v$v (acme-demo.yaml)"
}

approver() {
  [ -f "$ENV_FILE" ] || fail "Run make demo-up first."
  [ -n "${EMAIL:-}" ] || fail "Say whose email: make demo-approver EMAIL=you@deedglass.com"
  need_docker
  build_cli >/dev/null
  (cd "$STATE" && host_env "$BIN" dev seed --demo --keys-file "$KEYS_FILE" --approver "$EMAIL") || fail "The approver couldn't be added. The error is above."
  touch "$STATE/mail-on"
  compose up -d --wait deedglass >>"$LOG" 2>&1 || { tail -n 20 "$LOG" >&2; fail "Deedglass didn't restart. The log is $LOG."; }
  ok "Real email on for the demo · the server reads the Resend key and sender from the repo's .env (make demo-lan-off turns it off)"
  pend "Without them in .env, approval emails go to the server's log: docker compose -p $PROJECT logs deedglass"
}

lan_ip() { ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true; }

lan() {
  [ -f "$ENV_FILE" ] || fail "Run make demo-up first."
  need_docker
  ip="$(lan_ip)"
  [ -n "$ip" ] || fail "This Mac has no Wi-Fi address (en0/en1). Join the same Wi-Fi as your phone, then run make demo-lan again."
  mkdir -p "$STATE"
  cat > "$STATE/lan.yaml" <<YAML
# Written by make demo-lan (removed by make demo-lan-off). Publishes Deedglass on this Mac's
# Wi-Fi address so a phone can open approval links.
services:
  deedglass:
    ports: !override
      - "$ip:$PORT:8080"
    environment:
      DEEDGLASS_BASE_URL: "http://$ip:$PORT"
YAML
  compose up -d --wait deedglass >>"$LOG" 2>&1 ||
    { tail -n 20 "$LOG" >&2; fail "Deedglass didn't restart on $ip. The log is $LOG."; }
  ok "Deedglass on http://$ip:$PORT · approval links in new emails point here"
  pend "Anyone on this Wi-Fi can reach it while it is on. Run make demo-lan-off when you're done."
}

lan_off() {
  [ -f "$ENV_FILE" ] || fail "Run make demo-up first."
  need_docker
  rm -f "$STATE/lan.yaml" "$STATE/mail-on"
  compose up -d --wait deedglass >>"$LOG" 2>&1 || { tail -n 20 "$LOG" >&2; fail "Deedglass didn't restart. The log is $LOG."; }
  ok "Deedglass back on http://127.0.0.1:$PORT only · approval emails go to the server's log again"
}

case "${1:-}" in
  approvals-on) approvals_on ;;
  approvals-off) approvals_off ;;
  approver) approver ;;
  lan) lan ;;
  lan-off) lan_off ;;
  up) up ;;
  down) down ;;
  reset) reset ;;
  status) status ;;
  snippet) snippet ;;
  psql) need_docker; compose exec orders-db psql -U acme_owner -d acme ;;
  follow) build_cli >/dev/null; cd "$STATE"; host_env "$BIN" actions list --follow --connector "${CONNECTOR:-orders}" --org acme-demo ;;
  check) build_cli >/dev/null; cd "$STATE"; api connector postgres check --connector postgres ;;
  trigger-on) trigger_on ;;
  trigger-off) trigger_off ;;
  order-reset) order_reset ;;
  decoder-wait) decoder_running || fail "The decoder isn't running. Run deedglass decoder status (make demo-check), or make demo-reset." ;;
  hero) exec "$DEMO/scripts/hero.sh" ;;
  diff)
    build_cli >/dev/null; cd "$STATE"
    id="${ID:-}"
    if [ -z "$id" ]; then
      id="$(host_env "$BIN" actions list --org acme-demo --tool postgres.execute --limit 1 --json | grep -o '"id": *"act_[0-9a-z]*"' | head -1 | grep -o 'act_[0-9a-z]*' || true)"
    fi
    [ -n "$id" ] || fail "No postgres.execute action yet. Ask the agent to ship order 42 first."
    host_env "$BIN" actions show "$id" --diff --org acme-demo ;;
  verify)
    build_cli >/dev/null; cd "$STATE"
    # The whole chain by default (S34): a window that starts after deed 1 can't verify on its own
    # without --anchor. SINCE=10m keeps the short window for a fresh stack.
    if [ -n "${SINCE:-}" ]; then
      host_env "$BIN" deeds export --org acme-demo --since "$SINCE" --out - | host_env "$BIN" verify -
    else
      host_env "$BIN" deeds export --org acme-demo --out - | host_env "$BIN" verify -
    fi ;;
  *) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 2 ;;
esac
