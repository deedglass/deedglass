#!/usr/bin/env bash
# The L2 hero (S21), as one command: make demo-hero. Bash 3.2 compatible (macOS).
#
# An agent ships order 42 with one UPDATE, and the tool says 1 row. With the demo's legacy trigger
# on, Postgres commits 301. Deedglass reads Postgres's change log and seals the difference:
#
#   1. trigger off, every order back to the seed, the UPDATE   → L2 · VERIFIED
#   2. trigger on, order 42 back to paid, the same UPDATE       → L2 · MISMATCH · 301 rows
#   3. deedglass actions show <id> --diff                       → what Postgres committed, row by row
#   4. the org's deeds exported and verified against the server's published keys
#   5. trigger off
#
# Deterministic and idempotent: every run starts from the seed's statuses, so it can run twice in a
# row. Every check prints ✓ or ✕ and a failed one exits 1. Real engine runs only (CLAUDE.md
# rule 12): nothing here is mocked.
#
# Env: DEMO_PORT (from examples/demo/.env), HERO_PACE (seconds between beats when recording;
# default 0), HERO_AGENT (default claude-code), NO_COLOR.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
DEMO="$ROOT/examples/demo"
ENV_FILE="$DEMO/.env"
KEYS_FILE="$DEMO/.demo-keys"
STATE="$DEMO/.demo"
BIN="$ROOT/bin/deedglass"
SH="$DEMO/scripts/demo.sh"
PACE="${HERO_PACE:-0}"
AGENT="${HERO_AGENT:-claude-code}"

TEAL="" RED="" DIM="" BOLD="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  TEAL=$'\033[36m' RED=$'\033[31m' DIM=$'\033[2m' BOLD=$'\033[1m' RESET=$'\033[0m'
fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }
beat() { printf '\n%s%s%s\n' "$BOLD" "$*" "$RESET"; }
pause() { if [ "$PACE" != "0" ]; then sleep "$PACE"; fi; }
cmdline() { printf '%s$%s %s\n' "$DIM" "$RESET" "$*"; }

envget() { [ -f "$ENV_FILE" ] && grep -E "^$1=" "$ENV_FILE" | head -1 | cut -d= -f2- || true; }
[ -f "$ENV_FILE" ] || fail "The demo stack isn't set up. Run make demo-up first."
PORT="${DEMO_PORT:-$(envget DEMO_PORT)}"
PORT="${PORT:-8080}"
SERVER="http://127.0.0.1:$PORT"
KEY="$(grep -E "^$AGENT=" "$KEYS_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true)"
[ -n "$KEY" ] || fail "No key for the $AGENT agent in examples/demo/.demo-keys. Run make demo-up."
curl -fsS "$SERVER/v1/health" >/dev/null 2>&1 || fail "Deedglass isn't answering on $SERVER. Run make demo-status."
(cd "$ROOT" && go build -trimpath -o bin/deedglass ./cmd/deedglass) || fail "bin/deedglass didn't build. Run make build to see why."
# S22: the hero runs with approvals off (acme-demo.yaml's allow-order-status-updates), so it never
# waits for a person.
"$SH" approvals-off >/dev/null || fail "make demo-approvals-off failed. Run it to see why."

# host_env runs a DB-direct command against the demo's own database (demo.sh's host_env).
host_env() {
  env -u DEEDGLASS_SERVER -u DEEDGLASS_API_KEY -u DEEDGLASS_BASE_URL \
    DEEDGLASS_ENV=dev \
    DEEDGLASS_DATABASE_URL="postgres://deedglass_app:$(envget DEEDGLASS_DEV_PG_APP_PASSWORD)@127.0.0.1:55433/deedglass?sslmode=disable" \
    DEEDGLASS_SEAL_KEY_FILE="$STATE/keys/$(envget DEMO_SEAL_KEY)" \
    DEEDGLASS_VAULT_KEY="$(envget DEEDGLASS_VAULT_KEY)" \
    DEEDGLASS_API_KEY_PEPPER="$(envget DEEDGLASS_API_KEY_PEPPER)" \
    DEEDGLASS_BASE_URL="$SERVER" \
    "$@"
}

SQL="UPDATE public.orders SET status = 'shipped' WHERE id = 42"
ARGS="{\"sql\":\"$SQL\"}"

# act runs the agent's UPDATE through the Gate and prints it as the CLI does (exit 1 on MISMATCH
# is expected in beat 2).
act() {
  cmdline "deedglass act postgres.execute --args '$ARGS'"
  env DEEDGLASS_API_KEY="$KEY" DEEDGLASS_SERVER="$SERVER" "$BIN" act postgres.execute --args "$ARGS" || true
}

# latest prints the newest postgres.execute action's id, level, verdict and summary, tab-separated.
latest() {
  (cd "$STATE" && host_env "$BIN" actions list --org acme-demo --tool postgres.execute --limit 1 --json) |
    awk '
      /"id": "act_/ && id == "" { sub(/.*"id": "/, ""); sub(/".*/, ""); id = $0 }
      /"level": "L/ && level == "" { sub(/.*"level": "/, ""); sub(/".*/, ""); level = $0 }
      /"verdict": "/ && verdict == "" { sub(/.*"verdict": "/, ""); sub(/".*/, ""); verdict = $0 }
      END { printf "%s\t%s\t%s\n", id, level, verdict }'
}

"$SH" decoder-wait   # L2 needs the decoder reading the orders database's slot

if make -C "$ROOT" -n demo-approvals-off >/dev/null 2>&1; then
  make -C "$ROOT" -s demo-approvals-off   # S22's toggle: the hero runs without a pause for a person
fi

beat "1. Trigger off. Ship order 42."
"$SH" trigger-off
"$SH" order-reset
pause
act
IFS=$'\t' read -r id1 level1 verdict1 <<EOF
$(latest)
EOF
[ "$level1 $verdict1" = "L2 VERIFIED" ] || fail "Expected L2 · VERIFIED for $id1, got $level1 · $verdict1. Run make demo-status and deedglass decoder status."
ok "L2 · VERIFIED · $id1"
pause

beat "2. A legacy trigger is on. Ship order 42 again."
"$SH" trigger-on
ORDER=42 "$SH" order-reset
pause
act
IFS=$'\t' read -r id2 level2 verdict2 <<EOF
$(latest)
EOF
[ "$id2" != "$id1" ] || fail "No new action was recorded. Run make demo-follow to see what happened."
[ "$level2 $verdict2" = "L2 MISMATCH" ] || fail "Expected L2 · MISMATCH for $id2, got $level2 · $verdict2."
show="$(cd "$STATE" && host_env "$BIN" actions show "$id2" --org acme-demo --json)"
case "$show" in
  *"Postgres changed 301 rows in public.orders. The agent reported 1."*) ok "L2 · MISMATCH · 301 rows · $id2" ;;
  *) fail "The MISMATCH for $id2 doesn't say 301 rows. Run deedglass actions show $id2 --diff." ;;
esac
pause

beat "3. What Postgres committed."
cmdline "deedglass actions show $id2 --diff"
(cd "$STATE" && host_env "$BIN" actions show "$id2" --diff --org acme-demo)
pause

beat "4. Every deed checks out offline."
# The whole chain: an export that starts after deed 1 would need an anchor to verify.
cmdline "deedglass deeds export --out - | deedglass verify - --keys $SERVER/.well-known/deedglass/keys.json"
out="$(cd "$STATE" && host_env "$BIN" deeds export --org acme-demo --out - | "$BIN" verify - --keys "$SERVER/.well-known/deedglass/keys.json" 2>&1)" ||
  { printf '%s\n' "$out"; fail "The exported deeds didn't verify."; }
printf '%s\n' "$out"
case "$out" in *"chain intact"*) ;; *) fail "verify didn't report an intact chain." ;; esac
pause

beat "5. Trigger off."
"$SH" trigger-off
ok "Hero done · VERIFIED $id1 · MISMATCH $id2"
