#!/usr/bin/env bash
# S08's Stop clip, the left terminal: call GET /v1/deeds?limit=1 with the agent's key every 200 ms
# and print one line per call. (whoami skips the stop checks on purpose, so the loop uses a route
# that enforces them.) In the right terminal: deedglass agent stop <name> --reason "…".
#
#   AGENT_KEY=dg_live_… scripts/demo/stop-clip.sh [server]     (default http://127.0.0.1:8080)
#
# The key comes from the environment only, never an argument, so it stays out of shell history
# and process lists. Ctrl-C ends the loop.
set -uo pipefail
server="${1:-${DEEDGLASS_SERVER:-http://127.0.0.1:8080}}"
: "${AGENT_KEY:?Set AGENT_KEY to the key that deedglass agent keys create printed.}"
TEAL="" RED="" DIM="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' DIM=$'\033[2m' RESET=$'\033[0m'; fi
auth=(-H "Authorization: Bearer $AGENT_KEY")
# The first "name" after "agent" in whoami, and the first "code" in an error envelope. (Plain
# functions: macOS's bash 3.2 misparses parentheses inside $( ) even when they're quoted.)
agent_name() { tr -d '\n' | sed -n 's/.*"agent": *{[^}]*"name": *"\([^"]*\)".*/\1/p'; }
error_code() { tr -d '\n' | sed -n 's/.*"code": *"\([a-z_]*\)".*/\1/p'; }
name="$(curl -s "${auth[@]}" "$server/v1/whoami" | agent_name)"
name="${name:-agent}"
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
while :; do
  code="$(curl -s -o "$tmp" -w '%{http_code}' "${auth[@]}" "$server/v1/deeds?limit=1")"
  if [ "$code" = "200" ]; then
    printf '%s✓%s %s %s·%s 200\n' "$TEAL" "$RESET" "$name" "$DIM" "$RESET"
  else
    err="$(error_code < "$tmp")"
    printf '%s✕%s %s %s·%s %s\n' "$RED" "$RESET" "${err:-http_$code}" "$DIM" "$RESET" "$code"
  fi
  sleep 0.2
done
