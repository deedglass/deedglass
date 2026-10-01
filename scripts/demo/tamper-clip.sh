#!/usr/bin/env bash
# tamper-clip.sh: the S06 recording, from a real engine run. It seals 1,000 fake actions as real
# deeds in a fresh org, exports them, fetches the server's public keys, verifies (✓), changes one
# digit in deed 812, and verifies again (✕). Every command is echoed after a "$ " prompt with a
# 1.2 s pause, so the screen recording reads at a calm pace. The last line is
#
#   ✕ Deed 812 was changed after it was sealed.
#
# Needs: a dev database and a running server (make dev, or scripts/dev-pg.sh up and
# `deedglass serve` in another terminal), and bin/deedglass (make build).
#   DEEDGLASS_SERVER  default http://127.0.0.1:8080
#   CLIP_N, CLIP_SEQ, CLIP_SEED, CLIP_PAUSE   default 1000, 812, 42, 1.2
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SERVER="${DEEDGLASS_SERVER:-http://127.0.0.1:8080}"
N="${CLIP_N:-1000}" SEQ="${CLIP_SEQ:-812}" SEED="${CLIP_SEED:-42}" PAUSE="${CLIP_PAUSE:-1.2}"

fail() { printf '✕ %s\n' "$1" >&2; [ -n "${2:-}" ] && printf '  next  %s\n' "$2" >&2; exit 1; }

# The environment: the dev database (from .tmp/dev-pg.env or .env when not already set).
if [ -z "${DEEDGLASS_DATABASE_URL:-}" ]; then
  for f in "$ROOT/.tmp/dev-pg.env" "$ROOT/.env"; do
    if [ -f "$f" ]; then set -a; . "$f"; set +a; break; fi
  done
fi
export DEEDGLASS_ENV=dev
[ -n "${DEEDGLASS_DATABASE_URL:-}" ] || fail "DEEDGLASS_DATABASE_URL isn't set." "make dev, or scripts/dev-pg.sh up"
[ -x "$ROOT/bin/deedglass" ] || fail "bin/deedglass isn't built." "make build"
curl -sf "$SERVER/v1/health" >/dev/null || fail "No server at $SERVER." "deedglass serve (in another terminal)"
# Seal with the key the server uses, whatever directory the clip runs in.
if [ -z "${DEEDGLASS_SEAL_KEY_FILE:-}" ]; then
  key="$(ls -1 "$ROOT"/.deedglass/keys/sk_*.json 2>/dev/null | sort | tail -n 1 || true)"
  [ -n "$key" ] || fail "No dev seal key in .deedglass/keys." "Start the server once from $ROOT: deedglass serve"
  export DEEDGLASS_SEAL_KEY_FILE="$key"
fi

ORG="clip-$(date +%H%M%S)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"
export PATH="$ROOT/bin:$ROOT/scripts/demo:$PATH"

# show prints a command after a "$ " prompt, pauses, runs it, pauses.
show() {
  printf '$ %s\n' "$1"
  sleep "$PAUSE"
  eval "$2" || true
  sleep "$PAUSE"
}
# headline runs a verify on a pseudo-terminal (so it keeps its colours) and prints its first line.
headline() {
  if [ -t 0 ] && [ -t 1 ]; then
    script -q /dev/null "$@" | tr -d '\r' | grep -v '◐' | head -n 1
  else
    "$@" 2>&1 | head -n 1
  fi
}

# Every action ends in a deed (no pending approvals, no amendments), so N actions are N deeds.
FAKE="deedglass dev fake-action --n $N --seed $SEED --kinds allow,deny,mismatch,unverifiable,failed --org-slug $ORG --batch 100"
show "$FAKE" "$FAKE"
show "deedglass deeds export --org $ORG --format jsonl --out deeds.jsonl" \
     "deedglass deeds export --org $ORG --format jsonl --out deeds.jsonl"
show "curl -s $SERVER/.well-known/deedglass/keys.json -o keys.json" \
     "curl -s $SERVER/.well-known/deedglass/keys.json -o keys.json"
show "deedglass verify deeds.jsonl --keys keys.json | head -1" \
     "headline deedglass verify deeds.jsonl --keys keys.json"
show "flip-byte.sh deeds.jsonl $SEQ" \
     "flip-byte.sh deeds.jsonl $SEQ"
printf '$ %s\n' "deedglass verify deeds.jsonl --keys keys.json | head -1"
sleep "$PAUSE"
headline deedglass verify deeds.jsonl --keys keys.json || true # the ✕ is the point; the clip itself succeeded
