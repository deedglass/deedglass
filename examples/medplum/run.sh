#!/usr/bin/env bash
# The Medplum example end to end (examples/medplum/README.md): Medplum 5.2.6 on PostgreSQL 18 in
# Docker, Deedglass from this tree on this machine, an agent-style client and a Medplum Bot handler
# writing through the gate. Synthetic data only. Every port is on 127.0.0.1; every secret is made up
# here and kept in examples/medplum/.env and .env.keys (0600, git-ignored); nothing is printed.
#
#   examples/medplum/run.sh          (make medplum-example) start fresh, run (a), (b), (c) and the bot, remove everything
#   KEEP=1 examples/medplum/run.sh   leave Medplum and Deedglass running afterwards (prints how to use and stop them)
#   APPROVE=manual …                 don't countersign (c) for you: approve it yourself in another terminal
#   examples/medplum/run.sh down     remove the containers, volumes and state
#
# Ports (override with the variables): Medplum MEDPLUM_PORT=8103, its database MEDPLUM_DB_PORT=55441,
# Deedglass's database LEDGER_DB_PORT=55442, Deedglass DEEDGLASS_PORT=8094.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
STATE="$ROOT/.tmp/medplum-example"
ENV_FILE="$HERE/.env"
KEYS_FILE="$HERE/.env.keys"
export MEDPLUM_PORT="${MEDPLUM_PORT:-8103}" MEDPLUM_DB_PORT="${MEDPLUM_DB_PORT:-55441}" LEDGER_DB_PORT="${LEDGER_DB_PORT:-55442}"
DEEDGLASS_PORT="${DEEDGLASS_PORT:-8094}"
OWNER="owner@clinic.test"

TEAL="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' RESET=$'\033[0m'; fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }
compose() { docker compose -f "$HERE/compose.yaml" --env-file "$ENV_FILE" "$@"; }
envget() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -1; }
keyget() { sed -n "s/^$1=//p" "$KEYS_FILE" | tail -1; }

down() {
  [ -f "$ENV_FILE" ] && compose down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$STATE" "$KEYS_FILE" "$ENV_FILE" "$HERE/node_modules"
}
if [ "${1:-}" = down ]; then down; ok "Removed the Medplum example's containers, volumes and state."; exit 0; fi

command -v docker >/dev/null || fail "docker not found. Install Docker Desktop, then run this again."
docker info >/dev/null 2>&1 || fail "The Docker daemon isn't running. Start Docker Desktop, then run this again."
command -v node >/dev/null || fail "node not found. Install Node.js 24, then run this again."
for p in "$MEDPLUM_PORT" "$MEDPLUM_DB_PORT" "$LEDGER_DB_PORT" "$DEEDGLASS_PORT"; do
  case "$p" in 8080|55432) fail "Port $p is reserved for your own Deedglass dev stack. Pick another with the port variables." ;; esac
done
df -h "$ROOT" | tail -1 | awk '{ print "disk: " $4 " free on " $NF }'

# A fresh start every run, so the output is the same each time.
down
for p in "$MEDPLUM_PORT" "$MEDPLUM_DB_PORT" "$LEDGER_DB_PORT" "$DEEDGLASS_PORT"; do
  ! lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1 || fail "Port $p is in use. Free it or pick another with the port variables (see the top of this script)."
done
mkdir -p "$STATE"
(
  umask 077
  hex() { openssl rand -hex 16; }
  {
    for k in MEDPLUM_DB_PASSWORD REDIS_PASSWORD MEDPLUM_ADMIN_PASSWORD WITNESS_PASSWORD LEDGER_DB_PASSWORD DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD DEEDGLASS_DEV_PG_APP_PASSWORD; do echo "$k=$(hex)"; done
    echo "DEEDGLASS_VAULT_KEY=$(openssl rand -base64 32)"
    echo "DEEDGLASS_API_KEY_PEPPER=$(openssl rand -base64 32)"
  } > "$ENV_FILE"
)

DG="${DEEDGLASS_BIN:-$STATE/deedglass}"
if [ -z "${DEEDGLASS_BIN:-}" ]; then
  command -v go >/dev/null || fail "go not found. Install Go, or set DEEDGLASS_BIN to a deedglass binary."
  (cd "$ROOT" && go build -o "$DG" ./cmd/deedglass) || fail "deedglass didn't build. Run go build ./cmd/deedglass to see why."
fi
SDK="${DEEDGLASS_SDK_DIR:-$ROOT/sdk/typescript}"
[ -f "$SDK/dist/index.js" ] || fail "The TypeScript SDK isn't built. Run make sdk-ts (or: cd sdk/typescript && npm ci && npm run build), then run this again."
unset DEEDGLASS_BIN DEEDGLASS_SDK_DIR   # read above; deedglass itself would warn about unknown DEEDGLASS_ names
mkdir -p "$HERE/node_modules/@deedglass"
ln -sfn "$SDK" "$HERE/node_modules/@deedglass/sdk"

echo "Starting Medplum (the first start creates its schema, about a minute)…"
compose up -d --wait >/dev/null 2>&1 || { compose logs --tail 40 medplum >&2 || true; fail "The containers didn't start. Run docker compose -f examples/medplum/compose.yaml --env-file examples/medplum/.env logs to see why."; }
MEDPLUM="http://127.0.0.1:$MEDPLUM_PORT"
ok "Medplum 5.2.6 on PostgreSQL 18.6: $MEDPLUM/fhir/R4"

# What Medplum's database owner does once for Proven (docs/connectors/fhir.md): a role that can
# decode, and a publication limited to the history tables of the types the connector allows.
compose exec -T medplum-db psql -U postgres -d medplum -v ON_ERROR_STOP=1 -q \
  -v pw="$(envget WITNESS_PASSWORD)" >/dev/null <<'SQL'
CREATE ROLE deedglass_witness LOGIN REPLICATION PASSWORD :'pw';
CREATE PUBLICATION deedglass_pub FOR TABLE "Observation_History", "Patient_History";
SQL
ok "witness role and publication on Medplum's database"

# Deedglass from this tree, on this machine, in dev mode.
export DEEDGLASS_ENV=dev DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1 DEEDGLASS_LOG_LEVEL=info
DEEDGLASS_DATABASE_URL="postgres://deedglass_app:$(envget DEEDGLASS_DEV_PG_APP_PASSWORD)@127.0.0.1:$LEDGER_DB_PORT/deedglass?sslmode=disable"
DEEDGLASS_MIGRATE_DATABASE_URL="postgres://deedglass_migrator:$(envget DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD)@127.0.0.1:$LEDGER_DB_PORT/deedglass?sslmode=disable"
DEEDGLASS_VAULT_KEY="$(envget DEEDGLASS_VAULT_KEY)"
DEEDGLASS_API_KEY_PEPPER="$(envget DEEDGLASS_API_KEY_PEPPER)"
export DEEDGLASS_DATABASE_URL DEEDGLASS_MIGRATE_DATABASE_URL DEEDGLASS_VAULT_KEY DEEDGLASS_API_KEY_PEPPER
export DEEDGLASS_LISTEN="127.0.0.1:$DEEDGLASS_PORT" DEEDGLASS_BASE_URL="http://127.0.0.1:$DEEDGLASS_PORT" DEEDGLASS_SERVER="http://127.0.0.1:$DEEDGLASS_PORT"
unset DEEDGLASS_API_KEY
(cd "$STATE" && "$DG" keys gen --dir "$STATE/keys" >/dev/null)
DEEDGLASS_SEAL_KEY_FILE="$(find "$STATE/keys" -name 'sk_*.json' | sort | tail -1)"
export DEEDGLASS_SEAL_KEY_FILE
(cd "$STATE" && "$DG" migrate up >/dev/null) || fail "deedglass migrate up failed."
(cd "$STATE" && exec "$DG" serve >"$STATE/serve.log" 2>&1) &
SERVE_PID=$!
cleanup() {
  code=$?
  if [ "${KEEP:-}" = 1 ] && [ "$code" = 0 ]; then
    echo
    echo "Kept running. Deedglass: $DEEDGLASS_SERVER (pid $SERVE_PID, log .tmp/medplum-example/serve.log) · Medplum: $MEDPLUM"
    echo "The agent key and ids are in examples/medplum/.env.keys. Stop everything with: kill $SERVE_PID; examples/medplum/run.sh down"
    return
  fi
  kill "$SERVE_PID" 2>/dev/null || true
  wait "$SERVE_PID" 2>/dev/null || true
  [ "$code" = 0 ] || { echo "The server's last lines:" >&2; tail -20 "$STATE/serve.log" >&2 || true; }
  down
}
trap cleanup EXIT
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$DEEDGLASS_SERVER/v1/health" 2>/dev/null && break
  kill -0 "$SERVE_PID" 2>/dev/null || fail "deedglass serve stopped while starting."
  sleep 0.5
done
ok "Deedglass (this tree) on $DEEDGLASS_SERVER"

ADMIN_KEY="$(cd "$STATE" && "$DG" org create --name "Synthetic Clinic" --slug clinic --owner-email "$OWNER" --admin-key --json \
  | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>process.stdout.write(JSON.parse(s).admin_key.secret))')"
[ -n "$ADMIN_KEY" ] || fail "org create returned no admin key."
export DEEDGLASS_API_KEY="$ADMIN_KEY"
(cd "$STATE" && "$DG" policy push "$HERE/policy.yaml" >/dev/null && "$DG" policy activate medplum-example --version 1 --mode enforce --reason "Medplum example" --yes >/dev/null) \
  || fail "The example policy didn't activate."
ok "policy medplum-example active (examples/medplum/policy.yaml)"

MEDPLUM_URL="$MEDPLUM" MEDPLUM_ADMIN_EMAIL=admin@example.test MEDPLUM_ADMIN_PASSWORD="$(envget MEDPLUM_ADMIN_PASSWORD)" \
DEEDGLASS_ADMIN_KEY="$ADMIN_KEY" \
MEDPLUM_WITNESS_DSN="postgres://deedglass_witness:$(envget WITNESS_PASSWORD)@127.0.0.1:$MEDPLUM_DB_PORT/medplum?sslmode=disable" \
  node --no-warnings "$HERE/setup.ts"

# A person countersigns (c) while the agent waits: on a dev server, deedglass approve --as.
if [ "${APPROVE:-auto}" = auto ]; then
  (
    for _ in $(seq 1 240); do
      apr="$(cd "$STATE" && "$DG" approvals list --status pending --json 2>/dev/null | grep -o 'apr_[0-9a-z]\{26\}' | head -1 || true)"
      if [ -n "$apr" ]; then
        sleep 1
        (cd "$STATE" && "$DG" approve "$apr" --as "$OWNER" --reason "Checked the change: status preliminary to final." >/dev/null)
        exit 0
      fi
      sleep 0.5
    done
  ) &
  APPROVER_PID=$!
else
  echo "(c) waits for you: deedglass approvals list, then deedglass approve <apr_…> --as $OWNER (DEEDGLASS_SERVER=$DEEDGLASS_SERVER, an admin key)."
fi

echo
echo "$ node examples/medplum/agent.ts"
DEEDGLASS_API_KEY="$(keyget DEEDGLASS_AGENT_KEY)" MEDPLUM_PATIENT_ID="$(keyget MEDPLUM_PATIENT_ID)" node --no-warnings "$HERE/agent.ts"
if [ -n "${APPROVER_PID:-}" ]; then kill "$APPROVER_PID" 2>/dev/null || true; wait "$APPROVER_PID" 2>/dev/null || true; fi

echo
echo "$ node examples/medplum/bot/run-local.ts"
DEEDGLASS_API_KEY="$(keyget DEEDGLASS_AGENT_KEY)" MEDPLUM_PATIENT_ID="$(keyget MEDPLUM_PATIENT_ID)" node --no-warnings "$HERE/bot/run-local.ts"

echo
echo "$ deedglass actions list --connector medplum"
(cd "$STATE" && NO_COLOR=1 "$DG" actions list --connector medplum --limit 10)
